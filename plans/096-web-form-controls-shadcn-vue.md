# Plan 096: 全站表单控件与表单按钮改用 shadcn-vue 组件（分批迁移）

> **Executor instructions**: 按批次顺序执行，每批跑完本批的验证命令再进下一批。
> 批次之间的**代码**互不依赖（都依赖批次 0），但 e2e 里 `selectOption → pickOption` 的改写
> 跟着"这个控件第一次被迁移"的那一批走（每批的要点里点了名）——回滚某一批时，
> 它改过的那几行 spec 要一起回滚。全文共 12 批，**没有批次 9**（切分留下的空号）。
> 遇到 "STOP conditions" 里的任一情况，停下来报告，不要临场发挥。
> 做完一批就把 Done criteria 里已经成立的那几项勾上，并在 `plans/README.md` 的本行更新状态
> （索引由 owner 维护：若你被要求不要动索引，就只改本文件）。
>
> **Drift check（先跑）**: `git rev-parse --short HEAD` 应为 `ca8ce5b` 或其后代；
> 然后逐条比对下面 "Current state" 的摘录与真实文件。不一致就当成 STOP。
>
> **工作区状态提醒**: 本计划写下时工作区里已有未提交改动，其中与本任务直接相关的有五份，
> 都是本计划要仿照或要改的样板：`frontend/src/views/ProvidersView.vue`、
> `frontend/e2e/providers-keys.spec.ts`（迁移样板）、
> `ai-elements-vue/packages/shadcn-vue/components/ui/select/SelectContent.vue`（那处 `max-h` 修复）、
> `frontend/src/views/ProvidersView.test.ts`（批次 0 要改的 helper 调用点）、
> `frontend/src/locales/index.ts`（批次 11 要加一条文案）。
> **不要回滚、不要重写它们**，也**不要**对工作区做 `git checkout/stash`。
> 另外 `pkg/gateway/dist/assets/*` 里有一批条目处于 **deleted** 状态——那是既有状态，
> 别恢复、别提交、别当成本计划造成的。

## Status

- **Priority**: P2
- **Effort**: L（12 批，每批 S–M；批次 10 / 12 是 M–L）
- **Risk**: MED–HIGH（纯前端，但批次 10 换的是每一个弹窗/遮罩抽屉的外壳：焦点、Escape、遮罩点击）
- **Depends on**: none（批次 0 是表单批次的前置；批次 10 / 11 独立）
- **Category**: migration
- **Planned at**: commit `ca8ce5b`, 2026-10-21

## Why this matters

owner 2026-10-21 的裁决：**「所有的表单组件都必须使用 shadcn-vue 的原生表单组件，必须全部替换」**，
并在同一次追问里选择「连按钮也换」；同一天又追加两条：**「所有抽屉和弹窗必须改成 shadcn-vue
原生组件」**，以及**「子代理抽屉和工作台抽屉必须新增拖拽左侧边框上的把手自定义伸缩抽屉宽度的功能」**。
理由是同一个语义不能有两套实现：项目自造的
`<input class="forebrain-field">`、`<select>`、`<input type="checkbox">`、
`<button class="forebrain-btn">`、`<div class="forebrain-modal-mask">` 与库里的
`Input / Select / Checkbox / Button / Dialog / Sheet` 各写一份，
样式与行为必然漂移（焦点环、禁用态、焦点陷阱、Escape、可访问性、键盘行为都会分叉）。

今天的状态（2026-10-21 在 `ca8ce5b` 上实测，命令见「清单」）：`frontend/src` 里还有
**132 处**原生表单控件（不含 composer，见 Scope）、**385 处** `forebrain-btn` 落在**非 `Button`**
的标签上（60 个 `.vue` 文件；其中 377 处是原生 `<button>`，8 处是库里已有的
`ConfirmationAction`，后者不是缺陷、门禁豁免）、**24 个文件**用着自造的 `ForebrainModal`、
**4 个文件**用着自造的 `ForebrainDrawer`，而工作台（340px）与子代理面板（440px）的宽度是
写死的、不能拖。
`ProvidersView.vue` 的模型服务弹窗已经按目标形状迁移完毕（本计划就是把它当作样板推广）。

迁完之后：表单控件、遮罩弹窗、遮罩抽屉都只有一种实现（库组件 + 项目设计类），右侧两个并排面板
可以拖左缘把手改宽度（本会话有效），全站键盘/焦点/禁用/无障碍行为一致，
`radius.spec.ts` 这类全局几何门禁继续有效。

## Current state

### 样板：已经迁好的模型服务弹窗（照着写）

`frontend/src/views/ProvidersView.vue:112-124`：

```vue
<Label class="mb-1 block text-[12px] font-normal text-[var(--forebrain-text-2)]" for="provider-name">{{ t('providers.providerLabel') }}</Label>
<Select :model-value="form.provider" @update:model-value="pickProvider">
  <SelectTrigger
    id="provider-name"
    class="h-9 w-full rounded-md border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 text-[13px] text-[var(--forebrain-text)]"
    data-testid="provider-name"
  >
    <SelectValue :placeholder="t('providers.providerPlaceholder')" />
  </SelectTrigger>
  <SelectContent>
    <SelectItem v-for="name in providerOptions" :key="name" :value="name">{{ name }}</SelectItem>
  </SelectContent>
</Select>
```

> 注意：这一处是**手写工具类**的写法。本计划其余批次改用**设计类**
> （`class="forebrain-field …"`），理由见下一节——两者视觉等价，但后者更短、更少漂移。
> 执行者**不要**回头改写 `ProvidersView.vue` 的这两处。

### 为什么 `class="forebrain-field"` 能压过库组件的默认样式（这是本计划的地基）

- `frontend/src/assets/main.css:971` 定义了字段形状：

```css
.forebrain-field {
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-input-bg);
  padding: 0 10px;
  font-family: var(--forebrain-ui-font);
  font-size: 13px;
  color: var(--forebrain-text);
}
```

- `frontend/src/assets/main.css:929` 起是按钮形状（`.forebrain-btn`、`-primary`、`-ghost`），
  写在 `@layer utilities { … }` 里。
- **实测**（构建产物 `pkg/gateway/dist/assets/index-*.css`，本计划写时的那份）：产物里
  `@layer` 出现 **0 次**（Tailwind 3.4 会把用户 CSS 里的 `@layer` 展开成普通规则），
  且 `forebrain-btn` 定义在 `46261`、`forebrain-field` 在 `62117`，都排在
  工具类 `.h-9`(15373)、`.rounded-md`(27627)、`.border-input`(29199) **之后**。
  同特异性、后者胜 —— 所以设计类会覆盖库组件的默认外观，**迁移前后应逐像素一致**。
- 库组件内部用 `cn()`（`clsx` + `tailwind-merge`）拼 class：你传进去的工具类在最后，
  冲突的工具类（如 `h-9` 对 `h-[34px]`、`gap-2` 对 `gap-1.5`）会由 `tailwind-merge`
  丢掉前者，同样是我们胜。

复验命令（任何一次构建后都可跑，输出中 `forebrain-field` 的偏移必须大于 `.h-9`）：

```bash
python3 - <<'PY'
import glob
f = sorted(glob.glob('pkg/gateway/dist/assets/index-*.css'))[-1]
s = open(f).read()
print('native @layer:', s.count('@layer'))
for probe in ['.h-9{', '.rounded-md{', '.forebrain-btn{', '.forebrain-field{']:
    print(probe, s.find(probe))
PY
```

> 若 `forebrain-field` 的偏移不再大于 `.h-9`，说明构建顺序变了 —— 那是 STOP，
> 因为整份计划的"零视觉差异"前提失效。

### 这条地基只管**普通**工具类，管不了两种例外（照抄会漂移，必须按这里的写法）

"设计类排在后面所以赢"成立的前提是**同特异性**。库组件里有两类类的特异性是 0,2,0
或更高，设计类压不住；还有一类是设计类自己没写的属性（background / height / gap / padding），
库的默认值会留下来。下面三条是实测过的，全部按"改法"那一列写，**不要**靠再加设计类糊。

1. **`SelectTrigger` 的 `data-[size=default]:h-9`**（`select/SelectTrigger.vue`，编译产物里是
   `.data-\[size\=default\]\:h-9[data-size=default]{height:2.25rem}`，偏移 **90470**，
   排在 `.forebrain-field`（62117）之后，特异性 0,2,0）。
   后果：`class="forebrain-field h-[34px]"` 的下拉**永远是 36px**，`tailwind-merge` 也不会
   把它和 `h-[34px]` 合并（不同 modifier 组，两者都留下）。
   **改法**：今天 34px 的下拉写 `class="forebrain-field !h-[34px]"`（Tailwind 的 `!`
   重要前缀，v3.4 支持）；今天 36px（`h-9`）的下拉写 `class="forebrain-field h-9"`，
   正好等于库的默认值，不用 `!`。
2. **`Button` 的默认 variant/size**（`button/index.ts`：`variant: 'default'` →
   `bg-primary text-primary-foreground shadow-xs`；`size: 'default'` → `h-9 px-4 py-2`）。
   而 `.forebrain-btn` 只写 `min-height: 2rem`、**不写 `height`、不写 `background`**，
   于是：普通按钮会变成品牌实心 + 36px 高（今天是无底色、32px）。
   **改法**：每个 `<Button>` 都按下面第 5 条的三段式写，一个都不能少。
3. **焦点外观**：库的 `Input`/`Textarea`/`SelectTrigger` 自带 `outline-none` +
   `focus-visible:ring-[3px]`（0,2,0，`box-shadow` 组合，`.forebrain-field{box-shadow:none}`
   压不住它）。今天 `main.css:966-970` 的注释明说"焦点交给浏览器 outline"。
   **本计划的选择：接受库的 3px ring**（浏览器 outline 与 ring 都是"焦点可见"，
   `radius`/`visual` 两道门禁都不看焦点态），并在批次 0 把 `main.css` 那段注释改成
   "焦点由库组件的 ring 提供"——**不要**为了"逐像素一致"去写 `outline: auto`，
   那会在 ring 之外再套一圈浏览器描边。

### 库能给的、和它不直接给的

1. **`SelectItem` 的 value 不能是空串**：reka-ui 会抛
   `A <SelectItem /> must have a value prop that is not an empty string`。
   项目里有多个下拉框的合法取值就是空串（`''` = Chat Completions、空档位、空投递目标），
   必须走"哨兵值"映射（批次 0 提供 `frontend/src/lib/selectValue.ts`）。
2. **`Switch` 的滑块被写死成 `size-4`（16px）**。设计稿与今天的实现都是
   32×18 药丸 + **14px 滑块 + 2px 内缩**：
   `frontend/src/components/common/SwitchComponent.vue:42-79`（`<style scoped>`）与
   `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html:205-208` 是同一组数字。
   库组件只暴露了 `#thumb` **插槽**（插槽只能换内容，换不了类），所以批次 0 给 `Switch.vue`
   加一个 `thumbClass` prop（3 行，符合该库其余组件"`props.class` 合并到最后"的既有写法）。
3. **`NumberField` 会画出 +/− 按钮**，设计稿的数字输入器（`min="1"` 的次数/分钟）没有这两个按钮。
   数字输入一律用 `Input type="number"`，**不要**用 `NumberField`。
4. **库的 class 是按 Tailwind v4 写的**，本项目是 3.4：`max-h-(--var)`、`w-(--var)`、
   `outline-hidden`、`*:` 这类在 3.4 里**不产出任何 CSS**。已经踩过一次：
   `ui/select/SelectContent.vue` 的 `max-h-(--reka-select-content-available-height)`
   在真机里让下拉长到 68×32px、所有选项被推出窗口（工作区里那行已改成
   `max-h-[var(--reka-select-content-available-height)]`，**保留这个改法**）。
5. **`Button` 渲染的是裸 `<button>`**，`type` 不会自动给。项目里有两个真 `<form>`
   （`src/components/project/ProjectOverview.vue:6`、`src/views/LoginView.vue:32`），
   迁移后必须显式写 `type="button"`，否则点一下会提交表单。

### 遮罩弹窗、遮罩抽屉与两个并排面板（批次 10 / 11 的事实）

**`ForebrainModal.vue`（24 个文件在用）**
- 结构：`Teleport to="body"` → `.forebrain-modal-mask`（fixed inset 0、z-50、32% 暗色遮罩、grid 居中）
  → `.forebrain-modal`（560px，`--sm` 440 / `--xl` 860，`max-height` 660 / 780，12px 圆角），
  头里是标题 + 关闭按钮，body 滚动，footer 一行。
- 这整套 CSS 写在同文件的 **`<style scoped>`** 里 —— 换成库组件后会失效，必须搬进 `main.css`。
- 弹窗栈：`modalStack.ts` 的 `pushModal/popModal/isTopModal` 保证"遮罩点击与 Escape 只关最上面那一个"，
  关闭后焦点回到打开它的元素。`ForebrainModal.test.ts` 用五条用例钉住这些行为
  （第一个用例断言关着时 DOM 里没有 `.forebrain-modal`）。

