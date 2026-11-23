# Plan 323: shell 超时不再盲失败——默认 120s、超时返回部分输出与超时事实，首次成功率提升

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**:
> `git diff --stat 8e936d7b..HEAD -- pkg/tool/shell_tool.go pkg/tool/shell_tool_test.go`
> 以及 `git status --porcelain -- pkg/tool/shell_tool.go pkg/tool/shell_tool_test.go`。
> 若 in-scope 文件有改动，把下面 "Current state" 的摘录与活体代码逐段比对；
> **内容不一致即 STOP**（仅行号漂移不算）。

## Status

- **Priority**: P1（owner 报障：shell 命令经常 `command timed out`，要求必须提高首次成功率）
- **Effort**: S（单文件生产改动 + 测试）
- **Risk**: LOW（不改取消语义、不改 safety 层、不改任何 surface 代码）
- **Depends on**: none
- **Category**: bug / dx
- **Planned at**: commit `8e936d7b`（dev），2026-11-18

## Why this matters

owner 截图：`df … ; docker system df`、`docker ps` 在默认 10s 下连续超时；模型只看到
`tool shell: command timed out` 一行——不知道超时值是多少（只能瞎猜 "Timed out at 10s
default"）、看不到命令已经打印的部分输出、没有任何下一步指引，于是盲目加大 timeout 重试，
再超时一次又浪费 60s。两个根因都在 `pkg/tool/shell_tool.go`：

1. **默认 10s 太短**：docker/du/构建类命令在负载机器上 routinely 超过 10s，首次必败。
2. **超时把已有信息全部丢弃**：safety 层超时时明明保留了部分 stdout/stderr（exit 124 +
   `TimeoutError{Result: res}`），但 tool 层 `return "", runErr` 把整份结果扔掉，模型拿到
   零信息。

本计划把默认提到 120s（Claude Code Bash 工具同款默认），并让超时走**正常失败结果**通道：
模型拿到部分输出 + `timed_out` + 生效 `timeout_ms` + exit 124，卡片照常显示
"Failed to run" 与部分输出。首次成功率由默认值直接提升；即使仍超时，模型也能区分
「慢（输出在增长，加大 timeout 重试）」与「卡死（零输出，去查 daemon）」，不再盲重试。

**诚实边界**：截图里 60s 仍超时的那条是 docker daemon 因磁盘 98% 满而挂死（环境因素，
另一会话已在清理）。任何超时值都救不了挂死的 daemon；本计划让模型**第一时间看出**是卡死
而不是慢，这本身就是首次成功率的另一半。

## Current state（全部经 advisor 亲自读码 + 真实复现核实）

### 复现证据（advisor 已跑，executor 在 Step 1 会再跑一次）

隔离程序（`/tmp/fb-shell-timeout-repro`，`go run -tags fts5`，真实
`tool.NewShellTool(...).Handle` 与 `safety.Manager.RunCommand` 路径）：

- tool 层：`{"command":"echo PARTIAL_OUTPUT_LINE; sleep 30","timeout_ms":1000}`
  → `elapsed=1.017s err=command timed out out=""`（**部分输出全丢**）
- safety 层：同命令 `Timeout: time.Second`
  → `err=command timed out exit=124 stdout="PARTIAL_OUTPUT_LINE\n"`（**部分输出保留**）

### 因果链（每环都已对照源码核实）

1. `pkg/tool/shell_tool.go:85` 默认值；`:76` schema、`:83` description 都写 10000。
2. `pkg/tool/shell_tool.go:494-497` 选 timeout（`timeoutMs>0` 覆盖，无上限）。
3. safety 层超时保留部分输出并给 exit 124：
   - `pkg/safety/manager.go:554-556`（host 路径 `runHostCommand`）
   - `pkg/safety/backend_darwin.go:55-57`（seatbelt）、`backend_linux.go:55`、`backend_windows.go:899`
   - `TimeoutError` 定义 `pkg/safety/sandbox_config.go:211-216`，`Unwrap() → context.DeadlineExceeded`
   - `Manager.RunCommand`（`manager.go:257` 起）把 backend 的 `res` 经
     `finalizeCommandResult`（补 AggregatedOutput/ExecutionTiming）后**与错误一起返回**——
     即 shell_tool.go:613 拿到的 `res` 在超时时已含部分输出与 exit 124。
