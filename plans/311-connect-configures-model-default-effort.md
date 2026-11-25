# Plan 311: 让 /connect 的全部分组配置模型行默认思考强度

> 执行器须逐步实施并运行每步验证；命中 STOP 条件时停止，不自行扩大范围。本文只是一份实施计划，尚未修改功能源码。
>
> **先做漂移检查**：`git diff --stat 7611d6cc..HEAD -- pkg/tui/setup.go pkg/tui/setup_test.go pkg/tui/chat_session_test.go pkg/process/setup.go pkg/process/setup_test.go pkg/turn/models.go pkg/turn/models_test.go pkg/gateway/models_api.go pkg/gateway/models_api_test.go frontend/src/components/models/ModelCapsFields.vue frontend/src/components/models/ModelFormDrawer.vue frontend/src/components/models/ModelFormDrawer.test.ts frontend/src/lib/api.ts pkg/architecture/testdata/graph.json`。同时运行 `git status --short`，检查未提交改动。若相关实现已变化，重新核对本文摘录，不能机械应用。
>
> **2026-11-17 reconcile 漂移刷新（HEAD `fcb241bc`，已实跑 drift check 并逐锚点核对）**：基线后落了 4 个提交（`1433b024` home models.json 补充＋按名匹配、`ea7ea671`/`2c0c248f` custom 行 `catalog_provider`、`fcb241bc` docs），命中本计划 8 个 in-scope 文件（setup.go +73、setup_test.go +114、models_api.go +84、models_api_test.go +104、process/setup.go +11、ModelFormDrawer.vue +78、ModelFormDrawer.test.ts +60、api.ts +22）。**核心发现全部仍成立**：`llmModelCatalogCaps` 仍不填 `ReasoningOff`、`ModelCapsFields.vue:225-228` 两条路仍丢 Off、gateway 仍拒非 off-capable 行的 none、`Options` 仍无 `CapDefaultEffort`、`customThinkingLevels` 仍六档无 Off。**两处契约级变化**：① `llmModelCatalogCaps` 签名改为 `(provider, model, catalogProvider string) (llmModelCaps, string)`（多返回 `catalogId`，内部改走 `llm.LookupCatalogFor`，支持 home models.json 补充与行级 catalog_provider）——本文所有旧调用形 `llmModelCatalogCaps(provider, model)` 一律按新签名理解，Step 5 的 off 上报/可发送过滤逻辑不受影响；② catalog-caps GET handler 除 provider/model/base_url/id 外**还读 `catalog_provider` query**（:831），本计划新增 `api_path` 参数仍是 additive。**锚点重定位**（旧→新，均已活体核对）：setup.go `pickOffers` 1356→1402、`flowPages` 549→555、API key 问 effort 1656-1672→`pickModel`:1712 调 `promptReasoningEffort`:2122、`options` 631→637、`currentProviderParamsJSON` 2134→2181、`customThinkingLevels` 1016/1040→1062、`customTestConnection` 1068→1114、`runConnect` 2224→2271；新增 `catalogLookupProvider`:1037（Custom 目录查询 provider，`asksThinkingLevels` 经它调 `turn.ModelReasoningEfforts`）。process/setup.go `Options`:14（新增 `CatalogProvider` 字段）、`Execute`:57（custom 行写 CatalogProvider）。models_api.go：`llmModelCatalogCaps`:299-317、行输出调用:465、接口调用:878、none 校验:1100-1110、caps 整体替换:1011、禁 efforts 含 none:1078-1084、GET handler:820 起。catalog.go：`Hit`:41（仍无 ReasoningOff 字段）、`reasoningEffortLevels`:104、none 并入:896-898（旧 758-760）。ModelFormDrawer.vue：`capsWrite` 省略空 defaultEffort:454（旧 417）、caps 合并:600（旧 553-558）、`loadCaps`:642（旧 592-602）。api.ts：`llmModelCatalogCaps` query 已带可选 `catalogProvider`（本计划的 `apiPath` 参数与之并列 additive）；`LlmModelRow`/`LlmModelWrite` 新增 `catalogProvider`/`catalogId`。**前置已全消（2026-11-17）**：主树三件 WIP 已按 owner 裁决提交（`bf00055a` provider picker 副标题删除 / `97a1dca4` test-db scratch 容器修复 / `f437abaa` catalog 刷新）；**312 已合并 dev@`813bdd83`**——setup.go 现含 connect 面板状态机（`ConnectPanelSelector`/`TryRunConnectPanel`、signIn 三 flow 统一交面板、`fetchOffers` 拆 `fetchOffersOnce`+`runConnectStage`、note 返回 error），本文 §3.1/§3.2 的 setup.go 锚点（signIn/flowPages/options/runConnect 等）随之大幅漂移，**开工前必须重跑 drift check 并按符号重定位全部 setup.go 锚点**；默认强度页按 §2.1 页序插入同一向导，不得破坏 312 的面板归属语义（等待/编辑/错误一律留在面板，不落 transcript）。
> **并行修复已合并（2026-11-17 核实，dev@`9b646893`）**：forebrain-harness-fb 的 vendor-wins 修复经 owner 批准以 `git merge --ff-only` 进 dev（fcb241bc→9b646893，6 文件，setup.go 零改动；worktree `.worktrees/fix-custom-effort-vendor-wins` 与分支**已删除**（owner 批准；常设规则「合并回主树后 worktree 和分支必须删掉」，2026-11-17））：`applyModelOverride`/`OverlayModelOverride` 加 provider 参数（catalog.go:862/:856），**catalog 已带离散档位时行内陈旧 `cap_efforts`（gap-filler）不再遮蔽它；行内档位仅在 catalog 无档位或 login-backed 订阅时生效**（vendor-wins 判定 :900）；`ReasoningOff`→none 并入逻辑不变（Off 三态契约不受影响），原型 UI/UX 不受影响（不碰 surface 渲染）。**本计划实施时须对齐两处（契约已在 dev 生效）**：① §2.3「已有目标行：只覆盖该行非 nil 的 CapEfforts」——对 catalog 已文档化的模型，默认强度页候选必须取 catalog 档位（vendor-wins），行内档位只在 gap-filler/login-backed 情形生效；能力页仅在 catalog 无档位时显示（`asksThinkingLevels`）恰与 gap-filler 情形重合，Custom 新声明不受影响；② Step 5.2「声明优先于静态目录，与运行时行 override 语义一致」句——该一致性在 vendor-wins 后仅对「catalog 无档位」情形成立，措辞与断言按新契约更新（该修复已同步改 turn/process/gateway 三处旧断言，开工前先复跑 C0/C11 基线确认自身环境）。§2.3 引用的 `applyModelOverride` 行号（基线 751-763）已随两次落地漂移至 :862 起，按符号重定位。

## Status

- **Status**: TODO（owner 已于 2026-11-16 确认原型，门禁解除；312 已合并 dev@`813bdd83`、主树 WIP 已提交、vendor-wins 已落地——前置全消，可派发；开工前按 drift 节重新核对 setup.go 锚点）
- **Review**: 2026-11-16 `review-plan` APPROVE（基线 `7611d6cc` 与 HEAD 一致、主树干净；C0 与 C11 基线实跑绿。评审修订已并入本文：off-capable 三处统一为优先级语义而非 OR（§2.3/§2.5 第 1、4 条）、gateway none 校验加协议可发送条件、Custom Messages 不可发送默认（minimal）同 kind 收口（§2.5 第 3、4 条）、turn 禁 import run 的本地判定（Step 2）、C11 基线留痕（§4）；原型 SHA 核对一致，无需修订原型）
- **Priority**: P1
- **Effort**: M–L（TUI 向导 + 保存契约 + Web/gateway 对齐）
- **Risk**: MED（多步骤向导、订阅的两阶段保存、旧 params 的优先级）
- **Category**: Feature / Bug Fix
- **Depends on**: 无未完成前置计划；基线已具备模型行 caps、订阅注册表、行默认强度到请求参数的投影以及会话强度快照。原型门禁已解除（§0 确认留痕）；与 312 串行，索引建议 312 先行，若 311 先行则 312 开工前必须重新漂移核对。
- **Planned at**: commit `7611d6cc`，2026-11-16，分支 `dev`
- **Landed**: `c73511c8`（2026-11-17「feat(connect): configure a model default effort through connect」，提交正文点名 plan 311）——`/connect` 的订阅／API key／Custom 三条入口统一收在默认强度页，`Options.CapDefaultEffort` 存在，`turn.SetupReasoningEfforts` 是唯一可发送判定。2026-11-24 reconcile 在 dev `cba1bfba` 复核：`grep -rn CapDefaultEffort pkg/` 命中 `pkg/tui/chat_session_test.go` 的默认强度断言；原型 `plans/311-connect-prototype.html` 仍在（本行哈希）。本文 drift 节里的锚点漂移记录是落地前的状态，不要再按它核对。
- **Scope of investigation**: 只调查 `/connect`、共用 onboarding 向导、其配置/订阅/运行时直接链路，以及 Web 模型表单的 caps/默认强度入口（§2.5、§3.4），不是全仓审计。未审计插件私有协议、真实订阅服务、安全全景、Web 其他页面或数据库三引擎实现。

## 0. 高保真可交互原型（owner 已确认，2026-11-16）

