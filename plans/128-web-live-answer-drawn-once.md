# Plan 128：web 普通回合 live 不再把回答画两遍——observer 对本页请求 socket 已在投影的 run 片段直接丢弃，不再缓冲后重放

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置与 drift check（先跑，三步）**：
>
> 1. **122 必须先落**（owner 2026-10-26 决定：122 已开跑，本计划顺延到它之后）。在干净 dev（或它的
>    worktree）上确认：
>    `grep -n "RunEventAssistantDone" pkg/event/run_events.go` → **有输出**（含它在 `IsPushOnlyStreamEvent` 里）；
>    `grep -n "case 'assistant_done'" frontend/src/composables/useChatStream.ts` → **有输出**。
>    两条任一为空 = 122 还没落地，停在这里**不要开工**，也不要用旧基线代替。
> 2. **工作区干净度**：`git diff --stat HEAD -- frontend/src/composables/useChatStream.ts
>    frontend/src/composables/useChatStream.test.ts frontend/e2e/network-retry.spec.ts` → 预期**无输出**
>    （这三个文件没有别人未提交的改动；基线不再钉死在 `c72da06`）。
> 3. **行号不重要，内容才重要**：本文件所有行号取自 `c72da06`；122 在 :3210/:3410/:4710 附近各插入若干行，
>    之后的行号整体后移（:3189 之后 +6 左右，:4512 之后 +12 左右，:5036/:5418 之后 +17 左右）。一律按**符号与
>    引文**定位（`legacyMirroredRunIDs`、`transcriptBackedEventTypes`、`applyObservedEvent` 的缓冲分支、
>    `eventProjectionByRun.clear()`、`case 'turn_started'`、`op === 'run_started'`、`else if
>    (!legacyAlreadyProjected(...))`、send 的 `finally`）；找到的代码与摘录**内容**一致就继续，只有内容不同才
>    STOP 1。

## Status

- **Priority**: P2（存量显示缺陷：117 执行期在 e2e 里实测并记录，bb8d116 对照同红；每一条普通回合 live 都中招，重载自愈）
- **Effort**: S–M
- **Risk**: LOW–MED（改动只在 observer 侧加一条丢弃谓词，外加归属集合的清空时机；第二标签页/跨端直播由 Step 4 守护，断线重连由既有 :1236 用例守护；最坏情形——请求 socket 在断线窗口里丢掉一条片段——由 `stream_partial` 重推与 settle 的最终全文自愈，见 "Why this matters" 末段。唯一要小心的时序是"会话中途切换后旧 send 收尾"，Step 2.3 的 `stillMine` 守卫与 Step 3 的用例钉住它）
- **Depends on**: **plans/122**（必须先落：它给 `assistant_done` 定了 push-only 身份，也在 `useChatStream.ts` 里插入了
  几个 case，本计划的事件名单与全部行号都以 122 之后的树为准）。124–127 与本计划无依赖。
- **Category**: bug（web live 观察路径）
- **Planned at**: commit `c72da06`，2026-10-26；**执行基线 = 122 落地后的 dev HEAD**（行号见前置检查第 3 条）

## Why this matters

web 上每一条普通回合，回答的文字在 live 页面里出现**两遍**；重载后恢复正常（库里只有一份）。这是 117 执行期在
`frontend/e2e/network-retry.spec.ts:44-47` 的注释里实测记录的存量缺陷（"The live page draws a turn's answer twice
for every turn — a pre-existing observer-path double the reload does not carry"），当时把 exactly-once 断言只放在了
reload 一侧。根因不是重试，而是**同一页两条 WebSocket 都收到同一条 delta**：请求 socket（send 自己的那条）把它
流进气泡，sessionObserver 的副本在流期间被缓冲、send 结束时统一重放，又往同一个气泡追加了一遍。文字类的
push-only 片段（回答、思考、工具输出增量）没有事件 id 可供去重，所以双画挡不住。修掉之后 live 与重载一致，
117 留下的那条注释和"exactly-once 只敢在重载侧断言"的限制一并消失。

"发送中的那条 socket 是这个 run 的唯一应用方"不是新发明：同一个文件的 observer 对流中的 `stream_partial`
早就是这样让路的（`useChatStream.ts:3124-3127`），本计划只是把这条既有不变式推广到 push-only 片段。

安全网两条，都是既有机制、不新增代码：① 请求 socket 断线重连时，`stream_partial` op 把进行中的正文、思考与
工具输出整段重推（请求 socket 在 :5002-5008 应用；observer 流中主动丢弃，:3117-3127）；② turn 结束的
`run_completed` op 自带最终全文（网关 `pkg/gateway/server.go:2411-2416`）并驱动 `withSettledTurnText` 对账
（`useChatStream.ts:5072-5094`），文字类的残缺在 settle 时自愈。

