# Plan 097：MCP 启动失败只记在「看着它启动的那个会话」名下（/resume、/new 不再凭空多出、重复画出错误卡）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 81a770e -- pkg/tui/notify.go pkg/tui/chat_session.go pkg/tui/notify_test.go`
> 预期**无输出**。有输出就对照下面 "Current state" 的行号与摘录：不符即 STOP 1。
> （工作区里 `frontend/e2e/*`、`ai-elements-vue/*` 的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: **P1**（owner 2026-10-22 报障：TUI 启动后出现一条 mcp 错误卡，`/resume` 切到历史会话后**同一张卡出现两次**，见 `plans/README.md` 最新审核信息）
- **Effort**: S（`pkg/tui/notify.go` 加 ~35 行 + 两个字段；删掉一段误记逻辑的放行条件）
- **Risk**: LOW（只改「失败归属谁」这一判据；MCP 自身启动、状态行、错误文本、事件 ID 全不动）
- **Depends on**: none（`plans/099-one-error-card-per-screen.md` 依赖本计划，但本计划可独立落地）
- **Category**: bug（数据正确性 + 渲染重复）
- **Planned at**: commit `81a770e`，2026-10-22
- **Status**: **已在工作区实施（2026-10-22），未提交**。实现与下面的稿子有两处偏差，均已按实测改好并记在
  Step 1/3 里：① 闸门改成 `recordMCPStartupFailures` 内部「先认领、再比对」（原稿的 `ownsMCPGeneration` 会让
  「会话行还不存在时先看到失败」这条既有语义失效 —— 既有的 `TestMCPStartupFailureBeforeTheSessionExists*` 正是它）；
  ② 归属槽位从一个变两个（迟到的上一世代 snapshot 不能被当成新世代重新认领）。
  证据：`go build ./...` = 0；`go vet ./pkg/tui/` = 0；`pkg/tui` 全包 67.9s `ok`；全仓 `go test -tags fts5 ./... -count=1`
  零失败；新增测试 `TestMCPFailureBelongsToTheConversationThatSawItsGenerationStart`、
  `TestNewMCPGenerationBelongsToWhoeverSeesItStart` 通过，既有 MCP 5 条测试不变绿→绿。

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

**runner 级的 MCP 启动失败，被记到了「此刻打开的那个会话」名下。**

一个进程只有一个 `run.Runner`（`pkg/tui/chat_session.go:632-637` 返回 `s.Env.Runner`），
MCP 启动世代（generation）挂在它身上，且**只在 MCP 配置指纹变化时重建**
（`pkg/run/runner.go:2251-2270` 的 `startOrReplayMCPSegmentLocked`；`runner.go:905-910` 的注释明说
"a reload that leaves the list alone keeps the live sessions"）。因此**换会话、换模型都不会重启 MCP**：
`/resume`、`/new` 看到的失败永远是**之前就存在的那个世代留下的既定失败**。

但 TUI 在每次切会话时把 MCP watch 重挂到新会话上（`pkg/tui/run.go:1762-1770`），
而 `subscribeMCPStatus` 在订阅时会**同步重放当前 snapshot**（`pkg/run/runner.go:2838-2846`，含
`ConnStatusError` 的记录）。于是 `recordMCPStartupFailures(next, 既定失败)`（`pkg/tui/notify.go:1287`）
把这个**不属于该会话**的失败：

1. **写进该会话的历史**：`publishRunEvent` → `AppendSessionEventOnce` 落一行 `turn_error`
   （`notify.go:794-837`）。历史因此说谎——这条失败发生在这个会话存在之前。
2. **画一张卡**：同一路径把 `NewMessageMsg{Kind: MsgKindError, Title: "mcp <server>"}` 投进主循环队列
   （`chat_session.go:991-1013` 的异步派发）。

两条后果都是 owner 看到的现象：

- **重复两次**：卡片先入队（step 1 的插入在同一瞬间完成），切会话随后继续走到
  `ViewportResetSession()`（`run.go:1815`，清屏本身没问题）→ `printSessionResumeContext`
  （`commands.go:63-76`）→ `replaySession` 读库，**已经读到刚插那行**（无 `RunID` 的事件在重放里落到末尾）
  → 画在 "resumed" 之上；主循环回到 idle 后队列里那张 live 卡被投递 → 画在 "resumed" 之下。
  `publishRunEvent` 的 `!inserted → 不画` 去重挡不住这条路径：**这一行就是它自己插的**。
