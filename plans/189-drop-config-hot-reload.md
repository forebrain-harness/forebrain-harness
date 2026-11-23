# 189 — 删除文件热更新机制：fsnotify watcher、ConfigManager、文件 reload 链全部退场

- **基线**：`6f68cb21`（dev）＋ **前置：187、188 已落地**。执行前 `git log --oneline -8` 确认；未落地则 STOP。
- **状态**：TODO
- **依赖**：187（合成配置 + turn 同步已接管生效）、188（所有写面已不写 YAML——文件 reload 已无事可做）。

## 0. 背景与目标

owner 裁决：热更新机制**废弃并删除**。生效语义已由 187 的每 turn DB 快照同步接管。本计划删除文件侧整条链，落下最终语义：

- 手编 forebrain.yaml（只剩 gateway/credentials/http_addr）→ **重启 TUI/gateway 进程才生效**。
- TUI/Web/接口写配置 → 写 fb_config → 下一个 turn 生效（187 已交付，本计划不重做）。

## 1. 删除清单（基线已逐个核实）

| # | 删除对象 | 位置 | 依据 |
|---|---|---|---|
| D1 | `StartConfigHotReload`、`clearConfigHotReload`、`stopConfigHotReload`、`Environment.watchStop/watchID/watchMu` 字段 | `pkg/process/config_reload.go:21-135`；字段在 `pkg/process/open.go` 的 Environment 结构 | fsnotify watcher 全部 |
| D2 | 两个启用点 | `pkg/tui/notify.go:1734`、`pkg/gateway/serve_run.go:65` | 各一行调用 |
| D3 | `ConfigManager` 全部（struct/New/SetProbe/Pending/Request/Idle/applyIfIdle）、`ActiveRunProbe`、`Environment.reload` 字段、`configManager()`、`RequestConfigReload`、`ConfigIdle`、`SetActiveRunProbe` | `pkg/process/config_manager.go`（整文件）、`config_reload.go:137-186`、`open.go` 装配处（`NewConfigManager(env.Control, env.reloadConfig)`） | run 活跃时 defer 重载的协调器——turn 边界同步天然不在 run 中间执行，协调器无存在必要 |
| D4 | `Environment.ReloadConfig` / `reloadConfig`（文件源 reload 入口） | `config_reload.go:188-206` | 187 已把 watcher 的 reload 改走合成源；本计划连 watcher 一起删，此入口无调用方 |
| D5 | `ChatSession.ReloadConfig` / `reloadConfigFromDisk` / `applyConfigFromDisk` / `reloadConfigAfterRun`、`s.configApplyMu` | `pkg/tui/chat_session.go:2602-2682,2696+`；`notify.go` 中 `OnConfigReload` 回调旁的装配 | 188 后 slash 写面直写 DB，文件 reload 缝已无调用方（`chat_slash.go` 的 `reloadConfigFromDisk` 调用点在 188 已迁走） |
| D6 | TUI 的 `OnConfigReload` 回调注册 | `pkg/tui/notify.go:1700-1710` | 回调本身保留（见 R2），注册点改由 turn 同步路径触发 |
| D7 | 测试文件：`config_reload_test.go`、`config_manager_test.go`、`session_context_test.go`/`notify` 里对 Start/StopConfigHotReload 的引用 | `pkg/process`、`pkg/tui` | 随实现删 |
| D8 | `fsnotify` 依赖 | `go.mod`（唯一使用者是 `config_reload.go:18,39`；`pkg/tui/run.go:639` 的 stopWatch 是剪贴板 watcher 的 `context.WithCancel`，与 fsnotify 无关——已核实） | `go mod tidy` 后 `go.mod`/`go.sum` 无 fsnotify |

## 2. 保留与改名的（不是删除，是归位）

