# Plan: gateway status/stop 友好化输出（GATEWAY_STATUS_FRIENDLY_OUTPUT）

状态：已实施并真机验收通过（2026-10-08，验收记录见文末）。
计划日期：2026-10-08。代码基线：`1fd93a18`（工作区干净）。

> 本文件是已批准计划的仓库物化副本。执行完毕后本文件「状态」行更新为已实施，
> 并追加「验收记录」章节。改动留工作区，禁止 git commit。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（CLI 输出文案 + 删两行日志；无 API/协议/数据变更）
- **Depends on**: none
- **Category**: bug（UX 缺陷，根因级）
- **Planned at**: commit `1fd93a18`, 2026-10-08

## 1. 目标（Why this matters）

用户跑 `forebrain gateway status`，得到的是两行机器噪声：一条带时间戳/源码行的原始
slog 日志（`msg="gateway health probe skipped" err="Get \"…\": dial tcp …: connect: connection refused"`），
加一行 `gateway=not_running (optional for TUI) addr=… err=<原始传输错误>`。
"gateway 未运行"是这个命令的**答案本身**，是正常结果，不是异常事件；把它用日志格式+
Go 传输错误原文砸给用户，违背本仓库既有裁决（用户可读面显示语义化解释、原始错误留给
日志——`pkg/llm/explain.go` `ExplainError` 约定；斜杠输出禁原始 JSON 同族裁决）。
落地后：status/stop 的每个结果都是人话英文块（状态→原因→下一步动作），stderr 干净，
且顺手修掉两处语义谎报（任何非 200 也报 `gateway=up`、stop 任何状态码都报 requested）。

## 2. 根因（file:line 证据链）

**现场证据**（用户转录，2026-10-08）：

```
time=2026-10-08T12:31:45.896+08:00 level=INFO source=…/pkg/gateway/http_server.go:641 msg="gateway health probe skipped" addr=127.0.0.1:6060 err="Get \"http://127.0.0.1:6060/healthz\": dial tcp 127.0.0.1:6060: connect: connection refused"
gateway=not_running (optional for TUI) addr=127.0.0.1:6060 err=Get "http://127.0.0.1:6060/healthz": dial tcp 127.0.0.1:6060: connect: connection refused
```

1. **stderr 原始日志行** — `pkg/gateway/http_server.go:641`
   `slog.Info("gateway health probe skipped", "addr", addr, "err", err)`（stop 路径同款 `:667`）。
   CLI 默认 slog 是 Debug 级（`cmd/forebrain/root.go:79` 无环境变量时返回
   `slog.LevelDebug`），且 `prepPersistentHome`（root.go:109-110）装
   `AddSource: true` + telemetry tee——所以这条 Info 必然带时间戳+源码行打到 stderr。
   根因：把"命令的答案"当"日志事件"记了一遍。
2. **机器格式 + 原始传输错误** — `http_server.go:642`
   `fmt.Fprintf(w, "gateway=not_running (optional for TUI) addr=%s err=%v\n", addr, err)`。
   同族四处：`:646`（`gateway=up … status=%d`——非 200 也报 up，谎报）、
   `:668`（stop skipped + err 原文）、`:672`（stop requested——非 2xx 也报成功，谎报）。
3. 唯一调用方 `cmd/forebrain/gateway.go:37,46`（status/stop 子命令）；
   `gateway start` 走 `runGatewayE`→`RunServeBlocking`，不受影响。

### Current state 摘录（核对用）

`pkg/gateway/http_server.go:624-674`（现状，verbatim）：

