# Plan 331: list_usable_models 数据契约——含当前模型、精简字段、label 可读、思考档位列表

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: baseline commit `4e8158e9` (dev). Run:
> ```bash
> git diff --stat 4e8158e9..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/run/orchestration_llm_test.go pkg/tool/format.go pkg/tool/format_test.go
> ```
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> **content** mismatch, treat it as a STOP condition (moved line numbers
> alone are not drift).

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（LLM 可见契约变更＋派发授权例外扩大；全部由 owner 需求驱动）
- **Depends on**: none
- **Category**: direction + tech-debt（owner 2026-11-20 五项需求的第 2/3/4/5-数据 项）
- **Planned at**: commit `4e8158e9`, 2026-11-20

## Why this matters

owner 对 `list_usable_models`（主代理挑选子代理模型的工具）提出四项数据侧要求：

1. **返回列表必须默认包含主代理当前正在使用的模型**——即使该行没有打开
   `SubagentAllowed` 开关。现状：`usableModels` 只列「开关开＋有凭据」的行加
   hand-off 行，当前会话模型若开关关着就不在列表里，主代理看不到自己正在用
   的模型。
2. **字段精简到只对 LLM 判断/决策有价值的**——`endpoint` 字段对 LLM 选模型
   毫无意义（owner 原话），删掉省 token。
3. **qodercn 等订阅模型的 wire id 不可识读**（如 `dfmodel`）——`model` 字段
   改为显示 label（如 `DeepSeek-Flash`），label 缺失时回退 wire id。
4. **每行带上该模型支持的思考档位列表与默认档位**——为 plan 333 的
   `reasoning_effort` 派发参数提供数据基础。

同时保持本仓库的一条硬不变量（代码注释称为「语义 9」）：**列表里有的行，
派发就必须能解析；列表没有的行，派发必须拒绝**（`usableModels` 与
`usableModelByID` 是同一查询对）。所以「当前模型入列」必须同步放开
`usableModelByID` 的开关检查，否则列表会教模型去派发一个必被拒绝的 id。

授权语义说明（写进代码注释）：`SubagentAllowed` 的语义是「0 = 只有用户自己
的选择能用它」（`pkg/state/config_store.go:455-459`）。会话当前模型正是用户
为本会话亲自挑选的，与 hand-off 例外（用户亲自为计划挑的行）同族，因此放开
开关不违背该语义；凭据规则不放开（没人能调用的行不是可运行的模型）。

## Current state

### 包分层约束（决定实现位置，先读）

`pkg/architecture` 的包图测试强制分层：Layer 3 的 `pkg/run`、`pkg/session`、
`pkg/turn` 之间**零同层边**——`pkg/run` 不能 import `pkg/turn`。但
`pkg/run/config.go:24,27` 已经 import 了 Layer 0 子包
`pkg/llm/anthropic` 与 `pkg/llm/opencode`，所以「协议能发哪些档位」的判断
可以在 run 本地做。测试文件不受包图约束（`pkg/architecture` 的检查跳过
`_test.go`），且 `pkg/run/orchestration_llm_test.go:30` 已经 import
`pkg/turn`——平价测试可以直接调 `turn.SetupReasoningEfforts` 对照。

### `pkg/run/subagent.go` — 工具与查询对（本计划主战场）

工具描述常量（`:539-543`，固定文本、不点名任何模型，避免缓存前缀漂移）：

```go
// listUsableModelsDescription is fixed text on purpose: it names no model, so
// adding one or flipping an authorization switch never moves the tool
// definition (and every cached prompt prefix with it). The list itself is
// read live from the model table on every call.
const listUsableModelsDescription = "List the configured models you may run a subagent on, read live from the model table. Pass a model's id as the model argument of subagent_run, subagent_send or subagent_fanout. Pick a model by its id. Rows with the same provider/model are different models; their endpoint shows how they differ."
```

工具应答（`:549-580`）——现状 JSON 每行含 `endpoint`，`model` 是 wire id：

```go
func listUsableModels(ctx context.Context, fac Factory) (string, error) {
	models, err := usableModels(ctx, fac)
	if err != nil {
		return "", err
	}
	current := ""
	if sel, ok := SessionModelSelectionFromContext(ctx); ok {
		current = strings.TrimSpace(sel.ModelID)
	}
	out := struct {
		Models []map[string]any `json:"models"`
		Note   string           `json:"note,omitempty"`
	}{}
	for _, m := range models {
		out.Models = append(out.Models, map[string]any{
			"id":         m.Row.ID,
			"provider":   m.Row.Provider,
			"model":      m.Row.Model,
			"endpoint":   appcfg.ModelEndpointLabel(m.Row.BaseURL, m.Row.APIPath, m.Row.URLVerbatim),
			"current":    current != "" && m.Row.ID == current,
			"handed_off": m.HandedOff,
		})
	}
	if len(out.Models) == 0 {
		out.Note = "No model is allowed for subagents you pick. Omit model to run on the conversation's model."
	}
	b, err := json.Marshal(out)
	...
}
```

