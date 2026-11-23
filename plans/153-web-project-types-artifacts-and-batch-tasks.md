# Plan 153：网页——项目类型、生成物卡片与抽屉预览、办公计划审批四选项、跑批「任务 / 执行历史」与只读执行视图、任务通知设置（**先原型、owner 确认后再实施**）

> **执行者须知**：本计划分两部分。**Part A 只改原型文件**，做完即 **STOP，等 owner 确认**；owner 在 `plans/README.md` 或对话里明确"原型通过"之前，**不得开始 Part B**。
> 按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发 "STOP conditions" 时停下报告。完成后更新 `plans/README.md` 本计划状态行（Part A 完成写 `BLOCKED（原型待 owner 确认）`）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。工作区里无关的未提交改动不要碰。
>
> **前置**：Part A 无代码前置。Part B 要求 `145`–`151` 全部 DONE（网页消费它们的接口）。
>
> **基线（Part B 开始前）**：`cd frontend && pnpm typecheck && pnpm vitest run`，记下已红用例。

## Status

- **Priority**: P1
- **Effort**: L（Part A：M；Part B：L）
- **Risk**: MED（大量新界面；文件预览涉及安全：HTML 必须沙箱）
- **Depends on**: Part B 依赖 `145`、`146`、`148`、`149`、`150`、`151`
- **Category**: direction
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区

## owner 规则与裁定（必须遵守）

- **网页改动先原型**（owner 2026-10-11/17）：原型必须在 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 上迭代，**实施时 1:1 还原**；原型外框（页面背景、说明卡、caption）固定白/浅色，不写深色覆盖。
- 组件选择：**先用 ai-elements-vue，再用 shadcn-vue 原生组件**，都没有才自己写。新建 / 编辑表单用**弹窗**（MCP 与子代理页除外）；**所有列表服务端分页带总数**；文案**简洁**，能用标签就不写句子；对话卡片要有设计感；代码高亮一律 monokai-light；无页面标题层、少嵌套。
- 本期裁定：
  1. 项目类型三种：办公创作 / 创意编程 / 后台跑批；类型可改，**只影响新会话**；改成 / 改出跑批要二次确认；跑批项目必须受信任（创建或改成跑批时一并记录信任）。
     **owner 2026-10-27 默认规则**：TUI/Web 打开尚未登记的文件夹，Git 工作树根目录或子目录默认 coding，非 Git 默认 office；已有项目/会话与显式选择不被默认规则覆盖。Web 的预选只调用 145 的只读接口，不在前端复制 `.git` 判定。
  2. 办公创作、后台跑批的对话里必须显示**生成物的下载和预览卡片**；点下载即下载；点支持预览的文件**在右侧抽屉标签页预览**。预览：docx 用 docx-preview、xlsx 用 SheetJS（前端渲染）；**pptx 等用后端 LibreOffice 转 PDF**；图片 / 音视频 / PDF / HTML（一律沙箱）/ Markdown / 文本用浏览器原生能力；压缩包显示文件列表；psd / cad 只给下载。
  3. 办公计划审批：**按计划执行 / 修改计划（必须写意见）/ 只要计划，先不执行 / 请其他模型审阅（配置了审阅模型才出现）**。
  4. 跑批：用户在网页**只能查看，不能发起对话、不能插话**；允许：**停止运行中的任务、续跑已停止的执行、重新执行（不新建任务，进执行历史）、手工新建任务、定时任务、通道**；
     每条任务有**执行历史列表**；任务结束经通道通知（项目设置里配"任务通知"目标）。

## 后端已提供的接口（Part B 消费；字段以 145–151 落地后的实际代码为准，先读一遍）

