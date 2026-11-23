# Plan 043：项目键跟随会话的项目——修 027 之后 gateway 计划文件/审批/记忆/cron 会话的空项目键

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发 "STOP conditions"
> 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**：不建分支、不 commit、不 push、不 stash；改动留在工作区。

## Status

- **Priority**: P1（回归：web 的计划模式与 cron 点火在本工作区已坏）
- **Effort**: M
- **Risk**: MED
- **Depends on**: 025、026、027、028、029（都已落地、未提交）
- **Category**: bug / regression
- **Planned at**: commit `005c1a8` + 主树未提交改动（025–029），2026-10-11

## Why this matters（已复现，不是推测）

027 让 gateway 的进程不再有启动项目（`OpenOptions.NoLaunchProject`），于是 `Environment.Runner.ProjectKey`
与 `Server.Runner.ProjectKey` 恒为空。可是 027 同时让每个会话跑在**自己项目**的 runner 上，那个 runner 的
`ProjectKey` 是有值的。读写两条路因此分叉：

- 评审方独立复跑 `FOREBRAIN_E2E_SPEC="e2e/cron-fire.spec.ts e2e/exit-plan-approval.spec.ts" scripts/acceptance/web_e2e.sh`
  → **4 failed / 2 passed**（`cron-fire.spec.ts:78`、`:126`、`exit-plan-approval.spec.ts:90`、`:172`）。
- 029 执行者复跑全量 → 87 passed / 5 failed / 2 skipped（多出的一条是 `subagent-conversation.spec.ts` 的 roster 标签）。

已知分叉点：

| 位置 | 今天取的值 | 应该是 |
|---|---|---|
| `pkg/gateway/server.go` `s.projectKey()` → `s.Runner.ProjectKey`（调用方 `approval.go:37`、`memories_api.go:138`、`api_extra.go:3005`） | 空 | 该**会话**所属项目的键 |
| `pkg/process/run_executor.go` `env.projectScope()` → `env.Runner.ProjectKey`，喂给 `AgentContextForProject` | 空 | 该会话所属项目的键 |
| `pkg/state/plan.go` 的 `PlanDirForSession(workspaceRoot, projectKey, sessionID)` | 由上面两处喂 | 同上 |

`projectScope` 自己的注释就写着：喂了不同的键，"每个计划写入都会被拒绝"。这不是新语义，是 027 漏改的调用方。

另有 cron：025 的迁移把 `project_id IS NULL` 的定时任务连同运行记录删掉（"每个定时任务都属于一个项目"），
但 gateway 建定时任务时仍写 `project_id = NULL`，其点火会话因此没有项目，被 027 的严格 `RunnerForTurn`
拒绝（`turn cannot start: no runner for the session … session has no project`）。数据模型与写入方自相矛盾。

## Scope

**In scope**：
- `pkg/gateway/{server.go,approval.go,memories_api.go,api_extra.go}`、`pkg/process/{run_executor.go,runner_pool.go}`；
- `pkg/state`（只在需要新的单会话读取函数时，例如 `ProjectStore.ProjectForSession` 已有则不加）；
- `frontend/src/`（只在 cron 新建需要带上项目时；定时任务面板已在项目空间，项目 id 现成）；
- `frontend/e2e/*.spec.ts`、`frontend/e2e/support.ts`；`pkg/gateway/dist`。

**Out of scope**：计划 035 的通道、定时任务的完整项目化（迁移、旧配置删除、通道 UI）；计划 017 的 subagent 视图。

## Steps

### Step 1：一个"会话 → 项目键"的解析点

在 `pkg/process` 增加（池里已有 `ProjectForSession`）：

```go
// SessionProjectKey is the key a session's plan files live under: its project's
// key, derived from the project root, or "" when the session has no project.
func (env *Environment) SessionProjectKey(ctx context.Context, sessionID string) string
```

- 会话有项目 → `state.ProjectKeyForRoot(project.Root)`；
- 没有项目（工坊会话）→ 空（工坊会话的计划文件今天就在项目的无键目录，不要改它的行为）；
- 读不到状态库 → 空。

### Step 2：把三个取键点换掉

1. `pkg/process/run_executor.go` 的 `AgentContextForProject(..., x.env.projectScope())` → `x.env.SessionProjectKey(ctx, sid)`。
   核对 `projectScope()` 是否还有其他调用方；没有就删掉它（连同注释）。
2. gateway：
   - `approval.go:37` `runner.ProjectKey` → 该会话的项目键（审批锚点与计划目录必须同一把键）；
   - `memories_api.go:138` `s.Runner.ProjectKey` → 同上；
   - `api_extra.go:3005` 计划 md 读写 → 同上；
   - `server.go:405` `s.projectKey()`：**删掉它**，每个调用点改为按会话解析（它已经没有正确的实现）。
3. 用 `grep -rn "\.ProjectKey\b" pkg/gateway pkg/process --include='*.go' | grep -v _test` 逐行核对，
   把报告里剩下的每一处连同"为什么它是对的"列出来。

