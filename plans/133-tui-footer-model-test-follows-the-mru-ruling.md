# Plan 133：TUI 页脚模型测试改按「新会话跟随上次用的模型」的现行契约

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在 dev 工作区。
> 工作区里若有与本计划无关的未提交改动（例如 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`），
> **不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`grep -n "TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner" pkg/tui/chat_session_test.go`
> 只有 2 条结果（注释行 `:30732`、函数行 `:30739`）；`grep -n "fallback := run.NewSessionModel(base, store)" pkg/tui/chat_session.go`
> 恰好一条（`:1142`）。
>
> **Drift check（先跑）**：`git diff --stat 1c8f5d9 -- pkg/tui/chat_session_test.go pkg/tui/chat_session.go`
> 有输出就对照下面的摘录逐条比对；锚点不符即 STOP 1。
>
> **目标文件必须干净（否则 STOP 1）**：`git status --porcelain pkg/tui/chat_session_test.go pkg/tui/chat_session.go`
> 必须无输出。Step 1 按行号整段替换、Step 3 还要临时改 `chat_session.go`，文件里有别人未提交的改动就会被
> 覆盖，而本计划禁止 `git checkout` 还原。
>
> **基线（Step 1 之前跑一次，否则 STOP 5）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`（约 70 s，超时给足 300 s）
> → 只允许 `--- FAIL: TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner` 这一条，把失败清单记下来。
> 出现别的 FAIL 是环境/别人的存量红，**不是你要修的**。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只改一个测试；产品代码一个字节不动）
- **Depends on**: —（与 134 无依赖，可并行）
- **Category**: tests（存量红测，属 owner 2026-10-23 裁决后未同步的过期契约）
- **Planned at**: dev `1c8f5d9`，2026-10-26
- **已实测（评审方在 `/tmp` 的树副本上跑，未动用户工作区）**：Step 1 给出的测试文本原样落地后
  `-run 'TestFooterNamesWhatAProjectConversationAdopts'` 通过；`./pkg/tui` 整包 `ok`（67.8s，全绿）；
  Step 3 的两次变异都按预期 `FAIL`（`expected "gpt-other"` / `actual "gpt-main"`）。

## Why this matters

`pkg/tui` 里有一个长期红的测试，它编码的是一条**已经被取代的**产品契约，所以它红不是回归、
而是噪音：每次全量回跑都要人工判定"这个红是存量"，评审方在 122–129 批次的终验报告里也只能写
"除两个存量失败外全绿"。更糟的是它无法证明自己原本要守的东西——它的三个候选答案（base runner 的
pin、配置默认、上次用的模型）在测试数据里恰好是同一个值 `gpt-other`，所以即使断言绿了也区分不出
页脚到底读了哪一个。

本计划把这个测试改写成现行契约（owner 2026-10-23 裁决：新会话跟随该 agent **上次用的模型**，
MRU），并制造三方差异，让"页脚绝不读本进程启动时钉住的那个模型"这条原始防护重新变得可证。
做完之后 `pkg/tui` 全绿，且这条测试在有人把 fallback 改回 pin / 配置默认时会立刻变红。

## Current state

### 红的现状（已实测）

```
$ FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestFooterNames' -count=1
--- FAIL: TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner (0.04s)
    chat_session_test.go:30757:
        expected: "gpt-main"
        actual  : "gpt-other"
        Messages: the footer must name the model the project runner will adopt
FAIL
```

### 被测代码（现行，正确，不要改）

`pkg/tui/chat_session.go:1123-1157`（注释 + `sessionModelState`；页脚/`/model` 都读这里）：