| 用途 | 接口 | 关键字段 |
|---|---|---|
| 项目类型 | `POST /api/v1/projects`、`PATCH /api/v1/projects/:id`、`GET …/:id` | `project_type`、`trust`；错误 `batch_requires_trust`、`project_type_invalid`；`task_notify` |
| 默认类型预选 | `GET /api/v1/projects/default-type?root=<绝对目录>`（145 新增） | `{project_type,source}`；`git_worktree` → coding、`non_git` → office；只读、不创建项目 |
| 会话类型 | `GET /api/chat/sessions/:id` | 顶层 `project_type`（会话出生类型）；`project.project_type`（项目当前类型） |
| 生成物 | `GET /api/chat/sessions/:id/artifacts?page&limit` | `items[]{id,name,title,kind,preview,mime,size,origin,missing,changed,download_url,content_url,preview_url?,entries_url?}`、`total` |
| 生成物内容 | `…/artifacts/:aid/content?disposition=inline|attachment`、`…/preview.pdf`（503 `libreoffice_unavailable`）、`…/entries` | |
| 运行结束帧 | websocket `run_completed/run_error/run_cancelled` 与持久化事件 | `artifacts[]` |
| 工具卡 | `present_files` 的 `tool_call_completed` 输出 | `presented[]{id,name,rel_path,title,kind,preview,size}` |
| 只要计划 | `POST /api/actions/:id/approve` body `{"plan_only": true}` | 退出计划卡输出含 `plan_only`、`artifact` |
| 任务 | `GET /api/v1/projects/:id/tasks?page&limit&q&status`、`GET /api/v1/tasks/:task_id?page&limit` | 见 149 Step 7.5 |
| 新任务 / 新执行 | `POST /api/v1/projects/:id/sessions` `{title, task_id?, external_ref?, origin:"web"}` → websocket `start_run`（新任务带内容 / 附件；新执行与续跑发**空内容**） | 拒绝码 `batch_*` |
| 停止 | websocket `cancel_run` | |
| 通知目标 | `PATCH` 项目 `task_notify`；`GET /api/v1/projects/:id/channel-targets` | |
| 预设 | 会话预设读接口返回 `batch-sandbox` | 显示"沙箱隔离 · 免审批"，只读 |

> 注意（来自 2026-10-18 的复审）：前端的 axios 实例会把响应**深层改成驼峰**（`download_url` → `downloadUrl`），类型定义按驼峰写；请求体照后端蛇形写。

---

## Part A：原型（只改 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`）

### A0：读懂原型的组织方式

`app-preview.html` 是单文件原型：屏幕清单是一个 `[['id','中文名'], …]` 数组（约 `:4936`，如 `['projform', '新建项目（弹窗）']`）、各屏由 JS 函数拼 HTML（如新建项目弹窗约 `:4635`、项目页约 `:6526`、项目会话约 `:6564`）；
先通读"对话 · 全部消息卡""对话 · shell 命令卡（定稿）""项目 · 会话""新建项目（弹窗）""计划审批"相关屏，**复用其组件与样式类**，不引入新视觉语言。原型里的版本号 / "本轮迭代"说明卡照现有格式加一条（2026-10-26 项目类型与跑批）。

### A1–A13：新增 / 修改的屏（每屏一个清单条目，中文名如括号所示）

1. **新建项目（弹窗）· 类型**：在现有弹窗里加"类型"三选一卡片（图标 + 名称 + 一行说明：办公创作「文档、表格、幻灯片等办公交付」/ 创意编程「代码与软件项目」/ 后台跑批「供业务系统批量执行任务」）；选后台跑批时出现勾选「信任此目录（跑批以沙箱隔离、免审批运行）」，不勾不能提交。
   选中未登记文件夹后，类型默认选中后端建议值，并在卡片下显示简短依据「Git 工作树，默认创意编程」或「非 Git 目录，默认办公创作」；画出手动改成另一类型后不被预选覆盖的状态。
