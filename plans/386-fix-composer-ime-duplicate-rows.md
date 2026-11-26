# Plan 386（修订 v2）: 定位并除因 composer 带图 + 中文输入法偶现整行重复（IME 滚屏触发源诊断 → 除因修复）

> **Executor instructions**: 按步骤顺序执行，每步跑验证命令确认预期后再继续。**先诊断锁定真因，再除因修复**；
> 触发源未证实前不得改 painter 行为。出现 STOP 条件即停下报告，不即兴发挥、不堆防御代码。
> 完成后更新 `plans/README.md` 本计划状态行（除非派发你的评审方说它维护索引）。
>
> **Drift check（先跑）**: `git status --porcelain -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/reducer_test.go`
> 与 `git log --oneline -1`。把下方 "Current state" 摘录与现场用 `grep -n '<符号>'` 重定位对照；**内容不符即 STOP**
> （仅行号漂移不算）。并行会话 forebrain-harness-86 改 `Frame` 结构体/`emitFanoutFrame`/`fanoutHeaderLine`（不在本 scope）；
> `reducer_test.go` 共享，追加前读现场找空位。

## Status

- **Priority**: P1
- **Effort**: L（诊断 + 除因，触发源需真机锁定）
- **Risk**: MED（painter 热路径；除因方向以真机证据定，勿治标）
- **Depends on**: none
- **Category**: bug
- **Planned at**: 工作区 HEAD `c3176812`（2026-11-25；owner 在用二进制 `65fdd571+dirty`，painter 文件与 HEAD 一致——评审核实）
- **Issue**: 无（owner 截图报障）

## Why this matters

owner 在 TUI composer 粘贴截图（`[Image #1]` chip）后用搜狗拼音输入中文，偶现整段消息被复制成 4 行
（每行 `› [Image #1]` + 整段中文，内容随时刻略不同），末行尾挂 marked text。owner 截图像素带为
[顶边线][prompt×4][底边线][footer]，`qi't` 右侧仍留数格底色——证明"整屏被滚了若干次 + blink 修复只重写 anchor 以下 →
anchor 以上旧 prompt 副本累积残留"（**链后半成立**）。

**v1 计划的触发前提已被真机证伪**（zhipuai/glm-5.4 评审，Terminal.app 15.6 + 搜狗，HEAD 构建）：
caret 行 marked text 溢出**只压进下一行（底边线），不整屏滚**；8 组隔离探针表明**只有硬件光标停在末行右端**
（如 (30;158)）且 marked text 越过末列才整屏滚 1 行。所以触发源不是"caret 行溢出级联"，而是**光标何时停在末行右端**。

**owner 裁决（user_interaction，2026-11-25）**：① 先彻底定位真因再修（不接受治标兜底）；② bug 出现在**空闲输入**
（无流式/子代理）；③ **同一窗口多会话**（排除独立窗口抢焦点候选③）。

**leading 假设（候选①，待 Step 1 证实）**：空闲时唯一的 paint 是 500ms cursor-blink tick。每帧 damage loop 写完
满宽 footer（末行末列）后、收尾 CUP 回 caret 之前，硬件光标**瞬态停在 (末行, 末列)**（reducer.go:5714–5716 写行、
5779/5783 收尾 CUP）。Terminal.app 对 2026 同步输出无应答（评审 DECRQM 探针），字节按序处理 → IME marked-text
更新恰落入该窗口时，在末行右端插入 → 越过末列 → 整屏滚 1 行 → shadow 失步 → 副本累积。"偶现"= 竞态；
代码注释 reducer.go:5768–5778 本身就记载过"光标停 footer 右端 → IME 溢出每 500ms 滚屏"的历史 bug（当年的修复是
off-phase 也 CUP，但 CUP 仍在写 footer 之后，瞬态窗口仍在）。评审"worktree 版加延迟可复现"证实机制可能。

## Current state（执行者零上下文，全部内联）

