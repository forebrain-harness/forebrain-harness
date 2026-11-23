# Plan 135：引擎与网关——给 web 消息卡的结构化数据（卡头分段、shell 卡事实、拒绝说明、完整输出接口）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发 "STOP conditions" 时停下报告，
> 不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **提交**：按 owner 当次派发指令（若建 worktree，必须基于 dev 分支 HEAD，并把主树 `third_party/pdfium`、
> `third_party/libtokenizers` 符号链接进 worktree 才能编译）；没有指令时**不 commit、不 push**，改动留在工作区。
> 工作区里与本计划无关的未提交改动：不要碰、不要回滚、不要格式化。
>
> **前置（否则 STOP 1）**：计划 `130`（shell / apply_patch diff 块）与 `132`（卡片显示原始输出）已经落地：
> `grep -n "func ParseDiffBlocks" pkg/tool/diff_blocks.go`、`grep -n "shell_edit_diff" pkg/tool/shell_tool.go`、
> `grep -n "rawStdout" pkg/tool/shell_tool.go` 各有结果。
>
> **Drift check**：本文所有摘录按**符号**定位（行号仅供参考，写于 dev `1c8f5d9`）；符号找不到或内容与摘录不符 → STOP 1。

## Status

- **Priority**: P1（web 消息卡重做 136–138 的数据基础）
- **Effort**: M–L
- **Risk**: MED（网关多发几个字段，TUI 文案不变；`call_headers` 的形状要换）
- **Depends on**: 130、132
- **Blocks**: 136、137、138
- **Category**: feature（owner 2026-10-25：web 消息卡按原型重做）
- **Planned at**: dev `1c8f5d9`（原型在工作区的 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`），2026-10-25

## Why this matters

owner 2026-10-25 定稿了 web 对话页消息卡的新设计（原型 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`，
入口「对话 · 全部消息卡」「对话 · shell 命令卡（定稿）」「对话 · 审批卡（全部工具）」「对话 · 计划审批 ①–⑥」）。
web 不再把引擎拼好的一句英文卡头、Markdown 正文（`**shell**`、`input:` JSON、`stdout:`/`stderr:` 围栏、`output:` JSON）
原样铺出来，而是用结构化数据画卡。这需要网关提供下面四样东西，本计划只做引擎与网关，web 在 136–138：

1. **卡头分段**。owner：「当用户切换到中文时，消息卡头的所有英文文案……必须显示成中文」。引擎的卡头由
   `tool.DisplayParts` 产出 `action / target / suffix` 三段并把时长拼进 suffix；web 需要**分开的四段**
   （动作、对象、后缀、时长：时长在卡头最右单独一列），动作词按界面语言取词（中文表在 web 侧），对象不翻译。
   英文界面仍是引擎原话；TUI 不变。
2. **shell 卡事实**。原型的 shell 卡正文 = 输出（stdout，stderr 标红）+ 被截断时“中间 N KB 已省略”+ 改动的文件
   （计划 130 的 diff 块）+ 短注；卡头带非 0 退出码。这些现在只以 Markdown 形式存在正文里，**历史行只存了正文**
   （`ToolMeta` 里没有 stdout/退出码/文件），所以网关必须能从正文还原出结构，直播与重载走同一条路才能一致。
3. **拒绝时用户写的说明**。原型 ⑬：拒绝时写了说明，卡收成“已拒绝 …”，下面显示用户的话。引擎有
   `StepEvent.DenyFeedback`，但普通工具的展示正文是空串（`DeniedToolDisplayBody`），`ToolMeta` 也没有它，web 拿不到。
4. **完整输出**。输出被截断时卡片底栏的眼睛图标在右侧抽屉打开“输出 · 命令”文档页，显示完整输出。完整输出已经被
   引擎落盘（spool，≤64 MB，`retrieve_id`），但网关没有读它的接口。

另外修一个存量缺陷（本次调研发现）：网关的 `call_headers` 是**以调用 id 为键的 map**，而 web 的 axios 拦截器对所有
响应做**深度 camelCase**（`frontend/src/lib/case.ts` `toCamelCase`，`camelcase-keys` 的 `deep: true`），
`call_abc` 这样的键会被改成 `callAbc`，`row.callHeaders?.[call.id]` 永远取不到。新字段一律用数组。

## Current state

### 卡头

`pkg/tool/display_header.go`：

