# 计划：TUI 启动卡片吉祥物去方块化重绘、宽度随路径自适应、吉祥物与文案垂直居中

> **执行者须知**：按顺序执行每一步。每步都要运行它的验证命令，确认结果符合预期后，才进入下一步。
> 出现「STOP 条件」里的任何一种情况，就停下来报告，不要自行变通。
> **本仓库禁止执行者提交代码**：所有改动保持为未提交的工作区改动，由 owner 审阅后手动提交。
> 不要 `git commit`、`--amend`、rebase、merge 或 push。
>
> **漂移检查（最先运行）**：`git diff --stat 13ff5cf..HEAD -- pkg/tui/setup.go pkg/tui/chat_session_test.go`
> 如果这两个文件在本计划写成之后有改动，先把下文「现状」里的代码摘录和实际代码逐段比对；有任何不一致，按 STOP 条件处理。

## 状态

- **优先级**：P2
- **工作量**：S
- **风险**：LOW（只改启动卡片的排版和绘制，不碰模型请求）
- **依赖**：无
- **类别**：bug（视觉缺陷）+ owner 已拍板的设计变更
- **计划基于**：commit `13ff5cf`，2026-10-05
- **预览（owner 已确认方案 A）**：https://claude.ai/artifact/4NFmLaLrMfboe6YLoU8dkc
- **绘制规则已被 MASCOT_VERTICAL_SLIVER_LINES_PLAN 取代（2026-10-06，竖线回归）**
- **「吉祥物 6 行」与半块字形绘制已被 MASCOT_WHOLE_CELL_PIXELS_PLAN 取代（2026-10-07）：方案 A 网格保留，改为每像素 2 列 × 1 行的纯背景色，吉祥物 40×12；卡片宽度、下限 78、居中规则不变**

## 目标（Why this matters）

owner 在自己的终端里看到，启动卡片左侧的吉祥物「看上去是由很多方块拼起来的」。owner 的裁决如下（原话）：

1. 「吉祥物的造型本身没问题，只是不想要这种方块效果」。
2. 在预览页的两个候选里「选择方案 A」：头顶并成左右两瓣半球，中缝下面卡着琥珀色节点。
3. 「吉祥物图案和右侧文案必须垂直居中对齐」，并补充「现在右侧文案整体偏低」。
4. 「TUI 启动卡片宽度必须可以自适应路径长度，不能写死」。
5. （2026-10-05 补充裁决）宽度保留一个下限：**78 列**。内容不足时卡片至少 78 列宽；长路径时卡片超出 78 继续变宽，不再被压回 78。

本计划落地之后：

- 吉祥物是一整块纯色剪影，没有行间横缝，也没有杂色。在 macOS Terminal（有行距、只支持 256 色）和标准终端里看起来一样。
- 卡片宽度不小于 78 列，路径越长卡片越宽，没有固定上限。只有终端本身放不下时，路径才在分隔符处换行。
- 吉祥物与文案列的垂直中线在默认情况下完全重合。

## 根因（证据）

以下数据都从 owner 的截图里量出（截图 388×338，2x），截图里一个终端格是 18×40 像素。

owner 用的终端是 macOS Terminal：本机 `TERM_PROGRAM=Apple_Terminal`，`COLORTERM` 为空，所以 lipgloss 走 ANSI256 profile。

### 方块感：三个原因叠加

**原因 1：行间横缝。**

- 一行高 40px，但方块字符 `█▀▄` 是由字体画的，只覆盖下面 34px。每行顶上那 6px 露出的是单元格的背景色。
- 现在的 `colorHalfBlock`（`pkg/tui/setup.go:1910`）有两种格子的背景是终端默认色，因此每行顶上会露出约 5–6px 的终端底色：
  - 上下同色时，画前景 `█`；
  - 下半格透明时，画前景 `▀`。
- 截图实测：x=100 这一列在 y=154–158、234–238、274–278 处都是终端底色 `#2b2f3a`。

**原因 2：同一种颜色显示成两种亮度。** 这台终端会把「默认背景上的前景色」提亮，而颜色作为背景色使用时原样显示。

- `#5fafd7` 作背景色时显示为 `#5fafd7`，作前景 `█` 时变成 `#80cef8`。
- 轮廓色 `#005f87` 作前景时变成 `#5598c3`。
- 前景叠在显式背景上时不会被提亮。例如 `▀` 前景 `#005f87`、背景 `#5fafd7`，两色都原样显示。

**原因 3：七种色调逐格交错。**

- `forebrainMascotPalette`（`setup.go:1641`）有七种颜色：轮廓、身体、高光、褶皱、眼白、瞳孔、节点。
- 在 256 色下，褶皱色 `#3d8ccc` 被量化成偏紫的 `#5f87d7`（xterm 68），身体色 `#5fb0ea` 被量化成 `#5fafd7`（xterm 74）。

