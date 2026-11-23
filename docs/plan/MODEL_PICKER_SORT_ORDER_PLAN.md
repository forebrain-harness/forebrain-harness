# 模型列表排序：/model 面板、web 输入框模型选择器、exit_plan_mode 审批面板统一按 provider → 模型名 → endpoint

状态：已实施并真机验收通过（2026-11-13）
基线：HEAD `c3377e148539accbb33f925c857ae3a25f34d81d`，工作区另有他人未提交改动
（`pkg/run/subagent.go`、`pkg/process/peer_inbound.go`、`pkg/tui/setup.go` 等）——
不属于本计划，不得修改/暂存/提交。
**计划落盘**：批准后把本文件复制到 `docs/plan/MODEL_PICKER_SORT_ORDER_PLAN.md`（`docs/plan` 已 gitignore）。

---

## 1. 为什么做

owner 反馈：TUI `/model` 面板里的模型列表"排序好像是无序的，看不出来规则，不好找需要的模型"，并要求
**web 对话页输入框的模型选择器**、**`exit_plan_mode` 审批面板里的模型列表**这三处一并按
**同一条规则**排序（owner 本轮两次追加，最后确认规则为 A：provider → 模型名 → endpoint）。

本安装真实有 67 行已配置模型、9 个 provider；按库中 created_at 顺序渲染时 provider 交错、同名模型不连续，
几十行的列表无法扫描。

## 2. 根因（已现场复现，非推测）

### 2.1 一处数据源，三个呈现面

三处列表**全部**来自同一个函数 `turn.ModelChoices`：

| 面 | 链路 |
| --- | --- |
| TUI `/model` 面板 | `pkg/turn/executor.go:777 execModel` → `ModelChoices(...)`（`:785`）→ `PickerItem[]`（`:795-804`）→ `pkg/tui/run.go:5345-5370 runSlashPicker` 组装 `SelectItem`（原序）→ richPicker 绘制 |
| web 对话页输入框模型 chip | `pkg/gateway/api_extra.go:1887 sessionModelOptions` → `ModelChoices`（`:1892`）→ `/api/chat/model`（`:1957`）、`/api/sessions/{id}/model`（`:1998`）的 `options`；`frontend/src/components/chat/ModelChip.vue:163 visibleOptions` 只做过滤，按数组原序渲染（`:39 v-for="option in visibleOptions"`） |
| `exit_plan_mode` 审批面板 | `pkg/turn/approval.go:1260 PlanReviewModelOptions` → `ModelChoices`（`:1261`）→ TUI `pkg/tui/chat_surface.go:611-616 ReviewModels` → `pkg/tui/overlays.go:1358/1386` 按 `req.PlanReviewModels` 原序绘制；gateway 侧 `pkg/gateway/approval.go:58` 同一函数 |
| （附带一致化）`/api/llm-models/choices` | `pkg/gateway/models_api.go:623 handleLLMModelChoices` → `ModelChoices`（`:632`），设置页 goal-check 模型下拉、子代理配置抽屉读它 |
| `/model` 的写入侧（**顺序安全，不改**） | `pkg/turn/executor.go:874 chooseModel`、`:949 chooseReasoningEffort` 同样调 `ModelChoices`，但两者都**按 row id / 唯一 label 取行**（`choiceByID`、`countChoicesByLabel`），`changed` 也只在同一份切片内与 `defaultModelIndex` 比索引（`:898`）——排序前后语义一致 |

### 2.2 顺序从哪里来

`pkg/turn/models.go:709`（现行原文）：

```go
// ModelChoices lists the models the install has configured, in their
// configured order — storage order, not priority: the one in force is the
// effective default (the agent's default_model reference, else its first
// legacy entry, else main's, else the view's first row).
func ModelChoices(cfg *appcfg.Root, agentName string) []ModelChoice {
	var out []ModelChoice
	for _, configured := range appcfg.ConfiguredLLMs(cfg) {
		...
		out = append(out, choice)
	}
	return out            // ← 无任何排序
}
```

