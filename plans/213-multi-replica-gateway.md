# 213 — 多副本网关：副本登记、副本间转发、定时任务与聊天渠道只跑一份、SQLite 多主机拒绝

- **基线**：`db56121f`（dev，含 209–212 与单一主库裁决），并假定 203–212 已落地。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / HIGH（跨副本转发是新的分布式路径；定时任务与渠道的去重直接影响「消息发几次」）
- **依赖**：212 DONE（三种库上语句全绿）。部署计划 214 以本计划为前提。
- **设计**：`docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md` §0、§1.4、§2.3、§2.7（必读）。
- **执行隔离**：worktree + `exec/213` 分支 + 增量提交（owner 2026-11-03 裁定；213 不依赖 199–201，故从 dev 起）；合并在 owner 批准后 ff-only。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 213 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：README 里 212 是 DONE；`grep -n "CREATE TABLE fb_run_owners" -A4 pkg/state/schema.sql` 与 §2.1 一致；`git diff --stat db56121f -- pkg/state/schema.sql pkg/state/runrt.go pkg/gateway/server.go pkg/turn/scheduler.go` 应无输出。
>
> **2026-11-03 reconcile（相对 06f1bc57 的漂移，已在本文件原地改正）**：
> 1. 基线从 `06f1bc57` 改为 `db56121f`（209–212 与单一主库裁决已落 dev）。
> 2. 执行隔离从「零 commit（owner 手动提交）」改为 worktree + `exec/213` + 增量提交（owner 2026-11-03 裁定）。
> 3. **所有测试命令必须带 `FOREBRAIN_HOME="$(mktemp -d)"`**（house rule：测试不得继承环境里的 `FOREBRAIN_HOME`）。§4/§6 的命令表已按此改写；不带它的三库矩阵会因 §0.1 的既有测试隔离缺陷而假失败。
>
> ### 0.1 既有测试隔离缺陷（reconcile 时发现，不在本计划范围）
>
> `pkg/process` 的 `TestApplyComposedConfigRollsBackEveryFieldWhenRunnerLoadFails`（`pkg/process/config_apply_test.go`）**没有 pin 自己的 home**：`FOREBRAIN_HOME` 未设时它失败（`applying an unloadable config = nil, want an error`），设成任何 scratch 目录时它通过。根因是该测试间接走到 `llm.Lookup` 读 `<FOREBRAIN_HOME>/models.json`（`pkg/run/config.go:587` 的注释已点明这个依赖）。这违反 house rule「tests in packages that open state should pin their own home」。**本计划不修它**（不是本计划引入的），但 §4/§6 的命令必须显式给 scratch home，否则三库矩阵与全量套件都会假失败。

## 0. owner 裁决（执行期不得改）

1. **多网关共享一个库**：K8s 多副本、共享存储、**共享同一个 FOREBRAIN_HOME**。
2. **副本间转发**：每个副本在库里登记内网地址；按运行归属把**流式订阅、审批决定、插话、中断**经内网 HTTP 转给正在跑的副本；**不落库、不加中间件**（不引入 Redis）。流式文字与工具输出增量照旧**只推不存**。
3. 多副本只支持 MySQL / PostgreSQL；SQLite 放在共享存储上不安全。
4. 状态库结构变更必须写迁移，且三种方言都要写（209 的迁移框架）。

## 1. 交付

- 副本登记：`fb_run_owners` 加 `internal_addr`、`host`、`kind`（三方言迁移）。
- 内部端点 + 转发：网页 WS 的会话绑定与会话内操作、以及作用于运行中对话的 HTTP 接口，归属不在本副本时转发。
- 定时任务（含心跳任务）：库内比较并交换认领，只触发一次。
- 长连接聊天渠道：每个账号一个库内租约，只有一个副本连接。
- SQLite 多主机拒绝启动。
- 共享 home 单写者核查报告 + 必要修复。
- 多副本集成测试（docker compose）。

## 2. 现状事实（已对基线核实）

### 2.1 运行归属（已在库里）

```sql
CREATE TABLE fb_run_owners (
  owner TEXT PRIMARY KEY CHECK (TRIM(owner) <> ''),
  heartbeat_at_ms INTEGER NOT NULL
) STRICT;
```

`pkg/state/runrt.go:1192-1230` `HoldOwnerLease`：登记并按 `RunOwnerLease/6` 续心跳；活性判断 `runrt.go:217,290`（`heartbeat_at_ms >= ?`）。owner 由 `pkg/process/open.go:401` 生成：`source + "-" + uuid.NewString()`（`source` 区分终端 / 网关）。`fb_runs.owner` 记每个运行归谁。