行类型与列表查询（`:4448-4496`）：

```go
type usableModel struct {
	Row       state.LLMModel
	Entry     appcfg.AgentLLMProviderConfig
	Caps      *llm.ModelOverride
	HandedOff bool
}

func usableModels(ctx context.Context, fac Factory) ([]usableModel, error) {
	if fac.Owner == nil || fac.Owner.Deps == nil || fac.Owner.Deps.SessionStore == nil {
		return nil, fmt.Errorf("model store unavailable")
	}
	rows, err := (&state.LLMModelStore{DB: fac.Owner.Deps.SessionStore.DB()}).List(ctx)
	if err != nil {
		return nil, err
	}
	handOff := handOffModelID(ctx, fac)
	out := make([]usableModel, 0, len(rows))
	for _, row := range rows {
		entry, caps, cerr := configuredRow(row)
		if cerr != nil {
			continue
		}
		credentialed := strings.TrimSpace(row.APIKey) != "" || appcfg.CredentialsBackedProvider(row.Provider)
		if strings.TrimSpace(row.ID) == handOff && handOff != "" {
			// The hand-off exception (语义 8): ...
			if !credentialed {
				continue
			}
			out = append(out, usableModel{Row: row, Entry: entry, Caps: caps, HandedOff: true})
			continue
		}
		if !row.SubagentAllowed || !credentialed {
			continue
		}
		out = append(out, usableModel{Row: row, Entry: entry, Caps: caps})
	}
	return out, nil
}
```

派发侧孪生查询（`:4516-4559`，节选关键段）：

```go
func usableModelByID(ctx context.Context, fac Factory, id string) (usableModel, error) {
	...
	row, err := (&state.LLMModelStore{DB: fac.Owner.Deps.SessionStore.DB()}).Get(ctx, id)
	if err != nil {
		if errors.Is(err, state.ErrLLMModelNotFound) {
			// 遗留 hand-off（pre-table 配置条目）分支：仅 hand-off 走这里，本计划不动它
			...
		}
		return usableModel{}, err
	}
	entry, caps, err := configuredRow(row)
	if err != nil {
		return usableModel{}, err
	}
	credentialed := strings.TrimSpace(row.APIKey) != "" || appcfg.CredentialsBackedProvider(row.Provider)
	handedOff := strings.TrimSpace(row.ID) == handOffModelID(ctx, fac) && handOffModelID(ctx, fac) != ""
	if !handedOff && !row.SubagentAllowed {
		return usableModel{}, fmt.Errorf("model %q (provider/model) is not one you may pick for subagents; %s", llm.FormatProviderModel(row.Provider, row.Model), modelPickGuidance(ctx, fac))
	}
	if !credentialed {
		return usableModel{}, fmt.Errorf("model %q (provider/model) has no credentials; %s", llm.FormatProviderModel(row.Provider, row.Model), modelPickGuidance(ctx, fac))
	}
	return usableModel{Row: row, Entry: entry, Caps: caps, HandedOff: handedOff}, nil
}
```

行投影（`:4404-4415`）——`Entry` 永远带 `ID/Provider/Model/APIPath/Params`，
`Caps` 是该行能力列的活投影：

```go
func configuredRow(row state.LLMModel) (appcfg.AgentLLMProviderConfig, *llm.ModelOverride, error) {
	configRow, err := row.ConfigRow()
	if err != nil {
		return appcfg.AgentLLMProviderConfig{}, nil, err
	}
	caps, hasCaps := state.ModelOverrideFromCaps(configRow.Caps, configRow.CatalogProvider, configRow.Model)
	var override *llm.ModelOverride
	if hasCaps {
		override = &caps
	}
	return appcfg.ProviderFromModelRow(configRow), override, nil
}
```

会话选择从 ctx 读取（`pkg/run/config.go:1715-1721, 2181-2196`）：

```go
type PrimaryModelState struct {
	ModelID  string
	Provider string
	Model    string
	Effort   string
	Set      bool
}
func WithSessionModelSelection(ctx context.Context, sel PrimaryModelState) context.Context { ... sel.Set = true ... }
func SessionModelSelectionFromContext(ctx context.Context) (PrimaryModelState, bool) { ... }
```

### 档位数据的既有权威（只读，不改）

- `pkg/turn/models.go:227-302` —— 本计划要在 run 本地镜像的逻辑：

