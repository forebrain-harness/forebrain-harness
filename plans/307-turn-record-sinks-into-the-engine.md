# Plan 307: turn 记录下沉到 `pkg/turn`，TUI 与 gateway 共用一个写入点

> **Executor instructions**：按步骤执行，每步跑完验证命令并对照预期再往下。遇到「STOP 条件」任一条就停下报告，不要自行发挥。完成后不要改 `plans/`（索引由派发方维护）。
>
> **Drift check（开工第一步）**：
> ```sh
> git rev-parse --short HEAD                      # 基线 09b44fe3（dev）；后代提交亦可，按内容核对
> grep -n 'RunEventTurnStarted\|RunEventTurnCompleted' pkg/turn/submit.go
> grep -rn 'telemetry.Emit(.*"turn"' pkg/tui --include='*.go' | wc -l   # 期望 9
> grep -n 'publishGatewayRunEvent(ctx, sid, runID, event.RunEventTurnStarted' pkg/gateway/run_control.go
> grep -n 'type Origin struct' -A 4 pkg/turn/model.go
> grep -rn 'pkg/telemetry' pkg/turn/*.go | head -3                   # 期望空（本计划新增这条边）
> ```
> 内容对不上即 STOP；行号漂移不算。

## Status

- **Priority**: P1（owner 2026-11-12：「写 turn 记录的逻辑是否可以下沉到底层引擎，tui 端和 gateway 共享，避免重复造轮子」）
- **Effort**: S–M
- **Risk**: MED（动的是每一轮都走的 `Submit` 路径，两个 surface 共享）
- **Depends on**: `285`（事件日志与 `telemetry.Emit`）、`288`（发现网页会话没有轮次行）
- **Category**: refactor + bug fix（网页轮次在日志页缺失 + 一个行为两处实现）
- **Planned at**: commit `09b44fe3`（dev），2026-11-12

## Why this matters

同一个「一轮开始了 / 结束了」的事实今天有两套实现：

- **引擎已经有了**：`pkg/turn/submit.go` 在 runner 落出 run id 时发 `turn_started`（`:169`），在四条出口发 `turn_completed` / `turn_cancelled` / `turn_error` / `approval_requested`（`:259` 等），`TurnRequest.Origin` 带 `Surface`（tui/web/…）与 `ChannelID`，`RawInput`/`UserText` 也在。
- **TUI 又写了一遍**：`pkg/tui` 里 9 处 `telemetry.Emit(ctx, level, "turn", …)`（started / completed / cancelled ×2 / failed ×2 / awaiting approval / resumed）。而 **gateway 一处都没写**——所以网页会话在日志页没有轮次行（288 的真机发现），「轮次」菜单筛不出东西。

两套实现已经出现分叉成本：字段（channel / input_bytes / preview）只有 TUI 有，网页没有；改一处文案要记得改另一处。把记录写在 `Submit` 里，两个 surface 自动拿到同一形状，TUI 的重复调用删掉。

## Current state（执行者需要的事实）

- `pkg/turn/submit.go`
  - `:154` `startedAt := time.Now()`；`:158-180` `announce(id)`：**第一次拿到 run id 时**设 `runID` 并 `s.publish(ctx, sink, event.RunEvent{RunID, SessionID, Type: RunEventTurnStarted})`；注释写明「发布在 executor 回调里，才带得到真正落库的 run id」。
  - `:232-242` `TurnWaitingApproval` 分支 → 发 `RunEventApprovalReq`，`out.Approval` / `out.Resume` 带审批信息（action id 在 `waiting.Request`）。
  - `:243-248` `context.Canceled/DeadlineExceeded` → `TurnCancelled` + `RunEventTurnCancelled`。
  - `:249-258` 其它错误 → `TurnFailed` + `RunEventTurnError`。
  - `:259-264` 成功 → `TurnCompleted` + `RunEventTurnCompleted`。
