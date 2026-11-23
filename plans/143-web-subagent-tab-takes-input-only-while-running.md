# Plan 143：网页子代理标签与终端对齐——只在运行中可插话

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置 1（否则 STOP 1）**：`plans/README.md` 里 `142` 是 DONE；`grep -n 'subagentNotRunningCode' pkg/gateway/api_extra.go` 有结果；
> `grep -n '"/:id/subagents/:agent/withdraw"\|"/:id/subagents/:agent/compact"' pkg/gateway/api_extra.go` 无结果。
>
> **前置 2（否则 STOP 1）——目标文件必须干净**：`git status --porcelain frontend/src frontend/e2e scripts/acceptance` 无输出
> （写计划时 `82ab168` 上 135–138 批次正改着 `SubagentTab.vue`、`SubagentDrawer.vue`、`ChatView.vue`、`useChatStream.ts`、`api.ts`、`locales/index.ts`，须先由 owner 提交）。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 5）**：
> `cd frontend && pnpm vitest run && pnpm typecheck`；
> `FOREBRAIN_E2E_SPEC="e2e/subagent-*.spec.ts" scripts/acceptance/web_e2e.sh`（在仓库根目录跑；先读该脚本开头的注释了解它要的环境）。

## Status

- **Priority**: P1（owner 2026-10-26 指定）
- **Effort**: M
- **Risk**: MED
- **Depends on**: `142`（网关的新行为）、`141`（技能命令经网关送达）
- **Category**: direction
- **Planned at**: dev `82ab168`，2026-10-26
- **原型**：owner 2026-10-26 裁定**免原型**（输入框空闲时不渲染，不加新元素；规则与终端一致）

## Why this matters

142 已在引擎、网关、终端上落实 owner 的裁定（子代理视图只在子代理运行中可插话；空闲时隐藏输入框、引擎以 409 `subagent_not_running` 拒绝；
执行结束时没送达的消息退回主对话输入框；撤回、子代理 `/compact`、"排到之后"删除；"中断并发送"在同一次执行里接着跑）。
网页现在还在：空闲时显示输入框（发送会被 409 拒绝）、调用已删除的 `withdraw` / `compact` 端点、把执行结束时退回的消息塞回子代理标签的输入框、
在标签里提供 `queue-follow-up`。本计划让网页与终端一致（"TUI 是标准，网页对齐"）。

## Current state

- `frontend/src/components/chat/SubagentTab.vue`：`active = computed(() => subagentPhase(props.record.status) === 'active')`（`subagentPhase` 在 `src/lib/subagentStatus.ts:13`：done / failed / cancelled / interrupted 以外都算 active）。
  模板里 `.subagent-composer` 块包含：错误提示 `<Alert v-if="notice">`、`<AutoContinueBanner :state="autoContinue" @cancel="emit('cancel-auto-continue')" />`、
  `<PendingInputQueuePopover …>`、`<PromptInput>`（`ForebrainPromptTextarea` 带 `@queue-follow-up`、`@escape="emit('cancel-auto-continue')"`、`@interrupt-run="emit('escape-key')"`；
  底栏 `PromptInputFooter` 里依次是 `ComposerPlusMenu`、`ViewModeChip v-model="viewMode"`、右侧 `SubagentRunPill`、停止按钮（`v-if="active"`）、`PromptInputSubmit`）。
  根元素 `@keydown.esc="onEsc"` → `emit('escape-key')`。
- `frontend/src/views/ChatView.vue`：
  - `handleSubagentEscape(agentId)`："Esc in a subagent's tab (D1)"：`withdrawSubagentInput` → `interruptSubagentToSend` → `collapseSubagentDrawer()`；
  - `handleSubagentSubmit(agentId, message)`：`/compact` → `compactSubagentIn(id)`、`/context` → `openSubagentContext(id)`、内建非技能命令在主视图执行、其余 `sendToSubagent(id, sub, queued)`；
    `subagentDisposition`（`'steer' | 'queue'`）由标签的 `@queue-follow-up="subagentDisposition[record.agentId] = 'queue'"` 置位；
  - `compactSubagentIn`、`subagentComposer(id)?.restoreSubmission(...)`。
