# 计划 006：定时任务对话的保留期——默认 30 天，forebrain.yaml 与网页都能配置

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
> **开始前确认计划 004、005 已 DONE**。本计划依赖：
> - 005：触发会话出生即 `source='cron'`，`fb_cron_runs.session_id` 有索引，迁移版本 6；
> - 004：`RunStore.SessionHasLiveRun` 背后的"活着的主运行"SQL 片段。
>
> **漂移检查（先运行）**：
> `git diff --stat 1a6d708..HEAD -- pkg/config/config.go pkg/config/load.go pkg/state/session_store.go pkg/state/cron.go pkg/state/mode.go pkg/state/file_service.go pkg/hook/dispatch.go pkg/run/fork.go pkg/process/cron_service.go pkg/turn/scheduler.go pkg/gateway/api_extra.go frontend/src/views/SettingsView.vue frontend/src/views/CronView.vue frontend/src/lib/api.ts frontend/src/locales/index.ts`
> 计划 001–005 和并行的 LSP 计划（`pkg/config/config.go`、`load.go`）改过其中一部分，这是预期的。按内容核对"现状"摘录，不按行号。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED（这是产品里第一个删除会话的功能，删错了不可恢复）
- **依赖**：计划 004、005
- **类别**：direction / bug
- **基线**：提交 `1a6d708`，2026-10-04
- **决策**：D9（owner 2026-10-03/04）——"要设置保留上限，默认30天，必须支持在forebrain.yaml里可配置"；"web端也必须支持配置"

## 为什么要做

005 之后，定时任务每触发一次就多一段完整的对话，不再只是一行文本。一个每小时触发的任务一年会攒下约 8700 段对话，每段带着它的消息、运行、事件、溢出的工具输出和上传文件。今天这些东西没有任何上限。
本计划给"定时任务的对话和执行记录"设一个保留期：默认 30 天，可以在 `forebrain.yaml` 里配置，也可以在网页设置页里配置。

到期判断：一次触发的对话**最后一次活动**超过保留期，且它的运行已不再活着，就把对话连同它在库里和磁盘上的一切、以及执行记录一起删除。用户在一段触发对话里继续聊，"最后一次活动"随之推后，这段对话就不会在他眼前被删掉。

这是产品里第一个真正删除会话的功能（`grep -rn "DELETE FROM fb_sessions" pkg` 在基线没有结果）。所以本计划把"删除一个会话"做成一件完整的事：数据库里的行由外键级联删除；磁盘上散落在各处的会话文件、溢出的工具输出、上传文件的实体，逐一列清并删除。

## 现状

### 配置（基线）

- `pkg/config/config.go` 的 `type Root struct`：没有任何定时任务相关的段。段的写法照抄现有的，例如 `Compact CompactSection \`yaml:"compact,omitempty" json:"compact,omitempty"\``；可选值用指针表示"未设置"，例如 `FeaturesSection` 里的 `Memories *bool`，生效值由 `EffectiveFeatures()` 给出默认。
- `pkg/config/load.go` 的 `validateLoadedRoot(r *Root) error` 是唯一的校验入口：`Load`（启动与热加载）和 `ParseRootYAML`（网页的 YAML 编辑器 `PUT /api/config`）都走它。可导出、可被结构化接口复用的校验函数有先例：`ValidateHooksSettings`，在 `validateLoadedRoot` 和 `handleHooks` 里都被调用。
- 热加载：`pkg/process/config_reload.go` 的 `reloadConfig` 把 `env.Deps.AppCfg` 换成新值；gateway 用 `s.liveCfg()` 读它。
- `pkg/config` 现在是 20 个生产文件（加上 LSP 计划新增的 `lsp.go`），**不能新建文件**，新段写进 `config.go`，校验写进 `load.go`。

### 网页设置（基线）

- gateway 结构化设置接口的范本是 `pkg/gateway/api_extra.go` 的 `handleHooks`：GET 用 `s.persistedConfig()` 读；PUT 先校验，再改 `cfg` 的对应段，然后 `s.saveAndReload(cfg)`，返回 `configWriteResult{Path, Applied: true}`。
- `frontend/src/views/SettingsView.vue`：标签页数组（`appearance`、`approval`、`agents`、`mcp`、`hooks`、`memory`、`config`、`runtime`、`shared-skills`），由 `activeTab = ref('appearance')` 控制，**不读路由参数**。单个开关类设置的组件范本是 `frontend/src/components/settings/MemorySwitchesTab.vue`（区块样式、保存按钮、错误与提示条）。
- 全局设置只放在设置页（项目记忆 web-scope-boundaries：主代理维度的页面只放该主代理的数据，全局的放设置页）。保留期是安装级配置，属于全局。

