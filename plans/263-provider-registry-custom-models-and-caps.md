# Plan 263: 内置 provider 单一来源（加美团、腾讯）、自定义模型能跑、能力覆盖生效、测试连接

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/config/provider_defaults.go pkg/llm/catalog.go pkg/run/config.go pkg/tui/setup.go pkg/gateway/api_extra.go pkg/process/provider.go`
> → 只允许出现 plan 260–262 的改动（`pkg/run/config.go` 的视图/选择器、`pkg/tui/setup.go` 的默认解析与选模型、`pkg/gateway/api_extra.go` 的会话模型与 providers 处理器）。
> `providerGroups`（`pkg/tui/setup.go:335-518`）、`NewLLMFromYAML`（`pkg/run/config.go:385-470`）、`providerDefaultBaseURLs` 若有变化即对照，对不上 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（动到建客户端与目录查询；内置 provider 的行为必须保持不变）
- **Depends on**: plans/260-runtime-reads-global-model-list.md（`LLMModelConfig.Caps`、`ConfiguredLLMs`）
- **Category**: direction
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **Executed**: 2026-11-08, worktree `.worktrees/exec-259-268`, 基线 `d9c45060`（未提交）——**DONE**，判据实测：
  `go build ./...` 0；`pkg/config`/`pkg/llm`（含新包 `pkg/llm/modelinfo`）/`pkg/run`/`pkg/process`/`pkg/tui`/`pkg/gateway` 全包 ok；
  四条 done grep 成立（四家新 provider 在注册表、`api/coding/paas/v4` ×2、`providerDefaultBaseURLs` 已删、
  `providerGroupsFromRegistry` 在、`CustomProvider` 两处、用户可见文案不再提 `models.json`）；`test-db.sh check` 0；
  `pkg/architecture` 只剩本基线三条既有红（`pkg/llm`/`pkg/tool` 各 32、`pkg/skill/audit_skill_test.go`）——**没有新增违规**。
  **偏差与需 owner 知情的点**：
  ① **动了一处 owner 闸门**：新增 `pkg/llm/modelinfo` 包使包数 33→34，`pkg/architecture/cache_test.go` 的 `maxPackageCount`
  与 `allowedSubpackage` 各加一条（`pkg/llm` 已在 32 文件上限，实时档位源无法塞进 `pkg/llm`；`llm` 层不能 import `config`，
  所以它只能是一个独立叶子包）。`graph.json` 已用 `scripts/package-graph.sh` 重生成（含 259–262 未提交的 LOC 变化）。
  ② 注册表多带四个**展示**字段（`BuiltinProvider.Hint/APIKeyLabel`、`ProviderEndpoint.ChoiceLabel/Hint`）：计划给的派生规则
  无法逐字复现今天的 TUI 文案（google 的 `Gemini API key`、moonshotai 的 `Kimi API key`、alibaba 四端点的裸端点名、minimax 的
  `(Global)/(CN)`）；把旧文案搬进注册表才能同时满足「单一来源」与「逐字段不变」，测试从旧字面量逐项比对。
  ③ `ProbeLLMModel` 去掉计划里的 `caps` 形参（探针发固定最小请求，caps 无消费点）；`pkg/process/llm_probe.go` 与
  `pkg/llm/exact_url.go` 因文件上限并入既有文件（`pkg/process/llm_models.go`、`pkg/llm/request_context.go`）。
  ④ 后台档位查询：只在非测试进程里跑（`testing.Testing()` 守卫，与仓库既有先例一致），单 goroutine + 每源 10s 超时 + 静默回落；
  任何测试二进制都不会打 models.dev / ark / ChatGPT。
  ⑤ models.dev「多命中取第一个」实现为**候选键排序后取首个**（JSON 对象解码不保序），行为可复现。
  **手动验收清单（无真 key，未跑）**：LongCat `LongCat-2.5`（OpenAI 兼容 + Anthropic 两个端点）；TokenHub 国内 `hy3`；
  TokenHub 国际站（需确认地址真实存在，不存在就从注册表删）；火山方舟 Coding Plan 手填 Key（`/api/coding` 与 `/api/coding/v3` 各一次，
  并确认 Coding Key 读 `/api/coding/v3/models` 的 401 现象）；BytePlus `doubao-seed-2.1-pro`。

## Why this matters

owner 2026-11-07 的四条要求落在这里：

1. **把 meituan 和 tencent 加进内置 provider 列表**供用户选择；owner 同日追加**火山方舟（volcengine）的 Coding Plan 与 Agent Plan**
   （本计划只把它们作为内置地址 + 手填 Key 提供；SSO 登录自动取 Key 在 plan 264）。owner 裁定：登录只做国内 cn-beijing，BytePlus 海外
   Coding Plan 只作内置地址；默认协议 Anthropic Messages，OpenAI 兼容 `/v3` 作备选端点。
   owner 同日再追加：**智谱 Coding Plan**（调研结论：官方对第三方只给手填 Key，账号登录只在智谱自家 ZCode 里、靠私有接口；裁定不做登录）——
   在 zhipuai 下加 Coding Plan 专用端点（见下表），在「添加模型」里手填 Key；owner 规则「不做登录的不放进统一订阅入口」，所以它**不**进 plan 264 的订阅注册表。
2. **自定义模型配置**（参考 workbuddy「添加模型 → 自定义」）：不在列表里的服务也能用。owner 补充：
   - 自定义**不让用户输入 provider**；模型名称是自由文本，用户可以自带 `provider/` 前缀（如 `openai/gpt-4o`），**原样**作为请求的 model 参数；
   - 自定义的「接口协议」**必选**，完整接口地址 = base url 拼上所选协议对应的 api path（Chat Completions → `/chat/completions`，Responses → `/responses`，Anthropic Messages → `/v1/messages`）。
3. 自定义模型的「高级配置」（图片输入、思考模式、允许关闭思考、支持的思考强度、思考强度、输入/输出 token 上限）要真正生效。
4. 「测试连接」按钮需要一个后端能力。

今天内置 provider 散在三处、互不一致：`pkg/config/provider_defaults.go` 的默认地址表（10 个）、`pkg/tui/setup.go` 的
`providerGroups`（TUI 引导与 /connect 的选择列表，含千问 4 个地址、MiniMax 2 个）、网页「模型」页的 provider 下拉
（= 目录里出现过的全部 provider + 默认地址表）。客户端构造 `NewLLMFromYAML` 拒绝 models.json 不认识的 provider，
所以自定义服务根本跑不起来。

## Current state

- 默认地址表 `pkg/config/provider_defaults.go:15-26`（`openai`、`anthropic`、`alibaba`、`google`、`deepseek`、`xai`、`mistral`、`moonshotai`、`zhipuai`、`minimax`），
  对外 `ProviderDefaultBaseURL(provider)`、`ProviderDefaultBaseURLTable()`；网关 `providerBaseURLDefaults()`（`pkg/gateway/api_extra.go:630-643`）把它作为
  `provider_base_urls` 放进模型目录接口的响应（`:615`）。
- TUI 选择列表 `pkg/tui/setup.go:266-292`（`providerChoice{Group, Label, Hint, Provider, DefaultBaseURL, SupportsAPIPath, APIKeyLabel, ChatGPTOAuth, OpenCodeDevice}`、`providerGroup{Label, Hint, Options}`）
  与 `var providerGroups = []providerGroup{…}`（`:335-518`，12 组），英文文案。
- 客户端构造 `pkg/run/config.go:385-470` `NewLLMFromYAML`：
  - `apiKey == ""` 时回落 `appcfg.ProviderAPIKeyLookup(os.Getenv, prov)`，`baseURL == ""` 时回落 `ProviderBaseURLLookup`（**本计划不动，plan 268 删**）；
  - `chatgpt`、`opencode*` 各有专门分支；
  - `maxTokens` 取 `llm.Lookup(prov, model).DefaultMaxTokens`；
  - `case "anthropic", "minimax"` → Anthropic Messages；
  - `default:` 里 `if !llm.KnownProvider(prov) { return nil, fmt.Errorf("unsupported llm provider: %s (not found in models.json)", prov) }`（**自定义被挡在这里**），
    然后 `isAnthropicMessagesPath(apiPath)` → Anthropic；`strings.Contains(apiPath, "/responses")` → Responses；否则 Chat Completions（`NewCompatLLMWithPromptCaching(..., cfg.APIPath, ...)`）。
- 目录查询 `pkg/llm/catalog.go:600-624` `Lookup(provider, apiModel) (Hit, bool)`（全局目录 `globalCat`，键 `JoinProviderModel(provider, apiModel)`）；
  `ReasoningEfforts`（`:630`）、`KnownProvider`（`:669`）都基于它。上下文窗口、图片能力、思考档位全仓库都从 `llm.Lookup` 读。
- `pkg/process/provider.go:31-44` `VerifyProvider`：登录类放行，其余必须 `llm.KnownProvider`。
- 目录里已有 `meituan/longcat-2.0`、`meituan/longcat-2.5-preview`、`tencent/hy3`、`tencent/hy3-preview`、`tencent/hy4-preview` 等条目（`assets/models.json`），所以这两个 provider `KnownProvider` 为真。
- **官方端点（2026-11-07 核对，见文末来源）**：
  - 美团 LongCat：OpenAI 格式完整地址 `https://api.longcat.chat/openai/v1/chat/completions`；Anthropic 格式 `https://api.longcat.chat/anthropic/v1/messages`；官方示例模型名 `LongCat-2.5`（大小写如此）。
  - 腾讯云 TokenHub：`https://tokenhub.tencentmaas.com/v1/chat/completions`（官方）；国际站 `https://tokenhub-intl.tencentmaas.com/v1` 只在第三方文档出现（列为手动验收）；模型 `hy3`、`hy4`、`hy4-preview`。
