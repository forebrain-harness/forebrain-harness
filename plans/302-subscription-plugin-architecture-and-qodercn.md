# Plan 302: 闭源订阅登录插件机制（支持 Qoder CN / ZCode / WorkBuddy 等）与 Qoder CN 客户端严格伪装插件实现

> **Executor instructions**: 按步骤顺序执行，每步的 verification 命令通过后再进入下一步。遇到「STOP conditions」中的任何情况立即停止并报告，不得自行猜测或改写契约。**不要执行任何 `git commit` / `git push` / `git merge`**：所有改动留在工作区由仓库 owner 审阅。注意工作区当前可能存在其他并行会话的未提交改动（如 `cmd/forebrain/serve.go`、`frontend/src/locales/index.ts` 等），严禁回退、覆盖或清理非本计划的改动。
>
> **Drift check (run first)**:
> `git diff --stat ecc4e1aa..HEAD -- .gitignore Makefile npm/scripts/build-platform-packages.sh pkg/config/provider_registry.go pkg/config/agent_llm.go pkg/llm/catalog.go pkg/llm/llm.go pkg/run/config.go pkg/subscription/registry.go pkg/turn/models.go pkg/tui/setup.go frontend/src/components/models/SubscriptionLoginModal.vue`
> 若上述 in-scope 文件与下述「Current state」摘录不一致（行号平移除外，以符号与代码内容为准），按 STOP 条件处理。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（跨前后端插件架构 + 第三方客户端加密签名协议伪装 + 凭证安全）
- **Depends on**: none（基于已落地的 259–268 订阅与模型表体系）
- **Category**: direction
- **Planned at**: commit `ecc4e1aa`, 2026-11-12

## Why this matters

用户需要在 Forebrain Harness 的 TUI 端 `/connect` 命令与 Web 端「设置 › 模型 › 订阅登录」中接入本机已安装并登录的 **Qoder CN** 订阅账号，且提出三项核心架构与安全硬约束：

1. **严格客户端伪装**：登录 Qoder CN 与调用 Qoder CN 端点（模型目录拉取、Token 刷新、流式推理生成）必须 100% 严格伪装成官方 Qoder CN 客户端（含内嵌 `qoder_auth_wasm` 的 RSA-1024 + AES-128-CBC 运态鉴权字段生成、`Bearer COSY.<b64>.<md5>` 签名、自定义字母表三段置换 `Encode=1` 载荷编解码、`Cosy-*` 全套设备与客户端身份头、`RemoteChatAsk` 请求体与 SSE 响应信封解包）。
2. **闭源插件化与 `.gitignore` 隔离**：Qoder CN 订阅登录的完整功能与前后端代码必须闭源，不提交进 Git 仓库，全部放入 `.gitignore` 忽略的插件目录中；开源主树不硬编码任何闭源插件协议或密钥，在无闭源插件源码时必须正常编译并通过全部测试（含 `pkg/architecture` 全部棘轮）。
3. **健壮的多插件扩展架构 + 本地编译打包**：插件机制必须足够通用与健壮，除本次落地 `qodercn` 插件外，未来还要以完全相同的方式接入 `zcode`、`workbuddy` 等闭源订阅登录插件；开发者拿到闭源插件源码放到本地后，执行 `npm/scripts/build-platform-packages.sh`（及 `cd npm && npm link`）即可自动发现前后端插件并编译打包进本地二进制与 Web UI 中使用。

---

## Current state

### 1. 宿主现有订阅与模型体系（全部已亲自读取核实）

- `pkg/subscription/registry.go`（Layer 2，扇出受控）：
  ```go
  // lines 130-147
  func Registry() []Subscription {
  	return []Subscription{chatGPT{}, openCodeGo{}}
  }

  func ByID(id string) (Subscription, bool) {
  	want := strings.ToLower(strings.TrimSpace(id))
  	for _, sub := range Registry() {
  		if sub.ID() == want {
  			return sub, true
  		}
  	}
  	return nil, false
  }
  ```
  `provisionRows`（lines 291-310）目前是包内私有函数，将 `[]Offer` 写入 `process.UpsertLLMModel`（`APIKey: &empty`, `Source: source`, `SourcePlan: offer.Plan`）。

- `pkg/config/provider_registry.go` 与 `pkg/config/agent_llm.go`（Layer 1）：
  ```go
  // pkg/config/provider_registry.go:264-290
  func BuiltinProviders() []BuiltinProvider { ... }
  func BuiltinProviderByID(id string) (BuiltinProvider, bool) { ... }

  // pkg/config/agent_llm.go:439-444
  func CredentialsBackedProvider(provider string) bool {
  	provider = strings.TrimSpace(provider)
  	return strings.EqualFold(provider, "chatgpt") ||
  		strings.EqualFold(provider, "opencode") ||
  		strings.EqualFold(provider, "opencode-go")
  }
  ```
  `CredentialsBackedProvider` 决定了：① `process.VerifyProvider` 是否放行不在 `models.json` 中的登录型 provider；② `process.UpsertLLMModel` 与 `MainAgentLLMValidation` 是否允许模型行的 `api_key` 为空；③ `NormalizeLLMModelIdentity` 是否保持登录型 provider 的 `api_path`；④ Web 模型页是否将该行标为 `login_backed: true` 并禁止手填创建。

- `pkg/llm/catalog.go`（Layer 0）：
  ```go
  // lines 739-762
  func SetBuiltinModels(provider string, models []Model) { ... }

  // lines 811-817
  func LoginBackedProvider(provider string) bool {
  	switch strings.ToLower(strings.TrimSpace(provider)) {
  	case "chatgpt", "opencode", "opencode-go":
  		return true
  	}
  	return false
  }
  ```
  `SetBuiltinModels` 已支持为不在 `assets/models.json` 中的 provider 注册模型能力（`ContextWindow`、`DefaultMaxTokens`、`CanReason`、`SupportsAttachments`、`ReasoningOptions`、`ReasoningKnown`），`Lookup` / `LookupRow` / `KnownProvider` / `OutputTokenCap` / `ReasoningEfforts` 均已自动读取 `builtinModels`！

- `pkg/run/config.go`（Layer 3）：
  ```go
  // lines 453-477
  	if prov == "chatgpt" {
  		...
  	}
  	if prov == "opencode" || prov == "opencode-go" {
  		return newOpenCodeLLM(prov, model, apiKey, baseURL, cfg)
  	}
  	if apiKey == "" {
  		return nil, fmt.Errorf("%w for provider %s", errLLMKeyMissing, prov)
  	}
  ```
  目前仅硬编码了 `chatgpt` 和 `opencode`/`opencode-go` 两个免 `apiKey` 分支。

- `pkg/turn/models.go`（Layer 3）：
  ```go
  // lines 373-387
  func SignInRejected(err error) bool {
  	var api *llm.APIError
  	if errors.As(err, &api) && api.StatusCode == http.StatusUnauthorized {
  		return true
  	}
  	var oc *opencode.ModelsError
  	if errors.As(err, &oc) && oc.StatusCode == http.StatusUnauthorized {
  		return true
  	}
  	var oa *openai.ModelsError
  	if errors.As(err, &oa) && oa.StatusCode == http.StatusUnauthorized {
  		return true
  	}
  	return false
  }
  ```
  注：`llm.HTTPStatusOf(err)`（在 `pkg/llm/errors.go` 中）或 `api *llm.APIError` 已能表示 401；若插件返回 `&llm.APIError{StatusCode: http.StatusUnauthorized}`，`SignInRejected(err)` 天然返回 `true`！

- `pkg/tui/setup.go`（Layer 5）：
  ```go
  // lines 1134-1140
  	if address != "" && localBrowser() && flow != subscription.FlowDeviceCode {
  		if openErr := browserOpener(address, func(err error) { ... }); openErr != nil && status != nil { ... }
  	}
  // lines 1241-1250
  func signInPage(login subscription.Login, address, flow string) string {
  	lines := make([]string, 0, 3)
  	switch flow {
  	case subscription.FlowDeviceCode:
  		if address != "" {
  			lines = append(lines, "Open "+address+" and enter this code.", "")
  		}
  		if code := login.UserCode(); code != "" {
  			lines = append(lines, code)
  		}
  ```
  当订阅插件的 `Flows()` 返回 `[]string{subscription.FlowBrowserCallback, subscription.FlowDeviceCode}` 时：
  - 在有图形浏览器的本地终端（`localBrowser() == true`），`drivableFlow` 选 `FlowBrowserCallback`，TUI 自动打开系统浏览器跳转到 `address` 并轮询 `login.Wait(ctx)`；
  - 在 SSH / 无头终端或 Web 网关（`pkg/gateway/login_api.go:208` 的 `authLoginFlow` 只选 `FlowDeviceCode` 或 `FlowPasteCallback`），自动选 `FlowDeviceCode`。若该插件是免短码的 PKCE 浏览器轮询流（`login.UserCode() == ""`），`signInPage` 在 `code == ""` 时应显示 `"Open " + address + " to sign in."` 而不是 `"Open ... and enter this code."`。