```go
func SetupReasoningEfforts(provider, model, apiPath string, levels []string) []string {
	return filterSendableEfforts(provider, model, apiPath, OrderedEfforts(levels))
}

func filterSendableEfforts(provider, model, apiPath string, levels []string) []string {
	switch opencode.ClientProtocol(provider, model, apiPath) {
	case opencode.ProtocolGoogle:
		return nil // generateContent 客户端没有 effort 参数
	case opencode.ProtocolAnthropic:
		out := make([]string, 0, len(levels))
		for _, level := range levels {
			if anthropic.AcceptsEffort(level) {
				out = append(out, level)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return append([]string(nil), levels...)
}

var effortLevelOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func OrderedEfforts(levels []string) []string {
	// 小写化、去重、按 effortLevelOrder 排序，未知档位按原相对顺序附在已知档位之后；
	// 空输入返回 nil。完整实现见该文件 :275-302，镜像时逐行对照。
}
```

- `pkg/llm/catalog.go:973-979` —— `ReasoningEffortsRow(rowID, provider, apiModel)`
  返回 (levels, known)：行自己的 override 优先于 catalog。override 注册表由进
  程采纳配置时安装（`SetModelOverrides`），**回合中途新增的行不在其中**——所
  以活行优先读 `Caps.Efforts`（见下）。
- `pkg/state/config_store.go:514-536` —— `ModelOverrideFromCaps` 把
  `caps.Efforts` 原样映射进 `llm.ModelOverride.Efforts`；
  `pkg/state/config_store.go:441`：`CapEfforts []string // nil = catalog; empty (non-nil) = explicitly no levels`。
- `pkg/llm/catalog.go:757-771` —— `ModelOverride.Efforts []string`（non-nil 即
  替换 catalog 档位）。
- `pkg/llm/catalog.go:1086-1091` —— label 的唯一来源：

```go
func ListingDisplayName(provider, apiModel string) string {
	if m, ok := builtinModel(provider, apiModel); ok {
		return strings.TrimSpace(m.Name)
	}
	return ""
}
```

  `builtinModel`（`:941-955`）按 `m.ID` 或 `m.APIModel`（忽略大小写）匹配
  wire id。订阅插件通过 `llm.SetBuiltinModels(provider, []llm.Model{...})`
  安装（qodercn 例：`plugins/subscriptions/qodercn/models.go:130-131`
  `dfmodel` → `DeepSeek-Flash`）。`llm.Model` 结构见 `pkg/llm/catalog.go:69-93`
  （`ID`、`Name`、`APIModel` 等字段）。
- `pkg/config/agent_llm.go:27-39` —— `appcfg.ReasoningEffort(params)` 读
  `reasoning.effort`；`pkg/config/llm_models.go:277-282`
  （`ProviderFromModelRow`）已把行的 `CapDefaultEffort` 合并进 `Params`（仅当
  params 未显式命名档位时）——所以 `appcfg.ReasoningEffort(m.Entry.Params)`
  就是该行的有效默认档位，不需要再读 `CapDefaultEffort`。

### `pkg/tool/format.go` — 卡片读取器（gateway web 也走这里）

`:4299-4344`：

```go
type usableModelsAnswer struct {
	Models []usableModelRow `json:"models"`
	Note   string           `json:"note"`
}

type usableModelRow struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Endpoint  string `json:"endpoint"`
	Current   bool   `json:"current"`
	HandedOff bool   `json:"handed_off"`
}

func readUsableModels(content string) (ReadableAnswer, bool) {
	for _, candidate := range toolAnswerCandidates(content) {
		var raw usableModelsAnswer
		if json.Unmarshal([]byte(candidate), &raw) != nil {
			continue
		}
		if raw.Models == nil && strings.TrimSpace(raw.Note) == "" {
			continue
		}
		var lines []string
		for _, row := range raw.Models {
			line := fmt.Sprintf("%s/%s %s", strings.TrimSpace(row.Provider), strings.TrimSpace(row.Model), strings.TrimSpace(row.Endpoint))
			if row.Current {
				line += " (current)"
			}
			if row.HandedOff {
				line += " (handed off)"
			}
			lines = append(lines, line)
		}
		return ReadableAnswer{Text: strings.Join(lines, "\n"), Note: strings.TrimSpace(raw.Note)}, true
	}
	return ReadableAnswer{}, false
}
```

`ReadableToolAnswer`（`:4249-4267`）的 `case "list_usable_models"` 路由到
`readUsableModels`；调用方之一是 `pkg/gateway/wsevents.go:980`
（`readableAnswerFor`，web 卡片读 `answer.Text`）。**本计划保持 Text 为纯文
本行**（web 面直接展示）；TUI 的 markdown 渲染是 plan 332 的事，不在这里做。
`toolAnswerCandidates`（`:4287-4297`）同时接受裸 JSON 与 live 卡的
`{"output": …}` 信封——保留这个机制。

### 要更新的既有测试（先读再改）

