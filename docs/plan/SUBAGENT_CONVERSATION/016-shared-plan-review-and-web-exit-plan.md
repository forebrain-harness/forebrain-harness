# 计划 016：plan review 下沉到共享层，网页的"退出计划模式"审批与 TUI 对齐

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"（尤其第 5、6、8、10 条）。
>
> **前置检查**：README 里计划 015 必须是 `DONE`（本计划把它给 reviewer 运行上下文加的钩子变成共享入口的参数）；013、014 必须是 `DONE`（评审在两个界面上以 plan-reviewer 卡片出现：`Starting`/`Running`/`Ran`/`Failed to start`）；决策 D14 不是"待定"（网页审批卡片的预览已经 owner 确认）。否则 STOP。
>
> **漂移检查（先运行）**：
> `git diff --stat bda9505 -- pkg/turn/approval.go pkg/turn/model.go pkg/event/run_events.go pkg/process pkg/tui/chat_surface.go pkg/tui/chat_session.go pkg/tui/overlays.go pkg/gateway/api_extra.go pkg/gateway/approval.go pkg/gateway/serve_run.go frontend/src/components/chat/PendingActionsPanel.vue frontend/src/lib/api.ts frontend/src/locales/index.ts`
> 015 会改 `chat_surface.go`；按函数名和注释原文核对"现状"，对不上就 STOP。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED-HIGH（审批流程的共享层、TUI 现有行为必须一字不变、网页新交互）
- **依赖**：013、014、015
- **类别**：bug（网页与 TUI 不对齐、共享语义只在 TUI 里实现）
- **基线**：提交 `bda9505`，2026-10-05

## 来由

owner 报告 plan-reviewer 的四个缺陷时（见计划 015 的"owner 的要求"），为了确认网页是否有同样的问题，核对了网页上的同一条路径，发现网页根本没有这条路径。按项目的长期规矩（顺带发现的既有缺陷必须纳入本期；网页与 TUI 的功能和数据必须一一对应；两个界面都需要的语义只实现一份，放在引擎里），作为本期计划的一份。

## 发现（含证据）

| # | 现象 | 根因 |
| --- | --- | --- |
| P1 | 网页上 `exit_plan_mode` 的待审批行，标题是原始工具名 `exit_plan_mode`，说明行在没有理由时显示 action id | `frontend/src/components/chat/PendingActionsPanel.vue` 的 `approvalKindLabel` 只认 `enter_plan_mode`；`approvalHint` 最后 `return a.id` |
| P2 | 网页上看不到要批准的计划 | 计划正文在计划文件里（`state.PlanPathForProject(stateRoot, projectKey)`，`pkg/state/plan.go:41`），action 载荷只有 `{action, approval_reason, force_tool_approval, session_id}`（本机状态库核实）；TUI 由 `buildSurfaceToolApprovalRequest`（`pkg/tui/chat_surface.go:412`）填 `PlanFilePath` 后自己读文件，网页没有任何途径拿到 |
| P3 | 网页没有"批准并清空上下文" | 网关的批准接口已经支持 `clear_context`（`pkg/gateway/api_extra.go:2123`，`handleActionsApprove`），`frontend/src/lib/api.ts:761` 也有 `clearContext` 字段，但没有任何界面发送它 |
| P4 | 网页没有"请另一个模型评审这份计划" | 评审的整条流程只在 TUI 里：可选模型 `planReviewOptions`（`chat_surface.go:831`）、启动 `runPlanReview`（`:969`）、执行 `planSubagentReviewer`（`:1158`）、结果存在 TUI 进程内存 `ChatSession.planReviews`（`pkg/tui/chat_session.go:105-110`）、拒绝时把评审拼进反馈 `planReviewDenyReason`；网关没有对应接口，`turn.ToolApprovalDecision.RequestPlanReview`（`pkg/turn/model.go:566`）在网关里从未被设置 |
| P5 | 评审结果只活在 TUI 进程内存里：TUI 重启后，挂起的审批还在，评审却没了；网关多副本部署时这种进程内状态本来就不成立 | 同 P4；仓库自己的设计文档写的是"Plan review 是 approval 的子流程，不是 surface state：共享层维护 review 结果，TUI 只负责选择 review model 与展示"（`docs/plan/TUI_FIRST_RUNTIME_REFACTOR.md:1402`），这一步没有做 |
| P6 | 评审失败、被停止、超时的提示只有 TUI 的英文句子 | `notifyPlanReviewFailure`（`chat_surface.go:1058`）在 TUI 里拼句子；计划 013 改为由 plan-reviewer 卡片说明，网页由 014 的同一张卡片说明 |