- `frontend/src/components/models/SubscriptionLoginModal.vue`：
  ```vue
  <!-- lines 36-51 -->
        <template v-if="flow === 'device_code'">
          <p>{{ t('models.deviceHint') }}</p>
          <div class="ucode" data-testid="models-login-code">{{ userCode || t('models.deviceCodePending') }}</div>
          <div class="acts2">
            <Button ... @click="openAuthorization">
              <ExternalLink class="size-3.5" aria-hidden="true" />{{ t('models.deviceOpen') }}
            </Button>
            <span class="wait"><span class="spin" aria-hidden="true" />{{ t('models.deviceWaiting') }}</span>
          </div>
        </template>
  ```
  目前缺少前端订阅插件扩展钩子（用于自定义插件描述文案、免短码流隐藏 `.ucode` 占位框或挂载插件专属步骤组件）。

- `pkg/architecture/cache_test.go`：
  - `TestPackageShape`（line 719）只扫描 `filepath.Join(root, "pkg")` 下的 Go 包（`maxPackageCount = 35`，`allowedSubpackage` 限制 `pkg/` 下深度为 2 的子包）。
  - 因此，**将闭源插件放在仓库根目录下的 `plugins/subscriptions/<id>/`（位于 `pkg/` 之外）**，完全不会触发 `maxPackageCount`、`allowedSubpackage`、`TestLayerCeiling` 或 `fanOutBudgets` 的任何限制！

---

### 2. 本机 Qoder CN 客户端（v0.5.7 / CLI Runtime `1.1.79-hotfix.19`）逆向事实与验证向量

以下事实全部采集自本机 `/Applications/Qoder CN.app/Contents/Resources/app.asar.unpacked/node_modules/@qoder-ai/qoder-cn-agent-sdk/dist/_worker/qoder-worker-runtime.obf.mjs`（含内嵌 `qoder_auth_wasm_bg.wasm`）与 `~/.qoder-cn/` 运行时文件，并已用 Node.js 实例化原版 WASM 完成 100% 字节级对拍验证（**不含任何用户真实密钥值**）：

#### 2.1 域名、端点与客户端身份常量

| 项目 | 精确值 | 出处 |
|---|---|---|
| 产品标识 / 渠道 | `productId = "qoder-cn"`, `client_type = "5"` (CLI), `business_product = "cli"`, `business_type = "agent"`, `scene = "app"` | `qoder-worker-runtime.obf.mjs:16495`, `:28978`, `.models/default` |
| 客户端版本号 | `CosyVersion = "1.1.79-hotfix.19"`, `User-Agent = "qoder/1.1.79-hotfix.19"` | `runtime-info.json`, `qoder-worker-runtime.obf.mjs:1003566`, `:19345000` |
| 生产环境 OAuth `client_id` | `"e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"` | `qoder-worker-runtime.obf.mjs:19288500` (`Ofl`) |
| 网页授权端 | `https://qoder.cn` | `qoder-worker-runtime.obf.mjs:28978` (`bq`) |
| 网关与推理端 | `https://gateway.qoder.com.cn` | `qoder-worker-runtime.obf.mjs:28978` (`kq`/`RYA`), `qodercli.log` |
| 机器 ID 文件 | `~/.qoder-cn/.auth/machine_id`（36 字节 UUID 字符串；不存在时生成 UUIDv4） | `qoder-worker-runtime.obf.mjs:3871000` (`Lxe`) |
| UMID 机器指纹缓存 | `~/.qoder-cn/umid-cache.json`：`info.machineToken`（缺省回退为 `machine_id`）、`info.machineType`（缺省回退为 `"5"`） | `~/.qoder-cn/umid-cache.json`, `qoder-worker-runtime.obf.mjs:19915134` (`jat`) |
| CLI 本地凭证文件 | `~/.qoder-cn/.auth/user`（使用 `machine_id[:16]` 加密的 JSON） | `qoder-worker-runtime.obf.mjs:3866700` (`Utt`) |
| 桌面端状态快照 | `~/.qoder-cn/.qoder-app-status.json`（含 `logged_in`, `name`, `avatar_url` 中的用户 `uid`） | `~/.qoder-cn/.qoder-app-status.json` |
| 本地模型目录缓存 | `~/.qoder-cn/.models/default`（含当前 `uid` 与 `scene`）及 `~/.qoder-cn/.models/<uid>/catalog-v7` | `qoder-worker-runtime.obf.mjs:26360212` (`XMe`) |

#### 2.2 登录、用户信息与 Token 刷新协议

1. **PKCE 浏览器设备流启动 (`startDeviceFlow`)**：
   - 随机生成长度 64 的 PKCE `verifier`（字符集 `ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~`）；
   - 计算 `challenge = base64.RawURLEncoding.EncodeToString(sha256(verifier))`，`challenge_method = "S256"`；
   - 生成随机 UUIDv4 作为 `nonce`；
   - 构造浏览器授权 URL：
     `https://qoder.cn/device/selectAccounts?challenge=<challenge>&challenge_method=S256&nonce=<nonce>&machine_id=<machine_id>&client_id=e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb`（若 `machine_token` 非空且不等于 `machine_id`，追加 `&machine_token=<machine_token>`）。
2. **轮询 Token (`deviceTokenPoll`)**：
   - `GET https://gateway.qoder.com.cn/api/v1/deviceToken/poll?nonce=<nonce>&verifier=<verifier>&challenge_method=S256`
   - 请求头：
     - `Accept: application/json`
     - `User-Agent: qoder/1.1.79-hotfix.19`
     - `Cosy-Version: 1.1.79-hotfix.19`
     - `Cosy-ClientType: 5`
     - `Cosy-MachineOS: <arch_os>`（如 `aarch64_darwin`、`x86_64_linux`、`x86_64_win32`）
   - 轮询间隔：`1s`，总超时 `5m`：
     - 状态码 `404`：用户尚未在浏览器点击确认，继续下一轮轮询；
     - 状态码 `200`：返回 JSON：
       ```json
       {
         "token": "<access_token>",
         "refresh_token": "<refresh_token>",
         "user_id": "<uid>",
         "user_name": "<name>",
         "expires_at": "<ISO8601 或秒/毫秒时间戳>",
         "expires_in": 3600,
         "refresh_token_expires_at": "<ISO8601 或秒/毫秒时间戳>",
         "refresh_token_expires_in": 2592000
       }
       ```
3. **PAT（Personal Access Token）兑换（可选，支持环境变量 `QODER_PERSONAL_ACCESS_TOKEN` 或手填）**：
   - `POST https://gateway.qoder.com.cn/api/v1/jobToken/exchange`
   - Body: `{"personal_token": "<pat>", "machine_id": "<machine_id>", "machine_token": "<machine_token>"}`
   - 响应同上（取 `token` / `device_token` / `access_token` 及 `refresh_token`）。
4. **用户信息补全 (`auth.getUserInfo`)**：
   - `GET https://gateway.qoder.com.cn/api/v1/userinfo`
   - Header: `Accept: application/json`, `Authorization: Bearer <token>`, `User-Agent: qoder/1.1.79-hotfix.19`, `Cosy-Version: 1.1.79-hotfix.19`, `Cosy-ClientType: 5`, `Cosy-MachineOS: <arch_os>`
   - 响应提取：`uid` (`id` / `user_id` / `uid`)、`name` (`name` / `username` / `user_name`)、`email`、`organization_id` (`orgId` / `organization_id` / `organization.id`)。
   - 若 `organization_id` 非空，可选请求 `GET https://gateway.qoder.com.cn/api/v1/organizations/<org_id>/tags` 获取 `tags` 数组（失败忽略）。
5. **Token 刷新 (`refreshDeviceToken` / `refreshJobToken`)**：
   - 过期判定：距 `expire_time` 剩余不足 `60s`（或服务端返回 401）时刷新；
   - 若 `login_method == "job_token"` 或 `"token"`：
     `POST https://gateway.qoder.com.cn/api/v1/jobToken/refresh`，Body: `{"refresh_token": "<refresh_token>"}` -> 返回 `{ "token": "...", "refresh_token": "...", "expires_at": ... }`；
   - 否则（默认 `"browser"`）：
     `POST https://gateway.qoder.com.cn/api/v1/deviceToken/refresh`，Body: `{"refresh_token": "<refresh_token>", "machine_id": "<machine_id>", "machine_token": "<machine_token>"}` -> 返回 `{ "device_token": "...", "refresh_token": "...", "expires_at": ... }`。