2. **项目页 · 类型列**：项目表格加"类型"列（标签样式）。
3. **项目设置 · 类型**：项目设置里显示类型并可修改；改动弹确认（"只影响之后新建的会话"；改成 / 改出后台跑批时加醒目说明与信任勾选）。
4. **新对话 · 选中跑批项目**：输入框区域换成一行说明 + 「新建任务」按钮（跳到该项目的任务页并打开新建任务弹窗）。
5. **对话 · 生成物卡**：办公会话里 `present_files` 的卡片——每个文件一张：类型图标（文档 / 表格 / 幻灯片 / PDF / 图片 / 视频 / 音频 / 网页 / Markdown / 文本 / 压缩包 / 设计 / CAD / 其他）、标题、"类型 · 大小"、下载按钮、预览按钮（可预览才有）；文件已删除 / 已改动的状态标记。卡头沿用 136 的一行卡头（动作词「已交付文件」）。
6. **对话 · 只要计划**：退出计划卡显示「已保留计划」；计划文件作为本轮交付物出现（见第 7 条的统一规则）。
7. **本轮交付物（owner 2026-10-27 统一规则）**：`present_files`、plan_only 的计划文件、运行结束帧 / 接口兜底的生成物合并成**同一组**「本轮交付物」，按 id 去重，位置固定在**本轮最后一条助手正文之后**；该组之后只允许运行元信息（「已工作 …」），不得再有助手正文或工具卡。交付物**不参与活动线**（不是节点，竖线在正文处收尾），左边缘与正文对齐。**超过 3 个先折叠**：默认列前 3 个，底部一行安静的「显示其余 N 个」，展开后同一行变「收起」；3 个以内直接全列。每行左侧是文件类型图标 + 文件名 + 「类型 · 大小」，右侧只有图标按钮（预览、下载）。
8. **抽屉 · 生成物预览**（右侧抽屉的文档标签，与 136 的抽屉同一套外观）：分别画 docx、xlsx（底部工作表切换）、pptx（PDF 页）、PDF、图片、视频、音频、HTML（沙箱提示角标）、Markdown、代码文本（monokai-light）、压缩包文件列表（分页）、"不支持预览"（只有下载）、"预览服务不可用（未安装 LibreOffice）"。标签头有文件名与**图标按钮**（下载、关闭）；标签只列当前上下文的生成物。交付物每行左侧是文件类型图标 + 文件名 + 「类型 · 大小」，右侧只有**图标按钮**（预览、下载）——owner 2026-10-27：能用图标的一律图标按钮。
9. **对话 · 办公计划审批**：138 的"计划审批占输入框"（`PlanApprovalDock`）换成办公四选项；"修改计划"展开说明输入（必填）。
10. **项目 · 任务（跑批）**：跑批项目的项目空间里「会话」标签换成「任务」：工具栏（搜索、状态筛选、「新建任务」）、表格（任务：标题 + 任务ID；来源；状态（最新执行）；执行次数；生成物；更新时间）、行操作（查看、继续（最新执行已停止时）、重新执行（最新执行失败或停止时））、分页。
    **新建任务（弹窗）**：任务内容（必填）、附件（沿用现有"＋"附件交互）、标题（可选）、外部编号（可选）。
11. **任务详情（跑批）= 执行历史列表；执行详情 = 某一次执行的对话页**（owner 2026-10-27 裁定）：
    - **任务详情页**：顶栏「任务 › 任务名」+ 任务ID + 状态；页内是来源/外部编号一行、可做操作（停止 / 重新执行 / 新建执行）、四个事实（执行次数、已完成、最新状态、工作区）和**执行历史列表**（第 N 次、状态、开始时间、用时、交付物、结果或失败原因、操作图标）。列表只做索引，不放对话；点一行「查看执行过程」进入那次执行。
    - **执行详情页 = 执行过程**：**必须是对话视图，禁止另造一套执行视图**——同一条活动线与同一套节点图标（助手品牌标识、思考大脑、工具成功勾 / 失败叉）、工具卡、助手正文；没有输入框，底部是一条与输入框同宽同圆角的只读条。顶栏左侧「任务名 › 第 N 次执行」+ 状态，状态旁是这一次可做的操作；右上工作台按钮与其它页面**同一位置**（在「新建主代理」之前）。
    - **工作台**：概览（当前执行与任务事实）、上下文、工作目录。**执行历史不进工作台；工作台不放「交付物」标签**（交付物已在过程视图里）；办公创作与后台跑批的工作台也**不放「变更文件」**。