```go
func GatewayHealthText(ctx context.Context, w io.Writer) error {
	rt, err := process.Resolve()
	if err != nil {
		return err
	}
	addr := strings.TrimSpace(rt.Config.Gateway.HTTPAddr)
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	url := "http://" + addr + "/healthz"
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if tok := gatewayControlPlaneAuthToken(rt); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Info("gateway health probe skipped", "addr", addr, "err", err)
		_, _ = fmt.Fprintf(w, "gateway=not_running (optional for TUI) addr=%s err=%v\n", addr, err)
		return nil
	}
	defer resp.Body.Close()
	_, _ = fmt.Fprintf(w, "gateway=up addr=%s status=%d\n", addr, resp.StatusCode)
	return nil
}

func GatewayShutdownRequest(ctx context.Context, w io.Writer) error {
	// 同构：/admin/shutdown，err 分支 slog.Info("gateway shutdown skipped", …)
	// + "gateway stop skipped (not listening) addr=%s err=%v"；
	// 成功分支 "gateway stop requested addr=%s status=%d"
}
```

### 必须遵守的仓库约定

- **CLI 人话风格**：`cmd/forebrain/lsp.go`——英文散文，状态→解释→下一步动作
  （例 `:262` "No language server is enabled. Run forebrain lsp list to see the available ones."
  / `:369` "Enable it with: forebrain lsp enable %s"）。
- **gateway 启动横幅风格**：`pkg/gateway/serve_run.go:249-291` `writeStartupBanner`——
  两空格缩进对齐标签（`Web UI       http://…/`、`Auth         …`、`Listening on http://…`）。
- **语义化错误解释约定**：`pkg/llm/explain.go:129` `ExplainError`——只替换能改进的
  消息，其余原样透传；channels.go:92 注释同款裁决（"the raw error stays in the log"）。
  本修复的白名单收尾：ECONNREFUSED→"nothing is listening on <addr>"，
  超时→"no response within 3s"，其余→err.Error() 原文透传。
- /healthz 鉴权豁免（`pkg/config/gateway.go:39`），handler 恒 200 "ok"
  （`pkg/gateway/server.go:586-591`）；/admin/shutdown 走 control-plane 鉴权（token 已由
  探测侧带上）。

## 3. 修复设计

### 行为变化矩阵（旧行为 → 新行为）

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| status·未运行（connection refused） | stderr slog 行 + stdout `gateway=not_running (optional for TUI) addr=… err=<原始错误>` | stdout 人话块（见下），**stderr 全空** | 主诉求 |
| status·超时/其他网络错误 | 同上 | `The gateway could not be reached.` + Address/Reach 行 | 不再谎称 not running |
| status·运行中（HTTP 200） | `gateway=up addr=… status=200` | running 块（Address/Health/Web UI） | |
| status·非 200 应答 | `gateway=up … status=500` | `Gateway is reachable but the health check failed.` + `Health  HTTP 500` | **刻意修订**（旧值谎报 up） |
| stop·未运行 | slog + `gateway stop skipped (not listening) addr err=…` | `Gateway is not running — nothing to stop.` + Address/Reach | 同族修订 |
| stop·超时/其他错误 | 同上 | `The gateway could not be reached — nothing was stopped.` + Address/Reach | |
| stop·2xx 应答 | `gateway stop requested addr=… status=200` | 一句话 `Shutdown requested — the gateway at http://… is stopping.` | |
| stop·非 2xx 应答 | `gateway stop requested … status=501`（谎报成功） | `The gateway answered HTTP 501 — the shutdown may not have been accepted.` + Address | **刻意修订** |
| exit code | 全路径 0（仅 Resolve 失败非 0） | **不变** | 刻意非变更：脚本契约不动 |
| 日志 | 两条 slog.Info | **删除** | 探测结果是输出本身，无日志价值；telemetry 文件同样不再记录（纯读探测，无状态变更，无需事后诊断） |
| 输出语言 | 英文 | 英文 | 全部既有 CLI 消息均英文 |

### 新输出字节级形态（测试断言以此为准）

`gateway status`·未运行（refused）：

```
Gateway is not running (optional — the terminal works without it).
  Address    http://127.0.0.1:6060
  Reach      nothing is listening on 127.0.0.1:6060
  Start it with `forebrain gateway start`
```

`gateway status`·超时或其他网络错误：

