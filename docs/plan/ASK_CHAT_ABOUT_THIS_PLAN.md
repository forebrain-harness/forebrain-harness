# Plan: user_interaction 的「Chat about this」按 Claude Code 语义与卡片落地（TUI + web 一次修完）

> 执行者须知：本计划自包含。按 Steps 顺序执行，每步跑完 Verify 并确认预期输出再进下一步；
> 命中 STOP conditions 就停下来报告，不要临场发挥。**不要 commit**（owner 手动审阅提交），改动留在工作区。
>
> 落盘约定：审批通过后，把本文件原样复制到仓库 `docs/plan/ASK_CHAT_ABOUT_THIS_PLAN.md`
> （本仓库计划文件的惯例位置），再开始改源码；计划文件不 commit。
>
> 原始报告材料（评审已内联到本文件，执行者不必打开）：
> - 截图 1（坏的）：`● Denied` + `└ (no output)`，红色。
>   `.forebrain/attachments/forebrain-clipboard-5739-2767539375.png`
> - 截图 2（要对齐的 Claude Code）：`● User declined to answer questions` + `· 问题 (选项 / 选项)`。
>   `.forebrain/attachments/forebrain-clipboard-5739-3012019590.png`
> - 逆向对象：`/opt/homebrew/bin/claude` = Claude Code **2.1.220**（cask claude-code）。

## Status

- Priority: P1
- Effort: L（TUI + 引擎 + web 三面）
- Risk: MED（新增一个 engine 决策 `clarify`；不改状态机、不改 DB schema、**不新增 API 字段**，只新增一个 choice 值）
- Depends on: none
- Category: bug（交互语义 + 卡片 UI 对齐）
- Planned at: commit `a1da14e4`，2026-11-14，**工作树带他人未提交 WIP**
- 并行会话（同树，**不要 revert/stash/格式化他们的改动**）：
  - `forebrain-harness-a6`：`pkg/tool/artifacts.go` + `artifacts_test.go`（present_files 正文）——与本计划无重叠。
  - `forebrain-harness-69`（已用 peer 消息确认占用清单）：`pkg/tui/image_open.go`、`pkg/tui/reducer.go`
    （4031 一行 + 8104-8168 段）、`pkg/tui/render.go` **仅 600-605 的 Renderer 字段**、两个测试文件。
    与本计划不重叠；`render.go` 除 600-605 外还有归属不明的 WIP（`inlineCodeColor`，7+/1-），原样保留。
  - 更早的 `forebrain-harness-a3` 在 `pkg/tui/{overlays.go,channels.go,notify.go,render.go,setup.go,run.go}` 留了 WIP；
    另有 WIP 在 `pkg/turn/{approval.go,model.go,approval_test.go}`、`pkg/event/run_events.go`、`pkg/run/*`、
    `pkg/gateway/*`、`frontend/src/{components/chat,composables,lib,views}`。
  - 本计划**必改**其中两个带 WIP 的文件（`pkg/turn/{approval.go,model.go}`、`pkg/tui/{overlays.go,render.go}`）：
    只在自己的锚点处加代码，不动别人的 hunk；`pkg/gateway/*` 只读不改。

## 漂移检查（先跑，按内容不按行号）

基线是**工作区快照**，不是干净 commit。逐条 `git hash-object` 对内容：

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
git rev-parse --short HEAD     # 基线 a1da14e4：HEAD 变了不直接 STOP，但要重跑下面 26 个 hash 与锚点 grep
for f in pkg/tool/format.go pkg/tool/human.go pkg/tool/header_parts.go pkg/tool/display_header.go \
         pkg/tool/state.go pkg/tool/testdata/header_actions.json pkg/tui/overlays.go pkg/tui/render.go \
         pkg/tui/chat_surface.go pkg/tui/chat_turn.go pkg/turn/approval.go pkg/turn/model.go \
         pkg/run/orchestration_llm_test.go frontend/src/components/chat/ToolCallCard.vue \
         frontend/src/components/chat/ApprovalCard.vue frontend/src/composables/usePendingActions.ts \
         frontend/src/lib/api.ts ai-elements-vue/packages/elements/src/tool/headerWords.ts \
         pkg/tui/chat_session_test.go pkg/tui/render_test.go pkg/tool/format_test.go \
         pkg/tool/header_parts_test.go pkg/turn/approval_test.go \
         frontend/src/components/chat/ToolCallCard.test.ts \
         frontend/src/components/chat/ApprovalCard.test.ts \
         frontend/src/composables/usePendingActions.test.ts; do
  printf '%s %s\n' "$(git hash-object "$f" | cut -c1-12)" "$f"