`pkg/config/llm_models.go:90 ConfiguredLLMs` 的顺序是 `root.LLMModels` 的**存库顺序**
（`fb_llm_models.created_at`，即用户历史逐个 `/connect` 的先后），再拼 legacy `llm_providers`。
→ 列表顺序 = 用户"什么时候连上的"，没有任何可读规则。

**这条存库顺序有语义，不能排序**：`appcfg.EffectiveDefaultLLMFor` 的兜底是"视图第一行"，
owner 2026-10-23 也裁决过 `llm_providers` 顺序冻结（任何命令不得改序）。所以修复只能落在
**显示投影** `ModelChoices`，不能动 `ConfiguredLLMs` 或 `fb_llm_models`。

### 2.3 现场证据

owner 真实库 `~/.forebrain/state/forebrain.state.sqlite`，`fb_llm_models` 67 行：

```
$ sqlite3 ~/.forebrain/state/forebrain.state.sqlite \
    "SELECT rowid, provider, model, created_at FROM fb_llm_models ORDER BY created_at, id;" | head -10
1 | opencode-go | step-5-preview        | 1794203733946
2 | opencode-go | deepseek-v4.1-flash   | 1794203733946
5 | opencode-go | kimi-k3               | 1794203733946
3 | opencode-go | qwen3.8-flash         | 1794203733946
4 | opencode-go | qwen3.8-max           | 1794203733946
13| zhipuai     | glm-5.3-flash         | 1794203733949
7 | opencode-go | beam-free             | 1794203733949
18| chatgpt     | gpt-6-astra           | 1794203733949
10| alibaba     | deepseek-v4.1-flash-1017 | 1794203733949
11| deepseek    | deepseek-v4-flash     | 1794203733949
```

真机复现（`-tags fts5` 二进制 + 隔离 home，把上述 67 行按真实 created_at 灌入 scratch 库）
`/model` 首屏：

```
  Model
  The model this agent runs on

    ⌕  Type to filter

  opencode-go/step-5-preview              https://opencode.ai/inference/go/chat/completions
❯ opencode-go/deepseek-v4.1-flash         https://opencode.ai/inference/go/chat/completions
  opencode-go/qwen3.8-flash               https://opencode.ai/inference/go/v1/messages
  opencode-go/qwen3.8-max                 https://opencode.ai/inference/go/v1/messages
  opencode-go/kimi-k3                     https://opencode.ai/inference/go/chat/completions
  alibaba/deepseek-v4.1-flash-1017        https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions
  deepseek/deepseek-v4-flash              https://api.deepseek.com/chat/completions
  zhipuai/glm-5.3                         https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
  zhipuai/glm-5.3-flash                   https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
  deepseek/deepseek-v4-flash-0731         https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions
  openai/gpt-5.6-sol                      https://fast.sbbbbbbbbb.xyz/v1/chat/completions
  chatgpt/gpt-5.6-luna                    https://chatgpt.com/backend-api/codex/responses
  ↓ 54 more
```

（复现环境已留在 `/tmp/fb-model-order-repro`：`home/` scratch home + 真实 67 行、
`proj/` git 仓库；provider 交错、同名模型不连续，与 owner 描述一致。
注意 `ORDER BY created_at, id` 的 dump 与屏幕不完全对应：`ConfiguredLLMs` 按 `root.LLMModels` 的切片序
即 `created_at, id` 索引（`idx_fb_llm_models_created`）给出，屏幕是同一顺序的渲染，组内以 rowid/id 为序。）

### 2.4 仓库里已经存在的"一个排序语义"

`pkg/turn/models.go:539 ListModelCatalog`（模型目录列表，`/api/models` 与 TUI 模型页读它）
已经有排序（`:563-573`）：**provider 升序 → APIModel 升序 → ModelID 升序**，全部 `ToLower` 比较。
本修复就是把 `/model` 等"已配置模型列表"归一到这条既有约定，而不是新造一门排序。

## 3. 修复设计

### 3.1 规则（owner 本轮已确认 A）

1. `Provider` 升序（`ToLower` 比较）
2. `ModelName` 升序（`ToLower`）——**屏幕上模型半栏显示的就是它**（`Label = provider/ModelName`；
   catalog 有显示名就是显示名，没有就是 wire id），所以排序结果与肉眼看到的单调一致