4. `pkg/tool/shell_tool.go:613-616` 与 `:648-651`（escalation 重试路径）：
   `errors.Is(runErr, context.DeadlineExceeded)` 命中 TimeoutError 的 Unwrap →
   `return "", runErr`，丢弃 res、跳过 payload 组装、跳过 CaptureToolCompletion。
5. `pkg/run/orchestration_llm.go:2309` `fmt.Errorf("tool %s: %w", name, err)`；
   `pkg/agent/agent.go:421` err≠nil 时 Result 丢弃 → 模型只见 `tool shell: command timed out`。
6. orchestration 的 run 终止判定基于**父 ctx**（`orchestration_llm.go:697` 等查 `ctx.Err()`），
   tool 级超时不会终止 run（截图里 run 继续，相符）。

### 关键摘录（in-scope 文件，逐字）

`pkg/tool/shell_tool.go:76`（ShellInput 字段）：

```go
	TimeoutMs             int                              `json:"timeout_ms,omitempty" jsonschema:"description=Timeout in milliseconds (default 10000)."`
```

`pkg/tool/shell_tool.go:83-85`：

```go
const shellToolDescription = "Run a command - builds, tests, formatters, git - not file edits (use edit_file/write_file). The one exception is apply_patch, the only command in that shell call: a heredoc (apply_patch <<'PATCH' ... PATCH) with its terminator as the final nonblank line; run anything else in separate shell calls. Pass a description; timeout_ms defaults to 10000."

const defaultShellCommandTimeout = 10 * time.Second
```

`pkg/tool/shell_tool.go:494-497`：

```go
	timeout := defaultShellCommandTimeout
	if req.timeoutMs > 0 {
		timeout = time.Duration(req.timeoutMs) * time.Millisecond
	}
```

`pkg/tool/shell_tool.go:613-617`（第一处早退 + denial 判定）：

```go
	res, decision, runErr := manager.RunCommand(ctx, cfg, commandRequest(effectiveSandboxPermissions))
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return "", runErr
	}
	denied := decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)
```

`pkg/tool/shell_tool.go:647-653`（escalation 重试，第二处早退）：

```go
	if retry {
		res, decision, runErr = manager.RunCommand(ctx, cfg, commandRequest(safety.SandboxPermissionsRequireEscalated))
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return "", runErr
		}
		denied = decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)
	}
```

`pkg/tool/shell_tool.go:683-687`（模型 payload 基座）：

```go
	resultPayload := map[string]any{
		"stdout":    stdout,
		"stderr":    stderr,
		"exit_code": exitCode,
	}
```

`pkg/tool/shell_tool.go:742-752`（卡片完成捕获；Error 经
`pkg/run/orchestration_llm.go:2557 toolCompletedPayload` 的 `cp.Error → errText` 成为
StepEvent.Error，三个 surface 的 "Failed to run" 头都从它来——
`pkg/tool/format.go:2699 stepStatusLabel` 只看 `evt.Error != ""`，**不看 exit_code**）：

```go
	captureCompletion := func() {
		CaptureToolCompletion(ctx, ToolCompletionPayload{
			Output: appendToolExecutionMetadata(ctx, st, completionOutput, rt, safety.ToolKindShell),
			Error: func() string {
				if runErr == nil {
					return ""
				}
				return runErr.Error()
			}(),
		})
	}
```

### 必须保持的既有语义（已核实，改动不得破坏）

- **父 ctx 取消必须原样传播**：`TestShellToolCancellationPropagatesToOrchestration`
  （`pkg/tool/shell_tool_test.go:840`）钉住 `context.Canceled` 早退。backend 对父取消返回
  裸 `context.Canceled`（不是 TimeoutError），`errors.As` 区分两者。
- **exit 124 已是失败语义**：`shellExitIsFailure`（`pkg/tool/shell_diff.go`）对 124 返回
  true（grep/find 类 `>=2` 也 true）→ `editDiff.finish` 在 runErr=nil 时也不会给超时命令算 diff。