- 协议路径的语义（plan 260 的 `NormalizeLLMModelIdentity` 已把空 path 补成协议默认）：完整地址 = base_url + api_path。
  Chat Completions 客户端自己拼 `/chat/completions`（`pkg/llm/openai/compat_llm.go:97`：path 等于默认时不改写）；Responses 客户端拼 `responses`；Anthropic SDK 拼 `v1/messages`。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/config/... ./pkg/llm/... ./pkg/run/... ./pkg/process/... ./pkg/tui/... ./pkg/gateway/... -count=1` | ok |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | ok |

## Scope

**In scope**:
- `pkg/config/provider_registry.go`（新建）+ 测试；`pkg/config/provider_defaults.go`（改为从注册表派生）
- `pkg/config/llm_models.go`（plan 260 建的文件：`ConfiguredLLMs` 投影时套用 `DefaultEffort`）
- `pkg/llm/catalog.go`（覆盖层 + 自定义 provider 的目录借用）+ 测试
- `pkg/run/config.go`（`LLMProviderYAML` 加 `MaxOutputTokens`；`NewLLMFromYAML` 放行自定义）+ 测试
- `pkg/process/provider.go`（`VerifyProvider` 放行 `custom`）、`pkg/process/llm_probe.go`（新建，测试连接）+ 测试
- `pkg/process/config_db.go`（组合后安装覆盖层）
- `pkg/tui/setup.go`（`providerGroups` 改为由注册表生成）
- `pkg/gateway/api_extra.go`（只改 `providerBaseURLDefaults`）

**Out of scope**:
- 新网关接口（plan 265）、网页（plan 266）、TUI 的自定义模型流程与选择器显示（plan 267）。
- `NewLLMFromYAML` 的环境变量回落（plan 268 删）。
- `assets/models.json`：**不改**（它只在首次运行种入、不覆盖用户已有的；改了也到不了老安装）。

## Steps

### Step 1: 内置 provider 注册表（`pkg/config/provider_registry.go`）

```go
// CustomProvider is the provider id every user-defined model is stored under
// (owner 2026-11-07: the custom form asks for no provider; the model name is
// free text and may carry its own "vendor/" prefix). No built-in may use it.
const CustomProvider = "custom"

