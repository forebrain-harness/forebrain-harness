# Plan 297: 探路 —— 把通用 `model` 行参数复用到 `/goal` 检查子代理

> **Executor instructions**：这是一份 **spike / 设计计划，不是实施计划**。
> 目标是产出一份可裁决的设计结论 + 一个证明可行性的原型测试；**不要实现产品功能、不要改任何生产 `.go` 文件**。
> 按步骤执行，每步都跑验证命令并确认预期输出；遇到「STOP 条件」停下报告。完成后更新 `plans/README.md` 的本计划状态行。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/run/goal_test.go && git diff d715dafd -- pkg/run/goal_test.go`
> 若该文件在 `d715dafd` 之后已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。
> `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P3（方向项；先出结论，再决定要不要立项实现）
- **Effort**: S–M（spike）
- **Risk**: LOW（不碰生产代码）
- **Depends on**: none（`290` 的 `Caps` 投影是后续实现的前置，但本 spike 不依赖它）
- **Category**: direction
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

计划 283 把「按行 id 实时查库 → 客户端与能力读数 → 钉进派发上下文」收敛成一条可复用链路（`usableModelByID` → `subagentModelOverrideForRef` → `WithSubagentModelOverride`），并在非功能要求里明确写下 **stated-but-undelivered 的意图**：

> `plans/283-plan-approval-hand-off-and-review-again.md:1002`
> 「**可扩展**：`model` 参数是通用能力，后续别的流程（如 `/goal`）可直接用；`HandOffAvailable` 的判断集中在 gate，换条件只改一处。」
> `:1020` 的验证行也写明「新增别的流程复用 `model` 参数不改工具定义（`grep -c 'description=Optional id of a model from list_usable_models' pkg/run/subagent.go` = `3` 恒定）」。

而 `/goal` 的验收检查子代理（`pkg/run/subagent.go:4910-4930` `runGoalCheck`）今天直接 `dispatchSubagent`，`subagentDispatch` 不带 model 解析，检查子代理只能沿会话 / 类型的模型链走。产品价值：用户可让 `/goal` 的验收检查跑在更便宜或已授权的行上，不必切换整个会话模型；与刚落地的 `subagent_allowed` 授权开关保持同一套成本语义。

**但有一个必须先裁决的语义冲突**：`runGoalCheck` 的注释明确写「runs whether or not the model may spawn subagents, because the user asked for it with /goal」——它**有意绕过** `enable_subagent` 开关。若复用 `usableModelByID`，默认会撞上 `subagent_allowed` 校验。「用户亲自发起 `/goal`」算不算用户亲自选模型、要不要再绕过第二道开关，是策略问题，属于 maintainer 而非执行者。本 spike 的结论就是把这个（以及其余开放问题）摆清楚，并证明技术上确实只差一层注入。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26，CGO，`-tags fts5`）。

- **现成链路（计划 283 新增，只读）**
  - `pkg/run/subagent.go:4351-4386` `usableModelByID(ctx, fac, id)`：只认行 id；未授权 / 无凭据 / 无此行三种定稿拒绝文案；`handOffModelID(ctx, fac)` 命中的行走交接例外（不看开关、仍看凭据）。
  - `pkg/run/subagent.go:4413-4442` `subagentModelOverrideForRef(ctx, fac, id)`：读到的行 → `SubagentModelOverride`（含 `Caps`、`Entry`、`Version`）。
  - `pkg/run/subagent.go:4448-4456` `WithSubagentModelOverride(ctx, override)`。
  - `pkg/run/subagent.go:415-419`：`subagent_run` 工具 handler 的实际用法 —— `pin, err := subagentModelOverrideForRef(ctx, fac, in.Model)` → `ctx = WithSubagentModelOverride(ctx, pin)` → `execGeneralSubagent(ctx, ...)`。
  - `pkg/run/subagent.go:1977-2007`：`prepareSubagentExecutionResolved` 里
    ```go
    pin := SubagentModelOverride{}
    if override, ok := SubagentModelOverrideFromContext(baseCtx); ok {
        pin = override
    } else if ... { ... }
    if pin.valid() { ...; cctx = WithSubagentModelOverride(cctx, pin) }
    ```
    —— 即**任何调用方只要把带 pin 的 ctx 传进 `dispatchSubagent`，整条子代理链就已经按那一行跑**。这就是「相邻可能」的技术依据。
  - `pkg/run/subagent.go:4274-4276` `valid()` 只要求 `Model != ""`。
  - `pkg/run/subagent.go:4318-4345` `usableModels`（`list_usable_models` 的数据源）：只列已授权且有凭据的行，外加本会话交接例外行。

