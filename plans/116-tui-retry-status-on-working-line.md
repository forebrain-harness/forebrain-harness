# Plan 116：TUI 把网络重试状态显示在 Working 行，删除重试卡与网络错误卡

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置**：`plans/115-network-retry-resend-and-backoff.md` 已完成（`llm.TransportRetry` 有 `Cleared`、没有 `Exhausted`；
> `pkg/tui` 里已有 `WithdrawDeadStreamBlocks`）。先跑：`grep -n "Cleared bool" pkg/llm/stream.go pkg/tui/notify.go` 应各有 1 行，
> `grep -n "func (r \*Renderer) WithdrawDeadStreamBlocks" pkg/tui/reducer.go` 应有 1 行；否则 STOP 1。
>
> **Drift check（先跑）**：`git diff --stat cb4146e -- pkg/tui/render.go pkg/tui/tty_session.go .claude/skills/run-forebrain/SKILL.md`
> 预期**无输出**（`reducer.go`、`run.go`、`notify.go`、`chat_turn.go`、`transport_retry_test.go` 已被 115 改过，按下面的摘录对照现场）。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改的是运行中状态行与对话视图的块管理）
- **Depends on**: plans/115-network-retry-resend-and-backoff.md
- **Category**: direction（owner 2026-10-24 定稿：重试状态只在 spinner 行，不进对话消息卡）
- **Planned at**: commit `cb4146e`，2026-10-24

## Why this matters

网络重试时 TUI 现在在对话里插入一张 `Network error` 错误卡和一张带转圈的重试卡。定稿的表现是：**对话里不出任何重试相关的卡**，
重试期间输入框上方的 Working 行整行换成重试状态，重试成功或回合结束后恢复。最终文案（英文字面量，TUI 无 i18n）：

```
⠋ Network error · Retrying in 8s · attempt 2/10
⠋ read tcp 10.0.0.2:59383->172.65.90.20:443: read: operation timed… · Retrying in 8s · attempt 3/10
```

- 开头是 Working 行原有的转圈字形（`transientSpinnerFrame(tick)`）；
- 第 1、2 次（准确地说：`attempt < min(3, total)`）原因写 `Network error`；从第 3 次起写错误原文，按显示宽度截到 60 列、末尾 `…`；错误原文为空时仍写 `Network error`；（2026-11-04 修订：错误原文含请求方法、URL、地址等底层细节，已取消；每次都只写通用原因，`API error` / `Network error`。）
- `Retrying in {x}`：`x` 是距下一次重试的剩余时间，向上取整到秒；`< 60s` 写 `Ns`（到点后显示 `0s`，直到请求有产出或再次失败）；
  `≥ 60s` 且 `< 5m` 写 `Xm Ys`；`≥ 5m` 只写最高位（`6m`、`2h`、`1d`）；
- `attempt n/total`：`n`、`total` 直接取事件里的 `Retry`、`Total`；
- 重试中不显示耗时、`esc to interrupt`、计划进度；Esc 仍然照常取消回合；
- 引擎发出 `Cleared` 后恢复为普通 Working 行；回合结束时状态行本来就会消失；
- 次数用完时没有任何额外的卡，回合以 `turn_error`（文案 `network error, gave up after N retries: …`，由 115 产生）结束；
- 子代理视图里的 Working 行显示**该子代理自己的**重试状态，对话视图只显示主代理的。

## Current state

（行号为 cb4146e；115 改过的文件以现场为准，按符号查找。）

**重试卡（全部删除）**——用 `git grep -n "FrameRetrying\|retryCardLine\|formatRetryCountdown\|liveRetryAgents\|liveRetryCardMatch\|dismissLiveRetryCardsLocked\|transportRetryStepIDFor\|transportRetryErrorStepIDFor\|transportRetryErrorText\|RetryDeadline\|RetryTotal" -- pkg/tui` 列出：

