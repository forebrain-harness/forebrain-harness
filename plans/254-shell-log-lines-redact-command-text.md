# Plan 254: shell 工具的每一条日志都只带去密后的命令与原因

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 236fc32a..HEAD -- pkg/tool/shell_tool.go pkg/tool/shell_output.go pkg/tool/state.go pkg/tool/state_test.go`
> → 必须为空。不为空就把下面「Current state」里的摘录和实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（只改三条日志的参数和一个新的小函数；模型看到的结果、卡片、沙箱行为一概不变）
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `236fc32a`, 2026-11-06
- **Finding**: 2026-11-06 分支审计（238–245 实现细节），发现 2（`pre-existing`，是 plan 243 同类问题的漏网）

## Why this matters

模型执行的 shell 命令里常带内联凭据，比如：

- `curl -H "Authorization: Bearer …"`
- `GITHUB_TOKEN=ghp_… git push`
- `mysql -p…`

模型看到的输出早已经过 `Redact` 去密。plan 243 也把「输出历史保存失败」那条 ERROR 日志改成了去密后的命令。

但同一个包里还有两条日志原样记录 `req.command`：沙箱拒绝时的 WARN（「问一下能不能出沙箱跑」），
以及无人可批时的 ERROR。两条的 `reason` 字段还带着最多 512 字节的命令输出原文，
内容来自 stderr/stdout 里的拒绝行，比如带令牌的 URL。

沙箱拒绝远比保存失败常见：每次网络被拦、写到工作区外都会触发。于是令牌会落进网关日志。
K8s/Docker 部署下，这些日志还会被日志系统集中收走。

owner 对同类问题的裁决是「必须穷举所有同类问题，全部修复」。本计划把三处统一到一个函数，
再加一条扫源码的守卫测试，让以后新加的同类日志在测试里就红。

## Current state

- `pkg/tool/state.go:3013`、`:3043-3056`：去密规则与开关（**规则不改**）：

```go
const RedactionDisabledEnv = "FOREBRAIN_OUTPUT_FILTER_DISABLE_REDACTION"
…
// Redact masks high-confidence secrets in s. It is a no-op when s is empty.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, rule := range secretRules {
		s = rule.re.ReplaceAllString(s, rule.repl)
	}
	return s
}

// RedactEnabled reports whether output redaction is active for this process.
func RedactEnabled() bool {
	return !envTruthy(RedactionDisabledEnv)
}
```

- `pkg/tool/shell_output.go:611-621`：243 已改好的那一处（内联写法）：

```go
			logged := command
			if RedactEnabled() {
				logged = Redact(logged)
			}
			slog.Error("shell output history not saved; the output has no retrieval id",
				"session_id", sessionID, "command", logged, "command_bytes", len(command), "err", err)
```

- `pkg/tool/shell_tool.go:615-620`：漏网一（WARN）：

```go
			denialReason := sandboxDenialReason(decision, res, runErr)
			slog.Warn("sandbox denied shell command; asking whether it may run outside the sandbox",
				"command", req.command, "reason", denialReason, "err", runErr)
```

- `pkg/tool/shell_tool.go:742-743`：漏网二（ERROR）：

```go
		denialReason := sandboxDenialReason(decision, res, runErr)
		slog.Error("sandbox denied shell command", "command", req.command, "reason", denialReason, "err", runErr)
```

- `pkg/tool/shell_tool.go:1067-1087`：`sandboxDenialReason` 的取值依次来自：
  1. `safety.SandboxDenialDetail`，即输出里第一条带拒绝关键字的行；
  2. 否则 `runErr.Error()`；
  3. 否则 `res.Stderr`。

  最后截到 512 字节。**它也是给模型看的返回值的一部分**（`sandboxDeniedCommandError(res, denialReason)`），所以**只在日志参数处**去密，不改这个函数。

- 已全仓扫过：`pkg/tool` 里带 `"command"` 或 `"reason"` 日志键的 slog 调用**恰好这三处**。
- 仓库规则：`X_test.go` 必须与 `X.go` 同名（`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles`）。新函数放 `state.go`，它的测试和守卫放已有的 `state_test.go`（`package tool`）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'LogLines|RedactForLog' -count=1 -v` | `PASS` |
| Package | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | `ok` |
| House rule | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |

注意 `-run` 是 Go 正则，`|` 就是「或」，**不要**写成 `\|`：`\|` 是字面竖线，会匹配不到任何测试还 exit 0。

## Scope

**In scope**:
- `pkg/tool/state.go`：新增 `redactForLog`
- `pkg/tool/shell_output.go`：把 243 的内联写法换成 `redactForLog`
- `pkg/tool/shell_tool.go`：两条沙箱拒绝日志
- `pkg/tool/state_test.go`：单测与守卫

**Out of scope**:
- `secretRules` 和 `Redact` 本身。扩充规则是另一件事。
- `sandboxDenialReason` 和给模型的返回值，模型看到什么不变。
- 其他包的日志。守卫只管 `pkg/tool`，这是命令文本唯一出现在日志里的包；别的包要扩，另立计划。
- 日志级别和日志消息文字。

## Steps

### Step 1: 一个函数决定日志里能出现什么

在 `state.go` 的 `RedactEnabled` 之后加：

```go
// redactForLog is what a log line may carry of text a command, its output or
// its denial produced: the same masking the model-facing output gets, unless
// the operator turned redaction off. Every slog attribute in this package that
// carries such text goes through it (TestToolLogLinesRedactCommandText).
func redactForLog(s string) string {
	if !RedactEnabled() {
		return s
	}
	return Redact(s)
}
```