## 设计

### 1. 引擎：评审是审批的子流程

- **结果落在会话事件里**：计划 015 已经引入 `plan_review_started` / `plan_reviewed`（`pkg/event/run_events.go`，载荷 `PlanReviewStartedPayload`、`PlanReviewedPayload`，含 `ActionID`、`ReviewID`、模型、`Text`、`DurationMs`、`Outcome`、`Error`），本计划不再定义新事件，而是把它们定为评审结果的**唯一来源**：TUI 的内存存储删除，两个界面都从事件读。
- **共享函数**（`pkg/turn/approval.go`，不新建文件）：
  - `PlanReviewModelOptions(cfg *appcfg.Root, agentName, currentProvider, currentModel string) []PlanReviewModelOption`：把 `ChatSession.planReviewOptions` 的函数体原样搬下来（去重、标记当前模型）。
  - `PlanReviewsForAction(ctx context.Context, runs *state.RunStore, sessionID, actionID string) ([]PlanReviewResult, error)`：用 `runs.ListSessionEventsOfType(ctx, sessionID, event.RunEventPlanReviewed, 0)`（`pkg/state/session_events.go:223`）读出 `ActionID` 匹配、`Outcome == "done"` 的评审，按时间顺序。
  - `RunPlanReview(ctx context.Context, in PlanReviewRun) error`：把 `ChatSession.runPlanReview` 与 `planSubagentReviewer.Review` 的编排搬下来（含计划 015 加的 `ReviewID`、`plan_review_started`/`plan_reviewed` 的发布和 `tool.WithToolUseID(runCtx, reviewID)`）——生成 `ReviewID` 并发布 `plan_review_started`、读计划（`state.GetPlanForProject`，与 `pkg/turn/executor.go:416` 同样的 stateRoot/projectKey 来源）、取任务上下文（`planReviewTaskContext` 的逻辑）、调用 `in.Reviewer.Review`、把结果或失败以 `plan_reviewed` 发布。`PlanReviewRun` 携带 `ActionID`、`Model`、`SessionID`、`RunID`、`StateRoot`、`ProjectKey`、`Reviewer turn.Reviewer`、`Publish func(ctx, sessionID, runID, eventType string, payload any) error`。
  - 拒绝时给模型的理由：计划 015 已经把"拼评审"从决定时挪到恢复时、`action.Error` 只存用户的话。本计划把它做成共享的：`turn.BuildApprovalResume`（或紧挨它的新函数）对 `exit_plan_mode` 的拒绝返回 `Reason = ComposeDenyFeedback(PlanReviewsForAction(...), action.Error)`、`Feedback = action.Error`，TUI（`chat_turn.go` 的恢复路径）与网关（`api_extra.go:2417`）都用它，各自不再拼。
  - 挂起审批的请求：`PendingApprovalGate`（`pkg/turn/approval.go:1075`）加两个可选的注入点 `PlanScope func(ctx context.Context, sessionID string) (stateRoot, projectKey string)`、`ReviewModels func() []PlanReviewModelOption`，并让 `Pending` 对 `exit_plan_mode` 填 `PlanFilePath`、`PlanReviewModels`、`PlanReviews`（用 `PlanReviewsForAction`，需要 `Runs` 能取会话事件：执行者核对 `PendingApprovalRuns` 接口，必要时给它加一个方法而不是另传一个依赖）。TUI 的 `buildSurfaceToolApprovalRequest` 里对应的三段随之删除。
