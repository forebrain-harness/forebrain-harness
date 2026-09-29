# 任务 11：编辑后诊断、迟到提醒、读观察者、shell 变更扫描与诊断行

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §2（「等待窗口」「迟到诊断」）、§3.6、§3.10、§8.3（全部）、§8.4、§11.2、附录 B.3、B.4。
>
> **前置**：任务 08 已合入（`Manager.DidWrite`、`DidRead`、`DidRunShell`、`PeekLate`、`AckLate` 是真实实现）。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/tool/file_tools.go pkg/tool/shell_tool.go pkg/tool/state.go pkg/tool/format.go pkg/run/config.go pkg/run/runner.go pkg/tui/render.go`。任务 10 会改 `file_tools.go`（`authorizeRead`）、`format.go`、`render.go`；对比「现状」摘录。

## 状态

- 优先级：P1 · 工作量：L · 风险：MED（**模型可见变化**：编辑工具的结果可能带诊断；可能多一条提醒消息）
- 依赖：08
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

这是 LSP 最有价值的能力：agent 改完代码立刻知道自己引入了什么错误。本任务把任务 08 的 `DidWrite` 接到三个编辑工具上，把迟到诊断以提醒的形式送回模型，把读文件与 shell 命令的信息喂给 LSP 运行时，并在两个界面显示「Found N new diagnostic issues in M files」。

## 现状

- `pkg/tool/file_tools.go`
  - `:399 NewFileWriteTool(st, rt)`：写盘 `atomicWrite(abs, []byte(in.Content), 0o644)` 之后 `st.RememberWrite(...)`，构建 `output := map[string]any{"status": "ok", "abs_path": abs}`，`attachTurnDiff(output, abs, before, []byte(in.Content))`，`CaptureToolOutput(ctx, output)`，返回 `"ok"`（或 `note + "\n\nok"`）。`before` 在文件原本存在时为旧内容，否则为 nil。
  - `:536 FileEditOptions{PrepareLocked func(...)}`；`:540 NewFileEditTool(st)` 返回 `NewFileEditToolWithOptions(st, FileEditOptions{})`；编辑在 `atomicWrite(abs, []byte(out), 0o644)` 之后同样构建 `output`、`attachTurnDiff(output, abs, raw, []byte(out))`、`CaptureToolOutput`、返回 `"ok"`。编辑工具**拿不到** `rt`。
  - `:1018 applyPatchOperations(ctx, st, rt, patch, ops)`：对每个文件的 `plan`（`Before`、`After`、`Delete`、`MoveAbs`、`MoveBefore`）写盘；最后构建 `stdout := fmt.Sprintf("Applied patch to %d file(s).\n", len(files))`、`payload`（含 `"stdout"`）并 `CaptureToolCompletion(...)`，返回 `json.Marshal(payload)`。
  - `:117` `read_file` 读到内容后调用 `if observer := st.ReadObserver(); observer != nil { observer(ctx, abs, raw) }`。
- `pkg/tool/registry.go:85` `RegisterDefaultTools` 中 `t3, err := NewFileEditTool(st)`。
- `pkg/tool/state.go:117` 字段 `readObserver func(...)`；`:1833 SetReadObserver`、`:1842 ReadObserver`；生产代码没有调用 `SetReadObserver`（只有 `file_tools_test.go:179`）。
- `pkg/tool/shell_tool.go:233`：`shell` 工具最后 `return runSandboxedShellCommand(ctx, st, rt, sandboxedShellRequest{...})`。
- `pkg/tool/format.go:1046 formatGenericToolStep`：先写 `turnDiffSection(evt.Output)`，最后把 `compactExplainableOutput(evt.Output)` 以 JSON 输出；`:2580 turnDiffSection`、`:2598 outputWithoutTurnDiff`。
- `pkg/tui/render.go:6662 renderTurnDiffCard(content, theme, maxRows)`：解析 `turn diff:` 正文，输出 `"  └ " + turnDiffSummaryLine(...)` 与 diff 行。
- `pkg/run/config.go`：`:996 reminderAdoptionSink`、`:1076 recordReminderAdoption(ctx, message, insertAt)`、`:1083 planReminderMessage(reminder) llm.Message`。编排循环的约定（`pkg/run/orchestration_llm.go:194-206` 的注释）：调用失败时 sink 里的提醒不被收进会话，下次调用会再注入。
- `pkg/run/runner.go:982` `llmClient = wrapSkillOfferLLM(...)`；`summaryChain := llmClient` 在它之前（:969），所以挂在它之后的 wrapper 不会进入压缩请求。
- 结构约束：`tool`、`run`、`tui` 都已满 20 个生产文件，只能改已有文件。

## 范围

**修改：** `pkg/tool/file_tools.go`、`file_tools_test.go`、`registry.go`、`state.go`、`state_test.go`、`shell_tool.go`、`shell_tool_test.go`、`format.go`、`format_test.go`；`pkg/run/config.go`、`config_test.go`、`runner.go`；`pkg/tui/render.go`、`render_test.go`；`graph.json`。

**不要碰：** `pkg/lsp`、`frontend`（Web 端正文来自同一个 `FormatToolStepResult`，无需改动）、系统提示。

## 步骤

### 步骤 1：写工具调用 `DidWrite`

在 `file_tools.go` 加一个共用函数：

```go
// reportEditDiagnostics asks the language-server runtime what an edit
// introduced (spec §8.3) and returns the text to append to the tool result.
// It records the summary on output for the surfaces. A nil runtime, or an
// edit nothing covers, returns "".
func reportEditDiagnostics(ctx context.Context, ci CodeIntelligence, output map[string]any, changes []FileChange) string {
	if ci == nil || len(changes) == 0 {
		return ""
	}
	delta := ci.DidWrite(ctx, llm.AgentSessionIDFromContext(ctx), changes)
	if delta.Empty() {
		return ""
	}
	if output != nil {
		output["lsp_diagnostics"] = delta.Summary
	}
	return delta.Text
}
```

- **write_file**：在 `attachTurnDiff` 之后、`CaptureToolOutput` 之前：`diag := reportEditDiagnostics(ctx, codeIntelOf(rt), output, []FileChange{{AbsPath: abs, Before: before, After: []byte(in.Content)}})`；返回值 `result := "ok"`（有 note 时 `note + "\n\nok"`），`diag != ""` 时 `result += "\n\n" + diag`。`codeIntelOf(rt)` 为 `rt != nil ? rt.CodeIntel : nil` 的小函数。
- **edit_file**：`FileEditOptions` 加字段 `CodeIntel CodeIntelligence`；编辑成功路径同上（`Before: raw`，`After: []byte(out)`）。`registry.go` 中把 `NewFileEditTool(st)` 改为 `NewFileEditToolWithOptions(st, FileEditOptions{CodeIntel: codeIntelOf(rt)})`。
- **apply_patch**：在写盘循环里为每个 plan 收集 `FileChange`：普通修改 `{writePath, plan.Before, plan.After}`；删除 `{abs, plan.Before, nil}`；移动 `{abs, plan.Before, nil}` 与 `{plan.MoveAbs, plan.MoveBefore, plan.After}`。循环结束后 `diag := reportEditDiagnostics(ctx, codeIntelOf(rt), nil, changes)`；`diag != ""` 时 `stdout += "\n" + diag + "\n"`，并在 `payload` 与 `CaptureToolCompletion` 的 `Output` 两处都加 `"lsp_diagnostics": summary`（为此 `reportEditDiagnostics` 的 output 传一个临时 map，再把其中的键复制到两处）。
- 所有调用都在**写盘成功之后**；诊断收集不影响审批、沙箱与写入结果（规范 §8.3.5）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`。