### 会话的数据（基线）

- 数据库：引用 `fb_sessions(id)` 的表全部 `ON DELETE CASCADE`，包括 `fb_messages`、`fb_session_ui_state`、`fb_session_model_state`、`fb_session_prompt_state`、`fb_actions`、`fb_runs`（以及经由它的 `fb_run_waits`）、`fb_session_events`、`fb_files`、记忆的两张表、`fb_heartbeats`。只有 `parent_session_id` 是 `ON DELETE SET NULL`：从这段对话 `/fork` 出来的会话是用户自己的对话，父会话删了它照样留着，这正是要的语义。连接串里开了 `_foreign_keys=1`（`pkg/state/db.go`）。
- `fb_cron_runs` 不带外键（表注释写的是"刻意比任务和会话活得久"），要单独删。
- 磁盘上按会话 id 命名的文件，全部在**所属主代理的状态根**之下（状态根即 `config.ActiveStateRoot` / `Resolver.All()` 里各主代理 `Summary.WorkspaceRoot`）：
  - `pkg/state/mode.go`：`state/modes/<sid>.json`（`modePath`）、`state/fast/<sid>.json`（`fastPath`）
  - `pkg/state/todo.go`：`state/todos/<sid>.json`（`todoPath`）
  - `pkg/state/intermediate.go`：`state/intermediate/<sid>.md`（`intermediatePath`）
  - `pkg/hook/dispatch.go`：`state/hook-transcripts/<sanitize(sid)>.jsonl`（`WriteSessionTranscriptArtifact`），`state/fork-sidechain/<sanitize(sid)>/subagent-<agent>.jsonl`（`SidechainTranscriptPath`）
  - `pkg/run/fork.go`：`state/fork-sidechain/<sanitize(sid)>/<fork>-<run>.jsonl`（`SidechainFilePath`）。它和 hook 用的是同一个目录，但各写了一个 `sanitizePathSegment`：两者对非空输入完全一致，只在空串时一个返回 `"x"`、一个返回 `"default"`。"一个会话的旁路记录目录"因此有两份定义。
- 溢出的工具输出：`pkg/tool/shell_output.go` 的 `OutputGovernor.spill` 写到 `<runner 状态目录>/tool-outputs/<tool>-<callID>-<ms>.txt`（`tool.SpillDir = "tool-outputs"`），路径记在工具元数据的 `full_path` 里（`GovernedOutput` 的 meta），随工具行持久化在 `fb_messages.tool_meta_json`。
- 上传文件：`fb_files` 的行只记位置。实体在本地（`FileStore.LocalPath`、`ParsedTextLocalPath`）或 S3（`storage_bucket`/`storage_key`，`FileStore.s3Client`）。`FileStore` 没有删除实体的方法。

### 调度（005 后）

`pkg/turn/scheduler.go` 的 `RunDue` 依次是 `SettleFires` → `runDueJobs` → `runDueHeartbeats`。`pkg/process/cron_service.go` 的 `Bind` 装配 `SchedulerDeps`。`CronStore`（`pkg/state/cron.go`）有 `OpenFires`、`LatestOpenFire` 等。

## 设计

### 1. 配置（`pkg/config/config.go`、`load.go`）

```yaml
# forebrain.yaml
cron:
  # 定时任务每次运行的对话和执行记录，最后一次活动超过这么多天后自动删除。
  retention_days: 30
```

- `Root` 新增 `Cron CronSection \`yaml:"cron,omitempty" json:"cron,omitempty"\``。

  ```go
  // CronSection configures scheduled tasks for the whole install.
  type CronSection struct {
  	// RetentionDays is how long a scheduled run's conversation and history
  	// record are kept after their last activity. Unset means the default.
  	RetentionDays *int `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`
  }

  const (
  	DefaultCronRetentionDays = 30
  	MinCronRetentionDays     = 1
  	MaxCronRetentionDays     = 3650
  )

  // CronRetentionDays is the retention in force: the configured value, or the
  // default when none is set.
  func (r *Root) CronRetentionDays() int
  ```

