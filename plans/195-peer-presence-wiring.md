# 195 — 进程接线：每个 TUI / gateway 进程登记成对端（状态、扩展块、两种登记模式与切换、`peers` 配置段与开关的显示）

- **基线**：`202ed4f2`（dev）+ 194 落地后的工作区。工作区另有并行会话的未提交改动（只归因，不回滚）。
- **状态**：TODO
- **依赖**：194（`pkg/peer` 协议库）
- **执行隔离**：in-tree，**零 commit**
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` §2、§4、§7

> **执行者须知**：逐步执行，每步跑验证；碰到 §9 STOP 条件就停下报告。完成后更新 `plans/README.md` 本行状态。
>
> **Drift check（2026-11-01 已由评审者对现场刷新过 §2/§3.7 锚点**：191 重写了配置文件接口、207 把导入改标记驱动、203–207 把段表删到 10 项、§9 末尾 always-on 裁决覆盖 §3.1/§3.2 的部分字段——按更新后的锚点执行即可）。执行前确认：`ls pkg/peer/doc.go && CGO_ENABLED=1 go test -tags fts5 ./pkg/peer -count=1`（194 已落地）；`grep -n "stopAbandonedRunReaper" pkg/tui/notify.go` 与 `grep -n "stopReaper" pkg/gateway/serve_run.go` 仍在。

## 0. 背景与 owner 裁决（全族共用，执行期不得再改）

多个 forebrain TUI / gateway 在同一工作树并行干活却互相看不见。owner 2026-10-30 裁决：按工作树、跨 home 协同；空闲收件方自动唤醒；同树提醒 + 危险 git 审批；**线协议与 Claude Code 互通**；网关按发送方工作树路由；**只在 `~/.claude/sessions` 已存在时往里写，否则用 forebrain 自己的目录，读两处**；工具 forebrain 风格命名；防乒乓只用 Claude Code 那套；**装上/卸载 Claude Code 后登记运行中自动跟进、永不创建 Claude Code 目录**。

**同日追加裁决（改写上面与 Claude Code 有关各条的适用范围）**：与 Claude Code 互通是**全局开关，默认关闭，用户手动打开**；开关**加在状态库 `fb_config` 表里、只读写数据库，不通过 forebrain.yaml 配置**。关闭时只登记、只读取 forebrain 自己的目录（FB 目录），不碰 `~/.claude/sessions` 与 `cc-socks`，只和其他 forebrain 进程通信。

**同日再追加裁决（自闭环）**：forebrain 跨进程通信必须自闭环，**不依赖 Claude Code 或其他任何进程**——每个进程自己监听一个 socket、按登记点对点直连，没有中心进程，不调用外部命令（本计划的分支名改为直接读 `.git/HEAD`）。

**同日第三条追加裁决（取代上面「读两处」与早先的「桥」方案）**：**检测到本机装了 Claude Code 才在 TUI 与网页显示这个开关**，没装就不显示（库里已存的值保留，但不生效）；**用户打开后 forebrain 只登记、只读取 `~/.claude/sessions`（socket 放 `cc-socks`），不再碰自己的登记目录**。落到本计划：一个进程任何时刻只处在一种登记模式——**FB 模式**（默认：FB 目录 + `fb-socks`）或 **CC 模式**（开关打开且检测到 Claude Code：CC 目录 + `cc-socks`）；目标模式变化时整体切换。检测＝CC 目录存在（只 stat）。开关不同的进程互相看不见，这是裁决的直接结果，不要设法绕开（设计 §7）。

本计划：让每个 forebrain 进程（TUI、gateway）启动时按目标模式绑 socket、写 key 与登记记录（含 forebrain 扩展块）、持续更新状态；开关或 CC 目录变化时整体切换模式；退出时清理；新增只存数据库的 `peers` 配置段，并让 TUI `/config` 与网页知道什么时候显示开关。**收到消息的处理只放一个占位**：一律回「拒收」回执（196 替换）。

## 1. 共享架构（摘要）

- `pkg/peer`（194）：协议库；**只有 `pkg/process` 可以 import**（`pkg/tool`/`pkg/run`/`pkg/tui`/`pkg/gateway` 扇出预算已满，见 §2）。
- `pkg/process/peers.go`（本计划新建）：进程级 `PeerService`，TUI 与 gateway **共用**；表面只提供少量回调（当前会话、事件发布、唤醒——后两者 196 用）。
- 记录取值（设计 §4.3）：`kind:"interactive"`；`entrypoint:"forebrain-tui"`/`"forebrain-gateway"`；`version:"forebrain <VERSION>"`；`peerProtocol:1`；`peerFeatures:["reply_across_default_dirs"]`；`nameSource:"derived"`。
- 扩展块（设计 §4.1）：`forebrain.agents[]` 列出本进程可寻址的主代理与正在跑的子代理。

## 2. 现状事实（已对基线核实）

- 进程组装：`pkg/process/open.go:428` `runSvc := &state.RunStore{DB: sqlDB, Owner: source + "-" + uuid.NewString()}`；`Environment.Close()` 在 `open.go:146`。
- 每进程常驻循环的先例（照它的形状写）：`turn.AbandonedRunReaper{…}.Start(ctx)` 返回 stop 函数——TUI 在 `pkg/tui/notify.go:1706`（`s.stopAbandonedRunReaper = …Start(ctx)`），gateway 在 `pkg/gateway/serve_run.go:167-172`（`stopReaper := …Start(ctx)` + `defer stopReaper()`）。gateway 那里 `process.Environment` 变量名是 `h`（`serve_run.go:130` `h.EnsureRunnerPool()`，`:133` `Env: h`）。
- TUI 当前会话：`pkg/tui/chat_session.go:1179` `func (s *ChatSession) currentSessionID() string`。
- 运行态：`env.Control`（`run.Controller`）。`pkg/run/controller.go:331` `Find(sessionID) (runID string, phase Phase, ok bool)`；phase 取值有 `Running`、`Waiting`、`Finishing`、`Finished`。
- 会话 runner：`pkg/process/runner_pool.go:170` `env.BoundRunnerForSession(ctx, sessionID) (*run.Runner, bool)`（不新建 runner）。
- 会话审批模式：`Runner.PermissionSnapshotForSession(sid).ApprovalPolicy.Mode`，Full Access 预设是 `safety.ApprovalNever`（`pkg/safety/permission_types.go:860-866`）。
- 配置入库现状（plan 187，207 后现状）：运行时配置只在 `fb_config` 表（`section` 主键、`value_json`），`pkg/process/config_db.go` `composeSectionsFrom` 把 yaml 里所有运行时段清零后再用表里的值——**文件里的运行时段在运行时一律不生效**。唯一会把文件内容搬进表的是一次性导入：`pkg/process/config_import.go:34` `legacyImportMarkerSection = "meta.legacy_yaml_imported"`，`:63-64` 表里已有标记就直接返回；`:77` 表里**没有任何 Root 段**（`storedRootSectionPresent`）时才调 `:141` `importLegacyYAMLSections`，它在 `:151-153` 遍历 `rootConfigSections`、跳过零值段，把非零段写进表并把文件重写成只剩启动段。
- 配置段表（207 后）：`pkg/process/config_db.go:35-46` `rootConfigSections` 现为 **10 项**——`approval_preset`、`sandbox_workspace_write`、`compact`、`cron`、`agents`、`hooks`、`memories`、`batch`、`features`、`lsp`（203/206/207 已删 `windows`、网络等段），`:48` 起按 yaml 标签反射到 `appcfg.Root` 字段（名字对不上会在 init panic）。**207 的默认值登记表**：`pkg/process/config_defaults.go:36` `defaultStoredRoot()` 是全部存库字段的默认值单一来源，有覆盖测试逼新字段登记——`Root` 加 `Peers` 后按该测试的要求登记（默认全零值即可，注释写明段只存库）。`appcfg.Root` 在 `pkg/config/config.go`。校验入口 `validateLoadedRoot`（`pkg/config/load.go`）。
- 架构预算（`pkg/architecture/cache_test.go:224`）：`"process": {current: 19, target: -1}`，注释说组合根曾因 mcp、lsp 两次有意加依赖。本计划让 process 多 import 一个 `pkg/peer`，**必须把 `current` 改成 20 并写明理由**（见 §9 STOP 第 1 条）。
- 文件数：`pkg/process` 23 个生产文件（上限 31）；`pkg/gateway` **31 个，已到上限——本计划在 gateway 只改现有文件**。
- TUI `/config` 面板（plan 190）**已经落地**（`06f1bc57` 核实；`plans/README.md` 里 190 的状态没更新）：`pkg/tui/config_panel_schema.go:42` `type configField struct`（`Path, Label, Kind, Options, Min, Max, Clamp, List, Hint`）、`:96` `configSectionSchemas`（段表现状见 §3.7：`approval_preset`…`credentials`，无 `tools`）；`pkg/tui/config_panel.go:58` `configPanelSnapshot{Stored, File}`、`:75` `ChatSession.ConfigPanelSections`、`:150` `handleConfig`（`:165-166` 把快照存进面板）、`:298` `fieldItems`（逐个列 `schema.Fields`）。`pkg/tui/config_panel_test.go:533` `TestConfigPanelSchemaCoversEveryRootSection` 要求每个 Root 段都有描述符——**本计划给 Root 加 `peers` 后，不加描述符全量测试就会红**。
- 网关配置接口（**191 §3.2 已重写**）：`pkg/gateway/api_extra.go:5383` `handleConfigFile`——GET 返回**磁盘文件原文** `{path, yaml, approval_presets_visible}`，PUT 只查 YAML 语法（`configFileSyntaxError`）后原子写文件、**不写库**；`RenderConfigDocument` 已随 191 删除（全仓无引用）。设置页自身的读写是 `GET/PATCH /api/settings`（路由 `:383-386`，handler `:9479` 起；GET 的 `meta` map 在 `:9513-9523`，已有 `approval_presets_visible` 与 `builtin_handoff_prompt`）；前端 `frontend/src/lib/api.ts:4070` 一带已读 `raw.meta`。**194 落地后 `pkg/architecture` 的 `noImporterExempt` 里有 `pkg/peer` 的豁免条目（注释写明「pkg/process 一接入就删」）——本计划接线后必须删掉它，否则 `TestEveryPackageHasAnImporter` 相关注释失真。**
- 子代理通道：`pkg/run/subagent.go:641` `subagentChannelFor`、`:2313` `SubagentRunning(r, conversationSessionID, agentKey)`；通道里 `ch.running *subagentExecution`（`:611`）。目前**没有**「列出某会话正在跑的子代理」的导出函数。

## 3. 设计

### 3.1 配置段 `peers`（进 `fb_config`）

> **2026-10-31 追加裁决（always-on）覆盖本节部分内容**：跨进程协同固定开启，`PeersSection` **删 `Enabled` 与 `Inbound` 两个字段，只留 `ClaudeCode`**；`validateLoadedRoot` 不加 `peers.inbound` 校验；`StartPeers` 只在 `!peer.Enabled()`（不支持的平台或未隔离的测试）时空操作；§3.2/§3.3 里「配置关闭时停服务」「Enabled 由开变关」等分支删除；§4 步骤 4 的「`peers.enabled=false` → 什么都不写」用例删除。下文保留原字段描述仅作历史，**以本注为准**。

```go
// pkg/config/config.go
type PeersSection struct {
    // Enabled turns local peer messaging on or off; nil means on.
    Enabled *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
    // Inbound is "", "accept", "hold" or "refuse"; "" derives the policy
    // from the session's permission mode (plan 196).
    Inbound string `yaml:"inbound,omitempty" json:"inbound,omitempty"`
    // ClaudeCode moves this install's peer registration into Claude Code's
    // session directory and socket namespace, so Claude Code sessions and
    // forebrain can message each other; forebrain's own directory is then
    // left alone. It is shown, and takes effect, only while this machine
    // has Claude Code's session directory. nil or false — the default —
    // keeps peer messaging among forebrain processes only. The whole
    // section lives in fb_config only: forebrain.yaml never sets it.
    ClaudeCode *bool `yaml:"claude_code,omitempty" json:"claude_code,omitempty"`
}
```

- `Root` 加 `Peers PeersSection \`yaml:"peers,omitempty" json:"peers,omitempty"\``（放在现有段序列末尾、`Credentials` 之前；203–207 已删多个旧段，以当前 `Root` 为准）。
- `rootConfigSections` 末尾加 `"peers"`；`pkg/process/config_defaults.go` 的 `defaultStoredRoot()` 按覆盖测试要求登记 `peers`（默认零值）。
- **只存数据库（owner 追加裁决）**：`config_db.go` 新增 `var dbOnlyConfigSections = map[string]bool{"peers": true}`（注释：这些段从来不由 forebrain.yaml 设置——导入跳过、组装时文件值本就清零），`importLegacyYAMLSections` 的循环（`config_import.go:151-153`）开头加 `if dbOnlyConfigSections[name] { continue }`；`importLegacyConfigIfNeeded` 读完文件（`:69` `LoadLegacyFileSections` 之后）若 `!reflect.ValueOf(persisted.Peers).IsZero()` → `slog.Warn("forebrain.yaml peers section is ignored; peer settings live in the state database")`，**不报错、不改文件**。
- `validateLoadedRoot`：`Inbound` 不在四个值内 → 报错 `peers.inbound must be one of accept, hold, refuse`。
- 生效：`PeerService` 每个 tick 读 `env.Deps` 当前配置快照（每 turn 同步后的那份），`Enabled` 由开变关 → 停服务（删记录、key、socket）；由关变开 → 启动；`ClaudeCode` 变化 → 按 §3.4 重算目标模式、需要时整体切换。`Inbound` 由 196 在每条来信时读。
- 开关值：`switchOn := cfg.Peers.ClaudeCode != nil && *cfg.Peers.ClaudeCode`；目标模式 `cc := switchOn && detected`（`detected` 见 §3.4，开关关着时不检测）；`layout := peer.NewLayout(cc)`（194 §3.1），模式变化时重新 `NewLayout`。所有目录与 socket 路径都从当前 `Layout` 取，**不得在别处自己拼 FB 或 CC 路径**。

