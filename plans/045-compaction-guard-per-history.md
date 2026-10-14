# Plan 045: 压缩 guard 按「历史」多槽——键 = (run ID, 会话)，不再是单个 run ID

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 这是计划 006 决策闸门（判定「可达」）后 owner 拍板的三项修复之一。另两项：
> `plans/044-fork-continuation-owns-its-run.md`（可与本计划并行，改的是另一个文件）、
> `plans/046-unseparable-provisional-tail.md`（**依赖本计划**：它复用本计划引入的 `historyKey`，且与本计划改同一个函数）。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/run/llm_middleware.go pkg/run/compaction_test.go`
> 若这两个文件在 a8140fc 之后有改动，先把 "Current state" 的摘录与实码逐段对照；对不上按 STOP 处理。

## Status

- **Priority**: P2
- **Effort**: S（一个结构 + 三个方法 + 一处调用点；约 80 行含注释）
- **Risk**: MED（改的是压缩的核心判定输入；缓解：键为空时行为逐字不变，且键的第二维来自既有的会话身份）
- **Depends on**: none（044 可与本计划并行；046 依赖本计划）
- **Category**: bug
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

`recoverableLLM` 的 `compactRunGuard` 缓存「这段历史被压到哪个 checkpoint、压缩前的 prefix 基线是多少」，**但它的键只有一个 run ID**
（`pkg/run/llm_middleware.go:123-190`）。而 run ID 并不标识一段历史：同一个 Runner 的**一个** `recoverableLLM` 实例同时服务主链、fork 链和所有 typed 子代理
（`pkg/run/runner.go:1053` 装配，`1147-1148` 主/fork 各自 wrap 同一个实例；typed 子代理经 `run.Run` → supervisor 跑在 runner 的主 agent 上），
这些历史共用 run 身份的情况是设计内的：`pkg/run/subagent.go:1326-1341` 在 `own.RunRT == nil` 时让子任务沿用父 run ID。

后果两类，都有证据：

1. **别人的 checkpoint 被拼进这次请求**（006 探针 A 的实测）：
   `parent request = [user:checkpoint-1 assistant:parent answer]: another history's checkpoint replaced the parent's`。
   这是 003 之前就存在的缺陷，比撤回裁剪那条更大。
2. **交错调用互相顶替槽位**：`prefill` 只要 run ID 变了就清掉槽位与 `prefillTokens`（`llm_middleware.go:176-190`），`set` 直接覆盖 base（`163-174`）。
   一次 `subagent_fanout`（父 turn 与多个孩子同时跑）会让每段历史都拿不到自己的 checkpoint 复用，`body_after_prefix` 口径下的
   `prefillTokens` 基线（`pkg/run/compaction.go:161-167` 使用它）也会被别的历史反复重置，压缩阈值随之漂移。

修好之后：**一段历史的 checkpoint 只属于它自己**。键的第二维取会话身份，因为那正是压缩本身的作用域——`Runner.Load` 装配 `TryCompact` 时写得很清楚：
"Compaction owns model history, so it remains scoped to the worker session"（`pkg/run/runner.go:1037-1043`），而 transcript、partial capture、
`llm.AgentSessionIDFromContext` 也都是这个身份。缓存键与它所缓存的值从此同粒度。

## Current state

### `compactRunGuard`：单槽、按 run ID（`pkg/run/llm_middleware.go:119-193`，全文）

```go
// compactRunGuard caches the latest checkpoint and prefix token baseline for
// an agent run. The orchestration loop adopts each replacement through the
// compaction sink. Later calls still check the growing history and may compact
// again when the next threshold is reached.
type compactRunGuard struct {
	mu            sync.Mutex
	runID         string
	compactedBase []llm.Message
	baseLen       int
	prefillTokens int
	prefillSet    bool
}

func newCompactRunGuard() *compactRunGuard {
	return &compactRunGuard{}
}

