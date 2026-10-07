# 计划：修复工具卡片 output 区首行缩进丢失（多行载荷整体 TrimSpace）

> **执行者须知**：逐步执行。每一步都运行“验证”命令并确认结果后再进入下一步。出现
> “STOP 条件”里的任何情况，立即停止并报告，不要自行发挥。**不要提交代码**
> （owner 手动审阅提交，仓库铁律）。本计划批准后第一步先把它复制到仓库惯例位置
> `docs/plan/TOOL_OUTPUT_FIRST_LINE_INDENT_PLAN.md`（step 0）。
>
> **漂移检查（先运行）**：`git rev-parse HEAD` 应为 `5ce55b5`（工作区另有本会话临时复现
> 测试 `pkg/tui/zz_repro_indent_test.go`，step 0 删除）。若 `pkg/tool/format.go`、
> `pkg/tui/render.go` 的锚点代码对不上下面的摘录，STOP。

## 状态

- **优先级**：P1（owner 报障：截图里 shell 卡片 `Ran ...` 的 output 区首行缩进错位）
- **状态**：已实施并真机验收通过（2026-10-07；改动留工作区，未 commit）
- **工作量**：S
- **风险**：LOW（只改“边界空白怎么删”，不改任何渲染前缀与布局）
- **依赖**：无
- **类别**：bug
- **基线**：工作区 2026-10-07，HEAD `5ce55b5`（临时复现件 `pkg/tui/zz_repro_indent_test.go` 已按 Step 0 删除）

## 为什么要做

TUI 的工具卡片把正文每一行画在同一条 gutter 下（首行 `"  └ "`、其余 `"    "`，都是 4 列）。
引擎产出的 display body 现在把多行 stdout/stderr 当作**一个字符串**做 `strings.TrimSpace`，
于是只吃掉**第一行**行首的空白，第二行起保留原缩进 —— 卡片首行因此比其余行左移 N 列
（N = 该行原本的缩进宽度）。截图就是这个形态：网关启动横幅每条路由是 `"  %-8s%s"`
（`pkg/gateway/serve_run.go:272`），于是 `grep` 出来的路由表在卡片里变成

```
  └ GET     /api/chat/sessions          ← 首行丢了 2 个前导空格
      POST    /api/chat/sessions
      GET     /api/chat/sessions/:id
```

首行与其余行差 2 列，路径列对不齐。同一处缺陷还影响 stderr、通用工具的
`stdout_preview`/`preview_text`、`lsp` 输出、`retrieve_output` 正文、`intermediate_tool`
笔记正文，以及 TUI 非 viewport 路径的 `summaryToolBody`。live 与 replay 共用同一个
`FormatToolStepResult`，所以 live/replay 两张卡都错——修一处两头都对。

## 根因（已定位并复现，2026-10-07 核实）

**唯一根因**：`pkg/tool/format.go:559-560`（`formatShellStep`）

```go
		stdout := strings.TrimSpace(stringFromAny(evt.Output["stdout"]))
		stderr := strings.TrimSpace(stringFromAny(evt.Output["stderr"]))
```

`TrimSpace` 作用于**整段**多行文本：它只删字符串两端的空白，即**首行行首**与**末行行尾**，
中间每一行原封不动。首行缩进因此被吃掉，其余行保留 —— 逐行对齐的卡片正文里就表现为
首行左移。

**链路（可核对）**：

- 引擎侧产出 display body：`pkg/tool/notification.go:155`
  `displayBody, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)`
  （`:156` 的 `strings.TrimSpace(displayBody)` 只作用于整段的最外层，首行是 `**shell**`，
  不吃正文缩进，所以不是根因）。shell 分支 → `pkg/tool/format.go:99` → `formatShellStep`。
- 同一函数也是 replay 与 resume 的来源：`pkg/run/orchestration_llm.go:1204`、
  `pkg/tui/chat_turn.go:331`、`pkg/tui/commands.go:1261`。
- TUI 侧前缀本身是**正确**的：`pkg/tui/render.go:3517-3525`（`toolOutputLinePrefix`）
  `"  └ "` 与 `"    "` 同为 4 列；折叠路径 `pkg/tui/reducer.go:2850-2858`（`toolBodyLines`）
  按 `"  └ "` 切正文；viewport 里 body 与 fold 之间不做任何去缩进。

**现场证据（owner 机器真实日志）**：`$TMPDIR/forebrain-run/gw.log` 第 11-40 行是网关启动
路由表，每行 `"  " + %-8s + path`：

