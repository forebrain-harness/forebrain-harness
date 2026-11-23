# Plan 104：网关与浏览器不在同一台机器时，ChatGPT 登录靠「粘贴回调地址」完成

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **本计划有一道 owner 闸门**：Web 界面的任何改动都要先在设计稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`
> 上做出原型、**经 owner 确认**之后才能写前端（owner 规则）。所以本计划分两段——**Part A（引擎 + 网关，无界面）**可以直接做；
> **Part B Step B1 产出原型后必须停下**，等 owner 确认，B2 之后才能做。Part A 的验收（见 Done criteria A）不依赖界面。
>
> **前置**：计划 `108-login-flows-survive-strays-and-cancel` 必须已落地（本计划改同一个回调处理器，且沿用它的原则：state 不符的输入只被拒绝、不终止登录）。
> 先跑：`grep -n 'deliver := func' pkg/llm/openai/auth.go` 必须有命中；没有 → STOP 1。
>
> **Drift check（先跑）**：
> `git diff --stat 55019a2 -- pkg/llm/openai/auth.go pkg/gateway/login_api.go pkg/gateway/server.go frontend/src/lib/api.ts`
> 预期只会出现计划 108 对 `pkg/llm/openai/auth.go` 造成的变化（及你已知的、与本计划无关的 `frontend/src/lib/api.ts` 既有改动）。
> 其余有输出就对照下面 "Current state" 的摘录：不符即 STOP 2。
> （工作区里 `pkg/config/*`、`pkg/run/*`、`pkg/turn/*`、`pkg/tui/*` 的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P3
- **Effort**: L（Part A：M；Part B：M，且要等原型确认）
- **Risk**: MED（新增一个接受用户粘贴内容的网关入口；授权码是一次性且绑定网关手里的 PKCE verifier，风险可控，但这是登录路径）
- **Depends on**: `108-login-flows-survive-strays-and-cancel`（硬依赖）；Part B 另依赖 owner 对原型的确认
- **Category**: direction（把已知限制变成可用功能）
- **Planned at**: commit `55019a2`，2026-10-23
- **owner 2026-10-23 选定的做法**：粘贴回调地址（不做设备码）。设计稿现有的「已知限制：ChatGPT 的授权页必须在这台运行 gateway 的电脑上打开（不做远程中继）」
  被本计划**取代**，原型里相关三处文字（见 B1）要一起改。

## Why this matters

Web 版 ChatGPT 登录的回调只绑本机 `127.0.0.1:1455`（备用 `1457`，`pkg/llm/openai/auth.go` 的 `listenLoopback`）。授权服务器登录后把浏览器
重定向到 `http://localhost:1455/auth/callback?code=…&state=…`——**只有浏览器与网关在同一台机器时才打得开**。网关跑在服务器、容器（仓库有 docker 目标）
或另一台电脑上时，这条登录整条走不通，弹窗里只有一行小字「需要在这台运行 gateway 的电脑上完成授权」。

OpenAI 给这个公开客户端登记的重定向地址是固定的（就是上面那个 localhost 地址），所以不能换成网关的真实地址。可行且不依赖任何未公开端点的办法是：
**让用户把浏览器地址栏里那个打不开的回调地址原样复制，粘贴回网页**。网关拿到其中的 `code` 与 `state`，用自己持有的 PKCE verifier 换令牌——
授权码本来就是这样被换掉的，只是来源从「本机监听口」换成了「用户粘贴」。浏览器和网关在同一台机器时，监听口照常自动完成，粘贴只是备用。

## Current state

**引擎**（`pkg/llm/openai/auth.go`，已含计划 108 的改动）：

```go
func LoginWithBrowser(ctx context.Context, path string, out io.Writer, openBrowser func(url string) error) (Credentials, error) {
	ln, redirectURI, err := listenLoopback()
	if err != nil {
		return Credentials{}, err
	}
	defer ln.Close()

	state, err := randomURLToken(32)
	verifier, err := randomURLToken(64)
	... // 组装 authURL，query 里有 redirect_uri、state、code_challenge(S256)
	if openBrowser != nil { _ = openBrowser(authURL) } else { _ = openSystemBrowser(authURL) }

	type result struct { code string; err error }
	resultCh := make(chan result, 1)
	deliver := func(r result) { select { case resultCh <- r: default: } }
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 路径必须是 /auth/callback；state 不符只回 400、不 deliver；
		// error 参数 / 缺 code 才 deliver 错误；有 code 就 deliver(result{code: code})
	})
	go func() { server.Serve(ln) ... }()

	var callback result
	select {
	case callback = <-resultCh:
	case <-ctx.Done():
		callback.err = ctx.Err()
	}
	// 有期限的 Shutdown
	if callback.err != nil { return Credentials{}, callback.err }
	tokens, err := exchange(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {callback.code},
		"redirect_uri": {redirectURI}, "client_id": {ClientID}, "code_verifier": {verifier},
	})
	...Save...
}

func listenLoopback() (net.Listener, string, error) {
	for _, port := range []int{1455, 1457} {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, fmt.Sprintf("http://localhost:%d/auth/callback", port), nil
		}
	}
	return nil, "", errors.New("cannot start ChatGPT login callback on localhost ports 1455 or 1457")
}
```

`Login(ctx, path, out)`（TUI 的 `/connect` 用它）调 `LoginWithBrowser(ctx, path, out, nil)`——**TUI 不改**。

**网关**（`pkg/gateway/login_api.go`）：

- `chatGPTLoginRun = openai.LoginWithBrowser`（包级变量，`:63-66`，测试替换它），类型是
  `func(ctx, path string, out io.Writer, openBrowser func(string) error) (openai.Credentials, error)`。
- `gatewayAuthLogin`（`:88-105`）是一次登录的状态：`provider/status/url/userCode/errText/baseURL/keepUntil/cancel`；`Server.authLogins` 是 `map[string]*gatewayAuthLogin`，
  `authLoginMu` 保护（`server.go:78-83`）。
- `authLoginStart(provider, keepUntil, cancel) (id string, err error)`（`:114`）登记一次待定登录；`handleAuthLoginStart`（`:234`）在后台 goroutine 里跑 `runAuthLogin`。
- `runAuthLogin(ctx, provider, id)`（`:299`）对 chatgpt 调 `chatGPTLoginRun(ctx, openai.CredentialsPath(s.Home), io.Discard, func(url string) error { s.authLoginUpdate(id, ...url...); return nil })`。
- 路由在 `pkg/gateway/api_extra.go:55-57`：`auth.Post("/login/:provider", …)`、`auth.Get("/login/:provider/:loginId", …)`、`auth.Post("/login/:provider/:loginId/cancel", …)`。
- 测试夹具 `pkg/gateway/login_api_test.go`：`loginAPIServer(t)` 返回 `(*Server, gatewayDo)`，`gatewayDo func(method, target string) *httptest.ResponseRecorder`
  （请求体恒为 nil）；`startLogin`、`awaitPending`、`awaitTerminal`；测试里用 `chatGPTLoginRun = func(ctx, path, out, openBrowser) …` 替换流程
  （`:156-174`、`:367-372` 两处）。

**前端**（`frontend/src/views/ProvidersView.vue`）：连接弹窗 `auth` 步骤（约 `:228-252`）对 chatgpt 显示「打开授权页」按钮 + 转圈 + 一行
`t('providers.connectChatGPTLocal')`（"需要在这台运行 gateway 的电脑上完成授权。"）；`beginConnectLogin`/`pollConnect`/`pollConnectOnce` 轮询状态；
`frontend/src/lib/api.ts` 有 `startProviderLogin` / `providerLoginStatus` / `cancelProviderLogin`（约 `:3553-3584`）。

**设计稿**（`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，owner 在工作区里有未提交改动，不要回滚）：
`provLoginModal(k)`（约 `:5280`）里 `k === 'provchatgpt'` 分支是 ChatGPT 授权页；文字"需要在这台运行 gateway 的电脑上完成授权"在 `:5295`；
另有两处文字同义：`:1271` 的 `<li>`（"…已知限制：不做远程中继…"）与 `:2909` 的 `providers` 说明最后一句（"ChatGPT 的授权页要在这台运行 gateway 的电脑上打开"）。

**约定**：`pkg/llm` 的测试风格看 `auth_test.go`；网关错误文案对前端用**机器可读的短码**（前端映射成 i18n 文案，见 `login_api.go` 里 `authLoginReason` 的做法）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 构建 | `go build ./...` | exit 0（`ld: warning` 可忽略） |
| 引擎 + 网关测试 | `go test -tags fts5 ./pkg/llm/openai/ ./pkg/gateway/ -count=1` | 两个 `ok` |
| 竞态 | `go test -tags fts5 -race ./pkg/llm/openai/ -run 'Login' -count=1` | `ok` |
| gofmt / vet | `test -z "$(gofmt -l pkg)" && go vet ./pkg/llm/... ./pkg/gateway/` | exit 0（若 `gofmt -l` 列出 `pkg/tui/notify.go`，那是计划 103 要修的既有问题，忽略） |
| 包图快照 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 `pkg/llm/openai`、`pkg/gateway` 的 `loc` 变化 |
| 前端单测（Part B） | `cd frontend && npx vitest run src/views/ProvidersView.test.ts` | 全绿 |
| 前端类型（Part B） | `cd frontend && npx vue-tsc --noEmit` | 不新增错误 |

## Scope

**In scope**（Part A）：
- `pkg/llm/openai/auth.go`、`pkg/llm/openai/auth_test.go`
- `pkg/gateway/login_api.go`、`pkg/gateway/login_api_test.go`、`pkg/gateway/server.go`（仅给 `gatewayAuthLogin` 加字段不需要动它；若 `authLogins` 的声明要改才动）、`pkg/gateway/api_extra.go`（仅路由注册一行）
- `pkg/architecture/testdata/graph.json`（只允许 `scripts/package-graph.sh` 重新生成）

**In scope**（Part B，原型确认之后）：
- `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（B1）
- `frontend/src/views/ProvidersView.vue`、`frontend/src/views/ProvidersView.test.ts`、`frontend/src/lib/api.ts`、`frontend/src/locales/index.ts`

**Out of scope**：
- TUI 的 `/connect`（`openai.Login`、`pkg/tui/setup.go`）——终端在远程机器上（SSH）也有同样的限制，但不在本计划；`Login` 的行为必须保持不变。
- OpenCode 的登录（设备码，本来就不依赖本机）。
- 设备码流程——owner 没选；不要实现，也不要去探测 OpenAI 的设备授权端点。
- 重定向地址：**必须**保持 `http://localhost:1455/auth/callback`（或 `:1457`）——那是 OpenAI 为这个客户端登记的值，改了授权会被拒。
- `pkg/gateway/dist/*`、`frontend/e2e/*`——不重建 dist，不跑 e2e。
- 任何地方都**不要记录粘贴进来的地址**（日志、错误、状态体）——它含一次性授权码。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Part A（引擎 + 网关）

#### Step A1：先写会红的引擎测试

在 `pkg/llm/openai/auth_test.go`，仿 `TestLoginWithBrowserHandsTheAuthorizationURLToTheCallback`（`:269`）与 108 新增的 `TestLoginWithBrowserIgnoresACallbackWithTheWrongState`，
给将要新增的 `LoginWithManualCallback`（A2）写测试。这一步先写，**此刻编译不过是预期的**（符号还不存在）——A2 之后才跑。共通夹具：
`stubTokenEndpoint` 回有效令牌（`access_token: jwtWithExp(t, time.Now().Add(time.Hour))`, `refresh_token: "refresh-1"`）；`openBrowser` 回调里解析授权 URL 的 `redirect_uri` 与 `state`；
`manual := make(chan ManualCallback)`；一个辅助 `submit(url string) error` 把 `ManualCallback{URL: url, Reply: reply}` 发进 `manual` 并等 `reply`（超时 2 秒）。

1. `TestManualCallbackCompletesTheLogin`：`openBrowser` 里启动 goroutine，`submit(redirect + "?code=real&state=" + state)` 返回 `nil`；`LoginWithManualCallback` 返回无错，凭据文件写入且 `RefreshToken == "refresh-1"`。
   **同时**用「监听口被占满」的方式验证无监听口也能完成：本测试里先自己 `net.Listen` 占住 `127.0.0.1:1455` 与 `127.0.0.1:1457`（任一占不到就 `t.Skip`），这时 `LoginWithManualCallback` 必须仍然进入等待并被手动回调完成。
2. `TestManualCallbackRejectsWithoutEndingTheLogin`：表驱动，每一行都先 `submit` 一个坏输入、断言 `Reply` 收到**指定的错误**、登录**仍在等待**，最后再 `submit` 一个好地址、登录完成：
   | 输入 | 期望错误 |
   |---|---|
   | `"hello"` | `ErrCallbackNotACallback` |
   | `redirect + "?code=x&state=WRONG"` | `ErrCallbackStateMismatch` |
   | `"http://evil.example.com:1455/auth/callback?code=x&state=" + state` | `ErrCallbackNotACallback` |
   | `"http://localhost:9999/auth/callback?code=x&state=" + state`（端口不是我们的） | `ErrCallbackNotACallback` |
   | `"http://localhost:1455/other?code=x&state=" + state`（路径不对） | `ErrCallbackNotACallback` |
   | `redirect + "?state=" + state`（没有 code） | `ErrCallbackNoCode` |
3. `TestManualCallbackAcceptsWhatABrowserShows`：这些写法都应被接受（各自跑一次完整登录，或直接测解析函数 `parseCallback`）：
   前后带空白的地址；`127.0.0.1` 代替 `localhost`（端口相同）；没有 `http://` 前缀的 `localhost:1455/auth/callback?...`；`state`/`code` 之外多出的查询参数（如 `scope=...`）。
4. `TestManualCallbackCarriesTheProvidersOwnRefusal`：地址里是 `?error=access_denied&error_description=nope&state=<真 state>` → `Reply` 收到 `nil`（这是一个**有效的回答**），登录以 "ChatGPT sign-in failed: nope" 结束（与监听口收到同样回答时一致）。
5. `TestLoginWithBrowserStillWorksWithoutAManualChannel`：108 的既有测试就是它的护栏；这里不用新增，只确认它们仍绿。

#### Step A2：引擎实现

`pkg/llm/openai/auth.go`：

1. 新增导出的类型与错误（放在 `LoginWithBrowser` 之前）：

```go
// ManualCallback is a sign-in callback handed over by hand: the address the
// browser was sent to after the user approved, copied from its address bar.
// A gateway that is not on the browser's machine cannot receive the redirect
// itself (it goes to localhost on the browser's side), so the page asks the
// user to paste it. Reply gets nil when the flow took the callback as the
// provider's answer and an error saying why not otherwise — a rejected callback
// never ends the login, because the sender is a person who can try again.
type ManualCallback struct {
	URL   string
	Reply chan<- error
}

var (
	// ErrCallbackNotACallback: the text is not this sign-in's redirect address.
	ErrCallbackNotACallback = errors.New("not a sign-in callback address")
	// ErrCallbackStateMismatch: the address belongs to a different sign-in.
	ErrCallbackStateMismatch = errors.New("callback belongs to a different sign-in")
	// ErrCallbackNoCode: the address carries no authorization code.
	ErrCallbackNoCode = errors.New("callback carries no authorization code")
)
```

2. 把 `LoginWithBrowser` 的函数体改名为 `LoginWithManualCallback(ctx, path, out, openBrowser, manual <-chan ManualCallback)`，`LoginWithBrowser` 变成一行委托：
   `return LoginWithManualCallback(ctx, path, out, openBrowser, nil)`。**`Login`、`LoginWithBrowser` 的签名与行为不变。**
3. 监听口失败的处理：

```go
	ln, redirectURI, err := listenLoopback()
	if err != nil {
		if manual == nil {
			return Credentials{}, err
		}
		// Both ports taken (or no loopback at all): the redirect address the
		// authorization server accepts is fixed and never has to be served when
		// the user hands the callback over.
		ln, redirectURI = nil, "http://localhost:1455/auth/callback"
	}
	if ln != nil {
		defer ln.Close()
	}
```

   并让 HTTP 服务只在有监听口时才创建、启动和关闭——`deliver`/`resultCh` 保持在外面。形状：

```go
	var server *http.Server
	if ln != nil {
		server = &http.Server{ReadHeaderTimeout: 10 * time.Second}
		server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { /* 与现在完全相同 */ })
		go func() {
			if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				deliver(result{err: err})
			}
		}()
	}
	// …下面的等待循环（第 4 点）…
	if server != nil {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		stop()
	}
