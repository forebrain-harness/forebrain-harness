# Plan 117：web 把网络重试状态显示在输入框上方的状态行，删除重试卡与网络错误块

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **owner 2026-10-24 裁决：不做原型，直接开发落地。** 按本计划 “Why this matters” 的文案表与规则和“实施步骤”各步实现，不改 `app-preview.html`。
>
> **前置**：`plans/115-network-retry-resend-and-backoff.md` 已完成。先跑
> `grep -n "dropOpenStreamedBlocks" frontend/src/composables/useChatStream.ts` 应有结果、`grep -n "Cleared" pkg/event/run_events.go` 应有结果；否则 STOP 1。
>
> **Drift check（先跑）**：
> `git diff --stat cb4146e -- frontend/src/components/chat/RunStatusLine.vue frontend/src/components/chat/SubagentTab.vue frontend/src/components/chat/TransportRetryCard.vue frontend/src/components/chat/SubagentConversation.vue frontend/src/views/ChatView.vue frontend/src/views/WorkshopView.vue frontend/src/locales/index.ts scripts/acceptance/fake_provider.py`
> 预期**无输出**（`useChatStream.ts` 与其测试已被 115 改过，按符号对照现场）。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: plans/115-network-retry-resend-and-backoff.md
- **Category**: direction（owner 2026-10-24 定稿：重试状态只在状态行，不进对话消息卡）
- **Planned at**: commit `cb4146e`，2026-10-24

## Why this matters

网络重试时网页现在在对话里插入“网络错误”块和带转圈的重试卡（`TransportRetryCard.vue`），主对话、子代理标签、技能工坊三处都有。定稿的表现：
**对话里不出任何重试相关的块**；重试期间，输入框正上方的状态行（主对话的 `RunStatusLine`、子代理标签里的 `RunStatusLine`）整行换成重试状态，
重试成功或回合结束后恢复。与终端（`116`）同一语义。最终文案：

| 情况 | zh | en |
|---|---|---|
| 第 1、2 次（`attempt < min(3, total)`） | `网络错误 · 8 秒后重试 · 第 2/10 次` | `Network error · Retrying in 8s · attempt 2/10` |
| 第 3 次起（错误原文非空时） | `read tcp 10.0.0.2:59383->…: operation timed out · 8 秒后重试 · 第 3/10 次` | `read tcp …: operation timed out · Retrying in 8s · attempt 3/10` |
| 倒计时到点、请求在途 | `… · 0 秒后重试 · 第 3/10 次` | `… · Retrying in 0s · attempt 3/10` |

> 2026-11-04 修订：错误原文含请求方法、URL、地址等底层细节，已取消；每次都只写通用原因，`API error` / `Network error`。

- 倒计时向上取整到秒，每秒走一次：`< 60s` → zh `{n} 秒` / en `{n}s`；`60s–5m` → zh `{m} 分 {s} 秒` / en `{m}m {s}s`；`≥ 5m` 只写最高位：zh `{n} 分钟`/`{n} 小时`/`{n} 天`，en `{n}m`/`{n}h`/`{n}d`。
- 状态行原有的转圈图标保留；重试期间不显示耗时、`Esc 中断`、计划进度、首 token。
- 原因（`网络错误` 或错误原文）是这一行里**唯一会被截断**的部分：按宽度省略号截断，鼠标悬停 `title` 显示全文；倒计时与次数始终完整。
- 次数用完时没有额外的块，回合以既有的回合错误展示结束（文案 `network error, gave up after N retries: …` 来自引擎）。

## Current state

**前端代码**：

- `frontend/src/components/chat/RunStatusLine.vue` — props `label`、`plan?`、`tail?`；`Loader2` 转圈；`.run-status-tail` 是唯一可截断段。
- `frontend/src/views/ChatView.vue:346-353` 渲染 `RunStatusLine`；`:2039-2049` `runLine` computed：调 `formatRuntimeStatusLabel({...status, firstTokenMs: undefined}, t)`，tail 为首 token。
- `frontend/src/components/chat/SubagentTab.vue:34-41` 渲染子代理的 `RunStatusLine`（`label = t('chat.subagentWorking', { duration })`）；`:242-246` `duration` 用组件内每秒走的 `nowMs`。
- `frontend/src/composables/useChatStream.ts`：
  - `:1465-1483` `RuntimeStatus`（含 `kind: 'idle' | 'working' | 'reconnecting'`、`attempt`、`maxAttempts` 等）；
  - `:2238-2264` `formatRuntimeStatusLabel`；`:2676-2714` `startRuntimeStatus`（每秒刷新 `elapsedMs`）、`clearRuntimeStatus`；
  - `:1244-1290` `SubagentTranscript`；`:3194` `updateSubagent`；
  - 卡片：`TimelineBlock` 的 `| { kind: 'retry'; … }`（`:1200-1217` 一带）、`TransportRetryBlock`（`:1233`）、`withoutLiveRetryBlocks`（`:1347`，被 `appendStreamedText` 与 subagent_ended / turn_completed / turn_error / turn_cancelled 共 5 处调用）、
    `applyTransportRetryBlocks`（追加 `{ kind: 'error', id: 'transport-retry-error' }` 与 retry 块）、`replaceOrAppendById`、`truncateTransportError`；
  - 主对话 `case 'transport_retry'` 与子代理 `case 'transport_retry'`（115 已加 `payload.cleared` 早返回、`dropOpenStreamedBlocks`、`fullAnswer` 去尾）。
