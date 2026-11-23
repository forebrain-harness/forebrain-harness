# Plan 370: process 工具 `start`+`wait` 在进程已退出后不得再报陈旧的 `running: true`

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving on. If anything in the
> "STOP conditions" section occurs, stop and report — do not improvise.
>
> **Drift check (run first)**:
> `git diff --stat e1abd2f4..HEAD -- pkg/tool/process_tool.go pkg/tool/process_tool_test.go`
> 两个 in-scope 文件若在基线 `e1abd2f4` 之后被改过，先逐段比对下文 "Current state" 摘录；
> 内容不符 → STOP。行号漂移不算漂移，以内容为准。

## Status

- **Priority**: P1（完成卡上的自相矛盾状态会同时误导模型与人：`running: true` 会让模型把已退出的进程当活后台进程继续 `wait`/`kill`，或误判"服务还在起"）
- **Effort**: S
- **Risk**: LOW（一行重算 + 一条回归测试；不改 payload 键集合、不改其它 action、不碰渲染层）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `e1abd2f4`, 2026-11-23

## Why this matters

owner 截图：一张 `process` 完成消息卡同时显示 `Exited: true` 与 `Running: true`
（id 7、Elapsed ms 204618、Exit code 0、Ready: false、带 Tail）。这两个事实互斥。
完成卡逐键渲染工具返回的 JSON payload（截图即其字母序展开：Command / Elapsed ms /
Exit code / Exited / Id / Log / Pid / Ready / Running / Tail），所以矛盾出在 payload
本身，任何渲染表面（TUI、web）都会照样显示——修 payload 即修所有表面。

代价：模型侧读到 `running: true` 会把一个已退出的进程当活着的后台进程（再发 `wait`、
`kill`，或继续等一个永远不会 ready 的服务）；人侧无法从卡片判断进程死活，只能再去翻
log 文件。

owner 的第二个疑问（"调用是阻塞的，不是后台跑"）在本计划中裁定为**设计如此、不是 bug**：
`ProcessInput.Wait` 的 schema 注释写明 "start: optionally wait for readiness before
returning"，`start` 带 `wait` 就会阻塞到 ready／进程退出／timeout 三者之一；截图里的命令
是一条会结束的 `go test … | tail -30` 管道，其输出永远不命中 readiness pattern，轮询只能以
"进程退出"结束，于是阻塞了进程整个生命周期 3m24s。`id/pid/log` 这些"后台字段"也是设计：
`start` 一律后台化进程（工具描述："Run a command that outlives this call"）。
**唯一的 bug 是陈旧的 `running`**。

## Current state（advisor 亲读，基线 `e1abd2f4`）

- `pkg/tool/process_tool.go` — process 工具全部五个 action（start/log/wait/kill/list）与
  readiness 轮询、PID ledger 的实现。
- `pkg/tool/process_tool_test.go` — 该工具的 handler 级测试；辅助函数
  `newProcessToolForTest` / `processTestContext` / `callProcess` / `processTestID` /
  `processRecordForTest` / `waitForProcessSettled` 都在此文件（:21-98）。
- `pkg/safety/process.go` — `ProcessHandle`：`Exited()`(:417-427) 以 `done` channel 判定，
  `Result()`(:432-439) 返回收割结果。**本计划不改此文件**。

缺陷链条（三处摘录即全部根因）：

`pkg/tool/process_tool.go:336-347`（`processStart` 尾部）——`running` 在阻塞等待**之前**
快照进 payload，等待结束后只合并 readiness 的键、从不重算 `running`：

```go
	payload := map[string]any{"id": id, "pid": handle.PID(), "log": logPath, "command": command, "running": !handle.Exited()}
	if strings.TrimSpace(in.Wait) == "" {
		return marshalProcessResult(payload)
	}
	readiness, err := processReadiness(ctx, rec, in)
	if err != nil {
		return "", err
	}
	for key, value := range readiness {
		payload[key] = value
	}
	return marshalProcessResult(payload)
```

`pkg/tool/process_tool.go:514-523`（`processReadiness` 轮询循环的两个结束分支）——退出分支
**不带 `running` 键**，于是合并后陈旧的 `true` 存活：

```go
		if ready {
			return map[string]any{"ready": true, "elapsed_ms": elapsed.Milliseconds()}, nil
		}
		if rec.handle.Exited() {
			res, _ := rec.handle.Result()
			return map[string]any{
				"ready": false, "exited": true, "exit_code": res.ExitCode,
				"elapsed_ms": elapsed.Milliseconds(), "tail": logTail(rec.logPath, defaultProcessTail),
			}, nil
		}
```

分支核对（为什么只有一条路径矛盾）：
- 不带 `wait` 的 `start`（:337-339）：立即返回，`running` 快照即真值，正确。
- 轮询 ready 分支（:514-516）：进程仍活，`running: true` 正确。
- 轮询 timeout 分支（:527-532，`exited: false`）：进程仍活，`running: true` 正确。
- **轮询退出分支（:517-523）：进程已死，payload 里却是开始时刻的 `running: true` ⇒ 矛盾。**
- `action=wait`（`processWait` :448-466）两条路径都不含 `running` 键：形状不一致但不矛盾，
  本计划不动它（见 Maintenance notes 的 deferred 项）。