### 3.2 `PeerService`（`pkg/process/peers.go`）

```go
type PeerSurface struct {
    Kind           string                 // "tui" | "gateway"
    CurrentSession func() string          // TUI: the conversation on screen; nil for gateway
    // 196 adds: Publish (run-event funnel), Wake (start a turn for a peer prompt)
}

func (env *Environment) StartPeers(ctx context.Context, s PeerSurface) (stop func())
```

- `StartPeers` 在 `!peer.Enabled()` 或配置关闭时返回空 stop、什么也不做（错误只记 `slog.Warn`，**永不让 TUI/gateway 启动失败**）。
- 启动顺序：`hopKey := peer.NewHopKey()`；算目标模式（§3.4；开关关着时**不调用** `peer.ClaudeCodeDetected()`）；`layout := peer.NewLayout(cc)`；按 §3.4「进入模式」建 socket 目录 + 校验、`peer.Listen(layout.SocketPath(os.Getpid()))`、起 `peer.Server{Token: peer.NewToken(), Handler: inbox}`（`inbox` 内部先过来源闸门）、派生名字（`peer.DeriveName(cwd, taken)`，`taken` 查 `peer.ReadRecords(layout.Dir)` 里所有在线记录的 `NormalizeName(name)`）、写记录与 key；起 tick goroutine。
- 自身地址：`addr = peer.EncodeUDS(当前 socket)`。hop token 按地址算；**本进程用过的每个地址**各算一个，都放进「自身 token」集合（196 的 `CheckChain` 用；切换模式会换地址）。
- `cwd`：TUI＝`env.LaunchDir`（启动目录，会话工作目录）；gateway＝`env.LaunchDir`（只用于列表显示）。
- `sessionId`：TUI＝`CurrentSession()`；gateway＝启动时 `uuid.NewString()`，进程内不变。
- **来源闸门（FB 模式下生效；196 沿用）**：FB 模式时每个帧先查 FB 目录的在线记录里有没有 `pid == in.PeerPID`、带 `forebrain` 扩展块、且 `peer.EncodeUDS(rec.MessagingSocketPath) == 帧.from` 的那一条（目录列表缓存 1s）；没有 → 不回执、不交给下游，只记 `slog.Debug("peer frame from a non-forebrain process ignored")`。FB 模式的 socket 因此只收 forebrain。CC 模式不过这道闸门，与 Claude Code 一样按 196 的准入收同用户的帧。另外，`in.Local` 不是当前 socket 的帧（切换模式前旧 socket 上迟到的帧）一律丢弃，只记 debug。
- 占位收件（196 替换）：user 帧 → 若 `from` 通过 `peer.ReplyTargetAllowed(from, in.Local, os.Getuid(), 发送方记录的 peerFeatures)`，从当前地址回 `peer.RefusedStatus("inbox not ready", addr, msg_id)`；control 帧只记 `slog.Debug`。
- `PeerService` 挂在 `Environment` 上（字段 `peers *PeerService`），`env.Close()` 时若还在跑就停（与表面的 stop 重复调用安全，`sync.Once`）。

