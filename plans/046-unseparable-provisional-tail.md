# Plan 046: 分不清暂定尾巴的调用不压缩（尾巴的归属按历史判定）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 这是计划 006 决策闸门（判定「可达」）后 owner 拍板的三项修复之一，也是 006 原本的 Step 5–7。
> **依赖 045**（`plans/045-compaction-guard-per-history.md`）：本计划复用它在 `pkg/run/llm_middleware.go` 引入的 `historyKey`，并且两份计划改同一个函数
> `applyRunScopedCompact`，**必须串行执行（045 先）**，不要并行派发。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/run/compaction.go pkg/run/llm_middleware.go pkg/run/compaction_test.go`
> 期望只有 045 留下的改动。逐段对照 "Current state" 的摘录；对不上按 STOP 处理。

## Status

- **Priority**: P2
- **Effort**: S（三处改动，合计 < 40 行；测试约 90 行）
- **Risk**: LOW（只在「尾巴分不清」的异常分支上改变行为，今天的链路上该分支只有测试能触发）
- **Depends on**: `plans/045-compaction-guard-per-history.md`
- **Category**: bug
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

计划 003 让编排循环把「随本次模型调用发出、但用户还能撤回」的 steer 标成**暂定尾巴**（provisional tail）：`recoverableLLM` 中途压缩时只把尾巴**之前**的历史压进
checkpoint，尾巴原样接在后面。撤回路径据此假设「被放弃的那次调用若做了压缩，它的 replacement 一定以那几条 steer 结尾」，于是直接裁掉最后 `steerCount` 条
（`pkg/run/orchestration_llm.go:341-348`）。

但 `applyRunScopedCompact` 有一个防御分支：当 run 级缓存的 checkpoint 基线「伸进了暂定消息」时，它把尾巴置 0、**整段压缩**（`pkg/run/llm_middleware.go:322-328`）。
一旦尾巴没被遵守（探针 B 用「编排层与 `recoverableLLM` 之间多一层改消息条数的包装」复现），撤回路径就会把 checkpoint 一起裁掉，会话被裁空：
`session = [assistant:done]`。两处假设（「压缩会尊重尾巴」与「replacement 以 steer 结尾」）隔着一个包装层，谁都不会自己发现对方不成立。

本计划把不变式**从源头**立起来：**checkpoint 永不折入用户还能撤回的消息；一旦这次调用的暂定尾巴分不清在哪，这次就不压缩**——被跳过的那次调用带着更长的
上下文发出去，它的暂定消息随它一起结算，下一次调用照常压缩。

同时必须修掉一个副作用（006 原方案没有覆盖）：暂定尾巴的标记装在 callCtx 上（`pkg/run/orchestration_llm.go:313-315`），而父 turn 的工具调用就在同一个 callCtx 下执行，
所以父调用带 steer 时派发的 fork / 子代理会**继承这个标记**。如果「长度对不上」一律判成「分不清」，那么只要父 turn 有在途 steer，它的每个子代理在整个执行期间都不会压缩
（日志里静默出现 `compaction skipped: provisional messages cannot be told apart`，而 006 的计数校验只有 `1` 所以不会被发现）。所以尾巴的归属也要按**历史**判定：
**标记属于别的历史 → 这次调用本来就没有暂定尾巴 → 正常压缩**；标记属于本历史但长度对不上 → 才是「分不清」。

## Current state

### 暂定尾巴（`pkg/run/compaction.go:267-306`，全文）

```go
// provisionalTail marks the trailing messages of one model call that are not
// settled history yet: the queued steers riding on it — and, when they reopened
// a finished turn, the answer they follow — any of which the user can still
// retract until the call commits. recoverableLLM keeps them out of a
// checkpoint, which is durable: a retracted steer must never survive in it, and
// a steer rolled back after a failed call is sent again, so folding it in would
// send it twice.
type provisionalTail struct {
	sent  int             // length of the message slice the call was handed
	count int             // how many of its trailing messages are provisional
	outer context.Context // the orchestration context, without the call's retraction abort
}

type provisionalTailKey struct{}

