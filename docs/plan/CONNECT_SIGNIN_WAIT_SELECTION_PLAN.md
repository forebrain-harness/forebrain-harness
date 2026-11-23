# `/connect` 登录等待期间 URL/code 无法拖拽选中复制——根因修复

状态：已实施并真机验收通过（2026-11-14）（owner 裁决：① 路线 B「应用接管输入」；② Windows 保持现状；③ Esc/Ctrl+C = 放弃这次登录；④ 等待期间键入丢弃；⑤ `connectSetupTimeout` 保持 10 分钟）
基线：HEAD `a1da14e4` + 工作区未提交改动（另有会话 `forebrain-harness-69` 的 WIP：`pkg/tui/notify.go`、`render.go`、`render_test.go`、`run.go`、`pkg/turn/*`、`pkg/gateway/*`、`frontend/*`，以及未跟踪的 `pkg/tui/subagent_footer_display_test.go`）。本计划不动这些文件。
落盘：本文件是审批件；批准后把同一份内容另存一份到 `docs/plan/CONNECT_SIGNIN_WAIT_SELECTION_PLAN.md`（`docs/plan/` 已 gitignore，不入库）。

---

## 1. 目标（Why this matters）

owner 截图（`.forebrain/attachments/forebrain-clipboard-95348-1008250030.png`）：登录火山方舟（Volcengine Ark）订阅时，TUI 打出

```
Open https://signin.volcengine.com/authorize/oauth/device?trace_id=... and enter this code.

27S2-9U93
```

这两行在**整个等待窗口里既拖拽选不中，也 Esc/Ctrl+C 取消不了**。用户必须在另一个窗口里把 URL 和 code 抄/复制过去，而本产品的选区语义是"拖拽选中、松开即复制"，所以这一步直接卡住登录。

目标：等待期间这两行按全产品同一套语义可拖拽选中、松开即复制；Esc/Ctrl+C 放弃这次登录并回到 provider 页（顺手修掉这段最长 10 分钟的无输入窗口）。**范围之外如实保留**：等待前 `awaitSignInURL` 的至多 30 s 窗口、offer 列取与 probe 的至多 10 s 窗口仍无输入所有权（见 §4 排除理由）。

---

## 2. 根因（证据链，已在当前源码逐条核对）

1. `/connect` 在主输入循环里**同步**执行，整个向导（含等待）占住了循环。`pkg/tui/run.go:1324-1326`：

   ```go
   case "connect":
       _ = cmds.handleConnect(ctx, state.sessionID)
       return true, false, ComposerSubmission{}, false
   ```
   这段在 `func Run(...)`（`pkg/tui/run.go:33` 起）的 `for` 主循环体内；`runConnect`（`pkg/tui/setup.go:2204-2223`）用 `c.renderer.OutputWriter()` 作为 setup 的 `Output`。

2. 页面文本进 transcript，而不是直绘屏幕：selector 是 `NewInteractiveSelector(out, readLine, f, renderer)`（`pkg/tui/run.go:193-196`），`rawSelector.StatusWriter` 在非 standalone 时返回 nil（`pkg/tui/render.go:7244-7253`），于是 `llmSetup.statusOutput`（`pkg/tui/setup.go:1626-1633`）退回 `cfg.Output` = `renderer.OutputWriter()`；`pkg/tui/render.go:703-736` 的 `writeStatusText` → `seedRawViewportLocked`（`render.go:807-816`）把这段文本作为 `Frame{Kind: FrameRaw}` 追加进视口 transcript（`FrameRaw` 原样分行见 `pkg/tui/reducer.go:3367-3370`）。

3. 打印完页面就阻塞等人生效，最长 10 分钟。`pkg/tui/setup.go:1125-1194` 的 `signIn`：`status := s.statusOutput(...)`（1157）→ `signInPage`（`setup.go:1279-1307`，device_code 三行文本在 1288）→ `login.Wait(ctx)`（**1185**），时限来自 `connectSetupTimeout = 10 * time.Minute`（`pkg/tui/setup.go:2196`，owner 裁决本期不动）。

4. 鼠标被本进程捕获，终端不再自己选：`pkg/tui/run.go:67` 输出 `enableMouseSeq`，定义 `pkg/tui/input_events.go:791` = `"\x1b[?1002l\x1b[?1003h\x1b[?1006h"`（注释 `input_events.go:772-790` 明说"Capturing the mouse means the terminal's own text selection needs Shift held"）。因此拖拽必须由应用接管：`dispatchMouse`（`pkg/tui/overlays.go:3640-3658`）→ `Renderer.HandleMouse`（`pkg/tui/reducer.go:6299-6324`，press/drag/release/move/wheel 全在内）→ `viewportPressLocked`（7179 起）→ 松开走 `viewportRelease`（7449 起：`extractSelectedTextLocked` + `setCopiedNotificationLocked` + `writeClipboard`）。

5. 但等待期间**没有任何 goroutine 读终端 fd**：`events` 通道（`make(chan inputEvent, 8)`，`input_events.go:947`）的消费者是主循环，主循环正卡在第 3 步的 `login.Wait`；后台 raw reader 读到报告、解码成 `inputEventMousePress`（kind=7）后送不进被阻塞的通道就停在那里。→ 鼠标报告根本到不了 renderer；Esc/Ctrl+C 同理。

6. 反证（同一条向导里别的页面能选）：`rawSelector` 的 prompt 会 `acquireComposerInput()` 暂停共享 reader 并自己 `readKey()`，把鼠标事件转发给 `dispatchMouse`（`pkg/tui/overlays.go:3640`、`3728-3750`、`3758-3798`）。所以**确认页/粘贴页可拖选，只有"没有 prompt 的等待"是死的**。这解释了两类订阅的差别：`pkg/subscription/registry.go:23-25` 的 `FlowPasteCallback`（ChatGPT，等待前先开一个 `Input` prompt）vs `FlowDeviceCode`（OpenCode、Volcengine Ark，等待前无 prompt）。