- **`/goal` 检查链路（只读）**
  - `pkg/run/goal.go:99-100` `const GoalCheckSubagentType = "goal-evaluator"`；`:107-121` `goalCheck.Evaluate(ctx, objective, rounds)` → `c.runner.runGoalCheck(ctx, goalCheckTask(objective, rounds))`。
  - `pkg/run/subagent.go:4904-4930` `runGoalCheck`：
    ```go
    // runGoalCheck dispatches a goal's check as a subagent of the turn working
    // toward it ... except that it is reserved (absent from the enum the model
    // sees) and runs whether or not the model may spawn subagents, because the
    // user asked for it with /goal.
    func (r *Runner) runGoalCheck(ctx context.Context, task string) (output, agentID string, err error) {
        if r.SubagentExecutor == nil { return "", "", fmt.Errorf("the goal check requires the in-process subagent executor") }
        prep, output, err := dispatchSubagent(ctx, r.subagentFactory(), subagentDispatch{
            taskID: newSubagentTaskID(), title: "Goal check", task: task,
            subtype: GoalCheckSubagentType, resolve: agent.ResolveSubtype,
            run: func(runCtx context.Context, fac Factory, prep preparedSubagent) (string, error) { ... },
        })
        return output, subagentRosterKey(prep.entry), err
    }
    ```
  - `pkg/run/subagent.go:3878-3899` `subagentDispatch` 结构：`taskID/title/task/subtype/worktree/resolve/run`，**没有** model 字段。
  - `pkg/run/goal.go:81-83` `Evaluator.Evaluate(ctx, ...)`；`:200-240` 的 `/goal` 循环调用 `Evaluate(ctx, ...)`（ctx 来自目标轮，带会话身份）。
  - `pkg/tui/reducer.go:1612,1694` 按 `run.GoalCheckSubagentType` 呈现检查子代理视图。

- **原型测试样板（`pkg/run/goal_test.go`，只读）**
  - `:595-618` `TestGoalCheckDispatchesTheEvaluatorSubagent`：`dispatchProbe` + `fixedSubagentExecutor` + `&Runner{Deps: &Deps{Home: t.TempDir()}, Events: probe, SubagentExecutor: executor}`，调 `goalCheck{runner: runner}.Evaluate(dispatchTestCtx(), ...)`，断言 `executor.subagentType == GoalCheckSubagentType`、`probe.spawned[0].AgentType` 等。
  - `pkg/run/subagent_test.go:2194-2207` `fixedSubagentExecutor` 有 `model` / `pinnedModel` 字段；`:2262+` `RunSubagentExec` 会记录它们；`dispatchProbe.spawned`（`:43-47`）带 `Provider/Model/Effort`。原型断言用得上。
  - `pkg/run/subagent_test.go:84-86` `dispatchTestCtx()` = `llm.WithAgentSessionID(context.Background(), "session-1")`。

