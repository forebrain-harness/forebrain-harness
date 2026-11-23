# Plan 032：删除 working_set_* 工具及其上下文来源（模型从未调用，没有存在价值）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：`grep -rn 'newWorkingSetShowTool\|newWorkingSetPinTool\|newWorkingSetDropTool' pkg/tool/session_tools.go` 有 3 处定义与 3 处调用；
> `grep -n 'engine.Register(WorkingSetSource{})' pkg/assembly/hook.go` 有 1 行。不符 → STOP。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（删除从未被调用的工具；新会话的工具表与提示前缀会变一次）
- **Depends on**: 无
- **Category**: tech debt
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-11：所有 `working_set_*` 工具都删掉——模型从来没有主动调用过，没有存在价值；计划和设计稿都要改。
核实（本机库只读）：工具结果里提到 `working_set_` 的 109 行全是读源码、grep 的输出，没有一次是 `working_set_show/pin/drop` 本身。
这三个工具每轮都占着工具表与提示词（工具说明），`WorkingSetSource` 每轮把"固定项 + 用户消息里的 @ 引用"再列一遍进上下文，与用户消息本身重复。
设计稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 已去掉工作台里的"优先保留（Working Set）"。

## Current state（全部已核实）

- 工具定义与注册：`pkg/tool/session_tools.go:43-52`（`newWorkingSetShowTool` / `newWorkingSetPinTool` / `newWorkingSetDropTool` 的调用，结果 `t9`、`t10`、`t11` 加进注册列表），定义在 `:575-660`。
- 状态：`pkg/tool/state.go:92`（字段 `workingSetPin map[string][]string`）、`:1036`（初始化）、`:1203-1205` 与 `:1254-1257`（会话清理）、`:1540-1607`（`WorkingSetPins`、`UpsertWorkingSetPins`）。
- 安全白名单：`pkg/safety/engine.go:410`（`safeReadOnlyTools` 里的 `"working_set_show"`）、`:451-452`（`safeInternalTools` 里的 `"working_set_pin"`、`"working_set_drop"`）。
- 输出格式化：`pkg/tool/format.go:140-141`（分派到 `formatWorkingSetPinStep`）、`:1983-2000` 一带（`formatWorkingSetPinStep` 定义）、`:2385`、`:2495`（两个 `case` 列表里的三个名字）。
- TUI 显示：`pkg/tui/render.go:2291-2296`（动词）、`:2785-2812` 一带（三段 `case lower == "working_set_*"`）、`:3244-3250`（`formatWorkingSetPinOutput` 调用；同文件找定义）。
- 上下文来源：`pkg/assembly/sources.go:15-43`（`WorkingSetSource`：`extractAtRefs(req.Query)` + `req.PinnedSet` → 一条 "Task Working Set"），`:89` 起的 `extractAtRefs` 只被它用；
  注册在 `pkg/assembly/hook.go:87`；`PinsProvider` 在 `hook.go:24,69-70`、`runtime_hook.go:25,45`、`pkg/process/open.go:401-407`；
  `AssemblyRequest.PinnedSet`（`pkg/assembly/types.go:50`）、`AssemblyResult.WorkingSet`（`:60`，JSON `working_set`，由 `pkg/assembly/engine.go:139` 填）；
  优先级加成 `pkg/assembly/planner.go:35,40` 里的 `"working_set_source"`。
- **保留**：`LayerWorking`（`pkg/assembly/types.go:11`）也被 `RecentFilesSource` 使用（`sources.go:79`），不删。
- 前端：`frontend/src/components/ContextDebugPanel.vue:93-105` 一带（Working Set 区块）与 `:334`（`workingSet` computed）；`frontend/src/lib/api.ts:262`（`workingSet?: string[]`）；
  `frontend/src/locales/index.ts:738-739`、`:1718-1719`（`chat.contextWorkingSet`、`chat.contextWorkingSetHint`）。
- 引用这些名字的测试：`pkg/tui/chat_session_test.go`、`pkg/turn/compaction_test.go`、`pkg/tool/format_test.go`、`pkg/tool/request_permissions_test.go`、`pkg/tool/state_test.go`、`pkg/gateway/session_context_test.go`、`pkg/process/session_context_test.go`（`grep -rln 'WorkingSet\|working_set\|PinnedSet' pkg --include='*_test.go'`）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/assembly/... ./pkg/safety/... ./pkg/tui/... ./pkg/turn/... ./pkg/gateway/... ./pkg/process/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：Current state 列出的文件与测试；`pkg/gateway/dist`（`make ui`）。

