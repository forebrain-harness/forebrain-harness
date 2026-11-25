# Plan 376: VS Code 扩展——右侧面板内嵌 Forebrain 全功能 Web UI（扩展宿主回环中继鉴权）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)** — both must hold, or STOP:
>
> ```bash
> git status --porcelain -- editors scripts/build-vscode-forebrain-ext.sh .gitignore   # 无输出
> test ! -e editors && echo "editors/ absent"                                             # 必须打印 editors/ absent
> git diff 3a261297 -- .gitignore scripts/build-vscode-deliverable-ext.sh \
>   pkg/gateway/controlplane.go pkg/gateway/serve_run.go pkg/gateway/http_server.go \
>   pkg/process/gateway_token.go cmd/forebrain/serve.go \
>   frontend/src/lib/gatewaySession.ts frontend/src/router/index.ts frontend/src/views/LoginView.vue   # 无输出
> ```
>
> 本计划盖章于 `3a261297`（dev，2026-11-25）。第一组命令查**未提交**改动（基线==HEAD 时
> `git diff <sha>..HEAD` 恒为空，发现不了未提交改动，所以并用 `git status --porcelain`）；
> 第二组查本计划引用的 harness 文件是否在盖章后变过。若 `editors/` 已存在内容，视为漂移，STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED
- **Depends on**: none（harness 零源码依赖：不改 `pkg/`、`frontend/`、`npm/`、`cmd/`、`pkg/tui/vscode_ext/`）
- **Category**: direction（编辑器集成新功能）
- **Planned at**: commit `3a261297`, 2026-11-25
- **Status**: TODO
- **后续**: [379-vscode-native-integration](379-vscode-native-integration.md)（依赖本计划：宿主 WS 镜像驱动的 `session_todo` 清单树／状态栏／审批通知、选区与文件送会话、交付物在 VS Code 打开）；原生 Chat 参与者 `@Forebrain` 再后置为 380（未起草）。

## Why this matters

owner 原始需求：**在 VS Code 里加上 forebrain harness 的对话窗口，不碰 VS Code 源码，做成可安装的
VS Code 扩展，体验类 Cursor 且更顺，并且 harness 的全部特性（高级工具、内置工作流、子代理之间／
父子代理之间／跨会话跨进程的代理通信、plan 模式、`user_interaction`、`session_todo` 进度清单等）
都要能在 VS Code 里丝滑使用。** 这条需求排除了「在 webview 里自建一个精简聊天 UI」——任何自建 UI
都追不平网关自带 Web UI 的全部特性。唯一的做法是把**网关自带的 Web UI 原样嵌进面板**。

本计划交付 376 的核心：一个独立包 `editors/vscode/`（TypeScript 扩展），在 VS Code 右侧
（Secondary Side Bar / auxiliary bar）贡献 `Forebrain › Chat` 视图，视图里是一个占满的 iframe，
加载的是网关的 Web UI——**经扩展宿主内的一个回环中继**，由中继替页面向网关出示 Bearer token。
owner 2026-11-24 四项裁决（均取推荐项）：鉴权架构＝扩展宿主回环中继；拆分＝376 核心 + 379 原生集成，
参与者后置；harness 侧零改动。

## 为什么不是「iframe 直连网关」（旧稿的承重前提已被证伪）

旧稿（2026-11-24 第一版，基线 `db4f3f4a`）的方案是「iframe src 指向 `http://127.0.0.1:6060/login#token=<token>`
即可零前端改动自动登录」。**这在 VS Code webview 里不成立**，已用真实 Chromium 复现（附录 A 给了可重跑脚本）：

| 情形 | 结果 |
|---|---|
| 顶层直接打开 `/login#token=x`（对照） | `POST /api/auth/session → 204`，随后 `GET /api/probe → cookie=present` |
| 同站嵌入（`127.0.0.1:A` 页面嵌 `127.0.0.1:B`） | `cookie=present`（站点＝协议＋主机，不看端口） |
| **跨站嵌入**（`localhost:A` 页面嵌 `127.0.0.1:B`，等价于 VS Code webview） | `POST → 204` 但 **`GET /api/probe → cookie=absent`** |

原因链：网关登录 cookie 是 `SameSite=Strict`（`pkg/gateway/controlplane.go:530-540`）；前端只靠 cookie
（`frontend/src/lib/gatewaySession.ts:1-6`「Nothing here ever holds the token」）；VS Code webview 的顶层源是
`vscode-file://`／`vscode-webview://`，其内嵌的 `127.0.0.1` iframe 是**跨站框架**，Chromium 对
`SameSite=Strict` cookie 在跨站上下文里既不存也不带 ⇒ 前端每个 API 都 401 ⇒ 路由守卫把页面打回 `/login`
⇒ 死循环。

旧稿另一处判断也错了：它说「webview 必发 `vscode-webview://` Origin，所以页面直连 `/ws/chat` 在协议层不可能」。
实际上 iframe 文档的源是网关自己的源，它发起的 WS 握手 `Origin == Host`，gorilla 默认 CheckOrigin 会放行。
**真正的障碍只有 cookie。**

所以本计划的解法是：不让页面依赖 cookie。iframe 从扩展宿主的中继加载 Web UI，页面发的每个请求由中继
加上 `Authorization: Bearer <token>` 转给网关；网关对头部凭据的判定本来就不看 Origin
（`controlplane.go:80-95`：`ControlPlaneAuthorized(r, expected)` 通过即 `return "", nil`）。
harness 一行不改，token 仍然**永不进入页面**（网关既有裁决：`handleWebSessionCreate` 注释「The token itself
never becomes a browser-readable value」，且 `authorizeControlPlane` 注释明令「Query strings never carry
credentials」）。

## Current state（全部为 advisor 在 `3a261297` 实读核实的事实）

**网关认证（本计划的承重墙）**
- 机器客户端凭据只认头部：`Authorization: Bearer <token>` 或 `X-API-Key: <token>`
  （`pkg/gateway/controlplane.go:34-47`）；常量时间比较（`:49-59`）。
- 唯一判定点（`controlplane.go:80-95`）：
  ```go
  func authorizeControlPlane(r *http.Request, cfg *appcfg.Root) (string, error) {
      expected := strings.TrimSpace(cfg.Gateway.Auth.Token)
      if expected == "" { return "", errGatewayTokenMissing }
      if ControlPlaneAuthorized(r, expected) { return "", nil }          // 头部凭据：通过，不查 Origin
      if webSessionCookieValid(r, expected) {
          if !requestOriginMatchesHost(r) { return "", errGatewayCrossOrigin }
          return cookieSessionOrigin(r), nil
      }
      return "", errGatewayUnauthorized
  }
  ```
- 登录 cookie（`controlplane.go:530-540`）：`HttpOnly: true, SameSite: http.SameSiteStrictMode,
  Secure: requestIsHTTPS(r)`，`MaxAge: 2592000`，名字由 token 派生（`forebrain_session_<8hex>`）。
- 前端探活：`GET /api/auth/session`（`frontend/src/lib/gatewaySession.ts:21-27`，401 → `'required'`）；
  路由守卫（`frontend/src/router/index.ts:130-136`）据此重定向 `/login`。带 Bearer 时中间件放行，
  handler 回 `200 {"authenticated":true}`（`controlplane.go:593-596`）⇒ **经中继的页面不会看到登录页**。
- `GET /health|/healthz|/readyz` 免认证（`pkg/gateway/server.go:809-811`）。
- WS 握手：`var upgrader = websocket.Upgrader{}`（`server.go:829`），gorilla 默认 CheckOrigin——
  **不带 Origin 头的握手通过**（中继转发握手时丢掉 Origin）。协议版本常量 `wsProtocolVersion = "2026-04-18"`
  （`ws_protocol.go:13`）——**376 不需要扩展自己说 WS**（页面的 WS 经中继透明转发），379 才需要。
- 已凭据的请求跳过 IP 限流（`controlplane.go:428-445` `ServeHTTPChain`：`!requestAuthorized(env, r) && !rl.allow(...)`）。

**token 从哪来（token 梯子的依据）**
- `forebrain gateway start` → `runGatewayE`（`cmd/forebrain/serve.go:57-90`）→ `ensureGatewayToken` →
  `EnsureGatewayToken`（`pkg/process/gateway_token.go:29-45`）：**配置里有 token 就不生成、不打印**；
  没有才生成一个「仅本进程生命周期」的 token，且**不写任何文件**，只在 stdout 的 banner 里出现。
- banner（`pkg/gateway/serve_run.go:339-368` `writeStartupBanner`）逐行形状：
  ```
  Web UI       http://127.0.0.1:6060/
  Sign in      http://127.0.0.1:6060/login#token=<url-escaped token>   ← 仅生成的 token 才有
  Token        <token>                                                    ← 仅生成的 token 才有
  Listening on http://127.0.0.1:6060
  ```
  配置了 token 时后两行换成 `Auth         token`。`Listening on` 在端口绑定后打印。
- 网关响应 `SIGTERM`/`SIGINT`（`pkg/gateway/http_server.go:257` `signal.Notify(c, os.Interrupt, syscall.SIGTERM)`）。
- 默认监听 `127.0.0.1:6060`；`forebrain gateway stop` 走的 `/admin/shutdown` **服务端没有 handler**
  （全仓只有客户端 `http_server.go:670`）⇒ 扩展不得依赖 stop，只能杀**自己 spawn** 的子进程。
- `gateway.auth.token` 支持写成 `${NAME}` 引用由环境变量供给 ⇒ 扩展宿主环境里的
  `FOREBRAIN_GATEWAY_TOKEN` 是梯子第 2 级（best effort：以探活为准）。

**仓内先例与仓规约**
- 迷你扩展先例：`pkg/tui/vscode_ext/package.json`（`publisher: "forebrain"`、`engines.vscode: "^1.85.0"`）与
  打包脚本 `scripts/build-vscode-deliverable-ext.sh`（在 mktemp 目录外构建、`npx --yes @vscode/vsce@2.32.0 package
  --allow-missing-repository --out …`、再 `mv` 回）。本脚本是它的同构仿写。
- `.gitignore`：`node_modules`（第 31 行）与 `dist`（第 32 行）在任意深度生效——`editors/vscode/node_modules/` 与
  `editors/vscode/dist/` 无需再加规则（`git check-ignore -v` 可证）。仓库根 `LICENSE` 是 Apache-2.0。
- npm 包名 `@forebrain-harness/forebrain`（`npm/package.json:2`）——README 里「安装 CLI」指向它。
- Go 架构守卫 `pkg/architecture` 只数 `pkg/` 下的 Go 包；顶层 TS 包不触墙。
- **提交规则**：本仓现行 owner 常设裁决「禁止提交任何代码，用户手动提交」。执行器**不提交**，改动留在
  worktree 里等评审（见 Git workflow）。

**本机工具链（advisor 实测，2026-11-25）**
- `code --version` → `1.147.0`；`node --version` → `v22.22.1`；`npm --version` → `10.9.4`；
  `forebrain` 在 PATH 上（`~/.nvm/versions/node/v22.22.1/bin/forebrain`）。
- 下文全部依赖版本与命令在 `/tmp` 的一次性 spike 里**实际跑通过**：`npm install`（8 个包）、esbuild 打包、
  `tsc --noEmit` exit 0、`node --test` 10/10 PASS；中继在真实 Chromium 里对一个**替身网关**（只认 Bearer 的回环服务）
  端到端打通（跨站页面嵌入 `http://fb-<nonce>.localhost:<port>/login`，页面自己的 `fetch('/api/probe')` 返回
  `probe=200 auth=bearer`）。这证明的是中继机制与 Chromium 的行为，不是真实 Web UI——**真实 Web UI 在真实 VS Code
  webview 里的首次验证属于 Step 9 的 owner 验收**。Step 2 的 `relay.ts` 与 `relay.test.ts` 就是那次 spike 的逐字源码。

**必须避开的两个坑（spike 踩出来的）**
1. `node --test dist-test/`（目录形式）在 Node 22 下报 `Cannot find module '…/dist-test'`——必须用 glob：
   `node --test 'dist-test/*.test.js'`（引号保留，让 node 自己展开）。
2. esbuild `--format=cjs` 不允许 top-level await——入口用 async IIFE。

## Architecture（契约，执行器不得偏移）

```
VS Code (desktop, local window)
┌──────────────────────────────────────────────────────────────────────┐
│ Secondary Side Bar › Forebrain › Chat  (WebviewView, iframe 占满)     │
│   iframe src = http://fb-<nonce>.localhost:<port>/?boot=<boot>         │
│        │  页面自己的 fetch / WS（同源，无 cookie，页面里没有 token）      │
│        ▼                                                               │
│   扩展宿主 (Node) ── relay.ts：127.0.0.1:<port>，只认 Host               │
│        │  + Authorization: Bearer <token>；剥 Cookie/Origin/Referer     │
│        ▼                                                               │
│   网关 http://127.0.0.1:6060  （harness 零改动；头部凭据免 Origin 检查） │
│                                                                          │
│   另：gatewayProcess.ts 可选 spawn `forebrain gateway start`（只杀自己起的）│
└──────────────────────────────────────────────────────────────────────┘
```

**安全模型（评审重点）**
- 中继的 origin 是第一道能力凭证：主机名带 128 位随机 nonce（`fb-<32 hex>.localhost`），**只有 Host 头逐字等于
  `fb-<nonce>.localhost:<port>` 的请求才被受理**。没见过 iframe URL 的页面/进程用不了中继，也就借不到 token。
  Chromium 把 `*.localhost` 解析到回环（已实测），无需 DNS。**威胁模型边界**：这道凭证挡的是浏览器里的其他网站
  （它们伪造不了 Host）；同一用户的本地进程若从 Output 学到 origin，可以不带 Fetch Metadata 直接用它（`admitted`
  对无 `sec-fetch-site` 头的请求只查 Host）——这与「本机同用户进程本就能读 forebrain.yaml 与进程环境」的信任边界一致，
  不是本计划新增的攻击面。boot 秘密挡的是导航/嵌入（点击劫持），不挡这类进程的 API 请求。
