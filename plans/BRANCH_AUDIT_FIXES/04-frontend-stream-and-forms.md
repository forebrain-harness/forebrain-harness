# 04 前端：跨会话流竞态收口、ask 表单白名单校验、cron 设置错误口径

来源发现：P-3、P-4（pre-existing，本分支已修同族另一半 `loadMessages` 竞态，这两条是同一主题的剩余面）、I-10、I-11（introduced）。
涉及文件：`frontend/src/composables/useChatStream.ts`（+`useChatStream.test.ts`）、`frontend/src/components/chat/PendingActionsPanel.vue`、`frontend/src/components/settings/CronSettingsTab.vue`（+test）、`frontend/src/locales/index.ts`。

## 前置检查（漂移核对）

1. `useChatStream.ts`：`send()` 入口已算出 `sendSession`（约 2810 行 `String(options?.sessionId ?? sessionId.value ?? '').trim()`）；`finally` 无条件清 `isStreaming/streamAbortController/activeRunId`（约 3247-3256 行）；`released` 续发消息的 options 用**此刻**的 `sessionId.value`（约 3268 行）、随后 `send(next.text, next.options)`（约 3282 行）；`applySessionReset` 把 `heldBeforeRun` 还给 composer 且明确注释"被离开会话的消息不属于任何会话"（约 1917-1925 行）；`loadMessages` 已有 `lastSendEpochBySession` 快照丢弃（约 2797-2811 行）。
2. `PendingActionsPanel.vue`：`JSON.parse(a.payloadJson) as AskAction['form']` 一次 `as` 直转（约 257 行），`submitAsk` 里 `a.form.questions.map`（约 299 行）；解析已有 try/catch（268-270 行），缺的是形状校验。
3. `CronSettingsTab.vue`：三处 `cause instanceof Error ? cause.message : String(cause)`（约 100、119、135 行）；对照范式 `PendingActionsPanel.vue:286-288`（`GatewayHttpError` → status 本地化，兜底 `getErrorMessage`，`frontend/src/lib/api.ts:7`）。

## D1 [CORRECTNESS] 收口 useChatStream 的跨会话流竞态（P-3 + P-4，一并修）

**影响回顾**：(P-3) A 会话的排队消息在 A 运行结束释放时，若用户已切到 B，会作为 **B 的回合**发出；(P-4) A 的 `send` 收尾无条件清全局 `isStreaming/streamAbortController/activeRunId`，抹掉 B 的运行状态行、提前解锁 composer、使 `cancel()` 失效。

**改动**（`useChatStream.ts`，两件事）：

1. **释放消息归属发起会话（P-3）**：`released` 构造处（约 3268 行）`options: { sessionId: sendSession, … }`——用本 send 入口的 `sendSession`，不用 `sessionId.value`。
2. **会话已离开 → 归还 composer，不自动续发**：在 `finally` 后的 following 决策处（约 3263-3283 行）加一条与 `heldBeforeRun` 既有注释同一语义的判断——`sessionId.value !== sendSession`（用户已切走）时，`following`（含 released）整体 `giveBackHeld(following)` 归还 composer，不 `send(next)`。用户回到 A 时看到它们待发，与"离开会话的消息回到 composer"的既有先例一致。留在原会话时行为完全不变。
3. **收尾清理只清自己的（P-4）**：本 send 创建 controller/runId 的赋值点（约 2855 行一带）捕获局部 `myController`/`myRunId`；`finally` 改为属主判断：
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
（`flushBufferedSessionEvents` 照旧无条件执行；属主不等时说明另一 send 已接管全局状态，本 send 不得触碰。）执行前通读 `send()` 全函数（约 2803-3290 行）确认 held/queue 递归路径没有第二个赋值点漏捕获。

**语义取舍（已定，不重开）**：曾考虑"切走后把 released 消息绑定原会话在后台续发"，否——与代码自身"离开会话的消息还给 composer"先例相悖，且后台续发会再次制造全局单例流状态的多会话竞争。

**缓存影响**：无（纯客户端状态机）。

