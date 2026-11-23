# Plan 182：cost-summary 的 attempts 并入子代理 run 树

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **基线说明**：写在 `exec/156-165` 分支 tip（`d9d0b4d`，owner ff-merge 后即 dev 祖先）；依赖该分支的
> plan 165（`fb_llm_attempts` + cost-summary 的 `attempts` 字段）。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func (s \*RunStore) ListLLMAttempts" pkg/state/llm_attempts.go` 为 `1`；
> `grep -c "func (s \*RunStore) LatestPrimaryRunIDForSession" pkg/state/llm_attempts.go` 为 `1`；
> `grep -c "LatestPrimaryRunIDForSession(r.Context(), sid)" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "ListLLMAttempts(r.Context(), runID, 200)" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "func (s \*RunStore) SessionOutputRate" pkg/state/runrt.go` 为 `1`（递归 CTE 先例）；
> `grep -c "ORDER BY created_at ASC, rowid ASC" pkg/state/runrt.go` 为 `1`（`FirstPrimaryRunID` 的 rowid
> 平局先例，Step 2 要照它的形状）。
>
> **Drift check（先跑，两步）**：
> ① 基线可达：`git merge-base --is-ancestor d9d0b4d HEAD && echo OK`——不可达（exec/156-165 尚未被 owner
> 合并进当前分支）即 **STOP** 报告"等待 owner 合并 exec/156-165"，**不要在旧 dev 上执行**：旧 dev 上
> `pkg/state/llm_attempts.go` 根本不存在，锚点计数必失败。
> ② `git diff --stat d9d0b4d -- pkg/state/llm_attempts.go pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/gateway -count=1`（超时给足 900 s）。

## Status

- **Priority**: P2（owner 2026-10-27 对 plan 165 开放问题 4 的裁决：读取端并入子代理行）
- **Effort**: S（一条 run 树查询 + 处理器换调用 + 同秒平局确定化 + 测试）
- **Risk**: LOW（只改读取端；存储与写入零改动；前端今天不消费 attempts，无级联）
- **Depends on**: plan 165 已落地（在基线分支内）；与 181 正交（先做后做都行）
- **Blocks**: 无
- **Planned at**: `d9d0b4d`（exec/156-165 tip），2026-10-27
- **Category**: observability（读取端契约）

## Why this matters

子代理的请求**已经在记账**（plan 165：子代理 Runner 继承 `RunRT`，行落在子代理自己的 run id 下、
随父 run 级联删除）——存储侧没有缺口。但读取端只读**最新主 run 一行**的账：

```go
	// pkg/gateway/api_extra.go 的 handleSessionCostSummary（plan 165/176 之后的现场）：
	if runID, aerr := s.RunRT.LatestPrimaryRunIDForSession(r.Context(), sid); aerr != nil {
		...
	} else if runID != "" {
		rows, aerr := s.RunRT.ListLLMAttempts(r.Context(), runID, 200)
```

一个"主回答 + 3 个子代理"的回合，子代理的每次模型请求在 `attempts` 里**一行都看不到**——
"这次回答试了几次"对带子代理的回合（恰恰是最多尝试的回合）答不上来。owner 裁决：按 **run 树**取
（主 run + 其全部子代理 run），行带 `run_id` 区分归属。同一读取端还有第二个缺陷一并修死（owner
2026-10-27 追加裁决）：**"最新主 run"的选取在 `updated_at` 同秒平局时不稳定**——`updated_at` 是秒精度
（`CreateRun`/`CreateSubagentRun` 都写 `now.Unix()`），上一回合在同一秒内结束、下一回合又创建时两行
`updated_at` 相同，`ORDER BY updated_at DESC LIMIT 1` 选哪行由存储引擎决定（SQLite 实测选到**旧**的那
行），cost-summary 就读了错的一棵树。

## Current state（摘自 `d9d0b4d` 现场）

- `pkg/state/llm_attempts.go`：`ListLLMAttempts(ctx, runID, limit)`（单 run，`ORDER BY id ASC`，
  limit 缺省 500 / 上限 5000）；`LatestPrimaryRunIDForSession(ctx, sessionID)`（`fb_runs` 里
  `parent_run_id IS NULL` 按 `updated_at DESC` 取最新一个，无行返回 `("", nil)`）——该 `ORDER BY`
  **没有平局破解**，是同秒不稳定的来源（见 Why this matters）。平局破解的仓库先例：同表同族
  "选一个"查询 `FirstPrimaryRunID`（`runrt.go:1175`）用 `ORDER BY created_at ASC, rowid ASC LIMIT 1`；
  `fb_runs` 是普通 rowid 表（STRICT 不隐含 WITHOUT ROWID，`id` 是 uuid 文本不是整数主键），隐式
  rowid 即插入序、且唯一——`ORDER BY updated_at DESC, rowid DESC` 平局时后插入者胜，永不再平局。
  （列表查询的 `updated_at DESC, created_at DESC` 惯例（`runrt.go:1384/:1431`）不够：`created_at`
  也是秒，极端下仍平局。）