- 第二道是 `boot` 秘密（另一个 128 位随机值，**只存在于 SecretStorage 与 iframe URL，永不写日志**）：origin 会出现在
  Output channel 里供人查看，所以导航（面板加载）还必须带 `?boot=<boot>`，否则 404——从日志里学到 origin 的别的网站
  也嵌不进 Web UI，做不成点击劫持。
- 纵深防御（Fetch Metadata）：导航只受理 `Sec-Fetch-Dest: iframe` 且带正确 `boot`；其余请求必须
  `Sec-Fetch-Site: same-origin`（Web UI 自己的请求）。
- token 只存在于扩展宿主内存与 SecretStorage；**从不进入面板 HTML、iframe URL、日志、Output channel**。自起网关的
  banner 里 `Sign in …#token=` 与 `Token …` 两行在 `gatewayProcess.ts` 里被缓冲后丢弃，永不转给 `log`（Step 5）。
- 浏览器侧的 `Cookie`/`Origin`/`Referer` 与上游的 `Set-Cookie` 都不穿过中继；WS 握手要求
  `Origin == 中继 origin`，转发时丢 Origin、加 Bearer。
- 中继只在面板存活期间监听 `127.0.0.1` 的随机端口；`deactivate` 即关。
- 面板 HTML 自身 CSP：`default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-<随机>'; frame-src <中继 origin>`；
  webview 只有一段带 nonce 的内联脚本（按钮→postMessage），宿主侧对消息里的命令 id 做**白名单**。

**已知限制（写进 README，不在本计划解决）**
- Remote-SSH／WSL／容器窗口不支持（中继在远端回环，本地 webview 够不着）；manifest 声明 `extensionKind: ["ui"]`。
- webview 沙箱无 `allow-popups`：Web UI 里供应商 OAuth 登录的 `window.open`
  （`frontend/src/components/models/SubscriptionLoginModal.vue:594`）打不开系统浏览器 ⇒ 提供
  `Forebrain: Open Web UI in Browser` 命令，README 写明。
- `localStorage`（Web UI 的主题／上次会话等）按 origin 存：端口被占时回退随机端口 ⇒ origin 变 ⇒ 这些偏好回默认。
  nonce 持久化 + 粘性端口让它在通常情况下稳定，不承诺。

**组件清单（文件 → 职责）**

| 文件 | 职责 |
|---|---|
| `editors/vscode/package.json` | manifest：视图容器/视图/命令/配置/键位无关的 contributes、依赖与脚本 |
| `editors/vscode/build.mjs` | esbuild：`dist/extension.js`（external `ws`,`vscode`）与 `dist-test/*.test.js` |
| `editors/vscode/src/relay.ts` | 回环中继（HTTP 透传 + WS 升级字节管道），**逐字采用 Step 2** |
| `editors/vscode/src/gatewayOrigin.ts` | **纯函数**：`parseGatewayOrigin`（校验裸 http(s) origin、错误消息脱敏）、`GatewayConfig`、`ConfigError`；不 import vscode |
| `editors/vscode/src/config.ts` | `readGatewayConfig()`：读 `forebrain.*` 设置，调用 `gatewayOrigin`；唯一 import vscode 的设置代码（不单测） |
| `editors/vscode/src/gateway.ts` | REST 客户端：`health()`、`probe(token)`，5s 超时，错误分型 |
| `editors/vscode/src/token.ts` | token 梯子：SecretStorage → 环境 → 自起网关捕获 → （不自动弹窗）；探活三态；`redact` |
| `editors/vscode/src/gatewayProcess.ts` | 定位二进制、spawn、解析 banner（含 token 的行永不进日志）、只停自己起的子进程 |
| `editors/vscode/src/panelHtml.ts` | **纯函数**：`PanelState`、`panelUrl`、`renderPanelHtml`（无 token 入参）；不 import vscode |
| `editors/vscode/src/panel.ts` | `ChatPanel`（`WebviewViewProvider`）：持有 `WebviewView`、收发消息、调 `renderPanelHtml`；不单测 |
| `editors/vscode/src/connection.ts` | 编排：设置→梯子→中继→状态；重连串行化；dispose |
| `editors/vscode/src/extension.ts` | `activate`/`deactivate`：注册与命令转发 |
| `editors/vscode/src/*.test.ts` | node:test 单测（见 Test plan） |
| `editors/vscode/src/testing/*.ts` | 测试辅助（mock 网关等），仅被测试 import |
| `editors/vscode/assets/forebrain.svg` | 视图容器图标 |
| `editors/vscode/.vscodeignore` | VSIX 排除 src/测试/映射 |
| `editors/vscode/LICENSE` | 仓库根 `LICENSE` 的副本（vsce 找不到 license 文件时会交互提问） |
| `editors/vscode/README.md` | 安装、首次连接、设置、原理与安全模型、已知限制、排障 |
| `scripts/build-vscode-forebrain-ext.sh` | 编译 + vsce 打包到 `editors/vscode/out/` |
| `.gitignore` | 追加 `editors/vscode/dist-test/` 与 `editors/vscode/out/`（`dist`、`node_modules` 基线已覆盖） |

## Commands you will need

前置：`node --version` ≥ 20（spike 在 v22.22.1 上验证）。全部在 `editors/vscode/` 下执行。

| Purpose | Command | Expected on success |
|---|---|---|
| 安装依赖 | `npm install --no-audit --no-fund` | exit 0；新增 `package-lock.json` |
| 扩展编译 | `npm run compile` | exit 0；产出 `dist/extension.js`、`dist/extension.js.map`、`dist-test/relay.test.js` |
| 类型检查 | `npm run typecheck` | exit 0，无输出（**直接看退出码，别接管道**——管道后 `$?` 是 `tail` 的） |
| 单测 | `npm test` | exit 0；末尾 `# pass <N>` 等于 `# tests <N>`、`# fail 0` |
| 打包 VSIX | `bash scripts/build-vscode-forebrain-ext.sh`（仓库根执行） | 产出 `editors/vscode/out/forebrain-vscode-0.1.0.vsix` |
| 装进真机 VS Code | `code --install-extension editors/vscode/out/forebrain-vscode-0.1.0.vsix` | 输出含 `was successfully installed`（大小写无关） |
| 确认已装 | `code --list-extensions --show-versions`（输出用 `grep forebrain.forebrain-vscode` 过滤；完整管道见 Step 9 代码块，勿从本表复制管道） | 输出含 `forebrain.forebrain-vscode@0.1.0` |
| 零 harness 改动 | `git status --porcelain -- pkg frontend npm ai-elements-vue cmd` | 无输出 |

## Scope

**In scope（唯一允许修改/新建）**
- `editors/vscode/**`（新建整包）
- `scripts/build-vscode-forebrain-ext.sh`（新建）
- `.gitignore`（**仅追加** Step 8 给出的 3 行）
- `plans/376-*.md`、`plans/379-*.md` 与 `plans/README.md` 的对应行（由评审方维护，执行器不动）

**Out of scope（看起来相关也不许碰）**
- `pkg/**`、`frontend/**`、`ai-elements-vue/**`、`npm/**`、`cmd/**`、`docs/**`——harness 零改动是本计划的
  价值主张。「给网关加 CORS／Origin 豁免」「改 cookie 为 `SameSite=None`」「前端改持有 token」都是被 owner
  否决的备选架构，不是本计划的逃生口。
- `pkg/tui/vscode_ext/**`、`scripts/build-vscode-deliverable-ext.sh`、`pkg/tui/vscode_open.go`——既有迷你扩展，
  功能不同（SCM diff），不合并、不修改。
- VS Code 本体。
- 原生集成（选区/文件送会话、清单树、状态栏、审批通知、交付物打开）→ 379。**本计划不写它们的任何占位代码**（唯一例外：
  Step 6 `panelUrl` 的 `session` 参数与 `SESSION_ID` 校验是为 379 预留的接缝，376 自身不传第三参）。
- 原生 Chat 参与者（`vscode.chat.createChatParticipant`）→ 380。

## Git workflow

- 本仓现行规则（owner 常设裁决）：**禁止提交任何代码，用户手动提交。** 执行器不 `git commit`、不 push、不 merge。
- 被派发到隔离 worktree（`.worktrees/exec-376-vscode-panel`，分支同名，评审方创建）：全部改动留在该 worktree
  的未提交状态，报告里列出文件清单。
- 完成后不要动 `plans/README.md`（评审方维护）；若你是 owner 直接驱动的执行器且无人维护索引，才更新 376 行。

## Steps

### Step 1: 脚手架 `editors/vscode/`（manifest、tsconfig、构建脚本、图标、.vscodeignore）

新建 `editors/vscode/package.json`（依赖版本是 spike 实测可用的精确版本，**不要升级**；`typescript` 用 5.x
不用最新的 7.x）：

```json
{
  "name": "forebrain-vscode",
  "displayName": "Forebrain",
  "description": "The full Forebrain Harness Web UI in VS Code's side bar, authenticated through the extension host.",
  "version": "0.1.0",
  "publisher": "forebrain",
  "license": "Apache-2.0",
  "engines": { "vscode": "^1.100.0" },
  "categories": ["Other"],
  "extensionKind": ["ui"],
  "activationEvents": ["onView:forebrain.chat"],
  "main": "./dist/extension.js",
  "contributes": {
    "viewsContainers": {
      "auxiliarybar": [{ "id": "forebrain", "title": "Forebrain", "icon": "assets/forebrain.svg" }]
    },
    "views": {
      "forebrain": [{ "id": "forebrain.chat", "name": "Chat", "type": "webview", "contextualTitle": "Forebrain" }]
    },
    "commands": [
      { "command": "forebrain.openPanel", "title": "Forebrain: Open Chat Panel" },
      { "command": "forebrain.enterGatewayToken", "title": "Forebrain: Enter Gateway Token" },
      { "command": "forebrain.openWebUiInBrowser", "title": "Forebrain: Open Web UI in Browser" },
      { "command": "forebrain.reconnect", "title": "Forebrain: Reconnect to Gateway" }
    ],
    "configuration": {
      "title": "Forebrain",
      "properties": {
        "forebrain.gatewayUrl": {
          "type": "string", "default": "http://127.0.0.1:6060",
          "description": "The Forebrain gateway's HTTP(S) origin, without a path (e.g. http://127.0.0.1:6060)."
        },
        "forebrain.binaryPath": {
          "type": "string", "default": "",
          "description": "Path of the forebrain binary used to start a gateway when none is running. Empty: look up `forebrain` on PATH."
        },
        "forebrain.autoStartGateway": {
          "type": "boolean", "default": true,
          "description": "Start a gateway when the configured origin does not answer and no token is stored yet."
        },
        "forebrain.relayLogRequests": {
          "type": "boolean", "default": false,
          "description": "Write every relayed request's method and path to the Forebrain output channel (never headers)."
        }
      }
    }
  },
  "scripts": {
    "compile": "node build.mjs",
    "typecheck": "tsc --noEmit",
    "pretest": "node build.mjs --test",
    "test": "node --test 'dist-test/*.test.js'"
  },
  "devDependencies": {
    "@types/node": "22.20.5",
    "@types/vscode": "1.100.0",
    "@types/ws": "8.18.2",
    "esbuild": "0.28.2",
    "typescript": "5.9.3",
    "ws": "8.23.0"
  }
}
```

说明：`extensionKind: ["ui"]` 让扩展始终跑在本地（Remote 窗口里不跑去远端，见已知限制）。`ws` 在 376 里只有测试用，
所以放 `devDependencies`（VSIX 里不带它）；379 的宿主 WS 真正用到时再把它升为 dependency——不留「以后可能用到」的运行时依赖。

新建 `editors/vscode/tsconfig.json`：

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "Node16",
    "moduleResolution": "Node16",
    "lib": ["ES2022", "DOM"],
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "types": ["node"]
  },
  "include": ["src/**/*.ts"]
}
```

新建 `editors/vscode/build.mjs`：

```js
// Bundles the extension host entry and the tests with esbuild. `--test` builds
// only the tests (the `pretest` hook); the default run builds dist/extension.js
// and the tests.
import { build } from 'esbuild';
import { rm } from 'node:fs/promises';

const shared = {
  bundle: true,
  platform: 'node',
  target: 'node20',
  format: 'cjs',
  sourcemap: true,
  logLevel: 'info',
};

const testsOnly = process.argv.includes('--test');

if (!testsOnly) {
  await rm('dist', { recursive: true, force: true });
  // `vscode` only exists inside the extension host; `ws` stays external so the
  // tests (and, from plan 379 on, the extension) load it from node_modules.
  // 376 lists `ws` as a devDependency: nothing in dist/extension.js uses it yet.
  await build({
    ...shared,
    entryPoints: ['src/extension.ts'],
    outfile: 'dist/extension.js',
    external: ['ws', 'vscode'],
  });
}