**`ForebrainDrawer.vue`（4 个文件在用：`McpServerDrawer`、`SubagentConfigDrawer`、`LspTab` 的详情、gallery）**
- 结构：`.forebrain-drawer-mask` → `.forebrain-drawer`（**560px**、左侧 1px 分隔线、`-8px 0 24px` 阴影、
  160ms 滑入动画），头/体/脚三段。
- 这些 CSS **已经在** `frontend/src/assets/main.css:1557-1630`（全局），库组件带上类名就基本还原样
  ——**两处例外**：库的 `SheetContent` 带 `gap-4`（今天三段之间没有 gap，要 `gap: 0`），
  以及 `main.css:1625` 那条 `select.forebrain-field:disabled` 是元素选择器
  （`SelectTrigger` 渲染 `<button>`，改 `[data-disabled]`，见批次 12）。

**两个并排面板（批次 11，不是遮罩抽屉，owner 2026-10-21 确认保持内嵌）**
- `frontend/src/components/chat/ChatWorkbench.vue`：`<aside class="chat-workbench">`，
  `width: 340px`（`:352`），窄屏 `@media (max-width:1023px)` 时 `width: min(340px, calc(100vw - 64px))`
  并带 `--overlay` 变体（fixed）。
- `frontend/src/components/chat/SubagentDrawer.vue`：`<aside class="subagent-drawer">`，
  `width: 440px`（`:277`），同样有 `--overlay`（`position:fixed; right:0; z-index:44`）。
- 两个面板互斥（同一时刻只开一个），都有左缘 1px 分隔线与同一个滑入动画。
- **没有**任何原生表单控件（本计划的表单批次不会碰它们），它们缺的只有"可拖宽度"。

**库的 `Dialog` / `Sheet` 有五件事会咬人**（`ai-elements-vue/packages/shadcn-vue/components/ui/`）
1. 它们走 `DialogPortal` 渲染到 body：组件内 `<style scoped>` 不再命中 → 遮罩/面板的 CSS 必须是全局类
   （`.forebrain-drawer*` 已经是；`.forebrain-modal*` 要搬）。注意 `<Teleport>` 的单元测试桩
   （`ForebrainModal.test.ts:14` 的 `global.stubs.teleport`）**拦不住 reka 的 portal**，
   换成库之后内容真的会进 `document.body`，测试必须改成 `document.querySelector(...)`。
2. **遮罩拿不到类，而且不再是面板的祖先**。`DialogContent.vue` 内部是裸 `<DialogOverlay />`
   （不转发任何 class），`SheetContent.vue` 同理；外层组件没有任何把手给遮罩。
   结构也从"`mask > panel`"变成"`overlay` 与 `content` 是兄弟"。
   后果：`.forebrain-modal-mask` 这个类在库结构里**无处可放**，而
   `e2e/radius.spec.ts:32` 正是靠 `el.closest('.forebrain-modal-mask')` 把整块弹窗
   （12px 圆角、只允许 composer textarea）排除在几何门禁之外 → 遮罩类一没，
   `.forebrain-modal{border-radius:12px}` 立刻变成违规项，`radius.spec` 必红。
   **这是本计划唯一必须改门禁选择器的地方，见批次 10 的决定。**
3. `DialogContent.vue` / `SheetContent.vue` **自带一个右上角的关闭按钮**（`DialogClose` + `X`，
   里面是 `<span class="sr-only">Close</span>`，**没有 `aria-label`**），而设计稿的关闭按钮在头栏里。
   外壳必须把自己的关闭按钮留着、把库自带的那个藏掉，否则一个弹窗会有两个 X。
4. 默认样式按 v4 写：`DialogContent` 上是 `rounded-lg`、`gap-4`、**`p-6`**、`bg-background`、
   `sm:max-w-lg`、`shadow-lg`；`SheetContent` 上有 `gap-4`；`DialogOverlay`/`SheetOverlay` 是
   `bg-black/80`。今天 `.forebrain-modal` **没有 padding、没有 gap**（`ForebrainModal.vue:106-124`），
   抽屉的三段之间也没有 gap → "原值搬"搬不出这些，必须在 main.css 里显式 `padding: 0; gap: 0`。
   遮罩色只能靠全局 `[data-slot="dialog-overlay"]` / `[data-slot="sheet-overlay"]` 规则覆盖。
5. **响应式 max-width 要在同一断点覆盖**：库给的是 `w-full max-w-[calc(100%-2rem)] sm:max-w-lg`，
   只写一个不带 `sm:` 的 `max-w-[560px]` 在 ≥640px 时**打不过** `sm:max-w-lg`
   （`.forebrain-modal{width:560px;max-width:100%}` 写在 main.css 末尾则天然覆盖，选这条）。

**选择器耦合（换壳后仍要成立的，和必须改的那一条）**
- `.forebrain-modal`：**保留**。5 个 e2e 文件（`tenant-shell`、`projects-modals`、`radius`、
  `settings-tabs`、`providers-keys`）、12 个 `src` 文件引用；它挂在 `DialogContent` 的 `class` 上。
- `.forebrain-drawer`：**保留**，挂在 `SheetContent` 的 `class` 上（3 个 e2e、8 个 `src` 文件）。
- `.forebrain-modal-mask`：**这个类会消失**（见第 2 条）。全仓引用只有三处真依赖：
  `e2e/radius.spec.ts:32`、`ForebrainModal.test.ts:29,31`；
  `e2e/settings-tabs.spec.ts:330` 的选择器串里还有 `.forebrain-modal` 兜着，不用改。
- `chat-workbench`：8 个 e2e（`workbench`、`subagent-config`、`subagent-drawer`、`workbench-history`、
  `shell-chrome`、`layout`、`workspace-tree`、`skill-workshop`）；`subagent-drawer`：5 个 e2e。
  这两个类归批次 11，本批不许动。

### 清单（先跑命令，不要相信任何记忆里的数字）

```bash
cd frontend
# 本方决定范围内的原生控件（composer 已合规，见 Scope，故排除）
grep -rn '<input\|<select\|<textarea' src --include=*.vue | grep -v ForebrainPromptTextarea | wc -l   # 132
# 批次分布
for d in chat common cron mcp memory project providers settings skills subagents workbench workshop; do
  echo "$d: $(grep -rn '<input\|<select\|<textarea' src/components/$d --include=*.vue | wc -l)"; done
grep -rn '<input\|<select\|<textarea' src/views --include=*.vue | wc -l                                    # 26
# 落在非 <Button> 标签上的设计按钮：单行 grep 只能看到一小部分（多行开标签漏掉），
# 用下面这段 python 才是全量（门禁口径今天 377 处、59 个文件；未豁免口径 385 处、60 个文件）
python3 - <<'PY'
import glob, re
# 豁免名单：Button 是本计划的目标组件；ConfirmationAction 是库里已有的行动作
# （ApprovalCard/ApprovalGateCard 用它，本身带 forebrain-btn 设计类是对的），
# 它不该被"必须换成 Button"这条规则判为越界——把它算成违规，门禁永远到不了 0。
OK = {'Button', 'ConfirmationAction'}
hits = 0
files = set()
for path in glob.glob('src/**/*.vue', recursive=True):
    text = open(path).read()
    for m in re.finditer(r'forebrain-btn', text):
        start = text.rfind('<', 0, m.start())
        tag = re.match(r'<([A-Za-z][\w.-]*)', text[start:]) if start >= 0 else None
        if not tag or tag.group(1) not in OK:
            hits += 1
            files.add(path)
print(hits, len(files))
PY
```

各批的分布（2026-10-21 实测，括号里是接手它的批次；**每个文件都必须恰好属于一批**）：

| 位置 | 控件数 | 文件数 | 批次 |
|---|---|---|---|
| `src/views/*` | 26 | 11 | 5（`CronView` 5）、8（其余 21，含 `LoginView`/`ProvidersView` 各 1） |
| `src/components/project/*` | 22 | 12 | 4 |
| `src/components/mcp/*` | 18 | 3 | 2（`McpServerDrawer` 14 + `McpServersTable` 2）、0（`McpKeyValueRows` 2） |
| `src/components/subagents/` | 12 | 1 | 3 |
| `src/components/settings/*` | 10 | 5 | 1 |
| `src/components/cron/` | 9 | 2 | 5 |
| `src/components/chat/` | 8 | 8 | 6 |
| `src/components/ChatDrawer.vue`（不在 `chat/` 目录里） | 1 | 1 | 6 |
| `src/components/workshop/` | 8 | 5 | 7 |
| `src/components/common/*` | 3（+ `SwitchComponent`/`RadioField` 两个自造件的内部重写） | 4 | 0 |
| `src/components/workbench/` | 3 | 2 | 7 |
| `src/components/memory/MemoryFilesPanel.vue` | 2 | 1 | 12 |
| `src/components/providers/ModelChipsInput.vue` | 1 | 1 | 0 |
| `src/components/skills/*` | 4（`SkillsTable` 2 个复选框、`InstallDialogs` 2 个输入） | 2 | 12 |
| `src/components/editor/FrontmatterFields.vue` | 1（front matter 的字段输入；**不是** CodeMirror） | 1 | 12 |
| `src/gallery/ComponentGallery.vue` | 3 | 1 | 0 |
| `src/App.vue` | 1 | 1 | 8 |

合计 132。**这张表就是 Scope 的清单**：表里任何一行在批次 12 的门禁里还留有原生控件，
就是没做完，不是"门禁太严"。

### 测试面（迁移会打破它们，必须同步改）

- **e2e 里 10 个 spec 共 22 处真调用 `selectOption(...)`**（原生 `<select>` 才支持）：
  `agent-tables`1、`cron-builder`11、`cron-fire`2、`mcp-pages`1、`project-space-tables`1、
  `rules-permissions`2、`settings-tabs`1、`skills-lifecycle`1、`subagent-config`2。
  （第 23 处命中是 `providers-keys.spec.ts:23` 的**注释**里那个词，不是调用；
  改完之后门禁要用 `grep -rn "\.selectOption(" ` 而不是裸词，见 Done criteria。）
- **单测里 8 处把元素断言成 `HTMLSelectElement` / 读 `.options`**：
  `settings/AppearanceTab.test.ts:54`、`settings/HooksTab.test.ts:95,128`、
  `subagents/SubagentConfigDrawer.test.ts:100,106`、`cron/ScheduleBuilder.test.ts:17`、
  `views/CronView.test.ts:180`、`project/ProjectRules.test.ts:109-110`、`views/RulesView.test.ts:88-90`。
- **`e2e/radius.spec.ts` 是全国性的几何门禁**：它遍历 11 条路由、11 个设置标签、10 个项目标签
  和一批弹窗，断言"表单控件 6px、容器 8px、chips 5px、composer 12px"，并禁止 16/10/9/7px。
  它的 `FORBIDDEN` 在 `:19`，表单控件的 6px 正好等于库的 `rounded-md`（`--radius: 0.5rem`
  → `calc(0.5rem - 2px)` = 6px，`main.css:70`），所以迁移不该动它。
- `e2e/visual.spec.ts` 断言内容区永远白色、无渐变、三个品牌色一致 —— 迁移同样不该动它。

### 一处与设计稿的有意偏离（记录在案，别"修回去"）

