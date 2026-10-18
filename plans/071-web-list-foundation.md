# Plan 071：web 表格与表单的共用底座——服务端分页约定、工具栏/说明/分页器/表头排序/行图标按钮、紧凑提示条、单选组、右侧抽屉

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；若 `go test` 报 `Package pdfium was not found in the pkg-config search path`，
> 先跑 `make pdfium pdfium-fonts`，并照 Makefile 导出 `PKG_CONFIG_PATH`。
>
> **Drift check（先跑）**：以下每条都必须各有 1 行输出，否则 STOP：
> ```
> grep -n 'const RailTooltip\|import RailTooltip from' frontend/src/App.vue
> grep -n 'export function isTopModal' frontend/src/components/common/modalStack.ts
> grep -n '"page_size": pageSize,' pkg/gateway/memories_api.go
> grep -n '^\.forebrain-table-wrap {' frontend/src/assets/main.css
> grep -n '  function pager(total, size, key) {' docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html
> ```

## Status

- **Priority**: P1（072–076 全部依赖本计划）
- **Effort**: L
- **Risk**: LOW–MED（只新增共用件与一个 Go 辅助文件；替换 `RailTooltip` 会动到菜单栏的折叠提示）
- **Depends on**: none
- **Category**: direction（设计稿落地的基础设施）
- **Planned at**: commit `4888d76` + 工作区（设计稿 `app-preview.html` 已含 2026-10-17/18 全部迭代），2026-10-18

## Why this matters

owner 2026-10-17/18 在设计稿上把 web 的所有列表统一成“表格 + 上方工具栏（左搜索、右按钮）+ 下方说明 + 分页器”，
操作栏一律用同尺寸图标按钮（悬停出紧凑文字提示），可排序列在表头排序，表单里的单选用标准单选组，MCP 的查看/编辑/新增走右侧抽屉。
这些件在 072–076 里会被几十处页面复用；如果每页各写一份，尺寸、文案、键盘行为必然走样（设计稿要求 1:1）。
本计划只做“共用的件 + 服务端分页的约定”，不改任何业务页面——页面的改造由 072–076 分别完成。

owner 2026-10-18 的三项裁决（实施必须遵守，见 `~/.claude` 记忆 `web-list-implementation-decisions`）：
1. 原型没画、现有 web 已有的功能一律保留，按原型同一套规则摆放；
2. **所有表格列表都由服务端分页**：每个列表接口都在服务端完成搜索/筛选/排序后再按页返回，并带总数；前端不在浏览器里切页；
3. 原型里后端没数据的列由后端补只读字段。

本计划落实第 2 条的“约定 + 共用件”，具体接口在 072–076 各自改。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型：https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地源文件 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，
  用浏览器直接打开即可；顶部“本轮新增”区有一排状态按钮，点按钮切到对应状态）。
- 本计划对应原型里的函数与样式（**照它们的结构、尺寸、文案实现，不要自己发挥**）：
  | 共用件 | 原型函数 | 原型 CSS（`<style>` 里搜类名） |
  |---|---|---|
  | 分页器 | `pager(total, size, key)` | `.pager` `.pager-size` `.pg-num` `.pg-gap`；按钮用 `.btn.sm`（26px 高、12px 字） |
  | 表头排序 | `sortTh(scope, key, label, first)` | `.tb th.sortable` `.th-sort` `.sort-ic` |
  | 行图标按钮 | `act(icon, label, attrs, tone, name)`、`actSlot()`、`rowActs(list)` | `.icon-btn` `.row-act` `.acts-r` `.icon-slot` |
  | 表格固定列宽、单行 | — | `.tb.fix-tb` `.cell-1` `.tb tr:has(> td > .acts-r) > td` |
  | 工具栏 / 表下说明 | `tabTools(left, acts)`、`tabNote(...)` | `.row`、`.hint` |
  | 紧凑提示条 | `showTip(el)`、`tipTarget(node)` | `.rail-tip` `.tip-above` `.tip-below` |
  | 单选组 | `radioField(label, name, opts, cur, hint, vertical)` | `.radios` `.radio` `.radios.v` `.radio.desc` `.radio .rd` `.radio .swc` |
  | 右侧抽屉 | `mcpDrawer()` 的外壳 | `.mask.dw-mask` `.dw` `.dw-h` `.dw-b` `.dw-f`、`@keyframes dw-in` |
  原型状态按钮：**分页 · 多页（记忆）**（分页器多页、页码省略）、**记忆 · 表头排序**、**工具 · 查看入参**、**MCP 抽屉 · 查看（只读）**、**项目 · 项目设置**（单选组）。
- 原型 CSS 变量 → 真实 web 变量（`frontend/src/assets/main.css` 的 `:root`）对照，**写样式时一律用右列**：
  | 原型 | web |
  |---|---|
  | `--line` | `--forebrain-divider` |
  | `--line-2` | `--forebrain-divider-strong` |
  | `--page` | `--forebrain-surface` |
  | `--subtle` | `--forebrain-bg-alt` |
  | `--soft` | `--forebrain-button-alt-bg` |
  | `--text` / `--text-2` / `--muted` | `--forebrain-text` / `--forebrain-text-2` / `--forebrain-muted-text` |
  | `--brand` / `--brand-soft` / `--on-brand` | `--forebrain-brand-1` / `--forebrain-brand-soft` / `--forebrain-on-brand` |
  | `--danger` / `--success` | `--forebrain-danger` / `--forebrain-success` |
  | `--pop` | `--forebrain-shadow-pop` |
  提示条底色 `#101828`、字色 `#ffffff` 是原型写死的值，web 也写死（与现有 `RailTooltip.vue` 一致）。

