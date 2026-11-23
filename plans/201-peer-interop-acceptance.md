# 201 — 真机互通验收（forebrain ⇄ forebrain ⇄ Claude Code）+ 协议漂移检查脚本

- **基线**：`7a62e89f`（`exec/200`，199+200 全部提交之后；两者尚未合并入 dev）。
- **状态**：TODO
- **依赖**：194–199（200 落地后补网页场景）
- **执行隔离**：worktree `exec/201`（从 `exec/200` 起）+ 增量提交；真机验收跑本 worktree 构建的二进制，零 commit 的只是「真机验收本身」
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md`

> **执行者须知**：按场景逐个跑，每个场景把命令、关键屏幕截取、相关文件内容贴进本计划文末「执行记录」。任何场景与预期不符：先查是否 forebrain 的实现偏离设计；若是 Claude Code 的真实行为与设计 §3 的【推断】条目不同，**以真机为准**，回写设计 §3 对应条目并报告，不要改 Claude Code 侧的任何东西。完成后更新 `plans/README.md` 本行状态。
>
> **Drift check（先跑）**：`git diff --stat 7a62e89f -- scripts/peer-protocol-check.sh FOREBRAIN.md docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md pkg/process`，应无输出；有输出就 STOP。
>
> **2026-11-03 reconcile（相对 202ed4f2 的漂移，已在本文件原地改正）**：
> 1. 基线从 `202ed4f2` 改为 `7a62e89f`（199 的六笔 + 200 的四笔）；199/200 未合并入 dev，本计划的分支从 `exec/200` 起。
> 2. **验收模型从 `zhipuai/glm-5.3` 改为 `opencode-go/deepseek-v4.1-flash`**（owner 2026-11-01 裁决；智谱账户 2026-11-01 起余额不足，202 的真机已因此挂 HTTP 429「余额不足或无可用资源包」，glm-5.3 不可用）。§1/§2 的配置与 key 来源按此改写。
> 3. 设计文档 `docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` 是 gitignore 的本地文件，已复制进 worktree。

## 0. 背景与 owner 裁决

owner 2026-10-30 裁决 4：**严格对齐 Claude Code，线协议与 Claude Code 互通，forebrain 与 Claude Code 之间能互发消息**。本计划用真实的 Claude Code（写计划时本机为 2.1.301）和真实模型（forebrain 侧 glm-5.3）证明：发现、互发、挂起放行、回执、hop-chain、登记模式切换、网关路由全部成立；并核实设计里标【推断】的条目；最后提供一个升级 Claude Code 后先跑的协议漂移检查脚本。

同日三条追加裁决同样要真机证明：①与 Claude Code 互通是存于 `fb_config` 的全局开关，**默认关闭**，只读写数据库、不经 forebrain.yaml；关闭时 forebrain 不碰 `~/.claude/sessions` 与 `cc-socks`，只和其他 forebrain 通信；②forebrain 跨进程通信**自闭环**，不依赖 Claude Code 或其他任何进程；③**检测到本机装了 Claude Code 才在 TUI/网页显示这个开关**，**打开后只用 Claude Code 的目录与 `cc-socks`，不再碰自己的登记目录**，开关不同的进程互相看不见。对应场景 0、1、7、11。

## 1. 安全与隔离（硬规则）

- forebrain 一律用**新建的临时 home**（`mktemp -d`），**绝不**用 `~/.forebrain`，也不得继承环境里的 `FOREBRAIN_HOME`。模型凭据只用现成的 `opencode-go` 登录态：把 `~/.forebrain/opencode.json` 与 `~/.forebrain/models.json` 各拷一份到临时 home，**不写任何 key**（`opencode-go` 的鉴权走 `opencode.json`，不经环境变量）。临时 home 的 `forebrain.yaml` 只配 `opencode-go`/`deepseek-v4.1-flash`（base_url `https://opencode.ai/inference/go`，`enable_subagent: true`）。配置入库后若 yaml 的运行时段会被导入数据库，按导入后的状态验证一次 `sqlite3 <home>/state/forebrain.state.sqlite "select section from fb_config"` 含 `agents`。
- Claude Code 用 owner 本机已登录的安装（`claude`），在一个**临时项目目录**里启动，结束后退出；不改 `~/.claude/settings*.json`。
- 所有场景的 forebrain 都设 `FOREBRAIN_PEERS_DIR="$PD" FOREBRAIN_PEER_SOCKET_DIR="$PD/socks"`（FB 侧与 owner 平时在跑的 forebrain 隔开）。
- 需要 Claude Code 的场景（1–4、9、10、11）：forebrain **不设** `FOREBRAIN_PEERS_CC_DIR`/`FOREBRAIN_PEER_CC_SOCKET_DIR`（要走真实的 `~/.claude/sessions` 与 `/tmp/cc-socks`，这正是产品路径），并按 §2 的 `cc_on` **只改数据库**打开开关；**绝不**往 forebrain.yaml 写 `peers`。开关打开时 forebrain 的文件只应出现在 `~/.claude/sessions` 与 `/tmp/cc-socks`，`$PD`、`$PD/socks` 里不应有它的任何文件。场景 0、5、6、8 保持默认关闭。场景 7 与 0e 用 CC 覆盖变量，避免动真实目录。
- 终端驱动按 `run-forebrain` 技能：tmux 专用会话（高窗口 `-x 150 -y 150`）；需要 Terminal.app 时只用 `do script` 返回的窗口 id，禁用 `front window`。
- 每个场景结束：退出所有进程，`ls ~/.claude/sessions /tmp/cc-socks "$PD" "$PD/socks"` 确认 forebrain 的记录、key、socket 都已清理（Claude Code 自己的不管）。

