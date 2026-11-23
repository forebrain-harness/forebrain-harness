# Plan 130：apply_patch 与 shell 命令改文件时，卡片 output 区显示 diff 块（对齐 Claude Code 的 bashEditDiff）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：下列 grep 各有结果——
> `grep -n "func formatShellStep" pkg/tool/format.go`、`grep -n "func shellOutputMetadataOnly" pkg/tool/format.go`、
> `grep -n "attachTurnDiff(entry, outputPath" pkg/tool/file_tools.go`、`grep -n "maybeHandleApplyPatchCommand(ctx, st, rt, cmd)" pkg/tool/shell_tool.go`、
> `grep -n "func ExtractTurnDiffPayload" pkg/turn/events.go`、`grep -n "func renderTurnDiffCard" pkg/tui/render.go`、`grep -n "func toolSpecificMaxLines" pkg/tui/reducer.go`。
> 本计划不依赖 122–129，可以随时执行。
>
> **Drift check（先跑）**：`git diff --stat c72da06 -- pkg/tool/format.go pkg/tool/file_tools.go pkg/tool/shell_tool.go pkg/turn/events.go pkg/tui/render.go pkg/tui/reducer.go`
> 有输出就把下面「Current state」的摘录逐条对照现网代码；锚点找不到或内容不符即 STOP 1。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（新增一条在每个可能改文件的 shell 命令前后跑 git 的路径；只读用户仓库、写私有目录）
- **Depends on**: —
- **Category**: bug（owner 2026-10-25：「显示 diff 块的功能有一个严重问题」）
- **Planned at**: dev `c72da06`，2026-10-25

## Why this matters

只有 `edit_file` / `write_file` 完成后卡片 output 区才出现 diff 块。模型用 `apply_patch`（在 shell 工具里执行的
heredoc 命令）或普通 shell 命令（`cat > f <<EOF`、`python3 -c "open(...).write(...)"`、`sed -i` …）创建或修改
文本文件时，卡片里没有 diff 块：

- **apply_patch**：引擎其实已经算好了每个文件的 diff（`output.files[i].turn_diff`），但格式化器只认顶层
  `output.turn_diff`，于是 TUI 只显示一行 `Applied patch to 1 file(s).`；web 更糟——把整段转义后的 patch
  当输入 JSON 显示，又把带 `unified_diff` 转义字符串的 `files` 数组塞进卡片底部的 `output:` JSON 块
  （本机真实库里就有这样的行）。owner 原话：「web 直接显示该 JSON 是 bug，必须显示对用户友好的 diff 块」。
- **普通 shell 命令**：引擎根本没有捕获命令改了哪些文件的机制。

Claude Code（本机 2.1.301 二进制里的 `bashEditDiff`）的做法：每个可能改文件的 Bash 命令前后，各用一个**私有
git 索引**给工作树拍快照（不碰用户仓库），命令成功后 `diff-tree` 两个快照，把至多 5 个文件的 hunk 显示在 Bash
结果的 stdout/stderr 下面；只用于显示，不进模型上下文。本计划把这套语义搬进 forebrain，并把 apply_patch 已有的
每文件 diff 用同一种 diff 块显示出来；回合级的 `turn_diff_updated` 事件（web 消息底部的 diff 卡）也一并覆盖这两类改动。

### owner 的裁定（2026-10-25，必须遵守）

1. **always-on**：不加任何配置项、环境变量或开关，开箱即用（Claude Code 的 `bashEditDiffEnabled` /
   `CLAUDE_CODE_BASH_EDIT_DIFF` 都**不**照搬）。内部的超时、连续两次失败后放弃等保护照做，它们不是开关。
2. **不要** Claude Code 在会话第一张 diff 卡下的长说明句「(files that changed while this command ran, by the
   command or by another process; a convenience view, not a review or audit of the command)」。其余短注照搬。
