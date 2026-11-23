# Plan 290: 重建子代理模型 pin 时保留 `Caps`，让行的能力真正作用到子代理自己的请求

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/run/subagent.go pkg/run/subagent_test.go && git diff d715dafd -- pkg/run/subagent.go pkg/run/subagent_test.go`
> 若两个文件在 `d715dafd` 之后已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。
> 注意 `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

计划 283 新增了 `subagent_run` / `subagent_send` / `subagent_fanout` 的 `model` 参数：它只接受模型**行 id**，派发时实时读 `fb_llm_models` 那一行，并明确要求「子代理这次执行的所有请求、压缩预算、图片判定、输出上限都以这一行为准」，包括本轮配置快照采纳之后才新增的行。实现方式是 `state.ModelOverrideFromCaps(...)` 把该行能力投影塞进 `SubagentModelOverride.Caps`，`Runner.modelHitFor(ctx)` 是 `Caps` 的唯一读取者。

但派发链上有两处**重建** pin 的地方只复制了 `ModelID` / `Entry` / `Version`，把 `Caps` 丢了：
- `subagentExecContext(prep)` 从 `prep.ctx` 读原 pin，另造一个新 pin 并 `WithSubagentModelOverride(hostCtx, pin)` 覆盖 —— 子代理自己的 LLM 调用用的正是这个 `execCtx`（`subagent.go:2150-2151`）。
- `subagentRecordPin(...)` 续跑时重读该行，也只投影六个字段。

后果要看两段：`agentModelRowFor(execCtx)` 会回退到 pin 的 `ModelID`（`pkg/run/config.go:2507-2512`），所以**已经装进进程能力表**的行仍能被 `llm.LookupRow` 读到设置 —— 那一段不会退化。真正的缺口是 `modelHitFor` 自己注释里点名的那个窗口：「A dispatch-time override carries the capabilities of the row it read live — **the row may have been added after this turn's configuration was adopted** — so that projection is used instead of the installed table.」也就是说，本轮配置采纳之后才新增或被编辑的行，安装表里没有它（或只有旧值），`Caps` 正是为这个窗口准备的投影；丢掉它，子代理的上下文窗口 / 最大输出 / 图片判定 / 压缩触发点就退回目录值或旧值。这正是 `Caps` 设计出来要避免的场景。

现有测试 `pkg/run/compaction_test.go:967-1015`（`TestModelHitForFollowsTheConfiguredRow`）是手写 `WithSubagentModelOverride` 直接注入 Caps，绕过了这两条生产路径，所以测不到。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26，CGO，`-tags fts5`）。