## 2. 准备

```bash
B=$(mktemp -d); CGO_ENABLED=1 go build -tags fts5 -o "$B/forebrain" ./cmd/forebrain && scripts/install-dictionary.sh "$B"
REPO=$(mktemp -d); git -C "$REPO" init -q && echo 'package main' > "$REPO/a.go" && git -C "$REPO" add -A && git -C "$REPO" commit -qm init
H1=$(mktemp -d); H2=$(mktemp -d); H3=$(mktemp -d)   # 三个 forebrain home（H3 给场景 9 的网关），各自按 §1 写 forebrain.yaml，并拷 opencode.json 与 models.json
PD=$(mktemp -d)                     # FB 会合目录与 FB socket 目录的覆盖（§1）
# 打开 / 关闭与 Claude Code 互通（只写数据库；库在首次启动后才存在——先启动一次再退出，或在运行中改、等下一轮生效）
cc_on()  { sqlite3 "$1/state/forebrain.state.sqlite" "insert into fb_config(section,value_json,updated_at_ms) values('peers','{\"claude_code\":true}',0) on conflict(section) do update set value_json=excluded.value_json"; }
cc_off() { sqlite3 "$1/state/forebrain.state.sqlite" "update fb_config set value_json='{}' where section='peers'"; }
```

## 3. 场景

0. **默认关闭 + 自闭环**（先跑；`$H1` 不调 `cc_on`）：
   - 0a：在 `$REPO` 起 Claude Code 与 forebrain TUI A（`$H1`）、TUI B（`$H2`）。`ls ~/.claude/sessions /tmp/cc-socks` 里没有 A、B 的 pid；Claude Code 的 `ListAgents` 里没有 A、B；A 的 `peer_list` 里只有 B、没有 Claude Code；Claude Code 给 A 发消息不可达（它根本找不到 A）。A 的 `/config` →「对等通信」里**有**「与 Claude Code 互通」这一行（本机装了 Claude Code），值为「默认（不设置）」。
   - 0b：A `peer_send` 给 B → B 自动开轮并回复 A，全程不涉及 Claude Code。然后**退出 Claude Code**（必要时连同把 `PATH` 里的 `claude` 去掉再起 A、B）重复一次，结果相同。
   - 0c：A 运行期间观察 30s：`ps -o pid,ppid,command -A | awk -v p=<A pid> '$2==p'` 不周期性出现 `ps`/`git` 子进程；`lsof -p <A pid> | grep -E 'claude|cc-socks'` 无输出。
   - 0d：在 `$H1/forebrain.yaml` 里手写 `peers: {claude_code: true}` 后重启 A → 日志有「peers section is ignored」警告，A 仍然不出现在 `~/.claude/sessions`（文件配置不生效）；删掉这几行。
   - 0e：**没装 Claude Code 时不显示开关**：`X=$(mktemp -d)`，以 `FOREBRAIN_PEERS_CC_DIR=$X/none` 重启 A（模拟本机没有 Claude Code 的目录）→ `/config` 里**没有**「与 Claude Code 互通」这一行；退出后 `cc_on "$H1"` 再同样启动 → A 仍在 `$PD`（开关存了但不生效），`$X/none` 没有被创建。（191 落地后补：网页设置里同样不显示，`GET /api/config` 的 `claude_code_available` 为 false。）最后 `cc_off "$H1"`。
