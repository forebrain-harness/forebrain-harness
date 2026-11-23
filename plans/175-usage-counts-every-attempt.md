# Plan 175：用量按物理尝试累计（重试对用户保持透明，不加任何新界面）

> **Addendum（2026-10-27 执行批，REVISE 依据）**：执行者触发 STOP 4——三个上层消费者依赖 `res.Usage` 的
> "落定那次尝试自己的用量"旧语义：`recoverableLLM.observeSuccess`（token 估算学习：ProvenInput/overhead，
> 总和会虚标 ~N 倍）、`observeOverflow`（溢出学习：RejectedInput 取 min，总和会虚高）、`llmAttemptLedger.settle`
> （plan 165 的行级语义：终局行=落定尝试自己的 token）。复核记账拓扑后确认：**最内层 `executeWithAccounting`
> 每次尝试都 `acc.ObserveUsage(res.Usage ?? UsageFromError(err))`（llm_middleware.go:881-885）**——Step 2 落地后
> `fb_runs` 总量与子代理 delta 自动变为贴账单口径，**契约 #1 选错了载体**。修订后的设计：
> ① `Result.Usage` 语义**不变**（落定尝试自己的）——三个消费者零改动；② 新增 `Result.Attempts int`（重试层
> 计数，0=未知视为 1），`trace.go` 的 `llmCalls += max(1, res.Attempts)`；③ 失败返回处错误带**三件套**：
> 合并总量（`UsageFromError`，供 `PersistRunUsageFromError`）+ `attempts` + **最后一次尝试自己的用量**
> （`ExecuteError` 新增 `lastAttempt` 字段，新增 `LastAttemptUsageFromError`；`observeOverflow` 改用它）；
> ④ `NewExecuteError` **继承**内层 ExecuteError 的 attempts/lastAttempt（否则 `mergeErrorUsageTotal` 的重包会
> 遮蔽，`runner.go:1634-1646`）；⑤ KeepPartial 的最终错误同样挂三件套；⑥ 成功路径不合并 usage、不改
> `Result.Usage`——账单由 accumulator 路径承载。原 Step 3 的 `addUsage/mergeUsage` 仍需要（合并挂到失败错误上），
> 但**不再**写进 `result.Usage`。

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func NewExecuteError" pkg/llm/errors.go` 为 `1`；
> `grep -c "func UsageFromError(err error)" pkg/llm/errors.go` 为 `1`（用带签名的完整串：同文件还有一个
> `UsageFromErrorForSupervisor`，裸搜 `func UsageFromError` 会数到 `2`）；
> `grep -c "func (r \*streamRetryLLM) Execute" pkg/llm/stream_retry.go` 为 `1`；
> `grep -c "s.llmCalls++" pkg/agent/trace.go` 为 `1`；
> `grep -c "LLMCalls:         res.Summary.LLMCalls" pkg/run/supervisor.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/llm pkg/agent pkg/run pkg/state` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/agent ./pkg/run ./pkg/state -count=1`（超时给足 900 s）。

## Status

- **Priority**: P2（owner 2026-10-26 定口径：用量按**物理尝试**计）
- **Effort**: M（4 个适配器各一处 + 两个重试循环 + 计数透传 + 测试）
- **Risk**: MED（改的是"花了多少 token"这个对外数字的口径；**不改**任何界面与文案）
- **Depends on**: 无
- **Blocks**: `176`（输出速率的分母必须与这里的口径一致：都含重试）
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（口径）+ observability

## Why this matters

owner 的裁定（2026-10-26）：**token 用量按物理尝试累计；重试必须尽量对用户透明**——
所以本计划**不加**任何"含重试""尝试 N 次"之类的文案，也不加新的界面项，只把数字本身算对。

今天的实际行为（已逐条核实）：

| 数字 | 今天的来源 | 重试的影响 |
|---|---|---|
| `fb_runs.usage_*`（网页工作台"用量"卡读它） | `res.Summary.Usage` ← `llm.UsageAccumulator` ← **每次调用只 observe 一次 `res.Usage`**（`pkg/run/llm_middleware.go:865-875`） | **低估**：被重试掉的那次用量丢失 |
| `fb_runs.usage_llm_calls`（同一张卡的"模型请求"） | `res.Summary.LLMCalls` ← `pkg/agent/trace.go:54` 每次 `Execute` +1 | **低估**：10 次重试仍记 1 |
| TUI `Tracker` 的 run/session 计数 | `sink.OnUsage` 的流式 delta（每次尝试都报） | **偏高**，但**今天不上屏**（`formatComposerTokenStats` / `formatWorkingCounters` 都不画 in/out） |

所以三处互不相等，而唯一上屏的那处（工作台卡）是**低估**。owner 选的是"贴账单"：
失败尝试的用量也要算进去，并且"请求次数"直接变成物理尝试数（**复用同一列、不加新列、不改文案**）。

## Current state

失败尝试的用量**今天有没有可能拿到**？能——适配器在尝试内已经逐事件累加过，只是失败返回时丢掉了：

`pkg/llm/anthropic/agent_llm.go:219-241`（每次尝试内部自己算 delta）：

```go
	var latestUsage anthropicapi.Usage
	…
	reportUsage := func(input, output, cacheCreationInput, cacheReadInput int64) { … }