- **`/new` 出来的空会话立刻带着一张 mcp 错误卡**：`/new` 先 `Ensure` 出会话行（`pkg/turn/executor.go:314`），
  再切过去，于是插入成功、卡片照画。

**实测证据（owner 本机 state DB 副本，2026-10-22）**：`2026-10-22 01:07:04Z–01:09:36Z` 的 **2.5 分钟**内，
**6 个不同会话**各得到一条**完全相同**的 `fb_session_events` 行
（`event_id = mcp:caveman-shrink:9a948827be19:error`，payload 同为 "startup timed out after 30s: context deadline exceeded"），
其中 5 个会话 `fb_messages` 为 **0 条**（就是 `/new` 出来的空会话）。全库没有任何会话拥有两条 mcp 行——
即每个会话被污染一次（事件 ID 稳定，之后被 dedupe 挡住）。这正说明：**失败在跨会话流动，而不是某个会话真的失败了两遍。**

修完之后的契约：**一个 MCP 启动世代的失败，只属于「第一次看到这个世代启动的那个会话」**；
别的会话切进来时，失败既不写进它的历史，也不画进它的屏幕（它仍能从 runner 的状态行看到启动/失败状态，
那条线是瞬时的、不落历史，见 `MCPStatusMsg` 的定义 `notify.go:1202-1207`）。

## Current state（行号与摘录均于 `81a770e` 逐行核过）

### 1. 一个进程一个 Runner —— 会话切换不重启 MCP

`pkg/tui/chat_session.go:632-637`：

```go
func (s *ChatSession) runner() *run.Runner {
	if s == nil || s.Env == nil {
		return nil
	}
	return s.Env.Runner
}
```

`pkg/run/runner.go:2251-2270`（`startOrReplayMCPSegmentLocked`）：指纹相同时**直接复用**已有世代：

```go
func (r *Runner) startOrReplayMCPSegmentLocked(a *agent.Agent) *mcpLoad {
	fp := mcpSegmentFingerprint(r.MCPServers, r.mcpProjectDir())
	if ld := r.mcpLoad; ld != nil && ld.fingerprint == fp && !ld.published() {
		...
	}
	if seg := r.mcpSeg; seg != nil && seg.fingerprint == fp {
		r.replayMCPSegmentLocked(a, seg)
		r.mcpHub.attach(r.mcpReg, seg.generationServers(), seg.generation, false, time.Time{})
		return nil
	}
	...
```

`pkg/run/runner.go:2838-2846`（`subscribeMCPStatus`）：**订阅即同步重放当前世代**：

```go
	if reg := h.currentRegistry(); reg != nil {
		// Replay what the generation looks like right now; ...
		h.mu.Lock()
		snapshot := h.snapshotLocked(reg.ListStatus())
		h.mu.Unlock()
		fn(snapshot)
	}
```

### 2. 切会话把 watch 重挂到新会话上（这是设计，不是 bug）

`pkg/tui/run.go:1762-1770`（`switchStreamSession` 内）：

```go
	// The MCP watch follows the conversation: its failures belong to the session
	// the user has open, and the progress line describes that session's runner.
	if watcher, ok := state.session.(mcpStartupWatcher); ok {
		watcher.WatchMCPStartup(next)
	}
```

### 3. 症结：把重放出来的既定失败记成「当前会话的失败」

`pkg/tui/notify.go:1209-1245`（`WatchMCPStartup`，注意 1215-1217 那句注释：*"Switching sessions
re-arms the watch for the new one, which also means a startup that happened before the switch is
still found — the subscription replays the registry's current state."* —— 就是这句把误记当成了特性）：

```go
	cancel := runner.MCPStartup().Subscribe(func(snapshot run.MCPSnapshot) {
		s.recordMCPStartupFailures(sid, snapshot)
		...
		s.notifyUI(MCPStatusMsg{Snapshot: snapshot, Progress: snapshot.Progress})
	})
```

`pkg/tui/notify.go:1287-1317`（`recordMCPStartupFailures`，正文与注释）：

