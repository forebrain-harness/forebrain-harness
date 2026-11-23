# Plan 288: 网页日志页——设置 · 高级 · 日志

> **执行者须知**：逐步执行，每步跑完验证命令、对照预期再往下。遇到「STOP 条件」任一条，停下报告，不要自行发挥。
> **禁止任何 git commit / push**（owner 手动审阅提交），改动只留在工作区。完成后更新 `plans/README.md` 里本计划那一行的状态；若派发你的评审方说明由它维护索引，则跳过。
>
> **网页闸门（先看这条）**：owner 规则「网页改动先出原型、确认后 1:1 还原」。本计划的原型是 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/logs-preview.html`。**闸门已于 2026-11-12 由 owner 解除**（原话「288 原型已确认，可以实施」），原型哈希与制定时逐字节一致：`507d29fcdad154c853cfbdd28f9e71b24abf63d8fd6f5a067a7b18b441d12145`。`plans/README.md` 那一行可能仍写着「待 owner 确认」——以本条为准（索引由派发方统一更新）。开工的真正前提只剩 `plans/287` 已 DONE。
>
> **Drift check（开工前只跑一次）**：
>
> ```sh
> SCOPE="frontend/src/views/SettingsView.vue frontend/src/lib/api.ts frontend/src/locales/index.ts frontend/src/components/settings"
> git status --porcelain -- $SCOPE
> git diff 17b4633a -- frontend/src/views/SettingsView.vue
> shasum -a 256 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/logs-preview.html
> ```
>
> 预期：前两条无输出。原型哈希在 owner 确认时写进 `plans/README.md` 本计划那一行（制定时为 `507d29fcdad154c853cfbdd28f9e71b24abf63d8fd6f5a067a7b18b441d12145`，owner 提修改意见后会变）；与 README 记的不符即 STOP。`docs/plan/` 被 `.gitignore:76` 忽略，git 看不到原型的改动，所以必须用哈希。

## 状态

- **优先级**：P2
- **工作量**：M
- **风险**：LOW（纯前端 + 一个入口链接；只读）
- **依赖**：`plans/287-gateway-log-query-api.md`（必须已 DONE）
- **类别**：direction（新功能）
- **制定基线**：`17b4633a`，分支 `dev`，2026-11-11
- **网页闸门**：**已解除**——owner 2026-11-12 确认原型（`logs-preview.html`，哈希 `507d29fcdad154c853cfbdd28f9e71b24abf63d8fd6f5a067a7b18b441d12145`，与制定时逐字节一致）。原型右下「请你确认」五条以「Owner 裁决」与原型本身为准。

## Owner 裁决（2026-11-11）

- 页面位置：**设置 · 高级 · 日志**（全局页面放设置页标签的既有规则），内容区放宽到整宽；会话页的报错卡片加「查看日志」直达并带上会话筛选。
- 要能按时间范围、级别（调试/信息/警告/错误）、项目、会话、来源（终端/网关）查；不同会话并发的模型输出不能混在一起。

## 为什么要做

284–287 让每条日志带身份、每次模型调用有自己的请求与响应、网关能按条件查。这一份把它们放到网页上：选了会话就只剩它的记录；一次模型调用是表里的一行，点开才是它自己的请求、拼好的响应与逐条原文；并发的调用按会话各画一道。

## 现状（执行者需要的事实，全部内联）

### 原型（唯一的视觉与交互依据）

`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/logs-preview.html`，浏览器直接打开，全部可操作。实施必须 1:1 还原它的布局、间距、文案、状态；原型里是示例数据，真实数据来自 287 的接口。原型的结构：

```
设置（左：标签栏；右：内容区，日志标签时整宽）
└─ 筛选条（两行，自动换行）
   时间范围下拉（最近 15 分钟 / 1 小时 / 今天 / 最近 7 天 + 自定义起止 + 应用）
   级别四个按钮：调试 / 信息 / 警告 / 错误（多选，各带数量；默认勾后三个）
   来源分段：全部 / 终端 / 网关
   项目下拉 · 会话下拉（可搜标题或 ID，带来源与项目）· 类型下拉 · 搜索框 · 刷新 · 重置
