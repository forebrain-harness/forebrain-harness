# Plan: 阻塞派发的 plan review / goal check 不再重复 peer 送达报告

> **Executor instructions**: 按步骤执行；每步跑验证命令并确认预期结果后再继续。
> 命中 STOP conditions 立即停下报告，不得即兴发挥。**全程不 git commit**（owner 常设规则）；
> 改动留工作区由 owner 审阅。实施前用 peer_send 知会同树代理（见 Step 0）。
>
> **Drift check（先跑）**: 基线 commit `c0d4ee16`（2026-11-21）。
> `git status --porcelain -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/run/goal_test.go pkg/process/peer_inbound_test.go`
> 加 `git diff c0d4ee16 -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/run/goal_test.go pkg/process/peer_inbound_test.go`
> （基线对照**工作树**；`c0d4ee16..HEAD` 在 baseline==HEAD 时恒空、看不见未提交漂移，是已知假绿陷阱）。
> 再把下面 "Current state" 摘录与现码逐字对照；**内容不符 = STOP**（仅行号漂移不算）。
> HEAD 前移（其他代理落 commit，如 d6 的 skill 合并）**不算漂移**：判据是内容比对与
> `git diff c0d4ee16 -- <in-scope paths>` 为空（二轮评审已核该 diff 为空、锚点行号仍成立）。

## Status

- **Priority**: P1（每次 exit-plan 评审 / goal check 都向主代理重复注入整份报告，纯浪费 token，且重复 steer 会打断主代理正在进行的工具调用——现场已发生 user_interaction 被打断）
- **Effort**: S（产品代码 2 行；其余为测试与验收）
- **Risk**: MED（awaited 标记同时改变 continuation 归属，见行为矩阵；消费面已穷尽）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `c0d4ee16`, 2026-11-21

## Why this matters

exit_plan_mode 审批门派出的 plan-reviewer 是**阻塞**派发：评审全文随 gate 的 tool result
回到主代理。但执行结束时引擎又把同一份报告作为 peer message（`<agent-message …>Your
subagent … finished successfully. Its final report: …`）送进主会话——主代理上下文里同一份
报告出现两次，浪费 token，且迟到的 steer 会打断主代理下一个工具调用。goal check
（/goal 的 goal-evaluator）同缺陷。根因是这两条阻塞派发路径漏了 `withSubagentAwaited`
标记，而 subagent_run / subagent_continue 的阻塞路径都有。修复 = 把漏标记的两处归一到
既有语义，不新造机制。

## Current state（根因证据链，file:line + 摘录）

### 现场证据（owner 真实状态库 ~/.forebrain/state/forebrain.state.sqlite，只读取证已完成）

会话 `cli-6fc1936e-c46a-4b21-ab61-a9ea8bded004`（截图会话）：
- `fb_messages` id 550266, role=tool, created_at 1795255582：`The plan review you asked for has returned. The user has not decided yet…`（len 3548，含评审全文）= 第一次送达（gate tool result）。
- id 550271, role=user, created_at 1795255597：`<agent-message from="subagent-64893a36-501c-4773-8443-f5d687a09fd9">Your subagent subagent-64893a36-… finished successfully. … Its final report: …`（len 2897）= 重复送达，文案与 `subagentReportEnvelope` 逐字同源。
- id 550270 reasoning：主代理自述 "The subagent message is a duplicate of the review I already received"，且其 user_interaction 调用被该消息打断。
- 截图卡片 "Ran 1 plan-reviewer task" = TUI fanout 卡 verb `review`（pkg/tui/reducer.go `fanoutCardHeader` 的 `case "review"`）。

### 因果链

1. `pkg/process/run_executor.go:427` `PlanReviewer.Review` 阻塞调用
   `r.Runner.RunPlanReviewSubagent(runCtx, task, override, r.Approve)`（:455）；
   再评审走 `ContinuePlanReviewSubagent`（:453）。
2. `pkg/run/subagent.go:5387` `RunPlanReviewSubagent` → :5421
   `r.runPlanReviewAcrossApprovals(subagentExecContext(prep), fac, prep, approve)` →
   **:5567 `runSubagentExecution(ctx, fac, prep.entry, …)` 无 `withSubagentAwaited`**：
   ```go
   5564  func (r *Runner) runPlanReviewAcrossApprovals(
   5565      ctx context.Context, fac Factory, prep preparedSubagent, approve PlanReviewApprovalFunc,
   5566  ) (string, error) {
   5567      return runSubagentExecution(ctx, fac, prep.entry, func(execCtx context.Context) (string, error) {
   ```
