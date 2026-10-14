# Plan 044: fork 续跑在自己的 run 身份下发请求（`continueForkSubagent` 补 run ID）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 这是计划 006 决策闸门（判定「可达」）后 owner 拍板的三项修复之一（另两项：
> `plans/045-compaction-guard-per-history.md`、`plans/046-unseparable-provisional-tail.md`）。本计划**不依赖**那两份，可与 045 并行；
> 046 依赖 045，不要与 046 并行。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go`
> 若这两个文件在 a8140fc 之后有改动，先把 "Current state" 的摘录与实码逐段对照；对不上按 STOP 处理。

## Status

- **Priority**: P2
- **Effort**: S（改动约 4 行；测试改动约 25 行）
- **Risk**: LOW（只补一个上下文值，与同一文件里首跑路径的既有做法完全一致）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

一个 fork 子代理有两种执行方式：**首跑**由 `runForkSubagent` 在自己的进程内驱动 `RunFork`，它显式地把「子代理自己的 run ID + 自己的 worker 会话」装进上下文
（`pkg/run/subagent.go:1111-1121`）；**续跑**（模型调 `subagent_continue`、或用户给子代理发消息）由 `continueForkSubagent` 驱动，它**只装了 worker 会话、没装 run ID**
（`pkg/run/subagent.go:2458` 起）。于是续跑的所有模型请求与工具调用，在 `tool.RunIDFromContext` 看来都属于**派发它的那次编排 run**。

代价有三处，都是实码可证的：

1. **压缩串台（006 探针实证）**。`recoverableLLM` 的 `compactRunGuard` 按 run ID 单槽缓存 checkpoint（`pkg/run/llm_middleware.go:123-190`），而主链与 fork 链共用同一个
   `recoverableLLM` 实例（`pkg/run/runner.go:1053`）。续跑借用编排 run ID 后，它的请求被拼成「主链的 checkpoint + 续跑历史的后半段」。探针输出：
   `parent request = [user:checkpoint-1 assistant:parent answer]: another history's checkpoint replaced the parent's`（见 006 报告）。
2. **审批与等待行落到别人的 run 上**。工具的审批队列、durable 等待行、会话解析都从执行的 ctx 取 run ID（`pkg/run/runner.go:1347-1363` 的 `policyRunIDForTool`，
   被 `runner.go:377`（权限中间件 `PolicyRunID`）、`542-546`（按 run 反查会话）、`568-574`（`SetWaitingAction`）使用）。续跑的 gate 因此记在派发者的 run 名下。
3. **与文档契约不符**。`runner.go:1347-1358` 的注释明写：run 身份只能来自执行调用的 ctx，且「每个起 run 的入口都装它 —— supervisor.Run 的两条路径、gateway 自己构建的
   run 上下文，以及 **runForkSubagent**（它直接驱动子 agent）」。续跑同样直接驱动子 agent，却漏了这一步。

修好之后：续跑与首跑在「我是谁」这件事上完全一致；上面三处同时消失。压缩 guard 的键粒度问题（045 的主题）**不由本计划解决**，两者互不替代。

## Current state

### 续跑路径（问题的位置）——`pkg/run/subagent.go:2423-2475`（节选）

```go
func continueForkSubagent(ctx context.Context, fac Factory, record agent.HistoryEntry, input string) (string, error) {
	own := fac.Owner
	if own == nil || own.SubagentExecutor == nil {
		return "", fmt.Errorf("fork subagent requires runner")
	}
	llmClient := own.ForkLLM()
	...
	model := forkAgentModel(own, preparedSubagent{sessionID: record.SessionID})
	started := time.Now()
	agentCtx := ctx
	capture := NewPartialSessionCapture()
	agentCtx = WithPartialSessionCapture(agentCtx, capture)
	outcome, runErr := RunFork(agentCtx, RunParams{
		LLM: llmClient,
		...
		SessionID:      record.SessionID,
		ParentRunID:    record.ParentRunID,
		...
	})
	own.SubagentExecutor.PersistSubagentTurn(agentCtx, SubagentTurn{
		WorkerSessionID: record.WorkerSessionID,
		RunID:           record.RunID,
		...
	})
```

`agentCtx` 从 `ctx` 派生，全程没有任何一处写入子代理自己的 run ID。`ctx` 由调用者给出：`continueSubagentExecution` 在 `pkg/run/subagent.go:2326`
用 `subagentRecordContext(ctx, fac, record)` 组装宿主上下文，而该函数（`pkg/run/subagent.go:1787-1838`）只装这些：

```go
	if ws := strings.TrimSpace(record.WorkerSessionID); ws != "" {
		hostCtx = llm.WithAgentSessionID(hostCtx, ws)
	}
```

——worker 会话装了，run ID 没装。`ctx` 里剩下的 run ID 就是派发者（编排循环）的那个。

### 首跑路径（正确的参照）——`pkg/run/subagent.go:1108-1121`

```go
					// Give each fork agent a distinct intermediate_tool session key
					// so concurrent subagents write to separate files instead of
					// sharing the parent session file.
					if prep.workerSessionID != "" {
						agentCtx = llm.WithAgentSessionID(agentCtx, prep.workerSessionID)
					}
					if entryRunID := strings.TrimSpace(prep.entry.RunID); entryRunID != "" {
						agentCtx = tool.WithRunID(agentCtx, entryRunID)
					}
```

同一个文件里同一种执行，首跑装、续跑不装——这就是缺口。另外，supervisor 驱动的 typed 子代理两条路径也都装自己（`pkg/run/supervisor.go:216-229` 与
`255-273`，注释明写 "the identity is attached on both paths"），所以「续跑不装」是唯一的例外。

### run ID 的消费方（为什么这条 ctx 值重要）

- `pkg/run/llm_middleware.go:318`：`runID := strings.TrimSpace(tool.RunIDFromContext(ctx))` → `compactRunGuard` 的槽键（`139-190`）。
- `pkg/run/runner.go:1359-1363`：`policyRunIDForTool` → `runner.go:377`（权限中间件的 `PolicyRunID`）、`runner.go:1179`（工具依赖装配）、
  `runner.go:542-574`（按 run 反查会话、写 `SetWaitingAction` 等待行）。
- `pkg/run/llm_middleware.go:390-420`：`guardrailRunDedup` 也按 run ID 去重（输入防线的「一轮只跑一次」）。

### 记录里的两个身份——`pkg/run/subagent.go:1326-1341`

```go
	if own.RunRT != nil && parent != "" {
		...
		childRunID = cr.ID
	} else if parent != "" {
		parentRunID = parent
	}
```

`record.RunID` 是子代理自己的 run（首跑时由 `CreateSubagentRun` 建，见 `pkg/run/subagent.go:1327-1339`），`record.ParentRunID` 是它的父 run。
续跑用的是**同一条子代理 run**（`pkg/run/subagent.go:2359` 的 `runAcrossApprovals(execCtx, approve, record.RunID, "subagent continuation", ...)` 已经把它当作被监督的 run ID），
所以它就该是请求上下文里的 run ID。

> 注意：`own.RunRT == nil` 时 `record.RunID` 为空（那时数据库里根本没有这次子代理的 run 行）。**不要为这种情况编造一个 run ID**——本计划只在非空时装，
> 空的情况由 045 的多槽 guard 保证安全（届时键里的会话维度不同，两段历史不会互相顶替）。

### 仓库约定

- 注释写完整英文句子、解释「为什么」，口吻照 `pkg/run/subagent.go:1113-1120` 的既有注释。
- 测试用标准 `testing` + `t.Fatalf`，夹具与断言风格照 `pkg/run/subagent_test.go` 的 `TestForkContinuationReplaysItsOwnPrefixByteForByte`（3612 行起）。
- 不要 commit / push（见 Git workflow）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Fork\|SubagentContinue\|Continuation'` | `ok` |
| 新测试（修复前） | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestForkContinuationSpeaksUnderItsOwnRun -v` | **FAIL**（见 Step 2） |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run ./pkg/process ./pkg/turn` | 三个包 `ok` |
| race | `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` | `ok`，无 `DATA RACE` |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` | 无输出 |
| gofmt | `gofmt -l pkg/run` | 无输出 |
| 代码导航 | 有 codegraph 工具（`codegraph_explore`）时优先用它；否则 `grep -rn` | — |

## Scope

**In scope**（只改这三个文件）：

- `pkg/run/subagent.go`（`continueForkSubagent` 一处）
- `pkg/run/subagent_test.go`（`scriptedRequestsLLM` 加一个记录字段 + 一个新测试）
- `plans/README.md`（状态行）

**Out of scope**（看起来相关，明确不碰）：

- `pkg/run/llm_middleware.go` 的 `compactRunGuard`：键粒度问题归 045，本计划不碰。
- `pkg/run/compaction.go` 的 `provisionalTail` / `provisionalTailLen`：归 046。
- `prepareSubagentExecutionResolved` 的 `own.RunRT == nil` 分支（`pkg/run/subagent.go:1326-1341`）：不为无 run 行的子任务编造 run ID。
- `pkg/process`、`pkg/turn`、`pkg/gateway`：不改。
- `pkg/run/subagent.go` 的其它函数（尤其 `runForkSubagent` 与 `subagentRunContext`）：它们已经是对的，不要顺手重构。

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动留在工作区，由 owner 手动审阅提交。
- 如将来提交，commit message 建议：`fix(run): a fork continuation speaks under its own run`。

## Steps

### Step 1: 锚点与基线

```bash
git diff --stat a8140fc..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Fork|SubagentContinue|Continuation'
```

并确认下面五条各恰好 1 行：

```bash
grep -n "agentCtx := ctx" pkg/run/subagent.go                                        # continueForkSubagent 里的那一行（2458 附近）
grep -n "llmClient := own.ForkLLM()" pkg/run/subagent.go                             # 2 行：1077（首跑）与 2428（续跑）
grep -n "agentCtx = tool.WithRunID(agentCtx, entryRunID)" pkg/run/subagent.go        # 恰好 1 行（首跑 1120）
grep -n "hostCtx = llm.WithAgentSessionID(hostCtx, ws)" pkg/run/subagent.go          # 恰好 1 行（subagentRecordContext）
grep -n "func continueForkSubagent" pkg/run/subagent.go                              # 恰好 1 行
```

**Verify**: `git diff --stat` 无输出（或差异与下述摘录一致）；测试 `ok`；五条 grep 的条数如上（`ForkLLM` 那条是 2 行）。

### Step 2: 先写会失败的回归测试

**2a.** 给 `pkg/run/subagent_test.go:3070-3105` 的 `scriptedRequestsLLM` 加一个上下文记录（它现在丢弃 ctx，无法断言身份）：

```go
type scriptedRequestsLLM struct {
	mu        sync.Mutex
	requests  [][]llm.Message
	tools     [][]*llm.Tool
	// contexts records the context each request was made under: a run's
	// identity lives there, so a test can assert which run a request spoke for.
	contexts  []context.Context
	responses []string
}

func (m *scriptedRequestsLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	cp := deepCloneMessages(msgs)
	m.mu.Lock()
	m.requests = append(m.requests, cp)
	m.tools = append(m.tools, tools)
	m.contexts = append(m.contexts, ctx)
	...
}

func (m *scriptedRequestsLLM) gotContexts() []context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]context.Context(nil), m.contexts...)
}
```

**2b.** 在 `pkg/run/subagent_test.go` 文件末尾追加（夹具用法照 `TestForkContinuationReplaysItsOwnPrefixByteForByte`，3612 行起）：

```go
// A fork's continuation drives its own run the way its first execution did: the
// request it sends speaks for the fork's run, not for the run that dispatched
// the continuation. The dispatcher's run id must not travel into the child's
// requests, or the child's compaction guard and its approvals are filed under
// the parent's run.
func TestForkContinuationSpeaksUnderItsOwnRun(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system", "continuing")
	workerID := "main:conv-ownrun:worker:11111111-2222-3333-4444-555555555555"
	fix.seedWorker(t, "conv-ownrun", workerID, []llm.Message{
		llm.UserMessage(llm.Text("forked directive")),
		llm.AssistantMessage([]llm.ContentPart{llm.Text("fork finding")}),
	})
	if _, _, err := fix.store.FreezeSessionPromptState(context.Background(), workerID, forkSystemPromptKey, "frozen fork system"); err != nil {
		t.Fatal(err)
	}
	if err := agent.AppendHistory(fix.fac.subagentScopeRoot(), agent.HistoryEntry{
		TaskID:          "task-ownrun",
		AgentID:         "fork-ownrun-agent",
		RunID:           "child-own-run",
		ParentRunID:     "parent-1",
		SessionID:       "conv-ownrun",
		WorkerSessionID: workerID,
		Task:            "forked directive",
		Status:          agent.StatusOK,
		Output:          "fork finding",
		AgentType:       "fork",
		AgentKind:       "fork",
		RuntimeKind:     "fork_subagent",
		Continuable:     true,
		StartedAt:       10,
		UpdatedAt:       20,
		FinishedAt:      20,
	}); err != nil {
		t.Fatal(err)
	}
	// The continuation is dispatched during the parent's turn: the ctx carries
	// the parent's run id, exactly as production's dispatching turn does.
	ctx := toolpkg.WithToolUseID(toolpkg.WithRunID(llm.WithAgentSessionID(context.Background(), "conv-ownrun"), "parent-1"), "call-1")
	if _, err := continueSubagentExecution(ctx, fix.fac, &SubagentContinueInput{TaskID: "task-ownrun", Message: "go on"}); err != nil {
		t.Fatalf("continueSubagentExecution: %v", err)
	}
	contexts := fix.llm.gotContexts()
	if len(contexts) == 0 {
		t.Fatal("the continuation sent no request")
	}
	for i, reqCtx := range contexts {
		if got := toolpkg.RunIDFromContext(reqCtx); got != "child-own-run" {
			t.Fatalf("request %d spoke for run %q, want the fork's own run %q", i, got, "child-own-run")
		}
		if got := llm.AgentSessionIDFromContext(reqCtx); got != workerID {
			t.Fatalf("request %d carried agent session %q, want the worker session %q", i, got, workerID)
		}
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestForkContinuationSpeaksUnderItsOwnRun -v`
→ **FAIL**，信息是 `request 0 spoke for run "parent-1", want the fork's own run "child-own-run"`。把输出原样贴进报告。
（若它**通过**：说明当前代码已经装过 run ID 了——按 STOP 报告，不要继续。）

### Step 3: 补上 run ID

`pkg/run/subagent.go` 的 `continueForkSubagent`，在 `agentCtx := ctx` 之后、`WithPartialSessionCapture` 之前插入（与首跑 1113-1120 的写法一致）：

```go
	// The continuation drives the fork's own run, so its requests speak for
	// that run: the compaction guard, the approval queue and the durable wait
	// row of every tool this execution runs all resolve their identity from the
	// run id in this context, and the dispatching turn's run id belongs to a
	// different history.
	if runID := strings.TrimSpace(record.RunID); runID != "" {
		agentCtx = tool.WithRunID(agentCtx, runID)
	}
```

`strings` 与 `tool` 该文件已 import，无需新增。

**Verify**:
```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestForkContinuationSpeaksUnderItsOwnRun -v   # PASS
CGO_ENABLED=1 go build ./...                                                                          # exit 0
```

### Step 4: 回归与门禁

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run 'Fork|SubagentContinue|Continuation|Compaction|Overflow|Retract|Steer|Recall|Abandoned'
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run ./pkg/process ./pkg/turn
CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run
CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...
gofmt -l pkg/run
git status --short -- pkg/run
```

**Verify**: 全部 `ok`；race 无 `DATA RACE`；vet/gofmt 无输出；`git status` 相对执行前只多出 `pkg/run/subagent.go`、`pkg/run/subagent_test.go` 两个文件的改动。

### Step 5: 索引

把 `plans/README.md` 中 044 的状态行改为 `DONE`（若走到 STOP 则改 `BLOCKED` 并写一句原因）。

**Verify**: `grep -n "044-fork-continuation-owns-its-run" plans/README.md` → 1 行，状态为 `DONE`。

## Test plan

- **新增回归**：`TestForkContinuationSpeaksUnderItsOwnRun`（`pkg/run/subagent_test.go`）——用 `newPersistenceFixture` 起一条 fork 记录
  （`RunID: "child-own-run"`、`ParentRunID: "parent-1"`、`WorkerSessionID: <worker>`），派发上下文带 run `parent-1`，跑
  `continueSubagentExecution`，断言续跑发出的**每一个**请求的 ctx 里 `tool.RunIDFromContext == "child-own-run"` 且 agent session 仍是 worker 会话。
  修复前失败、修复后通过。结构范本：`TestForkContinuationReplaysItsOwnPrefixByteForByte`（3612 行起）。
- **既有测试必须原样通过**，尤其：`TestForkContinuationReplaysItsOwnPrefixByteForByte`（3612）、
  `TestForkSubagentContinuationCompactsFirstWhenItNoLongerFits`（3695）、`TestContinueSubagentExecutionReusesChildRunAndPreviousOutput`（2018）、
  `TestContinueSubagentExecutionAppendsUpdatedHistory`（1345）、`TestContinueSubagentExecutionRejectsOneShotSubtype`（2531）、
  以及 `pkg/run/compaction_test.go` 与 `pkg/run/orchestration_llm_test.go` 里全部压缩 / 撤回 / 溢出用例。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run -run TestForkContinuationSpeaksUnderItsOwnRun` → `ok`（修复前为 FAIL，报告里有修复前的失败输出）
- [ ] `grep -n "if runID := strings.TrimSpace(record.RunID); runID != \"\"" pkg/run/subagent.go` → 恰好 1 行，且位于 `continueForkSubagent` 内
- [ ] `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/run` → `ok`，无 `DATA RACE`
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/run ./pkg/process ./pkg/turn` → 三个包 `ok`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run/...` 与 `gofmt -l pkg/run` 均无输出
- [ ] `git status --short -- pkg/run` 只多出 `subagent.go`、`subagent_test.go` 的改动（045/046 留下的改动不算）
- [ ] `plans/README.md` 中 044 为 `DONE`

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 发现 `pkg/run/subagent.go` 在 a8140fc 之后被改过，且 "Current state" 的摘录与实码对不上。
- Step 2 的新测试**通过**（说明续跑已经在装 run ID，本计划的前提不成立）。
- 需要改 `pkg/run/subagent.go` 以外的生产文件（尤其 `llm_middleware.go` 的 guard——那是 045）。
- 装 run ID 之后，任何既有 subagent / fork / 压缩 / 审批测试失败：说明有代码依赖「续跑沿用父 run ID」，**不要改测试迁就**，把失败的用例与判断贴出来回报。
- `-race` 报 `DATA RACE` 且涉及本改动的代码。

## Maintenance notes

- **不变式**（本计划之后）：一个 run 身份只由「正在执行这次调用的 ctx」决定（`runner.go:1347-1363`）。任何**直接驱动**子 agent（不走 supervisor）的新入口，
  都必须同时装子代理自己的 run ID 与 worker 会话；本计划补的是 `continueForkSubagent`，此前 `runForkSubagent` 已经这么做。
- **与 045 的关系**：本计划让「续跑」不再借父 run ID，但 `own.RunRT == nil` 时子代理记录本来就没有 run ID（这种子任务是借来的身份），
  以及任何未来「同 run、两段历史」的调用，仍要靠 045 的 `(run ID, worker 会话)` 多槽 guard 才不会互相顶替。两者互不替代。
- **审阅重点**：只看 `continueForkSubagent` 里新增的三行；确认它没有影响 `record.RunID` 为空的情况（那三行是 no-op），也没有改动首跑路径。
- 后续若把 `subagentRecordContext` 变成「装全身份」的统一入口（把 run ID 也放进去），需重新核对：它同时服务 typed 子代理，而 typed 子代理的身份由
  supervisor 装，重复装同一值无害、装成不同的值会串台。
