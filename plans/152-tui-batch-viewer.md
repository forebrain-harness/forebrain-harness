# Plan 152：TUI 在后台跑批项目里是只读查看器——打开最近一次执行、只接受白名单命令（含 `/continue` 续跑）、不能切模式与改预设、底栏标「batch · read-only」

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`148`、`149`、`150` 为 DONE（本计划用到 `ChatSession.SessionProjectType`（148）、`turn.AdmitBatchTurn`/`BatchContinuePrompt`（149）、`run.PresetBatchSandbox`（150））。
>
> **Drift check（先跑）**：`git diff --stat d9d0b4d -- pkg/tui/run.go pkg/tui/chat_surface.go pkg/tui/chat_session.go pkg/tui/reducer.go pkg/tui/render.go pkg/tui/chat_slash.go`
>
> **基线**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`（900 s）。

## Status

- **Priority**: P2（跑批的主入口是业务系统与网页；TUI 是查看）
- **Effort**: M
- **Risk**: MED（TUI 启动与提交路径）
- **Depends on**: `148`、`149`、`150`
- **Category**: direction
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区

## owner 裁定（2026-10-26）

1. 跑批"用户只能在 tui 和 web 端查看，不能发起对话，不能插话"。
2. TUI 形态（选项题，选推荐）：**打开最近一条任务的只读回放；输入框只接受白名单斜杠命令（`/resume` 选任务、`/status`、`/help`、`/exit`），打普通文字提示「后台跑批项目只能查看」；底栏标「跑批 · 只读」**。
3. 续跑（选项题）：web/TUI/业务系统都可；TUI 用白名单命令 **`/continue`**，只对"已停止"的执行开放。

## 设计（评审方定）

- TUI 界面是英文（现有全部 TUI 文案都是英文，如 `plan mode (shift+tab to exit)`），所以：底栏段 `batch · read-only`；普通文字被拒时的提示
  `Batch projects are read-only here. /resume opens a task execution, /continue resumes a stopped one.`。
- 白名单：`/resume`、`/status`、`/help`、`/exit`、`/quit`（若 `/quit` 是 `/exit` 的别名才放）、`/continue`。其余内置命令与技能命令一律给同一句提示。
- 启动：跑批项目且没有 `forebrain resume <id>` 指定会话时，自动打开**本项目最近更新的那条任务的最新执行**（用 149 的 `ListTasksPage` 取第一行的 `Latest.SessionID`），走与 `/resume` 选中会话相同的切换路径（回放历史）；一条任务都没有则停在空屏并打印一条系统提示 `No tasks yet in this batch project.`。
- `/continue`：当前会话是**已停止**的执行 → 用 `turn.AdmitBatchTurn(ctx, store, sid, "", false)`（149 后为**只读预检**，不会改输入或状态）得到输入，按普通回合在本进程跑（回合期间与普通回合一样实时显示；跑的仍是无人值守 + 沙箱）；否则打印 Admission 的拒绝说明（按 code 给英文句子：running → `This execution is still running.`；finished → `This execution has finished; start a new execution of the task from the web or the API.`；no task → `This conversation is not a task execution.`）。
- `/resume` 的列表：沿用现有会话列表；行标题已由 149 写成 `<任务标题>`（第 1 次）或 `<任务标题> #<n>`。本计划给跑批会话行加状态后缀（` · running` / ` · stopped` / ` · completed` / ` · failed`）。
- 模式切换热键在跑批会话里不切换，打印 `The mode is fixed in batch projects.`；`/permissions`（不在白名单）自然被拒。

## Current state

- 启动后登记项目：`pkg/tui/run.go` 约 `:4100-4112`：`if strings.TrimSpace(opts.InitialSessionID) == "" { if a, ok := opts.Session.(launchProjectAdopter); ok { a.AdoptLaunchProject(ctx) } }`，之后 `askProjectServers(...)`、`startConversationRuntime(ctx, opts.Session, renderer, sessionID)`。
  `launchProjectAdopter` 接口在约 `:4082`。