### 原型里这些件的确切规格（摘自 `app-preview.html`，实现照抄数值）

```css
/* 分页器：整行 = 左“共 N 条” + 右侧[每页条数下拉][上一页][页码…][下一页]，间距 6px */
.pager-size select { height: 26px; padding: 0 24px 0 9px; border: 1px solid var(--line-2); border-radius: 6px;
  background: var(--page); color: var(--text); font-size: 12px; font-weight: 500; appearance: none; }
.pager-size svg { position: absolute; right: 7px; top: 50%; width: 13px; height: 13px; margin-top: -6.5px; color: var(--muted); } /* chevron-down */
.pg-num { min-width: 26px; height: 26px; padding: 0 7px; border: 1px solid var(--line-2); border-radius: 6px;
  background: var(--page); color: var(--text); font-size: 12px; font-weight: 500; font-variant-numeric: tabular-nums; }
.pg-num:hover { background: var(--soft); }
.pg-num.on { background: var(--brand); border-color: var(--brand); color: var(--on-brand); cursor: default; }
.pg-gap { min-width: 18px; text-align: center; color: var(--muted); font-size: 12px; }   /* “…” */
.btn.sm { height: 26px; padding: 0 9px; font-size: 12px; }                               /* 上一页 / 下一页，带 chevron-left / chevron-right 图标 */

/* 表头排序：列名 + 上下两个小三角（8×12 的 SVG，两条 path：上三角 "M4 1 7.5 5h-7z"，下三角 "M4 11 .5 7h7z"） */
.tb th.sortable { padding: 0; }
.th-sort { display: flex; align-items: center; gap: 6px; width: 100%; padding: 8px 10px; border: 0; background: transparent; font-weight: 600; text-align: left; cursor: pointer; }
.tb th.sortable:hover { background: var(--soft); }
.tb th.sortable.on { color: var(--text); }
.th-sort:focus-visible { outline: 2px solid var(--brand); outline-offset: -2px; }
.sort-ic path { fill: var(--line-2); }  .tb th.sortable:hover .sort-ic path { fill: var(--muted); }
.sort-ic path.act { fill: var(--brand); }

/* 行图标按钮 */
.icon-btn { width: 28px; height: 28px; display: grid; place-items: center; border: 0; background: transparent; border-radius: 6px; color: var(--muted); }
.icon-btn:hover { background: var(--soft); color: var(--text); }
.acts-r { display: flex; justify-content: flex-end; align-items: center; gap: 2px; }
.acts-r .icon-slot { width: 28px; flex: none; }          /* 这一行没有某个动作时留空位，图标上下对齐 */
.row-act.dgr { color: var(--danger); }  .row-act.dgr:hover { color: var(--danger); background: var(--soft); }
.row-act.ok:hover { color: var(--success); }
.tb tr:has(> td > .acts-r) > td { vertical-align: middle; } /* 带图标操作的行整行垂直居中 */

/* 固定列宽、每格一行 */
.tb.fix-tb { table-layout: fixed; }
.tb.fix-tb > thead > tr > th, .tb.fix-tb > tbody > tr > td { vertical-align: middle; white-space: nowrap; }
.cell-1 { display: block; overflow: hidden; text-overflow: ellipsis; }
.tb.fix-tb td.t, .tb.fix-tb td.num { font-variant-numeric: tabular-nums; }

/* 紧凑提示条：约 20px 高 */
.rail-tip { position: absolute; z-index: 20; background: #101828; color: #ffffff; font-size: 12px; font-weight: 500; line-height: 16px;
  padding: 2px 7px; border-radius: 4px; white-space: nowrap; pointer-events: none; box-shadow: var(--pop); }
.rail-tip::before { content: ""; position: absolute; left: -3px; top: 50%; margin-top: -3px; width: 6px; height: 6px; background: #101828; transform: rotate(45deg); } /* 右侧放置：箭头在左 */
.rail-tip.tip-above::before { left: var(--tip-x, 50%); top: auto; bottom: -3px; margin-top: 0; margin-left: -3px; }              /* 上方放置：箭头在下 */
.rail-tip.tip-below::before { left: var(--tip-x, 50%); top: -3px; margin-top: 0; margin-left: -3px; }                           /* 下方放置：箭头在上 */
/* 位置：上方 = 按钮顶 - 提示高 - 6px；下方 = 按钮底 + 6px；右侧 = 按钮右 + 8px、垂直居中；
   上/下放置时水平居中于按钮，但左右各留 4px 不超出窗口，箭头 --tip-x 仍指向按钮中心；
   上方放不下（按钮顶到窗口顶 < 提示高 + 10px）时自动改到下方。 */

/* 单选组（参照 Ant Design Radio.Group / Element Plus el-radio） */
.radios { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 24px; min-height: 34px; }
.radio { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 400; color: var(--text); cursor: pointer; }
.radio input { appearance: none; width: 16px; height: 16px; margin: 0; flex: none; border: 1px solid var(--line-2); border-radius: 50%;
  background: var(--page); display: grid; place-content: center; transition: border-color 120ms; }
.radio input::before { content: ""; width: 8px; height: 8px; border-radius: 50%; background: var(--brand); transform: scale(0); transition: transform 120ms; }
.radio input:checked, .radio:hover input { border-color: var(--brand); }
.radio input:checked::before { transform: scale(1); }
.radio input:focus-visible { outline: 2px solid var(--brand); outline-offset: 2px; }
@media (prefers-reduced-motion: reduce) { .radio input, .radio input::before { transition: none; } }
.radios.v { flex-direction: column; align-items: flex-start; gap: 12px; }     /* 竖排：每个选项带说明时 */
.radio.desc { align-items: flex-start; }  .radio.desc input { margin-top: 2px; }
.radio .rd { display: grid; gap: 2px; } .radio .rd b { font-weight: 500; } .radio .rd span { font-size: 12px; color: var(--muted); line-height: 1.5; }
.radio .swc { width: 14px; height: 14px; border-radius: 3px; flex: none; display: block; }   /* 主题色色块 */
/* 组的标签在上（与其他表单字段一样：12px、--text-2、500），组下方一行 .hint 帮助文字（选中项的含义可随选中变化） */

/* 右侧抽屉 */
.mask.dw-mask { display: flex; justify-content: flex-end; align-items: stretch; }   /* 与弹窗同一层遮罩 */
.dw { width: 560px; max-width: 100%; height: 100%; background: var(--page); border-left: 1px solid var(--line);
  box-shadow: -8px 0 24px rgba(16, 24, 40, 0.08); display: flex; flex-direction: column; animation: dw-in 160ms ease-out; }
@keyframes dw-in { from { transform: translateX(32px); opacity: 0.6; } to { transform: none; opacity: 1; } }
@media (prefers-reduced-motion: reduce) { .dw { animation: none; } }
.dw-h { display: flex; align-items: center; gap: 8px; padding: 14px 16px 12px; border-bottom: 1px solid var(--line); }
.dw-h b { font-size: 14px; font-weight: 600; }  .dw-h .icon-btn { margin-left: auto; }   /* 右上角关闭（X），悬停提示“关闭” */
.dw-b { flex: 1; min-height: 0; overflow: auto; padding: 14px 16px 18px; display: grid; gap: 12px; align-content: start; }
.dw-f { display: flex; align-items: center; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--line); }   /* 按钮靠右 */
.dw .field[readonly], .dw select.field:disabled { background: var(--subtle); color: var(--text-2); }  /* 只读表单 */
```

