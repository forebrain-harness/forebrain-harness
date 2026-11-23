# Plan 177：子代理视图的 Working / Worked for 也显示首 token 等待（引擎量、两端显示）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func (t \*Tracker) ObserveFirstToken" pkg/tui/reducer.go` 为 `1`；
> `grep -c "func (r \*Reducer) subagentWorkedFrames" pkg/tui/reducer.go` 为 `1`；
> `grep -c "func workingStatusFormatter" pkg/tui/run.go` 为 `1`；
> `grep -c "type SubagentEndedPayload struct" pkg/event/run_events.go` 为 `1`；
> `grep -c "FirstTokenMs int64" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "'chat.subagentWorking'" frontend/src/locales/index.ts` 为 `2`；
> `grep -c "chat.firstToken" frontend/src/locales/index.ts` 为 `2`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tui pkg/event pkg/run pkg/gateway frontend/src` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/run ./pkg/gateway -count=1` 与
> `cd frontend && pnpm vitest run src/components/chat src/composables/useChatStream.test.ts`。

## Status

- **Priority**: P2（owner 2026-10-26 明确要求：两端子代理视图的 Working 与 Worked for 都要有这个数）
- **Effort**: M（引擎一处测量 + 一条事件字段 + 两个 surface 各两处显示 + 测试）
- **Risk**: MED（引擎要新增测量点；但**不改**主会话既有的首 token 链路）
- **Depends on**: 无（与 `175`/`176` 无冲突：它们改用量与速率，本计划只加一个**等待时间**）
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: feature（可观测性）+ bug（数据缺口）
- **Owner 要求（原文）**：「tui和web端的子代理视图Working和Worked for消息没有显示first token，需要加上」

## Why this matters

主会话**两端都有**这个数：

- 终端：`pkg/tui/run.go:4306` 的 `firstTokenLabel(counters.FirstToken)` 挂在 Working 行上；
  结束行的 `pkg/tui/reducer.go:1837` 同样；
- 网页：`frontend/src/composables/useChatStream.ts:2482-2497` 的 `formatRuntimeStatusLabel` 把
  `chat.firstToken` 拼进运行时状态行；`workedLineOf`（`:2157-2164`）把 `firstTokenMs` 拼进 "Worked for"。

**子代理视图两端都没有**，而且原因不同——这一点是本计划的核心，先说清楚：

1. **终端**：数据结构已经有了（`Counters.FirstToken`），但**子代理的那份从来没被写入**。
   `Tracker.ObserveFirstToken`（`:7959-7975`）只写 `runUsage[runID]`；而子代理视图读的是
   `SnapshotAgentRun(agentID)`（`:7774-7792`）→ `agentRuns[agentID].counters`。两条 map 不相通，
   所以 `workingStatusFormatter` 的 agent 分支（`pkg/tui/run.go:4266-4284`）与 `subagentWorkedFrames`
   （`pkg/tui/reducer.go:2153-2174`）拿到的 `FirstToken` 恒为 0。
2. **网页**：数据**根本没有**到达子代理视图。
   - 实时：`first_token` op 由网关 `pkg/gateway/server.go:2217-2231` 在**主会话**的 `turn.TurnRequest.OnFirstToken`
     里发出，载荷只有 `first_token_ms`（+ run/session id）——没有 `agent_id`；
     客户端收到后只写 `runtimeStatus` 与主答案消息（`useChatStream.ts:5378-5390`）。
   - 重载：子代理每次执行的 "Worked for" 行由 `subagent_ended` 事件构造
     （`useChatStream.ts:4645-4662`：`kind: 'worked'`，只有 `durationMs`/`finishedAt`/`plan`），
     而 `SubagentEndedPayload`（`pkg/event/run_events.go:415-431`）**没有首 token 字段**。
3. **引擎**：子代理执行**从来没有测量过首 token**。子代理的回合不走 `turn.Service.Submit`
   （那条路才有 `FirstTokenStore`/`OnFirstToken`，见 `pkg/turn/submit.go:126-145`），而是
   `pkg/process/worker_cli.go:132` 的 `run.Run(opts)`。所以既没有落库值，也没有实时通知。

