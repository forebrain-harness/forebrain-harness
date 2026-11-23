# Plan 009：回放时，发出无卡 exit gate 调用的那段回答也要像 live 一样封存（修 review 交付流程回放时文本重复、残留 non-final 块）；收紧 007 的 test 8；勘误段放回原位

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push，改动留在工作区。
>
> **工作目录**：本计划**只**在 worktree `/var/folders/kw/mf8l2_c564b0m8qzhj073s940000gn/T/fb-007-wt`
> （分支 `wt/improve-007-exit-gate`，基于 `9656051`）里执行。该 worktree 已有计划 007 的**未提交**实现
> （11 个文件），本计划叠加在它之上。**不要还原、不要整理 007 的任何改动**；不要碰主仓库
> `/Users/doudou/workspace/unionj-cloud/forebrain-harness` 的任何文件。所有命令先 `cd` 到 worktree。
>
> **Drift check（先跑）**：
> `cd /var/folders/kw/mf8l2_c564b0m8qzhj073s940000gn/T/fb-007-wt && git status --short`
> 预期恰好是这 11 行（顺序无关）：
> ` M docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md`、` M docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md`、
> ` M pkg/tui/channels.go`、` M pkg/tui/chat_session_test.go`、` M pkg/tui/commands.go`、` M pkg/tui/commands_test.go`、
> ` M pkg/tui/notify.go`、` M pkg/tui/reducer.go`、` M pkg/tui/reducer_test.go`、` M pkg/tui/render.go`、` M pkg/tui/tty_session.go`。
> 然后对照下方 "Current state" 摘录逐段核对 `pkg/tui/commands.go`、`pkg/tui/commands_test.go`、
> `docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md`；不一致即 STOP。

## Status

- **Priority**: P1（007 的验收目标"live 与回放一致"在 owner 截图 1 的真实流程上不成立）
- **Effort**: S
- **Risk**: LOW（只改回放循环里一处封存；live 路径不动）
- **Depends on**: plans/007-exit-gate-live-card-and-cardless-anchoring.md（其未提交实现必须在 worktree 里）
- **Category**: bug
- **Planned at**: commit `9656051` + worktree 里 007 的未提交改动，2026-10-10

## Why this matters

计划 007 让 exit_plan_mode gate 在等待时不画卡，并让它的确认行、review 卡按产生顺序落位，目标是 live 与回放
一致。live 里，gate 的 running 步骤进入 reducer 时会封存（seal）发出调用的那段回答
（`Reducer.reduceMessage` 的 `case MsgKindTool` 开头那段 `flushBufferedText`）。**回放里没有这一步**：exit gate
的等待不画卡，被取消 / 被 review 交付关闭的结果行又被整行丢弃，所以发出调用的那段回答一直不封存。
后果在 owner 截图 1 的流程（选 review → review 交付 → 模型修订 → 再次 exit → 批准）上实测为：

```
回放（worktree 当前代码）                         live（应有）
0 user        "plan the work"                   user
1 assistant   "here is the plan"   final=false   assistant "here is the plan"
2 status      "✔ You asked … review the plan"    ✔ You asked …
3 fanout      Plan review 卡                     Plan review 卡
4 assistant   "here is the planrevised plan"     assistant "revised plan"
5 status      "✔ You approved …"                 ✔ You approved …
6 tool        Exited plan mode                   ◆ Exited plan mode
```

第一段回答出现两次（一张永远停在 non-final 的残留块 + 一张把两段回答首尾粘在一起的块）。
即使没有任何审批记录，同样的缺口也会把"交付前的回答"和"修订后的回答"粘成一块：
transcript = user → assistant "first answer"（发出 exit 调用，结果行是交付行被丢弃）→ assistant "second answer"，
回放得到一个 `"first answersecond answer"` 块，live 是两块。

这个缺口在 `9656051` 就存在（不是 007 引入的），但 007 的行为矩阵与 test 8（`TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt`）
明确要求修好。007 的执行者把 test 8 的断言改成"只取第一个匹配块"（`planText < 0 &&`），测试因此在 bug 存在时仍然通过。
本计划补上回放的封存，并把 test 8 收紧到能抓住这个 bug。

