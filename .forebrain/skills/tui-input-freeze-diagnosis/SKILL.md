---
name: tui-input-freeze-diagnosis
description: 复现并定位只在裸终端出现的 TUI 输入冻结/卡死（tmux 与单测都不复现的那种）：限速排水 pty 沉箱、真模型活动 run、滚轮洪峰、输入管线分段探针、SIGQUIT goroutine 栈取证。Use when a TUI hang reproduces only in a real terminal (iTerm2/Terminal.app), never under tmux or go test.
---

# TUI 输入冻结定位（裸终端专属问题）

先分流：

1. **tmux 里能复现** → 直接用 `run-forebrain` 的 driver.sh，不需要本 skill。
2. **只有裸终端复现**（典型指纹：滚轮/键盘/点击全死、屏幕仍在流式更新、
   过一会自己恢复）→ 终端排水停顿类，用本 skill。

核心事实（为什么 tmux 复现不出）：tmux 与 go test 的输出端瞬间排水；
真实终端（iTerm2/Terminal.app）在重渲染时暂停读 pty，内核 64KB pty 缓冲
填满后应用的 `write()` 阻塞。若 paint 持 `r.mu` 同步写 tty，输入管线
整条冻结，直到终端恢复排水。运行中的 subagent 视图持续重绘（任务时钟、
spinner、流式增量），pty 常态饱和，所以冻结最先出现在那里。

## 前置

- macOS（`sample`/`atos`）、tmux、python3。
- 构建：`CGO_ENABLED=1 go build -tags fts5 -o /tmp/fbdbg/forebrain ./cmd/forebrain`
  （诊断版与清洁版分开命名，别覆盖正在跑的进程用的二进制）。
- 真模型会话：`scripts/acceptance/live_subagent.sh`。凭据在
  `~/.forebrain/e2e-zhipu.env`（键名 `FOREBRAIN_E2E_ZHIPU_KEY`），
  绝不打印、绝不写进任何文件正文。
- 隔离 home（`$TMPDIR/forebrain-live/home/forebrain.yaml`）里把
  `approval_policy` 改为 `never`，否则审批 overlay 会不断打断
  subagent 场景，污染实验。

## 第一步：确认冻结边界（两种探针）

- **回显探针**：向 composer 发送单字符，10–20ms 粒度轮询
  `tmux capture-pane -t <s> -p | grep '› <char>'`。先测基线（未触发时，
  正常 <0.1s），再触发场景测一次，对比才有意义。
- **屏幕探针**：间隔 2s 两次 capture 的 md5 对比。屏幕也停 = 全冻结；
  屏幕继续更新但输入死 = 输入管线独占阻塞（排水类问题的指纹）。

## 第二步：限速排水 pty 沉箱（关键手段）

`scripts/slowpty.py`（本 skill 目录下）：在 pty 上启动 forebrain，
把排水速率限制为 N 字节/秒来扮演"停顿的终端"，同时按 JSONL 脚本注入
输入字节，记录每个输入时刻的累计写出量。

```
python3 scripts/slowpty.py <binary> <FOREBRAIN_HOME> <projdir> <DRAIN> <in.jsonl> <out.log>
```

- JSONL 每行 `{"t": <发送前等待秒>, "b": "<base64 字节>"}`。
- `DRAIN=0` 不限速：先量真实写出量（判断洪峰到底写多少字节）。
- `DRAIN=3000` 左右：模拟停滞终端，复现冻结。

典型输入脚本（subagent 视图滚顶场景）：

1. 信任页：Down + Enter；
2. 提交派发 subagent 的指令（明说"派发一个 general-purpose 子代理…写长文"）；
3. ~30s 后 Down、Down、Enter（roster 焦点 → subagent 行 → 打开视图，
   footer 出现 `viewing general-purpose` 即成功）；
4. **等 90–120s 让视图内容长出来**（太早洪峰=白跑）；
5. 80× 零间隔滚轮上 `\033[<64;60;15M`；
6. 探针字符。

判定：复现 = 限速下输入字节被读走但探针字符迟迟不出现在输出里；
对照 = `DRAIN=0` 同脚本一切正常。

## 第三步：冻结现场取证

- `sample <pid> 3 -file /tmp/s.txt`；Go 符号常被剥离，用
  `atos -o <启动用的那个二进制> -l <sample 输出里的 Load Address> <addr>`
  手工还原。Load Address 每次启动都变。
- goroutine 全量栈：启动时加 `GOTRACEBACK=crash`，冻结中
  `kill -QUIT <pid>`。**栈不在 tmux 的 stderr 重定向里**：TUI 用
  dup2 把 fd 2 接到 `$FOREBRAIN_HOME/logs/error.log`
  （pkg/tui/stderr_unix.go），去那个文件里找 `SIGQUIT`。
- 主线程 parked（`pthread_cond_wait`）不是"没事发生"：那是等待型
  死锁/背压，`sample` 看不出 goroutine 级真相，必须 SIGQUIT 取栈。

## 第四步：分段探针（直接证据拿不到时）

临时写一个 env 门控 tracer（如 `FOREBRAIN_INPUT_TRACE` /
`FOREBRAIN_INPUT_TRACE_DIR`，文件见好就删），在这几个点位各打一行：

1. reader 读到字节（input_events.go readCh 消费处）；
2. middle goroutine → 事件 channel 的发送；
3. 主循环两个 select 分支的消费（runTurnWithSkill）；
4. `handleActiveRunInput` 入口；
5. renderer 锁获得（`ViewportScrollAt` 的 `r.mu.Lock()` 之后）；
6. paint 帧 submit（异步/同步两路都打）。

判读：reader 计数持续涨、主循环计数停 → 事件死在 channel 下游
（主 goroutine 阻塞在锁或写上）；"锁获得"之后无下文 → 阻塞在
paint 内部。检查异步 writer 是否激活时注意：`r.out` 的真实类型是
`syncWriter → ttyNewlineWriter → *os.File` 三层包装，检测要层层解包。

## 失败模式与教训

- tmux 里怎么打都不冻结是**常态**，不是复现失败——排水类问题必须上沉箱。
- `live_subagent.sh stop` 会杀掉 flaky_proxy；手动重启：
  `python3 scripts/acceptance/flaky_proxy.py 8743 <upstream> <control-file> &`。
- 泛洪输入必须零间隔：`for i in $(seq 1 80); do tmux send-keys -t <s> -l "$(printf '\033[<64;60;15M')"; done`。
  SGR 滚轮上=`...M`，点击=press+release 对，Shift+←=`\033[1;2D`。
- run 进行中的键盘输入可能进 steer 队列而非 composer 回显——探针字符
  选场景时要先确认该状态下"可编辑回显"是预期行为。

## 收尾（owner 铁律）

根因修复后：诊断脚手架（tracer、探针、临时测试文件）**全部删除**，
不留"断言不再出现"的反向测试、不留 deadcode。然后
`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` + `go vet ./...` +
`gofmt -l pkg/tui` 收口，并用本 skill 的沉箱重跑一次验证。
排水类冻结的已落地根因修复参照 `pkg/tui/paint_writer.go`（异步合帧
writer：专用 goroutine 写 tty、丢帧置脏 shadow、控制写 flush 屏障）。

## 成功判据

- 复现：基线回显 <0.1s，触发后 >2s 无回显、屏幕仍在更新、进程存活。
- 修复验证：同一 DRAIN 下滚轮事件 100% 被主循环消费、探针字符出现在
  composer（输出 log 里能搜到）、`$FOREBRAIN_HOME/logs/error.log` 无新栈。
