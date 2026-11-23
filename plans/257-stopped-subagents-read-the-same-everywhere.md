# Plan 257: 停止的子代理（已取消 / 已中断）在终端与网页上说法一致

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 236fc32a..HEAD -- pkg/tui/reducer.go pkg/tui/tty_session.go pkg/tui/chat_session.go pkg/tui/reducer_test.go pkg/tui/tty_session_test.go pkg/tui/chat_session_test.go frontend/src/composables/useChatStream.ts frontend/src/composables/useChatStream.test.ts frontend/src/components/chat/SubagentCallCard.vue frontend/src/components/chat/SubagentCallCard.test.ts frontend/src/locales/index.ts`
> → 必须为空。`pkg/tui` 常有并行会话在改：不为空就把下面「Current state」里的摘录和实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW（只改呈现：图标、颜色、计数、收尾行；子代理的运行、状态落库、事件载荷都不变）
- **Depends on**: none（plan 244 已落地：网页的 `stopped` 块带 `status`/`reason`）
- **Category**: bug（一致性）
- **Planned at**: commit `236fc32a`, 2026-11-06
- **Finding**: 2026-11-06 分支审计（238–245 实现细节），发现 5（`introduced` by plan 244）
- **Owner 裁决（2026-11-06）**：
  1. interrupted 是独立的一档结果，两端的行都用**灰色**停止图标；卡头计数新增 interrupted 一档；TUI 不再把它折成 failed。
  2. **TUI 的两种停止（cancelled 与 interrupted）都改为灰色**，子代理视图的收尾也不再是红色错误帧，与网页完全一致。
  3. 本计划**免原型**（网页只在卡头计数里加一个词，沿用现有样式，布局不变）。

## Why this matters

子代理有两种「停止」结局：

- `cancelled`：人按了停止；
- `interrupted`：审批等待恢复时发现工具已开始执行、结果不确定，于是不再重试（引擎原句：`approval continuation was interrupted after execution began; tool outcome is uncertain and was not retried`）。

plan 244 让网页正确区分了这两种：卡片行用灰色 `Ban` 图标，不画红；抽屉里的收尾行写「已取消」或「已中断 + 原因」。
但留下三个问题：

1. **网页卡头把 interrupted 藏起来了**。卡头计数只数 done、failed、cancelled、skipped 四档，interrupted 两头都不进。
   比如两行分别是 done 和 interrupted，计数是 `{done:1}`，不出明细，卡头读成「全部完成」。
   244 之前 interrupted 被折成 failed，至少还写「1 个失败」。
2. **终端（TUI，本仓库的标准界面）还在旧口径上**：
   - interrupted 一律折成 failed，红色 ✗，计数写「1 failed」，子代理视图收尾是红色的「subagent failed」；
   - cancelled 是红色 ×，收尾是红色的「subagent cancelled」错误帧；
   - 两种停止的卡片行下面都会再印一行原因。

   网页的做法正好相反：灰色，卡片行不印原因，原因只在子代理自己的视图里出现一次。
3. **TUI 子代理视图里中断原因说了两遍**。`interruptUncertainTUIApproval` 往该视图另发了一条红色错误，而收尾帧已经带着同一原因。

仓库规则是「TUI 为准、两端语义一致，差异必须写进计划修成一样」。本计划把两端统一到 owner 裁定的口径。

## Current state

### TUI（`pkg/tui`）

- `reducer.go:651-658`：终态判据，不含 interrupted：

```go
func fanoutTaskTerminal(status string) bool {
	switch status {
	case "done", "failed", "cancelled", "skipped":
		return true
	default:
		return false
	}
}
```

  全文件 9 处调用（`grep -n 'fanoutTaskTerminal(' pkg/tui/reducer.go`）。

- `reducer.go:1641-1652`：结束事件把 interrupted 折成 failed：

```go
	case SubagentEndedMsg:
		status := strings.TrimSpace(m.Status)
		if status == "" {
			status = "done"
		}
		endStatus := status
		if endStatus != "cancelled" {
			if endStatus == "failed" || strings.TrimSpace(m.Error) != "" {
				endStatus = "failed"
			} else {
				endStatus = "done"
			}
		}
