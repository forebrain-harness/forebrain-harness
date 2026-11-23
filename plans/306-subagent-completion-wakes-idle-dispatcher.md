# Plan 306: fire-and-forget 子代理完成后自动回报并唤醒 idle 主代理（无人干预交付最终结果）

> **Executor instructions**：按步骤执行，每步跑完验证命令并对照预期再往下。遇到「STOP 条件」任一条就停下报告，不要自行发挥。完成后不要改 `plans/`（索引由派发方维护）。
>
> **Drift check（开工第一步）**：
> ```sh
> git rev-parse --short HEAD            # 基线 0a4be487（dev）；后代提交亦可，按内容核对
> grep -n 'func peerSendToDispatcher' -A 17 pkg/run/peer_tools.go
> grep -n 'func (env \*Environment) SubagentExecutionEnded' -A 6 pkg/process/one_shot.go
> grep -n 'type SubagentExecutionEnd struct' -A 20 pkg/run/subagent.go
> grep -n 'reportSubagentExecutionEnded(fac, record, err, awaited)' pkg/run/subagent.go
> grep -n 'env.setPeerService(svc)' -B 6 pkg/process/peers.go
> ls pkg/process/*.go | grep -v _test | wc -l    # 期望 33（2026-11-17 reconcile：c5cab032 删 planreview_images.go；守卫上限仍 34）
> ```
> 期望：`peerSendToDispatcher` 只 `SteerFromAgent`、注释明写 idle 不唤醒；`env.SubagentExecutionEnded` 只转发 `OnSubagentExecution.Ended`；`run.SubagentExecutionEnd` **没有** Output 字段；`reportSubagentExecutionEnded` 唯一调用点在 `runSubagentExecution` 尾部（:1276，基线时 1274）；`setPeerService` 在 `peer.Enabled()` 闸门**之后**；pkg/process 生产文件数 = 33（上限 34）。内容对不上即 STOP。
>
> **2026-11-17 reconcile 已在 `fcb241bc`（dev）实跑上述全部 grep 并逐段核对内容：语义全部成立**。唯一实质变化＝pkg/process 生产文件 34→33（owner bulk commit `c5cab032` 删除 `planreview_images.go`，守卫 `maxProductionFilesPerPackage = 34` 不变，cache_test.go:1532）——本计划「零新生产文件」的设计选择保留（逻辑仍进既有文件），但不再是硬约束。`deliverPrimary` 仍在 peer_inbound.go:754，`budgetSubagentReport` 仍在 subagent_report.go:83。

## Status

- **Priority**: P1（owner 2026-11-12 明确需求：「主代理先于子代理结束处于 idle 状态的情况下，子代理的工作结束后可以回报结果并唤醒主代理，促使主代理继续完成后续工作，在没有用户干预的情况下，交付最终结果」）
- **Effort**: M（run 侧一个字段 + 一处预算化；process 侧一个字段 + 一段投递逻辑；五组新测试）
- **Risk**: MED-HIGH（新行为会自动开启主回合＝烧 token；投递走所有 peer 消息共用的 deliverPrimary）
- **Depends on**: 无（与 `305` 零文件交集：305 只动 pkg/tui，本计划动 pkg/run + pkg/process，可并行）
- **Category**: feature（owner 需求）
- **Planned at**: commit `0a4be487`，2026-11-12

## Why this matters

owner 的工作流：主代理用 `subagent_send` 派发 fire-and-forget 子代理后先结束回合（idle），子代理随后完成——此时**没有任何路径**把结果送回主代理并让它继续：

1. **提示词没有指示**：子代理系统提示词只要求「finish with a concise factual report」（`pkg/agent/subagent_defs.go:35`），报告作为 Output 落盘，没有任何文字叫子代理把报告发给 parent。
2. **就算子代理主动 `peer_send` 给 parent，idle 主代理也不会醒**：进程内子代理→parent 走 `peerSendToDispatcher`（`pkg/run/peer_tools.go:346-362`），注释明写「A dispatcher with no run in flight is not woken here」；`SteerFromAgent` 在无回合时把消息留进 rejected 车道（`turn_input.go:849-857`），等**下一次用户开启的回合**才读到。
3. **完成事件不投递**：`finishSubagent` 只落历史 + 发 `subagent_ended`（TUI 画卡片），`SubagentExecutionEnded` 钩子链只做 usage-limit 自动续跑。

