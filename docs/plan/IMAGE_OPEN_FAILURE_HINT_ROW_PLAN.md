# TUI：图片打开失败改为 composer 提示行（替代 transcript error 卡）

状态：**已实施**（2026-11-15，executor 报告见文末 `## Implementation Status`；§13 已回填）。原 §5 点名的「预存 flaky」经复现确认是**真实数据竞争**，按 owner 裁决「预存缺陷必须本期修」已修根因，包级门禁现在**不带 `-skip`** 也 4/4 轮全绿。
Planned at：`a1da14e4` 起，**已在 `3506aea8`（2026-11-14，dev）重新核实**——§9 的五处内容锚点在该 HEAD 上全部命中，行号可能已漂移，一律以内容为准。drift 只认内容，不认 stat 数字。
实施基线：起手 `3506aea8`（工作树含并行会话 a6 的 WIP）；**执行中途 a6 按 owner 指令 bulk commit，HEAD 变为 `9f23024a`**，本计划的改动落在 `9f23024a` 之上（见 Implementation Status 的 deviations）。
实施时**第 0 步**把本文件复制为 `docs/plan/IMAGE_OPEN_FAILURE_HINT_ROW_PLAN.md`（`docs/plan/` 已 gitignore，不入库）

**owner 裁决（2026-11-14，按时间顺序；后者覆盖前者）**
1. 容器：error 卡 → composer 顶框上方的瞬时提示行；时长 **8 秒**，与 `Image in clipboard · ctrl+v to paste` 同一条行、同一个常量
2. 范围：**所有**打开失败（文件已删/看图程序拒绝/无看图程序/权限错误）都走提示行，transcript 不再出现 open 失败卡
3. 文案：宽裕时保留原文 `open <文件名>: <原因>`；一行放不下时退化为**只显示原因**
4. 静默状态：队列预览占位、slash 输入模式、idle subagent 的只读 composer —— **三处都要画**，没有 spacer 行时提示可见期间 composer 临时多几行
5. modal 遮罩（审批卡/提问卡）占屏时：**不响应 transcript 图片点击**（遮罩把 composer 整块替掉，提示画不出来 → 干脆不让点）
6. 极窄终端：**soft wrap 换行**，不截断、不省略号；连原因都放不下时按终端宽度换行显示
7. 极长失败文案（如 2048B launcher stderr）：**同样只换行，永不截断**，不设行数预算（知道代价：composer 会临时变高，且 composer 自己超过 2/3 屏时提示行会被既有 caret 滚动挤出可视区）

---

## 1. 目标（Why this matters）

图片消息卡 output 区域的图片行（`▤ logs-debug.png · 800×600 · 42.1 KB · click to open`）在文件已删除后仍可点。点一次就往 transcript 追加一张永久 error 卡：

```
● error
  open logs-debug.png: file no longer exists
```

用户连点 5 次就是 5 张一模一样的卡（截图现场），把滚动区填满、把对话挤走，而且**没有任何可修复的动作**。同仓早有一条为「一次性 UI 反馈」准备的通道：composer 顶框上方的瞬时提示行（`Image in clipboard · ctrl+v to paste` 用的就是它：右对齐、`\x1b[2m` 灰色、8 秒自动消失、不进 transcript、不进回放）。本计划把 open 失败**归一到这条既有行**。

## 2. 根因（证据链，逐环已验）

1. 点击图片行 → 解析该步 `PreviewPath` 与标签：`pkg/tui/reducer.go:2768-2789` `imageMarkerAtScreenRow`（只认 marker 行）。
2. 释放路径调 `openImagePreview`：`pkg/tui/reducer.go:7510-7518`（`viewportRelease`）；点击路径同：`reducer.go:6354-6356`。
3. 缺失判定：`pkg/tui/image_open.go:32-44` `openWithSystemViewer`（`os.Stat` → `errors.Is(err, fs.ErrNotExist)` → `errors.New("file no longer exists")`，不启动任何查看器）。
4. **【根因】** `pkg/tui/image_open.go:129-139` 把所有失败交给 error 卡：`renderer.PrintError(fmt.Errorf("open %s: %w", label, err))`。
5. `PrintError`（`pkg/tui/render.go:875-882`）→ `appendFrameViewportLocked`（`reducer.go:5754-5767`）→ `retainFrameLocked` 追加 `r.vm.blocks`。`retainCardOnceLocked`（`reducer.go:5769-5786`）只去重**带 EventID 的 error 帧**，`PrintError` 的帧没有 EventID → **点 N 次 = N 张卡**（复现证据：§7 Step 0.5）。这些卡只活在渲染器内（不写 state DB），同一屏内永不过期。

一次物理点击只触发一次 open：当前鼠标事件生产者只有 `pkg/tui/input_events.go:900-922` 的 press/release（`inputEventMouseClick` 无生产者），截图里的多张卡是多次点击。

## 3. 修复设计

### 3.1 提示槽：剪贴板专用 → 通用 composer 提示行（纯重构 + 三项加固）

现状：唯一状态位 `clipboardImage bool`（`pkg/tui/render.go:602-605`），唯一文本硬编码（`reducer.go:8104-8168`），唯一绘制点在普通 composer 的 spacer 行（`reducer.go:4023-4032`）。改成一个文本槽 + 宽窄两形态 + 多行绘制：

`pkg/tui/render.go` 替换 602-605 那 4 行：
```go
	// composerHint is the transient hint painted in the rows above the
	// composer's top rule (ShowComposerHint); composerHintNarrow replaces it
	// once it no longer fits one row; composerHintTimer takes the rows down
	// when their time is up, and composerHintGen makes a timer that fired
	// before Stop reached it unable to wipe a newer hint.
	composerHint       string
	composerHintNarrow string
	composerHintTimer  *time.Timer
	composerHintGen    uint64
```

`pkg/tui/reducer.go` 替换 8104-8168 整段（`clipboardImageHintText` 常量原样保留）：
```go
// clipboardImageHintText is the composer hint shown while the clipboard holds
// an image.
const clipboardImageHintText = "Image in clipboard · ctrl+v to paste"

// composerHintTimeout is how long a composer hint stays up once shown,
// matching Claude Code's clipboard-image-hint (8s). A var, not a const, so
// tests can shorten it.
var composerHintTimeout = 8 * time.Second

// SetClipboardImageAvailable shows or hides the paste hint. true shows it and
// auto-expires it after composerHintTimeout even if the clipboard still holds
// the image; a repeated true while it is already up keeps the original expiry.
// false hides it at once. When to show at all — debounce, rate limit — is the
// clipboardImageWatcher's call, not the renderer's.
func (r *Renderer) SetClipboardImageAvailable(on bool) {
	if on {
		r.ShowComposerHint(clipboardImageHintText, "", composerHintTimeout)
		return
	}
	r.HideComposerHint(clipboardImageHintText)
}

// ShowComposerHint puts text on the rows above the composer for d. narrow, when
// non-empty, is what the hint becomes once text no longer fits one row: a
// message's head names the thing the user just clicked and can go, while its
// tail carries the reason.
//
// The hint carries one message at a time: a different text replaces it and
// restarts the clock, while the text already there keeps the expiry it was
// given — a caller that keeps saying the same thing (the clipboard watcher's
// poll, a user clicking the same dead row) must not pin the hint up.
//
// This is the surface's answer to "something the user just did could not
// happen, and it costs nothing to try again": it replaces no content, expires
// on its own, and never enters the transcript.
func (r *Renderer) ShowComposerHint(text, narrow string, d time.Duration) {
	if r == nil || strings.TrimSpace(text) == "" {
		return
	}
	text, narrow = oneLineHint(text), oneLineHint(narrow)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.composerHint == text && r.composerHintTimer != nil {
		return
	}
	if r.composerHintTimer != nil {
		r.composerHintTimer.Stop()
	}
	r.composerHintGen++
	gen := r.composerHintGen
	r.composerHint, r.composerHintNarrow = text, narrow
	r.composerHintTimer = time.AfterFunc(d, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		// A timer that had already fired when the next hint replaced it must
		// not take that newer hint down: the generation says whose expiry
		// this still is.
		if r.composerHintGen != gen {
			return
		}
		r.composerHint, r.composerHintNarrow, r.composerHintTimer = "", "", nil
		if r.viewportMode {
			r.paintViewportLocked()
		}
	})
	if r.viewportMode {
		r.paintViewportLocked()
	}
}

// HideComposerHint takes the hint down when it still says text — the hint its
// own caller put up. Another caller's hint is left alone.
func (r *Renderer) HideComposerHint(text string) {
	if r == nil {
		return
	}
	text = oneLineHint(text)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.composerHint == "" || r.composerHint != text {
		return
	}
	if r.composerHintTimer != nil {
		r.composerHintTimer.Stop()
		r.composerHintTimer = nil
	}
	r.composerHintGen++
	r.composerHint, r.composerHintNarrow = "", ""
	if r.viewportMode {
		r.paintViewportLocked()
	}
}

// oneLineHint is a hint as the rows can paint it: printable text with its
// whitespace runs folded to single spaces. A newline or an escape from a
// launcher's stderr would otherwise break the painted rows or write into the
// terminal; folding and stripping are the two rules the composer status row
// already follows.
func oneLineHint(s string) string {
	return strings.Join(strings.Fields(sanitizeTerminalInputText(s)), " ")
}

// composerHintRows paints the hint: no rows when there is none, otherwise the
// text (or its narrow form once it no longer fits one row) wrapped to the
// terminal width and right-aligned as one block — every row starts at the
// column the WIDEST row leaves, so the widest row ends exactly at the right
// edge and a row wider than the terminal is what this alignment exists to
// prevent (a wide rune on a one-cell-wide terminal is wrapCardLine's own
// pre-existing edge, not this row's). Nothing is ever cut: a message too long
// to fit one screen simply takes the rows it takes (owner, 2026-11-14).
// Caller holds r.mu.
func (r *Renderer) composerHintRows(termWidth int) []string {
	if r.composerHint == "" || termWidth < 1 {
		return nil
	}
	hint := r.composerHint
	if runewidth.StringWidth(hint) > termWidth && r.composerHintNarrow != "" {
		hint = r.composerHintNarrow
	}
	rows := wrapCardLine(hint, termWidth)
	widest := 0
	for _, row := range rows {
		if w := runewidth.StringWidth(row); w > widest {
			widest = w
		}
	}
	pad := termWidth - widest
	if pad < 0 {
		pad = 0
	}
	indent := strings.Repeat(" ", pad)
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = indent + sharedBlockFooterStyle + row + sharedBlockReset
	}
	return out
}
```
（`sharedBlockFooterStyle` = `"\x1b[2m"`，`render.go:77`；`sanitizeTerminalInputText` 已存在，`tty_session.go:195`；`wrapCardLine` 已存在，`render.go:5080`，含硬断长词。全部复用，不新造任何截断/换行工具。）