done
```

计划撰写时的前缀（12 位）：

```
aa527e4ea368 pkg/tool/format.go          63cafdcc0115 pkg/tool/human.go
d309ea0524f8 pkg/tool/header_parts.go    de047df96dcd pkg/tool/display_header.go
609063c5d679 pkg/tool/state.go           a34feea9ad79 pkg/tool/testdata/header_actions.json
56d36203e0ab pkg/tui/overlays.go         8e77828db448 pkg/tui/render.go
95bc7afddba2 pkg/tui/chat_surface.go     d57497889333 pkg/tui/chat_turn.go
ccad9eb51afb pkg/turn/approval.go        24e5aa324749 pkg/turn/model.go
f275d639353e pkg/run/orchestration_llm_test.go
7e5d56b2a94e frontend/src/components/chat/ToolCallCard.vue
dfe2dd4fd56a frontend/src/components/chat/ApprovalCard.vue
f516037b34ad frontend/src/composables/usePendingActions.ts
4226b792a27e frontend/src/lib/api.ts
547346ddd869 ai-elements-vue/packages/elements/src/tool/headerWords.ts
1796e557eb83 pkg/tui/chat_session_test.go   79f259990f9c pkg/tui/render_test.go
a1b7a4bf357d pkg/tool/format_test.go       3128a1b67133 pkg/tool/header_parts_test.go
3a4d86a147c3 pkg/turn/approval_test.go
66d69a78d856 frontend/src/components/chat/ToolCallCard.test.ts
3fdb9752603f frontend/src/components/chat/ApprovalCard.test.ts
56ad851ece72 frontend/src/composables/usePendingActions.test.ts
```

基线 = **工作区快照**（HEAD `a1da14e4` + 上表 26 个文件内容哈希）。分两类处理：

- **哈希不符 = STOP**（开工时 `git status` 显示 clean，没有他人 WIP）：
  `pkg/tool/{format,human,header_parts,display_header,state}.go`、`pkg/tool/testdata/header_actions.json`、
  `pkg/tui/{chat_surface,chat_turn,chat_session_test}.go`、`pkg/tool/{format_test,header_parts_test}.go`、
  `pkg/run/orchestration_llm_test.go`、`frontend/.../{ToolCallCard,ApprovalCard}.vue`、
  `frontend/.../{ToolCallCard,ApprovalCard}.test.ts`、`frontend/.../usePendingActions.test.ts`、
  `ai-elements-vue/.../headerWords.ts`。
  （注意：`pkg/tool/*` 里 **`artifacts.go`/`artifacts_test.go` 是 a6 的 WIP，不在上表**，不在 STOP 范围。
  上表是唯一判据，不要按通配符推断。）
- **带 WIP 的文件**（`git status` 显示 ` M`：`pkg/tui/overlays.go`、`pkg/tui/render.go`、
  `pkg/turn/{approval,model,approval_test}.go`、`pkg/tui/render_test.go`、
  `frontend/.../usePendingActions.ts`、`frontend/src/lib/api.ts`）：哈希不符不算 STOP，但要
  **重读本计划摘录过的那些区域**（不是只看一行锚点），任一段落与摘录不符 = STOP。

**开工前清理（评审产物，不是他人 WIP；也是并行会话的门禁阻塞项）**：主树可能有
`?? pkg/tui/zz_review_step0_test.go`（计划评审留下的故意红的复现测试 `TestReview…`，另一个会话的
`go test ./pkg/tui` 门禁被它挡住）。**文件不存在就跳过**；存在则先 `sed -n '1,40p'` 确认它只含评审复现
（无产品代码）再 `rm`；内容不符就去问，别删。这是本计划执行的第一件事（owner 批准后立即做）。

锚点自检：

```bash
grep -n 'func DeniedToolDisplayBody' pkg/tool/format.go
grep -n 'case lower == "user_interaction"' pkg/tool/header_parts.go
grep -n 'func toolStatesOwnDeniedLabel' pkg/tool/display_header.go
grep -n 'if o.cursor == o.chatIdx()' pkg/tui/overlays.go
grep -n 'func (s \*ChatSession) notifyInPlaceRefusedCard' pkg/tui/chat_surface.go
grep -n 'content := tool.DeniedToolDisplayBody' pkg/tui/chat_turn.go
grep -n 'case toolStatusFailed(f), toolStatusDenied(f):' pkg/tui/render.go
grep -n 'func (s ApprovalService) decideAsk' pkg/turn/approval.go
grep -n 'AskAnswerJSON  *string' pkg/turn/model.go
grep -n 'function chatAboutQuestion' frontend/src/components/chat/ApprovalCard.vue
grep -n 'async function cancelAsk' frontend/src/composables/usePendingActions.ts
```

## Why this matters

`user_interaction`（= Claude Code 的 `AskUserQuestion`）的提问卡底部有 `Chat about this`（web 端是「和它聊聊」）。
今天两端都把它当 **cancel**，后果与 Claude Code 不同，且体验很差：

1. **语义错**：主 agent 的 in-place 门收到 cancel 后返回 `tool.ErrApprovalRejectedStop`，**整轮停止**，
   模型只收到 `RejectedStopText`（"STOP what you are doing and wait for the user…"），不知道用户想澄清问题。
   Claude Code 把它当 **deny + feedback**：模型收到「用户想澄清这些问题 / 先问他想澄清什么 / 已问的问题清单」，
   **同一轮继续**，模型接着问用户想澄清什么。
2. **UI 错**：卡片变成红色 `● Denied` + 灰色 `└ (no output)`（截图 1）。Claude Code 是
   `● User declined to answer questions`（默认色）+ 输出区逐条列出问题与选项（截图 2）。
3. **web 同一个 bug**：`AskTabs.vue` 的「和它聊聊」→ `ApprovalCard.chatAboutQuestion` → `cancelAsk`
   → `POST /api/actions/{id}/approve {decision:"cancel"}`，与 TUI 同根因。

修好后：`Chat about this` = 卡片说清「用户拒绝了回答问题」并回放所问内容，模型拿到澄清指令并在同一轮继续；
Esc（真取消）语义不变（回合停止、不产生新模型请求），但卡片改用同一张 declined 卡；
web 的「和它聊聊」发 `decision:"clarify"`，由引擎生成同一份澄清文本。

## 修前复现（评审已实测，执行者必须先自己复现一次）

**Go 层（红）**：现有断言 `TestQuestionOverlayChatAboutThisCancels`（`pkg/tui/chat_session_test.go:15477`）
把 chat 行固定为 cancel；评审在 worktree 里另写了一条期望测试，实跑输出：

```
Cancelled:true Denied:false
```

**真机**：`$D start ask` → 提问卡 4×Down+Enter（第一问 3 个选项，`chatIdx()=4`）得到：

- `$D screen`：`● Denied` / `└ (no output)`（截图 1 现状）；
- `$D provider-log`：**只有 1 条 REQUEST**（回合没有继续）；
- `fb_messages`：该 tool 行的内容 = `pkg/tool/format.go:56` 的 `RejectedStopText`。

**因果链（逐跳核对过）**：

```
pkg/tui/overlays.go:2831  chat 行 → {Cancelled:true}
  → pkg/tui/chat_surface.go:1542-1546  in-place && Cancelled ⇒ notifyInPlaceRefusedCard + ErrApprovalRejectedStop
  → pkg/run/orchestration_llm.go:2086-2092  ⇒ deniedToolResult(…, RejectedStopText, "") 且 stop=true
  → pkg/tool/format.go:90-101  DeniedToolDisplayBody("user_interaction") 落到 default: "" ⇒ body 空
  → pkg/tui/render.go:2226-2250 空 body + denied ⇒ faint "(no output)"；render.go:2528-2531 ⇒ 头色 203 红
  → pkg/tool/header_parts.go:438-441 头词被通用 "Denied" 覆盖
```

**排除的竞争解释**（均已核对）：
- 不是「渲染层丢了 body」——body 在生产处就是空串（`format.go:99-100`）。
- 不是「回合结束后被取消」——`provider-log` 只有 1 条 REQUEST，说明根本没有第二次模型请求。
- 不是「web 与 TUI 各自实现」——两端都落到同一个 `ApprovalService.Decide` cancel 分支。

## Claude Code 侧证据（`/opt/homebrew/bin/claude` 2.1.220，byte offset）

- `141704400`：`AskUserQuestion` 的 chat 回调 `EQf` 返回 `{behavior:"deny", feedback:<text>}`，`text` 固定段逐字为
  （第 2~5 行与 `Questions asked:` 行**行首 4 空格**是模板字面量的一部分）：

  ```
  The user wants to clarify these questions.
      This means they may have additional information, context or questions for you.
      Take their response into account and then reformulate the questions if appropriate.
      Start by asking them what they would like to clarify.

      Questions asked:
  ```

  其后是 `C9S` 生成的清单，每问：`- "<question>"` + `  Answer: <answer>` 或 `  (No answer provided)`
  （预览类问题另有 `  User notes: <text>`），问题之间换行。
- `234350400` `cancelAndAbort(feedback)`：`behavior:"deny"` + feedback 拼成 `bft+feedback`（主 agent）/
  `kRo+feedback`（subagent）；`234605314` 显示 `cancelled` 走 `{behavior:"deny", message:"User aborted"}`
  ——即 **cancel 也是 deny**，UI 的 rejected 视图同样生效。forebrain 侧的 `pkg/tool/format.go:71-83
  RejectionText` 与 `bft/kRo` 逐字一致，**不需要改**。
- `240594457` `P3p`（AskUserQuestion 的 `renderToolUseRejectedMessage`）：
  头行 `● User declined to answer questions`（default 前景），输出区每问一行
  `· <question> (<label> / <label> / …)`（inactive 前景）。

## Current state（磁盘现状，执行者按此核对）

- `pkg/tui/overlays.go` `questionOverlay.commitCurrent()`：

  ```go
  if o.cursor == o.chatIdx() {
      return turn.ToolApprovalDecision{Cancelled: true}, true
  }
  ```

  `commitForm()` 把 `o.answers []map[int]bool` + `o.otherText []string` 编成
  `state.AskAnswer{Answers: []AskAnswerItem{QuestionID, OptionIDs, OtherText}}`。
  该文件已 import `pkg/tool` 与 `encoding/json`。
- `pkg/tui/chat_surface.go` `answerInPlaceApproval`（`inPlace := gate.AgentID == ""`）里：
  `completeInPlaceApprovalDecision` 之后，`inPlace && decision.Denied` → `notifyInPlaceRefusedCard(gate, req, decision)`；
  `inPlace && decision.Cancelled` → `notifyInPlaceRefusedCard` + `return nil, tool.ErrApprovalRejectedStop`。
  然后 builder：`turn.SubagentApprovalResumeContext(ctx, gate, turn.ApprovalResume{Session: gate.Session,
  Approved: decision.Approved, Denied: decision.Denied, Reason: decision.DenyReason}, who)`。
- `pkg/tui/chat_surface.go` `approvalChoice(act, decision)`：`Cancelled→"cancel"`；`!Approved→"deny"`；否则 `"accept"`。
- `pkg/turn/approval.go` `Decide`：先处理 `choice=="deny"||"decline"` → `Actions.Deny(act.ID, reply.Reason)`；
  再 `user_interaction` → `decideAsk`；`decideAsk` 只认 `"cancel"` 与 `"accept"`，其余报
  `unsupported user_interaction decision`。
- `pkg/turn/model.go` `ToolApprovalDecision` 现有字段：`Approved / Cancelled / Denied / DenyReason /
  AskAnswerJSON / Update / …`（`pkg/turn/model.go:675-690` 一带）。
- `pkg/turn/subagent_approval.go:143-148` `SubagentApprovalResumeContext` 把 `resume.Reason` 同时写进
  `DenyReason` 与 `DenyFeedback`；`pkg/run/orchestration_llm.go:2099-2102` 的 in-place deny 用
  `rejectionTextFor(ctx, call, st.DenyReason)` 造模型文本、`st.DenyFeedback` 造卡片 meta。
  **模型文本只读 `DenyReason`，`DenyFeedback` 只进卡片 meta**——这是"丢弃 display feedback 不影响模型"的依据。
- `pkg/turn/approval.go:1799-1810` `PendingApprovalGate.PendingForRun`：`req.AskFormJSON = act.PayloadJSON`，
  `req.ToolInputJSON = wait.ToolInputJSON`；`pkg/tool/registry.go:474-480` 建等待行时把 **AskForm payload**
  写进 `ToolInputJSON`。所以 live 卡片的 `input` 是 **form 形状**（`questions[{id,prompt,options[{label}]}]`），
  而 replay 的 `DeniedToolStep` input 来自模型参数（`questions[{header,question,options[{label}]}]`）——两种形状都要认。
- `pkg/tool/header_parts.go:341-350`（`user_interaction` 头词）、`:434-441`（通用 `Denied`/`Canceled` 覆盖）；
  `pkg/tool/display_header.go:98-105` `toolStatesOwnDeniedLabel` 放行名单。
- `pkg/tui/render.go`：`toolHeaderColor`（第一个 `switch lowerTitle` 里 disturb 三个 plan/permission case；
  其后的布尔 switch 对 `toolStatusDenied` 返回 `"203"`）；`noColorActive()`（`pkg/tui/tty_session.go:1077`），
  plan/permission 的 case 在 NO_COLOR 下返回 `"244"`；空 body 分支优先打印 `ToolMeta.DenyFeedback`
  （`render.go:2244` 一带）。
- `pkg/tool/format.go:3831` `clampBody(s, maxBytes)` 已存在；denied 分支（`format.go:136-139`）**没有** clamp。
- `pkg/tool/state.go:241-250`：`Denied`/`DenyFeedback` 的字段注释（新行为下 ask 的 DenyFeedback 恒空，需同步）。
- web：`frontend/src/components/chat/AskTabs.vue` 底部 `ask-foot-chat` 按钮 `emit('chat-about')`；
  `ApprovalCard.vue:248` `chatAboutQuestion(actionId)` = `void cancelAsk(actionId); emit('focus-composer')`；
  `usePendingActions.ts:309` `cancelAsk` → `actionsApprove(id,{decision:'cancel'})`；
  `ToolCallCard.vue:388` `isRefused`（只画 `refusalSentence` + `refusalWords`=meta.deny_feedback，不画 body）、
  `:92` `isAnsweredAsk` → `AskDoneCard`、默认分支 `ToolOutput :output="step.output"`（= `tool_display.body`）；
  `ApprovalCard.test.ts:392` 断言 chat 点击 = `{decision:'cancel'}`。
  `ApprovalCard.vue:154` 只渲染 `block.confirmation`，**不渲染** `block.message`（= `ApprovalResolvedPayload.Reason`）。
- `pkg/gateway/api_extra.go:4449-4456`：REST `/approve` 把 `decision` 原样塞进 `ApprovalReply.Choice`
  ——新增 `"clarify"` **不需要改 gateway**。
- `pkg/run/orchestration_llm_test.go:4907,5108` 仍以 **两参** 调 `toolpkg.DeniedToolDisplayBody("exit_plan_mode", "…")`。

## 行为变化矩阵

| # | 场景 | 旧行为 | 新行为 | 类别 |
|---|---|---|---|---|
| 1 | TUI 提问卡选 `Chat about this` | 取消：回合停；模型收 STOP 句；卡 `● Denied`+`(no output)`（红） | 拒绝：回合继续；模型收 `RejectionText` 前缀 + 澄清文本；卡 `● User declined to answer questions` + `· 问题 (选项 / 选项)` | 目标 |
| 2 | TUI 提问卡 Esc / Ctrl+C | 取消：回合停；同一张 `● Denied`+`(no output)`（红） | 取消语义不变；卡改用同一张 declined 卡 | owner 裁决：同卡 |
| 3 | web「和它聊聊」 | `decision:"cancel"`：回合停；gate 卡成拒绝态 + 长 `deny_feedback` | `decision:"clarify"`：引擎生成同一份澄清文本并 Deny；回合继续；卡画问题回放 | owner 裁决：本期同修 |
| 4 | 已答提问（accept） | `● Asked user` + 问题/答案 | 不变 | 不动 |
| 5 | 其它工具被拒 | 各自不变 | 不变 | 只 `user_interaction` 进新分支 |
| 6 | 旧数据/迁移行（input 无 `questions`） | `Denied`+`(no output)` | 新头词 + `(no output)`；绝不把澄清指令当 body | 兜底 |
| 7 | subagent 的提问 + chat | 取消 | 拒绝（模型收 `deniedWithFeedbackPrefix`+澄清文本）；roster 行的 `message` 仍会含澄清原文 | 既有缺陷，见遗留 |
| 8 | 超长 `questions`（敌意模型） | body 空，无影响 | body 在 `DefaultMaxFormattedBody` 处被 `clampBody` 截断 | 输入上界 |

## 非功能项（本改动会碰到的）

- **延迟**：body 只在拒绝时构造一次（O(问题数) 字符串拼接），无 I/O、无网络、无锁；live 与 replay 同一函数。
- **资源/输入上界**：form 受 `pkg/tool/human.go validateInput` 约束（≤4 问、每问 2–4 选项、label ≤5 词），
  但题干无长度上限 → **必须** `clampBody(body, DefaultMaxFormattedBody)`（Step 2）。
- **健壮性**：form payload 解析失败、`AskAnswerJSON` 为空/坏、旧行 input 形状不认识——三种都退化为
  「头词正确 + `(no output)`」，不 panic、不把模型指令打到界面。
- **NO_COLOR**：declined ask 的头色在 `noColorActive()` 下返回 `"244"`，与 plan/permission 的既有 case 一致。
- **兼容**：不新增 API 字段、不改 DB schema、不改 `ApprovalResolvedPayload` 形状；`clarify` 走既有
  `Actions.Deny` 通道，旧客户端不受影响（多出来一个 choice 只有新前端会发）。

## Scope

**In scope**
- `pkg/tool/format.go`：`DeniedToolDisplayBody(toolName, input, feedback)` + `user_interaction` 的 declined body
  （含 `clampBody`）；`userInteractionQuestions` 兼容两种 input 形状；`FormatToolStepResult` 传 `evt.Input`；
  `DeniedToolStep` 对 ask 丢弃 display feedback。
- `pkg/tool/human.go`：`AskClarifyReason(form state.AskForm, answers state.AskAnswer) string`。
- `pkg/tool/header_parts.go` + `pkg/tool/display_header.go`：denied ask 头词 + 放行名单 + `HeaderActions()`。
- `pkg/tool/state.go`：`Denied`/`DenyFeedback` 注释同步。
- `pkg/tool/testdata/header_actions.json`（`UPDATE_HEADER_ACTIONS=1` 生成）。
- `pkg/tool/format_test.go`、`pkg/tool/header_parts_test.go`。
- `pkg/turn/model.go`：`ToolApprovalDecision.AskClarify bool`。
- `pkg/turn/approval.go`：`decideAsk` 的 `"clarify"` 分支 + `askFormOf` helper。
- `pkg/turn/approval_test.go`：clarify 用例。
- `pkg/tui/overlays.go`：chat 行改为 deny+clarify；抽出 `formAnswer()`；顶部注释同步。
- `pkg/tui/chat_surface.go`：`approvalChoice` 认 `AskClarify`；`answerInPlaceApproval` 的 in-place reason；
  `notifyInPlaceRefusedCard` 传 input。
- `pkg/tui/chat_turn.go`：`notifyToolApprovalDenied` 传 input。
- `pkg/tui/render.go`：头色（含 NO_COLOR）+ 空 body 兜底不打印 ask 的 DenyFeedback。
- `pkg/tui/chat_session_test.go`：chat 用例改断言 + declined 卡渲染/头色断言（新）。
- `pkg/run/orchestration_llm_test.go`：两处 3 参调用。
- `frontend/src/components/chat/ToolCallCard.vue` + `ToolCallCard.test.ts`。
- `frontend/src/components/chat/ApprovalCard.vue` + `ApprovalCard.test.ts`（chat → clarify）。
- `frontend/src/composables/usePendingActions.ts`（`clarifyAsk`）+ `usePendingActions.test.ts`（若已有 cancelAsk 用例则补一条）。
- `frontend/src/lib/api.ts`：`ActionApprovalBody.decision` 联合类型加 `'clarify'`。
- `ai-elements-vue/packages/elements/src/tool/headerWords.ts`：中文表新词。

**Out of scope（明确不碰）**
- `pkg/gateway/*`：REST 已把 `decision` 原样转发（见 Current state）；`pkg/gateway/api_extra.go` 有他人 WIP。
- `pkg/run/*`（除 `orchestration_llm_test.go` 的签名跟随）：`rejectionTextFor`/`deniedToolResult` 现成链路够用。
- `pkg/event/*`、`pkg/state/*`（除 `pkg/tool/state.go` 注释；`pkg/state/*` 一行不改）。
- answered ask 的卡头（Claude 是 `User answered Claude's questions:`，forebrain 是 `Asked user`）——不在本 bug 范围。
- 提问超时（Claude 的 `Claude asked:` / `No response after Ns`）——forebrain 无此机制。
- subagent roster 行显示澄清原文（见 Maintenance 的遗留项）。
- `pkg/tui/{setup,channels,notify,run}.go`、`pkg/tool/artifacts.go`、图片卡相关文件——他人 WIP。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`（需 pdfium 时先 `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`） | exit 0 |
| 定点工具包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestAskClarify|TestDeniedToolStepReadsAsTheRefusalEverywhere|TestHeaderActionsGolden|TestUserInteractionDenied' -count=1` | ok |
| 定点 turn | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'TestApprovalServiceAnswersAsk|TestDecideAskClarify' -count=1` | ok |
| 定点 TUI | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestQuestionOverlayChatAboutThis|TestDeclinedAskCard|TestDeniedAskHeaderColor' -count=1` | ok |
| 签名跟随 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestDenied' -count=1` | ok |
| golden 重写 | `UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestHeaderActionsGolden -count=1` | ok，`git diff pkg/tool/testdata/header_actions.json` 只多一行 |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/run ./pkg/state -count=1` | ok |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| gofmt（只查本计划改的文件，仓库级 gate 现在被他人 WIP 污染） | `gofmt -l pkg/tool pkg/turn pkg/tui` | 只允许出现 `pkg/tui/subagent_footer_display_test.go`（他人未跟踪文件） |
| 前端单测（**不要加 `--`**，加了会跑全量） | `cd frontend && pnpm test ToolCallCard ApprovalCard` | 全绿 |
| 前端类型 | `cd frontend && pnpm typecheck` | exit 0 |
| 真机 | 见 Step 11 | 见预期 |

## Steps

### Step 0 — 先复现（改任何源码之前）

1. 在 `pkg/tui/chat_session_test.go` 紧邻现有 `TestQuestionOverlayChatAboutThisCancels`（:15477）
   新增期望测试 `TestQuestionOverlayChatAboutThisRefusesWithClarify`：

   ```go
   func TestQuestionOverlayChatAboutThisRefusesWithClarify(t *testing.T) {
       o := newTestOverlay(sampleForm())
       for o.cursor < o.chatIdx() {
           o.handleKey(parsedKey{kind: rawKeyDown})
       }
       dec, done := o.handleKey(parsedKey{kind: rawKeyEnter})
       if !done {
           t.Fatalf("chat row must close the form: dec=%+v", dec)
       }
       // AskClarify 字段 Step 4 才存在：这条复现测试只断言今天就能看懂的行为。
       if dec.Cancelled || !dec.Denied {
           t.Fatalf("chat about this must refuse, not cancel: dec=%+v", dec)
       }
       if !strings.Contains(dec.AskAnswerJSON, `"answers"`) {
           t.Fatalf("the form's partial answer must ride along: %q", dec.AskAnswerJSON)
       }
   }
   ```

2. 跑它，**必须红**，记下输出（这就是修前复现）：
   `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestQuestionOverlayChatAboutThisRefusesWithClarify$' -count=1`
   → `chat about this must refuse, not cancel: dec={Cancelled:true …}`（现状 Cancelled，故红）。
3. 真机复现（临时 home，不污染 `~/.forebrain`）：

   ```bash
   D=.claude/skills/run-forebrain/driver.sh
   # 同一台机器可能有并行会话也在跑 driver：工作目录 / tmux 会话 / 端口都要唯一
   export FOREBRAIN_RUN_WORK=$TMPDIR/fb-run-pre
   export FOREBRAIN_RUN_SESSION=fb-pre
   export FOREBRAIN_RUN_PORT=18731
   export FAKE_DUMP_DIR=$TMPDIR/fb-pre-dump   # 必须在 start 前导出，provider 进程才会继承
   $D build && $D reset
   $D start ask
   $D screen            # 看清 ❯ 位置
   $D key Down; $D key Down; $D key Down; $D key Down   # 第一问 3 选项 ⇒ chatIdx=4
   $D screen            # 确认 ❯ 在 "Chat about this"
   $D key Enter
   $D screen            # 取证：● Denied + └ (no output)
   $D provider-log      # 取证：只有 1 条 REQUEST
   $D db "SELECT content FROM fb_messages ORDER BY id DESC LIMIT 3;"
   $D stop
   ```

   > `provider-log` 只写 `REQUEST <path> stream=… mode=…`，**没有请求体**；请求体只在 `FAKE_DUMP_DIR` 里
   > （`request-0001.json`、`request-0002.json`……按到达顺序编号）。

**Verify**：Go 测试红且失败信息是上面那句；真机三条证据齐全。把两段输出贴进本文件末尾「验收记录」。

### Step 1 — 模型侧澄清文本（`pkg/tool/human.go`）

在 `askAnswerJSONToHumanOutput` 之后新增（纯函数、无 panic、入参 trim）：

```go
// AskClarifyReason is what the model receives when the person picks
// "Chat about this" on a question form: the questions stay unanswered, and the
// model is told to find out what the person wants to clarify before asking
// again. The words are Claude Code's (AskUserQuestion's clarify feedback),
// verbatim for the instruction, with the questions as they were asked and
// whatever the person had already answered.
func AskClarifyReason(form state.AskForm, answers state.AskAnswer) string
```

规则：
- 固定段逐字（**第 2~5 行与 `Questions asked:` 行首 4 空格**）：
  `"The user wants to clarify these questions.\n    This means they may have additional information, context or questions for you.\n    Take their response into account and then reformulate the questions if appropriate.\n    Start by asking them what they would like to clarify.\n\n    Questions asked:"`
- 按 `form.Questions` 顺序，每问追加：
  - `"\n- \"" + questionText + "\""`，questionText = `strings.TrimSpace(q.Prompt)`，空则 `q.ID`；
  - 答案串按 Claude 的 `setAnswer`（`u9S`，offset `243230197`）分路：
    - **单选**（`!q.AllowMultiple`）：`OtherText` 非空 → 答案就是它（Claude 里自由文本**取代**选择）；
      否则是选中项的 `Label`（ID 映射不到就用原 ID）。
    - **多选**：选中项 `Label` 用 `", "` 连接；`OtherText` 非空则作为**最后一个**答案追加。
      （Claude 的多选忽略自由文本；forebrain 的多选同时保留选择与 Other，把它丢掉会让模型看不到用户写的
      话——这是有意的适配，测试注释里写明。）
  - 有答案 → `"\n  Answer: " + joined`；无答案 → `"\n  (No answer provided)"`。
  - **不要生成 `User notes:`**：Claude 的 notes 来自预览面板的独立文本框
    （`questionStates[q].textInputValue`）；forebrain 的 `state.AskAnswerItem` 只有 `OptionIDs` + `OtherText`，
    没有第二个文本框的值，硬造会把 Other 重复两次。
  - 问题行的引用：Claude 的 `C9S` 按题干键；forebrain 用 `AskAnswerItem.QuestionID`（= `form.Questions[].ID`）
    匹配答案，输出仍用题干文本。
- `form.Questions` 为空也要返回固定段（不要返回空串）。
- 不要吞掉/改写题干里的换行：原样放进引号内（模型看到的就是它自己写的问题）。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0。

### Step 2 — 被拒卡的 body：问题回放（`pkg/tool/format.go`）

1. 扩 `userInteractionQuestions(input)`（唯一投影，勿另写第二个读法）：
   - `type userInteractionQuestion` 增 `ID string \`json:"id"\``、`Prompt string \`json:"prompt"\``、
     `Options []userInteractionOption \`json:"options"\``；新增
     `type userInteractionOption struct{ Label string \`json:"label"\` }`。
   - 归一化：`Header = firstNonEmpty(Header, ID)`（`firstNonEmpty` 在同包 `web_tools.go:1237`）、
     `Question = firstNonEmpty(Question, Prompt)`；仍按 `Header == ""` 丢弃；所有字符串 trim。
   - 新增 `func (q userInteractionQuestion) Text() string`：`Question`，空则 `Header`。
2. 新增：

   ```go
   // userInteractionDeclinedBody is the body of a question call the person
   // declined to answer: one line per question — the question, then the options
   // it offered — the way Claude Code's declined ask reads. Empty when the call
   // carries no question this build can read (a migrated row), which leaves the
   // card on the ordinary empty-body path rather than inventing text.
   func userInteractionDeclinedBody(input map[string]any) string
   ```

   每问一行 `"· " + q.Text()`；`len(labels) > 0` 时拼 `" (" + strings.Join(labels, " / ") + ")"`；行间 `\n`。
   收尾（`clampBody` 返回 `(string, bool)`，别写成单值）：

   ```go
   body, _ := clampBody(strings.Join(lines, "\n"), DefaultMaxFormattedBody)
   return body
   ```
3. `DeniedToolDisplayBody(toolName string, input map[string]any, feedback string) string`：
   `switch` 增 `case strings.EqualFold(strings.TrimSpace(toolName), "user_interaction"): return userInteractionDeclinedBody(input)`。
4. `FormatToolStepResult` 的 denied 分支 → `DeniedToolDisplayBody(toolName, evt.Input, evt.DenyFeedback)`。
5. `DeniedToolStep` 里，ask 丢弃 display feedback（参数契约是"用户原话"，ask 的拒绝原因是引擎写的澄清指令）：

   ```go
   if strings.EqualFold(strings.TrimSpace(toolName), userInteractionToolName) {
       // The ask's only refusal is "Chat about this": its stored reason is the
       // engine's clarify text for the model, not the person's words, so the
       // card carries none — its body is the questions themselves.
       feedback = ""
   }
   ```

6. `pkg/tool/state.go:241-250` 的 `Denied`/`DenyFeedback` 注释补一句：ask 的 `DenyFeedback` 恒为空，
   其拒绝原因只进模型文本（`action.Error`）。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0。

### Step 3 — 头词（`pkg/tool/header_parts.go` + `display_header.go`）

1. `case lower == "user_interaction":` 内优先判 denied：

   ```go
   case lower == "user_interaction":
       if isDenied {
           // Claude Code's declined ask head: the sentence says what happened,
           // and the body lists what was asked.
           action = "User declined to answer questions"
       } else if isFailed { … 原样 … }
   ```

2. `toolStatesOwnDeniedLabel` 放行 `"user_interaction"`。
3. `HeaderActions()` 增 `"User declined to answer questions"`（「Notes and questions」组）。
4. 重生成 golden：`UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestHeaderActionsGolden -count=1`。

**Verify**：`git diff pkg/tool/testdata/header_actions.json` 只多一行；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestHeaderActionsGolden -count=1` ok。

### Step 4 — 引擎决策 `clarify`（`pkg/turn/model.go` + `pkg/turn/approval.go`）

1. `pkg/turn/model.go` `ToolApprovalDecision` 加字段（放在 `AskAnswerJSON` 旁）：

   ```go
   // AskClarify is "Chat about this" on a question form: the call is refused
   // with the engine's clarify instruction (tool.AskClarifyReason) instead of
   // being cancelled, so the turn carries on and the model asks the person what
   // they want to clarify. AskAnswerJSON carries what they had already answered.
   AskClarify bool
   ```

2. `pkg/turn/approval.go` `decideAsk`：在 `if choice == "cancel"` 之后、`if choice != "accept"` 之前加：

   ```go
   if choice == "clarify" {
       // "Chat about this": the ask is refused, not cancelled. The engine owns
       // the words, and the person's partial answers ride along so the model
       // sees what they were looking at before asking again.
       form, err := askFormOf(act.PayloadJSON)
       if err != nil {
           return ApprovalResult{}, err
       }
       var partial state.AskAnswer
       if raw := strings.TrimSpace(reply.AskAnswerJSON); raw != "" {
           // Lenient by design: these are answers to questions nobody
           // answered, not a submitted form.
           _ = json.Unmarshal([]byte(raw), &partial)
       }
       next, err := s.Actions.Deny(ctx, act.ID, tool.AskClarifyReason(form, partial))
       return ApprovalResult{Action: next, Denied: err == nil}, err
   }
   ```

3. 同文件 `resolvedApproval` 的 `case state.ActionDenied:` 加 clarify 幂等（web 双击/重试；今天只放行
   `deny/decline`，重复 `clarify` 会返回 `ErrApprovalConflict`）：

   ```go
   case state.ActionDenied:
       if choice == "deny" || choice == "decline" || choice == "clarify" {
           return ApprovalResult{Action: act, Denied: true, Idempotent: true}, nil
       }
   ```

4. 同文件加 helper（`decideAsk` 附近）：

   ```go
   // askFormOf decodes the question form an ask action stores as its payload.
   func askFormOf(payloadJSON string) (state.AskForm, error) {
       var form state.AskForm
       if err := json.Unmarshal([]byte(strings.TrimSpace(payloadJSON)), &form); err != nil {
           return state.AskForm{}, fmt.Errorf("decode ask form: %w", err)
       }
       if len(form.Questions) == 0 {
           return state.AskForm{}, fmt.Errorf("ask form has no questions")
       }
       return form, nil
   }
   ```

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/turn` → exit 0。

### Step 5 — TUI 发 clarify（`pkg/tui/overlays.go`）

1. 从 `commitForm()` 抽出（`commitForm` 改调它，行为不变）：

   ```go
   // formAnswer is the answer the form holds right now: the selections and the
   // Other text per question, skipping questions the person has not touched.
   func (o *questionOverlay) formAnswer() state.AskAnswer
   ```

2. `commitCurrent()` 的 chat 分支：

   ```go
   if o.cursor == o.chatIdx() {
       // Claude Code's "Chat about this": refused with the clarify instruction,
       // not cancelled — the turn carries on and the model asks what the person
       // wants to clarify. What they answered so far rides along: this is the
       // last the model hears of the form.
       //
       // state.AskAnswer is strings and slices only, so the encode cannot fail.
       encoded, _ := json.Marshal(o.formAnswer())
       return turn.ToolApprovalDecision{
           Denied: true, AskClarify: true, AskAnswerJSON: string(encoded),
       }, true
   }
   ```

3. 文件顶部 `questionOverlay` 注释里 "advance from the Submit button; cancel from
   \"Chat about this\"" 改成拒绝语义（"refuses the question with the clarify instruction"）。

**Verify**：Step 0 的红测试转绿：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestQuestionOverlayChatAboutThisRefusesWithClarify$' -count=1` → ok。

### Step 6 — TUI 决策→choice→resume 与两个画卡调用点

1. `pkg/tui/chat_surface.go` `approvalChoice`：**最前面**加（`AskClarify` 与 `Denied` 同时为 true，必须抢在
   `!Approved → "deny"` 之前）：

   ```go
   if decision.AskClarify {
       // The ask's clarify is its own decision: the engine composes the words
       // and records the denial, so it must not read as an ordinary refuse.
       return "clarify"
   }
   ```

2. `pkg/tui/chat_surface.go` `completeSurfaceApproval` 的 `turn.ApprovalReply{…}` 增
   `AskAnswerJSON: decision.AskAnswerJSON`（accept 的 ask 走早退分支，不受影响；其它 kind 忽略该字段）。

3. `pkg/tui/chat_surface.go` `answerInPlaceApproval`：resume 的 `Reason` 对 clarify 取**已落库的 action**：

   ```go
   reason := decision.DenyReason
   if decision.AskClarify {
       // The clarify instruction is composed where the denial is recorded (the
       // engine owns its words); the decision that asked for it carries none.
       act, err := s.actionSvc().Get(ctx, gate.ActionID)
       if err != nil {
           return nil, fmt.Errorf("clarify ask %s: %w", gate.ActionID, err)
       }
       if act == nil || strings.TrimSpace(act.Error) == "" {
           // Never fall through with an empty reason: the model would read the
           // generic deny sentence instead of the clarify instruction, and the
           // person would think their "chat about this" asked something it did
           // not. The waiting call fails visibly instead.
           return nil, fmt.Errorf("clarify ask %s: the engine stored no clarify reason", gate.ActionID)
       }
       reason = strings.TrimSpace(act.Error)
   }
   resumeCtx, resumeErr := turn.SubagentApprovalResumeContext(ctx, gate, turn.ApprovalResume{
       Session: gate.Session, Approved: decision.Approved, Denied: decision.Denied, Reason: reason,
   }, who)
   ```

4. `notifyInPlaceRefusedCard`：`Content: tool.DeniedToolDisplayBody(toolName, input, strings.TrimSpace(decision.DenyReason))`。
5. `pkg/tui/chat_turn.go` `notifyToolApprovalDenied`：`content := tool.DeniedToolDisplayBody(toolName, input, feedback)`。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestQuestionOverlay' -count=1` → ok。

### Step 7 — TUI 颜色与空 body 兜底（`pkg/tui/render.go`）

1. `toolHeaderColor` 的**第一个** `switch lowerTitle` 里加 case（非 denied 不能 return，要落到下面的布尔 switch）：

   ```go
   case "user_interaction":
       if toolStatusDenied(f) {
           // A declined ask is not an error: Claude paints its head in the
           // default ink, and all it says is that no answer came. Every other
           // state falls through to the ordinary rules below.
           if noColorActive() {
               return "244"
           }
           return ordinaryToolHeaderColor
       }
   ```

2. 空 body 兜底的 `DenyFeedback` 回退加条件：ask 永远不打印它（那是模型用的澄清指令）：

   ```go
   if words := strings.TrimSpace(f.ToolMeta.DenyFeedback); toolStatusDenied(f) && words != "" &&
       !strings.EqualFold(title, "user_interaction") {
   ```

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### Step 8 — Go 测试

1. `pkg/tool/format_test.go`
   - 扩 `TestDeniedToolStepReadsAsTheRefusalEverywhere`（目前 :2899）的 cases：给 case 结构加
     `input map[string]any` 字段并传进 `DeniedToolStep(…, tc.input, tc.feedback)`（现在写死
     `map[string]any{}`）；新增 `user_interaction` 用例，`input` =
     `{"questions":[{header,question,options:[{label:"A"},{label:"B"}]}, …]}` → body 两行 `· …(A / B)`；
     并断言 `BuildToolMeta(evt).DenyFeedback == ""`、`payload.display_body` 同值。
   - 新增 `TestUserInteractionDeniedBodyReadsBothShapes`：模型参数形状（`header/question`）与果 form 形状
     （`id/prompt`）产出同一 body；`options` 缺失时只剩 `· 问题`；`clampBody` 截断（造一个超长题干）。
   - 新增 `TestAskClarifyReasonSpeaksClaudeCodeWords`：固定段逐字（含 4 空格）+ 四类答案：
     单选未答（`(No answer provided)`）、单选已选（`Answer: <label>`）、单选 Other-only（`Answer: <text>`，
     即自由文本取代选择）、多选选择+Other（`Answer: a, b, <text>`）；再加空 form（只返回固定段——
     **有意**不保留 Claude 在 0 问时的尾换行；forebrain 的 form 至少 1 问，这条只是兜底）。
2. `pkg/tool/header_parts_test.go`：denied ask 头词 = `User declined to answer questions`，completed 仍 `Asked user`
   （照 `TestListUsableModelsHeader` 的写法）。
3. `pkg/turn/approval_test.go`：`TestDecideAskClarifyDeniesWithClarifyReason`（照 `TestApprovalServiceAnswersAsk` :228）
   —— 建 ask action → `Decide(choice:"clarify", AskAnswerJSON: 部分答案)` → `result.Denied`、`act.Error` 含
   `The user wants to clarify these questions.` 与题干、`act.Status == denied`；空 `AskAnswerJSON` 也能成功。
   同一测试后半段：对同一个 action 再发一次 `clarify` → `result.Idempotent && result.Denied`（不报 conflict）。
4. `pkg/tui/chat_session_test.go`：
   - Step 0 的 `TestQuestionOverlayChatAboutThisRefusesWithClarify` 保留并**补**
     `if !dec.AskClarify { … }`（Step 4 之后该字段已存在）；把
     `TestQuestionOverlayChatAboutThisCancels`（:15477）**删除或改名重写**为拒绝断言（不留两条互相矛盾的用例），
     并在注释里写明「契约由 2026-11 的 Claude Code 对齐修订」。
   - 新增 `TestDeclinedAskCardDrawsTheQuestionsNotTheClarifyText`：用
     `tool.DeniedToolStep(...)` + `tool.BuildToolMeta` + `ToolOutput` 那一层渲染（样板：
     `pkg/tui/chat_session_test.go:17921` 的 `renderExplicitSkillStepCard`）断言：头行含
     `User declined to answer questions`、body 含 `· ` 问题行、**不含** `(no output)`、**不含**
     `The user wants to clarify`。
   - 新增 `TestDeniedAskHeaderColor`：`toolHeaderColor(Frame{Kind: FrameTool, Title:"user_interaction",
     ToolMeta: tool.ToolMeta{Status:"denied"}}) == "214"`；`t.Setenv("NO_COLOR","1")` 后 == `"244"`。
5. `pkg/run/orchestration_llm_test.go:4907,5108`：`toolpkg.DeniedToolDisplayBody("exit_plan_mode", nil, "改成先写测试")`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/run -count=1` → ok。

### Step 9 — web：决策 + 卡片（三面一致）

1. `frontend/src/lib/api.ts:1634-1637`：`decision?: ApprovalDecisionName | 'deny' | 'clarify'`（注释补一句 clarify）。
2. `frontend/src/composables/usePendingActions.ts`：在 `cancelAsk` 旁加

   ```ts
   /** 「和它聊聊」：这个问题不算取消——引擎按 clarify 记一条拒绝（澄清指令由 Go 侧生成），
    *  这一轮继续，模型接着问要澄清什么。 */
   async function clarifyAsk(actionId: string) {
     await submitAction(actionId, () => forebrainApi.actionsApprove(actionId, { decision: 'clarify' }))
   }
   ```

   并加进 return 对象（`cancelAsk` 旁）。`usePendingActions.test.ts` 里没有 `submitAsk`/`cancelAsk` 的先例，
   照 `ApprovalCard.test.ts:375-398` 的写法补一条 `clarifyAsk` 用例：**文件顶部补 `vi` 与 `forebrainApi`
   的 import**（现有的只从 vitest 引了 `describe/expect/it` 等，且没引 `forebrainApi`），用该文件既有的
   `usePendingActions({ sessionId: ref(null), … })` 构造 store（`submitAction` 成功后会自动 `load()`，
   所以还要 `vi.spyOn(forebrainApi,'actionsList').mockResolvedValue([])`），断言只发一次
   `actionsApprove(id, {decision:'clarify'})`。
3. `frontend/src/components/chat/ApprovalCard.vue:248`：在本地 wrapper 区（`cancelAsk` 旁，:190）加
   `function clarifyAsk(actionId: string) { if (store) void store.clarifyAsk(actionId) }`，
   `chatAboutQuestion` 改调它（保留 `emit('focus-composer')`）。`ApprovalCard.test.ts:392` 的断言改成
   `{ decision: 'clarify' }`，测试名同步。
4. `frontend/src/components/chat/ToolCallCard.vue`：
   - **在 `const isRefused = …`（:388）之前**定义（否则 TDZ）：

     ```ts
     /** A question the reader declined: the card lists what was asked (Claude
      *  Code's declined ask), so it is not the ordinary refusal sentence. */
     const isDeclinedAsk = computed(() =>
       String(props.step.toolName ?? '').trim() === 'user_interaction' && toolStatus.value === 'denied')
     ```

   - `isRefused` 改 `computed(() => toolStatus.value === 'denied' && !isDeclinedAsk.value)`；
   - 模板在 `isAnsweredAsk` 分支旁插入（选 `ToolOutput`，不新建组件）：

     ```vue
     <!-- A declined question keeps its body: what was asked, one line each, the
          way the terminal's card lists it (never the engine's clarify text). -->
     <ToolOutput
       v-else-if="isDeclinedAsk"
       :output="String(step.output ?? '') || t('chat.toolNoOutput')"
       :error-text="''"
     />
     ```

   - `openedByDefault` 加 `|| isDeclinedAsk.value`（问题回放就是这张卡的全部内容）。
5. `ai-elements-vue/packages/elements/src/tool/headerWords.ts`：`HEADER_ACTION_ZH` 增
   `'User declined to answer questions': '用户拒绝回答提问',`。
6. `ToolCallCard.test.ts`：新增 denied ask 用例（step 形状照 `ToolCallCard.test.ts:515-538` 的
   `stepOf({toolName, summary, status:'denied', denyFeedback})` 写法），带
   `denyFeedback: 'The user wants to clarify these questions. …'` 以证明它不被渲染）：断言显示 `· ` 问题行、不含 refusal 句、
   不含 `The user wants to clarify`。

**Verify**：`cd frontend && pnpm test ToolCallCard ApprovalCard` → 全绿；`pnpm typecheck` → exit 0。

### Step 10 — 全量回归

```bash
gofmt -l pkg/tool pkg/turn pkg/tui        # 只允许 pkg/tui/subagent_footer_display_test.go（他人未跟踪文件）
CGO_ENABLED=1 go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/run ./pkg/state -count=1
```

**Verify**：gofmt 只有那一行、vet exit 0、测试 ok。

### Step 11 — 真机验收（tmux + fake provider，**禁真模型**）

> 下面几个 fenced 块是**同一个 shell 会话**里的连续步骤：`D`、`CHAT_SID`、`FAKE_DUMP_DIR` 等变量要在
> 同一个 shell 里导出，逐块复制到不同 shell 会丢变量（回放会静默变成全新会话）。

```bash
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-run-after
export FOREBRAIN_RUN_SESSION=fb-after
export FOREBRAIN_RUN_PORT=18732
export FAKE_DUMP_DIR=$TMPDIR/fb-after-chat   # start 之前导出；每个场景各用一个空目录
$D build && $D reset && $D start ask
$D screen
$D key Down; $D key Down; $D key Down; $D key Down
$D screen                                  # 确认 ❯ 在 "Chat about this"
$D key Enter
$D screen                                  # 取证 A：declined 卡
$D provider-log                            # 取证 B：应有第 2 条 REQUEST
ls "$FAKE_DUMP_DIR"; python3 - <<'PY'      # 取证 C：模型实际收到的文本
import glob, json, os
p = sorted(glob.glob(os.path.join(os.environ["FAKE_DUMP_DIR"], "request-*.json")))[-1]
body = json.load(open(p))
print(p)
for m in body.get("messages", []):
    c = m.get("content")
    text = c if isinstance(c, str) else " ".join(x.get("text", "") for x in c or [] if isinstance(x, dict))
    if "clarify these questions" in text or "doesn't want to proceed" in text:
        print(text)