结论：要两端都显示，必须先**在引擎里量出来**，再让它跟着**执行**走（不是跟着主 run 走）。

## 口径（与主会话完全一致，不要另立一套）

- 定义：**该次子代理执行**从开始到**模型第一个输出**（首个 `assistant_delta` 或 `reasoning_delta`）的墙钟时间。
  与 `pkg/turn/submit.go:126-145` 给主回合算的是同一件事、同一单位（`time.Duration`，显示到 0.1 s）。
- 一次执行只记一次（续跑/多轮不再覆盖），与 `ObserveFirstToken` 的既有规则一致。
- 没有输出（例如启动即失败）→ 没有这个数，视图**不显示**，不显示 `0s`。
- 文案**复用**既有的 `chat.firstToken`（中「首 token {duration}」/ 英「first token {duration}」）与终端的
  `firstTokenLabel`，不新增词汇。

## Current state

终端的两个显示点（数据在 `Counters` 里，只是没人写）：

```go
// pkg/tui/run.go:4266-4284（子代理视图的 Working 行）
func workingStatusFormatter(counterSnapshot func() Counters, agentSnapshot func(string) (AgentRunSnapshot, bool)) transientStatusFormatter {
	return func(elapsed time.Duration, tick int, view string) string {
		if view = strings.TrimSpace(view); view != "" {
			…
			title := transientSpinnerFrame(tick) + " Working (" + formatWorkingElapsed(snap.Elapsed) + ")"
			if counters := formatWorkingCounters(snap.Counters); counters != "" {
				title += " · " + counters
			}
			return title
		}
```

```go
// pkg/tui/reducer.go:2153-2174（子代理视图的结束行）
func (r *Reducer) subagentWorkedFrames(agentID string) []Frame {
	snap, ok := r.tracker.SnapshotAgentRun(agentID)
	…
	title := workedForLabel(snap.Elapsed)
	if counters := formatWorkingCounters(snap.Counters); counters != "" {
		title += " · " + counters
	}
```

主会话的写法（**照它写**，`pkg/tui/run.go:4300-4310` / `pkg/tui/reducer.go:1837`）：

```go
		if wait := firstTokenLabel(counters.FirstToken); wait != "" {
			title += " · " + wait
		}
```

网页的两个显示点：

```ts
// frontend/src/components/chat/SubagentTab.vue:279-282
const workingLabel = computed(() =>
  retryLine.value ? retryLine.value.label : t('chat.subagentWorking', { duration: duration.value }))
```

```ts
// frontend/src/composables/useChatStream.ts:4645-4662（每次执行的 Worked for 行）
			blocks = [...blocks, {
			  kind: 'worked' as const,
			  id: `worked:${executionId}`,
			  durationMs: Math.max(0, finishedAtMs - startedAtMs),
			  finishedAt: new Date(finishedAtMs).toISOString(),
			  ...(entry.plan && entry.plan.total > 0 ? { plan: entry.plan } : {}),
			}]
```

## Scope

**In scope（只改这些）**：
- `pkg/run/subagent.go`（在该次执行的状态里记下首 token 时间；随 `subagent_ended` 一起发）
- `pkg/event/run_events.go`（`SubagentEndedPayload` 增加 `FirstTokenMs int64`）
- 子代理执行的**实时**通知路径：`pkg/event`（若无现成事件则复用 `first_token` 的 ws op）+ `pkg/gateway`
  （`server.go` 的 `first_token` 推送带上 `agent_id`）+ `pkg/tui/notify.go`（`FirstTokenMsg` 带 `AgentID`）
- `pkg/tui/reducer.go`（`ObserveFirstToken` 同时写 agent 的 counters；`subagentWorkedFrames` 追加等待）
- `pkg/tui/run.go`（`workingStatusFormatter` 的 agent 分支追加等待）
- `frontend/src/composables/useChatStream.ts`（`first_token` op 归属到子代理执行；
  `worked` block 带 `firstTokenMs`）