#### 2.3 `qoder_auth_wasm` 全部 5 项加密与签名算法（纯 Go 标准库 100% 复现）

1. **凭证 AES-128-CBC 加解密 (`CredentialEncrypt` / `CredentialDecrypt`)**：
   - 要求 `key16` 长度恰好为 16 字节（例如 `machine_id[:16]` 或随机 16 字符 hex）；
   - 算法：`AES-128-CBC`，**`Key = []byte(key16)`，`IV = []byte(key16)`**（IV 与 Key 完全相同！），PKCS#7 Padding，输出标准 Base64 字符串。
   - **实测验证向量**：
     `CredentialEncrypt("hello world", "0123456789abcdef") == "wem0Upqsl5MBD0Z39jWO/g=="`。

2. **运行时鉴权字段生成 (`GenerateRuntimeAuthFields`)**：
   - 输入结构体（字段顺序严格一致）：
     ```go
     type runtimeAuthInput struct {
         UID              string   `json:"uid"`
         SecurityOAuthTok string   `json:"security_oauth_token"`
         OrganizationID   string   `json:"organization_id,omitempty"`
         OrganizationTags []string `json:"organization_tags,omitempty"`
         DataPolicyAgreed bool     `json:"data_policy_agreed"`
     }
     ```
   - 生成随机 UUIDv4，去掉 `-` 后取前 16 个字符作为 `aesKey16`（16 个 ASCII 字符）；
   - `encryptUserInfo = CredentialEncrypt(json(runtimeAuthInput), aesKey16)`；
   - 使用 Qoder CN WASM 内嵌的 1024-bit RSA 公钥（PEM 见下）对 `[]byte(aesKey16)` 执行 `rsa.EncryptPKCS1v15(rand.Reader, rsaPub, []byte(aesKey16))`，标准 Base64 编码得到 `cosyKey`：
     ```
     -----BEGIN PUBLIC KEY-----
     MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
     4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
     6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
     XcW+ML9FoCI6AOvOzwIDAQAB
     -----END PUBLIC KEY-----
     ```

3. **自定义字母表三段置换编解码 (`EncodePayload` / `DecodePayload`，对应 `Encode=1` 与 `decrypt_server_response`)**：
   - 标准 Base64 表：`stdAlpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"`
   - Qoder 自定义表：`customAlpha = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"`（64 字符），padding 为 `'$'`（替代 `'='`）。
   - **编码 `EncodePayload(plain []byte) string`**：
     1) 标准 Base64 编码 `plain`，逐字符将 `stdAlpha[i]` 替换为 `customAlpha[i]`，`'='` 替换为 `'$'`，得到字符串 `s`；
     2) 令 `k = len(s) / 3`（整数除法）；
     3) 返回 `s[len(s)-k:] + s[k:len(s)-k] + s[:k]`（即将前 `k` 字节与后 `k` 字节互换）。
   - **解码 `DecodePayload(encoded string) ([]byte, error)`**：
     1) `encoded = strings.TrimSpace(encoded)`；若以 `'{'` 或 `'['` 开头且是合法 JSON，直接返回 `[]byte(encoded)`；
     2) 令 `k = len(encoded) / 3`，还原 `s = encoded[len(encoded)-k:] + encoded[k:len(encoded)-k] + encoded[:k]`；
     3) 逐字符将 `customAlpha[i]` 替换回 `stdAlpha[i]`，`'$'` 替换回 `'='`；
     4) 标准 Base64 解码返回明文字节。
   - **实测验证向量**：
     对 `{"request_id":"req-1","session_id":"sess-1","messages":[{"role":"user","content":"hi"}],"model_config":{"key":"qmodel_38max","source":"system"}}` 编码结果为：
     `"u(QOPSN*lEr*P(FQBMn*NS.bDHFrx^VIuSgIB*f*N(W.NOjLBMn*NkL^JHFtBE.WN(gEDxV*lLtXBEKbuHa*l*K.N(FYB*f*#(WhJHFhJoByBOi)BE.JGoKtu(CLugWMmYKYDxg.Dxjwx(LzBMn*NOF%G@T*GoK^Dxj^PSWhx(LzBMn*N(F^NYw%B*f*uSF^"`（0..100 任意长度均与 WASM 100% 逐字节一致）。

4. **`Bearer COSY` 请求签名 (`SignCosyBearer`)**：
   - 构造 JSON（字段顺序固定）：
     `{"version":"v1","requestId":"<reqUUID>","info":"<encryptUserInfo>","cosyVersion":"1.1.79-hotfix.19","ideVersion":""}`
   - `b64Part = base64.StdEncoding.EncodeToString(cosyJSON)`
   - `cosyDate = strconv.FormatInt(nowUnixSec, 10)`
   - `preimage = b64Part + "\n" + cosyKey + "\n" + cosyDate + "\n" + encodedBody + "\n" + signPath`
     - 对于 `GET /algo/api/v2/model/list?Encode=1`：`encodedBody = ""`, `signPath = "/api/v2/model/list"`
     - 对于 `POST /algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1`：`encodedBody` 为 `EncodePayload(remoteChatAskJSON)` 后的字符串，`signPath = "/api/v2/service/pro/sse/agent_chat_generation"`
   - `sig = fmt.Sprintf("%x", md5.Sum([]byte(preimage)))`
   - 返回 `"Bearer COSY." + b64Part + "." + sig`。
   - **实测验证向量**：
     - 当 `b64Part = "eyJ2ZXJzaW9uIjoidjEiLCJyZXF1ZXN0SWQiOiIzMTMxMzEzMS0zMTMxLTQxMzEtYjEzMS0zMTMxMzEzMTMxMzEiLCJpbmZvIjoiSU5GT19WQUwiLCJjb3N5VmVyc2lvbiI6IjEuMS43OS1ob3RmaXguMTkiLCJpZGVWZXJzaW9uIjoiIn0="`, `cosyKey = "KEY_VAL"`, `cosyDate = "1700000000"`, `encodedBody = "u(QOPSN*..."`（上例）, `signPath = "/api/v2/service/pro/sse/agent_chat_generation"` 时，`sig == "61e82033157daeaab04685f056f27eb1"`。

5. **本地模型缓存解密 (`DecryptModelCache`，用于读取 `~/.qoder-cn/.models/<uid>/catalog-v7`)**：
   - `raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedB64))`
   - 校验 `len(raw) >= 32` 且 `string(raw[:4]) == "QMC\x01"`；
   - 使用 `crypto/hkdf`（或标准 RFC 5869 `hmac.New(sha256.New, ...)`）：`salt = []byte("qoder-model-cache-enc")`, `ikm = []byte(uid)`, `info = []byte("model-cache-v1")`，派生 32 字节 `aesKey`；
   - `nonce = raw[4:16]`（12 字节），`ciphertextAndTag = raw[16:]`，用 `cipher.NewGCM(aes.NewCipher(aesKey))` 解密得到明文 JSON。

#### 2.4 Qoder CN 真实模型列表（实测解密自本机 `catalog-v7` `app` 场景，共 13 个模型）

当远程 `/algo/api/v2/model/list?Encode=1` 或本机 `catalog-v7` 返回模型目录时，解析 `app`（回退 `chat` / `assistant`）场景，内置静态回退表同样使用这 13 个真实模型：

| `key` (APIModel) | `display_name` | `max_input_tokens` | `max_output_tokens` | `is_vl` | `is_reasoning` | `efforts` (`thinking_config`) | `default_effort` | `is_default` |
|---|---|---|---|---|---|---|---|---|
| `auto` | `Auto` | 200000 | 32000 | true | true | — | — | true |
| `fast` | `Fast` | 200000 | 32000 | true | true | — | — | false |
| `qmodel_latest` | `Qwen4-Max-Preview` | 1000000 | 32000 | true | true | `["low", "high", "xhigh"]` | `"xhigh"` | false |
| `qmodel_38max` | `Qwen3.8-Max` | 180000 | 32000 | true | true | `["low", "medium", "xhigh"]` | `"medium"` | false |
| `qfmodel` | `Qwen3.8-Flash` | 180000 | 32000 | true | true | `["low", "medium", "xhigh"]` | `"medium"` | false |
| `dmodel` | `DeepSeek-V4-Pro` | 96000 | 32000 | true | true | `["high", "max"]` | `"max"` | false |
| `dfmodel` | `DeepSeek-Flash` | 180000 | 32000 | true | false | `["low", "high", "max"]` | `"high"` | false |
| `gmodel` | `GLM-5.3` | 180000 | 32000 | true | true | `["low", "high", "max"]` | `"high"` | false |
| `gfmodel` | `GLM-5.3-Flash` | 1000000 | 32000 | true | true | `["high", "max"]` | `"max"` | false |
| `kmodel_latest` | `Kimi-K3.1-Pro` | 180000 | 32000 | true | false | `["low", "high", "max"]` | `"high"` | false |
| `kmodel` | `Kimi-K2.8-Preview` | 180000 | 32000 | true | true | `["low", "high", "max"]` | `"high"` | false |
| `jmodel` | `Step-5-Preview` | 1000000 | 32000 | true | true | `["low", "medium", "high"]` | `"high"` | false |
| `mmodel` | `MiniMax-M3.1-Flash` | 180000 | 32000 | true | true | `["low", "medium", "high", "xhigh", "max"]` | `"xhigh"` | false |