- **卡片失败头**：只要 `CaptureToolCompletion` 的 Error 非空，TUI live/replay 与 web 同源显示
  "Failed to run"（`stepStatusLabel` 只读 evt.Error）。**无需改任何 surface 代码。**
- **卡片正文自动显示部分输出**：`formatShellStep`（`pkg/tool/format.go:792`）从
  completionOutput 的 stdout/stderr 渲染——超时落入正常 payload 通道后部分输出自动可见。
- `safety.IsLikelySandboxDenied`（pkg/safety）对 exit 124 一般返回 false，但超时命令的
  stderr 可能含 "Operation not permitted" 等字样——**超时必须显式跳过 denial/escalation 分支**
  （Step 2 的 `!timedOut` 守卫），不得对超时发起沙箱升级审批。
- `Manager.RunCommand` 自带 `req.Timeout <= 0 → 10s` 兜底（`manager.go:267-269`）：
  shell 工具永远传 >0，用户 `!` shell 传 1h（`pkg/tui/chat_session.go:2826`），**无活体调用方
  依赖该兜底，不改**（out of scope）。
- 模型可见输出字段判据（owner 2026-11-18 常设裁决）：只发帮助 LLM 判断/决策的数据；
  语义与推导在 tool description 里教一次，不在每次调用里重复。故 payload 只加
  `timed_out` 与生效 `timeout_ms` 两个事实字段，指引文案进 description。
- 全仓文案禁 `·` 连接符（owner 2026-11-16 裁决）；新文案用空格或 " - "（与现有 description 同风格）。

### 仓库惯例

- 测试 exemplar：`TestShellToolBuiltinPolicyRunsAfterApproval`
  （`pkg/tool/shell_tool_test.go:769`）——`NewState(t.TempDir())` +
  `NewShellTool(st, &AgentToolRuntime{Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess}})`
  + `tool.Handle(ctx, json)` + `json.Unmarshal` payload 断言。
- completion 捕获 exemplar：`pkg/tool/file_tools_test.go:396-409`——
  `ctx := WithToolCompletionCapture(context.Background())` 后
  `ToolCompletionFromContext(ctx)` 读 `completion.Output` / `completion.Error`。
- 注释风格：解释「为什么」的整段英文注释，见 shell_tool.go:669-674（压缩只给模型、卡片留原文）。
- 无 golangci-lint；门禁是 `gofmt -l` + `go vet`（见 Commands）。
- 无 golden 测试钉 shellToolDescription 文案（grep 核实）；docs 站点无 timeout_ms 默认值文案（grep 核实）。

## Commands you will need

先设置环境（每个新 shell 都要；`pkg/pdf` 的 cgo 需要它）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 聚焦测试（新 + 回归） | 见下方代码块 | 全部 `--- PASS`，exit 0 |
| 包级测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | `ok`，exit 0 |
| 邻接包（payload 消费方） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok`，exit 0 |
| 格式 | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |

聚焦测试命令（**不要写进 markdown 表格里**，`|` 会被表格吞掉；裸 `|` 是 go test 的正则或）：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool \
  -run 'TestShellTimeoutReturnsPartialOutputAndTimeoutFacts|TestShellTimeoutReportsFailureOnTheCard|TestShellDefaultTimeoutIsTwoMinutes|TestShellToolCancellationPropagatesToOrchestration' \
  -count=1 -v
```

预期：4 个测试全部 `--- PASS`（3 新 + 1 既有回归）。`-v` 用来核对确实跑了这些测试
（`[no tests to run]` 是假绿，见 STOP）。

注意：本机磁盘曾长期接近满；若 go test 报 `errno=28`（no space left），是环境问题，
STOP 报告，不要删任何东西。全量套件是 owner 合并门禁，不在本计划内强制。

## Scope

**In scope**（只许改这些）：
- `pkg/tool/shell_tool.go`
- `pkg/tool/shell_tool_test.go`

**Out of scope**（看着相关也不许动）：
- `pkg/safety/*`——TimeoutError/exit 124/部分输出保留机制已正确，不改；
  `manager.go:267-269` 的 10s 兜底无活体依赖，不改。