- `pkg/run/subagent_test.go:8996-9052` `TestListUsableModelsToolReadsTheTableLive`
  ——断言 endpoint 字符串在场（`:9018`）、无选择时不得有 `"current":true`
  （`:9024`）、有选择时 `"current":true` 与 `"handed_off":true` 在场
  （`:9043`）、凭据不得泄露、子代理内调用被 guard 拒绝。
- `pkg/run/subagent_test.go:8720-8772` `TestUsableModelsListAndDispatchAgree`
  ——开关/凭据/hand-off 的列表-派发一致性。
- `pkg/run/orchestration_llm_test.go:819-926` ——工具上桌闸门测试族
  （`rosterFixture`/`rosterRequestTools` helper）；空 roster 测试用的 ctx **没
  有**会话选择，本计划的当前行例外不会使它们变红。
- `pkg/tool/format_test.go:3644-3665` `TestListUsableModelsCard` ——钉住
  `readUsableModels` 的逐字输出（含 endpoint）与信封等价。
- 测试夹具（`pkg/run/subagent_test.go`）：`dispatchTestCtx()`（`:85-87`，
  session-1）、`subagentsEnabledConfig()`（`:349-354`）、
  `newModelArgumentFixture(t, cfg)`（`:8379`）、
  `fix.seedModelRow(t, provider, model, baseURL, key, allowed)`（`:8407-8417`，
  APIPath 固定 `/chat/completions`）、`fix.insertModelRow(t, state.LLMModel{...})`
  （`:8419` 起，原样存入含 id）。
- builtin 表安装/清理的既有范式：`pkg/run/config_test.go:2686-2687`
  `llm.SetBuiltinModels("volcengine", []llm.Model{...})` +
  `t.Cleanup(func() { llm.SetBuiltinModels("volcengine", nil) })`。

### 仓库约定

- 错误文案风格：小写开头、`model %q (provider/model) …`、尾随引导句（见
  `usableModelByID` 三条拒绝）。注释风格：解释「为什么」而非「是什么」，
  常引用 owner 裁决/语义编号（照抄现有文风）。
- 提交信息风格（`git log`）：`feat(config): …`、`tui: …`、`gateway,web: …`。

## Commands you will need

先设置环境（pkg/pdf 经 pkg/process 被间接编译，需要 pdfium 的 pkg-config 路径；
在代理会话里跑测试时用临时 home，避免碰真实 `~/.forebrain`）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 聚焦测试（run） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | ok，全绿 |
| 聚焦测试（tool） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | ok，全绿 |
| 单测复跑 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestName$' -count=1 -v` | PASS |
| 包图不变量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok（run 不得新增对 turn 的生产 import） |
| 格式 | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| 全量回归（收尾） | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 见 Step 5 说明 |

## Scope

**In scope**（只许改这些）:
- `pkg/run/subagent.go`
- `pkg/run/subagent_test.go`
- `pkg/run/orchestration_llm_test.go`
- `pkg/tool/format.go`
- `pkg/tool/format_test.go`

**Out of scope**（看起来相关，禁止动）:
- `pkg/tui/**` —— TUI markdown 卡片是 plan 332。
- `SubagentInput`/`SubagentSendInput`/`SubagentTask` 结构、
  `subagentModelOverrideForRef`、`handOffPlanEffort`、
  `planHandOffContractFormat` —— `reasoning_effort` 派发参数与 hand-off 机制
  改造是 plan 333。本计划**不改** `listUsableModelsDescription` 里关于
  reasoning_effort 的措辞（333 会加）。
- `pkg/turn/models.go`、`pkg/llm/catalog.go`、`pkg/config/**`、`pkg/state/**`
  ——档位/label/默认档位的权威实现只读不改。
- `pkg/gateway/**`、web 前端 —— `readUsableModels` 的 Text 变化会自然传导到
  web 卡片，这是预期效果；不改 gateway 代码。
- `state.PlanHandOff`、审批卡写 Effort 的四处（`pkg/turn/approval.go`、
  `pkg/tui/chat_surface.go:727`、`pkg/gateway/api_extra.go` 附近）——不动。
- `appcfg.ModelEndpointLabel` 本身保留（其它面还在用）；只是本工具不再调它。

## Git workflow

按仓库既有约定：**默认 in-tree 零 commit**（owner 手动提交）；如 operator 明
示提交，按 conventional 风格（例：`feat(run): list_usable_models lists the current row, labels and thinking levels`）。不要 push、不要 merge、不要动
其它分支。若 operator 指示 worktree 执行，记录 worktree 起始 SHA 供范围审计。

## Steps

### Step 1: run 本地档位 helper（镜像 turn 的决策）＋平价测试

在 `pkg/run/subagent.go` 的 `usableModelByID` 之后新增三个函数（名字固定，
plan 333 会复用 `usableModelRawLevels` 与 `sendableEffortLevels`）：

