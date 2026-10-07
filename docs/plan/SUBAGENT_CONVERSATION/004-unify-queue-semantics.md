# 计划 004：队列语义只在引擎里实现一份，以 TUI 为准，TUI 主视图和网页都用它

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SUBAGENT_CONVERSATION/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **漂移检查（先运行）**：
> `git diff --stat bda9505 -- pkg/run/turn_input.go pkg/run/controller.go pkg/tui/run.go pkg/tui/chat_session.go pkg/gateway/run_control.go pkg/gateway/server.go frontend/src/composables/useChatStream.ts frontend/src/lib/composerSubmission.ts`
> 基线 `bda9505` 对应干净树：diff 列出文件即有新改动，按函数名和注释原文核对"现状"摘录；对不上就 STOP。

## 状态

- **优先级**：P1（README 决策 D7：顺带发现的既有缺陷，owner 2026-10-04 批准纳入本期；计划 005、007、008 依赖它）
- **工作量**：L
- **风险**：HIGH（改的是主视图每天都在用的排队、召回、中断后恢复）
- **依赖**：无
- **类别**：tech-debt / bug
- **基线**：提交 `bda9505`，2026-10-04

## 为什么要做

owner 的第 2 点要求每个 subagent 有"独享的、完整的 message queue 语义"，并与主视图一致。写计划时发现，"主视图的队列语义"今天就有三份实现，而且已经分叉：

1. **TUI**（`pkg/tui/run.go`）：`streamState` 自己维护三条队列 `pendingSteers`、`rejectedSteers`、`queuedTurns`，用共享时钟 `queueSeq` 给每条消息编号；召回、回合结束后的下一条、中断后的恢复都在 TUI 里决定（`restoreLatestQueuedEditableSubmission`、`nextAutomaticSubmission`、`restoreWithdrawnWork`）。只有 steer 的"投递给模型"这一半经过引擎的运行时。
2. **引擎**（`pkg/run/turn_input.go` 的 `InputQueue`）：gateway 用它存 steer 和 follow-up，运行结束时 `Controller.Release` 把剩下的交回。
3. **网页**（`frontend/src/composables/useChatStream.ts`）：收到 `queued_input_released` 后，由浏览器决定是作为下一条发出还是退回 composer（`state.awaitingRelease`、`state.released`、`giveBack`）。

已确认的分叉：
- **召回最新一条**：TUI 按共享时钟在三条队列里取编号最大的（`pkg/tui/run.go:2400-2407` 的注释明确说"先挑某一条队列会在最新消息落在别的队列时召回一条更旧的"）；引擎 `InputQueue.PopLatest` 先取 steer，再取普通 follow-up，最后取被拒的 steer——正是 TUI 注释里说的缺陷，网页的"编辑最后一条排队消息"会召回错的消息。
- **回合结束后的下一条**：TUI 先把被拒的 steer 合并成一条发出，下一轮再把未投递的 steer 合并成一条发出，再下一轮才是普通排队消息（`nextAutomaticSubmission` 的 `runTurnCompleted` 分支）；引擎 `Drain` 先 `RejectSteers` 把未投递的 steer 并进被拒列表，再由 `PopNext` 合成**一条**。
- **带图片的 steer**：TUI 把图片作为 parts 交给 `ctl.Steer(runID, run.Input{Parts: parts})`，`InputQueue.Steer` 因为 `Attachments` 为空而接受；网页带附件时 `Steer` 拒绝，改成排队。

如果 subagent 的队列（计划 005）建在引擎上，而 TUI 主视图用自己那套，TUI 里就会出现两套行为不同的队列，"与主视图一致"无从保证。按"TUI 是语义标准、共享的一律下沉到引擎"的规矩，先把这套状态机以 TUI 的语义收进引擎，TUI 主视图和网页都只调用它；计划 005 再给每个 subagent 一份同样的东西。

## 现状（2026-10-04 工作区的事实）

### 引擎

