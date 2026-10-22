# Plan 099：一次屏幕上，同一张错误卡最多画一次；重放接管整块屏幕（`/rewind` 不再把整段历史再贴一遍）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 81a770e -- pkg/tui/reducer.go pkg/tui/notify.go pkg/tui/commands.go pkg/tui/run.go`
> 预期**无输出**。有输出就对照 "Current state" 的摘录，不符即 STOP 1。
> **依赖**：本计划自带 `resetStreamTranscript`（`pkg/tui/run.go`，与 `switchStreamSession` 相邻）。若该函数已存在
> （owner 2026-10-22 撤掉了 `/clear` 清屏那份计划，但 helper 随本计划保留），按本计划的写法核对它是否已有
> `ScreenShowsOnlyBanner` 守卫；没有就补上，**不要**另写第二份清屏逻辑。
>
> 工作区里本来就有 owner 大量未提交改动（`frontend/**`、`pkg/gateway/dist/**`、`pkg/llm/**`、
> `pkg/tui/chat_session_test.go`、`pkg/tui/render_test.go`、`plans/096-*.md`、`scripts/acceptance/**`），
> **一律不动、不回滚**，判断自己改了什么用 `git diff --stat 81a770e -- pkg/tui/`。

## Status

- **Priority**: **P1**（owner 2026-10-22 裁决：「加入本期计划，彻底修复」——不只修 097 的归属问题，
  还要把「同一条事实被 live 与重放各画一次」这类重复彻底堵死）
- **Effort**: M（`Frame`/`Message` 各加 1 个字段；renderer 加 1 个 map + 1 道闸门；重放的 1 个接缝；`/rewind` 接入清屏）
- **Risk**: MED（动的是 `appendFrameViewportLocked` 这个全部绘制都会经过的窄口；闸门只对带稳定 ID 的 `FrameError` 生效，其余帧一字不改）
- **Depends on**: none
- **Category**: bug（重复渲染 / 界面事实不一致）
- **Planned at**: commit `81a770e`，2026-10-22
- **Status**: **已在工作区实施（2026-10-22），未提交**。证据：`go build ./...` = 0；`go vet ./pkg/tui/` = 0；
  新增 `TestOneErrorCardPerScreenWhenLiveAndReplayBothDrawIt`、`TestReplayStampsItsCardsWithTheEventIdentity`、
  `TestReplayReplacesTheScreenInsteadOfAppendingToIt`、`TestReplayToleratesAnUnwiredReset` 通过；
  `pkg/tui` 全包 67.9s `ok`，全仓 `go test -tags fts5 ./... -count=1` 零失败。
  **实现期修正一处**：重放循环里的 `msg` 是 `any`（`replaySubagentRunEventMessage` 的返回类型），Step 3b 的赋值要写成
  `if m, isMessage := msg.(Message); isMessage { m.EventID = eventID; msg = m }`。

## Review（advisor 复核，2026-10-22，`81a770e` 基线的工作区）

- **Verdict: APPROVE**。代码在上一轮直接落在 dev 工作区（owner 规则禁止 worktree / 新分支），因此本次 `execute`
  只执行评审半段：逐条复跑 done criteria + 查 scope + 读 diff + 审测试断言。**未提交**，提交由 owner 决定。
- **criteria 复跑**（全部实测，非执行者自报）：`go build ./...` = 0；`go vet ./pkg/tui/` = 0；
  `go test -tags fts5 ./pkg/tui/ -count=1 -run '<本计划点名的测试>'` 全 PASS；
  `env -u CGO_LDFLAGS -u CGO_CFLAGS PKG_CONFIG_PATH=<pdfium> go test -tags fts5 ./... -count=1` → **零 FAIL**
  （等价于 `make test` 的 go 测试部分；`make test` 额外跑的 pdfium/fonts 取件目标未跑，已确认二进制已在位）。
- **scope**：`git diff --name-only 81a770e -- pkg/run pkg/state pkg/turn` 为空。
  `pkg/tui/` 之外另有 5 个脏文件（`pkg/gateway/dist/index.html`、`pkg/llm/explain.go`、`pkg/llm/explain_test.go`、
  `pkg/llm/openai/error_test.go`、`pkg/llm/ratelimit_test.go`），会话开始前就已修改，与本计划无关，未触碰。
