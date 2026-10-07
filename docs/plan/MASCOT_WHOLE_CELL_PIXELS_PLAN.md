# 计划：TUI 吉祥物恢复方案 A 设计稿——整格放大 40×12，纯背景色绘制

> 本仓库禁止执行者提交代码：改动留在工作区，由 owner 审阅后手动提交。

## 状态

- 类别：bug（吉祥物与设计稿不一致）+ owner 已拍板的设计变更
- 状态：已实施（2026-10-07），改动未提交
- **网格与吉祥物显示门槛已被 `docs/plan/MASCOT_COMPACT_GRID_PLAN.md` 取代（2026-10-07，owner 嫌 40×12 太大）：网格改为 14×8，即 28 列 × 8 行，门槛改为 66。本计划的纯背景色绘制契约、居中、版本号折行、resize 先 `2J` 保留**
- **本计划的这一版网格与门槛其后又被 `docs/plan/TUI_STARTUP_CARD_DESIGN_SHEET_PLAN.md` 取代（2026-10-07）：网格 10×6（20 列 × 6 行），门槛 58，边框与名称下分隔线改为虚线**
- 关系：
  - **取代** `docs/plan/MASCOT_VERTICAL_SLIVER_LINES_PLAN.md` 的「带对齐网格」和它的刻意修订（删眼睛高光、平嘴、删下巴台阶、节点拉高）；该计划的「纯背景色、不用字形」绘制规则保留。
  - **取代** `docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md` 中「吉祥物正好 6 行」和「半块字形」的绘制规则；该计划的网格（方案 A）、配色、宽度自适应、下限 78 列、垂直居中规则全部保留。
- 设计稿：方案 A，https://claude.ai/artifact/4NFmLaLrMfboe6YLoU8dkc

## 1. 现象

owner 截图：启动卡片吉祥物是 20×6 的粗块，没有眼睛高光，嘴是平的，下巴没有台阶，琥珀节点占满一整格。设计稿（预览页方案 A）有眼睛高光、上扬的嘴角、下巴台阶和 1 像素高的节点。

## 2. 根因

### 2.1 与设计稿不一致的直接原因

竖线修复计划把方案 A 的 12 像素行网格改成了「每两行逐列相同」，绘制改成每格一种背景色（`pkg/tui/setup.go` 的 `forebrainMascot` 与 `colorCell`）。每个终端格只能表达一个像素，吉祥物实际分辨率只剩 20×6，半行细节全部丢失。

### 2.2 为什么不能退回半块字形（实测）

竖线修复计划改网格，是为了躲开半块字形的缝。实测确认了缝的确切机制：

- owner 的终端是 macOS Terminal，配置为 Fira Code Regular 14pt。
- Fira Code 的 unitsPerEm 为 1950，块字符字宽 1200，14pt 时字宽 8.615pt。Terminal.app 把格宽取整到 9pt，每个字形右侧少盖 0.385pt。Retina 下实拍为每 18px 格只盖 17px，右缘留 1 个半透明像素，露出格子底色。加粗无效。
- 纵向同理：字形高 2400/1950×14 = 17.23pt，行高 20pt，顶部约 6px 只有底色。
- 换字号对比：13pt 字宽恰好 8pt，无缝；15pt 竖缝消失但出现横缝；系统默认的 SF Mono 11/12pt、Menlo 11pt 都有缝，只有 Menlo 12pt 干净。

结论：在 Terminal.app 里，字形能否盖满格子取决于字体和字号，代码控制不了；只有背景色在任何字号下都铺满整格。6 行高画不出半行细节，所以「6 行高、与设计稿一致、无缝」三者不能同时成立。

预览页的终端模型只模拟了顶部行距，没模拟右缘缺口，所以预览比真机好看。这也是批准方案 A 时没发现问题的原因。

## 3. owner 裁决（2026-10-07）

在「整格放大 40×12」和「恢复 6 行半块（14pt 下竖线回来）」之间，owner 选择**整格放大 40×12**：方案 A 的每个像素画成 2 列 × 1 行的纯背景色格。

## 4. 实现

`pkg/tui/setup.go`：

- `forebrainMascot` 恢复方案 A 的 12 行网格，`forebrainMascotPalette` 恢复 `W`（`#ffffff`，xterm 231）。
- 新增 `forebrainMascotPixelCols = 2`；`forebrainMascotWidth` = 2 × 20 = 40。
- `forebrainMascotRows` 一个像素行输出一个终端行；`colorPixel` 输出两个背景色空格；`monoPixel` 在无色终端输出 `██`，眼睛和嘴（`P`、`W`）镂空。
- `bannerMascotMinWidth` 由 60 改为 78。边框、40 列吉祥物和间距之外，文案列还剩 29 列。viewport 给转录内容的绘制宽度是终端宽减 `viewportRightPadding`（2），80 列终端实际画 78 列，所以门槛取 78，默认 80 列窗口仍显示吉祥物。
- 居中取整修正：吉祥物（12 行）现在通常比文案高，原来的写法让文案的偏移也向上取整，行数差为奇数时文案会偏低半行。改为吉祥物偏移向上取整、文案偏移向下取整（`textTop := (rows - len(text)) / 2`），两种高低关系下都是吉祥物下沉半行。
- 版本号折行：名称和版本放不进一行时，版本号单独一行，但原来从不折行。dev 构建的伪版本（如 `v0.1.2-0.20261005142755-936df40fba17+dirty`，42 列）在 29 列文案列里会冲出右边框。现在在 `-`、`+` 处折行。路径折行函数一并泛化为 `wrapBannerText(text, width, seps)`。

`pkg/tui/chat_session_test.go`：

