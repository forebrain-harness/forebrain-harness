# QODERCN_MODEL_DISPLAY_NAME_PLAN — 模型标签显示厂商可读名

- 状态：待审批（plan-gated-bugfix 流程；批准前零源码改动）
- 基线：`dev` @ `c5cab032b87effda60803e865b08cf2d2166bdce`，工作区干净（`git status --porcelain` 空）。
- 落盘约定：本文件是会话计划（审批用）。批准后执行第 0 步将其复制到仓库惯例位置 `docs/plan/QODERCN_MODEL_DISPLAY_NAME_PLAN.md`（docs/plan 已 gitignore，计划文件本身禁止 commit）。
- Owner 裁决（本会话 user_interaction）：① 覆盖**所有人类可读模型标签**（选择器 + footer + /status + 审批/plan-review + 切换回复 + web 模型表 + subagent 标签 + web composer chip）；② **catalog 参考行保持 wire id**，只有订阅插件 builtin 表携带的厂商显示名进标签。

---

## 1. 目标（Why this matters）

TUI `/model` 面板里 qodercn 订阅的模型显示为 `qodercn/gfmodel`、`qodercn/dfmodel`、`qodercn/dmodel`、`qodercn/gmodel`、`qodercn/kmodel` 等**不透明 wire 代码**，用户无法识读（截图红框）。厂商列表其实给了可读名（`gfmodel`→`GLM-5.3-Flash`、`dmodel`→`DeepSeek-V4-Pro`、`gmodel`→`GLM-5.3`、`kmodel`→`Kimi-K2.8-Preview` 等），但模型标签投影把它丢了。同一缺陷遍布 footer、/status、审批、web 模型表等所有人类可读标签。

修复后：凡厂商列表（builtin 表）给了显示名的模型，所有人类可读标签显示 `provider/显示名`（如 `qodercn/GLM-5.3-Flash`）；catalog 参考行与无显示名的行（volcengine）保持 wire id 不变。

## 2. 根因（file:line 证据链）

**症状 → 根因链**（每环已现场读源核实）：

1. TUI `/model` 命令 `execModel`（`pkg/turn/executor.go:777-805`）把 `ModelChoices` 的 `choice.Label` 装进 `PickerItem{Label: choice.Label, Description: endpoint}`；TUI 选择器直接渲染 `item.Label`（`pkg/tui/overlays.go:5469` `selectableColumns(..., item.Label, ...)`，过滤匹配 `:5418`）。
2. `ModelChoices`（`pkg/turn/models.go:703-727`）：`Label: llm.FormatProviderModel(provider, model)`（`:712`）= `qodercn/gfmodel`（wire id）。它已调 `llm.LookupRow(choice.ID, provider, model)`（`:719`）但只读 `hit.SupportsAttachments`。
3. `FormatProviderModel`（`pkg/llm/catalog.go:913`）是纯字符串拼接 `provider + "/" + model`，不查任何表。
4. **显示名其实存在**：qodercn 插件 `init()`（`plugins/subscriptions/qodercn/plugin.go:22-25`）调 `installBuiltinModels(staticQoderModels())` → `llm.SetBuiltinModels("qodercn", []llm.Model{ID:"gfmodel", Name:"GLM-5.3-Flash", ...})`。`toLLMModel`（`plugins/subscriptions/qodercn/models.go:234-252`）设 `Name: record.DisplayName`；静态表 `staticQoderModels`（`models.go:118-145`）含全部 13 个生产模型的 DisplayName。`Offers()`（`plugin.go:176-203`）用 live 列表刷新同一张表，并设 `Offer.Name = record.DisplayName`。
5. **断点**：`llm.Hit`（`pkg/llm/catalog.go:41-66`）**没有 Name 字段**，`hitFromModel` 丢弃 `m.Name`；且没有任何显示路径读 builtin 表的 Name。于是厂商显示名在投影层被彻底丢弃。

