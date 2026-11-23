# Plan 294: 续跑不再吞掉行解析错误，异步自动续跑也有前置校验

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/run/subagent.go pkg/run/subagent_test.go && git diff d715dafd -- pkg/run/subagent.go pkg/run/subagent_test.go`
> `290` 与 `293` 改的也是这两个文件；若它们已经执行，重新逐段核对「当前状态」的摘录（行号可变，形状不能变）。对不上即 STOP。
> `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED
- **Depends on**: `290`（同改 `subagent.go`；先做 290 再做本计划）
- **Category**: bug
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

`subagentRecordPin` 的契约注释（`pkg/run/subagent.go:2390-2395`）写得很清楚：「A row that no longer resolves is an error — a continuation must fail loudly rather than quietly run on another model.」实现却在 `subagentRecordContext` 里写成 `if pin, err := subagentRecordPin(...); err == nil { ... }`，**把错误吞掉、不钉任何模型**。

三个具体后果：
1. **异步自动续跑**（`StartSubagentAutoContinue` → `go startSubagentContinuation`）没有 `continueSubagentRecord` 那道前置 guard，且 `base := context.Background()` 不带会话 id，导致 `handOffModelID(ctx, fac)` 恒为空、交接例外不可用。若被钉的行在派发与续跑之间被删除、开关被关掉或凭据被移除（交接执行子代理的 `subagent_allowed` 本来就是 0），错误被吞，子代理静默改跑会话默认模型 —— 正好违背函数自己的注释与 plan 283「选中的模型在批准与派发之间被删除 → 报出可用模型列表」。
2. **同步续跑**（`continueSubagentRecord`）虽然 `:3140` 有 guard 先校验一次，但 `:3145` 的 `subagentRecordContext` 会**再调一次** `subagentRecordPin` → 又一次 `LLMModelStore.Get`，构成 TOCTOU：guard 通过后第二次读失败仍被吞；也与计划 283「一次派发一次主键读」的资源口径不符。
3. 错误既不返回也不落到台账，用户只看到「续跑跑在另一个模型上」，没有任何失败原因。

本计划把「解析一次、显式传递、失败可见」做成结构事实：pin 由调用方解析一次后传进 `subagentRecordContext`，异步路径把解析失败写成该次执行的 `StatusFailed` 结束事件，并把会话 id 带进续跑上下文。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26，CGO，`-tags fts5`）。

- `pkg/run/subagent.go:2384-2448`（契约与缺陷）：
  ```go
  // subagentRecordContext rebuilds the context a subagent's own calls run
  // under from its record: ...
  // subagentRecordPin rebuilds the model pin a record carries. A run pinned to
  // a live row (ModelLive — the model argument named it) rereads that row: the
  // continuation is a new execution, and the row may have been edited or
  // withdrawn since the first one started, so the round runs on the row's
  // current key and endpoint. A row that no longer resolves is an error — a
  // continuation must fail loudly rather than quietly run on another model.
  func subagentRecordPin(ctx context.Context, fac Factory, record agent.HistoryEntry) (SubagentModelOverride, error) { ... }

  func subagentRecordContext(ctx context.Context, fac Factory, record agent.HistoryEntry) context.Context {
      hostCtx := withoutExplicitSkills(ctx)
      if strings.TrimSpace(record.AgentKind) == "fork" { ... }
      else if strings.TrimSpace(record.AgentKind) == "typed" { ... }
      if pin, err := subagentRecordPin(ctx, fac, record); err == nil {
          hostCtx = WithSubagentModelOverride(hostCtx, pin)
      }
      ...
      return hostCtx
  }
  ```
  `subagentRecordPin` 在 `:2412` 调 `usableModelByID(ctx, fac, modelID)`；`ModelLive == false` 或 `modelID == ""` 时早退返回纯 pair pin，不查库。

- `pkg/run/subagent.go:3136-3146`（同步续跑，双次读）：
  ```go
  if _, perr := subagentRecordPin(ctx, fac, record); perr != nil {
      return record, "", perr
  }
  // The subagent's own input runtime is attached by runSubagentExecution; the
  // dispatching agent's is not inherited.
  hostCtx := subagentRecordContext(ctx, fac, record)
  ```