- `pkg/run/orchestration_llm.go`、`pkg/agent/*`——错误传播与 run 终止语义不改。
- `pkg/tui/*`、`pkg/gateway/*`、`frontend/*`——卡片失败头与正文由引擎同源渲染，
  本计划不需要任何 surface 改动；若发现需要，STOP。
- `pkg/config/*`——不加配置项（见 Maintenance notes 的延期理由）。
- `timeout_ms` 上限——今天无上限，本计划不加（见 Maintenance notes）。
- 其他工具的超时（web_fetch、MCP 等）——不同契约，不碰。

## Git workflow

- owner 常设规则：**默认禁止任何 git commit**（owner 手动审阅提交）；隔离方式跟随 owner
  当次指令（in-tree 零 commit，或 worktree + exec 分支）。未获明确指令时在主树工作、零 commit。
- 若 owner 指示 worktree：建在 `.worktrees/323-shell-timeout-first-run-success`，
  分支同名；合并回主树后 worktree 与分支必须删除（owner 2026-11-17 常设规则）。
- 永不 push。

## Steps

### Step 1: 先跑复现，确认缺陷仍在

在仓库根目录用一次性 Go 程序复现（不要往仓库里加文件）。把下面两个文件放进
`$(mktemp -d)/repro`，`go.mod` 的 replace 指向本仓库绝对路径：

`go.mod`：

```
module fb-shell-timeout-repro

go 1.26

require github.com/forebrain-harness/forebrain-harness v0.0.0

replace github.com/forebrain-harness/forebrain-harness => <本仓库绝对路径>
```

`main.go`：

```go
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

func main() {
	st := tool.NewState(os.TempDir())
	t, err := tool.NewShellTool(st, &tool.AgentToolRuntime{
		Cfg: &appcfg.Root{SandboxMode: appcfg.SandboxModeDangerFullAccess},
	})
	if err != nil {
		panic(err)
	}
	start := time.Now()
	out, err := t.Handle(context.Background(), `{"command":"echo PARTIAL_OUTPUT_LINE; sleep 30","timeout_ms":1000}`)
	fmt.Printf("elapsed=%s\nerr=%v\nout=%#v\n", time.Since(start).Round(time.Millisecond), err, out)
}
```

运行：`cd <repro dir> && go mod tidy && CGO_ENABLED=1 go run -tags fts5 .`
（PKG_CONFIG_PATH 已 export；链接器 `ld: warning` 可忽略）。

**Verify（修复前预期）**：输出含 `err=command timed out` 且 `out=""`。
若输出不是这样（例如 out 已含 PARTIAL_OUTPUT_LINE），说明缺陷已被别的改动修掉——STOP 报告。
保留这个程序，Step 4 修复后要再跑一次。

### Step 2: 超时落入正常结果通道（pkg/tool/shell_tool.go）

2a. 第一处早退（当前 :613-617）改为区分「命令自身超时」与「父 ctx 取消」：

```go
	res, decision, runErr := manager.RunCommand(ctx, cfg, commandRequest(effectiveSandboxPermissions))
	// The command's own deadline is a failed command, not a cancelled call:
	// the backend already captured its partial output and exit 124, and the
	// model needs both to tell "slow" from "wedged". Only the caller's
	// cancellation still terminates the call here.
	var timeoutErr *safety.TimeoutError
	timedOut := errors.As(runErr, &timeoutErr)
	if timedOut {
		runErr = nil
	} else if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return "", runErr
	}
	denied := !timedOut && decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)
```

要点：
- **保留 `res` 原样**——`RunCommand` 返回的 res 已经过 finalize（含部分输出、exit 124、
  ExecutionTiming），不要用 `timeoutErr.Result` 覆盖它（那份缺 timing）。
- `timedOut` 变量后面 Step 2c/2d 还要用，声明在函数这一层（两处早退之间无新作用域）。

2b. 第二处早退（escalation 重试，当前 :647-653）同样处理：

```go
	if retry {
		res, decision, runErr = manager.RunCommand(ctx, cfg, commandRequest(safety.SandboxPermissionsRequireEscalated))
		timeoutErr = nil
		timedOut = errors.As(runErr, &timeoutErr)
		if timedOut {
			runErr = nil
		} else if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return "", runErr
		}
		denied = !timedOut && decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)
	}
```

