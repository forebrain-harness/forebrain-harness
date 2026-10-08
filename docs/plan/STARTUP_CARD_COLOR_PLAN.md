# 启动卡片配色与终端 profile 对齐计划

状态：**已实施并真机验收通过（2026-10-08）**；见文末「验收记录」与「Implementation Status」
执行者须知：本计划自包含，file:line 与实测字节都在文内；执行时**不 commit**，改动留工作区。
实施第一步：把本文件复制为 `docs/plan/STARTUP_CARD_COLOR_PLAN.md`（本仓库惯例），后续回填验收记录。

---

## 1. 目标（Why this matters）

owner 要求 TUI 启动卡片与设计稿 `docs/design/STARTUP_CARD.html` 的卡片 **UI 一致**：

- 边框 + 「Forebrain Harness」下的横线：`#35506a`
- 快捷键 `/` `@` `esc` `ctrl+j`：`#87c3ea`，**与标题文字同色**
- 版本号 + 快捷键说明文字：`#7d93a6`
- 整体与设计稿一致（设计稿卡片另有 `.path{color:#cfd9e2}`）

owner 的两张截图：现状卡片（灰框、快捷键与标题不同色、版本/说明偏淡紫灰、路径近纯白）与设计稿卡片（边框藏青、标题与快捷键同为天蓝、说明钢灰蓝、路径浅冰蓝）。

---

## 2. 根因（file:line 证据链 + 实测字节）

### 2.1 卡片配色只有一个来源

`pkg/tui/setup.go:1658-1669` 的四个 `lipgloss` 样式是卡片全部颜色的唯一来源：

```
1659 forebrainLogoStyle  .Foreground(AdaptiveColor{Light:"#0d6e9c", Dark:"#5DADE2"}).Bold(true)   // 标题
1662 bannerBorderStyle   .Foreground(AdaptiveColor{Light:"#b7c6d4", Dark:"#35506a"})              // 边框 + 横线
1664 bannerMutedStyle    .Foreground(AdaptiveColor{Light:"#6f8494", Dark:"#7d93a6"})              // 版本号 + 说明
1666 bannerKeyStyle      .Foreground(AdaptiveColor{Light:"#0d6e9c", Dark:"#87c3ea"}).Bold(true)   // 快捷键
```

使用点：边框 `setup.go:1758,1774,1781,1792`；横线 `1813`；标题/版本 `1799-1803`；**路径 `1814`（`wrapBannerText(dir,…)` 原样拼接，无任何样式）**；快捷键 `1845`。
渲染路径唯一：`render.go:628-639`（viewport 模式 → `FrameBannerInfo`）→ `reducer.go:2977-2982` → `forebrainBannerLines`（`setup.go:1723`）。没有第二份卡片实现；`frontend/` 与 `pkg/gateway` 无启动卡片（已 grep `/ commands`、`ctrl+j newline`：零命中）。

### 2.2 三个缺陷

- **(a) 标题色与设计稿不符**：设计稿 `.logo{color:#87c3ea}`（`docs/design/STARTUP_CARD.html:147`）↔ 源码 Dark `#5DADE2`。用户「快捷键与标题同色」的要求因此不成立。
- **(b) 路径没有样式**：设计稿 `.path{color:#cfd9e2}`（`STARTUP_CARD.html:148`）↔ 源码 `setup.go:1814` 未加样式，实际是终端默认前景色（owner 终端里接近纯白）。
- **(c) 四色写成真彩色 hex，在 256 色终端被量化成别的颜色** —— 这才是 owner 屏幕上的现象。
  owner 终端是 256 色（`TERM=xterm-256color` 且 `COLORTERM` 为空 → lipgloss profile = ANSI256）。`muesli/termenv@v0.16.0 color.go:162-204`（`hexToANSI256Color`）按「每通道就近吸附到 6×6×6 立方 + 灰度斜坡 HSLuv 比较」量化：