涉及文件（均 `pkg/tui/`）：
- `reducer.go` — viewport painter `paintViewportLocked`（约 L5205 起）；IME 修复 `imeRepairStart`（约 L5040）；
  blink ticker `startCursorBlinkLocked`（约 L4768）。
- `render.go` — `composerPromptMarker = "› "`（**L84，是 › 不是 ❯**）；`sharedCardPad`（约 L5138）pad prompt 到恰好终端宽；
  `renderSharedBlockBorderLine`（约 L5150）= width-1 个 ─ + 空格 = 恰好 width；footer 恰好 width。
- `reducer_test.go` — 既有 IME 测试（约 L3925–4064）。

关键摘录（执行前 `grep -n` 重定位确认）：

1. **光标瞬态窗口（候选①核心）**：damage loop 逐行写，footer 是末行，写完光标在 (末行,末列)；收尾才 CUP 回 caret：
```go
// reducer.go damage loop（约 5696–5717）：变化行 CUP+2K+content，overdrawn 行 CUP+content
fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K", i+1)
b.WriteString(content)   // ← 写满宽 footer 后，光标停在 (末行, 末列)
// …loop 结束、caret paint 之后才：
fmt.Fprintf(&b, "\x1b[%d;%dH", desiredRow, desiredCol)   // 5779/5783 收尾 CUP 回 caret
```
历史注释（5768–5778）："the cursor stayed at the footer's right edge (the last cell written by the repaint):
the IME drew the pinyin preedit there, overflowed the bottom screen row and scrolled the viewport up every 500ms"。

2. **blink tick 每 500ms 无条件 due + repaint**（空闲时唯一 paint）：
```go
r.cursorBlinkVisible = !r.cursorBlinkVisible
r.vpIMERepairDue = true
r.paintViewportLocked()
```

3. **IME 修复只覆盖 anchor 以下**（链后半，副本残留的直接原因）：
```go
if from := imeRepairStart(r.vpIMEAnchor, r.vpPainted, rows, r.vpIMERepairDue); from >= 0 {
	for i := from; i < len(rows); i++ { overdrawn[i] = true }   // anchor 以上不碰
}
// damage loop：shadow==帧 且非 overdrawn → skip（失步的 anchor 以上行在此被漏掉）
```

4. **整屏滚是 documented 行为**：reducer_test.go `TestIMEAnchorWithoutACaretIsTheStartOfTheLastRow` 注释
   "pinyin typed there ran past the bottom row and scrolled the whole screen"；run-forebrain skill gotcha
   "Marked text that runs past the last row scrolls the whole alt screen"。评审探针实证：光标 (30;158)+qi't 越末列 → 滚 1 行；
   (27;157)/(28;158)+qita → 只进下一行不滚；(30;1)+marked text → 不滚（末行左端有 slack）。

### 候选触发源（Step 1 逐一区分）
- **① 每帧光标瞬态停末行右端**（leading）：footer 写完 → 收尾 CUP 之间的窗口，IME 更新落入。空闲 blink tick 即触发。
  区分探针：worktree 版在 footer 写与 CUP 之间加延迟 → 应稳定复现滚屏；抓 out.bin 字节时序 + 按键时序对齐。
- **② tty 上的非 painter 写入**：别的代码路径往 tty 写字节把光标留在末行。区分：grep 所有 `r.out`/`fmt.Fprint(r.out` 写入点，
  抓 out.bin 看滚屏前是否有非 paint 帧字节。
- **③ 多窗口抢焦点**：评审实测失焦把 composition 以原始字母提交（in.log `kbd b'yu'`）。**owner 已排除**（同一窗口多会话）。
  仍记录：同窗口多 tab 切换是否偷焦点，作为次要排查。

### 已排除的竞争解释（副本来源）
- wrap 全副本 span：续行 `"  "` 缩进无 ›（renderSharedBlockContinuationLine）；截图每行有 › → 非 wrap。
- buffer 损坏成 4 硬行：只有 visual line 0 带 ›；副本随时刻不同 → 非 buffer。
- writeRegionScroll 误移：只滚 transcript 区且 scrollShadow 同步 → 不产生副本。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestIME' -count=1` | PASS |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1` | ok（必要时 `-timeout 20m`） |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| gofmt | `gofmt -l pkg/tui` | 无输出 |
| 真机驱动 | `.claude/skills/run-forebrain/driver.sh` + `term_live.py` | 见 Step 1 |