- `pkg/run/subagent.go`
  - `:336-354` `preparedSubagent` 含 `ctx context.Context`、`entry agent.HistoryEntry`。
  - `:1977-2007`（派发时）：
    ```go
    pin := SubagentModelOverride{}
    if override, ok := SubagentModelOverrideFromContext(baseCtx); ok {
        pin = override
    } else if ... { ... }
    if pin.valid() {
        entry.ModelProvider = strings.TrimSpace(pin.Provider)
        entry.Model = strings.TrimSpace(pin.Model)
        entry.ModelID = strings.TrimSpace(pin.ModelID)
        entry.ReasoningEffort = strings.TrimSpace(pin.Effort)
        entry.ModelLive = pin.Entry != nil
        cctx = WithSubagentModelOverride(cctx, pin)
    }
    ```
    即 `prep.ctx` 上的原 pin **带着 `Caps`**（`SubagentModelOverrideFromContext` 返回整个结构体）。
  - `:2150-2151`：`execCtx := subagentExecContext(prep)`，随后 `runSubagentExecution(withSubagentAwaited(execCtx), ...)` —— 子代理自身请求走这个 ctx。
  - `:4230-4243`（缺陷一，本分支 `git diff d715dafd` 里 `@@ -4053,6 +4232,10 @@` 只加了 4 行）：
    ```go
    pin := SubagentModelOverride{
        Provider: prep.entry.ModelProvider,
        Model:    prep.entry.Model,
        Effort:   prep.entry.ReasoningEffort,
    }
    if dispatched, ok := SubagentModelOverrideFromContext(prep.ctx); ok && strings.TrimSpace(dispatched.Model) == strings.TrimSpace(pin.Model) {
        pin.ModelID = dispatched.ModelID
        // The live row the dispatch resolved rides along: the client for this
        // run is built from that row, and the cache keys on its version.
        pin.Entry = dispatched.Entry
        pin.Version = dispatched.Version
    }
    hostCtx = WithSubagentModelOverride(hostCtx, pin)
    ```
  - `:2396-2425`（缺陷二，续跑重读行）：
    ```go
    pin = SubagentModelOverride{
        ModelID:  modelID,
        Provider: record.ModelProvider, Model: record.Model, Effort: record.ReasoningEffort,
    }
    if !record.ModelLive || modelID == "" {
        return pin, nil
    }
    reread, err := usableModelByID(ctx, fac, modelID)
    if err != nil { return SubagentModelOverride{}, err }
    entry := reread.Entry
    return SubagentModelOverride{
        ModelID:  strings.TrimSpace(entry.ID),
        Provider: strings.TrimSpace(entry.Provider),
        Model:    strings.TrimSpace(entry.Model),
        Effort:   appcfg.ReasoningEffort(entry.Params),
        Entry:    &entry,
        Version:  reread.Row.UpdatedAt,
    }, nil
    ```
    `reread.Row` 是完整的 `state.LLMModel`，带全部 `Cap*` 列，却没有被投影。
  - `:4413-4442`（唯一生产者 `subagentModelOverrideForRef`）：
    ```go
    configRow, err := usable.Row.ConfigRow()
    if err != nil { return SubagentModelOverride{}, err }
    capsOverride, hasCaps := state.ModelOverrideFromCaps(configRow.Caps)
    var caps *llm.ModelOverride
    if hasCaps { caps = &capsOverride }
    return SubagentModelOverride{ ..., Caps: caps }, nil
    ```
  - `:4274-4276`：`func (o SubagentModelOverride) valid() bool { return strings.TrimSpace(o.Model) != "" }`。
  - `:4245-4271`：结构体定义，`Caps *llm.ModelOverride` 在 `:4271`（本分支新增字段）。

- `pkg/run/config.go:2534-2545`（唯一读取者）：
  ```go
  func (r *Runner) modelHitFor(ctx context.Context) (llm.Hit, bool) {
      provider, model := r.agentModelFor(ctx)
      if override, ok := SubagentModelOverrideFromContext(ctx); ok && override.Caps != nil {
          hit, _ := llm.LookupCatalog(provider, model)
          return llm.OverlayModelOverride(hit, *override.Caps), true
      }
      return llm.LookupRow(r.agentModelRowFor(ctx), provider, model)
  }
  ```

- `pkg/state/config_store.go`
  - `:470-...` `func (m LLMModel) ConfigRow() (appcfg.LLMModelConfig, error)`：`ParseLLMRequestParamsJSON(m.ParamsJSON)` 后填 `Caps: appcfg.LLMModelCaps{ ..., ContextWindow: m.CapContextWindow, MaxOutput: m.CapMaxOutput }`。空 `ParamsJSON` 返回 `nil, nil`，不报错。
  - `:505-522` `func ModelOverrideFromCaps(caps appcfg.LLMModelCaps) (llm.ModelOverride, bool)`：全 nil 时返回 `false`；否则把九个能力字段投影成 `llm.ModelOverride`。
  - `:425-455` `type LLMModel struct`：`CapContextWindow *int64`、`CapMaxOutput *int64` 等；`nil = catalog`。

