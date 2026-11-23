# Plan 333: subagent_* 新增 reasoning_effort 派发参数；hand-off 思考档位改为提示词驱动的参数传递

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: baseline commit `4e8158e9` (dev). This plan
> **depends on plan 331** (`331-usable-models-roster-contract.md`) having
> landed first — it reuses 331's `sendableEffortLevels` /
> `usableModelRawLevels` helpers and appends to 331's
> `listUsableModelsDescription`. Run:
> ```bash
> git diff --stat 4e8158e9..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/run/config.go pkg/run/config_test.go pkg/tool/session_tools.go pkg/tool/session_tools_test.go
> grep -n "func sendableEffortLevels\|func usableModelRawLevels" pkg/run/subagent.go
> ```
> The grep must find both helpers (they are plan 331's). If they are absent,
> STOP: plan 331 has not landed. Excerpts below marked **(post-331)** show
> the code as 331 leaves it; all other excerpts are baseline `4e8158e9`. On
> a **content** mismatch against the live code, treat it as a STOP
> condition (moved line numbers alone are not drift).

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（LLM 可见契约变更＋hand-off 档位传递机制整体替换；owner 需求驱动）
- **Depends on**: 331（必须先落地；两者都改 `pkg/run/subagent.go`，必须串行）
- **Category**: direction（owner 2026-11-20 五项需求的第 5-参数 项＋机制改造指令）
- **Planned at**: commit `4e8158e9`, 2026-11-20

## Why this matters

owner 的两条指令：

1. **`subagent_run` / `subagent_send` / `subagent_fanout` 必须新增思考强度
   参数 `reasoning_effort`**（owner 定名）：非必传；不传则用所选模型行自己的
   默认档位。plan 331 已让 `list_usable_models` 每行返回 `efforts`（可发送
   档位列表）与 `default_effort`，本计划补上派发侧的参数与校验。
2. **hand-off 思考档位的传递机制整体替换**。现状是「用户在 exit_plan_mode
   审批卡选模型＋档位 → 写进会话 mode state（`state.PlanHandOff.Effort`）→
   派发时 `subagentModelOverrideForRef` 按 row id 从 state 读回」。owner 要求
   改为「用户审批 → 把用户选的 model row id 和 reasoning_effort **写进给
   LLM 的提示词**（exit_plan_mode 的 `on_completion` 契约与循环 reminder）→
   LLM 在调用 subagent_* 时把它们作为参数原样传入」。即：**state 读回路径
   删除，提示词成为唯一传递通道**。

保留不动的部分（审批与记录链仍然完整）：`state.PlanHandOff.Effort` 字段、
审批答案解析（`actionHandOffModel`）、各端审批写入器、`handOffModelID` 的
授权例外（hand-off 行免开关入列/可派发）。它们现在的职责是**喂提示词**，
不再被派发路径读回。

## Current state

### 1. 三个派发输入结构（`pkg/run/subagent.go:90-151`）

`SubagentInput`（subagent_run）、`SubagentSendInput`（subagent_send）、
`SubagentTask`（subagent_fanout 的 tasks 元素）各有同一段 Model 字段：

```go
	// Model is a configured row id, never a provider/model pair: two rows of
	// one pair are two models. The description is fixed text on purpose — it
	// must not enumerate the models or name the tool that lists them (that
	// tool rides a request only while the roster is non-empty), or a table
	// edit or the roster state would change the tool definition and every
	// cached prompt prefix with it.
	Model string `json:"model,omitempty" jsonschema_description:"Optional id of a configured model you may run a subagent on. Omit to use the model this subagent type is configured with, else the conversation's model."`
```

（Send/Task 上的注释是短版 `// Model is a configured row id; see SubagentInput.Model.`。）

**仓库硬规则**：描述文本含逗号时**必须**用 `jsonschema_description` tag，
不能用 `jsonschema:"description=…"`——后者在第一个逗号处截断
（`TestJSONSchemaDescriptionTagsSurviveReflection` 全模块扫这个）。描述必须
是固定文本：不点名任何模型、不点名 `list_usable_models`（该工具只在 roster
非空时随请求出现，写进描述会让工具定义随表状态漂移、击穿缓存前缀）。
现有钉死测试：`pkg/run/subagent_test.go:8978-8989`
`TestSubagentModelArgumentDescriptionIsFixedText`（反射读三个结构的 tag 与
常量逐字比对）。

### 2. 派发调用点（三处，都在 `pkg/run/subagent.go`）

- `:417`（subagent_run）与 `:3728`（subagent_send）：
  ```go
  			if in != nil {
  				pin, err := subagentModelOverrideForRef(ctx, fac, in.Model)
  				if err != nil {
  					return "", err
  				}
  				ctx = WithSubagentModelOverride(ctx, pin)
  			}
  ```
- `:3533-3540`（subagent_fanout，全部 pin 先解析、任一失败整单失败）：
  ```go
  			taskPins := make([]SubagentModelOverride, len(in.Tasks))
  			for i := range in.Tasks {
  				pin, err := subagentModelOverrideForRef(ctx, fac, in.Tasks[i].Model)
  				if err != nil {
  					return "", err
  				}
  				taskPins[i] = pin
  			}
  ```

### 3. 要替换的读回机制（`pkg/run/subagent.go:4579-4633`）

```go
// handOffPlanEffort names the thinking level this conversation's approved
// hand-off recorded, from the session's mode state; "" when the conversation
// handed nothing off, named no level, or the state cannot be read — all three
// leave the row's own configuration in charge.
func handOffPlanEffort(ctx context.Context, fac Factory) string {
	if fac.Owner == nil {
		return ""
	}
	sid := llm.AgentSessionIDFromContext(ctx)
	if strings.TrimSpace(sid) == "" {
		return ""
	}
	st, err := state.Get(fac.Owner.StateRoot(), sid)
	if err != nil || st.PlanHandOff == nil {
		return ""
	}
	return strings.TrimSpace(st.PlanHandOff.Effort)
}

// subagentModelOverrideForRef resolves the model argument's row id into the
// pin a dispatch runs under: the row read live, its client entry and its
// version carried along, so this subagent's requests go to exactly this row
// even when it joined the table after this turn's configuration was adopted.
// An empty id is no statement — the zero override leaves the dispatch on its
// ordinary model chain.
func subagentModelOverrideForRef(ctx context.Context, fac Factory, id string) (SubagentModelOverride, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return SubagentModelOverride{}, nil
	}
	usable, err := usableModelByID(ctx, fac, id)
	if err != nil {
		return SubagentModelOverride{}, err
	}
	entry, caps := usable.Entry, usable.Caps
	// The row's own configuration is the level this dispatch runs at, unless
	// the hand-off the user approved named one: then that is what they chose,
	// and an empty one keeps the row in charge (every record written before
	// the approval card had a second screen is that case).
	effort := appcfg.ReasoningEffort(entry.Params)
	if usable.HandedOff {
		if picked := handOffPlanEffort(ctx, fac); picked != "" {
			effort = picked
		}
	}
	return SubagentModelOverride{
		ModelID:  strings.TrimSpace(entry.ID),
		Provider: strings.TrimSpace(entry.Provider),
		Model:    strings.TrimSpace(entry.Model),
		Effort:   effort,
		Entry:    &entry,
		Version:  usable.Row.UpdatedAt,
		Caps:     caps,
	}, nil
}
```

`handOffPlanEffort` 的调用者只有 `subagentModelOverrideForRef` 和测试
`TestHandOffPinnedEffortReachesTheOverride`（`subagent_test.go:9355,9383`）。
`subagentModelOverrideForRef` 的全部调用点：生产 3 处（上面第 2 节）＋测试
4 处（`subagent_test.go:9072,9358,9368,9386`）。

### 4. 档位校验的既有语义（gateway，照抄不改）

`pkg/gateway/subagents_api.go:976-999` `subagentLevelRefused`——子代理配置页
保存时的校验，本计划的派发校验必须与它同语义：

```go
func subagentLevelRefused(r *http.Request, models *config.Root, agentID string, entry config.SubagentConfig) error {
	level := strings.TrimSpace(entry.ReasoningEffort)
	if level == "" {
		return nil
	}
	row, ok := config.ResolveModelRef(models, agentID, strings.TrimSpace(entry.Model))
	if !ok {
		return nil
	}
	refuse := func() error { … }
	if len(turn.SetupReasoningEfforts(row.Provider, row.Model, row.APIPath, []string{level})) == 0 {
		return refuse()
	}
	if levels, known := llm.ReasoningEffortsRow(row.ID, row.Provider, row.Model); known && len(levels) > 0 {
		if !slices.Contains(turn.SetupReasoningEfforts(row.Provider, row.Model, row.APIPath, levels), level) {
			return refuse()
		}
	}
	return nil
}
```

语义三条：**空 = 跟随行默认，永不校验**；**协议检查恒做**（该协议发不出这个
档位就拒，如 Google 协议没有档位参数、Anthropic 协议发不了 none/minimal）；
**成员检查只在档位列表已知且非空时做**（列表未知 = 本进程不了解这个模型的
档位，猜着拒会误伤；显式空列表同样跳过成员检查）。

`pkg/run` 不能 import `pkg/turn`（`pkg/architecture` 包图：Layer 3 零同层
边）。plan 331 已在 `pkg/run/subagent.go` 落了 run 本地镜像（**(post-331)**，
与 `turn.SetupReasoningEfforts` 有逐例平价测试
`TestSendableEffortLevelsMatchThePickerSetup` 钉住）：

```go
func sendableEffortLevels(provider, model, apiPath string, levels []string) []string
func usableModelRawLevels(m usableModel) []string
```

`sendableEffortLevels` = 排序去重＋按协议裁剪（等价
`turn.SetupReasoningEfforts`）；`usableModelRawLevels` = 行的原始档位列表
（`Caps.Efforts` 非 nil 优先——含显式空；否则
`llm.ReasoningEffortsRow(m.Entry.ID, m.Entry.Provider, m.Entry.Model)`）。
本计划直接复用，**不要**再写一份协议判断。

`usableModel` 结构（`subagent.go:4448-4453`，331 不改它）：

```go
type usableModel struct {
	Row       state.LLMModel
	Entry     appcfg.AgentLLMProviderConfig
	Caps      *llm.ModelOverride
	HandedOff bool
}
```

`Entry` 携带 `Provider`/`Model`/`APIPath`/`Params`；行默认档位 =
`appcfg.ReasoningEffort(entry.Params)`（`pkg/config/agent_llm.go:27-39`；
`configuredRow` 经 `ProviderFromModelRow` 已把 `CapDefaultEffort` 合并进
params）。

### 5. 拒绝文案的仓库样式（`subagent.go:4553,4556`）

```go
fmt.Errorf("model %q (provider/model) is not one you may pick for subagents; %s", llm.FormatProviderModel(row.Provider, row.Model), modelPickGuidance(ctx, fac))
fmt.Errorf("model %q (provider/model) has no credentials; %s", llm.FormatProviderModel(row.Provider, row.Model), modelPickGuidance(ctx, fac))
```

注意 `(provider/model)` 是字面量注释词，`%q` 里是
`llm.FormatProviderModel(provider, model)` 的显示串。新拒绝文案沿用这个
形状。

### 6. 续跑 pin 重建（`subagent.go:2474-2504` `subagentRecordPin`）

```go
	reread, err := usableModelByID(ctx, fac, modelID)
	if err != nil {
		return SubagentModelOverride{}, err
	}
	entry, caps := reread.Entry, reread.Caps
	return SubagentModelOverride{
		ModelID:  strings.TrimSpace(entry.ID),
		Provider: strings.TrimSpace(entry.Provider),
		Model:    strings.TrimSpace(entry.Model),
		Effort:   appcfg.ReasoningEffort(entry.Params),
		Entry:    &entry,
		Version:  reread.Row.UpdatedAt,
		Caps:     caps,
	}, nil
```

**现状问题**：live 重读把 `Effort` 无条件重置为行默认——派发时显式传的档位
在第一次续跑就丢。而配置型子代理路径（`prepareSubagent` 的
`cat.Model(agentType)` 分支，`:2002-2003`）本来就把 effort 冻结在 pin 里随
记录走。没有任何测试钉住这个重置行为（
`TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation` 只断言 Caps）。
本计划改为：**记录里有档位就保留，没有才落行默认**。

pin 的去向（背景，不改）：`prepareSubagent`（`:1987-2041`）把
`pin.Effort` 写进 `entry.ReasoningEffort`（`:2038`），子代理的每次请求都按
它发。

### 7. hand-off 提示词（`pkg/tool/session_tools.go`）

契约模板 `planHandOffContractFormat`（`:627-685`），占位符注释（`:623-626`）：
`%[1]s` plan 文件路径、`%[2]s` 显示名（用两次）、`%[3]s` 传给 subagent_run 的
模型引用（现在是裸 row id）、`%[4]s` `state.ImplementationHeading`。派发行
（`:643`）：

```
Read the whole plan file now. Call subagent_run with subagent_type "general-purpose", model "%[3]s", a short title, and a task made of these four parts, in order:
```

构造函数（`:687-692`）：

```go
// HandOffPlanExecutionPrompt is the contract the tech lead executes a handed-
// off plan under. modelRef is what the dispatch passes to subagent_run's
// model argument: the configured row id, never a provider/model pair.
func HandOffPlanExecutionPrompt(planFile, modelDisplay, modelRef string) string {
	return fmt.Sprintf(planHandOffContractFormat, planFile, modelDisplay, modelRef, state.ImplementationHeading)
}
```

调用点（全部，已 grep 核实）：
- `session_tools.go:796`——exit_plan_mode 审批通过（hand-off）时的
  `on_completion`；同一结果 map 的 `hand_off_model`（`:794`）现在只有
  id/provider/model 三键：
  ```go
  					display := llm.FormatProviderModel(handOff.Provider, handOff.Model)
  					ref := strings.TrimSpace(handOff.ModelID) // B1 guarantees a row id
  					b, _ := json.Marshal(map[string]any{
  						"mode":           string(restored),
  						"plan_file":      planFile,
  						"hand_off_model": map[string]any{"id": handOff.ModelID, "provider": handOff.Provider, "model": handOff.Model},
  						"message":        "Exited plan mode. The user handed this plan to " + display + ": dispatch one executor subagent on that model to carry it out in its own git worktree, then review its work. Do not implement the plan yourself.",
  						"on_completion":  HandOffPlanExecutionPrompt(planFile, display, ref),
  					})
  ```
  `handOff` 来自 `actionHandOffModel(act)`（`:487-513`，已解析 effort 字段，
  不改）。
- `pkg/run/config.go:1008`——循环 reminder 的 full 形态
  （`buildPlanHandOffReminder`，`:999-1009`；sparse 形态不含契约、不动）：
  ```go
  	display := llm.FormatProviderModel(h.Provider, h.Model)
  	ref := strings.TrimSpace(h.ModelID)
  	return planImplementationReminderMarker + " - read carefully.\n\n" + tool.HandOffPlanExecutionPrompt(planFile, display, ref)
  ```
  `h` 是 `state.PlanHandOff{ModelID, Provider, Model, Effort}`
  （`pkg/state/mode.go:49-57`）。

### 8. 相关测试现状

- `pkg/tool/session_tools_test.go:987-1059`
  `TestHandOffContractRendersVerbatim`：整份契约逐字 golden（`:992-1050`），
  等值断言 `:1051`，marker 循环 `:1054`。golden 的派发行在 `:1008`。
- `session_tools_test.go:1065-1107`
  `TestExitPlanModeHandOffRecordsTheRowAndReturnsTheContract`：审批 JSON 无
  effort；断言 `hand_off_model` 三键、message 逐字、`on_completion` marker。
- `session_tools_test.go:1109-1112` 有一段**将过时**的注释（
  `TestActionHandOffModelCarriesEffort` 上方）：说「executor dispatch resolves
  the mode-state row」——机制改后档位走提示词，注释要改写（测试本身不动）。
- `pkg/run/subagent_test.go:9326-9393`
  `TestHandOffPinnedEffortReachesTheOverride`：钉的就是旧读回机制，整个
  重写。fixture：`newModelArgumentFixture(t, cfg())`（`:8379`）、
  `fix.insertModelRow(t, state.LLMModel{…})`（`:8419`，返回带 ID 的行）、
  `dispatchTestCtx()`（`:85-87`，session-1）。两行 openai
  `/chat/completions`：`gpt-effort-hand` params `{"reasoning":{"effort":"low"}}`、
  `gpt-effort-other` params medium；hand-off 记 xhigh。
- `subagent_test.go:9058-9119`
  `TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation`：`:9072` 调
  `subagentModelOverrideForRef`（要补第 4 参 `""`）；无 effort 断言。
- `pkg/run/config_test.go:2704-2769`
  `TestPlanHandOffReminderIsTheTechLeadContract`：断言 reminder 含
  `model "llm_row2"`（子串，新子句形态下依然成立，不用改）；`:2737` 直接调
  `buildPlanHandOffReminder(…, "sparse")`。
- `state.LLMModel.CapEfforts []string`（`pkg/state/config_store.go:441`）：
  nil = 目录；**非 nil 空 = 显式无档位**。`CapDefaultEffort string`（`:442`）。

### 9. 工具描述常量（**(post-331)**，`subagent.go`）

plan 331 把 `listUsableModelsDescription` 换成：

```go
const listUsableModelsDescription = "List the configured models you may run a subagent on, read live from the model table. Pass a model's id as the model argument of subagent_run, subagent_send or subagent_fanout. Pick a model by its id; the model field is the name a person reads. Each row lists the thinking levels it can run at (efforts) and the level it runs at by default (default_effort)."
```

本计划在句尾追加 reasoning_effort 一句（步骤 8）。

## Steps

所有命令在仓库根目录执行。环境准备（每个新 shell 都要）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

### Step 1 — 三个输入结构加 ReasoningEffort 字段＋钉死测试

在 `pkg/run/subagent.go` 的 `SubagentInput`、`SubagentSendInput`、
`SubagentTask` 三个结构里，紧跟各自 `Model` 字段之后加（三处逐字相同）：

```go
	// ReasoningEffort is fixed text for the same reason as Model: it names no
	// model and no tool, so the definition never moves with the table.
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema_description:"Optional thinking level for this subagent, one of the levels the chosen model supports. Omit to run at the model's default thinking level."`
```

（Send/Task 上注释可短写 `// ReasoningEffort is fixed text; see SubagentInput.ReasoningEffort.`。
描述含逗号，必须 `jsonschema_description`。）

在 `pkg/run/subagent_test.go` 的
`TestSubagentModelArgumentDescriptionIsFixedText`（`:8978`）之后加孪生测试：

```go
func TestSubagentReasoningEffortArgumentDescriptionIsFixedText(t *testing.T) {
	const want = "Optional thinking level for this subagent, one of the levels the chosen model supports. Omit to run at the model's default thinking level."
	for _, typ := range []reflect.Type{reflect.TypeOf(SubagentInput{}), reflect.TypeOf(SubagentSendInput{}), reflect.TypeOf(SubagentTask{})} {
		field, ok := typ.FieldByName("ReasoningEffort")
		if !ok {
			t.Fatalf("%s has no ReasoningEffort field", typ.Name())
		}
		if tag, _ := field.Tag.Lookup("jsonschema_description"); tag != want {
			t.Fatalf("%s.ReasoningEffort description =\n%q\nwant the fixed text", typ.Name(), tag)
		}
	}
}
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentReasoningEffortArgumentDescriptionIsFixedText|TestSubagentModelArgumentDescriptionIsFixedText|TestJSONSchemaDescriptionTagsSurviveReflection' -count=1
```

预期：ok。（第三个测试若在别的包，用
`CGO_ENABLED=1 go test -tags fts5 ./pkg/... -run TestJSONSchemaDescriptionTagsSurviveReflection -count=1` 找到并跑它。）

### Step 2 — subagentModelOverrideForRef 加 effort 参数：校验＋删除 state 读回

`pkg/run/subagent.go`：

a. import 块加 `"slices"`（标准库组，`reflect` 与 `strings` 之间按字母序）。

b. **整个删除** `handOffPlanEffort`（`:4579-4596`，含注释）。

c. `subagentModelOverrideForRef` 改为（注释一并替换；`%q` 显示串与
`(provider/model)` 字面量沿用第 5 节样式）：

```go
// subagentModelOverrideForRef resolves the model argument's row id into the
// pin a dispatch runs under: the row read live, its client entry and its
// version carried along, so this subagent's requests go to exactly this row
// even when it joined the table after this turn's configuration was adopted.
// An empty id is no statement — the zero override leaves the dispatch on its
// ordinary model chain. effort is the caller's reasoning_effort argument: the
// user's approval choices reach it through the prompt, never through session
// state (owner 2026-11-20); empty runs at the row's own default level.
func subagentModelOverrideForRef(ctx context.Context, fac Factory, id, effort string) (SubagentModelOverride, error) {
	id = strings.TrimSpace(id)
	effort = strings.TrimSpace(effort)
	if id == "" {
		if effort != "" {
			return SubagentModelOverride{}, fmt.Errorf("reasoning_effort needs a model argument naming the row it applies to; pass model, or omit reasoning_effort.")
		}
		return SubagentModelOverride{}, nil
	}
	usable, err := usableModelByID(ctx, fac, id)
	if err != nil {
		return SubagentModelOverride{}, err
	}
	entry, caps := usable.Entry, usable.Caps
	if effort == "" {
		effort = appcfg.ReasoningEffort(entry.Params)
	} else if err := dispatchEffortRefused(entry, usable, effort); err != nil {
		return SubagentModelOverride{}, err
	}
	return SubagentModelOverride{
		ModelID:  strings.TrimSpace(entry.ID),
		Provider: strings.TrimSpace(entry.Provider),
		Model:    strings.TrimSpace(entry.Model),
		Effort:   effort,
		Entry:    &entry,
		Version:  usable.Row.UpdatedAt,
		Caps:     caps,
	}, nil
}

// dispatchEffortRefused judges one reasoning_effort argument with the same
// semantics the gateway's subagentLevelRefused applies to a saved config:
// the protocol check always runs; the membership check only when the row's
// level list is known and non-empty — a model this process knows no levels
// for is not judged, and guessing would refuse a level the client can send.
func dispatchEffortRefused(entry appcfg.AgentLLMProviderConfig, usable usableModel, effort string) error {
	display := llm.FormatProviderModel(entry.Provider, entry.Model)
	if len(sendableEffortLevels(entry.Provider, entry.Model, entry.APIPath, []string{effort})) == 0 {
		return fmt.Errorf("model %q (provider/model) cannot send thinking level %q on its protocol; omit reasoning_effort to run at the model's default thinking level.", display, effort)
	}
	if raw := usableModelRawLevels(usable); len(raw) > 0 {
		levels := sendableEffortLevels(entry.Provider, entry.Model, entry.APIPath, raw)
		if !slices.Contains(levels, effort) {
			return fmt.Errorf("model %q (provider/model) does not offer thinking level %q; it runs at: %s. Omit reasoning_effort to run at the model's default thinking level.", display, effort, strings.Join(levels, ", "))
		}
	}
	return nil
}
```

设计决定（已定，不要改）：
- **effort 有值而 model 为空 → 拒绝**。零 override 走常规模型链，这条缝上
  应用不了档位；静默忽略会骗调用方。
- 校验放在 `usableModelByID`（授权）之后——授权拒绝文案优先，与现状一致。
- 显式空档位列表（`CapEfforts` 非 nil 空）→ `len(raw)==0` → 跳过成员检查，
  与 gateway 的 `known && len(levels)>0` 完全同语义。
- 未知档位字符串（如 `"bogus"`）：协议检查对未知词是透传（openai
  `/chat/completions`），成员检查在列表已知时拒——与 gateway 一致。

d. 三个生产调用点补第 4 参：
- `:417` → `subagentModelOverrideForRef(ctx, fac, in.Model, in.ReasoningEffort)`
- `:3535` → `subagentModelOverrideForRef(ctx, fac, in.Tasks[i].Model, in.Tasks[i].ReasoningEffort)`
- `:3728` → `subagentModelOverrideForRef(ctx, fac, in.Model, in.ReasoningEffort)`

e. 测试调用点 `subagent_test.go:9072` 补 `""`。`:9358/:9368/:9386` 属于
Step 3 重写的测试，那里一并处理。

验证（此时旧 effort 测试会编译失败——它引用已删除的 `handOffPlanEffort`；
先做 Step 3 再跑包测试。本步只确认编译外的引用清干净）：

```bash
grep -rn "handOffPlanEffort" pkg/
```

预期：无输出。

### Step 3 — 重写 TestHandOffPinnedEffortReachesTheOverride

`pkg/run/subagent_test.go:9321-9393`：注释块＋整个测试替换为（fixture 两行
原样保留；新名字钉新机制）：

```go
// The level the user picked on the approval card travels through the prompt
// and comes back as the dispatch's reasoning_effort argument — never read
// back from session state (owner 2026-11-20). A hand-off record's Effort
// feeds the contract text; the dispatch itself runs at the row's own default
// unless the argument names a level.
func TestHandOffEffortIsAnArgumentNotStateReadBack(t *testing.T) {
	cfg := func() *appcfg.Root {
		on := true
		return &appcfg.Root{Agents: appcfg.AgentsSection{
			Defaults: appcfg.AgentDefaults{EnableSubagent: &on},
			Definitions: map[string]appcfg.AgentDefinition{
				"main": {},
			},
		}}
	}
	fix := newModelArgumentFixture(t, cfg())
	handedOff := fix.insertModelRow(t, state.LLMModel{
		Provider: "openai", Model: "gpt-effort-hand", BaseURL: "https://hand.test/v1",
		APIPath: "/chat/completions", APIKey: "key-hand", SubagentAllowed: true,
		ParamsJSON: `{"reasoning":{"effort":"low"}}`,
	})
	other := fix.insertModelRow(t, state.LLMModel{
		Provider: "openai", Model: "gpt-effort-other", BaseURL: "https://other.test/v1",
		APIPath: "/chat/completions", APIKey: "key-other", SubagentAllowed: true,
		ParamsJSON: `{"reasoning":{"effort":"medium"}}`,
	})
	ctx := dispatchTestCtx()
	if _, err := state.HandOffPlan(fix.fac.Owner.StateRoot(), "session-1", state.ModeAgent, state.PlanHandOff{
		ModelID: handedOff.ID, Provider: handedOff.Provider, Model: handedOff.Model, Effort: "xhigh",
	}); err != nil {
		t.Fatalf("hand off: %v", err)
	}

	// The record names xhigh, but the dispatch passed no argument: the row's
	// own default decides. The state is the prompt's source, not the pin's.
	pin, err := subagentModelOverrideForRef(ctx, fix.fac, handedOff.ID, "")
	if err != nil {
		t.Fatalf("subagentModelOverrideForRef(hand-off row): %v", err)
	}
	if pin.Effort != "low" {
		t.Fatalf("pinned effort = %q, want the row's own level — state must not be read back", pin.Effort)
	}

	// The argument is the channel: the level the prompt told the model to
	// pass arrives as the pin's effort.
	explicit, err := subagentModelOverrideForRef(ctx, fix.fac, handedOff.ID, "xhigh")
	if err != nil {
		t.Fatalf("subagentModelOverrideForRef(hand-off row, xhigh): %v", err)
	}
	if explicit.Effort != "xhigh" {
		t.Fatalf("pinned effort = %q, want the argument's level", explicit.Effort)
	}

	// A row that was not handed off keeps its own configuration too.
	otherPin, err := subagentModelOverrideForRef(ctx, fix.fac, other.ID, "")
	if err != nil {
		t.Fatalf("subagentModelOverrideForRef(other row): %v", err)
	}
	if otherPin.Effort != "medium" {
		t.Fatalf("pinned effort = %q, want that row's own level", otherPin.Effort)
	}
}
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestHandOffEffortIsAnArgumentNotStateReadBack|TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation' -count=1
```

预期：ok。

### Step 4 — 校验矩阵测试＋三个工具的参数管线测试

`pkg/run/subagent_test.go` 追加两个测试。矩阵测试直接打
`subagentModelOverrideForRef`（fixture 同上，`subagentsEnabledConfig()` 在
`:349-354`）：

```go
// reasoning_effort is judged with the gateway's saved-config semantics: the
// protocol check always, the membership check only when the row's levels are
// known and non-empty, unknown models unjudged, empty always the row default.
func TestSubagentReasoningEffortValidation(t *testing.T) {
	fix := newModelArgumentFixture(t, subagentsEnabledConfig())
	known := fix.insertModelRow(t, state.LLMModel{
		Provider: "openai", Model: "gpt-effort-known", BaseURL: "https://known.test/v1",
		APIPath: "/chat/completions", APIKey: "key-known", SubagentAllowed: true,
		ParamsJSON: `{"reasoning":{"effort":"low"}}`,
		CapEfforts: []string{"low", "medium", "high"},
	})
	claude := fix.insertModelRow(t, state.LLMModel{
		Provider: "anthropic", Model: "claude-effort", BaseURL: "https://claude.test",
		APIPath: "/v1/messages", APIKey: "key-claude", SubagentAllowed: true,
		CapEfforts: []string{"low", "medium", "high"},
	})
	unknown := fix.insertModelRow(t, state.LLMModel{
		Provider: "openai", Model: "gpt-effort-unknown", BaseURL: "https://unknown.test/v1",
		APIPath: "/chat/completions", APIKey: "key-unknown", SubagentAllowed: true,
	})
	ctx := dispatchTestCtx()

	if pin, err := subagentModelOverrideForRef(ctx, fix.fac, known.ID, ""); err != nil || pin.Effort != "low" {
		t.Fatalf("empty effort = (%q, %v), want the row default low", pin.Effort, err)
	}
	if pin, err := subagentModelOverrideForRef(ctx, fix.fac, known.ID, " high "); err != nil || pin.Effort != "high" {
		t.Fatalf("trimmed member level = (%q, %v), want high", pin.Effort, err)
	}
	if _, err := subagentModelOverrideForRef(ctx, fix.fac, known.ID, "xhigh"); err == nil ||
		!strings.Contains(err.Error(), "does not offer") || !strings.Contains(err.Error(), "low, medium, high") {
		t.Fatalf("non-member level must be refused with the offered list: %v", err)
	}
	if _, err := subagentModelOverrideForRef(ctx, fix.fac, claude.ID, "none"); err == nil ||
		!strings.Contains(err.Error(), "cannot send thinking level") {
		t.Fatalf("anthropic protocol must refuse none: %v", err)
	}
	if pin, err := subagentModelOverrideForRef(ctx, fix.fac, unknown.ID, "xhigh"); err != nil || pin.Effort != "xhigh" {
		t.Fatalf("a model with no known levels is not judged: (%q, %v)", pin.Effort, err)
	}
	if _, err := subagentModelOverrideForRef(ctx, fix.fac, "", "high"); err == nil ||
		!strings.Contains(err.Error(), "reasoning_effort needs a model argument") {
		t.Fatalf("effort without model must be refused, not silently dropped: %v", err)
	}
}
```

注意：`unknown` 行不写 `CapEfforts`（nil = 查目录），model 名
`gpt-effort-unknown` 不在内置目录 → 列表未知 → 放行。若运行环境装了覆盖
注册表导致它「已知」，测试会失败——那是 STOP 条件，不要改断言迁就。

管线测试证明三个工具都把参数递到了解析缝（用**必拒**档位走错误路径，
不需要真跑子代理；注册模式照抄
`TestListUsableModelsToolReadsTheTableLive`，`subagent_test.go:8996-9008`）：

```go
// All three dispatch tools plumb reasoning_effort into the pin resolver: a
// level the row cannot run fails the call before anything executes.
func TestSubagentDispatchToolsCarryReasoningEffort(t *testing.T) {
	fix := newModelArgumentFixture(t, subagentsEnabledConfig())
	row := fix.insertModelRow(t, state.LLMModel{
		Provider: "openai", Model: "gpt-effort-plumb", BaseURL: "https://plumb.test/v1",
		APIPath: "/chat/completions", APIKey: "key-plumb", SubagentAllowed: true,
		CapEfforts: []string{"low", "medium", "high"},
	})
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, fix.fac); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	ctx := dispatchTestCtx()
	inputs := map[string]string{
		"subagent_run":    `{"title":"t","task":"do it","model":"` + row.ID + `","reasoning_effort":"xhigh"}`,
		"subagent_send":   `{"title":"t","task":"do it","model":"` + row.ID + `","reasoning_effort":"xhigh"}`,
		"subagent_fanout": `{"max_parallel":1,"tasks":[{"title":"t","prompt":"do it","model":"` + row.ID + `","reasoning_effort":"xhigh"}]}`,
	}
	for name, input := range inputs {
		tl, ok := aToolByName(a, name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if _, err := tl.Handle(ctx, input); err == nil || !strings.Contains(err.Error(), "does not offer thinking level") {
			t.Fatalf("%s must refuse the non-member level before dispatching: %v", name, err)
		}
	}
}
```

（若 `subagent_send`/`subagent_fanout` 的注册需要额外 fixture 条件导致
`aToolByName` 找不到工具，参考同文件里既有的 send/fanout 测试补齐 fixture；
找不到就 STOP 报告，不要跳过某个工具。）

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentReasoningEffortValidation|TestSubagentDispatchToolsCarryReasoningEffort' -count=1
```

预期：ok。

### Step 5 — 续跑保留派发档位（subagentRecordPin）

`pkg/run/subagent.go:2494-2503`，live 重读分支改为：

```go
	entry, caps := reread.Entry, reread.Caps
	// The dispatched level is part of the model this run was pinned to: a
	// continuation keeps it, exactly as the configured-type path freezes it.
	// Only a record that never named one falls back to the row's default.
	effort := strings.TrimSpace(record.ReasoningEffort)
	if effort == "" {
		effort = appcfg.ReasoningEffort(entry.Params)
	}
	return SubagentModelOverride{
		ModelID:  strings.TrimSpace(entry.ID),
		Provider: strings.TrimSpace(entry.Provider),
		Model:    strings.TrimSpace(entry.Model),
		Effort:   effort,
		Entry:    &entry,
		Version:  reread.Row.UpdatedAt,
		Caps:     caps,
	}, nil
```

新测试（放在
`TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation` 之后）：

```go
// A continuation keeps the level the dispatch ran at; only a record without
// one falls back to the row's current default.
func TestSubagentRecordPinKeepsTheDispatchedEffort(t *testing.T) {
	fix := newModelArgumentFixture(t, subagentsEnabledConfig())
	row := fix.insertModelRow(t, state.LLMModel{
		Provider: "openai", Model: "gpt-effort-cont", BaseURL: "https://cont.test/v1",
		APIPath: "/chat/completions", APIKey: "key-cont", SubagentAllowed: true,
		ParamsJSON: `{"reasoning":{"effort":"low"}}`,
	})
	ctx := dispatchTestCtx()
	record := agent.HistoryEntry{
		ModelProvider: "openai", Model: "gpt-effort-cont",
		ModelID: row.ID, ModelLive: true, ReasoningEffort: "xhigh",
	}
	pin, err := subagentRecordPin(ctx, fix.fac, record)
	if err != nil {
		t.Fatalf("subagentRecordPin: %v", err)
	}
	if pin.Effort != "xhigh" {
		t.Fatalf("continuation effort = %q, want the dispatched level", pin.Effort)
	}
	record.ReasoningEffort = ""
	pin, err = subagentRecordPin(ctx, fix.fac, record)
	if err != nil {
		t.Fatalf("subagentRecordPin without a level: %v", err)
	}
	if pin.Effort != "low" {
		t.Fatalf("continuation effort = %q, want the row's default", pin.Effort)
	}
}
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentRecordPinKeepsTheDispatchedEffort|TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation' -count=1
```

预期：ok。

### Step 6 — hand-off 契约：档位写进提示词

`pkg/tool/session_tools.go`：

a. `planHandOffContractFormat` 派发行（`:643`）：`model "%[3]s"` 改为
`%[3]s`（占位符现在装整个参数子句），并在该行之后、`1. Where to work.`
之前插入一段（前后各空一行）：

```
The model and thinking level in that call are the user's own approval choices: pass them exactly as written. Never substitute another model or thinking level, and never add a thinking level the call does not name.
```

改后的派发行逐字为：

```
Read the whole plan file now. Call subagent_run with subagent_type "general-purpose", %[3]s, a short title, and a task made of these four parts, in order:
```

b. 占位符注释（`:623-626`）里 `%[3]s` 的说明改为「the model-and-level
argument clause passed to subagent_run (always the configured row id, plus
reasoning_effort when the approval named one)」。

c. 构造函数（`:687-692`）替换为：

```go
// HandOffPlanExecutionPrompt is the contract the tech lead executes a handed-
// off plan under. modelRef is the configured row id, never a provider/model
// pair; effort is the level the user picked on the approval card ("" when
// they picked none). Both travel to the dispatch through this prompt: the
// model passes them as subagent_run's model and reasoning_effort arguments,
// and the dispatch never reads them back from session state.
func HandOffPlanExecutionPrompt(planFile, modelDisplay, modelRef, effort string) string {
	args := fmt.Sprintf("model %q", modelRef)
	if effort = strings.TrimSpace(effort); effort != "" {
		args += fmt.Sprintf(", reasoning_effort %q", effort)
	}
	return fmt.Sprintf(planHandOffContractFormat, planFile, modelDisplay, args, state.ImplementationHeading)
}
```

d. exit_plan_mode 结果（`:789-797`）：`hand_off_model` map 加条件 effort
键，`on_completion` 传第 4 参：

```go
					display := llm.FormatProviderModel(handOff.Provider, handOff.Model)
					ref := strings.TrimSpace(handOff.ModelID) // B1 guarantees a row id
					handOffModel := map[string]any{"id": handOff.ModelID, "provider": handOff.Provider, "model": handOff.Model}
					if eff := strings.TrimSpace(handOff.Effort); eff != "" {
						handOffModel["effort"] = eff
					}
					b, _ := json.Marshal(map[string]any{
						"mode":           string(restored),
						"plan_file":      planFile,
						"hand_off_model": handOffModel,
						"message":        "Exited plan mode. The user handed this plan to " + display + ": dispatch one executor subagent on that model to carry it out in its own git worktree, then review its work. Do not implement the plan yourself.",
						"on_completion":  HandOffPlanExecutionPrompt(planFile, display, ref, handOff.Effort),
					})
```

e. `pkg/run/config.go:1008`：

```go
	return planImplementationReminderMarker + " - read carefully.\n\n" + tool.HandOffPlanExecutionPrompt(planFile, display, ref, h.Effort)
```

f. golden 测试 `pkg/tool/session_tools_test.go`
`TestHandOffContractRendersVerbatim`（`:987-1059`）：
- `:988` 调用改
  `HandOffPlanExecutionPrompt("/plans/283/plan.md", "zhipuai/glm-5.3", "llm_row2", "xhigh")`。
- golden 常量里派发行（`:1008`）改为：
  ```
  Read the whole plan file now. Call subagent_run with subagent_type "general-purpose", model "llm_row2", reasoning_effort "xhigh", a short title, and a task made of these four parts, in order:
  ```
  并在该行后插入与模板逐字相同的新段落（上面 a 的那句，前后空行）。
  **golden 其余部分一字不动。**
- marker 循环（`:1054`）加 `"reasoning_effort"`。
- 等值断言之后追加空档位腿：

```go
	// An approval that named no level names none in the contract either.
	plain := HandOffPlanExecutionPrompt("/plans/283/plan.md", "zhipuai/glm-5.3", "llm_row2", "")
	if wantPlain := strings.ReplaceAll(want, `model "llm_row2", reasoning_effort "xhigh"`, `model "llm_row2"`); plain != wantPlain {
		t.Fatalf("no-level contract drifted:\n got: %q\nwant: %q", plain, wantPlain)
	}
```

g. `TestExitPlanModeHandOffRecordsTheRowAndReturnsTheContract`
（`:1065-1107`）：现有无 effort 场景追加两条断言（在 `hand_off_model` 断言
之后）：

```go
	if _, hasEffort := handOff["effort"]; hasEffort {
		t.Fatalf("an approval that named no level must carry no effort key: %s", raw)
	}
```

以及 `on_completion` marker 检查之后：

```go
	if strings.Contains(on, "reasoning_effort") {
		t.Fatal("an approval that named no level must not name one in the contract")
	}
```

再加带 effort 的孪生测试（setup 助手 `officePlanOnlySetup` 同款，审批 JSON
多一个 `"effort":"medium"`）：

```go
func TestExitPlanModeHandOffCarriesThePickedLevelIntoTheContract(t *testing.T) {
	st, rt, home, sid := officePlanOnlySetup(t, `{"decision":"accept","hand_off_model":{"id":"llm_row2","provider":"zhipuai","model":"glm-5.3","effort":"medium"}}`)
	tool, err := newExitPlanModeToolFor(st, home, "", rt)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	tctx := withSession(context.Background(), sid)
	tctx = WithConversationSessionID(tctx, "conv-office-exit-effort")
	tctx = WithRunID(tctx, "run-office-exit-effort")
	raw, err := tool.Handle(tctx, `{}`)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw.(string)), &obj); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	handOff, _ := obj["hand_off_model"].(map[string]any)
	if handOff["effort"] != "medium" {
		t.Fatalf("result must carry the picked level: %s", raw)
	}
	on, _ := obj["on_completion"].(string)
	if !strings.Contains(on, `model "llm_row2", reasoning_effort "medium"`) {
		t.Fatalf("the contract must name both approval choices as arguments: %s", on)
	}
	final, _ := state.Get(home, sid)
	if final.PlanHandOff == nil || final.PlanHandOff.Effort != "medium" {
		t.Fatalf("mode state must still record the level for the reminder: %+v", final.PlanHandOff)
	}
}
```

h. `session_tools_test.go:1109-1112` 的过时注释改为（测试本身不动）：

```go
// The tool package reads the hand-off answer with its own decoder (it cannot
// import pkg/turn), so the level the user picked has to be read here too: it
// feeds the contract prompt and the recurring reminder, and a level dropped
// on this side would silently run at the row's own configuration.
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestHandOffContractRendersVerbatim|TestExitPlanModeHandOff|TestActionHandOffModelCarriesEffort' -count=1
```

预期：ok。

### Step 7 — reminder 测试加 effort 腿

`pkg/run/config_test.go` `TestPlanHandOffReminderIsTheTechLeadContract`：
现有断言全部保持（`model "llm_row2"` 子串在新子句下依然成立）。在 sparse
形态检查（`:2737-2742`）之前插入：

```go
	// The level the approval named rides the same contract as an argument.
	withLevel := buildPlanHandOffReminder("/plans/p/plan.md", state.PlanHandOff{
		ModelID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3", Effort: "xhigh",
	}, "full")
	if !strings.Contains(withLevel, `model "llm_row2", reasoning_effort "xhigh"`) {
		t.Fatalf("the reminder must name both approval choices as arguments: %q", withLevel)
	}