2c. 模型 payload（当前 :683-687 的 `resultPayload` 之后、`retrieveID` 之前）加两个事实字段：

```go
	if timedOut {
		resultPayload["timed_out"] = true
		resultPayload["timeout_ms"] = int(timeout / time.Millisecond)
	}
```

不加任何指引句子（语义在 description 教一次，owner 2026-11-18 裁决）。
exit_code 已是 124，stdout/stderr 已是部分输出，压缩/spool/retrieve_output 全部走既有机制。

2d. `captureCompletion`（当前 :742-752）的 Error 闭包加超时分支，让三个 surface 的卡片
照常 "Failed to run" 且错误行带上时长：

```go
			Error: func() string {
				if timedOut {
					return fmt.Sprintf("command timed out after %s", timeout)
				}
				if runErr == nil {
					return ""
				}
				return runErr.Error()
			}(),
```

（`fmt`/`errors`/`time` 均已在 import 块，核实过 :9/:10/:21。）

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0。

### Step 3: 默认 120s + 模型引导（同文件）

3a. `:85`：

```go
const defaultShellCommandTimeout = 120 * time.Second
```

3b. `:76` schema 描述改 `(default 120000)`：

```go
	TimeoutMs             int                              `json:"timeout_ms,omitempty" jsonschema:"description=Timeout in milliseconds (default 120000)."`
```

3c. `:83` `shellToolDescription`：**前半段逐字保留**，只把末句
`Pass a description; timeout_ms defaults to 10000.` 替换为：

```
Pass a description; timeout_ms defaults to 120000 - raise it for slow work (builds, installs, docker, du/find over large trees). A timed-out command is killed but its result keeps the partial output plus timed_out=true and the effective timeout_ms: output still growing means slow (retry with a larger timeout_ms); no output usually means blocked (investigate instead of retrying).
```