### 3.3 tick（1s）：状态与扩展块

每秒计算一次快照，**与上次写入的内容不同才重写记录**（Claude Code 判活不看 `updatedAt`，所以无变化时不写盘）：

- `status`：TUI——`env.Control.Find(CurrentSession())` 为 `Running` → `busy`，`Waiting` → `waiting`，否则 `idle`；gateway——`env.Control` 任一 run 在 `Running` → `busy`，否则 `idle`。变化时同时更新 `statusUpdatedAt`。
- `agents[]`（设计 §4.1）：
  - TUI：当前会话的主代理一项（`name`＝记录名；`root`＝`env.LaunchDir`）；gateway：最近 24 小时有活动的会话（新增 `state.SessionStore.RecentlyActiveSessions(ctx, since time.Time, limit int)`，按 `updated_at` 倒序，排除 `project_type = batch` 的会话），每个一项，`name`＝`<slug(basename(cwd))>-<会话 id 去掉 "cli-"/"web-" 等前缀后的前 4 位>`，`root`＝`fb_sessions.cwd`。最多 64 项。
  - 每个主代理会话再列它**正在跑**的子代理：新增 `run.LiveSubagents(r *Runner, conversationSessionID string) []LiveSubagent{Key, Title string; StartedAt time.Time}`（读该 runner 的子代理通道表里 `running != nil` 的通道；runner 用 `env.BoundRunnerForSession` 取，取不到就不列）。子代理 `id`＝`<会话 id>/<Key>`，`name`＝`<主代理名>.<slug(Title)>`（冲突加 `-2`…），`parent`＝会话 id，`task`＝`Title`。
  - `status`：主代理同上一条逻辑按会话算；子代理一律 `busy`。
  - `task`：新增 `state.RunStore.LivePrimaryInput(ctx, sessionID) (string, error)`——`SELECT input_text FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL AND status IN ('running','waiting_action') ORDER BY created_at DESC LIMIT 1`，取首行、截到 120 个字符；空闲会话为空。
  - `branch`：**进程内读取，不跑 `git`**（自闭环）：从 `root` 向上找 `.git`；是目录就读 `<.git>/HEAD`，是文件就按其 `gitdir: <路径>`（相对路径相对于该文件所在目录）读 `<gitdir>/HEAD`；内容 `ref: refs/heads/<名>` → `<名>`，否则（分离头）取前 7 个字符；按 root 缓存 30s，任何失败为空。
  - `mode`：会话审批模式为 `ApprovalNever` → `bypass`，否则 `prompting`（runner 取不到 → `prompting`）。