排除的竞争解释：
- 不是"`FrameRaw` 块不可选中"：它进 `vr.allLines`，`lineForAbsRow`（`pkg/tui/reducer.go:7158-7173`）与 `extractSelectedTextLocked`（8023 起）都读得到；选不中的原因是事件根本没进来。
- 不是"终端不支持"：SGR 报告在本仓库验收里长期可用（`.claude/skills/run-forebrain/driver.sh:181-184`）。
- 不是"没有选区代码"：普通对话区拖拽选中在同一个循环里可用（§5 Step 0 的 A 场景）。
- 不是首启独立向导的问题：独立向导不捕获鼠标（`enableMouseSeq` 只在 `Run` 里发，`pkg/tui/run.go:67`）。

同类缺陷穷举（owner 规则「修一个交互缺陷穷举同类」）：
- `/migrate` 异步（`pkg/tui/commands.go:2800-2814`）；`/mcp auth` 背景 goroutine + `ready` 通道（`chat_slash.go:1246-1300`）；`/compact` 走 `dispatchCancelableStreamSlashCommand`（`run.go:1414-1500`）；审批/问答 overlay 用 `tryAcquireComposerInput`（`overlays.go:3758`）。→ 均已避开此缺陷，本期不动。
- 同一向导里**同一模式但明确排除**的两处（理由见 §4）：`awaitSignInURL` 的至多 30 s（`setup.go:1314-1331`，页面尚未打印、屏上无可复制内容）；`probeSubscription`/`fetchOffers` 的至多 10 s（`setup.go:818-822`、`1535-1539`，无可复制内容）。它们是"短暂无输入"而非本 bug 的"屏上有一页要人复制的文本却选不中"。

**独立复现证据（计划阶段已取得，非推测）**：review 在 worktree `.worktrees/plan-review-4267496826`（HEAD `a1da14e4`）里写了三个用例（`pkg/tui/zz_review_signin_test.go`，该文件不合命名规范、不得并入主树），命令

```bash
cd .worktrees/plan-review-4267496826 && CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestReviewSignInWait -count=1 -v
```
→ 3/3 PASS：
- `TestReviewSignInWaitSwallowsTheMouseReport`：reader 解码出鼠标事件却卡在 `events` 通道，renderer 无选区、footer 无 copied 提示 —— **缺陷复现**；
- `TestReviewSignInWaitSelectionWorksWhenTheWaitOwnsTheInput`：`readKey` + `dispatchMouse` 后选区成立、复制出 `27S2-9U93`、footer `copied 9 chars to clipboard` —— **机制成立**；
- `TestReviewSignInWaitOwnsTheInputWhileTheBackgroundReaderRuns`：后台 reader 在跑时 pause + 自读 fd 仍能选中复制（无死锁、无抢字节）。

---

## 3. 修复设计（owner 已选路线 B）

**不变式（根因表述）**：只要屏幕上有一页需要人操作/复制的文本，且本进程捕获了鼠标，就必须有代码持有输入所有权（暂停共享 reader + 把鼠标事件转发给 renderer）。等待窗口今天违反这条不变式。

### 3.1 `pkg/tui/channels.go`：新增一个可选 selector 能力

放在 `ConnectProgressSelector`（`channels.go:707-715`）旁边，沿用既有"可选能力"惯例：

```go
// ConnectWaitSelector is an optional capability of a selector that can hold the
// terminal's input while a sign-in waits for the person in the browser. Without
// it the wait owns nothing: the page the flow printed — the address and the
// code — cannot be selected, because the surface has captured the mouse and the
// only servo that selection has is the input loop the wait is blocking.
type ConnectWaitSelector interface {
	// AwaitSignIn owns input until the flow's outcome arrives on done: a mouse
	// drag selects and copies the page's text with the conversation's own
	// semantics (release copies, Ctrl+C copies while a selection stands), the
	// wheel scrolls the transcript, and Esc or Ctrl+C abandons the sign-in —
	// which reports ErrCancelled to the caller. It otherwise returns the flow's
	// outcome exactly as Login.Wait did.
	AwaitSignIn(done <-chan error) error
}
```

### 3.2 `pkg/tui/overlays.go`：抽出"只暂停共享 reader"的一段，并实现 `AwaitSignIn`

把 `tryAcquireComposerInput`（`overlays.go:3758-3798`）拆成两层，行为完全不变（现有调用方一个都不改）：

```go
// pauseSharedInput takes exclusive ownership of the terminal fd for a caller
// that draws no overlay of its own: it pauses the shared input reader and waits
// for it to acknowledge, so this caller's reads cannot race it. The returned
// closure resumes the reader. ok is false when another modal already owns it.
func (s *rawSelector) pauseSharedInput() (func(), bool) {
	if !stdinReadMu.TryLock() {
		return func() {}, false
	}
	acquireInteractiveInputPause()
	stdinReadMu.Unlock()
	pauseInteractiveInputReadAndWait(50 * time.Millisecond)
	if s.inViewportOverlay() {
		deadline := time.Now().Add(2 * time.Second)
		limit := 100 * time.Millisecond
		for time.Now().Before(deadline) && interactiveInputPaused.Load() && !interactiveInputPausedAck.Load() {
			pauseInteractiveInputReadAndWait(limit)
			limit *= 2
			if limit > 200*time.Millisecond {
				limit = 200 * time.Millisecond
			}
		}
	}
	stdinReadMu.Lock()
	stdinReadMu.Unlock()
	return func() { releaseInteractiveInputPause() }, true
}

func (s *rawSelector) tryAcquireComposerInput() (func(), bool) {
	releaseReader, ok := s.pauseSharedInput()
	if !ok {
		return func() {}, false
	}
	if s.composer != nil {
		s.composer.BeginComposerOverlay()
	}
	return func() {
		if s.composer != nil {
			s.composer.EndOverlay()
		}
		releaseReader()
	}, true
}
```