仓库约定 / 硬约束：
- **`model` 参数是行 id，不是 provider/model**；同名多行合法（计划 283 owner 定稿）。
- **工具定义必须字节稳定**：新增能力不得改变主代理请求前缀（`plans/283...:1005` 的「前缀缓存」行）；`list_usable_models` 的结果只能追加在对话末尾。
- `subagent_allowed` 默认关、迁移把旧行全置 0；交接例外只放行本会话交接记录指名的那一行。
- `pkg/config` 不 import `pkg/llm`；共享投影只能放 `pkg/state`（避免新增 CI 包图边）。
- `pkg/architecture`：`X_test.go` 必须有 `X.go`、每包 ≤30 个生产文件、import 分层。原型测试写进既有 `goal_test.go`。
- `gofmt` 干净；注释用英文。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 原型测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalCheckCanBePinnedToAConfiguredRow$' -count=1 -v` | `--- PASS` |
| 既有 goal 回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalCheck' -count=1 -v` | 全部 `--- PASS` |
| 全包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok` |
| 架构规则 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 / 格式 | `CGO_ENABLED=1 go build -tags fts5 ./...` / `gofmt -l pkg cmd` | exit 0 / 无输出 |
| 生产代码零改动 | `git status --porcelain -- pkg/run/*.go \| grep -v '_test.go'` | 无输出 |

## Scope

**In scope**（只允许这两项）：
- `pkg/run/goal_test.go` —— 新增**一个**原型/表征测试（不改既有断言）。
- 本计划文件 `plans/297-goal-check-model-row-spike.md` —— 填写文末 `## Spike findings` 各节（执行者写结论）。

**Out of scope**（绝对不许动）：
- 任何生产 `.go` 文件（`pkg/run/subagent.go`、`goal.go`、`pkg/tool/*`、`pkg/tui/*`、`pkg/gateway/*`、`pkg/config/*`、`pkg/state/*`）。
- 工具定义、`model` 参数说明、`list_usable_models` 的文案与三种派发错误文案（逐字定稿）。
- CI 配置、前端、原型 HTML、i18n。
- 不新增文件（除本计划里已存在的章节填写）；不引入新依赖。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `test(run): spike pinning the goal check to a configured model row`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

### Step 1: 只读核对，把事实写进「Spike findings」

逐条打开上面「Current state」列出的位置，确认：
1. `prepareSubagentExecutionResolved` 确实优先采用 `SubagentModelOverrideFromContext(baseCtx)`；
2. `goalCheck.Evaluate` 的 ctx 从 `/goal` 循环一路传到 `runGoalCheck`，中间没有被 `context.Background()` 换掉；
3. `runGoalCheck` 的注释与「绕过 enable_subagent」这句仍然成立；
4. `usableModelByID` 的三条拒绝条件与交接例外的确切位置；
5. `list_usable_models` 与工具定义的当前字节（`grep -c 'Optional id of a model from list_usable_models' pkg/run/subagent.go` = `3`）。

把每条的 `file:line` 与结论写进本计划文末 `## Spike findings` 的「Facts verified」表格。任一条与描述不符 → STOP。

**Verify**：`grep -c 'Optional id of a model from list_usable_models' pkg/run/subagent.go` = `3`；`grep -n 'SubagentModelOverrideFromContext(baseCtx)' pkg/run/subagent.go` 有 1 处命中。

### Step 2: 写一个「只靠 ctx 就能钉到行」的原型测试

在 `pkg/run/goal_test.go` 末尾新增 `func TestGoalCheckCanBePinnedToAConfiguredRow(t *testing.T)`，照 `TestGoalCheckDispatchesTheEvaluatorSubagent`（`:595-618`）建 fixture，**不改任何生产代码**：

```go
func TestGoalCheckCanBePinnedToAConfiguredRow(t *testing.T) {
    probe := &dispatchProbe{}
    executor := &fixedSubagentExecutor{output: "{\"status\": \"done\", \"why\": \"pinned\"}"}
    runner := &Runner{Deps: &Deps{Home: t.TempDir()}, Events: probe, SubagentExecutor: executor}
    pin := SubagentModelOverride{
        ModelID: "row-cheap", Provider: "deepseek", Model: "deepseek-v4-flash",
    }
    ctx := WithSubagentModelOverride(dispatchTestCtx(), pin)
    verdict, err := goalCheck{runner: runner}.Evaluate(ctx, "make the tests pass", []RoundOutcome{{Index: 0, Text: "fixed the parser"}})
    if err != nil { t.Fatalf("Evaluate: %v", err) }
    if verdict.Status != StatusDone { t.Fatalf("verdict = %+v", verdict) }
    // The pin reached the dispatched goal check without a single production change.
    // Assert on whichever observation the fixture actually records — read
    // pkg/run/subagent_test.go:2194-2207 and :2262+ first.
    // e.g. executor.pinnedModel / executor.model / probe.spawned[0].Model
    ...
}
```

要求：
- **必须先读** `fixedSubagentExecutor.RunSubagentExec`（`pkg/run/subagent_test.go:2262` 起）确认它到底记录 `model` 还是 `pinnedModel`、值来自 `req.Model` 还是 `SubagentModelOverrideFromContext`；按真实情况写断言，不要猜字段。
- 断言至少覆盖：pin 的 `ModelID` / `Model` 之一确实被这次 goal check 采用；且 `probe.spawned` 仍只有 1 条、`AgentType == GoalCheckSubagentType`（证明没把检查子代理换成别的类型）。
- 再加一条负向对照：不注入 pin 时 `TestGoalCheckDispatchesTheEvaluatorSubagent` 的既有行为不变（该用例本身已覆盖，不要改它，只在报告里说明）。
- 测试名与注释都要写明这是 **spike / characterization**，不是产品功能。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalCheckCanBePinnedToAConfiguredRow$' -count=1 -v` → `--- PASS`。若测试无法在不改生产代码的前提下通过，说明「只差一层注入」的前提不成立 —— STOP 并如实记录（这本身就是有价值的 spike 结论）。

### Step 3: 把开放问题与建议写进「Spike findings」

在本计划文末 `## Spike findings` 里逐项填完（每项都要给 `file:line` 依据与「建议 / 备选 / 待 owner 裁决」标记）：

1. **行 id 从哪来**：`/goal` 命令参数？会话的 `SessionModelSelection`？目标轮的某个配置项？`goalCheck.Evaluate` 与 `runGoalCheck` 的签名各要加什么参数？给出建议 API 形状（`func (r *Runner) runGoalCheck(ctx, task string, modelRowID string)` 之类）与**精确的文件清单**（后续实现会动哪几个文件、多少处）。
2. **授权语义冲突（必须显式裁决）**：`runGoalCheck` 已绕过 `enable_subagent`；复用 `usableModelByID` 会撞 `subagent_allowed`。「用户亲自发起 `/goal`」是否算用户亲自选模型？给三个选项及各自代价：①完全复用 `usableModelByID`（最一致，但未授权行不可用）；②照交接例外做会话级 `/goal` 例外（需要新的状态记录，参考 `state.PlanHandOff`）；③只在用户显式指定行 id 时绕过开关（推荐？给出理由）。
3. **前缀缓存**：确认新能力不改工具定义、不改系统提示；`list_usable_models` 结果仍只追加在对话末尾。写出机器可判的验证命令（照 `plans/283...:1020` 的 `grep -c ... = 3`）。
4. **能力读数**：若沿用 `subagentModelOverrideForRef`，`Caps` 会带该行的窗口/输出上限；`290` 修的 `subagentExecContext` / `subagentRecordPin` 投影是否覆盖 goal check 路径（`runGoalCheck` 也调 `subagentExecContext(prep)`）；若不覆盖，写进后续实现的前置依赖。
5. **爆炸半径与验收**：列出后续实现计划该有的测试（goal check 钉到行、未授权行拒绝、同名两行各自生效、主代理请求前缀字节不变）与真机 / fake provider 是否必要。
6. **结论**：`GO / NO-GO / NEEDS OWNER DECISION` 三选一，一句话理由。**spike 不替 owner 做产品裁决**。

**Verify**：`grep -nE '^### (Facts verified|Open questions|Recommendation|Verdict)' plans/297-goal-check-model-row-spike.md` 四节都有且内容非空；`grep -c 'TODO' plans/297-goal-check-model-row-spike.md` = `0`（模板占位必须被真实结论替换）。

### Step 4: 收口（证明生产代码零改动）

**Verify**（全部要跑）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalCheck' -count=1 -v` → 全部 `--- PASS`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `gofmt -l pkg cmd` → 无输出
- `git status --porcelain -- pkg/run/*.go | grep -v '_test.go'` → 无输出（生产代码零改动）

## Test plan

- 原型测试：`pkg/run/goal_test.go` 的 `TestGoalCheckCanBePinnedToAConfiguredRow` —— 证明「把带 pin 的 ctx 传进 `goalCheck.Evaluate`」即可让 goal check 跑在指定模型行，**无需任何生产改动**。这是 spike 的核心证据，不是产品功能。
- 既有 `TestGoalCheckDispatchesTheEvaluatorSubagent` 等所有 `TestGoalCheck*` 断言一字不改地通过。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalCheck' -count=1 -v` 全 PASS；禁止用 `-run` 零匹配当证据。

## Done criteria

- [ ] `grep -n 'func TestGoalCheckCanBePinnedToAConfiguredRow' pkg/run/goal_test.go` 有 1 处命中
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalCheckCanBePinnedToAConfiguredRow$' -count=1 -v` → `--- PASS`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestGoalCheck' -count=1 -v` 全 `--- PASS`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`gofmt -l pkg cmd` 全绿
- [ ] `git status --porcelain -- pkg/run/*.go | grep -v '_test.go'` 无输出（生产代码零改动）
- [ ] 本计划 `## Spike findings` 四节全部填完、无 `TODO` 占位，且每条结论带 `file:line`
- [ ] `plans/README.md` 中本计划状态行已更新（建议状态写 `DONE — 待 owner 裁决`，不是 `DONE — 已实施`）

## STOP conditions

- Drift check 显示 `pkg/run/goal_test.go` 在 `d715dafd` 之后被改过，且摘录对不上。
- Step 1 的任一条事实与「Current state」不符（尤其 `prepareSubagentExecutionResolved` 不再优先采用 ctx 上的 override，或 `goalCheck.Evaluate` 的 ctx 被换成 `context.Background()`）—— 说明「相邻可能」的技术前提变了，如实记录并 STOP。
- Step 2 的原型测试**必须改生产代码**才能通过：这本身就是 NO-GO 证据，把原因写进「Spike findings」后停下，不要顺手把功能实现了。
- 你倾向于直接在 `runGoalCheck` 里加 `model` 参数、或改 `subagentDispatch` 结构 —— 那是后续实施计划的活，本 spike 明确禁止，先报告。
- 新增文件、新增 import 边、或 `pkg/architecture` 变红。
- 同一验证连续两次失败。

## Maintenance notes

- 本计划是**方向项的探路**，交付物是结论 + 原型测试，不是功能。若 owner 裁决 GO，应另立一份实施计划（建议编号接在本批之后），并把本计划「Open questions」里的裁决逐字写进新计划的「Why this matters」。
- 后续实现要守住三条：工具定义字节稳定（前缀缓存）、`model` 只认行 id、授权语义与 `subagent_allowed` 的一致性有 owner 明确裁决。
- `runGoalCheck` 的「绕过 enable_subagent」是用户发起 `/goal` 的有意设计，不要因为复用 `model` 就顺手把它改成受开关约束 —— 那会改变既有行为。
- 与 `290` 的关系：goal check 也走 `subagentExecContext(prep)`，因此 `Caps` 投影修好后这一路径同样受益；spike 结论里要写明这一点，避免后续实现重复排查。

---

## Spike findings

> 由执行者（spike）填写；以下每节均给出本次会话真实核对过的 `file:line` 依据与命令输出。行号以当前工作区为准（见 Facts verified 注）。

### Facts verified

| # | 事实 | 依据（`file:line`） | 与 Current state 一致？ |
|---|------|--------------------|------------------------|
| 1 | `prepareSubagentExecutionResolved` 优先采用 ctx 上的 pin：`if override, ok := SubagentModelOverrideFromContext(baseCtx); ok { pin = override }`，随后 `if pin.valid() { …; cctx = WithSubagentModelOverride(cctx, pin) }`；pin 经 `subagentExecContext` 从 `prep.entry` / `prep.ctx` 重建后 `WithSubagentModelOverride(hostCtx, pin)` 落到执行 ctx。`dispatchSubagent` → `startSubagent` 把调用方 ctx 原样作为 `baseCtx` 传入，无中途替换。 | `pkg/run/subagent.go:1978-1980`、`:2001-2008`、`:4149-4163`、`:3932-3933`、`:4217-4268`；Verify `grep -n 'SubagentModelOverrideFromContext(baseCtx)'` 命中 `:1979` 一处、`grep -c` = 1 | 一致（行号下移，见注） |
| 2 | `goalCheck.Evaluate` 的 ctx 从 `/goal` 循环一路传到 `runGoalCheck`，无 `context.Background()` 替换：`finishSupervisedRun(runCtx, …)` 把 `runCtx` 放进 `ctxGoalContinuationInput.ctx`，循环调 `in.eval.Evaluate(in.ctx, …)`；`Evaluate` 直接 `c.runner.runGoalCheck(ctx, …)`，`runGoalCheck` 直接 `dispatchSubagent(ctx, …)`。`goal.go` 全文无 `context.Background()`。 | `pkg/run/supervisor.go:91-98`、`pkg/run/goal.go:216`、`:111-115`、`pkg/run/subagent.go:4944-4948`；Verify `grep -n 'context.Background()' pkg/run/goal.go` 无命中（grep exit 1） | 一致（行号下移，见注） |
| 3 | `runGoalCheck` 注释「runs whether or not the model may spawn subagents, because the user asked for it with /goal」仍成立；函数签名 `(ctx, task string)` 无 model 参数；`subagentDispatch` 结构无 model 字段 —— 绕过 `enable_subagent` 仍是有意设计。 | `pkg/run/subagent.go:4938-4944`、`:3904-3925` | 一致（行号下移，见注） |
| 4 | `usableModelByID` 三条拒绝：空/未知行 id（`:4380-4382`、`:4388-4404` 的 `ErrLLMModelNotFound`）、`!row.SubagentAllowed` 未授权（`:4414-4416`）、`!credentialed` 无凭据（`:4417-4419`）；交接例外 `handOffModelID(ctx, fac) == id`（`:4392`、`:4413`）跳过开关（`!handedOff &&`）但仍查凭据（`:4397-4400`、`:4417`）。`handOffModelID` 从 `state.Get(...).PlanHandOff.ModelID` 读（`:4426-4439`）。 | `pkg/run/subagent.go:4378-4421`、`:4426-4439`；另 `:4339-4372`（usableModels）、`:4447-4475`（overrideForRef）、`:4482-4504`（With/From）、`:4301-4303`（valid 仅要求 `Model != ""`） | 一致（行号下移，见注） |
| 5 | 工具定义字节稳定：`grep -c 'Optional id of a model from list_usable_models' pkg/run/subagent.go` = 3（三处 `:102`、`:114`、`:148`）；handler `:415-419` 仍是 `pin := subagentModelOverrideForRef(ctx, fac, in.Model)` → `ctx = WithSubagentModelOverride(ctx, pin)` → `execGeneralSubagent(...)`。 | `pkg/run/subagent.go:102`、`:114`、`:148`、`:415-419`；Verify `grep -c` = 3 | 一致（行号下移，见注） |

> 注：以上 `file:line` 为本次会话 `grep -n` / `Read` / `sed -n` 在**当前工作区**核对到的真实行号 —— HEAD `ecc4e1aa` 加计划 290/293/294 对 `subagent.go` 的未提交前置改动（开工前 `git diff -- pkg/run/subagent.go` 的 SHA-256 记为 `c189ba8fe436773dd46d540ba7861526f81730385cc93599edea2d1b91670acd`，Step 4 收尾复核字节未变）。相对计划正文引用的 d715dafd 基线行号整体下移，但符号、控制流与文案逐字一致，故判「一致」；spike 未改动该文件。

### Open questions

1. **行 id 来源与 API 形状**【备选 + 建议，来源仍待 owner 裁决】。三个来源：(a) `/goal` 命令参数 —— 要沿 `GoalObjective` 的既有链逐处透传：`pkg/tui/chat_session.go:2451-2457` → `pkg/turn/model.go:49,506,526` → `pkg/turn/executor.go:499` → `pkg/process/run_executor.go:234` → `pkg/gateway/server.go:2224,2705` → `pkg/tui/chat_surface.go:109,122` → `pkg/tui/run.go:2264-2266,3028,4131` → `pkg/run/supervisor.go:43-48`（约 8–10 处）；(b) 会话的 `SessionModelSelection`（`pkg/run/config.go:2247-2256`，`pkg/run/subagent.go:1989-1996` 已用它给 pin 填 `ModelID`）—— 语义不符，它是会话**主模型**，不是给检查单独指定的行；(c) 与 `GoalObjective` 并列的 `Options.GoalModelRowID`（`pkg/run/supervisor.go:43-48`）。
   建议 API 形状（倾向，**不替 owner 裁决**）：**不改 `runGoalCheck` / `subagentDispatch` / `goalCheck.Evaluate` 的签名** —— 在 `finishSupervisedRun`（`pkg/run/supervisor.go:91-99`）里 `pin, err := subagentModelOverrideForRef(ctx, fac, rowID)` 后 `ctx = WithSubagentModelOverride(ctx, pin)`，`goalCheck.Evaluate`（`pkg/run/goal.go:111-115`）→ `runGoalCheck`（`pkg/run/subagent.go:4944-4948`）→ `prepareSubagentExecutionResolved`（`:1979`）原样传 ctx 即可生效。原型测试 `pkg/run/goal_test.go:743` 正是这么注入的。若拒绝在派发前解析，才需要 `func (r *Runner) runGoalCheck(ctx, task string, modelRowID string)`（`subagent.go:4944`）。
   **精确文件清单**（方案 a/c，后续实现）：`pkg/run/supervisor.go`（Options 字段 + `finishSupervisedRun` 注入）、`pkg/run/goal.go`（`ctxGoalContinuationInput:191-199` 仅当 rowID 要随轮次传递）、`pkg/run/goal_test.go`（原型转正）、命令参数贯穿的 8–10 个 tui/turn/process/gateway 文件（清单见上）；**`pkg/run/subagent.go` 可零改动**（注入在上游 ctx）。`pkg/config` / `pkg/state` 仅当选项②需要新增状态记录时才动（受硬约束：`pkg/config` 不 import `pkg/llm`，共享投影放 `pkg/state`，计划 :85）。

2. **授权语义冲突 —— 待 owner 裁决**【待 owner 裁决；spike 不替 owner 做产品裁决】。`runGoalCheck` 注释明确「runs whether or not the model may spawn subagents, because the user asked for it with /goal」（`pkg/run/subagent.go:4938-4943`），**有意绕过 `enable_subagent`**；而 `subagentModelOverrideForRef`（`:4447-4455`）→ `usableModelByID`（`:4378`）对 `!row.SubagentAllowed` 返回拒绝（`:4414-4416`），交接例外 `handOffModelID`（`:4392,4413,4426-4439`，状态样板 `state.PlanHandOff` 见 `:4435-4438`）才跳过开关、仍查凭据（`:4397-4400,4417`）。「用户亲自发起 `/goal`」算不算「用户亲自选模型」、`subagent_allowed` 这道成本开关要不要为 `/goal` 再让一次步，是产品策略问题。三个选项及代价：
   - ① **完全复用 `usableModelByID`**（最一致，`subagent.go:4447-4455`）：零新增状态，但 `/goal` 只能钉到 `subagent_allowed=1` 且有凭据的行 —— 与 `runGoalCheck` 现有「用户发了 /goal 就能跑」的语义冲突，用户发 /goal 却因行未开开关被拒；需改注释或接受不一致。
   - ② **照交接例外做会话级 `/goal` 例外**（参考 `state.PlanHandOff`，`subagent.go:4435-4438`）：新增 `pkg/state` 状态字段 + 迁移 + 投影（受计划 :85 约束），`usableModelByID` 对该行跳过 `subagent_allowed`、仍查凭据（`:4397-4400`）。代价：绕过范围从「是否 spawn」扩大到「是否授权该行」，语义面变大、持久化与迁移成本最高。
   - ③ **仅在用户显式指定行 id 时绕过开关**（spike 倾向，**仍待 owner 确认**）：不传 rowID 时维持现状、完全不碰 `usableModelByID`；传了 rowID 时视为「用户亲自选模型」，与 `runGoalCheck` 既有「user asked for it with /goal」同构，跳过 `subagent_allowed`、仍查凭据（`:4417`）。理由：把「用户主动指定」当授权边界，范围最小、零新增持久化状态。代价：不能直接用现成 `subagentModelOverrideForRef`（它会撞开关），需给 `usableModelByID` 加「调用方已获用户显式授权」的入口或参数 —— 这本身又是产品裁决。
   **结论：必须 owner 三选一；本 spike 只把冲突与代价摆清，不裁决。**

   > **Owner 裁决（2026-11-11）：选 ③** —— 「仅用户显式指定行 id 时绕过 `subagent_allowed`、仍查凭据」。三个选项与代价保留在上面作留痕，裁决不再改动它们。评审补充的落地护栏（后续实施计划必须逐条写进 Why / Scope / Done criteria）：
   > 1. **凭据校验绝不省**：`strings.TrimSpace(row.APIKey) != "" || appcfg.CredentialsBackedProvider(row.Provider)` 照旧，拒绝时逐字复用 `usableModelByID:4417` 的 `has no credentials` 定稿文案。
   > 2. **旁路显式、可审计**：单独一个明确命名的入口（建议 `usableModelByIDForExplicitGoal` 或同族名字），不要给 `usableModelByID` 悄悄塞一个布尔参数到处传；`list_usable_models` 与常规 `model` 参数路径**一个字节都不动**（前缀缓存，`pkg/run/subagent.go:537-540`）。以后给准入规则加条件时，仍按 293 Maintenance notes 的口径同时检查「列表 / DB 行 / 兜底 / `/goal` 旁路」四条路径。
   > 3. **row id 必须源自用户在 `/goal` 命令里的直接输入**：一旦允许 supervisor 或主代理自行推导 `GoalModelRowID`，这条旁路立刻变成 `subagent_allowed` 的后门（开关定稿语义见 `pkg/process/setup.go:30-34`「may the agent pick this row for a subagent on its own through the model argument」）。**没传 row id 时完全不解析、不改 ctx**：默认行为与今天一致（检查子代理仍跑会话主模型，`pkg/run/subagent.go:1978-2007` 第三级）。
   > 4. **语义依据（评审复核）**：`/goal` 检查是系统派发、用户发起的保留路径（`pkg/agent/subagent_defs.go:22-26` 的 `SystemSubagentNames`、`pkg/run/subagent.go:4938-4943` 的「because the user asked for it with /goal」），从设计上就不受 `enable_subagent` / `subagent_allowed` 约束；配置校验也禁止给 `goal-evaluator` 配模型（`pkg/config/agents.go:267-269`）。所以 ① 不是「保持一致」，而是第一次把开关套到一条原本不受它约束的路径上；③ 才是延续既有语义。

3. **前缀缓存 / 工具定义稳定性**【建议：现状已满足，后续实现须守住】。三处 `Optional id of a model from list_usable_models`（`pkg/run/subagent.go:102,114,148`），本次会话 Verify `grep -c 'Optional id of a model from list_usable_models' pkg/run/subagent.go` = `3`；`listUsableModelsDescription`（`:541`）是固定文本常量，注释 `:537-540` 写明「names no model, so adding one or flipping an authorization switch never moves the tool definition (and every cached prompt prefix with it)」；`list_usable_models` 注册 `:511-533`，`listUsableModels`（`:547`）返回的列表作为 tool result 只追加在对话末尾（计划 :83 约束）。原型测试（`pkg/run/goal_test.go:743`）不改工具定义、不改系统提示，仅注入 ctx。
   机器可判验证命令（照 `plans/283...:1020`）：`grep -c 'Optional id of a model from list_usable_models' pkg/run/subagent.go` = `3`（恒定）、`grep -c 'const listUsableModelsDescription' pkg/run/subagent.go` = `1`、定向原型测试 `--- PASS`。结论：复用 `model` 行参数不改工具定义、不改系统提示、前缀缓存不受影响。

4. **能力读数与 `290` 的关系**【建议：无新增前置依赖】。`subagentModelOverrideForRef`（`pkg/run/subagent.go:4447-4475`）用 `state.ModelOverrideFromCaps(configRow.Caps)`（`:4461-4465`）把 `Caps` 填进 `SubagentModelOverride.Caps`（`:4473`, 字段定义 `:4298`）；`subagentExecContext`（`:4217-4268`）从 `prep.ctx` 的 dispatched pin 投影 `Entry/Version/Caps`（`:4263-4265`，其中 `pin.Caps = dispatched.Caps` 正是 290 的改动，见开工前记录的 `git diff -- pkg/run/subagent.go` diff 第 99–106 行）。`runGoalCheck`（`:4956`）**也调 `subagentExecContext(prep)`**，与 plan review / 通用派发同一入口 —— 故 290 的 Caps 投影**已覆盖 goal check 路径**（与计划 :219 Maintenance notes 一致），后续实现**不需要**为 goal check 单独补能力读数投影。
   注意边界：290 另改的 `subagentRecordPin`（`:2396`）、`subagentRecordContext`（`:2437`）、`startSubagentContinuation`（`:2860`）、`continueSubagentRecord`（`:3125`）是**续跑/继续**路径，不作用于 goal check 的一次性派发；只有「让 goal check 可续跑/重放」时才需排查。原型测试注入的是手写 `SubagentModelOverride{ModelID,Provider,Model}`（`goal_test.go:743`，无 Caps），仅是 characterization，不代表真实链路缺 Caps —— 复用 `subagentModelOverrideForRef` 即自动带上。写进后续实现说明，避免重复排查。

5. **后续实现的测试与验收清单 / 爆炸半径**【建议】。后续实施计划该有的测试（写在 `pkg/run/goal_test.go` / `subagent_test.go`）：① goal check 钉到行 —— 把 `TestGoalCheckCanBePinnedToAConfiguredRow`（`goal_test.go:743`）转正，断言 `executor.pinnedModel` / `executor.model` = 指定行（`subagent_test.go:2267-2283`）；② 未授权行拒绝 —— 指定 `subagent_allowed=0` 的行时按 owner 裁决的选项①/②/③ 给出拒绝文案（参考 `usableModelByID:4414-4416`）或放行；③ 同名两行各自生效 —— `model` 是行 id 不是 provider/model（计划 :82），两个同名不同 id 的行分别钉到不同 endpoint/凭据（参考 `SubagentModelOverride.ModelID:4278-4288` 与 `listUsableModelsDescription:541`「Rows with the same provider/model are different models」）；④ 主代理请求前缀字节不变 —— `grep -c 'Optional id of a model from list_usable_models'` = 3 恒定 + 工具定义/系统提示字节对比；⑤ 回归 `TestGoalCheck*`（`goal_test.go:597-633`）、`TestSubagentModelOverride*`（`subagent_test.go:1268-1320`）、`TestPlanReviewSpawnEventNamesTheChosenModel`（`:430-461`）全 PASS。
   **真机 / fake provider 不必要**：spike 与原型证据链完全基于内存 fixture `fixedSubagentExecutor`（`subagent_test.go:2194-2283`）+ `dispatchProbe`（`:37-74`），pin 的采用可在 `RunSubagentExec` 的 ctx/请求上直接断言，无需真实 LLM；仅当要验证「换模型后请求确实发到该 endpoint」时才补 fake provider（仓库有 `supervisorScriptLLM` 样板），那属集成层而非本方向项门禁。

### Recommendation

**技术上可 GO，但整体结论待 owner 裁决**（spike 不替 owner 做产品裁决）。
- 可行性已证：`TestGoalCheckCanBePinnedToAConfiguredRow`（`pkg/run/goal_test.go:743`，`--- PASS`）证明「只差一层 ctx 注入」——在 `finishSupervisedRun`（`pkg/run/supervisor.go:91-99`）把 `subagentModelOverrideForRef` 的结果 `WithSubagentModelOverride` 到 `runCtx`，`goalCheck.Evaluate`（`pkg/run/goal.go:111-115`）→ `runGoalCheck`（`pkg/run/subagent.go:4944-4948`）→ `prepareSubagentExecutionResolved`（`:1979`）整链零生产改动即可钉到行；`runGoalCheck` / `subagentDispatch`（`:3904-3925`） / `Evaluate` 签名都不必改。
- **Owner 已裁决（2026-11-11）：授权语义选 ③**（仅显式指定行 id 时绕过 `subagent_allowed`、仍查凭据），范围最小、零新增状态、与 `runGoalCheck:4942`「user asked for it with /goal」同构。裁决与四条落地护栏逐字写在 Open questions #2 的引文块里，后续实施计划须原样抄入 Why / Scope / Done criteria。
- 评审建议（**仍待 owner 确认**）：行 id 来源用 `Options.GoalModelRowID`（`supervisor.go:43-48`），值由 `/goal` 命令参数填入；不复用 `SessionModelSelection`（`config.go:2247`，那是会话主模型，语义不符）。UI 需让用户看得到/选得到行 id（283 已定重名行用 id + 端点区分），否则 ③ 在体验上不可达。
- **仍待 owner 裁决的两项**：(a) 行 id 最终来源与命令面（`/goal` 参数 vs 会话配置）；(b) 是否立项实现 —— 若立项，另立实施计划并逐字抄入本节的裁决与护栏（计划 :216）。原第 (a) 项「授权语义三选一」已由本次裁决消解。
- 后续实现前置依赖：**无**（290 的 Caps 投影已覆盖 `subagentExecContext`，见 #4）；守住三条硬约束（工具定义字节稳定、`model` 只认行 id、授权语义有 owner 明确裁决，计划 :217）。

### Verdict

**`NEEDS OWNER DECISION`（spike 原始结论，保留作留痕）** —— 技术可行性已由零生产改动的原型测试证明（GO 级证据，`pkg/run/goal_test.go:743`），但 `runGoalCheck` 有意绕过 `enable_subagent`（`pkg/run/subagent.go:4938-4943`）与 `usableModelByID` 的 `subagent_allowed` 校验（`:4414-4416`）之间的授权语义冲突属产品策略，必须 owner 三选一裁决后另立实施计划；spike 不替 owner 做产品裁决。

**更新（owner 2026-11-11 裁决）：`GO` —— 授权语义定案为 ③，实施计划另立。** owner 选择「仅用户显式指定行 id 时绕过 `subagent_allowed`、仍查凭据」，冲突已消解，不再是 `NEEDS OWNER DECISION`。**但本 spike 仍不实施产品功能**：是否立项、行 id 来源与命令面仍待 owner 确认（见 Recommendation 的「仍待裁决的两项」）；若立项，另建一份实施计划，把 Open questions #2 的裁决与四条护栏逐字抄进 Why / Scope / Done criteria，并把本节结论作为它的依据。

**二次更新（2026-11-11 晚，两项后续裁决完成）**：owner 确认行 id 来源与命令面；Recommendation 里「仍待裁决的两项」至此全部完成。后续方向改为设置页按子代理类型配置并落库（见 `plans/299`），本 spike 仍不实施产品功能。
