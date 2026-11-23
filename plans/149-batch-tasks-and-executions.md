# Plan 149：后台跑批——「任务 / 执行」模型、每次执行一个新会话与独立任务工作区、非交互语义（免提问、不插话、不切模式、禁记忆）、停止后同会话续跑、对接文档

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`145`、`146`、`147`、`148` 均为 DONE。
>
> **Drift check（先跑）**：`git diff --stat d9d0b4d -- pkg/state pkg/turn/submit.go pkg/turn/service.go pkg/run/turn_input.go pkg/run/subagent.go pkg/run/runner.go pkg/tool/registry.go pkg/tool/session_tools.go pkg/tool/state.go pkg/process/run_executor.go pkg/assembly/rules_hook.go pkg/gateway/server.go pkg/gateway/ws_protocol.go pkg/gateway/run_control.go pkg/gateway/api_extra.go`
> 有输出是正常的。按"现状"里的函数名核对；含义变了即 STOP 1。
>
> **基线**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/turn ./pkg/run ./pkg/tool ./pkg/process ./pkg/assembly ./pkg/gateway -count=1`（900 s），记下已红用例。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（新表 + 改回合准入、工作目录与 websocket 协议；跑批是对外接口）
- **Depends on**: `145`–`148`；沙箱隔离在 `150`（本计划先把工作目录切到任务工作区，`150` 再用 OS 沙箱把它关起来）
- **Category**: direction
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区

## owner 裁定（2026-10-26，本计划相关的全部原话要点）

1. 跑批"是专门用于作为底层 agent 引擎提供给其他业务系统跑批量任务的"；"执行任务是非交互式的，必须固定设置 agent 模式"；"llm 调用任何工具都必须免审批，
   且不能将 user_interaction、enter_plan_mode 和 exit_plan_mode 工具暴露给 llm"；"不能将记忆类工具暴露给 llm，就是后台跑批类项目必须禁用记忆功能"；
   "用户只能在 tui 和 web 端查看，不能发起对话，不能插话"。
2. 业务系统接入：**直接用 web 端发起对话的 websocket 协议**（`/ws/chat`），"业务系统跟 gateway 建立 websocket 连接发起对话接收流式输出"。
3. "**每次任务都是一个新会话，重试一个任务必须发起一次新会话**，不是在之前的会话里重跑；**每一次执行任务都必须在项目工作区建一个单独的目录，作为任务工作区**，
   任务工作区是一块隔离空间，必须在任务工作区里执行任务"。"**任务停止后的续跑是在原会话里续跑**"。
4. 后续裁定（选项题）：
   - 工作区目录 `<项目根>/tasks/<任务ID>/<执行序号>/`，内含 `inputs/`（业务系统上传的附件落这里）、`outputs/`（交付物）、`.work/`（中间文件）。
   - **"重新执行"不新建任务，每条任务有执行历史列表**：一条任务 → 多次执行；每次执行 = 一个新会话 + 一个新工作区；重新执行时把上一次的 `inputs/` 复制过去，任务内容沿用。
   - 业务系统表达方式：沿用 `POST /api/v1/projects/:id/sessions` 建会话 → `bind_session` → `start_run`；**不带 `task_id` = 新任务**（可带 `title`、`external_ref`），首条消息与附件就是任务输入；
     **带 `task_id` = 该任务的一次新执行**，`start_run` 内容可空（沿用任务输入）。响应里返回 `task_id`、执行序号、工作区路径。
   - **续跑**：只对"已停止"的执行开放；web 任务页"继续"、TUI `/continue`、业务系统对该会话发**空内容**的 `start_run` 都可触发；引擎用固定的"从停下的地方继续"；
     已完成、已失败的执行拒绝再跑（失败只能"新执行"重试）。
   - 模型用量上限：任务**直接失败**（结构化 `code` + `reset_at`），不自动续跑。
   - 允许的人工操作：停止运行中的任务、定时任务（每次触发 = 一条新任务）、web 手工新建任务、通道接入（`151`）、重新执行、续跑。
   - 沙箱隔离（OS 沙箱强制、只读本工作区 + 共享技能、MCP 共享）在 `150`。

## 设计总览（评审方定，执行者照做）

```
fb_tasks (一条业务任务)                       fb_task_executions (一次执行 = 一个会话)
  id "tsk-…", project_id, title,               session_id (PK, = 会话 id), task_id, seq (1,2,3…),
  input_text, input_set, external_ref,         workspace (绝对路径 <root>/tasks/<task_id>/<seq>),
  origin (api|web|cron|channel), origin_ref    status (pending|running|stopped|completed|failed),
                                               error_code, error_text, started_at, finished_at