分页器页码算法（原型 `pager` 原样）：总页数 `pages = max(1, ceil(total/size))`，当前页夹在 `[1, pages]`；
`pages ≤ 7` 时列出全部页码；否则列 `1`，`cur > 3` 时插 `…`，再列 `max(2,cur-1)…min(pages-1,cur+1)`，`cur < pages-2` 时插 `…`，最后列 `pages`。
当前页按钮高亮、带 `aria-current="page"`、不可点；第 1 页时“上一页”禁用，最后一页时“下一页”禁用；
每页条数下拉选项固定 `10 / 20 / 50`，文案 `每页 {n} 条`；左侧文案 `共 {total} 条`；整行 `role="navigation" aria-label="分页"`；页码按钮 `aria-label="第 {n} 页"`。
**每个表格下方都有分页器，即使只有一页**。换每页条数、换搜索词、换筛选或换排序都回到第 1 页。

表头排序交互（原型 `sortTh` 原样）：未排序的列点一下按“首次方向”排序（时间列 = 新→旧 `desc`，名称列 = A→Z `asc`），
已排序的列再点切换方向；当前排序列高亮（`th.on`，对应三角用品牌色），`aria-sort` 取 `ascending/descending/none`；
悬停提示（`title`）：未排序时 `点击按{列名}排序（新→旧）` 或 `（A→Z）`，已排序时 `点击升序（旧→新）/（A→Z）`、`点击降序（新→旧）/（Z→A）`。

行图标按钮（原型 `act`）：图标 + `aria-label="{动作} {对象名}"` + 悬停提示 `{动作}`（显示在按钮**上方**）；
语气 `danger`（删除：红色）、`ok`（允许：悬停绿色）、默认（灰）。同一列里某行缺某个动作时放一个 28px 空位。
图标约定（lucide 名）：查看 `eye`、编辑 `pencil`、删除 `trash-2`、下载 `download`、允许 `check`、拒绝 `x`、上移 `arrow-up`、停止 `circle-stop`、
置顶 `pin` / 取消置顶 `pin-off`、进入 `folder-open`、打开会话 `message-square`、立即执行 `play`、执行记录 `history`、查看入参 `chevron-down` / 收起 `chevron-up`。

## Current state（全部已核实，行号为 `4888d76`）