3. `pkg/run/subagent.go:1283` `awaited := subagentAwaitedFromContext(ctx)` → false；
   :1366 `reportSubagentExecutionEnded(fac, record, err, awaited, ch.hasLiveWait(), out)`
   （无 subagent_wait 阻塞 → hasLiveWait=false）。
4. `pkg/process/one_shot.go:64` `SubagentExecutionEnded` → :86 `deliverSubagentReport`：
   ```go
   89   switch {
   90   case end.AwaitedByDispatcher, sessionID == "":
   91       return
   92   case end.DispatcherWaiting:
   ...
   97   case errors.Is(end.Err, context.Canceled):
   99   case subagentEndWaitsForItsContinuation(end.Err):
   ```
   两门都不拦 → :118 `deliverPrimary` 送 peer envelope（:155 `subagentReportEnvelope`，
   :160 `Your subagent %s finished successfully.`）。运行中的主代理按 steer 收（下一个工具
   边界取走，与现场时序一致：tool result 后 15s 出现）。
5. 同一报告已由阻塞调用返回 → gate tool result。重复成立。

### 既有语义（修复归一到的目标）

- `pkg/run/subagent.go:4267-4280`：
  ```go
  func withSubagentAwaited(ctx context.Context) context.Context {
      return context.WithValue(ctx, subagentAwaitedKey{}, true)
  }
  ```
- 阻塞路径范例 `pkg/run/subagent.go:2282`（execGeneralSubagent，subagent_run/fanout）：
  `return runSubagentExecution(withSubagentAwaited(execCtx), fac, prep.entry, …`
- `SubagentExecutionEnd.AwaitedByDispatcher` 注释（subagent.go:5309-5313）：阻塞调用等待该
  execution；dispatcher 接收失败并拥有 continuation。
- 消费面穷尽（grep 验证）：`pkg/process/one_shot.go:90`（送达腿）与
  `pkg/turn/submit.go:691`（continuation 腿）；两 surface 仅转发该 flag
  （`pkg/tui/notify.go:1827`、`pkg/gateway/serve_run.go:259` → `turn.Service.SubagentExecutionEnded`）。
  grep 结论：`pkg/tui`、`pkg/gateway` 的 `*_test.go` 中 `AwaitedByDispatcher` 断言 0 条
  （本会话与外部评审各 grep 一次），"仅转发、无行为差"成立，执行者不必怀疑漏改 surface。

### runSubagentExecution 全量调用点审计（同缺陷排查）

| 行 | 路径 | awaited | 判定 |
|---|---|---|---|
| 2282 | execGeneralSubagent（subagent_run/fanout 阻塞） | ✔ | 正确 |
| 2429 | spawnAsyncSubagent（subagent_send 异步） | ✘ | **故意**：需要 peer 送达 |
| 3116 | startSubagentContinuation（子代理自身 continuation） | ✘ | **故意**：结束需 peer 送达 |
| 3491 | continueSubagentRecord（subagent_continue；再评审 5471 经此） | ✔ | 正确 |
| **5545** | **runGoalCheck（goal-evaluator，阻塞；pkg/run/goal.go:115 Evaluate 调用）** | ✘ | **同缺陷，修** |
| **5567** | **runPlanReviewAcrossApprovals（新评审，阻塞）** | ✘ | **本 bug，修** |

5545 现状：
```go
5543      run: func(runCtx context.Context, fac Factory, prep preparedSubagent) (string, error) {
5544          approve := tool.SubagentApprovalHookFromContext(runCtx, factoryToolsState(fac))
5545          return runSubagentExecution(subagentExecContext(prep), fac, prep.entry, func(execCtx context.Context) (string, error) {
```

### continuation 腿现状

`pkg/turn/submit.go:687-706` `autoContinuer.subagentExecutionEnded`：
```go
691   if end.AwaitedByDispatcher {
695       a.closeRun(strings.TrimSpace(end.WorkerSessionID))
696       return
697   }
698   plan, ok := a.continuationFor(...)   // 用量限+reset → arm 自身 continuation
```

### 约定与范例

- 测试装配范例：`pkg/run/subagent_test.go:407-427`
  （`TestRunPlanReviewSubagentRunsWithSubagentsDisabled`：裸 `&Runner{Deps: (&Deps{Home: t.TempDir()}).WithConfig(cfg), Events: probe, SubagentExecutor: &fixedSubagentExecutor{output: "verdict"}}` + `dispatchTestCtx()`=:87 `llm.WithAgentSessionID(context.Background(), "session-1")`）。
  cfg 行映射范例 `modelRowsForTest`（pkg/run/config_test.go:2789）：`appcfg.LLMModelConfig{ID: "llm-1", Provider, Model, BaseURL, APIKey}` + `Agents.Definitions["main"].DefaultModel="llm-1"`。