```

`pkg/llm/errors.go:180-250`（**已经预见到这件事**，但没人用）：

```go
// NewExecuteError wraps err with usage, always creating a new wrapper even
// when err already carries one.
// …
// Callers that are recording the usage of the attempt they just made want this one.
func NewExecuteError(err error, usage *Usage) error { … }

func UsageFromError(err error) *Usage { … }
```

`grep -rn "NewExecuteError(" pkg/ --include=*.go` → 定义之外**只有一个**调用点：`pkg/run/llm_middleware.go:795`
的 `attachUsageToError`（usage 记账中间件用它把**最终** usage 挂到错误上，供 `PersistRunUsageFromError` 读）。
所以通路本身已有人用；本计划补的是**另一端**——适配器从不在失败返回处包它，于是重试层 `UsageFromError`
拿到的最多是中间层事后挂的总量，**拿不到"每次死尝试各消耗了多少"**。适配器侧的调用点是空的，这正是 Step 2
要填的。

重试层（`pkg/llm/stream_retry.go`）今天对失败的尝试只做三件事：`withdraw(snap)` 撤回内容、
`ResetStreamProgress(ctx)`、按决策继续或返回。**它从不看 `UsageFromError(err)`**。

计数（`pkg/agent/trace.go:53-56`）：

```go
func (s *runStats) recordLLM(duration time.Duration, usage *llm.Usage) {
	s.llmCalls++
```

## 契约（本计划定死）

1. `llm.Result.Usage` 的含义变成：**这一次逻辑请求（含被重试掉的尝试）的用量**；
   `llm.Result.Attempts` = 这次逻辑请求发出的物理请求数（没有重试层时缺省视为 1）。
2. 失败的逻辑请求：错误上带**累计后**的用量（`UsageFromError` 拿到的是总和）与物理尝试数。
3. `RunSummary.LLMCalls` / `fb_runs.usage_llm_calls` = **物理尝试数**（字段名与列名不变，注释与文档改准确）。
4. **界面零变化**：不加字段、不加文案、不改标签、不动重试状态行。重试对读者仍然只是"等了一会儿"。

## Scope

**In scope（只改这些）**：
- `pkg/llm/errors.go`（`ExecuteError` 增加 attempts 字段 + `AttemptsFromError`）
- `pkg/llm/llm.go`（`Result.Attempts` + `Result.Usage` 的注释）
- `pkg/llm/stream_retry.go`、`pkg/llm/retry.go`（两个重试循环：累计死尝试用量与尝试数）
- `pkg/llm/anthropic/agent_llm.go`、`pkg/llm/google/agent_llm.go`、`pkg/llm/google/stream.go`、
  `pkg/llm/openai/compat_llm.go`、`pkg/llm/openai/responses_llm.go`（失败返回处用 `llm.NewExecuteError` 带上本尝试用量；
  `pkg/llm/opencode` 若也实现了流式失败返回，同样处理）
- `pkg/agent/trace.go`（`s.llmCalls += max(1, res.Attempts)` + 注释）
- `pkg/run/supervisor.go`（`PersistRunUsageFromError` 一并写 `LLMCalls`）
- `pkg/tui/reducer.go`（**只改注释**：`ObserveRunEndForRun` 那段解释为什么保留流式值的话，现在多了一个理由——
  流式 delta 与最终值已经是同一个口径，clamp 会自然落到 0）
- 测试：上述各包内新增（见 Test plan）

**Out of scope（看着相关也不要动）**：
- **任何界面/文案**：工作台卡的标签仍是"模型请求"、仍不出现"重试"字样；TUI 不新增任何行
- schema：**不加列、不加迁移**（`usage_llm_calls` 直接复用为物理尝试数）
- `pkg/state` 的读写（`SetRunUsage` 签名不变）
- `sink.OnUsage` / `OnUsageSnapshot` 的签名与语义（快照仍是"当前响应的绝对占用"）
- `plans/165` 的账本（那是逐次明细，本计划只改汇总口径）
- `plans/176` 的分母口径（本计划只保证"分子含全部尝试"；176 负责把时间分母也对齐）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 适配器（逐个） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/anthropic ./pkg/llm/google ./pkg/llm/openai -run 'FailedAttempt' -count=1 -v` | 各 `--- PASS` |
| 重试层 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'AttemptUsage\|AttemptsCount' -count=1 -v` | PASS |
| 计数/持久化 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/agent ./pkg/run -run 'Attempts\|Usage' -count=1 -v` | PASS |
| 四个包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/agent ./pkg/run ./pkg/state -count=1` | 与基线相比无新增 FAIL |
| 两 surface 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/... ./pkg/agent` | exit 0 |

## Steps

### Step 1：先钉住"失败尝试的用量必须能拿到"（逐适配器）

每个适配器加一个测试：让 httptest 服务端**先吐出带 usage 的流、然后断连**，断言返回的 error 满足
`llm.UsageFromError(err) != nil` 且数值等于服务端报的那一份。

结构照抄各包已有的"流被切断"用例（如 `pkg/llm/stream_retry_test.go` 的 `connDropped` 思路、
`pkg/llm/anthropic/agent_llm_test.go` 里已有的 httptest 脚手架）。

**Verify**：`… -run 'FailedAttempt'` 在三个包全红（今天 `UsageFromError` 恒为 nil）。

### Step 2：适配器在失败返回处带上本尝试用量

对每个"已经见过 usage、随后失败"的返回点，用 `llm.NewExecuteError(err, &llm.Usage{...})` 包一层
（**不是** `WithUsageError`：`NewExecuteError` 的文档明写"recording the usage of the attempt they just made
want this one"，而重试层要的正是"这次尝试的用量"）。

要点：

- anthropic：用 `latestUsage`（input 是**不含** cache 的 `InputTokens`，cache 在独立字段里——**原样分开填**，
  不要把 cache 折进 input，`fb_runs` 的四列是分开存的）。
- google：流式路径用 `streamState.usage`（`PromptTokenCount` / `CandidatesTokenCount+ThoughtsTokenCount`）；
  非流式路径用响应里的 `usageMetadata`。
- openai compat / responses：用各自已经解析出的 usage 结构（`prompt_tokens` / `completion_tokens` 与缓存字段）。
- **只在"确实已经收到 usage 之后失败"才包**；输出前就 503 的尝试没有 usage，保持原错误。

**Verify**：Step 1 的三个测试转绿。

### Step 3：两个重试循环累计"死尝试"的用量与次数

`pkg/llm/stream_retry.go` 的 `Execute`：

```go
	var deadUsage llm.Usage      // 本包内类型：llm.Usage
	attempts := 0
	for {
		attempts++
		…
		result, err := r.inner.Execute(attemptCtx, messages, tools)
		if err == nil {
			// The request's usage is what every attempt it took consumed:
			// a retried attempt still cost its input tokens.
			result.Usage = addUsage(result.Usage, &deadUsage)
			result.Attempts = attempts
			return result, nil
		}
		if u := llm.UsageFromError(err); u != nil {
			deadUsage = mergeUsage(deadUsage, *u)
		}
		…（既有的判定与返回路径全部不变）
	}
```

失败返回点（`DecideFail` / `KeepPartial` / 两个 `requestLevelDecision` 的直接 return）：

- 用 `llm.NewExecuteError(err, merged)` 把"死尝试总量 + 本次错误自带的用量"一起挂上；
- 尝试数挂到错误上：`llm.WithAttempts(err, attempts)`（Step 4 新增）。
- `DecideKeepPartial` 走 `finish(&PartialResponseError{...})` 时，同样把累计用量挂到**最终返回的错误**上
  （`PartialResponseError` 现在已经带 `Err`，包在外层即可）。

`pkg/llm/retry.go` 的非流式循环（`NewRetry`）同样处理——它是**非流式回退**走的那条路，
`plans/156` 之后每次请求最多降级一次，但降级那次同样要计数。

辅助函数放在 `pkg/llm`（同包，不导出）：

```go
func addUsage(into *Usage, extra *Usage) *Usage    // nil 安全，返回新值
func mergeUsage(into Usage, extra Usage) Usage
```

**Verify**：`… -run 'AttemptUsage|AttemptsCount'` → PASS（见 Test plan 的两条）。

### Step 4：计数透传

1. `pkg/llm/errors.go`：`ExecuteError` 增加 `attempts int`；
   `func WithAttempts(err error, n int) error`、`func AttemptsFromError(err error) int`（无则 0）。
2. `pkg/llm/llm.go` 的 `Result` 增加 `Attempts int`（0 = 未知），并把 `Usage` 的注释改成"这次逻辑请求的
   用量，含被重试掉的尝试"。
3. `pkg/agent/trace.go`：`s.llmCalls += max(1, attempts)`（`recordLLM` 增加一个 attempts 参数，
   或在调用点先算好）；把 `RunSummary.LLMCalls` 的注释改成"物理模型请求数（一次请求的每次重试各计一次）"。
4. `pkg/run/supervisor.go` 的 `PersistRunUsageFromError`：`LLMCalls: llm.AttemptsFromError(err)`。
5. `pkg/tui/reducer.go` 的 `ObserveRunEndForRun` **只改注释**：说明流式 delta 与最终值现在是同一口径
   （都含重试），所以 `deltaIn/deltaOut` 的 clamp 会自然为 0，保留流式值这条规则不再造成偏差。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/agent ./pkg/run ./pkg/tui -run 'Usage|Attempts' -count=1`
→ PASS；`grep -c "max(1, " pkg/agent/trace.go` 为 `1`。

### Step 5：回归（含两个 surface）

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/agent ./pkg/run ./pkg/state ./pkg/tui ./pkg/gateway -count=1`
→ 与基线相比无新增 FAIL。特别注意既有的重试用例（`TestStreamRetry*`、`TestNonStreamingFallback*` 若已存在）
与 `pkg/run` 的用量累加用例都必须仍然 PASS。

## Test plan

- 适配器三条（`FailedAttempt`）：anthropic / google / openai-compat 的"见过 usage 后断流"→
  `UsageFromError` 返回该尝试的用量，cache 字段各自分开。
- `pkg/llm`：`TestRetriedAttemptsUsageIsCounted`（两次尝试各报 100/10 与 120/8，成功的第二次返回 →
  `res.Usage == {Input:220, Output:18}`、`res.Attempts == 2`）；
  `TestExhaustedRetryCarriesEveryAttempt`（全部失败 → `UsageFromError` 是各次之和、`AttemptsFromError == N`）。
- `pkg/agent`：一次带 `Attempts: 3` 的 `Result` → `Summary.LLMCalls == 3`。
- `pkg/state`/`pkg/run`：`PersistRunUsageFromError` 写入的 `LLMCalls` 等于错误上的尝试数
  （沿用 `pkg/state` 已有的 `SetRunUsage` 断言方式）。
- **不新增任何界面测试**（本计划不改界面）。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet -tags fts5 ./pkg/llm/... ./pkg/agent` exit 0；`gofmt -l pkg` 无输出
- [ ] 上面四条新测试 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/agent ./pkg/run ./pkg/state ./pkg/tui ./pkg/gateway -count=1` 与基线相比无新增 FAIL
- [ ] `grep -rc "llm.NewExecuteError(" pkg/llm/anthropic pkg/llm/google pkg/llm/openai | grep -v ":0"` 至少 3 个文件命中
- [ ] `grep -c "Attempts int" pkg/llm/llm.go` 为 `1`；`grep -c "func AttemptsFromError" pkg/llm/errors.go` 为 `1`
- [ ] `grep -rn "含重试\|包含重试\|attempts)" frontend/src pkg/tui | grep -v _test` **无输出**（界面零变化）
- [ ] `git status --porcelain` 只显示 Scope 里的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. 某个适配器在失败路径上**根本拿不到** usage（例如 SDK 把 usage 藏在已经返回的 error 里而不是事件里）：
   报告该适配器，**不要**用 `sink` 的 delta 去凑（delta 的单位在各适配器间不一致：anthropic 的 delta input
   含 cache，google 的不含——这条差异会让 `fb_runs` 的四列互相污染）。
4. 你发现 `res.Usage` 的语义在别处被当成"最后一次尝试"使用（`grep -rn "\.Usage\b" pkg/run pkg/tui | grep -v _test`）：
   报告后停手，先确认那个调用点是否依赖旧语义。
5. 你想顺手把工作台卡的标签改成"请求（含重试）"或加一行"尝试次数"：**owner 明确要求重试对用户透明**，不要加。
6. 你想加 `usage_attempts` 列：**不要**（owner 选了复用 `usage_llm_calls`，且迁移版本要与 145–151 排队）。

## Maintenance notes

- 口径现在是：**分子（token）与"请求次数"都按物理尝试计**；重试对用户不可见，但对账目可见。
  谁将来加"成本"或"速率"的展示，分母必须同为"含重试的时间"，否则数字会互相矛盾（`176` 就是这么做的）。
- `Result.Usage` 的语义变了（含死尝试），任何依赖"最后一次尝试用量"的新代码都要显式说明。
- 失败尝试若在输出前就 503，provider 根本不报 usage：那一次对 token 是加 0，但**尝试数仍然 +1**——
  这正是"尽量贴账单"，不是"精确账单"。
