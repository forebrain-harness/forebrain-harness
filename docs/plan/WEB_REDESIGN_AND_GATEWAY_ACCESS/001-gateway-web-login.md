# 计划 001：浏览器通过一次性登录获得会话凭据，web 端不再出现 401；建立自动化真机验证脚手架

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告，不要自行变通。
> 完成后更新 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/README.md` 中本计划的状态行。**不要提交代码**（见 README 全局规则 1）。
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/gateway/controlplane.go pkg/gateway/server.go pkg/gateway/api_extra.go pkg/config/gateway.go frontend/src/lib/api.ts frontend/src/composables/useAuth.ts frontend/src/composables/useChatStream.ts frontend/src/lib/forebrainGatewayRuntime.ts frontend/src/router/index.ts frontend/src/main.ts frontend/src/App.vue frontend/vite.config.ts frontend/package.json`
> 若有输出，先把下文"现状"里的摘录与当前代码逐一对照；对不上即按 STOP 条件处理。

## 状态

- **优先级**：P0
- **工作量**：L
- **风险**：MED（改动鉴权；有单测和真机验证双重覆盖）
- **依赖**：无
- **类别**：bug / security
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

每次全新安装，`pkg/home/seed.go` 都会生成一个随机 gateway token 写进 `~/.forebrain/.env`，配置默认 `gateway.auth.mode: token`；
控制面中间件因此要求每个 `/api` 请求携带这个 token。但 web 前端**没有任何途径拿到它**：`api.ts` 只在内存里放一个 token，
唯一给它赋值的是一个从未被调用的假登录桩（生成 `local-<用户名>`）。结果是默认安装下 web 端所有接口都返回 401，
这正是 owner 看到的 `auth=""` 日志。另外，即便前端拿到了 token，6 处裸 `fetch` 和附件 `<img src>` 也带不上请求头。

修复方向：浏览器用一次性登录把 gateway token 换成一个 **HttpOnly、SameSite=Strict 的会话 Cookie**。同源的 axios、fetch、`<img>`、
WebSocket 握手都会自动带上它，前端代码不再接触 token 本身。Cookie 的值是 token 的 HMAC 派生值，不含状态：
多副本部署共享同一 token 时任何副本都能校验，轮换 token 会让旧 Cookie 全部失效，Cookie 泄露也反推不出 token。

## 现状

- `pkg/home/seed.go:66-80`：首次运行生成 32 字节随机 token，写入 `~/.forebrain/.env` 的 `FOREBRAIN_GATEWAY_TOKEN`，配置里引用 `${FOREBRAIN_GATEWAY_TOKEN}`。
- `pkg/config/load.go:456-458`：`gateway.auth.mode` 缺省为 `token`。
- `pkg/gateway/controlplane.go`（全文 130 行）——控制面鉴权：

  ```go
  // controlplane.go:15-31
  func ControlPlaneTokenFromRequest(r *http.Request, queryToken string) string {
  	...
  	auth := strings.TrimSpace(r.Header.Get("Authorization"))
  	token := ""
  	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
  		token = strings.TrimSpace(auth[7:])
  	}
  	if token == "" {
  		token = strings.TrimSpace(r.Header.Get("X-API-Key"))
  	}
  	if token == "" && queryToken != "" {
  		token = strings.TrimSpace(queryToken)
  	}
  	return token
  }
  // controlplane.go:67-95（节选）
  		authMode := strings.ToLower(strings.TrimSpace(cfg.Gateway.Auth.Mode))
  		if authMode == "" { authMode = "token" }
  		if authMode == "none" { inner.ServeHTTP(w, r); return }
  		tok := strings.TrimSpace(cfg.Gateway.Auth.Token)
  		if authMode != "token" { ... 401 {"error":"unsupported gateway auth mode"} }
  		q := r.URL.Query().Get("token")
  		if tok == "" { ... 401 {"error":"gateway token missing"} }
  		if !ControlPlaneAuthorized(r, tok, q) {
  			slog.Error("gateway control plane auth failed", "path", r.URL.Path, "auth", telemetry.RedactLogLine(r.Header.Get("Authorization")))
  			... 401 {"error":"unauthorized"}
  		}
  ```
  `controlplane.go:101` 起的 `ServeHTTPChain` 把 `/ws/chat` 直接交给 `gw.HandleChatWS`，不经过上面的中间件。