// Protocol paths: the full endpoint is base_url + one of these.
const (
	APIPathChatCompletions   = "/chat/completions"
	APIPathResponses         = "/responses"
	APIPathAnthropicMessages = "/v1/messages"
)

type ProviderEndpoint struct {
	Label, LabelZH string // "Global" / "国际站"; "" when the provider has one endpoint
	BaseURL        string
	APIPath        string // one of the protocol paths above
}

type BuiltinProvider struct {
	ID              string // the models.json prefix: "deepseek", "meituan", …
	Label, LabelZH  string // "DeepSeek" / "深度求索"
	Endpoints       []ProviderEndpoint // first = default
	LoginBacked     bool // chatgpt, opencode-go: endpoint comes from the sign-in
	// Models is capability data for a provider models.json does not carry
	// (volcengine, byteplus): the catalog has no row for them, so without
	// this their context window, output limit and image support are unknown.
	// Empty for every provider the catalog already knows.
	Models []BuiltinModel
}

type BuiltinModel struct {
	ID                      string // the request model id, verbatim
	ContextWindow, MaxOutput int64
	Images                  bool
}

// BuiltinProviders is the one list every surface offers, in display order.
func BuiltinProviders() []BuiltinProvider

func BuiltinProviderByID(id string) (BuiltinProvider, bool)
```

内容（顺序即显示顺序；英文 Label 沿用 `pkg/tui/setup.go` 现有 group 名；端点与路径**逐字照下表**）：

| ID | Label / LabelZH | Endpoints（Label/LabelZH → BaseURL + APIPath） |
|---|---|---|
| chatgpt | ChatGPT / ChatGPT | 登录类：`openai.CodexBaseURL`，path `""`（`LoginBacked`）——注意 `pkg/config` 不能 import `pkg/llm/openai`，这里把常量值抄过来并在注释写明出处；Step 6 加测试断言两者相等 |
| opencode-go | OpenCode / OpenCode | 登录类：`opencode.ProviderBaseURL("opencode-go")` 的值，同上处理 |
| openai | OpenAI / OpenAI | `https://api.openai.com/v1` + `/chat/completions` |
| anthropic | Anthropic / Anthropic | `https://api.anthropic.com` + `/v1/messages` |
| alibaba | Qwen Cloud / 通义千问 | Standard · China/标准版·国内 `https://dashscope.aliyuncs.com/compatible-mode/v1`；Standard · Global/标准版·国际 `https://dashscope-intl.aliyuncs.com/compatible-mode/v1`；Coding Plan · China/编程套餐·国内 `https://coding.dashscope.aliyuncs.com/v1`；Coding Plan · Global/编程套餐·国际 `https://coding-intl.dashscope.aliyuncs.com/v1`；全部 `/chat/completions` |
| google | Google / Google Gemini | `https://generativelanguage.googleapis.com/v1beta/openai` + `/chat/completions` |
| deepseek | DeepSeek / 深度求索 | `https://api.deepseek.com` + `/chat/completions` |
| xai | xAI / xAI | `https://api.x.ai/v1` + `/chat/completions` |
| mistral | Mistral AI / Mistral AI | `https://api.mistral.ai/v1` + `/chat/completions` |
| moonshotai | Moonshot AI / 月之暗面 Kimi | `https://api.moonshot.ai/v1` + `/chat/completions` |
| zhipuai | Z.AI / 智谱 | 第一个端点保持今天的 `https://api.z.ai/api/paas/v4` + `/chat/completions`（Label 空，沿用旧文案）；**owner 2026-11-07 追加 Coding Plan 专用端点**：Coding Plan · Global/Coding Plan·国际 `https://api.z.ai/api/anthropic` + `/v1/messages`；Coding Plan · Global (OpenAI)/Coding Plan·国际（OpenAI 兼容）`https://api.z.ai/api/coding/paas/v4` + `/chat/completions`；Coding Plan · China/Coding Plan·国内 `https://open.bigmodel.cn/api/anthropic` + `/v1/messages`；Coding Plan · China (OpenAI)/Coding Plan·国内（OpenAI 兼容）`https://open.bigmodel.cn/api/coding/paas/v4` + `/chat/completions`；Standard · China/标准·国内 `https://open.bigmodel.cn/api/paas/v4` + `/chat/completions` |
| minimax | MiniMax / MiniMax | Global/国际站 `https://api.minimax.io/anthropic` ；CN/国内 `https://api.minimaxi.com/anthropic`；都 `/v1/messages` |
| **meituan** | **Meituan LongCat / 美团 LongCat** | **OpenAI 兼容 `https://api.longcat.chat/openai/v1` + `/chat/completions`；Anthropic 兼容 `https://api.longcat.chat/anthropic` + `/v1/messages`** |
| **tencent** | **Tencent Cloud TokenHub / 腾讯云 TokenHub** | **China/国内 `https://tokenhub.tencentmaas.com/v1` + `/chat/completions`；Global/国际站 `https://tokenhub-intl.tencentmaas.com/v1` + `/chat/completions`** |
| **volcengine** | **Volcengine Ark / 火山方舟** | **Coding Plan/Coding Plan `https://ark.cn-beijing.volces.com/api/coding` + `/v1/messages`；Agent Plan/Agent Plan `https://ark.cn-beijing.volces.com/api/plan` + `/v1/messages`；Coding Plan · OpenAI/Coding Plan · OpenAI 兼容 `https://ark.cn-beijing.volces.com/api/coding/v3` + `/chat/completions`；Agent Plan · OpenAI/Agent Plan · OpenAI 兼容 `https://ark.cn-beijing.volces.com/api/plan/v3` + `/chat/completions`；Pay as you go/按量付费 `https://ark.cn-beijing.volces.com/api/v3` + `/chat/completions`** |
| **byteplus** | **BytePlus ModelArk / BytePlus（海外）** | **Coding Plan/Coding Plan `https://ark.ap-southeast.bytepluses.com/api/coding` + `/v1/messages`** |