- `load.go`：新增导出的 `ValidateCronSection(c CronSection) error`，不在范围内时返回 `cron.retention_days must be between 1 and 3650`；在 `validateLoadedRoot` 里调用它。于是启动、热加载、YAML 编辑器、结构化接口四条路径的校验是同一条。

### 2. 删除一组会话（`pkg/state`）

- `pkg/state/session_store.go`：

  ```go
  // SessionLeftovers is what deleting sessions leaves outside the database:
  // the uploaded files' stored bytes and the spilled tool outputs their
  // transcripts point at. The caller removes them once the rows are gone.
  type SessionLeftovers struct {
  	Files      []File
  	SpillPaths []string
  }

  // DeleteSessions deletes this agent's sessions with the given ids and every
  // row that hangs off them (the foreign keys cascade), in one transaction,
  // and returns what they leave on disk. A session another agent owns is not
  // touched. A conversation forked from one of them keeps its own life: its
  // parent link is cleared, not followed.
  func (s *SessionStore) DeleteSessions(ctx context.Context, ids []string) (SessionLeftovers, error)
  ```

  事务内的三步：
  1. 读出这些会话的 `fb_files` 行；
  2. 读出这些会话 `fb_messages` 里非空的 `json_extract(tool_meta_json, '$.full_path')`，去重；
  3. `DELETE FROM fb_sessions WHERE agent_id = ? AND id IN (...)`。

  `ids` 按 `sessionLastActiveChunk`（400）分批，和已有的批量查询一样。
- `pkg/state/mode.go`：新增 `RemoveSessionStateFiles(stateRoot, sessionID string) error`，删除 `modePath`、`fastPath`、`todoPath`、`intermediatePath` 四个文件。文件不存在不算错误。把"一个会话在状态根下有哪些文件"写进函数注释。
- `pkg/state/file_service.go`：新增 `func (s *FileStore) RemoveStored(ctx context.Context, f File) error`。本地后端删除原件和解析文本（`LocalPath`、`ParsedTextLocalPath`）；S3 后端用已有的 `s3Client` 调 `DeleteObject(bucket, key)`。不存在不算错误。

### 3. 旁路记录目录只定义一次（`pkg/hook/dispatch.go`、`pkg/run/fork.go`）

- `pkg/hook/dispatch.go` 新增：
  - `SessionSidechainDir(workspaceRoot, sessionID string) string`，即 `<root>/state/fork-sidechain/<sanitize(sid)>`；
  - `SessionTranscriptPath(workspaceRoot, sessionID string) string`，从 `WriteSessionTranscriptArtifact` 里抽出来，即 `<root>/state/hook-transcripts/<sanitize(sid)>.jsonl`；
  - `RemoveSessionArtifacts(workspaceRoot, sessionID string) error`，删除前一个目录（`os.RemoveAll`）和后一个文件。

  `SidechainTranscriptPath`、`WriteSessionTranscriptArtifact` 改为调用这两个路径函数。
- `pkg/run/fork.go` 的 `SidechainFilePath` 改为 `filepath.Join(hook.SessionSidechainDir(workspaceRoot, sessionID), fl+"-"+rk+".jsonl")`。`run.sanitizePathSegment` 若仍被 `fl`/`rk` 使用就保留，只是不再参与会话目录名。理由：同一个目录只能有一个定义，删除时才不会漏掉另一份写法写出来的目录。`pkg/run` 已经 import `pkg/hook`，不新增依赖。

### 4. 到期的触发（`pkg/state/cron.go`）

```go
// ExpiredFire is one fire past the retention: its record, and its session
// when that session is still a scheduled-task conversation.
type ExpiredFire struct {
	RecordID  int64
	AgentID   string
	SessionID string
	// CronSession is false when the record's session is gone, or is not a
	// scheduled-task conversation — then only the record is deleted.
	CronSession bool
}

// ExpiredFires lists every fire, across all agents, whose conversation has
// been quiet since before cutoff and whose run is no longer alive.
func (s *CronStore) ExpiredFires(ctx context.Context, cutoff time.Time) ([]ExpiredFire, error)

func (s *CronStore) DeleteFireRecords(ctx context.Context, ids []int64) error
```

`ExpiredFires` 的条件：