- 提交入口：`pkg/tui/chat_surface.go` `(*ChatSession).DispatchSurfaceTurn(ctx, submission turn.TurnSubmission)`（约 `:24-210`）：`res, ok := s.slashCommandResult(ctx, sessionID, ch, userText)` → 斜杠结果处理（`res.SelectSession` → `s.notifyUI(SessionSelectionRequestedMsg{})` 等）→ 非斜杠走 `turn.SkillReferences` → 附件 → `s.dispatchUserTurnContent(...)`。
- `slashCommandResult(ctx, sessionID, channel, line)`（`chat_session.go` 约 `:1831`）解析斜杠命令。
- 模式热键：`run.go` 约 `:4970` `case hotkeyTogglePlanMode:` → `statepkg.Switch(...)` → `syncPlanModeIndicator`。
- 底栏：`pkg/tui/reducer.go` `composerFooterSidesLocked()`（约 `:3657-3667`）：`plan := ""; if r.planMode { plan = planModeFooterLabel }; return joinFooterSegments(plan, r.footer.Model, r.footer.ReasoningEffort), …`；`planModeFooterLabel` 在 `render.go` 约 `:3416`。
  渲染器字段用 setter 设置（范例 `renderer.SetPlanMode(bool)`）。
- 会话列表：`ListSessionsRecent` / `ListSessionsRecentPaged`（`chat_surface.go` 约 `:264-300`）供 `/resume` 选择器。
- 148 提供 `(*ChatSession).SessionProjectType(sessionID string) string`；`ChatSession.ProjectID` 是启动目录的项目；项目行有 `ProjectType`（145）。

## Scope

**In scope**：`pkg/tui/run.go`、`pkg/tui/chat_surface.go`、`pkg/tui/chat_session.go`、`pkg/tui/reducer.go`、`pkg/tui/render.go`（只加一个常量）、对应 `_test.go`。
**Out of scope**：引擎准入规则本身（149）；网页（153）；办公项目的 TUI 行为（148 已做）。

## Steps

### Step 1：ChatSession 的跑批判定与最新执行

`chat_session.go` 加：
```go
// BatchProject reports whether the launch project is a batch project.
func (s *ChatSession) BatchProject(ctx context.Context) bool
// LatestBatchExecution is the session of the most recently updated task's
// latest execution in the launch project, "" when there is none.
func (s *ChatSession) LatestBatchExecution(ctx context.Context) string
// batchSession reports whether a conversation is a batch execution.
func (s *ChatSession) batchSession(sessionID string) bool { return s.SessionProjectType(sessionID) == state.ProjectTypeBatch }
```
（`BatchProject`：`s.projectStoreFor().Get(ctx, s.ProjectID)` 的 `ProjectType`；`LatestBatchExecution`：`ListTasksPage(ctx, s.ProjectID, state.ListTasksOptions{}, 1, 0)` 第一行的 `Latest.SessionID`。）

### Step 2：启动打开最新执行

`run.go` 在 `AdoptLaunchProject` 成功之后（同一个 `if` 块内）：若 `opts.Session` 实现了 `BatchProject`/`LatestBatchExecution`（加一个小接口，照 `launchProjectAdopter` 的写法）且是跑批项目：取最新执行；非空则用 `/resume` 选中会话后走的**同一个切换函数**把屏幕切过去（先 `grep -n "SessionSelectionRequestedMsg\|func .*switchToSession\|SessionSwitched" pkg/tui/run.go` 找到选择器确认后调用的函数）；为空则打印系统提示。
同时把一个"跑批只读"标志交给渲染器：`renderer.SetBatchReadOnly(true)`（Step 4）。

### Step 3：提交入口的白名单与 `/continue`

`DispatchSurfaceTurn` 开头（取到 `sessionID`、`userText` 之后、`slashCommandResult` 之前）：
```go
if s.batchSession(sessionID) || (sessionID == "" && s.BatchProject(ctx)) {
	return s.dispatchBatchViewerLine(ctx, sessionID, userText, submission)
}
```
`dispatchBatchViewerLine`：
- 文本 `/continue`（去空白后完全相等）→ 只读预检（Design）→ 通过则把 `adm.Input` 当作本回合 `userText`，`rawInput` 记为 `/continue`，并在最终 `turn.TurnRequest` 设置 `BatchInput: &turn.BatchTurnInput{Text: "", HasAttachments: false}`，让 149 的 Submit 在锁内原子启动；继续走与普通消息相同的 `dispatchUserTurnContent(...)`（复用该函数，不复制逻辑；附件为空）。若 dispatch 中层不暴露该字段，给它加 BatchInput 透传参数/字段，不能靠预检提前写状态。拒绝则打印对应英文句子。
- 文本首个 token 是白名单命令（除 `/continue`）→ 交还原流程（调用 `slashCommandResult` 及其后的处理——把这段原逻辑抽成私有函数供两处调用，**不要复制**）。
- 其它（普通文字、其它命令、附件）→ 打印只读提示，`return nil`。