## Current state（摘录，均为 worktree 当前内容）

### 1. live 侧的封存规则（**不改**，只为理解）

`pkg/tui/reducer.go:1888-1898`：

```go
	case MsgKindTool:
		// The exit-plan gate's wait, and a wait abandoned before it resolved,
		// hold no card on any surface (tool.ToolStepHoldsNoCard). Every tool
		// message reaches the transcript through here — the main agent's own
		// step hook, the published-event funnel, replay — so this is the one
		// place the rule is applied. The call still ends the response that
		// preceded it, so the streamed text is sealed exactly as a drawn card
		// would have sealed it.
		if tool.ToolStepHoldsNoCard(firstNonEmpty(msg.ToolName, msg.ToolMeta.ToolName), msg.ToolMeta.Status) {
			return r.flushBufferedText(r.agentBuf(strings.TrimSpace(msg.AgentID)), msg.Timestamp)
		}
```

`tool.ToolStepHoldsNoCard(toolName, status)`（`pkg/tool/format.go`）：toolName 为 `exit_plan_mode` 且 status 为
`running` / `awaiting approval` / `canceled` 时返回 true。`flushBufferedText` 把缓冲的 reasoning/assistant 文本
封存成 `Final: true` 的帧返回；`msg.Timestamp` 用作 reasoning 的结束时间（零值时退回 `time.Now()`，
回放里会算出离谱的 "Thought for N days"，所以回放必须传行的时间戳）。

### 2. 回放主循环 `pkg/tui/commands.go:377-411`（`replayTimelineWithReducer` 内）

```go
	flush := func(pos int) {
		for _, item := range pending[pos] {
			if item.message != nil {
				result := reducer.Reduce(item.message)
				for _, frame := range result.Frames {
					if frame.Kind != FrameThinking || frame.Final {
						renderer.RenderFrame(frame)
					}
				}
			}
			...
			for _, frame := range item.frames {
				renderer.RenderFrame(frame)
			}
		}
	}
	for i := range turns {
		flush(i)
		replayTurnWithReducer(renderer, reducer, turns[i], callIndex, subagentCalls, &planUpdates)
		if line, ok := workedLines[i]; ok {
			// The run's own text is out first, then its error if it failed;
			// its line closes it, as live.
			flushReplayReducer(renderer, reducer)
			for _, frame := range runEndFrames[line.RunID] {
				renderer.RenderFrame(frame)
			}
			renderer.RenderFrame(replayWorkedFrame(line))
		}
	}
	flush(len(turns))
	return eventCount
```

要点：`pending[pos]` 在 `turns[pos]` 回放**之前** flush。007 把无卡 gate 的审批记录与 review 事件锚在
`callRow[stepID] + 1`（发出调用的 assistant 行之后），所以它们在下一行之前被渲染——此时发出调用的那段回答
还在 reducer 缓冲里、只以 non-final 块出现在视图上。

同函数开头已有（`commands.go` 约 `:268-271`）：

```go
	callIndex, callRow := buildToolCallIndex(turns)
	resultRow := toolResultRows(turns)
```

- `callIndex map[string]replayToolCall`：call id → `replayToolCall{name, args, order}`（`order` 是该调用在整个 transcript 里的发出顺序）。
- `callRow map[string]int`：call id → 发出它的 assistant 行下标（同一 id 多次出现时取最后一行）。

函数的 doc 注释末段（`commands.go` 约 `:257-260`）：

```go
// Approval records anchor by the tool call they name, and the canceled card for
// a call that never ran is drawn at the same position and first, so the
// confirmation line the user read lands above the card it answers - exactly
// where it was live.
func replayTimelineWithReducer(renderer *Renderer, turns []state.Message, events []event.RunEvent, reducer *Reducer, planUpdates []event.PlanUpdatedPayload) int {
```

### 3. 同文件的结构样板

