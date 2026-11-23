# Plan 260: 运行时改读全局模型列表（库表 + 旧 llm_providers 的兼容视图）

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- pkg/config/agent_llm.go pkg/config/agents.go pkg/config/config.go pkg/config/load.go pkg/process/config_db.go pkg/run/config.go pkg/run/runner.go pkg/turn/models.go pkg/gateway/subagents_api.go pkg/tui/setup.go`
> → 必须为空（plan 259 不碰这些文件）。不为空就把「Current state」的摘录与实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（所有「用哪个模型」的解析都经过这里；改错就是全站跑错模型或起不来）
- **Depends on**: plans/259-llm-model-and-variable-tables.md（`state.LLMModelStore`）
- **Category**: migration
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **Executed**: 2026-11-08, worktree `.worktrees/exec-259-268`, 基线 `d9c45060`（未提交）——**DONE**，判据实测：
  `pkg/config` 新用例 5 组全 PASS、`pkg/run`/`pkg/turn`/`pkg/process`/`pkg/gateway`/`pkg/tui`/`pkg/config` 全包 ok、
  `pkg/process -run 'ConfigDB|Startup|ImportLegacy'` ok（含两个新用例）、`test-db.sh check` exit 0、build/vet/gofmt 绿、
  旧的 `ResolvedLLMConfigs`/`EffectiveDefaultLLM` 只剩三处允许的调用方（grep 无输出）。
  **偏差与限制**：
  ① 有意的语义变化改了既有断言：`pkg/turn/slash_test.go` 的 `TestModelChoicesAreTheAgentsConfiguredModels` →
  `…AreTheInstallWideModels`；`pkg/run/config_test.go` 删除「无自有列表回退 main」用例并把 `…PrefersDedicated`
  改写为 `TestResolvedAgentProvidersIsInstallWide`（全局视图让「按代理隔离的列表」这一层语义消失，符合计划 Step 4 的说明）。
  ② done 判据里的「`./...` 全量 exit 0」在本基线不可满足：`pkg/architecture` 有 HEAD 既有三条红（`pkg/llm`/`pkg/tool`
  各 32 个生产文件、`pkg/skill/audit_skill_test.go` 无同名生产文件），与 259 同因；受影响的六个包已逐个跑全绿。
  ③ 环境配方（写进 README 批次块）：worktree 里跑测试需要 pdfium 三件套 env（`PKG_CONFIG_PATH`、`CGO_CFLAGS`、
  `CGO_LDFLAGS` 带 `-Wl,-rpath`），否则 `pkg/config` 测试二进制报 `Library not loaded: @rpath/libpdfium.dylib`。
  ④ 留给 261/262 的已知口子：`pkg/gateway/api_extra.go` 的 `cloneSettingsRoot` 走 JSON 往返拷贝 `Root`，`LLMModels`
  是 `json:"-"` 因此在该 probe 里为空；今天无害（旧列表仍可解析），但只存在于表里的行在设置页校验里看不见。

## Why this matters

owner 2026-11-07 裁定：大模型配置改为**全局一张表** `fb_llm_models`（plan 259 已建），所有主代理共用；同一
provider/model 可以配多条（不同 base_url / api_path / key），所以一条模型配置靠**行 id** 指认；每个主代理只保存
自己的「默认模型」引用。

今天每个主代理在 `fb_config` 的 `agents` 段里各有一份 `llm_providers` 列表和一个 `default_model: "provider/model"`，
运行时到处用 `config.ResolvedLLMConfigs(def)` / `config.EffectiveDefaultLLM(def)` 读「这个代理自己的列表」。

本计划只做一件事：**让运行时从一个全局视图读模型**——视图 = 表里的行 + 旧 `llm_providers` 现场「提升」出的行
（给旧数据一个确定性的 id），去重后合成一份。这样：

- 表里一有行，运行时立即看得到（下一回合生效）；
- 还没迁移的老安装、测试里手写的 `llm_providers` 结构体/yaml，照样能跑；
- 旧行提升出的 id 是确定性的，plan 262 迁移时用**同一个 id** 写进表，之前记下的引用不失效。

真正把旧数据搬进表、切换写入路径，是 plan 262；按 id 选择模型是 plan 261。

## Current state

- 每个代理的条目类型 `pkg/config/agents.go:92-106`：

```go
type AgentLLMProviderConfig struct {
	Provider string           `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string           `yaml:"model,omitempty" json:"model,omitempty"`
	APIKey   string           `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL  string           `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	APIPath  string           `yaml:"api_path,omitempty" json:"api_path,omitempty"`
	Params   LLMRequestParams `yaml:"params,omitempty" json:"params,omitempty"`
	Models   StringList       `yaml:"-" json:"-"`
	APIKeyCleared bool `yaml:"-" json:"-"`
}
```

- 代理定义 `pkg/config/agents.go:31-60`：`LLMProviders []AgentLLMProviderConfig`（json `llm_providers`）、
  `DefaultModel string`（json `default_model`，"provider/model"，空 = 第一个条目）。
  `SubagentConfig.Model`（`:71`）也是 "provider/model"。
- 旧的解析函数 `pkg/config/agent_llm.go`：`ResolvedLLMConfigs(def)`（`:38-47`，把 `model: [a,b]` 展开成多条）、
  `EffectiveDefaultLLM(def)`（`:58-80`，按 `DefaultModel` 在**本代理列表**里找，找不到返回 nil）、
  `expandLLMProviderConfigs`（`:88-110`）。
- 运行时「这个代理有哪些模型」`pkg/run/config.go:283-296`：

```go
func resolvedAgentProviders(cfg *appcfg.Root, agentType string) []appcfg.AgentLLMProviderConfig {
	if def, ok := cfg.Agents.Definitions[agentType]; ok {
		if p := appcfg.ResolvedLLMConfigs(def); len(p) > 0 {
			return p
		}
	}
	if def, ok := cfg.Agents.Definitions["main"]; ok {
		return appcfg.ResolvedLLMConfigs(def)
	}
	return nil
}
```

  默认模型 `effectiveDefaultProvider`（`pkg/run/config.go:298-321`）：本代理定义有条目就用它的 `EffectiveDefaultLLM`，
  否则用 main 的；悬空的 `default_model` 报错（`errModelNotConfigured` 包一层文字）。
- 其余直接调用旧函数的运行时消费点（全部要换，见 Step 4）：
  - `pkg/run/runner.go:768-774`（`unloadedPrimaryModelFallback`）、`:943`（typed subagent 循环的判断）、`:1791`（`SubagentOwnModel`）；
  - `pkg/turn/models.go:684`（`ModelChoices`）、`:705`（`defaultModelIndex`）；
  - `pkg/gateway/subagents_api.go:271`；
  - `pkg/tui/setup.go:196`、`:211`（`mainLLMBaseURL`/`mainLLMAPIPath`）、`:1612`；
  - `pkg/config/agent_llm.go:442`（`ValidateAgentLLMConfigured`）、`pkg/config/load.go:859`（`validatePrimaryAgents` 的悬空检查）。
- **不在本计划换的旧调用**（plan 262 才处理，届时这些代码要么改写要么删除）：
  `pkg/gateway/api_extra.go` 的 providers 处理器（`:6239`、`:6249`、`:6631`、`:6708`）、`pkg/process/agentyaml.go:39`、`:100`。
  所以 **`ResolvedLLMConfigs(def)` 与 `EffectiveDefaultLLM(def)` 这两个旧函数保留原签名与原行为**，只加注释「legacy」。
- 配置组合 `pkg/process/config_db.go:123-158`（`composeSectionsFrom`）：读 yaml 的启动段，再把 `fb_config` 每一段
  `json.Unmarshal` 进 `Root` 对应字段。运行时视图走 `composeConfigFrom` → `appcfg.FinishLoad`（`.env` 合并、`${ENV}` 展开、校验）。
- 每回合同步靠指纹 `configSnapshotFingerprint`（`pkg/process/config_db.go` 里，`json.Marshal(root)` 再 sha256）：
  **`json:"-"` 的字段不进指纹**——新加的 `Root.LLMModels` 必须显式加进指纹，否则改模型页不会在下一回合生效。
- 测试大量用 `OpenOptions.Config` 直接塞一个手写的 `Root`（不经 `FinishLoad`），`llm_providers` 写在结构体或 yaml 里
  （约 47 个测试文件）。**所以兼容视图必须是一个「读时计算」的纯函数，不能依赖 Load 时的改写**。
- 协议由 api_path 决定（`pkg/run/config.go:437-466` `NewLLMFromYAML`）：provider 为 `anthropic`/`minimax` 永远走
  Anthropic Messages；其他：path 以 `/messages` 结尾 → Anthropic Messages，含 `/responses` → Responses，否则 Chat Completions
  （`/chat/completions` 与空等价，`pkg/llm/openai/compat_llm.go:97` 只在「非空且不等于 `/chat/completions`」时改写路径）。
  登录类 provider（`chatgpt`、`opencode`、`opencode-go`，判断函数 `config.CredentialsBackedProvider`）的 api_path 一直为空。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| config 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/config/... -count=1` | ok |
| 主要受影响包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... ./pkg/turn/... ./pkg/process/... ./pkg/gateway/... ./pkg/tui/... -count=1` | ok |
| 全量 | `make test` | ok（需要 libtokenizers 等前置；若 make 前置下载失败，改跑 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 并在报告说明） |
| vet | `go vet ./pkg/config/... ./pkg/run/... ./pkg/process/...` | exit 0 |

## Scope

**In scope**:
- `pkg/config/llm_models.go`（新建）、`pkg/config/llm_models_test.go`（新建）
- `pkg/config/config.go`（`Root` 加一个字段）、`pkg/config/agents.go`（`AgentLLMProviderConfig` 加 `ID`）
- `pkg/config/agent_llm.go`（旧函数加 legacy 注释；`ValidateAgentLLMConfigured` 改用新函数）
- `pkg/config/load.go`（只改 `validatePrimaryAgents` 的悬空检查）
- `pkg/process/config_db.go`（组合时读表、指纹带上模型行）
- `pkg/run/config.go`、`pkg/run/runner.go`
- `pkg/turn/models.go`
- `pkg/gateway/subagents_api.go`
- `pkg/tui/setup.go`（只换 Current state 列出的三处调用）
- 以上包内为新行为补的测试

**Out of scope**:
- `pkg/gateway/api_extra.go` 的 providers 处理器、`pkg/process/agentyaml.go`、`pkg/process/setup.go`——plan 262。
- 会话按 id 选模型（`fb_session_model_state.model_id`、`ModelChoice.ID`）——plan 261。
- 能力覆盖（`cap_*` 列）进运行时、自定义 provider——plan 263。
- 任何 `.env` 逻辑——plan 268。
- 前端、TUI 的界面。

## Steps

### Step 1: 新类型与规范化（`pkg/config/llm_models.go`）

```go
// LLMModelConfig is one configured model as the runtime reads it: a row of the
// install-wide fb_llm_models table, or an entry lifted from a pre-table
// agents.definitions.<id>.llm_providers list (LegacyLLMModelID names those).
type LLMModelConfig struct {
	ID       string
	Provider string
	Model    string
	BaseURL  string
	APIPath  string
	APIKey   string
	Params   LLMRequestParams
	Caps     LLMModelCaps
	// URLVerbatim (「自定义协议」): requests go to BaseURL as stored; APIPath
	// only names the protocol. plan 263 wires it into the clients.
	URLVerbatim bool
}

// LLMModelCaps overrides the model catalog for one row; a nil pointer, a nil
// slice or "" means "the catalog decides". plan 263 wires them into the runtime.
type LLMModelCaps struct {
	Images, PDF, Video, Audio, Reasoning, ReasoningOff *bool
	Efforts                         []string
	DefaultEffort                   string
	ContextWindow, MaxOutput        *int64
}
```

`Root`（`pkg/config/config.go:102`）加：

```go
	// LLMModels is the install's model table (fb_llm_models), composed in by
	// pkg/process; never persisted as a section and never marshaled — it
	// carries plaintext keys. ConfiguredLLMs is the one reader.
	LLMModels []LLMModelConfig `yaml:"-" json:"-"`
```

`AgentLLMProviderConfig` 加 `URLVerbatim bool `yaml:"-" json:"-"``（`ConfiguredLLMs` 从表行投影）与 `ID string `yaml:"-" json:"-"``（注释：「the global row this entry is; empty for an entry read straight from a legacy list」）。

规范化函数（**与 plan 259 的 `state.NormalizeLLMModelIdentity` 前四条完全一致，再多一条补全**）：

```go
// NormalizeLLMModelIdentity canonicalizes the four identity fields: provider
// lower-cased, model trimmed (case kept), base URL without trailing slashes,
// api_path with one leading slash and no trailing one. An empty api_path is
// filled with the protocol the provider speaks by default — /v1/messages for
// anthropic and minimax, /chat/completions for every other API-key provider —
// so "" and the explicit default are one identity. Login-backed providers
// (CredentialsBackedProvider) keep "": their endpoint comes from the login.
func NormalizeLLMModelIdentity(provider, model, baseURL, apiPath string) (string, string, string, string)
```

确定性 id：

```go
// LegacyLLMModelID is the id a pre-table entry gets: stable across processes
// and restarts, so a reference recorded against the lifted entry still names
// the same row once plan 262's migration writes it into fb_llm_models.
func LegacyLLMModelID(provider, model, baseURL, apiPath string) string
// = "llm-legacy-" + hex(sha256(p + "\x00" + m + "\x00" + b + "\x00" + a))[:16]，四个参数先过 NormalizeLLMModelIdentity
```

**Verify**: `go build ./pkg/config/` → exit 0。

### Step 2: 全局视图与引用解析（同文件）

```go
// ConfiguredLLMs is every model the install has configured, in display order:
// the table's rows (root.LLMModels, already in created_at order), then entries
// lifted from legacy llm_providers lists — main's first, then every other
// definition by name, each list in stored order, `model: [a, b]` expanded.
// Rows are de-duplicated by normalized identity; the first wins, so a table
// row beats a legacy entry naming the same endpoint.
func ConfiguredLLMs(root *Root) []AgentLLMProviderConfig
```

实现要点：
- 表行投影成 `AgentLLMProviderConfig{ID, Provider, Model, APIKey, BaseURL, APIPath, Params}`（四元组用规范化后的值）；
- 旧条目：对 `expandLLMProviderConfigs(def.LLMProviders)` 的每一条，**跳过 provider 和 model 都为空的占位条目**
  （`normalize()` 会给 main 塞一个 `{}`，见 `pkg/config/load.go:633-640`），规范化四元组，`ID = LegacyLLMModelID(...)`；
- 去重用规范化后的四元组做 map key；
- 返回值每条都 `CloneAgentLLMProviderConfig`，调用方改了也不影响 root。

```go
// ResolveModelRef finds the row a stored reference names. A reference is a
// row id (what every write stores from plan 262 on) or a legacy
// "provider/model" label, split on the first slash. A label resolves inside
// agentName's own legacy list first — the list it was written against — and
// then against the whole view by provider+model (case-insensitive), first
// match wins. ok is false when nothing matches.
func ResolveModelRef(root *Root, agentName, ref string) (AgentLLMProviderConfig, bool)

// EffectiveDefaultLLMFor resolves the model an empty selection runs on for an
// agent. In order: the agent's default_model reference; the first entry of the
// agent's own legacy list; main's default_model reference; main's first own
// legacy entry; the first row of the view. A reference that is a row id no
// longer present is skipped (logged once per process at Warn) — a deleted row
// must not stop the install; a legacy label that matches nothing returns nil,
// the loader's existing verdict (validatePrimaryAgents) for a dangling label.
func EffectiveDefaultLLMFor(root *Root, agentName string) *AgentLLMProviderConfig
```

- 「是不是 id」的判断：`ConfiguredLLMs` 里有 `ID == ref` 的行就是；否则含 `/` 当旧 label；否则视为悬空 id。
- `agentName` 空 → `"main"`。代理定义不存在 → 直接从「main 的 default」那一步开始。
- 日志：`pkg/config` 已经在 `atomic_write.go` 里用 `log/slog`，这里用 `slog.Warn("default model reference names no configured model", "agent", name)`
  并用 `sync.Map` 按 (agent, ref) 只打一次。**日志里不许出现 key**。

另加一个小判断给 typed subagent 用：

```go
// HasOwnModelChoice reports whether a definition picks its own model: a
// default_model reference, or a legacy list of its own.
func HasOwnModelChoice(root *Root, agentName string) bool
```

旧函数 `ResolvedLLMConfigs(def)`、`EffectiveDefaultLLM(def)`、`FindLLMProviderIndex` **签名与行为不变**，函数注释第一行改成
`// Legacy: reads one definition's own pre-table list. Only the providers handler and agentyaml (both replaced by plan 262) and the plan 262 migration may call it.`

**Verify**: `go build ./...` → exit 0。

### Step 3: 视图的单元测试（`pkg/config/llm_models_test.go`）

照 `pkg/config/agent_llm_test.go`（同包已有的 `EffectiveDefaultLLM` 测试，`grep -n "func Test" pkg/config/agent_llm_test.go`）的写法：

1. `NormalizeLLMModelIdentity`：`"OpenAI"`→`openai`；`base_url` 去尾 `/`；`api_path` `"responses"`→`/responses`；
   空 path：`deepseek`→`/chat/completions`，`anthropic`/`minimax`→`/v1/messages`，`chatgpt`/`opencode-go`→`""`；model 大小写保留。
2. `LegacyLLMModelID`：同一身份不同写法（尾斜杠、大写 provider、空 path 与显式 `/chat/completions`）得到同一个 id；不同 base_url 得到不同 id；前缀 `llm-legacy-`。
3. `ConfiguredLLMs`：表两行 + main 旧列表（含 `model: [a, b]`）+ 另一个代理旧列表；断言顺序、展开、去重（表行与旧条目同身份时保留表行的 id）、占位 `{}` 被跳过。
4. `EffectiveDefaultLLMFor`：
   - 代理 `default_model` 是表行 id → 该行；
   - 代理 `default_model` 是旧 label，且本代理旧列表里有 → 本代理那条（即使 main 也有同 provider/model 不同 base_url）；
   - 代理没写 default、有自己的旧列表 → 自己第一条；
   - 代理什么都没有 → main 的 default → main 第一条 → 视图第一条；
   - `default_model` 是不存在的 id → 跳过，落到下一步，不返回 nil；
   - `default_model` 是匹配不到的旧 label → nil；
   - 视图为空 → nil。
5. `ResolveModelRef`：同 provider/model 两行（不同 base_url）时，按 id 能分别命中；按 label 命中「本代理旧列表里那条」，没有旧列表时命中视图里第一条。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/config/ -run 'LLMModel|ConfiguredLLMs|EffectiveDefaultLLMFor|ResolveModelRef' -count=1 -v 2>&1 | grep -c '^--- PASS'` → ≥ 5。

### Step 4: 运行时消费点改用视图

逐个改（改完每个包跑一次它自己的测试）：

1. `pkg/run/config.go` `resolvedAgentProviders(cfg, agentType)` → 函数体改成 `return appcfg.ConfiguredLLMs(cfg)`（全局：每个代理都看得到全部模型）。保留函数名，注释改成「every configured model; agentType no longer narrows the list (owner 2026-11-07: one install-wide table)」。
2. `effectiveDefaultProvider`（`:298-321`）→ `entry := appcfg.EffectiveDefaultLLMFor(cfg, agentType)`；nil 时：
   代理或 main 的 `DefaultModel` 是旧 label → 保持现有报错文字（`... default_model %q matches no llm_providers entry`）；否则 `errModelNotConfigured`。
3. `pkg/run/runner.go:768-774` `unloadedPrimaryModelFallback` → `EffectiveDefaultLLMFor(r.AppCfg, r.activeAgentNameForModel())`。
4. `pkg/run/runner.go:943` typed subagent 循环：`if !ok || len(appcfg.ResolvedLLMConfigs(def)) == 0 { continue }` →
   `if !appcfg.HasOwnModelChoice(r.AppCfg, typeName) { continue }`（`def`/`ok` 不再需要就删掉对应变量）。
5. `pkg/run/runner.go:1791` `SubagentOwnModel` → `EffectiveDefaultLLMFor(r.AppCfg, agentType)`，但**只在 `HasOwnModelChoice(r.AppCfg, agentType)` 为真时**（否则会把 main 的默认当成该类型「自己的模型」——今天它只看该定义自己的列表）。
6. `pkg/turn/models.go:684` `ModelChoices` → 遍历 `appcfg.ConfiguredLLMs(cfg)`；`:705` `defaultModelIndex` → `EffectiveDefaultLLMFor(cfg, agentName)`。
   **本计划先保持按 provider+model 比较**（plan 261 改成按 id）。
7. `pkg/gateway/subagents_api.go:271` → 先 `HasOwnModelChoice(cfg, name)`，真才 `EffectiveDefaultLLMFor(cfg, name)`（与第 5 条同理）。
8. `pkg/tui/setup.go:196`、`:211`、`:1612` → `EffectiveDefaultLLMFor(cfg, <原来用的代理名>)`。
9. `pkg/config/agent_llm.go:442` `ValidateAgentLLMConfigured` → `EffectiveDefaultLLMFor(root, name)`（代理定义不存在时保持返回空结构的现状：先查定义存在再调）。
10. `pkg/config/load.go:859` `validatePrimaryAgents`：条件改成
    `strings.Contains(def.DefaultModel, "/") && ` 旧 label 在 `ResolveModelRef(r, id, def.DefaultModel)` 里找不到 → 原报错。
    即：**只有旧 label 悬空才报错；悬空的 id 不报错**（行被删时不能让整个安装起不来）。

完成后：`grep -rn "ResolvedLLMConfigs(\|EffectiveDefaultLLM(" --include='*.go' pkg | grep -v _test` 只允许出现在
`pkg/config/agent_llm.go`（定义与 `ConfiguredLLMs` 内部）、`pkg/gateway/api_extra.go`、`pkg/process/agentyaml.go`。

**Verify**: `go build ./... && CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... ./pkg/turn/... ./pkg/gateway/... ./pkg/tui/... ./pkg/config/... -count=1` → ok。
有失败先判断：是「测试断言了按代理隔离的模型列表」（例如断言代理 B 看不到 main 的模型）→ 这是本计划**有意**改变的语义，
按新语义改测试断言并在报告里逐条列出；其他失败按 bug 修。

### Step 5: 组合时把表读进来，指纹带上模型行（`pkg/process/config_db.go`）

1. `composeSectionsFrom`（`:123`）末尾 `return out, nil` 之前：

```go
	models, err := (&state.LLMModelStore{DB: db}).List(ctx)
	if err != nil {
		return appcfg.Root{}, fmt.Errorf("compose config: read model table: %w", err)
	}
	out.LLMModels = llmModelsFromRows(models)
```

   `llmModelsFromRows` 写在同文件：逐字段搬，`ParamsJSON` 非空时转 `appcfg.LLMRequestParams`（用 `appcfg.ParseLLMRequestParamsJSON`，
   解析失败返回带行 id 的错误，**不带 key**），`Cap*` 搬进 `Caps`。
2. `configSnapshotFingerprint(root)`：在现有 `json.Marshal(root)` 之后，把 `root.LLMModels` 用一个**本地 wire 结构体**
   （字段全部带 json tag，含 APIKey 与 Caps）再 marshal 一次，两段字节一起进 sha256。注释写明「LLMModels is json:"-" so it never
   leaves the process; it still has to move the fingerprint, or a model-page edit would not reach the next turn」。
3. `OpenOptions.Config` 旁路（`pkg/process/open.go:401` 附近 `if opts.Config == nil`）不变：测试传进来的 Root 不读表，
   视图会从它的旧列表现场提升。

在 `pkg/process` 加测试（照 `grep -n "TestComposeConfigDB" pkg/process/config_db_test.go` 找到的用例（如
`TestComposeConfigDBSectionOverridesYAML`）当范本——用真实状态库的组合测试，
测试名以 `TestConfigDB` 开头以便落在引擎矩阵里）：
- 表里插一行 → `composeConfig` 得到的 `Root.LLMModels` 有它，且 `ConfiguredLLMs` 第一条就是它；
- 只改这一行的 api_key → 指纹变；什么都不改 → 指纹不变。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process/ -run 'ConfigDB|Startup|ImportLegacy' -count=1` → ok。

### Step 6: 全量回归

**Verify**:
- `go build ./... && go vet ./pkg/config/... ./pkg/run/... ./pkg/process/...` → exit 0
- `make test`（或 `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`）→ ok
- `./scripts/test-db.sh check` → exit 0

## Test plan

- 新：`pkg/config/llm_models_test.go`（Step 3 的五组）、`pkg/process` 组合/指纹两例（Step 5）。
- 回归：既有 `EffectiveDefaultLLM`/`ResolvedLLMConfigs` 测试必须原样通过（旧函数没改）；`pkg/run` 的 Config/Runner 测试、
  `pkg/turn` 的 model 测试、`pkg/gateway` 的 subagents 测试原样通过，除非属于 Step 4 说明的「按代理隔离」断言。

## Done criteria

- [ ] `go build ./...` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` exit 0
- [ ] `grep -rn "ResolvedLLMConfigs(\|EffectiveDefaultLLM(" --include='*.go' pkg | grep -v _test | grep -v "pkg/config/agent_llm.go\|pkg/gateway/api_extra.go\|pkg/process/agentyaml.go"` 无输出
- [ ] `grep -n "LLMModels" pkg/process/config_db.go` 至少两处（组合、指纹）
- [ ] `grep -n 'LLMModels .*json:"-"' pkg/config/config.go` 一处
- [ ] 报告里逐条列出因「全局列表」语义而改动的既有测试断言（没有就写「无」）
- [ ] `git status --short` 只有 Scope 内文件
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- `normalize()`（`pkg/config/load.go:633-640`）塞给 main 的占位条目以外，还有别的「空条目有意义」的用法。
- 某个测试断言的是「代理 A 用的模型不能是代理 B 列表里的」这种**安全/隔离**语义（不是单纯的列表内容）——停下报告，不要直接改。
- `configSnapshotFingerprint` 的调用方除了 `SyncConfigFromDB` 与 `Open` 之外，还把它的输出暴露到接口或日志里。
- 发现 `Root` 在别处被整体 `json.Marshal` 后**发到接口或写进文件**（`LLMModels` 是 `json:"-"` 不会泄露，但如果有反射遍历全字段的序列化器，例如 yaml 带 `,inline`，就停下核对）。
- Step 4 改完后任一包的失败不能归入「按代理隔离的断言」或明显 bug 两类。

## Maintenance notes

- 视图里旧条目的 id 是 `llm-legacy-<hash>`；plan 262 迁移进表时**必须**用 `LegacyLLMModelID` 算出的同一个 id 写入，
  否则会话里已记下的 `model_id`（plan 261 起记）全部悬空。
- 「悬空 id 不报错、回落默认」是有意的：行可以在网页上删除，删除时由网关拒绝删「某代理默认」的那行（plan 265）；
  其余引用（子代理、记忆）悬空时回落。
- 指纹必须覆盖 `LLMModels`：以后给 `LLMModelConfig` 加字段，记得把它加进指纹的 wire 结构体。
- 全局视图之后，「代理 B 没配模型就继承 main 的模型」这一层语义消失：所有代理看同一张表，只有「默认」各自不同。