只把颜色换成纯色并不能消除方块感，因为横缝来自原因 1 和原因 2，与配色无关。预览页「之前为什么像拼起来的方块」一节有对照图。

### 文案偏低

`forebrainBannerLines`（`setup.go:1728-1735`）按「行数」居中，而且向上取整：

```go
rows := max(len(text), len(art))
// Centre the text on the mascot; the mascot's last row is half clear, so
// round the offset up.
offset := 0
if len(text) < len(art) {
	offset = (len(art) - len(text) + 1) / 2
}
```

- 现在的吉祥物占 7 行，但墨迹只有 13 个半行（最后一个像素行是空的），墨迹中线在第 3.25 行。
- 文案有 6 行，被整体下移 1 行，中线在第 4.0 行。
- 两者相差 0.75 行，文案偏低。这和 owner 看到的「整体偏低」一致。

还有一个既有缺陷，这次一并修掉：当文案比吉祥物高时（路径换行，或窄卡片下快捷键排成单列），`offset` 恒为 0，吉祥物被钉在顶部，没有居中。

### 宽度写死

`setup.go:1669-1671` 定义了 `bannerMaxWidth = 78`，`setup.go:1709` 用 `cardW := min(width, bannerMaxWidth)`。

- 卡片最多 78 列，文案列因此最多 78 − 6（边框与内边距）− 23（吉祥物与间距）= 49 列。
- 路径一超过 49 列，就算终端有 200 列宽，也会被折成多行。
- 折行又会让文案变高，进一步加剧上面「吉祥物被钉在顶部」的问题。

## 设计（owner 已在预览中确认）

### 像素网格：方案 A，12 个像素行，正好 6 个终端行

与预览初稿相比，只删掉了额头下面一行全宽像素，让上下边都落在行边界上。居中需要这一点，见下文「居中规则」。

```
..FFFFFFF..FFFFFFF..
..FFFFFFF..FFFFFFF..
.FFFFFFFFAAFFFFFFFF.
.FFFFFFFFFFFFFFFFFF.
FFFFFFFFFFFFFFFFFFFF
FFFFFWPFFFFFFWPFFFFF
FFFFFPPFFFFFFPPFFFFF
FFFFFFFFPFFPFFFFFFFF
.FFFFFFFFPPFFFFFFFF.
.FFFFFFFFFFFFFFFFFF.
..FFFFFFFFFFFFFFFF..
....FFFFF..FFFFF....
```

网格必须满足两条不变量，由步骤 5 的测试强制：

- **行数为偶数，且第一行和最后一行都有墨。** 也就是说，吉祥物的上下边都落在终端行的边界上。
- **不存在「上半格透明、下半格有墨」的格子**，即头顶轮廓对齐整行。这种格子只有两种画法，都不行：
  - 用前景 `▄` 画在默认背景上：在 owner 的终端里会被提亮。
  - 用反显 `▀`：会在行距里多出一道细线。

### 配色：只用 xterm-256 里的精确色

只用精确色，真彩终端和 256 色终端就会显示得完全一样。下表的映射已用 termenv v0.16.0 验证过：`termenv.ConvertToRGB(termenv.ANSI256.Color(hex)).Hex() == hex`。

| 字母 | 用途 | 颜色 | xterm-256 |
|---|---|---|---|
| `F` | 身体 | `#5fafd7` | 74 |
| `P` | 瞳孔、嘴 | `#080808` | 232 |
| `W` | 眼睛高光 | `#ffffff` | 231 |
| `A` | 琥珀色节点 | `#ffaf5f` | 215 |

- 预览初稿的瞳孔色 `#121212` 会被 termenv 量化成 232（`#080808`），不是精确色，所以计划改用 `#080808`。
- 在 16 色终端下，节点仍映射为亮红（`101`），与现状的 `#f5b041` 一致，不在本计划范围内。

### 绘制规则：身体一律用背景色

每个终端格对应上、下两个像素，共有四种格子：

| top | bottom | 输出 | 理由 |
|---|---|---|---|
| 透明 | 透明 | `" "` | — |
| 颜色 c | 同为 c | `Background(c).Render(" ")`，即背景为 c 的空格 | 背景色铺满整格（包括行距），且不会被提亮 |
| 颜色 t | 透明 | `Reverse(true).Foreground(t).Render("▄")`，即反显的下半块 | 单元格背景为 t，顶上的行距也是 t，和上一行连成一片；下半块用终端默认背景色画，看起来就是透明 |
| 颜色 t | 颜色 b（t≠b） | `Foreground(b).Background(t).Render("▄")` | 行距永远是上半格的颜色；前景叠在显式背景上，不会被提亮 |