**Verify**: `go build ./...` → exit 0

### Step 2: 三处日志改用它

- `shell_output.go`：删掉 `logged := command` 和它下面的 `if` 块，改成

```go
			slog.Error("shell output history not saved; the output has no retrieval id",
				"session_id", sessionID, "command", redactForLog(command), "command_bytes", len(command), "err", err)
```

- `shell_tool.go` 的 WARN：

```go
			slog.Warn("sandbox denied shell command; asking whether it may run outside the sandbox",
				"command", redactForLog(req.command), "command_bytes", len(req.command),
				"reason", redactForLog(denialReason), "err", runErr)
```

- `shell_tool.go` 的 ERROR：

```go
		slog.Error("sandbox denied shell command",
			"command", redactForLog(req.command), "command_bytes", len(req.command),
			"reason", redactForLog(denialReason), "err", runErr)
```

`denialReason` 变量本身不变，下一行 `sandboxDeniedCommandError(res, denialReason)` 继续用原值。

**Verify**:
- `go build ./...` → exit 0
- `grep -nE '"(command|reason)", (req\.command|command|denialReason|logged)\b' pkg/tool/*.go` → 无输出

### Step 3: 单测——开关两态

在 `state_test.go` 末尾加 `TestRedactForLogFollowsTheRedactionSwitch`：

- 输入 `curl -H "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123" https://x`；
- 默认（`t.Setenv(RedactionDisabledEnv, "")`）下，输出不含 `abcdefghijklmnopqrstuvwxyz0123`，且含 `RedactedMarker`；
- `t.Setenv(RedactionDisabledEnv, "1")` 下，输出与输入逐字节相同。

如果 `getenv` 不是直接读 `os.Getenv`（`envTruthy` 用的是包级 `getenv`），以测试能切换开关为准。
先在 `state.go` 里找 `getenv` 的定义，照现有测试切换它的方式写。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestRedactForLogFollowsTheRedactionSwitch' -count=1 -v` → `PASS`

### Step 4: 守卫——源码里每个带命令文本的日志键都经过 `redactForLog`

在 `state_test.go` 末尾加 `TestToolLogLinesRedactCommandText`，用 `go/parser` + `go/ast` 扫本包所有
**非测试** `.go` 文件（`filepath.Glob("*.go")` 后跳过 `_test.go`；测试运行时的工作目录就是包目录）：

- 找所有 `*ast.CallExpr`，其 `Fun` 是 `*ast.SelectorExpr`，`X` 是名为 `slog` 的 `*ast.Ident`，`Sel.Name` ∈ {`Debug`, `Info`, `Warn`, `Error`}；
- 从第 2 个参数起两两成对看作「键、值」。若键是 `*ast.BasicLit`（`token.STRING`），去引号后 ∈ {`command`, `reason`}，则值必须是 `*ast.CallExpr` 且 `Fun` 为名为 `redactForLog` 的 `*ast.Ident`；
- 不满足就 `t.Errorf("%s: slog attribute %q carries command text without redactForLog", fset.Position(lit.Pos()), key)`；
- 额外断言**至少找到 3 处**符合要求的键。防止扫描本身坏掉（比如路径错了、一个文件都没扫到）时假绿。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestToolLogLinesRedactCommandText' -count=1 -v` → `PASS`

**负对照（必须做，做完复原）**：临时把 `shell_tool.go` 的 ERROR 那行改回 `"command", req.command`，
同一命令应 FAIL 并点出 `shell_tool.go:<行号>`。观察后复原。

### Step 5: 全量回归

**Verify**：
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `go vet ./...` → exit 0

## Test plan

- 新增 2 个测试：开关两态的单测，以及扫源码的守卫。守卫带负对照。
- 保持绿：`pkg/tool` 全包，尤其 `shell_tool_test.go` 里的沙箱拒绝、重试审批用例。它们断言的是返回给模型的文本，本计划不动。

## Done criteria

ALL must hold:

- [ ] `go build ./...`、`go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `grep -nE '"(command|reason)", (req\.command|command|denialReason|logged)\b' pkg/tool/*.go` → 无输出
- [ ] `grep -c 'redactForLog(' pkg/tool/shell_tool.go` → `4`；`grep -c 'redactForLog(' pkg/tool/shell_output.go` → `1`
- [ ] 守卫负对照已观察到红并复原
- [ ] `git status --porcelain` 只列出 In scope 的文件，外加 `plans/README.md`
- [ ] `plans/README.md` 的 254 行已更新

## STOP conditions

- 守卫扫出第四处、本计划没列的带 `command`/`reason` 键的 slog 调用：报告它的位置和值的来源。如果它确实是命令文本，就同法改掉并在报告里写明；如果是固定枚举，不要为了让守卫过而改键名，报告后等裁决。
- `RedactEnabled` 的开关在测试里无法切换（`getenv` 被别处缓存）：报告，不要为测试改开关的实现。

## Maintenance notes

- 规则：**`pkg/tool` 里任何 slog 属性，只要它的值可能来自命令文本、命令输出或拒绝原因，就必须经过 `redactForLog`**。守卫只认 `command`、`reason` 两个键名。新增的同类日志要么沿用这两个键名，要么把新键名加进守卫的集合。
- `command_bytes` 留着原始长度，是为了排查时还能看出命令有多长。它不泄露内容。
- 操作员用 `FOREBRAIN_OUTPUT_FILTER_DISABLE_REDACTION` 关掉去密时，日志按其选择原样记录，与给模型的输出口径一致。
