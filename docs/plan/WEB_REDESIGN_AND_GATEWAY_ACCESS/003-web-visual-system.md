# 计划 003：白底纯色视觉体系——三套可切换主题色（默认海军蓝），深浅只作用于菜单栏，删除渐变与毛玻璃，统一字体

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- frontend/src/assets/main.css frontend/tailwind.config.js frontend/tailwind.config.ts frontend/index.html frontend/src/views/SettingsView.vue ai-elements-vue/packages/elements/src/shimmer ai-elements-vue/packages/elements/src/message/MessageAttachment.vue`
> 计划 001 改过 `App.vue`、`main.ts`（`bare` 分支、`router.isReady`），那是预期的。

## 状态

- **优先级**：P1
- **工作量**：L（机械替换多，但每一类都有 grep 可验证）
- **风险**：MED（全站视觉变化；靠真机截图与计算样式断言兜底）
- **依赖**：计划 001（真机验证脚手架）；决策 D1 已定：**三套主题色都做成可切换，默认 A 海军蓝**；决策 D4 已定：**系统无衬线**
- **类别**：direction / tech-debt
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求："整体色调太暗太深沉。背景色要白色，只在菜单栏体现深色主题和浅色主题即可"；"简洁、大气、必须用纯色，禁止出现渐变色、官方、正式"；
主题色先选定"A海军蓝"，随后又要求"是否可以加上主题色切换功能？支持切换A海军蓝、B松石青和C石墨黑"。
现状是一套青色系、整页随主题切换深浅的配色，大量使用渐变、半透明"毛玻璃"与柔和阴影，字体是一个从未加载的衬线体（中文落到系统宋体），
浅色主题下 shadcn 桥接变量还有几处取错了值。本计划把视觉基础整体换成：内容区永远白底、只用实色、三套可切换的品牌色（默认海军蓝）、
深浅主题只作用于菜单栏、界面统一系统无衬线字体。

方案预览见 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/brand-options.html`，三套色值与本计划一致。

## 现状

- `frontend/src/assets/main.css`（723 行）：
  - `:5-99` `:root` 定义 `--forebrain-*` 变量（青色品牌 `#0d91b0`，页面底色 `#eaf4f5`）以及 shadcn 桥接的 HSL 变量（`--canvas`、`--ivory`、`--ink`、`--olive`、`--stone`、`--sand`、`--cream-border`、`--terracotta`、`--terracotta-fg`、`--charcoal`、`--ring-focus`……）；
    `:90-93` 浅色主题下 `--secondary-foreground: 210 30% 92%`（浅字）、`--muted: 221 24% 15%` 与 `--accent: 220 28% 18%`（深底），用在浅色界面上是错的；
  - `:102-172` `.dark { ... }` 整页深色变量；
  - `:55-57`（浅色）与 `:148-150`（深色）三个渐变变量 `--forebrain-content-gradient`、`--forebrain-card-gradient`、`--forebrain-input-gradient`，`:595-597` 工具类 `.forebrain-terracotta` 也是渐变；
  - `:180`、`:240`、`:289`、`:626` 使用 `"Source Serif 4"`，全仓没有任何 `@font-face` 或字体链接加载它；
  - `@layer utilities` 中 15 个工具类没有任何使用者：`forebrain-parchment`、`forebrain-ivory`、`forebrain-sand`、`forebrain-terracotta`、`forebrain-ink`、`forebrain-charcoal`、
    `forebrain-olive`、`forebrain-stone`、`forebrain-border-cream`、`forebrain-border-warm`、`forebrain-crimson`、`forebrain-status-done`、`forebrain-status-run`、`forebrain-status-fail`、`forebrain-whisper-shadow`；
  - 5 个变量没有任何使用者：`--forebrain-badge-bg`、`--forebrain-brand-3`、`--forebrain-brand-outline-shadow-soft`、`--forebrain-nav-bg`、`--forebrain-nav-shadow`。