用户要求先出原型稿。原型已按参考稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html` 的终端配色、半屏面板、两列布局与键盘操作独立制作；参考原稿本身在 2026-11-17 审计中按 owner 要求修正了两处语义（`xhigh` 标签由 `Highest` 统一为 `xHigh`、`/connect` 选项结构注释同步），**修正后哈希为 `aef1aa8cac4e86fd258706d56acb4b8b8c3507bf575356ddae4106bd51ac63b0`**，`plans/311-connect-prototype.html` 本身未再改动。

- **原型文件**: `plans/311-connect-prototype.html`（单文件、零依赖、零网络；SHA-256 `7325ab5ef666d263062930c38a0a69eb89555688705f64ea8a6f2d602c66d7d7`）。直接用浏览器打开即可，或 `python3 -m http.server --directory plans` 后访问。
- **性质**: 纯前端模拟。登录、模型发现、Provision/Execute、Test connection 全部是页内定时器与内存 Map；不写真实数据库、不调用任何提供商、不保存密钥（输入框仅演示，观察器与模拟表不含 key）。切换左侧场景会重置模拟数据。
- **覆盖**: 17 个场景——三组完整流程（订阅单选/API key/Custom）、默认强度页、Review、重连旧 params 清理、无离散档位、已知能力 Custom、登录与获取失败、Provision 后 Execute 失败的部分完成提示、探测失败，以及 Off 语义三帧：`off`（Off 与 Provider default 并存）、`anthropic-off`（off-capable 但协议不可发送 → 无假 Off）、`web-align`（Web 模型表单同一语义的可操作示意：协议切换、允许关闭思考开关、默认下拉、保存载荷预览）。键盘 ↑/↓/Enter/Esc/Tab/Space、过滤、鼠标点选、120/88/68 列宽均可操作；页内 `window.connectPrototype.snapshot()` 可观察状态（不含凭据）。
- **已浏览器实测**（2026-11-16，本机 Chromium 内核 in-app browser，真实键盘/鼠标事件驱动）：订阅黄金路径单行落库与会话同步；重连回显已存默认；旧 `params.reasoning.effort` 清除且 temperature/reasoning.summary 保留；同名 GLM 5.3 跨 Coding/Agent 套餐身份隔离；API 新行默认光标 Provider default（不自动 medium）；手动模型 ID；Custom 能力多选→默认单选→替换 key 确认；无档位仅 Provider default；获取失败重试；保存失败如实提示部分完成并可重试；探测失败可返回；过滤/取消/端点修改原子取消；三种宽度均 40 行无溢出；控制台零报错、零外部请求。**Off 复测**：off 场景候选 `[Off, Low, Medium, High, xHigh, Provider default]`，选 Off 落库 `cap_default_effort="none"` 且会话快照 `none`；ChatGPT offer 带 ReasoningOff 时 Off 居首、官方默认光标不变；anthropic-off 场景模型 off-capable 但候选无 Off；Custom 能力页第七项 Off 勾选独立于档位，保存拆成 `cap_reasoning_off=true` + `cap_efforts=["medium"]` + `cap_default_effort="none"`，重连回显 Off；web-align 下拉 `[默认, 关闭思考 (none), 低, 中, 高]`，切 Messages 协议移除 none 并把已选 none 重置为默认，保存 Off 得 `default_effort:"none"`、保存默认得 `""`，取消允许关闭思考后 none 消失且非法默认重置。关键帧截图：`plans/311-shot-start.png`、`311-shot-effort.png`（含 Off 与 Provider default）、`311-shot-plans.png`、`311-shot-review.png`、`311-shot-capabilities.png`（含 Off 勾选）、`311-shot-web-align.png`（Web 示意选 Off 的保存载荷），均由实际渲染 DOM 栅格化。另有并行会话交付的 node 回归脚本 `plans/311-connect-prototype.test.cjs`（纯断言、零写入；2026-11-16 复跑 6/6 过：三宽度全场景、订阅保存、Off=none 且 Messages 无假 Off、重连清旧 override、Web 载荷同规则、Esc 不写库）与截图 `311-connect-prototype-{home,effort,off,web}.png`。
- **确认留痕与执行闸门**：owner 于 2026-11-16 明确回复「原型已确认」，确认稿即 SHA-256 `7325ab5ef666d263062930c38a0a69eb89555688705f64ea8a6f2d602c66d7d7` 的 `plans/311-connect-prototype.html`（17 场景）。执行器开工前必须 `shasum -a 256 plans/311-connect-prototype.html` 核对等于该值，不符即 STOP 报告，不得按记忆中的旧稿实施。原型中的英文文案、页面顺序、选项标签（含 Off 三态与 Web 示意语义）即实施目标；如原型与 §2 契约冲突，以 §2 为准并先修订原型、重新留痕。Status 已翻为 TODO。

## 1. Why this matters

用户要求：`Sign in with a subscription` 从订阅接口获取最新模型后只允许单选，选模型后必须选择思考强度，保存为该模型的默认值；`Use an API key` 与 `Custom` 也必须采用相同语义。现有订阅分支批量保存并直接结束；API key 分支只写 request params；Custom 只声明支持哪些档位。三者应统一保存已有的模型行 `cap_default_effort`，而不是新建配置体系。

这里的“默认”属于安装级模型行，不是 agent 的默认模型，也不是强制覆盖所有已有会话的思考强度。配置某行后，后续默认使用该行的调用获得它的默认强度；会话显式档位仍按既有快照语义工作。

## 2. 已确定的产品与数据契约

### 2.1 交互顺序

| 分组 | 正向流程 |
|---|---|
| Subscription | Provider → Sign in（已登录则跳过）→ **单选 Model** → **Default reasoning effort** → Subagents（单行 Yes/No）→ Review → Provision → 返回保存选项 → Execute |
| Use an API key | Provider → API key → 单选 Model → **Default reasoning effort** → Subagents → Review → Execute |
| Custom | Provider → Base URL → Protocol → API key → Model ID → Supported thinking levels（仅无目录数据时；保留能力多选）→ **Default reasoning effort** → 可选 Test connection → Subagents → Review → Execute |

- Subscription 的模型选择改用已有 `selectRichTabbed`。保留 Plan tabs、官方显示名、endpoint 描述和模型 wire ID；不要把模型的显示名称落到 `Model` 字段。
- 每次 `/connect` 只配置一个模型；不要删除以前配置过的其他模型。此次更改不禁止 Custom 的**能力档位多选**，也不改变其他地方的通用多选控件。
- Subscription 增加 Review，以统一最终保存边界：三个分组都在用户确认保存前不写模型配置。登录凭据本身可能已由明确同意的登录步骤保存，不能承诺“取消向导会撤销登录”。
- Review 对订阅不提供 Change endpoint，端点由 offer 决定；也不调用原有 `confirmReplace` 的“Replace its key?”确认，订阅继续采用原先 registry/pasted-key 授权流程，Review Save 只确认本次配置。API key/Custom 的既有重复行 key 替换确认保留。每组均显示模型、endpoint、`Default reasoning effort`、Subagents；Custom 额外显示 `Supported thinking levels`，能力与默认必须分列。
- 默认强度页标题为 `Default reasoning effort`，说明为 `Choose the default for <provider>/<model>.`，使用真实 model ID，不用注册表分组名替代 provider。
- 选项用 `turn.EffortEnglishLabel` 的英文名称，并保留 wire ID 映射。`Provider default` 是单独选项，映射 `""`；`Off` 映射 `"none"`，两者不是一回事：前者不传 effort 参数、由厂商决定思考与否，后者显式关闭思考。两者都必须可保存、可回显。
- **Off 三态规则（用户明确要求：有些模型不支持关闭思考）**：
  1. 模型**声明支持关闭思考**且该协议**实际能发送** `none` → 候选必含 `Off`，排在档位最前；
  2. 模型**不支持关闭思考** → 候选只有离散档位 + `Provider default`，**不得出现 Off**，也不得把 `none` 混进档位；
  3. 无可发送离散档位 → 仅 `Provider default`（off-capable 但协议发不了 none 时同样无 Off）。
  off 能力只认四个来源：订阅 offer 的 `ReasoningOff`、目标行 `CapReasoningOff`、目录 efforts 中的 `none`（`applyModelOverride` 已按 ReasoningOff 重新并入）、Custom 能力页的 Off 声明。**不得**从档位列表、协议名、provider 名或模型名推断 off 能力。
- 已知边界：本构建的 Anthropic Messages 客户端没有 thinking-disabled 的 wire 路径（§3.4 证据），`none` 不可发送 → 即使模型 off-capable 也不给 Off 选项。这是诚实边界，不是待修 bug；补齐 Anthropic 关闭思考的运行时支持是另一个计划。
- 每组总要经过默认强度页。若无可发送的离散档位，仅显示 `Provider default`，说明 `No discrete reasoning levels are available for this model.`，仍可 Enter/Esc。不猜测 low/medium/high，不把 toggle/budget 转成离散档位。
- 不增加全局 medium 默认。新行若没有有效的官方默认，默认光标停在 Provider default；这是对现有 API key “有 medium 就默认选 medium”的有意调整。

### 2.2 数据保存语义

1. 复用 `state.LLMModel.CapDefaultEffort` / `LLMModelInput.CapDefaultEffort`；**零 schema 迁移**。
2. `process.Options` 增加 `CapDefaultEffort *string`：
   - nil：调用方没有设置模型行默认，完全保留现有非交互行为；
   - 非 nil 且非空：用户明确选择该档位；
   - 非 nil 且为空：用户明确选择 Provider default，必须清掉旧行默认。
3. 三个交互分组总是返回非 nil 指针，包括 Provider default。不要用 `s.params == ""` 判断是否答过题；用独立的 `defaultEffort` 与 `effortAnswered`。
4. 此向导配置默认时，**同时从最终保存的同一行 ParamsJSON 中删除 `reasoning.effort`**。保留 temperature、其他顶层字段以及 reasoning 的其他字段。否则旧 params 会优先于新 cap，导致“选了但没生效”。复用 `appcfg.ParseLLMRequestParamsJSON`、`appcfg.WithReasoningEffort(params, "")`，不要新增 JSON 变换库。
5. 不批量迁移其他模型；不改 `ProviderFromModelRow` 的全局优先级；不改变 Web/非交互调用显式 params 的既有含义。
6. 保存时读取**该次身份实际匹配的行**，不要从 `cfg.Defaults.ParamsJSON` 借参数。匹配复用 `process.LLMModelHeld`，涵盖登录模型空 APIPath 与旧 display path 的兼容规则。
7. Subscription 的 `Provision` 保留 `[]Offer` 公共接口，但本向导永远传长度 1 的 slice；不得篡改原始 `Offer.DefaultEffort` 来冒充用户偏好。先由 Provision 保存官方能力/来源/凭据，再由既有外层 `process.Execute` 同一次 upsert 写入默认强度、清除旧 params effort、保存 Subagents。用户选择必须在 Provision 之后落地，不能被官方默认再次覆盖。
8. `PromptMainLLMSetup` 返回成功只表示选项已准备好，不是整个配置已提交。最终成功文案只能在 Execute 成功后出现。Provision 后 Execute 失败，允许保留已经 provision 的行，必须提示配置未完成；不得谎称无写入或自动删除行/凭据来模拟回滚。
9. `/connect` 成功后仍沿用 ResetResolve → SyncConfig → SelectModel 链路。将本次明确选定的 `opts.CapDefaultEffort` 传给 SelectModel，而不是硬编码 nil；这样当前会话接收本次选择的具体快照，空指针值也明确清掉旧会话 effort。不改其他会话。
10. **Off 的落库**：选 Off 时 `CapDefaultEffort="none"`，与选档位走同一条 Upsert；`none` **永远不写进 `CapEfforts`**——关闭思考是能力（`CapReasoningOff`），不是档位，沿用 `pkg/subscription/registry.go:94-103` 的拆分语义。Custom 能力页的 Off 勾选只写 `CapReasoningOff`；Subscription 向导不改 `CapReasoningOff`（该字段由 offer/Provision 拥有）。
11. **Web 保存清空默认**：`ModelFormDrawer.vue` 的 save 总是发送 `caps: capsBody()`，gateway 整体替换 caps（`models_api.go:966` 一带），因此 `capsWrite` 省略空 `defaultEffort`（`:417`）即等于清空——现状已正确，**保持并加回归测试**，不要改成显式发送空串或新增清空接口。

### 2.3 档位、默认光标与身份

**先组装能力，再做协议过滤和排序**，绝不能把 `Offer.DefaultEffort` 当能力声明来增加选项。

- 基础数据来自 `llm.LookupCatalog(provider, model)` 的 `Hit.ReasoningEfforts()`；它绕开其他已配置行的 override，且包括内建模型表和可按名称匹配的 Custom 模型。不引入新的 runtime models.dev 下载。
- 已有目标行：只覆盖该行非 nil 的 CapEfforts、CapReasoningOff；CapReasoning 明确 false 时没有档位。非 nil 空 CapEfforts 是“明确无离散档位”，不能回退目录。不要用空 row ID 的 `ModelReasoningEfforts` 间接借同 provider/model 的兄弟行。
- Subscription：本次 `sub.Offers` 返回的非 nil Efforts、ReasoningOff 覆盖对应能力（与 Provision 将写入的规则一致）；缺失字段才沿用已有目标行/目录。Efforts 中的 `none` 按现有 `splitThinkingOff` 规则独立处理，明确 ReasoningOff=false 应移除 none；保持官方 offer 原值不变。已有行的 CapReasoning=false 仍有效，因为 Provision 不清除此字段。**拆分之后组装候选时必须把 none 重新并入**：off 能力按优先级解析——本次 offer 的非 nil `ReasoningOff` > 目标行的非 nil `CapReasoningOff` > 目录 efforts 含 `none`（Custom 以本次能力页的 `offDeclared` 为最高优先）；解析结果为 true 时候选 = `["none"] + 档位`，再交给协议过滤。**显式 false 必须覆盖更弱来源，不得用 OR 把已声明不支持关闭的行重新开出 Off**（与 §2.5 Web 下拉、gateway 校验同一优先级语义）。目录侧 `applyModelOverride`（`pkg/llm/catalog.go:751-763`）是行 override 的并入点；纯目录路径的 none 直接来自 `reasoning_options`（`effortsFromOptions` 保留 none）。setup 侧不得只拿拆分后的 Efforts 当候选，否则 Off 永远消失。
- Custom：本次能力页选择优先于已有 CapEfforts；空列表也是有效声明。能力页本身仅在纯目录没有离散档位时显示，重连时用目标行能力回显，不因兄弟行碰巧有档位而跳过。**能力页增加第七个勾选项 `Off`（wire `none`）**，独立布尔 `offDeclared` 回显自目标行 `CapReasoningOff`，与六个档位勾选互不混写；保存时按 registry 语义拆分：`CapEfforts` 只含档位、`CapReasoningOff=offDeclared`。未勾选 Off 的模型后续默认页不得出现 Off。
- 最后仅保留该构建实际能发送的档位；顺序 `none, minimal, low, medium, high, xhigh, max`，未知但明确声明的 wire ID 保留在末尾；trim/lowercase、去空、去重。协议过滤遵循现有 Anthropic/OpenCode 路径，不让 Custom 的 Messages 协议出现客户端实际丢弃的档位。**Anthropic 过滤集为 low/medium/high/xhigh/max（`pkg/llm/anthropic/agent_llm.go:110`），不含 none**：off-capable 的 Anthropic 模型经此过滤后无 Off，符合 §2.1 三态规则第 3 态。

默认光标按以下顺序选择，候选非空值必须仍在最终可发送列表中：

1. 同一身份在本次向导已经明确回答的值（包括 `""`）；
2. 已有目标行的有效值：legacy `ParamsJSON.reasoning.effort` 优先于 CapDefaultEffort；若该旧值不再支持，回退 Provider default 并给出简短提示；已有行两者均空时**保持 Provider default**，不要套用新的官方默认；
3. 仅全新 Subscription 行：有效的 `Offer.DefaultEffort`；
4. Provider default。

模型/端点/协议/订阅 offer 发生变化后清除下游已答状态，再从新目标行读取。对 API key 的 Review → Change endpoint 也适用：若身份变化，重新走默认强度和 Subagents 页后才允许再次 Review 保存。对于同一行仅修改 API key，不应清掉已答默认。

Subscription 用选中 offer 的索引而不是显示名定位；索引需有 -1 哨兵。相同 model ID 在不同 Plan/endpoint 下的条目不得混选；即使两个 offer 的归一化身份相同，切换 offer 仍需重算其 live 能力和官方默认。单选模型页的默认索引依次为本次选中项、按列表顺序首个已持有项、首个 `Offer.Default` 项、0。

### 2.4 返回与失败

- Esc：Review → Subagents → Default effort → Model；Custom 的 effort 返回 Supported levels（若存在），否则返回 Model。Custom 的 Test connection Esc 返回 effort。
- 已登录 Subscription 的 Model Esc 返回 Provider，不能重新触发登录；未登录流程保留已有 Sign in 导航。
- Esc 离开 effort 不算提交答案，不能调用 Provision/Execute/SetSubagentAllowed。
- 保存前退回同一模型要保留答案；换模型不能带过去。能力列表改动导致答案无效时，重新要求选默认。
- Provision 失败给出既有脱敏错误并返回模型页，保留登录和已获取的 offers；同一模型重试保留有效答案，选另一模型清状态。`ErrPlanKeyRequired` 继续走已有 masked Secret，不记录 key。
- 对单 offer，Provision 返回 ID 数量必须恰好 1；通过 LLMModelHeld 校验其与选中身份对应的实际行一致后才返回 Options。返回 ID 不符合约定、读行失败或身份不对应时停止本次保存，不写另一个模型的偏好。

### 2.5 Web 模型表单对齐（同一 Off / Provider default 语义）

Web 的设置·模型·高级配置（`ModelFormDrawer.vue` + `ModelCapsFields.vue`）必须与 TUI 用同一条三态规则，不允许出现"TUI 能选 Off、Web 不能"或反过来的分裂：

1. **默认下拉候选** = `默认/Default`（wire `""`，已有 `chat.effortDefault`）+ 可发送候选；off-capable 且可发送时候选含 `none`，标签用已有 `effortLevelLabel('none')`（zh `关闭思考`、en `Off`），**不新增 locale key**。off-capable 判定按优先级而非 OR：`draft.caps.reasoningOff` 非 null/undefined 时以它为准（**显式 false 必须移除 Off，即使目录 off-capable**——原型 web-align 取消「允许关闭思考」后 none 消失即此语义）；否则看 `catalogCaps.reasoningOff === true`；再否则 source efforts 含 `none`。现状 `effortOptions`（`ModelCapsFields.vue:225-228`）在 `effortsKnown` 时直接返回 sourceEfforts、否则滤掉 none，两条路都丢 Off——必须修。**负规则（用户重申）：模型不支持关闭思考（off 能力按上述优先级解析为 false）时，Web 任何入口不得显示 none/Off——默认下拉、聊天选择器、保存载荷预览 alike；gateway 同时拒绝非 off-capable 行的 `default_effort="none"`（第 4 条），前端隐藏 + 后端拒绝双保险。**
2. **gateway 上报 off 能力**：`llmModelCatalogCaps`（`models_api.go:282-301`）目前不填 `ReasoningOff`。`llm.Hit` 没有 ReasoningOff 字段（`catalog.go:41-66`）；纯目录路径的 off 能力表现为 efforts 含 `none`——`reasoning_options` 本身可含 none，`effortsFromOptions` 的白名单 `reasoningEffortLevels`（`catalog.go:104-112`）保留它（`applyModelOverride` 只是行 override 的并入点，不在本函数路径上），故据此填 `ReasoningOff=&true`；efforts 保持原样上报。行列表的 `CatalogCaps`（`:424`）与 catalog-caps 接口（`:833-839`）共用该函数，一处修两处受益。
3. **可发送过滤同规则**：gateway 已 import `pkg/turn`（`models_api.go:28`）。catalog-caps 接口与行输出在给出 efforts 前，用 Step 2 的 `turn.SetupReasoningEfforts(provider, model, apiPath, levels)` 过滤；apiPath 取值：编辑态用 `stored.APIPath`，新增态用请求新增的可选 `api_path`（GET query / POST body 的 `APIPath`，前端 `loadCaps` 传 `draft.apiPath`）。这是对该接口"endpoint 只随行不读"注释（`:779-782`）的**有意小改**：只读协议用于过滤，不用于决定来源。过滤后若已存/官方默认不在候选中，表单显示为默认（`""`），不显示非法值。**无来源列出档位（`effortsKnown=false`）但已存行声明了 `CapEfforts` 时**，行输出与 catalog-caps 接口把该声明经同一函数可发送修剪后作为下拉 base 上报（行输出填 `out.CatalogCaps.Efforts`、接口填 `body.Efforts`，`EffortsKnown` 保持 false）；勾选框仍回显未修剪的存储声明，保存回写不变。否则 Custom Messages 行勾选的 `minimal` 会出现在 Web 默认下拉里而 TUI 不提供——Anthropic 客户端构造时即丢弃不可发送档位（`pkg/llm/anthropic/agent_llm.go:88-92` 的 AcceptsEffort 门），落库后静默无效，正是本计划要消灭的"选了但没生效"。
4. **gateway 校验放行 off-capable 且可发送行的 none**：`llmModelCapsValid`（`:1039-1048`）现在要求 `DefaultEffort` 出现在 levels 中，而 `caps.Efforts` 永远不许含 none（`:1018-1024`，保持），导致订阅/Custom 的 off-capable 行存 Off 被拒。改为**两个条件同时满足**才放行 none：① off-capable 按优先级解析——`caps.ReasoningOff` 非 nil 时以它为准（本次显式 false 不得被 `stored.CapReasoningOff=true` 掩盖），否则取 `stored.CapReasoningOff`，再否则目录 efforts 含 none；② 协议可发送——`llmSendableEfforts(provider, model, apiPath, []string{"none"})` 非空（apiPath 取 `write.APIPath`，空则 `stored.APIPath`；Messages 路径发不了 none，off-capable 也拒，与 §2.1 三态规则第 1 态一致）。**非 off-capable 行继续拒绝 none**（用户要求：不支持关闭思考的模型不得显式关）。非 none 档位的成员集合同样先经 `llmSendableEfforts` 修剪再 contains（拒绝文案不变）：否则 Custom Messages 行可存 `minimal` 默认而客户端静默丢弃。其余校验（六档白名单、官方档位只读、窗口/输出）不变。
5. **非法默认自动重置**：协议切换、取消允许关闭思考、能力收窄导致当前 `defaultEffort` 不在候选中时，前端重置为 `""`（与原型 web-align 行为一致），不允许把非法值留在下拉里再被 gateway 拒。
6. **不改的部分**：Web 聊天页的强度选择器已走 `ModelReasoningEffortsRow` 的可发送修剪（`pkg/turn/models.go:821` 一带），不动；`capsWrite` 省略空默认 + save 总发 caps 的清空链路不动（§2.2 第 11 条）；`pickableLevels` 六档位勾选不含 none 不动（off 是开关不是档位）。

## 3. Current state 与可复核证据

所有行号以 `7611d6cc` 为准；执行前按符号定位复核。

### 3.1 向导目前的三处差异

`pkg/tui/setup.go:1356-1449`，订阅模型页自身完成批量配置；关键原文：

```go
idxs, ok, err := multiSelectRichTabbed(s.cfg.Selector, title, items, TabbedSelectOptions{Checked: checked})
```

```go
allowed, ok, err := s.pickOfferSubagents(chosen, idxs)
```

```go
ids, err := s.provisionWithPastedKey(sub, env, chosen)
```

`pkg/tui/setup.go:496-506` 的 run 分支在 pickOffers 成功后立即返回 Options。`flowPages:549-568` 中订阅以 Offers 结束，API key 只有 Model/Subagents/Review，Custom 只有能力 Thinking 页，没有独立默认强度页。

`pkg/tui/setup.go:1656-1672`，API key 在模型函数内问 effort：

```go
params, ok, err := promptReasoningEffort(s.cfg, s.choice, model, s.effortAnswer())
```

```go
s.model, s.params = model, params
```

`pkg/tui/setup.go:631-668` 的 options：订阅只带身份和授权，Custom 多带 CapEfforts，API key 多带 `ParamsJSON: s.params, UpdateParams: true`。`currentProviderParamsJSON` 位于 2134，只按 provider 判断能否复用 cfg.Defaults，不能拿它当目标行的参数来源。`customThinkingLevels:1040` 是能力多选，不应删除；`customTestConnection:1068` 当前不传 ParamsJSON，因此新默认页放到连接测试前时还需要把默认投影进探测请求。

### 3.2 保存与运行时优先级

`pkg/process/setup.go:14-39` 的 Options 尚无 CapDefaultEffort。`Execute:119-153` 组装 LLMModelInput 后调用 Upsert；只有 UpdateParams 为 true 才写 params。

`pkg/process/llm_models.go:654` 已有：

```go
CapDefaultEffort *string
```

`pkg/process/llm_models.go:755-757`：

```go
if in.CapDefaultEffort != nil {
    row.CapDefaultEffort = strings.TrimSpace(*in.CapDefaultEffort)
}
```

`pkg/subscription/registry.go:460-461`，ProvisionRows 会写回官方默认：

```go
defaultEffort := strings.TrimSpace(offer.DefaultEffort)
input.CapDefaultEffort = &defaultEffort
```

`pkg/config/llm_models.go:344-346`，实际请求的优先级：

```go
if effort := strings.TrimSpace(row.Caps.DefaultEffort); effort != "" && ReasoningEffort(out.Params) == "" {
    out.Params = WithReasoningEffort(out.Params, effort)
}
```

因此只增 cap 指针透传不够，必须清掉本次目标行的旧 params effort。`pkg/config/agent_llm.go:153-182` 的 WithReasoningEffort 已提供深拷贝和仅删除 effort 的行为；先 Parse，解析失败不要默默抛掉其他参数。

`pkg/process/llm_models.go:663-683` 的 LLMModelHeld 在 login-backed 且输入 APIPath 为空时按 provider/model/baseURL 认已有行，保留旧 display path。`pkg/tui/setup_test.go:2112` 已有对应回归测试。

`pkg/tui/chat_slash.go:625-679` 的 SelectModel 接收 `*string`，运行时调用 `Runner.SetPrimaryModel`，然后保存 published effort；`pkg/run/config.go:2018-2034` 定义 nil=推导配置、非 nil=明确覆盖（包括空）。`runConnect` 在 `pkg/tui/setup.go:2224-2329` 保存后同步并选模型，现传 nil。

**已有边界**：SelectModel 的运行时层当前按 provider/model 解析，而 choice.ID 用于持久化。同一 pair 不同 endpoint 的运行时选择改造不在本计划；本计划必须保证默认与参数落在准确行，并把本次默认显式传给当前会话，不得以“选择了 row ID”声称顺便修复该底层问题。

### 3.3 可复用组件、测试与约定

- `pkg/tui/tabbed_picker.go:12-17` 的 selectRichTabbed 使用 TabbedRichSelector，旧 selector 回退 SelectRich。`pkg/tui/tabbed_picker_test.go:23` 是真实 picker 布局 golden 的范例。
- `pkg/turn/models.go:217-237` 有 trimToSendable（`:198` 是 ModelReasoningEffortsRow），`:250-277` 有 orderedEffortLevels，`:285` 有导出的 EffortEnglishLabel。不要另造强度名/排序表。
- `pkg/llm/catalog.go:572-595` 的 LookupCatalog 是无 row override 的纯目录入口；不要使用全局 pair override 来决定新行能力。
- `pkg/run/config.go:453-515` 的 client 选择顺序为 ChatGPT、OpenCode、注册插件 factory、普通 provider/路径；Messages path 识别见 `:569-570`。`llm.HasProviderFactory` 位于 `pkg/llm/llm.go:571`。
- `pkg/process/session_context_test.go:434-498` 的 `TestExecuteSavesSubscriptionRowAlreadyProvisioned` 是保存集成范例：独立 t.TempDir，StateDB → Upsert → Close → Execute → 重新开 DB → 按 rep.ModelID 断言。
- `pkg/tui/setup_test.go:1083` 的 `TestVolcengineSubscriptionSetupSavesThroughExecute` 已把向导和 Execute 串起来；沿用它，不只断言 Options。
- `pkg/tui/setup_test.go:346` 的 fakeSubscription.Provision **未完整写入真实 offer caps/default**。新测试须补齐 fake，使其真的把官方默认写入，才能证明 Execute 覆盖成功；保留 pasted-key fake 的凭据行为，不能简单把所有 fake 都换成会清空 key 的 ProvisionRows。
- `pkg/tui/chat_session_test.go:14375-14528` 共用 fakeSelector：SelectRich 用完脚本会 cancel，Select 用完脚本会默认选。改单选后显式补齐脚本，不修改通用 fake 来掩盖遗漏。需要按索引区分重复 label 时，在 setup_test.go 中加局部 selector。
- 工程为 Go 单 module，Go 1.26.9、CGO、fts5。新测试与生产文件同名；本计划允许创建缺失的 `pkg/process/setup_test.go`，不新增生产文件/包。
- `FOREBRAIN.md:89-115` 的静态目录决策是“不在运行时下载 models.dev”；本需求仍使用订阅自身的 Offers 在线发现模型，二者不冲突。该文档“无档位不出 picker”在 `/model` 等旧入口保持不变；此次只对 `/connect`/共用 onboarding 增加 Provider-default-only 配置页。
- `FOREBRAIN.md:137-145` 的 CGO/fts5、测试隔离 home、包图守卫需遵循。TUI 新文案全英文。不要把这次变更扩成既有文案全面清理。

### 3.4 Off 能力与 Web 入口的现状证据

**none 是能力、不是档位，且 Anthropic 发不了 none。** `pkg/llm/anthropic/agent_llm.go:110-116`：

```go
var anthropicEffortLevels = []anthropicapi.OutputConfigEffort{
	anthropicapi.OutputConfigEffortLow,
	anthropicapi.OutputConfigEffortMedium,
	anthropicapi.OutputConfigEffortHigh,
	anthropicapi.OutputConfigEffortXhigh,
	anthropicapi.OutputConfigEffortMax,
}
```

`agent_llm.go:283-285` 只在非空时设置 effort，没有 thinking-disabled 的 wire 分支：

```go
if c.effort != "" {
	params.OutputConfig.Effort = c.effort
}
```

目录侧 off 能力以 none 并入 efforts 表达，`pkg/llm/catalog.go:758-760`：

```go
if override.ReasoningOff != nil && *override.ReasoningOff {
	levels = append([]string{"none"}, levels...)
}
```

订阅注册表把 none 拆出档位，`pkg/subscription/registry.go:94` 的注释即 "turning thinking off is ReasoningOff, not a level"。TUI Custom 能力页 `customThinkingLevels`（`pkg/tui/setup.go:1016-1025`）只有六档、**没有 Off 声明**，本计划要补。

**Web 表单丢 Off 的两条路。** `frontend/src/components/models/ModelCapsFields.vue:225-228`：

```ts
const effortOptions = computed(() => {
  if (props.effortsKnown) return props.sourceEfforts
  return props.draftEfforts.filter((level) => level !== 'none')
})
```

`effortsKnown=true`（订阅/目录行）时 sourceEfforts 不含 none（注册表已拆分）；`false` 时显式滤掉 none。off 能力只存在于同文件 `:47-57` 的 `reasoningOff` 开关（`draft.reasoningOff === true`），与下拉候选完全脱节。勾选集合 `pickableLevels = EFFORT_CHOICES`（`:212-213`），而 `frontend/src/lib/effortLabel.ts:38` 的 EFFORT_CHOICES 明确不含 none——这是对的，不要改；要改的是下拉候选。默认项标签已存在：`:87` 的 `t('chat.effortDefault')`，none 标签走 `effortLevelLabel`（`effortLabel.ts:16-30`，zh `关闭思考`）。

**gateway 不上报 off、且拒存 off-capable 行的 none。** `pkg/gateway/models_api.go:299-317` 的 `llmModelCatalogCaps(provider, model, catalogProvider string) (llmModelCaps, string)`（2026-11-17 reconcile 核实的新签名；基线稿为 :282-301 两参单返回）只填 Images/Reasoning/Efforts/ContextWindow/MaxOutput，不填 `ReasoningOff`（结构体 `:56` 已有该字段）。校验 `:1100-1110`（基线稿 :1039-1048）：

```go
if effort := strings.TrimSpace(caps.DefaultEffort); effort != "" {
	levels := caps.Efforts
	if len(levels) == 0 {
		catalogCaps, _ := llmModelCatalogCaps(provider, model, stored.CatalogProvider)
		levels = catalogCaps.Efforts
	}
	if !slices.Contains(levels, effort) {
		return llmRefuse(r, http.StatusBadRequest,
			"默认思考强度必须是这个模型支持的档位", "the default level must be one this model offers")
	}
}
```

（以上为 2026-11-17 reconcile 在 `fcb241bc` 核实的活体原文，models_api.go:1100-1110；基线稿的旧调用形 `llmModelCatalogCaps(provider, model).Efforts` 已随 catalog_provider 落地改为新签名。）

而 `:1078-1084`（基线稿 :1018-1024）禁止 `caps.Efforts` 含 none（`llmEffortLevels:45` 六档），所以 none 只能靠目录 efforts 含 none 才碰巧通过；订阅/Custom 行（off 在 CapReasoningOff）存 Off 必被拒。清空默认的现状链路：`ModelFormDrawer.vue:454`（基线稿 :417）`if (caps.defaultEffort) out.defaultEffort = caps.defaultEffort` 省略空值，但 save 总发 caps、gateway `:1011`（基线稿 :966-968）`if write.Caps != nil { caps = *write.Caps }` 整体替换 → 省略即清空，**现状正确**。行回显的 caps 合并见 `ModelFormDrawer.vue:600`（基线稿 :553-558，`caps: { ...row.catalogCaps, ...row.caps }`、`sourceEfforts` 取 catalogCaps.efforts），`loadCaps:642`（基线稿 :592-602）用 catalog-caps 接口刷新；接口 handler `:820` 起（基线稿 :783-840）的 GET 读 provider/model/base_url/id，**2026-11-16 起还读 `catalog_provider` query（:831）**，live overlay 在 `process.ResolveModelInfo`（:879-884，reconcile 核实）。

## 4. Commands you will need

在仓库根执行。以下环境是本机已验证可用的工具链，不需要安装依赖。其他机器用自身已安装且满足 go.mod 的 Go 与对应 PDFium triple；缺依赖 STOP，不降级 Go 或改 go.mod。

```sh
export PATH="/Users/doudou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin:/opt/homebrew/bin:$PATH"
export GOTOOLCHAIN=local GOPROXY=off GOFLAGS=-mod=readonly CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/darwin_arm64"
export VERIFY_DIR="$(mktemp -d /tmp/forebrain-connect-311.XXXXXX)"
export FOREBRAIN_HOME="$VERIFY_DIR/home"
go version
```

期望 Go 1.26.9，所有 home 均为新建 scratch，绝不继承真实 FOREBRAIN_HOME。测试中还须使用 t.TempDir/t.Setenv；订阅用 fake，网络用 loopback httptest，不读取本机真实凭据。

| 编号 | 用途 | 命令 | 期望 |
|---|---|---|---|
| C0 | 基线回归 | 使用紧接此表的 C0 shell 代码块 | 命中测试，exit 0 |
| C1 | 新持久化契约 | `go test -tags fts5 ./pkg/process -run '^TestExecuteModelDefaultEffort' -count=1 -v -timeout 180s` | 新测试全部运行并 PASS |
| C2 | 能力映射 | `go test -tags fts5 ./pkg/turn -run '^TestSetupReasoningEfforts' -count=1 -v -timeout 180s` | 新测试全部 PASS |
| C3 | 向导特性 | `go test -tags fts5 ./pkg/tui -run '^TestConnectDefaultEffort' -count=1 -v -timeout 180s` | 单选、三分组、返回、重连、取消、错误与渲染测试全部 PASS |
| C4 | 涉及包全量 | `go test -tags fts5 ./pkg/tui ./pkg/process ./pkg/subscription ./pkg/turn ./pkg/config ./pkg/state ./pkg/gateway -count=1 -timeout 10m` | 全部 PASS，非 no-tests 假绿 |
| C5 | 类型/编译 | `go build -tags fts5 ./...` | exit 0 |
| C6 | 静态检查 | `go vet -tags fts5 ./...` | exit 0 |
| C7 | 格式 | `gofmt -l pkg/tui/setup.go pkg/tui/setup_test.go pkg/tui/chat_session_test.go pkg/process/setup.go pkg/process/setup_test.go pkg/turn/models.go pkg/turn/models_test.go pkg/gateway/models_api.go pkg/gateway/models_api_test.go` | 无输出 |
| C8 | 架构 | `scripts/package-graph.sh && go test -tags fts5 ./pkg/architecture -count=1` | PASS；graph 仅允许 loc 变化，无新增边/包 |
| C9 | diff | `git diff --check && git status --short` | 无空白错误，变更仅在白名单 |
| C10 | gateway Off 校验 | `go test -tags fts5 ./pkg/gateway -run '^TestLLMModelCapsOff' -count=1 -v -timeout 300s` | 新测试全部运行并 PASS |
| C11 | Web 类型与组件测试 | 在 `frontend/` 下：`npm run typecheck && npx vitest run src/components/models/ModelFormDrawer.test.ts src/lib/effortLabel.test.ts` | typecheck exit 0；两个测试文件 PASS（含新增 Off 用例） |

避免表格中的正则转义造成误执行，C0 的可直接执行版本如下：

```sh
go test -tags fts5 ./pkg/tui ./pkg/process -run 'TestConfigureSubscription|TestCustomModel|TestConfigureEffortPage|TestPromptMainLLMSetup|TestVolcengineSubscriptionSetupSavesThroughExecute|TestExecuteSavesSubscriptionRowAlreadyProvisioned|TestExecuteSecondModelOfAProviderKeepsTheFirst' -count=1 -timeout 180s
```

**advisor 已运行的基线**：2026-11-16，在隔离 home 下运行上述相关子集（PromptMainLLMSetup 仅其中三个 effort 用例），TUI 1.525s、process 0.620s PASS；同时编译 subscription/config，但该过滤器在它们中匹配零测试，不能算其行为已验证。链接器有既有 libtokenizers macOS deployment-target 与 duplicate `-lm` warning，exit 0。未运行全量测试/真实终端验收，未访问真实订阅。**C10 为新测试，尚不存在；C11 评审已实跑基线（2026-11-16，基线 `7611d6cc`）**：`frontend/node_modules` 已存在（无需 install），`npm run typecheck` exit 0；`npx vitest run src/components/models/ModelFormDrawer.test.ts src/lib/effortLabel.test.ts` 2 文件 13 用例全过（ModelFormDrawer 9 + effortLabel 4）。执行器开工后仍须复跑 C11 确认自身环境；若出现与本计划无关的既有错误，记录并在 STOP 中报告，不顺手修无关文件。

不要为本计划运行 `make build` / `make test`，它们会引入前端生成或 native dependency fetch；使用上述直连 Go 命令。C8 会更新 graph，只能在实施时运行，本次 advisor 未运行。

## 5. Scope

**仅允许修改**：

- `pkg/tui/setup.go`：状态机、三分组默认页、订阅单选/最终提交、review/probe/connect。
- `pkg/tui/setup_test.go`：主要新测试与原有订阅/Custom 脚本迁移。
- `pkg/tui/chat_session_test.go`：更新既有 PromptMainLLMSetup 参数断言；只在这里确有相关共用 fixture/接线测试时修改，不做无关清理。
- `pkg/process/setup.go`：Options 与 Execute 的默认值/参数合并。
- `pkg/process/setup_test.go`（创建）：新的同名保存契约测试。
- `pkg/turn/models.go`、`pkg/turn/models_test.go`：窄的纯档位过滤/排序入口及对应测试。
- `pkg/gateway/models_api.go`、`pkg/gateway/models_api_test.go`：§2.5 的 off 能力上报、可发送过滤、none 校验放行与对应测试。
- `frontend/src/components/models/ModelCapsFields.vue`、`ModelFormDrawer.vue`、`ModelFormDrawer.test.ts`：下拉候选含 Off、非法默认重置、清空回归测试。
- `frontend/src/lib/api.ts`：仅 `llmModelCatalogCaps`（`:4625`）请求参数类型增加可选 `apiPath`；不改其他 API 类型。
- `pkg/architecture/testdata/graph.json`：生成器导致的 loc-only 变更，不接受新增 package/import 边（gateway→turn 边已存在，`models_api.go:28`）。
- 本计划及 `plans/README.md`：记录真实验证结果和状态。

**不得修改**：state schema/SQL、config 投影优先级、Subscription/ProvisionOptions 公共接口、订阅实现/插件源码、登录/凭据格式、模型目录资产、Go/前端依赖（不 install/升级）、`effortLabel.ts` 与 locale 文案（标签已齐全）、Web 聊天页强度选择器、审批面板、`/model` 的产品语义、run/chat_slash 的会话架构、`pkg/llm/anthropic` 的 wire 能力（补 Anthropic 关闭思考是另一个计划）。只新增 plan 不构成生产功能实现。

## 6. Git workflow

执行器在隔离工作树中基于核对后的 dev 开工，建议分支 `advisor/311-connect-default-effort`。不得覆盖用户未提交改动，不自动合并、push 或开 PR；只有明确获得提交授权才 commit。获授权后的 commit 遵循 Conventional Commits + DCO，例如 `feat(tui): configure a model default effort through connect`，记录实际跑过的验证，不跳 hooks。

本仓 `.gitignore:77` 忽略 `plans/`；新 worktree 不会自动得到本计划。派发时必须把本文件全文交给执行器，或明确提供可读的原文件绝对路径 `/Users/doudou/workspace/unionj-cloud/forebrain-harness/plans/311-connect-configures-model-default-effort.md`，不能仅让执行器在新工作树找相对路径。不要为了提交计划而擅自修改 ignore/强制 git add；索引由拥有原 plans 目录的 advisor/owner 更新。

## 7. Steps

### Step 0 — 核对基线与先写失败用例

运行漂移检查、环境块、C0。先在 `pkg/process/setup_test.go` 加 `TestExecuteModelDefaultEffort`，用子测试覆盖新行、旧行 high→low、high→Provider default、nil 指针不更改、UpdateParams 明确传值时其他字段保留。

对当前没有的新 Options 字段，编译失败是预期红灯；记录一次缺字段/断言失败后再进入 Step 1，不能提交一个无法编译的阶段。只用 dummy key，不把完整行/凭据打印到失败消息。

**Verify**：C0 → PASS；C1 → 新契约因字段缺失或尚未实现而失败，不能 `[no tests to run]`。

### Step 1 — 在 Execute 统一保存默认与清理旧 override

在 `pkg/process/setup.go` 的 Options 增加 `CapDefaultEffort *string`，仅在非 nil 时启用新契约：

1. 在已有 DB/验证器安装完成、构造 LLMModelInput 后，使用相同身份调用 LLMModelHeld，读错误直接返回。
2. params 基础：UpdateParams=true 时用入口已 parse 的 opts.ParamsJSON；否则用匹配行的 ParamsJSON，未找到则空。对存量 JSON 用 ParseLLMRequestParamsJSON，坏数据返回错误，不能用“解码失败=空 map”掩盖。
3. 用 WithReasoningEffort(base, "") 清除嵌套 effort，转 string（nil 变空 string），设置 in.ParamsJSON 指针；把 trim/lowercase 后的用户选择设置为 in.CapDefaultEffort。两字段与 SubagentAllowed 在**同一次 UpsertLLMModel** 写入。
4. nil 指针时完全沿用当前 UpdateParams 行为；不要更改泛用 UpsertLLMModel 的规则，也不要影响 Subscription 的普通 ProvisionRows。
5. 保持订阅已有 pasted key/source/sourcePlan/其他 caps；不修改首次模型才设 agent 默认的规则。

新测试加：同 provider 不同 model、同 provider/model 不同 URL/path 的兄弟行不变；登录空 path 匹配 legacy display path；显式更新 params + 默认同时给出时用户默认获胜；cap default 和去 effort 的 params 同时持久化；调用 `row.ConfigRow()` → `appcfg.ProviderFromModelRow` 的实际 effort 正确。nil 调用保持旧 raw params 不变，不要求 JSON 字节重排。

**Verify**：C1 → PASS；C0 → PASS。若要为非交互调用添加新必填参数或迁移 schema，STOP。

### Step 2 — 共用默认强度页及纯候选过滤

在 `pkg/turn/models.go` 增加窄函数 `SetupReasoningEfforts(provider, model, apiPath string, levels []string) []string`，只处理输入列表，不读 DB、不发请求、不新增包：

- 用 orderedEffortLevels 归一化排序；复用 trimToSendable 的现有过滤。
- 对普通 provider/Custom 的 Messages APIPath 用已有 Anthropic 过滤规则（`anthropic.AcceptsEffort`）。判定在 turn 本地复刻 `run.isAnthropicMessagesPath` 的规则（trim + 去尾斜杠后 `strings.HasSuffix(path, "/messages")`，见 `pkg/run/config.go:569-571`）：**turn 与 run 同为 Layer-3，禁止 import run**（`pkg/architecture` 强制），不得为此新增依赖边。分支顺序必须与真实 dispatch 一致（§3.3）：chatgpt 直接透传；opencode/opencode-go 沿用 trimToSendable 的 `opencode.ProtocolFor` 判定（Google 协议返回 nil、Anthropic 协议过滤）；`llm.HasProviderFactory(provider)` 为真的 provider 透传（factory 在名字/path 判断之前接管 client）；其余 provider 按 anthropic/minimax 名或 Messages path 过滤。
- 不修改 ModelReasoningEffortsRow 现有行为；新入口仅服务需要显式能力草稿的 setup。
- `models_test.go` 用 `TestSetupReasoningEfforts` 覆盖去重排序、不修改输入、Anthropic、Custom Messages、Responses/Chat、OpenCode Google、插件自有 factory 路径。仍保持 API 层无新依赖边。

在 `pkg/tui/setup.go` 增加 `setupPageEffort`，独立字段 defaultEffort/effortAnswered 与当前答案身份；用 §2.3 规则从当前 offer、准确目标行、目录和 Custom 能力草稿获得候选。同一份 helper 供三组调用。目标行读取失败应报错，不降级成新行来覆盖已有默认。

本步先添加新默认页及候选 helper，不切换现有生产页序、不移除旧函数，因此原有调用与测试仍可编译运行；Step 3 再统一切换并删除无调用的旧路径。不要通过包级全局临时 override 给目录“注入”草稿。

默认页可以使用现有 Select，建立 display label→wire value 的本地选项映射；传入显式 defaultOption。单独验证 Provider default 映射为已回答的空值，不能与未回答混淆。

**Verify**：C2 → PASS；`go test -tags fts5 ./pkg/tui -run '^TestConnectDefaultEffortDefaultSelection$' -count=1 -v -timeout 180s` → 新页直接测试 PASS；C0 → PASS（此时仍走旧页序）。原有 setup effort 测试在 Step 3 切换时再更新，不删除无档位/非 medium 模型覆盖。

### Step 3 — 让三个分组走同一状态机

1. 按 §2.1 更新 flowPages/run，保证分页编号与真实页一致；Subscription 的 Offers 不再是终态。
2. `chosen []int` 改单个索引（-1 未选择），firstOffer 改为明确的 selectedOffer；删除 offerSubagent map、offerSubagentAnswered、pickOfferSubagents 与多模型授权代码，复用 pickSubagents 的单行 Yes/No。
3. `pickOffers` 只 fetch/cache 模型并用 selectRichTabbed 单选，不再 Provision、不写授权、不返回整体完成。替换复数文案及 Space 多模型勾选说明。保留 tab、Name/Model 区别、登录探测缓存和 timeout。
4. 身份读取统一选择实际 offer 的 Provider/Model/BaseURL/APIPath，不能用 providerChoice.Label 或 first selected batch。storedRow/授权回显采用 LLMModelHeld。
5. Custom 的能力多选依旧存在；回显正确行的支持列表。本次改变能力后重新验证默认；已经有目录档位的 Custom 只跳能力声明页，**绝不跳默认页**。
6. 所有换模型/端点/协议路径做局部下游状态失效；同模型 Esc 保留已有有效答案。不要清掉 DB 句柄而不 close，不要复制一个持有 DB 的整个 setup 当草稿。
7. Review 使用独立默认字段展示。API Change endpoint 若身份改变，退出 review 回到 effort，再走授权和 review；Esc 取消修改 endpoint 要保留旧完整身份，不留下半改 URL/path。
8. options 三个分支均输出 CapDefaultEffort 指针和 SubagentAllowed；API key 不再从 cfg.Defaults 带入 ParamsJSON，不再为单纯选择默认设置 UpdateParams。Custom 仍输出 CapEfforts/verbatim；Subscription 仍不泄漏登录/pasted key。
9. 本步同时完成保存路径搬迁，不能留下订阅从 Review 返回但从不 Provision 的中间实现：在 run 的 Review Save 分支，仅 Subscription 调用提取后的 provisionSelected，传一个未篡改的 Offer，保留 provisionWithPastedKey。校验返回恰一 ID 且对应所选身份，再返回 Options；由外层 Execute 保存默认及授权，删除旧 SetSubagentAllowed 提前写入。Provision 失败按 §2.4 返回模型页，不重登。对应旧测试若原来只调用 Prompt 就断言授权已保存，应补上 Execute 后再读 DB；新取消测试须证明保存前没有 Provision。
10. 此时 pickModel 只负责选模型；查引用后删除无调用的旧 promptReasoningEffort/promptEffortChoice/provider-only params 合并路径及其专用状态/常量，保留其他仍使用的解析辅助函数。原有调用这些 helper 的测试同期改成新页测试，不能靠保留双套正式路径过编译。

改写旧测试：`TestConfigureSubscriptionAddsTheModelsTheUserTicks`、`TestOffersPageLabelsModelsByVendorName`、`TestSubscriptionFlowDropsSettledSignInPage`、`TestEscOnOffersPageReturnsToProviderPage`、`TestConfigureSubscriptionWritesTheSubagentSwitchByRowID`、两个 `TestOffersPageOpensOn...`。把“同时添加两行”改成“只选择一行且另一已有行不变”，不要直接删掉测试。

更新 `chat_session_test.go` 的三个 PromptMainLLMSetup effort 用例：断言 cap 指针而非旧 params；保留其他参数的用例必须 seed 同身份 DB 行并走 Execute，不再把 provider-only cfg.Defaults 当可复制参数。

**Verify**：C3 → 新三组流程、无档位页、导航、同名跨 tab/endpoint 测试 PASS；C0 → 所有旧相关回归 PASS。

### Step 4 — 接通订阅最终提交、探测和会话快照

- 复核 Step 3 已搬迁的订阅提交链路，加失败/取消用例：Select/Review 取消不能进入 provisionSelected；数量/身份不匹配不得返回可保存 Options；Provision 错误按 §2.4 返回模型页且不重登。保留错误分类与 secret 输入；失败不能输出“全部未写入”这种无法保证的说法。
- fakeSubscription 必须能模拟真实官方 CapDefaultEffort 写入。用 `TestConnectDefaultEffortSubscriptionPersistsAfterExecute` 验证：official=medium，用户=high，Provision 后 Execute 后最终 high；随后用户=Provider default，两处 override 均空；先前模型行的 params siblings/caps/source/key/授权不意外丢失。真实注册表/插件公共 API 不改。
- Custom 可选 Probe：在 setup 中将同目标行保留参数去掉旧 effort，再用 `WithReasoningEffort(..., s.defaultEffort)` 合成 **仅用于探测** 的 ParamsJSON 指针传给现有 ProbeLLMModel。ProbeLLMModel 当前只使用 ParamsJSON，不会应用 CapDefaultEffort；因此只把 cap 指针塞入 probe input 没用。存储时仍执行 canonical cap + 清除 params effort，不能把探测用 params 原样落库。
- runConnect 的 SelectModel 第四参改为 opts.CapDefaultEffort，choice.ReasoningEfforts 用保存的 rep.ModelID 的 row-aware lookup；保留 SyncConfig 先于 SelectModel。成功文案可在既有事实中显示默认值，但不添加重复成功提示。
- 加集成测试：三组各走向导→Execute→关闭/重开 DB→ConfigRow/ProviderFromModelRow；API/Custom 用 httptest 接收一次探测或请求，断言实际 wire effort；订阅用注册 fake factory/已有 runner fixture，不能访问真帐号。检查会话持久化选择的 high/空值，并验证先有其他会话档位时本次明确空值可以清掉。

**Verify**：C1、C2、C3、C0 → PASS；测试必须观察 DB 与投影/传给会话的实际值，不能仅断言 selector 的文本或 Options。

### Step 5 — Web 模型表单对齐 Off / Provider default

按 §2.5 实施，顺序：gateway → 前端 → 测试。

1. `llmModelCatalogCaps`（`models_api.go:282-301`）：在已有 efforts 分支内（`known && len(efforts) > 0` 的块里，`efforts` 变量只在该块可见），若 `slices.Contains(efforts, "none")` 则 `off := true; caps.ReasoningOff = &off`。不改 Efforts 内容。
2. 可发送过滤：新增局部 helper（同文件）`llmSendableEfforts(provider, model, apiPath string, levels []string) []string`，内部调用 Step 2 的 `turn.SetupReasoningEfforts`。在 `handleLLMModelCatalogCaps` 的 live overlay 之后（`:834-839`）与行输出处 **`llmModelApplyLiveCaps` 调用之后**（`:429`，live 答案会覆盖 Efforts，先覆盖再过滤）应用；apiPath 编辑态取 `stored.APIPath`/`row.APIPath`，新增态取请求可选 `api_path`（GET query 增读、POST 用 `write.APIPath`），为空时不过滤协议（保持现状行为）。同步更新 `:779-782` 注释说明协议只用于过滤。**无来源但已存行声明了档位时**：live overlay 未命中（行输出）或 `ResolveModelInfo` 失败/ErrNotListed（接口）且 `stored` 行 `CapEfforts` 非 nil（空 slice 是"明确无离散档位"，填空），把该声明经 `llmSendableEfforts` 修剪后填入 `out.CatalogCaps.Efforts`/`body.Efforts`（声明优先于静态目录，与运行时行 override 语义一致），`EffortsKnown` 保持 false；行自身 `Caps.Efforts`（勾选框数据）不修剪、不改写。
3. `llmModelCapsValid`：在 `:1039` 的 effort 校验中，`effort == "none"` 分支要求 ① off-capable 按优先级（`caps.ReasoningOff` 非 nil 取它，否则 `stored.CapReasoningOff` 非 nil 取它，否则目录 efforts 含 none）且 ② `llmSendableEfforts(provider, model, apiPath, []string{"none"})` 非空（apiPath：`write.APIPath` 非空取它，否则 `stored.APIPath`；需把该值传进校验函数或在调用点先算好）。不满足沿用现有拒绝文案；满足则放行，不再要求 levels 含 none。非 none 档位：levels（`caps.Efforts`，空则目录）先经 `llmSendableEfforts` 修剪再 contains，拒绝文案不变。其余分支不动；`:1018-1024` 对 Efforts 含 none 的拒绝不动。
4. 前端 `ModelCapsFields.vue`：`effortOptions` 改为先取 base（`props.effortsKnown || props.sourceEfforts.length ? props.sourceEfforts : props.draftEfforts.filter(≠none)`——gateway 已在无来源时把存储声明修剪后填入 sourceEfforts，见第 2 条；`effortsKnown` 仍单独决定勾选框是否出现），再按优先级判 off-capable：`props.draft.reasoningOff ?? props.catalog.reasoningOff ?? base.includes('none')`（`??` 保证显式 false 生效，不得写成 `||`），为 true 且 base 不含 none 时前置 `'none'`；用 `EFFORT_LEVELS` 顺序排序（从 `effortLabel.ts` 增 import，不改该文件）。props 已含 `catalog: LlmModelCaps`（`:188`），`LlmModelCaps.reasoningOff` 已存在（`api.ts:1411`），无需改类型。
5. 前端 `ModelFormDrawer.vue`：`loadCaps` 请求加 `apiPath: draft.apiPath`；`loadCaps` 的 catch 分支同时清空 `sourceEfforts`（新 base 规则下 `effortsKnown=false` 也会读它，不能留上一个模型的旧值）；新增 watch：当 `draft.caps.defaultEffort` 非空且不在 `['', ...effortOptions]` 中时 emit 重置为 `''`（effortOptions 需从 caps fields 暴露或在 drawer 复算同一规则——优先把候选计算提为 drawer 内 computed 并传给 caps fields，避免两处规则漂移）。`capsWrite` 与 save 的清空链路不动。
6. 测试：gateway `models_api_test.go` 新增 `TestLLMModelCapsOffDefault`（子测试：off-capable 行存 none 通过；非 off-capable 行存 none 被拒；本次显式 `reasoning_off:false` 不被已存 true 掩盖、存 none 被拒；off-capable 但 Messages 协议存 none 被拒；Efforts 含 none 仍被拒；catalog-caps 上报 reasoning_off；Messages 协议过滤掉 none 且非法默认归空；Custom Messages 行存 `minimal` 默认被拒；无来源时存储声明修剪后上报、勾选框回显不变；清空默认后重读为空）。沿用该文件既有 httptest/隔离 DB 范例。前端 `ModelFormDrawer.test.ts` 新增用例：reasoningOff=true 时下拉含 `关闭思考/none`；false 且目录无 none 时不含；目录 off-capable 但取消「允许关闭思考」后下拉无 none 且已选 none 重置为默认；切协议使当前默认非法时重置为默认；保存 Off 的 caps body 含 `default_effort:"none"`；保存默认时 body 省略 default_effort 且 gateway 替身收到整体 caps；无来源行下拉 base 用修剪后的 sourceEfforts。mock 范例见该文件 `:92` 的 `vi.spyOn(forebrainApi, 'llmModelCatalogCaps')`。
7. 浏览器验收：`frontend/` 下 `npm run dev` 起 dev server，配合 loopback gateway（或组件测试已覆盖交互时如实记录）打开设置·模型·高级配置，确认 Off 出现/消失、清空保存、协议切换重置与原型 web-align 帧一致；无法起真实 gateway 时以组件测试 + 原型对照记录，并标明未做真实后端联调。

**Verify**：C10 → PASS；C11 → PASS；C0 → PASS（gateway 改动不影响 TUI 基线）。若发现 caps JSON 需要新字段或订阅协议需要改动才能表达 off，STOP（`reasoning_off` 已存在，不应需要）。

### Step 6 — 全量回归与可见交互验收

运行 C4–C11。C8 允许 graph 的 loc-only 刷新；检查 JSON edges/packages 与基线相同，不能调大包预算来通过。

在 `setup_test.go` 增加 `TestConnectDefaultEffortRendersSingleChoice`：使用现有 picker state/真实 panel layout，而非只用 fakeSelector 的 options 列表。借鉴 `tabbed_picker_test.go:23`；覆盖模型页无多选圆点/Space 勾选提示、Plan tab 与 endpoint 仍在、默认页标题/英文档位/Provider default 可见。真实 renderer 路径需覆盖 Enter 单选和 Esc 回退。

在 scratch home 中启动 TUI 进行键盘验收，不能把单测当作真实操作：

```sh
go build -tags fts5 -o "$VERIFY_DIR/forebrain" ./cmd/forebrain
cp assets/models.json "$VERIFY_DIR/models.json"
FOREBRAIN_HOME="$VERIFY_DIR/home" "$VERIFY_DIR/forebrain" interactive
```

本机 native 库路径已在编译配置中；若启动需要其他运行资产，先报告缺失，不拷贝用户 home。用非敏感 dummy key、loopback mock endpoint；不向真实提供商发送 dummy key，不访问真实订阅。至少手动走 Custom→支持档位→默认 high→review→save，再重连改 Provider default；API key 走已配置 loopback endpoint 并检查重连，取消探测或只探测本地 mock。订阅使用能驱动真实 selector 的测试 fixture/PTY；没有可隔离的真实订阅 UI 环境时明确记为未验收，而不是声称真实登录通过。

保存逐帧文字/截图证据到 scratch 目录，确认页序、默认回显、Enter/Esc、换模型不会串值；Off 三态（有 Off/无 Off/仅 Provider default）各走一遍。Web 验收按 Step 5 第 7 条执行并记录。本计划不授权真实登录、真实付费模型请求。

**Verify**：C4–C11 → 全绿；C3 中真实布局/键盘测试 PASS；实际终端 golden path 与取消/reconnect 验收记录齐全，否则保持 IN PROGRESS/BLOCKED 并说明缺项，不标 DONE。

## 8. Test plan（必须逐项落在测试里）

| 新测试入口 | 文件 | 必须覆盖 |
|---|---|---|
| TestExecuteModelDefaultEffort | pkg/process/setup_test.go | nil/非空/明确空值；清 legacy effort；保留 siblings；UpdateParams 两种组合；准确行、兄弟行不变；登录空 path；重开 DB；坏 params 拒绝 |
| TestSetupReasoningEfforts | pkg/turn/models_test.go | 输入不变、排序去重、unknown wire ID、空集合、协议过滤、插件 factory 不被 display path 误判 |
| TestConnectDefaultEffortAllGroups | pkg/tui/setup_test.go | 每组都经过单选默认页；Subscription Provision 仅 1 项；Custom 能力多选仍在（含 Off 声明独立勾选）；无档位页仍出现；Off 三态：off-capable 且可发送必含 Off、不支持关闭思考必无 Off、Anthropic off-capable 无 Off |
| TestConnectDefaultEffortDefaultSelection | pkg/tui/setup_test.go | 同次答案、stored params/cap、已有空默认、新行官方默认、非法/过期默认 fallback、无 medium 自动假设；选 Off 落 `none` 且与 Provider default 清空互不混淆 |
| TestConnectDefaultEffortNavigation | pkg/tui/setup_test.go | 每个 Esc、已登录跳 Sign in、无保存副作用、API endpoint 变化重新问、Custom 能力收窄/取消 Off 声明后重选、同模型回退保留与换模型重置 |
| TestConnectDefaultEffortSubscriptionPersistsAfterExecute | pkg/tui/setup_test.go | 官方默认不盖用户值；显式清空；pasted-key 重试；source/caps/授权保留；失败不冒充成功 |
| TestConnectDefaultEffortUsesSelectedIdentity | pkg/tui/setup_test.go | 同 label 不同 tab、同 pair 不同 endpoint、wire ID 与显示名分离、登录 legacy path、兄弟行参数不串入 |
| TestConnectDefaultEffortProbeAndSession | pkg/tui/setup_test.go | custom probe 实际携带选定 effort；探测值不变成持久化 override；runConnect 明确指针 high/空快照，SyncConfig 后生效 |
| TestConnectDefaultEffortRendersSingleChoice | pkg/tui/setup_test.go | 真实控件无模型多选提示，标题/默认/endpoint/tab 可见，Enter/Esc 真键盘处理；Off 选项标签为 `Off`、说明 `none`，Provider default 说明不传参数 |
| TestConnectDefaultEffortOffSplit | pkg/tui/setup_test.go | Custom 勾 Off + 档位后 Execute：`cap_reasoning_off=true`、`cap_efforts` 不含 none、`cap_default_effort="none"`；取消 Off 声明后默认页无 Off 且旧 none 默认回退 Provider default；行/offer 显式 ReasoningOff=false 时目录含 none 也不出 Off（优先级，非 OR）；Subscription 向导不改 offer 的 ReasoningOff |
| TestLLMModelCapsOffDefault | pkg/gateway/models_api_test.go | off-capable 行存 none 通过；非 off-capable 行存 none 拒；显式 caps false 不被 stored true 掩盖；off-capable Messages 存 none 拒；Efforts 含 none 拒；catalog-caps 上报 reasoning_off；Messages 过滤 none；Custom Messages 存 minimal 默认拒；无来源时存储声明修剪上报、勾选回显不变；清空默认重读为空；api_path 可选参数缺省不过滤 |
| ModelFormDrawer.test.ts 新增 Off 用例 | frontend/src/components/models/ModelFormDrawer.test.ts | reasoningOff 开/关决定下拉含/不含 none；目录含 none 时含 Off；目录 off-capable 但显式取消开关后无 none 且非法默认重置；协议切换重置非法默认；保存 Off body 含 default_effort:"none"；保存默认 body 省略 default_effort；无来源行下拉 base 用修剪后的 sourceEfforts |

不得放宽 selector 的默认成功行为来凑绿。原有登录取消、不可达重试、空 offers、缺计划 key、provider 同名组定位、单行 subagent 授权、追加模型不改 agent default 测试都保留覆盖。

## 9. Done criteria

- [ ] C1/C2/C3 的指定测试实际运行（-v 可见），全部通过；不是 no-tests。
- [ ] C0、C4 全部通过；C5/C6 exit 0；C7 无输出；C8 架构测试通过且 graph 仅 loc 变化；C9 无 scope/空白问题。
- [ ] 三组向导都返回非 nil 默认指针，并在重新打开的 DB 中读到正确 CapDefaultEffort；ParamsJSON 不含旧 reasoning.effort，其他字段保留。
- [ ] 订阅模型选择/授权不再调用多模型选择；测试中每次 Provision 入参长度为 1，最终 rep.ModelID 对应选中项，其他行不变。
- [ ] Provider default 真正清除 cap 与旧 params effort，重连回显仍为空；不被官方默认、同 provider 的另一行或旧会话覆盖。
- [ ] 禁止硬编码兜底 low/medium/high；缺档位时仍有 Provider-default-only 配置页；`none` 与空默认测试分开。
- [ ] Off 三态在 TUI 与 Web 均有测试：off-capable 且可发送必含 Off；不支持关闭思考的模型任何入口都无 Off；Anthropic off-capable 无假 Off；`none` 从未进入 CapEfforts/llmEffortLevels。off-capable 判定三处（TUI 候选、Web 下拉、gateway 校验）同为优先级语义：显式 false 不被更弱来源掩盖；gateway 对 none 同时要求 off-capable 与协议可发送；Custom Messages 的不可发送默认（如 minimal）Web 不显示、gateway 拒存。
- [ ] C10/C11 实际运行并通过；Web 下拉与 gateway 校验语义一致（Off 可存、非 off-capable 拒 none、清空默认回归通过）；Web 浏览器验收或如实标记未联调。
- [ ] 实际终端验收记录完整；未完成的真实 UI 项目如实标记，不能只凭单元测试宣布功能完成。
- [ ] 仅白名单文件变更；无 schema/插件/依赖改动，Web 改动限于 §5 白名单；无真实凭据/帐号数据进入代码、计划或日志。
- [ ] 根据真实结果更新本计划与索引状态；尚有失败或必要 UI 验收未完成时不得标 DONE。

## 10. STOP conditions

- 基线漂移改变了 Options、Provision、ProviderFromModelRow 或模型身份规则，本文契约无法按既有调用链实现。
- 必须修改 DB schema、订阅/plugin 协议、run 的 provider/model 身份体系、Web 聊天选择器或审批面板才能完成：报告具体阻塞，不顺手重构。
- 发现官方 offer 声称的档位客户端确实无法发送：不绕开过滤、不假装已支持；记录 provider/model/protocol 与缺失能力，不能把不支持的项给用户。**off 同理：Off 只给"支持关闭且可发送"的模型，不得扩到所有模型；不支持关闭思考的模型保持无 Off。**
- Provision 成功返回的 ID 不对应所选行，或无法证明 default 将写回同一行；停止而不是按第一个 provider/model 猜测。
- 要求取消恢复已保存登录、或要求订阅跨远程操作+DB 全原子回滚才能满足新的产品要求：这是新增事务设计，不能在本期臆造。
- 测试必须读取真实 FOREBRAIN_HOME/真实 token 或访问付费模型才能跑通；改用 fake/loopback，不自行认证。
- 全量验证出现范围外失败或同一步经合理修复仍两次失败：记录命令和失败，不降低断言/跳过测试来绕过。

## 11. Maintenance notes 与考虑后不采用的方案

- **不新增 defaults 表/agent reasoning 字段**：现有 cap_default_effort 已被模型配置 UI 与运行时投影使用；重复存储会制造优先级冲突。
- **不继续把新选择只写 params**：那不是与行默认对齐，且未来编辑 cap 仍会被旧 effort 遮住。
- **不覆盖原始 Offer.DefaultEffort、不改批量 Provision API**：官方 metadata 与用户偏好属于不同层，其他表面仍需要批量接口。
- **不把 Custom 支持档位收缩成单选**：支持集合和默认元素是两项独立配置，用户禁止的是订阅“模型多选”。
- **不增加 runtime models.dev 拉取**：最新订阅模型继续由既有 sub.Offers 获取；静态 catalog 的更新策略保持不变。
- **不扩大为全部会话动态跟随行默认**：本次 /connect 的会话快照明确取新选择，已有会话和审批模型链路维持原语义。
- Review 时特别看：Provision 后是否仍覆盖回用户默认、明确空值是否被当未回答、同 provider 不同行是否串 params、fake 是否真的模拟了官方默认写入、API endpoint 编辑是否绕过重选、**none 是否被误写进 CapEfforts、off 能力是否只认 offer/行/目录/Custom 声明四源且按优先级解析（显式 false 不得被 OR 掩盖）、Web 与 TUI 候选规则是否漂移（应共用 turn.SetupReasoningEfforts）、gateway 校验是否与下拉同一优先级和可发送集合**。
- **已知边界（评审收口）**：全新未保存的 Custom 行，勾选框刚勾的档位尚未经 gateway 修剪（catalog-caps 无存储声明可读），下拉暂按勾选回显；保存时校验按可发送集合拒绝不可发送默认并给出既有文案。已保存行由修剪后的声明 base 覆盖，不再出现该窗口。
- **Off 的长期维护**：`none` 是能力不是档位——任何新入口（ picker、表单、校验）都必须沿用"ReasoningOff 独立、候选组装时并入、发送前 trim"三段式；若未来 Anthropic SDK 提供 thinking-disabled wire 路径，更新 `anthropicEffortLevels`/AcceptsEffort 后 Off 会自动经同一条过滤链出现，不需要改 picker。
- 同 provider/model 多 endpoint 的运行时消歧、订阅刷新对既存用户默认的长期保护是后续议题，不在本次 feature 中宣称修好。Web 的 Off/默认对齐已纳入本次（§2.5）；Web 其余独立保存语义议题仍后续。后续若更改这些入口，要重新审视 cap 默认与显式 params 的优先级，但不能据此阻塞此次明确的 `/connect` 默认配置闭环。

## 12. 2026-11-17 reasoning-semantics audit corrections (owner: "tui端和web端语义是否完全统一…必须补齐")

> Prototype gate: **owner re-confirmed the revised reference prototype on
> 2026-11-17** ("原型都已确认"); docs reference hash `aef1aa8c…63b0`.
> `plans/311-connect-prototype.html` itself is unchanged (`7325ab5e…`), so the
> §0 gate for this plan's own prototype still holds.

The audit at HEAD `1013b50f` **confirms** this plan's Off three-state, the
`Provider default` vs `Off` split, `cap_reasoning_off` unwrapping, and "`none`
never goes into `CapEfforts`". Keep executing §2 as written. The four points
below are additions the audit found; where they disagree with the text above,
this section wins.

1. **One label table.** `customThinkingLevels` (`pkg/tui/setup.go:1069-1081`)
   spells `xhigh` as `Highest` / `最高`, while `turn.EffortEnglishLabel`
   (`pkg/turn/models.go:279-305`, `xHigh`) and the web's `effortLevelLabel`
   agree on `xHigh`. When Step 5 adds the `Off` item to that table, rename the
   English label to `xHigh` in the same edit. Do not keep a third spelling.
2. **One order.** Build the default-effort page, the Custom capability page and
   the Web default dropdown in `none, minimal, low, medium, high, xhigh, max`
   order (the `turn.orderedEffortLevels` order). Do not use an offer's or the
   catalog's file order as UI order.
3. **One sendability helper, exported (keep Step 2's name).** Step 2 already
   defines `turn.SetupReasoningEfforts(provider, model, apiPath, levels)` and
   Step 5.2 calls it through the local `llmSendableEfforts`. Keep that name and
   export it exactly as Step 2 says — plan 318 reuses `turn.SetupReasoningEfforts`
   and replaces `trimToSendable`'s body with it, so there must be exactly one
   protocol decision (provider + `api_path`), not two. Do not rename it to a
   second helper.
4. **Plan 316 collision.** `customThinkingLevels`' labels are edited by 316
   (removing `·`) and by this plan (adding `Off`, renaming `xHigh`). 316 is
   executing first in its own worktree; apply this plan on top of 316's result
   (labels joined with spaces) and never restore `·`. `Provider default` keeps
   its wording: it is not a subagent's `Row default`, and the owner confirmed
   the prototype.

Nothing else changes; the Off three-state still excludes fabricated `Off` for
protocols that cannot send `none`.
