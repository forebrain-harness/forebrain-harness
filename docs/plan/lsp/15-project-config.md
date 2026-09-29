# 任务 15：项目级 `lsp_servers.yaml` 与逐条同意

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §3.7、§5.2、§5.3、§9.2、§9.3、附录 D 的第二个例子。
>
> **前置**：任务 08 已合入（`Manager.servers()` 有缓存，`ResolveInput.ProjectServers` 仍传 nil）。先确认：`grep -n "ProjectServers" pkg/lsp/resolve.go` 有输出，否则 STOP。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/process/mcp.go cmd/forebrain/mcp_consent.go cmd/forebrain/interactive.go pkg/gateway/api_extra.go frontend/src/views/ProjectDetailView.vue`。对比「现状」摘录。

## 状态

- 优先级：P2 · 工作量：M · 风险：MED（项目文件随代码检出，能决定在用户机器上执行什么）
- 依赖：08
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

团队需要把「这个仓库的 gopls 要带 `-tags=integration`」「TypeScript 用项目里的 tsserver」这类设置提交进仓库。项目文件随检出到达，等同于让仓库决定用户机器上执行的命令，所以与项目级 MCP 一样：只在受信任且受版本控制的项目生效，每个条目按指纹逐条同意。

## 现状

- 项目级 MCP 的全套做法（本任务逐项照搬）：
  - 存储：`pkg/mcp/project_consent.go`——`ProjectConsentRecord{Fingerprint, Decision, DecidedAt}`、`ProjectConsents map[projectKey]map[lowercased name]record`、`LoadProjectConsents`/`SaveProjectConsents`（原子写，目录 0700、文件 0600）、`Decision`（指纹不同视为未决定）、`Decide`、`ServerFingerprint`（对规范化 JSON 做 sha256）、`PendingProjectConsent{Name, Fingerprint, Summary}`、`ConsentSummary`（`name: runs \`cmd args\``）。
  - 加载：`pkg/mcp/project_config.go:77 LoadProjectMCPServers(projectRoot) ([]appcfg.MCPServerConfig, []MCPFileNote)`；明文密钥用 `appcfg.RejectPlaintextConfigSecrets(path, b)`（`pkg/config/secrets.go:26`）拒收，并给出 `file contains a plaintext secret; use ${ENV_NAME} resolved from ~/.forebrain/.env`。
  - 组合根：`pkg/process/mcp.go:166 PendingProjectMCPConsents(agentWorkspace, launch)`、`:194 DecideProjectMCPConsents(agentWorkspace, launch, allowed)`（`safety.TrustedRoot(launch)` 为空时直接返回；项目键 `memory.ProjectKey(root)`；未被允许的待决条目记为拒绝）。
  - 终端：`cmd/forebrain/mcp_consent.go:20 ensureProjectMCPConsent(in, out, home, cwd)` 在信任提示之后逐条询问 `Run it? [y/N]`；由 `cmd/forebrain/interactive.go:77` 调用（经变量 `interactiveEnsureProjectMCPConsent`，测试可替换）。
  - gateway：`GET /api/v1/projects/:id/mcp`（`pkg/gateway/mcp_oauth_api.go:356 handleProjectMCPPreview`）与 `POST /api/v1/projects/:id/mcp/consent`（路由在 `api_extra.go:119-120`）；Web 在 `frontend/src/views/ProjectDetailView.vue:77-120` 显示待确认条目与「允许 / 拒绝」按钮。
- `safety.TrustedRoot(launch)`（`pkg/safety/runtime.go:302`）已经包含「受版本控制」的判断，所以 `ManagerOptions.Trusted`（任务 03：`safety.TrustedRoot(launch) != ""`）为 true 即满足规范 §5.2 的两个条件。
- `pkg/lsp` 的 fan-out 只有 `config`、`home`、`event`、`tool`：**不能**导入 `pkg/mcp`、`pkg/memory`、`pkg/safety`，同意存储要在 `pkg/lsp` 里另写一份（结构相同），项目键由调用方传入。
- 任务 06：`ResolveInput.ProjectServers map[string]appcfg.LSPServerConfig`（只放已同意的条目）；合并规则已处理 `Scope = "project"`、`EnvFromProject = true`、忽略项目条目的 `EnvPassthrough` 与 `Priority`。任务 01：`appcfg.ValidateLSPServers(source, servers)`。
- 本任务新建 `pkg/lsp/project.go`，之后 `pkg/lsp` 共 17 个生产文件（规范附录 E）。

## 范围

**新建：** `pkg/lsp/project.go`、`pkg/lsp/project_test.go`。