3. **web 前端零改动、免原型**：web 卡片正文就是引擎产出的 Markdown（`display_body`），diff 块以 ```` ```diff ````
   代码块呈现——与现在 `edit_file` 卡的 diff 块同一条渲染路径。
4. 与本次改动无关的现状保持不变：普通 shell 卡的 `input:` / `output:` JSON 块照旧（只去掉其中承载文件改动的键），
   `edit_file` / `write_file` 卡完全不动。
5. PostToolUse hook 的 `tool_response` 对齐是另一份计划（131），本计划不碰 `pkg/hook`。

### 与 Claude Code 语义逐条对照（实现必须符合左列；右列是本计划的落点）

| Claude Code 2.1.301 | 本计划 |
|---|---|
| 只在 git 工作树里生效；不是 git 仓库就什么都不显示 | 同（`git rev-parse` 失败 → 无 diff、无注） |
| 只读命令、后台命令不拍快照 | 只读命令（`safety.ClassifyShellCommand(...) == ShellMutationReadOnly`）不拍；forebrain 的 shell 没有后台参数 |
| 命令被中断 / 失败（按退出码语义表）→ 不计算 diff | 同；退出码语义表照搬（见 Step 3.6） |
| 整条命令就是一个会移动工作树的 git 命令（checkout/switch/stash/pull/merge/rebase/reset/restore/clean/cherry-pick/revert）→ 不拍快照，显示 `(file diff skipped for this git command)` | 同（正则照搬） |
| 复合命令里含上述 git 子命令 → 正常 diff，另加注 `(a git step in this command can move the working tree, so these may be its changes, not edits)` | 同（非 verbose 时 Claude Code 只显示文件头；forebrain 用卡片折叠代替 verbose，正文给全 hunk） |
| 私有 GIT_DIR：`objects/info/alternates` 指向用户对象库、种子索引复制用户 index、`info/attributes` 关掉所有 filter、隔离全局/系统配置 | 同（Step 3.3） |
| 快照 = `status --porcelain=v2 -z` → `update-index` → `write-tree` → `commit-tree` 挂到私有 `refs/heads/main` | 同（Step 3.4） |
| 首次快照超时 10s，之后 750ms（Windows 2000ms）；同一仓库连续失败 2 次后本进程内放弃 | 同 |
| >20MB 或不可读的文件不哈希，按「大小+mtime」判变化，计入 moreFiles 并标 unavailable | 同 |
| 至多 5 个文件带 hunk；单文件 hunk ≤400 行且 ≤64000 字符，否则不显示 hunk、计入 moreFiles；空文件的新建/删除列在最前且无 hunk | 同 |
| `changedFiles`：所有已知改动文件的绝对路径，≤200 个（给 hook 用） | 同（本计划只产出字段；hook 消费见 131） |
| 同仓库有另一条命令同时在跑 → `shared`，加注 `(another command ran in this repository at the same time; a change made by either may show under either result)` | 同（forebrain 的子代理可并行跑 shell） |
| 每个文件一行 `Updated|Created|Deleted <path> (+A -D)`，下面是 hunk；之后 `… N more files changed` 或 `N files changed (binary, mode only or too large to show)`，unavailable 时追加 ` (part of the diff is unavailable)` | 同（文案逐字照搬，单复数同 Claude Code） |
| 只有注、没有文件时：`(file diff unavailable for this command[; N files changed])` / shared 注 | 同 |
| 结果里显示在 stdout / stderr 之后；不进模型上下文 | 同：只写进完成输出（`completionOutput`），不写进给模型的 `resultPayload` |
| 会话第一张卡的长说明句 | **不要**（owner 裁定 2） |
| `bashEditDiffEnabled` / `CLAUDE_CODE_BASH_EDIT_DIFF` 开关 | **不要**（owner 裁定 1） |
| Linux 上用 inotify 写观察器替代 git 快照的快路径 | 不照搬：纯性能实现，输出语义与 git 快照路径相同 |
| （Claude Code 没有 apply_patch） | apply_patch 的每文件 diff 用同一种 diff 块显示，每个文件都有头行；不受 5 文件上限约束（diff 是精确的），单文件同样受 400 行 / 64000 字符限制，超限只显示头行 |

## Current state

所有行号基于 dev `c72da06`。

### 1. 卡片正文的来源

`pkg/tool/format.go` 的 `FormatToolStepResult` 产出 Markdown 正文：TUI 帧的 `Content` 就是它；web 的 `display_body`
也是它（`pkg/tool/notification.go:165` `displayBody, _ := FormatToolStepResult(evt, DefaultMaxFormattedBody)`，
gateway `pkg/gateway/server.go:453` 写进 `data["display_body"]`，web `ToolCallCard.vue` → `ToolOutput.vue` 用
`MessageResponse` 按 Markdown 渲染）。`DefaultMaxFormattedBody = 120_000`（`format.go:22`），超出由
`clampBody`（`format.go:3616`）截断并追加 `_(formatted body truncated for display)_`。

`format.go:129-133`：shell 走专用格式化器——

```go
	switch toolName {
	case "read_file":
		body, truncated = formatReadFileStep(evt, maxBytes)
	case "shell":
		body, truncated = formatShellStep(evt, maxBytes)
```

`format.go:705-748`（原样）：

```go
func formatShellStep(evt StepEvent, maxBytes int) (string, bool) {
	sb := strings.Builder{}
	sb.WriteString("**shell**\n\n")
	if len(evt.Input) > 0 {
		if b, err := json.MarshalIndent(evt.Input, "", "  "); err == nil {
			sb.WriteString("input:\n\n```json\n")
			sb.WriteString(string(b))
			sb.WriteString("\n```\n\n")
		}
	}
	if len(evt.Output) > 0 {
		stdout := trimBlankEdgeLines(stringFromAny(evt.Output["stdout"]))
		stderr := trimBlankEdgeLines(stringFromAny(evt.Output["stderr"]))
		if stdout != "" {
			sb.WriteString("stdout:\n\n```")
			sb.WriteString(shellOutputLang(evt, stdout))
			sb.WriteString("\n")
			sb.WriteString(stdout)
			sb.WriteString("\n```\n\n")
		}
		if stderr != "" {
			sb.WriteString("stderr:\n\n```text\n")
			sb.WriteString(stderr)
			sb.WriteString("\n```\n\n")
		}
		if note := omittedBytesNote(evt.Output); note != "" {
			sb.WriteString(note)
			sb.WriteString("\n\n")
		}
		renderOutput := shellOutputMetadataOnly(evt.Output)
		if len(renderOutput) > 0 {
			if b, err := json.MarshalIndent(renderOutput, "", "  "); err == nil {
				sb.WriteString("output:\n\n```json\n")
				sb.WriteString(string(b))
				sb.WriteString("\n```")
			}
		}
	}
	if diagSection := lspDiagnosticsSection(evt.Output); diagSection != "" {
		sb.WriteString("\n\n")
		sb.WriteString(diagSection)
	}
	return clampBody(sb.String(), maxBytes)
}
```

`format.go:3539-3551`：

```go
func shellOutputMetadataOnly(output map[string]any) map[string]any {
	if len(output) == 0 {
		return output
	}
	dup := make(map[string]any, len(output))
	for k, v := range output {
		if k == "stdout" || k == "stderr" {
			continue
		}
		dup[k] = v
	}
	return outputWithout(dup, "turn_diff", "lsp_diagnostics")
}
```

`format.go:3553` `func extractTurnDiff(v any) (event.Summary, bool)`：把 `event.Summary`（直播）或等价 map（重放）
归一成 `event.Summary{Path, Added, Deleted, UnifiedDiff}`。辅助函数：`stringFromAny`、`intFromAny`、`boolFromAny`
（`format.go:3173`）、`trimBlankEdgeLines`（`format.go:687`）。`edit_file` 的 diff 段是 `turnDiffSection`
（`format.go:3403`，形如 ``turn diff: `<abs>` (+N/-M)`` + ```` ```diff ```` 代码块）——**本计划不改它**。

把 diff 当字符串放进代码块的现成做法（围栏长度自适应）见 `pkg/tui/render.go` `normalizeRetrievedOutputMarkdown`：

```go
	fence := "```"
	for strings.Contains(literal, fence) {
		fence += "`"
	}
```

### 2. apply_patch：已经算好每文件 diff，但没人显示

apply_patch 不是独立工具：`pkg/tool/shell_tool.go:134` 在 shell 工具里拦截——

```go
			if handled, out, err := maybeHandleApplyPatchCommand(ctx, st, rt, cmd); handled {
				return out, err
			}
```

`pkg/tool/file_tools.go:1539` `func applyPatchOperations(...)` 写完文件后（`:1800-1811`）：

```go
		outputPath := abs
		if plan.MoveAbs != "" {
			outputPath = plan.MoveAbs
		}
		entry := map[string]any{"abs_path": outputPath, "operation": string(plan.Kind)}
		if plan.MoveAbs != "" {
			entry["source_abs_path"] = abs
			entry["move_path"] = plan.MoveOpPath
		}
		attachTurnDiff(entry, outputPath, plan.Before, plan.After)
		files = append(files, entry)
```

`operation` 取值 `"add"` / `"update"` / `"delete"`（`file_tools.go:1199-1201`）。`attachTurnDiff`
（`pkg/tool/workspace.go`）把 `event.Build(absPath, before, after)` 的结果写到 `entry["turn_diff"]`。
完成输出（`:1832-1840`）是 `{"stdout": "Applied patch to N file(s).\n…", "stderr": "", "exit_code": 0,
"stdout_bytes", "stderr_bytes", "apply_patch": true, "files": files}` 加执行元数据。

本机 2026-10 的真实 web 卡片正文（`fb_session_events.payload_json → $.display_body`）：`**shell**`、
`input:` 里是整段 `<<'PATCH'…` 转义的 patch、`stdout: Applied patch to 1 file(s).`、
`output:` JSON 里 `files[0].turn_diff.unified_diff` 是一整串转义 diff。

### 3. 普通 shell：没有任何文件改动捕获

`pkg/tool/shell_tool.go` 的 `NewShellTool` 处理函数：

- `:164-166`：

```go
			assessment := safety.AssessShellCommand(cmd)
			mutation := safety.ClassifyShellCommand(cmd, shellAllowedRoots(ctx, st, rt), wdAbs)
			readOnlyCommand := mutation == safety.ShellMutationReadOnly
```
（`wdAbs, err := WorkingDir(ctx)` 在 `:160`）

- `:233-245`（审批门之后真正执行）：

```go
			out, err := runSandboxedShellCommand(ctx, st, rt, sandboxedShellRequest{
				command: cmd, timeoutMs: in.TimeoutMs, sandboxPermissions: in.SandboxPermissions,
				additionalPermissions: in.AdditionalPermissions,
				prefixRule:            append([]string(nil), in.PrefixRule...),
				profile:               profile, profileElevation: profileElevation,
				cwd: wdAbs, toolName: "shell", toolDescription: shellToolDescription,
			})
			// The command finished either way; files it changed outside the
			// edit tools still have to be re-synced. DidRunShell returns
			// immediately, so it adds nothing to the shell's own latency.
			if ci := codeIntelOf(rt); ci != nil {
				ci.DidRunShell(ctx)
			}
			return out, err
```

- `:258-273` `type sandboxedShellRequest struct { command, timeoutMs, sandboxPermissions, additionalPermissions,
  prefixRule, profile, profileElevation, exclusiveWritablePaths, cwd, toolName, toolDescription }`。
- `runSandboxedShellCommand`（`:275`）：`:476-479` 第一次 `manager.RunCommand`，`context.Canceled` /
  `DeadlineExceeded` 直接 `return "", runErr`；`:556` `exitCode := res.ExitCode`（`runErr != nil && exitCode == 0`
  时置 1）；`:571-594` 给模型的 `resultPayload`（stdout/stderr/exit_code…，`:594` 处 `json.Marshal`）；`:595-608` 给界面的
  `completionOutput`；`:619-630` `captureCompletion := func() { CaptureToolCompletion(ctx, ToolCompletionPayload{Output:
  appendToolExecutionMetadata(ctx, st, completionOutput, rt, safety.ToolKindShell), …}) }`；
  `:632` 沙箱不可用、`:641` 沙箱拒绝两条错误路径各自 `captureCompletion()`；**`:644` 是唯一的成功路径**
  `captureCompletion(); return string(b), nil`。
- 只读分类已核实：`cat > a.txt <<'EOF'…`、`python3 -c "open(...).write(...)"`、`sed -i …`、`echo hi >> a.txt`、
  `git checkout main` → 非只读；`cat a.txt`、`git status`、`ls -la` → 只读。
- 现有 git 调用写法参考 `pkg/lsp/manager.go:1208`（`exec.CommandContext(ctx, "git", "-C", root, …)`）。
- 命令切分工具：`pkg/safety/shell.go:130` `func SplitTopLevelWithSeparators(s string) []Segment`
  （按 `&&` `||` `|` `&` `;` 换行切顶层段，`Segment.Text` 是去空白的段原文）、`:2765`
  `func SplitShellWords(s string) []string`（按 shell 规则分词，去引号）。

### 4. 回合级 diff 事件

`pkg/turn/events.go:108` `CanonicalEventsFromStep` 对每个完成步骤调 `ExtractTurnDiffPayload(canonical.Payload)`，
命中就追加一条 `turn_diff_updated`（id = 步骤 id + `-diff`）；gateway `pkg/gateway/wsevents.go:148` 同样调用。
`ExtractTurnDiffPayload`（`events.go:124-175`）只读 `output["turn_diff"]`，产出**单文件**
`event.TurnDiffUpdatedPayload{Files: []event.TurnDiffFile{file}, Summary: "<path> (+A/-D)"}`；hunk 来自
`event.Parse(diffText)`，解析不出文件时用 `rawTextHunks(diffText)`；状态缺省用 `inferTurnDiffStatus(added, deleted)`
（`:296`，返回 `added` / `deleted` / `modified`）。web `parseTurnDiffPayload`（`frontend/src/lib/forebrainGatewayRuntime.ts:556`）
本来就逐个读 `payload.files[]`，多文件载荷前端零改动可用。

### 5. TUI 的 diff 卡

- `pkg/tui/render.go:2060-2065`：

```go
		if isDiffTool(title) {
			if block, ok := renderTurnDiffCard(f.Content, r.diffTheme, 0); ok {
				_, _ = fmt.Fprintln(r.out, block)
				_, _ = fmt.Fprintln(r.out)
				return
			}
		}
```
`isDiffTool`（`:3104`）只认 `edit_file` / `edit` / `write_file` / `write`。这个分支在 `r.fullBodyMode` 判断（`:2101`）之前。
- `renderTurnDiffCard`（`:5988`）：`looksLikeTurnDiff` 取路径、`extractDiffFence` 取 ```` ```diff ```` 体，
  `event.Parse("diff --git a/" + path + " b/" + path + "\n" + fence)`，然后：

```go
	p := diffPaletteFor(theme)
	lexer, style := resolveDiffHighlight(path, p)
	gutterW := diffGutterWidthFor(f.Hunks)
	bandW := termWidthOrDefault() - 4 // 4 = tool-card prefix width
	if bandW < 20 {
		bandW = 20
	}

	out := make([]string, 0, 16)
	out = append(out, "  └ "+turnDiffSummaryLine(f.Added, f.Deleted))

	for hi, hunk := range f.Hunks {
		if hi > 0 {
			out = append(out, "    "+p.meta("⋮"))
		}
		for _, line := range hunk.Lines {
			for _, row := range renderDiffLine(line, p, gutterW, bandW, lexer, style) {
				out = append(out, "    "+row)
			}
		}
	}
	out = append(out, lspDiagnosticsCardRows(content)...)
	return strings.Join(out, "\n"), true
```
- shell 卡的普通正文：`formatShellOutput(content)`（`:2650`）只抽 `stdout:` / `stderr:` 标签下的围栏体
  （`toolOutputSections`，`:3168`），然后 `formatToolOutputBlock(content)`（`:2693`，首行 `"  └ "`、续行 `"    "`）。
  所以 shell 正文里的 `input:` / `output:` JSON 在 TUI 里从来不显示。
- 折叠：`pkg/tui/reducer.go:2733-2744`：

```go
func toolSpecificMaxLines(title string) int {
	if strings.EqualFold(title, "write_file") ||
		strings.EqualFold(title, "edit_file") {
		return 30
	}
	if isMemoryQueryTool(title) {
		return 10
	}
	return toolOutputMaxLines
}
```
`:2749-2759` `toolPreviewUsesRenderedBody(f Frame) bool`（diff 卡、MCP、记忆等返回 true，折叠预览从已渲染行切）；
`:2894` `maxLines := toolSpecificMaxLines(b.frame.Title)`。`toolStatusFailed(f)`（`render.go:2206`）= `f.ToolMeta.Status == "failed"`。

### 6. 仓库约束（pkg/architecture 测试会卡）

- 文件名最多 2 个下划线，**且测试文件名也算**（`x_y_test.go` 有 2 个）：所以新生产文件最多 1 个下划线。
  本计划新建 `pkg/tool/diff_blocks.go`、`pkg/tool/shell_diff.go`（各自配 `_test.go`）。
- 每个测试文件必须有同名生产文件；每包生产文件 ≤31（`pkg/tool` 现有 21，`pkg/tui` 28——**不要**给 `pkg/tui` 加新文件）。
- `pkg/tool` 的包扇出已在上限 9：新代码只能用标准库和 `pkg/tool` 已经导入的模块内包（`pkg/event`、`pkg/safety` 等），
  **不得新增模块内导入**。
- 测试风格：`pkg/tool` 用标准库 `testing`（见 `format_test.go`、`request_permissions_test.go:207` `TestApplyPatchOperations`
  ——`root := t.TempDir(); t.Chdir(root); st := NewState(root); NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode:
  appcfg.SandboxModeDangerFullAccess}})`，`tool.Handle(ctx, string(args))`）；完成输出用
  `ctx := WithToolCompletionCapture(context.Background())` + `ToolCompletionFromContext(ctx)` 读（见 `shell_tool_test.go:55-92`）。
  `pkg/turn` 测试用 `require`（`events_test.go:73`）。TUI 测试见 `pkg/tui/chat_session_test.go:25500-25560`
  （`sampleTurnDiff`、`stripANSI`、`sp(n)`）与 `render_test.go:508`。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 环境 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"` | — |
| 单包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run '<Name>' -count=1` | `ok` |
| 全量 Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/turn/... ./pkg/tui/... ./pkg/gateway/... ./pkg/run/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool/... ./pkg/turn/... ./pkg/tui/...`；`gofmt -l pkg/` | 无输出 |
| 前端（只确认没被波及） | `cd frontend && pnpm vitest run` | 通过 |
| 真机 | `.claude/skills/run-forebrain/driver.sh`（Step 8） | 见 Step 8 |

（链接器会刷一串 `ld: warning: object file ... built for newer 'macOS' version` —— 已知噪音，忽略。）

## Scope

**In scope**（只改这些）：
- 新建 `pkg/tool/diff_blocks.go`、`pkg/tool/diff_blocks_test.go`
- 新建 `pkg/tool/shell_diff.go`、`pkg/tool/shell_diff_test.go`
- `pkg/tool/format.go`（`formatShellStep`、`shellOutputMetadataOnly`）、`pkg/tool/format_test.go`
- `pkg/tool/file_tools.go`（只在 apply_patch 的 `entry` 上加 `display_path`）、`pkg/tool/request_permissions_test.go`（apply_patch 测试）
- `pkg/tool/shell_tool.go`（`NewShellTool` 处理函数与 `runSandboxedShellCommand` 的接线、`sandboxedShellRequest` 加一个字段）、`pkg/tool/shell_tool_test.go`
- `pkg/turn/events.go`、`pkg/turn/events_test.go`
- `pkg/tui/render.go`、`pkg/tui/reducer.go`、`pkg/tui/render_test.go`（TUI 测试只加在这里）

**Out of scope**（不要碰）：
- `frontend/**`、`ai-elements-vue/**`：owner 裁定 web 零改动。
- `edit_file` / `write_file` 的卡片与 `turnDiffSection`、`renderTurnDiffCard` 的行为（Step 6 允许把其内部的
  hunk 行渲染抽成共用函数，但输出必须逐字节不变——现有测试兜底）。
- 普通 shell 卡里 `input:` / `output:` JSON 块的其余内容（owner 裁定 4）。
- `pkg/hook/**`（131 的事）、`pkg/config/**`（不加任何开关）、审批框 `pkg/tui/overlays.go`。
- 回退 / rewind（`fb_file_changes`）：Claude Code 也不把 Bash 的改动记进 checkpoint，本计划不把 shell 改动写进去。
- 其余工具卡、助手消息、思考卡在 web 与 TUI 之间的显示差异：owner 明确「暂不处理、保持现状」。

## Steps

### Step 1：diff 块的写与读放在一处（新建 `pkg/tool/diff_blocks.go`）

TUI 要从正文里把 diff 块认出来，web 直接渲染正文——两边看到的是同一段 Markdown。写它的函数和读它的函数放在同一个
文件，round-trip 由测试钉死。

```go
// DiffBlock is one changed file a shell card shows as a diff block: a header
// line, then the change as a unified diff.
type DiffBlock struct {
	Verb        string // "Updated", "Created" or "Deleted"
	Path        string // as displayed: relative to the working directory when inside it
	Added       int
	Deleted     int
	UnifiedDiff string // empty: header only (an empty file created/deleted, or a diff too large to show)
}

// DiffBlocks is the file-change part of a shell card: the files, then the short
// notes that follow them.
type DiffBlocks struct {
	Files []DiffBlock
	Notes []string
}
```

**写**：`func diffBlocksMarkdown(blocks DiffBlocks, budget int) string`

- 每个文件：头行 ``<Verb> `<Path>` (+<Added> -<Deleted>)``；`UnifiedDiff` 非空、且该文件的 hunk 行数 ≤400、
  hunk 字符数 ≤64000（「hunk 行」= 第一个以 `@@` 开头的行之后、不以 `@@` 开头的行；字符数 = 这些行的字节长度和）、
  且加上这一段后总长度不超过 `budget` 时，头行后空一行接围栏代码块：围栏从 ```` ``` ```` 开始、只要 diff 文本里
  含有当前围栏就再加一个反引号（同 `normalizeRetrievedOutputMarkdown`），开头一行是 `<fence>diff`，正文是
  `strings.TrimRight(UnifiedDiff, "\n")`，结尾一行是 `<fence>`；否则只写头行。
- 各段之间、各注之间用一个空行分隔；注写在所有文件之后，每条一行。
- `budget <= 0` 视为不限。
- 空 `DiffBlocks` 返回 `""`。

**读**：`func ParseDiffBlocks(body string) (DiffBlocks, bool)` 与 `func HasDiffBlocks(body string) bool`

- 逐行扫描并跟踪围栏：一行 `TrimSpace` 后以 ```` ``` ```` 开头即进入围栏，记下开头的反引号个数 n；围栏内遇到
  `strings.TrimRight(line, " \t\r")` 恰好等于 n 个反引号的行才结束。**头行与注只在围栏外识别**（shell 正文的
  stdout/stderr 都在 ```` ```text ```` 围栏里，不会误判）。
- 头行正则（包级变量，编译一次）：`` ^(Updated|Created|Deleted) `(.+)` \(\+(\d+) -(\d+)\)$ ``。
- 头行之后、下一个非空行若是信息串为 `diff` 的围栏开头，则该围栏体（不含开/闭两行）就是这个文件的 `UnifiedDiff`。
- 注：围栏外、`TrimSpace` 后满足任一条件的行——以 `"(file diff "`、`"(another command ran in this repository"`、
  `"(a git step in this command"`、`"… "` 开头，或匹配 `` ^\d+ files? changed \(binary, mode only or too large to show\) ``。
- `ok = len(Files) > 0 || len(Notes) > 0`。`HasDiffBlocks` 只做「围栏外是否存在头行或注」的快速判断（不收集 diff）。

本文件里还要定义注的文案常量与生成函数（Step 4 的 shell 与 Step 2 的 apply_patch 都用它们，文案逐字照搬 Claude Code）：

```go
const (
	diffNoteSkipped   = "(file diff skipped for this git command)"
	diffNoteShared    = "(another command ran in this repository at the same time; a change made by either may show under either result)"
	diffNoteTreeMoved = "(a git step in this command can move the working tree, so these may be its changes, not edits)"
)
```
- `func diffFileCount(n int) string` → `"1 file"` / `"N files"`。
- 「还有文件」注：有文件时 `fmt.Sprintf("… %d more %s changed", n, file|files)`（`1 more file` / `2 more files`）；
  一个文件都没显示时 `diffFileCount(n) + " changed (binary, mode only or too large to show)"`；`unavailable` 时末尾追加
  `" (part of the diff is unavailable)"`。
- 「不可用」注：`n > 0` 时 `"(file diff unavailable for this command; " + diffFileCount(n) + " changed)"`，否则
  `"(file diff unavailable for this command)"`。

**Verify**：新建 `pkg/tool/diff_blocks_test.go`（标准库 testing），覆盖：
1. round-trip：两个文件（一个带 diff、一个只有头行）+ 两条注 → `diffBlocksMarkdown` → `ParseDiffBlocks` 得回同样的
   `Verb/Path/Added/Deleted/UnifiedDiff`（`UnifiedDiff` 比较时两边都 `TrimRight("\n")`）与注。
2. diff 文本里含一行 `` +```go `` 时围栏变成 4 个反引号，且 round-trip 仍正确。
3. 401 行 hunk 的文件只有头行；`budget` 不够放下第二个文件的 diff 时第二个只有头行。
4. 围栏内的「伪头行」不被识别：正文 ``stdout:\n\n```text\nUpdated `x` (+1 -0)\n```\n`` → `ok == false`。
5. 注的文案：`… 1 more file changed`、`… 2 more files changed`、`3 files changed (binary, mode only or too large to show) (part of the diff is unavailable)`、
   `(file diff unavailable for this command; 1 file changed)`。

`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'DiffBlocks' -count=1` → `ok`。

### Step 2：apply_patch 卡显示 diff 块（不再显示转义 JSON）

2.1 `pkg/tool/file_tools.go` `applyPatchOperations`：在 `entry := map[string]any{"abs_path": outputPath, ...}` 处给
每个文件加 `entry["display_path"] = displayPathFor(ctx, outputPath)`；有移动时再加
`entry["source_display_path"] = displayPathFor(ctx, abs)`。`displayPathFor` 定义在 `diff_blocks.go`：

```go
// displayPathFor is how a card names a file: relative to the call's working
// directory when the file is inside it, absolute otherwise.
func displayPathFor(ctx context.Context, abs string) string
```
（`wd, err := WorkingDir(ctx)`；`err != nil` 或 `filepath.Rel` 结果以 `..` 开头 → 返回 `abs`。）
**不要**改 `stdout` 文案、`files` 的其它键、`payload`（给模型的那份）。

2.2 `diff_blocks.go` 加 `func applyPatchDiffBlocks(output map[string]any) (DiffBlocks, bool)`：仅当
`boolFromAny(output["apply_patch"])` 为真；遍历 `output["files"]`（直播是 `[]map[string]any`，重放是 `[]any`——用
`sliceFromAny` + `mapFromAny`，二者已在 `format.go`）；每个文件：
- `Verb`：`operation == "add"` → `Created`，`"delete"` → `Deleted`，其它 → `Updated`；
- `Path`：`display_path`，缺省用 `abs_path`；有 `source_abs_path` 时为 `"<source_display_path 或 source_abs_path> → <Path>"`；
- `Added/Deleted/UnifiedDiff`：`extractTurnDiff(entry["turn_diff"])`。
apply_patch 没有注。没有文件时返回 `false`。

2.3 `pkg/tool/format.go` `formatShellStep`：
- 开头算 `applyPatch := boolFromAny(evt.Output["apply_patch"])`；**apply_patch 时不写 `input:` 块**（patch 原文就是
  改动本身，下面以 diff 块呈现；完整命令仍在卡头）。
- 在 `omittedBytesNote` 之后、`output:` JSON 之前插入 diff 块：

```go
		if blocks, ok := shellDiffBlocks(evt.Output); ok {
			budget := maxBytes - sb.Len() - 4096 // leave room for the metadata block and the diagnostics
			if section := diffBlocksMarkdown(blocks, budget); section != "" {
				sb.WriteString(section)
				sb.WriteString("\n\n")
			}
		}
```
  `shellDiffBlocks(output)` 定义在 `diff_blocks.go`：apply_patch 用 2.2，否则用 Step 4 的 `shellEditDiffBlocks`
  （Step 4 之前先只接 apply_patch，Step 4 再补另一支）。`budget` 算出来 ≤0 时传 1（只写头行）。
- `shellOutputMetadataOnly`：在现有 `outputWithout(dup, "turn_diff", "lsp_diagnostics")` 里加上 `"shell_edit_diff"`；
  `boolFromAny(output["apply_patch"])` 为真时再去掉 `"files"`。

**Verify**：
- `format_test.go` 新增 `TestFormatToolStepResult_applyPatchShowsDiffBlocks`：构造 apply_patch 完成事件（`files` 里一个
  update 一个 add，`turn_diff` 用 `event.Build` 生成；再构造一份把 `files` 经 `json.Marshal`/`Unmarshal` 变成 `[]any`
  的重放版），两份都断言：含 ``Updated `a.txt` (+1 -1)`` 与 ``Created `b.txt` (+2 -0)``、含 ```` ```diff ````、
  **不含** `unified_diff`、**不含** `"files"`、**不含** `input:`；`ParseDiffBlocks(body)` 得 2 个文件。
- `request_permissions_test.go` 的 `TestApplyPatchOperations` 末尾追加：用 `WithToolCompletionCapture` 的 ctx 调用后，
  完成输出 `files[*].display_path` 为 `existing.txt` / `added.txt`（相对路径）。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'ApplyPatch|FormatToolStepResult|DiffBlocks' -count=1` → `ok`。

### Step 3：shell 命令的改动快照（新建 `pkg/tool/shell_diff.go`）

3.1 **数据形状**（写进完成输出的 `"shell_edit_diff"` 键；只给界面，不给模型）：

```go
// ShellEditDiff is the per-file diff of the working-tree changes one shell
// command made (Claude Code's bashEditDiff). It rides the completion output the
// surfaces render from and never reaches the model.
type ShellEditDiff struct {
	Files        []ShellEditDiffFile `json:"files"`
	MoreFiles    int                 `json:"more_files"`
	ChangedFiles []string            `json:"changed_files,omitempty"` // absolute, at most 200
	Unavailable  bool                `json:"unavailable,omitempty"`
	Skipped      bool                `json:"skipped,omitempty"`
	Shared       bool                `json:"shared,omitempty"`
	TreeMoved    bool                `json:"tree_moved,omitempty"`
}

type ShellEditDiffFile struct {
	Path        string `json:"path"`         // absolute
	DisplayPath string `json:"display_path"` // relative to the command's working directory when inside it
	Status      string `json:"status"`       // "created", "deleted" or "updated"
	Added       int    `json:"added"`
	Deleted     int    `json:"deleted"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}

