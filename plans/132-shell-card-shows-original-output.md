# Plan 132：shell 卡片显示命令的原始输出；输出精简只作用于给模型的结果

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动，**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`grep -n "compression := shellCompression{}" pkg/tool/shell_tool.go`、
> `grep -n "completionOutput := map\[string\]any{" pkg/tool/shell_tool.go` 各有一条结果。本计划与 130 / 131 无依赖，可以先做也可以后做
> （130 也改 `runSandboxedShellCommand`，但改的是成功路径上 `captureCompletion()` 之前的另一处，两者不冲突）。
>
> **Drift check（先跑）**：`git diff --stat c72da06 -- pkg/tool/shell_tool.go` 有输出就对照下面的摘录；锚点不符即 STOP 1。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（只改界面拿到的那份输出；给模型的结果一个字节不变）
- **Depends on**: —
- **Category**: bug（owner 2026-10-25 同意：「卡片一律显示原始输出，精简只给模型」）
- **Planned at**: dev `c72da06`，2026-10-25

## Why this matters

引擎有一个节省 token 的 shell 输出精简（`compressShellOutput`，`pkg/tool/filters/` 下 113 个过滤器，按命令匹配，比如 `git status / diff / push / pull`、
`go test`、`make`、`npm / pnpm`、`pytest`、`cargo`、`golangci-lint`）。它的本意只是压缩**给模型**的上下文，但现在精简后的文本同时被写进了
**给界面**的完成输出，所以：

- TUI 与 web 的 shell 卡片显示的都是精简版，用户看不到命令真正打印的内容（被去掉的行只是模型不需要，用户可能需要）；
- TUI 上有可见的跳变：运行期间实时流出的是原始输出，命令结束时卡片内容被换成精简版，刚才滚过的行在结束那一刻变了样。

改为：给模型的结果继续用精简版；完成输出（TUI 与 web 画卡片的那一份）保留原始 stdout。两个界面同时变正确，结束时的跳变也随之消失。

## Current state

`pkg/tool/shell_tool.go` 的 `runSandboxedShellCommand`（`:275`）在命令跑完后：

- `:546` `stdout := res.Stdout`（预览已经是引擎截过的头 32 KB + 尾 96 KB，截断时另有完整输出落盘，见 `pkg/safety/process.go:18-21`）。
- `:560-570`（原样）：

```go
	// Token-saving output compression (Boost-style, fail-open): compress the
	// final stdout on the return path before it enters the model context. The
	// original is kept recoverable via retrieve_output. Only stdout is touched;
	// stderr stays verbatim because errors carry dense signal.
	compression := shellCompression{}
	if res.StdoutOmittedBytes == 0 && res.StderrOmittedBytes == 0 {
		compression = compressShellOutput(ctx, rt, req.command, stdout)
	}
	if compression.Output != "" {
		stdout = compression.Output
	}
```
- `:571-574` 给模型的 `resultPayload := map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": exitCode}`，`:594` 序列化成返回给模型的字符串。
- `:595-608` 给界面的 `completionOutput := map[string]any{"stdout": stdout, …}`——**这里用的也是精简后的 `stdout`，这就是 bug**。
- `:614-618`：

```go
	if compression.Meta.Applied {
		completionOutput["output_compression"] = compressionMetaMap(compression.Meta)
		completionOutput["stdout_compressed_bytes"] = len(stdout)
		completionOutput["stdout_saved_bytes"] = compression.Meta.BeforeBytes - compression.Meta.AfterBytes
	}
```

谁读完成输出里的 `stdout`：只有显示层——`pkg/tool/format.go:716`（`formatShellStep` 取 `evt.Output["stdout"]` 生成卡片正文，TUI 帧与 web 的
`display_body` 都来自它）。模型拿到的是 `resultPayload` 序列化后的字符串，与完成输出无关；PostToolUse hook 拿到的也是给模型的那份。