```

- `reducer.go:1142-1170`：卡头计数 `fanoutOutcomeBreakdown`，四档：done、failed、cancelled、skipped。`:1132-1133` 把结果拼成 `head += " · " + breakdown`，形如 `Ran 2 general-purpose tasks · 1 done, 1 skipped`。

- `reducer.go:2408-2417`：行图标：

```go
		switch t.Status {
		case "done":
			head.WriteString("\x1b[32m✓\x1b[0m ")
		case "failed":
			head.WriteString("\x1b[31m✗\x1b[0m ")
		case "cancelled":
			head.WriteString("\x1b[31m×\x1b[0m ")
		case "skipped":
			head.WriteString("\x1b[33m!\x1b[0m ")
		}
```

  仓库里灰色的写法是 `\x1b[90m`（如 `reducer.go:5266`）。

- `reducer.go:2430-2433`：任何终态行都印原因：

```go
		if t.Error != "" && fanoutTaskTerminal(t.Status) {
			add("  ", t.Error)
		}
```

- `reducer.go:2192-2222`：子代理视图的收尾帧 `subagentClosingFrames`，cancelled 与 failed 都是 `FrameError`（红色）：

```go
func (r *Reducer) subagentClosingFrames(agentID string, m SubagentEndedMsg) []Frame {
	agentID = strings.TrimSpace(agentID)
	errText := strings.TrimSpace(m.Error)
	status := strings.ToLower(strings.TrimSpace(m.Status))
	if agentID == "" || (errText == "" && status != "failed" && status != "cancelled") {
		return nil
	}
	title := "subagent failed"
	if status == "cancelled" {
		title = "subagent cancelled"
	}
	content := errText
	if content == "" {
		content = "The subagent stopped before it produced an answer."
	}
	return []Frame{{
		Kind:    FrameError,
		Title:   title,
		Content: content,
		RunID:   r.activeRunID,
		AgentID: agentID,
		Final:   true,
	}}
}
```

- 灰色状态行的现成形态是 `FrameStatus`。`subagentWorkedFrames`（`reducer.go:2230-2263`）用它画「Worked for …」。
  渲染器 `render.go:5132-5160` 的 `renderStatus` 把 `Title + " " + Content` 用颜色 244（灰）画成一行，可换行。

- `tty_session.go:1227-1240`：全局错误镜像进当前子代理视图前的去重。它会**跳过所有状态帧**，只认 `FrameError`：

```go
func (m *viewModel) endsOnError(content string) bool {
	content = strings.TrimSpace(content)
	for i := len(m.blocks) - 1; i >= 0; i-- {
		f := m.blocks[i].frame
		if f.Kind == FrameStatus {
			continue
		}
		return f.Kind == FrameError && content != "" && strings.TrimSpace(f.Content) == content
	}
	return false
}
```

  调用点 `reducer.go:5777`。收尾帧一旦改成状态帧，紧随其后镜像进来的同句 turn_error 就不再被认作重复，所以去重必须同时认「带同一原因的停止状态行」。

- `chat_session.go:1135-1165`：`interruptUncertainTUIApproval` 依次做三件事：
  1. 发 `turn_error`，对话里显示原因；
  2. 发 `SubagentEnded{Status:"interrupted", Error: reason}`，收尾帧在子代理视图里显示原因；
  3. **再**往子代理视图 `notifyUI` 一条 `MsgKindError`（`:1158-1164`），同一原因第二次出现：

```go
	// Publishing the turn error drew it in the conversation already. A
	// subagent's own view is a separate screen the event does not reach, so
	// that view is told here; the conversation is not told twice.
	if strings.TrimSpace(wait.AgentID) != "" {
		s.notifyUI(NewMessageMsg{Msg: Message{Kind: MsgKindError, Content: reason, AgentID: wait.AgentID, RunID: wait.RunID, Timestamp: time.Now()}})
	}
```

- 测试范本：
  - `reducer_test.go:1922` 的 `TestFanoutCardTakesSkippedOutcomesFromTheCallFacts`：起卡 → spawn → end → 完成事件，断言 `card.Summary`；
  - `reducer_test.go:2405-2440`：cancelled 行，`fanoutContentLines` 去掉 ANSI 后断言前缀 `×`；
  - `chat_session_test.go:29236` 的 `TestTurnErrorRepeatingSubagentFailureIsNotMirroredTwice`：渲染器加 reducer，断言子代理视图里一句话只出现一次。

### 网页（`frontend/src`）

- `composables/useChatStream.ts:2510-2512`：`subagentTaskTerminal` 不含 interrupted。244 为此在 `:2552-2554` 另起了局部判据 `settledEnding`，并在 `:2578` 单独排除 interrupted：

```ts
function subagentTaskTerminal(status: string): boolean {
  return status === 'done' || status === 'failed' || status === 'cancelled' || status === 'skipped'
}
…
    const settledEnding = subagentTaskTerminal(status) || status === 'interrupted'
…
      ...(status !== 'waiting' && subagentTaskTerminal(status) && status !== 'cancelled' && status !== 'interrupted'
        && String(bound?.error ?? facts.error ?? '').trim()
```

  同一判据还用在 `:3079`。晚到的绑定消息会把已结束的行改回 `running`，而 interrupted 不算终态，所以**会被改回 running**。这是本计划顺带修掉的隐患。`:5370` 处同理。

- `useChatStream.ts:2501` 的类型与 `:2634-2643` 的计数：

```ts
  breakdown?: { done: number; failed: number; cancelled: number; skipped: number }
…
  if (phase === 'done' && !query) {
    const tally = { done: 0, failed: 0, cancelled: 0, skipped: 0 }
    for (const row of rows) {
      if (row.status === 'done' || row.status === 'failed' || row.status === 'cancelled' || row.status === 'skipped') {
        tally[row.status] += 1
      }
    }
    if (tally.failed + tally.cancelled + tally.skipped > 0) breakdown = tally
  }
```

- `components/chat/SubagentCallCard.vue:139-146`：计数渲染：

```ts
  if (v.breakdown) {
    const join = t('subagentCard.breakdownJoin')
    const tally: string[] = [t('subagentCard.breakdownDone', { count: v.breakdown.done })]
    if (v.breakdown.failed > 0) tally.push(t('subagentCard.breakdownFailed', { count: v.breakdown.failed }))
    if (v.breakdown.cancelled > 0) tally.push(t('subagentCard.breakdownCancelled', { count: v.breakdown.cancelled }))
    if (v.breakdown.skipped > 0) tally.push(t('subagentCard.breakdownSkipped', { count: v.breakdown.skipped }))
    parts.push(tally.join(join))
  }
```

- `locales/index.ts`：zh `:1835-1839`（`'{count} 个取消'` 等），en `:4044-4048`（`'{count} cancelled'` 等）。
- 网页卡片行图标已经是灰色 `Ban`，同时覆盖 cancelled 与 interrupted（`SubagentCallCard.vue:36`）。抽屉收尾行 `RunStoppedLine` 已按状态选词并显示原因。**这两处不改**。
- 测试范本：`useChatStream.test.ts:5091` 的 `counts every outcome of a settled fanout, a skipped task included`（纯函数 `subagentCallView`）；`SubagentCallCard.test.ts`（`mount` 加 `setLocale`）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go build | `go build ./...` | exit 0 |
| Go vet | `go vet ./...` | exit 0 |
| TUI focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Stopped|Interrupted|Fanout|Mirrored|EndsOn' -count=1` | `ok` |
| TUI package | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok` |
| House rule | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| Web typecheck | `cd frontend && pnpm typecheck` | exit 0 |
| Web tests | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/components/chat/SubagentCallCard.test.ts src/components/chat/SubagentConversation.test.ts` | all pass |
| Web build | `cd frontend && pnpm build` | exit 0（重生成 `pkg/gateway/dist`） |

`-run` 是 Go 正则，用 `|`，**不要**写 `\|`。

## Scope

**In scope**:
- `pkg/tui/reducer.go`：终态判据、结束事件、计数、行图标、行下原因、收尾帧
- `pkg/tui/tty_session.go`：`endsOnError`
- `pkg/tui/chat_session.go`：删掉 `interruptUncertainTUIApproval` 末尾那条重复的 `notifyUI`
- `pkg/tui/reducer_test.go`、`pkg/tui/tty_session_test.go`、`pkg/tui/chat_session_test.go`
- `frontend/src/composables/useChatStream.ts`、`frontend/src/components/chat/SubagentCallCard.vue`、`frontend/src/locales/index.ts`
- `frontend/src/composables/useChatStream.test.ts`、`frontend/src/components/chat/SubagentCallCard.test.ts`
- `pkg/gateway/dist/**`（`pnpm build` 的产物）

**Out of scope**:
- 事件载荷与引擎：`SubagentEndedPayload`、`pkg/gateway/api_extra.go` 里的 interrupted 发布，以及原因文案。
- 对话主视图里的 `turn_error`（两端都显示，不动）。
- 网页 `RunStoppedLine.vue`、卡片行图标（244 已经是目标形态）。
- TUI 的 roster、`/agents` 列表、plan review 卡（`PlanReviewReviewedMsg` 分支只读 `fanoutTaskTerminal`，判据扩充后行为自然一致，不另改）。
- failed 与 skipped 的任何呈现。

## Steps

### Step 1: TUI——interrupted 成为终态，结束事件不再折叠

1. `fanoutTaskTerminal` 的 case 加上 `"interrupted"`，注释改成说明五种终态。
2. `SubagentEndedMsg` 分支改成：

```go
		endStatus := status
		switch endStatus {
		case "cancelled", "interrupted":
			// The two stops keep their own names: neither is a failure, and the
			// card, the tally and the agent's own view all say which it was.
		default:
			if endStatus == "failed" || strings.TrimSpace(m.Error) != "" {
				endStatus = "failed"
			} else {
				endStatus = "done"
			}
		}
```

3. `reducer.go:293` 那行 `Status` 字段注释里的取值列表加上 `"interrupted"`。

**Verify**: `go build ./...` → exit 0

### Step 2: TUI——计数、图标、行下原因

1. `fanoutOutcomeBreakdown` 加 `interrupted` 计数。触发明细的条件改为 `failed+cancelled+interrupted+skipped == 0` 时返回空。拼接顺序：done、failed、cancelled、interrupted、skipped，interrupted 写 `fmt.Sprintf("%d interrupted", interrupted)`。
2. 行图标：

```go
		case "cancelled", "interrupted":
			// A stop is the quiet ending, never the red one a failure gets — the
			// same muted mark the web card draws for both.
			head.WriteString("\x1b[90m×\x1b[0m ")
```

   删掉原来单独的 `case "cancelled":`。
3. 行下原因只给非停止的终态：

```go
		// A stop's reason is said once, in the agent's own view (its closing
		// line); the card row only marks that it stopped, as the web card does.
		if t.Error != "" && fanoutTaskTerminal(t.Status) && t.Status != "cancelled" && t.Status != "interrupted" {
			add("  ", t.Error)
		}
```

**Verify**: `go build ./...` → exit 0

### Step 3: TUI——子代理视图的收尾行改为灰色状态行

把 `subagentClosingFrames` 改成：停止走灰色状态行，失败保持原样。

```go
func (r *Reducer) subagentClosingFrames(agentID string, m SubagentEndedMsg) []Frame {
	agentID = strings.TrimSpace(agentID)
	errText := strings.TrimSpace(m.Error)
	status := strings.ToLower(strings.TrimSpace(m.Status))
	if agentID == "" {
		return nil
	}
	// A stop closes the view with the quiet line the web drawer draws
	// (RunStoppedLine): its word, and the reason beside it when there is one.
	// It is a status, not an error — nothing went wrong that the user must fix.
	if status == "cancelled" || status == "interrupted" {
		title := "Cancelled"
		if status == "interrupted" {
			title = "Interrupted"
		}
		if errText != "" {
			title += " ·"
		}
		return []Frame{{
			Kind:    FrameStatus,
			Title:   title,
			Content: errText,
			RunID:   r.activeRunID,
			AgentID: agentID,
			Final:   true,
		}}
	}
	if errText == "" && status != "failed" {
		return nil
	}
	content := errText
	if content == "" {
		content = "The subagent stopped before it produced an answer."
	}
	return []Frame{{
		Kind:    FrameError,
		Title:   "subagent failed",
		Content: content,
		RunID:   r.activeRunID,
		AgentID: agentID,
		Final:   true,
	}}
}
```

注意 cancelled 现在**没有原因时也出一行**「Cancelled」。原来没原因的 cancelled 也会出红色帧，内容是「The subagent stopped before it produced an answer.」，所以这里是「换样式」，不是「新增一行」。

**Verify**: `go build ./...` → exit 0

### Step 4: TUI——去重认得停止状态行；删掉重复的那条错误

1. `tty_session.go` 的 `endsOnError`：状态帧如果带着**同一句**内容，就算已经说过：

```go
		if f.Kind == FrameStatus {
			// A stop's closing line carries its reason as content (plans/257):
			// that is the view already saying the sentence. Every other status
			// ("Worked for …", an auto-continue notice) annotates the ending and
			// is looked through.
			if content != "" && strings.TrimSpace(f.Content) == content {
				return true
			}
			continue
		}
```

   函数注释里补一句「or a stop's closing status saying it」。

2. `chat_session.go`：删掉 `interruptUncertainTUIApproval` 末尾 `:1158-1164` 的注释和 `if strings.TrimSpace(wait.AgentID) != "" { s.notifyUI(…MsgKindError…) }` 整块。子代理视图的那句原因由 `SubagentEnded` 的收尾行给出，它带 `AgentID`，必然落进该视图。

   删除前先确认 `SubagentEnded` 事件确实经 `publishRunEvent` 发布（`:1152-1155`），并经 `commands.go` 的 `RunEventSubagentEnded` 分支转成 `SubagentEndedMsg`（`commands.go:1374-1379`）。

**Verify**: `go build ./...` → exit 0；`grep -n 'MsgKindError, Content: reason, AgentID: wait.AgentID' pkg/tui/chat_session.go` → 无输出

### Step 5: TUI 测试

1. `reducer_test.go`：`TestFanoutCardCountsInterruptedAsItsOwnOutcome`。仿 `TestFanoutCardTakesSkippedOutcomesFromTheCallFacts`：
   - 两个任务，一个 `Status:"ok"` 结束，另一个 `Status:"interrupted", Error: reason` 结束；
   - 断言 `card.Summary` 以 ` · 1 done, 1 interrupted` 结尾；
   - interrupted 行去 ANSI 后以 `×` 开头；
   - 原始 `card.Content` 里该行含 `\x1b[90m×`；
   - 卡片内容**不含** reason 文本。
2. `reducer_test.go`：`TestStoppedSubagentClosesItsViewQuietly`。对 cancelled（无 Error）和 interrupted（有 Error）各调一次 `r.Reduce(SubagentEndedMsg{…})`，在返回的帧里找 `AgentID` 等于该代理、`Final` 为真的那一帧：
   - `Kind == FrameStatus`；
   - interrupted 的 `Title == "Interrupted ·"`、`Content == reason`；
   - cancelled 的 `Title == "Cancelled"`、`Content == ""`；
   - 两者都**没有** `FrameError`。
3. `reducer_test.go`：cancelled 行同样是灰色 ×。在 `reducer_test.go:2440` 附近那个用例的现有断言旁，补断言原始内容含 `\x1b[90m×`。只**新增**断言，不改原有断言。
4. `tty_session_test.go`：`TestEndsOnErrorRecognisesAStopsClosingLine`。分别构造 `viewModel`：
   - 末尾是 `FrameStatus{Title:"Interrupted ·", Content: s}` → `endsOnError(s)` 为真；
   - 末尾是 `FrameStatus{Title:"Worked for 3s"}` → `endsOnError(s)` 为假；
   - `FrameError{Content:s}` 之后接 `FrameStatus{Title:"Worked for 3s"}` → 仍为真（原行为）。

   `viewModel` 的构造照该文件现有用例写。
5. `chat_session_test.go`：`TestInterruptedSubagentViewSaysItsReasonOnce`。仿 `TestTurnErrorRepeatingSubagentFailureIsNotMirroredTwice`：
   - 渲染器加 reducer，spawn 后 `SetActiveView(rosterKey)`；
   - 喂 `SubagentEndedMsg{Status:"interrupted", Error: reason}`，再喂一条无 AgentID 的 `MsgKindError{Content: reason}`（模拟 turn_error 的镜像）；
   - 断言子代理视图 `strings.Count(view, reason) == 1`、含 `Interrupted`、**不含** `subagent failed`；
   - 主视图含 reason。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Interrupted|Stopped|EndsOnError|Fanout|Mirrored' -count=1 -v` → 新测试全部 PASS

**负对照（必须做，做完复原）**：
- 临时把 step 1 的 switch 退回旧的 `if endStatus != "cancelled"`，用例 1 应红（Summary 是 `1 failed`）；
- 临时撤掉 step 4.1 的状态帧判断，用例 5 应红（reason 出现 2 次）。

观察后复原。

### Step 6: 网页——interrupted 进终态判据，卡头计数加一档

1. `subagentTaskTerminal` 加上 `|| status === 'interrupted'`，并在上方注释写明「five endings: done, failed, cancelled, interrupted, skipped」。
2. 删掉 244 的局部判据 `settledEnding`（`:2552-2554` 那三行注释加 const），把它唯一的使用处换回 `subagentTaskTerminal(status)`。现在两者等价。
3. `:2578` 的错误抑制条件保持语义不变：`subagentTaskTerminal(status) && status !== 'cancelled' && status !== 'interrupted'`，不需要改字面。
4. 类型与计数：

```ts
  breakdown?: { done: number; failed: number; cancelled: number; interrupted: number; skipped: number }
…
    const tally = { done: 0, failed: 0, cancelled: 0, interrupted: 0, skipped: 0 }
    for (const row of rows) {
      if (row.status === 'done' || row.status === 'failed' || row.status === 'cancelled' || row.status === 'interrupted' || row.status === 'skipped') {
        tally[row.status] += 1
      }
    }
    if (tally.failed + tally.cancelled + tally.interrupted + tally.skipped > 0) breakdown = tally
```

5. `SubagentCallCard.vue`：在 cancelled 那行之后加

```ts
    if (v.breakdown.interrupted > 0) tally.push(t('subagentCard.breakdownInterrupted', { count: v.breakdown.interrupted }))
```

6. `locales/index.ts`：zh 在 `'subagentCard.breakdownCancelled'` 之后加 `'subagentCard.breakdownInterrupted': '{count} 个中断',`；en 同位置加 `'subagentCard.breakdownInterrupted': '{count} interrupted',`。

**Verify**: `cd frontend && pnpm typecheck` → exit 0

### Step 7: 网页测试

1. `useChatStream.test.ts`，紧挨 `counts every outcome of a settled fanout`：`counts an interrupted task as its own outcome`。两任务：`done` 与 `interrupted`，`status:'completed'`；断言 `view.breakdown` 等于 `{ done: 1, failed: 0, cancelled: 0, interrupted: 1, skipped: 0 }`，`view.rows[1].error` 为 `undefined`。
2. 现有用例 `counts every outcome of a settled fanout, a skipped task included` 的期望对象要补 `interrupted: 0`。这是因为类型多了字段，**只**补这一个键。其他断言里凡是 `toEqual` 整个 breakdown 的，同法只补 `interrupted: 0`。
3. 同文件：`keeps an interrupted row settled when a late bind arrives`。先让一行经实时事件结束为 interrupted，再喂一条同 `index` 的绑定或 spawn 消息，也就是触发 `:3079` 那段的消息。断言该行状态仍是 `interrupted`。这一步要先找到现有用例里怎样触发 `:3079`（搜索调用它的函数名），照着写。找不到可复用的触发方式时，在报告里写明并跳过本条，不要为测试改生产代码。
4. `SubagentCallCard.test.ts`：中英两种 locale 下，`[done, interrupted]` 两任务的卡头文字分别含 `1 个完成、1 个中断` 与 `1 done, 1 interrupted`。

**Verify**: `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/components/chat/SubagentCallCard.test.ts src/components/chat/SubagentConversation.test.ts` → all pass

**负对照**：临时去掉 step 6.4 计数里的 `interrupted`，用例 1 应红；观察后复原。

### Step 8: 构建与回归

**Verify**：
- `cd frontend && pnpm build` → exit 0
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `go build ./...`、`go vet ./...` → exit 0
- 有条件时跑 `cd frontend && pnpm e2e -- subagent-conversation.spec.ts`：`:228` 的 `Cancelled` 断言应仍 PASS。没跑就在报告里写明「e2e 未跑」。

## Test plan

- TUI 新增 5 组断言：
  1. 计数 + 图标 + 行下无原因；
  2. 收尾行两种停止；
  3. cancelled 灰色图标；
  4. `endsOnError` 三种情况；
  5. 端到端「原因只说一次」。

  其中用例 1 和用例 5 各带负对照。
- 网页新增 3 个用例：计数、晚到绑定不回退、卡头中英文。另外现有 breakdown 期望只补 `interrupted: 0`。
- 保持绿：
  - `pkg/tui` 全包。尤其 `TestTurnErrorRepeatingSubagentFailureIsNotMirroredTwice`（failed 路径不变）以及 `reducer_test.go:2405-2440` 的 cancelled 查询行用例；
  - 三个前端测试文件；
  - 244 的五个 interrupted 用例。删掉 `settledEnding` 后，行时钟用例 `settles an interrupted row’s clock on the duration it ran` 必须仍绿。

## Done criteria

ALL must hold:

- [ ] `go build ./...`、`go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `cd frontend && pnpm typecheck` exit 0；三个前端测试文件全部通过；`pnpm build` exit 0
- [ ] `grep -n '\\x1b\[31m×' pkg/tui/reducer.go` → 无输出（红色 × 已不存在；改动前它命中 `:2414`）
- [ ] `grep -n 'subagent cancelled' pkg/tui/reducer.go` → 无输出
- [ ] `grep -n 'settledEnding' frontend/src/composables/useChatStream.ts` → 无输出
- [ ] `grep -c 'breakdownInterrupted' frontend/src/locales/index.ts` → `2`
- [ ] 三个负对照已观察到红并复原
- [ ] `git status --porcelain` 只列出 In scope 的文件（含 `pkg/gateway/dist`），外加 `plans/README.md`
- [ ] `plans/README.md` 的 257 行已更新

## STOP conditions

- 某个现有 TUI 测试断言 cancelled 是红色、断言「subagent cancelled」错误帧、或断言 cancelled 行下印着原因：报告测试名和断言，不要改。owner 已裁定改灰色，但现有测试的改动要列出来给评审看。
- 删掉 `chat_session.go` 那条 `notifyUI` 后，`TestInterruptedSubagentViewSaysItsReasonOnce` 里子代理视图**完全没有**原因：说明 `SubagentEnded` 没有到达该视图。报告路由链，不要恢复那条 `notifyUI`。
- `pkg/tui/reducer.go` 在 drift check 时已被并行会话改动，且改动落在上面任一摘录处：STOP。
- 删掉 `settledEnding` 后 244 的行时钟用例变红：报告，不要恢复局部判据。这说明 `subagentTaskTerminal` 另有调用方依赖旧语义。

## Maintenance notes

- 口径：**停止不是失败**。cancelled 与 interrupted 在两端都是灰色 ×，或网页的灰色 `Ban`。卡片行只标「停了」，原因只在该代理自己的视图里说一次（TUI 的收尾状态行，网页的 `RunStoppedLine`）。卡头计数里各占一档。
- 终态判据是两端各一个函数：TUI 的 `fanoutTaskTerminal`，网页的 `subagentTaskTerminal`。以后新增结局时两个一起改，并同时改两端的计数与文案（TUI 英文、网页中英文）。
- `endsOnError` 现在把「`Content` 等于该句的状态帧」也算作说过。以后给别的状态帧塞 `Content` 时，留意它可能吞掉一条同句的全局错误镜像。这是有意的：同一句话在同一视图里只说一次。