3. `Model`（wire id）升序——同一 provider 下显示名相同的两行仍可判序
4. `BaseURL` 升序 → `APIPath` 升序——同 provider+model 的"双子行"（不同 endpoint）相邻且稳定

`ConfiguredLLMs` 已按 provider/model/base_url/api_path 归一化去重（`pkg/config/llm_models.go:95-101`），
所以四个键打完不会残留并列行，排序是确定的。

### 3.2 唯一实现点

只改 `pkg/turn/models.go` 一处（`ModelChoices` 返回前排序），三面及 `/api/llm-models/choices` 同时生效。
**不在任何 surface 自排**（否则就是第二处实现）。

代码形状（执行者可照抄，函数名/注释可微调但语义与顺序键不得改）：

```go
// ModelChoices lists the models the install has configured, in the one order
// every picker shows them: provider, then the model's readable name, then the
// wire id, then the endpoint — so a provider's rows are one block, rows that
// share a provider/model sit together, and the same configuration always draws
// the same list. The one in force is the effective default (the agent's
// default_model reference, else its first legacy entry, else main's, else the
// view's first row) — resolved by EffectiveDefaultLLMFor and found by id, not
// by position; the storage order ConfiguredLLMs carries is not touched.
func ModelChoices(cfg *appcfg.Root, agentName string) []ModelChoice {
	var out []ModelChoice
	// ... 现有循环保持不变 ...
	sort.SliceStable(out, func(i, j int) bool { return modelChoiceLess(out[i], out[j]) })
	return out
}

// modelChoiceLess is the display order of configured models: provider, the
// model's readable name (what the picker shows), the wire id, then the endpoint
// a row sends to — twins of one provider/model stay adjacent and in a stable
// order. Case-insensitive, the way the model catalog's own listing sorts.
func modelChoiceLess(a, b ModelChoice) bool {
	if cmp := strings.Compare(strings.ToLower(a.Provider), strings.ToLower(b.Provider)); cmp != 0 {
		return cmp < 0
	}
	if cmp := strings.Compare(strings.ToLower(a.ModelName), strings.ToLower(b.ModelName)); cmp != 0 {
		return cmp < 0
	}
	if cmp := strings.Compare(strings.ToLower(a.Model), strings.ToLower(b.Model)); cmp != 0 {
		return cmp < 0
	}
	if cmp := strings.Compare(strings.ToLower(a.BaseURL), strings.ToLower(b.BaseURL)); cmp != 0 {
		return cmp < 0
	}
	return strings.ToLower(a.APIPath) < strings.ToLower(b.APIPath)
}
```

`sort` 已在 `pkg/turn/models.go` 导入（`:261`、`:563` 在用），无需新增 import。

顺带把描述旧顺序的注释改成新语义（只改注释，不改行为）：
- `pkg/turn/models.go:705-708` 的 `ModelChoices` doc（上面已给新文）
- `pkg/gateway/models_api.go:620-622`：`the install's models in configured order` →
  `in the display order every picker uses (provider, then model name, then endpoint)`

### 3.3 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
| --- | --- | --- | --- |
| TUI `/model` 面板 | 存库（connect）顺序，provider 交错 | provider 字母序 → 模型名 → endpoint | 本期主目标 |
| web 对话页输入框模型 chip 下拉 | 同上 | 同上 | 本期追加目标；前端零改动（按 props 原序渲染） |
| `exit_plan_mode` 审批面板的 review 模型列表（TUI 卡 + web 审批卡） | 同上 | 同上 | 本期追加目标；与执行模型无关，只是"把计划交给谁评审"的候选列表 |
| `/api/llm-models/choices`（设置页 goal-check 下拉、子代理配置抽屉） | 同上 | 同上 | 刻意一致化：同一份候选列表只有一个顺序 |
| 当前生效模型的标记（`❯`／✓／gate 的 Current） | 按 row id 定位 | 不变（index 变了，标记跟着行走） | 由 `defaultModelIndex`/`selectionMatches` 按 id/对确定，与位置无关 |
| 默认模型解析（`EffectiveDefaultLLMFor`、`ConfiguredLLMs` 首行兜底） | 存库顺序 | 不变 | 本计划不碰 `pkg/config` |
| `fb_llm_models` / `llm_providers` 存库顺序 | 冻结 | 不变 | 只排显示投影 |
| 会话已存模型选择（`fb_session_model_state`） | row id | 不变 | 未改写入路径 |