`orphanToolCalls`（`commands.go` 约 `:735-750`）与 `replayOrphanToolCallMessage`（约 `:772-790`）就是"按
callIndex/callRow 选出调用 → 造一条与 live 相同形状的 `MsgKindTool` 消息喂给 reducer"的既有写法，新代码照它的
结构与注释风格写，放在 `replayOrphanToolCallMessage` 之后。`commands.go` 已 import `sort`、`time`、
`pkg/event`、`pkg/tool`，**无需增删 import**。

`replayTurnWithReducer`（`commands.go:170-182`）算行时间戳的写法（照抄）：

```go
	timestamp := time.Time{}
	if turn.CreatedAt > 0 {
		timestamp = time.Unix(turn.CreatedAt, 0).UTC()
	}
```

### 4. test 8 的被放宽断言 `pkg/tui/commands_test.go:2447-2473`

```go
	planText, asked, reviewCard, revised, approved, exitCard := -1, -1, -1, -1, -1, -1
	for i, block := range renderer.vm.blocks {
		f := block.frame
		switch {
		case f.Kind == FrameTool && f.Title == "exit_plan_mode":
			...
		case f.Kind == FrameFanout:
			reviewCard = i
		case f.Kind == FrameAssistant && planText < 0 && strings.Contains(f.Content, "here is the plan"):
			planText = i
		case f.Kind == FrameAssistant && strings.Contains(f.Content, "revised plan"):
			revised = i
		...
```

（`TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt` 从 `:2400` 开始；它的 turns/events 构造不改。）
同文件的行构造助手：`assistantToolCallRow(content, callID, toolName, args)`、`toolRowWithDisplay(rowID, callID, body, metaJSON)`。

### 5. 勘误段落位 `docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md:3-23`

文件头第一个引用块原本是第 3-5 行（执行者须知）+ 第 11-23 行（`>` 空行、"评审记录…"、"实施中 owner 裁决…"）
连续的一整块。007 把勘误段（第 6 行空行 + 第 7-10 行）插进了这块**中间**，导致第 11-23 行在 Markdown 里并入
勘误引用块，读起来像勘误的一部分。007 计划的要求是"文件头的第一个引用块**之后**"。
（另一份 `docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md` 的勘误位置正确，不动。）

### 仓库约定

- 注释英文、完整句子、写"为什么"，风格见上面摘录；不写"修复了 bug X"式的历史叙述。
- 测试断言要能在 bug 存在时失败；不新增"考古式"断言。
- `pkg/architecture` 测试守护 import 图；本计划不增删 import。

## Commands you will need（均在 worktree 根目录执行）

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 单测过滤 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<Name>' -count=1` | ok |
| TUI 全量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok（约 45s） |
| 架构守护 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |
| vet | `go vet ./pkg/tui` | 无输出 |
| fmt | `gofmt -l pkg cmd` | 无输出 |

## Scope

**In scope**（只改这些）：
- `pkg/tui/commands.go` — 新增 `heldGateCall`、`heldGateCallsByRow`、`replayHeldGateStepMessage`；回放主循环里补封存；`replayTimelineWithReducer` doc 补一段。
- `pkg/tui/commands_test.go` — 收紧 test 8；新增 1 个测试。
- `docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` — 只移动勘误段的位置，文字不改。

**Out of scope**（不要碰）：
- `pkg/tui/reducer.go`（包括 `case MsgKindTool` 的过滤位置）、`render.go`、`channels.go`、`notify.go`、`tty_session.go`、
  `reducer_test.go`、`chat_session_test.go`——007 已评审通过，本计划不改 live 路径。
- `replayToolMessage` 与 `orphanToolCalls` 里既有的 `ToolStepHoldsNoCard` 过滤——保留。
- `approvalEventAnchorPosition`、`replayApprovalEventFrames`、`planReviewAnchorByAction`——落位规则不改，只补封存。
- 007 的其他测试（`TestReplayPlacesCardlessGateConfirmationAfterItsCall`、`TestReplayCardlessGateDecisionPrecedesItsSettledCard` 等）——不改，必须仍通过。
- 主仓库目录、`plans/` 目录、web 前端。

