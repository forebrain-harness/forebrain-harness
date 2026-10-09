# Plan 002: subagent 视图的 composer 重绘不再整读 subagent 账本（修复快速滚轮后输入卡死）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 08a3145..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/agent/subagent_history.go pkg/tui/run.go pkg/tui/chat_session.go`
> 输出为空 = 无漂移。若有任何文件变化，先把下文「Current state」里的摘录逐段对照现行代码；
> 对不上即按 STOP 处理。

## Status

- **Priority**: P1（owner 报告的严重 bug：subagent 视图快速滚轮后输入冻结数秒）
- **Effort**: S（生产代码约 40 行，集中在 `pkg/run/subagent.go` 一个文件）
- **Risk**: LOW（只改「找 channel」的方式，不改 channel / 队列 / 执行语义；有红绿可证的新测试和真机定量验收）
- **Depends on**: none
- **Category**: bug / perf
- **Planned at**: commit `08a3145`，2026-10-09

## Why this matters

在 subagent 视图里，**每处理一次输入事件**（滚轮、按键、点击），TUI 主 goroutine 都会重绘一次 composer，
而 composer 的「待发送队列预览」走的是 `run.SubagentInputPreview → agent.GetMerged → agent.ListHistory`：
**整文件读取并逐行 JSON 解码** workspace 级的 `state/subagent-history.jsonl`。owner 真实环境里这个账本
已有 3.4 MB / 448 条（只追加、从不清理，每次 subagent 执行开始/结束各写一条），一次查找约 30 ms。
快速滚动一次就产生几百到上千个滚轮事件，主循环每轮都付这 30 ms，事件在输入管线里积压，表现为
「输入卡死几秒，恢复后把卡死期间的鼠标/按键快速执行一遍」。主视图走内存队列，不受影响。
修复后：subagent 视图的预览、撤回（Shift+←）、Esc、丢弃、running 判断全部只查进程内存里的
subagent channel，**不再碰账本**，subagent 视图的输入延迟回到与主视图同一量级。

## 根因证据（advisor 已实测，执行者不必重做，验收时按 §真机验收 复测）

1. **定量对照**（2026-10-09，tmux 120×40，owner 真实 `~/.forebrain`，HEAD `08a3145` 构建，
   resume 已结束的会话 `cli-d060f4c2-…`，同一进程先测 subagent 视图再按 Esc 测主视图；
   方法：一次注入 N 个 SGR 滚轮事件，紧跟探针字符 `Z`，测 `Z` 出现在 composer 的延迟）：

   | N（滚轮事件数） | subagent 视图 | 主视图（对照，transcript 有 557 条消息） |
   |---|---|---|
   | 0 | 0.10 s | 0.04 s |
   | 200 | 0.86 s | 0.04 s |
   | 500 | 1.99 s | 0.07 s |
   | 1000 | **3.97 s** | **0.11 s** |

   → 与 transcript 长度无关（主视图更长却更快）；tmux 里稳定复现（推翻了此前「只在裸终端、终端排水
   阻塞」的结论，见 `docs/plan/SUBAGENT_VIEW_PERF_ROLLBACK_PLAN.md`）。此前在 tmux 复现不出来，
   是因为 driver 的隔离 home 里账本几乎为空。

2. **CPU 采样**（同一场景，1500 个滚轮事件期间 `sample <pid> 3`，地址用
   `go tool addr2line <binary>` 还原，load address 减 `0x100000000` 为 slide）：所有落在
   `tui.Run` 下的主 goroutine 采样（124/124）都是这一条栈：
   `tui.Run → tui.renderComposerWithState → (*streamState).composerPendingInputPreview →
   (*ChatSession).SubagentInputPreview → run.SubagentInputPreview → run.subagentChannelForConversation →
   agent.GetMerged → agent.ListMerged → agent.ListHistory → encoding/json.Unmarshal`；
   其余热点是它每次约 10 MB 分配带来的 GC（`runtime.gcDrain` / `scanObject`）。
   `paintViewportLocked` 不在前 60 个热点地址里。

3. **单次成本**：对真实账本副本调用 `agent.GetMerged` 20 次，平均 29.6 ms（命中）/ 33.3 ms（未命中）。
   空闲循环每轮最多合并约 9 个滚轮事件（输入 channel 容量 8 + 中间 goroutine 手里 1 个），
   1000 / 9 × ~36 ms ≈ 4.0 s，与实测 3.97 s 吻合。活动回合循环（`runTurnWithSkill`）不合并滚轮，
   每个事件都重绘一次 composer，同样场景只会更糟（≈ N × 30 ms）。

4. **为什么「滚到首条消息/底部」时最明显**：积压期间每轮仍会 `drainNotifications`，流式输出照常绘制；
   视图滚到端点后，剩余积压的滚轮事件不再改变画面，用户看到的就是「画面在动、输入全死」。

## Current state

（全部摘录已在 commit `08a3145` 逐行核实。）

### 热路径：每次 composer 重绘都调 `SubagentInputPreview`

`pkg/tui/run.go:2201-2212`（`renderComposerWithState` 在 `pkg/tui/run.go:3126` / `:3139` 调它；
空闲循环每轮顶部、活动回合循环每处理一个输入事件后都会调 `renderComposerWithState`）：