- **测试断言审读**：新测试都具备可证伪性 —— 去掉闸门则 `TestOneErrorCardPerScreenWhenLiveAndReplayBothDrawIt`
  的块数断言变 2；去掉接缝盖章则 `TestReplayStampsItsCardsWithTheEventIdentity` 读到的 `EventID` 为空；
  去掉 `resetScreen` 调用则 `TestReplayReplacesTheScreenInsteadOfAppendingToIt` 里第二次重放会让块数翻倍。
  无「断言恒真」的空测试。
- **评审期修正**：done criteria 里三处与实现不符的表述已就地改正（`ownsMCPGeneration` 已合并、逐计划 diff 面、
  `screenCards`/`EventID` 的真实落点数）—— 这些是计划文本的缺陷，不是代码缺陷。

## Why this matters

097 只解决「失败被记进别人的会话」。**同一个会话里**仍有两条重复路径，本计划负责堵死它们：

1. **live 卡与重放卡同屏并存。** 一条错误事件既被 live 路径画成卡（`publishRunEvent` → `notifyTurnError`
   → 主循环队列），又会被任何一次重放从库里读出来再画一遍。两者之间**没有任何共同的身份判据**：
   `markEventShownOnce`（`notify.go:1035-1055`）只记录「这个 surface 发过这张卡」，重放路径从不查它，
   也不写它；而 `Renderer` 保留的 block 上根本没有事件身份（`Frame` 只有 `StepID`/`RunID`，`reducer.go:68-125`）。
   于是只要两者落在同一屏，就是两张卡 —— owner 截图里的那两张正是这么来的（`resumed` 之上是重放画的，
   之下是队列里那张 live 卡回 idle 后画的）。
2. **`/rewind` 把整段历史又贴一遍。** `handleRewind` 的两个成功分支（`commands.go:2408`、`commands.go:2481`）
   直接 `c.replaySession(sid, nil)`，而重放**从不清屏**（`commands.go:83-171` 只有 `BeginBatch`/`EndBatch`，
   没有 `ViewportResetSession`）。切会话那两条路径之所以看不到重复，全靠调用方 `switchStreamSession`
   事先清了屏（`run.go:1815`）；`/rewind` 没有那个调用方，于是重放是**追加**在一屏旧内容之上。

修完之后的契约，两条都是可机的：

- **同一屏上，同一条错误事件最多一张卡**（判据是同一次「屏幕世代」，见 Step 4）。
- **重放即替换屏幕**：重放跑完，屏上只有 banner + 库里那一段 + 重放自己的提示帧，不叠任何旧内容。

`/rewind` 因此顺带修好一个此前没人报的既有缺陷（rewind 后整段 transcript 视觉上翻倍）；这不是范围扩张，
它是同一个「重放追加而不替换」的根因。

## Current state（行号与摘录均于 `81a770e` 逐行核过）

### 1. 两条路径画同一张错误卡，没有任何共同身份

live（`pkg/tui/notify.go:1061-1073`）：

```go
func (s *ChatSession) notifyTurnError(evt event.RunEvent) {
	var p event.TurnErrorPayload
	if json.Unmarshal(evt.Payload, &p) != nil {
		return
	}
	text := turnErrorDisplayText(p)
	if text == "" {
		return
	}
	s.notifyUIForSession(evt.SessionID, NewMessageMsg{Msg: Message{
		Kind: MsgKindError, Content: text, Title: p.Title, Timestamp: evt.CreatedAt,
	}})
}
```

注意 `Message` **没有**任何事件身份字段（`notify.go:43-62`），`evt.ID` 在这里被丢掉了。

重放（`pkg/tui/commands.go:640-657`）：

```go
func replayTurnErrorEventFrames(evt event.RunEvent) []Frame {
	...
	return []Frame{{Kind: FrameError, Title: title, Content: text, Final: true}}
}
```

同样没有 `evt.ID`。`Frame`（`pkg/tui/reducer.go:68-125`）里也没有可放它的字段。

### 2. 全部绘制都经过 `appendFrameViewportLocked`，且那里没有去重

