# 任务 08：实例池与 Manager——诊断侧与控制面的真实实现

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §2、§4.3、§5.3、§5.4、§7.5–§7.8、§8.3（全部）、§8.4、§9.2、§9.3、附录 C（`not-installed`、`start-failed`、`too-many-servers`）。
>
> **前置**：任务 06、07 已合入（以及其依赖 03、04、05）。先确认：`grep -n "func ResolveServers\|func Detect\|func NewDocSync\|func NewDiagStore\|func FormatEditDiagnostics" pkg/lsp/*.go` 五个都有输出，否则 STOP。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/process pkg/run/runner.go`。任务 03 之后这些文件不应再为 LSP 改变；若有与 LSP 相关的改动，先读懂再继续。

## 状态

- 优先级：P0 · 工作量：L · 风险：MED（并发与进程生命周期）
- 依赖：06、07
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

任务 03 已经把 `Pool`/`Manager` 接进了组合根，但它们什么也不做。本任务让它们真正工作：按需启动实例并复用、限额与空闲回收、配置重载对账、编辑后诊断（含等待窗口）、迟到诊断队列、读文件时预打开、shell 之后的变更扫描、`/lsp` 所需的快照与开关。`Query`（导航）留给任务 09；推荐与安装留给任务 13、12。

完成后，只要用户在 `enabled.json` 或配置里启用了某个服务器，编辑工具（任务 11 接入后）就能拿到诊断；**在任务 10、11 合入之前，没有任何调用方会触发这些代码**，所以对用户仍然无感。

## 现状

- `pkg/lsp/pool.go`、`manager.go`、`control.go`（任务 03 骨架）：`Pool{cfg atomic.Pointer, managers}`、`NewPool`、`Reconcile`、`NewManager`、`Close`；`Manager` 的各方法为空实现，`errNotAvailable` 用于控制动作。
- 任务 06：`ResolveServers`、`ServersForFile`、`MatchFile`、`ResolveRoot`、`LoadEnabled`、`SaveEnabled`、`StateDir`、`CacheDir`、`LogPath`、`PIDFile`、`Detect`、`UsableInstallRecipe`。
- 任务 05：`StartInstance`、`InstanceSpec`、`BuildEnv`、`ExpandCacheDir`、`ExpandCacheDirJSON`、`Instance` 的方法（含 `Registrations`、`AddFolder`、`Restart`、`Shutdown`、`State`、`Progress`、`LastError`、`PID`）、`sweepOrphans`。
- 任务 07：`DocSync`、`DiagStore`（含 `Snapshot`）、`PullDiagnostics`、`NewProblems`、`SeverityThreshold`、`FormatEditDiagnostics`、`FormatLateDiagnostics`、`LateChange`、`ReportOptions`。
- 任务 04：`URIToPath`、`FromLSPPosition`、`PublishDiagnosticsParams`。
- `appcfg.(*Root).EffectiveLSP()`（任务 01）给出 `Wait`、`MinSeverity`、`MaxPerFile`、`MaxFiles`、`LateDelivery`、`MaxServers`、`IdleTimeout`、`RequestTimeout`、`AfterEdit`。
- 会话标识：迟到队列按 **agent 会话**分组（规范 §8.4，子代理与 fork 各有自己的 id）。`llm.AgentSessionIDFromContext` 在 `pkg/llm`，`pkg/lsp` **不能**导入它（fan-out 已满 4），所以由调用方传入：`DidWrite(ctx, agentSessionID, changes)` 的第二个参数（任务 11 在工具里用 `llm.AgentSessionIDFromContext(ctx)` 取值），`PeekLate`/`AckLate` 同理。推荐的「每会话一次」按**对话**会话判断，用 `tool.ConversationSessionIDFromContext(ctx)`（`pkg/tool/state.go:249`，`pkg/lsp` 可以用）。

## 范围

**修改：** `pkg/lsp/pool.go`、`manager.go`、`control.go`、`doc.go`，及 `pool_test.go`、`manager_test.go`、`control_test.go`；`pkg/architecture/testdata/graph.json`。

**不要碰：** `pkg/lsp` 其他文件（`resolve.go` 只允许读）、`pkg/process`、`pkg/run`、`pkg/tool`。`Query`、`Install`、`DecideRecommendation`、`ResetRecommendations` 保持骨架行为（任务 09、12、13 实现）。

## 步骤

### 步骤 1：`pool.go` —— 实例表、启动、限额、空闲、对账、关闭

在骨架基础上加：

```go
type poolInstance struct {
	keys        []string // every key this instance serves (multi-root aliases)
	serverID    string
	fingerprint string
	projectRoot string
	srv         ServerConfig
	inst        *Instance
	docs        *DocSync
	refs        map[*Manager]struct{}
	lastUsed    time.Time
	inflight    int
	ready       chan struct{} // closed when the start attempt finished
	startErr    error
}