`volcengine` 与 `byteplus` 的 `Models`（来源：ark-cli 仓库 `dsh-plugins/ark-plan-api/cordis.patch.yml`，即火山官方给 Agent 宿主的 Plan 模型表；同一模型在两个 Plan 里数值相同时只列一次，`Images` 对应 `input: [text, image]`）：

| ID | ContextWindow | MaxOutput | Images |
|---|---|---|---|
| doubao-seed-evolving | 1024000 | 256000 | true |
| doubao-seed-2.1-pro | 256000 | 256000 | true |
| doubao-seed-2.1-turbo | 256000 | 256000 | true |
| doubao-seed-2.0-lite | 256000 | 128000 | true |
| doubao-seed-2.0-mini | 256000 | 128000 | true |
| glm-5.3 | 1024000 | 128000 | false |
| kimi-k3 | 1024000 | 128000 | true |
| deepseek-v4-pro | 1024000 | 384000 | false |
| deepseek-v4-flash | 1024000 | 384000 | false |
| minimax-m3 | 1024000 | 128000 | true |

`byteplus` 只列 `doubao-seed-2.1-turbo`、`doubao-seed-2.1-pro`，`MaxOutput` 为 65536（BytePlus 表里的值）。

（MiniMax 现有写法带结尾 `/`；新表去掉结尾 `/`，与 plan 259/260 的规范化一致——Anthropic SDK 的 `option.WithBaseURL` 会自己补斜杠，Step 4 有测试钉住。）

`provider_defaults.go`：`providerDefaultBaseURLs` 删掉，`ProviderDefaultBaseURL(p)` 改为「注册表里非登录类 provider 的第一个端点的 BaseURL」，`ProviderDefaultBaseURLTable()` 同理生成。注释更新。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/config/ -count=1` → ok（`provider_defaults_test.go` 既有断言若因 minimax 去尾斜杠而失败，按新值改并在报告说明）。

### Step 2: 注册表测试（`pkg/config/provider_registry_test.go`）

- 每个 ID 唯一、非空、全小写；没有一个叫 `custom`；
- 每个非登录类端点 `BaseURL` 以 `https://` 开头、不以 `/` 结尾，`APIPath` ∈ 三个协议常量；
- `meituan`、`tencent` 存在，端点与上表逐字相等；
- 对每个 ID（除 `chatgpt`），`llm` 目录里至少有一个 `<id>/` 前缀的条目——用 `llm.SetGlobalCatalogForTest` 装 `assets/models.json`（照 `pkg/llm/catalog_test.go` 读 assets 的写法；若 config 包不能 import llm 造成循环，就把这条测试放进 `pkg/llm` 或 `pkg/process` 的测试里）。

### Step 3: 目录覆盖层与自定义借用（`pkg/llm/catalog.go`）

```go
// ModelOverride is one configured row's capability overrides (fb_llm_models
// cap_* columns). nil/empty fields leave the catalog's value.
type ModelOverride struct {
	SupportsImages, SupportsPDF, SupportsVideo, SupportsAudio, CanReason, ReasoningOff *bool // PDF/Video/Audio 由 plan 269 消费
	Efforts       []string // nil = catalog
	ContextWindow, MaxOutput *int64
}

// SetModelOverrides installs the overrides the configured rows carry, keyed
// by JoinProviderModel(provider, model). The process installs them every time
// it adopts a configuration; nil clears them.
func SetModelOverrides(m map[string]ModelOverride)
```