```
··GET·····/
··GET·····/api/actions
··GET·····/api/chat/sessions
```

**复现（本会话已实跑，证据）**：临时测试 `pkg/tui/zz_repro_indent_test.go`
（`tool.FormatToolStepResult` + `renderFrameLines`，120 列）输出：

```
engine body:
stdout:

```text
GET·····/api/chat/sessions        ← 首行 2 个前导空格已被吃掉
··POST····/api/chat/sessions
··GET·····/api/chat/sessions/:id
```

renderFrameLines:
 0 |●·Ran·grep·-E·'/api/chat'·"$TMPDIR/forebrain-run/gw.log"·|·grep·-v·subagent·|·head·-25|
 1 |··└·GET·····/api/chat/sessions|
 2 |······POST····/api/chat/sessions|
```

与报障截图逐列一致（首行 `└ GET`，其余行整体右移）。

**同类（同一缺陷模式，逐条核对过）**：把多行文本载荷整体 `strings.TrimSpace` 后交给
“每行一条 gutter”的展示面。JSON 解析类（`exit_plan_mode`/`working_set_pin`/subagent 的
`output`、`firstFencedBody` 的三个调用点）**不算**：它们紧接着 `json.Unmarshal`，删除两端
空白是解析需要，不影响任何正文缩进。

## 设计

新增一个共享 helper（放 `pkg/tool/format.go`，紧邻 `formatShellStep` 上方），只删**边界空行**
与**末行行尾空白**，保留第一行自身的缩进：

```go
// trimBlankEdgeLines drops the blank lines that pad a multi-line tool payload
// at either edge, plus the trailing whitespace of its last content line, and
// leaves every content line otherwise byte-for-byte intact.
//
// strings.TrimSpace is the wrong tool for a payload that is drawn line by line,
// and was the bug: it treats the payload as one string, so it strips the leading
// whitespace of the FIRST content line only — every later line keeps its own,
// and the card's first row lands N columns left of the rest (the gateway's
// startup route table, "  %-8s%s" in pkg/gateway/serve_run.go, is the case that
// surfaced it). A blank line at either edge still has to go: a fenced block must
// not open on an empty row, and an all-blank payload is still empty.
func trimBlankEdgeLines(s string) string {
	lines := strings.Split(s, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start >= end {
		return ""
	}
	out := lines[start:end]
	out[len(out)-1] = strings.TrimRight(out[len(out)-1], " \t")
	return strings.Join(out, "\n")
}
```

替换点（全部是“多行文本载荷 → 卡片正文/围栏代码块”的位置）：

