# 计划 019：文件编辑器——Markdown 用 Milkdown Crepe（MIT，所见即所得，代码块可编辑、选语言、带高亮），源码和代码文件用 CodeMirror 6（MIT），代码高亮一律 monokai-light

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新本目录 README 中本计划的状态行。**不要提交代码。**
> **1:1 还原设计稿** `app-preview.html`（第 18 版）。设计稿里装的就是真实的 Crepe 与 CodeMirror，打包配方见本目录 `vendor/README.md` 与 `vendor/build/`，实现照这份配方接进前端。
> "本轮新增"里依次点：
> - "编辑规则文件 · 所见即所得""编辑规则文件 · 源码"
> - "编辑 FOREBRAIN.md（代码块、表格、任务）"
> - "工坊 · 编辑 SKILL.md（属性 + 富文本）""工坊 · 编辑脚本（代码）"
> - "设置 · 配置文件（代码）"
>
> 原型代码：`mdeHtml()`、页面底部的 `<script type="module">`（`mountEditors()`、`zh` 汉化配置）、`.mde` 样式与 `.mde .milkdown` 的主题变量映射。
>
> **漂移检查（先运行）**：
> - `ls docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/vendor/build/{package.json,langs.js,highlight.js,entry.js}` 都在；
> - `grep -n "codeThemeLight" ai-elements-vue/packages/elements/src/code-block/theme.ts` 有结果。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：LOW–MED（新增依赖约 1.8MB，按需加载）
- **依赖**：015（弹窗）。018、017、020 用本计划的组件
- **类别**：功能 / 依赖
- **基线**：提交 `005c1a8` + 主树未提交改动，2026-10-11

## 为什么要做

owner 2026-10-11：
- "web端文件编辑必须使用支持markdown的富文本编辑器"；
- "我需要的富文本编辑器必须是可以编辑代码的，有代码高亮的"；
- "必须使用成熟稳定MIT/apache 2等开源可商用的富文本组件库"；
- "web端不论主题色是什么，代码高亮统一使用monokai-light主题"。

现在规则文件、记忆文件、技能文件、配置文件都是纯文本框（`plans/README.md` 偏差清单第 10 条）。

## 选型与许可证（评审方 2026-10-11 核实）

- `@milkdown/crepe` 7.22.3：MIT，10 月 10 日仍在发版，依赖 Vue 3.5，与本仓库前端一致。
  - 自带顶部工具栏（`Crepe.Feature.TopBar`，默认关闭，需打开）、斜杠菜单、块拖动、表格、任务列表、链接提示；
  - 代码块用 CodeMirror 6，可选语言、带复制。
- `codemirror` 6.0.2、`@codemirror/*`：MIT。
- 依赖全量核对：209 个 MIT；dompurify 为 MPL-2.0 或 Apache-2.0 双许可，按 Apache-2.0 使用；entities BSD-2-Clause；picocolors ISC；source-map-js BSD-3-Clause。全部可商用。
- 设计稿打包时遇到过一个坑：从 jsDelivr 按子路径加载 `@milkdown/kit`，会让 ProseMirror 加载两份，报 "Adding different instances of a keyed plugin"。前端用 Vite 打包、依赖去重，不会遇到，但要有一条测试确认只加载了一份（见步骤 5）。

## 现状（已核实）

- 编辑文件的地方全是 `<textarea>`：
  - `components/rules/RulesFileEditor.vue`（018 删掉）；
  - `components/memory/MemoryFilesPanel.vue`；
  - `components/workshop/WorkshopSkillPanel.vue`；
  - `components/settings/ConfigTab.vue`。
- 代码高亮今天只在消息里用 Shiki：`@repo/elements/code-block`，浅色主题 `monokai-light.ts` 的颜色取自 chroma `monokailight`，文件头注释有对照表。CodeMirror 需要一份按 lezer 高亮标签映射的同色样式。设计稿的映射在 `vendor/build/highlight.js`。

## 命令

