# TUI 剪贴板两条通知对齐 Claude Code 语义（图片粘贴提示 + 复制确认提示）

状态：待 owner 审批（exit_plan_mode；owner 已于 2026-11-11 裁决：image hint 对齐 CC、Copied 通知也必须对齐 CC，并入本期）
Planned at：工作区基线（dev 分支，未 commit；drift 检查以本计划内 file:line 对照为准）
前版计划：`clipboard-image-hint-transient.md`（仅 image hint 一条，被本版取代并留档）
实施时第 0 步先把本计划落盘为 `docs/plan/CLIPBOARD_NOTICES_ALIGN_CLAUDE_CODE_PLAN.md`（仓库惯例位置，docs/plan 已 gitignore 不入库）。

## 1. 目标（Why this matters）

两条剪贴板相关通知的行为与 Claude Code 同类语义不一致：

1. **图片粘贴提示** `Image in clipboard · ctrl+v to paste`：剪贴板有图就常驻显示，永不消失——噪声。CC 2.1.220 同款提示是**瞬时通知**（8 秒自动消失、1 秒防抖、30 秒限频）。
2. **复制确认提示** `Copied 6 chars to clipboard`（footer 右侧）：显示 3 秒、大写开头、按 rune 计数。CC 2.1.220 同款（key `selection-copied`）是 **2 秒（native 复制）/ 4 秒（OSC 52）**、小写文案 `copied 6 chars to clipboard`、按 grapheme 计数。

owner 裁决：两条都对齐 Claude Code 语义。本仓两条文案本就源自 CC，语义一并对齐。

## 2. 证据链

### 2.1 本仓现状

**图片提示（常驻的原因）**：
- `pkg/tui/run.go:668-678`：viewport 模式启动 `clipboardImageWatcher`；焦点上报 `run.go:4520-4537`。
- `pkg/tui/tty_session.go:600-620`（`run`）：每 1s（仅焦点在内时）探测，把**状态**推给渲染器 `w.renderer.SetClipboardImageAvailable(has)`，无边沿/时限。
- `pkg/tui/reducer.go:8082-8095`：`SetClipboardImageAvailable` 只置 `clipboardImage bool` 状态位并重绘；`render.go:605-607` 字段注释自证 "true while the system clipboard holds an image"。
- `pkg/tui/reducer.go:4030`（唯一绘制点）：composer 块无队列预览时 spacer 行画 `clipboardImageHintLine`（`reducer.go:8100-8111`）。
- 全仓唯一调用点：`SetClipboardImageAvailable`←watcher（tty_session.go:611）；`clipboardImageHintLine`←reducer.go:4030。gateway/web 无剪贴板图片探测。

**复制确认提示（3 秒的原因）**：
- `pkg/tui/reducer.go:7619-7631`：`setCopiedNotificationLocked` → `copiedNotice(text)` 生成 `Copied %d %s to clipboard`（rune 计数、char/chars 单复数）→ `setClipNotification`。
- `pkg/tui/reducer.go:8038-8049`：`setClipNotification` 硬编码 `time.AfterFunc(3*time.Second)` 清除重绘。
- 触发点：选区释放即复制（`reducer.go:7537-7539`）与 Ctrl+C 带选区（`CopySelectionForCtrlC`，`reducer.go:7572-7601`）——与 CC 的 copyOnSelect+ctrl+c 语义一致，不用改。
- 写剪贴板：`writeClipboard`（7606-7615）异步跑 `clipboardWriteText`（`tty_session.go:686-718`，per-GOOS LookPath pbcopy/wl-copy/xclip/xsel/clip.exe），失败才落 `writeClipboardOSC52Locked`（8031-8036，写 OSC 52 序列）。**通知在写之前就定了 3 秒，与方法无关**。
- 既有 timer 模式先例：`clipNotifyTimer`（render.go:597）、`ctrlCExitTimer`（render.go:603）。

### 2.2 Claude Code 2.1.220 逆向（目标语义）

二进制 `/opt/homebrew/Caskroom/claude-code/2.1.220/claude`（Mach-O 内嵌 minified JS；提取方式 `strings -a -n 6` + byte-offset dd）。

