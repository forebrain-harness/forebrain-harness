# Plan 165（设计/探路）：一次运行的「尝试与用量账本」

> **执行者须知**：这是**设计/探路计划**。产出是"一份能跑的纵向原型（存储 + 一个 surface 显示）+ 一份写下来的
> 决定 + 开放问题"，**不是**全量上线。做完 Step 4 就交回评审。
> 触发 "STOP conditions" 时停下报告。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "type TransportRetry struct" pkg/llm/stream.go` 为 `1`；
> `grep -c 'RunEventTransportRetry = "transport_retry"' pkg/event/run_events.go` 为 `1`；
> `grep -c "func (s \*RunStore) SetRunUsage" pkg/state/runrt.go` 为 `1`；
> `grep -c "stateSchemaVersion = 17" pkg/state/schema_migrations.go` 为 `1`（若版本号已升到 18+，见 STOP 条件 2）。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/llm/stream.go pkg/event/run_events.go pkg/state pkg/run/llm_middleware.go pkg/run/subagent.go pkg/gateway/wsevents.go pkg/gateway/api_extra.go` 必须**无输出**。

## Status

- **Priority**: P2（方向项；成本与"这次回答试了几次"今天都不可查）
- **Effort**: M–L（探路原型：一次迁移 + 一个写入点 + 一个读取端）
- **Risk**: MED（碰 schema 与事件表；原型只加可以一次删掉的东西）
- **Depends on**: 概念上依赖 `plans/156` 定下的"降级只许一次"（否则账本里会看到 10 次盲等 attempt）
- **Blocks**: 无（但它会让 `plans/README.md` 里"重试重复计费"那条旧发现拥有可测的落点）
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: direction（可观测性 / 成本）

## Why this matters

今天有三种"用量"事实，彼此都不完整：

1. **一次运行的总量**：`fb_runs` 的 4 个 token 列 + `usage_llm_calls`（`pkg/state/schema.sql:255-265`），
   由 `SetRunUsage`（`pkg/state/runrt.go:508-538`）写入；`SessionUsageForSession`（`:452`）按 run 树求和。
   网页的"成本概览"读的就是它（`GET /api/chat/sessions/:id/cost-summary` → `api_extra.go:3310`）。
2. **逐次尝试**：`llm.TransportRetry`（`pkg/llm/stream.go:78-100`）带着 `Retry/Total/Wait/Err/Status/
   Cleared/DropFinishedThinking`，**只走推送**：`RunEventTransportRetry = "transport_retry"`
   （`pkg/event/run_events.go:79-86`）明确写着 *"Push-only by design — the retried answer lands in the
   transcript whole"*，并且确实在 `IsPushOnlyStreamEvent`（`:99-105`）里，所以网关只推不存
   （`pkg/gateway/wsevents.go:629`）。**一次运行到底试了几次、每次的 HTTP 状态与等待，会话结束后就消失了。**
3. **逐次调用的 token**：只有子代理会发 `RunEventUsageDelta`（`pkg/event/run_events.go:19` +
   `pkg/run/subagent.go:1000`），主 run 的 token 只有"总量"——TUI 侧甚至把 total 从流式 delta 里累加
   （`pkg/tui/reducer.go` 的 `ObserveRunEndForRun`（`:7804`）与 `ObserveUsageDelta`（`:7857`）），页脚早已不画 token
   （`pkg/tui/render.go:3489-3506`）。

于是"这次回答花了多少、为什么这么慢"这类问题**没有可信答案**：总量里混着被重试丢弃的尝试，
而尝试本身没有记录。这不是为了做仪表盘——它直接决定了两个已知问题的可测性：

- "重试把输入 token 重复计入"（本次审计确认过的缺陷，`plans/README.md` 里登记为「暂缓」）：
  没有逐尝试记录，就没有办法说清"正确值应该是多少"；
- 弱网下用户看到的"重试 3/10"结束后无处可查，支持与复盘只能靠截图。

**要做成**：一次运行有一个**账本**——每次模型请求一行（第几次、流式/非流式、结果状态、等待、耗时、
该次用量），运行结束时仍可读。落到哪张表、是否复用现有事件表，是本计划要**定**的事（见 Step 1）。

## Current state（探路要用的全部事实）

**尝试级事件（已有形状，缺持久化）** — `pkg/llm/stream.go:78-100`：

```go
type TransportRetry struct {
	Retry int        // 1-based sequence number (the numerator of n/total)
	Total int        // the retry cap from configuration (default 10)
	Wait  time.Duration  // the back-off before the next attempt (0 when Cleared)
	Err   error      // the failure that just happened, rendered on the error card
	Status int       // the HTTP status, 0 for transport errors
	Cleared bool     // the retry is over (output started / loop returned)
	DropFinishedThinking bool
}
```