- `frontend/src/components/RailTooltip.vue` — 菜单栏折叠时的提示：右侧放置、`padding: 6px 9px`、`border-radius: 6px`、8px 箭头、`line-height: 1`；
  由 `App.vue` 的 `hoverTooltip(anchor, text)` / `clearTooltip()` 驱动（`App.vue:189` 渲染 `<RailTooltip :anchor :text />`，`App.vue:401-409` 两个函数）。
  它只支持“右侧”，且样式不是设计稿最新的紧凑版（设计稿 2026-10-18：12px 字、16px 行高、`2px 7px` 内边距、4px 圆角、6px 箭头）。
- `frontend/src/components/common/PageTopRow.vue` — 页面第一行“标题 + 说明 + 按钮”。设计稿 2026-10-18 起**所有页面和标签都不再有这一行**（072–076 逐页去掉；本计划不删组件）。
- `frontend/src/components/common/ForebrainModal.vue` + `modalStack.ts` — 弹窗外壳与“只关最上层”的栈（`pushModal/popModal/isTopModal`）。抽屉要接入同一个栈。
- `frontend/src/assets/main.css:1149-1178` — 现有表格样式 `.forebrain-table-wrap` / `.forebrain-table`（`td` 默认 `vertical-align: top`，没有固定列宽、没有单行截断、没有图标操作列的居中规则）。
- `frontend/src/components/memory/MemoryFilesPanel.vue:113-121` — 唯一已有的“分页”：只有上一页/下一页和 `1 / N`，没有页码按钮和每页条数下拉的设计稿样式。
- **服务端分页的现成范例**：`pkg/gateway/memories_api.go:419-492` `handleMemoriesFilesList`：
  读 `page`（默认 1）、`page_size`（默认 20、上限 `memoryListMaxPage`）、`sort`、`order`（`asc|desc`）、`q`；
  过滤 → 排序 → 切页，返回 `{"total", "page", "page_size", "files"}`。**本计划把它提炼为所有列表接口的统一约定**（见 Step 1）。
- 前端请求约定：`frontend/src/lib/api.ts:1408-1417` 响应拦截器把 snake_case 键转成 camelCase；查询参数手写 snake_case（范例 `api.ts:2112-2131` `memoryFilesList`）。
- 测试范例：前端组件测试 `frontend/src/components/common/ConfirmDialog.test.ts`（vitest + `@vue/test-utils`，`setLocale('en')`，`stubs: { teleport: true }`）；
  Go 列表接口测试 `pkg/gateway/memories_api_test.go:217` `TestMemoriesFilesListPaginatesAndSorts`。
- 文案：`frontend/src/locales/index.ts`，`zh` 与 `en` 两个平铺对象（`'chatDrawer.title': '…'` 这种键）。
- 组件陈列：`frontend/src/gallery/ComponentGallery.vue`（开发用的组件预览页）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| 依赖库 | `scripts/fetch-libtokenizers.sh` | exit 0 |
| gateway 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'ListQuery\|PageOf\|MemoriesFiles' -count=1` | ok |
| vet / fmt | `go vet ./pkg/gateway/...` ；`gofmt -l pkg/gateway` | 无输出 |
| 前端单测 | `cd frontend && pnpm test` | 全绿 |
| 前端单测（本计划） | `cd frontend && pnpm test -- src/components/common src/composables/useServerList` | 全绿 |
| 前端类型 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0、无输出 |
| 浏览器全套 | `scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |

## Scope

**In scope**（只改这些）：
- `pkg/gateway/list_page.go`（新建）、`pkg/gateway/list_page_test.go`（新建）
- `pkg/gateway/memories_api.go`（只把手写的 page/page_size 解析与切页换成新辅助函数，行为不变）
- `frontend/src/assets/main.css`（新增共用样式；不改已有类的数值，`RailTooltip` 的样式随组件迁走）
- `frontend/src/components/common/` 下新建：`TablePager.vue`、`SortHeader.vue`、`RowAction.vue`、`RowActions.vue`、`RowActionSlot.vue`、
  `ListToolbar.vue`、`ListNote.vue`、`RadioField.vue`、`ForebrainDrawer.vue`、`AppTooltip.vue`，以及各自的 `*.test.ts`
- `frontend/src/composables/useTooltip.ts`（新建）、`frontend/src/directives/tip.ts`（新建）、`frontend/src/composables/useServerList.ts`（新建）+ 测试
- `frontend/src/lib/api.ts`（只新增 `ListPage<T>` 类型与 `listParams()` 辅助函数，不改已有方法）
- `frontend/src/main.ts`（注册 `v-tip` 指令）
- `frontend/src/App.vue`（只把 `RailTooltip` 换成新的提示条；菜单栏的行为与位置不变——位置调整在 072）
- `frontend/src/components/RailTooltip.vue`（删除）
- `frontend/src/locales/index.ts`（新增文案键）
- `frontend/src/gallery/ComponentGallery.vue`（陈列新件）

**Out of scope**（不要碰）：
- 任何业务页面（`views/*`、`components/project/*`、`components/settings/*`、`components/skills/*`、`components/memory/*`）——072–076 的事。
- `PageTopRow.vue` 的删除——076 最后删。
- 除 `memories_api.go` 以外的任何 Go 列表接口——各自的计划改。