**(a) 图片提示 `F4o`**（key `clipboard-image-hint`，offset ≈238652587）：

```js
useEffect(() => {
  const prev = ref.current; ref.current = e;       // e = hasClipboardImage
  if (!t || !e || prev) return;                    // 仅上升沿 false→true
  timer = setTimeout(async () => {                 // xGy=1000：1s 防抖
    if (Date.now() - lastShown < IGy) return;      // IGy=30000：30s 限频
    if (await qIu())                               // 复核剪贴板仍持图（native hasClipboardImage/osascript）
      addNotification({ key:"clipboard-image-hint", kind:"contextual",
        text:`Image in clipboard · ctrl+v to paste`,
        priority:"immediate", timeoutMs: 8000 });  // 显示 8s 自动消失
  }, 1000);
}, [e, t, ...]);
```

**(b) 复制确认 `FJo`**（key `selection-copied`）：

```js
function FJo(e){                                   // e = 被复制文本
  let t = LKr();                                   // 复制方式：native | tmux-buffer | osc52
  let r = Yde(e);                                  // Intl.Segmenter grapheme 计数
  let n = r===1 ? "char" : "chars";
  switch(t){
    case "native":      o = `copied ${r} ${n} to clipboard`; break;
    case "tmux-buffer": o = `copied ${r} ${n} to tmux buffer · paste with prefix + ]`; break;
    case "osc52":       o = `sent ${r} ${n} via OSC 52 · if paste fails, hold ${HKr()} while selecting for native copy`; break;
  }
  let i = Puo(e);                                  // VS Code 1.123/1.124 mojibake 警告
  if (i) o = `⚠ ${i} · ${o}`;
  return { key:"selection-copied", kind:"feedback", text:o, color:"suggestion",
           priority:"immediate", timeoutMs: i?6000 : t==="native"?2000 : 4000 };
}
```

要点：**native 2000ms / osc52 4000ms / 带警告 6000ms**；小写文案；grapheme 计数；char/chars 单复数。触发（选区释放 copyOnSelect 默认开 + Ctrl+C 带选区）与本仓一致。`LKr()` 按 GOOS+工具存在性预先判定方式（macOS→native），非按写入结果。

### 2.3 依赖事实

- `go.mod:97`：`github.com/rivo/uniseg v0.4.7 // indirect`（runewidth 的依赖）——grapheme 计数零新增模块，仅转直接依赖。
- 文档无两条文案的引用（仅代码常量 `reducer.go:8078`、`reducer.go:7630`）。

## 3. 修复设计

### 3.A 图片提示：常驻 → 瞬时（owner 已裁决对齐 CC）

职责三层拆分，沿用既有 timer 模式：

**watcher（tty_session.go）— 何时通知**：边沿检测 + 1s 防抖复核 + 30s 限频。

```go
// Claude Code clipboard-image-hint 节奏（2.1.220 逆向）：上升沿后 1s 防抖
// 复核，显示 8s 自动消失，30s 内最多提示一次。
const (
    clipboardImageDebounce  = time.Second
    clipboardImageRateLimit = 30 * time.Second
)

type clipboardImageWatcher struct {
    // 既有字段不动
    had       bool          // 上一次探测结果，仅 run goroutine 读写
    lastShown time.Time     // 上次真正显示的时刻，仅 run goroutine 读写
    debounce  time.Duration // 测试覆写
    rateLimit time.Duration // 测试覆写
    confirm   *time.Timer   // 上升沿防抖计时器；nil = 无待复核
    confirmC  <-chan time.Time
}
```

`run` 循环（所有状态只在 run goroutine 变更）：