**Verify**：`grep -n "10000" pkg/tool/shell_tool.go` → 无输出；
`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0。

### Step 4: 新测试（pkg/tool/shell_tool_test.go）

结构照 `TestShellToolBuiltinPolicyRunsAfterApproval`（:769）；completion 断言照
`pkg/tool/file_tools_test.go:396-409`。三个新测试：

1. **TestShellTimeoutReturnsPartialOutputAndTimeoutFacts**——两个子用例：
   - `echo PARTIAL_OUTPUT_LINE; sleep 30`，`timeout_ms:1000`：断言 `err == nil`；
     payload `stdout` 含 `PARTIAL_OUTPUT_LINE`；`exit_code == 124`（JSON 数字解出来是
     float64，按 `payload["exit_code"].(float64) == 124` 断言）；`timed_out == true`；
     `timeout_ms == 1000`（同 float64）。
   - `sleep 30`，`timeout_ms:1000`（零输出的「卡死」形状）：断言 `err == nil`、
     `stdout == ""`、`exit_code == 124`、`timed_out == true`。
2. **TestShellTimeoutReportsFailureOnTheCard**——`ctx := WithToolCompletionCapture(context.Background())`
   跑 `echo PARTIAL_OUTPUT_LINE; sleep 30`（timeout_ms:1000），然后
   `ToolCompletionFromContext(ctx)`：断言 `completion.Error` 含
   `"command timed out after"`；`completion.Output["stdout"]` 含 `PARTIAL_OUTPUT_LINE`；
   `completion.Output["exit_code"] == 124`。这是卡片 "Failed to run" + 部分输出的同源证据。
3. **TestShellDefaultTimeoutIsTwoMinutes**——钉 `defaultShellCommandTimeout == 120*time.Second`
   且 `strings.Contains(shellToolDescription, "timeout_ms defaults to 120000")`。

每个测试约 1s（1000ms 超时 + sleep 30 被杀）。不要调大 timeout_ms 让测试变慢。

**Verify**：上面 Commands 节的聚焦测试命令 → 4 个 `--- PASS`（含既有取消回归）。

### Step 5: 复现程序复跑 + 门禁

5a. 重跑 Step 1 的复现程序。**修复后预期**：`err=<nil>`，`out` 为 JSON 字符串且含
`PARTIAL_OUTPUT_LINE`、`"exit_code":124`、`"timed_out":true`、`"timeout_ms":1000`。
跑完删除临时目录。

5b. 依次跑 Commands 表的包级测试（pkg/tool、pkg/run）、`gofmt -l`、`go vet`。

**Verify**：全部达到表中预期。任何失败先诊断再重跑；两次修不好 → STOP。

### Step 6（推荐，owner 真机验收惯例）: 真实 TUI 验收

若环境可用 run-forebrain 技能（`.claude/skills/run-forebrain/SKILL.md`，driver.sh 假 provider）：

- 用例 A（首次成功率直接证据）：shell 调用 `sleep 11; echo DONE`，**不带 timeout_ms**。
  修复前 10s 必超时；修复后应成功完成、卡片 "Ran"、stdout 含 DONE（约 11s）。
- 用例 B（超时新形状）：shell 调用 `echo PARTIAL; sleep 30`，`timeout_ms:2000`。
  卡片应显示 "Failed to run"、错误行含 "command timed out after 2s"、正文可见 PARTIAL；
  模型侧结果含 timed_out/timeout_ms/部分输出。

无该技能环境时此步可跳过，但要在交付报告里注明未做真机验收。

## Test plan

- 新增：Step 4 的 3 个测试（4 个子用例），都在 `pkg/tool/shell_tool_test.go`。
- 回归：`TestShellToolCancellationPropagatesToOrchestration`（:840）必须仍绿——
  父取消传播是本计划最不能破坏的语义。
- 既有包级：`./pkg/tool` 全绿（含 shell_card/shell_diff/format 的卡片同源测试）；
  `./pkg/run` 全绿（payload 消费方）。
- 失败路径覆盖：零输出超时（卡死形状）、有输出超时（慢形状）、卡片失败头、取消传播。
- 沙箱（seatbelt）路径与 host 路径在 tool 层共用同一段超时处理代码（backend 都返回
  TimeoutError），单元测试走 host 路径即覆盖该逻辑；seatbelt 真机形状由 Step 6 用例 B
  在默认沙箱模式下顺带覆盖（若环境有沙箱）。

## Done criteria

ALL must hold:

- [ ] Step 1 复现修复前失败形状、Step 5a 修复后成功形状（两次输出都贴进交付报告）
- [ ] 聚焦测试命令 4 个 `--- PASS`（`-v` 输出核对，非 `[no tests to run]`）
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` → 两包 `ok`
- [ ] `gofmt -l cmd pkg third_party plugins` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0
- [ ] `grep -n "10000" pkg/tool/shell_tool.go` 无输出
- [ ] `git status --porcelain` 只含 `pkg/tool/shell_tool.go`、`pkg/tool/shell_tool_test.go`
      （及 plans/README.md 若由 executor 更新索引；plans/ 已 gitignore 不会出现在 status）
- [ ] `plans/README.md` 状态行更新（除非 reviewer 维护索引）

## STOP conditions

Stop and report back (do not improvise) if:

- Current state 摘录与活体代码内容不符（漂移）。
- Step 1 复现没有产出 `err=command timed out` + `out=""`（缺陷可能已被修掉或环境异常）。
- 修改后取消回归测试失败——说明 `errors.As` 区分逻辑写错了，不得用「让测试适应实现」的方式绕过。
- 聚焦测试出现 `[no tests to run]`（正则/命名错了，不是绿）。
- 发现需要改 out-of-scope 文件（尤其任何 surface/safety/config）才能让卡片或模型侧正确。
- 出现钉住 shellToolDescription 旧文案的 golden 测试（advisor grep 未发现；若漂移后出现，
  按测试期望同步文案并留痕，不得删测试）。
- go test 报 `errno=28`（磁盘满，环境问题）——报告，不删任何东西。
- 同一验证失败两次且原因不明。

## Maintenance notes

- **默认值 120s 的取舍**：超时是上限不是等待——快命令不受影响；只有真卡死的命令会占住
  turn 最多 120s（此前 10s）。缓解：输出流式可见、Esc 随时取消（取消语义有测试钉住）、
  超时后模型拿到零输出事实会去查 daemon 而不是干等。120s 与 Claude Code Bash 默认一致。
  owner 若想要别的值，只改 `defaultShellCommandTimeout` + 两处文案 + Step 4.3 测试。