---

## Non-functional requirements

1. **Latency（延迟）**：
   - **风险**：插件探测（`Probe`）或模型列表获取阻塞首帧或拖慢 `/connect` 与 Web 模型页打开速度。
   - **设计选择**：`Probe` 先走本地缓存/本机文件校验，网络请求严格受调用方 `ctx`（`modelsDiscoveryTimeout`）约束；`GenerateRuntimeAuthFields` 仅在登录或 Token 刷新时计算一次并持久化在凭证 JSON 中，每次推理请求只做纯内存的 Base64 + MD5 签名（微秒级，0 外部调用）。
   - **验证检查**：单测断言 `PrepareInferRequest` 单次耗时 < 5ms，且 `Probe` 在取消的 `ctx` 下立即返回。
2. **Resources（资源）**：
   - **风险**：SSE 流解包 `io.Pipe` goroutine 在客户端取消或上游断流时泄漏。
   - **设计选择**：`unwrapQoderSSEStream` 的后台读取 goroutine 绑定 `resp.Body.Close()` 与 `pw.CloseWithError(err)`，当下游消费者关闭 `PipeReader` 时，`pw.Write` 立即返回 `io.ErrClosedPipe` 并退出 goroutine、关闭底层 `resp.Body`；所有 HTTP 响应体均用 `io.LimitReader` 限制最大字节数（模型列表 4 MiB、鉴权响应 1 MiB）。
   - **验证检查**：`go test -race` 跑插件流式中断测试，确认无 goroutine 泄漏与数据竞争。
3. **Robustness（健壮性）**：
   - **风险**：① 本机正在运行 Qoder CN 桌面端 Agent 任务时，Forebrain 刷新 Token 导致桌面端会话被踢；② 并发回合同时触发 Token 刷新导致单次有效的 `refresh_token` 竞争失败；③ 网络轮询无超时挂死。
   - **设计选择**：① 进程内使用 `llm.CredentialMutex(path)` 互斥锁串行化刷新，刷新遇到 401 时复用 `adoptRotatedLogin` 模式重读磁盘/状态库是否已被另一进程刷新；② 独立浏览器设备流登录签发独立的 `device_token`，绝不覆盖或破坏 `~/.qoder-cn/.auth/user` 或桌面端 `auth.v1.dat`；③ 设备流轮询带 5 分钟硬上限并响应 `ctx.Done()`。
   - **验证检查**：并发刷新竞争单测 + 取消上下文单测。
4. **Maintainability（可维护性）**：
   - **风险**：开源宿主散落插件特判。
   - **设计选择**：宿主仅暴露 4 个干净的注册表函数与 1 个前端插件接口，代码中零 `"qodercn"` / `"zcode"` / `"workbuddy"` 字符串；所有 Qoder CN 逻辑 100% 封装在 `plugins/subscriptions/qodercn/`。
   - **验证检查**：`grep -rn -i "qoder" pkg/ cmd/ frontend/src/`（排除 gitignored 的生成文件）输出为空。
5. **Extensibility（可扩展性）**：
   - **风险**：未来接入 `zcode`、`workbuddy` 订阅登录时需要再次修改宿主代码。
   - **设计选择**：`scripts/gen-subscription-plugins.sh` 自动扫描 `plugins/subscriptions/*`（后端 `plugin.go` + 前端 `frontend/index.ts`），未来新增 `plugins/subscriptions/zcode` 或 `plugins/subscriptions/workbuddy` 只需放入对应目录并执行 `npm/scripts/build-platform-packages.sh`，宿主零改动。
   - **验证检查**：单测用伪造的第二个插件（如 `fake-workbuddy`）验证多插件同时注册、按序展示、独立路由。
6. **User experience（用户体验）**：
   - **风险**：免短码的浏览器设备流在 TUI 或 Web 端显示空白短码框或误导用户“输入验证码”。
   - **设计选择**：TUI 本机有浏览器时自动打开授权页并显示等待进度行，无短码时提示打开链接确认即可；Web 端当插件声明 `hideEmptyUserCode`（或 `userCode` 为空且 `url` 已返回）时隐藏 `.ucode` 短码框，直接提供「在浏览器中打开」按钮与自动完成轮询。
   - **验证检查**：Vitest 组件测试与 Go TUI `setup_test.go` 测试。

---

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 生成插件桥接 | `scripts/gen-subscription-plugins.sh` | exit 0 |
| Go 构建 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 架构守护测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` | PASS |
| 宿主与插件单测 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go test -tags fts5 -race ./pkg/config/... ./pkg/llm/... ./pkg/subscription/... ./pkg/turn/... ./pkg/run/... ./pkg/gateway/... ./pkg/tui/... ./plugins/subscriptions/... -count=1` | PASS |
| 前端类型与单测 | `cd frontend && pnpm typecheck && pnpm exec vitest run src/components/models/SubscriptionLoginModal.test.ts` | PASS, 0 errors |
| 平台打包编译 | `npm/scripts/build-platform-packages.sh` | exit 0，产出 `npm/dist/@forebrain-harness/forebrain-*/vendor/*/bin/forebrain` |

---

## Scope

**In scope（允许修改或新增的文件）**：

1. **开源宿主通用插件机制（不含任何具体闭源插件名称）**：
   - `.gitignore`（新增 `/plugins/`、`/cmd/forebrain/plugins_local_gen.go`、`/frontend/src/plugins/local_subscriptions.gen.ts`）
   - `scripts/gen-subscription-plugins.sh`（新建：扫描 `plugins/subscriptions/*` 生成 Go 与前端桥接文件；无插件时生成空桥接或清理）
   - `Makefile` 与 `npm/scripts/build-platform-packages.sh`（在构建前端与 Go 二进制前调用 `scripts/gen-subscription-plugins.sh`）
   - `pkg/config/provider_registry.go`、`pkg/config/agent_llm.go` 及 `pkg/config/provider_registry_test.go`（新增 `RegisterProviderPlugin`，让 `CredentialsBackedProvider` 对 `LoginBacked` 插件自动返回 `true`）
   - `pkg/llm/catalog.go`、`pkg/llm/llm.go` 及 `pkg/llm/catalog_test.go`（新增 `RegisterProviderFactory` / `BuildProviderLLM`，让 `LoginBackedProvider` 对已注册工厂的 provider 自动返回 `true`）
   - `pkg/run/config.go` 及 `pkg/run/config_test.go`（在 `NewLLMFromYAML` 中接入 `llm.BuildProviderLLM`）
   - `pkg/subscription/registry.go` 及 `pkg/subscription/registry_test.go`（新增 `Register`、`ResetPluginsForTest`、导出 `ProvisionRows`、`Offer` 补充能力字段并同步注册到 `llm.SetBuiltinModels`）
   - `pkg/tui/setup.go` 及 `pkg/tui/setup_test.go`（`signInPage` 对 `FlowDeviceCode` 但 `UserCode() == ""` 时的文案适配）
   - `frontend/src/plugins/subscriptionPlugin.ts`（新建：前端订阅插件类型定义与注册表读取）
   - `frontend/src/components/models/SubscriptionLoginModal.vue` 及 `frontend/src/components/models/SubscriptionLoginModal.test.ts`（接入前端订阅插件钩子与免短码设备流展示）