await rm('dist-test', { recursive: true, force: true });
await build({
  ...shared,
  entryPoints: ['src/**/*.test.ts'],
  outdir: 'dist-test',
  external: ['ws', 'vscode'],
});
```

（`build.mjs` 是 ESM，`package.json` 没有 `"type": "module"` 也没关系：`.mjs` 后缀即 ESM。）

新建 `editors/vscode/assets/forebrain.svg`（24×24 单色，用 `currentColor` 跟随 VS Code 主题）：

```svg
<svg width="24" height="24" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M4 5h16v11H9l-5 4V5z" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"/><path d="M8 9h8M8 12h5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>
```

新建 `editors/vscode/LICENSE`（仓库根 `LICENSE` 的逐字副本，Apache-2.0）：

```bash
cp LICENSE editors/vscode/LICENSE
```

（vsce 2.32.0 在包目录里找不到 `LICENSE`/`LICENSE.md`/`LICENSE.txt` 时会打印 `LICENSE, LICENSE.md, or LICENSE.txt not found`
并交互提问 `Do you want to continue? [y/N]`——脚本是在非 TTY 下跑的，会自动答 y，但别把打包建立在这个隐式行为上。）

新建 `editors/vscode/.vscodeignore`：

```
.vscode/**
src/**
build.mjs
tsconfig.json
**/*.ts
**/*.map
dist-test/**
out/**
```

新建空入口 `editors/vscode/src/extension.ts`（Step 7 替换；先让编译链跑通）：

```ts
export function activate(): void {
  // replaced in Step 7
}

export function deactivate(): void {
  // replaced in Step 7
}
```

**Verify**：
```bash
cd editors/vscode && npm install --no-audit --no-fund && npm run compile && npm run typecheck
```
→ 三条均 exit 0；`ls dist/extension.js package-lock.json` 两个文件都在；`cd ../.. && git status --porcelain -- editors .gitignore`
只打印一行 `?? editors/`（`node_modules/` 与 `dist/` 已被根 `.gitignore` 第 31–32 行在任意深度忽略，所以 Git 只在目录级报一个未跟踪项）。

### Step 2: 回环中继 `src/relay.ts`（逐字采用）与其单测 `src/relay.test.ts`（逐字采用）

下面两份源码来自 advisor 在 `/tmp` spike 里跑通过的版本（10/10 PASS + 真实 Chromium 端到端）：**代码行与 spike
逐字一致**（advisor 已用脚本逐字节比对），仅 `relay.ts` 文件头的两处注释措辞比 spike 更新（描述稳定 nonce 与粘性端口）。
**逐字创建**，不要改函数名、常量名、判定逻辑；注释也保留（它们就是安全模型的说明）。唯一允许的改动：
若 `tsc` 因 `@types/node` 版本差异报类型错，用最小非空断言/收窄修复，并在报告 NOTES 里写明。

`editors/vscode/src/relay.ts`：

```ts
// The loopback relay: the one place where the gateway token meets a browser.
//
// The panel's iframe loads the gateway's Web UI from this relay instead of from
// the gateway itself, because the gateway's session cookie is SameSite=Strict
// and a page inside a VS Code webview is a cross-site frame: Chromium neither
// stores nor sends that cookie there. The relay fixes it without touching the
// harness: every upstream request carries `Authorization: Bearer <token>`, and
// the Web UI never needs a cookie.
//
// The relay's origin is the capability. Its hostname carries a 128-bit nonce
// (`fb-<nonce>.localhost`), and a request is admitted only when its Host header
// names exactly that origin, so another page or process that has not seen the
// iframe URL cannot use the relay to act as the token holder. A second secret
// (`boot`) rides in the iframe URL so that even a page that learned the origin
// cannot embed the Web UI. The extension generates both once and keeps them in
// its secret storage; the port is sticky while free; the relay lives only as
// long as the panel does.
import http, { request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import net from 'node:net';
import tls from 'node:tls';
import type { Duplex } from 'node:stream';

export interface RelayTarget {
  /** The gateway's HTTP(S) origin, e.g. `http://127.0.0.1:6060` (no path, no query). */
  origin: string;
  /** The gateway token. It is only ever written into upstream request headers. */
  token: string;
}

export interface RelayOptions {
  /**
   * The capability half of the relay's hostname: 32 lower-case hex characters.
   * The caller generates it once and keeps it, so the hostname half of the
   * origin stays the same between sessions.
   */
  nonce: string;
  /**
   * A second secret, independent of the nonce and never written to any log: the
   * panel's iframe URL carries it as `?boot=<boot>`, and a navigation is
   * admitted only with it. The origin (nonce and port) is written to the output
   * channel for the person to read; a page that learns the origin from there
   * still cannot embed the Web UI and click-jack it.
   */
  boot: string;
  /**
   * The port to try first. The Web UI keeps its own preferences in
   * localStorage, which is keyed by origin, and the origin includes the port:
   * reusing the previous port keeps those preferences. `0` (the default) lets
   * the OS choose; a taken port falls back to that too.
   */
  preferredPort?: number;
  /** Observes every admitted request for diagnostics: the method and path only. */
  onRequest?: (method: string, path: string) => void;
}

export interface Relay {
  /** The origin the panel's iframe loads: `http://fb-<nonce>.localhost:<port>`. */
  readonly origin: string;
  /** The port that was bound: remember it to pass back as `preferredPort`. */
  readonly port: number;
  /** Stops listening and closes every relayed connection. Safe to call twice. */
  close(): Promise<void>;
}

// Hop-by-hop headers belong to one connection and are never forwarded
// (RFC 9110 §7.6.1). `connection` and `upgrade` are re-added by the upgrade
// path, which writes its own request line.
const HOP_BY_HOP = new Set([
  'connection', 'keep-alive', 'proxy-connection', 'te', 'trailer',
  'transfer-encoding', 'upgrade', 'proxy-authenticate', 'proxy-authorization',
]);

// Credentials and context that must not cross the relay: the browser's own
// cookies and the frame's Origin/Referer would either leak into the gateway's
// cookie logic or be replaced by the token. `host` is rewritten explicitly.
const NEVER_FORWARD = new Set([
  'cookie', 'set-cookie', 'origin', 'referer', 'authorization', 'x-api-key', 'host',
]);

type ForwardedHeaders = Record<string, string>;

function forwardHeaders(
  headers: http.IncomingHttpHeaders,
  upstreamHost: string,
  authorization: string,
): ForwardedHeaders {
  const out: ForwardedHeaders = {};
  for (const [name, value] of Object.entries(headers)) {
    const key = name.toLowerCase();
    if (value === undefined || HOP_BY_HOP.has(key) || NEVER_FORWARD.has(key)) continue;
    out[key] = Array.isArray(value) ? value.join(', ') : value;
  }
  out.host = upstreamHost;
  out.authorization = authorization;
  return out;
}

function responseHeaders(headers: http.IncomingHttpHeaders): ForwardedHeaders {
  const out: ForwardedHeaders = {};
  for (const [name, value] of Object.entries(headers)) {
    const key = name.toLowerCase();
    if (value === undefined || HOP_BY_HOP.has(key) || key === 'set-cookie') continue;
    out[key] = Array.isArray(value) ? value.join(', ') : value;
  }
  return out;
}

/**
 * Whether a request may use the relay. The Host check is the capability check;
 * the Fetch Metadata checks are defence in depth for what a browser does with
 * the origin once a page has it:
 *  - a navigation is admitted only as the panel's own iframe load carrying the
 *    boot secret;
 *  - everything else must be same-origin with the relay (the Web UI's own
 *    fetches, scripts and styles), never a cross-site embed from another page.
 * A client that sends no Fetch Metadata headers is not a browser (the unit
 * tests); the Host check has already gated it.
 */
function bootQuery(url: string): string | null {
  try {
    return new URL(url, 'http://relay.invalid').searchParams.get('boot');
  } catch {
    return null;
  }
}

function admitted(req: http.IncomingMessage, expectedHost: string, boot: string): boolean {
  if (req.headers.host !== expectedHost) return false;
  // A proxy-style absolute URL would send the relay somewhere else entirely.
  if (!req.url || !req.url.startsWith('/')) return false;
  const site = req.headers['sec-fetch-site'];
  if (site === undefined) return true;
  if (req.headers['sec-fetch-mode'] === 'navigate') {
    // The panel's own iframe load: the boot secret is what tells it apart from
    // another page that learned the origin and tries to embed the same URL.
    return req.headers['sec-fetch-dest'] === 'iframe' && bootQuery(req.url) === boot;
  }
  return site === 'same-origin';
}

/**
 * Starts the relay on 127.0.0.1. `target.origin` must be an absolute http(s)
 * origin; the promise rejects for anything else.
 */
export async function startRelay(target: RelayTarget, options: RelayOptions): Promise<Relay> {
  const upstream = new URL(target.origin);
  if (upstream.origin !== target.origin || (upstream.protocol !== 'http:' && upstream.protocol !== 'https:')) {
    await Promise.reject(new Error(`gateway origin must be an http(s) origin without a path: ${target.origin}`));
  }
  const token = target.token.trim();
  if (!token) {
    await Promise.reject(new Error('gateway token is empty'));
  }
  if (!/^[0-9a-f]{32}$/.test(options.nonce)) {
    await Promise.reject(new Error('relay nonce must be 32 lower-case hex characters'));
  }
  if (!/^[0-9a-f]{32}$/.test(options.boot)) {
    await Promise.reject(new Error('relay boot secret must be 32 lower-case hex characters'));
  }
  const preferredPort = options.preferredPort ?? 0;
  const authorization = `Bearer ${token}`;
  const isTls = upstream.protocol === 'https:';
  const request = isTls ? httpsRequest : httpRequest;
  const nonce = options.nonce;
  const boot = options.boot;
  const onRequest = options.onRequest;
  const sockets = new Set<Duplex>();

  const server = http.createServer((req, res) => {
    sockets.add(req.socket);
    req.socket.once('close', () => sockets.delete(req.socket));
    if (!admitted(req, expectedHost, boot)) {
      res.writeHead(404, { 'content-type': 'text/plain' });
      res.end('not found');
      return;
    }
    onRequest?.(req.method ?? '', req.url ?? '');
    const preq = request(
      {
        protocol: upstream.protocol,
        hostname: upstream.hostname,
        port: upstream.port || (isTls ? 443 : 80),
        method: req.method,
        path: req.url,
        headers: forwardHeaders(req.headers, upstream.host, authorization),
      },
      (pres) => {
        res.writeHead(pres.statusCode ?? 502, responseHeaders(pres.headers));
        pres.pipe(res);
      },
    );
    preq.on('error', () => {
      if (!res.headersSent) res.writeHead(502, { 'content-type': 'text/plain' });
      res.end('gateway unreachable');
    });
    req.pipe(preq);
  });

  // A WebSocket handshake is relayed as raw bytes: the browser's handshake
  // headers are forwarded with the token added and the Origin dropped (the
  // gateway's default origin check passes a handshake without one), and the
  // gateway's `101` answer reaches the browser unchanged.
  server.on('upgrade', (req, client, head) => {
    if (!admitted(req, expectedHost, boot) || req.headers.origin !== origin) {
      client.destroy();
      return;
    }
    const headers = forwardHeaders(req.headers, upstream.host, authorization);
    headers.connection = 'Upgrade';
    headers.upgrade = 'websocket';
    const connect = isTls
      ? () => tls.connect({ host: upstream.hostname, port: Number(upstream.port || 443), servername: upstream.hostname })
      : () => net.connect({ host: upstream.hostname, port: Number(upstream.port || 80) });
    const upstreamSocket = connect();
    sockets.add(client);
    sockets.add(upstreamSocket);
    const drop = () => {
      sockets.delete(client);
      sockets.delete(upstreamSocket);
      client.destroy();
      upstreamSocket.destroy();
    };
    for (const s of [client, upstreamSocket]) {
      s.on('error', drop);
      s.on('close', drop);
    }
    upstreamSocket.once(isTls ? 'secureConnect' : 'connect', () => {
      let raw = `${req.method} ${req.url} HTTP/1.1\r\n`;
      for (const [name, value] of Object.entries(headers)) raw += `${name}: ${value}\r\n`;
      raw += '\r\n';
      upstreamSocket.write(raw);
      if (head.length > 0) upstreamSocket.write(head);
      client.pipe(upstreamSocket);
      upstreamSocket.pipe(client);
    });
  });

  await new Promise<void>((resolve, reject) => {
    const onError = (err: NodeJS.ErrnoException) => {
      if (err.code === 'EADDRINUSE' && preferredPort !== 0) {
        // The preferred port is taken: any port still serves, the origin just
        // changes and the Web UI starts from its default preferences.
        server.listen(0, '127.0.0.1', () => resolve());
        return;
      }
      reject(err);
    };
    server.on('error', onError);
    server.once('listening', () => {
      server.removeListener('error', onError);
      server.on('error', () => {}); // a later error is logged by the caller's close path
      resolve();
    });
    server.listen(preferredPort, '127.0.0.1');
  });
  const address = server.address();
  if (address === null || typeof address === 'string') {
    throw new Error('relay did not bind a TCP port');
  }
  const expectedHost = `fb-${nonce}.localhost:${address.port}`;
  const origin = `http://${expectedHost}`;

  let closed: Promise<void> | undefined;
  return {
    origin,
    port: address.port,
    close() {
      closed ??= (async () => {
        for (const s of sockets) s.destroy();
        await new Promise<void>((resolve) => server.close(() => resolve()));
      })();
      return closed;
    },
  };
}
```

`editors/vscode/src/relay.test.ts`：

```ts
import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import net from 'node:net';
import { WebSocket, WebSocketServer } from 'ws';
import { startRelay } from './relay';

const TOKEN = 'unit-test-token';
const NONCE = '0123456789abcdef0123456789abcdef';
const BOOT = 'ffffffffffffffffffffffffffffffff';

function startTestRelay(gatewayPort: number, options: { preferredPort?: number; onRequest?: (method: string, path: string) => void } = {}) {
  return startRelay({ origin: `http://127.0.0.1:${gatewayPort}`, token: TOKEN }, { nonce: NONCE, boot: BOOT, ...options });
}

interface Seen {
  path: string;
  headers: http.IncomingHttpHeaders;
}