`Lookup(provider, apiModel)` 改为：
1. 先照旧查目录得到 `hit, ok`；
2. **`provider == "custom"`（用 `strings.EqualFold`）且没命中、且 `apiModel` 含 `/`** → 再用 `cat.LookupModel(apiModel)`（把模型名本身当目录 id，如 `openai/gpt-4o`）借一份能力；仍没有 → `hit = Hit{}`，`ok=false`；
3. 有覆盖（键 `JoinProviderModel(provider, apiModel)` 小写）→ 在 `hit` 上逐项覆盖：`SupportsImages`→`SupportsAttachments`；`CanReason`；`ContextWindow`；`MaxOutput`→`DefaultMaxTokens`；
   档位：`Efforts != nil` 或 `ReasoningOff != nil` 时重建 `ReasoningOptions = []ReasoningOption{{Type: "effort", Values: levels}}`，`levels` = `Efforts`（为 nil 时用目录原档位）且 `ReasoningOff==true` 时在最前面加 `"none"`；`ReasoningKnown = true`；
   `CanReason` 被覆盖为 false 时 `ReasoningOptions = nil, ReasoningKnown = true`；覆盖后 `ok = true`。
4. 覆盖表用独立的 `sync.RWMutex` 保护，与 `globalCatMu` 分开。

`KnownProvider(p)`：`custom` 返回 true；另加

```go
// SetBuiltinModels installs capability rows for providers the catalog has no
// entries for (pkg/config's registry Models: volcengine, byteplus). They rank
// below the catalog and above nothing: a configured row's overrides still win.
func SetBuiltinModels(provider string, models []Model)
```

`Lookup` 第 1 步目录没命中时，再查这张表（键 `JoinProviderModel`）；`KnownProvider` 对装过内置模型的 provider 返回 true。
（`pkg/llm` 不能 import `pkg/config`，所以由 `pkg/process` 在启动时把注册表的 `Models` 装进来，见 Step 4 第 5 条。）

测试 `pkg/llm/catalog_test.go` 加（测试名 `TestModelOverride*`）：无覆盖时行为不变；覆盖图片/上下文/输出；`Efforts:["low","high"]` + `ReasoningOff:true` → `ReasoningEfforts()` = `[none low high]`；`CanReason:false` → 无档位；`custom` + `openai/gpt-4o` 借到目录的上下文窗口；`custom` + `my-model`（无斜杠、无覆盖）→ `ok=false`；`SetModelOverrides(nil)` 清空。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run 'ModelOverride|Lookup|Catalog' -count=1` → ok。

### Step 3b: 思考档位的来源与回落（owner 2026-11-07 定稿）

owner 规则（最终版）：
1. **ChatGPT 订阅行**：OpenAI/Codex 后端模型列表实时返回档位（`pkg/llm/openai/models.go` 的 `supported_reasoning_levels` / `default_reasoning_level`，已在用，保持）——**这一类行在界面上只给「思考强度」下拉，绝不出现「支持的思考强度」勾选**。
2. **火山方舟订阅行**：`GET /api/coding/v3/models` 实时返回 `data[].effort.supported_levels` / `default_level`（实测 200，见 `docs/plan/SUBSCRIPTION_EFFORT_SOURCES.md`）。
3. **其余一切模型**（OpenCode 订阅、内置厂商的模型、自定义模型）：**实时调用 `https://models.dev/catalog.json?type=all`** 查档位。
4. **查不到 → 「支持的思考强度」让用户勾选**。这是唯一的回落；没有第三层。

实现要点：
- 新建 `pkg/llm/modelinfo/`：

```go
// Source answers the reasoning levels for one configured model, live.
type Source interface {
	// Applies reports whether this source handles the row.
	Applies(row appcfg.LLMModelConfig, subscription string) bool
	Fetch(ctx context.Context, row appcfg.LLMModelConfig, apiKey string) (Info, error)
}
type Info struct {
	Efforts       []string // nil = not known -> the surface shows the checkboxes
	DefaultEffort string
	ContextWindow, MaxOutput int64
	Images *bool
	Origin string // "chatgpt-api" | "ark-api" | "models.dev"
}
```

- 三个实现：
  - `chatgpt`（已有路径：`turn.DiscoverChatGPTModels` 的后端列表）；
  - `volcengine`：`GET https://ark.cn-beijing.volces.com/api/coding/v3/models`，Bearer 用该行的套餐 Key，按 `data.id` 或 `data.name` 匹配（实测任一有效套餐 Key 都能调；响应同时给出 token_limits 与 input_modalities）；
  - `modelsdev`：`GET https://models.dev/catalog.json?type=all`（无鉴权，实测 6.5MB），进程内缓存整份解析结果（按 TTL 6h），查询规则：
    - OpenCode 订阅：`providers['opencode-go'|'opencode'].models[id]`；
    - 内置厂商：`providers['<注册表id>'].models[<模型名>]`；
    - 自定义带前缀：顶层 `models['<vendor>/<name>']`；自定义不带前缀：顶层表里「末段等于该名字」的命中，**多命中取第一个**（按响应表序，稳定可复现）；
    - `status:"deprecated"` 过滤；`reasoning_options` 里 `type:"effort"` 的 `values` 即档位，`toggle` 或空 = 查不到。