- `pkg/run/controller.go`：`Input{Text, Parts, Attachments, MentionImages, Rejected}`；`Controller` 的 `Steer/FollowUp/Retract/DrainSteers/Preview/Queue/Release`，按 run id 找到 `RunState.input *InputQueue`。
- `pkg/run/turn_input.go`：`TurnInputRuntime`（模型侧的 steer 运行时：`Enqueue`、`BeginSteerDelivery`/`Commit`/`Rollback`、`RetractLastSteer`、`Snapshot`、变更钩子）；`InputQueue`（`Steer`、`FollowUp`、`RejectSteers`、`PopLatest`、`RetractSteer`、`DrainSteers`、`PopNext`、`Drain`、`Preview`）。`QueuePreview{Steers, Rejected, FollowUp}`。

### TUI 主视图（`pkg/tui/run.go`，以下行为就是规格）

| 编号 | 行为 | 代码 |
| --- | --- | --- |
| Q1 | 运行中按 Enter：`session.SteerSurfaceRun` 接受 → 进 `pendingSteers`；不接受 → 进 `rejectedSteers` | `handleActiveRunInput` 的 `inputEventLine` 分支（约 `:3827-3893`） |
| Q2 | 正在跑用户 shell 命令、或前台回合已撤回时按 Enter → 进 `queuedTurns` | 同上，`onlyUserShellCommandsRunning`、`activeForeground.isWithdrawn()` |
| Q3 | steer 在工具边界投递给模型 → 从 `pendingSteers` 移除，并在对话里画成一条用户消息 | `reconcilePendingSteers`、`reconcilePendingSteersCount`、`renderDeliveredSteerMessages`（`:2340-2394`） |
| Q4 | 召回：按共享时钟取三条队列里最新的一条；是 pending steer 就先从运行时撤回，撤回失败（已投递）就跳过全部 pending steer 再找 | `restoreLatestQueuedEditableSubmission`、`newestQueuedSubmission`（`:2408-2470`） |
| Q5 | 回合正常结束：被拒 steer 合并成一条作为下一回合 → 否则未投递 steer 合并成一条 → 否则第一条排队消息；循环直到空 | `nextAutomaticSubmission` 的 `runTurnCompleted` 分支（约 `:3500-3530`） |
| Q6 | Esc 中断且有待投递 steer：这次中断就是为了"立即发送"，未投递 steer 合并成一条作为新回合发出 | `markSubmitPendingSteersAfterInterrupt`、`nextAutomaticSubmission` 的 `runTurnInterrupted` 分支 |
| Q7 | 其它中断/取消：被拒 steer、未投递 steer、排队消息、当前草稿按此顺序合并成一份草稿放回 composer | `mergeAndRestoreInterruptedWork` |
| Q8 | 撤回（模型尚未输出时按 Esc）：被撤回的消息 + 之后排队的一切 + 当前草稿，按写下的顺序合并成一份草稿 | `restoreWithdrawnWork`（`:2540`） |
| Q9 | 有待应用的模态选择（切会话、权限、技能、模型）时，暂停自动发送 | `suppressQueueAutosend` |
| Q10 | 清空：先从运行时撤回 pending steer，再清空三条队列，返回丢弃条数 | `discardQueuedInput`（`:2308`） |
| Q11 | 队列预览：三组文本，用 `DisplayText`（保留 `[Image #1]`、`[Pasted Content N chars]` 占位） | `composerPendingInputPreview`（`:2122`） |
| Q12 | 消息退回 composer 时完整退回：文字、上传附件、@ 图片、折叠粘贴 | `restoreQueuedSubmission`、`restoreDraftSubmission`；见 memory 规则"composer-restore-is-whole" |

### gateway / 网页

- `pkg/gateway/run_control.go`：`handleRunInput`（steer）、`handleRunQueuedInput`（follow-up；`action=edit_last` 调 `PopLatest`）、`finishRun`（`Release` 后发 `queued_input_released`）。
- `frontend/src/composables/useChatStream.ts`：`queueActiveRunInput`（约 `:2140`，steer 失败改排队）、`queued_input_released` 的处理（约 `:2565`）、`giveBack`。执行者第 1 步要把网页里所有参与排队决策的函数列成清单。

## 设计

### 1. 引擎的队列状态机（以 TUI 语义为准）

在 `pkg/run/turn_input.go` 里重写 `InputQueue`（不新建文件）：

