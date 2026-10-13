# Plan 015：首 token 延迟——每轮对话测一次、只存一次（fb_runs.first_token_ms），web 与 TUI 在运行中状态行和 Worked 行里显示

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> ```bash
> git diff --stat 005c1a8 -- pkg/state/schema.sql pkg/state/schema_migrations.go pkg/state/session_store.go pkg/turn/submit.go pkg/turn/service.go pkg/turn/compaction.go pkg/tui/commands.go pkg/tui/reducer.go pkg/gateway/api_extra.go
> grep -n 'const stateSchemaVersion' pkg/state/schema_migrations.go
> ```
> 预期：第一条无输出（若 plan 014 已执行，可能看到 `pkg/tui/*` 以外无变化）；第二条为 `const stateSchemaVersion = 8`。
> **若 stateSchemaVersion 已大于 8**（别的计划先加了迁移），本计划的迁移版本号取"当前值 + 1"，其余照做，并在报告写明实际版本号。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（改表结构，要给旧库写迁移；改两端的状态行与 Worked 行文字）
- **Depends on**: none（与 014 无代码依赖）
- **Category**: direction
- **Planned at**: commit `005c1a8` + 主树工作区里 008/011/012/013 的未提交改动，2026-10-10

## Why this matters

owner 2026-10-10 的裁决：**首 token 延迟一轮对话一个值、只存一次，存在 `fb_runs`（不写进 assistant 行的 `usage_json`），
web 与 TUI 都在每轮末尾的 Worked 行里显示，live 在首个 token 到达时就显示，重载/回放从库里读。** 口径：从这一轮（run）开始到
模型第一次输出（思考或回答的第一个 token）；审批后续跑仍是同一个 run，不重新计时。

今天没有任何地方测量或显示它（`event.AssistantDeltaPayload.FirstDeltaMS` 只在 gateway 一条少见路径上写、无人读）。

## Current state

### run 的计时与回放读取（沿用这条路径）

- `fb_runs`（`pkg/state/schema.sql` 的 `CREATE TABLE fb_runs`）有 `started_at_ms`/`finished_at_ms`/`worked_ms`（run 结束时一次写入，
  `SessionStore.StampRunTiming`，`pkg/state/session_store.go:1433-1455`，`UPDATE fb_runs SET ... WHERE id=? AND finished_at_ms IS NULL`）。
- transcript 读取时联表带出 run 计时：`pkg/state/session_store.go:1043`

```go
const messageColumnsJoined = `m.id, m.run_id, m.role, m.content, m.message_id, m.parts, m.model, m.created_at, m.usage_json, m.tool_step_id, m.tool_meta_json, m.exec_started_at_ms, m.exec_finished_at_ms, m.exec_duration_ms, r.started_at_ms, r.finished_at_ms, r.worked_ms`
```

  扫描在 `:1060`（`row.RunStartedAtMs, row.RunFinishedAtMs, row.RunWorkedMs = runStart.Int64, runFinish.Int64, runWorked.Int64`），
  拷贝到 `state.Message` 在 `:1081`；`RunWorkedMs` 字段在 `:618`、`:1033` 两个结构体里。
- 两端回放的 Worked 行都来自 `turn.RunWorkedLines(turns)`（`pkg/turn/compaction.go:180-222`）：

```go
type RunWorkedLine struct {
	RunID      string
	StartedAt  time.Time
	FinishedAt time.Time
	Worked     time.Duration
}
```

  - TUI 回放：`pkg/tui/commands.go:438-442` `replayWorkedFrame(line)` → `workedForLabel(line.Worked) + " · " + workedCompletionTime(...)`。
  - web：`pkg/gateway/api_extra.go:1373-1386` 把它变成 `role: "worked"` 的行（`RunStartedAt`/`RunFinishedAt`/`WorkedMs`/`PlanDone`…，
    结构体字段定义在 `:1260-1283`，json 名如 `worked_duration_ms`）。

### Worked 行与运行中状态行的拼法