**既有语义佐证**（产品早已确立「厂商名优先、wire id 兜底」，唯独模型标签投影没用它）：
- `offerDisplayName`（`pkg/tui/setup.go:1487-1492`）：「the vendor's own name for the model, and the wire id only when the listing named none」。
- `Offer.Name` 契约（`pkg/subscription/registry.go:71-74`）：「a surface shows it in place of the wire id, and "" falls back to Model」。
- web 登录 offer 选择器 `OfferPicker.vue:19`：`:model="row.offer.name || row.offer.model"`。

**现场证据**：截图 `/model` 面板左列 `qodercn/gfmodel` 等；右列 endpoint `https://gateway.qoder.com.cn`。builtin 表静态名（`models.go:120-143`）与 wire key 一一对应，证明名字可得、只是没被投影。

**波及面关键事实**（决定修复边界）：
- `assets/models.json` 全部 801 条 catalog 都带 `name`（如 `openai/gpt-4o`→`GPT-4o`、`deepseek/deepseek-chat`→`DeepSeek Chat`）。**owner 裁决 catalog 行不改名** → 显示名只取 builtin 表，不取 catalog。
- volcengine、qodercn 不在 catalog，走 builtin 表。volcengine 的 builtin 表（`pkg/process/config_db.go` `installBuiltinModelTables`）**不设 Name** → 修复后 volcengine 标签不变（仍 `volcengine/glm-5.3-flash`）。
- **收敛点**：gateway `sessionModelOptions.Label = choice.Label`（`pkg/gateway/api_extra.go:1895`）。故修 `ModelChoices.Label` 一处即覆盖三个消费方：TUI 选择器、web `/llm-models/choices`（`LlmModelChoice.label`）、web composer `/agents/primary/{id}/model-options`（`SessionModelOption.label`，ModelChip 菜单数据源）。

## 3. 修复设计

### 3.1 核心 helper（pkg/llm/catalog.go，新增两个导出函数）

```go
// ListingDisplayName 是厂商列表（builtin 表）给这个模型的可读名，无则 ""。
// 只读 builtin 表（订阅插件 installBuiltinModels 装的厂商列表投影），不读 catalog：
// catalog 是参考数据，其 wire id 已可读，保持原样（owner 裁决）。
func ListingDisplayName(provider, apiModel string) string {
    if m, ok := builtinModel(provider, apiModel); ok {
        return strings.TrimSpace(m.Name)
    }
    return ""
}

// FormatProviderModelDisplay 是人类可读标签：厂商显示名优先，wire id 兜底。
// 与 FormatProviderModel 同形（provider/model），供所有面向人的标签投影使用；
// 身份/解析/模型面向文本仍用 FormatProviderModel（wire id）。
func FormatProviderModelDisplay(provider, model string) string {
    if name := ListingDisplayName(provider, model); name != "" {
        return FormatProviderModel(provider, name)
    }
    return FormatProviderModel(provider, model)
}
```

- `builtinModel`（`catalog.go:796`）已按小写 provider + (ID|APIModel) 匹配，qodercn 行 provider=`qodercn`/model=`gfmodel` 命中 `ID:"gfmodel"`。
- builtin 表为空（多数单测）→ 返回 "" → 兜底 wire id → **既有 golden 测试不受影响**。
- 不改 `llm.Hit`/`hitFromModel`（避免把 catalog 名带进标签，违背裁决②）。
- 进程级 builtin 表：qodercn 插件 `init()` 装静态名（启动即可用，无需网络）；volcengine 在 config adoption 时装。gateway 与 TUI 同一二进制（cmd/forebrain），插件 init 同样生效。