**修改：** `pkg/lsp/manager.go`、`resolve.go`、`control.go`、`doc.go` 及对应测试；`pkg/process/mcp.go`、`mcp_test.go`；`cmd/forebrain/mcp_consent.go`、`interactive.go`、`interactive_test.go`；`pkg/gateway/api_extra.go`、`api_extra_test.go`；`frontend/src/views/ProjectDetailView.vue`、`frontend/src/lib/api.ts`、`frontend/src/locales/index.ts`；`pkg/architecture/testdata/graph.json`。

**不要碰：** `pkg/mcp`（不复用它的类型，避免 `lsp` 新增依赖）、`pkg/config`（`config` 已满 20 个文件）。

## 步骤

### 步骤 1：`pkg/lsp/project.go`

```go
// Project-level language-server entries: <project>/.forebrain/lsp_servers.yaml,
// honored only in trusted, version-controlled projects and only entry by
// entry after the operator confirmed each one (spec §5.2).
package lsp

// ProjectLSPPath is <projectRoot>/.forebrain/lsp_servers.yaml.
func ProjectLSPPath(projectRoot string) string

// LoadProjectServers parses the project file. A missing file is no entries and
// no notes; a file that cannot be used is no entries and one note saying why.
func LoadProjectServers(projectRoot string) (map[string]appcfg.LSPServerConfig, []string)

// ProjectServerFingerprint is the identity the consent is keyed by: sha256 of
// the canonical JSON of command, args, env, initialization_options, settings,
// workspace_folder, root_markers and enabled (spec §5.2), hex.
func ProjectServerFingerprint(srv appcfg.LSPServerConfig) string

// Project consent decisions and store, the same shape as the MCP store:
// project key -> lowercased server id -> record.
const (
	ProjectConsentAllow = "allow"
	ProjectConsentDeny  = "deny"
)

type ProjectConsentRecord struct {
	Fingerprint string `json:"fingerprint"`
	Decision    string `json:"decision"`
	DecidedAt   int64  `json:"decided_at"`
}

type ProjectConsents map[string]map[string]ProjectConsentRecord

func ProjectConsentPath(agentWorkspace string) string // <StateDir>/project_consent.json
func LoadProjectConsents(agentWorkspace string) (ProjectConsents, error)
func SaveProjectConsents(agentWorkspace string, c ProjectConsents) error
func (c ProjectConsents) Decision(projectKey, serverID, fingerprint string) (decision string, decided bool)
func (c ProjectConsents) Decide(projectKey, serverID, fingerprint, decision string, at time.Time)

// PendingProjectServer is one project entry awaiting confirmation.
type PendingProjectServer struct {
	ID          string `json:"id"`
	Fingerprint string `json:"-"`
	Summary     string `json:"summary"`
}

// ProjectServerState is the project file as it applies to one agent.
type ProjectServerState struct {
	Allowed map[string]appcfg.LSPServerConfig // what ResolveInput.ProjectServers receives
	Pending []PendingProjectServer
	Denied  []string
	Notes   []string // why the file or an entry was ignored
}

// ResolveProjectServers reads the file and the consent store. trusted false
// (not trusted or not version controlled) yields an empty state: the file is
// not even parsed.
func ResolveProjectServers(agentWorkspace, projectRoot, projectKey string, trusted bool) ProjectServerState

// DecideProjectServers records allow for the ids in allowed and deny for
// every other pending entry, so a declined entry is not asked again until its
// fingerprint changes.
func DecideProjectServers(agentWorkspace, projectRoot, projectKey string, allowed []string) error
```

实现要点：

- `LoadProjectServers`：读文件；`appcfg.RejectPlaintextConfigSecrets(path, b)` 出错 → 无条目 + 说明 `lsp_servers.yaml contains a plaintext secret; use ${ENV_NAME} resolved from ~/.forebrain/.env`；YAML 顶层为 `servers:`，未知顶层键 → 说明 `lsp_servers.yaml: unknown key "<k>"`，但 `servers` 照常读取；条目规范化（与任务 01 的 `normalizeLSPSection` 同规则：id 小写去空白、扩展名键小写、`Role` 小写）；`appcfg.ValidateLSPServers("lsp_servers.yaml servers", m)` 出错 → 无条目 + 该错误文本。
- 条目里的 `env_passthrough` 与 `priority` 在这里就清零，并加说明 `<id>: env_passthrough and priority are ignored in project files`。
- `ProjectServerFingerprint`：`struct{Command; Args; Env; InitializationOptions json.RawMessage; Settings json.RawMessage; WorkspaceFolder; RootMarkers; Enabled *bool}` 按字段名 `json.Marshal`（map 键自动排序），`InitializationOptions`/`Settings` 先经 `json.Compact` 规范化，空值用 `null`。
- 同意存储：照 `pkg/mcp/project_consent.go` 逐函数实现（原子写、目录 0700、文件 0600、非法 JSON 视为空存储）。
- `Summary`（逐字格式）：有 `Command` → `<id>: runs \`<command> <args…>\``；只覆盖目录服务器的设置 → `<id>: changes settings of the built-in <id> server`；两者都有时用前者；`enabled: true` 时追加 ` and enables it`。
- `ResolveProjectServers`：`trusted == false` 或 `projectRoot == ""` → 零值。否则对每个条目：`Decision` 为 allow → 放入 `Allowed`；deny → `Denied`；未决定 → `Pending`（按 id 排序）。

