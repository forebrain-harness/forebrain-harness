# Plan 295: 「再评审」e2e 改成语义轮询，杜绝 `undefined === undefined` 假绿

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- frontend/e2e/exit-plan-approval.spec.ts frontend/e2e/office-plan-approval.spec.ts && git diff d715dafd -- frontend/e2e/exit-plan-approval.spec.ts frontend/e2e/office-plan-approval.spec.ts`
> 若两个文件在 `d715dafd` 之后已有改动，把「当前状态」的摘录与活代码比对；对不上即 STOP。
> `plans/` 与 `docs/plan/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: tests
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

`frontend/e2e/exit-plan-approval.spec.ts` 的「再评审」用例要证明的性质是：第二次评审**延续的是同一个 reviewer 子代理**（`agent_id` / `worker_session_id` 不变，`continues_review_id` 指回第一次）。当前实现用一个固定预算的手写轮询（最多 50 次、每次 `waitForTimeout(200)`、以「输出行数 ≥ 3」为退出条件），循环结束后**没有任何断言证明数据真的到齐**，然后直接把 `chain.trim().split('\n').flatMap(...)` 的前六个槽解构成 `[continues, second, agent1, worker1, agent2, worker2]`。

若第二轮 `subagent_spawned` 没在 10 秒内落库，`agent2` 与 `worker2` 是 `undefined`，`expect(undefined).toBe(undefined)` **会通过** —— 核心性质在最需要它的慢机器 / 负载场景下反而假绿。硬编码等待还让结果不可复现，而 CI 有意不跑浏览器 e2e（owner 2026-10-26 裁定，plan 174 REJECTED），这条证据全靠人工跑 `web_e2e.sh`。修成语义轮询 + 非空前置断言，成本极小。

`frontend/e2e/office-plan-approval.spec.ts:115-116` 的两条 `toHaveCount(0)` 属于同类「否定断言」；已核实同套件的 `exit-plan-approval.spec.ts:232`、`:241`、`:314` 有对应的正向夹具（交接按钮、交接模型行、再评审行都真的出现），所以**不需要新增夹具**，只需在 office spec 里写明对照出处，免得下一位读者误以为它们能证明门控逻辑。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Vue 3 前端，`@playwright/test` 1.64.0；e2e 由 `scripts/acceptance/web_e2e.sh` 驱动，见 `frontend/e2e/playwright.config.ts`）。

- `frontend/e2e/exit-plan-approval.spec.ts:326-345`（缺陷，逐字）：
  ```ts
  // The continuation is the SAME reviewer: the second started event points
  // back at the first, and the spawned subagent keeps its identity. The card
  // is drawn from the started event, so the spawned row can trail it by a
  // moment — poll until both rounds' subagents are on record.
  await expect(page.locator('[data-testid="subagent-call-card"]').nth(1)).toBeVisible({ timeout: 120_000 })
  let chain = ''
  for (let attempt = 0; attempt < 50; attempt++) {
    chain = execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(),
      `SELECT json_extract(payload_json,'$.continues_review_id'), json_extract(payload_json,'$.review_id') FROM fb_session_events WHERE session_id='${session}' AND event_type='plan_review_started' ORDER BY rowid DESC LIMIT 1; ` +
      `SELECT json_extract(payload_json,'$.agent_id'), json_extract(payload_json,'$.worker_session_id') FROM fb_session_events WHERE session_id='${session}' AND event_type='subagent_spawned' ORDER BY rowid;`], { encoding: 'utf8' })
    if (chain.trim().split('\n').length >= 3) break
    await page.waitForTimeout(200)
  }
  const [continues, second, agent1, worker1, agent2, worker2] = chain.trim().split('\n').flatMap((l) => l.split('|'))
  expect(continues).toBe(firstStarted)
  expect(second).toMatch(/^plan-review:/)
  expect(agent2).toBe(agent1)
  expect(worker2).toBe(worker1)
  await shot(page, 'exit-plan-review-again')
  ```
  注意 SQL 是**两条语句**：第一行是 `plan_review_started`（两列），其后每行是一条 `subagent_spawned`（两列）。`flatMap` 把全部列拍平，槽位数量完全依赖运行时行数。