```go
// ... 一段注释说明：会话绑了项目时，轮次跑在项目 runner 上，而项目 runner 由 pool
// 从**活配置**在首轮构建；在那之前没有 runner 可读，终端的 base runner 是进程启动时
// 加载的，可能命名一个本会话请求从未用过的模型。
func (s *ChatSession) sessionModelState(sessionID string) run.PrimaryModelState {
	if s == nil || s.Env == nil {
		return run.PrimaryModelState{}
	}
	base := s.runner()
	sid := strings.TrimSpace(sessionID)
	store := s.sessStore()
	// A session with no row yet starts on the agent's most recently used
	// model — the same reading its first message adopts (run.NewSessionModel)
	// — so the footer names what the next turn will actually run on, not the
	// configured default the adoption would have to override. An older
	// session's row appears at its next turn (D9a) and this fallback stops
	// applying the moment it does.
	fallback := run.NewSessionModel(base, store)      // ← :1142
	if !fallback.Set {
		if def, err := run.ConfiguredDefaultSelection(base); err == nil {
			fallback = def
		} else {
			fallback = run.PrimaryModelSelection(base)
		}
	}
	if bound, ok := s.Env.BoundRunnerForSession(context.Background(), sid); ok {
		return run.PrimaryModelStateForSessionOn(bound, store, sid, fallback)
	}
	// No project runner exists yet: the first turn builds it from the live
	// config; the answer above is what that turn adopts — never this
	// process's base-runner pin.
	return run.PrimaryModelStateForSessionOn(base, store, sid, fallback)
}
```

它之所以正确，是因为**轮次入口采纳的正是同一次读法**：`pkg/run/config.go:1930-2013`
（`injectSessionModelSelection`）在会话没有 row 且没有任何消息时：

```go
	// A new conversation: its first message fixes it on the most
	// recently used model, as that session's own row. ...
	adopted := NewSessionModel(r, store)
	if adopted.Set {
		if _, saveErr := store.SaveSessionModelSelection(ctx, sid, state.SessionModelSelection{
			Provider: adopted.Provider, Model: adopted.Model, Effort: adopted.Effort,
		}); saveErr != nil { ... }
		return WithSessionModelSelection(ctx, adopted), nil
	}
```

`RunContent` 在每次轮次开头调用它（`pkg/run/runner.go:1478-1493`），而项目会话的轮次跑在
`RunnerPool.RunnerForTurn` 建出的**项目 runner** 上——同一个 store、同一份活配置，所以项目会话首轮
采纳的就是 MRU。页脚显示 MRU = 与首轮实际行为一致。

`pkg/run/config.go:1753-1777`（`NewSessionModel`，注释即契约）：

```
// NewSessionModel is the model a new session starts on: this primary agent's
// most recently used model (the newest session row among its conversations
// and workshops, provider/model/effort together), resolved against the
// current configuration. A derived default, not a user's choice ...
```

`pkg/run/config.go:2144-2169`（`PrimaryModelStateForSessionOn`）：会话有 row 就用 row，否则返回 caller
给的 fallback。

`pkg/process/runner_pool.go:625-656`（`BoundRunnerForSession`）：会话绑了项目但项目 runner 还没建时
返回 `(nil, false)`，页脚于是走上面代码的最后一行（用 `base` + MRU fallback）。

### 现行测试（过期，要改写）

`pkg/tui/chat_session_test.go:30732-30760`：

```go
// TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner pins the second
// half of the footer fix. ...
// name a model no request of this conversation ever carried. The answer is the
// configured default instead, i.e. what the project runner will adopt.
func TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner(t *testing.T) {
	s := newModelSelectionTestSession(t)
	ctx := context.Background()

	// The base runner is moved to the non-default entry, so reading it here
	// would answer gpt-other.
	require.NoError(t, s.SelectModel(ctx, "sess-a", turn.ModelChoice{Provider: "openai", Model: "gpt-other"}, nil))
	requireRunnerModel(t, s, "openai", "gpt-other", "high")

	// The same agent scope the environment's pool resolves projects under.
	s.Env.Deps.AgentName = "main"
	projects := state.NewProjectStore(s.Env.SQL, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "footer-default", Root: s.home()})
	require.NoError(t, err)
	require.NoError(t, projects.BindSession(ctx, "sess-b", project.ID))

	bound := s.sessionModelState("sess-b")
	require.True(t, bound.Set)
	require.Equal(t, "gpt-main", bound.Model, "the footer must name the model the project runner will adopt")
	s.SetViewingSession("sess-b")
	require.Equal(t, "openai/gpt-main", summarizeComposerModel(s))
}
```

两处毛病：① 期望值来自 2026-10-21 的旧契约（"配置默认"），被 2026-10-24 落地的 MRU 裁决取代；
② 即使把期望改对，测试也**没有区分度**——`SelectModel` 让 base runner 的 pin 也变成 `gpt-other`，
而 MRU 同为 `gpt-other`，"读 pin"和"读 MRU"两种实现都能绿。

### 相关事实与工具（照抄即可用）