12. **项目设置 · 任务通知（跑批）**：目标列表（通道 + 接收者）+「添加」弹窗：通道下拉（只列本项目已启用的通道）、接收者从"出现过的群 / 人"下拉挑选或手填。
13. **项目 · 定时任务（跑批）**：新建 / 编辑弹窗去掉"投递通道"字段（说明由任务通知接管）。

### A14：交付给 owner

在浏览器打开原型，逐屏截图检查无错位；把新增屏清单与"本轮迭代"说明发给 owner。**STOP，等待 owner 确认**；owner 的修改意见在原型上迭代，直到确认。确认后在 `plans/README.md` 本计划行记"原型 vNN 已确认（日期）"。

---

## Part B：实施（原型确认后）

> 每个界面**1:1 还原已确认的原型**；与原型不一致的地方以原型为准，原型没画的状态按现有同类界面的写法。

### B1：数据层（`frontend/src/lib/api.ts` 与类型）

按"后端已提供的接口"表补全请求函数与 TS 类型（驼峰）：`createProject`/`updateProject` 带 `project_type`、`trust`、`task_notify`；`getSessionArtifacts`、`getArtifactEntries`；`approveAction(id, {planOnly})` 发 `plan_only`；`listProjectTasks`、`getTask`、`createTaskSession`（`origin:'web'`）、`getChannelTargets`、`getProjectDefaultType(root)`（返回 `{projectType,source}`）。
每个函数配 `api.test.ts` 用例（照该文件现有 mock 写法）。

### B2：项目类型（A1–A4、A13）

- 新建项目弹窗加类型卡；跑批时强制信任勾选；后端 `batch_requires_trust` 等错误码映射为中文提示（locales 里加键）。
- **默认类型**：选择未登记文件夹后调用 `getProjectDefaultType(root)` 预选类型并显示来源。用 `projectTypeTouched` 记录用户是否手动选过类型：未 touched 时随文件夹更新建议；一旦 touched，后续文件夹/接口返回不得覆盖用户选择。请求竞态用 root 或递增序号丢弃旧响应；失败时不伪造 Git 判定，保留当前选择并提示“默认类型识别失败，可手动选择”。
  新建项目提交始终带当前 `project_type`；按文件夹直接发起新对话不带类型，由 145 的共享创建边界决定，页面用返回的 `project.project_type` 展示最终结果。
- 项目表格类型列；项目设置类型修改与确认弹窗。
- 新对话页：选中跑批项目时替换输入区（A4）。
- 跑批项目的定时任务弹窗隐藏投递字段（A13）。
- **后端小改（本步一并做）**：`GET /api/chat/sessions`（对话抽屉列表）默认**排除**跑批会话（`project_type='batch'`），带 `project_id` 时也排除——跑批执行只在任务页出现。改 `pkg/state/session_store.go` 的 `ListConversationsPage` 的 WHERE（加 `AND project_type <> 'batch'`），并给 `pkg/state` 补一个用例。

### B3：生成物卡（A5–A7）

- 新组件 `frontend/src/components/chat/ArtifactCard.vue`（+ `ArtifactList.vue`）：按 `kind` 选图标（优先用 ai-elements-vue / 现有图标集）、标题、"类型 · 大小"（大小格式与后端 `tool.HumanSize` 一致：B/KB/MB/GB 一位小数）、下载（`<a :href="downloadUrl" download>`）、预览按钮（`preview` 非空才显示）、`missing`/`changed` 标记。
- `ToolCallCard.vue`：`present_files` 的完成态正文渲染为 `ArtifactList`（数据来自卡的结构化输出；字段名对照 146 的输出 JSON）；`exit_plan_mode` 且输出 `planOnly` 时渲染"已保留计划"+ 计划生成物卡。
- 运行结束帧 / 回放事件里的 `artifacts`：`useChatStream` 记到对应 run；渲染 A7 的"交付物"块时**排除**已由 `present_files` 卡展示过的 id。
- 仅在会话类型为 office / batch 时渲染（coding 会话不会有这些数据，但组件判定以数据为准，不以类型为准）。