`AwaitSignIn` **不能**走 `BeginComposerOverlay`：它 `finishTransientLocked("")`（`render.go:4444-4456`）并把 composer 块清空，等待期间屏幕会少一块、登录 live line 被掐。只暂停 reader 则屏幕原样，`ConnectProgress` 的 live line 继续 tick。

`AwaitSignIn` 本体（放在 `dispatchMouse` 之后）：

```go
const (
	// How often the sign-in wait looks up from the terminal to ask whether the
	// flow ended. One paint tick; long enough to cost nothing, short enough
	// that the wizard resumes the moment the browser finishes.
	connectWaitPollMillis = 50
	// How long an ESC gets a second look before it counts as the Esc that
	// abandons the sign-in. readKey reports rawKeyEscape both for a bare Esc
	// and for the ESC of a report whose tail had not arrived inside its own
	// 50 ms window (overlays.go:3956-3968); one more look tells them apart, so
	// a mouse report split across two writes cannot discard a sign-in.
	connectWaitEscapeSettleMillis = 60
)

func (s *rawSelector) AwaitSignIn(done <-chan error) error {
	// Only a real interactive terminal has a viewport whose selection this wait
	// can serve, and only where the fd can be polled can the wait also notice
	// the flow ending. Everywhere else — the onboarding's own pages, piped
	// stdin, Windows consoles — the answer is a plain wait, which is what
	// Login.Wait already was there.
	if s == nil || s.in == nil || !s.inViewportOverlay() || !canGateTerminalReads {
		return <-done
	}
	release, ok := s.pauseSharedInput()
	if !ok {
		// Another modal owns the input (an approval overlay, a nested prompt).
		// Wait without it rather than take it from something the user is
		// answering.
		return <-done
	}
	defer release()
	fd := int(s.in.Fd())
	for {
		select {
		case err := <-done:
			return err
		default:
		}
		readable, pollErr := pollReadableInputFD(fd, connectWaitPollMillis)
		if pollErr != nil {
			return fmt.Errorf("sign-in wait: %w", pollErr)
		}
		if !readable {
			continue
		}
		key, readErr := s.readKey()
		if readErr != nil {
			// The terminal is gone (EOF or a read error). Waiting longer cannot
			// help: report it instead of parking here for the setup's window.
			return fmt.Errorf("sign-in wait: %w", readErr)
		}
		switch key.kind {
		case rawKeyMouse:
			s.dispatchMouse(key.mouse)
		case rawKeyEscape:
			if tail, _ := pollReadableInputFD(fd, connectWaitEscapeSettleMillis); tail {
				// Part of the report was still in flight: it belongs to that
				// gesture, not to the sign-in. The fragment is unusable on its
				// own, so drop it (stdinReadMu is ours: nobody else can be mid-
				// sequence) and keep waiting.
				stdinReadMu.Lock()
				drainImmediatelyAvailableInput(s.in)
				stdinReadMu.Unlock()
				continue
			}
			return ErrCancelled
		case rawKeyCtrlC:
			return ErrCancelled
		}
		// Owner decision: keys typed during the wait are dropped. Nothing on
		// screen accepts text there, and re-encoding a parsed key back into
		// bytes for the reader would be a second decoder beside readKey.
	}
}
```

要点（执行者别改错）：
- Ctrl+C 已在 `readKey` 内先走 `CopySelectionForCtrlC()`（`overlays.go:3946-3952`）：有选区时只复制并返回 `rawKeyNone`（本循环什么都不做、不取消），无选区才返回 `rawKeyCtrlC`。这是全产品语义，不要另加判断。
- 其它按键一律丢弃（owner 裁决 ④）。**已知代价**：今天在等待期间打的字会先落进 `events` 的 8 槽缓冲、之后（缓冲满即冻结，恢复后补齐）进入 composer；改成丢弃后这些字不出现。这是刻意的语义变更，写进 §3.4 矩阵与 §9。
- `ErrCancelled` 是既有变量（`pkg/tui/tty_session.go:1427`），复用，不新增错误类型。
- `canGateTerminalReads` 在 Windows 上是 `false`（`pkg/tui/platform_windows.go:20-32`，`pollReadableInputFD` 是恒 `true` 的桩）——owner 裁决 ②：Windows 保持今天的行为，不要为它加 `WriteConsoleInput` 之类偏方，也不要动 `platform_windows.go`。
- 端点无注入缝：`volcengineSignInEndpoint` 是 const（`pkg/subscription/volcengine_oauth.go:29`）。**不新增 env 覆盖**（out of scope）：复现靠 §5 Step 0 的离线用例，真机只作二次确认。

### 3.3 `pkg/tui/setup.go`：`signIn` 改等待方式

`signIn` 现在的等待段（`setup.go:1156-1191`，重要行号：`defer login.Cancel()` 1156、`end := s.startConnectProgress(...)` 1161、`err = login.Wait(ctx)` 1185）：

```go
	...
	if flow == subscription.FlowPasteCallback {
		if err := s.pasteSignInCallback(ctx, login, status); err != nil {
			end()
			return false, err
		}
	}
	err = login.Wait(ctx)
	if end != nil {
		end()
	}
	if err != nil {
		return false, err
	}
```

只替换末尾这一段（`login.Wait` 之后的部分）：

