# Plan: TUI subagent 卡（check task 等）卡头颜色跟随调用结果——失败红、取消灰

> **Executor instructions**: 按步骤顺序执行，每步先跑验证命令并确认预期结果再进下一步；
> 命中「STOP conditions」任一条立即停下报告，不要即兴发挥。**不要 commit**（owner 规则
> no-git-commits），改动留工作区由 owner 审阅。owner 裁决：**直接在当前项目源码上实施，
> 不用 git worktree、不建分支不切分支**。
>
> **审批后的交接（2026-11-25，owner 裁决，覆盖上面两条 git 约定）**：owner 把本计划交给
> `alibaba/deepseek-v4.1-flash-1017` 的执行器在**隔离 worktree** 里实施并在其自有分支上提交，
> 评审方独立复跑后给结论；**是否合并回 dev 由 owner 决定**。结果见文末「Implementation Status」。
>
> **Drift check（先跑，无条件）**：
> `git rev-parse --short HEAD`（计划时 `364a11a2`，分支 `dev`）；
> `git diff 364a11a2 -- pkg/tui/reducer.go pkg/tui/render.go pkg/tui/reducer_test.go`
> （计划时为空。同一目录树有其他 agent 并行工作〔7a / fd / a7 / 4c〕，他们的改动可能已提交也可能还在
> 工作区，所以不看 porcelain、直接与基线比）。有 diff 时逐个 hunk 核对：**本计划引用的五处代码
> （§2.1 的 ④⑤、§3.1 的三处插入点）必须与下文摘录逐字一致**——行号漂移不算，内容不一致才是 STOP。
> 行号漂了用下面的锚重定位：
>
> ```bash
> grep -n 'FanoutCallError string' pkg/tui/reducer.go                      # §3.1(1) 插入点之后
> grep -n 'FanoutCallError:  fanoutDrawnCallError(fs),' pkg/tui/reducer.go   # §3.1(3) 插入点之后
> grep -n 'func fanoutCardPhase' pkg/tui/reducer.go                           # §3.1(2) 新函数放它后面
> grep -n 'func fanoutHeaderLine' pkg/tui/render.go                           # §3.1(4)
> grep -n 'func TestFailedDispatchCardSaysWhyOnceOnTheTaskRow' pkg/tui/reducer_test.go  # 测试放它后面
> ```

## Status

- **Status**: DONE（已实施并真机验收通过，评审 APPROVE，2026-11-25；**已按 owner 指令 ff-only 落 dev
  `c3176812..c237bec1`，落 dev 门禁复跑绿（gofmt/vet 净、定点 6/6、全包 `pkg/tui` `ok 200.361s`，
  `pkg/architecture` 仅预存红 `TestTestFilesCorrespondToProductionFiles`／`deliverables_outcome_test.go`，
  与基线一致）**。改动原在 worktree `.worktrees/tui-subagent-card-failed-header-red`／分支同名 @`c237bec1`，
  基于 dev `c3176812`；未 push。）
- **Priority**: P2（owner 截图报障，2026-11-25）
- **Effort**: S（生产代码约 30 行 + 1 个回归测试）
- **Risk**: LOW（只动 TUI 一个卡头着色分支与 Frame 的一个字段，无数据/权限/公共契约变化）
- **Category**: bug（TUI 渲染）
- **Planned at**: commit `364a11a2`，分支 `dev`，2026-11-25
- **Depends on**: none
- **落盘编号**: 384（2026-11-25 落盘时 382 已占、383 由 7a 的计划预留，故取 384）

## Owner rulings（2026-11-25，本任务）

1. **失败 → 红**（原始报障）：subagent 卡调用失败时卡头文案红色，与其他工具调用失败的消息卡一致。
2. **取消 → 灰**（计划评审后追问，owner 选「一并对齐成灰色 244」）：被取消的 subagent 卡卡头对齐普通
   被取消工具卡的灰色 244。只对齐**颜色**；标记字形不动（见 §3.3）。
3. **任务级失败不变红**（owner 选「保持琥珀」）：调用成功、个别子任务失败的卡（`Ran 2 tasks · 1 failed`）
   保持琥珀——红色只表示「这次调用自己失败」。

## 1. 目标（Why this matters）

owner 原话（2026-11-25，附截图 `.forebrain/attachments/forebrain-clipboard-31531-1839451773.png`）：

> forebrain harness tui里的check task的失败消息卡卡头的文案颜色跟成功完成的消息卡卡头文案颜色一样，没有区分，失败的消息卡卡头文案颜色应该是红色，与其他工具调用失败的消息卡卡头文案颜色保持一致

截图里的卡（琥珀色卡头 + 白色错误行）：

```
● Failed to check 1 task 0.2s
    tool subagent_status: subagent not found
```

"Failed to check 1 task" 与成功时的 "Checked 1 task" 同为琥珀色，一眼分不出成败；而普通工具卡
（read_file / shell / …）失败时卡头是红色。

## 2. 根因（file:line 证据链 + 复现证据）

### 2.1 因果链（每一环都已对照复现输出核实）

1. **工具返回错误** — `pkg/run/subagent.go:4009-4011`：`subagent_status` 查不到 id 时
   `return "", fmt.Errorf("subagent not found")`；agent 循环在 `pkg/run/orchestration_llm.go:2336`
   包成 `tool %s: %w` → `tool subagent_status: subagent not found`。
2. **步骤状态 = failed** — `pkg/tool/format.go:239-246` `BuildToolMeta` 把带 `Error` 的步骤标成
   `Status: "failed"`，并挂上 `SubagentCall`（verb `status`，1 个 task，`format.go:3194-3195`）。
3. **reducer 认出了失败** — `pkg/tui/reducer.go:1042-1050`
   `applySubagentCallCompletion`：`status == "failed"` → `fs.CallError = normalizeFailedToolContent(content)`；
   `reducer.go:1205-1211` `fanoutCardPhase`：`CallError != ""` → `"failed"`；
   `reducer.go:1324-1332` → 卡头文案 `"Failed to check " + count`。**文案是对的。**