## Current state（2026-10-26 于 `c72da06` 逐行核过）

### 0. 三个文件的角色

- `frontend/src/composables/useChatStream.ts`（5784 行）——web 对话的全部实时投影：send 的请求 socket 处理器、
  sessionObserver 处理器、事件→消息块的 reducer。
- `frontend/src/composables/useChatStream.test.ts`——上述的单测（vitest），`FakeChatWebSocket` 模拟双 socket。
- `frontend/e2e/network-retry.spec.ts`——117 加的 e2e（假 provider `netdrop` 模式），注释里记录了本缺陷。

### 1. 网关侧（**不改**，只是事实）：每条会话事件扇出给所有订阅连接

`pkg/gateway/wsevents.go:605-649` `runEventBus.Publish`：

```go
	// A stream fragment — the words of an answer or reasoning block, a chunk of
	// a tool's output — is pushed and stored nowhere: ...
	// with no sequence and no event id. onRunEnded is not told either: nothing
	// ended.
	if event.IsPushOnlyStreamEvent(evt.Type) {
		b.deliver(session, evt)
		return nil
	}
```

`deliver` 把事件投给**每一个**看这个会话的连接——send 的请求 socket 和 sessionObserver 都在订阅表里。push-only
片段的成员表在 `pkg/event/run_events.go:93-100`：

```go
func IsPushOnlyStreamEvent(eventType string) bool {
	switch eventType {
	case RunEventAssistantDelta, RunEventReasoningDelta, RunEventReasoningDone, RunEventToolOutputDelta, RunEventTransportRetry:
		return true
	}
	return false
}
```

（即 `assistant_delta`、`reasoning_delta`、`reasoning_done`、`tool_output_delta`、`transport_retry`；这些事件
**无 id 无 sequence**——前端 `rememberObservedEvent` 的 `appliedEventIDs` 去重对它们无效。网关不能改成"只投给
请求 socket"：第二个浏览器标签页靠 observer 看直播。）

### 2. 请求 socket：delta 直接投影，不记录任何身份

`useChatStream.ts:5317-5337`（send 的 `ws.onmessage`，处理 `run_event` 帧）：

```ts
              // Primary canonical events mirror the legacy operations on this
              // same request socket. Project only child events here; retaining
              // the canonical id/cursor prevents the observer from replaying
              // the primary event after streaming finishes.
              if (resume) {
                ...
              } else if (!legacyAlreadyProjected(runEvent)) {
                handleRunEvent(runEvent, assistantMessageId, planBlocksAccumulator, eventState)
              } else {
                rememberObservedEvent(runEvent)
              }
```

`assistant_delta` 不在 `legacyMirroredEventTypes`（:2845-2852，成员是 turn_started/completed/cancelled/error、
mode_changed、pending_input_updated），所以走 `!legacyAlreadyProjected` 那一支：`handleRunEvent` 直接把文字
追加进气泡（`handleRunEvent` 的 `case 'assistant_delta'` 在 :4690-4695：`state.fullAnswer += txt` +
`withSpokenDelta(message, txt, evt.id, evt.createdAt)`）。**不记 id**（片段也没有 id）。

### 3. observer：流期间缓冲，send 收尾统一重放——第二遍就在这里发生

`useChatStream.ts:2996-3050` `applyObservedEvent`，缓冲分支在 :3044-3047：

```ts
    if (!historical && (historyLoadingSession || isStreaming.value)) {
      bufferedSessionEvents.push(evt)
      return
    }
    const projection = eventProjection(evt)
    handleRunEvent(evt, projection.assistantMessageId, projection.planBlocks, projection.state)
```

send 的收尾 `finally`（:5426-5439）：

```ts
      if (streamAbortController === myController) {
        isStreaming.value = false
        clearRuntimeStatus()
        streamAbortController = null
        activeRunId = null
        contextSignals.value = { ...contextSignals.value, activeRunId: undefined }
      }
	  flushBufferedSessionEvents()
```

`flushBufferedSessionEvents`（:3052-3057）把缓冲事件按原顺序逐条重走 `applyObservedEvent`——不再缓冲
（`isStreaming` 已 false），落到 :3048-3049 的投影分支。`eventProjection`（:2771 起）按 runId 查
`eventProjectionByRun`（send 在 `run_started` op（:5037-5040）与 `turn_started` 事件（:4513-4515）注册过，
runId→send 自己的 `assistantMessageId`），于是同一条 delta 被追加进**同一个气泡**。追加函数
`appendStreamedText`（:1342-1358）无条件 concat 或新建块：

```ts
  const last = blocks[blocks.length - 1]
  if (last && last.kind === kind && last.open) {
    const grown = { ...last, text: last.text + text }
    return [...blocks.slice(0, -1), grown]
  }
```