测试范例：`pkg/tool/request_permissions_test.go:207` `TestApplyPatchOperations`（`root := t.TempDir(); t.Chdir(root); st := NewState(root);
NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}})`，`tool.Handle(ctx, string(args))`）；
完成输出用 `ctx := WithToolCompletionCapture(context.Background())` + `ToolCompletionFromContext(ctx)` 读（`pkg/tool/shell_tool_test.go:55-92`）；
精简生效的样例输出见 `pkg/tool/shell_output_test.go:261` `TestCompressShellOutputRoundTrip`（`make` 过滤器：输出里要有 `make[1]: Entering directory`，
`make[N]:` 开头的行会被去掉）。精简需要 `AgentToolRuntime.WorkspaceRoot`（`StateRoot()` 非空）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 环境 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" FOREBRAIN_HOME="$(mktemp -d)"` | — |
| 单测 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'ShellToolCardKeepsTheOriginalOutput|CompressShellOutput' -count=1` | `ok` |
| 回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/tui/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool/...`；`gofmt -l pkg/` | 无输出 |

## Scope

**In scope**：`pkg/tool/shell_tool.go`（只改 `:560-618` 这一段）、`pkg/tool/shell_tool_test.go`（新增一个测试）。

**Out of scope**：精简引擎与过滤器（`pkg/tool/shell_output.go`、`pkg/tool/filters/**`）、`retrieve_output`、给模型的 `resultPayload`、TUI / web / gateway 代码
（它们读的就是完成输出，修好数据就修好了显示）、截断与落盘（`pkg/safety/process.go`）。

## Steps

### Step 1：完成输出保留原文

在 `compression := shellCompression{}` 之前加一行 `rawStdout := stdout`，并把注释改成说明两份输出各给谁：

```go
	// Token-saving output compression (Boost-style, fail-open) is for the model
	// only: the result it receives carries the compressed stdout, while the
	// completion output the surfaces draw the card from keeps what the command
	// printed (owner 2026-10-25). The original also stays recoverable via
	// retrieve_output. Only stdout is touched; stderr stays verbatim because
	// errors carry dense signal.
	rawStdout := stdout
	compression := shellCompression{}
```
`completionOutput` 里把 `"stdout": stdout,` 改为 `"stdout": rawStdout,`。其余不动：`resultPayload` 仍用精简后的 `stdout`；
`stdout_compressed_bytes`（`len(stdout)`，精简后的字节数）、`stdout_saved_bytes`、`output_compression` 这些元数据照旧写进完成输出。

**Verify**：`grep -n '"stdout":                     rawStdout' pkg/tool/shell_tool.go` 一条结果；`grep -n '"stdout":    stdout' pkg/tool/shell_tool.go` 仍在（resultPayload）；
`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool/` 通过。

### Step 2：测试

`pkg/tool/shell_tool_test.go` 新增 `TestShellToolCardKeepsTheOriginalOutputWhenTheModelGetsItCompressed`（`exec.LookPath("make")` 失败时 `t.Skip`；用 `t.Chdir`，不能 `t.Parallel`）：

1. `root := t.TempDir()`，写 `Makefile`（配方行必须以制表符开头）：
   ```make
   all:
   	@echo "make[1]: Entering directory '/tmp/app'"
   	@for i in $$(seq 1 400); do echo "make[2]: progress step xxxxxxxxxxxxxxxx [ OK ]"; done
   	@echo "ERROR: src/api.c:42:18: error: expected ';' before '}' token"
   	@echo "make[1]: Leaving directory '/tmp/app'"
   ```
2. `t.Chdir(root)`；`ResetCompressors()` 并 `defer ResetCompressors()`；`NewShellTool(NewState(root), &AgentToolRuntime{WorkspaceRoot: root, Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}})`；
   `ctx := WithToolCompletionCapture(context.Background())`；`out, err := tool.Handle(ctx, `{"command":"make"}`)`。