- `:1-45`：文件头 import `execFileSync`（`node:child_process`）、`expect, test`（`@playwright/test`）、`seedSessionInRoot, signIn, shot`；`stateDb()` 与 `sqlite(sql)` 辅助函数也在这一带。
- `:275-325`：同一用例的前半段 —— 真回合出审批、选 reviewer、`firstStarted` 从 DB 读第一次 `plan_review_started.review_id`、种下第二次 exit-plan 审批、点「再评审」、断言请求体 `{ again: true }`、断言无第二个选择器。
- `frontend/e2e/office-plan-approval.spec.ts:108-120`：办公计划审批的六条否定断言，其中 `:114-116`：
  ```ts
  // plan 283's two rows stay hidden here for the right reasons: the hand-off
  // needs a git repository (this project is a plain directory), and no review
  // has run yet, so there is nothing for 再评审 to continue.
  await expect(page.locator('[data-testid="exit-plan-hand-off"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="exit-plan-review-again"]')).toHaveCount(0)
  ```

- 正向对照（已核实存在，**只读**）：`frontend/e2e/exit-plan-approval.spec.ts:232` `const handOff = page.locator('[data-testid="exit-plan-hand-off"]')`、`:241` `const rows = page.locator('[data-testid="exit-plan-hand-off-model"]')`、`:314` `const again = page.locator('[data-testid="exit-plan-review-again"]')`（随后 `toBeVisible()`）。

仓库约定：
- 浏览器 e2e **不进 CI**（owner 裁决），验证靠 `FOREBRAIN_E2E_SPEC="..." scripts/acceptance/web_e2e.sh` 人工/评审者跑；不要提议加 CI job。
- `scripts/acceptance/web_e2e.sh` 的 `FOREBRAIN_E2E_SPEC` 接受空格分隔的多个 spec；`playwright.config.ts` 里 `workers: 1`、`timeout: 60_000`（用例内部可自行 `test.info().setTimeout(...)`）。
- Playwright 1.64 支持 `expect.poll(...)`；用它替代手写 `waitForTimeout` 循环。
- 不改产品代码、不改网关 / 引擎、不改种子夹具的 SQL 形状（除解析方式）。
- 注释用英文；按钮中文标签是逐字定稿的，不动。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 单测（不受影响，确认前端没被误改） | `cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` | `1 passed (1)` 文件、`10 tests` 全 PASS，exit 0 |
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0，0 errors |
| e2e 定向 | `FOREBRAIN_E2E_SPEC="e2e/exit-plan-approval.spec.ts e2e/office-plan-approval.spec.ts" scripts/acceptance/web_e2e.sh` | 两个 spec 全 PASS（计划 283 收口时为 8/8） |
| 静态检查 | `grep -nE 'waitForTimeout\(200\)|flatMap\(\(l\) => l.split' frontend/e2e/exit-plan-approval.spec.ts` | 无输出 |

## Scope

**In scope**（只允许改这两个文件）：
- `frontend/e2e/exit-plan-approval.spec.ts`
- `frontend/e2e/office-plan-approval.spec.ts`

**Out of scope**（不许动）：
- `frontend/src/**`、`pkg/**` —— 本计划只改测试证据，不碰产品代码。
- `frontend/e2e/support.*`、`playwright.config.ts`、`scripts/acceptance/web_e2e.sh`。
- CI 配置（`.github/workflows/*`）—— 明确不新增 e2e job。
- 其他 spec 的断言与截图名称（`shot(page, 'exit-plan-review-again')` 保持不变）。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `test(e2e): assert the review-again chain instead of polling a fixed budget`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

### Step 1: 把 DB 读取抽成一个结构化函数并做语义轮询

在 `frontend/e2e/exit-plan-approval.spec.ts` 里，把 `:326-344` 的整段（从 `let chain = ''` 到 `expect(worker2).toBe(worker1)`）替换掉。保留 `await expect(...subagent-call-card...).nth(1)).toBeVisible({ timeout: 120_000 })` 与末尾 `await shot(...)`。

新增一个文件级小函数（放在 `stateDb()` / `sqlite()` 附近，或紧贴用例，二选一，保持文件风格）：