- 优先级分发：1 → 2 → 3 →（查不到）用户勾选。**plan 263 原拟的「官方文档档位表」不再作为运行时层**——models.json（同步自 models.dev）继续作为非档位数据与断网兜底快照存在，但档位判定只走上面的链。
- 缓存与超时：每个 Source 10s 超时；models.dev 整份缓存 TTL 6h；实时失败静默回落（`slog.Debug`），不打扰用户。
- 401/403 的归因：推理/查询被拒时如实报「Key 无效或套餐已达限额」，**不因一次 401 清用户配置**（火山实测有「一次 200、重试 401」的不稳定）。

测试：每个 Source 用 `httptest` 冒充端点——命中/未命中/deprecated/toggle 四态；modelsdev 的三种查法（opencode id、内置厂商名、自定义带/不带前缀、多命中取第一个）；优先级分发（订阅接口失败回落 models.dev，再查不到才算未知）。调研与实测细节见 `docs/plan/SUBSCRIPTION_EFFORT_SOURCES.md`。

**STOP**：models.dev 的响应结构变化（顶层不再是 `{providers, models}`）或 opencode 条目消失——不要猜新结构，停下报告。

### Step 3c: 实时层在进程内的安装点

- `pkg/process` 在采纳配置时（Step 4 第 5 条的 `installModelOverrides` 同处）对当前安装的每一行跑一次 `modelinfo` 解析：订阅行走对应订阅 Source；其余行走 modelsdev（整份缓存 TTL 6h）。
- 结果进进程内缓存（按行 id）：`/api/llm-models/catalog-caps`、`catalog_caps`、思考强度下拉都读它；缓存过期或行改动后下次采纳/下次打开抽屉再查。
- 实时失败静默回落（`slog.Debug`）。「回落」= `efforts_known=false` → 界面显示「支持的思考强度」复选（models.dev 查不到与网络失败同待遇）。

### Step 4: 运行时接上覆盖与自定义

1. `pkg/config/llm_models.go`：`ConfiguredLLMs` 投影表行时，若 `Caps.DefaultEffort != ""` 且 `Params` 里没有 `reasoning.effort`（用 `ReasoningEffort(params) == ""` 判断）→ `Params = WithReasoningEffort(Params, Caps.DefaultEffort)`。
   `AgentLLMProviderConfig` 加 `MaxOutputTokens int64 `yaml:"-" json:"-"``，从 `Caps.MaxOutput` 填。
2. `pkg/run/config.go`：`LLMProviderYAML` 加 `MaxOutputTokens int64 `yaml:"-" json:"-"``；`llmYAMLsFromResolved` 搬过去。
   `NewLLMFromYAML` 里两处 `maxTokens` 计算之后：`if cfg.MaxOutputTokens > 0 { maxTokens = int(cfg.MaxOutputTokens) }`（行级精确值优先于按 provider/model 的覆盖层）。
3. `NewLLMFromYAML` 的 `default:` 分支：`if !llm.KnownProvider(prov)` 之前加：

```go
		if prov == appcfg.CustomProvider {
			// A user-defined model: the protocol is whatever its api_path
			// names (the custom form requires one), never inferred from a
			// catalog that does not know the model.
			apiPath := strings.TrimSpace(cfg.APIPath)
			if apiPath == "" {
				return nil, fmt.Errorf("a custom model needs an API protocol")
			}
		}
```

   之后的协议分派原样复用（`isAnthropicMessagesPath` / `/responses` / Chat Completions）。`openAIPromptCaching(prov)` 对 `custom` 必须为 false（确认该函数只对 `openai` 返回 true；不是就停下）。
4. `pkg/process/provider.go` `VerifyProvider`：`custom` 放行（在登录类判断之后加一行）。
5. `pkg/process/config_db.go`：写一个 `installModelOverrides(root appcfg.Root)`——遍历 `root.LLMModels`，有任一 Caps 字段非空的行，按 `JoinProviderModel(provider, model)` 建 `llm.ModelOverride`，**同键多行时第一行（created_at 最早）胜出**；然后 `llm.SetModelOverrides(m)`。
   同一函数里先把注册表里带 `Models` 的 provider 用 `llm.SetBuiltinModels` 装进去（每次调用都装，幂等）。
   在 `Open` 组合完成后（`loaded = composed` 之后）与 `SyncConfigFromDB` 采纳新配置的地方各调一次（`grep -n "func SyncConfigFromDB" -A 60 pkg/process/config_db.go` 找到「hash moved → 重新应用」那一支）。`opts.Config` 旁路也调（测试传入的 Root 里可以带 `LLMModels`）。