三道保证的由来（四轮评审逐条复现，本计划采纳）：
- **陈旧 timer 竞态**：旧代码单生产者，`Stop()` 返回 false 时已 fired 的回调会无条件清状态。加了第二个生产者（launcher 的异步 `failed` 回调）后必须用 `composerHintGen` 判归属，否则刚显示的提示会被上一条的 timer 立刻抹掉。
- **单行不变量**：launcher stderr 可达 2048B 且含换行（`image_open.go:60,70-73`），直接塞进绘制会带 `\n`（破坏行结构）或带 `\x1b`（写进终端）→ 必须先折叠 + 去控制序列。
- **右对齐必须按最宽行**：按最窄行算 pad 会让宽行超出 `termWidth`，被绘制层（`canvasRow`，`reducer.go:4583-4593`）裁掉尾部——正好把原因切掉。这里 `pad = termWidth - widest`。
- **不设行数预算（owner 裁决 7）**：只换行、永不截断。代价已知并接受：极长失败会把 composer 临时撑高；另外 composer 自身超过 2/3 屏时会被 `paintViewportLocked` 的高度上限（`reducer.go:4852-4905`）裁剪并按 caret 滚动，位于 caret 之上的提示行可能被滚出可视区——**这是既有局限，剪贴板提示今天同样如此**（40 行草稿 + 提示就会出现），不是本计划引入；修它等于把提示钉在滚动窗口之外，属于另一件事（见 §12）。

### 3.2 绘制点（owner 裁决 4：三处都画；裁决 5：遮罩期间不响应点击）

**(a) 普通 composer（`reducer.go:3991` `buildNormalComposerBlock`，现 4023-4032）**：无预览时提示占据 spacer 行（单行时行数不变，多行时多几行）；有队列预览时在预览之后**追加**提示行：
```go
	if len(previewLines) > 0 {
		lines = append(lines, previewLines...)
	}
	// The hint rows double as the spacer above the card: with no preview
	// queued they are the rows that would have been blank anyway, and with a
	// preview they add transient rows rather than hiding the hint — the same
	// way a queued preview itself makes the composer taller.
	if hint := r.composerHintRows(termWidth); len(hint) > 0 {
		lines = append(lines, hint...)
	} else if len(previewLines) == 0 {
		lines = append(lines, "")
	}
```
（`cursorRow = len(lines) + 1` 在其后计算，自动跟随。）

**(b) 只读 composer（`reducer.go:3863` `buildReadOnlyComposerBlock`，idle subagent 视图）**：没有 spacer 行；在 footer 之前追加：
```go
	if hint := r.composerHintRows(termWidth); len(hint) > 0 {
		lines = append(lines, hint...)
	}
	if len(cs.OverlayRows) == 0 {
		lines = append(lines, sharedBlockFooterStyle+footerText+sharedBlockReset)
	}
```
（该块 `cursorRow` 恒为 `-1`。）

**(c) slash 输入模式（`reducer.go:4077` `buildSlashComposerBlock`）**：同样没有 spacer 行；在 `previewLines` 之后（4104-4106）、`if cs.SlashMenu != nil`（4107）之前追加同样三行。

**(d) 队列预览占位**：见 (a) 的第二个分支。

**(e) modal 遮罩（owner 裁决 5）**：遮罩活跃时 `paintViewportLocked` 用遮罩自己的行替换整个 composer（`reducer.go:4749-4790`），提示没有可画之处 → **不让这次点击发生**。`transcriptClickLocked`（`reducer.go:6368-6382`）里把图片解析放进 `!r.overlayActive` 条件：
```go
	vm := r.activeVMLocked()
	// A modal overlay owns the composer area while it is up, so the hint rows
	// the click would answer on are not painted: the image row stays inert
	// instead of accepting a gesture whose only feedback cannot be shown.
	if !r.overlayActive {
		imagePath, imageLabel, _ = vm.imageMarkerAtScreenRow(s.render, row)
	}
```
（折叠与子代理视图的点击语义不动；只有「打开图片」这条没有可见反馈的路径被关掉。）

`composerSuppressed` **不是**静默状态：生产代码里只有 `render.go:4619` 把它置 `false`，无处置 `true`（其注释说明 viewport 模式不走它）。

### 3.3 打开失败改走提示行

`pkg/tui/image_open.go` 替换 `openImagePreview`（129-139）：
```go
// openImagePreview hands one already-resolved image step to the system viewer.
// A click resolves it on the screen its press landed on (viewportRelease). A
// miss is not an error worth saying: the marker line is the affordance, so
// nothing at all happens when no file was named. A file that was named and
// could not be opened is said on the composer's hint rows — the rows the
// clipboard's paste hint uses — because the click is a gesture the user can
// repeat at no cost, while a transcript error card is permanent: a marker row
// outlives its file, and one click per card soon filled the scrollback with
// identical "open …: file no longer exists" cards.
//
// The card's wording survives in full; only the rows are transient. The reason
// alone is the narrow form, so a hint that no longer fits one row drops the
// file name before anything is wrapped.
func openImagePreview(renderer *Renderer, path, label string) {
	if renderer == nil || strings.TrimSpace(path) == "" {
		return
	}
	report := func(err error) {
		renderer.ShowComposerHint(
			llm.ExplainError(fmt.Errorf("open %s: %w", label, err)),
			llm.ExplainError(err),
			composerHintTimeout,
		)
	}
	if err := imagePreviewOpener(path, report); err != nil {
		report(err)
	}
}
```
- 全形文案 = 今天 error 卡的**同一个构造**（`llm.ExplainError(fmt.Errorf("open %s: %w", …))`），`open logs-debug.png: file no longer exists` 一字不差。
- 窄形 = 同一错误的 `llm.ExplainError(err)`（即 `file no longer exists`）。
- 新增 import：`"github.com/forebrain-harness/forebrain-harness/pkg/llm"`。`pkg/tui` 早已 import `pkg/llm`（`render.go:881`），**包级 import 图不变**。
- `openWithSystemViewer` / `startDetached` / `imagePreviewOpener` / `imageMarkerAtScreenRow` / 鼠标事件解析一行不动；`label` 参数保留（宽形要用）。
- 无锁风险：两个调用点都在 `r.mu` 之外（`reducer.go:6354-6356`、`7510-7518`），`ShowComposerHint` 自己拿锁。

### 3.4 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 点图片行，文件在 | 打开查看器 | 不变 |
| 点图片行，文件已删 | 永久 error 卡，每点一次加一张 | 提示行 `open X: file no longer exists`，8s 自动消失，transcript 零新增；过期后再点重现 |
| 看图程序拒绝/无看图器/权限错误 | 永久 error 卡 | 提示行（同上） |
| 同一行连点 5 次（未过期） | 5 张卡 | 1 条提示，**不重排**过期时间 |
| 过期后再点 | 第 6 张卡 | 提示重新出现（新的 8s） |
| 剪贴板有图 | 提示行，8s；窄终端被 `runewidth.Truncate` 右截成 `Image in clipboa…` | 宽 80 逐字节不变；**窄终端从「右截+`…`」变成「换行」**（裁决 6 的刻意修订，同一常量、同一行位置） |
| 剪贴板提示在显示时点了失效图片 | —（不可能） | 文本被替换；剪贴板回落沿的 `HideComposerHint(剪贴板文本)` 不会误删别人的提示 |
| 一行放不下 | （卡里换行显示，不截断） | 先退化为只显示原因；仍放不下则**换行**（右对齐块，按最宽行对齐，永不省略；宽 1 + 宽字符那一个 `wrapCardLine` 既有边界见 §12） |
| 极端长错误（如 2KB launcher stderr） | 卡里全量换行 | 先窄形；仍放不下就**全量换行**（可能多行、composer 临时变高），**永不省略**（裁决 7）；composer 自身高度上限的那条既有局限见 §12 |
| composer 自身超过 2/3 屏（如粘贴 40 行草稿） | 提示行可能被既有 caret 滚动挤出可视区 | **不变**：同样的既有局限，本计划不修（修法是把提示钉在滚动窗口之外，另立项） |
| 队列预览可见时点失效图片 | error 卡 | 提示行（预览下方追加，8s 后收回） |
| slash 输入模式点失效图片 | error 卡 | 提示行（同上） |
| idle subagent 视图点失效图片 | 该视图一张 error 卡 | 该视图的 composer 提示行——**提示画在点击所在视图** |
| modal 遮罩占屏时点失效图片 | 打开尝试失败 → 遮罩下的 error 卡 | **不响应**：图片行 inert，无卡无提示（裁决 5） |
| launcher stderr 多行/含 ESC | 卡 body | 折叠成一行 + 去控制序列后再画/再换行 |
| 回放 / web | error 卡从未落库 | 不变：提示只在 live 屏；回放的 error 卡来自 run 事件（`commands.go:815`），与本路径无关 |

