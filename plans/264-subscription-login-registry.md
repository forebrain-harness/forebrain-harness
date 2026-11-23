# Plan 264: 订阅登录统一成一个注册表（ChatGPT / OpenCode Go）；火山方舟移到 plan 281

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。`plans/README.md` 由评审方维护，不要改。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/gateway/login_api.go pkg/llm/openai/login*.go pkg/llm/opencode/login*.go pkg/turn/models.go pkg/process/models.go`
> → `pkg/turn/models.go` 只允许有 plan 260/261 的改动；其它必须为空。不为空即对照，对不上 STOP。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（新包 + 两处登录实现搬家，行为必须不变）
- **Depends on**: plans/262-…（`process.UpsertLLMModel`）、plans/263-…（注册表里的 provider 端点与模型数据）
- **Category**: direction
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **Revised**: 2026-11-08（Step 1 调研已执行并按计划 STOP：`ListApiKeys`/`GetRawApiKey`/`CreateApiKey`/`UpdateApiKey` 与「Agent Plan 个人版套餐 Key 明文」在公开 SDK 与官方 API 参考里都没有出处。owner 2026-11-08 裁定：**本计划只落注册表 + ChatGPT / OpenCode Go 两个实现**；火山方舟另立 **plan 281**（设备码登录 + 判套餐/列模型 + 个人版由用户从控制台手填 Key，团队席位可用官方 `GetTeamSeatApiKey` 自动取）。

## Why this matters

owner 2026-11-07 要求「**登录订阅的入口需要统一**，方便以后扩展其他订阅」。今天 ChatGPT、OpenCode 两个登录在网关（`pkg/gateway/login_api.go`，按 provider 名 `switch`）和 TUI（`pkg/tui/setup.go` 的 `chatGPTLogin`/`openCodeLogin` 包变量）里各写一套分支，再加一个就要两边再写一遍。

本计划在后端建一个**订阅注册表**：每种订阅实现同一个接口（登录、探测、列出可用模型、把选中的模型写成模型行），ChatGPT / OpenCode Go 改为注册表里的两个实现。网关（plan 265）、网页（plan 266）、TUI（plan 267）只认这个注册表。

注册表**只收真正有登录流程的订阅**（owner 规则：不做登录的不进统一订阅入口）。智谱 Coding Plan 只有手填 Key、不进注册表。火山方舟见 plan 281——它落地后只是多一个实现，网关/网页/TUI 零改动，这正是本计划要立住的性质。

## Current state

- 网关登录接口 `pkg/gateway/api_extra.go:58-67`：`POST /api/auth/login/:provider`、`GET /api/auth/login/:provider/:loginId`、`POST …/:loginId/cancel`、`POST …/:loginId/callback`（ChatGPT 把浏览器里的 localhost 回调地址粘回来）。实现 `pkg/gateway/login_api.go`：`authLoginOpenCode`/`authLoginChatGPT` 常量（`:36-37`）、`authLoginProbe`（`:74`，已有登录可用就直接完成）、`runAuthLogin`（`:332`，按 provider 分派到 `openai`/`opencode` 包的登录函数）、`authLoginProvider(raw)`（`:499`）、`authLoginBaseURL(provider)`（`:524`）、状态体 `authLoginStatusBody`（`:367-395`）。
- 已有登录的底层：`pkg/llm/openai`（`Login:357`、`LoginWithManualCallback:397`、`CredentialsPath:124`、`CodexBaseURL:31`）、`pkg/llm/opencode`（`LoginDeviceFlow`，`auth.go:282`）；
  探测 `turn.ProbeChatGPTModels`（`pkg/turn/models.go:327`）、`turn.ProbeOpenCodeGoModels`（`:338`，返回 `turn.SignInCheck{State}`，`SignInUsable/SignInUnreachable/SignInMissing/…`）；
  登录后的模型列表 `process.DiscoverChatGPTModels`（`pkg/process/models.go:55`，配套 `NewChatGPTModelsSource:21`）、`turn.DiscoverOpenCodeModels`（`pkg/turn/models.go:106`）。
- 登录凭证已存在库里：`(*state.ConfigStore).SaveCredential(ctx, "auth.chatgpt"|"auth.opencode", json)`（`pkg/state/config_store.go:360`，plan 187/188）。
- 写模型行：plan 262 的 `process.UpsertLLMModel(ctx, db, LLMModelInput)`（`pkg/process/llm_models.go:625`，同身份覆盖 key）。
- Step 1 的调研产物（已完成）：`docs/plan/VOLCENGINE_LOGIN_FACTS.md`（worktree 内、`docs/plan/` 被 gitignore）。火山的事实与缺口已原样搬进 plan 281，本计划不再引用该文件。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 新包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... -count=1` | ok |
| 网关回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'Login\|Auth' -count=1` | ok |
| 架构闸门 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` | 除本基线既有三条红外无新失败（见下） |
| 全量 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 除既有红外无新失败 |