settle 后块已闭合，flush 的第二遍会**新建一个开块**把全文再放一遍——live 页面文字两遍。思考文本
（`reasoning_delta`）与工具卡内的 `tool_output_delta` 同理双份；`tool_call_started/completed` 是按 stepId 的
upsert（幂等）所以看不出来。重载侧正常：delta 只推不落库（plan 018 语义），transcript 每块收尾写一份。
`withSpokenDelta`（:985-994）把 `content` 按 `blocks` 重算，所以双画同时出现在 `content` 与块序列上——
Step 1 的两条断言都会红。

### 3.1 同一不变式的先例：observer 对流中 `stream_partial` 已经让路（`useChatStream.ts:3117-3127`）

```ts
          if (String(raw.op ?? '').trim() === 'stream_partial') {
            // The in-flight text and tool output this process holds for the
            // conversation; ... A local send owns the live tail: its own socket
            // handles the partials, so the observer drops them rather than fighting it.
            if (historyLoadingSession) {
              bufferedStreamPartials.push(toCamelCase(raw) as Record<string, unknown>)
            } else if (!isStreaming.value) {
              applyStreamPartial(toCamelCase(raw) as Record<string, unknown>)
            }
            return
          }
```

`isStreaming` 就是"本页有 send 在跑"的粗粒度判据。本计划把它细化到 run 维度（`sendOwnedRunIds`），
因为 `stream_partial` 是整段重推（可替换），而 push-only 片段是追加（不可替换）——追加必须按 run 判断。

### 4. `activeRunId` 的时序（修法的无竞态前提）

send 的 `run_started` **op 回复**（不是事件）就设置 `activeRunId`（:5033-5035）；网关先写这个 op、之后才跑回合
（`pkg/gateway/server.go:1988-1996`），所以 observer 收到第一条片段时 `activeRunId`（以及本计划要加的
send-owned run 集合）已就绪。

两条 TCP 连接之间没有先后保证，乱序窗口理论上仍在——但那条漏网的片段进的是 `bufferedSessionEvents`，而 flush
会把队列重新交给 `applyObservedEvent`（:3056），谓词在那里照样生效。所以判断只写在 `applyObservedEvent` 一处
就够，flush 不必再加一份（Step 2.4）。

### 5. 测试样板（照抄结构）

- 双 socket 夹具：`useChatStream.test.ts:1004-1052`（`a live run_completed closes the turn with its checklist facts`）
  ——`stream.send('hello')` → `FakeChatWebSocket.instances[0]` 是请求 socket → `socket.open()` →
  `session_bound` → `run_started` op → `run_event` 帧（注意请求 socket 上的帧带 `request_id`，observer 上不用，
  见 :3623-3628 的 `observerEvent` 助手：`{ op: 'run_event', data: { id, sequence, type, run_id, session_id, created_at, payload } }`）。
- observer 手动驱动：:3644-3655——`stream.switchToSession('s1')` 等 history 加载完 → 这条路径上 `instances[0]`
  就是 observer → `observer.open()` → 断言 `sent[0]` 是 `bind_session` → `observer.message({ op: 'session_bound', ... })`。
  send 流程里 observer 在 `session_bound` 之后由 `startSessionObserver` 自动创建（`useChatStream.ts:4993`），
  它的下标不在契约里——两个新用例都按 `sent[0]` 的 op 认 socket（`chat` = 请求 socket，`bind_session` = observer）。
- 断线重连（resume）守护用例：:1236-1268（`resume_connection` 重试 socket、durable cursor=2、另一 run 的事件
  不得污染本请求）——修完后必须仍然全绿。

### 6. 仓库约定

- 前端注释英文、完整句子、写"为什么"；改哪段就给哪段配注释，参考 :2830-2843 那种块注释。
- i18n：本计划不改任何用户可见文案，不涉及。
- 验证命令用 frontend 目录内的 pnpm 脚本（见下表）；e2e 走仓库统一的 `scripts/acceptance/web_e2e.sh`
  （假 provider，`FOREBRAIN_E2E_SPEC` 圈定单份 spec，约两分钟）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 单测（本计划主战场） | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` | 全绿（含新增用例） |
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0 |
| e2e（单 spec） | `FOREBRAIN_E2E_SPEC=e2e/network-retry.spec.ts scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**（只改这三个文件）：
- `frontend/src/composables/useChatStream.ts` — 新增片段类型常量与 send-owned run 集合；`applyObservedEvent` 缓冲
  分支之前的一处丢弃谓词；集合的注册点（`run_started`、`turn_started`，加上 Step 2.5 复现成立时的 spawn 映射）
  与两个清空点（send 收尾的 `stillMine` 守卫、`applySessionReset`）。
- `frontend/src/composables/useChatStream.test.ts` — 新增 3 个用例（复现 + 归属守卫 + 无 send 时 observer 直投），
  Step 2.5 复现成立时再加 1 个。