// DecodeShellEditDiff normalizes the "shell_edit_diff" output value: the struct
// on a live step, its JSON map after a replay.
func DecodeShellEditDiff(v any) (ShellEditDiff, bool)
```
`DecodeShellEditDiff` 照 `format.go` 里 `extractLSPDiagnosticsSummary` 的写法（nil → false；同类型直接返回；
其它 `json.Marshal` + `json.Unmarshal`）。

3.2 **常量**（包级 `var`，测试可改小）：

```go
var (
	shellDiffMaxFiles        = 5          // files shown with hunks
	shellDiffMaxHunkLines    = 400        // per file
	shellDiffMaxHunkChars    = 64000      // per file
	shellDiffMaxChangedFiles = 200
	shellDiffMaxHashBytes    = int64(20_000_000) // bigger files are tracked by size+mtime, never hashed
	shellDiffMaxBlobBytes    = int64(5_000_000)  // a side bigger than this is not read for a diff
	shellDiffMaxExamined     = 50         // candidates read before the rest is only counted
	shellDiffFirstTimeout    = 10 * time.Second
	shellDiffTimeout         = 750 * time.Millisecond // 2s on windows (runtime.GOOS)
	shellDiffGiveUpAfter     = 2          // consecutive failures per repository per process
	shellDiffArgBatchBytes   = 28000
	shellDiffGit             = "git" // the executable; tests point it at a missing binary to inject failures
)
```
所有 git 子进程都用 `exec.CommandContext(ctx, shellDiffGit, …)`。
空 blob：sha1 `e69de29bb2d1d6434b8b29ae775ad8c2e48c5391`，sha256
`473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813`。

3.3 **仓库发现与私有 GIT_DIR**

- 进程级注册表：`var shellDiffRepos = struct { sync.Mutex; m map[string]*shellDiffRepo }{m: map[string]*shellDiffRepo{}}`，
  键是工作树根（`--show-toplevel`）。

```go
type shellDiffRepo struct {
	mu        sync.Mutex // serializes every git call against the private index
	top       string     // work tree root
	seedIndex string     // <git-dir>/index — the user's index, copied once as the seed
	objects   string     // <git-common-dir>/objects
	sha256    bool
	private   string     // private GIT_DIR; "" until set up
	lastTree  string
	large     map[string]string // rel path -> "size mtimeUnixNano" of files not hashed
	largeChanged []string        // rel paths whose signature changed in the latest snapshot
	failures  int
	inflight  map[*shellDiffToken]bool // value: true once the token has seen another run
}
```
- 发现：`git -C <cwd> rev-parse --path-format=absolute --show-toplevel --git-dir --git-common-dir --show-object-format`
  （环境见下方「环境」，超时 2s）。失败或不是 4 行 → 不是 git 工作树（含 git < 2.31）→ 返回 nil。
  `failures >= shellDiffGiveUpAfter` 的仓库也返回 nil（本进程内放弃；之后 nil → 无 diff、无注）。
- 私有目录：`base := filepath.Join(os.TempDir(), "forebrain-shell-diff")`，`os.MkdirAll(base, 0o700)`；
  首次在本进程建目录前清扫：`base` 下名字不以 `<pid>-` 开头、且 `ModTime` 早于 24 小时的子目录 `os.RemoveAll`。
  私有目录名 `fmt.Sprintf("%d-%s-%s", os.Getpid(), sha256(top) 前 12 位十六进制, 4 字节随机十六进制)`，`0o700`。
- 写入（全部 `0o600`，目录 `0o700`）：
  - `HEAD`：`ref: refs/heads/main\n`
  - `config`：
    ```
    [core]
    	repositoryformatversion = 0
    	bare = false
    	logallrefupdates = false
    	untrackedCache = true
    ```
    `sha256` 仓库：`repositoryformatversion = 1`，并追加 `[extensions]\n\tobjectformat = sha256\n`。
  - `objects/info/alternates`：`<objects>\n`；建空目录 `objects/info`、`refs/heads`、`info`。
  - `info/attributes`：`* -filter -working-tree-encoding -text -eol -ident !diff\n`（**安全要点**：仓库
    `.gitattributes` 里的 clean/smudge filter 一律不执行）。
  - `info/exclude`：复制 `<git-common-dir>/info/exclude`（不存在或 >64KB 则写空文件）。
  - `config.global`：先在用户正常环境下跑 `git config --global --get --path core.excludesFile`（超时 750ms）；
    结果非空、是绝对路径、不含 `\n` `\r` `\0` 时写 `[core]\n\texcludesFile = "<path，反斜杠与双引号前加反斜杠>"\n`，否则写空文件。
  - 种子索引：`seedIndex` 存在且 ≤256MB 时复制成 `<private>/index`；复制后 `ls-files -z --stage`，任一条目的 stage 不是 0
    （制表符前最后一个空格分隔字段）或路径不合法（见 3.4 的路径校验）→ 删掉 `<private>/index`（从空索引开始）。
  - `update-index --untracked-cache`；`write-tree`；对结果 `commit-tree`（见 3.4）——这是基线。
  任一步失败 → 按 3.4「失败处理」。
- **环境**（所有 git 子进程，`exec.CommandContext`）：从 `os.Environ()` 去掉**所有**以 `GIT_` 开头的变量，再加：
  `GIT_DIR=<private>`、`GIT_WORK_TREE=<top>`、`GIT_CONFIG_GLOBAL=<private>/config.global`、`GIT_CONFIG_NOSYSTEM=1`、
  `GIT_ATTR_NOSYSTEM=1`、`GIT_NO_REPLACE_OBJECTS=1`、`GIT_NO_LAZY_FETCH=1`、`GIT_TERMINAL_PROMPT=0`、
  `GIT_OPTIONAL_LOCKS=0`（只有 `status` 用 `1`，让它顺手刷新**私有**索引的 stat 缓存）。
  参数前缀：`git -C <top> --literal-pathspecs -c core.quotePath=false -c core.safecrlf=false -c core.askPass= <子命令…>`。
  发现阶段（rev-parse）与读 excludesFile 用「去掉 `GIT_*` + `GIT_TERMINAL_PROMPT=0` + `GIT_OPTIONAL_LOCKS=0`」的环境、不加私有变量。
  用户仓库的 `.git/config`、`~/.gitconfig`、系统配置在快照命令里都不会被读到——这是有意的（fsmonitor、filter、hooks 都不会触发）。

3.4 **快照** `func (r *shellDiffRepo) snapshot(ctx context.Context) (string, error)`（调用方已持有 `r.mu`）

超时：`r.lastTree == ""` 时 `shellDiffFirstTimeout`，否则 `shellDiffTimeout`（Windows 2s）。`r.private == ""` 时先做 3.3 的建立。

1. 删掉 `<private>/index.lock`（忽略错误）。
2. `status --porcelain=v2 -z --untracked-files=normal --ignored=no --no-renames --ignore-submodules=all`，按 `\x00` 切记录：
   - `? <path>`：以 `/` 结尾 → 未跟踪目录（去掉 `/` 后收集），否则 → 改动。
   - `1 <XY> …`：路径是第 8 个空格之后的全部内容；`u <XY> …`：第 10 个空格之后。`Y`（XY 的第二个字符）为 `.`
     → 跳过；`D` → 删除；其它 → 改动。
   - 每个路径先过**路径校验**：非空、不以 `/` 开头、不含控制字符（`\x00-\x1f`、`\x7f-\x9f`、` `、` `）、
     按 `/` 切开没有空段 / `.` / `..`、没有任何一段（小写、去掉末尾的 `.` 和空格后）等于 `.git` 或匹配 `^git~\d+$`；
     Windows 上还不能含 `:`。不合格 → 丢弃。
3. 若改动、删除、未跟踪目录都为空且 `r.lastTree != ""` → `r.largeChanged = nil`，返回 `r.lastTree`。
4. 删除的路径若现在是目录（`os.Lstat` 是目录）→ 当作未跟踪目录 `path/` 处理。
5. 未跟踪目录：按 `shellDiffArgBatchBytes` 分批跑 `ls-files -z --others --exclude-standard -- <dir/>…`，把结果里
   不以 `/` 结尾且过路径校验的加入改动。扩展后改动与删除都为空且 `r.lastTree != ""` → 同第 3 条返回。
6. 逐个改动路径分类（`abs := filepath.Join(top, filepath.FromSlash(rel))`）：
   - 父目录链上每一级 `os.Lstat` 必须是**真目录**（不是符号链接；结果缓存在本次 map 里）→ 否则丢弃；
   - `os.Lstat(abs)`：不存在 → 保留（`update-index --remove` 会处理）；符号链接 → 保留；普通文件：`Size() > shellDiffMaxHashBytes`
     → 大文件；否则 `os.Open` 失败且是权限错误 → 大文件（不可读）；能打开就关掉、保留；其它类型（目录、FIFO、设备）→ 丢弃。
   - 大文件：签名 `fmt.Sprintf("%d %d", size, mtime.UnixNano())`（不可读的用 `"unreadable"`）记入新的 `large` map；
     旧 `r.large` 里没有或签名不同 → 记入 `largeChanged`。最后 `r.large = 新 map`、`r.largeChanged = 本次列表`。
7. 删除：`update-index --force-remove -z --stdin`（stdin 为 `rel\x00` 连接）；保留的改动：`update-index --add --remove -z --stdin`。
8. `write-tree` → 输出 trim 后必须匹配 `^[0-9a-f]{40,64}$`。
9. 新 tree 不等于 `r.lastTree` 时：`commit-tree <tree> -m snapshot`，环境额外加
   `GIT_AUTHOR_NAME=forebrain-shell-diff`、`GIT_AUTHOR_EMAIL=forebrain-shell-diff@localhost`、`GIT_AUTHOR_DATE=1000000000 +0000`，
   COMMITTER 三项同值；输出必须匹配同一正则；写入 `<private>/refs/heads/main`（`commit + "\n"`，`0o600`）。
10. `r.lastTree = tree`、`r.failures = 0`，返回。

**失败处理**（任何一步出错或超时）：`os.RemoveAll(r.private)`；`r.private = ""`、`r.lastTree = ""`、`r.large = nil`、
`r.largeChanged = nil`；`r.failures++`；`slog.Debug("shell diff: snapshot failed", "repo", r.top, "err", err)`；
达到 `shellDiffGiveUpAfter` 时 `slog.Info("shell diff: giving up on repository for this process", "repo", r.top)`；返回 error。

3.5 **比较** `func (r *shellDiffRepo) diffSince(ctx context.Context, before, cwd string) (*ShellEditDiff, error)`（持有 `r.mu`）

1. `after, err := r.snapshot(ctx)`；出错 → 返回 err。
2. `after == before`：`len(r.largeChanged) > 0` → `&ShellEditDiff{MoreFiles: len, ChangedFiles: 绝对路径(largeChanged)[:≤200], Unavailable: true}`；否则 `nil, nil`。
3. `diff-tree -r -z --raw --no-renames --no-ext-diff --no-textconv <before> <after>`：记录格式
   `:<mode1> <mode2> <sha1> <sha2> <status>\x00<path>\x00`（status 取首字母 `A`/`D`/`M`/`T`…）；路径过 3.4 的路径校验，不合格丢弃。
4. 没有记录：同第 2 条（有大文件变化就返回 unavailable 那份，否则 nil）。
5. 选文件（按 Claude Code 顺序）：先放「空文件」——`A` 且 sha2 是空 blob → `created`、`D` 且 sha1 是空 blob → `deleted`，
   无 diff 文本，最多 `shellDiffMaxFiles` 个；再按记录顺序看其余记录，直到已选够 `shellDiffMaxFiles` 个或已读过
   `shellDiffMaxExamined` 个：
   - 读两侧内容：`A` 的旧侧为空、`D` 的新侧为空，其余用 `cat-file -s <sha>`（>`shellDiffMaxBlobBytes` → 跳过）再
     `cat-file blob <sha>`；
   - 任一侧前 8000 字节含 `\x00`（二进制）→ 跳过；两侧字节相同（只改了 mode）→ 跳过；
   - `sum, _ := event.Build(abs, beforeBytes, afterBytes)`；hunk 行数 / 字符数（定义同 Step 1）超
     `shellDiffMaxHunkLines` / `shellDiffMaxHunkChars` → 跳过；
   - 选中：`ShellEditDiffFile{Path: abs, DisplayPath: displayPathRelativeTo(cwd, abs), Status: A→created/D→deleted/其它→updated,
     Added: sum.Added, Deleted: sum.Deleted, UnifiedDiff: sum.UnifiedDiff}`。
   （`displayPathRelativeTo` 是 `displayPathFor` 的纯函数版本，二者共用实现。）
6. `MoreFiles = len(合格记录) - len(已选) + len(r.largeChanged)`；`ChangedFiles` = 合格记录路径 + `largeChanged` 的绝对路径，截到 200；
   `Unavailable = len(r.largeChanged) > 0`。

3.6 **一次 shell 调用的生命周期**

```go
type shellDiffToken struct{}