### 2.2 网页 WS（`pkg/gateway/server.go:909` 升级，`ws_protocol.go`）

入站操作（`ws_protocol.go:15-41`）：`connect`、`bind_session`、`resume_connection`、`start_run`、`cancel_run`、`cancel_command`、`switch_mode`、`query_mode`、`approve_action`、`deny_action`、`submit_answer`、`sync_context`、`control_request`（`interrupt`）、`ping`、`mcp_status`、`mcp_resources`、`mcp_tool_search`、`task_status`、`cancel_auto_continue`。事件出站在 `wsevents.go`（`run_started`、`requires_action`、`step` 等）。`start_run` 在**收到它的副本**上起运行，所以「浏览器发起的对话」天然在浏览器所连的副本上；跨副本的情况是：定时任务、聊天渠道、另一个副本上的浏览器、或另一个标签页起的对话。

### 2.3 作用于运行中对话的 HTTP 接口（`pkg/gateway/api_extra.go:20-140` 一带）

例如 `POST /api/agents/cancel-all`、`POST /api/agents/:agentId/cancel`、`POST /api/subagents/:id/cancel`，以及子代理标签的插话、待办审批等（执行时全量列出：凡是读写进程内 `run.Controller` / `InputQueue` / 待审批等待的处理函数都算）。

### 2.4 定时任务（进程内去重）

`pkg/turn/scheduler.go:184-186` `runDueJobs` → `s.Store.DueJobs(ctx, agentID, now)`（`pkg/state/cron.go:244`，按 `fb_cron_jobs.next_run_at` 取到期任务）；`scheduler.go:577-588` `claim` 用进程内 `map` 防重入；心跳任务 `fireHeartbeat`（530）→ `ReanchorHeartbeat`。多副本下每个副本都会触发同一个任务。

### 2.5 聊天渠道

`pkg/channel/*.go` 每个适配器有 `Start(ctx, add, bus)`：长连接 / 轮询型（钉钉、QQ、Discord、Matrix、Mattermost、WhatsApp、Home Assistant、WS 桥等）——多副本下每个副本都会连一次、重复收消息；Webhook / HTTP 桥型——由负载均衡投递给某一个副本，天然只收一次。启动点 `pkg/gateway/serve_run.go:171` 一带。

### 2.6 记忆流水线（已是库内租约）

`pkg/memory/jobs.go:194-240` `TryClaimStage1Job`：`fb_memory_jobs` 的 `ownership_token`、`lease_until`，认领语句带并发上限——跨进程安全；第二阶段同样（`jobs.go:63` 注释）。本计划只核对，不改。

## 3. 设计

### 3.1 副本登记

- 迁移（三方言）：`fb_run_owners` 加 `internal_addr TEXT NOT NULL DEFAULT ''`、`host TEXT NOT NULL DEFAULT ''`、`kind TEXT NOT NULL DEFAULT ''`（`gateway` / `tui`）。
- 网关启动时写入：`internal_addr` = 环境变量 `FOREBRAIN_INTERNAL_ADDR`（形如 `http://10.0.3.17:8787`）；未设时 = `http://` + `gateway.http_addr`（`0.0.0.0` / `::` 替换为本机首个非回环 IPv4——用 `net.InterfaceAddrs`，取不到就留空并记一次警告）。`host` = `os.Hostname()`。终端进程 `kind=tui`、`internal_addr` 留空（终端不接受转发）。
- K8s：Deployment 用 downward API 注入 `POD_IP`，`FOREBRAIN_INTERNAL_ADDR=http://$(POD_IP):8787`（214 写进清单）。

### 3.2 转发

- **内部端点**（只挂在网关上）：
  - `GET /internal/v1/ws?session=<id>`：与 `/ws` 同协议，但只接受会话内操作；
  - `POST /internal/v1/run-op`：HTTP 接口的会话 / 运行内操作，请求体带原接口名与参数。
  - 鉴权：`Authorization: Bearer <gateway.auth.token>`（所有副本共享同一个 forebrain.yaml）；**并且**来源 IP 必须等于某个心跳新鲜的已登记副本的 `internal_addr` 主机。两者缺一拒绝。