└─ 直方图（60 段，按级别堆叠；标题「N 条 + 起止时间」；按下拖一段 = 把时间范围缩到那一段；单击一段 = 缩到那一段）
└─ 表格：时间 | 级别 | 来源（+进程号）| 会话（行悬停时末尾出现筛选图标）| 内容
   内容列按类型渲染：模型调用（状态码 + 耗时 + 地址 + 模型 + 输出 token）、工具调用（工具名 + 耗时 + 参数预览）、
   接口访问（方法 + 状态码 + 路径 + 耗时 + 大小）、其它（消息 + 错误 + 前 4 个字段）；子代理的记录前面有代理名小标签
└─ 分页：共 N 条 · 每页条数 · 页码
└─ 空状态：「这个范围内没有符合条件的日志」+ 三个放宽条件的按钮
右侧抽屉（宽 720）
├─ 普通记录：级别 + 标题 + 时间；正文；来源/项目/会话/会话 ID/轮次/代理/代码位置；错误（含错误链）；字段（JSON）；调用栈；
│            底部「前后 1 分钟」「复制这条记录」；会话行有「只看这个会话」、轮次行有「只看这一轮」
└─ 模型调用：状态徽标 + 「模型调用 · 会话标题」+ 时间；四个标签
   概览：状态 / 耗时 / 首字节 / 最长静默 四格；地址、模型、会话、代理、轮次、来源、起止、用量；「同一时刻的其他调用」
   请求：（失败时）「这次调用失败，请求体完整保留」提示；请求头；请求体（成功的调用中间是「只留头尾各 32KB，中间省略 N」）；下载
   响应：思考 / 正文 / 工具调用 三张卡 + 结束原因与用量；非 2xx 时是响应头与响应体原话
   原文：逐条事件（序号、相对时间、内容）；下载
键盘：Esc 关抽屉或弹层；抽屉开着时 ↑ ↓ 换行
```

级别颜色（原型 `:root`）：调试 `#98A2B3`、信息 `#3A67C6`、警告 `#DC8A14`、错误 `#D92D20`；其余颜色全部用既有设计令牌。

### 前端的既有约定

- 设置页标签在 `frontend/src/views/SettingsView.vue`。「高级」组当前是：
  ```ts
    {
      key: 'advanced',
      label: t('settings.groupAdvanced'),
      tabs: [
        { key: 'config', label: t('settings.tabConfig') },
        { key: 'runtime', label: t('settings.tabRuntime') },
      ],
    },
  ```
  模板里按 `activeTab` 分支渲染，例如 `<RuntimeStatusTab v-else-if="activeTab === 'runtime'" />`。
- 标签组件放 `frontend/src/components/settings/`，一个组件一个同名 `.test.ts`（vitest）。最小范例：`RuntimeStatusTab.vue`（只读、调一个接口）；带表格与分页的范例：`EnvVarsTab.vue`。
- 通用组件在 `frontend/src/components/common/`，**必须优先复用**（owner 规则：先 ai-elements-vue，再 shadcn-vue，再仓库已有的通用组件，都没有才自己写）：
  - `DataTable.vue`（`columns`、`rows`、`rowKey`、`rowAttrs`、`rowClass`…）、`TablePager.vue`（`total`、`page`、`pageSize`，`update:page` / `update:pageSize`，**每页条数固定为 10 / 20 / 50**）、`ListToolbar.vue`
  - `ForebrainDrawer.vue`（`open`、`title`、`readonly`，`close` 事件，Esc 关闭已内置）
  - `BadgeComponent.vue`、`AppTooltip.vue`
  - 代码展示：`frontend/src/components/editor/ReadonlyCode.vue`（CodeMirror，主题固定 monokai-light——owner 规则：网页代码高亮一律 monokai-light）
- 接口封装在 `frontend/src/lib/api.ts`，形如：
  ```ts
    runtimeInfo() {
      return api.get<RuntimeInfo>('/runtime').then((res) => res.data)
    },
  ```
- 文案在 `frontend/src/locales/index.ts`，中英两份扁平键（如 `'settings.tabRuntime': '运行状态'` / `'Runtime'`）。中文界面的动作词必须是中文；英文界面的级别用 `Debug / Info / Warn / Error`。
- 端到端测试在 `frontend/e2e/*.spec.ts`，用 `FOREBRAIN_E2E_SPEC=<spec 文件名> scripts/acceptance/web_e2e.sh` 跑（仓库没有 `make e2e`）。

### 287 的接口（已存在）