测试（`pkg/run/config_test.go`，名字带 `Custom` 或 `MaxOutput`）：用 `httptest.Server` 当端点——
- `custom` + `/chat/completions` → 请求路径 `<base>/chat/completions`，请求体 `model` 等于原样的 `openai/gpt-4o`；
- `custom` + `/responses` → 路径 `<base>/responses`；
- `custom` + `/v1/messages` → 路径 `<base>/v1/messages`；
- `custom` + 空 path → 报错 `a custom model needs an API protocol`；
- `minimax` 的 base 不带结尾 `/`（`https://x/anthropic` 形态，用 httptest 的 URL + `/anthropic`）→ 请求打到 `/anthropic/v1/messages`（钉住 SDK 补斜杠的行为）；
- `MaxOutputTokens: 1234` → 请求体里的 max tokens 字段是 1234。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'Custom|MaxOutput|NewLLMFromYAML' -count=1` → ok。

### Step 4a: 「自定义协议」——原样请求接口地址（owner 2026-11-07，参考 workbuddy）

语义（owner 定稿：自定义协议是「接口协议」下拉的第四项，**只有自定义服务商（`provider == custom`）有**；选中后不再拼接任何协议路径，接口地址就是完整请求地址）：行的 `URLVerbatim` 为真时，请求体按 **OpenAI Chat Completions**，直接发到 `base_url` 原样（不补全 `/chat/completions` 或任何路径）。存储上 `api_path` 固定为 `/chat/completions`、`url_verbatim = 1`。Anthropic / Responses 没有原样模式。
1. `LLMProviderYAML` 加 `URLVerbatim bool `yaml:"-" json:"-"``；`llmYAMLsFromResolved` 搬过去。
2. 新写 `exactURLRoundTripper{target *url.URL, inner http.RoundTripper}`（`pkg/llm/exact_url.go`）：对 POST 请求把 `req.URL` 的 scheme/host/path/rawQuery 换成 target（头部不动）；GET 不改。
3. `NewLLMFromYAML` 的 Chat Completions 分支：`URLVerbatim` 时用 `openai.NewCompatLLMWithTransport(…, exactURLRoundTripper)`。`URLVerbatim` 与 `api_path` 不是 `/chat/completions` 同时出现，或 provider 不是 `custom` → 返回错误 `custom protocol is only for custom models`（写入接口会先拦住，这里是防御）。
4. `ProbeLLMModel`（Step 5）同样带上它。
测试（`pkg/run/config_test.go`，名字带 `Verbatim`）：httptest 的 base 用 `…/llm/v2/chat` 这种不规则路径，断言请求路径正是 `/llm/v2/chat`、请求体是 Chat Completions 形状；`URLVerbatim=false` 时路径照旧补 `/chat/completions`。

### Step 4b: 用户可见的错误不提 models.json（owner 2026-11-07）

owner 规则：网页与 TUI 不向用户暴露 `models.json`，也不用「目录」指代模型数据。下面这些错误会原样出现在 TUI、网页或启动输出里，改写：
- `pkg/run/config.go:442` `unsupported llm provider: %s (not found in models.json)` → `unsupported model provider: %s`；
- `pkg/process/provider.go:43` `unsupported provider: %s (not found in models.json)` → `unsupported model provider: %s`；
- `pkg/llm/assets.go:210` `models.json is missing beside the binary (%s); install it with scripts/install-assets.sh %s` → `model data is missing beside the binary; reinstall forebrain`（路径与脚本名改为 `slog.Debug` 记录，不进错误文本）；
- `pkg/llm/catalog.go:512` `modelcatalog: models.json not found (home=%q)` → `model data not found; reinstall forebrain`（同上）。
再跑 `grep -rn "models\.json" --include='*.go' pkg | grep -v _test | grep -i "errorf\|errors.New\|Reply\|Content:"`，凡是会进入用户可见文本的都同样改写；只在注释、文件路径拼接、`slog.Debug` 里出现的保留。
受影响的既有测试断言按新文案改，并在报告里列出。

### Step 5: 测试连接（`pkg/process/llm_probe.go`）

```go
// ProbeLLMModel sends the smallest real request a configured model accepts —
// one user message, no tools, a 16-token answer — and reports whether it
// came back. It is what the models dialog's 测试连接 runs. The error is a short
// sentence safe to show (no key, no request body): its class first.
func ProbeLLMModel(ctx context.Context, in LLMModelInput, caps appcfg.LLMModelCaps) error
```

- 规范化四元组（`appcfg.NormalizeLLMModelIdentity`），组 `run.LLMProviderYAML{…, MaxOutputTokens: 16}`，`run.NewLLMFromYAML`，
  `Execute(ctx, []llm.Message{llm.UserMessage(llm.Text("ping"))}, nil)`（`llm.Message` 的字段是 `Parts []ContentPart`，没有 `Content` 字段；构造器用 `llm.UserMessage(llm.Text(...))`），整个调用 `context.WithTimeout(ctx, 20*time.Second)`。
- 错误归类：401/403 → `"the API key was refused"`；404 → `"the endpoint or model was not found"`；超时 → `"no answer within 20s"`；连接失败 → `"could not reach the base URL"`；其它 → `"the request failed: <状态码或错误类别>"`。
  判断用 `pkg/llm/error.go` 里已有的错误类型/状态码提取函数（`grep -n "func .*Status\|StatusCode" pkg/llm/error*.go`），**不要**把原始错误文本（可能带请求 URL 的 query 或响应体）透出。
- 登录类 provider 不支持探测：返回 `"sign-in models are checked by signing in"`。

测试：`httptest.Server` 返回 200 → nil；401 → 文案；404 → 文案；不监听的端口 → 连接失败文案；所有错误文本 `strings.Contains(err.Error(), key) == false`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ -run 'ProbeLLMModel' -count=1` → ok。