- 夹具：`newModelSelectionTestSession`（同文件 `:30396`）写出 `main` agent 的两个 provider 项 —
  `openai/gpt-main`（无 params，effort `""`）与 `openai/gpt-other`（`{"reasoning":{"effort":"high"}}`），
  并调用 `reloadConfigFromDisk`。session store 是 `state.NewSessionStore(db, "main")`，
  所以 MRU 读的是 `main` 这个 agent 的 row。
- `SelectModel`（`pkg/tui/chat_slash.go:667-723`）把 **published effort** 写进 row
  （`:696-701`），所以 sess-a 的 row 是 `(openai, gpt-other, high)`。
- `Runner.SetPrimaryModel(provider, model string, effort *string) error`（`pkg/run/config.go:1597-1606`）
  只动运行时与快照，**不写任何会话 row、不改配置文件**（`/model` 不落盘这条由
  `TestModelChoiceDoesNotLeakToSecondProcess` 守着）。pkg/tui 测试里可直接 `s.runner().SetPrimaryModel(...)`。
- 兄弟测试里已有多条同一契约的断言，可作为写法与措辞的范例：
  `TestNewSessionOpensOnTheMostRecentlyUsedModel`（`:30488`）、
  `TestNewConversationOpensOnTheMostRecentlyUsedModelAndFooterNamesIt`（`:30521`）、
  `TestModelChoiceDoesNotLeakToSecondProcess` 的结尾（`:30987`：`require.Equal(t, "gpt-m2", fresh.sessionModelState("sess-fresh").Model, "the footer of a conversation that never chose")`）。
- 仓库约定：Go 测试用 `testify/require`；测试注释用英文（与本文件其余部分一致）；
  格式化 `gofmt` 必须干净（CI 有 gofmt 门禁，见 `plans/103-ci-gates-gofmt-and-frontend-typecheck.md`）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译并跑本计划相关测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestFooterNames' -v -count=1` | 有 `--- PASS: TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner` 一行、无 FAIL（**必须带 `-v`**：名字写错时 `go test` 只报 `[no tests to run]` 并 exit 0，看着像成功） |
| 整个包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok`，无 FAIL |
| vet | `go vet ./pkg/tui` | exit 0 |
| 格式 | `gofmt -l pkg/tui` | 无输出 |
| 变更范围 | `git status --porcelain pkg/` | 只有 `pkg/tui/chat_session_test.go` |

（**必须**带 `FOREBRAIN_HOME="$(mktemp -d)"`：agent 会话里这个变量指向真实安装，测试会迁移真实状态库。）

## Scope

**In scope**（唯一允许改的文件，最终状态）：
- `pkg/tui/chat_session_test.go`

**临时触碰并必须还原**（Step 3 的变异验证）：
- `pkg/tui/chat_session.go`（只改一行 fallback，验证完立刻还原，收尾时 `git diff` 必须为空）

**Out of scope**（不要动，哪怕看起来相关）：
- `pkg/tui/chat_session.go` 的任何持久改动——现行实现是对的，本计划不改产品代码。如果你读下来认为
  实现有错，**STOP 并报告**，不要自行改。
- `pkg/run/config.go`（`NewSessionModel` / `injectSessionModelSelection` / `ConfiguredDefaultSelection`）。
- `plans/README.md`（评审方维护；里面 122–129 批次记录提到的旧测试名是历史事实，不要改）。
- 同文件里其他模型选择测试（`TestNewSessionOpensOnTheMostRecentlyUsedModel` 等）：它们已经是新契约，绿着。

## Git workflow

不提交。改动留在 dev 工作区，由 owner 手动提交。

## Steps

### Step 1: 用新契约与三方差异改写这个测试

把 `pkg/tui/chat_session_test.go:30732-30760`（旧的注释 + 函数，止于函数结尾那一行 `}`；`30761` 的空行
保留不动）整段替换为下面这段，缩进与空行照抄。
**函数名改了**：旧名断言的是"配置默认"，新名断言的是"首轮会采纳的模型"，别保留旧名。

```go
// TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner pins the
// second half of the footer fix. A conversation bound to a project runs its
// turns on the project runner, which the pool builds from the live
// configuration on the first turn. Before that happens there is no runner to
// read, and the terminal used to answer from its base runner — loaded at
// process start, so it could name a model no request of this conversation ever
// carried. The answer is what that first turn adopts instead: a conversation
// that never chose follows the agent's most recently used model (owner
// ruling, 2026-10-23), resolved against the live configuration.
//
// The three candidate answers are made to differ on purpose: the most
// recently used model is gpt-other, while this process's base-runner pin and
// the configured default are both gpt-main. Either old reading fails here.
func TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner(t *testing.T) {
	s := newModelSelectionTestSession(t)
	ctx := context.Background()

	// sess-a picks the non-default entry: it is now the agent's most recently
	// used model, effort included.
	require.NoError(t, s.SelectModel(ctx, "sess-a", turn.ModelChoice{Provider: "openai", Model: "gpt-other"}, nil))
	requireRunnerModel(t, s, "openai", "gpt-other", "high")

	// The base runner is then put back on the configured default without
	// writing any session row, so "what this process pinned" and "what the
	// newest row says" are no longer the same answer.
	require.NoError(t, s.runner().SetPrimaryModel("openai", "gpt-main", nil))
	requireRunnerModel(t, s, "openai", "gpt-main", "")

	// The same agent scope the environment's pool resolves projects under.
	s.Env.Deps.AgentName = "main"
	projects := state.NewProjectStore(s.Env.SQL, "main")
	project, err := projects.Create(ctx, state.CreateProjectInput{Name: "footer-adopts", Root: s.home()})
	require.NoError(t, err)
	require.NoError(t, projects.BindSession(ctx, "sess-b", project.ID))

	bound := s.sessionModelState("sess-b")
	require.True(t, bound.Set)
	require.Equal(t, "gpt-other", bound.Model, "the footer must name the model the first turn will adopt")
	require.Equal(t, "high", bound.Effort, "the adopted effort travels with the adopted model")
	s.SetViewingSession("sess-b")
	require.Equal(t, "openai/gpt-other", summarizeComposerModel(s))
}
```

如果 `SetPrimaryModel` 在这一行编译不过（签名不同），**STOP 2**，不要自己换招。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner$' -v -count=1`
→ 出现 `--- PASS: TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner`，末行
`ok github.com/forebrain-harness/forebrain-harness/pkg/tui`。只看到 `[no tests to run]` 或看不到那条
`--- PASS` = 函数名/改写没落地，**不是**通过（STOP 3）。

