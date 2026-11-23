# Plan 080：已删除的自定义子代理不再接受消息，且“自定义来源却没有工具策略”的运行一律 fail-closed

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> **为什么不能用 worktree**：本计划建立在工作区里**未提交**的 079 改动之上（`pkg/run/subagent_catalog.go` 是未跟踪文件，worktree 里根本没有）。
> 开工前先记快照，收尾时对比：`git status --short > "$TMPDIR/080-before.txt"`。
>
> **环境**（每个 shell 都要）：
> ```
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
> export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
> export FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1
> ```
> 若 `go test` 报 `Package pdfium was not found in the pkg-config search path`，先跑 `make pdfium pdfium-fonts`。
>
> **Drift check（先跑）**：以下 5 条都必须各有 1 行输出，否则 STOP：
> ```
> grep -n 'func SendToSubagent(ctx context.Context, r \*Runner' pkg/run/subagent.go
> grep -n 'func (c SubagentCatalog) configures' pkg/run/subagent_catalog.go
> grep -n 'func SubagentToolPolicyFromContext' pkg/tool/state.go
> grep -n 'func filterToolsForSubagentSubtype(ctx context.Context' pkg/run/typedsubagent_llm.go
> grep -n 'if policy, ok := SubagentToolPolicyFromContext(ctx); ok {' pkg/tool/permissions.go
> ```

## Status

- **Priority**: P1（安全：只读自定义子代理在被删除后会以完整工具集运行）
- **Effort**: S
- **Risk**: LOW（只加拒绝与兜底；正常路径的行为不变，已有测试钉住）
- **Depends on**: 工作区里未提交的 079（非编号依赖：079 已执行、评审 APPROVE，未提交）
- **Category**: bug / security
- **Planned at**: commit `3096298` + 工作区（079 未提交），2026-10-19

## Why this matters

079 让每个主代理可以定义**自定义子代理**，工具权限（只读 / 可编辑 / 指定工具）是**配置**，不是类型名，所以运行时靠一份挂在上下文里的 `SubagentToolPolicy` 执行。
这份策略在“续聊一个已存在的子代理”时从**当前配置**按名字重新查出来；配置里这条被删掉后查不到，**策略就没了，而静态表对未知类型名是放行的**。
结果（评审方已在 scratch 副本里复现）：用户在网页抽屉 / TUI 子代理视图里给一个已被删除的只读自定义子代理发一句话，这次运行的类型仍是 `repo-scout`、来源仍是 `custom`，但 `write_file`、`edit_file`、`subagent_run` 全部被放行，自定义系统提示词也丢了。
模型驱动的 `subagent_continue` 路径早就有“已被删除”的检查（`continueSubagentExecution`），用户驱动的 `SendToSubagent` 路径没有，测试也只覆盖了前者。

owner 已裁定（2026-10-19）：
1. **已删除的自定义子代理直接拒绝再发消息**（与模型路径一致），不做“降级只读继续跑”。
2. **续聊的工具档位继续取当前配置**（保持现状，不做快照）。所以把某个自定义子代理从 read_only 改成 edit，它的老记录下一句话起就能写；收紧则立即生效。**本计划不改这一语义**。

本计划因此做两件事：① 在用户驱动入口拒绝已删除的自定义子代理；② 加一道不依赖入口的兜底——“来源是 custom 却没有策略”的运行，所有工具一律拒绝、一个都不提供，这样今后任何新入口漏了检查也不会放权。

## Current state

