# Plan 169：`/model` 的「当前」标记改成和 effort 选择器同一条规则

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func defaultModelIndex" pkg/turn/models.go` 为 `1`；
> `grep -c "defaultAt := defaultModelIndex" pkg/turn/executor.go` 为 `1`；
> `grep -c "changed = at != defaultModelIndex" pkg/turn/executor.go` 为 `1`；
> `grep -c "func newModelSelectionFixture" pkg/turn/model_test.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/turn/executor.go pkg/turn/models.go pkg/turn/model_test.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1`（超时给足 600 s）。

## Status

- **Priority**: P3（窄：只在"会话没有选择行、而运行时的模型不是配置默认项"时标错一行；不是主路径）
- **Effort**: S（一处规则 + 一处 `changed` 判定 + fixture 一个可选字段 + 2 个测试）
- **Risk**: LOW（只读路径；不动应用/持久化模型选择的代码）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（两处用两种规则回答同一个问题）

## Why this matters

`/model` 的 picker 要回答一个问题：**这个会话现在跑在哪个模型上**。今天同一个文件里有两套答案：

| 选择器 | 会话有选择行 | 会话**没有**选择行 |
|---|---|---|
| 模型（`execModel`，`:809-818`） | 按行标 | 标**配置顺序里的默认项**（`defaultModelIndex`） |
| effort（`chooseModel` 之后，`:893-897`） | 按行标 | 标 **`LiveSelection`**——运行时真正在跑的那个 |

两套答案的差别不是理论问题：`defaultModelIndex` 读的是**配置文件**，`LiveSelection` 读的是**运行时**。
只要这两者不一致（例如 `default_model` 被热改成另一个**存在的**条目、而池里的 runner 还按它自己发布的模型在跑；
或同一个会话在一台按 agent 切换过模型的 runner 上重新打开），模型选择器就会标出**不是正在跑的那个**，
而且 `changed`（`:866-872`）会拿配置默认项当"当前值"，于是用户选回**正在用的模型**时得到 "Switched to …"，
并真的走一次应用 + 落库（写出一条多余的选择行）。effort 选择器不会犯这个错，因为它问的是运行时。

还有一处**不是**缺陷、但值得写进注释的细节：`defaultModelIndex` 在"配置默认项解析不出来"时返回 `-1`。
这次核实过它**不可达**（见下），所以本计划**不**把它当 bug 修，只把两套规则统一掉、并用测试钉住
"配置能跑 ⇒ 配置默认项一定能解析"这条不变式。

### 可达性核实（写在这里，避免下轮重审）

`defaultAt == -1` 需要 `EffectiveDefaultLLM() == nil` 而 `ModelChoices` 非空，只有两条路：

1. **`default_model` 非空且匹配不到任何条目**：被 `pkg/config/load.go:860-870` 的 `validatePrimaryAgents`
   在**加载、热重载、结构化写入**三条路上统一拒绝（注释明写"must never reach disk"）。**不可达**。
2. **`default_model` 为空、而 `llm_providers` 首项是空占位 `- {}`**：`EffectiveDefaultLLM` 返回
   `&expanded[0]`（`pkg/config/agent_llm.go:78-81`），`ModelChoices` 跳过它（`pkg/turn/models.go:683-686`）
   ⇒ `-1`。空占位确实是 `normalize` 自己造的（`pkg/config/load.go:620-628`），也**不会**被
   `expandLLMProviderConfigs` 丢掉（`agent_llm.go:94-99`）。但这样一个 config 的"有效默认"是**不完整**的：
   `ValidateAgentLLMConfigured(...).Complete()` 要求 provider+model+base_url 都存在
   （`pkg/config/agent_llm.go:442-476`），而它在三处被强制——setup（`pkg/process/setup.go:80`）、
   网页的 providers 写入（`pkg/gateway/api_extra.go:6046`）、终端的 setup 闸门（`pkg/tui/setup.go:171-179`）。
   一个**跑起来过**的安装因此必然有可解析的默认项。**实质不可达**（只有手工改坏、且应用起不来的配置才会命中）。

