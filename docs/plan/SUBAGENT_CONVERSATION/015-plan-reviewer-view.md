# 计划 015：plan-reviewer 的视图——工具卡片齐全、文字不重复、模型说对

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **漂移检查（先运行）**：
> `git diff --stat bda9505 -- pkg/tui/chat_turn.go pkg/tui/chat_surface.go pkg/tui/reducer.go pkg/tui/render.go pkg/tui/run.go pkg/tui/notify.go pkg/tui/commands.go pkg/tui/chat_slash.go pkg/run/subagent.go pkg/run/config.go pkg/agent/subagent_history.go pkg/event/run_events.go pkg/event/plan_progress.go pkg/run/orchestration_llm.go pkg/tool/request_permissions.go pkg/tool/format.go pkg/turn/subagent_approval.go pkg/gateway/api_extra.go pkg/state/schema_migrations.go frontend/src/composables/useChatStream.ts frontend/src/components/chat/SubagentConversation.vue frontend/src/components/chat/RunWorkedLine.vue frontend/src/views/ChatView.vue frontend/src/locales/index.ts`
> 计划 001、002、010、012、013 可能改过其中一部分；按函数名和注释原文核对"现状"摘录，对不上就 STOP。若计划 002 已经 `DONE`，第 3 步里与它 §5 重合的部分（`HistoryEntry` 的模型字段、`run.AgentModel`）已经存在，核对后复用，不要再写一份。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：MED（改 TUI 的工具步骤钩子取法和流式文字的分段规则，主视图也走这段代码）
- **依赖**：无（第 4 点的卡片在 013/014 里画，本计划提供它需要的事件）
- **类别**：bug
- **基线**：提交 `bda9505`，2026-10-05

## owner 的要求（逐字）

> （附三张 TUI 截图）如forebrain harness tui截图所示，exit_plan_mode审批选择other model to review plan后的plan-reviewer相关功能有严重bug：
>
> 1. plan-reviewer subagent的alt-screen VM视图里的每一条assistant message都是全量，而不是增量，出现重复内容
> 2. plan-reviewer subagent的alt-screen VM视图里只显示了思考内容消息卡片和assistant message消息卡片，没有工具调用消息卡片
> 3. 用户选择的plan-reviewer subagent的llm是zhipuai/glm-5.3-flash，但plan-reviewer subagent的alt-screen VM视图里composer下方显示的llm是zhupuai/glm-5.3，模型是错的
> 4. exit_plan_mode工具审批后，显示subagent plan-reviewer spawned plan-reviewer消息文案需要重新设计，这个文案语义有错误，应该是主agent spawned plan-reviewer subagent，必须改成语义正确且更容易理解的文案

> 必须先用improve skill在docs/plan/SUBAGENT_CONVERSATION生成计划文件，不要改代码

> （D13）没有派发调用的执行 plan-reviewer的消息卡片的header里不要加主语，与其他的subagent_*工具调用消息卡片的header保持一致使用Starting/Running/Ran/Failed to start等

> plan-reviewer 的视图的UI/UX必须与其他subagent的视图保持一致，tui和web都必须显示当前subagent会话的上下文窗口统计信息，例如"65%/1M"

> review completions print the reviewer's full answer into the main conversation as a card -------- it's a bug, must be fixed, output must stay in their own view just like any other subagent

> 如果subagent调用了session_todo工具，Working和Worked for消息还必须显示checkbox图标和进度

截图：① reviewer 视图里三条 assistant 消息，第二条 = 第一条 + 新句子，第三条 = 第二条 + 新句子，中间夹着 `Thought for 41s`、`Thought for 25s`，没有一张工具卡片；② 主视图 `✔ You asked zhipuai / glm-5.3-flash to review the plan`、`◆ Exiting plan mode`、`subagent plan-reviewer spawned plan-reviewer [subagent-e3581eba-…]`；③ reviewer 视图的 footer `viewing plan-reviewer · e358 · esc to return · zhipuai/glm-5.3 · xhigh`。

## 根因（逐条，含证据）

本机状态库（`~/.forebrain/state/forebrain.state.sqlite`，只读）里截图那次 review 的子运行 `8bf4e4f3-…`（会话 `cli-14a2baf9-…`）的事件：`assistant_delta` 1751 条、`reasoning_delta` 15660 条、`reasoning_done` 20 条、`usage_delta` 23 条、**工具事件 0 条**。按 `reasoning_done`/`usage_delta` 把 `assistant_delta` 切段，每一步的文字都是新的、互不重复（第一步 50 字"我将独立核查……关键代码。"，第二步 149 字"关键发现：……"，以此类推）。所以：模型和引擎发出的文字是增量的，重复发生在 TUI；工具调用发生了（20 次 LLM 往返），但一个工具事件都没发出。

### R2（第 2 点，也是第 1 点的触发条件）：reviewer 的运行没有 TUI 的工具步骤钩子

- TUI 把"发布工具步骤"的钩子只装在**回合**上：`ChatSession.installRunAuditStepHook`（`pkg/tui/chat_turn.go:224`）把钩子装到进程级 `tools.SetStepHook`，回合结束时恢复原值（`:346` `return func() { tools.SetStepHook(prev) }`）；`prepareTUIAgentBase` 再把它冻结到这个回合的上下文上（`:474-483`，注释："Detached/async subagents inherit this context and may emit tool events after the foreground turn has returned"）。回合里派发的 subagent 继承这个上下文，所以它们的工具步骤由钩子的 `else` 分支（`agentID != ""`，`:288` 起）以 canonical 事件发布、落库并画进它们自己的视图。
- plan review 不在任何回合里。用户在 `exit_plan_mode` 审批浮层选"Ask another model to review this plan"后，`completeSurfaceApproval` → `completeSurfacePlanReviewRequest` → `runPlanReview`（`pkg/tui/chat_surface.go:969`）→ `planSubagentReviewer.Review`（`:1159`）用的是审批循环的上下文（`chat_session.go:1247` 甚至是 `context.Background()`）。被审批挡住的那个回合此时已经返回，进程级钩子已经恢复，上下文里也没有冻结的钩子。`Review` 给运行上下文只加了 `llm.WithAgentSessionID` 和 `tool.WithRunID`（`:1183-1191`）。于是 reviewer 的每一次工具调用都执行了，但 `tool.StepHookFromContext` 拿不到钩子，没有任何事件发出。
- 文字和思考能到达它的视图，是因为流式输出不靠上下文：`executeSubagent` 里的 `subagentRunContext`（`pkg/run/subagent.go:529`）给每个子运行换上引擎自己的 `EventStreamSink`。

### R1（第 1 点）：TUI 只在"文字开始"时封存思考，从不在"思考开始"时封存文字

`pkg/tui/reducer.go` `reduceMessage`：