```go
func (s *streamState) composerPendingInputPreview() ComposerPendingInputPreview {
	if s == nil {
		return ComposerPendingInputPreview{}
	}
	// A subagent's view shows that subagent's own queued input, from its own
	// queue (plan 007 §6).
	if view := strings.TrimSpace(s.composerView); view != "" {
		if s.session == nil {
			return ComposerPendingInputPreview{}
		}
		return s.session.SubagentInputPreview(s.sessionID, view)
	}
```

`pkg/tui/chat_session.go:1112-1120`（只是转发，**本计划不改**）：

```go
// SubagentInputPreview is the subagent's queued input as its own view shows it.
func (s *ChatSession) SubagentInputPreview(sessionID, agentKey string) ComposerPendingInputPreview {
	preview := run.SubagentInputPreview(s.runner(), strings.TrimSpace(sessionID), strings.TrimSpace(agentKey))
	...
```

### 慢的根源：每次都整读账本

`pkg/agent/subagent_history.go:105-163` `ListHistory`（**本计划不改**）：`os.ReadFile` 整个
`<workspaceRoot>/state/subagent-history.jsonl` → `strings.Split` → 从尾到头逐行 `json.Unmarshal`，
直到凑够 `Limit`（默认 20）条匹配；按 (SessionID, TaskID) 查时匹配项只有几条，所以**每次都扫完整个文件**。

### 要改的文件：`pkg/run/subagent.go`

channel 结构与全局表，`pkg/run/subagent.go:579-604`、`:625-628`：

```go
// subagentChannel is the user's side of one subagent's conversation: the queue
// of what the user sent it, the one execution at a time that consumes it, and
// the surface the user is talking to it from. It is engine state — a surface
// reaches it only through the Subagent* functions below — and it lives for the
// life of the process, keyed by the subagent's worker session, the same way a
// conversation's queue lives in the run controller.
type subagentChannel struct {
	workerSessionID string
	agentKey        string
	// runID and sessionID are what the subagent's own events are published
	// on, read from the record the channel was built from.
	runID     string
	sessionID string
	store     *state.SessionStore
	queue     *InputQueue
	...
}
...
var (
	subagentChannelsMu sync.Mutex
	subagentChannels   = map[string]*subagentChannel{}
)
```

`subagentChannelFor`，`pkg/run/subagent.go:630-656`（**所有**创建 channel 的路径都经过它：
`SendToSubagent` 经 `subagentChannelForConversation`，`runSubagentExecution` 在 `:778` 直接调它）：

```go
// subagentChannelFor returns the channel of one subagent, creating it on first
// use. It is keyed by the worker session, which is unique to the subagent; the
// queue is the run controller's for that session, so the channel and the
// controller agree on one queue.
func subagentChannelFor(fac Factory, record agent.HistoryEntry) *subagentChannel {
	workerSessionID := strings.TrimSpace(record.WorkerSessionID)
	subagentChannelsMu.Lock()
	defer subagentChannelsMu.Unlock()
	ch := subagentChannels[workerSessionID]
	if ch == nil {
		ch = &subagentChannel{
			workerSessionID: workerSessionID,
			agentKey:        subagentRosterKey(record),
			runID:           strings.TrimSpace(record.RunID),
			sessionID:       strings.TrimSpace(record.SessionID),
			exec:            make(chan struct{}, 1),
		}
		if fac.Owner != nil {
			ch.store = fac.Owner.SessionStore
		}
		if control := fac.ownerControl(); control != nil {
			ch.queue = control.SessionQueue(workerSessionID)
		}
		subagentChannels[workerSessionID] = ch
	}
	return ch
}
```

账本查找，`pkg/run/subagent.go:1809-1825`：

```go
// subagentChannelForConversation resolves an agent key to its channel in this
// conversation, or ErrSubagentNotFound. The lookup filters by conversation, so
// another conversation's subagent — and another tenant's — is not found.
func subagentChannelForConversation(r *Runner, conversationSessionID, agentKey string) (*subagentChannel, agent.HistoryEntry, error) {
	if r == nil {
		return nil, agent.HistoryEntry{}, ErrSubagentNotFound
	}
	fac := r.subagentFactory()
	record, ok, err := agent.GetMerged(fac.subagentScopeRoot(), agent.Query{SessionID: conversationSessionID, TaskID: agentKey})
	if err != nil {
		return nil, agent.HistoryEntry{}, err
	}
	if !ok {
		return nil, agent.HistoryEntry{}, ErrSubagentNotFound
	}
	return subagentChannelFor(fac, record), record, nil
}
```

它的 7 个调用者（`pkg/run/subagent.go`）：

| 行 | 函数 | 谁在什么时候调 | 本计划 |
|---|---|---|---|
| 1850 | `SendToSubagent` | subagent 视图里按 Enter 发送 | **不改**（channel 不存在时需要账本记录来创建它、启动执行） |
| 1872 | `SubagentInputPreview` | **每次 composer 重绘**（根因） | 改为只查内存 |
| 1882 | `RecallSubagentInput` | subagent 视图 Shift+← | 改为只查内存 |
| 1894 | `InterruptSubagentToSend` | subagent 视图 Esc | 改为只查内存 |
| 1905 | `WithdrawSubagentInput` | subagent 视图 Esc | 改为只查内存 |
| 1915 | `DiscardSubagentInput` | 切会话时对 roster 每一行各调一次 | 改为只查内存 |
| 1926 | `SubagentRunning` | `/compact`、测试轮询 | 改为只查内存 |