// A stand-in for the gateway: it authenticates exactly the way
// pkg/gateway/controlplane.go does for a machine client — the Bearer header —
// and records every request it admitted, so a test can assert on what crossed
// the relay rather than on what the relay claims.
function startMockGateway() {
  const seen: Seen[] = [];
  const wss = new WebSocketServer({ noServer: true });
  const server = http.createServer((req, res) => {
    seen.push({ path: req.url ?? '', headers: req.headers });
    if (req.headers.authorization !== `Bearer ${TOKEN}`) {
      res.writeHead(401, { 'content-type': 'application/json' });
      res.end('{"error":"unauthorized"}');
      return;
    }
    if (req.url === '/stream') {
      res.writeHead(200, { 'content-type': 'text/event-stream', 'set-cookie': 'fb_session=1; SameSite=Strict' });
      res.write('first');
      setTimeout(() => {
        res.write('second');
        res.end();
      }, 60);
      return;
    }
    let body = '';
    req.setEncoding('utf8');
    req.on('data', (chunk: string) => {
      body += chunk;
    });
    req.on('end', () => {
      res.writeHead(201, { 'content-type': 'application/json' });
      res.end(JSON.stringify({
        body,
        auth: req.headers.authorization ?? null,
        cookie: req.headers.cookie ?? null,
        origin: req.headers.origin ?? null,
        referer: req.headers.referer ?? null,
      }));
    });
  });
  wss.on('connection', (ws: WebSocket) => {
    ws.on('message', (data) => ws.send(`echo:${String(data)}`));
  });
  server.on('upgrade', (req, socket, head) => {
    seen.push({ path: req.url ?? '', headers: req.headers });
    if (req.headers.authorization !== `Bearer ${TOKEN}`) {
      socket.destroy();
      return;
    }
    wss.handleUpgrade(req, socket as net.Socket, head, (ws) => wss.emit('connection', ws, req));
  });
  return {
    seen,
    wss,
    listen: () => new Promise<number>((resolve) => server.listen(0, '127.0.0.1', () => {
      resolve((server.address() as net.AddressInfo).port);
    })),
    close: () => new Promise<void>((resolve) => {
      wss.close();
      server.close(() => resolve());
      server.closeAllConnections();
    }),
  };
}

function request(port: number, path: string, headers: Record<string, string>, method = 'GET', body?: string) {
  return new Promise<{ status: number; headers: http.IncomingHttpHeaders; chunks: string[] }>((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, path, method, headers }, (res) => {
      const chunks: string[] = [];
      res.setEncoding('utf8');
      res.on('data', (c: string) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode ?? 0, headers: res.headers, chunks }));
    });
    req.on('error', reject);
    if (body !== undefined) req.write(body);
    req.end();
  });
}

function relayPort(relayOrigin: string): number {
  return Number(new URL(relayOrigin).port);
}

test('the relay adds the bearer token and drops what the browser sent', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const host = new URL(relay.origin).host;
  const res = await request(relayPort(relay.origin), '/echo', {
    host,
    cookie: 'fb_session=1',
    origin: relay.origin,
    referer: `${relay.origin}/`,
    'content-type': 'application/json',
  }, 'POST', '{"token":"x"}');
  assert.equal(res.status, 201);
  const echoed = JSON.parse(res.chunks.join('')) as Record<string, unknown>;
  assert.deepEqual(echoed, { body: '{"token":"x"}', auth: `Bearer ${TOKEN}`, cookie: null, origin: null, referer: null });
});

test('the relay refuses a request whose Host is not its own origin', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const port = relayPort(relay.origin);
  const res = await request(port, '/', { host: `127.0.0.1:${port}` });
  assert.equal(res.status, 404);
  assert.equal(gateway.seen.length, 0, 'nothing may reach the gateway under a wrong Host');
});

test('the relay streams a response unbuffered and never forwards Set-Cookie', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const host = new URL(relay.origin).host;
  const res = await request(relayPort(relay.origin), '/stream', { host, 'sec-fetch-site': 'same-origin' });
  assert.equal(res.status, 200);
  assert.equal(res.headers['set-cookie'], undefined);
  assert.deepEqual(res.chunks, ['first', 'second'], 'two writes arrive as two reads');
});

test('the relay admits only the panel iframe load, and only with the boot secret', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const host = new URL(relay.origin).host;
  const port = relayPort(relay.origin);
  const panelLoad = await request(port, `/?boot=${BOOT}`, {
    host, 'sec-fetch-site': 'cross-site', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe',
  });
  assert.equal(panelLoad.status, 201, 'the webview embedding the panel is cross-site');
  const withoutBoot = await request(port, '/', {
    host, 'sec-fetch-site': 'cross-site', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe',
  });
  assert.equal(withoutBoot.status, 404, 'another page that learned the origin cannot embed the Web UI');
  const wrongBoot = await request(port, '/?boot=00000000000000000000000000000000', {
    host, 'sec-fetch-site': 'cross-site', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe',
  });
  assert.equal(wrongBoot.status, 404, 'a guessed boot secret is refused');
  const topLevel = await request(port, '/', {
    host, 'sec-fetch-site': 'none', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'document',
  });
  assert.equal(topLevel.status, 404, 'the origin is for the panel, not for a browser tab');
  const crossSiteFetch = await request(port, '/api/chat/sessions', { host, 'sec-fetch-site': 'cross-site' });
  assert.equal(crossSiteFetch.status, 404, 'another page cannot fetch through the relay');
});

test('the relay carries a WebSocket handshake with the token and without Origin', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const ws = new WebSocket(`${relay.origin.replace(/^http/, 'ws')}/ws/chat`, { headers: { origin: relay.origin } });
  t.after(() => ws.terminate());
  const reply = new Promise<string>((resolve, reject) => {
    ws.once('error', reject);
    ws.on('message', (data) => resolve(String(data)));
  });
  await new Promise<void>((resolve, reject) => {
    ws.once('open', resolve);
    ws.once('error', reject);
  });
  ws.send('ping');
  assert.equal(await reply, 'echo:ping');
  const handshake = gateway.seen.find((s) => s.path === '/ws/chat');
  assert.ok(handshake, 'the handshake reached the gateway');
  assert.equal(handshake.headers.authorization, `Bearer ${TOKEN}`);
  assert.equal(handshake.headers.origin, undefined);
});

test('the relay refuses a WebSocket whose Origin is not its own', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const ws = new WebSocket(`${relay.origin.replace(/^http/, 'ws')}/ws/chat`, { headers: { origin: 'http://evil.example' } });
  const outcome = await new Promise<'error' | 'open'>((resolve) => {
    ws.once('error', () => resolve('error'));
    ws.once('open', () => resolve('open'));
  });
  t.after(() => ws.terminate());
  assert.equal(outcome, 'error');
  assert.equal(gateway.seen.length, 0);
});

test('close stops the listener and releases the port', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(() => gateway.close());
  const port = relayPort(relay.origin);
  await relay.close();
  await relay.close();
  await assert.rejects(request(port, '/', { host: new URL(relay.origin).host }));
});

test('startRelay rejects a gateway origin that is not a bare http(s) origin, an empty token, a malformed nonce or boot secret', async () => {
  const good = { origin: 'http://127.0.0.1:6060', token: TOKEN };
  await assert.rejects(startRelay({ ...good, origin: 'http://127.0.0.1:6060/login' }, { nonce: NONCE, boot: BOOT }));
  await assert.rejects(startRelay({ ...good, origin: 'ftp://127.0.0.1' }, { nonce: NONCE, boot: BOOT }));
  await assert.rejects(startRelay({ ...good, token: '   ' }, { nonce: NONCE, boot: BOOT }));
  await assert.rejects(startRelay(good, { nonce: 'not-hex', boot: BOOT }));
  await assert.rejects(startRelay(good, { nonce: NONCE, boot: 'nope' }));
});

test('a free preferred port is reused and a taken one falls back to another port', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const first = await startTestRelay(gatewayPort);
  t.after(() => gateway.close());
  const second = await startTestRelay(gatewayPort, { preferredPort: first.port });
  t.after(() => second.close());
  assert.equal(first.port, relayPort(first.origin));
  assert.notEqual(second.port, first.port, 'the port the first relay holds cannot be taken twice');
  const freed = first.port;
  await first.close();
  const third = await startTestRelay(gatewayPort, { preferredPort: freed });
  t.after(() => third.close());
  assert.equal(third.port, freed, 'a freed preferred port is reused, so the origin stays stable');
});

test('onRequest observes admitted requests by method and path only', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const observed: string[] = [];
  const relay = await startTestRelay(gatewayPort, { onRequest: (method, path) => observed.push(`${method} ${path}`) });
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  await request(relayPort(relay.origin), '/api/chat/sessions?project_id=p1', { host: new URL(relay.origin).host });
  await request(relayPort(relay.origin), '/', { host: `127.0.0.1:${relayPort(relay.origin)}` });
  assert.deepEqual(observed, ['GET /api/chat/sessions?project_id=p1']);
});
```

**Verify**：`cd editors/vscode && npm test` → exit 0，`# tests 10`、`# pass 10`、`# fail 0`；
`npm run typecheck` → exit 0。

### Step 3: 设置校验 `src/gatewayOrigin.ts`＋`src/config.ts`，REST 客户端 `src/gateway.ts`

**要单测的模块不得 import `vscode`**：`build.mjs` 把 `vscode` 设为 external，测试产物里是裸 `require("vscode")`，
而 `node --test` 下没有这个模块——哪怕只在类型位置用到，`import * as vscode from 'vscode'` 也会让整个测试文件崩掉
（实测 `Cannot find module 'vscode'`）。所以纯函数放独立文件；需要 `vscode` 的薄适配放另一个文件，只被 `extension.ts`
引用、不单测。本步的拆分：`gatewayOrigin.ts`（纯）＋`config.ts`（薄）。

`src/gatewayOrigin.ts`（纯函数，无 vscode）：

```ts
export interface GatewayConfig {
  /** A bare http(s) origin: `http://127.0.0.1:6060`. A trailing slash is dropped. */
  readonly origin: string;
  readonly binaryPath: string;
  readonly autoStartGateway: boolean;
  readonly relayLogRequests: boolean;
}

export class ConfigError extends Error {}

/**
 * What an error message may say about the value the person typed. A sign-in
 * link pasted into forebrain.gatewayUrl by mistake carries the token in its
 * fragment, and the message reaches both the output channel and the panel.
 */
