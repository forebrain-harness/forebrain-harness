# Plan 282: `/model` 与计划审批的评审选择器按原型 01 / 18 改列表

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 990ec9dd..HEAD -- pkg/turn/executor.go pkg/turn/model.go pkg/turn/approval.go pkg/tui/run.go pkg/tui/overlays.go pkg/tui/setup.go pkg/tui/setup_test.go pkg/tui/panel_columns.go pkg/gateway/api_extra.go pkg/tui/commands_test.go pkg/tui/chat_session_test.go pkg/turn/model_test.go pkg/turn/slash_test.go pkg/turn/approval_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> content mismatch, treat it as a STOP condition (moved line numbers alone
> are not).

## Status

- **Priority**: P1（owner 点名：原型页 01 与 18 的修改写进计划）
- **Effort**: M
- **Risk**: MED（改审批 overlay 的可见布局 + 删一个用户可见入口）
- **Depends on**: none
- **Category**: direction（TUI 交互与列表布局）
- **Planned at**: commit `990ec9dd`, 2026-11-09
- **Issue**: —

## Why this matters

owner 已确认的终端原型里，两个页面被改过，真机要跟上：

1. **原型 01（`A · /model 选择器`）**：`/model` 只列这个会话已配置的模型，**不再有 `Add a custom model…` 行，也没有进入自定义模型的入口**——新增模型（含自定义端点）统一走 `/connect`。这条目前真机还不成立：TUI 的列表末项仍是那一行，选中就打开 `/connect` 的自定义表单（`pkg/tui/run.go:5296`）。
2. **原型 18（`E · 计划审批 / exit-plan-pick`）**：`exit_plan_mode` 审批面板里「选评审模型」不再是一列 `1. provider/model (current)` 的纯文本，而是**和 `/model` 同一套两列列表**：光标 `❯` + `provider/model`，说明列是该行的**完整请求 URL**（太窄时从 base 中部省略、保住 api path），当前会话模型在名字后面标 `●`（accent 110），**行首没有序号**，按键只用 `↑/↓`、`j/k`、`Enter`、`Esc`。这条现在是另一套渲染（`pkg/tui/overlays.go:697-715` 编号行 + `planReviewModelRow` 追加 `" (current)"` 文本）。

改完之后，用户在两个地方看到的是同一种模型行：名字 + 请求落到哪；审批里选评审者时能一眼分出同 provider/model 的两行。

## Current state

原型（**这就是目标，逐行对照**）：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`
- 场景 id `model`（01 页）与 `exit-plan-pick`（18 页）；页面下方的「渲染契约」「计划审批（exit_plan_mode）的语义」两张卡片写死了列宽、省略、`●` 位置、无序号、无新增入口这些规则。
- 18 页目标形态（120 列）：

```
Which model should review this plan? (Esc to go back)
  deepseek/deepseek-v4-flash ●https://api.deepseek.com/chat/completions
❯ zhipuai/glm-5.3             https://open.bigmodel.cn/api/anthropic/v1/messages
  anthropic/claude-opus-4-1   https://api.anthropic.com/v1/messages
```

（这是原型 `columnRows` 真正画出来的形态：` ●` 两格吃进名字列与说明列之间那 2 格间距，名字最长的当前模型行 URL 紧跟图标。起草时这里写成 `●` 后四个空格，与原型渲染器不符，已按执行结论改正。）

### Part A：`/model` 的「添加自定义模型」

`pkg/turn/executor.go:795-846`（现状摘录，注释与代码一起删/改）：

```go
// ModelAddCustomValue is the /model picker's last row on the terminal: ...
const ModelAddCustomValue = "__add_custom__"

	items := make([]PickerItem, len(choices), len(choices)+1)
	for i, choice := range choices { /* …每行值=行 id，说明列=端点… */ }
	// The terminal can open the model setup from here; a channel or the web
	// cannot, so the row that does is the terminal's alone.
	if ctx.Surface == SurfaceTUI {
		items = append(items, PickerItem{Value: ModelAddCustomValue, Label: "Add a custom model…",
			Description: "OpenAI- or Anthropic-compatible, or custom protocol"})
	}