- 每个 tick 先看配置快照：`Enabled` 变了按 §3.1 停或启整个服务（本 tick 不再做别的）；再按 §3.4 算目标模式，与当前模式不同就整体切换（本 tick 不再做别的）。
- 每 5 个 tick 做一次登记检查（§3.4），每 60 个 tick 清理死记录（§3.5）。
- 记录有变化时写当前模式目录里的记录。

### 3.4 两种登记模式与切换（设计 §7；owner 追问 9、10 与追加裁决）

- **目标模式**：开关关（默认）→ FB 模式，**不调用** `peer.ClaudeCodeDetected()`；开关开 → 开关刚变为打开的那个 tick 与此后每 5 个 tick 调用一次，`detected` 为真 → CC 模式，否则 FB 模式。检测函数在 `peers.go` 里经一个包内变量调用（默认 `peer.ClaudeCodeDetected`），测试可以换成计数器。
- **FB 模式**：`MkdirAll(layout.Dir, 0o700)`（FB 目录）；socket 目录 `MkdirAll` + 校验；记录的 `messagingSocketPath`＝当前 socket；key 按它算文件名。**不得**碰 CC 会合目录与 CC socket 目录（目标模式检测那一次 stat 除外）。
- **CC 模式**：socket 目录（CC socket 目录）`MkdirAll` + 校验，Claude Code 自己也这么做；记录与 key 写进 CC 会合目录，**CC 会合目录永不创建**——写入因目录不在而失败只记日志，下一次检测自然退回 FB 模式。**不得** stat、读、写 FB 会合目录与 FB socket 目录。
- **登记检查**（每 5 个 tick）：当前目录里自己的 `<pid>.json` 或 key 不在 → 原子重写（CC 目录不在时不写）。
- **切换**（目标模式 ≠ 当前模式），按顺序：① 调 `onModeSwitch` 钩子（196 在这里把挂起未放行的来信全部回 `expired`；本计划阶段钩子为空）；② 删旧目录里自己的记录与 key（旧目录已不在就跳过）；③ 关旧 `peer.Server`、删旧 socket 文件；④ `layout = peer.NewLayout(新模式)`，建 socket 目录、监听新 socket、起新 `peer.Server`（新 token）；⑤ 名字沿用——新目录里有在线记录占着同一个名字才 `DeriveName` 换后缀，`nameSource` 记 `collision`；⑥ 写新记录与 key。任何一步失败只记 `slog.Warn`，下一个 tick 重试整个切换；**不得**为了稳妥同时监听两个 socket 或同时在两个目录登记。
- **结果**：开关打开且 CC 目录在 → CC 模式，FB 目录与 FB socket 目录里没有这个进程的任何文件；CC 目录被删 → ≤5s 回到 FB 模式（名字通常不变）；CC 目录再出现 → ≤5s 回到 CC 模式；开关关掉 → 下一个 tick 回到 FB 模式，此后不再 stat CC 目录。