```

**一次执行的生命周期**（引擎统一判定，所有入口共用 `turn.AdmitBatchTurn`）：

| 执行状态 | 来了一个回合请求 | 结果 |
|---|---|---|
| pending，任务还没有输入 | 有内容 / 附件 | 首回合：记录任务输入，回合输入 = 内容 |
| pending，任务已有输入（新执行） | **空**内容、无附件 | 首回合：回合输入 = 任务输入 |
| pending，任务已有输入 | 有内容或附件 | 拒绝 `batch_task_input_fixed` |
| stopped | **空**内容、无附件 | 续跑：回合输入 = `BatchContinuePrompt` |
| stopped | 有内容或附件 | 拒绝 `batch_takes_no_input` |
| running | 任何 | 拒绝 `batch_execution_running` |
| completed / failed | 任何 | 拒绝 `batch_execution_finished`（"请发起该任务的新执行"） |

回合跑起来以后：会话输入队列关闭（插话 / 排队一律拒绝）、强制 `Unattended`（用量上限不续跑）、工作目录 = 执行工作区、
`<forebrain_task_context>` 注入任务 ID / 执行序号 / 工作区 / inputs 文件清单；回合结束按结果把执行标为 completed / stopped（取消）/ failed，
若这次执行一个生成物都没交付，就把 `outputs/` 里的文件登记为 `origin='fallback'` 的生成物。

**跑批 runner 不注册的工具**：`user_interaction`、`enter_plan_mode`、`exit_plan_mode`、`request_permissions`、全部记忆工具；也不挂记忆注入 / 引用两个 LLM 包装器、不把记忆目录加进可读根。
（免审批本身——审批策略 never + 沙箱——在 `150`。）

## Current state

（行号为 2026-10-26 工作区近似值，以函数名为准。）

### 1. 回合入口与执行器

- `pkg/turn/submit.go` `(*Service).Submit`（约 `:71-189`）：拿锁 → `s.runner.Run(ctx, req, announce)` → 按结果分支：`*WaitingError` → waiting；`context.Canceled`/`DeadlineExceeded` → `TurnCancelled`；
  其它 err → `TurnFailed`（并 `s.autoContinue.turnEnded(ctx, req, runID, err)`）；成功 → `TurnCompleted`。所有界面（websocket、后台 detached、定时、通道、终端）都经它。
- `autoContinuer.planFor`（约 `:577-587`）：`req.Unattended` 或子运行 → 不续跑。
- `TurnRequest`（`pkg/turn/model.go` 约 `:39-79`）有 `Unattended bool`。
- `type Service struct`（`pkg/turn/service.go` 约 `:46-62`）+ `Option` 函数式选项（范例 `WithRunEventStore`）。
- `pkg/process/run_executor.go` `(*runExecutor).Run`（约 `:96-220`）：`agBase := AgentContextForProject(...)` → `runner, runnerErr := x.env.RunnerForTurn(ctx, sid)` → `run.Run(run.Options{... AgBase: agBase ...})`。
  `x.env.Control` 是 `*run.Controller`。
- `pkg/run/controller.go`：`SessionQueue(sessionID)` / `sessionQueueLocked` 返回会话的 `*InputQueue`；运行状态 `RunState.input` **就是**这个会话队列（约 `:159`：`input: c.sessionQueue(sessionID)`）。
- `pkg/run/turn_input.go` `InputQueue`（约 `:463-472`）字段 `mu, rt, steers, rejected, followUp, delivered, seq, hook`；`Steer(in)`（约 `:590-631`）、`FollowUp(in)`、`Recall()`。
- `pkg/run/subagent.go` `SendToSubagent(ctx, r *Runner, surface, conversationSessionID, agentKey, in)`（约 `:2026-2050`）是子代理视图插话的唯一引擎入口。

### 2. 工作目录

- `pkg/tool/state.go`：`WorkingDir(ctx)`（约 `:648-665`）= `ProjectRootFromContext(ctx)`，没有才退回进程目录；`ResolvePath(ctx, p)` 用它。
  shell、文件工具、`present_files`（146）的相对路径与包含校验都经它。
- `pkg/run/runner.go` 约 `:1503-1512` 在回合入口盖 `tool.WithProjectRoot(ctx, r.ProjectRoot)`（已有则不盖）。
- `pkg/assembly/rules_hook.go` `rulesWorkingDir(ctx)` 用 `tool.WorkingDir(ctx)` 解析 FOREBRAIN.md 与信任——**工作目录换成任务工作区后必须改用项目根**，否则 FOREBRAIN.md 与信任会按子目录判定而丢失。

### 3. 工具注册（`pkg/tool`）

- `RegisterDefaultTools`（`registry.go` 约 `:95-180`）：`memory.ReadPathEnabled(rt.Cfg)` 时把记忆目录并进 `rt.AdditionalReadRoots`；`requestPermissionsToolEnabled(rt)` 时注册 `request_permissions`；末尾 `registerSessionAndMemoryTools`。
- `registerSessionAndMemoryTools`（`session_tools.go` 约 `:21-80`）：计划工具（148 后：办公不注册 enter）、`intermediate_tool`、`session_todo`；`memory.DedicatedToolsEnabled(rt.Cfg)` 时注册 `memories_*`；末尾 `registerProductTools`（注册 `user_interaction`，`registry.go` 约 `:352-363`）。
- `AgentToolRuntime.ProjectType`（146 加的）。

### 4. 记忆包装（`pkg/run/runner.go` 约 `:1036-1037`）

```go
llmClient = wrapMemoryInstructionLLM(llmClient, r.workspaceRoot(), r.ProjectKey, r.AppCfg, r.SessionStore, r.ProjectMemoryOnly())
llmClient = wrapMemoryCitationLLM(llmClient, r.AppCfg, r.MemoryStore)
```

### 5. 网关

- 建会话：`handleProjectSessionsCreate`（`api_extra.go` 约 `:6971-7005`）读 `{title}` → `createSessionWithBirth(ctx, title, p, state.SessionSourceConversation)` → 201 + `res`（`createSessionResult`，含 `ID`、`Title`、`Project`）。
  `handleChatSessionCreate`（约 `:1355`）读 `{title, source, project_id, folder}`，也会建项目会话。
- websocket：`validateWSClientMessage`（`ws_protocol.go` 约 `:107-150`）对 `start_run` 要求"内容或附件"（`"message content or an attachment required for start_run"`）；
  `HandleChatWS` 主循环（`server.go`）：`switch_mode`（约 `:1468`）直接 `state.Switch`；`start_run` 段（约 `:1609-2000`）：
  `content == "" && len(attachments) == 0 && len(mentionImages) == 0` → `continue`（静默跳过）→ `s.Sessions.Ensure` → `parseSlashCommandWithOptions(...)` → `s.runnerForTurn` →
  自动压缩 → UserPromptSubmit 钩子 → `prepareWebTurnInput(ctx, sid, content, attachments, mentionImages)` → `s.RunRT.CreateRun(...)` → 回合。
  拒绝回合的帧形如 `wsServerMsg{Op: "turn_withdrawn", RequestID, SessionID, Error, Data: map[string]any{"code": code}}`（`SessionBusyCode` 的写法，约 `:1975-1985`）。
- `prepareWebTurnInput`（`run_control.go` 约 `:539-620`）：每个附件 `absPath, rec, err := s.Files.EnsureLocalFile(ctx, s.Sessions.AgentID(), fid)`，拼
  `"[Attachment "+named+"]\nSaved at: "+savedAt+"\nUse your file tools to open and process it as needed."`。
- 插话：`handleRunInput` / `handleRunQueuedInput`（`run_control.go` 约 `:63-140`）`q.Steer(...)` / `q.FollowUp(...)`；子代理视图 `handleSubagentInput` 等调 `run.SendToSubagent`。
- 定时：`startCronFire(ctx, fire turn.CronFire)`（`run_control.go` 约 `:964-1010`）`EnsureAt(fire.SessionID, …, birth)` → `bindSessionToProject` → `startDetachedTurn(detachedTurn{Prompt: fire.Job.Prompt, …})`。
- 约束：`pkg/gateway` 生产文件 31 个（上限）——**不新建 gateway 生产文件**；`pkg/state` 在 146 后 25 个；`pkg/turn` 23 个。不新增包级 import。

## Commands you will need

| 目的 | 命令 | 成功时 |
|------|------|--------|
| 编译 / vet | `go build ./... && go vet ./pkg/state ./pkg/turn ./pkg/run ./pkg/tool ./pkg/process ./pkg/assembly ./pkg/gateway` | exit 0 |
| 单测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/<pkg> -count=1` | ok |
| 架构 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |

