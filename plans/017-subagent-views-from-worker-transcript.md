# Plan 017：subagent 视图（web 与 TUI）在重载/回放时从它自己的 worker 会话 transcript 重建文字，而不是重放逐 token 增量

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> `grep -c 'transcript.flush(' pkg/run/orchestration_llm.go` → ≥ 3（plan 016 已执行；否则 STOP——本计划依赖 016 让 subagent 的文字及时进它的 worker transcript）。
> `git diff --stat 005c1a8 -- pkg/gateway/api_extra.go pkg/tui/commands.go` 只应有 015/016/022 已知的改动；对照 "Current state" 核对。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（换掉 subagent 视图重载的数据来源；web 与 TUI 两套投影都要改；必须做到"换源前后看到的一样"）
- **Depends on**: `plans/016-transcript-written-at-text-block-end.md`
- **Blocks**: `plans/018-*`（增量不落库）——本计划完成前不能停止落增量，否则 subagent 视图重载后没有文字
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树工作区未提交改动，2026-10-10

## Why this matters

owner 裁决：流式文字只存 transcript，增量不再落库，**subagent 本期一并改**——subagent 视图（web 与 TUI）改为从 worker 会话的 transcript 重建。

现状：每个 subagent 执行都有自己的 worker 会话（`fb_sessions.source='subagent'`），它的回答、思考、工具结果写在那里的 transcript 里；但两端重载 subagent 视图时**不读它**，
而是重放主会话事件日志里带 `agent_id` 的逐 token `assistant_delta`/`reasoning_delta`（本机约 450 万行）。所以文字存了两份，且重载要翻上百页事件。

本计划只换"重载/回放时文字从哪来"；live 绘制不变（仍由实时事件驱动）；工具卡、审批、生命周期等事件照旧。

## Current state

### web 重载

`frontend/src/composables/useChatStream.ts` 的 `loadMessages`（`grep -n 'async function loadMessages' ...`）：并行取主会话 `chatMessages(target)`、
全部 `sessionEvents`、旧的 `sessionSubagentHistory`；主对话用 `conversationFromTranscript(rows)`；随后按 sequence 回放事件 `applyObservedEvent(evt, true)`。
`applyObservedEvent` 对**主对话**的 transcript 已承载类型（`transcriptBackedEventTypes`：`assistant_delta`、`reasoning_delta`、`reasoning_done`、`input_delivered`、`tool_call_*`）在历史回放时跳过，
但对 subagent 事件不跳过（`isSubagentHistoryEvent(evt)` 为真时照常回放）——subagent 视图今天完全由事件重建（注释称之为 "lossless event path"）。
事件按 `agent_id` 路由进 `routeSubagentEvent`（约 `:2800` 起，`assistant_delta`/`reasoning_delta` 在 `:2800`、`:2817`），写入 `subagents` 记录的 `blocks`
（prompt / user / thinking / assistant / tool / approval / worked …）。`subagent_spawned` 的 payload 带 `worker_session_id`、`execution_id`、`task`（prompt）。

### gateway 接口

`GET /api/chat/sessions/:id/messages` → `handleChatMessages`（`pkg/gateway/api_extra.go:1184` 起），先 `s.sessionOwned(ctx, sid)`；worker 会话属于同一 agent，
按理可以直接用它读 worker transcript——Step 1 先确认。

### TUI 回放

`pkg/tui/commands.go`：主对话从 transcript 行回放；subagent 视图从事件回放——`replayEventMessage`（约 `:990-1110`）把带 `AgentID` 的
`assistant_delta`/`reasoning_delta`/`reasoning_done`/`tool_call_*`/`subagent_spawned`/`subagent_ended` 变成 reducer 消息（`NewMessageMsg{... AgentID}` 等）。
`replayToolEventSkipped`（`:1118-1123`）：主 agent 的工具事件跳过、subagent 的不跳过。

### 本机数据（迁移前，plan 019 才处理旧数据）