| 角色 | 设计稿 hex | 真彩下发 | 256 量化（owner 实际看到） |
|---|---|---|---|
| 边框/横线 | `#35506a` | `38;2;52;80;105` ✓ | **`38;5;59` = `#5f5f5f` 中性灰** ✗（海军蓝消失，截图里的灰框） |
| 标题 | `#87c3ea` | *现* `38;2;93;173;226` ✗（现值 `#5DADE2`）→ *改后* 与快捷键同值 | `38;5;74` = `#5fafd7`（天蓝） |
| 快捷键 | `#87c3ea` | `38;2;135;195;234` ✓ | `38;5;116` = `#87d7d7`（青）✗ 与标题不是一个色 |
| 版本/说明 | `#7d93a6` | `38;2;125;147;166` ✓ | `38;5;103` = `#8787af`（淡紫灰）✗ |
| 路径 | `#cfd9e2` | 无样式（`49` 默认前景） ✗ | 同上（终端默认前景） |

实测字节（真机 `./build/bin/forebrain`，tmux 120×40，隔离 `FOREBRAIN_HOME`，`tmux capture-pane -e`）：

- 真彩（tmux 内 `COLORTERM=truecolor`）：`/tmp/card-dark.ansi` → 顶边框 `38;2;52;80;105`、标题 `38;2;93;173;226`、版本 `38;2;125;147;166`、快捷键 `38;2;135;195;234`、说明 `38;2;125;147;166`、路径 `49`。
- 256（`env -u COLORTERM TERM=xterm-256color` 包装）：`/tmp/card-dark256.ansi` → `38;5;59` / `38;5;74` / `38;5;103` / `38;5;116` / `38;5;103` / `49`。
- 浅色（`FOREBRAIN_THEME=light` + 256）：`/tmp/card-light.ansi` → 边框 `38;5;152`、标题/快捷键 `38;5;25`、说明 `38;5;66`。

owner 截图像素采样（会话剪贴板 PNG）与 256 签名一致：边框灰 `#878789/#62646a`（≈ 索引 59 + 字体平滑提亮）、标题 `#80cff8`（≈74）、快捷键 `#9aeaea`（≈116）、版本/说明 `#afafd9`（≈103）、路径 `#eff0eb`（终端默认前景）——即「灰框 + 标题≠快捷键」是 256 量化，不是别的 bug。

### 2.3 仓库已有同规则先例

吉祥物调色板刻意使用**精确的 xterm-256 条目**（`setup.go:1640-1648` 注释：「Every colour is an exact xterm-256 entry, so a 256-colour terminal shows the same colour a true-colour one does instead of a nearest-match quantisation」），并由 `TestForebrainMascotPaletteIsExactXterm256`（`chat_session_test.go:16337-16347`）把守。卡片文字四色没有遵守这条规则，这就是 (c) 的成因。

---

## 3. 修复设计

### 3.1 机制选择：`lipgloss.CompleteAdaptiveColor`（owner 已选定「按 profile 分别下发」）

lipgloss v1.1.0 原生提供按 profile 取值的颜色类型，无需自造机制：

```
/muesli/lipgloss@v1.1.0/color.go:116-136  CompleteColor{TrueColor, ANSI256, ANSI}  // 按 r.ColorProfile() 取字段
/muesli/lipgloss@v1.1.0/color.go:149-162  CompleteAdaptiveColor{Light, Dark CompleteColor}
```

`p.Color(c.ANSI256)` 仍会走 `hexToANSI256Color`，但只要给的 hex **本身就是精确 xterm-256 条目**，吸附结果就是它自己（无二次失真）。
`ANSI`（16 色）字段一律沿用该角色的设计 hex → 16 色终端行为与今天完全一致（不引入无设计依据的新值）。

### 3.2 配色表（真彩 = 设计稿精确值；256 = 感知最接近的调色板条目）

候选由 CIEDE2000（Lab）在全部 256 条目中选出（括号内为 ΔE00，越小越接近）：