```ts
/** The two SQL statements the chain reads: one started row (2 columns), then
 *  one spawned row per round (2 columns each). Parse by statement, not by a
 *  flat list of slots — a short result must never look like a valid chain. */
type ReviewChain = {
  continues: string
  second: string
  spawned: Array<{ agent: string; worker: string }>
}

function readReviewChain(session: string): ReviewChain {
  const out = execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(),
    `SELECT json_extract(payload_json,'$.continues_review_id'), json_extract(payload_json,'$.review_id') FROM fb_session_events WHERE session_id='${session}' AND event_type='plan_review_started' ORDER BY rowid DESC LIMIT 1; ` +
    `SELECT json_extract(payload_json,'$.agent_id'), json_extract(payload_json,'$.worker_session_id') FROM fb_session_events WHERE session_id='${session}' AND event_type='subagent_spawned' ORDER BY rowid;`], { encoding: 'utf8' })
  const lines = out.split('\n').map((l) => l.trim()).filter(Boolean)
  const [continues = '', second = ''] = (lines[0] ?? '').split('|')
  const spawned = lines.slice(1).map((l) => {
    const [agent = '', worker = ''] = l.split('|')
    return { agent, worker }
  })
  return { continues, second, spawned }
}
```

然后：

```ts
// Poll the semantic condition, not a line count: both spawned rounds must be
// on record with non-empty identities before the comparison means anything.
await expect.poll(
  () => {
    const c = readReviewChain(session)
    return c.continues === firstStarted &&
      /^plan-review:/.test(c.second) &&
      c.spawned.length >= 2 &&
      c.spawned.slice(0, 2).every((s) => s.agent !== '' && s.worker !== '')
  },
  { timeout: 30_000, intervals: [200, 500, 1000] },
).toBe(true)

const chain = readReviewChain(session)
expect(chain.continues).toBe(firstStarted)
expect(chain.second).toMatch(/^plan-review:/)
expect(chain.spawned[0]?.agent).toBeTruthy()
expect(chain.spawned[0]?.worker).toBeTruthy()
expect(chain.spawned[1]?.agent).toBe(chain.spawned[0]?.agent)
expect(chain.spawned[1]?.worker).toBe(chain.spawned[0]?.worker)
```

要点（别简化掉）：
- `expect.poll` 超时会**失败**，不会像原循环那样耗尽预算后继续跑 —— 这就是「成功条件必须达成」的硬保证。
- 所有比较前先断言 `agent` / `worker` 非空（`toBeTruthy()`），杜绝 `undefined === undefined`。
- 删掉 `waitForTimeout(200)` 与 `flatMap` 的六槽解构；不要保留 `let chain = ''`。

**Verify**：`cd frontend && pnpm typecheck` → exit 0、0 errors；`grep -nE 'waitForTimeout\(200\)|flatMap\(\(l\) => l.split' frontend/e2e/exit-plan-approval.spec.ts` → 无输出；`grep -n 'expect.poll' frontend/e2e/exit-plan-approval.spec.ts` 有 1 处命中。

### Step 2: 给 office spec 的两条否定断言标注正向对照

在 `frontend/e2e/office-plan-approval.spec.ts:114-116` 的注释里补一句英文，说明这两条否定断言的正向对照在哪里（同套件 `exit-plan-approval.spec.ts` 的交接按钮 / 交接模型行 / 再评审行断言），以及它们各自验证的门控条件（非 git 项目不显示交接行、无既往评审记录不显示再评审行）。**只改注释，不加断言、不加夹具** —— 正向夹具已存在，重复添加只会拖慢串行 e2e。

**Verify**：`grep -n 'positive control\|exit-plan-approval.spec.ts' frontend/e2e/office-plan-approval.spec.ts` 有命中。

### Step 3: 真跑两个 spec

**Verify**：
- `FOREBRAIN_E2E_SPEC="e2e/exit-plan-approval.spec.ts e2e/office-plan-approval.spec.ts" scripts/acceptance/web_e2e.sh` → 两个 spec 全 PASS（计划 283 收口记录里这条命令是 8/8 PASS，作为基线对照）。
- `cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` → `1 passed (1)`、10 tests PASS（确认前端单测没被误改影响）。`pnpm test -- <路径>` **不会**过滤：它跑全量，并因既有的 10 个 unhandled rejection（`frontend/src/components/models/ModelFormDrawer.vue:575` 的 `capsTimer` TDZ，本计划范围外）以 exit 1 结束 —— 不要用它当证据。

