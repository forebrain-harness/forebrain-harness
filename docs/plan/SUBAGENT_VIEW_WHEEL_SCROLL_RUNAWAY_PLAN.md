# Plan: 修复 TUI subagent 视图鼠标滚轮"越滚越多停不下来"（SGR 滚轮 release/motion 事件被重复当档位）

> **[REVERTED 2026-10-09]** 本计划所述代码已于 2026-10-09 由
> `docs/plan/SUBAGENT_VIEW_PERF_ROLLBACK_PLAN.md` 整体回滚（owner 裁决：该系列
> subagent/主视图性能优化全部无效）。文中的 COMPLETED/验收回填仅作历史记录；
> 提及的代码位置（`input_events.go` 滚轮 press/motion guard、`TestWheelReleaseAndMotionReportsDoNotScroll`）
> 均已不存在，勿按本文实施或复现。press+release 双倍、motion 采样 N 倍超滚的旧行为
> 系 owner 已知悉并接受的回滚后果。

> 本文件为送审版。批准后第一步复制为仓库惯例路径
> `docs/plan/SUBAGENT_VIEW_WHEEL_SCROLL_RUNAWAY_PLAN.md`，收口时在该文件回填验收记录。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（单点协议守卫 + 表驱动回归测试；不动滚动/渲染/合并逻辑）
- **Depends on**: none
- **Category**: bug
- **Planned at**: 工作区 @ `6667bb5`（2026-10-08；实施前以本文件 file:line 对照做 drift check）

## 1. 目标（Why this matters）

owner 报告：TUI subagent 视图里鼠标滚轮"只滚动几下，但实际视图滚动停不下来，会滚动更多"。
定位结论：**SGR 鼠标解码把"带滚轮按钮位"的一切报告——press、motion（运动采样）、release——
都当作一个滚轮档位（±3 行）**。凡是对一次滚轮手势上报 press+release（Kitty/新版 xterm 系行为）
或 press+motion 采样+release 的终端/驱动，一个物理档位会被应用 2×~N×3 行：手感即"滚几下、
视图自己越滚越多、停不下来"。subagent 视图 transcript 最长、且是"看着 subagent 干活时滚动"的
主战场，所以症状在那里最刺眼——机制本身与视图无关（主视图同样受影响，只是内容短、
很快撞 maxOffset 钳制而不易察觉）。

修复后：一个滚轮档位 = 一次 press 报告（Cb 64/65、终止符 `M`、无 motion 位）；release 与
motion 采样不再产生滚动；press-only 终端（tmux 注入、多数常见默认）行为不变。

## 2. 根因（file:line 证据链 + 真机证据）

### 2.1 缺陷代码（唯一改动点）

`pkg/tui/input_events.go:739-771` `mouseEventFromSGR`（当前原文）：

```go
func mouseEventFromSGR(ev sgrMouseEvent) (inputEvent, bool) {
	if ev.wheel {
		delta := wheelScrollLines
		if ev.wheelUp {
			delta = -wheelScrollLines
		}
		// Carry the pointer position so ViewportScrollAt can route the
		// notch to the overlay composer (when the cursor is over it) or
		// the transcript. ...
		return inputEvent{kind: inputEventMouseWheel, wheelDelta: delta, mouseCol: ev.col, mouseRow: ev.row}, true
	}
	// Motion events set bit 5 (32) in Cb. Only a left-button drag affects
	// selection. ...
	if ev.button >= 32 && ev.press {
		...
```

`ev.wheel` 的来源（`pkg/tui/input_events.go:689-706` `parseSGRMouse`）：

```go
	ev := sgrMouseEvent{
		button: cb,
		col:    cx - 1,
		row:    cy - 1,
		press:  term == 'M',
	}
	...
	// Wheel notches set bit 6 (64) of Cb: 64=up, 65=down. They report as a
	// press ('M') with no matching release.
	if cb&0x40 != 0 {
		ev.wheel = true
		ev.wheelUp = (cb & 0x01) == 0
	}
```