3. 断言（给模型的）：`out.(string)` 解出的 `stdout` **不含** `progress step`、**含** `ERROR: src/api.c:42:18`；顶层有 `output_compression`。
4. 断言（给界面的）：`ToolCompletionFromContext(ctx)` 的 `Output["stdout"]` **含** `progress step` 与 `ERROR: src/api.c:42:18`；`Output["output_compression"]` 存在；
   `Output["stdout_compressed_bytes"]` 小于 `len(Output["stdout"].(string))`。
5. 断言（卡片正文）：`FormatToolStepResult(StepEvent{Kind: StepKindToolCompleted, ToolName: "shell", Input: map[string]any{"command": "make"}, Output: completion.Output}, DefaultMaxFormattedBody)`
   的正文含 `progress step`。

若第 3 条的「不含 progress step」失败（精简没生效）：先跑 `TestCompressShellOutputRoundTrip` 确认过滤器本身正常；仍不生效 → STOP 2，不要为了让测试过去改过滤器。

**Verify**：Commands 表「单测」一行 → `ok`。

### Step 3：回归

Commands 表「回归」「vet / fmt」两行全部通过。若有现有测试断言「完成输出 / 卡片正文是精简后的文本」而失败：只把断言改成原文（这正是本计划要改的行为），并在报告里列出用例名；
其它失败 → STOP 2。

### Step 4：真机核对（fake provider 驱动，无需真实模型）

读 `.claude/skills/run-forebrain/SKILL.md`「Run (agent path)」。`D=.claude/skills/run-forebrain/driver.sh`：

1. `$D reset && $D build`；`$D start shell '{"command": "make"}'`。
2. TUI 起来后、提交前，把上面 Step 2 的 `Makefile` 写进 `"${TMPDIR:-/tmp}/forebrain-run/proj/Makefile"`（注意制表符）。
3. `$D submit 'go'`，出现审批就批准；`$D wait 'progress step' 30`；`$D screen > /tmp/fb132.txt`。
4. 期望：
   - 命令结束后，卡片里仍能看到 `progress step` 行（展开卡片或按 SKILL.md 的方式看完整正文），即运行时流出的内容在结束时没有被换掉；
   - `$D db "SELECT content FROM fb_messages WHERE role='tool' ORDER BY id DESC LIMIT 1"`（给模型的那份）**不含** `progress step`；
   - `$D db "SELECT json_extract(payload_json,'$.display_body') FROM fb_session_events WHERE event_type='tool_call_completed' ORDER BY sequence DESC LIMIT 1"`（给界面的那份）**含** `progress step`。
5. `$D stop`，把第 4 条三项的实际输出贴进报告。

## Done criteria

- [ ] Step 2 新测试通过；Step 3 全部命令通过
- [ ] `git diff -- pkg/tool/shell_tool.go` 只有：`rawStdout := stdout` 一行、注释改写、`completionOutput` 的 `"stdout"` 一处替换
- [ ] `git status --short pkg/tool/shell_output.go pkg/tool/filters frontend pkg/tui pkg/gateway` 无输出
- [ ] Step 4 三项期望满足，输出贴在报告里

## STOP conditions

1. 前置 grep 不满足或摘录对不上。
2. 精简在测试里不生效，或出现与本计划无关的测试失败；某步验证连续两次修正后仍失败。
3. 发现还有别的代码依赖「完成输出里的 stdout 是精简版」（例如某处拿它回填给模型）——回报，不要顺手改。

## Maintenance notes

- 原则：**精简是模型上下文的事，界面永远显示命令真正打印的内容**。以后新增任何「给模型的变形」（脱敏、裁剪、摘要），都只作用于 `resultPayload`，不要再写进 `completionOutput`。
- 原始 stdout 本来就受引擎的头 32 KB + 尾 96 KB 预览上限约束，完成输出的体积不会因此超过现在不触发精简时的体积；超过上限的完整输出走「查看完整输出」（见原型稿）。
- 130 会在同一函数的成功路径上往 `completionOutput` 加 `shell_edit_diff`；两处改动互不影响，先后执行都可以。
