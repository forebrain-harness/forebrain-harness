# Plan 372: process 卡片头部必须点名它在执行的命令（或进程号）

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving on. If anything in the
> "STOP conditions" section occurs, stop and report — do not improvise.
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/tool/header_parts.go pkg/tool/header_parts_test.go pkg/tool/display_header_test.go pkg/tool/testdata/header_actions.json ai-elements-vue/packages/elements/src/tool/headerWords.ts frontend/src/locales/toolHeader.test.ts`
> `git diff --stat e1abd2f4 -- pkg/tool/header_parts.go pkg/tool/display_header_test.go`
> 用 `git diff <sha> -- <paths>` 对**工作树**比，**不要**用 `e1abd2f4..HEAD` 的范围 diff（看不到未提交改动）。
> 基线 `e1abd2f4` 之后 dev 已前进（stop-display 工作），下列漂移属**预期**，逐段比对下文摘录、内容相符即继续，**不 STOP**：
> - `display_header_test.go` 末尾追加了 `TestStoppedCardSaysNothing`（+34 行）——本计划引用的 :57 先例内容不变。
> - `format.go`／`state.go` 的 stop-display 改动（`StepEvent.Canceled`、`ToolStepHoldsNoCard` 签名）——本计划引用的
>   `BuildToolMeta`／`intFromAny` 内容不变。
> 摘录内容不符 → STOP。行号漂移不算漂移，以内容为准。

## Status

- **Priority**: P1（信息缺陷：活卡与完成卡头部只有 "Calling/Called process"，用户与复盘者都不知道在执行什么；owner 报障截图 "Calling process 1m 47s"）
- **Effort**: M（显示层一个 switch 分支＋测试，**另含 action 词表三处同步**：`HeaderActions()`、golden
  `testdata/header_actions.json`、中文词表 `ai-elements-vue/.../headerWords.ts`——见 Step 4，缺一不可）
- **Risk**: LOW-MED（不改事件／payload／渲染表面，但新增 action 词进入 TUI／web 共享词表，须三处同步否则 web 中文卡缺词）
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
  `wait` 为 ready 模式（start 可选）。
- **`ToolMeta` 只有 `Input`，没有 `Output` 字段**（`pkg/tool/notification.go:329-385` 逐字核实；
  `BuildToolMeta` 只把 `evt.Input` 拷进 `meta.Input`，不拷 `evt.Output`）。头部能读到的只有入参：
  start 的 `command`/`wait`、wait/log/kill 的 `id`（工具要求必传，故 Input 里一定有）。工具**返回 payload**
  虽也带 `id`，但那不进 `ToolMeta`，头部读不到——**不要**写 `meta.Output[...]`（编译失败：unknown field Output）。
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
| 目标测试 | 见下方代码块（含 `|` 的命令不放表格：GFM 吞裸竖线、逼出 `\|` 假绿） | `ok …/pkg/tool` |
| 整包测试 | 目标测试命令去掉 `-run` | `ok …/pkg/tool` |
| 词表 golden 再生 | `FOREBRAIN_HOME="$(mktemp -d)" UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run TestHeaderActionsGolden -count=1` | 重写 `testdata/header_actions.json`，exit 0 |
| 词表守卫测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestHeaderActions' -count=1` | `ok`（覆盖＋golden 双绿） |
| 前端词表测试 | `cd frontend && pnpm exec vitest run src/locales/toolHeader.test.ts` | 全 PASS（每个新词都有中文；`pnpm test -- <路径>` 不过滤，勿用） |
| Vet | `go vet -tags fts5 ./pkg/tool/` | exit 0 |
| Fmt | `gofmt -l pkg/tool` | 无输出 |

目标测试命令（裸 `|` 是 Go regexp 的或；**不要**写成 `\|`，那会匹配零个测试并 exit 0 假绿）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ \
  -run 'TestDisplay|TestHeader|TestProcess' -count=1