// withProvisionalTail marks the last count of the sent messages handed to the
// call made under ctx as provisional. outer is the context the call's own was
// derived from: a compaction runs on its cancellation, not the call's, so a
// retraction that aborts the call does not throw away a checkpoint that never
// included the retracted steer.
func withProvisionalTail(ctx, outer context.Context, sent, count int) context.Context {
	if count <= 0 || count > sent {
		return ctx
	}
	return context.WithValue(ctx, provisionalTailKey{}, provisionalTail{sent: sent, count: count, outer: outer})
}

// provisionalTailLen is how many trailing messages of a sent slice are
// provisional — zero unless ctx describes exactly this slice, so a nested call
// that inherits the context with other messages compacts them whole, as before.
func provisionalTailLen(ctx context.Context, sent int) int {
	if ctx == nil {
		return 0
	}
	pt, ok := ctx.Value(provisionalTailKey{}).(provisionalTail)
	if !ok || pt.sent != sent {
		return 0
	}
	return pt.count
}
```

`withProvisionalTail` 全仓只有一个调用点：`pkg/run/orchestration_llm.go:310-315`

```go
		if inFlightSteers != nil {
			provisionalFrom := sessionBeforeSteers
			if heldAnswer != nil {
				provisionalFrom--
			}
			callCtx = withProvisionalTail(callCtx, ctx, len(session), len(session)-provisionalFrom)
			inFlightSteers.bindAbort(func() { abandonCall(errSteerRetracted) })
		}