唯一能唤醒 idle 会话的投递表是 `deliverPrimary`（`pkg/process/peer_inbound.go:754-790`），但它今天只服务**跨进程** peer 消息。本计划把「fire-and-forget 子代理完成」接进这张表：完成即自动回报（预算化后的最终报告），主代理 idle 就被唤醒开一个真回合，读到报告后继续完成剩余工作、交付最终结果——全程无需用户干预。

**注意（勘误）**：跨进程 peer 消息能唤醒 idle 会话 ≠ 子代理能唤醒主代理。子代理与主代理同进程，走的是不唤醒的 `peerSendToDispatcher`。本计划之前没有任何子代理→idle 主代理的唤醒路径。

## Current state（执行者需要的事实，全部已逐行核实）

### 1. 钩子链（自动回报的落点）

```
run.runSubagentExecution 尾部（subagent.go:1274）
  → reportSubagentExecutionEnded(fac, record, err, awaited)   （subagent.go:1297-1310）
  → subagentExecutorOf(fac) = fac.Owner.SubagentExecutor       （subagent.go:1312-1317）
  = process.Environment                                        （pkg/process/open.go:829 `r.SubagentExecutor = env`）
  → env.SubagentExecutionEnded                                 （pkg/process/one_shot.go:55-60，只转发）
  → 表面钩子 OnSubagentExecution.Ended                          （TUI notify.go:1774-1789 / gateway serve_run.go:236-251）
  → turn.Service.SubagentExecutionEnded → autoContinuer        （pkg/turn/submit.go:509-514, 628-647，仅 usage-limit 续跑）
```

`env.SubagentExecutionEnded` 是 **TUI 与 gateway 共享**的一层，且与 `peerInbox`/`deliverPrimary` 同包（pkg/process）——自动回报放这里，两个表面同时受益，表面钩子一行不用改。

`one_shot.go:55-60` 现状：

```go
func (env *Environment) SubagentExecutionEnded(_ context.Context, end run.SubagentExecutionEnd) {
	if env == nil || env.OnSubagentExecution == nil || env.OnSubagentExecution.Ended == nil {
		return
	}
	env.OnSubagentExecution.Ended(end)
}
```

### 2. awaited 标记＝fire-and-forget 的判据

`runSubagentExecution`（`subagent.go:1189-1191`）开头 `awaited := subagentAwaitedFromContext(ctx)`。阻塞工具路径显式打标：`subagent_run`（`:2175`）、`subagent_continue`（`:3232`）用 `withSubagentAwaited(...)`（`:3982-3993`）。**唯一**的异步路径 `spawnAsyncSubagent`（`:2243-2294`，唯一生产调用点是 `subagent_send` 工具 `:3681`）不打标 → `awaited=false`。系统子代理（goal-evaluator / plan-reviewer / guardian 等）都走阻塞派发 → awaited=true，**不受本计划影响**。

`run.SubagentExecutionEnd`（`subagent.go:4800-4819`）现有字段：ConversationSessionID / WorkerSessionID / AgentKey / RunID / Err / AwaitedByDispatcher。**没有 Output**——要加。报告文本在 `runSubagentExecution` 里就是 `:1274` 处的 `out`（`finishSubagent` 在报告**之后**才跑，读台账会读到旧值，必须直接传 `out`）。

### 3. 投递表（复用，不改）

`pkg/process/peer_inbound.go:754-790` `deliverPrimary`：

```go
func (in *peerInbox) deliverPrimary(ctx context.Context, sessionID string, prompt PeerPrompt) string {
	…
	if runID, phase, ok := in.svc.env.Control.Find(sessionID); ok {
		switch phase {
		case run.Running:
			if in.svc.env.Control.Steer(runID, peerInput(prompt)) { … return "" }
		case run.Waiting, run.Finishing:
			if in.svc.env.Control.FollowUp(runID, peerInput(prompt)) { … return "" }
		}
	}
	if in.svc.env.deps().RunRT != nil {
		if err := in.svc.env.deps().RunRT.SessionBusy(ctx, sessionID); err != nil {
			return peerRefusedBusyElsewhere
		}
	}
	if in.surface.Wake == nil { return peerRefusedInboxNotAttached }
	if err := in.surface.Wake(ctx, prompt); err != nil { … }
	in.noteHanded(sessionID)
	return ""
}
```

