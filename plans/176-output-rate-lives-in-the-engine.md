# Plan 176：输出速率（tok/s）下沉到引擎，网页概览与终端用量页读同一个数

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "LLM time.Duration" pkg/agent/trace_events.go` 为 `1`；
> `grep -c "func persistRunUsage" pkg/run/supervisor.go` 为 `1`；
> `grep -c "func SessionUsageTotalsOf" pkg/turn/model.go` 为 `1`；
> `grep -c "func UsageFacts" pkg/turn/model.go` 为 `1`；
> `grep -c "func (s \*Server) handleSessionCostSummary" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "'chat.benchUsageCache'" frontend/src/locales/index.ts` 为 `2`（中英各一）。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/agent pkg/run pkg/state pkg/turn pkg/gateway/api_extra.go frontend/src/components/workbench/OverviewTab.vue` 必须**无输出**。

## Status

- **Priority**: P2（owner 2026-10-26 要求：会话概览加"输出速率 tok/s"，且**下沉到引擎、两端共享**）
- **Effort**: M（一次加法迁移 + 一条引擎查询 + 共享投影 + 两个 surface 各一处）
- **Risk**: MED（动 schema 版本与 `/status`、`/cost-summary` 的共享投影；数值口径必须与 `175` 对齐）
- **Depends on**: `175`（分子口径：token 按物理尝试累计；本计划的分母同口径，先做 175 再做本计划最省心）
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: feature（可观测性）/ 引擎 + 两 surface

## Why this matters

owner 的要求：**会话概览里加一项 token 输出速率（tokens/s）**，而且这个数要**由引擎算出来、TUI 与 web 共用**——
不是网页自己拿两个数字除一下。理由很实在：一旦两个 surface 各自算，口径（分母是"生成时间"还是"整轮墙钟"、
是否含重试、是否含思考）就会各写一遍，然后像 `139`/`154` 那样"同一条规则修两次"。

引擎里已经有一半材料：

- **分子**：`fb_runs.usage_completion_tokens`，`SessionUsageForSession`（`pkg/state/runrt.go:452`）按 run 树求和，
  网页工作台"用量"卡读的就是它；
- **分母**：`res.Summary.Latency.LLM`——`pkg/agent` 在每次模型调用外计时（`agent.go:336-339`，
  `LatencySummary.LLM` 的注释就是 "the time spent waiting on model calls"）。**但它今天从来不落库**：
  `grep -rn "LLM\b" pkg/state` 找不到任何列，`persistRunUsage`（`pkg/run/supervisor.go:318-329`）只写 4 个 token 列 + `LLMCalls`。

所以本计划的核心是**把分母持久化**，然后在 `pkg/turn` 里算出唯一的那个数，让两端都读它：

```
引擎：fb_runs.usage_llm_ms  ──►  SessionUsageForSession / LastRunUsageForSession
      ──►  turn.StatusUsageTotals.OutputTokensPerSecond（唯一算法）
            ├──► TUI   /status 的 Usage 页（turn.UsageFacts）
            └──► web   /api/chat/sessions/:id/cost-summary ──► 工作台「概览 → 用量」卡
```

## 口径（本计划定死，两个 surface 没有第二种解释）

```
输出速率 = Σ completion_tokens / Σ llm_ms        （单位：tok/s，保留 1 位小数）
```

- **只算输出 token**（`usage_completion_tokens`）：输入 token 不参与，这是"生成速度"，不是"吞吐"。
- **分母只算模型请求的墙钟**（`Latency.LLM`）：不含工具执行、不含等审批、不含思考期间的其它工作
  （思考本身发生在模型请求内，按定义计入——那是生成的一部分）。
- **分子分母取同一批 run**：只统计 `usage_llm_ms > 0` 的 run，并且分子也只取这些 run 的 completion tokens。
  **这条是必须的**：`SessionUsageForSession` 会把失败/取消的 run 的 token 也算进去（注释明写"every run counts
  whatever its status"），而失败 run 不一定有分母——两边不同批会让速率凭空变大。
- **含重试**：`Latency.LLM` 是在 `Execute` 外层计时的，重试与退避都在其中；`175` 之后分子也含重试，
  所以两边同口径。**退避睡眠（如 32s）会被算进分母**，这是刻意的（那段时间用户确实在等），
  但如果将来看起来偏慢，正确做法是让重试层单独报"provider 时间"，而不是改这里的口径。
- 没有分母（该会话所有 run 都没有 `llm_ms`）→ **不显示这一项**，不显示 `0.0 tok/s`。

## Current state

