# Plan 372: process 卡片头部必须点名它在执行的命令（或进程号）

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving on. If anything in the
> "STOP conditions" section occurs, stop and report — do not improvise.
>
> **Drift check (run first)**:
> `git diff --stat e1abd2f4..HEAD -- pkg/tool/header_parts.go pkg/tool/display_header_test.go`
> 两个 in-scope 文件若在基线 `e1abd2f4` 之后被改过，先逐段比对下文摘录；内容不符 → STOP。
> 行号漂移不算漂移，以内容为准。

## Status

- **Priority**: P1（信息缺陷：活卡与完成卡头部只有 "Calling/Called process"，用户与复盘者都不知道在执行什么；owner 报障截图 "Calling process 1m 47s"）
- **Effort**: S
- **Risk**: LOW（纯显示层一个 switch 分支 + 测试；不改事件、不改 payload、不改渲染表面）
- **Depends on**: none（与 370、371 互不依赖）
- **Category**: dx
- **Planned at**: commit `e1abd2f4`, 2026-11-23

## Why this matters

owner 报障：process 的消息卡（活卡 "Calling process 1m 47s"、完成卡 "Called process 3m24s"）
不显示命令，用户不知道在执行什么；而 shell 卡头部直接就是命令行，对照之下体验差距明显。

根因（advisor 亲读确认）：卡片头部措辞由仓内 `pkg/tool/header_parts.go` 的
`DisplayHeaderParts`（:25）统一产出，TUI 与 web 共用（`DisplayParts` :505 返回
`(action, target, suffix)` 三元组）。shell 有专属分支（:124-141）把 **target 设为命令本身**；
process 没有任何分支，落入 default（:424-444）：动词 `Calling/Called/Failed to call` +
target = 工具名、suffix 空 ⇒ 头部只有 "Calling process"。命令其实一直在手上：
started/completed 事件的 `Meta.Input` 携带工具入参（`BuildToolMeta` 在 `format.go:247-249`
把 `evt.Input` 原样放进 `meta.Input`），start 的入参含 `command`，wait/log/kill 含 `id`。

修法因此是一个显示层分支：按 `input.action` 组装动词与 target。不碰事件发射、不碰 payload、
不碰任何渲染表面——一处修改，TUI 与 web 同时受益。

## Current state（advisor 亲读，基线 `e1abd2f4`）

- `pkg/tool/header_parts.go` — 卡片头部措辞的唯一来源。
  - shell 参照分支（:124-141，逐字）：
    ```go
    	case lower == "shell":
    		if isFailed {
    			action = "Failed to " + FailedActionPhrase(lower)
    		} else if isRunning || isPending {
    			action = "Running"
    		} else {
    			action = "Ran"
    		}
    		target = InputString(meta, "command")
    		if target == "" {
    			// The summary joins its command and its details with a space, so
    			// the invocation is the only place that still holds the command
    			// alone. A row that kept neither falls back to the summary.
    			target = strings.TrimSpace(meta.Invocation)
    		}
    		if target == "" {
    			target = extractShellCommand(summary)
    		}
    ```
  - process 今天落入的 default 分支（:424-444，逐字）：
    ```go
    	default:
    		// A tool this build has no words for — in practice a row another
    		// agent recorded, brought in by /migrate — is named by what happened
    		// to it and by its own name: a card head reads as a sentence the
    		// surface can word ("已调用 SendMessage") instead of a bare
    		// identifier, and the name itself is never translated, because it
    		// is another tool's identity, not one of ours.
    		switch {
    		case isFailed:
    			action = "Failed to call"
    		case isRunning || isPending:
    			action = "Calling"
    		default:
    			action = "Called"
    		}
    		if name := strings.TrimSpace(title); name != "" {
    			target = name
    		} else {
    			target = defaultToolInvocationTarget(title, meta)
    		}
    	}
    ```
  - 共享尾部（:455-460）给终态追加 duration；`suffix` 由共享尾部拼在 target 之后。
  - 可用辅助：`InputString(meta, key)`（同文件 :132/:152/:164 在用）、
    `intFromAny`（`format.go:256` 在用）、`firstString`。
- `pkg/tool/format.go:239-249` — `BuildToolMeta` 把 `evt.Input` 原样放进 `meta.Input`
  （started 与 completed 事件都携带）⇒ 头部能读到 `action`/`command`/`id`/`wait`。
- process 工具的入参形状（`pkg/tool/process_tool.go:27-38`）：`action` ∈
  start/log/wait/kill/list；`command` 仅 start；`id` 为 JSON 数字（wait/log/kill）；
  `wait` 为 ready 模式（start 可选）。完成 payload 也带 `id`（数字）。
- 测试先例：`pkg/tool/display_header_test.go:57`
  （`require.Equal(t, "Exited plan mode", header(frame("exit_plan_mode", ToolMeta{Status: "completed"})))`）；
  本计划改用 `DisplayParts(f, cwd)` 的三元组断言（:505 签名
  `func DisplayParts(f DisplayFrame, cwd string) (action, target, suffix string)`），
  避免钉死表面拼接格式。