PY
CHAT_SID=$($D sid)                         # 取回放用的 session；**不能**写成 $CHAT_SID=…（那会执行 "=sid"）
```

预期：
1. **卡片**：`● User declined to answer questions`（不是 `Denied`，不是 203 红），输出区两行
   `· <题干> (<选项> / <选项>)`，**无** `(no output)`。
2. **回合继续**：`provider-log` 出现第 2 条 `REQUEST`；dump 里最后一条 request 含
   `The user doesn't want to proceed with this tool use.` 前缀 + `The user wants to clarify these questions.`
   + `Start by asking them what they would like to clarify.` + `Questions asked:` + 两条问题清单。
3. **回放（必须在 Esc 场景之前做，否则 chat 的 home 已被 reset 删掉）**：

   ```bash
   $D stop
   export FAKE_DUMP_DIR=$TMPDIR/fb-after-replay   # 回放自己的 dump，别混进 chat 的 request
   RESUME_ID="$CHAT_SID" $D start ask             # 不 reset：resume 就是读这个 home
   $D screen                                      # 同一张 declined 卡逐字重现（body 来自 tool_display）
   $D stop
   ```

4. **Esc 一张卡**（独立 work dir + 独立 dump，**不要**碰上一个 home）：

   ```bash
   export FOREBRAIN_RUN_WORK=$TMPDIR/fb-run-after-esc
   export FOREBRAIN_RUN_SESSION=fb-after-esc
   export FOREBRAIN_RUN_PORT=18733
   export FAKE_DUMP_DIR=$TMPDIR/fb-after-esc
   $D reset && $D start ask
   # 同法 4×Down 到 chat 行，然后：
   $D key Escape
   $D screen                                 # 同一张 declined 卡（有 · 问题 行、无 (no output)）
   $D provider-log                           # 仍只有 1 条 REQUEST（回合停止，不产生新请求）
   $D stop
   ```