「上透明、下有墨」不在表中：网格不变量保证它不会出现，并由测试强制。不要为它写兜底分支。

lipgloss v1.1.0 在 ANSI256 profile 下的实测输出：

- 背景格：`"\x1b[48;5;74m \x1b[0m"`
- 反显格：`"\x1b[7;38;5;74m▄\x1b[0m"`
- 双色格：`"\x1b[38;5;74;48;5;215m▄\x1b[0m"`

无颜色终端（`termenv.Ascii`）仍走现有的 `monoHalfBlock` 单色剪影。它的逻辑不变，只是改读新网格。

### 卡片宽度：随内容自适应，下限 78 列，上限只有终端宽度

把 `bannerMaxWidth` 改名为 `bannerMinWidth`，数值不变（78），语义从上限改为下限：内容不足时卡片仍是 78 列宽的卡片，不会窄成一条；长路径不再被压回 78。

**文案列的自然宽度**取以下三者中最大的：

- 名称与版本一行：`displayLineWidth(bannerProductName) + 2 + displayLineWidth(version)`；
- 路径：`displayLineWidth(dir)`；
- 两列快捷键一行：`2*itemW + bannerGutter + 1`，其中 `itemW` 是 `bannerShortcuts` 里 `key + " " + label` 的最大宽度。

**卡片宽度**的计算：

```go
framed := width >= bannerBorderMinWidth
mascot := width >= bannerMascotMinWidth
chrome := 0
if framed {
	chrome += 2 * (1 + bannerPadding)
}
if mascot {
	chrome += forebrainMascotWidth + bannerGutter
}
natural := bannerTextWidth(version, dir) + chrome
cardW := min(max(natural, bannerMinWidth), width)
textW := max(cardW-chrome, 1)
```

`textW` 从 `cardW` 推导，不由 `bannerTextWidth` 直接给出：下限生效时文案列被撑到 78 − 29 = 49 列，分隔线和右对齐的版本号仍然填满卡片内宽，与现状一致。

| 路径 | 文案列自然宽度 | 卡片 |
|---|---|---|
| `~/proj` | 32（快捷键最宽） | 78 列（下限生效） |
| owner 的 `~/workspace/unionj-cloud/forebrain-harness` | 42 | 78 列（下限生效） |
| 83 列的长路径 | 83 | 112 列 |

- 终端比卡片窄时，卡片等于终端宽度，路径在分隔符处换行，永不截断（沿用 `wrapBannerPath`）。
- `bannerMascotMinWidth`、`bannerBorderMinWidth` 改为按终端宽度判断，数值不变（60、32），只把注释从「narrowest card」改成「narrowest terminal」。

### 居中规则

吉祥物墨迹高度为 6 行（整行），文案列高度为 N 行。卡片内容区高 `rows = max(N, 6)`，两块各自居中：

```go
artTop := (rows - len(art) + 1) / 2
textTop := (rows - len(text) + 1) / 2
```

- 文案列至少有 6 行：名称、分隔线、目录、空行、两行快捷键。
- 宽度自适应之后，只要终端放得下，路径就不换行，所以通常 N = 6。此时 `artTop = textTop = 0`，两条中线完全重合。
- 当 N 与 6 的差为奇数时（例如终端窄、路径折成 7 行），终端只能按整行排版，差值没法平分。
  - 向上取整，让吉祥物下沉半行，文案相对略高。
  - 这样选是因为 owner 明确反感「文案偏低」。
  - 预览页「终端比路径窄」一图展示了这种情况。
- 这两行代码本身就是「两块各自居中」的定义，适用于任意高度关系，不是防御分支。

## 现状（执行前逐段核对）

- `pkg/tui/setup.go:1613-1652`：卡片说明注释、`forebrainMascot`（14 行 × 20 列，字母为 `O L F D W P A`）、`forebrainMascotPalette`（7 色）、`forebrainMascotWidth`。
- `pkg/tui/setup.go:1667-1680`：常量块。

  ```go
  const (
  	bannerProductName = "Forebrain Harness"
  	// bannerMaxWidth keeps the card a card on a wide terminal instead of a
  	// full-bleed band.
  	bannerMaxWidth = 78
  	// bannerMascotMinWidth is the narrowest card that still has room for
  	// the mascot beside a readable text column.
  	bannerMascotMinWidth = 60
  	// bannerBorderMinWidth is the narrowest card worth a frame; below it the
  	// text is printed bare.
  	bannerBorderMinWidth = 32
  	bannerGutter         = 3
  	bannerPadding        = 2
  )
  ```

  全仓库只有 `setup.go` 引用这三个宽度常量（`grep -rn 'bannerMaxWidth\|bannerMascotMinWidth\|bannerBorderMinWidth' --include='*.go' .` 只命中 1669-1677 行和 1709-1711 行）。