```

## Scope

**In scope**（允许修改的文件）:
- `pkg/tool/header_parts.go` — 新增 `case lower == "process":` 分支；**并在 `HeaderActions()`（:523）追加新 action 词**（Step 4）。
- `pkg/tool/display_header_test.go` — 新增头部三元组测试（Step 3）。
- `pkg/tool/header_parts_test.go` — `TestHeaderActionsCoverEveryKnownTool` 的 inputs map 加 process 的 per-action 遍历（Step 4）。
- `pkg/tool/testdata/header_actions.json` — 由 `UPDATE_HEADER_ACTIONS=1` 再生（Step 4，勿手改）。
- `ai-elements-vue/packages/elements/src/tool/headerWords.ts` — `HEADER_ACTION_ZH` 补新词＋`headerSuffixWords` 补 `wait` 规则（Step 4）。
  **这才是中文词表的真实位置**；`frontend/src/locales/toolHeader.ts` 只是 re-export，不改。
- `frontend/src/locales/toolHeader.test.ts` — 补 process 新词与 `wait` suffix 的断言（Step 4，仿 notes 段先例）。

**Out of scope**（看起来相关但不要碰）:
- `pkg/tool/format.go` 的 `toolInvocationLabel` / `SummarizeToolStep` — 转录摘要今天给 process 的是通用入参 JSON（`process {"action":"start",…}`），丑但不缺信息；紧凑化另立项（见 Maintenance notes）。
- `pkg/tui/*` — 头部三元组的消费者，零改动（TUI 直接显示英文 action）。
- 事件发射侧（`pkg/run`、`pkg/event`）— `meta.Input` 已携带所需事实。
- process 的 payload 形状（370 的领地）与活卡输出流（371 的领地）。
- `frontend/src/locales/toolHeader.ts`（re-export，无需改）与任何渲染组件——词表补词即可，组件零改动。

## Git workflow

- Worktree／分支：`.worktrees/process-card-header-command`（同名分支），BASE = **派发时的 dev HEAD**
  （`e1abd2f4` 是起草基线，dev 已前进；从旧 SHA 建分支会无法 ff-only 落 dev）。与 370／371 文件零交集，可并行。
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
		// id is a JSON number in Input (wait/log/kill require it). ToolMeta has
		// no Output field, so Input is the only source the header can read.
		id := intFromAny(meta.Input["id"])
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
			// The verb carries the object (like peer_list's "Listed agents"), so
			// the Chinese head reads "已列出进程" instead of "已列出 processes".
			if isFailed {
				action = "Failed to list processes"
			} else if isRunning || isPending {
				action = "Listing processes"
			} else {
				action = "Listed processes"
			}
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
- `id` 是 JSON 数字，`InputString` 读不到 ⇒ 用 `intFromAny(meta.Input["id"])`（**只从 Input**；`ToolMeta` 无 Output 字段）。
- start 的 target 缺失时**不**回退到入参 JSON（那正是噪音）；回退 `meta.Invocation` 与 shell 同款即可，仍空则 target 留空（头部只剩动词，好过一行 JSON）。
- list 的宾语进动词（`Listed processes`），target 留空——仿 `peer_list` 的 `Listed agents`，中文读「已列出进程」。
- 动词表与本计划测试、`HeaderActions()`、中文词表一一对应；改动词即同步改这三处，同提交（Step 4）。

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

	killDone := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Input: map[string]any{
		"action": "kill", "id": float64(7),
	}}}
	action, target, _ = DisplayParts(killDone, t.TempDir())
	require.Equal(t, "Killed", action)
	require.Equal(t, "process #7", target)

	listRunning := DisplayFrame{Title: "process", Meta: ToolMeta{Status: "running", Input: map[string]any{
		"action": "list",
	}}}
	action, target, _ = DisplayParts(listRunning, t.TempDir())
	require.Equal(t, "Listing processes", action)
	require.Equal(t, "", target)

	// A failed start words the failure; an action this build does not name keeps
	// the generic verb (never a guessed one).
	startFailed := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "failed", Input: map[string]any{
		"action": "start", "command": "go test ./pkg/tool",
	}}}
	action, target, _ = DisplayParts(startFailed, t.TempDir())
	require.Equal(t, "Failed to start", action)
	require.Equal(t, "go test ./pkg/tool", target)

	unknown := DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{Status: "completed", Input: map[string]any{
		"action": "teleport", "id": float64(7),
	}}}
	action, target, _ = DisplayParts(unknown, t.TempDir())
	require.Equal(t, "Called", action)
	require.Equal(t, "process #7", target)
}
```

（若该文件尚未 import `require` 或 `DisplayParts` 的包内调用方式与既有用例不同，以文件内既有用例为准调整调用形态，**断言值不变**。）
同时按 Step 1 的命中清单更新钉死旧裸头部措辞的既有条目。

**Verify**: 目标测试命令 → 全 PASS；整包测试 → `ok …/pkg/tool`。

### Step 4: 同步 action 词表三处（缺一即 web 中文卡缺词）

新分支产出 12 个新 action 词（log 复用 read_file 的 `Read/Reading/Failed to read`，default 复用
`Called/Calling/Failed to call`，均已在表内，无需新增）。**词表契约**：`HeaderActions()`（引擎）→
golden `testdata/header_actions.json`（前端测试直接 import 它）→ 中文表 `headerWords.ts`，三处同步，
缺任何一处 `frontend/src/locales/toolHeader.test.ts` 即红（它遍历 golden 要求每个词有中文）。

| action | 中文 |
|---|---|
| Starting | 正在启动 |
| Started | 已启动 |
| Failed to start | 启动失败 |
| Waiting for | 正在等待 |
| Waited for | 已等待 |
| Failed to wait for | 等待失败 |
| Killing | 正在终止 |
| Killed | 已终止 |
| Failed to kill | 终止失败 |
| Listing processes | 正在列出进程 |
| Listed processes | 已列出进程 |
| Failed to list processes | 列出进程失败 |

（中文词表测试要求除 MCP 外不含 ASCII 字母——上表全部满足。）

**4a.** `pkg/tool/header_parts.go` 的 `HeaderActions()`（:523）actions 切片追加这 12 个词（放 Shell 段附近，注释 `// Process`）。

**4b.** 再生 golden（**勿手改 JSON**）：
`FOREBRAIN_HOME="$(mktemp -d)" UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run TestHeaderActionsGolden -count=1`
→ 重写 `pkg/tool/testdata/header_actions.json`。

**4c.** `pkg/tool/header_parts_test.go` 的 `TestHeaderActionsCoverEveryKnownTool`：仿 notes 的 per-operation
循环（:267-273），在 skill 段之前加 process 的 per-action 遍历，使新词受覆盖守卫（该测试的 inputs map
今天没有 process，不加则新词绕过守卫、中文表静默缺词）：

```go
	// The process tool words each action; every verb it can produce belongs to
	// the table, including the generic fallback for an action it does not name.
	for _, act := range []string{"start", "wait", "log", "kill", "list", ""} {
		for _, status := range statuses {
			check(t, DisplayFrame{Title: "process", Final: true, Meta: ToolMeta{
				ToolName: "process", Status: status, Input: map[string]any{
					"action": act, "command": "go test ./pkg/tool", "id": 7, "wait": "log",
				},
			}})
		}
	}
```

（canceled/denied 由共享尾部 :446-453 覆盖成 `Canceled`/`Denied`，已在表内。）

**4d.** `ai-elements-vue/packages/elements/src/tool/headerWords.ts`：
- `HEADER_ACTION_ZH` 追加上表 12 个键值（放 Shell 段附近，注释 `// Process`）。
- `headerSuffixWords` 在 `in (.+)` 规则**之前**加 `wait` 规则（start 卡的 `wait log`/`wait tcp` suffix 中文化）：

```ts
  match = suffix.match(/^wait (.+)$/)
  if (match) return `等待 ${match[1]}`
```

**4e.** `frontend/src/locales/toolHeader.test.ts`：仿 notes 段（`it('words each notes operation…')`）加一个 `it`：

```ts
  it('words each process action and its wait suffix', () => {
    expect(headerActionWord('Started', 'zh')).toBe('已启动')
    expect(headerActionWord('Starting', 'zh')).toBe('正在启动')
    expect(headerActionWord('Failed to start', 'zh')).toBe('启动失败')
    expect(headerActionWord('Waited for', 'zh')).toBe('已等待')
    expect(headerActionWord('Waiting for', 'zh')).toBe('正在等待')
    expect(headerActionWord('Failed to wait for', 'zh')).toBe('等待失败')
    expect(headerActionWord('Killed', 'zh')).toBe('已终止')
    expect(headerActionWord('Killing', 'zh')).toBe('正在终止')
    expect(headerActionWord('Failed to kill', 'zh')).toBe('终止失败')
    expect(headerActionWord('Listed processes', 'zh')).toBe('已列出进程')
    expect(headerActionWord('Listing processes', 'zh')).toBe('正在列出进程')
    expect(headerActionWord('Failed to list processes', 'zh')).toBe('列出进程失败')
    expect(headerSuffixWords('wait log', 'zh')).toBe('等待 log')
    expect(headerSuffixWords('wait tcp', 'zh')).toBe('等待 tcp')
  })
```

**Verify**:
- 词表守卫测试命令 → `ok`（`TestHeaderActionsCoverEveryKnownTool` 与 `TestHeaderActionsGolden` 双绿）
- 前端词表测试命令 → 全 PASS
- `grep -c '"Started"' pkg/tool/testdata/header_actions.json` = 1（golden 已含新词）

### Step 5: 收尾验证与提交

**Verify**:
- `go vet -tags fts5 ./pkg/tool/` exit 0；`gofmt -l pkg/tool` 无输出
- `grep -rn 'Calling process\|Called process' pkg/ --include='*.go'` 不再命中 process 工具的断言（default 分支用其它工具名的测试保留；评审实跑基线零命中）
- `git status --porcelain` 只列 in-scope 文件
- 提交（不 push）

## Test plan

- 新增 1 条头部三元组测试（Step 3），覆盖五种 action 的 running/completed/failed 代表组合、未命名 action 的通用回落，id 从 Input 取。
- Step 4 新增词表守卫：`TestHeaderActionsCoverEveryKnownTool` 的 process per-action 遍历、`TestHeaderActionsGolden`（golden 再生）、前端 `toolHeader.test.ts` 的 process 段。
- 既有守卫必须原样绿：`display_header_test.go` 全部既有用例（尤其 shell/read_file 的头部断言与新增的 `TestStoppedCardSaysNothing`，证明新分支没有外溢）；`header_parts_test.go` 既有工具遍历。
- 结构先例：`pkg/tool/display_header_test.go:57`；词表守卫先例 `header_parts_test.go:201-307`；前端先例 `toolHeader.test.ts` 的 notes 段。
- Verification: 目标测试 + 整包测试 + 词表守卫测试 + 前端词表测试均 exit 0。

## Done criteria

机器可检查，ALL 必须成立：

- [ ] `grep -c 'case lower == "process"' pkg/tool/header_parts.go` = 1
- [ ] `grep -c 'TestDisplayPartsNamesTheProcessCommandAndID' pkg/tool/display_header_test.go` = 1
- [ ] `grep -c 'meta.Output' pkg/tool/header_parts.go` = 0（`ToolMeta` 无 Output 字段，写了即编译失败）
- [ ] 目标测试与整包测试 exit 0；vet / gofmt 干净
- [ ] 词表三处同步：`grep -c '"Started"' pkg/tool/testdata/header_actions.json` = 1；`grep -c "'Started'" ai-elements-vue/packages/elements/src/tool/headerWords.ts` = 1；词表守卫测试（`-run 'TestHeaderActions'`）与前端词表测试均 exit 0
- [ ] `grep -rn 'Calling process\|Called process' pkg/ --include='*.go'` 无 process 工具的生产或测试断言残留
- [ ] `git status --porcelain` 仅 in-scope 文件；`pkg/tui`、`pkg/run`、`pkg/event` 零改动

## STOP conditions

- shell 分支（:124-141）或 default 分支（:424-444）摘录与活代码不符。
- 实测 started 事件的 `meta.Input` 为空（活卡头部仍裸）⇒ 事件发射侧问题，停止并报告，不要改用 Output 猜命令。
- Step 1 在**生产代码**里命中 "Calling/Called process" 字面量（存在第二处头部来源）。
- 既有用例因新分支大面积变红且无法用"钉死旧裸头部"解释（说明分支条件外溢）。

## Maintenance notes

- 新增 process action 时必须同步扩展四处：本分支、Step 3 测试、`HeaderActions()`＋golden、中文词表（Step 4）；
  default 子分支保证未命名的 action 仍有可读头部。漏掉词表同步会让 web 中文卡静默显示英文动词。
- 与 stop-display 的交互：`StoppedCardSaysNothing`（`display_header.go:113`）按「头部是否点名 subject」决定取消卡画不画。
  今天取消的 process 卡 target＝工具名 `process`（非空⇒保留）；本计划后 target＝命令/进程号（仍非空⇒保留，且更有信息），无回归。
- Deferred：转录摘要 `toolInvocationLabel`（`format.go:2823-2846`）对 process 仍走通用入参 JSON
  （`process {"action":"start",…}`）。若 owner 要紧凑摘要，在 `toolInvocationNamedLabel`
  （:2851）加 `case "process":` 复用本计划的动词／target 组装，另立项。
- 与 370（完成 payload 陈旧 `running`）、371（活卡输出流）文件零交集、互不依赖，可并行落。

## 评审修订记录（2026-11-23 review-plan，复核时 HEAD `3ff6b134`）

缺口经评审独立实跑证实（`go test -overlay`，仓库树零改动）：当前 `DisplayParts(process start running)`
返回 `("Calling","process","")`（裸头部属实）。锚点 shell :124-141／default :424-444／`DisplayParts` :505／
共享尾 :446-460／`fmt` 已 import／`intFromAny` 处理 float64／`DisplayFrame` 字段／:57 先例均逐字核实。就地修订：
1. **BLOCKER：`ToolMeta` 无 `Output` 字段**（`notification.go:329-385` 核实；overlay 实跑报 `unknown field Output in struct literal`）——
   分支 `meta.Output["id"]` 与测试 `ToolMeta{Output:…}` 照抄即编译失败。id 改只从 Input 取，删 Output 回退与「完成卡两者皆有」断言。
2. **killDone 逻辑缺陷**：原测试无 Input.action ⇒ act 空 ⇒ default 子分支 ⇒ `Called`≠`Killed`；改用 `Input{"action":"kill","id":float64(7)}`。
3. **BLOCKER：词表契约整段缺失**——新增 12 个 action 词须同步 `HeaderActions()`＋golden＋中文表，而原计划禁 frontend 改动、
   done 判据要求 frontend 零改动（与 README 自己的 372 条目矛盾）。中文表真实位置是
   `ai-elements-vue/packages/elements/src/tool/headerWords.ts`（`frontend/src/locales/toolHeader.ts` 只是 re-export）；
   `frontend/src/locales/toolHeader.test.ts` 直接 import 引擎 golden，缺词即红。新增 Step 4 三处同步＋扩 scope＋改 done 判据。
4. **覆盖守卫**：`TestHeaderActionsCoverEveryKnownTool` 的 inputs map 无 process ⇒ 加 per-action 遍历（仿 notes :267-273）。
5. **目标命令含裸 `|` 在表格内** → 移 fenced block。
6. **润色**：list 改动词带宾语（`Listed processes`，仿 peer_list）避免中文「已列出 processes」；`wait` suffix 加 `headerSuffixWords` 中文化规则。
7. **drift check 语义**＋预期漂移白名单（display_header_test.go +34、format.go/state.go stop-display）；**BASE 改派发时 dev HEAD**。
8. Effort S→M、Risk LOW→LOW-MED（词表三处同步）。
结论：缺口成立，修订后可执行（P1/M/LOW-MED，TODO）；与 370／371 文件零交集可并行。

## 执行记录（2026-11-23 `execute`，评审 APPROVE）

- Worktree／分支：`.worktrees/process-card-header-command`（BASE `3ff6b134`＝派发时 dev HEAD），
  提交 `ef57158f`（评审的这版），**落地时 rebase 到 370 之后为 `01b421c9`**，已 ff-only 落 dev
  （`ba1f834a..01b421c9`，零冲突），未 push。
- 改动（6 文件 +200/−0，与 in-scope 列表逐条一致）：`pkg/tool/header_parts.go`（`case lower == "process"`
  五路措辞＋`HeaderActions()` 追加 12 词）、`pkg/tool/display_header_test.go`
  （`TestDisplayPartsNamesTheProcessCommandAndID`）、`pkg/tool/header_parts_test.go`
  （`TestHeaderActionsCoverEveryKnownTool` 的 process per-action 遍历）、`pkg/tool/testdata/header_actions.json`
  （`UPDATE_HEADER_ACTIONS=1` 再生，diff 仅 +12 词、无删改）、
  `ai-elements-vue/packages/elements/src/tool/headerWords.ts`（`HEADER_ACTION_ZH` 12 词＋`wait (.+)` suffix 规则，
  位置在 `in (.+)` 之前）、`frontend/src/locales/toolHeader.test.ts`（process 段断言）。
- 评审独立复跑：`-run 'TestDisplay|TestHeader|TestProcess'` 18 条全 PASS；`-run 'TestHeaderActions'` ok（覆盖＋
  golden 双绿）；整包 `ok …/pkg/tool 16.2s`；`pnpm exec vitest run src/locales/toolHeader.test.ts` 5/5 PASS，
  并逐条核到 12 个新词都有中文且不含 ASCII 字母；build／vet／gofmt 干净；
  `grep -c 'case lower == "process"'`=1、`meta.Output`=0、golden `"Started"`=1、中文表 `'Started'`=1、
  `grep -rn 'Calling process\|Called process' pkg/ --include='*.go'`=0；`pkg/tui`／`pkg/run`／`pkg/event` 零改动。
- 已判定可接受的偏差（两项，均在范围内、服务判据意图）：① 执行器把计划注释里的字面 `"Calling process"` 改写为
  同义句（`A head that named only the tool left the user watching a timer…`），否则 done 判据的 grep 会命中
  注释本身；WHY 完整保留；② `grep -c TestDisplayPartsNamesTheProcessCommandAndID`=2 而非计划写的 1——该文件每条
  测试都带 `// TestX …` doc 注释（既有 `TestStoppedCardSaysNothing` 同样 =2），`func` 声明计数=1，
  「测试恰好存在一次」的意图成立；改命中数会破坏本文件的注释惯例。
- 落地：与 370／371 文件零交集，可直接 `git merge --ff-only process-card-header-command`。