- `pkg/run/subagent.go`
  - `:1808 subagentRecordContext(ctx, fac, record)` 从记录重建运行上下文；`:1826-1830`（工作区现状）：
    ```go
    if strings.TrimSpace(record.AgentKind) == "typed" {
        // The policy a custom subagent ran under on its first execution, for the
        // same reason: it is configuration, and the record's execution has to
        // answer to it again.
        if policy, ok := SubagentCatalogFor(fac.appConfig(), fac.AgentName).ToolPolicy(record.AgentType); ok {
            hostCtx = tool.WithSubagentToolPolicy(hostCtx, policy)
        }
    }
    ```
    条目已被删除时 `ToolPolicy` 返回 `ok=false`，什么都不挂。类型（`:1812`）与来源（`:1814`，`WithSubagentDefinitionSource(hostCtx, record.DefSource)`）照常挂上。
  - `:1971 SendToSubagent(ctx, r, surface, conversationSessionID, agentKey, in, mode)`：用户给子代理发消息的唯一引擎入口，调用方有网页 `pkg/gateway/api_extra.go:2462`（`handleSubagentInput`，非 `ErrSubagentNotFound` 的错误回 400 + 错误文本）、TUI `pkg/tui/chat_session.go:1293`（失败时在子代理视图打 `Could not send this to the subagent: …` 并还原输入框）、自动续跑 `pkg/process/one_shot.go:77`（出错即 `ErrAutoContinueUnavailable`）。当前**没有**任何“类型是否还存在”的检查，直接 `go startUserSubagentExecution(...)`（`:2085`，`:2097` 用 `subagentRecordContext`）。
  - `:2297 continueSubagentExecution`（模型路径）已有检查，`:2326-2336`：
    ```go
    if strings.TrimSpace(record.DefSource) == SubagentSourceCustom {
        catalog := SubagentCatalogFor(fac.appConfig(), fac.AgentName)
        if _, rerr := catalog.ResolvePublic(record.AgentType); rerr != nil {
            if !catalog.configures(record.AgentType) {
                return agent.HistoryEntry{}, fmt.Errorf("subagent type %q no longer exists for this agent", strings.TrimSpace(record.AgentType))
            }
            return agent.HistoryEntry{}, rerr   // 被停用：用目录自己的理由
        }
    }
    ```
    已有测试钉住两句话：`pkg/run/subagent_test.go:5593 TestContinueAfterCustomSubagentDeletedSaysSo`、`:5638 TestContinueAfterCustomSubagentDisabledSaysDisabled`。
- `pkg/run/subagent_catalog.go`（未跟踪文件）：`:15 const SubagentSourceCustom = "custom"`；`:140 ToolPolicy(name)` 条目不存在返回 `(zero, false)`；`configures(name)`：内置名恒真，自定义名看条目是否在配置里（含已停用的）。
- `pkg/tool/permissions.go:836 GuardTool`：
  ```go
  st := strings.ToLower(strings.TrimSpace(SubagentTypeFromContext(ctx)))
  if policy, ok := SubagentToolPolicyFromContext(ctx); ok {
      if policy.Blocks(name) { return fmt.Errorf("subagent_type %q: tool %q blocked by policy", st, name) }
  } else if st != "" {
      if blockedByType, ok := typedSubagentDisallowedTools[st]; ok { ... }   // 静态表：未知类型名 → 放行
  }
  ```
  `pkg/tool/registry.go:462 toolBlockedForSubtype` 同理，未知类型返回 false。
- `pkg/run/typedsubagent_llm.go:136 filterToolsForSubagentSubtype(ctx, subtype, tools)`：有策略就按策略过滤，否则走 `tool.VisibleToolsForSubagentSubtype`（静态表）。
- `pkg/tool/state.go:562/569`：`WithSubagentDefinitionSource` / `SubagentDefinitionSourceFromContext`（来源会被小写化）。`pkg/process/worker_cli.go:64-68` 在 worker 进程里把上下文里的类型、策略、来源原样再挂一遍（只复制，不会凭空造策略）。
- 惯例：测试文件必须与被测生产文件同名（`pkg/architecture` 强制 `xxx_test.go ↔ xxx.go`），所以新测试只能进下面点名的现有测试文件；范例：`pkg/run/typedsubagent_llm_test.go:464 TestCustomReadOnlySubagentCannotWrite`（直接构造上下文测 `GuardTool` 与 `filterToolsForSubagentSubtype`）、`pkg/run/subagent_test.go:4516` 一带的 `SendToSubagent` 用例（`newPersistenceFixture` + `seedWorker` + `seedChannelRecord` + `channelTestExecutor`）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 目标包测试 | `go test -tags fts5 ./pkg/run ./pkg/tool -count=1` | `ok` ×2 |
| 单测过滤 | `go test -tags fts5 ./pkg/run -run 'TestUserMessageTo\|TestCustomSubagentWithoutPolicy' -count=1 -v` | 新用例 PASS |
| 相关包 | `go test -tags fts5 ./pkg/process ./pkg/tui ./pkg/gateway ./pkg/architecture -count=1` | `ok` ×4 |
| vet | `go vet ./pkg/... ./cmd/...` | 无输出 |
| gofmt | `gofmt -l cmd pkg third_party` | 无输出 |