## 4. Scope

**In scope（只改这 5 个 Go 文件）**
1. `pkg/tui/render.go`（仅 602-605 → 8 行字段）
2. `pkg/tui/reducer.go`（仅 4023-4032 / 3878-3880 前 / 4106 后三个绘制点 + `transcriptClickLocked` 6368-6382 的遮罩门 + 8104-8168 段重写）
3. `pkg/tui/image_open.go`（仅 `openImagePreview` + `llm` import）
4. `pkg/tui/image_open_test.go`（2 个既有用例改写 + 新增用例 + 2 个辅助）
5. `pkg/tui/tty_session_test.go`（跟随改名 + 改写 `TestClipboardImageHintLine` + 新增用例）
+ 计划文件 `docs/plan/IMAGE_OPEN_FAILURE_HINT_ROW_PLAN.md`（gitignore，不入库）

**Out of scope（明确不碰，含同类项及理由）**
- `pkg/tui/tty_session.go`：`clipboardImageWatcher` 只调 `SetClipboardImageAvailable`，API 名不变。
- `openWithSystemViewer` / `startDetached` / 鼠标事件解析 / `imageMarkerAtScreenRow` / 折叠与子代理视图点击语义（遮罩期间也只有图片解析被关掉）。
- `imagePreviewFrameLine`（marker 行文案 `▤ … · click to open`）、图片卡永不折叠、`clipboardImageHintText` 文案、8s 时长。
- **`openInBrowser` / `browserOpener`（`image_open.go:105-121`）**：同一 `startDetached` 家族，但它是登录动作，失败时 URL/code 仍在屏上可抄（另一会话正在修该处拖拽选中），`setup.go` 也正被别人改 → 单独立项。
- **其余 `PrintError` 一律保留（按类排除，逐族点名）**：`pkg/tui` 里 `PrintError(` 现有 100+ 处，按下述三类整体排除，不逐条改写——
  - slash `/` 命令与交互式切换失败：`chat_slash.go`、`commands.go:102-187,2372-2960`、`config_panel.go:165,443`；
  - 技能版本/项目 MCP 配置失败：`skills_versions.go:776-1032`、`project_servers.go:214,227`；
  - 通知/会话装载等后台失败：`notify.go:2340,2365`、`setup.go:2235-2272`。
  它们的共同点：错误**可诊断、可行动**，并且是一次性动作的失败记录，transcript 是正确归宿；与「点一下就重试」的图片行不是同一类。
  机器判据（§9）：`grep -rc 'PrintError(' pkg/tui | awk -F: '{s+=$2} END {print s}'` 改动后**恰好比改动前少 1**（`image_open.go` 那一处），其余文件一处不减。
- **web `frontend/src/components/chat/ToolCallCard.vue:67-79`**（`<a :href="previewUrl" target="_blank">`，文件删掉后新标签页 404）：不同 surface、反馈由浏览器给出 → 单独立项。
- `pkg/tui/notify.go`、`run.go`、`overlays.go`、`setup.go`、`channels.go`、`render_test.go`、`overlays_test.go`、`setup_test.go`、`subagent_footer_display_test.go`：他人文件（工作树里可能有并行会话的在途改动，见 §5；本计划不碰它们，也不修 `overlays_test.go` 里那条预存 flaky）。
- 遗留 `r.overlayComposer` 路径（`reducer.go:4840-4847`）：遮罩期间现在不响应图片点击，该路径无需绘制。

## 5. 当前状态摘录（executor 逐字对照）

`pkg/tui/render.go:600-605`
```go
	// clipboardImage is true while the transient paste hint is visible (shown
	// by SetClipboardImageAvailable, auto-expired via clipboardImageTimer).
	clipboardImage      bool
	clipboardImageTimer *time.Timer
```

`pkg/tui/reducer.go:4023-4032`（普通 composer）
```go
	if len(previewLines) > 0 {
		lines = append(lines, previewLines...)
	} else {
		// Without a queue preview, retain the usual single spacer before the
		// composer card. A visible preview already provides that boundary and
		// should sit directly against the card instead of leaving an empty row.
		// While the clipboard holds an image, that same row carries the paste
		// hint, right-aligned over the card's top rule.
		lines = append(lines, r.clipboardImageHintLine(termWidth))
	}
```

`pkg/tui/reducer.go:3878-3880`（只读 composer 尾部）
```go
	if len(cs.OverlayRows) == 0 {
		lines = append(lines, sharedBlockFooterStyle+footerText+sharedBlockReset)
	}
```

`pkg/tui/reducer.go:4104-4107`（slash composer：预览后、菜单前）
```go
	if len(previewLines) > 0 {
		lines = append(lines, previewLines...)
	}
	if cs.SlashMenu != nil {
```

`pkg/tui/reducer.go:6368-6382`（`transcriptClickLocked` 开头，遮罩门插在这里）
```go
func (r *Renderer) transcriptClickLocked(s shownScreen, row int) (changed bool, imagePath, imageLabel string) {
	if row < 0 || row >= s.bodyHeight || s.view != r.activeView {
		return false, "", ""
	}
	vm := r.activeVMLocked()
	imagePath, imageLabel, _ = vm.imageMarkerAtScreenRow(s.render, row)
	if r.openAgentViewAtRowLocked(s.render, row) {
		return true, imagePath, imageLabel
	}
	if vm.toggleAtScreenRow(s.render, row) {
		r.paintViewportLocked()
		return true, imagePath, imageLabel
	}
	return false, imagePath, imageLabel
}
```

`pkg/tui/reducer.go:8104-8168`、`pkg/tui/image_open.go:123-139`：见 §3.1 / §3.3 全文对照。

`pkg/tui/image_open_test.go`：`stubImageOpener` 24-34、`markerRowOf` 98-105、`TestImageMarkerHitTestOpensTheClickedStep` 125-152、`clickRow` 225-232（**它直接调 `imageMarkerAtScreenRow`，绕过 `transcriptClickLocked`**，§8 第 9 条据此设计）、`TestClickingTheImageMarkerRowLaunchesTheClickedFile` 238-279、`lastErrorFrame` 782-796、`TestClickingTheMarkerOfAFileSinceDeletedSaysSo` 798-817、`TestALauncherThatRefusesTheFileIsReported` 819-843。
`pkg/tui/tty_session_test.go`：`TestClipboardImageHintLine` 57-72、`clipboardHintState` 113-116、`TestClipboardImageHintExpiresWhileImageStays` 155-195（**155-157 行注释里也有 `clipboardImageHintTimeout` 字样，一并改**）、`TestClipboardWatcherRateLimitsRepeatHints` 213-227。

**并行会话基线（不修、不删；**执行时现取，不信任本文件的快照**）**
- **本文件写作期间工作树是脏的**：并行会话 `forebrain-harness-a6`（Presented files 交付物卡）正在改 `pkg/tui/{reducer.go,render.go,tty_session.go,commands.go,notify.go,chat_session.go,chat_turn.go}` 并新增 `deliverables_test.go`/`glyph_test.go`——**其中 `reducer.go`、`render.go` 也是本计划的 in-scope 文件**。已用 `peer_send` 对齐：a6 只动 `transcriptClickLocked`/composer hint 槽之外的地方（其改动集中在 reducer 前 3400 行、render 的 `renderDeliverables`），与本计划的 5 处插入点（render 600-605、reducer 4023/3878/4104/6368/8104）不重叠。
- **所以基线必须在执行时建立并抄进 §13**：`git rev-parse --short HEAD`、`git status --porcelain pkg/tui`、`git diff > "$TMPDIR/image-hint-baseline.patch"`（含他人改动）。§9 的 diff 判据以这份快照为基准比较，**不以"工作树干净"为前提**。
- 此前的 `inlineCodeColor` WIP 已随 `3506aea8` 提交，不再是未提交改动。
- `pkg/tui/subagent_footer_display_test.go`：**已提交**（`git ls-files` 可见）但 **gofmt 不干净**——`gofmt -l pkg/tui` 会打印它。这是他人文件里既有的格式缺陷，不属本计划（一行报告即可，不要替它格式化）。所以本计划的 gofmt 只查 5 个 in-scope 文件。
- `TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns`（`pkg/tui/overlays_test.go:3691`，并行会话刚提交的登录等待用例）：**疑似预存 flaky**——证据来自独立评审的多次全量跑（干净树两轮：第 1 轮该用例 FAIL、第 2 轮 `ok`；隔离单跑 8/8 绿，约 1/4 轮失败）；**我本人未复核**（plan mode 不能跑测试），执行者按 §6 的两条命令自行确认。处置：包级门禁用 `-skip` 把它排除（确定性），再单独 `-count=5` 跑一次确认它与本计划无关，并在最终报告里点名；**不修它**（不是本计划、也不是本计划的失败，且它的 owner 会话已结束，修它会越界改别人的测试）。隔离跑的失败按 §10 STOP 处理。
- `pkg/tui/zz_review_step0_test.go`：**已不存在**（2026-11-14 复核：并行会话 `forebrain-harness-63` 已把它连同修复一起提交进 `3506aea8`）。曾在主树留过一次性红色复现测试（`TestReviewStep0PlanTest`），是该会话 plan-review 的残留。**任何人都不得再把它当免责理由**：若它再次出现且失败 → 按 Step 0.6/STOP 处理，不加豁免。

## 6. 命令（CI 同款，已核实）