## Scope

**In scope**：
- `pkg/state/schema.sql`、`schema_migrations.go`、`schema_migrations_test.go`、`pkg/state/task_store.go`（新）、`pkg/state/task_store_test.go`（新）
- `pkg/turn/batch.go`（新）、`pkg/turn/batch_test.go`（新）、`pkg/turn/testdata/batch_prompts.golden.json`（新）、`pkg/turn/model.go`（只加跑批原始输入字段）、`pkg/turn/submit.go`、`pkg/turn/service.go`、`pkg/turn/slash.go`（只加 `/continue` 的拒绝/路由，见 Step 6）
- `pkg/run/turn_input.go`、`pkg/run/turn_input_test.go`、`pkg/run/subagent.go`（`SendToSubagent` 与把任务 ctx 带给子代理）、`pkg/run/runner.go`（记忆包装）
- `pkg/tool/state.go`、`pkg/tool/registry.go`、`pkg/tool/session_tools.go` 及其测试
- `pkg/process/run_executor.go`、`pkg/process/run_executor_test.go`、`pkg/gateway/serve_run.go` 与 `pkg/tui/notify.go`（只在 `turn.New(` 处接 `WithBatchTasks`）
- `pkg/assembly/rules_hook.go`、`rules_hook_test.go`
- `pkg/gateway/ws_protocol.go`、`server.go`、`run_control.go`、`api_extra.go` 及对应测试
- `docs/integration/batch-tasks.md`（新）

**Out of scope**：沙箱与审批策略（`150`）；通道入站与结束通知（`151`）；TUI（`152`，本计划只提供 `/continue` 的引擎路由）；网页（`153`）。

## Steps

### Step 1：表与存储（`pkg/state`，预期迁移 v22，取 146 后当前 + 1）

1. `schema.sql` 加（带注释）：
   ```sql
   -- A batch project's business task. A task is run by one or more executions;
   -- every execution is its own conversation with its own task workspace
   -- (<project root>/tasks/<task id>/<seq>), and re-running a task adds an
   -- execution instead of reusing a conversation (owner 2026-10-26).
   CREATE TABLE fb_tasks (
     id TEXT PRIMARY KEY,
     agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
     project_id TEXT NOT NULL REFERENCES fb_projects(id) ON DELETE CASCADE,
     title TEXT NOT NULL DEFAULT '',
     input_text TEXT NOT NULL DEFAULT '',
     input_set INTEGER NOT NULL DEFAULT 0 CHECK (input_set IN (0, 1)),
     external_ref TEXT NOT NULL DEFAULT '',
     origin TEXT NOT NULL DEFAULT 'api' CHECK (origin IN ('api', 'web', 'cron', 'channel')),
     origin_ref TEXT NOT NULL DEFAULT '',
     created_at INTEGER NOT NULL,
     updated_at INTEGER NOT NULL
   ) STRICT;
   CREATE INDEX idx_fb_tasks_project_updated ON fb_tasks(project_id, updated_at DESC);
   CREATE INDEX idx_fb_tasks_external_ref ON fb_tasks(project_id, external_ref) WHERE external_ref <> '';

   -- One execution of a task: exactly one conversation and one task workspace.
   CREATE TABLE fb_task_executions (
     session_id TEXT PRIMARY KEY REFERENCES fb_sessions(id) ON DELETE CASCADE,
     task_id TEXT NOT NULL REFERENCES fb_tasks(id) ON DELETE CASCADE,
     seq INTEGER NOT NULL CHECK (seq > 0),
     workspace TEXT NOT NULL,
     status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'stopped', 'completed', 'failed')),
     error_code TEXT NOT NULL DEFAULT '',
     error_text TEXT NOT NULL DEFAULT '',
     started_at INTEGER,
     finished_at INTEGER,
     created_at INTEGER NOT NULL,
     UNIQUE (task_id, seq)
   ) STRICT;
   ```
   迁移基线预期 `migrateStateV21ToV22`：`CREATE TABLE IF NOT EXISTS` ×2 + `CREATE INDEX IF NOT EXISTS` ×2，文本与 schema.sql 逐字相同；`stateSchemaVersion = 22`（以 146 落地后的当前值 + 1 为准，若其它迁移先落地则顺延并在报告写明）。
2. `pkg/state/task_store.go`（新建）——`*ProjectStore` 上的方法（同库、同租户边界）：
   ```go
   type Task struct {
   	ID, AgentID, ProjectID, Title, InputText string
   	InputSet                                 bool
   	ExternalRef, Origin, OriginRef           string
   	CreatedAt, UpdatedAt                     int64
   }
   type TaskExecution struct {
   	SessionID, TaskID      string
   	Seq                    int
   	Workspace, Status      string
   	ErrorCode, ErrorText   string
   	StartedAt, FinishedAt  int64 // 0 = not yet
   	CreatedAt              int64
   }
   type TaskRow struct { Task; Latest TaskExecution; Executions int }

   const (
   	TaskStatusPending = "pending"; TaskStatusRunning = "running"; TaskStatusStopped = "stopped"
   	TaskStatusCompleted = "completed"; TaskStatusFailed = "failed"
   	TaskOriginAPI = "api"; TaskOriginWeb = "web"; TaskOriginCron = "cron"; TaskOriginChannel = "channel"
   )
   var ErrTaskNotFound = errors.New("task not found")
   var ErrNotBatchProject = errors.New("project is not a batch project")

   // TaskWorkspaceDir is where one execution runs: <root>/tasks/<task id>/<seq>.
   func TaskWorkspaceDir(projectRoot, taskID string, seq int) string

   func (s *ProjectStore) CreateTask(ctx context.Context, in Task) (Task, error)                 // project 必须属于本主代理且 project_type='batch'，否则 ErrNotBatchProject；ID "tsk-"+uuid（用 NewID("tsk")）
   func (s *ProjectStore) GetTask(ctx context.Context, id string) (Task, error)
   func (s *ProjectStore) AddTaskExecution(ctx context.Context, taskID, sessionID, projectRoot string) (TaskExecution, error) // 事务内 seq = MAX(seq)+1；workspace = TaskWorkspaceDir(...)
   func (s *ProjectStore) ExecutionForSession(ctx context.Context, sessionID string) (TaskExecution, Task, bool, error)
   func (s *ProjectStore) StartTaskExecution(ctx context.Context, sessionID, expectedStatus, inputText string, recordInput bool) (TaskExecution, error) // 单事务：校验当前状态，按需首次固定输入，再置 running；任一步失败全回滚
   func (s *ProjectStore) MarkExecutionEnded(ctx context.Context, sessionID, status, code, text string) error // finished_at=now；并 touch fb_tasks.updated_at
   func (s *ProjectStore) ListTasksPage(ctx context.Context, projectID string, opts ListTasksOptions, limit, offset int) ([]TaskRow, int, error)
   func (s *ProjectStore) ListTaskExecutionsPage(ctx context.Context, taskID string, limit, offset int) ([]TaskExecution, int, error)
   type ListTasksOptions struct { Search string; Status string } // Search 匹配 title / external_ref / id；Status 按最新一次执行的状态过滤
   ```
   分页规则照 `ListProjectsPage`（`projectPageWindow`、越界读最后一页、返回总数）。`ListTasksPage` 用一条 SQL 取每个任务的最新执行（`seq = (SELECT MAX(seq) …)`）与执行数，按 `updated_at DESC`。
   项目删除时 `fb_tasks` 随 `ON DELETE CASCADE` 走；**目录不动**（与项目删除"目录里的文件不动"一致）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → ok。