**深色（设计稿卡片，`STARTUP_CARD.html:147-148`）**

| 角色 | TrueColor（设计稿） | ANSI256 | ΔE00 | 现渲染（256） |
|---|---|---|---|---|
| 边框 + 横线 | `#35506a` | **`#5f5f87`（xterm 60）** | 13.2 | `38;5;59` `#5f5f5f` 灰（14.4） |
| 标题 + 快捷键（同值） | `#87c3ea` | **`#87d7ff`（117）** | 5.4 | 74 `#5fafd7` / 116 `#87d7d7`（116 为 15.6） |
| 版本 + 说明 | `#7d93a6` | **`#5f87af`（67）** | 8.0 | 103 `#8787af`（12.7） |
| 路径 | `#cfd9e2` | **`#d7d7d7`（188）** | 5.4 | 无样式（终端默认前景） |

- 边框取 60 而不是感知最优的 24（`#005f87`，ΔE00 8.4）：设计稿 §04 已声明「`#35506a` · xterm 60 左右量化」（`STARTUP_CARD.html:454`），且 60 保留了设计稿「安静的低饱和虚线」性格（24 是饱和蓝，线条会明显变亮）。若验收时认为 60 偏灰/偏紫，按 STOP 条件换 24 即可（常量一处）。
- 标题与快捷键共用同一个 `CompleteColor` 值，天然满足「与标题文字颜色保持一致」。

**浅色（owner 已选「一并校正」；hex 值逐字保留，只补 256 条目）**

| 角色 | TrueColor（现值不变） | ANSI256 | ΔE00 | 现渲染（256） |
|---|---|---|---|---|
| 边框 + 横线 | `#b7c6d4` | **`#c6c6c6`（251）** | 7.7 | 152 `#afd7d7`（11.8） |
| 标题 + 快捷键 | `#0d6e9c` | **`#005f87`（24）** | 5.4 | 25 `#005faf`（8.2） |
| 版本 + 说明 | `#6f8494` | **`#5f87af`（67）** | 7.6 | 66 `#5f8787`（12.0） |
| 路径 | **`#3d4750`**（新增；设计稿未定义浅色卡片，取设计稿自身 `--ink-2`（`STARTUP_CARD.html:9`）作角色对齐：深色 `.path` 是卡片的主文案色，浅色的对应角色是次级墨色） | **`#444444`（238）** | 6.2 | 无样式（终端默认前景） |

> 浅色路径是本次唯一「设计稿没有直接给出」的值，落在设计稿自己的调色板里（`--ink-2`）；若 owner 另有偏好，只需改这一个常量。

### 3.3 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| 真彩 + 深色 | 边框 `#35506a`、标题 `#5DADE2`、快捷键 `#87c3ea`、说明 `#7d93a6`、路径=终端默认 | 边框不变、**标题→`#87c3ea`**、快捷键不变、说明不变、**路径→`#cfd9e2`** | 刻意修订：标题与路径对齐设计稿 |
| 256 + 深色 | 边框 `38;5;59`、标题 `38;5;74`、快捷键 `38;5;116`、说明 `38;5;103`、路径默认 | 边框 `38;5;60`、标题=快捷键 `38;5;117`、说明 `38;5;67`、路径 `38;5;188` | 刻意修订：256 端呈现设计稿外观（owner 主要诉求） |
| 真彩/256 + 浅色 | 边框 `#b7c6d4` / `38;5;152`、标题=快捷键 `#0d6e9c` / `38;5;25`、说明 `#6f8494` / `38;5;66` | 真彩值逐字不变；256 端改为 `38;5;251` / `38;5;24` / `38;5;67` | 刻意修订（owner 选定） |
| 16 色（ANSI profile） | 由 hex 走 `ansi256ToANSIColor` 量化 | 同左（`ANSI` 字段放同一 hex） | 不变 |
| 无色（Ascii/mono） | 吉祥物剪影、文字无色 | 同左 | 不变；`setup.go:1939` 的 mono 分支不动 |
| 布局/字形/宽度 | — | 逐字节不变 | 只允许 SGR 颜色字节变化 |

