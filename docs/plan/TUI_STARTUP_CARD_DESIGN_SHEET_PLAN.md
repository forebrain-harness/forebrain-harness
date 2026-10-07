# 计划：TUI 启动卡片按 HTML 设计稿实现（V1 吉祥物 + 虚线边框/横线）

> **执行者须知**：本计划自包含。逐步执行，每步都跑「验证」命令并确认预期输出后再进入下一步。
> 遇到「STOP 条件」立即停下报告，不要即兴发挥。改完把本文件复制到
> `docs/plan/TUI_STARTUP_CARD_DESIGN_SHEET_PLAN.md`（仓库惯例，见 `docs/plan/` 下其它计划）。
>
> **禁止提交**：本仓库默认禁止 commit / push / 开 PR。改动只留工作区交 owner 审阅。
>
> **漂移检查（先跑）**：本计划写于工作区未提交状态（`git rev-parse HEAD` 在 owner 会话期间可能失败）。
> 先确认下列「当前状态」摘录与磁盘一致；不一致就按 STOP 条件处理。

## 状态

- 状态：已实施并真机验收通过（2026-10-07）
- 优先级：P1
- 工作量：M（源码约 20 行 + 测试约 80 行）
- 风险：LOW（纯渲染层，无数据/协议影响；已被测试与真机双重覆盖）
- 依赖：无
- 类别：bug（封面卡片与 owner 批准的 HTML 设计稿不一致）
- 计划时工作区：`main` 分支工作区（含 owner 未提交改动），2026-10-07
- 设计稿（视觉规范来源，owner 已在浏览器确认）：
  `/tmp/mascotcheck/mascot-design-v5.html`（生成器 `/tmp/mascotcheck/sheet_v5.py`）
  本次要固化的那一版截图：启动卡片 · 78 列终端 · 同一比例，V1 吉祥物 + 虚线框 + 虚线横线。

## 为什么必须做

owner 已选定 **V1 吉祥物**（20 列 × 6 行 = 10 × 6 像素）并要求：

1. 启动卡片**整体 UI** 按 HTML 设计稿实现；
2. 卡片**边框**与「Forebrain Harness」下面的**横线**都改成**虚线**，颜色与设计稿一致；
3. 吉祥物造型必须是大脑、可爱，且**任何字体和字号下渲染一致**。

现状与设计稿有三处不一致：吉祥物还是上一版 14 × 8 网格（看起来更粗更方）、边框与横线是实线
`─`/`│`、吉祥物显示门槛是 66 列而不是设计稿的 58 列。修完这三处，启动卡片即与设计稿 1:1，
且吉祥物门槛下降后 60 列的小窗口也能看到吉祥物。

## 当前状态（file:line + 摘录）

### `pkg/tui/setup.go` —— 启动卡片与吉祥物的全部渲染代码

吉祥物网格与调色板（**要换的网格**）：

```go
// setup.go:1629
var forebrainMascot = [...]string{
	".FFFFF..FFFFF.",   // 14 列 × 8 行像素
	"FFFFFFAAFFFFFF",
	"FFWPFFFFFFWPFF",
	"FFPPFFFFFFPPFF",
	"FFFFFPFFPFFFFF",
	".FFFFFPPFFFFF.",
	".FFFFFFFFFFFF.",
	"..FFFF..FFFF..",
}

// setup.go:1643 —— 调色板不动（已是精确 xterm-256）
var forebrainMascotPalette = map[byte]lipgloss.Color{
	'F': "#5fafd7", // body (xterm 74)
	'P': "#080808", // pupils, mouth (xterm 232)
	'W': "#ffffff", // eye shine (xterm 231)
	'A': "#ffaf5f", // harness node (xterm 215)
}

// setup.go:1653-1656
const forebrainMascotPixelCols = 2
var forebrainMascotWidth = forebrainMascotPixelCols * len(forebrainMascot[0])
```

宽度门槛常量（**要改 66 → 58**）：

```go
// setup.go:1671
const (
	bannerProductName = "Forebrain Harness"
	bannerMinWidth    = 78
	bannerMascotMinWidth = 66   // ← 按设计稿应为 58
	bannerBorderMinWidth = 32
	bannerGutter         = 3
	bannerPadding        = 2
)
```

边框样式（**不改颜色**）：

```go
// setup.go:1662
bannerBorderStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#b7c6d4", Dark: "#35506a"})
```

卡片拼装与**实线**边框（改虚线的地方 1、2）：

