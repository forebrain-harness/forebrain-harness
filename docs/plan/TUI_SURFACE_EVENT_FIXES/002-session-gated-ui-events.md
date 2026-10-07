# 计划 002：UI 事件按会话闸门——别的会话的事件只落库不上屏

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告，不要自行发挥。
> 完成后更新本目录 `README.md` 里本计划的状态行。**不要提交代码**。先读本目录 `README.md` 的"全局规则"。

> **漂移检查（先运行）**：本计划写于 2026-10-07，基线是当时的工作区（HEAD `936df40` + SUBAGENT_CONVERSATION 全套未提交改动）。摘录一律按函数名和注释原文核对；对不上就 STOP。

## 状态

- **优先级**：P1（owner 2026-10-07 指令："notifyUI 不过滤跨会话事件（既有行为，本轮未触碰）……必须定位根因并彻底修复"）
- **工作量**：M
- **风险**：MED（改的是事件→屏幕的总漏斗；闸门漏过或过紧都会造成"看不到该看的"）
- **依赖**：无（与 001 文件相交 `pkg/tui/reducer.go` 但函数不相交；串行执行即可）
- **类别**：bug（既有缺陷，早于 SUBAGENT_CONVERSATION 套件）
- **基线**：工作区（2026-10-07），HEAD `936df40`

## 为什么要做

TUI 的事件管道是"按会话落库、按进程上屏"：事件先正确持久化到它自己会话的日志，然后**无条件**画到当前屏幕上。两个确凿的跨会话来源会让用户在会话 X 里看到会话 Y 的幽灵：

1. **进程级回收器**（`AbandonedRunReaper`，`pkg/tui/notify.go:1336` 装配）：每个 TUI 进程回收**整个状态库**里所有被放弃的运行——另一终端死在会话 Y 半路的 subagent，由本终端的回收器经**本会话的** `publishTUIRunEvent` 漏斗发 `subagent_ended`，reducer 在 X 的屏幕上三次绑定查找全部落空，按"无派发调用"规则新建孤儿卡（`✗ general-purpose`，类型当标题、无时长）——正是 009 修掉的 T23 孤儿机制，落在不相关会话上。
2. **切会话后仍在跑的异步 subagent**：`switchStreamSession` 只清 UI 队列不清引擎侧运行；它后续的工具事件/文字增量继续经同一条无闸门管道到达此时正在显示的另一个会话。

owner 本机常开多个 forebrain 终端，任一终端被杀（Ctrl+C、崩溃、限额中断）留下 subagent 在跑，另一终端在租约到期后就会画出幽灵。数据无污染（事件归档正确、模型上下文隔离），纯显示层缺陷，但用户看到的是"我没派发过的失败任务突然出现，重开又没了"。

## 根因（已定位，2026-10-07 核实）

- `ChatSession.publishRunEvent`（`pkg/tui/notify.go:763`）先按 `evt.SessionID` 正确持久化（`AppendSessionEventOnce`，`:768`），然后**无条件**把事件转成 UI 消息（`:792` 起的大 switch：assistant/reasoning delta、tool step、`RunEventSubagentSpawned/Ended` → `SubagentSpawnedMsg/SubagentEndedMsg` 等）经 `notifyUI` 发给当前屏幕。
- `notifyUI`（`pkg/tui/chat_session.go:720`）没有任何会话概念；UI 消息只带 `RunID`/`AgentID`，reducer 的 `Reduce` 没有会话闸门。
- **"当前正在看哪个会话"的唯一权威在 UI 侧**：`streamState.sessionID`（`pkg/tui/run.go:1714` 是唯一的运行期赋值点，位于 `switchStreamSession` 内）。ChatSession 侧没有任何字段记录它——`PreferredSurfaceTranscriptSessionID`（`pkg/tui/chat_surface.go:198`）是"最新消息所在会话"的启发式，不是正在浏览的会话，**不得**用作闸门。
- `ChatSession` 实例在整个 TUI 进程生命周期里**跨会话切换复用**（`switchStreamSession` 只调它的方法、从不替换 `state.session`——已核实）。所以缺的就是这一个概念："这个表面正附着在哪个会话上"。
- 对照：gateway 侧没有此缺陷（事件按会话总线分发，`TestRunEventBusDeliversOnlyToTheBoundSession`，`pkg/gateway/wsevents_test.go:370`）。这是 TUI-only 的缺口。