### 3.5 清理与退出

- 死记录清理：`peer.ReadRecords(layout.Dir)` 里**带 `forebrain` 扩展块**且 `!peer.Alive()` 的记录与其 key 删掉；不带扩展块的（Claude Code 自己的）不碰。只清当前模式的目录。
- stop：停 tick → 调 `onModeSwitch` 钩子（196 在这里回 `expired`）→ 关 server → 删 socket → 删当前目录里自己的记录与 key（CC 目录已不在就跳过）。

### 3.6 表面接线（各一处）

- TUI：`pkg/tui/notify.go` 在 `s.stopAbandonedRunReaper = …Start(ctx)` 之后加 `s.stopPeers = env.StartPeers(ctx, process.PeerSurface{Kind: "tui", CurrentSession: s.currentSessionID})`，在 TUI 现有的退出清理处（`stopAbandonedRunReaper` 被调用的同一位置）调用 `s.stopPeers()`。`ChatSession` 新增字段 `stopPeers func()`（若 TUI 有字段计数守卫，先跑测试确认）。
- gateway：`pkg/gateway/serve_run.go` 在 `defer stopReaper()` 之后加 `stopPeers := h.StartPeers(ctx, process.PeerSurface{Kind: "gateway"})` + `defer stopPeers()`。**不新建 gateway 文件**。

### 3.7 开关的显示（owner 追加裁决：检测到 Claude Code 才显示）

- `pkg/process/peers.go` 导出 `func ClaudeCodeDetected() bool`，直接返回 `peer.ClaudeCodeDetected()`；doc 注释写明「只 stat Claude Code 的会合目录，决定要不要显示 `peers.claude_code`」。`pkg/tui`、`pkg/gateway` 只经它判断，不 import `pkg/peer`。
- **TUI `/config` 面板**（190 已落地，见 §2）：
  - `configField` 加字段 `ClaudeCodeOnly bool`，注释：只在本机检测到 Claude Code 时显示（owner 2026-10-30 裁决）。
  - `configSectionSchemas` 现有段（2026-11-01 核实）：`approval_preset`/`sandbox_workspace_write`（configTabApproval）、`compact`/`memories`/`cron`/`features`（configTabSession）、`agents`/`hooks`/`lsp`（configTabAgents）、`gateway`/`credentials`（configTabFile）。在 `lsp` 段之后加一段，写法照 `lsp`：

    ```go
    {
        Name: "peers", Title: "对等通信", Tab: configTabAgents,
        Summary: "本机代理进程之间互发消息",
        Fields: []configField{
            {Path: "enabled", Label: "启用", Kind: configFieldBool3, Hint: "默认开"},
            {Path: "inbound", Label: "来信处理", Kind: configFieldEnum, Options: []string{"accept", "hold", "refuse"}, Hint: "默认按会话的审批模式推导"},
            {Path: "claude_code", Label: "与 Claude Code 互通", Kind: configFieldBool3, ClaudeCodeOnly: true, Hint: "默认关；开关不同的 forebrain 进程互相看不见"},
        },
    },
    ```

  - `configPanelSnapshot` 加 `ClaudeCode bool`，`ChatSession.ConfigPanelSections` 里填 `process.ClaudeCodeDetected()`（打开面板时判断一次）；`handleConfig` 把它存进 `configPanel` 的新字段 `claudeCode`；`fieldItems` 跳过 `ClaudeCodeOnly && !p.claudeCode` 的字段。隐藏时这个字段的值仍留在段的 JSON 树里，保存其它字段时原样写回。
  - 把 `config_panel_schema.go` 与 `config_panel_test.go` 里段数注释/断言按加 `peers` 后的实数更新（段表现为 10 项，加 `peers` 后 11；以现场注释实际写的数字为准）。