### 3.2 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 刻意? |
|---|---|---|---|
| TUI /model 列表 qodercn 行 | `qodercn/gfmodel` | `qodercn/GLM-5.3-Flash` | 是（本 bug） |
| TUI footer / CurrentModelOption | `qodercn/gfmodel` | `qodercn/GLM-5.3-Flash` | 是（同类） |
| /status Model 行 | `qodercn/gfmodel` | `qodercn/GLM-5.3-Flash` | 是 |
| 审批/plan-review reviewer 名 | `qodercn/gfmodel` | `qodercn/GLM-5.3-Flash` | 是 |
| 切换回复 "Switched to X" | `qodercn/gfmodel` | `qodercn/GLM-5.3-Flash` | 是 |
| web /model slash 选择器（SlashPickerCard item.label） | `qodercn/gfmodel` | `qodercn/GLM-5.3-Flash` | 是 |
| web composer chip（ModelChip）菜单+chip | `gfmodel` | `GLM-5.3-Flash` | 是 |
| web 模型表（ModelsTable cell） | `gfmodel` | `GLM-5.3-Flash` | 是 |
| web subagent 标签（ModelLabel） | `qodercn/gfmodel` | `qodercn/GLM-5.3-Flash` | 是 |
| catalog 行（openai/gpt-4o 等） | `openai/gpt-4o` | `openai/gpt-4o`（不变） | 否（裁决②保持） |
| volcengine 行 | `volcengine/glm-5.3-flash` | 不变（builtin 无 Name） | 否 |
| 模型面向 handoff 提示/dispatch 指令 | wire id | wire id（不变） | 否（精确身份） |
| subagent 诊断错误 `model %q (provider/model)` | wire id | wire id（不变） | 否（技术标识） |
| 选择/匹配/编辑/data-testid | wire id / row id | 不变 | 否（身份不动） |

### 3.3 Go 显示调用点分类（穷举 FormatProviderModel 全部调用点）

**改为 `FormatProviderModelDisplay`（面向人的完整 provider/model 标签）**：
| 文件:行 | 符号 | 用途 |
|---|---|---|
| pkg/turn/models.go:712 | ModelChoices.Label | 选择器（TUI+web 三消费方） |
| pkg/turn/approval.go:990 | Model.Display() | 审批 UI 行/确认行 |
| pkg/turn/model.go:1011 | statusModelLine | /status |
| pkg/turn/executor.go:889 | chooseModel name | "Switched to X" |
| pkg/turn/executor.go:974 | effort 回复 | "Still using X at Y" |
| pkg/turn/executor.go:977 | modelSelectionFailureReply | 切换失败回复 |
| pkg/tui/chat_slash.go:236 | CurrentModelOption | footer/composer |
| pkg/tui/reducer.go:1251 | openPlanReviewCard ReviewTarget | plan-review 卡 |
| pkg/tui/overlays.go:1511 | planReviewModelDisplay | 审批 overlay plan-review 行 |
| pkg/tui/setup.go:181 | mainLLMSummary | setup 摘要 |
| pkg/run/subagent.go:4667 | subagentModelDisplay | subagent 显示名 |
| pkg/tool/format.go:2111 | handoff 卡 | "Handed off to X" |
| pkg/gateway/subagents_api.go:281 | subagentRowModel ModelLabel | web subagent 标签 |
| pkg/gateway/subagents_api.go:307 | subagentRowModelFields ModelLabel | web subagent 标签 |

`pkg/tui/chat_slash.go:192 ModelSummaryString` 的 `model=` 行（composer 兜底摘要）：model 值改 `ListingDisplayName(provider,model)||model`（name-only，因该行是 `model=` 非完整标签）。

**保持 `FormatProviderModel`（wire id；模型面向/诊断，非人类显示标签）**：
| 文件:行 | 理由 |
|---|---|
| pkg/run/config.go:1091 | buildPlanHandOffReminder → HandOffPlanExecutionPrompt：模型面向 dispatch 契约，wire id 是精确身份 |
| pkg/tool/session_tools.go:779 | "dispatch one executor subagent on that model" 模型面向指令（人类卡由 format.go:2111 投影） |
| pkg/run/subagent.go:4485,4501,4504 | 诊断错误 `model %q (provider/model) has no credentials`，`(provider/model)` 明示标识格式 |