### B4：抽屉预览（A8）

- `useDocTabs.ts` 的 `DocTab.kind` 加 `'artifact'`，带 `artifact: {sessionId, id, name, preview, contentUrl, downloadUrl, previewUrl?, entriesUrl?}`；`docTabFor` 增加生成物版本（同一生成物再次打开只聚焦）。
- 抽屉标签内容组件（找到渲染 `DocTab` 的组件——136 加的 `ToolDocOutput`/抽屉标签容器）按 `preview` 分派到新组件 `ArtifactPreview.vue`：
  - `image` → `<img>`；`video` → `<video controls>`；`audio` → `<audio controls>`；`pdf` → `<iframe :src="contentUrl">`；
  - `html` → `<iframe :src="contentUrl" sandbox="allow-scripts allow-popups">`（**不得**加 `allow-same-origin`）；
  - `markdown` → 取文本用现有 Markdown 渲染组件（与 `PlanDoc.vue` 同一个）；`text` → CodeMirror 只读 + monokai-light（复用编辑器里已有的只读模式）；
  - `docx` → `docx-preview` 渲染 ArrayBuffer（fetch `contentUrl`，>50 MB 不渲染只提示下载）；
  - `sheet` → SheetJS 解析 ArrayBuffer，工作表切换，单表最多渲染 2000 行 × 100 列（超出提示"仅显示前 2000 行"）；
  - `office_pdf` → `<iframe :src="previewUrl">`；先 `fetch` HEAD/GET 判断 503 `libreoffice_unavailable` → 显示 A8 的不可用态；
  - `archive` → 调 `entries` 接口，表格分页显示；
  - `''` → 不支持预览态。
- 依赖：`pnpm add docx-preview`（Apache-2.0）；SheetJS 社区版用官方 tarball：`pnpm add https://cdn.sheetjs.com/xlsx-0.20.3/xlsx-0.20.3.tgz`（Apache-2.0；**不要**用 npm 上停在 0.18.5 的 `xlsx` 包）。两者都**动态 import**（只在打开对应预览时加载）。在 `NOTICE` 里登记两个依赖的许可证（照该文件现有条目格式）。

### B5：办公计划审批（A9）

`PlanApprovalDock.vue`：会话类型 office 时渲染四选项（i18n 键新增：`chat.officePlanExecute`=按计划执行、`chat.officePlanRevise`=修改计划、`chat.officePlanKeepOnly`=只要计划，先不执行、复用审阅键）；"修改计划"沿用现有 keep planning 的必填说明；"只要计划"调用 `approveAction(id, {planOnly: true})`。coding 会话的四项**一字不改**。

### B6：跑批任务页与执行视图（A10、A11）

- 路由：`/projects/:id/tasks`（项目空间标签）与 `/projects/:id/tasks/:taskId`（任务详情；**151 的通知链接就指向它，路径不得改**）。跑批项目的项目空间不显示「会话」标签、显示「任务」；其它类型反之。
- 任务列表：服务端分页 / 搜索 / 状态筛选；行操作：继续 = 打开该执行会话的 websocket 发空内容 `start_run`；重新执行 = `createTaskSession({taskId})` 后对新会话发空内容 `start_run`；成功后跳任务详情并选中新执行。
- 新建任务弹窗：先上传附件（沿用现有上传接口）→ `createTaskSession({title, externalRef})` → 对新会话发 `start_run`（内容 + 附件 id）→ 跳详情。
- 任务详情：执行历史（服务端分页）；右侧复用 `ChatView` 的消息渲染，但**只读**：不渲染输入框、插话队列、模式切换；底部只读条按状态给停止 / 继续 / 重新执行；子代理抽屉标签的输入框同样不渲染。
  拒绝码（`batch_execution_running` 等）映射为简短中文提示。