```go
// setup.go:1746
lines := []string{""}
if framed {
	lines = append(lines, bannerBorderStyle.Render("╭"+strings.Repeat("─", cardW-2)+"╮"))
	lines = append(lines, bannerFramedRow("", cardW))
}
...
if framed {
	lines = append(lines, bannerFramedRow("", cardW))
	lines = append(lines, bannerBorderStyle.Render("╰"+strings.Repeat("─", cardW-2)+"╯"))
}

// setup.go:1778 —— 左右竖边是实线 │
func bannerFramedRow(content string, cardW int) string {
	inner := cardW - 2 - 2*bannerPadding
	pad := max(inner-displayLineWidth(content), 0)
	side := strings.Repeat(" ", bannerPadding)
	return bannerBorderStyle.Render("│") + side + content + strings.Repeat(" ", pad) + side + bannerBorderStyle.Render("│")
}

// setup.go:1802 —— 名称下的横线是实线 ─（改虚线的地方 3）
lines = append(lines, bannerBorderStyle.Render(strings.Repeat("─", width)))
```

**不用改**的部分（渲染契约保持）：`forebrainMascotRows` / `colorPixel` / `monoPixel`
（setup.go:1927-1959，纯背景色、一格一色、无色终端画剪影）、`bannerTextColumn` 的行结构
（名称+版本 / 横线 / 路径 / 空行 / 快捷键两列）、`bannerShortcutRows`、`wrapBannerText`、
`bannerVersion`、居中规则 `artTop/textTop`（setup.go:1743-1744）。

### 调用路径（决定了改一处即全生效）

- `pkg/tui/setup.go:1699 RenderForebrainBanner` → `forebrainBannerLines`（非交互/首屏输出）
- `pkg/tui/render.go:638` 调 `RenderForebrainBanner`
- `pkg/tui/reducer.go:2981`：`case FrameBannerInfo:` → `forebrainBannerLines(f.Title, f.Content, width)`
  —— 每帧按**实际绘制宽度**重排（resize 会重排），所以 20 列吉祥物与 58 列门槛在 TUI 里同样生效。

### 现有测试（要跟着改的断言）

- `pkg/tui/chat_session_test.go:16303 TestForebrainMascotIsTheApprovedDesign` —— 逐行 pin 住 14×8 网格，**必须换成 V1**。
- `pkg/tui/chat_session_test.go:16230`：`strings.Contains(got, "██████████    ██████████")` —— 旧网格第 0 行的剪影，**要换**。
- `pkg/tui/chat_session_test.go:17762`：同一个冠顶串 `crown := "██████████    ██████████"` —— **要换**。
- 自适应、**不用改**的：`TestForebrainMascotPaintsBodyAsBackground`（用 `len(forebrainMascot)` /
  `forebrainMascotWidth`）、`TestForebrainMascotPaletteIsExactXterm256`、
  `TestForebrainBannerWidthFollowsPath`（`chrome` 由 `forebrainMascotWidth` 算出）、
  `TestForebrainBannerCentresMascotOnText`（用 `bannerMascotMinWidth±1`）、
  `TestForebrainBannerKeepsFullWorkingDirectory`、`TestForebrainBannerWrapsLongVersion`。

### 设计稿冻结值（必须逐字一致）

吉祥物 V1（10 × 6 像素；`F` 身体 / `P` 瞳嘴 / `W` 高光 / `A` 琥珀节点 / `.` 透明）：

```
.FFFFFFFF.
FFFFAAFFFF
FFWPFFWPFF
FFPPFFPPFF
.FFFPPFFF.
..FF..FF..
```

虚线字形（**已核对 Fira Code 覆盖率与字宽**，`~/Library/Fonts/FiraCode-*.ttf`，advance 全为 1200，
与 `─`(U+2500)/`│`(U+2502) 相同 → 不触发字体回退、列宽不变）：

| 用途 | 字形 | 码位 |
|---|---|---|
| 横向虚线 | `┄` | U+2504 |
| 纵向虚线 | `┆` | U+2506 |
| 四角（保持实线圆角） | `╭` `╮` `╰` `╯` | U+256D U+256E U+2570 U+256F |