写入点：`pkg/llm/stream_retry.go`（流式：`:244-249` 携尝试、`:65`/`:102`/`:112` 的 Cleared）
与 `pkg/llm/retry.go`（非流式：`:243-248`、`:282`）；投递通道 `StreamSink.OnTransportRetry`，
`pkg/run/llm_middleware.go:364-371` 把它转给 sink，`pkg/run/subagent.go:1049-1064` 另发一条
`event.RunEventTransportRetry`（payload：`pkg/event/run_events.go:617-628`）。

**持久化基础**（三条路，各有代价——Step 1 要选一条）：

| 方案 | 落点 | 好处 | 代价 |
|---|---|---|---|
| A. 存成 run 事件 | `fb_session_events`（`schema.sql:308-320`；写入 `pkg/state/session_events.go:89` 的 `AppendSessionEventOnce`） | 零迁移；现有 `/events` 与回放立刻可读；天然带 `sequence` 游标 | 要把 `transport_retry` 从 `IsPushOnlyStreamEvent` 里移出（`pkg/event/run_events.go:99-105`），并改网关 `wsevents.go:629`；事件表按 run 查询有索引（`idx_fb_session_events_run_type`） |
| B. 新表 `fb_llm_attempts` | 迁移 v17→v18（`schema_migrations.go` 的注册表 + `stateSchemaVersion`） | 形状最贴（每次尝试一行，可加列）；不污染事件流 | 一次 schema 迁移；v0→v1 重建要登记表（`stateV1Copies`，`schema_migrations.go:1795` 起）；多一处读写面 |
| C. 只扩 `fb_runs` 计数列 | `usage_llm_calls` 旁边加 `retry_count` 等 | 最便宜 | 只有汇总，**没有逐次事实**，解决不了"为什么"——本计划明确不选 |

**总量与消费端**：`SetRunUsage`（`runrt.go:508`）写 5 个列；`SessionUsageForSession`（`:452`）求和；
`GET /api/chat/sessions/:id/cost-summary`（`api_extra.go:190` 路由、`:3310` 处理）→
`frontend/src/components/workbench/OverviewTab.vue:299`。TUI 的"Worked for"行读的是**流式累加值**
（`pkg/tui/reducer.go:7804` 起的 `ObserveRunEndForRun`，注释明确"per-run counters are NOT modified here"）。

**已存在的同类模式**（新事件类型该怎么写）：`RunEventToolStarted = "tool_call_started"`、
`RunEventTurnCompleted`、`RunEventImageRejected`（后者是"请求被降级重发"的可读记录，非 push-only）。

## Scope

**In scope（探路原型，允许一次删掉）**：
- `pkg/state/`（若选方案 B：一次迁移 + 一个 Insert/List；若选 A：只改事件类型集合与网关的存/推判定）
- `pkg/run/`（把一次尝试的事实交给上面那条落点：`llm_middleware.go` 或 `subagent.go` 的写入点）
- 一个读取端（**二选一**：`GET /api/chat/sessions/:id/cost-summary` 里加 `attempts` 数组，或 TUI 的
  `/usage` 类命令；选能在一小时内验证的那个）
- 计划文件末尾的「原型结论 + 开放问题」