function echo(raw: string): string {
  return raw.replace(/token=[^\s&#]*/gi, 'token=‹redacted›');
}

/**
 * Validates the origin. The origin becomes the upstream of a relay that adds
 * the token to every request, so it may not carry a path, a query, a fragment
 * or credentials: anything after the host would be a request the relay could
 * be talked into making.
 */
export function parseGatewayOrigin(raw: string): string {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    throw new ConfigError(`forebrain.gatewayUrl is not a URL: "${echo(raw.trim())}"`);
  }
  const trimmed = raw.trim().replace(/\/+$/, '');
  if (url.username || url.password) {
    throw new ConfigError('forebrain.gatewayUrl must not carry credentials');
  }
  if (url.origin !== trimmed || (url.protocol !== 'http:' && url.protocol !== 'https:')) {
    throw new ConfigError(`forebrain.gatewayUrl must be a bare http(s) origin such as http://127.0.0.1:6060 (no path or query): "${echo(trimmed)}"`);
  }
  return trimmed;
}
```

`src/config.ts`（唯一 import vscode 的设置代码；不单测）：

```ts
import * as vscode from 'vscode';
import { parseGatewayOrigin, type GatewayConfig } from './gatewayOrigin';

export function readGatewayConfig(config: vscode.WorkspaceConfiguration = vscode.workspace.getConfiguration('forebrain')): GatewayConfig {
  return {
    origin: parseGatewayOrigin(config.get<string>('gatewayUrl', 'http://127.0.0.1:6060')),
    binaryPath: (config.get<string>('binaryPath', '') ?? '').trim(),
    autoStartGateway: config.get<boolean>('autoStartGateway', true),
    relayLogRequests: config.get<boolean>('relayLogRequests', false),
  };
}
```

`src/gateway.ts`（`fetch` 可注入以便测试；每个请求 5s 超时；**无应答不是异常而是 `'unreachable'`**，因为梯子要对
「token 被拒」与「网关没在听」做不同处理）：

```ts
export const REQUEST_TIMEOUT_MS = 5_000;

/** The gateway answered, but not the way the caller hoped. */
export class GatewayHttpError extends Error {
  constructor(readonly status: number, readonly path: string, body: string) {
    super(`${path} answered ${status}: ${body.slice(0, 200)}`);
  }
}

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

/** What `GET /api/auth/session` said about a token. */
export type ProbeResult = 'ok' | 'rejected' | 'unreachable';

export class GatewayClient {
  constructor(readonly origin: string, private readonly fetchImpl: FetchLike = fetch) {}

  /** One GET. Nothing answered in time is `'unreachable'`; any answer is returned as is. */
  private async get(path: string, token?: string): Promise<Response | 'unreachable'> {
    const headers: Record<string, string> = {};
    if (token !== undefined) headers.authorization = `Bearer ${token}`;
    try {
      return await this.fetchImpl(`${this.origin}${path}`, {
        method: 'GET',
        headers,
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch {
      return 'unreachable';
    }
  }

  /** `GET /health` is unauthenticated: it says whether a gateway listens here. */
  async health(): Promise<boolean> {
    const res = await this.get('/health');
    return res !== 'unreachable' && res.status === 200;
  }

  /** 200 = accepted, 401 = rejected, no answer = unreachable; any other status is a GatewayHttpError. */
  async probe(token: string): Promise<ProbeResult> {
    const res = await this.get('/api/auth/session', token);
    if (res === 'unreachable') return 'unreachable';
    if (res.status === 200) return 'ok';
    if (res.status === 401) return 'rejected';
    throw new GatewayHttpError(res.status, '/api/auth/session', await res.text());
  }
}
```

`GatewayClient` 只有 `get`（私有）、`health`、`probe` 三个方法，没有更多：376 没有别的调用方（owner 常设规则：死代码
必须删）。POST 与 JSON 解析在 379 有调用方时再加。

新建测试 `src/gateway.test.ts`（7 条；用 `src/testing/httpServer.ts` 辅助：起一个 `http.createServer`、按路径回状态码、
记录收到的头部、可配置「挂起不回」）：

| # | 用例 | 断言 |
|---|---|---|
| 1 | `probe` 带 Bearer | 服务端收到 `authorization: Bearer t0k`，且方法为 GET |
| 2 | `probe` 200 | `'ok'` |
| 3 | `probe` 401 | `'rejected'` |
| 4 | `probe` 500 | 抛 `GatewayHttpError`，`status === 500` |
| 5 | `health` 200 / 503 | 同一条用例里两个断言：`true` / `false` |
| 6 | 无监听端口 | `probe` → `'unreachable'`，`health()` → `false` |
| 7 | 挂起不回的服务端 | `probe` 在 ~5s 后返回 `'unreachable'`（计时断言耗时 `< 7000ms`） |

新建测试 `src/gatewayOrigin.test.ts`（8 条；从 `./gatewayOrigin` import，纯函数）：

| # | 输入 | 期望 |
|---|---|---|
| 1 | `http://127.0.0.1:6060` | 原样 |
| 2 | `http://127.0.0.1:6060/` | 去掉尾斜杠 |
| 3 | `http://127.0.0.1:6060/login` | 抛 `ConfigError` |
| 4 | `http://127.0.0.1:6060?x=1` | 抛 `ConfigError` |
| 5 | `ftp://127.0.0.1` | 抛 `ConfigError` |
| 6 | `http://user:pw@127.0.0.1:6060` | 抛 `ConfigError`，消息含 `must not carry credentials` |
| 7 | `not a url` | 抛 `ConfigError` |
| 8 | `http://127.0.0.1:6060/login#token=SECRETTOKEN` | 抛 `ConfigError`，消息含 `‹redacted›` 且**不含** `SECRETTOKEN` |

**Verify**：`npm test` → `# tests 25`（10+7+8）、`# fail 0`；`npm run typecheck` exit 0。

### Step 4: token 梯子 `src/token.ts`

梯子顺序即契约（owner 2026-11-24 裁决保留旧稿四档；本计划**不自动弹窗**，第 4 档只由命令/面板按钮触发）：

1. **SecretStorage**（key `forebrain.gatewayToken`）→ 探活 `'ok'` 即用；`'rejected'`（401：生成的 token 已随进程消亡，或配置
   换了）则**删除该键**并落到下一档；`'unreachable'`（网关没在听）则**保留该键**、记下「见过不可达」后落到下一档。
2. **进程环境变量** `FOREBRAIN_GATEWAY_TOKEN` → 同样三态；`'ok'` 时写入 SecretStorage，其余不写。
3. **自起网关 stdout 捕获**（Step 5；仅当 `forebrain.autoStartGateway` 为 true 且 `/health` 不通；起不来——没有二进制、端口
   被占——返回 null）→ 生成的 token **只留在内存**（它随进程消亡，下次启动会换一个，缓存它没有意义）。
4. **用户粘贴**：`Forebrain: Enter Gateway Token` 命令（`showInputBox`，`password: true`）→ 探活 `'ok'` 才写入 SecretStorage。

全部落空时：途中见过 `'unreachable'` 且没拿到 token ⇒ `none/gateway-unreachable`（提示先起网关再 Reconnect，存储的 token
不动）；否则 `none/not-found`。**探活的三种结果各有去处，没有「抛错」这一种**——网关不可达不是异常路径。

```ts
/** Structural subset of vscode.SecretStorage, so tests pass an in-memory store. */
export interface SecretStore {
  get(key: string): Promise<string | undefined>;
  store(key: string, value: string): Promise<void>;
  delete(key: string): Promise<void>;
}

export const TOKEN_KEY = 'forebrain.gatewayToken';

export type TokenSource = 'secret-storage' | 'environment' | 'spawned-gateway';

/** A gateway this extension started and therefore stops itself. */
export interface OwnedGateway {
  readonly origin: string;
  stop(): Promise<void>;
}

export type TokenResolution =
  | { kind: 'token'; token: string; source: TokenSource; gateway: OwnedGateway | null }
  | { kind: 'none'; reason: 'not-found' | 'configured-token' | 'gateway-unreachable'; detail: string };

export interface TokenDeps {
  secrets: SecretStore;
  env: NodeJS.ProcessEnv;
  /** The gateway's origin, for messages only. */
  gatewayOrigin: string;
  /** What the gateway said about a token (Step 3's `ProbeResult`). */
  probe: (token: string) => Promise<'ok' | 'rejected' | 'unreachable'>;
  /**
   * Starts a gateway and returns its token, or null when it was not attempted
   * or failed (disabled, no binary, port taken). Resolves to `{ token: null }`
   * when the gateway started but has a configured token the banner does not print.
   */
  startGateway: () => Promise<{ token: string | null; origin: string } & OwnedGateway | null>;
}

const NONE_FOUND: TokenResolution = {
  kind: 'none', reason: 'not-found',
  detail: 'No usable gateway token was found. Paste one with "Forebrain: Enter Gateway Token", or start a gateway and run "Forebrain: Reconnect".',
};

export async function resolveGatewayToken(deps: TokenDeps): Promise<TokenResolution> {
  let unreachable = false;
  const stored = (await deps.secrets.get(TOKEN_KEY))?.trim();
  if (stored) {
    const verdict = await deps.probe(stored);
    if (verdict === 'ok') {
      return { kind: 'token', token: stored, source: 'secret-storage', gateway: null };
    }
    // Rejected: a generated token died with its process, or the configured one
    // changed — forget it. Unreachable: the gateway is not listening, and the
    // stored token may still be the right one once it is.
    if (verdict === 'rejected') await deps.secrets.delete(TOKEN_KEY);
    else unreachable = true;
  }
  const fromEnv = deps.env.FOREBRAIN_GATEWAY_TOKEN?.trim();
  if (fromEnv) {
    const verdict = await deps.probe(fromEnv);
    if (verdict === 'ok') {
      await deps.secrets.store(TOKEN_KEY, fromEnv);
      return { kind: 'token', token: fromEnv, source: 'environment', gateway: null };
    }
    if (verdict === 'unreachable') unreachable = true;
  }
  const started = await deps.startGateway();
  if (started === null) {
    if (unreachable) {
      return {
        kind: 'none', reason: 'gateway-unreachable',
        detail: `A token is on file but ${deps.gatewayOrigin} did not answer. Start the gateway, then run "Forebrain: Reconnect".`,
      };
    }
    return NONE_FOUND;
  }
  if (started.token === null) {
    // A gateway this extension started but cannot authenticate to is an orphan
    // nobody asked for: stop it before asking the person for the token.
    await started.stop();
    return { kind: 'none', reason: 'configured-token', detail: `A gateway is running at ${started.origin} with a token from forebrain.yaml; enter it with "Forebrain: Enter Gateway Token".` };
  }
  return { kind: 'token', token: started.token, source: 'spawned-gateway', gateway: started };
}

/** Replaces every occurrence of the token in text meant for a log. */
export function redact(text: string, token: string): string {
  return token ? text.split(token).join('‹token›') : text;
}
```

注意 `configured-token` 分支：如果网关是扩展自己起的却带配置 token（banner 只有 `Auth         token` 行），扩展把它
停掉再返回 `none`——否则留下一个用户不认识的孤儿进程。

新建测试 `src/token.test.ts`（8 条；内存 `SecretStore`、可脚本化的 `probe` 三态）：

| # | 用例 | 断言 |
|---|---|---|
| 1 | SecretStorage 有且探活 `'ok'` | `source: 'secret-storage'`；不调用 `startGateway`；不改存储 |
| 2 | SecretStorage 有但 `'rejected'`，env 有且 `'ok'` | 存储键被删；`source: 'environment'`；env 值被写入存储 |
| 3 | SecretStorage 有但 `'unreachable'`，env 无，自起成功且带 token | **存储键未被删**；`source: 'spawned-gateway'`；生成的 token **不写入**存储 |
| 4 | 存储与 env 都无，自起成功且带 token | `source: 'spawned-gateway'`；存储为空 |
| 5 | 自起返回 `{token: null}` | `kind: 'none', reason: 'configured-token'`；调用了 `stop()` |
| 6 | 什么都没有，自起返回 null | `kind: 'none', reason: 'not-found'` |
| 7 | 存储有、探活 `'unreachable'`，自起返回 null | `kind: 'none', reason: 'gateway-unreachable'`；存储键仍在 |
| 8 | `redact('a SECRET b SECRET', 'SECRET')` | `'a ‹token› b ‹token›'`；空 token 原样返回 |

**Verify**：`npm test` → `# tests 33`、`# fail 0`；`npm run typecheck` exit 0。

### Step 5: 自起网关 `src/gatewayProcess.ts`

```ts
import { spawn, type ChildProcess } from 'node:child_process';
import { constants as fsConstants } from 'node:fs';
import { access } from 'node:fs/promises';
import path from 'node:path';
import type { OwnedGateway } from './token';

export const GATEWAY_START_TIMEOUT_MS = 20_000;

export interface Banner { origin?: string; token?: string }

/** Reads the lines `forebrain gateway start` prints (pkg/gateway/serve_run.go writeStartupBanner). */
export function parseGatewayBanner(text: string): Banner {
  const out: Banner = {};
  for (const line of text.split(/\r?\n/)) {
    let m = /^Listening on (http:\/\/\S+?)\/?\s*$/.exec(line);
    if (m) out.origin = m[1];
    m = /^Token\s+(\S+)\s*$/.exec(line);
    if (m) out.token = m[1];
  }
  return out;
}

/**
 * The binary to start: the setting when it names an executable file, else the
 * first `forebrain` on PATH that is executable. Undefined when neither is.
 */
export async function findForebrainBinary(binaryPath: string, env: NodeJS.ProcessEnv = process.env): Promise<string | undefined> {
  const candidates = binaryPath ? [binaryPath] : (env.PATH ?? '').split(path.delimiter).filter(Boolean).map((dir) => path.join(dir, 'forebrain'));
  for (const candidate of candidates) {
    try {
      await access(candidate, fsConstants.X_OK);
      return candidate;
    } catch {
      // try the next one
    }
  }
  return undefined;
}

export async function startGateway(command: string[], log: (line: string) => void, timeoutMs = GATEWAY_START_TIMEOUT_MS): Promise<OwnedGatewayProcess> { … }
```

`startGateway` 的行为（逐条）：
- `spawn(command[0], command.slice(1), { stdio: ['ignore', 'pipe', 'pipe'] })`（继承 `env`）；生产调用传 `[bin, 'gateway', 'start']`。
- stdout 逐行累积并喂给 `parseGatewayBanner`；一旦 `origin` 出现：若 `token` 也有 → resolve `{origin, token, stop}`；
  若之后 300ms 内仍无 `Token` 行 → resolve `{origin, token: null, stop}`（配置了 token 的网关）。
- 子进程在 `Listening on` 之前退出 → reject，错误文本带 stderr 末尾 400 字符（端口被占等），经 `redact`。
- 超时（默认 20s）→ `stop()` 子进程并 reject `gateway did not print "Listening on" within 20s`。
- `stop()`：`SIGTERM`（网关在 `http_server.go:257` 处理它）**在首个 `await` 之前同步发出**，然后等 `exit` 最多 5s，仍活则
  `SIGKILL`；幂等。
- **日志绝不带 token**（banner 的 `Sign in …/login#token=<token>` 行排在 `Token        <token>` 行之前，
  `serve_run.go:357-358`；token 是 64 位 hex，URL 转义后与原值相同）：stdout **先缓冲、不边读边记**；promise 定型后
  （token 已知，或 300ms 窗口判定为配置 token）再把缓冲的行按序交给 `log`，每行先 `redact(line, token)`，且
  **含 `#token=` 或匹配 `/^Token\s/` 的行整行丢弃**。启动后陆续到达的 stdout/stderr 行同样只经 `redact` 转发。

`OwnedGatewayProcess extends OwnedGateway { readonly pid: number; readonly token: string | null }`。

测试 `src/gatewayProcess.test.ts` 用 `fixtures/fake-forebrain.js`（一个 node 脚本，按 argv 打印两种 banner 之一，
然后 `setInterval` 保活，监听 `SIGTERM` 退出）——放 `editors/vscode/src/testing/fixtures/`。用 `process.execPath`
（node 自身）作为「二进制」、脚本路径作为参数传进去——这正是签名取 `command: string[]` 的原因；测试传
`[process.execPath, fixture, 'banner-with-token']`。

| 用例 | 断言 |
|---|---|
| `parseGatewayBanner`（生成 token 的 banner 全文） | `origin === 'http://127.0.0.1:6060'`，`token === 'abc123'` |
| `parseGatewayBanner`（配置 token：`Auth         token`） | `origin` 有、`token` undefined |
| 启动 `banner-with-token` 夹具 | resolve 的 `origin/token` 正确；`log` 收到的行里**没有** `abc123`、没有含 `Sign in` 的行、没有以 `Token` 开头的行；`Listening on http://127.0.0.1:6060` 等其余 banner 行逐行到达 |
| 启动 `banner-configured` 夹具 | resolve 的 `token === null` |
| 启动 `exit-with-error` 夹具 | reject，消息含夹具 stderr 文本 |
| `stop()` 后 | `process.kill(pid, 0)` 抛 `ESRCH` |
| `findForebrainBinary('', {PATH: <临时目录>})` | 目录里有可执行 `forebrain` → 返回其路径；没有 → `undefined`；设置给绝对路径 → 原样返回 |

**Verify**：`npm test` → `# tests 40`、`# fail 0`；`npm run typecheck` exit 0。

### Step 6: 面板 `src/panelHtml.ts`（纯函数）＋ `src/panel.ts`（`WebviewViewProvider`）

与 Step 3 同理：HTML 模板要单测，所以放在不 import vscode 的 `src/panelHtml.ts`；只碰 `vscode.WebviewView` 的 `ChatPanel`
放 `src/panel.ts`，不单测。

`src/panelHtml.ts`：

```ts
import { randomBytes } from 'node:crypto';

export type PanelState =
  | { kind: 'connecting' }
  | { kind: 'ready'; relayOrigin: string; boot: string }
  | { kind: 'unavailable'; reason: 'no-token' | 'gateway-unreachable' | 'error'; detail: string };

/** The commands the webview may ask the host to run. Anything else is ignored. */
export const PANEL_COMMANDS = new Set(['forebrain.enterGatewayToken', 'forebrain.reconnect', 'forebrain.openWebUiInBrowser']);

const RELAY_ORIGIN = /^http:\/\/fb-[0-9a-f]{32}\.localhost:\d{1,5}$/;
const SECRET = /^[0-9a-f]{32}$/;   // the relay's boot secret
const SAFE_ID = /^[0-9a-f]{64}$/;  // the nonce of this page's own script
const SESSION_ID = /^[A-Za-z0-9_-]{1,128}$/;

/**
 * The URL the panel's iframe loads: the relay origin, the boot secret the relay
 * insists on for a navigation, and (from plan 379 on) the session the Web UI
 * should open. Throws on anything that does not match its shape, so no
 * unvalidated text ever reaches an attribute.
 */
export function panelUrl(relayOrigin: string, boot: string, session?: string): string {
  if (!RELAY_ORIGIN.test(relayOrigin)) throw new Error('not a relay origin');
  if (!SECRET.test(boot)) throw new Error('boot secret must be 32 hex characters');
  let url = `${relayOrigin}/?boot=${boot}`;
  if (session !== undefined) {
    if (!SESSION_ID.test(session)) throw new Error('session id has an unexpected shape');
    url += `&session=${session}`;
  }
  return url;
}

/**
 * The panel's whole document. It takes no token: what varies with the
 * connection is the relay URL, and that URL is the capability — a page that
 * can load it is the panel, and nothing else.
 */
export function renderPanelHtml(state: PanelState, nonce: string = randomBytes(32).toString('hex')): string {
  if (!SAFE_ID.test(nonce)) throw new Error('panel nonce must be 64 hex characters');
  const frame = state.kind === 'ready' ? ` frame-src ${state.relayOrigin};` : '';
  const body = state.kind === 'ready' ? readyBody(state.relayOrigin, state.boot) : statusBody(state);
  return `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-${nonce}';${frame}">
<title>Forebrain</title></head>
<body>${body}
<script nonce="${nonce}">${PANEL_SCRIPT}</script>
</body></html>`;
}
```

要点：
- `readyBody(relayOrigin, boot)` 返回
  `<iframe id="panel" src="${panelUrl(relayOrigin, boot)}" title="Forebrain" allow="clipboard-read; clipboard-write"></iframe>`——
  URL 一律出自 `panelUrl`（它先校验形状），**绝不把未校验的文本插进 HTML 或 CSP**。
- `statusBody(state)`：一段 `<div id="status" class="<reason>">`，含标题句、`detail`（**只经 `escapeHtml`**）与按钮。逐状态：

  | 状态 | 标题句 | 按钮（`data-command`） |
  |---|---|---|
  | `connecting` | `Connecting to Forebrain…` | 无 |
  | `unavailable/no-token` | `Forebrain needs the gateway token.` | `forebrain.enterGatewayToken`（Enter gateway token）、`forebrain.reconnect`（Reconnect） |
  | `unavailable/gateway-unreachable` | `The Forebrain gateway is not answering.` | `forebrain.reconnect` |
  | `unavailable/error` | `Forebrain could not connect.` | `forebrain.reconnect`、`forebrain.openWebUiInBrowser`（Open Web UI in browser） |

  按钮形状：`<button data-command="forebrain.enterGatewayToken">Enter gateway token</button>`。
- `PANEL_SCRIPT`（字符串常量，内容固定）：
  ```js
  const vscode = acquireVsCodeApi();
  for (const b of document.querySelectorAll('button[data-command]')) {
    b.addEventListener('click', () => vscode.postMessage({ type: 'command', id: b.dataset.command }));
  }
  window.addEventListener('message', (e) => {
    const m = e.data;
    if (m && m.type === 'status' && typeof m.text === 'string') {
      const s = document.getElementById('status');
      if (s) { s.hidden = false; s.textContent = m.text; }
    }
  });
  vscode.postMessage({ type: 'ready' });
  ```
- 样式只写内联 `<style>`，颜色用 VS Code CSS 变量（`--vscode-foreground`、`--vscode-descriptionForeground`、
  `--vscode-button-background`、`--vscode-button-foreground`、`--vscode-panel-background`）；
  `html,body{margin:0;height:100%;background:var(--vscode-panel-background)}`、`iframe{width:100%;height:100%;border:0;display:block}`。

`src/panel.ts` 的 `ChatPanel` 类（不单测；只做 `WebviewView` 的持有与消息转发）：

```ts
import * as vscode from 'vscode';
import { PANEL_COMMANDS, renderPanelHtml, type PanelState } from './panelHtml';

export class ChatPanel implements vscode.WebviewViewProvider {
  private view?: vscode.WebviewView;
  private state: PanelState = { kind: 'connecting' };
  private readonly onCommandEmitter = new vscode.EventEmitter<string>();
  readonly onCommand = this.onCommandEmitter.event;

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    view.webview.options = { enableScripts: true, localResourceRoots: [] };
    view.webview.html = renderPanelHtml(this.state);
    view.webview.onDidReceiveMessage((m: unknown) => {
      const id = (m as { type?: unknown; id?: unknown })?.id;
      if ((m as { type?: unknown })?.type === 'command' && typeof id === 'string' && PANEL_COMMANDS.has(id)) {
        this.onCommandEmitter.fire(id);
      }
    });
  }

  /** Replaces the document: the relay origin changed, so the iframe must. */
  setState(state: PanelState): void {
    this.state = state;
    if (this.view) this.view.webview.html = renderPanelHtml(state);
  }
}
```

`retainContextWhenHidden: true` 在注册处给（Step 7），使面板切走时流式继续、回来不重载。

测试 `src/panelHtml.test.ts`（7 条；从 `./panelHtml` import，纯函数，不需要 vscode）：

| # | 用例 | 断言 |
|---|---|---|
| 1 | `panelUrl(origin, BOOT)`／`panelUrl(origin, BOOT, 's1')` | `http://fb-<32hex>.localhost:51151/?boot=<BOOT>`；带会话时为 `…?boot=<BOOT>&session=s1`；非法 origin、非 32hex 的 boot、含 `/` 的 session 各自抛错 |
| 2 | ready + 合法 origin 与 boot | 含 `<iframe id="panel" src="http://fb-<32hex>.localhost:51151/?boot=<BOOT>"`；CSP 含 `frame-src http://fb-…`；不含 `#token=` |
| 3 | ready + 非法 origin（`http://127.0.0.1:6060"`、`javascript:`）或非法 boot | 抛错 |
| 4 | unavailable 三态逐个 | **不含 `<iframe`**；CSP 不含 `frame-src`；按钮集合逐字等于上表（`no-token`＝Enter token＋Reconnect，`gateway-unreachable`＝Reconnect，`error`＝Reconnect＋Open Web UI） |
| 5 | detail 含 `<img onerror=x>` | 输出里是 `&lt;img`，不含 `<img` |
| 6 | 任意状态（detail 用固定良性文本） | `html` 不含 `‹`、不含 `Bearer`、不含 `token=`（断言意图：渲染函数自身不引入 token 材料；Connection 脱敏产生的 `‹token›`／ConfigError 回显的 `token=‹redacted›` 属合法 detail，不在本条遍历范围）；script 标签的 nonce 与 CSP 的 nonce 相同；给定 nonce 时逐字一致 |
| 7 | 传入非 64hex nonce | 抛错 |

**Verify**：`npm test` → `# tests 47`、`# fail 0`；`npm run typecheck` exit 0。

### Step 7: 编排 `src/connection.ts` 与入口 `src/extension.ts`

`src/connection.ts` 把 Step 2-6 接起来。为了可测，依赖全部由构造函数注入（`vscode` 类型只以结构子集出现）：

```ts
export interface ConnectionDeps {
  readonly config: GatewayConfig;
  /** Stable per install: the capability half of the relay's hostname. */
  loadNonce(): Promise<string | undefined>;
  saveNonce(nonce: string): Promise<void>;
  /** Stable per install, never logged: the secret a navigation must carry. */
  loadBoot(): Promise<string | undefined>;
  saveBoot(boot: string): Promise<void>;
  /** The port the last relay used, so the origin stays stable. */
  loadPort(): Promise<number | undefined>;
  savePort(port: number): Promise<void>;
  readonly secrets: SecretStore;
  readonly env: NodeJS.ProcessEnv;
  readonly probe: (token: string) => Promise<'ok' | 'rejected' | 'unreachable'>;
  readonly healthy: () => Promise<boolean>;
  /** Starts a gateway, or resolves null when not attempted or when it failed (after logging why). */
  readonly startGateway: () => Promise<{ token: string | null; origin: string; stop(): Promise<void> } | null>;
  readonly startRelay: typeof startRelay;
  readonly log: (line: string) => void;
  readonly emit: (state: PanelState) => void;
}

export class Connection {
  private relay?: Relay;
  private gateway?: OwnedGateway;
  private token = '';
  /** The stored chain never stays rejected: a failed start must not poison the next reconnect. */
  private chain: Promise<unknown> = Promise.resolve();

  constructor(private readonly deps: ConnectionDeps) {}

  /** Serialised: a reconnect never races the start it replaces. */
  start(): Promise<void> { return this.connect(false); }
  reconnect(): Promise<void> { return this.connect(true); }

  /** The gateway origin this connection was built for. */
  get origin(): string { return this.deps.config.origin; }

  private connect(force: boolean): Promise<void> {
    const run = this.chain.then(async () => {
      if (force || !this.relay) await this.connectOnce();
    });
    this.chain = run.catch(() => {});
    // A failed start leaves no relay, and must leave no gateway child either.
    // Capture the token BEFORE teardown clears it: fail() must redact with the
    // token that was live when the error happened, or the raw token reaches the
    // log and the panel detail.
    return run.catch(async (err) => {
      const token = this.token;
      await this.teardown();
      this.fail(err, token);
    });
  }

  private async connectOnce(): Promise<void> {
    await this.teardown();
    this.deps.emit({ kind: 'connecting' });
    const resolution = await resolveGatewayToken({
      secrets: this.deps.secrets,
      env: this.deps.env,
      gatewayOrigin: this.deps.config.origin,
      probe: this.deps.probe,
      startGateway: this.deps.config.autoStartGateway
        ? async () => {
            if (await this.deps.healthy()) return null;
            return this.deps.startGateway();
          }
        : async () => null,
    });
    if (resolution.kind === 'none') {
      this.deps.emit({
        kind: 'unavailable',
        reason: resolution.reason === 'gateway-unreachable' ? 'gateway-unreachable' : 'no-token',
        detail: resolution.detail,
      });
      return;
    }
    this.token = resolution.token;
    this.gateway = resolution.gateway ?? undefined;
    const nonce = (await this.deps.loadNonce()) ?? randomBytes(16).toString('hex');
    await this.deps.saveNonce(nonce);
    const boot = (await this.deps.loadBoot()) ?? randomBytes(16).toString('hex');
    await this.deps.saveBoot(boot);
    this.relay = await this.deps.startRelay(
      { origin: this.deps.config.origin, token: this.token },
      {
        nonce,
        boot,
        preferredPort: (await this.deps.loadPort()) ?? 0,
        // Method and path only, and the path without its query: the query of a
        // navigation carries the boot secret.
        onRequest: this.deps.config.relayLogRequests
          ? (method, path) => this.deps.log(`relay: ${method} ${path.split('?')[0]}`)
          : undefined,
      },
    );
    await this.deps.savePort(this.relay.port);
    this.deps.log(`relay: ${this.relay.origin} -> ${this.deps.config.origin}`);
    this.deps.emit({ kind: 'ready', relayOrigin: this.relay.origin, boot });
  }

  /** The token for the sign-in link the user opens in their own browser; empty when not connected. */
  currentToken(): string { return this.token; }

  /** What the gateway said about an entered token; only `'ok'` stores it and reconnects. */
  async enterToken(entered: string): Promise<'ok' | 'rejected' | 'unreachable'> {
    const token = entered.trim();
    if (!token) return 'rejected';
    const verdict = await this.deps.probe(token);
    if (verdict !== 'ok') return verdict;
    await this.deps.secrets.store(TOKEN_KEY, token);
    await this.reconnect();
    return 'ok';
  }

  private async teardown(): Promise<void> {
    // The gateway child gets its signal first, before anything is awaited: on
    // shutdown the extension host may not live for another turn of the event
    // loop, and an orphaned gateway is the one thing a person would notice.
    const gatewayStop = this.gateway?.stop();
    this.gateway = undefined;
    this.token = '';
    await this.relay?.close();
    this.relay = undefined;
    await gatewayStop;
  }

  private fail(err: unknown, token: string): void {
    const message = err instanceof Error ? err.message : String(err);
    this.deps.log(redact(`connection failed: ${message}`, token));
    this.deps.emit({ kind: 'unavailable', reason: 'error', detail: redact(message, token) });
  }

  async dispose(): Promise<void> {
    // SIGTERM to a known gateway child before any await: the host may not live
    // for another event-loop turn. A start still in flight is not reachable
    // here — see Maintenance notes.
    const stop = this.gateway?.stop();
    await this.chain;
    await this.teardown();
    await stop;
  }
}
```

`src/extension.ts`：

```ts
import * as vscode from 'vscode';
import { ChatPanel } from './panel';
import { type PanelState } from './panelHtml';
import { Connection } from './connection';
import { readGatewayConfig } from './config';
import { ConfigError } from './gatewayOrigin';
import { GatewayClient } from './gateway';
import { findForebrainBinary, startGateway } from './gatewayProcess';
import { startRelay } from './relay';
import { redact } from './token';

// Module level so that `deactivate` can reach it: the subscription below
// disposes it too, but a shutdown awaits this handle rather than racing it.
let active: Connection | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const output = vscode.window.createOutputChannel('Forebrain');
  const log = (line: string) => output.appendLine(`${new Date().toISOString()} ${line}`);
  const panel = new ChatPanel();

  const build = (): Connection => {
    const config = readGatewayConfig();          // throws ConfigError: handled by the caller
    const client = new GatewayClient(config.origin);
    return new Connection({
      config, env: process.env,
      probe: (t) => client.probe(t), healthy: () => client.health(),
      // async 包一层：context.secrets.* 返回 vscode.Thenable（PromiseLike，不是 Promise），
      // 直接赋给 ConnectionDeps／SecretStore 的 Promise 签名会 typecheck 失败（**TS2322**，
      // 不是 TS2739——执行器实测），故 get/store/delete 三行都要包，不改任何判定。
      secrets: {
        get: async (k) => context.secrets.get(k),
        store: async (k, v) => { await context.secrets.store(k, v); },
        delete: async (k) => { await context.secrets.delete(k); },
      },
      loadNonce: async () => context.secrets.get('forebrain.relayNonce'),
      saveNonce: async (n) => { await context.secrets.store('forebrain.relayNonce', n); },
      loadBoot: async () => context.secrets.get('forebrain.relayBoot'),
      saveBoot: async (b) => { await context.secrets.store('forebrain.relayBoot', b); },
      loadPort: async () => context.globalState.get<number>('forebrain.relayPort'),
      savePort: async (p) => { await context.globalState.update('forebrain.relayPort', p); },
      startGateway: async () => {
        const bin = await findForebrainBinary(config.binaryPath);
        if (!bin) {
          log('gateway: no forebrain binary found (set forebrain.binaryPath or install @forebrain-harness/forebrain)');
          return null;
        }
        log(`gateway: starting ${bin}`);
        try {
          const p = await startGateway([bin, 'gateway', 'start'], log);
          log(`gateway: started (pid ${p.pid}) at ${p.origin}`);
          return p;
        } catch (err) {
          // startGateway never throws the token (Step 5); a port already taken
          // or a failing binary lands here and is reported, not retried.
          log(`gateway: failed to start: ${err instanceof Error ? err.message : String(err)}`);
          return null;
        }
      },
      startRelay,
      log: (l) => log(redact(l, active?.currentToken() ?? '')),
      emit: (state: PanelState) => panel.setState(state),
    });
  };

  const boot = async (): Promise<void> => {
    try {
      active ??= build();
      await active.start();
    } catch (err) {
      const detail = err instanceof ConfigError ? err.message : err instanceof Error ? err.message : String(err);
      log(`startup failed: ${detail}`);
      panel.setState({ kind: 'unavailable', reason: 'error', detail });
    }
  };

  context.subscriptions.push(
    output,
    vscode.window.registerWebviewViewProvider('forebrain.chat', panel, { webviewOptions: { retainContextWhenHidden: true } }),
    vscode.commands.registerCommand('forebrain.openPanel', () => vscode.commands.executeCommand('forebrain.chat.focus')),
    vscode.commands.registerCommand('forebrain.reconnect', () => void active?.reconnect()),
    vscode.commands.registerCommand('forebrain.enterGatewayToken', async () => {
      const entered = await vscode.window.showInputBox({ prompt: 'Forebrain gateway token', password: true, ignoreFocusOut: true });
      if (entered === undefined) return;
      if (!active) {
        void vscode.window.showInformationMessage('Forebrain: not connected yet — run "Forebrain: Reconnect" first.');
        return;
      }
      const verdict = await active.enterToken(entered);
      if (verdict === 'rejected') void vscode.window.showErrorMessage('Forebrain: the gateway rejected that token.');
      else if (verdict === 'unreachable') void vscode.window.showWarningMessage('Forebrain: the gateway is not answering.');
    }),
    vscode.commands.registerCommand('forebrain.openWebUiInBrowser', () => {
      if (!active) return;
      const token = active.currentToken();
      if (!token) { void vscode.window.showInformationMessage('Forebrain: connect the panel first.'); return; }
      // The sign-in link the gateway's own banner prints (the Web UI reads the
      // fragment once and wipes it). The token leaves the extension only into
      // the person's own browser, the way the banner already does.
      void vscode.env.openExternal(vscode.Uri.parse(`${active.origin}/login#token=${encodeURIComponent(token)}`));
    }),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('forebrain.gatewayUrl') || e.affectsConfiguration('forebrain.binaryPath') || e.affectsConfiguration('forebrain.autoStartGateway')) {
        // A new origin/binary needs a new Connection; the old one owns a relay
        // and possibly a gateway child, so it is disposed, not dropped.
        const previous = active;
        active = undefined;
        void (async () => {
          await previous?.dispose();
          await boot();
        })();
      }
    }),
    panel.onCommand((id) => void vscode.commands.executeCommand(id)),
    { dispose: () => void active?.dispose() },
  );
  await boot();
}