- `MsgKindAssistant`（`:1298`）先 `r.flushReasoningFinal(buf, …)` 封存缓冲的思考，再把增量追加进 `buf.assistant`，并把**整个缓冲**作为一帧非 final 的 assistant 帧发出（注释："emit the FULL accumulated text as a live (non-final) frame"）。
- `MsgKindReasoning`（`:1333`）只把增量追加进 `buf.reasoning`，**不碰 `buf.assistant`**。
- `buf.assistant` 只在 `flushBufferedText` / `withFlushedBuffers` 里清空，而它们只在工具帧、用户消息、错误、计划、子运行结束等"别的东西插进来"时被调用。

一个 agent 的一步是"思考 → 文字 → 工具调用"。工具帧在时，它封存上一步的文字；工具帧不在时（R2），下一步的思考进来不封存文字，再下一步的文字接在上一步后面，每一帧都带着从第一步起的全部文字——截图里"全量而非增量"。网页没有这个问题：`appendStreamedText`（`frontend/src/composables/useChatStream.ts:874`）只在**最后一块**同类且未关闭时才续写，思考块之后的文字一定开新块。TUI 的规则不对称是缺陷本身，与 R2 是否修好无关：任何"文字之后紧跟思考、中间没有可见工具帧"的流（例如服务商在一次响应里交替输出思考和文字）都会重复。

### R3（第 3 点）：footer 按"类型"查模型，看不到这次运行被指定的模型

- reviewer 的模型是用户当场选的，以 `run.WithSubagentModelOverride`（`pkg/run/subagent.go:1925`）放在运行上下文里，由 `subagentModelOverrideLLM`（`:1951`）在每次调用时换成那个模型的客户端（`pkg/run/runner.go:958-960`：`ConfiguredModelClient(r.AppCfg, r.activeAgentNameForModel(), override.Provider, override.Model)`）。这个选择**没有记在任何地方**：`agent.HistoryEntry` 没有模型字段，`subagent_spawned` 事件也没有。
- footer 的模型来自 `Renderer.subagentFooterLeftLocked`（`pkg/tui/reducer.go:2904`）：`r.subagentModels[agentType]`，查不到就用主 agent 的 `r.footer.Model`/`ReasoningEffort`。`r.subagentModels` 由 `subagentModelsByType`（`pkg/tui/run.go:1059`）对 `agent.PublicTypeNames()` 逐个调 `SubagentModelSummary(agentType)` → `run.SubagentOwnModel`（`pkg/run/runner.go:1579`）得到：只有公开类型、只看 `agents.definitions[<type>].llm_providers`。`plan-reviewer` 是保留类型，不在其中，于是显示主 agent 的 `zhipuai/glm-5.3 · xhigh`——推理强度也是主 agent 的，不是 reviewer 那个模型配置里的。
- 根因是"模型是执行的属性"被当成了"类型的属性"。同一类型的两次执行可以跑在不同模型上（plan review 就是），只有派发那一刻知道答案。

### R4（第 4 点）：生命周期文字的主语错了

`pkg/tui/reducer.go` `SubagentSpawnedMsg` 分支末尾：`title := "subagent " + t + " spawned"`，内容 `subagentSummary(agentType, taskID, "")` = `plan-reviewer [subagent-…]`，拼出来是"subagent plan-reviewer spawned plan-reviewer [id]"——读起来像"plan-reviewer 这个 subagent 派发了一个 plan-reviewer"。真实语义是：主 agent（被审批挡住的那个运行；reviewer 的运行行是它的子运行，见 `Review` 里 `tool.WithRunID(runCtx, p.RunID)`）启动了一个 plan-reviewer subagent。计划 013（T2、T18）已经决定用"一次执行一张卡片"取代这两行文字（D11）。owner 定下（D13）：plan-reviewer 的卡片不加主语，与其它 `subagent_*` 卡片同一套头部 `Starting`/`Running`/`Ran`/`Failed to start`。要让 `Starting`（已请求、reviewer 还没开始）和 `Failed to start`（还没开始就失败：模型未配置、没有计划）有真实依据，界面必须知道"评审被请求了"这件事；今天它只存在于 TUI 的一个函数调用里，没有任何事件，reviewer 的 spawned 事件也指不回这次请求（`Review` 的上下文里没有工具调用 id，`ParentToolCallID` 为空）。

### R5（"与其他 subagent 的视图保持一致"）：评审的回答被印进了主视图

评审成功后，`runPlanReview`（`pkg/tui/chat_surface.go:1010-1016`）向主视图发一帧 `MsgKindPlan`：标题 `Plan review · <模型> · 125.3s`（`planReviewSummary`，`:1050`，时长还是另一种 `%.1fs` 写法），正文是评审的全文。其它任何 subagent 的回答都只在它自己的视图里（项目长期规则："subagent 说的一切都属于它自己的视图，主视图只有它的卡片"），而这段全文同时已经是 reviewer 视图里它的最后一条回答，并且在重新弹出的审批浮层里（`PlanReviews`）给用户做决定用——主视图这一份是第三次出现。它不落库，回放时也不会出现，实时与回放还不一致。没有任何测试依赖它（`grep -n "MsgKindPlan\|planReviewSummary" pkg/tui/*_test.go` 无输出）。

### R6（同一类，第二条路径）：选"继续规划"后，评审全文又出现在主视图的 `exit_plan_mode` 卡片里

- 拒绝时 TUI 把评审拼进给模型的理由**再**交给审批服务：`completeSurfaceApproval`（`pkg/tui/chat_surface.go:441`）传 `Reason: s.planReviewDenyReason(actionID, decision.DenyReason)`，即 `turn.ComposeDenyFeedback(reviews, userFeedback)`（`pkg/turn/approval.go:982`）——"The user asked … to review this plan … Treat each review as an outside opinion…" 加上每条 `<review model="…">全文</review>`，最后才是用户自己的话。`ApprovalService.Decide` 把它原样存进 `action.Error`（`pkg/turn/approval.go:209` `s.Actions.Deny(ctx, act.ID, reply.Reason)`）。本机状态库核实：截图会话里被拒绝的 `exit_plan_mode` action `f27ec373-…` 的 `error` 有 2479 字，含完整评审。
- 而 `action.Error` 的约定是"用户自己的话"：`pkg/tui/chat_turn.go:706-707` 的注释写着 "The user's feedback lives in the action's Error field, where ActionSvc.Deny stores it"。它被三处当成给人看的文字：实时的被拒卡片（`notifyToolApprovalDenied`，`chat_turn.go:976` 一带，`exit_plan_mode` 时 `content = act.Error`）、`approval_resolved` 事件的 `Reason`（`pkg/gateway/approval.go:422`）、恢复时给模型的拒绝理由（`turn.BuildApprovalResume` → `resume.Reason`，`chat_turn.go:705-709`、`pkg/gateway/api_extra.go:2417`、`pkg/turn/subagent_approval.go:146`）。所以评审全文（连同写给模型的指令）出现在主视图那张被拒的卡片上。
- 回放时更糟：拒绝的工具结果由 `buildDenialMessage`（`pkg/run/orchestration_llm.go:461`）生成，内容是写给模型的 "Tool approval denied by user: the user rejected this … tool call. Try a different approach…\n\nUser feedback: <上面整段>"，落库时**没有** `tool_display` 部分（本机核实 `fb_messages` id 85844 的 parts 只有 `text` 和 `tool_result_meta`），`replayToolMessage`（`pkg/tui/commands.go:981`）于是把这段原文当卡片正文画出来。任何被拒绝的工具在回放时都显示这段写给模型的话，实时却不显示——同一类缺陷。
- 根因：一个字段同时承担"用户说的话"和"给模型的理由"，以及拒绝结果没有自己的显示部分。修法：`action.Error` 回到只存用户自己的话；评审拼进给模型的理由的那一步挪到**恢复时**（构造给模型的拒绝理由的地方）；拒绝结果带上与实时卡片相同的显示部分。