- `pkg/tui/reducer.go:29-51` — `transportRetryStepIDFor`、`transportRetryErrorStepIDFor`、`transportRetryErrorText`。
- `pkg/tui/reducer.go:91-96` — 常量 `FrameRetrying FrameKind = "retrying"`。
- `pkg/tui/reducer.go:172-178` — `Frame` 的 `Retry`、`RetryTotal`、`RetryDeadline` 字段。
- `pkg/tui/reducer.go` `case TransportRetryMsg:`（cb4146e `:1383-1420`）— 产出 `FrameError`（Title `Network error`）+ `FrameRetrying` 两帧。
- `pkg/tui/reducer.go:3074-3078` — `renderBlockFull` 的 `animated` 条件里的 `(b.frame.Kind == FrameRetrying && !b.frame.Final)`。
- `pkg/tui/reducer.go:3111-3160` — `retryCardLine`、`formatRetryCountdown`。
- `pkg/tui/reducer.go:3167-3172` — `renderFrameLinesWithAgents` 的 `FrameRetrying` 分支。
- `pkg/tui/reducer.go:3289-3295` — `statusMarker` 的 `FrameRetrying` 分支。
- `pkg/tui/reducer.go:4052-4053` — `hasLiveBlockLocked` 的 `FrameRetrying` case。
- `pkg/tui/reducer.go:5313-5350` — `liveRetryCardMatch`、`dismissLiveRetryCardsLocked`。
- `pkg/tui/reducer.go` `retainFrameLocked`（cb4146e `:5472-5560`）——开头新增的 dismissal `switch` 与 `if f.Kind == FrameRetrying {…liveRetryAgents…}`；
  无主错误分支里两处 `r.vm.replaceOrAppendBlock(f)`（原为 `r.vm.append(f)`）；三处 `|| f.Kind == FrameRetrying` 条件。
- `pkg/tui/tty_session.go:1157-1162` — `streamingFrameKind` 里的 `|| k == FrameRetrying` 及注释最后一句；`:1234` `replaceOrAppendBlock` 的 case 列表里的 `FrameRetrying`。
- `pkg/tui/render.go:310-314` — `Renderer.liveRetryAgents` 字段。
- `pkg/tui/transport_retry_test.go` — 卡片相关测试：`TestTransportRetryMsgClearsBuffersAndEmitsCardPair`、`TestRetryCardLineWordingAndCountdownTiers`、
  `TestRetryCardsReplaceInPlace`、`TestLiveRetryCardDismissal`、`TestSubagentRetryCardIsScopedToItsView`、`TestRetryCardAnimationGating`。

cb4146e 之前（`f18ff14`）的对应代码就是“没有重试卡”的样子，可用 `git diff f18ff14 cb4146e -- pkg/tui/reducer.go pkg/tui/tty_session.go pkg/tui/render.go` 对照；
**只还原上面列出的重试卡片段**，同一 diff 里还有与本计划无关的改动（例如 plan review 的 `Label`、子代理队列），不要动。

**Working 行**：

- `pkg/tui/run.go:4212-4253` — `workingStatusFormatter(counterSnapshot func() Counters, agentSnapshot func(string) (AgentRunSnapshot, bool))`：
  `view != ""` 时读 `agentSnapshot(view)`，未开始或已结束返回 `""`，否则 `transientSpinnerFrame(tick) + " Working (" + … + ")"`；
  对话视图读 `counterSnapshot()`，拼 `Working (… • esc to interrupt)` 与计划进度、首 token。
- `pkg/tui/run.go:3682` — `renderer.StartTimedTransientStatus(transientSourceWorking, workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun))`；
  `render.go:972-1010` 每 200ms 重新求值一次 formatter，所以倒计时不需要新定时器。
- `pkg/tui/reducer.go:182-200` — `Counters` 结构体（值类型，含 `FirstToken time.Duration` 等）。
- `pkg/tui/reducer.go` `Tracker`：`StartRun`（`:7641`）、`ObserveAgent(agentID, executionID string, at time.Time)`（`:7717`）、`Reset`（`:7612`）、
  `SnapshotAgentRun`（`:7810`）、`SnapshotActiveRun`（`:7971`）、`ObserveFirstToken`（`:7992`，“按 run 写一次”的写法可参照）。