// shellEditDiffRun is one shell call's watch over the repository it runs in.
type shellEditDiffRun struct {
	repo        *shellDiffRepo
	token       *shellDiffToken
	before      string
	unavailable bool
	skipped     bool
	cwd         string
	left        bool
	shared      bool
}

func beginShellEditDiff(ctx context.Context, command, cwd string, readOnly bool) *shellEditDiffRun
func (r *shellEditDiffRun) finish(ctx context.Context, command string, exitCode int, runErr error) *ShellEditDiff
func (r *shellEditDiffRun) release() // idempotent; safe on nil
```

- `beginShellEditDiff`：`readOnly` → nil。`shellCommandMovesGitTreeAlone(command)` → 返回 `&shellEditDiffRun{skipped: true}`
  （不发现仓库、不拍快照）。否则发现仓库（nil → 返回 nil）；`repo.enter()` 登记 token（在 `repo.inflight` 里；登记后若
  `len(inflight) > 1`，把所有 token 的值置 true）；持 `repo.mu` 拍快照：成功记 `before`，失败记 `unavailable = true`。
- `finish`：`r == nil` → nil；`ctx.Err() != nil` 或 `runErr != nil` 或 `shellExitIsFailure(command, exitCode)` → `r.release()` 后返回 nil；
  `r.skipped` → `&ShellEditDiff{Files: []ShellEditDiffFile{}, Skipped: true}`；`r.release()` 拿到 `shared`；
  `r.unavailable` → `&ShellEditDiff{Files: …{}, Unavailable: true, Shared: shared}`；持 `repo.mu` 调 `diffSince`：出错 →
  `{Unavailable: true, Shared: shared}`；`nil` 且 `shared` → `{Files: …{}, Shared: true}`；`nil` → nil；
  否则补 `Shared = shared`、`TreeMoved = shellCommandMovesGitTree(command)` 返回。
- `release`：未离开过则从 `repo.inflight` 删除并把该 token 的值记入 `r.shared`；skipped 的 run 什么都不做。
- `inflight` 的登记 / 注销用注册表那把全局锁（`shellDiffRepos.Mutex`）保护，**不要**用 `repo.mu`——后者在拍快照期间一直被持有，
  用它会让第二条命令等到第一条的快照结束才登记，`shared` 就判不出来。

**退出码语义**（照搬 Claude Code）：`func shellExitIsFailure(command string, code int) bool`——取
`safety.SplitTopLevelWithSeparators(command)` 的最后一段，`strings.Fields` 后第一个词为 `first`：
`grep`、`rg`、`egrep`、`fgrep`、`find`、`diff`、`test`、`[` → `code >= 2`；`first == "git"` 且子命令（`git` 之后第一个不以 `-`
开头的词，遇 `-C` / `-c` 跳过它后面一个词）是 `diff` 或 `grep` → `code >= 2`；其它 → `code != 0`。

**git 移动工作树**（照搬 Claude Code，子命令列表 `checkout switch stash pull merge rebase reset restore clean cherry-pick revert`）：
- `shellCommandMovesGitTreeAlone(command)`：正则
  `` ^[ \t]*(?:sudo[ \t]+)?git[ \t]+(?:checkout|switch|stash|pull|merge|rebase|reset|restore|clean|cherry-pick|revert)(?:[ \t]+[A-Za-z0-9._/@~^][A-Za-z0-9._/@~^-]*){0,16}\s*$ ``。
- `shellCommandMovesGitTree(command)`：`SplitTopLevelWithSeparators` 的任一段 `safety.SplitShellWords` 后（去掉开头的 `sudo`）
  第一个词是 `git`、子命令（同上取法）在列表里 → true。

**Verify**：新建 `pkg/tool/shell_diff_test.go`。`exec.LookPath("git")` 失败时 `t.Skip`。辅助函数 `newShellDiffRepo(t)`：
`t.TempDir()` 里 `git init -q`、`git config user.email t@t`、`git config user.name t`、写 `a.txt`（`a\nb\n`）与
`.gitignore`（`ignored/\n`）、`git add -A`、`git commit -qm init`。每个测试结束前清掉注册表里本测试的仓库
（提供 `func forgetShellDiffRepo(top string)` 测试辅助：删私有目录并从注册表移除）。用例：
1. `TestShellEditDiffCapturesModifyCreateDelete`：`begin` → 用 Go 直接改 `a.txt`、新建 `new.py`、新建空 `empty.txt`、
   往 `ignored/x` 写文件 → `finish(…, 0, nil)`：`Files` 依次为 `empty.txt`(created, 无 diff) 和 `a.txt`/`new.py`（updated/created，
   有 diff，`+B` 行在 `a.txt` 的 diff 里），`ignored/x` 不出现，`ChangedFiles` 是绝对路径，`DisplayPath` 是相对路径。
2. `TestShellEditDiffLeavesTheUserRepositoryAlone`：跑完用例 1 的流程后，`git -C repo count-objects -v` 的 `count`/`in-pack`
   与开始前相同，`git -C repo status --porcelain` 与「不经过 begin/finish 直接做同样改动」时相同，`.git/index` 的字节未变。
3. `TestShellEditDiffNotAGitRepo`：普通临时目录 → `begin` 返回 nil。
4. `TestShellEditDiffReadOnlyAndFailedCommands`：`readOnly=true` → nil；`exitCode=1` 的普通命令 → nil；
   `grep x f` exit 1 → 仍计算；`git diff` exit 1 → 仍计算；`context` 已取消 → nil。
5. `TestShellEditDiffSkipsGitTreeMoves`：`git checkout main` → `{Skipped: true}`；`make && git pull` 改了文件 → `TreeMoved: true` 且有文件。
6. `TestShellEditDiffMoreFilesAndLimits`：把 `shellDiffMaxFiles` 临时改成 2、`shellDiffMaxHunkLines` 改成 5（`t.Cleanup` 还原），
   改 4 个文件、其中一个改 10 行 → 选中 2 个、`MoreFiles == 2`；再把一个文件写成二进制（含 `\x00`）→ 不在 `Files` 里、计入 `MoreFiles`。
7. `TestShellEditDiffLargeFilesAreUnavailable`：`shellDiffMaxHashBytes` 改成 10，写一个 20 字节文件 → `Unavailable: true`、`MoreFiles >= 1`。
8. `TestShellEditDiffShared`：同一仓库两个 run 交叠（A begin → B begin → A finish → B finish）→ 两者 `Shared == true`。
9. `TestShellEditDiffGivesUpAfterTwoFailures`：先正常 `begin`/`finish` 一次（仓库已登记），再把 `shellDiffGit` 改成不存在的可执行名
   （`t.Cleanup` 还原），对同一仓库直接调用两次 `snapshot` 都返回 error 且私有目录被删除；还原 `shellDiffGit` 后再 `begin` → 返回 nil（本进程已放弃）。
10. `TestShellExitIsFailure` 与 `TestShellCommandMovesGitTree`：表驱动覆盖上面两张表。

`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'ShellEditDiff|ShellExitIsFailure|ShellCommandMovesGitTree' -count=1 -race` → `ok`。

### Step 4：接进 shell 工具，并在卡片里显示

4.1 `pkg/tool/shell_tool.go`：
- `sandboxedShellRequest` 加字段 `editDiff *shellEditDiffRun`。
- `NewShellTool` 处理函数里，紧挨 `out, err := runSandboxedShellCommand(...)` 之前：

```go
			// The working-tree changes this command makes are shown under its output
			// (Claude Code's bashEditDiff): snapshot before, compare after. Always on.
			editDiff := beginShellEditDiff(ctx, cmd, wdAbs, readOnlyCommand)
			defer editDiff.release()
```
  并把 `editDiff: editDiff` 传进请求。（apply_patch 在 `:134` 已经提前返回，不会走到这里。）
- `runSandboxedShellCommand` 的**成功路径**（`:644` 那次 `captureCompletion()` 之前）：

```go
	if diff := req.editDiff.finish(ctx, req.command, exitCode, runErr); diff != nil {
		completionOutput["shell_edit_diff"] = *diff
	}
```
  只写 `completionOutput`；**不要**写 `resultPayload`（不给模型）。`:632` / `:641` 两条错误路径不加。

4.2 `diff_blocks.go` 加 `func shellEditDiffBlocks(output map[string]any) (DiffBlocks, bool)`：`DecodeShellEditDiff(output["shell_edit_diff"])`
不成功 → false。然后按 Claude Code 的显示逻辑生成：
- `Skipped` → 只有注 `diffNoteSkipped`；
- `len(Files) == 0` 且 `Unavailable` → 只有「不可用」注（用 `MoreFiles`）；
- `len(Files) == 0` 且 `Shared` → 只有 `diffNoteShared`；
- 否则：每个文件 `DiffBlock{Verb: created→Created / deleted→Deleted / 其它→Updated, Path: DisplayPath（空则 Path）, Added, Deleted, UnifiedDiff}`；
  注依次为：`MoreFiles > 0` 时「还有文件」注（带 `Unavailable` 后缀）；`TreeMoved` 时 `diffNoteTreeMoved`；`Shared` 时 `diffNoteShared`。
- 什么都没有 → false。
并把 Step 2.3 的 `shellDiffBlocks` 补成：apply_patch 走 `applyPatchDiffBlocks`，否则走 `shellEditDiffBlocks`。

**Verify**：
- `shell_tool_test.go` 新增 `TestShellToolShowsTheFilesACommandChanged`（不能 `t.Parallel`，用 `t.Chdir`）：在 git 仓库根里，
  `NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}})`，`ctx := WithToolCompletionCapture(context.Background())`，
  依次执行 `cat > note.txt <<'EOF'\nhello\nEOF` 与 `python3 -c "open('a.txt','w').write('a\\nB\\n')"`（没有 `python3` 时第二条改用 `printf 'a\nB\n' > a.txt`）：
  每次 `ToolCompletionFromContext(ctx)` 的 `Output["shell_edit_diff"]` 解出 1 个文件（`note.txt` created / `a.txt` updated）；
  **给模型的返回字符串里不含 `shell_edit_diff`**；`FormatToolStepResult` 的正文含 ``Created `note.txt` (+1 -0)`` 与 ```` ```diff ````，`output:` JSON 里不含 `shell_edit_diff`。
  再执行 `cat note.txt`（只读）→ 完成输出没有 `shell_edit_diff`。