**本基线既有红**（不是本计划的责任，别修、别放宽）：`pkg/architecture` 的 `pkg/llm` 32 个生产文件、`pkg/tool` 32 个（上限 31），以及 `pkg/skill/audit_skill_test.go` 无同名生产文件。

## Scope

**In scope**:
- `pkg/subscription/`（新包）：`doc.go`、`registry.go`、`chatgpt.go`、`opencode.go` 及 `registry_test.go`、`chatgpt_test.go`、`opencode_test.go`
- `pkg/architecture/cache_test.go`：`maxPackageCount` 34 → 35（带注释）；**不**改 `allowedSubpackage`（本计划没有 depth-2 包，`subscription/volcengine` 由 plan 281 加）
- `pkg/architecture/testdata/graph.json`（`./scripts/package-graph.sh` 重生成）

**Out of scope**:
- 火山方舟（plan 281）、网关路由（plan 265）、网页（plan 266）、TUI（plan 267）——本计划**不改** `pkg/gateway`、`pkg/tui`、`pkg/llm` 的行为。
- `go.mod`：**不新增任何依赖**（Step 1 已证明公开 SDK 不含需要的 action；V4 签名在 plan 281 用标准库实现）。
- 删除网关/TUI 里的旧登录分支（由 265/267 接注册表时删）。

## Steps

### Step 1: 调研闸门（已完成，勿重做）

Step 1 已执行：公开 SDK（`volcengine-go-sdk` v1.2.62）与官方 API 参考里没有 `ListApiKeys`/`GetRawApiKey`/`CreateApiKey`/`UpdateApiKey`，Agent Plan 个人版套餐 Key 明文也没有公开取法 → 按计划 STOP，owner 裁定拆出 plan 281。**本计划不重做调研，也不写任何火山代码。**

### Step 2: 订阅注册表（`pkg/subscription/registry.go` + `doc.go`）