- `GET /api/logs?from&to&level&source&project&session&run&kind&q&page&page_size` → `{records, total, page, page_size, level_counts, buckets[60], truncated}`；记录字段：`id, t, lvl, kind, msg, source, host, pid, day, project, project_id, project_name, session, session_title, run, agent, worker, at, err, attrs, blobs`。
- `GET /api/logs/exchange?day&id` → `{id, day, request, response|null, scope, summary|null, concurrent[]}`。
- `GET /api/logs/blob?day&name&offset&limit` → 文本片段，响应头 `X-Log-Blob-Bytes`；加 `download=1` 是整份下载。
- 会话下拉的数据用既有的 `GET /api/chat/sessions?page=1&page_size=20&q=<搜索词>[&project_id=…]`（服务端搜索分页）；项目下拉用既有的 `GET /api/v1/projects`。**不要为下拉另加接口**。

## 需要的命令

| 用途 | 命令（在 `frontend/` 下） | 成功时 |
|------|------|--------|
| 类型检查 | `pnpm typecheck` | 退出码 0 |
| 单元测试 | `pnpm test` | 全部通过 |
| 构建 | `pnpm build` | 退出码 0 |
| 端到端 | 仓库根：`FOREBRAIN_E2E_SPEC=logs.spec.ts scripts/acceptance/web_e2e.sh` | 通过 |
| 外链门 | 仓库根：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -run TestWebBuildLoadsNoExternalAsset -count=1` | `ok` |

## 建议的执行者工具

- 有 `frontend-design` 技能就用它核对还原度（owner 要求网页改动必须有设计感、与原型一致）。
- 有浏览器驱动能力时，把原型与实现并排截图对比后再报告完成。

## 范围

**In scope**：
- `frontend/src/components/settings/LogsTab.vue`、`LogsTab.test.ts`（新建）
- `frontend/src/components/settings/LogDetailDrawer.vue`、`LogDetailDrawer.test.ts`（新建）
- `frontend/src/lib/logs.ts`、`frontend/src/lib/logs.test.ts`（新建：查询参数拼装、时间范围、行内容分类这些纯函数）
- `frontend/src/lib/api.ts`（三个方法与类型）
- `frontend/src/views/SettingsView.vue`（加标签、日志标签时内容区整宽）
- `frontend/src/locales/index.ts`（中英文案）
- `frontend/e2e/logs.spec.ts`（新建）
- 会话页报错卡片所在的那一个组件（Step 6 定位）

**Out of scope**：
- `pkg/**`——接口已由 287 定稿；发现缺字段即 STOP，不要顺手改后端。
- 左栏导航——不加顶层入口。
- 实时刷新 / 自动轮询——只有刷新按钮。
- 删除、清空、导出全部日志。
- 其它设置标签的宽度与布局。

## 步骤

### Step 1：接口封装与纯函数

`frontend/src/lib/api.ts` 加类型 `LogRecord`、`LogPage`、`LogExchange`、`LogQuery` 与方法 `logs(query)`、`logExchange(day, id)`、`logBlob(day, name, offset?, limit?)`（返回 `{text, total}`，`total` 取自 `X-Log-Blob-Bytes`）、`logBlobDownloadUrl(day, name)`。

`frontend/src/lib/logs.ts`：
- `rangeOf(preset, now)`：`'15m' | '1h' | 'today' | '7d'` → `{from, to}`；`'custom'` 原样。
- `toQuery(filters, page, pageSize)`：把界面状态变成查询参数（级别为空集时不发请求，直接空结果）。
- `rowKind(record)`：返回 `'llm-request' | 'llm-response' | 'tool' | 'http' | 'plain'`，供内容列选渲染方式。
- `formatDuration(ms)`、`formatBytes(n)`（与原型的 `fDur` / `fBytes` 输出一致：`858ms`、`31.9s`、`13m34s`；`156B`、`93.0KB`、`1.12MB`）。

**验证**：`pnpm typecheck && pnpm test -- logs` → 通过。

### Step 2：`LogsTab.vue`——筛选条、直方图、表格、分页

状态：`filters = {preset, from, to, levels: Set, source, project, session, kind, q}`、`page`、`pageSize`（默认 50）、`selectedId`。任何筛选变化把 `page` 置 1 并重新请求；搜索框输入做 300ms 防抖；请求用递增序号丢弃过期响应。

- 表格用 `DataTable`，分页用 `TablePager`（每页条数用它自带的 10 / 20 / 50）。
- 时间列：范围跨天时前面加 `MM-DD`（原型同）。
- 会话列：标题为空时显示会话 ID 的前 12 位；没有会话显示 `—`。行悬停时列尾出现筛选图标按钮（`aria-label` 与提示「只看这个会话」），点它不打开抽屉。
- 直方图：60 个按级别堆叠的柱（自己画，`div` 即可，不引图表库）；`mousedown`→拖动→`mouseup` 得到一段，换算成 `from/to` 并把时间范围设为自定义；拖动距离小于一柱按单击那一柱处理。
- `truncated` 为真时，直方图标题与分页的条数写「N 条以上」（`logs.totalAtLeast`）。
- 接口失败：表格位置显示错误原因与「重试」按钮，不清空筛选。

**验证**：`pnpm test -- LogsTab` → 通过（见测试计划）。

### Step 3：`LogDetailDrawer.vue`

用 `ForebrainDrawer`（宽度按原型 720）。`kind === 'llm'` 的记录走调用视图：打开时请求 `logExchange(day, attrs.x)`；「请求」「原文」标签第一次点开时才请求 `logBlob`（先取前 256KB，超出时底部给「继续加载」与「下载」）。

- 请求体与工具参数用 `ReadonlyCode`（JSON、monokai-light）；请求体不是合法 JSON（被省略过中间）时按纯文本显示，省略标记行按原型的提示条样式。
- 「响应」标签用 `summary`；`summary` 为空且状态码非 2xx 时显示响应头与响应体原话；`response` 为空时显示「这次调用还没有结束，或进程在结束前退出」并仍可看请求。
- 「同一时刻的其他调用」用 `concurrent`；点一条 = 在同一个抽屉里换成那次调用。
- 换标签、换行**不重放抽屉的滑入动画**。

**验证**：`pnpm test -- LogDetailDrawer` → 通过。

### Step 4：接进设置页

`frontend/src/views/SettingsView.vue`：「高级」组加 `{ key: 'logs', label: t('settings.tabLogs') }`（放在 `runtime` 之后）；模板加 `<LogsTab v-else-if="activeTab === 'logs'" />`；`activeTab === 'logs'` 时给设置页的栅格容器加一个修饰类，使内容区取满剩余宽度（原型 `.set.wide`：标签栏 150px、间距 24px、内容区 `minmax(0,1fr)`）。其它标签的宽度不变。

地址栏：支持 `?tab=logs&session=<id>&from=<RFC3339>&to=<RFC3339>&focus=<记录 id>` 进入时预置筛选（用设置页既有的标签路由方式；先看 `SettingsView.vue` 怎么从路由读 `activeTab`，照它扩展，不要另起一套）。

**验证**：`pnpm typecheck && pnpm test -- SettingsView` → 通过。

### Step 5：文案

`frontend/src/locales/index.ts` 中英各加一组 `logs.*` 与 `settings.tabLogs`。中文逐字取自原型（如「最近 1 小时」「全部会话」「搜索内容、错误、字段」「拖一段可缩小范围」「同一时刻的其他调用」「这次调用失败，请求体完整保留（{size}）。」「成功的调用只留头尾各 32KB，中间省略 {size}」）。文案保持简短（owner 规则：能用标签表达就不写句子）。

**验证**：`pnpm test` 全绿；`grep -c "'logs\." frontend/src/locales/index.ts` 为偶数（中英成对）。

### Step 6：会话页报错卡片的「查看日志」

定位：`grep -rn "providerError\|turn_error" frontend/src/components/chat frontend/src/composables/useChatStream.ts --include='*.vue' --include='*.ts' -l`，找到渲染「轮次失败」那张卡片的组件（原型左栏「对话」里那张红色卡片）。在卡片右侧加一个次要按钮「查看日志」，点击跳到 `设置?tab=logs&session=<当前会话>&from=<本轮开始前 1 分钟>&to=<失败后 1 分钟>`。拿不到本轮开始时间时用失败时间前 15 分钟。

定位结果不唯一（多处都在渲染失败卡片）或组件不在 `frontend/src/components/chat` 下 → STOP 报告候选位置，不要猜。

**验证**：该组件的既有测试通过；新增一条断言按钮存在且链接参数正确。

### Step 7：端到端与还原度

`frontend/e2e/logs.spec.ts`（照 `frontend/e2e/` 里任一既有 spec 的启动方式）：起网关与 fake provider，发一条会触发工具调用的消息 → 打开 设置 · 日志 → 断言表格有 `轮次` 与 `模型调用` 的行且会话标题正确 → 选该会话 → 点一行模型调用 → 抽屉「响应」里有 fake provider 回的文字 → 「原文」里事件条数 > 0 → 关闭抽屉 → 点「调试」按钮后行数变多。

再把实现与原型并排截图（默认视图、按会话筛选、调用抽屉四个标签、空状态），附在执行报告里。

**验证**：`FOREBRAIN_E2E_SPEC=logs.spec.ts scripts/acceptance/web_e2e.sh` → 通过；`pnpm build` → 退出码 0；外链门 `ok`。

## 测试计划

- `frontend/src/lib/logs.test.ts`：`rangeOf` 四个预设；`toQuery` 的参数拼装（级别逗号分隔、空值不带）；`rowKind` 五种；`formatDuration` / `formatBytes` 的边界值。
- `LogsTab.test.ts`（照 `EnvVarsTab.test.ts` 的 mock 接口写法）：
  - 首次加载用默认条件（最近 1 小时、信息/警告/错误）请求一次；
  - 点「调试」→ 请求带四个级别且回到第 1 页；
  - 选来源、项目、会话、类型各触发一次带对应参数的请求；
  - 搜索框防抖后只发一次请求；
  - 行悬停的筛选图标把 `session` 设上且**不**打开抽屉；点行打开抽屉；
  - 直方图拖动把时间范围变成自定义的那一段；
  - 空结果显示空状态与三个按钮；接口失败显示错误与重试；
  - `truncated` 时显示「以上」。
- `LogDetailDrawer.test.ts`：普通记录渲染字段、错误链、调用栈；调用视图四个标签各自只在第一次点开时请求对应数据；`response` 为空时的提示；点「同一时刻的其他调用」换成另一条调用。
- `SettingsView.test.ts`：「高级」组有「日志」；带 `?tab=logs&session=…` 进入时 `LogsTab` 收到预置筛选。
- `frontend/e2e/logs.spec.ts`：Step 7。

## 完成判据（全部满足）

- [ ] `pnpm typecheck`、`pnpm test`、`pnpm build` 全部通过
- [ ] `FOREBRAIN_E2E_SPEC=logs.spec.ts scripts/acceptance/web_e2e.sh` 通过
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -run TestWebBuildLoadsNoExternalAsset -count=1` 为 `ok`
- [ ] `git status --porcelain -- pkg cmd` 无本计划引入的改动
- [ ] 执行报告附原型与实现的并排截图（至少 6 组）
- [ ] 改动文件 ⊆ 「范围」；`plans/README.md` 状态行已更新

## STOP 条件

- `plans/README.md` 里本计划那一行仍写「待 owner 确认原型」。
- 原型哈希与 README 记录的不符。
- 287 的接口缺少页面需要的字段（列出缺什么，不要改后端）。
- Step 6 定位不唯一。
- 还原原型需要引入新的第三方依赖（图表库、日期选择器等）——原型全部用原生控件与 `div` 完成，实现也应如此；确有必要时先报告。

## 非功能要求

| 维度 | 风险 | 对策 | 验证 |
|------|------|------|------|
| 性能 | 每次按键都发请求 | 搜索 300ms 防抖；过期响应按序号丢弃 | `LogsTab.test.ts` 的防抖用例 |
| 性能 | 1MB 的请求体卡住页面 | 先取 256KB，其余「继续加载」或下载；`ReadonlyCode` 只读 | `LogDetailDrawer.test.ts` |
| 体验 | 点行想看详情却触发了筛选 | 会话筛选是悬停才出现的独立图标按钮，行的其它位置一律打开抽屉 | `LogsTab.test.ts` |
| 体验 | 抽屉换标签时闪动 | 抽屉只在打开时播一次动画 | 并排截图 + 手动核对 |
| 可访问 | 键盘操作 | 行可聚焦、Enter 打开；Esc 关闭；级别按钮用 `aria-pressed`、来源分段用 `role="radiogroup"` | 组件测试断言属性 |
| 可维护 | 文案散落 | 全部进 `locales/index.ts`，中英成对 | Step 5 的 grep |
| 离线部署 | 引入外链资源 | 不加任何 CDN 资源 | 外链门 |

## 维护注意

- 页面是 287 接口的薄壳：新增记录类型时，`rowKind` 不认识的一律按普通记录渲染，不会空白。
- 级别四色只在这一页用；不要把它们加进全局主题。
- 评审重点：与原型逐屏对照（筛选条两行的换行点、直方图高度 56、表格行高 34、抽屉宽 720）；是否复用了 `DataTable` / `TablePager` / `ForebrainDrawer` / `ReadonlyCode` 而不是另写。
- 已知未做（有意）：自动刷新、保存常用筛选、导出。
