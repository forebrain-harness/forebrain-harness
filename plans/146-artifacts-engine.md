# Plan 146：生成物（交付物）引擎——`present_files` 工具、`fb_artifacts` 表、结构化数据随运行结束下发、下载 / 预览接口（含 LibreOffice 把 pptx 转 PDF）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`plans/README.md` 里 `145` 的状态是 DONE，且 `grep -n "func (r \*Runner) ProjectType" pkg/run/runner.go` 有 1 条、
> `grep -n "ProjectTypeOffice" pkg/state/project_store.go` 有输出。
>
> **Drift check（先跑）**：`git diff --stat d9d0b4d -- pkg/state/schema.sql pkg/state/schema_migrations.go pkg/tool/registry.go pkg/tool/format.go pkg/tool/header_parts.go pkg/tool/display_header.go pkg/tool/permissions.go pkg/run/runner.go pkg/run/subagent.go pkg/safety/engine.go pkg/event/run_events.go pkg/gateway/server.go pkg/gateway/wsevents.go pkg/gateway/approval.go pkg/gateway/session_files.go pkg/gateway/api_extra.go pkg/tui/notify.go pkg/tui/turn_lifecycle.go`
> 有输出是正常的（当前工作区含有 145 及其它任务的未提交改动）。按下文"现状"里的**函数名**逐段核对；含义变了即 STOP 1。行号是近似值。
>
> **基线（Step 1 之前跑一次，记下已红用例）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tool ./pkg/run ./pkg/gateway ./pkg/tui ./pkg/turn ./pkg/safety -count=1`（超时 900 s）；
> `cd frontend && pnpm vitest run src/locales/toolHeader.test.ts`

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（新表 + 新工具 + 新文件下发接口；文件下发是安全敏感面）
- **Depends on**: `plans/145-project-types-foundation.md`
- **Category**: direction
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区

## owner 裁定（2026-10-26，本计划相关部分）

1. 办公创作类、后台跑批类项目的 web 对话里必须显示**生成物的下载和预览卡片**；生成物类型包括但不限于 docx / pptx / xlsx / pdf、图片、视频、音频、
   html、markdown、压缩包等。**必须输出结构化的生成物数据**，方便其他业务系统集成对接。创意编程类保持现状（**不**注册本工具）。
2. 生成物由**模型显式提交**：新增一个提交交付物的工具（对标 claude.ai 的 `present_files`），只有提交的文件才是生成物；
   中间脚本、临时文件不算。（跑批任务结束时一个都没提交的兜底收集在 `149`；"只要计划"把计划文件登记为生成物在 `148`。）
3. 预览：docx、xlsx 在**浏览器前端渲染**（`153`）；**pptx 用 LibreOffice 转 PDF** 预览（本计划做服务端转换；主机没装 LibreOffice 时 pptx 只给下载并说明）；
   图片 / 音视频 / PDF / HTML / Markdown / 文本用浏览器原生能力；HTML 一律沙箱。压缩包给文件列表；psd / cad 等只给下载。
4. 跑批的业务系统接入方式 = 直接用 web 端的 `/ws/chat` websocket 协议（owner 原话："业务系统跟 gateway 建立 websocket 连接发起对话接收流式输出"）。
   所以结构化生成物要**出现在 websocket 的运行结束帧里**，同时提供 REST 读取 / 下载接口（同一个网关 token 鉴权）。

## Why this matters

办公 / 跑批会话的最终产物是文件（报告、表格、幻灯片、审核意见书）。现在模型生成了文件，界面上只有一张 shell 或 write_file 卡，
用户要自己去目录里找；业务系统更是拿不到"这次任务交付了哪些文件"的结构化清单。本计划给出唯一的"交付"动作和唯一的数据来源（`fb_artifacts`），
web 卡片（`153`）、跑批兜底（`149`）、通道通知（`151`）、对接文档（`149`）都只读它。

## Current state

### 1. 工具注册（`pkg/tool/registry.go`）

- `type AgentToolRuntime struct`（约 `:288-308`）字段：`Home, WorkspaceRoot, AdditionalReadRoots, ProjectKey, Cfg, YOLO, Sess *state.SessionStore, Actions, RunRT, PolicyRunID, PermissionSnapshot, PermissionSnapshotForSession, ApplyPermissionUpdate, CodeIntel, CodeIntelTool`。
- `RegisterDefaultTools(a ToolAdder, st *State, rt *AgentToolRuntime)`（约 `:95-180`）：逐个 `NewXxxTool` → `registerMeta(t, "<一句用途>", "<category>", event.ToolMeta{...})` → 最后 `st.Register(a, t)`，
  末尾 `return registerSessionAndMemoryTools(a, st, rt)`。范例（web_fetch）：
  ```go
  t16, err := NewWebFetchTool(st, rt)
  if err != nil {
  	return err
  }
  t16.SetContainsExternalContext(true)
  registerMeta(t16, "Use when you need the contents of a specific public URL (HTTP(S), SSRF-protected)", "web", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
  ...
  _ = st.Register(a, t16)
  ```
- 工具构造用 `llm.NewTool(name, description, func(ctx context.Context, in *XxxInput) (string, error))`，输入结构体用 `json` + `jsonschema` tag 描述（范例 `enterPlanModeInput`，`pkg/tool/session_tools.go` 约 `:305`）。
- `pkg/run/runner.go` `Load` 里（约 `:1265-1286`）构造 `rt := &tool.AgentToolRuntime{Home: r.Home, WorkspaceRoot: r.workspaceRoot(), ProjectKey: r.ProjectKey, Cfg: r.AppCfg, YOLO: r.yoloEnabled(), Sess: r.SessionStore, ...}` 后 `_ = tool.RegisterDefaultTools(a, r.tools, rt)`。

### 2. 工具运行时可用的上下文（`pkg/tool/state.go`）

- `RunIDFromContext(ctx)`（约 `:263`）、`ConversationSessionIDFromContext(ctx)`（约 `:282`，用户可见的对话，不是子代理 worker 会话）、
  `ToolUseIDFromContext(ctx)`（约 `:703`）、`IsSubagentRunFromContext(ctx)`（约 `:509`）。
- `WorkingDir(ctx)`（约 `:648`）= 会话项目根；`ResolvePath(ctx, p)`（约 `:672`）把相对路径接到项目根；`ResolveWithinRoots(p, roots)`（约 `:2806`）做包含校验（越界返回 `ErrPathNotAllowed`）。
- `GuardTool(ctx, toolName)`（`pkg/tool/permissions.go` 约 `:858`）开头有"结构性规则"：
  ```go
  if IsSubagentRunFromContext(ctx) && isSubagentManagementToolName(name) {
  	return fmt.Errorf("subagent tools are only available to the primary agent")
  }
  ```
- 子代理看到的工具表在 `pkg/run/subagent.go` `dropSubagentManagementTools`（约 `:458-470`）里先剔除 `subagent_*`。

### 3. 权限：内部安全工具免审批（`pkg/safety/engine.go` 约 `:447-458`）

```go
var safeInternalTools = map[string]struct{}{
	"intermediate_tool": {},
	"session_todo":      {},
	"subagent_run":      {},
	...
}
```
不在这里、也不是只读白名单的工具默认 `default_ask`——新工具不加进来，办公项目（默认预设）每次交付都会弹审批。

### 4. 卡片文字（两端共用，引擎定词）

- `pkg/tool/format.go` `FormatToolStepResult`（约 `:114-200`）按工具名分派正文格式化；`evt.Output` 是工具输出 JSON 解码后的 map，`evt.Input` 是入参 map。
  范例 `formatIntermediateToolStep`（约 `:1954`）。owner 规则：**卡片里出现原始 JSON 即 bug**，新工具必须有自己的正文格式化。
- `pkg/tool/header_parts.go` 按工具名给卡头动作词（范例 `case lower == "intermediate_tool":`，约 `:327`），
  `HeaderActions()`（约 `:413-460`）是**手写**的全部动作词清单，`header_parts_test.go:156` 拿它与 `testdata/header_actions.json` 比对，
  失败提示："更新 testdata/header_actions.json（UPDATE_HEADER_ACTIONS=1）并同步 frontend 的中文表"。
- `pkg/tool/display_header.go` `FailedActionPhrase`（约 `:216-252`）给 "Failed to <phrase>"。
- 网页中文表在 `ai-elements-vue/packages/elements/src/tool/headerWords.ts`（`HEADER_ACTION_ZH`，如 `'Notes': '笔记'`、`'Failed to save notes': '保存笔记失败'`），
  `frontend/src/locales/toolHeader.test.ts` 导入同一个 golden，**每个英文动作词都必须有不含 ASCII 字母的中文**，否则 vitest 红。

### 5. 运行结束载荷（`pkg/event/run_events.go` 约 `:166-211`）

```go
type RunPlanFacts struct {
	PlanDone   int    `json:"plan_done,omitempty"`
	PlanTotal  int    `json:"plan_total,omitempty"`
	PlanActive string `json:"plan_active,omitempty"`
}
type TurnCompletedPayload struct {
	Text      string `json:"text,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
	RunPlanFacts
}
type TurnCancelledPayload struct {
	Message string `json:"message,omitempty"`
	RunPlanFacts
}
type TurnErrorPayload struct {
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
	RunPlanFacts
	...
}
```

`RunPlanFacts` 在**五个**地方被填，本计划的 `RunArtifacts` 照抄同样五处：
- 网关 websocket：`pkg/gateway/server.go` `writeMsg`（约 `:1051-1057`）：`if wsRunEndOps[m.Op] && strings.TrimSpace(m.RunID) != "" { m.RunPlanFacts = s.runPlanFacts(context.Background(), m.RunID) }`；
  `wsServerMsg`（约 `:853-866`）内嵌 `event.RunPlanFacts`；`pkg/gateway/wsevents.go` `canonicalRunEventsFromWS`（约 `:60-100`）把 `m.RunPlanFacts` 抄进三种 payload；
  `wsRunEndOps`（约 `:800-807`）= `run_completed / run_cancelled / run_error`。
- 网关后台运行：`pkg/gateway/approval.go` `publishGatewayRunEvent`（约 `:140-160`）`switch ending := payload.(type)` 三个分支各填一次。
- 终端：`pkg/tui/notify.go` `publishTUIRunEvent`（约 `:1195-1215`）两个分支、`persistRunTurnError`（约 `:1220-1225`）；`pkg/tui/turn_lifecycle.go` `turnCompletedEvent`（约 `:20-27`）。
  它们都调 `s.runPlanFacts(ctx, runID)` → `turn.LastPlanProgressOfRun(ctx, s.runSvc(), runID).Facts()`（`pkg/turn/events.go` 约 `:66`）。
  终端的会话库访问器是 `s.sessStore()`（`pkg/tui/chat_session.go` 约 `:774`）。

### 6. 网关文件下发的现有安全范例（`pkg/gateway/files_write.go` 约 `:458-515`）

```go
var inlineAttachmentTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "application/pdf": true,
}
func serveAttachmentFile(w http.ResponseWriter, r *http.Request, f *os.File, name string, inline bool) {
	...
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.ServeContent(w, r, name, st.ModTime(), f)
}
```
它的原则（本计划沿用）：**只有浏览器画出来不会执行任何东西的类型才 inline**；其余一律下载；`nosniff`；`http.ServeContent` 自带 Range（视频拖动要它）。

### 7. 网关路由与会话归属

- 路由表在 `pkg/gateway/api_extra.go` 约 `:179-224`：`chatSessions := api.Group("/chat/sessions")`，如 `chatSessions.Get("/:id/tool-output/:retrieve", s.handleChatToolOutput)`。
  取路径参数：`ParamsFromContext(r.Context()).ByName("id")`。
- 会话归属检查：`s.sessionOwned(ctx, sessionID) (bool, error)`（不属于本主代理 → 404）。
- `pkg/gateway/session_files.go`（约 160 行）是"会话文件视图"的家：`filesRoot`、`resolveFilesPath`、`writeFilesRootError`。**生成物接口加在这个文件里**。
- `s.Home` 是 FOREBRAIN_HOME。

### 8. 约束

- `pkg/gateway` 生产文件已 31 个（上限 31）——**不许新建 gateway 生产文件**；测试加进现有的 `session_files_test.go`。
- `pkg/tool` 当前 28 个、`pkg/state` 当前 25 个，可以各新建 1 个（本计划新建 `pkg/tool/artifacts.go`、`pkg/state/artifact_store.go`，各配同名 `_test.go`）。
- fan-out 预算不许涨：`pkg/tool` 已 import `pkg/state`、`pkg/event`；`pkg/turn` 已 import `pkg/state`、`pkg/event`；`pkg/gateway` 已 import `pkg/tool`、`pkg/state`、`pkg/event`。**不新增任何包内 import**。
- 状态库版本：当前基线 `145` 预期用 v20，本计划紧接着预期用 **v21**。执行时以 145 落地后的 `stateSchemaVersion + 1` 为准，相应改迁移函数/测试名并在报告里写明；若 `183` 或其它迁移先落地则顺延，不能撞号。

## Commands you will need

| 目的 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `go build ./...` | exit 0 |
| vet | `go vet ./pkg/state ./pkg/tool ./pkg/run ./pkg/gateway ./pkg/tui ./pkg/turn ./pkg/event ./pkg/safety` | exit 0 |
| 单测（各包） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/<pkg> -count=1` | ok |
| 卡头 golden 更新 | `UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestHeaderActionsGolden -count=1` | ok，golden 多 3 行 |
| 前端词表 | `cd frontend && pnpm vitest run src/locales/toolHeader.test.ts` | passed |
| 架构 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |
| LibreOffice 是否可用（本机） | `command -v soffice \|\| ls /Applications/LibreOffice.app/Contents/MacOS/soffice` | 有输出才跑真机转换验收；没有则该验收记 NOT RUN |

## Scope

**In scope**：
- `pkg/state/schema.sql`、`pkg/state/schema_migrations.go`、`pkg/state/schema_migrations_test.go`
- `pkg/state/artifact_store.go`（新建）、`pkg/state/artifact_store_test.go`（新建）
- `pkg/tool/artifacts.go`（新建）、`pkg/tool/artifacts_test.go`（新建）、`pkg/tool/registry.go`、`pkg/tool/permissions.go`（只加 1 条结构性规则）、
  `pkg/tool/format.go`、`pkg/tool/header_parts.go`、`pkg/tool/display_header.go`、`pkg/tool/testdata/header_actions.json`（由测试生成）、`pkg/tool/testdata/present_files_description.golden`（新建，逐字契约）
- `pkg/run/runner.go`（只给 `rt` 补 `ProjectType`）、`pkg/run/subagent.go`（只改 `dropSubagentManagementTools`）
- `pkg/safety/engine.go`（只在 `safeInternalTools` 加一项）
- `pkg/event/run_events.go`
- `pkg/turn/events.go`（加 `RunArtifactsOf` 帮助函数）、`pkg/turn/events_test.go`
- `pkg/gateway/server.go`、`pkg/gateway/wsevents.go`、`pkg/gateway/approval.go`、`pkg/gateway/session_files.go`、`pkg/gateway/api_extra.go`（只加路由）、
  `pkg/gateway/session_files_test.go`、`pkg/gateway/wsevents_test.go`
- `pkg/tui/notify.go`、`pkg/tui/turn_lifecycle.go`（只在填 `RunPlanFacts` 的地方并列填 `RunArtifacts`）
- `ai-elements-vue/packages/elements/src/tool/headerWords.ts`（只加 3 个中文词）

**Out of scope**：
- 网页的生成物卡片、抽屉预览、docx/xlsx 前端渲染——`153`（先过原型）。
- 系统提示词里教模型何时调用 `present_files`——`147`（本计划只写工具自己的 description）。
- 跑批"没交付就兜底收集"——`149`；"只要计划"登记计划文件——`148`。
- 创意编程项目：**不注册** `present_files`，现有行为一字不改。

## Steps

### Step 1：`fb_artifacts` 表与迁移（预期 v21，取 145 后当前 + 1）

1. `pkg/state/schema.sql` 末尾（与其它表风格一致，带注释）加：
   ```sql
   -- A file a conversation delivered: the model presented it with present_files
   -- (origin 'model'), a batch task's output folder was collected when the task
   -- presented nothing (origin 'fallback'), or an office plan was kept as the
   -- deliverable (origin 'plan'). path is the absolute file as presented; the
   -- file itself is never copied, so a later edit or delete shows up as
   -- changed/missing when the row is read. run_id is a plain reference (no
   -- foreign key): an artifact must outlive the pruning of run rows.
   CREATE TABLE fb_artifacts (
     id TEXT PRIMARY KEY,
     agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
     session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
     run_id TEXT NOT NULL DEFAULT '',
     tool_call_id TEXT NOT NULL DEFAULT '',
     path TEXT NOT NULL,
     rel_path TEXT NOT NULL DEFAULT '',
     name TEXT NOT NULL,
     title TEXT NOT NULL DEFAULT '',
     description TEXT NOT NULL DEFAULT '',
     mime TEXT NOT NULL,
     kind TEXT NOT NULL,
     preview TEXT NOT NULL DEFAULT '',
     size INTEGER NOT NULL,
     mtime_ns INTEGER NOT NULL DEFAULT 0,
     sha256 TEXT NOT NULL DEFAULT '',
     origin TEXT NOT NULL DEFAULT 'model' CHECK (origin IN ('model', 'fallback', 'plan')),
     created_at INTEGER NOT NULL
   ) STRICT;
   CREATE INDEX idx_fb_artifacts_session ON fb_artifacts(session_id, created_at);
   CREATE INDEX idx_fb_artifacts_run ON fb_artifacts(run_id);
   ```
2. `schema_migrations.go`：基线预期 `stateSchemaVersion = 21`，迁移表加 `{version: 21, apply: migrateStateV20ToV21}`（若 145 后版本不同则顺延）；新函数照 `migrateStateV16ToV17` 写成
   `CREATE TABLE IF NOT EXISTS fb_artifacts (...)` + 两条 `CREATE INDEX IF NOT EXISTS`，**文本与 schema.sql 逐字相同**（`TestStateMigrationMatchesFreshSchema` 比对 `sqlite_master` 文本）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestStateMigrationMatchesFreshSchema|TestStateOpenIsIdempotent' -count=1` → ok。

### Step 2：`pkg/state/artifact_store.go`（新建）

`SessionStore` 上的方法（同库同租户边界；不新建 store 类型）：

```go
// Artifact is one row of fb_artifacts.
type Artifact struct {
	ID, AgentID, SessionID, RunID, ToolCallID string
	Path, RelPath, Name, Title, Description   string
	MIME, Kind, Preview                       string
	Size, MTimeNS                             int64
	SHA256, Origin                            string
	CreatedAt                                 int64
}

const (
	ArtifactOriginModel    = "model"
	ArtifactOriginFallback = "fallback"
	ArtifactOriginPlan     = "plan"
)

func (s *SessionStore) AddArtifact(ctx context.Context, a Artifact) (Artifact, error)
func (s *SessionStore) GetArtifact(ctx context.Context, sessionID, id string) (Artifact, error)      // 不存在或别人的 → ErrArtifactNotFound
func (s *SessionStore) ListArtifactsPage(ctx context.Context, sessionID string, limit, offset int) ([]Artifact, int, error)
func (s *SessionStore) ListArtifactsForRun(ctx context.Context, runID string) ([]Artifact, error)
var ErrArtifactNotFound = errors.New("artifact not found")
```

- `AddArtifact`：`ID` 为空时 `"art-" + ...`，用 `NewID("art")`；`AgentID = s.AgentID()`；`CreatedAt = time.Now().Unix()`；`Origin` 空 → `model`；
  `session_id` 必须是本主代理的会话（先 `s.requireOwned(ctx, s.db, sessionID)`，该函数在 `session_store.go` 已存在）。
- `ListArtifactsPage`：`ORDER BY created_at ASC, rowid ASC`（交付顺序），返回总数；分页窗口照 `ListSessionsForProjectPage` 用 `projectPageWindow(limit, offset)`、越界读最后一页的规则。
- `ListArtifactsForRun`：`WHERE run_id=? AND agent_id=? ORDER BY created_at, rowid`；`runID` 为空返回空切片。

**Verify**：`go build ./pkg/state` → exit 0。

### Step 3：分类与 `present_files` 工具（`pkg/tool/artifacts.go`，新建）

1. 分类（纯函数，网关也用它决定怎么下发）：
   ```go
   // ArtifactKind names what a delivered file is, for people and for the
   // systems that integrate with forebrain: document, spreadsheet,
   // presentation, pdf, image, video, audio, html, markdown, text, archive,
   // design, cad or other.
   // ArtifactPreview names how a surface may show it in place: image, video,
   // audio, pdf, html, markdown, text, docx, sheet, office_pdf (converted to
   // PDF by LibreOffice), archive, or "" (download only).
   func ClassifyArtifact(name string, head []byte) (kind, mimeType, preview string)
   ```
   按**小写扩展名**决定（`head` 只用于"无扩展名时"的 `http.DetectContentType` 兜底，以及图片/PDF 的签名校验见网关）：

   | 扩展名 | kind | preview | mime |
   |---|---|---|---|
   | docx | document | docx | application/vnd.openxmlformats-officedocument.wordprocessingml.document |
   | doc, odt, rtf | document | office_pdf | application/msword / application/vnd.oasis.opendocument.text / application/rtf |
   | xlsx, xlsm, xls, ods | spreadsheet | sheet | 各自标准 MIME（xlsx: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet；xls: application/vnd.ms-excel；ods: application/vnd.oasis.opendocument.spreadsheet；xlsm: application/vnd.ms-excel.sheet.macroEnabled.12） |
   | csv, tsv | spreadsheet | text | text/csv / text/tab-separated-values |
   | pptx, ppt, odp | presentation | office_pdf | application/vnd.openxmlformats-officedocument.presentationml.presentation / application/vnd.ms-powerpoint / application/vnd.oasis.opendocument.presentation |
   | pdf | pdf | pdf | application/pdf |
   | png, jpg, jpeg, gif, webp, bmp | image | image | image/png 等 |
   | svg | image | image | image/svg+xml |
   | mp4, m4v, webm, mov | video | video | video/mp4 / video/webm / video/quicktime |
   | mp3, wav, m4a, aac, ogg, oga, flac | audio | audio | audio/mpeg / audio/wav / audio/mp4 / audio/aac / audio/ogg / audio/flac |
   | html, htm | html | html | text/html |
   | md, markdown | markdown | markdown | text/markdown |
   | txt, log, json, yaml, yml, xml, sql, py, js, ts, go, java, c, h, cpp, sh, css | text | text | text/plain（json → application/json） |
   | zip, tar, tgz, gz（仅 .tar.gz） | archive | archive | application/zip / application/x-tar / application/gzip |
   | 7z, rar | archive | "" | application/x-7z-compressed / application/vnd.rar |
   | psd, ai, sketch, fig, xd, rp（Axure） | design | "" | application/octet-stream（psd: image/vnd.adobe.photoshop） |
   | dwg, dxf | cad | "" | application/acad / application/dxf |
   | 其它 | other | "" | `mime.TypeByExtension`，无则 application/octet-stream |

   表写成一个 `map[string]artifactClass{kind, mime, preview}`，测试逐行覆盖。
2. 工具：
   ```go
   type presentFilesInput struct {
   	Files []presentFileInput `json:"files" jsonschema:"minItems=1,maxItems=20,description=Every final deliverable of this task, in the order the user should see them."`
   }
   type presentFileInput struct {
   	Path        string `json:"path" jsonschema:"description=The file, relative to the project directory or an absolute path inside it."`
   	Title       string `json:"title,omitempty" jsonschema:"description=A short human title for the card, e.g. 2026 Q3 operating review. Defaults to the file name."`
   	Description string `json:"description,omitempty" jsonschema:"description=One sentence on what the file is and what it is for."`
   }
   const presentFilesToolName = "present_files"
   const presentFilesToolDescription = "Use when your work produced files the user (or the system that started this task) should receive: documents, spreadsheets, slides, PDFs, images, audio or video, HTML or Markdown pages, archives. Call it once, after the files are final, listing every deliverable; the user gets a download and preview card for each one. Do not present intermediate scripts, scratch data or files you did not finish. Paths must be inside the project directory — save or copy a deliverable there first if it was written elsewhere. Presenting the same path again later records a new version."
   ```
   工具 description **逐字使用** `presentFilesToolDescription`，不得执行时另行润色；`newPresentFilesTool` 把该常量传给 `llm.NewTool`。
   同一全文写入 `pkg/tool/testdata/present_files_description.golden`（保留末尾换行），`TestPresentFilesDescriptionGolden` 用 `os.ReadFile` + `strings.TrimSuffix(golden,"\n")` 与常量完全相等；正常测试不自动重写 golden。

   处理函数（`newPresentFilesTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error)`）：
   1. `rt == nil || rt.Sess == nil` → `fmt.Errorf("present_files unavailable")`。
   2. `IsSubagentRunFromContext(ctx)` → `fmt.Errorf("present_files is only available to the primary agent; report the files back and let it present them")`（双保险，见 Step 4）。
   3. `root, err := WorkingDir(ctx)`；对每个文件：`abs, _ := ResolvePath(ctx, f.Path)` → `resolved, err := ResolveWithinRoots(abs, []string{root})`，
      越界 → 整次调用失败，错误写明 `"<path> is outside the project directory <root>; save or copy it into the project first"`；
      `os.Stat` 不存在或是目录 → 失败并说明；**任何一个失败都不写任何一行**（先全部校验，再逐个写库）。
   4. 读前 512 字节做 `ClassifyArtifact`；整文件流式算 `sha256`（`io.Copy(h, f)`）；`size`、`mtime_ns` 取 `Stat`。
   5. `rel_path` = `filepath.ToSlash(filepath.Rel(root, resolved))`；`name` = `filepath.Base(resolved)`；`title` 空则用 `name`。
   6. `rt.Sess.AddArtifact(ctx, state.Artifact{SessionID: ConversationSessionIDFromContext(ctx), RunID: RunIDFromContext(ctx), ToolCallID: ToolUseIDFromContext(ctx), ...})`。
   7. 返回给模型的 JSON（紧凑，不含绝对路径）：
      ```json
      {"presented":[{"id":"art-…","name":"季度报告.docx","rel_path":"outputs/季度报告.docx","title":"…","kind":"document","preview":"docx","size":12345}],
       "note":"The user now sees a download and preview card for each file."}
      ```
3. `pkg/tool/registry.go`：
   - `AgentToolRuntime` 加 `ProjectType string`（注释：会话类型；office/batch 才注册 present_files）。
   - `RegisterDefaultTools` 里在 `tLSP` 之后：
     ```go
     var tPresent *llm.Tool
     if rt != nil && (rt.ProjectType == state.ProjectTypeOffice || rt.ProjectType == state.ProjectTypeBatch) {
     	tPresent, err = newPresentFilesTool(st, rt)
     	if err != nil {
     		return err
     	}
     	registerMeta(tPresent, "Use when your work produced final files the user should receive (download and preview cards)", "session", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
     }
     ...
     if tPresent != nil {
     	_ = st.Register(a, tPresent)
     }
     ```
   - `pkg/run/runner.go` 构造 `rt` 时加 `ProjectType: r.ProjectType(),`。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'Artifact|PresentFiles' -count=1` → ok（测试见 Test plan）。

### Step 4：只给主代理、免审批

1. `pkg/tool/artifacts.go` 导出 `func PrimaryOnlyToolName(name string) bool { return strings.TrimSpace(name) == presentFilesToolName }`。
2. `pkg/tool/permissions.go` `GuardTool` 的结构性规则后紧跟：
   ```go
   if IsSubagentRunFromContext(ctx) && PrimaryOnlyToolName(name) {
   	return fmt.Errorf("%s is only available to the primary agent", name)
   }
   ```
3. `pkg/run/subagent.go` `dropSubagentManagementTools` 的 `if` 条件改为 `subagentToolName(...) || tool.PrimaryOnlyToolName(...)`（子代理的工具表里看不到它）。
4. `pkg/safety/engine.go` `safeInternalTools` 加 `"present_files": {},`。

**Verify**：`go build ./...` → exit 0。

### Step 5：卡片正文与卡头（终端、网页同词）

1. `pkg/tool/format.go` `FormatToolStepResult` 的 switch 加 `case "present_files": body, truncated = formatPresentFilesStep(evt, maxBytes)`；函数放进 `pkg/tool/artifacts.go`：
   从 `evt.Output["presented"]` 读数组，每项一行 Markdown：`- **<title>** · <kind 的英文名> · <人类可读大小> · \`<rel_path>\``（title 等于 name 时只写 name）。
   输出不是该形状时返回 `"Presented files."`——**绝不回落成原始 JSON**。大小格式：B / KB / MB / GB，保留 1 位小数（`1.2 MB`）。
2. `pkg/tool/header_parts.go` 加
   ```go
   case lower == "present_files":
   	if isFailed {
   		action = "Failed to " + FailedActionPhrase(lower)
   	} else if isRunning || isPending {
   		action = "Presenting files"
   	} else {
   		action = "Presented files"
   	}
   	target = presentFilesHeaderTarget(meta) // 1 个文件：文件名；多个："<第一个文件名> +N"
   ```
   （`presentFilesHeaderTarget` 放 `artifacts.go`，从 `meta.Input["files"]` 读 `path` 取 `filepath.Base`。）
   `HeaderActions()` 在 `// Notes and questions` 之前加一行 `// Deliverables` 和 `"Presented files", "Presenting files", "Failed to present files",`。
3. `pkg/tool/display_header.go` `FailedActionPhrase` 加 `case "present_files": return "present files"`。
4. 生成 golden：`UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestHeaderActionsGolden -count=1`，确认 `git diff pkg/tool/testdata/header_actions.json` 只多这 3 行。
5. `ai-elements-vue/packages/elements/src/tool/headerWords.ts` 的 `HEADER_ACTION_ZH` 照同文件风格加：
   `'Presented files': '已交付文件'`、`'Presenting files': '正在交付文件'`、`'Failed to present files': '交付文件失败'`（先看同文件里 `'Saving notes'` 等"进行中"词的写法，保持一致）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → ok；`cd frontend && pnpm vitest run src/locales/toolHeader.test.ts` → passed。

### Step 6：结构化数据进运行结束帧

1. `pkg/event/run_events.go` 在 `RunPlanFacts` 之后加：
   ```go
   // Artifact is one delivered file as integrators and surfaces receive it.
   // Paths are the server's; a client downloads through the gateway's
   // /api/chat/sessions/{session_id}/artifacts/{id}/content route.
   type Artifact struct {
   	ID          string `json:"id"`
   	SessionID   string `json:"session_id"`
   	RunID       string `json:"run_id,omitempty"`
   	ToolCallID  string `json:"tool_call_id,omitempty"`
   	Name        string `json:"name"`
   	Title       string `json:"title,omitempty"`
   	Description string `json:"description,omitempty"`
   	Path        string `json:"path"`
   	RelPath     string `json:"rel_path,omitempty"`
   	MIME        string `json:"mime"`
   	Kind        string `json:"kind"`
   	Preview     string `json:"preview,omitempty"`
   	Size        int64  `json:"size"`
   	SHA256      string `json:"sha256,omitempty"`
   	Origin      string `json:"origin"`
   	CreatedAt   int64  `json:"created_at"`
   }

   // RunArtifacts is every file a run delivered, carried by every way a run
   // ends — the structured result an integrating system reads.
   type RunArtifacts struct {
   	Artifacts []Artifact `json:"artifacts,omitempty"`
   }
   ```
   并在 `TurnCompletedPayload`、`TurnCancelledPayload`、`TurnErrorPayload` 里 `RunPlanFacts` 下一行各加嵌入 `RunArtifacts`。
2. `pkg/turn/events.go` 仿 `LastPlanProgressOfRun` 加：
   ```go
   // RunArtifactLister is the slice of the session store the run's delivered
   // files are read through.
   type RunArtifactLister interface {
   	ListArtifactsForRun(ctx context.Context, runID string) ([]state.Artifact, error)
   }
   // RunArtifactsOf is every file the run delivered, in delivery order, as the
   // run's end carries it. Both surfaces close a run with it.
   func RunArtifactsOf(ctx context.Context, store RunArtifactLister, runID string) event.RunArtifacts
   ```
   （store 为 nil、runID 空、出错 → 空结构；`state.Artifact` → `event.Artifact` 逐字段拷贝。）
3. 网关：`wsServerMsg` 在 `event.RunPlanFacts` 下一行内嵌 `event.RunArtifacts`；`writeMsg` 在填 `RunPlanFacts` 的同一个 `if` 里加
   `m.RunArtifacts = turn.RunArtifactsOf(context.Background(), s.Sessions, m.RunID)`；`canonicalRunEventsFromWS` 三个 case 各把 `RunArtifacts: m.RunArtifacts` 抄进 payload；
   `approval.go` `publishGatewayRunEvent` 三个分支各加 `ending.RunArtifacts = turn.RunArtifactsOf(ctx, s.Sessions, runID)`（`s.Sessions` 为 nil 时函数返回空）。
4. 终端：`pkg/tui/notify.go` 加 `func (s *ChatSession) runArtifacts(ctx context.Context, runID string) event.RunArtifacts { return turn.RunArtifactsOf(ctx, s.sessStore(), runID) }`（注意 `s.sessStore()` 返回 `*state.SessionStore`，nil 时要传 nil 接口：先判 nil 再传）；
   凡是填 `RunPlanFacts` 的地方（`publishTUIRunEvent` 两处、`persistRunTurnError`、`turn_lifecycle.go` `turnCompletedEvent`）并列填 `RunArtifacts`。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/tui ./pkg/turn -count=1` → 无新增失败。

### Step 7：REST 接口（全部写进 `pkg/gateway/session_files.go`；路由加在 `api_extra.go` 的 `chatSessions` 组）

路由：
```go
chatSessions.Get("/:id/artifacts", s.handleSessionArtifacts)
chatSessions.Get("/:id/artifacts/:aid", s.handleSessionArtifact)
chatSessions.Get("/:id/artifacts/:aid/content", s.handleSessionArtifactContent)
chatSessions.Get("/:id/artifacts/:aid/preview.pdf", s.handleSessionArtifactPreviewPDF)
chatSessions.Get("/:id/artifacts/:aid/entries", s.handleSessionArtifactEntries)
```
（先用 `go test ./pkg/gateway -run TestRouter` 或读 `http_server.go` 确认 `/:aid/preview.pdf` 这种带点的静态尾段该路由器支持；不支持就改成 `/:aid/preview`，在报告里写明。）

所有处理函数开头：`sid` 不属于本主代理（`s.sessionOwned`）→ 404；`GetArtifact` 找不到 → 404 `{"error":"artifact_not_found"}`。

1. **行 DTO** `func artifactRow(a state.Artifact) map[string]any`：`event.Artifact` 的全部字段（snake_case）+
   `"missing"`（`os.Stat(a.Path)` 失败）、`"changed"`（大小或 `ModTime().UnixNano()` 与库里不同）、
   `"download_url": "/api/chat/sessions/<sid>/artifacts/<id>/content?disposition=attachment"`、
   `"content_url": ".../content?disposition=inline"`、`preview == "office_pdf"` 时加 `"preview_url": ".../preview.pdf"`、`preview == "archive"` 时加 `"entries_url"`。
2. `GET /artifacts?page=&limit=`（`limit` 默认 50、上限 200）→ `{"items":[row...], "total": N}`（owner 规则：列表一律服务端分页带总数）。
3. `GET /artifacts/:aid` → 一行。
4. `GET /artifacts/:aid/content?disposition=inline|attachment`：
   - 文件缺失 → 404 `{"error":"artifact_file_missing"}`。
   - 默认 `attachment`：`Content-Type` = 行里的 `mime`，`Content-Disposition: attachment; filename*=UTF-8''<url-escaped name>`（用 `mime.FormatMediaType("attachment", map[string]string{"filename": name})`，它会自动给非 ASCII 名字编码），`nosniff`，`http.ServeContent`。
   - `inline` 只在下面的白名单里生效，否则按 attachment：
     | preview | 条件 | 下发方式 |
     |---|---|---|
     | image（非 svg） | 头部签名是 `image/png|jpeg|gif|webp|bmp` | 该图片 MIME，inline |
     | image（svg） | — | `image/svg+xml` + `Content-Security-Policy: sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:` |
     | pdf | 头 5 字节 `%PDF-` | `application/pdf` inline |
     | video / audio | — | 行里的 MIME，inline（Range 由 ServeContent 处理） |
     | html | — | `text/html; charset=utf-8` + `Content-Security-Policy: sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox` |
     | markdown / text | — | `text/plain; charset=utf-8` inline（前端自己渲染，绝不当 HTML 下发） |
   - CSP 的 `sandbox` 指令（不带 `allow-same-origin`）让 HTML/SVG 即使被直接打开也是不透明源，拿不到网关的 cookie 与接口——注释里写明这是安全前提，**不许**加 `allow-same-origin`。
5. `GET /artifacts/:aid/entries`（只对 `preview=="archive"`，否则 400）：zip 用 `archive/zip`，`.tar`/`.tar.gz`/`.tgz` 用 `archive/tar` + `compress/gzip`；
   返回 `{"entries":[{"name","size","dir"}], "total": N, "truncated": bool}`，最多 5000 条；条目名过 `tool.ValidateArchiveRelPath` 不合法的照列但不做任何解压（本接口**只读目录**，永不解压到磁盘）。
6. `GET /artifacts/:aid/preview.pdf`（只对 `preview=="office_pdf"`，否则 400）：
   - 找 LibreOffice：环境变量 `FOREBRAIN_SOFFICE` → `exec.LookPath("soffice")` → `exec.LookPath("libreoffice")` → macOS `/Applications/LibreOffice.app/Contents/MacOS/soffice`（存在才用）。都没有 → 503 `{"error":"libreoffice_unavailable"}`。
   - 缓存：对**当前**文件内容算 sha256 → `<s.Home>/cache/artifact-previews/<sha256>.pdf`；命中直接下发。
   - 转换：`os.MkdirTemp` 建临时目录 → 把源文件复制成 `<tmp>/input.<ext>`（规避文件名里的空格 / 中文 / 前导 `-`）→
     `exec.CommandContext(ctx120s, soffice, "--headless", "--norestore", "--nolockcheck", "--nodefault", "--nofirststartwizard", "-env:UserInstallation="+profileURL, "--convert-to", "pdf", "--outdir", <tmp>/out, <tmp>/input.<ext>)`
     其中 `profileURL = (&url.URL{Scheme: "file", Path: filepath.ToSlash(<tmp>/profile)}).String()`（标准库 `net/url`，不能用字符串拼接：空格、`#`、`%` 必须转义）。
     每次转换独立 profile，两个转换不会抢同一个用户目录锁。成功后先读回 `<tmp>/out/input.pdf` 并校验 `%PDF-` 与非空，再写到缓存目录内唯一临时文件，`fsync/close` 后 `os.Rename` 为 `<sha256>.pdf`；跨盘问题因临时文件已在缓存目录而消除。不得让并发请求读到半写缓存；最后删除转换临时目录。
   - 并发：包级 `chan struct{}` 容量 2 做信号量；同一 sha256 并发请求用一个 `map[string]*sync.WaitGroup`（或 `singleflight` 式的手写去重，**不要**引入新依赖）只转一次。
   - 超时 → 504 `{"error":"preview_timeout"}`；退出码非 0 或没产出 → 502 `{"error":"preview_failed","detail":"<stderr 前 500 字节>"}`；源文件 > 200 MB → 413 `{"error":"preview_too_large"}`。
   - 下发：`application/pdf`，`inline`，`nosniff`，`ServeContent`。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'Artifact' -count=1` → ok。

### Step 8：测试与验收（见 Test plan），然后跑 Done criteria。

## Test plan

1. `pkg/state/artifact_store_test.go`（夹具照 `project_store_test.go` 的 `newProjectStoreForTest`）：
   `TestAddArtifactRequiresOwnedSession`（别的主代理的会话 → 错误、无行）；`TestListArtifactsPageOrdersAndCounts`（3 行，limit 2 → 2 行 + total 3，越界 offset 读最后一页）；
   `TestListArtifactsForRun`（两个 run 各 1 行，只返回本 run 的）；`TestArtifactsGoWithTheirSession`（删会话后行级联消失）。
2. `pkg/state/schema_migrations_test.go`：`TestStateV20UpgradesToV21AddsArtifacts`（版本顺延时同步改名；照 `TestStateV16UpgradesToV17AddsPermissionState`：`DROP TABLE fb_artifacts` + `user_version=20`（或前一版）→ 重开表在）。
3. `pkg/tool/artifacts_test.go`：
   - `TestClassifyArtifactTable`：表里每个扩展名一行断言 `(kind, mime, preview)`；无扩展名 + PNG 头 → image。
   - `TestPresentFilesDescriptionGolden`：工具注册后 `Description()` 与 `presentFilesToolDescription`、golden 三者逐字一致。
   - `TestPresentFilesRecordsEveryFile`：临时项目根里写 `a.docx`、`b.pptx`，ctx 带项目根 / 会话 / run / tool use id（照仓库里现有工具测试设置 ctx 的方式，grep `WithProjectRoot(` 找范例），调处理函数 → 库里 2 行、字段齐（sha256 与 `sha256.Sum256` 一致、rel_path、kind、preview）、返回 JSON 含两个 id。
   - `TestPresentFilesRefusesOutsideTheProject`：一个项目内文件 + 一个 `os.TempDir()` 外的文件 → 报错且**一行都不写**。
   - `TestPresentFilesRefusesSymlinkEscape`：项目内软链指向项目外文件 → 报错。
   - `TestPresentFilesRefusedForSubagents`：ctx 标为子代理运行 → 报错；`GuardTool` 对 `present_files` 报错。
   - `TestPresentFilesRegisteredOnlyForOfficeAndBatch`：`RegisterDefaultTools` 分别用 `ProjectType` = coding / office / batch，只有后两者注册了 `present_files`。
   - `TestFormatPresentFilesStepNeverShowsJSON`：正常输出 → 每文件一行 Markdown；畸形输出 → `"Presented files."`。
4. `pkg/turn/events_test.go`：`TestRunArtifactsOfConvertsRows`（假 lister 返回 2 行 → 2 个 `event.Artifact`，字段逐一相等；nil lister → 空）。
5. `pkg/gateway/wsevents_test.go`：照现有 `RunPlanFacts` 的测试（grep `PlanDone` 找）加 `TestRunEndFramesCarryArtifacts`：库里给某 run 插 1 行 artifact → `run_completed` 帧与持久化的 `turn_completed` 事件 payload 都含 `artifacts[0].id`。
6. `pkg/gateway/session_files_test.go`：
   `TestArtifactListPagesWithTotal`；`TestArtifactContentDownloadsByDefault`（`Content-Disposition` 以 `attachment` 开头、`nosniff`）；
   `TestArtifactContentInlineOnlyForSafeTypes`（`.html` inline 带 `Content-Security-Policy` 且含 `sandbox`、**不含** `allow-same-origin`；伪装成 `.png` 的 HTML 文本 inline 时被降级为 attachment；`.md` inline 为 `text/plain`）；
   `TestArtifactContentServesRanges`（`Range: bytes=0-3` → 206、4 字节）；`TestArtifactOfAnotherSessionIs404`；`TestArtifactMissingFileIs404WithCode`；
   `TestArtifactEntriesListsAZip`（用 `archive/zip` 现场写一个两条目的 zip）；
   `TestArtifactPreviewWithoutLibreOfficeIs503`（`t.Setenv("FOREBRAIN_SOFFICE", "/nonexistent")` 且 `t.Setenv("PATH", t.TempDir())`）；
   `TestArtifactPreviewUsesTheCache`（预先在 `<home>/cache/artifact-previews/<sha>.pdf` 放一个 `%PDF-1.4` 文件 → 200 且不调用 soffice；soffice 设为一个不存在的路径以证明没调用）。
7. **真机验收（本机有 LibreOffice 才跑，否则在报告里写 NOT RUN）**：写一个用 `exec.LookPath("soffice")` 守卫的测试 `TestArtifactPreviewConvertsAPptx`（`if testing.Short() || 找不到 soffice { t.Skip(...) }`），
   用仓库里任意现有 `.pptx` 测试夹具（`git ls-files '*.pptx'`；没有则用 `pkg/skill/system_assets/pptx` 下的脚本说明里的方法现场生成会太重——**没有现成夹具就只跑手工验收**：
   在本机找一个 pptx，用 `curl` 打 `/preview.pdf` 并确认返回 `%PDF`），记录耗时。

## Done criteria

- [ ] `go build ./...`、上面的 `go vet` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tool ./pkg/run ./pkg/gateway ./pkg/tui ./pkg/turn ./pkg/safety ./pkg/event ./pkg/architecture -count=1` 无新增失败；Test plan 1–6 的新测试都存在且通过
- [ ] `cd frontend && pnpm vitest run src/locales/toolHeader.test.ts` passed
- [ ] `git diff pkg/tool/testdata/header_actions.json` 只新增 `Failed to present files`、`Presented files`、`Presenting files` 三行
- [ ] `grep -n '"present_files": {}' pkg/safety/engine.go` 有 1 条
- [ ] `grep -rn "allow-same-origin" pkg/gateway/session_files.go` 无输出
- [ ] `ls pkg/gateway/*.go | grep -v _test.go | wc -l` 仍是 31
- [ ] 创意编程项目的工具表与本计划前一致：`TestPresentFilesRegisteredOnlyForOfficeAndBatch` 通过
- [ ] `plans/README.md` 本计划状态行已更新（真机 LibreOffice 验收：PASS / NOT RUN 写明）

## STOP conditions

1. 前置不满足，或"现状"里的函数对不上。
2. `ResolveWithinRoots` 对软链的处理与"解析后再判包含"不同（`TestPresentFilesRefusesSymlinkEscape` 写好后在未改实现前就是绿的、或者无法让它变红）——停下报告，不要自己写一套路径解析。
3. 路由器不支持 `/:aid/preview.pdf` 且改成 `/:aid/preview` 也不行。
4. 任何一步需要新建 `pkg/gateway` 生产文件、或新增包内 import（架构测试会红）。
5. LibreOffice 真机转换产出空 PDF 或卡死且超时也杀不掉——记录现象停下，不要加平台特定的进程组代码（`pkg/gateway` 加不了 build-tag 文件）。
6. `frontend/src/locales/toolHeader.test.ts` 在你加完中文词后仍红且原因不是缺词。

## Maintenance notes

- **只登记、不复制**：行里存的是绝对路径；文件之后被改/删，读接口给 `changed` / `missing`。如果以后业务系统要求"交付即冻结"，再加"复制到 `<home>/artifacts/<id>`"——那是存储策略变化，先问 owner。
- `ClassifyArtifact` 是 kind / preview 的**唯一**来源，网页（`153`）、通道通知（`151`）、对接文档（`149`）都按它的取值写；新增格式只改这张表并补测试。
- LibreOffice 子进程：`exec.CommandContext` 只杀直接子进程；Linux 上 `soffice` 是包装脚本，超时后 `soffice.bin` 可能残留。每次转换独立 `UserInstallation` 保证残留不阻塞下一次；
  若线上发现残留堆积，再单独立计划做进程组清理（需要能放 build-tag 文件的包）。
- 运行结束帧里的 `artifacts` 是业务系统拿结构化结果的主路径（`149` 的对接文档会写）；改它的字段名 = 改对外协议。