```go
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	outcome := make(chan error, 1)
	go func() { outcome <- login.Wait(waitCtx) }()
	var waitErr error
	if waiter, ok := s.cfg.Selector.(ConnectWaitSelector); ok && waiter != nil {
		waitErr = waiter.AwaitSignIn(outcome)
	} else {
		waitErr = <-outcome
	}
	if end != nil {
		end()
	}
	if errors.Is(waitErr, ErrCancelled) {
		// The person abandoned the sign-in from the page it printed: the same
		// answer Esc on this page always gave — the setup steps back a page and
		// writes nothing (owner decision: this login is given up, a later
		// /connect asks for a fresh device code).
		logPromptChoice(s.cfg, "llmsetup.sign_in_declined", s.choice, nil)
		return false, nil
	}
	if waitErr != nil {
		return false, waitErr
	}
```

（`defer cancelWait()` 与既有 `defer login.Cancel()`（1156）一起收尾；`outcome` 容量 1，取消后 goroutine 不泄漏。`errors.Is` 需要 `errors` 已在 imports 里——`setup.go` 现已导入。）

语义对齐：`credentials()` 返回 `(false, nil)` 在向导里就是"上一页"（`setup.go:526-556` 的 `run()`：`forward=false` → `s.step(pages, -1)`），与"确认登录时选 No"（1146-1150）一致，因此取消等待 = 回到 provider 列表，不需要新语义。

### 3.4 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 交互式 TUI，`/connect` → Volcengine Ark / OpenCode 设备码登录等待 | 拖拽无反应、无选区；Esc/Ctrl+C 无反应；滚轮无反应 | 拖拽选中并松开即复制（footer 右侧 `copied N chars to clipboard` / OSC 52 变体）；有选区时 Ctrl+C 只复制、不取消；无选区时 Esc/Ctrl+C **放弃这次登录**并退回 provider 页（`login.Cancel`，下次 /connect 拿新 code）；滚轮可滚 transcript |
| 同上，但登录在浏览器里完成 | 循环卡到完成或 10 分钟超时，期间无输入 | `connectWaitPollMillis` 内返回，照走进 offers 页 |
| 等待期间在输入框打字 | 先灌进 8 槽缓冲、缓冲满即冻结、恢复后进入 composer（"最终保留"） | **丢弃**（owner 裁决 ④）：等待窗口内字符不进入 composer。已知代价，见 §9 |
| 同上，Ctrl+C 且在等待期间已拖选出选区 | Ctrl+C 无反应 | 只复制并清选区（既有语义），不取消登录 |
| 鼠标报告被拆成两次写入（tmux/SSH 分片） | 无输入，无所谓 | `connectWaitEscapeSettleMillis` 内再查一次：分片尾巴到达则不取消（碎片丢弃），只有真 Esc 才取消 |
| ChatGPT paste 流程（`FlowPasteCallback`） | 页面打印后由 `Input` prompt 拥有输入（可选中）；`login.Wait` 在粘贴之后短暂阻塞 | 同上不变。**遗留缺口（如实记录）**：`setup.go:1165` 打印页面到 1180 打开粘贴 prompt 之间（浏览器打开调用之后，亚毫秒级）无人持有输入；本修复只接 1185 的等待，不覆盖这段 |
| 首启独立向导（standalone setup pages，不捕获鼠标） | 终端原生拖拽选中可用 | 不变：`!inViewportOverlay()` → 直接 `<-done` |
| 非 TTY / line selector / 测试 / gateway | `login.Wait` 阻塞 | 不变：selector 没实现该能力 → `<-outcome` |
| Windows 控制台 | 等待期间选不中（可用 Shift+拖拽走终端原生选择） | 不变（owner 裁决 ②） |
| `awaitSignInURL`（至多 30 s）、`probeSubscription`/`fetchOffers`（至多 10 s） | 无输入所有权 | 不变（§4 排除理由） |
| `/migrate`、`/mcp auth`、`/compact`、审批/问答 overlay | 已异步或已拥有输入 | 不变（本次不碰） |
| 选区/复制/提示文案（对话区、overlay、footer） | — | 完全复用现有实现，不新增语义、不新增文案 |
| `connectSetupTimeout` | 10 分钟 | 10 分钟（owner 裁决 ⑤） |

---

## 4. Scope

In scope（只这 5 个文件）：
- `pkg/tui/channels.go` — 新可选能力接口。
- `pkg/tui/overlays.go` — 抽 `pauseSharedInput`、加 `AwaitSignIn` + 两个常量、imports 里补 `fmt`（若尚未导入）。
- `pkg/tui/setup.go` — `signIn` 的等待段（约 1185-1192）。
- `pkg/tui/setup_test.go`、`pkg/tui/overlays_test.go` — 新增用例（`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles` 要求测试文件与生产文件同名）。

Out of scope（明确不碰，或明确排除）：
- `pkg/tui/run.go`、`pkg/tui/render.go`、`pkg/tui/notify.go`、`pkg/tui/reducer.go`（其他会话 WIP 或选区/复制实现本身）。
- `pkg/subscription/*`（`Login` 契约与 const 端点都不动；不新增 env 注入缝）。
- `pkg/tui/platform_windows.go`、`canGateTerminalReads`。
- `awaitSignInURL`（`setup.go:1314-1331`，至多 30 s）：页面尚未打印，屏上没有任何可复制内容，且已由 `ctx`/30 s 上界兜住 —— 不是本 bug 的同类症状（"屏上有一页要复制的文本却选不中"），本期不接输入所有权。
- `probeSubscription`（`setup.go:818-822`）与 `fetchOffers`（`setup.go:1535-1539`，`modelsDiscoveryTimeout`）：同上，无可复制内容、时限内自愈，本期不接。
- paste 流程"打印页面 → 粘贴 prompt"之间的亚毫秒窗口（§3.4 已登记为遗留缺口）。
- `/connect` 的整体异步化（像 `/mcp auth`），`/migrate`、`/mcp auth`、`/compact`。
- 首启独立向导的页面渲染（`paintPage`）、web/gateway 任何面。

不得改变的公共形态：`Selector` 接口（`channels.go:617-640`）不加方法（新能力必须是可选接口）；`Login` 接口；任何用户可见文案（新文案为零）。