### 步骤 2：读观察者改为可命名多路（`state.go`）

- 字段改为 `readObservers map[string]func(ctx context.Context, absPath string, content []byte)`。
- `SetReadObserver(h)` 保留，等价于 `SetNamedReadObserver("", h)`（保持现有测试语义）。
- 新增 `func (s *State) SetNamedReadObserver(name string, h func(ctx context.Context, absPath string, content []byte))`：`h == nil` 删除该名字。
- `ReadObserver()` 返回一个按名字字典序依次调用所有观察者的函数；一个都没有时返回 nil（`file_tools.go:117` 的调用点不改）。

### 步骤 3：接入读观察者与 shell 扫描

- `pkg/run/runner.go` 的 `loadLocked`：在构建 `rt` 之后加：

  ```go
  	// Reads feed the language servers that are already running (a baseline
  	// for the next edit's diagnostics); they never start one.
  	if ci := r.CodeIntel; ci != nil {
  		r.tools.SetNamedReadObserver("lsp", func(ctx context.Context, abs string, content []byte) {
  			ci.DidRead(ctx, abs, content)
  		})
  	} else {
  		r.tools.SetNamedReadObserver("lsp", nil)
  	}
  ```

- `pkg/tool/shell_tool.go:233`：改为先接住结果，再 `if ci := codeIntelOf(rt); ci != nil { ci.DidRunShell(ctx) }`，然后返回。`DidRunShell` 立即返回，不影响 shell 的结果与耗时。