设计稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html:592` 画的是
**原生复选框**（`input[type="checkbox"]{width:15px;height:15px;accent-color:var(--brand)}`）。
owner 2026-10-21 的裁决（全部换成库组件）覆盖了这条：复选框迁到库的 `Checkbox` 后
是 16px、`rounded-[4px]`、选中态"品牌底色 + 白色对勾"，看起来会和设计稿不同。
**这是 owner 的裁决，不许为了"1:1"把原生复选框保留下来。** 其余控件（输入框、下拉、文本域、
开关、单选、按钮）在迁移后必须与今天逐像素一致——**两个登记在案的例外**：
① 焦点态由库的 3px ring 提供，不再是浏览器 outline（见"地基"第 3 条）；
② 库的默认尺寸类必须按"地基"第 1、2 条抵消（`!h-[34px]`、`h-auto`），不抵消就会差 2–4px。

## Commands you will need

| 用途 | 命令 | 成功标志 |
|---|---|---|
| 单文件单测 | `cd frontend && corepack pnpm vitest run <文件>` | 全绿 |
| 全量单测 | `cd frontend && corepack pnpm test` | 基线 **136 文件 / 967 用例**全过 |
| 类型检查 | `cd frontend && corepack pnpm exec vue-tsc --noEmit` | exit 0、无输出 |
| 构建 | `cd frontend && corepack pnpm run build` | `✓ built in …` |
| 真机 e2e（按批） | `FOREBRAIN_E2E_SPEC="e2e/<spec>.spec.ts" scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |
| 真机 e2e（全量，仅批次 12） | `scripts/acceptance/web_e2e.sh`（不带 `FOREBRAIN_E2E_SPEC`） | 末行 `web e2e: PASS`，约 9 分钟 |
| 几何门禁 | `FOREBRAIN_E2E_SPEC="e2e/radius.spec.ts e2e/visual.spec.ts" scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

> e2e 每次会重新构建前端并把产物放到临时目录，**不会**改 `pkg/gateway/dist`；
> `pnpm run build` 会写 `pkg/gateway/dist`（这是既有行为，产物属 owner，别清理别提交）。

## Scope

**In scope**

- `frontend/src/**/*.vue` 里除下列"已合规/非表单"之外的**所有**原生
  `<input>`、`<select>`、`<textarea>`、`<input type="checkbox">` 与
  `class` 含 `forebrain-btn` 的原生 `<button>`。
- `frontend/src/components/common/{RadioField,SwitchComponent,ConfirmDialog,TablePager}.vue`
  —— 公共控件重写为库组件（保持对外 props 与 `data-testid` 不变）。
  `SwitchRow.vue` **不在**这份清单里：它本身没有原生控件，只是 `SwitchComponent` 的调用方，
  一行都不改。
- `frontend/src/components/common/ForebrainModal.vue`、`ForebrainDrawer.vue` —— **只换内部实现**
  （`Dialog` / `Sheet`），对外 props、插槽、类名、`modalStack` 的栈规则、焦点回还全部不变；
  24 + 4 个调用点一行都不改。
- `frontend/src/components/common/modalStack.ts` —— 只允许在注释里补一句"栈规则对库组件同样生效"，
  逻辑不改。
- `frontend/src/components/chat/ChatWorkbench.vue`、`frontend/src/components/chat/SubagentDrawer.vue`
  —— 加左缘拖拽把手 + 宽度绑定（布局、标签、内容一行不改）。
- `frontend/src/composables/usePanelWidth.ts`（新建）—— 两个面板共用的宽度状态与拖拽算法。
- `frontend/src/components/providers/ModelChipsInput.vue` —— chips 输入改库的 `TagsInput`。
- `frontend/src/gallery/ComponentGallery.vue` —— 组件库示例页同步。
- `frontend/src/lib/selectValue.ts`（新建）、`frontend/src/lib/selectValue.test.ts`（新建）、
  `frontend/src/test-utils/formControls.ts`（新建）、
  `frontend/src/composables/usePanelWidth.ts` 与其 `.test.ts`（新建）、
  `frontend/e2e/support.ts`（加一个 `pickOption` 辅助）。
- `frontend/src/assets/main.css` —— 允许：两行 `box-shadow: none` + 焦点注释更新（批次 0）、
  把 `.forebrain-modal*` 从组件的 scoped 样式**原值搬来**并补 `padding/gap: 0`、遮罩的
  `[data-slot="dialog-overlay"]`/`[data-slot="sheet-overlay"]` 规则、`.forebrain-modal > button`/
  `.forebrain-drawer > button` 藏库自带的 X（批次 10）、面板把手的 `.panel-resizer` 与
  拖动中的光标/禁用选区规则（批次 11）、以及批次 12 的死规则清理。
- `frontend/src/locales/index.ts` —— 只加两条：`common.resizePanel`（把手的中文/英文 aria-label）。
- `ai-elements-vue/packages/shadcn-vue/components/ui/{select/SelectItem.vue, switch/Switch.vue}`
  —— 只允许本计划明确写出的两处小改。
- 受影响的单测与 e2e（清单见上）。**这 5 处是明确允许改的测试**：`e2e/radius.spec.ts:32`
  （批次 10 的门禁选择器）、`ForebrainModal.test.ts:14/20/22/29/31`、`ForebrainDrawer.test.ts:52`、
  8 处 `HTMLSelectElement` 断言、22 处 `selectOption` → `pickOption`。
  其余测试的断言与测试名一律不许改。

**Out of scope**（看着相关也不许动）

- `frontend/src/components/ForebrainPromptTextarea.vue` —— 它**已经是**库的 input-group 形状：
  `:763` 的 `<textarea data-slot="input-group-control">` 是 `InputGroup` 约定的"由调用方提供控制元素"，
  `cn()` 也来自库。composer 有 IME/自适应高度/引用菜单一整套行为，本计划不碰。
  若将来库提供 `InputGroupTextarea` 再另立计划。
- `frontend/src/components/providers/ParamsJsonField.vue`、`frontend/src/components/editor/FileEditor.vue`
  （及其 Markdown/CodeMirror 相关件）—— CodeMirror 编辑器，不是表单控件。
  **但 `frontend/src/components/editor/FrontmatterFields.vue` 在范围内**：它是纯 `<input>`，
  归批次 12（见那张表）。
- `frontend/src/components/common/RowAction.vue`、`RowActions.vue`、`RowActionSlot.vue`、
  `AppTooltip.vue`、`SortButton.vue` —— 图标按钮/提示器，用 `forebrain-icon-btn`，
  不属于"表单按钮"，已有自己的共享实现。
- `frontend/src/components/common/DataTable.vue` —— 已经是库的 table，不改层级、尺寸、插槽。
  注意：它的列宽拖拽走的是 tanstack 的 `header.getResizeHandler()`（`mousedown` + 内联样式），
  **不是** `pointerdown`/`setPointerCapture`——全仓目前没有可照抄的指针实现，批次 11 的
  `usePanelWidth` 是从零写的，别去 DataTable 里找样板。
- **不要**把 `ChatWorkbench` / `SubagentDrawer` 改成 `Sheet`：owner 2026-10-21 明确裁定
  「保持内嵌，只拖宽度」——它们是对话页三列布局里的两列，改成浮层会推翻已 1:1 还原的设计稿。
- **不要**把 24 个调用点改成直接写 `<Dialog>`：外壳组件（`ForebrainModal`/`ForebrainDrawer`）
  是"库原语 + 项目栈规则 + 设计类"的组合层，不是第二套实现。它的存在让 24 个调用点、5 份 e2e、
  11 份单测都不用动。若 owner 要求调用点直接写 `<Dialog>`，那是另一份计划，先停下来问。
- 任何 Go 代码、`pkg/gateway/dist`、`plans/`（除本文件的状态勾选）。
- **不要**新建本地包装组件去"统一"库组件（如 `AppInput.vue`）—— 那正是 owner 反对的第二套实现。
  设计类 `forebrain-field` / `forebrain-btn` 已经是唯一的设计来源。

## 批次顺序

每批都自包含、可单独交付/回滚。**推荐顺序：10 → 0 → 1…8 → 11 → 12**：
批次 10（换弹窗/抽屉外壳）风险最高、影响面最广，趁表单迁移还没动过这些文件时先做，
回归一旦出现就只能是它；批次 11（拖宽度）排在 8 之后，因为它的 e2e 与批次 6 用的是同一批 spec；
批次 12 永远是最后的总门禁。批次 0 仍是所有表单批次的前置。

**每批的范围按目录划**：把该目录里**所有**带 `forebrain-btn` 的 `.vue` 一起迁（不只是有原生控件的那些），
否则同一个弹窗会一半是 `Button`、一半是原生 `<button>`，正是这份计划要消灭的两种实现。

**编号说明**：本计划**没有批次 9**（早期切分留下的空号，不要去找）。共 12 批：
0–8、10、11、12。

| 批次 | 范围 | 文件（含只改按钮的） | 控件 | 主要 e2e |
|---|---|---|---|---|
| 10 | 遮罩弹窗 + 遮罩抽屉换库的 Dialog / Sheet | 2（外壳）+ `main.css` | 0 | `tenant-shell`、`projects-modals`、`settings-tabs`、`providers-keys`、`mcp-pages`、`subagent-config`、`radius`、`visual` |
| 0 | 地基与公共控件 | 13（6 个组件 + 2 个新建 + 2 处库小改 + `main.css`/`e2e/support.ts` 等） | 9 | `providers-keys`、`settings-tabs`、`project-space-tables`、`mcp-pages`、`radius`、`visual` |
| 1 | 设置页 | 9 | 10 | `settings-tabs`、`visual` |
| 2 | MCP | 3（+ 批次 0 的 `McpKeyValueRows`） | 18 | `mcp-pages` |
| 3 | 子代理 | 2 | 14 | `subagent-config` |
| 4 | 项目空间 | 12 | 22 | `project-space-tables`、`project-space`、`projects-modals`、`rules-permissions`、`skills-lifecycle`、`radius` |
| 5 | 定时任务 | 2 | 14 | `cron-builder`、`cron-fire`、`cron-retention` |
| 6 | 对话与会话弹窗 | 12 | 9 | `rewind`、`new-chat-project`、`heartbeat`、`workbench`、`approval-families`、`message-cards`、`web-fetch-approval`、`exit-plan-approval` |
| 7 | 技能工坊与工作台 | 14 | 11 | `skill-workshop`、`workbench`、`workbench-history`、`file-editor` |
| 8 | 列表页与外壳 | 10（8 个页面 + `LoginView` + `App.vue`） | 20 | `agent-tables`、`flat-pages`、`skills-lifecycle`、`layout`、`shell-chrome`、`brand`、`auth` |
| 11 | 两个并排面板的左缘拖拽把手 | 4（`ChatWorkbench`、`SubagentDrawer`、新 composable + 其 test） | 0 | 引用 `chat-workbench` 的全部 8 份 |
| 12 | 收尾：其余页面的按钮 + 4 个零散文件 + 死规则清理 + 全量门禁 | 7 | 7 | **全量**（不带 `FOREBRAIN_E2E_SPEC`） |

批次 12 的 7 个控件都在那 4 个零散文件里（`memory/MemoryFilesPanel` 2、`skills/InstallDialogs` 2、
`skills/SkillsTable` 2、`editor/FrontmatterFields` 1），它们不属于上面任何一块页面；
剩下的零散按钮（`components/AgentRosterPanel.vue` 等）以总门禁的输出为准，
**门禁说什么就改什么，不要自己加油**。

## Git workflow

本仓库的既定约定（本目录其余计划一致）：

- **不建分支、不建 worktree、不 commit、不 push**。改动直接落在当前工作区（`dev`），
  由 owner 审阅后自己提交。
- 工作区里已有的未提交改动是 owner 的在途工作：**不许** `git checkout` / `git restore` /
  `git stash` / `git reset`，也不许为了"干净基线"去提交它们。
- `frontend/e2e/**` 与 `frontend/src/**` 之外的产物（`pkg/gateway/dist`）是 `pnpm run build`
  的副产物，属 owner，别清理、别提交。

## 迁移写法（动手前必读）

**字段（输入框 / 文本域 / 下拉触发器）一律带上 `class="forebrain-field <尺寸>"`。**
尺寸保持今天的值（`h-9` 或 `h-[34px]`），文本域加 `min-h-0`（库的 `min-h-16` 会顶着设计）。
**下拉触发器例外**：今天写 `h-[34px]` 的，迁移后写 `!h-[34px]`（库的 `data-[size=default]:h-9`
是 0,2,0，会赢；今天写 `h-9` 的正等于库默认值，不用 `!`）。这条不是可选项——
全仓有 5 个 34px 的筛选下拉（`SettingsSharedSkillsTab.vue:13`、`ProjectSkills.vue:26`、
`SkillsView.vue:16`、`ToolsView.vue:16,20`）。

```vue
<!-- 文本 -->
<Input v-model="form.baseUrl" class="forebrain-field h-9 w-full font-mono" data-testid="provider-base-url" />
<!-- 数字：用 Input，不要 NumberField（它会画出 +/− 按钮）；
     库的 Input 不实现 v-model 的 .number 修饰符，换算必须写在调用处 -->
<Input
  :model-value="String(inCount)"
  type="number" min="1"
  class="forebrain-field h-9 w-24"
  data-testid="schedule-in-count"
  @update:model-value="(v) => (inCount = Number(v) || 1)"
/>
<!-- 多行 -->
<Textarea v-model="draft.instructions" rows="3" class="forebrain-field min-h-0 w-full" data-testid="project-instructions" />
<!-- 下拉 -->
<!-- 注意三处：!h-[34px]（库的 data-[size=default]:h-9 是 0,2,0，普通 h-[34px] 压不住）、
     原样保留的 style="width: 110px"（今天这五个筛选下拉靠内联宽度，见规则 3）、
     data-testid 在触发器上 -->
<Select :model-value="origin" @update:model-value="(v) => (origin = String(v ?? ''))">
  <SelectTrigger
    class="forebrain-field !h-[34px]"
    style="width: 110px"
    :aria-label="t('skills.colSource')"
    data-testid="skills-source-filter"
  >
    <SelectValue />
  </SelectTrigger>
  <SelectContent>
    <SelectItem v-for="o in originOptions" :key="o.value" :value="o.value">{{ o.label }}</SelectItem>
  </SelectContent>
</Select>
```

规则：

1. **`data-testid` 放在可点的那个元素上**：下拉放 `SelectTrigger`（e2e 点它、`getByRole('option')` 选它），
   输入框放 `Input`，复选框放 `Checkbox` 根节点。测试 id 一个都不许改名或删除。
2. **空串取值走哨兵**：`frontend/src/lib/selectValue.ts` 提供
   `EMPTY_CHOICE`、`toChoice(value)`、`fromChoice(choice)`；下拉的 `v-model` 绑 `toChoice` 的计算属性，
   写回时 `fromChoice`。**不要**在每个文件里各造一个哨兵字符串。
3. **保留一切文案与属性**：`placeholder`、`aria-label`、`disabled`、`readonly`、`autocomplete`、
   `min`/`max`、`rows`、`id`（`Label for=` 靠它）、**`style`**（今天 5 个筛选下拉的
   `style="width: 110px"` 就是它们的宽度来源；`SelectTrigger` 自带 `w-fit`，只有内联宽度
   能压住它）。只换元素，不改语义。
4. **`type` 按今天的真实角色写，不要一律改成 `button`**：
   - 裸 `<button>`（没有 `type`）→ `<Button type="button" …>`（HTML 默认是 submit，显式更安全）。
   - 今天就是 `type="submit"` 的 → 保持 `type="submit"`。全仓只有两处：
     `ProjectOverview.vue:60`（在 `<form @submit.prevent="save">` 里，`ProjectOverview.vue:6`）
     与 `LoginView.vue:32` 的登录按钮。**把 `ProjectOverview` 的保存按钮改成 `type="button"`
     会让它保存不了**——今天它靠表单提交调 `save()`，没有 `@click`。
5. **`Button` 的 class 是三段式，"原样搬"只占第三段**：

   ```vue
   <Button
     type="button"
     class="forebrain-btn forebrain-btn-ghost text-xs"
   >
   ```

   因为库的默认 variant 会塞进 `bg-primary text-primary-foreground h-9 px-4 py-2 gap-2`，
   而 `.forebrain-btn` 不写 `background`/`height`（见"地基"第 2 条），所以：

   - **普通按钮**（只有 `forebrain-btn`，没有 `-primary`/`-ghost`）：必须显式抵消，
     写成 `class="forebrain-btn h-auto bg-transparent text-[var(--forebrain-text)] hover:bg-transparent …"`
     （`h-auto` 让 `min-height: 2rem` 重新决定高度 = 今天的 32px；
     `bg-transparent`/`text-[…]`/`hover:bg-transparent` 让 tailwind-merge 丢掉库的配色）。
   - **`-primary` / `-ghost` 按钮**：设计类自己写了 background/color 且排在后面，
     不用抵消配色，但**仍然要 `h-auto`**。

   不要换成 `variant="ghost"`——库的 ghost 只是"悬停变 accent"，底色仍不是设计稿的
   `--forebrain-button-alt-bg`。
6. **被 `<label>` 包着的控件，先看结构再换**：
   - 今天的包法是 `<label class="…"><span>标题</span><控件/></label>`，全仓 **73 处**
     （复验：`python3 - <<'PY'`，见下面这段）。`<input>`/`<textarea>` 换成库组件后仍是原生
     元素，这种包法**可以原样保留**；但 `Select`/`Checkbox`/`Switch`/`RadioGroup` **不能留在
     `<label>` 里**（它们渲染的是 `<button>`/`role=…`，点标签会被当成再点一次控件，
     `AppearanceTab.vue:29-39` 就是这个坑）。这些地方改成 `<Label for="…">标题</Label>`
     + 同级控件，并给控件一个稳定 `id`。
   - 判断标准只有一条：**控件渲染出来的是不是 `input`/`textarea`**。是 → 可以留；
     不是 → 拆成 `Label` + 兄弟元素。列表里的 73 处大多是前者，真正要拆的是含下拉/
     复选框/开关的那些。

```bash
cd frontend && python3 - <<'PY'
import glob, re
total = 0
for p in glob.glob('src/**/*.vue', recursive=True):
    t = open(p).read()
    for m in re.finditer(r'<label\b[^>]*>(.*?)</label>', t, re.S):
        if re.search(r'<(input|select|textarea)\b', m.group(1)):
            total += 1
