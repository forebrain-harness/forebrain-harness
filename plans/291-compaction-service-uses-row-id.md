# Plan 291: 显式 `/compact`、worker CLI 与子代理压缩也按模型行 id 读数

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/run/compaction.go pkg/run/compaction_test.go && git diff d715dafd -- pkg/run/compaction.go pkg/run/compaction_test.go`
> 若两个文件在 `d715dafd` 之后已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。
> `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none（与 `290` 文件不重叠，可并行）
- **Category**: bug
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

计划 283 Part E/F 把「能力设置」和「服务端实测窗口」从 `provider/model` 改成按**行 id** 隔离，目标是让同名两行各行其是。为此 `assembly.Service` 新增了 `ModelHit` 与 `ModelRow` 两个函数字段，`Runner.loadLocked`（交互式 run 的自动压缩装配点）已经填上了。但另一个装配点 `CompactionService(r, sessions)` 没有填 —— 它手里明明有完整的 `Runner`，却只装了 `PrimaryModel`、`CompactLLM` 等。

`CompactionService` 是显式 `/compact`（`pkg/tui/chat_slash.go:41`）、worker CLI（`pkg/process/worker_cli.go:98`）和子代理 `AutoCompactSession`（`pkg/run/subagent.go:3292`）的实际装配点。这些路径今天会走 `modelLimits` 的 `llm.Lookup(provider, model)` 回退与 `modelRow(ctx) == ""` 的 pair 观测桶：压缩阈值、压缩卡的 `TokensAfter`、摘要窗口全用目录值，且一行的 400 拒绝会压低另一行的窗口。也就是说，Part E/F 想消除的缺陷在这些路径上原样保留。`assembly.Service` 自己的注释明确写着 nil 只应留给「没有 runner 的构造」——此处构造有 runner，是遗漏而非设计。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26，CGO，`-tags fts5`）。

- `pkg/run/compaction.go:348-377`（缺陷）：
  ```go
  // CompactionService assembles the compaction service for an explicit /compact.
  // ...
  func CompactionService(r *Runner, sessions *state.SessionStore) assembly.Service {
      out := assembly.Service{Sessions: sessions}
      if r == nil {
          return out
      }
      out.CompactLLM = r.agentCompactClientFor
      out.ModelProvider = r.ContextCompactModelProvider
      out.CompactLLMForModel = r.ContextCompactLLMForModel
      out.PrimaryModel = func(ctx context.Context) (string, string) { return r.agentModelFor(ctx) }
      if r.AppCfg != nil {
          out.Prompt = r.AppCfg.Compact.Prompt
          opts := CompactBudgetOptions(r.AppCfg)
          out.AutoCompactPercent = opts.AutoCompactPercent
          out.ConfigContextTokens = opts.FallbackContextTokens
      }
      out.PreCompact = func(ctx context.Context, sid, trigger string) error { ... }
      out.PostCompact = func(ctx context.Context, sid, trigger string) error { ... }
      out.Events = event.SinkFunc(r.publishSurfaceEvent)
      out.ConversationSummary = r.conversationSummarizer
      return out
  }
  ```
  注意 `r != nil` 分支里**没有** `out.ModelHit` / `out.ModelRow`。

- `pkg/run/runner.go:1062-1072`（做对了的对照，逐字照抄这两个字段的写法）：
  ```go
  compactSvc := assembly.Service{
      Sessions: r.SessionStore,
      PrimaryModel: func(ctx context.Context) (string, string) {
          return r.agentModelFor(ctx)
      },
      // The conversation's own row answers what its model may do, so the
      // compaction budget and the compact card read the row the footer does.
      ModelHit: r.modelHitFor,
      // Observed window behaviour is per row, so the compact service reads
      // the same identity the checkpoint path resolves.
      ModelRow:   r.agentModelRowFor,
      CompactLLM: r.agentCompactClientFor,
      ...
  ```