export async function deactivate(): Promise<void> {
  await active?.dispose();
  active = undefined;
}
```

`connection.ts` 自己 `import { randomBytes } from 'node:crypto'`（生成 nonce 与 boot）、`import { startRelay, type Relay } from './relay'`、
`import { resolveGatewayToken, redact, TOKEN_KEY, type SecretStore, type OwnedGateway } from './token'`、
`import type { GatewayConfig } from './gatewayOrigin'` 与 `import type { PanelState } from './panelHtml'`。

`src/connection.test.ts`（7 条；真实 `startRelay`、mock 网关用 `src/testing/httpServer.ts`；`loadBoot` 首次返回 undefined）：

| # | 用例 | 断言 |
|---|---|---|
| 1 | 无 token、无二进制（`probe` 恒 `'rejected'`，`startGateway` 返回 null） | emit 序列末项 `{kind:'unavailable', reason:'no-token'}`；`startRelay` 未被调用 |
| 2 | 存储里有可用 token | emit 末项 `{kind:'ready', boot: <32hex>}`；`startRelay` 收到的 `preferredPort` 等于 `loadPort` 的值、`boot` 等于状态里的值；nonce 与 boot 只**生成**一次：连续两次 `reconnect()` 后 `startRelay` 收到的 nonce/boot 值不变（注意 `saveNonce`/`saveBoot` 每次 connectOnce 都会被调用、值相同——**不要断言调用次数不变**，实现是无条件保存） |
| 3 | `reconnect()`（本条 harness 的 `loadNonce`/`loadPort` 恒返回 undefined，强制新 nonce/新端口——与 #2 的忠实存储刻意不同，否则粘性端口复用会让新旧 origin 相同） | 旧 relay origin 不再响应（`fetch` 抛）；新 origin 可用且与旧 origin 不同 |
| 4 | `dispose()` | `gateway.stop()` 被调用一次；relay 端口已释放 |
| 5 | `startRelay` 第一次 reject、之后恢复 | 第一次：emit `{kind:'unavailable', reason:'error'}` 且 `log` 文本里 token 已被 `‹token›` 替换、**失败路径已调用 `gateway.stop()`**；随后 `reconnect()` → emit `connecting` → `ready`（串行链未被毒化）；`dispose()` 正常 resolve |
| 6 | 存储有 token、`probe` 返回 `'unreachable'`、`startGateway` 返回 null | emit `{kind:'unavailable', reason:'gateway-unreachable'}`；存储里的 token 未被删除 |
| 7 | 任意一条走到 ready 的用例 | 交给 `log` 的每一行都不含 ready 状态里的 `boot` 值 |

**Verify**：`npm test` → `# tests 54`、`# fail 0`；`npm run compile` exit 0 且 `dist/extension.js` 含
`forebrain.chat`（`grep -c "forebrain.chat" dist/extension.js` ≥ 1）；`npm run typecheck` exit 0。