print('label-wrapped controls:', total)   # 今天 73
PY
```

7. **复选框**：`:model-value="checked"` + `@update:model-value="(v) => (checked = v === true)"`；
   全选框沿用同一个写法。原来 `@change="(e) => e.target.checked"` 的取值改为事件参数。
8. **数字字段不能用 `v-model.number`**：`.number` 是原生输入的元素修饰符，库组件不认
   （它只会把 `modelModifiers` 当普通 prop 收下、不做换算），绑出来会变成字符串。
   一律写成 `:model-value="String(n)"` + `@update:model-value="(v) => (n = Number(v) || <兜底>)"`，
   兜底值照今天的行为（`min="1"` 的次数字段兜 `1`，`min="0"` 的兜 `0`）。
   今天全仓这样的字段有 5 个（复验：`grep -rn "v-model.number" src --include=*.vue`）：
   `settings/CronSettingsTab.vue:16`、`chat/SessionHeartbeat.vue:41`、
   `cron/ScheduleBuilder.vue:22,35`、`views/CronView.vue:151`。
   **一个有意的行为收紧**：清空数字框今天得到 `''`，改后立即回到兜底值。这是想要的
   （后端只收数字）——但如果某个字段的语义真的是"空 = 不限"，停下来问，不要就地放行 `''`。
9. **单选**：`RadioField.vue` 内部换成 `RadioGroup` + `RadioGroupItem`，对外 props
   （`label/name/options/modelValue/hint/vertical`）与 `.forebrain-radio*` 类名保持不变；
   调用方（7 处）**不用改**。
10. **开关**：`SwitchComponent.vue` 内部换成 `Switch` + `#thumb` 插槽。今天的三段几何
    （`SwitchComponent.vue:42-79` 的 `<style scoped>`）是 32×18 药丸 + 14px 滑块 +
    2px 内缩、打开态 `left: 16px`；库给的是 `h-[1.15rem] w-8` + `size-4` 滑块 +
    `data-[state=checked]:translate-x-[calc(100%-2px)]`（14px 滑块下只走 12px）。
    所以**三件事一起做，少一件就错位**：
    - 根：`class="forebrain-switch"` + `:class="{ 'forebrain-switch--on': modelValue }"`，
      并让 scoped 里的 `.forebrain-switch` 继续决定 32×18 底色（库的
      `data-[state=checked]:bg-primary` 由 `--on` 那条自己写，别两套并存）；
    - 滑块：`thumb-class="forebrain-switch-thumb size-3.5 !translate-x-0 data-[state=checked]:!translate-x-[14px]"`
      ——`!` 是必需的，`tailwind-merge` 只会丢掉 `size-4`，那条 `translate-x-[calc(100%-2px)]`
      带 `data-[state=checked]:` 修饰符，不合并；
    - scoped 样式里的 `.forebrain-switch-thumb` 要加 `:deep(...)`（滑块由库渲染，
      拿不到本组件的 scope id），并把原来的 `left: 16px` 那条删掉（位移改由
      `translate` 表达，两条并存会叠成 28px、滑块跑出药丸）。
    `SwitchRow.test.ts` 断言的是 `.forebrain-switch`、`forebrain-switch--on`、
    `aria-checked`、`disabled`——按上面写，这四条都还成立，**该测试不用改**。
11. 迁移后**删掉该文件里已经无人使用的本地辅助**（例如把 `HTMLSelectElement` 的事件处理函数
   改成值处理函数后剩下的旧代码），但不要顺手重构别的。

## Batch 0 — 地基与公共控件

**先建梯队，再动页面。**

1. 新建 `frontend/src/lib/selectValue.ts`：

```ts
/**
 * 一个下拉框的"空串选项"。reka-ui 的 SelectItem 拒绝空字符串 value
 * （空串留给了"清空选择/显示 placeholder"），而项目里有真实选项的取值就是
 * 空串（Chat Completions 的 api_path、空档位、空投递目标）。这里用一个不会
 * 与任何真实取值相同的标记代替，读写两端各自转换一次。
 */
export const EMPTY_CHOICE = '@empty'

export function toChoice(value: string | null | undefined): string {
  const text = String(value ?? '')
  return text === '' ? EMPTY_CHOICE : text
}

export function fromChoice(choice: unknown): string {
  const text = String(choice ?? '')
  return text === EMPTY_CHOICE ? '' : text
}
```

2. 新建 `frontend/src/test-utils/formControls.ts`（jsdom 里驱动 reka 下拉的唯一方式）。
   `ProvidersView.test.ts:203-230` 里有一份**同思路但签名不同**的私有实现
   （`openSelect(testid)` / `pick(testid, label)` / `picked(testid)`，都是按 `data-testid`
   取元素），所以这**不是搬，是改写**：新模块收 `HTMLElement`（组件测试里拿到的 wrapper 元素），
   再把 `ProvidersView.test.ts` 的四个调用点改成传元素。逻辑照抄下面这段（含
   `pointerup` 提交这一点，是那份私有实现已验证过的）：

```ts
import { flushPromises } from '@vue/test-utils'

/** Open a shadcn select: the browser's own selectOption is gone with the native element. */
export async function openSelect(el: HTMLElement): Promise<void> {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  await flushPromises()
}

export function optionTexts(): string[] {
  return [...document.querySelectorAll<HTMLElement>('[role="option"]')].map((o) => (o.textContent ?? '').trim())
}

/** Pick by visible label; the item commits on pointerup, not on click. */
export async function pickOption(el: HTMLElement, label: string): Promise<void> {
  await openSelect(el)
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
    (o) => (o.textContent ?? '').trim() === label,
  )
  if (!option) throw new Error(`no option "${label}"; saw ${JSON.stringify(optionTexts())}`)
  option.dispatchEvent(new Event('pointerup'))
  await flushPromises()
}

/** What a select shows: the trigger's text (SelectValue renders the chosen item). */
export function selectedText(el: HTMLElement): string {
  return (el.textContent ?? '').trim()
}
```

3. `frontend/e2e/support.ts` 加一个真浏览器用的辅助（`providers-keys.spec.ts` 里的私有副本改用它）：

```ts
/** 一个 shadcn 下拉：点触发器、点选项。没有原生 <select> 给 selectOption 驱动。 */
export async function pickOption(page: Page, testid: string, label: string): Promise<void> {
  await page.click(`[data-testid="${testid}"]`)
  await page.getByRole('option', { name: label, exact: true }).click()
}
```

4. 库的两处小改：
   - `ai-elements-vue/packages/shadcn-vue/components/ui/select/SelectItem.vue`：
     把 `outline-hidden` 改成 `outline-none`。`outline-hidden` 是 Tailwind v4 的类，在 3.4 里
     不产出任何 CSS（实测：编译产物里 `outline-hidden` 出现 0 次），所以选项行今天没有焦点
     样式，只有浏览器的默认描边。改成 `outline-none` 后焦点由组件自带的 `focus:bg-accent`
     表达（reka 会把焦点放到选项行上）——这正是设计稿的样子，不要另外加 ring。
   - `ai-elements-vue/packages/shadcn-vue/components/ui/switch/Switch.vue`：加
     `thumbClass?: HTMLAttributes['class']`，并把它合并进 `SwitchThumb` 的 `cn(...)` 末尾
     （与 `props.class` 在 `SwitchRoot` 上的用法完全一致）。
     **只加这一个 prop，不要改尺寸默认值**（尺寸由调用方用 `thumb-class` 里的
     `!translate-x-[14px]` 之类压过去，写法见"迁移写法"第 10 条）。
5. `frontend/src/assets/main.css`：在 `.forebrain-field` 里加 `box-shadow: none;`、
   在 `@layer utilities` 的 `.forebrain-btn` 里加 `box-shadow: none;`，并各写一行注释：
   库组件自带 `shadow-xs`，设计稿的字段/按钮没有投影。注意**这两行取消的是静态投影**：
   库的焦点态是 `focus-visible:ring-[3px]`（0,2,0 的 `box-shadow` 组合），压不掉，
   那是本计划接受的焦点外观（见"地基"第 3 条），不要把 `box-shadow: none` 写进
   `:focus-visible` 想连焦点环一起去掉。