- `pkg/tui/setup.go:1700-1767`：`forebrainBannerLines`，分为以下几段。
  - 1709-1722 行，按 `cardW := min(width, bannerMaxWidth)` 推出 `framed`、`mascot`、`textW`。
  - 1728-1735 行，上文引用的居中代码。
  - 1741-1756 行，逐行拼出「吉祥物格 + 间距 + 文案」：

    ```go
    for i := 0; i < rows; i++ {
    	row := ""
    	if mascot {
    		cell := strings.Repeat(" ", forebrainMascotWidth)
    		if i < len(art) {
    			cell = art[i]
    		}
    		row = cell + strings.Repeat(" ", bannerGutter)
    	}
    	if t := i - offset; t >= 0 && t < len(text) {
    		row += text[t]
    	}
    ```

- `pkg/tui/setup.go:1793-1820`：`bannerShortcutRows`。它在函数内部循环 `bannerShortcuts` 求出 `itemW`，并在 `2*itemW+bannerGutter+1 <= width` 时排成两列。
- `pkg/tui/setup.go:1884-1941`：三个绘制函数。
  - `forebrainMascotRows`：profile 为 Ascii 时走 `monoHalfBlock`，否则走 `colorHalfBlock`。
  - `colorHalfBlock`：现在用前景字符 `▀`、`▄`、`█`。
  - `monoHalfBlock`：单色剪影，墨迹 = 非 `.`、非 `W`、非 `P`。
- `pkg/tui/chat_session_test.go` 有两处断言旧网格的单色头顶行 `"▄▄███▄██▄▄██▄███▄▄"`：
  - `TestRendererBannerDefaultsToForebrain`（15947 行起），断言在 15954 行；
  - `TestRendererViewportSeedsStartupChromeAsBannerBlock`（17061 行起），断言在 17106 行。
- `TestRendererBannerKeepsFullWorkingDirectory`（15971 行起）用一条 83 列的路径，在宽度 120/80/50/24 下断言两件事：每行不超宽；去掉框线后路径完整。新逻辑下它应当照常通过，不需要修改。
- 调用路径有两条，都只调 `forebrainBannerLines`：
  - `pkg/tui/render.go:603` 的 `Renderer.Banner`：非 viewport 模式下直接打印；viewport 模式下作为 `FrameBannerInfo` 块追加。
  - `pkg/tui/reducer.go:2497-2502`：每次重绘时按当前宽度重新排版。终端尺寸变化时，卡片会自动重排。
- viewport 绘制会保留 SGR 序列：`paintKeepsSequence`（`reducer.go:3513`）对以 `m` 结尾的序列放行，所以背景色和反显（SGR 7）都能原样到达终端。
- 测试里强制颜色 profile 的写法，以 `pkg/tui/render_test.go:426-429` 为准：

  ```go
  previous := lipgloss.ColorProfile()
  lipgloss.SetColorProfile(termenv.ANSI256)
  t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
  ```

  `chat_session_test.go` 已 import `lipgloss`、`termenv`、`regexp`、`strings`，并已有 `stripANSI`、`displayLineWidth` 两个辅助函数。

## 命令