```go
func DisplayHeader(f DisplayFrame, cwd string) string {
	action, target, suffix := DisplayParts(f, cwd)
	// 三段用空格拼成一句
}

// DisplayParts maps a card's tool title and summary to three user-facing
// parts: action (past/progressive verb), target (the thing acted on), and
// suffix (extra metadata like duration or exit status).
func DisplayParts(f DisplayFrame, cwd string) (action, target, suffix string) {
	// …一个大 switch：read_file / edit_file / write_file / request_permissions / shell / web_fetch /
	// web_search / lsp / retrieve_output / session_todo / enter_plan_mode / exit_plan_mode /
	// memories_add_ad_hoc_note / memories_list / memories_read / memories_search / intermediate_tool /
	// user_interaction / mcp__* / default（action = title）
	if isCanceled && !toolStatesOwnCanceledLabel(lower) { action = "Canceled"; suffix = "" }
	if isDenied && !toolStatesOwnDeniedLabel(lower) { action = "Denied"; suffix = "" }

	// Append the executor-supplied duration for terminal states that represent an
	// actual execution. Pending, denied, and canceled cards intentionally omit it.
	if !isRunning && !isPending && !isDenied && !isCanceled && (isFailed || isCompleted) {
		if ds := FormatDuration(f.Duration); ds != "" {
			if suffix == "" { suffix = ds } else { suffix = suffix + " · " + ds }
		}
	}
	// A tool that is still executing shows how long it has been running; …
	if isRunning && f.Duration >= time.Second && !hdrStatus(f, "awaiting approval") {
		if suffix == "" { suffix = FormatWorkingElapsed(f.Duration) } else { suffix = suffix + " · " + FormatWorkingElapsed(f.Duration) }
	}
	return strings.TrimSpace(action), strings.TrimSpace(target), strings.TrimSpace(suffix)
}
```

`FailedActionPhrase(toolName)`（同文件）给出 `read / edit / write / run / fetch / search / look up / enter plan mode /
exit plan mode / request permissions / update todo / ask user / save notes / list memories / read memory /
search memories`，未知工具返回工具名本身；`FormatDuration`：`<0.1s` / `1.2s` / `12s` / `1m23s`。
已有测试：`pkg/tool/display_header_test.go` `TestDisplayHeaderIsTheTerminalsWording`。

网关（`pkg/gateway/wsevents.go`）把卡头作为**一句话**发出，共四处：

```go
// toolHeaderFor is one card's header: the tool, what it is doing or did, and
// how long it took when that is known.
func toolHeaderFor(root, toolName, summary, body string, meta tool.ToolMeta, final bool, took time.Duration) string {
	… return tool.DisplayHeader(tool.DisplayFrame{Title: title, Summary: summary, Meta: meta, Final: final, Content: body, Duration: took}, root)
}
// withStepHeader adds the card's header to a live step's data.
func withStepHeader(data map[string]any, header string) map[string]any { … data["header"] = h … }
func storedToolRowHeader(root, partsJSON, toolMetaJSON string, took time.Duration) string { … }
func canceledCallHeaders(root func() string, partsJSON string) map[string]string { … out[call.ID] = toolHeaderFor(…) … }
func toolEventPayloadWithHeader(root, eventType string, payload json.RawMessage) json.RawMessage { … fields["header"] = header … }
```

调用点：`pkg/gateway/server.go` 直播 step（`Op: "step"` 的两处 `withStepHeader(withStepSummary(gatewayToolStartedStepData(…)…), toolHeaderFor(…))`
与 `gatewayToolCompletedStepData(…)`，后者有 `rendered.DisplayBody`）；`server.go` `evt.Payload = toolEventPayloadWithHeader(…)`
（子代理事件）；`pkg/gateway/api_extra.go` `handleChatMessages` 里的历史行：

```go
		// Header is a tool row's card header and CallHeaders the header each
		// call an assistant row issued would carry if it never ran, both worded
		// by the engine exactly as the terminal words them (tool.DisplayHeader).
		Header      string            `json:"header,omitempty"`
		CallHeaders map[string]string `json:"call_headers,omitempty"`
…
			switch strings.ToLower(strings.TrimSpace(t.Role)) {
			case "tool":
				m.Header = storedToolRowHeader(displayRoot(), t.PartsJSON, t.ToolMetaJSON, time.Duration(t.ExecDurationMs)*time.Millisecond)
			case "assistant":
				m.CallHeaders = canceledCallHeaders(displayRoot, t.PartsJSON)
			}
```

`api_extra.go` 里事件分页（`page.Events[i].Payload = toolEventPayloadWithHeader(root(), t, page.Events[i].Payload)`）也走同一个函数。
网关测试：`pkg/gateway/wsevents_test.go` `TestToolHeadersSayWhatTheTerminalSays`。

### shell 正文