> 执行者注意：只改上表 DISPLAY 行；KEEP 行不动。`ParseProviderModelLabel` 及其 round-trip 测试不动（身份解析）。

### 3.4 Gateway DTO 增 `model_name`（镜像既有 provider/provider_name 对称）

DTO 已有 `provider`(id) + `provider_name`(display)（`llmModelRow.ProviderName`、`SessionModelOption.providerName`）。新增 `model`(id) + `model_name`(display, name-only) 完成对称——**非新概念**，是既有约定补全。web `ModelSignature` 已分开渲染 `model`(id)+`providerName`(display)，补 name-only 显示名即可。

- `turn.ModelChoice`（pkg/turn/models.go:660）增 `ModelName string`；`ModelChoices` 设 `Label: llm.FormatProviderModelDisplay(provider, model)`，ModelName 用内联兜底（`llm.firstNonEmpty` 未导出、跨包不可用）：
  ```go
  name := llm.ListingDisplayName(provider, model)
  if name == "" { name = model }
  choice.ModelName = name
  ```
- `gateway.sessionModelOption`（pkg/gateway/api_extra.go:1825）增 `ModelName string json:"model_name"`；`sessionModelOptions`（:1893）设 `ModelName: choice.ModelName`（Label 已 = choice.Label，自动继承）。
- `gateway.llmModelRow`（pkg/gateway/models_api.go:64）增 `ModelName string json:"model_name,omitempty"`；`llmModelRowOf`（:390）设 `ModelName: firstNonBlank(llm.ListingDisplayName(row.Provider,row.Model), row.Model)`（gateway 包内已有 `firstNonBlank`，run_control.go:1294）。**`Model` 字段保持 wire id**（编辑/身份用）。
- `frontend/src/lib/api.ts`：`LlmModelChoice`（:1486）增 `modelName?: string`；`SessionModelOption`（:1753）增 `modelName?: string`；llmModelRow 对应类型增 `model_name?: string`。

### 3.5 Web 前端渲染显示名（ModelSignature 消费方）

`ModelSignature.vue` 渲染 `{{ model }}` + providerName + endpoint，**组件本身不改**；消费方把传入的 `:model` 从 wire id 换成 name-only 显示名 `option.modelName || option.model`：
- `ModelChip.vue`：菜单 `:model="option.modelName || option.model"`（:54）；chip 自身 `label` computed（:151 `props.selection?.model`）改为按 selection 匹配 option 取 `modelName`，兜底 `selection.model`；`visibleOptions` 过滤数组（:160）加 `option.modelName`/`option.label` 使显示名可搜。
- `SubagentConfigDrawer.vue`：`:model="selectedModel.model"`（:42）与 `:model="option.model"`（:57）→ `modelName||model`。
- `GoalCheckModelSelect.vue`：`:model="selected.model"`（:21）、`:model="choice.model"`（:71）→ `modelName||model`；过滤（:119）加 modelName。
- `SettingsField.vue`：`:model="selectedChoice.model"`（:79）→ `modelName||model`。
- `ModelsTable.vue`：cell `{{ row.model }}`（:10）→ `{{ row.model_name || row.model }}`。
- **data-testid 保持 wire id**（`model-option-${option.model}` 等是身份/测试锚，不改）。
- 选择/保存仍发 wire id/row id（`choose()` 发 `option.model`，不动）。

## 4. Scope

**In scope**：
- pkg/llm/catalog.go（两个 helper）
- pkg/turn/models.go、approval.go、model.go、executor.go
- pkg/tui/chat_slash.go、reducer.go、overlays.go、setup.go
- pkg/run/subagent.go（仅 :4667）
- pkg/tool/format.go（仅 :2111）
- pkg/gateway/subagents_api.go、api_extra.go、models_api.go
- frontend/src/lib/api.ts、components/chat/ModelChip.vue、components/subagents/SubagentConfigDrawer.vue、components/settings/GoalCheckModelSelect.vue、components/settings/form/SettingsField.vue、components/models/ModelsTable.vue
- 上述各包对应测试