### 步骤 2：接入合并（`manager.go`、`resolve.go`、`control.go`）

- `Manager.servers()`：构建 `ResolveInput` 时 `ProjectServers = ResolveProjectServers(ws, opts.ProjectRoot, opts.ProjectKey, opts.Trusted).Allowed`；缓存失效条件增加「项目文件 mtime 变化」与「`project_consent.json` mtime 变化」。
- `ToolEnabled`：同样传入 `Allowed`（冻结时刻已同意的项目条目参与「至少一个已启用 primary」的判断）。
- `Snapshot()`：
  - 对 `Pending` 中的每个 id：若 `servers()` 里已有该 id（目录或全局条目），在其行的 `Note` 前加 `project settings await confirmation; `，状态不变；否则追加一行 `{ID, Scope: "project", State: blocked, Note: "project entry awaiting confirmation: start forebrain in this project, or confirm it on the project page"}`。
  - `Denied` 中只存在于项目的条目：追加一行 `State: blocked`，`Note: "project entry declined; it asks again when .forebrain/lsp_servers.yaml changes"`。
  - `Notes` 放进 `LSPSnapshot.ProjectNotes`（任务 02 定义的字段；任务 14 的三个渲染处已把它显示为 `Project file: <note>`）。
- `doc.go` 增加 `project.go (project-level servers and their per-entry consent)`。

### 步骤 3：组合根（`pkg/process/mcp.go`）

在 MCP 的两个函数之后加：

```go
// PendingProjectLSPConsents lists the project language-server entries that
// would apply but have no recorded decision.
func PendingProjectLSPConsents(agentWorkspace string, launch safety.ProjectContext) []lsp.PendingProjectServer

// DecideProjectLSPConsents records the operator's answers; every other pending
// entry is recorded as declined.
func DecideProjectLSPConsents(agentWorkspace string, launch safety.ProjectContext, allowed []string) error
```

两者都以 `root := safety.TrustedRoot(launch)` 为空直接返回，项目键 `memory.ProjectKey(root)`，然后调用 `lsp.ResolveProjectServers(...).Pending` / `lsp.DecideProjectServers(...)`。

### 步骤 4：终端启动时询问（`cmd/forebrain`）

- `mcp_consent.go` 加 `ensureProjectLSPConsent(in, out, home, cwd) error`，结构与 `ensureProjectMCPConsent` 相同，文案（逐字）：引导行 `This project configures language servers. Confirm each one before it runs in this session.`；每条先打印 `Summary`，再问 `Allow it? [y/N] `；回答 `y`/`yes` → `  confirmed — it applies from now on`，否则 `  skipped — confirm later on the project page or at the next start`。
- `interactive.go`：新增变量 `interactiveEnsureProjectLSPConsent = ensureProjectLSPConsent`，在 MCP 同意之后立即调用（同样的错误处理）。

### 步骤 5：gateway 与 Web

- `api_extra.go` 路由表在项目 MCP 两行之后加 `projects.Get("/:id/lsp", s.handleProjectLSPPreview)` 与 `projects.Post("/:id/lsp/consent", s.handleProjectLSPConsent)`，实现照 `handleProjectMCPPreview` / `handleProjectMCPConsent`：预览返回 `{"trusted": bool, "pending": [{id, summary}], "allowed": [id…], "denied": [id…], "notes": […]}`（`trusted` 为 `safety.TrustedRoot(launch) != ""`）；同意体 `{"allow": ["gopls"]}` → `process.DecideProjectLSPConsents`；成功返回 `{"ok": true, "allowed": [...]}`。gateway 不能导入 `pkg/lsp`（fan-out 已满）：预览数据通过在 `pkg/process/mcp.go` 再加一个 `InspectProjectLSP(agentWorkspace string, launch safety.ProjectContext) (trusted bool, pending []lsp.PendingProjectServer, allowed, denied, notes []string)` 取得；gateway 只读取 `pending` 元素的 `ID`、`Summary` 字段、不写出类型名，Go 允许这样使用而不导入 `lsp`。
- `ProjectDetailView.vue`：在 MCP 区块之后加「语言服务器」区块，结构与 MCP 区块相同（待确认列表 + 允许 / 拒绝按钮、已允许与已拒绝列表、说明行）；`api.ts` 加 `projectLsp(id)` 与 `projectLspConsent(id, allow)`；`locales` 中英文加对应键。