```
4. 等待回调那段改成循环（`manual` 为 nil 时 `case m := <-manual` 永远不就绪，所以现有行为不变）：

```go
	var callback result
wait:
	for {
		select {
		case callback = <-resultCh:
			break wait
		case m := <-manual:
			code, providerErr, rejected := parseCallback(m.URL, redirectURI, state)
			if m.Reply != nil {
				select {
				case m.Reply <- rejected:
				default:
				}
			}
			if rejected != nil {
				continue
			}
			callback = result{code: code, err: providerErr}
			break wait
		case <-ctx.Done():
			callback.err = ctx.Err()
			break wait
		}
	}
```

5. 新增 `parseCallback`：

```go
// parseCallback reads a pasted callback address against this sign-in. rejected
// means the text is not an answer to this sign-in (the caller keeps waiting);
// providerErr is the authorization server's own refusal carried by a valid one.
func parseCallback(raw, redirectURI, state string) (code string, providerErr, rejected error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, ErrCallbackNotACallback
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	got, err := url.Parse(raw)
	if err != nil {
		return "", nil, ErrCallbackNotACallback
	}
	want, err := url.Parse(redirectURI)
	if err != nil {
		return "", nil, ErrCallbackNotACallback
	}
	// The browser shows localhost or 127.0.0.1 depending on how it got there;
	// the port is the one this sign-in's redirect address names.
	if got.Scheme != "http" || got.Port() != want.Port() ||
		(!strings.EqualFold(got.Hostname(), "localhost") && got.Hostname() != "127.0.0.1") ||
		got.Path != want.Path {
		return "", nil, ErrCallbackNotACallback
	}
	q := got.Query()
	if q.Get("state") != state {
		return "", nil, ErrCallbackStateMismatch
	}
	if oauthErr := q.Get("error"); oauthErr != "" {
		msg := strings.TrimSpace(q.Get("error_description"))
		if msg == "" {
			msg = oauthErr
		}
		return "", fmt.Errorf("ChatGPT sign-in failed: %s", msg), nil
	}
	code = strings.TrimSpace(q.Get("code"))
	if code == "" {
		return "", nil, ErrCallbackNoCode
	}
	return code, nil, nil
}
```

   绝不把 `raw` 写进任何日志或错误。

**Verify**：
- `go build ./pkg/llm/... && go vet ./pkg/llm/openai/` → exit 0。
- `go test -tags fts5 ./pkg/llm/openai/ -count=1` → `ok`（A1 的 5 类测试全绿，108 的测试与既有登录测试不变）。
- `go test -tags fts5 -race ./pkg/llm/openai/ -run 'Login|ManualCallback' -count=1` → `ok`，无 `DATA RACE`。

#### Step A3：先写会红的网关测试

在 `pkg/gateway/login_api_test.go`：

1. 把 `gatewayDo` 改成可选请求体：`type gatewayDo func(method, target string, body ...string) *httptest.ResponseRecorder`，
   `loginAPIServer` 里的闭包把 `body[0]`（若有）作为 `httptest.NewRequest` 的 `strings.NewReader(body[0])`。既有调用点不用改。
2. 两处 `chatGPTLoginRun = func(ctx, path, out, openBrowser) …` 的测试替身（`:156`、`:367`）改成带第五个参数 `manual <-chan openai.ManualCallback`（A4 之后的类型）。
3. 新增：
   - `TestAuthLoginChatGPTAcceptsAPastedCallback`：替身流程把授权 URL 交给页面后阻塞在 `manual` 上：收到 `ManualCallback` 就把 `Reply <- nil` 并返回成功凭据。
     测试里 `POST /api/auth/login/chatgpt/<id>/callback`，体 `{"url":"http://localhost:1455/auth/callback?code=c&state=s"}` → 状态码 200；随后 `awaitTerminal` 为 `complete`。
   - `TestAuthLoginPastedCallbackRejectionIsReportedAndKeepsTheLoginPending`：替身对收到的回调 `Reply <- openai.ErrCallbackStateMismatch` 后继续等；
     接口应返回 400，正文**恰好**是 `state_mismatch`（`ErrCallbackNotACallback` → `not_a_callback`，`ErrCallbackNoCode` → `no_code`）；登录状态仍是 `pending`。
   - `TestAuthLoginPastedCallbackNeedsAPendingChatGPTLogin`：对 opencode 的登录 id → 404；对已结束的 chatgpt 登录 → 409；未知 id → 404；非 POST → 405；
     体不是 JSON 或没有 `url` 字段 → 400 `bad_request`；体超过 8 KiB → 400 `bad_request`。
   - `TestAuthLoginPastedCallbackNeverLeaksTheAddress`：提交一个含独特标记串 `code=SECRET-MARKER` 的地址（被拒的那种），断言响应体与 `awaitTerminal`/状态体里**都不含** `SECRET-MARKER`。

**Verify**：此刻编译不过是预期的；A4 之后才跑。

#### Step A4：网关实现

`pkg/gateway/login_api.go`：

1. 变量类型：

```go
var (
	openCodeLoginRun = opencode.LoginDeviceFlow
	chatGPTLoginRun  = openai.LoginWithManualCallback
)
```

2. `gatewayAuthLogin` 加字段 `manual chan openai.ManualCallback`（仅 chatgpt 的登录有；注释说明）。
3. `authLoginStart` 增加参数 `manual chan openai.ManualCallback`（opencode 传 `nil`），存进新登录。`handleAuthLoginStart` 里对 chatgpt 创建
   `manual := make(chan openai.ManualCallback)` 并传入；`runAuthLogin(ctx, provider, id, manual)` 把它交给 `chatGPTLoginRun` 的第五个参数（opencode 分支不变）。
4. 新处理器：

```go
// handleAuthLoginCallback takes the address a user pasted from their browser's
// address bar after approving a ChatGPT sign-in whose redirect could not reach
// this machine. The flow decides whether it is theirs; the answer is a short
// machine-readable reason the page words itself. The address carries a one-time
// authorization code, so it is never logged or echoed.
func (s *Server) handleAuthLoginCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if s == nil {
		http.Error(w, "nil", http.StatusInternalServerError)
		return
	}
	provider := authLoginProvider(pathParam(r, "provider"))
	if provider != authLoginChatGPT {
		http.Error(w, "unknown login provider", http.StatusNotFound)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		http.Error(w, "bad_request", http.StatusBadRequest)
		return
	}
	manual, status, ok := s.authLoginManual(provider, pathParam(r, "loginId"))
	if !ok {
		http.Error(w, "unknown login", http.StatusNotFound)
		return
	}
	if status != authLoginPending || manual == nil {
		http.Error(w, "login not pending", http.StatusConflict)
		return
	}
	reply := make(chan error, 1)
	select {
	case manual <- openai.ManualCallback{URL: body.URL, Reply: reply}:
	case <-time.After(5 * time.Second):
		http.Error(w, "login not pending", http.StatusConflict)
		return
	case <-r.Context().Done():
		return
	}
	select {
	case err := <-reply:
		if err != nil {
			http.Error(w, callbackReason(err), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	case <-time.After(5 * time.Second):
		http.Error(w, "login not pending", http.StatusConflict)
	case <-r.Context().Done():
	}
}

// callbackReason maps the flow's refusal to the short code the page words.
func callbackReason(err error) string {
	switch {
	case errors.Is(err, openai.ErrCallbackStateMismatch):
		return "state_mismatch"
	case errors.Is(err, openai.ErrCallbackNoCode):
		return "no_code"
	default:
		return "not_a_callback"
	}
}
```

   并加 `authLoginManual(provider, id) (chan openai.ManualCallback, string, bool)`（持锁读出该登录的 `manual` 与 `status`，provider 不匹配或 id 未知返回 `ok=false`），风格同 `authLoginGet`。
   注意：被接受的回调成功后，流程用令牌换票会花一点时间——接口只回 `202`（"已收下"），完成与否仍由页面轮询状态得到。
5. `pkg/gateway/api_extra.go` 的路由注册处（`:55-57`）加一行：
   `auth.Post("/login/:provider/:loginId/callback", s.handleAuthLoginCallback)`。

**Verify**：
- `go build ./... && go vet ./pkg/gateway/` → exit 0。
- `go test -tags fts5 ./pkg/gateway/ -run 'AuthLogin' -count=1 -v 2>&1 | grep -E '^(--- (PASS|FAIL)|ok|FAIL)'` → A3 的 4 个新测试与既有 `TestAuthLogin*` 全 `PASS`。
- `go test -tags fts5 -race ./pkg/gateway/ -run 'AuthLogin' -count=1` → `ok`。

#### Step A5：Part A 收尾门禁

1. `scripts/package-graph.sh`，`git diff --stat -- pkg/architecture/testdata/graph.json`：只应有 `pkg/llm/openai`、`pkg/gateway` 的 `loc` 变化。
2. `test -z "$(gofmt -l pkg/llm pkg/gateway)"`；`go build ./... && go vet ./pkg/llm/... ./pkg/gateway/` → exit 0。
3. `go test -tags fts5 ./pkg/llm/... ./pkg/gateway/ ./pkg/tui/ -count=1` → 全 `ok`（`pkg/tui` 用 `openai.Login`，确认没被牵连）。

### Part B（界面——必须等 owner 确认原型）

#### Step B1：在设计稿上做原型，然后**停下**

在 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 上迭代（不要另起文件；原型外框保持白色/浅色，不加深色模式覆盖）：

1. 改 `provLoginModal` 的 `provchatgpt` 分支（约 `:5293`）：去掉「需要在这台运行 gateway 的电脑上完成授权」那行小字，改成备用路径——
   授权页按钮下方一个输入框（占位：把浏览器地址栏里的完整地址粘贴到这里）和一个小按钮；文案要简洁（owner 规则：能用标签表达就不写句子）。
   推荐形态：一行标签「打不开页面？粘贴地址栏里的完整地址」+ 输入框 + 「提交」按钮，放在转圈那一行之下。
2. 新增一页展示「粘贴了错误地址」的状态（输入框下方一行红色提示，三种原因各一句短话：不是登录回调地址 / 是另一次登录的地址 / 地址里没有授权码）。
   把它登记进 `provLoginKinds`（`:5272`）和页面清单（`:4249` 那一长行里 `provchatgpt` 之后），命名如 `provchatgptpaste`。
3. 同步改三处过时文字：`:1271` 的 `<li>`、`:2909` 的 `providers` 说明最后一句、`provLoginModal` 里原来的小字——把「必须在这台电脑上打开」改成「也可以在别的电脑上授权，把地址栏里的完整地址贴回这里」的意思。

**Verify**：在浏览器里打开 `app-preview.html`（`open docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`），能从「模型服务 · 连接 ChatGPT（授权页）」进入新界面，新增的错误页能切到；控制台无报错。

**然后停下**：在报告里写明原型改了哪三处、新页面叫什么，请 owner 确认。**未得到确认不要做 B2。**

#### Step B2：API 客户端与文案（owner 确认后）

1. `frontend/src/lib/api.ts`，在 `cancelProviderLogin` 之后加：

```ts
  /** Hands the gateway the address a user pasted from their browser after approving
   * a ChatGPT sign-in whose redirect could not reach the gateway's machine. 202 means the
   * flow took it (the poll reports the ending); a refusal is a short reason code. */
  async submitProviderLoginCallback(provider: ProviderLoginProvider, loginId: string, url: string) {
    const res = await gatewayFetch(`${API_BASE}/auth/login/${provider}/${encodeURIComponent(loginId)}/callback`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    })
    if (!res.ok) throw new GatewayHttpError(res.status, await gatewayErrorText(res))
  },