### Step 2：工作目录与任务上下文进 ctx（`pkg/tool/state.go`）

1. 仿 `WithProjectRoot` 加：
   ```go
   // WithTaskWorkspace records a batch execution's task workspace. WorkingDir
   // prefers it over the project root, so every tool of the turn runs there.
   func WithTaskWorkspace(ctx context.Context, dir string) context.Context
   func TaskWorkspaceFromContext(ctx context.Context) string
   // WithTaskContext records the task context paragraph a batch turn's
   // environment carries (task id, execution, workspace, input files).
   func WithTaskContext(ctx context.Context, text string) context.Context
   func TaskContextFromContext(ctx context.Context) string
   ```
2. `WorkingDir(ctx)`：最先 `if ws := TaskWorkspaceFromContext(ctx); ws != "" { 返回其 Abs+Clean }`，其余不变。`ProjectRootFromContext` **不变**（它仍是项目身份）。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → 无新增失败。

### Step 3：引擎准入与生命周期（`pkg/turn/batch.go`，新建）

```go
// BatchContinuePrompt is the turn a stopped execution continues with.
const BatchContinuePrompt = "Continue this task from where it stopped. Inspect existing outputs/, .work/ and prior tool results before redoing any step. Preserve finished work, resume the first unfinished checklist item, and do not ask for new input. Before finishing, verify every deliverable and present any final files not already presented; then give the overall conclusion and coverage counts."

// BatchTurnInput is the original task text before attachment/enrichment
// blocks. Every surface starting a batch turn sets TurnRequest.BatchInput.
type BatchTurnInput struct {
	Text           string
	HasAttachments bool
}

// BatchTaskStore is the project-store slice used by batch turns.
type BatchTaskStore interface {
	ExecutionForSession(ctx context.Context, sessionID string) (state.TaskExecution, state.Task, bool, error)
	StartTaskExecution(ctx context.Context, sessionID, expectedStatus, inputText string, recordInput bool) (state.TaskExecution, error)
	MarkExecutionEnded(ctx context.Context, sessionID, status, code, text string) error
}

// BatchRefusal is a batch turn the engine refused, with a stable code every
// surface (and every integrating system) can switch on.
type BatchRefusal struct{ Code, Message string }
func (e *BatchRefusal) Error() string

const (
	BatchCodeNoTask          = "batch_session_has_no_task"
	BatchCodeRunning         = "batch_execution_running"
	BatchCodeFinished        = "batch_execution_finished"
	BatchCodeTakesNoInput    = "batch_takes_no_input"
	BatchCodeInputFixed      = "batch_task_input_fixed"
	BatchCodeCommandRefused  = "batch_command_not_allowed"
	BatchCodeModeFixed       = "batch_mode_fixed"
	BatchCodeAdmissionRequired = "batch_admission_required"
)

type BatchTurnKind string
const ( BatchTurnFirst BatchTurnKind = "first"; BatchTurnRerun = "rerun"; BatchTurnContinue = "continue" )

type BatchAdmission struct {
	Kind      BatchTurnKind
	Input     string // the turn's model input
	Task      state.Task
	Execution state.TaskExecution
}

// AdmitBatchTurn is a READ-ONLY preview of the admission table. It never
// records task input and never changes execution status. Submit repeats it
// under the session lock before StartTaskExecution commits the transition.
func AdmitBatchTurn(ctx context.Context, store BatchTaskStore, sessionID, content string, hasAttachments bool) (BatchAdmission, error)
```
- 按"设计总览"的表实现；`content` 先 `TrimSpace`；`Message` 用英文一句话，界面按 `Code` 本地化。
- `ProjectStore.StartTaskExecution` 必须在**一个 SQLite 事务**里重读 execution/task：要求当前 status = `expectedStatus`（pending 或 stopped）；
  `recordInput=true` 时要求 `input_set=0` 并写原始 `input_text`；同事务把 execution 改为 running，受影响行必须为 1，否则回滚输入与状态。
  竞争/输入冲突以 state sentinel error 返回，turn 映射成 `batch_execution_running` / `batch_execution_finished` / `batch_task_input_fixed`。
  **不得**拆成先 `RecordTaskInput` 再 `MarkExecutionRunning` 两个可独立成功的调用。
- `turn.TurnRequest` 加 `BatchInput *BatchTurnInput \`json:"-"\``；每个跑批入口传原始文本与附件标志。已有执行行的请求没填时，Submit 返回 `batch_admission_required`，阻止未来入口绕过。

`Service` 接线（`pkg/turn/service.go` + `submit.go`）：
1. 新选项 `WithBatchTasks(store BatchTaskStore, artifacts BatchArtifactStore, onEnd func(context.Context, BatchExecutionEnd)) Option`，
   `BatchArtifactStore interface { ListArtifactsPage(ctx, sessionID string, limit, offset int) ([]state.Artifact, int, error); AddArtifact(ctx, state.Artifact) (state.Artifact, error) }`（`*state.SessionStore` 满足）；
   `BatchExecutionEnd struct { SessionID, RunID string; Task state.Task; Execution state.TaskExecution; Status, ErrorCode, ErrorText string; Artifacts []state.Artifact }`（`151` 用它发通知）。