- `frontend/tailwind.config.js` 与 `frontend/tailwind.config.ts` 并存。Tailwind 3 按 `.js` → `.cjs` → `.mjs` → `.ts` 的顺序取第一个，所以 `.ts`（其中把 `sans` 也设成了衬线体）从未生效。
- `frontend/src/App.vue`（计划 001 之后）：`applyTheme()` 在 `<html>` 上切换 `dark` 类、设置 `colorScheme`、改写 `<meta name="theme-color">`（`#04111b` / `#eaf4f5`）；主题存于 `localStorage['forebrain-theme']`，缺省跟随 `prefers-color-scheme`。
- `frontend/index.html:6`：`<meta name="theme-color" content="#eaf4f5" />`。
- `frontend/src/views/SettingsView.vue`（644 行）：左栏 `:21-33` 是两张信息卡（健康检查、技能概览），右栏是技能管理、权限记忆等表单卡片。没有"外观"相关设置。
- 使用渐变或毛玻璃的位置（`grep -rnE "gradient|backdrop-blur|backdrop-filter"`）：
  `frontend/src/views/ChatView.vue`（渐变 8 处、`backdrop-blur-md` 1 处）、`frontend/src/components/ContextDebugPanel.vue`（7）、`frontend/src/components/chat/CompactionCard.vue`（3）、
  `frontend/src/views/AgentsView.vue`（2）、`frontend/src/components/chat/SubagentCard.vue`（1）、`frontend/src/components/chat/PlanUpdateCard.vue`（1）、`frontend/src/components/chat/AgentViewTabs.vue`（1）、
  `frontend/src/components/PendingInputQueuePopover.vue`（`backdrop-blur-xl`）、`frontend/src/components/ChatSidebar.vue`（`backdrop-blur-md`，该文件在计划 005 删除）、
  `ai-elements-vue/packages/elements/src/shimmer/Shimmer.vue`（渐变文字扫光）、`ai-elements-vue/packages/elements/src/message/MessageAttachment.vue`（`backdrop-blur-sm`）。
- 组件里硬编码的颜色（`grep -rnoE "#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3}\b|rgba?\([0-9 ,.]+\)" frontend/src --include='*.vue'`）约 110 处，集中在
  `ContextDebugPanel.vue`（23）、`DiffView.vue`（12）、`ChatView.vue`（10）、`CompactionCard.vue`（10）、`McpView.vue`（8）、`AgentRosterPanel.vue`（6）等。按色相归为四类：
  红 `rgba(160,70,70,a)` / `#dc2626` / `#e5484d` / `#fca5a5`（错误、删除行）、琥珀 `rgb(216,160,70)` / `rgba(200,150,60,a)` / `rgba(180,140,60,a)`（警告）、
  绿 `rgb(70,150,110)` / `#16a34a` / `#86efac`（成功、新增行）、青 `rgba(25,183,210,a)` / `#0ea5e9` / `rgba(9,52,74,a)`（旧品牌色与旧阴影）。
- 代码高亮：`frontend/src/lib/codeTheme.test.ts` 断言浅色代码主题 `monokailight` 的画布为 `#fafafa`。内容区永远白底后，页面上只会出现浅色代码主题。

## 设计

### 1. 与主题色无关的内容区变量（全部实色，只有一套，没有深色版本）

**保留现有 `--forebrain-*` 名字**以减少改动面，只改值、合并同义变量、删除多余变量：

| 变量 | 新值 | 说明 |
| --- | --- | --- |
| `--forebrain-bg`、`--forebrain-page-bg`、`--forebrain-surface`、`--forebrain-input-bg` | `#FFFFFF` | 白底 |
| `--forebrain-bg-alt` | `#F7F8FA` | 表头、工具卡片头等浅灰区 |
| `--forebrain-input-hover-bg`、`--forebrain-button-alt-bg` | `#F2F4F7` | 次级按钮、悬停 |
| `--forebrain-button-alt-hover-bg` | `#EAECF0` | 次级按钮悬停 |
| `--forebrain-divider` | `#E4E7EC` | 分隔线、卡片描边 |
| `--forebrain-divider-strong`、`--forebrain-rule-line` | `#D0D5DD` | 输入框描边、强分隔 |
| `--forebrain-text` | `#101828` | 正文 |
| `--forebrain-text-2` | `#475467` | 次级文字 |
| `--forebrain-muted-text` | `#667085` | 说明文字 |
| `--forebrain-on-brand` | `#FFFFFF` | 品牌底上的文字 |
| `--forebrain-danger` | `#B42318` | 错误文字与描边 |
| `--forebrain-warning` | `#B54708` | 警告文字与描边（新增） |
| `--forebrain-warning-soft` | `#FFFAEB` | 警告底（新增） |
| `--forebrain-success` | `#067647` | 成功文字（新增） |
| `--forebrain-success-soft` | `#ECFDF3` | 成功底、diff 新增行底（新增） |
| `--forebrain-code-bg` | `#FAFAFA` | 代码块底，与 `monokailight` 画布一致 |
| `--forebrain-code-surface` | `#F2F4F7` | 代码块头 |
| `--forebrain-code-text` | `#111111` | 代码正文 |
| `--forebrain-brand-scrollbar`、`--forebrain-brand-scrollbar-thumb` | `#D0D5DD` | 滚动条 |
| `--forebrain-shadow-pop` | `0 8px 24px rgba(16, 24, 40, 0.08)` | **唯一**允许的阴影，只用于下拉、弹窗等浮层（新增，取代 `--forebrain-doc-shadow`） |
| `--forebrain-ui-font` | 见第 6 条 | 界面字体 |
| `--forebrain-mono-font` | 不变 | 等宽 |