## Scope

**In scope**（只允许改这些文件）：
- `pkg/tool/state.go`、`pkg/tool/state_test.go`
- `pkg/tool/permissions.go`、`pkg/tool/permissions_test.go`
- `pkg/run/subagent.go`、`pkg/run/subagent_test.go`
- `pkg/run/subagent_catalog.go`（只改 `SubagentSourceCustom` 的定义）
- `pkg/run/typedsubagent_llm.go`、`pkg/run/typedsubagent_llm_test.go`

**Out of scope**（不要碰，即便看着相关）：
- `pkg/gateway/*`、`frontend/*`、`pkg/tui/*`：错误已有现成的呈现（网页 400 + 文本，TUI 系统帧）；本计划不改呈现。
- 不改“续聊取当前配置”的语义（owner 裁定保持现状）；不引入快照。
- 内置类型与静态表 `typedSubagentDisallowedTools`；`pkg/process/worker_cli.go`。
- 已停用（disabled）的自定义子代理：用户驱动的发消息**照常允许**（用户自己的操作），只是模型驱动的续聊保持拒绝——不要改成拒绝。

## Steps

### Step 0：基线

`git status --short > "$TMPDIR/080-before.txt"`，然后跑 Drift check 的 5 条 grep，再跑
`go test -tags fts5 ./pkg/run ./pkg/tool -count=1`。
**Verify**: grep 各 1 行；两个包 `ok`。否则 STOP（基线不绿，不要在红的树上叠改动）。

### Step 1：先写测试（红）

**1a** `pkg/tool/state_test.go` 追加（需要的 import 按文件现状补，`appcfg` 即 `pkg/config`）：

```go
// A typed run whose definition is custom — configuration, not a type name — has
// its tool policy on the context. One that carries the source without the
// policy is a run that lost it, and the static per-type table knows nothing
// about a custom name, so it must be recognised and refused.
func TestCustomSubagentPolicyMissing(t *testing.T) {
	typed := WithSubagentType(context.Background(), "repo-scout")
	custom := WithSubagentDefinitionSource(typed, SubagentDefinitionSourceCustom)
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"not a subagent", context.Background(), false},
		{"built-in typed run", WithSubagentDefinitionSource(WithSubagentType(context.Background(), "explore"), "built-in"), false},
		{"typed run, no source", typed, false},
		{"custom source, no type", WithSubagentDefinitionSource(context.Background(), SubagentDefinitionSourceCustom), false},
		{"custom source, no policy", custom, true},
		{"custom source with policy", WithSubagentToolPolicy(custom, SubagentToolPolicy{Mode: appcfg.SubagentToolsReadOnly}), false},
	}
	for _, tc := range cases {
		if got := CustomSubagentPolicyMissing(tc.ctx); got != tc.want {
			t.Errorf("%s: CustomSubagentPolicyMissing = %v, want %v", tc.name, got, tc.want)
		}
	}
}
```

**1b** `pkg/tool/permissions_test.go` 追加：

```go
// A custom subagent that reaches GuardTool without its policy is refused
// everything — not waved through because the static table has no entry for its
// name.
func TestGuardToolRefusesACustomSubagentWithoutAPolicy(t *testing.T) {
	st := NewState(t.TempDir())
	ctx := WithSubagentDefinitionSource(WithSubagentType(context.Background(), "repo-scout"), SubagentDefinitionSourceCustom)
	for _, name := range []string{"read_file", "write_file", "edit_file", "shell", "subagent_run"} {
		err := st.GuardTool(ctx, name)
		if err == nil || !strings.Contains(err.Error(), "no tool policy") {
			t.Errorf("GuardTool(%q) = %v, want a refusal naming the missing policy", name, err)
		}
	}
	// With its policy the run is held to it, not refused wholesale.
	withPolicy := WithSubagentToolPolicy(ctx, SubagentToolPolicy{Mode: appcfg.SubagentToolsReadOnly})
	if err := st.GuardTool(withPolicy, "read_file"); err != nil {
		t.Fatalf("read_file refused under a read-only policy: %v", err)
	}
	if err := st.GuardTool(withPolicy, "write_file"); err == nil || !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("write_file under a read-only policy = %v", err)
	}
}
```

**1c** `pkg/run/typedsubagent_llm_test.go` 追加（范例：`TestCustomReadOnlySubagentCannotWrite`）：

