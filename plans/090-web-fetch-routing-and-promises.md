# Plan 090: `web_fetch` 的路由与承诺对齐——内网 URL 不外发、大页面不变成 JSON 语法错、卡片不再许下两个假的保证

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- pkg/tool/web_tools.go pkg/tui/overlays.go frontend/src/lib/approvalGates.ts frontend/src/locales/index.ts`
> 若有变动，先把「Current state」的摘录与现码对照；对不上按 STOP 处理。

## Status

- **Priority**: P1（安全项在前两条）
- **Effort**: M
- **Risk**: MED
- **Depends on**: none（与 087 都改 `frontend/src/lib/approvalGates.ts` 时串行；建议 087 先做）
- **Category**: security
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

`agents.defaults.web_fetch.provider: deepseek` 把抓取交给第三方 API 完成。这条路径绕过了直连路径唯一的强判定——
拨号时的私网检查——于是 `allow_private_ip: true` 时，模型（或提示词注入）构造的 `http://localhost:…`、
`http://10.x/…`、带签名 token 的内网 URL 会被**完整的 URL（含 query 里的凭据）POST 到 api.deepseek.com**：
内容取不回来，URL 已经出网。这与该开关的语义（"让内置抓取器访问回环与 RFC1918"）相反。同一路径还有两个问题：
超过约 2.06 MiB 的页面因为读限被静默截断，`json.Unmarshal` 失败后当成工具错误抛给模型（"unexpected end of JSON input"），
而直连路径对同一页面是"截断 + 落盘可续读"；审批卡的 Explain 折行对用户保证"跳转只到同一个 host""私网 URL 一定失败"，
这两条在 DeepSeek 路由下、或 `allow_private_ip` 打开时都不成立，且 `allow_private_ip` 在两端都**没有任何显示点**。
修完之后：内网目标在路由模式下被拒绝并说明原因、超大页面得到可解释的结果、卡片只承诺它真做得到的事。

## Current state

- `pkg/tool/web_tools.go:196-222` — 门禁与载荷：

```go
allowPrivate := webFetchAllowPrivateIP(rt)
u, err := fetchURLAllowed(raw, allowPrivate)
...
gated := ShouldWebFetchRequestPermission(u.String()) || allowPrivate
if hook := st.ActionHook(); hook != nil && gated {
	if ApprovedActionIDFromContext(ctx) == "" && !PolicyApprovedFromContext(ctx) {
		payload := WebFetchPermissionPayload(u.String())
		if allowPrivate {
			payload["allow_private_ip"] = true
		}
```

- `pkg/tool/web_tools.go:229-260` — DeepSeek 分支（本计划要在这里加判定，并把路由事实写进载荷）：

```go
} else if dok {
	// Explicit provider routing: the permission gate and the SSRF checks above
	// have already run for the target URL, ...
	client := &http.Client{Timeout: 30 * time.Second}
	res, truncated, derr := webFetchDeepSeekPOST(ctx, client, endpoint, dkey, u.String(), maxB)
```

  注释里的前提是错的：`fetchURLAllowed` 只判**字面**主机（见下），真正的私网判定发生在直连拨号层，路由分支不拨号。

- `pkg/tool/web_tools.go:1881-1902` — 判定的真实形状：

```go
func webFetchHostAllowed(host string, allowPrivate bool) error {
	...
	if webFetchBlockedHostNames[host] { return fmt.Errorf("cloud metadata hosts are not allowed") }
	if ip := net.ParseIP(host); ip != nil { return webFetchIPAllowed(ip, allowPrivate) }
	if allowPrivate { return nil }            // ← 非字面 host 在放开私网时一律通过
	if host == "localhost" || strings.HasSuffix(host, ".localhost") { ... }
	return nil
}
```

  `pkg/tool/web_tools.go:1904-1922` 的 `webFetchIPAllowed` 注释写明"判据必须是拨号要连的那个地址，因为
  hostname 可以解析到内网、十进制/十六进制字面量根本不解析成 IP"——路由分支没有这一层。

- `pkg/tool/web_tools.go:365-402` — `webFetchDeepSeekPOST`：读限 2 MiB + 64 KiB，`ReadAll` 不报错，随后 `json.Unmarshal`：

```go
body, err := io.ReadAll(io.LimitReader(resp.Body, int64(defaultWebFetchMaxBytes)+65536))
if err != nil { return deepSeekFetchResult{}, false, err }
...
if err := json.Unmarshal(body, &parsed); err != nil {
	return deepSeekFetchResult{}, false, err
}
```

  常量：`defaultWebFetchMaxBytes = 2 << 20`（`pkg/tool/web_tools.go:77`）。CJK 页面在 JSON 里以 `\uXXXX` 转义时体积会翻倍以上，
  所以一个几百 KB 的中文文档就足以越过这个读限。