1. **Claude Code 看得见 forebrain**：`cc_on "$H1"` 后在 `$REPO` 起 forebrain TUI（home `$H1`）与 Claude Code。让 Claude Code 列出可联系的代理（它的 `ListAgents`）。
   预期：列表里有 forebrain 的名字（`<repo 目录名>-<两位十六进制>`），状态随 forebrain 忙闲变化。另检查 `~/.claude/sessions/<forebrain pid>.json` 字段与设计 §4.3 一致且 `messagingSocketPath` 是 `/tmp/cc-socks/<pid>.sock`（0600）、key 文件 0600；`$PD` 与 `$PD/socks` 里**没有**它的任何文件（打开后不再碰自己的登记目录）。
2. **Claude Code → forebrain（空闲唤醒）**：让 Claude Code 给 forebrain 发「请列出 a.go 的内容并回复我」。
   预期：forebrain 空闲时自动开一轮，屏上出现 `↘ <Claude Code 会话名>` 卡；forebrain 用 `peer_send` 回复；Claude Code 收到 `<cross-session-message from="uds:…" from-name="<forebrain 名>" from-mode="prompting">…` 并据此作答。查 `$H1` 状态库最近几行 `fb_messages.parts` 含 `is_meta` 与 `origin:peer`。
3. **forebrain → Claude Code（运行中插话）**：让 Claude Code 跑一个耗时任务（如 `sleep 30` 后再总结），期间 forebrain 发消息给它。
   预期：Claude Code 在下一个工具边界收到；forebrain 侧工具结果 `Sent to <name> (busy).`。
4. **挂起放行（双向）**：
   - 4a：Claude Code 以 `--dangerously-skip-permissions` 启动（bypass），forebrain 会话为 Default（prompting）发消息 → Claude Code 挂起并在终端询问；放行后 forebrain 收到 `Delivery` 状态卡（released）；拒绝的另一条收到 declined。
   - 4b：forebrain 切 Full Access（`/permissions`），Claude Code 以默认模式发消息 → forebrain 弹挂起浮层；放行 → 开轮；Claude Code 侧显示 delivered。
   - 核实设计 §3.10 的【推断】：「采信 from-mode」是否开启（4a 里若 forebrain 自己也切 Full Access 后再发，Claude Code 应**不再**挂起）。结果回写设计。
5. **forebrain ⇄ forebrain（跨 home、子代理；两边开关都保持默认关闭）**：在 `$REPO` 再起一个 forebrain（home `$H2`）。让 A 派发一个耗时子代理；B `peer_list` 能看到 A 的子代理（`subagent in another process`）；B 给 A 的子代理发消息 → 子代理在下一个工具边界收到；子代理结束后 B 再发 → A 的主代理收到转交说明 + 原信。
6. **同树护栏**：A 编辑 `a.go` 后，B 的下一轮请求带同树提醒（让 B 调用 `peer_list` 或回答「现在还有谁在这个目录工作」来观察）；B 执行 `git stash` → 审批卡原因 `Other agents in this tree: A`；B 切到 Full Access 后再执行 `git stash` → **仍然弹审批**（owner 追加裁决：YOLO 下也强制弹审批）；B 的项目里预先写一条允许 `git stash` 的规则 → 仍然弹审批。
7. **模式切换与 CC 目录跟进（owner 追问 9、10 与再追加裁决；开关打开）**：`X=$(mktemp -d)`；`cc_on "$H1"` 后 A 以 `FOREBRAIN_PEERS_CC_DIR=$X/sessions`（先不建）、`FOREBRAIN_PEER_CC_SOCKET_DIR=$(mktemp -d)` 启动 → A 在 FB 模式：记录在 `$PD`，`/config` 里没有开关这一行。`mkdir "$X/sessions"` → 5s 内 A 的记录与 key 只在 `$X/sessions`、socket 在 CC socket 覆盖目录，`$PD` 与 `$PD/socks` 里没有 A 的文件，`/config` 里出现开关并显示「开」。`rm -rf "$X/sessions"` → 5s 内 A 回到 `$PD`，`$X/sessions` **没有被重建**，`/config` 里开关消失。三个阶段 A 的名字相同。
8. **hop-chain 防环**：让 A 与 B 的模型互相转发同一句话（提示它们「收到就原样转给对方」），观察第 11 次左右被 `hop-loop` 丢弃，发送方收到 `dropped (hop-loop)` 状态行，循环停止。
9. **网关路由**：起 forebrain gateway（home `$H3`，先启动一次让库生成、退出后 `cc_on "$H3"` 再起），在网页里于 `$REPO` 项目下开一个会话并跑过一轮；Claude Code（cwd `$REPO`）给网关发消息 → 该会话被唤醒开轮；Claude Code 在一个无关目录里发 → 收到拒收回执（原因 `no conversation in this work tree`）。（200 落地后补：网页上来信卡、挂起横幅放行。）
10. **空闲通知（notify_idle）**：让 Claude Code 给忙碌中的 forebrain 发消息并要求「对方空闲时通知我」（它的 SendMessage `notify_when_idle`）。
    （`$H1` 已 `cc_on`。）预期：forebrain 收到 `notify_when_idle` 控制帧，转为空闲时发 `peer_idle_notice{state:"idle"}`，Claude Code 侧显示对方已空闲。**通过后**把 `pkg/process` 里发布特性的列表加上 `notify_idle`（196 留的开关），重跑本场景确认 Claude Code 的列表/行为无异常；不通过就保持不发布并在执行记录写清差异。