- `fixedSubagentExecutor` 定义 pkg/run/subagent_test.go:2147（RunSubagentExec 返回 output；其余 noop）。
- process 层 peer 装配范例：`pkg/process/peer_inbound_test.go:190` `startPeerTestService(t, "tui", session, view, nil)`；
  :1364 `TestSubagentEndWakesTheIdleDispatcherWithItsReport`（view.wakeCount/oneWake）；
  :1431 `TestSubagentEndLeavesAwaitedCancelledAndUsageLimitAlone`（awaited⇒不送达，既有 pin）。
- `dispatchSubagent` 同步执行 `d.run`（subagent.go:4489）→ 上述裸 Runner 测试**真走**
  `runSubagentExecution` 与 `reportSubagentExecutionEnded`。
- `SubagentExecutor` 接口四方法：subagent.go:5266-5284。
- 真机 harness：`.claude/skills/run-forebrain/driver.sh`（`$D`），fake provider 模式
  `toolcall`（enter_plan_mode→exit_plan_mode 脚本，fake_provider.py:204）、`goal`
  （按 system prompt 识别 evaluator 请求，:255/:1020；driver.sh 用法注释未列 goal 但实际可用，
  cmd_serve 不校验模式）、`subagent-limit`
  （`FAKE_SUBAGENT_DISPATCH=send` 走 send+自身 continuation）。
- exit-plan overlay 评审触发键 **`m`/`M`**（pkg/tui/overlays.go:1126-1129 `enterPlanReviewMode`；
  帮助文本 :948-950 明写 m / M）→ reviewer picker；行上 Enter 在该行无 thinking levels 时
  直接派发（:1649-1663；fake seed 行无 efforts，不进第二屏）；决策出口 :1537 `RequestPlanReview`。
  注意 :1120 的 'r' 是 RequestedPermissions 批准键、:3702 的 'r' 是 heldPeerOverlay release 键，
  都**不是**评审键（冷读批判 B1 实证）。
- plan 文件布局：`<FOREBRAIN_HOME>/workspace/plans/<projectKey>/<sessionID>/plan.md`
  （pkg/state/plan.go:76-82、:203-221）；projectKey = canonical root 的 '/'→'-' 编码
  （ProjectKeyForRoot pkg/state/project_store.go:270-280 → denseProjectKey :284-293；
  canonicalStatePath=Abs+Clean+EvalSymlinks ≡ python `os.path.realpath`，macOS /var→/private/var 同解析）。
  缺文件返回 ("",nil)；`ErrNoPlan` 由评审派发处抛（pkg/turn/approval.go:1727、
  pkg/process/run_executor.go:432）；plan 在**评审派发时**才读（approval.go:1721），
  gate 打开后预置亦可——**toolcall 模式必须预置非空 plan 文件**。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 环境 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | 直接 go 命令编译 pkg/pdf 必需 |
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 定点 | 见下表后「红绿门命令块」（含竖线的命令不得放 GFM 表格内，`\|` 转义会零匹配假绿） | 修前 FAIL 计数 4，修后 PASS 计数 4 |
| 包级 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -timeout 30m ./pkg/run ./pkg/process ./pkg/turn -count=1` | PASS（pkg/tui 全量套件默认 10m 包超时假红，一律 -timeout 30m） |
| 广回归 | 先走表后「磁盘与归因」块，再 `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -timeout 30m ./pkg/... ./cmd/forebrain -count=1` | PASS（失败归因见该块） |
| Lint | `gofmt -l cmd pkg third_party plugins`；`CGO_ENABLED=1 go vet -tags fts5 ./...` | 无输出 / exit 0 |
| 真机 | `$D build`（改码后必跑）；`$D start/submit/key/screen/messages/db/provider-log/stop` | 见 Steps |

真机隔离：`export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-dupfix FOREBRAIN_RUN_SESSION=fb-dupfix FOREBRAIN_RUN_PORT=8761`（其他会话可能占默认实例）。

红绿门命令块（裸竖线 + 全名；`-v` + 计数证明真跑了，防 `ok [no tests to run]` 假绿）：
```bash
CGO_ENABLED=1 go test -tags fts5 -v -count=1 ./pkg/run \
  -run 'TestPlanReviewEndIsAwaitedByDispatcher|TestGoalCheckEndIsAwaitedByDispatcher' > /tmp/dupfix-run.log 2>&1