- `frontend/src/components/chat/SubagentTab.vue`（Working 行追加首 token）
- 子代理视图的 worked 行渲染处（`SubagentConversation.vue` 或它用的共享行组件：`firstTokenMs` → 文案）
- `frontend/src/locales/index.ts`（**只在缺键时**补；`chat.firstToken` 已存在，优先复用）
- 两端测试

**Out of scope（看着相关也不要动）**：
- 主会话的首 token 链路（`pkg/turn/submit.go` 的 `FirstTokenStore`、`fb_runs.first_token_ms` 的写入规则）
- 子代理的用量/重试/账本（`175`、`165`）
- 子代理视图的其余部分（输入框、插话、队列、预算）
- "上一轮"之类的新概念；本计划只是把同一个等待时间补到缺的两个视图上

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 引擎定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentFirstToken' -count=1 -v` | PASS |
| 终端定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Subagent.*FirstToken' -count=1 -v` | PASS |
| 网关定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'FirstToken' -count=1 -v` | PASS |
| Go 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/event -count=1` | 与基线相比无新增 FAIL |
| 前端定向 | `cd frontend && pnpm vitest run src/components/chat -t 'first token'` | PASS |
| 前端回归 | `cd frontend && pnpm vitest run src/components/chat src/composables/useChatStream.test.ts` | 与基线相比无新增 FAIL |
| 类型/构建 | `cd frontend && pnpm typecheck`；`pnpm build` | exit 0 |

## Steps

### Step 0（强制）：确认"该次执行的第一个 delta"在引擎里的唯一观测点

子代理的回合走 `pkg/process/worker_cli.go:132` 的 `run.Run(opts)`，所以它没有 `turn.Submit` 的那套回调。
执行者在动手前必须先把**"子代理执行发出的第一个 `assistant_delta` / `reasoning_delta` 事件"**的发布点找出来，
并报告它的 `file:line` 与它当时能拿到的"执行开始时间"。

已知事实（本计划据以推断，Step 0 要确认）：子代理的事件由 `pkg/run/subagent.go` 的 `publishEvent` 族发布，
且 `subagent_spawned` / `subagent_ended` 携带 `agent_id`、`execution_id`、`finished_at_ms`
（`pkg/event/run_events.go:415-431`）。寻找方式：

```
grep -rn "RunEventAssistantDelta\|assistant_delta" --include=*.go pkg/run/ | grep -v _test
grep -rn "func .*publishEvent\|WithAgentID\|agentID" pkg/run/subagent.go | head -20
```

**判定**：

- 如果**所有**子代理的 delta 都经过**一个**发布点，且那里能拿到该次执行的开始时间 → 这就是测量点，继续。
- 如果需要跨多个点（流式/非流式回放各一条），**优先选共享的那一层**（例如把"第一次 delta 的时间"记在
  执行状态里，由多个发布点共享同一个 `sync.Once`）。
- 如果**没有任何**共同层（STOP 3）：停下报告，附上你找到的所有发布点。**不要**用"在 `run.Run` 前后包一层
  计时"来糊（那量到的是整次执行时长，不是首 token）。

把结论写进执行报告（一行即可：`测量点 = <file:line>`）。

### Step 1：引擎记账（执行级，一次）

在该次执行的状态上（`subagentExecution` 一类的结构，Step 0 会指出确切位置）增加一个"首 token"字段，
用 `sync.Once`（或既有的等价机制）保证只记第一次：

```go
	// firstToken is how long this execution waited for the model's first output:
	// stamped once, from the same delta the transcript draws. A resumed attempt
	// is the same execution and keeps the wait its first answer had — the rule
	// the main turn's FirstTokenStore applies.
	firstTokenOnce sync.Once
	firstToken     time.Duration
```

在测量点里：`firstTokenOnce.Do(func(){ firstToken = time.Since(start) })`（`start` 用该次执行的开始时刻，
与 `subagent_spawned` 的时间戳同源；报告里要说明取的是哪一个）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentFirstTokenIsMeasuredOnce' -count=1 -v`
→ PASS（用例：两个 turn 的执行，第一次有输出、第二次不再覆盖；断言等于第一次的等待且 > 0）。

### Step 2：实时通知（两端各一条，都带 agent id）