11. **开关不一致、两边都打开、运行中关闭**：`cc_on "$H1"`，`$H2` 保持关闭；两个 TUI 与 Claude Code 都在 `$REPO`。
    - A（CC 模式）与 B（FB 模式）**互相看不见**：Claude Code 列表里只有 A；A 的 `peer_list` 有 Claude Code、没有 B；B 的 `peer_list` 里没有 A；`peer_send` 用对方的名字 → `No agent named …`。（owner 再追加裁决的直接结果，不是缺陷。）
    - `cc_on "$H2"` 并让 B 跑一轮（配置每轮同步）→ ≤5s 内 B 进入 CC 模式：A、B 互相可见、互发正常，两边信封 `from` 都在 `/tmp/cc-socks/`；Claude Code 列表里有 A 与 B。
    - 杀掉 Claude Code（`kill <pid>`）→ A ⇄ B 互发不受影响（不需要任何 Claude Code 进程在跑）。
    - 运行中 `cc_off "$H1"`，让 A 跑一轮 → ≤5s 内 A 从 `~/.claude/sessions` 与 `/tmp/cc-socks` 消失、回到 `$PD` 与 `$PD/socks`，名字不变；此时仍在 CC 模式的 B 看不见 A。
    - 再 `cc_off "$H2"` 并让 B 跑一轮 → B 也回到 `$PD`；A ⇄ B 互发恢复。重新启动的 Claude Code 找不到 A、B。

## 4. 协议漂移检查脚本

新建 `scripts/peer-protocol-check.sh`（只读）：

- 找到 Claude Code 二进制：`readlink -f "$(command -v claude)"`；不存在就提示并退出 0。
- 对二进制做 `LC_ALL=C grep -a -c` 检查以下稳定字面量都存在：`cross-session-message`、`peer_message_status`、`notify_when_idle`、`peer_idle_notice`、`cc-socks`、`bucketCapacity:30,refillPerSecond:0.5,dedupWindowMs:30000,maxSelfHops:10,maxChainLength:28,maxTrackedSenders:256`、`"[uds-messaging] Closing a connection that sent no complete line within`。
- 若 `~/.claude/sessions` 下有 Claude Code 自己的记录（无 `forebrain` 键），检查其 `peerProtocol == 1`、`kind`/`status` 取值在设计 §3.2 的集合内。
- 输出 `claude --version`、每项 OK/MISSING；有 MISSING 时退出 1 并提示「Claude Code 对等协议可能已变：先读设计 §3，再按 plans/201 重跑互通验收」。
- 在 `FOREBRAIN.md` 的 House rules 末尾加一条：升级本机 Claude Code 后、改动 `pkg/peer` 前先跑这个脚本。

验证：`bash scripts/peer-protocol-check.sh` 在本机（2.1.301）退出 0 且全部 OK。

## 5. 边界

