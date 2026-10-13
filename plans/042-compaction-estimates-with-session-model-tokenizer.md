# Plan 042：压缩判定与用量观测按"本次调用所用模型"的分词器估算

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **前置**：Plan 038、041 已在工作区落地（041 提供 `llm.EstimatorFor` / `EstimateMessagesWith` / `EstimateTextWith` / `SetEstimatorForTest`）。先跑 `scripts/fetch-libtokenizers.sh`。
>
> **Drift check（先跑）**：`grep -n 'func EstimatorFor\|func EstimateMessagesWith\|func SetEstimatorForTest' pkg/llm/tokenizers.go` 三者都在；
> `grep -n 'msg.RunID' pkg/state/usage.go` 有 1 行（038）；
> `git diff --stat 005c1a8 -- pkg/run/compaction.go pkg/run/llm_middleware.go` 无输出。不符 → STOP。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（改变轮前、轮中两个压缩检查点的计数口径；两者必须保持同一口径）
- **Depends on**: plans/038-pre-turn-anchor-only-trusts-own-usage.md、plans/041-tokenizer-assets-and-per-model-estimator.md
- **Category**: bug / accuracy
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

041 之后进程里有了每个模型家族自己的分词器，但压缩判定仍调用不带模型的 `llm.EstimateMessages`（默认分词器 gpt-o200k）。
本机实测（只读状态库）：用 cl100k 估算的增量与服务端真实增量之比，deepseek-v4-flash 中位数 1.63、glm-5.3 为 1.30、glm-5.3-flash 为 1.35——对某些模型估算系统性偏差很大。
本计划让两个压缩检查点（轮前 `autoCompactDecision`、轮中 `shouldCompactMessages`）以及给观测开销用的"已发送估算值"（`RecordSuccess` / `RecordOverflow`）都用**本次调用所用模型**的估算器。
三者必须同口径：观测开销 `Overhead = 服务端实数 − 本地估算`，若估算口径和判定口径不同，学到的开销就会加错地方。

## Current state（全部已核实；行号以 2026-10-11 主树工作区为准，`runner.go` 含 owner 未提交改动）

- 轮中判定 `pkg/run/compaction.go`：
  - `:42-58` `estimateToolsTokens(tools)`：对每个工具 `llm.EstimateText(Name)+EstimateText(Description)+EstimateText(json(InputSchema))+8`。
  - `:114-131` `compactIfNeeded(ctx, messages, snapshotRaw, deps, bodyPrefixTokens, tools)`：`provider, model := deps.activeModel(ctx)` → `shouldCompactMessages(..., provider, model)`。
  - `:133-160` `shouldCompactMessages`：`total := llm.Project(provider, model, llm.EstimateMessages(messages)+estimateToolsTokens(tools))`；`body_after_prefix` 时调 `bodyAfterPrefixTokens(messages, total)`。
  - `:162-182` `bodyAfterPrefixTokens`：`max(0, total-llm.EstimateMessages(messages[:prefixEnd+1]))`。
- 轮中调用链 `pkg/run/llm_middleware.go`：
  - `:159-170` `compactRunGuard.set(runID, compacted)`：`g.prefillTokens = llm.EstimateMessages(compacted)`。
  - `:217-229` `Execute`：`sent := llm.EstimateMessages(messages) + estimateToolsTokens(tools)`，成功 `r.observeSuccess(ctx, res, sent)`，溢出 `r.observeOverflow(ctx, lastErr, sent)`；强制压缩后 `r.observeSuccess(ctx, res2, llm.EstimateMessages(compacted)+estimateToolsTokens(tools))`（`:255`）。
  - `:300-336` `applyRunScopedCompact`：`prefillTokens := r.runGuard.prefill(runID, llm.EstimateMessages(messages))`；`:334` `r.runGuard.set(runID, head)`；`:370`（`forceCompact`）`r.runGuard.set(..., compacted)`。
  - `:341-360` `observeSuccess` / `observeOverflow` 已用 `r.compactDeps.activeModel(ctx)` 取 provider/model。