```go
// A custom run that lost its policy is offered no tool at all.
func TestCustomSubagentWithoutPolicySeesNoTools(t *testing.T) {
	ctx := toolpkg.WithSubagentType(context.Background(), "repo-scout")
	ctx = toolpkg.WithSubagentDefinitionSource(ctx, toolpkg.SubagentDefinitionSourceCustom)
	tools := []*llm.Tool{testTool(t, "read_file"), testTool(t, "write_file"), testTool(t, "subagent_run")}
	if got := filterToolsForSubagentSubtype(ctx, "repo-scout", tools); len(got) != 0 {
		t.Fatalf("a custom subagent with no policy was offered %d tools", len(got))
	}
}
```

**1d** `pkg/run/subagent_test.go` 追加。先加夹具与三个用例（以下代码已在评审方 scratch 副本里编译并跑通过 configured/deleted 两种情形）：

```go
// customSubagentSend is a finished custom-subagent record ("repo-scout", typed,
// DefSource custom) in a conversation, owned by a runner whose primary agent
// "main" is configured with subagents. A nil subagents slice leaves the agent
// with no definition at all, which is the "deleted since" state.
type customSubagentSend struct {
	owner *Runner
	ex    *channelTestExecutor
	fac   Factory
	conv  string
}

func newCustomSubagentSend(t *testing.T, subagents []appcfg.SubagentConfig) customSubagentSend {
	t.Helper()
	fix := newPersistenceFixture(t, "worker system", "model answer")
	owner := fix.fac.Owner
	owner.Control = NewController()
	owner.AgentName = "main"
	cfg := &appcfg.Root{Agents: appcfg.AgentsSection{Defaults: appcfg.AgentDefaults{EnableSubagent: boolPtr(true)}}}
	if subagents != nil {
		cfg.Agents.Definitions = map[string]appcfg.AgentDefinition{"main": {Primary: true, Subagents: subagents}}
	}
	owner.AppCfg = cfg
	conv := "conv-custom-send"
	worker := "main:conv-custom-send:worker:00000000-0000-0000-0000-0000000000aa"
	fix.seedWorker(t, conv, worker, []llm.Message{llm.UserMessage(llm.Text("seed"))})
	seedChannelRecord(t, fix.fac, agent.HistoryEntry{
		TaskID: "task-x", RunID: "seed-run", ParentRunID: "parent-1", SessionID: conv, WorkerSessionID: worker,
		Task: "scout", Status: agent.StatusOK, AgentType: "repo-scout", AgentKind: "typed", DefSource: SubagentSourceCustom,
		RuntimeKind: "typed_subagent", Continuable: true, StartedAt: 10, UpdatedAt: 20, FinishedAt: 20,
	})
	ex := &channelTestExecutor{store: fix.store, answer: "ok", started: make(chan struct{}, 1)}
	owner.SubagentExecutor = ex
	return customSubagentSend{owner: owner, ex: ex, fac: fix.fac, conv: conv}
}

// startedContext sends one user message and returns the context the execution
// ran under.
func (s customSubagentSend) startedContext(t *testing.T) context.Context {
	t.Helper()
	delivery, err := SendToSubagent(context.Background(), s.owner, SubagentSurface{}, s.conv, "task-x", Input{Text: "hello"}, TurnInputModeSteer)
	if err != nil || delivery != SubagentDeliveryStarted {
		t.Fatalf("SendToSubagent = %q, %v; want started", delivery, err)
	}
	select {
	case <-s.ex.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the execution never started")
	}
	waitSubagentIdle(t, s.owner, s.conv, "task-x")
	return s.ex.contextSeen()
}

// A message addressed to a custom subagent its agent no longer defines is
// refused before anything runs: the type's tool policy was configuration, and
// there is no longer any to hold the run to.
func TestUserMessageToDeletedCustomSubagentIsRefused(t *testing.T) {
	s := newCustomSubagentSend(t, nil)
	delivery, err := SendToSubagent(context.Background(), s.owner, SubagentSurface{}, s.conv, "task-x", Input{Text: "hello"}, TurnInputModeSteer)
	if err == nil {
		t.Fatalf("SendToSubagent = %q, nil; want a refusal", delivery)
	}
	if !strings.Contains(err.Error(), `subagent type "repo-scout" no longer exists for this agent`) {
		t.Fatalf("refusal = %v", err)
	}
	if delivery != "" {
		t.Fatalf("delivery = %q, want none", delivery)
	}
	select {
	case <-s.ex.started:
		t.Fatal("a refused message started an execution")
	default:
	}
	record, ok, gerr := agent.GetMerged(s.fac.subagentScopeRoot(), agent.Query{SessionID: s.conv, TaskID: "task-x"})
	if gerr != nil || !ok || record.Status != agent.StatusOK {
		t.Fatalf("the record changed: %+v ok=%v err=%v", record, ok, gerr)
	}
}

// A configured custom subagent keeps running under its own policy when the user
// writes to it — the regression this pins is the policy riding the user-driven
// path's context.
func TestUserMessageToConfiguredCustomSubagentRunsUnderItsPolicy(t *testing.T) {
	s := newCustomSubagentSend(t, []appcfg.SubagentConfig{
		{Name: "repo-scout", Description: "d", SystemPrompt: "p", Tools: appcfg.SubagentToolsReadOnly},
	})
	ctx := s.startedContext(t)
	if _, ok := toolpkg.SubagentToolPolicyFromContext(ctx); !ok {
		t.Fatal("the user-driven execution carries no tool policy")
	}
	st := toolpkg.NewState(t.TempDir())
	for _, name := range []string{"write_file", "edit_file", "subagent_run"} {
		if err := st.GuardTool(ctx, name); err == nil {
			t.Errorf("a read-only custom subagent may call %s", name)
		}
	}
	if err := st.GuardTool(ctx, "read_file"); err != nil {
		t.Fatalf("read_file refused: %v", err)
	}
}

// Disabling is not deleting: the user may still talk to a subagent they
// switched off (only the model is kept from dispatching it), and it still runs
// under its policy.
func TestUserMessageToDisabledCustomSubagentStillRuns(t *testing.T) {
	s := newCustomSubagentSend(t, []appcfg.SubagentConfig{
		{Name: "repo-scout", Description: "d", SystemPrompt: "p", Tools: appcfg.SubagentToolsReadOnly, Enabled: boolPtr(false)},
	})
	ctx := s.startedContext(t)
	if _, ok := toolpkg.SubagentToolPolicyFromContext(ctx); !ok {
		t.Fatal("a disabled custom subagent lost its policy")
	}
}
```