- `TestForebrainMascotIsTheApprovedDesign`（取代 `TestForebrainMascotSitsOnWholeRows`）：网格必须与方案 A 逐行相同。它守护的是本次 bug：渲染问题只能在 `forebrainMascotRows` 里修，不得改画吉祥物。
- `TestForebrainMascotPaintsBodyAsBackground`：12 行、每行 40 列、去色后只剩空格；每个上色段恰好 2 个空格，且只带 `48;5;` 背景色；眼睛高光行含 `48;5;231`。
- `TestForebrainBannerWidthFollowsPath`：有吉祥物时卡片宽 = 49（边框、内边距、吉祥物、间距）+ 文案自然宽度；78 列时卡片铺满 78 列。
- `TestForebrainBannerCentresMascotOnText`：宽度 {200, 120, 90, 78} × 三种路径。覆盖吉祥物更高、文案更高、行数差为奇数三种情况；断言两个中线重合或吉祥物低半行；77 列时不画吉祥物。
- `TestForebrainBannerWrapsLongVersion`：dev 伪版本在各宽度下不超宽、不丢字。
- `TestRendererBannerDefaultsToForebrain` 与 `TestRendererViewportSeedsStartupChromeAsBannerBlock` 的冠顶串改为 `██████████████    ██████████████`。

## 5. 验证

- `go vet ./pkg/tui/` 干净；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain ./pkg/architecture -count=1` 全绿。
- 反向检查：
  - 把文案偏移改回向上取整，`TestForebrainBannerCentresMascotOnText` 在 120 列长路径处失败；
  - 去掉版本号折行，`TestForebrainBannerWrapsLongVersion` 在 78 列处失败（卡片行 91 列）。
- 真机（macOS Terminal，Fira Code 14pt）：120 列和 80 列分别截图。吉祥物与设计稿一致，无竖缝也无横缝；文案垂直居中；版本号和路径都在卡片内。
- tmux 字节检查：吉祥物行只有 `48;…` 背景色加空格，没有 `▄`/`▀`，也没有反显。
- 交互回归：composer 可输入，两次 Ctrl+C 退出，退出码 0。（本 TUI 没有 `/quit` 命令，竖线修复计划第 7 步的写法有误。）

## 6. 缩窄窗口残影（owner 要求一并修复，2026-10-07）

**现象**：窗口从 120 列缩到 80 列后，卡片下方残留旧卡片的右边框 `│` 和底边 `──╯` 碎片。再来一次 SIGWINCH 就会清掉。

**根因（在 macOS Terminal 里用最小程序实测）**：

- Terminal.app 缩窄窗口时不会重排 alt screen，也不丢弃超出新宽度的字符，而是把它们藏在行尾。
- `\x1b[2K` 只擦掉可见的那一屏宽，藏起来的尾巴随即滑到第 1 列开始的位置，再被新写的内容盖住开头，剩下的部分就是残影。
- 诊断程序：120 列时每行写 102 列，缩到 80 后每行 `CUP + 2K + 13 列文字`。结果在第 14–22 列冒出 `═══|R`，正好是 22 列尾巴减去 13 列文字。
- 对比几种写法：
  - 连擦两次 `2K`：尾巴不超过一屏宽时能清干净；150 → 50 列（尾巴 100 列）时仍有残影，因为每擦一次只丢一屏宽。
  - `ECH` 也有残影。
  - 只有 `\x1b[2J`（擦整屏）能把尾巴整体丢掉。
- forebrain 在 resize 时会丢掉 shadow，再逐行 `CUP + 2K` 重画，所以每一行的尾巴都被拉进可见区。随后的增量重画按 shadow 判断「这些行没变」，残影就一直留着。

**修复**：`paintViewportLocked`（`pkg/tui/reducer.go`）在 shadow 为空时，即画布对物理屏幕一无所知的那一帧（resize、Ctrl+L、首帧），先写 `\x1b[2J` 再逐行重画。`2J` 落在同步输出 `?2026h/l` 里面，普通的增量重画不会触发它。只有 viewport 模式（alt screen）走这条路径，不影响主屏和滚动历史。

**测试**：`TestViewportResizeErasesTheDisplayFirst`（`pkg/tui/reducer_test.go`）断言：普通重画不含 `2J`；缩窄后的重画含 `2J`，并且在写任何一行之前。去掉修复后该测试失败。

**真机**（macOS Terminal，Fira Code 14pt）全部没有残影：
- 120 → 80；
- 模拟拖拽 110 → 100 → 90 → 80 → 70 → 60 → 120 → 100 → 75；
- 拉回 120；
- 带对话内容（满宽用户卡片、长回复、「Worked for」分隔线）120 → 80。

## 7. 维护说明

- 吉祥物网格就是 owner 批准的设计稿，由 `TestForebrainMascotIsTheApprovedDesign` 锁死。改网格必须先经 owner 拍板，不得为绕开渲染问题而改画。
- 绘制契约：只用背景色，不用任何字形；一个像素 = `forebrainMascotPixelCols` 列 × 1 行。这样在任何终端、字体、字号下都无缝。
- `bannerMascotMinWidth` 必须 ≤ 78（80 列终端的实际绘制宽度），否则默认窗口会丢掉吉祥物。
- 文案列新增的每一行都必须在 `bannerTextColumn` 里折行到列宽以内，并纳入 `bannerTextWidth`。
- 画布丢掉 shadow 后的第一帧必须先 `\x1b[2J` 再画，由 `TestViewportResizeErasesTheDisplayFirst` 守护。不要改回逐行 `2K`，也不要改成连擦两次 `2K`，后者在尾巴超过一屏宽时同样会留残影。