- 截断工具：`tool.TruncateForDisplay(s, maxLen)`（`pkg/tool/display_header.go:785`，runewidth 截断加 `…`；`pkg/tui/tool_header.go:71` 有同名包装 `truncateForDisplay`）。

**消息**：`pkg/tui/notify.go` `TransportRetryMsg{turn, AgentID, Retry, Total, Wait, Err, Cleared}`（115 之后）；前台由 `chat_turn.go` 的 sink 发，子代理由事件路径 `notify.go` 发。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| TUI 测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"; CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/... -count=1` | `ok` |
| vet | 同上环境变量；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui/...` | 无输出 |
| gofmt | `gofmt -l pkg/tui` | 无输出 |
| 真机 | `D=.claude/skills/run-forebrain/driver.sh`（见 Step 5） | 见各条 |

## Scope

**In scope**：`pkg/tui/reducer.go`、`pkg/tui/run.go`、`pkg/tui/render.go`、`pkg/tui/tty_session.go`、`pkg/tui/notify.go`（仅注释）、
`pkg/tui/transport_retry_test.go`、`.claude/skills/run-forebrain/SKILL.md`（只加 `netdrop` 一行与验收说明）。

**Out of scope**：引擎与事件（`pkg/llm`、`pkg/run`、`pkg/event`、`pkg/gateway`）；`chat_turn.go`；web 前端（`117`）；
`WithdrawDeadStreamBlocks` 的行为（115 已定）；`FrameError` 在其他场景的渲染。

## Steps

### Step 1：Tracker 记录每个 agent 的重试状态

在 `reducer.go`：

1. 新类型（放在 `Counters` 旁）：

```go
// TransportRetryStatus is the network retry an agent is waiting on, read by
// the Working line. The zero value means none.
type TransportRetryStatus struct {
	Retry    int
	Total    int
	Deadline time.Time
	Err      string
}

// Active reports whether a retry is pending.
func (s TransportRetryStatus) Active() bool { return s.Total > 0 }
```

2. `Counters` 加字段 `Retry TransportRetryStatus`（英文注释：the pending network retry of the agent this snapshot belongs to; only live snapshots fill it）。
3. `Tracker` 加 `retries map[string]TransportRetryStatus`（key：`""` = 主代理，其余 = 子代理 roster key），`NewTracker` 与 `Reset` 里初始化。
4. 新方法：

```go
// ObserveTransportRetry records the retry an agent is about to make.
func (t *Tracker) ObserveTransportRetry(agentID string, retry, total int, wait time.Duration, errText string)
// ClearTransportRetry drops an agent's pending retry.
func (t *Tracker) ClearTransportRetry(agentID string)
```

   `ObserveTransportRetry` 写入 `Deadline: time.Now().Add(wait)`；两者都 `strings.TrimSpace(agentID)`、加写锁、nil 安全（照 `ObserveFirstToken`）。
5. `StartRun` 里清 `retries[""]`；`ObserveAgent` 里清 `retries[agentID]`（新一次执行不继承上一次的重试）。
6. `SnapshotActiveRun` 填 `c.Retry = t.retries[""]`；`SnapshotAgentRun` 返回的 `Counters` 填 `t.retries[agentID]`。`SnapshotRun`、`SnapshotSession` 不填。

**Verify**：`CGO_ENABLED=1 go build ./pkg/tui/` → exit 0

### Step 2：reducer 只更新 Tracker，不再产出帧

把 `case TransportRetryMsg:` 改为：

```go
	case TransportRetryMsg:
		if m.turn.isWithdrawn() {
			return EventResult{}
		}
		agentID := strings.TrimSpace(m.AgentID)
		if m.Cleared {
			if r.tracker != nil {
				r.tracker.ClearTransportRetry(agentID)
			}
			return EventResult{}
		}
		// The retried request re-streams from the first token: the dead
		// attempt's buffered text goes, and run.go withdraws its live blocks.
		if buf := r.agentBuf(agentID); buf != nil {
			buf.assistant.Reset()
			buf.reasoning.Reset()
		}
		if r.tracker != nil {
			errText := ""
			if m.Err != nil {
				errText = m.Err.Error()
			}
			r.tracker.ObserveTransportRetry(agentID, m.Retry, m.Total, m.Wait, errText)
		}
		return EventResult{}