---

## 5. Steps（顺序执行，中途保持仓库可用）

**Step 0（复现，未改动生产源码）**

0a. 离线复现（第一验证步骤，不依赖第三方网络）：在 `pkg/tui/overlays_test.go` 加入"缺陷断言"用例（照 review worktree 里 `TestReviewSignInWaitSwallowsTheMouseReport` 的写法，改成合规命名，如 `TestSignInWaitWithoutAnInputOwnerLosesTheDrag`）：`os.Pipe` + `readRawInputEvents`（不 drain）+ `newComposerRenderer(t, 80, 24)` 里放 `Frame{Kind: FrameRaw, Content: 登录页}`，写入 press→drag→release 的 SGR 报告，断言 reader 解码出事件（kind=7）却卡在通道、`renderer.HasSelection()` 为假、footer 无 `copied`。命令：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSignInWaitWithoutAnInputOwnerLosesTheDrag' -count=1 -v
```
预期：PASS（用例断言的是缺陷存在），日志打出"reader decoded the report (kind=7) — stuck in the channel"。该用例保留：它钉住"没有输入所有权就什么都到不了 renderer"，防止将来把能力删掉。

0b. 真机复现（二次确认；本机已核实 `curl -sS -m 8 -o /dev/null -w '%{http_code}' https://signin.volcengine.com/` = 302，若执行时不通则按 §8 降级并如实记录）：用 fake 模型驱动真机，`SSH_CONNECTION` 置位以免在 owner 机器上打开浏览器（`drivableFlow` 只跳过 `browser_callback`，`device_code` 仍可驱动，`setup.go:1220-1233`）：

```bash
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-signin FOREBRAIN_RUN_SESSION=fb-signin FOREBRAIN_RUN_PORT=8763
export SSH_CONNECTION=scratch
$D reset; $D build; $D start hang
$D wait '›' 20
```

A 场景（证明选区机制在验收环境可用，也是坐标基准）：用 `$D screen` 找一行对话文本，`$D col '<子串>'` 取列号，然后

```bash
$D send "$(printf '\033[<0;<C>;<R>M')"      # press（1-based 列/行）
$D send "$(printf '\033[<32;<C2>;<R>M')"     # drag
$D send "$(printf '\033[<0;<C2>;<R>m')"      # release
$D screen | tail -2                          # 期望 footer 右侧 "copied N chars to clipboard"
```

B 场景（本 bug）：`$D submit '/connect'` → 按屏幕实际行选 Volcengine Ark → 确认 `Sign in with Volcengine Ark …` → `$D wait 'and enter this code' 60`；对 URL 行与 code 行各做一次 press→drag→release，再 `$D screen | tail -2`。
预期（修复前）：A 有 copied 提示，**B 无 copied 提示、无高亮** —— B 即复现。
若 A 场景就没有提示：STOP（验收工具/坐标有误，先修工具，不许拿产品改动当解）。

**Step 1** `pkg/tui/channels.go`：按 §3.1 加 `ConnectWaitSelector`。
检查：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` 通过。

**Step 2** `pkg/tui/overlays.go`：拆 `pauseSharedInput` + 实现 `AwaitSignIn`（§3.2）。
检查：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` 通过；`grep -rn "tryAcquireComposerInput\|acquireComposerInput\|pauseSharedInput" pkg/tui` 的调用点集合与改动前一致（新函数只被 `tryAcquireComposerInput` 与 `AwaitSignIn` 使用）。

**Step 3** `pkg/tui/setup.go`：`signIn` 等待段按 §3.3 改造。
检查：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` 通过；`grep -n "login.Wait" pkg/tui/setup.go` 只剩 goroutine 里那一处调用。

**Step 4** 补齐 §6 的 T1/T2 用例。
检查：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'AwaitSignIn|SubscriptionWait|SignInWaitWithoutAnInputOwner' -count=1` 全绿。