### R7（网页与 TUI 不对齐，所有 subagent 视图）：网页的 subagent 视图没有计时的 Working 行，也没有 Worked for 行

- TUI 的 subagent 视图（包括 plan-reviewer，见截图三 `/ Working (49s)`）运行时有带该 subagent 自己时钟的 Working 行，每次执行结束时由 `subagentWorkedFrames`（`pkg/tui/reducer.go`，`SubagentEndedMsg` 分支调用）在它的视图里画一行 `─ Worked for 2m 05s · 16:42 ───`（`workedForLabel` + 计划进度 + `workedCompletionTime`，`workedStatusLine` 画成整行）。项目的规矩是每一次运行——主回合或 subagent——都以这行收尾。
- 网页的 subagent 视图（`frontend/src/components/chat/SubagentConversation.vue:101-113`）运行时只有一个圆点和固定的 `正在工作…`（`chat.subagentWorking`，`frontend/src/locales/index.ts:770/1684`），没有时长；结束后什么都没有（只在没有回答时显示 `subagentNoAnswer`）。主对话的 Worked for 行由 `RunWorkedLine.vue` 画，但它只接收 `ChatMessage`（`workedLineOf(props.message, t)`），subagent 的时间线用不上。
- 根因：网页的 subagent 时间线没有"一次执行结束"的块，Working 行也不知道这次执行从何时开始。

### R8（TUI，所有 subagent）：subagent 的时钟按 agent 记，不按执行记

TUI 已经在 subagent 的 Working 行和 Worked for 行里显示它自己的清单进度（`workingStatusFormatter`，`pkg/tui/run.go` 约 `:3740`：`Working (49s) · ☑2/5 · <进行中的任务>`；`subagentWorkedFrames` 用同一个 `formatWorkingCounters`），数据来自 `Tracker.ObserveAgentPlanProgress`（`pkg/tui/reducer.go:6117`）。但时钟错了：`Tracker.ObserveAgent`（`:6063`）只在第一次见到这个 agent 时记开始时间，`ObserveAgentEnded`（`:6099`）记结束时间后，再也没有地方把它清掉。一个 subagent 被继续（`subagent_continue`，以后还有用户在视图里发起的执行、额度恢复后的自动继续）时：第二次执行期间 `SnapshotAgentRun` 报 `Ended == true`，Working 行（连同清单进度）**不显示**；第二次结束时 Worked for 的时长从**第一次**开始算起，把两次执行之间的空闲也算了进去。

### R9（网页）：清单进度在 Working 行上没有勾选图标，主对话那行还是写死的英文

- 网页主对话的 Working 行由 `formatRuntimeStatusLabel`（`frontend/src/composables/useChatStream.ts:1390`）拼成一个字符串，清单进度写成 `Tasks 2/5 • <说明>`（`:1394`）——写死的英文（网页必须中英文），没有勾选图标；而同一页面的 Worked for 行（`RunWorkedLine.vue`）用 `SquareCheck` 图标加 `2/5`，TUI 两行都是 `☑2/5`。
- 进行中的任务名也不一致：网页取 `planUpdate.explanation`（`applyPlanRuntimeStatus`，`:2152`），TUI 取"进行中的最短任务名"（`shortestActiveTaskTitle` → `event.PlanProgressOf`，规则在 `pkg/event/plan_progress.go:26`，注释说网页的 Worked for 行用的是同一规则）。实时的这一行网页没有这份结果可读：`PlanUpdatedPayload`（`pkg/event/run_events.go:379`）有 `Title`、`Items`、`Completed`、`Total`、`Explanation`、`AgentID`，没有 `Active`。
- 网页的 subagent 视图（R7）连 Working 行的时长都没有，更没有清单进度。

### plan-reviewer 视图与其它 subagent 视图的一致性清单

owner 要求 plan-reviewer 的视图在 TUI 和网页上与其它 subagent 的视图完全一致，并显示它自己的上下文窗口统计（例如 `65%/1M`）。逐项核对的结果和负责的计划：

| 方面 | 今天 | 负责 |
| --- | --- | --- |
| 工具卡片 | 没有（R2） | 本计划 |
| 每条回答只有这一步的文字 | 重复（R1） | 本计划 |
| footer 左侧的模型和推理强度 | 主 agent 的（R3） | 本计划 |
| 回答只在它自己的视图里 | 主视图印一份评审卡片（R5）；选"继续规划"后被拒的 `exit_plan_mode` 卡片里又是全文，回放时还带着写给模型的指令（R6） | 本计划 |
| 主视图里的卡片 | 生命周期文字行，主语错误（R4） | 013/014（本计划提供事件） |
| footer 右侧 / 网页视图里它自己的上下文窗口统计 `N%/窗口` | 所有 subagent 视图都没有 | 006（按 agent 计算，窗口取 `run.AgentModel`，覆盖模型由本计划记录）、007（TUI footer 右侧）、008（网页 subagent 视图）；三份计划已为 plan-reviewer 各加一条专门的测试 |
| 运行中的 Working 行（带它自己的时长）与每次执行结束的 Worked for 行 | TUI 有；网页两样都没有，所有 subagent 都是（R7） | 本计划 |
| 两行里的清单进度（调用过 `session_todo` 时 `☑N/M · 进行中的任务`） | TUI 有，但被继续的 subagent 第二次执行时 Working 行消失、Worked for 时长算错（R8）；网页 subagent 视图没有，网页主对话写死英文 `Tasks N/M`、无图标、任务名规则不同（R9） | 本计划 |
| 在视图里直接对话、独享队列、Esc、`/compact` | 所有 subagent 都没有 | 002–008（D2：内部保留类型同样适用） |
| 网页上能打开它的视图 | 网页不能发起评审；TUI 发起的评审在网页上有 subagent 视图 | 016（网页发起评审）、008（视图本身） |

## 设计

### 1. reviewer 的运行带上 TUI 的工具步骤钩子（R2）