---

## 4. Scope

**In scope**
- `pkg/tui/setup.go`：四个样式改 `CompleteAdaptiveColor`；新增 `bannerPathStyle`；`bannerTextColumn`（1814 行）给路径套样式。
- `pkg/tui/chat_session_test.go`：新增配色/profile 回归测试（见 §5 Steps 3）。
- `docs/design/STARTUP_CARD.html`：§04 的卡片配色规格补一行 256/浅色条目（让设计稿仍是契约；卡片预览图本身是真彩 HTML，值不变）。
- `docs/plan/STARTUP_CARD_COLOR_PLAN.md`：本计划入库版 + 验收记录。

**Out of scope（明确不碰）**
- 吉祥物网格与调色板（`forebrainMascot*`，owner sign-off 才能动）、`bannerMinWidth`/`bannerMascotMinWidth` 等宽度语义。
- 卡片布局、折行、虚线字形、居中算法。
- 其他面板/聊天的配色（本次只动卡片）。
- `frontend/`、`pkg/gateway`：无启动卡片（已核）；官网仓库 `forebrain-harness.github.io` 是另一个仓库，不在本次改动面。

---

## 5. Steps（每步附验证命令与预期）

**Step 0** ✅ 复制本计划到仓库：`docs/plan/STARTUP_CARD_COLOR_PLAN.md`（仅新增文件，不动源码）。

**Step 1** ✅ 改 `pkg/tui/setup.go:1658-1712` 与 1814 行（行号已随改动下移）：四个角色按 §3.2 表改为 `lipgloss.CompleteAdaptiveColor`（`Light`/`Dark` 各带 `TrueColor/ANSI256/ANSI`），新增 `bannerPathStyle` 并套到路径上；表格加注释说明「256 条目由 CIEDE2000 从设计稿 hex 选出」+ 引用 `TestForebrainMascotPaletteIsExactXterm256` 的同规则。
验证：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → 无输出。

**Step 2** ✅ 先说清断言来源：真彩下 termenv 会做一次取整，`#35506a` 实测下发 `38;2;52;80;105`（不是 53/80/106）。**先抓字节再写期望值**（`--verbose` 跑一次 `forebrainBannerLines` 或按 §6 的 tmux 抓屏），不要照抄 hex 推字节。

**Step 3** ✅ 新增测试（`pkg/tui/chat_session_test.go`，紧邻 `TestForebrainMascotPaletteIsExactXterm256` 的写法：`lipgloss.SetColorProfile(...)` + `t.Cleanup` 还原，`lipgloss.SetHasDarkBackground(...)` 控主题）：
1. `TestForebrainBannerChromeIsTheDesignPalette` —— 强制 `termenv.TrueColor` + dark：卡片各行 SGR 必须含 `38;2;52;80;105`（边框/横线）、`38;2;135;195;234`（标题、快捷键、且两处相同）、`38;2;125;147;166`（版本与说明）、`38;2;207;217;226`（路径）。
2. 同测试内强制 `termenv.ANSI256` + dark：`38;5;60` / `38;5;117` / `38;5;67` / `38;5;188`，并断言**标题与快捷键的 SGR 完全相同**（owner 的显式要求）。
3. `TestForebrainBannerChromeLightProfile` —— `SetHasDarkBackground(false)` + ANSI256：`38;5;251` / `38;5;24` / `38;5;67` / `38;5;238`；TrueColor：`#b7c6d4`/`#0d6e9c`/`#6f8494`/`#3d4750` 对应字节。
4. 反证一次：把某个值改回旧值（例如边框 Dark.ANSI256 写回 `#35506a`），测试必须失败 —— 不能失败的守卫等于没有守卫。