| 用途 | 命令 | 预期 |
|---|---|---|
| 依赖 | `cd frontend && pnpm add @milkdown/crepe@7.22.3 codemirror@6.0.2 @codemirror/language@6.13.1 @codemirror/lang-markdown@6.5.2 @codemirror/lang-go@6.0.1 @codemirror/lang-json@6.0.2 @codemirror/lang-yaml@6.1.3 @codemirror/lang-javascript@6.2.5 @codemirror/lang-python@6.2.1 @codemirror/lang-sql@6.10.0 @codemirror/legacy-modes@6.5.5` | 成功，版本精确锁定 |
| 许可证 | `cd frontend && npx --yes license-checker-rseidelsohn --production --summary` | 只出现上面列出的许可证 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功；编辑器在单独的 chunk 里 |

## 范围

**改**：
- `frontend/package.json`、锁文件；
- 新建 `frontend/src/components/editor/{FileEditor.vue,FrontmatterFields.vue}`、`frontend/src/lib/editor/{languages.ts,monokaiLight.ts,crepeLocale.ts}`；
- `ai-elements-vue/packages/elements/src/code-block/`：导出调色板常量，供两套高亮共用；
- `MemoryFilesPanel.vue`、`WorkshopSkillPanel.vue`、`ConfigTab.vue` 里的文本框换成编辑器。规则文件弹窗在 018，用本组件；
- 测试；`pkg/gateway/dist`。

**不改**：
- 消息里的 Shiki 高亮：015 只去掉暗色变体；
- 后端。

## 步骤

### 步骤 1：共用调色板

1. 在 `@repo/elements/code-block` 新增并导出 `monokaiLightPalette`，内容是 chroma `monokailight` 的颜色：
   - keyword `#00a8c8`、namespace `#f92672`、name `#111111`、green `#75af00`、tag `#f92672`；
   - literal `#ae81ff`、string `#d88200`、escape `#8045ff`、operator `#f92672`、punctuation `#111111`；
   - comment `#75715e`、error `#960050`；
   - 背景 `#fafafa`、正文 `#272822`。

   `monokai-light.ts` 里的 Shiki 主题改为引用这些常量，结果不变。`frontend/src/lib/codeTheme.test.ts` 继续通过。
2. `frontend/src/lib/editor/monokaiLight.ts`：用 `HighlightStyle.define` 按 `vendor/build/highlight.js` 的标签映射生成 `monokaiLightHighlight`（`syntaxHighlighting(...)`），颜色全部取自 `monokaiLightPalette`。

**验证**：单测断言 `monokaiLightHighlight` 里用到的颜色集合与 `monokaiLightPalette` 一致。

### 步骤 2：语言与汉化

1. `languages.ts`：照 `vendor/build/langs.js`，用 `LanguageDescription.of` 列出 Bash、Go、JSON、YAML、TypeScript、JavaScript、Python、SQL、Markdown、Diff，各自按需 `import()`。
   - 不引入 `@codemirror/language-data` 的全量列表。Crepe 会默认引用它，在 Crepe 配置里传 `languages` 覆盖；
   - 打包后若 language-data 仍被打进来，用 Vite 的 `resolve.alias` 指向 `languages.ts`，与设计稿配方相同。
2. `crepeLocale.ts`：按当前界面语言给出 Crepe 的 `featureConfigs`，中文照原型 `zh` 对象：
   - 顶部工具栏 `headingOptions`：正文、标题 1–3；
   - 代码块：`searchPlaceholder`、`noResultText`、`copyText`、`previewToggleText`；
   - 斜杠菜单：`textGroup`、`listGroup`、`advancedGroup` 各项；
   - `placeholder`。

   英文用库的默认文案。

### 步骤 3：`FileEditor.vue`

props：
- `path`（决定语言与模式）；
- `modelValue`（全文）；
- `readonly?`；
- `height?`。

事件：`update:modelValue`、`dirty`。

1. **`.md` 文件**：
   - 两种模式"所见即所得 / 源码"，切换按钮在编辑器**底栏**右侧，照原型 `.mde-foot` / `.mde-seg`；
   - 底栏左侧显示"Markdown"，有改动时显示"· 已修改"。
   - **所见即所得**：`new Crepe({ root, defaultValue, features: { [TopBar]: true, [AI]: false, [Latex]: false, [ImageBlock]: false }, featureConfigs })`：
     - 代码块配置传 `languages` 和 `theme: monokaiLightHighlight`；
     - 监听 `markdownUpdated` 回写 `modelValue`；
     - 组件卸载时 `destroy()`。
   - **源码**：CodeMirror，`basicSetup + monokaiLightHighlight + markdown({ codeLanguages: languages }) + EditorView.lineWrapping`。
   - 切换模式时把当前内容带过去，不丢改动。
   - **SKILL.md 等带 YAML 头的文件**：所见即所得模式下把开头的 `---` 段拆出来，用 `FrontmatterFields.vue` 在编辑器上方显示为字段（`name` 等宽字），保存时拼回原样；源码模式显示原文。