```go
for {
    if w.focused.Load() {
        has := w.probe.ClipboardHasImage(ctx)
        if ctx.Err() != nil { return }          // 保留：关屏截断的探测不当作事实
        switch {
        case has && !w.had:                     // 上升沿：arm 防抖复核
            w.had = true
            w.armConfirm()                      // confirm = time.NewTimer(w.debounce)
        case !has && w.had:                     // 下降沿：立即隐藏 + 取消复核
            w.had = false
            w.stopConfirm()
            w.renderer.SetClipboardImageAvailable(false)
        }
    }
    select {
    case <-ctx.Done(): return
    case <-tick.C:
    case <-w.wake:                              // 焦点回归立即探测（既有行为保留）
    case <-w.confirmC:                          // 防抖到点：复核
        w.confirmC, w.confirm = nil, nil
        if !w.focused.Load() { w.armConfirm(); continue }  // 失焦不探测（既有契约），重挂等焦点
        if ctx.Err() != nil { return }
        if !w.probe.ClipboardHasImage(ctx) { continue }    // 图已走，等下轮 tick 记下降沿
        if time.Since(w.lastShown) < w.rateLimit { continue }
        w.lastShown = time.Now()
        w.renderer.SetClipboardImageAvailable(true)
    }
}
```

- 复核失败不回写 `had`：下一轮 tick 的正常探测看到 false 走下降沿（与 CC 的 prop 轮询更新一致）。
- `stopConfirm` 在 ctx 结束路径也调用（defer），不泄漏挂起 timer。
- 结构体 doc 注释（tty_session.go:565-570，现说 "hint is current whenever it can be seen"）改写为瞬时通知语义。

**renderer（reducer.go + render.go）— 显示多久**：

```go
// clipboardImageHintTimeout 是图片粘贴提示的显示时长（CC 同款 8s）。
// var 而非 const：测试覆写为短时限。
var clipboardImageHintTimeout = 8 * time.Second

// SetClipboardImageAvailable 显示/隐藏图片粘贴提示。true = 显示并在
// clipboardImageHintTimeout 后自动消失（即使剪贴板仍有图）；false = 立即隐藏。
// 显示节奏（防抖/限频）由 clipboardImageWatcher 决定。
func (r *Renderer) SetClipboardImageAvailable(on bool) {
    // on:  已可见→return；置位→(重)挂 timer(8s: 清位+paintViewportLocked)→repaint
    // off: 停 timer；已隐藏→return；清位→repaint
}
```

`render.go` Renderer 加 `clipboardImageTimer *time.Timer`（紧邻 `clipboardImage`），字段注释改为 "true while the transient paste hint is visible (auto-expires via clipboardImageTimer)"。timer 回调结构逐款对齐 `setClipNotification`（8038-8049）。绘制点 `clipboardImageHintLine`/`reducer.go:4030` 与文案不动。

### 3.B 复制确认提示：3s 大写 rune → CC 同款（2s/4s 小写 grapheme）

**方法探测（tty_session.go）— 一个列表两个使用者**：把 `writeTextToClipboard`（686-718）的 per-GOOS LookPath 判定抽成共享探测，`writeTextToClipboard` 与通知共用，杜绝两份工具清单漂移：

```go
// clipboardTextMethod 报告文本复制走哪条通道："native"（系统剪贴板工具可用）
// 或 "osc52"（无工具，writeClipboard 落 OSC 52）。CC 的 LKr() 同款语义。
// var 是测试 seam（同 clipboardWriteText）。
var clipboardTextMethod = detectClipboardTextMethod

func detectClipboardTextMethod() string {
    // darwin: pbcopy；linux: wl-copy/xclip/xsel；windows: clip.exe
    // 找到→"native"，否则→"osc52"（与 writeTextToClipboard 的分支一一对应）
}
```

`writeTextToClipboard` 改为先问探测结果再构造命令（同一份 LookPath 逻辑，行为不变）。

**通知（reducer.go）**：

```go
const (
    copiedNoticeNativeTimeout = 2 * time.Second // CC FJo: native → 2000ms
    copiedNoticeOSC52Timeout  = 4 * time.Second // CC FJo: osc52 → 4000ms
)

// copiedNotice 对齐 CC selection-copied：小写、grapheme 计数、char/chars。
func copiedNotice(text string) string {
    n := uniseg.GraphemeClusterCount(text)      // CC 用 Intl.Segmenter 同义
    ...
}

// osc52Notice：sent %d %s via OSC 52（CC osc52 分支文案核心，去掉其
// hold-Shift 子句——那是 CC 的修饰键原生复制机制，本仓没有）
```