- 把 `installRunAuditStepHook` 拆成两半：`func (s *ChatSession) runAuditStepHook(sessionID string, prev tool.StepHook) tool.StepHook`（只构造钩子，函数体就是今天传给 `tools.SetStepHook` 的那个闭包，`prev` 作为参数传入）和原来的 `installRunAuditStepHook`（取 `tools.StepHook()` 作为 `prev`，调用前者并 `SetStepHook`，返回恢复函数）。行为不变，`pkg/tui/run_test.go` 里三个调用 `installRunAuditStepHook("sid")` 的测试不改也必须通过。
- `planSubagentReviewer.Review` 构造 `runCtx` 时加：

  ```go
  // The review runs outside any turn — the gated run has already returned —
  // so it carries no turn context and no frozen step hook. Its tool steps
  // reach its own view only through the same hook a turn freezes onto the
  // context its subagents inherit.
  runCtx = tool.WithStepHook(runCtx, s.runAuditStepHook(sessionID, s.Env.Tools().StepHook()))
  ```

  （`s.Env.Tools()` 为 nil 的会话里 `Review` 已经因为 `s.runner()` 为 nil 提前返回；执行者核对这一点，若不成立 STOP，不要加 nil 判断。）
- 不改引擎。计划 016 把 plan review 下沉到共享层时，钩子由各个界面作为参数传入，形状与这里相同。

### 2. 换一种块就封存上一种（R1）

- `pkg/tui/reducer.go` 新增 `flushAssistantFinal(buf *agentBuffer) []Frame`，与 `flushReasoningFinal` 对称：缓冲里有文字就返回一帧 final 的 `FrameAssistant` 并清空；`flushBufferedText` 改为调用它（行为不变）。
- `MsgKindReasoning` 分支在追加思考之前先 `out := r.flushAssistantFinal(buf)`，返回 `append(out, <思考帧>)`。注释写明规则："a block of one kind ends when a block of the other kind starts, in either direction; the tool frame that usually sits between them is not what ends the text"。
- 网页对齐同一规则（不是可见缺陷，但两边的分段规则必须相同）：`useChatStream.ts` 里所有处理 `reasoning_delta` 的地方（`routeSubagentEvent` 和主对话的处理，执行者用 `grep -n "'reasoning_delta'" frontend/src/composables/useChatStream.ts` 找全），在 `appendStreamedText(…, 'thinking', …)` 之前先 `closeStreamedBlocks(blocks, ['assistant'], evt.createdAt)`，与 `assistant_delta` 先关思考对称。

### 3. 模型是执行的属性：派发时记下、事件里带上、footer 按 agent 读（R3）

与计划 002 §5 是同一件事，这里先做（002 未完成时由本计划实现，002 完成时复用它的实现）：

- `pkg/agent/subagent_history.go` 的 `HistoryEntry` 加 `ModelProvider string \`json:"model_provider,omitempty"\``、`Model string \`json:"model,omitempty"\``，注释："the model a dispatch-time override put this agent on; empty when it runs on its definition's or the conversation's model"（与 002 §5 逐字相同）。
- `RunPlanReviewSubagent`（`pkg/run/subagent.go:2088`）在调 `dispatchSubagent` **之前**就把覆盖放到上下文上（`ctx = WithSubagentModelOverride(ctx, override)`），`run` 回调里不再单独加；`prepareSubagentExecutionResolved` 生成 `entry` 时，`subagentModelOverrideFromContext(baseCtx)` 有值就写进这两个字段。执行者确认 `prep.ctx` 由 `baseCtx` 派生、覆盖随之进入 reviewer 的每次调用；不是的话 STOP。
- `pkg/run/config.go`（紧挨 `PrimaryModelForSession`，`:1721`）新增，签名比 002 §5 多一个 `effort`（002 的调用方只取前两个返回值）：

  ```go
  // AgentModel names the model one agent of a conversation runs on, and the
  // reasoning effort its configuration gives that model, the way the runtime
  // routes its calls. subagent is nil for the primary agent. A subagent
  // dispatched with a model override runs on it; a typed subagent whose
  // definition has llm_providers runs on the first of them; the primary
  // agent, a fork and a typed subagent without a chain of its own run on the
  // conversation's model.
  func AgentModel(r *Runner, conversationSessionID string, subagent *agent.HistoryEntry) (provider, model, effort string)
  ```

  覆盖分支的推理强度：用 `resolveAgentProvider(r.AppCfg, r.activeAgentNameForModel(), modelSelector{provider: …, model: …})` 取到那条 provider 配置，`reasoningEffortFromParams(provider.Params)`（与 `ConfiguredModelClient` 建客户端用的是同一条配置）。类型分支用 `SubagentOwnModel`（它已返回 effort）。对话分支用 `PrimaryModelForSession` 加 `PrimaryReasoningEffort(r)`。
- `pkg/event/run_events.go` 的 `SubagentSpawnedPayload` 加 `ModelProvider`、`Model`、`ReasoningEffort`（`json:"model_provider,omitempty"` 等），注释："the model this execution runs on, resolved once when it starts; surfaces show it rather than re-deriving it from the agent's type"。`notifySubagentSpawnedDirect`（`pkg/run/subagent.go:1149`）用 `AgentModel(fac.Owner, entry.SessionID, &entry)` 填它们。
- TUI：`SubagentSpawnedMsg` 加同名三个字段，`pkg/tui/notify.go:789` 和 `pkg/tui/commands.go:795` 两处构造时带上。渲染器的 `subagentModels` 改为**按 agent key** 存，在 `subagentTypes[agentID]` 的同一处填写：`Renderer.NoteSubagentSpawned`（`pkg/tui/render.go:4253`）加一个 `model ComposerFooter` 参数，调用点一并改；删除按类型设置整张表的 `Renderer.SetSubagentModels`（`:4271`）及其调用。值取自 spawned 消息（`Model` 按 footer 今天的口径拼成 `provider/model`，见 `pkg/tui/run.go:1070` 的 `strings.ReplaceAll(…, " / ", "/")`）；`subagentFooterLeftLocked` 用 `r.subagentModels[agentID]`。spawned 消息没有模型字段（本改动之前录下的旧事件）时 footer 不写模型这一段——不再拿主 agent 的模型冒充。
- 删除不再有调用者的：`subagentModelsByType`、`Renderer.SetSubagentModels`、`Session` 接口的 `SubagentModelSummary` 方法、`ChatSession.SubagentModelSummary`、`run_test.go` 里假 session 的对应实现与 `subagentModels` 字段（`grep -rn "SubagentModelSummary\|subagentModelsByType" pkg` 确认后删）。`run.SubagentOwnModel` 保留（`AgentModel` 用它）。
- 网页（与 TUI footer 对齐的同一份事实）：`SubagentTranscript` 加 `modelProvider?`、`model?`、`reasoningEffort?`，`subagent_spawned` 处理时取自载荷；`SubagentConversation.vue` 头部在状态旁边显示 `<provider>/<model> · <effort>`（`--forebrain-muted-text`，与 token 数同一行）。没有就不显示。

### 4. 评审请求是这张卡片的"派发调用"（R4，D13）

本计划只提供事实；卡片怎么画在计划 013（TUI）、014（网页），头部是 `Starting 1 plan-reviewer task…` → `Running 1 plan-reviewer task…` → `Ran 1 plan-reviewer task`，还没开始就失败时是 `Failed to start 1 plan-reviewer task`，不加主语。