- `pkg/run/subagent.go:2678-2703`（`StartSubagentAutoContinue` → `go startSubagentContinuation(ctx, fac, ch, record, in)`）；测试里都以 `context.Background()` 调它，所以**入参 ctx 不一定带会话 id**，不能靠它传递。
- `pkg/run/subagent.go:2850-2930`（`startSubagentContinuation`，关键片段）：
  ```go
  base := context.Background()
  if surface := ch.surfaceFor(); surface.Frame != nil {
      base = surface.Frame(base)
  }
  hostCtx := subagentRecordContext(base, fac, record)
  ...
  runCtx, cancelRun := context.WithCancelCause(hostCtx)
  defer cancelRun(nil)
  handle := agent.RegistryFor(scopeRoot).Start(lifecycle, ...)
  notifySubagentSpawnedDirect(fac, lifecycle, "user")
  ...
  var outText string
  runErr := subagentWorktreeMissing(record)
  if runErr == nil {
      outText, runErr = runSubagentExecution(runCtx, fac, lifecycle, subagentResumeAttempt(fac, record, in, skills))
  }
  ...
  switch {
  case runErr == nil:                      final.Status = agent.StatusOK
  case errors.Is(runErr, context.Canceled) ...: final.Status = agent.StatusCancelled
  default:                                 final.Status = agent.StatusFailed
                                           final.Error = llm.ExplainError(runErr)
  }
  finishSubagentExecution(fac, handle, final)
  ...
  notifySubagentEndedDirect(fac, final)
  ```
  注释「A recorded worktree that is gone fails this execution instead of running it ... the ended event below settles its row as failed with the reason」已经给出了**现成的失败落账模式**：把错误塞进 `runErr`，跳过执行，让末尾的 switch 统一结算。本计划照它做。
- `pkg/run/subagent.go:4394-4404`：`handOffModelID` 需要 `llm.AgentSessionIDFromContext(ctx)`；`agent.HistoryEntry.SessionID` 是会话 id（测试里 `record := agent.HistoryEntry{SessionID: "conv-row", ...}`）。
- `pkg/run/subagent.go:688-705`：`SubagentSurface.Frame func(ctx) context.Context` 只装 step/approval hooks，不保证带会话 id。

测试调用点（改签名后**全部**要更新，共 6 处，均在 `pkg/run/subagent_test.go`）：
- `:5010` `subCtx := subagentRecordContext(context.Background(), fix.fac, record)`
- `:5093` 同上
- `:7763` `pin, ok := SubagentModelOverrideFromContext(subagentRecordContext(context.Background(), subagentRecordRowFactory(), record))`
- `:7781` 同上（无 row id 的 pair 场景）
- `:8013` `hostCtx := subagentRecordContext(context.Background(), fac, record)`
- `:8034` `toolpkg.WorkingDir(subagentRecordContext(context.Background(), fac, plain))`
- `subagentRecordRowFactory()`（`:7746-7752`）只给 `appcfg.Root{LLMModels: ...}`，**没有**状态库；但那两条测试的 `record.ModelLive` 为 false，`subagentRecordPin` 走早退分支不查库，安全。

其他样板：
- `pkg/run/subagent_test.go:5699`、`:5820`、`:6128` 等用 `StartSubagentAutoContinue(context.Background(), owner, SubagentSurface{}, conv, key, Input{Text:"resume"})` 驱动异步续跑；`goalEventProbe` / `dispatchProbe` 可观察 spawned/ended 事件。
- `context.WithoutCancel` 在本仓已被广泛使用（`pkg/tui/chat_session.go:2247`、`pkg/assembly/compact_lifecycle.go:55` 等），但这里 `ctx` 可能压根没有会话 id，所以本计划用 `record.SessionID` 显式注入。