- **只动**：`scripts/peer-protocol-check.sh`（新）、`FOREBRAIN.md`（加一条规则）、`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md`（回写核实结果）、`pkg/process` 里发布特性的那一行（场景 10 通过时）、本计划执行记录。
- 发现实现缺陷：在执行记录里写清现象与定位，**回到对应计划（194–199）修**，不在本计划里顺手改实现。

## 6. 完成判据

1. 场景 0–8、11 全部通过，执行记录齐全（命令、截屏/日志、相关文件内容）；场景 9 的终端侧通过（网页侧待 200）；场景 10 有明确结论（发布或不发布 `notify_idle`）。
2. 设计 §3 里所有【推断】条目要么改为【已核实】，要么写明真机结论。
3. `bash scripts/peer-protocol-check.sh` 退出 0。
4. 每个场景后 `~/.claude/sessions`、`/tmp/cc-socks`、`$PD` 里没有残留的 forebrain 文件。
5. `plans/README.md` 本行状态更新。

## 7. 维护注意

- Claude Code 升级后：先跑漂移脚本；有变化就按本计划重跑场景 1–4、10，必要时新建 `pkg/peer/testdata/cc-<新版本>/` 夹具。
- 本计划的执行记录是「互通到底在哪个 Claude Code 版本上验证过」的唯一凭据，不要删。

## 8. STOP 条件

- 任何一步需要使用或改写 `~/.forebrain`、`~/.claude/settings*.json`。
- 场景 0 里 forebrain 在开关关闭时出现在 `~/.claude/sessions` 或 `/tmp/cc-socks`、forebrain 之间的通信在 Claude Code 退出后失败、或 0e 里没有 Claude Code 目录时 `/config` 仍出现开关；或任何场景里开关打开的 forebrain 在 `$PD`/`$PD/socks` 留下文件——这些都是违反 owner 裁决的实现缺陷，回 195/196/197 修，不要继续后面的场景。
- Claude Code 列表里完全看不到 forebrain（场景 1 失败）：先比对 forebrain 记录与真实 Claude Code 记录的字段差异并报告，不要猜着改字段。
- 模型 key 出现在任何命令行输出或文件里——立即停止、清理并报告。

**追加裁决（owner 2026-10-31）：跨进程协同固定开启（always-on）**——不能关闭、不支持关闭、不支持配置；唯一的开关是「与 Claude Code 互通」（`peers.claude_code`，默认关闭，用户可手动打开，只存库，装了 Claude Code 才显示）。本计划受影响：验收里凡是关闭跨进程协同的步骤都删除；只保留「与 Claude Code 互通」开关的开 / 关验收。

**改（owner 2026-10-31）：与 Claude Code 互通的部分不做执行者真机验收，由用户手动验收。** 执行者只做：
- 不涉及 Claude Code 的场景（0 中除 0a 里 Claude Code 列表那一条外的部分、5、6、7〔用覆盖变量模拟目录，不起 Claude Code〕、8）照常真机跑，结果写进执行记录；
- 协议漂移检查脚本（§4）照常交付；
- 把涉及 Claude Code 的场景（0a 的 Claude Code 部分、1、2、3、4、9、10、11）整理成「**用户手动验收清单**」（步骤 + 预期结果，沿用 §3 原文），附在本计划末尾并抄进 `plans/README.md` 本行结果栏，由用户手动验收。设计里标【推断】的条目（如 §3.10「采信 from-mode」）在清单里标明「需用户验收时确认」，不回写设计。
- 其余判据通过即可标 DONE（注明「含用户手动验收项」）；第 10 场景的 `notify_idle` 特性**保持不发布**，等用户验收通过后再打开。

**再改（owner 2026-11-03）：禁止真机验收 forebrain ⇄ Claude Code 互通信**——「禁止启动 claude code」「forebrain 和 claude code 互通信的功能由用户自己验证」「只可以真机验收 forebrain harness 进程之间互通信」。执行者只做 forebrain ⇄ forebrain 的场景（0b/0c/5/6/8）+ 漂移脚本 + 用户手动清单；0d/0e/7 一并没有真机跑（它们验证的是 `peers.claude_code` 开关本身，不是进程间互通信），移入 §11 的用户手动清单。

## 11. 执行记录（2026-11-03，评审 APPROVE，含用户手动验收项）