## 4. Scope

**In scope**
- `pkg/turn/models.go`（`ModelChoices` + 新增 `modelChoiceLess`；doc 注释）
- `pkg/gateway/models_api.go:620-622`（仅注释）
- 测试：`pkg/turn/models_test.go`、`pkg/turn/slash_test.go`、`pkg/turn/approval_test.go`、`pkg/gateway/slash_handlers_test.go`、必要时 `pkg/gateway/api_extra_test.go`
- 计划文件回填 `docs/plan/MODEL_PICKER_SORT_ORDER_PLAN.md`

**Out of scope（明确不碰）**
- `pkg/config/llm_models.go`（`ConfiguredLLMs` 顺序有语义）、`pkg/config/*` 任何写路径、`fb_llm_models` 数据
- `frontend/**`：web 选择器按 API 原序渲染，引擎排序已贯通；本期不改前端（若验证发现前端另排，见 STOP）
- `pkg/tui/*`：picker/overlay 按 item 原序绘制，无需改；`SelectItem.Category` 分组标题是另一个显示层议题。
  特别地 `pkg/tui/setup.go`、`pkg/tui/setup_test.go` 是 peer e9 的 `/connect` WIP，本计划不碰
- 目录列表 `ListModelCatalog` 的既有排序、模型管理页（`/api/llm-models` 行表）顺序
- 他人 WIP：`pkg/run/subagent.go`、`pkg/run/subagent_test.go`（peer 1e/他人）等未提交改动

## 5. 步骤（每步带验证与预期）

> 所有 go 命令前置（`pkg/pdf` 的 cgo 需要）：
> `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`
> 测试统一用隔离 home，避免污染真实 `~/.forebrain`：`export FOREBRAIN_HOME="$(mktemp -d)"`

1. **落盘计划**：`cp <plan dir>/model-picker-sort-order.md docs/plan/MODEL_PICKER_SORT_ORDER_PLAN.md`。
   验证：`test -f docs/plan/MODEL_PICKER_SORT_ORDER_PLAN.md && echo OK`；`git status --porcelain` 不出现它（已 gitignore）。

2. **实施排序**：按 §3.2 改 `pkg/turn/models.go`，改两处注释（§3.2 末尾）。
   验证：`CGO_ENABLED=1 go build -tags fts5 ./pkg/turn ./pkg/gateway` → 无输出；`gofmt -l pkg/turn pkg/gateway` → 空。