问题：`wheel` 只看 Cb 的 bit6；`mouseEventFromSGR` 的 wheel 分支不检查
`press`、也不排除 motion 位（bit5=32）。于是：

- **release**（Cb 64/65、终止符 `m`、`press=false`）→ 再滚一档：每档双倍；
- **motion 采样**（?1002h 按住期间的运动报告，Cb 96/97 = 64+32，终止符 `M`）→
  每个采样滚一档：一个手势 N 倍滚动，且采样流以终端上报频率持续——正是
  "停不下来、滚更多"。

注释自己写明协议语义（"They report as a press ('M') with no matching release"），
代码却未按它收口。

### 2.2 真机证据（2026-10-08，tmux 120×40，`-tags fts5` 构建于 `6667bb5`+dirty）

用 `.claude/skills/run-forebrain/driver.sh`（subagent-net 模式，`ENABLE_SUBAGENT=1`，
40 个 continue 回合约 1100 行 transcript，点任务卡片打开 subagent 视图），向 pane 注入
SGR 字节序列，用"单档下滚数回底、以 Jump to bottom 按钮消失判定到底"的内容无关计数法测量：

| 注入（每手势） | 实测行程 | 语义正确值 | 超滚倍数 |
|---|---|---|---|
| press(`\e[<64;30;10M`) ×2 | 6 行 | 6 行 | 1×（正常） |
| press+release(`M`+`m`) ×2 | **12 行** | 6 行 | **2×** |
| press + motion×6(`\e[<96;30;10M`) + release | **24 行** | 3 行 | **8×** |
| 仅 press ×300 @12ms（动量模拟） | 900 行，尾延 0.24s | 900 行 | 1×（精确） |

最后一行同时完成了对滚动链路其余环节的**排除性证明**（见 §2.3）。

### 2.3 已查证排除（执行者不要重查）

以下环节经 300 档 @12ms、150 档 @3ms、21 档 @0ms 等注入实验证实**精确无丢失、无自走、
无 follow 误重挂**，与根因无关：

- 空闲循环滚轮合并（`pkg/tui/run.go:740-766`）、活动回合合并（`drainActiveRunEvents`，
  `pkg/tui/run.go:3732-3760`）、`handleBlockingSlashInput`（run.go:1357）：delta 求和后一次应用，总数精确；
- `ViewportScrollAt`/`scrollViewportLocked`（`pkg/tui/reducer.go:5052-5074, 5338-5360`）：钳制与 follow 数学正确；
- 读入管线（`input_events.go` 中段 goroutine 逐字节解析、`ch` 缓冲 8、ack 握手）：press-only 流全部到达且应用；
- 流式输出期间的滚动位置保持（drip 模式实测：上滚后 8 秒纹丝不动）；
- 画布/阴影（burst 后 Ctrl+L 全量重绘位置不变 → 屏幕与模型一致）。

方法论坑（本调查踩过）：subagent 视图内各回合内容相同（26 行周期）时，"首行锚/10 行指纹"会
跨回合伪匹配，得出"事件丢失/跳底"假象。**测位移一律用 §2.2 的按钮计数法或唯一内容**。

## 3. 修复设计

### 3.1 生产改动（唯一一处）

`pkg/tui/input_events.go` `mouseEventFromSGR` 的 wheel 分支加白名单收尾——只有
"press 报告且无 motion 位"才算一个档位：

```go
func mouseEventFromSGR(ev sgrMouseEvent) (inputEvent, bool) {
	if ev.wheel {
		// One wheel notch is one PRESS report (Cb 64/65, terminator 'M')
		// without the motion bit. Terminals that answer a wheel gesture with
		// release reports (same Cb, terminator 'm') or wheel-bit motion
		// samples (Cb 96/97 = 64+32) must not scroll again for each of
		// those: this branch used to turn every report carrying the wheel
		// bit into a 3-row scroll, so one physical notch scrolled 2x
		// (press+release) or Nx (press+motion stream+release).
		if !ev.press || ev.button&0x20 != 0 {
			return inputEvent{}, false
		}
		delta := wheelScrollLines
		if ev.wheelUp {
			delta = -wheelScrollLines
		}
		// （原有 mouseCol/mouseRow 注释与返回保持不变）
		return inputEvent{kind: inputEventMouseWheel, wheelDelta: delta, mouseCol: ev.col, mouseRow: ev.row}, true
	}
	...（后续分支不动）
```