## 修复设计

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 性质 |
|---|---|---|---|
| 终端宽 58–65 列 | 不画吉祥物，文案列 = 卡宽−6 | **画吉祥物**，文案列 = 卡宽−29 | 刻意修订（门槛 66→58） |
| 终端宽 < 58 列 | 不画吉祥物 | 不画吉祥物（不变） | 保持 |
| 卡片边框 | 实线 `╭──╮ │ ╰──╯` | **虚线** `╭┄┄╮ ┆ ╰┄┄╯` | 刻意修订 |
| 名称下横线 | `─` × 文案列宽 | `┄` × 文案列宽 | 刻意修订 |
| 边框/横线颜色 | 深色 #35506a / 浅色 #b7c6d4 | **不变** | 保持 |
| 吉祥物网格 | 14 × 8 像素（28 列 × 8 行） | **10 × 6 像素（20 列 × 6 行）** | 刻意修订（owner 选 V1） |
| 卡片宽度下限 / 上限 | max(内容, 78)，上限=终端宽 | 不变 | 保持 |
| 吉祥物与文案居中 | 各自居中，奇数差时吉祥物下沉半行 | 不变（两者都是 6 行时正好对齐） | 保持 |
| 版本号/路径折行、快捷键两列、无色终端剪影 | — | 不变 | 保持 |
| 快捷键两列之间的间距 | 补齐到最长项(itemW) + `bannerGutter+1`（78 列卡实测 8 空格） | 不变 | 保持（见下方说明） |

### 设计稿与本计划的唯一已知差异（避免执行者"照 mock 改代码"）

`/tmp/mascotcheck/mascot-design-v5.html` 的卡片 mock 里，快捷键两列之间我手写成 4 个空格，
而真实代码是「先补齐到最长项宽度 14，再写 4 空格分隔」（78 列卡实测 8 空格）。
**以代码为准**（两列对齐的视觉效果一致）：冻结设计稿到 `docs/design/STARTUP_CARD.html` 时
把 mock 的间距改成与代码一致的算法（`pad to itemW` + `bannerGutter+1`），**不要**为迁就 mock 去改
`bannerShortcutRows`。

### 门槛推导（写进注释，供后续维护）

`bannerMascotMinWidth = 边框(2×1) + 内边距(2×2) + 吉祥物宽(20) + 间距(3) + 文案列(29) = 58`，
且必须 ≤ 78（80 列终端由 viewport 按 `80 − viewportRightPadding` 绘制）。

## Scope

**In scope（只许改这些文件）**

- `pkg/tui/setup.go` —— `forebrainMascot` 网格、`bannerMascotMinWidth`、三处 `─`/`│` → `┄`/`┆`、注释更新
- `pkg/tui/chat_session_test.go` —— 网格 pin、两处冠顶串、新增虚线测试
- `docs/plan/TUI_STARTUP_CARD_DESIGN_SHEET_PLAN.md` —— 本计划落盘（实施时复制过去）
- `docs/plan/MASCOT_COMPACT_GRID_PLAN.md`、`docs/plan/MASCOT_WHOLE_CELL_PIXELS_PLAN.md`、
  `docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md` —— **各加一行「已被本计划取代」**，正文不改
- （可选，默认做）`docs/design/STARTUP_CARD.html` —— 把设计稿冻进仓库作为视觉规范（见 Step 6）

**Out of scope（明确不碰）**

- `pkg/gateway/serve_run.go` 的 `bannerArt`（`gateway serve` 的 ASCII 字标，另一个 surface，与启动卡片无关）
- `pkg/tui/render.go` 的 `displayLineWidth` / `termWidthOrDefault` / `wrapCardLine`（通用基础设施）
- 首启向导的其它页面（`pageProgressRule` 等用的是 `━`/`─`，与本卡片无关）
- `forebrainMascotRows` / `colorPixel` / `monoPixel`（**渲染契约不动**：纯背景色、零字形）
- 调色板颜色值（owner 已确认沿用）
- web 前端（本计划只改 TUI；web 若要 1:1 parity 另开计划）
- 任何非 TUI 行为（模型、权限、MCP、状态库）

## 命令

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| Vet | `go vet ./pkg/tui/` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Forebrain\|Banner\|Mascot\|StartupChrome' -count=1` | 全部 ok |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain -count=1` | 全部 ok |
| （非门槛）架构测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | **改动前就已失败**（owner 未提交的 `pkg/tool/helpers_test.go`），不要修 |
| 真机构建 | `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0（**必须带 -tags fts5**，否则 TUI 起不来：`no such module: fts5`） |

## Steps

### Step 1：换吉祥物网格与门槛

`pkg/tui/setup.go`：

- `forebrainMascot` 换成上面的 V1 10 × 6 网格（10 个字符 × 6 行，每行宽度必须相同，否则 `forebrainMascotWidth` 失真）。
- 注释改为：设计来源 = 本计划 + `docs/design/STARTUP_CARD.html`，10 × 6 像素 / 20 列 × 6 行，
  「改网格须先经 owner 拍板，由 `TestForebrainMascotIsTheApprovedDesign` 把守」。
- `bannerMascotMinWidth`：`66` → `58`，注释按上面的门槛推导改（含「必须 ≤ 78」）。
- `forebrainMascotWidth` 由公式自动变为 20（不要写死 20）。

**验证**：`grep -n "bannerMascotMinWidth = " pkg/tui/setup.go` → 输出 `= 58`；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestForebrainMascotPaletteIsExactXterm256 -count=1` → ok。