```go
// dispatchEffortOrder mirrors turn.effortLevelOrder: the one order every
// effort picker draws its levels in. pkg/run cannot import pkg/turn (the
// package graph pins Layer 3's zero same-layer edges), so the order lives
// here too; TestSendableEffortLevelsMatchThePickerSetup pins the parity.
var dispatchEffortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// orderedEffortLevels mirrors turn.OrderedEfforts: lower-case, de-duplicate,
// rank by dispatchEffortOrder, unknown levels after the known ones in their
// original relative order; no levels yields nil. The input is never modified.
func orderedEffortLevels(levels []string) []string { ... }

// sendableEffortLevels mirrors turn.SetupReasoningEfforts: order the levels,
// then keep only what the client this row speaks can put on the wire
// (opencode.ClientProtocol is the same decision pkg/run/config.go builds the
// client with). Google's generateContent carries no effort parameter;
// Anthropic's Messages client drops the levels AcceptsEffort rejects.
func sendableEffortLevels(provider, model, apiPath string, levels []string) []string {
	levels = orderedEffortLevels(levels)
	switch opencode.ClientProtocol(provider, model, apiPath) {
	case opencode.ProtocolGoogle:
		return nil
	case opencode.ProtocolAnthropic:
		out := make([]string, 0, len(levels))
		for _, level := range levels {
			if anthropic.AcceptsEffort(level) {
				out = append(out, level)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return append([]string(nil), levels...)
}

// usableModelRawLevels is the row's own level list before the protocol trim:
// the live capability projection wins (a row added mid-turn is not in the
// installed override registry), the catalog answers through ReasoningEffortsRow
// otherwise. A non-nil empty CapEfforts means "explicitly no levels".
func usableModelRawLevels(m usableModel) []string {
	if m.Caps != nil && m.Caps.Efforts != nil {
		return m.Caps.Efforts
	}
	levels, _ := llm.ReasoningEffortsRow(m.Entry.ID, m.Entry.Provider, m.Entry.Model)
	return levels
}

// usableModelEfforts is what list_usable_models reports: the levels this
// build can actually send for the row, in picker order.
func usableModelEfforts(m usableModel) []string {
	return sendableEffortLevels(m.Entry.Provider, m.Entry.Model, m.Entry.APIPath, usableModelRawLevels(m))
}
```

`orderedEffortLevels` 逐行镜像 `pkg/turn/models.go:275-302` 的
`OrderedEfforts`（把 `effortLevelOrder` 换成 `dispatchEffortOrder`）。
`subagent.go` 顶部 import 增加
`github.com/forebrain-harness/forebrain-harness/pkg/llm/anthropic` 与
`github.com/forebrain-harness/forebrain-harness/pkg/llm/opencode`
（`pkg/run/config.go:24,27` 已有同款 import，包图允许）。

在 `pkg/run/subagent_test.go` 新增平价测试
`TestSendableEffortLevelsMatchThePickerSetup`：对下列每个用例断言
`sendableEffortLevels(p, m, path, levels)` 与
`turn.SetupReasoningEfforts(p, m, path, levels)` 结果相等（用
`reflect.DeepEqual` 或 testify require.Equal，跟随该文件既有风格；需要 import
`pkg/turn`——测试文件不受包图约束，`orchestration_llm_test.go:30` 已有先例）。
用例矩阵照抄 `pkg/turn/models_test.go:1111-1150`（**不含** plugin factory 那
条，它需要注册全局工厂）：

| provider | model | apiPath | levels | want |
|---|---|---|---|---|
| openai | gpt-5 | "" | `{"High"," low ","none","low","","ultra"}` | `{"none","low","high","ultra"}` |
| anthropic | claude-sonnet-5 | "" | `{"none","minimal","low","medium","high","max"}` | `{"low","medium","high","max"}` |
| minimax | MiniMax-M2.7 | "" | `{"none","low","xhigh","medium","max"}` | `{"low","medium","xhigh","max"}` |
| custom | vendor-model | /v1/messages | `{"none","minimal","low","medium"}` | `{"low","medium"}` |
| custom | vendor-model | /messages/ | `{"none","minimal","low","medium"}` | `{"low","medium"}` |
| custom | vendor-model | /chat/completions | `{"low","none","minimal"}` | `{"none","minimal","low"}` |
| custom | vendor-model | /responses | `{"low","none","minimal"}` | `{"none","minimal","low"}` |
| opencode-go | gemini-4-argon-contributor | /chat/completions | `{"none","low","high"}` | nil |
| opencode | claude-sonnet-4-5 | /chat/completions | `{"none","low","high"}` | `{"low","high"}` |
| openai | gpt-5 | "" | nil | nil |
| openai | gpt-5 | "" | `[]string{}` | nil |

另断言输入 slice 不被修改（对照 turn 测试 :1115）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSendableEffortLevelsMatchThePickerSetup$' -count=1 -v` → PASS；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok。

