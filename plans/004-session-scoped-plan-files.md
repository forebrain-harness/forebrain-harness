# Plan 004: 计划文件按会话隔离——多个 TUI / 会话同时在 plan 模式时，exit_plan_mode 审批不再显示别人的计划

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 71c0449..HEAD -- pkg/state/plan.go pkg/state/plan_test.go pkg/tool/state.go pkg/tool/session_tools.go pkg/tool/shell_tool.go pkg/process/session_context.go pkg/run/config.go pkg/turn/approval.go pkg/turn/model.go pkg/turn/executor.go pkg/gateway/api_extra.go pkg/tui/chat_surface.go pkg/migrate/assets.go pkg/migrate/codex_assets.go`
> 输出为空 = 无漂移。若有文件变化，把下文「Current state」里的摘录逐段对照现行代码；对不上就按 STOP 处理。
>
> **注意**：写这份计划时，`plans/003-recall-in-flight-steer.md` 已在工作区落地完毕，但**还没有提交**（owner 手动提交）。
> 它改了 `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`、`pkg/run/orchestration_llm_test.go` 等，
> 这些文件里也有本计划要改的地方。所以**要么等 003 提交后再执行，要么在基于 HEAD 的独立 worktree 里执行**。
> 下文对这几个文件一律**按函数名 / 测试名定位**，不要依赖行号。

## Status

- **Priority**: P1（owner 报告的严重 bug：多进程/多会话同时规划时审批框串台，批准后模型会去实现别人的计划）
- **Effort**: M（生产代码约 120 行，分布在 12 个文件；测试改动约 40 处，大多是机械替换）
- **Risk**: MED（计划文件的磁盘位置变了：所有读写计划的地方必须一起切到按会话的目录，漏掉任何一处，
  该会话就会把计划写到 A 处、从 B 处读，结果显示 "No plan found"。Step 8 用编译错误 + grep 兜底，保证一处不漏）
- **Depends on**: none（与 003 只有文件层面的冲突，见上方注意）
- **Category**: bug
- **Planned at**: commit `71c0449`，2026-10-09

## Owner 裁决（2026-10-09，必须遵守，不要改动）

1. **隔离方案 = 每会话子目录**：计划文件放在 `<stateRoot>/plans/<projectKey>/<sessionID>/*.md`。
   不要做成「在会话状态里记一个绑定的文件名」。
2. **旧会话不迁移**：升级前写在扁平目录 `<stateRoot>/plans/<projectKey>/*.md` 下的计划原样留在原处，当作历史；
   之后任何会话都不再把它们当作「当前计划」。升级时恰好处在 plan 模式或实现阶段的旧会话，审批时会看到
   "No plan found"，也收不到实现提醒，需要模型重写一次计划——这是 owner 接受的代价。**不要写任何迁移或回退逻辑**，
   也不要回退到「扁平目录里最新的文件」。
3. 两个会话起同名计划文件的问题，随子目录方案自然消失，不再单独处理。
4. **`/migrate` 导入的计划进入它所属对话的会话目录**（并入本期，见 Step 7）：
   - 导入后的对话 ID 是确定的：Claude 是 `cli-<Claude 会话 ID>`，Codex 是 `cli-<thread ID>`。计划写进
     `<stateRoot>/plans/<projectKey>/cli-<id>/`，这个对话被 resume 之后，`/plan`、plan-mode 提醒、审批都能看到它；
   - **一个 Claude slug 对应多个会话时，复制到每一个会话的目录**；
   - 找不到对应对话的计划（slug 没有匹配任何会话），照旧写进不带项目的计划根目录 `<stateRoot>/plans/<slug>.md`，只当历史。
5. **删会话不碰计划目录**：`state.RemoveSessionStateFiles` 不删 `plans/.../<sessionID>/`，计划永远作为历史保留。
   不要为此改 `RemoveSessionStateFiles`，但要在它的注释里写明这是有意为之（见 Step 7）。

## Why this matters

「当前计划」现在的定义是 `<stateRoot>/plans/<projectKey>/` 目录里 **mtime 最新的 `.md`**
（`pkg/state/plan.go` 的 `PlanPathForProject` → `newestMarkdown`），解析时完全不带会话 ID。
同一个项目里启动的所有 TUI 进程，以及 gateway、/fork 出来的会话，用的都是这同一个扁平目录
（owner 本机这个目录已有 38 个计划文件）。所以哪个会话最后写了计划，它的文件就成了**所有会话**的「当前计划」：
A 会话调用 `exit_plan_mode` 时，审批框显示的是 B 刚写的计划。

后果远不止显示错，下面这些都是同一个函数在决定：

- 批准之后，`exit_plan_mode` 的结果告诉 A「Implement the approved plan in <B 的文件>」，A 会去实现 B 的计划；
- 「让另一个模型评审」评审的是 B 的计划；
- 每轮注入的 plan-mode 提醒告诉 A「current: <B 的文件>」，A 可能直接 edit_file 改坏 B 的计划；
- 实现阶段的提醒让 A 往 B 的计划里写 `## Implementation Status`；
- `/plan`、`/status`、gateway 的 `GET /sessions/:id/plan-md`、web 的审批卡、shell 的 `FOREBRAIN_PLAN_FILE` 全部指向 B 的文件；
- `/fork` 会把「最新文件」原样重写一遍，等于改动别的会话计划文件的 mtime。

修复后，每个会话（连同它派生的 subagent）只读写自己的 `plans/<projectKey>/<sessionID>/` 目录。会话内部的语义不变
（目录里最新的 `.md` 就是当前计划，旧计划作为历史保留），会话之间互相看不见，在 plan 模式下也写不进对方的目录。
`/migrate` 导入的计划也随之放进所属对话的目录；不这样做的话，修复后导入的计划就没有任何会话能看到了。

## 根因证据（advisor 已实测，执行者不必重做；Step 9 用同一流程验收修复）

在真实 TUI 里复现（`.claude/skills/run-forebrain/driver.sh`，用隔离的 FOREBRAIN_HOME 和假 provider，HEAD `71c0449` 构建）：

1. `driver.sh start toolcall`（假模型先调用 `enter_plan_mode`，再调用 `exit_plan_mode`，**本会话从头到尾没写过任何计划**）；
2. 在 `$WORK/home/workspace/plans/<projectKey>/other-process-plan.md` 放一份「别的进程写的计划」，内容为 `# FOREIGN PLAN written by another TUI process`；
3. 提交消息，批准 enter_plan_mode，等 exit_plan_mode 的审批框出现。

实际画面：

```
Tool approval
Tool: exit_plan_mode

Here is the plan:
────────────────────────────────
FOREIGN PLAN written by another TUI process

1. This belongs to someone else.
```

选择「Yes, proceed」后，state DB 里这次 exit_plan_mode 的工具结果是
`Implement the approved plan in …/plans/<projectKey>/other-process-plan.md.`。

## Current state

所有「从项目解析当前计划」的地方（生产代码，2026-10-09 在 `71c0449` 上核对过）：

| 文件 | 符号 | 用途 |
|---|---|---|
| `pkg/state/plan.go` | `PlanDirForProject` / `PlanPathForProject` / `GetPlanForProject` / `SetPlanForProject` / `newestMarkdown` | 计划存储本身 |
| `pkg/turn/approval.go` | `RunPlanReview`（`state.GetPlanForProject(...)` 那一行） | 「让另一个模型评审」读的计划 |
| `pkg/turn/approval.go` | `(*PendingApprovalGate).Pending`（`req.PlanFilePath = state.PlanPathForProject(...)`） | TUI 审批框和 web 审批卡用的 `PlanFilePath` |
| `pkg/turn/model.go` | `BuildStatusReport`（`state.GetPlanForProject(src.StateRoot, src.ProjectKey)`） | `/status` 的「有没有计划」 |
| `pkg/turn/executor.go` | `/plan` 处理（`p, err := state.GetPlanForProject(ctx.stateRoot(), ctx.projectKey())`） | `/plan` 显示计划 |
| `pkg/turn/executor.go` | `copySlashSessionState` | `/fork` 复制会话状态 |
| `pkg/run/config.go` | `(*planModeLLM).Execute`、`(*planModeLLM).executeImplementationPhase` | 每轮 plan-mode 提醒、实现阶段提醒、本轮可写的计划目录 |
| `pkg/tool/session_tools.go` | `newEnterPlanModeTool`、`newExitPlanModeTool` | 工具返回给模型的 `plan_file`、本轮可写目录 |
| `pkg/tool/shell_tool.go` | `planModeShellEnv` | shell 的 `FOREBRAIN_PLAN_FILE` / `FOREBRAIN_PLAN_DIR` |
| `pkg/process/session_context.go` | `AgentContextForProject` | 每个 surface 的 agent ctx 里的可写计划目录 |
| `pkg/gateway/api_extra.go` | `handleSessionPlanMarkdown` | web 的 `GET /sessions/:id/plan-md` |
| `pkg/migrate/assets.go` | `importPlans`、`planSlugProjects`、`PlanOutcome` | `/migrate` 从 Claude Code 导入计划 |
| `pkg/migrate/codex_assets.go` | `extractCodexPlans`、`writeExtractedPlan` | `/migrate` 从 Codex 抽取计划 |

只读不改、但要知道的：

- `pkg/tui/overlays.go` 的 `approvalPlanContent` 只读 `req.PlanFilePath`，文件不存在时显示 `No plan found.`。**不改。**
- `pkg/gateway/api_extra.go` 的审批卡接口（`approvalGate().Pending` 后 `os.ReadFile(req.PlanFilePath)`）同样只读 `PlanFilePath`。**不改。**
- `pkg/tui/chat_surface.go` 的 `approvalGate()` 和 `pkg/gateway/approval.go` 的 `approvalGateOn` 只提供
  `PlanScope`（stateRoot + projectKey），会话 ID 由闸门自己的 `sessionID` 参数带入。**只改一段注释。**
- `pkg/state/mode.go` 的 `RemoveSessionStateFiles`（唯一调用方是 cron 的保留期清理 `pkg/process/cron_service.go:299`）
  只删 `state/modes|fast|todos|intermediate` 下的会话文件。按 owner 裁决 5，**不删计划目录**；本计划只给它的注释补一句说明。
- `isSanctionedPlanWrite`（`pkg/tool/state.go`）只放行「允许目录**正下方**的文件」（`isWithinDir` 拒绝子目录和目录本身）。
  所以只要可写目录换成会话目录，别的会话的目录、扁平项目目录在 plan 模式下自然都写不进去，**不需要改这个函数**。

关键摘录（行号以 `71c0449` 为准）：

`pkg/state/plan.go:37-47`
```go
// PlanPathForProject returns the current plan file for a project scope: the
// newest .md file directly in PlanDirForProject, or a default
// <PlanDirForProject>/plan.md when none exists yet. The returned path may not
// exist on disk.
func PlanPathForProject(workspaceRoot, projectKey string) string {
	dir := PlanDirForProject(workspaceRoot, projectKey)
	if f := newestMarkdown(dir); f != "" {
		return f
	}
	return filepath.Join(dir, "plan.md")
}
```

`pkg/turn/approval.go:1664-1671`（`Pending` 内）
```go
	if strings.EqualFold(toolName, "enter_plan_mode") || strings.EqualFold(toolName, "exit_plan_mode") {
		if g.PlanScope != nil {
			if stateRoot, projectKey := g.PlanScope(ctx, sessionID); strings.TrimSpace(stateRoot) != "" {
				req.PlanFilePath = state.PlanPathForProject(stateRoot, projectKey)
			}
		}
	}
```

`pkg/turn/approval.go:1505`（`RunPlanReview` 内，`sessionID := strings.TrimSpace(in.SessionID)` 已在函数开头定义）
```go
	plan, err := state.GetPlanForProject(strings.TrimSpace(in.StateRoot), strings.TrimSpace(in.ProjectKey))
```

`pkg/turn/model.go:789-792`（`BuildStatusReport` 内，`sid` 已在函数开头定义）
```go
	// Work: plan existence and todo progress.
	if planText, err := state.GetPlanForProject(src.StateRoot, src.ProjectKey); err == nil {
		rep.Work.PlanSet = strings.TrimSpace(planText) != ""
	}
```

`pkg/turn/executor.go:413`（`/plan` 处理）
```go
		p, err := state.GetPlanForProject(ctx.stateRoot(), ctx.projectKey())
```

`pkg/turn/executor.go:962-970`（`copySlashSessionState`）
```go
func copySlashSessionState(ctx Context, sourceSessionID, targetSessionID string) error {
	stateRoot := ctx.stateRoot()
	if plan, err := state.GetPlanForProject(stateRoot, ctx.projectKey()); err != nil {
		return err
	} else if strings.TrimSpace(plan) != "" {
		if err := state.SetPlanForProject(stateRoot, ctx.projectKey(), plan); err != nil {
			return err
		}
	}
```

`pkg/run/config.go:559-582`（`(*planModeLLM).Execute`）
```go
func (w *planModeLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	...
	sid := llm.AgentSessionIDFromContext(ctx)
	...
	st, err := state.Get(w.stateRoot, sid)
	...
	projectKey := w.projectKey
	if ctxProjectKey := tool.ProjectKeyFromContext(ctx); ctxProjectKey != "" {
		projectKey = ctxProjectKey
	}
	if st.Mode != state.ModePlan {
		return w.executeImplementationPhase(ctx, msgs, tools, projectKey, st)
	}
	planDir := state.PlanDirForProject(w.stateRoot, projectKey)
	planFile := state.PlanPathForProject(w.stateRoot, projectKey)
	planExists := false
	if content, err := state.GetPlanForProject(w.stateRoot, projectKey); err == nil && strings.TrimSpace(content) != "" {
		planExists = true
	}
```

`pkg/run/config.go:671-693`（`executeImplementationPhase`）
```go
	planDir := state.PlanDirForProject(w.stateRoot, projectKey)
	...
	content, err := state.GetPlanForProject(w.stateRoot, projectKey)
	...
	planFile := state.PlanPathForProject(w.stateRoot, projectKey)
```

`pkg/tool/session_tools.go:472-482`（enter）与 `:554-559`（exit）
```go
			planDir := state.PlanDirForProject(stateRoot, projectKey)
			st.SetRuntimeSessionMode(ctx, sid, string(state.ModePlan), planDir)
			...
			existingContent, _ := state.GetPlanForProject(stateRoot, projectKey)
			...
				respPlanFile = state.PlanPathForProject(stateRoot, projectKey)
```
```go
			planDir := state.PlanDirForProject(stateRoot, projectKey)
			...
			st.SetRuntimeSessionMode(ctx, sid, string(restored), planDir)
			planFile := state.PlanPathForProject(stateRoot, projectKey)
```

`pkg/tool/shell_tool.go:43-45`
```go
	projectKey := strings.TrimSpace(rt.ProjectKey)
	planDir := state.PlanDirForProject(stateRoot, projectKey)
	planFile := state.PlanPathForProject(stateRoot, projectKey)
```

`pkg/process/session_context.go:35-54`（`AgentContextForProject`）
```go
	agCtx := llm.WithAgentSessionID(base, sid)
	agCtx = tool.WithProjectKey(agCtx, projectKey)
	...
	agCtx = tool.WithAllowedPlanPath(agCtx, state.PlanDirForProject(stateRoot, projectKey))
	return agCtx
```

`pkg/gateway/api_extra.go`，函数 `handleSessionPlanMarkdown`（`sid` 已在函数开头从 URL 取出）
```go
	p, err := state.GetPlanForProject(s.stateRoot(), s.projectKey())
```

`pkg/migrate/assets.go:993-998`（报告里每份计划一条记录）
```go
type PlanOutcome struct {
	Name       string
	ProjectKey string // "" when the plan landed in the unscoped plans root
	Status     string // installed | up-to-date | diverged | skipped
	Detail     string
}
```

`pkg/migrate/assets.go:1008-1065`（`importPlans`，Claude 导入；节选）
```go
	slugProject := planSlugProjects(data)
	...
	for _, plan := range data.Plans {
		...
		cwd := slugProject[plan.Name]
		projectKey := ""
		scope := "the unscoped plans root"
		if cwd != "" {
			projectKey = memory.ProjectKey(cwd)
			scope = "project " + projectKey
		}
		destDir := state.PlanDirForProject(workspace, projectKey)
		dest := filepath.Join(destDir, plan.Name+".md")
		body, err := os.ReadFile(plan.Path)
		... // 读失败 → skipped；dest 已存在且内容相同 → up-to-date；不同 → diverged（不覆盖）；
		... // DryRun → installed + "planned for "+scope；否则 MkdirAll + WriteFile(append(bytes.TrimRight(body, "\n"), '\n'))
		detail := "written to " + scope
		if projectKey == "" {
			detail += " (no conversation matched this plan's slug)"
		}
```

`pkg/migrate/assets.go:1068-1084`（slug → 第一个带这个 slug 且有 cwd 的会话的 cwd）
```go
// planSlugProjects maps a plan slug to the cwd of the conversation that
// carried it. When several conversations share a slug the first discovered
// wins; their cwd is the same project in every observed case.
func planSlugProjects(data *claudeData) map[string]string {
	out := map[string]string{}
	for _, project := range data.Projects {
		for _, session := range project.Sessions {
			if session.Slug == "" || session.Cwd == "" {
				continue
			}
			if _, exists := out[session.Slug]; !exists {
				out[session.Slug] = session.Cwd
			}
		}
	}
	return out
}
```

`claudeSession`（`pkg/migrate/claude.go:61`）有 `SessionID`（文件名里的 uuid）、`Cwd`、`Slug` 字段。
导入后的对话 ID 由 `pkg/migrate/sessions.go:215` 的 `claudeSessionTarget(sessionID) = "cli-" + sessionID` 给出，
Codex 的由 `pkg/migrate/sessions.go:235` 的 `codexSessionTarget(threadID) = "cli-" + threadID` 给出。

`pkg/migrate/codex_assets.go:389-435`（`extractCodexPlans`；`parsed` 的 key `target` 就是导入后的对话 ID）
```go
	for _, target := range targets {
		session := parsed[target]
		...
		threadID := strings.TrimPrefix(target, "cli-")
		...
		for i, block := range blocks {
			... // name 的编号、跨 thread 撞名时加 threadIDSuffix 的逻辑
			outcome := writeExtractedPlan(workspace, projectKey, name, body, opts.DryRun)
			out = append(out, outcome)
		}
	}
```

`pkg/migrate/codex_assets.go:481` 起（`writeExtractedPlan`，三态幂等写入）
```go
func writeExtractedPlan(workspace, projectKey, name, body string, dryRun bool) PlanOutcome {
	scope := "the unscoped plans root"
	if projectKey != "" {
		scope = "project " + projectKey
	}
	destDir := state.PlanDirForProject(workspace, projectKey)
	dest := filepath.Join(destDir, name+".md")
	... // 已存在且相同 → up-to-date；不同 → diverged；dryRun → installed "planned for "+scope；
	... // MkdirAll/WriteFile 失败 → skipped；成功 → installed "written to "+scope
}
```

`/migrate` 的报告逐条打印 `outcomeLine(plan.Name, plan.Status, plan.Detail)`，汇总行按 Status 计数
（`pkg/migrate/plan.go` 里 `Plans: %d installed · ...`）。不需要改渲染代码，复制到多个会话时每份副本各占一条记录。

### 「计划属于哪个会话」——会话 ID 的取法（重要）

计划属于**用户看得见的那个对话（conversation）**，不属于 subagent 的 worker 会话。继承了 plan 模式的 subagent
今天往父对话的计划目录里写（它的 plan-mode 提醒让它「只写计划目录」），改完以后也必须写进**父对话**的会话目录，
否则父对话的 exit_plan_mode 看不到它写的计划。

仓库里现成的身份是 `tool.ConversationSessionIDFromContext(ctx)`（`pkg/tool/state.go:249`）：
有显式的对话 ID 就返回它，没有就回落到 `llm.AgentSessionIDFromContext(ctx)`。所有 surface 都会设置对话 ID：
TUI 在 `pkg/tui/chat_turn.go:729`，gateway 在 `pkg/gateway/server.go`、`pkg/gateway/run_control.go`、
`pkg/gateway/api_extra.go`，subagent 在 `pkg/run/subagent.go`（多处 `tool.WithConversationSessionID(..., prep.sessionID / sid)`）。
主 agent 的对话 ID 和 agent 会话 ID 相同。

本计划新增**一个**命名明确的入口 `tool.PlanSessionIDFromContext(ctx)`（就是 `ConversationSessionIDFromContext`），
凡是「从 ctx 推出计划目录」的地方一律用它，**不要**用 `llm.AgentSessionIDFromContext` 或 `sessionKey(ctx)` 推计划目录
（这两个对 subagent 返回的是 worker 会话）。会话的 **mode 状态**（`state.Get/Switch`）仍然按原来的 `sid` 读写，不要改。

### 仓库约定

- Go 1.26，单模块，CGO 必须开。注释用英文，解释「为什么」，密度跟周围代码一致（看 `pkg/state/plan.go`、`pkg/state/mode.go` 的注释风格）。
- 测试：`pkg/state` 和 `pkg/tool` 用标准库 `testing` + `t.Fatalf`；`pkg/turn`、`pkg/gateway` 用 `testify/require`。新测试跟所在文件一致。
- 每会话存储空 ID 一律映射成 `"default"`（见 `pkg/state/mode.go` 的 `modePath`、`pkg/state/todo.go` 的 `todoPath`）。
- 不新增 package import 关系（本计划只在已有的 import 边上加调用，`scripts/package-graph.sh` 不受影响）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./pkg/state ./pkg/tool ./pkg/process ./pkg/run ./pkg/turn ./pkg/gateway ./pkg/tui` | exit 0 |
| 单包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` | `ok` |
| 相关包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tool ./pkg/process ./pkg/run ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` | 全部 `ok` |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（即 `make test`） | 全部 `ok` |
| 残留检查 | `grep -rn 'PlanPathForProject\|GetPlanForProject\|SetPlanForProject' --include='*.go' pkg cmd` | 无输出 |

基线（advisor 2026-10-09 实测）：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/turn ./pkg/tool ./pkg/process -run 'Plan|AgentContext' -count=1` 四个包全部 `ok`。

## Suggested executor toolkit

- 真机验收（Step 9）用仓库自带的 `run-forebrain` skill（`.claude/skills/run-forebrain/SKILL.md`）。
  先读它的「Run (agent path)」一节；`toolcall` 模式就是「先 enter_plan_mode、再 exit_plan_mode」。

## Scope

**In scope**（只改这些文件）：
- `pkg/state/plan.go`、`pkg/state/plan_test.go`
- `pkg/tool/state.go`（只新增 `PlanSessionIDFromContext`）
- `pkg/tool/session_tools.go`、`pkg/tool/shell_tool.go`
- `pkg/tool/session_tools_test.go`、`pkg/tool/request_permissions_test.go`
- `pkg/process/session_context.go`、`pkg/process/session_context_test.go`
- `pkg/run/config.go`、`pkg/run/orchestration_llm_test.go`
- `pkg/turn/approval.go`、`pkg/turn/model.go`、`pkg/turn/executor.go`
- `pkg/turn/approval_test.go`、`pkg/turn/model_test.go`，以及 `pkg/turn` 下 /fork 测试所在的文件（Step 5 新增测试）
- `pkg/gateway/api_extra.go`（只改 `handleSessionPlanMarkdown` 一行）、`pkg/gateway/api_extra_test.go`、
  `pkg/gateway/slash_handlers_test.go`、`pkg/gateway/channels_test.go`
- `pkg/tui/chat_surface.go`（只改注释）、`pkg/tui/chat_session_test.go`
- `pkg/migrate/assets.go`、`pkg/migrate/codex_assets.go`、`pkg/migrate/assets_test.go`、`pkg/migrate/codex_test.go`
- `pkg/state/mode.go`（只改 `RemoveSessionStateFiles` 的注释）
- 编译或测试失败**直接指向**的其他 `_test.go`（只做同样的机械替换）

**Out of scope**（不要碰，哪怕看起来相关）：
- `pkg/tui/overlays.go`、gateway 审批卡接口：它们只读 `PlanFilePath`，路径一改对就自动正确。
- `pkg/tool/state.go` 的 `isSanctionedPlanWrite` / `GuardWrite` / `isWithinDir`：语义不变，换了可写目录就自动生效。
- 任何迁移/回退逻辑（owner 裁决：不迁移）。这也包括：**不要**把以前 `/migrate` 已经导入到扁平目录的旧副本挪进会话目录。
- `state.RemoveSessionStateFiles` 的**代码**：按 owner 裁决 5，删会话不碰计划目录，只改它的注释。
- `pkg/migrate/codex_assets.go` 里跨 thread 撞名时加 `threadIDSuffix` 的逻辑：每个 thread 有了自己的目录后它已不再必要，但保持原样，
  免得改动已导入计划的文件名。
- `pkg/migrate` 里除计划以外的任何导入逻辑（会话、记忆、技能、MCP……）。
- plan-mode 提醒的文案（`buildPlanModeReminder`）：它说的「目录里最新的 .md 是当前计划」在会话目录下依然成立；
  改文案会让所有会话的提示缓存失效，不值得。
- `pkg/run/subagent.go`、`pkg/run/turn_input.go` 等 003 正在改的文件。

## Git workflow

- 分支：`fix/session-scoped-plan-files`
- 提交信息遵循仓库强制的 Conventional Commits，例如 `fix(plan): scope plan files to the conversation`。
- **不要 push，不要开 PR**；按 owner 规矩，提交由 owner 手动完成——除非调度你的人明确要求你提交。

## Steps

### Step 1: 在 `pkg/state/plan.go` 加入按会话的 API（旧函数暂时保留，保证全仓还能编译）

在 `PlanDirForProject` 后面新增下列函数（目标形状如下，注释可微调但要保留「为什么」）：

```go
// PlanDirForSession returns the directory holding one conversation's plan
// files: <PlanDirForProject>/<session>. Plans belong to a conversation, not to a
// project: two conversations planning in the same project — in one process or
// in several terminals — must never resolve, show or edit each other's plan.
// A conversation's subagents share its directory; callers derive the session
// with tool.PlanSessionIDFromContext.
func PlanDirForSession(workspaceRoot, projectKey, sessionID string) string {
	return filepath.Join(PlanDirForProject(workspaceRoot, projectKey), planSessionSegment(sessionID))
}

// planSessionSegment turns a session id into a single directory name. Anything
// that could reach outside the project's plan directory is neutralised, and an
// empty id maps to "default" like every other per-session store (see modePath).
func planSessionSegment(sessionID string) string {
	s := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		default:
			return r
		}
	}, strings.TrimSpace(sessionID))
	s = strings.Trim(s, ". ")
	if s == "" {
		return "default"
	}
	return s
}

// PlanPathForSession returns a conversation's current plan file: the newest
// .md file directly in PlanDirForSession, or a default <dir>/plan.md when the
// conversation has not written one yet. The returned path may not exist.
func PlanPathForSession(workspaceRoot, projectKey, sessionID string) string {
	dir := PlanDirForSession(workspaceRoot, projectKey, sessionID)
	if f := newestMarkdown(dir); f != "" {
		return f
	}
	return filepath.Join(dir, "plan.md")
}

// GetPlanForSession reads a conversation's current plan. A missing file yields ("", nil).
func GetPlanForSession(workspaceRoot, projectKey, sessionID string) (string, error) { /* 与 GetPlanForProject 同体，改调 PlanPathForSession */ }

