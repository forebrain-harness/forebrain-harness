# TUI composer 上方残留文本修复计划

状态：已实施（2026-09-21，实施记录见 §10）
定位日期：2026-09-21
涉及包：`pkg/tui`、`pkg/tool`

---

## 1. 结论

三张截图是**同一个缺陷**的三个样本：composer 上方那一行永久性的文本碎片（`tL`、`un git comm`、重复两遍的 `r 与冻结 · 实现非交互式硬失败 · 177 tools · 33510k in / 162k out`），**不是画错的行，而是别的行溢出到它上面、而画布永远不会再擦它**。

根因由两部分共同构成，缺一不可：

**根因 A（产生溢出）——画布的「一个合成行 = 一个物理行」不变量没有任何地方强制，而 composer 状态行恰好是全块里唯一一个既顶满整宽、又不过滤控制字符的行。**

`pkg/tui/render.go:4601` `renderComposerStatusLine`：

```go
text = runewidth.Truncate(strings.TrimSpace(text), termWidth, "…")
return sharedBlockReset + "\x1b[2m" + text + "\x1b[K" + sharedBlockReset
```

两个问题：

1. 预算是 `termWidth`（整宽），而同一块里其他所有行都留了 1 列余量——`renderSharedBlockBorderLine`（`render.go:4626`）用的是 `width-1`，注释写得很清楚：「so they never reach the terminal's autowrap threshold」。状态行是 composer 块里唯一顶到自动换行阈值的行，一列余量都没有。
2. `runewidth.Truncate` 把 `\n` `\t` `\r` 当成 0/1 宽的普通字符，原样放行。而状态行的内容里有一段是**模型写的**：`Counters.PlanActive` ← `PlanUpdatedPayload.Explanation` ← `pkg/tool/session_tools.go:217` `emitPlanUpdateStep` 把所有 in-progress 待办的 `active_form` 用 `" · "` 拼起来，全程只做过 `strings.TrimSpace`。模型写进 `active_form` 的一个 `\t`，终端会展开到下一个制表位；一个 `\n`，终端直接换行。runewidth 量出来的宽度和终端实际画的宽度对不上，行就溢出到下一物理行的第 1 列。

**根因 B（让溢出永久化）——damage-tracking 画布只重绘「字节变了」的行，而溢出落点恰好是一个字节永远不变的空行。**

`pkg/tui/reducer.go:3476` `paintViewportLocked` 的损伤循环：

```go
for i, content := range rows {
    if i < len(r.vpPainted) && r.vpPainted[i] == content {
        continue                      // 不发任何字节
    }
    fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", i+1)
    b.WriteString(content)
}
```

`buildNormalComposerBlock`（`reducer.go:3032`）里状态行的上下**各有一个空行**做间距：

```go
if statusText != "" {
    lines = append(lines, "")                                       // ← 溢出落点
    lines = append(lines, r.renderComposerStatusLine(statusText, termWidth))
}
```

这两行的合成字节恒为 `""`，从第一帧之后永远命中 `continue`，**再也不会收到 `\x1b[2K`**。于是状态行溢出到它下面那个空行上的字节，在整个会话剩余时间里一直留在屏幕上。`r.vpPainted` 只在 resize 和 alt-screen 进出时被 `dropPaintShadowLocked`（`reducer.go:3462`）丢弃，会话中途没有任何自愈路径，也没有任何用户可用的强制重绘。

**为什么用户感觉是「视图滚动后」出现的：**

- 溢出落在下标 `>= bodyHeight` 的行上，而 `writeRegionScroll`（`reducer.go:3430`）设的滚动区是 `\x1b[1;bodyHeight r`——**残留正好在滚动区外面**，所以正文哗哗往上滚，它纹丝不动地钉在 composer 上方，视觉上极其刺眼。
- composer 是底部固定的。它的逻辑行数一变（待办预览行出现、composer 文本换行、subagent roster 行出现/消失），状态行就整体上移或下移一行，于是**新的溢出落在新的空行上，旧的溢出还留在旧的空行上**——这就是截图三里同一段文字同时出现在状态行上方和下方的原因。

---

## 2. 复现与证据

### 2.1 终端行为：溢出确实落在下一行第 1 列（tmux，40 列）

