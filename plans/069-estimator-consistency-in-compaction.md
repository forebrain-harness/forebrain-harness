# Plan 069：估算口径一致——观测开销按估算器分开记；压缩内部（摘要预算、裁剪、卡片数字）与上下文面板都用会话模型的估算器

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`。
>
> **前置**：Plan 065、066、067 已在工作区落地。
> **Drift check（先跑）**：`grep -n 'DisableDownload' pkg/llm/tokestimate.go` 有输出；`grep -n 'func (g \*compactRunGuard) prefill(key string, current func() int) int' pkg/run/llm_middleware.go` 有 1 行；
> `grep -n 'total := llm.Project(provider, model, llm.EstimateMessagesWith(est, messages)+estimateToolsTokens(est, tools))' pkg/run/compaction.go` 有 1 行。不符 → STOP。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW–MED（改观测 API 与压缩内部的计数口径）
- **Depends on**: plans/065、066、067（`pkg/run/llm_middleware.go` 与 066 重叠，须串行）
- **Category**: correctness / semantic gap
- **Planned at**: commit `fa4af8a` + 工作区（含 065–067），2026-10-16

## Why this matters

1. **观测开销混口径**：`pkg/llm/observed.go` 把"服务端实数 − 本地估算"的最大差值记为 `Overhead`，按 provider/model 存，**只增不减**，再由 `Project` 加回到之后的估算上。
   但估算器在一个进程里可能更换：随包文件缺失时先用默认分词器或 rune/4、下载完成后切到该模型的分词器；配置热重载改了覆盖项。旧估算器下学到的开销（例如 rune/4 少算 12% → 学到几万 token 的开销）会永久加到新估算器的结果上，压缩提前触发。
2. **压缩内部仍用默认估算器**：042 之后两个检查点按会话模型估算，但压缩本身——摘要请求能否放进窗口（`localCompact` 的 `budget` 判断与减半）、裁剪历史（`trimHistoryToFit`/`groupHistory`）、保留用户原话的预算（`retainedRealUserMessages`）、卡片上的 `TokensBefore/After`——还用 `llm.EstimateMessages`（默认 gpt-o200k）。
   不同家族在同一语料上的计数差可达数个百分点（实测：o200k 6.34M、qwen 6.73M、claude 旧分词器更多），判断"放得下"偏乐观会多一次被拒再减半。
3. **上下文面板口径不一致**：`pkg/assembly/hook.go:136` 的 `ConversationTokens`（web 工作台上下文面板与 `ContextPressure`）用默认估算器，和压缩判定不是同一个数。

## Current state（全部已核实）

- `pkg/llm/observed.go`：`type Observation struct{ ProvenInput, RejectedInput, Overhead int }`；`RecordSuccess(provider, model string, realInput, estimated int)`（`:84-118`，`gap := realInput - estimated; if gap > obs.Overhead { obs.Overhead = gap }`）；
  `RecordOverflow(provider, model string, realInput, estimated int)`；`Get(provider, model)`；`Project(provider, model string, estimated int) int`（`:168-177`，加 `obs.Overhead`）；`EffectiveWindow`。键由 `observationKey(provider, model)` 生成。
- 生产调用：`pkg/run/compaction.go:152` `llm.Project(provider, model, llm.EstimateMessagesWith(est, messages)+estimateToolsTokens(est, tools))`；`pkg/run/llm_middleware.go` `observeSuccess(ctx, res, estimated)` → `llm.RecordSuccess(provider, model, realInput, estimated)`（`:415-424`），`observeOverflow` → `llm.RecordOverflow`。`Execute` 里 `est := llm.EstimatorFor(provider, model)`（`:282`）。
- 测试：`pkg/llm/observed_test.go`（约 20 处用旧 API 与 `obs.Overhead`）、`pkg/run/compaction_test.go`（3 处）。
- 压缩内部 `pkg/assembly/service.go`：`ManualCompactSession` 的 `beginCompaction(..., llm.EstimateMessages(messages))`（`:118`）、`TryCompactOnMessages` 同（`:304`）、`compactMessages` 的 `before := llm.EstimateMessages(messages)`（`:348`）与 `TokensAfter: llm.EstimateMessages(replacement)`（`:416`）、
  `localCompact` 的 `withSummaryProgress(ctx, llm.EstimateMessages(messages))`（`:477`）、`llm.EstimateMessages(messages) <= budget`（`:479`）、`budget = llm.EstimateMessages(input)`（`:534`）、`trimHistoryToFit(messages, budget)`（`:520`）、`retainedRealUserMessages(...)`（`:484,:502,:543`，定义 `:661`，内部 `llm.EstimateText(text)` `:673`）。
  `pkg/assembly/trim.go`：`groupHistory`（`:25-58`，`llm.EstimateMessage`）、`trimHistoryToFit`（`:73` 起，`llm.EstimateMessage`）。`Service.PrimaryModel func(ctx) (string, string)` 已存在（轮中为 `agentModelFor(ctx)`，子代理按自己的模型）。
  **不改**：`pkg/assembly/service.go` 的 `AutoCompactSession`（038 已用 `decision.Usage`）；`summaryInputBudget`（只算窗口）；`pkg/assembly/compact_lifecycle.go:253` 的流式摘要进度（显示用）。
- 上下文钩子：`pkg/assembly/hook.go:128-137`（`res.ConversationTokens = state.TokenCountWithEstimation(turns)`）；`pkg/assembly/runtime_hook.go:28-47` `NewRuntimeHook` 已有 `params.PrimaryModel(sessionID)`，用来注入 `hook.ModelLimitsProvider`——**照它的写法**注入估算器。
  `state.TokenCountWithEstimationFor(turns, e)` 已存在（`pkg/state/usage.go`）。
- 分层：`pkg/assembly` 第 2 层，可用 `pkg/llm`、`pkg/state`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 取库 | `scripts/fetch-libtokenizers.sh` | 成功 |
| 测试 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/ -count=1`；`CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly/ ./pkg/run/ ./pkg/state/ -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/ ./pkg/assembly/ ./pkg/run/`；`gofmt -l pkg` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 重新生成并保留改动 |