## 设计

**原则：落库永远发生；上屏只发生在事件属于"正在看的会话"（或事件不带会话）。**

1. **ChatSession 记录附着会话。** `pkg/tui/chat_session.go` 增加私有字段（放 `uiNotify` 旁，同样由 `tuiMu` 保护）：

   ```go
   	// uiSessionID is the conversation this surface is currently looking at.
   	// Events the event funnel receives for any other session are persisted
   	// to their own log but never painted here: a process-wide reaper and
   	// async subagents of other conversations would otherwise draw ghosts on
   	// whichever screen happens to be open. Empty while no session is
    // attached — boot-time events still paint, as they always did.
   	uiSessionID string
   ```

   配套导出方法（不加锁的读由持 `tuiMu` 的路径使用）：

   ```go
   // SetViewingSession records the conversation this surface is attached to.
   // The event funnel consults it to keep other sessions' events off this
   // screen (they still land in their own session's log).
   func (s *ChatSession) SetViewingSession(sessionID string)
   ```

   注意：`Session` 接口（`pkg/tui/notify.go`）**不**加此方法——调用方（run.go）拿的是具体 `*ChatSession`（执行者核实 `streamState.session` 的静态类型；若是接口，则在接口加方法并同步所有测试替身，见 STOP 条件）。
2. **UI 侧在会话切换点写入。** `switchStreamSession`（`pkg/tui/run.go`）在 `state.sessionID = next`（`:1714`）之后紧跟 `state.session.SetViewingSession(next)`。启动时的初始附着：执行者 `grep -n "sessionID:" pkg/tui/run.go` 找 streamState 构造/首会话建立点（新会话与 `/resume` 都汇入 `switchStreamSession` 则只此一处；若 `/new` 或启动另有直接赋值路径，一并加）。
3. **闸门。** `pkg/tui/chat_session.go` 新增私有辅助：

   ```go
   // notifyUIForSession paints one event-funnel message when it belongs to the
   // conversation this surface is looking at. Empty sessionID (an event from
   // before any conversation started) and an unattached surface paint as they
   // always did; anything else for another session stays in that session's log
   // alone.
   func (s *ChatSession) notifyUIForSession(sessionID string, msg any) {
   	s.tuiMu.Lock()
   	viewing := s.uiSessionID
   	s.tuiMu.Unlock()
   	if viewing != "" && strings.TrimSpace(sessionID) != "" && strings.TrimSpace(sessionID) != viewing {
   		return
   	}
   	s.notifyUI(msg)
   }
   ```

   把 `publishRunEvent` 的 switch 里**所有** `s.notifyUI(...)` 调用（`pkg/tui/notify.go:792` 起，含 `SubagentSpawnedMsg`/`SubagentEndedMsg`/`PlanUpdatedMsg`/tool step/assistant/reasoning/usage 各分支）改为 `s.notifyUIForSession(evt.SessionID, ...)`。`publishTUIRunEvent`（`:1002`）经 `publishRunEvent`，自动被覆盖（回收器路径即此）。
   **不改** `publishRunEvent` 之外的约 40 处直接 `notifyUI` 调用——那些是本表面在附着会话上的自身动作（审批结果、队列变化等），不属于跨会话漏斗。
4. **一个已核实的边界**：subagent 事件的 `SessionID` 是其**父对话**的 id（与正在看的会话相同），不受闸门影响；MCP 启动失败事件按 `WatchMCPStartup(next)` 跟随当前会话，亦不受影响。

## 缓存影响

无。只改 UI 消息的分发条件；不改任何持久化（`AppendSessionEventOnce` 路径原样）与任何模型请求。

## 范围

**只改**：`pkg/tui/chat_session.go`、`pkg/tui/notify.go`、`pkg/tui/run.go`（仅 setter 调用点）、`pkg/tui/chat_session_test.go` / `pkg/tui/notify_test.go`（测试）。
**不要动**：`pkg/gateway/**`（其会话总线已正确）；`pkg/turn`/`pkg/run` 的事件发布；`publishRunEvent` 的持久化半段；直接 `notifyUI` 的非漏斗调用点；`pkg/gateway/dist`。

## 步骤

### 第 1 步：先写失败的测试

在 `pkg/tui/chat_session_test.go` 加 `TestForeignSessionEventsStayOffTheScreen`（照该文件里现有调用 `publishRunEvent` 的测试搭建——执行者 `grep -n "publishRunEvent(" pkg/tui/*_test.go` 找最近的一个当模板；需能看到 UI 消息的假 sink 用 `SetUINotify` 安装计数器）：

