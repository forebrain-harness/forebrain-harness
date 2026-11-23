# Plan 139：子代理卡失败时只在任务行下说一次原因（终端 + 网页）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置 1（否则 STOP 1）——目标文件必须干净**：
> `git status --porcelain pkg/tui/reducer.go pkg/tui/reducer_test.go frontend/src/composables/useChatStream.ts frontend/src/composables/useChatStream.test.ts`
> 必须**无输出**。写计划时（`82ab168`）`reducer.go`、`useChatStream.ts`、`useChatStream.test.ts` 上挂着
> 135–138 批次未提交的改动（另一个会话正在主树执行）；那批改动由 owner 手动提交之后本计划才能开工，
> 否则你的改动会和别人的混在一起、评审分不开。
>
> **前置 2（否则 STOP 1）——锚点仍在**：
> `grep -n "FanoutCallError:  fs.CallError," pkg/tui/reducer.go` 恰好 1 条；
> `grep -n "const callError = !canceled" frontend/src/composables/useChatStream.ts` 恰好 1 条。
>
> **Drift check（先跑）**：`git diff --stat 82ab168 -- pkg/tui/reducer.go pkg/tui/render.go frontend/src/composables/useChatStream.ts frontend/src/components/chat/SubagentCallCard.vue`
> 有输出是预期的（135–138 批次提交后会改这些文件的别处）；按下文"现状"里的**函数名**逐段核对，
> 被引用的那几段代码含义变了才算 STOP 1。
>
> **基线（Step 1 之前各跑一次，记下失败清单；否则 STOP 5）**：
> - `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`（约 70 s，超时给足 300 s）
> - `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/components/chat/SubagentCallCard.test.ts`
>
> 基线里已经红的用例是存量问题，**不是你要修的**；Done criteria 只要求"不新增红"。

## Status

- **Priority**: P1（owner 2026-10-26 指定）
- **Effort**: S
- **Risk**: LOW（只改两处"画不画"的判断，不动状态机、不动引擎）
- **Depends on**: 135–138 批次在 `reducer.go` / `useChatStream.ts` 上的改动已被 owner 提交（见前置 1）
- **Blocks**: `plans/140-dispatcher-owns-its-failed-subagent.md`（140 会把调用级错误换成一段写给主代理的多行说明，
  必须先有本计划把它从卡片上收掉）
- **Category**: bug（UI 重复信息）
- **Planned at**: dev `82ab168`，2026-10-26
- **原型**：owner 2026-10-26 裁定**免原型**（只删一行重复文字、不加新元素；网页规则与终端一致）

## Why this matters

`subagent_run` 派发的子代理中途失败时，终端（以及网页）的子代理卡把同一个失败说了两遍：

```
● Failed to run 1 general-purpose task
    tool subagent_run: error, status code: 429, status: 429 Too Many Requests, message: {"error":{"code":"1310","message":"您已达到每周/每月使用上…
  └ ✗ 执行计划 137 shell 正文 · 12m34s
      您已达到每周/每月使用上限，您的限额将在 2026-11-01 06:09:05 重置。 (HTTP 429 Too Many Requests)
      └ read /Users/…/frontend/src/composables/useChatStream.ts
      … +126 tool uses
```

第二行是**调用级错误**（工具调用自己的失败文本，原样的 HTTP 报错，又长又带 JSON）；任务行下面那行是
**这个子代理执行自己的失败原因**（引擎已经把它"描述"过：服务商的话 + HTTP 状态）。对派发型卡片来说，
调用之所以失败就是因为它派出去的任务失败了，两行说的是同一件事。owner 的要求：卡头下那行删掉，留任务行下那行。

## Current state

### 终端：卡片的数据从哪来