6. 公共控件内部重写（对外 API 与 `data-testid` 不变）：
   - `RadioField.vue` → `RadioGroup`/`RadioGroupItem`（`.forebrain-radio*` 类名保留）；
   - `SwitchComponent.vue` → 按"迁移写法"第 10 条的三件事一起做；
   - `TablePager.vue:5-13` 的页大小下拉 → `Select`（哨兵不适用，尺寸是数字，用 `String`）。
     这是本批唯一有额外结构改动的一处：
     - `data-testid="pager-size"` 移到 `SelectTrigger`，`data-testid="table-pager"`/`pager-total` 不动；
     - `main.css:1353-1365` 的 `.forebrain-pager-size select {…}` 是**元素选择器**，
       换成 `SelectTrigger`（渲染的是 `<button>`）之后永远不命中 → 改成
       `.forebrain-pager-size .forebrain-field`（26px 高度那里要 `!h-[26px]`，因为库的
       `data-[size=default]:h-9` 是 0,2,0）；
     - 模板里 `TablePager.vue:16` 那个手写的 `<ChevronDown>` 要删掉（`SelectTrigger` 自己会画
       一个），`main.css:1366` 的 `.forebrain-pager-size > svg` 那条随之改成
       `.forebrain-pager-size [data-slot="select-trigger"] svg`，否则会出现**两个箭头**；
   - `ConfirmDialog.vue:5` 的输入 → `Input class="forebrain-field"`；
   - `McpKeyValueRows.vue` 两处 → `Input class="forebrain-field"`；
   - `ModelChipsInput.vue:18-28` → `TagsInput` + `TagsInputItem` + `TagsInputItemDelete` + `TagsInputInput`
     （reka 2.11 的 props 已核实：`delimiter` 默认就是 `","`、`addOnBlur`/`addOnPaste` 要显式打开、
     `duplicate` 默认关；Enter 成 chip 是内建的，还带 `isComposing` 守卫，输入法打字不会误提交）。
     必须保住今天这四样：
     1. `data-testid="model-chips"` 仍在外层 `space-y-2` 容器上（e2e 用
        `[data-testid="model-chips"] [data-chip="…"]` 这个后代选择器，`ModelChipsInput.test.ts:54`
        还按 `.test.ts` 里的 `button:not([aria-label])` 找 chip 的删除键）；
     2. 每个 chip 的 `data-chip="<model>"`，以及**删除键**（今天的 `×` 按钮，`aria-label` 是
        `providers.removeModel`，换成 `TagsInputItemDelete` 并把它带 `aria-label`；
        少了它 chip 就删不掉）；
     3. `data-testid="model-chips-input"` 在 `TagsInputInput` 上（e2e 对它 `fill` + `blur`）；
     4. 下方 `suggestions` 建议按钮行仍在 `TagsInput` **之外**（它是根容器的第二个子元素）。
     `TagsInput` 根默认是 `rounded-md border-input bg-background px-3 py-1.5`，今天的框是
     `rounded-lg border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2 py-1.5`
     → 把这些设计类作为 `class` 传给 `TagsInput`（`cn()` 会合并）。目标形状：

```vue
<TagsInput
  class="rounded-lg border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2 py-1.5"
  :model-value="models"
  :add-on-blur="true"
  @update:model-value="emit('update:modelValue', $event as string[])"
>
  <TagsInputItem v-for="model in models" :key="model" :value="model" :data-chip="model">
    {{ model }}
    <TagsInputItemDelete :aria-label="t('providers.removeModel', { model })" />
  </TagsInputItem>
  <TagsInputInput data-testid="model-chips-input" :placeholder="…" />
</TagsInput>
```

7. `ComponentGallery.vue` 的 3 处原生控件换成库组件，并加一节「表单控件」
   展示 Input / Textarea / Select / Checkbox / Switch / RadioGroup / Button 的设计形状
   （这是给人眼复核的一页，`vite` 开发服务器打开 `/component-gallery.html`）。

**Verify**
- `cd frontend && corepack pnpm exec vue-tsc --noEmit` → exit 0
- `cd frontend && corepack pnpm vitest run src/lib/selectValue.test.ts src/components/common src/components/providers src/views/ProvidersView.test.ts` → 全绿
  （`selectValue.test.ts` 是本批要新建的：空串→哨兵→空串往返、`null`/`undefined`、真实值不被改写）
- `FOREBRAIN_E2E_SPEC="e2e/providers-keys.spec.ts e2e/settings-tabs.spec.ts e2e/project-space-tables.spec.ts e2e/mcp-pages.spec.ts e2e/radius.spec.ts e2e/visual.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`
  （后两份不是凑数：本批改的 `TablePager` 只有 `project-space-tables.spec.ts:331` 在用，
  `McpKeyValueRows` 只有 `mcp-pages` 会打开那个抽屉——只跑前四份的话，这两处回归要到
  批次 4/2 才炸，那时已经查不出是谁改坏的）

## Batch 1 — 设置页

本批 9 个文件（8 个带 `forebrain-btn`，`AppearanceTab` 只有控件没有按钮）：
`settings/AppearanceTab.vue`（1 select + 2 个 `RadioField` 调用点不动）、
`settings/HooksTab.vue`（5 个控件）、`settings/CronSettingsTab.vue`（1）、
`settings/LspTab.vue`（1）、`settings/SettingsSharedSkillsTab.vue`（2）、
`settings/SettingsApprovalTab.vue`（只有按钮与 `SwitchRow`）、`settings/ConfigTab.vue`、
`settings/SettingsSubagentsTab.vue`、`settings/MemorySwitchesTab.vue`（后三个只有按钮）。
`SwitchRow` 的调用点一律不动。

要点：`AppearanceTab.vue:29-39` 现在把 `<select>` 塞在 `<label class="forebrain-form-field">` 里；
改成 `<Label for="appearance-language">` + 同级 `Select`（交互元素不能嵌在 `<label>` 里，否则点开下拉会二次触发）。

**Verify**
- `corepack pnpm vitest run src/components/settings src/views/SettingsView.test.ts` → 全绿（`AppearanceTab.test.ts:54` 等
  读 `HTMLSelectElement.value` 的断言改用 `selectedText()` 或组件内部状态）
- `FOREBRAIN_E2E_SPEC="e2e/settings-tabs.spec.ts e2e/visual.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`

## Batch 2 — MCP

文件：`mcp/McpServerDrawer.vue`（14 个控件、3 个 `<select>`：`:58` `form.transport`、
`:182` `form.oauth.mode`、`:269` `form.defaultToolsApprovalMode`）、
`mcp/McpServersTable.vue`（2）、`mcp/McpKeyValueRows.vue`（已在批次 0）。

要点：`transport`/`oauth.mode` 是**枚举下拉**，没有空串；
`mcp-default-approval`（`:275`）的 `<option value="">`＝"跟随全局"，**是空串** → 用 `EMPTY_CHOICE`。
三个 `data-testid`（`mcp-transport`、`mcp-oauth-mode`、`mcp-default-approval`）必须留在 `SelectTrigger` 上。
抽屉里的错误块与禁用态逻辑一行都不改。

**Verify**
- `corepack pnpm vitest run src/components/mcp` → 全绿（该目录有 4 个测试文件）
- `FOREBRAIN_E2E_SPEC="e2e/mcp-pages.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`
  （`mcp-pages.spec.ts:385` 的 `selectOption('streamable_http')` 改用 `pickOption`）

## Batch 3 — 子代理

文件：`subagents/SubagentConfigDrawer.vue`（12 个控件：3 个 `<select>` + 9 个输入）、
`views/SubagentsView.vue`（2，只改控件与按钮，列表逻辑不动）。

**本批的第三个下拉不是字符串**，这是计划里点名的 STOP 情形，处理方式如下：
- `subagent-model`（`:27`）与 `subagent-effort`（`:47`，含一个 `value=""` 的"跟随模型"空档）
  → 字符串，空档走 `EMPTY_CHOICE`；
- `subagent-mode`（`:61`）绑定的是**布尔** `form.continuable`（`<option :value="true">`）。
  reka 的 `SelectItem` 只认字符串 value，所以这一处写成 `:value="'on'"` / `:value="'off'"`
  （或 `toChoice` 同样的思路），**读回时在同一个 `@update:model-value` 里换回布尔**：
  `form.continuable = String(v) !== 'off'`。不要改用 `Switch`——设计稿这一格是下拉，
  换控件属于改设计，STOP。
- 工具那三档是 **radio**（`input[name="subagent-tools"]`，e2e `subagent-config.spec.ts:352`
  用 `.check()` 驱动），不在本批的 12 个控件里，`RadioField` 批次 0 已经重写完，**不要**改成 `Select`。

要点：`SubagentConfigDrawer.test.ts:100,106` 读 `HTMLSelectElement.value` 的两处改成
`selectedText()`；空档位那条（`:106` 断言 `''`）改成断言"触发器上没有选中项文案"
**或**直接断言组件内的 form 字段，别用"触发器文本 === ''"——reka 在 `modelValue: ''` 时
显示的是 placeholder，二者都无法区分"空档"与"没选"。

**Verify**
- `corepack pnpm vitest run src/components/subagents src/views/SubagentsView.test.ts` → 全绿
  （新增用例见 Test plan：`subagent-mode` 的布尔往返）
- `FOREBRAIN_E2E_SPEC="e2e/subagent-config.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`
  （`:254-255` 两处 `selectOption` 改 `pickOption`）

## Batch 4 — 项目空间

文件（`frontend/src/components/project/` 下**全部** 12 个文件，22 个控件）：
`ProjectOverview.vue`（6，含 `<form>`）、`ProjectPerm.vue`（6）、`ProjectRules.vue`（3）、
`ProjectSkills.vue`（2）、`ProjectSessions.vue`（1）、`ProjectMemory.vue`（1）、
`ProjectLsp.vue`（1）、`ProjectChannels.vue`（1）、`ChannelEditDialog.vue`（1）、
以及三个只有按钮的：`ProjectMcp.vue`、`ProjectDeleteDialog.vue`、`ProjectMcpTab` 若存在同类文件
一并处理（复验：`grep -rn 'forebrain-btn' src/components/project`）。

要点：
- **`ProjectOverview.vue` 的保存按钮今天是 `type="submit"`（`:60`），在
  `<form @submit.prevent="save">`（`:6`）里靠提交触发 `save()`，没有 `@click`。**
  迁移后必须保持 `type="submit"`（`<Button type="submit" …>`），其余按钮 `type="button"`。
  把它改成 `type="button"` 会让保存彻底失效。
- `ProjectRules.vue:73,79` 的两个 `<select>` 是**运行时填充**的目录/文件选择（选项来自磁盘），
  用 `v-for` 渲染 `SelectItem`，选项文案原样；测试
  `ProjectRules.test.ts:109-110` 读 `.options` 的断言改成打开下拉后比 `optionTexts()`。
- `ProjectPerm.vue:85` 的 `v-tip:top` 在 `<SelectTrigger>` 上要保住（指令作用于根元素）。

**Verify**
- `corepack pnpm vitest run src/components/project src/views/ProjectsView.test.ts src/views/ProjectDetailView.test.ts` → 全绿
- `FOREBRAIN_E2E_SPEC="e2e/project-space-tables.spec.ts e2e/project-space.spec.ts e2e/projects-modals.spec.ts e2e/rules-permissions.spec.ts e2e/skills-lifecycle.spec.ts" scripts/acceptance/web_e2e.sh`
  → `web e2e: PASS`（`project-space-tables.spec.ts:331`、`rules-permissions.spec.ts:50,90`、
  `skills-lifecycle.spec.ts:152` 的 `selectOption` 改 `pickOption`；最后一份查的是
  `project-skills-source-filter`，正是本批的 `ProjectSkills.vue`）
- `FOREBRAIN_E2E_SPEC="e2e/radius.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`
  （项目标签是 `radius.spec` 覆盖最多的一块）

## Batch 5 — 定时任务

文件：`cron/ScheduleBuilder.vue`（9：3 个 `<select>` + 5 个 `<input>`
（`datetime-local`、两个 `number`、`time`、cron 文本）+ 1 个复选框）、
`views/CronView.vue`（5）。

要点：`schedule-in-unit` / `schedule-every-unit` / `schedule-mode` 的 `data-testid` 留在触发器上；
`cron-deliver` 的"不投递"就是空串 → `EMPTY_CHOICE`。
两个数字框（`schedule-in-count`、`schedule-every-count`）不能用 `v-model.number`（规则 8）。
`schedule-once-at`（`datetime-local`）与 `schedule-time`（`time`）**换成库 `Input` 时必须保留
`type="datetime-local"` / `type="time"`**（库的 `Input` 会把 `type` 透传到原生元素上，但默认值是
`text`——漏写 `type` 会让这两个字段变成自由文本，而它们各自没有任何测试兜底）。
单测 `ScheduleBuilder.test.ts:17`、`CronView.test.ts:180` 的 `HTMLSelectElement.value` 改 `selectedText()`。

**Verify**
- `corepack pnpm vitest run src/components/cron src/views/CronView.test.ts` → 全绿
  （新增用例见 Test plan：两个时间/日期字段的 `type` 保持、数字框回落兜底值）
- `FOREBRAIN_E2E_SPEC="e2e/cron-builder.spec.ts e2e/cron-fire.spec.ts e2e/cron-retention.spec.ts" scripts/acceptance/web_e2e.sh`
  → `web e2e: PASS`（`cron-builder` 11 处、`cron-fire` 2 处 `selectOption` 全部改 `pickOption`）

## Batch 6 — 对话与会话弹窗