4. **Frame 不携带调用结果（断点①）** — `reducer.go:1155-1168` `emitFanoutFrame` 只输出
   `Summary` / `FanoutCallError` / `Final: fanoutCardSettled(fs)`，`ToolMeta` 是零值
   （复现日志：`ToolMeta.Status=""`），没有任何字段说明「这次调用失败/被取消了」：

   ```go
   	return Frame{
   		Kind:             FrameFanout,
   		StepID:           fs.StepID,
   		RunID:            r.activeRunID,
   		AgentID:          fs.OwnerAgentID,
   		Final:            fanoutCardSettled(fs),
   		Content:          content,
   		FanoutLineAgents: lineAgents,
   		FanoutLineClocks: clocks,
   		Summary:          fanoutCardHeader(fs),
   		Duration:         duration,
   		FanoutQuery:      fs.Query,
   		FanoutCallError:  fanoutDrawnCallError(fs),
   	}
   ```

5. **卡头着色无视结果（断点②，直接原因）** — `pkg/tui/render.go:5620-5628`
   `fanoutHeaderLine`：只要 `f.Final`，一律 `ordinaryToolHeaderColor`（214 琥珀）：

   ```go
   func fanoutHeaderLine(f Frame, summary string, phase int, theme DiffTheme) string {
   	if f.Final || noColorActive() {
   		marker := "○"
   		if f.Final {
   			marker = "●"
   		}
   		style := lipgloss.NewStyle().Foreground(lipgloss.Color(ordinaryToolHeaderColor)).Bold(true)
   		return style.Render(marker) + " " + style.Render(summary)
   	}
   ```

   而普通工具卡走 `toolHeaderColor`（`render.go:2646-2653`）：失败/拒绝 → `"203"` 红；
   运行中/被取消 → `"244"` 灰。subagent 卡根本不经过这个函数。

这是仓库里典型的「同一语义多处存在、唯独出错路径没用它」：「卡头颜色跟随调用状态」这一语义已由
`toolHeaderColor` 承载，fanout 卡头没有接入。

### 2.2 复现（已跑，改代码之前；评审方独立复跑结论一致）

用 `go test -overlay` 把一个临时测试挂进 `pkg/tui`（不写源码树；测试源码见附录 A）：

```bash
D=$(mktemp -d /tmp/fbrepro.XXXXXX); cp <附录A> $D/zz_repro_fanout_color_test.go
echo "{\"Replace\": {\"$PWD/pkg/tui/zz_repro_fanout_color_test.go\": \"$D/zz_repro_fanout_color_test.go\"}}" > $D/overlay.json
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -overlay $D/overlay.json ./pkg/tui \
  -run 'TestReproFailedSubagentCallHeaderIsRed' -count=1 -v
```

实际输出（修复前，live 与 replay 两种到达顺序一致，stream 渲染器与 viewport 画出的字节相同）：

```
frame: Final=true Summary="Failed to check 1 task" FanoutCallError="tool subagent_status: subagent not found" ToolMeta.Status=""
stream raw:
    ^[[1;38;5;214m●^[[0m ^[[1;38;5;214mFailed to check 1 task 0.2s^[[0m
        tool subagent_status: subagent not found
header row SGR states: map[:true 1;38;5;214:true]
    --- FAIL: TestReproFailedSubagentCallHeaderIsRed/live  (failed subagent card header is NOT red)
    --- FAIL: TestReproFailedSubagentCallHeaderIsRed/replay  (同上)
FAIL
```

与 owner 截图逐字一致。`NO_COLOR` 未设置（排除了无色假设）；stream 与 viewport 同色
（排除了「viewport 重绘丢色」假设）；`Summary` 已是 "Failed to check"（排除了「没识别出失败」假设）。

### 2.3 同一缺陷的其他路径（一个修复全覆盖）

所有走 `emitFanoutFrame` → `fanoutHeaderLine` 的卡共用这条着色路径，失败态全都是琥珀色：
`subagent_send`（Failed to start … in background）、`subagent_continue`、`subagent_status`、
`subagent_wait`、`subagent_close`（Failed to stop …）、`subagent_list`（Failed to list tasks）、
`subagent_run` / `subagent_fanout`（Failed to run …；含「agent 自己失败、原因写在任务行」的卡，
其 `FanoutCallError` 为空，见 `reducer.go:1179-1189`）、plan-review 卡（Failed to start 1 plan-reviewer task，
`reducer.go:2084-2096`）。被取消态同理全为琥珀（`CallStatus == "canceled"` → `fanoutCardPhase` →
`"canceled"` → 文案 `"Canceled " + fanoutCanceledLabel(fs)`；来源有三：完成消息 status=canceled
`reducer.go:1046-1047`、run/agent 结束时在途查询卡 `cancelInFlightQueryCards` `reducer.go:1468-1486`、
plan-review 被停 `reducer.go:2078-2083`）。
`fanoutHeaderLine` 只有 `render.go:5539` 一个调用点，`FrameFanout` 只有 `reducer.go:1156` 一个构造点
（均 grep 核实；评审方独立核实一致）。fanout 卡不可折叠，没有 fold 卡头旁路。

## 3. 修复设计

一个事实、一个载体：**卡所属的调用是怎么结束的**（failed / canceled / 无），由 `emitFanoutFrame`
从唯一的相位定义（`fanoutCardPhase`）写到 Frame 上，渲染器只读不算。词表沿用 `ToolMeta.Status`
与 `fanoutState.CallStatus` 已在用的 `"failed"` / `"canceled"`。

### 3.1 改动点（共 4 处生产代码 + 1 个测试）

**(1) `pkg/tui/reducer.go` — `Frame` 结构体，`FanoutCallError string`（L128-131）之后新增字段：**