- `pkg/tui/reducer.go` —— 子代理卡的状态机。
  - `fanoutState.CallError`（`:320-321`）："the call's own failure, shown once under the header"。
  - `applySubagentCallCompletion`（约 `:758-768`）在工具调用完成且状态为 failed 时写它：
    ```go
    switch {
    case strings.EqualFold(status, "canceled"):
        fs.CallStatus = "canceled"
    case strings.EqualFold(status, "failed"):
        fs.CallError = normalizeFailedToolContent(strings.TrimSpace(msg.Content))
    }
    ```
  - 任务行的失败原因是 `FanoutTaskState.Error`，来自子代理生命周期事件 `SubagentEndedMsg.Error`（引擎侧
    `finishSubagent` 用 `llm.ExplainError(runErr)` 写成），由 `renderFanoutContent`（约 `:2316-2320`）画在任务行下：
    ```go
    // Reason line. The indent is repeated on every sentence so the whole
    // reason reads as one continuation of the task above it.
    if t.Error != "" && fanoutTaskTerminal(t.Status) {
        add("  ", t.Error)
    }
    ```
  - `emitFanoutFrame`（约 `:862-886`）把状态变成帧，**这里是本计划要改的地方**：
    ```go
    return Frame{
        Kind:             FrameFanout,
        ...
        Summary:          fanoutCardHeader(fs),
        Duration:         duration,
        FanoutQuery:      fs.Query,
        FanoutCallError:  fs.CallError,
    }
    ```
  - `fanoutCardPhase`（紧跟其后，约 `:900-925`）用 `fs.CallError != ""` 决定卡头是 "Failed to run …"：
    ```go
    if fs.CallStatus == "canceled" {
        return "canceled"
    }
    if fs.CallError != "" {
        return "failed"
    }
    ```
    **卡头必须保持 "Failed to run 1 general-purpose task"**，所以 `fs.CallError` 本身不能清空，只能让帧上的
    `FanoutCallError` 在重复时为空。
- `pkg/tui/render.go` `renderFanout`（约 `:4540-4548`）把 `f.FanoutCallError` 逐行画在卡头下、所有任务行之上：
  ```go
  // The call's own failure sits under the header, above every task row: it
  // belongs to no task, and a copy on each row would say it N times.
  callErr := strings.TrimSpace(f.FanoutCallError)
  if callErr != "" {
      for _, line := range strings.Split(callErr, "\n") {
          _, _ = fmt.Fprintln(r.out, "    "+strings.TrimRight(line, "\r"))
          owner(-1, 1)
      }
  }
  ```
  **`render.go` 不用改**——帧上的字段为空它就不画。

### 必须继续显示调用级错误的情形（已有测试钉住，改完必须仍然绿）

调用级错误只有在"任务行没有自己的原因"时才是唯一的说明，这些情形都必须原样保留：

| 测试（`pkg/tui`） | 情形 | 任务行 |
|---|---|---|
| `TestReducerFanoutFailedDispatchSettlesWaitingTasksBothOrders`（`reducer_test.go` 约 `:684`） | 派发本身失败（`unknown tool`），子代理从没启动 | 无原因 |
| `TestFanoutCardShowsWhyTheCallFailed`（约 `:1873`） | fanout 容量为 0，一个任务都没派 | 无原因 |
| `TestNestedDispatchMirroredThroughRunEventsKeepsItsCard`（约 `:2076`） | 事实里任务 `Status: "failed"` 但没有 `Error` | 无原因 |
| `TestPlanReviewCardUsesTheDispatchVerbs`（约 `:2344`，后半段 r2） | 计划审阅从没启动 | 无原因 |
| `TestPlanReviewFailureKeepsTheApprovalAndReportsIt`（`chat_session_test.go` 约 `:13223`，断言 `card.FanoutCallError` 含 `upstream 429`） | 同上 | 无原因 |

查询型卡片（`subagent_status` / `wait` / `close` / `list`，`fs.Query == true`）的行是快照，行上的 failed
说的是"那个任务曾经失败"，与这次查询调用自己的失败无关——**查询卡一律保留调用级错误**。

### 网页：同一套规则的 TS 版

- `frontend/src/composables/useChatStream.ts` `subagentCallView`（约 `:2313-2440`）是网页子代理卡的唯一推导，
  注释写明"The semantics are the terminal's"。相关片段：
  ```ts
  const status = String(step.status ?? '').trim().toLowerCase()
  const canceled = status.startsWith('cancel')
  const callError = !canceled && (status === 'failed' || status === 'error') && String(step.error ?? '').trim()
    ? String(step.error).trim()
    : undefined
  ...
  let phase: 'starting' | 'running' | 'done' | 'failed' | 'canceled'
  if (canceled) phase = 'canceled'
  else if (callError) phase = 'failed'
  ...
  return {
    headerKey: phase === 'canceled' ? 'subagentCard.canceled' : `subagentCard.${verb}.${phase}`,
    ...
    ...(callError ? { callError } : {}),
  ```
  行的 `error` 字段在上面的循环里只给终态行：
  ```ts
  ...(status !== 'waiting' && subagentTaskTerminal(status) && String(bound?.error ?? facts.error ?? '').trim()
    ? { error: String(bound?.error ?? facts.error ?? '').trim() }
    : {}),
  ```