直接 go 编译 pkg/pdf 需 `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`。
**构建必须带 `-tags fts5`**（否则真机报 `no such module: fts5`，工具链错误非产品 bug）。

## Suggested executor toolkit

- **run-forebrain skill**：`driver.sh serve` 隔离 home + fake provider；`term_live.py open/send/keys/contents/shot/probe/bytes`
  真窗口真实搜狗驱动。真机验收用 fake provider（owner 裁决）。
- **评审遗留工具（scratch，可复用/参考）**：`/var/folders/kw/mf8l2_c564b0m8qzhj073s940000gn/T/forebrain-plan-review-1796765728/`
  下 `shotscan.py`（截图 rule 行 y 坐标判滚屏，**比 contents 可靠**——contents 省略前导空行、合并 soft-wrapped 行，
  检测不到首行为空时的 1 行滚）、`decrqm.py`（探 2026 支持）、`fastopen.py`、证据图 `clip/owner-report-0.png`、
  `live-02-yuasdf.png`、`s1-t.png`。若 scratch 已清，按 skill 自建等价探针。
- **评审 worktree 测试**（证 Step 3 负断言修正）：`.worktrees/plan-review-2313910458/pkg/tui/review_plan383_ime_test.go`，
  `cd .worktrees/plan-review-2313910458 && CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestReviewPlan383 -v`
  → anchor 以上 addressed `[8 9]`（commit 使 composer 上长，顶边线/prompt 上移合法重写）。
- 隔离环境：`export FOREBRAIN_RUN_WORK=$TMPDIR/fb-ime-383 FOREBRAIN_RUN_SESSION=fb-ime-383 FOREBRAIN_RUN_PORT=8764`；
  **永不污染真实 `~/.forebrain`**。

## Scope

**In scope**：
- `pkg/tui/reducer.go` — painter 光标定位/写序（除因，约 L5696–5817）；如证据支持，`imeRepairStart`/blink 修复分支。
- `pkg/tui/reducer_test.go` — 回归测试（更新既有断言 + 新增）。

**Out of scope（明确不碰）**：
- `render.go` 的 `sharedCardPad`/border/footer 宽度契约（footer 故意满宽贴边）——除非 Step 1 证据表明必须改且 owner 批准。
- `render.go` wrap 函数、`input_events.go`、`Frame` 结构体/`emitFanoutFrame`/`fanoutHeaderLine`（86 的 scope）。
- `verticalShift`/`writeRegionScroll`/`scrollShadow` transcript 滚动逻辑。
- 任何 public API/配置/前端/其他包。
- **常驻 2Hz 整屏重画兜底**：owner 已裁决先除因，**不默认实施**；仅当 Step 1 证明真因短期无法除、且 owner 再次批准时才作兜底。

## Git workflow

- owner 裁决（plan-gated-bugfix）：直接当前主树源码实施，不用 worktree/快照、不 commit；改动留工作区待 owner 审阅。
  （诊断探针如需改代码加延迟，用 `-overlay` 或临时 worktree，**不改主树产品代码**；评审 worktree 可参考。）
- 计划文件禁 commit；`plans/` gitignore。共享 `reducer_test.go` 追加前读现场，不覆盖并行会话改动。

## Steps

### Step 1: 真机诊断——锁定滚屏触发源（先于任何产品源码改动）