| 用途 | 命令 | 成功判据 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run '<正则>' -count=1` | `PASS` |
| 包级回归（**0 失败**） | `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -skip 'TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns' -count=1` | `ok`，**0 失败**（确定性：把那条已被点名的预存 flaky 排除，见下） |
| 预存 flaky 隔离确认 | `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run 'TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns' -count=5` | 5 次里 0 次失败 → 证明它与本计划无关；有任何一次失败 → STOP（那是别人真 bug，报告不动手） |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` | exit 0，无输出 |
| 格式 | `gofmt -l pkg/tui/render.go pkg/tui/reducer.go pkg/tui/image_open.go pkg/tui/image_open_test.go pkg/tui/tty_session_test.go` | 无输出 |
| 真机二进制 | `.claude/skills/run-forebrain/driver.sh build` | `built <TMPDIR>/forebrain-run/forebrain` |

**为什么可以用 `-skip`**：被跳过的是**他人文件里已点名的预存 flaky**（§5 第 3 条），它被单独跑一次确认与本计划无关，并在最终报告里点名——不是掩盖本计划造成的失败。**除它之外一律不许 skip**。
**不要**用整包 `gofmt -l pkg/tui` 当门禁（它会命中已提交的 `subagent_footer_display_test.go`，那不是本计划的文件）；**不要**把 `go test ./pkg/architecture` 当门禁（该包有他人 WIP 与预存失败；本改动不新增任何包级 import 边）。

## 7. Steps

### Step 0：计划落盘
本文件原样写到 `docs/plan/IMAGE_OPEN_FAILURE_HINT_ROW_PLAN.md`。
**Verify**：`ls docs/plan/IMAGE_OPEN_FAILURE_HINT_ROW_PLAN.md` 存在；`git status --porcelain docs/plan` → **无输出**。

### Step 0.5：先复现缺陷（临时测试，改前跑一次）
新建临时文件 `pkg/tui/image_hint_repro_test.go`。两条断言缺一不可：**A** 直调 `openImagePreview`（证明报告层），**B** 走真实点击路径 `ViewportSelectStart/End`（证明用户连点这一事实，不是只有直调才堆卡）。**注意不要在持 `r.mu` 时调 `lastErrorFrame`（它会再拿一次锁 → 自死锁）**。
```go
// countErrorCards reads the transcript's error cards under the renderer's own
// lock; call it unlocked.
func countErrorCards(r *Renderer) (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cards, newest := 0, ""
	for _, b := range r.vm.blocks {
		if b.frame.Kind == FrameError {
			cards++
			newest = b.frame.Content
		}
	}
	return cards, newest
}

// TestReproDeadImageClicksStackErrorCards is temporary: it records the defect
// this plan removes — one permanent transcript error card per click.
func TestReproDeadImageClicksStackErrorCards(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "shot.png")

	direct := NewRenderer(io.Discard, io.Discard)
	direct.viewportMode = true
	for i := 0; i < 5; i++ {
		openImagePreview(direct, gone, "shot.png")
	}
	if cards, newest := countErrorCards(direct); cards != 5 {
		t.Fatalf("direct calls appended %d cards, want 5 (newest %q)", cards, newest)
	} else {
		t.Logf("REPRO A: 5 direct calls appended %d permanent error cards; newest=%q", cards, newest)
	}

	// The same thing through the real click path a user drives.
	r := NewRenderer(io.Discard, io.Discard)
	r.viewportMode = true
	r.composerSuppressed = true
	r.vm.append(imageStepFrame("/tmp/click-repro.png"))
	r.vpHeight, r.vpBodyHeight = 10, 10
	r.vpLastRender = renderViewport(&r.vm, 80, 10, 0, DiffThemeDark)
	row := markerRowOf(r.vpLastRender.lines, "click-repro.png")
	if row < 0 {
		t.Fatalf("no marker row:\n%s", stripANSI(strings.Join(r.vpLastRender.lines, "\n")))
	}
	for i := 0; i < 3; i++ {
		clickRow(t, r, row) // ViewportSelectStart/End, the real release path
	}
	cards, newest := countErrorCards(r)
	t.Logf("REPRO B: 3 real clicks appended %d permanent error cards; newest=%q", cards, newest)
	if cards != 3 {
		t.Fatalf("real clicks appended %d cards, want 3", cards)
	}
}
```
**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestReproDeadImageClicksStackErrorCards -count=1 -v` → `PASS`，日志 `REPRO: 5 clicks appended 5 permanent error cards; newest="open shot.png: file no longer exists"`。
把日志抄进 §13，**然后删除该临时文件**（Step 3 用正式断言取代）。

### Step 0.6：清掉并行会话的红色残留（0 失败门禁的前提）
`ls pkg/tui/zz_review_step0_test.go`：2026-11-14 复核时已不存在（并行会话已提交修复 `3506aea8` 并删掉了它的红色残留）。这一步仍是硬门：**改动前包级测试必须本来全绿**，否则后面任何失败都分不清是谁造成的。
**Verify**：`ls pkg/tui/zz_review_step0_test.go 2>/dev/null` → 无输出；`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -skip 'TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns' -count=1` → `ok`（改动前就该是绿的；§6 说明了为什么这条 skip 不算掩盖）。
若出现其它失败：**STOP**，报告「0 失败门禁不成立」，不要开始改代码、不要替它改、不要加豁免。

### Step 0.7：建立并在 §13 记录基线快照
执行（**不要**指望工作树是干净的：并行会话 a6 正在改 `reducer.go`/`render.go`）：
```bash
git rev-parse --short HEAD
git status --porcelain pkg/tui
git diff -- pkg/tui > "$TMPDIR/image-hint-baseline.patch"
grep -c 'clipboardImage      bool' pkg/tui/render.go                     # 期望 1
grep -c 'r.clipboardImageHintLine(termWidth)' pkg/tui/reducer.go          # 期望 1
grep -c 'imagePath, imageLabel, _ = vm.imageMarkerAtScreenRow' pkg/tui/reducer.go  # 期望 1
grep -c 'renderer.PrintError(fmt.Errorf("open %s: %w", label, err))' pkg/tui/image_open.go  # 期望 1
```
把四条命令的输出抄进 §13。**五处内容锚点任一不命中 → STOP**（那说明有人改到了本计划的插入点，需要先重新对齐）。

### Step 1：提示槽泛化（纯重构 + 加固；**宽终端**剪贴板行为逐字节不变）
1. `pkg/tui/render.go`：§3.1 字段替换。
2. `pkg/tui/reducer.go`：§3.1 重写 8104-8168（`SetClipboardImageAvailable` 变瘦包装；新增 `ShowComposerHint`/`HideComposerHint`/`oneLineHint`/`composerHintRows`；删 `clipboardImageHintLine`；`clipboardImageHintTimeout` → `composerHintTimeout`；`clipboardImageHintText` 原样）。
3. `pkg/tui/reducer.go:4025-4032`（普通 composer 的 `else` 分支）改成**行数等价**的形状（本步只换绘制函数、保住 spacer，§3.2 的布局分支在 Step 3）：
   ```go
   	} else if hint := r.composerHintRows(termWidth); len(hint) > 0 {
   		lines = append(lines, hint...)
   	} else {
   		lines = append(lines, "")
   	}
   ```
   无提示时仍然恰好一行空行（今天是 `clipboardImageHintLine` 返回 `""` 的那一行），所以剪贴板行为逐字节不变。
4. `pkg/tui/tty_session_test.go` 跟随改名：
   - `r.clipboardImage = true` → `r.composerHint = clipboardImageHintText`
   - `return r.clipboardImage` → `return r.composerHint != ""`
   - `r.clipboardImageTimer` → `r.composerHintTimer`
   - `clipboardImageHintTimeout` → `composerHintTimeout`（含 155-157 行注释）
5. `TestClipboardImageHintLine`（57-72）整体改写为 `[]string` 断言（旧 helper 返回 `string`，直接改调用会编译失败）：
   ```go
   func TestClipboardImageHintLine(t *testing.T) {
   	r := NewRenderer(nil, nil)
   	if rows := r.composerHintRows(80); len(rows) != 0 {
   		t.Fatalf("hint shown without an image: %v", rows)
   	}
   	r.composerHint = clipboardImageHintText
   	rows := r.composerHintRows(80)
   	if len(rows) != 1 || !strings.Contains(rows[0], clipboardImageHintText) {
   		t.Fatalf("hint missing: %v", rows)
   	}
   	if w := runewidth.StringWidth(stripANSI(rows[0])); w != 80 {
   		t.Fatalf("hint not right-aligned, width %d", w)
   	}
   	// Owner's 2026-11-14 ruling: a hint narrower than its text WRAPS, it is
   	// never truncated.
   narrow := r.composerHintRows(5)
   	if len(narrow) < 2 {
   		t.Fatalf("hint did not wrap at width 5: %v", narrow)
   	}
   	// Every row carries its own SGR (the grey the clipboard hint has), so the
   	// text must be compared with the styling stripped: joining the styled
   	// strings would break a word across an escape sequence.
   	var plain strings.Builder
   	for _, row := range narrow {
   		text := stripANSI(row)
   		if w := runewidth.StringWidth(text); w > 5 {
   			t.Fatalf("row %q is %d cells wide, terminal is 5", text, w)
   		}
   		plain.WriteString(text)
   	}
   	if !strings.Contains(plain.String(), "clipboard") {
   		t.Fatalf("wrapped hint lost its text: %q", plain.String())
   	}
   }
   ```

**Verify**（注意：宽终端剪贴板输出逐字节不变，**窄终端从「右截 + `…`」变成「换行」是裁决 6 的有意修订，不得回退**）：
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run 'TestClipboard' -count=1` → 全绿
- `grep -rn 'clipboardImageHintLine\|clipboardImageHintTimeout\|r\.clipboardImage\b' pkg/tui` → **无匹配**
- `grep -rn 'clipboardImageHintText\|clipboardImageWatcher' pkg/tui` → 仍在（常量 + watcher 未改名）