### 步骤 4：迟到提醒 wrapper（`pkg/run/config.go`）

在 `planReminderMessage` 附近加：

```go
// lspDiagnosticsReminderLLM appends the language-server diagnostics that
// arrived after an edit's wait window (spec §8.4). The text is appended at
// the end of the request, so the cached prefix is untouched; it is delivered
// once, acknowledged only after the model call succeeds.
type lspDiagnosticsReminderLLM struct {
	inner llm.LLM
	ci    tool.CodeIntelligence
}

func wrapLSPDiagnosticsReminderLLM(inner llm.LLM, ci tool.CodeIntelligence) llm.LLM {
	if inner == nil || ci == nil {
		return inner
	}
	return lspDiagnosticsReminderLLM{inner: inner, ci: ci}
}

func (w lspDiagnosticsReminderLLM) Execute(ctx context.Context, msgs []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sid == "" {
		return w.inner.Execute(ctx, msgs, tools)
	}
	text, token := w.ci.PeekLate(sid)
	if text == "" {
		return w.inner.Execute(ctx, msgs, tools)
	}
	message := planReminderMessage(text)
	recordReminderAdoption(ctx, message, len(msgs))
	res, err := w.inner.Execute(ctx, append(append([]llm.Message(nil), msgs...), message), tools)
	if err == nil {
		w.ci.AckLate(sid, token)
	}
	return res, err
}
```