验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ForebrainBanner' -count=1` → ok。
（注意 `process.Resolve()` 进程级缓存：若用例触及 path 解析，按仓库惯例 `process.ResetResolve()` + `t.Cleanup`。）

**Step 4** ✅ 包级回归 + vet：
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1`、`go vet ./...` → 绿（既有失败项需在验收记录里显式列出，属既有基线）。

**Step 5** ✅ 更新 `docs/design/STARTUP_CARD.html` §04：在「颜色 `#35506a` · xterm 60 左右量化」处补成三列表格（角色 / 真彩 / 256），并改写「浅色主题仍是 #b7c6d4，不改动」为新的浅色表；`open` 打开设计稿自查（owner 的视觉线硬流程：交付前无头 Chrome + 打开确认）。卡片预览图（真彩 HTML）不改。

**Step 6** ✅ 真机验收（`tui-render-ab-verify` 的字节法，构建必须带 `-tags fts5`）：
```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset; $D build; $D start hang          # 隔离 home/proj，真彩（tmux 注入 COLORTERM）
tmux capture-pane -t forebrain-run -e -p > /tmp/card-after-truecolor.ansi
# 256 路径：同一 work 目录放 env -u COLORTERM TERM=xterm-256color 包装，再抓一次
# 浅色路径：FOREBRAIN_THEME=light + 包装，再抓一次
```
四场景断言（从 capture 里解析 SGR run）：
1. 真彩/dark = §3.2 真彩列，且标题与快捷键 SGR 相同；
2. 256/dark = `38;5;60` / `38;5;117` / `38;5;67` / `38;5;188`；
3. 浅色/256 = `38;5;251` / `38;5;24` / `38;5;67` / `38;5;238`；
4. **布局零变化**：`diff <(sed 's/\x1b\[[0-9;]*m//g' /tmp/card-dark256.ansi) <(sed ... /tmp/card-after-*.ansi)` 只应出现版本串差异（构建版本号），字形/列宽/居中完全一致。
结束时 `$D stop` + 清理临时 home（不得污染真实 `~/.forebrain`）。

**Step 7** ✅ 回填计划：状态行改「已实施并真机验收通过（日期）」，补「验收记录」（场景→字节证据→踩坑），并把证据文件路径写进记录。

---

## 6. Done criteria（机器可查）

1. `rg -n "CompleteAdaptiveColor" pkg/tui/setup.go` 命中 4 处角色样式（+ 路径样式）。
2. `rg -n "5DADE2" pkg/tui/` → 0 命中（标题旧色彻底消失）。
3. 强制 profile 的单测（§5 Step 3 的 3 个）全绿，且在把任一值改回旧值后变红。
4. 真机 4 场景 capture 的 SGR 与 §3.2 表逐字节一致；SGR-stripped 布局 diff 仅版本串。
5. `go vet ./...` 无新增告警；`git status` 改动面 = §4 In scope（`pkg/tui/setup.go`、`pkg/tui/chat_session_test.go`、`docs/design/STARTUP_CARD.html`、`docs/plan/STARTUP_CARD_COLOR_PLAN.md`），且未 commit。

---

## 7. STOP conditions

- 真机上 60（`#5f5f87`）看起来是灰/紫而不是藏青 → 只改边框 `Dark.ANSI256` 为 `#005f87`（24，ΔE00 8.4，感知最优）后复验；不要顺手改别的角色。
- `#87d7ff`（117）在真机上过电（比设计稿明显更亮） → 换 `#87afd7`（110，ΔE00 6.5）。
- 任意两个角色在 256 下落到**同一个**条目（角色不可区分） → 按 §3.2 表的第二候选替换该角色。
- SGR-stripped 布局 diff 出现除版本串以外的任何差异（字形、列宽、居中、折行） → 停下，那是未被请求的变更，先查清再继续。
- 发现除卡片外的其它位置也在用这四个旧值（例如别的包/文档/官网）→ 不要扩散修改，写清位置留在报告里交 owner 决定。