- 卡片渲染：`ChatView.vue:227-229`、`SubagentConversation.vue:104-110`、`WorkshopView.vue:40` 的 `<TransportRetryCard v-else-if="block.kind === 'retry'" …>`；
  三处 `import TransportRetryCard`（`ChatView.vue:660`、`SubagentConversation.vue:166`、`WorkshopView.vue:225`）；组件文件 `frontend/src/components/chat/TransportRetryCard.vue`（127 行）。
- 文案：`frontend/src/locales/index.ts:781-784`（zh）与 `:2580-2583`（en）的 `chat.transportRetry.errorTitle/retrying/retryingNow/stopped`。
- 测试：`frontend/src/composables/useChatStream.test.ts` 的三个 transport retry 用例（`renders the transport retry…`、`freezes the retry card…`、`scopes a subagent transport retry…`）；
  `frontend/src/views/ChatView.test.ts:550` 一带有 `runtimeStatus` 的 mock 写法。
- 浏览器验收：`scripts/acceptance/web_e2e.sh`（`FOREBRAIN_E2E_SPEC=e2e/<file>.spec.ts scripts/acceptance/web_e2e.sh` 只跑指定 spec），
  fake provider 以 `reply` 模式运行，用户消息里带标记触发特殊行为（例：`scripts/acceptance/fake_provider.py:111` `SUBAGENT_NET_MARKER = "[[e2e:subagent-net]]"`、`serve_subagent_net` `:155-195`）；
  spec 写法参照 `frontend/e2e/subagent-conversation.spec.ts`（`signIn`、`shot` 来自 `./support`）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 单测 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts` | 全部通过 |
| 全部前端单测 | `cd frontend && pnpm test` | 全部通过 |
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0 |
| 浏览器验收 | `FOREBRAIN_E2E_SPEC=e2e/network-retry.spec.ts scripts/acceptance/web_e2e.sh` | 1 passed |

## Scope

**In scope**：`useChatStream.ts`、`useChatStream.test.ts`、`RunStatusLine.vue`、`ChatView.vue`、`ChatView.test.ts`、
`SubagentTab.vue`、`SubagentConversation.vue`、`WorkshopView.vue`、`locales/index.ts`、删除 `TransportRetryCard.vue`、
`scripts/acceptance/fake_provider.py`（只加 netdrop 标记）、新建 `frontend/e2e/network-retry.spec.ts`。

**Out of scope**：Go 代码（引擎、网关、TUI）；`dropOpenStreamedBlocks` 与 `fullAnswer` 去尾逻辑（115 已定）；
`RuntimeStatus` 的 `reconnecting`（WebSocket 重连）状态；网页重新连接时恢复重试状态（重试状态只推不存，刷新后不显示，属已知限制）。

## 实施步骤

### Step B1：状态数据

`useChatStream.ts`：

1. 新类型（放在 `RuntimeStatus` 前）：

```ts
/** The network retry a run is waiting on: what the status line shows instead of "Working". */
export interface TransportRetryStatus {
  attempt: number
  total: number
  /** When the next attempt fires, epoch ms. */
  deadline: number
  /** The transport's own sentence; shown from the third attempt on. */
  error: string
}
```

2. `RuntimeStatus` 加 `retry?: TransportRetryStatus`；`SubagentTranscript` 加 `retry?: TransportRetryStatus`。
3. 主对话 `case 'transport_retry'`：`payload.cleared` 时 `runtimeStatus.value = { ...runtimeStatus.value, retry: undefined }` 后 return；
   否则在 115 的逻辑之后设置 `runtimeStatus.value = { ...runtimeStatus.value, retry: { attempt: Number(payload.retry ?? 0), total: Number(payload.total ?? 0), deadline: Date.now() + Math.max(0, Number(payload.waitMs ?? 0)), error: String(payload.error ?? '').trim() } }`。
4. 子代理 `case 'transport_retry'`：同理写/清 `entry.retry`（通过 `updateSubagent`）；`subagent_ended` 分支里同时清 `retry: undefined`。
5. 主对话 `turn_completed` / `turn_error` / `turn_cancelled` 设置 `runtimeStatus.value` 处加 `retry: undefined`；`startRuntimeStatus`、`clearRuntimeStatus` 新建的对象天然不带 `retry`。

### Step B2：删除卡片与网络错误块

1. `useChatStream.ts`：删 `TimelineBlock` 的 `retry` 成员、`TransportRetryBlock`、`withoutLiveRetryBlocks`（及其 5 处调用——调用处直接用原来的参数）、
   `truncateTransportError`；`applyTransportRetryBlocks` 改成只做 `dropOpenStreamedBlocks(blocks)`（或在两处调用点直接调 `dropOpenStreamedBlocks` 并删掉该函数）；
   若 `replaceOrAppendById` 不再被使用则删掉。不再生成 `id: 'transport-retry-error'` 的 error 块。
2. 删文件 `frontend/src/components/chat/TransportRetryCard.vue`；删 `ChatView.vue`、`SubagentConversation.vue`、`WorkshopView.vue` 里的 `TransportRetryCard` 分支与 import（含 `SubagentConversation.vue:104` 上方的注释）。
3. `locales/index.ts`：删除 zh、en 各 4 条 `chat.transportRetry.*` 旧 key，新增（zh / en）：

| key | zh | en |
|---|---|---|
| `chat.transportRetry.networkError` | `网络错误` | `Network error` |
| `chat.transportRetry.retryIn` | `{countdown}后重试 · 第 {n}/{total} 次` | `Retrying in {countdown} · attempt {n}/{total}` |
| `chat.transportRetry.seconds` | `{n} 秒` | `{n}s` |
| `chat.transportRetry.minutesSeconds` | `{m} 分 {s} 秒` | `{m}m {s}s` |
| `chat.transportRetry.minutes` | `{n} 分钟` | `{n}m` |
| `chat.transportRetry.hours` | `{n} 小时` | `{n}h` |
| `chat.transportRetry.days` | `{n} 天` | `{n}d` |

**Verify B2**：`grep -rn "TransportRetryCard\|kind === 'retry'\|transport-retry-error\|withoutLiveRetryBlocks\|transportRetry.retryingNow\|transportRetry.stopped" frontend/src` → 无输出

### Step B3：状态行渲染

1. `useChatStream.ts` 新增并导出：

```ts
/** The status line while a network retry is pending: the reason (the only part that may be cut) and the countdown with the attempt. */
export function formatTransportRetryLine(retry: TransportRetryStatus, tr: TranslateFn = translate, now = Date.now()): { reason: string; label: string }
export function formatRetryCountdown(ms: number, tr: TranslateFn = translate): string
```

   - `reason`：`retry.attempt >= Math.min(3, retry.total) && retry.error ? retry.error : tr('chat.transportRetry.networkError')`；
   - `label`：`tr('chat.transportRetry.retryIn', { countdown: formatRetryCountdown(retry.deadline - now, tr), n: retry.attempt, total: retry.total })`；
   - `formatRetryCountdown`：`secs = Math.max(0, Math.ceil(ms / 1000))`；`< 60` → `seconds`；`< 300` → `minutesSeconds`（`m = floor(secs/60)`、`s = secs%60`）；
     否则只写最高位：`≥ 86400` → `days`，`≥ 3600` → `hours`，否则 `minutes`（向下取整）。
2. `RunStatusLine.vue` 加可选 prop `reason?: string`：有值时作为第一段 `<span class="run-status-seg run-status-reason" :title="reason">{{ reason }}</span>`，
   其后是 `·` 与 `label`；`.run-status-reason` 用等宽字体并 `overflow: hidden; text-overflow: ellipsis; min-width: 0;`，此时 `label` 不截断。更新组件顶部注释。
3. `ChatView.vue` `runLine`：`status.retry` 存在时返回 `{ ...formatTransportRetryLine(status.retry, t), plan: undefined, tail: '' }`，模板把 `runLine.reason` 传给 `RunStatusLine`。
   倒计时随 `runtimeStatus` 每秒刷新（`startRuntimeStatus` 的定时器）重算；若 computed 不随之重算，在 `formatTransportRetryLine` 调用里显式读 `status.elapsedMs` 以建立依赖。
4. `SubagentTab.vue`：`record.retry` 存在时 `RunStatusLine` 改用 `formatTransportRetryLine(record.retry, t, nowMs.value)` 的 `reason`/`label`，不传 `plan`/`tail`。

### Step B4：单测

- `useChatStream.test.ts`：
  - `renders the transport retry…`：不再断言 error/retry 块；改为断言重试后 `stream.runtimeStatus.value.retry` 为 `{ attempt: 1, total: 3, … }`，消息 blocks 里没有 `kind: 'error'`；
    第二次 transport_retry 后 `attempt === 2`；收到 `{ cleared: true }` 后 `retry` 为 undefined；其余（死掉的文字撤下、恢复后只有一份答案）沿用 115 的断言。
  - `freezes the retry card…` 改为 `clears the retry status when the turn ends`：transport_retry 后 `turn_cancelled`，`runtimeStatus.value.retry` 为 undefined。
  - 子代理用例：断言 `entry.retry.attempt === 1`、blocks 里没有 retry/error 块；`cleared` 后 `entry.retry` 为 undefined。
  - 新增 `formatRetryCountdown` 表驱动：`500→'1s'`、`8000→'8s'`、`59200→'1m 0s'`、`65000→'1m 5s'`、`360000→'6m'`、`9000000→'2h'`、`93600000→'1d'`、`-1000→'0s'`（en）；zh 抽两条：`8000→'8 秒'`、`65000→'1 分 5 秒'`。
  - 新增 `formatTransportRetryLine`：attempt 2/10 → reason `Network error`；attempt 3/10 带 error → reason 为原文；total=2 时 attempt 2 → 原文。
- `ChatView.test.ts`：按 `:550` 一带的 mock 写法加一例：`runtimeStatus` 带 `retry` 时 `[data-testid="run-status-line"]` 文本含 `Retrying in` 与 `attempt 2/10`、不含 `Working`。

**Verify B4**：单测命令全部通过；`pnpm typecheck` exit 0。

### Step B5：浏览器验收

1. `scripts/acceptance/fake_provider.py`：在 `SUBAGENT_STEER_MARKER` 旁加 `NETDROP_MARKER = "[[e2e:netdrop]]"`；在 `reply` 模式的分发处（`:67-69` 一带，`serve_subagent_net` 之后）加 `serve_netdrop_marker(request)`：
   最后一条人类消息含该标记时，前 3 次请求（模块级计数）返回 200 + `text/event-stream`，写一个 `{"role":"assistant","content":"partial answer that never"}` 的 chunk 后
   **不写 finish 帧**直接关闭连接（关闭写法照抄同文件 `serve_netdrop` `:241-270`）；第 4 次起返回 `False` 走正常回复。
2. 新建 `frontend/e2e/network-retry.spec.ts`（照 `subagent-conversation.spec.ts` 的 `signIn`/发送消息写法）：
   - 发送 `hello [[e2e:netdrop]]`；
   - `expect(page.locator('[data-testid="run-status-line"]')).toContainText(/Retrying in|后重试/)`（等待 ≤ 5s）；
   - 等回合结束；断言页面里 `partial answer that never` 出现 0 次、正常回复文本出现 1 次、没有 `.transport-retry-card` 或 `[data-testid^="transport-retry"]` 元素；
   - `shot(page, 'network-retry-status')` 留一张重试中的截图（在状态行出现时截）。

**Verify B5**：`FOREBRAIN_E2E_SPEC=e2e/network-retry.spec.ts scripts/acceptance/web_e2e.sh` → `1 passed`；截图路径附在报告里。

## Done criteria

- [ ] `grep -rn "TransportRetryCard\|kind === 'retry'\|transport-retry-error\|withoutLiveRetryBlocks" frontend/src` 无输出；`TransportRetryCard.vue` 已删除
- [ ] `pnpm test` 全部通过；`pnpm typecheck` exit 0
- [ ] `network-retry.spec.ts` 通过，截图已附
- [ ] 实现与 “Why this matters” 的文案表和规则逐项一致（文案、分隔符、截断位置、子代理标签）
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置检查不满足（115 未完成）或 Drift check 摘录与现场不符。
2. `runtimeStatus` 每秒刷新后状态行倒计时仍不走（computed 依赖问题），两次修正无效。
3. e2e 里状态行在 5 秒内没出现，且 fake provider 日志显示确有 3 次断流——回报日志，不要放宽等待时间以外的断言。

## Maintenance notes

- 状态行的重试数据只来自 `transport_retry` 事件（`cleared` 清除）；它是推送状态，刷新页面后不恢复。
- `118` 会在 payload 里加 HTTP 状态码，届时 HTTP 错误的泛称改为 `API error`/`接口错误`，改动点是 `formatTransportRetryLine` 的 `reason`。
- `RunStatusLine` 的 `reason` 段是唯一会被截断的段；以后加段时保持倒计时与次数不被截断。