- `r.status <> 'running'`；
- `COALESCE(s.updated_at, r.finished_at, r.started_at) < cutoff`（会话还在就按会话的最后活动；会话已不在就按执行记录的时间）；
- 会话里没有活着的主运行（复用 004 的 SQL 片段）。

`CronSession = (s.source = 'cron')`，用常量 `SessionSourceCron` 绑定。**任何非 `cron` 的会话都不会被删除**，即使某条执行记录指向它。

### 5. 清理器（`pkg/process/cron_service.go`）

```go
// cronRetentionSweepEvery is how often expired fires are looked for. The
// retention is counted in days, so an hour of slack costs nothing.
const cronRetentionSweepEvery = time.Hour

// pruneExpiredFires deletes every scheduled-task fire that has been quiet
// longer than the configured retention: the conversation with everything it
// left in the database and on disk, then its history record. Retention is a
// property of the install, so it covers every agent's fires, not only the
// bound agent's.
func (c *CronService) pruneExpiredFires(ctx context.Context, now time.Time) error
```

步骤：

1. 用**当前**配置（`c.env.Deps.AppCfg`，热加载会替换它）取 `CronRetentionDays()`，`cutoff := now.Add(-days * 24h)`。
2. `ExpiredFires(cutoff)`。
3. 用 `config.NewResolver(c.env.Root, cfg).All()` 得到各主代理的状态根。
4. 按 `AgentID` 分组。对 `CronSession` 的那些调 `state.NewSessionStore(c.env.SQL, agentID).DeleteSessions(ctx, ids)`，然后清理磁盘：
   - 每个会话 `state.RemoveSessionStateFiles(root, sid)`、`hook.RemoveSessionArtifacts(root, sid)`；
   - 每个 `SpillPaths` 中的路径，只有 `filepath.Base(filepath.Dir(p)) == tool.SpillDir` 且是普通文件时才删（库里的路径不经核对不得删除）；
   - 每个 `Files` 用 `c.env.Files.RemoveStored`。

   主代理已不在配置里时，库里的行照删，磁盘清理跳过并记一条日志。磁盘清理单个失败只记日志、继续。
5. 最后 `DeleteFireRecords`，删掉全部到期记录（含 `CronSession == false` 的）。

调度接线：`SchedulerDeps` 新增 `Maintain func(ctx context.Context, now time.Time)`，`RunDue` 在 `SettleFires` 之后调用它。`CronService` 的实现记下上次清理时间（受 `c.mu` 保护）：从未清理过、或距上次已满 `cronRetentionSweepEvery` 时，才调用 `pruneExpiredFires`。所以 gateway 启动或切换主代理后的第一跳就会清理一次。

### 6. gateway 接口（`pkg/gateway/api_extra.go`）

```go
// handleCronSettings reads and writes the install's scheduled-task settings —
// today the conversation retention. It is global configuration, so it is
// served apart from the agent-scoped /cron routes.
func (s *Server) handleCronSettings(w http.ResponseWriter, r *http.Request)
```

- 路由：`api.Get("/cron-settings", ...)`、`api.Put("/cron-settings", ...)`。不要挂在 `/cron` 组下，避免和 `GET /cron/:id` 冲突。
- GET 返回：`{"retention_days": 生效值, "configured": 是否在文件里设置过, "default_days": 30, "min_days": 1, "max_days": 3650}`。
- PUT 接收 `{"retention_days": n}` 或 `{"retention_days": null}`（null 表示删除这个键，回到默认）。流程照抄 `handleHooks`：`config.ValidateCronSection` → `persistedConfig` → 改 `cfg.Cron` → `saveAndReload`，返回 `configWriteResult`。

### 7. 网页

- `frontend/src/lib/api.ts`：`CronSettings` 类型，以及 `cronSettings()`、`saveCronSettings(retentionDays: number | null)`。
- 新建 `frontend/src/components/settings/CronSettingsTab.vue`（样式照抄 `MemorySwitchesTab.vue`），内容：
  - 一个区块，标题 `cronSettings.retentionTitle`，说明 `cronSettings.retentionDescription`；
  - 一个数字输入框（`min`/`max` 取自 GET，步长 1，`data-testid="cron-retention-days"`），没设置过时显示默认值并带提示 `cronSettings.defaultHint`；
  - "保存"（`data-testid="cron-retention-save"`）和"恢复默认"（`data-testid="cron-retention-reset"`）。

  前端先按 GET 给的范围校验，超出时显示本地化的 `cronSettings.outOfRange`，不发请求；服务端的错误只作兜底。