- `pkg/gateway/server.go:586-588`：`var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}`——接受任意来源。
- `pkg/gateway/server.go:651-665`：WebSocket 自己再做一遍鉴权，只看 token 是否非空，**不看 `auth.mode`**（`mode: none` 且配置了 token 时，HTTP 放行而 WebSocket 拒绝）：

  ```go
  func (s *Server) HandleChatWS(w http.ResponseWriter, r *http.Request) {
  	if tok := s.controlPlaneToken(); tok != "" {
  		q := r.URL.Query().Get("token")
  		if !ControlPlaneAuthorized(r, tok, q) {
  			http.Error(w, "Unauthorized", http.StatusUnauthorized)
  			return
  		}
  	}
  ```
- `pkg/gateway/api_extra.go:43` 起的 `AttachExtraRoutes` 注册全部 `/api` 路由，用 `routes.Group("/api")`。处理函数的风格参考
  `pkg/gateway/memories_api.go:21-45`：`json.NewDecoder` + `DisallowUnknownFields()`，错误用 `http.Error`。
- `pkg/config/gateway.go:37-51` 的 `GatewayControlPlaneAuthExemptPath` 决定哪些路径免鉴权（健康检查和渠道入站 webhook）。
- 前端 `frontend/src/lib/api.ts`：
  - `:6-24` 模块级 `authUser`、`authToken` 及 `getUser/setUser/getToken/setToken`；
  - `:26-36` `getErrorMessage` 只识别响应体里的 `message` 字段，而 gateway 的错误体是 `{"error": "..."}`；
  - `:839-846` axios 请求拦截器在有 token 时加 `Authorization: Bearer`；
  - `:1148`、`:1194`、`:1203`、`:1216`、`:1237` 五处裸 `fetch`（`sessionRewindLast`、`actionsList`、`actionsAnswer`、`actionsApprove`、`actionsDeny`）不带任何凭据；
  - `:1431-1454` 假登录桩 `authLogin/authRegister/authCurrent/authLogout`；
  - `:1459-1476` `filesUpload` 用裸 `fetch`，手工拼 `Authorization`。
- `frontend/src/views/ChatView.vue:1643-1645`：`attachmentUrl()` 返回 `/api/files/<id>/download`，用作 `<img src>`，浏览器不会为它加请求头。
- `frontend/src/composables/useChatStream.ts:1592`、`:2588`：`new WebSocket(buildBrowserForebrainGatewayChatWsUrl(getToken() ?? undefined))`。
- `frontend/src/lib/forebrainGatewayRuntime.ts:588-641`：`parseBotMentionsFromEnv`、`getForebrainGatewayRuntimeConfig`（读取 `VITE_FOREBRAIN_GATEWAY_TOKEN`，会把密钥编译进产物）、
  `buildForebrainGatewayWsUrl`、`textTriggersBotMention`、`primaryBotMentionHint`、`getForebrainAdminBaseUrl`（默认 `http://127.0.0.1:18789`）——全部没有生产调用者；
  `:616-625` `buildBrowserForebrainGatewayChatWsUrl(token?)` 把 token 放进 URL 查询串。
- `frontend/src/composables/useAuth.ts`：匿名 `clientId`、`login/register/logout/checkAuth/isAuthenticated` 没有调用者；真正被用到的只有
  `lastSessionId`（`ChatView.vue:686`）、`persistLastSessionId`（`useChatStream.ts:946`、`usePrimaryAgents.ts:2`）、`lastSessionIdValue`（`McpView.vue:179`）。
- `frontend/src/main.ts`：`app.use(router); app.mount('#app')`，没有等待首次导航。
- `frontend/vite.config.ts:53-63`：一段转发到 `http://localhost:8216` 的 `/forebrain/api/v1` 旧规则，与本项目无关；`:64-71` 的 `/api`、`/ws` 代理指向 `127.0.0.1:6060`。

## 设计

### 服务端

1. **会话 Cookie**
   - 名称：`forebrain_session_` + `hex(sha256(token))` 的前 8 个字符。名称随 token 变化，同一台机器上用不同 home（例如 `~/.forebrain` 与 `~/.forebrain_01`）
     启动的两个 gateway 不会互相覆盖 Cookie；共享同一 token 的集群副本名称一致。
   - 值：`hex(HMAC-SHA256(key = token, message = "forebrain-harness web session v1"))`。
   - 属性：`Path=/`、`HttpOnly`、`SameSite=Strict`、`Max-Age=2592000`（30 天）；当 `r.TLS != nil` 或 `X-Forwarded-Proto` 为 `https` 时加 `Secure`。