`pkg/tui/reducer.go:5236-5246`：

```go
func (r *Renderer) appendFrameViewportLocked(f Frame) {
	visible := r.activeVMLocked()
	blocksBefore := len(visible.blocks)
	repaint := r.retainFrameLocked(f)
	r.countNewMessageLocked(f, visible, blocksBefore)
	if repaint {
		r.paintViewportLocked()
	}
}
```

`RenderFrame`（`render.go:686-690`）只做加锁 + 调它。live 与重放都从这里进。

### 3. 重放从不替换屏幕

`pkg/tui/commands.go:83-167`（`replaySession`）：`c.renderer.BeginBatch()`（`:132`） → `replayTimelineWithWorkers` →
`flushReplayReducer` → `EndBatch()`。**全程没有** `ViewportResetSession`。
`ViewportResetSession` 的唯一调用方是 `switchStreamSession`（`run.go:1815`）。

`/rewind` 的调用方（`commands.go:2403-2409` 撤销分支、`commands.go:2479-2482` 应用分支）直接
`c.replaySession(sid, nil)`，没有切会话，因此**没有清屏**。

### 4. 重放的 event 循环是唯一需要盖身份的接缝

`pkg/tui/commands.go:392-473`：每个 `evt` 产出一个 `Message`（`msg`）和一串 `Frame`（`frames`），
最后压进 `pending[pos]`：

```go
		pending[pos] = append(pending[pos], replayEventItem{sequence: sequence, message: msg, frames: frames})
```

`replayEventItem` 同时带 `message Message` 与 `frames []Frame`（定义在 `commands.go:473` 的调用点上方），
所以**一个赋值点**就能把两者都盖上同一个 `evt.ID`。

### 5. `ViewportResetSession` 已经是「屏幕世代」的唯一分界

`pkg/tui/reducer.go:5867-5889`（本计划在它里面加一行清表）：

```go
func (r *Renderer) ViewportResetSession() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return
	}
	r.vm.reset()
	...
```

`pkg/tui/reducer.go:3761-3790` 的 `EnableViewportMode` 是另一处「屏幕从空开始」的点。

### 6. 既有测试钉住的语义（本计划不能破坏）

`pkg/tui/notify_test.go`：`TestStoredMCPFailureReplaysWithItsHeadingAndText`（新 renderer 上重放 1 条 → 1 块）、
`TestMCPStartupFailureBeforeTheSessionExistsIsShownOnce…`、`TestCancelledServerStartupIsNotAFailure`
（无文本的 payload 不产生 frames）、`TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored`。

## Commands you will need（本机已实跑验证）

> **跑测试的正确写法**：只设 `PKG_CONFIG_PATH`，**不要**再覆盖 `CGO_LDFLAGS`/`CGO_CFLAGS`。覆盖会丢掉 pkg-config 里的
> `-Wl,-rpath`，让**不引用 pdfium 的**测试二进制也链上 `libpdfium.dylib` 却找不到 rpath，于是 `pkg/event`/`pkg/state`/
> `pkg/testutil` 等一大批包报 `Library not loaded: @rpath/libpdfium.dylib` —— 看起来像代码坏了，其实是命令行写法坏了。
> 本机已实测：只给 `PKG_CONFIG_PATH` 时全仓零失败。

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `go build ./...` | exit 0（只有 `libtokenizers` 的 `ld: warning: ... newer 'macOS' version` 既有噪声） |
| 聚焦测试 | `P=$PWD/third_party/pdfium/darwin_arm64 && env -u CGO_LDFLAGS -u CGO_CFLAGS PKG_CONFIG_PATH="$P" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1 -run 'TestError|TestReplay|TestMCP'` | `ok  .../pkg/tui` |
| 全量测试 | `make test` | 各包 `ok` |
| 静态检查 | `go vet ./pkg/tui/` | exit 0 |

## Scope

**In scope**（只允许改这五个文件）：