- **提交**（worktree `.worktrees/201`，分支 `exec/201`，**未合并**）：`c2c558a7`（漂移脚本）、`79099b63`（FOREBRAIN.md 规则）、`4789773a`（缺陷一修复）、`5cb91311`（缺陷二修复）。
- **验收模型**：`opencode-go/deepseek-v4.1-flash`（owner 2026-11-01 裁决）；每个临时 home 只配 provider/model/base_url，鉴权走拷贝的 `opencode.json`，未写任何 key。
- **真机结果**：0a / 0c 通过；0b 通过并复跑通过；5 的前两半通过、转交一半未验；6 全部通过（4/4）；8 通过（`dropped (hop-loop)`，循环停止，两进程存活）。

### 11.1 缺陷一（已修 `4789773a`）：空闲 TUI 收到对等消息后不自动开轮

`pkg/tui/notify.go` 的 `idleWake()` 只覆盖「到点的 continuation」与「被挂起的对等消息」，漏了 `peerDue`。`peerDue` 由 `handlePeerWakeNotification` 在收到 `PeerWakeMsg` 时设置，消费它的是空闲循环的 `state.takePeerDue()`；空闲循环不醒，`takePeerDue` 永远不被调用。**证据**：A `peer_send` 给 B，A 侧 `Sent to … (idle).`，但 B 的屏幕与状态库都无变化（`fb_messages` 8→8）；B 敲一个键后 8→13，`channel=peer` 的回合才完成。修法：`idleWake()` 加 `|| s.peerDue != nil` + 回归测试（去掉第三项即失败）。

### 11.2 缺陷二（已修 `5cb91311`）：对等输入投进主代理回合会 panic

`queuedSubmissions`（`pkg/tui/run.go`）对队列里每个 `run.Input` 做 `in.Payload.(ComposerSubmission)`，未检查 `Payload` 是否为 nil。而所有对等来源的输入都不带 `Payload`（`process.peerInput` 与 `deliverNote` 都构造没有 `Payload` 的 `run.Input`）。只要这样一条输入被投递进正在运行/正在结束的回合（`Control.Find` 命中 Running/Waiting/Finishing → `Steer`/`FollowUp`），该回合在工具边界调用 `takeDeliveredSteersForDisplay` 时 panic 退出。**最小复现**：A 起 `sleep 60` → B 发一条消息 → `sleep 60` 结束时 A panic。**影响**：给一个忙碌的 forebrain 发消息就会触发。修法：`queuedSubmissions` 只收 `Payload` 是 `ComposerSubmission` 的输入；`takeDeliveredSteersForDisplay` 把 delivered 列表**一次排空**并分回两半；新增 `renderDeliveredInputCards`，用既有的 `peerOriginCard` 把对等投递画成它 replay 时的同一张卡（`↘ <sender>` 信或 `Delivery` 卡）；三处渲染点各自一次排空、两半都画。回归测试 `TestDeliveredPeerInputDrawsItsCardWithoutPanicking` 把断言改回去就以**与真机完全相同的 panic 信息**失败。

### 11.3 协议漂移检查（§4）

`bash scripts/peer-protocol-check.sh` 在本机退出 **0**（19.7s）。**本机有两个 Claude Code 安装**：`command -v claude` 解析到 Homebrew **2.1.220**（不具备 forebrain 实现的协议：`peer_message_status`/`notify_when_idle`/`peer_idle_notice`/`cc-socks`/限速对象/`hop-chain`/`peerFeatures`/`pidDomain` 字节计数全为 0），nvm 全局 **2.1.301** 才是计划写作时假设的版本（逐条 OK）。脚本检查它找到的每个安装，只有全部都不具备协议才退出 1，并对不具备的安装打印 WARNING。负路径（stub `claude`）退出 1 并打印「对等协议可能已变」提示；空 PATH 退出 0。**对计划 §4 字面量表的一处修正**：最后一条字面量在计划里带前导双引号，2.1.301 里该串不带引号（带引号计数 0，不带 2），脚本按不带引号检查。

### 11.4 场景 6（同树护栏，4/4 通过）

同树 `<system-reminder>` 点名 A 与 `a.go`；`git stash` 弹审批卡 `Other agents in this tree: …`；Full Access 下仍弹；预先写 `.forebrain/safety.json` allow 规则仍弹（`pkg/safety.MayPermit` 对 `SourceProjectSettings` 返回 false，项目设置的 allow 规则加载时即丢弃）。四次 `git stash` 全被拦下，`a.go` 始终完好。

### 11.5 场景 8（hop-chain 防环，通过）