- `pkg/gateway/api_extra.go` 的 `handleSessionCostSummary`：上面摘录的块，响应键 `"attempts": attempts`
  （空时 `[]`）。
- 递归 CTE 先例（**照抄这个形状**）：`pkg/state/runrt.go` 的 `SessionOutputRate`——

```sql
WITH RECURSIVE run_tree(id) AS (
  SELECT id FROM fb_runs WHERE session_id=?
  UNION
  SELECT r.id FROM fb_runs r JOIN run_tree t ON r.parent_run_id=t.id
)
```

- 前端：`grep -rn "attempts" frontend/src/lib/api.ts frontend/src/components/workbench/` 为空——
  DTO 键尚无前端消费者，改读取端**没有界面级联**。
- 子代理 run 的父子关系：子代理请求归因到"子代理自己的 run 行"（supervisor 路径），其
  `parent_run_id` 指向主 run（plan 165 的实现事实；`SessionUsageForSession` 的 run 树求和同源）。

## 契约

`GET /api/chat/sessions/:id/cost-summary` 的 `attempts` = **最新主 run 的 run 树**（该 run 本身 + 其
全部后代 run）的账本行，按 `id ASC`（插入序 = 发生序，主/子代理行天然按时间交错）、上限 200；每行带
`run_id`，读方以此区分归属。**"最新主 run"的选取必须确定**：`updated_at` 同秒平局按插入序破
（`ORDER BY updated_at DESC, rowid DESC`）——后创建者胜，任何读者任何时刻得到同一答案。
空会话/无行仍返回 `[]`。`ListLLMAttempts`（单 run）保留——165 的直接调用方
（`TestLLMAttemptLedgerFillsFromASupervisedRun` 等）还在用；`LatestPrimaryRunIDForSession` 也保留，
其 `ORDER BY` 补同款平局破解（Step 2）。

## Scope

**In scope（只改这些）**：
- `pkg/state/llm_attempts.go`（新增 `LatestPrimaryRunTreeAttempts`；`ListLLMAttempts`/`LatestPrimaryRunIDForSession` 原样保留）
- `pkg/state/db_test.go` 或 `llm_attempts` 侧既有测试文件（新增 1 个测试）
- `pkg/gateway/api_extra.go`（换一处调用）
- `pkg/gateway/api_extra_test.go`（扩展既有用例或新增 1 条）

**Out of scope（看着相关也不要动）**：
- 写入路径、schema、行形状（`LLMAttempt` 结构一个字节不动）
- 前端（今天无消费者；将来画列表时再动 `api.ts` 类型）
- `SessionOutputRate` / cost-summary 的其它键（176 的速率口径）
- `LatestPrimaryRunIDForSession` 的选取**语义**（最新、不限状态）保持——但其 `ORDER BY` 补平局破解
  （Step 2，owner 追加裁决），这是本计划的在范围内的修复，不是语义变化
- 分页/游标（200 上限保持；溢出场景属将来的读取端演进）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| state 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'RunTreeAttempts' -count=1 -v` | PASS |
| gateway 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'CostSummary' -count=1 -v` | PASS（含子代理断言） |
| 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译/vet/gofmt | `CGO_ENABLED=1 go build -tags fts5 ./...`；`go vet -tags fts5 ./pkg/state ./pkg/gateway`；`gofmt -l pkg/state pkg/gateway` | exit 0 / 无输出 |

## Steps

### Step 1：state——run 树读取（先写会红的测试）

`pkg/state` 新增测试（`OpenStateForTest` + `InsertLLMAttempt` 组合；**造 run 不走 `CreateRun`**——它有
同会话存活主 run 独占检查，`runrt.go:250-273`，连造两个 running 主 run 会吃 `ErrSessionRunning`；也不存在
叫 `InsertRun` 的 helper。直接写
`INSERT INTO fb_runs(id, session_id, input_text, status, created_at, updated_at)`（照 `pkg/gateway/api_extra_test.go:238`
`seedRun` 的直插先例，但 **updated_at 必须显式拉开**：r1=100、r2=200——`updated_at` 是秒精度
（`CreateRun`/`CreateSubagentRun` 都写 `now.Unix()`，`runrt.go:237/:352`），`seedRun` 写死 0；两个主 run
同值时 `ORDER BY updated_at DESC LIMIT 1` 选哪行不确定，SQLite 实测选到**旧**的那行，树就选错了）。
子代理 run 用 `CreateSubagentRun(ctx, "r2", sid, "sub")`（`runrt.go:336`，写 `parent_run_id`）或同样直插：