仓库惯例（执行器须遵守）：
- 测试写 handler 级：用 `callProcess` 发 `ProcessInput`、断言解出的 payload map，失败用
  `t.Fatalf("…: %+v", payload)` 带全 payload。结构先例：
  `pkg/tool/process_tool_test.go:101-126` 的 `TestProcessStartWaitsForExitAndLogs`
  （其中 :123-125 已有同款断言形状：`if running, _ := logged["running"].(bool); running { t.Fatalf("a finished command still reports running: %+v", logged) }`——本计划的回归测试就是把它搬到 start+wait 路径）。
- 注释风格：短句说明 WHY（参见本文件各处一行注释），不写多段 docstring。
- 提交信息 conventional：例 `fix(memory): anchor tool paths and refuse traversal`（`git log`）。
- 执行/合并惯例：执行器在隔离 worktree `.worktrees/<slug>`／同名分支改码、**不 push**；
  合并由 owner/reviewer 以 `git merge --ff-only` 落 dev（见 plans/README.md 近期条目）。

## Commands you will need

前置：`third_party/pdfium/$(go env GOOS)_$(go env GOARCH)/` 须存在（仓内已带 `third_party/`；
若缺则 `make pdfium`，需网络；仍失败 → STOP 并报告，不要改 tag 绕过）。

| Purpose   | Command | Expected on success |
|-----------|---------|---------------------|
| Build | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 目标测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestProcess' -count=1` | `ok …/pkg/tool`，全部 TestProcess* PASS |
| 整包测试 | 同上但去掉 `-run` | `ok …/pkg/tool` |
| Vet | `go vet -tags fts5 ./pkg/tool/` | exit 0 |
| Fmt | `gofmt -l pkg/tool` | 无输出 |

（与 Makefile `test:` 目标同口径：`CGO_ENABLED=1` + `-tags fts5` + pdfium 的 PKG_CONFIG_PATH；
`FOREBRAIN_HOME` 指到临时目录避免污染真状态根。）

## Scope

**In scope**（唯一允许修改的文件）:
- `pkg/tool/process_tool.go`（`processStart` 内一行重算）
- `pkg/tool/process_tool_test.go`（新增一条回归测试）

**Out of scope**（看起来相关但不要碰）:
- `pkg/safety/process.go` — `ProcessHandle` 语义正确，`Exited()` 是真相来源；改它 blast radius 覆盖全部沙箱执行。
- `processReadiness` / `processWait` 的返回键集合 — 改形状会波及 `action=wait` 的消费者与既有测试。
- `pkg/tool/process_tool.go` 的 `processLog`/`processKill`/`processList` — 它们都现场重算 `!rec.handle.Exited()`，本就正确。
- TUI / web / 任何渲染层 — 矛盾在 payload，渲染层逐键显示是正确行为。
- 工具描述文案（`processToolDescription`）— 它在缓存的 tools 前缀里，改一处即让每个会话重新计费（先例见 `pkg/tool/process_tool.go:24-40` 注释与 plans/README 368 条目）。
- PID ledger / teardown（plans/358 的领地）。

## Git workflow

- Worktree／分支：`.worktrees/process-start-wait-stale-running`（同名分支），BASE = `e1abd2f4`。
- 提交：一次逻辑提交，信息形如 `fix(tool): stop reporting a stale running flag after start's readiness wait ends on exit`。
- 不 push、不开 PR；合并由 owner/reviewer ff-only 落 dev。

## Steps

### Step 1: 先写回归测试（RED）

在 `pkg/tool/process_tool_test.go` 文件末尾（或 `TestProcessStartWaitsForExitAndLogs` 之后）
新增测试，结构照抄 :101-126 的先例：

```go
// A start whose readiness wait ends on the process's own exit must not carry
// the pre-wait running snapshot: exited and running are mutually exclusive.
func TestProcessStartWithWaitEndingOnExitReportsNotRunning(t *testing.T) {
	_, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", Command: "echo hi", WorkDir: projectDir,
		Wait: "log", Pattern: "NEVER-READY-MARK", TimeoutMs: 15000,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if exited, _ := started["exited"].(bool); !exited {
		t.Fatalf("the readiness wait did not end on the process's exit: %+v", started)
	}
	if ready, _ := started["ready"].(bool); ready {
		t.Fatalf("a process that exited was reported ready: %+v", started)
	}
	if running, _ := started["running"].(bool); running {
		t.Fatalf("an exited process still reports running: %+v", started)
	}
	if code, _ := started["exit_code"].(float64); code != 0 {
		t.Fatalf("exit_code = %v, want 0: %+v", started["exit_code"], started)
	}
}
```

要点：`echo hi` 毫秒级退出、pattern 永不命中 ⇒ 轮询必以退出分支结束（首 tick 若赶上进程
还活，100ms 后的第二 tick 必见退出），不依赖 sleep 或墙钟断言。