- `frontend/src/components/chat/SubagentCallCard.vue:17` 只在 `view.callError` 有值时画那一行：
  `<p v-if="view.callError" …>{{ view.callError }}</p>`。**`.vue` 不用改**。

### 约定

- 终端测试的写法照 `pkg/tui/reducer_test.go` 的 `TestSendCardFollowsItsAgentToTheEnd`（约 `:1638`）：
  `subagentStepMessage(stepID, tool.StepEvent{...})` 造工具消息，`SubagentSpawnedMsg` / `SubagentEndedMsg`
  用 `ParentToolCallID: <stepID>, TaskIndex: 0` 绑到卡上，`lastFanoutFrame` 取帧，`renderFanoutForTest(frame)`
  取画出来的行（已去 ANSI）。
- 网页测试的写法照 `useChatStream.test.ts` 里 `it('counts every outcome of a settled fanout, a skipped task included', …)`
  （约 `:4605`）：直接调纯函数 `subagentCallView({ stepId, toolName, summary, status, subagentCall: {...} })`。
- 代码注释风格：英文、说"为什么"，与周围一致；不要写中文注释。

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 终端定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<名字>' -count=1 -v` | 看到 `--- PASS: <名字>`（**必须看到 PASS 行**：`-run` 名字写错只会报 `no tests to run` 且 exit 0） |
| 终端整包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | 与基线相比不新增 FAIL |
| gofmt | `gofmt -l pkg/tui` | 无输出 |
| 网页定向测试 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t '<名字>'` | 1 passed |
| 网页测试 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/components/chat/SubagentCallCard.test.ts` | 与基线相比不新增失败 |
| 网页类型检查 | `cd frontend && pnpm typecheck` | exit 0 |

## Scope

**In scope（只改这些）**：
- `pkg/tui/reducer.go`（`emitFanoutFrame` + 一个新的小函数）
- `pkg/tui/reducer_test.go`（新增 1 个测试）
- `frontend/src/composables/useChatStream.ts`（`subagentCallView` 的 return）
- `frontend/src/composables/useChatStream.test.ts`（新增 1 个用例）

**Out of scope（看着相关也不要动）**：
- `pkg/tui/render.go` 的 `renderFanout`——字段为空就不画，不需要改。
- `fs.CallError` 的写入点（`applySubagentCallCompletion`、`PlanReviewReviewedMsg` 分支）和 `fanoutCardPhase`——
  卡头 "Failed to run …" 靠它们，必须保持。
- `SubagentCallCard.vue`、locale 文案、gateway 任何文件。
- 引擎（`pkg/run`）——让主代理拿到更好的失败结果是 `plans/140` 的事，不在这里做。

## Steps

### Step 1：终端——先写会红的测试

在 `pkg/tui/reducer_test.go` 的 `TestFanoutCardShowsWhyTheCallFailed` 之后新增：