2. **其他文件**：CodeMirror，`basicSetup + monokaiLightHighlight + 按扩展名匹配的语言`。底栏显示语言名，没有模式切换。
3. **样式**：照原型 `.mde` 与 `.mde .milkdown` 的变量映射：
   - 背景、文字、主色接前端 token；
   - 字体用界面字体与等宽字体；
   - `ProseMirror` 内边距 `14px 28px 28px 64px`，左侧留给块拖动柄；
   - 标题字号 1.6 / 1.3 / 1.12em；
   - 代码块语言按钮常显；
   - 代码块和 CodeMirror 背景为 `#fafafa`，正文 `#272822`。

   Crepe 自带的主题 CSS 从 `@milkdown/crepe/theme/common/style.css` 引入，加上 frame 主题，再覆盖变量。
4. **按需加载**：`FileEditor.vue` 用 `defineAsyncComponent` 导出，加载中显示"加载编辑器…"，打包时编辑器单独成一个 chunk。

### 步骤 4：替换文本框

- 记忆查看 / 编辑：018 改成弹窗，弹窗里用 `FileEditor`；
- 工坊技能文件：020 会重做工坊页，本步先在今天的 `WorkshopSkillPanel` 里替换，020 沿用；
- 配置文件：YAML，代码模式；
- 018 的规则文件弹窗、017 的工作目录编辑弹窗也用 `FileEditor`。

### 步骤 5：测试

- 单测：
  - `.md` 默认为所见即所得；切到源码后内容一致；在源码里改一行，切回所见即所得仍在；
  - YAML 头拆成字段并原样拼回（保存的文本与输入逐字相同，除了改动处）；
  - `.sh` / `.json` / `.yaml` 用代码模式；
  - `dirty` 事件。

  jsdom 下 Crepe 与 CodeMirror 可能无法完整运行：只测拆分、拼回、模式选择等纯逻辑，渲染交给 e2e。
- e2e：
  - 打开规则文件编辑弹窗，确认工具栏出现，代码块的语言按钮可见；
  - 在代码块里输入，内容带高亮（检查 `.cm-line span` 的颜色为调色板里的值）；
  - 切源码再切回不丢内容。
  - 在页面里执行 `document.querySelectorAll('.ProseMirror').length` 等检查，确认没有报 "Adding different instances of a keyed plugin"：e2e 里监听 `console` 的 error。
- **往返保真**：用仓库里真实的 `FOREBRAIN.md`、一个 `SKILL.md`、一个记忆文件，在所见即所得里打开、不做改动、直接保存。比较保存前后的文本：
  - 有差异就列出（Crepe 会把 Markdown 重新序列化，例如列表符号、转义）；
  - "未改动不写盘"必须成立：用户没改就不保存，`dirty` 为 false 时"保存"按钮禁用或直接关闭。

## 测试计划

见步骤 5。

## 完成标准

- [ ] 命令表全部通过；许可证核对输出写进报告
- [ ] 与原型 6 个跳转状态的并排截图写进报告
- [ ] 往返保真的差异清单写进报告
- [ ] `grep -rn "<textarea" frontend/src/components/{memory,workshop,settings}/*.vue` 里不再有编辑文件内容的文本框
- [ ] `git status --short` 中本计划新增的改动只涉及范围列出的文件

## STOP 条件

1. 许可证核对出现上面列表以外的许可证。
2. Crepe 打开顶部工具栏后，工具栏的项目无法汉化（`headingOptions` 之外的标签写死）。停下报告哪些文案改不了。
3. 往返保真发现：不做改动就保存会改写文件里大段未触碰的内容。停下报告，由 owner 决定是否接受，或默认用源码模式打开已有文件。

## 维护说明

- 升级 Crepe 或 CodeMirror 前，先在 `vendor/build` 里用新版本重打设计稿，确认没有回归再升前端。
- 代码高亮的颜色只有 `monokaiLightPalette` 一份。消息里的 Shiki 和编辑器里的 CodeMirror 都从它取色，不要在别处另写颜色。