当前 6 个函数的原文，`pkg/run/subagent.go:1870-1933`：

```go
// SubagentInputPreview is the subagent's queued input as its view shows it.
func SubagentInputPreview(r *Runner, conversationSessionID, agentKey string) QueuePreview {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return QueuePreview{}
	}
	return ch.queue.Preview()
}

// RecallSubagentInput pulls the newest queued message back out for editing,
// exactly as Recall does for the primary conversation's queue.
func RecallSubagentInput(r *Runner, conversationSessionID, agentKey string) (Input, bool) {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return Input{}, false
	}
	return ch.queue.Recall()
}

// InterruptSubagentToSend stops the running execution precisely to send the
// steers queued behind it — Esc's second meaning in a subagent's view. It
// returns false when no execution is running or no steer is waiting, so the
// surface can fall back to Esc's other meanings.
func InterruptSubagentToSend(r *Runner, conversationSessionID, agentKey string) bool {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return false
	}
	return ch.interruptToSend()
}

// WithdrawSubagentInput takes a just-sent user message back before the subagent
// answers — Esc's first meaning. It returns the withdrawn message plus
// everything queued after it, or false when the window has closed.
func WithdrawSubagentInput(r *Runner, conversationSessionID, agentKey string) ([]Input, bool) {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return nil, false
	}
	return ch.withdraw()
}

// DiscardSubagentInput empties the subagent's queue and returns how many
// messages were dropped.
func DiscardSubagentInput(r *Runner, conversationSessionID, agentKey string) int {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil {
		return 0
	}
	return ch.queue.Discard()
}

// SubagentRunning reports whether this subagent has an execution in flight. It
// is the one definition of "running" for a subagent: the registry handle that
// used to answer it does not cover a user-driven execution.
func SubagentRunning(r *Runner, conversationSessionID, agentKey string) bool {
	ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)
	if err != nil || ch == nil {
		return false
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.running != nil
}
```

### 为什么「只查内存」与旧语义等价

- 旧查找的过滤条件是 `agent.Query{SessionID: conversationSessionID, TaskID: agentKey}`（`matchesQuery`，
  `pkg/agent/subagent_history.go:287-301`：`entry.SessionID == SessionID && entry.TaskID == TaskID`），
  找到记录后按 `record.WorkerSessionID` 取 channel。一个 task id 终身对应同一个 worker session
  （用户驱动的续跑 `lifecycle := record` 只改 ExecutionID/Status，`pkg/run/subagent.go:1985-1995`）。
  所以以 **(record.SessionID, record.TaskID)** 为键、在 `subagentChannelFor` 里登记 channel 的二级索引，
  查到的就是旧查找会得到的那个 channel。
- 队列与执行状态**只存在于 channel 上**：subagent 的队列是 `control.SessionQueue(workerSessionID)`，
  唯一往里放东西的入口是 `SendToSubagent`（先建 channel）和执行期的 channel 逻辑；执行只走
  `runSubagentExecution`（`:778` 先建 channel）。所以「本进程没有这个 channel」⇔「没有排队、没有在跑」，
  6 个函数都返回空/false/0——与旧代码对一个新建空 channel 的回答完全相同。
- 租户/会话隔离：键里含会话 id（生产中是 UUID，如 `cli-d060f4c2-…`），另一个会话的同名 task 查不到，
  与旧注释「another conversation's subagent — and another tenant's — is not found」一致。
- **不要**用 `*Runner` 指针做键：配置重载可能替换 `Env.Runner`，重载前建的 channel 会因此查不到。
- 测试里多个用例复用字面 id（如 gateway 的 `seedSubagentRecord` 固定 `WorkerSessionID: "worker-1"`、
  `"sid"`/`"agent-1"`）：旧代码按 worker 共享同一个 channel，新索引在每次 `subagentChannelFor` 时
  覆盖登记（latest wins），指向同一个 channel，行为一致。

### 仓库约定

- 注释密度高、写「为什么」：照 `subagentChannelForConversation`、`subagentChannelFor` 上方注释的语气写
  英文注释，不写中文注释，不写「修复了某 bug」式的历史注释。
- 生产代码不新增文件（`pkg/architecture` 限制每包生产文件数）；测试加在已有的 `pkg/run/subagent_test.go`。
- `pkg/run` 行数变化必须重新生成 `pkg/architecture/testdata/graph.json`（CI 步骤
  「Verify package graph」：`scripts/package-graph.sh && git diff --exit-code -- pkg/architecture/testdata/graph.json`）。