- `format_test.go` 新增 `TestFormatToolStepResult_shellEditDiffNotes`：分别用 `{Skipped}`、`{Unavailable, MoreFiles:2}`、`{Shared}`、
  `{Files:[1个], MoreFiles:3, Unavailable, TreeMoved, Shared}` 构造输出（再各做一份 JSON 重放版），断言正文里的注逐字等于常量/上面的文案、
  各注在 diff 块之后、`ParseDiffBlocks` 读回同样的注。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... -count=1` → `ok`。

### Step 5：回合级 `turn_diff_updated` 覆盖 apply_patch 与 shell

`pkg/turn/events.go` `ExtractTurnDiffPayload`：
- 把现有「从一个 diff map 生成一个 `event.TurnDiffFile`」的代码原样抽成
  `func turnDiffFile(path, status string, added, deleted int, diffText string) event.TurnDiffFile`（保留 `event.Parse` → `rawTextHunks` 的回退）。
- 依次收集文件（`output` 来自 `normalizePayloadMap` 的 JSON 往返，`files` 是 `[]any`、元素是 `map[string]any`）：
  ① `output["turn_diff"]`（现有逻辑，状态缺省 `inferTurnDiffStatus`）；② `output["apply_patch"] == true` 时
  `output["files"][i]`：路径 `abs_path`，状态 `add→added`、`delete→deleted`、其它→`modified`，数字与文本取自 `files[i]["turn_diff"]`；
  ③ `tool.DecodeShellEditDiff(output["shell_edit_diff"])` 的 `Files`：路径 `Path`，状态 `created→added`、`deleted→deleted`、其它→`modified`。
- 一个文件都没有 → `false`（与现状相同）。
- `Summary`：只有 1 个文件时保持现有格式 `"<path> (+A/-D)"`；多于 1 个时 `fmt.Sprintf("%d files (+%d/-%d)", n, sumAdded, sumDeleted)`。

**Verify**：`pkg/turn/events_test.go` 新增 `TestExtractTurnDiffPayloadCoversApplyPatchFiles`（两个文件、状态 `added`/`modified`、
`Summary` 为 `2 files (+3/-1)` 形式、每个都有 hunk）与 `TestExtractTurnDiffPayloadCoversShellEditDiff`（`map` 形式的
`shell_edit_diff`，`Skipped` 或无文件时返回 `false`）；现有 3 个 `TestExtractTurnDiffPayload*` 不改、仍通过。
`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ -count=1` → `ok`。

### Step 6：TUI 的 shell 卡画 diff 块

6.1 `pkg/tui/render.go`：
- 把 `renderTurnDiffCard` 里「`event.Parse` → palette → `resolveDiffHighlight` → 逐 hunk `renderDiffLine` + `⋮`」那段抽成
  `func diffHunkRows(path, unifiedDiff string, theme DiffTheme) ([]string, bool)`（返回不带前缀的行），`renderTurnDiffCard`
  改为调用它——**输出逐字节不变**（`TestRenderTurnDiffCard_*`、`TestTurnDiffCard*` 原样通过即证明）。
  注意 `bandW` 仍是 `termWidthOrDefault() - 4`。
- 新增 `func renderShellDiffCard(content string, theme DiffTheme) (string, bool)`：
  `blocks, ok := tool.ParseDiffBlocks(content)`，`!ok` → false。行序列：
  1. `text := formatShellOutput(content)`（与普通 shell 卡同一份 stdout/stderr 文本）；非空时先放
     `strings.Split(formatToolOutputBlock(text), "\n")`。
  2. 每个文件：头行 `<Verb> <Path> ` + `lipgloss.NewStyle().Faint(true).Render("(+A -D)")`（`NO_COLOR` 下 lipgloss 自然退化）；
     有 diff 时接 `diffHunkRows(<Path 里 " → " 之后的部分>, UnifiedDiff, theme)`。
  3. 每条注：`lipgloss.NewStyle().Faint(true).Render(note)`。
  第 2、3 步的每一行加前缀 `"    "`；若第 1 步没有文本，则整张卡的第一行改用 `"  └ "`。返回 `strings.Join(rows, "\n"), true`。
- 在 `:2060` 的 `isDiffTool` 分支之后加：

```go
		if strings.EqualFold(title, "shell") && !toolStatusFailed(f) {
			if block, ok := renderShellDiffCard(f.Content, r.diffTheme); ok {
				_, _ = fmt.Fprintln(r.out, block)
				_, _ = fmt.Fprintln(r.out)
				return
			}
		}