## Git workflow

不建分支、不 commit、不 push（owner 手动提交）。改动留在 worktree 工作区。

## Steps

### Step 1：先把 test 8 收紧到能抓住 bug（此时应当失败）

`pkg/tui/commands_test.go` 的 `TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt`：

1. 把 `case f.Kind == FrameAssistant && planText < 0 && strings.Contains(f.Content, "here is the plan"):` 改回
   `case f.Kind == FrameAssistant && strings.Contains(f.Content, "here is the plan"):`。
   注意 switch 的 case 顺序：这个 case 在 "revised plan" 的 case **之前**，所以一个同时含两段文字的粘连块会被它截获、
   `revised` 拿不到——这正是要的失败信号。
2. 在现有的 `if planText < 0 || …` 检查**之前**，紧接 for 循环之后，加入：

```go
	// Each response is its own sealed block, exactly once: the first one ended
	// when it issued the gate's call, so the revision that followed the review
	// is a new block, not the first response with the revision appended.
	for _, block := range renderer.vm.blocks {
		f := block.frame
		if f.Kind != FrameAssistant {
			continue
		}
		if !f.Final {
			t.Fatalf("a replayed response was left unsealed: %#v", f)
		}
		if strings.Contains(f.Content, "here is the plan") && strings.Contains(f.Content, "revised plan") {
			t.Fatalf("two responses replayed as one block: %q", f.Content)
		}
	}
```

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt' -count=1`
→ **FAIL**，失败信息是 `a replayed response was left unsealed` 或 `two responses replayed as one block`。
若此时**通过** → STOP 1。

### Step 2：新增只看 transcript 的封存测试（此时应当失败）

在 `pkg/tui/commands_test.go` 中 `TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt` 之后新增：

```go
// A gate whose wait holds no card still ends the response that issued it: live,
// its running step sealed the streamed text. A delivered review's answer row
// draws nothing, so replay alone has to seal that response, or the revision
// the planner wrote next is appended to it as one block.
func TestReplaySealsTheResponseThatIssuedACardlessGate(t *testing.T) {
	turns := []state.Message{
		{RowID: 1, Role: "user", Content: "plan the work", CreatedAt: 99},
		assistantToolCallRow("first answer", "call-exit-a", "exit_plan_mode", "{}"),
		toolRowWithDisplay(3, "call-exit-a", tool.PlanReviewDeliveredDisplayKey, `{"tool_name":"exit_plan_mode","status":"denied"}`),
		{RowID: 4, Role: "assistant", Content: "second answer", CreatedAt: 100},
	}
	renderer := NewRenderer(nil, nil)
	renderer.viewportMode = true
	renderer.composerSuppressed = true
	reducer := &Reducer{}
	replayTimelineWithReducer(renderer, turns, nil, reducer, nil)
	flushReplayReducer(renderer, reducer)

	var got []string
	for _, block := range renderer.vm.blocks {
		f := block.frame
		if f.Kind != FrameAssistant {
			continue
		}
		if !f.Final {
			t.Fatalf("a replayed response was left unsealed: %#v", f)
		}
		got = append(got, f.Content)
	}
	want := []string{"first answer", "second answer"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("replayed responses:\n%q\nwant:\n%q", got, want)
	}
}
```

（若 `assistantToolCallRow` 生成的行没有 `RowID`/`CreatedAt`，不影响本测试；不要改助手。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestReplaySealsTheResponseThatIssuedACardlessGate' -count=1`
→ **FAIL**，`got` 为 `["first answersecond answer"]`。若通过 → STOP 1。

### Step 3：回放在发出无卡 gate 调用的行之后补喂 running 步骤

`pkg/tui/commands.go`：

1. 在 `replayOrphanToolCallMessage` 函数之后新增：