A、B 都设「收到就原样转回」，hop-chain 4→6→…→16 后协议自己的防环闸生效：B 的库出现 `- to …: dropped (hop-loop); it was not delivered.`，模型据此报告循环停止，两进程全程存活。**与计划文字的一处差异**：计划写「第 11 次左右」，实测链长到 16 才触发——闸的判据是自身 hop token 在链中出现 ≥ 10 次（设计 §3.8），不是链长 28（那是 `hop-runaway`）。

### 11.6 场景 5 的转交一半未验

子代理一结束就从 A 的登记记录里消失（`LiveSubagents` 只列 `ch.running != nil` 的），名字不再可解析，`peer_send` 直接回 `No agent named …`。设计 §5.2 的转交分支要求名字仍可解析而 `SteerLiveSubagent` 回 `ErrSubagentNotRunning`，两者只在「记录已写出、执行已结束」的一秒窗口内同时成立；100 帧/30s 扫描未命中。**这是一处待查的设计/实现缺口**（196/199）：要么记录应滞后一个已结束的子代理，要么转交分支是死代码。

### 11.7 用户手动验收清单（涉及 Claude Code 的场景，执行者未跑、未启动过 Claude Code）

> 通用前置：`cc_on()`/`cc_off()` 只改数据库（`fb_config` 表 `peers` 行），绝不往 forebrain.yaml 写 `peers`；每个 forebrain 用 `mktemp -d` 的临时 home，绝不使用 `~/.forebrain`，也不继承环境里的 `FOREBRAIN_HOME`；所有 forebrain 设 `FOREBRAIN_PEERS_DIR="$PD" FOREBRAIN_PEER_SOCKET_DIR="$PD/socks"`；需要 Claude Code 的场景不设 `FOREBRAIN_PEERS_CC_DIR`/`FOREBRAIN_PEER_CC_SOCKET_DIR`，走真实的 `~/.claude/sessions` 与 `/tmp/cc-socks`；模型 `opencode-go/deepseek-v4.1-flash`；每个场景结束退出所有进程并确认 forebrain 的记录/key/socket 已清理。
> **注意**：本机 `claude` 解析到 2.1.220（协议更早），用 2.1.220 跑这些场景大概率失败——请用 2.1.301（nvm 那个）或先升级 Homebrew cask。