```go
// A dispatched subagent that ran and then failed: its row carries the
// described reason, and the call's own failure — the same failure, as the raw
// error text the tool returned — must not be said a second time under the
// header. The header still says the call failed.
func TestFailedDispatchCardSaysWhyOnceOnTheTaskRow(t *testing.T) {
	t0 := time.Unix(1790000000, 0).UTC()
	input := map[string]any{
		"title": "执行计划 137 shell 正文", "task": "执行 plans/137",
		"subagent_type": "general-purpose",
	}
	const described = "The usage limit has been reached (HTTP 429 Too Many Requests)"
	const raw = `tool subagent_run: error, status code: 429, status: 429 Too Many Requests, message: {"error":{"message":"The usage limit has been reached"}}`
	var r Reducer
	_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("run-fail", tool.StepEvent{
		Kind: tool.StepKindToolStarted, ToolName: "subagent_run", Input: input,
	})})
	_ = r.Reduce(SubagentSpawnedMsg{
		AgentID: "subagent-137", AgentType: "general-purpose", TaskID: "subagent-137",
		Title: "执行计划 137 shell 正文", Task: "执行 plans/137",
		ParentToolCallID: "run-fail", TaskIndex: 0, ExecutionID: "exec-137", Timestamp: t0,
	})
	_ = r.Reduce(SubagentEndedMsg{
		AgentID: "subagent-137", AgentType: "general-purpose", TaskID: "subagent-137",
		Status: "failed", Error: described,
		ParentToolCallID: "run-fail", TaskIndex: 0, ExecutionID: "exec-137",
		FinishedAt: t0.Add(754 * time.Second), Timestamp: t0.Add(754 * time.Second),
	})
	card := lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("run-fail", tool.StepEvent{
		Kind: tool.StepKindToolCompleted, ToolName: "subagent_run", Input: input, Error: raw,
		Duration: 754 * time.Second,
	})}))

	if card.Summary != "Failed to run 1 general-purpose task" {
		t.Fatalf("summary = %q, want the call still reported as failed", card.Summary)
	}
	if card.FanoutCallError != "" {
		t.Fatalf("call error = %q, want none: the task row already says why", card.FanoutCallError)
	}
	painted := strings.Join(renderFanoutForTest(card), "\n")
	if strings.Contains(painted, "status code: 429") {
		t.Fatalf("the raw call error is still painted:\n%s", painted)
	}
	if n := strings.Count(painted, "The usage limit has been reached"); n != 1 {
		t.Fatalf("the reason is painted %d times, want exactly once:\n%s", n, painted)
	}
}
```

（`SubagentSpawnedMsg` 的字段名以 `pkg/tui/notify.go` 里的定义为准——`TestSendCardFollowsItsAgentToTheEnd` 用的就是
同一组字段；若编译报字段不存在，按 `notify.go` 改名，**不要**改断言。）

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestFailedDispatchCardSaysWhyOnceOnTheTaskRow$' -count=1 -v`
→ `--- FAIL`，失败信息是 `call error = "tool subagent_run: …", want none`。（若它直接 PASS，STOP 2：规则已经存在或测试没走到绑定路径。）

### Step 2：终端——帧上只在任务行没说原因时才带调用级错误

在 `pkg/tui/reducer.go` 里：

1. `emitFanoutFrame` 的返回值里把 `FanoutCallError:  fs.CallError,` 改为 `FanoutCallError:  fanoutDrawnCallError(fs),`。
2. 在 `emitFanoutFrame` 之后（`fanoutCardSettled` 之前）新增：

```go
// fanoutDrawnCallError is the call failure the card draws under its header.
// On a dispatch card whose task row already says why it failed, the call
// failed because that task did, and the row's reason is the described one —
// the call's text is the same failure again, as the raw error the tool
// returned — so the card says it once, on the row. The header still reads
// the call's failure from fs.CallError. A query card's rows are a snapshot of
// other executions, never the reason its own call failed, so it always draws
// the call's error.
func fanoutDrawnCallError(fs *fanoutState) string {
	if fs.CallError == "" || fs.Query {
		return fs.CallError
	}
	for _, t := range fs.Tasks {
		if t.Status == "failed" && strings.TrimSpace(t.Error) != "" {
			return ""
		}
	}
	return fs.CallError
}
```

不要改 `fanoutCardPhase`、`applySubagentCallCompletion`、`render.go`。

**Verify**：
- 同 Step 1 的命令 → `--- PASS: TestFailedDispatchCardSaysWhyOnceOnTheTaskRow`
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^(TestReducerFanoutFailedDispatchSettlesWaitingTasksBothOrders|TestFanoutCardShowsWhyTheCallFailed|TestNestedDispatchMirroredThroughRunEventsKeepsItsCard|TestPlanReviewCardUsesTheDispatchVerbs|TestPlanReviewFailureKeepsTheApprovalAndReportsIt)$' -count=1 -v`
  → 5 个 `--- PASS`（这些是"必须继续显示调用级错误"的情形）
- `gofmt -l pkg/tui` → 无输出