1. **事件**：`SubagentEndedPayload` 增加

```go
	// FirstTokenMs is how long this execution waited for the model's first
	// output; 0 when it never produced one. The subagent views draw it on the
	// "Worked for" line, and a reload reads it from here.
	FirstTokenMs int64 `json:"first_token_ms,omitempty"`
```

并在发布 `subagent_ended` 处填上 Step 1 的值。

2. **实时**（Working 行在执行就让读者看到，与主会话一致）：
   - 终端：`FirstTokenMsg`（`pkg/tui/notify.go:151`）增加 `AgentID string`；子代理执行第一次 delta 时发一条
     （**已有**的 `FirstTokenMsg` 是主会话从 `turn.Submit` 的回调发的，不要动它）。
   - 网页：网关的 `first_token` op（`pkg/gateway/server.go:2217-2231`）在子代理场景下带 `agent_id`。
     **子代理的执行不在那条 `OnFirstToken` 回调里**——所以要么由 `pkg/run/subagent.go` 另发一条同名 op
     （网关已有 op 表白名单 `pkg/gateway/ws_protocol.go:59-62`），要么在 `subagent_ended` 之前发一条轻量事件；
     **选前者**（与主会话同一个 op，客户端只需多认一个 `agent_id`）。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'FirstToken' -count=1 -v` → PASS
（用例：子代理执行产生首 token 时，推送的帧里 `agent_id` 与 `first_token_ms` 都在）。

### Step 3：终端 —— 把值接到已有的两个显示点

1. `pkg/tui/reducer.go` 的 `ObserveFirstToken`：改成同时写 agent 的 counters。
   它今天只有 `runID`，需要能把 `runID` 映射到 `agentID`：执行开始时的 `subagent_spawned`（或既有的
   `ObserveSubagentUsage(runID, agentID, …)` 路径）已经同时拿到两者——**在那里登记 `runID → agentID`**
   （一个新 map，随会话重置一起清），然后：

```go
	if agentID := t.agentOfRunLocked(runID); agentID != "" {
		if run := t.agentRuns[agentID]; run != nil && run.counters.FirstToken == 0 {
			run.counters.FirstToken = wait
		}
	}
```

2. `pkg/tui/run.go` 的 `workingStatusFormatter` agent 分支（`:4279-4283`）追加：

```go
			if wait := firstTokenLabel(snap.Counters.FirstToken); wait != "" {
				title += " · " + wait
			}
```

3. `pkg/tui/reducer.go` 的 `subagentWorkedFrames`（`:2157-2162`）同样追加。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Subagent.*FirstToken' -count=1 -v`
→ PASS（两条用例，逐个钉住；见 Test plan）。

### Step 4：网页 —— 实时与重载两条路

1. `useChatStream.ts` 的 `first_token` 处理（`:5378-5390`）：若 `payload.agentId` 非空 → 把等待时间写到**那个子代理**
   的执行状态上（`subagentCallTasks` 里该 agent 当前执行，或 `subagents` 里该 agent 的当前 `record`：
   加一个 `firstTokenMs?: number`），**不要**同时写主会话的 `runtimeStatus`。
2. `SubagentTab.vue` 的 `workingLabel`：有 `firstTokenMs` 时追加 ` · ` + `chat.firstToken`：

```ts
const workingLabel = computed(() =>
  retryLine.value
    ? retryLine.value.label
    : t('chat.subagentWorking', { duration: duration.value }) + firstTokenSuffix.value)
```

（`firstTokenSuffix` 用既有的 `formatFirstTokenDuration`，与主会话同一格式；**复用** `chat.firstToken` 文案。）

3. 重载：`subagent_ended` 的 `worked` block 增加 `firstTokenMs`（来自 `payload.firstTokenMs`），
   `TimelineBlock` 的 `worked` 变体加字段；工作视图里那条 worked 行渲染时，把
   `formatFirstTokenDuration(firstTokenMs)` 拼在时长之后（与主会话 `workedLineOf` 的拼法一致）。

**Verify**：`cd frontend && pnpm vitest run src/components/chat -t 'first token'` → PASS；
`cd frontend && pnpm typecheck` → exit 0。