2. `Submit` 在拿到锁之后、`s.runner.Run` 之前：已有执行行时必须存在 `req.BatchInput`，重新 `ExecutionForSession` + `AdmitBatchTurn`，再调用
   `StartTaskExecution(sid, adm.Execution.Status, req.BatchInput.Text, adm.Kind==BatchTurnFirst && !adm.Task.InputSet)`；
   成功才 `req.Unattended = true`。若原始 `UserText` 为空且无附件，才把 `adm.Input` 填进 `UserText` 与 `RawInput`（新执行沿用任务文本、停止续跑）；
   首回合带附件时保留 `prepareWebTurnInput` 已生成的附件块与 Parts，不能覆盖。非跑批会话完全走旧路径。
3. `Submit` 各结束分支（cancelled / failed / completed；waiting 按 failed 处理并记 `code="approval_required"`——跑批永不应停在审批，出现即说明 150 未生效）之后统一调用私有 `s.endBatchExecution(context.WithoutCancel(ctx), req, runID, status, err)`：
   - status：cancelled → `stopped`；err → `failed`，`code` 取 `llm.Explain(err)` 的 `Code`（取不到为 `"error"`），`text` = `err.Error()` 截断 2000 字；completed → `completed`；
   - `MarkExecutionEnded(...)`；随后重新 `ExecutionForSession` 取得带 `finished_at` 的最终执行行供 `BatchExecutionEnd.Execution` 使用，不能把 running/零时间的旧快照交给 151 计算耗时；
   - **兜底生成物**：该会话 `ListArtifactsPage(sid, 1, 0)` 总数为 0 时，遍历 `<exec.Workspace>/outputs/`（跳过以 `.` 开头的目录和文件，最多 200 个文件，不跟随软链），逐个 `tool.ClassifyArtifact` + sha256 → `AddArtifact(origin: state.ArtifactOriginFallback, RunID: runID, RelPath: 相对**工作区**的路径)`；
   - `onEnd` 非 nil 时调用（带上本次执行的全部生成物）。
   注意：`stopped` 的执行之后还可能续跑——续跑结束会再走一次 end，兜底只在"该会话一个生成物都没有"时发生，所以不会重复登记。
   `WithoutCancel` 是必需的：取消后仍要落 stopped、收集生成物并让 151 通知；不能让 canceled ctx 吃掉收尾。
4. 跑批的 `Submit` 不需要改 `autoContinue`：`req.Unattended = true` 已让 `planFor` 不排续跑。
5. **每个跑批入口都同时做“创建 run 前的只读预检 + Submit 的 BatchInput”**：
   - WebSocket `start_run`：在空消息跳过、斜杠分派、附件准备与 `RunRT.CreateRun` 前预检；用 `adm.Input` 继续准备，BatchInput 保存**预检前**原文与 `len(attachments)+len(mentionImages)>0`。
   - TUI `/continue`：预检用于显示拒绝，准后把 `BatchContinuePrompt` 交给普通 dispatch，BatchInput 仍记原始空文本。
   - `startDetachedTurn`（定时及后台路径）：在 `RunRT.CreateRun` 前预检，`detachedTurn` 携带 BatchInput 随 `runDetachedTurn` 的 Submit；cron 不提前 `RecordTaskInput`。
   - `submitChannelTurn`：在持久化用户消息前预检并填 BatchInput；151 创建通道任务后不提前 `RecordTaskInput`，由首次 Submit 原子固定输入。
   这样附件复制/图片处理失败发生在原子启动前，不会把 pending 执行的输入锁死；两个竞争入口至多一个能提交 running。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → 无新增失败。

### Step 4：输入队列可关闭；子代理视图拒绝

1. `pkg/run/turn_input.go`：`InputQueue` 加 `closed bool`；`func (q *InputQueue) Close()`（置位并触发 hook）、`func (q *InputQueue) Closed() bool`；
   `Steer`、`FollowUp` 在加锁后 `if q.closed { 解锁; return false }`（**不**进 rejected 车道——跑批没有"之后再送"）。
2. `pkg/run/subagent.go` `SendToSubagent` 开头：`if r != nil && r.ProjectType() == state.ProjectTypeBatch { return "", ErrBatchTakesNoInput }`，`ErrBatchTakesNoInput = errors.New("batch tasks take no input")` 新增导出变量。
3. 子代理继承任务工作区：`subagent.go` 里构造子代理运行 ctx 的几处（`grep -n "tool.WithProjectRoot(" pkg/run/subagent.go`，约 `:1193`、`:1435`、`:1917`）——在每处之后加
   `if ws := tool.TaskWorkspaceFromContext(<该处可拿到的派发方 ctx>); ws != "" { <子 ctx> = tool.WithTaskWorkspace(<子 ctx>, ws) }`（任务上下文同理）。
   **若某处拿不到派发方的 ctx**（例如后台派发用 `context.Background()` 起新 ctx），改为从 `fac.Owner` 不可得——此时 STOP 3 报告该处，不要猜。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → 无新增失败。

### Step 5：跑批 runner 的工具表与记忆

1. `pkg/tool/registry.go` / `session_tools.go`：`batch := rt != nil && rt.ProjectType == state.ProjectTypeBatch`；batch 时：不并入记忆可读根、不注册 `request_permissions`、不注册 `enter_plan_mode`/`exit_plan_mode`、不注册 `memories_*`、`registerProductTools` 不注册 `user_interaction`。
   （`intermediate_tool`、`session_todo`、`present_files`、文件 / shell / web / lsp / skill / subagent 工具照常。）
2. `pkg/run/runner.go` 约 `:1036-1037`：`if r.ProjectType() != state.ProjectTypeBatch { 两个记忆包装照旧 }`。

**Verify**：`go build ./...` → exit 0。

### Step 6：执行器盖上任务 ctx、关队列；规则钩子带任务上下文