### Step 2：边框与横线改虚线

`pkg/tui/setup.go`：

- 新增两个常量（就近放在 `bannerGutter`/`bannerPadding` 旁），并把理由写进注释：

```go
// bannerFrameH / bannerFrameV are the dashed frame: a solid box-drawing
// glyph is a font shape, but these two are the same 1200-unit advance in
// Fira Code as ─ and │, so the frame keeps its column math and renders
// dashed in every font that has U+2504/U+2506 (Fira Code does).
const (
	bannerFrameH = "┄" // U+2504, horizontal dashed (three dashes)
	bannerFrameV = "┆" // U+2506, vertical dashed (three dashes)
)
```

- 上/下边框：`"╭"+strings.Repeat("─", cardW-2)+"╮"` → `"╭"+strings.Repeat(bannerFrameH, cardW-2)+"╮"`；下边框同理。
- `bannerFramedRow`：两侧 `"│"` → `bannerFrameV`。
- `bannerTextColumn` 里名称下的横线：`strings.Repeat("─", width)` → `strings.Repeat(bannerFrameH, width)`。
  注释写明「名称下的分隔线是虚线，与卡片边框同一造型」。
- 颜色样式 `bannerBorderStyle` **一个字都不改**（深色 #35506a / 浅色 #b7c6d4）。

**验证**：`grep -c '"─"\|"│"' pkg/tui/setup.go` → 输出 `0`（setup.go 里不再有任何实线边框字形；
全仓其它文件（`render.go:4937/5983/7000`、`reducer.go:4523/5273`、`overlays.go:1289`）里的 `─`
属于别的 surface，是 out of scope）。再跑 `grep -n 'bannerFrameH\|bannerFrameV' pkg/tui/setup.go`
→ 至少 6 行（2 处常量声明 + 上边框 + 下边框 + bannerFramedRow 的左右两处 + 名称下横线）。

### Step 3：更新被 pin 住的断言与新增虚线测试（`pkg/tui/chat_session_test.go`）

1. `TestForebrainMascotIsTheApprovedDesign`（:16303）：`approved` 换成 V1 的 6 行；补断言
   `len(forebrainMascot[0]) == 10`、`forebrainMascotWidth == 20`；注释说明这版是 20 列 × 6 行、
   取代 `docs/plan/MASCOT_COMPACT_GRID_PLAN.md` 的 14×8。
2. 两处冠顶串（:16230、:17762）换成 V1 第 0 行的剪影。已实测：无色 profile 下
   `.FFFFFFFF.` 渲染为 `"  ████████████████  "`（2 空格 + 16 个 `█` + 2 空格）。**用带两侧空格的整串**，
   这样断言唯一指向冠顶行（第 1 行是满宽 20 个 `█`；第 4、5 行中间有缺口）。
3. 新增 `TestForebrainBannerFrameIsDashed`（无色 profile，避免 SGR 干扰）：

```
width := 120
lines := forebrainBannerLines("v0.1.0", "~/workspace/forebrain", width)
plain := 各行 stripANSI
// 78 列卡的实测行序（共 11 行）：[0] 前导空行，[1] 上边框，[2] 边框内空行，
//   [3] 名称+版本，[4] 名称下的横线，[5] 路径，[6] 空行，[7..8] 快捷键两行，
//   [9] 边框内空行，[10] 下边框
top := plain[1]
断言: strings.HasPrefix(top, "╭┄") && strings.HasSuffix(top, "┄╮")
断言: !strings.Contains(top, "─")
断言: 每个 body 行（plain[2]..plain[len-2]）以 "┆" 开头、以 "┆" 结尾
断言: plain[len-1] 以 "╰┄" 开头、以 "┄╯" 结尾
断言: plain[4]（名称下的横线）去掉边框与两侧内边距（`strings.TrimSuffix(strings.TrimPrefix(...))` 后再按
      `bannerPadding` 切）后只含 "┄"，且长度 = 文案列宽（78 列卡 = 49；已实测 plain[4] 的 "┄" 计数 = 49）
断言: 整张卡片不含 "│" 与 "─"（虚线化完成的机器判据）
```

4. 新增 `TestForebrainBannerKeepsMascotAtFiftyEight`：照既有写法用剪影判定吉祥物是否存在
   （`strings.ContainsRune(stripANSI(line), '█')`，见 `TestForebrainBannerCentresMascotOnText`:16462）：