## Steps

### Step 1：Go——统一的列表请求/响应约定（`pkg/gateway/list_page.go`）

新建 `pkg/gateway/list_page.go`，内容要点（注释照这个意思写成英文，与本包风格一致）：

```go
// listQuery is one list request's search, order and page, read the same way by
// every list endpoint the web pages page through (owner 2026-10-18: every table
// is paged on the server, after search, filters and order are applied).
type listQuery struct {
	Q        string // trimmed "q"
	Sort     string // "sort": an endpoint-defined key; "" = the endpoint's default
	Order    string // "asc" | "desc"; "" = the default direction of Sort
	Page     int    // 1-based
	PageSize int
	// Paged is true only when the request carried "page". Without it an
	// endpoint keeps its old answer (the whole list, or its old limit/offset
	// window): the chat project picker, the folder dialog and other non-table
	// callers read these endpoints too and must not suddenly get 10 rows.
	Paged bool
}

const (
	listDefaultPageSize = 10
	listMaxPageSize     = 100
)

// readListQuery reads q, sort, order, page and page_size. page < 1 → 1;
// page_size < 1 → listDefaultPageSize; page_size > listMaxPageSize → listMaxPageSize;
// order other than "asc"/"desc" → "".
func readListQuery(r *http.Request) listQuery

// desc reports whether the order is descending, given the sort key's own
// default (e.g. times default to newest first, names to A→Z).
func (q listQuery) desc(defaultDesc bool) bool

// pageOf returns the requested page of items and the page actually served:
// a page past the end serves the last page (so a delete that empties the last
// page lands on the one before it instead of an empty table).
func pageOf[T any](items []T, q listQuery) (page []T, servedPage int)

// writeListPage answers one page: {"<key>": rows, "total": n, "page": p, "page_size": s}
// plus any extra top-level fields the endpoint keeps (pass nil for none).
func writeListPage(w http.ResponseWriter, key string, rows any, total int, page int, q listQuery, extra map[string]any)
```

约定（写进文件头注释）：**每个列表接口**都接受 `q`、`sort`、`order`、`page`、`page_size` 和自己的筛选参数；**只有请求带了 `page` 才分页**（`Paged`），不带时保持该接口原来的回答（兼容聊天的项目选择器、文件夹对话框等非表格调用方——它们不改）；分页时
在服务端完成“筛选 → 排序 → 切页”，响应里**保留原有的列表键**（如 `projects`、`sessions`、`servers`），并加上 `total`、`page`、`page_size`。
数据库里的列表用 `LIMIT/OFFSET` + `COUNT(*)`（同一个 WHERE），内存里的列表用 `pageOf`。

然后把 `memories_api.go:428-492` 的手写解析与切页换成 `readListQuery` + `pageOf` + `writeListPage(w, "files", …)`：
**保持 `page_size` 默认 20 不变**（该接口的老默认值；前端 Step 10 起总是显式传 `page_size`）——实现方式：`readListQuery` 之后若请求没带 `page_size` 就设 20。
其余行为（`sort` 只认 `created`，否则 `updated`；`order` 默认 desc；`q` 过滤）不变。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'MemoriesFiles' -count=1` → ok（老用例不改也全过）。

### Step 2：Go 测试

新建 `pkg/gateway/list_page_test.go`（照 `memories_api_test.go` 的风格），至少覆盖：
- `TestReadListQueryClampsPageAndSize`：`page=0`→1、`page_size=0`→10、`page_size=1000`→100、`order=up`→""、`q` 去空白；不带 `page` 时 `Paged=false`，带 `page=1` 时 `Paged=true`。
- `TestPageOfServesLastPageWhenPastTheEnd`：25 条、page_size 10、page 9 → 返回第 3 页的 5 条、servedPage=3；0 条时 servedPage=1、空切片（非 nil）。
- `TestWriteListPageKeepsTheListKey`：响应 JSON 含 `rows` 键（用测试里传的 key）、`total`、`page`、`page_size` 与 extra 字段。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'ListQuery|PageOf|WriteListPage|MemoriesFiles' -count=1` → ok；`gofmt -l pkg/gateway` 无输出。

### Step 3：前端共用样式（`main.css`）

在 `main.css` 文件末尾新增一段（注释标注 `2026-10-18 shared list/table/form pieces (plan 071)`），把上面“确切规格”里的样式改写成 `forebrain-` 前缀的类、换成 web 变量：
- `.forebrain-table--fixed`（= `.tb.fix-tb`）、`.forebrain-cell-1`（= `.cell-1`）、`.forebrain-table td.forebrain-num`（数字列等宽）、
  `.forebrain-table tr:has(> td > .forebrain-row-actions) > td { vertical-align: middle; }`