**分子（已有）**：`pkg/state/runrt.go:452-478` 的 `SessionUsageForSession`（递归 CTE 求和）；
`:482-505` 的 `LastRunUsageForSession`。

**分母（今天的去向）**：

```go
// pkg/agent/agent.go:330-340（节选）
	duration := time.Since(start)          // 一次模型调用（重试层在里面）
	…
	stats.recordLLM(duration, msg.Usage)
```

```go
// pkg/agent/trace_events.go:38-45
type LatencySummary struct {
	Total time.Duration // the full wall-clock duration of the run
	LLM   time.Duration // the time spent waiting on model calls
	Tool  time.Duration
}
```

**落库点（只有 token，没有时间）**：

```go
// pkg/run/supervisor.go:318-329
func persistRunUsage(rt *state.RunStore, runID string, res *agent.Result) {
	…
	_ = rt.SetRunUsage(context.Background(), runID, state.LastRunUsage{
		PromptTokens:     res.Summary.Usage.InputTokens,
		CompletionTokens: res.Summary.Usage.OutputTokens,
		CacheReadTokens:  res.Summary.Usage.CacheReadInputTokens,
		CacheWriteTokens: res.Summary.Usage.CacheCreationInputTokens,
		LLMCalls:         res.Summary.LLMCalls,
	})
}
```

**共享投影（两端都读它）**：

```go
// pkg/turn/model.go:664-681
type StatusUsageTotals struct {
	InputTokens, OutputTokens, CacheRead, CacheWritten, Uncached, Requests, CacheHitPercent …
}
// pkg/turn/model.go:684-688
type StatusLastTurn struct { InputTokens, OutputTokens, CacheHitPercent int }
```

```go
// pkg/turn/model.go:991-1031（TUI 的 Usage 页与 /status 都用它）
func UsageFacts(rep StatusReport) []StatusFact { … add("Session", strings.Join(session, " · ")) … }
```

**两个出口**：

- TUI：`pkg/tui/render.go:7215-7233` 的 `buildStatusPanel` → `turn.UsageFacts(*rep)`（`tab == 1` 就是 Usage 页）；
  数据由 `pkg/tui/chat_session.go:3050` `PanelStatusReport` → `turn.BuildStatusReport`。
- web：`pkg/gateway/api_extra.go:3310` `handleSessionCostSummary` → 把 `turn.SessionUsageTotalsOf(usage)` 摊平成 JSON
  → `frontend/src/components/workbench/OverviewTab.vue:131-135` 的"用量"卡（`chat.benchUsage*` 键）。

## Scope

**In scope（只改这些）**：
- `pkg/state/schema.sql` + `pkg/state/schema_migrations.go`（`fb_runs.usage_llm_ms` + 一次迁移）
- `pkg/state/runrt.go`（`LastRunUsage.LLMMs`、`SessionUsage.LLMMs`、`SetRunUsage`、两条查询 + 一条"只统计有分母的 run"的查询）
- `pkg/run/supervisor.go`（`persistRunUsage` 写入 `res.Summary.Latency.LLM`）
- `pkg/turn/model.go`（`StatusUsageTotals`/`StatusLastTurn` 加字段与速率；`SessionUsageTotalsOf` 算法；`UsageFacts` 加一行）
- `pkg/gateway/api_extra.go`（`handleSessionCostSummary` 多两个字段）
- `frontend/src/components/workbench/OverviewTab.vue`（用量卡加一行）
- `frontend/src/locales/index.ts`（中英两个键）
- 测试：上述各包 + `frontend/src/components/workbench/OverviewTab.test.ts`（若不存在，按同目录既有组件测试的写法新建）

**Out of scope（看着相关也不要动）**：
- **口径外的任何时间**：不要把工具时间、审批等待、首 token 延迟混进这个数
- TUI 底栏（`formatComposerTokenStats`）——它只画上下文占用百分比，**不要**把速率塞进底栏
- 重试状态行、`TransportRetry`、`plans/175` 的累计逻辑
- `plans/165` 的账本（逐次明细）
- 网页"上一轮"——本计划只加会话累计那一项（owner 只要求"会话概览里加一项"）
- 计费/价格

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 迁移 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestSchema\|TestRunUsage' -count=1 -v` | PASS |
| 引擎投影 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'OutputRate\|UsageFacts' -count=1 -v` | PASS |
| 网关 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'CostSummary' -count=1 -v` | PASS |
| Go 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 前端 | `cd frontend && pnpm vitest run src/components/workbench` | PASS |
| 类型/构建 | `cd frontend && pnpm typecheck`；`cd frontend && pnpm build` | exit 0 |

## Steps

### Step 1：迁移与存储

1. `pkg/state/schema.sql`：`fb_runs` 增加