`pkg/tool/format.go` `formatShellStep(evt StepEvent, maxBytes int) (string, bool)` 依次写：`**shell**`；（非 apply_patch 时）
`input:` + ```` ```json ```` 块；`stdout:` + ```` ```<text|diff> ```` 块（`trimBlankEdgeLines` 之后）；`stderr:` + ```` ```text ```` 块；
`omittedBytesNote(evt.Output)`（`preview truncated: stdout omitted N bytes; stderr omitted M bytes` 或 `output omitted N bytes`，
可带 `` (full output: `<path>`) ``）；计划 130 的 diff 块（`diffBlocksMarkdown`）；`output:` + ```` ```json ```` 块
（`shellOutputMetadataOnly`：去掉 stdout/stderr/turn_diff/lsp_diagnostics/shell_edit_diff 之后的完成输出，含 `exit_code`、
`stdout_omitted_bytes`、`stderr_omitted_bytes`、`retrieve_id`、`spool_bytes`、`spool_omitted_bytes`、沙箱字段等）；
LSP 诊断段。最后 `clampBody(sb.String(), maxBytes)`。**围栏固定是三个反引号**，stdout 里若有一行 ```` ``` ````，Markdown 会提前闭合。
计划 130 在 `pkg/tool/diff_blocks.go` 提供 `type DiffBlock struct{ Verb, Path string; Added, Deleted int; UnifiedDiff string }`、
`type DiffBlocks struct{ Files []DiffBlock; Notes []string }`、`func ParseDiffBlocks(body string) (DiffBlocks, bool)`（只在围栏外认头行）。

shell 完成输出（`pkg/tool/shell_tool.go` `runSandboxedShellCommand` 的 `completionOutput`）键：`stdout`、`stderr`、`exit_code`、
`stdout_bytes`、`stderr_bytes`、`stdout_omitted_bytes`、`stderr_omitted_bytes`、`sandbox_*`；有落盘时 `retrieve_id`、`spool_bytes`、
`spool_omitted_bytes`；130 之后可能有 `shell_edit_diff`；apply_patch 时有 `apply_patch: true` 与 `files`。

### 拒绝说明

`pkg/tool/format.go`：`DeniedToolStep(stepID, toolName, input, feedback)` 产出 `StepEvent{…, Denied: true, DenyFeedback: feedback}`；
`DeniedToolDisplayBody(toolName, feedback)` 只对 `exit_plan_mode` 返回说明、对 `request_permissions` 返回固定句，其余返回 `""`。
`func BuildToolMeta(evt StepEvent) ToolMeta` 从 `StepEvent` 生成要落库的 `ToolMeta`（`pkg/tool/notification.go` `type ToolMeta struct`，
没有说明字段）。

### 完整输出

`pkg/tool/shell_output.go` `registerShellSpool`：`store := CompressorFor(rt.StateRoot()).Store()`，`store.Save(Entry{Kind: KindShell,
Command, SessionID: llm.AgentSessionIDFromContext(ctx), SpoolPath, SpoolOmittedBytes, …})` 返回 `retrieve_id`。
`pkg/tool/loaded_skills.go` `func (s *Store) Get(id int64) (Entry, error)`：`Entry.OriginalOutput` 是 spool 文件全文
（文件已过期时是预览加一句 `[full spooled output is no longer available: …]`），`Entry.SessionID`、`Entry.SpoolOmittedBytes`。
网关已有用法：`pkg/gateway/slash_handlers.go` `tool.CompressorFor(s.Runner.StateRoot()).Store()`；会话所属 runner：
`pkg/gateway/server.go` `func (s *Server) runnerFor(ctx context.Context, sessionID string) *run.Runner`。
路由表在 `pkg/gateway/api_extra.go`（`chatSessions := api.Group("/chat/sessions")` …）；按会话读取的处理函数写法见
`handleSessionPlanMarkdown`（`ParamsFromContext(r.Context()).ByName("id")`、`s.Sessions.HasSession` 不属于自己 → 404）。
子代理路由写法见 `handleSubagentContext`（`sid, agentKey, ok := s.subagentRoute(w, r)`，`run.SubagentContextGauge(…)` 的第一个返回值是
子代理的 worker 会话 id）。

## Commands you will need

```bash
export PKG_CONFIG_PATH=$PWD/third_party/pdfium/darwin_arm64   # 编译 go-pdfium 需要（Makefile 也是这么设的）
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run '<Name>' -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run '<Name>' -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/... -count=1           # 收尾全量
go vet ./pkg/tool/ ./pkg/gateway/ && gofmt -l pkg/
```

## Scope

- 改：`pkg/tool/display_header.go`（+测试）、新建 `pkg/tool/header_parts_test.go`、新建 `pkg/tool/testdata/header_actions.json`、
  `pkg/tool/format.go`（只动 `formatShellStep` 与 `turnDiffSection` 的围栏、`BuildToolMeta`）、新建 `pkg/tool/shell_card.go`（+测试）、
  `pkg/tool/notification.go`（`ToolMeta` 加一个字段）、`pkg/gateway/wsevents.go`、`pkg/gateway/server.go`（直播 step 两处）、
  `pkg/gateway/api_extra.go`（历史行字段、两条新路由与处理函数）及对应网关测试。
- **不改**：TUI（`pkg/tui/**`）、`DisplayHeader`/`DisplayParts` 的输出（TUI 逐字依赖）、给模型的工具结果、任何前端文件
  （web 在 136–138）。`call_headers` 旧字段本计划**保留**（136 改完 web 后由 136 删除）。

## Steps

### Step 1：卡头分段（`pkg/tool/display_header.go`）

1.1 新增类型与函数（放在 `DisplayParts` 之前）：

```go
// HeaderParts is a card header in its four parts, for a surface that lays the
// header out itself (the web): the action ("Read", "Running", "Failed to run"),
// what it acted on, the suffix ("lines 2-101", "matching “x”", an MCP call's
// input summary) and — apart from the suffix — the duration ("0.3s", or the
// running "12s"). DisplayParts joins the duration back into the suffix.
type HeaderParts struct {
	Action   string `json:"action"`
	Target   string `json:"target,omitempty"`
	Suffix   string `json:"suffix,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// DisplayHeaderParts is DisplayParts with the duration kept apart.
func DisplayHeaderParts(f DisplayFrame, cwd string) HeaderParts
```

1.2 实现方式：把 `DisplayParts` 的函数体原样搬进 `DisplayHeaderParts`，**只改末尾两段**：时长不再拼进 `suffix`，而是写进
`Duration`（完成/失败用 `FormatDuration(f.Duration)`，运行中 ≥1s 用 `FormatWorkingElapsed(f.Duration)`，条件与现在完全一样）；
返回值各段 `TrimSpace`。`DisplayParts` 改成：

```go
func DisplayParts(f DisplayFrame, cwd string) (action, target, suffix string) {
	p := DisplayHeaderParts(f, cwd)
	suffix = p.Suffix
	if p.Duration != "" {
		if suffix == "" {
			suffix = p.Duration
		} else {
			suffix = suffix + " · " + p.Duration
		}
	}
	return p.Action, p.Target, suffix
}
```

**Verify**：`TestDisplayHeaderIsTheTerminalsWording` 不改、仍通过（这证明 TUI 文案逐字不变）；新增
`TestDisplayHeaderPartsKeepTheDurationApart`：同一个 shell 完成帧（`Duration: 1500*time.Millisecond`）→
`HeaderParts{Action: "Ran", Target: "ls", Duration: "1.5s"}`；read_file 完成（带行范围）→ `Suffix: "lines 1-96"`、`Duration` 单独；
运行中 3s 的 shell → `Duration: FormatWorkingElapsed(3*time.Second)`；审批等待中 → `Duration == ""`；
并对这些帧断言 `DisplayParts` 的第三段等于 `Suffix + " · " + Duration`（或二者之一）。

### Step 2：动作词表（给 web 的中文表对齐用）

2.1 `display_header.go` 新增：

```go
// HeaderActions is every action DisplayHeaderParts can produce for a known
// tool, in English. The web words each one in the reader's language from a
// table keyed by these strings (frontend/src/locales, plan 136); the golden
// copy in testdata/header_actions.json is what the frontend's test reads, so
// a new action here fails both sides until the table has it.
func HeaderActions() []string
```

返回**排好序、去重**的列表，内容逐字来自 `DisplayParts` 的 switch：各工具的完成/进行时/失败（`"Failed to " + FailedActionPhrase(...)`）
动作、`request_permissions` / `retrieve_output` / `memories_*` 的拒绝与取消专用句（`Permission request denied`、`Memory read denied`、
`Output retrieval canceled` …）、`Kept planning`、`Loading skill`、`Skill`、`MCP`、`Todo`、`Updating todo`、`Notes`、`Saving notes`、
`Asked user`、`Asking user`、`Canceled`、`Denied`。（`default` 分支的 `action = title` 是工具名，不进词表。）

2.2 新建 `pkg/tool/header_parts_test.go`：
- `TestHeaderActionsCoverEveryKnownTool`：对工具名表 `read_file, edit_file, write_file, request_permissions, shell, web_fetch,
  web_search, lsp, retrieve_output（带 query 与不带）, session_todo, enter_plan_mode, exit_plan_mode, memories_add_ad_hoc_note,
  memories_list, memories_read, memories_search, intermediate_tool, user_interaction, mcp__github__create_issue` 与状态表
  `running, awaiting approval, completed, failed, denied, canceled`（write_file 完成再分 deleted>0 / =0 两种；skill 用
  `Meta.Category: "skill"` + `SkillName`/`SkillPath` 的 loading/completed/failed）逐一构造 `DisplayFrame`，断言
  `DisplayHeaderParts(...).Action` 都在 `HeaderActions()` 里；
- `TestHeaderActionsGolden`：`HeaderActions()` 与 `testdata/header_actions.json`（一个 JSON 字符串数组，排序后）逐项相等；
  不等时失败信息写明「更新 testdata/header_actions.json 并同步 frontend 的中文表」。先写测试、再生成文件
  （可在测试里加 `if os.Getenv("UPDATE_HEADER_ACTIONS") == "1" { 写文件 }`，生成后照常跑一遍确认通过）。

**Verify**：`go test … ./pkg/tool/ -run 'HeaderActions|DisplayHeaderParts' -count=1` → `ok`。

### Step 3：网关发出分段（`pkg/gateway/wsevents.go`、`server.go`、`api_extra.go`）

3.1 `wsevents.go`：新增 `toolHeaderPartsFor(...)`，参数与 `toolHeaderFor` 相同，返回 `tool.HeaderParts`（同一个 `DisplayFrame`，
调 `tool.DisplayHeaderParts`）；`toolHeaderFor` 不变。
`withStepHeader` 改为同时写两样（签名改为 `withStepHeader(data map[string]any, header string, parts tool.HeaderParts)`）：
`data["header"] = header`（不变）与 `data["header_parts"] = parts`（`parts.Action == ""` 时不写）。`server.go` 两个调用点同步传入
`toolHeaderPartsFor(…同样的参数…)`。

3.2 `toolEventPayloadWithHeader`：在写 `fields["header"]` 的同时写 `fields["header_parts"] = parts`。

3.3 `storedToolRowHeader` 拆出 `storedToolRowHeaderParts(root, partsJSON, toolMetaJSON string, took time.Duration) (string, tool.HeaderParts)`
（原函数改为调用它并返回第一项）；`api_extra.go` 历史行结构体加
`HeaderParts *tool.HeaderParts `json:"header_parts,omitempty"``，`case "tool":` 处一并填上（`Action` 为空则不填）。

3.4 `canceledCallHeaders` 旁新增：

```go
// callHeaderEntry is one call's header on an assistant row, as a list entry
// rather than a map key: the web camel-cases response keys deeply, which
// rewrites a call id used as a key ("call_abc" → "callAbc").
type callHeaderEntry struct {
	CallID string          `json:"call_id"`
	Header string          `json:"header"`
	Parts  tool.HeaderParts `json:"parts"`
}
func canceledCallHeaderList(root func() string, partsJSON string) []callHeaderEntry
```

（与 `canceledCallHeaders` 同样的解析与过滤，按 parts 里调用出现的顺序。）历史行加 `CallHeaderList []callHeaderEntry `json:"call_header_list,omitempty"``，
`case "assistant":` 一并填上。**旧的 `CallHeaders` 保留不动**（136 删除）。

**Verify**：`wsevents_test.go` 扩充 `TestToolHeadersSayWhatTheTerminalSays`（或新增 `TestToolHeadersCarryTheirParts`）：
`toolEventPayloadWithHeader` 的输出同时有 `header` 与 `header_parts`，且 `header_parts.action/target/duration` 与
`tool.DisplayHeaderParts` 一致；新增 `TestCanceledCallHeaderListKeepsTheCallIDs`：一条含两个调用（id `call_a_1`、`toolu_01X`）的
assistant parts → 列表两项、`call_id` 原样；`handleChatMessages` 的现有测试（找 `Header` 断言所在的用例）补断言 `header_parts`。
`go test … ./pkg/gateway/ -run 'ToolHeader|CallHeader|ChatMessages' -count=1` → `ok`。

### Step 4：shell 正文的围栏不被内容截断（`formatShellStep`）

stdout/stderr 里出现一行 ```` ``` ```` 会提前闭合三反引号围栏，web 与 TUI 都会读错。新增（`format.go`，`formatShellStep` 附近）：

```go
// fenceFor is the shortest backtick fence longer than any backtick run at the
// start of a line in text, never shorter than three.
func fenceFor(text string) string
```

stdout、stderr 两个块改用 `fence := fenceFor(stdout)` / `fenceFor(stderr)` 开合（信息串照旧 `text`/`diff`）。input/output 两个 JSON
块不需要（JSON 里不会有行首反引号串）。
**Verify**：`format_test.go` 新增 `TestFormatShellStepFencesOutputThatHasFences`：stdout 为 ``"a\n```\nb"`` → 正文里 stdout 用
四个反引号开合、`b` 仍在围栏内；现有 shell 格式测试全部仍通过。

### Step 5：shell 卡事实（新建 `pkg/tool/shell_card.go`）

```go
// ShellCard is what a shell card shows, in parts, read back from the card's
// own body (formatShellStep): a reloaded card and a live one come from the
// same body, so they always agree. Absent facts are zero.
type ShellCard struct {
	Stdout             string          `json:"stdout,omitempty"`
	Stderr             string          `json:"stderr,omitempty"`
	ExitCode           *int            `json:"exit_code,omitempty"`
	StdoutOmittedBytes int             `json:"stdout_omitted_bytes,omitempty"`
	StderrOmittedBytes int             `json:"stderr_omitted_bytes,omitempty"`
	OmittedBytes       int             `json:"omitted_bytes,omitempty"`       // "output omitted N bytes" (neither stream named)
	RetrieveID         int64           `json:"retrieve_id,omitempty"`
	SpoolOmittedBytes  int             `json:"spool_omitted_bytes,omitempty"` // past the spool's 64 MB cap: not kept
	Files              []ShellCardFile `json:"files,omitempty"`
	Notes              []string        `json:"notes,omitempty"`
}

// ShellCardFile is one changed file (plan 130's DiffBlock) with JSON names.
type ShellCardFile struct {
	Verb        string `json:"verb"` // "Updated", "Created" or "Deleted"
	Path        string `json:"path"`
	Added       int    `json:"added"`
	Deleted     int    `json:"deleted"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}

// ParseShellCard reads formatShellStep's body back into its facts.
func ParseShellCard(body string) (ShellCard, bool)
```

解析规则（与 `formatShellStep` 的写法一一对应，写在同一个包里，round-trip 由测试钉死）：
- 逐行扫描并跟踪围栏（同 `ParseDiffBlocks`：一行 `TrimSpace` 后以 ≥3 个反引号开头即进入，记下个数 n；遇到 `TrimRight` 后恰为
  n 个反引号的行结束）。
- 围栏外一行恰为 `stdout:` / `stderr:` 时，紧随其后的围栏体（不含开闭行）就是 `Stdout` / `Stderr`。
- 围栏外以 `preview truncated: ` 开头的行：用正则读 `stdout omitted (\d+) bytes`、`stderr omitted (\d+) bytes`、`output omitted (\d+) bytes`。
- 围栏外一行恰为 `output:` 时，紧随其后信息串为 `json` 的围栏体 `json.Unmarshal` 成 map：`exit_code`（存在才设 `ExitCode`）、
  `retrieve_id`、`stdout_omitted_bytes`、`stderr_omitted_bytes`、`spool_omitted_bytes`（用 `format.go` 的 `intFromAny`）。
  解析失败（正文被 `clampBody` 截断等）就跳过，不报错。
- `Files`/`Notes`：`ParseDiffBlocks(body)` 的结果逐项转换。
- `ok`：正文以 `**shell**` 开头。
- `input:` 块与 LSP 诊断段忽略（web 不显示它们）。

**Verify**：新建 `pkg/tool/shell_card_test.go`，`TestParseShellCardReadsBackWhatFormatShellStepWrote`，表驱动，每例用真实的
`FormatToolStepResult`（或直接 `formatShellStep`）生成正文再解析，断言得到的 `ShellCard` 等于期望（直播 map 一份、经
`json.Marshal`/`Unmarshal` 的重放 map 一份）：① 只有 stdout、`exit_code: 0`；② stdout+stderr、`exit_code: 2`；③ stdout 里有行首
```` ``` ````（依赖 Step 4）；④ `stdout_omitted_bytes: 412000` + `retrieve_id: 7` + `spool_omitted_bytes: 0`（底栏要的数）；⑤ apply_patch
（两个文件、无 input 块）；⑥ `shell_edit_diff`（一个文件 + `MoreFiles: 3` + `Shared`）→ `Files` 与 `Notes` 逐字；⑦ 非 shell 正文 → `ok == false`。
`go test … ./pkg/tool/ -run 'ShellCard|FormatShellStep|DiffBlocks' -count=1` → `ok`。

### Step 6：网关发出 shell 卡事实

- 新增（`wsevents.go`）`func shellCardFor(toolName, body string) *tool.ShellCard`：工具名（大小写不敏感）是 `shell` 且
  `tool.ParseShellCard(body)` 成功才返回，否则 nil。
- 直播完成 step（`server.go` 那处 `gatewayToolCompletedStepData(… rendered.DisplayBody …)`）：返回的 data 上加
  `data["shell_card"] = card`（非 nil 时）。用 `rendered.DisplayBody`，**不要**用 `evt.Output` 另算——直播与重载必须是同一份正文。
- `toolEventPayloadWithHeader`：`final` 时用 `fields["display_body"]` 同样加 `fields["shell_card"]`。
- 历史行：结构体加 `ShellCard *tool.ShellCard `json:"shell_card,omitempty"``，`case "tool":` 用 `state.ParseToolDisplayPart(t.PartsJSON)`
  的 `Body` 与 meta 里的工具名算出（`storedToolRowHeaderParts` 已经解析了这两样，可以让它多返回一个 body，避免重复解析）。

**Verify**：网关测试 `TestShellCardRidesTheStepTheEventAndTheStoredRow`：同一份 shell 完成正文分别经直播 step data、
`toolEventPayloadWithHeader`、`handleChatMessages` 历史行三条路得到的 `shell_card` JSON 完全相同；非 shell 行没有 `shell_card`。

### Step 6b：文件改动卡的 diff 事实（edit_file / write_file）

web 的“已编辑 / 已创建”卡要用与 shell 卡同一种 diff 行（owner 2026-10-25：diff 全站统一），所以它们也要结构化的 diff。
这两类卡的正文由 `formatGenericToolStep` 写：开头是 `turnDiffSection(evt.Output)`——一行 ``turn diff: `<path>` (+A/-D)``，
有 diff 时空一行接 ```` ```diff ```` 围栏——之后可能还有 LSP 诊断段等。

- `pkg/tool/shell_card.go` 加：
  ```go
  // FileDiff is the change a file tool's card shows (its turn diff), read back
  // from the card's body; Rest is the body after that section (diagnostics and
  // the like), for the surface to render as it always has.
  type FileDiff struct {
  	Path        string `json:"path"`
  	Added       int    `json:"added"`
  	Deleted     int    `json:"deleted"`
  	UnifiedDiff string `json:"unified_diff,omitempty"`
  	Rest        string `json:"rest,omitempty"`
  }
  func ParseFileDiff(body string) (FileDiff, bool)
  ```
  规则：第一行（`TrimSpace` 后）匹配 `` ^turn diff: `(.+)` \(\+(\d+)/-(\d+)\)$ `` 才算；其后紧跟（中间只允许空行）的信息串为 `diff`
  的围栏体是 `UnifiedDiff`（围栏跟踪同 Step 5，并把 `turnDiffSection` 的三反引号围栏也换成 Step 4 的 `fenceFor(diff)`）；剩余正文
  `TrimSpace` 后是 `Rest`。
- 网关：与 Step 6 同样的三处，工具名是 `edit_file` / `write_file` 且 `ParseFileDiff` 成功时加 `file_diff`（历史行字段
  `FileDiff *tool.FileDiff `json:"file_diff,omitempty"``）。

**Verify**：`shell_card_test.go` 加 `TestParseFileDiffReadsTheTurnDiffSection`（用 `formatGenericToolStep` 生成带 diff + LSP 诊断的正文 →
`Path/Added/Deleted/UnifiedDiff` 逐字、`Rest` 以 LSP 段开头；无 diff 的正文 → false）；网关测试扩充：edit_file 行三条路的 `file_diff` 一致。

### Step 7：拒绝说明进 `ToolMeta`

- `pkg/tool/notification.go` `type ToolMeta struct` 加
  ```go
  	// DenyFeedback is the user's own words when they refused this call, for
  	// the card that shows the refusal; empty for every other call.
  	DenyFeedback string `json:"deny_feedback,omitempty"`
  ```
- `format.go` `BuildToolMeta`：`if evt.Denied { meta.DenyFeedback = strings.TrimSpace(evt.DenyFeedback) }`。
- 网关已经把 `ToolMeta` 整个发出（直播 `tool_meta`、历史行 `tool_meta_json`、事件 payload `tool_meta`），不需要再改。

**Verify**：`format_test.go` 新增 `TestBuildToolMetaKeepsTheRefusalWords`：`DeniedToolStep("s1", "write_file", input, "复用 http_server.go")`
→ `BuildToolMeta(...).DenyFeedback == "复用 http_server.go"`；非拒绝步骤为空。

### Step 8：完整输出接口

8.1 路由（`api_extra.go`，放在 `chatSessions.Get("/:id/plans", …)` 附近）：

```go
	chatSessions.Get("/:id/tool-output/:retrieve", s.handleChatToolOutput)
	chatSessions.Get("/:id/subagents/:agent/tool-output/:retrieve", s.handleSubagentToolOutput)
```

8.2 `handleChatToolOutput`（照 `handleSessionPlanMarkdown` 的写法）：GET；`sid` 为空 → 404；`s.Sessions.HasSession` 不属于 → 404；
`retrieve` 解析成正整数失败 → 400；`r := s.runnerFor(ctx, sid)`（nil 时用 `s.Runner`）；
`store := tool.CompressorFor(r.StateRoot()).Store()`；`entry, err := store.Get(id)`：`sql.ErrNoRows` → 404，其它错误 → 500；
**`strings.TrimSpace(entry.SessionID) != sid` → 404**（别的会话的输出不可读）。
响应 `application/json`：`{"command": entry.Command, "text": entry.OriginalOutput, "spool_omitted_bytes": entry.SpoolOmittedBytes}`。

8.3 `handleSubagentToolOutput`：`sid, agentKey, ok := s.subagentRoute(w, r)`；用 `handleSubagentContext` 同样的方式取得子代理的
worker 会话 id（`run.SubagentContextGauge` 的第一个返回值；若 `pkg/run` 里紧挨着它有更轻的只取会话 id 的函数，用那个）；
校验 `entry.SessionID == workerSessionID`，其余同 8.2。

8.4 大小：`OriginalOutput` 最大约 64 MB（spool 上限）。本计划**不分页**（web 侧在 137 按需截断显示）；若实测
`Store.Get` 对 64 MB 文件超过 2 秒或内存翻倍到不可接受，STOP 4 回报。

**Verify**：网关测试 `TestChatToolOutputServesItsOwnSessionOnly`：用临时 state root 建 store、`Save` 一条 `SessionID: "s1"`、带
spool 文件的记录 → `GET /api/chat/sessions/s1/tool-output/<id>` 200 且 `text` 等于文件内容；`s2` → 404；不存在的 id → 404；`abc` → 400。
（会话存在性：照现有会话读取测试的 fake/真实 `Sessions` 写法。）

### Step 9：收尾

`CGO_ENABLED=1 go test -tags fts5 ./pkg/... -count=1`（与 dev 基线对比：只允许出现基线就有的失败，见 `plans/README.md` 顶部记录的存量失败）、
`go vet ./pkg/tool/ ./pkg/gateway/`、`gofmt -l pkg/` 为空、`PKG_CONFIG_PATH=… go build -tags fts5 ./...` 退出 0。

## Done criteria

- [ ] `TestDisplayHeaderIsTheTerminalsWording` 未改且通过（TUI 文案逐字不变）
- [ ] `HeaderActions()` 与 `pkg/tool/testdata/header_actions.json` 一致，覆盖测试通过
- [ ] 直播 step、子代理事件、历史行都带 `header_parts`；历史 assistant 行带 `call_header_list`（数组，`call_id` 原样）
- [ ] shell 行三条路的 `shell_card` 完全一致；`ParseShellCard` round-trip 七例通过
- [ ] edit_file / write_file 行三条路的 `file_diff` 一致；`ParseFileDiff` 测试通过
- [ ] 拒绝步骤的 `tool_meta.deny_feedback` 是用户原话
- [ ] `GET /api/chat/sessions/:id/tool-output/:retrieve` 与子代理版本可读本会话的完整输出、拒绝其它会话
- [ ] `go test ./pkg/...` 无新增失败；`go vet`、`gofmt -l pkg/` 干净
- [ ] `git status --short` 只含 Scope 内文件

## STOP conditions

1. 前置（130、132）未落地，或摘录里的符号找不到 / 内容不符。
2. 改动 `DisplayParts` 后 `TestDisplayHeaderIsTheTerminalsWording` 或任何 TUI 测试失败——说明拼接顺序变了，回报，不要改测试。
3. `ParseShellCard` 某个 round-trip 例子必须改 `formatShellStep` 除 Step 4 以外的写法才能通过——回报。
4. 完整输出接口对 64 MB spool 的读取明显不可接受（见 8.4）。
5. 子代理的 worker 会话 id 拿不到，或 `entry.SessionID` 与它对不上（spool 记录的会话 id 不是 worker 会话）——回报实测值。
6. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 新增或改动任何卡头动作词：改 `DisplayParts` 的同时更新 `HeaderActions()`、`testdata/header_actions.json`，并在
  `frontend/src/locales` 的中文卡头表里加对应词（136 的前端测试会读同一个 JSON，缺词即红）。
- `formatShellStep` 与 `ParseShellCard` 是一对：改正文的任何一段（标签、围栏、注的句子），同步改解析与 round-trip 测试。
- `call_headers`（map）在 136 删除；之后任何「以调用 id 为键」的响应字段都用数组，原因见 Step 3.4 的注释。