错误状态**不使用浅红/粉红填充**（它会落进 owner 禁用的粉色一族）：错误提示用白底或 `--forebrain-bg-alt` 底，配 `--forebrain-danger` 的文字与 1px 描边；diff 删除行用 `--forebrain-bg-alt` 底配 `--forebrain-danger` 文字。

### 2. 三套主题色（品牌相关变量）

主题色由 `<html>` 上的 `data-brand` 属性决定，取值 `navy`（A 海军蓝，默认）、`teal`（B 松石青）、`graphite`（C 石墨黑）。
`main.css` 写成 `:root, :root[data-brand="navy"] { … }`、`:root[data-brand="teal"] { … }`、`:root[data-brand="graphite"] { … }` 三块，每块定义下表全部变量：

| 变量 | A `navy` | B `teal` | C `graphite` |
| --- | --- | --- | --- |
| `--forebrain-brand-1` | `#1F4A9E` | `#0E6E7E` | `#1C1C1F` |
| `--forebrain-brand-hover` | `#173B80` | `#0A5A67` | `#3A3A40` |
| `--forebrain-brand-soft` | `#EBF0F9` | `#E7F3F4` | `#F1F1F2` |
| `--forebrain-brand-border` | `#C3D2EC` | `#B7DDE2` | `#D4D4D8` |
| `--forebrain-brand-border-strong` | `#8FA9DA` | `#6FB5BF` | `#A1A1AA` |
| `--forebrain-focus-border` | `#1F4A9E` | `#0E6E7E` | `#1C1C1F` |
| `--forebrain-ring-soft` | `#C3D2EC` | `#B7DDE2` | `#D4D4D8` |
| `--brand`（shadcn 用的 HSL） | `220 67% 37%` | `189 80% 27%` | `240 5% 12%` |

菜单栏变量按"主题色 × 深浅"六种组合定义，选择器为 `:root[data-brand="<名>"] .forebrain-app-shell[data-rail-theme="<dark|light>"]`
（`navy` 那两块同时写 `:root:not([data-brand])` 的对应选择器，作为未设置时的默认）。
**变量定义在外壳根元素上，而不是 `.forebrain-rail` 上**：计划 005 会把主代理下拉、心跳浮层、对话抽屉、折叠提示渲染在菜单栏元素之外（菜单栏自身 `overflow: hidden`），
它们中属于菜单栏的部分（主代理下拉、折叠提示）必须仍能取到 `--rail-*`；内容区的组件从不引用 `--rail-*`，所以定义在外壳上不会影响白底内容区。