2. **闭源 Qoder CN 插件实现（完全位于 `.gitignore` 忽略的 `plugins/subscriptions/qodercn/` 下）**：
   - `plugins/subscriptions/qodercn/plugin.go`（`init()` 注册 `config.BuiltinProvider`、`llm.RegisterProviderFactory`、`llm.SetBuiltinModels`、`subscription.Register`）
   - `plugins/subscriptions/qodercn/crypto.go`（WASM 5 大加密/签名/编解码算法的纯 Go 1:1 实现）
   - `plugins/subscriptions/qodercn/identity.go`（Qoder CN 客户端身份、机器 ID、UMID 指纹与 `Cosy-*` 请求头构造）
   - `plugins/subscriptions/qodercn/auth.go`（本机 `~/.qoder-cn` 凭证与缓存探测、PKCE 浏览器设备流登录、用户信息补全、Token 自动刷新与 `fb_config` `auth.qodercn` 持久化）
   - `plugins/subscriptions/qodercn/models.go`（`/algo/api/v2/model/list?Encode=1` 远程拉取、`catalog-v7` 本地缓存解密与 13 个模型的静态回退表）
   - `plugins/subscriptions/qodercn/transport.go`（`http.RoundTripper`：将 OpenAI Chat Completions 请求重写为 `RemoteChatAsk` + `Encode=1` + `COSY` 签名，并将 SSE 响应信封解包为标准 OpenAI SSE 流）
   - `plugins/subscriptions/qodercn/qodercn_test.go`（覆盖 WASM 加密签名向量、PKCE 登录轮询、Token 刷新、模型列表解密、推理请求伪装与 SSE 解包的完整测试套件）
   - `plugins/subscriptions/qodercn/frontend/index.ts`（Qoder CN 前端订阅插件定义）

**Out of scope（严禁触碰）**：
- 不得修改 `~/.qoder-cn/.auth/user` 或 `/Users/doudou/Library/Application Support/com.qodercn.app.stable/auth.v1.dat`（只读探测本机文件，绝不覆写外部客户端文件，以免干扰本机正在运行任务的 Qoder CN 进程）。
- 不得在 `pkg/`、`cmd/`（除 gitignored 生成文件外）或 `frontend/src/`（除 gitignored 生成文件外）硬编码 `"qodercn"`、`"zcode"`、`"workbuddy"`。
- 不得修改 `pkg/architecture/cache_test.go` 的任何预算常数。

---

## Git workflow

- **零 Commit 规则**：按仓库 owner 常设规则，**严禁执行任何 `git commit`、`git push`、`git merge` 或修改分支**。
- 所有闭源插件文件位于 `/plugins/`，生成桥接文件位于 `/cmd/forebrain/plugins_local_gen.go` 与 `/frontend/src/plugins/local_subscriptions.gen.ts`，均由 `.gitignore` 忽略。

---

## Steps

### Step 1: `.gitignore` 与插件自动发现生成脚本 `scripts/gen-subscription-plugins.sh`

1. 在 `.gitignore` 末尾追加以下规则：
   ```gitignore
   # Closed-source subscription plugins (qodercn, zcode, workbuddy, etc.) and generated bridges:
   /plugins/
   /cmd/forebrain/plugins_local_gen.go
   /frontend/src/plugins/local_subscriptions.gen.ts
   ```
2. 创建 `frontend/src/plugins/subscriptionPlugin.ts`（纳入版本控制的通用前端插件契约，不含任何具体插件名）：
   ```ts
   import type { Component } from 'vue'
   import type { SubscriptionRow } from '@/lib/api'
   import { localSubscriptionPlugins } from './local_subscriptions.gen'

   export interface SubscriptionFrontendPlugin {
     /** Must match backend Subscription.ID() (e.g. 'qodercn', 'zcode', 'workbuddy'). */
     id: string
     /** Optional subtitle shown under the subscription name in Step 1. */
     describe?: (locale: 'zh' | 'en', row: SubscriptionRow) => string
     /** Optional hint shown in Step 2 while waiting for browser authorization. */
     waitingHint?: (locale: 'zh' | 'en') => string
     /** When true, hides the 8-char user_code box if userCode is empty (browser-confirm device flow). */
     hideEmptyUserCode?: boolean
     /** Optional custom Vue component for plugin-specific login steps. */
     customStepComponent?: Component
   }

   const registry = new Map<string, SubscriptionFrontendPlugin>()
   for (const plugin of localSubscriptionPlugins) {
     registry.set(plugin.id.trim().toLowerCase(), plugin)
   }

   export function getSubscriptionFrontendPlugin(id: string): SubscriptionFrontendPlugin | undefined {
     return registry.get(id.trim().toLowerCase())
   }
   ```
3. 创建可执行脚本 `scripts/gen-subscription-plugins.sh`（`chmod +x`）：
   - 扫描 `$ROOT_DIR/plugins/subscriptions/*/plugin.go`（按目录名排序）：
     - 若存在至少一个插件，生成 `$ROOT_DIR/cmd/forebrain/plugins_local_gen.go`：
       ```go
       // Code generated by scripts/gen-subscription-plugins.sh; DO NOT EDIT.
       package main

       import (
       	_ "github.com/forebrain-harness/forebrain-harness/plugins/subscriptions/<id>"
       )
       ```
     - 若不存在任何 Go 插件，删除 `$ROOT_DIR/cmd/forebrain/plugins_local_gen.go`（若存在）。
   - 扫描 `$ROOT_DIR/plugins/subscriptions/*/frontend/index.ts`（按目录名排序）：
     - 始终生成 `$ROOT_DIR/frontend/src/plugins/local_subscriptions.gen.ts`：
       - 无插件时：
         ```ts
         // Code generated by scripts/gen-subscription-plugins.sh; DO NOT EDIT.
         import type { SubscriptionFrontendPlugin } from './subscriptionPlugin'

         export const localSubscriptionPlugins: SubscriptionFrontendPlugin[] = []
         ```
       - 有插件时：静态 `import plugin_<idx> from '../../../../plugins/subscriptions/<id>/frontend/index'` 并导出 `export const localSubscriptionPlugins: SubscriptionFrontendPlugin[] = [plugin_0, ...]`。
4. 在 `Makefile` 的 `ui:` 与 `build:` 目标前、以及 `npm/scripts/build-platform-packages.sh` 的第 62 行（构建 UI 之前）调用 `"$ROOT_DIR/scripts/gen-subscription-plugins.sh"`。

**Verify**:
`./scripts/gen-subscription-plugins.sh && test -f frontend/src/plugins/local_subscriptions.gen.ts` → exit 0。

---

### Step 2: 宿主后端扩展点（`pkg/config`、`pkg/llm`、`pkg/run`、`pkg/subscription`、`pkg/tui`）

1. **`pkg/config/provider_registry.go` 与 `pkg/config/agent_llm.go`**：
   - 在 `pkg/config/provider_registry.go` 中增加线程安全的插件 provider 表：
     ```go
     var (
     	pluginProvidersMu sync.RWMutex
     	pluginProviders   []BuiltinProvider
     )

     // RegisterProviderPlugin registers a provider supplied by a local subscription
     // plugin. Re-registering the same ID replaces the entry in place.
     func RegisterProviderPlugin(p BuiltinProvider) {
     	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
     	if p.ID == "" || p.ID == CustomProvider {
     		return
     	}
     	cloned := p
     	cloned.Endpoints = append([]ProviderEndpoint(nil), p.Endpoints...)
     	cloned.Models = append([]BuiltinModel(nil), p.Models...)
     	pluginProvidersMu.Lock()
     	defer pluginProvidersMu.Unlock()
     	for i, existing := range pluginProviders {
     		if existing.ID == cloned.ID {
     			pluginProviders[i] = cloned
     			return
     		}
     	}
     	pluginProviders = append(pluginProviders, cloned)
     }

     // ResetProviderPluginsForTest clears registered provider plugins in tests.
     func ResetProviderPluginsForTest() {
     	pluginProvidersMu.Lock()
     	pluginProviders = nil
     	pluginProvidersMu.Unlock()
     }
     ```
   - 修改 `BuiltinProviders()` 与 `BuiltinProviderByID(id string)`，使其在 `builtinProviders` 之后合并查询 `pluginProviders`。
   - 修改 `pkg/config/agent_llm.go:439` 的 `CredentialsBackedProvider(provider string) bool`：
     ```go
     func CredentialsBackedProvider(provider string) bool {
     	provider = strings.TrimSpace(provider)
     	if strings.EqualFold(provider, "chatgpt") ||
     		strings.EqualFold(provider, "opencode") ||
     		strings.EqualFold(provider, "opencode-go") {
     		return true
     	}
     	if p, ok := BuiltinProviderByID(provider); ok && p.LoginBacked {
     		return true
     	}
     	return false
     }
     ```

