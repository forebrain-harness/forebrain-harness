# Plan 131：PostToolUse hook 收到的 shell `tool_response` 改为对象并带上本次改动的文件（对齐 Claude Code）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`plans/130` 已完成——`grep -n "func DecodeShellEditDiff" pkg/tool/shell_diff.go`、
> `grep -n '"shell_edit_diff"' pkg/tool/shell_tool.go` 各有结果；以及 `grep -n "ToolResponse: out," pkg/hook/tool_middleware.go`、
> `grep -n "ToolUseID:    tool.ToolUseIDFromContext," pkg/run/runner.go` 各有结果。
>
> **Drift check（先跑）**：`git diff --stat c72da06 -- pkg/hook/runtime.go pkg/hook/tool_middleware.go pkg/run/runner.go`
> 有输出就把「Current state」摘录逐条对照；不符即 STOP 1。（`pkg/tool/shell_diff.go` 由 130 新建，不在 drift 范围内。）

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: MED（**对用户 hook 脚本是可见的格式变化**：shell 的 `tool_response` 从 JSON 字符串变为 JSON 对象）
- **Depends on**: plans/130-shell-and-apply-patch-diff-blocks.md
- **Category**: direction（owner 2026-10-25：「对齐，单独立计划」）
- **Planned at**: dev `c72da06`，2026-10-25

## Why this matters

Claude Code（2.1.301）的 `bashEditDiff` 有两个用途：画在 Bash 结果下面，以及交给 **PostToolUse Bash hook**——它的 schema
描述原文：「Per-file diff of the working-tree changes this command made, for rendering and for PostToolUse Bash hooks
(changedFiles: absolute paths of every changed file known, shown or not, at most 200; …); not surfaced to the model.」
Claude Code 的 PostToolUse 输入里 `tool_response` 是工具结果**对象**（Bash：`{stdout, stderr, interrupted, …, bashEditDiff}`）。

forebrain 现在给 PostToolUse 的 `tool_response` 是工具返回给模型的那个值——对 shell 来说是一段 **JSON 字符串**
（`{"stdout":…,"stderr":…,"exit_code":…}` 编码后的文本），而且 130 新增的 `shell_edit_diff` 只在界面用的完成输出里，
hook 拿不到。owner 裁定与 Claude Code 对齐：shell 的 `tool_response` 改为对象，并带上 `shell_edit_diff`（含 `changed_files`）。

## Current state

- `pkg/hook/runtime.go:35-52`：

```go
type Runtime struct {
	Home string
	...
	RunAgentHook          func(context.Context, string) (string, error)
	SessionID             func(context.Context) string
	ToolUseID             func(context.Context) string
	PermissionMode        func() string
}
```
- `pkg/hook/runtime.go:140-146`：

```go
type PostToolUseInput struct {
	BaseInput
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse any            `json:"tool_response"`
	ToolUseID    string         `json:"tool_use_id"`
}
```
- `pkg/hook/tool_middleware.go:72-104`：`out, err := next(ctx, arguments)`；成功时

```go
			post, err := rt.ExecutePostToolUse(hookCtx, PostToolUseInput{
				BaseInput: BaseInput{
					HookEventName:  EventPostToolUse,
					SessionID:      sessionID,
					TranscriptPath: transcriptPath,
					Cwd:            runtimeCWD(rt.Home),
					PermissionMode: permMode,
				},
				ToolName:     toolName,
				ToolUseID:    toolUseID,
				ToolInput:    inputMap,
				ToolResponse: out,
			})
```
  之后 `post.UpdatedMCPToolOutput != nil` 时替换返回值，否则 `return out, nil`——**返回给模型的值不受 hook 输入影响**，本计划也不能让它受影响。
- 依赖方向：`pkg/hook` 只导入 `pkg/llm`、`pkg/config`、`pkg/state` 等，**不导入 `pkg/tool`**，且 hook 的包扇出已在上限（`pkg/architecture`
  `fanOutBudgets["hook"] = {current: 8, target: 8}`）。跨包取数据的现成做法是**函数注入**：`pkg/run/runner.go:1167-1205`

```go
	hookRT := &hook.Runtime{
		...
		RunAgentHook: r.RunAgentHook,
		SessionID:    llm.AgentSessionIDFromContext,
		ToolUseID:    tool.ToolUseIDFromContext,
		PermissionMode: func() string {
	...
	r.tools.AddToolMiddleware(hook.NewToolMiddleware(hook.ToolMiddlewareOptions{
```
  这是唯一装配工具中间件的地方（`grep -rn "NewToolMiddleware(" pkg --include='*.go' | grep -v _test` 只有 `runner.go:1205`）；
  gateway / TUI 里另外几处 `&hook.Runtime{` 不装工具中间件，不用改。