**Out of scope（本期明确不做）**：
- 改 `fb_runs` 的 token 语义（见开放问题 1；那会动"成本"这个对外数字）
- 改 `TransportRetry` 的结构或推送语义（用户看到的重试行不变）
- 计费/价格表（`assets/models.json` 里没有价格，本期不引入）
- 历史数据的回填（老运行没有尝试记录，就是没有）
- 网页/TUI 的图表、导出、告警
- 保留策略之外的清理任务（原型阶段不实现清理）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| state 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` | 与基线相比无新增 FAIL |
| 迁移 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestSchema' -count=1 -v` | PASS |
| run 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | 与基线相比无新增 FAIL |
| 网关 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/state ./pkg/run` | exit 0 |

## Steps

### Step 1：定"账本的形状"（先写下来，再写代码）

在计划文件末尾写一张表并勾选：

1. **落点**：A（存事件）/ B（新表）——各自把"未来三个月最可能加的三个字段"写出来，再判断形状会不会撑破。
   推荐评估顺序：先 A（零迁移、已有回放），只有当 A 表达不了"同一个 attempt 会更新两次（开始/结束）"时才选 B。
2. **一行账本的最小字段**：至少 `attempt`（第几次）、`streaming`（bool）、`status`（HTTP/传输分类）、
   `wait_ms`、`elapsed_ms`、`input_tokens`、`output_tokens`、`outcome`（success/failed/kept-partial/cancelled）。
   字段名与 `TransportRetry` 对齐，别另造词汇。
3. **一条 attempt 的边界**：一次"逻辑请求"包含 N 次物理尝试；账本记**物理尝试**（这样才能解释 50 分钟盲等）。
4. **谁写**：`pkg/run`（它知道 run id 与 session id）而不是 `pkg/llm`（它不知道）。

**Verify**：无命令；Step 1 的产出是计划文件里的一段文字。写不出"最小字段"就说明还没想清，STOP。

### Step 2：把尝试事实送到落点（纵向一条，不做全量）

只做**最小纵向切片**：一次**失败并重试**的请求写一行，成功的那次也写一行。

- 若选 A：把 `RunEventTransportRetry` 从 `IsPushOnlyStreamEvent` 移出，并在 `pkg/gateway/wsevents.go:629`
  的分支里改为"存 + 推"；payload 用现成的 `event.TransportRetryPayload`（`run_events.go:617-628`），
  只补 `elapsed_ms` 与 `outcome` 两个字段。
- 若选 B：新增 `fb_llm_attempts(run_id, attempt, streaming, status, wait_ms, elapsed_ms, input_tokens,
  output_tokens, outcome, created_at)` + 迁移 v17→v18 + `InsertLLMAttempt`/`ListLLMAttempts(runID)`；
  在 `pkg/run/llm_middleware.go` 的 notify 钩子里写（那里已经有 `ctx` 里的 run id）。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/run ./pkg/gateway -count=1`
→ 与基线相比无新增 FAIL；并新增**一个**用例（放在改动所在包）断言"一次重试的请求留下两行 attempt"。

### Step 3：一个读取端

只做一个，二选一（按"能在一小时内验证"挑）：

- **网关**：`GET /api/chat/sessions/:id/cost-summary` 的响应里加 `attempts: [...]`（最近一次 run 的尝试）；
- **TUI**：在既有的用量视图里多一行（不要新增斜杠命令，除非既有视图确实无处可放）。

**Verify**：读出真实数据——用假 provider 跑一次带 503 重试的回合，`curl` 或 TUI 里看到两次 attempt
（一次 503、一次成功），数值与 `fb_runs` 的 `usage_llm_calls` 不矛盾。

### Step 4：写「原型结论 + 开放问题」，停下

至少回答下节四条，并把"下一步该由谁定"标出来。

## Open questions（必须由 owner 定，不要自行决定）

1. **总量要不要变成"账本求和"？** 今天 `fb_runs` 的 token 是流式累加（`pkg/run/orchestration_llm.go`
   的 `accumulateUsage`），重试会把同一次输入算多遍。改成"只算成功那次"会**降低**用户看到的成本数字——
   属于对外口径变化，必须 owner 拍板，且要有一次版本说明。
2. **保留多久？** 账本会长在 `fb_session_events`（或新表）里。按"每个 run 平均 1.2 行 × 每天 N 个 run"
   估算一年体量，先给数字再决定是否随会话删除（`fb_runs` 已有级联）。
3. **非流式回退要不要单列？** `plans/156` 之后降级只发生一次；账本里它是一次 `streaming=false` 的 attempt，
   还是要单独标 `fallback=true`（便于统计"有多少请求被降级"）。
4. **子代理的 attempt 要不要进同一账本？** 子代理已有 `usage_delta` 事件（`pkg/run/subagent.go:1000`），
   但那是"每个子代理的总量"，不是"每次尝试"。合流会让一个会话的账本变长但更完整。

## Test plan

- 新增 1 个用例（落点所在包）：一次重试的请求 → 账本里有 2 行，第 1 行 `status=503`/失败，
  第 2 行成功，且 `attempt` 递增。
- 既有 state/run/gateway 用例全部保持绿：`fb_session_events` 的 append 语义有唯一约束
  （`UNIQUE(session_id, event_id)`），新增事件必须带稳定 `event_id`，否则重放会重复。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] 落点所在包的既有用例与基线相比无新增 FAIL
- [ ] 新增 1 个账本用例 PASS
- [ ] 一个真实（假 provider）回合能在读取端看到 ≥2 条 attempt
- [ ] 若动了 schema：`stateSchemaVersion` 已 +1，且迁移在临时 home 上能从旧版本升上来
      （`FOREBRAIN_HOME="$(mktemp -d)" … -run 'TestSchema'`）