```

`pkg/tui/run.go:5291-5299`（现状摘录，整段删）：

```go
	// The model list's last row opens the custom form instead of naming a
	// model; that form writes the row and switches this session to it, exactly
	// as /connect does.
	if picker.Items[idx].Value == turn.ModelAddCustomValue {
		cmds.handleConnectCustom(ctx, state.sessionID)
		return true, false, ComposerSubmission{}, false
	}
```

`pkg/tui/setup.go:1887-1892`（删掉后**没有生产调用者**，连注释一起删）：

```go
// handleConnectCustom runs the model setup from the custom form's first
// question instead of the provider page: the /model picker's «Add a custom
// model…» row is the way in, and the answers, the write and the switch that
// follows are /connect's own.
func (c *commandController) handleConnectCustom(ctx context.Context, sessionID string) bool {
	return c.runConnect(ctx, sessionID, PromptCustomLLMSetup)
}
```

删掉 `handleConnectCustom` 之后，自定义表单**仍然只能从 `/connect` 进**——这是本步的关键事实，别再另找入口：

- `pkg/tui/run.go:1312-1313` → `handleConnect` → `runConnect(..., PromptMainLLMSetup)`；第一页的 provider 列表由 `pickProvider`（`pkg/tui/setup.go:783-813`）用 `SelectRich` 列出，最后一行就是 `customChoice()`（`pkg/tui/setup.go:670-680`，Label `Custom model`，`Custom: true`），选中后同一条 flow 走到 `setupPageBaseURL`。
- 因此 `PromptCustomLLMSetup`（`pkg/tui/setup.go:422-433`）也变成**只有测试在调**的死代码：它只是 `newLLMSetup(cfg)` 预置 custom choice、从 `setupPageBaseURL` 起跑。

```go
func PromptCustomLLMSetup(cfg PromptConfig) (process.Options, bool, error) {
	if cfg.Selector == nil { … }
	setup := newLLMSetup(cfg)
	setup.setChoice(customChoice())
	setup.page = setupPageBaseURL
	return setup.run()
}
```

它的 6 个测试（`pkg/tui/setup_test.go:882-1090` 一带，`TestCustomModelFlowSavesTheProviderItDescribes` 等）是这个表单的**唯一**覆盖，删函数时必须把它们改成从 `/connect` 的第一页进入：`fakeSelector.selects` 是「按 label 排队」的（`pkg/tui/chat_session_test.go:14325-14347`），`withCustomAnswers`（`setup_test.go:993-998`）在 `selects` 前插一条 `"Custom model"`、入口改成 `PromptMainLLMSetup(cfg)` 即可，断言不动。

钉住这一行现状的测试（Part A 要改/删）：
- `pkg/tui/commands_test.go:624-700`：两个测试，一个断言「选中自定义行不产生 engine choice」且第 2 行 `ElideTail == ""`，一个断言模型行照旧进引擎；都构造了 `turn.ModelAddCustomValue` 行。
- `pkg/tui/chat_session_test.go:1860-1880`：断言 TUI 的 `/model` labels 末尾是 `Add a custom model…`，`Items[2].Value == turn.ModelAddCustomValue`。
- `pkg/turn/model_test.go:640-660`：`TestModelPickerTellsTwinRowsApart` 断言 `Len(res.Picker.Items, 3)` 且 `Items[2].Value == ModelAddCustomValue`。
- `pkg/turn/slash_test.go:218-230`：断言 `/model` picker 的最后一项是 `ModelAddCustomValue`。

### Part B：评审选择器的两列列表

数据层缺字段——`pkg/turn/model.go:611-615`：

```go
type PlanReviewModelOption struct {
	Provider string
	Model    string
	Current  bool
}
```

`pkg/turn/approval.go:1085-1111` 从 `ModelChoices` 造它：**按行 id 去重**（`ModelChoice.ID`；只有拿不到 id 的旧配置才退回 `provider/model` 当分组键）、标 `Current`，**没带端点**。端点在 `ModelChoice` 上现成：`pkg/turn/models.go:653-691` 的 `BaseURL`/`APIPath`/`URLVerbatim` 与 `ModelEndpointLabel(baseURL, apiPath, urlVerbatim)`；同文件 `endpointTail(choice)`（`pkg/turn/executor.go:849-855`，同包）给出「窄列必须保住的那段」`APIPath`（`URLVerbatim` 行为空）。

渲染现状——`pkg/tui/overlays.go:697-715`：

```go
	case o.planReviewPicking:
		o.planReviewTopRow = lineCount
		writeLine("Which model should review this plan? (Esc to go back)")
		models := o.req.PlanReviewModels
		for i, model := range models {
			marker := "  "
			if i == o.planReviewCursor {
				marker = "\x1b[1m> \x1b[0m"
				o.focusStart = lineCount
				…
			}
			writeLine("%s%d. %s", marker, i+1, planReviewModelRow(model))
			…
		}