文件：`chat/FolderDialog.vue`、`chat/NewFolderSheet.vue`、`chat/ProjectPopover.vue`、
`chat/RewindDialog.vue`（复选框）、`chat/SessionHeartbeat.vue`（number + textarea）、
`chat/AskTabs.vue`、`chat/ApprovalCard.vue`、`src/components/ChatDrawer.vue`，
以及四个**只有按钮**的同目录文件：`chat/ApprovalGateCard.vue`、`chat/AutoContinueBanner.vue`、
`chat/LspRecommendationCard.vue`、`chat/McpNotice.vue`（复验：
`grep -rln 'forebrain-btn' src/components/chat src/components/ChatDrawer.vue`）。
按批次规则（同目录一起迁），这四个不能漏。

要点：
- `RewindDialog.vue:28` 的复选框现在是"选中=丢弃"的反向绑定，改写时保持取反语义
  （`:model-value="!keep.includes(f.path)"`），别把语义弄反。
- `SessionHeartbeat.vue:41` 的 `v-model.number` **不能直接照搬**（库的 `Input` 不实现
  `.number` 修饰符，见"迁移写法"第 8 条）：改成 `:model-value="String(minutes)"` +
  `@update:model-value="(v) => (minutes = Number(v) || 1)"`。
- **`ApprovalCard.vue` / `ApprovalGateCard.vue` 里的 `ConfirmationAction` 是库组件、不是原生
  `<button>`**，它带着 `forebrain-btn` 设计类是对的，本批**只换这两个文件里真正的原生
  `<button>`**，不要动 `ConfirmationAction` 的用法（门禁对它豁免，见"清单"）。
- 四个文件里有**元素选择器的 scoped 样式**，换完控件后会静默失效，逐条改成类选择器：
  `ProjectPopover.vue:134 .srch input`、`FolderDialog.vue:454 .fb-search input`、
  `RewindDialog.vue:250 .rw-file input`（复选框换成 `Checkbox` 后尤其会掉）。

**Verify**
- `corepack pnpm vitest run src/components/chat` → 全绿（该目录有 26 个测试文件）
- `FOREBRAIN_E2E_SPEC="e2e/rewind.spec.ts e2e/new-chat-project.spec.ts e2e/heartbeat.spec.ts e2e/workbench.spec.ts e2e/approval-families.spec.ts e2e/message-cards.spec.ts e2e/web-fetch-approval.spec.ts e2e/exit-plan-approval.spec.ts" scripts/acceptance/web_e2e.sh`
  → `web e2e: PASS`（后四份是 `ApprovalCard`/`AskTabs` 的真正驱动者：`approval-families`、
  `message-cards`、`web-fetch-approval`、`exit-plan-approval` 都在查这两张卡上的按钮）

## Batch 7 — 技能工坊与工作台

文件：`components/workshop/` 下 5 个带控件的文件（`CaseEditDialog.vue` 2、`EvalCaseDialog.vue` 1、
`EvalsTab.vue` 1、`PublishDialog.vue` 1、`TriggerTab.vue` 3）——本批把该目录**全部 11 个**
带 `forebrain-btn` 的文件一起迁；`components/workbench/` 下 `FileEditDialog.vue` 1、
`WorkingDirTab.vue` 2，连同该目录其余带 `forebrain-btn` 的文件（复验：
`grep -rln 'forebrain-btn' src/components/workshop src/components/workbench`）。
`src/App.vue` 归批次 8，不要在这里动。

要点：
- `TriggerTab.vue:81` 的"应不应该触发"是**布尔**下拉（`:value="true"`/`:value="false"`），
  和批次 3 的 `subagent-mode` 同一类问题：`SelectItem` 只认字符串，写成 `'on'`/`'off'`，
  在 `@update:model-value` 里换回布尔。
- `FileEditDialog` 的输入框旁边有 CodeMirror，别把编辑器的容器类改掉。
- `PublishDialog`/`EvalsTab` 里的 `TagsInput`/多值字段按批次 0 的 `ModelChipsInput` 写法走。

**Verify**
- `corepack pnpm vitest run src/components/workshop src/components/workbench` → 全绿
- `FOREBRAIN_E2E_SPEC="e2e/skill-workshop.spec.ts e2e/workbench.spec.ts e2e/workbench-history.spec.ts e2e/file-editor.spec.ts" scripts/acceptance/web_e2e.sh`
  → `web e2e: PASS`

## Batch 8 — 列表页与外壳

文件：`src/App.vue`（1 个控件 + 设计按钮）、
`views/` 下 8 个页面（`ToolsView`、`SkillsView`、`RulesView`、`ProvidersView`、`ProjectsView`、
`PermissionsView`、`MemoriesView`、`ChatView`）——本批 20 个控件；
`views/LoginView.vue`（1 个输入，在 `<form>` 里；它没有设计按钮）。
**`views/SubagentsView.vue` 归批次 3，`views/CronView.vue` 归批次 5**，本批不要碰它们
（批次表里旧的"26 控件"把这两份算重了）。
`views/ProvidersView.vue` 的模型服务弹窗已在上一轮改动里迁完、`views/CronView.vue` 的弹窗
在批次 5 迁完，**这里只碰 ProvidersView 的搜索框和两页的按钮**，不要再动弹窗。

要点：列表工具栏的搜索框今天写的是 `class="forebrain-field h-[34px]"`；
换成 `<Input class="forebrain-field h-[34px]" :placeholder=… data-testid=… />`，`type="search"` 保留。
`ProvidersView.vue:8` 的搜索框是最后一个原生控件；**它周围的表格、弹窗一行都不要动**。
`LoginView.vue:32` 的 `<form>` 里，提交按钮保持 `type="submit"`（见"迁移写法"第 4 条），
其余按钮 `type="button"`。

**Verify**
- `corepack pnpm vitest run src/views` → 全绿（该目录有 12 个测试文件；`App.vue` 没有单测）
- `FOREBRAIN_E2E_SPEC="e2e/agent-tables.spec.ts e2e/flat-pages.spec.ts e2e/skills-lifecycle.spec.ts e2e/layout.spec.ts e2e/shell-chrome.spec.ts e2e/brand.spec.ts e2e/auth.spec.ts" scripts/acceptance/web_e2e.sh`
  → `web e2e: PASS`（`agent-tables.spec.ts:250` 的 `selectOption` 改 `pickOption`；
  `shell-chrome`/`brand` 覆盖 `App.vue` 的工具栏与品牌色——本批动了它的按钮，
  只跑 `layout` 抓不到）

## Batch 10 — 遮罩弹窗与遮罩抽屉换成库的 Dialog / Sheet

> 按推荐顺序**最先执行**：它改的是每一个弹窗的外壳，风险最高；此时表单还没迁，出了回归只能是它。

**目标**：`ForebrainModal` 的内部换成库的 `Dialog`，`ForebrainDrawer` 的内部换成库的 `Sheet`；
对外 API、插槽、`modalStack` 的栈规则、焦点回还全部不变，24 + 4 个调用点一行都不改。
**但"类名一个都不许改名"和"测试一条都不许改"这两句只对 `.forebrain-modal` / `.forebrain-drawer`
成立**：遮罩类 `.forebrain-modal-mask` 在库结构里无处安放（Current state 第 2 条），
所以本批**明确允许**改三处测试与一个门禁选择器，清单见第 5 步。除此之外一行测试都不许动。

1. 把 `.forebrain-modal*` 的样式从 `frontend/src/components/common/ForebrainModal.vue` 的
   `<style scoped>` **原值搬**到 `frontend/src/assets/main.css`（放在 `.forebrain-drawer*` 那一段
   ——即 `:1557` 起——的紧前面）。值一个都不许改：`z-index: 50`、`width: 560/440/860px`、
   `max-height: 660/780px`、`border-radius: 12px`、头/体/脚的内边距。
   搬的时候补两条本来不需要、现在必须写的：
   - `.forebrain-modal { padding: 0; gap: 0; }` —— 抵消库的 `p-6 gap-4`；
   - 遮罩色改成全局规则（`.forebrain-modal-mask` 这个类不要了）：
     ```css
     /* 库的 DialogContent 内部自己渲染遮罩，调用方拿不到它的 class，
        所以设计稿的 32% 遮罩只能落在 data-slot 上。 */
     [data-slot='dialog-overlay'],
     [data-slot='sheet-overlay'] {
       background: color-mix(in srgb, var(--forebrain-text) 32%, transparent);
     }
     ```
   `.forebrain-drawer*` 已经是全局的（`main.css:1557-1630`），不用搬。
2. 重写 `ForebrainModal.vue` 的内部（模板骨架；类名、槽位、`role` 都由库补齐）：

```vue
<Dialog :open="open" @update:open="(v) => { if (!v) requestClose() }">
  <DialogContent
    :class="['forebrain-modal', `forebrain-modal--${size}`]"
    @escape-key-down="(e: Event) => e.preventDefault()"
    @interact-outside="(e: Event) => { e.preventDefault(); requestClose() }"
    @close-auto-focus="(e: Event) => e.preventDefault()"
  >
    <div class="forebrain-modal-head">
      <DialogTitle class="forebrain-modal-title"><slot name="title">{{ title }}</slot></DialogTitle>
      <button type="button" class="forebrain-modal-close" :aria-label="t('common.close')" @click="requestClose">…</button>
    </div>
    <div class="forebrain-modal-body"><slot /></div>
    <div v-if="$slots.footer" class="forebrain-modal-foot"><slot name="footer" /></div>
  </DialogContent>
</Dialog>
```

   要点（每一条都是一个已实测的坑，照写）：
   - **Escape 只有一条路**：本组件保留 `document` 上的 capture `keydown` 监听（`activate()` 里那句，
     它是 `ForebrainModal.test.ts` 第三、五条例的契约），并用
     `@escape-key-down="(e) => e.preventDefault()"` **关掉 reka 自己的 Escape 处置**
     （reka 的 `DismissableLayer` 在 `onKeyStroke('Escape')` 里先 emit `escapeKeyDown`、
     未被 `preventDefault` 才 `dismiss` → `onOpenChange(false)`）。不写这一句，
     一次 Escape 会 emit 两次 `close`（第三条例的 `toHaveLength(1)` 直接红）。
   - **遮罩点击只有一条路**：`@interact-outside` 里先 `preventDefault()`（挡住 reka 的
     `dismiss`）再走 `requestClose()`，于是仍然经过 `isTopModal(id)`。
     不要额外监听原生 click——库的遮罩是 reka 的 `DialogOverlay`，它报的是
     `pointerdown` 外部事件，不是 click。
   - **库自带的上栏 X 用全局规则藏掉**（`:deep()` 不管用：内容在 `DialogPortal` 里，
     没有本组件的 scope id 祖先）。加进 `main.css` 的 modal 段：
     ```css
     /* 关闭入口属于头栏（设计稿如此）；库在内容右上角自带一个。
        它永远是 .forebrain-modal 的最后一个直接子按钮，头栏那个按钮在 .forebrain-modal-head 里。 */
     .forebrain-modal > button { display: none; }
     ```
   - `@close-auto-focus` 阻止 reka 自己还焦点：焦点回还仍由本组件的 `restoreFocus` 负责。
   - `DialogContent` 的默认 `fixed top-[50%] left-[50%] translate-…` 负责居中；
     `.forebrain-modal` 不要写 `position`。
3. 重写 `ForebrainDrawer.vue` 的内部：`<Sheet :open="open" @update:open="…">` +
   `<SheetContent side="right" class="forebrain-drawer">`，**同样**加
   `@escape-key-down` / `@interact-outside` / `@close-auto-focus` 三个 preventDefault，
   头/体/脚与 `readonly` 徽标原样；库的 X 用 `.forebrain-drawer > button { display: none; }` 藏掉；
   `modalStack` 与焦点回还逻辑原样。`SheetContent` 也有 `gap-4` →
   `.forebrain-drawer { gap: 0; }`（今天三段之间没有 gap）。
4. `modalStack.ts` 不改逻辑，只在注释里补一句"库的 Dialog/Sheet 之外仍由这个栈决定谁关"。
5. **允许改的测试**（其余一律不动的契约仍然有效：断言与意图不变，只换取 DOM 的方式）：

   | 文件:行 | 今天 | 改成 |
   |---|---|---|
   | `ForebrainModal.test.ts:14` | `global: { stubs: { teleport: true } }` | 去掉这个桩（reka 的 portal 不认 `<Teleport>` 桩），断言改查 `document.body` |
   | `ForebrainModal.test.ts:20,22` | `wrapper.find('.forebrain-modal')` | `document.querySelector('.forebrain-modal')` |
   | `ForebrainModal.test.ts:29,31` | `find('.forebrain-modal-mask').trigger('click')` | 在 `document` 上派发 `pointerdown`（`bubbles: true`）到面板之外的区域，等 `flushPromises()` |
   | `ForebrainDrawer.test.ts:52` | `find('.forebrain-drawer-mask').trigger('click')` | 同上 |
   | `e2e/radius.spec.ts:32` | `el.closest('.forebrain-modal-mask')` | `el.closest('.forebrain-modal, [data-slot="dialog-overlay"]')`（遮罩不再是祖先，面板自己要在排除集里） |

   jsdom 里 reka 需要 `ResizeObserver` 与 `Element.prototype.scrollIntoView` 的桩
   （照 `ProvidersView.test.ts:232-244` 那两段；**注意那份 beforeAll 里没有 pointer-capture 桩**，
   需要时自己补 `hasPointerCapture/setPointerCapture/releasePointerCapture`）。
   加 reka 的 Portal 之后 `mount()` 拿不到内容，是**预期行为**，不是 bug。

