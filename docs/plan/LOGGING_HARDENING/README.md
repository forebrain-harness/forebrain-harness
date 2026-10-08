# 日志加固（gateway / debug.log / info.log）：实施计划索引

由 improve skill（branch 变体）于 2026-10-08 生成，基于 commit `eafaf46`。

**审计范围**：本仓库当前**在 `main`、领先 origin/main 0 个提交**，因此"分支"= 未提交的
工作区 diff。工作区里同时存在两套互不相干的改动：

- **A（本批计划的来源）**：网关控制台日志重设计 —— `cmd/forebrain/{console_log,console_log_test,gateway_log}.go`（未跟踪新增）、`cmd/forebrain/{root,serve}.go`、`pkg/gateway/{access_log,access_log_test,controlplane,http_server,http_server_test,serve_run}.go`、`pkg/channel/{channel,channel_test}.go`、`pkg/architecture/testdata/graph.json`。
- **B（**不**属于本批计划）**：npm 打包脚本 UI 重建 —— `Makefile`、`.github/workflows/release.yml`、`npm/scripts/build-platform-packages.sh`、`docs/plan/NPM_PACKAGE_UI_REBUILD_PLAN.md`，以及被重建的 `pkg/gateway/dist`。

计划里引用的 file:line 以制定时的工作区为准；未跟踪文件在 `eafaf46` 上不存在，drift check 只比对内容。

## owner 决策（2026-10-08，已拍板，勿再讨论）

| 决策点 | 结论 |
|---|---|
| 日志写入性能 | **debug.log 与 gateway.log 的性能问题都必须解决**（原"待裁决"项转必做），修在 `pkg/telemetry` 共享原语上，两处同时受益 |
| 断电耐用性取舍 | **接受**：去掉逐条 fsync 后，进程崩溃不丢（page cache），断电/内核崩溃可能丢失最近若干秒的日志尾部。与同包 `error.log`（panic/错误取证，本来就零 fsync）一致。**不要**为此加定时器 |
| 计划落盘位置 | 本批计划**只**生成在 `docs/plan/LOGGING_HARDENING/`，不得写入 `plans/` 或 `docs/plan/` 根目录 |

## 执行顺序与状态

| 计划 | 标题 | 优先级 | 工作量 | 依赖 | 状态 |
|---|---|---|---|---|---|
| [001](001-log-file-permissions.md) | `gateway.log` 改为仅属主可读（0644 → 0600） | P1 | S | — | DONE |
| [002](002-log-write-performance.md) | 日志写入不再为每条记录付出一次 fsync | P1 | S-M | 001（同改 `gateway_log.go`） | DONE |
| [003](003-console-format-fidelity.md) | 控制台日志：解析 `LogValuer`、保证一条记录一行 | P1 | S | — | DONE |
| [004](004-console-sink-resilience.md) | 一个日志目的地写失败不得静默另一个（含 `io.MultiWriter` 同类缺陷） | P1 | S | 003（同文件） | DONE |
| [005](005-panic-request-observability.md) | panic 的请求必须留下访问日志行，且 panic 栈必须进入日志 | P1 | S-M | 003（多行消息需要 003 的转义） | DONE |
| [006](006-access-log-middleware-seam.md) | 访问日志中间件下沉 `pkg/telemetry`，并接入沙箱代理与 MCP 回调 | P2 | M | 005、003（移动的是修好的版本） | DONE |
| [007](007-console-handler-layering.md) | 控制台日志格式移出 CLI 入口（`cmd/forebrain` → `pkg/telemetry`） | P2 | M | 003、004（同文件）；建议排在 006 之后 | DONE |

## 执行记录（2026-10-08，execute 变体，全部经 reviewer 逐计划审定）

- 001–007 全部 DONE；每个计划由独立执行者在当前源码树实施（零 commit，改动留工作区待 owner 审阅），reviewer 重跑各计划 done criteria 并亲读全部 diff。
- 002 实测（Apple M4，`-benchtime 500x`）：120B 记录 3,652 µs → **1.6 µs**；37KB 记录 3,695 µs → **13.4 µs**；8 并发 3,612 µs/op → **2.7 µs/op**。
- 004 修订一轮：删除 `multiWriter` 中不可达的 `w == nil` 防御分支（owner 铁律：不为不可达状态写防御代码）。
- 006/007 的 `graph.json` 统一在批末再生成一次：终态差异恰为 +2 边（`safety→telemetry`、`mcp→telemetry`）、`telemetry` fan_in 5→7、`mcp`/`safety` fan_out 各 +1、四包 loc（gateway −118 / telemetry +602 / safety +11 / mcp +3），无其它漂移。
- 已知遗留红点（非本批引入）：`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles` 因 owner 的 WIP 文件 `pkg/tui/performance_test.go`（尚无对应 `performance.go`）而失败；owner 的 tui 改动落定后自愈。
- `pkg/tui` 曾出现一次不可复现的偶发失败（002 执行期间，三测试二进制并行时）；随后 5 次运行（含本批两次全量）全绿，未追因。