仓库约定：
- 错误用 `return ..., err` 向上传递，不要 `slog` 一行了事；异步 goroutine 的错误必须落到 ended 事件/台账，不能只打印。
- `llm.ExplainError(err)` 是 ended 事件的既有错误文本来源，复用它。
- 三种派发错误文案逐字定稿，不改。
- 不新增生产文件、不新增 import 边（`llm`、`agent`、`state`、`context` 本文件已 import）。
- `pkg/architecture` 要求 `X_test.go` 对应 `X.go`：测试只写进既有 `subagent_test.go`。
- `gofmt` 干净；注释用英文。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentContinuationFailsLoudlyOnAMissingRow$' -count=1 -v` | `--- PASS` |
| 异步续跑回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^Test(SubagentContinuation|AutoContinue|EverySubagentExecution|ExecutionEnd)' -count=1 -v` | 全部 `--- PASS`，无 FAIL |
| 全包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok` |
| 回归包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/tool ./pkg/state -count=1` | 全部 `ok` |
| 架构规则 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 / Vet / 格式 | `CGO_ENABLED=1 go build -tags fts5 ./...` / `CGO_ENABLED=1 go vet -tags fts5 ./...` / `gofmt -l pkg cmd` | exit 0 / 无输出 / 无输出 |

## Scope

**In scope**（只允许改这两个文件）：
- `pkg/run/subagent.go` —— `subagentRecordContext`、`continueSubagentRecord`、`startSubagentContinuation`。
- `pkg/run/subagent_test.go` —— 6 处调用点更新 + 新增回归用例。

**Out of scope**（不许动）：
- `subagentRecordPin` / `usableModelByID` / `subagentModelOverrideForRef` 的解析规则（由 `290`、`293` 处理）。
- `subagentExecContext`、`prepareSubagentExecutionResolved`。
- `SubagentSurface.Frame` 的契约、TUI / 网关的续跑入口、`pkg/turn/*`、`pkg/agent/*`。
- 三种派发错误文案、`model` 参数说明与任何模型可见文本。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `fix(run): fail a continuation when its pinned model row no longer resolves`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

> **执行顺序（必读）**：本计划的回归测试是 **Step 5**，但必须**先写、先跑红**，再改生产代码。
> 顺序：Step 5（写测试 → 跑一次，确认当前吞错导致执行 `StatusOK` 的红）→ Step 1–4（修复与调用点更新）
> → 再跑 Step 5 的验证取绿 → Step 6（收口回归）。红/绿两次输出都要进执行报告。

### Step 1: 让 `subagentRecordContext` 接收已解析的 pin（消除吞错与双次读）

把签名改成：

```go
func subagentRecordContext(ctx context.Context, fac Factory, record agent.HistoryEntry, pin SubagentModelOverride) context.Context
```

并**删掉**函数体里这两行：

```go
if pin, err := subagentRecordPin(ctx, fac, record); err == nil {
    hostCtx = WithSubagentModelOverride(hostCtx, pin)
}
```

换成无条件使用传入的 pin：

```go
hostCtx = WithSubagentModelOverride(hostCtx, pin)
```

在注释里补一句：pin 由调用方解析一次并负责传播错误，本函数不再解析、也不吞错。其余部分（kind flags、worker session、roster key 等）一律不动。

**Verify**：此时**故意先不编译**，先做 Step 2/3 把两处生产调用点改完，再一起编译；避免中间态误判。

### Step 2: 同步续跑只解析一次

`continueSubagentRecord`（`:3136-3146`）现在有 guard 与 `hostCtx` 两行。改成：

```go
pin, perr := subagentRecordPin(ctx, fac, record)
if perr != nil {
    return record, "", perr
}
// The subagent's own input runtime is attached by runSubagentExecution; the
// dispatching agent's is not inherited.
hostCtx := subagentRecordContext(ctx, fac, record, pin)
```

原 guard 的注释（「A run pinned to a live row rereads it first ...」）保留在原位、措辞不变。**不要**再加第二次 `subagentRecordPin`。

**Verify**：`grep -n 'subagentRecordPin(ctx, fac, record)' pkg/run/subagent.go` 应只剩 1 处（Step 3 还会加一处用 `base`，见下）；`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` 编译通过（前提是 Step 3 也做完）。