### Step 2: 当前行例外——usableModels 与 usableModelByID 同步放开

`usableModels`：在 `handOff := handOffModelID(ctx, fac)` 之后读会话选择：

```go
current := ""
if sel, ok := SessionModelSelectionFromContext(ctx); ok {
	current = strings.TrimSpace(sel.ModelID)
}
```

在循环里 hand-off 分支之后、开关检查之前插入当前行分支（注释解释授权语义）：

```go
if current != "" && strings.TrimSpace(row.ID) == current {
	// The conversation's own model is the user's own choice for this
	// conversation — the switch's "only the user's own choices use it"
	// (owner 2026-11-10) already admits it, the same family as the
	// hand-off exception. It still needs credentials: a row nobody can
	// call is not a model to run on.
	if !credentialed {
		continue
	}
	out = append(out, usableModel{Row: row, Entry: entry, Caps: caps})
	continue
}
```

`usableModelByID`：在 `handedOff := …` 之后加：

```go
isCurrent := false
if sel, ok := SessionModelSelectionFromContext(ctx); ok {
	isCurrent = strings.TrimSpace(row.ID) != "" && strings.TrimSpace(sel.ModelID) == strings.TrimSpace(row.ID)
}
```

把开关拒绝条件改为 `if !handedOff && !isCurrent && !row.SubagentAllowed {`。
凭据检查、三条拒绝文案、遗留 pre-table hand-off 分支（`ErrLLMModelNotFound`
内）一律不动。更新两个函数的 doc 注释，把「当前行例外」与 hand-off 例外并
列写明（列表与派发同一查询对的不变量保持）。

测试：
1. 新增 `TestUsableModelsIncludesTheCurrentRowWithoutTheSwitch`
   （`pkg/run/subagent_test.go`，夹具范式照抄
   `TestUsableModelsListAndDispatchAgree`）：
   - `fix := newModelArgumentFixture(t, subagentsEnabledConfig())`；
     `id := fix.seedModelRow(t, "zhipuai", "glm-5.3", "https://cur.test/v1", "key-cur", false)`。
   - 无选择：`usableModels(dispatchTestCtx(), fix.fac)` 不含 id；
     `usableModelByID(dispatchTestCtx(), fix.fac, id)` 报
     "not one you may pick for subagents"。
   - `ctx := WithSessionModelSelection(dispatchTestCtx(), PrimaryModelState{ModelID: id, Provider: "zhipuai", Model: "glm-5.3"})`：
     列表含 id 且 `HandedOff == false`；`usableModelByID(ctx, fix.fac, id)` 成功。
   - 无凭据当前行：再 seed 一行 `key=""`、allowed=false，选择指向它 → 列表不
     含、派发报 "has no credentials"。
2. `TestUsableModelsListAndDispatchAgree` 追加一条腿：选择指向 `denied` 行
   （`WithSessionModelSelection`）后，`got[denied]` 为 true 且
   `usableModelByID` 解析成功；原有各腿不动。
3. `pkg/run/orchestration_llm_test.go` 新增
   `TestToolOrchestrationKeepsListUsableModelsForTheCurrentRow`：结构照抄
   `TestToolOrchestrationKeepsListUsableModelsForAHandedOffRow`（:861-880），
   把 `state.HandOffPlan(...)` 换成
   `ctx0 = WithSessionModelSelection(ctx0, PrimaryModelState{ModelID: row.ID, Provider: "zhipuai", Model: "glm-5.3"})`，
   断言工具仍在请求工具表里。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestUsableModels|TestToolOrchestration' -count=1` → 全 PASS。

### Step 3: 重写 listUsableModels 的 JSON 与工具描述

`listUsableModels` 的行循环替换为（`current` 的读取保留原样）：

```go
for _, m := range models {
	label := llm.ListingDisplayName(m.Entry.Provider, m.Entry.Model)
	if label == "" {
		label = m.Entry.Model
	}
	row := map[string]any{
		"id":       m.Entry.ID,
		"provider": m.Entry.Provider,
		"model":    label,
	}
	if current != "" && m.Row.ID == current {
		row["current"] = true
	}
	if m.HandedOff {
		row["handed_off"] = true
	}
	if efforts := usableModelEfforts(m); len(efforts) > 0 {
		row["efforts"] = efforts
		// The default is only worth reporting when it is a level the model
		// can actually be given: a default outside the sendable list would
		// teach the model a fake option.
		if def := appcfg.ReasoningEffort(m.Entry.Params); def != "" && slices.Contains(efforts, def) {
			row["default_effort"] = def
		}
	}
	out.Models = append(out.Models, row)
}
```