- `frontend/src/composables/useChatStream.ts`：
  - `sendToSubagent(agentId, submission, followUp = false)`（约 `:4202`）：`forebrainApi.subagentInput(sid, id, {message, attachments, mentionImages, mode})`，`catch` 一律 `returnSubagentDraft(id, submission)`；
  - `withdrawSubagentInput`（约 `:4256`）、`compactSubagent`（约 `:4275`）；
  - 子代理范围的 `case 'queued_input_released'`（约 `:3840`）：`releasedInputs(payload)` → `returnSubagentDraft(agentId, sub)`，`releasedNextInputs(payload)` → `sendToSubagent(agentId, merged, false)`；
  - `giveBack(submission, notSentBecause = '', notSentBecauseCode = '')`（约 `:4062`）把消息退回**主输入框**并带一个提示；`ChatView.vue` 约 `:2468` 的 `watch(returnedDraft, …)` 取出草稿与提示，
    提示码经 `formatProviderError({ code }, locale)`（`src/lib/providerError.ts:103`）翻成当前语言，标题 `t('chat.messageNotSent')`。
- `frontend/src/lib/api.ts`：`subagentInput(sessionId, agentId, {message, attachments?, mentionImages?, mode?})`（约 `:2439`）、`subagentWithdraw`（约 `:2465`）、`subagentCompact`（约 `:2473`）；
  错误类 `GatewayHttpError(status, body)`（`:73`），`message` 是响应体文本（网关的错误体是 `{"error": "...", "code": "..."}`）。
- `frontend/src/components/chat/AutoContinueBanner.vue`：取消按钮的提示 `t('autoContinue.cancelHint')`（zh "取消自动继续；在输入框里输入内容也会取消"，en "Cancel auto-continue; typing in the composer cancels it too"）。
- `frontend/src/locales/index.ts`：`chat.subagentCompactRunning`（zh `:1402`、en `:3224`）只服务子代理 `/compact`。
- e2e（`frontend/e2e/`，经 `scripts/acceptance/web_e2e.sh` 跑，假模型是 `scripts/acceptance/fake_provider.py`）：
  - `subagent-conversation.spec.ts` `'a subagent view talks to its own subagent'`：运行中发 `continue` + 中断（保留），后半段 `/compact` 期望压缩卡（要改），
    `'a subagent that lost the network is continued from its own tab'`（约 `:170`，空闲后从标签续跑——新规则下不存在）；
  - `subagent-drawer.spec.ts`：`"a tab's + is the composer's own…"`、`"a tab's own composer adds attachments and @ references…"` 等依赖标签输入框——确认它们的子代理在操作时处于运行中。
- 约定：组件优先 ai-elements-vue / shadcn-vue（owner 规则）；UI 文案简洁、中英对等；测试照 `src/components/chat/SubagentDrawer.test.ts`、`src/composables/useChatStream.test.ts` 的写法。

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 单测 | `cd frontend && pnpm vitest run <文件…>` | 与基线相比无新增失败 |
| 全量单测 | `cd frontend && pnpm vitest run` | 同上 |
| 类型 | `cd frontend && pnpm typecheck` | exit 0 |
| e2e（假模型） | `FOREBRAIN_E2E_SPEC="e2e/subagent-*.spec.ts" scripts/acceptance/web_e2e.sh` | 与基线相比无新增失败 |
| 死代码 | `grep -rn 'subagentWithdraw\|subagentCompact\|withdrawSubagentInput\|compactSubagent\|subagentDisposition\|subagentCompactRunning' frontend/src` | 无输出 |

## Scope

**In scope**：`frontend/src/components/chat/SubagentTab.vue`、`frontend/src/components/chat/AutoContinueBanner.vue`、`frontend/src/views/ChatView.vue`、
`frontend/src/composables/useChatStream.ts`、`frontend/src/lib/api.ts`、`frontend/src/lib/providerError.ts`、`frontend/src/locales/index.ts`、它们的单测、
`frontend/e2e/subagent-conversation.spec.ts`、`frontend/e2e/subagent-drawer.spec.ts`、`scripts/acceptance/fake_provider.py`（只在需要删除不再使用的子代理断网续跑分支时）。

**Out of scope**：Go 代码（142 已完成）；主对话的撤回、排队、`/compact`；`ForebrainPromptTextarea.vue`（主输入框也用它，不改它的行为，只是标签不再监听 `queue-follow-up`）；`SubagentDrawer.vue` 的抽屉结构。