结论：`-1` 是"给不可运行配置的防御代码"，按本仓规则不追着改；真正要修的是**同一问题两套规则**。

## Current state

`pkg/turn/executor.go:809-818`（标记）：

```go
	// The effective default (default_model, else the first) is what an empty
	// selection resolves to; index zero only matches it by coincidence.
	defaultAt := defaultModelIndex(settings.Config, settings.AgentName, choices)
	items := make([]PickerItem, len(choices))
	for i, choice := range choices {
		// The live selection marks the current model; the effective default
		// is only the fallback for a runtime that has never published one.
		items[i] = PickerItem{Value: choice.Label, Label: choice.Label, Current: !settings.Selection.Set && i == defaultAt || selectionMatches(settings.Selection, choice)}
	}
```

注意这段注释**自己就写着**"the effective default is only the fallback for a runtime that has never published one"——
但代码从没问过运行时发布的是什么，它只比了配置顺序。

`pkg/turn/executor.go:866-872`（`changed`）：

```go
	if settings.Selection.Set {
		changed = !selectionMatches(settings.Selection, chosen)
	} else {
		changed = at != defaultModelIndex(settings.Config, settings.AgentName, choices)
	}
```

`pkg/turn/executor.go:893-897`（同一个文件里 effort 的规则，**照它写**）：

```go
	// … A session without a row shows the effort its runner actually carries
	// (the resolved view), while a session with one shows its own pinned effort.
	current := settings.Selection.Effort
	if !settings.Selection.Set {
		current = settings.LiveSelection.Effort
	}
```

`ModelSettings`（`pkg/turn/model.go:278-283`）的两个字段就绪：`Selection`（会话自己的行，raw）与
`LiveSelection`（按 runner 解析后的视图）。两个 surface 都会填 `LiveSelection`
（`pkg/gateway/slash_handlers.go:239-249`、`pkg/tui/chat_slash.go:655-660`）；`Selection.Set == false`
表示"这个会话没有自己的行"（不是"没有模型"）。

## Scope

**In scope（只改这些）**：
- `pkg/turn/executor.go`（新增 `currentSelection`；标记与 `changed` 用它）
- `pkg/turn/model_test.go`（fixture 加一个可选 `live` 字段 + 2 个测试）

**Out of scope（看着相关也不要动）**：
- `defaultModelIndex` 的返回值（**不要**改成"返回 0"，那既不解决"标错行"，也掩盖了配置默认项的真实定义）
- `SelectModel` / 持久化 / `SelectionApplyOutcome`
- effort 选择器（它是对的）
- 网页或终端的渲染（都读 `PickerItem.Current`）
- 陈旧选择行的"不标当前"语义（**刻意**：`pkg/tui/chat_slash.go:644-651` 的注释写明
  "the picker draws no current marker for such a row, which is the truth"）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run '^(TestExecModelMarksWhatTheRuntimeCarries\|TestChooseModelInForceModelIsNotASwitch)$' -count=1 -v` | 两个 PASS |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` | 与基线相比无新增 FAIL |
| 两个 surface | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/turn` | exit 0 |
| gofmt | `gofmt -l pkg/turn` | 无输出 |

## Steps

### Step 1：fixture 能表达"没有会话行、但运行时模型不同"

`pkg/turn/model_test.go` 的 `modelSelectionFixture`（`:272` 起）与 `modelSettings`（`:300-306`）今天把
`handler.selection` 同时当 `Selection` 与 `LiveSelection`。加一个可选字段（既有 20 多处调用不受影响）：

```go
// live is what the runtime publishes when the session has no row of its own;
// nil means "the same as selection", which is what every existing fixture
// assumes.
live *ModelSelectionState
```

`modelSettings` 里：

```go
	live := f.handler.selection
	if f.live != nil {
		live = *f.live
	}
	return ModelSettings{Config: &cfg, AgentName: "main", Selection: f.handler.selection, LiveSelection: live}, nil
