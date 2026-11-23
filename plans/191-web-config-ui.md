# 191 — 网页设置页按已确认原型重做：配置只存数据库、按组保存、凭据只露后四位

- **基线**：`06f1bc57`（dev），并假定 203–208、215、216 已落地（它们提供本计划要用的后端字段与语义）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / MED
- **依赖**：203、204、205、206、207、208、215、216 DONE。与 202、194–201 无硬依赖。
- **原型（owner 2026-10-31 已确认，实现 1:1 还原）**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`。字段清单、标签文字、说明文字、单位、范围、默认值、分组、显隐条件以原型里的 `FORMS`、`TBSW`、`LSPM`、`PRESETS` 常量为准；顶部的「第八版 / 配置来源」说明卡与场景按钮栏只属于原型，不实现。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：先在浏览器里把原型的每个场景点一遍（场景按钮：审批与沙箱、保存流程、会话、联网工具、语言服务器参数、语言服务器、共享技能、配置文件、不存在的配置项、YAML 语法错误、模型服务、模型服务弹窗、MCP 凭据、编辑主代理、窄屏、无沙箱、对话输入框、跑批项目设置、无沙箱·输入框）。逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 191 那一行的状态改为 DONE 并写一句结果。
>
> 本文 §0–§7 是执行依据；文末「附录 A」是 2026-10-30～31 的逐轮裁决记录，**冲突时以 §0–§7 为准**。

## 0. owner 裁决（终版，执行期不得改）

**通用**
1. 面向 C 端（含不懂开发的白领），配置精简；**不显示任何英文配置键**；中文界面不中英混排；计划与界面里不提 Codex。
2. **没有「默认」选项**：布尔两态开关、枚举单选，默认值预先选中；不做三态。
3. **每组单独保存**：改动后组底出现保存条「取消 / 保存」；保存成功提示「已保存，下一轮生效」。改过的行左侧有「未保存的改动」竖线。
4. **库里一定有值**（207 建库写入全部默认值）：页面一律显示库里存的值，不用「留空＝默认」的占位。明确表示的取值显示中文：联网搜索服务 `none`＝「不启用」、网页抓取 `builtin`＝「内置抓取」、记忆模型 `current`＝「当前模型」。
5. 单字段组省略行名；单组标签不显示组标题；例外见各页（「跑批项目」组显示项名）。
6. **设置页的配置项都是全局默认值**，主代理或项目自己的配置可以覆盖（本期按需实现：已有的 MCP、技能、子代理、模型，以及审批预设、跑批启用沙箱两例）。
7. 「技能推荐」「语言服务器」总开关放在各自页面工具栏右侧，**拨动即保存**（不走保存条）。
8. 所有表格列表服务端分页（含总数）；新建 / 编辑表单用弹窗，MCP 与子代理页用右侧抽屉。
9. 组件优先 ai-elements-vue，其次 shadcn-vue 原生组件，都没有才自己写。

**配置来源**
10. 原型里出现的所有配置项**只存数据库**（`fb_config`），只在网页与终端读写；forebrain.yaml 里写了也不认。
11. forebrain.yaml 只放：`gateway.http_addr`、`gateway.auth.token`、`credentials.chatgpt`（以及 209 落地后的 `database.url`）——白名单单一来源是 207 的 `FileSupportedKeys()` 与 `GET /api/config/file-schema`。

**设置页标签**：常规〔外观 / 会话 / 审批与沙箱 / 子代理 / 联网工具〕· 全局服务〔MCP / 语言服务器 / 钩子 / 定时任务 / 记忆 / 共享技能〕· 高级〔配置文件 / 运行状态〕。英文界面「联网工具」写 Web tools。

12. **审批与沙箱**：
    - 「审批预设」：「默认」「完全访问」两项（「只读」已删，203），组标题旁说明「只对办公创作、创意编程项目生效」；语义是**新建会话的默认审批模式**（建会话时拷贝进会话，203）。
    - 「沙箱可写工作区」（额外可写目录、允许联网）只在选「默认」时展开；「允许联网」在 `sandbox_network_configurable=false` 时不显示（Windows 或无沙箱，206 / 215）。
    - 「跑批项目」组：表单项「启用沙箱」（默认开），**显示项名、无说明文字**；它是新建跑批项目的默认值（216）。
    - **无沙箱**（`approval_presets_visible=false`，215）：整个「审批与沙箱」标签不出现；直接访问时跳到「外观」。
13. **会话**：「上下文压缩」（自动压缩阈值 50–90 %、交接提示词、网络重试上限、流空闲超时）、「上下文余量」（上下文窗口、提醒余量 %、规则注入上限等，以原型为准）、「护栏」（单条消息上限、拦截片段——说明写明是「你发的消息」）、「词元估算」（六项）。**交接提示词**：输入框显示库里的原文；右上角「恢复默认」只把输入框回显为当前内置提示词、**不落库**，点保存才写入；内容已等于内置原文时按钮置灰。
14. **子代理**：子代理功能、并行上限。
15. **联网工具**：「联网搜索」（服务单选含「不启用」、各家 Key、Tavily 搜索深度）、「网页抓取」（服务、DeepSeek Key、允许内网地址）。
16. **记忆**：「记忆功能」开关的项名用**组标题字体**，说明「记下你的偏好与要点，供以后的会话使用；仅对办公创作和创意编程类项目生效。跑批项目无记忆功能」；**开关打开才显示下面的「模型」「检索与整合」「会话处理」三组，关闭即收起**（跟随未保存的开关值）；「会话处理」组标题旁的长说明照原型；底部「重置记忆」始终显示。
17. **语言服务器**：工具栏右侧总开关；参数（原型「语言服务器参数」场景）；服务器列表与「添加服务器」（以原型为准）。
18. **钩子、定时任务、共享技能、运行状态**：照原型；共享技能工具栏右侧「技能推荐」开关。
19. **配置文件**：编辑**磁盘上的 forebrain.yaml**；保存提示「已保存，重启后生效」；白名单外的键（已取消的、页面在管的、拼错的）画红色波浪线、悬停「不存在」，**照常保存**、读取时静默忽略；**只有 YAML 语法错误才拒绝保存**，编辑器上方提示「没有保存」+「第 N 行 …」（缺少冒号、缩进不能用 Tab、引号没有闭合）。
20. **凭据只露后四位**：模型服务 API Key、联网搜索 / 网页抓取 Key、MCP 的 Bearer token 与 OAuth 客户端密钥 / 访问令牌 / 刷新令牌——显示 `••••••••` + 后四位；点「更换」变为密码输入框，留空即不改，「取消」恢复遮住。**接口只回「是否已设置 + 后四位」**，库里照存明文。
21. **菜单栏页面**：模型服务（Key 遮住）；MCP 抽屉（字段中文、凭据遮住）；主代理下拉每行铅笔 → 弹窗改显示名与描述（名称只读；main 与当前主代理仍不能删）。
22. **对话输入框（会话页与技能工坊共用）**：审批预设选择框两项；**无沙箱时固定选中「完全访问」、置灰、不能改**，不加说明文字。
23. **跑批项目空间 → 设置**：「启用沙箱」开关，无说明文字；无沙箱时不显示（216）。

## 1. 交付

- 后端：通用设置读写接口（§3.1）；配置文件接口改为读写磁盘文件（§3.2）；模型服务与 MCP 的凭据遮住（§3.3）；对话 / 设置用到的环境字段汇总（§3.4）。
- 前端：设置页标签结构与 11 个标签按原型重做（§4）；模型服务、MCP 抽屉、主代理铅笔弹窗（§4）；对话输入框预设选择框的置灰态；跑批项目空间设置开关。
- 文案：中英文 locale 同步。
- 测试：Go 接口测试、vitest、受影响的 e2e。

## 2. 现状事实（已对基线核实）

- **设置页**：`frontend/src/views/SettingsView.vue:24-34,71-96`——三组：常规〔外观 / 审批 / 子代理〕、服务〔MCP / 语言服务器 / 钩子 / 定时任务 / 记忆 / 共享技能〕、高级〔配置文件 / 运行状态〕；组件在 `frontend/src/components/settings/`：`AppearanceTab`、`SettingsApprovalTab`（`forebrainApi.approvalDefault` / `saveApprovalDefault`）、`SettingsSubagentsTab`（`subagentDefaults` / `setSubagentDefaults`）、`McpTab`、`LspTab`（`lspPage` / `lspSetEnabled` / `lspRestart` / `lspInstall` / `lspResetRecommendations`）、`HooksTab`（`hooksPage` / `hooks` / `saveHooks`）、`CronSettingsTab`（`cronSettings` / `saveCronSettings`）、`MemorySwitchesTab`（`memorySettings` / `updateMemorySettings` / `resetMemories`）、`SettingsSharedSkillsTab`（`skillsPage` / `skillsToggle` / 下载 / 删除）、`ConfigTab`（`configFile` / `saveConfigFile`）、`RuntimeStatusTab`（`/api/runtime`）。**没有「会话」「联网工具」标签**；联网搜索与网页抓取今天在网页上无处可配。
- **配置文件接口**：`pkg/gateway/api_extra.go:371-372` 路由，`5352` 起 `handleConfigFile`——GET 返回**合成文档**（`RenderConfigDocument`：文件的 gateway 段 + 库里全部段，凭据明文）；PUT 解析整份 YAML、拒绝改启动段、逐段写库。与 §0 第 19 条不符。
- **按段写入**：`api_extra.go:5302-5307` `withConfigSection(ctx, section, fn)` → `Env.UpdateRootSection`（读改写一个段，带值域校验）；`configBadRequest` 给 400。没有按路径写多个字段的通用接口。
- **模型服务**：`api_extra.go:5772-5779` `providerRowDTO` 带 `api_key` **明文**返回（188 的做法）；前端 `frontend/src/views/ProvidersView.vue:468,537,752` 还留着 `apiKeySet` / `keyEdit` 的「已保存 / 更换」逻辑（188 之前的形态）。
- **MCP 抽屉**：`frontend/src/components/mcp/McpServerDrawer.vue:184-185,261` 字段旁显示英文键名（`headers`、`client_secret`）、凭据明文。
- **主代理改名**：`pkg/gateway/agents_api.go:670-671` 已支持改 `DisplayName` / `Description`（`agents_api_test.go:373-374`）。
- **对话输入框**：`frontend/src/views/ChatView.vue:540`、`frontend/src/views/WorkshopView.vue:118` 都用 `frontend/src/components/chat/ApprovalPresetPicker.vue`。
- **运行时语义由前置计划提供**：审批预设两项与「新会话默认」（203）；`compact.auto_compact_percent`、`warn_remaining_percent` 等（204）；记忆一个开关（205）；**`sandbox_network_configurable`（206）已作废**——2026-11-01 执行前核对：该字段按 215 §3.3 与 `approval_presets_visible` 合并为一个字段（`pkg/gateway/api_extra.go:5394-5401` 注释「One field for the whole section on purpose — the 允许联网 switch lives in it」，`api_extra.go:5401` 只回 `approval_presets_visible`），所以 **grep 不到它不要 STOP**；「允许联网」行与整个「审批与沙箱」标签同受 `approval_presets_visible` 控制；默认值登记、`GET /api/config/file-schema`、`FileRoot`（207）；`approval_presets_visible`、无沙箱强制完全访问（215）；`batch.sandbox_default`、`fb_projects.batch_sandbox`（216）。执行前用 `grep` 逐个确认这些字段与接口存在；缺哪个就 STOP（说明前置计划没落全）。

## 3. 后端

### 3.1 通用设置接口

- `GET /api/settings` → 

```json
{
  "values":  { "compact.auto_compact_percent": 90, "agents.defaults.web_search.provider": "tavily", "...": "..." },
  "secrets": { "agents.defaults.web_search.tavily.api_key": { "set": true, "last4": "Y1s8" } },
  "meta":    { "approval_presets_visible": true, "sandbox_network_configurable": true,
               "builtin_handoff_prompt": "<assembly.DefaultPrompt 原文>" }
}
```

- `PATCH /api/settings`，请求体 `{"values": {path: value, ...}, "secrets": {path: "新值", ...}}`——一个组的保存就是一次请求。服务端按路径归到所属的 Root 段，**同一请求涉及的段在一个事务里**依次 `UpdateRootSection`（全部成功或全部不写）；`secrets` 里没出现的凭据路径保持原值，出现且为非空字符串则替换（空字符串视同未出现）。失败返回 400，体为 `{"path": "<出错的路径>", "message": "<中文原因>"}`，前端把原因显示在该行下。
- **路径登记表（单一来源）**：Go 侧 `pkg/gateway/settings_paths.go` 一张表：路径 → 所属段、Go 字段访问（按 yaml 标签反射）、是否凭据、值类型。表里的路径＝原型 `FORMS` / `TBSW` / `LSPM` 里出现的全部 `F('<path>', …)`；原型里的 `@preset` 对应路径 `approval_preset`（203 的新段）；`project.*` 不在表里（项目字段走项目接口，§3.4）。覆盖测试：表里每个路径都能在 `config.Root` 上解析到字段；凭据路径的 GET 永不出现明文。
- 原来各页专用的表单接口（`approval-default` 的写、`subagentDefaults`、`cronSettings`、`memorySettings` 的写）**改走本接口后删除**（清死代码）；列表 / 增删改类接口（钩子、技能、语言服务器服务器列表、MCP）保留。`approval-default` 的写由「审批预设」组改走 `PATCH /api/settings` 的 `approval_preset` 路径。

### 3.2 配置文件接口（改写 `handleConfigFile`）

- GET：返回磁盘文件原文 `{"path": "...", "yaml": "<文件内容>"}`；文件不存在返回空串。
- PUT：只做 **YAML 语法**检查（`yaml.v3` 解析成节点即可）；语法错 → 400，体 `{"line": N, "message": "缺少冒号|缩进不能用 Tab|引号没有闭合|…"}`（行号从 1 起）；语法对 → 原子写文件（照 `pkg/process/config_import.go` 的 `writeFileAtomic0600`），**不写库**，返回 `{"saved": true}`。白名单外的键不报错。
- 删除 `RenderConfigDocument` 等只为旧合成文档存在的代码（若再无调用者）。
- 白名单：`GET /api/config/file-schema`（207）。

### 3.3 凭据遮住

- 模型服务：`providerRowDTO` 的 `api_key` 不再出现在 GET 里，改为 `api_key_set bool`、`api_key_last4 string`；PUT 的 `api_key` 字段：缺省或空串＝保持原值，非空＝替换（`api_extra.go:6203` 一带已有「指针为 nil 即保持」的写入逻辑，核对后沿用）。
- MCP：服务配置的 GET 对 `headers` 里 `Authorization: Bearer …` 的值、OAuth 的 `client_secret` / `access_token` / `refresh_token` 同样只回 `{set, last4}`；PUT 缺省 / 空串＝保持。找到 MCP 服务读写处理函数（`grep -rn "client_secret" pkg/gateway pkg/mcp --include='*.go'`）逐个改。
- 后四位：取字符串末 4 个字符；不足 4 个字符时返回空串（只显示 `••••••••`）。

### 3.4 环境字段与其它

- 网页启动需要的环境字段：`approval_presets_visible`、`sandbox_network_configurable` 放在 `GET /api/settings` 的 `meta`；对话页需要的 `approval_presets_visible` 若对话页不读设置接口，就放进对话页已在读的引导接口（执行时选一个，两边同源同一个函数）。
- 跑批项目 `batch_sandbox`：用项目读写接口（216 已加）。
- 主代理改名：沿用 `agents_api.go` 的更新接口。

## 4. 前端

### 4.1 表单框架

- 新建 `frontend/src/lib/settingsSchema.ts`：照原型 `FORMS` / `TBSW` / `LSPM` / `PRESETS` 写成同形的描述（组 id、标题、说明、字段路径、标签、类型 `bool|num|enum|text|area|list|pairs|access|model|secret`、单位、范围、`clamp`、提示、显隐条件）——**文字逐字照抄原型**。
- 新建通用组件（放 `frontend/src/components/settings/form/`）：`SettingsGroup`（组标题、说明、保存条、未保存竖线、错误行）与 `SettingsField`（按类型渲染 shadcn-vue 的 `Switch`、`Input`、`Select`、`RadioGroup`、`Textarea` 等；数字带单位与范围提示、超范围标红；`secret` 类型实现 §0 第 20 条）。保存时把该组改动的路径收拢成一次 `PATCH /api/settings`。
- 「保存流程」场景（原型）是行为基准：改动 → 保存条出现 → 保存中按钮禁用 → 成功提示 2.6 秒后消失；取消恢复库里的值。

### 4.2 设置页结构

`SettingsView.vue` 的分组与标签按 §0 改；新增「会话」「联网工具」两个标签组件；「审批」改名「审批与沙箱」；`approval_presets_visible=false` 时不渲染该标签。窄屏（390px）时子导航变横向标签条（原型「窄屏」场景）。

### 4.3 各标签（以原型为准，逐个 1:1）

| 标签 | 组件 | 要点 |
|---|---|---|
| 外观 | `AppearanceTab`（保留） | 对照原型核对 |
| 会话 | 新 `SettingsSessionTab` | 四组；交接提示词「恢复默认」用 `meta.builtin_handoff_prompt` |
| 审批与沙箱 | 改写 `SettingsApprovalTab` | 预设两项 + 组说明；「沙箱可写工作区」随「默认」展开（展开 / 收起动画照原型 `.cond`）；「允许联网」按 `sandbox_network_configurable`；「跑批项目」组显示项名 |
| 子代理 | 改写 `SettingsSubagentsTab` | 两项走 `PATCH /api/settings` |
| 联网工具 | 新 `SettingsWebToolsTab` | Key 用 `secret` 字段 |
| MCP | `McpTab`（保留） | 抽屉见 4.4 |
| 语言服务器 | 改写 `LspTab` | 工具栏总开关拨动即保存；参数组；服务器列表照旧接口 |
| 钩子 | `HooksTab`（保留，对照原型核对） | — |
| 定时任务 | 改写 `CronSettingsTab` | 执行记录保留走 `PATCH /api/settings` |
| 记忆 | 改写 `MemorySwitchesTab` | 项名标题字体；开关驱动三组折叠；「重置记忆」照旧 |
| 共享技能 | `SettingsSharedSkillsTab`（保留） | 工具栏「技能推荐」拨动即保存 |
| 配置文件 | 改写 `ConfigTab` | 现有 `FileEditor`（CodeMirror 6）加 `@codemirror/lint` 的 `linter()`：白名单外最外层键整行波浪线、悬停「不存在」；语法错行波浪线 + 原因；保存失败时编辑器上方「没有保存」+「第 N 行 …」；`@codemirror/lint` 加进 `frontend/package.json` 直接依赖 |
| 运行状态 | `RuntimeStatusTab`（保留，对照原型核对；无沙箱时不出现沙箱行，215） | — |

### 4.4 菜单栏页面与其它

- 模型服务（`ProvidersView.vue`）：表格 Key 列与编辑弹窗用 `secret` 表现；读 `api_key_set` / `api_key_last4`。
- MCP 抽屉（`McpServerDrawer.vue`）：字段名改中文（去掉英文键名小字）；Bearer token 与 OAuth 三项用 `secret` 表现。
- 主代理下拉：每行铅笔 → 弹窗改显示名、描述（原型「编辑主代理」场景）。
- 对话输入框（`ApprovalPresetPicker.vue`，会话页与技能工坊共用）：两项；`approval_presets_visible=false` 时只有「完全访问」一项、`disabled`（原型「无沙箱·输入框」场景）。
- 跑批项目空间 → 设置：「启用沙箱」开关（原型「跑批项目设置」场景），读写项目的 `batch_sandbox`；无沙箱时不渲染。

## 5. 测试

- Go（放被测文件同名 `_test.go`）：`GET /api/settings` 不含任何凭据明文、含 `last4`；`PATCH` 跨两个段一次保存要么都写要么都不写；凭据缺省保持原值；值域错误返回 400 与路径；路径登记表覆盖测试；配置文件 PUT——语法错 400 带行号、白名单外键照常保存且不写库、文件原子写；模型服务与 MCP 的 GET 无明文。
- vitest：`settingsSchema.ts` 与原型字段一致（写一个测试读原型 HTML、抽出全部 `F('<path>'` 与标签文字，与 schema 比较——原型是验收基准）；`SettingsGroup` 保存条状态机；`secret` 字段「更换 / 取消 / 留空不改」；记忆开关驱动折叠；审批预设选「完全访问」时收起沙箱组；`approval_presets_visible=false` 时标签与选择框的表现；配置文件 lint 的三类语法错误与「不存在」。
- e2e：只跑受影响的 spec（`settings*`、`providers*`、`mcp*`、对话输入框相关）；新增一条 e2e：会话页把「自动压缩阈值」改成 70 保存 → 刷新仍为 70；配置文件写一个拼错的键保存成功、刷新后波浪线仍在。

## 6. 完成判据

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/process/... -count=1 -p 2   # 全 ok
go vet -tags fts5 ./...                                                             # 无输出
cd frontend && pnpm typecheck && pnpm test                                          # 全绿
cd frontend && pnpm exec playwright test <受影响的 spec>                             # 全绿
grep -rn "api_key\"" pkg/gateway/api_extra.go | grep -v "api_key_set\|api_key_last4"   # GET 结构里不再有明文 api_key 字段（PUT 结构除外，人工核对）
```

**1:1 还原核对**：用 Playwright 对实现与原型的同名场景各截一张图并排放进 README 结果（`frontend/e2e` 里已有截图写法照抄）；文字、顺序、分组、显隐逐项一致。

## 7. 维护注意

- 原型是验收基准：今后改设置页，先改原型、owner 确认后再改实现与 `settingsSchema.ts`。
- 新增一个存库配置项：207 的默认值登记表 + §3.1 的路径登记表 + `settingsSchema.ts` 三处一起改（覆盖测试会提醒前两处）。
- 凭据类字段一律走 `secret`，接口永不回明文。

## 8. STOP 条件

- §2 最后一条列出的前置字段 / 接口有缺失。
- 某个原型字段在 `config.Root` 里找不到对应字段（前置计划改名或删了它）。
- 跨段事务写在 `UpdateRootSection` 现有结构下做不到全有或全无。
- MCP 的凭据存储结构使「保持原值」无法按字段做到。
- 原型与 §0 有冲突（以 §0 为准前先报告）。

---

# 附录 A：裁决记录（2026-10-30～31，历史；冲突时以上文 §0–§7 为准）

> 其它计划里引用的「191 §8.x」就是本附录的「A-8.x」小节。

## A-8. 原型与核实更正（2026-10-30 起；owner 2026-10-31 已确认原型；已据此重写正文 §0–§7）

owner 同日两条追加要求：①目标是 forebrain.yaml 里除 TUI/网关启动必需项以外的配置**全部**入库、TUI 与 web 两端都能配；②**归属**——主代理级配置复用菜单栏现有页面，项目级配置复用项目空间标签页，全局配置复用设置页。

### A-8.1 原型

`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`（frontend-design 技能；外框固定浅色；视觉取自现有实现 `frontend/src/assets/main.css` 的 token）。12 个场景按钮 + 可交互：

- **设置页（全局）**：子导航 常规〔外观 / 审批与沙箱（原「审批默认」扩）/ 权限档案（新）/ 子代理〕· 会话与工具〔会话（新）/ 工具（新）/ 功能开关（新）〕· 全局服务〔MCP / 语言服务器（加「参数」弹窗）/ 钩子 / 定时任务 / 记忆（原「记忆开关」扩成整段）/ 共享技能〕· 高级〔启动配置（原「配置文件」改只读）/ 运行状态〕。
- **菜单栏（主代理级 `agents.definitions.<id>`）**：模型服务 API Key 列与弹窗明文；MCP 抽屉请求头、client_secret、access_token、refresh_token 明文；主代理下拉每行加铅笔 → 弹窗改 `display_name`/`description`（现无页面承载）。
- **项目空间**：不改——forebrain.yaml 没有项目级的段（项目 MCP/LSP 仍是仓库里的项目文件，秘密扫描照旧）。
- **字段行的统一语言**：品牌色圆点＝已设置（存在 fb_config 里）；虚线框＝选「默认」时实际生效的值；行左竖条＝未保存；非布尔字段已设置时行尾「恢复默认」图标。控件：三态（开/关/默认，shadcn-vue `ToggleGroup`）、标准单选组（含「默认」项）、数字框（单位 + 范围，`clamp` 字段失焦按上限存并提示，非 clamp 越界即时标红）、明文凭据框（带复制）、行编辑（列表 / 允许拒绝 / 两列）、标签输入（`TagsInput`）。
- **保存**：一组 = 一个配置路径，组底保存条只在有改动时展开；成功「已保存，下一轮生效」2.6 s 后收起；后端拒绝原文显示在保存条（弹窗里显示在页脚上方）。单字段组不重复字段名；单组标签不加组标题（owner 2026-10-18 规则）。

### A-8.2 核实更正（基线 `5c6a7d34` + 工作区，逐条打开代码确认）

1. **§2.1 矩阵三处「已有」不成立**：
   - `PermissionsView` 改的是工具权限规则（`POST /api/permissions/updates` → `safety.PermissionUpdate`，`pkg/gateway/api_extra.go:961`），**不是** Root 的 `permissions`（档案）/`default_permissions`——web 无任何入口。
   - `MemoriesView` 是记忆文件列表；设置页「记忆开关」只覆盖 3 个开关，`memories` 段其余 10 项（`search_top_k`、各上限、两个模型）无入口。
   - `LspTab` 是运行状态表；`lsp` 段参数（`max_servers`/`idle_timeout`/`request_timeout`/`diagnostics.*`/`recommendations`）与 `servers` 定义无入口。
2. **矩阵漏列**：`agents.defaults` 下 6 块——`context_inject`、`execution.max_parallel_subagents`、`guardrails`、`token_estimate`、`web_search`（含各家 api_key）、`web_fetch`——web 与 TUI 都没覆盖（`pkg/tui/config_panel_schema.go` 的 agents 只登记了 4 项）；`approval_policy.granular`（5 个布尔）同样漏列。**190 的描述符表需同步补齐**，否则「两端都能配」不成立。
3. **后端契约缺口（触发 §7 逃生口）**：工作区里的 `/api/config` PUT（`pkg/gateway/api_extra.go:5382-5417`）收**整份 YAML 文档**，逐段全量写、请求体里缺的段会被删——没有按段、更没有按路径的写接口。web 的分组保存（尤其 `agents.defaults.*`、`features.network_proxy` 这类与其他页面共享同一段的子路径）若走整文档 PUT，会覆盖模型服务/子代理/MCP 页同时写入的 `agents.definitions`。需要在 188 补一个**按路径写**的端点（如 `PUT /api/config/sections/{path}`，服务端在 `withConfigSection` 事务内只替换该路径），本计划「不改后端」的边界随之改为「只消费该端点」。
4. **「配置文件」标签与 §4「不做 YAML 在线编辑器」冲突**：`ConfigTab.vue` 现在就是整文件 YAML 编辑器，且文案「通过后立即生效」与每轮同步语义不符；原型改为只读「启动配置」。
5. **措辞**：web 现有文案用「下一轮」（`frontend/src/locales/index.ts` 两处），190 写的是「已写入数据库，下一 turn 生效」；原型统一用「已保存，下一轮生效」。

### A-8.3 待 owner 拍板（原型「需要你拍板」卡同列）

1. 「配置文件」YAML 编辑器下线，改只读「启动配置」？
2. 主代理 `display_name`/`description` 放菜单栏主代理下拉的铅笔弹窗？
3. 模型服务表格 API Key 列直接显示明文（单行截断、悬停全文）？
4. 生效提示统一「已保存，下一轮生效」，TUI `/config` 同一措辞？
5. 启动配置里的网关令牌照原样显示明文？

### A-8.4 owner 第二轮审稿裁决（2026-10-30，原型已按此改为第二版）

背景：forebrain harness **面向 C 端**（开发者 + 不懂开发的白领），配置必须尽可能精简。

1. **界面不出现原始英文配置键**（中英文页面都一样）：字段名旁、组标题旁的灰色小字配置键全删；MCP 抽屉现有的 `.mcp-key` 灰字一并删除，`client_id`/`client_secret` 等英文字段名改中文（客户端 ID / 客户端密钥 / 访问令牌 / 刷新令牌 / 令牌地址 / 授权范围）；单选项一律中文（全部令牌 / 不含固定前缀、受限 / 完全、基础 / 深入、错误 / 警告 / 信息 / 提示、内置、不启用）。**后端校验错误不得原样显示**（原文带 `lsp.idle_timeout` 这类键）：后端拒绝须带字段路径，前端按描述符映射成「字段名 + 中文范围说明」，原文不上屏。
2. **以下项只在 forebrain.yaml 手配，web 与 TUI 都不显示不暴露**：`approval_policy`、`approvals_reviewer`、`auto_review`、`sandbox_mode`、`sandbox_workspace_write.go_cache_mode`、`sandbox_workspace_write.exclude_tmpdir_env_var`、`sandbox_workspace_write.exclude_slash_tmp`（后两项为同日追加）、`windows`。界面上的「沙箱可写工作区」只剩额外可写目录、允许联网两项。harness 只从 forebrain.yaml 读它们——**不入库、程序不写**。
3. **审批默认中文只写「只读 / 默认 / 完全访问」**：现有 `SettingsApprovalTab.vue` 的 `bothNames`（「只读 · Read Only」）改为只用当前语言。
4. **沙箱可写工作区跟随审批默认**：只有选「默认」（工作区可写沙箱）时显示；选「只读」「完全访问」时收起（按草稿值即时收起/展开，带高度过渡）。
5. **不要「默认」选项**：布尔改为两态开关（现有 `SwitchRow`/shadcn-vue `Switch`），单选组去掉「默认」项；默认值是哪个就预先选中哪个。原第一版的「已设置圆点 / 默认虚线框 / 恢复默认图标」随之删除，只保留「未保存竖条」。改动判断按**生效值**比较（存储值，缺省时取默认值），把开关拨回默认值即视为无改动。

**跨计划影响（执行 191 前须先落到上游计划）**：
- **187**：§0 第 3 条「YAML 留存最小清单」与 §1.1「16 个 Root 段」须改——`approval_policy`、`approvals_reviewer`、`auto_review`、`sandbox_mode`、`windows` 五段留在 YAML（与 gateway 同类：用户手编、重启或下一次合成时读取），`sandbox_workspace_write` 入库但 `go_cache_mode`、`exclude_tmpdir_env_var`、`exclude_slash_tmp` 三个字段只取 YAML；存量导入不导这些、重写 YAML 时原样保留；`composeConfig` 对这些键只认 YAML。工作区里 187 的实现若已把它们入库，需要迁移回 YAML 或在合成时忽略 DB 值（**待 owner 定**，见下）。
- **188**：`/api/config` 写入拒收这五段与上述三个字段（同 gateway 段的 400）；「审批默认」写入点见下方待定项。
- **190**：`pkg/tui/config_panel_schema.go` 删除这五段与上述三个字段的描述符，`configFieldBool3` 改两态开关、enum 去掉「默认（不设置）」项，面板不显示配置键；覆盖测试 `TestConfigPanelSchemaCoversEveryRootSection` 改为「每个入库段都有描述符、YAML-only 段不得有描述符」。

**待 owner 拍板（原型「需要你拍板」卡同列）**：
1. ~~「审批默认」的选择存哪~~——**已由 §8.5 裁决取代**。原文：「审批默认」的选择存哪：它今天写的正是 `approval_policy` + `sandbox_mode`（`pkg/safety/permission_types.go:843` 的三个预设），这两项改为只在 YAML 后，建议预设本身单独入库（`fb_config` 新键），YAML 写了审批策略或沙箱模式时以文件为准、这组单选置灰。
2. 是否继续精简（候选，建议也只留 YAML）：权限档案整页（与审批默认重叠）、令牌估算、护栏、上下文余量、远程压缩 v2、网络代理两个危险选项与 Unix socket 规则。
3. ~~187 工作区实现里已入库的这五段：迁回 YAML，还是合成时忽略 DB 值？~~——不考虑老用户，直接按新段集合实现（见 §8.5 第 2 条）。

### A-8.5 owner 第三轮裁决：审批预设取代审批策略与沙箱模式（2026-10-30，原型已改为第三版）

**裁决**：`sandbox_mode` 与 `approval_policy` **不再对用户暴露，forebrain.yaml 里也不能配**；新增配置项「**审批预设**」，用户只能配它（只读 / 默认 / 完全访问，即 `pkg/safety/permission_types.go:843` 的 `read-only` / `auto` / `full-access`），不能再分别配置审批策略与沙箱模式。§8.4 第 2 条名单里的这两项随之从「只在 YAML」改为「彻底不是配置项」；`approval_policy.granular` 一并消失。

**现状事实（2026-10-30 核实）**：
- 终端 `/permissions` 与网页输入框旁的会话审批选择器写的是**会话级**行：状态库表 `fb_session_permission_state(session_id, preset, source, updated_at)`（`pkg/state/session_store.go:3368`），每轮由 `injectSessionPermission`（`pkg/run/permissions.go:1016`）叠到配置默认之上；`/fork` 复制、`/new` 不继承；「完全访问」只在受信任项目生效。**不写任何配置**——本裁决不改它。
- 设置页「审批默认」（`PUT /api/permissions/approval-default`，`pkg/gateway/api_extra.go:8082`）今天把预设拆成三段写：清空 `default_permissions`、写 `sandbox_mode`、写 `approval_policy`。
- 两项都不配时，运行时按项目信任度推导（`pkg/safety/sandbox_config.go:556-602`）：审批——被拒绝信任的项目 `untrusted`，其余 `on-request`；沙箱——信任已有定论且非「Windows 未配沙箱」时 `workspace-write`，否则 `read-only`。`default_permissions` 指定的权限档案优先于沙箱模式（`EffectiveConfig` 558 行；`ApplyToConfig` 为此先清空它，`permission_types.go:922-931`）。

**设计（原型第三版已体现）**：
- 「审批预设」是一个入库配置项：**`fb_config` 表新段 `approval_preset`**（owner 同日确认「预设存在数据库，fb_config 表」），值为 `read-only` / `workspace_write` / `full-access`（owner 同日：`auto` 改名 `workspace_write`）；写入方是设置页与 TUI `/config`，程序只写库、不写 YAML（同裁决④）。界面：设置 → 审批与沙箱 → 「审批预设」单选组，**默认值「默认」预选**（裁决 §8.4-5），无「自定义组合」状态；选「默认」才展开「沙箱可写工作区」。TUI `/config` 同一项、同一选项。
- 生效链（建议，待确认第 3 条）：会话自己的选择（`fb_session_permission_state`）> 审批预设 > 内置默认（「默认」）；项目信任度只收窄、不放宽（与「任何来源可收窄、只有操作者来源可放宽」的权限来源模型一致）。
- `PUT /api/permissions/approval-default` 改为写这一项；`approval_policy`、`sandbox_mode` 两段从 `fb_config` 段集合、TUI 描述符、`/api/config` 写入面全部移除；`Root` 里的两个字段降为运行时派生值（由预设 + 信任度算出，永不持久化）。

**待 owner 拍板**：
1. **权限档案**（`default_permissions` + `permissions` 档案表）能绕过预设单独指定沙箱，与「只能配审批预设」冲突。建议整块去掉（网页「权限档案」标签、TUI 描述符、forebrain.yaml 都不再支持；内置的只读 / 工作区 / 完全访问三档由预设内部选用）。备选：只留 forebrain.yaml 手配、且仅在预设为「默认」时生效。
2. ~~老用户迁移~~——**已裁决：不考虑老用户（owner：「没有老用户」）**。`approval_policy` / `sandbox_mode` 直接从配置结构里删除，不做换算、不做兼容提示；§8.4 待定项 3（187 已入库五段迁回还是忽略）同理作废——直接按新段集合实现，不写迁移。
3. **信任度收窄**：预设是上限，未信任 / 未判定项目照今天的推导收紧，「完全访问」只在受信任项目生效（与会话级同一道闸）。

**预设 id 改名（owner 2026-10-30）**：「默认」预设的 id `auto` 改为 **`workspace_write`**。今天这个 id 由 `safety.PresetDefault`（`pkg/safety/permission_types.go:814`）定义，且是三处共用的线上值：`fb_session_permission_state.preset` 列、`/api/permissions/session-preset` 与 `/api/permissions/approval-default` 请求体、前端 `ApprovalPresetPicker.vue:73` / `SettingsApprovalTab.vue:68` 的选项值。界面上的名字仍是「默认」，只改 id；按「不考虑老用户」不迁移已存的会话行。待 owner 确认：只改 `approval_preset` 的值，还是三处共用 id 一起改（建议一起改，一套词汇）；另两个 id 是否同步改成下划线（`read_only` / `full_access`）。

### A-8.6 owner 第四轮裁决（2026-10-30，原型已改为第四版）

1. **预设 id 三处一起改、统一下划线**：`read_only` / `workspace_write` / `full_access`。改动面：`pkg/safety/permission_types.go:812-816` 三个常量（`PresetDefault` 宜改名 `PresetWorkspaceWrite`）、`fb_session_permission_state.preset` 存值、`/api/permissions/session-preset` 与 `/api/permissions/approval-default` 请求体、前端 `ApprovalPresetPicker.vue:73` 与 `SettingsApprovalTab.vue:68` 的选项值、`fb_config.approval_preset` 的值；界面名不变。
2. **权限档案整块去掉**：`Root.Permissions`（`permissions` 段）与 `Root.DefaultPermissions`（`default_permissions`）从配置结构删除——网页、TUI、forebrain.yaml 都不再支持；三个内置档案（`pkg/config/permission_profiles.go:499`）留作预设的内部实现；`normalizeSandboxConfig` 的 `default_permissions` 分支（`pkg/config/sandbox.go:31-44`）与 `ApplyToConfig` 清空它的那行随之删除。
3. **项目信任度照旧只收紧、不放宽**：审批预设是上限；未信任 / 未判定项目按 `pkg/safety/sandbox_config.go:556-602` 今天的推导收紧；`full_access` 只在受信任项目生效（与会话级 `injectSessionPermission` 同一道闸）。
4. **继续精简**：第一轮候选全部改为只在 forebrain.yaml——`agents.defaults.token_estimate`、`agents.defaults.guardrails`、`agents.defaults.context_inject`、`compact.remote_compaction_v2`、`features.network_proxy.dangerously_allow_non_loopback_proxy` / `dangerously_allow_all_unix_sockets` / `unix_sockets`。第二轮（原型第四版已按此画，**待 owner 确认**）：`agents.defaults.execution.max_parallel_subagents`；`compact` 整段与 `agents.defaults.network_retry_max` / `stream_idle_timeout_seconds`；`tools.command_rewrite`、`agents.defaults.web_fetch.allow_private_ip`；`features.exec_permission_approvals` / `request_permissions_tool`；`features.network_proxy` 整组；`memories` 的 `dedicated_tools` / `disable_on_external_context` / `search_top_k` / `max_raw_memories_for_consolidation` 与五个记录处理项；`lsp` 除 `recommendations`、`diagnostics.after_edit` 外的全部参数与 `servers`。精简后设置页标签：常规〔外观 / 审批与沙箱 / 子代理 / 联网〕· 全局服务〔MCP / 语言服务器 / 钩子 / 定时任务 / 记忆 / 共享技能〕· 高级〔配置文件 / 运行状态〕；「技能推荐」「语言服务器」总开关挪到共享技能页、语言服务器页工具栏右侧，拨动即保存（同子代理页开关先例）。
5. **「配置文件」YAML 编辑器保留**：用户在网页里编辑**磁盘上的 forebrain.yaml**，保存前校验，成功提示「已保存，重启后生效」（189 删除热更新后，文件只在启动时读）。这是裁决④「程序只写 fb_config」的唯一例外：写文件的是用户自己的编辑动作。**连带修订 188 W3**：`/api/config` GET 回到返回磁盘文件原文（不再返回合成 Root），PUT 写文件、不再逐段写库；入库配置一律走 §8.2-3 的按路径写端点。待定：文件里写了已取消的项（审批策略、沙箱模式、权限档案）或页面管理的项时，保存拒绝并用中文说明去哪改（原型「配置文件报错」场景已画）。
6. **状态库迁移规则保留**：`fb_config` 已随 `202ed4f2` 提交（schema v24），所以本族要带一次有版本号的数据迁移（取执行时 `stateSchemaVersion+1`，事务内完成、失败整体回滚）：(a) `fb_session_permission_state.preset` 值改名（`read-only`→`read_only`、`auto`→`workspace_write`、`full-access`→`full_access`）；(b) 删除不再入库的段行：`approval_policy`、`sandbox_mode`、`approvals_reviewer`、`auto_review`、`windows`、`permissions`、`default_permissions`；(c) 从仍入库段的 `value_json` 里剔除改为只在 YAML 的字段（第 4 条两轮清单 + `sandbox_workspace_write` 的 `go_cache_mode` / `exclude_tmpdir_env_var` / `exclude_slash_tmp`）。「不考虑老用户」只意味着不把这些旧值换算成新配置、不搬去 YAML；旧行旧字段照迁移规则清理干净。

### A-8.7 owner 澄清与补充（2026-10-30）

1. **187 的「导入数据库并重写 YAML」整条保留**（owner 同日最终裁决，取代一度的「不碰文件」说法）：首次启动照 187 §3.3 把页面管理的项导入 `fb_config`、留一份 `forebrain.yaml.pre-db-<时间戳>` 备份、**重写 forebrain.yaml**；重写时**把不能通过 forebrain.yaml 配置的内容全部删掉**——只保留「文件支持项」白名单（网关启动项 + 只在文件里配的高级项，即第 2 条与 §8.6 第 4 条的清单），其余一律删：页面管理的项（已导入库）、已取消的项（审批策略、沙箱模式、权限档案——**不换算成审批预设**，「不考虑老用户」）、白名单外的未知键。只在首次导入时做一次；之后用户在「配置文件」里再写的不支持项不再改写文件，读取时静默忽略、编辑器显示「不存在」。状态库迁移规则不变（§8.6 第 6 条的 v25 数据迁移照做）。
2. **配置文件编辑器的提示与保存**：
   - 文件**不支持的配置项**（已取消的、页面在管的、拼错的——即不在「forebrain.yaml 支持项」白名单里的键）：该行画红色波浪线，悬停提示「不存在」；**照常保存**，读取时**静默忽略**。只标最外层不支持的键，嵌套在它下面的行随它一起，不重复标。
   - **只有 YAML 语法错误才拒绝保存**：语法错的那一行同样画波浪线、悬停显示原因（如「缺少冒号」「缩进不能用 Tab」）；点保存时编辑器上方提示「没有保存」+「第 N 行…」，文件不动。
   - 实现：现有 `FileEditor`（CodeMirror 6）加 `@codemirror/lint` 的 `linter()`——诊断即波浪线、悬停即提示，都是它的原生能力；`@codemirror/lint` 今天只是 `codemirror` 的间接依赖（node_modules 里是 6.9.7），须加进 `frontend/package.json` 成为直接依赖。白名单是前端描述符表之外的第二张表（「文件支持项」），与 187 合成逻辑读取 YAML 的字段集合必须同源——建议后端出一个只读端点下发白名单，前端不手抄。
   - 后端读取同理：`config.Load` 对白名单之外的键静默忽略（不报错、不中断启动）。
3. **计划与界面里不提外部产品对齐**（owner 2026-10-30）：本节以前的相关说明已删。

### A-8.8 owner 第六轮（2026-10-30，原型已改为第六版）

1. **第二轮精简清单先画出来再定**：原型第六版把第二轮候选画回原位，以只在原型里出现的黄色「候选」标记标出（子代理并行上限；「会话」「功能开关」两个标签整体；联网的允许内网地址；记忆的专用工具、外部上下文时停用与「检索与整合」「记录处理」两组；语言服务器参数里八项与「添加服务器」）。框架工具栏「第二轮候选」开关可切换「画出候选 / 精简后」两种样子。**§8.6 第 4 条第二轮清单仍待 owner 逐项定**。
2. **主代理铅笔弹窗确认**：菜单栏主代理下拉每行加铅笔，弹窗改 `display_name`（显示名）与 `description`（描述）；名称只读；main 与当前主代理仍不能删。
3. **凭据遮住、只露后四位**（推翻 188「GET 回明文」与本计划 §2.1 的明文回显）：模型服务的 API Key（表格列与编辑弹窗）、联网搜索与网页抓取的 Key、MCP 请求头里的 Bearer token，一律显示为 `••••••••` + 后四位，像密码框一样不可读、不可复制；点「更换」变为 `type=password` 输入框，留空即不改，「取消」恢复遮住。这正是 188 之前模型服务页的旧形态（`providers.keySaved` / `providers.changeKey` 文案仍在 locale 里），直接恢复。
   - 建议接口同步：GET 只回「是否已设置 + 后四位」（恢复 `api_key_set` / `api_key_hint` 一类字段），浏览器拿不到完整值；PUT 收到空值即保持原值。
   - ~~(a) MCP OAuth 几项是否同样遮住~~——**已裁决：和其他凭据一样都遮住**（客户端密钥 `client_secret`、访问令牌 `access_token`、刷新令牌 `refresh_token`，原型已改）；~~(b)「加密」是否也指库里加密存储~~——**已裁决（owner 2026-10-30）：任何鉴权凭证、API Key、密码密钥、token 在数据库里必须存明文**（与 187 §1.3 一致）；「加密」只指界面遮住 + 接口只回后四位。

### A-8.9 owner 第七轮（2026-10-31，原型已改为第七版）

1. **保存条按钮「放弃改动」改为「取消」**。
2. **模型的 token 中文统一叫「词元」**（单位、提示文案、locale）。鉴权意义上的 token（访问令牌、刷新令牌、网关令牌）仍叫「令牌」——按语义区分，待 owner 确认这一理解。
3. **删除「阈值口径」配置项及相关代码**：自动压缩阈值只按全部词元计。删除面：`CompactSection.ModelAutoCompactTokenLimitScope`（`pkg/config/config.go:73-74`）及其校验；接线 `pkg/run/runner.go:1142,1153`、`pkg/run/compaction.go:367`；`assembly.Service.LimitScope`（`pkg/assembly/service.go:61`）与 `normalizedScope`、所有 `body_after_prefix` 分支（同文件 245、266、415、875-876 行）；`pkg/state/usage.go:229` 一带为该口径服务的计量；TUI 描述符（`pkg/tui/config_panel_schema.go`）；相关测试。清理后不得残留只为该口径存在的代码。
4. **远程压缩不再是配置项**（YAML 与库都不可配）：`compact.remote_compaction`、`compact.remote_compaction_v2` 从配置结构删除；语义改为**对 ChatGPT 模型固定开启**——以客户端是否实现 `llm.ContextCompactor` 为准（今天只有 OpenAI Responses 客户端，`pkg/llm/openai/responses_llm.go:877`），其余模型照旧本地摘要。
   - **已裁决（2026-10-31）：只留 v2，v1 作死代码删除**——`CompactModeRemoteV1`、`/responses/assembly` 路径、`UseRemoteV2`（`pkg/config/config.go:93-95`）与 `RemoteV1` 接线（`runner.go:1140`、`compaction.go:365`、`assembly/service.go:354-358`）一并删。
   - **风险（须在实现计划里给出规则并测试）**：`config.go:75-81` 写明远程压缩结果是服务商私有的不透明条目，只有同一家服务商能回放；固定开启后，会话在远程压缩后切到非 ChatGPT 模型会拿着用不了的检查点。现有换模型压缩与回退逻辑（`assembly/service.go:168-180` 的 `shouldRetryWithCurrentModel`）须核实能覆盖；**已裁决（2026-10-31）：下一次请求要发给别家服务商时，先在本地重新压缩一次**（用本地摘要生成可移植的检查点，再发请求）；须有测试覆盖「ChatGPT 上远程压缩 → 切到 DeepSeek 继续对话」。
5. **交接提示词默认回显内置提示词**：输入框直接显示 `assembly.DefaultPrompt`（`pkg/assembly/service.go:28`）原文；与内置文本相同即视为未设置、不入库；读接口须下发内置文本（或随描述符下发）。
6. **三个开关的含义**（原型已加一句说明，仍是第二轮候选）：
   - 命令改写 `tools.command_rewrite.enabled`（默认开）：命令送审前自动补上只影响呈现的参数（关分页器、关颜色），你批准的就是实际运行的命令（`pkg/config/config.go:211-224`、`pkg/run/permissions.go:757-770`）。
   - 命令执行审批 `features.exec_permission_approvals`（默认关）：允许 shell 调用带 `with_additional_permissions` 临时申请沙箱外的文件或网络权限，由你批准；关闭时这种调用直接报错（`pkg/tool/shell_tool.go:1250-1258`）。
   - 权限申请工具 `features.request_permissions_tool`（默认关）：注册 `request_permissions` 工具，让模型提前申请后续操作要用的文件或网络权限；批准后的授权也能让上面那类调用免于再问（`pkg/tool/request_permissions.go:42`、`pkg/tool/registry.go:162,210`、`shell_tool.go:1260-1262`）。
7. **后台跑批项目不使用记忆、不可配置**：记忆功能开关只对办公创作类、创意编程类项目生效。今天 `pkg/memory` 没有按项目类型的判断（无 `ProjectTypeBatch` 分支），须新增：跑批项目的 runner 上记忆读、写、专用工具一律关闭。
8. **删除「使用记忆」「生成记忆」「专用工具」三个配置项及相关代码**：`features.memories` 一个开关同时管三者。删除面：`MemorySection.UseMemories/GenerateMemories/DedicatedTools` 及校验；`pkg/memory/settings.go` 的 `ReadPathEnabled`/`GenerationEnabled`/`DedicatedToolsEnabled` 收敛为 `Enabled`，`Settings` 结构的对应 JSON 字段；网关记忆设置接口；网页 `MemorySwitchesTab`（三开关变一个）；TUI `/memories` 设置浮层与 `UpdateMemorySettings`（`pkg/tui/overlays.go:4465`、`pkg/tui/commands.go:2218`、`pkg/tui/chat_slash.go:1380-1410`）；相关测试。
9. **「外部上下文时停用」的含义**（`memories.disable_on_external_context`，默认关，仍是第二轮候选）：开启后，一个会话只要用过外部内容——联网搜索、网页抓取的结果（`pkg/tool/registry.go:152,158`）、MCP 工具的结果（`pkg/run/runner.go:399`）、OpenAI 内置联网搜索（`pkg/llm/openai/responses_llm.go:1399`）——就被标为「受污染」，不再用来生成记忆；若它已经参与过记忆整合，会触发一次重新整合把它的贡献撤掉（`pkg/memory/settings.go:85-105`）。

**连带**：§8.6 第 6 条的 v25 数据迁移，剔除字段清单加上 `compact.model_auto_compact_token_limit_scope`、`compact.remote_compaction`、`compact.remote_compaction_v2`、`memories.use_memories`、`memories.generate_memories`、`memories.dedicated_tools`；「文件支持项」白名单同步去掉这些键。第 3、4、7、8 条是运行时代码删改，超出本计划「只改网页」的范围——待第二轮清单定稿后，拆成独立实施计划（编号接 201 之后）。

### A-8.10 owner 第八轮（2026-10-31）

1. **命令改写、命令执行审批、权限申请工具三个配置项删除，改为固定开启（always-on），不可关闭、不可配置**，相关配置代码与死代码清理：
   - `tools` 段整段删除：`ToolsSection` / `CommandRewriteSection` / `UseRewrite`（`pkg/config/config.go:207-224`）；`pkg/run/permissions.go:764` 与 `pkg/tool/shell_tool.go:1983` 的开关判断改为恒真后删除分支（命令送审前一律改写）。
   - `features.exec_permission_approvals`、`features.request_permissions_tool` 两字段删除：`EffectiveFeatures()` 里的对应项；`execPermissionApprovalsEnabled`（`shell_tool.go:1256-1258`）及报错文案里的配置键；`requestPermissionsToolEnabled`（`registry.go:209-211`）与 `additionalPermissionsPreapproved` 里的开关判断（`shell_tool.go:1260-1262`）。
   - **跑批项目的排除保留**：`registry.go:161` 的 `!batchProject(rt)` 继续把权限申请工具挡在跑批项目外（无人值守、没人批准）；shell 调用的 `with_additional_permissions` 在跑批项目里放不宽沙箱——现状已保证：跑批任务的 shell 边界是**整体替换**而非收窄（`pkg/tool/shell_tool.go:467-479` 的 `batchShellBoundary`：可写只剩任务目录、可读只剩任务目录与共享技能目录、项目根与 home 拒读；`batchSandboxConfig` 强制工作区可写沙箱，`shell_tool.go:313-320,777-788`），且跑批调用永不升级出沙箱（`pkg/run/runner.go:435-448`）。固定开启后只需补一条测试证明这点不变。
   - 行为变化（如实写进执行计划的验收）：两项过去默认关，固定开启后模型可在执行命令时临时申请沙箱外权限、可调用 `request_permissions` 提前申请——两者都走现有审批（审批预设决定是否要问你）；工具列表多出 `request_permissions`，提示词缓存前缀随之位移一次。
   - 连带：TUI 描述符、「文件支持项」白名单、v25 迁移（删 `tools` 段行；剔除 `features` 里两个字段）。
2. **远程压缩**：只留 v2、v1 删除；跨服务商前先本地重新压缩（见 §8.9 第 4 条，已改为已裁决）。

### A-8.11 跑批会话禁止生成记忆（owner 2026-10-31）

**现状（根因）**：跑批项目只在读取侧关了记忆（无记忆工具、记忆指令与引用不进请求：`pkg/tool/registry.go:104`、`pkg/tool/session_tools.go:82`、`pkg/run/runner.go:1064-1068`），生成侧没有：
- 新会话的 `fb_sessions.memory_mode` 取进程级默认（`pkg/process/open.go:493` → `memory.SessionModeForConfig`，`pkg/memory/settings.go:30-35`；配置变更时 `pkg/process/config_apply.go:80` 同步默认），与项目类型无关——记忆开着时跑批会话也是 `enabled`。
- Stage 1 挑会话（`pkg/memory/jobs.go:149-153` `ClaimStage1JobsForStartup`）只按 `memory_mode`、`memory_source`、空闲时间筛，不看 `fb_sessions.project_type`；且任一项目的 runner 启动流水线都会扫到同一主代理下所有项目的会话。Phase 2 选材（`pkg/memory/store.go:270`）同样只看 `memory_mode`。

**修法（修根因，不在查询里加防御分支）**：
1. 建会话时按项目类型定 `memory_mode`：`project_type='batch'` 一律写 `disabled`，与配置无关（`fb_sessions.project_type` 出生即定，`pkg/state/schema.sql` 81 行）。
2. 单会话记忆开关的两个写入口拒绝把跑批会话改成 `enabled`：TUI `/memories`（`pkg/tui/chat_slash.go:1422`）、网关 `memories_api.go:88`（都经 `memory.Store.SetThreadMemoryMode`，`pkg/memory/store.go:112-130`，在这里拒绝即可）。
3. 状态库迁移（并入 §8.6 第 6 条的 v25）：`UPDATE fb_sessions SET memory_mode='disabled' WHERE project_type='batch'`；删除这些会话已产出的 Stage 1 结果，并为受影响的作用域排一次 Phase 2 重新整合（照 `MarkPollutedByExternalContext` 的撤回做法，`pkg/memory/settings.go:85-105`），把它们对已整合记忆的贡献清掉。
4. 测试：跑批会话建出即 `disabled`；开着记忆跑一个跑批任务后 Stage 1 不认领它；迁移把旧跑批会话改为 `disabled` 并撤回其贡献；`SetThreadMemoryMode` 对跑批会话返回错误。

与 §8.9 第 7 条（跑批项目不使用记忆）合并为同一实施项。

### A-8.12 「外部上下文时停用」固定开启（owner 2026-10-31）

`memories.disable_on_external_context` 删除配置、固定开启、不可修改，清理死代码：
- **always-on 语义**（owner 原话）。`MemorySection.DisableOnExternalContext` 及生效值字段、默认值、归一化（`pkg/config/config.go:240,260,281`、`pkg/config/load.go:834`）删除；`memory.MarkPollutedByExternalContext`（`pkg/memory/settings.go:89-105`）去掉 `cfg.EffectiveMemories().DisableOnExternalContext` 条件——记忆功能开着时，用过外部内容（联网搜索、网页抓取、MCP 工具结果、OpenAI 内置联网搜索）的会话一律不再用于生成记忆，已参与整合的照旧触发撤回。
- 连带：TUI 描述符、「文件支持项」白名单删该键；v25 迁移从 `memories` 段剔除该字段。
- 行为变化写进验收：过去默认关，现在经常联网或重度用 MCP 的会话不再攒记忆（owner 已知悉此代价，安全优先）。

### A-8.13 配置来源定稿（owner 2026-10-31，原型已改为第八版）

**裁决**：原型稿里保留的**所有**配置项都必须放进数据库表，只能读写数据库，**不支持在 forebrain.yaml 里配置**。据此：§8.6 第 4 条的「第二轮候选」**全部留在页面**（不再挪进文件，原型去掉了候选标记与开关）；§8.4 待定项「同一段按字段分来源」随之成立——页面出现的字段只认库，页面没有的少数高级字段只认文件。

**只存数据库（页面与 TUI `/config` 读写；文件里写了也不认，编辑器标「不存在」）**：
- 审批与沙箱：`approval_preset`；`sandbox_workspace_write.writable_roots`、`network_access`。
- 子代理：`agents.defaults.enable_subagent`、`agents.defaults.execution.max_parallel_subagents`。
- 联网：`agents.defaults.web_search.*`（服务、各家 Key、Tavily 搜索深度）、`agents.defaults.web_fetch.provider`、`deepseek.api_key`、`allow_private_ip`。
- 会话：`compact.model_auto_compact_token_limit`、`compact.prompt`、`agents.defaults.network_retry_max`、`agents.defaults.stream_idle_timeout_seconds`。
- 网络代理：`features.network_proxy.enabled`、`mode`、`proxy_url`、`socks_url`、`allow_upstream_proxy`、`allow_local_binding`、`domains`。
- 语言服务器：`features.lsp`、`lsp.recommendations`、`lsp.diagnostics.after_edit`、`lsp.max_servers`、`idle_timeout`、`request_timeout`、`diagnostics.wait_ms`、`min_severity`、`max_per_file`、`max_files`、`late_delivery`、`lsp.servers`（「添加服务器」）。
- 记忆：`features.memories`、`memories.extract_model`、`consolidation_model`、`search_top_k`、`max_raw_memories_for_consolidation`、`max_unused_days`、`max_rollout_age_days`、`max_rollouts_per_startup`、`min_rollout_idle_hours`、`min_rate_limit_remaining_percent`。
- 共享技能：`features.skill_offer`。定时任务：`cron.retention_days`。
- 菜单栏页面（主代理级）：模型服务、子代理、MCP、主代理显示名与描述（`agents.definitions.<id>.*`）。

**只在 forebrain.yaml（「文件支持项」白名单，页面与 TUI 都不显示）**：`gateway.*`；`approvals_reviewer`、`auto_review`、`windows`；`sandbox_workspace_write.go_cache_mode`、`exclude_tmpdir_env_var`、`exclude_slash_tmp`；`agents.defaults.context_inject`、`guardrails`、`token_estimate`；`features.network_proxy.dangerously_allow_non_loopback_proxy`、`dangerously_allow_all_unix_sockets`、`unix_sockets`。

**连带**：
- 187 首次导入：上面「只存数据库」的项从文件导入库；重写 forebrain.yaml 时只留白名单项（§8.7 第 1 条）。
- v25 迁移（§8.6 第 6 条）：库里各段剔除的字段改为「白名单项 + 已删除的项」；§8.6 第 4 条里第二轮清单的字段**不再剔除**（它们留在库里）。
- 190：TUI 描述符覆盖上面「只存数据库」的全部项。
- 白名单由后端下发给配置文件编辑器（§8.7 第 2 条），与 187 合成逻辑读取文件的字段集合同源。

**待确认（owner 2026-10-31 要求先画出来看）**：上下文余量（`agents.defaults.context_inject` 四项）、护栏（`agents.defaults.guardrails` 四项）、词元估算（`agents.defaults.token_estimate` 六项，原「令牌估算」按「词元」译法改名）画进会话标签，网络代理的两个危险选项与 `unix_sockets` 画进网络代理标签，均标「待确认」并配用途说明。确认放页面 → 移入上面「只存数据库」清单、从文件白名单删去；不放 → 维持只在文件里。

### A-8.14 按百分比压缩、库里一定有值（owner 2026-10-31，原型已改）

1. **自动压缩阈值改为按百分比配置**：新字段 `compact.auto_compact_percent`（整数，50–90，初始 90，超过 90 按 90 存），替换绝对词元数 `compact.model_auto_compact_token_limit`（删字段、删代码）。现状：阈值＝模型有效输入窗口 × `ProactiveCompactFraction`(0.90)，配置的绝对数只在更小时生效（`pkg/state/usage.go:247-268` `CalculateTokenBudgetWithOptions` 的 `ExplicitLimit`；读取点 `pkg/run/runner.go`、`pkg/run/compaction.go`、`pkg/tui/chat_turn.go:1574`）。改后：`autoThreshold = effective × percent / 100`，`TokenBudgetOptions.ExplicitLimit` 删除。页面说明写明「调低会更频繁压缩、缓存命中率下降」（`usage.go:18-24` 的缓存命中率警告）。
2. **所有存库的配置项必须显示数据库里当前存的值**——不再用「留空＝默认」的占位显示；页面上没有空着的存库字段。
3. **初始化数据库时把全部默认值写进配置表**：每个「只存数据库」的字段（§8.13 清单）在建库时就有明确值（布尔、枚举、数字、地址、空列表；「当前模型」「不启用」「内置抓取」这类取值存成它们的明确表示，页面照常显示中文名；子代理并行上限按本机 CPU 核数减一算出后写入）。连带：
   - **187**：首次从 forebrain.yaml 导入的触发条件不能再是「`fb_config` 全表为空」（建库即已写入默认值）——改为独立的导入标记；顺序＝写入默认值 → 用文件里的值覆盖 → 重写文件。
   - **状态库迁移**（§8.6 第 6 条的 v25 及以后）：迁移后的老库与新建库一致——缺的默认值要补齐；以后每新增一个存库配置项，都要同时登记默认值并在迁移里补齐。
   - **190**：TUI `/config` 同样显示库里的值。
   - **交接提示词（已裁决 2026-10-31）**：库里照写提示词原文（建库时写入内置原文）。字段右上角加「恢复默认」按钮：点击只把输入框**回显为当前版本的内置提示词**（`assembly.DefaultPrompt`），**不落库**；组底保存条展开，用户点「保存」才写入。内容已等于内置原文时按钮置灰。读接口须下发当前内置原文供按钮使用。TUI `/config` 同一字段提供同样的恢复项。

### A-8.15 提醒余量、拦截余量改为百分比（owner 2026-10-31）

- `agents.defaults.context_inject.warn_remaining_tokens` / `block_remaining_tokens`（绝对词元数，默认 25000 / 13000，`pkg/config/load.go:788-793` 归一化、`pkg/assembly/hook.go:144-160` 判定）改为 `warn_remaining_percent` / `block_remaining_percent`：**按剩余占比算**——剩余可用词元低于模型有效输入窗口的这个百分比时标为吃紧 / 告急。初始 12% / 6%（原绝对值按 20 万窗口换算约 12.5% / 6.5%），范围 1–50，拦截余量不得大于提醒余量（后端校验，原型已画拒绝提示）。旧两个字段删除、相关代码清理。
- **与自动压缩阈值口径一致**：分母同为「模型有效输入窗口」——今天两边已都是「输入上限，没有则上下文窗口」（`pkg/llm/catalog.go:154-162` `EffectiveInputLimit`；`pkg/state/usage.go:258-261`）；**统一兜底**：模型窗口查不到时，压缩（今天写死 20 万，`usage.go:251-253`）与提醒 / 拦截（今天用 `context_inject.model_context_tokens`）都改用配置项「上下文窗口」`model_context_tokens`（初始 200000）。词元估算两边已同源（`hook.go` 注释：与压缩判断用同一估算器）。实现时抽一个共用的「有效输入窗口」函数，三处都调它。

### A-8.16 删除「拦截余量」配置与语义（owner 2026-10-31，纳入本期）

`block_remaining`（拦截余量）**配置项与语义完全删除**，清理死代码；§8.15 里与它有关的部分（`block_remaining_percent`、「拦截余量不得大于提醒余量」校验、兜底说明里的「拦截」）随之作废。上下文压力只剩两级：`ok` / `warn`。删除面（已核实）：
- 配置：`ContextInjectConfig.BlockRemainingTokens`（`pkg/config/tools.go:105`）与默认值归一化（`pkg/config/load.go:791-793`）；TUI 描述符；「文件支持项」白名单；v25 迁移从 `agents` 段剔除该字段。
- 判定：`pkg/assembly/hook.go:149-160` 的 `blockAt` 与 `block` 分支（只留「剩余低于提醒余量 → warn，否则 ok」）；`pkg/assembly/engine.go:156-170` `contextPressureForInjectedContext` 的 `block` 档——注入的上下文达到自身上限时改报 `warn`（该函数的另一个调用语义，执行时核实无其它读者后照此处理；有读者依赖 `block` 就 STOP 报告）。
- 显示：网页上下文调试面板 `frontend/src/lib/contextDebug.ts:77-88` 的 `block` → `danger` 分支删除（`ContextSignal` 类型去掉 `block`），对应测试 `contextDebug.test.ts` 更新。网关 `pkg/gateway/session_context_test.go` 只用到 `warn` / `ok`，不受影响。
- 没有任何代码因 `block` 真正拦截请求（仅打标与显示），删除无行为副作用。
- 原型已删「拦截余量」一行与相关校验；「上下文窗口」说明改为「自动压缩与提醒都按这个值算」。

**本期运行时改动的汇总**：§8.9–§8.16 的运行时删改（阈值口径、远程压缩配置与 v1、三个固定开启的开关、记忆三开关合并、跑批不生成记忆、外部上下文时停用固定开启、自动压缩改百分比、提醒余量改百分比、拦截余量删除、输出护栏删除、检索片段上限与注入字符串匹配删除、禁止非本机代理、记忆限流余量删除、记忆增量提取〔含状态库迁移〕、审批复核人与自动复核策略配置删除〔guardian 代码作为死代码保留〕、文件配置项再精简五项、对外地址自动识别、Windows 固定受限令牌沙箱、网络代理整块删除、建库写入默认值）合并为一份独立实施计划（编号接 202 之后），与 187–191 同期执行。

### A-8.17 删除输出护栏（owner 2026-10-31，纳入本期）

**现状（已核实）**：输出护栏是纯启发式——`pkg/safety/permission_rules.go:555-588` 的三条规则：英文注入话术正则 `grSysLeakRe`（`ignore previous instructions` / `disregard the above` / `you are now in developer mode`，名为 sys leak 实则不查系统提示泄露）、`javascript:` 正则、链接协议白名单（只放 http / https / mailto，`data:`、`file://`、`postgres://` 等一律命中）；命中即整段拦下。默认关；开启后网关不再流式显示回答（`pkg/gateway/run_control.go:460-466`）。识别弱、误伤大（开发者常见的连接串、文件链接），owner 裁定**配置项与规则整体删除**，清理死代码：

- 配置：`GuardrailsOutputConfig` 与 `GuardrailsConfig.Output`（`pkg/config/guardrails.go:5-11`）；TUI 描述符、「文件支持项」白名单（原型已改）、v25 迁移剔除 `agents.defaults.guardrails.output`。
- 解析：`Resolved.OutputEnabled` 及 `StrictDefaults` / `ResolveGuardrails` 里的对应行（`pkg/safety/store.go:1006,1022,1038-1040`）。
- 规则本体：`ApplyOutputRail`、`OutputRailEnabled`、`SanitizeOutbound`、`OutputBlockedError`、`grSysLeakRe` / `grJSURLRe` / `grOpaqueURLRe`、`grExtractURLs` / `grExtractHierarchicalURLs` / `grIsSchemeFirst` / `grIsSchemeChar` / `grTrimTrailingURLPunct`（`permission_rules.go:537-549,555-690` 一带，执行时以 `grep` 确认只此一处使用）。**保留** `incBlocked` 与 `guardrailBlockedTotal`——输入护栏也在用（`permission_rules.go:520,530`）。
- 调用点：`pkg/run/llm_middleware.go:700-733` 的 `outputGuard` 与 `WithResultGuard("output-rail", …)`（`WithResultGuard` 若再无调用者一并删，`permission_rules.go:452-454`）；网关 `SanitizeOutbound` 的全部调用（`pkg/gateway/server.go:606,669,767,2520`、`api_extra.go:4558`、`run_control.go:1036`、`channels.go:342`）改为直接用原文；`run_control.go:465` 的「输出护栏开着就不流式」分支删除（回答始终流式显示）。
- 测试：删除只测输出护栏的用例；外部通道发送、网关流式的既有测试改为不再期待 `[withheld: output guardrails]`。
- 原型：「护栏」组删去「输出护栏」一行。
- 行为：默认本就关闭，删除对默认用户无变化；手动开过的用户此后不再有该过滤（不考虑老用户）。外部通道真正需要的防护（如不外发密钥）另行设计，不在本期。

### A-8.18 删除「检索片段上限」配置与注入字符串匹配（owner 2026-10-31，纳入本期）

**现状（已核实）**：「检索片段」实为每轮自动附进上下文的内容——最近读过的文件（`RecentFilesSource`）与 git 状态（`GitContextSource`），由 `pkg/assembly/hook.go:88-109` 逐段交给 `safety.EvaluateRetrieval`（`pkg/safety/store.go:1060-1088`）：空段丢弃；超过 `RetrievalMaxChunk`（默认 120000 字，配置 `agents.defaults.guardrails.retrieval.max_chunk_runes`）丢弃；含 `ignore previous instructions` / `disregard the above` 丢弃。

**改动**：
- 删配置项：`GuardrailsRetrievalConfig` 与 `GuardrailsConfig.Retrieval`（`pkg/config/guardrails.go:6,23-26`）及默认值归一化（`pkg/config/load.go:803-805`）；`Resolved.RetrievalMaxChunk` 及 `StrictDefaults` / `ResolveGuardrails` 里的对应行（`pkg/safety/store.go:1008,1023,1041-1043`）；TUI 描述符、「文件支持项」白名单（原型已改）、v25 迁移剔除 `agents.defaults.guardrails.retrieval`。
- 长度上限改为代码常量（120000 字，固定，不可配），`EvaluateRetrieval` 只做「空段丢弃、超长丢弃」两件事。
- 删除注入字符串匹配（`store.go` 中 `strings.Contains(lt, "ignore previous instructions") || strings.Contains(lt, "disregard the above")` 分支与 `injection_substring` 原因）；相关测试同步删改。
- 原型：「护栏」组只剩「单条消息上限」「拦截片段」两项。
- 并入本期运行时实施计划的汇总清单。

### A-8.19 禁止非本机代理（owner 2026-10-31，纳入本期）

> **已作废**：网络代理整块删除，见 §8.25 第 3 条。下文仅留作记录，不要执行。

**现状（已核实）**：`features.network_proxy.proxy_url` / `socks_url` 是 forebrain 给沙箱命令开的代理的**监听地址**（`pkg/safety/managed_network.go:172,211` `listenProxyURL`）。`dangerously_allow_non_loopback_proxy` 打开时可监听局域网地址（把代理暴露给别的机器）；关着时非本机地址被**静默改写**为 127.0.0.1（同文件 221-229 行）。

**改动（always-off 语义，不可配置、不可修改）**：
- 删配置项 `DangerouslyAllowNonLoopbackProxy`：`pkg/config/network_proxy.go:23,34,103,116,132,226-227,324,334,345,367-368` 一带的字段、归一化、合并与约束；`pkg/config/permission_profiles.go:218,230,248,544,600-601`（权限档案整块删除时一并消失，§8.6 第 2 条）；TUI 描述符（`pkg/tui/config_panel_schema.go:226`）；「文件支持项」白名单（原型已改）；v25 迁移剔除该字段。
- `listenProxyURL` 去掉 `allowNonLoopback` 参数，**永远只监听本机**。
- **代理地址只允许本机**：`proxy_url`、`socks_url` 的主机必须是 `127.0.0.0/8`、`::1` 或 `localhost`——写入时后端校验拒绝（中文原因「代理地址只能是本机地址（127.0.0.1、::1 或 localhost）」），不再静默改写；页面即时标红、保存被拒（原型已画）。
- 两个地址字段补用途说明：代理地址＝「沙箱里的命令经此代理联网；只能是本机地址」，SOCKS 地址＝「SOCKS 代理的监听地址；只能是本机地址」。
- 网络代理组剩下的待确认项：「放行所有 Unix socket」与 Unix socket 规则。

### A-8.20 配置来源最终定稿（owner 2026-10-31，取代 §8.13 的两张清单）

**原型稿里留下的全部配置项只存数据库**——此前标「待确认」的上下文余量（`model_context_tokens`、`warn_remaining_percent`、`max_pre_hook_rules_chars`）、护栏（`input.max_runes`、`input.block_substrings`）、词元估算（`token_estimate` 六项）、「放行所有 Unix socket」与 Unix socket 规则，一并只存数据库、页面与 TUI `/config` 读写，forebrain.yaml 里写了不认。

**forebrain.yaml 只放（「文件支持项」白名单，最终；§8.23 后再去掉 `approvals_reviewer`、`auto_review`）**：`gateway.*`；`windows.sandbox`、`windows.sandbox_private_desktop`；`sandbox_workspace_write.go_cache_mode`、`exclude_tmpdir_env_var`、`exclude_slash_tmp`。其余一切（原型可见的全部，以及菜单栏主代理级页面管理的 `agents.definitions.<id>.*`）只存数据库。

**已从配置中彻底删除（库与文件都不存在）**：`approval_policy`、`sandbox_mode`、`permissions`、`default_permissions`、`compact.model_auto_compact_token_limit`（改为 `auto_compact_percent`）、`compact.model_auto_compact_token_limit_scope`、`compact.remote_compaction`、`compact.remote_compaction_v2`、`memories.use_memories` / `generate_memories` / `dedicated_tools` / `disable_on_external_context`、`tools.command_rewrite`、`features.exec_permission_approvals` / `request_permissions_tool`、`context_inject.warn_remaining_tokens`（改为 `warn_remaining_percent`）/ `block_remaining_tokens`、`guardrails.output`、`guardrails.retrieval`、`network_proxy.dangerously_allow_non_loopback_proxy`、`memories.min_rate_limit_remaining_percent`（§8.21）、`approvals_reviewer`、`auto_review`（§8.23）、`peers.enabled` / `peers.inbound`。

**连带**：187 首次导入 + 重写文件、v25 迁移、190 描述符、配置文件编辑器白名单（原型已改）都以本节为准。

### A-8.21 删除记忆「限流余量」功能与配置（owner 2026-10-31，纳入本期）

**现状（已核实）**：`memories.min_rate_limit_remaining_percent`（默认 25）只在记忆流水线的模型是 ChatGPT 订阅登录时生效——每次后台记忆处理前请求 `https://chatgpt.com/backend-api/wham/usage`，短 / 长两个窗口已用比例都不超过「100 − 余量」才继续（`pkg/run/memory_llm.go:163-207`）；其他服务商不装这道检查；且它按主代理默认模型判断，不看记忆页配置的抽取 / 整合模型（判断对象错位）。owner 裁定**功能与配置一并删除**，清理死代码：
- 配置：`MemorySection.MinRateLimitRemainingPercent` 与生效值、钳制、归一化（`pkg/config/config.go:249,269,290`、`pkg/config/load.go:843`）；TUI 描述符（`pkg/tui/config_panel_schema.go:195`）；v25 迁移从 `memories` 段剔除该字段。
- 流水线：`Pipeline.RateLimitGuard` 字段与 `acquireRateLimit`（`pkg/memory/pipeline.go:41,144-147`）及其两处调用（`pipeline.go:89-91`、`121-123`，删除后直接继续）；依赖注入 `RateLimitGuard`（`pipeline.go:615-618,653-655`）。
- 运行时：`memoryRateLimitGuard`（`pkg/run/memory_llm.go:163-207`）与注入点（`memory_llm.go:54-56`）；`openai.UsageURL`（`pkg/llm/openai/auth.go:32`，唯一使用者就是它）。删后若 `memory_llm.go` 不再用 `net/http` / `io` / `encoding/json` 等导入，一并去掉。
- 测试：删除只测这道检查的用例。
- 原型：记忆页「记录处理」组删去「限流余量」一行。
- 行为：ChatGPT 订阅用户的后台记忆处理不再因额度紧张而暂停（不考虑老用户）。

### A-8.22 记忆增量提取（owner 2026-10-31 选 A，纳入本期）

**问题（已核实）**：会话只在「上次提取之后又有新内容」时才会再被提取（`pkg/memory/jobs.go:206-219` 按 `source_updated_at` / `last_success_watermark` 判重，没变化不重复）；但一旦老会话被继续使用、再次闲置满时长，`runStage1`（`pkg/memory/pipeline.go:247-262`）用 `ListMemoryTranscriptMessages(ctx, thread, 0)` **整段重读**并重新提取（按 `MiddleTokens` 首尾保留截到 ≤32000 词元，`pkg/run/memory_llm.go:282-295`）——断断续续回来的长会话，旧内容被反复送进模型。

**改法**：
1. **记住提取到哪一条**：`fb_memory_stage1_outputs` 加列 `last_message_row_id INTEGER`（`pkg/state/schema.sql:419` 起），提取成功时写入本次读到的最后一条消息行号。**状态库迁移**（并入 v25 或取执行时 `stateSchemaVersion+1`，事务内完成）：给老库加这一列，旧行为 NULL（＝下一次按全量提取一次，之后转为增量）；迁移后老库结构须与新建库逐条一致（`sqlite_master` 比对）。
2. **只读新增部分**：`SessionStore` 新增按行号之后读取的方法（如 `ListMemoryTranscriptMessagesAfter(ctx, sessionID, afterRowID)`，复用 `listAllStoredMessages` 与 `messagesFromStoredRows`，同样跳过压缩边界行）；`runStage1` 在有上次行号时只读之后的消息。
3. **带着上次结果提取**：增量时把上次的 `raw_memory` 与 `rollout_summary` 作为「已有提取结果」一并交给抽取模型，要求输出**合并后的完整新结果**（替换该会话原有的第一阶段结果行，第二阶段整合照旧以该行为输入）。第一阶段系统提示词（`stageOneSystem`）为增量情形补一段说明——提示词是模型可见文本，须遵守仓库「provider-neutral、byte-stable」规则，全量与增量各一份常量，配 golden 测试。
4. **截断**：新增部分超过上限时仍用 `MiddleTokens` 首尾保留；上次结果本身不截断（它已是压缩后的摘要）。
5. **边界情况**：上次提取无产出（`MarkStage1SucceededNoOutput`）→ 增量时「已有提取结果」为空；会话在上次提取后被压缩过 → 边界行跳过，只读边界后的新消息；新增部分为空（仅元信息变化）→ 不调模型，直接把行号与时间戳推进到最新。
6. **测试**：同一会话先全量提取、再追加 3 条消息并闲置 → 第二次只把这 3 条与上次结果送进模型（断言请求体不含旧消息正文）、结果替换原行；老库迁移后旧行 `last_message_row_id IS NULL` 时走一次全量；新增为空时零模型调用。
7. **验收**：真机（glm-5.3，隔离 home）记录一个长会话两次提取的输入词元数，第二次应只与新增量成正比。

### A-8.23 删除审批复核人、自动复核策略配置；guardian 代码作为死代码保留（owner 2026-10-31，纳入本期）

**裁决**：`approvals_reviewer`、`auto_review`（含 `policy`）两个配置项**全部删除**（文件与库都不支持）；**审批复核人永远是 `user`**（由人审批），不支持配置和修改。auto_review / guardian（AI 复核）的代码**只作为死代码留在仓库里**——不删，但没有任何路径能走到。

**现状（已核实）**：`approvals_reviewer=auto_review` 时，需审批的调用交给系统子代理 guardian 判定（`pkg/run/runner.go:579-597`，仅 on-request / granular 模式，`runner.go:875-879`）；`auto_review.policy` 作为附加规则拼进 guardian 提示词（`pkg/safety/runtime.go:1666-1671`）；另有审批卡选项「本轮批准并严格自动复核」`grant_for_turn_with_strict_auto_review`（`pkg/safety/permission_types.go:1021`，处理在 `runner.go:561-576`）也走 guardian。

**改动**：
- 配置：删 `Root.ApprovalsReviewer`（`pkg/config/config.go:125`）及其默认 / 校验 / 归一化（`pkg/config/load.go:104,132-135,627,900`）；删 `Root.AutoReview` 与 `AutoReview` 结构（`config.go:64-66,126`）；`pkg/process/config_db.go:36` 段表里的 `auto_review` 与 `approvals_reviewer`；TUI 描述符；「文件支持项」白名单（原型已改：文件只剩 `gateway.*`、`windows.*`、`sandbox_workspace_write` 的 `go_cache_mode` / `exclude_tmpdir_env_var` / `exclude_slash_tmp`）；v25 迁移删除这两个段的行。
- 运行时（owner 同日修订：**不引入任何「复核人」常量或硬编码 `user`**——复核人这个概念整体消失，审批就是交给人，always-user 语义）：
  - 删除 `autoReviewApprovalsEnabled`（`runner.go:875-879`）及审批流程里由它控制的分支（`runner.go:579-597`）：需要审批的调用直接走现有的「交给人」路径，不再有「谁来复核」的判断。
  - **保留为死代码**：`pkg/safety` 里的 `GuardianReviewer`（复核流程、熔断、`guardianSystemPrompt`、`reviewPrompt` 附加策略的逻辑）原样保留，不被审批流程引用；strict 分支（`runner.go:561-576`，由会话状态 `StrictAutoReviewRunIDs` 触发）因选项不再提供而不可达，原样保留。启动时构造 guardian 客户端并 `SetGuardian`（`runner.go:1005-1016`）的接线：`AutoReview.Policy` 已删，策略参数改为不传（或空串）；「选了 auto_review 却没有模型就启动失败」的分支随配置删除。保留的代码各加一行注释「auto_review 暂不使用（owner 2026-10-31），保留代码」。
- **审批卡不再提供「本轮批准并严格自动复核」**：从 `request_permissions` 的可选决定里去掉 `DecisionGrantForTurnStrictAutoReview`（`permission_types.go:1021`），终端与网页的审批卡随之不再出现该选项（`pkg/tui/overlays.go:278`、`frontend/src/lib/approvalSuggestions.ts:18,158`、`frontend/src/composables/usePendingActions.ts:498` 的显示代码作为死代码保留，或随选项一并不再命中）；后端收到该决定时按「不在可选项内」拒绝。这样 guardian 没有任何入口，成为纯死代码。
- 测试：删 / 改依赖 `approvals_reviewer=auto_review` 与 strict 选项的用例；guardian 自身的单元测试保留（它测的是保留下来的代码）。
- **两端都不暴露「本轮批准并严格自动复核」（owner 2026-10-31 再次明确）**：验收与测试须覆盖——① `pkg/safety` 单测：`request_permissions` 的可选决定列表只有「本轮批准 / 本会话批准 / 拒绝」三项；② TUI 单测：`request_permissions` 审批浮层的选项行里没有该项（`pkg/tui/overlays.go` 现有审批浮层测试照此加断言）；③ 网页 vitest：`approvalSuggestions` 对 `request_permissions` 不产出 `grant_for_turn_with_strict_auto_review`，审批卡不渲染该按钮；④ 网关：提交该决定返回 4xx（不在可选项内）；⑤ 真机：终端与网页各触发一次 `request_permissions` 审批，截图确认无此选项。
- 架构测试：若有「未使用代码」类检查（如 `staticcheck` 的 unused）在 CI 里，给保留的入口加 `//nolint` 或在计划执行时报告，不要为通过检查而删代码。

### A-8.24 文件配置项再精简（owner 2026-10-31，纳入本期）

删除以下五项配置，改为固定行为（不支持配置与修改），清理死代码：
- `gateway.auth.mode` → **固定 token 鉴权**（删除 `none` 无鉴权模式）：`GatewayAuth.Mode`（`pkg/config/config.go:16-19`）、默认归一化（`pkg/config/load.go:634-636,786`）、`gatewayAuthMode` 与 `none` 分支（`pkg/gateway/controlplane.go:34-40,85-90,325`）、`/auth-mode` 等对外返回的 `auth_mode`（`controlplane.go:366`、`api_extra.go:6497`，前端若依赖该字段同步删改）、启动横幅的 `AuthMode`（`serve_run.go:236-245`）。
- `windows.sandbox_private_desktop` → **固定开**：`WindowsSection.SandboxPrivateDesktop` 与 `UseSandboxPrivateDesktop`（`config.go:197-204`），`backend_windows.go:133` 直接传 true。
- `sandbox_workspace_write.go_cache_mode` → **固定 isolated**：字段、`EffectiveGoCacheMode`、`shared` / `disabled` 两条分支（`pkg/tool/shell_tool.go:2030-2045` 一带）删除。
- `sandbox_workspace_write.exclude_tmpdir_env_var` / `exclude_slash_tmp` → **固定可写**（沙箱里 `$TMPDIR` 与 `/tmp` 始终可写）：字段与 `pkg/safety/process.go:522-526`、`backend_darwin.go:86-90`、`sandbox_config.go:331-332` 的条件删除，按「不排除」实现。
- 连带：TUI 描述符、「文件支持项」白名单、v25 迁移剔除、相关测试。

`gateway.public_url` 与 `windows.sandbox` 的裁决见 §8.25。

### A-8.25 对外地址自动识别、Windows 固定受限令牌沙箱、网络代理整块删除（owner 2026-10-31，纳入本期）

**1. `gateway.public_url` 删除，改为自动识别。** 它只用于跑批结束通知里的「查看：」链接（`batchNotifyLink`，`pkg/gateway/channels.go:462-471`）。新规则：
- 登录后的网页每做一次写操作（非 GET/HEAD/OPTIONS、带 `Origin`、已通过 `requestOriginMatchesHost`，`pkg/gateway/controlplane.go:114-127`），网关记下该 `Origin`（协议 + 主机 + 端口），**存库**、重启不丢；只在值变化时写。
- 已记的是外部地址时，本机地址（`localhost`、`127.0.0.0/8`、`::1`）不覆盖它。
- 从没人用网页做过写操作 → 通知不带链接（与今天未配置时相同）。
- 删 `Gateway.PublicURL` 字段与注释（`pkg/config/config.go:8-13`）、相关测试；落点用状态库现有的键值存储或 `fb_config` 的一行运行时记录，**不加新表**（若必须改表结构，照迁移规则写迁移）。

**2. `windows.sandbox` 删除，Windows 固定受限令牌沙箱（unelevated），elevated 代码删除。**
- 删 `WindowsSection` 整个结构（`Sandbox` 与 §8.24 的 `SandboxPrivateDesktop` 都不在了，`config.go:189-204`）、`load.go:142-143` 的取值校验。
- 删 elevated 全部代码：专用账户 `ForebrainSandboxOnline` / `ForebrainSandboxOffline` 的创建与登录（`NetUserAdd` 一带，`backend_windows.go:162-240`）、`reconcileWindowsOfflineFirewall` 与防火墙端口表达式、`windowsElevatedMu` 全局串行锁、`spawnWindowsSandboxProcess` 的 elevated 参数。
- 删「Windows 没配沙箱」的退化分支：`manager.go:64-67`（workspace-write 退成只读）、`manager.go:212-213,236-237`（`windows_sandbox_disabled`）、`sandbox_config.go:577-584` 的 `windowsSandbox` 参数、`process.go:495-499` 里 `windows_sandbox_disabled` / `windows_sandbox_missing_config` 两条说明。Windows 从此与 macOS / Linux 同一套判定：信任过的项目「默认」预设就是可写工作区。
- **跑批在 Windows 上仍按 plans/150 fail-closed**：150 的前提是「Windows 后端做不到跑批要求的独占读集合」（`ExclusiveReadablePaths`，`shell_tool.go:558`）。以前靠 `windows_sandbox_disabled` 让命令失败，这条分支删掉后，执行者必须给跑批加一条显式判定：Windows 上 `ExclusiveReadablePaths` 请求不可用即失败，不得静默放宽成普通沙箱。若发现受限令牌其实能保证独占读集合，**停下报告**，不要自行放开。
- **验收必须在 Windows 真机上做**：工作区可写、工作区外不可写、「允许联网」打开时 `curl` / `git clone` / `npm install` 能直连、多条命令并发不排队、跑批命令直接失败。仓库里没有 unelevated 在真机上跑过的证据。

**3. 网络代理（托管网络）配置、语义、代码全部删除，所有操作系统都不提供。** owner 原话：「沙箱允许联网的开关打开，就可以直连网络，不支持配置任何代理」。
- 删 `features.network_proxy` 整段：`pkg/config/network_proxy.go`（443 行）与测试、`load.go` / `config.go` 引用、`pkg/home/seed.go:99` 种子里的 `network_proxy: false`。
- 删 `pkg/safety/managed_network.go`（2519 行）与 `managed_network_test.go`；`ManagedNetworkCommand`、`acquireManagedNetworkProxy`（`manager.go:368` 一带）、`sandbox_config.go` 的 `ManagedNetwork` 字段；macOS / Linux / Windows 三个后端里接代理的分支（`backend_darwin.go`、`backend_linux.go`、`backend_windows.go` 的 `windowsNetworkPolicy` 只留「联网 / 断网」两态）。
- 删**按域名的联网审批**全链（它只由代理触发）：`NetworkApproval*` 类型与决定（`sandbox_config.go:221-228`、`permission_types.go:587-615` 的协议与规则类型）、`tool.State` 的 `networkSessionDecisions` / `SetNetworkSessionDecision` / 提示钩子（`pkg/tool/state.go`、`shell_tool.go:849-852`）、`turn` 的 `ParseNetworkApproval` / `NetworkResult` / `ValidateNetworkSessionUpdate` 与相关分支（`pkg/turn/approval.go`）、`run` 的 `permissionRequestNetworkTarget`（`runner.go:744-792`）、TUI `promptInlineNetworkApproval`（`chat_surface.go:506-577`）、网关 `promptGatewayNetworkApproval` 与 `network_approval_context` 字段（`gateway/approval.go`、`wsevents.go`、`event/approval_events.go`）、前端 `approvalSuggestions.ts` 的联网建议、TUI 配置面板 `config_panel_schema.go` 里的网络代理描述符。
- `SandboxNetworkInWords`（`process.go:533-545`）只剩两句：能联网 / 不能联网（需审批）。
- **保留**：「允许联网」开关（`sandbox_workspace_write.network_access`，默认开，只存库，开关旁不加说明文案）——开＝沙箱命令直接联网，关＝断网；`request_permissions` 的整体联网授权（`NetworkPermissionProfile`，`permission_types.go:524-560`），它不依赖代理。
- 迁移：v25 删掉 `fb_config` 里已入库的 `features.network_proxy`；历史审批记录里带 `network_approval_context` 的行不改，但网页 / TUI 回看时必须正常显示（按普通命令审批卡显示，卡片里出现原始 JSON 即 bug）。
- **§8.19 整条作废**（「允许非本机代理」删除、`listenProxyURL` 只监听本机、代理地址只收本机地址都随代理一起消失）。

**4. 原型（第八版内改）**：删「网络代理」标签页；「会话与工具」组只剩「会话」，并入「常规」：常规〔外观 / 会话 / 审批与沙箱 / 子代理 / 联网〕；「允许联网」不加说明文案（owner 明确删掉）；「配置文件」白名单只剩 `gateway.http_addr`、`gateway.auth.token`。

**5. Windows 不显示「允许联网」，沙箱命令固定直连**（owner 2026-10-31）。原因：elevated 删除后，受限令牌沙箱做不到真断网——关闭时只是把代理变量指向死地址（`applyWindowsNoNetworkEnvironment`，`backend_windows.go:1305-1325`），只拦得住遵守代理变量的工具，不给一个关了也拦不住的开关。
- 判定按**运行沙箱的那台机器**（`runtime.GOOS`），不是浏览器所在系统：`GET /api/config` 返回一个布尔（如 `sandbox_network_configurable`），网页据此隐藏；TUI `/config` 面板在 Windows 上不列这一行。库里的值不改、Windows 上不读。
- Windows 上沙箱命令一律放行网络（`AllowNetwork` 恒为真，只读预设也一样——原来也只是软断网）；删 `applyWindowsNoNetworkEnvironment`、`windowsNetworkPolicy` 的断网分支与 `Offline` 字段。`SandboxNetworkInWords` 在 Windows 上恒为「能联网」。
- macOS / Linux 不变：照常显示，关闭即真断网。
- Windows 真机验收追加：设置页与 TUI 面板都看不到「允许联网」；库里把它改成关后，沙箱命令照样能联网。

**结果：forebrain.yaml 只剩三项——网关监听地址、登录令牌、`credentials.chatgpt`（ChatGPT 登录文件路径，见 §8.27）。**

### A-8.26 设置标签「联网」改名「联网工具」（owner 2026-10-31）

设置页常规组的「联网」标签（联网搜索、网页抓取）改名「联网工具」，英文界面同步改为 Web tools；原型已改。当前标签：常规〔外观 / 会话 / 审批与沙箱 / 子代理 / 联网工具〕· 全局服务〔MCP / 语言服务器 / 钩子 / 定时任务 / 记忆 / 共享技能〕· 高级〔配置文件 / 运行状态〕。

### A-8.27 `credentials.chatgpt` 留在文件；企业内网（离线）部署（owner 2026-10-31）

1. **`credentials.chatgpt` 保留在 forebrain.yaml**（owner 选择）：ChatGPT 登录文件路径（默认 `<home>/auth.json`），188 之后只用于首次导入旧登录文件与一次性终同步（`pkg/process/config_import.go:68-72`、`config_credential_sync.go:31-37`）。「文件支持项」白名单＝`gateway.http_addr`、`gateway.auth.token`、`credentials.chatgpt`；原型已改。
2. **forebrain gateway 必须支持部署到企业内网**：内网**离线**（不通公网、也不需要公网），有 npm / pip / go 内网镜像源。裁决：不传公司代理；**放行镜像相关环境变量**给子进程（沙箱命令、MCP 本地服务、语言服务器）；**跑批读集合加包管理器配置文件只读**（具体文件，不是整个主目录）；「允许内网地址」保留；离线下各类自动下载必须缓存优先、超时有界、失败降级。另立实施计划（编号接本期运行时计划之后）。
3. **三种内网部署方式**：Linux 主机部署、Docker 部署、K8s 部署都必须支持；**必须生成三份部署说明文档，纳入本期计划**（owner 2026-10-31）。
4. **生成 gateway 的 Dockerfile，纳入本期计划**（owner 2026-10-31）：与三份部署文档同属部署计划。

### A-8.28 多数据库与多副本（owner 2026-10-31，纳入本期）

1. 状态库除 SQLite 外**必须支持 MySQL、PostgreSQL**，仓库里的 SQL 在三种库上都能跑；**forebrain.yaml 必须能配 MySQL / PostgreSQL 的连接地址**——「文件支持项」白名单随之加一个数据库段（具体键名由设计文档定）。
2. 实现方式（owner 同日改需求，取代一度选的 GORM）：**禁止引入 GORM 等任何 ORM**；**SQLite 的 SQL 语句不改**；为 MySQL、PostgreSQL **分别新增**各自语法的 SQL，运行时**判断数据库类型、调用对应语法的 SQL**。全文检索**用各库原生**；**不搬旧数据**（切库即新库）。
3. 部署形态：**多网关共享一个库**——K8s 多副本、共享存储、共享同一个 FOREBRAIN_HOME。
4. 先出设计文档再拆实施计划；本期运行时计划（203–207）先在 SQLite 上落地，多库改造在其后统一转换。
5. 设计 `docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md`；实施计划 209–213；部署（Dockerfile + 三份文档 + K8s 清单）214。容器裁决：镜像带 bubblewrap、挂最小放行 seccomp；镜像自带 git / Node / Python / Go 工具链。

### A-8.29 沙箱有就用、没有就不用；审批预设只剩两档（owner 2026-10-31，纳入本期）

1. **任何类型的项目、任何操作系统、任何部署环境，沙箱都是有就用、没有就不用**；有没有沙箱不是硬性条件。跑批没有沙箱也照常跑；Windows 跑批在受限令牌沙箱里照常跑（读不隔离）。启动时沙箱不可用不再拒绝启动。可用性要实际试跑判断。实施：`plans/215-sandbox-optional.md`；206 §3.3、214 已改。
2. **审批预设只保留「默认」与「完全访问」，无论有无沙箱；「只读」删干净、清死代码；网页与终端一致**。原型已改（审批预设两项）；203 已改（id 迁移里「只读」行改成 `workspace_write`）。信任度未判定时收紧到只读沙箱是信任收紧，不是预设，保留。
3. **没有沙箱时「默认」的语义**：读文件、搜索不问；编辑工具改工作区内文件不问、工作区外要问；每条 shell 命令都要问（命中「总是允许」规则的除外）；批准后的命令直接在机器上跑。「完全访问」都不问。没有沙箱时「允许联网」不显示（关了也拦不住）。

### A-8.30 无沙箱时审批界面全隐藏、自动完全访问；完全访问＝YOLO（owner 2026-10-31，取代 §8.29 第 3 条）

1. **有沙箱**：审批预设只有「默认」「完全访问」两种，网页与终端一致。
2. **无沙箱**：网页与终端**完全不显示审批预设的配置和选项**（设置页「审批与沙箱」整个标签、会话选择器、终端 `/permissions`、`/config` 的审批与沙箱项），对用户完全透明，直接按完全访问运行；**不看项目信任度**；**任何地方都不提示**（含运行状态）；危险 git（同目录有其他代理时）、退出计划、提问照旧问人。强制的完全访问不写库。原型新增「无沙箱」场景。
3. **「完全访问」与 YOLO 等价，YOLO 只是完全访问的别称**：除明确拒绝规则外全部放行（「每次都问」规则也放行），不再受项目信任度限制；`FOREBRAIN_YOLO` 等于强制完全访问。实施：203（等价与删信任闸）、215（无沙箱强制与隐藏）、214（部署文档向运维写明）。

### A-8.31 完全访问的提示与终端 shift+tab（owner 2026-10-31，修订 §8.30 第 2 条的「任何地方都不提示」）

1. **无沙箱**：网页对话输入框（**含技能工坊**）保留审批预设选择框，**固定选中「完全访问」、置灰、不能改**（原型「无沙箱·输入框」场景）；终端 footer 显示完全访问说明。设置页「审批与沙箱」、终端 `/permissions`、`/config` 审批项仍不出现。
2. **有沙箱**：用户开启完全访问时，终端 footer 必须显示完全访问说明；网页保持现状（输入框里的选择框本就显示当前预设）。
3. 终端 shift+tab **对齐 Claude Code 的语义**（在模式之间循环、footer 标出当前模式），**文案不照搬**：暂定 `full access (shift+tab to cycle)`、`plan mode (shift+tab to cycle)`。循环：有沙箱 默认 → 计划模式 → 完全访问 → 默认；无沙箱 完全访问 ↔ 计划模式。实施见 `plans/215-sandbox-optional.md` §3.4、§3.6。

### A-8.32 跑批项目语义、继承语义（owner 2026-10-31）

1. **跑批五条**（实施 `plans/216-batch-project-semantics.md`）：①跑批会话只能由业务系统接口或项目空间「创建任务」产生，网页对话页与终端都不能建、终端不能发消息；②跑批会话在网页与终端只读，对话页「停止 / 继续 / 重新执行」保留；③审批预设对跑批无效——有沙箱且项目启用沙箱：工作区可读写、可加载技能目录只读、FOREBRAIN_HOME（及 home 外的项目目录）其余拒绝读写、其余整个文件系统只读免审批写拒绝（不加凭据拒读名单）、允许联网、无提权工具、主子代理可沟通、永不问用户（无 user_interaction / enter_plan_mode / exit_plan_mode）；无沙箱或项目关闭沙箱：完全访问 + 同样不问用户；④跑批项目没有信任度；⑤有沙箱时设置 → 审批与沙箱新增组「跑批项目」、表单项「启用沙箱」（默认开），跑批项目空间 → 设置「启用沙箱」新建时拷贝全局值、可单独关闭；无沙箱时不显示、无效。原型已加（审批与沙箱的「跑批项目」组〔表单项「启用沙箱」，单字段组例外地显示项名〕、「跑批项目设置」场景；审批预设组注明「只对办公创作、创意编程项目生效」）。
2. **继承语义**：设置页的审批预设＝新建会话的默认审批模式，建会话时拷贝进会话；用户用输入框选择框改的只影响本会话（实施 203 §0 第 2b 条）。
3. **总原则**：设置页的配置项都是全局默认值，主代理或项目自己的配置可以覆盖全局默认值。**本期作为原则、按需实现**（owner 同日）：已有覆盖的项（MCP、技能、子代理、模型）与本期两例（审批预设、跑批启用沙箱）按此语义；其它设置项以后谁需要覆盖再加主代理 / 项目层。

---

# 附录 B：旧版正文（2026-10-30 第一版原型之前写的 §0–§7，已作废，仅留作记录）


- **基线**：`6f68cb21`（dev）＋ **前置：187、188 已落地**。189/190 非硬依赖，但按族序最后执行。
- **状态**：TODO
- **依赖**：187、188。

### 旧 0. 背景

Web 端已有大量配置面（Providers/Permissions/Memories/Mcp 视图 + SettingsView 多 Tab），它们在 188 之后已经在读写 DB 后端但**段覆盖不全**。本计划把 16 个 DB 段全部表单化，YAML 剩余段只读展示，并把稿面（app-preview.html）与实现同步——owner 铁律：**web 验收基准 = app-preview.html 1:1 还原且稿面与实现同步**。

### 旧 1. 现状盘点（基线已核实）

- 视图：`frontend/src/views/` 下 `ProvidersView / PermissionsView / MemoriesView / McpView / SettingsView(+ConfigTab/AppearanceTab/CronSettingsTab/HooksTab/LspTab…)`。
- 接口：`/api/config`(GET/PUT)、`/api/providers`、`/api/hooks`、`/api/cron-settings`、agents/subagents/MCP 写 API（188 已全部落 DB 存储，契约未变，写成功带 `applies:"next_turn"`）。
- 组件优先级（owner 规则）：**设计稿 > ai-elements-vue/shadcn-vue 原生组件 > 自造**。
- 视觉铁律：禁渐变、禁粉色；长内容滚动不截断。

### 旧 2. 交付

### 2.1 段覆盖矩阵（Settings 页重组）

| 段 | 落点 | 现状 |
|---|---|---|
| providers（agents 段内） | ProvidersView | 已有，需小改：`api_key` 字段显示/提交**明文真值**（188 后 GET 已回明文，`api_key_set`/`api_key_hint` 下线——owner 2026-10-30 裁决明文读写） |
| permissions / default_permissions | PermissionsView | 已有 |
| memories | MemoriesView | 已有 |
| mcp（agents.defaults.mcp_servers + primary 自有） | McpView | 已有，需小改：`headers`/`oauth` 凭据字段明文直存直显（同上裁决） |
| hooks | HooksTab | 已有 |
| cron | CronSettingsTab | 已有 |
| lsp | LspTab | 已有 |
| compact / tools / features / windows / auto_review / approval_policy / approvals_reviewer / sandbox_mode / sandbox_workspace_write / agents(defaults 其余项 + 定义结构)；**peers（195 落地后：`enabled`、`inbound`、`claude_code`「与 Claude Code 互通」默认关——owner 2026-10-30 裁决只存数据库、用户手动打开；`claude_code` 行**只在 `GET /api/config` 返回 `claude_code_available:true`（本机检测到 Claude Code）时显示**，提示「默认关；开关不同的 forebrain 进程互相看不见」，与 TUI 同文；原始 YAML 编辑器里不出现这个字段（195 已处理）；原型 `config-db-settings-preview.html` 需补这一卡的「显示 / 不显示开关」两种状态并经 owner 确认）** | **新增表单**（SettingsView 新 Tab：按段一卡） | 缺，本计划补 |
| gateway / credentials | 只读卡片 + `手编 forebrain.yaml，重启生效` | 缺，本计划补 |

新增 Tab 的表单控件与 TUI 面板（190 §2.2）同构：三态布尔、enum 下拉（选项=后端校验集合）、数字输入（边界同后端）、文本输入、列表行编辑。表单校验以 `PUT /api/config` 的后端校验为准——前端只做即时提示，不复制规则（单一来源）。

### 2.2 生效提示

所有保存成功的表单显示 `已保存，下一 turn 生效`（读 188 加的 `applies:"next_turn"` 字段或固定文案）。gateway/credentials 只读卡固定尾注同 TUI。

### 2.3 稿面同步

`app-preview.html` 增加新 Tab 与只读卡的静态稿（与实现 1:1）；先改稿、owner 过目、再实现——顺序照既有流程。稿面与实现的像素验收以既有 A/B 截图流程为准。

### 旧 3. 步骤

1. **原型稿面**：`app-preview.html` 已不在工作树（见 §8），本计划的稿面另立为
   `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`（2026-10-30 初稿已出）。
   验证：owner 过目稿面并回答 §8.3 的拍板项（阻断点——稿面未确认不开工实现）。
2. **只读 gateway/credentials 卡**（最小、先行可验收）。
   验证：`cd frontend && pnpm typecheck`；对应 vitest。
3. **缺段表单逐段实现**（compact → features → tools → windows → auto_review → approval_policy/approvals_reviewer → sandbox 两段 → agents 结构卡）。每段：组件 + `PUT /api/config` 段写（走 188 的端点，**不改后端**）。
   验证：每段一个 vitest（渲染 + 提交 payload 形状断言）；`pnpm typecheck`。
4. **三态布尔/enum 控件**优先取 ai-elements-vue/shadcn-vue 原生（`@forebrain/workspace` 内 742 个 symlink 的组件库先例）；确无原生再自造，自造需在 PR 描述里说明查证过程。
5. **e2e（按触及面裁剪）**：`FOREBRAIN_E2E_SPEC="e2e/settings*.spec.ts e2e/providers*.spec.ts"` 先跑触及的；改动共享组件则按 AGENTS.md 规则扩到所有驱动该组件的 spec。**不跑全量 160 用例**（发布门禁才跑）。
   验证：触及 spec 全绿（约 2 分钟档）。
6. **全量**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`（确认无后端意外破坏）；`go vet ./...`。

### 旧 4. 边界

- **不动**：后端任何代码（187/188 已交付全部 API 能力）；既有视图的行为与路由。
- **不做**：YAML 在线编辑器（明确拒绝）；跨段批量保存（每段独立保存，避免部分失败语义）。

### 旧 5. 完成判据

1. 触及的 e2e spec 全绿；`pnpm typecheck` 绿；Go 全量绿。
2. 手测清单：16 段每段改一个合法值 → `fb_config` 变化 + 提示出现；一个非法值（如 `approval_policy: bogus`）→ 后端 4xx + 表单错误显示；gateway 只读卡无编辑入口；Providers 表单填明文 api_key 保存后**回显明文真值**、MCP header 凭据同理。
3. app-preview.html 与实现的 A/B 截图比对通过（既有流程）。
4. 验收全程 forebrain.yaml mtime 不变。

### 旧 6. 维护注意

- 新增 Root 段 → 后端段集合（187 §1.1）、TUI 描述符（190）、本页表单三处同步；漏一处至少要有测试红（TUI 有覆盖测试，web 端建议补一个段覆盖 vitest）。
- `applies:"next_turn"` 文案与 189 的事件徽标（若有）保持同一措辞。

### 旧 7. 逃生口

- 若某段在 `/api/config` PUT 的段粒度上没有独立端点（例如 agents 结构需要专用 API）→ 停，列出缺口，回 188 补端点，不要在前端拼整文件 PUT。
- 若 app-preview.html 现有结构与新 Tab 布局冲突（稿面债务）→ 停，报告冲突点，owner 拍板稿面改法。