- `frontend/e2e/network-retry.spec.ts` — 删过期注释、live 侧加 exactly-once 断言。

**Out of scope**（不要碰，即使看起来相关）：
- `pkg/gateway/**`（扇出语义是对的：第二标签页靠它看直播）。
- `appendStreamedText` / `withSpokenDelta` / settle 逻辑（`withSettledTurnText`）。
- `legacyMirroredEventTypes` / `transcriptBackedEventTypes` / `rememberObservedEvent` 的语义。
- `eventProjection` 对 `input_delivered` 续答 run 仍指向旧消息的既有怪相（独立问题，未立项）。
- ChatView / 子代理的渲染与视图投影 / TUI。（子代理的**片段丢弃映射**是唯一例外，且只允许 Step 2.5 那几行：
  改 `routeSubagentEvent` 的绘制、抽屉布局、子代理 transcript 都不是本计划的事。）

## Git workflow

不 commit、不 push、不开 PR，改动留在工作区（owner 审阅后自行提交）。

**开工位置**：等 122 合到 dev 之后再动手，从**当时的 dev** 起工作区——owner 用 worktree 就在新 worktree 里做
（122 自己就在 `.worktrees/exec-122-129` 上）：工作树里 `git rev-parse --abbrev-ref HEAD` 不要求是 `dev`，但
`git merge-base --is-ancestor <122 的提交> HEAD` 必须成立，否则前置检查第 1 条视为不满足。

## Steps

### Step 1：先写复现用例（此刻必须红）

在 `useChatStream.test.ts` 里、紧挨 :1004-1052 那组 worked-line 用例之后新增一个用例
（夹具照抄该组：替换 `globalThis.WebSocket`、spy `chatMessages`/`sessionMode`、try/finally 还原），命名
`one send draws its answer once even though both sockets carry the delta`：

1. `const stream = useChatStream()`；`const pendingSend = stream.send('hello')`。
2. `const socket = FakeChatWebSocket.instances[0]`；`socket.open()`；读 `sent[0].request_id`；
   `socket.message({ op: 'session_bound', request_id, session_id: 'web-once' })`；
   `socket.message({ op: 'run_started', request_id, run_id: 'run-once', session_id: 'web-once' })`。
3. 请求 socket 收一条 delta：`socket.message({ op: 'run_event', request_id, data: { id: 'evt-once-1', type: 'assistant_delta', run_id: 'run-once', session_id: 'web-once', payload: { text: 'single answer' } } })`
   （不需要 sequence；真实片段没有）。
4. 等一拍（`await Promise.resolve()` 或现有用例惯用的同步推进），按**它说的话**找 observer socket
   （照 :1245-1252 的既有写法："found by what it says, not by where it lands in the list"）：
   遍历 `FakeChatWebSocket.instances`，跳过 `socket` 本身，对每个候选先 `candidate.open()`，再读
   `JSON.parse(candidate.sent[0] ?? '{}')`，`op === 'bind_session' && session_id === 'web-once'` 的那个就是
   `observer`；随后 `observer.message({ op: 'session_bound', session_id: 'web-once' })`（回 bind 应答让它就位）。
   不要用下标或 `readyState` 猜：observer 由 `session_bound` 之后的 `startSessionObserver`（:4993）创建，
   它落在 `instances` 的第几位不在契约里。
5. **同一条 delta 再投给 observer**（不带 request_id）：
   `observer.message({ op: 'run_event', data: { id: 'evt-once-1', type: 'assistant_delta', run_id: 'run-once', session_id: 'web-once', payload: { text: 'single answer' } } })`。
6. 收尾：`socket.message({ op: 'run_completed', request_id, run_id: 'run-once', session_id: 'web-once', text: 'single answer', finished_at: '2026-06-11T00:00:01.000Z' })`；
   `await pendingSend.catch(() => {})`。
7. 断言：`const turn = stream.messages.value.find((m) => m.role === 'assistant')`；
   `expect(turn.content).toBe('single answer')`；并数块：
   `expect((turn.blocks ?? []).filter((b) => b.kind === 'assistant' && b.text?.includes('single answer')).length).toBe(1)`。

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'draws its answer once'`
→ **FAIL**：`turn.content` 是 `'single answersingle answer'`（flush 走 `withSpokenDelta` :985-994，`content` 按
块重算），且含该文本的 assistant 块是两个。不红就 STOP 2。

### Step 2：实现丢弃规则

`useChatStream.ts`，四处改动：

1. 在 `legacyMirroredRunIDs`（:2424）旁新增：

```ts
  // Runs whose live fragments this page's request socket is already
  // projecting. Push-only fragments (below) have no event id, so the
  // observer's copy of one cannot be deduped — it must not be applied at all.
  const sendOwnedRunIds = new Set<string>()