- `pkg/event/run_events.go` 新增两个事件（与第 3 节的字段放在同一个文件）：

  ```go
  // RunEventPlanReviewStarted is a second opinion being asked for an
  // exit-plan approval. ReviewID is the dispatch id of the review run: the
  // reviewer's subagent_spawned names it as its ParentToolCallID, the way a
  // subagent names the tool call that dispatched it.
  RunEventPlanReviewStarted = "plan_review_started"
  // RunEventPlanReviewed ends one: Outcome is "done", "stopped",
  // "timed_out" or "failed", Error the raw error of a failed review.
  RunEventPlanReviewed = "plan_reviewed"

  type PlanReviewStartedPayload struct {
  	ActionID string `json:"action_id"`
  	ReviewID string `json:"review_id"`
  	Provider string `json:"provider,omitempty"`
  	Model    string `json:"model"`
  	Label    string `json:"label,omitempty"`
  }

  type PlanReviewedPayload struct {
  	ActionID   string `json:"action_id"`
  	ReviewID   string `json:"review_id"`
  	Provider   string `json:"provider,omitempty"`
  	Model      string `json:"model"`
  	Label      string `json:"label,omitempty"`
  	Text       string `json:"text,omitempty"`
  	DurationMs int64  `json:"duration_ms,omitempty"`
  	Outcome    string `json:"outcome"`
  	Error      string `json:"error,omitempty"`
  }
  ```

  （计划 016 把评审结果的唯一来源定为 `plan_reviewed`，用的就是这个载荷，不再另定义。）
- `ChatSession.runPlanReview`（`pkg/tui/chat_surface.go:969`）：开头生成 `reviewID := "plan-review:" + uuid.NewString()`，在读计划之前发布 `plan_review_started`（发布到被挡住的运行所在的会话、`RunID` 为被挡住的运行，用 `s.runner().Events.Publish`，与 `installRunAuditStepHook` 发布 canonical 事件的写法相同），并把 `reviewID` 传给 `Review`（`turn.Request` 已有字段放不下时，给 `planSubagentReviewer` 加字段，不改 `turn.Request`）；`Review` 构造运行上下文时 `runCtx = tool.WithToolUseID(runCtx, reviewID)`，于是 `prepareSubagentExecutionResolved`（`pkg/run/subagent.go:886`）记下 `ParentToolCallID = reviewID`，spawned 事件带上它。
- 每一个结束路径都发布且只发布一次 `plan_reviewed`：成功（`Outcome:"done"`、`Text`、`DurationMs`）；`notifyPlanReviewFailure` 今天区分的三种失败对应 `stopped`（`context.Canceled`）、`timed_out`（`context.DeadlineExceeded`）、`failed`（其它，`Error` 为原文）。读计划失败、`ErrNoPlan`、`planReviewerFor` 失败这些"还没开始"的失败同样发布（`failed`）。
- 本计划不改界面上的提示句：`notifyPlanReviewFailure` 和 `appendPlanReview` 照旧（013 用卡片取代提示句，016 把结果存储换成这个事件）。

### 5. 评审的回答只留在 reviewer 自己的视图里（R5）

- 删除 `runPlanReview` 成功分支里发 `MsgKindPlan` 的那段 `s.notifyUI(...)` 和只为它存在的 `planReviewSummary`（`grep` 确认没有别的调用者）。`appendPlanReview` 保留（审批浮层靠它显示评审，016 再把它换成事件）。
- 用户看到评审的地方：reviewer 视图里它的最后一条回答（与任何 subagent 相同）、重新弹出的审批浮层（`PlanReviews`）、主视图里 plan-reviewer 的卡片（可点进视图）。

### 6. 被拒绝的审批：用户的话与给模型的理由分开，拒绝结果带显示部分（R6）

- **决定时只存用户的话。** `completeSurfaceApproval` 的 `Reason` 改回 `decision.DenyReason`（用户在"继续规划"里输入的原文），`planReviewDenyReason` 不再在这里调用。`action.Error` 于是始终是用户自己的话，实时被拒卡片、`approval_resolved` 的 `Reason` 自然只显示它。
- **恢复时再拼给模型的理由。** TUI 的恢复路径（`chat_turn.go:705-709`，`denyReason = resume.Reason` 那里）对 `exit_plan_mode` 改为 `denyReason = turn.ComposeDenyFeedback(s.planReviewResults(actionID), resume.Reason)`——给模型的文字与今天逐字相同，只是换了组装的位置（缓存不受影响：它是消息尾部的工具结果）。网关的恢复路径（`api_extra.go:2417`）今天没有评审可拼，保持原样；计划 016 把"按 action 取评审并拼理由"做成共享函数后，两条恢复路径都调用它。
- **拒绝结果带上与实时卡片相同的显示部分。** `tool.ToolApprovalResumeState`（`pkg/tool/request_permissions.go:525`）加 `DenyFeedback string`（用户的话），TUI 与网关构造它的地方填 `resume.Reason`（即 `action.Error`）。`buildDenialMessage` 生成的工具结果附带显示部分：正文是"实时卡片显示的那句"，状态 `denied`。"实时卡片显示的那句"只实现一份，放在 `pkg/tool`（例如 `func DeniedToolDisplayBody(toolName, feedback string) string`：`exit_plan_mode` 返回用户的话，为空时 `(no output)`；`request_permissions` 返回 `The user did not approve the requested permissions`；其它返回空串），`notifyToolApprovalDenied` 也改为调用它。显示部分怎么挂到工具结果上，照 `replayPendingToolCalls` 里 `ToolDisplay: result.display` 那条已有的路径（同一个文件，`orchestration_llm.go` 约 `:520`）——执行者先读那段代码确认被拒结果也能走同一条持久化路径；走不通就 STOP 回来商量，不要另造一种显示格式。
- 旧会话里已经落库、没有显示部分的被拒行：回放时仍会显示写给模型的那段（含评审全文）。按 README 决策 D15 处理（第 2c 步）。

### 7. 网页 subagent 视图的 Working 与 Worked for 行（R7）

- `useChatStream.ts`：`TimelineBlock` 加 `{ kind: 'worked'; id: string /* 'worked:' + executionId */; durationMs: number; finishedAt: string; plan?: WorkedPlanProgress }`。处理 `subagent_ended` 时往那个 subagent 的 `blocks` 追加一块：时长 = 这次执行的结束时间（载荷的 `finishedAtMs`，计划 012 加入；没有就用事件的 `createdAt`）减去同一 `executionId` 的 `subagent_spawned` 的 `createdAt`；`plan` 取这个 subagent 最近一次计划更新的进度（与主对话 Worked for 行取进度的方式相同，执行者照 `workedLineOf` 的写法）。同一 `executionId` 只追加一次；刷新后由事件重放得到同样的块。每一次执行（首次派发、continue、用户在视图里发起的执行）各有一行，与 TUI 一致。
- 呈现：把 `RunWorkedLine.vue` 改为接收一个已算好的 `WorkedLine`（`line` 属性），主对话处传 `workedLineOf(msg, t)`，subagent 视图处传 `formatWorkedDurationLabel(block.durationMs, t, block.finishedAt, block.plan)`——同一个组件、同一种样子，不复制一份模板。`SubagentConversation.vue` 的时间线遇到 `worked` 块就画它。
- Working 行：`chat.subagentWorking` 改为 `正在工作（{duration}）` / `Working ({duration})`，`duration` 用 `formatRuntimeDuration(now - 当前执行开始时间)`，每秒更新（与主对话运行时计时同一个刷新方式）；开始时间取当前执行的 `subagent_spawned` 的 `createdAt`。
- TUI 不改（它已经这样）；本计划的 TUI 测试只核对 plan-reviewer 视图也有这两行。