- `.forebrain-icon-btn`（28×28，`.icon-btn` 规格；`[disabled]` 半透明）、`.forebrain-row-actions`（= `.acts-r`）、`.forebrain-row-action--danger` / `--ok`、`.forebrain-icon-slot`
- `.forebrain-th-sort`、`.forebrain-sort-ic`、`th.forebrain-sortable(.is-on)`
- `.forebrain-pager`、`.forebrain-pager-size`、`.forebrain-pg-num(.is-on)`、`.forebrain-pg-gap`、`.forebrain-btn--sm`（26px 高、`0 9px`、12px；只给分页器用）
- `.forebrain-radios(.is-vertical)`、`.forebrain-radio(.has-desc)`、`.forebrain-radio-desc`、`.forebrain-radio-swatch`
- `.forebrain-drawer-mask`（`ForebrainModal.vue:99-107` 的 `.forebrain-modal-mask` 是 **scoped** 样式，抽屉用不到，所以照抄它的值：`position: fixed; inset: 0; z-index: 50; background: color-mix(in srgb, var(--forebrain-text) 32%, transparent);`，再加 `display: flex; justify-content: flex-end; align-items: stretch;`，不要 padding）、`.forebrain-drawer`、`.forebrain-drawer-head`、`.forebrain-drawer-body`、`.forebrain-drawer-foot`、`@keyframes forebrain-drawer-in`、
  只读字段 `.forebrain-drawer .forebrain-field[readonly], .forebrain-drawer select.forebrain-field:disabled`
- `.forebrain-tip`（= `.rail-tip`，`position: fixed`，`z-index: 70`——高于弹窗/抽屉遮罩的 50，见 `ForebrainModal.vue:102`）、`.forebrain-tip--above`、`.forebrain-tip--below`、`.forebrain-tip--right`
- `.forebrain-list-note`（= `.hint`：12px、`--forebrain-muted-text`）、`.forebrain-list-toolbar`（flex、`gap: 8px`、`flex-wrap: wrap`、`align-items: center`）

**Verify**：`cd frontend && npx vue-tsc --noEmit -p tsconfig.json` → exit 0；`grep -c 'forebrain-pg-num' src/assets/main.css` → ≥ 2。

### Step 4：提示条——`useTooltip` + `AppTooltip.vue` + `v-tip` 指令，替换 `RailTooltip`

1. `src/composables/useTooltip.ts`：模块级单例状态 `{ anchor: HTMLElement | null, text: string, placement: 'top' | 'bottom' | 'right' }`，
   导出 `showTip(anchor, text, placement)`、`hideTip()`、`useTooltipState()`。
2. `src/components/common/AppTooltip.vue`：`Teleport to body`，`role="tooltip"`，按 `placement` 计算位置（规则见“确切规格”的注释，
   **注意用 `getBoundingClientRect()` 与 `window.innerWidth`，提示条 `position: fixed`**）；上方放不下自动改到下方；
   上/下放置时设置 CSS 变量 `--tip-x` 让箭头对准按钮中心。挂在 `App.vue` 原 `<RailTooltip>` 的位置（`App.vue:189`），整个应用只有这一个。
3. `src/directives/tip.ts`：`v-tip="text"`，参数是放置方向（`v-tip:top`、`v-tip:bottom`、`v-tip:right`，默认 `top`），修饰符 `.trunc` 表示
   “只有元素文字被截断（`scrollWidth > clientWidth`）时才显示”。监听 `mouseenter`/`focus` 显示、`mouseleave`/`blur`/`click` 隐藏；
   绑定值变化时更新；元素卸载时若它是当前锚点则 `hideTip()`。**不要再同时设置原生 `title`**（设计稿：不叠浏览器原生提示）。
4. `main.ts` 里 `app.directive('tip', tip)`。
5. `App.vue`：删 `RailTooltip` 的 import 与渲染；`hoverTooltip(anchor, text)` 改为调用 `showTip(anchor, text, 'right')`、`clearTooltip()` 改为 `hideTip()`
   （菜单栏现有的 `@mouseenter/@focus` 调用点不动——072 会改成 `v-tip:right`）。删除 `RailTooltip.vue`。

**Verify**：`grep -rn 'RailTooltip' frontend/src` → 无输出；`cd frontend && pnpm test` → 全绿；手动：`pnpm dev`（或 e2e 截图）折叠菜单栏后悬停图标，提示在图标右侧、约 20px 高。

### Step 5：`TablePager.vue`

props：`total: number`、`page: number`、`pageSize: number`；emits：`update:page`、`update:pageSize`。
结构与算法照“确切规格”的分页器（`共 {total} 条` 左；右侧下拉 + 上一页 + 页码/省略 + 下一页）；下拉用原生 `<select>` + `ChevronDown` 图标叠在右侧；
上一页/下一页用 `forebrain-btn forebrain-btn-ghost forebrain-btn--sm`（白底灰边，**不是**主色）+ `ChevronLeft`/`ChevronRight` 图标。
`pageSize` 不在 `[10,20,50]` 里时把它也加进选项并排序（原型同样处理）。`data-testid`：根 `table-pager`、页码 `pager-page-{n}`、`pager-prev`、`pager-next`、`pager-size`。

**Verify**：Step 13 的单测通过。

### Step 6：`SortHeader.vue`

