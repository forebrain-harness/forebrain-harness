# Plan 371: 阻塞式 process 调用的活卡必须流式显示命令输出（`tool_output_delta`）

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving on. If anything in the
> "STOP conditions" section occurs, stop and report — do not improvise.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/tool/process_tool.go pkg/tool/process_tool_test.go pkg/tool/shell_tool.go pkg/tool/shell_output.go`
> `git diff --stat e1abd2f4 -- pkg/tool/process_tool.go pkg/tool/process_tool_test.go pkg/tool/shell_tool.go pkg/tool/shell_output.go`
> 用 `git diff <sha> -- <paths>` 对**工作树**比，**不要**用 `e1abd2f4..HEAD` 的范围 diff（看不到未提交改动）。
> 前两个是本计划 in-scope 文件；后两个只读参照。基线 `e1abd2f4` 之后 dev 已前进，下列漂移属**预期**，
> 逐段比对下文摘录、内容相符即继续，**不 STOP**：
> - 兄弟计划 370 也改这两个 in-scope 文件（`processStart` 合并循环后一行 `payload["running"]` 重算＋一条新测试）——
>   与本计划的 emitter 接线/尾读落在不同代码段；370 的新测试以 `TestProcess` 开头、会被目标命令选中且应绿。
> - `pkg/tool/state.go` 的 `StepEvent` 新增了 `Canceled` 字段（stop-display 工作），本计划引用的
>   `WithStepHook`/`StepHookFromContext`/`WithToolStepID` 等内容不变。
> 摘录内容不符 → STOP。行号漂移不算漂移，以内容为准。

## Status

- **Priority**: P1（体验缺陷：阻塞窗口内活卡输出区空白，用户/模型只能干等数分钟；owner 报障截图 "Calling process 1m 47s" 无任何输出）
- **Effort**: M
- **Risk**: LOW-MED（只新增事件发射与一个日志尾读器；不改 payload 形状、不改渲染层、不改 shell）
- **Depends on**: none（与 370 互不依赖，可任意顺序落）
- **Category**: dx
- **Planned at**: commit `e1abd2f4`, 2026-11-23

## Why this matters

owner 报障：阻塞式 `process` 调用的活卡（"Calling process 1m 47s"）output 区域没有任何实时
stdout/stderr，用户干等几分钟甚至十几分钟，体验不如 shell 命令卡。

根因（advisor 亲读确认）：shell 工具的实时输出走**通用**活卡通道——
`shell_tool.go:502-528` 用 `newShellOutputEmitter` 把管道 chunk 合并（75ms / 32KB 上限）后
以 `StepEvent{Kind: StepKindToolOutputDelta, …}` 发给 step hook；TUI 在
`pkg/tui/notify.go:586-607` 按 StepID 把任意工具的 delta 渲染进活卡（`ToolOutputDelta: true`），
gateway 以 `tool_output_delta`（`pkg/event/run_events.go:21`）走 ws 推给 web。
而 process 工具的 `OnOutput`（`process_tool.go:301-303`）**只把 chunk 写进 log 文件**，
从不构造 emitter、从不发 delta 事件 ⇒ 整个阻塞窗口（start+wait 的 readiness 轮询、或独立
`wait` 调用的轮询）活卡输出区空白。

修法因此不需要碰任何渲染层：让 process 工具发同款事件即可，TUI 与 web 同时受益。
两个阻塞入口都要覆盖：
1. `start` 带 `wait`：管道 chunk 直接进 emitter（镜像 shell 的接线）；
2. 独立 `wait`：进程是更早的调用起的，管道已无人读，改为在轮询期间增量尾读 log 发 delta。

## Current state（advisor 亲读，基线 `e1abd2f4`）

- `pkg/tool/process_tool.go` — process 工具。缺陷点：
  - `:301-303` OnOutput 只写 log：
    ```go
    	onOutput := func(_ safety.OutputStream, chunk []byte) {
    		_, _ = logFile.Write(chunk)
    	}
    ```
  - `:271-348` `processStart`：无任何 `StepHookFromContext` / emitter 接线；阻塞点在
    `:340` 的 `processReadiness`。
  - `:448-466` `processWait`：早退分支（进程已退出）后直接 `processReadiness`，阻塞轮询期间无任何输出反馈。
  - `:470-542` `processReadiness`：轮询循环（100ms→500ms interval），本计划**不改其签名与逻辑**。
- `pkg/tool/shell_tool.go:502-528` — 要镜像的参照接线（逐字摘录）：
  ```go
  	var onOutput func(safety.OutputStream, []byte)
  	var outputEmitter *shellOutputEmitter
  	if step := StepHookFromContext(ctx, st); step != nil {
  		stepID := ToolStepIDFromContext(ctx)
  		startedAt := ToolStepStartedAtFromContext(ctx)
  		outputEmitter = newShellOutputEmitter(func(stream safety.OutputStream, chunk []byte) {
  			if len(chunk) == 0 {
  				return
  			}
  			step(ctx, StepEvent{
  				Kind:            StepKindToolOutputDelta,
  				StepID:          stepID,
  				ToolName:        req.toolName,
  				ToolDescription: req.toolDescription,
  				StartedAt:       startedAt,
  				Input: map[string]any{
  					"command": req.command,
  				},
  				Output: map[string]any{
  					"stream": string(stream),
  					"chunk":  string(chunk),
  				},
  			})
  		})
  		onOutput = outputEmitter.Push
  	}
  	defer outputEmitter.Close()
  ```
- `pkg/tool/shell_output.go:23-124` — `shellOutputEmitter`：`Push` 与 `Close` 均 nil-safe
  （:54-56、:115-118），`Close` 会先 flush 再返回（:80-82）⇒ 快命令的输出不会丢；
  合并常量 `liveOutputFlushInterval = 75ms`、`liveOutputPendingBytes = 32KB`（:23-26）。
- `pkg/tool/state.go:2068-2083` — `WithStepHook(ctx, hook)` / `StepHookFromContext(ctx, fallback *State)`；
  `State.SetStepHook` 存在（测试先例 `shell_tool_test.go:304`）。
- `pkg/tool/state.go:795-823` — `WithToolStepID` / `ToolStepIDFromContext` /
  `WithToolStepStartedAt` / `ToolStepStartedAtFromContext`。
- 消费端（只读参照，**不改**）：`pkg/tui/notify.go:586-607`（delta → `MsgKindTool` +
  `ToolOutputDelta: true`，按 StepID 归卡）；`pkg/tui/chat_turn.go:349-364`；
  gateway `tool_output_delta` ws 流（`pkg/gateway/stream_parts_test.go:41-43` 佐证形状）。
- 投递策略 `ToolStepUserVisible`（`pkg/tool/notification.go:244-256`）只看 `SuppressUI`／
  错误／action 字段，**没有按工具名的白名单** ⇒ process 的 delta 天然可达 UI。

测试注入先例（逐字参照 `pkg/tool/shell_tool_test.go:301-329`）：

```go
	st := NewState(t.TempDir())
	var deltas []StepEvent
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		if evt.Kind == StepKindToolOutputDelta {
			deltas = append(deltas, evt)
		}
	})
	…
	ctx := WithToolStepID(context.Background(), "shell-step-1")
