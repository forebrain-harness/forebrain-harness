# Plan 033：回到这里——按会话记录文件工具的改动，三种恢复方式（代码和对话 / 只对话 / 只代码），可撤销；web"回到这里"与 TUI `/rewind` 共用一套引擎；工作台"变更文件"的数据源；去掉旧的"撤销最近一次文件改动"

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **web 界面 1:1 还原设计稿** `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`：在"本轮新增"里点"回到这里"和"恢复之后"；对应原型函数 `threadHtml()` 的 `.u-wrap` / `.u-acts`、`rewindModal()`、`.sys-line` 的撤销行，CSS 前缀 `.rw-`。
>
> **Drift check（先跑）**：
> - `grep -n "func (s \*State) SnapshotBeforeWrite" pkg/tool/state.go` 有 1 行；`grep -n "SnapshotBeforeWrite(" pkg/tool/file_tools.go | wc -l` 为 4。
> - `grep -n "rewind-last" pkg/gateway/api_extra.go` 有 1 行；`grep -n "sessionRewindLast" frontend/src/lib/api.ts frontend/src/views/ChatView.vue` 各 1 处。
> - `grep -n "messageWithdrawn = \"withdrawn\"" pkg/state/session_store.go` 有 1 行。
>
> 不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（改写用户文件；对话行的可见性新增一种）
- **Depends on**: 025（每个会话有项目根）、026（文件工具按项目根解析路径）
- **Category**: feature / bug（取代有缺陷的 rewind-last）
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-11 定的设计（`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 2.10）：
- **恢复点**：每条用户消息之前，也就是这条消息启动的那一轮开始之前。
- **记录范围**：只记文件工具（`edit_file`、`write_file`、`apply_patch`）的改动；终端命令造成的改动不撤回，确认框里写明。
- **三种方式**：
  - 代码和对话（默认）；
  - 只恢复对话；
  - 只恢复代码。
- **恢复代码**：这条消息之后各轮改过的文件，恢复到第一次改动之前的内容；之前不存在的文件删除。
  - 确认框列出文件和增删行数；
  - 当前内容与最后一次工具写入后的内容不一致的文件标"之后被改过"，可逐个取消勾选。
- **恢复对话**：之后的行不显示、不进模型历史，但数据留在库里；这条消息放回输入框。
- **可撤销**：发出下一条消息之前都能撤销。恢复前先把当前文件内容存一份，撤销时写回，并去掉回退点。发出新消息后回退点固定下来。
- **两端**：web 在用户消息上悬停出现"回到这里"，TUI 用 `/rewind`。两端共用同一套实现。对话正在运行时不可用。
- **工作台"变更文件"**（owner 2026-10-11）：列出本会话用文件工具改过的文件，每个可对比、编辑、撤销变更，与本计划同一份记录。

今天的"撤销最近一次文件改动"（`POST /api/chat/sessions/:id/rewind-last`）有四个问题：
- 只恢复一个文件；
- 用的快照不分会话，可能取到别的会话的；
- 不能预览，也不能撤回这次恢复；
- TUI 没有对应功能。

所以这个接口和按钮都要去掉。

## Current state（已核实）

- **快照**：
  - `State.SnapshotBeforeWrite(absPath, content)`（`pkg/tool/state.go:2431-2447`）写 `<historyDir>/<sha256(path)[:8]>/<纳秒>.snapshot`。**不记会话、轮次、步骤**，也不记"写之前文件是否存在"。
  - `historyDir` 由 `pkg/run/runner.go:1087` 设为 `<stateDir>/file-history`。
  - 调用处：
    - `pkg/tool/file_tools.go:507`：`write_file` 覆盖已有文件，只在文件已存在时调用；
    - `:678`：`edit_file`；
    - `:1261`、`:1265`：`apply_patch`，含移动。
  - 新建文件时没有任何记录。
- **rewind-last**：
  - 路由在 `pkg/gateway/api_extra.go:119`；
  - 处理函数 `handleSessionRewindLast`（`:2112-2165`）：
    - 用 `RunRT.LatestRewindableToolCall` 找会话最后一次成功的写入（`pkg/state/session_events.go:423-447`）；
    - 再用 `LatestSnapshot`（`pkg/tool/state.go:2449` 起）取该路径**任何会话**的最新快照覆盖回去。
  - 前端：`frontend/src/lib/api.ts:1672-1673`、`frontend/src/views/ChatView.vue:1406`。
- **对话可见性**：
  - `fb_messages.visibility CHECK IN ('visible','repaired','withdrawn')`（`pkg/state/schema.sql:66`）；
  - 所有读对话记录的地方都只取 `'visible'`（`pkg/state/session_store.go:1800-1808` 的约定，8 处查询，`grep -rn "visibility = 'visible'\|visibility='visible'" pkg/state`）；
  - 唯一的写入函数 `setMessageVisibility`（`:1816` 起）。
- **事件**：
  - `fb_session_events` 以 `sequence` 自增排序（`schema.sql:194`），没有可见性列；
  - web 重载与 TUI 回放的显示由对话记录与事件共同重建。
- **本机体量**：`fb_messages` 约 10 万行，`fb_session_events` 约 457 万行（019 会删掉大部分增量）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/tool/... ./pkg/turn/... ./pkg/run/... ./pkg/gateway/... ./pkg/tui/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：
- `pkg/state`：schema、迁移，新文件 `pkg/state/rewind.go`；
- `pkg/tool/{state.go,file_tools.go}`：记录改动；
- `pkg/turn`：新文件 `pkg/turn/rewind.go`，两端共用的恢复逻辑；
- `pkg/run`：把记录器接到工具状态上；
- `pkg/gateway`：新接口，删 rewind-last；
- `pkg/tui`：`/rewind` 命令；
- 前端：用户消息的悬停操作、`RewindDialog.vue`、撤销行；`api.ts`；删 `sessionRewindLast`；locale；
- 测试、`pkg/gateway/dist`。

**Out of scope**：
- 工作台"变更文件"标签的界面（web 017，用本计划的接口）；
- 终端命令改动的追踪。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：库结构（迁移 vN+1，版本号顺延）

1. **新表 `fb_file_changes`**，每次文件工具写一个文件记一行：
   - `id`：主键，自增；
   - `session_id`：引用 `fb_sessions(id)`，`ON DELETE CASCADE`；
   - `run_id`、`step_id`：文本；
   - `anchor_message_id`：这一轮的用户消息在 `fb_messages` 的行 id，整数；
   - `path`：绝对路径，规范化；
   - `existed_before`：0 或 1；
   - `before_snapshot`：快照文件路径，文件原本不存在时为空；
   - `after_sha256`：工具写完后的内容哈希，删除时为空；
   - `deleted`：0 或 1，`apply_patch` 删除文件时为 1；
   - `created_at_ms`。
   - 索引：`(session_id, anchor_message_id)`、`(session_id, path)`。
2. **新表 `fb_rewinds`**，每次"回到这里"一行：
   - `id`：文本主键；
   - `session_id`：外键，`CASCADE`；
   - `anchor_message_id`：回到哪条用户消息之前；
   - `mode`：`'both'`、`'chat'` 或 `'code'`；
   - `hidden_event_seq_from`：被隐藏的第一个事件序号，只恢复代码时为空；
   - `files_json`：恢复了哪些文件、每个文件恢复前当前内容的快照路径，供撤销用；
   - `state`：`'pending'`（可撤销）或 `'fixed'`（发出新消息后固定），`'undone'` 的行直接删除；
   - `created_at_ms`。
3. **`fb_messages.visibility` 增加 `'rewound'`**。CHECK 约束要改，需重建 `fb_messages`：照 schema 迁移里已有的"改名、建新表、复制、删旧表"写法（`grep -n "rename-aside\|_legacy" pkg/state/schema_migrations.go | head`），保留全部列、索引、触发器（`grep -n "fb_messages" pkg/state/schema.sql`）。本机约 10 万行。`schema.sql` 同步。
4. `session_store.go` 的可见性常量加 `messageRewound = "rewound"`，注释补一句：被"回到这里"移出的行，撤销时恢复为 `visible`。

### Step 2：记录文件工具的改动

1. `tool.State` 增加一个记录器接口：

   ```go
   type FileChangeRecorder interface {
       RecordFileChange(ctx context.Context, c FileChange) error
   }
   ```

   由 runner 在 `Load` 时注入（`pkg/run/runner.go:1087` 设 `historyDir` 的同一处），实现放在 `pkg/state/rewind.go`。
2. `SnapshotBeforeWrite` 改签名为 `SnapshotBeforeWrite(ctx, absPath string, content []byte, existed bool) (snapshotPath string, err error)`：
   - 文件已存在时写快照，返回快照路径；不存在时返回空路径；
   - 快照目录改为按会话分：`<historyDir>/<会话 id>/<sha256(path)[:8]>/<纳秒>.snapshot`，会话 id 取 `ConversationSessionIDFromContext(ctx)`，为空时用旧布局。
3. 四处调用改为：
   - 写之前：不管文件存不存在都调用一次，`write_file` 新建文件也要调；
   - 写成功之后：调 `RecordFileChange`，带上会话 id、run id、step id（上下文里都有，用 `grep -n "func RunIDFromContext\|func StepIDFromContext\|func ConversationSessionIDFromContext" pkg/tool/*.go` 找函数）、本轮用户消息的行 id、写后内容的 sha256，以及是否删除。
   - "本轮用户消息的行 id"若上下文里没有：在 `Runner.RunContent` 入口查出本轮用户消息的行 id 放进上下文（新的上下文键）。查不到 → STOP。
4. 子 agent（subagent）的改动记在**父会话**上：用 `ConversationSessionIDFromContext`，与计划文件用的会话 id 同一个来源，核对 `tool.PlanSessionIDFromContext` 的写法。
5. `LatestSnapshot` 只剩 rewind-last 在用，Step 5 删掉 rewind-last 后一并删除。

**Verify**：`pkg/tool` 测试：
- `write_file` 新建、`write_file` 覆盖、`edit_file`、`apply_patch` 移动和删除，各产生正确的 `fb_file_changes` 行；
- 快照落在会话目录下。

### Step 3：两端共用的恢复逻辑（`pkg/turn/rewind.go`）

```go
type RewindPreview struct {
	AnchorMessageID int64
	AnchorText      string
	Files           []RewindFile // path, +/- lines, changedAfter (之后被改过)
}
func PreviewRewind(ctx, store, sessionID string, anchorMessageID int64) (RewindPreview, error)
func Rewind(ctx, store, sessionID string, anchorMessageID int64, mode string, keepPaths []string) (RewindResult, error)
func UndoRewind(ctx, store, sessionID string) error
func SessionFileChanges(ctx, store, sessionID string) ([]ChangedFile, error) // 工作台"变更文件"
func RevertFile(ctx, store, sessionID, path string) error                    // 单个文件撤销变更
```

1. **前置条件**：会话有 run 在 `running` 或 `waiting_action` → 返回 `ErrRewindBusy`。
2. **代码部分**（`mode` 为 `both` 或 `code`）：
   1. 取 `anchor_message_id >= anchor` 的所有改动，按路径分组；每组用最早一行的 `existed_before` / `before_snapshot`，最晚一行的 `after_sha256`；
   2. 当前内容哈希 ≠ 最晚一行的 `after_sha256` → 预览里标 `changedAfter`；
   3. 恢复 `keepPaths` 之外的文件：
      - 先把当前内容存一份快照，路径记进 `fb_rewinds.files_json`；
      - 原本存在的写回 `before_snapshot`；
      - 原本不存在的删除。
   4. 增删行数由当前内容与恢复后内容逐行比较得出（仓库里现成的 diff 工具：`grep -rn "func DiffStat\|func LineDiff" pkg | head`）。
3. **对话部分**（`mode` 为 `both` 或 `chat`）：
   - 把 `id >= anchor` 的 `fb_messages` 设为 `rewound`，经 `setMessageVisibility` 或它的批量版本；
   - 记录 `hidden_event_seq_from` = 锚点那一轮第一个事件的 `sequence`；
   - 返回锚点消息的文字，供输入框回填。
4. **同时最多一个 `pending`**：再次"回到这里"时，先把已有的 `pending` 记录固定，再执行新的。撤销只撤最近一次。
5. **固定**：会话里发出新的用户消息时，把该会话的 `pending` 记录改为 `fixed`。在两端共用的提交入口处做（`grep -rn "func.*AppendUserMessage\|InsertUserMessage" pkg/state pkg/turn | head`），只放一处。
6. **撤销**：
   - 写回 `files_json` 里的快照；
   - 把这次回退设成 `rewound` 的行恢复为 `visible`，按 `fb_rewinds` 记录的锚点与 `hidden_event_seq_from` 定位；
   - 删除这条记录。
7. **事件的隐藏**：事件读取方（web 的 `/events`、重载、TUI 回放；`grep -rn "FROM fb_session_events" pkg/state | grep -v _test`）对每个会话排除 `sequence >= hidden_event_seq_from` 且属于 `pending` 或 `fixed` 回退区间的事件。
   - 区间终点：该回退之后第一条新用户消息那一轮的第一个事件，`pending` 时没有终点；
   - 把这个过滤写成 `pkg/state` 里的一个函数，所有读取方调用它。
8. **变更文件**：
   - `SessionFileChanges` 按路径汇总全会话的改动：最早的 `before`、当前的增删行数、是否新建；
   - `RevertFile` 把该路径恢复到最早的 `before`（原本不存在的删除），并删掉该路径的 `fb_file_changes` 行。这样"变更文件"里就不再显示它。

### Step 4：gateway 接口

- `GET /api/chat/sessions/:id/rewind-preview?message_id=` → `RewindPreview`
- `POST /api/chat/sessions/:id/rewind`，body `{message_id, mode, keep_paths}` → `{anchor_text, restored:[...]}`
- `POST /api/chat/sessions/:id/rewind/undo`
- `GET /api/chat/sessions/:id/file-changes` → 变更文件列表
- `POST /api/chat/sessions/:id/file-changes/revert`，body `{path}`
- `GET /api/chat/sessions/:id/file-changes/diff?path=` → 统一 diff 文本，"对比"用

`ErrRewindBusy` 返回 409 `{"error":"session_busy"}`。消息行 id 的来源：web 的消息列表接口要给每条用户消息带上行 id（`handleChatMessages`，核对现有返回字段，已有就复用）。

### Step 5：删除 rewind-last

删除：
- 路由（`api_extra.go:119`）；
- `handleSessionRewindLast`；
- `RunRT.LatestRewindableToolCall`：先 `grep -rn "LatestRewindableToolCall" pkg` 确认只有它在用；
- `LatestSnapshot`；
- 前端的 `sessionRewindLast` 与 `ChatView.vue:1406` 那段调用及其按钮、文案。

### Step 6：web

1. 用户消息悬停时，右下出现两个图标按钮："回到这里"（`history` 图标）、"复制"。结构照原型的 `.u-wrap` / `.u-acts`，键盘聚焦时也显示。对话在运行时，"回到这里"禁用，悬停提示"对话运行中"。
2. `RewindDialog.vue` 结构照 `rewindModal()`：
   - 标题"回到这条消息之前？"；
   - 消息原文；
   - 三个单选："代码和对话""只恢复对话""只恢复代码"；
   - 文件列表（复选框、路径、"之后被改过"标记、增删行数）与"终端命令造成的改动不会撤回。"，只恢复对话时隐藏；
   - "之后的对话会移除，这条消息放回输入框。"，只恢复代码时隐藏；
   - "取消""恢复"。
3. 恢复成功后：
   - 对话部分：移除之后的气泡，锚点消息放回输入框；
   - 消息流末尾显示撤销行，照原型 `.sys-line`：
     - 代码和对话："已回到这条消息之前 · 恢复 N 个文件 [撤销]"；
     - 只恢复代码："已把 N 个文件恢复到“…”之前 [撤销]"。
   - 发出新消息后撤销行消失。

### Step 7：TUI `/rewind`

1. 在 TUI 斜杠命令表里注册 `/rewind`（照 `/resume` 的写法：`pkg/tui/run.go:1159` 一带、`turn` 的命令目录），在会话运行时拒绝（文案同 web）。
2. 交互，组件沿用 `/resume` 用的选择器与 `Review` 确认页：
   1. 有可撤销的回退时，列表第一项是 `Undo the last rewind`；
   2. 列出本会话的用户消息，新的在上，第一行截断显示；
   3. 选中后选恢复方式：`Code and conversation`、`Conversation only`、`Code only`；
   4. 有文件要恢复时，确认页列出文件（路径、`+n −m`、`changed since`），可逐个取消，再 `Restore` / `Cancel`。
   5. 完成后，对话部分在视图里移除之后的内容（重放当前会话），锚点消息放进输入框，留一行 `Rewound to before “…” · /rewind to undo`。
3. TUI 文案沿用英文。

### Step 8：测试

- `pkg/turn`：
  - 两轮里改了 a、b，又新建 c；回到第一轮之前（both）→ a、b 恢复、c 删除、第一轮及之后的行变成 `rewound`；
  - 撤销后全部还原；
  - 只对话 / 只代码各一例；
  - 第二轮之后用户手改了 b → 预览标 `changedAfter`，`keepPaths` 含 b 时 b 不动；
  - 发新消息后撤销返回错误；
  - 运行中返回 `ErrRewindBusy`；
  - subagent 的改动记在父会话上。
- `pkg/state`：`rewound` 行不进对话记录读取与模型历史；事件过滤；迁移测试（含 `fb_messages` 重建后行数、索引、外键检查）。
- gateway：各接口；rewind-last 路由 404。
- 前端：悬停操作、对话框三种模式的显隐、撤销行、发新消息后撤销行消失。
- TUI：命令流程测试，照 `/resume` 测试的桩写法。
- 真机（必须，glm-5.3，隔离 home）：
  1. TUI 里让模型用 `write_file` 写两轮文件；
  2. `/rewind` 回到第一轮之前，文件恢复、视图移除；
  3. 再 `/rewind` 撤销，全部回来；
  4. web 打开同一会话，"变更文件"里能看到（017 未做时用接口 `curl` 看）。

  把步骤和结果写进报告。

## Test plan

见 Step 8。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] `grep -rn "rewind-last\|RewindLast\|LatestRewindableToolCall\|LatestSnapshot" pkg frontend/src --include='*.go' --include='*.ts' --include='*.vue' | grep -v _test` 无输出
- [ ] 真机四步都符合预期
- [ ] 与原型"回到这里""恢复之后"截图对照，写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 工具写文件时，上下文里拿不到本轮用户消息的行 id，也找不到注入点。
3. `fb_messages` 重建在本机副本上超过 1 分钟（先用 `cp` 的副本测；不要碰真实库）。
4. 事件读取方太分散（超过 6 处各自拼 SQL），无法用一个过滤函数收口：停下报告清单。

## Maintenance notes

- 以后新增会写文件的工具，必须走 `SnapshotBeforeWrite` 和 `RecordFileChange`，否则"回到这里"和"变更文件"都看不到它。评审时检查。
- `fb_messages` 的可见性现在有四个值，读取方仍然只认 `visible`。新增读取方时不要按"非 withdrawn"写反向条件。
- 快照文件不会自动清理（与今天相同）。体量大了再另立计划。