**Verify（红）**：先只加测试、不动生产代码：
`go test -tags fts5 ./pkg/tool ./pkg/run -run 'TestCustomSubagentPolicyMissing|TestGuardToolRefusesACustomSubagent|TestCustomSubagentWithoutPolicy|TestUserMessageTo' -count=1`
预期：`pkg/tool` 报**编译错误** `undefined: CustomSubagentPolicyMissing` / `SubagentDefinitionSourceCustom`（这是红）。为了让 `pkg/run` 里的用例能单独看到行为上的红，先做 Step 2 的**仅常量与函数签名**部分再跑一次：`TestUserMessageToDeletedCustomSubagentIsRefused` 必须因 `SendToSubagent = "started", nil; want a refusal` 失败，`...ConfiguredCustomSubagentRunsUnderItsPolicy` 与 `...DisabledCustomSubagentStillRuns` 必须已经 PASS（它们钉的是现状）。

### Step 2：`pkg/tool` 的兜底

`pkg/tool/state.go`，紧挨 `SubagentToolPolicy` 定义处追加：

```go
// SubagentDefinitionSourceCustom is the definition source of a subagent a
// primary agent defines in its own configuration. Its tool policy is that
// configuration, carried on the run's context (see SubagentToolPolicy); no
// static table says anything about its name.
const SubagentDefinitionSourceCustom = "custom"

// CustomSubagentPolicyMissing reports a typed run whose definition is custom
// but whose tool policy never reached the context. Such a run is not a
// built-in the static table describes and not a custom one the policy
// describes: nothing says what it may call, so it may call nothing. Every entry
// point rebuilds the policy from the agent's configuration, which is exactly
// what is gone when the definition has been deleted; this is the net under any
// entry point that forgets to check first.
func CustomSubagentPolicyMissing(ctx context.Context) bool {
	if SubagentTypeFromContext(ctx) == "" || SubagentDefinitionSourceFromContext(ctx) != SubagentDefinitionSourceCustom {
		return false
	}
	_, ok := SubagentToolPolicyFromContext(ctx)
	return !ok
}
```