```

仓库惯例：handler 级测试 + `callProcess` 辅助（`process_tool_test.go:46-62`）；
danger-full-access 配置钉工具不钉沙箱（`process_tool_test.go:18-40`）；
注释写 WHY 一行；提交 conventional（`fix(tool): …`）；worktree `.worktrees/<slug>` 同名分支、
不 push、owner/reviewer ff-only 落 dev。

## Commands you will need

前置：`third_party/pdfium/$(go env GOOS)_$(go env GOARCH)/` 须存在（缺则 `make pdfium`，需网络；失败 → STOP）。

| Purpose   | Command | Expected on success |
|-----------|---------|---------------------|
| Build | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 目标测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestProcess' -count=1` | `ok …/pkg/tool` |
| 抖动复核 | 见下方代码块（含 `|` 的命令不放表格：GFM 表格会吞裸竖线、逼出 `\|` 转义假绿） | 3 轮全 PASS |
| 整包测试 | 目标测试命令去掉 `-run` | `ok …/pkg/tool` |
| Vet | `go vet -tags fts5 ./pkg/tool/` | exit 0 |
| Fmt | `gofmt -l pkg/tool` | 无输出 |

抖动复核命令（裸 `|` 是 Go regexp 的或；**不要**写成 `\|`，那会匹配零个测试并 exit 0 假绿）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ \
  -run 'TestProcessStartWithWaitStreamsOutputDeltas|TestProcessWaitStreamsLogDeltasWhilePolling' -count=3 -v