- `pkg/turn/model.go:25-29` `Origin{Surface, ChannelID, RequestID}`；`:39-` `TurnRequest` 带 `SessionID`、`Origin`、`RawInput`、`UserText`、`ExistingRunID`。
- `pkg/turn` **目前不 import `pkg/telemetry`**（layer 3 → 1，架构测试允许；不要新增别的跨层边）。fan-out 有富余：`fanOutBudgets["turn"] = {current: 12, target: 12}`（`pkg/architecture/cache_test.go:218`，断言为 `<=`），实测顶层依赖 ≤10，新增 `telemetry` 后 ≤11，`TestFanOutOnlyShrinks` 仍绿——**不要**为此动预算表。
- `pkg/telemetry.Emit(ctx, level, kind, msg, attrs…)`：身份（project/session/worker/run/agent）默认从 ctx 的 Scope 取，**显式同名属性优先**；`Msg`/`Err`/字符串属性写盘前过 `RedactLogLine` 与 `BoundText`。分层评审要求：turn 只依赖 telemetry 的导出函数，不得把 `event`/`turn` 的类型塞进 telemetry。
- TUI 的 9 处（逐条处置见 Step 3）：
  | 文件:行 | 消息 | 引擎是否已知道 |
  |---|---|---|
  | `chat_session.go:2301` | turn started（session/channel/input_bytes/preview） | **是**（announce） |
  | `chat_session.go:2518` | turn awaiting approval（action_id） | **是**（`TurnWaitingApproval`） |
  | `chat_session.go:2543` | turn cancelled（elapsed） | **是** |
  | `chat_session.go:2567` | turn failed（reason=skill_load） | **否**：技能装载失败发生在 `Submit` 之前 |
  | `chat_session.go:2575` | turn failed（err/elapsed） | **是**（`TurnFailed`） |
  | `chat_turn.go:641` | turn completed（assistant_bytes/preview/elapsed） | **是**（`TurnCompleted`，`out.Result` 即答案） |
  | `chat_turn.go:957` | turn resumed（label/action_id） | **否**：TUI「重新挂上一条在跑的 run」的界面状态 |
  | `chat_turn.go:1063` | turn cancelled | **是** |
  | `chat_turn.go:1088` | turn failed | **是** |
- gateway 的**事件**（不是记录）另有两处自己发布：`pkg/gateway/run_control.go:1527`（detached turn 在 `Submit` 之前就发 `RunEventTurnStarted`，让网页立刻看到 run）与 `api_extra.go:5221`（结束侧）。但全仓**所有** `Core.Submit` 调用点的 sink 参数都是 `nil`（gateway `server.go:2699→:2725`、`api_extra.go:5157`、`approval.go:608`、`channels.go:561`、`run_control.go:1545`；TUI `chat_session.go:2462`、`chat_turn.go:1011`），而 `Submit` 里 `s.publish` 对 nil sink 是 no-op——引擎发的 turn 事件今天**落不了库**，网页的 `turn_started` 事件只来自 gateway 自己那条，每 run 应为 **1 条**。Step 0 实测量出几条写几条；只有量出 2 条才做 Step 4 的收口。TUI 的幂等做法是**从 run 派生的确定性 id**：`"turn-started:"+runID` 在 `chat_session.go:2443`、`chat_turn.go:1000`，结束侧对应 `"turn-completed:"+runID` 在 `pkg/tui/turn_lifecycle.go:31`。

## Scope

**In scope**：`pkg/turn/submit.go`（新增记录，不改 `Submit` 签名/返回值）、`pkg/turn` 的测试；`pkg/tui/{chat_session.go,chat_turn.go}`（删重复调用、保留两处引擎不知道的）；`pkg/gateway/{run_control.go,api_extra.go}`（仅「事件幂等」这一项，若 Step 4 判定确实重复）；`pkg/architecture`（新增一条语言门禁测试）。
**Out of scope**：`telemetry` 的导出面、事件类型定义（`pkg/event`）、`Submit` 的返回结构、网页/TUI 的其它 `Emit` 调用（其它 kind）、`turn resumed` 的跨 surface 语义（另议）。

## Steps

### Step 0：先把缺口与重复量清楚（只读，不改代码）

用隔离 home + fake provider 跑一轮 **网页**对话（沿用 287/288 的 web_e2e 环境或直接起 gateway），然后：
```sh
jq -r 'select(.kind=="turn")|.msg' "$H"/logs/*/*.jsonl          # 期望：0 行（这就是缺口）
sqlite3 "$H/forebrain.state.sqlite" \
  "select event_type,count(*) from fb_session_events where run_id='<该轮 run id>' and event_type like 'turn_%' group by 1;"
```
把「网页一轮的 `turn_started` 是 1 条还是 2 条」写进报告——Step 4 按这个结论决定是否收口。

**Verify**：报告的缺口证据（0 条记录）+ 事件条数。

### Step 1：引擎写 started（唯一写入点）