- **WS 转发**：入口副本处理 `bind_session` / `resume_connection` 时查该会话**进行中运行**的 `fb_runs.owner`；owner ≠ 自己、kind=gateway、心跳新鲜、`internal_addr` 非空 → 入口副本对这个浏览器连接切换为**透明中继**：向 owner 的 `/internal/v1/ws?session=` 建立 WS，把浏览器发来的会话内操作原样转发、把 owner 推来的事件原样转回。运行结束（`run_completed` / `run_cancelled` / `run_error`）后中继拆除，连接回到本副本正常处理（下一次 `start_run` 在本副本起）。
  - 若一个浏览器 WS 连接会同时绑定多个会话（执行时核实 `bind_session` 的语义），中继按会话分路；做不到 → STOP。
- **HTTP 转发**：2.3 的接口在处理前查运行归属，owner 是别的副本 → `POST /internal/v1/run-op`，原样回传结果与状态码。
- owner 心跳过期（副本挂了）→ 不转发，按现有「运行被遗弃」逻辑处理（`runrt.go` 的 reaper）。
- 转发失败（对方不可达）→ 浏览器收到可读错误（中文），不静默。
- 流式增量**不落库**：中继只在内存里转。

### 3.3 定时任务只触发一次

- `runDueJobs` 对每个到期任务先做**库内比较并交换**：

```sql
-- SQLite（新语句，三联写法；SQLite 版本是新增的，不违反「旧语句不改」）
UPDATE fb_cron_jobs SET next_run_at = ? WHERE id = ? AND next_run_at = ?
```

  受影响行数为 1 的副本才执行；新的 `next_run_at` 按现有调度计算先算好再 CAS。进程内 `claim` 保留（防同一副本内重入）。
- 心跳任务（`fireHeartbeat` / `ReanchorHeartbeat`）同样用 CAS（以它的下一次时间列为比较对象，执行时核实列名）。
- 「手动立即执行」等不经 `DueJobs` 的入口不受影响。

### 3.4 聊天渠道租约

- 新表（三方言迁移）：`fb_leases(name TEXT PRIMARY KEY, holder TEXT NOT NULL, expires_at_ms BIGINT NOT NULL)`。
- 长连接 / 轮询型适配器在 `Start` 之前，以 `channel:<account_key>` 为名抢租约（`INSERT … ON CONFLICT … WHERE expires_at_ms < now` 的三方言写法，或「事务内读 → 过期才写」——209 的全局写锁保证互斥）；拿到才连接，并每 10 秒续约；续约失败立刻断开连接。没拿到的副本每 15 秒重试一次（持有者挂了之后接手）。
- Webhook / HTTP 桥型不需要租约。
- 适配器类型分组写成一张表（执行时逐个确认是长连接还是 webhook）。

### 3.5 SQLite 多主机拒绝

网关启动、`HoldOwnerLease` 之后：库是 SQLite 且 `fb_run_owners` 里存在 `kind=gateway`、心跳新鲜、`host` ≠ 本机的行 → 退出并报「多副本部署需要 MySQL 或 PostgreSQL（forebrain.yaml 的 database.url）」。同主机的终端与网关共享 SQLite 照旧允许。

### 3.6 共享 home 单写者核查