**Out of scope**：`LayerWorking` 常量；`RecentFilesSource`；工作台 / 上下文面板的重新设计（另立计划，按设计稿 1:1 还原）；历史 transcript 里可能存在的旧调用（回放走通用工具卡，见 Step 4）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：删工具

1. `session_tools.go`：删掉 `:43-52` 的三段构造与它们在注册列表里的 `t9/t10/t11`（后面的编号顺延或改为直接追加，保持同文件写法）；删掉 `:575-660` 的三个构造函数与输入类型。
2. `state.go`：删掉字段、初始化、两处会话清理、`WorkingSetPins`、`UpsertWorkingSetPins`。
3. `safety/engine.go`：从两份白名单里删掉三个名字。
4. `format.go`：删掉分派、`formatWorkingSetPinStep`，以及两个 `case` 列表里的三个名字（列表里其他名字不动）。

**Verify**：`go build -tags fts5 ./...` 通过；`grep -rn 'working_set_show\|working_set_pin\|working_set_drop\|WorkingSetPins\|UpsertWorkingSetPins' pkg --include='*.go' | grep -v _test.go` 只剩 TUI（Step 3 处理）。

### Step 2：删上下文来源

1. `sources.go`：删掉 `WorkingSetSource` 与只被它用的 `extractAtRefs`（先 `grep -rn 'extractAtRefs' pkg` 确认无其他调用；有 → STOP）。
2. `hook.go`：删掉 `engine.Register(WorkingSetSource{})`、`PinsProvider` 字段与 `pins` 变量；`runtime_hook.go` 删掉 `PinsProvider`；`process/open.go` 删掉 `PinsProvider:` 那段。
3. `types.go`：删掉 `AssemblyRequest.PinnedSet` 与 `AssemblyResult.WorkingSet`；`engine.go:139` 删掉对应赋值；`planner.go` 两处 `case` 删掉 `"working_set_source"`（同 case 的其他来源保留）。

**Verify**：`go build -tags fts5 ./...` 通过；`grep -rn 'WorkingSetSource\|PinnedSet\|PinsProvider\|\.WorkingSet\b\|working_set_source' pkg --include='*.go' | grep -v _test.go` 无输出。

### Step 3：TUI 显示

删掉 `render.go` 里三个名字的动词、三段 `case` 与 `formatWorkingSetPinOutput`（及其只被它用的辅助函数，逐个 grep 确认）。
回放里若有旧的 `working_set_*` 调用，走通用工具卡（不再有专门的标签）——这是预期行为。

### Step 4：前端

删掉 `ContextDebugPanel.vue` 的 Working Set 区块与 `workingSet` computed、`api.ts` 的 `workingSet` 字段、两种语言的两个文案键。其余区块不动。

**Verify**：`grep -rn 'workingSet\|contextWorkingSet\|working_set' frontend/src` 无输出；前端命令全绿；`make ui` 成功。

### Step 5：测试

更新 Current state 列出的测试：删掉只测这三个工具或 working set 的用例；断言"工具表包含 working_set_*"的改为断言**不包含**。
新增一条 `pkg/tool` 测试 `TestWorkingSetToolsAreGone`：构建一次会话工具表，断言没有任何名字以 `working_set_` 开头。

## Test plan

见 Step 5。

## Done criteria

- [ ] Commands 表全部通过
- [ ] `grep -rn 'working_set\|WorkingSet\|PinnedSet\|PinsProvider' pkg cmd frontend/src --include='*.go' --include='*.ts' --include='*.vue' | grep -v 'LayerWorking'` 无输出（`LayerWorking   Layer = "working_set"` 一行除外）
- [ ] `TestWorkingSetToolsAreGone` 存在且 PASS
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. `extractAtRefs` 或 `PinnedSet` 还有本计划没列出的使用者。
3. 某个测试说明 @ 引用只靠 `WorkingSetSource` 才进入模型上下文（例如断言 "Task Working Set" 条目里的路径被模型读取）——停下报告，由 owner 决定 @ 引用的去处。

## Maintenance notes

- 删除后，新会话的工具表与提示前缀变化一次（首轮提示缓存未命中），属预期。
- 以后若要"固定上下文"，先有模型确实会用的证据再加，不要重新引入同类工具。