- **一个共享时钟。** 每条进队的消息都带 `Seq`（队列内单调递增）。`TurnInputEntry` 加 `Seq int`，`InputQueue.Steer` 入运行时时带上它；follow-up 和被拒 steer 也带。
- **消息携带界面自己的数据。** `Input` 加 `Payload any`，注释："the surface's own record of the message — what it shows and what it hands back to the composer. The queue never reads it; it travels with the message so a recalled, released or restored message comes back whole." TUI 放 `ComposerSubmission`，网页（经 gateway）放它的附件 id 和 @ 图片路径（今天已在 `Attachments`/`MentionImages` 里）。
- **steer 的准入以 TUI 为准（Q1、Q11）。** 只要有 parts 就接受；带附件的消息也能 steer（`Payload` 让它能被完整退回，原来拒绝它的理由不复存在）。删掉 `Steer` 里对 `Attachments`/`MentionImages` 的拒绝及其注释。**这是网页可见的行为变化**：网页上带附件的消息在运行中会作为 steer 投递，而不是排到下一回合。owner 已于 2026-10-04 批准这一变化（README 决策 D7），在完成报告里写明即可。
- **召回（Q4）**：`Recall() (Input, bool)`：按 `Seq` 取三类里最新的；是 steer 就 `RetractLastSteer`，失败就把全部 steer 排除后重找。删除 `PopLatest`。
- **边界决定（Q5、Q6、Q7）**：

  ```go
  // Boundary is how one turn of a conversation ended, as far as its queue is
  // concerned.
  type Boundary int
  const (
  	BoundaryCompleted Boundary = iota // the turn ran to its end
  	BoundaryInterruptToSend            // Esc with steers pending: send them now
  	BoundaryInterrupted                // any other interruption or cancellation
  )

  // Next decides what follows a turn that ended at b. Send is the input to run
  // as the next turn, merged from the entries listed in From; Restore is every
  // entry that goes back to the composer instead, in the order they were
  // written. At most one of the two is non-empty.
  func (q *InputQueue) Next(b Boundary) (send []Input, restore []Input)
  ```

  - `BoundaryCompleted`：有被拒 steer → `send` = 全部被拒 steer（按 Seq）；否则有未投递 steer → `send` = 全部未投递 steer；否则 `send` = 第一条 follow-up。调用方在下一回合结束时再调 `Next(BoundaryCompleted)`，直到两者都空（Q5 的循环）。
  - `BoundaryInterruptToSend`：`send` = 全部未投递 steer（Q6）。
  - `BoundaryInterrupted`：`restore` = 被拒 steer、未投递 steer、follow-up，按这个顺序（Q7；草稿由界面自己追加在最后）。
  - 合并成一条消息由**界面**做（它知道怎样合并自己的 `Payload`：TUI 用 `mergeComposerSubmissions`），引擎返回的是有序的条目。这样"哪些消息合在一起、按什么顺序"只在引擎里决定一次。
- **清空（Q10）**：`Discard() int`。
- **预览（Q11）**：`Preview()` 的文本取自 `Input.Text`；TUI 进队时把 `Text` 设为它的预览文本（`composerSubmissionPreview` 的结果），模型侧的内容在 `Parts`。
- **撤回（Q8）**：`TakeAll() []Input`（按 Seq 排序的全部条目，steer 先撤回），供界面的撤回路径使用。
- **队列属于对话，运行时属于回合。** 这是 TUI 今天的结构（三条队列在 `streamState` 里跨回合存在，`tuiTurnInputRT` 每个回合换新），引擎照此建模：
  - `InputQueue` 持有被拒 steer 和 follow-up，跨回合存在；`Attach(rt *TurnInputRuntime)` 在回合开始时挂上本回合的 steer 运行时，`Detach()` 在回合结束时摘下，并把运行时里未投递的 steer 移进被拒列表（保留各自的 `Seq`）。摘下后再来的 steer 进被拒列表，不会被投递进下一回合（保持 `discardTUITurnInput` 注释里的不变式）。
  - `Controller` 改为按**会话**保存 `InputQueue`（`RunState` 引用它所属会话的队列），`Queue(runID)` 仍按 run id 找到它。
  - `Controller.Release(runID)` 是 gateway"运行结束、交回剩余输入"的入口，签名改为 `Release(runID string, b Boundary) (send, restore []Input)`：先 `Detach()`，再 `Next(b)`。`Next(BoundaryCompleted)` 只取出下一回合要发的那一批，其余条目留在会话的队列里，等那一回合结束时再由下一次 `Release` 决定——与 TUI 的 Q5 循环一致。gateway 按运行结束的方式传入 boundary（正常结束、用户中断并发送、其它取消）。
  - 会话被切走、删除或进程退出时队列随之丢弃，与今天 TUI 的行为一致（`switchStreamSession` 会丢弃排队输入）；执行者读 `switchStreamSession` 里处理排队输入的那段确认，不一致就 STOP。