// Pool 增加字段：instances map[string]*poolInstance；diags *DiagStore；
// janitorStop chan struct{}；now func() time.Time（测试可替换）。

var janitorInterval = time.Minute // tests shorten it
```

1. `NewPool` 创建 `diags = NewDiagStore()`，并启动 janitor goroutine（`Close` 停止它）。
2. **键**：`serverID + "\x00" + fingerprint + "\x00" + root`。
3. **`acquire(ctx, m, srv, root) (*poolInstance, error)`**（未导出）：
   - 已有该键：加入 `refs`，等待 `ready`（select `ctx.Done()`）；`startErr != nil` 时从表中删除该条并返回错误（下次调用会重试）。
   - 没有：先找「同 `serverID`、同指纹、同 `projectRoot`、已就绪且 `inst.Capabilities().WorkspaceFolderChanges()`」的实例 → `AddFolder(ctx, root)` 成功则把新键加入其 `keys`、加入表并返回它。
   - 否则检查限额：不同实例数 ≥ `EffectiveLSP().MaxServers` 时关闭 `inflight == 0` 且 `lastUsed` 最早的一个（异步 `Shutdown`，立即从表中移除）；没有可关闭的 → `fmt.Errorf("too many language servers are running (%d); raise lsp.max_servers or stop one with /lsp", n)`。
   - 新建条目放进表，`go p.start(entry, m)`，然后等待 `ready`（select ctx）。**启动本身不受调用方 ctx 取消的影响**（调用方只是不再等待）。
4. **`start(entry, m)`**（在后台 goroutine 中）：
   - `env, missing := BuildEnv(EnvSpec{Passthrough: srv.EnvPassthrough, Env: srv.Env, FromProject: srv.EnvFromProject, Home: m.opts.Home})`；`missing` 非空时写入实例日志的第一行（不失败）。
   - `det := Detect(ctx, srv, env, runtime.GOOS, filepath.Join(StateDir(ws), "detect.json"))`；未安装 → `startErr = fmt.Errorf("language server %s is enabled but its command %q was not found%s", id, srv.Command, installSuffix)`，其中 `installSuffix` 为 `"; install it with: " + det.InstallCommand`（有配方时）或空。
   - `cache := CacheDir(ws, id, root)`，`os.MkdirAll(cache, 0o755)`；`Args` 逐个 `ExpandCacheDir`；`InitializationOptions`、`Settings` 用 `ExpandCacheDirJSON`。
   - `StartInstance(context.Background(), InstanceSpec{ServerID, Command: det.Path, Args, Env: env, Root: root, …, LogPath: LogPath(ws, id, root), PIDFile: PIDFile(ws), OnNotification: p.onNotification(srv.ID), OnStateChange: p.notifyRefs(entry), OnRestart: func(ctx) { entry.docs.Reopen(ctx) }})`；`OnRestart` 引用 `entry.docs`，所以 `docs` 在 `StartInstance` 返回后立刻赋值（重启不可能早于首次初始化完成）。
   - 失败 → `startErr = fmt.Errorf("language server %s failed to start: %v", id, err)`。
   - 成功 → `entry.docs = NewDocSync(inst, languageFor(srv), 64)`，`languageFor` 先查 `Filenames[base]` 再查 `ExtensionToLanguage[小写扩展名]`。
   - 最后 `close(entry.ready)` 并通知订阅者。
5. **`onNotification(serverID)`**：只处理 `textDocument/publishDiagnostics`：解码 `PublishDiagnosticsParams`，`URIToPath` 失败则忽略，`p.diags.Publish(serverID, path, params.Version, params.Diagnostics)`。
6. **使用计数**：未导出 `begin(entry)`/`end(entry)` 维护 `inflight` 与 `lastUsed`；所有经过实例的操作（DidWrite、DidRead、Query、sweep）都要包在 `begin/end` 里。
7. **janitor**：每 `janitorInterval`：`inflight == 0` 且 `now - lastUsed > EffectiveLSP().IdleTimeout` 的实例 → 从表中删除并 `Shutdown`（后台，超时 10 秒）。
8. **`Reconcile(cfg)`**：存入新配置；让每个 manager 失效其服务器缓存（见步骤 2）；对每个实例，取任一 ref manager 重新解析该 `serverID`：找不到、`!Enabled`、`Invalid != ""` 或指纹不同 → 从表中删除并后台 `Shutdown`。
9. **`releaseFor(m)`**（`Manager.Close` 调用）：从所有实例的 `refs` 删除 `m`；`refs` 为空的实例删除并后台 `Shutdown`。
10. **`Close()`**：停 janitor；关闭所有 manager；并行 `Shutdown` 全部实例，总等待上限 10 秒（超时写 `slog.Warn`）。

### 步骤 2：`manager.go` —— 服务器解析缓存与闸门

```go
// Manager 增加字段（均受 m.mu 保护）：
//   cache      []ServerConfig; cacheCfg *appcfg.Root; cacheEnabledMod time.Time
//   late       map[string]*lateSession   // key: conversation session id
//   lastSweep  time.Time; gitSnapshot map[string]time.Time
//   detected   map[string]detectEntry     // for Snapshot, refreshed in the background
```

- `servers() []ServerConfig`：若 `pool.config()` 指针与 `cacheCfg` 相同且 `enabled.json` 的 mtime 未变，返回缓存；否则 `LoadEnabled` + `ResolveServers(ResolveInput{Config, Enabled, GOOS: runtime.GOOS})` 重建。`invalidate()` 清空缓存（`Reconcile`、`SetEnabled` 调用）。
- `gated() bool`：`!closed && pool.config().EffectiveFeatures().LSP && opts.Trusted && opts.ProjectRoot != ""`。
- `Handles(abs)`：`gated()` 且 `ResolveRoot(abs, ProjectRoot, …)` 的项目内判断成立（用任意 ServerConfig 调一次即可判断「在项目内」，或直接用 `SamePath` 前缀判断）且 `ServersForFile` 返回 primary 或 diagnostics 非空。
- `NewManager`（在 `pool.go`）追加：
  1. 对 `PIDFile(opts.AgentWorkspace)` 做一次 `sweepOrphans`（每个进程每个路径只做一次，用包级 `sync.Map`；在后台 goroutine 中执行）。
  2. 若 `gated()`：对 `Enabled && Prewarm && Invalid == ""` 的服务器，若项目根目录下存在其任一 `RootMarkers`（只看项目根这一层），后台 `acquire`（ctx 为 `context.Background()` 加 `StartupTimeout`）。**`NewManager` 必须立即返回。**

### 步骤 3：`manager.go` —— `DidWrite`（等待窗口，规范 §8.3）

按下列顺序实现（每一条都对应规范条文，注释里写上条号）：

1. `eff := cfg.EffectiveLSP()`；`!gated() || !eff.AfterEdit` → 返回零值。`start := now()`，`deadline := start.Add(eff.Wait)`；`windowCtx, cancel := context.WithDeadline(ctx, deadline)`。
2. 分组：对每个 change，`ServersForFile` 得到 primary（若 `Diagnostics`）与 diagnostics 型服务器；每个 `(server, root)` 为一组。所有文件都无人负责 → 返回零值（任务 13 会在这里加推荐调用，本任务不加）。
3. 在迟到队列里登记：`sid := agentSessionID`（参数；为空时不登记），对每个被写文件记录 `edited[path] = now`（`eff.LateDelivery` 为 false 时不登记）。
4. 每组并发（`sync.WaitGroup`）执行：
   1. `entry, err := pool.acquire(windowCtx, m, srv, root)`；超时或错误 → 该组文件全部记为 pending（错误写 `slog.Debug`），结束该组。启动仍在后台继续，之后到达的诊断走迟到路径。
   2. `begin(entry)`/`defer end(entry)`；`snap := pool.diags.Snapshot(srv.ID)`；`seq0 := pool.diags.Seq()`。
   3. 对该组每个文件（按传入顺序）：
      - `After == nil`：`docs.Close` 并 `diags.Forget`，跳过。
      - 基线：`docs.IsOpen(path)` → `base = snap[path]`；否则 `Before != nil` → `v := docs.OpenWith(Before)`，`WaitFor(path, v, seq0)` 的等待上限为 `min(deadline, start + eff.Wait/2)`，等到则 `base = Get(path)`，否则 `baselineUnavailable = true`；否则（新文件）`base = nil`。
      - `seq1 := Seq()`；`v := docs.Change(After)`；`docs.Save(path)`。
      - 收集：`PullDiagnostics(windowCtx, …)` 返回 `true` 则已完成；否则 `WaitFor(windowCtx, path, v, seq1)`，返回 false → pending。
   4. 所有文件处理完后，若窗口未到期：`WaitQuiet(windowCtx, srv.ID, 300*time.Millisecond, deadline)`（`quietWindow` 包级变量，测试可调）。
   5. 结果：对每个被写且未 pending 的文件 `NewProblems(base, current)`；对 `PublishedSince(srv.ID, seq0)` 中未被写、在项目根内的文件 `NewProblems(snap[path], current)`。
   6. 迟到基准：把这些文件的「当前完整问题列表」记为该会话已知（见步骤 5）。
5. 合并各组：问题、pending 文件（去重）、贡献服务器 id（排序）、`baselineUnavailable`（任一组为真即真）。
6. `ReportOptions{ProjectRoot, MinSeverity: SeverityThreshold(eff.MinSeverity), MaxPerFile, MaxFiles, FirstPaths: 被写文件按传入顺序, Position: …}`，`Position` 用该问题所属实例的 `docs.Content(path)`（没有则读盘）与 `inst.Encoding()` 调 `FromLSPPosition`，读不到内容时用 `Line+1, Character+1`。
7. `text, summary := FormatEditDiagnostics(…)`，返回 `tool.DiagnosticsDelta{Text: text, Summary: summary}`。
8. 整个函数不得 panic、不得返回错误：内部错误写 `slog.Debug` 并按「没有诊断」处理。

### 步骤 4：`manager.go` —— `DidRead` 与 `DidRunShell`

- `DidRead(ctx, abs, content)`：`gated()` 且有 primary；在 pool 表中**只查找**（不 acquire）该 `(server, root)` 的已就绪实例；有则 `go func() { ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second); begin; docs.EnsureSynced(ctx2, abs); end }()`。绝不启动实例，调用方立即返回。
- `DidRunShell(ctx)`：`gated()`；距上次扫描不足 1 秒直接返回；否则记录时间并 `go m.sweep()`：
  1. 对本 manager 引用的每个已就绪实例，对其打开的每个文档调用 `EnsureSynced`（mtime 变化会触发 `didChange`）。
  2. 若项目根是 git 仓库（存在 `.git`）：执行 `git -C <ProjectRoot> status --porcelain=v1 -z --untracked-files=all`（2 秒超时），得到路径集合，对每个路径取 mtime；与上次的 `gitSnapshot` 比较得出 created（1）/changed（2）/deleted（3）；首次扫描只建立快照不发通知；每次最多处理 2000 个路径。
  3. 对每个实例：`inst.Registrations("workspace/didChangeWatchedFiles")` 为空则跳过；否则把变化中「未打开」且匹配任一 watcher `globPattern` 的文件发 `workspace/didChangeWatchedFiles {"changes": [{"uri", "type"}]}`。glob 匹配实现 `matchWatcherGlob(pattern, relPath string) bool`：支持 `**`（任意层目录）、`*`、`?`、`{a,b}`；`globPattern` 为对象（`RelativePattern`）时用其 `pattern`，相对路径以 `baseUri` 转成的目录为基准。

### 步骤 5：`manager.go` —— 迟到诊断队列（规范 §8.4）

```go
type lateSession struct {
	edited   map[string]time.Time  // file -> last edit time (kept 30 minutes)
	known    map[string][]Problem  // what the model was last told, per file
	pending  map[string]LateChange // not yet delivered
	token    uint64                // bumps on every pending change
	acked    uint64
}
```

- `NewManager` 时（`eff.LateDelivery` 为真）订阅 `pool.diags.Subscribe`；`Close` 时取消。回调：对每个 `edited` 中含该路径（且 30 分钟内）的会话，取当前问题 `cur`，`new := NewProblems(known[path], cur)`：`new` 非空 → `pending[path] = LateChange{Path, New: new}`；`new` 为空但 `known[path]` 非空且 `cur` 为空 → `pending[path] = LateChange{Path, Cleared: true}`；有变化时 `token++`。**正在该会话的 `DidWrite` 窗口内的文件不产生迟到项**（`DidWrite` 期间在会话上标记 `inWindow[path]`，窗口结束后清除并按步骤 3.4.6 更新 `known`）。
- `PeekLate(sid)`：`pending` 为空返回 `("", 0)`；否则按路径排序，用 `FormatLateDiagnostics` 渲染（`ReportOptions` 同步骤 3.6，`FirstPaths` 为空），返回 `(text, token)`。
- `AckLate(sid, token)`：若 `token == 当前 token`，把每个 pending 的文件并入 `known`（New 追加、Cleared 置空）并清空 `pending`；若 `token` 较旧，不做任何事（期间又有新变化，下次一起送）。
- 清理：每次访问会话时删除超过 30 分钟的 `edited` 项；会话没有任何 `edited` 与 `pending` 时删除该会话。

### 步骤 6：`control.go` —— 快照与开关

- `Snapshot()`：对 `servers()` 的每个服务器生成 `event.LSPServerStatus`：基本字段来自 `ServerConfig`；`BinaryPath`/`Version`/`InstallCommand` 来自 `m.detected`（没有或超过 60 秒时触发一次后台 `Detect`，完成后通知订阅者）；`State`：
  - 有实例（任一 root）→ 按实例状态映射：`starting`/`initializing` → `starting`，`indexing` → `indexing`，`ready` → `ready`，`failed` → `failed`；`Roots`、`PIDs`、`OpenDocuments`（`docs.Count()` 之和）、`IndexingPercent`（`Progress()` 最大值，-1 记 0）、`LastError`、`LogPath`、`Errors`/`Warnings`（`diags.Counts`）。
  - 无实例：`Invalid != ""` → `blocked`（`Note = Invalid`）；未启用 → 已探测且已安装为 `available`，否则 `not_installed`；已启用 → 已探测且未安装为 `not_installed`，否则 `stopped`。
  - `Servers` 排序：已启用在前，然后按 ID。其余快照字段同骨架（`RecommendationsDisabled` 等留给任务 13，保持 false）。
- `Subscribe`：已有；在实例状态变化、探测完成、`SetEnabled`、`Restart`、`Reconcile` 后以 100ms 去抖调用订阅者（在 goroutine 中调用，不持锁）。
- `SetEnabled(id, on)`：`id` 不在 `servers()` 中 → `fmt.Errorf("unknown language server %q", id)`；`SaveEnabled`；`invalidate()`；`on == false` 时关闭本项目中该服务器的所有实例；通知订阅者。
- `Restart(id)`：未知 id 报错；对本项目中该服务器的每个实例 `inst.Restart(ctx)`（ctx 带 `StartupTimeout`）；没有实例返回 nil。
- `ReleaseIdle()`（manager.go）：对本 manager 引用、`inflight == 0` 的实例，若 `lastUsed` 早于 1 分钟 → 关闭（gateway 的 RunnerPool 在会话安静时调用它，比 janitor 的 `idle_timeout` 更积极）。

### 步骤 7：更新 `doc.go`，写测试

`doc.go` 对 `pool.go`、`manager.go`、`control.go` 的描述补充：instances are started lazily, shared across runners of one project, capped by lsp.max_servers and stopped when idle; DidWrite implements the diagnostics wait window。

测试一律用任务 05 的假服务器，经**自定义服务器配置**接入（`cfg.LSP.Servers["fake"] = {Command: <fake 路径>, ExtensionToLanguage: {".fk": "fake"}, Env: {"FAKE_LSP_SCRIPT": …}}`——注意 `Env` 走 `BuildEnv`，`FAKE_LSP_SCRIPT` 名字良性可通过）；`ManagerOptions{Trusted: true, ProjectRoot: t.TempDir(), AgentWorkspace: t.TempDir()}`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race -timeout 8m` → `ok`。