---

## 8. Maintenance notes

- 卡片任何角色的颜色都必须给全 `TrueColor / ANSI256 / ANSI` 三个字段，并把 256 条目加进 §5 Step 3 的断言清单；只填一个字段会在某些终端上悄悄退化。
- 256 条目不是随手取的近似，而是「该设计 hex 在 xterm-256 调色板上的 CIEDE2000 最近条目」——换设计色时按同一方法重算（候选挑选脚本思路：把 256 调色板枚举出来，对目标色做 Lab/CIEDE2000 排序）。
- 浅色路径 `#3d4750` 是本次对设计稿调色板 `--ink-2` 的角色对齐，设计稿若将来给出浅色卡片，应改以设计稿为准。
- 与吉祥物调色板的关系：那里要求「本身就是精确 256 条目」，这里是「按 profile 各给一个值」——两者都为了同一个目标（256 端不出现最近匹配失真）；改卡片配色时不要退回单一 hex。

---

## 验收记录（2026-10-08）

### A. 与计划的三处偏差（计划前提有误，按根因改正）

1. **§3.1 的机制前提只对 6×6×6 立方体条目成立。** `CompleteColor.ANSI256` 最终走 `termenv.Profile.Color`，hex 会进 `hexToANSI256Color`；而 termenv v0.16.0 的**灰度候选是用立方体索引算的**（`average := (r+g+b)/3`，r/g/b ∈ 0..5 → `grayIdx = (average-3)/10` 恒为 0 → 灰度候选恒为 `#080808`），所以 233–255 灰阶条目**从 hex 不可达**：实测 `#c6c6c6`→188、`#444444`→59、`#3d4750`→59。计划里浅色边框 `#c6c6c6`(251)、浅色路径 `#444444`(238) 若按 hex 写就会各自落到 188 / 59（角色还会撞色）。
   → 落地方案：`ANSI256` 字段**直接写 xterm 索引字符串**（`"60" "117" "67" "188"` / `"251" "24" "67" "238"`）。`termenv.Profile.Color("251")`（profile.go:84-105）用 `strconv.Atoi` 直接返回 `ANSI256Color(251)`，比 hex 更精确：没有任何量化步骤，索引即值。
2. **§5 Step 3 的三个用例合并为一个表驱动用例** `TestForebrainBannerPaletteIsTheDesignSheet`（`{真彩,256} × {深,浅}` 四个子用例），断言 7 个角色（边框/横线/名称/版本/路径/快捷键/说明）+ 名称与快捷键逐字节相同 + 四色互不相同。浅色不再单独成例；§5 Step 3.1/3.2/3.3 的断言值全部包含在表里。
3. **16 色（ANSI profile）的标题色随设计 hex 变化**：旧 `#5DADE2` 量化到 ANSI 青（`36`），新设计 hex `#87c3ea` 量化到亮青（`1;96`）。其余角色的 16 色字节不变（深色 `90`/`94`/`37`，浅色 `96`/`36`/`90`）。这是「标题对齐设计稿」的必然结果，属刻意修订而非回归。

另：`pkg/tui/render.go:4929` 的注释点名了 logo 的旧色 `#5DADE2`，按 Done criteria 2（`pkg/tui/` 内 `5DADE2` 零命中）一并改成 `#87c3ea`。

### B. 配色表逐项复核（CIEDE2000，全 256 调色板）

