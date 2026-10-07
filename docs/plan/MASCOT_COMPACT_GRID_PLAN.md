# 计划：TUI 吉祥物缩小——方案 A 重画为 14×8 网格（28 列 × 8 行）

> 本仓库禁止执行者提交代码：改动留在工作区，由 owner 审阅后手动提交。

## 状态

- 类别：owner 已拍板的设计变更
- 状态：已实施（2026-10-07），改动未提交
- **网格与吉祥物显示门槛已被 `docs/plan/TUI_STARTUP_CARD_DESIGN_SHEET_PLAN.md` 取代（2026-10-07）：启动卡片按 owner 的 HTML 设计稿实现，网格改为 10×6（20 列 × 6 行），门槛改为 58，边框与名称下分隔线改为虚线。本计划的绘制契约（只用背景色、一个像素占 2 列 × 1 行）保留**
- 关系：**取代** `docs/plan/MASCOT_WHOLE_CELL_PIXELS_PLAN.md` 中的 20×12 网格（40 列 × 12 行）和吉祥物显示门槛 78 列。该计划的绘制契约保留：只用背景色，一个像素占 2 列 × 1 行。配色、居中规则、版本号折行、resize 先 `2J` 也都保留。

## 1. 现象

owner 截图：启动卡片里的吉祥物占 40 列 × 12 行，比右侧 6 行文案高出一倍，显得太大。

## 2. 为什么只能重画网格

只用背景色时，一个终端格只能表达一个像素。终端格高约为宽的两倍，所以方形像素必须占 2 列 × 1 行。20×12 的网格因此最小就是 40×12。要缩小，只有两条路：

1. 降低网格分辨率，绘制方式不变，仍然无缝；
2. 改回半块字形，网格不变，变成 20 列 × 6 行。但 Terminal.app 在 Fira Code 14pt 下字形盖不满格子，竖线会回来（实测见 `MASCOT_WHOLE_CELL_PIXELS_PLAN.md` §2.2）。

## 3. owner 裁决（2026-10-07）

候选：14×8（28×8）、16×9（32×9）和半块字形 20×6。owner 选择 **14×8**：

```
.FFFFF..FFFFF.
FFFFFFAAFFFFFF
FFWPFFFFFFWPFF
FFPPFFFFFFPPFF
FFFFFPFFPFFFFF
.FFFFFPPFFFFF.
.FFFFFFFFFFFF.
..FFFF..FFFF..
```

保留了双瓣头顶、中缝下 1 像素高的琥珀节点、带高光的 2×2 眼睛、上扬的嘴角和两只脚，去掉了下巴台阶，身体边距也变窄了。眼睛与嘴角之间隔一列，所以嘴不会和眼睛连成一片。面积约为原来的 47%，高度比文案多 2 行。

## 4. 实现

`pkg/tui/setup.go`：

- `forebrainMascot` 换成上面的网格。`forebrainMascotWidth` 自动变为 28。
- `bannerMascotMinWidth` 从 78 改为 66。推导规则沿用原注释：边框和内边距 6 列、吉祥物 28 列、间距 3 列，再给文案留 29 列。因此 66–77 列的终端现在也会显示吉祥物（文案列较窄，快捷键改为每行一个）。

`pkg/tui/chat_session_test.go`：

- `TestForebrainMascotIsTheApprovedDesign`：锁定新网格。
- `TestForebrainMascotPaintsBodyAsBackground`：眼睛高光行改为在网格里查找含 `W` 的行，不再写死行号。
- `TestForebrainBannerWidthFollowsPath`：吉祥物变窄后，最短文案的卡片（6+28+3+32=69 列）低于 78 列下限。所以断言改为：卡片停在 `bannerMinWidth`，并先断言自然宽度确实低于下限。
- `TestRendererBannerDefaultsToForebrain` 与 `TestRendererViewportSeedsStartupChromeAsBannerBlock` 的冠顶串改为 `██████████    ██████████`。

## 5. 验证

- `go vet ./pkg/tui/` 干净；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain ./pkg/architecture -count=1` 全绿。
- 用 run-forebrain driver 启动（tmux，独立目录/会话/端口），抓带颜色的屏幕：
  - 120 列：吉祥物 28×8，与文案垂直居中；
  - 80 列与 70 列：显示吉祥物，长路径和 dev 版本号都在卡片内折行；
  - 60 列：不画吉祥物。

## 6. 维护说明

- 网格由 `TestForebrainMascotIsTheApprovedDesign` 锁死，改网格须先经 owner 拍板。
- 绘制契约不变：只用背景色，不用任何字形；一个像素 = `forebrainMascotPixelCols` 列 × 1 行。
- `bannerMascotMinWidth` 必须 ≤ 78，并随吉祥物宽度按「6 + 吉祥物宽 + 3 + 29」重算。