**Verify**：四段屏/日志/JSON 输出全部贴进「验收记录」。

## Test plan（汇总）

| 层 | 文件 | 用例 |
|---|---|---|
| 文案 | `pkg/tool/format_test.go` | `TestAskClarifyReasonSpeaksClaudeCodeWords` |
| body | `pkg/tool/format_test.go` | `TestUserInteractionDeniedBodyReadsBothShapes`、`TestDeniedToolStepReadsAsTheRefusalEverywhere`（扩） |
| 头词 | `pkg/tool/header_parts_test.go` | denied ask 头词 + golden |
| 引擎 | `pkg/turn/approval_test.go` | `TestDecideAskClarifyDeniesWithClarifyReason` |
| 交互 | `pkg/tui/chat_session_test.go` | `TestQuestionOverlayChatAboutThisRefusesWithClarify`（Step 0 红→绿）、原 cancel 用例重写 |
| 卡片 | `pkg/tui/chat_session_test.go` | `TestDeclinedAskCardDrawsTheQuestionsNotTheClarifyText`、`TestDeniedAskHeaderColor` |
| web | `frontend/src/components/chat/ToolCallCard.test.ts`、`ApprovalCard.test.ts` | denied ask 画 body；chat 点击发 `clarify` |

## Done criteria（机器可查；全是**执行后**的门禁，未实施前不得勾选）