Running→Steer（下一工具边界读到）、Waiting/Finishing→FollowUp、idle→Wake。`noteHanded`（`:806-812`）只是内存计数，不依赖注册。`PeerPrompt`（`peers.go:80-87`）：SessionID / Text / Origin / MsgID / FromName。

### 4. 两个表面的 Wake 都已实现（不用改）

- **TUI**：`notify.go:1753-1758` 注册 `Wake: s.wakeForPeer`；`wakeForPeer`（`:2281-2293`）→ `PeerWakeMsg` → `handlePeerWakeNotification`（`:2490-2511`，**非当前屏幕会话的消息会被丢弃并记日志**——既有限制）→ `setPeerDue` → idle 循环 `executePeerTurn`（`run.go:772-774`、`:4339-4375`，画 peer 卡片、开真回合，回合结束后 `nextAutomaticSubmission` 继续排空队列）。
- **gateway**：`serve_run.go:187-200` `Wake` → `startDetachedTurn`（Trigger "peer"、Unattended: true）。

### 5. inbox 的接线缺口

`StartPeers`（`peers.go:284-334`）：`svc.inbox = newPeerInbox(svc, s)` 在 **:304**（闸门之前）就建好；但 `env.setPeerService(svc)` 在 **:329**，位于 `if env == nil || !peer.Enabled() { return stop }`（:319-321）**之后**——peer 协议不可用的平台/未隔离测试里 `env.peers == nil`。自动回报是**进程内**投递，不该依赖跨进程注册是否启用，所以要把 inbox 单独无条件挂到 env 上（新字段，见 Step 2）。

### 6. 报告预算与信封

- `budgetSubagentReport(fac, taskID, report, budget)`（`pkg/run/subagent_report.go:83-92`）：预算内原样返回；超预算先整份 spill 到工具输出目录，再返回「行边界截断的头部 + 固定句子里的完整报告路径」。`subagentReportInlineChars = 50_000`（`:18`）。注释（`:56-60`）明写这是**唯一**截断点，「no caller grows a truncation of its own」——自动回报必须在 run 侧经它预算化后再传出。
- peer 信封形状（`pkg/run/peer_tools.go:404-409`）：`<agent-message from="名字">正文</agent-message>`。TUI 按 `Origin=state.MessageOriginPeer` 画 peer 卡片，`FromName` 上标题（`run.go:4360-4368`）。
- usage-limit 识别（`pkg/turn/submit.go:665-678`）：`llm.Explain(err)` → `explanation.Code ∈ {llm.ExplainRateLimitQuota, llm.ExplainRateLimitThrottle}` 且 `ResetAt` 非零。这类失败由子代理**自己的** auto-continue 定时续跑（`submit.go:628-647`），自动回报必须跳过，否则主代理会在子代理还没干完时被 premature 唤醒。
- 用户取消：`errors.Is(err, context.Canceled)`（`finishSubagent` 同判据，`subagent.go:4149`）。用户亲手停的子代理不该唤醒主代理。

### 7. 守卫与测试基建

- **pkg/process 生产文件 33/34**（2026-11-17 reconcile：`c5cab032` 删 `planreview_images.go`；守卫 `pkg/architecture/cache_test.go:1532` `maxProductionFilesPerPackage = 34`）：本计划仍**不新建任何生产文件**（设计选择，保持包收敛）。自动回报逻辑写进 `one_shot.go`（钩子所在）与 `peers.go`/`open.go`（接线）。文件名 ≤2 下划线、`X_test.go` beside `X.go`（`cache_test.go:1445/1497`）。
- 测试样板（都在 `pkg/process/peer_inbound_test.go`）：`startPeerTestService(t, "tui", sessionID, view, nil)`（`:187-217`，真注册、隔离目录）、`peerTestView`（记 wake）、`TestPeerInboundWakesAnIdleSession`（`:427-449`，断言 wake 的 SessionID/Text/Origin）、`TestPeerInboundSteersARunningSession`（`:451-481`，`svc.env.Control.SessionQueue` + `Attach(run.NewTurnInputRuntime())` + `TrackRuntime` 造 running 态）。新测试直接调 `svc.env.SubagentExecutionEnded(ctx, run.SubagentExecutionEnd{…})` 即可，不需要真跑子代理。
- run 侧样板：`pkg/run/subagent_test.go:871/1052/1224`（`spawnAsyncSubagent` 与异步钩子测试）、`factory_test.go:21` 与 `orchestration_llm_test.go:3519` 的 `fixedSubagentExecutor`（可捕获 `SubagentExecutionEnd`）。