### Step 2: 确认整包的存量红只剩这一条

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`
→ `ok ...`，无 `--- FAIL`。改造前该包唯一失败就是本测试（`TestFooterNames...`），所以现在应为全绿；
出现任何 FAIL → STOP 5（先对照前置基线：基线里没有的 FAIL 是你弄红的，基线里就有的不是你要修的）。

### Step 3: 变异验证——把 fallback 改回 pin，新测试必须变红

这一步是本计划的**核心证据**：证明改写后的测试仍然守得住"页脚绝不读 base runner 的 pin"。

1. 编辑 `pkg/tui/chat_session.go:1142`，把
   `fallback := run.NewSessionModel(base, store)` 临时改成
   `fallback := run.PrimaryModelSelection(base)`。
2. **Verify（期望失败）**：
   `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '^TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner$' -v -count=1`
   → `--- FAIL: TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner`，失败信息为
   `expected: "gpt-other"` / `actual  : "gpt-main"`。
3. **先还原第 1 处变异**：把 `:1142` 改回 `fallback := run.NewSessionModel(base, store)`
   （用 `git status --porcelain pkg/tui/chat_session.go` 应无输出）。
4. 再做第二处：把 `:1142` 那一行**连同它下面 `if !fallback.Set { … }` 整块（`:1143-1149`）**
   一起替换成

   ```go
   	def, _ := run.ConfiguredDefaultSelection(base)
   	fallback := def
   ```

   **Verify（期望失败）**：同一条命令 → `--- FAIL`，同为 `expected: "gpt-other"` / `actual  : "gpt-main"`。
   （两次变异的症状本来就相同，所以第 3 步的"先还原"不能省：否则这一跑可能只是第 1 处变异的红。）
5. **还原**：`git checkout -- pkg/tui/chat_session.go` 是**禁止**的（工作区可能有 owner 未提交改动）。
   用编辑把这一段整块改回原文（两处变异都要还原干净）：

   ```go
   	fallback := run.NewSessionModel(base, store)
   	if !fallback.Set {
   		if def, err := run.ConfiguredDefaultSelection(base); err == nil {
   			fallback = def
   		} else {
   			fallback = run.PrimaryModelSelection(base)
   		}
   	}
   ```

   **Verify**：`git status --porcelain pkg/tui/chat_session.go` → 无输出（文件回到未改动状态）。
   非空就再逐字对照上面的原文块改一次；仍非空 → STOP 1，**不要**用 `git checkout` / `git restore` 覆盖。

### Step 4: 收尾门禁

**Verify**（四条都要过）：

1. `gofmt -l pkg/tui` → 无输出
2. `go vet ./pkg/tui` → exit 0
3. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`
4. `git status --porcelain pkg/` → 只有 ` M pkg/tui/chat_session_test.go`