- [ ] 计划文件末尾有「原型结论 + 开放问题」四条
- [ ] `git status --porcelain` 只显示 Scope 里列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且被引用符号的语义变了。
2. `stateSchemaVersion` 已经不是 17（有人先做了迁移）：本计划的迁移步骤要重新编号，先报告。
3. 你发现 `RunEventTransportRetry` 的 payload 里缺"每次尝试的用量"且**拿不到**（`pkg/llm` 层不返回失败尝试的用量）：
   那是开放问题 1 的核心，报告后停手，不要为了"凑出数字"去估算 token。
4. 选 A 之后回放/去重（`UNIQUE(session_id, event_id)`）导致同一次尝试写两行：说明 `event_id` 设计不对，
   报告后再定。
5. 你想顺手改 `fb_runs` 的 token 语义或 TUI 的"Worked for"数字：那是对外口径，本期不做。

## Maintenance notes

- 账本的第一价值是**可解释性**，不是精度：它要能回答"这次为什么等了 40 秒""这次为什么又发了一遍"。
  任何让"一行 attempt 不再对应一次物理请求"的设计（例如把重试合并成一行）都是背离目的。
- 与 `plans/156` 的关系：156 把最坏盲等从 N×5 分钟降到 5 分钟；账本让这个降级**看得见**。
- 评审重点：原型阶段允许"只做一条纵向切片"，但要保证删掉时不留孤儿（若加了表，删除时连迁移一起撤，
  不要留下空表）。

## 原型记录（2026-10-26 执行批，exec/156-165）

### Step 1 落定的形状

**落点：B（新表 `fb_llm_attempts`）**。按计划的评估顺序先验了 A，A 被否的证据：

- A 的行是「通知」不是「尝试」：thinking-only / stalled / 非流式回退前的截断这些物理尝试**故意不发
  非 Cleared 通知**（走 `withdraw()`），成功的尝试则完全没有通知——选 A 会系统性漏行，违背
  "一行 attempt = 一次物理请求"的第一原则。
- A 补不出 `elapsed_ms`：发布点闭包 ctx 拿不到 `StreamProgress`，而 `TransportRetry` 结构不许改。
- A 会踩 STOP 4 预警：一次尝试产生 non-Cleared + Cleared 两条通知，与 `UNIQUE(session_id, event_id)`
  的「一行事实」模型相抵触。
- B 的新表**不需要** `stateV1Copies` 条目（`schema_migrations.go:1791-1794` 明文：final schema 声明
  而无 entry 命名的表从空开始——旧库本来就没有尝试数据）。

**一行账本字段**（命名与 `TransportRetry` 对齐）：`run_id`（FK 级联）、`call`（逻辑请求序号）、
`attempt`（1 起物理序号）、`streaming`（**本原型恒 NULL**，见开放问题 3）、`status`（HTTP 码，0=传输错）、
`error`、`wait_ms`、`elapsed_ms`（`StreamProgress` 快照）、`input_tokens`/`output_tokens`（**失败行恒
NULL=未上报，绝不估算**）、`outcome`（success/failed/kept-partial/cancelled）、`created_at_ms`。
边界：逻辑请求 = 一次穿过 networkRetry 包装的 Execute；账本记物理尝试。谁写：`pkg/run`（notify 钩子处
ctx 同时有 run id、session id、StreamProgress——计划里"那里已经有 ctx 里的 run id"经查证成立）。
未来三个月最可能加的字段：`chain`（main/summary）、cache token 两列、`model`/`provider`——全窄列，撑不破。

### Step 2/3 实现要点

写入点两处均在 `pkg/run`：notify 钩子 `noteFailure`（只在 `!tr.Cleared && tr.Retry>0` 时写、在 sink
转发**之前**——静音流抑制卡片不抑制记录）；`networkRetryLLM.Execute` 返回处 `settle`（终局行，
`context.WithoutCancel` 保证取消竞态下仍落最后一行）。per-call 状态经 ctx value 传递。写入 best-effort
（失败仅 WARN，含 FK 拒绝）。一个 ledger 服务主链 + compaction summary 链；子代理经
`Factory.NewIsolatedRunner` 继承 `RunRT`，其请求同样记账（归因到子代理自己的 run 行）。读取端 =
cost-summary 加 `attempts`（`LatestPrimaryRunIDForSession`，不限状态；空时 `[]`）。

