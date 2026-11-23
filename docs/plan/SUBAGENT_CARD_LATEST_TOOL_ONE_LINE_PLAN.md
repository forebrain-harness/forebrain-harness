# 子代理卡“最近一次工具调用”只画一行（超宽截断加省略号）

> **给执行者（零上下文）**：本计划自带全部事实。按 Steps 顺序做，每一步都跑验证命令并对照“预期”，不要跳步、不要顺手改别的。
> 命中 "STOP conditions" 就停下汇报，不要自己发挥。
> 本仓库 owner 的硬规则：**不 commit**（改动留工作区由 owner 审阅）、回复用中文、只修根因不做症状补丁、不为不可达状态写防御代码。

## Status

- **Priority**: P1（owner 直接报的体验缺陷）
- **Effort**: S（2 个生产文件、2 个测试文件）
- **Risk**: LOW（只动 TUI 一处卡片渲染；无状态、无协议、无持久化）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `3dff52cd`（计划时 `git status --porcelain` 为空），2026-11-15
- **Plan 文件落盘**：审批通过后把本文件复制到 `docs/plan/SUBAGENT_CARD_LATEST_TOOL_ONE_LINE_PLAN.md`（本仓惯例，`docs/plan/` 已 gitignore），再开始改源码

## Why this matters

owner 原话（本会话报障，附截图）：

> “所有 subagent_* 子代理派发工具的消息卡里的最近一条工具调用的显示行数现在没有做任何限制，如果 shell 命令非常长，会显示多行，用户体验很差，请加上限制：**一个子代理最近一次工具调用只显示一行，超过终端宽度则截断加省略号**”

`subagent_*` 家族（`subagent_run` / `subagent_fanout` / `subagent_send` / `subagent_continue` / `subagent_status` / `subagent_wait` / `subagent_close` / `subagent_list` / 计划评审卡）在 TUI 里画成一张“子代理卡”：标题行 + 每个任务一行 + **第三层：该任务最近一次工具调用**。

第三层的文本是工具调用的 invocation 标签；`shell` 工具的标签就是**整条命令原文**。命令一长（heredoc、`python3 - <<'EOF' …`），渲染器按词换行成十几行，把转录区挤满——owner 截图里的 `python3 - <<'EOF' s = open('pkg/run/runner.go').read() …` 就是这样铺满一屏。第三层的语义是“它此刻在跑什么”，一眼扫过即可。

修好之后：第三层永远只占一行，超过卡片可用宽度就截断并以 `…` 结尾；`/resume` 回放与“子代理自己发起的派发卡”同步生效（同一 reducer + 同一渲染函数）。

**已由 owner 裁决的取舍（2026-11-15）**：这一行不再提供 tooltip / 点击展开；要看完整命令就进该子代理自己的视图——它的工具卡头部写的是 `Ran <命令原文>`（`pkg/tool/header_parts.go:130`、`render.go:2162-2181` 的 FrameTool 头部），走 `wrapCardHeader`（`render.go:3100`，内部 `wrapToolDisplayRows`）换行显示、**没有行数上限**，全文完整可读。web 卡同一位置也只有 CSS 省略号、没有悬浮全文，本次不改 web。

## Current state（根因链，已逐链取证）

### 1) 内容层：第三层是**一条**内容行，原样写入未截断的 label

`pkg/tui/reducer.go:2549-2562`（`renderFanoutContent`）：

```go
		// A paused task's third layer is the pause: the last call it made was
		// stopped, and what it does next is the user's message.
		if t.Paused && !fanoutTaskTerminal(t.Status) {
			add("  └ ", subagentPausedCardLine)
			continue
		}
		// Live tool progress (third layer): the most recent tool label.
		overflow := t.ToolTotal - len(t.RecentTools)
		for _, tool := range t.RecentTools {
			add("  └ ", singleDisplayLine(tool.Label))
		}
		if t.ToolTotal > 0 && overflow > 0 {
			add("    ", fmt.Sprintf("… +%d tool uses", overflow))
		}
```

- `add(prefix, text)`（`reducer.go:2510-2516`）把文本按 `\n` 切成多条内容行，并同步写 `lineAgents` / `clocks`。
- `singleDisplayLine`（`pkg/tool/display_header.go:371-383`）只把换行折成空格，**不限长度**。
- label 来自 `fanoutToolLabel`（`reducer.go:559-568`）→ `Message.ToolMeta.Invocation`；`shell` 工具的 Invocation 就是命令原文（`pkg/tool/format.go:2788-2811`、`2819-2820`）。
- 每个任务只保留最近 1 条（`maxFanoutRecentTools = 1`，`reducer.go:275-277`）。
- **注意**：`pkg/tui/reducer.go` 里还有别的 `"  └ "` 字面量（`:2889` 图片 marker 命中判定、`:3316`/`:3328` 折叠块 body 分类）。它们与第三层无关，**不准动**。

### 2) 渲染层：卡片 body 的每一条内容行都走 `wrapCardLine`，第三层没有例外

`pkg/tui/render.go:5459-5484`（`renderFanout` 的 body 循环）：

```go
	for contentLine, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			_, _ = fmt.Fprintln(r.out)
			owner(-1, 1)
			continue
		}
		if suffix := fanoutClockSuffix(f, contentLine); suffix != "" {
			line += suffix
		}
		lead, leadWidth, text := fanoutRowLead(line)
		wrapWidth := bodyWidth - leadWidth
		if wrapWidth < 20 {
			wrapWidth = 20
		}
		wrapped := wrapCardLine(text, wrapWidth)
		continuation := strings.Repeat(" ", leadWidth)
		for i, wl := range wrapped {
			p := lead
			if i > 0 {
				p = continuation
			}
			_, _ = fmt.Fprintln(r.out, p+wl)
		}
		owner(contentLine, len(wrapped))
	}
```