- `pkg/tui/reducer.go` —— `Frame.EventID`、`buildFrame` 透传、renderer 的「本屏已画」表 + 闸门 + 两处清表。
- `pkg/tui/notify.go` —— `Message.EventID`、`notifyTurnError` 盖上 `evt.ID`。
- `pkg/tui/commands.go` —— 重放接缝盖身份；`replaySession` 替换屏幕。
- `pkg/tui/run.go` —— 把 `cmds.resetScreen` 接上。
- `pkg/tui/notify_test.go`（或新建 `pkg/tui/replay_screen_test.go`）—— 测试。

**Out of scope**（看着相关，但**不要**动）：

- `pkg/run/**`、`pkg/state/**`：事件身份的产生与落库不动。
- 非 `FrameError` 的帧（approval、goal、migration、compaction、status、tool）：它们要么本来就原地更新
  （tool 卡按 `StepID` 覆盖），要么同一屏上重复出现是合法的（两次 approval 两行确认）。**不要**给它们套本计划的闸门。
- `pkg/tui/run.go` 里 `switchStreamSession` 的清屏：它在 `/new` 这类没有重放的路径上仍然必需。
- `markEventShownOnce`（`notify.go:1035-1055`）：它管的是「发布侧同一事实只发一次」，语义正确，不动；
  本计划补的是「绘制侧同一屏只画一次」，两者互补。
- `handleRewind` 的交互与 `turn.Rewind`：只经由 `replaySession` 获得清屏，不改进程。
- `/clear` 的现有行为（**owner 2026-10-22 裁决：保持现状**，只清模型上下文、不清屏，也不在运行中拒绝）——
  本计划**不碰** `/clear`，`dispatchStreamSlashCommand` 里不得为它新增分支。

## Git workflow

- 不建分支、不用 worktree，直接在 dev 工作区改。
- **不 commit、不 push、不开 PR**：owner 手动提交，中文 Conventional Commit。

## Steps

### Step 1：给 `Frame` 与 `Message` 各加一个事件身份

`pkg/tui/reducer.go` 的 `type Frame struct`（`:68`）里、`RunID` 之后加：

```go
	// EventID is the stable identity of the run event this frame was built
	// from, when it has one (an error event's derived id, for instance). Empty
	// for everything a surface invents locally, and for transcript rows, which
	// have no event. It is what lets a frame painted live and the same frame
	// painted again by a replay be recognized as one card (see
	// retainCardOnceLocked).
	EventID string
```

`pkg/tui/notify.go` 的 `type Message struct`（`:43`）里、`RunID` 之后加同义字段（注释同样说明用途）。

`pkg/tui/reducer.go:2090-2113` 的 `buildFrame` 里补一行 `EventID: strings.TrimSpace(msg.EventID),`。

**Verify**：`go build ./...` → exit 0。

### Step 2：live 路径把事件身份带上

`pkg/tui/notify.go:1061-1073` 的 `notifyTurnError`：

```go
	s.notifyUIForSession(evt.SessionID, NewMessageMsg{Msg: Message{
		Kind: MsgKindError, Content: text, Title: p.Title, Timestamp: evt.CreatedAt,
		EventID: strings.TrimSpace(evt.ID),
	}})
```

（只此一处。其余错误消息 —— `chat_session.go:867`、`:1506`、`:1639`、`chat_turn.go:912`/`:1029` ——
不是由 run event 产生的，没有可复用的身份，留空即可，闸门对空身份不生效。）

**Verify**：`go build ./...` → exit 0。

### Step 3：重放接缝盖上同一个身份（两处）

**3a. 错误帧自己的构造点**（必须单独做：一个 run 正常收尾时，它的 `turn_error` 会被
`closingRuns` 分支（`commands.go:394-400`）提前塞进 `runEndFrames` 并 `continue`，**根本走不到**下面那句
`pending[pos] = append(...)`，漏了它这条路径就没有身份）。`pkg/tui/commands.go:655` 的
`return []Frame{{Kind: FrameError, Title: title, Content: text, Final: true}}` 改成：

```go
	return []Frame{{Kind: FrameError, Title: title, Content: text, EventID: strings.TrimSpace(evt.ID), Final: true}}
```

**3b. 通用接缝**：`pkg/tui/commands.go:473` 那句 `pending[pos] = append(...)` **之前**加：