- **网页（2026-11-01 按 191 落地后的现场重写；原 KeepEditorHiddenFields/RenderConfigDocument 机制已随 191 作废——配置文件编辑器现在只读写磁盘文件，`peers` 段只存库、永不进文件，编辑器天然看不见也冲不掉它）**：
  - `GET /api/settings` 的 `meta` map（`api_extra.go:9513-9523`，`approval_presets_visible` 旁）加 `"claude_code_available": process.ClaudeCodeDetected()`。
  - 开关行的呈现（⑬裁决「检测到才显示」落在网页）：191 的设置页按 schema 画——先在原型 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html` 的 `FORMS` 补一行（组「对等通信」，字段 `F('peers.claude_code', …)`，标签「与 Claude Code 互通」，说明照 TUI 行「默认关；开关不同的 forebrain 进程互相看不见」，条件：仅 `claude_code_available` 为真时显示），再同步 `frontend/src/lib/settingsSchema.ts`（显隐条件用 meta 驱动的条件形状，照 191 form 框架已有条件写法）与 `pkg/gateway/settings_paths.go`（路径 `peers.claude_code`、bool、非凭据）；`settingsSchema.test.ts` 的原型一致性用例要求原型与 schema 同改后保持绿。`meta.claude_code_available=false` 时不渲染该行，隐藏时库里的值不动。

## 4. 步骤

1. **配置段**：改 `pkg/config/config.go`、`pkg/config/load.go`（校验）、`pkg/process/config_db.go`（段表 + `dbOnlyConfigSections`）、`pkg/process/config_import.go`（导入跳过 + 警告）。补测试：`pkg/config` 的校验用例（非法 `inbound` 被拒）；`pkg/process` 现有 config_db 测试里加 `peers` 段往返（含 `claude_code:true`，参照该文件已有段的用例）；`pkg/process` 现有导入测试（`config_import_test.go`）加一例：表为空、forebrain.yaml 含 `peers: {claude_code: true}` 与另一个普通段 → 导入后表里有那个普通段、**没有 `peers` 行**，组装出的 `Root.Peers.ClaudeCode == nil`；再加一例：表里 `peers` 行是 `{"claude_code":true}`、文件里写 `claude_code: false` → 组装结果为 true（文件不生效）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/config ./pkg/process -run 'Config|Compose|Validate' -count=1` → ok。
2. **state 查询**：`pkg/state` 加 `RunStore.LivePrimaryInput`、`SessionStore.RecentlyActiveSessions`（放进现有文件 `runrt.go`、`session_store.go`，不新建文件）+ 用例。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → ok。
3. **`run.LiveSubagents`**：加在 `pkg/run/subagent.go`（不新建文件、不新增 import）+ 用例（参照 `pkg/run/subagent_test.go` 里构造运行中子代理通道的现有用例）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run Subagent -count=1` → ok。
4. **`PeerService`**：新建 `pkg/process/peers.go` + `peers_test.go`；架构表 `process` 的 `current` 改 20，注释追加一句「and pkg/peer: the local peer protocol the composition root runs for both surfaces (plan 195)」。
   测试（每个用例用 `t.Setenv` 设四个覆盖变量 `FOREBRAIN_PEERS_DIR`、`FOREBRAIN_PEER_SOCKET_DIR`、`FOREBRAIN_PEERS_CC_DIR`、`FOREBRAIN_PEER_CC_SOCKET_DIR`；两个 socket 目录各用一个 `os.MkdirTemp("/tmp","fbp")`；tick 间隔做成包内可改的变量，测试里设 20ms）：
   - **默认（表里没有 `peers` 行）**：CC 会合目录与 CC socket 目录都**预先建好** → 记录与 key 只在 FB 目录、socket 只在 FB socket 目录；两个 CC 目录始终为空，且检测函数（§3.4 的包内变量，换成计数器）**一次都没被调用**；记录字段与 §1 取值一致；key 权限 0600、socket 权限 0600。
   - **FB 模式来源闸门**：另起一个 `peer.Server` 当发送方，只在 FB 目录写一条**不带扩展块**的记录指向它 → 给本服务的 socket 发 user 帧，发送方**收不到任何回执**；给那条记录补上扩展块再发 → 收到 refused 回执；开关打开但 CC 目录不存在（仍是 FB 模式）时重复第一种 → 仍然收不到。
   - **开关打开、CC 目录不存在** → 仍是 FB 模式：记录在 FB 目录、socket 在 FB socket 目录，CC 会合目录**仍不存在**、CC socket 目录为空。之后 `os.MkdirAll(ccDir)` → 一个检查周期内切到 CC 模式：记录与 key 只在 CC 目录（`messagingSocketPath` 在 CC socket 目录），**FB 目录与 FB socket 目录里没有这个进程的任何文件**，`name` 不变；再 `os.RemoveAll(ccDir)` → 一个检查周期内回到 FB 模式，CC 目录**没有被重建**，`name` 不变。
   - **CC 模式不碰 FB 侧**：FB 目录里预先放一条别的进程的记录；CC 模式下跑够 70 个 tick（覆盖死记录清理周期）→ FB 目录与 FB socket 目录的文件列表与各文件 mtime 都不变。
   - **CC 模式收信**：往当前 socket 发 user 帧（发送方记录**不**带扩展块、只写在 CC 目录）→ 收到 refused 回执，回执 `from` 是当前（CC）地址。
   - **运行中关开关**：CC 模式下把配置快照改成关闭 → 下一个 tick 回到 FB 模式：CC 目录里没有它的记录与 key、旧 socket 已删；此后 10 个 tick 内检测函数的计数不再增加。
   - **开关的显示**：`process.ClaudeCodeDetected()` 在 `FOREBRAIN_PEERS_CC_DIR` 指向已存在目录时为 true，指向不存在的路径时为 false 且该路径仍不存在；`GET /api/settings` 的 `meta.claude_code_available` 随之同值（网关测试，见步骤 5）。
   - 自闭环：`grep -n '"os/exec"' pkg/process/peers.go` 无输出；`branch` 用例：临时目录里手写 `.git/HEAD`（`ref: refs/heads/feat-x`）与 worktree 形状（`.git` 文件 + `gitdir:`）各一例，读出 `feat-x`。
   - 在 `env.Control` 里 `Begin` 一个当前会话的 run → 记录 `status` 变 `busy`；`Finish` 后变 `idle`；无变化的 tick 不改文件 mtime。
   - 写一条 pid 已死、带扩展块的假记录 → 清理后消失；不带扩展块的假死记录保留。
   - （always-on 裁决：原「`peers.enabled=false` → 什么都不写」用例删除；服务只在 `!peer.Enabled()` 平台/测试守卫时空操作。）
   - 用 `peer.Send` 往本服务发一条 user 帧，自己另起一个 `peer.Server` 当发送方地址 → 收到 `status:"expired", status_detail:"refused"` 回执。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run Peer -count=1` → ok；`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok。
5. **表面接线与开关的显示**：改 `pkg/tui/notify.go`（及 `ChatSession` 字段所在文件）、`pkg/gateway/serve_run.go`；按 §3.7 改 `pkg/tui/config_panel.go`、`pkg/tui/config_panel_schema.go`、`pkg/gateway/api_extra.go`（只动 `handleSettings` 的 meta）、`pkg/gateway/settings_paths.go`、`frontend/src/lib/settingsSchema.ts`、原型 `config-db-settings-preview.html`（§3.7 网页条）。**接线后删 `pkg/architecture/cache_test.go` `noImporterExempt` 里的 `pkg/peer` 条目**（194 留的，注释写明接线即删）。补测试：
   - `pkg/tui/config_panel_test.go`：快照 `ClaudeCode=false` → `peers` 段页面的条目里没有「与 Claude Code 互通」；`true` → 有；`false` 且表里 `peers` 是 `{"claude_code":true}` 时改其它字段再保存 → 写入的 JSON 仍含 `"claude_code":true`。覆盖测试 `TestConfigPanelSchemaCoversEveryRootSection`、`TestConfigPanelSchemaFieldsMatchTheirSection` 保持绿。
   - `pkg/gateway/api_extra_test.go`：`GET /api/settings` 的 `meta.claude_code_available` 测试二进制里为 false；`t.Setenv("FOREBRAIN_PEERS_CC_DIR", 一个已存在的临时目录)` 后为 true；`PATCH /api/settings` 对 `peers.claude_code` 写 true → 表里 `peers` 行变 `{"claude_code":true}`。
   - 前端：`settingsSchema.test.ts` 原型一致性保持绿（原型与 schema 同加了该行）；一个 vitest 断言 `claude_code_available=false` 时该行不渲染。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -count=1` → ok（测试二进制里服务不启用，行为不变）。