- `bodyWidth = r.layoutTermWidth() - contentRightMargin`（`render.go:5455`；`contentRightMargin = 10`，`render.go:64`）。
- 真机（viewport）里 block 的布局宽度 = 终端宽 − `viewportRightPadding`(2)（`reducer.go:5089`、`plan_drawer.go:676-685`、`reducer.go:3530`），所以 120 列终端：`layoutWidth` 118 → `bodyWidth` 108。
- `fanoutRowLead`（`render.go:5571-5579`）按“行首空白 + 状态图标”这一既有协议判角色：任务行 → 前缀 `"  └ "`；其它行 → 前缀 `"    "` + 原缩进。因此第三层画成 **6 列缩进 + `└ ` + 文本**（`fanoutPrefixWidth = 4`，`render.go:5559`），文字预算 = 108 − 6 = 102 列。
- 角色判定已有先例：`isFanoutTaskLine`（`render.go:5585-5591`）就是看 `✓/✗/!` 与行首空白。

结论：**根因是渲染器把“一层一行”的第三层当成普通段落换行**；内容层没错（它写的就是一条内容行）。

### 3) 现场证据（owner 截图 + 本会话已复现）

截图：`.forebrain/attachments/forebrain-clipboard-75448-1948180953.png`——一条 shell 命令铺了 10+ 行。

本会话用 `go test -overlay`（不改仓库）在真实渲染路径上复现，**修复前**（120 列）输出：

```
rendered 5 non-blank rows:
    ● Continuing 1 task…
    └ Execute plan 302
    └ python3 - <<'EOF' s = open('pkg/run/runner.go').read() old = '''// WithConfig is SetConfig in
    construction form''' new = old + '''// NewDeps is SetConfig in value-construction form''' assert old in
    s; s = s.replace(old, new, 1) open('pkg/run/runner.go','w').write(s) print('NewDeps added') EOF
--- FAIL: card draws 5 rows, want 3 (header, task row, one tool row)
```

复现用例源码（本文件 Step 1 会把它固化成 `pkg/tui/render_test.go` 里的正式用例）：

```go
package tui

import (
	"strings"
	"testing"
)

func TestReproFanoutLatestToolStaysOneLine(t *testing.T) {
	label := "python3 - <<'EOF' s = open('pkg/run/runner.go').read() old = '''// WithConfig is SetConfig in construction form''' " +
		"new = old + '''// NewDeps is SetConfig in value-construction form''' assert old in s; s = s.replace(old, new, 1) " +
		"open('pkg/run/runner.go','w').write(s) print('NewDeps added') EOF"
	fs := &fanoutState{
		StepID:  "repro-1",
		Verb:    "continue",
		Running: true,
		Tasks: []FanoutTaskState{{
			Title: "Execute plan 302", AgentID: "agent-1", Status: "running",
			ToolTotal: 1, RecentTools: []fanoutToolEntry{{StepID: "tool-1", Label: label}},
		}},
	}
	f := (&Reducer{}).emitFanoutFrame(fs)
	lines := renderFrameLines(f, 120, DiffThemeDark, 0)
	var nonBlank []string
	for _, line := range lines {
		if plain := strings.TrimSpace(stripANSI(line)); plain != "" {
			nonBlank = append(nonBlank, plain)
		}
	}
	t.Logf("rendered %d non-blank rows:\n%s", len(nonBlank), strings.Join(nonBlank, "\n"))
	if len(nonBlank) != 3 {
		t.Fatalf("card draws %d rows, want 3 (header, task row, one tool row)", len(nonBlank))
	}
}
```

（`renderFrameLines` 是测试辅助函数，定义在 `pkg/tui/reducer_test.go:3058`，走 viewport 自己的 `renderFrameLinesWithAgents` 路径，也就是真机同一条渲染链。）

### 4) 相邻事实（确认过，本次不改）

- 卡片高度、点击归属都由渲染器自己的输出推出：`renderFrameLinesWithAgents`（`reducer.go:3520-3575`）→ `fanoutLineAgents`（`3577-3594`）用 `r.fanoutLineOwners` 映射；第三层截成 1 行后 `owner(contentLine, 1)` 自动正确。
- **无空格超宽 token 不会溢出**：`wrapCardLineIndex`（`render.go:5148-5216`）对“比整行还宽的词”会在词内折断，400 个 `x` 在 120 列下今天是**折成 4 行**，不是溢出。
- **子代理自己的视图不画这张卡**：只有“派发这个子代理的那个 agent”的视图里才有它（`reducer.go:6014-6036`，卡的 `AgentID = OwnerAgentID`）。被派发的子代理视图里看到的是它自己的工具卡（`reducer.go:2244-2250`），头部 `Ran <命令原文>` 换行显示，全文可读——这正是本次“放弃在本行显示全文”的依据。
- web 端第三层已是单行：`frontend/src/components/chat/SubagentCallCard.vue:46-51`（`truncate`）；该行**没有** `v-tip`（`v-tip` 只在标题行 `:42`），web 也没有悬浮全文。本次不改 web。
- 第三层还有另一条合法文本：暂停行 `waiting for your message`（`subagentPausedCardLine`，`reducer.go:2267-2269`），很短，同一规则天然不受影响。
- `runewidth.Truncate(s, w, "…")` 在 `s` 已放得下时**原样返回 `s`**（`go-runewidth` 的 `Condition.Truncate` 第一个判断即 `StringWidth(s) <= w → return s`），所以不需要“先判断是否超宽”的分支/助手。

## 修复设计

**一句话**：把“卡片第三层”这个角色在渲染器里显式认出来，让它只画一行——放得下原样画，放不下用 `runewidth.Truncate(text, wrapWidth, "…")` 截到卡片可用宽度并以 `…` 收尾；其余内容行（任务标题、失败原因、`… +N tool uses`）保持现有换行行为。

