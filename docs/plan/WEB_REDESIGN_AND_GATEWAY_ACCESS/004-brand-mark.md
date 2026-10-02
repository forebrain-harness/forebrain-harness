# 计划 004："方印"品牌 logo 落到菜单栏、登录页、favicon 与文档站

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- frontend/public frontend/index.html`
> 计划 001、003 改过 `App.vue`、`LoginView.vue`、`main.css`，那是预期的。

## 状态

- **优先级**：P2
- **工作量**：S
- **风险**：LOW
- **依赖**：计划 003（`--forebrain-brand-1`、`--rail-*` 变量与 `data-rail-theme`）；决策 D2 已定：**1 方印**（owner 2026-09-29："Logo：1方印"）
- **类别**：direction
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求重新设计品牌 logo，并从三套方案中选定"1 方印"：字母 F 刻在圆角方印里，右下角一枚小方块像落款。
现有标识是一条侧面的鲸（`frontend/public/mark.svg`），用 `<img>` 引用，`currentColor` 在 `<img>` 里不生效，所以它在深色菜单栏上显示成一块灰黑色；
`frontend/public/favicon.svg` 则是一个从未被引用、带多重渐变与发光滤镜的旧图标，字形也与本项目无关。文档站使用同一个鲸图标。

预览见 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/brand-options.html` 的"Logo 方案 · 1 方印"。

## 现状

- `frontend/public/mark.svg`：鲸形标识，`fill="currentColor"`，`<rect ... fill-opacity="0.12"/>` 做底。
- `frontend/public/favicon.svg`：`viewBox="0 0 120 120"`，含 2 个 `linearGradient`、2 个 `radialGradient`、1 个 `feGaussianBlur` 滤镜；全仓没有任何地方引用它。
- `frontend/index.html:5`：`<link rel="icon" type="image/svg+xml" href="/mark.svg" />`。
- `frontend/src/App.vue:5`：`<img class="forebrain-rail-mark" src="/mark.svg" alt="" aria-hidden="true" />`；`:7` 字标是单个 `<span class="forebrain-rail-wordmark">Forebrain Harness</span>`。
- `frontend/src/views/LoginView.vue`（计划 001 新建）：沿用 `/mark.svg`。
- 文档站（独立仓库 `forebrain-harness.github.io/`）：`docs/.vitepress/config.mjs:100` `logo: "/mark.svg"`，文件在 `docs/public/mark.svg`。

## 设计

1. **图形（32×32 网格，逐字使用）**：

   ```svg
   <rect width="32" height="32" rx="7"/>                       <!-- 方印底，填充色 = fill -->
   <path d="M9.5 7.5h13v4h-9v3.75h7.5v4h-7.5V24.5h-4z"/>       <!-- 字母 F，填充色 = glyph -->
   <rect x="18.5" y="20.5" width="4" height="4" rx="0.6"/>     <!-- 落款方块，填充色 = glyph -->
   ```
2. **Vue 组件 `frontend/src/components/BrandMark.vue`**：内联上面的 SVG（`viewBox="0 0 32 32"`，`role="img"`，`aria-label` 取 `t('brand.name')` = "Forebrain Harness"），
   属性 `size`（数字，默认 28）。方印底用 `fill: var(--brand-mark-fill)`，F 与落款用 `fill: var(--brand-mark-glyph)`；组件里不写颜色字面量。
3. **颜色变量**（写在 `frontend/src/assets/main.css`，随计划 003 的主题色与菜单栏深浅自动变化）：
   - `:root`：`--brand-mark-fill: var(--forebrain-brand-1); --brand-mark-glyph: var(--forebrain-on-brand);`（白底上：品牌色方印、白色字）；
   - 深色菜单栏内的标识反白：`.forebrain-app-shell[data-rail-theme="dark"] .forebrain-rail { --brand-mark-fill: #FFFFFF; --brand-mark-glyph: var(--rail-bg); }`（限定在菜单栏内部，避免影响外壳里其他位置的标识）；浅色沿用 `:root`。
4. **使用位置**：
   - `App.vue` 菜单栏：`<img ... src="/mark.svg">` 换成 `<BrandMark :size="28" class="forebrain-rail-mark" />`；字标改为
     `<span class="forebrain-rail-wordmark"><span class="forebrain-rail-wordmark-strong">Forebrain</span> Harness</span>`，`-strong` 字重 650，其余 400；
   - `LoginView.vue`：`<BrandMark :size="44" />` 置于卡片顶部。