- TUI live：`pkg/tui/reducer.go:1729-1738` `formatWorkedStatusTitle`：`[workedForLabel(d), 计数, 完成时刻]` 用 `" · "` 连接。
  subagent 的在 `:2015-2019`（**不改**，首 token 是主对话一轮的）。TUI 的 "Working" 运行中状态行在 `reducer.go` 约 `:6433-6760`
  （`grep -n '"Working' pkg/tui/reducer.go` 定位拼字符串的函数）。
- web：`frontend/src/composables/useChatStream.ts:1563-1584` `workedLineOf` / `workedLineText`，`formatWorkedDurationLabel`（`:1530`）；
  运行中：`formatRuntimeStatusLabel`（`:1871-1892`）；live 结束时 `runEndPatch`（`:1589`）。`ChatMessage` 上的 `workedDurationMs` 等字段由
  重载（`worked` 行）和 live（`turn_completed` / `run_completed`）两条路写入。

### 测量点（两端共用）

`pkg/turn/submit.go:100-160` `Service.Submit`：`startedAt := time.Now()`，`announce(id)` 拿到 run id，然后 `s.runner.Run(ctx, req, announce)`。
两端的每一轮都经过这里（TUI：`chat_session.go:1896`、`chat_turn.go:897`；gateway：`server.go:1904`、`run_control.go:766` 等）。
调用前，surface 已把流式 sink 放进 ctx（TUI `chat_turn.go:446`；gateway `run_control.go:415-434`）。`llm.StreamSink.OnResponseStarted`
在每次模型响应"第一个产出内容的事件"之前触发（`pkg/llm/stream.go:14-19`、`NotifyResponseStarted`）；内部嵌套调用（压缩等）经
`WithMutedForegroundStream`（`stream.go:82-102`）把它置 nil，所以只有前台回答会触发——正是"本轮第一次输出"。

`turn.Service` 的依赖由 option 注入（`pkg/turn/service.go:37-75`），构造点：TUI `pkg/tui/notify.go:1319`（以及 `:1314` 的简化构造），
gateway `pkg/gateway/serve_run.go:61-75`（已有 `turn.WithChildRunStore(runSvc)`）。

### 迁移框架

`pkg/state/schema_migrations.go`：`const stateSchemaVersion = 8`（`:18`）；迁移登记表 `:39-46`；加列样板：`migrateStateV4ToV5`（`:69-115`，
先查 `pragma_table_info` 再 `ALTER TABLE fb_runs ADD COLUMN ...`，对已存在容忍）。`schema.sql` 必须同步加这一列（新库与升级库一致）。

### 约定

- Go 注释写"为什么"、英文；前端文案进 `frontend/src/locales/index.ts` 的中英两份。
- 时长格式：TUI 用 `formatElapsedDuration`（`reducer.go` 内），web 用 `formatRuntimeDuration`/与 Worked 行同风格；首 token 显示到 0.1 秒
  （如 `1.2s`；≥ 60s 时用与耗时相同的分秒格式）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/turn/... ./pkg/gateway/... ./pkg/tui/... ./pkg/architecture/... -count=1` | 全部 `ok`（`TestCharacterizationNetworkApproval` 偶发网络超时，单跑通过即可） |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui`（仓库约定：清空 dist 后构建） | 成功 |

## Scope

**In scope**：
- `pkg/state/schema.sql`、`pkg/state/schema_migrations.go`、`pkg/state/session_store.go`（联表与 `StampRunFirstToken`）、`pkg/state/*_test.go`
- `pkg/turn/submit.go`、`pkg/turn/service.go`、`pkg/turn/compaction.go`（`RunWorkedLine` 加字段）、`pkg/turn/*_test.go`
- `pkg/tui/notify.go`（构造 option；新 UI 消息类型）、`pkg/tui/reducer.go`（Working 行与 Worked 行）、`pkg/tui/commands.go`（回放 Worked 行）、`pkg/tui/*_test.go`
- `pkg/gateway/serve_run.go`（option）、`pkg/gateway/api_extra.go`（`worked` 行字段）、`pkg/gateway/server.go`（live 下发，见 Step 6）、`pkg/gateway/*_test.go`
- `frontend/src/composables/useChatStream.ts`、`frontend/src/locales/index.ts`、`frontend/src/composables/useChatStream.test.ts`、`pkg/gateway/dist`（`make ui`）