2. **`pkg/llm/llm.go` 与 `pkg/llm/catalog.go`**：
   - 在 `pkg/llm/llm.go` 新增插件 LLM 构造器注册接口：
     ```go
     // ProviderLLMRequest carries the configuration a registered provider plugin
     // needs to construct an LLM client.
     type ProviderLLMRequest struct {
     	Provider        string
     	Model           string
     	APIKey          string
     	BaseURL         string
     	APIPath         string
     	MaxOutputTokens int
     	ParamsJSON      []byte
     }

     type ProviderLLMFactory func(req ProviderLLMRequest) (LLM, error)

     var (
     	providerFactoriesMu sync.RWMutex
     	providerFactories   = map[string]ProviderLLMFactory{}
     )

     func RegisterProviderFactory(provider string, factory ProviderLLMFactory) {
     	p := strings.ToLower(strings.TrimSpace(provider))
     	if p == "" || factory == nil {
     		return
     	}
     	providerFactoriesMu.Lock()
     	providerFactories[p] = factory
     	providerFactoriesMu.Unlock()
     }

     func HasProviderFactory(provider string) bool {
     	p := strings.ToLower(strings.TrimSpace(provider))
     	providerFactoriesMu.RLock()
     	_, ok := providerFactories[p]
     	providerFactoriesMu.RUnlock()
     	return ok
     }

     func BuildProviderLLM(req ProviderLLMRequest) (LLM, bool, error) {
     	p := strings.ToLower(strings.TrimSpace(req.Provider))
     	providerFactoriesMu.RLock()
     	factory, ok := providerFactories[p]
     	providerFactoriesMu.RUnlock()
     	if !ok {
     		return nil, false, nil
     	}
     	client, err := factory(req)
     	return client, true, err
     }
     ```
   - 在 `pkg/llm/catalog.go:811` 的 `LoginBackedProvider(provider string) bool` 中，若 `HasProviderFactory(provider)` 为 `true` 则返回 `true`；在 `KnownProvider(provider string) bool` 中，若 `HasProviderFactory(provider)` 为 `true` 也返回 `true`。

3. **`pkg/run/config.go`**：
   - 在 `NewLLMFromYAML(cfg *LLMProviderYAML)`（line 471 `if prov == "opencode" || prov == "opencode-go"` 之后、`if apiKey == ""` 之前）加入：
     ```go
     	if client, ok, err := llm.BuildProviderLLM(llm.ProviderLLMRequest{
     		Provider:        prov,
     		Model:           model,
     		APIKey:          apiKey,
     		BaseURL:         baseURL,
     		APIPath:         apiPath,
     		MaxOutputTokens: cfg.MaxOutputTokens,
     		ParamsJSON:      cfg.Params.Bytes(),
     	}); ok {
     		if err != nil {
     			return nil, err
     		}
     		return wrapUsageAccountingLLM(client), nil
     	}
     ```

4. **`pkg/subscription/registry.go`**：
   - 增加插件订阅注册表：
     ```go
     var (
     	pluginSubsMu sync.RWMutex
     	pluginSubs   []Subscription
     )

     // Register installs a subscription plugin into the surface registry.
     func Register(sub Subscription) {
     	if sub == nil || strings.TrimSpace(sub.ID()) == "" {
     		return
     	}
     	want := strings.ToLower(strings.TrimSpace(sub.ID()))
     	pluginSubsMu.Lock()
     	defer pluginSubsMu.Unlock()
     	for i, existing := range pluginSubs {
     		if existing.ID() == want {
     			pluginSubs[i] = sub
     			return
     		}
     	}
     	pluginSubs = append(pluginSubs, sub)
     }

     func ResetPluginsForTest() {
     	pluginSubsMu.Lock()
     	pluginSubs = nil
     	pluginSubsMu.Unlock()
     }
     ```
   - `Registry()` 返回内置 `[]Subscription{chatGPT{}, openCodeGo{}}` 追加 `pluginSubs`。
   - 将 `provisionRows` 导出为 `ProvisionRows(ctx context.Context, env Env, offers []Offer, source string) ([]string, error)`（内部保持兼容别名 `provisionRows`），供外部插件包直接调用。

5. **`pkg/tui/setup.go`**：
   - 在 `signInPage(login subscription.Login, address, flow string) string`（line 1244）中：
     ```go
     	case subscription.FlowDeviceCode:
     		code := login.UserCode()
     		if address != "" && code != "" {
     			lines = append(lines, "Open "+address+" and enter this code.", "", code)
     		} else if address != "" {
     			lines = append(lines, "Open "+address+" to sign in.")
     		} else if code != "" {
     			lines = append(lines, code)
     		}
     ```

**Verify**:
`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... ./pkg/config/... ./pkg/llm/... ./pkg/subscription/... ./pkg/run/... ./pkg/tui/... -count=1` → 全部 PASS（尤其确认 `pkg/architecture` 零违规）。

---

### Step 3: Web 前端订阅插件接入（`SubscriptionLoginModal.vue`）

1. 在 `frontend/src/components/models/SubscriptionLoginModal.vue` 中引入 `getSubscriptionFrontendPlugin`：
   - 在 `describe(subscription: SubscriptionRow)` 中：
     ```ts
     function describe(subscription: SubscriptionRow): string {
       const plugin = getSubscriptionFrontendPlugin(subscription.id)
       if (plugin?.describe) {
         return plugin.describe(locale.value === 'zh' ? 'zh' : 'en', subscription)
       }
       if (subscription.flows.includes('paste_callback')) return t('models.pasteHint')
       return t('models.deviceHint')
     }
     ```
   - 在 Step 2 的 `flow === 'device_code'` 分支中：
     - 支持 `deviceHintText`：若当前订阅插件定义了 `waitingHint`，优先显示 `plugin.waitingHint(locale)`，否则显示 `t('models.deviceHint')`；
     - 当 `userCode` 非空，或者（`!currentPlugin?.hideEmptyUserCode && !url`）时显示 `<div class="ucode" data-testid="models-login-code">`，而在免短码设备流（如 `qodercn` 设置了 `hideEmptyUserCode: true` 且 `userCode === ''`）时不渲染空的短码框，直接展示「打开授权页面」按钮与等待状态。
2. 在 `frontend/src/components/models/SubscriptionLoginModal.test.ts` 中新增单测：验证当订阅项属于免短码设备流插件时，Step 1 显示插件自定义描述，Step 2 展示打开授权链接按钮且不渲染冗余的空短码占位框。

**Verify**:
`cd frontend && pnpm typecheck && pnpm exec vitest run src/components/models/SubscriptionLoginModal.test.ts` → PASS。

---

### Step 4: 实现闭源插件 `plugins/subscriptions/qodercn` — 加密、签名与身份伪装层（`crypto.go` & `identity.go`）

1. 创建目录 `plugins/subscriptions/qodercn/`。
2. 实现 `plugins/subscriptions/qodercn/crypto.go`：
   - 严格按 **§Current state 2.3** 实现：
     - `CredentialEncrypt(plaintext, key16 string) (string, error)` 与 `CredentialDecrypt(cipherB64, key16 string) (string, error)`（AES-128-CBC，`Key = IV = key16[:16]`，PKCS#7 padding）；
     - `GenerateRuntimeAuthFields(in RuntimeAuthInput) (encryptUserInfo, cosyKey string, err error)`（随机 16 字节 hex key + `CredentialEncrypt` + 内嵌 1024-bit RSA 公钥 `rsa.EncryptPKCS1v15`）；
     - `EncodePayload(plain []byte) string` 与 `DecodePayload(encoded string) ([]byte, error)`（64 字符表 `_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!`，padding `'$'`，首尾 `k = len(s)/3` 互换）；
     - `SignCosyBearer(requestID, encryptUserInfo, cosyKey, cosyDate, encodedBody, signPath string) string`（构造 `{"version":"v1","requestId":...}` Base64，拼 5 行字符串算 MD5）；
     - `DecryptModelCache(encryptedB64, uid string) ([]byte, error)`（校验 `"QMC\x01"` 魔数，HKDF-SHA256 `salt="qoder-model-cache-enc"`, `info="model-cache-v1"` 派生 32B key，AES-256-GCM 解密）。