5. **静态文件**（不能随主题变化，固定使用默认主题色海军蓝——logo 的正色）：
   - 新 `frontend/public/favicon.svg`：

     ```svg
     <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
       <rect width="32" height="32" rx="7" fill="#1F4A9E"/>
       <path d="M9.5 7.5h13v4h-9v3.75h7.5v4h-7.5V24.5h-4z" fill="#FFFFFF"/>
       <rect x="18.5" y="20.5" width="4" height="4" rx="0.6" fill="#FFFFFF"/>
     </svg>
     ```
   - `index.html`：`<link rel="icon" type="image/svg+xml" href="/favicon.svg" />`；
   - **删除** `frontend/public/mark.svg`（再无引用）；
   - 文档站 `docs/public/mark.svg` 替换为与上面 `favicon.svg` 相同的内容（文件名保持不变，`config.mjs` 无需改）。文档站改动留在其工作区，不提交。
6. 文案键 `brand.name`（中英两表均为 `Forebrain Harness`）。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 范围

**只允许修改**：
- `frontend/src/components/BrandMark.vue`（新建）、`frontend/src/components/BrandMark.test.ts`（新建）
- `frontend/src/App.vue`（品牌区）、`frontend/src/views/LoginView.vue`（品牌区）、`frontend/src/assets/main.css`（`--brand-mark-*` 与字标样式）、`frontend/src/locales/index.ts`
- `frontend/public/favicon.svg`（重写）、删除 `frontend/public/mark.svg`、`frontend/index.html`
- `forebrain-harness.github.io/docs/public/mark.svg`（文档站仓库）
- `frontend/e2e/brand.spec.ts`（新建）

**不要碰**：`pkg/gateway/dist/**`；TUI 的启动画面（`pkg/tui/setup.go`）；计划 002 的终端 banner 字样。

## 步骤

### 第 1 步：组件与变量

实现设计第 1–4、6 条。新增 `BrandMark.test.ts`（结构参照 `frontend/src/components/DiffView.test.ts` 的挂载方式）：挂载后存在 1 个 `rect[rx="7"]`、1 个 `path`、1 个 `rect[rx="0.6"]`；
`size=44` 时根 `svg` 的 `width`、`height` 均为 `44`；组件源码不含 `#` 开头的颜色字面量。

**验证**：`cd frontend && corepack pnpm test` → 全部通过；`grep -rn "mark.svg" frontend/src frontend/index.html` → 无输出。

### 第 2 步：静态文件

实现设计第 5 条。

**验证**：`test ! -e frontend/public/mark.svg` → 退出码 0；`grep -ci "gradient\|filter" frontend/public/favicon.svg` → `0`；
`grep -c "1F4A9E" frontend/public/favicon.svg forebrain-harness.github.io/docs/public/mark.svg` → 两个文件各为 `1`。

### 第 3 步：真机验证

新建 `frontend/e2e/brand.spec.ts`：
1. `GET /favicon.svg` 状态 200、正文含 `#1F4A9E`、不含 `Gradient`；页面 `link[rel="icon"]` 的 `href` 为 `/favicon.svg`；
2. 登录页：品牌标识 `svg` 内 `rect[rx="7"]` 的计算 `fill` 为 `rgb(31, 74, 158)`，F 的 `fill` 为 `rgb(255, 255, 255)`；截图 `brand-login.png`；
3. 登录后，菜单栏深色：方印 `fill` 为 `rgb(255, 255, 255)`、F 的 `fill` 为 `rgb(15, 29, 56)`；菜单栏浅色：方印 `fill` 为 `rgb(31, 74, 158)`；各截一张菜单栏区域图 `brand-rail-dark.png`、`brand-rail-light.png`；
4. 在设置页切到松石青后，浅色菜单栏方印 `fill` 为 `rgb(14, 110, 126)`；切回海军蓝。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 前端单测 172/172 通过
- [x] 两者均通过
- [x] `web e2e: PASS`（11/11 用例；brand-login/brand-rail-dark/brand-rail-light 截图齐）
- [x] 均无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上。
- 发现 `mark.svg` 或 `favicon.svg` 还被其他位置引用（例如 Go 代码、npm 包、Dockerfile、README 图片）。
- 需要修改"范围"之外的文件。

## 维护说明

- logo 几何只存在两处：`BrandMark.vue`（可随主题变色）与 `favicon.svg` / 文档站 `mark.svg`（固定海军蓝正色）。改图形时三处同步。
- 深色背景上一律用反白版本（白色方印、底色字），不要把品牌色方印直接放在深色底上。