- `pkg/tool/web_tools.go:310-316` — 直连路径的正确形状（本计划的基准）：`io.LimitReader(..., maxB+1)` +
  `truncated := len(body) > maxB` + `body = body[:maxB]`；随后 `pkg/tool/web_tools.go:443-476` 的
  `marshalWebFetchResult` 负责"内联裁剪 + 剩余内容落盘 + `full_path` 续读指针"。

- `pkg/tui/overlays.go:675-679` — Explain 折行里的两句承诺（由 `09277a1` 引入）：

```go
} else if isApprovalWebFetch(o.req) {
	writeLine("  %s", approvalWebFetchURL(o.req))
	writeLine("Access: Public web")
	writeLine("Redirects: only to the same host; another host stops the fetch and asks again")
	writeLine("Fails: authenticated and private URLs")
```

  直连路径真有跳转约束（`pkg/tool/web_tools.go:280-308` 的 `CheckRedirect` + `webFetchRedirectCovered`），
  DeepSeek 路由由服务端抓取，客户端拦不住任何跳转。`allow_private_ip` 这个载荷键在**两端都没有渲染点**
  （`grep -rn allow_private_ip --include=*.go --include=*.vue --include=*.ts` 只命中 config/test/`web_tools.go`）。

- `frontend/src/locales/index.ts:1461` — web 端提问句也断言"公开网络"：
  `'chat.webFetchQuestion': '要我向公开网络取这个页面吗？'`（en `:3208`）。

- 文档（可选步骤）：`forebrain-harness.github.io/docs/config/agents-and-models.md:352` 与中文镜像 `:347`
  目前只写"允许内置抓取器访问回环与 RFC1918 主机"，没有说明路由模式下拿不到私网目标。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 单包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | `ok` |
| Go 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/config -count=1` | 全 `ok` |
| vet | `go vet ./pkg/tool ./pkg/tui` | 无输出 |
| 前端单测 | `cd frontend && pnpm test -- src/lib/approvalGates.test.ts src/composables/usePendingActions.test.ts` | 全 passed |
| 前端全量 | `cd frontend && pnpm test` | 全 passed |
| 类型检查 | `cd frontend && pnpm exec vue-tsc --noEmit` | exit 0 |
| e2e | `FOREBRAIN_E2E_SPEC="e2e/approval-families.spec.ts e2e/message-cards.spec.ts" scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**：
- `pkg/tool/web_tools.go`
- `pkg/tool/web_tools_test.go`
- `pkg/tui/overlays.go`（只改 web_fetch 的 Explain 折行）+ `pkg/tui/overlays_test.go`
- `frontend/src/lib/approvalGates.ts` + `approvalGates.test.ts`（web_fetch 族的事实行）
- `frontend/src/locales/index.ts`（只改 web_fetch 的键）
- `forebrain-harness.github.io/docs/config/agents-and-models.md` 与 `docs/zh/config/agents-and-models.md`（**仅** `allow_private_ip` 那一行）

**Out of scope**：
- **不要**让 DeepSeek 路由变成默认：`provider` 永远只有显式设置才生效（`pkg/tool/web_tools.go:60-62` 的现状是对的）。
- 不要改 `webFetchIPAllowed` 的策略（metadata/link-local 永远拦）——本计划只把它的判据延伸到路由分支。
- 直连路径的跳转/裁剪/落盘语义：已经正确，改它就是扩大风险面。
- `frontend/src/components/chat/ApprovalGateCard.vue` 的渲染：事实行通过 `approvalFacts` 提供，无需改组件。
- `pkg/config` 的开关语义：本计划不改配置字段，只让行为与文档一致。

## Git workflow

- 直接改当前 `dev` 工作区；**不 commit、不 push**，除非派发方明确要求。
- 若要求提交：`fix(tool): …`，body 用 `Why:` / `What:` / `Verification:`，`git commit -s`。

## Steps

### Step 1: 路由模式下拒绝私网 / 回环目标（安全项，先做）

`pkg/tool/web_tools.go`：在进入 DeepSeek 分支**之前**判定目标是否私网，并且**不看** `allowPrivate`——
第三方服务不可能"访问你的内网"，放开私网只对本地抓取有意义。

新增一个只给路由用的检查（放在 `webFetchHostAllowed` 附近）：