### Step 8: `.gitignore`、打包脚本、README

`.gitignore` **末尾追加 3 行**（不动既有行；`dist` 与 `node_modules` 已被第 31–32 行在任意深度忽略，不必重复）：

```
# editors/vscode build output (the VSIX is built from source by scripts/build-vscode-forebrain-ext.sh)
editors/vscode/dist-test/
editors/vscode/out/
```

新建 `scripts/build-vscode-forebrain-ext.sh`（仿 `scripts/build-vscode-deliverable-ext.sh`：mktemp 外构建、
同版本 vsce、`--allow-missing-repository`；先 `npm ci`/`npm install` + compile 保证 `dist/` 新鲜；vsix **不进 git**）：

```bash
#!/usr/bin/env bash
# Builds editors/vscode into a VSIX under editors/vscode/out/ (git-ignored).
# The build runs outside the package directory so no stray .vsix is packaged
# into the next one; vsce is pinned to the version the sibling script uses.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
pkg="$root/editors/vscode"
cd "$pkg"
if [ -f package-lock.json ]; then npm ci --no-audit --no-fund; else npm install --no-audit --no-fund; fi
npm run typecheck
npm test
npm run compile
mkdir -p "$pkg/out"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
version="$(node -p "require('./package.json').version")"
vsix="forebrain-vscode-$version.vsix"
npx --yes @vscode/vsce@2.32.0 package --allow-missing-repository --out "$tmp/$vsix"
mv -f "$tmp/$vsix" "$pkg/out/$vsix"
echo "built $pkg/out/$vsix"
```

（文件名里的版本号从 `package.json` 读，后续计划升版本时不必再改脚本。）

`editors/vscode/README.md`（英文，与仓内 README 风格一致；vsce 会把它放进 VSIX 的详情页）逐节：

1. **What it is**：the full Forebrain Harness Web UI as a VS Code side-bar view — sessions, streaming, approvals,
   diffs, subagent views, plan mode, `user_interaction`, the `session_todo` checklist — all of it, because it *is* the Web UI.
2. **Install**：`bash scripts/build-vscode-forebrain-ext.sh` → `code --install-extension editors/vscode/out/forebrain-vscode-0.1.0.vsix`；
   CLI 另装：`npm i -g @forebrain-harness/forebrain`。
3. **First connection**：四种拿 token 的方式（SecretStorage 里已存；`FOREBRAIN_GATEWAY_TOKEN`；扩展自动
   `forebrain gateway start` 并从 banner 捕获；或从终端 banner 的 `Token …` 行／`forebrain.yaml` 复制后运行
   `Forebrain: Enter Gateway Token`）。
4. **Settings**：四个设置的表格。
5. **How it works**：一张与本文 Architecture 同构的小图 + 安全模型（origin 与 `boot` 两道能力凭证、Fetch Metadata
   纵深防御、token 永不进页面与日志、中继只活到面板关闭、只停自己起的网关进程）。
6. **Known limitations**：Remote 窗口不支持；OAuth 弹窗被沙箱拦截 → `Forebrain: Open Web UI in Browser`；
   端口被占时 Web UI 本地偏好回默认；`forebrain gateway stop` 服务端无 handler（既有缺口，另立项）。
7. **Troubleshooting**：Output › Forebrain；`Help › Toggle Developer Tools › Console` 看面板白屏；
   `curl http://127.0.0.1:6060/health`；`forebrain gateway status`。
8. **Development**：`npm install && npm run compile && npm run typecheck && npm test`；打包脚本。

**Verify**：`bash scripts/build-vscode-forebrain-ext.sh` → 末行 `built …/forebrain-vscode-0.1.0.vsix`；
`unzip -l editors/vscode/out/forebrain-vscode-0.1.0.vsix | grep -E "extension/(dist/extension\.js|package\.json|assets/forebrain\.svg|LICENSE)$"`
→ 恰好 4 行；`unzip -l editors/vscode/out/forebrain-vscode-0.1.0.vsix | grep -E "extension/(src/|dist-test/|node_modules/|build\.mjs|tsconfig\.json)|\.map$"`
→ 无输出（`ws` 是 devDependency，不进 VSIX）；
`git status --porcelain -- editors/vscode/dist-test editors/vscode/out editors/vscode/node_modules` → 无输出。

### Step 9: 真机安装（执行器）与验收清单（owner）

执行器做（本机有 `code` 1.147.0）：

```bash
code --install-extension editors/vscode/out/forebrain-vscode-0.1.0.vsix
code --list-extensions --show-versions | grep forebrain.forebrain-vscode
```

→ 第一条输出含 `was successfully installed`（VS Code 1.147 是小写，按大小写无关匹配）；第二条打印 `forebrain.forebrain-vscode@0.1.0`。

**owner 验收清单（执行器看不到 UI，以下由 owner 逐条勾选并把截图放到 `editors/vscode/out/acceptance/`，该目录已 gitignore）**：

1. 重载 VS Code；Secondary Side Bar 出现 `Forebrain` 图标，点开是 `Chat` 视图。
2. **已有网关**（终端里跑着 `forebrain gateway start`）：面板显示未连接说明 → 命令面板 `Forebrain: Enter Gateway Token`
   → 粘贴 banner 的 `Token …` 值 → **不重载**即出现 Web UI 的会话列表（不是登录页）。
3. **无网关**（先确认 `forebrain` 在 PATH：`command -v forebrain`）：重载窗口 → 约 25s 内面板出现 Web UI；
   Output › Forebrain 有 `gateway: started (pid N) at http://127.0.0.1:6060`；关闭 VS Code 后
   `pgrep -fl "forebrain gateway start"` 无输出（子进程随扩展停止）。
4. 流式不断：面板里发一条消息，切到别的视图 10s 再切回——回复在继续、composer 没被重载。
5. WS 经中继：发一条会触发审批的消息（例如让它跑一条 shell 命令）→ 面板里出现审批卡 → 批准后继续。
6. 高级特性冒烟：打开一个带子代理的会话 → 子代理抽屉渲染正常；切到 plan 模式；让代理调 `session_todo` →
   进度清单卡渲染；`user_interaction` 的问题卡可作答。