- `SettingsView.vue`：标签页加 `{ key: 'cron', label: t('settings.tabCron') }`。另外让设置页读取路由参数 `?tab=<key>` 作为初始标签、切换标签时同步回路由，这样别的页面可以直接链到某个标签。
- `CronView.vue`（主代理页和项目空间标签共用）：页头下加一行 `cron.retentionNote`（"执行记录和对话保留 {days} 天。"），旁边链接 `cron.retentionEdit` 指向 `/settings?tab=cron`。
- `frontend/src/locales/index.ts`，中英文：

  | 键 | 中文 | 英文 |
  | --- | --- | --- |
  | `settings.tabCron` | 定时任务 | Scheduled tasks |
  | `cronSettings.retentionTitle` | 对话保留天数 | Days to keep conversations |
  | `cronSettings.retentionDescription` | 定时任务每次运行的对话和执行记录，在最后一次活动超过这么多天后自动删除。 | Each scheduled run's conversation and history record is deleted once it has been inactive for this many days. |
  | `cronSettings.defaultHint` | 未设置时为 {days} 天。 | {days} days when not set. |
  | `cronSettings.outOfRange` | 请输入 {min} 到 {max} 之间的整数。 | Enter a whole number from {min} to {max}. |
  | `cronSettings.reset` | 恢复默认 | Restore default |
  | `cronSettings.saved` | 已保存，下一次清理时生效。 | Saved; it applies from the next cleanup. |
  | `cron.retentionNote` | 执行记录和对话保留 {days} 天。 | History and conversations are kept for {days} days. |
  | `cron.retentionEdit` | 修改 | Change |

## 缓存影响

无。只删除已结束、已到期的会话，不触及任何正在进行的会话的请求内容。

## 需要的命令

见 README"常用命令"。

## 范围

**要改的文件**：

- `pkg/config/config.go`、`pkg/config/load.go` 及对应测试
- `pkg/state/session_store.go`、`pkg/state/session_store_test.go`、`pkg/state/mode.go`、`pkg/state/mode_test.go`（不存在就新建，名字对应 `mode.go`）、`pkg/state/file_service.go` 及其测试、`pkg/state/cron.go`、`pkg/state/cron_test.go`
- `pkg/hook/dispatch.go`，新建 `pkg/hook/dispatch_test.go`（基线没有这个文件；名字对应 `dispatch.go`）；`pkg/run/fork.go`、`pkg/run/fork_test.go`
- `pkg/turn/scheduler.go`、`pkg/turn/scheduler_test.go`
- `pkg/process/cron_service.go`、`pkg/process/cron_service_test.go`
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `frontend/src/lib/api.ts`、新建 `frontend/src/components/settings/CronSettingsTab.vue` 与 `CronSettingsTab.test.ts`、`frontend/src/views/SettingsView.vue`、`frontend/src/views/CronView.vue`、`frontend/src/locales/index.ts`、新建 `frontend/e2e/cron-retention.spec.ts`
- `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md`（状态行）

**不要碰**：

- 普通对话、工坊任务、项目对话的生命周期：本计划只删 `source='cron'` 的会话。
- 删除定时任务时的行为（D5 = A）：保留期之内，任务删了它的历史仍在；过了保留期按本计划删除。
- `pkg/config` 里属于 LSP 计划的改动。
- 运行存活规则（004）。

## 步骤

### 第 0 步：前置与基线

确认 004、005 是 DONE。运行 README 常用命令里的 Go 全量测试和 `deadcode`，记下基线。

**验证**：全部 `ok`。

### 第 1 步：配置

按设计 §1 改。测试（放进覆盖 `config.go`、`load.go` 的现有测试文件）：