| 用途 | 命令 | 成功时 |
|---|---|---|
| 编译 | `go build -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| vet | `go vet ./pkg/tui/` | exit 0，无输出 |
| 相关测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Banner\|Mascot' -count=1` | `ok` |
| TUI 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok` |
| 真机驱动 | `.claude/skills/run-forebrain/driver.sh`（先读 `.claude/skills/run-forebrain/SKILL.md`） | 见步骤 7 |

## 范围

**只改这两个文件：**

- `pkg/tui/setup.go`：网格、调色板、`colorHalfBlock`、`bannerMaxWidth` 改名为 `bannerMinWidth`（下限 78）、新增文案列自然宽度的计算、`forebrainBannerLines` 的宽度与居中逻辑，以及相关注释。
- `pkg/tui/chat_session_test.go`：更新两处旧断言，新增步骤 5 的测试。

**不要碰：**

- `bannerTextColumn` 的内容和顺序，`bannerShortcuts` 的条目，`bannerGutter`、`bannerPadding`、`bannerMascotMinWidth`、`bannerBorderMinWidth` 的数值。
- `forebrainLogoStyle`、`bannerBorderStyle` 等文字样式。
- `monoHalfBlock` 和 `wrapBannerPath` 的逻辑。
- `pkg/tui/render.go`、`pkg/tui/reducer.go` 和 web 前端，因为调用路径不变。
- 任何进入模型请求的内容。启动卡片只在 UI 上显示，不进 transcript，也不进 prompt 前缀。

## 步骤

### 步骤 1：替换网格和调色板

在 `pkg/tui/setup.go` 中：

1. 把 `forebrainMascot`（1621-1636 行）替换成「设计」一节的 12 行网格，保持 `[...]string` 声明。
2. 把 `forebrainMascotPalette`（1641-1649 行）替换成下面四项，每项注释写明用途和 xterm-256 编号：

   ```go
   'F': "#5fafd7", // body (xterm 74)
   'P': "#080808", // pupils, mouth (xterm 232)
   'W': "#ffffff", // eye shine (xterm 231)
   'A': "#ffaf5f", // harness node (xterm 215)
   ```

3. 改写两处注释（英文，风格贴合周边代码）：
   - `forebrainMascot` 上方的注释说明四点：它是「戴着 harness 节点的小脑袋」的纯色剪影；每两个像素行画成一个终端行；行数为偶数，且首尾行都有墨；没有上半格透明、下半格有墨的格子。
   - `forebrainMascotPalette` 上方的注释说明：颜色都是 xterm-256 的精确色，256 色终端与真彩终端显示一致。

**验证**：`go build ./pkg/tui/` → exit 0

### 步骤 2：重写 `colorHalfBlock`，让身体一律用背景色

把 `colorHalfBlock`（1910-1925 行）改成「绘制规则」表中的四种情况：

```go
func colorHalfBlock(top, bottom byte) string {
	tc, topOn := forebrainMascotPalette[top]
	bc, bottomOn := forebrainMascotPalette[bottom]
	switch {
	case !topOn && !bottomOn:
		return " "
	case top == bottom:
		return lipgloss.NewStyle().Background(tc).Render(" ")
	case !bottomOn:
		return lipgloss.NewStyle().Reverse(true).Foreground(tc).Render("▄")
	default:
		return lipgloss.NewStyle().Foreground(bc).Background(tc).Render("▄")
	}
}
```

同时改写 `forebrainMascotRows` 上方的注释（1884-1887 行），说明三件事：

- 为什么身体用背景色：方块字符不一定能画满带行距的行，前景色在部分终端会被提亮，而背景色两种问题都没有。
- 为什么只用下半块，并让背景色取上半格的颜色。
- 为什么网格里没有「上半格透明、下半格有墨」的格子。

**验证**：`go build ./pkg/tui/` → exit 0。`grep -n '"▀"\|"█"' pkg/tui/setup.go` 的命中只出现在 `monoHalfBlock` 内。

### 步骤 3：卡片宽度随内容自适应

1. 把常量 `bannerMaxWidth` 改名为 `bannerMinWidth`，数值仍为 78，注释改为下限语义，例如「bannerMinWidth keeps the card a card when the content is narrow; long paths grow past it」。把 `bannerMascotMinWidth`、`bannerBorderMinWidth` 的注释改为按终端宽度表述：
   - 前者是「the narrowest terminal that still has room for the mascot beside a readable text column」；
   - 后者是「the narrowest terminal worth a frame」。
2. 新增 `bannerShortcutItemWidth() int`：把 `bannerShortcutRows` 里求 `itemW` 的循环搬过来，返回最大的 `len(key)+1+len(label)`。`bannerShortcutRows` 改为调用它，行为不变。
3. 新增 `bannerTextWidth(version, dir string) int`，返回文案列的自然宽度：

   ```go
   max(displayLineWidth(bannerProductName)+2+displayLineWidth(version),
   	displayLineWidth(dir),
   	2*bannerShortcutItemWidth()+bannerGutter+1)
   ```

   注释说明：这是卡片自然宽度的来源，路径越长卡片越宽；实际卡片宽度还要先过 `bannerMinWidth` 下限，再被终端宽度封顶。
4. 把 `forebrainBannerLines` 的 1709-1722 行，从 `cardW := min(width, bannerMaxWidth)` 开始，改为「设计 / 卡片宽度」一节的代码：先用 `width` 判断 `framed` 和 `mascot`，再算 `chrome`、`natural`、`cardW`、`textW`。后面所有用到 `cardW` 的地方（边框、`bannerFramedRow`）保持不变。
5. 更新 `forebrainBannerLines` 的函数注释，以及 1613-1616 行卡片说明注释里的宽度表述：卡片宽度不小于 `bannerMinWidth`（78），内容更宽时随内容增长，只有终端放不下时路径才在分隔符处换行。

**验证**：`go build ./pkg/tui/` → exit 0。`grep -rn 'bannerMaxWidth' --include='*.go' .` → 无命中。

### 步骤 4：两块各自居中

在 `forebrainBannerLines` 中：

1. 把 1728-1735 行的 `offset` 计算换成：

   ```go
   rows := max(len(text), len(art))
   artTop := (rows - len(art) + 1) / 2
   textTop := (rows - len(text) + 1) / 2
   ```

   并在上方加注释，说明两点：
   - 吉祥物网格首尾行都有墨，所以按行数居中就等于按墨迹居中；
   - 差为奇数时向上取整，吉祥物下沉半行，文案不会偏低。
2. 循环体里，吉祥物格改为：

   ```go
   if a := i - artTop; a >= 0 && a < len(art) {
   	cell = art[a]
   }
   ```

   文案改为 `if t := i - textTop; ...`。
3. 删除旧注释「the mascot's last row is half clear, so round the offset up」。

**验证**：`go vet ./pkg/tui/` → 无输出

### 步骤 5：测试

以下改动都在 `pkg/tui/chat_session_test.go` 中。

**5a. 更新旧断言。** 15954 行和 17106 行（两处）的 `"▄▄███▄██▄▄██▄███▄▄"` 改为新网格的单色头顶行 `"███████  ███████"`，并同步修改相关注释的措辞。

**5b. 新增测试**，放在 `TestRendererBannerKeepsFullWorkingDirectory` 之后：

1. `TestForebrainMascotSitsOnWholeRows`：对 `forebrainMascot` 断言以下几点：
   - `len % 2 == 0`；
   - 首行和末行都含非 `.` 字符；
   - 每行长度等于 `forebrainMascotWidth`；
   - 每个非 `.` 字母都在 `forebrainMascotPalette` 中；
   - 对每个偶数 `y`、每个 `x`，不存在 `g[y][x] == '.' && g[y+1][x] != '.'`。失败信息写出坐标，并说明「头顶轮廓必须对齐整行」。
2. `TestForebrainMascotPaletteIsExactXterm256`：遍历 `forebrainMascotPalette`，断言 `termenv.ConvertToRGB(termenv.ANSI256.Color(string(c))).Hex() == string(c)`。
3. `TestForebrainMascotPaintsBodyAsBackground`：按「现状」中的写法强制 `termenv.ANSI256` 并在 cleanup 中恢复，然后取 `rows := forebrainMascotRows()`，断言：
   - `len(rows) == 6`；
   - 每行经 `stripANSI` 后只含 `' '` 和 `'▄'`；
   - `rows[0]` 含 `"\x1b[48;5;74m \x1b[0m"`；
   - 用 `regexp.MustCompile("\x1b\\[([0-9;]*)m(.)\x1b\\[0m")` 找出每个上色的格子：字形为 `" "` 时，参数必须以 `48;5;` 开头；字形为 `"▄"` 时，参数必须以 `7;38;5;` 开头，或者同时含 `38;5;` 和 `;48;5;`。失败信息说明「前景色不能单独画在默认背景上」。
4. `TestForebrainBannerWidthFollowsPath`：强制 `termenv.Ascii`。卡片的顶边是 `lines[1]`（`lines[0]` 是开头的空行），卡片宽度 = `displayLineWidth(lines[1])`。令 `chrome := 2*(1+bannerPadding) + forebrainMascotWidth + bannerGutter`，即 29。逐项断言：
   - `width=200`、`dir="~/proj"`：卡片宽 = `bannerMinWidth`，即 78（自然宽度 61 不足，下限生效）。
   - `width=200`、`dir` 为 `TestRendererBannerKeepsFullWorkingDirectory` 里那条 83 列路径：卡片宽 = `chrome + 83`，并且存在某一行 `stripANSI` 后包含完整路径（没有换行）。
   - `width=90`、同一条长路径：卡片宽 = 90，并且没有任何一行包含完整路径（已在分隔符处换行）。
   - `width=70`、`dir="~/proj"`：卡片宽 = 70。终端比下限还窄时终端宽度优先，卡片不超出终端。
5. `TestForebrainBannerCentresMascotOnText`：强制 `termenv.Ascii`。这时只有吉祥物里有块字符：边框是 `╭─╮│╰╯`，分隔线是 `─`。
   - 对 `width ∈ {200, 90, 70, 60}` × `dir ∈ {"~/workspace/forebrain", 上面那条 83 列路径}`，调用 `forebrainBannerLines("v0.1.0", dir, width)`。
   - 在结果中找出四个行号：
     - `textTop`：含 `"Forebrain Harness"` 的行；
     - `textBottom`：含 `"ctrl+j"` 的行；
     - `artTop`：第一条含 `█`、`▀` 或 `▄` 的行；
     - `artBottom`：最后一条含这三个字符之一的行。
   - 断言：
     - `artBottom - artTop + 1 == 6`；
     - `above := artTop - textTop`、`below := textBottom - artBottom` 都 ≥ 0，且 `above - below` ∈ {0, 1}；
     - `width == 200` 时，两种路径都满足 `above == 0 && below == 0`，因为宽度自适应后路径不换行，文案正好 6 行。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Banner|Mascot' -count=1` → `ok`