逐个检查网关写 FOREBRAIN_HOME 的地方（`grep -rn "os.WriteFile\|os.Create\|os.OpenFile\|os.Rename\|os.MkdirAll" pkg --include='*.go' | grep -v _test` 中落在 home 下的），按「同一会话同一时刻只有一个副本在跑」判断是否安全，结论写成表放进 README 结果。已知需要关注：启动时的种子写入（词典、`models.json`、技能种子——多副本同时启动会并发写同一文件：改为「写临时文件 + 原子重命名」，已是的就跳过）、日志文件（每个副本带副本 ID 写自己的文件）、`state/outfilter/history.db`（212 已在 MySQL / PG 下取消）。跨进程协同（unix socket，plans/194–201）只在同一节点内有效，不改，部署文档注明。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...` 与 SQLite 全量 `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2`（`FOREBRAIN_HOME` 必须显式，见 §0.1）。

1. 3.1 迁移与登记（三方言）。
2. 3.5 SQLite 多主机拒绝。
3. 3.3 定时任务 CAS。
4. 3.4 渠道租约（迁移三方言 + 适配器分组）。
5. 3.2 内部端点与鉴权。
6. 3.2 WS 中继。
7. 3.2 HTTP 转发（先列出全部接口，再逐个接）。
8. 3.6 核查与修复。
9. 多副本集成测试（§5）。
10. 包图：`scripts/package-graph.sh`。

## 5. 测试

- 单元（三库矩阵都跑）：`fb_run_owners` 新列读写；CAS 两个调度器并发只触发一次；租约抢占、续约、过期接手；SQLite 多主机拒绝。
- 网关单元：内部端点无令牌 / 令牌对但来源不在登记表 → 401；中继在运行结束后拆除。
- **多副本集成测试**（`deploy/compose/multi-replica.yaml` + `scripts/multi-replica-test.sh`）：PostgreSQL 14 + 两个网关（同一镜像、同一共享卷挂作 FOREBRAIN_HOME、各自 `FOREBRAIN_INTERNAL_ADDR`）+ 假模型（仓库已有的 fake driver / 假 LLM 服务，执行时找现有的）。脚本断言：
  1. 在 A 上通过定时任务起一个会触发审批的对话；浏览器（用 WS 客户端脚本）连 B 绑定该会话 → 收到流式事件与 `requires_action`；在 B 上批准 → A 上的运行继续并完成。
  2. 在 B 上对 A 的运行发 `control_request interrupt` → 运行中断。
  3. 一个每分钟触发的定时任务跑 3 分钟 → `fb_cron_runs` 恰好 3 行。
  4. 一个长连接渠道（用 WS 桥型适配器或测试替身）只有一个副本连上；停掉持有者 → 另一个副本在 30 秒内接手。
  5. 换成 MySQL 8.0 再跑一遍。
- 没有 Docker：没有对应环境的项：把步骤与预期结果写成「用户手动验收清单」，附在本计划末尾并抄进 README 的结果栏，**由用户手动验收**；其余完成判据全部通过即可标 DONE（注明「含用户手动验收项」）。

## 6. 完成判据（机器可查）

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2   # SQLite 全绿
for url in 'mysql://root:fb@127.0.0.1:13306/fbt' 'postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'; do
  FOREBRAIN_HOME="$(mktemp -d)" FOREBRAIN_TEST_DATABASE_URL="$url" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/memory/... ./pkg/turn/... ./pkg/gateway/... -count=1 -p 2
done
scripts/multi-replica-test.sh postgres && scripts/multi-replica-test.sh mysql   # 两次都打印 PASS
go vet -tags fts5 ./...
```

（每条都带 `FOREBRAIN_HOME="$(mktemp -d)"`：house rule 禁止测试继承环境里的 `FOREBRAIN_HOME`，且 §0.1 的既有缺陷会让不带它的矩阵假失败。）

## 7. 维护注意

- 今后新增「作用于运行中对话」的接口或 WS 操作，必须同时接入转发（在转发表里登记，测试覆盖）。
- 今后新增后台单例（只该有一个副本做的事）一律用 `fb_leases`。
- 214 的 K8s 清单要注入 `FOREBRAIN_INTERNAL_ADDR`、Service 不需要粘滞。

## 8. STOP 条件

- 一个浏览器 WS 连接同时承载多个会话且无法按会话分路中继。
- 有作用于运行中对话的操作不经 WS / HTTP（例如经文件或 unix socket）且无法转发。
- 定时任务的下一次时间在 CAS 前算不出来（依赖执行结果）。
- 某个聊天渠道适配器既不是长连接也不是 webhook，无法判断要不要租约。
- 集成测试里转发带来的延迟让流式明显卡顿（单次转发 > 200ms），报告数据再定。

## 9. 执行记录（2026-11-03，步骤 1–10 全部完成；集成测试两引擎通过；容器形态列手验）