```

——工具调用正是从这个 `callCtx` 下发出（同函数随后 `res, err := w.inner.Execute(callCtx, session, tools)`），所以子代理会继承该标记。
`provisionalTailLen` 只有一个调用点：`pkg/run/llm_middleware.go:320`。

### 消费方（045 之后的样子；`pkg/run/llm_middleware.go:317-350`，节选）

```go
func (r recoverableLLM) applyRunScopedCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool, est llm.Estimator) ([]llm.Message, int, bool) {
	key := historyKey(ctx)
	prefillTokens := r.runGuard.prefill(key, llm.EstimateMessagesWith(est, messages))
	tail := provisionalTailLen(ctx, len(messages))
	if base, baseLen, ok := r.runGuard.get(key); ok && baseLen <= len(messages) {
		newMsgs := messages[baseLen:]
		if tail > len(newMsgs) {
			// A cached base reaching into the provisional messages is not the
			// history the orchestration loop described; compact the slice whole,
			// as before.
			tail = 0
		}
		combined := make([]llm.Message, 0, len(base)+len(newMsgs))
		combined = append(combined, base...)
		combined = append(combined, newMsgs...)
		messages = combined
	}
	head, provisional := messages[:len(messages)-tail], messages[len(messages)-tail:]
	...
	head, compacted := compactIfNeeded(compactCtx, head, snapshotRaw, r.compactDeps, prefillTokens, tools)
	...
	if !compacted {
		return messages, tail, false
	}
	r.runGuard.set(key, head, llm.EstimateMessagesWith(est, head))
	return joinProvisionalTail(head, provisional), tail, true
}
```

`forceCompact` 的开头（同文件 `375-378`）：

```go
func (r recoverableLLM) forceCompact(ctx context.Context, messages []llm.Message, tail int, tools []*llm.Tool, est llm.Estimator) ([]llm.Message, bool) {
	head, provisional := messages[:len(messages)-tail], messages[len(messages)-tail:]
```

`tail` 只有这两个消费者：`Execute` 把它透传给 `forceCompact`（同文件 `252-266`，供应商报 context 溢出时的补救压缩）。

### 撤回路径的前提（`pkg/run/orchestration_llm.go:338-349`，本计划**不改**它）

```go
			steerCount := len(session) - sessionBeforeSteers
			inFlightSteers.Rollback()
			inFlightSteers = nil
			session = session[:sessionBeforeSteers]
			if replaced, ok := adoptionSink.take(); ok && len(replaced) >= steerCount {
				// A mid-turn compaction finished inside the abandoned call. Its
				// checkpoint stops before the steers (see provisionalTail), so
				// the replacement ends with exactly the steer messages: keep the
				// checkpoint, drop them.
				session = replaced[:len(replaced)-steerCount]
			}
```

它的注释写明了它依赖的前提。本计划让该前提**不可能被违反**：被放弃的调用要么没压缩（`adoptionSink` 为空 → 走 `session = session[:sessionBeforeSteers]` 那条），
要么压缩时尾巴被完整尊重。

### 文件与 import

- `pkg/run/llm_middleware.go` 当前 import 组（`3-18`）：`context, encoding/json, errors, fmt, strings, sync, time` + `appcfg/llm/safety/tool`；
  **没有 `log/slog`**，本计划要加。
- `pkg/run/compaction.go` 已 import `context` 与 `strings`（`historyKey` 是 045 引入的同包函数，直接用即可）。

### 仓库约定

- 注释写完整英文句子、解释「为什么」，口吻照上面摘录。
- 日志用 `log/slog`（`slog.Warn("...", "key", value)`），先例见 `pkg/gateway/api_extra.go` 的 `slog.Error("publish subagent pending input", ...)`。
- 测试用标准 `testing` + `t.Fatalf`，夹具照 `pkg/run/compaction_test.go` 的 `steerCompactFixture`（389 行起）与
  `TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`（409 行起）；`roles()` 在 220 行。
- 不要 commit / push（见 Git workflow）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 压缩 / 撤回相关 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compaction\|Compact\|Overflow\|Retract\|Steer\|Recall\|Abandoned'` | `ok` |
| 新测试（修复前） | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored -v` | **FAIL**（见 Step 2） |
| race | `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` | `ok`，无 `DATA RACE` |
| 跳过次数 | `CGO_ENABLED=1 go test -v -tags fts5 -count=1 ./pkg/run 2>&1 \| grep -c "compaction skipped: provisional"` | `1` |
| package 回归 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run ./pkg/gateway ./pkg/tui ./pkg/turn` | 四个包 `ok`（`./pkg/tui` 的 `TestCharacterizationNetworkApproval` 真实访问 `https://example.com`，与本计划无关，网络不稳时偶发失败，单跑通过即可） |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` | 无输出 |
| gofmt | `gofmt -l pkg/run` | 无输出 |
| 代码导航 | 有 codegraph 工具（`codegraph_explore`）时优先用它；否则 `grep -rn` | — |

## Scope

**In scope**（只改这四个文件）：

- `pkg/run/compaction.go`（`provisionalTail` 结构 + `withProvisionalTail` + `provisionalTailLen`）
- `pkg/run/llm_middleware.go`（`applyRunScopedCompact`、`forceCompact`、doc 注释、`log/slog` import）
- `pkg/run/compaction_test.go`（新增两个测试与一个测试用包装）
- `plans/README.md`（状态行）

**Out of scope**（看起来相关，明确不碰）：

- `pkg/run/orchestration_llm.go` 的撤回分支：本计划从源头保证它的前提成立，不改它。
- `pkg/run/orchestration_llm.go` 的 `withProvisionalTail` 调用点：签名不变（`outer` 仍然由调用方给出），不需要动。
- `pkg/run/llm_middleware.go` 的 `compactRunGuard` / `historyKey`：归 045，本计划只**使用** `historyKey`。
- `compactIfNeeded` / `shouldCompactMessages` / `bodyAfterPrefixTokens`（`pkg/run/compaction.go`）：本计划不改判定本身。
- 任何「让编排层与 `recoverableLLM` 之间的包装不改消息」的重构：那是另一个方向，本计划选择「分不清就不压缩」。

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动留在工作区，由 owner 手动审阅提交。
- 如将来提交，commit message 建议：`fix(run): never compact a call whose provisional steers cannot be told apart`。

## Steps

### Step 1: 锚点与基线

```bash
git diff --stat a8140fc..HEAD -- pkg/run/compaction.go pkg/run/llm_middleware.go pkg/run/compaction_test.go   # 只应有 045 的改动
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compaction|Compact|Overflow|Retract|Steer|Recall|Abandoned'
grep -n "func historyKey(ctx context.Context) string" pkg/run/llm_middleware.go   # 恰好 1 行（045 已落地）
grep -n "provisionalTailLen(" pkg/run/*.go                                         # 2 行：定义 + llm_middleware.go 的调用
```

**Verify**: 045 已在工作区（最后两条满足）；压缩相关测试 `ok`。

### Step 2: 先写会失败的回归测试

在 `pkg/run/compaction_test.go` 末尾追加：

```go
// reshapingLLM stands in for a wrapper between the orchestration loop and
// recoverableLLM that changes the message slice, so the provisional tail the
// loop described no longer matches what recoverableLLM is handed.
type reshapingLLM struct{ inner llm.LLM }

func (w reshapingLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	extra := llm.UserMessage(llm.Text("reshaped"))
	return w.inner.Execute(ctx, append(append([]llm.Message(nil), messages...), extra), tools)
}

// A call whose provisional steers can no longer be told apart from settled
// history is not compacted at all: a checkpoint never folds in a steer the user
// can still take back, and the steer reaches the model verbatim.
func TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored(t *testing.T) {
	st, readTool, _, rt := steerCompactFixture(t, "also check the tests")
	var compacted [][]llm.Message
	inner := &steerCompactLLM{}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compacted = append(compacted, append([]llm.Message(nil), messages...))
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	ctx := WithTurnInputRuntime(tool.WithRunID(context.Background(), "steer-tail-reshaped"), rt)
	if _, err := wrapToolOrchestrationLLM(reshapingLLM{inner: recoverable}, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool}); err != nil {
		t.Fatal(err)
	}
	for i, input := range compacted {
		for _, msg := range input {
			if strings.Contains(msg.TextContent(), "also check the tests") {
				t.Fatalf("compaction %d folded the provisional steer into its checkpoint: %v", i, roles(input))
			}
		}
	}
	if len(inner.calls) != 2 {
		t.Fatalf("requests = %d want 2", len(inner.calls))
	}
	carried := false
	for _, msg := range inner.calls[1] {
		if msg.TextContent() == "also check the tests" {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the steer did not reach the model verbatim: %v", roles(inner.calls[1]))
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored -v`
→ **FAIL**，信息包含 `compaction 0 folded the provisional steer into its checkpoint`。把输出原样贴进报告。
（若它**通过**：说明当前代码的行为已与本计划的理解不同——按 STOP 报告。）

### Step 3: 再加一个护栏测试（另一个历史的尾巴不阻止本历史压缩）

同一个文件末尾追加：

```go
// A call that inherits a tail recorded for another history — a subagent
// dispatched during a steered call runs under the dispatching call's context —
// carries no provisional messages of its own, so it compacts as usual. Reading
// that as "cannot be told apart" would stop every child of a steered turn from
// compacting for the whole execution.
func TestCompactionOfAHistoryThatCarriesAnotherHistorysTail(t *testing.T) {
	inner := &steerCompactLLM{}
	var compacted [][]llm.Message
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, messages []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compacted = append(compacted, append([]llm.Message(nil), messages...))
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	history := []llm.Message{
		llm.UserMessage(llm.Text("start")),
		llm.UserMessage(llm.Text(strings.Repeat("large history\n", 300))),
	}
	parentCtx := llm.WithAgentSessionID(tool.WithRunID(context.Background(), "steered-run"), "conversation-parent")
	// The dispatching call's context: a provisional tail of one message, marked
	// for the parent's history.
	parentCall := withProvisionalTail(parentCtx, parentCtx, len(history)+1, 1)
	// The child's own history: the same run (that is what its calls inherit),
	// its own worker session, its own messages.
	childCtx := llm.WithAgentSessionID(parentCall, "worker-child")
	if _, err := recoverable.Execute(childCtx, history, nil); err != nil {
		t.Fatal(err)
	}
	if len(compacted) != 1 || len(compacted[0]) != len(history) {
		t.Fatalf("compactions=%d, first input=%v; want the child's own history compacted whole", len(compacted), roles(compacted[0]))
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestCompactionOfAHistoryThatCarriesAnotherHistorysTail -v`
→ **PASS**（修复前后都必须通过：修复前是因为长度对不上→尾巴算 0→整段压缩；修复后是因为归属不同→无尾巴→整段压缩）。
它是护栏：如果实现把「另一个历史的标记」当成「分不清」，它会失败。

### Step 4: 标记记住自己属于哪段历史（`pkg/run/compaction.go`）

**4a.** `provisionalTail` 增加归属字段：

```go
type provisionalTail struct {
	sent    int             // length of the message slice the call was handed
	count   int             // how many of its trailing messages are provisional
	history string          // the history this tail belongs to (see historyKey)
	outer   context.Context // the orchestration context, without the call's retraction abort
}
```

**4b.** `withProvisionalTail` 记录归属（`outer` 就是那段历史所属的编排上下文；调用点不用改）：

```go
	return context.WithValue(ctx, provisionalTailKey{}, provisionalTail{
		sent: sent, count: count, history: historyKey(outer), outer: outer,
	})
```

**4c.** `provisionalTailLen` 改成两返回值，并按归属三分支：

```go
// provisionalTailLen reports how many trailing messages of a sent slice are
// provisional, and whether they can be told apart at all. A call that carries
// no tail of its own has none: a context with no recorded tail (0, true) and a
// tail recorded for another history (0, true) both describe this call as
// carrying no provisional messages — a subagent dispatched during a steered
// call inherits that call's context and is exactly that case. A tail recorded
// for this very history whose length no longer matches the slice in hand was
// reshaped on the way from the orchestration loop (0, false): its provisional
// messages are somewhere in it, but no longer at a known place, so a checkpoint
// must not be taken from it.
func provisionalTailLen(ctx context.Context, sent int) (int, bool) {
	if ctx == nil {
		return 0, true
	}
	pt, ok := ctx.Value(provisionalTailKey{}).(provisionalTail)
	if !ok || pt.history != historyKey(ctx) {
		return 0, true
	}
	if pt.sent != sent {
		return 0, false
	}
	return pt.count, true
}
```

**Verify**:
```bash
grep -n "history string" pkg/run/compaction.go                     # 恰好 1 行（新字段）
grep -n "func provisionalTailLen(ctx context.Context, sent int) (int, bool)" pkg/run/compaction.go   # 恰好 1 行
CGO_ENABLED=1 go build ./...                                       # exit 0（此时 llm_middleware.go 的调用点会编译失败——见下一步，两处必须一起改）
```
（若 Step 4 与 Step 5 之间需要一次可编译的中间态，把 4c 与 5 一起提交到工作区即可；本仓库的验证在 Step 5 之后统一跑。）

### Step 5: 分不清就不压缩（`pkg/run/llm_middleware.go`）

**5a.** `applyRunScopedCompact`：把 `tail := provisionalTailLen(ctx, len(messages))` 起到拼接块结束的那一段替换为：

```go
	tail, separable := provisionalTailLen(ctx, len(messages))
	if base, baseLen, ok := r.runGuard.get(key); ok && baseLen <= len(messages) {
		newMsgs := messages[baseLen:]
		if tail > len(newMsgs) {
			// A cached base reaching into the provisional messages is not the
			// history the orchestration loop described.
			separable = false
		}
		combined := make([]llm.Message, 0, len(base)+len(newMsgs))
		combined = append(combined, base...)
		combined = append(combined, newMsgs...)
		messages = combined
	}
	if !separable {
		// A checkpoint is durable, and a steer riding on this call is still the
		// user's to take back: once its tail cannot be told apart from settled
		// history, compacting would fold it in. This call goes out uncompacted —
		// its provisional messages settle with it, and the next call compacts as
		// usual. A negative tail also tells the overflow path not to compact
		// (see forceCompact): the same call must not fold the steer in through
		// the reactive route either.
		slog.Warn("compaction skipped: provisional messages cannot be told apart",
			"run_id", strings.TrimSpace(tool.RunIDFromContext(ctx)), "messages", len(messages))
		return messages, -1, false
	}
```

注意两点（审阅会看）：返回的是**拼接后**的 `messages`（与旧代码「不压缩时返回拼接结果」一致）；`tail` 用 `-1` 表示「本次不得压缩」。

**5b.** 该函数上方的 doc 注释末句补一句：
`A negative tail means the provisional messages cannot be told apart, and the call must not be compacted at all.`

**5c.** `forceCompact`：在函数体第一行之前插入早退（`tail < 0` 时 `messages[:len(messages)-tail]` 会越界，必须挡在这里）：

```go
	if tail < 0 {
		return messages, false
	}
```

**5d.** import 组按字母序加入 `"log/slog"`（放在 `"fmt"` 与 `"strings"` 之间）。

**Verify**:
```bash
grep -n "if tail < 0 {" pkg/run/llm_middleware.go                       # 恰好 1 行，位于 forceCompact 内
grep -n "\"log/slog\"" pkg/run/llm_middleware.go                        # 恰好 1 行
grep -n "compaction skipped: provisional messages cannot be told apart" pkg/run/llm_middleware.go   # 恰好 1 行
CGO_ENABLED=1 go build ./...                                            # exit 0
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored|TestCompactionOfAHistoryThatCarriesAnotherHistorysTail' -v   # 两个都 PASS
```

### Step 6: 全量校验

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compaction|Compact|Overflow|Retract|Steer|Recall|Abandoned'
CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run
CGO_ENABLED=1 go test -v -tags fts5 -count=1 ./pkg/run 2>&1 | grep -c "compaction skipped: provisional"
CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...
gofmt -l pkg/run
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway ./pkg/tui ./pkg/turn
git status --short -- pkg/run
```

**Verify**: 全绿；race 无 `DATA RACE`；`grep -c` 恰好 `1`（只有新测试触发这一次跳过——大于 1 说明有既有路径的暂定尾巴对不上，
用 `grep -B5 "compaction skipped" ` 找出是哪个测试，按 STOP 报告，不要改那些测试）；vet/gofmt 无输出；
`git status` 只多出 `compaction.go`、`llm_middleware.go`、`compaction_test.go` 的改动（045 的改动不算）。

### Step 7: 索引

把 `plans/README.md` 中 046 的状态行改为 `DONE`（若走到 STOP 则改 `BLOCKED` 并写一句原因）。

**Verify**: `grep -n "046-unseparable-provisional-tail" plans/README.md` → 1 行，状态为 `DONE`。

## 行为变化矩阵（逐场景核对，供审阅）

| 场景 | 旧行为 | 新行为 | 是否刻意修订 |
|---|---|---|---|
| 尾巴被完整尊重（编排层直接交给 `recoverableLLM`，无中间包装） | 尾巴被排除在 checkpoint 外，撤回路径裁掉后保留 checkpoint | 逐字不变 | 否 |
| 尾巴分不清（本历史、长度不符：中间包装改了消息条数） | `tail = 0`，**整段压缩**，steer 被折入 checkpoint；撤回随后把 checkpoint 也裁掉（探针 B：`session = [assistant:done]`） | 本次不压缩；steer 原样发给模型；撤回走「没有 adoption」分支（`session = session[:sessionBeforeSteers]`） | **是**（本计划的目标） |
| 分不清的那次调用真的超出窗口 | 先整段压缩（折入 steer）再重发；供应商仍报溢出则失败 | 不压缩，溢出错误直接返回；steer 回滚回队列由 turn 边界重发 | **是**（刻意的 fail-closed；今天的链路上该分支走不到） |
| 子代理/ fork 继承了父调用的尾巴标记（归属不同） | 长度不符 → `tail = 0` → 整段压缩 | `(0, true)` → 整段压缩 | 否（行为一致；但若实现漏掉归属判定，它会退化成「整天不压缩」，本计划用护栏测试挡住） |
| 上下文里没有尾巴标记 | `tail = 0` | `(0, true)`，同上 | 否 |
| 尾巴长度恰好相符（本历史） | 尊重尾巴 | 尊重尾巴 | 否 |

## Test plan

- **新增回归**：`TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored`——在编排层与 `recoverableLLM` 之间插一个会改消息条数的包装（`reshapingLLM`），
  断言没有任何一次压缩的输入含有暂定 steer，且 steer 原样到达模型。修复前失败、修复后通过。范本：`TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`。
- **新增护栏**：`TestCompactionOfAHistoryThatCarriesAnotherHistorysTail`——继承**别的历史**的尾巴标记时照常压缩（修复前后都通过）。
- **既有测试必须原样通过**，尤其：`TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`（`compaction_test.go:409`）、
  `TestRetractionDuringCompactionKeepsTheCheckpoint`（`compaction_test.go:446`）、
  `TestOverflowCompactionKeepsProvisionalSteersOutOfTheCheckpoint`（`orchestration_llm_test.go:160`，它用 `withProvisionalTail(ctx, ctx, len(history), 1)` 且 ctx 无会话 id——
  归属两侧都算作 `"<run>\x00"` 因而相等，仍会被尊重）、`TestOrchestrationAdoptsCompactedSession`（`compaction_test.go:150`）、
  `TestDiscardRetractsTheInFlightSteer`（`orchestration_llm_test.go:5786`）、以及 045 新增的两个 guard 测试。
- `grep -c "compaction skipped: provisional"` 必须恰好 `1`——它是「生产路径不会误跳过压缩」的机器校验。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored|TestCompactionOfAHistoryThatCarriesAnotherHistorysTail'` → `ok`（报告里有第一个测试修复前的失败输出）
- [ ] `grep -n "func provisionalTailLen(ctx context.Context, sent int) (int, bool)" pkg/run/compaction.go` → 恰好 1 行
- [ ] `grep -n "if tail < 0 {" pkg/run/llm_middleware.go` → 恰好 1 行，位于 `forceCompact` 内
- [ ] `CGO_ENABLED=1 go test -v -tags fts5 -count=1 ./pkg/run 2>&1 | grep -c "compaction skipped: provisional"` → `1`
- [ ] `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` → `ok`，无 `DATA RACE`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` 与 `gofmt -l pkg/run` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway ./pkg/tui ./pkg/turn` → 三个包 `ok`
- [ ] `git status --short -- pkg/run` 只多出 `compaction.go`、`llm_middleware.go`、`compaction_test.go` 的改动
- [ ] `plans/README.md` 中 046 为 `DONE`

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 发现 045 不在工作区（`historyKey` 不存在），或 "Current state" 的摘录与实码对不上。
- Step 2 的新测试在修复前就**通过**（当前代码的行为已与本计划的理解不同）。
- Step 3 的护栏测试在修复**后**失败（说明实现把「别的历史的尾巴」当成了「分不清」——这正是本计划要防的退化，不要把它写成预期结果）。
- Step 6 的 `grep -c "compaction skipped: provisional"` 大于 `1`，或任何既有压缩 / 撤回 / 溢出测试失败：说明有既有路径依赖「整段压缩」的旧行为。
  **不要改测试迁就**，报告是哪条路径，并把它的意图说清楚。
- `-race` 报 `DATA RACE` 且涉及本计划改动的代码。
- 修复看起来需要改 `pkg/run/orchestration_llm.go`（撤回分支或尾巴安装点）——那说明前提仍不成立，停下来回报而不是打补丁。

## Maintenance notes

- **不变式**：checkpoint 永不折入用户还能撤回的消息。两条守卫一起成立才有效：编排层直接交给 `recoverableLLM` 的切片必须与它描述的尾巴一致；
  一旦分不清，这次调用**不压缩**（而不是整段压缩）。
- **今后加包装的代价**：在编排层与 `recoverableLLM` 之间新增任何会改变消息条数（或改变消息内容顺序）的包装，都会让带 steer 的调用每次都跳过压缩，
  日志里会出现 `compaction skipped: provisional messages cannot be told apart`；此时应把该包装挪到 `recoverableLLM` 之内/之外，或同步调整 `withProvisionalTail`
  记录的长度，而不是放宽这条守卫。
- **跳过的代价**（刻意接受）：被跳过的调用若本该压缩，它会带着更长的上下文发出；若因此溢出，`forceCompact` 也不会压缩（`tail < 0`），调用以溢出错误失败，
  steer 回滚由 turn 边界重发。今天的链路上这条分支走不到（Step 6 的计数校验为 `1`）。
- **审阅重点**：`applyRunScopedCompact` 在 `!separable` 时返回的是拼接后的 `messages`（与旧行为一致）；`tail` 的两处使用（`forceCompact` 早退、`Execute` 的透传）
  都按「负数 = 本次不得压缩」理解；`provisionalTailLen` 的归属比较用的是 045 的 `historyKey`，两侧算法必须一致。
- **后续可选项**（不在本计划内）：把「尾巴」改成随调用携带的结构体（而不是 context 值）可以从类型上消灭这类错配；那是更大的改动，需 owner 重新裁决。