```

2. `frontend/src/locales/index.ts`：中英文各加（键名可按原型确认的文案调整，两种语言必须同步）：
   `providers.connectPasteLabel`、`providers.connectPasteSubmit`、`providers.connectPasteNotCallback`、`providers.connectPasteStateMismatch`、`providers.connectPasteNoCode`；
   并**删除**不再使用的 `providers.connectChatGPTLocal`（中英文两处）。

**Verify**：`cd frontend && npx vue-tsc --noEmit` → 不新增错误；`grep -n "connectChatGPTLocal" frontend/src -r` 无命中（B3 之后）。

#### Step B3：弹窗实现（1:1 还原已确认的原型）

`ProvidersView.vue` 的 `auth` 步骤，对 `connectProvider === 'chatgpt'`：加输入框 + 提交按钮（用项目里已有的表单组件，看同文件其它输入框的写法，不要引入新的原生控件）；
提交时调用 `forebrainApi.submitProviderLoginCallback('chatgpt', connectLoginId.value, text)`：
- 成功（202）：清空输入框，继续轮询（页面照旧在 `complete` 时进入选模型）。
- `GatewayHttpError` 的 `message` 是 `state_mismatch` / `no_code` / `not_a_callback`：显示对应提示（`bad_request` 与其它归为 `not_a_callback` 的提示）；409/404：按现有「登录已丢失」路径处理（`connectStep = 'lost'`）。
- 输入框与按钮带 `data-testid`：`provider-login-paste`、`provider-login-paste-submit`、`provider-login-paste-error`。

**Verify**：`cd frontend && npx vue-tsc --noEmit` → 不新增错误。

#### Step B4：前端测试

`ProvidersView.test.ts`，参照同文件现有的连接弹窗测试（搜 `provider-login-open` 或 `startProviderLogin` 的用例）新增：
1. 粘贴框**只在 chatgpt** 的授权步骤出现（opencode 不出现）。
2. 提交会以当前 `loginId` 调用 `submitProviderLoginCallback`，成功后输入框清空、仍在 `auth` 步骤。
3. 三种拒绝原因各显示各的提示，且登录没有结束（仍在 `auth` 步骤）。
4. 提交的地址不会出现在页面任何文字里（回显除外：输入框自身的值）。

**Verify**：`cd frontend && npx vitest run src/views/ProvidersView.test.ts` → 全绿；`cd frontend && npx vue-tsc --noEmit` → 不新增错误。

## Test plan

见 A1、A3、B4。结构样板：`pkg/llm/openai/auth_test.go` 的 `TestLoginWithBrowserHandsTheAuthorizationURLToTheCallback`；
`pkg/gateway/login_api_test.go` 的 `TestAuthLoginChatGPTReportsTheAuthorizationURLAndCompletes`。
**可证伪性**：A1 的拒绝表在去掉 `parseCallback` 的某项检查时必须有行变红；A3 的「不泄露」用例在把 `raw` 拼进错误时必须变红。

## Done criteria

**Part A**（可独立验收）：
- [ ] `go test -tags fts5 ./pkg/llm/... ./pkg/gateway/ ./pkg/tui/ -count=1` 全 `ok`
- [ ] `go test -tags fts5 -race ./pkg/llm/openai/ ./pkg/gateway/ -run 'Login|ManualCallback|AuthLogin' -count=1` → `ok`
- [ ] `grep -rn 'LoginWithManualCallback' pkg --include='*.go' | grep -v _test` 恰有：定义、`LoginWithBrowser` 委托、`pkg/gateway/login_api.go` 一处
- [ ] `grep -n 'redirect_uri' pkg/llm/openai/auth.go` 仍只来自 `listenLoopback` 的两个 localhost 地址与 A2 的 1455 备用值（没有网关地址）
- [ ] `git diff --name-only -- pkg/architecture/testdata/graph.json` 只有这一个文件，且只含 loc 变化
- [ ] 没有任何 `git commit` / `git push` / 新分支

**Part B**（owner 确认原型之后）：
- [ ] 原型已被 owner 确认（报告里引用确认的原话或日期）
- [ ] `cd frontend && npx vitest run src/views/ProvidersView.test.ts` 全绿，且 `npx vue-tsc --noEmit` 不新增错误
- [ ] `grep -rn "connectChatGPTLocal" frontend/src` 无命中
- [ ] 评审方真机验收（`run-forebrain` 技能）：网关起在一台机器，浏览器在另一台（或用端口被占住的网关）→ 点「连接 ChatGPT」→ 授权 → 贴回地址栏地址 → 进入选模型

## STOP conditions

1. 计划 108 没落地（`deliver := func` 不存在）。
2. 摘录对不上现状（`LoginWithBrowser`、`runAuthLogin`、`gatewayAuthLogin`、路由注册任一已被改过）。
3. 实现需要改 `Login` / `LoginWithBrowser` 的签名，或改 TUI——说明拆法不对，停下报告。
4. 发现 OpenAI 的授权服务器拒绝以「用户粘贴来源」换票（`exchange` 对同一 `redirect_uri` 和 `code_verifier` 报 `invalid_grant`）——这在真机上才能验证；停下报告，不要尝试改 redirect_uri。
5. B1 完成后没有得到 owner 的确认就想继续做 B2——不许。
6. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 授权码一次性、且只有持有 PKCE verifier 的网关能换成令牌，所以「地址被粘贴到网页里」不是新的泄露面；但它仍是登录路径上的输入，**永远不要记录它**。
- 监听口依然是主路径（浏览器与网关同机时用户什么都不用做）。粘贴是备用，不要把页面改成「默认只让粘贴」。
- 终端的 `/connect` 在远程机器上（SSH）有同样的限制；若要解决，复用 `LoginWithManualCallback`，让 TUI 的 `openBrowser` 回调打印地址并从输入框读取粘贴——另立计划。
- 评审重点：`parseCallback` 的主机/端口/路径校验（防止把别的站点的地址当成回调）；网关处理器的两次 5 秒等待是否会在登录刚好结束时泄漏 goroutine（`select` 的 `r.Context().Done()` 分支）。