1. `pkg/process/run_executor.go`：`runner` 解析成功后，若 `runner.ProjectType() == state.ProjectTypeBatch`：
   ```go
   exec, task, ok, err := state.NewProjectStore(x.env.SQL, x.env.Deps.AgentName).ExecutionForSession(ctx, sid)
   // err → return nil, err；!ok → return nil, &turn.BatchRefusal{Code: turn.BatchCodeNoTask, ...}
   if err := os.MkdirAll(filepath.Join(exec.Workspace, "outputs"), 0o755); ... // inputs/、.work/ 同样确保存在
   agBase = tool.WithTaskWorkspace(agBase, exec.Workspace)
   agBase = tool.WithTaskContext(agBase, turn.BatchTaskContext(task, exec))
   x.env.Control.SessionQueue(sid).Close()
   ```
   `turn.BatchTaskContext(task, exec)`（放在 `pkg/turn/batch.go`，执行器只调用这一处）读取 `<workspace>/inputs/` 下不跟随软链的普通文件，路径转 `/`、按字节排序，返回**不含外层标签**的如下文本；`rules_hook` 只负责套 `<forebrain_task_context>`：
   ```
   task_id: <XML-escaped task id>
   execution: <seq>
   task_workspace: <XML-escaped absolute workspace>
   input_files:
   - <XML-escaped inputs/relative/path>
   input_files_truncated: <only when more than 50: "and N more files; inspect inputs/ for the complete set">
   deliverables: outputs/
   scratch: .work/
   ```
   最多列 50 条，超出的 `N = 总数 - 50` 必须显式写出；没有文件时 `input_files:` 下一行是 `- none`，不出现 `input_files_truncated`。
   `deliverables`、`scratch` 两行恒在最后。所有动态值先 `strings.ToValidUTF8(v, "\uFFFD")`，再用 `encoding/xml.EscapeText` 写入 buffer（包含 `& < > " '` 与控制字符），防止文件名/路径伪造标签或追加指令；纯文本里的 `- ` 列表不换行拆分用户文件名。
   `BatchTaskContext` 不返回 error：目录暂时不存在按无文件处理；读取/遍历错误则以一行 `input_files_error: <escaped error>` 代替清单，不静默声称没有输入。
   任务标题不要放入上下文，减少未必要的用户文本注入面；任务原始指令只在用户消息中出现一次。该动态文本走 addendum/meta，**不改写** 147 的静态 `BatchTaskPrompt`。
   `pkg/turn/testdata/batch_prompts.golden.json` 增加 `BatchTaskContextEmpty`、`BatchTaskContextSortedAndEscaped`、`BatchTaskContextTruncated` 三组完整期望字符串，分别覆盖空 inputs、排序与 `& < > " '`、51 个文件的 `and 1 more file...`；`TestBatchPromptsGolden` 同时逐字比较 `BatchContinuePrompt` 与这些完整渲染结果。
2. `pkg/assembly/rules_hook.go`：
   - `rulesWorkingDir(ctx)` 改为：`ProjectRootFromContext(ctx)` 非空就用它，否则 `WorkingDir(ctx)`（非跑批两者相同，行为不变）。
   - `PreHook.run` 先取 `tc := tool.TaskContextFromContext(ctx)`。现有 `t == ""` / `strings.HasPrefix(t,"/")` 的早退只在 `tc == ""` 时保留：**只有附件、没有文字的首个跑批回合也必须注入任务上下文**；coding/office 的空消息和斜杠路径不变。
   - 在算出 `out.SystemAddendum`（规则可能为空）之后，`if tc != "" { out.SystemAddendum = strings.TrimSpace(out.SystemAddendum + "\n\n<forebrain_task_context>\n" + tc + "\n</forebrain_task_context>") }`。
     注意原函数在 rules 为空时提前 `return out, nil`——改成"规则为空时不写 `<forebrain_rules>` 段，但仍追加任务上下文"。