**Step 5** 包级回归 + 静态检查：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go build -tags fts5 ./...
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1
gofmt -l cmd pkg third_party            # 必须无输出
CGO_ENABLED=1 go vet -tags fts5 ./...
```

**Step 6** 真机验收（修复后 `$D build` 重建，重跑 Step 0b 的场景）：
- A：正常对话区拖拽 → copied 提示（回归不变）。
- B：等待期间拖拽 URL 行与 code 行 → 选区高亮 + copied 提示。高亮证据必须直接抓带 SGR 的抓屏（`cmd_screen()` 忽略参数，`$D screen -e` 无效，`driver.sh:185`）：
  `tmux capture-pane -e -t "$FOREBRAIN_RUN_SESSION" -p | grep -n '<转发下来的行>'`；能取到剪贴板时再用 `pbpaste | head -1` 比对内容与屏上一致。
- C：等待期间 `$D key Escape` → 1 s 后 `$D screen` 回到 provider 列表（`Connect a model`），transcript 里登录页仍在，进程存活、无报错行。
- D：`$D stop`，删除 `$TMPDIR/forebrain-run-signin`。

**Step 7** 收口：计划文件回填"已实施并真机验收通过（日期）+ 验收记录"；改动留工作区不 commit；报告根因链、最小改动清单、证据、遗留限制（含 §9 的键入丢弃与两项未覆盖窗口）。

---

## 6. 测试与验收

**T0（Step 0a，缺陷断言，长期保留）** `pkg/tui/overlays_test.go::TestSignInWaitWithoutAnInputOwnerLosesTheDrag` —— 见 Step 0a。

**T1 `pkg/tui/setup_test.go`（wiring 与取消语义，无需 TTY）**
现成夹具：`fakeLogin`（`setup_test.go:247-286`，`Wait` 目前立即返回）、`fakeSubscription`（288-370）、`withSubscriptions`（447-452）、`runSubscriptionSetup`（489-496）、`volcengineLike()`/`openCodeLike()`（404-443）。

- 给 `fakeLogin` 加"把 Wait 挂住"的控制位（如 `waitCh chan error` / `hold chan struct{}`，测试用写入或 close 结束），默认仍立即返回 → 现有用例不受影响。
- 新增 `setupWaitSelector`（嵌入 `*fakeSelector`，实现 `AwaitSignIn`）：记录调用次数、`done` 是否活着，并按用例返回 `ErrCancelled` 或 `<-done`。
- 用例：
  1. `TestConfigureSubscriptionWaitOwnsTheInputAndStepsBackOnEscape`：device_code 订阅，`AwaitSignIn` 返回 `ErrCancelled` → `ok=false`、无写入、页面文本（URL+code）**仍然**已写到 `cfg.Output`、`sub.login.canceled == true`。
  2. `TestConfigureSubscriptionWaitDeliversTheOutcome`：`AwaitSignIn` 返回 `<-done`（nil）→ 照旧进 offers 页，`opts.Model` 符合期望（守住成功路径）。
  3. `TestConfigureSubscriptionWaitPropagatesTheFlowError`：`login.waitErr = errors.New("code expired")` → 返回该错误（不是 ErrCancelled）。
  4. `TestConfigureSubscriptionPlainSelectorStillWaits`：selector 未实现该能力（普通 `fakeSelector`）→ 仍按 `<-outcome` 跑通（line selector / 非 TTY 回归）。

**T2 `pkg/tui/overlays_test.go`（选区、取消、完成检测；pipe 驱动）**
骨架照 review worktree 的 `reviewPageRenderer`/`reviewSGR`/`reviewDrag`（合规改名后并入）：
- renderer 用 `newComposerRenderer(t, 80, 24)`；设备码所在屏幕行从 `r.vpLastRender.lines` 里按子串找（**不要写死坐标**）；测试里用变量桩 `clipboardWriteText = func(context.Context, string) error { return nil }` 并 `t.Cleanup` 还原（避免真的写系统剪贴板）。
- 用例 `TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns`：transcript 放 `FrameRaw` 登录页 → `go func(){ got <- rs.AwaitSignIn(done) }()` → 向 pipe 写 press/drag/release（覆盖 code 行）→ 断言 `renderer.selectCopied` 含 `27S2-9U93`、footer 含 `copied`；再写裸 ESC 字节 → `AwaitSignIn` 返回 `ErrCancelled`。
- 用例 `TestAwaitSignInEndsWhenTheFlowEndsWithoutAKey`：不发任何字节，只向 `done` 写 nil → 1 s 内返回 nil（完成检测不依赖按键）。
- 用例 `TestAwaitSignInFallsBackWhenTheInputIsOwned`：先 `rs.acquireComposerInput()` 占住 → `AwaitSignIn` 不得抢锁/不得死锁，`done` 一到即返回。
- 用例 `TestAwaitSignInFallsBackWithoutAViewport`：`renderer.viewportMode = false`（或 `s.in == nil`）→ 直接返回 `<-done`。
- 用例 `TestAwaitSignInReportsAReadError`：关闭读端（EOF）→ 返回错误而非继续等（对应 §3.2 的 P9 修正）。

**回归面**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1` 全绿（含既有的 `TestConfigureSubscription*`、`TestViewportSelection*`、`TestCopySelectionForCtrlC*`、嵌套 pause 的 `chat_session_test.go:11237` 用例）。

**真机验收**：Step 6 的 A/B/C/D，证据 = 带 SGR 的抓屏（`tmux capture-pane -e`）+ footer 提示行 + C 场景回到 provider 页的抓屏。

---

## 7. Done criteria（机器可查）

1. `CGO_ENABLED=1 go build -tags fts5 ./...` 通过；`gofmt -l cmd pkg third_party` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` 通过。
2. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1` 全绿，且按名字可见 §6 的 T0 + T1（4 个）+ T2（5 个）用例。
3. 改动范围检查（相对开工前的基线快照，不是相对 HEAD）：开工前记录 `git status --short` 与每个 out-of-scope 文件的 `git diff | shasum`（存到 `$TMPDIR/fb-baseline/`）；收工时这些 out-of-scope 文件的哈希不变，`git status --short` 里**本会话新增**的条目只有 `pkg/tui/channels.go`、`pkg/tui/overlays.go`、`pkg/tui/setup.go`、`pkg/tui/setup_test.go`、`pkg/tui/overlays_test.go` 这 5 项（他人 WIP 条目原样保留）。零 commit。
4. 真机验收记录齐备：A（copied 提示）、B（等待期间拖拽 → 带 SGR 的高亮 + copied 提示，内容与屏上一致）、C（Esc 退回 provider 页且进程存活）、D（清理完成），每条含抓屏摘录；若联网不可用，记录降级原因与未跑项（不许伪造）。
5. 计划文件状态行改为"已实施并真机验收通过（日期）"并附验收记录。

---

## 8. STOP conditions

- **基线漂移**：`pkg/tui/setup.go` 的 `login.Wait(ctx)` 段（锚点 1185）或 `overlays.go` 的 `tryAcquireComposerInput`（3758）内容与计划摘录不符（别的会话改过）→ 停，先回报差异，不硬改。（行号漂移不算漂移，内容不一致才算。）
- **Step 0a 的缺陷断言用例不通过**（例如 reader 没解码出事件、或竟然已有选区）→ 停：说明因果链与当前源码不符，回到 §2 重新取证。
- **Step 0b 的 A 场景就不出 copied 提示**（循环活着时也选不中）→ 停：先定位验收工具/坐标，不许拿产品改动当解。
- **`pauseSharedInput` 在真机上持续返回 false**（等不到输入所有权）→ 停并回报现场（说明还有别的 modal 在跑），不许改成抢锁或强行 Read。
- 需要改 `pkg/tui/run.go`、`pkg/tui/render.go`、`pkg/tui/platform_windows.go` 或任何他人 WIP 文件 → 停，`peer_send forebrain-harness-69` 协调后再议。
- 想"顺手"修 Windows、给端点加 env 注入缝、把 `/connect` 整体异步化、或改 `connectSetupTimeout` → 停：owner 已裁决本期都不做。
- 联网真机验收不可用（`signin.volcengine.com` 不可达 / device code 请求失败）→ 不伪造：记录降级（离线复现 + 单测 + fake 夹具验收已覆盖机制），并写清哪一场景没跑、为什么。