- [ ] `sed -n '/if o.cursor == o.chatIdx()/,/^\t}/p' pkg/tui/overlays.go | grep -c 'AskClarify: true'` → 1，且该段没有返回 `Cancelled: true`。
- [ ] `grep -c 'if choice == "clarify"' pkg/turn/approval.go` → 1；`grep -c 'AskClarify bool' pkg/turn/model.go` → 1
       （只数字段行；字段上方的注释也含 `AskClarify`，所以不要用裸 `grep -c 'AskClarify'`）；
       `grep -c 'choice == "clarify"' pkg/turn/approval.go` → 2（decideAsk + resolvedApproval 幂等）。
- [ ] `grep -rn 'DeniedToolDisplayBody(' pkg | grep -v _test | grep -v 'func ' | grep -c 'DeniedToolDisplayBody([^)]*,[^)]*,[^)]*)'` → 3（三处都是 3 参）。
- [ ] `grep -rn "decision: 'clarify'" frontend/src` → ≥2（`usePendingActions.ts` 的生产调用 + `ApprovalCard.test.ts` 的断言）。
- [ ] `grep -rn 'User declined to answer questions' pkg/tool ai-elements-vue frontend/src` → ≥4
       （`header_parts.go`、`header_parts_test.go`、`testdata/header_actions.json`、`headerWords.ts`）。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/run ./pkg/state -count=1` 全绿。
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0；`gofmt -l pkg/tool pkg/turn pkg/tui` 只有他人的 `pkg/tui/subagent_footer_display_test.go`。
- [ ] `cd frontend && pnpm test ToolCallCard ApprovalCard` 全绿；`pnpm typecheck` exit 0。
- [ ] 真机四项取证（chat / Esc / 模型收到文本 / 回放）齐全并贴进「验收记录」。
- [ ] `git status` 与开工前相比只多出 Scope 列的文件（评审产物 `pkg/tui/zz_review_step0_test.go` 已删；
       他人既有 WIP 原样保留）；**没有 commit**。
- [ ] 计划文件已复制到 `docs/plan/ASK_CHAT_ABOUT_THIS_PLAN.md` 并回填「验收记录」（不 commit）。

## STOP conditions

- 「Current state」里任一锚点的**内容**与摘录不符（尤其 `commitCurrent` 的 chat 分支、`decideAsk` 的
  cancel/accept 结构、`approvalChoice` 的先后顺序、`ToolCallCard.vue` 的 `isRefused` 定义）——说明他人 WIP 已改同一处。
- `pkg/turn/approval.go` 的 `Decide` 已出现 `clarify` 处理，或 `DeniedToolDisplayBody` 已是 3 参（别人先做了）。
- Step 0 的复现**不红**，或红的原因不是 `Cancelled:true`（说明现状已变，先报告）。
- 某步 Verify 连续两次失败（同命令同输出）。
- 需要修改 Out of scope 的文件（尤其 `pkg/gateway/*`、`pkg/state/*`）。
- 真机里 chat 之后 `provider-log` 仍只有 1 条 REQUEST，或 dump 里没有 `The user wants to clarify these questions.`
  —— 说明 in-place deny 链路与摘录不符，停。

## 验收记录（执行者回填）

| 场景 | 命令/证据 | 结果 |
|---|---|---|
| 修前 Go 红 | `go test … -run '^TestQuestionOverlayChatAboutThisRefusesWithClarify$'` | **FAIL** `chat about this must refuse, not cancel: dec={Approved:false Denied:false Cancelled:true …}` |
| 修前真机 | `$D start ask` + 4×Down + Enter，`screen`/`provider-log`/`fb_messages` | `● Denied` + `  └ (no output)`；provider-log 仅 1 条 `REQUEST`；`fb_messages` 末条 = `RejectedStopText` |
| 修后 chat | `$D screen`（declined 卡）、`provider-log`（2 条 REQUEST）、dump request-0002 | `● User declined to answer questions` + `└ · 问题 (60 秒 / 15 秒 / 180 秒)` + `    · 问题 (原始报错 / 结构化错误)`，无 `(no output)`；**2 条 REQUEST**；tool 消息 = `To tell you how to proceed, the user said:` + clarify 全文 + `Questions asked:` + 两条 `(No answer provided)` |
| 修后 Esc | 同一张卡 + `provider-log` 1 条 REQUEST | 同一张 `● User declined to answer questions` + 两行 `· `（无 `(no output)`）；**仍只有 1 条 REQUEST**；`fb_messages` 末条 = `RejectedStopText`（取消语义未变） |
| 修后回放 | `RESUME_ID=… $D start ask` | 同一张 declined 卡逐字重现（`● User declined to answer questions` + 两行 `· 问题 (选项…)`），`resumed 4 messages above` |
| web | `pnpm test ToolCallCard ApprovalCard` | 3 files / **42 tests 全绿**；`pnpm typecheck` exit 0；`pnpm test toolHeader` 10 tests 全绿 |

## Maintenance notes

- 这张卡的 body/头词是**共享契约**：TUI live、TUI replay（`tool_display.body`）、web 全从
  `tool.DeniedToolDisplayBody` 派生；澄清措辞只从 `tool.AskClarifyReason` 出（Go 侧），前端不许拼串。
- `AskClarifyReason` 的固定段是 Claude Code 逐字文本；改它等于改模型契约，必须同步
  `TestAskClarifyReasonSpeaksClaudeCodeWords`。问题清单对齐 Claude 的 `C9S`，答案串对齐 `setAnswer`（`u9S`）：
  单选自由文本取代选择、多选用 ", " 连接。两处**有意适配**（不是逐字等价）：① 多选把 Other 追加进答案串
   （Claude 丢弃它，会丢用户写的话）；② 不生成 `User notes:`（`AskAnswerItem` 只有 `OptionIDs`/`OtherText`，
  没有预览面板那种独立 notes 值）。
- 新增 `HeaderActions()` 词条：任何新 action 都要同时更新 `testdata/header_actions.json` 与
  `ai-elements-vue/.../headerWords.ts`（`frontend/src/locales/toolHeader.test.ts` 会红）。
- **已知遗留（本期不修）**：
  1. subagent 的提问 + chat：子代理 roster 行（`replayApprovalMessage` / `ApprovalResolvedPayload.Reason`）
     仍会显示澄清原文。要做到「只给模型、不进任何界面」需要一个 marker + drop-key
     （与 `tool.PlanReviewDeliveredDisplayKey` 同型）。
  2. web 的 clarify 不带用户已填的部分答案（TUI 带），因为 `actionsApprove` 没有 answer 字段；
     若将来要给，加 `ask_answer_json` 到 `ActionApprovalBody` 并让 `Decide` 用它。
  3. answered ask 的卡头未对齐 Claude（`Asked user` vs `User answered Claude's questions:`）。
- 若 `state.AskForm` 再增字段，`AskClarifyReason` 与 `userInteractionDeclinedBody` 都要同步
  （两者只认 `ID/Prompt/Options[].Label`）。

## Implementation Status

**STATUS: COMPLETE**（2026-11-14，基线 HEAD `a1da14e4`，工作区快照零漂移）

| Step | 状态 | 验证结果 |
|---|---|---|
| 开工前清理 | done | 删 `pkg/tui/zz_review_step0_test.go`（评审复现测试，无产品代码） |
| 漂移检查 | done | 26 文件 12 位哈希全部吻合；11 条锚点 grep 全部命中 |
| Step 0 复现 | done | Go：`FAIL … chat about this must refuse, not cancel: dec={… Cancelled:true …}`；真机：`● Denied` + `└ (no output)`、1 条 REQUEST、DB=RejectedStopText |
| Step 1 `AskClarifyReason` | done | `go build ./pkg/tool` OK |
| Step 2 declined body + 3 参 | done | `go build ./pkg/tool` OK；`TestUserInteractionDeniedBodyReadsBothShapes`、扩写的 `TestDeniedToolStepReadsAsTheRefusalEverywhere` 绿 |
| Step 3 头词 | done | `UPDATE_HEADER_ACTIONS=1` 重生成 golden，`git diff` 只多 `"User declined to answer questions"` 一行；golden 测试绿 |
| Step 4 引擎 clarify | done | `go build ./pkg/turn` OK；`TestDecideAskClarifyDeniesWithClarifyReason`（含幂等 + 空答案）绿 |
| Step 5 TUI chat→clarify | done | Step 0 红测试转绿（含 `AskClarify` 断言） |
| Step 6 choice/resume/画卡 | done | `go build ./pkg/tui` OK；`TestQuestionOverlay*` 绿 |
| Step 7 头色 + 兜底 | done | `go build ./pkg/tui` OK；`TestDeniedAskHeaderColor`（214 / NO_COLOR 244）、`TestDeclinedAskCardDrawsTheQuestionsNotTheClarifyText` 绿 |
| Step 8 Go 测试 | done | `go test ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/run -count=1` 四包全绿 |
| Step 9 web | done | `pnpm test ToolCallCard ApprovalCard usePendingActions` = 3 files / 42 tests 绿；`pnpm typecheck` exit 0；`pnpm test toolHeader` 10 tests 绿 |
| Step 10 全量回归 | done | `gofmt -l` 只剩他人未跟踪 `pkg/tui/subagent_footer_display_test.go`；`go vet -tags fts5 ./...` exit 0；五包（含 `./pkg/state`）全绿（model.go 复跑一次仍全绿） |
| Step 11 真机 | done | chat：declined 卡 + 2 条 REQUEST + dump 含 clarify 全文；Esc：同一张卡 + 1 条 REQUEST + DB=RejectedStopText；回放：同一张卡逐字重现 |

**Done criteria 逐条**：`AskClarify: true` ×1 且同段无 `Cancelled: true`；`if choice == "clarify"` ×1、`AskClarify bool` ×1、`choice == "clarify"` ×2；
`DeniedToolDisplayBody` 生产调用 3 处均 3 参；`decision: 'clarify'` ×3；`User declined to answer questions` ×6；五包测试/vet/gofmt/前端测试/typecheck 全绿；
真机四项取证见「验收记录」；`git status` 只多本计划 Scope 内文件（评审产物已删，他人 WIP 原样保留）；**无 commit**；计划已复制到 `docs/plan/ASK_CHAT_ABOUT_THIS_PLAN.md` 并回填验收记录。

**Deviations（均为非承载性，意图与对外行为不变）**
1. `pkg/turn/model.go`：`AskClarify bool` 之后留一个空行，使该字段自成一个 gofmt 对齐组——否则 gofmt 会把它与 `RequestPermissionsResponse` 对齐成多空格，Done criteria 的 `grep -c 'AskClarify bool'` 会变成 0（该判据只数字段行）。字段位置（`AskAnswerJSON` 旁）与语义不变。
2. 真机脚本：`$D start ask` 后需 `$D submit "请提问"` 才会产生第一条 request（Step 11 的块里省略了这一步，与 Step 0 的写法一致）。
3. `pkg/tool/format.go` 的 `userInteractionDeclinedBody` 在问题为空时提前返回 `""`（与 `clampBody(join(...))` 结果等价，读起来更直白）。

**Notes**
- `pkg/turn/{approval.go,model.go}`、`pkg/tui/{overlays.go,render.go}`、`frontend/src/lib/api.ts`、`frontend/src/composables/usePendingActions.ts`、`pkg/turn/approval_test.go` 内含他人既有 WIP（plan-review 的 `ModelName`、`inlineCodeColor` 等），本计划只在锚点处加代码，未改动其语义；`pkg/turn/model.go` 用了 `gofmt -w`（该文件是自己的锚点文件，格式化只影响 `ToolApprovalDecision` 的对齐）。
- 遗留（未修，已在 Maintenance 列明）：subagent 提问 + chat 的 roster 行仍显示澄清原文；web 的 clarify 不带部分答案；answered ask 头词未对齐 Claude。