238 次 subagent 执行引用 222 个 worker 会话，库里只剩 70 个 subagent 会话（6327 行）——**约 150 个 worker 会话已不存在**，它们的文字目前只在增量事件里。
本计划必须让"worker transcript 不存在"的执行**退回事件重放**（旧行为），由 plan 019 迁移补齐后再统一。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... ./pkg/gateway/... ./pkg/state/... ./pkg/run/... -count=1` | 全部 `ok` |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：`frontend/src/composables/useChatStream.ts`（重载时 subagent 文字的来源）、`frontend/src/lib/api.ts`（若需新参数）、相关前端测试；
`pkg/gateway/api_extra.go`（若 Step 1 发现 messages 接口拒绝 worker 会话，放开**仅限本会话下属**的 worker 会话）、测试；
`pkg/tui/commands.go`（回放 subagent 视图的文字来源）、`pkg/tui/*_test.go`。

**Out of scope**：live 绘制；停止落增量（plan 018）；旧数据（plan 019）；subagent 的工具卡、审批、生命周期事件（继续由事件提供）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1（调研，先做，不改代码）：读得到 worker transcript 吗、它和事件重放出来的内容一致吗

1. 在一个能产生 subagent 的会话上取数据：用 driver 的 `subagent-net` 或能派发 subagent 的模式（`.claude/skills/run-forebrain/SKILL.md`）跑一轮，
   或用 gateway e2e 的 `subagent-conversation.spec.ts` 同款造数方式；记下主会话 id、`subagent_spawned` 的 `worker_session_id`。
2. 起 gateway（同一 home），`GET /api/chat/sessions/<worker_session_id>/messages` 是否返回 worker 的 transcript 行？若 404/403 → 记下原因。
3. 对比：同一个 subagent，(a) 现有事件重放得到的 blocks 序列（kind + 文字），(b) worker transcript 行的序列。写一个**不提交的** vitest 探针（`*.probe.test.ts`，跑完删除）
   打印两者。记录差异：prompt 是否在两边都有、思考/回答分段是否一致、工具卡是否一一对应、`worked`/错误等生命周期块只在事件里。
4. **把调研结果写进报告**，然后：若 (b) 与 (a) 的文字内容能一一对上（只差生命周期块），继续 Step 2；若差异超出"生命周期块只在事件里"（例如 worker transcript
   缺段、顺序不同、工具结果对不上）→ **STOP**，报告差异，评审方会据此修订本计划。

### Step 2：gateway 读 worker transcript

若 Step 1.2 不通：给 `handleChatMessages` 放开"属于本 agent 的 subagent 会话"——**只有**当 worker 会话的 `parent_session_id` 链能回到一个本 agent 拥有的会话时才放行（读 `fb_sessions.parent_session_id`）；
不要放宽对其他会话的所有权检查。加测试：本 agent 会话下属的 worker 可读，别的 agent 的 worker 404。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` → `ok`。

### Step 3：web 重载 subagent 文字改从 worker transcript 来

在 `loadMessages` 里：事件回放之前，从事件中找出所有 `subagent_spawned`（`worker_session_id` 非空），并行取这些 worker 会话的 messages；
对**取到行**的执行（按 `execution_id` / worker 会话区分），历史回放时跳过该执行的 `assistant_delta`、`reasoning_delta`、`reasoning_done`、`tool_call_*`、`tool_output_delta`，
改由 worker 行生成这些块（复用 `conversationFromTranscript` 的逐行转换逻辑——把它对单行的转换抽成可复用的函数，不复制一份），插入该 subagent 记录的 `blocks`
中这个执行对应的位置（执行的 prompt 块之后、它的 worked/ended 块之前）。生命周期、审批、计划卡等其余事件照旧回放。
**取不到行**（worker 会话不存在或为空）的执行：完全按今天的事件回放（不跳过任何类型）。
prompt 去重：worker transcript 的第一条 user 行若与 `subagent_spawned.task` 相同，只保留一份（保留事件那份的 `kind:'prompt'` 形状）。

**Verify**：前端测试通过（含 Test plan 2、3）。

### Step 4：TUI 回放 subagent 文字改从 worker transcript 来

`pkg/tui/commands.go` 回放时：收集 `subagent_spawned`（`WorkerSessionID`），对有 worker 行的执行，跳过它的文字/工具增量事件，改为把 worker 行转换成同样的 reducer 消息
（`MsgKindAssistant`/`MsgKindReasoning`（带 Step 016 的耗时）/`MsgKindTool`，都带该 subagent 的 `AgentID`）按行序喂入，位置与原事件序列中该执行的文字所在位置相同
（spawned 之后、ended 之前）。没有 worker 行的执行照旧走事件。读 worker 行用已有的 `SessionStore` 读取方法（与主对话回放读 transcript 的方式一致）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1` → `ok`（含 Test plan 4）。

### Step 5：真机对照

用 Step 1 的会话：web 重载（vitest 探针或浏览器）与 TUI `resume` 回放里，subagent 视图的文字与 Step 1.3 记录的"事件重放"结果一致；探针删除；临时目录清理。

## Test plan

1. gateway：worker transcript 可读（本 agent 下属）/ 不可读（他人）。
2. 前端：`a reloaded subagent reads its words from its own transcript`——打桩主会话 rows、事件（含 spawned/ended 与该 subagent 的增量）和 worker rows；
   重载后 subagent 记录的 blocks 与"只用事件重放"得到的 blocks（同一份数据，去掉 worker rows 时的结果）**文字与顺序相同**。
3. 前端：worker 会话缺失时退回事件重放（结果与今天相同）。
4. TUI：回放含 worker 行的 subagent → agent 视图帧序列与仅事件回放一致；worker 缺失时走事件。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] Test plan 4 组测试存在且 PASS
- [ ] Step 1 调研结果与 Step 5 对照结果写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check：plan 016 未执行。
2. Step 1.4：worker transcript 与事件重放的文字内容对不上（超出"生命周期块只在事件里"）。
3. 放开 worker 会话读取需要放宽所有权检查到 Step 2 描述之外。
4. 改动迫使 live 绘制路径变化（本计划只改重载/回放）。

## Maintenance notes

- 本计划落地后，subagent 文字在重载/回放时的唯一来源是 worker transcript（缺失时退回事件，plan 019 迁移补齐后这条退路基本不再触发）。
- plan 018 停止落增量后，新会话的 subagent 只有 worker transcript 这一份文字。