```
6.2 `pkg/tui/reducer.go`：
- 新增 `func shellFrameHasDiffBlocks(f Frame) bool`：`f.Kind == FrameTool && strings.EqualFold(strings.TrimSpace(f.Title), "shell") && !toolStatusFailed(f) && tool.HasDiffBlocks(f.Content)`。
- 新增 `func toolFrameMaxLines(f Frame) int`：`shellFrameHasDiffBlocks(f)` → 30（与 edit_file / write_file 的 diff 卡相同），否则 `toolSpecificMaxLines(f.Title)`；
  `:2894` 改为 `maxLines := toolFrameMaxLines(b.frame)`（`FrameMemoryCompact` 的覆盖保留在其后）。
- `toolPreviewUsesRenderedBody` 的返回表达式末尾追加 `|| shellFrameHasDiffBlocks(f)`。

**Verify**：`pkg/tui/render_test.go` 新增：
1. `TestShellDiffCardShowsOutputThenFiles`：正文用 `tool.FormatToolStepResult` 从一个带 `stdout: "done\n"` 与两文件 `shell_edit_diff`
   （加 `MoreFiles: 1`、`Shared: true`）的完成事件生成 → `renderShellDiffCard` → `stripANSI` 后按行断言：第一行以 `"  └ "` 开头且含 `done`；
   之后依次出现 `Updated a.txt (+1 -1)`、diff 行（含 `- ` 与 `+ ` 标记行）、`Created new.py (+1 -0)`、`… 1 more file changed`、
   `(another command ran in this repository at the same time; …)`；所有后续行以 `"    "` 开头。
2. `TestShellDiffCardWithoutStdoutStartsWithTheFirstFile`：无 stdout 的 apply_patch 正文 → 第一行是 `"  └ Updated …"`。
3. `TestShellCardWithoutDiffBlocksIsUnchanged`：普通 shell 正文 → `renderShellDiffCard` 返回 false。
4. `TestShellDiffFrameFoldsLikeAnEditCard`：带 diff 块的 shell 帧 `toolFrameMaxLines == 30`、`toolPreviewUsesRenderedBody == true`；
   不带的 shell 帧分别为 `toolOutputMaxLines` 与 false。

`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1` → `ok`（含原有 diff 卡测试）。

### Step 7：全量回归

`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/turn/... ./pkg/tui/... ./pkg/gateway/... ./pkg/run/... ./pkg/architecture/... -count=1` → 全部 `ok`；
`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool/... ./pkg/turn/... ./pkg/tui/...` 与 `gofmt -l pkg/` 无输出；`cd frontend && pnpm vitest run` 通过。
已有测试若断言「shell 正文含 `input:` / 某个 JSON 键」而失败：只有 apply_patch 卡的 `input:` 与 `files`、以及所有 shell 卡的
`shell_edit_diff` 是本计划有意去掉的——仅为这三处改断言；其它失败按 STOP 2 处理。

### Step 8：真机验收（fake provider 驱动，无需真实模型）

读 `.claude/skills/run-forebrain/SKILL.md` 的「Run (agent path)」一节。`D=.claude/skills/run-forebrain/driver.sh`。

1. `$D reset && $D build`（必须重新编译）。
2. fake provider 的 `shell` 模式按**请求序号**发脚本里的调用，后台请求也会占序号，所以**每次启动只脚本一个调用**，共三次。
   三个调用（各是一个 JSON 对象，作为 `$D start shell '<json>'` 的第二个参数，注意 shell 引号）：
   - A：`{"command": "cat > note.txt <<'EOF'\nhello\nworld\nEOF"}`
   - B：`{"command": "python3 -c \"open('README.md','a').write('appended line\\n')\""}`
   - C：`{"command": "apply_patch <<'PATCH'\n*** Begin Patch\n*** Update File: note.txt\n@@\n hello\n-world\n+there\n*** End Patch\nPATCH"}`
3. `$D start shell '<A>'`；TUI 起来后（若出现信任 / 项目服务面板，按 SKILL.md 的说明确认），**提交前**把项目变成 git 仓库：
   `P="${TMPDIR:-/tmp}/forebrain-run/proj"; git -C "$P" init -q && git -C "$P" add -A && git -C "$P" -c user.email=a@b -c user.name=a commit -qm init`。
4. `$D submit 'go'`；出现审批框就批准；`$D wait 'note.txt' 30` 后 `$D screen > /tmp/fb130-A.txt`。
   然后对 B、C 各做一次：`$D start shell '<B或C>'` → `$D submit 'go'` → 等待 → `$D screen > /tmp/fb130-<B|C>.txt`
   （`start` 不清项目目录与库，git 仓库与之前的行都还在；不要在中间 `reset`）。
5. 期望（屏幕）：`fb130-A.txt` 的 shell 卡有 `Created note.txt (+2 -0)` 与两行 `+` diff；`fb130-B.txt` 有 `Updated README.md (+1 -0)`；
   `fb130-C.txt`（apply_patch）在 `Applied patch to 1 file(s).` 之后是 `Updated note.txt (+1 -1)` 与 `-`/`+` 两行。
   （每次 `start` 都会把 `README.md` 重写成同一行原文，B 的快照在这之后才拍，所以 B 只显示追加的那一行。）
6. 期望（web 用的正文，直接查库）：
   `$D db "SELECT json_extract(payload_json,'$.display_body') FROM fb_session_events WHERE event_type='tool_call_completed' ORDER BY sequence"`
   三条正文都含 ``Created `note.txt` `` / ``Updated `README.md` `` / ``Updated `note.txt` `` 与 ```` ```diff ````；任何一条**正文**都不含 `unified_diff`；
   apply_patch 那条正文不含 `input:` 与 `"files"`。（只查 `display_body`——同一行 payload 的 `output` / `data` 里本来就带结构化数据，那是给重放用的，不在此列。）