// get returns the cached compacted base for the given run ID, along with the
// original message-count at compaction time. Returns ok=false when no
// compaction has been cached for this run (or when runID is empty).
func (g *compactRunGuard) get(runID string) (base []llm.Message, baseLen int, ok bool) {
	if g == nil || runID == "" {
		return nil, 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runID != runID || len(g.compactedBase) == 0 {
		return nil, 0, false
	}
	return g.compactedBase, g.baseLen, true
}

// set caches the compacted base for the given run ID, replacing any previous
// entry (which would be from a different run). prefillTokens is the compacted
// base's token count, computed by the caller with the session's own model's
// estimator so this prefix baseline shares the unit of the compaction decision
// and the observed overhead.
//
// baseLen is stored as the compacted length because the orchestration loop
// adopts the compacted history (see compactionAdoptionSink): the next call's
// message slice therefore starts with exactly this base, so combining
// base + messages[baseLen:] is an identity that appends only genuinely-new
// messages. Anchoring on the pre-compaction length instead would, on an
// unusually long turn, slice past newly produced messages and drop them.
func (g *compactRunGuard) set(runID string, compacted []llm.Message, prefillTokens int) {
	if g == nil || runID == "" || len(compacted) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runID = runID
	g.compactedBase = compacted
	g.baseLen = len(compacted)
	g.prefillTokens = max(0, prefillTokens)
	g.prefillSet = true
}

func (g *compactRunGuard) prefill(runID string, currentTokens int) int {
	if g == nil || runID == "" {
		return -1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runID != runID {
		g.runID = runID
		g.compactedBase = nil
		g.baseLen = 0
		g.prefillSet = false
	}
	if !g.prefillSet {
		g.prefillTokens = max(0, currentTokens)
		g.prefillSet = true
	}
	return g.prefillTokens
}
```

### 唯一的调用点（`pkg/run/llm_middleware.go:317-345`，节选）

```go
func (r recoverableLLM) applyRunScopedCompact(ctx context.Context, messages []llm.Message, tools []*llm.Tool, est llm.Estimator) ([]llm.Message, int, bool) {
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	prefillTokens := r.runGuard.prefill(runID, llm.EstimateMessagesWith(est, messages))
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
	...
	head, compacted := compactIfNeeded(compactCtx, head, snapshotRaw, r.compactDeps, prefillTokens, tools)
	...
	r.runGuard.set(runID, head, llm.EstimateMessagesWith(est, head))
	return joinProvisionalTail(head, provisional), tail, true
}
```

`prefillTokens` 是 `compactIfNeeded` 的 `bodyPrefixTokens` 参数（`pkg/run/compaction.go:115-133`），在 `LimitScope == "body_after_prefix"` 时参与判定；
负数表示「由切片自己推导」（`compaction.go:161-167`）。

### 同文件里现成的形状（照抄它，`pkg/run/llm_middleware.go:387-421`）

```go
// guardrailRunDedup tracks which run IDs have already had their input rails
// checked, so a multi-iteration turn checks once instead of per iteration. It
// is bounded, so a long-lived process cannot grow it without bound; the oldest
// entry is dropped first.
type guardrailRunDedup struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
}

const guardrailRunDedupCap = 512
```

### 身份事实（写代码时要用的）

- 主链的会话 id 由入口装配：`pkg/run/supervisor.go:222` / `266`（`llm.WithAgentSessionID(runCtx, sid)`）、gateway 同款；fork 子代理装自己的 worker 会话
  （`pkg/run/subagent.go:1117`、`1807-1809`）；typed 子代理由 `pkg/process/worker_cli.go:43` 的 `AgentContext(ctx, stateRoot, sid)` 装 worker 会话。
- `runGuard` 只在本文件内使用（`grep -rn "runGuard" pkg cmd` 只有定义与 `:319` 一处调用），改它的形状不会波及别处。
- `prefill` 唯一调用点也是 `:319`。
- 本文件 import 组：`context, encoding/json, errors, fmt, strings, sync, time` + `appcfg/llm/safety/tool`——**已含 `llm` 与 `tool`**，本计划不需要新增 import。

### 仓库约定

- 注释写完整英文句子、解释「为什么」，口吻照上面摘录。
- 测试用标准 `testing` + `t.Fatalf`，夹具照 `pkg/run/compaction_test.go` 的 `steerCompactLLM`（371 行起）与
  `TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`（409 行起）；`roles()` 辅助函数在 220 行。
- 不要 commit / push（见 Git workflow）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 压缩 / 撤回相关 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compaction\|Compact\|Overflow\|Retract\|Steer\|Recall\|Abandoned'` | `ok` |
| 新测试（修复前） | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestCompactionGuardKeepsAHistorysCheckpointToItself -v` | **FAIL**（见 Step 2） |
| race | `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` | `ok`，无 `DATA RACE` |
| package 回归 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run ./pkg/turn ./pkg/gateway` | 三个包 `ok`（`./pkg/tui` 的 `TestCharacterizationNetworkApproval` 真实访问 `https://example.com`，与本计划无关，网络不稳时偶发失败，单跑通过即可） |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` | 无输出 |
| gofmt | `gofmt -l pkg/run` | 无输出 |
| 代码导航 | 有 codegraph 工具（`codegraph_explore`）时优先用它；否则 `grep -rn` | — |

## Scope

**In scope**（只改这三个文件）：

- `pkg/run/llm_middleware.go`（`compactRunGuard` 结构 + `newCompactRunGuard` + `get`/`set`/`prefill` + `applyRunScopedCompact` 里的键计算 + 新增 `historyKey`）
- `pkg/run/compaction_test.go`（新增两个测试）
- `plans/README.md`（状态行）

**Out of scope**（看起来相关，明确不碰）：

- `pkg/run/subagent.go`：fork 续跑借父 run ID 的缺陷归 044（本计划不用它也能保证正确，两者互不替代）。
- `pkg/run/compaction.go` 的 `provisionalTail` / `provisionalTailLen` / `withProvisionalTail`：归 046。本计划保持 `tail := provisionalTailLen(ctx, len(messages))`
  这一行的现有调用形状不变（046 会把它改成两返回值）。
- `pkg/run/runner.go` 的 LLM 链装配（`1147-1148` 主/fork 共用一个实例）：**不要**通过拆实例来"顺便"修 —— 多槽 guard 才是根因修法。
- `pkg/run/orchestration_llm.go` 的撤回分支与 adoption sink。
- `guardrailRunDedup`：保持原样，本计划只借用它的形状。

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动留在工作区，由 owner 手动审阅提交。
- 如将来提交，commit message 建议：`fix(run): keep one compaction checkpoint per history, not per run`。

## Steps

### Step 1: 锚点与基线

```bash
git diff --stat a8140fc..HEAD -- pkg/run/llm_middleware.go pkg/run/compaction_test.go
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compaction|Compact|Overflow|Retract|Steer|Recall|Abandoned'
```

并确认：

```bash
grep -n "runGuard" pkg/run/llm_middleware.go                                    # 4 行：定义 3 处 + 调用 1 处
grep -rn "runGuard" --include='*.go' pkg cmd | grep -v llm_middleware.go        # 无输出
grep -n "^func (r recoverableLLM) applyRunScopedCompact" pkg/run/llm_middleware.go   # 恰好 1 行
```

**Verify**: `git diff --stat` 无输出；测试 `ok`；grep 结果如上。

### Step 2: 先写会失败的回归测试

在 `pkg/run/compaction_test.go` 末尾追加两个测试（第一个是本次修复的回归，第二个是「空 run ID 不缓存」的护栏）：

```go
// One run identity does not name one history: a fork dispatched while its
// dispatcher has no run row of its own runs under the dispatching run's id
// (pkg/run/subagent.go:1326-1341), and its history is its own worker session's.
// Each history keeps its own checkpoint, so neither request is spliced with the
// other's.
func TestCompactionGuardKeepsAHistorysCheckpointToItself(t *testing.T) {
	inner := &steerCompactLLM{}
	compactions := 0
	recoverable := WrapRecoverableLLM(inner, nil, &CompactChainDeps{
		ExplicitLimit: 500,
		TryCompact: func(_ context.Context, _ []llm.Message, _ []*llm.Tool, _ bool) ([]llm.Message, bool, error) {
			compactions++
			return []llm.Message{llm.UserMessage(llm.Text(fmt.Sprintf("checkpoint-%d", compactions)))}, true, nil
		},
	})
	child := []llm.Message{
		llm.UserMessage(llm.Text("child task")),
		llm.UserMessage(llm.Text(strings.Repeat("large child history\n", 300))),
	}
	childCtx := llm.WithAgentSessionID(tool.WithRunID(context.Background(), "shared-run"), "worker-child")
	if _, err := recoverable.Execute(childCtx, child, nil); err != nil {
		t.Fatal(err)
	}
	parent := []llm.Message{
		llm.UserMessage(llm.Text("parent start")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("parent answer")}),
	}
	parentCtx := llm.WithAgentSessionID(tool.WithRunID(context.Background(), "shared-run"), "conversation-parent")
	if _, err := recoverable.Execute(parentCtx, parent, nil); err != nil {
		t.Fatal(err)
	}
	last := inner.calls[len(inner.calls)-1]
	if len(last) != 2 || last[0].TextContent() != "parent start" {
		t.Fatalf("parent request = %v: another history's checkpoint replaced the parent's", roles(last))
	}
}