**Out of scope**：assistant 行的 `usage_json`（owner 明令不写）；subagent 的 Worked 行；把首 token 延迟写进任何持久化事件（"只存一次"）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：表结构与迁移

1. `pkg/state/schema.sql` 的 `fb_runs`：在 `owner TEXT NOT NULL DEFAULT '',` 之后、`CHECK (...)` 之前加（与 `ALTER TABLE ADD COLUMN`
   追加在表尾的顺序一致，新库与升级库列顺序相同——`owner` 就是这样放的）：

```sql
  -- How long the turn waited for the model's first output, measured once by
  -- the surface that ran it; NULL for a run that never produced output or
  -- was stored before turns kept this.
  first_token_ms INTEGER,
```

   `CHECK` 约束不变：`first_token_ms` 不参与那组"三者同空"约束。
   `pkg/state/schema_migrations_test.go` 若有"新库与升级库结构一致"的对照测试，它会验证这一点。
2. `pkg/state/schema_migrations.go`：`stateSchemaVersion` 改为 9；登记 `{version: 9, apply: migrateStateV8ToV9}`；新函数照 `migrateStateV4ToV5`
   的写法：`pragma_table_info('fb_runs')` 查 `first_token_ms`，不存在才 `ALTER TABLE fb_runs ADD COLUMN first_token_ms INTEGER`。旧 run 为 NULL。
   doc 注释说明：首 token 延迟一轮一值，旧 run 没有测过，留空而不是猜。
3. `pkg/state/session_store.go` 加：

```go
// StampRunFirstToken records how long the run waited for the model's first
// output. It is written once: a continuation past an approval is the same
// run and keeps the wait its first answer had.
func (s *SessionStore) StampRunFirstToken(ctx context.Context, runID string, wait time.Duration) error
```

   SQL：`UPDATE fb_runs SET first_token_ms=?, updated_at=? WHERE id=? AND first_token_ms IS NULL`。（若 run 计时方法实际在 `RunStore` 而不在
   `SessionStore`，放到 `StampRunTiming` 所在的同一类型上，并在报告写明。）
4. 联表：`messageColumnsJoined` 末尾加 `r.first_token_ms`，扫描处加 `runFirstToken sql.NullInt64`，`:618`/`:1033` 两个结构体与 `state.Message`
   加 `RunFirstTokenMs int64`，`:1081` 拷贝。凡是用到 `messageColumnsJoined` 的 Scan 都要同步（`grep -n 'messageColumnsJoined' pkg/state/*.go`）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` → `ok`；新增迁移测试（见 Test plan 1）PASS。

### Step 2：引擎测量（两端共用）

1. `pkg/turn/service.go` 加接口与 option：

```go
// FirstTokenStore records the one first-token wait each run keeps.
type FirstTokenStore interface {
	StampRunFirstToken(ctx context.Context, runID string, wait time.Duration) error
}

func WithFirstTokenStore(store FirstTokenStore) Option { ... s.firstTokens = store ... }
```

2. `pkg/turn/submit.go` 的 `Submit`：在 `result, err := s.runner.Run(ctx, req, announce)` 之前，若 `s.firstTokens != nil` 且 ctx 里有流式 sink，
   用一个包装 sink 替换 ctx 里的 sink（复制结构体，链式调用原 `OnResponseStarted`），首次触发时：

```go
	// The turn's first output: the moment the model began answering it, once
	// per run. A nested call (compaction, a goal check) is muted and never
	// fires this, so the wait is the user's, not the plumbing's.
	wait := time.Since(startedAt)
	if id := strings.TrimSpace(runID); id != "" {
		_ = s.firstTokens.StampRunFirstToken(context.WithoutCancel(ctx), id, wait)
	}
	if req.OnFirstToken != nil { req.OnFirstToken(id, wait) }