- 构建/测试必须 `CGO_ENABLED=1` + `-tags fts5`（SQLite FTS5）；看到 `no such module: fts5` 是漏了 tag。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build pkg | `CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway` | exit 0，无输出 |
| 目标测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel\|TestInterruptSubagentToSendRunsThePendingSteersNext\|TestWithdrawBeforeTheSubagentAnswers\|TestWithdrawnUserExecutionPublishesItsEndSoTheSurfaceRetiresIt' -count=1` | `ok  .../pkg/run`（advisor 实测旧三项约 40 s 含编译） |
| 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/gateway ./pkg/tui -count=1` | 三行 `ok` |
| 全量 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m` | 全部 `ok`（已知干扰见 STOP 之前的说明） |
| vet | `go vet ./...` | 无输出 |
| gofmt | `gofmt -l cmd pkg third_party` | 无输出 |
| 包图 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 graph.json 1 个文件、pkg/run 的 `loc` 一处变化 |

全量测试已知干扰（与本计划无关，先单独重跑判定）：`pkg/tui` 的 `TestCharacterizationNetworkApproval`
依赖真联网，离线时 `curl: (28)` 超时；`pkg/gateway` 偶发 macOS TempDir 清理 flake（`-count=3` 复跑即绿）。

## Scope

**In scope**（只允许改这些）：
- `pkg/run/subagent.go`：新增二级索引与 `liveSubagentChannel`；改 6 个函数（见 Step 2）。
- `pkg/run/subagent_test.go`：新增 1 个测试（Step 3）。
- `pkg/architecture/testdata/graph.json`：由 `scripts/package-graph.sh` 重新生成（不要手改）。
- `plans/README.md`：更新本计划状态行。

**Out of scope**（看起来相关，但**不要碰**）：
- `pkg/agent/subagent_history.go`（`ListHistory`/`GetMerged` 的实现与账本格式）：owner 已定方向是
  「所有会话消息只存 SQLite」，账本整体迁库是**下一期**单独的计划；本期给它加缓存/索引是白做。
- `SendToSubagent`、`SubagentCompactTarget`、`mergedSubagentRecord`、`SubagentContextBudget`
  及 `pkg/tui`、`pkg/gateway` 里其他 `agent.GetMerged`/`ListMerged` 调用：都是一次性动作（按 Enter、
  打开视图、`/compact`、审批恢复、HTTP 请求），不在每事件路径上，留给迁库计划。
- `pkg/tui/run.go` 的滚轮合并、`renderComposerWithState` 调用频率、`pkg/tui/input_events.go` 的
  读入管线（1 字节读、ack 握手）：主视图同样走这些路径，1000 个事件只要 0.11 s，不是根因。
- `pkg/tui/reducer.go` 的绘制/滚动：同上，已被主视图对照排除。
- 工作区里既有的 `pkg/gateway/dist/**` 未暂存删除：不碰、不恢复、不暂存。

## Git workflow

- **不要 commit、不要建分支、不要 push、不要 `git add`**：owner 手工审阅并提交。改动留在工作区。
- 不要运行 `make` 或任何会重建 `npm/dist/**` 的命令：owner 正在用
  `npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/forebrain`
  跑自己的会话，覆盖它会影响 owner。

## Steps

### Step 1: 给 channel 表加 (会话, task) 二级索引，并新增 `liveSubagentChannel`

在 `pkg/run/subagent.go`：

1a. 把 `:625-628` 的 `var (...)` 扩成下面这样（保留原两行，追加索引与键类型）：

```go
var (
	subagentChannelsMu sync.Mutex
	subagentChannels   = map[string]*subagentChannel{}
	// subagentChannelsByTask indexes the same channels by what a surface names
	// a subagent with — the conversation and the task id — so the questions a
	// subagent's view asks on every input event are answered from memory. See
	// liveSubagentChannel.
	subagentChannelsByTask = map[subagentTaskKey]*subagentChannel{}
)

// subagentTaskKey is the conversation and task id one subagent is filed
// under: the same two fields the ledger lookup filters on.
type subagentTaskKey struct {
	conversationSessionID string
	taskID                string
}
```

1b. 在 `subagentChannelFor` 里、`return ch` 之前（在 `if ch == nil {...}` 块**之外**，这样已存在的
channel 也会被登记），加：

```go
	// Every path that creates or reuses a channel passes here with the record
	// it resolved, so the index always names the channel the ledger lookup
	// would have reached.
	if conv, task := strings.TrimSpace(record.SessionID), strings.TrimSpace(record.TaskID); conv != "" && task != "" {
		subagentChannelsByTask[subagentTaskKey{conversationSessionID: conv, taskID: task}] = ch
	}
	return ch
```

1c. 紧跟在 `subagentChannelForConversation`（`:1809-1825`）之后新增：

```go
// liveSubagentChannel returns the channel this process already holds for one
// subagent of a conversation, or nil when it holds none. It never reads the
// subagent ledger, and that is its point: a subagent's view asks what is
// queued on every repaint of its composer — once per input event — and the
// ledger lookup re-reads and re-decodes the whole workspace-wide history file
// each time, a cost that grows with every subagent ever run and, under a fast
// wheel scroll, held the view's input back for seconds.
//
// A subagent with no channel here has nothing queued and no execution running
// in this process: its queue and its executions exist only through a channel,
// which SendToSubagent and runSubagentExecution create first. So callers read
// nil as the empty answer. The key is the conversation and task id the ledger
// lookup filtered on, so another conversation's subagent is not found.
func liveSubagentChannel(r *Runner, conversationSessionID, agentKey string) *subagentChannel {
	if r == nil {
		return nil
	}
	key := subagentTaskKey{
		conversationSessionID: strings.TrimSpace(conversationSessionID),
		taskID:                strings.TrimSpace(agentKey),
	}
	if key.conversationSessionID == "" || key.taskID == "" {
		return nil
	}
	subagentChannelsMu.Lock()
	defer subagentChannelsMu.Unlock()
	return subagentChannelsByTask[key]
}
```

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0、无输出
（此时 `liveSubagentChannel` 尚未被使用，Go 对未用的包级函数不报错）。