```go
// webFetchRoutableTarget refuses a target a server-side fetcher cannot reach,
// or must not be told about: loopback/RFC1918/ULA/link-local addresses and
// names that resolve to them. allow_private_ip grants the local dialer, never a
// third party, so this check ignores it on purpose: the URL itself is what
// leaves the machine.
func webFetchRoutableTarget(host string) error {
	h := strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(h); err == nil { /* keep the host part */ _ = h }
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return webFetchIPAllowed(ip, false)
	}
	addrs, err := net.DefaultResolver.LookupHost(context.Background(), strings.Trim(h, "[]"))
	if err != nil {
		return nil // 解析不了就不在这里拦：出网的是名字，且服务端也取不到；真正错误由 provider 报告
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil { continue }
		if err := webFetchIPAllowed(ip, false); err != nil { return err }
	}
	return nil
}
```

（`net.DefaultResolver.LookupHost` 需要 `net` 与 `context`；两者 `web_tools.go` 已经引入，按实际 import 情况补。
若 `webFetchIPAllowed(ip,false)` 对公网地址返回 `nil`、对私网/回环返回错误，则直接照上面的写法即可；先用现存的测试
`pkg/tool/request_permissions_test.go:3036-3053` 的用例确认它的语义。）

然后在 `resolveWebFetchDeepSeek` 返回 `dok == true` 的分支里，**在** `webFetchDeepSeekPOST` 之前：

```go
if rerr := webFetchRoutableTarget(u.Hostname()); rerr != nil {
	return "", fmt.Errorf("web_fetch: provider %q cannot fetch %s: %w — use the built-in fetcher (agents.defaults.web_fetch.provider is unset) for private addresses",
		"deepseek", u.Hostname(), rerr)
}
```

错误文案要求：说清"第三方抓取器够不到内网"，并指出改用内置抓取器的做法；不要显示端点内部名以外的密钥信息。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestWebFetchDeepSeek|TestWebFetchPrivate' -count=1` →
见 Step 4 的新用例（先加用例再实现也可以，红→绿更好）。

### Step 2: 载荷带上路由与"私网已放开"两个事实

1. 在 DeepSeek 分支决定之后（即 `dok == true` 时），把路由事实写进门禁载荷。因为门禁在分支**之前**执行，
   最小改法是把门禁判定挪到分支选择之后，或在取得 `dok` 后把 `payload["routed_provider"] = "deepseek"` 写进
   已经创建不了的载荷——具体做法：把 `resolveWebFetchDeepSeek` 的调用提到门禁**之前**，用它的 `dok` 填充载荷：

```go
endpoint, dkey, dok, derr := resolveWebFetchDeepSeek(rt)
if derr != nil { return "", derr }
if hook := st.ActionHook(); hook != nil && gated {
	if ApprovedActionIDFromContext(ctx) == "" && !PolicyApprovedFromContext(ctx) {
		payload := WebFetchPermissionPayload(u.String())
		if allowPrivate { payload["allow_private_ip"] = true }
		if dok { payload["routed_provider"] = "deepseek" }
		...
```

   `resolveWebFetchDeepSeek` 是纯读配置 + 校验自己的端点，提前调用不产生副作用（已核：
   `pkg/tool/web_tools.go:55-74`）。**不要**因此改变门禁的触发条件（`gated` 保持原样）。
2. `pkg/tui/overlays.go` 的 web_fetch Explain 折行按载荷分支：

```go
} else if isApprovalWebFetch(o.req) {
	writeLine("  %s", approvalWebFetchURL(o.req))
	routed, private := approvalWebFetchRoute(o.req)   // 新助手：读 payload 的 routed_provider / allow_private_ip
	if routed != "" {
		writeLine("Fetched by: %s (server-side; redirects and private addresses are not controlled locally)", routed)
	} else {
		writeLine("Access: Public web")
		writeLine("Redirects: only to the same host; another host stops the fetch and asks again")
	}
	if private {
		writeLine("Fails: authenticated URLs (private addresses are allowed by agents.defaults.web_fetch.allow_private_ip)")
	} else {
		writeLine("Fails: authenticated and private URLs")
	}
```

  新增 `approvalWebFetchRoute(req turn.ToolApprovalRequest) (routed string, private bool)`：解析 `req.ToolInputJSON`，
   读 `routed_provider`（string）与 `allow_private_ip`（bool）。