```

2. 在 `transcriptBackedEventTypes` 定义旁新增常量。成员**必须与磁盘上 Go `pkg/event/run_events.go` 的
   `IsPushOnlyStreamEvent` 逐字一致**——前提检查已保证 122 落地，所以是**六个**：多一个 `assistant_done`
   （122 Step 6 加的；漏了它，该片段的 observer 副本仍会在 flush 时被应用，settle 后表现为重复关块）。
   注释里写明这层对应关系与"无 id 无 sequence、只投不存"：

```ts
  // The stream fragments the gateway pushes and never stores: they carry no
  // event id and no sequence (pkg/event/run_events.go IsPushOnlyStreamEvent),
  // so a second copy of one cannot be deduped — it can only be dropped.
  const pushOnlyFragmentTypes = new Set([
    'assistant_delta',
    'assistant_done',
    'reasoning_delta',
    'reasoning_done',
    'tool_output_delta',
    'transport_retry',
  ])
```

3. 注册与清空（**清空点只有两个，多一个都不行**）：
   - `run_started` op 分支（:5036-5040，`legacyMirroredRunIDs.add(activeRunId)` 旁）：`sendOwnedRunIds.add(activeRunId)`；
   - `turn_started` 事件分支（:4512-4515，`eventProjectionByRun.set` 旁）：同样 add（注释：resume 连接的请求 socket
     不会再收到 legacy 的 `run_started` op，它的 run 归属只能从这条事件认领）。
     `turn_started` 的 `handleRunEvent` **会**被子代理事件绕过（`routeSubagentEvent` 更早），所以子代理的 run
     **不在这里**认领——它走 Step 2.5 的 spawn 映射（子代理片段带的是**子进程自己的 run id**：
     `pkg/run/subagent.go:1013` 连同 `AgentID` 一起发出，父 run id 对不上）。
   - send 的 `finally`（:5418-5435）：**只在本次 send 仍然持有流时清空**。会话中途切换时 `applySessionReset`
     （:3219-3225）把 `isStreaming` 置 false、把 `streamAbortController` 置 null，但**不会**中止在跑的那次 send；
     于是旧 send 的收尾可能落到新 send 正在流的时候，无条件 `clear()` 会把新 run 的归属一起抹掉——双画在这一刻
     悄悄回来，没有任何报错。把 finally 改成：

```ts
    } finally {
      // ...（既有注释与那块 if 的正文原样保留，只多一个 stillMine）
      const stillMine = streamAbortController === myController
      if (stillMine) {
        isStreaming.value = false
        clearRuntimeStatus()
        streamAbortController = null
        activeRunId = null
        contextSignals.value = { ...contextSignals.value, activeRunId: undefined }
      }
      flushBufferedSessionEvents()
      // Only the send that still owns the stream may retire these: a session
      // switch mid-stream nulls the controller without stopping this send, so
      // its end can arrive while a later send's run is streaming — clearing
      // here would hand that run back to the observer and draw it twice.
      if (stillMine) sendOwnedRunIds.clear()
    }
```

   （`stillMine` 必须在 `streamAbortController = null` **之前**取值。两次 send 不会并行：`send` 在 :4922
   （steer/queue）与 :4929（held）已经挡住重叠，所以"整集合清空"在唯一那次 send 收尾时是安全的。）
   - `applySessionReset` 里 `eventProjectionByRun.clear()` 的**那一处**（:3189）旁加 `sendOwnedRunIds.clear()`。
     **不要**动 :5612 那一处：它不是 `applySessionReset`，而是 `loadMessages` 的历史投影路径，而这条路径明说了
     可能在 send 流着的时候落地（:5601 "A send that started before this snapshot resolved owns the live tail"）。
     在那里清空会让正在直播的 run 失去归属，双画在该回合剩余时间里复发。见 STOP 6。
4. 丢弃谓词——**只放一处**，放在 `applyObservedEvent` 上方：

```ts
  // While a send owns a run, its request socket is the sole deliverer that
  // applies that run's fragments: the gateway fans every event out to every
  // subscriber, and the observer's copy of a fragment has no id to dedup by —
  // applied at flush it would append the same words a second time. A page not
  // sending (a second tab, another surface's run) has no owned runs and still
  // draws live from the observer.
  function fragmentSuppressedBySend(evt: ForebrainRunEvent): boolean {
    return pushOnlyFragmentTypes.has(String(evt.type))
      && sendOwnedRunIds.has(String(evt.runId ?? '').trim())
  }