```go
	// FanoutCallStatus is how the card's own call ended — "failed", "canceled",
	// or "" while it is in flight or ended well — in the vocabulary a tool
	// frame's ToolMeta.Status uses, so the header takes the colour
	// toolHeaderColor gives a tool card in the same state. The header's words
	// already say it ("Failed to check 1 task"); this is what lets the renderer
	// paint them without reading its own text back. It is not FanoutCallError:
	// a dispatch whose agent failed says why on its task row and draws no call
	// error, and its head is a failed one all the same.
	FanoutCallStatus string
```

（插入后 gofmt 会把紧随其后的 `Summary` / `ToolMeta` 两行的对齐各加一个空格——Step 2 的 `gofmt -w`
负责，属预期 diff。）

**(2) `pkg/tui/reducer.go` — `fanoutCardPhase`（L1205-1228）之后新增函数：**

```go
// fanoutCallStatus is how the card's own call ended, in the vocabulary a tool
// frame's ToolMeta.Status uses: "failed", "canceled", or "" while the call is
// still in flight or ended well. It rides the Frame so the header can take the
// colour toolHeaderColor gives a tool card in the same state.
func fanoutCallStatus(fs *fanoutState) string {
	switch fanoutCardPhase(fs) {
	case "failed":
		return "failed"
	case "canceled":
		return "canceled"
	}
	return ""
}
```

**(3) `pkg/tui/reducer.go` — `emitFanoutFrame`（L1155-1168），`FanoutCallError:` 一行之后新增**
（`FanoutCallStatus:` 与 `FanoutLineAgents:` 等长，单空格即对齐）：

```go
		FanoutCallStatus: fanoutCallStatus(fs),
```

**(4) `pkg/tui/render.go` — `fanoutHeaderLine`（L5613-5628）：**

```go
// fanoutHeaderLine is a subagent card's header row: its marker and summary. A
// settled card wears the amber every finished tool card wears — except a card
// whose call failed (red) or was canceled (gray), which wear what
// toolHeaderColor gives a tool card in the same state. A card whose agents are
// still at work has its summary in a steady orchid nothing else in the
// transcript uses, and its dot breathes, so a background agent reads as alive
// at a glance rather than as one more finished call. Without colour the running
// card keeps its hollow marker: the breath is all colour, and the glyph is what
// is left to tell the two apart.
func fanoutHeaderLine(f Frame, summary string, phase int, theme DiffTheme) string {
	if f.Final || noColorActive() {
		marker := "○"
		if f.Final {
			marker = "●"
		}
		color := ordinaryToolHeaderColor
		switch f.FanoutCallStatus {
		case "failed":
			// "Failed to check 1 task" in the settled amber read as one more
			// finished call; 203 is the red toolHeaderColor gives a failed one.
			color = "203"
		case "canceled":
			// The gray of a canceled tool card (toolHeaderColor), owner 2026-11-25.
			color = "244"
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
		return style.Render(marker) + " " + style.Render(summary)
	}
	// ……以下（运行态 orchid 呼吸点）原样不动
```

`"203"` / `"244"` 用字面量是沿用 `toolHeaderColor` / `headerColorForFrame` / `compactMarker` 的现有写法
（仓库没有命名的状态色常量）。

### 3.2 权衡过的替代方案（均不采用）

| 方案 | 不采用原因 |
|---|---|
| A. 用现有 `FanoutCallError != ""` 当失败信号 | `fanoutDrawnCallError`（`reducer.go:1179-1189`）对「任务行已写明原因」的派发卡会把该字段置空，这类 "Failed to run 1 … task" 会漏修；也表达不了「被取消」 |
| B. 给 fanout Frame 设 `ToolMeta.Status="failed"` 复用 `toolStatusFailed`/`toolHeaderColor` | 两个判定都带 `f.Kind == FrameTool`（`render.go:2560-2562`、`2607`），要放宽 Kind 检查，会改变 `shellFrameHasDiffBlocks`/`statusMarker`/`renderCompactFrame` 等全部调用方语义 |
| C. 从卡头文案解析 "Failed to" / "Canceled" | 文案不是状态；改词即坏 |
| D. 两个 bool（`FanoutCallFailed` / `FanoutCallCanceled`） | 同一事实拆成两个字段；字符串状态沿用既有词表，一个字段说清「怎么结束的」 |

### 3.3 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| `subagent_status/wait/close/continue/list` 调用失败（错误行画在卡头下） | 琥珀 `1;38;5;214` | **红 `1;38;5;203`** | 刻意修订（裁决 1） |
| `subagent_send/run/fanout` 调用失败（含 agent 失败、原因只在任务行） | 琥珀 | **红** | 刻意修订（同一根因） |
| plan-review 请求失败（Failed to start 1 plan-reviewer task） | 琥珀 | **红** | 刻意修订（同一根因） |
| 调用成功的 subagent 卡（Checked / Ran / Listed …，含「调用成功但个别任务失败」的 `Ran 2 tasks · 1 failed`） | 琥珀 | 琥珀（不变） | 裁决 3：调用本身成功 |
| 被取消的 subagent 卡（Canceled check … / Canceled wait for … / Canceled review the plan；来源三处见 §2.3） | 琥珀 | **灰 `1;38;5;244`** | 刻意修订（裁决 2）；标记字形保持 `●`（只对齐颜色） |
| 运行中的 subagent 卡 | orchid 卡头 + 呼吸点 | 不变 | — |
| `NO_COLOR` | 无颜色字节 | 不变 | — |
| live 绘制 vs resume 回放 | 一致 | 一致（回放走同一 reducer 路径） | 测试覆盖两种到达顺序 |

非功能：无新增等待/IO/分配热点（Frame 多一个 string；每次发卡多算一次 `fanoutCardPhase`，
`fanoutCardSettled` 本来就在算）；无并发/资源变化；NO_COLOR 行为不变。

## 4. Scope

**In scope（只改这些）**