- `setCopiedNotificationLocked`（7619）：按 `clipboardTextMethod()` 选文案+时长；native → `copied %d %s to clipboard` + 2s；osc52 → `sent %d %s via OSC 52` + 4s。
- `setClipNotification(text)`（8038）加时长参数 `d time.Duration`（唯一调用链随行改）。
- `go.mod`：uniseg indirect → 直接依赖（`go mod tidy`；无新模块进依赖图）。

**刻意不复刻（CC 有、本仓无对应机制，列出防"为什么不"）**：tmux-buffer 通道及文案（本仓写栈无 tmux load-buffer 通道）；VS Code mojibake ⚠ 前缀+6s（VS Code 版本缺陷检测）；"· disable auto-copy in /config" 尾缀（本仓无该配置项）；osc52 文案的 hold-Shift 子句；kind/color/priority 字段体系（本仓无通知分级系统，保持 footer 右侧既有槽位）。

### 3.C 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| 复制图片进剪贴板 | ≤1s 显示，常驻 | ≤2s（探测 1s+防抖 1s）显示，8s 自动消失 | 刻意修订 |
| 剪贴板图片保持 N 分钟 | 一直显示 | 出现时提示一次，之后安静 | owner 核心诉求 |
| 剪贴板清空/换文本（图片提示在显示中） | 立即隐藏 | 立即隐藏 | 不变（比 CC 挂满 8s 更严谨，既有契约） |
| 30s 内再次复制新图 | 常驻 | 下降沿隐藏+上升沿重新防抖，限频内不再提示 | 刻意修订 |
| 失焦期间复制图片、回焦点 | 回焦点立即显示 | 回焦点立即探测+防抖复核后显示 | 语义对齐 |
| 拖拽选中释放复制文本（native 工具） | `Copied N chars to clipboard` 3s | `copied N chars to clipboard` 2s | 刻意修订（CC FJo） |
| 无剪贴板工具（OSC 52） | 同上 3s | `sent N chars via OSC 52` 4s | 刻意修订 |
| emoji/组合字符计数 | 按 rune | 按 grapheme（👨‍👩‍👧‍👦=1） | 刻意修订（CC Intl.Segmenter） |
| 两条提示的位置 | 图片=composer 上方行；Copied=footer 右侧 | 不变 | 不动 |
| 队列预览可见时图片提示行 | 被预览替代 | 不变 | 不动（reducer.go:4019-4031 既有分支） |
| ctrl+v 粘贴图片入 composer | 正常 | 不动 | 回归验证 |

## 4. Scope

**In scope**
- `pkg/tui/tty_session.go`（watcher + 方法探测）
- `pkg/tui/reducer.go`（两个通知 + 常量）
- `pkg/tui/render.go`（Renderer 字段）
- `pkg/tui/tty_session_test.go`、`pkg/tui/reducer_test.go`、`pkg/tui/overlays_test.go`（适配+新增）
- `go.mod`/`go.sum`（uniseg 转 direct）
- `docs/plan/CLIPBOARD_NOTICES_ALIGN_CLAUDE_CODE_PLAN.md`（计划落盘+验收回填）

**Out of scope（明确不碰）**
- 图片提示文案/绘制位置、`writeClipboardPNG`/`pngpaste`/`clipboardTypesHoldPastableImage` 探测链
- OSC 52 写入实现（`writeClipboardOSC52Locked`）、tmux load-buffer 通道（CC 有、本仓无，§3.B 已列）
- `run.go` watcher 接线（接口签名不变则零改动）
- gateway/web composer（无剪贴板图片探测）
- footer 布局/`layoutComposerFooter`、`composerFooterSidesLocked`（通知只借右侧槽位，结构不动）

## 5. Steps

**Step 0：协调 + 计划落盘**
本树有并行会话（forebrain-harness-ec busy）。`peer_list`/`peer_send` 确认无人正在改 `pkg/tui` 四个 go 文件与 go.mod，再动手；冲突则等或改道。本计划复制到 `docs/plan/CLIPBOARD_NOTICES_ALIGN_CLAUDE_CODE_PLAN.md`。
验证：`git status` 基线上只有本计划文件新增。

