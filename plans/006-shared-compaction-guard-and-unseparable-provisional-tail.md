# Plan 006: 查清 Runner 共享的压缩 guard 会不会被同 run ID 的另一段历史覆盖；不可达则让「分不清暂定尾巴」的调用不压缩

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 这是一份**排查计划**：Step 1–4 只读（外加一个临时探针测试），Step 4 是决策闸门。只有闸门判定「不可达」时才执行 Step 5–7 的小修；
> 判定「可达」时停下来把报告交给 owner，由 owner 拍板怎么修（本仓规矩：有风险、需拍板的点必须停下来问）。
>
> **Drift check (run first)**: 本计划写在 HEAD `71c0449` **加上工作区里尚未提交的 plan 003 实现**之上；写计划时工作区里还有另一个执行者在落地
> plan 004（改 `pkg/state/plan.go`、`pkg/run/config.go`、`pkg/tool/*` 等，与本计划文件不重叠）。所以漂移检查用锚点：
>
> ```bash
> grep -n "func provisionalTailLen(ctx context.Context, sent int) int {" pkg/run/compaction.go            # 恰好 1 行
> grep -n "// history the orchestration loop described; compact the slice whole," pkg/run/llm_middleware.go # 恰好 1 行
> grep -n "func (r recoverableLLM) forceCompact(ctx context.Context, messages \[\]llm.Message, tail int" pkg/run/llm_middleware.go  # 恰好 1 行
> grep -n "if replaced, ok := adoptionSink.take(); ok && len(replaced) >= steerCount {" pkg/run/orchestration_llm.go  # 恰好 1 行
> grep -n "r.forkLLM = r.wrapExecutionLLM(llmClient, false)" pkg/run/runner.go                            # 恰好 1 行
> ```
>
> 任何一条不符，先把「Current state」摘录与实码逐段对照；对不上按 STOP 处理。

## Status

- **Priority**: P3
- **Effort**: S（排查约半天；若走到 Step 5，代码改动 < 40 行）
- **Risk**: LOW（排查只读；Step 5 只改一个今天走不到的异常分支）
- **Depends on**: `plans/003-recall-in-flight-steer.md`（必须已在工作区或已提交）
- **Category**: bug（investigation）
- **Planned at**: commit `71c0449` + 未提交的 003 工作区，2026-10-09

## Why this matters

plan 003 让编排循环把「随本次模型调用发出、但用户还能撤回」的 steer 标成**暂定尾巴**（provisional tail），`recoverableLLM` 中途压缩时只把
尾巴之前的历史压进 checkpoint，尾巴原样接在后面。撤回路径据此假设「被放弃的那次调用若做了压缩，它的 replacement 一定以那几条 steer 结尾」，
直接裁掉最后 `steerCount` 条。但 `applyRunScopedCompact` 有一个防御分支：当 run 级缓存的 checkpoint 基线「伸进了暂定消息」时，它把尾巴
置 0、**整段压缩**——steer 被折进持久的 checkpoint，撤回路径随后又把 replacement 的末尾当成 steer 裁掉。审计时用探针在隔离环境里证实了
两件事（输出见 Current state）：

1. **探针 A**：`compactRunGuard` 每个 `recoverableLLM` 只有一个槽、按 run ID 存基线，而一个 Runner 的主链和 fork 链共用同一个
   `recoverableLLM`。另一段历史以**同一个 run ID** 压缩后，父对话下一次请求变成「别人的 checkpoint + 自己历史的后半段」。这若在生产可达，
   是比撤回更大的、003 之前就存在的压缩串台 bug。
2. **探针 B**：只要尾巴没被遵守（上面的串台，或将来有人在编排层与 `recoverableLLM` 之间加一个会改消息条数的包装），撤回后会话会被裁空，
   checkpoint 从结果里消失（`session = [assistant:done]`）。

本计划先查清 (1) 在生产里是否可达；不可达则用一个小改动把 (2) 从根上堵住：**分不清暂定尾巴的调用这次不压缩**，下一次调用照常压缩。

## Current state

### 相关文件