`pkg/tool/permissions.go`，`GuardTool` 里 `st := …` 之后、`if policy, ok := …` 之前插入：

```go
	if CustomSubagentPolicyMissing(ctx) {
		return fmt.Errorf("subagent_type %q: custom subagent has no tool policy, tool %q refused", st, name)
	}
```

**Verify**: `go test -tags fts5 ./pkg/tool -count=1` → `ok`（含 1a、1b 两个新用例）。

### Step 3：`pkg/run` 的入口检查与过滤

**3a** `pkg/run/subagent_catalog.go`：把 `const SubagentSourceCustom = "custom"` 改为 `const SubagentSourceCustom = tool.SubagentDefinitionSourceCustom`（该文件已 import `tool`）。两处定义不能各写一份字面量。

**3b** `pkg/run/typedsubagent_llm.go`：在 `filterToolsForSubagentSubtype` 里 `if len(tools) == 0 { return nil }` 之后加：

```go
	if tool.CustomSubagentPolicyMissing(ctx) {
		return nil
	}
```
注意该函数后面的循环里有名为 `tool` 的局部变量遮蔽包名——插入点在循环之前，引用的是包；插入后若编译报遮蔽错误，把局部变量改名，不要改插入点。

**3c** `pkg/run/subagent.go`：在 `continueSubagentExecution` 上方（或 `SendToSubagent` 上方）新增：

```go
// refuseGoneCustomSubagent answers a message addressed to a custom subagent
// whose definition this primary agent no longer has. A custom subagent is the
// agent's configuration, so its record can outlive its type; with the entry
// gone nothing can rebuild the tool policy the run was held to, and a run
// without one must not start. A type that is merely disabled still has a
// definition and is not refused here — switching it off keeps the model from
// dispatching it, not the user from talking to it.
func refuseGoneCustomSubagent(fac Factory, record agent.HistoryEntry) error {
	if strings.TrimSpace(record.DefSource) != SubagentSourceCustom {
		return nil
	}
	if SubagentCatalogFor(fac.appConfig(), fac.AgentName).configures(record.AgentType) {
		return nil
	}
	return fmt.Errorf("subagent type %q no longer exists for this agent", strings.TrimSpace(record.AgentType))
}
```

`SendToSubagent`：在 `ch, record, err := subagentChannelForConversation(...)` 的错误检查之后、`ch.setSurface(surface)` 之前加：

```go
	if err := refuseGoneCustomSubagent(r.subagentFactory(), record); err != nil {
		return "", err
	}
```
（放在 `setSurface` 之前，被拒的消息不能换掉当前接管的界面。对“正在跑、此刻才被删”的子代理也一并拒绝 steer/排队：已被删除的类型不再接受新输入，在途那次执行继续用它上下文里已有的策略跑完。）

`continueSubagentExecution`：把内联块改成复用，**行为与措辞逐字不变**：

```go
	if strings.TrimSpace(record.DefSource) == SubagentSourceCustom {
		if err := refuseGoneCustomSubagent(fac, record); err != nil {
			return agent.HistoryEntry{}, err
		}
		if _, rerr := SubagentCatalogFor(fac.appConfig(), fac.AgentName).ResolvePublic(record.AgentType); rerr != nil {
			return agent.HistoryEntry{}, rerr // disabled: the catalog's own reason
		}
	}
```
（保留原有注释的意思，把它挪到 `refuseGoneCustomSubagent` 的文档里，不要两处都留。）

**Verify**:
- `go test -tags fts5 ./pkg/run ./pkg/tool -count=1` → `ok` ×2，Step 1 的全部新用例 PASS，`TestContinueAfterCustomSubagentDeletedSaysSo`、`...DisabledSaysDisabled` 仍 PASS。
- `go test -tags fts5 ./pkg/run -run 'TestUserMessageTo|TestCustomSubagentWithoutPolicy|TestContinueAfterCustomSubagent' -count=1 -v` 里这几个用例名都出现且 PASS。

### Step 4：收口