- **0a 的 Claude Code 部分**：`$H1` 保持默认，在 `$REPO` 起 forebrain TUI A 与 Claude Code。预期：`ls ~/.claude/sessions /tmp/cc-socks` 没有 A 的 pid；Claude Code 的 `ListAgents` 里没有 A、B；A 的 `peer_list` 里只有 B；Claude Code 给 A 发消息不可达。A 的 `/config` →「对等通信」里有「与 Claude Code 互通」这一行，值为「默认（不设置）」。**需用户验收时确认**：Claude Code 的 `ListAgents` 里没有 A、B。
- **场景 1**：`cc_on "$H1"` 后在 `$REPO` 起 forebrain TUI 与 Claude Code，让 Claude Code 列出可联系的代理。预期：列表里有 forebrain 的名字（`<repo 目录名>-<两位十六进制>`），状态随忙闲变化；`~/.claude/sessions/<forebrain pid>.json` 字段与设计 §4.3 一致，`messagingSocketPath` 是 `/tmp/cc-socks/<pid>.sock`（0600），key 0600；`$PD`/`$PD/socks` 里没有它的任何文件。
- **场景 2**：让 Claude Code 给 forebrain 发「请列出 a.go 的内容并回复我」。预期：forebrain 空闲时自动开一轮，屏上出现 `↘ <Claude Code 会话名>` 卡；forebrain 用 `peer_send` 回复；Claude Code 收到 `<cross-session-message from="uds:…" from-name="<forebrain 名>" from-mode="prompting">…` 并据此作答；`$H1` 库里最近几行 `fb_messages.parts` 含 `is_meta` 与 `origin:peer`。
- **场景 3**：让 Claude Code 跑耗时任务（如 `sleep 30` 后再总结），期间 forebrain 发消息给它。预期：Claude Code 在下一个工具边界收到；forebrain 侧工具结果 `Sent to <name> (busy).`
- **场景 4**：4a Claude Code 以 `--dangerously-skip-permissions` 启动（bypass），forebrain 为 Default（prompting）发消息 → Claude Code 挂起并询问；放行后 forebrain 收到 `Delivery` 卡（released），拒绝的另一条收到 declined。4b forebrain 切 Full Access，Claude Code 以默认模式发消息 → forebrain 弹挂起浮层；放行 → 开轮；Claude Code 侧显示 delivered。**需用户验收时确认**：设计 §3.10 的【推断】「采信 from-mode」是否开启（4a 里若 forebrain 自己也切 Full Access 后再发，Claude Code 应不再挂起），结果回写设计 §3.10。
- **0d / 0e**：0d 在 `$H1/forebrain.yaml` 手写 `peers: {claude_code: true}` 后重启 A → 日志有「peers section is ignored」警告，A 仍不出现在 `~/.claude/sessions`；删掉这几行。0e 以 `FOREBRAIN_PEERS_CC_DIR=$X/none` 重启 A → `/config` 里没有「与 Claude Code 互通」这一行；退出后 `cc_on "$H1"` 再同样启动 → A 仍在 `$PD`（开关存了但不生效），`$X/none` 没被创建；网页设置里同样不显示，`GET /api/config` 的 `claude_code_available` 为 false；最后 `cc_off "$H1"`。
- **场景 7**：`cc_on "$H1"` 后 A 以 `FOREBRAIN_PEERS_CC_DIR=$X/sessions`（先不建）、`FOREBRAIN_PEER_CC_SOCKET_DIR=$(mktemp -d)` 启动。三阶段：① A 在 FB 模式（记录在 `$PD`，`/config` 无开关行）；② `mkdir "$X/sessions"` → 5s 内 A 的记录与 key 只在 `$X/sessions`、socket 在 CC 覆盖目录，`$PD`/`$PD/socks` 里没有 A 的文件，`/config` 出现开关并显示「开」；③ `rm -rf "$X/sessions"` → 5s 内 A 回到 `$PD`，`$X/sessions` 没有被重建，`/config` 里开关消失。三阶段 A 的名字相同。
- **场景 9**：起 forebrain gateway（home `$H3`，先启动一次让库生成、退出后 `cc_on "$H3"` 再起），在网页里于 `$REPO` 项目下开一个会话并跑过一轮；Claude Code（cwd `$REPO`）给网关发消息 → 该会话被唤醒开轮；Claude Code 在无关目录里发 → 收到拒收回执（原因 `no conversation in this work tree`）。网页上来信卡、挂起横幅放行（200 落地后补）。
- **场景 10**：让 Claude Code 给忙碌中的 forebrain 发消息并要求「对方空闲时通知我」（`notify_when_idle`）。预期：forebrain 收到订阅帧，转为空闲时发 `peer_idle_notice{state:"idle"}`，Claude Code 侧显示对方已空闲。**需用户验收时确认**：设计 §3.12 标【推断】的时序（订阅过期 12h、订阅上限 32）。通过后才把 `pkg/process` 的发布特性列表加上 `notify_idle`；不通过就保持不发布并写清差异。
- **场景 11**：`cc_on "$H1"`、`$H2` 保持关闭，两个 TUI 与 Claude Code 都在 `$REPO`。① A（CC 模式）与 B（FB 模式）互相看不见（Claude Code 列表里只有 A；A 的 `peer_list` 有 Claude Code、没有 B；B 的 `peer_list` 里没有 A；按名字发 → `No agent named …`）——这是 owner 追加裁决的直接结果，不是缺陷。② `cc_on "$H2"` 并让 B 跑一轮 → ≤5s 内 B 进入 CC 模式，A、B 互相可见、互发正常，两边信封 `from` 都在 `/tmp/cc-socks/`，Claude Code 列表里有 A 与 B。③ 杀掉 Claude Code → A ⇄ B 互发不受影响。④ 运行中 `cc_off "$H1"` 并让 A 跑一轮 → ≤5s 内 A 从 `~/.claude/sessions` 与 `/tmp/cc-socks` 消失、回到 `$PD`/`$PD/socks`，名字不变；仍在 CC 模式的 B 看不见 A。⑤ 再 `cc_off "$H2"` 并让 B 跑一轮 → B 也回到 `$PD`，A ⇄ B 互发恢复；重新启动的 Claude Code 找不到 A、B。
- **设计里其余【推断】条目**：§3.1 `CLAUDE_CONFIG_DIR` 覆盖 Claude 配置目录【推断】；§3.10 forebrain 侧挂起缓冲上限 20【推断】——均需用户验收时确认。