2. **新文件 `pkg/gateway/web_session.go`**
   - `func webSessionCookieName(token string) string`、`func webSessionValue(token string) string`。
   - `POST /api/auth/session`，请求体 `{"token": "<gateway token>"}`，用 `http.MaxBytesReader(w, r.Body, 4096)` 限制大小，`DisallowUnknownFields()`：
     - `auth.mode` 为 `none`：返回 204，不设 Cookie；
     - token 与配置一致（`crypto/subtle.ConstantTimeCompare`）：`http.SetCookie` 后返回 204；
     - 不一致：`slog.Warn("gateway web sign-in rejected", "remote", clientIP(r))`，返回 401，正文 `{"error":"invalid gateway token"}`（`Content-Type: application/json`）；
     - 请求体无法解析：400，`http.Error(w, err.Error(), http.StatusBadRequest)`。
   - `GET /api/auth/session`：经过控制面中间件，能到达即已认证，返回 200 与 `{"auth_mode":"token"}`（或 `"none"`）。前端用它探测是否需要登录。
   - **不提供** DELETE/退出接口：本期没有任何界面会调用它，按"无调用者即删除"的规则不先写。
3. **统一鉴权规则**（`controlplane.go`）
   - 新增 `func authorizeControlPlane(r *http.Request, cfg *appcfg.Root) error`，返回 `nil` 表示放行，否则返回以下哨兵错误之一：
     `errGatewayAuthModeUnsupported`、`errGatewayTokenMissing`、`errGatewayUnauthorized`。规则：`mode` 为空视同 `token`；`none` 放行；
     `token` 模式下，请求头凭据（`Authorization: Bearer`、`X-API-Key`）或会话 Cookie 任一匹配即放行。
   - `ControlPlaneHTTPMiddleware` 保留路径豁免与匿名 GET 判断，其余交给 `authorizeControlPlane`，按哨兵错误写出与今天相同的三种 401 JSON 正文和同一条 `slog.Error` 日志。
   - `HandleChatWS` 开头改为调用同一个 `authorizeControlPlane`（失败时 `http.Error(w, "Unauthorized", 401)`），删除 `controlPlaneToken()` 这套独立实现。
   - **删除 URL 查询串 `?token=` 凭据**：`ControlPlaneTokenFromRequest` 与 `ControlPlaneAuthorized` 去掉 `queryToken` 参数。唯一用它的是浏览器 WebSocket，改用 Cookie 后就没有使用者了；
     URL 里的 token 会进入访问日志与浏览器历史。
   - `POST /api/auth/session` 必须免鉴权：在 `ControlPlaneHTTPMiddleware` 的豁免判断里加一条"方法为 POST 且路径为 `/api/auth/session`"（写在 `pkg/gateway/controlplane.go`，不要放进 `pkg/config/gateway.go`——它是 gateway 自己的路由，不是配置语义）。
4. **WebSocket 来源校验**：删除 `server.go:586-588` 里 `CheckOrigin` 的覆盖，使用 gorilla/websocket 的默认规则
   （存在 `Origin` 头且其主机与 `Host` 不同则拒绝）。改用 Cookie 后，跨站页面发起的 WebSocket 握手不能被放行。

### 前端

1. **新文件 `frontend/src/lib/gatewaySession.ts`**：
   - `export type GatewaySessionState = 'ok' | 'required'`；
   - `export function probeGatewaySession(): Promise<GatewaySessionState>`：用原生 `fetch('/api/auth/session')` 探测一次并缓存 Promise（不用 axios，避免被 401 拦截器循环处理）；
     状态码 401 → `'required'`，其他任何结果 → `'ok'`（服务不可达等问题由各页面自己的加载错误呈现，守卫只负责"需要登录"这一件事）；
   - `export function markGatewaySessionOk()`、`export function onGatewayUnauthorized(handler)`、`export function reportGatewayUnauthorized()`：
     `api.ts` 在收到 401 时调用 `reportGatewayUnauthorized()`，由路由注册的处理函数跳转登录页。这样 `api.ts` 不需要 import 路由，避免循环依赖。
   - `export function signInToGateway(token: string): Promise<void>`：原生 `fetch` POST `/api/auth/session`；非 2xx 时抛出 `Error`，消息取响应体 `error` 字段原文。
   - `export function tokenFromLocationHash(hash: string): string`：从 `#token=<值>` 中取出并 `decodeURIComponent`，没有则返回空串。
2. **`frontend/src/lib/api.ts`**：
   - 删除 `AuthUser`、`authUser`、`authToken`、`getUser/setUser/getToken/setToken`、请求拦截器（`:839-846`）、`authLogin/authRegister/authCurrent/authLogout`；
   - axios 响应拦截器增加错误分支：`error.response?.status === 401` 时调用 `reportGatewayUnauthorized()`，然后照常 `Promise.reject(error)`；
   - 新增 `function gatewayFetch(input: string, init?: RequestInit): Promise<Response>`：调用原生 `fetch`，响应为 401 时调用 `reportGatewayUnauthorized()`，原样返回响应。
     把 6 处裸 `fetch`（含 `filesUpload`）改为 `gatewayFetch`，并删除 `filesUpload` 里手工拼 `Authorization` 的代码；
   - `getErrorMessage`：在读取 `message` 之后，增加对 `error` 字段的读取（`'error' in data` 时返回其字符串）。