测试样板（**必须照这些写，不要新建 `_test.go` 文件** —— `pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles` 要求 `X_test.go` 必须有 `X.go`）：
- `pkg/run/compaction_test.go:967-1015` `TestModelHitForFollowsTheConfiguredRow`：`llm.SetModelOverrides(...)` + `t.Cleanup`、宽/窄两行同名、断言 `r.modelHitFor(ctx).ContextWindow`。
- `pkg/run/subagent_test.go:7180-7200`：用 `dispatchSubagent(dispatchTestCtx(), fac, subagentDispatch{ run: func(_, _, prep preparedSubagent) { execCtx = subagentExecContext(prep) ... } })` 拿到真实 `prep` 再调 `subagentExecContext`。
- `pkg/run/subagent_test.go:8347-8395`：`newModelArgumentFixture(t, cfg)`（真实 SQLite 状态库 + `state.LLMModelStore`）、`(*modelArgumentFixture).seedModelRow(t, provider, model, baseURL, key, allowed)`（**是方法，接收者 `fix`**，不是包级函数）、`dispatchTestCtx()`（带 session id `"session-1"`）。

仓库约定：
- 能力表按**行 id** 建键；`llm.Lookup(provider, model)` 在同名多行时只给目录值，这是计划 283 明确的有意回退，**不要改**。
- 观测（`pkg/llm/observed.go`）只在内存、旧导出 API 签名不变 —— 不动。
- `pkg/config` 不 import `pkg/llm`；不要在 `pkg/config` 里加共享投影（会新增 CI 包图边，`scripts/package-graph.sh` 会红）。`pkg/state` 已同时 import 两者，是唯一合适的投影位置。
- 注释、日志与文案沿用英文；`gofmt` 必须干净。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation$' -count=1 -v` | `--- PASS: TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation` |
| 回归包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/llm ./pkg/assembly ./pkg/state -count=1` | 全部 `ok` |
| 架构规则 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0，无输出 |
| 格式 | `gofmt -l pkg cmd` | 无输出 |

## Scope

**In scope**（只允许改这两个文件）：
- `pkg/run/subagent.go`
- `pkg/run/subagent_test.go`

**Out of scope**（不许动）：
- `pkg/run/config.go`、`pkg/llm/catalog.go`、`pkg/llm/observed.go`、`pkg/state/config_store.go` —— 只读，本计划不改它们的签名。
- `pkg/assembly/*`、`pkg/process/*` —— 另一个装配点缺口由 `291` 单独处理。
- `subagentRecordContext` 的错误传播 / 双次读库 —— 由后续计划处理（`290` 只补字段投影，不改控制流）。
- 任何用户可见文案、工具定义与 `model` 参数说明文字（这些是逐字定稿，改了会漂移 prompt 前缀）。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `fix(run): carry the model row's caps through the subagent pin`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

> **执行顺序（必读）**：本计划的测试是 **Step 3**，但必须**先写、先跑红**，再改生产代码。
> 顺序：Step 3（写测试 → 跑一次，确认 `execCtx` 与 `recordPin` 两处 `Caps == nil` 的红）
> → Step 1、Step 2（修复）→ 再跑 Step 3 的验证取绿 → Step 4（收口回归）。
> 红/绿两次输出都要进执行报告；先做 Step 1/2 会让「修复前必须失败」这条 Done criterion 无法成立。

### Step 1: 让 `subagentExecContext` 把 `Caps` 一起带过去

在 `pkg/run/subagent.go` 的 `subagentExecContext` 里，`if dispatched, ok := ...` 分支中，紧跟 `pin.Version = dispatched.Version` 之后加一行：

```go
pin.Caps = dispatched.Caps
```

`Caps` 是 `*llm.ModelOverride`，直接复制指针即可（它是不可变投影，不要深拷贝、不要新建对象）。**不要**改动分支条件或 `WithSubagentModelOverride` 的调用方式。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0；`grep -n 'pin.Caps = dispatched.Caps' pkg/run/subagent.go` 有 1 处命中。

### Step 2: 让续跑重读行也投影 `Caps`

在 `subagentRecordPin` 的 `reread, err := usableModelByID(...)` 成功之后，把当前直接 `return SubagentModelOverride{...}, nil` 改成先投影：

```go
    configRow, err := reread.Row.ConfigRow()
    if err != nil {
        return SubagentModelOverride{}, err
    }
    capsOverride, hasCaps := state.ModelOverrideFromCaps(configRow.Caps)
    var caps *llm.ModelOverride
    if hasCaps {
        caps = &capsOverride
    }
    return SubagentModelOverride{
        ModelID:  strings.TrimSpace(entry.ID),
        Provider: strings.TrimSpace(entry.Provider),
        Model:    strings.TrimSpace(entry.Model),
        Effort:   appcfg.ReasoningEffort(entry.Params),
        Entry:    &entry,
        Version:  reread.Row.UpdatedAt,
        Caps:     caps,
    }, nil
```

`state` 与 `llm` 在本文件已 import（现有代码用了 `state.LLMModelStore`、`llm.FormatProviderModel`），不需要新增 import。若 `ConfigRow()` 报错就照 `subagentModelOverrideForRef` 的写法返回 error —— 不要吞。

注意：`!record.ModelLive || modelID == ""` 的早退分支仍返回不带 `Caps` 的 pin，这是对的（没有行就没有能力可投影），**不要**给它硬塞空 `Caps`。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0；`grep -n 'Caps:     caps' pkg/run/subagent.go` 至少 2 处命中（`subagentModelOverrideForRef` 与 `subagentRecordPin`）。

### Step 3: 补一条走真实生产路径的回归测试

在 `pkg/run/subagent_test.go` 末尾新增 `func TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation(t *testing.T)`。要求：

1. `fix := newModelArgumentFixture(t, subagentsEnabledConfig())`。
2. 用 `fix.store.Create(context.Background(), state.LLMModel{Provider: "zhipuai", Model: "glm-5.3", BaseURL: "https://caps.test/v1", APIPath: "/chat/completions", APIKey: "key-caps", SubagentAllowed: true, CapContextWindow: &window})` 插一行，其中 `window := int64(32000)`；记下 `row.ID`。`state.LLMModel.CapContextWindow` 是 `*int64`（`pkg/state/config_store.go:443`）。
3. `ctx := dispatchTestCtx()`，用 `pin, err := subagentModelOverrideForRef(ctx, fix.fac, row.ID)` 得到带 `Caps` 的原 pin；断言 `pin.Caps != nil && pin.Caps.ContextWindow != nil && *pin.Caps.ContextWindow == 32000`。
4. **执行路径**：把 `WithSubagentModelOverride(ctx, pin)` 作为 `dispatchSubagent` 的第一个参数（样板见 `subagent_test.go:7180-7200`），在 `run` 回调里调 `execCtx := subagentExecContext(prep)`，并断言：
   - `got, ok := SubagentModelOverrideFromContext(execCtx)` 为 `ok`；
   - `got.Caps != nil && got.Caps.ContextWindow != nil && *got.Caps.ContextWindow == 32000`；
   - `hit, ok := fix.fac.Owner.modelHitFor(execCtx)` 为 `ok && hit.ContextWindow == 32000`（用 fixture 自带的 `Runner`；它带 `Deps`，且 `agentModelFor` 会先读 ctx 上的 pin，所以这里读到的一定是这一行，不是会话默认模型）。
5. **续跑路径**：构造 `agent.HistoryEntry{ModelProvider: "zhipuai", Model: "glm-5.3", ModelID: row.ID, ModelLive: true}`，调 `subagentRecordPin(ctx, fix.fac, record)`，断言返回的 `pin.Caps` 同样带 32000。
6. 用 `t.Cleanup` 复位任何全局状态（若用到 `llm.SetModelOverrides`）；本测试主要靠真实 DB 行，尽量不要引入全局 override，避免与并行用例互相干扰。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation$' -count=1 -v` → `--- PASS`。**修复前该测试必须失败**（先写测试跑一次，确认 `execCtx` 与 `recordPin` 两处 `Caps == nil` 的红，再实施 Step 1/2）；把红/绿两次输出都记进执行报告。

### Step 4: 收口回归

**Verify**（全部要跑）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/llm ./pkg/assembly ./pkg/state -count=1` → 全部 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → 无输出
- `gofmt -l pkg cmd` → 无输出

## Test plan

- 新测试：`pkg/run/subagent_test.go` 的 `TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation`，覆盖：
  - happy path：`subagentModelOverrideForRef` 从真实 DB 行读出 `Caps`；
  - 回归一（本计划修的 bug）：`subagentExecContext(prep)` 产出的 `execCtx` 仍带 `Caps`，且 `modelHitFor(execCtx)` 返回该行窗口；
  - 回归二：`subagentRecordPin` 续跑重读行仍带 `Caps`；
  - 边界：同名两行分别设 32000 / 64000 时，`execCtx` 拿到的是**被派发那一行**的窗口（不是第一行的）。
- 结构样板：`pkg/run/subagent_test.go:7180-7200`（dispatch + execContext）与 `pkg/run/compaction_test.go:967-1015`（能力断言）。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation$' -count=1 -v` → PASS；禁止用 `-run` 零匹配当证据。

## Done criteria

- [ ] `grep -c 'pin.Caps = dispatched.Caps' pkg/run/subagent.go` = `1`
- [ ] `grep -c 'Caps:     caps' pkg/run/subagent.go` ≥ `2`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation$' -count=1 -v` 输出 `--- PASS` 且只有 1 条 RUN（真跑了）
- [ ] 修复前该新用例失败、修复后通过，两次证据都在执行报告里
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/llm ./pkg/assembly ./pkg/state -count=1` 全 `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`CGO_ENABLED=1 go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 全绿
- [ ] `git status --porcelain -- pkg/run` 只列出 `subagent.go` 与 `subagent_test.go`
- [ ] `plans/README.md` 中本计划状态行已更新

## STOP conditions

- Drift check 显示两个文件在 `d715dafd` 之后被改过，且摘录对不上。
- `SubagentModelOverride` 上找不到 `Caps *llm.ModelOverride` 字段，或 `state.ModelOverrideFromCaps` 不存在。
- `subagentExecContext` 的条件已经不是 `strings.TrimSpace(dispatched.Model) == strings.TrimSpace(pin.Model)` —— 说明重建逻辑变过，先报告再改。
- 新用例必须放宽既有 `pkg/run` 用例的断言才能通过（例如 `TestSubagentModelArgumentPinsTheNamedRow` 的 pin 断言）。
- 修复需要新增 import 边（尤其 `pkg/config → pkg/llm`），或 `pkg/architecture` / `scripts/package-graph.sh` 变红 —— 本计划只改两处字段投影，不应产生新边。
- 同一验证连续两次失败。

## Maintenance notes

- 以后给 `SubagentModelOverride` 再加字段时，必须同时检查**三个**投影点：`subagentModelOverrideForRef`（生产者）、`subagentExecContext`（派发→执行）、`subagentRecordPin`（记录→续跑）。本计划的 bug 正是新增 `Caps` 时漏了后两个。CR 时把这三点一起看。
- `modelHitFor` 目前是 `Caps` 唯一读取者；若将来别的读者也读 `Caps`，要确认它们拿到的是 `execCtx` 而不是 `prep.ctx`。
- 明确延后、不在本计划：`subagentRecordContext` 吞掉 `subagentRecordPin` 错误、`continueSubagentRecord` 的双次查库（TOCTOU）、异步 `startSubagentContinuation` 缺前置校验 —— 那是同一段代码的控制流改动，风险更高，单独立项 `294`（硬依赖本计划先落地）。
- `291` 会补 `CompactionService` 的 `ModelHit` / `ModelRow` 装配，与本计划同属「行的能力要到位」但文件不重叠，可并行。