**Verify**: `go vet ./pkg/tui` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestWebFetchOverlay -count=1` → `ok`。

### Step 3: 大页面不再变成 JSON 语法错

`pkg/tool/web_tools.go` 的 `webFetchDeepSeekPOST`：

```go
const webFetchDeepSeekMaxBodyBytes = 8 << 20   // 读限：给 provider 的响应体一个明确上限
...
lim := io.LimitReader(resp.Body, int64(webFetchDeepSeekMaxBodyBytes)+1)
body, err := io.ReadAll(lim)
if err != nil { return deepSeekFetchResult{}, false, err }
if len(body) > webFetchDeepSeekMaxBodyBytes {
	// Not a parse failure: the provider's response itself is past what this
	// client reads. Say so, in the model's terms, instead of surfacing
	// "unexpected end of JSON input" as a tool error.
	res.oversized = true
	return res, true, nil
}
```

并在调用侧（`pkg/tool/web_tools.go:229-260` 的分支）对 `res.oversized` 返回结构化结果而不是错误：

```go
out := map[string]any{
	"final_url":   u.String(),
	"status":      res.status,
	"truncated":   true,
	"error":       "provider_response_too_large",
	"body":        fmt.Sprintf("The %s response for this page exceeded the %d MiB the client reads, so no page text could be parsed. Fetch a narrower URL (a section anchor or an alternate host) instead.", "deepseek", webFetchDeepSeekMaxBodyBytes>>20),
}
return marshalWebToolOutput(out), nil
```

要求：
- 不要为了"能解析"把正文改成字符串处理（分段解析 JSON 字符串不值得）；
- `truncated: true`，让上游的"是否截断"语义一致；
- 错误句必须可行动（告诉模型换更窄的 URL），不要出现 Go 的 JSON 错误文本。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestWebFetchDeepSeekOversizedResponse -count=1` → `ok`（用例见 Step 4）。

### Step 4: Go 用例

`pkg/tool/web_tools_test.go`（现有 `deepseek` 相关用例就在这个文件，沿用它的 `httptest` + 配置注入写法）：

1. `TestWebFetchDeepSeekRefusesPrivateTarget`（表驱动）：`http://localhost:8080/x`、`http://127.0.0.1/x`、
   `http://10.1.2.3/x`、`http://[::1]/x`，在 `provider=deepseek` **且** `allow_private_ip=true` 下都必须返回错误，
   且错误里出现 "cannot fetch"；同时断言**没有**发生任何对 provider 端点的请求（用一个计数器 server）。
2. `TestWebFetchDeepSeekAllowsPublicTarget`：`http://198.51.100.7/x`（TEST-NET-2，公网字面量）在同样配置下会请求 provider。
3. `TestWebFetchDeepSeekOversizedResponse`：provider 返回一个 > 8 MiB 的合法 JSON 之外的大响应（例如 `{"content":"` + 9 MiB 的 `a`），
   断言工具返回的是输出 JSON（不是 error），其中 `error == "provider_response_too_large"`、`truncated == true`。
4. `TestWebFetchPayloadNamesRouting`：在 `provider=deepseek` + `allow_private_ip=true` 下捕获门禁载荷，
   断言 `routed_provider == "deepseek"` 且 `allow_private_ip == true`；在默认配置下两者都不存在。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`。

### Step 5: TUI 用例

`pkg/tui/overlays_test.go` 加一条：web_fetch 审批载荷带 `routed_provider:"deepseek"` 时，
Explain 折行里**没有** "Redirects: only to the same host"、**有** "Fetched by: deepseek"；
带 `allow_private_ip:true` 时 `Fails:` 那行不再说 "private URLs"。用同文件现有的 dump 断言写法
（`grep -n "Explain" pkg/tui/overlays_test.go | head`）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestWebFetch -count=1` → `ok`。

### Step 6: web 端提问句与事实行不再断言"公开网络"

1. `frontend/src/locales/index.ts`：`chat.webFetchQuestion` 改成不断言"公开"的句子——
   zh `'要我去取这个页面吗？'`、en `'Fetch this page?'`。若 `chat.webFetchRemember`（`'不再询问 {host}'`）仍准确，保持不变。
2. `frontend/src/lib/approvalGates.ts` 的 `web_fetch` 族：现在它被 `approvalFacts` 的 `default` 分支转发（`facts: []`）。
   增加两条**仅在载荷带相应字段时**出现的事实：
   - `routed_provider` 非空 ⇒ `t('chat.webFetchRoutedFact', { provider })`（zh `'由 {provider} 在服务端取回'`）；
   - `allow_private_ip === true` ⇒ `t('chat.webFetchPrivateFact')`（zh `'私网地址已放开'`）。
   新增这两个键（zh/en 成对）。注意 `approvalFamily` 仍把 web_fetch 判定为 `web_fetch`，卡片形状不变。
