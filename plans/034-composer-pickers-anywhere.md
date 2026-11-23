# Plan 034：TUI 与 web 的输入框在消息任意位置输入 / 和 @ 都能呼出选择器；消息中间的 /技能名 发送时加载该技能

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> - `grep -n 'composerDisplayText(draft) == "/"' pkg/tui/notify.go` 有 1 行
> - `grep -n "if (!before.startsWith('/')) return null" frontend/src/components/ForebrainPromptTextarea.vue` 有 1 行
> - `grep -n 'func WithExplicitSkillSelection' pkg/run/skills.go` 有 1 行
> 任一不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改输入框选择器的触发规则；技能预加载从"一轮一个"扩成"一轮多个"）
- **Depends on**: 无
- **Category**: bug / feature
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-11：TUI 和 web 的输入框都必须修——现在只有在消息开头输入的 `/` 和 `@` 能呼出命令和文件选择器，必须改成在消息任意位置都能呼出。
owner 已定：**消息中间的 `/` 只列技能**，选中后插入 `/技能名`；发送时引擎识别消息里任意位置的 `/技能名`（可以多个），先加载这些技能再照常执行这条消息。
内置命令（`/plan`、`/goal`、`/init` 等）仍只在开头生效；开头的 `/` 照旧列出全部命令与技能。

## Current state（已核实）

### `/`：两端都只认开头

- TUI：`pkg/tui/notify.go:1681-1697` 的 `HandleDraftUpdate`，只有整个草稿等于 `"/"` 时才新建 `SlashOverlay`。之后由 `SlashOverlay.UpdateFromDraft`（`pkg/tui/commands.go:2153-2192`）按 `strings.HasPrefix(draft, "/")` 过滤或锁定命令。
  `AcceptSlashSelection`（`:2198-2208`）把**整个草稿**替换成 `"/" + name + " "`。
- web：`frontend/src/components/ForebrainPromptTextarea.vue:250-257` 的 `parseSlash` 要求光标前的文字以 `/` 开头且不含换行。命令列表来自 `forebrainApi.slashCommands`（`frontend/src/lib/api.ts:1357-1367`），每条记录都有 `category`（`:323-335`），技能的 `category` 为 `turn.SkillCategory = "skill"`（`pkg/turn/skills.go`）。
- 引擎：`turn.Execute`（`pkg/turn/executor.go:21-40`）只处理以 `/` 开头的消息。技能是动态命令：开头写 `/技能名 请求` 时，处理函数返回 `Result{ShouldContinueRun, ContinueInput, SkillName, SkillPath}`（`pkg/turn/skills.go:60-90`）。
  runner 用 `run.WithExplicitSkillSelection(ctx, name, path)`（`pkg/run/skills.go:320-357`）标记**一个**技能，在预加载阶段加载它；`pkg/run/orchestration_llm.go:1175-1212` 一带会为它画 Skill 卡。
  分发结构里也只有单个 `SkillName`/`SkillPath`（`pkg/turn/model.go:472-500`）。

### `@`：两端都能在中间呼出，但规则不一致，TUI 还有一处失效

- TUI：`turn.TokenAtCursor`（`pkg/turn/compaction.go:314` 起）要求 `@` 在开头或前面是空白。但 `updateMentionOverlay`（`pkg/tui/commands.go:2263-2267`）**只要 `SlashOverlay` 不为空就不弹文件选择器**，所以 `/plan 看看 @pkg` 这种写法 `@` 失效。
- web：`parseMention`（`ForebrainPromptTextarea.vue:259-266`）的正则 `[@＄]([^\s\n@＄]*)$` 连单词中间的 `@`（如 `foo@bar`）也认，与 TUI 不一致。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... ./pkg/run/... ./pkg/tui/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/turn/... ./pkg/run/... ./pkg/tui/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：
- `pkg/turn`：新增取光标处 `/` 记号的函数、技能引用识别、两端共用的记号用例表；
- `pkg/turn/model.go`（分发结构带多个技能）、`pkg/turn` 中把 `Execute` 结果写进分发结构的位置；
- `pkg/run/skills.go` 与 `pkg/run/orchestration_llm.go` 的预加载（扩成多个技能）；
- `pkg/tui/notify.go`、`pkg/tui/commands.go`；
- `frontend/src/components/ForebrainPromptTextarea.vue` 及其测试；
- 对应 Go 测试；`pkg/gateway/dist`。

**Out of scope**：
- 选择器的视觉样式：沿用现有命令和文件选择器的外观，不新增界面；
- 内置命令在消息中间的语义：不生效，只是文字；
- steer、排队消息里的技能引用：本计划不处理，报告里写明；
- subagent 视图的技能命令（`pkg/run/controller.go:53-60` 的单技能字段）：不动。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：一条记号规则，两端共用一份用例

1. `pkg/turn` 新增 `SlashTokenAtCursor(draft string, cursor int) (start int, query string, atStart bool, ok bool)`。规则与 `TokenAtCursor` 一致：
   - `/` 在开头，或前一个字符是空白（含换行）；
   - `/` 到光标之间只含 `tokenRuneAllowed` 允许的字符；
   - `atStart` 表示这个 `/` 是整条消息去掉开头空白后的第一个字符。