### 步骤 8：全量与包图

**验证**：`gofmt`、`go vet ./pkg/lsp`（含 `GOOS=windows`）干净；`scripts/package-graph.sh` 后提交 `graph.json`；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

- `pool_test.go`
  - `TestAcquireSharesInstance`：两个 manager（同项目）对同一文件各 `DidWrite` 一次 → 假服务器只启动一次（record 中一次 `initialize`）。
  - `TestMaxServersEvictsIdle`：`max_servers: 1`，两个不同服务器先后使用 → 第一个被关闭；第一个仍在请求中（`hang`）时第二个的调用得到 `too many language servers` 错误文本。
  - `TestJanitorStopsIdle`：`janitorInterval` 10ms、`idle_timeout` 60（配置最小值）不便测试时，直接调用 janitor 的单步函数并注入 `now`，断言实例被关闭。
  - `TestReconcileRestartsOnFingerprintChange`：改自定义服务器的 `args` 后 `Reconcile` → 旧实例关闭；下一次使用新起实例（record 中两次 `initialize`）。
  - `TestPoolCloseStopsEverything`：`Close` 后假服务器进程消失（Unix）。
  - `TestMultiRootReuse`：capabilities 声明 `workspace.workspaceFolders.changeNotifications: true`，两个不同根 → 只一个进程，record 中有 `didChangeWorkspaceFolders`。