### 生产改动 1：`pkg/tui/render.go` — 角色判定（常量 + 谓词）

在 `isFanoutTaskLine` 之后（`render.go:5591` 附近）新增：

```go
// fanoutActivityPrefix opens a fanout card's third layer: the row under a task
// naming the call it is running now (or the pause that replaced it). The
// producer (renderFanoutContent) and the reader (isFanoutActivityLine) share
// this one marker, so the two halves cannot drift apart silently.
const fanoutActivityPrefix = "  └ "

// isFanoutActivityLine reports whether a fanout content line is that third
// layer rather than a task row or a reason/stat continuation.
func isFanoutActivityLine(line string) bool {
	if isFanoutTaskLine(line) {
		return false
	}
	return strings.HasPrefix(line, fanoutActivityPrefix)
}
```

**不要**新增 `truncateFanoutRow` 之类的助手，也**不要**在这里剥 SGR：第三层文本来自 `ToolMeta.Invocation`（纯文本），仓内同类截断（`render.go:3591/3593/3610`）都是直接 `runewidth.Truncate(...)`；为不可达的带样式输入写防御分支违反本仓规则。

### 生产改动 2：`pkg/tui/render.go` — body 循环对第三层只写一行

`render.go:5474` 处，把 `wrapped := wrapCardLine(text, wrapWidth)` 改为：

```go
		wrapped := wrapCardLine(text, wrapWidth)
		if isFanoutActivityLine(line) {
			// The third layer is one row by contract: it names the call, and a
			// shell command can be a whole heredoc — wrapped, it painted a
			// paragraph across the transcript where a reader wanted one line.
			// It is cut at the card's right edge with an ellipsis instead, the
			// same one row the web card draws for it. The full command stays
			// readable in the task's own view, on its tool card's header.
			wrapped = []string{runewidth.Truncate(text, wrapWidth, "…")}
		}
```

（`runewidth` 已 import，`render.go:32`。）

### 生产改动 3：`pkg/tui/reducer.go` — 标记只写一次

把 `reducer.go:2552` 与 `reducer.go:2558` 的 `add("  └ ", …)` 换成 `add(fanoutActivityPrefix, …)`。语义零变化，只是把“生产者/消费者”共用的事实命名一次。**只改这两处**，`reducer.go` 里其它 `"  └ "` 都不动。

### 行为变化矩阵

| # | 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|---|
| 1 | 第三层命令长于卡片可用宽度（截图场景） | 按词换行成 N 行 | 1 行，卡片右边界截断，以 `…` 结尾 | 本次修复目标 |
| 2 | 第三层命令放得下 | 1 行 | 1 行，逐字符不变 | 无变化 |
| 3 | 第三层是暂停行 `waiting for your message` | 1 行 | 1 行 | 本来就短 |
| 4 | 第三层是无空格长串（如 base64 / 超长 token） | `wrapCardLineIndex` 在词内折断成多行（400 个 `x`@120 列 = 4 行） | 1 行截断 + `…` | 同属“多行 → 一行” |
| 5 | 任务标题很长 | 换行 | 换行 | 刻意不变：标题是任务的标识，不是“在跑什么” |
| 6 | 失败任务的原因文本很长 | 逐句成行、句内换行 | 不变 | 刻意不变：原因要读全 |
| 7 | `… +N tool uses` 行 | 一行 | 一行 | 不涉及 |
| 8 | 点击第三层行打开子代理视图 | 命中该任务 | 命中该任务 | `owner(contentLine, 1)`；新增用例断言归属 |
| 9 | `/resume` 回放同一张卡 | 多行 | 1 行 | 同一 reducer + 同一渲染函数；Step 7 真机验一次 |
| 10 | web 卡第三层 | 一行（CSS 截断） | 一行 | 不改 |
| 11 | 某子代理自己派发的子代理卡（`OwnerAgentID` = 该子代理） | 多行 | 1 行 | 同一渲染函数 |
| 12 | 被派发任务的视图里看完整命令 | 它自己的工具卡头部换行显示全文 | 不变 | 这是“放弃本行全文”的兜底，Step 7 真机验一次 |

## Scope

**In scope（只准改这些）**

- `pkg/tui/render.go`（`renderFanout` body 循环 + 常量/谓词）
- `pkg/tui/reducer.go`（两处 `add("  └ ", …)` 换常量，共 2 行）
- `pkg/tui/render_test.go`（新增渲染层用例）
- `pkg/tui/chat_session_test.go`（新增“标记共用”与“第三层行归属”用例）

**Out of scope（明确不碰）**

- `frontend/src/**`、`pkg/gateway/**`：web 端第三层已是单行，本次不改任何前端/网关代码。
- `pkg/tool/**`：`toolInvocationLabel` / `SingleDisplayLine` / `TruncateForDisplay` 行为不动（工具卡 header 的换行策略是另一条刻意决定，见 `pkg/tool/display_header.go:385-393` 的注释）。
- 任务标题、失败原因、`… +N tool uses`、查询卡的 `  · ` 短语行的换行行为。
- `pkg/tui` 里其它的 `"  └ "` 用法（`reducer.go:2889/3316/3328` 图片 marker / 折叠块分类）。
- `pkg/tui/run_test.go`（另一会话 forebrain-harness-1b 正在改一行，已确认互不影响，别碰）。
- 其他会话正在改的文件（peer `forebrain-harness-2f` 在改 `plugins/subscriptions/workbuddy/*`，已确认不碰 `pkg/tui`）。共享工作树：**不要 revert/stash/提交任何不是自己改的内容**。

## 基线 / drift check

- 基线 commit：`3dff52cd`（计划时 `git status --porcelain` 为空）。
- 开工先跑：
  ```bash
  git diff --stat 3dff52cd..HEAD -- pkg/tui/render.go pkg/tui/reducer.go pkg/tui/render_test.go pkg/tui/chat_session_test.go
  ```
  有输出就把本文件 “Current state” 的代码摘录与现场逐一比对：**内容不符 = STOP**；只是行号漂移，继续。