3. **`frontend/src/composables/useAuth.ts` → 重命名为 `frontend/src/composables/useLastSession.ts`**：只保留 `lastSessionId`、`persistLastSessionId`、`lastSessionIdValue`，
   导出 `useLastSession()` 返回 `{ lastSessionId }`；删除匿名 clientId 及登录相关代码。更新 4 个导入方。
4. **WebSocket URL**：`buildBrowserForebrainGatewayChatWsUrl()` 去掉 `token` 参数；`useChatStream.ts` 两处调用去掉 `getToken()`。
5. **删除死代码**：`forebrainGatewayRuntime.ts` 里 `DEFAULT_BOT_MENTIONS`（若再无使用）、`parseBotMentionsFromEnv`、`getForebrainGatewayRuntimeConfig`、`buildForebrainGatewayWsUrl`、
   `textTriggersBotMention`、`primaryBotMentionHint`、`getForebrainAdminBaseUrl` 及其测试；`vite-env.d.ts` 中不再被读取的 `VITE_FOREBRAIN_*` 声明。
6. **登录页 `frontend/src/views/LoginView.vue`**，路由 `{ path: '/login', name: 'login', component: ..., meta: { bare: true, title: () => t('routes.loginTitle') } }`：
   - 页面样式以预览页 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/login-preview.html` 为准：白底，右上角一个语言切换按钮（复用 `useI18n()` 的 `languageOptions` / `setLocale`，登录页没有顶栏，这是唯一的语言入口）；
     居中一张 1px 描边、最大宽 400px 的卡片：品牌标识（**直接使用方印 mark**——内联 SVG，色值用预览页的海军蓝品牌色；owner 2026-09-30 指示 "login page 直接使用方印 mark"，计划 004 只需统一 favicon 与菜单栏）与字标、标题"登录"、一句说明、一个 `type="password"`、`id="gateway-token"`、`autocomplete="off"` 的输入框、提示、提交按钮（输入为空时禁用）；
     页脚一行"已连接 `<location.host>` · `<__FOREBRAIN_VERSION__>`"，同一台机器开了多个 gateway 时能分清登录的是哪一个；手机宽度下卡片去掉描边、贴边显示；
   - 说明文案告诉用户 token 在哪里：`~/.forebrain/.env` 中的 `FOREBRAIN_GATEWAY_TOKEN`，或 `forebrain gateway start` 输出的登录链接（计划 002）；
   - `onMounted`：若 `tokenFromLocationHash(location.hash)` 非空，先 `history.replaceState(history.state, '', location.pathname + location.search)` 抹掉哈希，再自动提交；
     否则若 `probeGatewaySession()` 为 `'ok'`，直接 `router.replace(redirect || '/')`；
   - 提交成功：`markGatewaySessionOk()`，`router.replace(route.query.redirect || '/')`；失败：在输入框下方显示服务端返回的原文（例如 `invalid gateway token`），一句话，不加额外解释；
   - 通过登录链接进入时（哈希里带 token），卡片只显示品牌区与"正在通过登录链接登录…"一行加载提示，不显示表单；
   - 所有文案进 `locales/index.ts` 的 `zh` 与 `en`：`routes.loginTitle`、`login.title`、`login.description`、`login.tokenLabel`、`login.tokenPlaceholder`、`login.tokenHint`、`login.submit`、`login.submitting`、`login.viaLink`、`login.viaLinkHint`、`login.connectedTo`。
7. **路由守卫**（`frontend/src/router/index.ts`）：
   ```ts
   router.beforeEach(async (to) => {
     if (to.name === 'login') return true
     if ((await probeGatewaySession()) === 'required') {
       return { name: 'login', query: { redirect: to.fullPath } }
     }
     return true
   })
   onGatewayUnauthorized(() => {
     const current = router.currentRoute.value
     if (current.name !== 'login') void router.replace({ name: 'login', query: { redirect: current.fullPath } })
   })
   ```
8. **`frontend/src/main.ts`**：`app.use(router)` 之后改为 `router.isReady().then(() => app.mount('#app'))`，保证鉴权结论先于外壳挂载。
9. **`frontend/src/App.vue`**：模板根改为 `<router-view v-if="route.meta.bare" />` 与 `<div v-else class="forebrain-app-shell">…</div>`；
   `onMounted` 里的 `void loadPrimaryAgents()` 改为 `watch(() => route.meta.bare, (bare) => { if (!bare) void loadPrimaryAgents() }, { immediate: true })`，
   登录页不发任何业务请求。
10. **`frontend/vite.config.ts`**：删除 `:53-63` 的 `/forebrain/api/v1` 旧规则；`/ws` 代理增加
    `configure: (proxy) => { proxy.on('proxyReqWs', (proxyReq) => proxyReq.setHeader('origin', 'http://127.0.0.1:6060')) }`，
    让开发服务器转发的握手通过同源校验；`test` 块增加 `exclude: [...configDefaults.exclude, 'e2e/**']`（`import { configDefaults } from 'vitest/config'`），避免 vitest 去跑 Playwright 用例。

### 自动化真机验证脚手架（后续计划都往里加用例）

- `frontend/package.json`：`devDependencies` 增加 `"@playwright/test": "1.59.1"`（与仓库锁文件里已有的 playwright 版本一致）；`scripts` 增加 `"e2e": "playwright test -c e2e/playwright.config.ts"`。
- `frontend/e2e/playwright.config.ts`：`testDir: '.'`、`workers: 1`、`timeout: 60_000`、`reporter: [['list']]`；
  `use: { baseURL: process.env.E2E_BASE_URL, channel: process.env.E2E_BROWSER_CHANNEL ?? 'chrome', viewport: { width: 1440, height: 900 }, trace: 'retain-on-failure' }`；
  `outputDir: process.env.E2E_SHOTS + '/artifacts'`。默认使用本机安装的 Google Chrome；没有 Chrome 的机器设 `E2E_BROWSER_CHANNEL=chromium` 并先运行 `corepack pnpm exec playwright install chromium`。
- `frontend/e2e/support.ts`：`signIn(page)`（打开 `/login`，填入 `process.env.E2E_TOKEN`，提交并等待离开登录页）、`shot(page, name)`（保存到 `${E2E_SHOTS}/${name}.png`，整页）、
  `watch401(page)`（收集所有状态为 401 的 `/api/` 响应，返回数组供断言）、
  `expectAssistantReply(page)`（等待对话区出现 assistant 消息且文本非空；`process.env.E2E_REAL_LLM === '1'` 时到此为止——真实模型回复内容不确定，只断言非空；否则额外断言文本为假模型的精确回复 `E2E_REPLY_OK`）。
- `scripts/acceptance/web_e2e.sh`（可执行，风格仿照 `scripts/acceptance/driver.sh`），按顺序：
  1. 变量：`WORK=${FOREBRAIN_E2E_WORK:-${TMPDIR:-/tmp}/forebrain-web-e2e}`、`GW_PORT=${FOREBRAIN_E2E_GW_PORT:-8761}`、`LLM_PORT=${FOREBRAIN_E2E_LLM_PORT:-8762}`；
     清空并重建 `$WORK/home`、`$WORK/proj`、`$WORK/webui`、`$WORK/shots`；
  2. 构建：`CGO_ENABLED=1 go build -tags fts5 -o "$WORK/forebrain" ./cmd/forebrain`，`scripts/install-dictionary.sh "$WORK"`；
     `(cd frontend && corepack pnpm build --outDir "$WORK/webui" --emptyOutDir)`——**不写 `pkg/gateway/dist`**；
  3. 生成本次运行的 token：`TOKEN=$(openssl rand -hex 32)`，写入 `$WORK/home/.env`（`FOREBRAIN_GATEWAY_TOKEN=$TOKEN`，`chmod 600`）；
  4. 写 `$WORK/home/forebrain.yaml`：`gateway.http_addr: 127.0.0.1:$GW_PORT`、`gateway.auth.mode: token`、`gateway.auth.token: "${FOREBRAIN_GATEWAY_TOKEN}"`；
     `agents` 段照抄 `driver.sh:53-65` 的写法（`deepseek` / `deepseek-chat`、`api_key: ${FAKE_LLM_KEY}`、`base_url: http://127.0.0.1:$LLM_PORT`）。
     **真实模型档（`FOREBRAIN_E2E_REAL_LLM=1` 时替代上面的 agents 段，gateway/auth 部分不变）**：
     `set -a; . ~/.forebrain/e2e-zhipu.env; set +a` 后，agents 段改写为智谱真实配置——
     ```yaml
     agents:
       definitions:
         main:
           primary: true
           enable_subagent: false
           llm_providers:
           - provider: zhipuai
             model: glm-5.3-flash
             api_key: ${FOREBRAIN_E2E_ZHIPU_KEY}
             base_url: https://open.bigmodel.cn/api/coding/paas/v4
     ```
     `~/.forebrain/e2e-zhipu.env` 是 owner 私有文件（`chmod 600`，含 `FOREBRAIN_E2E_ZHIPU_KEY`，不进仓库）；yaml 里的 api_key 必须走 `${ENV}` 引用——明文密钥会被 config 加载守卫直接拒绝启动，这是既有行为。真实档下不再启动假模型进程；`E2E_REAL_LLM=1` 透传给 Playwright，spec 内依赖精确回复文本的断言据此放宽（见 `support.ts`）。
  5. `git init` 一个 `$WORK/proj` 作为启动目录；
  6. 启动假模型：`python3 scripts/acceptance/fake_provider.py reply "$LLM_PORT" E2E_REPLY_OK` 后台运行，等日志出现 `LISTENING`；真实模型档（`FOREBRAIN_E2E_REAL_LLM=1`）跳过本步；
  7. 在 `$WORK/proj` 里后台启动 `FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME="$WORK/home" FOREBRAIN_STATIC_DIST="$WORK/webui" "$WORK/forebrain" gateway start`，
     stdout 写 `$WORK/gateway.out`，stderr 写 `$WORK/gateway.err`；`trap` 在退出时结束两个后台进程；
  8. 轮询 `curl -fsS http://127.0.0.1:$GW_PORT/healthz`，最多 30 秒；
  9. `(cd frontend && E2E_BASE_URL=http://127.0.0.1:$GW_PORT E2E_TOKEN=$TOKEN E2E_GATEWAY_OUT=$WORK/gateway.out E2E_SHOTS=$WORK/shots corepack pnpm e2e)`；
  10. 命令行客户端回归：`FOREBRAIN_HOME="$WORK/home" "$WORK/forebrain" gateway status` 的输出必须包含 `status=200`（CLI 仍用请求头 token）；
  11. 打印 `screenshots: $WORK/shots`，最后一行打印 `web e2e: PASS`。任何一步失败都以非零退出，并把 `gateway.err` 末尾 50 行打到 stderr。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/config/... -count=1` | 全部 `ok` |
| Go 静态检查 | `go vet ./pkg/gateway/... ./cmd/forebrain/...` | 退出码 0 |
| 前端依赖 | `cd frontend && corepack pnpm install` | 退出码 0 |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 范围

**只允许修改**：
- `pkg/gateway/controlplane.go`、`pkg/gateway/controlplane_test.go`、`pkg/gateway/server.go`、`pkg/gateway/server_test.go`（如需）、`pkg/gateway/api_extra.go`
- `pkg/gateway/web_session.go`（新建）、`pkg/gateway/web_session_test.go`（新建）
- `frontend/src/lib/api.ts`、`frontend/src/lib/api.test.ts`、`frontend/src/lib/gatewaySession.ts`（新建）、`frontend/src/lib/gatewaySession.test.ts`（新建）
- `frontend/src/lib/forebrainGatewayRuntime.ts`、`frontend/src/lib/forebrainGatewayRuntime.test.ts`、`frontend/src/vite-env.d.ts`
- `frontend/src/composables/useAuth.ts` → `frontend/src/composables/useLastSession.ts`（重命名），及其 4 个导入方：`useChatStream.ts`、`usePrimaryAgents.ts`、`views/McpView.vue`、`views/ChatView.vue`
- `frontend/src/composables/useChatStream.ts`（两处 WebSocket URL）、`frontend/src/views/LoginView.vue`（新建）、`frontend/src/router/index.ts`、`frontend/src/main.ts`、`frontend/src/App.vue`、`frontend/src/locales/index.ts`
- `frontend/vite.config.ts`、`frontend/package.json`、`frontend/pnpm-lock.yaml`
- `frontend/e2e/playwright.config.ts`、`frontend/e2e/support.ts`、`frontend/e2e/auth.spec.ts`（均新建）、`scripts/acceptance/web_e2e.sh`（新建）

**不要碰**：
- `pkg/gateway/dist/**`：发版流程管理的构建产物。
- `pkg/config/gateway.go` 的豁免逻辑：登录接口的豁免属于 gateway 路由，写在 `controlplane.go`。
- `pkg/home/seed.go`：token 的生成与存放方式是对的，本计划不改。
- 任何提示词组装相关包（见 README 全局规则 4）。
- App.vue 的布局与样式：布局在计划 005 重做，本计划只加 `bare` 分支和加载时机。

## 步骤

### 第 1 步：先写失败的真机用例，复现 401

建立脚手架（上文"自动化真机验证脚手架"全部文件），并写 `frontend/e2e/auth.spec.ts` 的第一个用例 `web app reaches the api without 401`：
`signIn` 之前先直接访问 `/providers`、`/hooks`、`/memories`，用 `watch401` 收集 401。此时登录页还不存在，`signIn` 会失败——这是预期的。

**验证**：`scripts/acceptance/web_e2e.sh` → 以非零退出，且失败信息指向登录页或 401（证明脚手架能启动真实 gateway 并复现问题）。
若脚手架本身跑不起来（gateway 启动失败、Chrome 启动失败），按 STOP 条件处理。

### 第 2 步：服务端统一鉴权规则并支持会话 Cookie

实现"设计 / 服务端"第 1、3、4 条：`authorizeControlPlane`、哨兵错误、去掉 `queryToken`、`HandleChatWS` 改用同一规则、删除 `CheckOrigin` 覆盖、删除 `controlPlaneToken()`。
同步修改 `controlplane_test.go`：删除依赖查询串 token 的用例（`query token when headers empty`、`blank bearer falls through to query` 等），其余用例改为新签名。新增：

- `TestControlPlaneHTTPMiddlewareAcceptsWebSessionCookie`：带正确名称与值的 Cookie → 204；
- `TestControlPlaneHTTPMiddlewareRejectsForgedWebSessionCookie`：名称正确、值错误 → 401；
- `TestControlPlaneHTTPMiddlewareIgnoresQueryToken`：只在 `?token=` 里给出正确 token → 401；
- `TestHandleChatWSFollowsAuthModeNone`：`mode: none` 且配置了 token，无凭据请求 `/ws/chat` → 状态码不是 401；
- `TestHandleChatWSRejectsCrossOriginHandshake`：用 `httptest.NewServer` 挂 `HandleChatWS`，带正确 Cookie、`Origin: http://evil.example` 的 gorilla 客户端握手 → 失败且响应码 403。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'ControlPlane|HandleChatWS' -count=1` → `ok`。

### 第 3 步：登录接口

实现"设计 / 服务端"第 2 条与登录接口的豁免；在 `AttachExtraRoutes` 的 `api` 分组里注册 `api.Post("/auth/session", ...)` 与 `api.Get("/auth/session", ...)`。
新增 `web_session_test.go`：

- 正确 token → 204，`Set-Cookie` 名称为 `webSessionCookieName(token)`，值为 `webSessionValue(token)`，含 `HttpOnly`、`SameSite=Strict`、`Path=/`、`Max-Age=2592000`，不含 `Secure`；
- 同上但请求带 `X-Forwarded-Proto: https` → Cookie 含 `Secure`；
- 错误 token → 401，正文 `{"error":"invalid gateway token"}`，无 `Set-Cookie`；
- `mode: none` → 204，无 `Set-Cookie`；
- 请求体超过 4096 字节 → 400；
- 通过 `ServeHTTPChain` 走完整链路：未带凭据 `POST /api/auth/session` 不被中间件拦截；`GET /api/auth/session` 未带凭据 → 401，带 Cookie → 200 且正文 `{"auth_mode":"token"}`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` → `ok`；`go vet ./pkg/gateway/...` → 退出码 0。

### 第 4 步：前端去掉假登录与 token 传递

实现"设计 / 前端"第 1–5 条。`api.test.ts` 与 `forebrainGatewayRuntime.test.ts` 中针对被删函数的用例一并删除；
新增 `gatewaySession.test.ts`：`tokenFromLocationHash('#token=abc%2F1')` 返回 `abc/1`，`tokenFromLocationHash('')` 与 `('#other=1')` 返回空串；
`getErrorMessage` 对 `{ response: { data: { error: 'invalid gateway token' } } }` 返回原文。

**验证**：
- `cd frontend && corepack pnpm test` → 全部通过；
- `grep -rn "getToken\|setToken\|authLogin\|VITE_FOREBRAIN_GATEWAY_TOKEN\|searchParams.set('token'" frontend/src` → 无输出；
- `grep -rn "useAuth" frontend/src` → 无输出。

### 第 5 步：登录页、路由守卫与外壳

实现"设计 / 前端"第 6–10 条。

**验证**：`cd frontend && corepack pnpm test` → 全部通过；`cd frontend && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` → 退出码 0。

### 第 6 步：补全真机用例并跑通

`frontend/e2e/auth.spec.ts` 最终包含以下用例，每个用例结束时 `shot()` 一张截图：

1. `unauthenticated visit lands on the sign-in page`：新上下文访问 `/` → URL 变为 `/login?redirect=%2F`；整个过程中除 `GET /api/auth/session` 外没有其他 401；页脚包含 `127.0.0.1:${GW_PORT}`；
   输入框为空时提交按钮禁用；点击右上角语言按钮切到 English 后标题变为 `Sign in`。
2. `wrong token is refused with the server's message`：输入 `not-the-token` 提交 → 页面出现 `invalid gateway token`，仍在 `/login`。
3. `sign-in unlocks every page without 401`：`signIn` 后依次访问 `/`、`/agents`、`/projects`、`/cron`、`/channels`、`/permissions`、`/tools`、`/mcp`、`/providers`、`/hooks`、`/memories`、`/config`、`/settings`，
   每页等待 `networkidle`；`watch401` 结果为空数组；并断言 `GET /api/providers`、`GET /api/hooks`、`GET /api/memories/settings` 各至少出现一次且状态 200（即 owner 报错的三个接口）。
4. `session survives a reload and stays out of script reach`：登录后 `page.reload()` 仍在 `/`；`context.cookies()` 中以 `forebrain_session_` 开头的 Cookie `httpOnly === true`、`sameSite === 'Strict'`；
   `page.evaluate(() => document.cookie)` 不包含 `forebrain_session_`。
5. `one-click link signs in and scrubs the token from the address bar`：新上下文访问 `/login#token=${E2E_TOKEN}` → 落到 `/`，`page.url()` 不含 `token`。
6. `chat streams over the cookie-authenticated websocket`：登录后在对话输入框输入 `ping` 并发送 → `expectAssistantReply` 通过（假模型档即页面出现 `E2E_REPLY_OK`；真实模型档为收到非空 assistant 回复）。
7. `uploaded image renders through the cookie`：在对话输入框的文件选择 `input[type=file]` 上 `setInputFiles` 一个内存中的 1×1 PNG
   （base64 `iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==`，文件名 `dot.png`）并发送 →
   用户消息里的 `img[src*="/api/files/"]` 的 `naturalWidth > 0`。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`；截图目录里有 7 张以上 PNG。

## 测试计划

- Go：第 2、3 步列出的用例，结构参照 `pkg/gateway/controlplane_test.go:95-121`（构造 `process.Environment{Deps: run.Deps{AppCfg: ...}}` 包一层中间件）。
- 前端单测：`gatewaySession.test.ts`，结构参照 `frontend/src/lib/api.test.ts`。
- 真机：`frontend/e2e/auth.spec.ts` 的 7 个用例，由 `scripts/acceptance/web_e2e.sh` 驱动。

## 完成标准（全部满足）

- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/config/... ./cmd/forebrain/... -count=1` 全部 `ok`
- [x] `go vet ./...` 退出码 0
- [x] `cd frontend && corepack pnpm test` 全部通过
- [x] `scripts/acceptance/web_e2e.sh` 最后一行 `web e2e: PASS`（7/7 用例，全程 18s）
- [x] `grep -rn 'Query().Get("token")' pkg/gateway` 无输出
- [x] `grep -rn "CheckOrigin" pkg/gateway --include='*.go' | grep -v _test` 无输出
- [x] `grep -rn "getToken\|setToken\|authLogin\|VITE_FOREBRAIN_GATEWAY_TOKEN\|useAuth\|8216\|18789" frontend/src frontend/vite.config.ts` 无输出
- [x] `git status --short pkg/gateway/dist` 无输出
- [x] README 全局规则 4 的提示缓存检查命令无输出
- [x] `git status --short` 中只出现"范围"列出的文件（两处计划清单遗漏的必要连带：`views/MemoriesView.vue` 也是 `getUser` 导入方；`lib/forebrainGatewaySessionSocket.ts` 持有 `getToken` option——两者同属"删除 token 传递"同一根因的修改）
- [x] README 状态行已更新

## STOP 条件

- "现状"里的代码摘录与当前代码对不上。
- 第 1 步时 `forebrain gateway start` 在隔离 home 下无法启动（例如要求交互式初始化），或 Chrome 无法由 Playwright 启动。
- 发现除浏览器 WebSocket 之外还有任何代码或文档依赖 `?token=` 查询串凭据（例如某个渠道适配器、外部脚本）。
- 发现 `/ws/chat` 之外还有别的 WebSocket 端点依赖 `CheckOrigin: true`。
- 某一步的验证在合理修正后连续两次失败。
- 需要修改"范围"之外的文件。

## 维护说明

- 以后新增任何浏览器直接访问的资源 URL（下载、预览、导出），都不需要处理凭据——同源请求自动携带会话 Cookie；**不要**再给前端引入 token。
- 轮换 gateway token（改 `~/.forebrain/.env` 并重启）会让所有浏览器会话失效，用户会被带回登录页，这是预期行为。
- 审阅重点：`authorizeControlPlane` 是否是 HTTP 与 WebSocket 唯一的判定入口；Cookie 值比较是否使用常量时间比较；登录接口是否只豁免 `POST`。
- 计划 002 会在启动输出里打印登录地址；计划 004 会替换登录页上的品牌标识。