### Step 2：打开失败改走提示行
`pkg/tui/image_open.go` 按 §3.3 改；`image_open_test.go` 的 2 个既有用例按 §8 第 1、2 条改写。
**Verify**：`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run 'TestClickingTheMarkerOfAFileSinceDeleted|TestALauncherThatRefusesTheFileIsReported|TestClickingTheImageMarkerRowLaunchesTheClickedFile' -count=1 -v` → 全 PASS

### Step 3：布局分支（§3.2 a/b/c）+ 遮罩门（§3.2 e）+ 新增测试（§8 第 3-11 条）
**Verify**：`CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run 'TestComposerHint|TestRepeatedClicksOnAMissingImage|TestAStaleComposerHintTimer|TestAModalOverlay' -count=1 -v` → 全 PASS

### Step 4：包级回归 + 静态检查（0 失败）
- `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -skip 'TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns' -count=1` → `ok`，**0 失败**（除 §5 点名的那一条外，任何失败 → STOP）
- `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -run 'TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns' -count=5` → 0 失败（确认那条 flaky 与本计划无关；有失败 → STOP）
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` → exit 0
- §6 的 5 文件 `gofmt -l` → 无输出

### Step 5：真机验收（§11）

### Step 6：收口
- 计划文件回填状态 + §13 验收记录（含 Step 0.5 复现日志、真机取证）。
- 清 tmux 会话与临时目录。
- **不 commit / 不 git add / 不 stash / 不 checkout**（owner 手动审阅提交）。

## 8. 测试计划（`pkg/tui`，CGO + `-tags fts5` + `-race`）

新增辅助（`image_open_test.go`，`lastErrorFrame` 旁；**返回值已去 ANSI 并 TrimSpace，右对齐补的空格不参与比较**）：
```go
// hintRowsFor returns the composer hint rows as painted at the given width,
// ANSI stripped and each row trimmed, so a test compares text rather than
// right-alignment padding.
func hintRowsFor(r *Renderer, width int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := r.composerHintRows(width)
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = strings.TrimSpace(stripANSI(row))
	}
	return out
}
```

1. **改写** `TestClickingTheMarkerOfAFileSinceDeletedSaysSo`（798-817）→ 更名 `TestClickingTheMarkerOfAFileSinceDeletedSaysItInTheHintRow`：`openImagePreview(r, gone, "shot.png")` 后
   - `strings.Join(hintRowsFor(r, 80), "\n")` 含 `open shot.png: file no longer exists`；
   - `lastErrorFrame(r) == ""`；
   - 未启动查看器（沿用原 `viewerDir/<program>.ran` 断言）。
2. **改写** `TestALauncherThatRefusesTheFileIsReported`（819-843）：`want := "open shot.png: No application knows how to open it."`；**先把 `composerHintTimeout` 临时放大（如 1 分钟，`t.Cleanup` 还原）**，再轮询 `strings.Join(hintRowsFor(r, 80), "\n")` 含 `want`（异步失败必须走通），并断言 `lastErrorFrame(r) == ""`。
3. **新增** `TestRepeatedClicksOnAMissingImageNeverStackCards`（取代 Step 0.5 临时复现）：连调 3 次 → 提示仍是那一条、`lastErrorFrame == ""`、`len(r.vm.blocks) == 0`。
4. **新增** `TestOneComposerHintSaysOneThingAtATime`（`tty_session_test.go`）：`ShowComposerHint(clipboardImageHintText, "", time.Second)` → 行文本是剪贴板提示；再 `ShowComposerHint("open a.png: file no longer exists", "file no longer exists", time.Second)` → 被替换；`HideComposerHint(clipboardImageHintText)` **不**清掉后者；`HideComposerHint("open a.png: file no longer exists")` 清空。
5. **新增** `TestComposerHintExpiresOnItsOwn`（`tty_session_test.go`）：照 `TestClipboardImageHintExpiresWhileImageStays` 的引子写法临时把 `composerHintTimeout` 改 50ms；先 `eventually` 等它出现，再等它自动消失。
6. **新增** `TestComposerHintDropsTheNameBeforeItWraps`（`tty_session_test.go`）：`ShowComposerHint("open logs-debug.png: file no longer exists", "file no longer exists", time.Second)`；
   - 宽 80 → 1 行，等于全形；
   - 宽 = `len("file no longer exists")`（21）→ **恰好 1 行**且等于 `file no longer exists`（无 `open`、无 `…`）；
   - 宽 8 → **≥ 2 行**、每行 `runewidth` 宽度 ≤ 8、拼起来含 `file`/`longer`/`exists`，且**全文没有 `…`**（换行而非截断）。
7. **新增** `TestComposerHintRowsNeverExceedTheTerminal`：宽 8 / 宽 5 两档，直接断言 `composerHintRows(width)` 的**原始行**（`stripANSI` 后不做 TrimSpace）`runewidth.StringWidth(row) <= width`；这是上一版「按最窄行补 pad 导致超宽被绘制层裁掉」的定点回归。**只用 ASCII**：宽 1 + 宽字符（CJK）会触发 `wrapCardLine` 既有行为（宽 1 时一个宽字符仍占 2 格），那与本计划无关，不在门禁内（见 §12）。
8. **新增** `TestComposerHintNeverBreaksItsRows`：`ShowComposerHint("open a.png: boom\nsecond line\twith tab\x1b[31m red", "", time.Second)` →
   - 每一行的 `stripANSI(row)` 不含 `\n`、`\t`，也不含任何 C0 控制字符（`\x1b` 已被 `oneLineHint` 剥掉）；
   - 原始行**只允许**首尾那两个已知 SGR（`\x1b[2m` 前缀 + `\x1b[0m` 后缀）——对齐裁决 1「同一条灰行」，不能把 `\x1b` 当脏字符整行禁掉；
   - 宽 20 时每一行 `stripANSI` 宽度 ≤ 20。
9. **新增** `TestAModalOverlayLeavesTheImageMarkerInert`（`image_open_test.go`，照 `TestClickingTheImageMarkerRowLaunchesTheClickedFile` 的骨架）。**注意 `clickRow` 内部直接调 `imageMarkerAtScreenRow`，绕过 `transcriptClickLocked`，所以不能断言 `isMarker == false`**，只断言结果：
   ```go
   opened := stubImageOpener(t)
   r := NewRenderer(io.Discard, io.Discard)
   r.viewportMode = true
   r.overlayActive = true                      // a modal owns the composer area
   r.vm.append(imageStepFrame("/tmp/shot.png"))
   r.vpHeight, r.vpBodyHeight = 10, 10
   r.vpLastRender = renderViewport(&r.vm, 80, 10, 0, DiffThemeDark)
   row := markerRowOf(r.vpLastRender.lines, "shot.png")
   clickRow(t, r, row)
   if len(*opened) != 0 {
   	t.Fatalf("a modal overlay must not open files: %v", *opened)
   }
   if rows := hintRowsFor(r, 80); len(rows) != 0 {
   	t.Fatalf("a modal overlay must not post a hint nobody can see: %v", rows)
   }
   r.overlayActive = false                     // the same click works again
   clickRow(t, r, row)
   if len(*opened) != 1 || (*opened)[0] != "/tmp/shot.png" {
   	t.Fatalf("after the overlay closed, opened = %v", *opened)
   }
   ```
10. **新增** `TestComposerHintIsPaintedByEveryComposerLayout`（`tty_session_test.go`，照既有函数 `TestViewportComposerKeepsSingleSpacingAroundWorkingAndPendingQueue`（`chat_session_test.go`，**按函数名锚定：该文件正被并行会话修改，行号不可靠**）的 `buildComposerBlock` 直调写法）：
    - 普通（`Text:""`）：无提示与单行提示的 `len(block.lines)` **相等**（提示取代那条空 spacer），且提示文本出现在块里；
    - 普通 + `PendingInput`（队列预览）→ 行数比无提示时多 `len(hintRows)`；
    - `Text:"/model"`（slash 模式）→ 含提示文本；
    - `ReadOnly:true` → 含提示文本。
11. **新增** `TestAVeryLongFailureWrapsWithoutEverCutting`：`ShowComposerHint("open a.png: "+strings.Repeat("x", 2000)+" file no longer exists", "", time.Second)` → 宽 80 时行数 > 2（确实换行了）、每行 `stripANSI` 宽度 ≤ 80、拼起来**含结尾的 `file no longer exists`**、且**全文没有 `…`**（裁决 7：只换行、永不省略）。
12. **新增** `TestAStaleComposerHintTimerCannotWipeANewerHint`：`ShowComposerHint("first", "", time.Nanosecond)` 紧随 `ShowComposerHint("second", "", time.Hour)`；轮询 100ms 断言行文本始终是 `second`（旧 timer 的 `Stop()` 已来不及，只能靠 generation 判归属）。**必须带 `-race`。**

既有、不得改断言的用例：`TestClipboardImageHintExpiresWhileImageStays`、`TestClipboardWatcherDebounceDropsAFleetingImage`、`TestClipboardWatcherRateLimitsRepeatHints`、`TestClickingTheImageMarkerRowLaunchesTheClickedFile`、`TestImageMarkerHitTestOpensTheClickedStep`、`TestImageMarkerHitTestIgnoresEverythingButTheMarkerRow`、`TestImageCardIsNeverCollapsibleAndItsMarkerOpensTheFile`。（`TestClipboardImageHintLine` 按 Step 1 第 5 点整体改写。）

## 9. Done criteria（机器可查，全部成立）

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 -race ./pkg/tui -skip 'TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns' -count=1` → `ok`，**0 失败**；且该被跳过用例单独 `-count=5` 跑 0 失败（§5 第 3 条）
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` exit 0 且无输出
- [ ] §6 的 5 文件 `gofmt -l` → 无输出
- [ ] `grep -rn 'clipboardImageHintLine\|clipboardImageHintTimeout' pkg/tui` → 无匹配
- [ ] `grep -rn 'renderer.PrintError' pkg/tui/image_open.go` → 无匹配
- [ ] `grep -c 'ShowComposerHint' pkg/tui/image_open.go` → `1`
- [ ] `grep -c 'r.composerHintRows(termWidth)' pkg/tui/reducer.go` → `3`（普通 composer / 只读 / slash；函数定义写作 `composerHintRows(termWidth int)`，不匹配该模式）
- [ ] `grep -n 'if !r.overlayActive' pkg/tui/reducer.go` → 1 处，位于 `transcriptClickLocked` 内
- [ ] `grep -rn 'composerHintGen' pkg/tui/reducer.go pkg/tui/render.go` → 两文件都有（字段 + 判归属）
- [ ] `grep -c 'truncateMiddle' pkg/tui/reducer.go` → `0`（提示行永不截断，裁决 6/7）
- [ ] `grep -rc 'PrintError(' pkg/tui | awk -F: '{s+=$2} END {print s}'` → 比改动前**恰好少 1**（只有 `image_open.go` 那一处消失）
- [ ] 本计划 5 个文件的 `git diff`，**相对执行时基线快照（`$TMPDIR/image-hint-baseline.patch`）新增的 hunk 只对应 §3**；基线里已有的他人改动（a6 的 deliverables/FrameKind 等）原样保留、一条不减
- [ ] `git diff -- pkg/tui/render.go` 中属于本计划的只有字段那一段（该文件可能还有 a6 的 `renderDeliverables` 等他人 hunk，**必须保留**）
- [ ] §13 验收记录已回填，§11 的 4 条真机判据全部通过（活文件路径由 §11 末尾点名的两个单测覆盖）
- [ ] 未执行 `git add` / `git commit` / `git stash` / `git checkout`

**Drift 基线（内容对照，不看行号漂移也不看 stat 数字）**：改动前在 `pkg/tui/render.go` 搜 `clipboardImage      bool`；`pkg/tui/reducer.go` 搜 `func (r *Renderer) SetClipboardImageAvailable`、`r.clipboardImageHintLine(termWidth)`、`imagePath, imageLabel, _ = vm.imageMarkerAtScreenRow`；`pkg/tui/image_open.go` 搜 `renderer.PrintError(fmt.Errorf("open %s: %w", label, err))`。五处全命中 = 未漂移。

## 10. STOP conditions（不要即兴发挥）

- Step 4 的包级测试（带 §6 的 `-skip`）出现任何失败 → STOP 报告「0 失败门禁不成立」，**不豁免、不替它修、不删别人的文件**。
- §6 的隔离确认（`-run 'TestAwaitSignIn…' -count=5`）出现任何一次失败 → STOP 报告「那条不是 flaky，是别人文件里的真 bug」，只报告不动手。
- 除 §5 点名的那一条之外，想 skip 任何用例 → 不允许，STOP。
- §5 摘录内容对不上（尤其 `render.go:600-605`、`reducer.go:4023-4032 / 3878-3880 / 4104-4107 / 6368-6382`、`reducer.go:8104-8168`、`image_open.go:123-139`）→ 停下报告。
- 需要改 `pkg/tui/tty_session.go`、`notify.go`、`run.go`、`overlays.go`、`setup.go`、`channels.go` 或它们的测试才能编译 → 停下报告（他人 WIP）。
- Step 1 之后 `TestClipboard*` 任何一条不绿 → 重构不是纯重构，停下报告。
- `sanitizeTerminalInputText` / `wrapCardLine` / `stripANSI` 的签名或语义与 §3.1/§8 引用不符 → 停下报告（不要自己造一个）。

- Step 5 marker 行点不到、或 `read_file` 没产出图片卡 → 先查 `$WORK/tui.err` 与 `$D screen`；10 分钟内定位不了就停下报告，**不得改产品代码迁就验收**。

## 11. 真机验收（tmux + fake provider，不用真模型）

```bash
D=.claude/skills/run-forebrain/driver.sh
WORK="${TMPDIR:-/tmp}/forebrain-run"
$D build
mkdir -p "$WORK/proj"        # $PROJ 由 cmd_serve 建；这里先建好，供下面 python 写图
python3 - "$WORK/proj/shot.png" <<'PYEOF'
import struct, sys, zlib
w = h = 8
raw = b"".join(b"\x00" + b"\x40\x80\xc0" * w for _ in range(h))
def chunk(t, d):
    return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
png = (b"\x89PNG\r\n\x1a\n"
       + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
       + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))
open(sys.argv[1], "wb").write(png)
PYEOF
# read_file 的参数名是 file_path（pkg/tool/file_tools.go:63），不是 path
$D start tool "{\"name\":\"read_file\",\"arguments\":{\"file_path\":\"$WORK/proj/shot.png\"}}"
$D submit 'read the screenshot'
$D wait 'click to open' 30
ROW=$($D screen | grep -n 'click to open' | head -1 | cut -d: -f1)   # 1-based 屏幕行号
echo "marker row=$ROW"
rm -f "$WORK/proj/shot.png"        # 复现前置状态：marker 行还在，文件已没
# 每次点击前重算行号：Working/status 行的增减会让行号漂移，旧 ROW 会点到别处
for i in 1 2 3; do
  ROW=$($D screen | grep -n 'click to open' | head -1 | cut -d: -f1)
  $D click 3 "$ROW"
done
tmux capture-pane -t forebrain-run -p > "$WORK/screen-after.txt"
grep -c '●' "$WORK/screen-after.txt" ; echo '^ 正对照：期望 ≥1（工具卡圆点能被 grep 到，说明这屏看得见、模式有效）'
grep -c '● error' "$WORK/screen-after.txt" ; echo '^ 期望 0（连点三次也不出卡）'
grep -c 'error' "$WORK/screen-after.txt" ; echo '^ 期望 0（整屏无 error 字样）'
grep -n 'open shot.png: file no longer exists' "$WORK/screen-after.txt"
# 位置校验：提示在 marker 行之下、且落在屏幕底部（composer 区，driver 的 tmux 是 40 行），才算「顶框上方」
awk '/click to open/{m=NR} /open shot.png: file no longer exists/{h=NR} END{print "marker=" m " hint=" h " screenRows=" NR; exit !(m && h>m && h>NR-10)}' \
  "$WORK/screen-after.txt" && echo '^ 期望 marker < hint，且 hint 在屏幕最后 10 行内'
tmux capture-pane -t forebrain-run -e -N -p | grep -F 'file no longer exists' | head -1 | cat -v   # 取证：右对齐空格 + ^[[2m 灰色
sleep 9
tmux capture-pane -t forebrain-run -p | grep -c 'file no longer exists' ; echo '^ 期望 0（8s 已自动消失）'
ROW=$($D screen | grep -n 'click to open' | head -1 | cut -d: -f1)
$D click 3 "$ROW"
tmux capture-pane -t forebrain-run -p | grep -c 'file no longer exists' ; echo '^ 期望 1（再点重现）'
$D stop
```
四条判据（真机全部通过才算收口）：
1. **不堆卡**：文件删掉后连点三次，`● error` 计数 0、整屏 `error` 字样 0，transcript 不增长；**正对照** `●` 计数 ≥ 1（工具卡圆点可被 grep 到，模式确实有效）。
2. **有提示**：那行右侧显示 `open shot.png: file no longer exists`（`capture-pane -e` 可见 `\x1b[2m`）。
3. **位置正确**：提示行在 marker 行之下，且落在屏幕最后 10 行（composer 区）内 —— 才叫「composer 顶框上方」。**不要用 footer 文案做锚点**：driver 的 `forebrain.yaml` 是 `approval_policy: on-request`，footer 只显示模型名（`deepseek-chat`），没有 `full access` 字样。
4. **会消失/重现**：最后一次点击后 ~9s 文本消失；再点一次又出现（可重复）。

**为什么真机不验「活文件仍能打开」**：TUI 进程的运行环境来自 tmux **server**（`driver.sh` 用 `tmux new-session` 起会话，server 已存在时继承的是 server 的环境），在不改 `driver.sh`、也不 `tmux setenv -g`（会污染同机其它会话，含并行会话）的前提下无法给该进程注入记录调用的假 `open`；真跑会弹出真的预览程序，属无必要副作用。这条路径由确定性单测覆盖：`TestClickingTheImageMarkerRowLaunchesTheClickedFile`、`TestImageCardIsNeverCollapsibleAndItsMarkerOpensTheFile`（都用 `stubImageOpener`，§8 列为不得改断言）。

## 12. Maintenance notes

- **这条提示是「一次性 UI 反馈」的唯一入口**：`ShowComposerHint(text, narrow, d)` / `HideComposerHint(text)`。以后再有「用户刚做的动作没成功、重试零成本」的反馈走这里；**真正需要事后排查的错误**（网络、鉴权、配置、slash 命令失败）继续走 `PrintError`。
- **四个不变量**：① 宽/窄不是两套语义，`narrow` 只在放不下**一行**时启用；② 放不下就**换行**，不省略；③ 宽窄都过 `oneLineHint`（折叠空白 + 去控制序列），因为这些行是直接拼进绘制流的；④ 右对齐按**最宽行**补 pad（按最窄行会让宽行超宽、被 `canvasRow` 裁掉尾部，正好切掉原因）。
- **没有缩短点**：提示永不截断（裁决 6/7），只换行。**已知的既有局限**：composer 自身超过屏幕 2/3 时（例如粘了一大段 JSON），`paintViewportLocked` 会按 caret 滚动裁剪，位于 caret 之上的提示行可能整段滚出可视区——剪贴板提示今天就是这样，不是本计划引入。真要让提示永远可见，得把它钉在滚动窗口之外（composer 卡片上方固定行），那是另一个改动，别顺手做。
- **文本槽 + generation**：不同文本互相替换、同文本保留原过期时间；`composerHintGen` 是「哪个 timer 还有权清行」的唯一判据，别删（两个生产者：剪贴板 watcher、launcher 异步失败回调）。
- **四个状态必须同步**：普通（复用 spacer）/ 队列预览（追加）/ slash（追加）/ 只读 subagent 视图（追加）。**modal 遮罩期间不画也不响应点击**（§3.2 e），这是刻意设计：加第五种 composer 布局时，先问「提示画得出来吗」，画不出来就把那条交互门掉，而不是让点击静默失败。
- **`composerSuppressed` 目前不可达**（生产代码只有 `render.go:4619` 的 `= false`）；将来若有地方置 `true`，需重新评估。
- **宽 1 + 宽字符是 `wrapCardLine` 的既有边界**：宽度 1 时一个 CJK 宽字符仍占 2 格（该函数“无断点”分支原样吐出）。当前提示文案是英文 + 文件名，只有文件名可能含宽字符；这属于既有行为，本计划不改、也不为它加门禁（§8 #7 只用 ASCII）。真要修，改 `wrapCardLine`，那会影响所有卡片。
- **已知代价**：队列预览 / slash / 只读视图下，提示可见的 8 秒里 composer 块会变高（行数 = 换行结果，无上限），transcript 可见区相应减少，8 秒后恢复。替代方案是常驻行（每屏永久少几行），不在本期。
- **reviewer 重点看**：`render.go` 字段替换没动 `clipNotifyTimer`/`ctrlCExitTimer`；三处绘制插入点都在 caret 行之前、`firstLine`/`cursorRow` 计算之前；遮罩门只关图片解析、不动折叠与子代理视图；`composerHintRows` 返回 nil 时调用点用空展开（普通 composer 会补回原来的空 spacer 行）。
- **评估过的同类项与处置**（防止重新翻账）：`openInBrowser` 失败只写日志（`setup.go:1173`）—— 登录场景 URL 仍在屏上可抄，另立项；web `ToolCallCard.vue` 删除后新标签页 404 —— 不同 surface，另立项；`commands.go`/`chat_slash.go`/`config_panel.go`/`setup.go` 的 `PrintError` —— 有意保留。

## 13. 验收记录（实施后回填）

| 场景 | 命令 | 观察输出 |
|---|---|---|
| 复现（改前，Step 0.5 A+B） | `go test ./pkg/tui -run TestReproDeadImageClicksStackErrorCards -v` | `REPRO A: 5 direct calls appended 5 permanent error cards; newest="open shot.png: file no longer exists"` / `REPRO B: 3 real clicks appended 3 permanent error cards; newest="open click-repro.png: file no longer exists"` → **PASS**（复现成立，随后删除该临时文件） |
| 修复后连点三次不出卡 | `$D click` ×3 + `capture-pane` | `●` 计数 5（正对照）/ `● error` 0 / `error` 全屏 0；末尾提示行 `open shot.png: file no longer exists` |
| 8s 自动消失 / 再点重现 | `sleep 9` + `capture-pane` | 9s 后 `grep -c 'file no longer exists'` = **0**；再点一次 = **1** |
| 提示在 composer 区（不在 transcript） | awk 行号校验（marker < hint < 屏幕倒数 10 行） | `marker=20 hint=36 screenRows=40` → PASS（40 行 tmux 屏） |
| 提示的灰色取证 | `tmux capture-pane -e -N -p \| grep -F 'file no longer exists'` | `^[[39m …右对齐空格… ^[[2mopen shot.png: file no longer exists` |
| 基线快照（Step 0.7） | `git rev-parse --short HEAD` + `git status --porcelain pkg/tui` + 五处锚点 grep | HEAD `3506aea8`；工作树含 a6 的在途改动（`M chat_session.go chat_turn.go commands.go notify.go reducer.go render.go tty_session.go` + `?? deliverables_test.go glyph_test.go`）；五处锚点 `1 1 1 1 1` 全命中；`pkg/tui/zz_review_step0_test.go` 不存在 |
| 改动前 0 失败门禁 | `go test -race ./pkg/tui -skip 'TestAwaitSignIn…' -count=1` | `ok … 93.506s`（0 失败） |
| 活文件仍能打开 | `go test ./pkg/tui -run 'TestClickingTheImageMarkerRowLaunchesTheClickedFile\|TestImageCardIsNeverCollapsibleAndItsMarkerOpensTheFile\|TestImageMarkerHitTest' -v` | `ok … 1.994s`（真机不验，理由见 §11 末尾） |
| 改动后包级回归 | `go test -race ./pkg/tui -skip 'TestAwaitSignIn…' -count=1` | `ok … 93.646s`（0 失败） |
| 预存 flaky 隔离确认 | `go test -race ./pkg/tui -run 'TestAwaitSignIn…' -count=5`（修前） | **5 次里 1 次失败**：`WARNING: DATA RACE` — 测试 `signInWaitRenderer` 的 `t.Cleanup` 还原 `clipboardWriteText`（`overlays_test.go:3610`）与 `Renderer.writeClipboard` 的后台 goroutine 读同一 seam（`reducer.go:7771`）竞争。复现成立 → 根因已修（见文末 Implementation Status） |
| 竞态修复后隔离确认 | `go test -race ./pkg/tui -run 'TestAwaitSignIn…' -count=10` | `ok … 24.606s`（10/10 绿，含 `-race`） |
| 竞态修复后包级门禁（**不再带 -skip**） | `go test -race ./pkg/tui -count=1` ×4 轮 | 4/4 轮 `ok`（95.864s / 95.931s / 95.866s / 95.880s），**0 失败** |
| **收口复跑真机验收（终态代码，竞态修复之后重建）** | `$D build` + `$D reset` + fresh `serve tool` + 4 条判据 | 见下三行；二进制 built from `9f23024a` + 本计划 5 文件改动 |
| ↳ 不堆卡（终态） | 删文件后连点 3 次，前后各 capture 一次 | 卡片行 `●` **5 → 5**（transcript 零增长，比只查 `● error` 更严）/ `● error` 0 / 全屏 `error` 0 |
| ↳ 有提示 + 位置（终态） | `capture-pane` + awk | 提示行 `open shot.png: file no longer exists` 命中第 36 行；`marker=20 hint=36 screenRows=40` → `marker < hint` 且 hint 在最后 10 行内（composer 区） |
| ↳ 灰色 + 消失 + 重现（终态） | `capture-pane -e -N` / `sleep 9` / 再点一次 | `<ESC>[39m` + 84 空格 + `<ESC>[2m` + 文案；9s 后匹配 **0**；再点一次 **1**；全程 `error` 0 |
| 收口复跑全量门禁（终态代码） | `go build` / `go vet` / `gofmt -l` / `go test -race ./pkg/tui -count=1` ×2 | build exit 0；vet、gofmt 无输出；`ok 96.154s`、`ok 95.357s`（**累计 6 轮全量 `-race` 全绿**） |
| 并行会话核对 | `peer_send` forebrain-harness-a6 / -1c | a6 的 bulk commit 把 HEAD 推到 `9f23024a`；1c 的插件落地**零提交**、HEAD 仍 `9f23024a`，只动 gitignored 文件与其生成物，未碰 `pkg/tui` |

## Implementation Status

**STATUS: 实施完成，全部验证通过（含 owner 追加要求的预存缺陷修复）** — 2026-11-15，executor：parent（cli-ca6ed24c）。

### 每步结果

| Step | 结果 | 证据 |
|---|---|---|
| 0 计划落盘 | done | `docs/plan/IMAGE_OPEN_FAILURE_HINT_ROW_PLAN.md` 存在；`git status --porcelain docs/plan` 无输出（已 gitignore） |
| 0.5 改前复现 | done | 临时 `pkg/tui/image_hint_repro_test.go`：A 直调 5 次 → 5 张永久 error 卡；B 真实点击 3 次 → 3 张。日志入 §13，文件随后删除 |
| 0.6 绿门禁 | done | 改动前包级 `-race`（带 -skip）`ok … 93.506s`，0 失败；`zz_review_step0_test.go` 不存在 |
| 0.7 基线快照 | done | HEAD `3506aea8`、工作树含 a6 WIP、五处锚点 `1 1 1 1 1`；快照存 `$TMPDIR/image-hint-baseline.patch`（执行中途 HEAD 前移，见 deviations） |
| 1 提示槽泛化 | done | `render.go` 4 字段；`reducer.go` `ShowComposerHint`/`HideComposerHint`/`oneLineHint`/`composerHintRows`，删 `clipboardImageHintLine`，`clipboardImageHintTimeout`→`composerHintTimeout`；`go build` exit 0；`-run TestClipboard` 全绿；旧名 grep 0 匹配 |
| 2 失败走提示行 | done | `image_open.go` `openImagePreview` 改 `ShowComposerHint`（全形/窄形=同一 `llm.ExplainError`），加 `pkg/llm` import；`TestClickingTheMarkerOfAFileSinceDeletedSaysItInTheHintRow`、`TestALauncherThatRefusesTheFileIsReported`、`TestClickingTheImageMarkerRowLaunchesTheClickedFile` 全 PASS |
| 3 三绘制点+遮罩门+新测试 | done | `buildNormalComposerBlock`（spacer/预览后追加）、`buildReadOnlyComposerBlock`、`buildSlashComposerBlock` 三处 `composerHintRows`；`transcriptClickLocked` 的 `if !r.overlayActive` 门；§8 #3-12 全 PASS |
| 4 包级回归+静态检查 | done（**不带 -skip**） | 4/4 轮 `-race` 全绿（95.86–95.93s），0 失败；`go vet` 无输出；5 文件 `gofmt -l` 无输出。原「疑似预存 flaky」已定位为真实数据竞争并修根因（见下节） |
| 5 真机验收 | done（**竞态修复后复跑一遍**） | 4 条判据全过；终态代码复跑：`●` 5→5（零增长）/ `● error` 0 / `error` 0 / `marker=20 hint=36 screenRows=40` / `<ESC>[2m` 灰色取证 / 9s 后 0、再点 1。harness 需把会话模型换成 `deepseek/deepseek-ocr-2`（默认 `deepseek-chat` 不收图片输入），仅改 `$FOREBRAIN_HOME/forebrain.yaml`，未动产品代码 |
| 6 收口 | done（**彻底收口**） | 本节 + §13；终态树：5 个 in-scope 文件、无 staged、HEAD 仍 `9f23024a`（1c 的插件落地零提交，已 peer 确认）；tmux 会话已 stop、`$TMPDIR/forebrain-run` 已 reset 清空；**未执行** git add/commit/stash/checkout |

### 机器判据（§9）逐条

- `go build -tags fts5 ./pkg/tui` exit 0 ✓
- 包级回归 0 失败 ✓；`composerHintGen` 在两文件都出现（reducer 4 / render 2）✓
- `go vet` exit 0 无输出 ✓；5 文件 `gofmt -l` 无输出 ✓
- 旧名 grep 0 匹配 ✓；`renderer.PrintError` in `image_open.go` 0 ✓；`ShowComposerHint` in `image_open.go` = 1 ✓
- `r.composerHintRows(termWidth)` in `reducer.go` = 3 ✓；`if !r.overlayActive` = 1（在 `transcriptClickLocked` 内）✓
- `truncateMiddle` in `reducer.go` = 0 ✓
- `PrintError(` 总数 97 → **96**（只少 `image_open.go` 那一处）✓
- 改动集恰为 5 个 in-scope 文件，无其它文件被碰 ✓（`git status --porcelain pkg/tui` 只列这 5 个）；其中 `reducer.go` 另含 owner 追加要求的预存竞态修复 hunk（`writeClipboard` 的 seam 读取上提，见下节）

### 预存缺陷：已修（owner 裁决 2026-11-15「预存缺陷必须本期修」）

`TestAwaitSignInSelectsAndCopiesThePageWhileTheFlowRuns`（`pkg/tui/overlays_test.go`）在 `-count=5` 隔离确认里 **1/5 失败**，失败原因是**数据竞争**，不是超时抖动：

```
WARNING: DATA RACE
Write at ... by goroutine 19:
  pkg/tui.signInWaitRenderer.func2()  overlays_test.go:3610   // t.Cleanup 还原 clipboardWriteText
Previous read at ... by goroutine 24:
  pkg/tui.(*Renderer).writeClipboard.func1()  reducer.go:7771  // 后台 goroutine 读 clipboardWriteText
```

**根因**：`Renderer.writeClipboard` 在后台 goroutine 里读包级 seam `clipboardWriteText`（`tty_session.go:760`）。测试用 `t.Cleanup` 换回/还原这个 seam，goroutine 还在飞 → 读写竞争。缺陷在**生产侧的形状**（可变 seam 被异步读），不只是那个测试用得多余。

**修法（`pkg/tui/reducer.go`，1 条语句 + 注释）**：在调用方 goroutine 上把 seam 先读成局部值，goroutine 只用这个局部值：

```go
func (r *Renderer) writeClipboard(text string) {
	// The seam is read here, on the caller's goroutine, and the goroutine gets
	// the function it chose: reading it inside the goroutine made every test
	// that swaps clipboardWriteText and restores it in a cleanup race a write
	// still in flight (go test -race caught it between signInWaitRenderer's
	// cleanup and this goroutine).
	write := clipboardWriteText
	go func() {
		clipErr := write(context.Background(), text)
		...
	}()
}
```

两个调用点（`reducer.go` 拖拽释放 `viewportRelease`、`CopySelectionForCtrlC`）本来就在调用方 goroutine 上同步调用 `writeClipboard`，所以「读 seam」的时间点语义不变；变的是它不再与任何测试的 cleanup 并发。没有改那个测试文件、没有加等待/超时或 skip。

**验证**：隔离 `-count=10 -race` → 10/10 绿（24.606s）；包级门禁**去掉 `-skip`** 后 4/4 轮全绿（95.864s / 95.931s / 95.866s / 95.880s），0 失败；`go vet`、`gofmt -l` 均无输出。

### Deviations（都已记录、不影响本计划的意图与验收）

1. **执行中途 HEAD 前移**：起手基线 `3506aea8`（含 a6 的未提交 WIP）。中途 a6 按 owner 指令把全树 bulk commit 成 `9f23024a`（未跑任何测试），其中带走了我当时的 `tty_session_test.go` 中间态。我先把改动存到 `$TMPDIR/image-hint-mine/`，按 a6 要求把 `render.go`/`reducer.go` 回退到其落盘态，待 commit 后原样恢复再继续。因此 §9 里「相对基线快照的 diff」判据改为**相对 `9f23024a` 的 diff**：更强、且已验证（改动集恰为 5 文件、每个 hunk 都对应 §3）。快照补丁 `$TMPDIR/image-hint-baseline.patch` 因此只作为「改前确实没有 composerHint」的旁证，不再作比较基准。
2. **`TestClipboardWatcherRateLimitsRepeatHints` 也引用了旧常量**（`clipboardImageHintTimeout`，§5 只点了 155-157 的注释）：随改名一并改为 `composerHintTimeout`，断言未动。
3. **真机验收的模型**：driver 默认 `deepseek-chat` 不收图片输入，`read_file` 直接失败（`does not accept image input`），marker 行不出现。改为在 `$FOREBRAIN_HOME/forebrain.yaml` 里把会话模型设成 `deepseek/deepseek-ocr-2`（`attachment: true`，fake provider 照常应答）+ `driver.sh reset` 清掉「上次用的模型」，真机才产出图片卡。属验收 harness 配置，未改产品代码。
4. **`TestRepeatedClicksOnAMissingImageNeverStackCards` 用 `openImagePreview` 连调 3 次**（§8 #3 原文），点击路径本身由 §8 #9 与真机判据 1 覆盖。
5. **`TestComposerHintExpiresOnItsOwn` 的出现断言改成同步**（`ShowComposerHint` 返回后立即断言在屏，再 `eventually` 等消失）：50ms 窗口下先 `eventually` 等出现本身有竞态，同步检查更严。

### 收口复核（终态，2026-11-15 执行者本人复跑）

**交付物状态**：`git status --porcelain` 全树只有这 5 个 `M`：

```
pkg/tui/image_open.go        pkg/tui/image_open_test.go   pkg/tui/reducer.go
pkg/tui/render.go            pkg/tui/tty_session_test.go
```

- 无 staged、无 commit、无 stash、无 checkout；HEAD 仍 `9f23024a`（a6 的 bulk commit），并行会话 1c 的 WorkBuddy 插件落地是**零提交**（只写 gitignored 文件与其生成物），已 `peer_send` 双向确认，未碰 `pkg/tui`。
- `reducer.go` 的两类 hunk：本计划的提示行改动（§3）+ owner 追加要求的预存竞态修复（`writeClipboard` seam 读取上提）。

**终态代码的全套验证（竞态修复之后）**：

| 检查 | 结果 |
|---|---|
| `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` | exit 0 |
| `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` | 无输出 |
| `gofmt -l`（5 文件） | 无输出 |
| 本计划相关用例（提示行/双形态/换行/遮罩门/连点/open 路径/剪贴板族） | `ok … 3.619s` |
| 全量 `-race`，**不带 `-skip`** | 再跑 2 轮 `ok`：96.154s、95.357s → **累计 6 轮全绿、0 失败** |
| 隔离 `-run 'TestAwaitSignIn…' -count=10 -race` | 10/10 绿 |

**真机验收（终态二进制，`$D build` 重建后）**：

1. 正对照：交付物卡圆点 `●` 在点击前 5 行、点击后 **仍 5 行** → 连点 3 次 transcript 零增长（比 §11 的「`● error` 计数 0」更严）。
2. `● error` 0、全屏 `error` 0。
3. 提示行在第 36 行（`marker=20 hint=36 screenRows=40`）→ 在 marker 之下、在屏幕最后 10 行的 composer 区内。
4. SGR 取证：`<ESC>[39m` + 84 空格 + `<ESC>[2m open shot.png: file no longer exists`（右对齐 + 与剪贴板提示同一条灰行）。
5. `sleep 9` 后匹配 0（自动消失）；再点一次匹配 1（重现），全程 `error` 仍 0。

**一个观察（既有行为，不属本计划，未改）**：在同一行快速连点 3 次会先被多重点击识别成选词/选行，footer 出现 `copied 47 chars to clipboard`。这与本次改动无关（改前同样的连点也会走到选词复制），只是真机取证时会看到；若要连点纯测「失败报告」，用间隔 >500ms 的单击即可。

**环境**：`driver.sh stop` + `reset` 已执行，tmux server 无会话，`$TMPDIR/forebrain-run` 已删除；未留下任何后台进程。