### Step 5：回归

**Verify**：Go 四个包 + 前端两套测试与基线相比无新增 FAIL；`cd frontend && pnpm build` exit 0。
额外确认**主会话**的首 token 没有被动坏：`grep -c "firstTokenLabel(counters.FirstToken)" pkg/tui/run.go` 仍为 `1`，
且 `useChatStream.ts` 的主会话 `first_token` 分支仍在（无 `agentId` 时行为不变）。

## Test plan

- `pkg/run`：`TestSubagentFirstTokenIsMeasuredOnce`（第二次 turn 不覆盖；等于第一次的等待）。
- `pkg/run`：`TestSubagentEndedCarriesFirstToken`（事件 payload 的 `first_token_ms` 等于测得值；无输出时为 0/缺省）。
- `pkg/tui`：`TestSubagentWorkingLineShowsFirstToken`（`workingStatusFormatter` 的 agent 分支输出含 `first token`）；
  `TestSubagentWorkedFrameShowsFirstToken`（`subagentWorkedFrames` 的输出含 `first token`）。
- `pkg/gateway`：`TestFirstTokenPushCarriesAgentIDForSubagents`（帧里两字段齐全；主会话帧不变）。
- 前端：`SubagentTab.test.ts` 的 "first token" 用例（有值时出现、无值时不出现）；
  `useChatStream.test.ts` 里一条"`first_token` 带 `agentId` 只写子代理、不写主状态"的断言。

## Done criteria

机器可检查，全部满足：

- [ ] 执行报告里写着 `测量点 = <file:line>`（Step 0 的结论）
- [ ] 五个 Go 新测试 + 两个前端用例 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/event -count=1` 与基线相比无新增 FAIL
- [ ] `cd frontend && pnpm vitest run src/components/chat src/composables/useChatStream.test.ts` 与基线相比无新增 FAIL；`pnpm typecheck`、`pnpm build` exit 0
- [ ] `grep -c "FirstTokenMs int64" pkg/event/run_events.go` 为 `1`
- [ ] `grep -c "firstTokenMs" frontend/src/components/chat/SubagentTab.vue` ≥ `1`
- [ ] `grep -c "firstTokenLabel(snap.Counters.FirstToken)" pkg/tui/run.go` 为 `1`；同一条在 `pkg/tui/reducer.go` 为 `1`
- [ ] 主会话链路未动：`git diff -- pkg/turn/submit.go pkg/state/session_store.go` **无输出**
- [ ] `git status --porcelain` 只显示 Scope 里的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数与标注不符，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. Step 0 找不到"该次执行的第一个 delta"的**共同**发布点——报告所有候选点。
4. 你打算用"整次执行时长"或"首字节时间"来代替首 token —— **不要**，那与主会话的口径不是一回事。
5. `pkg/tui` 里 `runID → agentID` 的映射在重置/切换会话时无法可靠清空（会串到别的子代理）：
   报告你看到的生命周期，先别加 map。
6. 网页端 `first_token` 的 `agentId` 归属需要**改主会话**的既有行为（例如把无 agentId 的帧也改写路径）：
   停手——主会话那条路必须保持逐字不变。
7. 你发现子代理的 worked 行在网页上其实由**另一个组件**渲染（不是 Step 4 说的那个）：报告组件路径，
   再把追加点挪过去；不要同时改两处。

## Maintenance notes

- 首 token 现在是**执行级**的事实：主会话在 `fb_runs.first_token_ms`，子代理在 `SubagentEndedPayload` +
  实时帧。两者**定义相同**，谁将来新增第三个视图（例如工作台里的执行历史），读这两种来源即可，不要另算。
- 评审重点：Step 3 的 `runID → agentID` map 是唯一的新状态，它必须随会话重置清空，否则一个 runID 复用到
  另一个 agent 时会把等待显示到错的视图上。
- 已知限制：子代理的首 token **不落库到 `fb_runs.first_token_ms`**（本计划不写它：那条列属于"用户回合"的
  统计口径，`/status` 与 `worked` 行按 run 读）。若将来要它在历史接口里逐行出现，再单独加。
