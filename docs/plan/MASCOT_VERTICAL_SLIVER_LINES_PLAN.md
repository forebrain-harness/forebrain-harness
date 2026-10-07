# 计划：TUI 吉祥物竖线根因修复——网格带对齐，渲染去字形化（纯背景色）

> 执行者须知：按顺序执行，每步先跑该步的验证命令。出现 STOP 条件立即停下报告。
> 本仓库禁止执行者提交代码：改动留在工作区，由 owner 审阅后手动提交。
> 计划文件本身禁止 commit。

## 状态

- 类别：bug（视觉缺陷），既有已批准计划 TUI_BANNER_MASCOT_REDRAW_PLAN.md 实施后的回归修正
- 工作量：S；风险：LOW（只改启动卡片吉祥物的数据与绘制，不碰布局/宽度/居中/模型请求）
- 计划基线：工作区 @ commit `936df40f` + 未提交改动（TUI_BANNER_MASCOT_REDRAW_PLAN 的实施成果）。文内 file:line 均指当前工作区状态
- 关系：**取代** `docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md` 的「绘制规则」一节；该计划的宽度自适应、居中规则、配色不变量全部保留且不受影响
- **已被 `docs/plan/MASCOT_WHOLE_CELL_PIXELS_PLAN.md` 部分取代（2026-10-07）**：owner 判定带对齐网格与设计稿不一致，网格回滚为方案 A（恢复眼睛高光、弧线嘴、下巴台阶、1 像素节点），改为每像素 2 列 × 1 行；本计划的「纯背景色、不用字形」保留。第 8 节「像素画必须 band-uniform」的契约作废，由新计划第 7 节取代；第 5 步第 7 项的 `/quit` 退出不存在，应为两次 Ctrl+C

## 1. 目标（Why this matters）

owner 截图：启动卡片吉祥物多处下边缘伸出细竖线（橙色补丁下方、两眼下方、嘴下方、底部轮廓左/中/右三处），颜色与上方特征一致，很丑。要求定位根因并删除竖线。

## 2. 根因（file:line 证据链）

吉祥物是 12 像素行网格，每两个像素行合成 1 个终端行（`pkg/tui/setup.go:1627-1640` 数据，`:1918-1938` forebrainMascotRows）。

`colorHalfBlock`（`pkg/tui/setup.go:1940-1953`）把"上下两像素颜色不同"的格子画成 `▄` 字形：`Foreground(下像素).Background(上像素)`；"上实下空"画成 `Reverse(true).Foreground(上像素)` 的 `▄`（`:1949`）。

**竖线的机制**：`▄` 是字体字形，依赖字体把它画满整个单元格。已批准的前计划在同owner终端（macOS Terminal，ANSI256）里实测过块状字形不铺满格子——高度方向只盖 34/40px（见 `docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md` 根因节）。本次截图证明宽度方向同样不铺满：`▄` 右缘留出 1–2px 未覆盖竖条，露出的是**单元格背景色 = 上像素的颜色**，高度恰为字形下半格 = 1 像素行。逐一对上截图：

| 截图竖线 | 对应双色 cell | 露出色 |
|---|---|---|
| 橙补丁下方 | band(2,3) cols 9,10：A/F（`setup.go:1630,1633` 附近） | 橙 |
| 两眼下方 | band(6,7) cols 5,6,13,14：P/F | 黑 |
| 嘴下方 | band(8,9) cols 9,10：P/F | 黑 |
| 底边左/中/右 | band(10,11) cols 2,3,9,10,16,17：F/.（反显 ▄） | 蓝 |

每条线都在特征右缘、1 像素行高——与截图完全吻合。而实心身体（全背景色空格 cell，`:1947` 分支）在截图里渲染完美：背景色铺满整格、不受字体影响。

**结论**：缺陷类 = 「双色 cell 依赖字形铺满格宽」。修法 = 让网格不再产生双色 cell（每带两行逐列一致），渲染退化为纯背景色空格，删除 `▄`/`Reverse` 分支。竖线在构造上不可能出现，与终端/字体无关。