`pkg/turn/submit.go` 的 `announce` 里，**紧接** `s.publish(... RunEventTurnStarted)` 之后：
```go
telemetry.Emit(ctx, slog.LevelInfo, "turn", "turn started",
    slog.String("session", req.SessionID), slog.String("run", runID),
    slog.String("channel", string(req.Origin.Surface)),
    slog.Int("input_bytes", len(req.RawInput)),
    slog.String("preview", llm.TruncateBytes(req.RawInput, 512, "...")))
```
- 显式带 `session`/`run`：`announce` 的 ctx 不保证带 Scope（TUI 的 ctx 有、gateway 的 detached 路径没有），显式属性优先于 Scope，两个 surface 一致。
- `input_bytes` 用 `req.RawInput`（用户原话，TUI 今天用的就是它）；空值时不报错，记 0。
- 一轮**只准一条**：`announce` 本身有 `announced` 幂等，天然成立。
- `runner.Run` 返回后还有一次保底 `announce("")`（`submit.go:216-217`：executor 早失败、没建出 run 也要让事件流完整）：这条路径的记录照样发，`runID` 为空时**省略 `run` 属性**（其余属性照带）。与今天 TUI 的差别是良性的：TUI 在 `Submit` 之前就发（`chat_session.go:2301`），被 busy/待审批拒掉、从未起跑的轮今天也留一条 started；下沉后这些轮不再有 started 记录——「有 run 才有开始」，验收时**不要当成回归**。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'TestSubmit' -count=1` → ok；新增用例见 Test plan 1。

### Step 2：引擎写四条出口

在 `:232`/`:243`/`:249`/`:259` 四个分支里、各自 `publish` 之后发对应记录（消息与级别与今天的 TUI 对齐）：
- `TurnWaitingApproval` → INFO `"turn awaiting approval"`，attrs `run`、`action_id`（`waiting.Request.ActionID`——`*ToolApprovalRequest` 的字段，`pkg/turn/model.go:560`）。
- `TurnCancelled` → INFO `"turn cancelled"`，attrs `run`、`elapsed`（`time.Since(startedAt).String()`）。
- `TurnFailed` → **ERROR** `"turn failed"`，attrs `run`、`elapsed`、`err`（`errors` 走 `Emit` 的 err 通道，写盘前已脱敏）。
- `TurnCompleted` → INFO `"turn completed"`，attrs `run`、`elapsed`、`assistant_bytes`、`preview`。正文取法（执行者注意）：`out.Result` 是 `*agent.Result`（`pkg/turn/model.go:102`），**没有**整段字符串字段——把 `result.Parts` 里 `Type == llm.ContentTypeText` 的 `Text` 拼起来即正文；仓内无现成 helper，就在 `pkg/turn` 内写这个小函数（`pkg/turn` 已 import `agent` 与 `llm`，合法，不算新增依赖边）。`result == nil` 或拼出空串时**省略** `assistant_bytes`/`preview` 两属性，不写 0。
- 每条都带 `session` 与 `channel`（同 Step 1 的理由）。
- **不要**在 `Submit` 里再发一遍 started；不要在错误路径重复发（每条出口只有一条记录）。

**Verify**：新用例（Test plan 1）覆盖四条出口各一条记录、级别正确、`err` 不含密钥（用 `Bearer` 夹具验证 `RedactLogLine` 仍生效）。

### Step 3：删掉 TUI 的重复调用

按 Current state 的表逐条处置：
- **删**：`chat_session.go:2301`、`:2518`、`:2543`、`:2575`；`chat_turn.go:641`、`:1063`、`:1088`。
- **留并加一行注释说明为什么留**：`chat_session.go:2567`（`reason=skill_load`，发生在 `Submit` 之前，引擎没有这一轮）与 `chat_turn.go:957`（`turn resumed`，TUI 的「重新挂上在跑的 run」状态，引擎无对应事实）。
- TUI 里 `telemetry` 还有其它 kind 的调用，**不要**删 import。
- 删完 `grep -rn 'telemetry.Emit(.*"turn"' pkg/tui --include='*.go'` 必须只剩 2 处（上面保留的两条），且两条都带「为什么」的注释。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok（83s 量级）；`grep` 计数 2。

### Step 4：gateway 的事件幂等（按 Step 0 的证据决定）

- 若 Step 0 显示网页一轮存了 **2 条** `turn_started`：把 gateway 提前发布的那条改成与 TUI 同一条确定性 id（`"turn-started:"+runID` 的等价物），或在 `publishGatewayRunEvent` 落库前按 `(run_id, event_type)` 去重——**二选一，选改动更小的那个**；不得新增第三个发布点，也不得为了去重把 `Submit` 改成不发。
- 若只有 **1 条**：本步不做，报告写「不适用 + 证据」（按 Current state 的 sink 事实，这是**预期结果**——gateway 自己的提前发布是唯一落库的一条）。
- 记录本身（telemetry）只由 Step 1/2 的引擎写，gateway 一行 `Emit` 都不加。

**Verify**：同一轮网页会话 `select event_type,count(*) … like 'turn_%'` 每条为 1；网页实时流仍能看到 `turn_started`（e2e 或手点一次对话不回归）。

### Step 5：语言门禁（防止再长出一套）

`pkg/architecture` 加 `TestOnlyTheEngineWritesTurnRecords`（照 `TestEngineLogsCarryContext` 的 AST 写法）：扫 `pkg` 下所有非测试 `.go`，`telemetry.Emit(…, "turn", …)` 的调用点只允许出现在 `pkg/turn/`，以及白名单里那两条带注释的 TUI 例外（按 `file:消息` 精确匹配，不是按文件整片放行）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok；故意在 `pkg/gateway` 加一条 `Emit(..., "turn", ...)` 时该测试必须红（把这次红/绿写进报告）。

### Step 6：真机验收（fake provider，隔离 home）

1. TUI 一轮 + 网页一轮（不同会话），然后
   ```sh
   jq -r 'select(.kind=="turn")|[.msg,.session,.attrs.channel,.attrs.elapsed//"-"]|@tsv' "$H"/logs/*/*.jsonl
   ```
   期望：每轮**恰好** `turn started` + `turn completed`（失败/取消/待审批另测），`channel` 分别为 `tui` / `webchat`（`Surface` 常量值，`pkg/turn/model.go:142-145`），网页那两行存在（今天为 0）。
2. 取消一轮、失败一轮、待审批一轮：各一条，级别分别是 INFO / ERROR / INFO。
3. `grep -c '"turn"'` 全仓生产代码只剩 `pkg/turn/submit.go` 一处（外加白名单两条 TUI）。

**Verify**：上述 `jq`/`grep` 的真实输出进报告。

## Test plan（必须新增）

1. `pkg/turn`：`TestSubmitWritesTurnRecordsForEveryOutcome` —— 表驱动五个场景（complete / cancel / fail / waiting-approval / started-only-when-executor-fails），断言**每条出口恰好一条**记录、`msg`/`level`/`channel` 正确、`err` 脱敏；照 `pkg/telemetry/eventlog_test.go` 的手法（临时 home + `OpenEventLog` 读出 jsonl）。
2. `pkg/turn`：`TestTurnRecordIdentityComesFromTheRequestNotTheScope` —— ctx 不带 Scope 时记录仍带 `session`/`run`/`channel`（这正是 gateway detached 路径的形态）。
3. `pkg/architecture`：`TestOnlyTheEngineWritesTurnRecords`（Step 5）。
4. `pkg/tui`：把删掉调用后受影响的既有断言改到「引擎记录」上（若有），**不得放宽**；`TestChatSession*` 全绿。

## Done criteria

- [ ] 网页一轮在日志里恰好一条 `turn started` + 一条 `turn completed`，`channel` 为网页 surface（今天 0 条）
- [ ] TUI 一轮同样恰好两条，`channel` 为 `tui`；取消/失败/待审批各一条、级别正确
- [ ] `grep -rn 'telemetry.Emit(.*"turn"' pkg --include='*.go' | grep -v _test` 只剩 `pkg/turn/submit.go` + 白名单两条（带注释）
- [ ] `pkg/architecture` 的新门禁存在且会被违反触发（红/绿证据）
- [ ] Step 0 的 `turn_started` 事件重复（若存在）已收口为每 run 每条 1 次；网页实时流不回归
- [ ] `go build`/`vet`/`gofmt` 干净；`./pkg/turn ./pkg/tui ./pkg/gateway ./pkg/architecture` 全 ok

## STOP conditions

- `Submit` 的 ctx 或 `TurnRequest` 拿不到 `session`/`run`/`channel`/输入文本（需要改 `TurnRequest` 或 `Submit` 签名）→ 停下报告，不要改公共请求结构。
- 取 `assistant_bytes` 必须把 `agent.Result` 的内部结构暴露给 telemetry → 停下，改为省略该属性（写明理由）。
- 收口 gateway 事件重复会改变网页实时流的行为（run 出现时机变化）→ 停下，报告两种取舍，由 owner 定。
- 删除 TUI 某条调用后发现该状态引擎确实没有、而你又不能保留（例如会丢用户可见信息）→ 停下，逐条列出。
- 真机验收需要真模型或真凭据 → 停下（只用 fake provider）。

## 非功能要求

- 性能：每轮多 2 次 `Emit`（各一次 write(2)，微秒级）；`preview` 截断 512 字节，不复制整段输入。
- 资源：不新增 goroutine、不新增缓存；记录失败按 284 的 `dropped_before` 语义处理，不重试、不阻塞回合。
- 可靠性：记录失败不得改变 `Submit` 的返回（`Emit` 本身无返回）；取消路径同样要发（用 `context.WithoutCancel` 之外的普通 ctx 即可，`Emit` 不依赖 ctx 存活）。
- 维护性：一处写、两处注释（TUI 的两条例外必须写明为什么），门禁测试按调用点而不是按文件放行。