```
for _, width := range []int{58, 59, 66, 78} {   // 有吉祥物
    断言: 至少一行的 stripANSI 含 '█'
    断言: 每行 displayLineWidth(line) <= width
}
for _, width := range []int{24, 50, 57} {       // 无吉祥物
    断言: 没有任何行含 '█'
}
```

5. 既有 `TestForebrainBannerCentresMascotOnText`（:16456）**实测在新网格下直接 PASS，不需要改**
   （已用 overlay 真跑确认）：58 列时文案列只有 29 列，`tallDir`（320 字）折成多行，文案仍比 6 行吉祥物高。
   若它在你手上失败，说明工作区已漂移 → 按 STOP 条件 #1 停下报告，**不要**放宽断言或改测试数据。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Forebrain|Banner|Mascot|StartupChrome' -count=1`
→ 全部 ok（含 2 个新测试）。

### Step 4：包级回归与 vet

- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain -count=1` → 全 ok
- `go vet ./pkg/tui/` → 无输出
- **不要**把 `./pkg/architecture` 当成门槛：它在**本次改动之前就已失败**
  （`TestTestFilesCorrespondToProductionFiles` 报 `pkg/tool/helpers_test.go has no helpers.go beside it`
  —— owner 未提交的 untracked 文件造成，属 Scope 之外）。若你想确认与本次改动无关，可跑
  `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` 并在报告里如实注明该失败是既有问题，**不要去改 `pkg/tool`**。

### Step 5：tmux 真机验收（必做，且**必须先解决三道启动门**）

用全新 `FOREBRAIN_HOME` + 全新项目目录启动时，交互路径在画卡片之前会依次遇到：
workspace trust 页（默认停在 Quit，裸 Enter 会直接退出，需 ↓ 再 Enter）、
`tui.NeedsFirstSetup` 的 onboarding 向导（空 home 没有 `forebrain.yaml` / 未配置主模型）、
以及缺 provider 时的启动失败。**直接 `start` 只会 capture 到信任页或向导，看不到启动卡片。**

仓库已有现成做法：`.claude/skills/run-forebrain/driver.sh`（写 `$HOME/forebrain.yaml` 配 fake provider
与 `api_key`，并自动回答信任页）。**优先复用它**；若不想用 driver，就手工复刻同样的前置：

```bash
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
mkdir -p /tmp/cardcheck/home /tmp/cardcheck/proj
cat > /tmp/cardcheck/home/forebrain.yaml <<'YAML'
agent:
  name: main
  llm:
    provider: openai
    model: gpt-5
    api_key: FAKE_KEY_FOR_RENDER_CHECK
YAML
tmux new-session -d -s card -x 120 -y 40 \
  'cd /tmp/cardcheck/proj && FOREBRAIN_HOME=/tmp/cardcheck/home ./build/bin/forebrain; echo EXIT=$?'