## 3. 修复设计

### 3.1 新网格（`setup.go:1627-1640` 整体替换；20 列 × 12 像素行不变，仍渲染 6 个终端行）

每两行为一带，带内逐列一致（band-uniform）：

```
..FFFFFFF..FFFFFFF..
..FFFFFFF..FFFFFFF..    耳
.FFFFFFFFAAFFFFFFFF.
.FFFFFFFFAAFFFFFFFF.    缰绳节点（1px→2px 高）
FFFFFFFFFFFFFFFFFFFF
FFFFFFFFFFFFFFFFFFFF    额头
FFFFFPPFFFFFFPPFFFFF
FFFFFPPFFFFFFPPFFFFF    眼（实心 2×2）
.FFFFFFFFPPPPFFFFFFF.
.FFFFFFFFPPPPFFFFFFF.    嘴（平 4 宽）
....FFFFF..FFFFF....
....FFFFF..FFFFF....    脚
```

每行均 20 字符；首末带都有墨（居中规则依赖，不变量保留）。

**刻意修订（美术取舍，owner 审批本计划即认可）**：
- 节点 1px→2px 高（1px 特征必然跨带，无法带对齐）；
- 眼白高光（W）删除：1px 角部高光无法带对齐；备选的「眼左白右黑竖条」比无高光更怪，取实心眼；
- 嘴从弧线笑改平 4 宽（弧线 = 角高光同类问题）；
- 下巴台阶段（`..FFFFFFFFFFFFFFFF..` 行，正是反显 ▄ 竖线所在）删除，下颌直接接脚。
- W 从 `forebrainMascotPalette`（`:1645-1650`）删除（网格不再使用，不留死数据）。

### 3.2 渲染去字形化（替换 `setup.go:1906-1969` 的注释、forebrainMascotRows、colorHalfBlock、monoHalfBlock）

- `forebrainMascotRows`：逐带取 `forebrainMascot[y]`，逐列画一个 cell（带内一致性由测试强制，不读 `[y+1]`、不写兜底分支）。
- `colorCell(p)`：palette 命中 → `Background(c).Render(" ")`；`'.'` → `" "`。无字形、无反显。
- `monoCell(p)`：`p=='.' || p=='P'` → `" "`，否则 `"█"`（W 已删；脸仍镂空）。
- 注释改写：说明带对齐 + 纯背景色是唯一在所有终端（含块字形不铺满格的字体回退）都无缝的画法。
- 不新增任何防御分支（不可达状态不写代码——带一致性由测试锁死）。

### 3.3 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 彩色终端吉祥物 | ▄/反显字形；块字形不铺满格宽的终端出现竖线 | 纯背景色；任何终端无竖线（刻意修订） |
| 脸部细节 | 眼白高光、弧线嘴、下巴台阶 | 实心眼、平嘴、无下巴台阶（刻意修订） |
| 无色终端 | ▀▄█ 剪影 | █/空格 剪影（语义不变） |
| 卡片宽度/居中/边框/文案 | — | 不变（不触碰） |

### 3.4 测试修订（`pkg/tui/chat_session_test.go`）

- `TestForebrainMascotSitsOnWholeRows`（:15994）：把单向 pair 检查（:16027-16033，只禁"上空下实"）升级为 **band-uniform 强断言**：`forebrainMascot[y][x] != forebrainMascot[y+1][x]` 即 Fatal——本 bug 的回归护栏。更新函数 doc 注释。
- `TestForebrainMascotPaintsBodyAsBackground`（:16048）：现契约**要求** ▄（:16063-16064、:16080-16083）。改为**禁止**任何字形：剥 ANSI 后只允许 `' '`；cell 正则匹配到非空格字符即 Fatal；保留 `"\x1b[48;5;74m \x1b[0m"` 断言。更新 doc 注释。
- `TestForebrainMascotPaletteIsExactXterm256`（:16036）：删 W 后自动适配，无改动。