7. `Forebrain: Open Web UI in Browser` → 系统浏览器打开 `http://127.0.0.1:6060/` 且已登录。
8. 安全自检：在普通浏览器标签打开 Output 里的中继地址 `http://fb-….localhost:PORT/` → 显示 `not found`。
9. 截图：`panel.png`（面板 + 一轮对话）、`not-connected.png`（未连接态）。

## Test plan

- 全部为 `node:test` + `node:assert/strict`，测试名即断言句。文件与用例数：`relay.test.ts` 10、`gateway.test.ts` 7、
  `gatewayOrigin.test.ts` 8、`token.test.ts` 8、`gatewayProcess.test.ts` 7、`panelHtml.test.ts` 7、`connection.test.ts` 7
  ⇒ **共 54**。要单测的模块一律不 import `vscode`（Step 3、Step 6 的拆分就是为此）。
- mock 网关：`relay.test.ts` 内置（逐字来自 spike）；其余用 `src/testing/httpServer.ts`（按路径回状态码、记录头部、
  可挂起）与 `src/testing/fixtures/fake-forebrain.js`（两种 banner、一种带错退出）。
- 回归证明（harness 零改动）：`git status --porcelain -- pkg frontend npm ai-elements-vue cmd` 无输出。

## Done criteria

机器可核，全部成立：

- [ ] `(cd editors/vscode && npm run compile && npm run typecheck && npm test)` 全部 exit 0，且输出 `# tests 54`、`# pass 54`、`# fail 0`
      （子 shell，不改变 cwd；**其余各条命令均在仓库根执行**）
- [ ] `grep -c "forebrain.chat" editors/vscode/dist/extension.js` ≥ 1
- [ ] `bash scripts/build-vscode-forebrain-ext.sh` exit 0，产出 `editors/vscode/out/forebrain-vscode-0.1.0.vsix`
- [ ] VSIX 内容：`unzip -l editors/vscode/out/forebrain-vscode-0.1.0.vsix | grep -E "extension/(dist/extension\.js|package\.json|assets/forebrain\.svg|LICENSE(\.txt)?)$"` 恰 4 行
      （**勘误 2026-11-25**：`vsce@2.32.0` 打包时把 `LICENSE` 写成 `extension/LICENSE.txt`，原正则 `LICENSE)$` 只能命中 3 行——判据意图不变，是正则要放宽）；
      `unzip -l editors/vscode/out/forebrain-vscode-0.1.0.vsix | grep -E "extension/(src/|dist-test/|node_modules/|build\.mjs|tsconfig\.json)|\.map$"` 无输出
- [ ] `code --install-extension editors/vscode/out/forebrain-vscode-0.1.0.vsix` 输出含 `was successfully installed`（**大小写无关**——
      VS Code 1.147 打印小写，`Successfully installed` 原句不再出现，勘误 2026-11-25）；
      `code --list-extensions --show-versions | grep forebrain.forebrain-vscode` 打印 `forebrain.forebrain-vscode@0.1.0`
- [ ] 零 harness 改动：`git status --porcelain -- pkg frontend npm ai-elements-vue cmd` 无输出；`git status --porcelain -- scripts` 只有
      `scripts/build-vscode-forebrain-ext.sh`
- [ ] `.gitignore` 相对 `3a261297` 的差异恰为 Step 8 的 3 行：`git diff 3a261297 -- .gitignore | grep '^+' | grep -v '^+++' | wc -l` → `3`
- [ ] `editors/vscode/` 下无构建产物入库：`git status --porcelain -- editors/vscode | grep -E "node_modules|dist-test/|/out/"` 无输出
- [ ] token 不进面板：`grep -rn "login#token=" editors/vscode/src --include=*.ts | grep -v "\.test\.ts"` 仅命中 `src/extension.ts`
      的 1 行（`openWebUiInBrowser` 打开用户自己浏览器登录链接的那行——不是面板）。**用 `login#token=` 而非 `#token=`**：
      Step 5 的 `gatewayProcess.ts` 丢弃规则合法地含字面量 `#token=`，不得被本判据误伤
- [ ] 要单测的模块不依赖 vscode：`grep -l "from 'vscode'" editors/vscode/src/*.ts | grep -E "relay|gateway|gatewayOrigin|token|panelHtml|connection"` 无输出
- [ ] 日志不走 console：`grep -rn "console\." editors/vscode/src --include=*.ts | grep -v "\.test\.ts" | grep -v "src/testing/"` 无输出
- [ ] `editors/vscode/README.md` 含 `Known limitations` 一节，且该节提到 Remote 窗口与 OAuth 弹窗
- [ ] owner 验收清单（Step 9）1–9 全部勾选，截图在 `editors/vscode/out/acceptance/`（执行器只负责其前的安装步骤）
- [ ] `plans/README.md` 本计划行状态更新（评审方维护）

## STOP conditions

遇到以下任一情况，停止并报告（不要绕路）：

- Drift check 任一条不成立（`editors/` 已存在、in-scope 文件有未提交改动、引用的 harness 文件在 `3a261297` 之后变过）。
- `node --version` < 20，或 `npm install` 两次失败（网络）。
- Step 2 的 `relay.ts` / `relay.test.ts` **逐字创建后**测试失败：这是 advisor 在 Node v22.22.1 上跑过的源码，失败说明环境不同——
  停下附完整输出，不要自行改写判定逻辑。
- 任何一步的验证命令修复两次后仍失败。
- 需要修改 out-of-scope 文件（尤其 `pkg/**`、`frontend/**`）才能继续——包括「给网关加 CORS／Origin 豁免」、
  「把 cookie 改成 `SameSite=None`」、「前端改成持有 token」：这三条是 owner 已否决的备选架构，不是逃生口。
- `code` CLI 不存在或 `code --install-extension` 失败（Step 9）。
- owner 验收时面板白屏／停在登录页／审批卡不出现：owner 提供 `Help › Toggle Developer Tools › Console` 与
  Output › Forebrain 的输出；执行器**不要**改成在 webview 里自建 UI，报告即可。

## Maintenance notes

- **wire 契约**：本扩展对 harness 的全部依赖只有四处——Bearer 头部鉴权（`controlplane.go:34-59,80-95`）、
  `GET /api/auth/session` 探活、`GET /health`、`forebrain gateway start` 的 banner 行形状（`serve_run.go:339-368`）。
  网关改其中任何一处，须同步本扩展（review checklist 项）。WS 协议（`wsProtocolVersion`、`wsClientMsg`）376 不触碰，
  379 才耦合。
- **中继是承重件，评审须逐行看**：Host 判定、导航的 `boot` 秘密判定、Fetch Metadata 判定、`NEVER_FORWARD`/`HOP_BY_HOP`、
  WS 握手的 `Origin == 中继 origin` 与「丢 Origin、加 Bearer」、`close()` 幂等且释放端口。任何「顺手放宽」都是安全回归。
- **为什么不用 iframe 直连**：见「为什么不是 iframe 直连网关」与附录 A。若将来网关提供「页面内 token 传输」
  （例如前端传输层可注入化），中继可整体移除，面板退化为直连 iframe。
- **已知限制**（README 同步）：Remote 窗口不支持；沙箱无 `allow-popups`，供应商 OAuth 登录需 `Open Web UI in Browser`；
  端口被占时 Web UI 的 localStorage 偏好回默认；`forebrain gateway stop` 的 `/admin/shutdown` 无服务端 handler 是既有缺口，
  扩展因此只停自己 spawn 的子进程。
- **dispose 的边界**：`dispose()` 已改为先同步 SIGTERM 已接管的网关子进程再等 chain；但若关窗时 `startGateway` 仍在等
  banner（≤20s），子进程句柄尚未 resolve，dispose 够不着它，可能留下孤儿进程。彻底解决需给 `startGateway` 加取消支持
  （传入 AbortSignal，spawn 前检查）——后续项，不在本期。验收第 3 条只测 ready 后关窗（可过）。
- **后续**：379（原生集成：宿主 WS 镜像 → `session_todo` 清单树／状态栏／审批通知；选区与文件送会话；交付物在 VS Code 打开。
  `forebrain.selectionTarget` 默认「面板当前会话」，其判定靠本计划 `RelayOptions.onRequest` 观察到的
  `GET /api/chat/sessions/:id/messages`）；380（原生 Chat 参与者 `@Forebrain`：`vscode.chat.createChatParticipant`
  自 `@types/vscode@1.100.0` 起为稳定 API，本机 VS Code 1.147）；再往后：类 Claude Code 的隐藏 `ide` MCP 服务
  （让代理取诊断／开 diff）与「每轮自动附带编辑器选区上下文」（需 harness 侧上下文注入钩子）。
- **文档缺口**：`docs/integration/batch-tasks.md` 默认地址写 8787（代码 6060）、`gateway.auth.mode` 旋钮已删——修它另立项。

## 参考先例（owner 2026-11-24：可参考 Codex / Claude Code 的 VS Code 插件）

| 先例 | 核实到的事实（来源） | 本计划取舍 |
|---|---|---|
| OpenAI Codex（`openai.chatgpt`） | 闭源 UI wrapper；扩展宿主 spawn 自带的 `codex app-server`；依赖 proposed API，需 VS Code ≥ 1.103；社区大量「webview 卡在 loading」报告（openai/codex#26951 及 OpenAI 社区的 code-server 版本边界分析帖） | 采纳「扩展宿主自起后端进程、只停自己起的」与「启动期显式渲染 connecting 态」；**拒绝** proposed API——只用稳定 API，`engines.vscode ^1.100.0` |
| Anthropic Claude Code | 官方文档 <https://code.claude.com/docs/en/vs-code>：面板可停靠 Secondary Side Bar／Primary Side Bar／编辑器标签；Activity Bar 有会话列表；diff 用 VS Code 原生 diff viewer；选区供 `@` 引用；隐藏 MCP 服务 `ide`（执行 notebook 单元／取诊断／开 diff）；CLI 每轮自动附带当前选区与活动文件路径；`Alt+Cmd+K` 把选区推入 prompt | 采纳：auxiliary bar 视图容器、`retainContextWhenHidden`、token／后端自起；379 采纳「选区→会话」命令与默认键位 `Ctrl+Cmd+K`／`Ctrl+Alt+K`（**不**用 `Alt+Cmd+K`，避免双装撞键）；`ide` MCP 与「每轮自动选区上下文」列为后续 |

两者都是**自建 webview UI + 扩展宿主桥接后端进程**。本计划不照搬自建 UI：forebrain 的全部特性已经在网关的 Web UI 里，
自建一份精简 UI 满足不了 owner「全部特性丝滑可用」的要求；两者的「宿主桥接」思想则由中继与 379 的宿主 WS 继承。

## Appendix A: 「跨站 iframe 拿不到 SameSite=Strict cookie」复现脚本

保存为任意路径（如 `/tmp/samesite-experiment.py`），`python3 samesite-experiment.py 8765 8766`，然后：

- 顶层对照：浏览器打开 `http://127.0.0.1:8765/login#token=x` → 页面显示 `cookie=present`。
- 跨站嵌入（等价于 VS Code webview）：打开 `http://localhost:8766/` → iframe 内显示 **`cookie=absent`**
  （`POST /api/auth/session -> 204` 成功，但随后的 GET 不带 cookie）。
- 同站嵌入对照：打开 `http://127.0.0.1:8766/` → iframe 内 `cookie=present`（站点＝协议＋主机，不看端口）。

```python
#!/usr/bin/env python3
"""Two loopback servers: B stands in for the gateway (SameSite=Strict session
cookie), A embeds B's login page. Cross-site = localhost embedding 127.0.0.1."""
import sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
B_PORT, A_PORT = int(sys.argv[1]), int(sys.argv[2])
PAGE = """<!doctype html><meta charset=utf-8><title>pending</title>
<h1 id=h>pending</h1><pre id=out></pre>
<script>(async () => {
  const log = (m) => { document.getElementById('out').textContent += m + "\\n"; };
  try {
    const r = await fetch('/api/auth/session', {method: 'POST'});
    log('POST /api/auth/session -> ' + r.status);
    const j = await (await fetch('/api/probe')).json();
    const verdict = 'cookie=' + j.cookie + ' origin=' + location.origin;
    log('GET /api/probe -> ' + JSON.stringify(j));
    document.title = verdict; document.getElementById('h').textContent = verdict;
  } catch (e) { document.title = 'error ' + e; }
})();</script>"""
COOKIE = "fb_session=1; Path=/; HttpOnly; SameSite=Strict"
class B(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _send(self, code, body, ctype="text/html; charset=utf-8", extra=None):
        data = body.encode(); self.send_response(code)
        self.send_header("Content-Type", ctype); self.send_header("Content-Length", str(len(data)))
        for k, v in (extra or []): self.send_header(k, v)
        self.end_headers(); self.wfile.write(data)
    def do_GET(self):
        path = self.path.split("#")[0].split("?")[0]
        if path == "/login": self._send(200, PAGE)
        elif path == "/api/probe":
            present = "present" if "fb_session=" in (self.headers.get("Cookie") or "") else "absent"
            self._send(200, '{"cookie": "%s"}' % present, "application/json")
        else: self._send(404, "not found", "text/plain")
    def do_POST(self): self._send(204, "", "text/plain", [("Set-Cookie", COOKIE)])
class A(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        body = ('<!doctype html><meta charset=utf-8><title>embedder</title>'
                '<p>embedder; iframe below:</p>'
                '<iframe src="http://127.0.0.1:%d/login#token=x" style="width:90%%;height:240px"></iframe>' % B_PORT).encode()
        self.send_response(200); self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
def serve(cls, port): ThreadingHTTPServer(("127.0.0.1", port), cls).serve_forever()
threading.Thread(target=serve, args=(A, A_PORT), daemon=True).start()
print(f"READY B={B_PORT} A={A_PORT}", flush=True)
serve(B, B_PORT)
```