sleep 2
tmux send-keys -t card Down; sleep 0.3; tmux send-keys -t card Enter   # 过信任页
sleep 3
tmux capture-pane -t card -p -e | cat -v
```

逐项取证（capture 证据贴进本计划末尾的「验收记录」章节）：

1. **120 / 80 列**：卡片上边 `╭┄`、下边 `╰┄`、左右 `┆`；名称下横线为 `┄`；卡片范围内**无 `│`/`─`**
   （同屏的向导/提示页可能有其它 `─`，只针对卡片那几行断言）。
2. **78 列**（80 列终端实际绘制宽）：吉祥物 20 列 × 6 行，与右侧 6 行文案等高居中。
3. **58–65 列**：`tmux resize-window -t card -x 60 -y 40` → 吉祥物**仍在**，路径与版本号在卡片内折行。
4. **57 列**：`tmux resize-window -t card -x 57 -y 40` → 吉祥物消失，文案完整、不超宽。
5. **字节级（机器判据）**：`capture-pane -e` 里吉祥物部分只有 `48;5;<n>` + 空格，**零字形**；
   卡片边框行里能 grep 到 `┄`/`┆`。
6. **真机终端肉眼比对（人眼步骤，非机器判据）**：在 owner 自己的终端跑
   `bash /tmp/mascotcheck/v5_V1.sh`，与设计稿截图 `/tmp/mascotcheck/mascot-design-v5.html` 的
   「启动卡片」一栏对齐。
7. **退出干净**：`tmux send-keys -t card C-c` 两次；再 `tmux capture-pane -t card -p` 应看到 `EXIT=0`
   （所以上面的启动行必须带 `echo EXIT=$?`）。

**验证**：上述 7 项逐条有 capture 证据；发现偏差回到 Step 1–3 修，不得只改验收口径。

### Step 6：落盘计划与设计稿

- 把本文件内容复制到 `docs/plan/TUI_STARTUP_CARD_DESIGN_SHEET_PLAN.md`（用 `mkdir -p docs/plan` 不必要，
  该目录已存在），把其「状态」一行改为「已实施并真机验收通过（2026-10-07）」，并把本计划末尾
  「验收记录」的实际证据一并带过去。
- 三份旧计划补「已被取代」一行（只加一行说明，**不改它们的正文结论**）：
  - `docs/plan/MASCOT_COMPACT_GRID_PLAN.md`（:9/:27-34/:44/:50/:65 陈述 14×8、28 列、门槛 66）
  - `docs/plan/MASCOT_WHOLE_CELL_PIXELS_PLAN.md`（:9/:49/:101 陈述 40×12 与门槛 78）
  - `docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md`（:429 写「边框是 ╭─╮│╰╯，分隔线是 ─」）
  新指向：`docs/plan/TUI_STARTUP_CARD_DESIGN_SHEET_PLAN.md`。
- （可选，默认做）`mkdir -p docs/design` 后把 `/tmp/mascotcheck/mascot-design-v5.html` 复制为
  `docs/design/STARTUP_CARD.html`，作为启动卡片的视觉规范与以后 web 面 parity 的参照，
  并在 `pkg/tui/setup.go` 的卡片注释里指向它。
- 清理：`tmux kill-session -t card`；`rm -rf /tmp/cardcheck`。

## Test plan

- 新增：`TestForebrainBannerFrameIsDashed`（边框/横线虚线化 + 无实线残留，机器判据）、
  `TestForebrainBannerKeepsMascotAtFiftyEight`（门槛 58/57 边界）。
- 更新：`TestForebrainMascotIsTheApprovedDesign`（V1 网格 + 10×6/20 列断言）、
  `TestRendererBannerDefaultsToForebrain`、`TestRendererViewportSeedsStartupChromeAsBannerBlock`（冠顶串）。
- 结构样板：新增测试照 `TestForebrainBannerWidthFollowsPath`（:16399）的写法——
  `lipgloss.SetColorProfile(termenv.Ascii)` + `t.Cleanup` 复原，用 `stripANSI`/`displayLineWidth` 断言。
- 回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain ./pkg/architecture -count=1` 全绿。

## Done criteria

全部满足才算完成：