- `manager_test.go`
  - `TestDidWriteReportsNewProblemsOnly`：假服务器规则：文本含 `bad` 推送一条 error；文件原本含一个 `bad`（基线），编辑后含两个 → 结果只报 1 条新问题；`Text` 以 `<diagnostics>` 开头，首行 `1 new problem after this edit (fake)`。
  - `TestDidWriteNoNewProblemsAddsNothing`：编辑不引入问题 → `Text == ""`。
  - `TestDidWriteWindowExpiresToPendingAndLate`：`diagnostics.delay_ms: 400`、`wait_ms: 100` → 结果含 pending 行；随后 `PeekLate(sid)` 在诊断到达后返回以 `Language server diagnostics changed` 开头的文本；`AckLate` 后 `PeekLate` 为空。
  - `TestDidWriteWaitZero`：`wait_ms: 0` → 立即返回（< 50ms），有 pending；迟到队列之后收到。
  - `TestDidWritePullMode`：`pull_diagnostics: true` → 不依赖推送也能报问题。
  - `TestDidWriteBaselineUnavailable`：未打开文件、`delay_ms` 大于窗口一半 → 首行为「could not be told apart」形态。
  - `TestDidWriteCrossFile`：编辑 a.fk 后假服务器同时为 b.fk 推送新问题 → b.fk 也出现，且 a.fk 在前。
  - `TestDidWriteNoServerIsFree`：未启用任何服务器 → 立即返回零值，没有进程启动。
  - `TestDidWriteUntrustedOrOff`：`Trusted: false` 或 `features.lsp: false` → 零值。
  - `TestDidWriteCancel`：ctx 在窗口中途取消 → 立即返回，写入不受影响。
  - `TestLateDeliveryOffRecordsNothing`。
  - `TestDidReadNeverStarts`：没有运行中实例时 `DidRead` 不启动进程；有实例时 record 出现 `didOpen`。
  - `TestDidRunShellResyncsOpenDocs`：打开文档后在磁盘上改它，`DidRunShell` 后 record 出现 `didChange`。
  - `TestPrewarmDoesNotBlock`：`prewarm: true`、`never_initialize: true`、项目根有根标记 → `NewManager` 在 100ms 内返回。
  - `TestMatchWatcherGlob`：`**/*.go`、`*.{ts,tsx}`、`src/?.go`、`RelativePattern` 各若干正反例。