| 变量 | A 深 | A 浅 | B 深 | B 浅 | C 深 | C 浅 |
| --- | --- | --- | --- | --- | --- | --- |
| `--rail-bg` | `#0F1D38` | `#F3F5F9` | `#0D2429` | `#F1F6F6` | `#141416` | `#F6F6F7` |
| `--rail-fg` | `#E7ECF5` | `#101828` | `#E4F0F1` | `#101828` | `#EDEDEF` | `#18181B` |
| `--rail-fg-2` | `#9FB0CC` | `#475467` | `#97B7BB` | `#475467` | `#A1A1A8` | `#52525B` |
| `--rail-line` | `#1E3055` | `#E1E5EC` | `#1B393F` | `#DFE7E8` | `#26262A` | `#E4E4E7` |
| `--rail-hover` | `#172A4D` | `#E8ECF3` | `#143238` | `#E6EEEF` | `#1E1E21` | `#ECECEE` |
| `--rail-active-bg` | `#1D3563` | `#E1E8F5` | `#1A3F46` | `#DCEBED` | `#2A2A2E` | `#E6E6E8` |
| `--rail-active-fg` | `#FFFFFF` | `#1F4A9E` | `#FFFFFF` | `#0E6E7E` | `#FFFFFF` | `#18181B` |
| `--rail-mark` | `#7EA2EA` | `#1F4A9E` | `#6CC7D3` | `#0E6E7E` | `#FFFFFF` | `#18181B` |
| `--rail-btn-bg` | `#3A67C6` | `#1F4A9E` | `#16808F` | `#0E6E7E` | `#FFFFFF` | `#1C1C1F` |
| `--rail-btn-hover` | `#4A75D0` | `#173B80` | `#1B90A0` | `#0A5A67` | `#E4E4E7` | `#3A3A40` |
| `--rail-btn-fg` | `#FFFFFF` | `#FFFFFF` | `#FFFFFF` | `#FFFFFF` | `#141416` | `#FFFFFF` |
| `--rail-field-bg` | `#152646` | `#FFFFFF` | `#122E34` | `#FFFFFF` | `#1C1C1F` | `#FFFFFF` |
| `--rail-pop-bg` | `#132447` | `#FFFFFF` | `#112C31` | `#FFFFFF` | `#1C1C1F` | `#FFFFFF` |

菜单栏内所有现有样式（`.forebrain-rail*`、`.forebrain-tenant*` 等，`main.css:202-436`）改为只引用 `--rail-*` 变量；
主代理下拉菜单（`.forebrain-tenant-menu`）在菜单栏内部，背景用 `--rail-pop-bg`、阴影用 `--forebrain-shadow-pop`。

需要**合并后删除**的变量（先把所有 `var(旧名)` 替换成右列，再删定义）：

| 旧变量 | 替换为 |
| --- | --- |
| `--forebrain-brand-2`、`--forebrain-brand-3` | `--forebrain-brand-1` |
| `--forebrain-surface-soft`、`--forebrain-surface-glass`、`--forebrain-surface-control`、`--forebrain-surface-strong`、`--forebrain-composer-bg`、`--forebrain-popover-bg` | `--forebrain-surface` |
| `--forebrain-bg-alt-soft`、`--forebrain-bg-alt-glass` | `--forebrain-bg-alt` |
| `--forebrain-input-bg-soft` | `--forebrain-input-bg` |
| `--forebrain-content-gradient`、`--forebrain-card-gradient`、`--forebrain-input-gradient` | `--forebrain-surface` |
| `--forebrain-doc-shadow` | 浮层（`popover`、`menu`、`dialog`、`dropdown`）上用 `--forebrain-shadow-pop`；其余位置删除该 `box-shadow` |
| `--forebrain-inset-shadow`、`--forebrain-inset-highlight`、`--forebrain-brand-inset-shadow`、`--forebrain-brand-outline-shadow`、`--forebrain-brand-outline-shadow-soft`、`--forebrain-brand-button-shadow`、`--forebrain-sidebar-shadow` | 删除所在的 `box-shadow`/`shadow-[...]`（描边已由 `border` 承担） |
| `--forebrain-badge-bg`、`--forebrain-nav-bg`、`--forebrain-nav-shadow` | 无使用者，直接删除 |
| `--forebrain-rail-bg` | 由上表 `--rail-bg` 取代 |

shadcn 桥接变量改名并修正取值（`tailwind.config.js` 里的 `hsl(var(--background))` 等保持不变，只改 `main.css`；`--brand` 在上面三块主题色里定义，其余只在 `:root` 定义一次）：