用法：`<SortHeader label="更新时间" sort-key="updated" :active="sort === 'updated'" :order="order" first="desc" @sort="onSort" />`，
渲染一个 `<th class="forebrain-sortable" :class="{ 'is-on': active }" :aria-sort>` 里面一个 `<button class="forebrain-th-sort">`；
点击 emit `sort(key, nextOrder)`：未激活 → `first`；已激活 → 反向。三角 SVG 照原型两条 path；激活方向的 path 加 `is-act`。
`title` 文案见“确切规格”（`first='asc'` 用 A→Z/Z→A，`first='desc'` 用 新→旧/旧→新）。

### Step 7：`RowAction.vue`、`RowActions.vue`、`RowActionSlot.vue`

- `RowAction`：props `icon: Component`、`label: string`、`name?: string`、`tone?: 'danger' | 'ok'`、`disabled?: boolean`；
  渲染 `<button type="button" class="forebrain-icon-btn forebrain-row-action" :class="tone" :aria-label="name ? label + ' ' + name : label" v-tip:top="label">`，
  透传 `@click` 与 `data-testid`。
- `RowActions`：`<div class="forebrain-row-actions"><slot /></div>`。
- `RowActionSlot`：`<span class="forebrain-icon-slot" aria-hidden="true" />`。

### Step 8：`ListToolbar.vue`、`ListNote.vue`

- `ListToolbar`：`<div class="forebrain-list-toolbar"><slot name="left" /><span class="flex-1" /><slot name="actions" /></div>`（原型 `tabTools`：左搜索/筛选，右按钮）。
- `ListNote`：`<p class="forebrain-list-note"><slot /></p>`（原型 `tabNote`：表格**下方**、分页器之后的说明）。
- 约定（写进两个组件的注释）：页面不再有标题行；页面说明与原来的提示合成一段放在 `ListNote` 里；
  工具栏左侧**必须有搜索框**（`forebrain-field`，`max-width: 240px`），右侧放原标题行的按钮。

### Step 9：`RadioField.vue`

props：`label: string`、`name: string`、`options: Array<{ value: string; label: string; swatch?: string; description?: string }>`、
`modelValue: string`、`hint?: string`、`vertical?: boolean`；emits `update:modelValue`。
渲染：`<div class="forebrain-form-field" role="radiogroup" :aria-labelledby>`，标签 `<span :id>{{label}}</span>`（与其他表单字段的标签同样式：12px、`--forebrain-text-2`、500），
其下 `.forebrain-radios`（`vertical` 时 `.is-vertical`），每个选项 `<label class="forebrain-radio" :class="{ 'has-desc': o.description }"><input type="radio" :name :value :checked @change>`
+ 可选色块 `<i class="forebrain-radio-swatch" :style="{ background: o.swatch }">` + 文字（有说明时 `<span class="forebrain-radio-desc"><b>{{label}}</b><span>{{description}}</span></span>`）；
最后一行 `hint`（`.forebrain-list-note` 同样式）。键盘方向键切换由原生 radio 提供，不要自己拦截。

### Step 10：`ForebrainDrawer.vue`

props：`open: boolean`、`title: string`、`readonly?: boolean`（为真时标题旁显示灰色徽标“只读”）；slots：默认（表单）、`footer`；emits `close`。
`Teleport to body`；遮罩 `.forebrain-drawer-mask`（Step 3 定义，与弹窗遮罩同色同层 z-index 50，面板靠右）；
面板 `<aside class="forebrain-drawer" role="dialog" aria-modal="true" :aria-label="title">`；
头部：标题 `<b>`、只读徽标、右上角关闭按钮（`X` 图标，`forebrain-icon-btn`，`v-tip:bottom="t('common.close')"`，`aria-label` 同）；
接入 `modalStack`（打开时 `pushModal`、关闭时 `popModal`；Esc 与点遮罩只关最上层，照 `ForebrainModal.vue` 的写法）；关闭后焦点回到打开它的元素。

### Step 11：`useServerList.ts` 与 `api.ts` 的 `ListPage<T>`

1. `api.ts` 新增（放在文件靠前的类型区）：
   ```ts
   /** One page of a server-paged list (plan 071): the endpoint's own list key plus these. */
   export interface ListPageMeta { total: number; page: number; pageSize: number }
   export interface ListQuery { q?: string; sort?: string; order?: 'asc' | 'desc'; page: number; pageSize: number; [filter: string]: unknown }
   /** The query string every list endpoint reads: q, sort, order, page, page_size, plus its own filters (snake_cased). */
   export function listParams(query: ListQuery): URLSearchParams
   ```
   `listParams` 跳过空字符串/undefined；筛选参数键转 snake_case。
2. `useServerList<Row>(fetch: (q: ListQuery) => Promise<{ rows: Row[] } & ListPageMeta>, options: { pageSize?: number; sort?: string; order?: 'asc' | 'desc'; filters?: Record<string, unknown> })`：
   返回 `{ rows, total, page, pageSize, q, sort, order, filters, loading, error, load, reload }`（都是 `Ref`）。
   - `q` 变化 300ms 防抖后回到第 1 页并重新加载；`filters`/`sort`/`order`/`pageSize` 变化立即回到第 1 页并加载；`page` 变化直接加载。
   - 响应里的 `page` 是服务端实际给的页（越界时服务端给最后一页），写回 `page` 时**不要**再触发一次加载（用一个标志位）。
   - 同时只认最后一次请求的结果（请求序号），丢弃过期响应。
   - `error` 存 `getErrorMessage(e)`。
   默认 `pageSize` 10。