```

   - 在 `applyObservedEvent` 的缓冲分支（:3044）**之前**加：
     `if (!historical && fragmentSuppressedBySend(evt)) return`（直接丢，不进缓冲）。
   - `flushBufferedSessionEvents`（:3052-3057）**不要**再加一份同样的判断：它只是把队列里的每条事件重新交给
     `applyObservedEvent(evt)`（:3056），谓词在那里会再跑一次；而且 flush 只在 `isStreaming` 已为 false 时才执行
     （:3053），那一刻的状态与当场判断完全一致。再写一份只会日后两边漂移，不关任何窗口。

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts`
→ Step 1 的用例**转绿**，且全文件无其他红（尤其 :1236 的断线重连用例、:3630 起的 auto-continue observer 组）。

### Step 2.5：同类覆盖——子代理片段（**先复现，复现不出来就不要写**）

子代理的片段带的是**子进程自己的 run id**（`pkg/run/subagent.go:1013`），所以 Step 2 的谓词对它们是失效的；
而子代理的文字同样是**追加**（`routeSubagentEvent` 的 `case 'assistant_delta'` 走 `appendStreamedText`，
`useChatStream.ts:3384-3398`），请求 socket 已经画过一遍，observer 的副本在 flush 时还会再画一遍——同一类双画，
只是画在抽屉里。这一段是**从代码路径推出来的，尚未在真机/浏览器上实测**，所以先复现：

1. 在 Step 1 的用例旁边加一个同结构的用例 `a subagent's streamed words are drawn once`：请求 socket 上先投
   `subagent_spawned`（事件自身的 `run_id: 'child-run'`，`payload: { agentId: 'task-1', parentRunId: 'run-once', ... }`），
   再投 `assistant_delta`（`run_id: 'child-run'`，`payload: { agentId: 'task-1', text: 'child says' }`）；
   observer 上投同样两条；`run_completed` 收尾后断言子代理的条目里 `child says` 只出现一份。
   （子代理数据在 `stream.subagents.value`；找不到宿主就断言它的 `blocks` 里恰好一个 assistant 块含该文本。）
2. 跑一下这个用例，只有两种结果：
   - **红** → 做第 3 步；
   - **绿**（例如 worker transcript 回填把重复吃掉）→ **什么都不加**，把这条结果写进报告，Step 2.5 到此收口。
   不做投机改动。
3. 变红的修法（只加这几行，仍用同一个集合、同样的两个清空点）：在 `handleRunEvent` 的
   `if (evt.type === 'subagent_spawned') { … }` 分支里（`bindSubagentSpawn(...)` 调用旁，`payload` 已在作用域内）加：

```ts
      // A send owns its run's children too: a spawned child's fragments come
      // under its own run id, and the observer's copy of them would append the
      // child's words a second time at flush exactly as the parent's did.
      if (sendOwnedRunIds.has(String(payload.parentRunId ?? '').trim())) {
        sendOwnedRunIds.add(String(evt.runId ?? '').trim())
      }
```

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` → Step 1、Step 2.5 两个用例都绿，
其余全绿。

### Step 3：守护用例——会话中途切换后，新 send 的归属不被旧 send 的收尾抹掉

同文件新增用例 `a send that starts after a session switch keeps its run its own`。它同时钉住两件事：丢弃谓词
本身，以及 Step 2.3 里 `stillMine` 那个清空守卫（去掉守卫时它会红）。

本步骤与 Step 4 都可以照 :1245-1252 的做法写一个小助手：遍历 `FakeChatWebSocket.instances`，对还没 `open()` 的
候选调用 `candidate.open()`，再读 `JSON.parse(candidate.sent[0] ?? '{}')`——`op === 'chat'` 的是请求 socket，
`op === 'bind_session'` 的是 observer。

1. `const stream = useChatStream()`；`const pendingA = stream.send('hello')`；用上面的助手找到 `op === 'chat'` 的
   那条 socket 记为 `socketA`；`const sentA = JSON.parse(socketA.sent[0] ?? '{}')`；用 `sentA.request_id` 回
   `session_bound`（`session_id: 'web-a'`）与 `run_started`（`run_id: 'run-a'`）；
   投一条 `assistant_delta`（`run-a`，`payload: { text: 'first answer' }`）。
2. `stream.switchToSession('web-b')`。前提是这些 spy 已生效：`chatMessages`（`[]`）、`sessionMode`、
   `sessionSubagentHistory`、`sessionEvents`（`{ sessionId: 'web-b', nextCursor: 0, highWater: 0, hasMore: false,
   schemaVersion: 1, events: [] }`）；`await vi.waitFor(() => expect(stream.historyLoading.value).toBe(false))`。
3. `const pendingB = stream.send('second')`；用同一个助手找到**新出现的**那条 `op === 'chat'` socket 记为 `socketB`
   （`sent[0]` 只在 `open()` 之后才有，所以助手必须先 open 再判断）；回 `session_bound`（`'web-b'`）与
   `run_started`（`run_id: 'run-b'`）；投一条 `assistant_delta`（`run-b`，`payload: { text: 'second answer' }`）。
4. **让旧 send 先收尾**：`socketA.message({ op: 'run_completed', request_id: sentA.request_id, run_id: 'run-a',
   session_id: 'web-a', text: 'first answer', finished_at: '2026-06-11T00:00:01.000Z' })`；
   `await pendingA.catch(() => {})`。（此时 B 仍在流中；旧 send 的 `finally` 已经跑过。）
5. 再跑一次同一个助手：第 2 步之后新建的那条 `bind_session` socket（助手会 `open()` 它，`sent[0].session_id`
   应是 `'web-b'`）就是 observer；回 `session_bound`（`session_id: 'web-b'`），然后把同一条 `assistant_delta`
   （`run-b`，`payload: { text: 'second answer' }`）投给它。
6. 收尾 B：`socketB.message({ op: 'run_completed', request_id: JSON.parse(socketB.sent[0] ?? '{}').request_id,
   run_id: 'run-b', session_id: 'web-b', text: 'second answer', finished_at: '2026-06-11T00:00:02.000Z' })`；
   `await pendingB.catch(() => {})`。
7. 断言：`const turn = stream.messages.value.find((m) => m.role === 'assistant' && String(m.content ?? '').includes('second answer'))`；
   `expect(turn?.content).toBe('second answer')`；且 `(turn.blocks ?? []).filter((b) => b.kind === 'assistant' &&
   b.text?.includes('second answer')).length` 为 1。

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'keeps its run its own'` → PASS。
可选自查（不计入验收）：把 Step 2.3 的 `if (stillMine) sendOwnedRunIds.clear()` 临时改回无条件 `clear()`，本用例应
转红；确认后改回来。