状态取值：TODO | IN PROGRESS | DONE | BLOCKED（附一行原因）| REJECTED（附一行理由）。

## 全局执行纪律（每个计划都适用）

1. **禁止提交**：不得运行 `git commit` 或任何产生提交/改写历史的命令（amend、merge、rebase）。改动留在工作区，由 owner 手动审阅提交。
2. **不要碰无关的未提交改动**：工作区同时有变更集 A 与 B（§审计范围）。只改计划点名的文件，不格式化或重排整文件；`gofmt` 只对改过的文件跑，并确认 diff 只含自己的改动。发现别人正在改同一文件 → STOP 报告。
3. **测试形态唯一可信**：`CGO_ENABLED=1 go test -tags fts5`（本仓 CGO 强制；无 fsync 相关测试可用 `-race`）。
4. **通过门禁**：`gofmt -l cmd pkg` 为空（`pkg/tui/render.go` 在 `eafaf46` 上本就不合规，属既有）→ `go vet ./...` 为 0 → 全量 `./...` 回归 ok → `scripts/package-graph.sh` 的 `graph.json` 与提交内容一致（006 与 007 **会**改变它，见各自计划）。
5. **prompt-cache 不变量**：`git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` 必须为空。
6. 修根因不修症状；同类组件一起修；**删掉的语义无痕迹删除**（不写"断言不再出现"的负向测试，调用链 deadcode 一起删）；不为不可达状态写防御代码。

## 依赖与顺序说明

- **001 → 002**：两者都改 `cmd/forebrain/gateway_log.go`（001 加 `Perm:`，002 删 `SyncWrites:`）。001 先落，002 的 diff 才只剩它自己那一行。
- **003 → 004**：都在 `cmd/forebrain/console_log.go`；003 定格式（消息转义、值解析），004 改 sink 写入语义。
- **003 → 005**：`005` 的 net/http panic 报告天然是多行的，靠 003 的 `messageText` 才落成一条记录。
- **005、003 → 006**：006 是"移动修好的中间件"，若前两者未落，移动会把修复丢掉（006 的 STOP 条件里有一条前置检查）。
- **006 与 007**：都往 `pkg/telemetry` 搬东西，都是纯移动。建议 006 先、007 后，让每个 diff 只讲一件事；不要合并成一个提交。
- **002 与 006 无依赖，但有量级关系**：006 让沙箱出口代理也开始记访问日志，日志量上升——002 正是让这份量变便宜的那一步。

## 本轮实测证据（制定计划时的关键事实）

用**真实代码路径**（临时探针直接调 `telemetry.Open` + `File.WriteSection`，跑完即删，工作区零残留）在 Apple M4 / APFS SSD 上测得：

| 场景 | 每条记录耗时 |
|---|---|
| `debug.log` 典型 37KB 记录，现状（含 fsync） | **3,798 µs** |
| 同记录，去掉逐条 fsync | 20.8 µs（**183×**） |
| `gateway.log` 访问日志 120B 记录，现状 | **3,697 µs** |
| 同记录，去掉 fsync | 3.9 µs（**938×**） |
| 8 并发写，现状 | 3,741 µs/op（**并发度为零**） |
| 8 并发写，去掉 fsync | 9.7 µs/op |

真实负载：`debug.log.1` = 20,932,542 B / 565 条 → 单条均值 **~37KB**；单个 20MB 分片约 10 分钟写满（mtime 18:54 → 19:04），即约 1 条/秒、突发期更高。绝对值随设备变化（HDD / overlayfs 上 fsync 更贵），**可迁移的是比值与并发行为**。

根因：`pkg/telemetry/logfile.go` 的 `File.Write` 对每条记录做 `fstat + write + fsync`，且 fsync 在**进程级全局 `rotateMu`** 内。**反证**：同包 `error.log`（panic/错误取证）用裸 `f.WriteString`、**零 fsync**（`paniclog.go:44`、`error_write.go:101`、`slog_handler.go:56`），故"逐条 fsync 是刻意设计"不成立。