## Steps

### Step 1：子代理标签——空闲时不渲染输入框

`SubagentTab.vue`：

1. `PendingInputQueuePopover` 与 `PromptInput` 只在 `active` 时渲染（`v-if="active"`）。`Alert`（提示）与 `AutoContinueBanner` 照旧渲染。
2. 空闲时在原底栏的位置渲染一行工具条，只放原底栏里与"输入"无关的两个控件，顺序与原底栏一致：左 `ViewModeChip v-model="viewMode"`，右 `SubagentRunPill :record="record"`
   （同样的组件、不加边框和背景；`data-testid="subagent-idle-bar"`）。不渲染 `ComposerPlusMenu`、停止按钮、发送按钮。
3. 去掉 `@queue-follow-up="emit('queue-follow-up')"` 与 emits 里的 `'queue-follow-up'`；Tab 在标签里不再有"排到之后"的含义（`ForebrainPromptTextarea` 不收到监听时按它自己的默认处理）。
4. `AutoContinueBanner` 加 prop `typingCancels`（默认 `true`），为 `false` 时取消按钮提示用新键 `autoContinue.cancelHintEscOnly`（zh "取消自动继续"，en "Cancel auto-continue"）；
   标签里传 `:typing-cancels="active"`。
5. 单测（`SubagentTab` 已有测试文件就加在里面，没有就在 `SubagentDrawer.test.ts` 里挂载标签）：`record.status = 'done'` 时没有 `textarea`、有 `[data-testid="subagent-idle-bar"]` 且其中有阅读模式切换；
   `status = 'running'` 时有 `textarea`、没有 idle bar；`AutoContinueBanner` 在 `typingCancels=false` 时提示文字为新键。

**Verify**：相关单测通过；`pnpm typecheck` exit 0。

### Step 2：Esc、发送、退回

`ChatView.vue`：

1. `handleSubagentEscape(agentId)`：删掉撤回那段；顺序改为：`interruptSubagentToSend(id)` 为 true 就返回 → 该标签有待触发的自动续跑（`autoContinueForView(id)` 有值）就 `cancelAutoContinue(id)` 并返回 → `collapseSubagentDrawer()`。
   注释改为 "the same meanings in the same order as the terminal: interrupt to send, cancel its pending continuation, leave"。
2. `handleSubagentSubmit`：删 `/compact` 分支与 `compactSubagentIn`（`/compact` 现在由网关以 `subagent_view_command` 拒绝，页面照处理其他主对话命令那样显示提示）；
   删 `subagentDisposition` 及其清理；`sendToSubagent(id, sub)` 不再带 queued 参数。

`useChatStream.ts`：

3. `sendToSubagent(agentId, submission)`：去掉 `followUp` 参数与请求体的 `mode`；`catch (err)`：若 `err instanceof GatewayHttpError && err.status === 409 && err.message.includes('subagent_not_running')`，
   调 `giveBack(submission, '', 'subagent_ended')`（退回主输入框并提示）；其余错误照旧 `returnSubagentDraft(id, submission)`。
4. 子代理范围的 `case 'queued_input_released'`：`releasedInputs(payload)` 与 `releasedNextInputs(payload)`（后者 142 之后恒为空，仍一并处理以兼容）都经 `releasedSubmission` 转成提交，
   逐个 `giveBack(sub, '', 'subagent_ended')`；不再 `returnSubagentDraft`、不再 `sendToSubagent`。注释写明：执行结束时子代理已空闲，不接受消息，没送达的退回主输入框（owner, 2026-10-26）。
5. 删 `withdrawSubagentInput`、`compactSubagent` 及其导出；`api.ts` 删 `subagentWithdraw`、`subagentCompact`，`subagentInput` 的 body 类型去掉 `mode`。
6. `providerError.ts`：`formatProviderError` 加一支 `if (detail.code === 'subagent_ended') return tr('runError.subagentEnded')`（放在其它 `runError.*` 分支旁）；
   `locales/index.ts` 加 `runError.subagentEnded`（zh "子代理已结束，消息没有送达，已放回输入框。"，en "The subagent finished before your message reached it; it's back in the composer."），
   删 `chat.subagentCompactRunning`（中英两处）。