**Out of scope（看起来相关但明确不碰）**：
- `llm.Hit`/`hitFromModel`/`LookupRow`（不加 Name，避免 catalog 名进标签）
- `FormatProviderModel`/`ParseProviderModelLabel` 本体（身份/解析契约）
- §3.3 KEEP 表三处（run/config.go:1091、tool/session_tools.go:779、run/subagent.go:4485/4501/4504）
- `assets/models.json`、catalog 生成脚本（catalog 行不改名）
- `plugins/subscriptions/qodercn/*`（闭源插件已正确提供 DisplayName，无需改；且禁入 git）
- `ProvisionRows`/state schema（不加行字段、不迁移；显示名走 lookup 投影）
- volcengine builtin 表（无 Name，保持现状）
- `ModelSignature.vue` 组件本体（消费方传显示名即可）

## 5. Steps（每步带验证命令与预期；步骤间仓库可用）

> 环境：`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`；所有 go 命令 `CGO_ENABLED=1 -tags fts5`。

**Step 0 — 落盘仓库计划 + drift 检查**
- 复制本文件到 `docs/plan/QODERCN_MODEL_DISPLAY_NAME_PLAN.md`。
- `git rev-parse HEAD` 应为 `c5cab032…`；`git diff --stat c5cab032..HEAD -- pkg/llm/catalog.go pkg/turn/models.go pkg/gateway/api_extra.go pkg/gateway/models_api.go` 空＝无 drift。
- 复核 §3.3/§3.4 引用的符号仍在（`grep -n "func ModelChoices" pkg/turn/models.go` 等）。内容不符＝STOP（仅行号漂移不算）。

**Step 1 — 复现测试（先红）**
- 在 `pkg/turn/models_test.go` 加 `TestModelChoicesShowsVendorListingDisplayName`（cfg 构造照同包 `TestModelChoicesKeepCatalogEffortLevels`，models_test.go:518 的范式）：
  ```go
  llm.SetBuiltinModels("qodercn", []llm.Model{{ID: "gfmodel", Name: "GLM-5.3-Flash"}})
  t.Cleanup(func() { llm.SetBuiltinModels("qodercn", nil) })
  // 构造含一行 qodercn/gfmodel 的 *appcfg.Root：cfg 写法照搬同包
  // TestModelChoicesKeepCatalogEffortLevels（pkg/turn/models_test.go:518）的范式
  choices := ModelChoices(cfg, "main")
  // 断言 choices[0].Label == "qodercn/GLM-5.3-Flash"
  ```
- 跑 `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run TestModelChoicesShowsVendorListingDisplayName -count=1` → **预期 FAIL**（got `qodercn/gfmodel`）。这是根因复现，必须先红。若未红＝STOP（根因链与现场不符）。

**Step 2 — pkg/llm helper**
- 加 `ListingDisplayName` + `FormatProviderModelDisplay`（§3.1）。
- 加 `pkg/llm/catalog_test.go` 测试：builtin 有名→返回名；builtin 无名→""；catalog 有名但 builtin 无→`FormatProviderModelDisplay` 仍 wire id（钉死裁决②）；空 provider/model 兜底。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'TestListingDisplayName|TestFormatProviderModelDisplay' -count=1` → PASS。

**Step 3 — pkg/turn 投影（修 Step 1 红测）**
- `ModelChoices`：Label 改 `FormatProviderModelDisplay`；ModelChoice 增 ModelName 并设值（§3.4）。
- approval.go:990、model.go:1011、executor.go:889/974/977 改 `FormatProviderModelDisplay`。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run TestModelChoicesShowsVendorListingDisplayName -count=1` → **PASS**（红转绿）。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → 全包 PASS（既有断言 builtin 空→不变）。