## Test plan

- 改写（不是新增）`pkg/tui/chat_session_test.go` 里的
  `TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner` →
  `TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner`。
- 覆盖的用例：项目绑定的会话、项目 runner 尚未建、该会话没有 row → 页脚答 MRU（`gpt-other`/`high`），
  且 `summarizeComposerModel` 输出 `openai/gpt-other`。
- 反例覆盖靠 Step 3 的两次变异（读 pin → 红；读配置默认 → 红）。
- 结构范例：同文件 `TestNewConversationOpensOnTheMostRecentlyUsedModelAndFooterNamesIt`（`:30521`）。

## Done criteria

- [ ] `grep -n "TestFooterNamesTheConfiguredDefaultBeforeAnyProjectRunner" pkg/tui/chat_session_test.go` 无输出
- [ ] `grep -n "TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner" pkg/tui/chat_session_test.go` 恰好 2 条：头一条是注释行（`// TestFooterNamesWhatAProjectConversationAdoptsBeforeAnyProjectRunner pins …`），第二条是 `func` 行（本文件的惯例是注释以测试名开头，别为了"只留 1 条"去改注释）
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿
- [ ] Step 3 的两次变异都为 FAIL，且 `git status --porcelain pkg/tui/chat_session.go` 事后为空
- [ ] `gofmt -l pkg/tui` 空、`go vet ./pkg/tui` exit 0
- [ ] `git status --porcelain pkg/` 只有 `pkg/tui/chat_session_test.go` 一个文件

## STOP conditions

1. **STOP 1**：前置 grep 的锚点数量不符，或 `pkg/tui/chat_session.go:1142` 的 fallback 不是
   `run.NewSessionModel(base, store)`，或前置的 `git status --porcelain` 显示两个目标文件不干净
   （= 有人在计划写完后改过实现/文件）。
2. **STOP 2**：`Runner.SetPrimaryModel` 的签名与本文不符，或它在这一夹具上返回错误。
3. **STOP 3**：Step 1 的 `-run` 里看不到 `--- PASS:`，或断言得到的值与计划不符（例如 `bound.Effort`
   不是 `"high"`）。**报告实测值并停下，禁止把断言改成实测值来变绿**——那样测试就失去区分度了。
4. **STOP 4**：改写后测试仍要求 `gpt-main` 才绿（说明 MRU 裁决在本仓库被推翻/改写——那是产品决定，交 owner）。
5. **STOP 5**：整包基线或回归里出现与本用例无关的 FAIL（环境/别人的存量红）：报告清单，不要修。
6. **STOP 6**：Step 3 的变异**不变红**（说明新测试没有区分度，改写无效）：停下报告，不要加断言糊过去。
7. **STOP 7**：需要改动本计划 Scope 之外的文件。

## Maintenance notes

- 这条测试钉的是"页脚 = 首轮会采纳的模型"。若将来 owner 再次改"新会话开在哪个模型上"
  （例如改回配置默认、或加"按项目记忆模型"），**这个测试必须与
  `pkg/run/config.go:injectSessionModelSelection` 的采纳逻辑一起改**——两者是同一份契约的两端，
  只改一端就会重现本次这种"红测说不清是谁错"的状态。
- 评审要点：确认测试里的三方取值仍然互不相同（MRU `gpt-other` ≠ pin `gpt-main` ≠ 配置默认 `gpt-main`）。
  若夹具以后把 `gpt-main` 的 effort 改成非空，`requireRunnerModel(t, s, "openai", "gpt-main", "")` 会红，
  那是夹具漂移，按实际值更正即可，但**不要**把三方搞成同一个值。
- 本计划刻意不改产品代码：`sessionModelState` 的现行实现与轮次入口一致。