```
The gateway could not be reached.
  Address    http://127.0.0.1:6060
  Reach      no response within 3s
```

（其他错误时 Reach 行为 err.Error() 原文。）

`gateway status`·运行中：

```
Gateway is running.
  Address    http://127.0.0.1:6060
  Health     ok (HTTP 200)
  Web UI     http://127.0.0.1:6060/
```

`gateway status`·可达但非 200：

```
Gateway is reachable but the health check failed.
  Address    http://127.0.0.1:6060
  Health     HTTP 500
```

`gateway stop`·未运行：

```
Gateway is not running — nothing to stop.
  Address    http://127.0.0.1:6060
  Reach      nothing is listening on 127.0.0.1:6060
```

`gateway stop`·2xx：

```
Shutdown requested — the gateway at http://127.0.0.1:6060 is stopping.
```

`gateway stop`·非 2xx：

```
The gateway answered HTTP 501 — the shutdown may not have been accepted.
  Address    http://127.0.0.1:6060
```

标签行格式统一为 `fmt.Fprintf(w, "  %-10s %s\n", label, value)`（两空格缩进、
标签宽 10、一个空格——与启动横幅同族观感）。

### 目标代码形状

`pkg/gateway/http_server.go` 内新增两个包内 helper（单一分类点，status/stop 共用）：

```go
// reachOutcome classifies a probe error for the status and stop commands:
// whether nothing is listening (the gateway is simply not running) and the
// short cause a person can act on. Only the outcomes a local probe can
// actually hit are named; anything else passes through as its own text
// (the ExplainError convention: replace only what can be improved).
func reachOutcome(err error, addr string) (refused bool, reason string) {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true, "nothing is listening on " + addr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false, "no response within 3s"
	}
	return false, strings.TrimSpace(err.Error())
}

// displayBaseURL is the clickable gateway base URL, with the same
// unspecified-host mapping the startup banner applies (a browser cannot
// connect to "0.0.0.0" as a destination).
func displayBaseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
```

`GatewayHealthText` / `GatewayShutdownRequest` 重写输出分支（签名、resolve、URL、
token、3s 超时均不变；删除两处 `slog.Info`），按上面的字节级形态输出。status 的
err 分支先 `refused, reason := reachOutcome(err, addr)`，refused→not running 块
（含 `Start it with` 行），否则→could not be reached 块；200→running 块；非 200→
unhealthy 块。stop 的 err 分支同构（refused→"nothing to stop"，否则→"nothing was
stopped"）；2xx→一句话；非 2xx→answered 块。

`slog` import 若因此不再被 http_server.go 使用则移除（编译器/vet 裁决）；
新增 `errors`、`syscall`、`net` import（全 stdlib，包图不变）。

`pkg/gateway/serve_run.go:231` `displayHost(addr net.Addr) string` 改签名为
`displayHost(addr string) string`（逻辑不变），唯一调用点 `:283`
`displayHost(b.Addr)` → `displayHost(b.Addr.String())`；`displayBaseURL` 复用同一
语义而非复制三行归一化。（若实施中发现 displayHost 有其他调用方/测试引用，直接同步
更新——仍在本计划 Scope 内。）

`cmd/forebrain/gateway.go` 不改。

## 4. Scope

**In scope**（仅这些文件）：
- `docs/plan/GATEWAY_STATUS_FRIENDLY_OUTPUT_PLAN.md`（新建，本计划物化）
- `pkg/gateway/http_server.go`（两个导出函数的输出分支 + 两个 helper + import）
- `pkg/gateway/serve_run.go`（displayHost 签名 string 化及其唯一调用点）
- `pkg/gateway/http_server_test.go`（追加测试）

**Out of scope**（看起来相关，明确不碰）：
- `cmd/forebrain/gateway.go` —— 命令接线/Short 描述/`context.Background()` 与
  `cmd.Context()` 的既有不一致，均非本缺陷根因。