```go
// The attempts of a turn include its subagents': the latest primary run's
// tree, every row carrying its own run id so a reader can tell whose request
// it was.
func TestLatestPrimaryRunTreeAttemptsIncludesSubagentRows(t *testing.T) {
	// 会话里两个主 run：r1（旧）与 r2（新，updated_at 更晚）；
	// r2 下挂一个子代理 run c1（parent_run_id=r2）。
	// r1 插一行 attempt；r2 插两行；c1 插一行（时间上夹在 r2 的两行之间）。
	// 断言：返回恰 3 行（r1 的行不在），按插入序 = [r2 第一行, c1 行, r2 第二行]，
	// 且每行 run_id 与造数一致。
	// 再断言（同秒平局场景，独立第二组造数）：主 run rA（先插）与 rB（后插）
	// updated_at 相同（如都 = 300）→ 后插入者胜：LatestPrimaryRunIDForSession 与
	// LatestPrimaryRunTreeAttempts 都取 rB（给 rB 挂一行 attempt、rA 挂一行，
	// 断言返回的是 rB 的行）。平局行为从此被测试钉死，不再取决于存储引擎。
	// 再断言：空会话返回 0 行不报错——state 层照 ListLLMAttempts 的惯例返回 nil；
	// handler 的 len>0 守卫（api_extra.go:3449-3451）保住 JSON 的 [] 契约，
	// 不要在 state 层强造非 nil 空 slice。
}
```

先跑 → FAIL（方法不存在，编译红）。

### Step 2：实现 `LatestPrimaryRunTreeAttempts`

`pkg/state/llm_attempts.go` 在 `LatestPrimaryRunIDForSession` 之后新增（**一条 SQL**，最新主 run 的选取
与 run 树递归都放里面，避免两段查询间的竞态）：

```go
// LatestPrimaryRunTreeAttempts returns the attempt rows of the session's
// latest primary run and every run below it — its subagents' runs included,
// each row carrying its own run id. The owner's ruling on plan 165's fourth
// open question: "how many times did this answer try" must count the agents
// the answer dispatched. Oldest first, insertion order preserved; limit caps
// the rows (0 means the 500 default ListLLMAttempts applies).
func (s *RunStore) LatestPrimaryRunTreeAttempts(ctx context.Context, sessionID string, limit int) ([]LLMAttempt, error)
```

SQL 骨架（行扫描照抄 `ListLLMAttempts` 的循环，两处保持同一形状）：

```sql
WITH RECURSIVE latest AS (
  SELECT id FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL
  ORDER BY updated_at DESC, rowid DESC LIMIT 1
), run_tree(id) AS (
  SELECT id FROM latest
  UNION
  SELECT r.id FROM fb_runs r JOIN run_tree t ON r.parent_run_id=t.id
)
SELECT id, run_id, call, attempt, streaming, status, error, wait_ms, elapsed_ms,
       input_tokens, output_tokens, outcome, created_at_ms
FROM fb_llm_attempts WHERE run_id IN (SELECT id FROM run_tree)
ORDER BY id ASC LIMIT ?
```

（`LatestPrimaryRunIDForSession` 保留——165 的测试还在用（`pkg/run/attempt_ledger_test.go:173`）；
实现可以内部复用它，也可以如上内联，**选内联**，一条语句一个快照。）limit 归一化照抄
`ListLLMAttempts`（<=0 → 500，>5000 → 5000）。

**同一步里把 `LatestPrimaryRunIDForSession` 的 `ORDER BY updated_at DESC` 补成
`ORDER BY updated_at DESC, rowid DESC`**（同款平局破解），并在其 doc comment 补一句：同秒平局按
插入序破，后创建者胜。为什么 rowid 是对的破法：`fb_runs.id` 是 uuid 文本（无序），`created_at` 也是
秒（`updated_at DESC, created_at DESC` 在两个秒列都撞时仍平局），隐式 rowid 是唯一且单调的插入序；
同表先例 `FirstPrimaryRunID`（`runrt.go:1175`）正是 `created_at ASC, rowid ASC`。VACUUM 可能重编号
rowid，但按 rowid 顺序顺次重编、**相对次序不变**，而 tiebreak 只用相对次序；本仓的 VACUUM 只出现在
备份（`VACUUM INTO` 写副本）与迁移收尾，原库不被动重排。

**Verify**：Step 1 的测试转绿；`gofmt -l pkg/state` 无输出。

### Step 3：网关换调用

`handleSessionCostSummary` 的 attempts 块：`LatestPrimaryRunIDForSession` + `ListLLMAttempts` 两步换成
一次 `LatestPrimaryRunTreeAttempts(r.Context(), sid, 200)`（错误分支同今天：500）。块上方的注释补一句
"the tree includes the subagents' runs; each row's run_id names whose request it was"。
`"attempts": attempts` 与空 `[]` 契约不变。