接着做两次反向检查，确认新测试能抓到 owner 报告的问题：

- 临时把步骤 4 改回旧的 `offset` 逻辑，确认 `TestForebrainBannerCentresMascotOnText` 失败。
- 临时恢复 `cardW := min(width, 78)`，确认 `TestForebrainBannerWidthFollowsPath` 失败（长路径被压回 78）。
- 临时去掉下限（`cardW := min(natural, width)`），确认 `TestForebrainBannerWidthFollowsPath` 也失败（`~/proj` 时卡片变 61，不再等于 78）。

两次都确认后恢复原样，不要留下任何临时改动。

### 步骤 6：全量回归

**验证**：`go vet ./pkg/tui/` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`

### 步骤 7：真机验证

owner 要求界面类修复必须在运行中的 TUI 里实证，不能只靠单测。

1. 编译：`go build -o ./build/bin/forebrain ./cmd/forebrain`。
2. 用 run-forebrain 驱动：

   ```bash
   D=.claude/skills/run-forebrain/driver.sh
   $D start hang
   $D wait 'Forebrain Harness' 25
   ```

   tmux 会话名为 `forebrain-run`，大小 120×40。
3. 抓带转义序列的屏幕：`tmux capture-pane -t forebrain-run -p -e > /tmp/banner.ansi`；再用 `$D screen` 看纯文本。逐项核对：
   - 含 `Forebrain Harness` 的那一行，同时是吉祥物的第一行。这一行应含 `48;5;74`，或真彩形式的 `48;2;95;175;215`。
   - 含 `ctrl+j newline` 的那一行，同时是吉祥物的最后一行（两只脚）。
   - 吉祥物区域的纯文本只含空格和 `▄`。
   - 卡片顶边宽度 = `max(bannerMinWidth, 29 + 文案列自然宽度)`：owner 的路径自然宽度 42，卡片为 78（下限生效）；换成更长的路径再抓一次屏，卡片超过 78，右边框跟着变宽，路径永不因卡片被折行。
4. 把 tmux 窗口缩窄到比路径还窄（`tmux resize-window -t forebrain-run -x 70`），再抓一次屏，确认：路径在分隔符处换行，卡片不超出终端宽度，吉祥物仍按「居中规则」居中。
5. `$D stop`。
6. 截图证据：
   - 先尝试在 macOS Terminal 中自动截图：
     - 用 `osascript -e 'tell application "Terminal" to do script "<repo>/build/bin/forebrain"'` 开一个新窗口；
     - 用 `osascript -e 'tell application "Terminal" to id of front window'` 取窗口 id；
     - 几秒后用 `screencapture -x -l <窗口 id>` 截该窗口。
   - 如果系统不允许截屏（没有屏幕录制权限），在报告里写明，并请 owner 在自己的 Terminal 里启动新二进制后截图确认。
   - 截图里应当看到：吉祥物是无横缝的纯色剪影；卡片宽度贴合路径；吉祥物与文案上下居中。

## 完成标准（必须全部满足）

- [ ] `go vet ./pkg/tui/` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`，五个新测试都存在并通过
- [ ] `grep -rn 'bannerMaxWidth' --include='*.go' .` 无命中
- [ ] `grep -n '▄▄███▄██▄▄██▄███▄▄' pkg/tui/` 无命中
- [ ] `grep -n "'O'\|'L'\|'D'" pkg/tui/setup.go` 无命中（旧的轮廓、高光、褶皱色已删除；改动前它只命中调色板的 1642、1644、1645 行）
- [ ] `git status --short` 只列出 `pkg/tui/setup.go`、`pkg/tui/chat_session_test.go` 和本计划文件
- [ ] 步骤 7 的 tmux 核对全部成立，并附 Terminal 截图（或注明为什么请 owner 截图）
- [ ] 没有任何提交：`git log -1` 仍是执行前的 HEAD