---

## 9. Maintenance notes（给后来人）

- 这个接口只服务"**屏幕上有一页要人操作/复制的文本 + 人在另一个窗口里操作**"的等待。抓取类短等待（`awaitSignInURL`、`fetchOffers`、`probeSubscription`）不要接：多一次输入所有权没有收益，还会与审批 overlay 抢锁。
- **owner 裁决 ④（等待期键入丢弃）是有意的语义变更**：今天那点"保留"只是 `events` 8 槽缓冲的副产品（缓冲满即冻结、恢复后补齐），不是契约。若将来要恢复"打字落到输入框"，正确做法是让等待循环把**未解析的字节**喂回 `keepTerminalInputForInputPipeline`（`input_events.go:235`），而不是把 `readKey` 的解析结果再编码回字节（那是第二套解码器）。
- **owner 裁决 ③（Esc = 放弃登录）**：走 `login.Cancel` + 向导上一页；屏上那个 device code 就此作废，下次 `/connect` 拿新的。若将来要"只停等待、登录后台继续"，需要一条后台完成→UI 的通知路径（可参考 `/mcp auth` 的 `done` 回调，`chat_slash.go:1246-1300`）。
- **Windows 一致性的钥匙是 `canGateTerminalReads`**（`platform_windows.go:20`）：真要在 Windows 上启用，先做出一个"有字节才返回"的可门控读（例如独立读线程 + `WriteConsoleInput` 注入唤醒），再把该常量置真——`AwaitSignIn` 一行都不用改。
- 若将来 `/connect` 改成 `/mcp auth` 那样的异步等待（循环存活、后台报通知），`AwaitSignIn` 可整体删除，测试随之删。
- 语义维护点：`ErrCancelled` → `credentials()` 返回 `(false, nil)` = 向导上一页（`setup.go:526-556`）。改向导分页语义时这条要一起看。
- 审查者请重点看三处：(a) `pauseSharedInput` 拆出后 `tryAcquireComposerInput` 的 Begin/End overlay 顺序与嵌套 release 语义是否仍与原来一致；(b) 等待期间"其它按键丢弃"是否有任何路径泄漏到 composer；(c) `rawKeyEscape` 的 settle 二次查询是否会误吞真 Esc（应当只有分片报告时才不取消）。
- 未覆盖窗口（如实登记）：paste 流程"打印页面 → 粘贴 prompt"之间亚毫秒窗口；`awaitSignInURL` 的至多 30 s；`probeSubscription`/`fetchOffers` 的至多 10 s。

---

## Implementation Status

**结论：COMPLETE。§5 Step 0–7 全部按计划执行，5 个 in-scope 文件落地，零 commit，改动留在工作区。验收记录见下。**

### 已落地清单（相对基线 HEAD `a1da14e4` + 工作区 WIP，`git diff --stat`）

| 文件 | 内容 |
|---|---|
| `pkg/tui/channels.go` | `ConnectWaitSelector` 可选能力接口（+15 行，`Selector` 接口未加方法） |
| `pkg/tui/overlays.go` | 拆出 `pauseSharedInput`（`tryAcquireComposerInput` 变为调用它 + Begin/End overlay，行为不变）、新增 `AwaitSignIn` 与 `connectWaitPollMillis`/`connectWaitEscapeSettleMillis`（`dispatchMouse` 之后；+110/−12） |
| `pkg/tui/setup.go` | `signIn` 等待段：`login.Wait` 进 goroutine（`waitCtx`/`outcome` 容量 1），`ConnectWaitSelector` 在则 `AwaitSignIn`，否则 `<-outcome`；`ErrCancelled` → `(false, nil)` 退回上一页并记 `llmsetup.sign_in_declined`（+26/−6） |
| `pkg/tui/overlays_test.go` | T0 `TestSignInWaitWithoutAnInputOwnerLosesTheDrag` + T2 五例 + 共用夹具 `signInWaitPage`/`signInWaitRenderer`/`sgrMouseReport`/`sgrDrag`/`selectCopiedText`（+281） |
| `pkg/tui/setup_test.go` | `fakeLogin.waitRelease`（让 Wait 挂住）+ `setupWaitSelector` + T1 四例（+133） |

### 验收记录（2026-11-14）