```

   用 `sync.Once` 保证只触发一次；`runID` 读的是 `announce` 已设置的那个变量（`OnResponseStarted` 一定晚于 `announce`；若 `runID` 为空则跳过写库）。
   `TurnRequest` 加可选回调字段 `OnFirstToken func(runID string, wait time.Duration)`，让 surface 拿到测量值做 live 显示（Step 4、Step 6）。
   **不要**在没有流式 sink 的调用上伪造测量（无 sink → 不测）。
3. 构造点接线：TUI `pkg/tui/notify.go:1319` 的 `turn.New(...)` 加 `turn.WithFirstTokenStore(<state 的 run 计时存储>)`；gateway `serve_run.go:61` 加
   `turn.WithFirstTokenStore(...)`。（两端各自拿得到 `SessionStore`/`RunStore`，以 Step 1 方法所在类型为准。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... -count=1` → `ok`（含 Test plan 2）。

### Step 3：回放读数（两端）

1. `pkg/turn/compaction.go`：`RunWorkedLine` 加 `FirstToken time.Duration`，`RunWorkedLines` 从 `row.RunFirstTokenMs` 填（0 表示未知）。
2. TUI 回放：`replayWorkedFrame` 在耗时后插入首 token 段（未知时不插）：`Worked for 12s · first token 1.2s · 14:05`。
   段文字抽成一个函数 `firstTokenLabel(d time.Duration) string`（放 `reducer.go` 里 `workedForLabel` 旁边），live 与回放共用。
3. gateway：`worked` 行结构体加 `FirstTokenMs int64 \`json:"first_token_ms,omitempty"\``，`:1373-1386` 赋值 `line.FirstToken.Milliseconds()`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/... ./pkg/tui/... ./pkg/gateway/... -count=1` → `ok`。

### Step 4：TUI live

1. 新 UI 消息 `FirstTokenMsg{RunID string; Wait time.Duration}`（`pkg/tui/notify.go` 里与 `RunStartedMsg` 同处定义），在 TUI 的两处 `Submit` 调用里通过
   `TurnRequest.OnFirstToken` 发出（`s.notifyUI(FirstTokenMsg{...})`）。
2. reducer 收到后记在当前 run 上（与 `activeRunID` 同处保存）；"Working" 运行中状态行在已知时追加 `· first token 1.2s`；`formatWorkedStatusTitle`
   在耗时之后插入同一段。run 结束时清掉。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1` → `ok`（含 Test plan 4）。

### Step 5：web 重载

1. `ChatMessage` 加 `firstTokenMs?: number`；重载时从 `worked` 行（`firstTokenMs`，经 `toCamelCase`）写入到它所属的 assistant 消息（与 `workedDurationMs` 同处赋值——
   `grep -n "workedDurationMs" frontend/src/composables/useChatStream.ts` 找到重载赋值点）。
2. `WorkedLine` 加 `firstToken?: string`；`workedLineOf` 把 `firstTokenMs` 格式化后放进去；`workedLineText` 把它放在 `label` 之后。
   文案：`chat.firstToken`：中文 `首 token {duration}`，英文 `first token {duration}`。

**Verify**：`cd frontend && npx vitest run src/composables/useChatStream.test.ts` → 全绿（含 Test plan 5）。

### Step 6：web live（推送、不落库）

1. gateway 两条跑 turn 的路径（socket：`server.go:1904` 附近的 `Submit`；detached/续跑：`run_control.go:766` 等，`grep -n 'Core.Submit(' pkg/gateway/*.go` 全列出）
   传 `OnFirstToken`，在其中向**本次请求的 socket**发一条 legacy op（不经 `RunEvents().Publish`，所以**不落库**）：
   `writeMsg(wsServerMsg{Op: "first_token", RequestID: ..., RunID: runID, SessionID: sid, Data: map[string]any{"first_token_ms": wait.Milliseconds()}})`。
   detached 路径没有 socket 的，跳过 live（刷新后从库里读）。**确认 `writeMsg` 的 `canonicalRunEventsFromWS` 对 `first_token` op 返回空**
   （`wsevents.go` 的 switch 没有这个 op → 不生成规范事件、不落库）；`ws_protocol.go` 若有 op 白名单，把 `first_token` 加进出站列表。