判据来源：`parseSGRMouse` 已解码 `press`（终止符 `M`/`m`，input_events.go:693）与原始
`button`（=Cb）。motion 位 = bit5（32），与既有注释"Motion events set bit 5 (32) in Cb"
同源。覆盖取值用白名单收尾（press 且无 motion 位才滚），不为每种"别的组合"写排除分支。

`overlays.go:2941` `rawSelector.dispatchMouse` 复用同一 `mouseEventFromSGR`，自动同修，
无需另改。

### 3.2 行为变化矩阵

| 输入（SGR 报告） | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| wheel press：`<64;…M` / `<65;…M` | -3 / +3 行 | 同左 | 不变（tmux 注入、多数终端默认即此） |
| wheel release：`<64;…m` / `<65;…m` | 再滚 -3 / +3 行 | **忽略** | 刻意修订（双倍根因）；xterm 语义滚轮无 release |
| wheel 位 motion：`<96;…M` / `<97;…M` | 每采样 -3 / +3 行 | **忽略** | 刻意修订（N 倍根因） |
| 左键 press/drag/release（Cb 0/32/…） | 选择/点击语义 | 不变 | 分支顺序未动 |
| 无键 motion Cb=35 丢弃、Cb=32 拖拽 | 同现有测试 | 不变 | `TestMouseEventFromSGRDropsNoButtonMotion` 保绿 |

### 3.3 回归测试（`pkg/tui/chat_session_test.go`，紧邻 `TestMouseEventFromSGRDropsNoButtonMotion` :23420 的写法）

```go
func TestWheelReleaseAndMotionReportsDoNotScroll(t *testing.T) {
	// press = 一个档位
	if ev, ok := mouseEventFromSGR(sgrMouseEvent{button: 64, press: true, col: 4, row: 2}); !ok || ev.kind != inputEventMouseWheel || ev.wheelDelta != -wheelScrollLines {
		t.Fatalf("wheel-up press = (%#v, %v), want one -3 wheel notch", ev, ok)
	}
	if ev, ok := mouseEventFromSGR(sgrMouseEvent{button: 65, press: true}); !ok || ev.wheelDelta != wheelScrollLines {
		t.Fatalf("wheel-down press = (%#v, %v), want one +3 wheel notch", ev, ok)
	}
	// release（终止符 'm' → press=false）不得再滚
	for _, cb := range []int{64, 65} {
		if got, ok := mouseEventFromSGR(sgrMouseEvent{button: cb, press: false, col: 4, row: 2}); ok {
			t.Fatalf("wheel release Cb=%d produced %#v; a wheel notch has no release", cb, got)
		}
	}
	// motion 采样（Cb 96/97 = 64+32）不得滚
	for _, cb := range []int{96, 97} {
		if got, ok := mouseEventFromSGR(sgrMouseEvent{button: cb, press: true, col: 4, row: 2}); ok {
			t.Fatalf("wheel-bit motion Cb=%d produced %#v; motion samples are not notches", cb, got)
		}
	}
	// 端到端解码：release 报告经 parseSGRMouse 后同样被忽略
	if mev, ok := parseSGRMouse([]byte("\x1b[<64;30;10m")); !ok {
		t.Fatalf("parseSGRMouse failed on wheel release report")
	} else if _, ok := mouseEventFromSGR(mev); ok {
		t.Fatalf("wheel release decoded into a scroll event")
	}
}
```

既有 `pkg/tui/overlays_test.go:376-400` 的 parseSGRMouse press 用例不受影响（只断言解码字段）。