```go
// heldGateCall is a call whose wait holds no card (tool.ToolStepHoldsNoCard):
// the exit-plan gate. Live, its running step still reached the reducer, which
// drew nothing for it but sealed the response that issued it.
type heldGateCall struct {
	callID string
	name   string
	order  int
}

// heldGateCallsByRow groups those calls by the assistant row that issued them,
// in the order the model asked for them. Replay has no row of its own for the
// running step — the wait draws nothing, and a canceled or review-delivered
// answer row is dropped — so this is where it learns the step happened.
func heldGateCallsByRow(callIndex map[string]replayToolCall, callRow map[string]int) map[int][]heldGateCall {
	out := make(map[int][]heldGateCall)
	for callID, row := range callRow {
		call, ok := callIndex[callID]
		if !ok || !tool.ToolStepHoldsNoCard(call.name, "running") {
			continue
		}
		out[row] = append(out[row], heldGateCall{callID: callID, name: call.name, order: call.order})
	}
	for row := range out {
		calls := out[row]
		sort.SliceStable(calls, func(i, j int) bool { return calls[i].order < calls[j].order })
	}
	return out
}

// replayHeldGateStepMessage is the running step live fed the reducer when the
// gate's call started. The reducer draws nothing for it and seals the response
// that issued the call (Reducer.reduceMessage), at the issuing row's time.
func replayHeldGateStepMessage(call heldGateCall, at time.Time) Message {
	return Message{
		Kind:      MsgKindTool,
		StepID:    call.callID,
		ToolName:  call.name,
		ToolMeta:  tool.ToolMeta{ToolName: call.name, Status: "running"},
		ToolPhase: event.RunEventToolStarted,
		Timestamp: at,
	}
}
```

2. 回放主循环（`for i := range turns {` 那段，见 Current state §2）改为：

```go
	heldCalls := heldGateCallsByRow(callIndex, callRow)
	for i := range turns {
		flush(i)
		replayTurnWithReducer(renderer, reducer, turns[i], callIndex, subagentCalls, &planUpdates)
		// A held gate's records are parked right after this row; the response
		// they follow has to be sealed first, as live sealed it.
		if calls := heldCalls[i]; len(calls) > 0 {
			at := time.Time{}
			if turns[i].CreatedAt > 0 {
				at = time.Unix(turns[i].CreatedAt, 0).UTC()
			}
			for _, call := range calls {
				result := reducer.Reduce(NewMessageMsg{Msg: replayHeldGateStepMessage(call, at)})
				for _, frame := range result.Frames {
					if frame.Kind != FrameThinking || frame.Final {
						renderer.RenderFrame(frame)
					}
				}
			}
		}
		if line, ok := workedLines[i]; ok {
			...（原样保留）
```

   `heldCalls := …` 放在 `for i := range turns {` 的上一行；`flush` 闭包与循环其余部分一字不改。

3. `replayTimelineWithReducer` 的 doc 注释末段（"Approval records anchor by the tool call they name, … exactly
   where it was live."）之后、`func` 行之前补一段：

```go
//
// A gate whose wait holds no card is the exception on both counts: its records
// anchor right after the row that issued the call, and that row's response is
// sealed before them by the same running step live fed the reducer
// (heldGateCallsByRow), so the response that follows the gate is a block of
// its own rather than an extension of the one before it.
```

**Verify**：
- `CGO_ENABLED=1 go build ./...` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt|TestReplaySealsTheResponseThatIssuedACardlessGate|TestReplayPlacesCardlessGateConfirmationAfterItsCall|TestReplayCardlessGateDecisionPrecedesItsSettledCard|TestReplayTimelinePutsThePlanReviewCardBetweenTheApprovalLines|TestReplayDrawsNoCardForCanceledExitGate|TestReplayDropsCanceledExitGateSettledRow' -count=1 -v`
  → 7 个 `--- PASS`，ok。

### Step 4：勘误段放回第一个引用块之后

`docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md`：

1. 删掉第 6-10 行（第 6 行空行 + 第 7-10 行四行勘误 `> **勘误（2026-10-10 …` … `…（live 与回放一致）。`）。
   删除后原第 5 行（`> \`docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md\`,实施完成回填验收记录。`）后面紧跟原第 11 行 `>`，
   第一个引用块恢复为连续的一整块。