### Step 4：底栏与热键

1. `render.go` 加 `const batchReadOnlyFooterLabel = "batch · read-only"`；渲染器加字段 `batchReadOnly bool` 与 `SetBatchReadOnly(bool)`（加锁写法照 `SetPlanMode`）。
2. `composerFooterSidesLocked`：`r.batchReadOnly` 时左侧第一段用 `batchReadOnlyFooterLabel`（并不再显示 plan 段）。
3. `hotkeyTogglePlanMode`：`state.session` 能判跑批且当前会话是跑批 → 打印 `The mode is fixed in batch projects.`，`return true`，不调 `Switch`。
4. 会话切换后（resume 进入另一会话）按新会话类型刷新 `SetBatchReadOnly`（在 `syncPlanModeIndicator` 被调用的同一处加一行）。

### Step 5：`/resume` 列表的状态后缀

`ListSessionsRecent` / `ListSessionsRecentPaged` 返回前：对跑批项目，批量查这些会话的执行状态（给 `ProjectStore` 加 `ExecutionStatuses(ctx, sessionIDs []string) (map[string]string, error)`——一条 `IN (...)` 查询；`pkg/state/task_store.go` 在 149 新建，本步可改），把 ` · <status>` 追加到标题。

### Step 6：测试与真机核对

见 Test plan；真机按 `.claude/skills/run-forebrain/SKILL.md` 起 TUI（隔离 home）：先用网关或直接写库造一个跑批项目、两条任务（一条 completed、一条 stopped），在项目目录启动 TUI → 截屏核对：直接打开最新执行的回放、底栏 `batch · read-only`、输入普通文字出现只读提示、`/resume` 列表带状态后缀、对 stopped 执行 `/continue` 开始一个回合（fake provider 即可）、shift+tab 打印模式固定提示。

## Test plan

`pkg/tui` 的单测（照该包现有 `DispatchSurfaceTurn` / 斜杠测试的夹具写法）：
1. `TestBatchViewerRefusesPlainText`：跑批会话里普通文字 → 不产生回合、输出只读提示。
2. `TestBatchViewerAllowsWhitelistedCommands`：`/status` 正常处理；`/plan`、`/model` 被拒。
3. `TestBatchContinueRunsAStoppedExecution`：执行状态 stopped → 回合输入等于 `turn.BatchContinuePrompt` 且 BatchInput 为原始空文本，Submit 才写 running；预检本身不写输入/状态；completed → 打印 finished 句子且无回合。
4. `TestBatchFooterLabel`：`SetBatchReadOnly(true)` 后左侧以 `batch · read-only` 开头。
5. `TestBatchHotkeyDoesNotSwitchMode`。
6. `TestResumeListShowsExecutionStatus`。
7. `TestBatchLaunchOpensLatestExecution`（若启动流程有可测入口；没有则由 Step 6 真机覆盖并在报告说明）。

## Done criteria

- [ ] `go build ./...` exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/state -count=1` 无新增失败；新测试存在且通过
- [ ] 非跑批项目的 TUI 行为不变（`pkg/tui` 既有测试全绿）
- [ ] Step 6 截屏核对 PASS（或写明 NOT RUN 的原因）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

1. 前置不满足或"现状"对不上。
2. 找不到 `/resume` 选中后切换会话的那个函数，或它需要 UI 事件循环里才能调用、启动阶段调不到。
3. 把斜杠处理抽成私有函数会改动 `DispatchSurfaceTurn` 的既有行为（既有测试变红）。

## Maintenance notes

- TUI 不做跨进程实时推送（owner 2026-10-10 规则）：网关里正在跑的任务，TUI 看到的是打开那一刻库里的内容，再 `/resume` 刷新。
- `/continue` 在 TUI 进程里跑回合：网关与 TUI 同时续跑同一执行时，引擎的会话忙检查会拒绝后到的那一个（既有规则）。