**Verify**：扩展 `pkg/gateway/api_extra_test.go` 的 `TestSessionCostSummaryIncludesTheLatestRunAttempts`（或旁加一条）：
造主 run + 子代理 run 各带 attempt 行 → 响应 `attempts` 含两者且 `run_id` 各归其主；只有主 run 的旧断言
继续成立。造数注意：`seedRun`（`:238`）把 updated_at 写死 0——**再造第二个主 run 必须显式拉开 updated_at**
（给 `seedRun` 加参数并同步改既有调用点，或新写带时间戳的 helper），否则平局选错树。子代理行可直插
（带 `parent_run_id`，照 `seedRun` 的直插形状扩一列）。
`grep -c "LatestPrimaryRunIDForSession(r.Context(), sid)" pkg/gateway/api_extra.go` 变为 `0`。

### Step 4：回归

**Verify**：两包全量与基线相比无新增 FAIL（特别：176 的 `TestCostSummaryCarriesOutputRate`、165 的
`TestSessionCostSummaryIncludesTheLatestRunAttempts`、`TestLLMAttemptLedger*`）；build/vet/gofmt 全过。

## Test plan

- `pkg/state`：`TestLatestPrimaryRunTreeAttemptsIncludesSubagentRows`（树包含/排除、插入序交错、run_id
  归属、**同秒平局后插入者胜**、空会话空 slice）。
- `pkg/gateway`：子代理行出现在 `attempts` 且带自己的 `run_id`；既有主-run 用例不回退。
- 不新增前端测试（无消费者）。

## Done criteria

机器可检查，全部满足：

- [ ] `grep -c "func (s \*RunStore) LatestPrimaryRunTreeAttempts" pkg/state/llm_attempts.go` 为 `1`
- [ ] `grep -c "LatestPrimaryRunTreeAttempts(r.Context(), sid, 200)" pkg/gateway/api_extra.go` 为 `1`
- [ ] `grep -c "ListLLMAttempts(r.Context(), runID, 200)" pkg/gateway/api_extra.go` 为 `0`；`ListLLMAttempts` 与 `LatestPrimaryRunIDForSession` 的定义仍在（各 `1`）
- [ ] `grep -c "ORDER BY updated_at DESC, rowid DESC" pkg/state/llm_attempts.go` 为 `2`（`LatestPrimaryRunIDForSession` 与新 CTE 各一处）；Step 1 的同秒平局用例 PASS
- [ ] 新测试 PASS；两包回归与基线相比无新增 FAIL；build/vet/gofmt 全过
- [ ] `git status --porcelain` 只显示 Scope 里的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点计数不符，或 drift check 有输出且"Current state"摘录对不上。
2. 基线跑不起来。
3. 子代理 run 的 `parent_run_id` 实际**不**指向主 run（树取不到子代理行——先在测试造数阶段用真实
   `CreateSubagentRun`/直插形状核实；把 `fb_runs` 里子代理行的实际父子关系贴回来，不要改成 `session_id` 全表捞）。
4. cost-summary 响应因行数并入而显著膨胀（>200 行被截断导致"看起来少了子代理"）：报告实测行数，
   上限调整是 owner 口径（默认仍 200，不要自行调大）。
5. 你想顺手给 attempts 分页/游标或改 DTO 形状：**不要**（契约是仍是数组、行仍是 `LLMAttempt`）。

## Maintenance notes

- 读取端的"一次回答"从此 = 最新主 run 的 run 树。谁将来给 attempts 画 UI，用 `run_id` 区分主/子代理、
  用 `call` 分组逻辑请求、用 `attempt` 排物理次序（181 之后 attempt 是 call 内单调序号）。
- 200 上限与整树读取的取舍记在这里：一个重子代理回合可能触顶，截断从**最旧**的行开始（`ORDER BY id
  ASC LIMIT`）——若将来要"优先主 run"，那是读取端的下一个演进，不是本计划的口径。
- 与 181 正交：181 补写入（内层重发与非流式位），本计划补读取；两者都落地后账本三问
  （试了几次/每次什么结果/谁的请求）全部可答。
- **同族但不在本期**（`fb_runs` 上 `ORDER BY updated_at … LIMIT 1` 无平局破解的其它读法，各有各的
  读取端与后果，留 owner 裁决是否另立计划）：`runrt.go:293` liveRunRefusal（两存活行平局本属被独占
  检查禁止的态，任选其一都是有效答案）；`runrt.go:503` 最新 done run 的用量读取（同秒两个 done 平局
  任选，读到的 usage 可能非最新一回合）；`runrt.go:701` 最老 waiting_action run（ASC 平局任选）；
  `runrt.go:1358` agent 最新存活 run。谁将来改这些查询，先问 owner 是否统一破法。