2. 新建 `pkg/turn/testdata/composer_tokens.json`，每项是 `{draft, cursor, kind: "@"|"/", ok, start, query, at_start}`，至少 16 条，覆盖：
   - 开头；
   - 空格后；
   - 换行后；
   - 单词中间，如 `foo@bar`、`a/b`，应为不触发；
   - 引号形式的 `@"…"`；
   - 光标不在记号末尾；
   - 中文前后；
   - 开头是命令、中间是 `@`。
3. Go 测试 `TestComposerTokenTable` 读这个文件，逐条断言 `TokenAtCursor` 与 `SlashTokenAtCursor`。

### Step 2：识别消息里的技能引用（引擎）

1. 给技能命令暴露名字与路径，例如在 `skills.go` 的 `DynamicCommand` 上加 `SkillPath`，不必调用处理函数也能拿到。
2. 新增 `turn.SkillReferences(text string) []SkillSelection`：
   - 找出所有满足 Step 1 规则、且后面紧跟空白、行尾或标点的 `/名字`；
   - 用 `Find` 查表，只收 `Category == SkillCategory` 的；
   - 开头那个记号由 `Execute` 处理，这里跳过；
   - 按出现顺序去重。
3. 把结果接进分发：在 `pkg/turn` 里 `Execute` 的结果写进分发结构的那一处（`grep -n 'SkillName' pkg/turn/*.go` 定位），在单个 `SkillName/SkillPath` 之外加 `Skills []SkillSelection`：
   - 开头是技能命令时，列表第一个就是它，后面接上中间的引用；
   - 开头不是命令时，列表只有中间的引用；
   - 消息文字原样发送，`/技能名` 保留在文字里。

   若 TUI 与 gateway 不经同一处写入这个字段（各写各的）→ STOP。

### Step 3：一轮预加载多个技能（runner）

- `WithExplicitSkillSelection` 扩成 `WithExplicitSkillSelections(ctx, []…)`，原函数保留为只包一个元素的薄封装，或把调用方一起改掉；
- 预加载阶段按顺序加载每个技能，每个技能各画一张 Skill 卡（与今天单个技能的卡相同）；
- 任一技能加载失败：照今天的 `ExplicitSkillLoadError` 处理（`pkg/run/skills.go:358` 起），失败的那张卡显示失败，其余照常；
- 若今天的错误路径会让整轮停止，就保持停止，在报告里写明。

### Step 4：TUI 选择器

1. `HandleDraftUpdate`：用 `SlashTokenAtCursor` 代替 `== "/"` 判断。
   - `atStart`：走今天的完整列表；
   - 不是开头：打开"只列技能"的内联模式。`SlashOverlay` 记下 `Inline bool`、`TokenStart int`，`rebuildFiltered` 在内联模式下只留 `Category == SkillCategory` 的项。
2. 内联模式下，`AcceptSlashSelection` 把 `[TokenStart, Cursor)` 替换成 `/名字 `，不替换整个草稿；`UpdateFromDraft` 在内联模式下按光标处记号重新计算。
3. `updateMentionOverlay`：去掉"`SlashOverlay` 不为空就不弹"的整句判断，改为：光标处有 `@` 记号时文件选择器优先，并关掉命令选择器。

### Step 5：web 选择器

1. `parseSlash` 用与 Step 1 相同的规则（TS 里实现）：
   - 开头：照旧；
   - 中间：请求后只保留 `category === 'skill'` 的记录；
   - 接受选择时只替换记号范围。
2. `parseMention` 改为要求 `@` 在开头或空白之后，与 TUI 一致；全角 `＄` 的现有处理保留。
3. 前端测试读同一个 `pkg/turn/testdata/composer_tokens.json`（vitest 里用 `fs.readFileSync` 按相对路径读取），逐条断言两个解析函数与 Go 结果一致。

### Step 6：测试与真机

- Go：
  - `TestSkillReferences`：中间多个技能、重复、未知名字、中间的内置命令被忽略、开头技能加中间技能；
  - runner 一轮预加载两个技能，出现两张 Skill 卡；
  - TUI：草稿 `看下 /imp`，光标在末尾，打开只含技能的内联选择器，接受后变成 `看下 /improve `；草稿 `/plan 看看 @pk` 打开文件选择器。
- 前端：同样两种情况；`foo@bar` 不再触发。
- 真机：用 driver（隔离 home，项目里放一个技能），提交 `请按 /<技能名> 列一下步骤`。在 `$D provider-log` 对应请求的 `request-NNNN.json` 里，能看到该技能的加载内容（照 `pkg/run/skills.go:387` 的 "The user selected the `…` skill" 文案查找）。收尾清理。

## Test plan

见 Step 1、5、6。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] `TestComposerTokenTable`、`TestSkillReferences` 与前端用例表测试存在且 PASS
- [ ] 真机：中间的 `/技能名` 让该技能在这一轮被加载（请求体里可见）
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. TUI 与 gateway 把技能选择写进分发结构的位置不是同一处。
3. 一轮加载多个技能需要改提示缓存冻结的前缀（例如技能内容被写进冻结的提示前缀，而不是本轮输入）——停下报告。
4. 某个既有测试断言"`/` 只在开头触发"并且注明是 owner 的设计决定——停下报告是哪条。

## Maintenance notes

- 记号规则只有一份用例表（`pkg/turn/testdata/composer_tokens.json`），Go 与前端都读它；改规则先改表。
- 新增"开头才生效"的内置命令不需要额外处理；新增技能来源时，确保它们的 `Category` 仍是 `SkillCategory`，中间引用才能识别。