2. 在该引用块最后一行（以 `> 并按"删除语义无痕迹"纪律清除。` 开头的那一行）之后、`## Status` 之前，插入
   一个空行 + 原样的四行勘误，保证勘误段前后各有一个空行。

**Verify**：
- `grep -c '勘误（2026-10-10' docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` → `1`
- `awk '/勘误（2026-10-10/{a=NR} /^> 并按"删除语义无痕迹"纪律清除/{b=NR} /^## Status/{c=NR} END{print (b<a && a<c) ? "ok" : "bad"}' docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` → `ok`
- `sed -n 5,6p docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` → 第 6 行是单独的 `>`
- `git diff -- docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md | wc -l` 与改动前相同（这个文件不动）

### Step 5：回归

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1
go vet ./pkg/tui
gofmt -l pkg cmd
git diff --stat 9656051 -- pkg/architecture/testdata/graph.json
git status --short
```

**Verify**：两个测试包 ok；vet、gofmt、graph.json diff 均无输出；`git status --short` 仍是 Drift check 里那 11 行
（没有新增文件、没有新的修改路径）。

### Step 6：真机回放抽查（driver，独立 WORK/SESSION/PORT）

```bash
cd /var/folders/kw/mf8l2_c564b0m8qzhj073s940000gn/T/fb-007-wt
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-009 FOREBRAIN_RUN_SESSION=fb-009 FOREBRAIN_RUN_PORT=8809
D=.claude/skills/run-forebrain/driver.sh
$D build
```

（driver 依赖 tmux；`$D build` / `$D start` 失败 → STOP 3，不要尝试安装软件。）
toolcall 模式下 enter_plan_mode 也要审批，先 `$D key Enter` 批准。

- **C' 取消后回放**：`$D reset; $D start toolcall; $D submit 'plan something'; $D wait 'Yes, proceed' 25; $D key Enter; $D wait 'Ask another model' 25; sleep 1; $D key C-c; sleep 2`；
  `SID=$($D sid); $D stop; RESUME_ID=$SID $D start reply; sleep 3; $D screen`
  → `$D screen | grep -c 'Exited plan mode'` = 0；`$D screen | grep -c '◆ Canceled'` = 0；
  `$D screen | grep -n 'Entered plan mode\|calling exit_plan_mode\|You canceled'` 行号严格递增；
  `$D screen | grep -c 'calling exit_plan_mode'` = 1（文本没有被复制）。
- **E' review 后批准再回放**：`$D reset; $D start toolcall; $D submit 'plan something'; $D wait 'Yes, proceed' 25; $D key Enter; $D wait 'Ask another model' 25; sleep 1; $D key Down Down Down; $D key Enter; sleep 1; $D key Enter; sleep 4; $D key Down; $D key Enter; sleep 3`；
  然后同 C' 的方法回放 → 顺序 `calling exit_plan_mode` → `You asked` → `plan-reviewer` → `You approved` → `Exited plan mode`
  （`grep -n` 行号严格递增），`grep -c 'calling exit_plan_mode'` = 1。
  若 live 阶段 overlay 菜单项与上述按键不符（例如 review 选项不在第 4 项），把 `$D screen` 原文写进报告并跳过 E'，不要猜按键。

收尾：`$D stop; rm -rf $TMPDIR/fb-009`。把 C'/E' 各自的 `$D screen` 相关行原文写进报告 NOTES。

## Test plan

- 收紧：`TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt`（Step 1）——每个 assistant 块必须 final，且没有同时含两段回答的块；
  "here is the plan" 改回不带 `planText < 0` 的匹配。
- 新增：`TestReplaySealsTheResponseThatIssuedACardlessGate`（Step 2）——只有 transcript、没有事件时，交付行被丢弃的 gate 仍封存它前面的回答。
- 两者都必须在 Step 3 之前**失败**、之后**通过**（TDD 证据写进报告）。
- 007 的既有回放测试全部不改、仍通过（Step 3 的 7 个过滤）。

## Done criteria

- [ ] Step 1、Step 2 的测试在 Step 3 之前 FAIL（报告里附失败信息）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok；`./pkg/architecture` ok
- [ ] `go test -tags fts5 ./pkg/tui -run 'TestReplayDeliveredReviewKeepsTheRevisedTurnBelowIt|TestReplaySealsTheResponseThatIssuedACardlessGate' -count=1 -v` 显示 2 个 PASS
- [ ] `grep -n 'planText < 0 &&' pkg/tui/commands_test.go` 无输出
- [ ] `grep -n 'func heldGateCallsByRow\|func replayHeldGateStepMessage' pkg/tui/commands.go` 各 1 行
- [ ] `go vet ./pkg/tui`、`gofmt -l pkg cmd` 无输出；`git diff --stat 9656051 -- pkg/architecture/testdata/graph.json` 无输出
- [ ] `git status --short` 仍是 Drift check 列出的 11 行
- [ ] Step 4 的三条 Verify 全部满足
- [ ] Step 6 C'/E' 的断言满足（或按 Step 6 的说明如实报告跳过原因）

## STOP conditions

1. Step 1 或 Step 2 的测试在改代码**之前**就通过——说明回放行为与本计划推演不同，报告实际块序列（在测试里临时 `t.Logf` 打印 `renderer.vm.blocks` 的 Kind/Final/Content，报告后删掉日志）。
2. Step 3 之后 007 的 7 个回放测试中任何一个失败，或 `./pkg/tui` 全量出现新的失败——报告失败用例与输出，不要改那些测试。
3. driver 无法 build/start（缺 tmux 等）——报告原文，跳过 Step 6，其余照常完成。
4. 修复需要改 Out of scope 中的任何文件，或需要增删 import。
5. Drift check 的 `git status --short` 与预期的 11 行不一致，或 Current state 摘录对不上。

## Maintenance notes

- **封存是 live 规则在回放侧的镜像**：live 由 gate 的 running 步骤在 `Reducer.reduceMessage` 里封存；回放没有这条步骤的行，
  由 `heldGateCallsByRow` + `replayHeldGateStepMessage` 补喂同一条步骤。将来若 `tool.ToolStepHoldsNoCard` 覆盖更多工具，
  两边自动一起生效；若 exit gate 重新画等待卡，回放这里补喂的 running 步骤会画出 running 卡——那时要一并删掉这段。
- 选择"补喂 reducer 一条 running 步骤"而不是在 `flush` 里直接 `flushBufferedText`：前者让回放走 live 同一入口，
  封存时机、agent 缓冲区选择、reasoning 时长都与 live 一致，不在回放侧复制规则。
- 补喂发生在发出调用的行**之后**、`flush(i+1)` 之前，所以锚在 `callRow+1` 的确认行 / review 卡总是落在已封存的回答之后。
- web 端（计划 008）是否有同类"交付前回答与修订回答粘连"的回放问题未核实，不在本计划；评审时可顺带在浏览器重载一次 review 交付流程确认。

## 验收记录（2026-10-10，评审侧）

- 执行者：Step 1–5 完成；Step 1/2 修复前失败信息分别为 `a replayed response was left unsealed: … Content:"here is the plan" … Final:false` 与
  `replayed responses: ["first answersecond answer"] want: ["first answer" "second answer"]`；修复后通过。
- 评审复跑：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1` → 两包 ok；007 的 8 个新测试/改写测试 + 009 的 1 个新测试 + 3 个既有回放测试，共 12 个 `--- PASS`；
  `go vet ./pkg/tui`、`gofmt -l pkg cmd`、`git diff --stat 9656051 -- pkg/architecture/testdata/graph.json` 无输出；`grep 'planText < 0 &&'` 无输出；`git status --short` 为 Drift check 的 11 行。