```

`planReviewModelRow`（`pkg/tui/overlays.go:1364-1372`）把 `" (current)"` 拼在名字后：

```go
func planReviewModelRow(model turn.PlanReviewModelOption) string {
	row := planReviewModelDisplay(model.Provider, model.Model)
	if model.Current {
		row += " (current)"
	}
	return row
}
```

按键——`pkg/tui/overlays.go:1308-1353`：`↑/↓/j/k` 移动、`PgUp/PgDn` 滚动、`Enter` 请求评审、`Esc` 回退，另有 `1..9` 直接跳行（`case key.r >= '1' && key.r <= '9'`，1336-1350）。序号去掉后这段数字跳行也要去掉。

滚动/聚焦规则**不要改**（`pkg/tui/overlays.go:867-882`、`1286-1296`）：选择器首帧把问句带到视野顶端（`planReviewTopRow`），之后按键只把选中行带进视野；`enterPlanReviewMode` 仍默认选第一个非 `Current` 的模型；`Esc` 仍回到 verdict 行且选中项不丢。

两列规则在库里也有唯一一份实现，**复用它，不要另起**（`pkg/tui/panel_columns.go`）：

```go
const panelColumnGap = 2        // 两列之间的固定间距
const panelNameColumnMax = 40   // 名字列上限；行宽 2/5 会再收窄
func elideEndpoint(url, tail string, width int) []string   // 从 base 中部省略，tail 永远保住
```

宽度测量用 `displayLineWidth`（`pkg/tui/render.go:5048`，去 ANSI 后按 runewidth 计），`●` 在某些 locale 下可能是 2 格——**不要写死 1 格**，一切用 `displayLineWidth(" ●")` 算。

行首光标字形与配色沿用选择器现有做法：`selectorCursorGlyph` + `panelAccentStyle`（`pkg/tui/render.go:7548-7552` 一带，`panelCursor` 就是它拼出来的：accent 110 的 `❯` + 空格）。审批 overlay 的其它行（verdict 行）继续用现在的 `"\x1b[1m> \x1b[0m"`，**不要**把它们改成 `❯`。

钉住 Part B 现状的测试：
- `pkg/tui/chat_session_test.go:4628-4660`：`selected := func(i int) string { return fmt.Sprintf("> %d. opencode-go/model-%02d", i, i) }`——断言编号行。
- `pkg/tui/chat_session_test.go:11070-11100`：断言 `"anthropic/claude-opus-4-1 (current)"`。
- `pkg/turn/approval_test.go:718-735`：断言 `PlanReviewModels` 的 `Current`/`Provider`/`Model`（新字段要一起补断言）。

web 侧不在本计划范围：审批卡的评审列表只显示 `provider/model` + 「当前」chip（`frontend/src/components/chat/PlanApprovalDock.vue:229-240`、`frontend/src/composables/usePendingActions.ts:102-104`），wire 形状在 `pkg/gateway/api_extra.go:4492-4502`（`planReviewModelWire`）。**不要给它加端点字段**。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `CGO_ENABLED=1 go build ./...` | exit 0 |
| Unit tests（本计划触及的三个包） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/gateway -count=1` | 全 ok |
| Vet | `go vet ./pkg/tui ./pkg/turn ./pkg/gateway` | exit 0 |
| Format | `gofmt -l pkg/tui pkg/turn pkg/gateway` | 无输出 |