```css
  --page: 0 0% 100%;
  --surface: 0 0% 100%;
  --ink: 220 43% 11%;      /* #101828 */
  --ink-2: 216 18% 34%;    /* #475467 */
  --ink-3: 221 13% 46%;    /* #667085 */
  --subtle: 216 24% 96%;   /* #F2F4F7 */
  --line: 218 17% 91%;     /* #E4E7EC */
  --danger: 4 76% 40%;     /* #B42318 */
  --background: var(--page);
  --foreground: var(--ink);
  --card: var(--surface);
  --card-foreground: var(--ink);
  --popover: var(--surface);
  --popover-foreground: var(--ink);
  --primary: var(--brand);
  --primary-foreground: 0 0% 100%;
  --secondary: var(--subtle);
  --secondary-foreground: var(--ink);
  --muted: var(--subtle);
  --muted-foreground: var(--ink-3);
  --accent: var(--subtle);
  --accent-foreground: var(--ink);
  --destructive: var(--danger);
  --destructive-foreground: 0 0% 100%;
  --border: var(--line);
  --input: var(--line);
  --ring: var(--brand);
  --radius: 0.5rem;
```
删除 `--canvas`、`--ivory`、`--olive`、`--stone`、`--sand`、`--cream-border`、`--terracotta`、`--terracotta-fg`、`--charcoal`、`--ring-focus`、`--destructive-fg`。
在 `:root` 加 `color-scheme: light;`。**删除整个 `.dark { ... }` 块**。

### 3. 外观偏好：主题色与菜单栏深浅

新文件 `frontend/src/composables/useAppearance.ts`（模块级单例，写法仿照 `usePrimaryAgents.ts` 的模块级 `ref`）：

```ts
export type BrandScheme = 'navy' | 'teal' | 'graphite'
export type RailTheme = 'dark' | 'light'
export const BRAND_SCHEMES: BrandScheme[] = ['navy', 'teal', 'graphite']
// localStorage keys: 'forebrain-brand' (BrandScheme), 'forebrain-theme' (RailTheme)
export function applyStoredAppearance(): void   // main.ts 在 mount 之前调用一次
export function useAppearance(): {
  brand: Readonly<Ref<BrandScheme>>
  railTheme: Readonly<Ref<RailTheme>>
  setBrand(next: BrandScheme): void      // 写 localStorage + document.documentElement.dataset.brand
  setRailTheme(next: RailTheme): void    // 写 localStorage
  toggleRailTheme(): void
}
```
- 主题色缺省 `navy`；菜单栏深浅缺省跟随 `prefers-color-scheme`，并在用户未手动选择时随系统变化（保留 `App.vue` 现有的 `matchMedia` 监听逻辑，移入此文件）。
- `localStorage` 读写包在 `try/catch` 里（浏览器可能禁用存储；这是外部环境，不是自身逻辑的兜底），读不到时使用缺省值。
- 菜单栏深浅继续用键 `forebrain-theme`、值 `light`/`dark`，用户原有选择直接沿用（语义从"整页"变为"菜单栏"，无需迁移）。
- `main.ts`：在 `app.mount` 之前调用 `applyStoredAppearance()`，避免首屏先闪一下默认色。

`App.vue`：
- 删除 `theme`、`applyTheme`、`storedTheme`、`syncSystemTheme` 及对 `document.documentElement` 的 `dark` 类、`colorScheme`、`theme-color` 的操作，改用 `useAppearance()`；
- 外壳根元素 `<div class="forebrain-app-shell">` 绑定 `:data-rail-theme="railTheme"`（不是绑在 `<aside class="forebrain-rail">` 上，原因见第 2 节）；
- 顶栏的深浅切换按钮改为调用 `toggleRailTheme()`（它的位置由计划 005 移进菜单栏底部）；文案键 `theme.toLight` / `theme.toDark` 改为"菜单栏切换为浅色 / 深色"（中英两表）。

`SettingsView.vue`：左栏最上方新增一张"外观"卡片（与现有信息卡同样的边框与内边距）：
- **主题色**：三个并排的单选项（`role="radiogroup"`，每项 `role="radio"`、`aria-checked`），每项显示一块 28×28 的品牌色实色方块和名称
  "海军蓝 / 松石青 / 石墨黑"（英文 "Navy / Teal / Graphite"），点击立即 `setBrand()`，当前项有 2px 品牌色描边；
- **菜单栏**：两个单选项"深色 / 浅色"，点击 `setRailTheme()`；
- 文案键：`settings.appearance`、`settings.appearanceBrand`、`settings.appearanceRail`、`appearance.navy`、`appearance.teal`、`appearance.graphite`、`appearance.railDark`、`appearance.railLight`，中英两表都写。