正好顶满整宽的行**不会**换行（延迟换行），紧随其后的绝对 CUP 会清掉挂起标志：

```
row3: AAAA…AAAA (40 列)
row4: (空)
```

宽出 6 列的行，多出来的 6 列落到下一行第 1 列，且下一行不被重写时就留在那里：

```
row3: AAAA…AAAA (40 列)
row4: BBBBBB          ← 溢出，左对齐，正是截图里残留的形状
```

结论：截图里那种**左对齐的短文本碎片**，形态上只可能是上一行的溢出尾巴。

### 2.2 合成宽度：状态行是 composer 块里唯一顶满整宽的行

临时探针（已删除）对 `buildComposerBlock` 在 `termWidth=60` 下取各行显示宽度：

| 行 | 内容 | 宽度 |
|---|---|---|
| 0 | 间距空行 | 0 |
| 1 | **状态行** | **60 ← 顶满** |
| 2 | 间距空行 | 0 |
| 3 | 上分隔线 | 59 |
| 4 | `›` 输入行 | 2 |
| 5 | 下分隔线 | 59 |
| 6 | footer | 0 |

### 2.3 控制字符：`\n` `\t` 原样进入合成行

同一探针对 `renderComposerStatusLine` 的输出：

```
newline  width=107 containsNL=true   raw="…四态与订阅\n实现非交互式硬失败 · 177 tools…"
tab      width= 52 containsTab=true  raw="…step\tone\ttwo · 177 tools…"
cr       width= 49 containsCR=true
```

`runewidth` 把 `\t` 记成 0 宽，终端展开成 1–8 列；把 `\n` 记成 0 宽，终端直接换行。宽度模型和终端对不上，全部体现为溢出。

### 2.4 真机复现（`.claude/skills/run-forebrain/driver.sh`，tmux 120×40）

用 `tool` 模式让模型调一次 `session_todo`，其中一条待办的 `active_form` 里带一个制表符（完全合法的模型输出），再调一次长时 shell 让状态行停留：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D start tool '[{"name":"session_todo","arguments":{"items":[
  {"id":"1","content":"…","status":"in_progress","active_form":"实现 Registry 四态与订阅"},
  {"id":"2","content":"…","status":"in_progress","active_form":"实现配置项与\tfingerprint 分离"},
  {"id":"3","content":"…","status":"in_progress","active_form":"实现 Runner 异步加载与顺序控制"},
  {"id":"4","content":"…","status":"in_progress","active_form":"实现 Loader 与冻结"},
  {"id":"5","content":"…","status":"in_progress","active_form":"实现非交互式硬失败"}]}},
 {"name":"shell","arguments":{"command":"sleep 45"}}]'