- **共享的 reviewer 实现**（`pkg/process`，它同时 import `run` 和 `turn`；该包已是 20 个生产文件，写进已有文件，执行者选与 subagent 执行器最近的那个）：`process.PlanReviewer{Runner *run.Runner; Model turn.Model; StepHook tool.StepHook; Approve run.PlanReviewApprovalFunc}` 实现 `turn.Reviewer`，函数体就是今天 `planSubagentReviewer.Review` 的主体（含计划 015 加的 `tool.WithStepHook`，但钩子改为字段，由界面传入）。

### 2. TUI：改用共享层，行为一字不变

- `runPlanReview` → `turn.RunPlanReview`；`planSubagentReviewer` 删除，改为构造 `process.PlanReviewer{…, StepHook: s.runAuditStepHook(sessionID, s.Env.Tools().StepHook()), Approve: <今天的 resolvePlanReviewApproval 闭包>}`；`planReviewerFactory`（测试接缝）保留，类型不变。
- `planReviews` 字段、`appendPlanReview`、`planReviewResults`、`planReviewNotes`、`forgetPlanReviews` 删除，读取改为 `turn.PlanReviewsForAction`。
- 失败、停止、超时在两个界面上都只由卡片说（计划 013/014：`Failed to start 1 plan-reviewer task` 加原因，或任务行的 `✗`/`×`）；`notifyPlanReviewFailure` 在 013 里已删除，这里确认没有残留。
- `pkg/tui/chat_session_test.go` 里 `TestPlanReviewLeavesTheApprovalPendingAndReachesThePlanner`（`:12482`）、`TestPlanReviewReachesThePlannerWhenTheUserTypesNothing`（`:12535`）、`TestPlanReviewFailureKeepsTheApprovalAndReportsIt`（`:12556`）、`TestPlanReviewRefusedForAnApprovalThatIsNotAnExitPlan`（`:12601`）、`TestApprovalConfirmationTextPlanReviewRequest`（`:10693`）、`TestPlanReviewPickerScrollKeysReleaseTheOptionAnchor`（`:4148`）**不改断言**也必须通过（只允许改它们的搭建，例如评审结果改由事件提供；`TestPlanReviewFailureKeepsTheApprovalAndReportsIt` 的断言以计划 013 改过之后的为准——失败由卡片说明）。

### 3. 网关：传输

- `GET /chat/sessions/:id/approval-request`：返回 `PendingApprovalGate.Pending` 的结果（类型化的审批请求，含 `exit_plan_mode` 的 `planText`——网关按 `PlanFilePath` 读文件放进响应、`planReviewModels`、`planReviews`）。没有挂起的审批返回 204。`serve_run.go:79` 构造的 gate 注入 `PlanScope`、`ReviewModels`。
- `POST /actions/:id/plan-review`，body `{provider, model}`：校验 action 是挂起的 `exit_plan_mode`、模型在 `PlanReviewModelOptions` 里，然后在后台跑 `turn.RunPlanReview`，`Reviewer` 是 `process.PlanReviewer`，`StepHook` 与 `Approve` 来自 `withDetachedGatewayApprovalHooks(ctx, sessionID, runID)`（`pkg/gateway/approval.go:25`）已经装好的那一套（执行者从它返回的上下文里取 `tool.StepHookFromContext`，以及照 `promptGatewaySubagentApproval`（`:243`）写一个 `run.PlanReviewApprovalFunc`）。立即返回 202。
- 停止评审：评审是一个 subagent，网页用已有的 `POST /subagents/:id/cancel` 停它（与停任何 subagent 相同）；`RunPlanReview` 把 `context.Canceled` 记为 `Outcome:"stopped"`。
- 批准、批准并清空上下文、拒绝（带反馈）：沿用 `handleActionsApprove`；`reason` 只是用户的话，给模型的理由在恢复时由共享层拼（与 TUI 相同）。