7. 单测（`useChatStream.test.ts`）：
   - 子代理的 `queued_input_released`（`agent_id` 为某子代理、`inputs` 一条）→ `returnedDraft` 有它、`takeReturnedNoticeCode()` 为 `'subagent_ended'`，且没有发出 `subagentInput` 请求；
   - `sendToSubagent` 收到 409 `{"code":"subagent_not_running"}` → 同上退回主输入框；收到其它错误 → 仍回子代理标签（`takeSubagentReturnedDraft`）。
   - `ChatView.test.ts`：标签 Esc 在有待触发自动续跑时调 `cancelAutoContinue`、无则收起抽屉；不再调用 withdraw。

**Verify**：`cd frontend && pnpm vitest run` 与基线相比无新增失败；`pnpm typecheck` exit 0；"死代码" grep 无输出。

### Step 3：e2e

1. `subagent-conversation.spec.ts`：
   - `'a subagent view talks to its own subagent'`：运行中发送 + 中断的前半段保留；`/compact` 那段改为与 `/new` 相同的期望（标签里出现 "Run this from the main view" 或页面对 `subagent_view_command` 的提示文案，以实际为准），不再期望压缩卡；
     结尾在子代理结束后补一段：标签里 `textarea` 数量为 0、`[data-testid="subagent-idle-bar"]` 可见。
   - 删除 `'a subagent that lost the network is continued from its own tab'`（空闲后从标签续跑的流程已不存在）。若 `scripts/acceptance/fake_provider.py` 里只为它服务的分支因此无人使用，一并删除；仍被前一个测试用到的不动。
2. `subagent-drawer.spec.ts`：凡在标签输入框里打字/加附件的用例，确认操作时子代理在运行（看用例里用的假模型标记会不会让子代理挂住）；若某用例在子代理结束后才操作输入框，改为在运行中操作，或改断言为"空闲时没有输入框"。不要为了让旧用例通过而放宽 `active` 的判断。

**Verify**：`FOREBRAIN_E2E_SPEC="e2e/subagent-*.spec.ts" scripts/acceptance/web_e2e.sh` 与基线相比无新增失败；删掉的用例不算。

### Step 4：真机看一眼（可选但推荐）

用 `scripts/acceptance/web_e2e.sh` 的同一套环境打开页面，派发一个会立即失败的子代理（例如 `[[e2e:subagent-net]]` 让它断网失败），点卡片任务行打开标签：
空闲时没有输入框、底部有阅读模式切换与运行状态胶囊；截图存 `$TMPDIR/p143-idle.png` 附在报告里。

## Test plan

- 新单测：标签空闲/运行两态、横幅提示两态、子代理 `queued_input_released` 退回主输入框、409 退回主输入框、Esc 顺序。
- e2e：`subagent-conversation.spec.ts` 改 1 删 1；`subagent-drawer.spec.ts` 按需调整。
- 回归：全量 vitest、typecheck、`e2e/subagent-*.spec.ts`。

## Done criteria

- [ ] `pnpm vitest run` 与 `pnpm typecheck` 通过（与基线相比无新增失败）
- [ ] "死代码" grep 无输出；`grep -rn "queue-follow-up" frontend/src/components/chat/SubagentTab.vue` 无输出
- [ ] e2e `subagent-*` 与基线相比无新增失败
- [ ] 中英 locale 键成对（`runError.subagentEnded`、`autoContinue.cancelHintEscOnly`）
- [ ] `git status --porcelain` 里本计划新增的改动只落在 In scope；`plans/README.md` 状态行已更新（除非评审方说由其维护）

## STOP conditions

1. 前置不满足（142 未完成，或网页文件上还有别人未提交的改动）。
2. `subagentPhase` 的 `active` 与终端"子代理在跑"的判断明显不一致（例如等审批时被判为非 active）——报告，不要自己改状态映射。
3. e2e 需要改 Go 代码或 `scripts/acceptance/web_e2e.sh` 本身才能跑通。
4. 任何一步需要改 In scope 以外的文件。
5. 基线跑不起来。

## Maintenance notes

- 网页与终端用同一条规则：子代理在跑才有输入框；没送达的消息回主输入框，提示码 `subagent_ended`。改一端必须改另一端。
- `ForebrainPromptTextarea` 的 `queue-follow-up` 仍服务主输入框；子代理标签只是不监听它。