- **延期项（均非本计划目标，需 owner 裁决另立）**：
  - `timeout_ms` 无上限（模型可传任意大值）——今天如此，本计划不改；如要加 600000 上限
    （Claude Code 同款）另立计划。
  - 默认超时的配置项（appcfg）——新增 stored config 字段必须注册
    `pkg/process/config_defaults_test.go` 的 `zeroIsTheDefault`，且 appcfg 变更触发全模块
    go test 门禁（owner 2026-11-17 规则），成本与本计划不相称，延期。
  - 按命令名自适应超时（docker/du 自动加长）——魔法启发式，不可预测，**明确不做**；
    description 引导 + 120s 默认是现有机制。
- **reviewer 重点**：① 两处早退的 `errors.As` 区分与 `!timedOut` denial 守卫；
  ② `res` 未被 `timeoutErr.Result` 覆盖（timing 保留）；③ payload 只加两个事实字段
  （owner 2026-11-18 工具输出裁决）；④ 取消回归绿。
- **未来交互**：若有人给 shell 结果加新字段或改 `toolCompletedPayload` 的 cp.Error 语义，
  注意超时现在走「nil error + cp.Error 非空」组合——卡片失败头依赖后者，别把 cp.Error
  当成「工具返回了 error」的等价物。
- `Manager.RunCommand` 的 `Timeout<=0 → 10s` 兜底（manager.go:267-269）与 shell 默认值
  从此不同（120s vs 10s）：shell 永远传正值，无活体冲突；若未来有新调用方传 0，
  记得它拿到的是 10s 而不是 shell 的 120s。

## 执行记录（2026-11-18，advisor 自执行，in-tree 零 commit，待 owner 审阅）

- 漂移核对：HEAD == 基线 `8e936d7b`，in-scope 文件干净。
- Step 1 修复前复现：`elapsed=1.017s err=command timed out out=""`（缺陷形状确认）。
- Step 2/3 按计划落地；gofmt 重排了 retry 块缩进（替换时少一级 tab， brace 结构不变，
  gofmt -w 修正后语义不变）与 struct tag 列对齐。
- Step 4 三个新测试（4 子用例）+ 取消回归：聚焦 `-v` 全 `--- PASS`。
- Step 5a 修复后复现：`err=<nil>`，
  `out={"exit_code":124,"stderr":"","stdout":"PARTIAL_OUTPUT_LINE\n","timed_out":true,"timeout_ms":1000}`。
- 门禁：pkg/tool ok 13.2s；pkg/run 首跑 `TestRosterStopReachesEveryExecutionOfTheSubagent`
  因 `t.TempDir` 清理竞态 FAIL（directory not empty）——单测 `-count=3` 全 PASS、
  整包复跑 ok 43.6s，定性既有 flaky，与本改动无关（改动仅 pkg/tool）；
  `gofmt -l` 空、`go vet -tags fts5 ./...` exit 0。
- Step 6 真机（run-forebrain driver，隔离实例 fb-323/8763，二进制构建于 ac 的
  pkg/tui WIP 落盘之前）：
  - A：`start shell 'sleep 11; echo DONE'`（无 timeout_ms）→ `● Ran sleep 11; echo DONE 11s └ DONE`
    ——修复前 10s 必败的命令首次成功。
  - B：`start shell '{"command":"echo PARTIAL; sleep 30","timeout_ms":2000}'` →
    `● Failed to run echo PARTIAL; sleep 30 2.0s └ command timed out after 2s`；
    DB 模型侧 tool 结果 `{"exit_code":124,"stderr":"","stdout":"PARTIAL\n","timed_out":true,...}`。
  - 紧凑卡 `└` 行显示错误（与既有失败命令行为一致）；部分输出在卡片 body 数据
    （completion.Output["stdout"]，单测钉住）与模型结果。展开交互属既有 viewport
    机制，本计划未改。
- 交付 diff：`pkg/tool/shell_tool.go` +26/−7、`pkg/tool/shell_tool_test.go` +100，
  无 scope 外文件。复现临时目录已删。