```

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestPlanHandOffReminderIsTheTechLeadContract -count=1
```

预期：ok。

### Step 8 — list_usable_models 描述追加 reasoning_effort 句

`pkg/run/subagent.go` 的 `listUsableModelsDescription`（331 版，见第 9 节）
句尾追加（仍是固定文本：不点名模型、不枚举档位；`list_usable_models` 自身
的名字本来就不在描述里，保持）：

```go
const listUsableModelsDescription = "List the configured models you may run a subagent on, read live from the model table. Pass a model's id as the model argument of subagent_run, subagent_send or subagent_fanout. Pick a model by its id; the model field is the name a person reads. Each row lists the thinking levels it can run at (efforts) and the level it runs at by default (default_effort). Pass reasoning_effort on the dispatch to run the subagent at one of the chosen model's thinking levels; omit it to run at that model's default."
```

若 331 的测试钉了旧描述全文（grep
`listUsableModelsDescription\|Each row lists the thinking levels` 于
`pkg/run/*_test.go`），同步更新该断言为新全文。

验证：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestListUsableModels|TestSubagentModelArgumentDescription' -count=1
```

预期：ok。

### Step 9 — 全量验证门

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/tui -count=1
gofmt -l cmd pkg third_party plugins
CGO_ENABLED=1 go vet -tags fts5 ./...
```