// A call that names no run caches nothing, exactly as before: the guard is
// keyed by the history a run identifies, and a call without one has nothing to
// cache under. Its slice is compacted whole, never spliced with a stale base.
func TestCompactionGuardCachesNothingWithoutARunID(t *testing.T) {
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
		llm.UserMessage(llm.Text("first")),
		llm.UserMessage(llm.Text("second")),
		llm.UserMessage(llm.Text(strings.Repeat("large history\n", 300))),
	}
	for i := 0; i < 2; i++ {
		if _, err := recoverable.Execute(context.Background(), history, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(compacted) != 2 {
		t.Fatalf("compactions=%d want 2", len(compacted))
	}
	for i, input := range compacted {
		if len(input) != len(history) || input[0].TextContent() != "first" {
			t.Fatalf("compaction %d saw %v, want the sent slice unchanged", i, roles(input))
		}
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'TestCompactionGuard' -v`
→ 第一个 **FAIL** 且信息是 `parent request = [user:checkpoint-1 assistant:parent answer]: another history's checkpoint replaced the parent's`；
第二个 **PASS**（它是护栏，修复前后都必须通过）。把第一个的失败输出原样贴进报告。
（若第一个**通过**：说明 guard 已经不是单槽了——按 STOP 报告。）

### Step 3: 键 = (run ID, 会话)

**3a.** 在 `pkg/run/llm_middleware.go` 里，紧跟 `newCompactRunGuard` 之前或之后，新增：

```go
// historyKey names the model history a call is carrying: the run it belongs to
// and the conversation whose history it is. A run identity alone does not name
// one history — one Runner serves the primary chain, the fork chains and every
// typed child with a single recoverableLLM (see Runner.Load), and a child
// dispatched without a run row of its own borrows the dispatching run's id — so
// the guard's key carries the same conversation identity compaction itself is
// scoped to. Empty when the call names no run: such a call caches nothing, as
// before.
func historyKey(ctx context.Context) string {
	runID := strings.TrimSpace(tool.RunIDFromContext(ctx))
	if runID == "" {
		return ""
	}
	return runID + "\x00" + strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
}
```

**3b.** 把 `compactRunGuard` 的结构与三个方法整体替换为（保留原有注释的正确部分，并按新语义改写）：

```go
// compactRunGuard caches the latest checkpoint and prefix token baseline for
// one model history. The orchestration loop adopts each replacement through the
// compaction sink. Later calls still check the growing history and may compact
// again when the next threshold is reached.
//
// One entry per history: a Runner's single recoverableLLM serves many histories
// at once, and a run identity is not one of them, so keying by run id alone let
// one history's checkpoint be spliced into another's request. The map is
// bounded; when it is full the oldest entry is dropped, which costs that
// history only its cached base until its next compaction.
type compactRunGuard struct {
	mu      sync.Mutex
	entries map[string]compactRunGuardEntry
	order   []string
}

type compactRunGuardEntry struct {
	compactedBase []llm.Message
	baseLen       int
	prefillTokens int
	prefillSet    bool
}

// compactRunGuardCap bounds the number of histories one Runner's guard
// remembers. A Runner serves its primary conversation plus the subagents
// running on it; the cap is far above that, and a dropped entry is harmless.
const compactRunGuardCap = 64

func newCompactRunGuard() *compactRunGuard {
	return &compactRunGuard{entries: make(map[string]compactRunGuardEntry)}
}

// get returns the cached compacted base for the given history, along with the
// original message-count at compaction time. Returns ok=false when no
// compaction has been cached for this history (or when the key is empty).
func (g *compactRunGuard) get(key string) (base []llm.Message, baseLen int, ok bool) {
	if g == nil || key == "" {
		return nil, 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.entries[key]
	if !ok || len(entry.compactedBase) == 0 {
		return nil, 0, false
	}
	return entry.compactedBase, entry.baseLen, true
}

// set caches the compacted base for the given history. prefillTokens is the
// compacted base's token count, computed by the caller with the session's own
// model's estimator so this prefix baseline shares the unit of the compaction
// decision and the observed overhead.
//
// baseLen is stored as the compacted length because the orchestration loop
// adopts the compacted history (see compactionAdoptionSink): the next call's
// message slice therefore starts with exactly this base, so combining
// base + messages[baseLen:] is an identity that appends only genuinely-new
// messages. Anchoring on the pre-compaction length instead would, on an
// unusually long turn, slice past newly produced messages and drop them.
func (g *compactRunGuard) set(key string, compacted []llm.Message, prefillTokens int) {
	if g == nil || key == "" || len(compacted) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.entries[key]; !exists {
		g.order = append(g.order, key)
		for len(g.order) > compactRunGuardCap {
			oldest := g.order[0]
			g.order = g.order[1:]
			delete(g.entries, oldest)
		}
	}
	g.entries[key] = compactRunGuardEntry{
		compactedBase: compacted,
		baseLen:       len(compacted),
		prefillTokens: max(0, prefillTokens),
		prefillSet:    true,
	}
}

// prefill returns the prefix token baseline recorded for this history's last
// checkpoint, recording the current size on the first call. A history that has
// never compacted answers -1, which tells the caller to derive the baseline
// from the slice it is holding.
func (g *compactRunGuard) prefill(key string, currentTokens int) int {
	if g == nil || key == "" {
		return -1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.entries[key]
	if !ok {
		entry = compactRunGuardEntry{}
		g.order = append(g.order, key)
		for len(g.order) > compactRunGuardCap {
			oldest := g.order[0]
			g.order = g.order[1:]
			delete(g.entries, oldest)
		}
	}
	if !entry.prefillSet {
		entry.prefillTokens = max(0, currentTokens)
		entry.prefillSet = true
		g.entries[key] = entry
	}
	return entry.prefillTokens
}
```

**3c.** 调用点改成用键（`applyRunScopedCompact` 头两行）：

```go
	key := historyKey(ctx)
	prefillTokens := r.runGuard.prefill(key, llm.EstimateMessagesWith(est, messages))
```

——并把该函数体内其余的 `r.runGuard.get(runID)` 改成 `r.runGuard.get(key)`、`r.runGuard.set(runID, head, ...)` 改成 `r.runGuard.set(key, head, ...)`。
`runID` 这个局部变量在 `applyRunScopedCompact` 里不再需要（若别处仍用，保留；用 `grep -n "runID" pkg/run/llm_middleware.go` 确认没有悬空引用）。

**3d.** 顺带核对 `applyRunScopedCompact` 上方的 doc 注释：它说 guard 按 run 缓存的话，请按新语义改一句（"the checkpoint this history's later calls start from"）。

**Verify**:
```bash
grep -n "func historyKey(ctx context.Context) string" pkg/run/llm_middleware.go      # 恰好 1 行
grep -n "historyKey(ctx)" pkg/run/llm_middleware.go                                  # 恰好 1 行（调用点）
grep -rn "\.prefill(\|\.get(\|\.set(" pkg/run/llm_middleware.go | grep -v "func "    # 只有三处，且都传 key
CGO_ENABLED=1 go build ./...                                                          # exit 0
```

### Step 4: 回归与门禁

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'TestCompactionGuard'            # 两个都 ok
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Compaction|Compact|Overflow|Retract|Steer|Recall|Abandoned'
CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run ./pkg/turn ./pkg/gateway
CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...
gofmt -l pkg/run
git status --short -- pkg/run
```

**Verify**: 全部 `ok`；race 无 `DATA RACE`；vet/gofmt 无输出；`git status` 只多出 `llm_middleware.go`、`compaction_test.go` 的改动（044/046 留下的改动不算）。

### Step 5: 索引

把 `plans/README.md` 中 045 的状态行改为 `DONE`（若走到 STOP 则改 `BLOCKED` 并写一句原因）。

**Verify**: `grep -n "045-compaction-guard-per-history" plans/README.md` → 1 行，状态为 `DONE`。

## 行为变化矩阵（逐场景核对，供审阅）

| 场景 | 旧行为 | 新行为 | 是否刻意修订 |
|---|---|---|---|
| 同 run、同会话的连续调用（主链常态） | 一个槽，base 正常拼接 | 同一个键，行为逐字不变 | 否 |
| 同 run、不同会话（`RunRT == nil` 的 fork 子任务、typed 子代理、审阅子代理） | 后到者覆盖槽位；先到者的 base 被拼进后到者的请求（探针 A） | 各占一个槽，互不拼接 | **是**（本计划的目标） |
| 不同 run、无会话（今天所有压缩单测的写法） | 换 run 即清槽，get 不命中 | 键不同，get 同样不命中；差别只在「不再清掉别人的槽」——而那个槽在新代码里属于另一个键 | 否（观察不到差异） |
| 不同 run、同会话 | 同 run 变化清槽 → 不拼接 | 键不同 → 不拼接 | 否 |
| run ID 为空 | 一律不缓存，`prefill` 返回 -1，切片整段判定 | 逐字不变（`historyKey` 返回 ""） | 否 |
| 交错调用下 `body_after_prefix` 的 `prefillTokens` | 每次 run 变化清零并重设，基线随交错漂移 | 每段历史各记自己的基线 | **是**（本计划的第二个目标） |
| 单 Runner 同时活跃的历史超过 64 | 只有一个槽可用 | 淘汰最旧的条目，该历史只是暂时失去 base，下次压缩重新建立 | **是**（有界是刻意的；无界 map 会随进程寿命增长） |

## Test plan

- **新增回归**：`TestCompactionGuardKeepsAHistorysCheckpointToItself`（`pkg/run/compaction_test.go`）——同一个 `WrapRecoverableLLM` 实例上，两段历史共用 run ID
  `shared-run`、会话分别为 `worker-child` / `conversation-parent`；先让子历史压缩并缓存，再断言父历史发给模型的请求**就是它自己那两条消息**（没有被拼上
  `checkpoint-1`）。修复前失败、修复后通过。
- **新增护栏**：`TestCompactionGuardCachesNothingWithoutARunID`——无 run ID 时两次调用各自整段压缩，第二次的压缩输入仍是原切片（修复前后都通过；若有人把空键也拿去缓存，它会失败）。
- 结构范本：`TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`（409 行起）与 `steerCompactLLM`（371 行起）。
- **既有测试必须原样通过**，尤其：`TestCompactionKeepsInFlightSteersOutOfTheCheckpoint`、`TestRetractionDuringCompactionKeepsTheCheckpoint`、
  `TestOrchestrationAdoptsCompactedSession`、`TestOverflowCompactionKeepsProvisionalSteersOutOfTheCheckpoint`（`orchestration_llm_test.go:160`）、
  `TestOverflowTeachesWindowSoNextRunCompactsProactively`（`orchestration_llm_test.go:193`，它故意用两个 run ID 共用一个实例）、
  `TestRecoverableLLMCompactsAndRetriesAfterContextOverflow`（`orchestration_llm_test.go:3731`）。
- **验证**：`CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` → `ok`，无 `DATA RACE`。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'TestCompactionGuard'` → `ok`（报告里有第一个测试修复前的失败输出）
- [ ] `grep -n "func historyKey(ctx context.Context) string" pkg/run/llm_middleware.go` → 恰好 1 行
- [ ] `grep -n "key := historyKey(ctx)" pkg/run/llm_middleware.go` → 恰好 1 行
- [ ] `grep -n "runID         string" pkg/run/llm_middleware.go` → 无输出（单槽字段已删除）
- [ ] `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` → `ok`，无 `DATA RACE`
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run ./pkg/turn ./pkg/gateway` → 三个包 `ok`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` 与 `gofmt -l pkg/run` 无输出
- [ ] `git status --short -- pkg/run` 只多出 `llm_middleware.go`、`compaction_test.go` 的改动
- [ ] `plans/README.md` 中 045 为 `DONE`

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 发现 `pkg/run/llm_middleware.go` 在 a8140fc 之后被改过，且 "Current state" 的摘录与实码对不上。
- Step 2 的第一个测试**通过**（说明 guard 已不是单槽）。
- 既有压缩 / 撤回 / 溢出用例失败：说明有代码依赖「换 run 即清槽」的旧行为。**不要改测试迁就**，把失败用例、它的意图与判断贴出来回报。
  特别留意 `TestOverflowTeachesWindowSoNextRunCompactsProactively`（两个 run ID 共用一个实例）——它在新区下应当仍绿，因为键不同导致的行为与「清槽」等价。
- 需要改 `pkg/run/subagent.go`、`pkg/run/runner.go` 或 `pkg/run/compaction.go` 才能让测试变绿——那说明根因不在 guard 的键上，停下来回报。
- 修复看起来需要为 `prefillTokens` 引入新的持久化或新的配置项。
- `-race` 报 `DATA RACE` 且涉及 `compactRunGuard`。

## Maintenance notes

- **不变式**：guard 的键必须与它缓存的值同粒度——一段历史（run + 会话）一个槽。今后任何新增「同一 Runner 上再多一段历史」的路径（新的子代理种类、
  新的 in-process 驱动）都不需要动这里，只要那段历史有自己的会话身份。
- **与 046 的接口**：046 会复用 `historyKey`（把暂定尾巴的归属也按历史判定），并把 `provisionalTailLen` 改成两返回值。两计划都改
  `applyRunScopedCompact`，**必须串行**（045 先）。
- **审阅重点**：`get`/`set`/`prefill` 对空键的处理是否与旧代码逐字等价（空 run ID 一律不缓存、`prefill` 返回 -1）；淘汰逻辑是否只在插入新键时发生（不得淘汰当前正在用的键）。
- **代价**：`set` 存的是 checkpoint 切片本身（与旧代码相同，没有额外拷贝）；64 个上限意味着极端并发下可能丢掉某段历史的 base——代价只是下一次压缩重新建立它。