目的：在 owner 条件（空闲输入、同窗口多会话、带 chip + 长中文 caret 靠右）下复现副本累积，并**确定光标何时停在末行右端**，
区分候选①/②。用 `out.bin`（程序写字节）+ 按键时序 + `shotscan.py`（rule 行 y 坐标）取证，**不靠 contents 数行**。

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/fb-ime-383 FOREBRAIN_RUN_SESSION=fb-ime-383 FOREBRAIN_RUN_PORT=8764
R=.claude/skills/run-forebrain
CGO_ENABLED=1 go build -tags fts5 -o "$FOREBRAIN_RUN_WORK/forebrain" ./cmd/forebrain
$R/driver.sh serve reply ok
D=$FOREBRAIN_RUN_WORK/fb
python3 $R/term_live.py open $D 162 30        # 复刻 owner 终态宽度（评审用 162 列 qi't 贴右缘）
osascript -e 'set the clipboard to (read (POSIX file "<真实png>") as «class PNGf»)'
python3 $R/term_live.py send $D '\x16'        # [Image #1] chip
python3 $R/term_live.py send $D ' forebrain harness tui里的check task的失败消息卡卡头的文案颜色跟成功完成的消息卡卡头文案颜色一样，没有区分，失败的消息卡卡头文案颜色应该是红色，'
python3 $R/term_live.py keys $D shift         # 搜狗切中文（in.log kbd b'x' 即英文模式）
python3 $R/term_live.py keys $D qita          # composing，空闲（无流式）
python3 $R/term_live.py shot $D $TMPDIR/fb-ime-383/s-compose.png
sleep 1; python3 $R/term_live.py shot $D $TMPDIR/fb-ime-383/s-settled.png   # 静置 ≥1 blink
python3 $R/term_live.py bytes $D > $TMPDIR/fb-ime-383/out.txt              # 程序写的字节
```
判定与区分：
- `shotscan.py` 比 s-compose/s-settled 的顶边线 rule y 坐标：y 上移 = 整屏滚过。副本累积 = 复现成功。
- **候选①证实探针**：用 `-overlay`  build 一个在 footer 写与收尾 CUP 之间加 `time.Sleep` 的副本（不改主树），
  同条件 composing → 应**稳定**复现滚屏（竞态变必然）。若加延迟稳定复现、不加偶现 → ① 证实。
- **候选②排查**：`grep -n 'r.out\|fmt.Fprint(r.out\|Fprintln(r.out' pkg/tui/*.go` 列非 painter 写入点；
  对照 out.bin 滚屏前是否有非 paint 帧字节。
- 记录：滚屏发生时硬件光标位置（out.bin 最后一个 CUP/写行落点）；是否仅空闲 blink tick 触发；2026 支持（decrqm.py）。

**预期**：① 证实——空闲 blink tick 的光标瞬态窗口是触发源。
**STOP**：复现不出（调宽度使 caret/光标贴末行右缘；仍不出报告）；或证据指向 ②/其他 → 按证据重判触发源，不凭猜测修。
收尾：`term_live.py close $D`；`$R/driver.sh stop`。环境留 Step 4 复用。

### Step 2: 除因修复（方向以 Step 1 证据定，下面是候选①证实后的设计）

目标：**消除/收窄硬件光标瞬态停在 (末行,末列) 的窗口**，使 IME 更新无机会在末行右端插入触发滚屏。
保持最小改动，候选方案按真机证据择优（执行者实测对比，选能稳定消除滚屏且无副作用者）：

- **方案 A（推荐先试）：收尾 CUP 紧跟 damage loop，先于 caret paint**。把光标回 caret 的 CUP 提到 damage loop 之后立即发射，
  最小化 footer 写完 → CUP 的字节间隙；并评估把 software-caret paint 移到 CUP 之后（caret paint 本身含 CUP 到 caret cell）。
- **方案 B：blink tick 不重写未变化的 footer**。空闲 blink 只需 toggle software caret（one cell）；若 footer 无 bite
  （shadow==帧）则不重写它 → 光标不到末行右端 → 无竞态。需与既有 bite 修复（imeRepairStart anchor-down）协调：
  仅当 marked text 真 shifted footer 时才重写。评估是否削弱 bite 修复。
- **方案 C：写序调整使末行非最后写入**（如 footer 先写、caret 行最后写），光标自然停在 caret 行而非末行。评估复杂度。
- **兜底（仅 owner 再批准）**：若 A/B/C 真机证无法稳定除因，blink 驱动整屏 resync（逐行 erase，2026 内）清累积副本——
  治标，常驻 2Hz 成本，owner 已表示先除因，**不默认做**。

无论哪个方案，**链后半的修复**（anchor 以上失步行被漏掉）应一并补：blink 驱动的修复覆盖 anchor 以上失步行
（整屏或至少 composer 区 + 上方受影响行），作为除因之外的安全网；erase 用于变宽 transcript 行（no-erase 留尾）。
此安全网是否保留以真机证除因后是否仍偶现副本定。

验证：
```bash
gofmt -l pkg/tui                                  # 无输出
CGO_ENABLED=1 go build -tags fts5 ./pkg/tui       # exit 0
```
**STOP**：需动 out-of-scope（footer 宽度契约等）才能除因 → 停下报告请 owner 裁决；两方案 edit-review 不收敛 → 重评。

### Step 3: 回归测试

在 `pkg/tui/reducer_test.go`（样板照既有 IME 测试的 newComposerRenderer/addressedRows/erasedRows/composerTextRowScreen）：
1. **除因回归**：按 Step 2 选定方案写测试钉死行为（如方案 A：断言收尾 CUP 在 damage loop 后、caret paint 前的字节序；
   方案 B：断言 blink 不重写未变化 footer）。
2. **失步安全网回归**（若保留）：渲染一帧（shadow==帧）→ 设 `vpIMERepairDue=true` → paint → 断言 anchor 以上失步行被重写回正确内容。
   **关键（评审 F1）**：忠实失步态是 shadow==帧，**不要移动 vpPainted**（移了会走 changed 分支恒绿无牙）。反证必做：去掉安全网分支 → 红。
3. **修正负断言（评审第 4 条，worktree 已证）**：commit 使 composer 上长时 anchor 以上行会合法重写，
   故"commit 路径不升级整屏"的负断言须限定为"**anchor 以上且帧字节==shadow 的行**不得被 address"，不是"anchor 以上一律不 address"。
4. 既有 `TestIMERepairStart`/`TestIMEAnchorWithoutACaretIsTheStartOfTheLastRow` 仍绿；
   `TestIMECancelledCompositionIsRepairedOnTheNextBlink`/`TestIMECommitRewritesTheRowsItsMarkedTextShifted` 随契约更新断言并注明理由。

验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestIME' -count=1     # PASS
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
```
**STOP**：非 IME 测试红 → 诊断是否本改动引入；预存红 `pkg/architecture TestTestFilesCorrespondToProductionFiles` 与本计划无关。

### Step 4: 真机验收（fake provider，按 bug 裁剪）

复用 Step 1 环境，重建修复二进制：
1. **复现→修复**：带 chip + 长中文 + 搜狗 composing（空闲）。`shotscan.py` 断言：静置 ≥1 blink 后顶边线 y 坐标**不再上移**、
   composer 仅 1 行 `› [Image #1] …`、无累积副本。composing 瞬态副本若仍 ≤500ms 闪现记录为限制（除因方案应消除滚动本身）。
   取证 shot + out.bin 为主，contents 辅助（容忍合并/省前导空行）。
2. **回归（无图短文）**：行为不变。
3. **回归（streaming）**：`$D start drip` 一轮，真机目检（连续帧，静态图验不了闪烁）streaming 正常、无整屏闪烁、无副本。
4. **中断自救**：composing 中 Esc → 静置无残留、无 bite；退出 EXIT_CODE 干净。
收尾：close/stop/kill-session/清临时目录。
**STOP**：真机仍累积副本 → 除因不彻底，回 Step 1 重判，不堆防御。

### Step 5: 收口

- 计划回填 Status"已实施并真机验收通过（日期）"+"验收记录"（触发源证据、选定方案与理由、踩坑）。
- 落盘 `plans/386-fix-composer-ime-duplicate-rows.md`（修订 v2 内容；383/385 已被并行会话占用），更新 `plans/README.md` 索引（DONE+日期）。
- `git status` 改动仅 reducer.go + reducer_test.go（+ plans/），未 commit。
- 报告：真因链（含触发源证据）、最小改动、验证、遗留限制。

## Done criteria（机器可查）

- [ ] Step 1 锁定触发源（候选①加延迟稳定复现 / 或证据指向的其他源），out.bin + shotscan 证据存档。
- [ ] `gofmt -l pkg/tui` 无输出；`go vet -tags fts5 ./...` exit 0。
- [ ] `go test -tags fts5 ./pkg/tui -run TestIME -count=1` PASS（含新增除因回归）；`./pkg/tui ./pkg/turn ./pkg/state` ok。
- [ ] 真机：带图 composing 静置后顶边线 y 不上移、composer 仅 1 行 prompt、无累积副本；streaming/中断回归正常、无闪烁。
- [ ] `git status` 改动仅 reducer.go + reducer_test.go（+ plans/），未 commit。

## STOP conditions

- 触发源未证实就改 painter 行为 → 禁止；先诊断。
- 复现不出 / 证据指向竞争解释 → 停下重判。
- 除因需动 out-of-scope（footer 宽度契约）→ 停下请 owner 裁决。
- 真机仍累积副本或出现整屏闪烁/streaming 退化 → 停下重评方案。
- 共享 reducer_test.go 冲突 → peer_send 协调，不覆盖。
- 两次 edit-review 不收敛 → 重评 scope。

## Maintenance notes

- 真因是 painter 写序的光标瞬态竞态（候选①），不是 caret 行溢出级联（v1 误判，真机证伪）。评审实验：只有光标在末行右端才滚。
- 除因方案以真机证据择优（A 收尾 CUP 提前 / B blink 不重写未变 footer / C 写序调整）；安全网（blink 覆盖 anchor 以上失步行）是否保留以除因后是否仍偶现定。
- 负断言限定"anchor 以上且帧字节==shadow 的行"（commit 上长会合法重写上方行——评审 worktree 实证）。
- 取证用 shotscan rule y 坐标，不靠 contents（省前导空行/合并 wrapped 行有盲区）。prompt 标记是 ›（render.go:84）。
- 2026 同步输出 Terminal.app 无应答（评审 DECRQM）——"无闪烁"不能押 2026，须真机目检。
- deferred：常驻 2Hz 整屏重画兜底仅 owner 再批准才做；transcript 漂移若独立另立计划。

## 评审 provenance

zhipuai/glm-5.4 真机评审（2026-11-25）证伪 v1 触发前提（caret 行溢出不滚；末行右端光标才滚），证实链后半（副本累积）。
owner user_interaction 裁决：先除因、空闲输入、同窗口多会话（排除候选③）。修订 v2 改为诊断先行 + 除因，
折入评审第 3/4/5/6 条（探针几何、负断言限定、基线 c3176812/标记 ›、多进程条件）。

## Implementation Status

- **VERDICT: APPROVE**（tech lead 独立评审，2026-11-25）。
- **Worktree / branch**: `.worktrees/fix-composer-ime-scroll`，分支 `fix-composer-ime-scroll`，提交 `38ec74ac fix(tui): write viewport frames bottom row first`（父 `c3176812`，未 push/merge；合并是 owner 决定）。
- **Executor**: zhipuai/glm-5.4（max effort），单 worktree 实施。
- **真因（比 v2 候选①更精确，已锁定）**: macOS pty 把 >1024 字节的 write 按 ≤1024 字节分片交给 Terminal.app，终端逐片生效；片间硬件光标停在第一片末字节。基线 blink 帧按行升序写、满宽 footer 最后写，owner 草稿长度下第 1024 字节恰落在 footer 末格 → 光标停 (30,162) → IME marked text 插那里越末行 → 整屏滚 1 行 → blink 修复只重写 anchor 以下 → 上方 prompt 副本累积。命中率取决于帧长 mod 1024 是否落在 footer 末几格（~6 字节窗口）→「偶现」。
- **修复（方案 C）**: damage loop 改末行最先写（`for i := len(rows)-1; i>=0; i--`），footer 落到帧前 ~300 字节，空闲帧不可能在 footer 内分片。reducer.go +16/−1（含机制注释）。
- **任务完成度**: Step 1 诊断 done（触发源锁定）；Step 2 除因 done（方案 C）；Step 3 回归 done；Step 4 真机验收 done；Step 5 收口 done（计划回填/README 由评审方维护）。

### 评审方亲跑的验证（worktree 内，非信报告）
- `gofmt -l pkg/tui` → 无输出。
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。
- `go test -tags fts5 ./pkg/tui -run 'TestIME|TestViewportWritesTheLastRowFirst|TestViewportRewritesTheRowsAReplacedBoxGlyphDrewOver' -count=1` → ok 0.653s。
- `go test -tags fts5 ./pkg/tui ./pkg/turn ./pkg/state -count=1` → ok（tui 199.841s / turn 4.826s / state 5.127s）。
- `go vet -tags fts5 ./...` → exit 0。
- scope：`git diff --stat c3176812..HEAD` 仅 reducer.go(+17/−1) + reducer_test.go(+97/−3)；`status --porcelain` 净（仅 gitignore 的 third_party 软链）。
- 真机证据归档 `$TMPDIR/fb-ime-383/evidence/`：基线 `s4b-delay150b.log` 逐 trial `SCROLL FOOTER`（tick 帧 1097/1101B）；修复 `s4f-delay150.log` SCROLL 计数 **0**、footer 标 ok。A/B 成立。

### 偏差（均有据、in-scope、服务意图）
- 选方案 C 而非 A/B：A 字节流不变无效；B（blink 不写 footer）会削弱「拼音从底边线溢出进 footer」的 bite 修复。合理。
- 未加「blink 覆盖 anchor 以上」安全网：除因后真机无残留副本，owner 不默认要兜底；计划本就「以真机证除因后是否仍偶现定」。合理。
- `TestViewportRewritesTheRowsAReplacedBoxGlyphDrewOver` 顺序期望 [row,row+2]→[row+2,row]：写序契约修订，注释引用新测试。合理。
- 负断言限定「anchor 以上且字节==shadow 的行」（评审第 4 条 worktree 实证）。已正确实现。
- term_live 代理把内核分片当两次 write 转发会掩盖本类 bug；executor 自建 `proxy2.py`（每帧单 write + 片尾保持）复现。合理判断。

### 遗留限制（executor 如实记录）
- 帧起点若 tty 队列非空（流式突发）分片位置偏移，仍低概率；分片落光标行时拼音短暂现光标右侧 ≤500ms 自愈（既有同类，非滚屏）。自然命中率随终端调度变化，本机未定量。
- 后续可做（未实施）：给 run-forebrain term_live.py 代理加「每帧单 write」模式，免再被工具掩盖。

### 必须告知 owner 的副作用
- **executor 清理时误删 owner 原剪贴板短文本的备份，无法恢复**（剪贴板按计划被覆盖为测试 PNG，但原内容备份丢失）。
- 搜狗已切回英文模式（探针验证）。测试窗口/fake provider/二进制/home 已清理，端口 8764/8765 释放。
- dev 在实施期间前移到 `c237bec1`（并行会话 384，动 reducer.go/render.go/reducer_test.go）；本 worktree 从 `c3176812`，**合并时可能冲突，以 dev tip 为准，合并是 owner 决定**。
- 落盘编号：repo `plans/383`/`385` 已被并行会话占用，本计划落盘为 **386**（本文件）。
- **合并记录（owner 裁决「合并回主树dev分支」，2026-11-26）**：rebase 串行落 dev——先 7a `0540a22f`(385)、fd `23e4afe1`(copy_on_select) 先落，本分支两次 re-rebase 无冲突、reducer_test.go 双方测试共存，组合树门禁全绿后 ff-only 落 dev **`9a3ae0c2`**（未 push）。executor worktree/分支已清理；他人未提交改动未碰。