// SetPlanForSession writes content to a conversation's current plan file,
// creating its directory; with no plan yet it writes <dir>/plan.md.
func SetPlanForSession(workspaceRoot, projectKey, sessionID, content string) error { /* 与 SetPlanForProject 同体，改调 PlanPathForSession */ }

// CopySessionPlans copies every plan file of one conversation into another's
// directory, keeping modification times so the plan that was current in the
// source stays current in the copy. /fork uses it: a fork starts with the plans
// its source had. A source without plans is not an error.
func CopySessionPlans(workspaceRoot, projectKey, fromSessionID, toSessionID string) error { ... }
```

`CopySessionPlans` 的要求：
- `src == dst` 时直接返回 nil；
- `os.ReadDir(src)` 返回 `os.IsNotExist` 时返回 nil，且**不创建** dst 目录；
- 只复制 `newestMarkdown` 认作计划的文件：非目录、不以 `.` 开头、后缀不区分大小写为 `.md`。
  把这个判断抽成一个小函数（例如 `isPlanMarkdown(e os.DirEntry) bool`），`newestMarkdown` 和 `CopySessionPlans` 共用；
- 目标文件用 `os.WriteFile(target, b, 0o600)` 写，然后 `os.Chtimes(target, mt, mt)`，`mt` 是源文件的 `ModTime()`；
- 遇到任何读写错误直接返回该错误。

同时更新文件顶部的 package 注释（`pkg/state/plan.go:1-14`）：计划在
`<workspaceRoot>/plans/<projectKey>/<session>/*.md`（没有 projectKey 时是 `<workspaceRoot>/plans/<session>/*.md`），
「当前计划」是**该会话目录**里最新的 `.md`；再补一句：升级前留在 `plans/<projectKey>/*.md` 的文件是历史，不会被任何会话当作当前计划。

**Verify**: `go build ./pkg/state && CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → exit 0，`ok`

### Step 2: `pkg/state/plan_test.go`——新 API 的测试

新增以下测试（标准库 `testing`，用 `os.Chtimes` 给文件设**明确**的 mtime，不要依赖写入先后）：

1. `TestPlanSessionsResolveOnlyTheirOwnPlans`（**本 bug 的回归测试**）：
   `root := t.TempDir()`；会话 `cli-a` 的目录里写 `a.md`（内容 `# A`，mtime = t0）；
   扁平项目目录 `PlanDirForProject(root, "proj")` 里写 `legacy.md`（mtime = t0+1min）；
   会话 `cli-b` 的目录里写 `b.md`（mtime = t0+2min）。断言：
   - `PlanPathForSession(root, "proj", "cli-a") == filepath.Join(PlanDirForSession(root, "proj", "cli-a"), "a.md")`
   - `GetPlanForSession(root, "proj", "cli-a")` 返回 `"# A"`
   - 没写过计划的 `cli-c`：`PlanPathForSession` 返回 `filepath.Join(PlanDirForSession(root,"proj","cli-c"), "plan.md")`，
     `GetPlanForSession` 返回 `""`、err 为 nil
2. `TestPlanDirForSessionStaysInsideProjectDir`：表驱动，`PlanDirForSession(root, "proj", id)` 的 `filepath.Dir(...)`
   必须等于 `PlanDirForProject(root, "proj")`；`id` 取 `"cli-1"`、`""`（base 为 `default`）、`"  "`（`default`）、
   `"../escape"`、`"a/b"`、`"wx:abc"`（base 为 `wx-abc`）、`".."`（`default`）。
3. `TestCopySessionPlansKeepsTheCurrentPlanCurrent`：源会话写 `old.md`（t0）、`new.md`（t0+1min）、`notes.txt`、`.hidden.md`；
   `CopySessionPlans(root, "proj", "src", "dst")` 后：dst 里只有 `old.md` 和 `new.md`，内容一致；
   `filepath.Base(PlanPathForSession(root,"proj","dst")) == "new.md"`。
4. `TestCopySessionPlansWithoutSourceIsNoop`：源目录不存在 → 返回 nil，且 `os.Stat(PlanDirForSession(root,"proj","dst"))` 为 `os.IsNotExist`。

旧的 `PlanPathForProject` / `SetPlanForProject` / `GetPlan` 相关测试**先不动**（Step 8 统一改），这一步只加测试。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestPlan' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
→ 四个新测试都是 `--- PASS`，最后一行 `ok`

### Step 3: `pkg/tool`——`PlanSessionIDFromContext`，以及 enter/exit 工具和 shell 环境

1. 在 `pkg/tool/state.go` 紧挨着 `ConversationSessionIDFromContext` 后新增：
   ```go
   // PlanSessionIDFromContext names the conversation whose plan files a call
   // reads and writes. Plans belong to the user-visible conversation, not to a
   // subagent's worker transcript: a subagent that inherits plan mode works in
   // its parent conversation's plan directory, so the parent's exit_plan_mode
   // sees what it wrote. Every plan directory derived from a context goes
   // through here.
   func PlanSessionIDFromContext(ctx context.Context) string {
   	return ConversationSessionIDFromContext(ctx)
   }
   ```
2. `pkg/tool/session_tools.go`：
   - `newEnterPlanModeTool`：在 `state.Switch` 之后取 `planSID := PlanSessionIDFromContext(ctx)`；
     `planDir := state.PlanDirForSession(stateRoot, projectKey, planSID)`；
     `existingContent, _ := state.GetPlanForSession(stateRoot, projectKey, planSID)`；
     `respPlanFile = state.PlanPathForSession(stateRoot, projectKey, planSID)`。`state.Get/Switch` 仍用 `sid`。
   - `newExitPlanModeTool`：同理，`planDir` 和 `planFile` 改用 `PlanDirForSession` / `PlanPathForSession(..., planSID)`。
3. `pkg/tool/shell_tool.go` 的 `planModeShellEnv`：`sid == ""` 的提前返回保持不变；
   `planDir` / `planFile` 改为 `state.PlanDirForSession(stateRoot, projectKey, PlanSessionIDFromContext(ctx))` 和对应的 `PlanPathForSession`。
4. 更新测试（机械替换，**session ID 必须和该测试 ctx 里的 sid 一致**）：
   - `pkg/tool/request_permissions_test.go` 的 `planModeWriteCtx(home, sid)`：`WithAllowedPlanPath(ctx, state.PlanDirForSession(home, "", sid))`；
     该文件里 `state.PlanPathForProject(home, "")` → `state.PlanPathForSession(home, "", sid)`；
     `TestPlanModeWriteFileAllowsPlanFileUnderStateRoot` 里的 `state.PlanDirForProject(stateRoot, "forebrain")` → `state.PlanDirForSession(stateRoot, "forebrain", sid)`
     （同时检查该测试自己构造的 ctx：它的 AllowedPlanPath 也必须是这个会话目录）。
   - `pkg/tool/session_tools_test.go`：`state.SetPlanForProject(home, "", X)` → `state.SetPlanForSession(home, "", sid, X)`；
     `planDir := state.PlanDirForProject(home, "")` → `state.PlanDirForSession(home, "", sid)`。
5. 新增测试：
   - `pkg/tool/request_permissions_test.go`：`TestPlanModeWriteFileStaysInOwnSessionPlanDir`。仿照 `TestPlanModeWriteFileAllowsPlanFile` 的写法：
     ctx = `planModeWriteCtx(home, "sid-a")`；写 `PlanDirForSession(home, "", "sid-a")/mine.md` 成功；
     写 `PlanDirForSession(home, "", "sid-b")/theirs.md` 返回 err，且文件不存在；
     写 `PlanDirForProject(home, "")/flat.md` 返回 err，且文件不存在。只断言 `err != nil` 和文件不存在，**不要**断言具体错误文本。
   - `pkg/tool/session_tools_test.go`：`TestExitPlanModeReportsOwnSessionPlan`。仿照 `TestExitPlanModeRestoresModeNoPrompts`：
     sid = `"sid-own"`；`state.SetPlanForSession(home, "", sid, "# Mine")`；再在 `PlanDirForSession(home, "", "sid-other")` 写一个 mtime 更新的
     `theirs.md`；执行 exit 工具后断言输出含 `PlanPathForSession(home, "", sid)`、不含 `"theirs.md"`。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → exit 0，`ok`

### Step 4: `pkg/process/session_context.go` 与 `pkg/run/config.go`

1. `AgentContextForProject`：把
   `agCtx = tool.WithAllowedPlanPath(agCtx, state.PlanDirForProject(stateRoot, projectKey))`
   改成
   `agCtx = tool.WithAllowedPlanPath(agCtx, state.PlanDirForSession(stateRoot, projectKey, tool.PlanSessionIDFromContext(agCtx)))`。
   这里传 `agCtx`（此时它已带上 `WithAgentSessionID(base, sid)`）：base 里有对话 ID 就用对话 ID，没有就用 `sid`（已默认成 `"default"`）。
   顺手更新函数上方注释里 "the allowed plan path" 的说明：它现在是**该对话**的计划目录。
2. `pkg/run/config.go` 的 `(*planModeLLM).Execute`：在算 `projectKey` 之后加
   `planSID := tool.PlanSessionIDFromContext(ctx)`，然后把 `planDir` / `planFile` / `GetPlanForProject` 三处换成
   `PlanDirForSession` / `PlanPathForSession` / `GetPlanForSession(..., planSID)`。`state.Get(w.stateRoot, sid)` 不变。
3. `executeImplementationPhase`：同样取 `planSID := tool.PlanSessionIDFromContext(ctx)`，三处换成会话版本。
4. 更新测试：
   - `pkg/process/session_context_test.go`：`TestAgentContextDefaults` 期望改为 `state.PlanDirForSession(root, "", "default")`；
     `TestAgentContextPlanMode` 改为 `state.PlanDirForSession(home, "", "sid")`；
     `TestAgentContextIsolatesTwoAgents` 改为 `state.PlanDirForSession(mainRoot, "", "sid")` / `state.PlanDirForSession(reviewRoot, "", "sid")`。
   - 新增 `TestAgentContextPlanDirFollowsTheConversation`：`base := tool.WithConversationSessionID(context.Background(), "conv-1")`，
     `ctx := AgentContext(base, root, "worker-1")`，断言 `tool.AllowedPlanPathFromContext(ctx) == state.PlanDirForSession(root, "", "conv-1")`；
     再断言不带对话 ID 时（`AgentContext(context.Background(), root, "s2")`）是 `PlanDirForSession(root, "", "s2")`。
   - `pkg/gateway/channels_test.go`（调 `process.AgentContextForProject` 的那个测试）：期望改成 `state.PlanDirForSession(stateRoot, s.projectKey(), sid)`。
   - `pkg/run/orchestration_llm_test.go`：所有 `state.PlanDirForProject(home, "")` / `PlanPathForProject(home, "")` / `GetPlanForProject(home, "")` / `SetPlanForProject(home, "", X)`
     换成对应的 `...ForSession(home, "", <该测试 ctx 里的 sid>)`。sid 一般就在同一个测试函数里（`sid := "..."`、`llm.WithAgentSessionID(ctx, sid)`）。
   - 新增 `pkg/run/orchestration_llm_test.go`：`TestPlanModeReminderNamesOnlyTheConversationsPlan`。仿照该文件里 `wrapPlanModeLLM(innerLLM, home)` + `capturingLLM` 的用法：
     会话 `sid-a` 处于 plan 模式（`state.Set(home, "sid-a", state.State{Mode: state.ModePlan})`）、写了 `PlanDirForSession(home,"","sid-a")/mine.md`；
     会话 `sid-b` 的目录里有一份 mtime 更新的 `theirs.md`；用带 `llm.WithAgentSessionID(ctx, "sid-a")` 的 ctx 调一次 `Execute`。
     断言：注入的提醒正文含 `PlanDirForSession(home,"","sid-a")`、不含 `"theirs.md"`；
     `toolpkg.AllowedPlanPathFromContext(innerLLM.gotCtx) == state.PlanDirForSession(home, "", "sid-a")`。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run ./pkg/gateway -count=1` → 全部 `ok`

### Step 5: `pkg/turn`——审批闸门、计划评审、`/status`、`/plan`、`/fork`

1. `pkg/turn/approval.go`：
   - `RunPlanReview`：`state.GetPlanForSession(strings.TrimSpace(in.StateRoot), strings.TrimSpace(in.ProjectKey), sessionID)`。
     把 `PlanReviewRun.StateRoot/ProjectKey` 字段上的注释补一句 "together with SessionID"。
   - `(*PendingApprovalGate).Pending`：`req.PlanFilePath = state.PlanPathForSession(stateRoot, projectKey, sessionID)`。
     更新 `PendingApprovalGate.PlanScope` 字段的注释：它给出计划目录所在的 stateRoot 和 projectKey，闸门再加上会话 ID 得出**该会话**的计划。
2. `pkg/turn/model.go` 的 `BuildStatusReport`：`state.GetPlanForSession(src.StateRoot, src.ProjectKey, sid)`。
3. `pkg/turn/executor.go`：
   - `/plan`：`state.GetPlanForSession(ctx.stateRoot(), ctx.projectKey(), ctx.SessionID)`。
   - `copySlashSessionState`：把开头 `GetPlanForProject` / `SetPlanForProject` 那整段 `if ... else if ...` 换成
     ```go
     // A fork starts with the plans its source had; each conversation then
     // works in its own plan directory.
     if err := state.CopySessionPlans(stateRoot, ctx.projectKey(), sourceSessionID, targetSessionID); err != nil {
     	return err
     }
     ```
4. 更新测试：
   - `pkg/turn/approval_test.go` 的 `TestPendingExitPlanRequestCarriesPlanReviewsAndModels`：
     `state.SetPlanForProject(stateRoot, "proj", X)` → `state.SetPlanForSession(stateRoot, "proj", sessionID, X)`；
     期望 `state.PlanPathForProject(stateRoot, "proj")` → `state.PlanPathForSession(stateRoot, "proj", sessionID)`。
   - `pkg/turn/model_test.go` 的 `TestBuildStatusReportAssemblesThePanel`：`state.SetPlanForSession(stateRoot, "proj", "s1", "a plan")`
     （该测试的会话就是 `"s1"`；确认 `BuildStatusReport` 调用里 `SessionID` 是 `"s1"`，不是就按 STOP 处理）。
5. 新增测试：
   - `pkg/turn/approval_test.go`：`TestPendingExitPlanRequestResolvesOnlyItsOwnSessionPlan`（**本 bug 在闸门层的回归测试**）。
     初始化照抄 `TestPendingExitPlanRequestCarriesPlanReviewsAndModels` 的前几行：
     ```go
     ctx := context.Background()
     _, runs, sessionID := planReviewEventDB(t)
     actions := &state.ActionService{DB: runs.DB}
     run, err := runs.CreateRun(ctx, sessionID, "plan the work")
     require.NoError(t, err)
     action, err := actions.CreatePending(ctx, sessionID, "exit_plan_mode", map[string]any{"session_id": sessionID})
     require.NoError(t, err)
     require.NoError(t, runs.SetWaitingAction(ctx, run.ID, state.Wait{
     	RunID: run.ID, ActionID: action.ID, ToolName: "exit_plan_mode", ToolInputJSON: "{}",
     }))
     ```
     然后：`stateRoot := t.TempDir()`；`state.SetPlanForSession(stateRoot, "proj", sessionID, "# Mine")`；
     在 `PlanDirForSession(stateRoot, "proj", "another-session")` 和扁平目录 `PlanDirForProject(stateRoot, "proj")` 各写一份内容为 `# Theirs`、
     mtime 比自己的计划新 1 分钟的 `.md`；`gate := &PendingApprovalGate{Runs: runs, Actions: actions, PlanScope: func(context.Context, string) (string, string) { return stateRoot, "proj" }}`；
     `req, err := gate.Pending(ctx, sessionID)`。断言 `req.PlanFilePath == state.PlanPathForSession(stateRoot, "proj", sessionID)`，
     且 `os.ReadFile(req.PlanFilePath)` 的内容是 `# Mine`。
   - `/fork` 复制计划：在 `pkg/turn/model_test.go` 的 `TestForkCopiesMaterializedModelSelection` 旁边新增 `TestForkCopiesThePlansIntoTheForksOwnDirectory`，
     照抄那个测试搭 fork 的方式（先读懂它怎么构造 `Context` 和调用 /fork）。源会话目录里写 `plan-a.md`，执行 /fork 后：
     目标会话目录里有内容一致的 `plan-a.md`，源会话的文件仍在，扁平项目目录里没有新文件。
     **如果搭不起 /fork 的测试环境（那个测试的做法不能直接套用），只写一个直接调用 `copySlashSessionState` 的单元测试也可以；两条都走不通就 STOP。**

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → `ok`

### Step 6: gateway 的 plan-md 接口，以及 TUI / gateway 剩余的测试

1. `pkg/gateway/api_extra.go`，函数 `handleSessionPlanMarkdown`：
   `p, err := state.GetPlanForSession(s.stateRoot(), s.projectKey(), sid)`。把上面那句注释改成：
   计划按项目 **和会话** 解析，`plan-md` 返回的是 URL 里这个会话自己的计划。
2. `pkg/tui/chat_surface.go` 的 `approvalGate()` 里 `PlanScope` 上方的注释：把
   `state.PlanPathForProject(stateRoot, projectKey)` 改成 `state.PlanPathForSession(stateRoot, projectKey, sessionID)`，意思不变（强调要用 stateRoot 而不是 home）。
3. 更新测试：
   - `pkg/gateway/api_extra_test.go`：`state.SetPlanForProject(s.stateRoot(), s.projectKey(), X)` → `state.SetPlanForSession(s.stateRoot(), s.projectKey(), "s1", X)`（该 helper 返回的会话就是 `"s1"`）。
   - `pkg/gateway/slash_handlers_test.go` 的 `TestHandleStatusSlashUsesRuntimeStores`：`state.SetPlanForSession(stateRoot, "", "s1", "1. do work")`。
   - `pkg/tui/chat_session_test.go`：
     - `TestBuildSurfaceToolApprovalRequestExitPlanModePlanPath`：`wantPath := state.PlanPathForSession(stateRoot, "", "s1")`、
       `state.SetPlanForSession(stateRoot, "", "s1", wantPlan)`、`homePath := state.PlanPathForSession(home, "", "s1")`；它上方注释里的
       `state.PlanPathForProject(stateRoot, projectKey)` 同步改。
     - 其余 `state.SetPlanForProject(cs.stateRoot(), runnerProjectKey(cs.runner()), X)`（几个 plan-review 的测试 helper 里）：
       换成 `state.SetPlanForSession(cs.stateRoot(), runnerProjectKey(cs.runner()), <该 helper 创建 pending action 时用的 session id>, X)`。
       session id 就在同一个 helper 往上几行的 `actions.CreatePending(ctx, "<sid>", ...)` / `runs.CreateRun(ctx, "<sid>", ...)` 里。
       **找不到明确的 session id 就 STOP。**
   - 新增 `pkg/gateway/api_extra_test.go`：`TestSessionPlanMarkdownReturnsOnlyThatSessionsPlan`。目前没有任何测试覆盖
     `handleSessionPlanMarkdown`，直接调用 handler，路由参数的注入方式照抄 `pkg/gateway/channels_test.go:1092-1098`：
     ```go
     req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/s1/plan-md", nil)
     req = req.WithContext(context.WithValue(req.Context(), ParamsKey, Params{
     	{Key: "id", Value: "s1"},
     }))
     rr := httptest.NewRecorder()
     s.handleSessionPlanMarkdown(rr, req)
     ```
     `s` 用最简单的 `&Server{Home: t.TempDir(), Runner: &run.Runner{Deps: &run.Deps{ProjectKey: "proj"}}}`
     （与该文件里返回 `"s1"` 的那个 helper 构造 `Server` 的方式一致；若 `s.stateRoot()` 需要更多字段，就照抄那个 helper）。
     会话 `s1` 的计划是 `# Mine`，会话 `s2` 的计划 mtime 更新、内容是 `# Theirs`；断言 `rr.Code == 200`，
     响应 JSON 的 `markdown` 字段等于 `# Mine`。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/tui -count=1` → 全部 `ok`

### Step 7: `/migrate` 把导入的计划放进所属对话的会话目录；`RemoveSessionStateFiles` 注释写明保留计划

1. `pkg/migrate/assets.go` 的 `PlanOutcome` 加一个字段（放在 `ProjectKey` 后面）：
   ```go
   SessionID  string // the imported conversation whose plan directory received it; "" for the unscoped plans root
   ```
2. `pkg/migrate/codex_assets.go` 的 `writeExtractedPlan` 改成带会话 ID：
   `func writeExtractedPlan(workspace, projectKey, sessionID, name, body string, dryRun bool) PlanOutcome`
   - `sessionID != ""`：`destDir := state.PlanDirForSession(workspace, projectKey, sessionID)`，
     `scope` 写成 `"project " + projectKey + ", conversation " + sessionID`（`projectKey == ""` 时写 `"conversation " + sessionID`）；
   - `sessionID == ""`：保持原样，`destDir := state.PlanDirForProject(workspace, projectKey)`，`scope` 不变；
   - 三态幂等逻辑（up-to-date / diverged / dryRun / skipped / installed）**一行都不改**，只是每个返回的 `PlanOutcome` 都带上 `SessionID: sessionID`。
   - 更新函数注释：它是 Claude 和 Codex 两个导入器共用的「写一份计划」。
3. `extractCodexPlans`：调用改成 `writeExtractedPlan(workspace, projectKey, target, name, body, opts.DryRun)`。
   `target` 就是 `parsed` 的 key，等于 `codexSessionTarget(threadID)`。`used` / `threadIDSuffix` 的撞名逻辑不动。
4. `importPlans`（Claude）：
   - 用 `planSlugSessions` 替换 `planSlugProjects`：
     ```go
     // planSlugSessions maps a plan slug to every imported conversation that
     // carried it. A slug shared by several conversations gives each of them its
     // own copy of the plan (owner decision 2026-10-09): a plan belongs to the
     // conversation that resumes it, and any of them may be the one resumed.
     func planSlugSessions(data *claudeData) map[string][]claudeSession
     ```
     过滤条件保持不变（`session.Slug == "" || session.Cwd == ""` 就跳过）；同一 slug 下按 `claudeSessionTarget(session.SessionID)`
     去重，并按它升序排序，保证输出顺序确定。删除 `planSlugProjects`（之后没有调用方）。
   - 循环改成：先读一次 `body`（读失败照旧给一条 `skipped`，`SessionID` 为空，然后 `continue`），把 body 规范成
     `string(bytes.TrimRight(body, "\n")) + "\n"`（就是现在写盘时用的形式）。
     - `sessions := slugSessions[plan.Name]` 为空：调用 `writeExtractedPlan(workspace, "", "", plan.Name, normalized, opts.DryRun)`，
       状态为 `installed` 时在 `Detail` 后面追加原来那句 `" (no conversation matched this plan's slug)"`。
     - 否则对每个 session：`projectKey := memory.ProjectKey(session.Cwd)`，
       `writeExtractedPlan(workspace, projectKey, claudeSessionTarget(session.SessionID), plan.Name, normalized, opts.DryRun)`，每份副本 append 一条 outcome。
   - 删掉 `importPlans` 里不再使用的局部变量和 import（`go build` 会提示）。更新 `importPlans` 的函数注释：计划写进
     `<workspaceRoot>/plans/<projectKey>/cli-<session>/<slug>.md`；一个 slug 被多个对话携带时每个对话一份；
     slug 没有匹配任何对话的计划写进不带项目的计划根目录，只当历史，没有任何对话会读它。
5. `pkg/state/mode.go` 的 `RemoveSessionStateFiles` 注释末尾补一段（**代码不改**）：
   ```go
   // A session's plan directory (plans/<project>/<session>/, see
   // PlanDirForSession) is deliberately not on this list: plans are kept as
   // history after the conversation that wrote them is gone (owner decision
   // 2026-10-09).
   ```
6. 更新测试：
   - `pkg/migrate/assets_test.go` 的 `TestImportPlansAttribution`：`claudeSession{Slug: "known-slug", Cwd: project}` 加上 `SessionID: "s-1"`；
     `scoped` 改成 `filepath.Join(state.PlanDirForSession(workspace, memory.ProjectKey(project), "cli-s-1"), "known-slug.md")`；
     断言 `outcomes[0].SessionID == "cli-s-1"`、`outcomes[1].SessionID == ""`；orphan 的路径和断言不变；divergence 部分不变。
     再加一条断言：`state.GetPlanForSession(workspace, memory.ProjectKey(project), "cli-s-1")` 返回以 `# scoped` 开头的内容
     （证明导入的对话 resume 后能读到它）。若该文件还没 import `pkg/state`，就加上。
   - `pkg/migrate/codex_test.go` 的 `TestCodexPlansExtracted`：读文件的路径改成
     `filepath.Join(state.PlanDirForSession(opts.AgentWorkspace, report.Plans[0].ProjectKey, report.Plans[0].SessionID), report.Plans[0].Name+".md")`，
     并断言 `strings.HasPrefix(report.Plans[0].SessionID, "cli-")`。
7. 新增测试 `pkg/migrate/assets_test.go`：`TestImportPlansCopiesIntoEveryConversationWithTheSlug`。仿照 `TestImportPlansAttribution`：
   同一个 project 下两个会话 `{SessionID: "s-b", Slug: "shared", Cwd: project}`、`{SessionID: "s-a", Slug: "shared", Cwd: project}`，
   一份计划 `shared`。断言：返回 2 条 outcome，`SessionID` 依次是 `cli-s-a`、`cli-s-b`（排过序），状态都是 `installed`；
   两个会话目录下都有 `shared.md`，内容相同；`state.PlanDirForProject(workspace, memory.ProjectKey(project))` 正下方**没有** `shared.md`。
   再跑一次 `importPlans`：两条都是 `up-to-date`。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/migrate ./pkg/state -count=1` → 全部 `ok`

### Step 8: 删除按项目解析「当前计划」的旧 API，扫尾

1. 从 `pkg/state/plan.go` 删掉 `PlanPathForProject`、`GetPlanForProject`、`SetPlanForProject`。**保留** `PlanDirForProject`
   （它是所有会话目录的父目录，`pkg/migrate` 也在用）。
2. `go build ./... && go vet ./...` 报错的地方，全部按 Step 3–6 的规则换成会话版本。
3. `pkg/state/plan_test.go` 里剩下的旧测试：
   - 只测 `PlanDirForProject` 的三个测试（`TestPlanPlanDir_DefaultGatewayScope`、`TestPlanPlanDirForProject_UsesProjectKeyVerbatim`、
     `TestPlanPlanDirForProject_EmptyProjectUsesPlansRoot`）**原样保留**；
   - 其余用到 `PlanPathForProject` / `SetPlanForProject` / `GetPlan` 的测试，统一用会话 `"s1"` 改写成会话版本
     （`PlanDirForProject(tmpDir, "")` 用来放计划文件的地方改成 `PlanDirForSession(tmpDir, "", "s1")`）；
   - `TestPlanGet_EmptySessionID` 改成 `SetPlanForSession(tmpDir, "", "", ...)` + `GetPlanForSession(tmpDir, "", "")`（空 ID 映射到 `default`，测试名的含义正好对上）；
   - 删掉文件末尾只为测试保留的 `GetPlan` helper（没有调用方之后）。
4. 残留检查，三条都必须**无输出**：
   - `grep -rn 'PlanPathForProject\|GetPlanForProject\|SetPlanForProject' --include='*.go' pkg cmd`
   - `grep -rn 'PlanDirForProject' --include='*.go' pkg cmd | grep -v '_test.go' | grep -v 'pkg/migrate/' | grep -v 'pkg/state/plan.go'`
   - `grep -rn 'PlanDirForProject' --include='*_test.go' pkg | grep -v 'pkg/migrate/' | grep -v 'pkg/state/plan_test.go'`

**Verify**: `go build ./... && go vet ./... && CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → exit 0，全部 `ok`；上面三条 grep 均无输出

### Step 9: 真机验收（复现流程，改完后必须不再串台）

用 `run-forebrain` skill 的 driver。WORK 指向一个临时目录，避免和别人正在跑的 driver 冲突：

```bash
export FOREBRAIN_RUN_WORK="$(mktemp -d)/fbrun" FOREBRAIN_RUN_SESSION=fb-plan-iso FOREBRAIN_RUN_PORT=8797
D=.claude/skills/run-forebrain/driver.sh
$D reset && $D start toolcall          # 末行 "started: session=fb-plan-iso provider=toolcall ..."
KEY=$(cd "$FOREBRAIN_RUN_WORK/proj" && pwd -P | tr '/:' '--')
PLANS="$FOREBRAIN_RUN_WORK/home/workspace/plans/$KEY"
mkdir -p "$PLANS/cli-someone-else"
printf '# FOREIGN FLAT PLAN\n' > "$PLANS/other-process-plan.md"
printf '# FOREIGN SESSION PLAN\n' > "$PLANS/cli-someone-else/their-plan.md"
$D submit 'plan a tiny change'
$D wait 'enter_plan_mode' 25 && $D key Enter      # 批准 enter_plan_mode（第一项 "Yes, proceed"）
$D wait 'exit_plan_mode' 25
$D screen | tail -20
```

期望（全部满足）：
- `$D screen | grep -c 'FOREIGN'` 输出 `0`（**修复前**这里会显示 `FOREIGN FLAT PLAN`）；
- `$D screen | grep -c 'No plan found'` 输出 `1`（这个会话自己没写过计划）。

然后选「Yes, proceed」（`$D key Down` 再 `$D key Enter`），`sleep 3` 后：
```bash
sqlite3 "$FOREBRAIN_RUN_WORK/home/state/forebrain.state.sqlite" "select * from fb_messages" | grep -o 'Implement the approved plan in [^"\\]*' | head -1
```
期望：路径在 `"$PLANS/<本会话 id>/"` 下、以 `/plan.md` 结尾，**不含** `other-process-plan.md`，也不含 `cli-someone-else`。最后执行 `$D stop`。

**Verify**: 以上输出全部符合期望。把 `$D screen | tail -20` 的输出和那条 sqlite 结果原样贴进执行报告。

## Test plan

新增测试（都要先在修复前的代码上**跑红**一次，确认它们真的锁住了这个 bug；做法：先写测试，临时把被测调用换回旧的按项目版本跑一次，看到失败后再恢复）：

| 测试 | 文件 | 锁住什么 |
|---|---|---|
| `TestPlanSessionsResolveOnlyTheirOwnPlans` | `pkg/state/plan_test.go` | 别的会话 / 扁平目录里更新的文件不会成为本会话的当前计划 |
| `TestPlanDirForSessionStaysInsideProjectDir` | `pkg/state/plan_test.go` | 会话 ID 无法逃出项目计划目录；空 ID → `default` |
| `TestCopySessionPlansKeepsTheCurrentPlanCurrent` / `TestCopySessionPlansWithoutSourceIsNoop` | `pkg/state/plan_test.go` | /fork 复制计划的语义 |
| `TestPlanModeWriteFileStaysInOwnSessionPlanDir` | `pkg/tool/request_permissions_test.go` | plan 模式下写不进别的会话目录和扁平目录 |
| `TestExitPlanModeReportsOwnSessionPlan` | `pkg/tool/session_tools_test.go` | 批准后告诉模型的是自己的计划 |
| `TestAgentContextPlanDirFollowsTheConversation` | `pkg/process/session_context_test.go` | subagent（worker ID ≠ 对话 ID）写进父对话的目录 |
| `TestPlanModeReminderNamesOnlyTheConversationsPlan` | `pkg/run/orchestration_llm_test.go` | 每轮提醒不会把别人的计划说成 current |
| `TestPendingExitPlanRequestResolvesOnlyItsOwnSessionPlan` | `pkg/turn/approval_test.go` | 审批闸门（TUI 审批框、web 审批卡共用）只解析本会话的计划 |
| `TestForkCopiesThePlansIntoTheForksOwnDirectory` | `pkg/turn/model_test.go` | /fork 之后两边各有自己的目录 |
| `TestSessionPlanMarkdownReturnsOnlyThatSessionsPlan` | `pkg/gateway/api_extra_test.go` | web 的 plan-md 接口 |
| `TestImportPlansCopiesIntoEveryConversationWithTheSlug` | `pkg/migrate/assets_test.go` | 一个 slug 对应多个会话时每个会话一份，不写进扁平目录 |
| `TestImportPlansAttribution`（改写） | `pkg/migrate/assets_test.go` | Claude 导入的计划落在 `cli-<id>` 会话目录，并能被 `GetPlanForSession` 读到 |
| `TestCodexPlansExtracted`（改写） | `pkg/migrate/codex_test.go` | Codex 抽出的计划落在 `cli-<thread>` 会话目录 |

`TestImportPlansCopiesIntoEveryConversationWithTheSlug` 和两条改写的 migrate 测试锁的是新行为，不是旧 bug，不需要「先跑红」。
另外：所有已有的 plan 相关测试改成会话版本后必须全部通过。Step 9 是端到端验收。

## Done criteria

ALL must hold:

- [ ] `go build ./...` 与 `go vet ./...` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部 `ok`；Test plan 表里的新测试和改写测试都存在并通过
- [ ] `grep -n 'planSlugProjects' pkg/migrate/*.go` 无输出；`grep -n 'SessionID' pkg/migrate/assets.go` 能看到 `PlanOutcome` 的新字段
- [ ] `RemoveSessionStateFiles` 的函数体与 `71c0449` 相同（`git diff 71c0449 -- pkg/state/mode.go` 只有注释行的改动）
- [ ] `grep -rn 'PlanPathForProject\|GetPlanForProject\|SetPlanForProject' --include='*.go' pkg cmd` 无输出
- [ ] Step 8 的另外两条 `PlanDirForProject` grep 无输出
- [ ] Step 9 的真机输出：`FOREIGN` 计数 0、`No plan found` 计数 1、批准后的路径在本会话目录下
- [ ] `git status` 只显示 Scope 里列出的文件（加上编译/测试失败直接指向的 `_test.go`）
- [ ] `plans/README.md` 里本计划的状态行已更新

## STOP conditions

Stop and report back (do not improvise) if:

- 「Current state」里的摘录和现行代码对不上（漂移）——尤其是 003 合入后 `pkg/gateway/api_extra.go`、`pkg/run/orchestration_llm_test.go` 里的相关函数。
- 你发现某个生产代码路径用来推计划目录的 ctx **没有**对话 ID，而且它的 `AgentSessionID` 是 subagent 的 worker 会话
  （表现为：subagent 在 plan 模式下写的计划，父对话 exit_plan_mode 时看不到）。不要自己发明新的身份来源，报告具体调用链。
- 除了上表列出的地方，还有别的生产代码在读写 `<stateRoot>/plans/...`（Step 8 的 grep 之外，例如有人直接 `filepath.Join(root, "plans", ...)`）。
- 某个测试要改的 session id 无法从测试代码里确定。
- 任何一步的 Verify 修了两次还是不过。
- 修复看起来需要改 `pkg/tui/overlays.go`、`isSanctionedPlanWrite`、`RemoveSessionStateFiles` 的代码，或者需要加迁移/回退逻辑（owner 已明确不要）。
- Step 7 里 `extractCodexPlans` 的 map key `target` 不是以 `cli-` 开头（说明它不是导入后的对话 ID）。
- Step 9 修复后仍然出现 `FOREIGN`。

## Maintenance notes

- **不变量**：凡是需要「当前会话的计划」的地方，都通过 `state.PlanDirForSession` / `PlanPathForSession` / `GetPlanForSession`，
  并且会话 ID 来自 `tool.PlanSessionIDFromContext(ctx)`（有 ctx 时）或该请求自己的会话 ID（闸门、HTTP handler、slash 命令）。
  新增读计划的入口时，review 要专门看会话 ID 是不是对话 ID，而不是 subagent 的 worker 会话 ID。
- **旧数据**：`plans/<projectKey>/*.md`（扁平目录）里的旧计划不会再被任何会话当作当前计划（owner 裁决：不迁移）。
  升级时正在 plan 模式或实现阶段的会话会看到 "No plan found" / 收不到实现提醒，模型需要重写计划。
- **/fork**：fork 会复制源会话目录里的计划文件；fork 的 transcript 里仍然引用源会话的绝对路径，fork 在 plan 模式下
  edit_file 那个旧路径会被拒绝，下一轮提醒会告诉它自己的目录——这是预期行为。
- **/migrate**：导入的计划落在 `plans/<projectKey>/cli-<id>/`，所以导入后的对话被 resume 时，前提是它运行时的 projectKey
  和导入时用 `memory.ProjectKey(cwd)` 算出来的一致——这是修复前就有的假设，本计划没有改变它。一个 Claude slug 对应多个会话时
  每个会话各有一份副本，之后各改各的，互不同步（owner 裁决）。升级前已经导入到扁平目录的旧副本留在原处；升级后再跑一次 `/migrate`，
  会在会话目录里再导入一份（报告显示 `installed`），扁平目录里的旧副本不受影响。
- **删会话**：`RemoveSessionStateFiles` 有意不删计划目录（owner 裁决，注释里写明了）。以后如果有人想「顺手」把计划目录加进清理列表，
  先回头找 owner 确认。
- **延后的事项**：
  - owner 已定方向：会话内容最终只存 SQLite，不落盘文件（见 `plans/README.md`「已审核但未计划的发现」）。计划文件迁库时，
    以「对话 ID」为键，与本计划的不变量一致。