**Step 0a 复现（离线，改代码前）**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestSignInWaitWithoutAnInputOwnerLosesTheDrag -count=1 -v`
→ PASS，日志 `overlays_test.go: the reader decoded the report (kind=7) — stuck in the channel`，`HasSelection()` 为假、footer 无 copied：缺陷链在源码里成立。

**Step 0b 真机复现（修复前，fake 模型 `driver.sh`：WORK=`$TMPDIR/forebrain-run-signin`、SESSION=`fb-signin`、PORT=8763、`SSH_CONNECTION=scratch` 以免开浏览器）**：
- A 场景（基线）：对话区第 21 行 `● hello from the fake model` 拖拽 → footer `copied 25 chars to clipboard`，`tmux capture-pane -e` 第 21 行含 `^[[48;5;24mhello from the fake model^[[49m`。
- B 场景（本 bug）：`/connect` → Volcengine Ark → `y` → 等待页（第 21 行 URL、第 23 行 `GM5M-6HSR`、第 35 行 `Waiting for you to confirm the code in Volcengine Ark…`），对两行各做一次 press→drag→release：footer 仍 `100%/1M`、无 copied；两行 SGR 无任何背景色（无高亮）；期间 `Escape` 被无视，计时继续走到 `(19s)`：完整复现。

**Step 6 真机验收（重建二进制后，同一配方）**：
- A（回归）：再次拖拽同一行 → `copied 25 chars to clipboard` + `^[[48;5;24m…^[[49m`，与修复前逐字一致。
- B（修复生效）：等待页第 23 行 `D7UE-FQ91` 拖拽 → footer `copied 9 chars to clipboard`，SGR `^[[48;5;24mD7UE-FQ91^[[49m`，`pbpaste` = `D7UE-FQ91`（与屏上一致）；第 21 行 URL 拖拽（列 1→40）→ `copied 39 chars`，SGR 背景覆盖 `Open https://signin.volcengine.com/auth`，`pbpaste` 同文。
- 有选区时 `Ctrl+C`：只复制（`copied 9 chars`），等待继续 tick（`Waiting for you to confirm the code in Volcengine Ark… (9s)`），登录不取消。
- C（Esc 放弃）：等待期间 `Escape` → 1 s 内回到 `Connect a model` provider 页，登录页 URL/code 仍留在 transcript 里，`tmux list-sessions` 进程存活，`tui.err` 为空、无报错行；再次 `/connect` 打出**新的** trace_id 与 code（`8Y25-W84A`，不是 `D7UE-FQ91`），佐证旧 code 已作废。
- 附带确认：无选区时 `Ctrl+C` 同样放弃登录并退回 provider 页。
- 滚轮：本次验收 transcript 不足一屏、无可滚动内容（`maxScrollOffset` 为 0），真机未产生可见位移；滚轮走的是同一条 `dispatchMouse`，既有 `TestApprovalOverlayWheelScrollsTallOverlay`、`TestQuestionOverlayWheelScrollsTallOverlay` 覆盖该路径，且 pkg/tui 全绿。
- D：`$D stop` + `rm -rf $TMPDIR/forebrain-run-signin`，session 已清理。

**机器门禁**（`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`）：
- `CGO_ENABLED=1 go build -tags fts5 ./...` → PASS；`CGO_ENABLED=1 go vet -tags fts5 ./...` → PASS；`gofmt -l cmd pkg third_party` → 本会话文件无输出。
- `go test -tags fts5 ./pkg/tui` → PASS（87 s），含 T0 + T1 四例 + T2 五例，按名字可见。
- `go test -tags fts5 ./pkg/architecture` → FAIL 3 例，**全部与本改动无关、且均在改动之外**：(1) `TestSQLiteStatementsUnchanged` —— 快照 437 vs 实测 440，差的是 `pkg/state/schema_migrations.go` 里三条 `cap_images = NULL` 迁移；已在 `.worktrees/plan-review-4267496826`（干净 HEAD `a1da14e4`）原样复现，属预存缺陷（快照未随迁移更新，需 `go run ./scripts/sqlite-sql-snapshot` 且必须是那次迁移的作者来做）。(2)(3) `TestFileNamesHaveAtMostTwoUnderscores`、`TestTestFilesCorrespondToProductionFiles` —— 被另一会话未跟踪的 `pkg/tui/subagent_footer_display_test.go` 触发。

### 与计划的偏差

1. **T2 的 pipe 用例多出一段 2 s 停顿**：没有后台 reader 时 `pauseSharedInput` 的 ack 重试窗口会走满（真机有 reader，~2 ms 返回）。因此 `TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns`、`TestAwaitSignInReportsAReadError` 的等待上界放宽到 5 s；`TestAwaitSignInEndsWhenTheFlowEndsWithoutAKey` 改为带上真实 `readRawInputEvents` reader（真机拓扑），保持计划要求的 1 s 上界。意图、断言与门禁不变。
2. **`TestAwaitSignInFallsBackWhenTheInputIsOwned` 用"测试直接持有 `stdinReadMu`"模拟占用**，而不是计划写的 `rs.acquireComposerInput()`：`acquireComposerInput` 成功后立刻释放 `stdinReadMu`（modal 的所有权靠 pause 深度），所以先调用它并不能让 `TryLock` 失败；真正让 `pauseSharedInput` 返回 false 的是"有人正在 `readKey`"（它全程持锁）。用例因此改为持锁建模，并额外断言 `interactiveInputPauseDepth == 0`（没抢所有权）。
3. **T1 用例补了 `confirms: []bool{true}`**：`fakeSelector` 未脚本化时 `Confirm` 返回 `signInPrompt` 的默认值 `false`（= 不登录），会让"完成路径"用例走成"拒绝登录"；补上确认脚本才测到目标路径。
4. 计划 §6 T1 说"`fakeLogin` 加 `waitCh chan error` / `hold chan struct{}`"，落地为单个 `waitRelease chan error`（一个控制位即可表达"等待中"，并能在取消时随 `ctx` 退出）。

### 遗留（如实登记，未做）

- paste 流程"打印页面 → 粘贴 prompt"之间亚毫秒窗口未接输入所有权（计划 §4 已登记）。
- `awaitSignInURL`（≤30 s）与 `probeSubscription`/`fetchOffers`（≤10 s）仍未接所有权（计划 §4 排除理由）。
- `AwaitSignIn` 内 `readKey()` 的既有性质：一个残缺的鼠标报告（`ESC[<` 之后字节不再到达）会让它阻塞在 Read 里，此时即使 `done` 已到也要等字节到来才返回。这是 `readKey` 全产品既有行为，本期未改（计划未纳入）。
- 真机滚轮未取得可见证据（原因见上）。
- 审查用的 worktree `.worktrees/plan-review-4267496826`（含不合命名规范的 `zz_review_signin_test.go`）未删除、未并入主树，留待 owner 处置。