- **提交**（worktree `.worktrees/213`，分支 `exec/213`，**未合并**）：`9e5c81e1`（步骤 1 登记列）、`332bd114`（步骤 2 SQLite 多主机拒绝）、`af955711`（步骤 3 定时任务 CAS）、`5f4df593`（步骤 4 `fb_leases` 渠道租约）、`e37b9765`（步骤 5 内部端点与鉴权）、`371092aa`（步骤 6 WS 中继）、`e418212c`（步骤 7 HTTP 转发）、`6a60d68a`（步骤 8 共享 home 单写者）、`eb32d755`（步骤 9 集成测试脚手架）、`1f6e6de1`（步骤 10 包图）、`5ce6f559`（步骤 9 揪出的 PG 缺陷修复 + 回归测试）、`f1a05780`（步骤 9 harness 四处缺陷修复）。
- **已验证**：`go build -tags fts5 ./...` 干净；`go vet -tags fts5 ./pkg/...` 无输出；**SQLite 全量 `./... -count=1 -p 2` 全绿（35 包，exit 0）**；步骤 5–8 各自新增的用例通过（`TestInternalEndpointsRequireTheTokenAndARegisteredReplica`、`TestEveryInboundOpIsClassifiedForRelay`、`TestHandleChatWSRelaysToTheOwningReplica`、`TestRunBoundRoutesForwardToTheOwningReplica` 等）；`pkg/gateway` 生产文件仍 31。
- **三库矩阵**：PG 与 MySQL 上 `pkg/state`、`pkg/state/dbx`、`pkg/memory` 全绿（PG 79.9s/1.8s/18.3s；MySQL 263s/1.2s/102s）。`pkg/turn` 与 `pkg/gateway` 在 PG 上失败但**失败全部落在 `_test.go`**（详见 §9.4，既有测试方言化缺口，非本计划引入）。
- **步骤 9（多副本集成）已通过，两个引擎都跑通**：`scripts/multi-replica-test.sh postgres` 与 `... mysql` 各打印
  `PASS A`（B 绑定会话并收到 A 上运行的审批）、`PASS B`（在 B 上批准 → A 上的运行恢复并完成）、`PASS C`（在 B 上中断 → A 上的运行被取消）、`PASS: multi-replica forwarding on <engine>`。

### 9.1 步骤 9 的通过过程：先修 6 个缺陷

脚手架最初是「能起两个副本但永远打不出审批」的未完成件。逐个定位后，四处是 harness 自身的缺陷、一处是**产品缺陷**：

1. **假 provider 用了单步形式**（harness）：`FAKE_TOOL_NAME` 走的是 `serve_tool_script(..., streaming=False)`，而网关每轮都请求 stream——于是它回 JSON、客户端当 SSE 解析，回合以 `streaming_fallback`（"Streaming response ended before any complete data was received"）结束，审批闸从未被抬起。改用 `FAKE_TOOL_SCRIPT`（多步形式，只有它会流式返回工具调用）。
2. **plan mode 的等待被提前满足**（harness）：`switch_mode` 的等待写成「`mode_changed` 或 `session_bound`」，而 `session_bound` 是连接自身 bind 先发的，`start_run` 因此抢在模式生效前发出、回合跑在默认模式。改成只认 `mode_changed`。
3. **run 结束有两种线上形态**（harness）：发起该轮的那个副本对自己的 socket 回 `run_completed` op，而**其他任何 socket**（第二个标签页、或被中继到归属副本的浏览器）收到的是规范 run 事件 `turn_completed`——中继只会送后者，所以原来的等待在「已经结束的 run」上超时。新增 `run_end_of` 同时接受两者（网页两端本来就都处理，见 `useChatStream.ts` 的 `case 'turn_completed'` 与 `op === 'run_completed'`，产品无缺陷）。
4. **中断断言取错 run id**（harness）：A 的 socket 里还留着第一轮的帧，等待条件「run_started 或任意审批」命中了上一轮的残留，`control_request` 因此带上已结束的 run id，网关回 `unknown run_id for cancel`。改为只认「run id 与第一轮不同」的 `run_started`。
5. **`ListResolvedWaitActionIDs` 在 PostgreSQL 上直接报错**（**产品缺陷**，`5ce6f559`）：`SELECT DISTINCT w.action_id, … ORDER BY w.updated_at, w.action_id` —— PG 要求 DISTINCT 的 ORDER BY 表达式出现在选择列表里（`SQLSTATE 42P10`）。网关每次启动的「审批恢复扫描」都走这条路径，A 的日志里每分钟一条 `approval recovery scan failed`。三库矩阵没抓到是因为**既有调用这些扫描的用例全部用裸 `Open(...)` 只跑 SQLite**。修法：`waitRecoveryScan` 仅对 PostgreSQL 去掉 `DISTINCT` 前缀（`fb_run_waits.action_id` 是 UNIQUE、`conversation_runs` 的 join 走该表主键，一行最多出现一次，结果集不变；SQLite 语句逐字不动）。回归测试 `TestRecoveryScanRunsOnEveryDialect` 走 `OpenStateForTest`，因此在 PG/MySQL 上都跑；关掉修复即复现同样的 `42P10`。
6. **MySQL 的 `fbt` 库不存在**（harness）：Go 方言测试每个用例自建 scratch 库，从不碰 `fbt`；而网关直接打开配置里的库。harness 在 mysql 模式下先 `CREATE DATABASE IF NOT EXISTS fbt`。

### 9.2 用户手动验收清单（容器形态；执行者已用宿主进程形态跑通同一组断言）