### 4. 网页：退出计划模式的审批卡片

（样式以 `subagent-cards-preview.html` 的"退出计划模式审批（网页）"一节为准，D14。）

- `PendingActionsPanel.vue` 对 `kind === 'exit_plan_mode'` 画专门的卡片（其它 kind 不变），数据来自新接口：
  - 标题：`退出计划模式` / `Exit plan mode`（新文案键，`approvalKindLabel` 一并认它）。
  - 计划正文：用对话里渲染 Markdown 的同一个组件完整显示，过长时卡片内滚动，不截断。
  - 已有的评审：每条一块，标题 `<模型> · <耗时>`，正文 Markdown；评审没有成功时，审批卡片里不再重复说明（时间线上的 plan-reviewer 卡片已经说了：`未能启动…` 加原因，或任务行的失败/取消）。
  - 四个选择，与 TUI 的 `buildExitPlanChoices`（`pkg/tui/overlays.go:267`）一一对应：`批准并清空上下文（已用 N%）`（N 取网页已有的上下文占用数，没有就不带括号）、`批准`、`继续规划`（拒绝，下面是反馈输入框）、`请其他模型评审`（展开模型列表，当前模型标"当前"，选中后调 `POST /actions/:id/plan-review`）。
  - 评审进行中：选择区变为"评审进行中"加"停止"按钮（停止即取消那个 subagent）；时间线里同时出现计划 013/014 的 plan-reviewer 卡片（`正在启动 1 个 plan-reviewer 任务…` → `正在运行…`），可以点进去看它工作。评审完成（收到 `plan_reviewed`）后重新取一次审批请求，卡片显示新评审并恢复四个选择。
- 文案中英文同键；不显示原始 JSON；纯色。

## 缓存影响

- 评审是独立的 subagent 运行，有自己的 worker 会话和模型；不进入主会话的前缀。
- 评审到达规划模型的方式不变：仍是用户拒绝时由 `ComposeDenyFeedback` 拼进拒绝理由，追加在消息尾部。
- `plan_reviewed` 是界面事件，不进入任何模型请求。
- 第 7 步记录主会话命中率并与基线比较，不得下降。

## 范围

**只改这些文件：** `pkg/event/run_events.go`、`pkg/turn/approval.go`、`pkg/turn/model.go`（如需）、`pkg/process/<执行者选定的已有文件>.go`、`pkg/tui/chat_surface.go`、`pkg/tui/chat_session.go`、`pkg/gateway/api_extra.go`、`pkg/gateway/approval.go`、`pkg/gateway/serve_run.go`、`frontend/src/components/chat/PendingActionsPanel.vue`、`frontend/src/lib/api.ts`、`frontend/src/locales/index.ts`，以及它们对应的测试；网页 e2e 新建 `frontend/e2e/exit-plan-approval.spec.ts`。

**不要动：** TUI 审批浮层的外观与按键（`pkg/tui/overlays.go` 只读不改）；计划模式的进入/退出语义本身；`pkg/gateway/dist`。

## 步骤

### 第 1 步：先写失败的测试