## 设计决策（默认取值，owner 可裁决改）

1. **引擎驱动，不改提示词**：完成即自动投递最终 Output，不依赖模型自觉 `peer_send`（提示词驱动不可靠，且 idle 不唤醒的缺口仍在）。子代理提示词本就要求以事实报告收尾，Output 即报告。
2. **只对 fire-and-forget 生效**（`!end.AwaitedByDispatcher`）：阻塞调用的结果已由工具返回给主代理，再投递是重复；系统子代理全是阻塞派发，天然排除。
3. **跳过三类**：awaited；用户取消（context.Canceled）；带已知重置时间的 usage-limit（子代理自己的 auto-continue 负责）。**其余失败要回报**（主代理需要知道失败才能换路交付）。
4. **复用 deliverPrimary，不改其语义**：主代理还在跑→Steer（与任何 peer 消息一致）；idle→Wake。一种投递语义，不另造。
5. **本期常开，无配置开关**：owner 需求即默认行为；peer 协调本身也是 owner 裁决的常开（peers.go:279-280 注释）。若日后要关，再加配置（见 Maintenance notes）。
6. **报告在 run 侧预算化**（50_000 字符，超长 spill 带路径），process 侧只包信封。

## Scope

**In scope**（只许改这些）：
- `pkg/run/subagent.go`（`SubagentExecutionEnd` 加 `Output`；`reportSubagentExecutionEnded` 带上预算化报告；调用点传 `out`）
- `pkg/process/open.go`（Environment 新字段）
- `pkg/process/peers.go`（StartPeers 无条件挂 inbox）
- `pkg/process/one_shot.go`（自动回报逻辑 + 信封构造）
- `pkg/process/peer_inbound_test.go`、`pkg/run/subagent_test.go`（新测试）

**Out of scope**（看着相关也不许动）：
- `pkg/agent` 提示词、`deliverPrimary` 本体、`peerSendToDispatcher`（运行中子代理主动 peer_send 的语义不变）、TUI/gateway 表面代码（Wake 已能用）、阻塞工具（subagent_run/fanout/continue）、配置 schema、`pkg/tui` 任何文件（那是 305 的范围）。
- **不新建任何生产文件**（pkg/process 33/34，本计划保持零新文件的设计选择）。

## Commands you will need

本机 `go` 不在默认 PATH，先 `export PATH=/usr/local/go/bin:$PATH`。

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run '<Filter>' -count=1` | ok |
| 包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process ./pkg/turn ./pkg/tui -count=1` | ok |
| 竞态 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run -race -count=1` | ok，0 DATA RACE |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Vet | `go vet -tags fts5 ./...` | exit 0 |
| 格式 | `gofmt -l pkg cmd` | 输出为空 |
| 架构守卫 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |

链接期 `ld: warning: … built for newer 'macOS' version` 是本机既有噪声，忽略。

## Steps

### Step 1：run 侧把预算化报告带进完成钩子

1. `run.SubagentExecutionEnd`（`subagent.go:4800`）加字段：

```go
// Output is the execution's final report, already budgeted (over-budget
// reports carry the cut head and the path to the whole). The composition
// root delivers it to the dispatching conversation when nobody awaited the
// execution; an awaited one received it through the blocking tool already.
Output string
```

2. `reportSubagentExecutionEnded`（`:1297`）加 `output string` 参数，内部先 `inline, _ := budgetSubagentReport(fac, strings.TrimSpace(record.TaskID), output, subagentReportInlineChars)`，填 `end.Output = inline`。
3. 唯一调用点 `:1274` 改为 `reportSubagentExecutionEnded(fac, record, err, awaited, out)`（`out` 在该处已在作用域内）。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestAsyncSubagent|TestSubagentExecution' -count=1` ok。新测试 `TestSubagentExecutionEndCarriesTheBudgetedReport`（`subagent_test.go`，用捕获型 SubagentExecutor fake，样板 `factory_test.go:21`）：短报告原样；>50_000 字符的报告被截断且含 spill 路径句、spill 文件内容与原文逐字节一致（样板 `subagent_report.go` 既有测试的断言形状）。