```

同时更新 `notify.go` 里 `TransportRetryMsg` 的注释：不再提 “error/retry card pair”，改为“更新 Working 行的重试状态”。

**Verify**：`grep -n "FrameRetrying\|transportRetryErrorText" pkg/tui/reducer.go | grep -n "case TransportRetryMsg" ` → 无输出（该分支不再引用卡片符号）

### Step 3：Working 行渲染重试状态

在 `run.go` 新增（放在 `workingStatusFormatter` 后面，英文注释）：

```go
// transportRetryStatusLine renders the Working line while a network retry is
// pending: the reason, the countdown to the next attempt and the attempt
// counter. The first attempts name the failure generically; from the third on
// the transport's own sentence is shown, cut to fit.
func transportRetryStatusLine(tick int, s TransportRetryStatus, now time.Time) string {
	reason := "Network error"
	if s.Retry >= min(3, s.Total) && strings.TrimSpace(s.Err) != "" {
		reason = truncateForDisplay(strings.TrimSpace(s.Err), 60)
	}
	return transientSpinnerFrame(tick) + " " + reason + " · Retrying in " + formatRetryCountdown(s.Deadline.Sub(now)) +
		" · attempt " + strconv.Itoa(s.Retry) + "/" + strconv.Itoa(s.Total)
}

// formatRetryCountdown renders a remaining wait rounded up to the second:
// "8s" under a minute, "1m 5s" under five minutes, then only the largest unit.
func formatRetryCountdown(remaining time.Duration) string
```

`formatRetryCountdown` 规则：`secs := ceil(remaining/1s)`，负数按 0；`secs < 60` → `"%ds"`；`secs < 300` → `"%dm %ds"`（秒为 0 也写 `0s`，例：`2m 0s`）；
否则只写最高位：`≥ 86400` → `"%dd"`，`≥ 3600` → `"%dh"`，否则 `"%dm"`（向下取整）。
（Step 4 会删掉卡片版的同名函数，先删再加，避免重名。）

在 `workingStatusFormatter` 里：
- 子代理视图：`snap.Ended` 判断之后，`if snap.Counters.Retry.Active() { return transportRetryStatusLine(tick, snap.Counters.Retry, time.Now()) }`；
- 对话视图：取到 `counters` 后，`if counters.Retry.Active() { return transportRetryStatusLine(tick, counters.Retry, time.Now()) }`（在目标/计划/首 token 拼接之前）。

**Verify**：`CGO_ENABLED=1 go build ./pkg/tui/` → exit 0（若与卡片版 `formatRetryCountdown` 重名，先做 Step 4 的删除）

### Step 4：删除重试卡与网络错误卡的全部代码

按 Current state 的清单逐处删除/还原：
1. `reducer.go`：删 `transportRetryStepIDFor`、`transportRetryErrorStepIDFor`、`transportRetryErrorText`、`FrameRetrying` 常量、`Frame.Retry/RetryTotal/RetryDeadline`、
   `retryCardLine`、卡片版 `formatRetryCountdown`、`renderFrameLinesWithAgents`/`statusMarker`/`hasLiveBlockLocked`/`renderBlockFull` 里的 `FrameRetrying` 分支、
   `liveRetryCardMatch`、`dismissLiveRetryCardsLocked`；`retainFrameLocked` 开头的 dismissal `switch` 与 `liveRetryAgents` 赋值删掉，
   无主错误分支两处 `r.vm.replaceOrAppendBlock(f)` 还原为 `r.vm.append(f)` 并删掉其上方关于 transport-retry 的注释，三处 `|| f.Kind == FrameRetrying` 删掉并把注释还原为不含 retry 的说法
   （`// Tool, fanout, memory-compact, skill-install and migration frames replace the previous block with the same StepID (compact running → compact ran, installing → installed, previewing → migrating → migrated).`）。