### Step 2: 6 个函数改走 `liveSubagentChannel`

把 `pkg/run/subagent.go` 中 `SubagentInputPreview`、`RecallSubagentInput`、`InterruptSubagentToSend`、
`WithdrawSubagentInput`、`DiscardSubagentInput`、`SubagentRunning` 的
`ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)` + `if err != nil {...}`
统一替换为 `ch := liveSubagentChannel(...)` + `if ch == nil {...}`，返回值不变。目标形状：

```go
// SubagentInputPreview is the subagent's queued input as its view shows it.
// The view asks on every repaint of its composer, so it is answered from the
// live channel only (see liveSubagentChannel).
func SubagentInputPreview(r *Runner, conversationSessionID, agentKey string) QueuePreview {
	ch := liveSubagentChannel(r, conversationSessionID, agentKey)
	if ch == nil {
		return QueuePreview{}
	}
	return ch.queue.Preview()
}

func RecallSubagentInput(r *Runner, conversationSessionID, agentKey string) (Input, bool) {
	ch := liveSubagentChannel(r, conversationSessionID, agentKey)
	if ch == nil {
		return Input{}, false
	}
	return ch.queue.Recall()
}

func InterruptSubagentToSend(r *Runner, conversationSessionID, agentKey string) bool {
	ch := liveSubagentChannel(r, conversationSessionID, agentKey)
	if ch == nil {
		return false
	}
	return ch.interruptToSend()
}

func WithdrawSubagentInput(r *Runner, conversationSessionID, agentKey string) ([]Input, bool) {
	ch := liveSubagentChannel(r, conversationSessionID, agentKey)
	if ch == nil {
		return nil, false
	}
	return ch.withdraw()
}

func DiscardSubagentInput(r *Runner, conversationSessionID, agentKey string) int {
	ch := liveSubagentChannel(r, conversationSessionID, agentKey)
	if ch == nil {
		return 0
	}
	return ch.queue.Discard()
}

func SubagentRunning(r *Runner, conversationSessionID, agentKey string) bool {
	ch := liveSubagentChannel(r, conversationSessionID, agentKey)
	if ch == nil {
		return false
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.running != nil
}
```

其余 5 个函数**原有的文档注释保持不变**（上面为省篇幅没重复）；只有 `SubagentInputPreview` 的注释
追加那两句。`SendToSubagent` 一个字都不改。

**Verify**:
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/run ./pkg/tui ./pkg/gateway` → exit 0、无输出
- `grep -c "liveSubagentChannel(" pkg/run/subagent.go` → `7`（1 处定义 + 6 处调用）
- `grep -n "subagentChannelForConversation(" pkg/run/subagent.go` → 恰好 2 行：定义（约 1812 行）和 `SendToSubagent` 里的调用
- 既有测试：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestInterruptSubagentToSendRunsThePendingSteersNext|TestWithdrawBeforeTheSubagentAnswers|TestWithdrawnUserExecutionPublishesItsEndSoTheSurfaceRetiresIt|TestModelContinuationWaitsForTheUsersExecutionOfTheSameSubagent|TestUserCanTalkToEveryKindOfSubagent|TestUserMessageContinuesAFailedSubagent' -count=1` → `ok`

### Step 3: 新增测试，证明这些问题由内存里的 channel 回答、不经过账本

在 `pkg/run/subagent_test.go` 里，紧跟 `TestInterruptSubagentToSendRunsThePendingSteersNext`（约 4830-4879 行）
之后加入下面的测试。它以那个测试为结构范本（同样的 fixture、executor、seed 方式）。`os`、`path/filepath`、
`agent`、`llm`、`context` 均已在该文件 import，无需新增 import。