7. `$D db "SELECT count(*) FROM fb_session_events WHERE event_type='turn_diff_updated'"` ≥ 3。
8. `git -C "$P" status --porcelain` 只列出 `note.txt`、`README.md` 两个用户改动（没有任何 forebrain 私有文件）。
9. `$D stop`。把第 5–8 条的实际输出贴进执行报告。

## Test plan

见各步 Verify：`diff_blocks_test.go`（写/读 round-trip、围栏自适应、限额、围栏内不误判、注文案）、`shell_diff_test.go`（10 组：
改/建/删/空文件/忽略、用户仓库不被写、非 git、只读与失败、git 移动工作树、限额与二进制、大文件、并发 shared、放弃、两张语义表）、
`shell_tool_test.go`（端到端：完成输出有、模型结果没有、只读命令没有）、`format_test.go`（apply_patch 正文、注）、
`request_permissions_test.go`（`display_path`）、`events_test.go`（两类载荷）、`render_test.go`（TUI 卡与折叠）。

## Done criteria

- [ ] Step 7 的全部命令通过（含 `pkg/architecture`）
- [ ] `grep -rn "shell_edit_diff" pkg/tool/shell_tool.go` 只出现在 `completionOutput` 那一处，`resultPayload` 附近没有
- [ ] `grep -rn "bashEditDiffEnabled\|SHELL_EDIT_DIFF\|shell_edit_diff_enabled" pkg/ frontend/src` 无结果（没有开关）
- [ ] `grep -rn "a convenience view" pkg/` 无结果（没有长说明句）
- [ ] `git status --short frontend ai-elements-vue pkg/hook pkg/config` 无输出
- [ ] Step 8 的期望 5–8 全部满足，输出贴在报告里