1. `go vet ./pkg/... ./cmd/...`、`gofmt -l cmd pkg third_party` 均无输出。
2. `go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/process ./pkg/tui ./pkg/gateway ./pkg/architecture -count=1` → 全 `ok`。
3. 对比快照：`git status --short > "$TMPDIR/080-after.txt"; diff "$TMPDIR/080-before.txt" "$TMPDIR/080-after.txt"` → **无输出**（没有新增文件；你只修改了工作区里本来就已被修改或跟踪的文件）。再用 `git diff --stat -- <Scope 里的文件>` 确认改动只在 Scope 内。
4. 可选的真机确认（有 `.env` 与真模型时才做，没有就跳过并在报告里说明）：不要用真实 home；用 `FOREBRAIN_HOME="$(mktemp -d)"`，在 `forebrain.yaml` 里给 main 配一个只读自定义子代理，派发一次，随后删掉该条配置并重载，在子代理视图发一句话，应看到 `Could not send this to the subagent: subagent type "…" no longer exists for this agent`。

## Test plan

- 新增 6 个用例（落点见 Step 1）：`TestCustomSubagentPolicyMissing`（6 行表）、`TestGuardToolRefusesACustomSubagentWithoutAPolicy`、`TestCustomSubagentWithoutPolicySeesNoTools`、`TestUserMessageToDeletedCustomSubagentIsRefused`、`TestUserMessageToConfiguredCustomSubagentRunsUnderItsPolicy`、`TestUserMessageToDisabledCustomSubagentStillRuns`。
- 结构范例：`pkg/run/typedsubagent_llm_test.go:464`（策略与过滤）、`pkg/run/subagent_test.go:4516` 一带（`SendToSubagent` 夹具）。
- 红绿证据写进报告：删除用例在修复前失败（`SendToSubagent = "started", nil`），修复后通过；另两个 `UserMessageTo…` 用例前后都 PASS（钉住现状）。

## Done criteria

- [ ] `go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/process ./pkg/tui ./pkg/gateway ./pkg/architecture -count=1` 全部 `ok`
- [ ] 上面 6 个新用例存在并通过；`TestContinueAfterCustomSubagentDeletedSaysSo` 与 `...DisabledSaysDisabled` 未改且通过
- [ ] `grep -rn '"custom"' pkg/run/subagent_catalog.go pkg/tool/state.go` 只剩 `tool.SubagentDefinitionSourceCustom` 那一处字面量
- [ ] `go vet ./pkg/... ./cmd/...` 与 `gofmt -l cmd pkg third_party` 无输出
- [ ] 与开工前快照比较，没有 Scope 之外的文件变化；没有 commit / 分支 / worktree
- [ ] 报告里附红绿证据与（若做了）真机确认结果

## STOP conditions

- Drift check 任何一条没有输出，或 Current state 的摘录与工作区不符（079 可能已被 owner 改过或提交）。
- Step 2 之后既有测试失败，且失败原因是“某条合法路径带着 custom 来源却没带策略”（说明兜底拦到了真实路径）——**不要放宽兜底**，停下报告是哪条路径。
- 需要改 Scope 之外的文件（例如想在网关里改错误码）。
- 任一步验证连续两次修复后仍失败。
- 你发现“删除后续聊”还有别的入口没经过 `SendToSubagent` / `continueSubagentExecution`（grep `subagentRecordContext(` 的调用者：`SubagentCompactTarget`、`startUserSubagentExecution`、`continueSubagentExecution` 之外若有新调用者，报告）。

## Maintenance notes

- 任何新的“从记录重建子代理上下文”的入口都必须先过 `refuseGoneCustomSubagent`；`GuardTool`/过滤里的 fail-closed 只是兜底，不是入口检查的替代（兜底的症状是“工具全被拒”，用户看到的是难懂的失败）。
- 兜底依赖“自定义运行的上下文里有来源 custom”这一约定：`subagentRecordContext`（`:1814`）与 `typedSubagentExecContext`（`:3163`）都设置它；新增构造上下文的地方要同样设置，否则兜底看不见。
- **已知并接受的语义**（owner 2026-10-19 保持现状）：续聊的工具档位取当前配置——放宽对老记录也生效，收紧立即生效。若将来想要“记录固定其运行时的定义”，思路是：策略与提示词存旁路文件（不要进 `subagent-history.jsonl`，它被整文件读取，见 plan 002），并规定工具取“快照与当前配置较窄者”以保住“收紧立即生效”。本期不做。
- Review 时重点看：`SendToSubagent` 的检查是否在 `setSurface` 之前；`continueSubagentExecution` 重构后两句错误文本是否逐字不变。