**验证**：`cd frontend && pnpm install --frozen-lockfile && pnpm test && pnpm build` → 退出码 0；然后 `git checkout -- pkg/gateway/dist`。

### 步骤 6：全量检查

**验证**：`gofmt`、`go vet ./...` 干净；`ls pkg/lsp/*.go | grep -v _test.go | wc -l` 比任务开始时多 1（任务 09 已合入时为 17，本任务可以在 09 之前执行）；`scripts/package-graph.sh` 后提交 `graph.json`（`lsp` 的 fan-out 仍为 4）；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`；`git status --short pkg/gateway/dist` 无输出。

## 测试计划

- `pkg/lsp/project_test.go`
  - `TestLoadProjectServers`：正常文件；缺文件（无条目无说明）；明文密钥（无条目 + 说明逐字）；非法 id（无条目 + 校验错误）；`env_passthrough` 被清零并有说明。
  - `TestProjectServerFingerprint`：字段顺序与 map 键顺序不同的两个等价条目指纹相同；改 `args` 或 `settings` 后不同；改 `priority` 不变。
  - `TestProjectConsentStore`：往返；指纹变化后 `Decision` 为未决定；非法 JSON 视为空。
  - `TestResolveProjectServers`：`trusted == false` → 零值且不读文件（把文件设成不可读也不报错）；allow/deny/pending 三种分类。
  - `TestDecideProjectServers`：允许一个、其余待决条目记为 deny。
- `pkg/lsp/manager_test.go`：`TestProjectEntryAppliesAfterConsent`（项目条目覆盖 `fake` 的 `args`：同意前实例用全局 args，同意后下一次 `servers()` 用项目 args 且 `Scope == "project"`、指纹变化导致旧实例在下一次对账时关闭）；`TestProjectEntryEnvFromDotEnvOnly`（项目条目 `env: {X: "${SECRET_FROM_ENV}"}`：进程环境里有、`.env` 里没有 → 假服务器的 `echo_env` 看到空值）。
- `pkg/lsp/control_test.go`：`TestSnapshotShowsPendingProjectEntry`（新服务器 → `blocked` 行；覆盖目录服务器 → 该行 `Note` 以 `project settings await confirmation` 开头）；`TestSnapshotProjectNotes`。
- `pkg/lsp/resolve_test.go`：`TestToolEnabledCountsConsentedProjectPrimary`。
- `pkg/process/mcp_test.go`：`TestProjectLSPConsentsNeedTrust`（未受信任 → 无待决、决定为 no-op）。
- `cmd/forebrain/interactive_test.go`：`TestInteractiveAsksProjectLSPConsentAfterMCP`（替换两个变量，断言调用顺序）；`mcp_consent` 的 LSP 询问：输入 `y\nn\n` → 第一条允许、第二条拒绝，输出文案逐字。
- `pkg/gateway/api_extra_test.go`：预览与同意接口（项目不存在 404；同意后预览里该 id 进入 `allowed`）。
- 前端：`ProjectDetailView` 已有测试文件时补一例（待确认条目渲染、点击允许调用 `projectLspConsent`）；没有测试文件时新建 `ProjectDetailView.test.ts` 只测新区块。

## 完成判据

- [ ] 上述测试全部通过；`pnpm test`、`pnpm build` 通过且 `pkg/gateway/dist` 无改动
- [ ] `pkg/lsp` 生产文件比任务开始时多 1、且不超过 17；`lsp` 的 fan-out 仍为 4（`graph.json`）
- [ ] 手动验证（写进 PR 描述）：在一个受信任的 git 仓库放 `.forebrain/lsp_servers.yaml`，启动 `forebrain` → 在 MCP 询问之后出现语言服务器询问；拒绝后 `/lsp` 显示 `blocked` 与说明
- [ ] README 中任务 15 状态为 `DONE`

## STOP 条件

- 需要让 `pkg/lsp` 导入 `pkg/mcp`、`pkg/memory` 或 `pkg/safety`。
- 需要在未受信任或未受版本控制的项目里读取项目文件。
- 需要在 `pkg/config` 新建文件。

## 维护说明

- 指纹覆盖的字段与规范 §5.2 绑定：新增能影响「执行什么」的字段时，同时加进指纹，并在 PR 里说明这会让已同意的条目重新询问。
- 与 MCP 的同意存储结构相同但文件不同（`state/lsp/project_consent.json`）；不要合并成一个文件。
- 评审重点：项目条目的 `${VAR}` 只从 `~/.forebrain/.env` 展开（任务 05 的 `BuildEnv` 已按 `FromProject` 处理，本任务要确认合并后 `EnvFromProject` 为 true）。