`index.html`：`<meta name="theme-color" content="#ffffff" />`（内容区永远白底，不再随主题改写）。

### 4. 去掉渐变、毛玻璃与装饰阴影

- 上文"现状"列出的每一处 `gradient`、`backdrop-blur-*`、`backdrop-filter` 全部换成实色变量（渐变底 → `--forebrain-surface` 或 `--forebrain-bg-alt`；渐变强调条 → `--forebrain-brand-1` 实色）。
- `Shimmer.vue`：删除渐变文字扫光，改为对文字本身做透明度呼吸动画（`@keyframes` 在 `opacity` 1 ↔ 0.45 之间，1.6s，`ease-in-out`，`infinite`），并在 `@media (prefers-reduced-motion: reduce)` 下关闭动画；颜色继承 `currentColor`。
- `MessageAttachment.vue`：删除 `backdrop-blur-sm`，底色改为实色（`bg-background`）。
- `frontend/src` 下 `.vue` 文件里的 `shadow-[...]` 任意值阴影与 `box-shadow`：浮层保留并统一为 `shadow-[var(--forebrain-shadow-pop)]`，其他删除。

### 5. 硬编码颜色全部改用变量

`frontend/src` 下 `.vue` 文件里的十六进制与 `rgb()/rgba()` 颜色，按色相映射：

| 原色 | 用途 | 替换为 |
| --- | --- | --- |
| 红：`rgba(160,70,70,a)`、`#dc2626`、`#e5484d`、`#fca5a5` | 文字/图标/描边 | `var(--forebrain-danger)` |
| 同上 | 底色（a ≤ 0.12） | `var(--forebrain-bg-alt)`（不用浅红底，见第 1 条） |
| 琥珀：`rgb(216,160,70)`、`rgba(200,150,60,a)`、`rgba(180,140,60,a)` | 文字/描边 | `var(--forebrain-warning)` |
| 同上 | 底色 | `var(--forebrain-warning-soft)` |
| 绿：`rgb(70,150,110)`、`#16a34a`、`#86efac` | 文字/描边 | `var(--forebrain-success)` |
| 同上 | 底色 | `var(--forebrain-success-soft)` |
| 青：`#19b7d2`、`rgba(25,183,210,a)`、`#0ea5e9` | 强调 | `var(--forebrain-brand-1)`；底色用 `var(--forebrain-brand-soft)` |
| `rgba(9,52,74,a)`、`rgba(255,255,255,0.55)` 等阴影/高光 | 装饰 | 删除，浮层改用 `--forebrain-shadow-pop` |

Tailwind 调色板类（如 `bg-amber-500`）同样换成对应变量的任意值类。这样切换主题色时，所有强调色都跟着变。

### 6. 字体（D4：系统无衬线）

- `--forebrain-ui-font: -apple-system, BlinkMacSystemFont, "PingFang SC", "Hiragino Sans GB", "Segoe UI", "Microsoft YaHei", "Noto Sans SC", Roboto, "Helvetica Neue", Arial, sans-serif;`
- `body` 的 `font-family` 改为 `var(--forebrain-ui-font)`；`main.css` 其余 3 处 `"Source Serif 4"` 改为 `var(--forebrain-ui-font)`；删除 `.font-serif` 工具类的覆盖定义（`:626` 附近），
  组件里 13 处 `font-serif` 类名删除（标题层级靠字号与字重表达）。
- `tailwind.config.js` 的 `theme.extend.fontFamily` 设 `sans` 为同一字体栈；**删除 `frontend/tailwind.config.ts`**。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过（含 `codeTheme.test.ts`） |
| 前端构建 | `cd frontend && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` | 退出码 0 |
| 渐变与毛玻璃 | `grep -rnE "gradient\|backdrop-blur\|backdrop-filter" frontend/src ai-elements-vue/packages/elements/src --include='*.vue' --include='*.ts' --include='*.css'` | 无输出（`.test.ts` 除外） |
| 硬编码颜色 | `grep -rnoE "#[0-9a-fA-F]{6}\b\|#[0-9a-fA-F]{3}\b\|rgba?\([0-9 ,.]+\)" frontend/src --include='*.vue'` | 无输出 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 范围