2. `tty_session.go`：`streamingFrameKind` 只剩 `k == FrameThinking || k == FrameAssistant`，删掉注释里 “The retry card joins them…” 一句；`replaceOrAppendBlock` 的 case 列表删掉 `FrameRetrying`。
3. `render.go`：删 `liveRetryAgents` 字段及注释。
4. 若 `reducer.go` 顶部因此不再需要 `llm` import，按编译器提示处理（`llm` 还被 `openPlanReviewCard` 使用，正常不会变）。

**Verify**：
- `git grep -n "FrameRetrying\|retryCardLine\|liveRetryAgents\|liveRetryCardMatch\|dismissLiveRetryCardsLocked\|transportRetryStepIDFor\|transportRetryErrorStepIDFor\|transportRetryErrorText\|RetryDeadline\|RetryTotal" -- pkg/tui` → 只剩测试文件里将在 Step 5 删除的引用
- `CGO_ENABLED=1 go build ./pkg/tui/` → exit 0

### Step 5：测试

`transport_retry_test.go`：删除 Current state 列出的 6 个卡片测试；保留 115 加的 `TestWithdrawDeadStreamBlocksRemovesOnlyLiveBlocks`；
把 `TestTransportRetryClearedIsANoOp` 改成 `TestTransportRetryClearedClearsTheStatus`。新增：

1. `TestTransportRetryMsgSetsTrackerAndEmitsNoFrames`：reducer（带 tracker）收 `TransportRetryMsg{Retry:1, Total:10, Wait:8*time.Second, Err: errors.New("read tcp: i/o timeout")}`
   → `EventResult.Frames` 为空、主 agent buffer 已清、`tracker.SnapshotActiveRun().Retry` 为 `Retry 1 / Total 10`、Deadline 约 8s 后。
2. `TestTransportRetryClearedClearsTheStatus`：先设再收 `Cleared:true` → `SnapshotActiveRun().Retry.Active()` 为 false、无帧。
3. `TestWorkingLineShowsRetryStatus`（参照 `run_test.go:2920` 一带 `workingStatusFormatter(func() Counters {…}, nil)` 的写法）：
   - `Retry 2/10`、Deadline=now+8s、Err="read tcp x" → 输出含 `Network error · Retrying in 8s · attempt 2/10`，不含 `Working`、`esc to interrupt`；
   - `Retry 3/10` → 原因是错误原文；Err 为 80 个字符时被截到 60 列并以 `…` 结尾；
   - `Retry 1/2`（total=2）→ `min(3,2)=2`，第 1 次写 `Network error`；`Retry 2/2` 写原文；
   - Deadline 已过 → `Retrying in 0s`。
4. `TestFormatRetryCountdownTiers`：`500ms→"1s"`、`8s→"8s"`、`59.2s→"1m 0s"`（向上取整为 60 秒，归入分钟档）、`65s→"1m 5s"`、`6m→"6m"`、`2h30m→"2h"`、`26h→"1d"`、`-1s→"0s"`。
5. `TestSubagentWorkingLineShowsItsOwnRetry`：tracker `ObserveAgent("task-1", "exec-1", now)` 后 `ObserveTransportRetry("task-1", 1, 10, 8s, "x")`；
   `workingStatusFormatter(tracker.SnapshotActiveRun, tracker.SnapshotAgentRun)` 对 `view="task-1"` 输出重试行，对 `view=""` 输出普通 Working 行。