**Verify**
- `corepack pnpm vitest run src/components/common` → 全绿
  （`ForebrainModal.test.ts` 五条与 `ForebrainDrawer.test.ts` 全过；只有第 5 步那 4 行改了写法）
- `corepack pnpm exec vue-tsc --noEmit` → exit 0
- 下面这一整串（5 个 `.forebrain-modal` 引用者 + 2 个抽屉驱动者 + 两道门禁）：
  `FOREBRAIN_E2E_SPEC="e2e/tenant-shell.spec.ts e2e/projects-modals.spec.ts e2e/settings-tabs.spec.ts e2e/providers-keys.spec.ts e2e/mcp-pages.spec.ts e2e/subagent-config.spec.ts e2e/radius.spec.ts e2e/visual.spec.ts" scripts/acceptance/web_e2e.sh`
  → `web e2e: PASS`（`tenant-shell` 是最先被漏掉的那份；`radius.spec` 现在靠第 5 步改过的
  选择器排除弹窗内部）
- 人眼：`shot(page, 'modal-after-sheet')` / `shot(page, 'drawer-after-sheet')` 与迁移前截图并排看
  ——遮罩暗度（32%）、12px 圆角、头/脚分隔线、关闭按钮位置、**面板内没有多出 24px 内边距**。
  基线截图：动手前先在同一台机器上跑一次
  `FOREBRAIN_E2E_SPEC="e2e/providers-keys.spec.ts" scripts/acceptance/web_e2e.sh`，把
  `E2E_SHOTS` 里的两张存下来（这一步是本批唯一的"迁移前"证据，别跳）。
- `grep -rn "forebrain-modal-mask" frontend/src frontend/e2e` → 只剩
  `main.css` 里的那条历史注释（如果保留）与第 5 步改过的行，其余为 0

## Batch 11 — 工作台与子代理面板：左缘把手拖宽度

> 按推荐顺序在**批次 8 之后、收尾（批次 12）之前**执行（它的 e2e 与批次 6 用同一批 spec）。

**目标**：`ChatWorkbench`（默认 340px）与 `SubagentDrawer`（默认 440px）都能拖左缘把手改宽度；
宽度**本会话有效**（owner 2026-10-21 裁决：与表格列宽同一套规则，刷新回设计值），
布局、标签、内容一律不改。

1. 新建 `frontend/src/composables/usePanelWidth.ts`（两个面板共用一份，别各写一套）：

```ts
import { ref } from 'vue'

/** 宽度按面板 id 存在模块作用域里：关掉再打开还在，刷新页面回设计值（owner 2026-10-21）。 */
const widths = new Map<string, number>()

export function usePanelWidth(id: string, initial: number, bounds: { min: number; max: () => number }) {
  const width = ref(widths.get(id) ?? initial)
  let startX = 0
  let startWidth = 0

  function apply(next: number): void {
    width.value = Math.round(Math.min(Math.max(next, bounds.min), bounds.max()))
    widths.set(id, width.value)
  }
  // pointerdown：setPointerCapture、记 startX/startWidth、body 上挂 cursor 与禁用选区
  // pointermove：apply(startWidth + (startX - event.clientX))   ← 把手在左缘，往左拖变宽
  // pointerup / pointercancel：清理监听与 body 样式
  // dblclick：apply(initial)
  // keydown：ArrowLeft +16、ArrowRight -16、Home 回 initial
  return { width, onPointerDown, onPointerMove, onPointerUp, onKeydown, reset }
}
```

2. `ChatWorkbench.vue`：`<aside>`（`ChatWorkbench.vue:2`）加
   `:style="overlay ? undefined : { width: `${width}px` }"`
   ——**组件里没有 `docked` 这个变量，它叫 `overlay`**（`:class="{ 'chat-workbench--overlay': overlay }"`）；
   浮层态不要内联宽度，否则压掉 `main.css` 里 `@media (max-width:1023px)` 的
   `width: min(340px, calc(100vw - 64px))`。
   面板 `position: relative`（scoped 样式里的 `.chat-workbench` 那段加一行），第一个子元素是把手：

```vue
<div
  class="panel-resizer"
  role="separator"
  aria-orientation="vertical"
  :aria-label="t('common.resizePanel')"
  tabindex="0"
  data-testid="chat-workbench-resizer"
  @pointerdown="onPointerDown"
  @pointermove="onPointerMove"
  @pointerup="onPointerUp"
  @pointercancel="onPointerUp"
  @dblclick="reset"
  @keydown="onKeydown"
/>
```

   同法加到 `SubagentDrawer.vue`（`data-testid="subagent-drawer-resizer"`，默认 440，`--overlay` 时不启用）。
   把手的样式放 `main.css`：`position:absolute; inset-block:0; left:-3px; width:6px; cursor:col-resize;
   z-index:1`，hover/拖动中加深；`--overlay` 与 `@media (max-width: 1023px)` 下 `display:none`。
3. 边界：工作台 `min 280 / max min(720, 0.55 * window.innerWidth)`；子代理
   `min 320 / max min(860, 0.6 * window.innerWidth)`。
   用 `window.innerWidth` 算，不要在 JS 里读 `55vw`（jsdom 里 `innerWidth` 有默认值 1024，
   严格来说这两条断言会落在上限上——单测里用 `Object.defineProperty(window, 'innerWidth', …)`
   固定它）。拖动中给 `document.body` 临时加一个 class（`.panel-resizing`，
   `cursor: col-resize; user-select: none`），结束后移除；**不要在把手或 body 上写行内样式**。
4. `locales/index.ts` 加 `common.resizePanel`：中文「拖动调整宽度」/ EN `Resize panel`。
5. 新 composable 的单元测试：`src/composables/` 的既有约定是"每个 composable 自己的 `.test.ts`"，
   所以 `usePanelWidth` 的用例写进 **`src/composables/usePanelWidth.test.ts`**（新建），
   两个面板组件里只加"把手存在 + 宽度绑定生效"两条（Test plan）。

**Verify**
- `corepack pnpm vitest run src/composables/usePanelWidth.test.ts src/components/chat` → 全绿
  （新增用例见 Test plan）
- `FOREBRAIN_E2E_SPEC="e2e/workbench.spec.ts e2e/subagent-drawer.spec.ts e2e/workbench-history.spec.ts e2e/subagent-config.spec.ts e2e/layout.spec.ts e2e/shell-chrome.spec.ts e2e/workspace-tree.spec.ts e2e/skill-workshop.spec.ts" scripts/acceptance/web_e2e.sh`
  → `web e2e: PASS`（`chat-workbench` 被这 8 份 spec 引用，只跑前两份会漏掉窄屏与布局断言的回归）
- 人眼：`shot(page, 'workbench-resized')`、`shot(page, 'subagent-drawer-resized')`

## Batch 12 — 收尾清理与总门禁

**本批还要负责 4 个"没有页面归它"的文件**（它们不在任何页面批次里，但 132 个控件里有 7 个
落在其中，门禁要求 0）：

| 文件 | 控件 | 说明 |
|---|---|---|
| `src/components/memory/MemoryFilesPanel.vue` | 2 个 `<input>`（`:16`、`:25`，搜索/新文件名） | 顺带把该文件的按钮一起迁 |
| `src/components/skills/InstallDialogs.vue` | 2 个 `<input>`（`:21`、`:32`） | 同上 |
| `src/components/skills/SkillsTable.vue` | 2 个 `<input type="checkbox">`（`:14` 全选、`:23` 行选） | 复选框用 `Checkbox`；`data-testid="skills-select-all"` 与 `aria-label` 必须留在 `Checkbox` 根上（e2e `skills-lifecycle` 用它） |
| `src/components/editor/FrontmatterFields.vue` | 1 个 `<input>`（`:5`，front matter 的字段行） | **不是 CodeMirror**（那是 `FileEditor.vue`）。它渲染的是纯 `<input>`，必须迁；`aria-label`/`disabled`/`@input` 语义保留 |

另有 `components/AgentRosterPanel.vue`、`scripts`/`views/ChatView.vue` 等只剩按钮的零散文件：
以门禁输出为准，**门禁说什么就改什么，不要自己加油**。

1. 跑总门禁（下面是本计划的定义式，必须全部成立）：

```bash
cd frontend
# 1) 不再有原生表单控件（composer 除外）
grep -rn '<input\|<select\|<textarea' src --include=*.vue | grep -v ForebrainPromptTextarea | wc -l   # 期望 0
# 2) 不再有原生 <button> 扛着设计按钮类（Button 与库里的 ConfirmationAction 豁免）
python3 - <<'PY'
import glob, re
OK = {'Button', 'ConfirmationAction'}
offenders = []
for path in glob.glob('src/**/*.vue', recursive=True):
    text = open(path).read()
    for m in re.finditer(r'forebrain-btn', text):
        start = text.rfind('<', 0, m.start())
        tag = re.match(r'<([A-Za-z][\w.-]*)', text[start:]) if start >= 0 else None
        if not tag or tag.group(1) not in OK:
            offenders.append((path, text.count('\n', 0, m.start()) + 1))
print('offenders:', len(offenders))
PY
# 期望 offenders: 0（照上面这条豁免规则，今天从 377 起算）
# 3) e2e 里不再有原生下拉驱动（用带点的正则，别用裸词：注释里出现过这个词）
grep -rn "\.selectOption(" e2e | wc -l   # 期望 0
```

2. 清理死代码：`main.css` 里被库组件取代且**已无任何引用**的规则。按批次 0 的约定，
   `RadioField` / `SwitchComponent` 的**外层类名保留**（`.forebrain-radios`、`.forebrain-radio`、
   `.forebrain-switch*` 的布局与配色仍然有用），真正会死掉的是这些类下面**指向原生 input 的子规则**
   （`.forebrain-radio input {…}`、`.forebrain-radio input::before {…}`、`:checked` / `:focus-visible` 那几条）。
   逐个核对下面这几处**元素选择器**（它们不会报错，只会静默不命中）：
   - `main.css:983 textarea.forebrain-field` → **保留**（库的 `Textarea` 根仍是 `<textarea>`）；
   - `main.css:1625 .forebrain-drawer select.forebrain-field:disabled` → **改**成
     `.forebrain-drawer .forebrain-field[data-disabled]`（`SelectTrigger` 渲染 `<button>`，
     reka 用 `data-disabled` 而不是 `disabled`）；
   - `main.css:1353 .forebrain-pager-size select` 与 `:1366 > svg` → 批次 0 已经改过，核对即可；
   - 组件内 scoped 的 `input` 后代选择器：`.sa-chk input`（`SubagentConfigDrawer.vue:687`）、
     `.srch input`（`ProjectPopover.vue:134`）、`.fb-search input`（`FolderDialog.vue:454`）、
     `.rw-file input`（`RewindDialog.vue:250`）→ 各自改成类选择器或确认库组件仍渲染原生元素。
   `forebrain-field` 与 `forebrain-btn*` **保留**——它们是迁移后仍在使用的设计类。
   删之前先 `grep -rn "<选择器>" src` 证明零引用，一条一条删、每删一条跑一次 `pnpm test`。
3. `frontend/src/gallery/ComponentGallery.vue` 与真实控件逐项对照一遍（人眼复核页）。
4. 全量单测与构建：

```bash
cd frontend && corepack pnpm test && corepack pnpm exec vue-tsc --noEmit && corepack pnpm run build
```

**Verify**
- 上面三条门禁输出 `0` / `offenders: 0` / `0`
- `corepack pnpm test` → 136 个文件全过（用例数会因改写而增减，必须全绿）
- **全量 e2e**（本批是发布门禁，不是按批抽查）：`scripts/acceptance/web_e2e.sh` 不带
  `FOREBRAIN_E2E_SPEC` 跑完，末行 `web e2e: PASS`（约 9 分钟）。
  前面每一批只跑了自己那几份；批次 12 是唯一一次全量，漏掉的 spec 在这里兜住。

## Test plan

**表单批次（0–8，外加 10/11/12）**

- 结构：沿用各组件的既有测试文件，测试名与断言意图不变，只换驱动方式。
  共用驱动来自 `src/test-utils/formControls.ts`（新建）：`openSelect` / `optionTexts` / `pickOption` / `selectedText`。