预期：三包 ok；gofmt 无输出；vet 无输出。`pkg/tui` 必须跑——
`pkg/tui` 有 hand-off 相关测试（如 `handOffModelOf` 一族），签名变更可能
波及；若 tui 测试因 `HandOffPlanExecutionPrompt` 签名失败，按同样模式补
`""` 或对应 effort 参数（这属于本计划的连带修复，允许）。

## Scope

**In scope**（只许改这些）：
- `pkg/run/subagent.go`
- `pkg/run/subagent_test.go`
- `pkg/run/config.go`（仅 `:1008` 一行）
- `pkg/run/config_test.go`（仅 reminder 测试加腿）
- `pkg/tool/session_tools.go`
- `pkg/tool/session_tools_test.go`
- `pkg/tui/*_test.go`（仅当签名变更导致编译失败时的连带修复）
- `plans/README.md`（状态行）

**Out of scope**（看着相关但不许动）：
- `state.PlanHandOff.Effort` 字段、`state.HandOffPlan`、`actionHandOffModel`
  （`session_tools.go:487-513`）——审批记录链保留，它们喂提示词。
- 各端审批写入器：`pkg/turn/approval.go`、`pkg/tui/chat_surface.go`、
  `pkg/gateway/api_extra.go`——第二屏选档位的 UI/写入不变。