6. **手测（本机，scratch home，不碰真实 home）**：

   ```bash
   B=$(mktemp -d); CGO_ENABLED=1 go build -tags fts5 -o "$B/forebrain" ./cmd/forebrain
   H=$(mktemp -d); P=$(mktemp -d); R=$(mktemp -d)   # R = 一个临时项目目录
   ```

   覆盖变量（生产二进制不受测试守卫影响；用覆盖变量是为了不碰真实目录）：`V="FOREBRAIN_HOME=$H FOREBRAIN_PEERS_DIR=$P FOREBRAIN_PEER_SOCKET_DIR=$P/fb-socks FOREBRAIN_PEERS_CC_DIR=$P/cc-sessions FOREBRAIN_PEER_CC_SOCKET_DIR=$P/cc-socks"`（`$P/cc-sessions` 先不建）。
   - **默认关闭**：tmux 里 `cd "$R" && env $V "$B/forebrain"` 启动 TUI。另一个窗口：`cat "$P"/*.json | jq .` 能看到记录，socket 在 `$P/fb-socks/`。TUI 里 `/config` → 「对等通信」：**没有**「与 Claude Code 互通」这一行。`mkdir "$P/cc-sessions"` 后重新打开 `/config` → 这一行出现，显示「默认（不设置）」；等 10s，`ls "$P/cc-sessions"` 为空、`$P/cc-socks` 不存在或为空。退出 TUI，`$P` 下没有它的 json/key、`$P/fb-socks/` 里没有它的 socket。
   - **打开开关**：在 `/config` 里把「与 Claude Code 互通」设为开并保存，再随便发一句让 TUI 跑一轮（配置每轮同步）——或者直接写库：`sqlite3 "$H/state/forebrain.state.sqlite" "insert into fb_config(section,value_json,updated_at_ms) values('peers','{\"claude_code\":true}',0) on conflict(section) do update set value_json=excluded.value_json"` 后重启。≤5s 内：记录与 key 只在 `$P/cc-sessions`、socket 只在 `$P/cc-socks/`；`$P/*.json`、`$P/*.key`、`$P/fb-socks/` 里都没有它的文件。`rm -rf "$P/cc-sessions"` → 5s 内记录回到 `$P`、socket 回到 `$P/fb-socks/`，`$P/cc-sessions` 没有被重建，`/config` 里这一行消失；`mkdir "$P/cc-sessions"` → 5s 内又回到 CC 侧，名字不变。退出后各处都没有它的文件。
   - **自闭环**：TUI 运行期间 `ps -o pid,ppid,command -A | awk -v p=<TUI pid> '$2==p'` 不应周期性出现 `ps`/`git` 子进程（观察 30s）。
7. **收口**：`bash scripts/package-graph.sh`（`pkg/process → pkg/peer` 新边）；`go vet ./...`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（核心路径变更，全量）。

## 5. 边界