### Step 4：守护用例（无 send 时 observer 直投仍画）

同文件新增用例 `an observer with no send in flight still draws live deltas`（夹具照抄 :3630-3655 的
switchToSession 模式，spy 里记得补 `sessionSubagentHistory`）：

1. `stream.switchToSession('s1')`，等 `historyLoading.value === false`；
2. 对还没 `open()` 的候选调用 `open()` 后读 `sent[0]`，`op === 'bind_session'` 的那条记为 `observer`；回
   `session_bound`；
3. 投 `run_event`：`{ data: { type: 'assistant_delta', run_id: 'run-tab2', session_id: 's1', payload: { text: 'from the other tab' } } }`；
4. 断言：`stream.messages.value` 里出现一条 assistant 消息，`content` 含 `from the other tab`（恰好一份：
   `split('from the other tab').length - 1 === 1`）。

**Verify**：`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts -t 'still draws live deltas'` → PASS。

### Step 5：e2e——live 侧补 exactly-once，删过期注释

`frontend/e2e/network-retry.spec.ts`：

1. :36-37 之间（`toContainText(reply)` 之后、`partial answer` 计数断言之前）加：

```ts
  // Exactly once live too: the request socket streams the fourth attempt's
  // answer, and the observer's copy of the same fragments must not draw it a
  // second time once the turn settles.
  expect((await main.innerText()).split(reply).length - 1).toBe(1)
```

2. 删掉那段注释里的圆括号补充（现状 :44 的 `(The` 起、到 :47 的 `durable record.)` 止整段），保留它前面的
   "The persisted transcript a reload replays is the same story: exactly one answer, and the withdrawn text
   belongs to no attempt that survived."。