- `pkg/assembly/service.go`
  - `:70-80` 两个字段的契约注释：
    ```go
    // ModelHit is PrimaryModel's capability answer, read from the configured
    // row the conversation runs on (owner 2026-11-10). nil — a service built
    // without a runner — leaves the pair's catalog values in force.
    ModelHit func(ctx context.Context) (llm.Hit, bool)
    // ModelRow names the configured row the conversation's model comes from:
    // observed window behaviour belongs to that row, so two rows naming one
    // provider/model do not share a learned cap (owner 2026-11-10). nil — or
    // "" — falls back to the pair.
    ModelRow func(ctx context.Context) string
    ```
  - `:665-683`：
    ```go
    func (s Service) modelRow(ctx context.Context) string {
        if s.ModelRow == nil { return "" }
        return s.ModelRow(ctx)
    }
    func (s Service) modelLimits(ctx context.Context, provider, model string) (llm.Hit, bool) {
        if s.ModelHit != nil {
            if hit, ok := s.ModelHit(ctx); ok { return hit, true }
        }
        return llm.Lookup(provider, model)
    }
    ```
  - `:366`、`:482`、`:695`：`TryCompactOnMessages`、`compactMessages`、`summaryInputBudget` 分别以 `s.modelRow(ctx)` 调 `ProjectRequestTokensForRow` / `ProjectForRow` / `EffectiveWindowForRow`。
  - `:268`、`:286`：`shouldCompactMessages` 用 `s.modelLimits(...)` 算阈值。

- 实际调用方（**只读，不在 Scope**）：
  - 终端：`pkg/tui/chat_slash.go:41`（显式 `/compact`）、`pkg/tui/chat_turn.go:1383`
  - 网关 / 网页：`pkg/gateway/slash_handlers.go:24`（网页 `/compact`）、`pkg/gateway/api_extra.go:3278`、`pkg/gateway/run_control.go:1330`
  - worker CLI：`pkg/process/worker_cli.go:98`
  - 子代理：`pkg/run/subagent.go:3292`（`AutoCompactSession`）
  - 测试里也有 `pkg/run/subagent_test.go:5014`、`:5105` 与 `pkg/run/compaction_test.go:302`、`:442`
  也就是说**网页的 `/compact` 走同一个装配点**；补上两个字段会同时改变那条路径的阈值与观测桶（正是意图），所以回归集必须带 `./pkg/gateway ./pkg/turn`。
  网页路径的 ctx 通常不带会话选择：`agentModelRowFor` 会退到 `fb_session_model_state`（按 `llm.AgentSessionIDFromContext(ctx)` 回读，`pkg/run/config.go:2521-2531`），这是既有回退，不是缺陷；不要为此在 `CompactionService` 里加兜底。

测试样板（**不要新建 `_test.go` 文件** —— `pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles` 要求 `X_test.go` 必须有 `X.go`）：
- `pkg/run/compaction_test.go:967-1015` `TestModelHitForFollowsTheConfiguredRow`：
  ```go
  wide, narrow := int64(64000), int64(32000)
  llm.SetModelOverrides(
      map[string]llm.ModelOverride{
          "row-wide":   {ContextWindow: &wide},
          "row-narrow": {ContextWindow: &narrow},
      },
      map[string]string{
          "row-wide":   "deepseek/deepseek-v4",
          "row-narrow": "deepseek/deepseek-v4",
      },
  )
  t.Cleanup(func() { llm.SetModelOverrides(nil, nil) })
  r := &Runner{}
  ctx := WithSessionModelSelection(context.Background(), PrimaryModelState{
      ModelID: "row-narrow", Provider: "deepseek", Model: "deepseek-v4", Set: true,
  })
  if hit, ok := r.modelHitFor(ctx); !ok || hit.ContextWindow != 32000 { t.Fatalf(...) }
  if got := r.agentModelRowFor(ctx); got != "row-narrow" { t.Fatalf(...) }
  ```