```go
// TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel pins where a
// subagent view's per-repaint and per-keystroke questions are answered: from
// the channel this process holds, never from the subagent ledger. The view
// repaints its composer — and asks for the queue preview — after every input
// event, so a lookup that re-reads the whole ledger each time puts that cost on
// every wheel notch. The ledger is made unreadable mid-run here, so an answer
// that still goes through it comes back empty.
func TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel(t *testing.T) {
	fix := newPersistenceFixture(t, "worker system")
	owner := fix.fac.Owner
	owner.Control = NewController()
	owner.Events = &channelEventProbe{}
	release := make(chan struct{})
	owner.SubagentExecutor = &channelTestExecutor{store: fix.store, hold: true, release: release, answer: "x"}
	surface := SubagentSurface{OnBoundary: func(string, []Input, []Input) {}}
	conv := "conv-live-channel"
	worker := "main:conv-live-channel:worker:00000000-0000-0000-0000-0000000004bb"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-live", RunID: "seed-run-live", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "work", Status: agent.StatusOK, AgentType: "explore", AgentKind: "typed", RuntimeKind: "typed_subagent",
		Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	if got, err := SendToSubagent(context.Background(), owner, surface, conv, "task-live", Input{Text: "start"}, TurnInputModeSteer); err != nil || got != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", got, err)
	}
	waitSubagentBusy(t, owner, conv, "task-live")
	for _, text := range []string{"one", "two"} {
		if got, err := SendToSubagent(context.Background(), owner, surface, conv, "task-live", Input{Text: text}, TurnInputModeSteer); err != nil || got != SubagentDeliverySteered {
			t.Fatalf("steer %q = %q, %v; want steered", text, got, err)
		}
	}

	// From here on every ledger read fails: the file becomes a directory.
	root := fix.fac.subagentScopeRoot()
	ledger := filepath.Join(root, "state", "subagent-history.jsonl")
	saved, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledger, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := agent.GetMerged(root, agent.Query{SessionID: conv, TaskID: "task-live"}); err == nil {
		t.Fatal("the ledger is still readable, so this test would prove nothing")
	}

	if p := SubagentInputPreview(owner, conv, "task-live"); len(p.Steers) != 2 || p.Steers[0] != "one" || p.Steers[1] != "two" {
		t.Fatalf("SubagentInputPreview = %+v, want steers [one two] from the live channel", p)
	}
	if !SubagentRunning(owner, conv, "task-live") {
		t.Fatal("SubagentRunning = false, want true from the live channel")
	}
	if p := SubagentInputPreview(owner, "conv-other", "task-live"); p.Visible() {
		t.Fatalf("another conversation's preview = %+v, want nothing", p)
	}
	if p := SubagentInputPreview(owner, conv, "task-missing"); p.Visible() {
		t.Fatalf("unknown task's preview = %+v, want nothing", p)
	}
	if in, ok := RecallSubagentInput(owner, conv, "task-live"); !ok || in.Text != "two" {
		t.Fatalf("RecallSubagentInput = %+v, %v; want the newest steer", in, ok)
	}
	if n := DiscardSubagentInput(owner, conv, "task-live"); n != 1 {
		t.Fatalf("DiscardSubagentInput = %d, want 1", n)
	}

	// The execution's end appends to the ledger: give the file back first.
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitSubagentIdle(t, owner, conv, "task-live")
}
```

**Verify（红绿反证，两步都要做）**:
1. 绿：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel -count=1 -v`
   → `--- PASS: TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel`
2. 红（证明测试真的盯住了根因）：**临时**把 `SubagentInputPreview` 里的
   `ch := liveSubagentChannel(r, conversationSessionID, agentKey)` 换回
   `ch, _, err := subagentChannelForConversation(r, conversationSessionID, agentKey)` + `if err != nil { return QueuePreview{} }`，
   重跑同一命令 → `FAIL`，消息含 `SubagentInputPreview = {Steers:[] ...}, want steers [one two] from the live channel`。
   然后**恢复** Step 2 的写法，重跑 → `PASS`。用 `git diff pkg/run/subagent.go` 确认恢复后与 Step 2 一致。

### Step 4: 相关包与全量门禁

**Verify**（按顺序）：
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/gateway ./pkg/tui -count=1` → 三行 `ok`
2. `go vet ./...` → 无输出
3. `gofmt -l cmd pkg third_party` → 无输出
4. `scripts/package-graph.sh && git diff -- pkg/architecture/testdata/graph.json` → 只有
   `github.com/forebrain-harness/forebrain-harness/pkg/run` 条目的 `"loc"` 一处数字变化（当前 17524，
   增加约 40-60）；无 edges 变化
5. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
6. `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m` → 全部 `ok`（已知干扰项按「Commands」一节判定）
7. `git status --short | grep -v '^ D pkg/gateway/dist/'` → 只列出
   `pkg/run/subagent.go`、`pkg/run/subagent_test.go`、`pkg/architecture/testdata/graph.json`、`plans/README.md`

### Step 5: 真机定量验收（tmux + owner 真实 `~/.forebrain`，owner 已授权用它复现）

规则（必须遵守）：
- 用**独立的** tmux 会话名 `fb-ledger-fix-probe`；不要碰任何已存在的 tmux 会话和 owner 正在跑的 forebrain 进程
  （`pgrep -fl forebrain` 能看到它们，不要 kill）。
- 二进制构建到仓库**外**的临时目录；不要覆盖 `npm/dist/**`、`/tmp/fbdbg/**`。
- **只滚动、点击、输入探针字符再删掉；绝不按 Enter 提交任何消息**（不能产生模型调用、不能改会话）。
- 结束后 `tmux kill-session -t fb-ledger-fix-probe`。

5a. 前置检查：`wc -c ~/.forebrain/workspace/state/subagent-history.jsonl` → 应 ≥ 1000000 字节
（2026-10-09 为 3474833）。若明显更小，修复前的卡顿本就不显著；照做 5b-5e，并在报告里注明账本大小。

5b. 构建：

```bash
FIX=$(mktemp -d)/fb-ledger-fix && mkdir -p "$FIX"
CGO_ENABLED=1 go build -tags fts5 -o "$FIX/forebrain" ./cmd/forebrain
scripts/install-dictionary.sh "$FIX"
echo "$FIX"
```