| # | 对象 | 处置 |
|---|---|---|
| R1 | `applyRawConfigLocked` | **保留**：它现在是 `SyncConfigFromDB`（187）的 apply 内核。改名 `applyComposedConfigLocked` 并把注释从"文件 reload"改为"turn 边界 DB 快照应用"；内部逻辑零改动（含 MCP 冻结、pool 传播、回滚/unhealthy fail-closed） |
| R2 | `OnConfigReload` 回调 + `RunEventConfigReloaded` 事件 | **保留**：turn 同步在快照实际变化时仍要让 session 采纳指针、仍要发事件（可观测）。TUI 的采纳回调注册移到 session 构造中不依赖 watcher 的位置 |
| R3 | `AdoptConfig` / `AdoptLaunchTrust` | **保留不动**（slash 面 188 之后仍可能显式采纳；trust 采纳与配置应用共用 reloadMu 的锁序注释一并保留） |
| R4 | `refreshSandboxRuntimeLocked` | 保留（R1 的组成部分） |

## 3. 步骤

1. **删 D1/D2/D4**：watcher 与文件 reload 入口。全仓 grep `StartConfigHotReload|stopConfigHotReload|clearConfigHotReload` 应只剩测试（随后删）。
   验证：`go build ./...`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1`（预期编译错误由 D3/D5/D7 一并清完才绿，本步到 D7 是一个原子改动簇）。
2. **删 D3**：ConfigManager 及装配。`grep -rn "ConfigIdle\|RequestConfigReload\|SetActiveRunProbe" pkg/ cmd/ --include='*.go' | grep -v _test` 归零。
3. **删 D5/D6**：TUI reload 缝；`OnConfigReload` 注册移位（新位置：session 构造里紧跟 `s.adoptConfig` 可用之处，语义=每次快照应用后采纳指针）。
4. **R1 改名**＋注释重写；`R2` 事件触发点确认在 `SyncConfigFromDB` 的 hash-变化分支。
5. **删 D7 测试、补新测试**：
   - `pkg/process/config_db_test.go` 补"turn 同步期间 run 活跃也不会中途打断"（原 ConfigManager 语义的等价回归：同步只发生在 Submit 入口，天然不在 run 中）；
   - "手编 YAML 不生效"回归：改 scratch home 的 YAML gateway 段 → 起进程 → 断言新值可见；进程不重启再改 → 断言不生效（无 watcher）。
6. **D8**：`go mod tidy`；`git diff go.mod go.sum` 只应见 fsnotify 相关行消失。
   验证：`go vet ./...`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（全量——核心链删改）。

## 4. 边界

- **不动**：`SyncConfigFromDB`/`WithConfigSync`（187）；所有写面（188）；`applyRawConfigLocked` 内部逻辑（只改名+注释）；YAML 格式与 187 导入器。
- **不做**：任何 UI。

## 5. 完成判据

1. 全量测试绿、`go vet` 无输出。
2. `grep -rn "fsnotify" go.mod go.sum pkg/ cmd/` 归零（go.mod/go.sum 与源码都不含）。
3. `grep -rn "StartConfigHotReload\|ConfigManager\|reloadConfigFromDisk\|saveAndReload" pkg/ cmd/ frontend/src 2>/dev/null | grep -v _test | grep -v plans/` 归零。
4. 真机（tmux，run-forebrain 驱动）：TUI 里改 `fb_config` 一行（合法）→ 下个 turn 生效日志/事件；改 YAML gateway 段 → TUI 无任何反应，重启后生效。
5. `scripts/package-graph.sh` 无 diff（若 fsnotify 移除影响 graph，重生成并说明）。

## 6. 维护注意

- 从此"配置为什么不生效"的排查树只有两枝：改的是 YAML（重启了吗）／改的是 DB（下一个 turn 了吗）。任何新代码想加"自动感知配置变化"，先回本文件看这条裁决。
- `RunEventConfigReloaded` 事件的语义已从"文件重载"变为"turn 边界快照应用"——事件消费者（若有 UI 徽标）文案要跟 191 对齐。

## 7. 逃生口

- 发现 `reloadConfigFromDisk`/`ReloadConfig` 在 188 之后仍有调用方（某个没迁到的 slash）→ 停，列调用方清单，先补 188 的遗漏再回来删。
- 发现 `pkg/migrate` 或 CLI 子命令依赖 `Environment.ReloadConfig` 做导入后刷新 → 停，报告该路径，改走"导入后下次 Open 生效"的显式语义再删。