3. 实现 `plugins/subscriptions/qodercn/identity.go`：
   - 定义客户端伪装常量：`CosyVersion = "1.1.79-hotfix.19"`, `ClientType = "5"`, `BusinessProduct = "cli"`, `BusinessType = "agent"`, `DefaultScene = "app"`, `OAuthClientID = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"`；
   - 实现 `LoadOrCreateMachineIdentity(forebrainHome string) MachineIdentity`：
     - 只读探测 `~/.qoder-cn/.auth/machine_id` 与 `~/.qoder-cn/umid-cache.json`；
     - 若本机存在，直接复用其 `MachineID`、`MachineToken`、`MachineType`；
     - 若不存在，从 `<forebrainHome>/qodercn-machine.json` 读取或生成新的 UUIDv4（绝不向 `~/.qoder-cn` 写入未授权文件）。
   - 实现 `PrepareRequestHeaders(creds Credentials, id MachineIdentity, req PrepareSpec) (url string, headers http.Header, body string, err error)`：
     - 自动将路径补齐 `/algo` 前缀（如 `/api/v2/model/list?Encode=1` → `https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1`）；
     - 完整生成与 `qoder_auth_wasm` 逐字一致的 `Cosy-*` 请求头（`Cosy-Version`, `Cosy-ClientType`, `Cosy-Business-Product`, `Cosy-Business-Type`, `Cosy-Scene`, `Cosy-Data-Policy: agree`, `Cosy-Date`, `Cosy-Key`, `Cosy-MachineId`, `Cosy-MachineToken`, `Cosy-MachineType`, `Cosy-MachineOS`, `Cosy-User`, `Login-Version: v2`，以及非空时的 `Cosy-Organization-Id` / `Cosy-Organization-Tags`）。

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn/... -run 'TestCrypto|TestIdentity' -count=1` → PASS（包含 §Current state 2.3 的全部黄金测试向量断言）。

---

### Step 5: 实现闭源插件 `plugins/subscriptions/qodercn` — 登录、本机凭证复用与模型目录（`auth.go` & `models.go`）

1. 实现 `plugins/subscriptions/qodercn/auth.go`：
   - 凭证存储：优先读写 Forebrain 状态库 `fb_config` 的 `"auth.qodercn"` section（通过 `state.ConfigStore{DB: db}`），同时回退 `<forebrainHome>/qodercn.json`（权限 `0600`）。
   - 本机凭证自动导入探测（`tryAdoptLocalQoderCNCredentials`）：
     - 当 Forebrain 尚未存储 `auth.qodercn` 时：
       1) 检查环境变量 `QODER_PERSONAL_ACCESS_TOKEN`：若非空，调 `/api/v1/jobToken/exchange` 兑换并补全 `/api/v1/userinfo`；
       2) 检查 `~/.qoder-cn/.auth/user` + `~/.qoder-cn/.auth/machine_id`：若存在，用 `CredentialDecrypt(encrypted, machineID[:16])` 解密并校验未彻底失效；
       3) 若解密出的凭证无 `encrypt_user_info` 或 `key`，调用 `GenerateRuntimeAuthFields` 生成并保存至 Forebrain 的 `auth.qodercn`。
   - 浏览器 PKCE 轮询登录（`LoginPKCEDeviceFlow`）：
     - 按 **§Current state 2.2** 生成 `verifier`, `challenge`, `nonce`；
     - 通过 `onURL` 回调立即发布 `https://qoder.cn/device/selectAccounts?...`；
     - 每 1s 轮询 `GET https://gateway.qoder.com.cn/api/v1/deviceToken/poll?nonce=...&verifier=...&challenge_method=S256`（404 继续，200 成功）；
     - 拿到 token 后调用 `GET https://gateway.qoder.com.cn/api/v1/userinfo` 补全用户信息，生成 `encrypt_user_info` 与 `key`，保存至 `auth.qodercn`。
   - Token 自动刷新（`EnsureFreshCredentials`）：
     - 使用 `llm.CredentialMutex` 加锁；当 `ExpiresAt` 剩余 ≤60s 时调用 `/api/v1/deviceToken/refresh`（或 `/api/v1/jobToken/refresh`），重新生成 `encrypt_user_info` 与 `key` 并保存。

2. 实现 `plugins/subscriptions/qodercn/models.go`：
   - 实现 `FetchModels(ctx, env)`：
     1) 使用当前有效凭证调用 `GET https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1`（若返回 401/403 则强制刷新一次 Token 后重试一次）；
     2) 用 `DecodePayload` 解密响应体，解析 `app`（或 `chat`/`assistant`）场景下的模型列表；
     3) 若本机存在 `~/.qoder-cn/.models/<uid>/catalog-v7` 且网络不可达，支持通过 `DecryptModelCache` 读取本地缓存；
     4) 将每个模型转换为 `llm.Model`（含 `ReasoningOptions` 思考强度档位、`ContextWindow`、`DefaultMaxTokens: 32000`、`SupportsAttachments`）并调用 `llm.SetBuiltinModels("qodercn", models)` 更新内存能力表；
     5) 同时转换为 `[]subscription.Offer`（`Subscription: "qodercn"`, `Provider: "qodercn"`, `BaseURL: "https://gateway.qoder.com.cn"`, `APIPath: "/algo/api/v2/service/pro/sse/agent_chat_generation"`，默认勾选 `qmodel_latest`、`qmodel_38max`、`auto`）。

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./plugins/subscriptions/qodercn/... -run 'TestAuth|TestModels' -count=1` → PASS（使用 `httptest.Server` 模拟 `selectAccounts`、`deviceToken/poll`、`userinfo`、`deviceToken/refresh`、加密的 `/algo/api/v2/model/list?Encode=1` 以及真实 `catalog-v7` 密文块解密）。

---

### Step 6: 实现闭源插件 `plugins/subscriptions/qodercn` — 推理传输层伪装与插件注册（`transport.go` & `plugin.go` & `frontend/index.ts`）

1. 实现 `plugins/subscriptions/qodercn/transport.go`：
   - 定义 `Transport` 实现 `http.RoundTripper` 及 `SetBase(http.RoundTripper)`、`OwnsUserAgent() bool { return true }`。
   - 在 `RoundTrip(req *http.Request)` 中：
     1) 调用 `EnsureFreshCredentials` 获取有效 `Credentials`（含 `UID`, `AccessToken`, `EncryptUserInfo`, `CosyKey`, `OrganizationID`, `OrganizationTags`）；
     2) 读取 `go-openai` 构造的请求 JSON（`messages`, `tools`, `tool_choice`, `max_tokens`/`max_completion_tokens`, `temperature`, `reasoning_effort`, `enable_thinking`）；
     3) 从 `req.Context()` 提取 `llm.AgentSessionIDFromContext(req.Context())`（若为空则生成 UUIDv4 作为 `session_id`），生成新的 `request_id` UUIDv4；
     4) 将 `messages` 中 `role == "system"` 的消息提取到顶层 `system` 字段（同时保留在 `messages` 开头，与 `kqe` 行为完全一致），构造 `RemoteChatAsk` 对象（含 `chat_task: "FREE_INPUT"`, `chat_context`, `is_reply: true`, `is_retry: false`, `source: 1`, `version: "3"`, `agent_id: "agent_common"`, `task_id: "common"`, `session_type: "qoderclicn"`, `model_config`, `parameters`）；
     5) 调用 `EncodePayload` 将 `RemoteChatAsk` JSON 编码为 `encodedBody`；
     6) 调用 `PrepareInferRequest` 生成目标 URL `https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1` 及全套 `Cosy-*` 头、`X-Model-Key`、`X-Model-Source: system`、`Cosy-MachineHostname`、`Authorization: Bearer COSY.<b64>.<sig>`；
     7) 发送请求；若返回 401/403，触发一次 `ForceRefreshCredentials` 后重签重发；
     8) 对返回的 `200 OK` `text/event-stream` 响应体，通过 `unwrapQoderSSEStream(resp.Body)` 解包 `{ "statusCode": "OK", "statusCodeValue": 200, "body": "<chunk_json>" }` 信封，过滤 `[NOT_EXCEED_QUOTA]` / `[NOTIFICATIONS]` 心跳帧，将内部标准 `chat.completion.chunk` 作为 `data: <chunk_json>\n\n` 流式传给上层 `compat_llm`。

2. 实现 `plugins/subscriptions/qodercn/plugin.go`：
   - 实现 `subscription.Subscription` 接口：
     - `ID() string { return "qodercn" }`
     - `Label() (en, zh string) { return "Qoder CN", "Qoder CN" }`
     - `Flows() []string { return []string{subscription.FlowBrowserCallback, subscription.FlowDeviceCode} }`
     - `Probe(ctx, env)`：检查凭证并验证模型目录可用性（返回 `turn.SignInUsable` / `SignInMissing` / `SignInDead` / `SignInUnreachable`）；
     - `Start(ctx, env, flow)`：启动 `LoginPKCEDeviceFlow`；
     - `Offers(ctx, env)`：返回解锁的模型 `[]subscription.Offer`；
     - `Provision(ctx, env, offers, opts)`：调用 `subscription.ProvisionRows(ctx, env, offers, "qodercn")`；
     - `RefreshKey(ctx, env, modelID)`：返回 `subscription.ErrNoKeyToRefresh`。
   - 在 `func init()` 中：
     1) 调用 `llm.SetBuiltinModels("qodercn", defaultQoderModels())` 预装 13 个模型的上下文窗口、最大输出与思考强度档位；
     2) 调用 `config.RegisterProviderPlugin(...)` 注册 `qodercn`（`LoginBacked: true`, Endpoint: `https://gateway.qoder.com.cn`, APIPath: `/algo/api/v2/service/pro/sse/agent_chat_generation`, Hint: `"Use your Qoder CN subscription"`）；
     3) 调用 `llm.RegisterProviderFactory("qodercn", newQoderCNLLM)` 注册 LLM 客户端工厂（内部调用 `openai.NewCompatLLMWithTransport` 挂载 `*qodercn.Transport`）；
     4) 调用 `subscription.Register(qoderCNSubscription{})` 注册订阅项。