### Step 3: 异步自动续跑：带会话 id、解析一次、失败落账

在 `startSubagentContinuation`（`:2850-2930`）里改三处：

1. **base 带上会话 id**（保持「fresh background context，续跑比请求活得久」的语义，同时让 `handOffModelID` 能读到交接记录）：
   ```go
   base := context.Background()
   if sid := strings.TrimSpace(record.SessionID); sid != "" {
       base = llm.WithAgentSessionID(base, sid)
   }
   if surface := ch.surfaceFor(); surface.Frame != nil {
       base = surface.Frame(base)
   }
   ```

2. **解析一次，失败变成该次执行的 `runErr`**（照函数里已有的 worktree-missing 模式，不要提前 `return`，让 spawned/ended 成对落账）：
   ```go
   pin, pinErr := subagentRecordPin(base, fac, record)
   hostCtx := base
   if pinErr == nil {
       hostCtx = subagentRecordContext(base, fac, record, pin)
   }
   ```
   然后在 `runErr := subagentWorktreeMissing(record)` 之后、`if runErr == nil { outText, runErr = runSubagentExecution(...) }` 之前插入：
   ```go
   if runErr == nil && pinErr != nil {
       runErr = pinErr
   }
   ```
   这样末尾的 `switch` 会把它结算成 `agent.StatusFailed` + `llm.ExplainError(pinErr)`，`finishSubagentExecution` / `notifySubagentEndedDirect` 照常发布。在 `pinErr` 处补一句英文注释，说明「a continuation whose row no longer resolves fails the execution with the refusal text instead of quietly running on the conversation's default model」。

   注意：`pinErr != nil` 时 `hostCtx` 仍是 `base`，`runCtx` 由它派生，但 `runSubagentExecution` 不会被执行 —— 不要为了「让 ctx 好看」而手工塞一个空 pin。

3. **不要**改 `StartSubagentAutoContinue` 的签名，也不要改 `SubagentSurface`。

**Verify**：
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0
- `grep -n 'if pin, err := subagentRecordPin' pkg/run/subagent.go` 无输出（吞错已消失）
- `grep -nE 'pinErr|WithAgentSessionID\(base' pkg/run/subagent.go` 有命中

### Step 4: 更新 6 处既有测试调用点

在 `pkg/run/subagent_test.go` 里逐个改：