5c. 启动并打开 subagent 视图（会话 `cli-d060f4c2-f798-45e9-b26e-0ca4dedfca70` 是已结束的旧会话，
含一个标题为 `Execute plan 001 recall fix` 的 general-purpose subagent）：

```bash
S=fb-ledger-fix-probe
tmux new-session -d -s $S -x 120 -y 40 -c "$(git rev-parse --show-toplevel)" \
  "$FIX/forebrain resume cli-d060f4c2-f798-45e9-b26e-0ca4dedfca70 2>$FIX/tui.err; echo EXIT=\$?; exec bash"
sleep 6; tmux capture-pane -t $S -p | tail -3      # 看到 composer 行 "›" 和 footer
# 向上滚找卡片（每轮 20 档；advisor 实测约在 1100-1300 档处出现）
up=$(printf '\033[<64;60;10M')
for round in $(seq 1 300); do b=""; for i in $(seq 1 20); do b="$b$up"; done
  tmux send-keys -t $S -l "$b"; sleep 0.2
  tmux capture-pane -t $S -p | grep -q "Execute plan 001" && break; done
tmux capture-pane -t $S -p | grep -n "Execute plan 001"   # 记下行号 ROW（1-based = grep 的行号）
# 点击卡片所在行（列 12），SGR press + release
tmux send-keys -t $S -l "$(printf '\033[<0;12;%sM' ROW)"; tmux send-keys -t $S -l "$(printf '\033[<0;12;%sm' ROW)"
sleep 1.5; tmux capture-pane -t $S -p | tail -1      # 必须含 "viewing 3bc4 · esc to return"
```

若该会话已不存在：`sqlite3 -readonly ~/.forebrain/state/forebrain.state.sqlite "SELECT id FROM fb_sessions ORDER BY updated_at DESC LIMIT 40"`
与 `grep -o '"session_id":"[^"]*"' ~/.forebrain/workspace/state/subagent-history.jsonl | sort | uniq -c` 交叉，
选一个**不是 owner 当前正在用**、且带 subagent 卡片的旧会话，同法打开。

5d. 探针脚本（存到 `$FIX/probe.sh`，`chmod +x`）：

```bash
#!/bin/bash
# probe.sh <tmux-session> <n_wheel> <up|down>: seconds until the probe char echoes in the composer
S=$1; N=$2; DIR=${3:-up}; cb=64; [ "$DIR" = down ] && cb=65
ev=$(printf '\033[<%s;60;10M' $cb); burst=""
for i in $(seq 1 $N); do burst="$burst$ev"; done
# tmux send-keys rejects very long arguments: send in chunks of 300 events
while [ -n "$burst" ]; do tmux send-keys -t $S -l "${burst:0:3600}"; burst="${burst:3600}"; done
tmux send-keys -t $S -l "Z"
t0=$(python3 -c 'import time;print(time.time())')
for i in $(seq 1 3000); do tmux capture-pane -t $S -p | grep -q '^› Z' && break; sleep 0.02; done
t1=$(python3 -c 'import time;print(time.time())')
python3 -c "print('N=%s dir=%s echo_latency=%.2fs' % ('$N','$DIR', $t1-$t0))"
tmux send-keys -t $S BSpace; sleep 0.5
```

（每个事件 `\033[<64;60;10M` 是 12 字节，3600 字节 = 300 个事件。）

5e. 测量（先 subagent 视图，再 Esc 回主视图做对照）：

```bash
for n in 0 200 500 1000; do "$FIX/probe.sh" fb-ledger-fix-probe $n up; done
for n in 200 1000; do "$FIX/probe.sh" fb-ledger-fix-probe $n down; done
tmux send-keys -t fb-ledger-fix-probe Escape; sleep 1
tmux capture-pane -t fb-ledger-fix-probe -p | tail -1     # footer 不再含 "viewing"
for n in 0 1000; do "$FIX/probe.sh" fb-ledger-fix-probe $n up; done
tmux kill-session -t fb-ledger-fix-probe
```

**通过标准**：subagent 视图 `N=1000` 的 `echo_latency` ≤ 0.5 s（修复前 3.97 s），且不超过同次主视图
`N=1000` 数值的 3 倍；`N=0` ≤ 0.2 s。把完整输出原样写进报告。

## Test plan

- 新增 `TestSubagentViewQueueQuestionsAnswerFromTheLiveChannel`（`pkg/run/subagent_test.go`）覆盖：
  账本不可读时预览仍给出 `[one two]`（根因回归）；`SubagentRunning` 仍为真；另一会话同名 task 看不到
  （会话隔离）；未知 task 为空；`RecallSubagentInput` 取回最新一条；`DiscardSubagentInput` 丢弃剩余 1 条。
- `InterruptSubagentToSend`、`WithdrawSubagentInput` 的行为由既有的
  `TestInterruptSubagentToSendRunsThePendingSteersNext`、`TestWithdrawBeforeTheSubagentAnswers`、
  `TestWithdrawnUserExecutionPublishesItsEndSoTheSurfaceRetiresIt` 继续覆盖；gateway 侧由
  `TestSubagentCompactRefusesWhileRunning` 覆盖。**这些既有测试一个都不许改。**