### Step 2：process 侧无条件挂投递 inbox

1. `open.go` Environment（`peers *peerService` 字段旁，:161）加：

```go
// peerDelivery is the in-process delivery table (deliverPrimary). It is
// wired even where the cross-process registration cannot run: a subagent
// reporting to its own dispatcher is in-process work and must not depend
// on the peer protocol being available.
peerDelivery *peerInbox
```

2. `peers.go` StartPeers：`svc.inbox = newPeerInbox(svc, s)`（:304）之后立即 `env.peerDelivery = svc.inbox`（env 非 nil 时），**在 Enabled 闸门之前**。`setPeerService` 的位置与语义不动。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` ok（既有 peer 测试一条不红）。

### Step 3：完成即自动回报（核心）

`one_shot.go` 的 `env.SubagentExecutionEnded`：先照旧转发表面钩子（auto-continue 不变），随后调用新的私有方法 `env.deliverSubagentReport(end)`：

跳过规则（任一命中即 return）：
- `end.AwaitedByDispatcher`；
- `strings.TrimSpace(end.ConversationSessionID) == ""`；
- `errors.Is(end.Err, context.Canceled)`；
- usage-limit 且带已知重置时间：`llm.Explain(end.Err)` 的 Code 为 `ExplainRateLimitQuota`/`ExplainRateLimitThrottle` 且 `ResetAt` 非零（判据与 `pkg/turn/submit.go:665-678` 逐字一致）；
- `env.peerDelivery == nil`（记一条 slog.Debug 后 return）。

信封（ helper 放 one_shot.go，形状对齐 `peerMessageText`）：
- 成功：`<agent-message from="{AgentKey}">Your subagent {AgentKey} finished successfully. Its final report:\n\n{Output}\n\nContinue the task you dispatched it for: integrate this result, complete the remaining work, and deliver the final result.</agent-message>`
- 失败：同形，状态句改为 `finished with an error: {llm.ExplainError(end.Err)}`，报告段为 `Its partial report:` + Output（空则省略该段），结尾句改为 `Decide how to proceed — continue the subagent, dispatch a replacement, or finish the task another way — and deliver the final result.`

投递：`env.peerDelivery.deliverPrimary(context.Background(), end.ConversationSessionID, PeerPrompt{SessionID: …, Text: 信封, Origin: state.MessageOriginPeer, MsgID: uuid.NewString(), FromName: end.AgentKey})`；返回非空（refused）时 slog.Debug 记录，不重试。

**Verify**：Step 4 的新测试全绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` ok。

### Step 4：定向测试（全部进 `peer_inbound_test.go`，样板见 Current state §7）

1. `TestSubagentEndWakesTheIdleDispatcherWithItsReport`（样板 `:427`）：`svc.env.SubagentExecutionEnded(ctx, run.SubagentExecutionEnd{ConversationSessionID: 屏幕会话, AgentKey: "log-287", Output: "the report", AwaitedByDispatcher: false})` → `view.wakeCount()==1`；prompt.Text 含 `the report` 与 `log-287`、Origin 为 `state.MessageOriginPeer`、FromName 为 `log-287`。
2. `TestSubagentEndSteersTheRunningDispatcher`（样板 `:451`）：造 running 态 → steer 条目含报告；`view.wakeCount()==0`。
3. `TestSubagentEndLeavesAwaitedCancelledAndUsageLimitAlone`：三种 end 各调一次 → 无 wake、无 steer。usage-limit 错误构造：grep `ExplainRateLimitQuota` 在 `pkg/turn`/`pkg/llm` 测试里的现成 fixture 并复用（**不要自造错误类型**）。
4. `TestSubagentEndReportsAFailure`：普通错误（如 `errors.New("provider refused")`）→ wake 文本含错误描述与「Decide how to proceed」。
5. 既有 `TestPeerInbound*` 全数仍 PASS、断言零放宽。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestSubagentEnd|TestPeerInbound' -count=1 -v` → 全 PASS。**红绿演示（写进报告）**：临时注释 Step 3 的 deliverPrimary 调用，测试 1/2/4 必须红；恢复后复绿。