```

### Step 2：先写两个会红的测试

照抄同文件两个既有用例的骨架（`TestExecModelMarksCurrentByLiveSelection`（`:337`）与
`TestChooseModelSameSelectionIsNoOp`（`:368`）：`Choose(f.ctx, SlashChoice{Command: "model", Value: label})`、
`f.choiceLabel(t, provider, model)`、`slices.Index(f.calls(), "select(")`）。

fixture 造好的配置（`:276`）第一项是 `openai/gpt-5`、第二项是 `deepseek/deepseek-chat`；
两个测试都用**没有会话行**、而运行时跑在 `deepseek/deepseek-chat` 的会话：

```go
func newNoRowDeepseekFixture(t *testing.T) *modelSelectionFixture {
	t.Helper()
	f := newModelSelectionFixture(t, ModelSelectionState{})          // 会话没有自己的行
	live := ModelSelectionState{Provider: "deepseek", Model: "deepseek-chat", Set: true}
	f.live = &live
	return f
}
```

**测试 1（标记）**：

```go
// The picker marks what the conversation actually runs on — the effort picker's
// rule — not the config's order-based default.
func TestExecModelMarksWhatTheRuntimeCarries(t *testing.T) {
	f := newNoRowDeepseekFixture(t)
	res := execModel(f.ctx)
	require.NotNil(t, res.Picker)
	current := -1
	for i, item := range res.Picker.Items {
		if item.Current {
			require.Equal(t, -1, current, "more than one current item")
			current = i
		}
	}
	require.NotEqual(t, -1, current, "no current item: %v", res.Picker.Items)
	require.Contains(t, res.Picker.Items[current].Value, "deepseek-chat", "current = %s", res.Picker.Items[current].Value)
}
```

**测试 2（选回在跑的模型不算切换）**：

```go
func TestChooseModelInForceModelIsNotASwitch(t *testing.T) {
	f := newNoRowDeepseekFixture(t)
	label := f.choiceLabel(t, "deepseek", "deepseek-chat")
	res := Choose(f.ctx, SlashChoice{Command: "model", Value: label})
	require.True(t, res.Handled)
	// calls() 的记录形状是 "select(<sessionID>,<provider/model>)"（recordingSlashHandlers.record），
	// 所以这里必须匹配完整串——`slices.Index(f.calls(), "select(")` 是精确等值匹配，恒为 -1，
	// 那是空断言（既有 :373/:665 就带着这个弱点，不要照抄；:536 的完整串才是有效写法）。
	require.Equal(t, -1, slices.Index(f.calls(), "select(s1,deepseek/deepseek-chat)"), "a repeat selection applied a live switch: %v", f.calls())
	require.Equal(t, "Still using deepseek/deepseek-chat.", res.Reply)
}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run '^(TestExecModelMarksWhatTheRuntimeCarries|TestChooseModelInForceModelIsNotASwitch)$' -count=1 -v`
→ 两个都 FAIL（第一个标的是 `openai/gpt-5`，第二个走了 `select(`）。若有一个直接 PASS，STOP 3。

### Step 3：一条规则，两处使用

在 `pkg/turn/executor.go` 的 `selectionMatches`（`:823`）附近新增：

```go
// currentSelection is the selection a picker marks as current: the session's own
// row when it has one, otherwise what the runtime actually carries. The config's
// order-based default is only the last resort, for a runtime that has published
// nothing — asking the config instead is what let the model picker mark a model
// the conversation was not running on.
func currentSelection(settings ModelSettings) ModelSelectionState {
	if settings.Selection.Set {
		return settings.Selection
	}
	if settings.LiveSelection.Set {
		return settings.LiveSelection
	}
	return ModelSelectionState{}
}
```

标记处：

```go
	defaultAt := defaultModelIndex(settings.Config, settings.AgentName, choices)
	currentSel := currentSelection(settings)
	items := make([]PickerItem, len(choices))
	for i, choice := range choices {
		// The session's own row marks the current model; a session without one
		// marks what its runtime carries (the effort picker's rule); the config
		// default is only the fallback for a runtime that published nothing.
		items[i] = PickerItem{Value: choice.Label, Label: choice.Label,
			Current: selectionMatches(currentSel, choice) || (!currentSel.Set && i == defaultAt)}
	}
```

`changed` 处：

```go
	if settings.Selection.Set {
		changed = !selectionMatches(settings.Selection, chosen)
	} else if live := currentSelection(settings); live.Set {
		changed = !selectionMatches(live, chosen)
	} else {
		changed = at != defaultModelIndex(settings.Config, settings.AgentName, choices)
	}
```

**Verify**：同 Step 2 的定向命令 → 两个 PASS；`gofmt -l pkg/turn` 无输出。

### Step 4：回归（含两个 surface）

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tui ./pkg/gateway -count=1`
→ 与基线相比无新增 FAIL。特别确认这四条仍 PASS（它们守着"陈旧行不标当前"与"真切换要生效"）：

- `TestExecModelMarksCurrentByLiveSelection`（`:337`）
- `TestChooseModelSameSelectionIsNoOp`（`:368`）
- `TestChooseModelAppliesLiveSelectionWithoutWritingTheConfigFile`（`:353`；它的 fixture `live` 为 nil ⇒ `LiveSelection`
  与 `Selection` 同为未设 ⇒ 走配置默认分支，仍须报 `Switched to …`）
- `TestChooseModelEffortPickerMarksLiveEffort`（`:428`）

## Test plan

- `TestExecModelMarksWhatTheRuntimeCarries`：无会话行 + 运行时 deepseek + 配置首项 openai → 恰好一行 Current，且是 deepseek。
- `TestChooseModelInForceModelIsNotASwitch`：同状态选 deepseek → `Still using …`，`SelectModel` 未被调用。
- fixture 只加一个可选字段；`live` 为 nil 时行为与今天完全一致（Step 4 那四条是这一点的证明）。

## Done criteria

机器可检查，全部满足：

- [ ] 两个新测试 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tui ./pkg/gateway -count=1` 与基线相比无新增 FAIL
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/turn` exit 0
- [ ] `gofmt -l pkg/turn` 无输出
- [ ] `grep -c "func currentSelection" pkg/turn/executor.go` 为 `1`
- [ ] `grep -c "(!currentSel.Set && i == defaultAt)" pkg/turn/executor.go` 为 `1`
- [ ] `grep -c "func defaultModelIndex" pkg/turn/models.go` 仍为 `1`（**没有**改它的返回值）
- [ ] `git status --porcelain` 只显示 Scope 里的两个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. 两个测试里有一个在 Step 2 直接 PASS（说明基线已变）。
4. 你想通过"造一个 `default_model` 悬空或首项空占位的配置"来让测试红：**不要**——那两种配置应用都跑不起来
   （见 Why 里的可达性核实），用它们做测试只会把不可达状态固化成契约。本计划的测试**只用能跑起来的配置**
   （openai + deepseek 两条完整条目）。
5. `currentSelection` 让某条既有用例变红（尤其"陈旧行不标当前"那类）：先读那条用例的意图再决定，读不清就停——
   **绝不要**为了让测试变绿去改 `Selection.Set` 的判定。
6. 你发现 `LiveSelection` 在某条 surface 路径上**从不**被填（例如某个 fake 只填了 `Selection`）：报告该路径，
   不要顺手改 `ModelSettings` 的构造。

## Maintenance notes

- 这条规则现在是三处一致：模型标记、模型 `changed`、effort 标记。将来任何"当前是哪个"的新表面都该用
  `currentSelection`，而不是自己再读一遍 `default_model`。
- `defaultModelIndex` 保留为"配置顺序默认"的唯一权威（它仍被 `changed` 的最后一档用着）。它的 `-1` 是给
  不可运行配置的防御，不要拿它当"运行时当前"。
- 评审重点：本计划不动 `SelectModel` 与持久化；`changed` 的新分支只在"没有会话行且运行时发布了模型"时生效。