## STOP conditions

1. 前置 grep 不满足，或 Current state 的锚点 / 摘录与现网不符。
2. Step 7 出现与本计划三处有意变更无关的测试失败；或某步验证连续两次修正后仍失败。
3. `pkg/architecture` 报 `pkg/tool` 扇出增长（说明引入了新的模块内导入）——不要去改预算，回报。
4. 发现私有快照会改动用户仓库（Step 3 用例 2 失败，或 Step 8 第 8 条看到多余文件）——立即停，不要放宽断言。
5. 需要改 `frontend/**` 才能让 web 显示 diff 块。
6. `renderTurnDiffCard` 抽函数后原有 diff 卡测试的输出发生任何变化。

## Maintenance notes

- 快照只在 shell 工具里跑，不是常驻服务：每个非只读 shell 命令多 2 次 `status` + `write-tree` 与少量 `cat-file`；小仓库约几十毫秒。
  若以后观察到大仓库超时频繁，先看 `slog` 的 `shell diff: snapshot failed`，再考虑 Claude Code 在 Linux 上的 inotify 快路径（本计划未做）。
- `124`（工具边流边执行）落地后 shell 可能并发更多：`shared` 注就是为这种情况准备的，不要把它当 bug 去掉。
- 131 会让 PostToolUse hook 的 `tool_response` 带上 `shell_edit_diff`（含 `changed_files`）；改这里的数据形状时同步 131 的测试。
- diff 块的文本协议（头行正则、注的前缀）由 `diff_blocks.go` 一处写、一处读；新增注文案时两边同改，并给 round-trip 测试加例。
- 评审重点：`resultPayload` 不得出现 `shell_edit_diff`；私有 GIT_DIR 的环境变量清洗与 `info/attributes`（安全）；
  失败后私有目录被删除、`failures` 计数正确；TUI 的普通 shell 卡（无 diff 块）渲染逐字节不变。