```

预期：3 轮里两条测试各 `--- PASS` 共 6 行（用 `-v` 数 `--- PASS` 证明真选中了测试，`-run` 零匹配也 exit 0）。

## Scope

**In scope**:
- `pkg/tool/process_tool.go`（start 的 emitter 接线；wait 的日志尾读 emitter；新增 `processLogTailer`）
- `pkg/tool/process_tool_test.go`（新增 2 条测试）

**Out of scope**（看起来相关但不要碰）:
- `pkg/tool/shell_tool.go` / `shell_output.go` — 只复用 emitter，不改它。
- `pkg/tui/*`、`pkg/gateway/*`、`frontend/` — 通道是通用的，渲染层零改动；若发现需要改渲染层 → STOP。
- `processReadiness` 的签名与轮询逻辑 — 尾读用独立 goroutine，不侵入轮询。
- 完成 payload 形状（那是 370 的领地）；`log`/`kill`/`list` action。
- `processToolDescription` 文案 — 缓存 tools 前缀，改一处全会话重新计费。

## Git workflow

- Worktree／分支：`.worktrees/process-live-output-deltas`（同名分支），BASE = **派发时的 dev HEAD**
  （`e1abd2f4` 是起草基线，dev 已前进；从旧 SHA 建分支会无法 ff-only 落 dev）。与 370 共改两文件，
  建议 370 先落、本计划 rebase 到其后再落（漂移核对以内容为准，见顶部）。
- 提交：一次逻辑提交，信息形如 `fix(tool): stream live output into the blocking process card`。
- 不 push、不开 PR；合并由 owner/reviewer ff-only 落 dev。

## Steps

### Step 1: `start` 镜像 shell 的 emitter 接线

在 `processStart` 中，把 `:301-303` 的 onOutput 段替换为（位置不变，仍在 `reserve` 之后、
`manager.StartCommand` 之前）：

```go
	var outputEmitter *shellOutputEmitter
	if step := StepHookFromContext(ctx, st); step != nil {
		stepID := ToolStepIDFromContext(ctx)
		startedAt := ToolStepStartedAtFromContext(ctx)
		outputEmitter = newShellOutputEmitter(func(stream safety.OutputStream, chunk []byte) {
			if len(chunk) == 0 {
				return
			}
			step(ctx, StepEvent{
				Kind:      StepKindToolOutputDelta,
				StepID:    stepID,
				ToolName:  "process",
				StartedAt: startedAt,
				Input: map[string]any{
					"command": command,
					"id":      id,
				},
				Output: map[string]any{
					"stream": string(stream),
					"chunk":  string(chunk),
				},
			})
		})
	}
	defer outputEmitter.Close()
	// The log keeps every byte; the emitter is the lossy live preview beside it.
	onOutput := func(stream safety.OutputStream, chunk []byte) {
		_, _ = logFile.Write(chunk)
		outputEmitter.Push(stream, chunk)
	}
```

要点：`Push`/`Close` 对 nil emitter 安全（无 step hook 时行为与今天完全一致）；
`defer Close()` 的 flush 语义保证快命令的输出在返回前送达（shell 的同款测试依赖它）。
`ToolDescription` 不填（process 卡不需要；shell 填的是它的请求结构字段，此处无对应物）。

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0；既有 `TestProcess*` 全绿
（此步尚无新测试，行为对无 hook 调用者不变）。

### Step 2: 新增 `processLogTailer` 与 `wait` 的尾读 emitter

在 `process_tool.go`（建议放在 `processWait` 之前）新增：

```go
// processLiveTailInterval is how often a blocking wait reads what its process
// appended to the log since the last read.
const processLiveTailInterval = 250 * time.Millisecond

// processLogTailer streams a log file forward from the moment a wait starts:
// the live card is a preview of what is happening now, and the output that
// predates the call belongs to `log`.
type processLogTailer struct {
	path   string
	offset int64
	seen   bool
}

// delta returns the bytes appended since the previous call, bounded per tick so
// a noisy process cannot make one read unbounded. The first call seeds the
// offset at the current end and returns nothing.
func (t *processLogTailer) delta() []byte {
	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	if !t.seen {
		t.seen = true
		t.offset = info.Size()
		return nil
	}
	if info.Size() <= t.offset {
		return nil
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil
	}
	limit := info.Size() - t.offset
	if limit > liveOutputPendingBytes {
		limit = liveOutputPendingBytes
	}
	buf := make([]byte, limit)
	n, _ := io.ReadFull(f, buf)
	if n <= 0 {
		return nil
	}
	t.offset += int64(n)
	return buf[:n]
}
```

（`io` 需加入 import；`os`/`time`/`safety` 已在。）紧接着在同一文件新增启停辅助函数
（**定死这种组织方式**，不要另选；它把尾读 goroutine 的启停包成一个返回 `stop func()` 的函数，
无 hook 时返回 no-op，`processWait` 的主体一字不动）：

```go
// startProcessLiveTail streams rec's log into the live card while a wait polls.
// The caller defers the returned stop. With no step hook it is a no-op, so a
// hookless wait behaves exactly as before.
func startProcessLiveTail(ctx context.Context, st *State, rec *processRecord) func() {
	step := StepHookFromContext(ctx, st)
	if step == nil {
		return func() {}
	}
	stepID := ToolStepIDFromContext(ctx)
	startedAt := ToolStepStartedAtFromContext(ctx)
	emitter := newShellOutputEmitter(func(stream safety.OutputStream, chunk []byte) {
		if len(chunk) == 0 {
			return
		}
		step(ctx, StepEvent{
			Kind:      StepKindToolOutputDelta,
			StepID:    stepID,
			ToolName:  "process",
			StartedAt: startedAt,
			Input:     map[string]any{"id": rec.id},
			Output: map[string]any{
				"stream": string(stream),
				"chunk":  string(chunk),
			},
		})
	})
	tailer := &processLogTailer{path: rec.logPath}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(processLiveTailInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if d := tailer.delta(); len(d) > 0 {
					// The log interleaves both streams; the card shows one preview.
					emitter.Push(safety.OutputStreamStdout, d)
				}
			}
		}
	}()
	return func() {
		close(stop)
		emitter.Close()
	}
}
```

然后在 `processWait` 的早退分支（`:453-459`）**之后**、`processReadiness` 调用（`:460`）之前插入两行：

```go
	stopTail := startProcessLiveTail(ctx, st, rec)
	defer stopTail()
```

`processWait` 的其余 body（`readiness, err := processReadiness(ctx, rec, in)` 到 return）**一字不动**。
**不要**改 `processReadiness` 的签名或轮询逻辑——尾读完全在独立 goroutine 里，由 `stopTail` 启停。

要点：
- 无 step hook 时返回 no-op，hookless wait 行为与今天完全一致。
- `stopTail` 先 `close(stop)`（ticker goroutine 在下一个 select 退出，有界、无泄漏），再 `emitter.Close()`
  flush 最后一批。goroutine 若在 `close(stop)` 瞬间正在 Push，那一批可能赶不上最终 flush 而丢——
  活卡本就是**有损预览**，log 文件才是全量真相，可接受。
- `emit` 回调只在 emitter 自己的 run goroutine 上跑；`emitter.Close()` 等 `done`（run 退出）后才返回，
  故测试在 `callProcess` 返回后读 `deltas` 有 happens-before 保证，无数据竞争。

**Verify**: build exit 0；既有 `TestProcess*` 全绿。

### Step 3: 两条回归测试

在 `pkg/tool/process_tool_test.go` 新增（注入先例照抄 `shell_tool_test.go:301-329`）：

```go
// A blocking start+wait must stream the command's output into the live card:
// the owner stared at an empty output area for the whole wait.
func TestProcessStartWithWaitStreamsOutputDeltas(t *testing.T) {
	st, tl, _, projectDir := newProcessToolForTest(t)
	var deltas []StepEvent
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		if evt.Kind == StepKindToolOutputDelta {
			deltas = append(deltas, evt)
		}
	})
	ctx := WithToolStepID(processTestContext(projectDir), "process-step-1")
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", Command: "echo live-process-output", WorkDir: projectDir,
		Wait: "log", Pattern: "NEVER-READY-MARK", TimeoutMs: 5000,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(deltas) == 0 {
		t.Fatal("a blocking start+wait emitted no output deltas")
	}
	var combined strings.Builder
	for _, evt := range deltas {
		if evt.StepID != "process-step-1" {
			t.Fatalf("delta StepID = %q, want process-step-1", evt.StepID)
		}
		if evt.ToolName != "process" {
			t.Fatalf("delta ToolName = %q, want process", evt.ToolName)
		}
		chunk, _ := evt.Output["chunk"].(string)
		combined.WriteString(chunk)
	}
	if !strings.Contains(combined.String(), "live-process-output") {
		t.Fatalf("deltas never carried the command's output: %q", combined.String())
	}
	// Await the exit watcher's ledger write so it cannot land during t.TempDir's
	// RemoveAll (the flake waitForProcessSettled's comment documents); the echo
	// already exited via the readiness wait's exit branch.
	waitForProcessSettled(t, processRecordForTest(t, st, processTestID(t, started)))
}

// A blocking wait on a process an earlier call started must stream what the
// process appends to its log while the wait polls.
func TestProcessWaitStreamsLogDeltasWhilePolling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ticking command is a POSIX shell loop")
	}
	st, tl, _, projectDir := newProcessToolForTest(t)
	ctx := processTestContext(projectDir)
	started, err := callProcess(t, tl, ctx, ProcessInput{
		Action: "start", WorkDir: projectDir,
		Command: `sh -c 'i=0; while [ $i -lt 20 ]; do echo tick-$i; i=$((i+1)); sleep 0.2; done'`,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := processTestID(t, started)
	var deltas []StepEvent
	st.SetStepHook(func(_ context.Context, evt StepEvent) {
		if evt.Kind == StepKindToolOutputDelta {
			deltas = append(deltas, evt)
		}
	})
	waitCtx := WithToolStepID(ctx, "process-step-2")
	if _, err := callProcess(t, tl, waitCtx, ProcessInput{
		Action: "wait", ID: id, Wait: "tcp", Address: "127.0.0.1:1", TimeoutMs: 3000,
	}); err != nil {
		t.Fatalf("wait: %v", err)
	}
	var combined strings.Builder
	for _, evt := range deltas {
		if evt.StepID != "process-step-2" {
			t.Fatalf("delta StepID = %q, want process-step-2", evt.StepID)
		}
		chunk, _ := evt.Output["chunk"].(string)
		combined.WriteString(chunk)
	}
	if !strings.Contains(combined.String(), "tick-") {
		t.Fatalf("the wait streamed none of the log it tailed: %q", combined.String())
	}
}
```

（`runtime` 需加入测试文件 import。）第一条同时是 370 形状的旁证（exited 分支路径），
但**不**替代 370 的断言。第二条的 `sh` 循环在 wait 超时（3s）后仍在跑（约 4s 才自然结束），
由 `t.Cleanup(st.KillProcesses)` 杀掉并 `awaitProcessSettled`，无需手动收尾；第一条的 echo 已退出，
必须自己 `waitForProcessSettled`（`KillProcesses` 只 reap 仍活着的进程）。

**Verify**: 目标测试命令 → 全 PASS；抖动复核命令（`-count=3`）→ 3 轮全 PASS。

### Step 4: 包级验证与提交

**Verify**:
- 整包测试命令 → `ok …/pkg/tool`
- `go vet -tags fts5 ./pkg/tool/` exit 0；`gofmt -l pkg/tool` 无输出
- `git status --porcelain` 只列两个 in-scope 文件
- 提交（不 push）

## Test plan

- 新增 2 条（Step 3），覆盖两个阻塞入口的活卡流式输出。
- 既有守卫必须原样绿且不被修改：全部 `TestProcess*`（尤其
  `TestProcessStartWaitsForExitAndLogs`、`TestProcessWaitTCPTimesOutWithTheLogTail`、
  `TestProcessWaitAndKillDoNotRace`）与 `TestShellToolEmitsOutputDeltasWithToolStepID`
  （shell 侧参照，证明 emitter 语义未被动）。
- 结构先例：`shell_tool_test.go:301-329`（delta 捕获）+ `process_tool_test.go:101-126`（process 侧）。
- Verification: 目标测试 + 整包测试 + `-count=3` 抖动复核全绿。

## Done criteria

机器可检查，ALL 必须成立：

- [ ] `grep -c 'newShellOutputEmitter' pkg/tool/process_tool.go` = 2（start 与 wait 各一）
- [ ] `grep -c 'StepKindToolOutputDelta' pkg/tool/process_tool.go` = 2
- [ ] `grep -n 'processLogTailer' pkg/tool/process_tool.go` ≥ 3（类型定义、delta 方法、`startProcessLiveTail` 内实例化）
- [ ] `grep -c 'SetStepHook' pkg/tool/process_tool_test.go` = 2（两条新测试）
- [ ] 目标测试、整包测试、`-count=3` 抖动复核均 exit 0
- [ ] build / vet / gofmt 干净
- [ ] `git status --porcelain` 仅两个 in-scope 文件
- [ ] `pkg/tui`、`pkg/gateway`、`frontend/` 零改动（`git diff --name-only` 佐证）

## STOP conditions

- `shell_tool.go:502-528` 或 `shell_output.go:23-124` 的摘录与活代码不符（参照漂移）。
- 实测发现 delta 事件对 `process` 被某处按工具名过滤（即 `ToolStepUserVisible` 之外还有白名单）→ 停止并报告，不要改渲染层绕过。
- 新测试在 `-count=3` 下仍抖动两次以上（时序设计有误，报告而非加 sleep）。
- 尾读 goroutine 的启停看似需要改 `processReadiness` 签名或轮询逻辑才能正确。
- pdfium 前置无法满足（无网络）。

## Maintenance notes

- 不变量：活卡输出是**有损预览**（emitter 32KB pending + 75ms 合并），log 文件才是全量真相；
  任何"卡上没看到"的排障先读 `log` action / log 文件。
- `defer emitter.Close()` 不可删：它的 flush 是快命令输出能上卡的唯一保证。
- 尾读刻意跳过 wait 开始前的历史（首 tick 播种 offset）；若日后 owner 要求"wait 也回放历史"，
  那是另一个决定（回放上限、与 start 卡的去重），不要在本计划里顺手做。
- 尾读 delta 一律标 `stdout`：log 本身是混合流。若日后 log 分流存储，此处要 revisited。
- 与 370 的关系：370 修完成 payload 的陈旧 `running`，本计划加活卡事件，语义互不依赖；但**两者改同两个文件**，
  并行执行会互见对方改动（drift check 已列为预期），ff-only 合并须串行——建议 370 先落、本计划 rebase 后再落。

## 评审修订记录（2026-11-23 review-plan，复核时 HEAD `3ff6b134`）

缺口经评审独立实跑证实（`go test -overlay`，仓库树零改动）：基线上 `TestProcessStartWithWaitStreamsOutputDeltas`
按 `a blocking start+wait emitted no output deltas` 变红；`grep -c StepKindToolOutputDelta process_tool.go`=0。
shell 接线 :502-528 逐字、emitter nil-safe/Close flush、`ToolStepUserVisible` 无工具名白名单、消费端
（notify.go:586-607／chat_turn.go:349-364／stream_parts_test.go:41-43）、Done 计数（`SetStepHook` 基线 0）均核实。就地修订：
1. **Step 2 占位函数 `processWaitPoll` 不存在＋「二选一」**（照抄即编译失败）→ 定死 `startProcessLiveTail(ctx, st, rec) func()`
   辅助函数（无 hook 返回 no-op），`processWait` 早退后插 `stopTail := …; defer stopTail()` 两行，主体一字不动；
   给出完整可编译代码与启停顺序（close(stop)→emitter.Close）、有界退出与 happens-before 说明。
2. **抖动命令含裸 `|` 却在 markdown 表格单元格内**（GFM 吞竖线／逼出 `\|` 假绿）→ 移出表格放 fenced block，并写明数 `--- PASS`。
3. **start 测试补 `waitForProcessSettled`**（同 370 的 flake）；wait 测试的 sh 循环由 `KillProcesses` 收尾，已注明。
4. **drift check 语义**（范围 diff→工作树 diff＋status）＋兄弟计划 370 白名单＋`state.go` Canceled 漂移说明；**BASE 改派发时 dev HEAD**。
5. `tailer.delta()` 末尾两个 identical `return buf[:n]` 分支合并为一个（`n, _ := io.ReadFull`）。
结论：缺口成立、修法渲染层零改动且接线已验证，修订后可执行（P1/M/LOW-MED，TODO）。

## 执行记录（2026-11-23 `execute`，评审 APPROVE）

- Worktree／分支：`.worktrees/process-live-output-deltas`（BASE `3ff6b134`＝派发时 dev HEAD，不含尚未合并的 370，
  按计划属预期漂移），提交 `805228db`（评审的这版），**落地时 rebase 到 370＋372 之后为 `75d293f0`**；
  未 push。
- 落地（owner 指示「合并回主树 dev」）：rebase 到新 dev 时唯一冲突是 `process_tool_test.go` 末尾「两端各自追加测试」，
  按事先验证的做法保留 370 的一条＋本计划的两条；rebase 后在该树跑 `TestProcess|TestDisplay|TestHeader` 13 条全 PASS，
  再 `git merge --ff-only` 落 dev（`01b421c9..75d293f0`）。落地后全量门禁唯一失败是 `pkg/architecture` 的
  `TestTestFilesCorrespondToProductionFiles`（`pkg/tui/deliverables_outcome_test.go` 无配对产物，`3ff6b134` 起即存在），
  与本计划无关。
- 改动：`pkg/tool/process_tool.go` +131/−1（`io` import；`processStart` 的 `shellOutputEmitter` 接线与
  `onOutput` 双写 log＋Push；`processLiveTailInterval`／`processLogTailer.delta`／`startProcessLiveTail`；
  `processWait` 早退后 `stopTail := …; defer stopTail()` 两行），`pkg/tool/process_tool_test.go` +83
  （`runtime` import＋`TestProcessStartWithWaitStreamsOutputDeltas`、`TestProcessWaitStreamsLogDeltasWhilePolling`）。
  `processReadiness` 签名与轮询逻辑一字未动；`shell_tool.go`／`shell_output.go` 零改动。
- 评审独立复跑：`-overlay` 覆盖回 `3ff6b134` 版生产文件后两条新测试按
  `a blocking start+wait emitted no output deltas` 与 `the wait streamed none of the log it tailed: ""` 变红；
  工作树 `-run 'TestProcess'` 10 条全 PASS；抖动复核 `-count=3` 数到 6 个 `--- PASS`；补跑执行器因磁盘满
  跳过的 `-race`（两条新测试＋`TestShellToolEmitsOutputDeltasWithToolStepID`，`-count=2`）`ok`；整包
  `ok …/pkg/tool 19.3s`；build／vet／gofmt 干净；`git diff --name-only` 恰两个 in-scope 文件，
  `pkg/tui`／`pkg/gateway`／`frontend/` 零改动。
- 合成态验证（与 370 同改两文件）：`git merge-tree` 预演的唯一冲突是 `process_tool_test.go` 末尾「两边各自
  追加测试」；在临时分支上合并并保留双方三个测试后，11 条 `TestProcess*`＋整包＋build/vet 全绿。
- 落地建议：370 先 ff-only，本分支 rebase 到新 dev 后 ff-only；或直接解那一处纯追加冲突（保留全部测试）。
- 已知边界（计划已裁定，非缺陷）：活卡是有损预览（32KB pending＋75ms 合并），log 文件是全量真相；尾读首个
  tick 播种 offset、跳过 wait 开始前的历史；尾读 delta 一律标 `stdout`（log 本身混合两流）。