要点（逐条落实）：
- `endpoint` 键删除；`current`/`handed_off` 只在 true 时出现（省 token）。
- `id`/`provider`/`model` 改读 `m.Entry`（而非 `m.Row`）：遗留 pre-table
  hand-off 行的 `Row` 是零值，现状会列出空 id/provider/model；`Entry` 永远
  有值（`configuredRow`/`ResolveModelRef` 投影），此改动顺带修正该缺陷。
- `model` = label（`ListingDisplayName`），空则回退 wire id。
- `efforts` 为空时整个键省略；`default_effort` 仅当非空且属于 `efforts` 时
  出现（规则的理由写进注释，见上）。
- 空列表 note 文案不变。
- import 增加 `slices`。
- 更新 `listUsableModels` 的 doc 注释（不再提 endpoint）。

`listUsableModelsDescription` 替换为（仍是固定文本：不点名任何模型、不枚举
档位；**不要**提 reasoning_effort——那是 plan 333 的参数）：

```go
const listUsableModelsDescription = "List the configured models you may run a subagent on, read live from the model table. Pass a model's id as the model argument of subagent_run, subagent_send or subagent_fanout. Pick a model by its id; the model field is the name a person reads. Each row lists the thinking levels it can run at (efforts) and the level it runs at by default (default_effort)."
```

测试：
1. `TestListUsableModelsToolReadsTheTableLive`：把 :9018 的 endpoint 断言改
   为「`"endpoint"` 键不得出现」＋ rowID 仍在；其余断言（凭据不泄露、
   `"current":true`/`"handed_off":true`、guard）保留。
2. 新增 `TestListUsableModelsRowsCarryLabelsAndEfforts`：
   - `llm.SetBuiltinModels("labelprov", []llm.Model{{ID: "wire-id", Name: "Readable Label"}})`
     ＋ `t.Cleanup(func() { llm.SetBuiltinModels("labelprov", nil) })`
     （范式：`config_test.go:2686-2687`）。
   - `fix.insertModelRow(t, state.LLMModel{Provider: "labelprov", Model: "wire-id", BaseURL: "https://label.test/v1", APIPath: "/chat/completions", APIKey: "key-label", SubagentAllowed: true, CapEfforts: []string{"high", "low", "medium"}, CapDefaultEffort: "medium"})`。
   - 直接调 `listUsableModels(dispatchTestCtx(), fix.fac)`，unmarshal 后断言：
     `model == "Readable Label"`、`efforts == ["low","medium","high"]`（排序
     后）、`default_effort == "medium"`、`id == row.ID`、raw 文本不含
     `"endpoint"`。
   - 同 provider 第二行 wire id 无 builtin 条目（如 `Model: "plain-wire"`）
     → `model == "plain-wire"`。
   - 第三行 `CapDefaultEffort: "max"`、`CapEfforts: []string{"low","medium","high"}`
     → 无 `default_effort` 键（默认不在可发列表则不报）。
   - 第四行 `CapEfforts: []string{}`（non-nil 空）→ 无 `efforts` 键。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestListUsableModels' -count=1 -v` → 全 PASS。

### Step 4: pkg/tool 读取器对齐新契约

`pkg/tool/format.go`：
- `usableModelRow` 删 `Endpoint`，加
  `Efforts []string \`json:"efforts"\`` 与
  `DefaultEffort string \`json:"default_effort"\``；更新其 doc 注释
  （model 字段现在是给人读的 label）。
- `readUsableModels` 的行格式改为：

```go
line := fmt.Sprintf("%s (%s)", strings.TrimSpace(row.Model), strings.TrimSpace(row.Provider))
if len(row.Efforts) > 0 {
	line += " — thinking: " + strings.Join(row.Efforts, ", ")
	if strings.TrimSpace(row.DefaultEffort) != "" {
		line += " (default " + strings.TrimSpace(row.DefaultEffort) + ")"
	}
}
if row.Current {
	line += " (current)"
}
if row.HandedOff {
	line += " (handed off)"
}
```

- 更新 `readUsableModels` doc 注释。`usableModelsAnswer`、
  `toolAnswerCandidates`、空 note 逻辑、`ReadableToolAnswer` 路由不动。
- 旧会话回放的卡片仍带旧 JSON（有 endpoint、无 efforts）：新结构体忽略
  endpoint、efforts 为空 → 行退化为 `model (provider)`，可接受，不做兼容分支。

`pkg/tool/format_test.go` `TestListUsableModelsCard` 重写夹具与期望：

```go
roster := `{"models":[` +
	`{"id":"llm_a","provider":"zhipuai","model":"glm-5.3","current":true,"efforts":["low","medium","high"],"default_effort":"medium"},` +
	`{"id":"llm_b","provider":"qodercn","model":"DeepSeek-Flash","handed_off":true}` +
	`]}`
// want:
// "glm-5.3 (zhipuai) — thinking: low, medium, high (default medium) (current)\n" +
// "DeepSeek-Flash (qodercn) (handed off)"
```