- `cmd/forebrain/root.go` 的默认 slog 级别/tee 装配 —— 全局日志策略是另一个话题；
  本修复按"探测结果不是日志事件"在源头删除记录，不动全局。
- `gateway start`（`runGatewayE`/`RunServeBlocking`/`writeStartupBanner` 的内容文案）。
- exit code 语义、输出语言（保持英文）、`/healthz` handler 本体。
- web 前端（pkg/gateway/dist、frontend/）——本改动不触 UI 资产。

## 5. Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'GatewayHealth|GatewayShutdown|ReachOutcome' -count=1` | all pass |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./cmd/forebrain -count=1` | all pass |
| Vet（CI 同款） | `go vet ./...` | exit 0 |
| 验收构建 | `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | 产出二进制 |

## 6. Steps

### Step 0: 物化计划文件

把本计划全文写入 `docs/plan/GATEWAY_STATUS_FRIENDLY_OUTPUT_PLAN.md`（仓库惯例位置，
状态行 `Status: 已批准待实施`）。

**Verify**: `ls docs/plan/GATEWAY_STATUS_FRIENDLY_OUTPUT_PLAN.md` → 存在。

### Step 1: 重写 pkg/gateway/http_server.go 的两个输出函数

按「目标代码形状」：新增 `reachOutcome`、`displayBaseURL`；重写
`GatewayHealthText`/`GatewayShutdownRequest` 的输出分支；删除 `:641`/`:667` 两处
`slog.Info`；修 import。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0。

### Step 2: displayHost 签名 string 化

`pkg/gateway/serve_run.go:231` 及唯一调用点 `:283`（见「目标代码形状」末段）。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；
`grep -n "displayHost(" pkg/gateway/*.go | grep -v _test` → 仅定义 + 一处调用，
均 string 版。

### Step 3: 追加测试（pkg/gateway/http_server_test.go，包内风格对齐现有文件）

测试基建 helper（进程级 Resolve 缓存必须重置——仓库既有裁决）：

```go
func useGatewayTestHome(t *testing.T, addr string) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "forebrain.yaml"),
		[]byte("gateway:\n  http_addr: \""+addr+"\"\n"), 0o600)
	t.Setenv("FOREBRAIN_HOME", dir)
	process.ResetResolve()
	t.Cleanup(process.ResetResolve)
}
```

用例（输出断言到上面的字节级形态；slog 静默断言：临时
`slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))` + `t.Cleanup` 恢复，
断言 buf 为空）：
1. `TestGatewayHealthTextNotRunning`——`net.Listen("tcp","127.0.0.1:0")` 取 addr 后
   `Close()`（保证 refused），断言三行 not-running 块 + `Start it with` 行 + err 为
   nil + slog buf 空。
2. `TestGatewayHealthTextRunning`——`httptest.NewServer` 恒 200 "ok"，断言 running
   块四行。
3. `TestGatewayHealthTextUnhealthyStatus`——httptest 返回 500，断言 unhealthy 块。
4. `TestGatewayShutdownRequestNotRunning`——refused 端口，断言 nothing-to-stop 块。
5. `TestGatewayShutdownRequestAccepted`——httptest POST /admin/shutdown 返回 200，
   断言一句话 Shutdown requested。
6. `TestReachOutcomeClassification`——包 `syscall.ECONNREFUSED` 的错误 →
   refused=true + "nothing is listening on <addr>"；`net.Error` 且 `Timeout()=true`
   的假错误 → refused=false + "no response within 3s"；普通错误 → 原文透传。

**Verify**: 定点测试命令 → all pass（新增 6 个）。

### Step 4: 回归 + vet

**Verify**: 包级回归 + `go vet ./...` → 全绿；`git status --porcelain` 改动仅
Scope 内 4 个文件。

### Step 5: 真机验收（构建必须带 `-tags fts5`）

隔离环境（临时 `FOREBRAIN_HOME` + 临时目录，不污染真实 `~/.forebrain*`）：

1. **复现→修复（主诉求）**：
   `FOREBRAIN_HOME=$(mktemp -d) ./build/bin/forebrain gateway status`
   stdout=三行人话块、**stderr 完全为空**、exit 0（分别捕获两流取证）。
2. **运行中路径**：起一个本地 200 应答器占住配置端口（如
   `python3 -c '…http.server…'` 或 nc 循环；status 只认 HTTP 应答，这是对输出
   路径的真实网络验收；与真 gateway 的集成语义已由 refusal/httptest 覆盖），
   `gateway status` → running 块。stop 打过去得非 2xx → answered 块。
   如可用 `.claude/skills/run-forebrain/` fake-provider harness 起真 gateway，则以
   真 gateway 复跑 status(running)/stop(2xx)/status(not running) 全链。
3. **回归**：`forebrain --version`、`forebrain gateway`（无子命令报 requires one of）、
   非 TTY `forebrain gateway start </dev/null` 仍报 startup config 错误且不挂起
   （四类场景模板之"非 TTY 行为不变"）。
4. 清理：杀掉占端口进程、删临时目录。

### Step 6: 收口

`docs/plan/GATEWAY_STATUS_FRIENDLY_OUTPUT_PLAN.md` 状态行改"已实施并真机验收通过
（日期）"，新增「验收记录」章节（场景→证据，含踩过的工具链坑）。改动留工作区，
**不 commit**。

## 7. Done criteria（机器可查）

- [x] `CGO_ENABLED=1 go build ./...` exit 0
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./cmd/forebrain -count=1` 全绿，
      新增 6 测试存在且通过
- [x] `go vet ./...` exit 0
- [x] `grep -n "gateway=not_running\|gateway=up\|gateway stop skipped\|gateway stop requested\|health probe skipped\|shutdown skipped" pkg/gateway/*.go cmd/forebrain/*.go`
      → 仅测试文件可能命中，源码零命中（实测：源码与测试均零命中）
- [x] 真机：refused 场景 stderr 为空、stdout 为三行块、exit 0
- [x] `git status --porcelain` 仅 Scope 内文件

## 8. STOP conditions

- drift check 或 Step 1 现场核对发现 `http_server.go:624-674` 与摘录不符。
- `displayHost` 存在除 `serve_run.go:283` 外的调用方且改签名波及测试以外的行为面。
- 隔离 home 下 `process.Resolve()` 行为与计划假设不符（如 forebrain.yaml 键名不生效），
  导致测试基建起不来——报告而不是绕过。
- 真机验收发现 stderr 仍有其他 INFO 日志泄漏（说明还有第二来源）——停下来报告，
  不在本计划内扩全日志策略。

## 9. Maintenance notes

- 今后 status/stop 新增结果分支时，必须走 `reachOutcome` 单一分类点，输出保持
  「状态行 + 对齐标签行 + 下一步动作」三段式；禁止回退 key=value 机器格式。
- 若 `/healthz` 未来返回结构化体（版本/启动时间），running 块的 Health 行是挂点。
- `displayHost`(string) 与 `displayBaseURL` 语义相同（display 归一化）；再出现第三个
  消费者时应合并而不是再复制。
- 全局 slog 默认 Debug 级 + AddSource tee（root.go:79,109-110）意味着任何库级
  `slog.Info` 都会以机器格式出现在 CLI stderr——新代码在"命令答案路径"上不要打日志；
  若要改默认级别，是独立提案。

## 10. 验收记录（2026-10-08）

实施环境：macOS，go1.26.6，基线 `1fd93a18`，drift check 零变动、工作区干净。
验收二进制 `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`
（`go version -m` 显示 `+dirty`，证明非 stale binary——本仓库曾因 stale binary
误判过显示缺陷）。

### 自动化验证

| 检查 | 命令 | 结果 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'GatewayHealth|GatewayShutdown|ReachOutcome' -count=1` | ok（新增 6 测试全过） |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./cmd/forebrain -count=1` | 两包 ok |
| Vet | `go vet ./...` | exit 0 |
| gofmt | `gofmt -l pkg/gateway/` | 无输出 |
| 机器格式残留 grep | 计划 §7 第 4 条 | 源码+测试零命中 |
| Scope | `git status --porcelain` | 仅 4 个 Scope 内文件（build/bin 已被 gitignore） |

测试基建实现备注：`useGatewayTestHome` + `warmResolve` + `captureDefaultSlog` +
`refusedAddr` 四个 helper。`warmResolve`（先 Resolve 一次装缓存再挂 slog 捕获）
比计划原稿多一步：把 resolve/catalog 安装期的任何日志与"探测路径静默"断言隔离，
静默断言只度量为本次修复负责的那段路径；isolation 键（FOREBRAIN_HOME +
`gateway.http_addr` + `process.ResetResolve`）与计划假设完全一致，未触发 STOP 条件 3。

### 真机验收（隔离 `FOREBRAIN_HOME=$(mktemp -d)`，stdout/stderr 分别捕获）

1. **status·refused（主诉求复现→修复）**：exit 0；stdout 198 字节、四行块与
   §3 字节级形态完全一致（`Gateway is not running (optional — the terminal works
   without it).` / `Address` / `Reach` / `Start it with`）；**stderr 0 字节**
   （旧版此处是一条带时间戳+源码行的 slog INFO）。
2. **status·running**：python3 应答器占 127.0.0.1:16060（GET /healthz→200 "ok"），
   输出 `Gateway is running.` + Address/Health/Web UI 四行，stderr 0 字节。
3. **stop·非 2xx**：同端口 POST /admin/shutdown 得 501，输出
   `The gateway answered HTTP 501 — the shutdown may not have been accepted.` +
   Address 行，stderr 0 字节（旧版此处谎报 "stop requested"）。
4. **status·回到未运行**：杀掉应答器后再跑 status，not-running 块（Reach 指向
   16060），stderr 0 字节。
5. **回归三件套**：`--version` exit 0；`gateway`（无子命令）exit 1 +
   `requires one of: start, status, stop` 原文案；非 TTY
   `gateway start </dev/null` exit 1 + startup config 错误、15s timeout 内返回
   （不挂起）。
6. **stop·2xx**：未做真机场景（见下"偏差"），由
   `TestGatewayShutdownRequestAccepted` 以真实 HTTP POST /admin/shutdown→200
   字节级断言覆盖。

### 偏差与说明

- **真 gateway 全链（stop 2xx）未真机复跑**：`.claude/skills/run-forebrain/`
  fake-provider harness 是 TUI 回合控制装置，不提供 gateway 启动路径；真
  `gateway start` 需要完整 first-setup（main agent LLM 配置 + .env），拼装一套
  已超出本计划"如可用"条款的本意。2xx 输出行由 httptest 级真 HTTP 请求字节级
  覆盖；探测路径的网络真实性已由真机 python 应答器场景覆盖。
- `displayHost` 无计划外调用方（grep 证实仅定义+serve_run.go:283 一处调用），
  未触发 STOP 条件 2。
- 验收 stderr 中的 `Terminated: 15` 字样是 shell 杀 python 应答器的 job-control
  消息，非 forebrain 输出。

### 落地清单

- `pkg/gateway/http_server.go`：`GatewayHealthText`/`GatewayShutdownRequest`
  输出分支重写为三段式人话块；新增 `reachOutcome`/`displayBaseURL`；删除两处
  `slog.Info`；import 移除 `log/slog`、新增 `errors`（`net`/`syscall` 原已引入）。
- `pkg/gateway/serve_run.go`：`displayHost(addr net.Addr)` → `displayHost(addr string)`，
  唯一调用点改 `displayHost(b.Addr.String())`。
- `pkg/gateway/http_server_test.go`：新增 6 测试 + 4 个 helper。
- `docs/plan/GATEWAY_STATUS_FRIENDLY_OUTPUT_PLAN.md`：本文件。

改动留在工作区，未 commit（仓库铁律）。