```go
		// One event, one identity: the message and every frame rebuilt from it
		// carry the event's own id, so the card this replay draws is
		// recognizable as the same card the live path already painted (and
		// vice versa) instead of a second copy of it.
		eventID := strings.TrimSpace(evt.ID)
		msg.EventID = eventID
		for i := range frames {
			if strings.TrimSpace(frames[i].EventID) == "" {
				frames[i].EventID = eventID
			}
		}
```

同一循环里那条 `replayEventItem{sequence: sequence, message: injected}`（worker transcript 注入，`commands.go:468`）
**不要**盖身份：它是子代理自己的话，不是事件卡。

**Verify**：`go build ./...` → exit 0；`grep -n "msg.EventID = eventID" pkg/tui/commands.go` → 1 处；
`grep -n "EventID: strings.TrimSpace(evt.ID)" pkg/tui/commands.go` → 1 处（3a）。

### Step 4：绘制闸门 —— 一屏之内同一张错误卡只画一次

`pkg/tui/render.go` 的 `Renderer` 结构里（照 `vm` 那些字段的写法，加在 viewport 段内）加：

```go
	// screenCards remembers which event-backed cards this screen has already
	// painted, keyed by event id and the view the card belongs to. It is the
	// drawing half of "one fact, one card": the live path and a replay can both
	// ask for the same card, in either order, and only the first request that
	// lands on this screen paints. It is deliberately cleared together with the
	// screen (ViewportResetSession, EnableViewportMode): after a reset the cards
	// on screen are gone, so a replay must be able to draw them again.
	screenCards map[string]struct{}
```

`pkg/tui/reducer.go` 里、`appendFrameViewportLocked`（`:5236`）**之前**加：

```go
// retainCardOnceLocked reports whether this frame may still be painted on the
// current screen. Only an error frame carrying an event id participates: those
// are the cards whose duplicate is never legitimate — one run event, one card —
// while every other kind either updates in place (a tool card is replaced by
// its completed frame) or repeats on purpose (two approvals are two
// confirmations). A frame with no event id, and every non-error frame, passes
// untouched. Caller holds r.mu.
func (r *Renderer) retainCardOnceLocked(f Frame) bool {
	id := strings.TrimSpace(f.EventID)
	if id == "" || f.Kind != FrameError {
		return true
	}
	key := id + "\x00" + strings.TrimSpace(f.AgentID)
	if _, seen := r.screenCards[key]; seen {
		return false
	}
	if r.screenCards == nil {
		r.screenCards = make(map[string]struct{})
	}
	r.screenCards[key] = struct{}{}
	return true
}
```

`appendFrameViewportLocked` 的第一行改成闸门：

```go
func (r *Renderer) appendFrameViewportLocked(f Frame) {
	if !r.retainCardOnceLocked(f) {
		return
	}
	visible := r.activeVMLocked()
	...
```

（闸门放在 `retainFrameLocked` 之前，所以「全局错误镜像进当前 subagent 视图」这条既有行为不受影响：
同一次 `RenderFrame` 调用里的两份仍然都会画，被挡掉的只有**第二次调用**。）

**Verify**：`go build ./...` → exit 0；`go vet ./pkg/tui/` → exit 0。

### Step 5：屏幕重置时清空「本屏已画」

- `pkg/tui/reducer.go:5867` 的 `ViewportResetSession`：在 `r.vm.reset()` 旁加 `r.screenCards = nil`。
- `pkg/tui/reducer.go:3761` 的 `EnableViewportMode`：在 `dropPaintShadowLocked()` 旁加 `r.screenCards = nil`。

**Verify**：`grep -n "screenCards = nil" pkg/tui/reducer.go` → 2 处。

### Step 6：重放接管整块屏幕（修 `/rewind` 的整段重复）

1. `pkg/tui/commands.go` 的 `commandController` 结构体里加一个钩子（照 `openPanel` 的写法）：

```go
	// resetScreen, when wired by the main loop, replaces everything the
	// terminal is showing with the chrome a fresh screen opens with. A replay
	// appends otherwise: /resume looked right only because switchStreamSession
	// had just cleared the screen, and /rewind — which has no such caller —
	// pasted the whole transcript on top of the one already there.
	resetScreen func()
```