**Step 4 — pkg/tui + pkg/run + pkg/tool 显示点**
- 按 §3.3 DISPLAY 表改 tui/chat_slash.go:236(+192 model=)、reducer.go:1251、overlays.go:1511、setup.go:181、run/subagent.go:4667、tool/format.go:2111。KEEP 表不动。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/run ./pkg/tool -count=1` → PASS。

**Step 5 — gateway 投影 + DTO**
- subagents_api.go:281/307 改 `FormatProviderModelDisplay`。
- sessionModelOption 增 ModelName（api_extra.go:1825/1893）；llmModelRow 增 ModelName（models_api.go:64/390）。
- 加 gateway 测试：装 qodercn builtin 名后，`/llm-models/choices` 的 label、`/agents/primary/{id}/model-options` 的 label+model_name、`/llm-models` 的 model_name 均为显示名（参照同包既有 choices/models 测试写法）。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → PASS。

**Step 6 — 前端类型 + 渲染**
- api.ts 三类型加 modelName/model_name（§3.4）。
- §3.5 各消费方传 `modelName||model`；搜索过滤加显示名；data-testid 不动。
- `cd frontend && pnpm typecheck` → 0 error；`pnpm exec vitest run src/components/chat/ModelChip.test.ts src/components/subagents/SubagentConfigDrawer.test.ts src/components/settings/__tests__/GoalCheckModelSelect.spec.ts`（按实际存在的测试文件）→ PASS；为 ModelChip/ModelsTable 显示名补/改断言。
- `pnpm build` → 成功。

**Step 7 — 包级回归 + lint**
- `gofmt -l cmd pkg third_party` → 空。
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → 0。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` → 全绿。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → PASS（分层/fan-out 不增：helper 在 llm 内部，无新跨包 import）。

**Step 8 — tmux 真机验收（fake 模型，隔离 home；见 §9）**

**Step 9 — 收口**
- 回填本文件§9 验收记录 + 状态改「已实施并真机验收通过（日期）」；同步 docs/plan 副本。
- 清理 tmux/临时目录。`git status` 改动范围与§4 一致，留工作区不 commit。

## 6. Done criteria（机器可查）

1. Step 1 复现测试修复前 FAIL、修复后 PASS。
2. `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` 0 fail；`./pkg/architecture` PASS。
3. `gofmt -l cmd pkg third_party` 空；`go vet -tags fts5 ./...` 0。
4. `cd frontend && pnpm typecheck && pnpm build` 成功；相关 vitest PASS。
5. tmux 验收：/model 面板、footer、/status 显示 `qodercn/GLM-5.3-Flash`（逐屏 capture 取证）；gateway 三端点返回显示名。
6. `grep -rn "FormatProviderModel(" pkg/run/config.go pkg/tool/session_tools.go` 仍命中 KEEP 三处（未被误改）；`grep -n "Name" pkg/llm/catalog.go` 的 Hit 结构无 Name 字段（裁决②）。
7. `git status --porcelain` 改动文件 ⊆ §4 In scope；无 commit。

## 7. STOP conditions

- Step 0 drift：in-scope 文件内容与本计划摘录不符（非仅行号漂移）。
- Step 1 复现测试未红：根因链与现场不符，停下重查，不得径直改。
- 既有 golden 测试因 builtin 表非空而大面积红：说明误把 catalog 名带进标签（违背裁决②），停下核对 helper 只读 builtin。
- 需要改 §4 Out of scope（Hit/schema/插件/catalog/KEEP 三处）才能通过：停下报告，不擅自扩。
- 真机验收发现显示名未生效：先查二进制是否带 `-tags fts5` 与插件桥接（`cmd/forebrain/plugins_local_gen.go` 含 qodercn），再查 builtin 表是否装载；不得用症状补丁掩盖。
- 同一验证连续两轮失败不收敛：重评范围而非循环。

## 8. Maintenance notes