3. **新增回归测试**（`pkg/turn/models_test.go`，跟既有 `TestModelChoices*` 同风格）：

   - `TestModelChoicesSortByProviderThenModelNameThenEndpoint`：构造乱序 cfg（provider 交错、
     同一 provider 内模型名乱序、`deepseek/deepseek-v4-flash` 双子行 base_url 一/二、一个大写
     `meituan/LongCat-2.5` 与一个纯小写 wire id），断言 `ModelChoices` 的 `Provider/ModelName/BaseURL`
     序列**逐项**等于期望切片；断言双子行相邻且 `http://one.test` 在 `http://two.test` 之前。
   - `TestModelChoicesSortKeepsTheDefaultMarked`：用 `execModel`（复用 `newModelSelectionFixture`）在一个
     "默认模型不在排序首位"的 cfg 上断言 `res.Picker.Items` 里 `Current` 恰好一个，且其 `Value` 是该 row id、
     `Label` 是该行——即排序不改"当前模型"判定（防止把默认解析与显示序耦合）。
   - `TestModelChoicesDoNotReorderTheStoredView`：断言 `appcfg.ConfiguredLLMs(&cfg)` 的顺序仍是配置原序，
     而 `ModelChoices(&cfg, ...)` 是排序后的顺序（把"只排显示投影"钉成契约）。

   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'TestModelChoices' -count=1 -v` → PASS。

4. **修订受契约变更影响的既有断言**（逐条注明理由："显示顺序由存库顺序改为统一排序"）：

   - `pkg/turn/slash_test.go:345-370 TestModelChoicesAreTheInstallWideModels`：`want` 改为排序后的
     `[anthropic/claude-sonnet-4-5, openai/gpt-5.3-codex, openai/gpt-5.4, openai/gpt-5.5, zhipuai/glm-5.1]`
     （`openai/gpt-5.3-codex` 与 `openai/gpt-5.4` 的先后由 ModelName 决定，执行时以实际值核对再写死），
     并追加一行断言"存库视图未变"。函数 doc 首行把"in configured order"改成新规则。
   - `pkg/gateway/slash_handlers_test.go`（`/model` 两模型的那条）：改成按 label 找**非** current 行来切换，
     不再依赖 `Items[1]`；`Items[0].Current` 那句改成"current 行在排序中的位置"。
   - `pkg/gateway/api_extra_test.go:6740 TestSessionModelOptionsCarryOpenCodeEfforts`：若显示名把
     `glm-5.3 / minimax-m3 / qwen3.8-max` 的次序打乱，改成按 `option.Model` 取行再断言 efforts（其余逻辑不动）。
   - `pkg/turn/models_test.go:554 TestModelChoicesCarryTheRowsConfiguredEffort` 等按 `choices[0]`/`choices[1]`
     取行的既有用例：排序后若行位置变化，改成按 `choice.Model` 取行再断言（不断言位置，断言事实）。
   - `pkg/turn/approval_test.go`：新增 `TestPlanReviewModelOptionsFollowTheDisplayOrder`——cfg 行乱序
     （openai 在 deepseek 之前）断言 `options` 按 provider→模型名 排，且 `Current` 仍落在会话所在的 row id。

   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'TestModelChoices|TestExecModel|TestChooseModel|TestPlanReview' -count=1` → PASS。

5. **包级回归 + vet**：
   ```bash
   FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui -count=1
   CGO_ENABLED=1 go vet -tags fts5 ./pkg/turn ./pkg/gateway
   gofmt -l pkg/turn pkg/gateway pkg/tui
   ```
   预期：全 PASS / vet 无输出 / gofmt 无输出。
   若出现与本改动无关的失败：先确认是否他人 WIP 文件（`pkg/run/subagent*.go`、`pkg/tui/notify.go`、
   `pkg/tui/setup.go`、`pkg/tui/zz_connect_repro_test.go`）——按 STOP 处理，不代为修。