## 本轮同时考虑并否决（勿重审）

- **`gateway.log` / `debug.log` / `info.log` 统一 0600 的"一刀切"**：改 `Options.normalized()` 默认值会连带改变 `debug.log` 等既有文件的权限语义。001 只钉调用点，一刀切另议（已记入 001 的 maintenance notes）。
- **给日志加用户态缓冲（`bufio.Writer` + 定时 flush）**：会让 `tail -f` 滞后、崩溃时丢掉最想看的尾部——恰好摧毁 debug.log 的存在理由，并引入"缓冲积压"新故障模式。
- **异步队列 + 后台写入 goroutine**：机器更多、需定义关闭顺序与丢日志语义，而"直接不 fsync"已取得同等收益。
- **只降低日志量级 / 缩小 `DefaultRecordBytes`**：治症状——只要还有一条记录走 fsync，屏障就在；且削弱取证能力。
- **只关 `gateway.log` 的 fsync**：根因在共享原语，逐处打补丁会留下下一个受害者（`info.log`）。
- **`WriteRaw` / `RawWriter` 的 TUI 裸描述符路径**：保留 per-call fstat（非热点、本就无 fsync），不扩大 diff。
- **`File.Sync()` 公开方法无生产调用方**：保留不删，避免在同一计划里扩大公开 API 变动面。
- **为 panic 增加恢复（recovery）中间件**：会向可能已部分写出的响应补写 500，并改变 net/http 的既有契约（关闭连接、继续服务）。005 只"观测并原样放行"。
- **`pkg/llm/openai/auth.go` 的 provider OAuth 回调也接入访问日志**：**不可行** —— `pkg/telemetry` 依赖 `pkg/llm`（`go list -deps` 证实），故 `pkg/llm` 不能 import `pkg/telemetry`。要覆盖它需把中间件放进更底层的**新叶子包**（会新增包与 `doc.go`、改变 package graph），属独立决策。已记入 006 的 maintenance notes（Deferred）。
- **给访问日志加采样率 / 每服务开关**：`LOG_LEVEL`/`FOREBRAIN_LOG_LEVEL` 已是既有杠杆（访问日志是 INFO），够用，不加新旋钮。
- **重命名访问日志的 `path` 字段**（沙箱代理的 `CONNECT` 形态下它承载 host:port）：会破坏已定格式。006 保留字段名，用文档说明例外。
- **`scripts/check-webui-dist.sh` 不存在**（006 相关检查曾被怀疑）：该脚本**存在**（`scripts/`），是误报。
- **`pkg/tui/render.go` 未过 `gofmt`**：`eafaf46` 上即如此（`git show HEAD:pkg/tui/render.go | gofmt -l` 非空），既有问题，未纳入本批。
- **`telemetry.Options.Perm` 默认 0644 让既有 `debug.log` 也可读**：既有行为，本批只修新增文件（001）。
- **`Attr.Equal` 弃用**：Go 1.26.6 `go doc` 显示未弃用。
- **`statusRecorder` 无条件实现 `Flusher`**：仓内无 SSE / 流式服务端端点，且 `Unwrap` 保证 `http.ResponseController` 正确；理论问题，不立项。
- **访问日志按 key 名的颜色策略会误伤其它日志点**：已核 —— `bytes`/`duration` 两个 key 目前**只有**新增的访问日志在用，`status`/`error` 站点只着色不改值。
- **旋转与已打开描述符的写入**：`rotateActiveLocked` 是同描述符 copy-and-truncate，不存在"写入已改名文件"的问题。
- **`clientIP` 采信 `X-Forwarded-For` 可被伪造**：既有行为（限流器本来就用它），006 原样平移，不改变信任语义。
- **变更集 B 携带重建过的 `pkg/gateway/dist`**：会被 `scripts/check-webui-dist.sh` 在非 release PR 上拦下（179 个 tracked 删除 / 209 个新增 / 2 个修改）。修复是 `git checkout origin/main -- pkg/gateway/dist`，但在变更集 B 的边界内，**不属本批计划**。

## 未审计

`branch` 变体范围：变更集 A/B 及其直接调用方之外的全仓、frontend 源码、B 改动文件之外的 npm 发布链路、`pkg/safety`/`pkg/mcp`/`pkg/llm/openai` 内部深审均未覆盖。