- diff 与 Step 3 的代码逐行一致；勘误段已移到第一个引用块之后（`git diff 9656051` 只剩新增的 4 行勘误 + 1 个空行）。
- 真机 C'（执行者）：`Exited plan mode` 0、`◆ Canceled` 0、`calling exit_plan_mode` 1 次，`Entered plan mode`(21) < `calling exit_plan_mode`(24) < `You canceled`(28)。
- 真机 E'（评审补跑，执行者把 driver 里预期中的 review 失败误判为偏离而跳过）：live 与重启回放的顺序都是
  `● calling exit_plan_mode` → `✔ You asked deepseek / deepseek-chat to review the plan` → `● Failed to start 1 plan-reviewer task` →
  `✔ You approved forebrain to exit plan mode` → `◆ Exited plan mode`；回放里 `calling exit_plan_mode` 只出现 1 次、`◆ Exited plan mode` 1 张、`◆ Canceled` 0。
  （恢复会话时 driver 的 `start` 报了"没等到 `/ commands`"的超时：恢复后 composer 上方显示的是 "resumed N messages above"，driver 等的提示文案不出现；`tui.err` 为空，属 driver 的等待条件问题，不影响结论。）
- 未覆盖：review **交付成功**的真机路径（driver 的 toolcall 夹具没有计划文件，review 必然失败），交 owner 用真实模型复核。