6. **tmux 真机三面验收**（构建必须 `-tags fts5`）：
   ```bash
   CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
   ```
   - **TUI `/model`**：复用 `/tmp/fb-model-order-repro`（scratch home 已灌入真实 67 行、`proj/` 已信任）
     ```bash
     tmux new-session -d -s fbmodel -x 150 -y 46 "cd /tmp/fb-model-order-repro/proj && \
       FOREBRAIN_HOME=/tmp/fb-model-order-repro/home $PWD/build/bin/forebrain; echo EXIT=\$?; sleep 600"
     tmux send-keys -t fbmodel '/model'; sleep 1; tmux send-keys -t fbmodel Enter; sleep 2
     tmux capture-pane -pt fbmodel
     ```
     预期：首屏 provider 块按字母序（`alibaba…`、`chatgpt…`、`deepseek…`、`meituan…`、`openai…`、
     `opencode-go…`、`qodercn…`、`volcengine…`、`zhipuai…`），块内模型名升序，`❯` 落在原来的默认行
     （`opencode-go/deepseek-v4.1-flash`，现在不在首行）；`↓ N more` 与 67 行总数一致。
     再 `↓` 跨 provider 边界 2 次、`Enter` 选一行 → 回复 `Switched to …`，footer 模型名变化；重开 `/model`
     顺序不变、`❯` 在新选中行。`Esc` 退出面板不切换模型。
   - **web 输入框模型选择器**（引擎层就是它读的那份）：
     ```bash
     FOREBRAIN_HOME=/tmp/fb-model-order-repro/home FOREBRAIN_GATEWAY_TOKEN=t \
       ./build/bin/forebrain gateway start &   # 起在 127.0.0.1:6060
     # 对话页 chip 读的那个接口（字段名是 JSON tag：model_name / base_url）
     curl -s -H 'Authorization: Bearer t' -H 'X-Forebrain-Project: /private/tmp/fb-model-order-repro/proj' \
       http://127.0.0.1:6060/api/chat/model \
       | jq -r '.options[] | "\(.provider)\t\(.model_name)\t\(.base_url)"'
     ```
     预期：`options` 顺序与 TUI `/model` 完全一致（同一条规则、同一份数据）。
     若该进程没绑定 runner（`handleChatModelDefault` 无 runner 回 503，`api_extra.go:1966-1975`）
     或该路由要会话 id（改用 `GET /api/sessions/<id>/model`，同结构，同样要 runner），
     退回不依赖 runner 的同源接口 `GET /api/llm-models/choices`（`api_extra.go:421` → `models_api.go:632`，
     同一个 `turn.ModelChoices`；只要求 bound state DB + config）：
     ```bash
     curl -s -H 'Authorization: Bearer t' http://127.0.0.1:6060/api/llm-models/choices \
       | jq -r '.[] | "\(.provider)\t\(.model_name)\t\(.base_url)"'
     ```
     预期与上面同一顺序。
   - **`exit_plan_mode` 审批面板**（fake provider，禁真模型）：
     ```bash
     D=.claude/skills/run-forebrain/driver.sh
     $D build && $D start tool '{"name":"exit_plan_mode","arguments":{}}'
     $D submit 'please exit plan mode'
     $D wait 'exit plan mode' 20 && $D screen
     ```
     预期：审批卡出现，其 review 模型行按新顺序排列；`$D provider-log` 证明请求来自 fake provider。
     若 driver 的隔离 home 只有 1 行模型，先用 §2.3 的 INSERT 手法给它的
     `$TMPDIR/forebrain-run/home/state/forebrain.state.sqlite` 灌 3 个不同 provider 的行再重启（不改产品代码）。
     该面板若因 fake provider 的工具名/参数不匹配而无法停住，**不追加产品改动**：改为
     `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PlanReview|ExitPlan' -count=1` 的渲染断言 +
     `pkg/turn` 的 `PlanReviewModelOptions` 顺序断言，并在验收记录里写明真机未覆盖的原因。

7. **回填**：计划文件状态行改"已实施并真机验收通过（日期）"，追加"验收记录（场景→证据）"；
   清理 tmux 会话。改动留工作区，**不 commit**。

## 6. Done criteria（机器可查）

1. `git diff --stat` 只含：`pkg/turn/models.go`、`pkg/gateway/models_api.go`、`pkg/turn/models_test.go`、
   `pkg/turn/slash_test.go`、`pkg/turn/approval_test.go`、`pkg/gateway/slash_handlers_test.go`、
   （必要时）`pkg/gateway/api_extra_test.go`；`git status --porcelain` 里他人的
   `pkg/run/subagent*.go`、`pkg/tui/zz_connect_repro_test.go` 等改动原样保留、未被 stage。
2. `grep -rn "modelChoiceLess" pkg` 只命中 `pkg/turn/models.go`（单一实现）；
   `grep -rn "ModelChoices(" pkg --include=*.go | grep -v _test` 的调用点不新增排序逻辑。
3. `FOREBRAIN_HOME=$(mktemp -d) CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` PASS。
4. `CGO_ENABLED=1 go vet -tags fts5 ./pkg/turn ./pkg/gateway` 无输出；`gofmt -l pkg/turn pkg/gateway pkg/tui` 空。
5. 真机证据入档：`/model` 首屏截图（provider 块字母序）+ 选择后 footer 变化截图 + web `options` 的
   `provider / model_name / base_url` 序列（与 TUI 逐行一致）+ exit gate 面板截图（或按 §5.6 的替代断言与原因说明）。
6. `frontend/` 无改动（`git status --porcelain frontend` 空）。

## 7. STOP conditions