### Step 12：文案（`locales/index.ts`，zh 与 en 都加）

| 键 | zh | en |
|---|---|---|
| `list.total` | `共 {total} 条` | `{total} items` |
| `list.pageSize` | `每页 {n} 条` | `{n} / page` |
| `list.pageSizeAria` | `每页条数` | `Items per page` |
| `list.prev` | `上一页` | `Previous` |
| `list.next` | `下一页` | `Next` |
| `list.pageAria` | `第 {n} 页` | `Page {n}` |
| `list.pagerAria` | `分页` | `Pagination` |
| `list.sortFirst` | `点击按{label}排序（{dir}）` | `Sort by {label} ({dir})` |
| `list.sortAsc` | `点击升序（{dir}）` | `Sort ascending ({dir})` |
| `list.sortDesc` | `点击降序（{dir}）` | `Sort descending ({dir})` |
| `list.dirNewOld` / `list.dirOldNew` | `新→旧` / `旧→新` | `newest first` / `oldest first` |
| `list.dirAZ` / `list.dirZA` | `A→Z` / `Z→A` | `A→Z` / `Z→A` |
| `drawer.readonly` | `只读` | `Read-only` |

### Step 13：测试与陈列

新建并通过（照 `ConfirmDialog.test.ts` 的写法）：
- `TablePager.test.ts`：①3 页时列出 1 2 3、无省略；②20 页当前第 10 页 → `1 … 9 10 11 … 20`；③第 1 页“上一页”禁用、最后一页“下一页”禁用；
  ④点页码 emit `update:page`；⑤换每页条数 emit `update:pageSize`；⑥`total=0` 时显示 `0 items`、只有第 1 页。
- `SortHeader.test.ts`：未激活点击 emit `first`；激活 desc 点击 emit asc；`aria-sort` 三种取值。
- `RowAction.test.ts`：`aria-label` 拼对象名；`tone="danger"` 加类；悬停后 `useTooltipState().text` 为 label。
- `RadioField.test.ts`：选中项 `checked`；change emit；`vertical` 加类；色块与说明渲染。
- `ForebrainDrawer.test.ts`：Esc 关闭；叠在 `ForebrainModal` 下面时 Esc 只关上面的弹窗；`readonly` 显示徽标。
- `tip.test.ts`（指令）：`.trunc` 在未截断时不显示（用 jsdom 手动设 `scrollWidth/clientWidth`）。
- `useServerList.test.ts`：q 防抖后请求一次且 page=1；服务端返回 page=2（越界回退）时不重复请求；过期响应被丢弃。

`ComponentGallery.vue` 增加一节“列表与表单（plan 071）”：一个 3 列假表格（固定列宽 + 一行截断 + 行图标按钮含空位 + 表头排序）+ 分页器（total 196）+ 单选组（横排与竖排各一）+ 打开抽屉的按钮。

**Verify**：`cd frontend && pnpm test` → 全绿（新增 ≥ 7 个测试文件）；`npx vue-tsc --noEmit -p tsconfig.json` → exit 0；`scripts/acceptance/web_e2e.sh` → `web e2e: PASS`（本计划不改页面，老用例应全过；若折叠菜单栏提示的用例因尺寸断言失败，按新规格更新那条断言并在报告里写明）。

## Test plan

见 Step 2、Step 13。另：在 `ComponentGallery` 页面里人工并排对照原型的“分页 · 多页（记忆）”“记忆 · 表头排序”“工具 · 查看入参”“MCP 抽屉 · 查看（只读）”“项目 · 项目设置”五个状态，截图存 `$TMPDIR/071-*.png` 附在报告里。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` ok；`go vet ./pkg/gateway/...`、`gofmt -l pkg/gateway` 无输出
- [ ] `cd frontend && pnpm test` 全绿、`npx vue-tsc --noEmit -p tsconfig.json` exit 0
- [ ] `grep -rn 'RailTooltip' frontend/src` 无输出；`frontend/src/components/common/` 下 10 个新组件与测试都存在
- [ ] `scripts/acceptance/web_e2e.sh` 末行 `web e2e: PASS`
- [ ] `git status` 只出现 Scope 列出的文件

## STOP conditions

- Drift check 有任意一条没有输出。
- `memories_api.go` 换成新辅助函数后老用例失败且不是因为 `page_size` 默认值（说明理解错了老行为）。
- 提示条改 `position: fixed` 后在弹窗/抽屉里被遮住（z-index 不够）且调到 70 仍被遮——报告实际层级。
- 需要改任何业务页面才能让测试通过。

## Maintenance notes

- 072–076 的页面一律用这些件；新页面不要再写 `PageTopRow` 或手写分页/排序/图标按钮。
- 服务端分页约定（`q/sort/order/page/page_size` → 原列表键 + `total/page/page_size`，越界给最后一页；不带 `page` 时保持接口原行为）是所有列表接口的契约，改动要同时改 `list_page.go` 与 `useServerList`。`useServerList` 总是带 `page`。
- 提示条全局只有一个实例；任何图标按钮都用 `v-tip`，不要再加原生 `title`。