（`FOREBRAIN_HOME` 必须是临时目录：`pkg/tui` 会驱动真实终端入口并打开状态库，继承 ambient home 会写用户家目录。）

## Scope

**In scope**（只改这些）：
- `pkg/turn/executor.go` — 删 `ModelAddCustomValue` 与 TUI 追加行；`execModel` 的注释跟着改。
- `pkg/tui/run.go` — 删 `__add_custom__` 分支。
- `pkg/tui/setup.go` — 删 `handleConnectCustom` 与 `PromptCustomLLMSetup`（含注释）。
- `pkg/tui/setup_test.go` — 自定义表单的 6 个测试改成从 `/connect` 第一页进入（`withCustomAnswers` 插 `"Custom model"`，入口换 `PromptMainLLMSetup`）。
- `pkg/turn/model.go` — `PlanReviewModelOption` 加端点字段。
- `pkg/turn/approval.go` — `PlanReviewModelOptions` 填端点。
- `pkg/tui/overlays.go` — 评审选择器两列渲染、`●`、去序号、去数字跳行、`planReviewModelRow` 改签名或删除。
- `pkg/tui/commands_test.go`、`pkg/tui/chat_session_test.go`、`pkg/turn/model_test.go`、`pkg/turn/slash_test.go`、`pkg/turn/approval_test.go` — 改/删对应断言，新增 Part B 的布局测试。

**Out of scope**（看起来相关但不要动）：
- `frontend/**` 与 `pkg/gateway/api_extra.go`：web 卡片不显示端点，wire 不加字段。
- `/connect` 的任何流程（`handleConnect`、`runConnect`、`PromptMainLLMSetup`、原型 B/D 组）——新增模型仍然只能从那里进。
- `pkg/tui/panel_columns.go` 的常量与 `elideEndpoint` 实现：只许调用，不许改语义（`/model` 也在用）。
- verdict 三行（`Yes, clear context…` / `Yes, proceed` / `No, keep planning`）与第 4 行 `Ask another model to review this plan` 的文案、编号、`"\x1b[1m> \x1b[0m"` 光标：不动。
- `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`：已经改好，是基准，不要改。

## Git workflow

- 仓库默认 **in-tree、零 commit**（owner 手动审阅提交）。若 owner 当次指令改用 worktree，就按 `exec/282` 建分支，收尾只回报 SHA 与判据结果，**不要** merge / rebase / 删 worktree / push。
- 提交信息风格（若被授权提交）：`feat(tui): ...` / `fix(tui): ...`，见 `git log -5`。

## Steps

### Step 1: `/model` 不再提供新增模型入口（Part A）