```sql
  -- Wall-clock time spent waiting on model requests during this run. The
  -- denominator of the output rate; 0 for a run that never wrote one (an
  -- older row, or a run that failed before any call returned).
  usage_llm_ms INTEGER NOT NULL DEFAULT 0,
```

2. `pkg/state/schema_migrations.go`：新增 `{version: <next>, apply: migrateStateV<N>ToV<N+1>}`，
   `apply` 里只有一句 `ALTER TABLE fb_runs ADD COLUMN usage_llm_ms INTEGER NOT NULL DEFAULT 0`，
   并把 `stateSchemaVersion` 改成同一个号。**版本号取执行时的 `stateSchemaVersion + 1`**：
   `plans/145`/`146`/`149`/`151` 分别预留了 v18–v21，若它们先落地，本计划的号就顺延在它们之后——
   **不要回头改那些计划的号**，追加到注册表末尾即可（注册表按 version 升序执行）。
3. `pkg/state/runrt.go`：
   - `LastRunUsage` 与 `SessionUsage` 增加 `LLMMs int`；
   - `SetRunUsage` + `runColumns` + `LastRunUsageForSession` + `SessionUsageForSession` 带上这一列；
   - 新增一条**只统计有分母的 run** 的查询（速率专用）：

```go
// SessionOutputRate returns the completion tokens and model-call time of the
// runs in this session's tree that recorded a model-call time, so the rate's
// numerator and denominator describe the same runs: a failed run that spent
// tokens but never wrote a duration must not inflate it.
func (s *RunStore) SessionOutputRate(ctx context.Context, sessionID string) (completionTokens int, llmMs int64, err error)
```

（同样用递归 CTE 取 run 树，`WHERE usage_llm_ms > 0`。）

**Verify**：`… -run 'TestSchema|TestRunUsage'` → PASS；并新增 `TestSessionOutputRateIgnoresRunsWithoutTime`
（两个 run：一个有 token 有 ms、一个有 token 无 ms → 只统计第一个）。

### Step 2：写入分母

`pkg/run/supervisor.go` 的 `persistRunUsage` 增加一行：`LLMMs: res.Summary.Latency.LLM.Milliseconds()`。
`PersistRunUsageFromError` 保持不写（失败路径没有可靠的模型时间——`Latency` 只在有 Result 时存在；
写 0 正好让它落在"无分母"一侧，与 Step 1 的查询规则一致）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'RunUsage' -count=1 -v` → PASS
（新增一条：一次成功 run 的 `usage_llm_ms` 等于 `Summary.Latency.LLM` 的毫秒数）。

### Step 3：引擎里唯一的算法

`pkg/turn/model.go`：

```go
// OutputTokensPerSecond is completion tokens per second of model-call time:
// the one place this rate is computed, so the terminal's Usage page and the
// web's overview card cannot disagree. Zero when no run recorded a duration.
func OutputTokensPerSecond(completionTokens int, llmMs int64) float64 {
	if llmMs <= 0 || completionTokens <= 0 {
		return 0
	}
	return math.Round(float64(completionTokens)*1000/float64(llmMs)*10) / 10
}
```

- `StatusUsageTotals` 增加 `GenerationMs int64` 与 `OutputTokensPerSecond float64`；
- `SessionUsageTotalsOf` 保持纯投影（**不查库**），速率由 `BuildStatusReport` 里那条新查询填：

```go
	if tokens, ms, err := src.RunStore.SessionOutputRate(ctx, sid); err == nil {
		rep.Session.GenerationMs = ms
		rep.Session.OutputTokensPerSecond = OutputTokensPerSecond(tokens, ms)
	}
```

（`StatusRunStore` 接口相应加一个方法——它是 `/status` 读的最小切片，所以**必须**加在那里，
否则两个 surface 的数据源会分叉。`LastTurn` 的速率本计划**不加**，见 Out of scope。）

- `UsageFacts`：在 `Session` 那一行追加一段（同一行，不新增行，控件与既有风格一致）：

```go
	if rep.Session.OutputTokensPerSecond > 0 {
		session = append(session, fmt.Sprintf("%.1f tok/s", rep.Session.OutputTokensPerSecond))
	}
```

**Verify**：`… -run 'OutputRate|UsageFacts'` → PASS（用例见 Test plan）。

### Step 4：两个出口

1. **web DTO**：`handleSessionCostSummary` 增加 `"output_tokens_per_second": totals.OutputTokensPerSecond`
   与 `"generation_ms": totals.GenerationMs`。
2. **web 卡片**：`OverviewTab.vue` 的"用量"卡，在"模型请求"那一行之后加：

```vue
      <div v-if="cost?.outputTokensPerSecond" class="bench-kvs">
        <span class="k">{{ t('chat.benchUsageRate') }}</span>
        <span class="v">{{ t('chat.benchUsageRateValue', { rate: cost.outputTokensPerSecond.toFixed(1) }) }}</span>
      </div>