2. `replaySession`（`commands.go:87`）：在「没有东西可重放」的早退（`commands.go:123`，`if len(turns) == 0 && len(events) == 0 { return }`）
   **之后**、`c.renderer.BeginBatch()`（`commands.go:132`）**之前**加：

```go
	// A replay replaces the screen rather than adding to it: what is on screen
	// now is either the same conversation drawn from a store that has since
	// changed (/rewind) or another conversation entirely (/resume). Replaying
	// above it doubled the transcript and the cards in it.
	if c.resetScreen != nil {
		c.resetScreen()
	}
```

3. `pkg/tui/run.go` 里 `cmds.openPanel = ...`（`:199-220`）旁接线：

```go
	cmds.resetScreen = func() {
		resetStreamTranscript(renderer, &state)
	}
```

注意 `state` 是 `Run` 里的局部变量，闭包捕获它没问题（与 `openPanel` 里 `openStatusPanel(&state)` 同一写法）。

4. `resetStreamTranscript` 加一条「已经空了就不动」的守卫，避免 `switchStreamSession` 刚清完、
   紧接着的重放再清一次（同一主循环迭代内两次全屏重绘）：

```go
	if renderer == nil || state == nil || !renderer.viewportMode || renderer.ScreenShowsOnlyBanner() {
		return
	}
```

配套在 `pkg/tui/reducer.go` 加（`ViewportResetSession` 附近）：

```go
// ScreenShowsOnlyBanner reports whether the retained transcript holds nothing
// but the welcome banner — the state a screen is in immediately after a reset.
// It is how a caller that would reset again (a replay right after a session
// switch) avoids a second full repaint of a screen that is already empty.
func (r *Renderer) ScreenShowsOnlyBanner() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.viewportMode {
		return false
	}
	if len(r.vm.blocks) != 1 {
		return false
	}
	return r.vm.blocks[0].frame.Kind == FrameBannerInfo
}
```

**Verify**：`go build ./...` → exit 0；`grep -n "c.resetScreen()" pkg/tui/commands.go` → 1 处；
`grep -n "cmds.resetScreen = func" pkg/tui/run.go` → 1 处。

### Step 7：测试（见 Test plan）

**Verify**：聚焦测试命令 → `ok`。

## Test plan

写进 `pkg/tui/notify_test.go`（该文件已有 `sessionEnv`、`requireUIMessage`、`waitForQueuedNotifications`、
`mcpFailureRecord`；viewport 断言照 `TestStoredMCPFailureReplaysWithItsHeadingAndText` 用
`renderer := NewRenderer(nil, nil); renderer.viewportMode = true` 然后读 `renderer.vm.blocks`）。

1. **`TestOneErrorCardPerScreenWhenLiveAndReplayBothDrawIt`**（本计划的正题）
   - `renderer`（viewport）+ `reducer := &Reducer{}`。
   - 先「live」画一次：`renderer.RenderFrame(Frame{Kind: FrameError, Title: "mcp docs", Content: "connection refused", EventID: "mcp:docs:abc:error", Final: true})` → 1 块。
   - 再让**真实的重放**画同一个事件（走 Step 3 的接缝，不要手工设字段）：
     `evt := event.RunEvent{ID: "mcp:docs:abc:error", Sequence: 1, Type: event.RunEventTurnError, Payload: json.RawMessage(`{"error":"connection refused","title":"mcp docs","source":"mcp"}`)}`；
     `replayTimelineWithWorkers(renderer, nil, []event.RunEvent{evt}, &Reducer{}, nil, nil)` → **仍然只有 1 块**，且内容还是第一次那张。
   - **反向**（重置后必须能重画，否则 `/resume` 会丢历史里的失败卡）：
     `renderer.ViewportResetSession()`；再 `renderer.Banner(StartupInfo{})` 之外只跑一次
     `replayTimelineWithWorkers(renderer, nil, []event.RunEvent{evt}, &Reducer{}, nil, nil)` → 这一次它被画出来了
     （断言存在一个 `frame.Kind == FrameError && frame.EventID == evt.ID` 的块）。