1. `pkg/turn/executor.go`：删 `const ModelAddCustomValue = "__add_custom__"` 及其注释；删 `execModel` 里 `if ctx.Surface == SurfaceTUI { items = append(…) }` 整段；`items := make([]PickerItem, len(choices), len(choices)+1)` 的容量改回 `len(choices)`；`execModel` 的 doc 注释里若有「最后一行打开自定义表单」类描述一并删掉。
2. `pkg/tui/run.go`：删 5291-5299 那段 `turn.ModelAddCustomValue` 分支（保留它前面的 model 行处理与后面的默认提交）。
3. `pkg/tui/setup.go`：删 `handleConnectCustom`（含注释）与 `PromptCustomLLMSetup`（含注释；`/connect` 的 provider 页自己会走到同一个表单）。
4. `pkg/tui/setup_test.go`：`withCustomAnswers` 在 `selector.selects` 队首插 `"Custom model"`；6 个 `PromptCustomLLMSetup(...)` 调用改成 `PromptMainLLMSetup(...)`，其余断言逐字不动。若某个测试的流程依赖「从 base URL 页起跑」（例如断言第一页的 `inputs`/`selects` 顺序），允许只在该测试里调整排队顺序，**不允许改断言内容**。若 `fakeSelector` 无法回答 provider 页（`SelectRich` 拿不到 queued label 对应的行），STOP 并回报，不要改成绕开 provider 页的私有入口。
5. 改 Part A 的四个测试文件：`TestModelPickerTellsTwinRowsApart` 的 `Len` 3→2、删 `Items[2]` 断言；`chat_session_test.go` 的 labels 断言删掉 `Add a custom model…`；`slash_test.go` 的「最后一项是 ModelAddCustomValue」改成断言最后一项是普通模型行（或删除该断言并说明理由）；`commands_test.go` 两个测试改成「所有行都是模型行」，删掉伪造的自定义行。

**Verify**：`grep -rn "ModelAddCustomValue\|handleConnectCustom\|PromptCustomLLMSetup" pkg cmd --include=*.go` → **无输出**；`CGO_ENABLED=1 go build ./pkg/turn ./pkg/tui` → exit 0。

### Step 2: `PlanReviewModelOption` 带上端点（Part B 数据层）

`pkg/turn/model.go` 的类型加两个字段（放在 `Current` 之后）：

```go
	// Endpoint is where this reviewer's requests go — the picker's second
	// column; EndpointTail is the part a narrow column must keep, the api
	// path (empty for a url-verbatim row).
	Endpoint     string
	EndpointTail string
```

`pkg/turn/approval.go` 的 `PlanReviewModelOptions` 在去重循环里填：用该 provider/model 命中的**第一个** `ModelChoice`（去重规则不变），`Endpoint: ModelEndpointLabel(choice.BaseURL, choice.APIPath, choice.URLVerbatim)`、`EndpointTail: endpointTail(choice)`。注释写清「twin 行按 provider/model 去重，说明列只代表第一行配置」。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'TestPlanReviewModel|TestPendingApproval' -count=1` → ok（测试夹具需要补端点字段，见 Step 5）。

### Step 3: 评审选择器改两列渲染

在 `pkg/tui/overlays.go` 的 `case o.planReviewPicking:` 里换掉编号行：

- 名字列宽：`colW := min(panelNameColumnMax, maxNameWidth, floor((width-cursorW)*2/5))`，其中 `cursorW = displayLineWidth("❯ ")`；`maxNameWidth` 是该列表所有 `planReviewModelDisplay` 的最宽值——**只量名字，不把 `●` 算进去**。
- 整列表堆叠条件：任一 `nameWidth > colW`（同样只量名字）。命中则整列表改为「名字一行 + 端点缩进挂在下面一行」，缩进 = 光标列宽（与 `/model` 的 `columnRowsStacked` 一致）。
- 非堆叠行：`光标 + 名字`，若 `Current` 追加 `" ●"`（`panelAccentStyle`），再补 `max(0, colW - nameWidth - markWidth + panelColumnGap)` 个空格（`markWidth` = `displayLineWidth(" ●")`，非当前行为 0），最后是 `elideEndpoint(Endpoint, EndpointTail, 剩余宽)` 的结果（一段，不折行时一行；盘子行用 `writeLine("%s", …)` 输出，注意 `writeLine` 会再走 `wrapCardLine`，所以拼接后的显示宽度不得超过 `width`）。
  端点列对带 `●` 的行与普通行都是 `光标宽 + colW + panelColumnGap`：`●` 连同前面一格空格吃进那 2 格间距，名字最长的当前模型行因此 URL 紧跟图标（原型 `columnRows` 的 `Math.max(0, colW - nameW - used + 2)`）。
- 光标：选中行用 `selectorCursorGlyph`（`❯`）+ `panelAccentStyle`，其余行两格空格；**不再有 `%d.`**。
- `planReviewModelRow` 删掉（它只在这里用）；`planReviewModelDisplay` 保留（确认行与 `Review by` 块仍在用）。
- `enterPlanReviewMode`/`exitPlanReviewMode`、`focusStart/focusEnd`、`planReviewTopRow` 逻辑保持不变。

**Verify**：`CGO_ENABLED=1 go build ./pkg/tui` → exit 0；`grep -n '%d\. %s' pkg/tui/overlays.go` → 无输出（该格式串只属于旧编号行）。

### Step 4: 去掉数字跳行

`pkg/tui/overlays.go:1336-1350` 的 `case key.r >= '1' && key.r <= '9': …` 整段删掉；`j`/`k` 保留。（序号不再显示，留下数字跳行等于隐藏快捷键。）

**Verify**：`grep -n "key.r >= '1'" pkg/tui/overlays.go` → 无输出。

### Step 5: 改/加测试

按 Test plan 一节逐条落。至少覆盖：两列对齐、`●` 位置、无序号、无 `(current)` 文本、堆叠、数字键无效、`Enter`/`Esc` 行为不变、`/model` 不再有自定义行。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/gateway -count=1` → 全 ok。