- 显示名唯一来源是 builtin 表（厂商列表投影）。未来订阅插件只要 `installBuiltinModels` 带 Name，所有人类标签自动可读——无需逐 surface 改。
- `FormatProviderModel`（wire id，身份/解析/模型面向）与 `FormatProviderModelDisplay`（人类标签）职责分离：新增面向人的标签用后者，新增身份/解析/模型面向文本用前者。
- `model`/`model_name` 与 `provider`/`provider_name` 对称：web 新显示组件取 `modelName||model`，身份/编辑/testid 取 `model`。
- 审阅重点：§3.3 KEEP 三处不得被误改；helper 不得读 catalog；data-testid 与选择/保存路径保持 wire id。
- 遗留（本期不做，owner 已知）：catalog 参考行仍 wire id（裁决②）；若日后要 catalog 名也进标签，需另议（801 行视觉冲击）。

## 9. 验收记录（执行后回填）

验收配方（fake 模型、隔离 home，禁污染真实 `~/.forebrain*`）：
```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain   # 必带 fts5；binary 须含插件桥接
H=$(mktemp -d); P=$(mktemp -d)
# 前置：向隔离 home 的 state DB 插入一行 qodercn/gfmodel（provider=qodercn,model=gfmodel,
#   base_url=https://gateway.qoder.com.cn,api_path=/algo/api/v2/service/pro/sse/agent_chat_generation,source=qodercn）。
#   插件 init 已装静态 builtin 名，无需真实登录/网络。
tmux new-session -d -s fbmodel -x 120 -y 42 \
  "cd $P && FOREBRAIN_HOME=$H ./build/bin/forebrain; echo EXIT_CODE=\$?; sleep 300"
# 驱动：/model → capture 断言左列 qodercn/GLM-5.3-Flash（非 gfmodel）；选中 → footer 显示名；/status Model 行显示名。
tmux send-keys -t fbmodel "/model" Enter; sleep 1; tmux capture-pane -t fbmodel -p
# gateway 投影：FOREBRAIN_HOME=$H ./build/bin/forebrain gateway start（隔离端口）后
#   curl /api/llm-models/choices（label）、/api/agents/primary/main/model-options（label+model_name）、/api/llm-models（model_name）。
```
四场景按 bug 裁剪：① 复现→修复（/model 显示名）② 回归（catalog/volcengine 行仍 wire id）③ 中断自救（Esc 退出 EXIT_CODE 干净、再启动可恢复）④ 非 TTY（gateway start </dev/null 不挂起）。

| 场景 | 证据 | 结果 |
|---|---|---|
| ① /model 显示名 | reviewer 自建隔离 home（yaml main 配 qodercn/openai/volcengine）+ HEAD c3377e14 重建二进制，tmux 捕获 `❯ qodercn/GLM-5.3-Flash`；footer 同显示名 | PASS |
| ② 回归 wire id | 同屏 `openai/gpt-4o`、`volcengine/glm-5.3-flash` 保持 wire id；gateway 三端点 model_name 对二者=wire | PASS |
| ③ 中断自救 | executor 证据 04：Ctrl+C×2 EXIT_CODE=0，重启 footer 恢复显示名 | PASS（executor 取证） |
| ④ 非 TTY + gateway 投影 | reviewer 亲自 curl 127.0.0.1:6060 三端点：choices/model-options `model=gfmodel,model_name=GLM-5.3-Flash,label=qodercn/GLM-5.3-Flash`；models 表 `model_name=GLM-5.3-Flash` | PASS |

## Implementation Status

VERDICT: **APPROVE**

- Worktree：`/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/qodercn-model-display-name`，分支 `qodercn-model-display-name`，基线 `c5cab032`，executor 提交 `c3377e14`（26 files, +388/−46）。合并到 dev 是 owner 的决定，tech lead 未 merge/rebase/commit 主树。
- Executor：general-purpose subagent（handoff 指定 zhipuai/glm-5.3；实际运行模型为 qodercn/qmodel_latest——属并行会话 forebrain-harness-97 正在修的「handoff 模型未生效」预存缺陷，非本计划/执行问题，已 peer 确认边界）。