## 4. Scope

**In scope**：`pkg/tui/setup.go`（网格/注释/forebrainMascotRows/colorCell/monoCell/palette 删 W）、`pkg/tui/chat_session_test.go`（上述两个测试）、计划文档。
**Out of scope（明确不碰）**：`forebrainBannerLines`/宽度/居中/边框逻辑（`setup.go:1706-1767`）；`forebrainLogoStyle` 等 banner 样式；mono 分支语义；gateway/web（吉祥物仅 TUI，全仓库 `▄`/`Reverse(true)` 仅 setup.go 使用，已穷举）；`pkg/tui` 其余文件。

## 5. Steps（每步带验证）

0. 批准后：本计划拷贝为 `docs/plan/MASCOT_VERTICAL_SLIVER_LINES_PLAN.md`（工作区文件，不 commit）；在 `docs/plan/TUI_BANNER_MASCOT_REDRAW_PLAN.md` 状态节加一行「绘制规则已被 MASCOT_VERTICAL_SLIVER_LINES_PLAN 取代（2026-10-06，竖线回归）」。
1. 改 `pkg/tui/setup.go`（3.1 + 3.2）。
2. 改 `pkg/tui/chat_session_test.go`（3.4）。
3. 编译：`CGO_ENABLED=1 go build ./...` → 无输出退出 0。
4. 定点测试：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestForebrainMascot|TestForebrainBanner|TestRendererBanner' -count=1 -v` → 全 PASS。
5. 包回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain -count=1` → 全绿（基线：当前工作区这些包为绿；若出现既有失败，按"失败集合逐项相同"判读并须根因修复）。
6. `go vet ./...` → 干净。
7. 真机验收（tmux，隔离环境，不污染真实 `~/.forebrain*`）：
   ```bash
   go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
   tmux new-session -d -s fbt -x 120 -y 42 \
     'cd /tmp/fbt-accept && FOREBRAIN_HOME=/tmp/fbt-home ./build/bin/forebrain; echo EXIT_CODE=$?; sleep 300'
   tmux capture-pane -t fbt -e -p
   ```
   - 字节断言：banner 吉祥物各行不含 `▄`/`▀`/`█`（彩色路径），不含 `[7;`（反显）；cell 形如 `\x1b[48;5;74m \x1b[0m`；
   - 目视断言（`tmux capture-pane -t fbt -p` + 截图）：吉祥物无任何竖线，脸可读（耳/节点/眼/嘴/脚）；
   - 回归：composer 可输入，`/quit` 退出 EXIT_CODE=0；
   - 清理：`tmux kill-session -t fbt`、删临时目录。

## 6. Done criteria（机器可查）

- `grep -n '▄\|▀\|Reverse(true)' pkg/tui/setup.go` → 无匹配；
- 步骤 3-6 全部命令通过；
- capture-pane 字节断言通过；截图无竖线；
- `git status` 改动仅：`pkg/tui/setup.go`、`pkg/tui/chat_session_test.go`、`docs/plan/` 两个计划文件；未 commit。

## 7. STOP conditions

- 实施中发现竖线机制判断有误（如纯背景 cell 也出现竖线/横缝）→ 停，重新取证，不得升级为防御代码。
- 步骤 5 出现基线外失败且无法归因本次改动 → 停下报告。
- 真机发现带对齐后吉祥物不可读/严重走样 → 停，交 owner 拍板美术方案。
- 工作区 `pkg/tui` 现状与第 2 节 file:line 摘录不符（他人在计划后改过）→ 停。

## 8. Maintenance notes

- 「像素画必须 band-uniform + 纯背景色渲染」自此成为本仓库契约，由 TestForebrainMascotSitsOnWholeRows 的强断言把守；未来新增像素画/改网格必须维持该不变量，禁止重新引入 fg 字形拼色。
- owner 终端（macOS Terminal）的块状字形不保证铺满单元格（高 34/40px 已实测，宽向见本次截图）；任何依赖字形覆盖率的画法都视为缺陷。