3. `turn.Service` 的构造函数是 `turn.New(opts ...Option)`（`pkg/turn/service.go` 约 `:199`）。给**会跑回合**的两个构造点接上 `turn.WithBatchTasks(state.NewProjectStore(<SQL>, <agent>), <session store>, nil)`：网关 `pkg/gateway/serve_run.go` 约 `:66` 的 `core := turn.New(`，终端 `pkg/tui/notify.go` 约 `:1608` 的 `s.Core = turn.New(`（`onEnd` 在 `151` 接网关通知；先传 nil）。`notify.go:1603` 与 `api_extra.go:1478/1517` 只建会话/改名，不接。
   拿不到 SQL 的构造点传 nil 即不启用。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/assembly -count=1` → 无新增失败。

### Step 7：网关——建任务 / 新执行、websocket 准入、附件进 inputs、拒绝插话与切模式、定时触发

1. **建会话**（`handleProjectSessionsCreate`）：body 扩为 `{title, task_id, external_ref, origin}`（`origin` 只接受 `""`/`"api"`/`"web"`，默认 `api`）。项目是 batch 时：
   - 无 `task_id`：`CreateTask(Task{ProjectID, Title: title, ExternalRef, Origin})`；有 `task_id`：`GetTask` 且属于本项目（否则 404 `task_not_found`）。
   - 新任务按原 `createSessionWithBirth` 流程建会话（空 title 让首条消息命名）→ `AddTaskExecution`；新执行先用任务标题建会话，`AddTaskExecution` 返回真实 `exec.Seq` 后再调用现有 `s.Sessions.SetTitle(ctx, sid, fmt.Sprintf("%s #%d", task.Title, exec.Seq))`，避免在还不知道 seq 时写标题。
     随后 `os.MkdirAll` 出 `inputs/`、`outputs/`、`.work/` → `exec.Seq > 1` 时把**上一次执行**（seq-1）的 `inputs/` 递归复制过来（只复制普通文件，不跟随软链；失败 → 500，并删除刚建的执行目录；执行行/会话保留为 pending 供排错，不得伪造 running/completed）。
   - 201 响应在原有字段外加 `"task": {"id","title","external_ref","origin"}`、`"execution": {"seq","status","workspace","workspace_rel"}`（`workspace_rel` = 相对项目根，`/` 分隔）。
   - 非 batch 项目带 `task_id` → 400 `task_id_requires_batch_project`。
   `handleChatSessionCreate` 遇到 batch 项目 → 409 `{"error":"batch_project_uses_tasks"}`（新任务只能经项目会话接口建，保证每个跑批会话都有执行行）。
2. **websocket 准入**：
   - `validateWSClientMessage` 的 `start_run` 分支：`m.SessionID` 非空时**允许空内容**（运行时再判定）；`SessionID` 空时保持原错误。
   - 主循环 `start_run`：在"空内容即跳过"那行**之前**，若 `sid := m.SessionID` 非空且 `s.projectStore().SessionProjectType(ctx, sid) == batch`：
     ① 内容以 `/` 开头且命令名是内置命令（`turn.IsBuiltinName`）→ 拒绝 `BatchCodeCommandRefused`（技能命令放行）；
     ② `originalContent := content`；`hasInputs := len(attachments)+len(mentionImages) > 0`；
     `adm, err := turn.AdmitBatchTurn(ctx, s.projectStore(), sid, content, hasInputs)`，拒绝时回 `turn_withdrawn`，`Data: {"code": refusal.Code}`、`Error: refusal.Message`，并写进 request 缓存（照 `turn_refused` 的写法）；
     ③ 通过则保存 `batchInput := &turn.BatchTurnInput{Text: originalContent, HasAttachments: hasInputs}`，后续 `turn.TurnRequest` 带上它；`content = adm.Input`，并在本请求内跳过 `parseSlashCommandWithOptions`（跑批不跑内置斜杠命令的本地分派；技能命令按 `m.Message.SkillName` 或内容里的 `/技能名` 交给引擎的既有激活路径——先读懂该路径，若技能激活**依赖**斜杠分派，则只对"以 `/` 开头的技能命令"调用分派、其余跳过，并在报告里写明）。
   - `prepareWebTurnInput`：会话有任务工作区时（`ExecutionForSession` ok），每个附件**复制**到 `<workspace>/inputs/<原文件名>`（重名加 ` (2)` 等后缀），附件说明块里 `Saved at: inputs/<文件名>`。
   - `switch_mode`：batch 会话 → 回 `run_error`，`Data: {"code": "batch_mode_fixed"}`，不改模式。
3. **插话**：`handleRunInput` / `handleRunQueuedInput` 在 `q.Steer`/`q.FollowUp` 之前，若会话是 batch → 409 `{"error":"batch_takes_no_input"}`（队列也已关闭，双保险）；子代理输入三个接口遇到 `run.ErrBatchTakesNoInput` → 409 同码。
4. **定时触发**（`startCronFire`）：项目是 batch 时，在 `EnsureAt` + `bindSessionToProject` 之后：`CreateTask(Task{ProjectID, Title: fire.Title, Origin: cron, OriginRef: fire.Job.ID})` →
   `AddTaskExecution(task.ID, fire.SessionID, p.Root)` → 建三个子目录；**不提前 RecordTaskInput**。
   `startDetachedTurn` 在 `RunRT.CreateRun` 前按 Step 3 预检，并让 `runDetachedTurn` 的 Submit 带 `BatchInput{Text: fire.Job.Prompt, HasAttachments:false}`；首次 Submit 原子固定任务输入并启动。
5. **任务读接口**（路由加在 `api_extra.go` 的 `projects` 组与新组 `tasks := api.Group("/v1/tasks")`；处理函数写在 `api_extra.go` 末尾）：
   - `GET /api/v1/projects/:id/tasks?page=&limit=&q=&status=` → `{"items":[{task 字段…, "latest": {seq, session_id, status, started_at, finished_at, error_code}, "executions": N, "artifacts": M}], "total": T}`（`artifacts` = 最新执行的生成物数）。
   - `GET /api/v1/tasks/:task_id?page=&limit=` → `{"task": {...}, "executions": {"items":[{seq, session_id, status, workspace_rel, started_at, finished_at, error_code, error_text, artifacts}], "total": N}}`。
   - 都按本主代理过滤；别人的任务 404。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → 无新增失败。

### Step 8：对接文档 `docs/integration/batch-tasks.md`（新建，中文）

面向业务系统开发者，必须覆盖（每节给请求 / 响应示例 JSON）：
1. 概念：项目（类型 batch）、任务、执行（每次执行 = 一个会话 + `tasks/<任务ID>/<序号>/` 工作区）、状态机（上表）、生成物。
2. 鉴权：网关 token（`Authorization: Bearer <token>`；websocket 用同一 token——写清楚网页登录用的是同一机制，引用 `pkg/gateway/controlplane.go` 的 `authorizeControlPlane` 支持的方式；**不要**在文档里写任何真实 token）。
3. 新任务：① 可选上传附件 `POST /api/files`（写出字段名，读 `handleFiles` 确认）→ 文件 id；② `POST /api/v1/projects/{project_id}/sessions` `{"title","external_ref"}` → `session_id`、`task.id`、`execution`；
   ③ 连 `/ws/chat`，`bind_session`，`start_run` `{"op":"start_run","request_id","session_id","message":{"content":"…","attachments":["<file id>"]},"protocol_version":"2026-04-18"}`。
4. 流式事件：列出业务系统会收到的 op（`session_bound`、`run_started`、`assistant_delta`/`reasoning_delta`、`tool_call_started`/`tool_call_completed`、`run_completed`/`run_error`/`run_cancelled`、`turn_withdrawn`），
   以 `server.go` / `wsevents.go` 的实际输出为准（读代码写，不要凭印象）；运行结束帧的 `artifacts` 数组字段（146 的 `event.Artifact`）与 `run_error` 的 `detail.code`（如 `rate_limit_quota`、`reset_at`）。
5. 生成物：`GET /api/chat/sessions/{sid}/artifacts`、`…/content?disposition=attachment`、`…/preview.pdf`、`…/entries`。
6. 停止：`cancel_run`；续跑：对同一 `session_id` 发**空内容** `start_run`；重新执行：`POST …/sessions` 带 `task_id` → 新 `session_id` → 空内容 `start_run`。
7. 查询：任务列表与任务详情接口；错误码表（本计划所有 `batch_*` 码 + 146 的 `artifact_*` 码 + `task_not_found` 等）。
8. 并发：一条 websocket 连接一次跑一个回合；并行跑多条任务请开多条连接（读主循环确认后写）。

### Step 9：测试（见 Test plan）

## Test plan

1. `pkg/state/task_store_test.go`：建任务只允许 batch 项目；`AddTaskExecution` 的 seq 连续且并发安全（两个 goroutine 同时加 → 1、2）；
   `StartTaskExecution` 在事务里首次写输入+running、重复 running 拒绝、pending→stopped 后续跑、输入冲突回滚；并发两个 starter 只有一个成功且 `input_set` 与 running 一致；`MarkExecutionEnded` 的时间与状态；
   `ListTasksPage` 的最新执行 / 执行数 / 状态过滤 / 搜索 external_ref / 分页总数；删项目级联删任务与执行。迁移测试 `TestStateV21UpgradesToV22AddsTasks`（版本顺延时同步改名）。
2. `pkg/turn/batch_test.go` + `pkg/turn/testdata/batch_prompts.golden.json`：`TestBatchPromptsGolden` 逐字验证续跑常量及三组完整任务上下文（含 51 个文件、XML 特殊字符与零文件）；`AdmitBatchTurn` 按设计表逐行（7 行）断言 `Kind/Input` 或 `Code`，并断言预检不写输入/状态；
   `Submit` 的跑批路径：首回合只在启动时记录原始输入、成功 → completed、取消（真实 canceled ctx）→ stopped 且收尾与 onEnd 仍执行、缺 BatchInput → `batch_admission_required`；
   用量上限错误（造一个 `llm.Explain` 能识别为 `rate_limit_quota` 的错误，grep 现有 autoContinue 测试的造法）→ failed + code 且**没有**排续跑；
   兜底：工作区 `outputs/a.docx`、`outputs/.work/x.py`、`outputs/.hidden` → 只登记 `a.docx`、origin fallback；已有生成物时不兜底；`onEnd` 收到正确的 status 与生成物。
3. `pkg/run/turn_input_test.go`：关闭后 `Steer`/`FollowUp` 返回 false 且不进任何车道。
4. `pkg/tool`：`WorkingDir` 优先任务工作区；batch 的 `RegisterDefaultTools` 工具名单不含 `user_interaction`、`enter_plan_mode`、`exit_plan_mode`、`request_permissions`、`memories_*`，含 `present_files`。
5. `pkg/assembly/rules_hook_test.go`：带任务上下文时 addendum 含 `<forebrain_task_context>`；**空文本 + 附件型任务**也注入；coding/office 的空文本早退不变；任务工作区是项目根子目录时 FOREBRAIN.md 仍按项目根加载（受信任）。
6. `pkg/process/run_executor_test.go`：batch 会话的回合 ctx 里 `WorkingDir` = 执行工作区、`outputs/` 被建出、会话队列已关闭。
7. `pkg/gateway`（照 `TestProjectSessionCreateBindsProjectAndCwd` 与现有 websocket 测试的写法）：
   - 新任务建会话返回 `task`/`execution`、目录存在；带 `task_id` 建第二次执行 → seq 2、`inputs/` 被复制；非 batch 带 `task_id` → 400；`/chat/sessions` 建 batch → 409。
   - websocket：首回合带内容 + 附件 → 附件出现在 `inputs/`；跑完再发 → `turn_withdrawn` + `batch_execution_finished`；新执行发带内容 → `batch_task_input_fixed`；
     取消后发空内容 → 续跑且回合输入是 `BatchContinuePrompt`；发 `/plan` → `batch_command_not_allowed`；`switch_mode` → `batch_mode_fixed`；运行中 `POST /runs/:id/input` → 409 `batch_takes_no_input`。
   - 定时触发在 batch 项目建出 origin=cron 的任务与执行；`submitChannelTurn` 与 detached/cron 两路都实际带 BatchInput，首次输入固定；
     附件准备失败后同一 pending 执行可用原输入重试，不出现 `batch_task_input_fixed` 或空输入。
   - 任务列表 / 详情接口分页与 404。
8. **真机验收**（glm-5.3）：先查看 `~/.forebrain/.env` 与 `~/.forebrain/forebrain.yaml`，按 owner 后续裁定取事故同类 provider/model 的凭据；只注入隔离验收进程环境，不写秘密到命令行/临时配置/日志/报告，不改用户文件，不使用默认 deepseek-chat。
   只有两处都确认无 glm-5.3 凭据才记 NOT RUN 并写明已检查位置。起隔离网关，建 batch 项目（`trust:true`），用一个最小 websocket 客户端脚本（Python `websockets` 或 Node `ws`，脚本放会话 scratch 目录，不进仓库）
   按对接文档第 3 节建任务、上传一个 CSV、要求"统计每列缺失值并输出 xlsx 报告"→ 收到 `run_completed` 帧且 `artifacts` 含 xlsx、文件在 `tasks/<id>/1/outputs/`；
   再按第 6 节重新执行 → 新会话、`tasks/<id>/2/inputs/` 有同一 CSV。（沙箱隔离在 150 验收，这里不测。）

## Done criteria

- [ ] `go build ./...`、`go vet`（上列包）exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/turn ./pkg/run ./pkg/tool ./pkg/process ./pkg/assembly ./pkg/gateway ./pkg/architecture -count=1` 无新增失败；Test plan 1–7 的新测试都存在且通过
- [ ] `ls pkg/gateway/*.go | grep -v _test.go | wc -l` 仍是 31
- [ ] `docs/integration/batch-tasks.md` 存在，8 节齐全，`grep -n "Bearer [A-Za-z0-9]" docs/integration/batch-tasks.md` 只命中占位符 `<token>` 的示例
- [ ] 真机验收 PASS 或 NOT RUN（写明原因）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