## Test plan

- **Part A（`/model`）**
  - `pkg/turn/model_test.go`：`TestModelPickerTellsTwinRowsApart` 改为两项，仍钉「twin 行不同 id、不同端点」；新增 `TestModelPickerOffersNoAddCustomRow`：`execModel` 的每个 item 的 `Value` 都非空且不等于 `"__add_custom__"`，且 TUI surface 与 web surface 的列表**完全一致**（同一 `len` 与逐项 `Value`）。
  - `pkg/tui/chat_session_test.go`：TUI `/model` 的 labels 只含已配置模型。
  - `pkg/tui/commands_test.go`：删掉两个围绕自定义行的测试；保留并改写 `TestSlashPickerModelRowStillReachesTheEngine`（单项列表也能进引擎）。
  - `pkg/tui/setup_test.go`：自定义表单的 6 个测试改从 `PromptMainLLMSetup` + provider 页 `Custom model` 行进入，断言逐字保留（覆盖不降级）；`TestProviderPageListsThreeGroups`（`setup_test.go:440-470`）已钉住 provider 页的 `Custom model` 行，不用改。
- **Part B（评审选择器）**，都放在 `pkg/tui/chat_session_test.go` 现有 exit-plan 选择器测试旁（那里已有 `newApprovalOverlay` + `buildApprovalContent` + `paintedScreen` 的现成夹具，**照它写**）：
  1. `TestExitPlanReviewPickerRendersTwoColumns`：120 列、两行带端点的选项；断言每行同时含 `provider/model` 与完整 URL，且两行 URL 的起始显示列相同（用 `displayLineWidth` 量前缀）。
  2. `TestExitPlanReviewPickerMarksCurrentWithGlyph`：当前模型行在名字后含 `●`，且不含 `(current)`；非当前行不含 `●`。
  3. `TestExitPlanReviewPickerHasNoRowNumbers`：`strings.Contains(plain, "1. ")` 为假（用该 fixture 的真实模型名做主断言，避免误伤 `Would you like to proceed?` 之外的文本）。
  4. `TestExitPlanReviewPickerStacksWhenNarrow`：68 列时每个名字独占一行、URL 在下一行缩进（名字行不含 URL）。
  5. `TestExitPlanReviewPickerIgnoresDigitKeys`：按 `'2'` 后 `planReviewCursor` 不变，`↓` 才变。
  6. 既有 `TestExitPlanOverlayRendersPickerAndCollectedReviews`（`chat_session_test.go:11070-11100`）把 `"anthropic/claude-opus-4-1 (current)"` 改成「名字 + `●`」断言；`chat_session_test.go:4628-4660` 的 `selected(i)` 辅助函数改成匹配无序号行。
  7. `pkg/turn/approval_test.go`：给新字段补断言（端点与 tail 来自第一行配置）。