- `pkg/turn` 加 `TestPlanReviewsLiveOnTheConversation`：发布两条 `plan_reviewed`（一条 done、一条 failed），`PlanReviewsForAction` 只返回 done 那条；换一个 `RunStore` 实例（模拟另一个进程）读到同样的结果。
- `pkg/turn` 加 `TestPendingExitPlanRequestCarriesPlanReviewsAndModels`：`PendingApprovalGate` 注入 `PlanScope`、`ReviewModels` 后，`Pending` 对 `exit_plan_mode` 返回 `PlanFilePath`、模型列表、已有评审。
- `pkg/gateway/api_extra_test.go` 加：`GET /chat/sessions/:id/approval-request` 返回 `planText`；`POST /actions/:id/plan-review` 对非 `exit_plan_mode` 的 action 返回 400、对未配置的模型返回 400、成功时 202。
- `frontend/src/components/chat/PendingActionsPanel.test.ts`（已存在，照它的写法）加：`exit_plan_mode` 卡片显示计划正文、四个选择、"批准并清空上下文"发出 `clearContext: true`、选模型后调用评审接口。

**验证**：上述测试全部失败（编译失败也算）。

### 第 2 步：引擎（设计第 1 节）

**验证**：第 1 步前两条转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/event ./pkg/process -count=1` → `ok`。

### 第 3 步：TUI 改用共享层（设计第 2 节）

**验证**：设计第 2 节列出的六个既有测试不改断言全部通过；`grep -n "planReviews\b\|appendPlanReview\|planSubagentReviewer" pkg/tui/*.go` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`。

### 第 4 步：网关（设计第 3 节）

**验证**：第 1 步网关测试转绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → `ok`。

### 第 5 步：网页（设计第 4 节）

**验证**：`cd frontend && corepack pnpm test` 全部通过（含第 1 步的前端用例）；`corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` 退出码 0；中英文文案键一一对应。

### 第 6 步：全量回归

**验证**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`；架构测试与包依赖图通过（不新增包间 import：`pkg/turn` 不 import `pkg/run`，reviewer 实现放在 `pkg/process` 正是为此）；死代码不增加。

### 第 7 步：真机

1. TUI（智谱，隔离环境，两条模型）：计划模式 → `exit_plan_mode` → 请另一个模型评审 → 评审结束后浮层显示评审 → 关掉 TUI 再 `/resume`，浮层里评审仍在（P5）→ 选"继续规划"，规划模型收到的拒绝理由里有评审（查 `fb_messages` 里那条 tool 结果）。
2. 网页（`FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh`，新 spec）：同样走一遍，截图：卡片标题、计划正文、四个选择、评审进行中、时间线里的 reviewer 卡片、点进 reviewer 视图看到工具卡片、评审结果、"批准并清空上下文"后上下文被清空。
3. 跨界面：在 TUI 发起评审，网页刷新后看到同一条评审（同一个 `FOREBRAIN_HOME`）。

**验证**：截图与查询结果贴进报告，逐条对应 P1–P6。

## 测试计划

见第 1 步；另外第 3 步的六个既有 TUI 测试是行为不变的守门测试。

## 完成标准

- [ ] 第 1–7 步的验证全部通过
- [ ] `grep -rn "planReviews\b\|appendPlanReview\|planSubagentReviewer" pkg/tui` → 无输出
- [ ] 网页 `exit_plan_mode` 卡片不再显示 `exit_plan_mode` 原文或 action id
- [ ] README 状态行已更新

## STOP 条件

- 前置检查不满足（尤其 D14 的预览未经 owner 确认）。
- 第 3 步需要改六个既有 TUI 测试的断言才能通过。
- 共享层需要 `pkg/turn` import `pkg/run`（违反层规则）而 `pkg/process` 放不下。
- 网关多副本下"评审进行中"的状态无法从事件推出（例如需要进程内状态才能知道评审是否还在跑）：回来商量，不要加进程内状态。
- 主会话命中率下降。

## 维护说明

- 评审结果的唯一来源是 `plan_reviewed` 事件；任何界面都不要再在内存里存一份。
- 新增审批选择时，TUI 的 `buildExitPlanChoices` 与网页卡片要一起改；网页的四个选择与它一一对应。
- 评审 subagent 的工具步骤钩子与审批函数是 `process.PlanReviewer` 的字段，由各界面传入；新增界面必须给它们，否则会重现计划 015 的第 2 点。