- **出现第二处排序**：若发现别处（`pkg/tui`、`frontend`、`pkg/gateway` 某 handler）也在重排模型列表 →
  停下来汇报并把该处归一到 `turn.ModelChoices`，不得留两份规则。
- **排序影响了默认/当前模型**：`EffectiveDefaultLLMFor` 结果变化、`❯`/`Current` 落错行、
  或 `/model` 选"当前模型"时误报 `Switched to`（而不是 `Still using`）→ 停，说明显示序与解析语义被耦合了。
- **需要改前端源码**才能让 web 选择器一致 → 停（说明引擎排序未贯通三面），先汇报设计再动。
- **他人 WIP 冲突**：`pkg/turn`、`pkg/gateway` 或 `pkg/tui` 测试失败在
  `pkg/run/subagent*.go`、`pkg/tui/notify.go`、`pkg/tui/setup.go`、`pkg/tui/zz_connect_repro_test.go`
  （peer 1e / e9 在改）相关文件 → 先 `peer_send` 询问归属，不代改、不 revert。
- **基线漂移**：`pkg/turn/models.go` 的 `ModelChoices` 已不是 §2.2 摘录的形状（循环体/字段名对不上）→
  停，重新读当前源码再改计划。
- **契约变更超出矩阵**：任何"用户可见但不是本期目标"的顺序/行为变化（例如模型管理页行表顺序、目录列表顺序）
  → 停，回到计划。

## 8. Maintenance notes（给后续人/agent）

- **一个概念一个实现**：任何"列出已配置模型"的新面（新面板、新 API、新客户端）都必须走
  `turn.ModelChoices`，不得自己 sort；目录列表（`ListModelCatalog`）是另一个概念（catalog 模型，不是已配置行），
  它已有自己的 provider→model 排序，两者键一致但不是同一函数。
- **存库顺序仍是语义**：`appcfg.ConfiguredLLMs` = `fb_llm_models.created_at` + legacy 列表序，
  被 `EffectiveDefaultLLMFor` 的"首行兜底"依赖；排序只允许出现在显示投影层。想改这条要先动默认模型语义。
- **显示名的来源**：`ModelChoice.ModelName` = catalog 显示名（`models.json` 的 `name`），catalog 未解析时退化为 wire id；
  排序键用 `ModelName` 正是为了让屏幕单调。若将来 catalog 显示名大改，列表顺序会随之微调（预期行为）。
- **可能的后续（本期不做）**：①"当前模型置顶"；②provider 分组标题——TUI 的 `SelectItem.Category` 已能画分组标题
  （`richPickerState.panel` 按 `item.Category` 开组），但 web `ModelChip` 没有分组概念，需要设计两侧一致的呈现。
- **复查要点**：排序键是否漏了 `Model`/endpoint 兜底（双子行会不稳定）；`Current` 判定是否仍按 row id；
  默认解析路径未被触碰（`pkg/config` diff 应为空）。

---

## 验收记录（2026-11-13，场景 → 证据）

基线：`c3377e148539accbb33f925c857ae3a25f34d81d`（drift check 通过：`ModelChoices` 循环体、字段名、
`sort` 已导入均与 §2.2 摘录一致）。