CGO_ENABLED=1 go test -tags fts5 -v -count=1 ./pkg/process \
  -run 'TestPlanReviewBlockingDispatchDoesNotRedeliverItsReport|TestPlanReviewRunningDispatcherIsNotSteeredWithTheReport' > /tmp/dupfix-process.log 2>&1
grep -c '^--- FAIL' /tmp/dupfix-run.log /tmp/dupfix-process.log  # 修前预期 2 与 2
grep -c '^--- PASS' /tmp/dupfix-run.log /tmp/dupfix-process.log  # 修后预期 2 与 2
```

磁盘与归因（外部评审问题 1；计划时实测 /System/Volumes/Data 仅 1.0GiB 可用，二轮评审时
回升 3.6GiB 已过门——executor 以亲测为准）：
```bash
df -h /System/Volumes/Data
```
- <2GiB：加载并遵循 `macos-disk-reclaim` 技能（只读盘点 → 清 Docker 临时测试库 → VM 内
  fstrim → 清可再生缓存与历史日志 → df 前后对照），清完复查。不得自删不可再生数据。
- 清理后仍 <2GiB：广回归收窄为
  `./pkg/run ./pkg/process ./pkg/turn ./pkg/tui ./pkg/gateway ./pkg/architecture ./cmd/forebrain`，
  并在报告说明偏离；不得硬跑 `./pkg/...`。
- 归因规则：失败只落在其他代理声明拥有的文件（d6：pkg/skill、.claude/skills/improve；
  56：plans/ 及其本 turn 声明的交付物 bug 相关文件）→ 记录并 peer_send 上报，不算本计划
  失败、不代修；本计划自己的包（run/process/turn）必须全绿。

## Scope

**In scope**（只许改这些）:
- `pkg/run/subagent.go`（仅 :5545 与 :5567 两行 wrap）
- `pkg/run/subagent_test.go`（新增 1 个 flag 回归测试）
- `pkg/run/goal_test.go`（新增 1 个 flag 回归测试）
- `pkg/process/peer_inbound_test.go`（新增 1 个症状级回归测试 + 记录型 executor）
- `docs/plan/PLAN_REVIEW_DUPLICATE_PEER_REPORT_PLAN.md`（本计划落盘副本，Step 0 创建；禁止 commit）

**Out of scope**（看似相关但不碰）:
- `pkg/process/one_shot.go`、`pkg/turn/submit.go` —— 门与调度语义正确，不改。
- `pkg/run/subagent.go` 其余调用点（2282/2429/3116/3491）—— 审计已判定正确/故意。
- `pkg/tui/*`、`pkg/gateway/*` —— 仅转发 flag，无行为差。
- plans/345、plans/README、.claude/skills/improve、pkg/skill —— 其他代理（forebrain-harness-56 / d6）正在改。
- 不为不可达状态写防御代码；不新增 flag/字段/配置。

## Git workflow

不建分支、不 worktree、不 commit（owner 裁决：直接在当前源码实施，改动留工作区）。

## Steps

### Step 0: 漂移检查 + 知会 + 计划落盘

1. 跑 drift check；对照 Current state 摘录（5545/5567/1283/1366/one_shot:90/submit:691）。
2. `peer_list` 后向 forebrain-harness-d6、forebrain-harness-56 各发一条 peer_send：
   "本会话将修改 pkg/run/subagent.go（5545/5567 两行）与 pkg/run/pkg/process 的测试文件，
   不碰 plans/、pkg/skill、.claude/skills；如有冲突请告知。"
3. 把本计划内容写入 `docs/plan/PLAN_REVIEW_DUPLICATE_PEER_REPORT_PLAN.md`。

**Verify**: `git status --short` 仅显示该 docs/plan 新文件（其余干净或他人改动不动）。

### Step 1: 写回归测试（修前必须红）

四个测试，装配照 Current state 范例：

1. `pkg/process/peer_inbound_test.go` 追加（症状级，真 dispatch + 真送达表）：
   ```go
   type planReviewOnceExecutor struct {
       env    *peerTestEnv
       output string
   }
   func (e *planReviewOnceExecutor) RunSubagentExec(context.Context, run.SubagentExecRequest) (string, error) { return e.output, nil }
   func (e *planReviewOnceExecutor) PersistSubagentTurn(context.Context, run.SubagentTurn) {}
   func (e *planReviewOnceExecutor) SubagentExecutionStarting(context.Context, string) {}
   func (e *planReviewOnceExecutor) SubagentExecutionEnded(ctx context.Context, end run.SubagentExecutionEnd) { e.env.SubagentExecutionEnded(ctx, end) }

   func TestPlanReviewBlockingDispatchDoesNotRedeliverItsReport(t *testing.T) {
       view := &peerTestView{}
       svc := startPeerTestService(t, "tui", "cli-plan-review-once", view, nil)
       cfg := &appcfg.Root{
           LLMModels: []appcfg.LLMModelConfig{{ID: "llm-1", Provider: "openai", Model: "gpt-4o", APIKey: "key", BaseURL: "https://example.invalid"}},
           Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{"main": {DefaultModel: "llm-1"}}},
       }
       runner := &run.Runner{Deps: (&run.Deps{Home: t.TempDir()}).WithConfig(cfg), SubagentExecutor: &planReviewOnceExecutor{env: svc.env, output: "the review verdict"}}
       out, err := runner.RunPlanReviewSubagent(llm.WithAgentSessionID(context.Background(), "cli-plan-review-once"), "review the plan", run.SubagentModelOverride{Provider: "openai", Model: "gpt-4o"}, nil)
       if err != nil || out != "the review verdict" { t.Fatalf("blocking review must return its report: out=%q err=%v", out, err) }
       if n := view.wakeCount(); n != 0 { t.Fatalf("peer wakes = %d, want 0: the blocking call already delivered the report (%v)", n, view.texts()) }
   }
   ```
   （import 按该文件现有别名对齐：run/llm/appcfg 已存在则复用；`peerTestEnv` 内嵌
   `*Environment`，`svc.env.SubagentExecutionEnded` 用法同 :1368。）
2. `pkg/process/peer_inbound_test.go` 追加 **steer 腿独立测试**（二轮评审小项 1：与 wake 腿
   同测试时第一个 Fatalf 会中止、steer 断言拿不到红门；拆开后两腿各自红→绿。评审探针已实测
   修前 `steers = 1`、envelope 与现场同形）：
   ```go
   func TestPlanReviewRunningDispatcherIsNotSteeredWithTheReport(t *testing.T) {
       view := &peerTestView{}
       svc := startPeerTestService(t, "tui", "cli-plan-review-steer", view, nil)
       // runner 构造同测试 1（cfg/executor 同形，executor env=svc.env，output="the review verdict"；
       // 可提 helper 复用，断言不变）。
       queue := svc.env.Control.SessionQueue("cli-plan-review-steer")
       queue.Attach(run.NewTurnInputRuntime())
       if !svc.env.Control.TrackRuntime("run-review", "cli-plan-review-steer", func() {}, queue.Runtime()) { t.Fatal("the test run was not tracked") }
       out, err := runner.RunPlanReviewSubagent(llm.WithAgentSessionID(context.Background(), "cli-plan-review-steer"), "review the plan", run.SubagentModelOverride{Provider: "openai", Model: "gpt-4o"}, nil)
       if err != nil || out != "the review verdict" { t.Fatalf("blocking review must return its report: out=%q err=%v", out, err) }
       if entries := queue.Runtime().Snapshot(); len(entries) != 0 { t.Fatalf("steers = %d, want 0: the report must not steer the running dispatcher", len(entries)) }
       if view.wakeCount() != 0 { t.Fatalf("wakes = %d, want 0", view.wakeCount()) }
   }
   ```
   装配仿 peer_inbound_test.go:1399-1410（running dispatcher 的 queue/TrackRuntime 形状）。
3. `pkg/run/subagent_test.go` 追加 `TestPlanReviewEndIsAwaitedByDispatcher`：
   记录型 executor 嵌入 `fixedSubagentExecutor` 并覆写 `SubagentExecutionEnded` 捕获 end；
   装配同 :407-420；调 `RunPlanReviewSubagent(dispatchTestCtx(), …)`；断言捕获到 1 个 end
   且 `end.AwaitedByDispatcher == true`。
4. `pkg/run/goal_test.go` 追加 `TestGoalCheckEndIsAwaitedByDispatcher`：同款记录 executor；
   装配照 goal_test.go:604 附近（`runner.SubagentExecutor = &fixedSubagentExecutor{output: …}`
   的既有形状）；经 `goalCheck{runner: runner}.Evaluate(ctx, "objective", nil)` 触发
   runGoalCheck；断言 end.AwaitedByDispatcher == true。

goal 症状腿的判据组合（回应外部评审问题 3，部分采纳并说明分歧）：goal 半边 =
flag 级测试（钉因）+ 既有 pin :1431（awaited⇒不送达）+ 真机场景 B（钉症状）。
不在 pkg/process 合成 goal end 做送达测试：runGoalCheck 无 exported 入口
（goalCheck.Evaluate 私有，pkg/run/goal.go:111，引擎 supervisor 调用），跨层不可直接派发；
合成 end 的送达断言与 :1431 同语义重复（_house rule：一语义一测试_）。plan review 半边
有 exported 入口，故得症状级测试（含 wake/steer 双腿）。

**Verify**: 红绿门命令块 → `--- FAIL` 计数 **2 + 2 = 4**，原因分别是
`peer wakes = 1` / `steers = 1` / `AwaitedByDispatcher = false` ×2。若不红 → STOP（因果链不成立）。
独立评审已实测（结论内联，**不依赖 /tmp 或 worktree 会话产物**）：一轮评审原样实跑
wake/flag 三测试修前红（2 FAIL flag + 1 FAIL wake，envelope 与现场逐字同形）、修后全 PASS，
`go test -tags fts5 -timeout 30m ./pkg/run ./pkg/process ./pkg/turn` ok（约 48s/241s/6s），
29 个既有相关 pin 无破坏；二轮评审补跑本轮新增 steer 腿探针修前 `steers = 1` 红、修后绿，
`grep -c "runSubagentExecution(withSubagentAwaited("` 修前 2 修后 4。executor 仍须在当前树
亲跑红绿门存档。

### Step 2: 真机红（修前二进制，复现症状）

```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-dupfix FOREBRAIN_RUN_SESSION=fb-dupfix FOREBRAIN_RUN_PORT=8761
D=.claude/skills/run-forebrain/driver.sh
$D build   # 当前=修前代码
```
场景 A（plan review，报告症状）：
1. `$D reset; $D start toolcall 'review verdict: approve with changes'`
2. `$D submit 'drive the exit gate'`；`$D screen` 读屏：enter_plan_mode overlay → Enter 批准。
3. 预置 plan（外部评审问题 2：不查 fb_projects——全新隔离 home 可能零行；driver.sh:11-16
   直接给出 WORK/home 与 WORK/proj）：
   `SID=$($D sid)`；`ROOT="$FOREBRAIN_RUN_WORK/proj"`；`HOME_DIR="$FOREBRAIN_RUN_WORK/home"`；
   `KEY=$(python3 -c "import os,sys;print(os.path.realpath(sys.argv[1]).replace('/','-'))" "$ROOT")`；
   `mkdir -p "$HOME_DIR/workspace/plans/$KEY/$SID" && printf '# plan\n\nstep one\n' > "$HOME_DIR/workspace/plans/$KEY/$SID/plan.md"`。
4. 主代理第二请求发 exit_plan_mode → exit overlay；`$D key m` → picker；Enter 选 fake 模型行
   （行无 efforts，一次 Enter 即派发，无第二屏）。
5. reviewer 请求被 fake provider 以纯文本回答（脚本越界=TEXT）→ 评审完成 → gate tool result
   回主代理 → 主代理下一请求纯文本收尾。`$D wait 'review verdict' 30` 等评审文本上屏，
   再轮询 `$D screen` 至 turn 结束（出现 `Worked for`、无 `esc to interrupt`），sleep 5。
   核对 `$D provider-log` REQUEST 顺序应为 1=enter、2=exit、3=reviewer、4=主收尾
   （toolcall 脚本计数全局、background 请求也耗槽位，防错位）。
6. 断言（红预期）：
   - `$D db "SELECT count(*) FROM fb_messages WHERE role='tool' AND content LIKE 'The plan review you asked for has returned%'"` ≥ 1
   - `$D db "SELECT count(*) FROM fb_messages WHERE role='user' AND content LIKE '%<agent-message from=\"subagent-%'"` **== 1**（重复存在）
   - `$D screen` 含 "Received a message from subagent"。
场景 B（goal check）：`$D reset; $D start goal '["done"]'`；`$D submit '/goal lengthen the scratch README'`；
`$D wait 'esc to interrupt' 30` 确认开跑，轮询 `$D screen` 至 turn 结束（`Worked for`），sleep 5；
同款 peer 行计数 **== 1**（红）。
取证保存（screen/db 输出贴入计划验收记录）。`$D stop`。

**Verify**: 两场景 peer 行 == 1。若场景 A 评审未派发（ErrNoPlan/picker 空）→ 按 STOP 报告，不得改产品代码迁就 harness。

### Step 3: 实施修复（2 行）

`pkg/run/subagent.go`：
- :5567 `runSubagentExecution(ctx, …` → `runSubagentExecution(withSubagentAwaited(ctx), …`
- :5545 `runSubagentExecution(subagentExecContext(prep), …` → `runSubagentExecution(withSubagentAwaited(subagentExecContext(prep)), …`
注释：如需，仅在两处之一补一行说明"阻塞派发：dispatcher 经调用本身收报告并拥有 continuation（同 execGeneralSubagent）"——保持周围注释风格，不新增概念。

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；Step 1 四条定点 → PASS。

### Step 4: 回归与 lint

Commands 表包级 → 磁盘与归因块 → 广回归 → gofmt/vet。既有断言若因契约修订失败：只允许更新
"usage-limited review/goal 自身 continuation" 相关断言（行为矩阵刻意修订），并注明理由；
他人拥有文件的失败按归因规则上报；其余失败 → 诊断，两次修不好 → STOP。

**Verify**: 全绿；`git status --short` 改动 ⊆ Scope。

### Step 5: 真机绿 + 回归场景

`$D build`（修后）。重跑场景 A/B：peer 行 **== 0**，tool result 行 ≥ 1（A），screen 无
"Received a message from subagent"（A）。
场景 C（send 路径不变，回归）：`$D reset; ENABLE_SUBAGENT=1 FAKE_SUBAGENT_DISPATCH=send $D start subagent-limit`；
`$D submit 'probe'`；等 FAKE_LIMIT_SECONDS（默认 20）过后 continuation 在子代理视图自跑；
sleep 5；peer 行 **≥ 1**（异步送达保留）。
清理：`$D stop`；`rm -rf "$FOREBRAIN_RUN_WORK"`（仅本验收自建目录）。

**Verify**: A/B == 0 且 C ≥ 1。

### Step 6: 收口

计划副本回填"已实施并真机验收通过（日期）"+ 验收记录章节（场景→证据，含工具链坑）；
会话计划文件同步状态。最终报告：根因链、2 行改动、测试/真机证据、行为矩阵刻意修订一项。

## Test plan

- 新增 4 测试（Step 1：wake 腿、steer 腿、review flag、goal flag），结构范例见 Current state；
  红→绿是验收核心，两腿各自有红门。
- 既有 pin 不动：peer_inbound_test.go:1431（awaited⇒不送达）、submit_test.go（awaited⇒closeRun）、
  subagent_test.go:407（review 可在 subagents disabled 下跑）。
- 广回归覆盖 flag 两消费面所在包（run/process/turn）与 surface（tui/gateway 在 ./pkg/... 内）。

## Done criteria

- [ ] `grep -c "runSubagentExecution(withSubagentAwaited(" pkg/run/subagent.go` == 4（文本匹配，不依赖行号；Step 3 补注释不影响）
- [ ] 4 新测试存在且 PASS；修前曾红（Step 1 输出存档，FAIL 计数 4）
- [ ] 包级 + 广回归 + vet + gofmt 全绿
- [ ] 真机 A/B peer 行 == 0、C ≥ 1（修前 A/B == 1 存档）
- [ ] `git status` 改动 ⊆ Scope；无 commit
- [ ] docs/plan 副本含验收记录

## STOP conditions

- Current state 摘录与现码内容不符（drift）。
- Step 1 测试修前不红，或红的原因不是指定断言（因果链被证伪）。
- 场景 A 预置 plan 后仍 ErrNoPlan / picker 无行 / reviewer 请求未被 fake provider 回答。
- 修复需要改 Out of scope 文件（尤其 one_shot.go / submit.go / 新 flag）。
- 任一验证两次合理修复后仍失败。
- 磁盘与归因块无法覆盖空间需求（先报告，不硬跑、不自删不可再生数据）。

## Maintenance notes

- 今后新增"阻塞派发"路径必须 wrap `withSubagentAwaited`；异步路径（send/自身 continuation）
  不得 wrap。调用点审计表（Current state）是 checklist；`runSubagentExecution` 是唯一入口，
  新路径绕过它即绕过该契约。
- reviewer 重点 scrutinize：行为矩阵用量限两行（review 改由主代理拥有 continuation；
  goal 纯去噪、failed 结局不变）——这是 awaited 语义的固有另一腿，与 subagent_run
  一致；若 owner 想要"评审撞限后台自续"，需另立计划新造语义，不在本修复内。
- 无延期项。

## 行为变化矩阵

**Owner 裁决（2026-11-21，user_interaction 两问均选推荐项）**：接受评审撞限不再后台自续、
接受 goal 撞限去除 evaluator 自续。下表两行刻意修订已获批准，executor 无需再问。

| 场景 | 旧 | 新 | 性质 |
|---|---|---|---|
| 新评审成功 | gate tool result + peer 重复 | 仅 gate tool result | 修复目标 |
| 新评审一般失败 | gate error result + peer "finished with an error" | 仅 gate error result | 修复目标 |
| 新评审撞用量限(带 reset) | gate error；arm 自身 continuation；continuation 结束再 peer 送迟到报告 | gate error（带 code/reset）；不 arm；主代理决策重试 | 刻意修订（归一 subagent_run 语义） |
| goal check 撞用量限(带 reset) | goal 标 failed（pkg/run/goal.go:216-225，引擎代码，无模型 dispatcher）；evaluator 自续、reset 后送迟到 peer verdict（goal 已死，纯噪声） | goal 照旧标 failed；不 arm 自续、无迟到 peer verdict | 刻意修订（纯去噪，失败结局不变） |
| 再评审（Again） | 已 awaited，无重复 | 不变 | 不变 |
| goal check 成功/失败 | goal loop 收 verdict + peer 重复 | 仅 goal loop | 修复目标 |
| subagent_send / 自身 continuation / subagent_wait | peer 送达 / latch 语义 | 不变 | 不变 |
| 用户取消评审 | context.Canceled 门跳过 | 不变 | 不变 |

## Implementation Status

- **状态：已实施并真机验收通过（2026-11-21）**。**Verdict: APPROVE**。
- Executor：deepseek/deepseek-flash（max），worktree `.worktrees/fix-dup-subagent-report`，
  branch `fix-dup-subagent-report`，commit `8e08d693`（parent `63f26ce8`）。
  合并回 dev 由 owner 下令执行：ff-only 落 dev `8e08d693`（2026-11-21，
  63f26ce8→8e08d693，仅 4 文件）；落地后主树定点测试绿；未 push；
  worktree/branch 已按 owner 指令清理（2026-11-21：git worktree remove +
  branch -d，was 8e08d693，全量在 dev）；56 的 345-* 与评审机制 plan-review-* 未碰。
- Done criteria 全部由 reviewer 亲跑复核：
  - `grep -c "runSubagentExecution(withSubagentAwaited(" pkg/run/subagent.go` = 4。
  - 4 新测试 `-v` 实跑 PASS；修前红存档 /tmp/dupfix-run.log + /tmp/dupfix-process.log
    `--- FAIL` 2+2，原因 peer wakes=1 / steers=1 / AwaitedByDispatcher=false ×2。
  - 广回归 `./pkg/... ./cmd/forebrain -count=1` exit 0、0 FAIL、36 ok；
    `go vet -tags fts5 ./...` OK；`gofmt -l cmd pkg third_party plugins` 无输出。
  - 真机（reviewer 独立隔离实例 fb-tl/8762）：A tool-result=1、peer 行=0、screen
    Received=0；B peer 行=0 且非空转（2 REQUEST、✓ Goal met 1 round、verdict done）；
    C（send+subagent-limit）peer 行=1。修前红 executor 存档：A peer=1 且 screen
    "Received a message from subagent-80f7e29e…"，B peer=1（wake 还触发多余 round 2）。
  - scope：worktree diff 63f26ce8..HEAD 仅 4 文件（subagent.go +9−2、subagent_test.go +48、
    goal_test.go +21、peer_inbound_test.go +75）；worktree status 干净；主树零 tracked
    改动（docs/plan/ 被 .gitignore:76 忽略，副本仅本地）。
- 已记录偏离（均判可接受，不触产品语义）：
  1. worktree 内 commit（handoff 契约；计划"不 commit"限主树）。
  2. 跳过 Step 0.2 peer_send 与 Step 0.3/Step 6 计划回填（reviewer 维护计划文件；
     peer 协调已由 reviewer 完成）。
  3. 环境接线：worktree symlink third_party/{libtokenizers,pdfium} 与
     pkg/gateway/dist/{assets,favicon.svg,index.html}（后者为新 worktree 过
     pkg/architecture web_assets 守卫所必需）。
  4. 真机场景 A 需 `$D reset` 后、`$D start` 前对 $WORK/proj `git init`
     （否则 project_type=office 不注册 enter_plan_mode，overlay 不出现）；
     未改产品/harness 文件。
- 遗留观察已调查结案：**非 bug**（2026-11-21 续查）。composer 显示的是 plan 344
  idle-composer next-step ghost（pkg/tui/reducer.go:4458-4461，38;5;245 灰_placeholder，
  "never part of the draft itself"）：turn 结束后 suggestion 引擎发一条背景请求
  （dump request-0005 末消息 `[SUGGESTION MODE: …]`），fake provider 对其回同一 TEXT，
  ghost 内容恰与 reviewer 文本相同；去 SGR 捕获看似 draft。证据：-e 捕获 composer 行
  SGR=38;5;245（draft 色 252）；fb_session_prompt_state 无 draft 行。真实 provider 下
  ghost 为预测的下一步输入（Tab/Right 取、Enter 发、打字dismiss），行为如设计，零代码改动。
- 无未完成项；无修订轮（executor 一次通过）。