### 2. TUI 主视图改用它

- `streamState` 删除 `pendingSteers`、`rejectedSteers`、`queuedTurns`、`queueSeq` 及其直接操作函数；改为通过 `Session` 接口操作当前对话的 `*run.InputQueue`（`ChatSession` 用 TUI 控制器 `tuiControl` 里按会话保存的那一个；回合开始时 `Attach` 本回合的 `tuiTurnInputRT`，回合结束时由 `Release`/`Detach` 摘下）。`SteerSurfaceRun`、`RetractSurfaceSteer`、`SurfacePendingSteerCount`、`QueueSurfaceFollowUp` 改为操作这个队列；因此冗余的接口方法删掉。
- Q1–Q12 的每一条，TUI 的调用点改为调用引擎方法，界面只做：把 `ComposerSubmission` 装进 `Payload`、渲染 `Preview`、把 `Next` 返回的 `send` 用 `mergeComposerSubmissions` 合并后提交、把 `restore` 合并后放回 composer。
- `discardTUITurnInput`（回合结束时丢弃运行时）的语义保持：队列对象跨回合存在，运行时按回合换新——执行者读 `ensureTUITurnInputRuntime`/`discardTUITurnInput` 的注释，确保"为一个回合排的输入不会被投递进下一个回合"这条不变式仍然成立；做不到就 STOP。

### 3. gateway 和网页改用引擎的决定

- `finishRun`：按运行结束方式取 `send`/`restore`，在 `queued_input_released` 的载荷里分开给出：`event.QueuedInputReleasedPayload` 加 `Next []ReleasedInput`（下一回合要发的，已按引擎的顺序列出、由网页合并成一条）和保留 `Inputs`（退回 composer 的）。字段是新增的，旧客户端忽略它。
- 网页：`queued_input_released` 的处理不再自己决定发还是退：`Next` 非空就把它们合并后作为下一回合发出（沿用今天发出一条消息的函数），`Inputs` 非空就 `giveBack`。删掉浏览器端重复的判断（执行者第 1 步列出的清单里，凡是"决定发还是退"的分支都删）。
- `handleRunQueuedInput` 的 `edit_last` 改调 `Recall`。

## 缓存影响

无。只改排队与投递的时机和顺序的决策位置，不改变投递给模型的内容和位置（steer 仍在工具边界追加到尾部）。唯一的可见变化是网页上带附件的消息在运行中会作为 steer 投递——同样是追加在尾部。

## 范围

**只改这些文件：** `pkg/run/turn_input.go`、`pkg/run/controller.go`、`pkg/tui/run.go`、`pkg/tui/chat_session.go`、`pkg/tui/notify.go`（`Session` 接口）、`pkg/gateway/run_control.go`、`pkg/gateway/server.go`、`pkg/event/run_events.go`（`QueuedInputReleasedPayload.Next`）、`frontend/src/composables/useChatStream.ts` 以及它们的测试。

**不要动：** steer 的投递时机（`orchestration_llm.go` 里 `BeginSteerDelivery` 的两个调用点）；composer 的编辑行为；subagent 的任何路径（计划 005）。

## 步骤

### 第 1 步：把现行语义钉成测试（先于任何改动）