**Verify**：`FOREBRAIN_E2E_SPEC=e2e/network-retry.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。

## Test plan

- 新增 3–4 个单测：Step 1 复现（122 之后仍然必红）、Step 2.5 子代理同类（先复现，绿则不写）、Step 3 归属守卫、
  Step 4 无 send 时 observer 直投仍画。结构样板 :1004-1052 与 :3630-3655。
- 既有全量 `useChatStream.test.ts` 必须保持全绿——特别是断线重连（:1236）与 observer/auto-continue 组（:3619 起），
  以及 122 新加的 `assistant_delta → assistant_done → assistant_delta` 两块用例（本计划不许改它的期望）。
- e2e 复用 117 的 network-retry.spec.ts（假 provider netdrop：三次断流+第四次成功，正好覆盖"重试期间片段撤下
  +恢复后恰好一份"两条不变量）。

## Done criteria

全部满足才算完成：

- [ ] `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts` 全绿，含三个新用例（Step 2.5 若复现
      成立则四个；若不成立，报告里写明它当初是绿的）
- [ ] `cd frontend && pnpm typecheck` exit 0
- [ ] `FOREBRAIN_E2E_SPEC=e2e/network-retry.spec.ts scripts/acceptance/web_e2e.sh` 输出 `web e2e: PASS`
- [ ] `grep -n "draws a turn's answer twice" frontend/e2e/network-retry.spec.ts` 无输出
- [ ] `grep -c "sendOwnedRunIds.clear()" frontend/src/composables/useChatStream.ts` 输出 `2`（send 收尾 +
      `applySessionReset`；第三处即违反 Step 2.3）
- [ ] `grep -c "fragmentSuppressedBySend(" frontend/src/composables/useChatStream.ts` 输出 `2`（定义 + 一处调用；
      flush 里不该有第二处调用）
- [ ] `grep -c "sendOwnedRunIds.add(" frontend/src/composables/useChatStream.ts` 输出 `2`（`run_started`、
      `turn_started`）或 `3`（Step 2.5 的 spawn 映射也加了）——两种都合法，按实际报告
- [ ] `git status --porcelain` 只有上述三个 in-scope 文件（外加你未动过的既有未提交改动）

## STOP conditions

1. 前置检查第 1 条不满足（122 没落地）或第 2 条有输出，或 "Current state" 的**内容**与磁盘不符（行号漂移不算）。
2. Step 1 的复现用例**不红**（122 之后仍然应该是 `'single answersingle answer'`：这条用例不投 `assistant_done`）
   ——说明机制判断错了，停下报告观察到的实际行为。
3. Step 2 之后 :1236 断线重连用例、Step 3 归属守卫或 Step 4 守护用例红——"请求 socket 是 send-owned run 片段的
   唯一应用方"这一前提不成立，停下报告是哪条、红法是什么。
4. 修法看起来必须改 out-of-scope 文件（尤其网关）才能收敛。
5. 任一验证命令两次合理修复尝试后仍失败。
6. 为了让某个用例变绿，你想要在 :5612（`loadMessages` 的历史投影路径）或 `flushBufferedSessionEvents` 里再清空/
   再判断一次——停下报告：这两处都会让直播中的 run 失去归属，是 STOP 3 的同一个前提在别的入口被破坏。
7. 122 的 web 侧改动（`assistant_done` 分支、或它给封块语义带来的别的变化）与本计划 Step 2 的谓词语义打架——
   例如某个用例只有改掉 122 的期望才变绿。停下报告，不要改 122 的用例。

## Maintenance notes

- 将来若网关给 push-only 片段补上 id/sequence，`rememberObservedEvent` 的 `appliedEventIDs` 就能去重，本计划的
  谓词可以退役——但集合本身仍有价值（避免缓冲浪费），删之前先确认双路径都走 id 去重。
- **并行开发**：只有 `frontend/e2e/network-retry.spec.ts` 归本计划独有；`useChatStream.ts` 与
  `useChatStream.test.ts` 同时被 122（`assistant_done` 封块）、123（`response_notice`、撤下思考）、127（主代理当场
  审批的 web 回归测试）、129（`isSteerPreemptedRow` → `isHiddenRunRow`）改。**122 已先跑（owner 2026-10-26 决定）**，
  本计划基线就是它落地后的树；123/127/129 仍可与本计划并行，但**同一工作树内不行**，要分 worktree
  （锚点相差数百行，冲突只可能出现在测试文件的新增用例处）。**本计划要排在 127 之前**——127 Step 8 的 web 回归
  用例要求"正文只出现一次"，这条断言在本 bug 落地前会被它判红，而 127 授权"20 行以内的最小修正"，容易就地打个
  症状补丁。
- 122 落地后 `pushOnlyFragmentTypes` 必须六个成员（含 `assistant_done`），否则该片段的 observer 副本仍会在 flush
  时被应用（settle 后只表现为重复关块，肉眼多半看不出，但谓词本身就不完整了）。resume 的请求 socket 会把**别的
  run** 的事件转发进 `applyObservedEvent`（:5328）；一旦 `input_delivered` 续答 run 的 run id 进了
  `sendOwnedRunIds`，这条转发就会被这个谓词拦掉——动续答投影时回来复核。
- 122 之后 live 双画的**观感变了**：`assistant_done` 一到就把正文块封上（`open: false`），所以 flush 的第二遍不再
  拼成 `xxxx`，而是多出一个独立的正文块——用户看到的是同一段回答重复成两段。这也是为什么 122 之后仍然要修，而不是
  "看起来已经好了"。
- 评审重点看三处：① 丢弃谓词是否只作用于 send-owned run（第二标签页路径不受影响）；② `sendOwnedRunIds` 的两个
  清空点（send 收尾的 `stillMine` 守卫、`applySessionReset`）；③ 子代理 run id 只能经 `subagent_spawned` 的
  `parentRunId` 映射进来（Step 2.5），不得从 `turn_started` 认领。
- 已知但不在本计划范围内：settle 之后才投到的 observer 副本（跨连接迟到）仍会追加第二个块——这条竞态在改动前就
  存在，随 ① 的 id 化才能根治。
- 明确不修的相邻怪相：`eventProjection` 对 `input_delivered` 续答 run 仍指向旧消息的投影（重载与 live 的宿主
  可能不同）——独立问题，未立项。
