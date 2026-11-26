# Plan 382: exit_plan_mode 再次审批时，面板显示最近一次 plan-review 的结论行

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**:
> `git diff --stat 364a11a2 -- pkg/turn/model.go pkg/turn/approval.go pkg/turn/approval_test.go pkg/tui/overlays.go pkg/tui/chat_session_test.go pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go frontend/src/lib/api.ts frontend/src/components/chat/PlanApprovalDock.vue frontend/src/components/chat/PlanApprovalDock.test.ts frontend/src/locales/index.ts .claude/skills/run-forebrain/fake_provider.py .claude/skills/run-forebrain/driver.sh`
> and `git status --porcelain -- <same paths>`. If any in-scope file has
> changed since this plan was written — committed or not — compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch **in the cited region**, treat it as a STOP condition. Line
> numbers alone drifting is NOT a stop: re-locate by symbol/excerpt. Note:
> `pkg/tui/overlays.go` may carry unrelated edits in its questionOverlay
> region (another agent's plan 383, lines ~3200–4400); those are expected
> drift, not a STOP — only the approvalOverlay regions cited below matter.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW（纯增量显示字段；不改任何既有决策/交付/回放语义）
- **Depends on**: none
- **Category**: dx（审批 UX 缺口）
- **Planned at**: commit `364a11a2`（工作树干净）, 2026-11-25
- **Owner 裁决（2026-11-25，本会话 user_interaction）**:
  1. 显示内容＝**评审首行原样**（verdict＋一句理由，markdown 渲染，不做枚举解析）；
  2. 显示范围＝**仅会话最近一条 outcome=done 的评审**。

## Execution record（2026-11-25，execute＋评审）

- **Verdict**: **APPROVE**（评审方亲自复跑全部 done criteria，含真机抓屏与 DB 实证）
- **Worktree**: `.worktrees/exit-plan-review-verdict`，分支 `exit-plan-review-verdict`，
  基于 `364a11a2`（＝dev tip），5 提交，`git status` 干净；**已按 owner 指示 ff-only
  落 dev `c3176812`**（2026-11-25，未 push）；落地后主树复跑 gofmt／vet／`pkg/turn`＋
  `pkg/gateway` 全量／`pkg/tui -run ExitPlan` 全绿（前端与全量套件已在同内容上跑过，
  未重复）；**worktree 与同名分支已按 owner 指示删除**（验收 scratch home 一并清理）
- **Commits**: `b3b3a09d` feat(turn) → `3d16b545` feat(tui) → `e0b45071` feat(gateway)
  → `1719d8e5` feat(web) → `c3176812` test(run-forebrain)
- **Files**: 13 个，全部在 Scope 内（`plans/README.md` 由评审方维护，执行体未动；
  未触碰 reducer.go / render.go / questionOverlay / `PlanReviewsForAction` / 存储层）
- **评审方复跑（均在 worktree 内）**:
  - `gofmt -l cmd pkg third_party plugins` → 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0
  - `go test ./pkg/turn ./pkg/gateway` → ok（4.1s / 9.4s）；新用例逐个 `-run` 点名均 PASS
    （turn 6 个、tui 1 个、gateway 1 个）
  - `go test ./pkg/tui -run ExitPlan` → ok；全量 `./pkg/tui` → ok（198.7s）
  - `cd frontend && pnpm typecheck` → exit 0；`pnpm test PlanApprovalDock` → 25/25 pass
  - 全量 `FOREBRAIN_HOME=$(mktemp -d) go test -tags fts5 ./... -timeout 30m` → 仅
    `pkg/architecture TestTestFilesCorrespondToProductionFiles` 红，**预存**：基线
    `364a11a2` 里 `pkg/tui/deliverables_outcome_test.go` 存在而无同名生产文件
    （`80bb73b4` 引入），本 diff 零 deliverables 改动
  - **真机（独立复跑，非引用执行体抓屏）**：`driver.sh start plan-review` 走通全流程——
    第一屏 `Tool: exit_plan_mode` ＋ `1. Ship the change.`，`grep -c 'Review by'` = 0；
    第二屏 `Review by deepseek/deepseek-chat` ＋
    `Verdict: approve with changes — the step order is sound; one verification command needs its expected output pinned.`；
    DB 实证根因链：评审事件 `action_id=12c43f10`、该 action `denied {"plan_review_delivered":true}`、
    新 pending action `a2ea42d0` 正是显示结论行的那一个
- **偏差（执行体报告，评审按价值接受）**: ① `make build` 的 `fetch-libtokenizers.sh` 连不上
  GitHub → 执行体把主树同版本 third_party 工件复制进 worktree（gitignored，零 tracked 改动）；
  ② 真机验收要求 scratch project 是 git 仓库（否则 `state.DefaultProjectTypeForRoot` 判成
  office、plan 工具未注册——预存行为，`toolcall` 模式同样失败）→ 执行体在隔离 proj 里
  `git init`，未改 driver 逻辑；③ done criteria 的 `grep LatestReviewVerdict pkg/turn/approval.go`
  只命中 `PendingForRun` 接线，因为 helper 名是 `LatestPlanReviewVerdict`（计划即如此定名），
  实际命中 `approval.go:1643` 定义与 `:2012` 调用；④ `pnpm test -- PlanApprovalDock` 并不过滤
  （跑全量 1602 例，也全绿），聚焦用 `pnpm test PlanApprovalDock`。
- **一行遗留（范围外）**: `.claude/skills/run-forebrain/SKILL.md` 的模式表还没有
  `plan-review` 一行（driver usage 已写）；`driver.sh` 的 scratch project 顺手 `git init`
  可让 `toolcall` 与 `plan-review` 都不必手工初始化。
- **风格 nit（未修）**: `pkg/turn/approval.go:1636` 与 `pkg/turn/model.go:709` 的新注释紧贴
  上一个 `)`/`}`，缺一行空行；gofmt 无异议，与周围风格略不一致，留给 owner 顺手决定。

## Why this matters

用户在 exit_plan_mode 审批面板选「Ask another model to review this plan」后，
评审完成会被引擎自动交付给规划模型（旧审批以内部 marker denial 关闭），规划
模型修改计划后再次发起 exit_plan_mode。**新审批面板没有任何评审信息**——用户
想知道 reviewer 给的是 approve / approve with changes / rework，必须回时间线
翻 reviewer 子代理卡。本计划在再次审批的面板里（TUI overlay 与 web dock 两端）
显示最近一条已完成评审的结论行，让用户在决策当场看到评审结论。

## Current state

### 根因链（每一环都已对照源码核实）

1. 用户选评审 → `runPlanReview`（`pkg/tui/chat_surface.go:1181`）→
   `turn.RunPlanReview`（`pkg/turn/approval.go:1702`）发
   `plan_review_started` / `plan_reviewed` 事件，**ActionID = 当前审批 A**
   （`approval.go:1727-1755`）。事件是评审结果的唯一存储。
2. 评审成功 → 立即交付（`chat_surface.go:1228-1235`）：

   ```go
   // A review that finished delivers itself: the planner revises against it
   // and submits a fresh exit_plan_mode, so the approval the user answers
   // next has already absorbed the review. ...
   if reviewErr == nil {
       s.deliverCompletedPlanReview(ctx, actionID)
   }
   ```

   `DeliverPlanReview`（`approval.go:196-209`）把 A 以
   `PlanReviewDeliveredReason` marker denial 关闭 → planner 收到评审全文 →
   修改计划 → 新 exit_plan_mode → **新 action B**。
3. 两端面板都从同一个共享派生点取审批请求：
   `PendingApprovalGate.Pending → PendingForRun`（`approval.go:1882-1974`）。
   其中评审信息的填充（`approval.go:1948-1955`）：

   ```go
   if strings.EqualFold(toolName, "exit_plan_mode") {
       if g.ReviewModels != nil {
           req.PlanReviewModels = g.ReviewModels(sessionID)
       }
       results, err := PlanReviewsForAction(ctx, g.Runs, sessionID, actionID)
       if err == nil {
           req.PlanReviews = planReviewNotesFrom(results)
       }
   ```

   而 `PlanReviewsForAction`（`approval.go:1432-1469`）**按 action 过滤**：

   ```go
   if !strings.EqualFold(strings.TrimSpace(payload.ActionID), actionID) ||
       strings.TrimSpace(payload.Outcome) != planReviewOutcomeDone {
       continue
   }
   ```

   → B 名下没有评审事件 → `req.PlanReviews` 为空 → 面板零评审信息。
   这就是痛点的根因：评审按 action 键控，交付即关 action，新 action 看不到。
4. 渲染现状：
   - TUI：`pkg/tui/overlays.go:597-613` 在计划块后渲染 `req.PlanReviews`
     （"Review by <model>" 头＋分隔线＋评审全文 markdown），仅在恢复窗口
     （评审完成但交付前进程重启，同一 action 重新提示）可见。
   - web：`frontend/src/components/chat/PlanApprovalDock.vue` **完全不渲染**
     `planReviews`（全前端只有 `api.ts:1764` 的类型声明消费它）。
5. 评审全文仍留在时间线的 reviewer 子代理卡里（`chat_surface.go:1200-1203`
   注释：review run opens its own subagent block in the transcript）。

### 结论行的格式契约与真实样本

reviewer 系统提示词强制首行格式（`pkg/agent/subagent_defs.go:126`，
`planReviewerMethod`，所有项目类型共用）：

```
"- First line: `Verdict: approve` | `approve with changes` | `rework`, with one sentence of justification.\n"
```

**真实状态库样本**（`~/.forebrain/state/forebrain.state.sqlite`，
`fb_session_events` 表 `event_type='plan_reviewed'`，只读查询所得；payload
字段为 `payload_json`，文本在 `$.text`）——首行存在英文/中文/加粗变体：

```
Verdict: approve — 上轮唯一的遗留问题已修好并经 8 次重复跑证实稳定，…
Verdict: approve with changes — 上轮两个问题都已修好并经干跑/真机证实，…
**Verdict: rework** — 修订稿的核心机制（Step 3/4：…）在真实链路上取不到值。…
**结论：approve** — 上一轮提出的三处问题都已修正，并逐条核对无误；…
**结论：批准，但需要做一处修改**——上一轮提出的两个问题都已修正；…
```

**因此不做 verdict 枚举解析**（owner 裁决）：取评审文本的**首个非空行原样**
（只 trim 首尾空白，保留 markdown 记号），交给两端既有的 markdown 渲染。
这天然兼容中文自由措辞与加粗变体。

### 必须尊重的既有语义（不得改动）

- **`PlanReviews` 保持 action 键控**。`overlays.go:742-744` 的 deny 反馈提示
  「The review above is sent to Forebrain Harness with your feedback.」只对
  action-scoped 评审成立；`ComposeDenyFeedback`（`approval.go:1169`）与
  `BuildApprovalResumeWithPlanReviews`（`approval.go:166`）也只收
  action-scoped 评审。**新字段独立于 `PlanReviews`，绝不放宽
  `PlanReviewsForAction` 的过滤**——否则提示文案对模型收到的内容说谎。
- **gate 无卡**（owner 裁决「任何情况下不要显示 Exiting plan mode 消息」）：
  本计划只改审批 overlay/dock 的内容，不新增/改动任何 transcript 卡，
  不触碰 `tool.ToolStepHoldsNoCard` / web `stepHoldsNoCard` 共享卡规则。
- 事件读取 oldest-first；outcome 词表 `done/stopped/timed_out/failed`
  （`approval.go:1629-1634`），只有 `done` 算完成评审。
- 系统消息文案说人话（owner 常设规则）；面板极简、labels only（owner 既往
  裁决）：结论行不新增说明 caption、不新增交互键。
- 并行协作：另一代理（forebrain-harness-7a）在改 `pkg/tui/reducer.go`、
  `render.go`、`overlays.go` 的 questionOverlay 区域（plan 383）。本计划
  **只改 overlays.go 的 buildApprovalContent（approvalOverlay）区域**，
  不碰 reducer.go/render.go/questionOverlay。若需同文件并行，先 peer_send。

### 关键符号坐标（写计划时核实于 364a11a2）

| 符号 | 位置 | 作用 |
|---|---|---|
| `ToolApprovalRequest` | `pkg/turn/model.go:~560-618` | 审批请求；`PlanReviews []PlanReviewNote` 在 :602 |
| `PlanReviewNote` | `pkg/turn/model.go:694-700` | 显示用评审注记（Provider/Model/ModelName/Text/Duration）——新类型的形状范本 |
| `PlanReviewLog` | `pkg/turn/approval.go:1419-1421` | 事件读接口（`ListSessionEventsOfType`） |
| `PlanReviewsForAction` | `pkg/turn/approval.go:1432` | action 键控读取（不改） |
| `LastPlanReview` | `pkg/turn/approval.go:1594` | session 键控读取的既有范本（取最新 started，供 review-again） |
| `PendingForRun` | `pkg/turn/approval.go:1882-1974` | 唯一派生点；exit-plan 分支 :1948-1972 |
| `planReviewModelDisplay` | `pkg/tui/overlays.go:1781` | TUI 的 provider/model 显示 |
| `buildApprovalContent` | `pkg/tui/overlays.go:529` | overlay 正文；评审块 :597-613；问句条件 :632 |
| `sessionApprovalRequestWire` | `pkg/gateway/api_extra.go:4634-4644` | web wire；投影在 :4598-4607 |
| `SessionApprovalRequest` | `frontend/src/lib/api.ts:1758-1785` | 前端类型；`toCamelCase`（api.ts:3 自 `./case` 导入，递归转换见 `frontend/src/lib/case.ts`；调用点 :3913）自动蛇形→驼峰 |
| `PlanApprovalDock.vue` | `frontend/src/components/chat/PlanApprovalDock.vue` | xp-doc :32-34 与 gate-ask :35 之间是插入点；`request` computed 用法见 :328-329 |
| `activeReviewLabel` | `frontend/src/composables/usePendingActions.ts:143-146` | 模型标签拼法范本：`[provider, modelName \|\| model].filter(Boolean).join('/')` |
| locale | `frontend/src/locales/index.ts` | zh 区 `chat.exitPlanAskReview` :2277 附近；en 区 :4871 附近 |
| `planReviewEventDB` / `appendPlanReviewed` | `pkg/turn/approval_test.go:681 / :694` | gate 级测试 helper |
| `exitPlanReviewRequest` | `pkg/tui/chat_session_test.go:11023` | TUI overlay 测试 helper |
| `exitPlanServer` | `pkg/gateway/api_extra_test.go`（:5389 测试使用） | gateway wire 测试 helper |
| reviewer 识别标记 | `pkg/agent/subagent_defs.go:114` | `planReviewerOpening = "You are reviewing a plan another agent wrote. …"` |
| `_is_turn_request` | `.claude/skills/run-forebrain/fake_provider.py:444` | 有工具表且非 memory/compaction 即算 turn——**reviewer 请求也带工具表，须先按系统提示词标记排除** |

### 仓库约定（ exemplar ）

- 错误处理/读取降级：事件解析失败 `continue`、读失败返回空——照
  `PlanReviewsForAction`（approval.go:1443-1453）与 `LastPlanReview`
  （:1599-1601）的既有形状，不新增错误类型。
- 测试风格：testify `require`，表驱动/直断言均有；gate 级测试范本
  `TestPendingExitPlanRequestCarriesPlanReviewsAndModels`
  （approval_test.go:738）；TUI 渲染测试范本
  `TestExitPlanOverlayRendersPickerAndCollectedReviews`
  （chat_session_test.go:11176，`buildApprovalContent(100)` + `stripANSI` +
  `strings.Contains`）；gateway wire 测试范本
  `TestSessionApprovalRequestCarriesTheExitPlanCard`
  （api_extra_test.go:5389）；web 测试范本 `PlanApprovalDock.test.ts:75`
  （vitest + `data-testid`）。
- 注释风格：解释「为什么」的整句注释，随代码英文书写（照 surroundings）。

## Commands you will need

在本仓根目录运行。直接调 `go` 编译 `pkg/pdf` 需要自带 PKG_CONFIG_PATH
（Makefile 只在 make 内部设置）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

| Purpose | Command | Expected on success |
|---|---|---|
| 聚焦测试（引擎，新用例） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'Verdict' -count=1` | ok，全部新旧用例 pass |
| 聚焦测试（引擎，复现） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run NewExitPlanAction -count=1` | ok |
| 聚焦测试（TUI） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ExitPlanOverlay' -count=1` | ok |
| 聚焦测试（gateway） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'SessionApprovalRequest' -count=1` | ok |
| 前端类型 | `cd frontend && pnpm typecheck` | exit 0 |
| 前端聚焦测试 | `cd frontend && pnpm test -- PlanApprovalDock` | 全部 pass（含新用例） |
| 格式 | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| 全量 Go（scratch home） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m` | exit 0（pkg/tui 约 200s，勿用默认 10m 超时） |
| 前端全量 | `cd frontend && pnpm test` | 全部 pass |
| 真机验收 | run-forebrain skill 的 `driver.sh`（见 Step 6） | 第二屏含结论行 |

首次构建/原生依赖缺失时先 `make build`（会 fetch libtokenizers/pdfium）。
**不要**运行 `scripts/test-db.sh`（本计划不动 pkg/state 存储层）。

## Suggested executor toolkit

- **run-forebrain** skill（`.claude/skills/run-forebrain/SKILL.md`）：Step 6
  真机验收的唯一入口——driver.sh 的隔离 home/端口/tmux 配方、`screen` 抓屏、
  `serve` 播种模型行。先读它再动 driver。
- 若改了 TUI 渲染想做字节级 A/B 证明，可用 **tui-render-ab-verify** skill；
  本计划以 `buildApprovalContent` 单测＋真机抓屏为准，A/B 可选。

## Scope

**In scope**（只许改这些）:
- `pkg/turn/model.go` — 新类型 `PlanReviewVerdict`＋请求新字段
- `pkg/turn/approval.go` — 新 helper `LatestPlanReviewVerdict`＋`PendingForRun` 接线
- `pkg/turn/approval_test.go` — 复现/新行为测试
- `pkg/tui/overlays.go` — 仅 `buildApprovalContent` 的 exit-plan 区域（:597-634 附近）
- `pkg/tui/chat_session_test.go` — overlay 渲染测试
- `pkg/gateway/api_extra.go` — wire 结构与投影
- `pkg/gateway/api_extra_test.go` — wire 测试
- `frontend/src/lib/api.ts` — `SessionApprovalRequest` 新字段＋新 interface
- `frontend/src/components/chat/PlanApprovalDock.vue` — 结论行渲染
- `frontend/src/components/chat/PlanApprovalDock.test.ts` — 新用例
- `frontend/src/locales/index.ts` — zh/en 各一个新 key
- `.claude/skills/run-forebrain/fake_provider.py` — 新 `plan-review` 场景
- `.claude/skills/run-forebrain/driver.sh` — 场景名进 usage/注释（3 处字符串）
- `plans/README.md` — 本计划状态行（收尾）

**Out of scope**（一律不碰，即使看起来相关）:
- `pkg/tui/reducer.go`、`pkg/tui/render.go`、`overlays.go` 的 questionOverlay
  一族（:3200-4400 区域）——另一代理 plan 383 的领地。
- `PlanReviewsForAction` / `ComposeDenyFeedback` /
  `BuildApprovalResumeWithPlanReviews` / `DeliverPlanReview` 的语义——
  action 键控与模型侧 guidance 合同不变。
- transcript 卡显示规则（`ToolStepHoldsNoCard` / `stepHoldsNoCard`）、
  gate 锚定、回放——gate 依旧无卡。
- `pkg/agent/subagent_defs.go` 的 reviewer 提示词。
- web 对 action-scoped `planReviews`（恢复窗口全文）的渲染——预存相邻缺口，
  本计划不补（见 Maintenance notes）。
- hand-off 执行评审（APPROVE/REVISE/BLOCK）的显示、verdict 着色/枚举解析、
  面板内展开全文——owner 未选，明确推迟。
- `pkg/state` 存储层、任何迁移。

## Git workflow

- 被派发执行时：进 `.worktrees/exit-plan-review-verdict`（dispatch 从 HEAD
  创建），按逻辑单元 commit（仓库惯例 conventional commits，例：
  `feat(tui): show the latest plan-review verdict on a re-submitted exit-plan approval`）。
  **绝不 push、绝不 merge 进 dev/用户分支**——落地由评审方按 owner 指示做。
- 若被要求直接在主树工作区实施：owner 常设规则 no-git-commits——改动留工作区
  不 commit，报告里说明。
- 不 revert/stash/丢弃任何不是你做的改动；测试若红在你没碰的文件，先 peer_send
  问正在改它的代理。

## Steps

### Step 1: 复现缺口（只加测试，不改生产代码）

在 `pkg/turn/approval_test.go` 追加
`TestNewExitPlanActionSeesNoPriorReview`，照 :738
`TestPendingExitPlanRequestCarriesPlanReviewsAndModels` 的形状：
`planReviewEventDB(t)` 建库；创建 action A（exit_plan_mode）并
`appendPlanReviewed(t, runs, sessionID, A, "rev-old", "done", "Verdict: rework. …")`；
把 A 置为 denied（模拟交付关闭，`actions.DenyWithAnswer` 或直接
`state.ActionService` 的 deny 路径——照
`TestDeliverPlanReviewDeniesPendingExitPlan` :1112 的写法）；再创建**新**
pending action B，`SetWaitingAction` 指向 B；`gate.Pending(ctx, sessionID)`。
断言：`req.PlanReviews` 为空（B 名下无评审）——这就是面板无评审可画的机制。
此测试固化根因，修复前后都应 pass。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run NewExitPlanAction -count=1`
→ ok（1 个新测试 pass）。若它 fail，说明根因链与计划不符——STOP。

### Step 2: 引擎——类型、helper、派生点接线（红→绿）

2a. 先在 `approval_test.go` 写新行为测试（此时编译失败＝红态证据）：
- `TestLatestPlanReviewVerdictTakesTheNewestDone`：helper 单测，用
  `planReviewEventDB`/`appendPlanReviewed` 造事件，覆盖：
  - 两条 done 评审挂在不同 action → 返回**最新**一条的首行与模型身份；
  - 最新一条 outcome=failed/stopped/timed_out → 回退到更早的 done；
  - text 为空 / payload 非法 JSON → 跳过；
  - 首行前提取：text 以空行开头、首行带 `**` 加粗、中文「结论：…」→
    原样返回首个非空行（trim 首尾空白后逐字节相等）；
  - 无事件 / log=nil / sessionID 空 → false。
- `TestPendingExitPlanRequestCarriesLatestReviewVerdict`：gate 级，Step 1
  的形状但断言新字段：A 名下 done 评审（text 首行
  `"Verdict: approve with changes — one test needs its expected output pinned."`，
  正文多行）→ B 的 `req.LatestReviewVerdict` 非 nil，`Line` 恰为首行，
  `Provider/Model` 来自事件；`req.PlanReviews` 仍为空。
  反向：评审挂在 B 自己名下 → `req.PlanReviews` 有全文而
  `LatestReviewVerdict` 为 nil（不重复显示）；无任何评审 → nil。

**Verify（红）**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run LatestReviewVerdict -count=1`
→ 编译错误 `unknown field LatestReviewVerdict`（预期）。

2b. 实现：
- `pkg/turn/model.go`：`PlanReviewNote`（:694）之后加：

  ```go
  // PlanReviewVerdict is the conclusion line of a completed plan review, as
  // a later approval shows it: the reviewer's first line — verdict plus one
  // sentence of justification, verbatim — with the model that wrote it. The
  // line keeps its markdown; each surface renders it the way it renders the
  // plan itself.
  type PlanReviewVerdict struct {
      Provider  string
      Model     string
      ModelName string
      Line      string
  }
  ```

  `ToolApprovalRequest` 的 `PlanReviews` 字段（:602）后加：

  ```go
  // LatestReviewVerdict is the conversation's newest completed review's
  // conclusion line, shown when this approval collected no reviews of its
  // own: the planner already absorbed that review and resubmitted, and the
  // user decides with its verdict in view. Nil when there is none, or when
  // PlanReviews already carries this action's own reviews. Display only —
  // it never feeds the model-side deny feedback.
  LatestReviewVerdict *PlanReviewVerdict
  ```

- `pkg/turn/approval.go`：`LastPlanReview` 附近加 helper（读取形状照
  `PlanReviewsForAction`， newest-first 扫描照 `ActivePlanReview` :1534）：

  ```go
  // LatestPlanReviewVerdict returns the conclusion line of the conversation's
  // newest completed review — the verdict the planner last absorbed — for a
  // later approval to show. Only outcome "done" with a non-empty first line
  // counts; the line is the review's first non-empty line, verbatim, because
  // the reviewer prompt makes it the verdict plus one sentence of justification
  // and real reviews write it in English or Chinese, plain or bold.
  func LatestPlanReviewVerdict(ctx context.Context, log PlanReviewLog, sessionID string) (PlanReviewVerdict, bool) {
      // trim/nil guards → false; ListSessionEventsOfType(ctx, sessionID,
      // event.RunEventPlanReviewed, 0); err → false;
      // for i := len(records)-1; i >= 0; i-- {
      //     unmarshal payload (fail → continue);
      //     outcome != planReviewOutcomeDone → continue;
      //     line := first non-empty line of payload.Text (strings.Split "\n",
      //         TrimSpace each, keep the raw line trimmed); "" → continue;
      //     return PlanReviewVerdict{Provider/Model trimmed,
      //         ModelName: llm.ListingDisplayName(provider, model), Line: line}, true
      // }
      // return PlanReviewVerdict{}, false
  }
  ```

  （ModelName 用 `llm.ListingDisplayName`，与 `planReviewNotesFrom` :1482 一致。）
- `PendingForRun` 的 exit-plan 分支，`req.PlanReviews = planReviewNotesFrom(results)`
  块（:1952-1955）之后加：

  ```go
  if len(req.PlanReviews) == 0 {
      if verdict, ok := LatestPlanReviewVerdict(ctx, g.Runs, sessionID); ok {
          req.LatestReviewVerdict = &verdict
      }
  }
  ```

**Verify（绿）**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'PlanReviewVerdict|LatestReviewVerdict|NewExitPlanAction' -count=1`
→ ok，Step 1 与 2a 全部用例 pass。再跑 `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → ok（无回归）。

### Step 3: TUI overlay 渲染结论行

`pkg/tui/overlays.go` `buildApprovalContent`：在 `req.PlanReviews` 循环
（:597-613）之后、picker 问句 switch（:619）之前插入：

```go
if v := o.req.LatestReviewVerdict; v != nil {
    writeLine("")
    writeLine("\x1b[1mReview by %s\x1b[0m", planReviewModelDisplay(v.Provider, v.Model))
    bodyWidth := width - 4
    if bodyWidth < 20 {
        bodyWidth = 20
    }
    md := renderAssistantMarkdownWithWidth(strings.TrimSpace(v.Line), bodyWidth, DiffThemeUnknown)
    if md == "" {
        md = strings.TrimSpace(v.Line)
    }
    for _, l := range strings.Split(md, "\n") {
        writeLine("%s", l)
    }
}
```

（引擎保证该字段只在 `PlanReviews` 为空时非 nil——见字段注释——这里不再
重复判空，不写不可能态的防御分支。）
问句条件（:632）改为：

```go
case planContent != "" || len(o.req.PlanReviews) > 0 || o.req.LatestReviewVerdict != nil:
```

不新增分隔线、不新增键位、不动 `feedbackPlanDeny` 提示（:742-744，其条件
仍是 action-scoped `PlanReviews`，语义不变）。

测试：`pkg/tui/chat_session_test.go` 追加
`TestExitPlanOverlayRendersLatestReviewVerdict`，照 :11176 形状：
`req := exitPlanReviewRequest(t, "# Plan\n\nStep one.\n")`；
`req.LatestReviewVerdict = &turn.PlanReviewVerdict{Provider: "openai", Model: "gpt-5.1", Line: "**Verdict: approve with changes** — one test needs its expected output pinned."}`；
`buildApprovalContent(100)` + `stripANSI` 断言含
`"Review by openai/gpt-5.1"`、`"Verdict: approve with changes"`（加粗被渲染、
文字在）、`"one test needs its expected output pinned"`、
`"Would you like to proceed?"`。第二例：plan 文件内容为空字符串
（`exitPlanReviewRequest(t, "")`）＋ verdict 非 nil → 仍含
`"Would you like to proceed?"`（问句条件的新分支）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ExitPlanOverlay' -count=1`
→ ok（新旧用例全 pass）。

### Step 4: gateway wire

`pkg/gateway/api_extra.go`：
- `planReviewNoteWire`（:4678）附近加：

  ```go
  // planReviewVerdictWire is the latest completed review's conclusion line,
  // shown on a re-submitted approval; absent when there is none.
  type planReviewVerdictWire struct {
      Provider  string `json:"provider"`
      Model     string `json:"model"`
      ModelName string `json:"model_name,omitempty"`
      Line      string `json:"line"`
  }
  ```

- `sessionApprovalRequestWire`（:4634-4644）加字段
  `LatestReviewVerdict *planReviewVerdictWire \`json:"latest_review_verdict,omitempty"\``。
- `handleSessionApprovalRequest` 的投影（:4604-4608 附近）加：

  ```go
  if v := req.LatestReviewVerdict; v != nil {
      out.LatestReviewVerdict = &planReviewVerdictWire{
          Provider: v.Provider, Model: v.Model, ModelName: v.ModelName, Line: v.Line,
      }
  }
  ```

测试：`pkg/gateway/api_extra_test.go` 追加
`TestSessionApprovalRequestCarriesLatestReviewVerdict`，照 :5389
`TestSessionApprovalRequestCarriesTheExitPlanCard` 形状（`exitPlanServer`）：
评审事件的 `ActionID` 写成**另一个** action id（如 `"act-old"`，非当前 pending
action），outcome done、text 首行 `"Verdict: rework — the mechanism claim is falsified."`
＋后续正文行；GET approval-request → 解码断言
`latest_review_verdict.line` 恰为首行、`model` 正确、`plan_reviews` 为空。
第二例：无评审事件 → 响应无 `latest_review_verdict` 键（omitempty）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'SessionApprovalRequest' -count=1`
→ ok。

### Step 5: web dock

- `frontend/src/lib/api.ts`：`PlanReviewNote`（:1734）附近加

  ```ts
  /** The latest completed review's conclusion line, shown on a re-submitted
   *  exit-plan approval (plan 382). Absent when there is none. */
  export interface PlanReviewVerdict {
    provider?: string
    model: string
    modelName?: string
    line: string
  }
  ```

  `SessionApprovalRequest`（:1758）加 `latestReviewVerdict?: PlanReviewVerdict | null`
  （`toCamelCase` 自 `./case` 导入、在 :3913 调用，递归完成蛇形→驼峰，无需手写映射）。
- `frontend/src/locales/index.ts`：zh 区 `chat.exitPlanReviewStop`（:2286 附近）
  后加 `'chat.exitPlanReviewVerdict': '{model} 的评审结论',`；en 区对应位置
  （:4879 附近）加 `'chat.exitPlanReviewVerdict': 'Review by {model}',`。
- `PlanApprovalDock.vue`：
  - script：照 `reviewActive`（:328）加

    ```ts
    const latestVerdict = computed(() => request.value?.latestReviewVerdict ?? null)
    const verdictModelLabel = computed(() => {
      const v = latestVerdict.value
      return v ? [v.provider, v.modelName || v.model].filter(Boolean).join('/') : ''
    })
    ```

    （拼法照 `activeReviewLabel`，usePendingActions.ts:143-146。）
  - template：`xp-doc` 块（:32-34）与 `<div class="gate-ask">`（:35）之间加

    ```html
    <!-- The latest completed review's conclusion line: the planner already
         absorbed it and resubmitted; the user decides with the verdict in
         view (plan 382). The full review stays in the reviewer's card. -->
    <div v-if="latestVerdict" class="xp-verdict" data-testid="exit-plan-review-verdict">
      <span class="cap">{{ t('chat.exitPlanReviewVerdict', { model: verdictModelLabel }) }}</span>
      <MessageResponse :content="latestVerdict.line" />
    </div>
    ```

    `MessageResponse` 已在组件内使用（:33）。样式：`.xp-verdict` 只做间距
    （与 `xp-doc`/`gate-ask` 的既有 padding 一致），标签复用既有 `.cap`
    排版，正文走 MessageResponse 的 markdown——不新增颜色、不加边框。
    该块在 gate-ask 的状态开关之外，picking/review-running 态也保持可见
    （与 TUI 一致：结论行在 picker 问句之前渲染）。
- `PlanApprovalDock.test.ts`：照 :75 用例形状加两例：
  request 带 `latestReviewVerdict`（line 含 `**Verdict: approve with changes** — …`）
  → `exit-plan-review-verdict` testid 存在、含模型标签与结论文字；
  不带 → testid 不存在。mock 形状照该文件既有 request fixture。

**Verify**: `cd frontend && pnpm typecheck && pnpm test -- PlanApprovalDock`
→ typecheck exit 0；测试全 pass（含 2 个新用例）。

### Step 6: 真机验收（run-forebrain driver，新 plan-review 场景）

先读 `.claude/skills/run-forebrain/SKILL.md`。fake_provider.py 现有
`TOOL_SCRIPTS`（:203）是顺序脚本，无法表达「reviewer 请求答结论、planner
下一轮再交 exit_plan_mode」的条件分支——照 `serve_subagent_ask`（:1004）
的条件应答先例新增 `plan-review` 模式：

- 识别标记：reviewer 请求的系统提示词含
  `"You are reviewing a plan another agent wrote"`
  （`planReviewerOpening`，pkg/agent/subagent_defs.go:114；备用标记：任务
  首句 `"Review the plan below"`，BuildPrompt approval.go:1083）。
  **注意 `_is_turn_request`（:444）只按工具表判 turn，reviewer 也带工具表，
  必须先排除 reviewer 再计 turn**（用独立 turn 计数器，照
  `MULTISHELL_TURNS` :243 的先例；memory/compaction 背景请求沿用既有应答）。
- 应答脚本：planner turn #1 → `enter_plan_mode` 工具调用（照 `toolcall`
  场景 :204-207 的先例，进 plan 模式）；planner turn #2 → `exit_plan_mode`
  （第一屏计划审批）；reviewer 请求 → 纯文本（无工具调用）
  `"Verdict: approve with changes — the step order is sound; one verification command needs its expected output pinned.\n\n## Problems\n1. ..."`；
  planner turn #3（交付后的 resume，仍是带工具表的 turn 请求）→ 再次
  `exit_plan_mode`（第二屏＝再次审批）；turn #4+ → `"done"` 文本。
- driver.sh：`cmd_start` 注释（:122）、usage（:329）、模式说明（:345 附近）
  三处字符串加入 `plan-review`。

**计划文件播种（必须，否则评审必 failed）**：`turn.RunPlanReview` 派发
reviewer 前先读会话计划文件（`approval.go:1757-1765` →
`state.GetPlanForSession`），空计划即 `ErrNoPlan` 返回、不交付、无第二屏。
driver 没有任何计划播种机制，脚本里的 `exit_plan_mode` 也不会写计划文件
（真实流程里计划是 planner 用 write_file 写的）。因此在**第一屏计划审批
出现后、选评审之前**，向隔离 home 的会话计划目录写一份非空计划：

```bash
# 先导出验收环境变量（与下方「验收运行」块同一组值）：
export FOREBRAIN_RUN_WORK="$TMPDIR/forebrain-run-382" FOREBRAIN_RUN_SESSION=fb-382 FOREBRAIN_RUN_PORT=8793
# 布局依据：主代理 stateRoot=<home>/workspace（chat_surface.go:598-607 注释），
# 计划目录=<workspaceRoot>/plans/<projectKey>/<session>（pkg/state/plan.go:1-52），
# projectKey=项目根规范化后 / \ : → -（state.ProjectKeyForRoot/denseProjectKey，
# project_store.go:270-293；macOS 的 /tmp  symlink 用 pwd -P 解析）。
SID=$(sqlite3 "file:$FOREBRAIN_RUN_WORK/home/state/forebrain.state.sqlite?mode=ro" \
  "SELECT id FROM fb_sessions ORDER BY rowid DESC LIMIT 1")
KEY=$(cd "$FOREBRAIN_RUN_WORK/proj" && pwd -P | sed 's|/|-|g')
mkdir -p "$FOREBRAIN_RUN_WORK/home/workspace/plans/$KEY/$SID"
printf '# Plan\n\n1. Ship the change.\n' > "$FOREBRAIN_RUN_WORK/home/workspace/plans/$KEY/$SID/plan.md"
```

播种正确的当场证据：动一下光标让 overlay 重渲染后抓屏，第一屏含
`Here is the plan:` 与 `Ship the change`（overlay 每次渲染重读计划文件）。
看不到即播种路径与 gate 解析不一致——STOP 排查，不要继续。

验收运行（隔离 work/session/port，避开他会话占用的 8763/8791）：

```bash
export FOREBRAIN_RUN_WORK="$TMPDIR/forebrain-run-382" FOREBRAIN_RUN_SESSION=fb-382 FOREBRAIN_RUN_PORT=8793
D=.claude/skills/run-forebrain/driver.sh
$D start plan-review          # 按 skill 的启动/等待配方
# turn#1 的 enter_plan_mode 审批：按 skill 的 toolcall 配方批准（Yes）
# 第一屏 exit_plan_mode 审批出现：抓屏断言含 "Tool: exit_plan_mode"，不含 "Review by"
$D screen
# → 执行上面的「计划文件播种」→ 动光标重渲染 → 抓屏断言含 "Ship the change"
# 按 4 选「Ask another model to review this plan」→ 模型 picker → Enter
# （若出现思考强度第二屏，Enter 取默认档）→ 评审运行 → 交付 → planner 重交
# 轮询抓屏直到第二屏审批出现：
$D screen                     # 断言含 "Review by" 与 "Verdict: approve with changes"
$D stop
```

抓屏输出留存为证据（贴进执行报告）。若 driver 机制本身失败（端口、tmux、
播种），按 skill 的 troubleshooting 处理并在报告注明——不要把基建失败归因
于本改动。

**Verify**: 第一屏无 "Review by"；第二屏含 `Review by <provider/model>` 与
`Verdict: approve with changes`（结论行原样、加粗渲染）；`$D stop` 干净退出。

### Step 7: 广域门禁＋索引

```bash
gofmt -l cmd pkg third_party plugins        # 无输出
CGO_ENABLED=1 go vet -tags fts5 ./...       # exit 0
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 30m
cd frontend && pnpm typecheck && pnpm test
```

全量若有失败：先诊断是否预存/他人改动（`git status`、peer_send），不属于
本改动且无法归因时报告，不顺手修。最后更新 `plans/README.md` 本计划行
（定点插入/编辑，不重排他行；另一代理在并行追加 383 行，edit 前重读现状）。

**Verify**: 四条命令全绿；README 行状态 DONE（或按派发者指示留 IN PROGRESS）。

## Test plan

- 新测试（全部随 Step 落地）：
  - `pkg/turn/approval_test.go`：`TestNewExitPlanActionSeesNoPriorReview`
    （机制复现）、`TestLatestPlanReviewVerdictTakesTheNewestDone`（helper：
    最新 done 胜出、failed/stopped 回退、空文本/坏 payload 跳过、首行原样
    含中文/加粗、空态）、`TestPendingExitPlanRequestCarriesLatestReviewVerdict`
    （跨 action 携带、own-action 抑制、无评审 nil）。
  - `pkg/tui/chat_session_test.go`：`TestExitPlanOverlayRendersLatestReviewVerdict`
    （渲染＋空计划问句分支）。
  - `pkg/gateway/api_extra_test.go`：`TestSessionApprovalRequestCarriesLatestReviewVerdict`
    （wire 首行/模型、omitempty）。
  - `frontend/src/components/chat/PlanApprovalDock.test.ts`：结论行有/无两例。
- 结构范本：approval_test.go:738 / chat_session_test.go:11176 /
  api_extra_test.go:5389 / PlanApprovalDock.test.ts:75。
- 回归：`./pkg/turn`、`./pkg/tui -run ExitPlan`、`./pkg/gateway -run SessionApproval`
  全量绿；Step 7 全量套件。
- 失败路径：评审 failed/stopped/坏 payload 不产生结论行；事件读失败面板照旧
  （helper 返回 false，不新增错误面）。

## Done criteria

ALL must hold:

- [ ] Step 1 复现测试在**未改生产代码前** pass（机制证据在报告里）
- [ ] Step 2 红态证据（unknown field 编译错误）在报告里，随后绿
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway -count=1` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ExitPlan' -count=1` exit 0
- [ ] `cd frontend && pnpm typecheck && pnpm test` exit 0
- [ ] `gofmt -l cmd pkg third_party plugins` 无输出；`go vet -tags fts5 ./...` exit 0
- [ ] 全量 `./... -timeout 30m`（scratch home）exit 0，或唯一失败已归因为预存/他人改动并留证
- [ ] 真机：第二屏抓屏含 `Review by` 与 `Verdict: approve with changes`，第一屏不含
- [ ] `grep -n "LatestReviewVerdict" pkg/turn/approval.go` 命中 helper 与 PendingForRun 接线；
      `grep -n "latest_review_verdict" pkg/gateway/api_extra.go` 命中 wire tag
- [ ] `git status` 对照 Scope：无 in-scope 之外的源码改动（plans/README.md 除外）
- [ ] `plans/README.md` 状态行已更新

## STOP conditions

Stop and report back (do not improvise) if:

- drift check 发现**被引用区域**内容与 Current state 摘录不符（行号漂移不算）。
- Step 1 复现测试 fail——根因链与计划不符，先报告证据。
- 任一 Step 的 verify 合理修复后仍两次失败。
- 需要触碰 out-of-scope 文件（尤其 reducer.go/render.go/questionOverlay、
  PlanReviewsForAction 语义、transcript 卡规则）才能完成。
- 「评审按 action 键控、交付即关 action」这一前提被源码证伪。
- 真机验收中 driver 基建失败且按 skill troubleshooting 无法恢复——报告卡点
  （命令、错误），不要改 driver 之外的东西去绕。
- 发现并行代理正在改你需要的文件区域——peer_send 协调，串行或让行。

## Maintenance notes

-  reviewer 提示词（subagent_defs.go planReviewerMethod）若改首行格式契约，
  结论行显示随之变化——取首行原样的设计不需要跟着改解析，但要重看样本。
-  恢复窗口（评审完成未交付即重启）里 web 仍不渲染 action-scoped 全文评审
  （预存缺口，本计划未补）；TUI 该路径行为不变。
-  明确推迟（owner 未选）：verdict 枚举解析/着色、面板内展开全文、hand-off
  执行评审结论显示、web 恢复窗口全文渲染。
-  评审方重点 scrutinize：`PendingForRun` 新分支只在 `len(req.PlanReviews)==0`
  时填字段（own-action 抑制）；deny 反馈提示与 ComposeDenyFeedback 未被波及；
  TUI 问句条件新增分支；wire omitempty；dock 结论行在 picking/review-running
  态仍可见。
-  非功能：每次 exit-plan gate **派生**（TUI 每轮 prompt 循环、web 每次 poll，
  非每帧）多一次按 session+type 索引的事件读
  （`idx_fb_session_events_session_type`；`ListSessionEventsOfType` 上限 500
  条，pkg/state/session_events.go:384-385），与既有 PlanReviewsForAction 同量级；
  无新存储、无新等待/锁；读失败降级为不显示，面板其余部分不受影响。