`runner.go` 中紧跟 `llmClient = wrapSkillOfferLLM(...)`（:982）之后加一行：`llmClient = wrapLSPDiagnosticsReminderLLM(llmClient, r.CodeIntel)`，并在上面补一行注释说明它在 `summaryChain` 之外的原因（与 plan-mode / skill-offer 提醒相同）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`。

### 步骤 5：卡片显示

- `pkg/tool/format.go`：
  - 新增 `lspDiagnosticsSection(output map[string]any) string`：读取 `output["lsp_diagnostics"]`（可能是 `event.LSPDiagnosticsSummary`，也可能是回放后的 `map[string]any`——统一用 `json.Marshal` + `json.Unmarshal` 转成 `event.LSPDiagnosticsSummary`）；`New == 0 && len(PendingFiles) == 0` 返回空。否则输出：

    ```
    lsp diagnostics: {New} new in {Files} file{s}

    ```text
    {path}
      {severity} {line}:{column} {message} [{source} {code}]
    diagnostics for {path} are still being computed and will follow
    ```
    ```

    （问题行格式与附录 B.3 相同，只是来自 `Items`；只有 pending 时首行为 `lsp diagnostics: pending`。）
  - `formatGenericToolStep`：在 `turnDiffSection` 段之后追加该段；并且在最后输出 JSON 之前从输出副本中删除 `lsp_diagnostics` 键（照 `outputWithoutTurnDiff` 的写法新增 `outputWithout(output, keys...)` 或扩展现有函数）。
  - `shell` 类步骤（apply_patch 走 shell 的格式化）：`formatShellStep` 若 `evt.Output["lsp_diagnostics"]` 存在，在正文末尾同样追加该段。
- `pkg/tui/render.go`：在 `renderTurnDiffCard` 输出 diff 行之后，若 `content` 中存在以 `lsp diagnostics:` 开头的行，解析其后 ```` ```text ```` 块，追加一行 `"  └ " + lspDiagnosticsSummaryLine(new, files)` 和块中每一行（前缀四个空格）。`lspDiagnosticsSummaryLine`：`Found {N} new diagnostic issue{s} in {M} file{s}`（单复数：1 时不加 s）；只有 pending 时为 `Diagnostics pending`。长卡片由现有的 `foldBlock` 折叠，不需要新的折叠逻辑。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1` → `ok`。

### 步骤 6：写测试

见「测试计划」。

### 步骤 7：全量检查

**验证**：`gofmt`、`go vet ./...` 干净；满额包文件数仍为 20；`scripts/package-graph.sh` 后提交 `graph.json`；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

用一个记录调用、可编排返回值的 `CodeIntelligence` 测试桩（写在各包对应的 `_test.go` 里）：

- `pkg/tool/file_tools_test.go`
  - `TestWriteFileReportsDiagnostics`：桩返回 `DiagnosticsDelta{Text: "<diagnostics>\nX\n</diagnostics>", Summary: {New: 1, Files: 1}}` → 返回值为 `"ok\n\n<diagnostics>…"`；`CaptureToolOutput` 的 map 含 `lsp_diagnostics`；桩收到 `Before == nil`（新文件）或旧内容（覆盖）以及 agent 会话 id（用 `llm.WithAgentSessionID` 设置的值）。
  - `TestWriteFileWithoutCodeIntelUnchanged`：`rt.CodeIntel == nil` → 返回 `"ok"`，output 无该键。
  - `TestEditFileReportsDiagnostics`：通过 `NewFileEditToolWithOptions(st, FileEditOptions{CodeIntel: stub})`；`Before` 为编辑前内容、`After` 为编辑后内容。
  - `TestEditFileEmptyDeltaAddsNothing`。
  - `TestApplyPatchReportsDiagnosticsOnce`：一个补丁改两个文件、删一个、移动一个 → 桩只被调用一次，`changes` 数量与内容正确；`stdout` 末尾含诊断文本。
- `pkg/tool/state_test.go`：`TestNamedReadObservers`（两个命名观察者都被调用、按名字顺序；`nil` 删除；旧的 `SetReadObserver` 仍有效）。
- `pkg/tool/shell_tool_test.go`：`TestShellNotifiesCodeIntel`（命令执行后桩的 `DidRunShell` 被调用一次；`rt.CodeIntel == nil` 时不 panic）。
- `pkg/run/config_test.go`
  - `TestLSPReminderAppendedAndAcked`：桩 `PeekLate` 返回文本与 token 7；内层 LLM 记录收到的消息 → 最后一条是 IsMeta 用户消息，内容为 `<system-reminder>\n<文本>\n</system-reminder>`；`AckLate(sid, 7)` 被调用一次。
  - `TestLSPReminderNotAckedOnError`：内层返回错误 → 不调用 `AckLate`。
  - `TestLSPReminderNoTextNoMessage`：`PeekLate` 返回空 → 消息列表原样。
  - `TestLSPReminderNeedsSession`：ctx 没有 agent 会话 id → 不调用 `PeekLate`。
- `pkg/tool/format_test.go`
  - `TestLSPDiagnosticsSection`：`event.LSPDiagnosticsSummary` 与等价 `map[string]any` 两种输入输出相同；只有 pending 的形态；JSON 转储里不再出现 `lsp_diagnostics`。
- `pkg/tui/render_test.go`
  - `TestTurnDiffCardShowsDiagnostics`：含诊断段的正文 → 出现 `└ Found 2 new diagnostic issues in 1 file` 与问题行；单数形态 `1 new diagnostic issue in 1 file`。

## 完成判据

- [ ] 上述测试全部通过
- [ ] `grep -n "wrapLSPDiagnosticsReminderLLM" pkg/run/runner.go` 恰好一行，且位于 `wrapSkillOfferLLM` 之后
- [ ] `grep -n "SetNamedReadObserver(\"lsp\"" pkg/run/runner.go` 有输出
- [ ] 满额包文件数仍为 20；`graph.json` 已提交；全量测试通过
- [ ] README 中任务 11 状态为 `DONE`

## STOP 条件

- 需要把 `DidWrite` 放到写盘之前或让它的结果影响写入是否成功。
- 需要修改系统提示或 `summaryChain` 之内的 wrapper。
- 编排循环对 `recordReminderAdoption` 的处理与「现状」描述不符（调用失败时提醒被重复收进会话）。

## 维护说明

- 新增编辑类工具时必须调用 `reportEditDiagnostics`，否则该工具的编辑拿不到诊断，迟到队列也不会登记它编辑的文件。
- 迟到提醒的 Ack 语义与编排循环「失败则下次再注入」的约定绑定；改动编排循环时回看本 wrapper。
- 评审重点：`agentSessionID` 在工具（`llm.AgentSessionIDFromContext`）与 wrapper 两处取值一致；apply_patch 的移动/删除变更是否完整。