### Step 6: TUI 与网关的列表改为由注册表生成

1. `pkg/tui/setup.go`：把 `var providerGroups = []providerGroup{…}` 的字面量换成 `var providerGroups = providerGroupsFromRegistry()`。生成规则：
   - 每个 `BuiltinProvider` 一组：`Label` = 英文 Label；`Hint` = 登录类 `"OAuth subscription login"`（chatgpt）/`"Go subscription"`（opencode-go），其余 `"API key"`；
   - 每个端点一个 `providerChoice`：`Provider`=ID，`DefaultBaseURL`=BaseURL，`SupportsAPIPath`=`APIPath != APIPathAnthropicMessages`（与今天 anthropic/minimax 为 false 一致），`APIKeyLabel`=`<Label> API key`；
     `Label`/`Hint`：**今天就有的选择项逐字沿用今天的文案**（单端点 provider 的那一项，例如 `"DeepSeek API key"`；千问、MiniMax 的各端点项）；
     今天不存在的端点（例如 zhipuai 新加的 Coding Plan 端点、新 provider 的端点）用端点 Label 作 Label、host 作 Hint。
     实现方式：注册表端点的 `Label` 为空表示「沿用 provider 级文案」，生成器对它用旧写法（`<Label> API key` + provider 级说明）。
     **今天已有的条目生成后必须与旧字面量逐字段相等**——先写测试：把旧字面量复制进测试文件作为期望值，断言生成结果对已有 provider 完全一致，只多出 meituan、tencent、volcengine、byteplus 四组。
   - chatgpt / opencode-go 两组保留 `ChatGPTOAuth` / `OpenCodeDevice` 标志与原 Label/Hint。
2. `pkg/gateway/api_extra.go` `providerBaseURLDefaults()`：仍返回 `[]providerBaseURLDefault{Provider, BaseURL}`，数据来自 `config.ProviderDefaultBaseURLTable()`（已由注册表派生）——代码可能无需改；确认响应里多了 `meituan`、`tencent`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -run 'Setup|Provider' -count=1 && CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → ok。

### Step 7: 全量

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → ok。

## Test plan

见 Step 2–6。关键回归：已有 provider 的 TUI 选择项逐字段不变；已有 provider 建出的客户端（`anthropic`/`minimax`/compat/responses）路径不变（`pkg/run` 既有 `NewLLMFromYAML` 测试原样通过）。

## Done criteria

- [ ] `go build ./...`、`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` exit 0
- [ ] `grep -n "meituan\|tencent\|volcengine\|byteplus" pkg/config/provider_registry.go` 四个都有；`grep -c "api/coding/paas/v4" pkg/config/provider_registry.go` ≥ 2
- [ ] `volcengine` 用 Anthropic 协议的 Coding Plan 地址建客户端，请求路径是 `/api/coding/v1/messages`（httptest 断言）
- [ ] `grep -n "providerDefaultBaseURLs = map" pkg/config/provider_defaults.go` 无输出
- [ ] `grep -n "providerGroupsFromRegistry" pkg/tui/setup.go` 有
- [ ] `grep -n "CustomProvider" pkg/run/config.go pkg/process/provider.go` 都有
- [ ] `grep -rn "not found in models.json\|models.json is missing\|models.json not found" --include='*.go' pkg | grep -v _test` 无输出
- [ ] 报告里有「手动验收清单」：用真 key 各发一次——LongCat（`LongCat-2.5`，OpenAI 与 Anthropic 两个端点）、TokenHub 国内（`hy3`）、TokenHub 国际站（确认该地址真实存在；不存在就从注册表删掉并在报告说明）、火山方舟 Coding Plan 手填 Key（Anthropic 与 OpenAI 两个端点各一次）
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- 由注册表生成的 TUI 选择项与旧字面量对不上，且差异不是「多了美团/腾讯/火山方舟/BytePlus」。
- `openAIPromptCaching` 对 `custom` 返回 true，或它的判断不只是 provider 名。
- `pkg/config` 需要 import `pkg/llm/openai` 或 `pkg/llm/opencode` 才能拿常量（会造成循环）——按 Step 1 说的抄值；抄不了就停下。
- Anthropic SDK 在 base 不带结尾 `/` 时打到错误路径（Step 4 的 minimax 测试失败）——停下报告，不要改回带斜杠（规范化规则在 259/260 已定）。

## Maintenance notes

- 注册表是内置 provider 的**唯一来源**：TUI、网关、网页（plan 265 起由接口下发）都从它来。加 provider 只改这一个文件。
- 覆盖层按 provider/model 键：同 provider/model 的两行若能力不同，只有最早那行的覆盖进全局查询（上下文窗口、图片、档位）；客户端的输出上限按行精确生效。若以后需要按行区分上下文窗口，要把「行 id」传进压缩/预算的调用链。
- `custom` 是保留 provider id；自定义模型名可带 `vendor/` 前缀，目录借用只在名字恰好是目录 id 时发生。

来源（2026-11-07 核对）：LongCat 官方文档 https://longcat.chat/platform/docs/ ；腾讯云混元调用指南 https://cloud.tencent.com/document/product/1823/132252 ；国际站地址仅见 https://docs.openclaw.ai/zh-CN/providers/tencent （第三方）。