**Verify**：新测试——
- gateway：一个属于项目 P 的会话（进程无启动项目）读 `/api/chat/sessions/:id/plan-md` 命中 `plans/<P 的键>/<sid>/`；
- `pkg/process`：`SessionProjectKey` 对有项目、无项目、库不可用三种输入返回正确值。

### Step 3：定时任务必须属于一个项目

1. 建定时任务的接口（`grep -n "func (s \*Server) handleCronJobs" -A 40 pkg/gateway/api_extra.go`）接受 `project_id`：
   - 属于本主代理、根存在；缺失 → 400 `{"error":"project_required"}`；
   - 写入 `fb_cron_jobs.project_id`。
2. 点火会话（`run_executor.go`）在 `Ensure` 时用 `SessionBirth{ProjectID: job.ProjectID, Cwd: 项目根}` 出生，
   与 028 的会话出生同口径；工作目录即项目根，因此 026 之后工具就在项目根执行。
3. 前端：项目空间的定时任务新建带上当前项目 id（项目空间路由里已有）；`grep -rn "cronJobCreate\|cronJobsCreate" frontend/src` 找调用点。
4. `frontend/e2e/cron-fire.spec.ts`：夹具改成"项目里的定时任务"，断言照旧（点火是一条能打开的对话、不在抽屉里、语言按 viewer）。

**Verify**：gateway 测试（建任务必须带项目；点火会话 `project_id` 与 `cwd` 正确）+ `cron-fire.spec.ts` 两条 PASS。

### Step 4：回归验收

- `FOREBRAIN_E2E_SPEC="e2e/cron-fire.spec.ts e2e/exit-plan-approval.spec.ts scripts/acceptance/web_e2e.sh"` 目标两条 spec 必须全绿；
- 再跑一次全量 `scripts/acceptance/web_e2e.sh`，把结果与 029 报告的 87/5/2 对比。`subagent-conversation.spec.ts`
  那条 roster 失败若仍存在，**报告**它（属 017/027 领域），不要在本计划里改。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/process/... ./pkg/state/... ./pkg/run/... ./pkg/turn/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && corepack pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| UI | `make ui` | 成功 |
| 验收 | `scripts/acceptance/web_e2e.sh`（可用 `FOREBRAIN_E2E_SPEC` 定向） | 至少不再有 cron-fire / exit-plan-approval 的失败 |
| 依赖图 | `scripts/package-graph.sh` 后 `git diff --stat pkg/architecture/testdata/graph.json` | 只反映 loc/边的真实变化 |

## Done criteria

- [ ] Commands 表全部通过，`make ui` 成功
- [ ] `cron-fire.spec.ts`、`exit-plan-approval.spec.ts` 全绿（附输出）
- [ ] `grep -rn "s.Runner.ProjectKey\|env.Runner.ProjectKey" pkg --include='*.go' | grep -v _test` 无输出
- [ ] 全量 e2e 结果与 029 基线逐条对比写在报告里
- [ ] `git status --short`：本计划新增改动只涉及 Scope 文件

## STOP conditions

1. `PlanDirForSession` 的键不是"会话的项目键"（读一遍确认，别猜）。
2. 建定时任务要带项目这件事，牵出通道（channel）或迁移的改动 → STOP，那属 035。
3. 修完之后 `exit-plan-approval.spec.ts` 仍失败，且原因不是项目键 → STOP 报告，不要扩到 plan review 子系统。
4. 需要改 `pkg/turn`/`pkg/run` 的计划写入路径才能让读写同源 → 说明分叉在更深处，STOP 报告。

## Maintenance notes

- 项目键的唯一来源是 `state.ProjectKeyForRoot`。运行时任何"当前项目"的读取都必须来自**会话所属项目**，
  不能来自进程或基础 runner——027 之后进程没有项目，`Runner.ProjectKey` 只对项目 runner 有意义。
- 以后新增"按会话取配置/目录"的代码，走 `Environment.SessionProjectKey`，不要读 `env.Runner`。

---

## 勘误（2026-10-11，执行后由评审方补写；本计划原编号 038，因另一个会话已占用 038–042 而改为 043）

- **Step 3.1/3.3 不成立**：定时任务的项目管道在 027 之前就已经存在（建任务写 `project_id`、`startCronFire`
  已按任务的项目出生并绑定会话，有 `TestProjectCronFireRunsInItsProject` 等既有测试）。要求"建任务必须带项目"
  反而会打红 agent 级 cron 面（`cron-builder.spec.ts`、`TestCronJobsRoundTripThroughTheAPI`）。
  本计划实际只改了 e2e 夹具（Step 3.4）。**遗留**：agent 级（无项目）定时任务现在建得出来但永远点不着火——
  属于计划 035「定时任务属于项目」的范围。
- 评审方独立复跑：`FOREBRAIN_E2E_SPEC="e2e/cron-fire.spec.ts e2e/exit-plan-approval.spec.ts"` → 6 passed / 0 failed。