- 另跑 `git status --porcelain pkg/tui` 看是否有别人的在途改动；有就先 `peer_list` / 问清楚再动。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| PDFium 环境（本仓 `pkg/pdf` 的 cgo 需要，跑 go 命令前 export） | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | 无输出 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentCard|TestFanout' -count=1` | `ok` |
| 包回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok` |
| 相邻包回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tool -count=1` | 两行 `ok` |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` | 无输出 |
| 格式 | `gofmt -l pkg/tui` | 无输出 |
| 真机构建 | `.claude/skills/run-forebrain/driver.sh build` | 末行 `built …/forebrain` |

## Steps

### Step 1：先落回归测试（红），复现同一症状

在 `pkg/tui/render_test.go` 末尾（或紧挨 `TestSubagentCardBodyIsFullBrightness`，`render_test.go:873-901`，照它的写法：`NewRenderer`/`renderFrameLines` + `stripANSI`）新增四个用例：

```go
// A subagent card's third layer — the latest tool call — is one row: cut at the
// card's right edge with an ellipsis, never wrapped into a paragraph.
func TestSubagentCardLatestToolIsOneElidedRow(t *testing.T) {
	const longCommand = "python3 - <<'EOF' s = open('pkg/run/runner.go').read() old = '''// WithConfig is SetConfig in construction form''' " +
		"new = old + '''// NewDeps is SetConfig in value-construction form''' assert old in s; s = s.replace(old, new, 1) " +
		"open('pkg/run/runner.go','w').write(s) print('NewDeps added') EOF"
	const wideCommand = "python3 - <<'EOF' 重构 " + "读取 subagent 卡片第三层的最近一次工具调用并输出为一行 " +
		"然后比较终端宽度决定是否截断 " + "EOF"

	cases := []struct {
		name  string
		width int
		label string
	}{
		{name: "long shell command at 120 columns", width: 120, label: longCommand},
		{name: "long shell command at 60 columns", width: 60, label: longCommand},
		{name: "wide characters are counted in columns", width: 80, label: wideCommand},
		{name: "unbroken token stays one row", width: 120, label: strings.Repeat("x", 400)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fanoutState{
				StepID: "fanout-elide", Verb: "continue", Running: true,
				Tasks: []FanoutTaskState{{
					Title: "Execute plan 302", AgentID: "agent-1", Status: "running",
					ToolTotal: 1, RecentTools: []fanoutToolEntry{{StepID: "tool-1", Label: tc.label}},
				}},
			}
			f := (&Reducer{}).emitFanoutFrame(fs)
			var rows []string
			for _, line := range renderFrameLines(f, tc.width, DiffThemeDark, 0) {
				if plain := stripANSI(line); strings.TrimSpace(plain) != "" {
					rows = append(rows, plain)
				}
			}
			// header + task row + the one tool row.
			if len(rows) != 3 {
				t.Fatalf("card draws %d rows, want 3:\n%s", len(rows), strings.Join(rows, "\n"))
			}
			toolRow := rows[2]
			if !strings.Contains(toolRow, "└ ") || !strings.HasSuffix(toolRow, "…") {
				t.Fatalf("tool row = %q, want one row ending in an ellipsis", toolRow)
			}
			if got, max := displayLineWidth(toolRow), tc.width-contentRightMargin; got > max {
				t.Fatalf("tool row is %d columns wide, want at most %d: %q", got, max, toolRow)
			}
		})
	}
}

// A command that fits keeps every character: the elision adds nothing to pay
// for on the rows that were already fine.
func TestSubagentCardLatestToolShortCommandIsUntouched(t *testing.T) {
	fs := &fanoutState{
		StepID: "fanout-short", Verb: "continue", Running: true,
		Tasks: []FanoutTaskState{{
			Title: "Execute plan 302", AgentID: "agent-1", Status: "running",
			ToolTotal: 1, RecentTools: []fanoutToolEntry{{StepID: "tool-1", Label: "sleep 8"}},
		}},
	}
	f := (&Reducer{}).emitFanoutFrame(fs)
	var rows []string
	for _, line := range renderFrameLines(f, 120, DiffThemeDark, 0) {
		if plain := stripANSI(line); strings.TrimSpace(plain) != "" {
			rows = append(rows, plain)
		}
	}
	if len(rows) != 3 || !strings.HasSuffix(rows[2], "└ sleep 8") || strings.Contains(rows[2], "…") {
		t.Fatalf("short command row changed:\n%s", strings.Join(rows, "\n"))
	}
}