6. `TestStartRunClearsAStaleRetry`：主 agent 有重试状态时 `StartRun("run-2")` 后状态清空。

**Verify**：TUI 测试命令 → `ok`；`gofmt -l pkg/tui` 无输出；`go vet` 无输出。

### Step 6：run-forebrain 文档与真机验收

1. `.claude/skills/run-forebrain/SKILL.md` 的模式表（`| Mode | Behaviour | Use it to test |`）在 `limit` 行上方加一行：

```
| `netdrop` | request 1 streams part of an answer, then closes the connection cleanly with no finish frame; later requests answer normally. `FAKE_NETDROP_EVERY=1` drops every request, `FAKE_NETDROP_SILENT=1` drops with no bytes; `NETWORK_RETRY_MAX=N` sets `agents.defaults.network_retry_max` | network retry: the Working line's retry status, the dead attempt withdrawn, exhaustion |
```

2. 真机（构建后，按 SKILL.md 的 agent path；每段结束 `$D stop`）：

```bash
D=.claude/skills/run-forebrain/driver.sh
# A：断一次后恢复
$D start netdrop
$D submit 'hello'
$D wait 'Worked for' 30
$D screen      # 期望：答案只出现一次；屏幕上没有 "partial answer that never"、没有 "Network error"、没有 "Retrying"
$D messages    # 期望：没有任何一行含 "partial answer that never"
$D stop
# B：持续断网，观察状态行与用完
FAKE_NETDROP_EVERY=1 NETWORK_RETRY_MAX=4 $D start netdrop
$D submit 'hello'
$D wait 'attempt 3/4' 15
$D screen      # 期望：输入框上方一行 "<错误原文…> · Retrying in Ns · attempt 3/4"，对话里没有重试卡/网络错误卡
$D wait 'gave up after 4 retries' 30
$D screen      # 期望：只有一张回合错误卡，文案以 "network error, gave up after 4 retries:" 开头；状态行已消失
$D stop
# C：重试等待中按 Esc
FAKE_NETDROP_EVERY=1 NETWORK_RETRY_MAX=6 $D start netdrop
$D submit 'hello'
$D wait 'attempt 4/6' 20
$D key Escape
$D screen      # 期望：回合被取消，状态行消失
$D stop
```

**Verify**：A、B、C 三段的 `screen` / `messages` 与期望一致；把三段的 `screen` 输出贴进执行报告。

## Done criteria

- [ ] `git grep -n "FrameRetrying\|retryCardLine\|liveRetryAgents\|transportRetryErrorText\|RetryDeadline" -- pkg/tui` 无输出
- [ ] TUI 测试 `ok`，包含 Step 5 的新测试；`go vet`、`gofmt -l pkg/tui` 干净
- [ ] 真机 A、B、C 符合期望，`screen` 输出已附在报告里
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置检查不满足（115 未完成）或 Drift check 有输出且摘录与现场不符。
2. 子代理事件里的 `AgentID`（`TransportRetryMsg.AgentID`）与 `Tracker.ObserveAgent` / `SnapshotAgentRun` 使用的 key 不是同一个值（Step 5 第 5 条用真实 roster key 跑不通）——回报两边的实际取值。
3. 还原 `retainFrameLocked` 的错误分支后，`pkg/tui` 里与网络重试无关的既有测试失败。
4. 真机 A 里屏幕上仍出现 `partial answer that never`，或 `messages` 里有这行——说明 115 的撤下/不落库没生效，回报而不是在 TUI 层补救。
5. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 重试状态的唯一来源是 `Tracker.retries`，只由 `TransportRetryMsg` 写入；Working 行每次 paint 时现算倒计时，不要为它加定时器。
- 以后给 Working 行加新段落时，确认重试状态仍是整行替换（重试期间不拼其他段）。
- `118` 会在事件里加 HTTP 状态码，届时原因文案对 HTTP 错误改用 `API error`，改动点就是 `transportRetryStatusLine` 的 `reason`。