### Step 3：终端——变异验证（证明新测试有区分度）

临时把 `fanoutDrawnCallError` 的整个循环删掉（只留 `return fs.CallError`），跑 Step 1 的命令 → 必须 `--- FAIL`。
**立刻还原**成 Step 2 的版本，再跑一次 → `--- PASS`。

**Verify**：还原后 `git diff pkg/tui/reducer.go` 里只剩 Step 2 的两处改动。

### Step 4：网页——先写会红的用例

在 `frontend/src/composables/useChatStream.test.ts` 里 `it('counts every outcome of a settled fanout, a skipped task included', …)`
之后新增：

```ts
  it('says why a dispatched subagent failed once, on its row', () => {
    const raw = 'tool subagent_run: error, status code: 429, status: 429 Too Many Requests, message: {"error":{}}'
    const described = 'The usage limit has been reached (HTTP 429 Too Many Requests)'
    const view = subagentCallView({
      stepId: 'call-run-137', toolName: 'subagent_run', summary: 'run 执行计划 137', status: 'failed', error: raw,
      subagentCall: {
        verb: 'run',
        tasks: [
          { index: 0, title: '执行计划 137 shell 正文', agentType: 'general-purpose', status: 'failed', error: described, executionId: 'e1', startedAt: 1_799_223_000, finishedAt: 1_799_223_754 },
        ],
      },
    })
    // The header still says the call failed.
    expect(view.headerKey).toBe('subagentCard.run.failed')
    // The row says why; the call's raw text is the same failure again.
    expect(view.callError).toBeUndefined()
    expect(view.rows[0]).toMatchObject({ status: 'failed', error: described })

    // With no reason on any row, the call's own failure is the only account.
    const bare = subagentCallView({
      stepId: 'call-run-x', toolName: 'subagent_run', summary: 'run x', status: 'failed', error: 'unknown tool "subagent_run"',
      subagentCall: { verb: 'run', tasks: [{ index: 0, title: 'x', agentType: 'general-purpose', status: 'waiting' }] },
    })
    expect(bare.headerKey).toBe('subagentCard.run.failed')
    expect(bare.callError).toBe('unknown tool "subagent_run"')
  })
```

（`SubagentToolStep` 的字段名以 `useChatStream.ts` 里的类型为准：若 `error` / `status` 字段名不同，按类型改用例里的键，
**不要**改断言的意思。若 `subagentCall.tasks[].error` 不在类型里，STOP 3。）

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'says why a dispatched subagent failed once'`
→ 1 failed，失败在 `expect(view.callError).toBeUndefined()`。

### Step 5：网页——同一条规则

在 `subagentCallView` 的 `return { … }` 之前加：

```ts
  // A dispatch card whose row already says why its task failed draws no call
  // error: the call failed because that task did, and the row carries the
  // described reason — the call's text is the same failure again, raw. The
  // phase still reads callError, so the header keeps saying it failed. A
  // query card's rows are a snapshot of other executions, so it keeps its
  // call error. Mirrors the terminal's fanoutDrawnCallError.
  const drawnCallError = callError && !query && rows.some((row) => row.status === 'failed' && row.error)
    ? undefined
    : callError
```

并把 return 里的 `...(callError ? { callError } : {}),` 改为 `...(drawnCallError ? { callError: drawnCallError } : {}),`。
`phase` 的推导**继续用 `callError`**，不要改。

**Verify**：
- 同 Step 4 的命令 → 1 passed
- `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/components/chat/SubagentCallCard.test.ts` → 与基线相比不新增失败
- `cd frontend && pnpm typecheck` → exit 0

### Step 6：网页——变异验证

临时把 `drawnCallError` 改成 `const drawnCallError = callError`，跑 Step 4 的命令 → 1 failed；**立刻还原**，再跑 → 1 passed。

### Step 7：终端真机看一眼（run-forebrain 假模型驱动）

读 `.claude/skills/run-forebrain/SKILL.md` 的开头与 `subagent-limit` 那一行。然后：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D build
ENABLE_SUBAGENT=1 FAKE_LIMIT_SECONDS=60 $D start subagent-limit 'probe answered'
$D submit 'go'
$D wait 'Failed to run 1 general-purpose task' 30
$D screen > "$TMPDIR/p139-screen.txt"; cat "$TMPDIR/p139-screen.txt"
grep -A1 'Failed to run 1 general-purpose task' "$TMPDIR/p139-screen.txt"
grep -c 'tool subagent_run:' "$TMPDIR/p139-screen.txt"
$D stop
```