## 4. Scope

**In scope**
- `pkg/tui/input_events.go`：`mouseEventFromSGR` wheel 分支守卫（§3.1）。
- `pkg/tui/chat_session_test.go`：§3.3 一个测试。
- `docs/plan/SUBAGENT_VIEW_WHEEL_SCROLL_RUNAWAY_PLAN.md`：本计划入库版 + 验收回填。

**Out of scope（明确不碰）**
- `pkg/tui/run.go` 三处滚轮派发/合并、`pkg/tui/reducer.go` 滚动/绘制/follow：已实证精确（§2.3）。
- 读入管线吞吐（`rawInputReadBufferSize = 1`、ch 缓冲 8、ack 握手）、动量取消/平滑滚动：
  非"滚更多"根因；如 owner 另有手感诉求另立计划。
- `overlays.go`：经 `mouseEventFromSGR` 自动同修，无直接改动。
- web/gateway 前端：无 SGR 解码路径。

## 5. Steps（每步带验证）

1. 复制本计划到 `docs/plan/SUBAGENT_VIEW_WHEEL_SCROLL_RUNAWAY_PLAN.md`。验证：文件存在。
2. §3.1 守卫。验证：`CGO_ENABLED=1 go build ./pkg/tui` 无输出。
3. §3.3 测试。验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestWheelReleaseAndMotionReportsDoNotScroll|TestMouseEventFromSGRDropsNoButtonMotion' -count=1` → ok；
   反证：临时还原守卫后该测试红，再恢复。
4. 包级回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok；`go vet ./...` 无新增告警。
5. §6 真机验收（构建必须 `-tags fts5`）。

## 6. 真机验收（tmux，用 §2.2 同法重建）

```bash
D=.claude/skills/run-forebrain/driver.sh
.claude/skills/run-forebrain/driver.sh build   # 或 $D 内建
TXT=<40 句 "L%02d the quick brown fox..." 拼接>
ENABLE_SUBAGENT=1 $D start subagent-net "$TXT"
$D submit 'please run the probe'; sleep 6
$D click 10 21; sleep 1.2          # 打开 subagent 视图（行号以当屏为准）
for i in $(seq 1 20); do $D submit 'continue'; sleep 1.5; done
```

场景与预期（计数法：注入后逐档 `\e[<65;…M` 下滚至 "Jump to bottom" 消失，档数×3=行程）：

1. **复现→修复**：press+release 对 ×5 → 行程恰 15 行（修复前 30）；
   press+motion(96)×10+release ×1 → 行程恰 3 行（修复前 33）。
2. **回归（press-only 不变）**：单档 ×10 间隔 250ms → 每档恰 3 行；20 档 @10ms → 恰 60 行。
3. **视图切换/选择不回归**：Esc 回主视图再点卡片回 subagent 视图；左键 press-drag-release
   文本选择与点击展开卡片行为不变。
4. **owner 真机终验**（不可代替）：owner 终端里 subagent 视图滚轮轻滚 2-3 档即停，
   视图随停（此步依赖 owner 的真实终端事件形态，是本根因的最终证伪点）。

验收后 `$D stop`、`tmux kill-session -t forebrain-run`，清理临时 home（driver 自带隔离，不碰真实 `~/.forebrain*`）。

## 7. Done criteria（机器可查）

1. `grep -n "motion samples are not notches\|!ev.press || ev.button&0x20" pkg/tui/input_events.go` 命中守卫 1 处。
2. 新测试绿；反证（还原守卫）红；`TestMouseEventFromSGRDropsNoButtonMotion` 等既有测试保绿。
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 与 `go vet ./...` 全绿。
4. §6 场景 1/2/3 数值精确命中。
5. `git status` 改动面 = §4 In scope，未 commit。

## 8. STOP conditions