仓库约定：
- 能力表按行 id 建键；无行 id 时回退 pair / 目录值是计划 283 明确的有意行为，**不要改**。
- 观测只在内存、旧导出 API 签名不变；`assembly.Service` 两个字段都是 `func(ctx)` 形状，照 `runner.go` 原样赋方法值即可。
- 不改公开接口、不加 import 边、不加生产文件（`pkg/architecture` 限制每包 ≤30 个生产文件）。
- `gofmt` 必须干净；注释用英文。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestCompactionServiceReadsTheSessionsOwnRow$' -count=1 -v` | `--- PASS: TestCompactionServiceReadsTheSessionsOwnRow` |
| 回归包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/assembly ./pkg/tui ./pkg/process ./pkg/gateway ./pkg/turn -count=1` | 全部 `ok` |
| 架构规则 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0，无输出 |
| 格式 | `gofmt -l pkg cmd` | 无输出 |

## Scope

**In scope**（只允许改这两个文件）：
- `pkg/run/compaction.go`
- `pkg/run/compaction_test.go`

**Out of scope**（不许动）：
- `pkg/assembly/service.go` —— `ModelHit` / `ModelRow` 的语义与回退规则不改。
- `pkg/run/runner.go` —— 它已经是对的，仅作为样板。
- `pkg/tui/chat_slash.go`、`pkg/process/worker_cli.go`、`pkg/run/subagent.go` —— 调用方不用改，装配点补齐即可。
- `pkg/llm/*`、`pkg/state/*`、`pkg/process/config_db.go`。
- `290`（`subagent.go` 的 `Caps` 投影）—— 不同文件、不同缺陷，不在这里顺手改。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `fix(run): give the manual compaction service the session's model row`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

### Step 1: 先写会红的回归测试

在 `pkg/run/compaction_test.go` 末尾新增 `func TestCompactionServiceReadsTheSessionsOwnRow(t *testing.T)`，照 `TestModelHitForFollowsTheConfiguredRow` 的 fixture：

1. `wide, narrow := int64(64000), int64(32000)`；`llm.SetModelOverrides(...)` 注入同名两行（`row-wide` / `row-narrow`，pair 均为 `deepseek/deepseek-v4`）；`t.Cleanup(func() { llm.SetModelOverrides(nil, nil) })`。
2. `r := &Runner{}`；`ctx := WithSessionModelSelection(context.Background(), PrimaryModelState{ModelID: "row-narrow", Provider: "deepseek", Model: "deepseek-v4", Set: true})`。
3. `svc := CompactionService(r, nil)`，断言：
   - `svc.ModelHit != nil`、`svc.ModelRow != nil`；
   - `svc.ModelRow(ctx) == "row-narrow"`；
   - `hit, ok := svc.ModelHit(ctx)` → `ok == true && hit.ContextWindow == 32000`。
4. 无 runner 的构造仍保持 nil：`bare := CompactionService(nil, nil)`，断言 `bare.ModelHit == nil && bare.ModelRow == nil`（守住 `assembly.Service` 注释里「只有无 runner 的构造才留空」这条契约）。
5. 再加一条 twin-row 断言：把 `ctx` 换成 `ModelID: "row-wide"`，`svc.ModelHit(ctx).ContextWindow == 64000`，证明读数跟着所选行走，而不是钉死第一行。

**Verify（预期红）**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestCompactionServiceReadsTheSessionsOwnRow$' -count=1 -v` → **FAIL**，报错指向 `svc.ModelHit == nil`（修复前就是 nil）。把这次红输出记进执行报告；不要为了让测试通过而改断言。

### Step 2: 补齐装配点

在 `pkg/run/compaction.go` 的 `CompactionService` 里，紧跟 `out.PrimaryModel = ...` 之后加两行（方法值，与 `runner.go:1067-1070` 完全一致）：

```go
	out.ModelHit = r.modelHitFor
	out.ModelRow = r.agentModelRowFor
```

加一行英文注释说明原因（照 `runner.go` 的措辞即可），例如：
`// The manual /compact path reads the same row as the interactive run's auto-compaction; without these two the pair's catalog values and the shared observation bucket stay in force (plan 291).`

不要改 `r == nil` 的早退分支；不要给 `bare` 情况塞默认函数。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestCompactionServiceReadsTheSessionsOwnRow$' -count=1 -v` → `--- PASS`；`grep -n 'out.ModelHit\|out.ModelRow' pkg/run/compaction.go` 各 1 处命中。

### Step 3: 收口回归

**Verify**（全部要跑）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/assembly ./pkg/tui ./pkg/process ./pkg/gateway ./pkg/turn -count=1` → 全部 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → 无输出
- `gofmt -l pkg cmd` → 无输出

## Test plan

- 新测试：`pkg/run/compaction_test.go` 的 `TestCompactionServiceReadsTheSessionsOwnRow`，覆盖：
  - happy path：`CompactionService(r, sessions)` 两个函数字段已装配；
  - 回归（本计划修的 bug）：`ModelHit(ctx)` 返回所选行的窗口（32000）， twin-row 切到另一行得 64000；
  - 边界：`CompactionService(nil, nil)` 仍留空（无 runner 的构造契约）；
  - `ModelRow(ctx)` 返回行 id（保证 `EffectiveWindowForRow` 用 row 桶而不是 pair 桶）。
- 结构样板：`pkg/run/compaction_test.go:967-1015`。
- 验证：Step 1 的 `-run` 命令修复前 FAIL、Step 2 后 PASS；禁止用 `-run` 零匹配当证据。

## Done criteria

- [ ] `grep -c 'out.ModelHit = r.modelHitFor' pkg/run/compaction.go` = `1`
- [ ] `grep -c 'out.ModelRow = r.agentModelRowFor' pkg/run/compaction.go` = `1`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestCompactionServiceReadsTheSessionsOwnRow$' -count=1 -v` → `--- PASS`（修复前 FAIL 的证据在报告里）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/assembly ./pkg/tui ./pkg/process ./pkg/gateway ./pkg/turn -count=1` 全 `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`CGO_ENABLED=1 go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 全绿
- [ ] `git status --porcelain -- pkg/run` 只列出 `compaction.go` 与 `compaction_test.go`
- [ ] `plans/README.md` 中本计划状态行已更新

## STOP conditions

- Drift check 显示两个文件在 `d715dafd` 之后被改过，且摘录对不上。
- `assembly.Service` 上找不到 `ModelHit` / `ModelRow` 字段，或 `Runner` 上没有 `modelHitFor` / `agentModelRowFor` 方法。
- 加完两行后 `pkg/run`、`pkg/assembly`、`pkg/tui`、`pkg/process`、`pkg/gateway`、`pkg/turn` 任一既有测试变红，且原因是「某个调用方的 ctx 拿不到会话选择、导致 `agentModelRowFor` 返回空或 panic」—— 说明装配点的 ctx 语义与假设不符，先报告，不要在 `CompactionService` 里加兜底掩盖。
- 修复需要改 `assembly.Service` 的公开字段、新增 import 边，或 `pkg/architecture` / `scripts/package-graph.sh` 变红。
- 同一验证连续两次失败。

## Maintenance notes

- 以后再给 `assembly.Service` 加「按行」的字段时，必须同时检查**所有**构造点：`pkg/run/runner.go`（自动压缩）、`pkg/run/compaction.go`（显式 /compact、worker CLI、子代理），以及测试里的手工构造。本计划的 bug 就是只改了前者。CR 时把两处一起看。
- `CompactionService(nil, nil)` 的 nil 语义是 `assembly.Service` 注释里的契约，已有测试用它构造无 runner 的服务；不要「顺手」给它加默认值。
- Part F 的按行观测仍只在内存、进程重启清空；本计划不改变这一点，只让显式压缩路径用对观测键。
- 明确延后：同名两行在 `/compact` 路径的真机（fake provider）数值演示不在本计划，Go 单测已经钉住读数来源；若 owner 要求再补，另立计划。