### Step 5：回归

`CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 空、`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process ./pkg/turn ./pkg/tui ./pkg/architecture -count=1` ok，再 `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run -race -count=1` ok 0 DATA RACE。

## Test plan（汇总）

| 文件 | 新测试 | 覆盖 |
|------|--------|------|
| `pkg/run/subagent_test.go` | `TestSubagentExecutionEndCarriesTheBudgetedReport` | 短报告原样；超长截断 + spill 路径 + 文件逐字节 |
| `pkg/process/peer_inbound_test.go` | `TestSubagentEndWakesTheIdleDispatcherWithItsReport` | idle→Wake，信封/Origin/FromName |
| 同上 | `TestSubagentEndSteersTheRunningDispatcher` | running→Steer，不 wake |
| 同上 | `TestSubagentEndLeavesAwaitedCancelledAndUsageLimitAlone` | 三类跳过 |
| 同上 | `TestSubagentEndReportsAFailure` | 失败也回报 |

## Done criteria（全部满足）

- [ ] Drift check 通过；五个新测试存在且 PASS；红绿演示记录在报告
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process ./pkg/turn ./pkg/tui -count=1` ok；`./pkg/process ./pkg/run -race` ok 0 DATA RACE
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet -tags fts5 ./...` exit 0；`gofmt -l pkg cmd` 空
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` ok（pkg/process 仍 34 个生产文件，无新生产文件）
- [ ] `git status` 改动只落在 Scope 的六个文件；既有测试断言零放宽
- [ ] `pkg/agent`、`pkg/tui`、`pkg/gateway`、`deliverPrimary` 本体、`peerSendToDispatcher` 零改动

## STOP 条件（停下报告，不要自行发挥）

- Drift check 任一处与现状不符。
- pkg/process 生产文件数不是 34，或架构守卫因本计划变红——文件边界/分层棘轮比预想紧。
- 既有 `TestPeerInbound*` 或 auto-continue 测试变红且无法在不改断言的前提下修复——说明投递语义被意外改动。
- 找不到现成的 usage-limit 错误 fixture（grep `ExplainRateLimitQuota` 无测试用例）——不要自造错误类型，报告后等指示。
- 发现必须改 `deliverPrimary` 本体、表面 Wake、或新建生产文件才能成立。
- 真机验证（若派发方要求）：TUI 里 `subagent_send` 派发一个短任务，主回合结束后等待——子代理完成时应自动出现 peer 卡片并开新回合，主代理读到报告后继续交付；子代理视图/主视图的 Working 行行为与 305 无关、不受影响。

## Maintenance notes

- **成本与循环面**：每个 fire-and-forget 完成都会开一个主回合；被唤醒的主代理可能再派发子代理→再唤醒。这是 owner 要的自主闭环，终止条件是模型认为任务完成。评审/运维关注 token 消耗；若日后要加闸，建议配置项挂 `agents` 段（参考 299 的全局字段路线），本期不做。
- **TUI 既有限制**：wake 到达时若用户已切到别的会话，消息被丢弃并记日志（`notify.go:2504-2507`）——与跨进程 peer 消息今天的行为一致，本计划不改。gateway 无此限制（detached turn）。
- **与子代理主动 peer_send 的关系**：运行中子代理仍可 peer_send 进度消息（Steer 投递）；完成自动回报是最终报告，两者内容不同、不算重复。本期不改提示词，模型自发行为不变。
- **usage-limit 的边界**：跳过判据必须与 `autoContinuer.continuationFor` 保持逐字一致；若那边改了判据（如新增 Code），这里要同步，否则会出现「auto-continue 不续、主代理也没被唤醒」的真空。
- 评审重点：跳过规则是否完整（awaited/取消/usage-limit）；信封是否经 budgetSubagentReport 单点截断；inbox 接线是否在 Enabled 闸门之前；表面钩子转发是否原样保留。