| # | 位置 | 现文 | 说明 |
|---|---|---|---|
| 1 | `pkg/tool/format.go:559` | `stdout := strings.TrimSpace(stringFromAny(evt.Output["stdout"]))` | 报障点 |
| 2 | `pkg/tool/format.go:560` | `stderr := strings.TrimSpace(stringFromAny(evt.Output["stderr"]))` | 同函数，同类 |
| 3 | `pkg/tool/format.go:1201` | `preview := strings.TrimSpace(stringFromAny(evt.Output["stdout_preview"]))` | 通用工具围栏预览 |
| 4 | `pkg/tool/format.go:1209` | `body := strings.TrimSpace(stringFromAny(evt.Output["preview_text"]))` | 同上（`result preview:`） |
| 5 | `pkg/tool/format.go:1238` | `body := strings.TrimSpace(stringFromAny(evt.Output["output"]))` | `retrieve_output` 正文（Markdown，逐字展示） |
| 6 | `pkg/tool/format.go:1246` | `body := strings.TrimSpace(stringFromAny(evt.Output["output"]))` | `lsp` 正文（` ```text ` 围栏） |
| 7 | `pkg/tool/format.go:1623` / `1626` / `1631` | `strings.TrimSpace(... "content")` | `intermediate_tool` 笔记正文（Markdown，逐字展示；`:1631` 是 `read` 分支） |
| 8 | `pkg/tui/render.go:3629` / `3638` | `strings.TrimSpace(f.Content)` | 非 viewport 路径的正文；改用 TUI 既有的同语义 helper `trimBlankFenceEdges`（`pkg/tui/render.go:4088`）：`trimBlankFenceEdges(strings.Split(f.Content, "\n"))` |

第 8 处是 TUI 侧同族：交互 TTY 走 viewport/fold（body 直接来自引擎），
`summaryToolBody` 只在非 TTY（plain append stream）路径生效——同一缺陷模式，一并归一。

**行为变化矩阵**

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| stdout/stderr 首行有缩进（表格、列表、`git status`/`diff` 预览、路由表…） | 首行行首空白被吃掉，卡片首行左移 N 列 | 首行与其余行同列（缩进逐字保留） |
| 载荷首/尾有空行（transport padding） | 空行被删 | 不变（仍删边界空行） |
| stdout/stderr 全为空白 | 视为空，不画该段 | 不变 |
| 末行行尾空格 | 删除 | 不变 |
| 载荷中间的空行 | 保留 | 不变 |
| JSON 解析类载荷（`firstFencedBody` / `exit_plan_mode` / `working_set_pin` / subagent） | 两端 TrimSpace 后解析 | 不变（不在改动范围） |
| Markdown 正文首行缩进 ≥4 空格 | TrimSpace 使其降级为普通段落 | 保留为代码块（作者原意，faithful） |
| web 面 | 同一 display body，首行同样错位 | 随引擎修复一并正确（1:1 对齐） |

## Scope

**In scope**（只允许改这些文件）：

- `pkg/tool/format.go`（新增 helper + 上表 1-7 处替换）
- `pkg/tool/format_test.go`（新增回归测试）
- `pkg/tui/render.go`（上表第 8 处，2 个 return）
- `pkg/tui/render_test.go`（新增对齐回归测试）
- `docs/plan/TOOL_OUTPUT_FIRST_LINE_INDENT_PLAN.md`（step 0 建；收口时回填验收记录）

**Out of scope**（看着相关，明确不碰）：

- `pkg/gateway/serve_run.go:272`（`"  %-8s%s"`）：这是网关启动横幅的**真实输出格式**，
  缩进是它的一部分，不是 bug。
- `pkg/tui/render.go` 的 gutter 体系（`toolOutputLinePrefix` / `formatToolOutputBlock` /
  `foldBlock` / `toolBodyLines`）：渲染前缀本就对齐，改它就是把 bug 藏起来。
- 所有“TrimSpace 后 `json.Unmarshal`”的位置（`pkg/tool/format.go:1649`、`:1921`、
  `:2609`、`firstFencedBody` 的三个调用点 `:1303`/`:1359`/`:1783`）。
- `pkg/tui/render.go:2031-2037`（MCP envelope）：框体解析需要 TrimSpace，且回退分支展示的是
  以 `{` 开头的 JSON 框体，首行缩进无意义（本计划内注明，不修）。
- `pkg/tool/web_tools.go`、web 前端 `pkg/gateway/dist`、`frontend/`。
- 任意 `git commit` / `git push`。

## Steps

### Step 0：落位与清理

1. 把本文件复制为 `docs/plan/TOOL_OUTPUT_FIRST_LINE_INDENT_PLAN.md`（仓库惯例）。
2. 删除本会话的临时复现件 `pkg/tui/zz_repro_indent_test.go`（其内容在 Step 4/5 以正式
   回归测试重建）。

**验证**：`ls docs/plan/TOOL_OUTPUT_FIRST_LINE_INDENT_PLAN.md && ! test -e pkg/tui/zz_repro_indent_test.go` → 退出码 0。

### Step 1：先写会红的回归测试（证明测试能抓住这个 bug）

在 `pkg/tool/format_test.go` 新增（沿用该文件既有 `StepEvent` 构造风格）：

- `TestTrimBlankEdgeLines`（表驱动）：
  - `"  GET x\n  POST y\n"` → `"  GET x\n  POST y"`
  - `"\n\n  GET x\n\n\n"` → `"  GET x"`
  - `"  GET x   \n"` → `"  GET x"`
  - `"a\n\nb"` → `"a\n\nb"`（中间空行保留）
  - `"   \n\t\n"` → `""`；`""` → `""`
- `TestFormatShellStepKeepsFirstLineIndent`（**报障用例**）：`evt.Output["stdout"]` =
  `"  GET     /api/chat/sessions\n  POST    /api/chat/sessions\n  GET     /api/chat/sessions/:id\n"`
  （2 空格 + 方法名补到 8 列 + 路径，与 `gw.log` 逐字一致）。断言 body 含
  `` "```text\n  GET     /api/chat/sessions\n  POST    /api/chat/sessions\n  GET     /api/chat/sessions/:id\n```" ``，
  并断言三条路径在 body 内的列号相同（`strings.Index` of `/api/chat/sessions` 每行相等）。
  另加 stderr 同形用例。
- `TestToolBodiesKeepFirstLineIndent`（同族表驱动）：对 `stdout`、`stderr`、
  `stdout_preview`、`preview_text`、`lsp.output`、`retrieve_output.output`、
  `intermediate_tool` 的 `input.content` 各造一条载荷，断言首行缩进（`"  "`）在产出正文里
  逐字保留。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestTrimBlankEdgeLines|TestFormatShellStepKeepsFirstLineIndent|TestToolBodiesKeepFirstLineIndent' -count=1`
→ **必须失败**（`trimBlankEdgeLines` 未定义 / 断言不符）。看到失败结果即“测试有效”的证明，
记录下来再进 Step 2。

### Step 2：加 helper

按上文“设计”把 `trimBlankEdgeLines` 加到 `pkg/tool/format.go`（放在 `formatShellStep` 上方，
文档注释与上文一致）。不加新文件（`pkg/architecture` 对每包 production 文件数有上限，本次不需
跑 `scripts/package-graph.sh`）。

**验证**：`CGO_ENABLED=1 go build ./pkg/tool` → 退出码 0；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestTrimBlankEdgeLines -count=1` → PASS。

### Step 3：替换引擎侧 7 处

按“设计”表格 1-7 逐处把 `strings.TrimSpace(...)` 换成 `trimBlankEdgeLines(...)`（只换这些
调用表达式，不动任何周边逻辑、空值判断与围栏拼接）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → 全绿（含 Step 1 的两个新测试）。

### Step 4：替换 TUI 侧正文 TrimSpace（2 处）

`pkg/tui/render.go` 的 `summaryToolBody`（`:3629` 与 `:3638`）：把
`strings.TrimSpace(f.Content)` 换成 `trimBlankFenceEdges(strings.Split(f.Content, "\n"))`
（该 helper 已存在于同文件 `:4088`，语义一致；不要新增 helper）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestSummaryToolBody -count=1` → PASS
（既有用例；若该名字不存在，跑 `-run 'ToolBody|Summary'` 或直接跑整包）。

### Step 5：TUI 对齐回归测试

在 `pkg/tui/render_test.go` 新增 `TestToolOutputCardAlignsFirstRowWithRest`：
用 `tool.FormatToolStepResult`（shell + 上面的路由表 stdout）造帧，
`renderFrameLines(f, 120, DiffThemeDark, 0)`，去掉 SGR 后断言：

- 第一行正文（index 1）== `"  └   GET     /api/chat/sessions"`（gutter 4 列 + 载荷自身 2 空格）；
- 所有正文行里 `/api/chat/sessions` 的列号相同。

（`renderFrameLines` 是 `pkg/tui/reducer_test.go:2609` 的既有测试 helper，可直接用。）

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestToolOutputCardAlignsFirstRowWithRest -count=1 -v`
→ PASS，且日志里三行列号一致。

### Step 6：包级回归 + vet

```bash
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn ./pkg/run -count=1
go vet ./...
```

**验证**：三条全绿。既有断言若因为“首行缩进现在被保留”而失败，先判断是哪一个契约变了
（本计划只允许“首行缩进保留”这一种变化），按新契约更新断言并在测试注释里写明理由；
任何其他失败停机上报。

### Step 7：tmux 真机验收（必做；测试绿 ≠ 真机可用）

用运行器 `.claude/skills/run-forebrain/driver.sh`（fake provider 的 `shell` 模式：首个请求回一个
真实 shell 工具调用，命令即 answer text）：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D reset
$D build
$D start shell 'printf "  GET     /api/chat/sessions\n  POST    /api/chat/sessions\n  GET     /api/chat/sessions/:id\n"'
$D submit '列出网关路由表'
$D wait 'Ran' 30
$D screen
```

- 若弹出审批浮层（approval_policy: on-request），按 `$D key Down Enter` 批准后继续等待。
- 取证：`$D screen > /tmp/fb-indent.txt`，再核对三行正文的路径列号一致：

```bash
python3 - <<'PY'
rows = open('/tmp/fb-indent.txt').read().splitlines()
body = [l for l in rows if l.lstrip().startswith(('GET ', 'POST '))]
cols = [l.index('/api/chat/sessions') for l in body]
assert len(body) >= 3 and len(set(cols)) == 1, (body, cols)
print('columns:', cols)
PY
```

  期望：三行**列号相同**（本例 `columns: [14, 14, 14]` —— 4 列 gutter + 载荷自身 2 空格缩进
  + 8 列方法名补位；pane 从第 0 列开始捕获，所以就是绝对列号）。关键判据是三者相等，
  而不是某个具体数字。
- 回归场景：同一会话再跑一次 `$D start shell '<一条无缩进的命令，例如 printf "hello\n">'`
  → 卡片与修复前一致（首行 `  └ hello`）。
- 收尾：`$D stop`；临时目录 `$TMPDIR/forebrain-run` 由 driver 管理，验收用隔离
  `FOREBRAIN_HOME`，不得触碰真实 `~/.forebrain*`。

**验证**：上面的 python 断言通过（三行列号一致），且 `$D screen` 里首行为
`  └   GET     /api/chat/sessions`（gutter 与载荷缩进都在）。

### Step 8：收口

- `docs/plan/TOOL_OUTPUT_FIRST_LINE_INDENT_PLAN.md` 状态行改“已实施并真机验收通过（日期）”，
  补“验收记录”章节（场景 → 证据，含 Step 7 的列号输出）。
- 报告根因链、最小改动清单、验证证据、遗留限制；改动留工作区，**不 commit**。

## Test plan

- 新增（`pkg/tool/format_test.go`）：`TestTrimBlankEdgeLines`（边界空行/末行尾空白/中间空行保留）、
  `TestFormatShellStepKeepsFirstLineIndent`（报障用例：路由表 stdout+stderr 首行缩进与列对齐）、
  `TestToolBodiesKeepFirstLineIndent`（同族 7 个载荷各一例）。结构照 `pkg/tool/format_test.go`
  既有表驱动用例写。
- 新增（`pkg/tui/render_test.go`）：`TestToolOutputCardAlignsFirstRowWithRest`（卡片正文逐行同列）。
- 结构范式：`pkg/tool/format_test.go` 里已有的 `StepEvent{ToolName, Kind, Output}` 构造；
  `pkg/tui/render_test.go` 里已有的 `renderFrameLines(...)` + `stripANSI` 断言。
- 命令：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1` → 全绿，新增用例 4 个。

## Done criteria

- [x] `CGO_ENABLED=1 go build ./...` 退出码 0
- [x] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn ./pkg/run -count=1` 全绿
- [x] `go vet ./...` 退出码 0
- [x] `rg -c 'trimBlankEdgeLines\(' pkg/tool/format.go` 输出 `10`（1 处定义 + 9 处调用；
      **本条原写 8 是笔误**——上面站点表第 7 行含 3 个 `content` 调用，`sed` 那条列的也是 9 行）
- [x] `sed -n '559p;560p;1201p;1209p;1238p;1246p;1623p;1626p;1631p' pkg/tool/format.go` 的
      对应代码位置每一行都含 `trimBlankEdgeLines`（新增 helper 后行号整体 +11：实际落在
      588/589、1230/1238、1267/1275、1655/1658/1663）
- [x] `sed -n '3629p;3638p' pkg/tui/render.go` 两行都是 `trimBlankFenceEdges(strings.Split(f.Content, "\n"))`
- [x] Step 7 的真机断言通过（三行正文路径列号一致），证据记入下方验收记录
- [x] `git status --porcelain` 只含 Scope 文件（`plans/WEBSITE_GRAPHITE_REDESIGN/` 是 owner 并行
      新建的未跟踪目录，非本计划产物）
- [x] 无任何 commit（改动留工作区）

## 验收记录（2026-10-07，已实施）

- **Step 1 红证**：`go test ./pkg/tool -run '...'` → `undefined: trimBlankEdgeLines`（build failed）。
  加 helper 后、替换前再跑一次，两个 format 测试**在真实缺陷上失败**（非编译失败）：
  `TestFormatShellStepKeepsFirstLineIndent` 与 `TestToolBodiesKeepFirstLineIndent` 的 7 个
  子用例全部报 `first line lost its indent`，body 为 `"MARKER_ONE\n  MARKER_TWO"`。证明测试
  抓的是 bug 本身。
- **Step 6**：`go build ./...` OK；`go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn ./pkg/run`
  全绿（tool 6.5s / tui 42.4s / turn 3.2s / run 14.1s）；`go vet ./...` 退出 0。
- **Step 7 真机（driver.sh，隔离 FOREBRAIN_HOME）**：
  - `$D start shell 'printf "  GET     /api/chat/sessions\n  POST    /api/chat/sessions\n  GET     /api/chat/sessions/:id\n"'` → 卡片正文：
    ```
      └   GET     /api/chat/sessions
          POST    /api/chat/sessions
          GET     /api/chat/sessions/:id
    ```
  - 列号断言：三行 `/api/chat/sessions` 的列号 = `[14, 14, 14]`（4 列 gutter + 载荷 2 空格 +
    8 列方法名补位），一致。
    - 注意：计划里那段 python 过滤条件 `l.lstrip().startswith(('GET ','POST '))` 会**漏掉第一行**
      （它以 gutter 字符 `└` 开头），实际只匹配到 2 行。已把过滤条件改为“含 marker 且非
      summary/回显行”重跑，得到完整 3 行 `[14,14,14]`。**这是计划脚本的缺陷，非产品缺陷。**
  - 回归：`$D start shell 'printf "hello\n"'` → `  └ hello`，与修复前一致。
- **额外补强（计划外，属同一改动的覆盖缺口）**：`summaryToolBody` 是 live 卡片正文管线
  （`renderCompactFrame` 在非 full-body 模式调用它），而计划 Step 5 的 `renderFrameLines`
  强制 `fullBodyMode=true`，从不经过它——即 Step 4 的改动原本无自动化测试覆盖。因此新增
  `TestSummaryToolBodyKeepsFirstLineIndent`，并用 `go test -overlay`（把 `summaryToolBody`
  临时还原成 `strings.TrimSpace`）验证：还原后该测试**失败**（`"alpha\n  beta"`），修复后通过
  ——确认它确实能抓住回归。
- **环境事件（非本计划产物）**：Step 6 首次 `go build ./...` 因 owner 机器磁盘写满
  （`no space left on device`）失败，并连带损坏了 `~/Library/Caches/go-build`（后续报
  `hash/maphash is not in std` 等）。清理 `$TMPDIR/go-build*`、`$TMPDIR/go-link*` 并
  `go clean -cache` 后恢复；**未触碰** VSCode ShipIt、`forebrain-tool-outputs`（本会话
  `retrieve_output` 缓存）与其它 owner 数据。磁盘现状见会话报告。
- **Step 3 与计划的一处偏差**：计划把 `intermediate_tool` 的 `input.content` 站点写作
  `strings.TrimSpace(firstString(evt.Input, "content"))`，但 `firstString`
  （`pkg/tool/format.go:2943`）**内部**就 `TrimSpace` 了，包一层 `trimBlankEdgeLines` 是空操作。
  该站点改为直接取原值 `stringFromAny(evt.Input["content"])`（只查一个 key，语义等价），
  否则计划 Step 1 要求的“首行缩进逐字保留”在该分支无法成立。

## STOP conditions

- `pkg/tool/format.go:559-560`、`:1201`/`:1209`、`:1238`/`:1246`、`:1623`/`:1626`/`:1631`
  与 `pkg/tui/render.go:3629`/`:3638` 的实际代码与摘录不符（已漂移）。
- Step 1 的测试**直接通过**（说明 bug 已被别人修掉或测试没打到点，别再改源码，停下来报告）。
- Step 3 之后 `pkg/tool` 有既有断言失败，且失败原因**不是**“首行缩进保留”这一契约变化
  ——说明改动打到别的语义，停下来报告。
- Step 7 真机上首行仍然错位、或三行列号不一致：说明还有第二条独立路径（例如正文在别处又被
  TrimSpace 过一次），停止并按现场证据重新定位根因，不要加补丁掩盖。
- 修复需要动 Scope 之外的文件（尤其 `pkg/gateway/serve_run.go`、view gutter 体系）——STOP。

## Maintenance notes

- 这条不变量值得记住：**引擎产出的多行载荷必须逐行 byte 保真，只能删边界空行**
  （`trimBlankEdgeLines`）。今后任何“把多行工具输出塞进卡片/围栏”的新代码都要用它，
  不要用 `strings.TrimSpace`——后者在多行字符串上等价于“只对齐第一行”。
- review 重点：看替换点是否**只**换了取值表达式（空值判断、围栏拼接、`HasSuffix("\n")`
  逻辑不许顺手改）；看新测试是否真的在修复前失败过（Step 1 的失败输出应记入报告）。
- 刻意不做的：MCP envelope 的 `TrimSpace`（`pkg/tui/render.go:2031-2037`，解析需要）、
  JSON 解析类位置、`pkg/gateway/serve_run.go` 的输出格式。若今后发现 MCP 回退分支
  （非 JSON 正文）也出现同类错位，再单独开计划处理。