仓库惯例：注释写 WHY 一行；提交 conventional（`fix(tool): …`）；worktree
`.worktrees/<slug>` 同名分支、不 push、owner/reviewer ff-only 落 dev。

## Commands you will need

前置：`third_party/pdfium/$(go env GOOS)_$(go env GOARCH)/` 须存在（缺则 `make pdfium`，需网络；失败 → STOP）。

| Purpose   | Command | Expected on success |
|-----------|---------|---------------------|
| Build | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 目标测试 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestDisplay|TestHeader|TestProcess' -count=1` | `ok …/pkg/tool` |
| 整包测试 | 同上去掉 `-run` | `ok …/pkg/tool` |
| Vet | `go vet -tags fts5 ./pkg/tool/` | exit 0 |
| Fmt | `gofmt -l pkg/tool` | 无输出 |

## Scope

**In scope**:
- `pkg/tool/header_parts.go`（新增 `case lower == "process":` 分支）
- `pkg/tool/display_header_test.go`（新增头部三元组测试；若既有断言钉死了 process 的旧裸头部措辞，同步更新这些断言——它们编码的正是被报障的行为）

**Out of scope**（看起来相关但不要碰）:
- `pkg/tool/format.go` 的 `toolInvocationLabel` / `SummarizeToolStep` — 转录摘要今天给 process 的是通用入参 JSON（`process {"action":"start",…}`），丑但不缺信息；紧凑化另立项（见 Maintenance notes）。
- `pkg/tui/*`、`frontend/` — 头部三元组的消费者，零改动。
- 事件发射侧（`pkg/run`、`pkg/event`）— `meta.Input` 已携带所需事实。
- process 的 payload 形状（370 的领地）与活卡输出流（371 的领地）。

## Git workflow

- Worktree／分支：`.worktrees/process-card-header-command`（同名分支），BASE = `e1abd2f4`。
- 提交：一次逻辑提交，信息形如 `fix(tool): name the command in the process card header`。
- 不 push、不开 PR；合并由 owner/reviewer ff-only 落 dev。

## Steps

### Step 1: 先摸清既有断言

**Verify**: `grep -rn 'Calling process\|Called process' pkg/ --include='*.go'`
→ 记录命中行。若命中在**测试**里且断言的是 process 工具的裸头部（而非 default 分支用别的工具名
的测试），这些断言属于本计划 in-scope 的更新对象（Step 3 一并改）；若命中在**生产代码**里 →
STOP 并报告（说明还有第二处头部来源）。

### Step 2: 新增 process 头部分支

在 `DisplayHeaderParts` 的 switch 中、`// --- Shell ---` 块之后（任何位置皆可，但必须在
`default:` 之前）加入：

```go
	// --- Process ---
	// The header names what the call acts on: the command a start runs, the
	// process id a wait/log/kill drives, the registry a list reads. A bare
	// "Calling process" left the user watching a timer for minutes.
	case lower == "process":
		act := strings.ToLower(strings.TrimSpace(InputString(meta, "action")))
		id := intFromAny(meta.Input["id"])
		if id <= 0 {
			id = intFromAny(meta.Output["id"])
		}
		proc := "process"
		if id > 0 {
			proc = fmt.Sprintf("process #%d", id)
		}
		switch act {
		case "start":
			if isFailed {
				action = "Failed to start"
			} else if isRunning || isPending {
				action = "Starting"
			} else {
				action = "Started"
			}
			target = InputString(meta, "command")
			if target == "" {
				target = strings.TrimSpace(meta.Invocation)
			}
			if w := strings.TrimSpace(InputString(meta, "wait")); w != "" {
				suffix = "wait " + w
			}
		case "wait":
			if isFailed {
				action = "Failed to wait for"
			} else if isRunning || isPending {
				action = "Waiting for"
			} else {
				action = "Waited for"
			}
			target = proc
		case "log":
			if isFailed {
				action = "Failed to read"
			} else if isRunning || isPending {
				action = "Reading"
			} else {
				action = "Read"
			}
			target = proc + " log"
		case "kill":
			if isFailed {
				action = "Failed to kill"
			} else if isRunning || isPending {
				action = "Killing"
			} else {
				action = "Killed"
			}
			target = proc
		case "list":
			if isFailed {
				action = "Failed to list"
			} else if isRunning || isPending {
				action = "Listing"
			} else {
				action = "Listed"
			}
			target = "processes"
		default:
			// An action this build does not name keeps the generic wording
			// rather than guessing a verb.
			if isFailed {
				action = "Failed to call"
			} else if isRunning || isPending {
				action = "Calling"
			} else {
				action = "Called"
			}
			target = proc
		}
```

要点：
- `id` 是 JSON 数字，`InputString` 读不到 ⇒ 用 `intFromAny`（先 Input 后 Output，活卡只有 Input、完成卡两者皆有）。
- start 的 target 缺失时**不**回退到入参 JSON（那正是噪音）；回退 `meta.Invocation` 与 shell 同款即可，仍空则 target 留空（头部只剩动词，好过一行 JSON）。
- 动词表与本计划测试一一对应；改动词即改测试，二者同提交。

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0。