## Scope

**In scope**：`pkg/llm/observed.go`、`pkg/llm/observed_test.go`、`pkg/run/compaction.go`、`pkg/run/llm_middleware.go`、`pkg/run/compaction_test.go`、`pkg/assembly/service.go`、`pkg/assembly/trim.go`、`pkg/assembly/hook.go`、`pkg/assembly/runtime_hook.go`、`pkg/assembly/*_test.go`、`pkg/architecture/testdata/graph.json`。

**Out of scope**：`AutoCompactSession`；`summaryInputBudget`；记忆检索注入预算（`pkg/assembly/engine.go` 的 `EstimateText`，近似即可）；`RecordOverflow` 的语义（见 Maintenance notes）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：开销按估算器分开

`pkg/llm/observed.go`：
- 新增 `RecordSuccessFor(provider, model, estimator string, realInput, estimated int)` 与 `ProjectFor(provider, model, estimator string, estimated int) int`：`ProvenInput`/`RejectedInput` 仍按 provider/model 记（它们是关于真实窗口的事实，与估算器无关），开销改存进按 `observationKey + "\x00" + estimator` 区分的表。
- 旧的 `RecordSuccess`/`Project` 保留为 `estimator == ""` 的包装（注释写明"for callers that have no estimator name; production callers use the For variants"），`Observation.Overhead` 继续反映 `""` 估算器下的值，现有测试不改断言。
- `ResetObservedForTest` 一并清空新表。
- 包文档注释（文件开头那段）补一句：开销按估算器分开，因为估算器换了，"服务端实数 − 本地估算"的差就不再是同一个量。

**Verify**：`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/ -count=1 -run 'Observ|Project|Overhead'` 全绿。

### Step 2：生产调用改用 For 版本

- `pkg/run/compaction.go`：`llm.ProjectFor(provider, model, est.Name(), ...)`。
- `pkg/run/llm_middleware.go`：`observeSuccess` 增加 `est llm.Estimator` 参数并调用 `llm.RecordSuccessFor(provider, model, est.Name(), realInput, estimated)`；两个调用点传 `est`。`observeOverflow` 不改。