3. 实现 `plugins/subscriptions/qodercn/frontend/index.ts`：
   ```ts
   import type { SubscriptionFrontendPlugin } from '../../../../frontend/src/plugins/subscriptionPlugin'

   const qodercnPlugin: SubscriptionFrontendPlugin = {
     id: 'qodercn',
     describe: (locale) =>
       locale === 'zh'
         ? '打开浏览器授权登录 Qoder CN 订阅账号，自动同步可用模型'
         : 'Authorize your Qoder CN subscription in the browser',
     waitingHint: (locale) =>
       locale === 'zh'
         ? '点击下方按钮在浏览器中确认登录 Qoder CN，授权完成后将自动跳转。'
         : 'Click the button below to confirm sign-in in your browser. This page will advance automatically.',
     hideEmptyUserCode: true,
   }

   export default qodercnPlugin
   ```

**Verify**:
`./scripts/gen-subscription-plugins.sh && export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go test -tags fts5 -race ./plugins/subscriptions/qodercn/... -count=1` → PASS。

---

### Step 7: 本地执行 `npm/scripts/build-platform-packages.sh` 完整编译打包与端到端验证

1. 运行 `./scripts/gen-subscription-plugins.sh`，确认生成：
   - `cmd/forebrain/plugins_local_gen.go`（导入 `_ "github.com/forebrain-harness/forebrain-harness/plugins/subscriptions/qodercn"`）
   - `frontend/src/plugins/local_subscriptions.gen.ts`（导入 `qodercnPlugin`）
2. 执行 `npm/scripts/build-platform-packages.sh`（按仓库规矩构建包含最新 Web UI 与 Qoder CN 插件的本机平台包）并在 `npm/` 下确认产物：
   - 前端 `make ui` 编译通过；
   - Go 二进制 `forebrain` 编译通过，且内置 `qodercn` 插件；
   - `git status --short` 确认 `/plugins/`、`cmd/forebrain/plugins_local_gen.go`、`frontend/src/plugins/local_subscriptions.gen.ts` 均被 `.gitignore` 忽略，未出现在 untracked 列表中！

**Verify**:
`npm/scripts/build-platform-packages.sh && git status --ignored --short plugins/ cmd/forebrain/plugins_local_gen.go frontend/src/plugins/local_subscriptions.gen.ts` → 构建 exit 0，且三个路径均带 `!!`（已被 `.gitignore` 忽略）。

---

## Test plan

1. **宿主插件机制单测**：
   - `pkg/config/provider_registry_test.go`：`TestRegisterProviderPluginMarksLoginBackedAndCredentialsBacked` —— 验证动态注册的 `LoginBacked` provider 在 `BuiltinProviders()`、`BuiltinProviderByID()`、`CredentialsBackedProvider()`、`NormalizeLLMModelIdentity()` 中均生效。
   - `pkg/llm/catalog_test.go`：`TestRegisterProviderFactoryAndBuiltinModels` —— 验证动态注册的 provider factory 在 `BuildProviderLLM()`、`LoginBackedProvider()`、`KnownProvider()`、`ReasoningEfforts()` 中均生效。
   - `pkg/subscription/registry_test.go`：`TestRegisterPluginSubscriptionAppendsInOrderAndDeduplicates` —— 验证多插件（模拟 `qodercn`、`zcode`、`workbuddy`）注册、去重、`ByID` 查找与 `ProvisionRows` 落库。
   - `frontend/src/components/models/SubscriptionLoginModal.test.ts`：验证前端插件 `describe`、`waitingHint` 与 `hideEmptyUserCode` 行为。
2. **闭源 Qoder CN 插件单测（`plugins/subscriptions/qodercn/qodercn_test.go`）**：
   - `TestWasmCryptoVectors`：逐字节比对 `CredentialEncrypt/Decrypt`、`EncodePayload/DecodePayload`（0..100 全长度）、`SignCosyBearer`（GET 与 POST 两条黄金向量）、`DecryptModelCache`（含本机真实格式 `QMC\x01` 向量）。
   - `TestLoginPKCEDeviceFlowAndUserInfo`：`httptest.Server` 模拟 `deviceToken/poll`（先 404 后 200）与 `/api/v1/userinfo`，断言请求 URL 参数（`challenge`, `challenge_method=S256`, `nonce`, `machine_id`, `client_id`）与 `Cosy-*` 头完整无误。
   - `TestTokenRefreshAndConcurrentLock`：模拟即将过期的 `device_token` 与 `job_token` 自动刷新，断言并发请求只触发一次刷新且更新 `encrypt_user_info` 与 `key`。
   - `TestFetchModelsDecodesPayloadAndRegistersCaps`：模拟 `/algo/api/v2/model/list?Encode=1` 加密响应，断言解出 13 个模型且 `llm.ReasoningEfforts("qodercn", "qmodel_38max")` 返回 `["low", "medium", "xhigh"]`。
   - `TestTransportRewritesToRemoteChatAskAndUnwrapsSSE`：通过 `NewLLMFromYAML` 构造 `qodercn/qmodel_38max` 客户端，发送包含 `system`、`user`、`tools` 的请求，在 `httptest.Server` 端解码 `Encode=1` 请求体，断言 `RemoteChatAsk` JSON 结构、`Authorization: Bearer COSY...` 签名、`X-Model-Key: qmodel_38max` 与 `Cosy-*` 头 100% 匹配官方客户端；返回 `{ "statusCode": "OK", "statusCodeValue": 200, "body": "..." }` 包装的 SSE 流与 `[NOT_EXCEED_QUOTA]` 心跳，断言客户端正确解包出正文、思考内容（`reasoning_content`）、工具调用（`tool_calls`）与 token 用量（`usage`）。

---

## Done criteria

全部判据必须机器可检且同时满足：

- [ ] `./scripts/gen-subscription-plugins.sh` 退出码为 0
- [ ] `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/... -count=1` 退出码为 0（架构层、扇出预算、包数上限全部通过）
- [ ] `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" && CGO_ENABLED=1 go test -tags fts5 -race ./pkg/config/... ./pkg/llm/... ./pkg/subscription/... ./pkg/run/... ./pkg/gateway/... ./pkg/tui/... ./plugins/subscriptions/qodercn/... -count=1` 退出码为 0
- [ ] `cd frontend && pnpm typecheck && pnpm exec vitest run src/components/models/SubscriptionLoginModal.test.ts` 退出码为 0
- [ ] `npm/scripts/build-platform-packages.sh` 退出码为 0，生成本机平台二进制
- [ ] `grep -rn -i "qoder" pkg/ cmd/forebrain/*.go frontend/src/ --exclude="plugins_local_gen.go" --exclude="local_subscriptions.gen.ts"` 输出为空（开源宿主代码零闭源插件名泄露）
- [ ] `git status --ignored --short plugins/ cmd/forebrain/plugins_local_gen.go frontend/src/plugins/local_subscriptions.gen.ts` 每一行均以 `!! ` 开头（闭源插件与生成文件全部被 `.gitignore` 忽略，绝不入库）
- [ ] 全仓与计划文件中不包含任何真实 token / key 明文

---

## STOP conditions

遇到以下任一情况立即停止并报告，不得自行发挥：

- `pkg/architecture` 测试因任何原因失败（例如误在 `pkg/` 下新建了子目录或增加了包扇出）。
- `Current state` 中引用的宿主文件发生实质性结构冲突，导致注册表接口无法按计划挂载。
- 在任何日志、测试或文件中意外打印出本机真实 `access_token`、`refresh_token` 或 `security_oauth_token` 明文。

---

## Maintenance notes

- **新增未来闭源订阅插件（如 `zcode`、`workbuddy`）的标准步骤**：
  1. 创建 `plugins/subscriptions/<id>/plugin.go`（在 `init()` 中调用 `config.RegisterProviderPlugin`、`llm.SetBuiltinModels`、`llm.RegisterProviderFactory`、`subscription.Register`）与可选的 `plugins/subscriptions/<id>/frontend/index.ts`；
  2. 执行 `npm/scripts/build-platform-packages.sh`（内部自动运行 `scripts/gen-subscription-plugins.sh` 将新插件编入前后端）；
  3. 开源宿主代码无需修改一行。
- **审查重点**：
  - 审查 `plugins/subscriptions/qodercn/crypto.go` 中的 `SignCosyBearer` 与 `EncodePayload` 是否保持与 WASM 黄金向量完全一致；
  - 确认任何操作都不会写入或删除 `~/.qoder-cn/.auth/user` 及 `/Users/doudou/Library/Application Support/com.qodercn.app.stable/auth.v1.dat`，保证本机正在运行的 Qoder CN 客户端任务零受扰。