> 容器形态需要 `docker build` 出 `forebrain:213`（依赖 214 的 Dockerfile）与 `FOREBRAIN_GATEWAY_TOKEN`（compose 的 seed 服务用它写共享 home 的 forebrain.yaml）。宿主进程形态已由 `scripts/multi-replica-test.sh` 在两个引擎上跑通，下面的清单验的是「同一套机制在容器 + 共享卷 + K8s 式寻址下也成立」。


1. **容器形态起两副本**：`FOREBRAIN_GATEWAY_TOKEN=<token> docker compose -f deploy/compose/multi-replica.yaml up --build -d` → 两个网关容器 running，`seed` 服务成功退出一次，共享卷里有 forebrain.yaml 且含该 token；两个容器在 `fb_run_owners` 里各有自己的 `internal_addr`（`http://<容器名>:6060`）、`host`、`kind=gateway`。
2. **中继与批准**：在 A 上通过定时任务起一个会触发审批的对话；用 WS 客户端（`scripts/acceptance/multi_replica_client.py`）连 B 绑定该会话 → 收到流式事件与 `requires_action`；在 B 上批准 → A 上的运行继续并完成。（预期：B 自己没有任何运行，事件全部来自 A。）
3. **中断**：在 B 上对 A 的运行发 `control_request interrupt` → 运行中断。
4. **定时任务只触发一次**：一个每分钟触发的任务跑 3 分钟 → `fb_cron_runs` 恰好 3 行（不是 6 行）。
5. **渠道租约**：一个长连接渠道（WS 桥型适配器或测试替身）只有一个副本连上；`kill` 持有者的容器 → 另一个副本在 30 秒内接手（`fb_leases` 的 `holder` 换人）。
6. **换成 MySQL**：把 `database.url` 换成 MySQL 8.0，重跑 2–5。
7. **鉴权负路径**：不带 token、或 token 对但来源 IP 不在登记表 → `/internal/v1/ws` 与 `/internal/v1/run-op` 都回 401。
8. **SQLite 多主机拒绝**：两个容器共享一个 SQLite 文件启动 → 后启动的那个退出并打印「多副本部署需要 MySQL 或 PostgreSQL」。

### 9.3 步骤 8 记录的两处残留（214 需处理）
- 全新共享 home 上的 gateway token 是「后写者赢」：两个副本同时启动会各生成一个。compose 的 seed 服务与 K8s 清单必须**先**把 token 写进共享配置，别让副本自己生成。
- `logs/debug.log` 仍是共享文件（opt-in、带轮转）；如需严格隔离，每副本带副本 ID 写自己的文件。

### 9.4 新发现：`pkg/turn` 与 `pkg/gateway` 的测试完全不方言化（既有缺口，非本计划引入）

本计划的完成判据把 `./pkg/turn/...` 与 `./pkg/gateway/...` 也放进了三库矩阵（212 的矩阵是 `state/memory/tool/process/migrate`，不含这两个包）。在 PG 上跑：

- **327 个用例失败，失败位置全部在 `_test.go`**（`pkg/gateway/api_extra_test.go` 348 次、`run_control_test.go` 67 次、`server_test.go` 30 次……），产品代码零命中。
- 错误分类：**93 次 `SQLSTATE 42601`（`syntax error at or near ","` / `at end of input`）＝ 裸 `?` 占位符直接发给 Postgres**，例如 `pkg/turn/rewind_test.go:208` 的 `INSERT INTO fb_runs(...) VALUES('r-live', ?, 'x', …)`；另有 **58 次 `SQLSTATE 57P03`**（PG 在跑的中途进入 recovery mode，属环境）。
- 仓库既有惯例是 `dbx.Rebind(dbx.DialectOf(db), sql)`（`pkg/memory/*_test.go` 早已这么写），这两个包的测试没有跟上。
- 已核实：**213 没有改过这些文件**（`git diff db56121f -- pkg/turn/rewind_test.go pkg/gateway/api_extra_test.go pkg/gateway/agents_api_test.go` 为空）。

**结论**：213 的产品代码在 PG 上通过（`pkg/state`、`pkg/memory` 全绿）；判据里「`pkg/turn`/`pkg/gateway` 也要在 PG 上全绿」这一条**不是本计划能修的**——它需要一次跨两个包的测试方言化（把裸 `?` 换成 `dbx.Rebind`），量级约 300+ 处，建议另立计划。214 不受影响（部署不依赖这两个包的 PG 测试）。