**只允许修改**：
- `frontend/src/assets/main.css`、`frontend/tailwind.config.js`、删除 `frontend/tailwind.config.ts`、`frontend/index.html`、`frontend/src/main.ts`
- `frontend/src/composables/useAppearance.ts`（新建）、`frontend/src/composables/useAppearance.test.ts`（新建）
- `frontend/src/App.vue`（主题逻辑与菜单栏 `data-rail-theme` 绑定）、`frontend/src/views/SettingsView.vue`（外观卡片）、`frontend/src/locales/index.ts`
- `frontend/src/views/*.vue`、`frontend/src/components/**/*.vue` 中涉及渐变、毛玻璃、阴影、硬编码颜色、`font-serif` 的行
- `ai-elements-vue/packages/elements/src/shimmer/Shimmer.vue`、`ai-elements-vue/packages/elements/src/message/MessageAttachment.vue`
- `frontend/e2e/visual.spec.ts`（新建）

**不要碰**：
- 页面布局与组件结构（计划 005 负责对话页三栏；本计划只动颜色、字体、阴影和设置页新增的外观卡片）。
- `ai-elements-vue` 里其他组件的 `dark:` 类：内容区不再出现 `.dark` 祖先，它们自然不生效，不必逐个删除。
- 代码高亮主题（`@repo/elements/code-block` 的 monokai/monokailight 配对）与其测试。
- `frontend/public/*.svg`（计划 004）。

## 步骤

### 第 1 步：先写失败的视觉用例

新建 `frontend/e2e/visual.spec.ts`。对以下 13 个页面：`/`、`/agents`、`/projects`、`/cron`、`/channels`、`/permissions`、`/tools`、`/mcp`、`/providers`、`/hooks`、`/memories`、`/config`、`/settings`，
在默认主题色（未设置 `forebrain-brand`）下，分别以菜单栏深色、浅色两种状态（登录后先写 `localStorage['forebrain-theme']` 再 `reload`）执行：

1. `getComputedStyle(document.body).backgroundColor` 为 `rgb(255, 255, 255)`，`.forebrain-work` 的背景色也为 `rgb(255, 255, 255)`；
2. `.forebrain-rail` 的背景色：深色为 `rgb(15, 29, 56)`，浅色为 `rgb(243, 245, 249)`；点开主代理下拉，其背景色深色为 `rgb(19, 36, 71)`、浅色为 `rgb(255, 255, 255)`（不得为 `rgba(0, 0, 0, 0)`）；
3. 遍历 `document.querySelectorAll('*')`：没有任何元素的计算样式 `backgroundImage` 含 `gradient`，没有任何元素的 `backdropFilter` 不是 `none`；
4. `document.documentElement.classList.contains('dark')` 为 `false`；
5. `getComputedStyle(document.body).fontFamily` 以 `-apple-system` 开头；
6. 整页截图，命名 `visual-<页面>-navy-<dark|light>.png`。

再加一个用例 `brand scheme switch recolours the app and persists`：打开 `/settings`，在"外观"卡片依次点选三套主题色，每次断言：

| 选中 | `document.documentElement.dataset.brand` | 任一 `.forebrain-btn-primary` 背景色 | 深色菜单栏背景色 |
| --- | --- | --- | --- |
| 海军蓝 | `navy` | `rgb(31, 74, 158)` | `rgb(15, 29, 56)` |
| 松石青 | `teal` | `rgb(14, 110, 126)` | `rgb(13, 36, 41)` |
| 石墨黑 | `graphite` | `rgb(28, 28, 31)` | `rgb(20, 20, 22)` |

选中松石青后 `reload`，`dataset.brand` 仍为 `teal`（持久化）；每套主题色在对话页、菜单栏深浅两种状态各截一张图，命名 `visual-chat-<brand>-<dark|light>.png`；最后切回海军蓝。

**验证**：`scripts/acceptance/web_e2e.sh` → 失败，且失败断言来自 `visual.spec.ts`（证明用例能抓到旧样式）。

### 第 2 步：外观偏好与变量层