**原型已知盲区**（诚实记录）：①静默重试（thinking-only/stalled/非流式回退前截断）无通知→无行；②blank-output
重试与 image-heal 重发发生在 networkRetry **内层**，完全不可见——「重试重复计费」的可测性只是部分落地
（transport 级可测，逻辑层重发仍不可测）；③无 sink 路径 elapsed 记 0（真实 TUI/网关路径总有 sink）。

### Step 4 原型结论

1. **纵向切片成立**：一次「503→重试→成功」留两行（`attempt=1, status=503, outcome=failed, wait_ms>0,
   tokens=NULL` 与 `attempt=2, outcome=success, tokens=80/12`），运行结束后经存储 API 与 cost-summary
   都可读，与 `fb_runs` 不矛盾（`usage_llm_calls=1` 逻辑调用 vs 2 次物理尝试；**transport 级重试今天
   并没有重复计费**——transport 失败不产生 usage；重复计费风险在内层逻辑重发，即盲区②）。
2. **pkg/llm 不需要改**是本原型最重要的结论：run id、elapsed、终局 usage 在 `pkg/run` 边界全都拿得到。
   剩余盲区需要 pkg/llm 配合时再单独立计划。
3. 验证方式：未起 live gateway curl；替代 = 真实 supervised 回落盘（真链/真库）+ 网关 handler 对真库读回。

### 开放问题（owner 裁决）

> **裁决（owner，2026-10-27，四条全部落定）**：
> 1. **总量口径**：选「立项补盲区」——总量维持 175 的按物理尝试累计不变；blank-output / image-heal
>    内层重发的重复计费单独立计划覆盖（pkg/llm 在重发点发一次轻量通知或回调，账本记一行）。
> 2. **账本保留**：选「随 run 级联」（现状）——行随 fb_runs 级联删除，不加跨会话留存、不加清理任务。
> 3. **非流式回退单列**：选「动 pkg/llm 带上位」——给 TransportRetry 带上 streaming 位（或等价新回调），
>    pkg/run 落进账本填 `streaming` 列，使「降级率 / fallback 单列」可统计。**注意**：届时 TransportRetry
>    的结构变更与四个适配器核对要在计划里一并处理。
> 4. **子代理并入读取端**：选「并入子代理行」——cost-summary 的 attempts 查询按 run 树取（主 run + 其全部
>    子代理 run），每行带 run_id 区分归属；存储侧无需改动（递归 CTE 已有先例）。
>
> 后续工作：①与③同属 pkg/llm→账本的管道补全，④是读取端一条查询——待立后续计划（181+）。

1. **总量要不要变成"账本求和"？**（owner）实测新事实：transport 失败的尝试**不产生 usage**，所以
   「重试重复计入输入 token」在 transport 级**不成立**；真正重复计费的是 blank-output/image-heal 这类
   「HTTP 成功后被丢弃重发」的内层重发——恰是本账本盲区②。若要覆盖它需 pkg/llm 配合（新通知/回调），
   独立计划。`fb_runs` 只算成功那次的口径变化会**降低**用户看到的数字，须 owner 拍板 + 版本说明。
2. **保留多久？**（owner，附数字）行 ≈150B；每天 100 run × 平均 3 逻辑请求 × 5% 重试率 ≈ 115k 行/年
   ≈ **17MB/年**；重度（500 run/天、5 请求）≈ 400MB/年。行随 `fb_runs` 级联删除，无需独立清理任务。
3. **非流式回退要不要单列？**（owner）`streaming` 列已建但恒 NULL——pkg/run 层观测不到某次物理尝试是否
   非流式（`WithNonStreamingRequest` 只挂在 attempt 自己的 ctx 上；`TransportRetry` 不携带该位且不许改）。
   要标 `fallback=true` 必须动 pkg/llm。plan 156 已保证一次逻辑请求最多一次降级。
4. **子代理 attempt 要不要进同一账本？**（owner）**已经进了**（继承 RunRT，归因子代理自己的 run 行，
   随父 run 级联）；cost-summary 现只读最新主 run——要不要并入子代理行是读取端口径问题。

执行者对 STOP 3 边界的处理（评审已裁决接受）：失败尝试的用量在 pkg/llm 层结构性拿不到属实；账本对失败行
记 NULL（未上报）而非估算，是 STOP 3 要防的"凑数字"的反面；计划 Step 2 本身就要求失败行与成功行各一行。

删除原型不留孤儿的路径：撤 10 个文件即可；schema v18 纯增量（`CREATE TABLE IF NOT EXISTS`）；已升 v18 的库
被撤除后的二进制会报 `ErrStateSchemaNewer`，连库回退需 `DROP TABLE fb_llm_attempts; PRAGMA user_version=17`。