- 必改的 8 处 `HTMLSelectElement` 断言（清单见 Current state → 测试面）改成
  "打开下拉比 `optionTexts()`" 或 "读触发器文案 `selectedText()`"。
- 新增用例：
  1. `src/lib/selectValue.test.ts`（新建，批次 0）：`''`→`EMPTY_CHOICE`→`''` 往返；
     `null`/`undefined` 当空串；真实取值（含 `'@empty'` 之外的任何字符串）不被改写。
     这是全计划最高风险的一件小事，必须有测试。
  2. `RadioField.test.ts`：三选项里点第二个 → `update:modelValue` 一次、值正确；键盘 ArrowDown 可用。
  3. `TablePager.test.ts`：页大小下拉有 10/20/50 三项、选 50 → emit `update:page-size` 50。
  4. `SubagentConfigDrawer.test.ts`：档位下拉选"空档"→ 模型值是 `''`（哨兵映射正确）；
     **`subagent-mode` 布尔往返**：选"一次性"→ `form.continuable === false`（不是 `'off'`）。
  5. `ScheduleBuilder.test.ts`：次数输入填 `3` → 组件状态是**数字** 3（不是 `'3'`）；
     `schedule-once-at` / `schedule-time` 仍是 `datetime-local` / `time` 类型（读元素 `type`）。
  6. `McpServerDrawer.test.ts`：`mcp-default-approval` 选"跟随全局"→ 值是 `''`（空串哨兵）。
     这一条今天全仓没有任何测试看着，是本批最容易静默改坏的一处。
- e2e：不新增 spec，把 22 处真调用 `selectOption` 换成 `pickOption(page, testid, label)`
  （`e2e/support.ts` 里的新辅助），断言不变。

**批次 10（弹窗外壳）**

- `ForebrainModal.test.ts` 五条 / `ForebrainDrawer.test.ts` 的既有用例**断言一条都不改**
  （关着时无 DOM、遮罩点击只关最上面、Escape 只关最上面、焦点回还、监听器注册/注销），
  只有取 DOM 的方式按批次 10 第 5 步那张表改 4 行。
- **不要再加"库自带的关闭按钮被藏掉"的断言**：库那个 X 是 `<DialogClose>` + `<span class="sr-only">Close</span>`，
  **没有 `aria-label`**，所以 `querySelectorAll('.forebrain-modal button[aria-label]').length === 1`
  在 X 完全没藏掉时也成立——那是条空断言。这条契约由批次 10 的人眼截图
  （`modal-after-sheet`：头栏只有一个 X）负责验证。
- e2e：在 `e2e/radius.spec.ts` 里改那 1 行排除选择器（第 5 步），不新增用例。

**批次 11（拖宽度）**

- 单测 `src/composables/usePanelWidth.test.ts`（新建，因为 `src/composables/` 的约定是一个
  composable 一份测试）：
  1. 默认值 = 传入的 `initial`；`Math.round` 与 `min/max` 夹取；
  2. `pointerdown` → `pointermove`（clientX 往左 80px）→ `pointerup`：宽度 +80；越界被夹到上限；
  3. `reset()` 回到设计值；同一 id 的第二次 `usePanelWidth` 读到上次的值（模块级 `Map`）。
- 两个面板组件各加两条：把手存在（`[data-testid="…-resizer"]`）、`overlay` 为真时不写内联宽度
  （`style.width` 为空）。
- e2e（各自 spec 加一条）：读 `boundingBox().width` → 拖把手 80px → 宽度 +80（±2）→
  `dblclick` 回到设计值 → `reload` 后在设计值（本会话有效，不跨刷新）。

## Done criteria

全部成立才算完成：

- [ ] `grep -rn '<input\|<select\|<textarea' frontend/src --include=*.vue | grep -v ForebrainPromptTextarea | wc -l` → `0`
- [ ] 批次 12 的 `offenders:` 门禁（带 `Button`/`ConfirmationAction` 豁免）→ `0`
- [ ] `grep -rn "\.selectOption(" frontend/e2e | wc -l` → `0`（用带点的正则；**裸词 grep 不会到 0**，
      因为 `providers-keys.spec.ts:23` 与 `support.ts` 的注释里就写着这个词）
- [ ] `cd frontend && corepack pnpm exec vue-tsc --noEmit` → exit 0
- [ ] `cd frontend && corepack pnpm test` → 全绿（新文件 `selectValue.test.ts`、
      `usePanelWidth.test.ts` 也在里面）
- [ ] `cd frontend && corepack pnpm run build` → exit 0
- [ ] `e2e/radius.spec.ts` + `e2e/visual.spec.ts` 通过（几何与视觉系统未漂移）
- [ ] 每个批次自己那份 spec 通过；批次 12 的全量 e2e 通过
- [ ] `grep -rn "v-model.number" frontend/src --include=*.vue | wc -l` → `0`（库组件不实现该修饰符；
      今天有 5 处，见"迁移写法"第 8 条）
- [ ] `grep -rn "components/ui/dialog" frontend/src/components/common/ForebrainModal.vue` 有输出，
      `grep -rn "components/ui/sheet" frontend/src/components/common/ForebrainDrawer.vue` 有输出
      （外壳真的建在库组件上，不是换了个注释；**不要**引 `reka-ui`——`frontend/package.json`
      没有这个依赖，`frontend/node_modules` 里也没有它，引了会直接解析失败）
- [ ] `grep -rn "<Teleport" frontend/src/components/common/{ForebrainModal,ForebrainDrawer}.vue` → 无输出
      （挂载点交给库的 portal）
- [ ] `grep -rnl "forebrain-modal" frontend/e2e | wc -l` → `5`（`tenant-shell`、`projects-modals`、
      `radius`、`settings-tabs`、`providers-keys`）；
      `grep -rnl "forebrain-drawer" frontend/e2e | wc -l` → `3`（类名没被改名）
- [ ] `grep -rn "forebrain-modal-mask" frontend/src frontend/e2e` → 只剩批次 10 第 5 步允许的
      那 1 行 e2e（已改成排除集）与组件历史注释
- [ ] `grep -rn "resizer" frontend/src/components/chat/{ChatWorkbench,SubagentDrawer}.vue` 各有一条（把手在）
- [ ] `git status` 只出现本计划 In-scope 的文件（外加既有的工作区基线与 `pkg/gateway/dist`；
      基线里 `pkg/gateway/dist/assets/*` 有一批 **deleted** 条目，那是既有状态，别去恢复别去提交）
- [ ] `plans/README.md` 的本行状态更新（或按 owner 要求不动）

## STOP conditions

停下并报告，不要临场发挥：

- **构建产物的 CSS 顺序变了**：`forebrain-field` 的偏移不再大于 `.h-9`（批次 0 的复验脚本）。
  那意味着"设计类压过普通工具类"的前提失效，全部批次都会产生视觉漂移。
- **某个库组件在 v3.4 下明显缺样式**（下拉弹出层高度/位置错乱、布局崩坏）。
  允许修的只有本计划点名的那两处（`SelectItem` 的 `outline-hidden`、`Switch` 的 `thumbClass`）；
  其他组件要改，先停下来报告，附上 `grep -n "(\-\-" <文件>` 的证据。
- **`!` 重要前缀也压不住某个库默认类**（例如某个 `SelectTrigger` 写了 `!h-[34px]` 仍是 36px）：
  说明那个类的特异性或位置与实测不同，属于 STOP——不要继续加类，把
  `grep -n "该类的选择器" pkg/gateway/dist/assets/index-*.css` 的结果贴回来。
- **某个下拉的取值不是字符串**（数字 `0`、布尔 `true/false`、对象）——`selectValue.ts` 只处理字符串，
  先把该字段的 wire 形状查清楚再改；不要就地塞一个 `String()` 了事，那是把类型错误藏起来。
  （已知的两处见批次 3 的 `subagent-mode` 与批次 7 的 `TriggerTab`，它们的写法已给定。）
- **迁移后出现的可见差异不是"复选框"那一项**（例如按钮高度、字段边框、下拉宽度变了）——
  先照"地基"第 1、2 条核对 `!h-[…]` / `h-auto` 有没有写；写了还差，才是 STOP。
- **某个 spec 的失败原因看起来是行为变化而不是选择器变化**（例如提交了表单、值没写回、
  键盘操作失效）—— 停下来，把失败原文贴回来。
- 需要改 Go 代码、`pkg/gateway/dist`、`ForebrainPromptTextarea.vue`、或新建包装组件才能继续。
- **批次 10 特有的停止线**：
  - `ForebrainModal.test.ts` 的"Escape 只关最上面"或"遮罩点击只关最上面"必须靠改**断言**才能过
    （改取 DOM 的方式是允许的，见第 5 步那张表）—— 那是栈规则没保住，属于 STOP。
    如果一次 Escape 让 `close` emit 了两次，先确认 `@escape-key-down` 的 `preventDefault` 写了。
  - 弹窗的遮罩暗度/圆角/头脚分隔线/内边距肉眼可见地变了，而 `main.css` 的搬移与
    `padding: 0; gap: 0` 都照做了 —— 说明库的 `fixed/translate`、`bg-black/80` 或 `p-6`
    有规则压过了设计类，先查级联顺序再报告。
  - 为了藏掉库自带的关闭按钮，需要改库的 `DialogContent.vue` / `SheetContent.vue` ——
    那是 STOP：应该用 `main.css` 里 `.forebrain-modal > button` 那条全局规则解决
    （`:deep()` 不行，内容在 portal 里，没有本组件的 scope id 祖先）。
- **批次 11 特有的停止线**：把手需要动对话页的列布局（`ChatView` 的 flex 结构、互斥逻辑）才能实现 ——
  本批必须是纯增量（面板自身 `position: relative` + 一个绝对定位的把手）。

## Maintenance notes

- 一句话规则：**表单控件只有一种实现——`@repo/shadcn-vue/components/ui/*` 的组件；
  外观只有一个来源——`main.css` 的 `forebrain-field` / `forebrain-btn*` 设计类。**
  新写表单时照这个组合写，不要再手写 `<input>`。
- **"设计类赢"只对普通工具类成立**：库里的 variant/size 类（`data-[size=default]:h-9`、
  `Button` 的 `bg-primary h-9`）是 0,2,0 或"设计类没写的属性"，必须用 `!` 前缀或
  `h-auto`/`bg-transparent` 这类显式抵消（本文件"地基"三条就是判据）。新引入任何库组件前，
  先看它的默认 class 串里有没有 `data-[…]:`、`variant`、`size`。
- 设计类靠"在样式表里排在工具类之后"取胜（本文件有复验脚本）。将来若升级到 Tailwind v4，
  它的原生 `@layer` 会改变这条优先关系 —— 那时要么把这些类搬进 `@theme`/`@layer components`，
  要么把设计值改成 CSS 变量令牌。**升级 Tailwind 之前先读这一段。**
- 库的 class 是按 v4 写的、项目编译 v3.4：新引入一个库组件前，先
  `grep -n "(\-\-\|outline-hidden\|\*:" <组件目录>` 看有没有 v4-only 语法，
  有就按 `[var(--x)]` 改写。这不是洁癖：`SelectContent` 的 `max-h` 就是这样让下拉在真机上
  完全点不到的。
- 复选框与设计稿的差异（库 16px 方框 vs 设计稿 15px 原生）是 owner 2026-10-21 的裁决，
  不要在"还原设计稿"的名义下改回去；焦点由库的 3px ring 提供（不再是浏览器 outline）同为裁决结果。
- **弹窗外壳的三个不变量**（批次 10 之后）：
  ① 任何"关掉"的路径（Escape、遮罩点击、库的 `update:open`）都必须经过 `modalStack` 的
  `isTopModal` —— 否则两层弹窗会一起关；实现方式是 `@escape-key-down` 与 `@interact-outside`
  两个 `preventDefault`，把 reka 自己的处置挡在门外；
  ② 焦点回还由外壳自己做（`@close-auto-focus` 阻止 reka 的）；
  ③ `.forebrain-modal-mask` 这个类**已经不存在**——遮罩是库的 `[data-slot="dialog-overlay"]`，
  几何门禁靠 `.forebrain-modal` + `[data-slot="dialog-overlay"]` 两个选择器排除弹窗内部。
- **抽屉宽度的所有者**（批次 11 之后）：`usePanelWidth.ts` 的模块级 `Map`。它按 owner 2026-10-21
  的裁决是**本会话有效**；改成 `localStorage` 需要新的裁决，不要顺手加。
  两个面板的默认值 340 / 440 与设计稿一致，`reset()` 必须回到它们。
- 后续可做（本计划刻意不做）：把 `forebrain-field` / `forebrain-btn*` 的数值搬成
  shadcn 的 design token（`--input`、`--ring`、`--primary`），让库的 `variant` 直接等于设计稿。
  那是一次全局配色/尺寸重构，要单独取证与截图对照，不要顺手做。