| 场景 | 证据 |
| --- | --- |
| TUI `/model` 首屏（scratch home `/tmp/fb-model-order-repro`，真实 67 行 / 9 provider） | provider 块字母序：`alibaba`(1) → `chatgpt`(3) → `deepseek`(4) → `meituan`(1) → `openai`(2) → `opencode-go`(16) → `qodercn`(12) → `volcengine`(26) → `zhipuai`(2)；块内模型名升序；`●`/`❯` 落在原默认行 `opencode-go/deepseek-v4.1-flash`（现位于索引 12，不在首行）；`↑ 1 more · ↓ 53 more` + 13 行 = 67 |
| 跨 provider 边界 + 选择生效 | `↓`×15 从 `opencode-go/step-5-preview` 进入 `qodercn/Auto`、`qodercn/DeepSeek-Flash`；`Enter` → `Switched to qodercn/DeepSeek-Flash.`；effort 面板 `Esc` 后 footer 变 `qodercn/DeepSeek-Flash`；重开 `/model` 顺序不变、`❯` 在新选中行；`Esc` 退出面板不再切换 |
| web 输入框 chip 读的那份列表 | `GET /api/llm-models/choices` → 67 条，按 (provider, model_name, model, base_url, api_path) 全序单调；双子行相邻（`opencode-go/deepseek-v4.1-flash` 12/13、`volcengine` 39/40、43/44、45/46 …） |
| web 对话页 chip 接口 | `GET /api/chat/model` → 同一 67 行、与 choices 逐行一致（首行 `alibaba/deepseek-v4.1-flash-1017`）；`frontend/src/components/chat/ModelChip.vue` 的 `visibleOptions` 只过滤不排序，`git status --porcelain frontend` 空 → 前端零改动 |
| `exit_plan_mode` 审批面板 review 模型列表 | driver（fake provider，禁真模型）：`/plan` 进 plan mode → 提交 → 审批卡 → `4. Ask another model to review this plan` → 列表 `deepseek/deepseek-chat ●`、`openai/gpt-5.5`、`zhipuai/glm-5.3`（配置序为 deepseek, zhipuai, openai）；`$D provider-log` 见 `REQUEST /chat/completions stream=True mode=tool`，行内 endpoint 为 `http://127.0.0.1:8771/...` |
| 默认/当前模型不受排序影响 | `pkg/turn` 用例 `TestModelChoicesSortKeepsTheDefaultMarked`（默认行不在首位仍唯一 `Current`、按 row id 定位、选它不产生 `select(`）+ `TestModelChoicesDoNotReorderTheStoredView`（`ConfiguredLLMs`/`EffectiveDefaultLLMFor` 仍取存库首行） |

真机备注：driver 的 `toolcall` 模式本次无法停住 gate —— 其 scratch 项目被判定为 office 类型，
`enter_plan_mode` 未注册（DB 里 `Error executing tool 'enter_plan_mode': unknown tool "enter_plan_mode"`），
与本次改动无关；改用 TUI 自带 `/plan` 进入 plan mode、fake provider 回 `exit_plan_mode` 后 gate 正常出现并停住，
未追加任何产品改动。

## Implementation Status

- 已完成（全部步骤）：
  - 计划落盘 `docs/plan/MODEL_PICKER_SORT_ORDER_PLAN.md`（gitignored）。
  - `pkg/turn/models.go`：`ModelChoices` 返回前 `sort.SliceStable` + 新增 `modelChoiceLess`（provider → ModelName
    → Model → BaseURL → APIPath，全 `ToLower`）；两处 doc 注释改写（本文件 + `pkg/gateway/models_api.go`）。
  - 测试：新增 `TestModelChoicesSortByProviderThenModelNameThenEndpoint`、
    `TestModelChoicesSortKeepsTheDefaultMarked`、`TestModelChoicesDoNotReorderTheStoredView`（`pkg/turn/models_test.go`）、
    `TestPlanReviewModelOptionsFollowTheDisplayOrder`（`pkg/turn/approval_test.go`）；修订
    `TestModelChoicesAreTheInstallWideModels`（顺序 + 存库视图断言）、`TestWebModelPickerSwitchesTheModel`
    （按 current 行定位、按 label 取非 current 行）、`TestChatSessionModelEndpoints` /
    `TestChatSessionModelPutWithoutAnEffortPinsTheRowsConfiguredEffort`（按 `option.Model` 取行）、
    `pkg/tui/TestAvailableModelOptionsUseConfiguredLLMProvidersOnly`（顺序 + 标记位置）。
  - 验证：`go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` 全 PASS；
    `go vet -tags fts5 ./pkg/turn ./pkg/gateway` 无输出；`gofmt -l pkg/turn pkg/gateway pkg/tui` 空。
  - 真机三面验收见上表；tmux 会话已清理；改动留在工作区，未 commit。
- 偏差：
  - `pkg/tui/chat_session_test.go` 不在原 in-scope 清单内，但其断言写死了旧顺序，是本契约变更的直接后果
    （done criterion 3 要求 `./pkg/tui` 绿），按同一理由修订；已与 peer e9 确认归属，未碰其 `setup.go/setup_test.go`。
- 未完成：无。