**Verify**：第一个 grep 打出的两行里，第二行就是 `└ ✗ network probe …` 任务行（卡头与任务行之间不再夹一行调用级错误）；
任务行下面紧跟一行失败原因（引擎描述过的 429 文本，含 `HTTP 429`）；第二个 grep 输出 `0`
（调用级错误总以 `tool subagent_run:` 开头，见 `pkg/run/orchestration_llm.go` 的 `executeToolHandler`）。
改代码前若先跑一遍同样的命令作对照，应能看到那行 `tool subagent_run:`——可选，不是闸门。
（必须在 `FAKE_LIMIT_SECONDS` 的 60 秒内截屏；之后子代理会按现行规则自己续跑，屏幕会变——那是 140 要改的事，与本计划无关。）
若驱动起不来，按 SKILL.md 的 Troubleshooting 表处理；仍不行就把现象写进报告（STOP 4），**不要**跳过此步宣称完成。

## Test plan

- 新增 `pkg/tui/reducer_test.go::TestFailedDispatchCardSaysWhyOnceOnTheTaskRow`：派发 → 绑定 → 以描述过的原因结束 →
  调用以原始错误失败；断言卡头仍是 failed、帧上无调用级错误、画出来的原因恰好一次、原始文本不出现。
- 新增 `useChatStream.test.ts` 用例 `says why a dispatched subagent failed once, on its row`：同样两件事 + 无原因时保留调用级错误。
- 回归：上面"必须继续显示调用级错误"表里的 5 个 Go 测试、整包 `pkg/tui`、两个前端测试文件。
- 两处都有变异验证（Step 3、Step 6）。

## Done criteria

- [ ] `TestFailedDispatchCardSaysWhyOnceOnTheTaskRow` 有 `--- PASS` 行
- [ ] "必须继续显示调用级错误"表里 5 个测试全部 `--- PASS`
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 与基线相比无新增 FAIL
- [ ] `gofmt -l pkg/tui` 无输出
- [ ] 新网页用例 passed；两个前端测试文件与基线相比无新增失败；`pnpm typecheck` exit 0
- [ ] `grep -n "fanoutDrawnCallError" pkg/tui/reducer.go` ≥ 2 条（定义 + 调用）；`grep -n "drawnCallError" frontend/src/composables/useChatStream.ts` ≥ 2 条
- [ ] Step 7：卡头下一行是 `└ ✗ network probe` 任务行；`grep -c 'tool subagent_run:'` 输出 `0`
- [ ] `git status --porcelain` 里本计划新增的改动只落在 In scope 的 4 个文件
- [ ] `plans/README.md` 状态行已更新（除非评审方说由其维护）

## STOP conditions

1. 前置 1 / 前置 2 不满足，或"现状"里按函数名找到的代码含义已变（例如 `fanoutCardPhase` 不再读 `fs.CallError`）。
2. Step 1 的新测试在改代码前就 PASS。
3. 网页类型里没有行级 `error`、或 `subagentCallView` 已不再是网页子代理卡的唯一推导。
4. Step 7 的驱动反复起不来，或屏幕上原因仍出现两次。
5. 基线跑不起来（编译失败、依赖缺失）。
6. 任何一步需要改 In scope 以外的文件。

## Maintenance notes

- 规则是"派发卡的任务行已带原因 ⇒ 调用级错误不再画"，终端 `fanoutDrawnCallError` 与网页 `drawnCallError`
  是同一条规则的两份实现，改一处必须改另一处（`subagentCallView` 的注释写明"The semantics are the terminal's"）。
- 卡头 failed 的判定仍读 `fs.CallError` / `callError`；以后若有人想"干脆不记调用级错误"，卡头会退回 "Ran …"，那是错的。
- `plans/140` 会把 `subagent_run` / `subagent_continue` 失败时的调用级错误换成写给主代理的多行说明；本规则正是它不会出现在卡片上的保证。