$D submit 'do the plan'
```

屏幕（`tmux capture-pane`）：

```
32  ─ Worked for 10s · Tasks 0/5 · 实现 Registry 四态与订阅 · 实现配置项与    fingerprint 分离 · 实现 Runner 异步加载与顺序
33  控制 · 实现 Loader 与冻结 · 实现非交互式硬失败 · 1 tools · 0 in / 0 out
34
35
36  Runner …                                    ← 残留
37  ───────────────────────────────────────────────────────────────────────
38  ›
```

第 36 行的 `Runner …` 就是用户截图里的东西。它：

- 在空闲重绘、光标闪烁 8 秒后仍在；
- 滚上去再滚回来仍在；

即**永久残留**，与截图一致。

同时第 32–33 行暴露了**同一类缺陷的第二个实例**：`Worked for` 行也溢出成了两行（见 §4.2）。

---

## 3. 修复目标

1. 画布的不变量——**一个合成行占且仅占一个物理行**——必须被结构性地强制，而不是靠每个行生产者自觉。
2. 生产端按语义修好，使得这个强制在正常情况下永远不需要真的裁掉内容。
3. 画布具备一次性自愈手段（当前完全没有）。
4. 不得以任何形式回退 `47474054`（只重绘变化行）带来的免闪烁收益。

---

## 4. 同类缺陷清单（本期一并修完）

按「修交互缺陷必须穷举同类组件」的要求，以下全部属于本期范围，不进「已知问题」。

### 4.1 `renderComposerStatusLine`（`render.go:4601`）— 主因

整宽预算 + 控制字符放行。

### 4.2 `workedStatusLine`（`render.go:5641`）— 已在 §2.4 实测溢出

```go
prefix := "─ " + label + " "
if width <= lipgloss.Width(prefix) {
    return prefix          // ← 超宽时原样返回，完全不截断
}
```

`label` 同样含模型写的待办标签。超宽分支必须截断，而不是原样返回。

### 4.3 `overlaySeparatorLine`（`reducer.go:3930`）

`strings.Repeat("─", width)` 顶满整宽，与 `renderSharedBlockBorderLine` 的 `width-1` 约定不一致，零余量。

### 4.4 正文行的清洗不完整

`splitCapturedLines`（`reducer.go:2687`）只做了：按 `\n` 切分、去尾部 `\r`、`stripTerminalBells` 去 BEL。`\t`、行内 `\r`、`\v`、`\f`、以及会移动光标/滚屏的非 SGR CSI 全部原样进入正文行。`expandTabs`（`render.go:6170`）已经存在，但只有 diff 渲染在用。

### 4.5 画布没有自愈路径，也没有强制重绘入口

`vpPainted` 只在 resize / alt-screen 进出时丢弃。任何一次外部写入（异常的子进程输出、终端自身重绘）都会永久留在屏幕上，而用户手上**没有任何办法**把屏幕刷干净——全仓没有 Ctrl+L / redraw 绑定（已确认）。

### 4.6 计划标签在语义边界上没有被约束成单行

`emitPlanUpdateStep`（`pkg/tool/session_tools.go:217`）把模型写的 `active_form` 直接 join。`active_form` 的语义就是「一句正在做什么」，多行 / 带制表符不是这个字段的合法取值，应该在它变成 UI 文本的那一刻就被规范化。

---

## 5. 修复方案

### 5.1 画布层：强制不变量（根因 B）

在 `pkg/tui/reducer.go` 画布层新增：

```go
// fitPaintRow 把一条合成行收敛成画布能保证「占且仅占一个物理行」的形态：
// 制表符按当前显示列展开成空格，所有会移动光标、换行或滚屏的字节删除，
// 显示宽度截断到 width-1（与 renderSharedBlockBorderLine 同一条余量约定）。
// 保留 SGR（\x1b[…m）与 EL（\x1b[K / \x1b[2K），它们不落字形也不移动光标。
//
// 这不是兜底：damage tracking 只重绘字节变化的行，其正确性完全建立在
// 「一个合成行 = 一个物理行」之上。这里强制的就是那条不变量本身。
func fitPaintRow(line string, width int) string
```

接入点：`paintViewportLocked` 里源行进入 `rows[]` 的**两个**位置——

- 正文：`line := vr.lines[row]` 之后、选中/悬停装饰之前；
- composer：`composer.lines[i]` 取出之后、选中装饰之前。

顺序要求（否则会误伤）：

- 必须在装饰（`applySelectionHighlight`、hover 背景、尾部 `\x1b[K`）**之前**，装饰只加 SGR/EL，不影响宽度；
- 必须在 jump-to-bottom 按钮那条**绝对 CUP 追加之前**（`reducer.go:3750` 的 `rows[buttonRow] += "\x1b[%d;%dH…"`）——那是画布自己加的合法定位序列，不能被当成非法 CSI 删掉。

宽度一律用 `acd71c54` 定下的那一套显示宽度模型（`runewidth` + `EastAsianWidth=false`，与 lipgloss/x-ansi 一致），**不得引入第二套度量**。

性能：加一条快路径——先扫一遍，若不含控制字节且显示宽度已 `< width`，原样返回。绝大多数行走这条路径，每帧只多一次线性扫描，远小于一次行重绘。

### 5.2 生产端：按语义修好（根因 A）

| 位置 | 改法 |
|---|---|
| `renderComposerStatusLine` | 预算从 `termWidth` 改为 `termWidth-1`，与同块其他行一致 |
| `workedStatusLine` | `width <= lipgloss.Width(prefix)` 分支改为按 `width` 截断（尾部 `…`），不再原样返回 |
| `overlaySeparatorLine` | `width` → `width-1`，与 `renderSharedBlockBorderLine` 统一 |
| `splitCapturedLines` | 在 `stripTerminalBells` 同一位置扩成一次完整的控制字符清洗：展开 `\t`，删除行内 `\r` `\v` `\f` `\b`，删除非 SGR/EL 的 CSI |
| `emitPlanUpdateStep` | `active_form` / `content` 取出时把所有空白串（含 `\n` `\t`）折叠为单个空格再 join——`active_form` 语义上就是单行 |

`emitPlanUpdateStep` 那条是**语义修复而非清洗**：它让「进入 UI 的计划标签是单行文本」成为字段自身的约束，而不是留给下游每个渲染点各自防一遍。

### 5.3 画布自愈：新增强制重绘（§4.5）

新增 Ctrl+L：`dropPaintShadowLocked()` + `paintViewportLocked()`。

这不是兜底代码，而是画布当前缺失的一个能力：影子是对物理屏幕的模型，模型与现实一旦分叉，必须存在一条把模型重新对齐到现实的指令。它是用户触发的一次性操作，不在渲染热路径上，不会影响 `47474054` 的免闪烁收益。

绑定点：`pkg/tui/input_events.go` 的按键分发；帮助文案按「一句话」规范写成 `ctrl+l redraw`。

---

## 6. 任务拆解

| # | 任务 | 文件 |
|---|---|---|
| T1 | 实现 `fitPaintRow` + 表驱动单测 | `pkg/tui/reducer.go` |
| T2 | 在 `paintViewportLocked` 的两个源行入口接入（注意与装饰、按钮 CUP 的顺序） | `pkg/tui/reducer.go` |
| T3 | `renderComposerStatusLine` 预算改 `termWidth-1` | `pkg/tui/render.go` |
| T4 | `workedStatusLine` 超宽分支改截断 | `pkg/tui/render.go` |
| T5 | `overlaySeparatorLine` 改 `width-1` | `pkg/tui/reducer.go` |
| T6 | `splitCapturedLines` 扩成完整控制字符清洗，`stripTerminalBells` 并入 | `pkg/tui/reducer.go` |
| T7 | `emitPlanUpdateStep` 把 `active_form`/`content` 折叠为单行 | `pkg/tool/session_tools.go` |
| T8 | Ctrl+L 强制重绘 + 帮助文案 | `pkg/tui/input_events.go`、`pkg/tui/run.go` |
| T9 | 不变量测试（见 §7.2） | `pkg/tui/reducer_test.go` |
| T10 | 真机验证（见 §7.3） | — |

T1→T2 有依赖，其余可并行。T3–T7 单独任何一条都不足以修掉这个 bug（模型总能写出新的越界内容），T2 单独也不够（会静默裁掉本该显示的内容），必须一起落地。

---

## 7. 验证

### 7.1 单元测试

- `fitPaintRow` 表驱动：纯 ASCII / CJK / 东亚歧义宽字符 / `\t` 在不同起始列 / `\n` / `\r` / `\v` / `\f` / `\b` / SGR 保留 / `\x1b[K` 保留 / 非法 CSI 删除 / 宽度边界（`width-1`、`width`、`width+1`）/ 空串。
- `renderComposerStatusLine`：含 `\t` 与 `\n` 的状态文本，断言输出不含控制字节且显示宽度 `<= termWidth-1`。
- `workedStatusLine`：超宽 label 断言被截断。
- `emitPlanUpdateStep`：`active_form` 含 `\n`/`\t` 时 `Explanation` 是单行。

### 7.2 不变量测试（本次缺陷的真正护栏）

一条覆盖全部行生产者的断言测试：对 `buildComposerBlock`（normal / slash / overlay / 带 roster / 带待办预览 / 超长 composer 文本）与 `renderViewport`（含工具卡、diff、思考块、markdown 表格、含制表符的工具输出）的产物逐行断言：

> 显示宽度 `< termWidth` **且** 不含任何会移动光标或换行的字节。

再加一条画布级断言：连续喂两帧，其中第一帧状态行超宽、第二帧状态行变短，断言画布发出的字节里不存在「某一物理行被写入的字符数超过它自己那一行」的情况。

### 7.3 真机验证（必做，不可只靠单测）

1. 跑 §2.4 的 `driver.sh` 复现脚本，修复前必现 `Runner …`，修复后 composer 上方干净；等待 10 秒空闲重绘 + 上下滚动各一次后仍然干净。
2. 用长 CJK 待办列表（5 条 in-progress）跑一遍，状态行应被截断成一行并以 `…` 结尾，下方空行保持空。
3. 真终端（非 tmux）里手动跑一遍长会话，反复触发 composer 高度变化（打字换行、待办预览出现/消失、subagent roster 出现/消失）+ 滚动，确认 composer 上方不出现任何碎片。
4. Ctrl+L：人为把屏幕弄脏（例如在另一个窗口 `cat` 一段文字到同一个 tty）后按 Ctrl+L，屏幕应完全恢复。

### 7.4 回归

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/tool ./pkg/turn ./pkg/state -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1
```

重点看 `47474054` 留下的那几条 damage-tracking 测试（`reducer_test.go` 里统计 `\x1b[2K` 次数的用例）：修复后**每帧重绘的行数不得增加**。这是本次改动不回退免闪烁收益的硬指标。

---

## 8. 明确不做的事

- **不**每帧无条件重绘空行，**不**每 N 帧做一次全量重绘。那是治标，而且正好把 `47474054` 修掉的闪烁改回来。
- **不**在残留出现的位置加清屏补丁。残留的位置是症状，产生溢出的行和不会被擦的行才是原因。
- **不**为「模型可能写出奇怪内容」加一圈防御性判空/兜底。清洗只发生在两个真正的外部输入边界上：模型写的计划标签（`emitPlanUpdateStep`）和工具输出（`splitCapturedLines`）；画布层那一道是不变量强制，不是兜底。
- **不**引入第二套显示宽度度量。

---

## 9. 影响面

- 纯渲染层与一个工具事件字段的规范化，**不触碰提示词组装，对输入 token 缓存命中率无影响**。
- 用户可见变化：超长状态行/`Worked for` 行会以 `…` 结尾而不是溢出；composer 上方不再出现碎片；新增 Ctrl+L。
- 无状态结构变更，无迁移。

---

## 10. 实施记录（2026-09-21）

T1–T10 全部落地，回归 `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/tool ./pkg/turn ./pkg/state ./pkg/architecture -count=1` 全绿；damage-tracking 既有用例（`TestViewportRepaintOfAnUnchangedScreenTouchesNoRow` 等）原样通过，每帧重绘行数未增加。不变量测试在临时摘除画布接线后确实失败（护栏有效）。

实施中对计划的三处定向修正（其余逐条照计划执行）：

1. **§5.1 `fitPaintRow` 的画布预算取 `width`，不是 `width-1`**。footer 由 `layoutComposerFooter` 精确铺满整宽（`sharedBlockFooterTruncateExtraRoom = 0` 是刻意的贴右缘设计），按 `width-1` 裁剪会每帧削掉 token 统计的最后一列。计划 §2.1 自己证明了「正好顶满整宽的行不换行」，故「占且仅占一个物理行」的数学边界是 `≤ width`；`width-1` 余量按 §5.2 保留在生产端（T3/T4/T5）。`TestFitPaintRowKeepsAFlushFooterRowIntact` 把这一决策钉死。
2. **§5.2 `renderComposerStatusLine` 在改预算之外增加了空白折叠**（§7.1 的属性断言要求输出不含控制字节，仅改预算无法满足）：与 §4.6 的「进入 UI 文本的那一刻规范化」同一语义。
3. **T8 的帮助文案落在 `pkg/tui/setup.go` 的 banner 按键行**（`ctrl+l redraw` 一行），计划任务表未列出该文件；run.go 内无按键帮助文案可挂。T5 的契约变化同步更新了 `TestOverlayRulesSpanTheFullContentWidth`。

真机验收（tmux 120×40，`driver.sh tool` 模式）：5 条 in-progress CJK 待办（其中一条 `active_form` 带制表符）+ 长停留 shell——状态行/`Worked for` 行各收敛为一行并以 `…` 截断，tab 显示为折叠后的单空格，状态行上下空行保持空白；空闲重绘 10 秒 + 多次上下滚动后仍干净；向 pane tty 直接写入垃圾后按 Ctrl+L，屏幕完整恢复。§7.3 第 3 项（真人真终端长会话）留给使用者日常使用确认，tmux 内已等价覆盖 composer 高度变化与滚动的组合。