- **结构性参照**：`pkg/tui/chat_session_test.go:4628-4660`（键盘流程）与 `pkg/tui/overlays_test.go` 里 approval overlay 的绘制断言。
- 不写前端测试：本计划不触前端。

## Done criteria

全部成立才算完成：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/gateway -count=1` 全 ok
- [ ] `go vet ./pkg/tui ./pkg/turn ./pkg/gateway` exit 0；`gofmt -l pkg/tui pkg/turn pkg/gateway` 无输出
- [ ] `grep -rn "ModelAddCustomValue\|handleConnectCustom\|PromptCustomLLMSetup" pkg cmd --include=*.go` 无输出
- [ ] `grep -n '(current)' pkg/tui/overlays.go` 无输出
- [ ] `grep -n "key.r >= '1'" pkg/tui/overlays.go` 无输出；`grep -n planReviewModelRow pkg/tui/overlays.go` 无输出（评审选择器不再有编号行。**不要**用 `grep '%d\. %s'` 判这条：verdict 三行按 Out of scope 仍保留编号，那条格式串还在 `default:` 分支里。）
- [ ] `git status --porcelain` 只列出 Scope 内的文件
- [ ] `plans/README.md` 的状态行更新（执行者维护时）

## STOP conditions

- drift check 命中的 in-scope 文件内容与本文摘录不符（已漂移）。
- `pickProvider`（`pkg/tui/setup.go:783-813`）里已经没有 `Custom model` 那一行，或 `customChoice()` 不再被 provider 页使用——那意味着 `/connect` 的自定义入口本身在漂移，删 `PromptCustomLLMSetup` 前先停下核实，不要把它当成唯一入口删掉。
- `fakeSelector` 无法让改写的 6 个测试从 `/connect` 第一页走进自定义表单（例如 `SelectRich` 不按 `selects` 队列匹配 label）——STOP，不要改成私有入口或直接删掉这些测试。
- 两列渲染让审批 overlay 的超长内容（计划正文 + 评审块 + 选项行）在 40 行终端里把 verdict 行挤出视野（overlay 的贴底规则被破坏）——停下来报告，不要靠缩字号/删行硬塞。
- `●` 在仓库的 `displayLineWidth` 下量出 2 格且按 2 格实现后 88 列触发整列表堆叠——照「整列表堆叠」规则走即可，但若连 120 列也堆叠，STOP（说明列宽算错）。
- 某一步的验证连续两次失败。

## Maintenance notes

- **web 卡片不显示端点**：`planReviewModelWire`（`pkg/gateway/api_extra.go:4528`）只带 `provider/model/current`。以后 web 也要两列时，要在 DTO 与 `frontend/src/lib/api.ts` 的 `PlanReviewModelOption` 上加字段，并同步审批卡布局——TUI 是标准，但三面（TUI live / TUI replay / web）的**语义**必须一致：哪些模型可评、哪个是当前、选中的是谁。
- **`/model` 与评审列表共用的列规则**只有一份：`pkg/tui/panel_columns.go` 的 `panelColumnGap`/`panelNameColumnMax`/`elideEndpoint`。以后要改列宽或省略算法，两处同时生效是正确的；不要在任何一处复制一份。
- **去重口径**：`PlanReviewModelOptions` 按**行 id** 去重（`ModelChoice.ID`；拿不到 id 的旧配置才退回 `provider/model`），所以同 provider/model 的两行配置在评审列表**各出现一次、各带自己的端点**——这正是端点列存在的理由（起草时写的「按 provider/model 去重、twin 行只出现一次」是错的）。评审请求仍按 `provider/model` 记（`turn.PlanReviewModelOption.MatchesSelection`），于是两行分得开、选谁却等价：这是既有的语义缺口（见「执行记录」），不是本计划引入的。
- **评审者身份仍只有 provider/model**：`process.PlanReviewer` 用 `turn.Model{Provider, Model}` 构建，端点字段只用于显示；不要让它成为身份的一部分。
- 若以后 `/model` 又要提供新增模型入口，恢复的不应是「最后一行魔法值」，而是 `/connect` 的入口（owner 2026-11-09 裁决：那是 `/connect` 的功能）。

## 执行记录（2026-11-09，`execute 282`）

- **结果**：评审 APPROVE。隔离 worktree `/tmp/fb-282-exec`，分支 `exec/282`，原 commit `741016a2`（12 个文件 +352/−157）。**2026-11-09 owner 指示落库**：主树快照先提交为 `a9258099`，`exec/282` rebase 到它成为 `c81640a4`，主树 `git merge --ff-only` 进 dev；worktree 已删，分支 `exec/282` 保留。
- **判据复跑（评审方在 worktree 内独立执行）**：`go build ./...` exit 0；`FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/gateway -count=1` 三包全 ok（82.9 s / 3.1 s / 28.9 s）；`go vet ./pkg/tui ./pkg/turn ./pkg/gateway` exit 0；`gofmt -l pkg/tui pkg/turn pkg/gateway` 无输出；`git status --porcelain` 空。
- **实际渲染**（120 列；评审方临时测试导出后即删，不在提交里）：

```
Which model should review this plan? (Esc to go back)
  deepseek/deepseek-v4-flash ●https://api.deepseek.com/chat/completions