// The elision is the third layer's alone: a failed task's reason still wraps,
// because a reader has to be able to read all of it.
func TestSubagentCardReasonStillWraps(t *testing.T) {
	const reason = "The provider is rate-limiting this account — available again in 1h 3m, at 2026-09-07 15:40 CST."
	fs := &fanoutState{
		StepID: "fanout-reason", Verb: "run", Running: false,
		Tasks: []FanoutTaskState{{
			Title: "read-only investigation", AgentID: "agent-1", Status: "failed",
			Error: reason,
		}},
	}
	f := (&Reducer{}).emitFanoutFrame(fs)
	var rows []string
	for _, line := range renderFrameLines(f, 60, DiffThemeDark, 0) {
		if plain := stripANSI(line); strings.TrimSpace(plain) != "" {
			rows = append(rows, plain)
		}
	}
	// header + task row + the reason on at least two rows.
	if len(rows) < 4 {
		t.Fatalf("card draws %d rows, want the failure reason wrapped onto more:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	for _, row := range rows {
		if strings.HasSuffix(row, "…") {
			t.Fatalf("a row was elided, want the reason wrapped whole: %q", row)
		}
	}
}

// The pause that replaces the last call is the third layer too, so it obeys the
// same one-row rule.
func TestSubagentCardPausedRowIsOneRow(t *testing.T) {
	fs := &fanoutState{
		StepID: "fanout-paused", Verb: "continue", Running: true,
		Tasks: []FanoutTaskState{{
			Title: "Execute plan 302", AgentID: "agent-1", Status: "running", Paused: true,
			ToolTotal: 1, RecentTools: []fanoutToolEntry{{StepID: "tool-1", Label: strings.Repeat("very long command ", 40)}},
		}},
	}
	f := (&Reducer{}).emitFanoutFrame(fs)
	var rows []string
	for _, line := range renderFrameLines(f, 120, DiffThemeDark, 0) {
		if plain := stripANSI(line); strings.TrimSpace(plain) != "" {
			rows = append(rows, plain)
		}
	}
	if len(rows) != 3 || !strings.HasSuffix(rows[2], "waiting for your message") {
		t.Fatalf("paused row = %q", strings.Join(rows, "\n"))
	}
}
```

（此时**先不要**动 `chat_session_test.go`：那两条用例要用到 Step 2 新增的 `fanoutActivityPrefix` / `isFanoutActivityLine`，放到 Step 5。）

**Verify**：`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` 后

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentCard' -count=1
```

→ 预期**失败**：`TestSubagentCardLatestToolIsOneElidedRow` 的四个子用例都因 `card draws N rows, want 3` 而红（120 列长命令今天 5 行；400 个 `x` 今天 6 行＝命令折成 4 行；60 列与宽字命令同理）；`TestSubagentCardLatestToolShortCommandIsUntouched`、`TestSubagentCardReasonStillWraps`、`TestSubagentCardPausedRowIsOneRow` 应当**通过**（它们就是“行为不变”的证据）。
如果长命令用例 **不失败**，说明症状已不存在或定位错了：STOP，汇报现场。
（`-run 'TestSubagentCard'` 也会连带跑既有同名族用例——它们必须保持绿。）

### Step 2：落地角色判定常量与谓词

按“修复设计 / 生产改动 1”在 `pkg/tui/render.go` 的 `isFanoutTaskLine` 之后加 `fanoutActivityPrefix` 与 `isFanoutActivityLine`。

**Verify**：`gofmt -l pkg/tui` → 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` → 无输出。

### Step 3：body 循环只给第三层一行

按“修复设计 / 生产改动 2”改 `renderFanout` 的 body 循环（`render.go:5474` 处的 `wrapped := wrapCardLine(...)` 之后加条件分支）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentCard' -count=1` → `ok`（Step 1 的 4 个用例全绿 + 既有同名族用例保持绿）。

### Step 4：把第三层标记收敛成常量

按“修复设计 / 生产改动 3”把 `pkg/tui/reducer.go:2552` 和 `:2558` 的 `add("  └ ", …)` 换成 `add(fanoutActivityPrefix, …)`。

**Verify**：`grep -n 'add("  └ "' pkg/tui/reducer.go` → **无输出**；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentCard' -count=1` → `ok`。
（注意：`grep -n '"  └ "' pkg/tui/reducer.go` 仍会有输出——`:2889`、`:3316`、`:3328` 与注释是图片 marker / 折叠块的用途，**不在本次范围**，别去动它们。）

### Step 5：补两条契约用例

**5a. 生产者/消费者共用标记**——在 `pkg/tui/chat_session_test.go` 的 `TestFanoutContentAttributesEveryLineToItsTask`（`chat_session_test.go:23779-23809`）之后新增：

```go
// The third layer is marked once — fanoutActivityPrefix — and the renderer keys
// its one-row rule on that same marker, so a change on either side fails here
// instead of silently painting a paragraph again.
func TestFanoutThirdLayerUsesTheActivityPrefix(t *testing.T) {
	for _, tc := range []struct {
		name string
		task FanoutTaskState
		want string
	}{
		{name: "latest tool", task: FanoutTaskState{
			Title: "probe", AgentID: "task-a", Status: "running", ToolTotal: 1,
			RecentTools: []fanoutToolEntry{{Label: "sleep 8"}},
		}, want: "sleep 8"},
		{name: "paused", task: FanoutTaskState{
			Title: "probe", AgentID: "task-a", Status: "running", Paused: true,
		}, want: subagentPausedCardLine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, _, _ := renderFanoutContent(&fanoutState{Tasks: []FanoutTaskState{tc.task}})
			var marked []string
			for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
				if isFanoutActivityLine(line) {
					marked = append(marked, line)
				}
			}
			if len(marked) != 1 || !strings.HasPrefix(marked[0], fanoutActivityPrefix) || !strings.Contains(marked[0], tc.want) {
				t.Fatalf("third layer = %q, want one %q row carrying %q", marked, fanoutActivityPrefix, tc.want)
			}
		})
	}
}
```

**5b. 第三层行仍归属该任务（点击归属）**——第三层被截成一行后，`fanoutLineOwners` 必须仍把这一行算给该任务；用 `renderFrameLinesWithAgents`（签名 `(f Frame, width int, theme DiffTheme, spinnerPhase int, cwdOpt ...string) ([]string, []string)`，定义在 `pkg/tui/reducer_test.go`）断言：

```go
// Cutting the third layer to one row must not cost its click: the row still
// belongs to the task whose call it names.
func TestSubagentCardElidedToolRowKeepsItsTask(t *testing.T) {
	fs := &fanoutState{
		StepID: "fanout-owner", Verb: "continue", Running: true,
		Tasks: []FanoutTaskState{{
			Title: "Execute plan 302", AgentID: "subagent-7", Status: "running",
			ToolTotal: 1, RecentTools: []fanoutToolEntry{{StepID: "tool-1", Label: strings.Repeat("python3 - <<'EOF' ", 20)}},
		}},
	}
	f := (&Reducer{}).emitFanoutFrame(fs)
	lines, agents := renderFrameLinesWithAgents(f, 120, DiffThemeDark, 0)
	if len(lines) != 3 || len(agents) != 3 {
		t.Fatalf("card renders %d rows with %d owners, want 3 and 3", len(lines), len(agents))
	}
	if agents[2] != "subagent-7" {
		t.Fatalf("third-layer row owner = %q, want subagent-7", agents[2])
	}
}
```

（`renderFrameLinesWithAgents` 的 `agents[i]` 与第 `i` 行一一对应；返回行已去掉尾部空行，所以 header=0、任务行=1、第三层=2。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestFanoutThirdLayer|TestSubagentCardElidedToolRow' -count=1` → `ok`。
如果 5b 的 `agents[2]` 不是该任务，说明行归属被你改坏了：先修 `owner(contentLine, …)`，别改断言。

### Step 6：包回归 + 相邻包回归

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tool -count=1
CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui
gofmt -l pkg/tui
git status --porcelain
```

**Verify**：两次测试输出 `ok`；vet 与 gofmt 无输出；`git status` 只列出 Scope 里的 4 个文件（若 owner 或别的会话在工作树里另有改动，那些行不是你的，别动）。

### Step 7：真机验收（tmux + fake provider，必做）

`subagent-steer` 模式正好造出截图那类卡片：主会话派发 `subagent_run`，子代理第一条请求返回一条 `shell` 调用，命令文本就是 `start` 的 answer-text 参数（`.claude/skills/run-forebrain/fake_provider.py:859-901`，默认 `sleep 8`）。把它换成长命令即可。

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-subcard FOREBRAIN_RUN_SESSION=fb-subcard FOREBRAIN_RUN_PORT=8763
D=.claude/skills/run-forebrain/driver.sh
$D build
# 一条简单命令 + 超长注释：label 就是整条命令（长），但命令本身是 `sleep`，
# 不会触发审批面板（`;` 拼出的复合命令有可能触发，别用）。
LONG="sleep 9 # $(python3 -c 'print("x"*220)')"
ENABLE_SUBAGENT=1 $D start subagent-steer "$LONG"
$D submit 'dispatch the probe'
$D wait 'sleep 9 #' 30          # 第三层出现（子代理发起了 shell 调用）
$D screen | sed -n '/Running 1 general-purpose task/,+5p'
```

**预期（120×40 终端）**：卡片第三层**只有一行**，以 `…` 结尾；该行宽度 ≤ 108 列（120 − `viewportRightPadding` 2 − `contentRightMargin` 10），文字部分 ≈ 102 列；紧跟其后的下一行是空行/状态行，**没有**命令续行。
若屏幕出现审批面板（`sleep` 加了注释理论上不会），`$D key Enter` 批准后重看，**不要**为验收改产品代码。

**7b. 全文可达（“放弃本行全文”的兜底）**：在同一屏上进入该任务视图（fake 的派发标题是 `network probe`）——按 `Down`, `Down`, `Enter`（run-forebrain skill 的 subagent-steer 配方原文）；也可点该任务行：先 `ROW=$($D screen | grep -n 'network probe' | head -1 | cut -d: -f1)`，再 `$D click "$($D col 'network probe' "$ROW")" "$ROW"`（坐标都是 1 基）。然后：

```bash
$D screen | sed -n '1,20p'
```

**预期**：子代理视图里它自己的 `shell` 工具卡头部写 `Ran sleep 9 # xxx…` 并**换行成多行**，最后一行的**结尾是那 220 个 `x` 的末尾**（即以 `xxx` 收尾，不是以 `…` 收尾）——这正是“头部不截断、全文可读”的证据。看不到就说明“全文仍有地方可读”这前提不成立：STOP 汇报，别在本期另造展开手段（owner 已裁决只做单行截断）。

**7c. 短命令对照（回归路径）**：

```bash
ENABLE_SUBAGENT=1 $D start subagent-steer 'sleep 8'
$D submit 'dispatch the probe'
$D wait 'sleep 8' 30
$D screen | sed -n '/Running 1 general-purpose task/,+4p'
```

**预期**：第三层是完整的一行 `└ sleep 8`，**没有** `…`。

**7d. 回放（/resume）**：让这一轮真正结束（`FAKE_SUBAGENT_STEER_FINISH=1` 让子代理第二次请求收尾，派发返回、卡片 settle），再回放同一张卡。用仓内已验过的配方 `RESUME_ID=$($D sid) $D start hang`（回放只读，不需要 provider 回答新请求）。

```bash
FAKE_SUBAGENT_STEER_FINISH=1 ENABLE_SUBAGENT=1 $D start subagent-steer "$LONG"
$D submit 'dispatch the probe'
$D wait 'Ran 1 general-purpose task' 40
RESUME_ID=$($D sid) $D start hang
$D wait 'Ran 1 general-purpose task' 15 || { for i in 1 2 3 4; do $D wheel 60 20 up; done; }
$D screen | sed -n '/Ran 1 general-purpose task/,+5p'
$D stop
```

**预期**：回放出来的卡片第三层仍是**一行**、以 `…` 结尾，与 live 一致。
回放落在转录**底部**——若上面 `sed` 为空，先确认是不是卡片滚出了可见 pane（`$D wheel 60 20 up` 多翻几次再截）；**先排除“滚出屏幕”再判**是否缺第三层。确认不是滚动问题、回放里这张卡真的不带第三层（结构性差异，不是本次改动造成）：STOP 汇报并写进遗留限制，不要为此在本期改回放。

收尾：`$D stop`；`tmux kill-session -t fb-subcard` 如有残留。

若出现 `open database: migrate state schema: no such module: fts5`：那是构建漏了 `-tags fts5`，用 `$D build`（自带 tag）重来，别去“修”产品代码。
验收只用隔离 `FOREBRAIN_HOME`（driver 默认 `$TMPDIR/forebrain-run-subcard/home`），不得污染真实 `~/.forebrain`。

### Step 8：回填与收口

1. 把本文件复制到 `docs/plan/SUBAGENT_CARD_LATEST_TOOL_ONE_LINE_PLAN.md`，Status 行改为“已实施并真机验收通过（日期）”，补“验收记录”章节（场景→证据，含 7a/7b/7c/7d 的 `$D screen` 截取）。
2. 报告：根因链（本文件 §Current state 的三层）、改动清单（4 个文件）、验证证据（红→绿、真机四组截图/文本）、遗留限制（第三层不提供 tooltip/展开；完整命令在子代理视图；web 未改）。
3. **不 commit**；改动留工作区等 owner 审阅。

## Test plan

- 新增（`pkg/tui/render_test.go`）：
  - `TestSubagentCardLatestToolIsOneElidedRow`：120 列长命令 / 60 列长命令 / 含 CJK 的宽字符命令 / 无空格 400 字符串 → 恒 3 行、第三层行以 `…` 结尾、行宽 ≤ `width-contentRightMargin`。
  - `TestSubagentCardLatestToolShortCommandIsUntouched`：`sleep 8` → 3 行、无 `…`、行尾 `└ sleep 8`。
  - `TestSubagentCardReasonStillWraps`：失败原因在 60 列下仍 ≥2 行、且没有被省略号截断（证明只截第三层）。
  - `TestSubagentCardPausedRowIsOneRow`：暂停行不受长 label 影响，仍是 1 行。
- 新增（`pkg/tui/chat_session_test.go`）：
  - `TestFanoutThirdLayerUsesTheActivityPrefix`：`renderFanoutContent` 产出的第三层行恰好 1 条、以 `fanoutActivityPrefix` 开头、`isFanoutActivityLine` 认它（工具行 + 暂停行两个子用例）。
  - `TestSubagentCardElidedToolRowKeepsItsTask`：渲染后第三行（第三层）的 agent 仍是该任务（点击归属不丢）。
- 结构参照：`render_test.go:873-901`（`TestSubagentCardBodyIsFullBrightness`）；内容层参照 `chat_session_test.go:23779-23809`；归属参照 `chat_session_test.go:23519` 附近的点击用例。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿，`-run 'TestSubagentCard|TestFanoutThirdLayer' -v` 可见 6 个新用例（含子用例）。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 退出 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tool -count=1` 退出 0
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` 无输出；`gofmt -l pkg/tui` 无输出
- [ ] 新用例存在并通过（`-v` 全部 `--- PASS`）：`TestSubagentCardLatestToolIsOneElidedRow`、`TestSubagentCardLatestToolShortCommandIsUntouched`、`TestSubagentCardReasonStillWraps`、`TestSubagentCardPausedRowIsOneRow`、`TestFanoutThirdLayerUsesTheActivityPrefix`、`TestSubagentCardElidedToolRowKeepsItsTask`
- [ ] `grep -n 'add("  └ "' pkg/tui/reducer.go` 无输出（两处已换常量）
- [ ] `grep -n '"  └ "' pkg/tui/reducer.go` 仍有输出，且 `git diff pkg/tui/reducer.go` **只有两处改动**（`+2 −2`；`--stat` 报 4 个变更行）：`add("  └ ", …)` 的两处常量化
- [ ] `grep -n "isFanoutActivityLine" pkg/tui/render.go` 至少 2 处（定义 + 调用）
- [ ] 真机 7a：第三层一行、以 `…` 结尾、行宽 ≤ 108；7b：子代理视图里能看到完整命令尾部；7c：短命令 `└ sleep 8` 无 `…`；7d：回放同样一行
- [ ] `git status --porcelain` 只含 Scope 的 4 个文件（外加他人/owner 既有改动，不入本计划）
- [ ] 计划文件已复制到 `docs/plan/` 并回填验收记录
- [ ] 无任何 commit

## STOP conditions

- `git diff 3dff52cd..HEAD -- pkg/tui/render.go pkg/tui/reducer.go` 里 `renderFanout` body 循环、`renderFanoutContent` 第三层、`isFanoutTaskLine` 的内容与本文件摘录不符（drift）。
- Step 1 的长命令用例**没有**先失败（症状已不存在或定位错了）。
- 定点测试修完仍失败两次（同一用例、同一原因）而找不到根因。
- 修复看起来需要动 Scope 之外的文件（尤其 `pkg/tool/**` 的 label 生成、web 端组件、`reducer.go` 里其它的 `"  └ "` 用法）。
- 发现第三层内容行并非由 `renderFanoutContent` 产生（本计划的核心假设）。
- 真机 7a 里第三层不是一行，而单测全绿（说明真机走的是另一条渲染路径）——停下来取证，不要加第二个渲染补丁。
- 真机 7b 里子代理视图看不到完整命令：说明“放弃本行全文”的兜底不存在，回来找 owner，不要自己加展开手段。
- Step 5b 的归属断言失败而你又解释不清 `owner(...)` 的语义。

## Maintenance notes

- **契约只有一处**：第三层内容行的前缀 = `fanoutActivityPrefix`；渲染器的“一行”规则 key 在 `isFanoutActivityLine`。将来若第三层要多显示一条信息（例如耗时），必须仍写成同一前缀的单行；`TestFanoutThirdLayerUsesTheActivityPrefix` 会先失败提醒你。
- **已知的良性误判**：失败原因若恰好以 `└ ` 开头（`add("  ", reason)` 后就是 `  └ …`），该行会被当成第三层截成一行。概率极低、后果只是那一行被截断；真出现时给原因行换一个不会撞的标记，不要放宽 `isFanoutActivityLine`。
- **owner 已裁决的取舍**：第三层不再有 tooltip/展开，全文看子代理视图（`Ran <命令>` 的卡片头部，无行数上限）；web 不动。后续若 owner 要求“这里也能看全文”，那是新需求，不是本修复的返工。
- **审阅重点**：① 截断用的是卡片可用宽度（`wrapWidth`），不是裸终端宽度——不能吃掉右侧 `contentRightMargin`；② `owner(contentLine, len(wrapped))` 在截断后必须是 1，否则点击归属错位（5b 用例守着）；③ 只截第三层，任务标题/失败原因/`… +N tool uses` 必须维持换行。
- **本次刻意不做**：任务标题限行、`… +N tool uses` 合并、web tooltip、工具卡 header 换行策略——都不在 owner 这次诉求里。
- 相关文件：`pkg/tui/render.go`（`renderFanout`/`fanoutRowLead`）、`pkg/tui/reducer.go`（`renderFanoutContent`/`emitFanoutFrame`）、web 对应实现 `frontend/src/components/chat/SubagentCallCard.vue`。

## 复现记录（本会话，计划阶段）

- 命令：
  ```bash
  export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
  CGO_ENABLED=1 go test -tags fts5 -overlay /tmp/fbtui-repro/overlay.json ./pkg/tui -run TestReproFanoutLatestToolStaysOneLine -count=1 -v
  ```
  （`overlay.json` 把 `$TMPDIR/fbtui-repro/zz_repro_test.go` 映射成 `pkg/tui/zz_repro_test.go`，不改仓库。）
- 结果：`--- FAIL: TestReproFanoutLatestToolStaysOneLine`，`card draws 5 rows, want 3`，5 行里 3 行是命令的续行——与 owner 截图一致。
- 计划阶段 `git status --porcelain` 为空，基线 `3dff52cd`。
- 计划评审（openai/deepseek-v4.1-flash，2026-11-15）独立复现同一症状并在 worktree 里跑绿本计划的三步生产改动（`pkg/tui` 全包 `ok`，`./pkg/turn ./pkg/tool` 两行 `ok`，vet/gofmt 无输出）；本文件已按其指出的 5 处事实错误修订（`"  └ "` 其它用法、无空格长串实为折行、子代理视图不画此卡、live 可用宽度 102/108、web 无 tooltip）。

## Implementation Status

**Verdict: APPROVE（2026-11-15）**

- **Worktree**: `/Users/doudou/workspace/unionj-cloud/forebrain-harness/.worktrees/subagent-card-one-line`，branch `subagent-card-one-line`，base `3dff52cd`。**未合并、未 push**；是否合并由 owner 决定，worktree 保留。
- **Commits**（只在 worktree 分支上）: `c8eb4edf` test(tui): pin a subagent card's latest tool call to one row；`4afc156a` fix(tui): draw a subagent card's latest tool call on one row。
- **任务状态**：
  - Step 1 红测试 — done（先落测试，红态由 reviewer 复验）
  - Step 2 常量+谓词 — done
  - Step 3 body 循环单行 — done
  - Step 4 reducer 两处常量化 — done
  - Step 5 契约用例 5a/5b — done
  - Step 6 包/相邻包回归 — done
  - Step 7 真机 7a/7b/7c/7d — done（reviewer 亲自重跑）
  - Step 8 — partially：8.1 由 reviewer 执行（本文件已复制到主树 `docs/plan/SUBAGENT_CARD_LATEST_TOOL_ONE_LINE_PLAN.md`）；8.2 执行者与 reviewer 均已报告；8.3 主树零改动，commit 只存在于 worktree 分支

**Reviewer 自己跑过的验证（全部在 worktree 内）**

| 检查 | 结果 |
|---|---|
| 6 个新用例（7 个子用例）`-v` | 全 `--- PASS` |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok 87.553s` |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tool -count=1` | `ok 3.334s` / `ok 13.176s` |
| `go vet -tags fts5 ./pkg/tui`；`gofmt -l pkg/tui` | 均无输出 |
| `git status --porcelain`；`git diff --stat 3dff52cd..HEAD` | 空；4 个文件 `+201 −2`，全在 Scope 内 |
| 红态复验（`-overlay` 把 `render.go`/`reducer.go`/`chat_session_test.go` 换回 `3dff52cd` 版本，不动仓库） | `TestSubagentCardLatestToolIsOneElidedRow` 4 子用例全红：`card draws 5 / 10 / 4 / 6 rows, want 3` |
| `grep -c 'add("  └ "' pkg/tui/reducer.go` / `grep -c '"  └ "'` / `grep -c isFanoutActivityLine pkg/tui/render.go` | `0` / `6`（2889/3316/3328 等既有站点未动）/ `4` |
| 真机 7a（`sleep 9 # <220 个 x>`，120×40 tmux，隔离 home） | 第三层**一行 108 列**，以 `…` 结尾，其后空白行、无续行 |
| 真机 7b（`Down`,`Down`,`Enter` 进任务视图） | `● Ran sleep 9 #` 头部换行 3 行（104+104+12＝220 个 `x`），末尾 `9.0s`，**无** `…` → 全文兜底成立 |
| 真机 7c（`sleep 8` 对照） | `└ sleep 8` 原样一行，无 `…` |
| 真机 7d（settle 后 `RESUME_ID=$($D sid) $D start hang`） | 回放卡第三层同样 108 列一行、`…` 结尾 |

**Deviations**：执行者记录的 8763 端口被别的会话的 fake provider 占用（`lsof` 有进程监听），改用 8871 重跑；环境问题，无产品改动。除此之外与计划一致，无未记录偏离。

**Not done / deferred**：无功能项遗留。Maintenance 里列明的「本次刻意不做」（任务标题限行、`… +N tool uses` 合并、web tooltip、工具卡 header 换行策略）保持不做；`frontend/`、`pkg/tool/**` 未改。真机验收只用 fake provider（本仓裁决：agent 验收不烧真配额）。