- `pkg/run/llm_middleware.go` — `recoverableLLM`：`compactRunGuard`（119–171 行）、`Execute`（220 行起，溢出时调 `forceCompact`，约 252 行）、
  `applyRunScopedCompact`（304 行起）、`forceCompact`（362 行起）。import 里**没有** `"log/slog"`（有 `"fmt"`、`"strings"`）。
- `pkg/run/compaction.go` — `provisionalTail` / `withProvisionalTail` / `provisionalTailLen`（约 258–297 行）、`compactionContext`、`joinProvisionalTail`。
- `pkg/run/orchestration_llm.go` — 编排循环；撤回分支（约 234–270 行）。
- `pkg/run/runner.go` — 装配 LLM 链：`WrapRecoverableLLM`（约 1051 行，每次 Load 新建一个，内含一个 `newCompactRunGuard()`），
  `r.forkLLM = r.wrapExecutionLLM(llmClient, false)` 与 `mainLLM := r.wrapExecutionLLM(llmClient, true)`（约 1142–1143 行，**同一个** `llmClient`）。
- `pkg/run/subagent.go` — fork 子任务的 run ID（约 1309–1326 行、1103–1105 行），异步派发继承父 run ID（约 2684–2686 行）。
- `pkg/run/compaction_test.go` — 压缩相关测试与夹具：`steerCompactLLM`（363 行起）、`steerCompactFixture`（381 行起）、
  范本 `TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`（401 行起）、`roles`（220 行）。

### `compactRunGuard`：单槽、按 run ID（`pkg/run/llm_middleware.go:119-171`，节选）

```go
type compactRunGuard struct {
	mu            sync.Mutex
	runID         string
	compactedBase []llm.Message
	baseLen       int
	prefillTokens int
	prefillSet    bool
}

func (g *compactRunGuard) get(runID string) (base []llm.Message, baseLen int, ok bool) {
	...
	if g.runID != runID || len(g.compactedBase) == 0 {
		return nil, 0, false
	}
	return g.compactedBase, g.baseLen, true
}

func (g *compactRunGuard) prefill(runID string, currentTokens int) int {
	...
	if g.runID != runID {
		g.runID = runID
		g.compactedBase = nil
		g.baseLen = 0
		g.prefillSet = false
	}
	...
}
```

### `applyRunScopedCompact` 的防御分支（`pkg/run/llm_middleware.go:304-336`，节选）

```go
func (r recoverableLLM) applyRunScopedCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool) ([]llm.Message, int, bool) {
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	prefillTokens := r.runGuard.prefill(runID, llm.EstimateMessages(messages))
	tail := provisionalTailLen(ctx, len(messages))
	if base, baseLen, ok := r.runGuard.get(runID); ok && baseLen <= len(messages) {
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
	... // snapshot
	compactCtx, release := compactionContext(ctx)
	head, compacted := compactIfNeeded(compactCtx, head, snapshotRaw, r.compactDeps, prefillTokens, tools)
	release()
	if !compacted {
		return messages, tail, false
	}
	r.runGuard.set(runID, head)
	return joinProvisionalTail(head, provisional), tail, true
}
```

`forceCompact`（`pkg/run/llm_middleware.go:362` 起）开头是：

```go
func (r recoverableLLM) forceCompact(ctx context.Context, messages []llm.Message, tail int, tools []*llm.Tool) ([]llm.Message, bool) {
	head, provisional := messages[:len(messages)-tail], messages[len(messages)-tail:]
```

### `provisionalTailLen`（`pkg/run/compaction.go:285-297`）