- `pkg/tui/reducer.go`：`Frame` 新增 `FanoutCallStatus`；新增 `fanoutCallStatus(fs)`；`emitFanoutFrame` 赋值。
- `pkg/tui/render.go`：`fanoutHeaderLine` 的失败/取消着色 + 该函数文档注释。
- `pkg/tui/reducer_test.go`：在 `TestFailedDispatchCardSaysWhyOnceOnTheTaskRow`（L2111-2152）之后新增
  `TestSubagentCardHeaderColourFollowsItsCallStatus`。
- 收尾：`plans/NNN-tui-subagent-card-header-call-status-colour.md` + `plans/README.md` 两处定点插入
  （顶部「最新审核信息」一条、计划表 382 行之后一行；agent 4c 已确认互不冲突）。

**Out of scope（看起来相关但明确不碰）**

- `pkg/tool/display_header.go`、`pkg/tool/header_parts.go`（引擎文案层）——文案已正确。
- `frontend/src/components/chat/SubagentCallCard.vue`（web 端）——owner 只报 TUI；web 端卡头文字
  各状态同色、靠机器人图标变红 + 错误行红色表达失败，是另一套设计。
- 被取消 subagent 卡的**标记字形**（现为 `●`；普通被取消工具卡是 `○`）——owner 裁决只说对齐颜色。
- 失败卡下方的错误行颜色（现为正文白）——普通失败工具卡的正文同样是正文色，保持一致。
- `toolHeaderColor` / `toolStatusFailed` 的 Kind 判定（方案 B 已否）。

## 5. Steps

环境变量（所有 `go` 命令前）：
`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`。

### Step −1：批准后的第一件事——计划落盘（动源码之前）

`ls plans | grep -E '^38[0-9]-'` 取当时未被占用的下一个号 NNN（计划时 382 已占、383 疑被 7a 预留），
把本计划全文写到 `plans/NNN-tui-subagent-card-header-call-status-colour.md`（Status 改 **IN PROGRESS**），
并在 `plans/README.md` 两处定点插入（顶部「最新审核信息」一条；计划表 382 行之后一行，
状态列 IN PROGRESS）。创建后 `head -3 plans/NNN-…md` 确认是自己刚写的内容（防撞号覆盖他人文件）。

### Step 0：真机复现（改代码之前，当前二进制）

```bash
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK="$TMPDIR/forebrain-run-cardcolor" FOREBRAIN_RUN_SESSION=fb-cardcolor FOREBRAIN_RUN_PORT=8766
$D reset && $D build
ENABLE_SUBAGENT=1 $D start tool '[{"name":"subagent_status","arguments":{"task_id":"nope"}}]'
$D submit 'check the status of subagent task nope'
$D wait 'Failed to check' 30
tmux capture-pane -t fb-cardcolor -e -p | grep -a 'Failed to check' | cat -v | tee "$FOREBRAIN_RUN_WORK/before.txt"
$D screen | tee "$FOREBRAIN_RUN_WORK/before-screen.txt"
$D stop
```

注意 `tool` 模式只有两个参数：第二参是工具调用 JSON 脚本，**没有第三参**（第三参是 drip 延迟秒数，
传文本会让 fake provider 以 `ValueError: could not convert string to float` 起不来——评审方实跑证实）；
工具结果回来后的那次请求，provider 固定回 `done`。

预期：`before.txt` 那行含 `^[[1;38;5;214m●` 与 `^[[1;38;5;214mFailed to check 1 task`（琥珀），
`before-screen.txt` 里下一行是 `tool subagent_status: subagent not found`。
若屏幕出现的是普通工具卡（如 `unknown tool` / `subagent capacity is 0`）而不是 subagent 卡 → STOP
（验收环境问题：subagent 工具未启用）。
若因其他 agent 的未提交改动导致主树编不过：按 run-forebrain skill 的 `go build -overlay` 配方，
把那些文件的 HEAD 版本覆盖进去构建，**不要回退别人的改动**。

### Step 1：先落地回归测试（红）

在 `pkg/tui/reducer_test.go` 的 `TestFailedDispatchCardSaysWhyOnceOnTheTaskRow` 之后新增
`TestSubagentCardHeaderColourFollowsItsCallStatus`（完整代码见附录 B），然后：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentCardHeaderColourFollowsItsCallStatus' -count=1 -v 2>&1 | tail -30
```

预期：**编译失败**（`f.FanoutCallStatus undefined`）——这是红灯信号。行为级红灯由附录 A 的 overlay
测试给出（修复前 FAIL，见 §2.2）。确认测试引用的助手都存在
（`forceColorProfile`、`rowColumnStates`、`lastFanoutFrame`、`renderViewport`、`subagentStepMessage`、`stripANSI`）。

### Step 2：生产代码（§3.1 的 (1)(2)(3)(4)）

按 §3.1 逐字插入四处代码，然后：

```bash
gofmt -w pkg/tui/reducer.go pkg/tui/render.go pkg/tui/reducer_test.go
gofmt -l pkg/tui                                   # 预期：无输出
git diff --stat -- pkg/tui                         # 预期：仅 reducer.go / render.go / reducer_test.go（+ 其他 agent 的文件，逐一确认非本计划）
git diff -- pkg/tui/reducer.go pkg/tui/render.go   # 通读：除 gofmt 对齐（Summary/ToolMeta 各多一空格）外，每一行都在 §3.1 里
```

### Step 3：定点测试（绿）+ 计数门

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestSubagentCardHeaderColourFollowsItsCallStatus|TestFanoutCardShowsWhyTheCallFailed|TestFailedDispatchCardSaysWhyOnceOnTheTaskRow|TestReducerFanoutFailedDispatchSettlesWaitingTasksBothOrders|TestFanoutHeaderIncludesExecutorDurationOnlyWhenFinal|TestInterruptedWaitCardSettlesAsCanceled' \
  -count=1 -v > "$TMPDIR/tui-focused.log" 2>&1; echo "exit=$?"
grep -c '^--- PASS' "$TMPDIR/tui-focused.log"      # 预期：6（顶层测试数；子测试行有缩进不计）
grep -c '^--- FAIL\|^FAIL' "$TMPDIR/tui-focused.log"   # 预期：0
grep '^    --- ' "$TMPDIR/tui-focused.log"         # 预期：新测试 5 个子用例全 PASS
```