- 未设置时 `CronRetentionDays() == 30`；设置 7 时为 7。
- `ParseRootYAML` 对 `cron: {retention_days: 0}`、`3651` 返回带 `cron.retention_days` 的错误；对 `1`、`3650` 成功。
- `Save` 后重新 `Load`，值不变；未设置时文件里不出现 `cron:` 段。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config/ -count=1` → `ok`。

### 第 2 步：删除会话与它的遗留物

按设计 §2、§3 改。测试：

- `pkg/state/session_store_test.go` 的 `TestDeleteSessionsCascadesAndReportsLeftovers`：一个会话带消息（其中一条工具行的 `tool_meta_json` 有 `full_path`）、运行、事件、上传文件行、心跳；另有一个从它 fork 出来的会话、一个别的主代理的会话。删除后：
  - 前者所有相关表都没有行；
  - fork 会话还在，`parent_session_id` 为 NULL；
  - 别的主代理的会话不受影响；
  - 返回的 `Files` 和 `SpillPaths` 正好是那一个文件、那一条路径。
- `pkg/state/mode_test.go`：`RemoveSessionStateFiles` 删除四个文件；文件不存在时不报错。
- `pkg/state/file_service.go` 的测试：本地后端 `RemoveStored` 删除原件和解析文本。S3 后端用 `httptest.Server` 充当 S3 端点（`FileStore.Cfg` 指向它），断言收到 `DELETE /<bucket>/<key>`。
- `pkg/hook`：`RemoveSessionArtifacts` 删除旁路目录和 hook 转写文件；`SidechainTranscriptPath` 的结果仍在 `SessionSidechainDir` 之下。
- `pkg/run`：`SidechainFilePath` 的结果在 `hook.SessionSidechainDir` 之下，对非空会话 id 与改动前完全相同（写一个对比用例，钉住几个典型 id）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/hook/ ./pkg/run/ -count=1` → `ok`。

### 第 3 步：到期判断

按设计 §4 改。`pkg/state/cron_test.go` 的 `TestExpiredFires`，覆盖：

- 会话最后活动早于 cutoff → 到期；
- 晚于 cutoff → 不到期（用户刚在里面聊过）；
- 记录 `running` → 不到期；
- 会话里有活着的主运行 → 不到期（用 004 的方式造一个属主新鲜的运行）；
- 会话已不存在、记录时间早于 cutoff → 到期且 `CronSession == false`；
- 记录指向一个 `source=''` 的会话 → 到期但 `CronSession == false`；
- 两个主代理的到期记录都返回。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'ExpiredFires' -count=1 -v` → PASS。

### 第 4 步：清理器

按设计 §5 改。`pkg/process/cron_service_test.go` 的 `TestPruneExpiredFiresRemovesEverything`：用临时目录作 FOREBRAIN_HOME，配置两个主代理，各有一次到期触发和一次未到期触发；到期会话在各自状态根下有 modes/todos/intermediate/hook-transcripts/fork-sidechain 文件，有一个溢出文件在 `tool-outputs/` 下，另有一个 `full_path` 指向 `tool-outputs/` 之外的文件。断言：

- 到期的会话、记录、上述文件全部删除；
- `tool-outputs/` 之外的那个文件**还在**；
- 未到期的一切都在。

另外两个测试：

- 节流：`Maintain` 连调两次（间隔小于 1 小时）只清理一次；
- 热加载：改 `env.Deps.AppCfg` 的保留天数后，下一次清理用的是新值。

在 `pkg/turn/scheduler_test.go` 加：`RunDue` 调用了 `Maintain`，且在 `SettleFires` 之后。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ ./pkg/turn/ -count=1` → `ok`。

### 第 5 步：gateway 接口

按设计 §6 改。`pkg/gateway/api_extra_test.go`（参照该文件里 `cronTestServer` 的夹具，加上配置文件路径）：

- GET 在未设置时返回 `retention_days: 30, configured: false`；
- PUT 7 后文件里出现 `cron:` 段和 `retention_days: 7`，`s.liveCfg().CronRetentionDays() == 7`，GET 返回 `configured: true`；
- PUT 0 返回 400，文件不变；
- PUT null 后文件里没有 `retention_days`，GET 回到 30；
- 通过 `PUT /api/config` 写入 `retention_days: 0` 也被拒绝（证明 YAML 编辑器走同一条校验）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`。

### 第 6 步：网页

按设计 §7 改。`CronSettingsTab.test.ts`：

- 加载后显示 GET 的值；
- 输入 0 时显示 `outOfRange` 文案且不发请求；
- 保存调用 `saveCronSettings(7)`；
- "恢复默认"调用 `saveCronSettings(null)` 并显示默认值。

`SettingsView` 读取 `?tab=cron` 后，打开的就是定时任务标签。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` 退出码 0。