空 note 腿与 live 信封腿（`strconv.Quote(roster)`）保留，期望随新格式。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestListUsableModelsCard' -count=1 -v` → PASS。

### Step 5: 全部门禁

**Verify**（逐条跑）:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/architecture -count=1` → 全部 ok
- `gofmt -l cmd pkg third_party plugins` → 无输出
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 期望全绿；若有**不在
  本计划触及包**的失败，先在干净基线（`git stash` 不可用时用 worktree 检出
  `4e8158e9`）复跑同一测试确认是否基线既有红灯，再决定归属——不要把基线红
  灯当成本计划回归，也不要把本计划引入的红灯归给基线。

## Test plan

新增测试汇总（全部在 in-scope 文件里）：
- `pkg/run/subagent_test.go`：
  `TestSendableEffortLevelsMatchThePickerSetup`（Step 1 矩阵＋输入不被修改）、
  `TestUsableModelsIncludesTheCurrentRowWithoutTheSwitch`（Step 2）、
  `TestListUsableModelsRowsCarryLabelsAndEfforts`（Step 3）。
- `pkg/run/orchestration_llm_test.go`：
  `TestToolOrchestrationKeepsListUsableModelsForTheCurrentRow`（Step 2）。
- 更新：`TestListUsableModelsToolReadsTheTableLive`、
  `TestUsableModelsListAndDispatchAgree`、`TestListUsableModelsCard`。
- 结构范式：列表/派发一致性测试照抄 `TestUsableModelsListAndDispatchAgree`；
  闸门测试照抄 `TestToolOrchestrationKeepsListUsableModelsForAHandedOffRow`；
  builtin 表安装照抄 `config_test.go:2686-2687`。

## Done criteria

全部成立：
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/architecture -count=1` exit 0
- [ ] 新增 4 个测试存在且 PASS（`-run` 逐一点名复跑）
- [ ] `grep -n '"endpoint"' pkg/run/subagent.go` 在 listUsableModels 函数体内无命中（`grep -n 'endpoint' pkg/run/subagent.go` 只应剩注释/无关引用；`ModelEndpointLabel` 调用在该函数内消失）
- [ ] `grep -n 'Endpoint' pkg/tool/format.go` 在 usableModelRow/readUsableModels 区域无命中
- [ ] `gofmt -l cmd pkg third_party plugins` 无输出
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0
- [ ] `git status --porcelain` 只显示 in-scope 五个文件（加 plans/README.md 若由你更新）
- [ ] `plans/README.md` 状态行已更新（除非 reviewer 声明由其维护）

## STOP conditions

停下来报告（不要即兴发挥）如果：
- "Current state" 的任何摘录与活代码内容不符（代码已漂移）。
- 某步验证失败两次（合理修复尝试后仍红）。
- 修复需要触碰 out-of-scope 文件（尤其：发现必须改 `pkg/turn` 或
  `pkg/llm` 才能拿到档位数据——本计划的前提是 run 本地镜像足够）。
- `pkg/architecture` 测试因新增 import 变红（说明镜像方案触了包图）。
- 发现既有测试钉住了「当前模型不得入列/不得派发」的相反语义（本计划假定没
  有；`TestUsableModelsListAndDispatchAgree` 的 denied 断言只在无选择 ctx
  下成立，若发现带选择的相反断言，STOP）。
- `turn.SetupReasoningEfforts` 的实现与摘录不符（镜像对象漂移）。

## Maintenance notes

- `dispatchEffortOrder`/`orderedEffortLevels`/`sendableEffortLevels` 是
  `pkg/turn` 对应物的**镜像**：turn 侧改排序、改协议裁剪规则时，必须同步这
  里——平价测试 `TestSendableEffortLevelsMatchThePickerSetup` 是护栏，评审
  时盯住它是否被削弱。根治方案（把决策下沉到 Layer 0 供两层共用）超出本期。
- plan 333 会复用 `usableModelRawLevels`/`sendableEffortLevels` 做
  `reasoning_effort` 校验，并给描述常量追加 reasoning_effort 句——改描述时
  保持「固定文本、不点名模型」的约束。
- 当前行例外使「有会话选择且当前行有凭据」时 roster 恒非空 →
  `list_usable_models` 恒上桌（`runner.go:1443-1448` 闸门）。这是预期：主代
  理总能看到自己的模型。若未来要恢复「空 roster 不上桌」的 token 节省，需
  重新权衡 owner 需求 2。
- `default_effort` 只在属于可发列表时上报：若未来允许「列表未知但默认已
  知」的行上报默认，需同步 plan 333 的校验语义（当前 333 镜像 gateway
  `subagentLevelRefused`：列表未知时只做协议检查）。
- 评审重点：列表-派发一致性（语义 9）是否在所有分支保持；凭据规则是否未被
  当前行例外放宽；旧 JSON 回放卡片是否仍可读。