**Step 1：图片提示 renderer 瞬时化**（reducer.go + render.go，§3.A renderer 部分）
验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestClipboardImageHintLine' -count=1`（既有绘制契约保持绿）。

**Step 2：watcher 边沿/防抖/限频**（tty_session.go，§3.A watcher 部分）
验证：`CGO_ENABLED=1 go build ./pkg/tui`（既有 watcher 测试此时可能红，属预期）。

**Step 3：Copied 通知对齐**（tty_session.go 方法探测 + reducer.go 通知 + go.mod uniseg，§3.B）
验证：`go mod tidy && git diff go.mod`（uniseg 转 direct，无其他变动）。

**Step 4：测试适配 + 新增**
- 适配 `startClipboardWatcher` helper：设 `w.debounce = 5*time.Millisecond`（默认 1s 会贴既有两测试 2s deadline 的边）。
- 适配 `TestClipboardWatcherProbesAtOnceWhenFocusReturns`（interval=time.Hour 保留）：断言链不变，现在同时覆盖"回焦点立即探测"+"防抖复核后显示"。
- `TestClipboardWatcherProbesOnlyWhileFocused`：断言不变（防抖复核分支的 focused 检查保证失焦零探测）。
- Copied 断言适配：overlays_test.go:2442/2467/2516、reducer_test.go:5068/5098/5131/5141 改小写 `copied ...`；`TestCopiedNoticeCountsCharacters` 增加 grapheme 用例（"👨‍👩‍👧‍👦"→`copied 1 char to clipboard`）。
- 新增：
  1. `TestClipboardImageHintExpiresWhileImageStays`：覆写 `clipboardImageHintTimeout=50ms`（t.Cleanup 还原）；Set(true)→可见→eventually 不可见；`false` 幂等、重复 `true` 不重置时限。
  2. `TestClipboardWatcherDebounceDropsAFleetingImage`：debounce=100ms；图停留 <100ms 即消失→提示从未出现。
  3. `TestClipboardWatcherRateLimitsRepeatHints`：rateLimit=10s、timeout=50ms；首显→过期→图走→图回→限频内不再显示（观察窗 ~300ms）。
  4. `TestClipboardWatcherRetriesConfirmWhileUnfocused`：上升沿后立即失焦→复核到点不探测（probe 计数不增）→回焦点→复核→显示。
  5. `TestCopiedNoticeFollowsClipboardMethod`：stub `clipboardTextMethod` 分别返回 native/osc52 → 文案与时长（读 `clipNotifyTimer` 不可行则用行为断言：短覆写时长后 eventually 清空；或直接断言 `copiedNotice`/`osc52Notice` 文案 + `setClipNotification` 参数化单测）。
- 回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Clipboard|Copied' -count=1` → 全绿。
- 包级：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`；`gofmt -l pkg/tui`（无输出）；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui`。

**Step 5：tmux 真机验收（fake 模型，不烧真配额）**

```bash
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
tmux new-session -d -s cbhint -x 120 -y 42 \
  'cd <临时项目目录> && FOREBRAIN_HOME=<临时home> ./build/bin/forebrain; echo EXIT_CODE=$?; sleep 300'
# 造图进剪贴板（1x1 png 先落盘）
osascript -e 'set the clipboard to (read (POSIX file "/tmp/cbh.png") as «class PNGf»)'
```

场景与断言（逐屏 `tmux capture-pane -t cbhint -p` 取证）：
1. **图片提示出现→自动消失**：放图 → ≤3s 出现 `Image in clipboard · ctrl+v to paste`；`sleep 10` 后消失（`osascript -e 'clipboard info'` 证明剪贴板仍有图）。
2. **限频**：图走（`printf x | pbcopy`）→ 30s 内再放图 → 不再提示；≥31s 后（先 pbcopy 走掉再放图）→ 再次出现。
3. **下降沿**：显示期间 `printf x | pbcopy` → 提示立即消失（不等 8s）。
4. **粘贴回归**：放图 → ctrl+v → 图片附件正常入 composer（粘贴路径未动）。
5. **Copied 通知（best-effort）**：tmux `send-keys -M` 合成鼠标拖选（SGR press→drag→release）→ footer 右侧出现 `copied N chars to clipboard`（小写）→ ~2s 后消失。若合成鼠标在 tmux 下不可靠：记录工具链限制，该场景以 Step 4 单测为准（通知逻辑全在 renderer 层，已被单测覆盖），并在验收记录注明。
6. **中断自救**：q 退出 → EXIT_CODE=0；清理：`tmux kill-session -t cbhint`、临时目录、`pbcopy </dev/null` 清剪贴板。