```go
// Flow kinds a surface knows how to drive.
const (
	FlowBrowserCallback = "browser_callback" // open a URL; the redirect lands on a loopback server (TUI on the same machine)
	FlowPasteCallback   = "paste_callback"   // open a URL; paste back the address the browser ended on (ChatGPT on a remote gateway)
	FlowDeviceCode      = "device_code"      // show a user code and auto-open the browser; the flow polls (OpenCode; Volcengine in plan 281)
)

// Subscription is one sign-in-backed model source. Every surface lists them
// from Registry() and drives them through this interface only.
type Subscription interface {
	ID() string                    // "chatgpt", "opencode-go"
	Label() (en, zh string)
	// Flows lists the flow kinds this subscription supports, best first; a
	// surface picks the first one it can drive (the gateway cannot run a
	// loopback server for a remote browser, so it skips browser_callback).
	Flows() []string
	Probe(ctx context.Context, env Env) turn.SignInCheck
	Start(ctx context.Context, env Env, flow string) (Login, error)
	// Offers lists what a finished sign-in unlocks: one entry per model per
	// plan, each already carrying its endpoint.
	Offers(ctx context.Context, env Env) ([]Offer, error)
	// Provision writes the chosen offers as model rows (fetching any key the
	// plan needs) and returns their row ids, in input order.
	Provision(ctx context.Context, env Env, offers []Offer, opts ProvisionOptions) ([]string, error)
	// RefreshKey fetches the plan's key again for one row this subscription
	// wrote (the user rotated it in the provider's console) and stores it.
	// Login-backed subscriptions without keys return ErrNoKeyToRefresh.
	RefreshKey(ctx context.Context, env Env, modelID string) error
}

type Login interface {
	AuthorizeURL() string
	UserCode() string                       // device_code only
	Submit(ctx context.Context, pasted string) error // paste_* only; error text is safe to show
	Wait(ctx context.Context) error         // returns when signed in, failed, or ctx ends
	Cancel()
}

type Offer struct {
	Subscription string // ID()
	Plan         string // "" | "coding-plan" | "agent-plan" | "coding-plan-team" | "agent-plan-team"
	PlanLabel    string // "Coding Plan" / "Agent Plan · 团队" …
	Provider, Model, BaseURL, APIPath string
	ContextWindow int64
	SupportsImages bool
	Default bool
}

// ProvisionOptions carries the choices a key-backed plan needs. ChatGPT and
// OpenCode Go ignore it; plan 281 (Volcengine) reads both fields.
type ProvisionOptions struct {
	// KeyID picks one key when a plan's key pool holds several; empty means
	// "the only Active one" and fails with ErrKeyChoice when there are several.
	KeyID string
	// CreateKey consents to creating one key when the pool is empty.
	CreateKey bool
}

type Env struct{ Home string; DB *sql.DB }

func Registry() []Subscription  // order: chatgpt, opencode-go
func ByID(id string) (Subscription, bool)
```

哨兵错误与类型（供 265 的接口与 281 的实现共用；本计划的实现里只有 `ErrNoKeyToRefresh` 会被返回）：

```go
// ErrKeyChoice carries the candidate keys (id, name, masked tail) when a pool
// has several Active keys; ErrNoKey says the pool is empty and creating one
// needs the user's consent.
type ErrKeyChoice struct{ Keys []KeyCandidate }
var ErrNoKey = errors.New("no API key in this plan's pool; creating one needs your consent")
var ErrNoKeyToRefresh = errors.New("this subscription has no stored key to refresh")
type KeyCandidate struct{ ID, Name, Tail string }
```

`doc.go` 写清包的领域：**订阅登录的唯一注册表，界面只认它**；新增一种订阅 = 一个实现 + `Registry()` 一行。

**Verify**: `go build ./pkg/subscription/` → exit 0；`gofmt -l pkg/subscription` 无输出。

### Step 3: ChatGPT 与 OpenCode 收进注册表（行为不变）

- `chatgpt.go`：`ID()="chatgpt"`、`Label()=("ChatGPT","ChatGPT")`、`Flows() = [FlowBrowserCallback, FlowPasteCallback]`；
  `Probe` = `turn.ProbeChatGPTModels(ctx, openai.CredentialsPath(env.Home), process.NewChatGPTModelsSource(env.Home))`；
  `Start` 照 `pkg/gateway/login_api.go` `runAuthLogin` 里 ChatGPT 那支（`openai.LoginWithManualCallback` + `ManualCallback` 通道）包成 `Login`；
  `Offers` = `process.DiscoverChatGPTModels` 投影成 `Offer{Provider:"chatgpt", BaseURL: openai.CodexBaseURL, APIPath:""}`；
  `Provision` = 每个 offer `process.UpsertLLMModel(…, APIKey: ptr(""), Source:"chatgpt")`（登录类，无 key）；`RefreshKey` → `ErrNoKeyToRefresh`。