### Step 3: 测试（三元组断言）

在 `pkg/tool/display_header_test.go` 新增（结构照抄该文件既有的 `frame(...)` 构造，
断言改用 `DisplayParts`）：

```go
func TestDisplayPartsNamesTheProcessCommandAndID(t *testing.T) {
	startRunning := DisplayFrame{Title: "process", Meta: ToolMeta{Status: "running", Input: map[string]any{
		"action": "start", "command": "go test ./pkg/tool", "wait": "log",
	}}}
	action, target, suffix := DisplayParts(startRunning, t.TempDir())
	require.Equal(t, "Starting", action)
	require.Equal(t, "go test ./pkg/tool", target)
	require.Equal(t, "wait log", suffix)

	startDone := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Input: map[string]any{
		"action": "start", "command": "go test ./pkg/tool",
	}}}
	action, target, _ = DisplayParts(startDone, t.TempDir())
	require.Equal(t, "Started", action)
	require.Equal(t, "go test ./pkg/tool", target)

	waitRunning := DisplayFrame{Title: "process", Meta: ToolMeta{Status: "running", Input: map[string]any{
		"action": "wait", "id": float64(7),
	}}}
	action, target, _ = DisplayParts(waitRunning, t.TempDir())
	require.Equal(t, "Waiting for", action)
	require.Equal(t, "process #7", target)

	killDone := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Output: map[string]any{
		"id": float64(7), "killed": true,
	}}}
	action, target, _ = DisplayParts(killDone, t.TempDir())
	require.Equal(t, "Killed", action)
	require.Equal(t, "process #7", target)

	listRunning := DisplayFrame{Title: "process", Meta: ToolMeta{Status: "running", Input: map[string]any{
		"action": "list",
	}}}
	action, target, _ = DisplayParts(listRunning, t.TempDir())
	require.Equal(t, "Listing", action)
	require.Equal(t, "processes", target)
}
```

（若该文件尚未 import `require` 或 `DisplayParts` 的包内调用方式与既有用例不同，以文件内既有用例为准调整调用形态，**断言值不变**。）
同时按 Step 1 的命中清单更新钉死旧裸头部措辞的既有条目。

**Verify**: 目标测试命令 → 全 PASS；整包测试 → `ok …/pkg/tool`。

### Step 4: 收尾验证与提交

**Verify**:
- `go vet -tags fts5 ./pkg/tool/` exit 0；`gofmt -l pkg/tool` 无输出
- `grep -rn 'Calling process\|Called process' pkg/ --include='*.go'` 不再命中 process 工具的断言（default 分支用其它工具名的测试保留）
- `git status --porcelain` 只列 in-scope 文件
- 提交（不 push）

## Test plan

- 新增 1 条表格式测试（Step 3），覆盖五种 action × running/completed/failed 的代表组合与 id 的 Input/Output 双来源。
- 既有守卫必须原样绿：`display_header_test.go` 全部既有用例（尤其 shell/read_file 的头部断言，证明新分支没有外溢）。
- 结构先例：`pkg/tool/display_header_test.go:57`。
- Verification: 目标测试 + 整包测试 exit 0。

## Done criteria

机器可检查，ALL 必须成立：

- [ ] `grep -c 'case lower == "process"' pkg/tool/header_parts.go` = 1
- [ ] `grep -c 'TestDisplayPartsNamesTheProcessCommandAndID' pkg/tool/display_header_test.go` = 1
- [ ] 目标测试与整包测试 exit 0；vet / gofmt 干净
- [ ] `grep -rn 'Calling process\|Called process' pkg/ --include='*.go'` 无 process 工具的生产或测试断言残留
- [ ] `git status --porcelain` 仅 in-scope 文件；`pkg/tui`、`frontend/`、`pkg/run`、`pkg/event` 零改动

## STOP conditions

- shell 分支（:124-141）或 default 分支（:424-444）摘录与活代码不符。
- 实测 started 事件的 `meta.Input` 为空（活卡头部仍裸）⇒ 事件发射侧问题，停止并报告，不要改用 Output 猜命令。
- Step 1 在**生产代码**里命中 "Calling/Called process" 字面量（存在第二处头部来源）。
- 既有用例因新分支大面积变红且无法用"钉死旧裸头部"解释（说明分支条件外溢）。

## Maintenance notes

- 新增 process action 时必须同步扩展本分支与 Step 3 测试；default 子分支保证未命名的 action 仍有可读头部。
- Deferred：转录摘要 `toolInvocationLabel`（`format.go:2823-2846`）对 process 仍走通用入参 JSON
  （`process {"action":"start",…}`）。若 owner 要紧凑摘要，在 `toolInvocationNamedLabel`
  （:2851）加 `case "process":` 复用本计划的动词／target 组装，另立项。
- 与 370（完成 payload 陈旧 `running`）、371（活卡输出流）互不依赖，可任意顺序落。