**Step 6：收口**
计划文件回填"已实施并真机验收通过（日期）"+ 验收记录；`git status` 改动范围与 §4 Scope 一致；不 commit（owner 手动审阅提交）。

## 6. Done criteria（机器可查）

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿（含 5 个新测试 + 适配断言）。
- [ ] `gofmt -l pkg/tui` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` 干净。
- [ ] `git diff go.mod`：仅 uniseg indirect→direct。
- [ ] 真机验收场景 1-4、6 通过（场景 5 best-effort），验收记录回填计划文件。
- [ ] `git status` 新增/修改文件恰好 = §4 In scope 列表。
- [ ] grep 复核：`SetClipboardImageAvailable` 仍只有 watcher 一个产品调用点；`clipboardImageHintLine` 仍只有 reducer.go:4030 一个调用点；`setClipNotification` 仍只有 `setCopiedNotificationLocked` 一个调用点。

## 7. STOP conditions

- 既有 watcher/Copied 测试适配后仍失败，且失败原因指向设计缺陷（防抖/限频与焦点唤醒交互、方法探测 seam 不闭合）→ 停，重新评估，不为绿灯硬改断言。
- 实施中发现三个"唯一调用点"出现计划外第二调用点 → 停，扩计划。
- 真机场景 1 提示 >3s 未出现（探测 1s+防抖 1s+余量）→ 先查 watcher 启动条件（run.go:668）与 osascript 可用性，不得跳过。
- `go mod tidy` 带出 uniseg 之外的依赖变动 → 停，查原因。
- 并行会话正在改 pkg/tui 同文件且 peer_send 无响应 → 等待，不抢写。
- 验收需要真模型 → 停（验收统一 fake 模型，owner 裁决）。

## 8. Maintenance notes

- 未来若引入统一通知系统（CC 的 kind/priority 体系），两条通知的节奏参数（8s/2s/4s、防抖、限频）整体可搬，watcher 端（边沿/防抖/限频）与 renderer 端（时长）解耦正是为这一天。
- `clipboardImageHintTimeout` 是 var（测试 seam），勿改 const；watcher 的 debounce/rateLimit 是字段默认值，测试覆写字段。
- `clipboardTextMethod` 与 `writeTextToClipboard` 共享一份工具探测——改动支持的工具清单时两处自动同步。
- 与 CC 的刻意偏差已在 §3.B/§3.C 列明：下降沿立即隐藏（CC 挂满 8s）、osc52 文案无 hold-Shift 子句、无 tmux-buffer/VS Code 警告/自动复制配置尾缀。
- CC 逆向基于 2.1.220（homebrew cask，2026-08-09 安装）；CC 升级后若通知语义变化，重跑 `strings`+dd 法比对（见 §2.2 方法）。

## Implementation Status

已实施并真机验收通过（2026-11-11）。基线 dev `5265abac`，改动未 commit（owner 手动审阅提交）。

**落地内容**
- Step 0：漂移检查全过（所有 file:line 与计划一致）；peer forebrain-harness-ec 确认零交集（其改动全在 worktree 283-execute；主树 pkg/process、pkg/run、pkg/tool、pkg/agent WIP 归属他人，不涉 pkg/tui/go.mod）；计划落盘本文件。
- Step 1（renderer 瞬时化）：`clipboardImageHintTimeout = 8s`（var，测试 seam）；`SetClipboardImageAvailable` 重写为 on=置位+(重)挂 8s timer（已可见则早退不重置时限）/ off=停 timer+立即隐藏；render.go 加 `clipboardImageTimer` 字段。
- Step 2（watcher 节奏）：上升沿→1s 防抖复核→30s 限频→显示；下降沿立即隐藏+取消复核；复核到点失焦则重挂等焦点；defer stopConfirm 不泄漏 timer。
- Step 3（Copied 对齐）：`clipboardTextTool()` 单一工具清单（pbcopy/wl-copy/xclip/xsel/clip.exe），`clipboardTextMethod`（var seam，native|osc52）与 `writeTextToClipboard` 共用；`copiedNotice` 小写+grapheme（uniseg）+char/chars；`osc52Notice`（无 hold-Shift 子句）；`setClipNotification(text, d)` 参数化，native 2s / osc52 4s；go.mod uniseg indirect→direct（go.sum 零变动）。
- Step 4：`startClipboardWatcher` 加 tune 变参（goroutine 启动前覆写，无数据竞争）+默认 debounce 5ms；新增 5 测试（ExpiresWhileImageStays、DebounceDropsAFleetingImage、RateLimitsRepeatHints、RetriesConfirmWhileUnfocused、CopiedNoticeFollowsClipboardMethod + ClipNotificationLastsItsDuration）；既有断言改小写并加 `pinNativeClipboardTextMethod`（测试不依赖宿主剪贴板工具）；grapheme 用例（👨‍👩‍👧‍👦=1 char、a+◌́=1 char）。
- Step 5 真机验收（fake 模型，driver.sh 独立 work/session/port）：
  1. 放图 → T0+2.1s 提示出现 → 8s 自动消失，`clipboard info` 证明剪贴板仍持 PNGf ✓
  2. 限频（自计时）：窗内（T0+16.4s）复放图静默；过窗（T0+34.4s）复放图重现 ✓（前两次跨命令延迟致时钟基准漂移，改为单命令自计时后通过）
  3. 下降沿：提示显示中 pbcopy 文本 → +0.97s 隐藏（早于自然过期 7s）✓
  4. 粘贴回归：放图 → 提示出现 → ctrl+v → composer 出现 `[Image #1]` ✓
  5. Copied 通知：SGR 合成拖选释放 → footer 右侧 `copied 5 chars to clipboard`（小写）→ +1.7s 仍在 → +4.2s 已消失 ✓（超 best-effort 预期，真实通过）
  6. 干净退出：提交 `quit` 行 → pane 显示 `EXIT=0` ✓（偏差见下）；清理完成（session/work dir/临时 png/剪贴板）
- Step 6：`git status` 改动恰好=§4 In scope（go.sum 无变动；docs/plan gitignored 不显示）。

**偏差记录（均非 load-bearing）**
1. 场景 6 退出键：计划写 `q`，本仓 `q` 是普通字符（进入 composer 草稿）；干净退出路径是提交 `quit`/`exit` 行（run.go:996）或 Ctrl+C 两次。按本仓语义验收，EXIT=0。
2. 方法探测形态：计划草图为 `detectClipboardTextMethod()` 直接返回 native/osc52，`writeTextToClipboard` "先问探测再构命令"——但 "native" 无法告知用哪个命令。落为 `clipboardTextTool() []string`（单一工具清单）+ `clipboardTextMethod`（判 native/osc52 的 var seam），两使用者共享一份 LookPath，行为与契约不变。
3. 既有测试等待文案微调（"the first probe to show the hint"→"the debounced re-check…"），断言链未动。
4. `SetClipboardImageAvailable` 产品调用点在 watcher.run 内为两条语句（下降沿 false + 复核 true），与计划 §3.A 伪代码一致，调用组件仍唯一（watcher）。

**验证命令与结果**
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok 83.6s（含 5+1 新测试与全部适配）✓
- `gofmt -l pkg/tui` → 无输出 ✓；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` → 干净 ✓
- `git diff go.mod` → 仅 uniseg indirect→direct ✓
- grep 唯一调用点：`clipboardImageHintLine`←reducer.go:4031 唯一；`setClipNotification`←setCopiedNotificationLocked 唯一；`SetClipboardImageAvailable`←watcher 唯一 ✓