```go
func (s *ChatSession) recordMCPStartupFailures(sessionID string, snapshot run.MCPSnapshot) {
	var failures []mcp.ServerRecord
	for _, rec := range snapshot.Servers {
		// ... cancelled 不算失败，无文本不记 ...
		if rec.ConnStatus != mcp.ConnStatusError || strings.TrimSpace(rec.Error) == "" {
			continue
		}
		failures = append(failures, rec)
	}
	if len(failures) == 0 {
		return
	}
	generation := snapshot.Generation
	now := time.Now().UTC()
	go func() {
		bg := context.Background()
		for _, rec := range failures {
			// Persist first, then let the store decide whether this is new: ...
			// ... and a resume restarts MCP, recording a failure then against
			// the session that exists by then.
			_ = s.publishRunEvent(bg, mcpStartupErrorEvent(sessionID, generation, rec, now))
		}
	}()
}
```

「resume 会重启 MCP」这句是**假的**（见 §1），也正是本 bug 能活过评审的原因。

### 4. 双画的第二个来源：卡片走异步队列，重放走读库

- 插入：`notify.go:794-837`（`publishRunEvent`；`inserted` 才继续走到画卡）。
- 入队：`notify.go:1061-1073`（`notifyTurnError` → `notifyUIForSession`）→ `chat_session.go:991-1013`
  （`enqueueUINotification`，独立 goroutine 派发到主循环的 `notifyQ`）。
- 重放：`commands.go:63-76`（`printSessionResumeContext`）→ `commands.go:83+`（`replaySession` 读
  `SurfaceSessionEvents` / `SurfaceTranscriptMessages`）。

主循环在切会话期间是忙的（`switchStreamSession` + `replaySession` 都在主循环上同步跑），
所以队列里那张卡一定在 "resumed" 之后才落地。

### 5. 四处与实现不符的注释（本计划要一并改对）

| 位置 | 现在写的 | 实际 |
|---|---|---|
| `notify.go:1215-1217` | 切会话还能"找到"切换前发生的启动 | 那不是"找到"，是把旧世代的失败记到新会话；状态行才是跨会话应该看的 |
| `notify.go:1283-1285` | "the event id is derived from the generation and the server" | 10-19 起是 `mcp:<server>:<sha256(text)[:6]>:error`（`notify.go:1181-1192`） |
| `notify.go:1312-1314` | "a resume restarts MCP, recording a failure then against the session that exists by then" | 不会重启：`runner.go:2251-2270` 复用一个世代；所以那时记的是既定失败 |
| `notify.go:1183-1187` | "Every launch (and every resume) starts MCP again under a new generation" | 同一个错误前提：会话切换不换世代（`runner.go:2251-2270`），所以事件 ID 里根本没有 generation（见上一行） |

### 6. 相关但**不改**的事实（供判断，不要顺手改）

- `mcpFailureEventID` 的事件名是「会话内」稳定身份（`notify.go:1181-1192`）：跨会话本来就不该靠它去重。
- 重放时无 `RunID` 的事件落到末尾（`pkg/tui/commands.go` 的 `replayTimelineWithWorkers`，`pos < 0` 分支），
  本计划的修法不依赖它，但它是 owner 看到「卡片紧贴在 resumed 上一行」的原因。

## Commands you will need（本机已实跑验证）