1. 构造 ChatSession（带 store 与 runSvc 的既有测试骨架），`SetViewingSession("sess-A")`。
2. 对 `sess-B` 发布一条 `subagent_ended`（走 `publishRunEvent` 公开入口或测试内等价路径）→ 断言：**store 里 sess-B 的日志有该事件**（持久化发生）且 **UI sink 计数为 0**（不上屏）。
3. 对 `sess-A` 发布同款 → sink 计数为 1。
4. `SetViewingSession("")` 后对 `sess-B` 发布 → sink 计数为 1（未附着时保持旧行为）。
5. 对空 SessionID 发布 → sink 计数为 1。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestForeignSessionEventsStayOffTheScreen -count=1` → **失败**（第 2 条：sink 计数为 1）。不失败就 STOP。

### 第 2 步：实现（设计第 1–3 条）

**验证**：第 1 步测试转绿；`grep -n "s.notifyUI(" pkg/tui/notify.go` 在 `publishRunEvent` 的 switch 内**无**残留（switch 外的调用点保持）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`。

### 第 3 步：会话切换接线与回归

按设计第 2 条接 `SetViewingSession`。若发现 `/new`、启动、`/resume` 之外还有 `state.sessionID` 赋值路径（`grep -n "state.sessionID = " pkg/tui/*.go` 须只有 `:1714` 一处运行期赋值），补齐并逐一列出。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（既有会话切换测试全部不改断言通过）；`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/tui -run 'Session|Switch' -count=1` → 无竞态。

### 第 4 步：全量

**验证**：`gofmt -l pkg cmd` 无输出；`go vet -tags fts5 ./...` 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全 `ok`；架构测试 `ok`；死代码与 `/tmp/deadcode-before.txt` 按符号名一致。

### 第 5 步：真机（真实模型，两个场景）

用 `scripts/acceptance/live_subagent.sh`（`LIVE_WORK=/tmp/fb002-fix`，真模型；密钥只经 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用，绝不打印）：

1. **切换场景**：会话 1 派发 `subagent_send` 长任务（慢读多个文件逐步总结），`/resume` 切到会话 2（或 `/new`），等 subagent 继续跑 30 秒以上——截图：会话 2 屏幕无该 subagent 的任何卡片/roster 行/增量；切回会话 1——它的卡片与输出都在（自己的日志）。
2. **回收器场景**：会话 1 派发长任务后杀掉 TUI（tmux kill-session），重启后**先开另一个会话**（`/resume` 选会话 2），等过 SRT-004 租约 + 一个回收周期（`RunOwnerLease` 值在 `pkg/turn`/`pkg/process` 里查）——截图：会话 2 屏幕无孤儿卡；再切回会话 1——该任务按 T23 语义收尾（`✗` + 盖章时长 + 回收原因行）。

**验证**：两个场景的截图与时间点附进报告。

## 完成标准

- [ ] 第 1–5 步验证全部通过；全量测试 `ok`
- [ ] `grep -n "s.notifyUI(" pkg/tui/notify.go` 的命中全部在 `publishRunEvent` 的 switch 之外
- [ ] 两个真机场景的截图在报告里
- [ ] README 状态行已更新

## STOP 条件

- 第 1 步的测试没有失败。
- `streamState.session` 的静态类型是 `Session` 接口而非 `*ChatSession`，导致 `SetViewingSession` 必须进接口（此时：加进接口并同步全部测试替身，在报告里列出；若替身数量失控则 STOP 回来商量）。
- 发现某类**必须**跨会话上屏的漏斗事件（修完第 2 步后有既有测试证明某场景依赖跨会话上屏）——停下来带回证据。
- `state.sessionID` 存在 `:1714` 之外的运行期赋值路径且无法归一到一个 setter 点。

## 维护说明

- 以后任何"进程级、扫全库"的组件（回收器、未来的清理任务）发布事件都走 `publishTUIRunEvent`，闸门自动生效；新起一条直接 `notifyUI` 的漏斗时必须问一句它带不带会话，带就改走 `notifyUIForSession`。
- 评审时盯着一点：闸门只许拦"上屏"，不许拦"落库"——`AppendSessionEventOnce` 在闸门之前，顺序不可倒换。