**测试**（`useChatStream.test.ts`，沿用现成夹具）：
1. 同会话释放：released follow-up 以发起 sessionId 发出（现有用例不回归 + 断言 sessionId 来源）。
2. 释放前切到 B：following 全部归还 composer，无第二次 send。
3. A 流式中切 B 并发送：A 的 `finally` 不清 B 的 `isStreaming`/controller；A 结束后 B 的 `cancel()` 仍有效。
4. 正常单会话路径全部回归。

## D2 [CORRECTNESS] ask 表单 payloadJson 白名单校验（I-10）

**改动**（`PendingActionsPanel.vue` 的 `loadPending`）：`JSON.parse` 之后、`asks.push` 之前校验形状（照 `frontend/src/lib/lspRecommendation.ts` 的白名单解析范本）：

- `form` 为对象且 `Array.isArray(form.questions)`；
- 每题 `typeof q.id === 'string' && typeof q.prompt === 'string' && Array.isArray(q.options)`。

不合格的 ask 跳过（与现有 catch 的静默跳过一致，不新增提示位）；`ensureModels(a.id, form)` 只对合格表单调。`as` 直转删除，校验后再收窄类型。

**测试**（`PendingActionsPanel.test.ts`）：畸形 payload（缺 questions / questions 非数组 / 题缺 options）→ 不进 pendingAsk、不抛；合法 payload 现行为不变。

## D3 [文案] CronSettingsTab 错误提示走统一口径（I-11）

**改动**：

1. `CronSettingsTab.vue` 三处 catch 改为 `PendingActionsPanel` 同款双分支：
```ts
error.value = cause instanceof GatewayHttpError
  ? t('cronSettings.failedStatus', { status: cause.status })
  : getErrorMessage(cause)
```
（`GatewayHttpError` 与 `getErrorMessage` 从 `frontend/src/lib/api.ts` 导入；若 `getErrorMessage` 已内建 status 本地化，则单用 `getErrorMessage(cause)`，以现场实现为准，二选一、不两套并存。）
2. `frontend/src/locales/index.ts` 新增键（zh 前半、en 后半，键名一致）：`cronSettings.failedStatus`（中文如"保存保留期设置失败（{status}）" / 英文 "Saving the retention setting failed ({status})"），加载与重置共用此键或按需拆 `loadFailedStatus`——从简，一个键。
3. 网关侧稳定 code 属后端改造，**不在本期**（README"明确不做"未列它，此处记录：现行兄弟端点均以 status 本地化，本计划对齐现状即可）。

**测试**（`CronSettingsTab.test.ts`）：失败路径断言展示本地化文案、不出现原始 `error.message`；成功路径回归。

## D4 [CORRECTNESS] 执行期发现的同族残留：send() socket op 路径的终态写入补属主判断

**发现**：D1-3 修了 `finally`，但 send() 自己 socket 的 op 分支里仍有五处无守卫的全局写：`slash_reply` 收尾（约 2910 行）、`run_completed`（约 2956）、`run_error`（约 2971）、`turn_withdrawn`（约 2997）、`run_cancelled`（约 3008）都直接 `isStreaming.value = false`。A 流式中切到 B 并发送后，A 的 socket 迟到的终态 op 会把 B 的流状态提前解锁——P-4 的另一半。观察者路径（`applyObservedEvent`）已有会话过滤（1746 行），无需动。

**改动**（`useChatStream.ts`）：五处的 `isStreaming.value = false` 都包进 D1-3 同一属主判断 `if (streamAbortController === myController)`（`myController` 与这些 op 分支同在 send() 闭包内）；各分支自身的 `eventState`/消息投影簿记保持无条件——那是本 send 自己的账。

**缓存影响**：无。

**测试**（`useChatStream.test.ts`）：A 流式中切 B 并发送（B 持有 controller）→ A 的 socket 投递 `run_completed` → `isStreaming` 仍为 true、B 的 `cancel()` 仍有效；A 结束后由 B 自身收尾正常清状态。

## 验证（本计划全部完成后）

```bash
cd frontend && corepack pnpm test
cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json
scripts/acceptance/web_e2e.sh    # 假模型网页真机，最后一行 web e2e: PASS
```

D1 的三个新用例在浏览器手工过一遍（双标签或两窗口模拟切会话）作为真机佐证，截图/录屏留档不强制。