3. 用例：`approvalGates.test.ts` 加两条（有/无上述字段），断言事实行的出现与缺席。

**Verify**: `cd frontend && pnpm test -- src/lib/approvalGates.test.ts` → 全 passed；`pnpm exec vue-tsc --noEmit` → exit 0。

### Step 7: 文档（两行）

`forebrain-harness.github.io/docs/config/agents-and-models.md:352` 的 `allow_private_ip` 行改成说明其边界，例如
"Lets the built-in fetcher reach loopback and RFC1918 hosts. A routed provider (`provider: deepseek`) cannot reach them and refuses such targets."
中文镜像 `docs/zh/config/agents-and-models.md:347` 同步。**只改这两个表格行**，不要碰同文件其它内容。

**Verify**: `git diff --stat -- forebrain-harness.github.io` → 只这两行变化（每个文件 1 行）。

### Step 8: 全量回归

**Verify**:
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/config -count=1` → 全 `ok`。
2. `go vet ./pkg/tool ./pkg/tui` → 无输出。
3. `cd frontend && pnpm test` → 全 passed；`pnpm exec vue-tsc --noEmit` → exit 0。
4. `FOREBRAIN_E2E_SPEC="e2e/approval-families.spec.ts e2e/message-cards.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。

## Test plan

见 Step 4 / 5 / 6。参照用例：`pkg/tool/web_tools_test.go` 里现有的 deepseek 用例（`grep -n "deepseek" pkg/tool/web_tools_test.go | head`）；
`pkg/tool/request_permissions_test.go:2915-2975` 展示了"私网目标被拦 / 放开后可达"的断言风格。

反向验证（必须做）：把 Step 1 的路由判定注释掉，`TestWebFetchDeepSeekRefusesPrivateTarget` 必须变红；恢复后再跑。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/config -count=1` 全 `ok`
- [ ] `go vet ./pkg/tool ./pkg/tui` 无输出
- [ ] `cd frontend && pnpm test` exit 0；`pnpm exec vue-tsc --noEmit` exit 0
- [ ] `grep -n "webFetchRoutableTarget" pkg/tool/web_tools.go` → 命中（定义 + 调用各一处）
- [ ] `grep -n "routed_provider" pkg/tool/web_tools.go pkg/tui/overlays.go frontend/src/lib/approvalGates.ts` → 三处命中
- [ ] `grep -n "Public web" pkg/tui/overlays.go` → 只在直连分支里出现（1 处）
- [ ] `FOREBRAIN_E2E_SPEC="e2e/approval-families.spec.ts e2e/message-cards.spec.ts" scripts/acceptance/web_e2e.sh` → PASS
- [ ] `git status --porcelain` 只含 Scope 列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- `webFetchIPAllowed(ip, false)` 对公网地址并不返回 `nil`（例如它把"未知"也拦住）——那说明它不能如本计划假设那样复用，
  停下来报告，不要自己写第二套 IP 判定。
- `net.DefaultResolver.LookupHost` 在测试环境不可用（CI 无网）：改为只判字面 IP 与 `localhost`，
  并把"名字解析到内网"的情形写进 Maintenance notes 作为已知缺口——但**必须**在报告里说明这一点。
- 门禁载荷的提前构造会让某条既有审批流出现第二次询问（同一个 URL 被问两次）——停下来报告。
- e2e 因为提问句文案变化而红，且修法是放宽断言（而不是更新期望文案）。
- 任一 Verify 连试两次仍失败。

## Maintenance notes

- **路由 = 出网**：任何"把目标 URL 交给第三方"的路径都必须先过"公网可解析"这关；`allow_private_ip` 只对本地拨号有意义。
  新增 provider 时照抄 Step 1 的判定，不要只依赖 `fetchURLAllowed`。
- 载荷里的 `routed_provider` / `allow_private_ip` 是审批卡唯一的事实来源；两端渲染都读它们，不要在渲染层再推断 provider。
- 读限（8 MiB）与 `webFetchMaxMarkdownBytes`（28 KiB 内联）是两件事：前者防解析失败，后者防工具输出过载；
  改任一个都要看一眼 `marshalWebFetchResult` 的落盘/续读逻辑（`pkg/tool/web_tools.go:443-476`）。
- 评审要点：路由模式下**没有发出**任何 provider 请求（用例 1 的计数器）；拒绝时的错误文案给出可行动做法；
  直连路径的跳转约束一字未动。
- 已知缺口（本计划不修）：路由模式下仍无法约束 DeepSeek 侧的跳转行为——我们只能如实说出这一点，
  要真正约束就得让 draw 在本地做（那等于不用 routed provider）。