- `ChatView` 打开一个跑批会话（例如从旧链接进入）时同样只读，并在顶部给"在任务页查看"链接。

### B7：任务通知设置（A12）

项目设置里"任务通知"区（仅跑批项目显示）：列表 + 添加弹窗（通道下拉来自项目已启用通道；接收者下拉来自 `channel-targets`，可手填）；保存走 `PATCH` 项目 `task_notify`。

### B8：测试

- 单测（vitest，照同目录现有测试写法）：`ArtifactCard.test.ts`（图标 / 大小 / 预览按钮显隐 / missing 标记）、`ArtifactPreview.test.ts`（每种 preview 分派到对应元素；html 的 iframe `sandbox` 属性**不含** `allow-same-origin`；503 显示不可用态）、`useDocTabs` 新 kind、`PlanApprovalDock.test.ts` 办公四项与 `plan_only` 请求体、任务列表与详情组件、新建项目弹窗类型与信任勾选。
  默认类型专项：`getProjectDefaultType` 请求/驼峰解析、Git 工作树预选 coding、非 Git 预选 office、旧 root 响应被丢弃、手动选择后不被接口返回覆盖、直接打开 folder 显示后端最终类型。
- e2e（新 spec，`FOREBRAIN_E2E_SPEC` 只跑本批；**同树并发时**设置 `FOREBRAIN_E2E_WORK` 与私有端口，见 `scripts/acceptance/web_e2e.sh` 头注释）：
  `project-types.spec.ts`（真实后端建 Git 根目录/子目录/空仓库/非 Git 四种文件夹：默认类型与界面一致；手动选择优先）、`artifacts.spec.ts`（fake provider 让模型调用 `present_files` 交付一个 md、一个 png、一个 docx：卡片出现、下载链接 200、预览标签打开）、
  `office-plan-approval.spec.ts`（只要计划 → 卡头"已保留计划"、计划生成物卡）、`batch-tasks.spec.ts`（新建任务 → 详情页出现执行 #1 且无输入框；取消 → 继续；失败 → 重新执行出现 #2）。
  e2e 里需要的 fake provider 行为若 `.claude/skills/run-forebrain/fake_provider.py` 不支持（例如脚本化 `present_files` 调用），先读它的脚本格式再加用例；做不到即 STOP 2。

## Done criteria

- Part A：原型新增 A1–A13 全部屏（含 A14 交付记录），owner 已确认（README 记录日期与原型版本）。
- Part B：
  - [ ] `cd frontend && pnpm typecheck` exit 0；`pnpm vitest run` 无新增失败，新单测存在且通过
  - [ ] 四个新 e2e spec 经 `scripts/acceptance/web_e2e.sh` 通过（列出命令与结果）
  - [ ] `grep -rn "allow-same-origin" frontend/src` 无输出
  - [ ] coding 会话的计划审批、消息卡、项目页行为不变（既有 e2e：`exit-plan-approval.spec.ts`、`message-cards.spec.ts`、`project-space.spec.ts` 通过）
  - [ ] `NOTICE` 已登记 docx-preview 与 SheetJS 的许可证
  - [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

1. Part A 完成后：**无条件 STOP**，等 owner 确认原型。
2. e2e 所需的 fake provider 能力不存在且无法用现有脚本格式表达。
3. 后端接口与"已提供的接口"表不一致（字段缺失或语义不同）——列出差异停下，不要在前端绕。
4. docx-preview 或 SheetJS 的许可证与上文不符，或官方 tarball 无法安装。

## Maintenance notes

- 预览安全：HTML / SVG 的隔离靠两层——后端响应头 `Content-Security-Policy: sandbox …`（146）与前端 iframe `sandbox`；任一层都不许加 `allow-same-origin`。
- 生成物卡的数据来源有三处（`present_files` 卡输出、运行结束帧 `artifacts`、生成物列表接口），渲染以 id 去重。
- 任务详情路由 `/projects/:id/tasks/:taskId` 是通知链接的落点（151），改路由必须同步后端模板。