若本机缺 Chrome / 网关二进制依赖导致脚本起不来，见 STOP 条件 —— **不要**把「没跑」写成「通过」。

## Test plan

- 本计划加固的是既有用例（`exit-plan-approval.spec.ts` 的「再评审」用例），不新增 spec 文件。
- 覆盖：
  - 语义轮询：`expect.poll` 必须真的等到「两条 started/spawned 链齐全且身份非空」；超时即 FAIL；
  - 非空前置：比较前对 `agent` / `worker` 各断言一次 `toBeTruthy()`；
  - 回归：`continues === firstStarted`、`second` 匹配 `^plan-review:`、`agent2 === agent1`、`worker2 === worker1` 一条不少；
  - 对照标注：office spec 的两条否定断言注明正向对照出处。
- 验证：`FOREBRAIN_E2E_SPEC="..." scripts/acceptance/web_e2e.sh` 全 PASS；`pnpm typecheck` 0 错。禁止以「本地没跑但代码看起来对」当通过证据。

## Done criteria

- [ ] `grep -n 'expect.poll' frontend/e2e/exit-plan-approval.spec.ts` 有 1 处命中
- [ ] `grep -nE 'waitForTimeout\(200\)|flatMap\(\(l\) => l.split' frontend/e2e/exit-plan-approval.spec.ts` 无输出
- [ ] `grep -c 'toBeTruthy()' frontend/e2e/exit-plan-approval.spec.ts` ≥ 2（两条非空前置断言）
- [ ] `grep -n 'positive control' frontend/e2e/office-plan-approval.spec.ts` 有命中
- [ ] `cd frontend && pnpm typecheck` exit 0、0 errors
- [ ] `FOREBRAIN_E2E_SPEC="e2e/exit-plan-approval.spec.ts e2e/office-plan-approval.spec.ts" scripts/acceptance/web_e2e.sh` 全 PASS，报告里写明实际通过数（基线 8/8）
- [ ] `cd frontend && pnpm exec vitest run src/components/chat/PlanApprovalDock.test.ts` → `1 passed (1)`、10 tests PASS
- [ ] `git status --porcelain -- frontend/e2e` 只列出两个 spec 文件
- [ ] `plans/README.md` 中本计划状态行已更新

## STOP conditions

- Drift check 显示两个 spec 在 `d715dafd` 之后被改过，且摘录对不上。
- 当前 Playwright 版本不支持 `expect.poll`（本仓是 1.64.0，应当支持）；若确实不支持，先报告，不要退回手写 `waitForTimeout` 循环。
- `web_e2e.sh` 因缺 Chrome / 无法起网关 / 构建失败而跑不起来：不要声称 e2e 通过；把失败原因与已完成的静态验证写进报告，并把本计划状态标为 BLOCKED（不标 DONE）。
- 语义轮询在 30s 内稳定超时，说明第二轮 `subagent_spawned` 的落库时序与此用例的假设不符 —— 这是产品侧的竞态线索，先报告，不要靠调大超时掩盖。
- 修复需要改 `frontend/src/**`、`pkg/**` 或 CI 配置。
- 同一验证连续两次失败。

## Maintenance notes

- 以后给这条链路加新断言时，继续走「结构化解析 → 非空前置 → 语义轮询 → 逐字段比较」的顺序；不要再回到「按行数 break + 拍平槽位」的写法。
- 评审 PR 时重点看：`expect.poll` 的条件是否包含「两条 spawned 行且身份非空」，以及 `expect.poll` 之后是否还有逐字段断言（只轮询不复核会丢掉诊断信息）。
- 已核实：office spec 的两条 `toHaveCount(0)` 在 `exit-plan-approval.spec.ts:232/241/314` 有正向对照，因此不是假绿；本计划只补注释出处，避免下轮重复上报。
- 明确不做：把浏览器 e2e 加进 CI（owner 2026-10-26 已裁定）、改 `playwright.config.ts` 的全局 timeout、给假绿加截图断言。