1. 前置不满足或"现状"对不上。
2. `Controller` 的运行态输入队列不是 `SessionQueue(sid)` 的那一个（关闭队列拦不住插话）。
3. 子代理派发处拿不到派发方 ctx，任务工作区无法传给子代理（Step 4.3）。
4. 技能命令的激活与斜杠分派耦合到无法"只跳过内置命令"的程度（Step 7.2③）。
5. 上述两个 `turn.New(` 构造点拿不到 SQL / 主代理名以构造 `*state.ProjectStore`——列出构造点停下。
6. 把工作目录切到任务工作区后，任何**非跑批**的既有测试变红（说明 `WorkingDir` 的改动影响了其它类型）。

## Maintenance notes

- **会话类型 + 执行行**是跑批语义的两把钥匙：入口先看会话类型是不是 batch，再看执行行；没有执行行的 batch 会话一律拒绝（`batch_session_has_no_task`），所以任何新的"建 batch 会话"路径都必须同时建任务与执行。
- `AdmitBatchTurn` 的表是对外协议的一部分（错误码写进了对接文档）；改动要同步文档。
- 任务工作区只建不删：删除任务 / 项目都不动磁盘（与项目删除一致）。若以后要"清理旧执行目录"，单独立计划并问 owner。
- 本计划让跑批会话在任务工作区里**执行**，但此时 shell 仍可越界——`150` 落地前不要把跑批项目交给外部业务系统用。