## STOP 条件

出现以下任一情况，停下来报告：

- 「现状」里的代码摘录或行号范围与实际代码对不上。
- lipgloss 的实际输出和「绘制规则」里的三个转义串不同。例如 Reverse 的参数顺序不同，导致步骤 5 的正则需要改写。这时先报告实际输出，不要放宽断言了事。
- 文案列在某个宽度下少于 6 行。这会推翻「默认情况完全对齐」的前提。
- 真机抓屏里，吉祥物行的 SGR 被改写或丢失（例如反显 `7` 不见了）。这说明 viewport 绘制链对 SGR 有过滤，需要先查根因，不要绕开。
- 除 `forebrainBannerLines` 外，还有别的代码依赖卡片宽度不超过 78 列（旧上限），或依赖吉祥物为 7 行。例如某个测试或别处用 `bannerMaxWidth` 算布局。
- 修复看起来需要改动「范围」以外的文件。

## 维护说明

以下规则在以后修改启动卡片时都要遵守：

- **网格不变量**：偶数行、首尾行有墨、头顶对齐整行，由 `TestForebrainMascotSitsOnWholeRows` 守护。重画吉祥物时，必须同时守住这些不变量和「两块各自居中」规则，居中才精确，也才没有横缝。
- **调色板**：只能用 xterm-256 的精确色，由 `TestForebrainMascotPaletteIsExactXterm256` 守护。新增颜色前，先用 termenv 验证映射。
- **绘制规则**：身体一律用背景色；前景只能叠在显式背景上，或用反显画「下半格透明」。以后在卡片里加其他像素图，同样适用。
- **宽度**：卡片宽度 = max(`bannerMinWidth` 78, 内容自然宽度)，上限只有终端宽度。「没有固定上限」和「下限 78」都是 owner 的长期要求：不得再引入固定上限，也不得改动下限。将来往文案列加新行时，要把新行的宽度纳入 `bannerTextWidth`。
- **居中**：启动卡片里的吉祥物和文案必须垂直居中，这也是 owner 的长期要求。行数差为奇数时，吉祥物下沉半行，文案不得偏低。
- **Prompt cache**：启动卡片只是 UI 输出，不进 transcript，也不进模型请求，对缓存命中率没有影响。
- **16 色终端**：节点映射为亮红，与改动前一致。如需改成黄色，另开计划。