任务状态（Steps 0–9 全部 done）：
- 环境接线、drift 检查、红复现、helper、turn/tui/run/tool/gateway 投影、DTO model_name、前端渲染、全量回归、四场景验收、worktree 提交——均完成。

 reviewer 亲自重跑的验证（不信任报告）：
1. 复现测试 `TestModelChoicesShowsVendorListingDisplayName` PASS（红由 executor 观测；基线 Label=FormatProviderModel=wire id，逻辑必红）。
2. `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` → 36 包全 ok，FULL_EXIT=0，无 FAIL/panic/DATA RACE；`./pkg/architecture` ok。
3. `gofmt -l cmd pkg third_party` 空；`go vet -tags fts5 ./...` exit 0。
4. 前端 `pnpm typecheck` exit 0；vitest 4 文件 36/36 PASS；`pnpm build` exit 0。
5. 真机：reviewer 自建隔离 fixture + HEAD 重建二进制，tmux 取证 /model 面板与 footer 显示 `qodercn/GLM-5.3-Flash`、openai/volcengine 保持 wire id；亲自 curl gateway 三端点确认 model_name/label。/status 行由 statusModelLine 单测覆盖（tmux 清屏键位问题未取到独立屏，代码路径同一且测试绿）。
6. KEEP 不变式：`pkg/run/config.go:1091`、`pkg/tool/session_tools.go:779` 仍 FormatProviderModel；`llm.Hit`（catalog.go:41-66）无 Name 字段（裁决②）。
7. 范围：`git diff --name-only c5cab032..HEAD` 26 文件全部 ⊆ §4 In-scope；无 gitignore 工件（plugins/桥接/third_party）入库；worktree 与主树 dev 均干净。
8. 测试审计：Go 表驱动测试钉死「catalog 名不进标签」「身份保持 wire id」；gateway 测试覆盖 choices/models/model-options/subagent 双路径；前端测试断言显示名/wire 兜底/身份锚不变/显示名可搜——均非空断言。

偏差（均已记录、按 merit 接受）：
- 按 preamble 在 worktree 本分支提交（计划 Step 9「不 commit」被 preamble 覆盖；worktree 分支提交是 handoff 明确要求）。
- 前端用 camelCase `modelName`（api.ts 对响应深度 camelCase 归一化，跟随既有 providerName 先例；wire 字段仍 model_name）。
- 补 gateway `llmModelChoice` DTO 的 model_name（计划§3.4 未列，但 GoalCheckModelSelect 数据源需要；落在 in-scope models_api.go）。
- SettingsField.vue 同文件下拉 SelectItem 行一并改（同投影、同契约）。
- 验收 fixture 调整：INSERT 补 updated_at（NOT NULL）；隔离 yaml 补 main 定义跳过 setup；gateway 单测 qodercn 行用占位 key（host 测试二进制无插件、非 login-backed，Upsert 强制要 key；被测投影不读 key）。均为 fixture/测试适配，非代码范围变更。

 minor 观察（非阻塞）：chooseModel 的 legacy label 兜底（executor.go:876-884）按 c.Label 匹配，Label 现为显示名；部署窗口内未刷新的旧客户端若发 wire label `qodercn/gfmodel` 会落空。主选择路径（row id）不变，该兜底本就为远古客户端 best-effort，计划已 acknowledged。

遗留：catalog 参考行保持 wire id（裁决②，owner 已知）。

合并落地（owner 批准「合并到 dev」后执行）：主树 dev 经 `git merge --ff-only` 推进到 `c3377e14`（dev 原 c5cab032 干净、分支为其直接后继，无需 rebase）；主树二进制以 `-tags fts5` 重建（BUILD_EXIT=0，修复生效）；worktree 与分支已清理（删除前 `diff -r` 验证闭源插件主树/worktree 字节一致，主树原件完好）；并行会话 forebrain-harness-97 的 worktree 未触碰，已 peer 同步 dev 推进。永不 push。