- 轮前判定 `pkg/assembly/service.go:208-266` `autoCompactDecision`：`usage := state.TokenCountWithEstimation(turns)`（`:217`），之后才 `provider, model = s.PrimaryModel(ctx)`（`:219-221`）；`body_after_prefix` 时 `llm.EstimateMessages(checkpoint.ReplacementHistory)` 与 `state.BodyAfterPrefixTokenCount(turns, estimatedPrefill)`（`:256-265`）。
- `pkg/state/usage.go`：`TokenCountWithEstimation` → `estimateTurns` → `estimateMessage`（`:293-303`，把存储行投影成请求消息后 `llm.EstimateMessage(parsed)`）；`BodyAfterPrefixTokenCount`（`:203-214`）调 `TokenCountWithEstimation`。
- provider/model 的来源已有：轮中 `deps.ActiveModel`（`pkg/run/runner.go:1032-1036` → `r.agentModelFor(ctx)`，子代理按自己的模型，见 `TestCompactionSizesATypedSubagentByItsOwnModel`）；轮前 `Service.PrimaryModel`（`runner.go:1005-1007`）。
- 分层：`pkg/state` 第 1 层可以导入 `pkg/llm`（已导入）。`pkg/assembly`、`pkg/run` 同理。
- 测试样例：`pkg/run/compaction_test.go`（真实包装链的压缩测试）、`pkg/assembly/service_test.go`（`newCompactTestStore`、`ShouldAutoCompactSession` 用法；038 新增了带 run 写入的帮手）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 取库 | `scripts/fetch-libtokenizers.sh` | 成功 |
| 相关测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/assembly/... ./pkg/run/... ./pkg/llm/... -count=1` | 全部 `ok` |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./...`；`gofmt -l pkg` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 重新生成并连同提交 |

## Scope

**In scope**：`pkg/state/usage.go`、`pkg/assembly/service.go`、`pkg/run/compaction.go`、`pkg/run/llm_middleware.go`、它们的测试文件、`pkg/architecture/testdata/graph.json`。

**Out of scope**：
- 其他调用 `llm.EstimateText` / `EstimateMessages` 的地方（记忆检索条目 `pkg/assembly/engine.go`、裁剪 `pkg/assembly/trim.go`、压缩进度、`pkg/llm/openai/responses_llm.go`、`pkg/assembly/hook.go:136` 的 `ConversationTokens`）：继续用默认估算器。
- `llm.Project` / `RecordSuccess` / `RecordOverflow` 的算法（`pkg/llm/observed.go`）。
- 估算结果缓存（性能优化，另立计划）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：`pkg/state` 提供带估算器的版本

在 `pkg/state/usage.go`：
- `estimateMessage(msg)` 改为 `estimateMessageWith(e llm.Estimator, msg)`，最后一行 `llm.EstimateMessageWith(e, parsed)`；`estimateTurns` 同理带 `e`。
- 新增 `TokenCountWithEstimationFor(turns []Message, e llm.Estimator) int` 与 `BodyAfterPrefixTokenCountFor(turns []Message, estimatedPrefill int, e llm.Estimator) int`；原 `TokenCountWithEstimation` / `BodyAfterPrefixTokenCount` 保留签名，委托 `llm.DefaultEstimator()`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` 全绿（行为对默认估算器不变）。

### Step 2：轮前判定

`autoCompactDecision`：把取 `provider, model` 的几行移到算 `usage` 之前；`est := llm.EstimatorFor(provider, model)`；`usage := state.TokenCountWithEstimationFor(turns, est)`；
`body_after_prefix` 分支用 `llm.EstimateMessagesWith(est, checkpoint.ReplacementHistory)` 与 `state.BodyAfterPrefixTokenCountFor(turns, estimatedPrefill, est)`。`ManualCompactSession` / `AutoCompactSession` 里给 `beginCompaction` 的估算（038 已改为 `decision.Usage` 的那处除外）不改。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly/... -count=1` 全绿。

### Step 3：轮中判定与观测

1. `pkg/run/compaction.go`：`estimateToolsTokens(tools)` 改为 `estimateToolsTokens(e llm.Estimator, tools)`（内部用 `llm.EstimateTextWith(e, ...)`）；`shouldCompactMessages` 开头 `est := llm.EstimatorFor(provider, model)`，`EstimateMessages` → `EstimateMessagesWith(est, ...)`；`bodyAfterPrefixTokens` 加参数 `e`。
2. `pkg/run/llm_middleware.go`：
   - `Execute` 开头（`applyRunScopedCompact` 之前）`provider, model := r.compactDeps.activeModel(ctx)`；`est := llm.EstimatorFor(provider, model)`；`sent` 与强制压缩后的估算都用 `est`。把 `est` 传给 `applyRunScopedCompact` 与 `forceCompact`（加参数）。
   - `applyRunScopedCompact` 的 `prefill(runID, llm.EstimateMessagesWith(est, messages))`。
   - `compactRunGuard.set(runID, compacted)` 改为 `set(runID string, compacted []llm.Message, prefillTokens int)`，由调用方用 `est` 算好传入（两个调用点）。
   - `observeSuccess` / `observeOverflow` 不变（它们的 `estimated` 参数现在已是按模型的估算）。
3. 加英文注释说明三处必须同口径（判定、prefill 基线、观测），理由同 "Why this matters"。