> **跑测试的正确写法**：只设 `PKG_CONFIG_PATH`，**不要**再覆盖 `CGO_LDFLAGS`/`CGO_CFLAGS`。覆盖会丢掉 pkg-config 里的
> `-Wl,-rpath`，让**不引用 pdfium 的**测试二进制也链上 `libpdfium.dylib` 却找不到 rpath，于是 `pkg/event`/`pkg/state`/
> `pkg/testutil` 等一大批包报 `Library not loaded: @rpath/libpdfium.dylib` —— 看起来像代码坏了，其实是命令行写法坏了。
> 本机已实测：只给 `PKG_CONFIG_PATH` 时全仓零失败。

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `go build ./...` | exit 0（只有 `libtokenizers` 的 `ld: warning: ... built for newer 'macOS' version` 警告，属既有噪声） |
| 聚焦测试（pkg/tui） | `P=$PWD/third_party/pdfium/darwin_arm64 && env -u CGO_LDFLAGS -u CGO_CFLAGS PKG_CONFIG_PATH="$P" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1 -run '<你的测试名或 TestMCP>'` | `ok  github.com/forebrain-harness/forebrain-harness/pkg/tui` |
| 全量测试 | `P=$PWD/third_party/pdfium/darwin_arm64 && env -u CGO_LDFLAGS -u CGO_CFLAGS PKG_CONFIG_PATH="$P" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 每包 `ok`，无 `FAIL`（见下方关于 `CGO_LDFLAGS` 的告警） |
| 静态检查 | `go vet ./pkg/tui/` | exit 0 |

> 注意 `GOFLAGS` 里写 tags 必须用 `-tags=fts5` 形式；直接 `go test -tags fts5` 最省事。

## Scope

**In scope**（只允许改这三个文件）：

- `pkg/tui/notify.go` —— 归属判据 + `recordMCPStartupFailures` 放行条件 + 注释更正。
- `pkg/tui/chat_session.go` —— 只加 2 个字段（照 §"mcpWatch*" 那组字段的写法放在旁边）。
- `pkg/tui/notify_test.go` —— 新增测试。

**Out of scope**（看着相关，但**不要**动）：

- `pkg/run/**`：`mcpStatusHub` / `startOrReplayMCPSegmentLocked` / `MCPSnapshot` 的语义不动。
  MCP 世代该不该随会话重启是另一个决定，本计划不碰。
- `pkg/tui/run.go` 的 `switchStreamSession`：切会话、清屏、Banner、重放顺序都是对的。
- `pkg/tui/commands.go` 的重放：不在这里做 live/replay 的二次去重（见本计划 Maintenance notes）。
- `MCPStatusMsg` 状态行：`/resume` 之后仍要在状态行里如实显示该 runner 的启动/失败状态。
- `pkg/turn/**`（`/clear`、`/new`、`/resume` 的命令目录与可用性）：`/clear` 的行为由 owner 2026-10-22 裁决为
  **保持现状**（只清模型上下文），本计划不碰。

## Git workflow

- 不建分支、不用 worktree，直接在 dev 工作区改。
- **不 commit、不 push、不开 PR**：owner 手动提交，commit message 用中文 Conventional Commit。
- 只改上面 In scope 的三个文件。工作区原本就有大量 owner 未提交的改动（`frontend/**`、`pkg/gateway/dist/**`、
  `pkg/llm/**`、`pkg/tui/chat_session_test.go`、`pkg/tui/render_test.go`、`plans/096-*.md`、`scripts/acceptance/**` 等），
  **一律不动、不回滚**；判断自己改了什么用 `git diff --stat 81a770e -- pkg/tui/`。

## Steps

### Step 1：给 `ChatSession` 加「世代归属」的两个字段和方法

在 `pkg/tui/chat_session.go` 的 `mcpWatchMu/mcpWatchSession/mcpWatchCancel`（`:105-107`）**同一组**里追加
（保持该组注释风格）：

```go
	// mcpOwner* name the conversation that owns the failures of one MCP startup
	// generation: the one that was open when the generation was first seen. A
	// generation belongs to the Runner, not to a conversation (see
	// run.Runner.startOrReplayMCPSegmentLocked: a generation is rebuilt only when
	// the MCP server list changes), so a failure observed after a session switch
	// is a standing failure of that earlier generation, and no other
	// conversation's history may claim it.
	//
	// mcpOwnerMu is its own lock on purpose, NOT mcpWatchMu: the subscribe
	// callback that claims a generation runs inside the registry's delivery lock,
	// while WatchMCPStartup calls the unsubscribe (which takes that same lock)
	// with mcpWatchMu held — sharing the mutex would invert the lock order.
	mcpOwnerMu         sync.Mutex
	mcpOwnerGeneration string
	mcpOwnerSession    string
	mcpPrevGeneration  string
	mcpPrevSession     string
```

（**实测后修正**：槽位从原稿的一个变两个。一个迟到的、属于上一世代 registry 的 snapshot 会在新世代已被认领之后到达；
只有一个槽时它会被当成「新世代」重新认领，于是旧世代的失败又被记到当前会话 —— 正是本计划要修的那件事。
两个槽覆盖「当前 + 刚落幕」这两代，更旧的迟到投递退回改动前的行为，不是回归。）

在 `pkg/tui/notify.go` 里、`recordMCPStartupFailures` **之前**（建议就放它上方）加两个方法：

```go
// claimMCPGenerationOwner names the conversation that owns one MCP startup
// generation — the first one to see it — and returns that owner. An empty
// generation cannot be attributed and owns nothing; claiming one that is
// already owned changes nothing, which is what keeps a standing failure from
// following the user from conversation to conversation.
func (s *ChatSession) claimMCPGenerationOwner(sessionID, generation string) string {
	if s == nil {
		return ""
	}
	gen := strings.TrimSpace(generation)
	sid := strings.TrimSpace(sessionID)
	if gen == "" {
		return ""
	}
	s.mcpOwnerMu.Lock()
	defer s.mcpOwnerMu.Unlock()
	if s.mcpOwnerGeneration != gen {
		s.mcpOwnerGeneration = gen
		s.mcpOwnerSession = sid
		return sid
	}
	if s.mcpOwnerSession == "" {
		s.mcpOwnerSession = sid
	}
	return s.mcpOwnerSession
}

```
（**实测后修正**：原稿这里还有一个 `ownsMCPGeneration` 做第二道判断。它会把「会话行还不存在时就先看到失败」这条既有语义
挡掉——既有的 `TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored` 正是这个场景，而它是对的：失败该被看见、
只是还不该落库。所以判据合并成一条：**谁先看见谁拥有**，`recordMCPStartupFailures` 自己先认领再比对（见 Step 3）。）
```

**Verify**：`go build ./...` → exit 0；`go vet ./pkg/tui/` → exit 0。

### Step 2：世代第一次被看见时就认领它

`pkg/tui/notify.go:1245-1262` 的订阅回调，在 `recordMCPStartupFailures` **之前**加认领（顺序有语义：
认领必须在任何失败被记录之前发生，否则切到新会话时才第一次看到失败的那一瞬间又会归错人）：

```go
	cancel := runner.MCPStartup().Subscribe(func(snapshot run.MCPSnapshot) {
		// The first conversation to see a generation owns its failures. This runs
		// before the failure pass on purpose: a generation that is already
		// failing when the subscription replays it must still be attributed to
		// the conversation that saw it start, not to the one that just opened.
		s.claimMCPGenerationOwner(sid, snapshot.Generation)
		s.recordMCPStartupFailures(sid, snapshot)
		...
	})
```

**Verify**：`go build ./...` → exit 0。

### Step 3：`recordMCPStartupFailures` 按归属放行

在 `pkg/tui/notify.go` 的 `recordMCPStartupFailures` 函数体最前面、收集 `failures` 之前加闸门（**先认领再比对**，
这样任何入口都不必知道自己是不是第一个到达者）：

```go
	// Claiming here as well as in the watch means no caller has to know whether
	// it is the first to see the generation: whoever arrives first owns it, and
	// every later observer of the same generation is a non-owner.
	if owner := s.claimMCPGenerationOwner(sessionID, snapshot.Generation); owner != "" && owner != strings.TrimSpace(sessionID) {
		return
	}
```

**Verify**：`go build ./...` → exit 0；`go vet ./pkg/tui/` → exit 0。

### Step 4：把四处注释改成事实（见 Current state §5）

- `notify.go:1215-1217`：改成「切会话会重放当前世代；一个世代属于 Runner，不属于会话，所以重放出来的
  既定失败只归它启动时在场的那个会话所有；跨会话可见的启动/失败状态由状态行负责」。
- `notify.go:1283-1285`：改成「事件 ID 由 server + 失败文本派生（见 mcpFailureEventID）」。
- `notify.go:1312-1314`：改成「会话切换不会重启 MCP：同一世代（同指纹）会被复用（run.Runner.startOrReplayMCPSegmentLocked），
  所以这里记的只可能是本世代自己的失败」。
- `notify.go:1183-1187`（`mcpFailureEventID` 的文档）：删掉「每次启动（以及每次 resume）都会用新 generation 重启 MCP」这句；
  改成「会话切换/换模型不会重启 MCP（`run.Runner.startOrReplayMCPSegmentLocked`），所以事件身份只由 server + 失败文本派生」。

**Verify**：`grep -n "restarts MCP\|derived from the generation\|starts MCP again" pkg/tui/notify.go` → **无输出**。

### Step 5：加测试（见 Test plan）

**Verify**：聚焦测试命令 → `ok`。

## Test plan

新增测试全部写进 `pkg/tui/notify_test.go`（该文件已有 `sessionEnv`（`chat_session_test.go:29062`）、
`requireUIMessage`（`chat_surface_test.go:84`）、`waitForQueuedNotifications`（`notify_test.go:488`）、
`mcpFailureRecord(name, text)`（`notify_test.go:20`）可用；结构照
`TestStoredMCPFailureReplaysWithItsHeadingAndText` 与
`TestMCPStartupFailureBeforeTheSessionExistsIsShownOnceAndItsRecordPaintsNoSecondCopy` 写）。

1. **`TestMCPFailureBelongsToTheConversationThatSawItsGenerationStart`**（本 bug 的回归）
   - `sessionEnv{Home, SQL: db, SessStore: sessions, RunSvc: runs}.session()`；`Ensure(ctx, "a", "a")` 与 `Ensure(ctx, "b", "b")`。
   - `PrependUINotify` 收集 `MsgKindError`。
   - `session.claimMCPGenerationOwner("a", "g1")`（模拟启动时 a 在场）。
   - `session.recordMCPStartupFailures("b", run.MCPSnapshot{Generation: "g1", Servers: []mcp.ServerRecord{mcpFailureRecord("docs", "connection refused")}})`
     → `runs.ListSessionEvents(ctx, "b", ...)` **为空**；`require.Never` 300ms：没有任何 `MsgKindError` 被发出。
   - 再 `session.recordMCPStartupFailures("a", 同一个 snapshot)` → `a` 恰好 1 条事件，`event_id == mcpFailureEventID("docs","connection refused")`，且恰好 1 张卡（`Title == "mcp docs"`）。
2. **`TestNewMCPGenerationIsOwnedByTheConversationThatSawItStart`**
   - `claimMCPGenerationOwner("a", "g1")`；随后 `claimMCPGenerationOwner("b", "g2")`；
   - `recordMCPStartupFailures("b", snapshot(G2, failure))` → `b` 得到 1 条、`a` 仍是 g1 的旧账（各自独立）。
   - 顺带断言 `claimMCPGenerationOwner("b", "g1")` 返回 `"a"`（旧世代不会被抢走）。
3. **`TestUnknownGenerationStillRecordsForTheOpenConversation`**（保留既有兜底语义）
   - `recordMCPStartupFailures("a", run.MCPSnapshot{Servers: []mcp.ServerRecord{mcpFailureRecord("docs","boom")}})`
     （`Generation` 为空）→ `a` 得到 1 条事件、1 张卡。防止有人把空世代也一并挡掉。
4. **既有测试必须保持绿**（它们钉的是本计划不能破坏的语义）：
   `TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored`、
   `TestMCPStartupFailureBeforeTheSessionExistsIsShownOnceAndItsRecordPaintsNoSecondCopy`、
   `TestStoredMCPFailureReplaysWithItsHeadingAndText`、`TestCancelledServerStartupIsNotAFailure`。
   若第 4 组里任何一个变红，说明你动了不该动的语义 → STOP 4。

**真机验收（owner 侧，可选但强烈建议）**：用当前配置（`caveman-shrink` 必定超时）启动 TUI，等错误卡出现，
然后连续 `/new` ×3 与 `/resume` 到任一历史会话：
- 新会话/被恢复会话的历史里**不再出现**新的 `mcp:` 行：
  `sqlite3 "file:$HOME/.forebrain/state/forebrain.state.sqlite?mode=ro" "select session_id,event_id,datetime(occurred_at_ms/1000,'unixepoch') from fb_session_events where event_id like 'mcp:%' order by occurred_at_ms desc limit 10;"`

## Done criteria（全部必须成立）

- [ ] `go build ./...` → exit 0
- [ ] `go vet ./pkg/tui/` → exit 0
- [ ] 聚焦测试通过：`P=$PWD/third_party/pdfium/darwin_arm64 && env -u CGO_LDFLAGS -u CGO_CFLAGS PKG_CONFIG_PATH="$P" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1 -run 'TestMCP|TestStoredMCP'` → `ok`
- [ ] `make test` → 全部 `ok`
- [ ] `grep -c "claimMCPGenerationOwner" pkg/tui/notify.go` → **5**（1 定义 + watch 调用 + 闸门调用 + 2 处文档注释）。
      注：原稿写的是「两个定义」，实测已合并为一个（见 Status 实现期修正①）；`grep -rn "ownsMCPGeneration" pkg/tui/` → **无输出**
- [ ] `grep -n "restarts MCP\|derived from the generation\|starts MCP again" pkg/tui/notify.go` → 无输出
- [ ] 本计划的三个文件在改动面内，且**没有碰**邻居：`git diff --name-only 81a770e -- pkg/run pkg/state pkg/turn` → **无输出**；
      `git diff --name-only 81a770e -- pkg/tui/` → 只含 `chat_session.go notify.go notify_test.go` 与 099 的
      `reducer.go render.go commands.go run.go run_test.go`（两条计划同批落地，故一次性出现）
      （工作区里本来就有大量 owner 未提交的改动：`frontend/**`、`pkg/gateway/dist/**`、`pkg/llm/**`、
      `pkg/tui/chat_session_test.go`、`pkg/tui/render_test.go`、`plans/096-*.md`、`scripts/acceptance/**` …… 这些**不要动、不要回滚、不要顺手格式化**）
- [ ] 未提交（`git log --oneline -1` 仍是 `81a770e` 及其之前的历史）

## STOP conditions

- **STOP 1**：Drift check 有输出，且 `pkg/tui/notify.go` 里 `recordMCPStartupFailures` / `WatchMCPStartup` 的样子与 §"Current state" 的摘录不符（行号漂移可以接受，结构不符不行）。
- **STOP 2**：你发现 `runner()` 不再是 `s.Env.Runner`，或 `startOrReplayMCPSegmentLocked` 已经改成「会话切换也重建世代」——那说明归属问题已经在别处解决（或设计变了），本计划的前提失效。
- **STOP 3**：改完后 owner 侧真机仍能看到「/resume 之后同一张卡出现两次」→ 说明还有第二个生产者在画这张卡（重放与 live 的竞态）。
  **不要在本计划里加去重**：那条修复已经写进 `plans/099-one-error-card-per-screen.md`（同期计划），报告后按 099 执行。
- **STOP 4**：Test plan 第 4 组（既有 4 个 MCP 测试）变红且不是测试自身笔误。
- **STOP 5**：需要动 `pkg/run/**`、`pkg/tui/run.go` 或 `pkg/tui/commands.go` 才能实现 → 越界，报告。

## Maintenance notes

- **本计划只修归属，不修 live/replay 双画这条通用路径。** 修完后仍存在一个很窄的窗口：某个**真的属于当前会话**的 MCP 失败，
  恰好在一次重放（`/resume`、`/rewind`）读库之前落库，那么重放画一次、队列里的 live 卡再画一次。
  这条通用路径由 **Plan 099 负责**（给 live 与重放共享「这张卡这块屏上画过没有」的身份判据，并让重放替换而不是追加屏幕）。
  owner 2026-10-22 已裁定：**097 与 099 同属本期，两条都要落地**，缺一条都不算修完。
- **reviewer 要盯的三点**：① `claimMCPGenerationOwner` 只用了**独立的** `mcpOwnerMu`：该回调运行在 registry 的
  delivery lock 内，而 `WatchMCPStartup` 会**持有 `mcpWatchMu` 去调用 unsubscribe**（unsubscribe 取的是同一个 hub 锁），
  共用 `mcpWatchMu` 会造成锁序反转死锁；回调里也绝不能做 DB 写或回调 Runner；② 空 `Generation` 必须仍然记账（`claimMCPGenerationOwner` 对空世代返回 `""`，闸门据此放行），否则会悄悄吃掉「无世代信息」的失败；③ 两个槽位（当前 + 上一世代）的取舍：更旧的迟到投递会被当成新世代认领，退回改动前的行为 —— 三代同飞在真实运行里没有路径。
- **未来会与它交互的改动**：若 MCP 改成「按会话/按项目独立启动」（世代随会话变化），本计划的归属槽位就该删除，
  改为「谁的世代谁记账」的自然成立；若 `/mcp` 面板加了「reload」，reload 产生的世代由当时在场的会话认领——
  这正是期望行为。
- **历史数据的既定污染不清理**：owner 库中已存在的 25 条 `mcp:%` 行是旧行为留下的，本计划不删；需要清理另开一份 plan
  （涉及用户历史，必须 owner 决定）。