**Verify**: 跑目标测试命令 → 该测试 FAIL，失败信息含 `an exited process still reports running`
（这就是基线缺陷的 RED；若它居然 PASS → STOP，根因不在本计划描述处）。

### Step 2: 一行修复（GREEN）

`pkg/tool/process_tool.go` 的 `processStart` 中，把合并循环之后、返回之前改为重算
`running`（:344-347 现状 → 目标）：

```go
	for key, value := range readiness {
		payload[key] = value
	}
	// The wait can end on the process's exit; running was snapshotted before it.
	payload["running"] = !handle.Exited()
	return marshalProcessResult(payload)
```

只加这一行赋值与一行注释；不动其它任何行。三个非退出分支（ready / timeout / 无 wait）
重算结果与快照一致，行为不变。

**Verify**: 跑目标测试命令 → `ok …/pkg/tool`，新测试与全部既有 `TestProcess*` PASS。

### Step 3: 包级验证

**Verify**:
- 整包测试命令 → `ok …/pkg/tool`
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `go vet -tags fts5 ./pkg/tool/` → exit 0
- `gofmt -l pkg/tool` → 无输出

### Step 4: 提交（不 push）

**Verify**: `git status --porcelain` 只列出两个 in-scope 文件；`git log --oneline -1` 为本次提交。

## Test plan

- 新增 1 条：`TestProcessStartWithWaitEndingOnExitReportsNotRunning`（上文 Step 1），
  覆盖本 bug 的精确形状：start+wait、轮询以退出结束 ⇒ `exited:true` 且 `running:false`
  且 `ready:false` 且 `exit_code:0`。
- 既有守卫必须原样绿且**不被修改**：`TestProcessStartWaitsForExitAndLogs`（log 路径的
  running 断言）、`TestProcessWaitTCPReportsReadyWhileRunning`（ready 分支仍报 running:true
  的隐含形状）、`TestProcessWaitTCPTimesOutWithTheLogTail`（timeout 分支）、
  `TestProcessWaitOnAnExitedProcessReturnsImmediately`、`TestProcessKillIsIdempotent`、
  `TestProcessListReflectsStartAndKill`、`TestKillProcessesEndsEveryRunningProcess`、
  `TestProcessWaitAndKillDoNotRace`、`TestProcessLogSpoolGrowsUnderTheStateRoot`。
- 结构先例：`pkg/tool/process_tool_test.go:101-126`。
- Verification: 目标测试命令与整包测试命令均 `ok`。

## Done criteria

机器可检查，ALL 必须成立：

- [ ] `grep -n 'payload\["running"\] = !handle.Exited()' pkg/tool/process_tool.go` 恰好 1 行，且位于合并循环之后、`processStart` 返回之前
- [ ] `grep -c 'NEVER-READY-MARK' pkg/tool/process_tool_test.go` = 1（新测试存在）
- [ ] 目标测试命令与整包测试命令均 exit 0 / `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`go vet -tags fts5 ./pkg/tool/` exit 0；`gofmt -l pkg/tool` 无输出
- [ ] `git status --porcelain` 仅两个 in-scope 文件（+ 索引条目若由执行器维护则再加 `plans/README.md`）
- [ ] RED 证据留存：基线上该新测试按 `an exited process still reports running` 变红（执行记录或 reviewer 复跑）

## STOP conditions

出现以下任一情况，停止并报告，不要即兴发挥：

- `process_tool.go:336-347` 或 `:514-523` 的摘录与活代码内容不符（已漂移）。
- Step 1 的新测试在**未修复**的基线上就 PASS（说明矛盾另有来源，本计划根因描述失效）。
- 修复看似需要改 `processReadiness`/`processWait` 的返回键、其它 action 的 payload、或 `pkg/safety`。
- `third_party/pdfium/$(go env GOOS)_$(go env GOARCH)` 缺失且 `make pdfium` 因无网络失败。
- 任一既有 `TestProcess*` 在本修复后变红且无法用"该测试断言了陈旧行为"解释。

## Maintenance notes

- 不变量：`processStart` 的 payload 里任何取自 handle 状态的键，必须在 readiness 等待**之后**
  赋值或重算；合并循环允许 readiness 覆盖同名键，保持"先快照静态字段、等待、合并、重算动态字段"的顺序。
- Reviewer 重点看：新测试是否真的在基线变红（防止"绿测试假修"）；那一行重算是否落在合并循环之后。
- Deferred（本计划刻意不做，另立项时引用本条）：
  1. `processWait` 的两条返回路径都不含 `running` 键（形状不一致但不矛盾）；若日后补齐，退出路径须为 `false`。
  2. `process_tool.go:457` 早退分支的 `"elapsed_ms": 0` 把"进程早已退出"报成 0 毫秒等待，属同一张卡的观感问题，与本 bug 独立。
- 交互面：plans/358 的 teardown（`KillProcesses`）与 ledger reap 都直接读 `handle.Exited()`，
  不受本修复影响；反之本修复也不得改 `handle` 语义。