- 结构范本：`TestInterruptSubagentToSendRunsThePendingSteersNext`（`pkg/run/subagent_test.go` 约 4830 行）。

## Done criteria

- [ ] Drift check 为空，或已对照无差异
- [ ] `grep -c "liveSubagentChannel(" pkg/run/subagent.go` 输出 `7`
- [ ] `grep -n "subagentChannelForConversation(" pkg/run/subagent.go` 恰好 2 行（定义 + `SendToSubagent`）
- [ ] `grep -n "subagentChannelsByTask\[" pkg/run/subagent.go` 恰好 2 行（`subagentChannelFor` 写、`liveSubagentChannel` 读）
- [ ] Step 3 的红绿反证都做过，报告里贴出红的那次失败消息
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/gateway ./pkg/tui -count=1` 三行 `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m` 全绿（或仅有「Commands」一节列出的已知干扰，且单独重跑通过）
- [ ] `go vet ./...`、`gofmt -l cmd pkg third_party` 均无输出
- [ ] `scripts/package-graph.sh` 后 graph.json 只有 pkg/run 的 `loc` 变化，`./pkg/architecture` 测试 `ok`
- [ ] Step 5 真机：subagent 视图 `N=1000` ≤ 0.5 s，报告里附 5e 全部输出
- [ ] `git status` 改动面 = Scope 列出的 4 个文件（外加既有的 `pkg/gateway/dist` 删除，未触碰）；未 commit
- [ ] `plans/README.md` 中 002 状态行已更新

## STOP conditions

遇到以下情况立即停下汇报，不要自行发挥：

- Drift check 发现 in-scope 文件变化，且摘录与现行代码对不上。
- Step 3 的「红」那一步没有变红（说明测试没盯住根因，或 `SubagentInputPreview` 还有别的读账本路径）。
- 任何**既有**测试在 Step 2 之后失败（尤其 `pkg/run` 的 subagent 系列和 `pkg/gateway` 的 subagent 系列）：
  不许改既有测试来迁就；把失败输出贴出来汇报。这意味着「无 channel ⇔ 无队列/无执行」或
  「(会话, task) → worker 1:1」的前提在某条路径上不成立。
- 发现某条代码路径会往 subagent worker session 的 `Controller.SessionQueue(...)` 放东西而**不**先经过
  `subagentChannelFor`（核查：`grep -rn "SessionQueue(" pkg --include=*.go | grep -v _test`，
  当前只有 `pkg/run/subagent.go:651` 用 worker session，`pkg/tui/chat_session.go:1034/1081` 用的是会话 session）。
- Step 5 里 subagent 视图 `N=1000` 仍 > 0.5 s：说明还有别的每事件开销。**不要**扩大改动范围；按下面方法取证后汇报：
  在注入一批 1500 个滚轮事件的同时 `sample <forebrain 子进程 pid> 3 -file $FIX/sample.txt`
  （pid 用 `pgrep -P <tmux pane 的 zsh pid>` 拿到 forebrain 子进程，不是 zsh），
  再把 `load address 0x... + 0xOFF` 换算成 `0x100000000+OFF` 后喂给
  `go tool addr2line "$FIX/forebrain"` 还原函数名，贴出 `tui.Run` 下最热的栈。
- 真机验收需要提交消息、调用模型，或需要关闭/影响 owner 已有的 forebrain 进程或 tmux 会话。

## Maintenance notes

- **规则**：凡是 `renderComposerWithState` 里调到的、或每个输入事件都会走到的代码，不得做磁盘/数据库 I/O。
  subagent 视图的 composer 每个事件都重绘一次，任何 O(数据量) 的读取都会被滚轮放大成秒级卡顿；
  新增 subagent 视图的 footer/预览信息时，从 channel 或 renderer 内存状态取。
- 仍走账本的一次性路径（本期刻意不动，留给「会话消息全部入 SQLite」的迁库计划）：
  `SendToSubagent`（视图里按 Enter）、`SubagentComposerTokenStats`/`SubagentContextBudget`（打开视图时一次）、
  `SubagentCompactTarget`（`/compact`）、`pkg/tui/chat_turn.go:712`、`pkg/tui/chat_surface.go:1212`（审批恢复）、
  `pkg/gateway/api_extra.go` 的若干 HTTP 处理。账本只增不减，这些动作各自的 ~30 ms 会随时间变长；
  迁库后由索引查询解决。
- `subagentChannelsByTask` 与 `subagentChannels` 一样随进程常驻、从不清理，数量上界是本进程跑过的 subagent 数。
  若以后给 channel 加回收，必须同时从两个 map 里删。
- 若以后 channel 的键改为不含会话 id，或 task id 不再全局唯一，`liveSubagentChannel` 的会话隔离要重新论证。
- 本期未处理、已被主视图对照排除为非根因的项（如需更好手感可另立计划）：活动回合循环
  `runTurnWithSkill` 不合并滚轮事件、每个滚轮事件都重绘 composer；输入读入管线逐字节读取 + ack 握手。
- Review 重点：`subagentChannelFor` 的登记放在 `if ch == nil` 块之外；6 个函数的返回值在 nil 分支与旧的
  err 分支完全一致；`SendToSubagent` 未改。