### 第 7 步：真机验证

1. **e2e（假模型）**：新建 `frontend/e2e/cron-retention.spec.ts`：
   - 打开 `/settings?tab=cron`，看到默认 30；
   - 改成 7 并保存，刷新后仍是 7；
   - 读取 `$E2E_HOME/forebrain.yaml`（Node 的 `fs`），含 `retention_days: 7`；
   - `/cron` 页显示"保留 7 天"，点"修改"回到设置页的定时任务标签；
   - 恢复默认后，文件里没有 `retention_days`。

   运行 `scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
2. **清理真机（智谱）**：隔离的 `FOREBRAIN_HOME`，配置照抄 `scripts/acceptance/web_e2e.sh:78-98` 的真模型块。起 gateway，建一个任务并立即执行两次，等两次都"成功"。用 `sqlite3` 把第一次触发的会话 `updated_at` 和记录的 `started_at`/`finished_at` 改到 40 天前，记下它的会话 id，并在它的状态根下确认有对应文件（例如 `state/modes/<id>.json`；没有就手工放一个空文件，用来验证会被删除）。重启 gateway（第一跳就会清理），30 秒后检查：
   - 第一次触发的会话、消息、运行、执行记录都没了，状态根下它的文件也没了；
   - 第二次触发的一切都还在；
   - 网页的执行记录里只剩第二次。
3. **配置热生效**：在网页把保留期改为 1 天，**不重启**。`curl` 带登录凭据请求 `GET /api/cron-settings`，立即返回 `retention_days: 1`；`forebrain.yaml` 里是 `retention_days: 1`。清理读取热加载后的值，这一点由第 4 步的测试钉住，真机不必等一个清理周期。

**验证**：e2e PASS；第 2、3 项的查询结果写进"执行记录"。

## 完成标准（全部满足）

- [ ] README 常用命令里的每一条都达到成功标志
- [ ] `forebrain.yaml` 的 `cron.retention_days` 生效，四条写入路径用同一个校验
- [ ] 第 1–6 步列出的测试存在且通过
- [ ] e2e PASS；真机清理和热生效的证据写进"执行记录"
- [ ] `grep -rn '"fork-sidechain"' pkg --include='*.go' | grep -v _test` 只剩 `pkg/hook/dispatch.go` 一处
- [ ] `deadcode` 输出与基线一致
- [ ] `git status` 里只有"范围"列出的文件有改动
- [ ] README 状态行已更新

## STOP 条件

- 004、005 没有 DONE。
- 发现有引用 `fb_sessions(id)` 的表**没有** `ON DELETE CASCADE`（`parent_session_id` 的 `SET NULL` 除外）。
- 发现还有别的"按会话 id 命名"的磁盘文件不在设计 §2/§3 的清单里（`grep -rn 'sessionID+\|sanitizePathSegment(sessionID)\|sanitizePathSegment(sid)' pkg --include='*.go'` 逐个核对）。
- 工具元数据里不记录溢出路径（`full_path`），以致无法从库里找到一段对话的溢出文件。
- `FileStore.Cfg` 不支持把 S3 端点指向测试服务器，无法测试 S3 删除。
- `run.SidechainFilePath` 改为基于 `hook.SessionSidechainDir` 后，对某个非空会话 id 的结果与改动前不同。
- 某一步的验证在一次合理修正后仍失败。

## 维护说明

- **删除会话**目前只有这一个调用方。将来如果要给普通对话做"删除"，复用 `DeleteSessions` + `RemoveSessionStateFiles` + `hook.RemoveSessionArtifacts` + 溢出与上传清理这一整套，不要另写。新增任何"按会话 id 命名的磁盘文件"时，必须同时加进 `RemoveSessionStateFiles` 或 `RemoveSessionArtifacts`，否则删除会话会留下孤儿文件。
- 保留期按**最后一次活动**计算，所以用户在触发对话里继续聊会让它多留。如果 owner 以后要求"按触发时间严格计算"，只需改 `ExpiredFires` 里的时间表达式。
- 溢出文件只在路径位于 `tool-outputs/` 目录下时才删除，这是对库中路径的必要核对，不要去掉。

## 执行记录

（执行者在此记录：真机清理前后的查询结果、热生效的验证结果、e2e 结果。）