2. 前端 `handleOp`（`useChatStream.ts` 里处理 `run_started`/`run_completed` 的那个函数）加 `op === 'first_token'`：记到当前 assistant 消息的
   `firstTokenMs`，并让运行中状态行（`runtimeStatus` 加 `firstTokenMs`，`formatRuntimeStatusLabel` 在已知时追加首 token 段）显示。
   `run_completed` 时 Worked 行自然带上（Step 5 的 `workedLineOf`）。

**Verify**：前端测试全绿（含 Test plan 6）；`make ui` 成功。

### Step 7：真机

TUI：`export FOREBRAIN_RUN_WORK=$TMPDIR/fb-015 FOREBRAIN_RUN_SESSION=fb-015 FOREBRAIN_RUN_PORT=8818; D=.claude/skills/run-forebrain/driver.sh; $D build && $D reset;
$D start drip; $D submit 'hello'; sleep 1; $D screen`（`drip` 模式逐段慢速输出，运行中能看到状态行）→ 状态行出现 `first token`；`sleep 6; $D screen` → Worked 行含
`first token`；`$D db "select first_token_ms from fb_runs order by created_at desc limit 1"` 为正整数；回放（`RESUME_ID=$($D sid) $D start reply`）Worked 行含同一个值。
web：同一 home 起 gateway（写法照 plan 014 Step 6 / 008 验收记录），`curl .../messages` 的 `worked` 行含 `first_token_ms`。收尾删除临时目录、停掉进程。
（若 `drip` 模式名不同，用 `.claude/skills/run-forebrain/SKILL.md` 里能产生慢速流式的模式，报告写明。）

## Test plan

1. `pkg/state`：迁移测试——v8 库（照既有迁移测试的造库方式，`grep -n 'func TestMigrateStateV' pkg/state/*_test.go` 找样板）升级后 `fb_runs` 有 `first_token_ms` 列、旧行为 NULL；
   `StampRunFirstToken` 只写一次（第二次调用不覆盖）；transcript 联表读出 `RunFirstTokenMs`。
2. `pkg/turn`：`TestSubmitStampsTheFirstTokenOncePerRun`——假 runner 在 `Run` 里调用两次 ctx 里 sink 的 `OnResponseStarted`，store 只收到一次、runID 正确；
   无 sink 时不调用 store。
3. `pkg/turn`：`RunWorkedLines` 带出 `FirstToken`。
4. `pkg/tui`：回放 Worked 行含首 token 段（未知时不含）；reducer 收到 `FirstTokenMsg` 后 Working 行与 Worked 行含该段。
5. 前端：重载的 `worked` 行带 `firstTokenMs` → `workedLineText` 为 `Worked for 12s · first token 1.2s · 14:05` 这样的顺序（英文 locale）。
6. 前端：live 收到 `first_token` op 后运行中状态行含首 token 段；`run_completed` 后 Worked 行含它。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] Test plan 6 组测试存在且 PASS
- [ ] `grep -n 'first_token_ms' pkg/state/schema.sql` 有 1 行；`grep -n 'usage_json.*first_token\|first_token.*usage' -r pkg frontend/src` 无输出（没有写进 usage_json）
- [ ] 持久化事件里没有首 token：`grep -rn 'first_token' pkg/event/` 无输出
- [ ] Step 7 断言满足，输出摘要写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符，或 `stateSchemaVersion` 与预期不同且无法确定下一个版本号。
2. `OnResponseStarted` 在前台回答之前就被别的调用触发（Test plan 2 的假设不成立），或 `announce` 晚于首次 `OnResponseStarted`。
3. `writeMsg` 会把 `first_token` op 落库（`canonicalRunEventsFromWS` 生成了事件）。
4. 需要改 assistant 行 `usage_json` 或任何持久化事件才能完成。

## Maintenance notes

- 首 token 延迟只在 `fb_runs.first_token_ms` 存一份；live 显示走不落库的推送。不要为了"重连后也能看到"把它塞进事件。
- 口径从 `Submit` 的 `startedAt` 起算，与 Worked 行的起点（surface 自己的 run 计时）可能相差几毫秒，属预期。
- 新增 surface（如 channel/cron）只要经过 `Submit` 并带流式 sink，就会自动测量。