- 中间件拿到的 `ctx` 就是编排层为这次调用打开了完成捕获槽的 `toolCtx`（`pkg/run/orchestration_llm.go:1409`
  `toolCtx := withToolStepCapture(...)` → `executeToolHandler(toolCtx, …)`），所以 `next()` 返回后
  `tool.ToolCompletionFromContext(ctx)` 能读到 shell 刚写的完成输出（130 把 `shell_edit_diff` 放在那里）。
- 130 之后 `pkg/tool/shell_diff.go` 有 `type ShellEditDiff struct{…}`（json：`files`、`more_files`、`changed_files`、`unavailable`、
  `skipped`、`shared`、`tree_moved`）与 `func DecodeShellEditDiff(v any) (ShellEditDiff, bool)`。
- shell 工具给模型的返回值（`pkg/tool/shell_tool.go` `runSandboxedShellCommand` 的 `resultPayload` 经 `json.Marshal` 后的 `string(b)`）：
  键有 `stdout`、`stderr`、`exit_code`，可能还有 `output_retrieval`、`output_compression`、`yolo`、`approval_bypassed_by_yolo`；
  apply_patch 的返回值另有 `apply_patch: true` 与 `files`（每个带 `turn_diff`）。
- 测试范例：`pkg/hook/runtime_test.go:240-273` `TestToolMiddlewareBlocksRealToolExecution`（`state.OpenStateForTest`、`config.HooksSettings{EventPreToolUse: …}`、
  `llm.NewTool` + `testTool.Use(NewToolMiddleware(...))` + `testTool.Handle(llm.WithAgentSessionID(ctx, "s1"), "{}")`）。
- 文档：`forebrain-harness.github.io/docs/config/hooks-and-skill-extensions.md:74-76` 与
  `forebrain-harness.github.io/docs/zh/config/hooks-and-skill-extensions.md` 对应段落只说「Use `PostToolUse` when…」，没有写输入字段。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 环境 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"` | — |
| 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/hook/... ./pkg/tool/... ./pkg/run/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/hook/... ./pkg/tool/... ./pkg/run/...`；`gofmt -l pkg/` | 无输出 |

## Scope

**In scope**：`pkg/hook/runtime.go`、`pkg/hook/tool_middleware.go`、`pkg/hook/runtime_test.go`、`pkg/tool/shell_diff.go`、`pkg/tool/shell_diff_test.go`、
`pkg/run/runner.go`（只加一行注入）、`forebrain-harness.github.io/docs/config/hooks-and-skill-extensions.md`、
`forebrain-harness.github.io/docs/zh/config/hooks-and-skill-extensions.md`。

**Out of scope**：其它工具的 `tool_response`（保持原样，只改 shell）；`PostToolUseFailure`；返回给模型的值；`pkg/hook` 的任何新导入。

## Steps

### Step 1：hook 运行时多一个注入点

`pkg/hook/runtime.go` 的 `Runtime` 末尾加：

```go
	// ToolResponse shapes the tool_response a PostToolUse hook receives from the
	// tool's raw result. Nil hands the raw result over unchanged. It never
	// changes what the model receives.
	ToolResponse func(ctx context.Context, toolName string, out any) any
```
`pkg/hook/tool_middleware.go`：`ExecutePostToolUse` 之前

```go
			response := out
			if rt.ToolResponse != nil {
				response = rt.ToolResponse(ctx, toolName, out)
			}