- `control_test.go`（替换骨架测试中已不成立的断言）
  - `TestSnapshotStates`：未启用未安装 → `not_installed`；启用 + 运行 → `ready` 并有 `PIDs`、`LogPath`；自定义条目缺字段 → `blocked` 且 `Note` 非空。
  - `TestSetEnabledPersistsAndStops`：`SetEnabled("fake", false)` → `enabled.json` 写入 false；运行中的实例被关闭；未知 id 报错。
  - `TestRestart`：record 中出现第二次 `initialize`。
  - `TestSubscribeNotified`：`SetEnabled` 后 200ms 内订阅者被调用。

## 完成判据

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -count=1 -race -timeout 8m` 通过（至少 25 个与本任务相关的测试）
- [ ] `grep -n "errNotAvailable" pkg/lsp/control.go` 只剩 `Install`、`DecideRecommendation`、`ResetRecommendations` 三处使用
- [ ] `pkg/lsp` 仍只导入 `config`、`event`、`home`、`tool` 四个本仓库包（`go list -f '{{join .Imports "\n"}}' ./pkg/lsp | grep forebrain-harness`）
- [ ] `gofmt`、`go vet`（含 `GOOS=windows`）干净；`graph.json` 已提交；全量测试通过
- [ ] README 中任务 08 状态为 `DONE`

## STOP 条件

- 需要 `pkg/lsp` 导入 `pkg/llm`、`pkg/safety` 等新包。
- 等待窗口相关测试在两次修复后仍然不稳定（说明等待/唤醒设计有竞态，需要复查任务 07 的存储）。
- 需要改 `pkg/process` 或 `pkg/run`（接线已在任务 03 完成）。

## 维护说明

- 会话标识：`DidWrite` 的 `agentSessionID` 参数与 `PeekLate` 的参数都是 `llm.AgentSessionIDFromContext` 的值（由 `pkg/tool` 与 `pkg/run` 取）。两处取值必须一致，否则迟到提醒永远取不到。
- 所有进入模型的文本都经 `FormatEditDiagnostics` / `FormatLateDiagnostics`，本任务不得自行拼接模型可见文本。
- 评审重点：启动不受调用方 ctx 取消影响；`DidRead`/`DidRunShell` 立即返回；锁的粒度（订阅回调不在锁内）；janitor 与 `Close` 的竞态。