- 发现任何既有测试/代码**有意**依赖 wheel release 或 wheel 位 motion 产生滚动（当前 grep 未见；
  `grep -rn "wheel" pkg/tui/*_test.go` 复核）→ 停下汇报，不得擅自改语义。
- 修复后 press-only 注入行为有任何变化（不应发生）→ 停下重查。
- owner 真机复验仍过滚 → 该终端还在发别的形态事件；先在 `parseSGRMouse` 出口加临时输入
  记录仪（env 开关）取证一屏日志再议，不盲目扩改。
- 见到 `no such module: fts5` → 构建漏 `-tags fts5`，是工具链错误不是产品 bug。

## 9. Maintenance notes

- 本判定与 xterm SGR 协议语义一致：滚轮是瞬时按键，只有 press 一个报告；任何后续把
  motion/release 引入滚轮交互的设计（如"按住滚轮持续滚动"）须改这里并补协议出处注释。
- `dispatchMouse`（overlays.go:2937-2966）与主循环共享 `mouseEventFromSGR`，一处修两处得。
- 滚动手感类后续（动量取消、单档吞吐、平滑滚动）已有 §2.3 的排除性证据打底，另立计划时直接引用。

## 10. 验收回填（2026-10-08 实施后）

### 机器验证（全部通过）

| 检查 | 结果 |
|---|---|
| `CGO_ENABLED=1 go build ./pkg/tui` | 通过 |
| 定向测试 `TestWheelReleaseAndMotionReportsDoNotScroll` / `TestMouseEventFromSGRDropsNoButtonMotion` / `TestMouseWheelEventCarriesPointerPosition` | 全 PASS |
| 反证：临时把守卫反转为 `ev.press && ev.button&0x20 != 0` → 新测试红（release Cb=64 产生 `wheelDelta:-3` 滚动事件被捕获），恢复后绿 | 通过 |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok 40.776s |
| `go vet ./...` | 无输出 |
| Done criteria grep：守卫 1 处（input_events.go:748）、测试消息 1 处 | 命中 |

### 真机验收（tmux 120×40，`-tags fts5` 构建含修复，subagent-net + ENABLE_SUBAGENT=1，20 轮 continue 长 transcript，计数法同 §2.2/§6）

| 场景 | 预期 | 实测 | 判定 |
|---|---|---|---|
| 1a 复现→修复：press+release 对 ×5 | 恰 15 行（修复前 30） | 15 行（补档 N=15，3×(20−15)=15） | ✅ |
| 1b 复现→修复：press + motion(97)×10 + release ×1 | 恰 3 行（修复前 33） | 3 行（N=19） | ✅ |
| 2a press-only 回归：单档 ×10 @250ms | 恰 30 行 | 30 行（N=10） | ✅ |
| 2b press-only 回归：20 档 @10ms burst | 恰 60 行到底 | 到底（Jump to bottom 消失） | ✅ |
| 3 视图切换 | Esc 回主视图 → 点卡片重开 subagent 视图 | 往返成功（esc to return 0→1） | ✅ |
| 3 文本选择 | 左键 press-drag-release 仍出选择 | 拖拽 3 行精确高亮（48;5;24m ×3），单击清除 | ✅ |
| 3 点击交互 | 点击类不回归 | 任务卡点击开视图、Jump to bottom 按钮点击跳底均正常（本 transcript 无可折叠卡可展开，点击解码路径已经任务卡/按钮两路证实） | ✅ |
| 4 owner 真机终验 | owner 真实终端轻滚即停 | **待 owner 复验**（不可代替） | ⏳ |

### 实施偏差（计划本身的一处缺陷，已修正）

§3.3 计划测试直接构造 `sgrMouseEvent{button:64, press:true}` 漏了 `wheel`/`wheelUp` 字段：`mouseEventFromSGR` 以 `ev.wheel` 分发，缺字段时报告落入 drag 分支（首跑实测 FAIL：kind=drag、wheelDelta=0）。落地版按 `parseSGRMouse` 产物补齐 `wheel:true` + 按方向的 `wheelUp`，断言不变。