按设计第 1、2、3 条改 `main.css`、新建 `useAppearance.ts`、改 `main.ts`、`App.vue`、`SettingsView.vue`、`index.html`、`locales/index.ts`。
先做"合并后删除"表里的全局替换（可用 `grep -rl 'var(--forebrain-surface-glass)' frontend/src | xargs sed -i '' 's/var(--forebrain-surface-glass)/var(--forebrain-surface)/g'`，逐个旧名执行），再删定义。
删除 15 个无人使用的工具类与 5 个无人使用的变量。新增 `useAppearance.test.ts`：缺省主题色为 `navy`；`setBrand('teal')` 后 `document.documentElement.dataset.brand === 'teal'` 且 `localStorage` 写入；
`localStorage` 中是非法值 `pink` 时回落为 `navy`。

**验证**：
- 对"合并后删除"表的每个旧变量名执行 `grep -rn -- "<旧名>" frontend/src ai-elements-vue/packages/elements/src` → 无输出；
- `grep -n "^\.dark" frontend/src/assets/main.css` → 无输出；
- `cd frontend && corepack pnpm test && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` → 通过。

### 第 3 步：去掉渐变、毛玻璃、装饰阴影

按设计第 4 条逐文件修改。

**验证**：上表"渐变与毛玻璃"命令无输出；`cd frontend && corepack pnpm test` 通过。

### 第 4 步：硬编码颜色改用变量

按设计第 5 条逐文件修改。

**验证**：上表"硬编码颜色"命令无输出；`grep -rnoE "(bg|text|border|ring|from|to|via)-(red|rose|pink|fuchsia|purple|violet|indigo|blue|sky|cyan|teal|emerald|green|lime|yellow|amber|orange)-[0-9]{2,3}" frontend/src --include='*.vue'` → 无输出。

### 第 5 步：字体

按设计第 6 条修改；删除 `tailwind.config.ts`。

**验证**：`grep -rn "Source Serif\|Iowan\|font-serif" frontend/src frontend/tailwind.config.js` → 无输出；`test ! -e frontend/tailwind.config.ts` → 退出码 0。

### 第 6 步：真机验证

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`；截图目录中有 26 张 `visual-*-navy-*.png` 与 6 张 `visual-chat-<brand>-*.png`。把三套主题色的对话页截图路径写进 README 本计划状态说明。

## 完成标准（全部满足）

- [x] `cd frontend && corepack pnpm test` 全部通过（169/169）
- [x] "渐变与毛玻璃""硬编码颜色"两条 grep 无输出（唯一保留：LoginView 自带 `--lp-*` 色板定义——bare 页面在 shell 之外、无法引用外壳变量，属计划 001 的既定设计；其余组件零字面量）
- [x] 该 grep 无输出
- [x] 计数 6（teal/graphite 各 3 处：品牌块 + 深/浅 rail 块）
- [x] `test ! -e frontend/tailwind.config.ts` 通过
- [x] `web e2e: PASS`（10/10 用例，64s；26 张 `visual-*-navy-*.png` + 6 张 `visual-chat-<brand>-*.png`）
- [x] 两者均无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上。
- 某个硬编码颜色无法归入上表任何一类（例如图表的多系列配色）——列出位置与用途，停下来让 owner 定色。
- 去掉某处渐变后，界面上原本靠渐变区分的两种状态变得无法区分。
- 删除 `.dark` 块后，发现有逻辑（不只是样式）依赖 `<html>` 上的 `dark` 类。
- 石墨黑方案下某处"品牌色文字放在品牌浅底上"对比度不足（肉眼在截图中难以辨认）——截图标出位置后停下。
- 需要修改"范围"之外的文件。

## 维护说明

- 新增颜色一律先在 `main.css` 定义变量再使用；组件里不得出现十六进制或 `rgb()` 字面量——上面两条 grep 可以直接放进代码审阅清单。
- 凡是"强调色"都必须引用 `--forebrain-brand-*` 或 `--rail-*`，否则切换主题色时它不会跟着变。新增一套主题色 = 在 `main.css` 加一块 `data-brand` 变量、六块中的两块菜单栏变量、`BRAND_SCHEMES` 加一项、两张语言表各加一个名字。
- 菜单栏以外的区域没有深色模式；任何新组件都按白底设计。错误态不使用浅红底色（owner 禁用粉色一族）。
- 主题色是浏览器本地偏好（`localStorage`），不同浏览器、不同设备各自独立，不写入 gateway 配置。
- 计划 004 的 logo、计划 005 的新菜单栏都直接使用本计划定义的 `--forebrain-brand-1` 与 `--rail-*` 变量。