```go
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

它是全仓唯一调用点：`pkg/run/llm_middleware.go` 的 `applyRunScopedCompact`（`grep -n "provisionalTailLen(" pkg/run/*.go` 只有定义与这一处）。

### 撤回路径的裁剪（`pkg/run/orchestration_llm.go:242-253`）

```go
				accumulateUsage(&totalUsage, usageCopy(res))
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

### 链路事实（审计时已核实，供 Step 3 起步）

- 生产链从外到内：编排层（`wrapToolOrchestrationLLM*`）→ `wrapForkCaptureLLM`（只记录请求，不改消息，`pkg/run/fork.go:75-86`）→
  `wrapGuardrailsLLM`（只读校验，不改消息，`pkg/run/llm_middleware.go:422` 起）→ `recoverableLLM` → plan-mode / skill-offer / LSP 提醒包装（在
  `recoverableLLM` **里面**，`pkg/run/runner.go` 约 987–998 行）→ …。所以**今天**编排层交给 `recoverableLLM` 的切片就是它描述的那个，暂定尾巴会被遵守。
- fork 子任务只在 `own.RunRT != nil && parent != ""` 时得到自己的 run ID（`pkg/run/subagent.go` 约 1312–1323 行，`childRunID = cr.ID`）；
  否则 `entry.RunID` 为空，`pkg/run/subagent.go:1103-1105` 不覆盖 run ID，子任务沿用 ctx 里父对话的 run ID，并经 `own.ForkLLM()`（与主链共用同一个
  `recoverableLLM`）发请求。
- `pkg/run/runner.go` 里 `policyRunIDForTool` 的注释写明：「One Runner executes many runs at once — subagent_fanout dispatches its children onto the
  parent's Runner and agent」——父子调用会在同一个 `recoverableLLM` 上交错。
- 生产装配 `pkg/process/open.go`（约 292、341 行）总是设置 `RunRT`；`pkg/run/factory.go`（约 14、43 行）用 `f.ownerRunRT()`；`pkg/run/runner.go` 的 hook
  运行时用一个**没有 Owner** 的 `Factory{...}` 建 `NewIsolatedRunner`（约 1090–1110 行）。这些是 Step 3 要逐一确认的线索，不是结论。
- `prefillTokens` 只在 `deps.LimitScope == "body_after_prefix"`（用户配置 `assembly.model_auto_compact_token_limit_scope`，默认 `total`）时参与压缩判定
  （`pkg/run/compaction.go` 约 152 行）。

### 审计时的探针输出（隔离运行，未改工作区）

```
--- FAIL: TestProbeSharedRunGuardMixesHistories
    parent request = [user:checkpoint-1 assistant:parent answer]: another history's checkpoint replaced the parent's
--- FAIL: TestProbeRetractionTrimWhenTailIsNotHonored
    session = [assistant:done]: the checkpoint the call adopted was trimmed away
```

### 仓库约定

- 注释：完整英文句子，解释「为什么」，口吻同上方摘录。不要写中文注释。
- `pkg/run` 测试用标准 `testing` + `t.Fatalf`，夹具与断言风格照 `pkg/run/compaction_test.go` 的 `TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`。
- 日志用 `log/slog`（如 `slog.Warn("...", "key", value)`），见 `pkg/gateway/api_extra.go` 的 `slog.Error("publish subagent pending input", ...)`。
- 不要 commit / push（见 Git workflow）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 探针 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestProbe -v` | Step 2 预期 **FAIL**（见下） |
| 压缩 / 撤回相关 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compact\|Compaction\|Overflow\|Retract\|Steer\|Recall\|Abandoned'` | `ok` |
| run 包（race） | `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` | `ok`，无 `DATA RACE` |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` | 无输出 |
| gofmt | `gofmt -l pkg/run` | 无输出 |
| 代码导航 | 有 codegraph 工具（`codegraph_explore`）时优先用它查调用链；否则 `grep -rn` | — |

## Scope

**In scope**：

- Step 2 临时文件 `pkg/run/compact_guard_probe_test.go`（创建，Step 4 前删除）
- 仅当 Step 4 判定「不可达」时：`pkg/run/compaction.go`（`provisionalTailLen`）、`pkg/run/llm_middleware.go`（`applyRunScopedCompact`、`forceCompact`、import）、
  `pkg/run/compaction_test.go`（新增一个测试与一个测试用包装）
- `plans/README.md`（状态行）
- 排查报告：写在完成回报里（不需要新文件）

**Out of scope**（不要碰）：

- `compactRunGuard` 的结构（单槽 / 键）、`Runner` 的 LLM 链装配、fork 子任务的 run ID 分配——这些是「可达」时 owner 要拍板的修法，本计划只报告。
- `pkg/run/orchestration_llm.go` 的撤回分支：Step 5 从源头保证它的前提成立，不改它。
- `prefillTokens` 在交错调用下被重置的问题（见 Step 3 的 Q3）：只报告，不修。
- plan 004 正在改的文件（`pkg/state/plan.go`、`pkg/run/config.go`、`pkg/tool/*`、`pkg/process/session_context.go`）。

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动留在工作区，由 owner 手动审阅提交。
- 如走到 Step 5，commit message 建议：`fix(run): never compact a call whose provisional steers cannot be told apart`。

## Steps

### Step 1: 锚点与基线

运行 Drift check 的五条 grep；再跑：

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compact|Compaction|Overflow|Retract|Steer|Recall|Abandoned'
```

**Verify**: grep 各恰好 1 行；测试 `ok`。（若测试失败且失败的是 plan-mode 相关用例——`TestPlanMode*`、`TestImplementationReminder*`——多半是 plan 004 的执行者改到一半，
等几分钟重跑；仍失败则按 STOP 报告。）

### Step 2: 跑探针（临时）

新建 `pkg/run/compact_guard_probe_test.go`，内容**原样**如下：

```go
package run

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// Probe A (plans/006): one recoverableLLM serves a Runner's main and fork
// chains, and its compactRunGuard keeps one checkpoint per run ID. A caller
// with another history under the same run ID replaces the parent's base.
func TestProbeSharedRunGuardMixesHistories(t *testing.T) {
	inner := &steerCompactLLM{}
	compactions := 0
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, _ []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compactions++
			return []llm.Message{llm.UserMessage(llm.Text(fmt.Sprintf("checkpoint-%d", compactions)))}, true, nil
		},
	})
	ctx := tool.WithRunID(context.Background(), "shared-run")
	child := []llm.Message{
		llm.UserMessage(llm.Text("child task")),
		llm.UserMessage(llm.Text(strings.Repeat("large child history\n", 300))),
	}
	if _, err := recoverable.Execute(ctx, child, nil); err != nil {
		t.Fatal(err)
	}
	parent := []llm.Message{
		llm.UserMessage(llm.Text("parent start")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("parent answer")}),
	}
	if _, err := recoverable.Execute(ctx, parent, nil); err != nil {
		t.Fatal(err)
	}
	last := inner.calls[len(inner.calls)-1]
	if len(last) == 0 || last[0].TextContent() != "parent start" {
		t.Fatalf("parent request = %v: another history's checkpoint replaced the parent's", roles(last))
	}
}

// extraMessageLLM stands in for a wrapper between the orchestration loop and
// recoverableLLM that changes the message count.
type extraMessageLLM struct{ inner llm.LLM }

func (w extraMessageLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	extra := llm.UserMessage(llm.Text("extra"))
	return w.inner.Execute(ctx, append(append([]llm.Message(nil), messages...), extra), tools)
}

// Probe B (plans/006): when the tail is not honored the checkpoint folds the
// steer in, and the retraction path trims a replacement that does not end with
// the steers.
func TestProbeRetractionTrimWhenTailIsNotHonored(t *testing.T) {
	st, readTool, q, rt := steerCompactFixture(t, "never mind")
	inner := &steerCompactLLM{}
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, _ []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			if _, ok := q.Recall(); !ok {
				t.Error("recall refused a steer that is still in the queue")
			}
			return []llm.Message{llm.UserMessage(llm.Text("checkpoint"))}, true, nil
		},
	})
	ctx := WithTurnInputRuntime(tool.WithRunID(context.Background(), "steer-tail-mismatch"), rt)
	res, err := wrapToolOrchestrationLLM(extraMessageLLM{inner: recoverable}, st).Execute(ctx,
		[]llm.Message{llm.UserMessage(llm.Text("start"))}, []*llm.Tool{readTool})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Session) == 0 || res.Session[0].TextContent() != "checkpoint" {
		t.Fatalf("session = %v: the checkpoint the call adopted was trimmed away", roles(res.Session))
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestProbe -v` → 两个测试都 **FAIL**，信息分别包含
`parent request = [user:checkpoint-1 assistant:parent answer]` 与 `session = [assistant:done]`。把输出原样贴进报告。
（若任一探针**通过**了：说明当前代码已与本计划的理解不同，按 STOP 报告。）

### Step 3: 可达性排查（只读）

回答下面四个问题，每条结论都要带 `file:line` 证据。用 codegraph（若可用）或 `grep -rn` 追调用链。

- **Q1（关键）**：生产里有没有一条路径，会让**带着某个编排循环的 run ID、但消息历史不同**的调用，经过**同一个** `recoverableLLM` 实例？
  逐一核对：
  1. fork 子任务在 `own.RunRT == nil` 时沿用父 run ID（`pkg/run/subagent.go` 约 1312 行的条件）。列出所有构造 `run.Runner` / `run.Deps` 的地方
     （`grep -rn "&run.Runner{\|&Runner{\|RunRT:" --include='*.go' pkg cmd | grep -v _test`），确认哪些装配的 `RunRT` 可能为 nil，以及这些 Runner 能否派发 fork 子任务
     （重点：`pkg/run/runner.go` hook 运行时里 `Factory{...}.NewIsolatedRunner(label)` 建的 prompt runner；`Factory.ownerRunRT()` 在无 Owner 时返回什么）。
  2. 异步派发（`pkg/run/subagent.go` 约 2684–2686 行把父 run ID 放进子任务的 base ctx）之后，子任务真正发请求时 ctx 里是谁的 run ID？
  3. 计划评审（`pkg/turn/approval.go` 约 1519–1521 行用被评审 run 的 ID）的评审调用走的是哪个 Runner 的哪条 LLM 链？
  4. 其它可能带父 run ID 调到同一 `llmClient` 的：goal check、/btw 侧问、标题生成、memory 整理（`pkg/run/memory_llm.go`，看它是否用独立 client）。
- **Q2**：除 Q1 外，是否有任何包装在编排层与 `recoverableLLM` 之间改变消息条数（会让 `provisionalTailLen` 长度校验失败）？核对
  `pkg/run/runner.go` 的 `wrapExecutionLLM` 与 `WrapRecoverableLLM` 之后、`wrapToolOrchestrationLLM*` 之前的所有包装。审计时的结论是「没有」，请复核。
- **Q3（只报告）**：父子 run ID 不同时，交错调用会让单槽 guard 在 `prefill` 里互相清空（基线与 `prefillTokens` 被重置）。确认这只影响
  `body_after_prefix` 口径下的压缩阈值，并估计影响（不修）。
- **Q4**：`provisionalTailLen` 现注释说的「a nested call that inherits the context with other messages」实际指哪条调用？若找不到任何这样的调用，写明「未找到」。

**Verify**: 报告里有一张表：`路径 | 是否经过同一 recoverableLLM | ctx 中的 run ID 来源 | 结论（可达/不可达/不确定）| 证据 file:line`，覆盖 Q1 的 4 项；Q2–Q4 各有一段结论。

### Step 4: 决策闸门

先删除探针文件：`rm pkg/run/compact_guard_probe_test.go`，并确认 `git status --short -- pkg/run/compact_guard_probe_test.go` 无输出。

- **Q1 任一项「可达」或「不确定」** → **STOP**。回报 Step 3 的表、Step 2 的探针输出，并附上可选修法供 owner 拍板（不要实施）：
  (a) 让每个 fork 子任务都有自己的 run ID（即使没有 `RunRT`，也生成一个本地 ID）；(b) 让 `compactRunGuard` 按 (run ID, worker 会话 ID) 多槽存基线；
  (c) 给 fork 链单独一个 `recoverableLLM` 实例。同时说明 Step 5 的小修与这三者独立、可以先做。
- **Q2「有」** → **STOP**，回报是哪层包装、它怎么改消息。
- **Q1 全部「不可达」且 Q2「没有」** → 继续 Step 5。

**Verify**: 探针文件已删除；报告里写明闸门结论。

### Step 5:（仅「不可达」）先写会失败的回归测试

在 `pkg/run/compaction_test.go` 文件末尾追加：

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
// history is not compacted: a checkpoint never folds in a steer the user can
// still take back, and the steer reaches the model verbatim.
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

（`compaction_test.go` 已 import `context`、`strings`、`testing`、`llm`、`tool`；编译若报缺 import，按报错补。）

**Verify**（修复前必须失败）: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored`
→ `FAIL`，信息包含 `compaction 0 folded the provisional steer into its checkpoint`。

### Step 6:（仅「不可达」）分不清暂定尾巴时不压缩

三处改动，目标代码已在审计时用 `go test -overlay` 实测（`pkg/run` 含 `-race` 全过）：

**6a. `pkg/run/compaction.go`**：把 `provisionalTailLen`（连同上方 3 行注释）整体替换为：

```go
// provisionalTailLen is how many trailing messages of a sent slice are
// provisional, and whether they can be told apart at all. A context without a
// provisional tail describes no slice in particular (0, true). One whose
// recorded length differs from the slice in hand was reshaped on the way from
// the orchestration loop (0, false): its provisional messages are somewhere in
// it, but no longer at a known place.
func provisionalTailLen(ctx context.Context, sent int) (int, bool) {
	if ctx == nil {
		return 0, true
	}
	pt, ok := ctx.Value(provisionalTailKey{}).(provisionalTail)
	if !ok {
		return 0, true
	}
	if pt.sent != sent {
		return 0, false
	}
	return pt.count, true
}
```

**6b. `pkg/run/llm_middleware.go` 的 `applyRunScopedCompact`**：把从 `tail := provisionalTailLen(ctx, len(messages))` 到 `messages = combined` 后的 `}` 这一段替换为：

```go
	tail, separable := provisionalTailLen(ctx, len(messages))
	if base, baseLen, ok := r.runGuard.get(runID); ok && baseLen <= len(messages) {
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
		// user's to take back; once it cannot be told apart from settled history,
		// compacting would fold it in. This call goes out uncompacted: its
		// provisional messages settle with it, and the next call compacts as
		// usual.
		slog.Warn("compaction skipped: provisional messages cannot be told apart", "run_id", runID, "messages", len(messages))
		return messages, -1, false
	}
```

并把函数上方 doc 注释的最后一句扩成（在 `the checkpoint and follow it verbatim.` 之后接一句）：
`A negative tail means they cannot be told apart, and the call must not be compacted at all.`

**6c. `pkg/run/llm_middleware.go` 的 `forceCompact`**：在函数体第一行（`head, provisional := ...` 之前）插入：

```go
	if tail < 0 {
		return messages, false
	}
```

并在该文件 import 的标准库组里按字母序加入 `"log/slog"`（放在 `"fmt"` 与 `"strings"` 之间）。

**Verify**:

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored   # ok
CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run                                                             # ok，无 DATA RACE
CGO_ENABLED=1 go test -v -tags fts5 -count=1 ./pkg/run 2>&1 | grep -c "compaction skipped: provisional"               # 1
```

最后一条必须恰好是 `1`（只有新测试触发）。大于 1 说明有既有路径的暂定尾巴对不上——按 STOP 报告是哪些测试（用 `grep -B5` 找 `=== RUN` 行）。

### Step 7: 全量校验与索引

```bash
CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...
gofmt -l pkg/run
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/gateway ./pkg/tui ./pkg/turn
grep -n "provisionalTailLen(" pkg/run/*.go
```

**Verify**: vet、gofmt 无输出；三个包 `ok`（`./pkg/tui` 的 `TestCharacterizationNetworkApproval` 真实访问 `https://example.com`，网络不稳时偶发
`curl: (35)` 失败，与本计划无关——单跑通过即可）；最后一条 grep 只有定义与 `llm_middleware.go` 一处调用。
然后把 `plans/README.md` 中 006 的状态改为 `DONE`（走到 Step 4 STOP 的情况改为 `BLOCKED`，并写一句原因）。

## Test plan

- **临时探针**（Step 2，Step 4 删除）：`TestProbeSharedRunGuardMixesHistories`（共享 guard 在同一 run ID 下拼错历史）、`TestProbeRetractionTrimWhenTailIsNotHonored`
  （尾巴不被遵守时撤回把 checkpoint 裁掉）。它们是证据，不留在仓库里。
- **新增回归**（Step 5，仅「不可达」时）：`TestCompactionSkipsACallWhoseProvisionalTailCannotBeHonored`——编排层与 `recoverableLLM` 之间插一个改消息条数的包装，
  断言没有任何一次压缩的输入含有暂定 steer，且 steer 原样到达模型。修复前失败、修复后通过。范本：`TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`。
- 既有测试必须原样通过，尤其 `TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`、`TestRetractionDuringCompactionKeepsTheCheckpoint`、
  `TestOverflowCompactionKeepsProvisionalSteersOutOfTheCheckpoint`、`TestOrchestrationAdoptsCompactedSession`、`TestRetracted*`、`TestDiscardRetractsTheInFlightSteer`。

## Done criteria

走到 Step 4 STOP 时：

- [ ] 报告含 Step 2 两个探针的 FAIL 输出、Step 3 的表与 Q2–Q4 结论、可选修法
- [ ] `git status --short -- pkg/run/compact_guard_probe_test.go` 无输出（探针已删）
- [ ] `plans/README.md` 中 006 为 `BLOCKED`（附一句原因）

走完 Step 7 时：

- [ ] 上面前两条同样满足（报告 + 探针已删）
- [ ] `grep -n "func provisionalTailLen(ctx context.Context, sent int) (int, bool)" pkg/run/compaction.go` 恰好 1 行
- [ ] `grep -n "if tail < 0 {" pkg/run/llm_middleware.go` 恰好 1 行，位于 `forceCompact` 内
- [ ] `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` → `ok`，无 `DATA RACE`
- [ ] `CGO_ENABLED=1 go test -v -tags fts5 -count=1 ./pkg/run 2>&1 | grep -c "compaction skipped: provisional"` 输出 `1`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` 与 `gofmt -l pkg/run` 无输出
- [ ] `git status --short -- pkg/run` 相对执行前只多出 `compaction.go`、`llm_middleware.go`、`compaction_test.go` 三个文件的改动（003 / 004 留下的改动不算）
- [ ] `plans/README.md` 中 006 为 `DONE`

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 锚点不符，且「Current state」摘录与实码对不上。
- Step 2 任一探针**通过**（当前代码已与本计划的理解不同）。
- Step 4 闸门：Q1 任一项可达或不确定，或 Q2 发现有包装改变消息条数（见 Step 4）。
- Step 5 的新测试在修复前就通过。
- Step 6 后 `grep -c "compaction skipped: provisional"` 大于 1，或任何既有压缩 / 撤回 / 溢出测试失败——说明有既有路径依赖「整段压缩」的旧行为，报告是哪条，不要改测试迁就。
- `-race` 报 `DATA RACE` 且涉及本计划改动的代码。
- 修复看起来需要改 Out of scope 的文件（尤其是 `compactRunGuard` 结构、Runner 装配、撤回分支）。

## Maintenance notes

- **不变式**：checkpoint 永不折入暂定消息（用户还能撤回的 steer，以及它们重开的回答）。Step 6 之后，凡是分不清暂定尾巴的调用都不压缩，撤回路径
  「replacement 以 steer 结尾」的假设由源头保证。今后在编排层与 `recoverableLLM` 之间新加会改消息切片的包装时，必须同步调整
  `withProvisionalTail` 记录的长度，否则每次带 steer 的调用都会跳过压缩（日志里会出现 `compaction skipped: provisional messages cannot be told apart`）。
- **跳过的代价**：被跳过的那次调用若本该压缩，它会带着更长的上下文发出；若因此溢出，`forceCompact` 也不会压缩（`tail < 0`），调用以溢出错误失败，
  steer 回滚由 turn 边界重发。在今天的链路上这条分支走不到（Step 6 的计数验证为 1）。
- **共享 guard 是更大的问题**：若将来出现与编排循环同 run ID、但历史不同的调用经过同一个 `recoverableLLM`（Step 3 的 Q1），即使有本计划的守卫，
  父请求也会被拼成「别人的 checkpoint + 自己历史的后半段」（探针 A）。届时按 Step 4 列的三种修法之一处理，并补一个以探针 A 为蓝本的回归测试。
- 审阅时重点看：`applyRunScopedCompact` 在 `!separable` 时返回的是**拼接后**的 `messages`（与改动前「不压缩时返回拼接结果」一致），以及 `forceCompact` 的 `tail < 0` 早退。