## 真机验收：glm-5.3 跑通 review 交付成功路径（2026-10-10，主树）

- 环境：主树（9656051 + 已并回的 007/009 + owner 未提交改动）编译的二进制；隔离 FOREBRAIN_HOME（精简配置：唯一 provider `zhipuai/glm-5.3`，reasoning `xhigh`，`enable_subagent: true`），
  key 由启动脚本从 `~/.forebrain/.env` 注入、不落盘；tmux 独立会话，测试项目 `calc.py`。
- 流程：enter_plan_mode（批准）→ 模型写计划 → exit gate → 选 "4. Ask another model to review this plan" → `zhipuai / glm-5.3` →
  `✓ Plan review · 50s` → **自动交付** → planner 核实评审、改计划文件、再次 exit_plan_mode → 选 "2. Yes, proceed" → 实现 → `Worked for 2m 36s`。
- live 结果：review 运行期间轮询屏幕，`Exited plan mode` 最大计数 0；交付时无 "Kept planning" 卡、无 "Plan review delivered" 行；整段只有批准后那一张 `◆ Exited plan mode`。
- 回放（`forebrain resume <sid>` 重启）与 live 的关键行**逐行相同**（diff 为空）：

```
● 计划已成功写入 add-subtract-multiply.md。现在请求批准：
✔ You asked zhipuai / glm-5.3 to review the plan
● Ran 1 plan-reviewer task
  └ ✓ Plan review
● 评审结论为批准、无需修改。我快速核实评审提到的一个新事实（README.md 内容），再把评审结果记入计划文件后重新提交：
● Read README.md lines 1-3
● README 确实只是两行描述，不含函数清单，评审说法成立。将评审结果补记进计划文件：
● Edited
● 计划文件已更新。重新提交请求批准：
✔ You approved forebrain to exit plan mode
◆ Exited plan mode
● 计划已获批准。开始实施：编辑 calc.py。
...
─ Worked for 2m 36s · 14:03
```

- 计数（live / 回放相同）：第一段回答 1 次（未与修订回答粘连）、`◆ Exited plan mode` 1、`Kept planning` 0、`Plan review delivered` 0、`◆ Canceled` 0。
- 状态库 `approval_resolved`：`review_requested`（exit 调用 A，带确认行）→ `denied`（交付，无确认行、无 step）→ `approved`（exit 调用 B）。
- 附带发现（既有、不在本计划）：home 位于符号链接路径（`$TMPDIR` = `/var/folders/…` → `/private/var/…`）时，`isSanctionedPlanWrite`（`pkg/tool/state.go:916`）
  只做 `filepath.Clean` 的字面比较，模型用 `/private/var/…` 写法写计划文件会被拦；owner 的真实 `~/.forebrain` 不在符号链接下，不受影响。
