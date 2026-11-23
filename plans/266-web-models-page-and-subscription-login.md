# Plan 266: 网页——设置·模型（全局）、添加/自定义模型、统一订阅登录、四项显示的模型下拉、设置·变量

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **开工前提**：owner 已确认原型（2026-11-07 确认；本行状态为 TODO）。
>
> **Drift check (run first)**:
> `git diff --stat 0a698bf2..HEAD -- frontend/src/views/ProvidersView.vue frontend/src/components/chat/ModelChip.vue frontend/src/components/subagents/SubagentConfigDrawer.vue frontend/src/views/SettingsView.vue frontend/src/lib/api.ts`
> → `ModelChip.vue`、`api.ts` 只允许有 plan 261 的改动；其余必须为空。对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（整页替换 + 新弹窗；接口在 plan 265 已就绪）
- **Depends on**: plans/265-gateway-models-subscriptions-variables-api.md（全部接口）、plan 261（会话下拉按 id）
- **Category**: direction
- **Planned at**: commit `0a698bf2`, 2026-11-07
- **原型**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/llm-models-preview.html`（**v4，可交互单页；owner 2026-11-07 已确认**）。**实现 1:1 还原原型**（owner 规则）。
  原型不是分场景的截图稿：左栏进「对话 / 设置」，设置里切「模型 / 记忆 / 变量」，表格上的添加 / 编辑 / 删除、抽屉里的服务商与协议选择、订阅登录三步都真生效。实现时按同一交互做，不要只还原静态版面。
  v2 按 owner 2026-11-07 三条意见改：①样式与同目录已有原型（`config-db-settings-preview.html`）一致——v2 的 CSS 整段取自它；②多主代理共享的全局页面放设置页——「模型」「变量」都在设置 · 全局服务；③API Key 列不写「已保存」，只显示 `••••` + 末四位。v3 再按 owner 意见改：④添加、编辑模型都用**右侧抽屉**（同 MCP、子代理），不用弹窗；⑤抽屉标题随动作：编辑图标打开的是「编辑模型」。

## Why this matters

owner 2026-11-07 的网页要求：模型配置全局一张表，**作为全局配置放在设置页**（owner 规则：多主代理共享的全局页面必须放进设置页，不做左栏顶层页）；同 provider/model 可配多条、四项相同只能一条且保存前提示；参考 workbuddy 的「添加模型」，
但**自定义不填 provider**、**接口协议必选**；美团、腾讯（及火山方舟、BytePlus、智谱 Coding Plan 接入点）进内置列表；
「**模型下拉框和 picker 必须把 provider/model/base url/api path 都显示出来**」；「**登录订阅的入口需要统一**」，且**只有支持 OAuth 或其他协议登录的订阅才放进统一入口**（今天 ChatGPT、OpenCode Go；火山方舟由 plan 281 加，网页零改动）；
`.env` 删除后 MCP/LSP 的 `${名称}` 从「设置 · 变量」取值。

## Current state

- 旧页面 `frontend/src/views/ProvidersView.vue`（1205 行，路由 `/providers`，`frontend/src/router/index.ts:111-115`）：整表 PUT 的旧接口（plan 262 起 PUT 返回 410）、
  两个登录按钮（`providers-connect-opencode`、`providers-connect-chatgpt`，`:25`/`:34`；按钮区块 `:19-43`）与登录弹窗（`:244-360`），列表用 `useServerList`（import `:433`、调用 `:493`）+ `DataTable` + `TablePager`。
  组件与写法范本：`DataTable`、`ListToolbar`、`RowAction(s)`、`ForebrainModal`、`ConfirmDialog`、`BadgeComponent`（`frontend/src/components/common/`）；shadcn 组件从 `@repo/shadcn-vue/components/ui/*` 引入；
  i18n 用 `useI18n()` 的 `t('…')`（`frontend/src/locales/`）。
- 会话模型下拉 `frontend/src/components/chat/ModelChip.vue`（146 行；ChatView、WorkshopView 两处使用）：菜单宽 260px，每项 `<b>model</b><span>provider</span>`。
- 子代理配置抽屉 `frontend/src/components/subagents/SubagentConfigDrawer.vue:32-44`：`Select` 的值是 `provider/model`（`modelKey`，`:367-368`）。
- 设置页 `frontend/src/views/SettingsView.vue`：分组标签（`tabGroups`，`:84-116`），记忆模型在记忆相关标签里由 settings 接口驱动（`memories.extract_model`/`consolidation_model`）。
- plan 265 提供的接口：`/api/llm-models`（列表/choices/增删改/默认/测试）、`/api/llm-providers`、`/api/subscriptions`（列表/offers/provision）、`/api/auth/login/:id…`（状态体含 `flow`）、`/api/env-vars`。
- owner 的网页规则（全部适用）：原型 1:1；表单用弹窗；所有列表服务端分页带总数；**先用 ai-elements-vue、再用 shadcn-vue 的原生组件，都没有才自己造**；提示文案简洁，能用标签不写句子；代码高亮一律 monokai-light（本计划不涉及）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0 |
| 单测 | `cd frontend && pnpm test` | 全过 |
| 构建 | `cd frontend && pnpm build` | exit 0 |
| e2e（单个） | `FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh`（仓库**没有** `make e2e` 目标；通道同 plan 257 用的 `FOREBRAIN_E2E_SPEC`） | 通过 |
| 后端（删旧接口后） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` | ok |

## Suggested executor toolkit

- 用 `frontend-design` 技能核对细节；截图用无头 Chrome，与原型同场景逐一对照。

## Scope

**In scope**:
- `frontend/src/components/settings/ModelsTab.vue`（新建，替代 `ProvidersView.vue`）、`frontend/src/components/models/`（新建：`ModelsTable.vue`、`ModelFormDrawer.vue`、`ProviderCombobox.vue`、`ProtocolPicker.vue`、`RequestUrlLine.vue`、`ModelCapsFields.vue`、`SubscriptionLoginModal.vue`、`OfferPicker.vue`、`ModelSignature.vue`）及各自 `.test.ts`
- `frontend/src/components/settings/EnvVarsTab.vue`（新建）+ 测试；`SettingsView.vue`（加「变量」标签）
- `frontend/src/components/chat/ModelChip.vue`、`frontend/src/components/subagents/SubagentConfigDrawer.vue`、记忆模型下拉所在组件（`grep -rln "extract_model\|extractModel" frontend/src`）
- `frontend/src/lib/api.ts`、`frontend/src/locales/*`、`frontend/src/router/index.ts`（删 `/providers` 路由、改为重定向到设置）、左栏导航（删「模型服务」入口：在 `frontend/src/App.vue:619` 的导航数组里 `{ to: '/providers', … }` 那行——该入口**不在** `frontend/src/components` 下，`grep routes.providersTitle` 只会落到 `locales/index.ts` 与 `router/index.ts:114`）
- 删除：`frontend/src/views/ProvidersView.vue`、`ProvidersView.test.ts`、`frontend/src/components/providers/`（若只被它用）、`frontend/e2e/providers-keys.spec.ts`（由新 spec 取代）；
  后端删除旧 `/api/providers` 路由与 `handleProviders` 及只服务它的辅助函数/测试（`pkg/gateway/api_extra.go`、`api_extra_test.go`）
- 新 e2e：`frontend/e2e/settings-models.spec.ts`；改写 `frontend/e2e/opencode-login.spec.ts` 走新订阅弹窗

**Out of scope**：TUI；后端接口语义（plan 265 已定，发现不够用就 STOP）。

## Steps

### Step 1: 接口类型与调用（`frontend/src/lib/api.ts`）

按 plan 265 的 DTO 加类型（`LlmModelRow`、`LlmModelChoice`、`LlmProvidersListing`、`Subscription`、`SubscriptionOffer`、`EnvVarRow`）与方法：
`llmModelsPage(query)`、`llmModelChoices()`、`createLlmModel(body)`、`updateLlmModel(id, body)`、`deleteLlmModel(id)`、`setDefaultLlmModel(id)`、`testLlmModel(body)`、`llmProviders()`、
`subscriptions()`、`subscriptionOffers(id)`、`provisionSubscription(id, body)`、`envVarsPage(query)`、`setEnvVar(name, value)`、`deleteEnvVar(name)`；
登录沿用 `startProviderLogin/providerLoginStatus/submitProviderLoginCallback/cancelProviderLogin`，`ProviderLoginProvider` 改为 `string`（订阅 id），状态体加 `flow`，回调体加 `code`。
409 的 body 要能读出来（`existing_id`、`default_for`、`key_choice`、`needs_key_consent`）：`GatewayHttpError`（api.ts:73-77）今天只存 `status` 与拼好的消息串、`gatewayErrorText` 只提 `error`/`missing`——**本计划扩展它携带已解析的响应 body**（`JSON.parse` 失败置 `null`），上述字段从 body 读；既有消费点只读 `.status`/消息文本，行为不变。

**Verify**: `pnpm typecheck` → exit 0。

### Step 2: `ModelSignature.vue`（两行签名，全站唯一写法）

照原型 `.sig`：第一行模型名（粗）+ 服务商名（灰）+ 可选插槽（默认徽标）；第二行接口地址（等宽、灰）+ API 路径（等宽、浅蓝底 `--path-bg`/`--path-ink`）。
props：`model, providerName, baseUrl, apiPath, bare?`（`bare` 去掉 `https://`，下拉里用）、`hideProvider?`。登录类无路径时不画路径块。
把 `--path-bg: #E9EEF8`、`--path-ink: var(--forebrain-brand)` 加进 `frontend/src/assets/main.css` 的令牌（三套品牌色各自给值：路径块底色取各品牌 `-soft` 的更深一档，墨色取品牌色）。

### Step 3: 设置 · 模型（`ModelsTab.vue` + `ModelsTable.vue`）

照原型「设置 · 模型」：
- **位置**：设置页 `tabGroups` 的「全局服务」组第一个标签「模型」（组内顺序：模型、MCP、语言服务器、钩子、定时任务、记忆、共享技能、变量），标签 key `models`；
  左栏导航**删除**「模型服务」入口；旧路由 `/providers` 重定向到 `/settings?tab=models`（照 `SettingsView.vue` 现有的按路由选标签写法）。不新建顶层路由。
- 工具栏：左搜索（`搜索模型、服务商或地址`），右「添加模型」（主按钮）「订阅登录」「刷新」。
- 表格（设置内容区宽度内放得下，列宽照原型 colgroup：25% / 自适应 / 160px / 86px / 112px）：模型（模型名 + 默认徽标；下一行灰色服务商名，订阅来的追加「 · 订阅登录 · <套餐>」）、接口地址（等宽字体，**可折行完整显示**）、API 路径（浅蓝底路径块；**订阅登录类也要显示真实路径**——ChatGPT 订阅是 `/responses`（`CodexBaseURL` 之后），OpenCode 按该模型的协议给 `/v1/messages` / `/responses` / `/chat/completions`，不写「由登录决定」；OpenCode 里走 Gemini 原生协议的模型显示灰块「原生协议」而不是路径块）、
  API Key（**末四位明文、其余位点阵**——按 `api_key` 实际长度画点阵（最少 4 个、最多 12 个，列宽 140px 内不截断），最后四位用正文色显示；不写「已保存」；登录类灰字「账号登录」；没填 `未设置` 警示徽标）、操作（设为默认、编辑、删除；默认行星标位置占位不显示，保证对齐）。
- 服务端分页（`useServerList`），分页条与其它设置页列表一致。
- 删除：确认弹窗；409 时照原型「删除默认被拒」列出 `default_for`。

### Step 4: 添加/编辑抽屉（`ModelFormDrawer.vue` 及子组件）

照原型「添加 · 选服务商 / 内置 / 自定义 / 编辑 · 抽屉 / 重复配置 / 跳到已有行」。**右侧抽屉**（照现有 MCP 抽屉组件与样式：标题栏 + 可滚动内容 + 底部「取消 / 保存」），不是弹窗；标题：从「添加模型」按钮打开为「添加模型」，从表格编辑图标打开为「**编辑模型**」并带出该行的值（服务商、接入点、地址、协议、模型名、能力覆盖），Key 显示 `••••` + 末四位 +「更换」（不点更换则保存时不带 `api_key`）。（控件一律用设置页已有的：单选组、**开关类字段一律用 switch 组件**（owner 规则）、复选、带「词元」单位的数字框、`••••`+「更换」的密钥框）：
- `ProviderCombobox`：shadcn `Popover` + `Command`（可搜索）；分组「内置服务商」（`/api/llm-providers` 顺序，登录类不出现）与「其他 · 自定义」。
- 内置：「接入点」一排 chip（该 provider 的 endpoints，多于一个才显示），点选填入接口地址与协议，仍可改。
- **思考强度中文标签（owner 2026-11-07 定稿）**：关闭思考（none）· 极低（minimal）· 低（low）· 中（medium）· 高（high）· 最高（xhigh，沿用现有 `chat.effortXhigh`）· 极致（max）。在 `frontend/src/locales/index.ts` 补 `chat.effortNone`=关闭思考 / `chat.effortMinimal`=极低 / `chat.effortMax`=极致（英文 Off / Minimal / Max），对话的思考强度选择、子代理与本页全部用这一套；不得出现「最低」「超高」。
- **文案规则（owner 2026-11-07）**：界面上不用「目录」指代模型数据，不出现 `models.json`；模型自带的能力值一律叫「默认值」（例如「不勾选就用默认档位」）。后端错误若带内部文件名，界面不得原样展示（263 已改写这些错误）。
- 接口地址（全宽、等宽字体）；`ProtocolPicker`：**下拉框**，**输入框（触发框）只回显协议名称、不回显路径**；展开的选项每项两行——上面协议名称（正文色），下面路径（等宽、`--quiet` 更浅）；当前项打勾；前三项（Anthropic Messages · /v1/messages、OpenAI Chat Completions · /chat/completions、OpenAI Responses · /responses），服务商为「自定义」时用分隔线隔开多第四项「自定义协议」；**自定义服务商必选、未选时保存置灰**；内置时默认选中接入点的协议；行里已存其它路径时多出「当前路径」一项（与今天一致，不丢数据）。
- `RequestUrlLine`：「实际请求」+ base url + 高亮路径，随输入实时更新。
- API Key：**原生密码输入框**（照 v4 原型的「编辑模型」抽屉）——`type=password` 显示点阵、右侧图标是**闭眼**；点它切成 `type=text` 显示明文、图标同时换成**睁眼**（再点回点阵与闭眼）；旁边「测试连接」，整行撑满抽屉宽度（输入框弹性、按钮固定宽），没有「更换」按钮。编辑时框里以**库里存的明文 Key**回显（来自 plan 265 的 `api_key`，见其 2026-11-07 的裁定），所以眼睛能真的显示已存的值；键入即替换、留空不改；新建时是空框 + 占位「输入 API Key」。前端不得把这个值写进任何日志、埋点、URL 或错误提示。
- 模型名称：内置时下面一排「常用」chip（目录里该 provider 的前几个），可直接输入；自定义时 placeholder `例如 gpt-4o 或 openai/gpt-4o`，下方一行「原样作为请求里的 model 参数发送。」
- **自定义协议**（照原型「添加 · 自定义协议」，参考 workbuddy）：是「接口协议」**下拉**的第四项（前三项为「Anthropic Messages · /v1/messages」「OpenAI Chat Completions · /chat/completions」「OpenAI Responses · /responses」），不在高级配置里；**只有服务商选「自定义」时才出现这一项**，内置服务商的下拉只有三项。选中后不再拼接任何协议路径，接口地址就是完整请求地址。选中时下拉下方一行说明「直接请求填写的接口地址，不再补全 /chat/completions 路径。适用于代理层或服务端已自行处理协议的场景。」；「实际请求」行改为「实际请求（原样）」+ 接口地址本身；提交 `api_path: /chat/completions` + `url_verbatim: true`。表格 API 路径列与所有下拉第二行对这类行显示灰色「自定义协议」。
- 高级配置（折叠，标题行右侧「展开/收起」，摘要只在有改动时显示「已改 N 项」，没有改动时留空（不写解释性废话））：「支持的输入」是**标题与复选框同一行**的一组复选框（标题在左、四个复选框跟在右侧、一律不换行；多选：图片 / PDF / 视频 / 音频；PDF/视频/音频勾上时引擎原生发送这类附件，plan 269），思考模式、允许关闭思考用 **switch 开关**（不用开/关/默认三态）；复选框与开关都按同一规则：初始状态是该模型在目录里的值（`/api/llm-models/catalog-caps` 或行 DTO 的 `catalog_caps`），保存时与目录值相同就发 `null`（不存覆盖、继续跟随目录），不同才发 true/false；不加「目录：…」说明文字；**思考强度**：档位来源由后端分发（ChatGPT/火山订阅接口，其余一切走 models.dev），界面只看 `efforts_known`；填好接口地址、Key、模型名后取一次 `catalog-caps`，取的过程中「思考强度」下拉显示「查询中…」。`efforts_known` 为真时**不单独显示档位行**（档位就是「思考强度」下拉的选项），也不给勾选——**订阅登录的行同样如此**：ChatGPT 订阅查自己的后端模型列表（`supported_reasoning_levels`）、火山方舟订阅查 `/api/coding/v3/models`、其余查 models.dev；三者都查不到才显示勾选；models.dev 查不到（无条目/无档位/多命中已取第一个仍无）时显示「支持的思考强度」复选（含自定义与内置厂商，如美团/腾讯/BytePlus）；models.dev 查到的（无论内置还是自定义）只给「思考强度」下拉；两处复选的标签都中英双写（极低/低/中/高/最高/极致）。「思考强度」下拉的选项：自定义模型 = 用户已勾的档位（没勾就只有首项「默认」）（首项恒为「默认」）；上下文窗口与最大输出数字框（单位「词元」，占位只写目录里的数值本身，如 `128000`，不加「目录：」前缀）；请求参数：**不是普通表单行，而是独立一整块**（顶部一条标题行：左「请求参数」+ 说明「JSON 对象，原样合并进请求」，右「已格式化」状态与「格式化」按钮；下方整宽编辑器，带行号）。编辑器**复用前端已有的 `frontend/src/components/providers/ParamsJsonField.vue`**（CodeMirror 6 + `basicSetup` + `@codemirror/lang-json` + `monokaiLightHighlight` + `EditorView.lineWrapping`），不新造编辑器；上方加一行工具条：左「请求参数（JSON）」、右「格式化」（`JSON.parse` 后 `JSON.stringify(v, null, 2)` 两空格缩进；解析失败只在工具条显示「JSON 格式有误」，不改原文），格式化时可把 `EditorView.lineWrapping` 保持开启（**自动换行**是既有扩展，不要关掉）；高度约 132px，随内容自动增高；主题色取 `@repo/elements/code-block` 的 monokaiLight 调色板（键 #f92672、字符串 #d88200、数字 #ae81ff、true/false/null #00a8c8、标点 #111111、画布 #fafafa）。 **选项与回显一律中英双写**（`极低 minimal`、`低 low`、`中 medium`、`高 high`、`最高 xhigh`、`极致 max`；英文取档位取值名、等宽小字、比中文浅一档），「支持的思考强度」的每个复选标签同样中英双写。
- **订阅来的行**（`source` 非空，照原型「编辑 · 订阅模型」「编辑 · 订阅登录失效」）：同一个「编辑模型」抽屉，但顶部是**来源卡片**——「来自订阅」+ 订阅名 · 套餐 + 登录状态徽标（`source_state`：已登录 / 登录已失效）+ 按钮：有 Key 的套餐「重新获取 Key」（`POST …/refresh-key`）与「重新登录」，登录类只有「重新登录」（打开订阅登录弹窗并直接到该订阅的第 2 步）；
  下面服务商 / 模型 / 接口地址 / API 路径 / API Key 只读展示（Key 为 `••••` + 末四位 +「套餐 Key」或「账号登录」）；一行提示「要换模型或协议，在订阅登录里重新选择」+「打开订阅登录」；只有高级配置可改，保存只发 `caps`/`params`。
- 保存：409 → 抽屉底部红色块「已经有一条相同的配置」+ 那条的签名 +「去编辑那一条」（关抽屉、翻到那一页、行高亮 2 秒）。

### Step 5: 统一订阅登录（`SubscriptionLoginModal.vue` + `OfferPicker.vue`）

照原型「订阅登录 · 选订阅 / 火山方舟 / OpenCode / 选模型」：步骤条（选择订阅 → 登录 → 选择模型）。
- 第一步列 `/api/subscriptions`（只会有能登录的订阅）：标记、名称、说明、状态徽标（已登录 · N 个模型 / 未登录）。已登录点进去直接到第三步。
- 第二步按状态体的 `flow` 渲染：`device_code`（OpenCode 今天用它；火山方舟由 plan 281 接入同一支：大号验证码 + 打开授权页 + 倒计时 + 等待，授权在浏览器完成后由轮询自动推进，没有粘贴框）、`paste_callback`（ChatGPT：打开授权页 + 粘回地址，沿用今天的文案与校验）。轮询沿用旧页 `CONNECT_POLL_MS`。
- 第三步：`subscriptionOffers` 按套餐分组的多选列表（行用 `ModelSignature`，组内隐藏服务商名，右侧窗口/图片）；`from_static_list` 为真时组头加「可用模型以控制台为准」。
  提交 `provisionSubscription`：409 `key_choice` → 组底部出现「选一把 Key」下拉后重新提交；409 `needs_key_consent` → 组底部一行「账号里还没有 Key，创建一把？」+「创建并添加」。成功后关弹窗、刷新列表。
- 删除旧页面的两个登录按钮与旧登录弹窗。

### Step 6: 所有模型下拉统一用签名

- `ModelChip.vue`：菜单宽 420px，样式照原型「对话 · 模型下拉」（与主代理切换菜单同一版式：名称一行、等宽地址一行）；选项用 `ModelSignature`（`bare`）；≥ 8 项时顶部过滤框（搜模型/服务商/地址）；底部「添加自定义模型」（打开 `ModelFormDrawer` 自定义模式，保存后刷新选项）与「管理模型」（跳 `/settings?tab=models`）。
- `SubagentConfigDrawer.vue`：模型 `Select` 改用 `/api/llm-models/choices`，值为 id，选项与触发器都用 `ModelSignature`（触发器单行可截断，悬停提示完整地址）。
- 记忆的两个模型下拉：选项「当前会话的模型」+ choices，值为 `current` 或 id，同样用签名。
- 新会话页的模型选择（若与 `ModelChip` 不是同一组件）同样处理。

### Step 7: 设置 · 变量（`EnvVarsTab.vue`）

照原型「设置 · 变量」：顶部一行说明「MCP、LSP 配置里的 `${名称}` 从这里取值」+「添加变量」；表格 名称 / 值（····尾号）/ 更新时间 / 操作；新增与修改用弹窗（名称、值；名称非法时字段下红字）。服务端分页。
`SettingsView.vue` 的 `tabGroups` 里加「变量」标签：「全局服务」组最后一个。

### Step 8: 删除旧页面与旧接口、改写 e2e

- 删 `ProvidersView.vue` 及其测试、只被它使用的 `components/providers/*`（`ParamsJsonField` 若被新弹窗复用则移到 `components/models/`）；
- 后端删 `/api/providers` 两条路由、`handleProviders` 与只服务它的类型/函数（`providerRowDTO`、`providerPutRow`、`mergeProviderEntries`、`providerRowsFor` 等，`go vet` 与 `grep` 确认无引用）及对应测试；
- 新 `e2e/settings-models.spec.ts`：新建内置（火山 Coding Plan）→ 列表出现；同四项再建 → 看到重复提示并能跳到已有行；自定义不选协议保存置灰、选协议后保存成功；删除默认被拒；对话页下拉能看到两条同名模型且第二行不同；
- 改写 `e2e/opencode-login.spec.ts` 走订阅弹窗。

**Verify**: `pnpm typecheck && pnpm test && pnpm build` → 全过；两个 e2e 通过；`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → ok。

### Step 9: 截图对照

无头 Chrome 截图原型 18 个场景对应的真实页面状态，逐一对照；报告里列出每个场景的差异（没有就写「无」）。

## Test plan

- 组件单测：`ModelSignature`（bare、无路径）、`ProtocolPicker`（自定义必选）、`RequestUrlLine`（拼接与高亮）、`ModelFormDrawer`（409 显示已有行、Key 不回显、编辑时 Key 不改则请求体不带 `api_key`）、`SubscriptionLoginModal`（三种 flow 的渲染、key_choice 与 consent 分支）、`ModelChip`（同名两项、按 id 判当前、≥8 项出现过滤框）、`EnvVarsTab`。
- 范本：`frontend/src/views/ProvidersView.test.ts`（删除前先读它的 mock 写法）、`frontend/src/components/settings/*Tab.test.ts`。
- e2e：Step 8。

## Done criteria

- [ ] `pnpm typecheck`、`pnpm test`、`pnpm build` 全过
- [ ] `settings-models.spec.ts`、改写后的 `opencode-login.spec.ts` 通过
- [ ] `grep -rn "ProvidersView\|/api/providers\|handleProviders" frontend/src pkg/gateway --include='*.ts' --include='*.vue' --include='*.go'` 无输出（`/providers` 只剩路由重定向一处）
- [ ] `grep -rn "最低\|超高" frontend/src/components/models frontend/src/components/settings/ModelsTab.vue frontend/src/components/chat/EffortChip.vue` 无输出（思考强度标签只用定稿的七个）
- [ ] `grep -rn "目录里\|默认档位\|models\.json" frontend/src/components/models frontend/src/components/settings/ModelsTab.vue frontend/src/locales` 只允许出现「默认档位」（不出现「目录」指代模型数据、不出现 models.json）
- [ ] 左栏导航里没有「模型服务」；`/settings?tab=models` 打开设置 · 模型
- [ ] 添加/编辑模型是右侧抽屉；编辑图标打开的抽屉标题是「编辑模型」（组件测试断言标题文本）
- [ ] 自定义模型：models.dev 命中（含多命中取第一个）→ 只给「思考强度」下拉；未命中 → 显示「支持的思考强度」复选
- [ ] `grep -rn "已保存" frontend/src/components/settings/ModelsTab.vue frontend/src/components/models` 无输出（API Key 列只显示 `••••` + 末四位）
- [ ] `grep -rn "console.log" frontend/src/components/models frontend/src/components/settings/ModelsTab.vue` 无输出（明文 Key 不进日志/埋点）
- [ ] `grep -rn "provider}/\${option.model}\|modelKey(" frontend/src` 无输出（不再用 provider/model 当值）
- [ ] 报告含 18 个场景的截图对照差异清单
- [ ] `plans/README.md` 本行已更新

## STOP conditions

- 原型未确认。
- plan 265 的接口缺少原型需要的数据（例如行的「来源」）：在报告列出，**不要**自己改后端接口语义（只允许本计划 Step 8 的删除）。
- ai-elements-vue / shadcn-vue 里找不到可用的 Combobox 组合（Popover + Command）——停下报告，不要引入新组件库。

## Maintenance notes

- `ModelSignature` 是「模型怎么显示」的唯一实现；以后任何模型选择/展示都用它，保证四项都在。
- 订阅弹窗按 `flow` 渲染：后端新增订阅只要 flow 是已知三种之一，网页零改动；新 flow 需要在 Step 5 加一支。
- 「模型」「变量」是全局配置，在设置页；旧地址 `/providers` 只做重定向。以后新增的跨主代理全局页面同样放进设置页。