| 角色 | 计划 256 条目 | 全调色板最近条目（ΔE00） | 落地 |
|---|---|---|---|
| 深 边框/横线 `#35506a` | 60 | 24（8.4）、25（10.0）、60（12.8） | **60**（计划刻意取设计稿命名值，保留低饱和藏青） |
| 深 名称/快捷键 `#87c3ea` | 117 | 117（5.4） | **117** ✓ |
| 深 版本/说明 `#7d93a6` | 67 | 67（8.0） | **67** ✓ |
| 深 路径 `#cfd9e2` | 188 | 188（5.4） | **188** ✓ |
| 浅 边框/横线 `#b7c6d4` | 251 | 251（7.7） | **251** ✓ |
| 浅 名称/快捷键 `#0d6e9c` | 24 | 24（5.4） | **24** ✓ |
| 浅 版本/说明 `#6f8494` | 67 | 67（7.6） | **67** ✓ |
| 浅 路径 `#3d4750` | 238 | 238（6.2） | **238** ✓ |

计划表里的 ΔE00 数字逐条复现（±0.1），即 §3.2 的选色方法成立；只有「用 hex 下发条目」这一步不成立（见 A.1）。

### C. 真机字节（`driver.sh` + `tmux capture-pane -e -p`，120×40，隔离 home，`-tags fts5`）

| 场景 | 边框/横线 | 名称 + 4 个快捷键 | 版本 + 4 条说明 | 路径 |
|---|---|---|---|---|
| 真彩 · 深 | `38;2;52;80;105` ×19 | `1;38;2;135;195;234` ×5 | `38;2;125;147;166` ×5 | `38;2;207;217;226` ×1 |
| 256 · 深 | `38;5;60` ×19 | `1;38;5;117` ×5 | `38;5;67` ×5 | `38;5;188` ×1 |
| 真彩 · 浅 | `38;2;183;198;211` ×19 | `1;38;2;13;110;156` ×5 | `38;2;111;131;147` ×5 | `38;2;60;71;80` ×1 |
| 256 · 浅 | `38;5;251` ×19 | `1;38;5;24` ×5 | `38;5;67` ×5 | `38;5;238` ×1 |

四列逐字节等于 §3.2 表；「名称 + 4 个快捷键 = 同一串 ×5」即 owner 要求的同色（改动前真彩是 `38;2;93;173;226` 标题 + `38;2;135;195;234` 快捷键两个色）。
证据：`/tmp/card-after-{truecolor,256,light256,truecolor-light}.ansi`；改前基线 `/tmp/card-{dark,dark256,light}.ansi`。

**布局零变化**：`/tmp/card_fingerprint.py` 对同一 work 目录（`$TMPDIR/forebrain-run`，保证路径串一致）比较改前/改后，卡片宽度 96、10 行、上下边框各 94 个 ┄、名称行/横线行/路径行行号与起始列（26）、横线 67 个 ┄ 全部相同——`layout fields that differ: none`（真彩与 256 两对都一样）；唯一差异是版本串（新构建带 `+dirty`）。
原始行 diff 另有两处**与卡片无关的 transient**：旧 capture 里的一行剪贴板提示 `Image in clipboard · ctrl+v to paste`，以及 composer 行尾多一个空格——均属抓屏时刻的输入区状态，不在卡片内。

### D. 命令与结果