## 验收记录

**2026-10-05，执行者 general-purpose 子代理（步骤 1–6）+ 审查者复验（步骤 7 及全部完成标准）。**

- 编译：`go build -o ./build/bin/forebrain ./cmd/forebrain` → exit 0。
- vet：`go vet ./pkg/tui/` → exit 0 无输出；`gofmt -l` 两文件无输出。
- 聚焦测试：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Banner|Mascot' -count=1` → `ok`，8 个测试全 PASS，含 5 个新测试（SitsOnWholeRows / PaletteIsExactXterm256 / PaintsBodyAsBackground / WidthFollowsPath / CentresMascotOnText）。
- 全量：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（40.4s）。
- 反向检查：临时恢复旧居中 → CentresMascotOnText FAIL；临时 `min(width, 78)` → WidthFollowsPath FAIL（长路径被压回 78）；临时去下限 → WidthFollowsPath FAIL（`~/proj` 卡片变 61）。均已恢复，`grep TEMP-REVERSE-CHECK` 无命中。
- grep 验收：`bannerMaxWidth`、旧头顶断言、`'O'/'L'/'D'` 均无命中。
- 真机 tmux 120×40（版本串 `v0.1.2-0.20261005142755-936df40fba17+dirty`，证明为新构建）：卡片宽 96 = 29 chrome + 67 路径，公式 `min(max(natural,78),终端)` 生效（未触下限与上限）；名称行 = 吉祥物首行，SGR 为真彩 `48;2;95;175;215` 背景铺满，吉祥物区纯文本只有空格与 `▄`；`ctrl+j newline` 行 = 吉祥物末行（双脚）；吉祥物 6 行与文案 6 行同跨（above=below=0）。
- 真机 70 列：路径在分隔符处折成 3 行、完整不截断；卡片 68 ≤ 70 不超终端（viewport 布局宽为 pane−2，滚动条预留，既有行为；80 列下卡片恰好 78，与公式吻合）；吉祥物 6 行（5–10）对文案 9 行（3–11），above=2/below=1，奇数差吉祥物下沉半行。
- 截图：`screencapture -x -l` 三次被系统拒绝（`could not create image from window`）。三条宿主链都试过：本会话宿主（forebrain vendor 二进制，`TERM_PROGRAM` 为空）、经 `osascript do shell script` 的 Terminal.app 链——均无「屏幕录制」权限，macOS TCC 放行与否只能由 owner 授权。回退结论：tmux 的 SGR 级抓屏（上两条）已逐字节证明渲染正确；「视觉上无横缝」请 owner 在自己的 Terminal 里运行 `./build/bin/forebrain` 截图确认（或给 Terminal 授予屏幕录制权限后由执行者重试）。
- 例外说明：完成标准「git log -1 仍是执行前 HEAD」被 owner 的并行提交 `936df40` 打破（该提交在执行者会话中途把执行者已完成并写入工作区的步骤 1 改动 setup.go 55 行一并带入；reflog 证明执行者无任何 git 写操作）。计划笔误更正：测试用长路径实测 84 列（计划写 83），测试断言按实际值书写并通过。
- `git status --short`：`M pkg/tui/chat_session_test.go`、`M pkg/tui/setup.go`、`?? docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md`（另有既有的 `?? plans/`）。无任何提交。