```
并把 `ToolResponse: out,` 改为 `ToolResponse: response,`。其余（含 `return out, nil`）不动。

**Verify**：`pkg/hook/runtime_test.go` 新增 `TestToolMiddlewarePostToolUseGetsTheShapedResponse`（照 `TestToolMiddlewareBlocksRealToolExecution` 搭）：
PostToolUse hook 命令 `cat > <t.TempDir()/in.json>`（matcher `demo_tool`），`rt.ToolResponse` 设为返回 `map[string]any{"shaped": out}` 的函数；
调用后读 `in.json`：`tool_response` 是对象且 `shaped == "ok"`；`Handle` 的返回值仍是 `"ok"`。再加一例 `rt.ToolResponse == nil` 时 `tool_response == "ok"`（字符串，现状）。
`CGO_ENABLED=1 go test -tags fts5 ./pkg/hook/ -run 'ToolMiddleware' -count=1` → `ok`。

### Step 2：shell 的 tool_response 形状（`pkg/tool/shell_diff.go`）

```go
// HookToolResponse is the tool_response a PostToolUse hook receives (Claude
// Code's shape): for shell, the result object the model was given, decoded,
// plus the working-tree diff the command made; any other tool's result passes
// through unchanged.
func HookToolResponse(ctx context.Context, toolName string, out any) any
```
- `toolName != "shell"` → 返回 `out`。
- `out` 是 `string` 且 `json.Unmarshal` 成 `map[string]any` 成功 → `obj`；否则返回 `out`（解不开就不变）。
- `completion, ok := ToolCompletionFromContext(ctx)`；`ok` 且 `DecodeShellEditDiff(completion.Output["shell_edit_diff"])` 成功 →
  `obj["shell_edit_diff"] = diff`（`ShellEditDiff` 值，序列化后是蛇形键，`changed_files` ≤200 已由 130 保证）。
- 返回 `obj`。apply_patch 的结果本来就带 `files`，不另加字段。

**Verify**：`pkg/tool/shell_diff_test.go` 新增 `TestHookToolResponse`：① 非 shell → 原值（同一指针/同一字符串）；② shell + 合法 JSON 字符串 + 捕获里有
`shell_edit_diff` → 对象含 `stdout` 与 `shell_edit_diff.changed_files`；③ shell + 捕获里没有 → 对象不含该键；④ shell + 非 JSON 字符串 → 原字符串。
`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'HookToolResponse' -count=1` → `ok`。

### Step 3：装配

`pkg/run/runner.go` 的 `hookRT := &hook.Runtime{` 里，`ToolUseID:    tool.ToolUseIDFromContext,` 下一行加 `ToolResponse: tool.HookToolResponse,`。

**Verify**：`grep -n "ToolResponse: tool.HookToolResponse" pkg/run/runner.go` 有结果；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/hook/... ./pkg/tool/... ./pkg/run/... ./pkg/architecture/... -count=1` 全部 `ok`。

### Step 4：文档

两份 `hooks-and-skill-extensions.md` 的 PostToolUse 段落各加一句（中英文对应）：
- EN：`` For the `shell` tool, `tool_response` is the result object (`stdout`, `stderr`, `exit_code`, …); when the command changed files in a git work tree it also carries `shell_edit_diff`, whose `changed_files` lists up to 200 absolute paths. ``
- ZH：`` `shell` 工具的 `tool_response` 是结果对象（`stdout`、`stderr`、`exit_code` 等）；命令在 git 工作树里改了文件时还带 `shell_edit_diff`，其中 `changed_files` 列出至多 200 个绝对路径。 ``

**Verify**：`grep -n "shell_edit_diff" forebrain-harness.github.io/docs/config/hooks-and-skill-extensions.md forebrain-harness.github.io/docs/zh/config/hooks-and-skill-extensions.md` 两处各一条。

## Test plan

`runtime_test.go`：注入点生效 / 未注入时保持字符串；`shell_diff_test.go`：`HookToolResponse` 四种情况；全量包测试含 `pkg/architecture`（证明 hook 没有新导入）。

## Done criteria

- [ ] Step 3 的全量命令通过；`go vet` / `gofmt -l pkg/` 无输出
- [ ] `git diff c72da06 -- pkg/hook | grep '^+.*"github.com/forebrain-harness'` 无输出（hook 没有新增模块内导入）
- [ ] 模型收到的 shell 结果不变：130 的 `TestShellToolShowsTheFilesACommandChanged`（返回字符串不含 `shell_edit_diff`）仍通过
- [ ] 文档两处已更新

## STOP conditions

1. 前置 grep 不满足（130 未完成）或锚点不符。
2. 中间件的 `ctx` 里读不到完成捕获（`ToolCompletionFromContext` 为空）——说明调用路径与「Current state」描述不同，回报，不要改编排层。
3. `pkg/architecture` 报 hook 或 tool 扇出增长。
4. 有现有测试断言 PostToolUse 的 shell `tool_response` 是字符串——回报用例名（owner 已裁定改为对象，评审改断言）。

## Maintenance notes

- 这是对 hook 脚本可见的格式变化：以前对 shell 的 `tool_response` 做 `fromjson` 的脚本需要去掉那一步。合并说明里要写明。
- 以后若别的工具也要给 hook 结构化结果，在 `HookToolResponse` 里按工具名加分支，不要在 `pkg/hook` 里认工具。
- `shell_edit_diff` 的字段由 130 定义；改形状时两份计划的测试一起改。