```

3. **i18n**（`frontend/src/locales/index.ts`，中英各一处，紧跟 `chat.benchUsageCache` 之后）：
   - 中：`'chat.benchUsageRate': '输出速率'`、`'chat.benchUsageRateValue': '{rate} tok/s'`
   - 英：`'chat.benchUsageRate': 'Output rate'`、`'chat.benchUsageRateValue': '{rate} tok/s'`

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'CostSummary' -count=1 -v` → PASS；
`cd frontend && pnpm vitest run src/components/workbench` → PASS；`pnpm typecheck` → exit 0。

### Step 5：回归

**Verify**：Go 五个包 + 前端两套测试与基线相比无新增 FAIL；`cd frontend && pnpm build` exit 0。

## Test plan

- `pkg/state`：`TestSessionOutputRateIgnoresRunsWithoutTime`（分子分母同批）；`TestRunUsageStoresModelCallTime`。
- `pkg/turn`：`TestOutputTokensPerSecondRoundsToOneDecimal`（`1000 tokens / 3000 ms = 333.3`；
  `0 ms → 0`；`0 tokens → 0`）；`TestUsageFactsCarriesTheOutputRate`（有速率时那一行含 `tok/s`，
  无速率时**不含**）。
- `pkg/gateway`：`TestCostSummaryCarriesOutputRate`（两个字段；无分母时 `output_tokens_per_second` 缺省/0）。
- 前端：`OverviewTab.test.ts` —— mock `sessionCostSummary` 返回 `outputTokensPerSecond: 18.4` →
  卡片里出现 `18.4 tok/s`；返回 0 → **不出现**这一行。
- TUI 侧不新增测试：它读的是 `UsageFacts`，上面那条已覆盖。

## Done criteria

机器可检查，全部满足：

- [ ] `stateSchemaVersion` 已 +1，且临时 home 上 `TestSchema` 通过（迁移能从旧版本升上来）
- [ ] `grep -c "usage_llm_ms" pkg/state/schema.sql` 为 `1`；`grep -c "func (s \*RunStore) SessionOutputRate" pkg/state/runrt.go` 为 `1`
- [ ] `grep -c "func OutputTokensPerSecond" pkg/turn/model.go` 为 `1`（**唯一**算法）
- [ ] `grep -c "OutputTokensPerSecond" pkg/gateway/api_extra.go` 为 `1`
- [ ] `grep -c "tok/s" frontend/src/locales/index.ts` 为 `2`（中英各一）
- [ ] 上面四条 Go 测试 + 前端用例 PASS；五个包与前端两套回归与基线相比无新增 FAIL
- [ ] `cd frontend && pnpm typecheck`、`pnpm build` exit 0
- [ ] `git status --porcelain` 只显示 Scope 里的文件（外加 `pkg/gateway/dist/` 的构建产物）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. `stateSchemaVersion` 已经不是本计划基线（`17`）：说明 `145`–`151` 中有计划先落地了。
   **按"取当前值 +1、追加到注册表末尾"处理并按报告说明**，不要改别人的计划文件。
3. 你发现 `res.Summary.Latency.LLM` 在某些成功路径上是 0（例如某条非流式回放路径没有计时）：
   报告具体路径——那会让速率永远不出来，属于本计划必须一起修的前提，但要先确认影响面。
4. 你想把速率也加到 TUI 底栏、或加上"上一轮"的速率：**不要**（Out of scope，owner 只要求概览一项，
   且底栏的宽度预算另有契约）。
5. 你想顺手把"模型请求"的文案改掉：**不要**（`175` 已定：重试对用户透明，标签不动）。

## Maintenance notes

- 这个数的含义是**生成速度**，不是"对话吞吐"。任何人想把工具时间并进分母，请先回答："那它就不再是 tok/s 了，
  而是整轮平均吞吐"——两者都要的话，是两项，不是一项。
- 分母只在成功 run 上写（失败路径没有可靠的模型时间）；因此分子的查询必须**同批**。这条规则在
  `SessionOutputRate` 的注释里，改查询时不要拆开。
- 将来 `plans/165` 的账本落地后，速率可以由逐次 attempt 的时间与 token 重算——届时`OutputTokensPerSecond`
  仍然是唯一算法，只是数据源挂着谁的问题；不要让两个算法并存。