- `opencode.go`：`ID()="opencode-go"`、`Flows()=[FlowDeviceCode]`，同理照 `runAuthLogin` 的 OpenCode 那支与 `turn.DiscoverOpenCodeModels(ctx, src, true)`；`Provision` 写 `Source:"opencode-go"`。
- 为了可测，包内登录函数走**包级变量**（照 `pkg/gateway/login_api.go` 里 `var authLoginProbe = …` 的写法），测试替换它们，不连真网。

测试：桩掉包级登录/探测变量，断言 `Probe/Start/Wait/Offers/Provision` 的结果与表里写入的行（`provider`/`base_url`/`api_path`/`source`/`api_key` 为空）。既有 `pkg/gateway/login_api_test.go` 一行不改也必须通过（本计划不动 gateway）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/ -run 'ChatGPT|OpenCode' -count=1` → ok。

### Step 4: 结构闸门与验证

- `pkg/architecture/cache_test.go`：`maxPackageCount` 34 → 35，按现有注释风格补一句（订阅注册表是新的一层，plan 264）。
- `./scripts/package-graph.sh` 重生成 `pkg/architecture/testdata/graph.json`；`git status` 确认只动了这一个文件（它本来就有 loc 漂移）。
- `go vet ./pkg/subscription/...`、`gofmt -l pkg/subscription`。

**Verify**:
- `go build ./...` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... ./pkg/gateway/ -run 'Login|Auth|ChatGPT|OpenCode' -count=1` → ok
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` → 只有那三条既有红
- 全量（一次）：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 失败清单只有既有红

## Test plan

见 Step 3。桩的写法照 `pkg/gateway/login_api_test.go` 与 `pkg/llm/opencode/models_test.go`（`stubTransport`）。

## Done criteria

- [ ] `go build ./...` exit 0；`gofmt -l pkg/subscription` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription/... -count=1` ok；`./pkg/gateway/ -run 'Login|Auth'` ok（既有用例一行未改）
- [ ] 全量运行的失败清单 = 已知三条既有红（`pkg/llm`/`pkg/tool` 32 文件、`pkg/skill/audit_skill_test.go`），没有新增失败
- [ ] `grep -n "func Registry" pkg/subscription/registry.go` 有；`Registry()` 依次返回 `chatgpt`、`opencode-go`（没有任何「只填 Key」的条目）
- [ ] `grep -rn "zcode.z.ai\|oauth/cli" pkg/subscription` 无输出
- [ ] `grep -rn "slog\." pkg/subscription` 里没有把 token/key 变量作为字段值
- [ ] `git status --short` 里**本计划**没有 `pkg/gateway`、`pkg/tui` 下的改动（worktree 里这些文件属于 plans 260/261 的既有改动，不得再动）
- [ ] `pkg/architecture` 只改了 `maxPackageCount` 与 `graph.json`
- [ ] 报告里列出 plan 281 需要的接口扩展（若有）

## STOP conditions

- 既有 `pkg/gateway/login_api_test.go` 需要改断言（而不只是桩位置）才能通过 → 行为变了，停下。
- 需要 `pkg/llm`、`pkg/turn`、`pkg/process` 提供今天不存在的导出（说明注册表的接口形状不对）→ 停下报告，不要改它们的签名。
- 需要的依赖不止标准库 → 停下。

## Maintenance notes

- 加一种订阅 = 在 `pkg/subscription` 加一个实现 + 在 `Registry()` 里登记；网关/网页/TUI 不需要改分支（plan 265–267 保证这点）。
- `ProvisionOptions`/`ErrKeyChoice`/`ErrNoKey`/`KeyCandidate` 是**带 Key 的订阅**的契约（plan 265 的 409 分支与 plan 281 的实现），ChatGPT/OpenCode 忽略它们；不要因为今天没人用就删。
- 注册表只收有登录流程的订阅（owner 规则）；智谱 Coding Plan 之类的「只填 Key」端点属于内置 provider（plan 263），永远不进这里。