❯ zhipuai/glm-5.3             https://open.bigmodel.cn/api/anthropic/v1/messages
  anthropic/claude-opus-4-1   https://api.anthropic.com/v1/messages
```

- **本轮改正的计划文本错误**（执行以原型渲染器与实码为准，正文已就地改好）：
  1. 18 页样例块原写 `●` 后四个空格；原型 `columnRows` 实际把 ` ●` 吃进那 2 格间距，最长名字那行 URL 紧跟图标。
  2. 原堆叠条件多一条「`nameWidth + displayLineWidth(" ●") > colW`」，会让原型自己的 120 列场景堆叠；正确规则是只量名字。
  3. `PlanReviewModelOptions` 实际按行 id 去重（`ModelChoices` 永远给 id，pre-table 条目也有确定性 id），不是 provider/model。
  4. Done criteria 里 `grep '%d\. %s' pkg/tui/overlays.go` 无输出不可能成立：verdict 三行按 Out of scope 保留编号，那条格式串仍在 `default:` 分支。
- **第一次提交被 REVISE 的原因**：`●` 被算进名字列宽，端点列整体右移 2 格、名字列被撑宽——违反原型卡片「说明列的对齐不变，也不撑宽名字列」。现版本按原型规则测量，并加了护栏断言（端点列 = 光标宽 + 最长名字 + 2；`●` 与 URL 之间恰为 0 格）。
- **已知遗留（不在本计划范围）**：评审请求的身份只有 provider/model，所以同 provider/model 的两行虽然分得开，选哪一行发出的评审请求等价。要让端点成为身份的一部分需另立计划（牵动 `process.PlanReviewer` 与落库的评审记录）。
- **未跑真机验收**：本计划判据只到渲染层；真机（fake 模型 + tmux）如需另按 owner 指示补。