1. 在 `pkg/tui/run_test.go` 为 Q1–Q12 各写一条**表征测试**（characterization），名字以 `TestQueueSemantics_Q<编号>_` 开头，断言今天的 TUI 行为。已有测试能覆盖的条目，在测试注释里写"由 `<已有测试名>` 覆盖"，不重复写。
2. 在 `frontend/src/composables/useChatStream.test.ts` 为网页今天的行为写同样编号的测试（只写网页实际具备的条目）。
3. 在报告里列出网页里所有参与"排队/召回/发还是退"的函数（名字和行号）。
4. 写一张对照表（放在报告里）：每个 Q 编号，TUI 的行为、网页今天的行为、是否一致。**所有不一致项都按 TUI 改网页**；如果发现某个不一致项让你怀疑 TUI 才是错的，STOP 并把它带回来让 owner 拍板。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestQueueSemantics_' -count=1` → `ok`；`cd frontend && corepack pnpm test -- useChatStream` → 通过。

### 第 2 步：引擎状态机

按"设计"第 1 条重写 `InputQueue`，在 `pkg/run/turn_input_test.go` 用表驱动测试覆盖 Q1、Q4、Q5、Q6、Q7、Q8、Q10、Q11 的引擎部分（尤其：最新一条落在被拒队列时召回它；完成后先发被拒、再发未投递 steer、再发排队消息，各为一回合；撤回失败的 steer 被跳过）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'InputQueue|TurnInput' -count=1` → `ok`；`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run -run 'InputQueue|TurnInput' -count=1` → 无 `DATA RACE`。

### 第 3 步：TUI 主视图改用引擎

按"设计"第 2 条。第 1 步的表征测试**一条都不许改断言**，只允许改搭建方式（例如原来直接往 `pendingSteers` 塞数据的地方改为调用队列方法）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`；`grep -n "pendingSteers\|rejectedSteers\|queuedTurns\|queueSeq" pkg/tui/*.go | grep -v _test.go` → 无输出。

### 第 4 步：gateway 与网页

按"设计"第 3 条。第 1 步网页测试里，与 TUI 一致的条目断言不变；不一致的条目按对照表改成 TUI 的行为。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → `ok`；`cd frontend && corepack pnpm test` → 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` → 退出码 0。

### 第 5 步：真机

TUI（`.claude/skills/run-forebrain/driver.sh`，`stream` 模式让回合停在已输出、未结束的状态）：

1. 运行中连发三条消息 `a`、`b`、`c`；`$D screen` 看预览；按召回键 **Shift+Left**（`hotkeyEditLastQueued`，`pkg/tui/input_events.go:57`，绑定在约 `:1100`）应召回 `c`。
2. 再发 `d`，按 Esc：`d` 作为新回合立即发出（Q6）。
3. 另起一轮：运行中发 `x`，`$D key C-c` 取消：`x` 回到 composer（Q7）。

网页：`scripts/acceptance/web_e2e.sh` 里若已有排队场景，补"召回的是最新一条"的断言并运行；没有就在报告里写明，留给计划 009。

**验证**：三步的屏幕输出与预期一致，贴进报告。

## 完成标准

- [ ] 第 1 步的表征测试在第 3 步之后断言未改、全部通过
- [ ] `pkg/tui` 生产代码里不再有三条队列的字段
- [ ] 网页不再自己决定"发还是退"
- [ ] 全量 Go 测试、前端测试、类型检查、架构测试、死代码检查通过
- [ ] 报告里有 Q1–Q12 的对照表、网页函数清单、真机屏幕输出
- [ ] README 状态行已更新

## STOP 条件

- 表征测试写不出来（某条 TUI 行为与上表描述不符）。
- 对照表里出现"TUI 可能才是错的"的不一致项。
- 第 3 步需要改表征测试的断言才能通过。
- "为一个回合排的输入不会被投递进下一个回合"的不变式无法保持。

## 维护说明

- 从此以后，排队语义的任何改动都在 `run.InputQueue` 里改一次；界面只负责渲染和合并自己的 `Payload`。评审时，凡是界面代码里出现"按某种顺序挑选排队消息"的逻辑，都是回退。
- 计划 005 会为每个 subagent 建一个 `InputQueue`，计划 007、008 让 subagent 视图调用同样的方法——三处行为一致是由"同一份代码"保证的。