预期：顶层 `--- PASS` 共 **6** 条，0 FAIL；新测试 5 个子用例
（status failed live / replay、agent failed、call canceled、call completed）全 PASS。
附录 A 的 overlay 复现测试此时应转绿（Step 0 同一命令重跑，`--- PASS`）。

### Step 4：包级回归 + vet

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 2>&1 | tail -5
CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui
```

预期：`ok  github.com/forebrain-harness/forebrain-harness/pkg/tui`（评审方在 what-if 副本上实跑全包 196s ok）；
vet 无输出。`pkg/tui` 里若有失败落在**我没碰的文件**（其他 agent 正在改的 overlays.go / composer 等）→ 不修，
`peer_send` 通知对应 agent，并在报告里单列。

### Step 5：真机验收（tmux + fake provider，重建后）

```bash
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK="$TMPDIR/forebrain-run-cardcolor" FOREBRAIN_RUN_SESSION=fb-cardcolor FOREBRAIN_RUN_PORT=8766
$D stop; $D reset; $D build      # 必须重建：旧二进制会让修复看起来没生效；build 自带 -tags fts5
# 场景 1（复现→修复）：失败的 subagent_status
ENABLE_SUBAGENT=1 $D start tool '[{"name":"subagent_status","arguments":{"task_id":"nope"}}]'
$D submit 'check the status of subagent task nope'
$D wait 'Failed to check' 30
tmux capture-pane -t fb-cardcolor -e -p | grep -a 'Failed to check' | cat -v | tee "$FOREBRAIN_RUN_WORK/after.txt"
$D screen | tee "$FOREBRAIN_RUN_WORK/after-screen.txt"     # 整屏文本留证
# 场景 2（回归）：resume 回放同一会话，卡仍为红色
SID=$($D sid); $D stop
RESUME_ID=$SID ENABLE_SUBAGENT=1 $D start reply 'ok'
$D wait 'Failed to check' 30
tmux capture-pane -t fb-cardcolor -e -p | grep -a 'Failed to check' | cat -v | tee "$FOREBRAIN_RUN_WORK/replay.txt"
$D stop; $D reset
# 场景 3（回归）：成功的卡仍是琥珀（独立会话：tool 模式只给第一个 turn 请求发工具调用）
ENABLE_SUBAGENT=1 $D start tool '[{"name":"subagent_list","arguments":{}}]'
$D submit 'list the subagents'
$D wait 'Listed' 30
tmux capture-pane -t fb-cardcolor -e -p | grep -a 'Listed' | cat -v | tail -2 | tee "$FOREBRAIN_RUN_WORK/done.txt"
$D stop
```

预期：
- 场景 1 `after.txt`：`^[[1;38;5;203m●^[[0m ^[[1;38;5;203mFailed to check 1 task`，整行不含 `38;5;214`；
  `after-screen.txt` 的下一行是 `tool subagent_status: subagent not found`。
- 场景 2 `replay.txt`：与 `after.txt` 同为 `38;5;203`（回放走同一 reducer 路径）。
- 场景 3 `done.txt`：`Listed 0 tasks` 行是 `1;38;5;214`（成功卡不受影响）。

**被取消卡的证据边界（如实报告，不硬凑）**：fake provider 现有场景造不出「被中断的 subagent 查询卡」
（需要一个真实在跑的子代理让 `subagent_wait` 阻塞，再按 Esc；`tool` 模式下发出的子代理会立刻拿到
`done` 文本结束）。被取消卡的灰色以附录 B 的 `call canceled` 子用例为证——它走真实 reducer
（`RunStartedMsg` → wait started → `RunEndedMsg` → `cancelInFlightQueryCards`）与真实 viewport painter，
断言的是 SGR 字节 `1;38;5;244`。真机手工复现路径写进报告供 owner 自验：有子代理在跑时对其
`subagent_wait` 按 Esc，卡头 `Canceled wait for …` 应为灰色。

技能模板里的「中断自救」「非 TTY 行为」两类场景与本次改动无关（纯着色分支，不新增等待/路径），
不单独验收；`$WORK/tui.err` 为空或仅有既有告警、屏幕无 panic 文本即视为干净退出。

### Step 6：收口

1. `plans/NNN-tui-subagent-card-header-call-status-colour.md`（Step −1 已落盘）：
   Status 行改「已实施并真机验收通过（日期）」，追加「验收记录」（场景 → 证据，贴 before/after/replay/done
   四行 + 被取消卡的证据边界说明）。
2. `plans/README.md`：顶部那条与计划表那一行的状态列改为 DONE（验收日期）。只做定点编辑。
3. 清理：`$D stop`；`rm -rf "$FOREBRAIN_RUN_WORK"`；`/tmp/fbrepro.*` 临时目录。
4. 报告：根因链、改动清单、验证证据、证据边界（被取消卡仅单元级真 painter 证据）。

## 6. Done criteria（机器可查）

- [ ] 本计划改动的源码文件恰好是 `pkg/tui/reducer.go`、`pkg/tui/render.go`、`pkg/tui/reducer_test.go`
      三个（`git diff --stat -- pkg/tui` 里若还有别的文件，逐一确认是并行 agent 的改动、不是本计划所改）。
- [ ] `gofmt -l pkg/tui` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` 无输出。
- [ ] Step 3：`grep -c '^--- PASS'` = 6，FAIL = 0，新测试 5 子用例 PASS；附录 A overlay 测试转 PASS。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（或失败全在我未触碰的文件，且已通知对应 agent）。
- [ ] 真机：`after.txt`、`replay.txt` 含 `38;5;203` 且不含 `38;5;214`；`before.txt` 含 `38;5;214`；
      成功卡 `Listed 0 tasks` 行含 `38;5;214`。
- [ ] `plans/` 计划文件落盘、README 索引两处同步为 DONE；改动留工作区未 commit。

## 7. STOP conditions

1. **Drift**：§3.1 四个插入点 / §2.1 ④⑤ 的摘录与现场内容不一致（非行号漂移）→ 停，重新对锚后再动。
2. **前提不成立**：Step 0 真机上卡头已是红色，或附录 A 测试在修复前就 PASS → 停，根因判断需重做。
3. **验收环境**：Step 0/5 屏幕出现的是普通工具卡（`unknown tool` / `subagent capacity is 0`）而非 subagent 卡，
   或 provider 报 `never bound port`（多半是 `tool` 模式误传了第三参）→ 停，这是 fake provider 配置问题，
   不是产品 bug，不要去改产品代码凑验收。
4. **越界**：修复需要动 `pkg/tool`、`frontend/`、web 端组件，或需要改 `toolStatusFailed`/`toolHeaderColor` 的
   Kind 判定 → 停，回 owner。
5. **红在我没碰的文件**：`./pkg/tui` 包级回归的失败落点不在三个改动文件 → 不修，通知对应 agent 并单列报告。
6. Step 3 计数门 ≠ 6 或同一验证连续两次失败 → 停，先诊断再继续，不盲目重跑。

## 8. Maintenance notes

- 状态色 `203` / `244` 目前是字面量约定（`toolHeaderColor`、`headerColorForFrame`、`compactMarker` 同写法）；
  若将来引入命名常量，连同本处一起换。
- 「调用成功但个别任务失败」的卡（`Ran 2 tasks · 1 failed`）相位是 `done`，保持琥珀——红只表示
  **这次调用自己**失败了（owner 裁决 3）；评审者要看的是 `fanoutCardPhase` 的定义，别从文案反推。
- `FanoutCallStatus` 的取值词表 = `ToolMeta.Status` 的失败/取消两个值；将来若 fanout 卡还要区分
  `denied`，在 `fanoutCallStatus` 与 `fanoutHeaderLine` 的 switch 各加一例，不要另起字段。
- 被取消卡的标记仍是 `●`（普通被取消工具卡是 `○`）——owner 这次只要求对齐颜色；要对齐字形在
  `fanoutHeaderLine` 的 `f.Final` 分支里按 `FanoutCallStatus == "canceled"` 换 `○` 即可。
- web 端 `SubagentCallCard.vue` 用 `headerKey.endsWith('.failed')` 把机器人图标变红、错误行红色，
  卡头文字各状态同色——与 TUI 约定不同，是有意的两端差异，不要为了「一致」去改 web。
- 新测试依赖 `forceColorProfile`（进程级 lipgloss profile，不可 `t.Parallel`）与 `rowColumnStates`
  （逐 cell 的 SGR 状态）；断言的是字节不是肉眼。

---

## 附录 A：复现测试（overlay 用，不进源码树；修复后应当转绿）

```go
package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func TestReproFailedSubagentCallHeaderIsRed(t *testing.T) {
	forceColorProfile(t)
	input := map[string]any{"task_id": "nope"}
	const why = "tool subagent_status: subagent not found"
	cases := []struct {
		name string
		msgs []Message
	}{
		{"live", []Message{
			subagentStepMessage("call-status", tool.StepEvent{Kind: tool.StepKindToolStarted, ToolName: "subagent_status", Input: input}),
			subagentStepMessage("call-status", tool.StepEvent{Kind: tool.StepKindToolCompleted, ToolName: "subagent_status", Input: input, Error: why, Duration: 200 * time.Millisecond}),
		}},
		{"replay", []Message{
			subagentStepMessage("call-status", tool.StepEvent{Kind: tool.StepKindToolCompleted, ToolName: "subagent_status", Input: input, Error: why, Duration: 200 * time.Millisecond}),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r Reducer
			var card Frame
			for _, m := range tc.msgs {
				for _, f := range r.Reduce(NewMessageMsg{Msg: m}).Frames {
					if f.Kind == FrameFanout {
						card = f
					}
				}
			}
			if card.Kind != FrameFanout {
				t.Fatalf("no fanout card: %#v", card)
			}
			if card.Summary != "Failed to check 1 task" || !card.Final {
				t.Fatalf("frame is not the failed card: %q final=%v", card.Summary, card.Final)
			}
			var buf bytes.Buffer
			NewRenderer(&buf, &buf).renderFanout(card)
			t.Logf("stream raw:\n%s", buf.String())
			vm := viewModel{}
			vm.append(card)
			vr := renderViewport(&vm, 120, 42, 0, DiffThemeDark)
			for _, row := range vr.lines {
				if !strings.Contains(stripANSI(row), "Failed to check 1 task") {
					continue
				}
				cells, _ := rowColumnStates(row, "")
				seen := map[string]bool{}
				for _, c := range cells {
					seen[c] = true
				}
				t.Logf("header row SGR states: %v", seen)
				if !strings.Contains(row, "38;5;203") {
					t.Fatalf("failed subagent card header is NOT red: %q", row)
				}
				if strings.Contains(row, "38;5;214") {
					t.Fatalf("failed subagent card header still wears the settled amber: %q", row)
				}
				return
			}
			t.Fatalf("header row not found in viewport output")
		})
	}
}
```

## 附录 B：回归测试 `TestSubagentCardHeaderColourFollowsItsCallStatus`（放进 `pkg/tui/reducer_test.go`）

```go
// A subagent card's header follows how its own call ended, the way a tool
// card's follows its status (toolHeaderColor): failed is red, canceled is
// gray, a call that ended well keeps the amber. "Failed to check 1 task" in
// amber read as one more finished call (owner 2026-11-25, who also asked for
// the canceled card to match a canceled tool card's gray). The colour follows
// the frame's own status, never the drawn call error — a dispatch whose agent
// failed says why on its task row and draws no call error, and its head is red
// all the same. Live reduces the started and the completed message; a replay
// reduces the completed row alone.
func TestSubagentCardHeaderColourFollowsItsCallStatus(t *testing.T) {
	forceColorProfile(t)
	statusInput := map[string]any{"task_id": "nope"}
	const notFound = "tool subagent_status: subagent not found"
	statusDone := func(err string) Message {
		return subagentStepMessage("status", tool.StepEvent{
			Kind: tool.StepKindToolCompleted, ToolName: "subagent_status", Input: statusInput, Error: err,
			Duration: 200 * time.Millisecond,
		})
	}
	runInput := map[string]any{
		"title": "执行计划 137 shell 正文", "task": "执行 plans/137",
		"subagent_type": "general-purpose",
	}
	const described = "The usage limit has been reached (HTTP 429 Too Many Requests)"
	const raw = `tool subagent_run: error, status code: 429`
	agentFailed := func(t *testing.T) Frame {
		t0 := time.Unix(1790000000, 0).UTC()
		var r Reducer
		_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("run", tool.StepEvent{
			Kind: tool.StepKindToolStarted, ToolName: "subagent_run", Input: runInput,
		})})
		_ = r.Reduce(SubagentSpawnedMsg{
			AgentID: "subagent-137", AgentType: "general-purpose", TaskID: "subagent-137",
			Title: "执行计划 137 shell 正文", Task: "执行 plans/137",
			ParentToolCallID: "run", TaskIndex: 0, ExecutionID: "exec-137", Timestamp: t0,
		})
		_ = r.Reduce(SubagentEndedMsg{
			AgentID: "subagent-137", AgentType: "general-purpose", TaskID: "subagent-137",
			Status: "failed", Error: described,
			ParentToolCallID: "run", TaskIndex: 0, ExecutionID: "exec-137",
			FinishedAt: t0.Add(754 * time.Second), Timestamp: t0.Add(754 * time.Second),
		})
		return lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: subagentStepMessage("run", tool.StepEvent{
			Kind: tool.StepKindToolCompleted, ToolName: "subagent_run", Input: runInput, Error: raw,
			Duration: 754 * time.Second,
		})}))
	}
	cases := []struct {
		name   string
		status string
		// header is the text the head must start with; the canceled label
		// names a task the started row never titled, so only its verb is pinned.
		header string
		sgr    string
		card   func(t *testing.T) Frame
	}{
		{"status failed, live", "failed", "Failed to check 1 task", "1;38;5;203", func(t *testing.T) Frame {
			var r Reducer
			_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("status", tool.StepEvent{
				Kind: tool.StepKindToolStarted, ToolName: "subagent_status", Input: statusInput,
			})})
			return lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: statusDone(notFound)}))
		}},
		{"status failed, replay", "failed", "Failed to check 1 task", "1;38;5;203", func(t *testing.T) Frame {
			var r Reducer
			return lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: statusDone(notFound)}))
		}},
		{"agent failed, reason on the row", "failed", "Failed to run 1 general-purpose task", "1;38;5;203", agentFailed},
		{"call canceled", "canceled", "Canceled ", "1;38;5;244", func(t *testing.T) Frame {
			var r Reducer
			_ = r.Reduce(RunStartedMsg{RunID: "run-c"})
			_ = r.Reduce(NewMessageMsg{Msg: subagentStepMessage("wait-c", tool.StepEvent{
				Kind: tool.StepKindToolStarted, ToolName: "subagent_wait",
				Input: map[string]any{"task_id": "subagent-6e5c"},
			})})
			return lastFanoutFrame(t, r.Reduce(RunEndedMsg{RunID: "run-c"}))
		}},
		{"call completed", "", "Checked 1 task", "1;38;5;214", func(t *testing.T) Frame {
			var r Reducer
			return lastFanoutFrame(t, r.Reduce(NewMessageMsg{Msg: statusDone("")}))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := tc.card(t)
			if card.FanoutCallStatus != tc.status {
				t.Fatalf("FanoutCallStatus = %q, want %q", card.FanoutCallStatus, tc.status)
			}
			if !card.Final || !strings.HasPrefix(card.Summary, tc.header) {
				t.Fatalf("summary = %q final=%v, want a settled card starting %q", card.Summary, card.Final, tc.header)
			}
			vm := viewModel{}
			vm.append(card)
			vr := renderViewport(&vm, 120, 42, 0, DiffThemeDark)
			for _, row := range vr.lines {
				if !strings.HasPrefix(stripANSI(row), "● "+tc.header) {
					continue
				}
				cells, _ := rowColumnStates(row, "")
				colored := 0
				for i, c := range cells {
					switch c {
					case "":
					case tc.sgr:
						colored++
					default:
						t.Fatalf("cell %d wears SGR %q, want only %q on this row: %q", i, c, tc.sgr, row)
					}
				}
				// The marker and every rune of the head wear the state colour.
				if cells[0] != tc.sgr || colored < 1+len([]rune(tc.header)) {
					t.Fatalf("marker SGR %q, %d colored cells for %q, want the marker and the whole head in %q: %q",
						cells[0], colored, tc.header, tc.sgr, row)
				}
				return
			}
			t.Fatalf("header row %q not found:\n%s", tc.header, stripANSI(strings.Join(vr.lines, "\n")))
		})
	}
}
```

说明：`rowColumnStates` 每个可见 rune 追加一项、SGR 序列只更新状态，所以下标 0 是 `●`，其后是卡头文字；
viewport 给 subagent 卡头不加左侧 `│` 水槽（复现输出已证实），行首即 `"● "`；行尾若有补白格，其状态是
空串，循环里放行。`call canceled` 用例照 `TestInterruptedWaitCardSettlesAsCanceled` 的到达顺序造卡
（`cancelInFlightQueryCards` 把在途查询卡结算成 canceled）。

---

## Implementation Status

- **Verdict**: APPROVE（评审方＝本会话主 agent，在 worktree 内独立复跑，不采信执行器报告）
- **执行器**: `alibaba/deepseek-v4.1-flash-1017`（owner 指定），general-purpose 子代理，单实例
- **Worktree / 分支**: `.worktrees/tui-subagent-card-failed-header-red`，分支同名，基于 dev `c3176812`
- **提交**: `c237bec1` `fix(tui): subagent card header follows its call status (failed red, canceled gray)`
  — 3 文件 +166/−9（`pkg/tui/reducer.go` +28、`pkg/tui/render.go` +26/−6、`pkg/tui/reducer_test.go` +121）；
  未 push、未 merge，**合并待 owner**。

### 计划任务逐项

| 计划步骤 | 状态 | 说明 |
|---|---|---|
| Drift check | done | 评审方复跑 `git diff 364a11a2 -- 三文件` 为空；五个锚在计划原行号 |
| Step −1 / Step 6 项 1–2（plans/ 落盘 + 索引） | done（评审方） | 本文件 + `plans/README.md` 两处 |
| Step 0 真机复现（修前） | done | 琥珀 `38;5;214`，与 owner 截图一致 |
| Step 1 回归测试（红） | done | 编译级红（`FanoutCallStatus undefined`）；行为级红由附录 A overlay 给出 |
| Step 2 生产代码（§3.1 四处） | done | 与计划摘录逐字一致，仅多 gofmt 对齐两行 |
| Step 3 定点测试 + 计数门 | done | 顶层 PASS 6 / FAIL 0，新测试 5 子用例全 PASS |
| Step 4 包级回归 + vet | done | `ok`；vet 0 |
| Step 5 场景 1（失败卡）/ 2（resume 回放）/ 3（成功卡） | done | 203 / 203 / 214 |
| Step 5 被取消卡真机场景 | not done（计划声明的证据边界） | fake provider 造不出；以单测 `call canceled` 为证 |
| Step 6 清理 | done | tmux/临时目录清除，`tui.err` 0 字节 |

### 偏差（均已判定可接受）

1. **git 工作流**：按 owner 的交接在隔离 worktree 的自有分支提交；计划头「不用 worktree / 不 commit」
   描述的是主树实施方式，被交接覆盖——不是执行器自作主张。
2. **真机抓屏的 SGR 形态**：tmux `capture-pane -e` 把粗体与颜色拆成两个序列
   （`ESC[1mESC[38;5;203m`），不是计划里写的合并形式 `ESC[1;38;5;203m`。颜色码相同，
   判据（含 203 且不含 214）不受影响；单测里 `rowColumnStates` 读到的合并状态正是 `1;38;5;203`。

### 评审方独立复跑的证据（全部在 worktree 内）

- **Scope**：`git diff --stat c3176812..HEAD` 恰为三个在范围文件；`git status --porcelain` 为空；
  主树仍在 dev `c3176812`，`pkg/tui` 零改动。
- **gofmt / vet**：`gofmt -l pkg/tui` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` exit 0。
- **定点 6 测试**：`PASS top-level 6`、`FAIL 0`；`TestSubagentCardHeaderColourFollowsItsCallStatus`
  的 5 个子用例全 PASS。
- **测试会咬人（反证）**：用 `-overlay` 把 `fanoutHeaderLine` 的两个 `case` 改名（恢复旧行为）后——
  附录 A 复现的 live/replay 两例 FAIL（`38;5;214`）；新测试里 `status failed, live`、`status failed, replay`、
  `agent failed, reason on the row`（期望 `203`）与 `call canceled`（期望 `244`）四例 FAIL（实得 `214`），
  `call completed` PASS。即：测试对每一态都真的在断言颜色字节。
- **全包**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok 200.975s`。
- **真机 A/B**（评审方自跑；driver 会话 `fb-cc-review`、端口 8767、独立 work dir；`ENABLE_SUBAGENT=1`，
  `tool` 模式两参）：
  - **BEFORE**：以 `-overlay` 把三个文件换回 `c3176812` 版本构建的修复前二进制 →
    `^[[1m^[[38;5;214m●^[[0m ^[[1m^[[38;5;214mFailed to check 1 task^[[0m`，
    下一行 `tool subagent_status: subagent not found`。
  - **AFTER**（worktree 构建）：`^[[1m^[[38;5;203m●^[[0m ^[[1m^[[38;5;203mFailed to check 1 task^[[0m`
    （203×1、214×0）。
  - **resume 回放**（`forebrain resume <sid>`）：同 AFTER（203×1、214×0）。
  - **成功卡 `subagent_list`**：`^[[1m^[[38;5;214m●^[[0m ^[[1m^[[38;5;214mListed 0 tasks^[[0m`（仍琥珀）。
  - `tui.err` 0 字节，屏幕无 panic。
- 执行器自报的同一组抓屏（会话 `fb-cardcolor`、端口 8766）与上述逐码一致。

### 遗留 / 未做

- **被取消卡的真机取证**：无现成 fake-provider 场景。owner 手工自验路径：有子代理在跑时对其
  `subagent_wait` 按 Esc，卡头 `Canceled wait for …` 应为灰色。单元级证据已覆盖（真实 reducer →
  `cancelInFlightQueryCards` → 真实 viewport painter，SGR `1;38;5;244`）。
- **合并**：worktree 分支 `tui-subagent-card-failed-header-red` @`c237bec1` → dev，由 owner 决定
  （对 `pkg/tui/reducer.go` / `render.go` 的改动区域与并行 agent 7a / fd / a7 的区域无交集，均已互通）。
- **被取消卡的标记字形**仍为 `●`（owner 裁决只对齐颜色）。

### 验收中踩到的工具链坑（留给下一次）

- fresh worktree 缺三样 gitignored 构建输入：`third_party/libtokenizers`、`third_party/pdfium`、
  `pkg/gateway/dist`。用主树 symlink（两个 third_party）+ 拷贝（dist）接线；
  `.git/info/exclude` 已忽略这两个 symlink，`git status` 保持干净。这样 `$D build` 里的
  fetch 脚本是 no-op，且 cgo 缓存不会烤进 worktree 路径（删 worktree 后主树不用 `go clean -cache`）。
- driver `tool` 模式只收两参（第二参＝工具调用 JSON）；第三参是 drip 延迟秒数，传文本会让
  fake provider `ValueError` 起不来。