- `:5010`、`:5093`、`:8013`、`:8034`：在参数末尾加 `SubagentModelOverride{}`（这些用例只关心 working dir / worker session / 压缩前缀，不关心 pin）。
- `:7763`（`TestSubagentRecordContextKeepsTheRowTheRecordNames`）与 `:7781`（`TestSubagentRecordContextResolvesThePairOnlyWithoutARow`）：先解析再传：
  ```go
  fac := subagentRecordRowFactory()
  recordPin, err := subagentRecordPin(context.Background(), fac, record)
  if err != nil { t.Fatalf("resolve the record's pin: %v", err) }
  pin, ok := SubagentModelOverrideFromContext(subagentRecordContext(context.Background(), fac, record, recordPin))
  ```
  两条用例的断言（`pin.ModelID == "row-2"` / `"row-1"`）**一字不改**，必须继续通过。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentRecordContext(KeepsTheRowTheRecordNames|ResolvesThePairOnlyWithoutARow)$' -count=1 -v` → 两条都 `--- PASS`；`grep -c 'subagentRecordContext(' pkg/run/subagent_test.go` 与改前一致（6 处，加上新用例里的调用另计）。

### Step 5: 补一条「行消失时异步续跑必须失败」的回归测试

在 `pkg/run/subagent_test.go` 新增 `func TestSubagentContinuationFailsLoudlyOnAMissingRow(t *testing.T)`。用 `newModelArgumentFixture(t, subagentsEnabledConfig())` + `fix.seedModelRow(t, "zhipuai", "glm-5.3", "https://cont.test/v1", "key-cont", true)` 造一行，构造 `agent.HistoryEntry{SessionID: "session-1", ModelID: <rowID>, ModelLive: true, ModelProvider: "zhipuai", Model: "glm-5.3", AgentKind: "typed", AgentType: "explore", TaskID: "task-cont", RunID: "run-cont", WorkerSessionID: <worker>, Continuable: true}`；先断言 `subagentRecordPin(ctx, fix.fac, record)` 成功，随后**删掉该行**（`fix.store.Delete(context.Background(), rowID)`，`pkg/state/config_store.go:748`），再断言 `subagentRecordPin` 返回 error 且错误文本含 `no configured model has id`。

然后驱动异步续跑，**用确定性的 ended 事件等待，不要 sleep / 轮询**：

1. 给 fixture 的 owner 换上带通道的探针（`notifySubagentEndedDirect` 就发在 `fac.Owner.Events` 上，见 `pkg/run/subagent.go:2344-2355`）：
   ```go
   probe := &dispatchProbe{endedCh: make(chan endedMsg, 1)}
   fix.fac.Owner.Events = probe
   ```
2. 沿 `pkg/run/subagent_test.go:5689-5700` 的形状先 `seedChannelRecord(t, fix.fac, record)`（按 `seedStoppableSubagent` 的写法），再 `StartSubagentAutoContinue(context.Background(), fix.fac.Owner, SubagentSurface{}, "session-1", "task-cont", Input{Text: "resume"})`。
3. 照 `pkg/run/subagent_test.go:1221-1243` 的 `select` 写法收事件（`case ended := <-probe.endedCh:` / `case <-time.After(2 * time.Second): t.Fatal("…")`），断言：
   - `ended.Status == string(agent.StatusFailed)`（不是 `StatusOK`）；
   - `strings.Contains(ended.Error, "no configured model has id")`（即拒绝文案真的落账，不是只打日志）。

再加一条正例：**同一行未删除**时同样的异步续跑 `StatusOK`，并用 `fixedSubagentExecutor`（`pkg/run/subagent_test.go:2267-2282`：它把 `req.Model` 记到 `model`、把 ctx 上 pin 的 `Model` 记到 `pinnedModel`，且从不发 LLM 请求）断言 `pinnedModel == "glm-5.3"` —— 证明行还在时 pin 照旧生效、正常路径没被一起关掉。

失败用例里也顺手断言「根本没进执行器」：同一条 `fixedSubagentExecutor` 的 `task` 字段此时应为 `""`（`runErr` 在 `runSubagentExecution` 之前就置上了）。

若 `StartSubagentAutoContinue` 的装配成本过高，允许改为直接调用 `startSubagentContinuation`（同包可访问），但必须在报告里说明为什么，并且仍要断言 ended 事件的状态与错误文本 —— 不允许只断言 `subagentRecordPin` 返回 error 就算完。

**Verify（先红后绿）**：在做 Step 1–4 之前先写这条测试并跑一次：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentContinuationFailsLoudlyOnAMissingRow$' -count=1 -v` → **FAIL**（当前吞错 → 执行 `StatusOK`）；修复后同一命令 → `--- PASS`。两次输出都写进执行报告。

### Step 6: 收口回归

**Verify**（全部要跑）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`（注意本仓曾有一次 `TestRosterStopReachesEveryExecutionOfTheSubagent` 并行负载抖动，单跑与整包复跑均过；若复现，单跑确认后记录，不要放宽断言）
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/tool ./pkg/state -count=1` → 全部 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → 无输出
- `gofmt -l pkg cmd` → 无输出

## Test plan

- 新测试：`pkg/run/subagent_test.go` 的 `TestSubagentContinuationFailsLoudlyOnAMissingRow`，覆盖：
  - happy path：行还在时异步续跑成功且 pin 带正确 `ModelID`；
  - 回归一：行被删后 `subagentRecordPin` 报错；
  - 回归二（本计划核心）：该错误落成该次执行的 `StatusFailed` ended 事件，错误文本是定稿的拒绝文案，且没有 LLM 请求；
  - 回归三：带会话 id 后，交接例外（未授权行）在续跑时仍能被 `handOffModelID` 读到（可用 `state.HandOffPlan` + 未授权行做一条断言；若装配困难，写进报告并说明未覆盖）。
- 既有用例：`TestSubagentRecordContextKeepsTheRowTheRecordNames`、`TestSubagentRecordContextResolvesThePairOnlyWithoutARow`、所有 `StartSubagentAutoContinue` 系列（`:5699` 起）断言一字不改地通过。
- 结构样板：`pkg/run/subagent_test.go:8523-8580`（fixture + hand-off 状态）与 `:5699`（异步续跑驱动）。
- 验证：Step 5 红→绿；禁止用 `-run` 零匹配当证据。

## Done criteria

- [ ] `grep -n 'if pin, err := subagentRecordPin' pkg/run/subagent.go` 无输出
- [ ] `grep -c 'func subagentRecordContext' pkg/run/subagent.go` = `1`，且签名含 `pin SubagentModelOverride`
- [ ] `grep -nE 'pinErr != nil|WithAgentSessionID\(base' pkg/run/subagent.go` 各有命中
- [ ] `grep -n 'subagentRecordPin(ctx, fac, record)' pkg/run/subagent.go` 只剩同步路径 1 处；`subagentRecordPin(base, fac, record)` 1 处
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentContinuationFailsLoudlyOnAMissingRow$' -count=1 -v` → `--- PASS`（修复前 FAIL 证据在报告里）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/tool ./pkg/state -count=1` 全 `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`CGO_ENABLED=1 go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 全绿
- [ ] `git status --porcelain -- pkg/run` 只列出 `subagent.go` 与 `subagent_test.go`
- [ ] `plans/README.md` 中本计划状态行已更新

## STOP conditions

- Drift check 显示两个文件在 `d715dafd` 之后被改过且摘录对不上（`290`/`293` 已动过同一文件；行号漂移可接受，形状必须一致）。
- `290` 尚未执行且你打算同时改 `subagentRecordPin` 的 `Caps` 投影 —— 先做 `290`，不要在本计划里顺手改。
- `record.SessionID` 实际是 worker session id 而不是会话 id（导致 `handOffModelID` 读不到交接记录）—— 先报告，不要改 `state.Get` 的键或另造上下文键。
- 把 `subagentRecordContext` 改成返回 `(context.Context, error)` 看起来更省事：本计划已选定「传入已解析 pin」的形状，擅自换方案会与 6 处测试更新和异步落账设计冲突，先报告再定。
- 异步 goroutine 的失败无法落到 ended 事件（例如 `finishSubagentExecution` / `notifySubagentEndedDirect` 依赖尚未建立的 lifecycle）—— 不要靠 `return` 提前退出留下永远 running 的 roster 行，先报告。
- 新增 import 边、`pkg/architecture` 变红，或必须放宽既有续跑用例断言。
- 同一验证连续两次失败。

## Maintenance notes

- 以后任何「续跑 / 重建上下文」的新入口，都必须先 `subagentRecordPin` 解析一次、显式传播错误，再调 `subagentRecordContext`；不要在新函数里再调一次解析。`grep -n 'subagentRecordContext(' pkg/run/*.go` 是快速检查点。
- 异步 goroutine 的错误一律走「塞进 `runErr` → 末尾 switch → `finishSubagentExecution` / `notifySubagentEndedDirect`」这条既有模式；本仓已有 `subagentWorktreeMissing` 先例。任何 `return` 提前退出都必须先想清楚 spawned 事件已发布后 roster 行怎么收口。
- `base := context.Background()` 被换成了带 `record.SessionID` 的版本，但**仍不继承请求 ctx 的取消**（续跑要比请求活得久）；`SubagentSurface.Frame` 加在会话 id 之后，不要反过来。
- 与 `290` 的关系：290 补 `subagentRecordPin` 的 `Caps`，本计划补它的错误传播；两者都动 `subagentRecordPin` 附近，合并时以 290 的字段投影为准。
- 明确延后：`startSubagentContinuation` 与 `continueSubagentRecord` 之间仍有两套续跑入口的重复逻辑（未来可抽 `resolveContinuationPin`）；本计划不重构，避免扩大爆炸半径。