### 8. TUI：subagent 的时钟按执行记（R8）

- `Tracker.ObserveAgent(agentID string, at time.Time)` 改为 `ObserveAgent(agentID, executionID string, at time.Time)`：`agentRun` 加 `executionID`；收到与当前不同的 `executionID` 时 `startedAt = at`、`endedAt` 清零（清单进度保留——同一个 agent 的清单跨执行延续，新的 `session_todo` 会覆盖它）；同一个 `executionID` 的重复送达不重置。调用点：`reducer.go` 的 `SubagentSpawnedMsg` 分支传 `m.ExecutionID`。
- 其余不变：Working 行与 Worked for 行已经带 `☑N/M · 进行中的任务`。

### 9. 网页：两行都显示勾选图标与进度，规则与 TUI 相同（R9）

- 引擎：`PlanUpdatedPayload` 加 `Active string \`json:"active,omitempty"\``，在构造这个载荷的地方（执行者 `grep -rn "PlanUpdatedPayload{" pkg` 找到 session_todo 生成它的那一处）用 `event.PlanProgressOf(items, completed, total, explanation).Active` 填写。TUI 的 `shortestActiveTaskTitle` 改为直接读它（同一个结果，不再各算一次）。
- 呈现：把 `RunWorkedLine.vue` 里"勾选图标 + `done/total` + 进行中的任务"那一段提成一个小组件（例如 `PlanProgressSegments.vue`，接收 `WorkedPlanProgress`），Worked for 行、主对话的 Working 行、subagent 视图的 Working 行三处共用。
- 主对话 Working 行：`formatRuntimeStatusLabel` 改为返回 `{ label, plan? }`（`label` 不再拼 `Tasks …`），`ChatView.vue:440-448` 在 `label` 后画 `PlanProgressSegments`；`applyPlanRuntimeStatus` 的 `planActive` 改取载荷的 `active`。删除写死的 `Tasks`。
- subagent 视图：`SubagentTranscript` 记录它最近一次计划更新的 `{done, total, active}`（`routeSubagentEvent` 处理该 agent 的 `plan_updated` 时更新）；Working 行为 `正在工作（49s）` 加 `PlanProgressSegments`；第 7 节的 `worked` 块的 `plan` 取结束那一刻的值。

## 缓存影响

- 第 1、2、3 节不改任何发给模型的内容：工具表、system、消息都不变；`subagent_spawned` 只是界面事件。
- reviewer 的前缀：它一直在自己的 worker 会话里、用自己的模型，前缀与本改动无关。
- `PlanUpdatedPayload.Active` 只在界面事件里；`session_todo` 返回给模型的结果不变（执行者核对生成载荷的那一处没有改到工具结果文本）。
- 执行者在第 6 步记录主会话命中率（真机会话的 `usage_json`），与 README 记录的基线比较，不得下降。

## 范围

**只改这些文件：** `frontend/src/components/chat/RunWorkedLine.vue`、`frontend/src/components/chat/PlanProgressSegments.vue`（新建）、`frontend/src/views/ChatView.vue`（只改 Working 行那一段）、`pkg/event/plan_progress.go`（如需）、生成 `PlanUpdatedPayload` 的那个已有文件、`frontend/src/locales/index.ts`、`pkg/state/schema_migrations.go` 及其测试（第 2c 步，D15 = B）、`pkg/tool/request_permissions.go`、`pkg/tool/format.go`（放 `DeniedToolDisplayBody`）、`pkg/run/orchestration_llm.go`、`pkg/gateway/api_extra.go`（构造 `ToolApprovalResumeState` 处填 `DenyFeedback`）、`pkg/turn/subagent_approval.go`（同上）、`pkg/tui/chat_turn.go`、`pkg/tui/chat_surface.go`、`pkg/tui/reducer.go`、`pkg/tui/render.go`、`pkg/tui/run.go`、`pkg/tui/notify.go`、`pkg/tui/commands.go`、`pkg/tui/chat_slash.go`、`pkg/run/subagent.go`、`pkg/run/config.go`、`pkg/agent/subagent_history.go`、`pkg/event/run_events.go`、`frontend/src/composables/useChatStream.ts`、`frontend/src/components/chat/SubagentConversation.vue`，以及它们对应的测试文件。

**不要动：** 生命周期文字行与卡片的绘制（013/014 负责，第 4 点）；`notifyPlanReviewFailure` 的提示句与 `appendPlanReview` 的内存存储（013、016 负责）；plan review 下沉到共享层（016）；`continueSubagentExecution` 里"继续时把覆盖装回上下文"（002 §6）；任何工具的描述、参数 schema、返回值。

## 步骤

### 第 1 步：先写失败的测试