**Verify**：`grep -n 'llm.EstimateMessages(' pkg/run/compaction.go pkg/run/llm_middleware.go` 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/... -count=1` 全绿。

### Step 4：测试

见 Test plan。

### Step 5：真机验收（owner 要求：fake driver 覆盖不到的流程要在主树用 glm-5.3 真机跑通）

1. 主树 `make build`。隔离 `FOREBRAIN_HOME`（例如 `$HOME/fb-042-acc`，**不要**放 `$TMPDIR`、不要碰真实 `~/.forebrain`）：精简 `forebrain.yaml`，只配一个 `zhipuai` / `glm-5.3`（`base_url: https://open.bigmodel.cn/api/coding/paas/v4`，`api_key: ${ZHIPUAI_API_KEY}`），并设 `compact.model_auto_compact_token_limit: 30000`（逼出轮中压缩）。
2. 用启动脚本注入密钥：`eval "$(grep '^ZHIPUAI_API_KEY=' ~/.forebrain/.env)"; export ZHIPUAI_API_KEY`，密钥不得出现在命令行或拷贝的文件里。
3. 在独立 tmux 会话里启动 TUI，让模型依次读取仓库里几个大文件（例如 `pkg/run/llm_middleware.go`、`pkg/assembly/service.go`、`pkg/state/session_store.go`），直到出现 "Context compacted" 卡片。
4. 核对：`$FOREBRAIN_HOME/logs/info.log`（或 debug.log）里有 `tokenizer ready id=glm-5`；状态库（`sqlite3 -readonly`）`fb_session_events` 里有 `context_compacted`，`reason=model_context_window_pressure`；回合正常结束无报错。

**Verify**：四点成立；报告附 tmux 截取片段与两条查询结果。

## Test plan

1. `pkg/state/action_service_test.go`（或 `usage_test.go`）`TestTokenCountWithEstimationForUsesGivenEstimator`：一个固定返回 `len(text)` 的假估算器（`Name()=="fixed"`），无锚点行时 `TokenCountWithEstimationFor` 等于按该估算器手算的值，且与默认版本结果不同。
2. `pkg/assembly/service_test.go` `TestAutoCompactDecisionUsesSessionModelEstimator`：`PrimaryModel` 返回 `("test-provider","model-x")`（不在目录 → 阈值 180000）；写若干无用量的行，使默认（rune/4）估算远低于 180000；
   用 `llm.SetEstimatorForTest("test-provider","model-x", 放大 100 倍的假估算器)` 后 `ShouldAutoCompactSession` 为 `true`；还原后为 `false`。
3. `pkg/run/compaction_test.go` `TestShouldCompactMessagesUsesActiveModelEstimator`：同样思路，直接调 `shouldCompactMessages`，假估算器放大 → `true`，默认 → `false`。
4. `pkg/run/compaction_test.go` `TestObservedEstimateMatchesDecisionEstimator`：用 `WrapRecoverableLLM` 包一个返回 `Usage{InputTokens: N}` 的假 LLM，`ActiveModel` 返回一个设了假估算器的模型；调用一次后 `llm.Get(provider, model).Overhead` 等于 `N − 假估算器对同一请求（消息 + 工具）的估算`（先 `llm.ResetObservedForTest()`）。
5. 每个测试结束都要还原 `SetEstimatorForTest` 的覆盖。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Test plan 4 个测试存在且 PASS
- [ ] `grep -n 'llm.EstimateMessages(' pkg/run/compaction.go pkg/run/llm_middleware.go` 无输出；`grep -n 'EstimatorFor' pkg/assembly/service.go pkg/run/compaction.go pkg/run/llm_middleware.go` 三个文件都有
- [ ] Step 5 真机验收四点成立
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符，或 038/041 未落地。
2. 某个现有压缩测试的结果因口径变化而改变——测试二进制里 `EstimatorFor` 应当总是 rune/4（041 的保护），出现变化说明保护失效，停下报告。
3. `compactRunGuard` 的 prefill 基线改动导致 `TestOrchestrationAdoptsCompactedSession` 或 `TestCompactionKeepsInFlightSteersOutOfTheCheckpoint` 失败两次。
4. 真机验收里没有出现 "tokenizer ready id=glm-5"，或出现估算相关报错。

## Maintenance notes

- 轮前与轮中两个检查点、prefill 基线、观测估算**必须**用同一个 `llm.EstimatorFor(provider, model)`；以后新增检查点照此办理，否则一个检查点总先触发、另一个形同虚设（见 `llm.EstimateMessages` 的文档注释）。
- 换模型的会话：轮前判定按"当前要用的模型"估算新增部分，锚点仍是 038 规定的原生用量。
- 评审重点：`est` 在 `Execute` 里只取一次并贯穿；`set` 的新签名两个调用点都改了；没有残留不带估算器的计数。