- **只动**：`pkg/config/config.go`、`pkg/config/load.go`、`pkg/process/config_db.go`、`pkg/process/config_import.go`（及其现有测试文件）、`pkg/process/peers.go`（新）、`pkg/process/peers_test.go`（新）、`pkg/process/open.go`（`Environment` 字段 + `Close` 里停服务）、`pkg/state/runrt.go`、`pkg/state/session_store.go` 及其测试、`pkg/run/subagent.go` 及其测试、`pkg/tui/notify.go`（+ `ChatSession` 字段所在文件）、`pkg/tui/config_panel.go`、`pkg/tui/config_panel_schema.go`、`pkg/tui/config_panel_test.go`、`pkg/gateway/serve_run.go`、`pkg/gateway/api_extra.go`（只改 `handleConfigFile`）、`pkg/gateway/api_extra_test.go`、`pkg/architecture/cache_test.go`（只改 process 那行）、`pkg/architecture/testdata/graph.json`（脚本生成）。
- **不动**：收件处理（196）、工具（197）、护栏（198）、任何对等消息的界面呈现（199/200）；Web 设置页前端（191 执行时加 `peers` 卡——见 §8）；TUI `/config` 面板除 §3.7 列出的改动外不动。
- 不在 `pkg/tui`、`pkg/gateway`、`pkg/tool`、`pkg/run` 里 import `pkg/peer`。

## 6. 完成判据（机器可查）

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...` 无输出。
2. `grep -rln 'forebrain-harness/pkg/peer"' pkg --include='*.go' | grep -v _test.go | grep -v '^pkg/process/'` 无输出。
3. `ls pkg/gateway/*.go | grep -v _test | wc -l` 仍是 31。
4. §4 第 6 步手测记录（命令输出贴进本计划文末「执行记录」）：默认关闭时 CC 侧始终为空；`/config` 里开关随 CC 目录出现/消失；打开后只有 CC 侧有它的文件、FB 侧没有；删 CC 目录后回到 FB 侧且不重建 CC 目录，再建回来又切回去；没有周期性的 `ps`/`git` 子进程；每次退出都清理干净。
5. `plans/README.md` 本行状态更新。

## 7. 测试计划

- `pkg/process/peers_test.go`：上面第 4 步列的各组用例，Environment 构造参照 `pkg/process/config_apply_test.go`（plan 189 执行后由 `config_reload_test.go` 改名而来；若尚未改名就用旧名）。
- `pkg/state`：两条查询各一组用例（含 batch 会话被排除、按时间倒序、limit 生效）。
- `pkg/run`：`LiveSubagents` 只返回 running 的通道。

## 8. 维护注意

- TUI 面板的 `peers` 描述符由本计划加；191（Web 设置）执行时必须加 `peers` 卡，`claude_code` 行只在 `GET /api/config` 的 `claude_code_available` 为真时显示，标签与提示文案与 TUI 相同。在那之前网页端打开互通只能用 TUI 或直接写 `fb_config`（§4 第 6 步的 SQL）。**不要**为了方便给 forebrain.yaml 加回 `peers` 段的读取——owner 裁决它只存数据库。
- 以后新增任何碰登记目录或 socket 的代码，都必须走当前 `Layout`，并在 `peers_test.go` 里证明 FB 模式不碰 CC 侧（检测那一次 stat 除外）、CC 模式不碰 FB 侧。
- 自闭环：forebrain 之间的功能不得依赖任何 Claude Code 进程，也不要引入外部命令或常驻的协调进程。**不要**为了让开关不同的进程互相看见而同时读两个目录或同时开两个 socket——owner 裁决明确要求打开后不再碰自己的登记目录。
- gateway 的 `agents[]` 只列 24 小时内活跃的会话：网关收到 Claude Code 来信时按发送方工作树路由（196），会话太久没动就不会被选中——这是有意的。
- 记录里的 `task` 是当前一轮输入的首行，会被本机同用户的其它进程读到；不要往扩展块里放更多会话内容。

## 9. STOP 条件

- **把 `fanOutBudgets["process"].current` 从 19 改到 20 之前**，确认 owner 已在 `plans/README.md` 本族说明里认可（索引里写的是「已随本族计划报 owner」；若 owner 明确否决，STOP——替代方案是把 `pkg/peer` 合进 `pkg/process`，但那会超过 31 文件上限，需要重新设计）。
- `run.Controller.Find` 的 phase 取值与 §2 不符，或 TUI 退出路径里找不到 `stopAbandonedRunReaper` 的调用点。
- 任何一步需要在 `pkg/gateway` 新建文件。
- 测试里出现了对真实 `~/.claude/sessions`、`/tmp/cc-socks`、`/tmp/fb-socks-<uid>` 或真实 FB 目录（`os.UserCacheDir()/forebrain/peers`）的写入（`ls -la` 前后对比确认）。
- `config_import.go` 的结构与 §2 摘录不符（比如导入不再遍历 `rootConfigSections`），无法用一张跳过表让 `peers` 不被导入。
- `pkg/tui/config_panel*.go` 或 `handleConfigFile` 的结构与 §2 摘录不符（比如面板已被重写、没有 `configField`/`fieldItems`，或 PUT 不再逐段写），无法按 §3.7 加描述符与隐藏规则。

**追加裁决（owner 2026-10-31）：跨进程协同固定开启（always-on）**——不能关闭、不支持关闭、不支持配置；唯一的开关是「与 Claude Code 互通」（`peers.claude_code`，默认关闭，用户可手动打开，只存库，装了 Claude Code 才显示）。本计划受影响：`PeersSection` 删 `Enabled` 与 `Inbound` 两个字段，只留 `ClaudeCode`；`validateLoadedRoot` 里 `peers.inbound` 的校验删除；`StartPeers` 不再有「配置关闭时什么也不做」的分支（只在 `!peer.Enabled()`——即不支持的平台或未隔离的测试——时空操作）；「由开变关停服务 / 由关变开启动」的生效逻辑删除；测试里「`peers.enabled=false` → 什么都不写」一例删除。

**确认（owner 2026-10-31）**：process 扇出 19 → 20 已确认可以，执行时不必再停下询问。
