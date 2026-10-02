# 计划 002：`forebrain gateway start` 输出 banner、完整路由表和监听地址；删除无人读取的 gateway 配置项

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/gateway/http_server.go pkg/gateway/serve_run.go pkg/channel/channel.go cmd/forebrain/serve.go cmd/forebrain/gateway_test.go pkg/config/config.go pkg/config/load.go forebrain.yaml`
> 计划 001 会改动 `pkg/gateway/controlplane.go`、`server.go`，那是预期的；上面列出的文件若有输出，先与"现状"摘录核对。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：LOW
- **依赖**：计划 001（路由表要包含 `/api/auth/session`，登录链接指向 `/login`）；决策 D3 已定：**仅当 stdout 是终端时打印登录链接**
- **类别**：dx / tech-debt
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 原话："forebrain gateway start命令启动后必须输出banner和全部路由信息以及listen on端口信息，现在什么都没有。"
现在启动后终端上只有一行 `slog` 调试日志（`gateway auth configured`），用户不知道服务是否起来、监听在哪、网页在哪个地址、有哪些接口。
同时，配置结构体里有 `banner`、`banner_text` 等 18 个字段写进了示例配置和文档，却没有任何代码读取——用户照着文档配 `banner: true` 什么也不会发生。
端口被占用时，`RestServer.Run` 会直接 `panic` 打出一屏堆栈，而不是一句"端口被占用"。

## 现状

- `cmd/forebrain/serve.go:21`：`var gatewayRunBlocking = gateway.RunServeBlocking`；`:23-29` `runGatewayE` 调用 `gatewayRunBlocking(ctx)`。
  `cmd/forebrain/gateway_test.go:26,38,68,80,105,114,139,149` 替换了这个变量（签名 `func(ctx context.Context) error`）。
- `pkg/gateway/serve_run.go:16` `func RunServeBlocking(ctx context.Context) error`；末尾：

  ```go
  // serve_run.go:130-142
  	addr := strings.TrimSpace(h.Deps.AppCfg.Gateway.HTTPAddr)
  	if addr == "" {
  		addr = "127.0.0.1:6060"
  	}
  	httpSrv := NewRestServer(addr)
  	gw.AttachREST(httpSrv)
  	gddHandler := httpSrv.Handler
  	httpSrv.Handler = ServeHTTPChain(h, gw, gddHandler)
  	slog.Info("gateway auth configured", "mode", strings.TrimSpace(h.Deps.AppCfg.Gateway.Auth.Mode))
  	httpSrv.Run()
  	return nil
  ```
- `pkg/gateway/http_server.go:131-141` `AddRoute` 把路由交给 `srv.router.Handle`，**没有保留路由清单**；`:229-253`：

  ```go
  func (srv *RestServer) Run() {
  	addr := srv.Addr
  	if addr == "" { addr = "127.0.0.1:6060" }
  	ln, err := net.Listen("tcp", addr)
  	if err != nil {
  		panic(err)
  	}
  	go func() {
  		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
  			panic(err)
  		}
  	}()
  	c := make(chan os.Signal, 1)
  	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
  	<-c
  	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
  	defer cancel()
  	_ = srv.Shutdown(ctx)
  }
  ```
- `pkg/gateway/controlplane.go` 的 `ServeHTTPChain` 在路由器之外直接处理 `/ws/chat`，因此它不在路由器清单里。
- `pkg/gateway/server.go:562-584` `AttachREST`：注册健康检查、`AttachExtraRoutes` 的全部 `/api` 路由，绑定当前主代理的渠道（渠道路由存放在
  `channel.Registry` 自己的表里，见 `pkg/channel/channel.go:62` 的 `routes map[string]http.HandlerFunc` 与 `:187-193` 的 `routeKey` = `"METHOD /path"`），
  最后 `attachStaticUI` 注册 `GET /` 与 `GET /*filepath`（仅当有 UI 可服务时，见 `http_server.go:445-455`）。
- `cmd/forebrain/root.go:33`：`rootCmd.Version = home.Version`，`cmd/forebrain` 已经 import `pkg/home`；`pkg/gateway` **没有** import `pkg/home`（不要新增这条依赖，版本号由命令层传入）。
- `cmd/forebrain/serve.go:18-20`：`gatewayIsTerminal` 判断文件是否是终端。
- 无人读取的配置字段（`pkg/config/config.go:5-30`）：`Grpc`（及类型 `GatewayGRPC`）、`Banner`、`BannerText`、`ManageEnable`、`EnableResponseGzip`、`LogReqEnable`、`RouteRootPath`、
  `WriteTimeout`、`ReadTimeout`、`IdleTimeout`、`GraceTimeout`、`ServiceName`、`ServiceGroup`、`ServiceVersion`、`LogLevel`、`LogPath`、`LogCaller`、`LogDiscard`。
  唯一的"读取"是 `pkg/config/load.go:543` 对 `Grpc.Port` 做 `TrimSpace`。YAML 解析不是严格模式（`load.go:79` 用 `yaml.Unmarshal`），删掉字段后旧配置文件里残留的这些键会被忽略，不会导致启动失败。
- 示例配置 `forebrain.yaml:15-83` 除上述字段外还写了结构体里根本不存在的 `cors_origins`。
- 文档站（独立仓库，检出在 `forebrain-harness.github.io/`，本仓库 `.gitignore` 忽略它）`docs/config/runtime-gateway-and-channels.md:54-107` 的 Gateway Reference 表格与说明段落
  列出了上述全部字段，外加不存在的 `gateway.auth.password`、`gateway.cors_origins`、`gateway.grpc.port`。

## 设计

1. **命令层传参**：`pkg/gateway/serve_run.go` 新增

   ```go
   // ServeOptions is what the command line hands the gateway when it starts.
   type ServeOptions struct {
   	// Out receives the startup banner and the route table.
   	Out io.Writer
   	// Version is this build's version, printed under the banner.
   	Version string
   	// SignInLink prints a sign-in URL that carries the gateway token. The
   	// command sets it only when stdout is a terminal, so the token never lands
   	// in a log file that captures a service's output.
   	SignInLink bool
   }
   func RunServeBlocking(ctx context.Context, opts ServeOptions) error
   ```
   `runGatewayE` 传入 `gateway.ServeOptions{Out: cmd.OutOrStdout(), Version: home.Version, SignInLink: gatewayIsTerminal(os.Stdout)}`。
2. **路由清单**：`RestServer` 增加字段 `routes []RouteInfo`（`type RouteInfo struct { Method, Path string }`），`AddRoute` 每注册一条就追加一条；
   新增 `func (srv *RestServer) Routes() []RouteInfo`，返回按 `Path` 再按 `Method` 排序的副本。
3. **渠道路由清单**：`pkg/channel/channel.go` 新增 `func (r *Registry) RouteKeys() []string`，在读锁下取 `r.routes` 的键（`"METHOD /path"`）排序后返回。
4. **`Run` 返回错误并在监听成功后回调**：

   ```go
   // Run listens on srv.Addr, calls ready with the bound address once the
   // socket is open, serves until SIGINT/SIGTERM, then shuts down gracefully.
   func (srv *RestServer) Run(ready func(net.Addr)) error
   ```
   `net.Listen` 失败返回 `fmt.Errorf("gateway listen on %s: %w", addr, err)`；`Serve` 的非 `http.ErrServerClosed` 错误通过 channel 送回，与信号一起 `select`，
   作为 `Run` 的返回值；删除两处 `panic`。`RunServeBlocking` 返回 `Run` 的错误，由 cobra 统一打印。
5. **新文件 `pkg/gateway/banner.go`**：

   ```go
   // bannerArt is the wordmark printed when the gateway starts.
   const bannerArt = " _____              _               _\n" +
   	"|  ___|__  _ __ ___| |__  _ __ __ _(_)_ __\n" +
   	"| |_ / _ \\| '__/ _ \\ '_ \\| '__/ _` | | '_ \\\n" +
   	"|  _| (_) | | |  __/ |_) | | | (_| | | | | |\n" +
   	"|_|  \\___/|_|  \\___|_.__/|_|  \\__,_|_|_| |_|\n"

   type startupBanner struct {
   	Version      string
   	Routes       []RouteInfo // the router's table, web UI routes included
   	ChannelAgent string      // the primary agent whose channels are mounted
   	ChannelRoutes []string   // "METHOD /path", from channel.Registry.RouteKeys
   	Addr         net.Addr    // the address actually bound
   	AuthMode     string
   	SignInToken  string      // set only when a sign-in link is to be printed
   	WebUI        bool        // true when the web UI is being served
   }

   func writeStartupBanner(w io.Writer, b startupBanner)
   ```
   输出格式（逐字，`<>` 为变量；方法列宽 8，左对齐）：

   ```
   <bannerArt>
   Forebrain Harness Gateway <Version>

   Routes (<N>)
     GET     /api/agents/primary
     ...
     WS      /ws/chat
   Channel routes · <ChannelAgent> (<M>)
     POST    /<inbound path>
   Web UI       http://<host:port>/
   Sign in      http://<host:port>/login#token=<SignInToken>
   Auth         <AuthMode>
   Listening on http://<host:port>
   ```
   规则：
   - `Routes` 计数 `N` 含 `WS /ws/chat` 这一条（它由 `ServeHTTPChain` 处理，不在路由器里，在输出时追加）；
   - 没有渠道路由时整段 `Channel routes` 不输出；
   - `Web UI` 行仅在 `WebUI` 为真时输出；`Sign in` 行仅在 `SignInToken` 非空时输出；
   - `<host:port>` 取实际绑定地址；绑定在未指定地址（`0.0.0.0`、`::`）时，URL 里的主机写 `127.0.0.1`，`Listening on` 行仍写实际绑定地址；
   - `Version` 为空时第二行只写 `Forebrain Harness Gateway`。
   - 输出写到 `opts.Out`（stdout），不经过 `slog`。
6. **`RunServeBlocking` 收尾改为**：构造 `startupBanner`（`Routes: httpSrv.Routes()`、`ChannelRoutes: chReg.RouteKeys()`、`ChannelAgent: runner.AgentName`、
   `AuthMode`、`WebUI: gw.resolveStaticFS() != nil`、`SignInToken`：当 `opts.SignInLink` 且模式为 `token` 且 token 非空时取 token），
   `return httpSrv.Run(func(a net.Addr) { b.Addr = a; writeStartupBanner(opts.Out, b) })`。删除 `slog.Info("gateway auth configured", ...)`，`Auth` 行取代它。
7. **删除死配置**：从 `Gateway` 结构体删除上文 18 个字段与 `GatewayGRPC` 类型，删除 `load.go:543`；
   `forebrain.yaml` 的 `gateway:` 段只保留 `http_addr` 与 `auth`（`mode`、`token`）及其注释；
   文档站 `docs/config/runtime-gateway-and-channels.md` 的 Gateway Reference 表只保留 `gateway.http_addr`、`gateway.auth.mode`、`gateway.auth.token`，
   删除 "How To Use The Gateway Block" 里关于 `cors_origins`、timeout、service identity、logging 的段落，并补一段"启动输出"说明
   （banner、路由表、`Web UI` / `Sign in` / `Listening on` 各行含义，以及登录链接只在终端里打印）。文档站是独立仓库，改动同样留在其工作区不提交。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/channel/... ./pkg/config/... ./cmd/forebrain/... -count=1` | 全部 `ok` |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| 包依赖图 | `scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` | 第二条无输出（不应新增依赖） |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 范围

**只允许修改**：
- `pkg/gateway/serve_run.go`、`pkg/gateway/http_server.go`、`pkg/gateway/banner.go`（新建）、`pkg/gateway/banner_test.go`（新建）、`pkg/gateway/http_server_test.go`（新建或已有则修改）
- `pkg/channel/channel.go`、`pkg/channel/channel_test.go`（或该包现有测试文件）
- `cmd/forebrain/serve.go`、`cmd/forebrain/gateway_test.go`
- `pkg/config/config.go`、`pkg/config/load.go`，以及引用被删字段的 `pkg/config` 测试
- `forebrain.yaml`
- `forebrain-harness.github.io/docs/config/runtime-gateway-and-channels.md`（文档站仓库）
- `frontend/e2e/banner.spec.ts`（新建）

**不要碰**：
- `pkg/gateway/controlplane.go` 的鉴权逻辑（计划 001 已定稿）。
- `pkg/home`：不要让 `pkg/gateway` import 它。
- `gateway.http_addr`、`gateway.auth.*` 的语义与默认值。

## 步骤

### 第 1 步：路由清单与渠道路由清单

实现设计第 2、3 条。

**验证**：新增测试 `TestRestServerRoutesSorted`（注册 `POST /b`、`GET /a`、`GET /b` → `Routes()` 依次为 `GET /a`、`GET /b`、`POST /b`）与
`TestRegistryRouteKeysSorted`（`Bind` 两个在 `Start` 中注册路由的假 handler → 返回排序后的键；参照 `pkg/channel` 现有测试里构造 handler 的方式）；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ ./pkg/channel/ -run 'Routes|RouteKeys' -count=1` → `ok`。

### 第 2 步：`Run` 返回错误

实现设计第 4 条。新增 `TestRestServerRunReportsBusyPort`：先 `net.Listen("tcp", "127.0.0.1:0")` 占住一个端口，再让 `RestServer` 在同一地址 `Run` → 返回的错误包含 `gateway listen on` 且不 panic。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run RunReportsBusyPort -count=1` → `ok`；`grep -n "panic(" pkg/gateway/http_server.go` → 无输出。

### 第 3 步：banner 渲染

实现设计第 5 条。`banner_test.go`：

- `TestWriteStartupBannerGolden`：固定输入（版本 `v0.3.0`，三条路由，一条渠道路由，地址 `127.0.0.1:6060`，`AuthMode: token`，`SignInToken: "tok"`，`WebUI: true`）→ 输出与测试里内联的期望字符串逐字节相等；
- `TestWriteStartupBannerOmitsSignInWithoutToken`：`SignInToken` 为空 → 输出不含 `Sign in`；
- `TestWriteStartupBannerUnspecifiedHost`：地址 `0.0.0.0:6060` → `Web UI       http://127.0.0.1:6060/` 且 `Listening on http://0.0.0.0:6060`；
- `TestWriteStartupBannerNoChannels`：无渠道路由 → 不含 `Channel routes`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run StartupBanner -count=1` → `ok`。

### 第 4 步：接到启动流程与命令层

实现设计第 1、6 条；更新 `cmd/forebrain/gateway_test.go` 里 8 处替换 `gatewayRunBlocking` 的地方为新签名，并新增
`TestRunGatewayPassesStdoutAndVersion`：替换 `gatewayIsTerminal` 返回 `false`，断言收到的 `opts.Out` 就是 `cmd.OutOrStdout()`、`opts.SignInLink == false`、`opts.Version == home.Version`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain/ ./pkg/gateway/ -count=1` → `ok`；`go vet ./...` → 退出码 0。

### 第 5 步：删除死配置

实现设计第 7 条。先 `grep -rn "Banner\|BannerText\|ManageEnable\|EnableResponseGzip\|LogReqEnable\|RouteRootPath\|WriteTimeout\|ReadTimeout\|IdleTimeout\|GraceTimeout\|ServiceName\|ServiceGroup\|ServiceVersion\|LogCaller\|LogDiscard\|GatewayGRPC\|Gateway.Grpc\|Gateway.LogLevel\|Gateway.LogPath" pkg cmd`
找出全部引用（注意 `LogLevel`、`LogPath` 这类名字可能在别的结构体里也有，只删 `Gateway` 上的）。

**验证**：
- 上面的 grep（限定 `Gateway` 结构体相关）无残留；
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/config/... -count=1` → `ok`；
- 用仓库根的 `forebrain.yaml` 做一次加载冒烟：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config/ -run Example -count=1`（若没有加载示例配置的测试，新增 `TestExampleConfigLoads`，调用 `config.Load` 加载 `../../forebrain.yaml` 并断言无错误）→ `ok`。

### 第 6 步：真机验证

新增 `frontend/e2e/banner.spec.ts`，读取 `process.env.E2E_GATEWAY_OUT` 文件内容，断言：
- 含 `|  ___|__  _ __ ___| |__`（banner 第二行）与 `Forebrain Harness Gateway`；
- 含 `Routes (`、`POST    /api/auth/session`、`GET     /api/providers`、`GET     /api/hooks`、`GET     /api/memories/settings`、`WS      /ws/chat`；
- 含 `Web UI       ${E2E_BASE_URL}/` 与 `Listening on ${E2E_BASE_URL}`；
- **不含** `process.env.E2E_TOKEN`（脚本里 stdout 重定向到文件，不是终端，不得打印带 token 的链接）；
- 不含 `level=`（启动输出不混入 slog 日志）。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。另外手工看一眼 `$TMPDIR/forebrain-web-e2e/gateway.out`，把内容贴进 README 本计划的状态说明里作为证据。

## 完成标准（全部满足）

- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/channel/... ./pkg/config/... ./cmd/forebrain/... -count=1` 全部 `ok`
- [x] `go vet ./...` 退出码 0
- [x] `scripts/package-graph.sh` 重新生成（diff 仅 LOC 计数变化，fan_in/fan_out 无任何变化，未新增包依赖）
- [x] `grep -n "panic(" pkg/gateway/http_server.go` 无输出
- [x] `grep -n "banner\|manage_enable\|cors_origins\|log_discard" forebrain.yaml` 无输出
- [x] `scripts/acceptance/web_e2e.sh` 最后一行 `web e2e: PASS`（8/8 用例，26s；banner 用例断言路由表、Web UI、Listening on、无 token、无 slog）
- [x] `git status --short pkg/gateway/dist` 无输出；README 全局规则 4 的检查无输出
- [x] README 状态行已更新，附 `gateway.out` 内容

`gateway.out` 证据（节选）：

```
 _____              _               _
|  ___|__  _ __ ___| |__  _ __ __ _(_)_ __
| |_ / _ \| '__/ _ \ '_ \| '__/ _` | | '_ \
|  _| (_) | | |  __/ |_) | | | (_| | | | | |
|_|  \___/|_|  \___|_.__/|_|  \__,_|_|_| |_|
Forebrain Harness Gateway v0.1.2-…

Routes (104)
  GET     /
  GET     /*filepath
  …
  POST    /api/auth/session
  …
  WS      /ws/chat
Channel routes · main (6)
  GET     /channels/wecom/health
  …
Web UI       http://127.0.0.1:8761/
Auth         token
Listening on http://127.0.0.1:8761
```

## STOP 条件

- "现状"摘录与代码对不上。
- 删除某个配置字段时发现它其实有生产读取者（grep 结果出现在非测试代码里）。
- 发现某个外部调用方（脚本、部署文件 `deploy/`、`Dockerfile`）依赖被删配置项或依赖 `Run()` 的旧签名。
- `scripts/package-graph.sh` 之后依赖图发生变化。
- 需要修改"范围"之外的文件。

## 维护说明

- 新增路由无需任何额外登记：只要经 `RestServer` 注册就会出现在启动路由表里。在路由器之外处理的路径（像 `/ws/chat`）必须在 `writeStartupBanner` 里显式列出。
- `Sign in` 行包含真实 token：任何让它出现在非终端输出里的改动都需要 owner 重新决策（D3）。
- 以后若真的需要读写超时、gzip 之类的能力，按需求重新设计配置项，不要恢复这批从未生效的字段名。