**Verify**：`grep -n 'llm.Project(\|llm.RecordSuccess(' pkg/run/*.go | grep -v _test` 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` 全绿。

### Step 3：压缩内部用会话模型估算器

`pkg/assembly`：
1. 新增 `func (s Service) estimator(ctx context.Context) llm.Estimator`：`PrimaryModel` 为 nil 时返回 `llm.DefaultEstimator()`，否则 `llm.EstimatorFor(s.PrimaryModel(ctx))`。
2. `trim.go`：`groupHistory`、`trimHistoryToFit` 增加 `e llm.Estimator` 参数，内部用 `llm.EstimateMessageWith(e, ...)`；`retainedRealUserMessages` 同样加参数并用 `llm.EstimateTextWith(e, text)`。
3. `service.go`：Current state 列出的 `:118 :304 :348 :416 :477 :479 :534` 全部换成同一个 `est`（在函数开头取一次，往下传；`compactMessages`→`localCompact` 之间以参数传递，不要在每处各取一次），`:484 :502 :520 :543` 的调用传 `est`。
4. 其他 `groupHistory`/`trimHistoryToFit`/`retainedRealUserMessages` 的调用方（`grep -rn` 找）一并传估算器；测试里传 `llm.DefaultEstimator()`。

**Verify**：`grep -n 'llm.EstimateMessages(\|llm.EstimateMessage(\|llm.EstimateText(' pkg/assembly/service.go pkg/assembly/trim.go` 只剩 Out of scope 允许的（应为无输出）；`CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly/ -count=1` 全绿。

### Step 4：上下文面板

`hook.go` 的 `Hook` 增加 `EstimatorProvider func(sessionID string) llm.Estimator`；`Assemble` 里有它时用 `state.TokenCountWithEstimationFor(turns, h.EstimatorProvider(sid))`，否则维持原调用。
`runtime_hook.go` 在 `params.PrimaryModel != nil` 时注入：`func(sid string) llm.Estimator { p, m := params.PrimaryModel(sid); return llm.EstimatorFor(p, m) }`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly/ -count=1` 全绿。

## Test plan

1. `pkg/llm/observed_test.go` 新增 `TestOverheadIsPerEstimator`：对同一 provider/model 用估算器 "a" 记一次大差值、"b" 记一次小差值；`ProjectFor(..., "a", x)` 与 `ProjectFor(..., "b", x)` 分别加各自的开销；`EffectiveWindow` 不受估算器影响（`ProvenInput` 共享）。
2. `pkg/run/compaction_test.go` 新增 `TestObservationRecordedUnderActiveEstimator`：`SetEstimatorForTest` 给活动模型注入名为 "fake" 的估算器，`WrapRecoverableLLM` 包一个返回 `Usage{InputTokens: N}` 的假 LLM，调用一次后 `ProjectFor(p, m, "fake", 0) == N − 假估算器对请求的估算`，且 `ProjectFor(p, m, "", 0) == 0`。
3. `pkg/assembly/service_test.go` 新增 `TestLocalCompactBudgetsWithSessionEstimator`：`PrimaryModel` 返回一个用 `SetEstimatorForTest` 注入了"放大 10 倍"估算器的模型；构造一段默认估算下能放进 `summaryInputBudget`、放大后放不进的历史，断言 `conversation` 摘要路径**不**被调用（因为放不下），而是走 `trimHistoryToFit` 分支（可用假 LLM 记录收到的消息条数 < 原条数）。
4. `pkg/assembly`：`TestTrimHistoryToFitUsesGivenEstimator`——同一历史与预算，放大估算器下保留的组比默认估算器少。
5. `pkg/assembly`：`TestHookConversationTokensUseSessionEstimator`——`Hook.EstimatorProvider` 返回放大估算器时 `ConversationTokens` 大于不设时。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Test plan 5 个测试存在且 PASS
- [ ] `grep -n 'llm.Project(\|llm.RecordSuccess(' pkg/run/*.go | grep -v _test` 无输出
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符（065–067 未落地）。
2. 改口径后现有压缩测试结果变化——测试二进制里 `EstimatorFor` 默认是 rune/4，与默认估算器相同，不应有变化；有变化说明某处取了不同的估算器，停下报告。
3. `groupHistory`/`trimHistoryToFit` 还有 Scope 外的调用方。

## Maintenance notes

- 以后新增"往服务端发之前估算大小"的地方，一律用会话模型的估算器（`Service.estimator(ctx)` 或 `llm.EstimatorFor`），并与观测用同一个估算器名。
- `RecordOverflow` 在服务端没报用量时会用本地估算作为被拒大小（估算器单位），与真实窗口比较有口径偏差——这是既有行为，本计划未改；若要改，被拒大小也应按估算器分开。