1. `pkg/tui/reducer_test.go`（紧挨 `TestReducerFlushesReasoningBeforeAssistant`，`:455`）加 `TestReducerSealsAgentTextWhenReasoningStarts`：对 `AgentID: "task-r"` 依次喂 assistant `"step one."`、reasoning `"think"`、assistant `"step two."`；断言喂思考时返回的帧是 `[final FrameAssistant "step one.", FrameThinking]`，喂第二段文字时那一帧的 `Content` 是 `"step two."`（不是 `"step one.step two."`）。
2. `pkg/tui/run_test.go`（照 `:180-225` 那个用 `installRunAuditStepHook("sid")` 的测试搭建）加 `TestPlanReviewRunContextPublishesTheReviewersToolSteps`：不调用 `installRunAuditStepHook`，改为调用第 1 节将要提取的"构造 reviewer 运行上下文"的那段（执行者把 `Review` 里构造 `runCtx` 的几行提成 `func (s *ChatSession) planReviewRunContext(ctx context.Context, sessionID string) context.Context` 以便测试），从它取 `tool.StepHookFromContext(runCtx, nil)`，用 `tool.WithHookAgentID(tool.WithRunID(runCtx, run.ID), "plan-review-1")` 调一次 `read_file` 完成步骤，断言 UI 收到 `AgentID == "plan-review-1"` 的工具消息。
3. `pkg/tui/render_test.go` 或 `chat_session_test.go`（执行者按 `subagentFooterLeftLocked` 已有测试所在的文件放：`grep -rn "subagentFooterLeftLocked\|viewing " pkg/tui/*_test.go`）加 `TestSubagentFooterNamesTheModelThisExecutionRunsOn`：一个 `plan-reviewer` 的 spawned 消息带 `ModelProvider:"zhipuai", Model:"glm-5.3-flash", ReasoningEffort:"high"`，主 footer 是 `zhipuai/glm-5.3 · xhigh`；进入该 agent 的视图后 footer 含 `zhipuai/glm-5.3-flash · high`、不含 `glm-5.3 · xhigh`。再加一条：spawned 消息没有模型字段时 footer 不含任何模型。
4. `pkg/run` 加 `TestAgentModelResolvesEachKindOfAgent`（与 002 第 3 步同名；002 已完成则跳过本条，改为扩展它覆盖 effort）：主 agent（nil）、fork、无自有模型的 typed、有自有模型的 typed、带覆盖的 plan-reviewer（覆盖的推理强度取那条 provider 配置的 `params.reasoning.effort`）。再加 `TestPlanReviewSpawnEventNamesTheChosenModel`：`RunPlanReviewSubagent` 带覆盖派发后，发布的 `subagent_spawned` 载荷 `Model` 等于覆盖的模型，`HistoryEntry` 的 `Model` 同样。
5. `pkg/tui/chat_session_test.go`（紧挨 `TestPlanReviewFailureKeepsTheApprovalAndReportsIt`，`:12556`，照它用 `planReviewerFactory` 的搭建）加 `TestPlanReviewPublishesItsStartAndEnd`：成功一次、失败一次（`planReviewerFactory` 返回错误），各断言 runner 事件里有成对的 `plan_review_started`/`plan_reviewed`（`review_id` 相同、`Outcome` 分别为 `done`/`failed`）；再用真实 reviewer 路径（第 2 条测试的上下文构造）断言运行上下文的 `tool.ToolUseIDFromContext` 等于 `review_id`。
6. `pkg/tui/chat_session_test.go` 加 `TestPlanReviewAnswerStaysInTheReviewersView`：照 `TestPlanReviewLeavesTheApprovalPendingAndReachesThePlanner`（`:12482`）的搭建跑一次成功的评审，断言 UI 没有收到 `Kind == MsgKindPlan` 且 `AgentID == ""` 的消息，而重建的审批请求的 `PlanReviews` 里有这次评审。
7. `pkg/tui/chat_session_test.go` 加 `TestDeniedExitPlanCardShowsOnlyTheUsersWords`：评审一次后选"继续规划"并输入 `改成先写测试`；断言 `action.Error == "改成先写测试"`；UI 收到的被拒 `exit_plan_mode` 卡片内容就是这句、不含 `<review`；交给恢复的 `DenyReason` 与今天 `ComposeDenyFeedback` 的结果逐字相同（含评审）、`DenyFeedback == "改成先写测试"`。`pkg/run` 加 `TestDeniedToolResultCarriesItsDisplay`：拒绝后落库的工具结果行有 `tool_display` 部分，正文等于 `DeniedToolDisplayBody` 的结果；`pkg/tui/commands_test.go` 加回放用例：这样的行回放出的卡片正文不含 `Tool approval denied by user`。
8. 前端：`useChatStream.test.ts` 加——同一 subagent 两次执行（首次与 continue）各收到一对 spawned/ended，`blocks` 里有两块 `worked`，时长各按自己的执行算，重复送达的 ended 不产生第三块；`SubagentConversation` 组件测试：运行中显示 `正在工作（Ns）`、推进假定时器后数字变化，结束后显示 `已工作 …` 行且与主对话的 Worked for 行是同一个组件。TUI：`pkg/tui/reducer_test.go` 加 `TestPlanReviewerViewClosesWithWorkedFor`：`AgentType: "plan-reviewer"` 的 spawned/ended 之后，该 agent 的帧里有一行以 `Worked for` 开头的状态帧。
9. TUI：`pkg/tui/reducer_test.go` 加 `TestContinuedSubagentGetsAClockPerExecution`：同一 agent 的两次执行（不同 `ExecutionID`，第二次在第一次结束 10 分钟后开始、跑 30 秒）；第二次执行期间 `SnapshotAgentRun` 的 `Ended == false`、时长从第二次开始算；第二次结束后的 Worked for 行时长是 30s；两次之间有 `PlanUpdatedMsg{AgentID}`（`Completed:2, Total:5`）时，Working 行与 Worked for 行都含 `☑2/5`。网页：`useChatStream.test.ts` 加——主对话 Working 行的结构里有 `plan: {done:2,total:5,active}` 且 `label` 不含 `Tasks`；subagent 收到 `plan_updated(agent_id)` 后它的 Working 行与 `worked` 块带同样的进度；`active` 取载荷的 `active` 而不是 `explanation`。`pkg/event` 或生成载荷处加测试：`Active` 与 `PlanProgressOf` 的结果相同。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/run -run 'TestReducerSealsAgentTextWhenReasoningStarts|TestPlanReviewRunContextPublishesTheReviewersToolSteps|TestSubagentFooterNamesTheModelThisExecutionRunsOn|TestAgentModelResolvesEachKindOfAgent|TestPlanReviewSpawnEventNamesTheChosenModel|TestPlanReviewPublishesItsStartAndEnd|TestPlanReviewAnswerStaysInTheReviewersView|TestDeniedExitPlanCardShowsOnlyTheUsersWords|TestDeniedToolResultCarriesItsDisplay' -count=1` → 全部失败（编译失败也算）。第 1、2 条不失败就 STOP（根因判断与代码不符）。

### 第 2 步：reviewer 的钩子、评审事件、回答只留在它的视图（设计第 1、4、5 节）

**验证**：第 1 步第 2、5、6 条转绿；`grep -n "planReviewSummary" pkg/tui/*.go` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'RunAuditStepHook|PlanReview' -count=1` → `ok`。

### 第 2b 步：被拒绝的审批（设计第 6 节）

**验证**：第 1 步第 7 条转绿；`grep -n "planReviewDenyReason" pkg/tui/chat_surface.go` 只剩恢复路径那一处调用（或已删除、由恢复路径直接调 `ComposeDenyFeedback`）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/run ./pkg/turn ./pkg/gateway -count=1` → `ok`。

### 第 2c 步：旧会话里的被拒行（D15：owner 选 B）

在 `pkg/state/schema_migrations.go` 加下一个版本号的只改数据的迁移（与计划 012 第 7 步同样的规则：以当时最大版本 +1，不抢别的计划已占的号），给 `fb_messages` 里 `role='tool'`、`content` 以 `Tool approval denied by user: the user rejected this ` 开头、`parts` 里还没有 `tool_display` 部分的行追加一个 `tool_display` 部分：工具名从这句话里取（`this <tool> tool call`），正文按 `DeniedToolDisplayBody` 的规则；D15 选 B 时，`content` 含 `\n\nUser feedback: ` 且其后不含 `<review model=` 的，用户的话取其后的原文，其余为空。`tool_meta_json` 写 `{"tool_name":<工具名>,"status":"denied"}`。测试照计划 012 第 7 步的写法（上一版本的库、插入三种行、升级后断言）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → `ok`；在 scratch 里复制本机状态库跑一次，id 85844 那一行（拒绝理由里有评审）回放时只显示 `(no output)`；另找一条没有评审的被拒行（理由里有 `User feedback: ` 而没有 `<review model=`）回放时显示当时用户输入的原话。

### 第 3 步：封存规则（设计第 2 节）

**验证**：第 1 步第 1 条转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（如有既有测试断言"思考不封存文字"，那是在断言缺陷，改它并在报告里列出；改动超过 3 个既有测试就 STOP 报告）；`cd frontend && corepack pnpm test -- useChatStream` → 通过，并新增一条：同一 agent 的 assistant → reasoning → assistant 事件之后，第一个 assistant 块 `open === false`。

### 第 4 步：执行的模型（设计第 3 节）

**验证**：第 1 步第 3、4 条转绿；`grep -rn "SubagentModelSummary\|subagentModelsByType\|SetSubagentModels" pkg` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/agent ./pkg/event -count=1` → `ok`；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 退出码 0。

### 第 4b 步：网页 subagent 视图的 Working 与 Worked for（设计第 7 节）

**验证**：第 1 步第 8 条转绿；`cd frontend && corepack pnpm test` 全部通过；`corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` 退出码 0；`grep -n "subagentWorking" frontend/src/locales/index.ts` 中英文都带 `{duration}`。

### 第 4c 步：清单进度与按执行的时钟（设计第 8、9 节）

**验证**：第 1 步第 9 条转绿；`grep -n "Tasks \${" frontend/src/composables/useChatStream.ts` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/event ./pkg/tool -count=1` → `ok`；`cd frontend && corepack pnpm test` 全部通过。

### 第 5 步：全量回归

**验证**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` → `ok`（`Runner` 字段和导出方法数不变：`AgentModel` 是包级函数）；`scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` → 退出码 0；死代码与开工前基线一致或更少；`cd frontend && corepack pnpm test` 通过。

### 第 6 步：真机（tmux + 真实模型）

按 README 全局规则 11 建隔离的 `FOREBRAIN_HOME`，配置两条智谱模型（主 agent 用一条，另一条作为评审模型，例如截图里的 `glm-5.3` 与 `glm-5.3-flash`；没有两条可用就用同一 provider 的两个模型名），密钥只用 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用。

1. `shift+tab` 进入计划模式，让主 agent 写一个小计划并调用 `exit_plan_mode`。
2. 在审批浮层选 "Ask another model to review this plan"，选与主 agent 不同的那个模型。
3. 等 roster 出现 `plan-reviewer  Plan review`，进入它的视图（roster 回车或计划 001 的 `click`）。`$D screen` 两次（隔 10 秒）。
4. 断言并截图：视图里有工具卡片（`Read …`、`Ran …` 等）；每条 assistant 消息只有这一步的文字，没有任何一条包含上一条的全文；footer 左侧是选中的模型和它配置里的推理强度。让 reviewer 在任务里调用 `session_todo`（评审提示词里要求它先列清单），reviewer 视图运行时有 `Working (…) · ☑N/M · <进行中的任务>`，结束时最后一行是 `─ Worked for … · ☑N/M · HH:MM ───`；网页同一视图两行都有勾选图标和同样的数字与任务名；主对话的 Working 行同样是图标而不是 `Tasks`；网页打开同一个 reviewer 视图，运行时 `正在工作（…）` 在走，结束后有 `已工作 … · HH:MM` 行。评审结束后回到主视图：没有 `Plan review · …` 那张印着评审全文的卡片，审批浮层里有这次评审。再选"继续规划"并输入一句话：主视图被拒的 `exit_plan_mode` 卡片只显示这句话；`/resume` 后同样只显示这句话；查库这条 action 的 `error` 就是这句话，而规划模型收到的工具结果里仍有评审（`fb_messages` 那一行的 `content` 含 `<review model=`）。（footer 右侧的 `N%/窗口` 由 006/007 提供，在计划 009 里验收。）
5. 评审结束后 `/resume` 回到这个会话，再进 reviewer 视图截图：工具卡片仍在（证明事件落库了）。
6. 查库：`SELECT count(*) FROM fb_session_events WHERE run_id='<reviewer 子运行 id>' AND event_type LIKE 'tool_call%'` > 0；`subagent_spawned` 载荷里 `model` 是选中的模型。
7. 查库：这次评审有一条 `plan_review_started` 和一条 `plan_reviewed`，`review_id` 相同；reviewer 的 `subagent_spawned` 载荷 `parent_tool_call_id` 等于这个 `review_id`。再选一个未配置的模型名（直接改配置让它失效）请求评审，库里有 `plan_review_started` 和 `Outcome:"failed"` 的 `plan_reviewed`，没有 spawned。若计划 013 已完成：主视图依次是 `Starting 1 plan-reviewer task…`、`Running …`、`Ran 1 plan-reviewer task` 卡片，任务行 `Plan review` 加计时；失败的那次是 `Failed to start 1 plan-reviewer task` 加错误原文。

**验证**：截图和查询结果贴进报告，逐条对应 owner 的第 1–4 点。

## 测试计划

第 1 步的 8 条 Go 测试（第 7 条含三个测试） + 第 3 步的 1 条前端测试。Go 测试放在与被测生产文件同名的测试文件里（架构测试要求）；结构参照 `pkg/tui/reducer_test.go:455`、`pkg/tui/run_test.go:180-225`。

## 完成标准

- [ ] 第 1–6 步的验证全部通过
- [ ] `grep -rn "SubagentModelSummary\|subagentModelsByType\|SetSubagentModels" pkg` → 无输出
- [ ] 第 6 步的截图与查询结果已附在报告里
- [ ] README 状态行已更新；若计划 002 未完成，在 002 的状态行旁注明"§5 的模型字段与 `AgentModel` 已由 015 实现"

## STOP 条件

- 第 1 步第 1、2 条没有失败。
- `prep.ctx` 不由 `baseCtx` 派生，覆盖放早了也进不了 reviewer 的调用。
- 封存规则改动让超过 3 个既有测试失败（说明有依赖旧行为的地方，需要回来看）。
- 计划 002 已完成且它的 `AgentModel` 签名与本计划不同：不要改它的签名，回来商量 effort 放在哪里。
- 主会话命中率下降。

## 维护说明

- 任何"不在回合里、却要跑 agent"的入口（今天只有 plan review）都必须给运行上下文带上界面的工具步骤钩子；计划 016 把它变成共享入口的显式参数，那之后新入口无法漏掉它。
- "模型是执行的属性"：以后任何界面要显示某个 subagent 的模型，读它 spawned 事件里的模型（或 `run.AgentModel`），不要再按类型推。
- 流式文字的分段规则（思考与文字互相封存）在 TUI 的 `reduceMessage` 和网页的 `appendStreamedText`/`closeStreamedBlocks` 两处，改一处必须改另一处。