- [x] `go vet ./pkg/tui/` 无输出
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Forebrain|Banner|Mascot|StartupChrome' -count=1` 全 ok，含 2 个新测试
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain -count=1` 全 ok
- [x] `grep -c 'bannerMascotMinWidth = 58' pkg/tui/setup.go` → `1`
- [x] `grep -c '"─"\|"│"' pkg/tui/setup.go` → `0`（setup.go 内已无实线边框字形）
- [x] `grep -c 'bannerFrameH\|bannerFrameV' pkg/tui/setup.go` → `>= 6`（实测 7）
- [x] `grep -c '┄' pkg/tui/setup.go` → `>= 1`（字面量只出现在 `bannerFrameH` 的声明行，其余是引用；
      真正的门是新增的 `TestForebrainBannerFrameIsDashed`）
- [x] `grep -c '"\.FFFFFFFF\."' pkg/tui/setup.go` → `1`（新网格冠顶行）
- [x] tmux 真机 5 个宽度场景（120/80/78/60/57）capture 证据齐备；吉祥物字节里零字形；边框行含 `┄`/`┆`
- [x] 本次改动只落在 Scope 列出的文件：`git status --porcelain` 与 `git diff --name-only` 里
      **新增/修改项只有 `pkg/tui/setup.go`、`pkg/tui/chat_session_test.go` 与 Scope 里的 docs**
      （工作区本就有 60 余项 owner 未提交改动，**那些属既有状态，不是本次改动**，不要试图清理）
- [x] 计划文件已落盘 `docs/plan/TUI_STARTUP_CARD_DESIGN_SHEET_PLAN.md` 并回填验收记录

## STOP 条件

遇到以下情况**停下报告**，不要自行发挥：

1. `pkg/tui/setup.go` 的 `forebrainMascot` / `bannerMascotMinWidth` / 三处 `─│` 与「当前状态」
   摘录不一致（工作区已漂移，可能是 owner 并行改了同一文件——本仓库发生过）。
2. 出现**计划外**的测试失败：本计划的作者已用 overlay 真跑确认，改动后 `./pkg/tui` 里
   **只有 3 个既有测试会失败**，且正是 Step 3 要更新的那 3 个
   （`TestRendererBannerDefaultsToForebrain`、`TestForebrainMascotIsTheApprovedDesign`、
   `TestRendererViewportSeedsStartupChromeAsBannerBlock`）。若还有第 4 个失败，先报告再统一处理。
   已知的**既有失败**（与本计划无关，不要修）：`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture`
   的 `TestTestFilesCorrespondToProductionFiles`（owner 未提交的 `pkg/tool/helpers_test.go` 造成）。
3. 虚线字形导致行宽断言失败（即 `displayLineWidth("┄") != 1`）——本计划已实测两者都为 1，
   若你的环境不是，说明前提被推翻，回来重新选字形（候选 `┈` U+2508 / `╌` U+254C），
   **不要**去放宽断言的宽度上限。
4. 需要改 `forebrainMascotRows` / `colorPixel` / `monoPixel` 才能让某个场景通过
   ——渲染契约是「纯背景色、零字形」，改它等于回到竖线 bug。
5. 需要动 Scope 之外的文件才能通过（尤其 `render.go`、`reducer.go` 的通用渲染，与 `pkg/tool`）。
6. tmux 真机跑不到「启动卡片」这一屏（例如卡在信任页或 onboarding 向导）且照 Step 5 的配方
   两次尝试仍不行——报告当时的 capture，不要为了拿到证据去改产品代码或绕过信任门槛。

## 维护说明

- **虚线与网格都是设计契约**：`TestForebrainBannerFrameIsDashed` 把守虚线，`TestForebrainMascotIsTheApprovedDesign`
  把守网格；视觉规范在 `docs/design/STARTUP_CARD.html`（如采纳）。改它们必须先经 owner 拍板。
- `bannerMascotMinWidth` 必须等于 `6 + forebrainMascotWidth + 3 + 29`，且 ≤ 78；换网格时同步重算。
- `bannerFrameH`/`bannerFrameV` 的选择前提是「与 `─`/`│` 同字宽」；换字体或加宽字符集时重新核对
  （本计划已核对 Fira Code U+2504/U+2506 advance = 1200 == U+2500/U+2502）。
- 复核者重点看：卡片行宽是否仍 ≤ 绘制宽度、吉祥物是否仍是零字形、58/57 列边界行为、
  以及 `pkg/tui` 是否出现了别处的实线边框回归。
- 明确延后：web 前端的启动卡片 parity（本项目有 TUI/Web 1:1 常设要求），本计划不碰。

## 验收记录

（2026-10-07 执行者在真机上逐条取证。驱动方式：优先复用仓库现成做法
`.claude/skills/run-forebrain/driver.sh`（隔离 `FOREBRAIN_HOME` + fake provider + 自动回答信任页），
而不是本计划 Step 5 里那份手写配方；两者前置等价，driver 额外解决了「信任页 + 空 home 的 onboarding」
两道启动门，因此启动卡片直接可见。终端宽度用 `tmux resize-window` 调整。）

| # | 场景 | 命令 | 实测结果 | 结论 |
|---|---|---|---|---|
| 1 | 120 / 80 列：边框虚线、横线虚线 | `tmux capture-pane -t forebrain-run -p` | 120 列终端：卡片宽 96（长路径撑宽），`╭┄…┄╮` / `┆ … ┆` / `╰┄…┄╯`，名称下横线为 `┄`；80 列终端：卡片宽 78，同上。两者卡片行内 `│`/`─` 计数 = 0 | PASS |
| 2 | 78 列：吉祥物 20×6 居中 | 同上 + `-e` | 78 列卡：文本自第 27 列起（`┆` + 2 内边距 + 20 列吉祥物 + 3 间距），6 行吉祥物与文案等高居中；`-e` 里吉祥物行为 `48;2;95;175;215`/`48;2;255;175;95`/`48;2;255;255;255`/`48;2;8;8;8` + 空格 | PASS |
| 3 | 60 列：吉祥物仍在、文案折行 | `tmux resize-window -t forebrain-run -x 60 -y 40` | 终端 60 列 → 绘制宽 58 → 卡片宽 58；吉祥物仍在（文本自第 27 列起，卡片内 `48;` 背景色转义 6 个）；版本号折成 `v0.1.2-0.20261007094715` + `-88eb320dcb2e+dirty`，路径折成 `…/T` + `/forebrain-run/proj` | PASS |
| 4 | 57 列：吉祥物消失、不超宽 | `tmux resize-window -t forebrain-run -x 57 -y 40` | 终端 57 列 → 绘制宽 55 → 卡片宽 55；吉祥物消失（文本自第 4 列起，卡片内 `48;` 转义 0 个），路径 2 行折行，所有行 ≤ 55 列 | PASS |
| 5 | 字节级：吉祥物零字形、边框含 ┄/┆ | 同 3/4 + 解析 `capture-pane -e` | 吉祥物 20 列内可见字符全为空格（`bad_glyphs=[]`）；边框/横线为 `┄`/`┆`；**本机 tmux 走 truecolor，吉祥物转义是 `48;2;R;G;B` 而不是计划里写的 `48;5;<n>`** —— 二者同一套精确 xterm-256 配色值（#5fafd7 / #ffaf5f / #ffffff / #080808），256 色路径由 `TestForebrainMascotPaintsBodyAsBackground`（强制 ANSI256 profile）把守 | PASS |
| 6 | 真机终端肉眼比对设计稿 | owner 在自己的终端跑 `bash /tmp/mascotcheck/v5_V1.sh`，对照浏览器里 `docs/design/STARTUP_CARD.html` 的启动卡片一栏 | 未执行（人眼步骤，留给 owner；设计稿已按代码算法修正两列间距并冻结进仓库） | 待 owner 过目 |
| 7 | 退出干净 | `tmux send-keys -t forebrain-run C-c` ×2 | 面板留下 `EXIT=0`（启动行带 `echo EXIT=$?`），`tui.err` 为空 | PASS |

闸门与边界（Step 5 第 3、4 条的精确边界，用 `tmux resize-window` 逐格扫）：

| 终端宽 | 绘制宽（= 终端 − `viewportRightPadding`） | 卡片宽 | 卡片内 `48;` 背景色转义数 | 结论 |
|---|---|---|---|---|
| 58 | 56 | 56 | 0 | 无吉祥物 |
| 59 | 57 | 57 | 0 | 无吉祥物 |
| 60 | 58 | 58 | 6 | 有吉祥物（6 行全部绘制） |
| 61 | 59 | 59 | 6 | 有吉祥物 |

即门槛落在 `bannerMascotMinWidth = 58`：绘制宽 58 有、57 无，与计划一致。
（计划里「58–65 列」「57 列」按终端宽写，实际判定用的是**绘制宽**，差值为
`viewportRightPadding = 2`；不影响结论。）

### 取证命令（可复现）

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset; $D build; $D start hang
tmux capture-pane -t forebrain-run -p            # 120 列：卡片 96 宽、边框虚线
tmux resize-window -t forebrain-run -x 80 -y 40  # → 卡片 78（= 80 − 2）
tmux resize-window -t forebrain-run -x 60 -y 40  # → 卡片 58，吉祥物仍在
tmux resize-window -t forebrain-run -x 59 -y 40  # → 卡片 57，吉祥物消失
tmux capture-pane -t forebrain-run -p -e         # 吉祥物：48;2;… + 空格，零字形
tmux send-keys -t forebrain-run C-c; sleep 1; tmux send-keys -t forebrain-run C-c
tmux capture-pane -t forebrain-run -p | grep EXIT   # EXIT=0
$D stop
```