- `handOffModelID`（`subagent.go:4561-4577`）与 `usableModelByID` /
  `usableModels` 的 hand-off 授权例外——模型行的授权机制不变，本计划只改
  档位。
- `pkg/gateway/subagents_api.go` 的 `subagentLevelRefused`、
  `pkg/turn/models.go` 的档位逻辑、331 的镜像 helper 本体——只复用。
- `pkg/tool/format.go`、`pkg/tui/render.go`、`pkg/tui/reducer.go`——
  工具卡渲染是 plan 332。
- `prepareSubagent` 的三级 pin 链（`:1987-2041`）——不动。

## Done criteria（机器可查）

1. `grep -rn "handOffPlanEffort" pkg/` 无输出。
2. `grep -n "reasoning_effort" pkg/run/subagent.go` 至少命中：三个结构 tag、
   两处拒绝文案、描述常量。
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/tui -count=1`
   全部 ok，且包含新测试：
   `TestSubagentReasoningEffortArgumentDescriptionIsFixedText`、
   `TestHandOffEffortIsAnArgumentNotStateReadBack`、
   `TestSubagentReasoningEffortValidation`、
   `TestSubagentDispatchToolsCarryReasoningEffort`、
   `TestSubagentRecordPinKeepsTheDispatchedEffort`、
   `TestExitPlanModeHandOffCarriesThePickedLevelIntoTheContract`
   （用 `-run` 单独点名各跑一遍确认存在且过）。
4. `gofmt -l cmd pkg third_party plugins` 无输出。
5. `CGO_ENABLED=1 go vet -tags fts5 ./...` 无输出。
6. `git diff --stat` 只含 In scope 文件。

## Maintenance notes

- **提示词是档位唯一通道**：以后改 hand-off 审批（加字段、改第二屏），要
  同步改 `HandOffPlanExecutionPrompt` 的子句构造与 golden；改
  `planHandOffContractFormat` 必须同步 `TestHandOffContractRendersVerbatim`
  的逐字 golden——这是刻意的行为钉，不是措辞测试。
- `dispatchEffortRefused` 与 gateway `subagentLevelRefused` 是同一语义的
  两份实现（gateway 用 turn，run 用 331 镜像）。改任何一侧的校验语义要两
  侧同步；331 的平价测试
  `TestSendableEffortLevelsMatchThePickerSetup` 钉住镜像与 turn 的一致。
- 描述常量是固定文本（缓存前缀不变量）：往
  `listUsableModelsDescription` 或三个 `ReasoningEffort` tag 里加任何随表
  状态变化的内容都是回归。
- `subagentRecordPin` 现在保留记录档位：若未来要支持「续跑时改档位」，
  入口应是新的显式参数，而不是恢复无条件重置。

## STOP conditions

- 331 的 helper（`sendableEffortLevels`/`usableModelRawLevels`）不存在或
  签名不符 → STOP，先落地 331。
- `TestSubagentReasoningEffortValidation` 的 unknown 行被判「已知」→ 环境
  装了覆盖注册表或目录数据变了，STOP 报告，不要改断言。
- `officePlanOnlySetup` / `aToolByName` / `noopLLM` 等 fixture 助手签名与
  本计划摘录不符 → 以现有测试的实际用法为准做最小适配；若语义对不上 →
  STOP。
- 发现 `HandOffPlanExecutionPrompt` 或 `subagentModelOverrideForRef` 有本
  计划未列出的调用点 → STOP 报告（grep 已核实全集，多出来的说明代码漂移）。
- 任何需要改动 Out of scope 文件才能让测试通过的「发现」→ STOP 报告。