2. **`TestReplayLayerStampsEveryFrameAndMessageWithItsEventID`**
   - 构造一个只含一条 `turn_error` 事件的 `[]event.RunEvent`，`renderer`（viewport）+ 空 `turns`；
     `replayTimelineWithWorkers(renderer, nil, events, &Reducer{}, nil, nil)`。
   - 断言画出来的那 1 块的 `frame.EventID == evt.ID`（证明 Step 3 的接缝真的盖上了；这条断言就是接缝的守卫）。
3. **`TestReplayReplacesTheScreenInsteadOfAppendingToIt`**
   - `renderer`（viewport）；先画两条「上一屏」的块（`FrameTool` ×2，无 `EventID`）。
   - `cmds := newCommandController(session, renderer, &stubSelector{}, nil, "")`，`cmds.replaySession("s1", nil)`，
     其中 `session` 的 `SurfaceTranscriptMessages` 返回 1 行 assistant 文本、`SurfaceSessionEvents` 返回空
     （照 `TestStoredMCPFailureReplaysWithItsHeadingAndText` 里 `sessionEnv{session}` 的搭法，必要时补一个最小 fake）。
   - 断言：**那块旧内容不在了**，屏上只有 banner + 重放的那条（块总数 = 2，且没有任何块的 Content 是旧内容）；
     并且 `cmds.resetScreen` 被接上时确实被调用过一次（`cmds.resetScreen = func() { calls++ }`）。
   - 再一个子用例：`resetScreen == nil` 时不得 panic（非 TUI/测试装配路径）。
4. **`TestRewindReplayDoesNotDoubleTheTranscript`**（`/rewind` 的回归，若 3 已覆盖同一机制可合并，但必须有一条
   以 `rewind` 命名或用例说明，便于将来对照）
   - 同一 renderer 上连续两次 `cmds.replaySession("s1", nil)`，第二次之前**不**清屏（模拟 `/rewind` 的旧调用形状是
     不可行的，因为 Step 6 让重放自己清屏）：断言两次之后块数不变（第一次 N，第二次仍是 N），不是 2N。
4b. （实测落位）第 4 条与第 3 条**合并**为 `TestReplayReplacesTheScreenInsteadOfAppendingToIt`：它的文档注释写明了
   /rewind 的来源（`/rewind` 没有 `switchStreamSession` 替它清屏），并且第二次重放断言块数不增 —— 这就是第 4 条要钉的
   同一机制，不另立一条同义测试。

5. **既有测试必须保持绿**：`TestStoredMCPFailureReplaysWithItsHeadingAndText`、
   `TestMCPStartupFailureBeforeTheSessionExistsIsShownOnce…`、`TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored`、
   `TestCancelledServerStartupIsNotAFailure`。任何一个变红 → STOP 4。

**真机验收（owner 侧）**：TUI 里等 `mcp caveman-shrink` 错误卡出现 → `/resume` 到任一历史会话：
错误卡**只出现一次**；再 `/rewind`（选一条消息、只恢复对话）：屏上只剩 banner + 回退后的历史 + rewind 提示，
**看不到整段重复**；`/clear` 之后 `/resume` 同一会话：历史完整重放（`/clear` 不清屏、不删历史，行为保持现状）。

## Done criteria（全部必须成立）

- [ ] `go build ./...` → exit 0；`go vet ./pkg/tui/` → exit 0
- [ ] 聚焦测试：`P=$PWD/third_party/pdfium/darwin_arm64 && env -u CGO_LDFLAGS -u CGO_CFLAGS PKG_CONFIG_PATH="$P" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1 -run 'TestError|TestReplay|TestRewind|TestMCP'` → `ok`
- [ ] `make test` → 全部 `ok`
- [ ] `EventID` 六个落点全在（实测行号，随改动漂移）：`reducer.go:92`（`Frame.EventID`）、`notify.go:61`（`Message.EventID`）、
      `reducer.go:2118`（`buildFrame` 透传）、`notify.go:1077`（`notifyTurnError`）、`commands.go:688`（3a
      `replayTurnErrorEventFrames`）、`commands.go:492`/`:496-497`（3b 通用接缝）