### 踩到的坑

- `capture-pane -p`（不带 `-e`）**看不到吉祥物**：吉祥物是纯背景色、零字形，纯文本抓屏只剩空格。
  判定「有没有吉祥物」要用 `-e` 数卡片行里的 `48;` 背景色转义（有 = 画了），
  或看文案起始列（第 27 列 = 有吉祥物、第 4 列 = 没有）。这是本计划吉祥物门控最可靠的机器判据。
- 绘制宽 = 终端宽 − `viewportRightPadding`（2），所以「终端 60 列」对应「卡片 58 列」；
  照抄计划里的终端宽数字会以为 57 列应当还有吉祥物。
- `sheet_v5.py`（设计稿生成器）把输出路径**写死**为 `/tmp/mascotcheck/mascot-design-v5.html`：
  重跑它会原位重写那份定稿。本次为核对「生成器能否复现设计稿」跑过一次，
  重写后文件大小与重写前一致（195710 字节，同脚本同输入、输出确定），定稿未被改动。
  冻结进仓库的那一版改用打补丁后的副本 `/tmp/freeze_sheet_v5.py` 生成，只改了快捷键两列间距。

### 落盘产物

- `docs/design/STARTUP_CARD.html`：设计稿定稿（195,710 → 196,235 字节；
  与 `/tmp/mascotcheck/mascot-design-v5.html` 逐字节比对，差异**只有** 3 处卡片 mock 的快捷键两列间距：
  `/ commands` … `@ mention` 由 4 空格改为 8 空格、`esc interrupt` … `ctrl+j newline` 由 2 空格改为 5 空格，
  即代码的「先补齐到 `itemW = 14`，再写 `bannerGutter + 1 = 4` 个空格」，与真机 capture 一字不差）。
  其余 555 行正文未动。