| 命令 | 结果 |
|---|---|
| `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` | 通过（后续 `go build -tags fts5 ./...` 亦通过） |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ForebrainBanner' -count=1` | ok；`-count=2` 亦 ok（全局 profile 状态在 `t.Cleanup` 还原） |
| **反证**：把 `bannerBorderColor.Dark.ANSI256` 写回 `"#35506a"` | `256-colour dark: the border is "\x1b[38;5;59m", want "\x1b[38;5;60m"` → **FAIL**；已还原 |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1` | ok（41.3s / 0.4s）；**既有基线**：`pkg/tui` 整包连跑 4 次里有 1 次 `TestCharacterizationNetworkApproval` 失败——`curl: (28) Connection timed out after 5002 milliseconds`（该机 curl 取不到网），与配色无关，属本机已知基线（`tui-render-ab-verify` 的 failure modes 里就列着它）；连跑其余 3 次全绿 |
| `go vet ./...` | 无输出 |
| `rg -n "5DADE2" pkg/tui/` → 0 命中；`rg -n "CompleteAdaptiveColor" pkg/tui/setup.go` → 5 处（1 处注释 + 4 个角色字面量） | Done criteria 1、2 满足 |
| 设计稿自查（owner 的视觉线硬流程） | 无头 Chrome 1280×2400：`docScrollW 1265 ≤ innerW 1280`（无横向溢出）；§04 两张表均 5 行 × 3 列、宽 1052、x=107（与 §06 表同栏）；右侧 40px 条带除滚动条外单色；截图 `/tmp/startup_card_colour_check.png`；已 `open` 设计稿给 owner 确认 |
| `git status --short` | `M docs/design/STARTUP_CARD.html`、`M pkg/tui/chat_session_test.go`、`M pkg/tui/render.go`、`M pkg/tui/setup.go`、`?? docs/plan/STARTUP_CARD_COLOR_PLAN.md`，**未 commit** |

真机跑完后已 `$D stop` 并清掉本次的 tmux 会话（只剩 owner 自己的 `graphite-docs-dev`）、包装脚本与两个临时 work 目录；未触碰真实 `~/.forebrain`。`$TMPDIR/forebrain-run/` 里只留 driver 的标准产物（`home`/`proj`/`tui.err` 空/`provider.log`）。

### E. STOP 条件核对

- 60（`38;5;60`）在真机上不是灰/紫：它是低饱和藏青，与设计稿虚线框一致 → 按计划保留 60，不换 24。
- `38;5;117` 在真机上没有「过电」感（与设计稿 `#87c3ea` 同调）→ 不换 110。
- 四个角色在 256 下互不相同（60/117/67/188、251/24/67/238），并被用例把守。
- 布局 diff 无字形/列宽/居中/折行差异（见 C）。
- 旧值没有出现在卡片以外的其它实现里：`pkg/tui/render.go:4929` 只是一句注释引用（已改），`frontend/`、`pkg/gateway/` 无启动卡片。

## Implementation Status

**已完成，改动 4 个文件 + 1 个新增入库计划，未 commit。**

- `pkg/tui/setup.go`：新增 `bannerBorderColor` / `bannerTitleColor` / `bannerMutedColor` / `bannerPathColor` 四个 `lipgloss.CompleteAdaptiveColor`（真彩 = 设计稿 hex，ANSI256 = xterm 索引，ANSI = 设计稿 hex），五个样式改为引用它们（新增 `bannerPathStyle`），`bannerTextColumn` 的路径逐段套 `bannerPathStyle`。
- `pkg/tui/chat_session_test.go`：新增 `TestForebrainBannerPaletteIsTheDesignSheet`（4 场景 × 7 角色 + 同色/互异断言）与 `bannerSGRBefore`/`bannerRoleSGR` 两个 helper。
- `pkg/tui/render.go`：注释里的旧 logo 色 `#5DADE2` → `#87c3ea`。
- `docs/design/STARTUP_CARD.html` §04：补「角色 / 真彩 / xterm-256」两张表（深色、浅色），并把原「浅色仍是 #b7c6d4，不改动」改写成新的浅色表。
- `docs/plan/STARTUP_CARD_COLOR_PLAN.md`：本计划入库 + 验收记录。

**偏差**：见「验收记录 A」（ANSI256 字段改用索引字符串；三个用例合并为一个表驱动用例；16 色标题色随设计 hex 变化）。
**未做**：吉祥物/宽度语义/布局/其它面板配色/`frontend`+`pkg/gateway`（计划 Out of scope），一行未碰；`go.mod` 未变（CIEDE2000 复核用的一次性脚本已删除）。
**验证**：见「验收记录 D」——单测（含反证）、包级回归、`go vet`、真机四场景字节、布局指纹、设计稿无头渲染自查，全部通过。唯一遇到的失败是本机既有基线 `TestCharacterizationNetworkApproval`（curl 超时，与本次改动无关），已在 D 表显式列出。