- [ ] `grep -n "screenCards" pkg/tui/render.go pkg/tui/reducer.go` → 定义 1（`render.go:276`）+ 清表 2（`reducer.go:3795`
      `EnableViewportMode`、`reducer.go:5930` `ViewportResetSession`）+ 闸门与初始化 4（`reducer.go:5286/5289/5290/5292`）
- [ ] `git diff --stat 81a770e -- pkg/tui/reducer.go pkg/tui/notify.go pkg/tui/commands.go pkg/tui/run.go` 只列这四文件（+ 测试文件）
- [ ] 未提交

## STOP conditions

- **STOP 1**：Drift check 有输出，且 `appendFrameViewportLocked` / `replaySession` / `notifyTurnError` 与摘录结构不符。
- **STOP 2**：`run.go` 里的 `state` 不再是 `Run` 的局部变量（`cmds.resetScreen` 的闭包捕获不到它）。
- **STOP 3**：你发现 `/rewind` 其实通过别的路径清过屏（例如 `turn.Rewind` 内部调用了 renderer）——那说明"重放追加"
  这个前提在你的基线上不成立，停下来报告，不要为了凑测试去改 `turn`。
- **STOP 4**：Test plan 第 5 组（既有 4 条 MCP/重放测试）变红，且不是测试自身笔误。
- **STOP 5**：为了让重复消失，你需要给**非 error** 的帧加去重（approval/goal/migration/tool）→ 越界：那些帧重复是合法的，
  说明真正的重复源还没找到，报告。
- **STOP 6**：需要动 `pkg/run/**`、`pkg/state/**`，或需要改 `/clear` 的行为 → 越界（`/clear` 保持现状是 owner 的裁决）。

## Maintenance notes

- **两条重放路径都要盖身份。** 一个 run 的 `turn_error` 有两种画法：多数事件按位置插进 `pending[pos]`，
  而**正常收尾**的 run 的最后一个错误会被 `closingRuns` 提前收进 `runEndFrames`，画在 "Worked for" 之前。
  只在通用接缝盖章会漏掉后者 —— 所以 3a 必须做。将来若再加一种「错误帧的另一个出口」，同样要在构造点盖章。
- **为什么闸门只管 `FrameError`。** 错误卡是唯一「一条事件 = 一张卡、同屏两张必定是错的」的类别。
  tool 卡靠 `StepID` 原地替换、approval 的两行确认是两次不同事件、status/goal/migration 各自有锚点规则 ——
  给它们套「一屏一次」会静默吞掉合法输出。将来若出现**别的**「一条事件一张卡」的类别（例如 image refusal 卡），
  做法是给它盖上 `EventID` 并把它加进 `retainCardOnceLocked` 的判断，**不要**放宽成「凡有 EventID 就去重」。
- **闸门的粒度是「一次屏幕」。** 分界点就是 `ViewportResetSession`（切会话与每次重放都会经过它）
  与 `EnableViewportMode`（进程内第一次进入 alt-screen）。非 viewport（管道/非 TTY）路径不参与去重，因为它们没有保留的屏幕。
- **reviewer 要盯的三点**：① 闸门在 `retainFrameLocked` **之前**，所以「全局错误镜像进当前 subagent 视图」这条既有行为
  必须仍然成立（同一次调用画两份）；② `AgentID` 参与 key —— 同一个错误事件在会话视图与子代理视图各一份是合法的；
  ③ `ScreenShowsOnlyBanner` 只读 `len(blocks)==1 && Kind==FrameBannerInfo`，不要让它在非 viewport 下返回 true。
- **历史数据不清理**（owner 2026-10-22 裁决）：库里已有的 `mcp:%` 行原样保留；它们此后只会各自画出一次。
- **`markEventShownOnce` 与本闸门的关系**：前者是发布侧（"这条事实我发过没有"），后者是绘制侧（"这张卡这块屏上有没有"）。
  两者不合并：合并会让「重放时屏幕已清空、必须重画」这条变成不可能。
- **未来会与它交互的改动**：任何在重放之外「整屏换内容」的新命令都要走 `resetStreamTranscript`（或直接
  `ViewportResetSession`），否则 `screenCards` 会把新屏幕上的第一张错误卡挡掉——这是本设计唯一的新失败模式，
  也是 `ScreenShowsOnlyBanner` 那类测试值得保留的原因。
