# subagent 成为可以直接对话的会话：实施计划索引

由 improve skill 于 2026-10-04 生成。计划摘录的代码是 2026-10-04 工作区里的样子，那批改动已于 2026-10-04 深夜由 owner 提交为 **`bda9505`**（`feat(lsp): add language-server code intelligence runtime and surfaces`，git 时间戳 2026-10-04 22:55 +0800）；各计划的漂移检查与基线一律对比 `bda9505`。2026-10-06 用 review-plan 复核过全部引用并收紧（见文末"评审记录"）。
**状态：D1–D15 已全部由 owner 定夺（D1–D4 由 owner 直接选择，D5–D9 于 2026-10-04 按推荐批准，D10–D15 于 2026-10-05 定夺，见决策表），全部纳入本期。预览页 `subagent-cards-preview.html`（Version 2）与网页的退出计划模式审批卡片均已由 owner 确认（D12、D14）。决策与预览均无待定项；开始实施前仍以 owner 的明确指令为准。**

## owner 的原始要求（2026-10-04，逐字）

> forebrain harness的subagent相关的语义和功能需要优化：
>
> 1. 现在subagent会话/视图因为网络原因失败和中断后，无法通过发送"continue"用户消息让subagent继续执行，subagent视图里发送的用户消息会加入primary agent的message queue，只能回到primary agent视图，按下esc键将本应该发送给subagent的消息发送给primary agent。subagent失败后，必须支持在该subagent的视图里发送用户消息给该subagent使会话继续
> 2. subagent会话/视图必须支持该subagent独享的完整的message queue语义和与用户直接对话的语义
> 3. subagent会话/视图必须支持compact压缩，包括auto compact自动压缩和用户执行slash命令/compact手动压缩
> 4. subagent视图footer区域右侧必须展示"75%/1M"这样的该subagent自己的上下文窗口统计数据，与primary agent主视图保持一致

> 还有第5点：primary agent主视图里点击subagent的消息卡片，进入该subagent的视图时，agent roster区域的agent选择器没有自动选中该subagent的行，已经进入subagent的alt-screen VM视图，但是agent roster里箭头仍然指向main，必须定位根因并修复，加入本期计划

> 必须使用 improve skill 先写计划，我批准后再实施，不要直接改代码

> （附 TUI 截图）如forebrain harness tui的截图所示，agent roster 里的subagent行的任务与subagent消息卡片的任务不一致，请定位根因并修复bug，加入本期计划。agent roster 里的subagent行的任务必须使用subagent消息卡片的任务

> 因为usage limit reach而中断的subagent必须可以自动恢复，对齐primary agent的语义，给subagent加上相同功能

> （2026-10-05，附两张 TUI 截图）如 forebrain harness tui截图所示，这两个subagent_*工具的消息卡片不符合需求，output区域是原始json数据，必须定位根因并修复bug，对齐subagent_fanout的消息卡片的UI/UX，重新设计，必须在docs/plan/SUBAGENT_CONVERSATION目录下生成新计划文件

> （2026-10-05）必须全面审计所有的subagent_*工具的消息卡片的UI和UX，找出所有问题，定位根因并设计修复计划，加入本期计划中

> （2026-10-05，看过预览后）把 · 66 tool uses 这行统计删掉

> （2026-10-05）✓ 任务16 migrate 导入、✗ 任务17 扩展目录和计划002 Web车道实施这样的任务名称后面加上运行时长计时和最终耗时
>
> 运行中的加上运行时长计时，执行结束的（包括成功、失败、取消等）的加上最终耗时

> （2026-10-05，附三张 TUI 截图）如forebrain harness tui截图所示，exit_plan_mode审批选择other model to review plan后的plan-reviewer相关功能有严重bug：
>
> 1. plan-reviewer subagent的alt-screen VM视图里的每一条assistant message都是全量，而不是增量，出现重复内容
> 2. plan-reviewer subagent的alt-screen VM视图里只显示了思考内容消息卡片和assistant message消息卡片，没有工具调用消息卡片
> 3. 用户选择的plan-reviewer subagent的llm是zhipuai/glm-5.3-flash，但plan-reviewer subagent的alt-screen VM视图里composer下方显示的llm是zhupuai/glm-5.3，模型是错的
> 4. exit_plan_mode工具审批后，显示subagent plan-reviewer spawned plan-reviewer消息文案需要重新设计，这个文案语义有错误，应该是主agent spawned plan-reviewer subagent，必须改成语义正确且更容易理解的文案

> 必须先用improve skill在docs/plan/SUBAGENT_CONVERSATION生成计划文件，不要改代码

> （2026-10-05）没有派发调用的执行 plan-reviewer的消息卡片的header里不要加主语，与其他的subagent_*工具调用消息卡片的header保持一致使用Starting/Running/Ran/Failed to start等

> （2026-10-05）plan-reviewer 的视图的UI/UX必须与其他subagent的视图保持一致，tui和web都必须显示当前subagent会话的上下文窗口统计信息，例如"65%/1M"

> （2026-10-05）review completions print the reviewer's full answer into the main conversation as a card -------- it's a bug, must be fixed, output must stay in their own view just like any other subagent

> （2026-10-05）如果subagent调用了session_todo工具，Working和Worked for消息还必须显示checkbox图标和进度

## 一句话结论

七个要求背后是六个根因，前三个都在引擎里，不在界面上：

1. **subagent 的模型上下文从来没有落库。** typed subagent 用 `workerSessionID` 跑 `run.Run`，但没有任何代码往这个会话写消息（本机状态库核实：`fb_messages` 里 `session_id LIKE 'main:%'` 的行数为 0，`fb_sessions` 里也没有这类会话）。fork subagent 的上下文只在内存和一个旁路 JSONL 里。所以 subagent 一旦失败，它做过的工具调用和结果全部丢失。`subagent_continue` 只能拼一段"原任务 + 上次结果摘要"重新开始（`pkg/run/subagent.go:1283` `buildSubagentContinuePrompt`）。`/compact` 没有可压缩的东西，footer 的上下文占用也无从计算。这是第 1、3、4 点共同的根因（计划 002）。
2. **subagent 没有自己的输入通道。** subagent 的运行上下文被刻意剥掉了输入运行时（`pkg/run/subagent.go:803` `WithoutTurnInputRuntime`），子运行在控制器里也只登记了取消函数，没有输入队列（`:824` `own.Control.Track`）。TUI 的 composer 不看当前视图，提交永远走主会话：空闲时直接起主回合，运行中则 steer 或排进主 agent 的队列（`pkg/tui/run.go` 的主循环和 `handleActiveRunInput`）。这是第 1、2 点的根因（计划 005、007、008）。
3. **队列语义有两份实现。** TUI 主视图自己维护一套镜像队列（`streamState.pendingSteers/rejectedSteers/queuedTurns` + `nextAutomaticSubmission`），网页用引擎的 `run.InputQueue`。两者已经分叉，例如"召回最新排队消息"：TUI 按统一的入队时钟取最新一条，引擎先取 steer。TUI 的注释明确说后者是缺陷（`pkg/tui/run.go:2399-2404`）。要让 subagent "独享完整的队列语义、与主视图一致"，只能先把队列语义统一到引擎（以 TUI 为准），再给每个 subagent 一份（计划 004、005）。
4. **roster 光标有两个事实来源。** 箭头在 roster 获得键盘焦点后读 `streamState.agentRosterSelected`，这个下标只被 ↑/↓ 改写；点击卡片改变视图时它不动。已用一次性测试（`go test -overlay`，未落盘到仓库）复现：先按 ↓ 让 roster 获得焦点，再点卡片，视图已是 `task-b`，箭头仍指着 `main`。同一个下标在 subagent 行被移除时还会挪到别的 agent 上，按 `x` 会取消错的 subagent（计划 001）。
5. **roster 行显示的是"正在跑的工具"，任务名还有两套推导。** `pkg/tui/render.go` 的 `agentRosterRowDetail` 按 `Activity` → `Title` → `Task` 取值，subagent 一调工具，行就变成 `edit /…`、`read /…`（当初有意的设计，owner 现已否定）。卡片上的任务名（`parseFanoutTasksFromMeta`：title，否则 prompt 截 80 列）和 roster 的标题（引擎 `subagentDisplayTitle`：title，否则 prompt 第一行截 160 字节）又是两条规则，没给 title 时不一致（计划 010）。
6. **自动继续只挂在主会话的回合上。** 主 agent 的"额度恢复后自动继续"由 `turn.Service.Submit` 在回合结束时安排（`pkg/turn/submit.go` 的 `autoContinuer`），subagent 的执行不经过 `Submit`，而且 `planFor` 明确排除了子运行（`req.ParentRunID != ""`）。计划 011 让同一个调度器按 subagent 安排、取消和触发，继续时经由计划 005 的用户输入通道发给这个 subagent。

另有两处顺带发现的既有缺陷，按规矩纳入本期：

- **有自己模型的 typed subagent，压缩阈值按主 agent 的模型窗口算。** `compactDeps.ActiveModel` 和压缩服务的 `PrimaryModel` 都走 `r.effectiveModelFor(ctx)`（`pkg/run/config.go:1676`），而 typed subagent 的请求被 `typedSubagentProviderLLM`（`pkg/run/typedsubagent_llm.go`）路由到 `agents.definitions[<type>].llm_providers`。窗口比主模型小的 subagent 会在压缩前先撞上上限（计划 003）。
- **footer 的上下文预算在 TUI 和 gateway 各算一遍。** `pkg/tui/chat_turn.go:1214` `tokenBudgetMessageFromUsage` 和 `pkg/gateway/wsevents.go:392` `tokenBudgetPayloadFromSession` 是同一段逻辑的两份拷贝（计划 006 下沉到引擎，并按 agent 区分模型）。

## 执行顺序与状态

| 计划 | 标题 | 对应要求 | 优先级 | 工作量 | 依赖 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| 001 | [roster 光标以视图为准，按 agent 键记录](001-roster-cursor-follows-view.md) | 5 | P1 | S | — | TODO |
| 002 | [每个 subagent 的模型上下文落到它自己的 worker 会话里](002-persist-subagent-transcripts.md) | 1、3、4 的根基 | P1 | L | SRT-002、SRT-003 | TODO |
| 003 | [subagent 的压缩：按它自己的模型算阈值，支持回合前自动压缩和 /compact](003-subagent-compaction.md) | 3 | P1 | M | 002 | TODO |
| 004 | [队列语义只在引擎里实现一份，以 TUI 为准，TUI 主视图改用它](004-unify-queue-semantics.md) | 2 的前提（既有缺陷） | P1 | L | — | TODO |
| 005 | [引擎：每个 subagent 一条独享的用户输入通道](005-subagent-input-channel.md) | 1、2 | P1 | L | 002、003、004 | TODO |
| 006 | [上下文预算下沉到引擎，按 agent 计算并推送](006-per-agent-context-budget.md) | 4 | P1 | M | 002、003 | TODO |
| 007 | [TUI：subagent 视图里的对话、队列、Esc、斜杠命令和 footer](007-tui-subagent-conversation.md) | 1–4 | P1 | L | 001、003、004、005、006 | TODO |
| 008 | [网页：与 TUI 对齐的 subagent 对话](008-web-subagent-conversation.md) | 1–4 | P1 | L | 003、005、006、007 | TODO |
| 009 | [真机验收：断网后在 subagent 视图里继续](009-live-acceptance.md) | 1–9 | P1 | M | 001–008、010–016、SRT-004 | TODO |
| 010 | [roster 的 subagent 行显示的任务就是卡片上的任务](010-roster-row-shows-the-card-task.md) | 6 | P1 | S | — | TODO |
| 011 | [因用量上限中断的 subagent 在额度恢复后自动继续](011-subagent-auto-continue.md) | 7 | P1 | M | 005、007、008 | TODO |
| 012 | [subagent_* 工具调用的"卡片事实"只在引擎里推导一份](012-subagent-call-facts.md) | 8 | P1 | L | 010；D10、D11 | TODO |
| 013 | [TUI：八个 subagent_* 工具都画成与 fanout 同一种卡片](013-tui-subagent-call-cards.md) | 8、9 | P1 | L | 001、010、012、015；D12 | TODO |
| 014 | [网页：与 TUI 对齐的 subagent_* 卡片](014-web-subagent-call-cards.md) | 8、9 | P1 | L | 010、012、013、015；D12 | TODO |
| 015 | [plan-reviewer 的视图：工具卡片齐全、文字不重复、模型说对](015-plan-reviewer-view.md) | 9 | P1 | M | —（第 4 点的卡片在 013/014 里画） | TODO |
| 016 | [plan review 下沉到共享层，网页的"退出计划模式"审批与 TUI 对齐](016-shared-plan-review-and-web-exit-plan.md) | 9（顺带发现） | P1 | L | 013、014、015；D14 | TODO |

010、011 是 owner 在写计划过程中追加的第 6、7 点，012–014 是 2026-10-05 追加的第 8 点（subagent_* 工具卡片），015–016 是同日追加的第 9 点（plan-reviewer），编号沿用追加顺序，不重排。**推荐的执行顺序**：001 → 015 → 010 → 012 → 013 → 014 → 016 → 002 → 003 → 004 → 005 → 006 → 007 → 008 → 011 → 009。015 没有前置依赖、改动集中，放在最前面先修 owner 看到的三个错误；它实现了 002 §5 的模型字段与 `run.AgentModel`，002 执行时复用。012–014 不依赖 SRT，也不依赖 002–008，可以在当前阻塞期间先做；它们改的 `pkg/tui/reducer.go`、`useChatStream.ts` 也是 007、008 要改的文件，先做完能让 007、008 在新的卡片模型上接着做，而不是反过来改两遍。

状态取值：TODO | IN PROGRESS | DONE | BLOCKED（附一句原因）| REJECTED（附一句理由）

"SRT-002""SRT-003"指 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/002-session-birth-identity.md` 和 `003-conversation-lists-by-purpose.md`。

## 依赖说明

- **002 依赖 SRT-002、SRT-003。** worker 会话要在出生时就带上用途（`source='subagent'`）、父会话和"不参与记忆"的身份，并且不能出现在任何对话列表里。SRT-002 引入 `SessionBirth` / `EnsureAt`，SRT-003 把列表改成在 SQL 里按用途过滤。`SessionBirth.Source` 字段由 SRT-005 引入；如果 002 开工时 SRT-005 还没做，002 第 1 步照 SRT-005 的原文加上这个字段（见 002）。
- **003 依赖 002。** 压缩要有落库的历史可压；回合前自动压缩读的是 worker 会话的行。
- **004 不依赖其它计划**，但它改的是 TUI 主视图的队列，风险最高，建议在 001 之后、005 之前单独做完并真机验证。
- **005 依赖 002、003、004。** 用户给 subagent 的消息要写进它的 worker 会话（002），宿主上下文由 003 的 `subagentRecordContext` 重建，队列用 004 统一后的 `run.InputQueue`（005 的前置检查与此一致）。
- **006 依赖 002、003。** subagent 的占用读它 worker 会话的最后一次 API 用量；"这个 agent 跑在哪个模型上"的解析函数 `run.AgentModel` 由 002 提供；压缩阈值的口径由 003 定下，footer 的百分比必须和它一致。
- **007 依赖 001、003、004、005、006**（004 之后主视图的队列语义在引擎里，subagent 视图的"与主视图一致"才有所指；007 的前置检查与此一致）；**008 依赖 003、005、006，并以 007 定下的交互为标准**（TUI 是语义标准）。
- **011 依赖 005、007、008。** 到点时经 005 的 `SendToSubagent` 继续；提示和 Esc 取消显示在 007/008 建好的 subagent 视图里。
- **012 依赖 010**：卡片事实里的任务名用 010 引入的 `tool.SubagentTaskTitle`（2026-10-05 修订：从 `pkg/agent` 挪到 `pkg/tool`，见 010 文末修订说明）。
- **013 依赖 001**：第 7 步真机验收要用 001 给 `driver.sh` 加的 `click` 命令点卡片任务行（001 也是推荐顺序的第一步）。
- **015 无前置依赖**：它实现计划 002 §5 的 `HistoryEntry` 模型字段与 `run.AgentModel`（签名多一个 `effort`）；002 执行时核对并复用，不再写第二份。015 的第 4 点：它发布评审请求的开始/结束事件，013/014 据此画 plan-reviewer 卡片（D13）；所以 013、014 依赖 015。
- **plan-reviewer 视图与其它 subagent 视图一致（owner 2026-10-05）**：逐项清单在计划 015 的"一致性清单"里。工具卡片、文字不重复、footer 模型、评审回答不再出现在主视图（评审完成时印出的卡片 R5；选"继续规划"后被拒的 `exit_plan_mode` 卡片与其回放 R6，owner 2026-10-05 再次确认这是必须修的缺陷）由 015 修；网页 subagent 视图缺带时长的 Working 行和每次执行结束的 Worked for 行（所有 subagent 都是，R7，2026-10-05 核对预览时发现）也由 015 修；两行里的清单进度（勾选图标 + `N/M` + 进行中的任务）在 TUI 已有，但被继续的 subagent 第二次执行时 Working 行消失、Worked for 时长算错（R8），网页 subagent 视图没有、网页主对话写死英文 `Tasks N/M` 且无图标（R9），同样由 015 修；主视图卡片由 013/014；它自己的上下文窗口统计 `N%/窗口` 与其它 subagent 一样由 006（按 agent、按评审模型的窗口算）、007（TUI footer 右侧）、008（网页 subagent 视图）提供，三份计划各加了一条 plan-reviewer 专门的测试，009 验收两个界面。
- **016 依赖 013、014、015**：reviewer 在两个界面上以"无派发调用的执行"卡片出现（013/014），它的工具步骤钩子从 015 的写法变成共享入口的参数。
- **013 依赖 012，014 依赖 012、013**：两个界面都只从 012 的事实和精确关联画卡片；TUI 是语义标准，网页按 013 落地后的 TUI 做呈现。
- **T23/W11 的根因修复在 SRT-004**（`docs/plan/SCHEDULED_RUNS_TRANSCRIPT/004-run-liveness-and-session-exclusivity.md`，回收器为被放弃的子运行发 `subagent_ended`；2026-10-05 已在它的第 2 节补上"`FinishedAtMs` 填运行被盖章的结束时间"）。012–014 不等它；009 的前置条件里加了 SRT-004。
- **009 最后做**，覆盖 001–008、010–016 的全部验收（第 5 步是 subagent_* 卡片，第 5b 步是 plan-reviewer）。
- **当前阻塞（2026-10-06 核实）**：SRT-002、SRT-003、SRT-005 都还是 TODO，002 的前置检查会 STOP，依赖它的 003、005、006、007、008、011 一并排不开。001、010、004 没有外部依赖，可以先做或与 SRT 并行。

## 决策（D1–D15 已全部由 owner 定夺，全部纳入本期）

| 编号 | 问题 | 选项 | 推荐 | 结论 |
| --- | --- | --- | --- | --- |
| D1 | subagent 视图里 Esc 的语义 | A 有在途输入才作用于该 subagent：撤回窗口内撤回刚发给它的消息；有发给它的待投递 steer 时中断它并立即发送；其余情况返回主视图；Esc 永不直接取消 subagent / B 完全照搬主视图（无待投递时 Esc 取消 subagent）/ C Esc 只负责返回 | A | **A**（owner 2026-10-04） |
| D2 | 哪些 subagent 能在视图里直接对话 | A 所有公开类型（含一次性类型），内部保留类型只读 / B 沿用 OneShot 限制 | A | **所有类型，包括一次性类型和内部保留类型（plan-reviewer、goal-evaluator）**（owner 2026-10-04："因为网络原因等非人为的原因造成的中断都必须支持用户直接跟该subagent对话，让该subagent继续执行"）。`OneShot`/`Continuable` 只约束模型调用 `subagent_continue`。 |
| D3 | 用户在 subagent 视图里驱动它继续后，主 agent 是否被告知 | A 不注入，主 agent 用 `subagent_status`/`subagent_wait`/`subagent_list` 自行查询 / B 在主会话尾部追加通知 | A | **A**（owner 2026-10-04） |
| D4 | subagent 视图里斜杠命令和 `!shell` 的作用范围 | A 三分类（作用于该 subagent / 照旧全局 / 隐藏并一句话提示）/ B 只开放 /compact / C 只开放 /compact，其余全拒 | A | **A**（owner 2026-10-04），逐条分类表见 007 |
| D5 | worker 会话是否参与记忆抽取 | A 不参与（subagent 做的事已由主会话里它的结论和卡片代表；参与会让后台为每个 subagent 多跑一次抽取模型调用）/ B 参与 | A | **A：不参与**（owner 2026-10-04 按推荐批准；与 SRT 的 D6"定时任务对话不参与"同理） |
| D6 | fork subagent 继续时用什么前缀 | A 原样重放它上次请求的 system 和消息（冻结 system、消息落库），前缀与它上次请求逐字节相同 / B 改走 typed 路径重建前缀 | A | **A**（owner 2026-10-04 按推荐批准；B 会让 fork 继续时的整段前缀失效，违反缓存铁律） |
| D7 | 统一队列语义（计划 004）是否纳入本期 | 按"顺带发现的既有缺陷必须纳入本期"的规矩，只能问"怎么修"：以 TUI 为准把语义收进 `run.InputQueue`，TUI 主视图和网页都只用它 | — | **纳入本期**（owner 2026-10-04 按推荐批准；工作量 L、风险高，单列为计划 004。网页上带附件的消息在运行中改为作为 steer 投递，与 TUI 一致，此变化一并批准） |
| D8 | subagent 视图里有待触发的自动继续时 Esc 做什么 | 由 owner"对齐 primary agent 的语义"和 D1 的原则推出：主视图里 Esc 取消待继续；subagent 视图里按"撤回 → 中断并发送 → 取消自动继续 → 返回主视图"的优先级 | — | **按推导的优先级执行**（owner 2026-10-04 按推荐批准，见 007、011） |
| D9 | 斜杠命令三分类里由推导得出的 9 个命令 | `diff`、`subagents`、`skills`、`connect`、`memories`、`migrate` 在 subagent 视图里照旧全局执行；`rename`、`agent`、`fast` 在 subagent 视图里隐藏并一句话提示（依据：是否改变主会话） | 按左列 | **按左列执行**（owner 2026-10-04 按推荐批准，见 007） |
| D10 | 旧会话里 `subagent_send` 的生命周期事件缺 `parent_tool_call_id`（012 的 A8：本机 9 次派发、18 条事件），怎么处理 | A 用一次**只改数据、不改结构**的状态库迁移，按"同会话 `subagent_send` 调用结果的 `run_id` = 事件的 `execution_id`"精确补上（本机只读查询核实全部唯一命中、goal check 不误中；会占用下一个 schema 版本号）/ B 不迁移：旧会话里 send 卡片永远对不上它的 subagent，停在"运行中"，旁边另有一张独立卡片 | A | **A**（owner 2026-10-05 定夺；由计划 012 第 7 步实施，占用下一个 schema 版本号） |
| D11 | 主对话里 subagent 的 "spawned / ended" 两行文字（网页是两张生命周期卡片）是否由"一次执行一张卡片"取代 | A 取代：有派发调用的执行画进派发调用那张卡片，没有派发调用的执行（plan-reviewer 等）画一张同样的卡片；卡片本身就是"对话对这个 subagent 的记述"和回到它视图的入口（任务行可点）/ B 保留这两行文字，卡片之外再各打一行 | A | **A**（owner 2026-10-05 定夺；"主视图里唯一属于 subagent 的东西"的规矩不变，只是把形式统一为与 fanout 一致的卡片，任务行可点进视图） |
| D12 | 卡片样式（终端与网页） | 以 `subagent-cards-preview.html` 为准：终端文字与 013 第二、三节逐字相同，网页与 014 第 1、5 节相同 | 确认预览 | **已确认**（owner 2026-10-05 确认 Version 2：已删统计行、任务名后带计时/最终耗时的版本；后续改动仍要同时改预览与计划并重新确认） |
| D13 | plan-reviewer 这类没有派发调用的 subagent 的卡片头怎么写（owner 第 9 点之 4：旧文案 `subagent plan-reviewer spawned plan-reviewer` 主语错误） | A 写明主语（"主 agent 启动了 plan-reviewer subagent"）/ B 不加主语，与其它 `subagent_*` 卡片同一套头部 | — | **B**（owner 2026-10-05："没有派发调用的执行 plan-reviewer的消息卡片的header里不要加主语，与其他的subagent_*工具调用消息卡片的header保持一致使用Starting/Running/Ran/Failed to start等"）。为此评审请求本身成为这张卡片的派发：015 发布 `plan_review_started`/`plan_reviewed` 并让 reviewer 的 spawned 事件以 `ReviewID` 作为 `parent_tool_call_id`，013/014 据此画 `Starting 1 plan-reviewer task…` → `Running …` → `Ran …`，开始前失败为 `Failed to start 1 plan-reviewer task` |
| D14 | 网页"退出计划模式"审批卡片的样式（计划 016） | 以预览页"退出计划模式审批（网页）"一节为准：标题、完整计划、已有评审、四个选择（批准并清空上下文 / 批准 / 继续规划 / 请其他模型评审）、评审进行中可停止 | 确认预览 | **已确认**（owner 2026-10-05） |
| D15 | 旧会话里被拒绝的工具结果行没有显示部分，回放时显示写给模型的那段（含 "Try a different approach…"，评审后拒绝的还含评审全文；本机例：`fb_messages` id 85844）。计划 015 修好新数据后，旧行怎么办 | A 只改数据的迁移：给这些行补显示部分，正文按实时卡片的规则（`exit_plan_mode` 为 `(no output)`），不回显旧的用户反馈 / B 同 A，但当拒绝理由里没有评审时，把"User feedback:"之后的用户原话作为正文（与实时卡片一致）/ C 不迁移 | B | **B**（owner 2026-10-05）。由计划 015 第 2c 步实施 |

## 全局规则（每份计划都适用，执行者必须遵守）

1. **不要提交代码。** 所有改动留在工作区，由 owner 手动审阅提交。不要运行 `git commit`、`git merge`、`git rebase`，也不要做任何改写历史的操作。
2. **缓存命中率只能升不能降，这是最高优先级。** 任何改动都不得改变会话在对话之前的那段前缀（工具表、system、对话前注入的开发者指令），也不得在会话中途改变它；新内容只能追加在消息尾部。每份计划都有"缓存影响"一节，执行者必须按它留下前后对比的证据（逐字节金样测试，或 `usage_json` 算出的命中率 `CacheRead / (CacheRead + CacheCreation + Input)`）。
3. **修根因，不打补丁。** 不加只为掩盖症状的 nil 判断、兜底、重试或 recover。真正来自外部的输入（用户输入、模型响应、文件系统、网络）除外。
4. **每个包最多 20 个生产文件。** `pkg/run`、`pkg/turn`、`pkg/tui`、`pkg/process`、`pkg/gateway`、`pkg/state`、`pkg/tool`、`pkg/llm` 现在**都正好是 20 个**（2026-10-04 核实），所以**不得在这些包里新建任何生产 `.go` 文件**，新代码只能写进已有文件。`pkg/event` 有 18 个、`pkg/agent` 有 12 个、`pkg/assembly` 有 18 个，但本计划也不在里面新建文件。测试文件必须以它覆盖的生产文件命名（`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles`）。
5. **包的扇出只能减少，`run.Runner` 只能变小。** 不得给任何包新增仓库内部包的 import（`TestFanOutOnlyShrinks`，`pkg/architecture/cache_test.go:221`）；`pkg/run`、`pkg/turn` 互不 import（`TestLayer3PackagesDoNotImportEachOther`）。`run.Runner` 现有 **39 个导出方法、25 个字段，都正好是上限**（`runnerMethods = 39`、`runnerFields = 25`，`cache_test.go:849-850`）：**不得给 `Runner` 新增导出方法或字段**。引擎对外的新入口一律写成包级函数，第一个参数是 `*Runner`（先例：`run.PrimaryModel(r)`、`run.SubagentOwnModel(r, …)`、`run.CompactionService(r, …)`）。
6. **TUI 是语义标准，共享的一律下沉到引擎。** 两个 surface 都需要的语义只实现一份，放在 `pkg/run` / `pkg/turn` / `pkg/process`，TUI 和 gateway 都调用它；冲突时以 TUI 为准。gateway 只保留传输层的事。
7. **死代码要删干净。** 开工前记录基线：`$(go env GOPATH)/bin/deadcode -tags fts5 ./... > /tmp/deadcode-before.txt`；每份计划完成后输出必须与基线一致（不得新增条目）。
8. **界面文案只写一句话。** 中英文同时加在 `frontend/src/locales/index.ts`（键名一致）。任何界面都不得显示原始 JSON。Go 要告诉网页的话带稳定代码，由网页用查看者的语言写出句子；终端照旧用英文句子。TUI 内容文本不得用 dim/faint 低亮度配色，不得用粉色/品红。
9. **subagent 的一切都属于它自己的视图。** 它说的、被告知的、关于它的确认，都只出现在它的视图里；主视图只有它的生命周期卡片。新加的任何"属于某个 agent"的帧或事件都必须带 roster key（`agent.RosterKey(taskID, agentType)`），路由按"是否带 agent"判断，不按帧类型白名单判断。
10. **不碰 `pkg/gateway/dist`。** 不要运行 `make ui` 或在 `frontend` 里运行 `pnpm build`（`frontend/vite.config.ts:50` 的 `outDir` 指向 `pkg/gateway/dist`）。网页真机验证用 `scripts/acceptance/web_e2e.sh`，它构建到临时目录。
11. **真机证明。** 涉及模型行为或屏幕呈现的改动，必须在运行中的 TUI（tmux，`.claude/skills/run-forebrain/driver.sh`）和网页上证明；单测和假模型只证明"做出来了"，不证明"真的这样工作"。真实模型用智谱：密钥只在 `~/.forebrain/e2e-zhipu.env`（变量 `FOREBRAIN_E2E_ZHIPU_KEY`，权限 600），配置里只能写 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用，任何文件、日志、截图说明里都不得出现密钥的值。缓存命中率的真机测量只要求 DeepSeek 和 OpenAI；本机没有凭据就记"凭据缺失，已跳过"，不要停下等人。
12. **状态库结构不变。** 本计划不新增表、不改列，不需要迁移。如果执行中发现必须改结构，按 STOP 处理，回来问；改了结构就必须迁移用户已有的旧库。例外是两次只改数据、不改结构的迁移：012 第 7 步（D10 选 A 时，补 `fb_session_events` 的 `parent_tool_call_id`）和 015 第 2c 步（D15 选 A/B 时，给旧的被拒工具结果行补显示部分）。

## 常用命令（2026-10-04 在本机核实过的形式）

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 格式 | `gofmt -l pkg cmd` | 无输出 |
| 静态检查 | `go vet -tags fts5 ./...` | 退出码 0 |
| 单包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<Name>' -count=1` | `ok` |
| Go 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok` |
| 竞态 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run ./pkg/tui -run '<Name>' -count=1` | `ok`，无 `DATA RACE` |
| 架构约束 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` | `ok`（2026-10-04 基线通过） |
| 包依赖图 | `scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` | 退出码 0 |
| 死代码 | `$(go env GOPATH)/bin/deadcode -tags fts5 ./...` | 与开工前的基线一致 |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端类型检查 | `cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` | 退出码 0 |
| TUI 真机（假模型） | `.claude/skills/run-forebrain/driver.sh`（用法见 `.claude/skills/run-forebrain/SKILL.md`） | 见各计划 |
| 网页真机（假模型） | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 网页真机（智谱） | `FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

本机磁盘余量很紧（2026-10-06 核实只剩约 3.8 GiB）。不要复制仓库或 Go 构建缓存；临时验证用 `go test -overlay` 或 scratch 目录里的小文件。

## 与并行工作的关系

写本计划时（2026-10-04），工作区里有一批**不属于本计划**的未提交改动（LSP 计划与定时任务计划的产物：`pkg/lsp/`、`pkg/run/*`、`pkg/turn/*`、`pkg/tui/*`、`frontend/src/*`、`docs/plan/lsp/`、`docs/plan/SCHEDULED_RUNS_TRANSCRIPT/` 等）。**2026-10-06 核实：这批改动已全部由 owner 提交为 `bda9505`，工作区干净**；`docs/plan/SCHEDULED_RUNS_TRANSCRIPT/` 的计划本身仍未实施（001–006 全是 TODO）。因此：

- 各计划的漂移检查与基线一律对比 `bda9505`；干净树上 diff 为空，列出文件即说明有新改动。
- 摘录核对按内容（函数名、注释原文），不按行号核对；对不上就按 STOP 处理。
- 002 的前置检查当前会 STOP（SRT-002/003 未做），见"依赖说明"的当前阻塞。

## subagent_* 工具卡片审计（2026-10-05）

范围：八个工具 `subagent_run`、`subagent_fanout`、`subagent_send`、`subagent_status`、`subagent_wait`、`subagent_continue`、`subagent_close`、`subagent_list`，以及没有派发调用的 subagent 执行（plan-reviewer）在主对话里的呈现；TUI 与网页、实时与回放/重载、主视图与 subagent 视图（嵌套派发）。证据来自代码阅读和本机状态库（`~/.forebrain/state/forebrain.state.sqlite`，只读）。

**结论：截图里的 JSON 只是最表面的一层。** 八个工具里只有 run/fanout 有卡片，另外六个没有任何专门的显示逻辑；卡片需要的事实只存在于一段显示文字里，两个界面各自猜；`subagent_send` 派发的 subagent 根本不知道是哪次调用派发的它。

- 共同根因（计划 012）：A1 `subagent_send` 异步执行丢了派发调用 id；A2 `subagent_continue` 的事件把本次调用 id 配上原调用的任务序号；A3 六个生命周期工具落到通用格式化器，JSON 套 JSON；A4 它们的调用标签和摘要是原始输入 JSON（网页标题就是它，含整段 prompt）；A5 没有结构化的卡片事实，TUI 去解析显示文字（永远失败），网页只能显示文字；A6 run/fanout 的正文把派发 prompt 放进主对话；A7 run 的调用标签截断；A8 旧会话的 send 事件缺关联（D10）。
- TUI（计划 013，T1–T23）：除截图两项外，还有 send 卡片报 20ms 而 subagent 跑了十几分钟、spawned/ended 文字行类型说两遍且露出原始 id；wait 超时仍写 "Agent done"；continue 把原派发卡片"复活"；`Ran 1 tasks`（测试把错的复数当成了期望值）；对不上号的 subagent 被挂到 fanout 的等待行上（按随机的"第一个"猜）；跳过的任务显示 ✓；调用失败看不到原因；取消的任务不计数；正文半亮；`ActivityStatusUpdatedMsg` 没有生产者、`TokenCount` 只写不读却让每个 usage delta 重画卡片；嵌套派发的卡片漏进主视图；空 prompt 显示成成功；回放仍显示落库的 JSON；改成卡片后中断/孤儿调用的处理；owner 看过预览后追加的两条：删掉 `· N tool uses` 统计行（T21）、每个任务名后面加运行中计时和结束后的最终耗时（T22）；以及为 T22 核对时发现的既有缺陷：上个进程没跑完的 subagent 在恢复的会话里永远是运行中（T23，根因修复就是 SRT-004 的回收器，本期只要求 `subagent_ended` 带上真实结束时间）。
- 网页（计划 014，W1–W11）：标题是原始 JSON；正文是 JSON 或整段 prompt；run/fanout 没有任务级卡片；每个 subagent 在消息末尾各有"已派发""已结束"两张卡片、与派发调用分离；卡片细节是原始 id 和 `status=ok`；嵌套派发在主对话里新建空消息；重载后显示落库的旧正文；任务名后没有计时与最终耗时（W10）；gateway 中途退出后没跑完的 subagent 永远运行中（W11，同 T23）。

设计确认稿：[`subagent-cards-preview.html`](subagent-cards-preview.html)（终端与网页并排，可切换各工具的状态，可点任务行）。已发布为私有预览页：https://claude.ai/artifact/M5CwinN7H8AhH1Qi2D2Hrw（2026-10-05 Version 1；同日 Version 2 按 owner 意见删掉统计行、加上任务计时与最终耗时；改设计时同时改这个文件并重新发布到同一链接）。

## 考虑过但不做的

- **把 subagent 账本（`<workspace>/state/subagent-history.jsonl`）和进程内注册表搬进状态库，以支持 gateway 多副本。** 不在本期。gateway 集群化的前提是状态库本身可以跨副本共享（今天是本地 SQLite），这是全局性的前置工作，不属于 subagent 对话这个特性。本期让 subagent 的对话历史和主会话一样落在状态库里，已经是"与主会话同等持久"的最好结果；账本的位置不变，不会让集群化更难。
- **把 fork 旁路 JSONL（`SidechainFilePath`）删掉。** 不做。`SubagentStop` 钩子通过 `hook.SidechainTranscriptPath` 读它，它是钩子的输入契约，不是对话历史的来源；002 之后对话历史的唯一来源是 worker 会话。
- **改 `subagent_*` 工具返回给模型的 JSON**（例如让 `subagent_status` 不再返回整段 prompt 和输出）。不在本期：那是工具与模型之间的契约，改它会改变模型看到的工具结果；本期只改呈现，模型看到的逐字节不变（012 第 6 步有测试守住）。
- **把 `FrameFanout`/`fanoutState`/`renderFanout` 改名为 subagent 卡片。** 不做：语义已经由注释说明，改名会让 013 的差异面翻倍、与 001/007/010 冲突，收益只是名字。
- **subagent 视图里的 `ctrl+c`。** 不改。它今天在任何视图都作用于主运行和 composer；owner 这次没有提出，D1 也只定了 Esc。

## 评审记录（2026-10-06，review-plan）

- 全部 11 份计划的代码引用对照 `bda9505` 复核：绝大多数 file:line、函数名、注释原文精确命中；本次修正的偏差有——001 的 `reducer.go:2928`→`:2926/:2940`；002 的 `worker_cli.go:16-75`→`:16-77`、`run.go:41`→`:43`；003 的 gateway `RunPreflight` 引用 `:514`→`:527`；005 的 D2 一次性类型补 `guardian`；008 的 `chatSessionSubagentHistory`→`sessionSubagentHistory`、`token_budget_updated` 行号→`:1137`；011 的 `handleCancelAutoContinueMessage` 行号→`:758`。基线从 `1a6d708`（写作时的 HEAD）改为 `bda9505`（摘录实际对应的提交）。
- 结构修正：依赖表补齐 005→003、007→004（与各自前置检查一致）；001 的测试计数 6→5；002 的"缓存影响"fork 引用与 005 的"测试计划第 6 条"改指真实位置（第 6 步测试 / 第 4 步第 6 条）；003 的"缓存影响"引用改指步骤编号，并补了"`CompactLLMForModel` 按显式 model 建客户端、不属于本次模型解析"的说明与第 1 步失败断言的具体形态（编译失败也算失败）；002 第 4 步测试文件的自指改为"不存在则新建"、范围清单补上条件性的 SRT README 备注；011 第 1 步的矛盾（新测试编译不过 vs 全绿验证）改为第 2 步再加该测试；004/009 把召回键（Shift+Left）与"改动前基线怎么跑"（git worktree，磁盘不足 2 GiB 时 STOP）钉死。
- 核实过且**不需要改**的声明：八个包各正好 20 个生产文件、`runnerMethods=39`/`runnerFields=25`、内置斜杠命令 26 个与 007 的分类表逐一相同、`You are a general-purpose subagent.` 识别串、driver.sh/db/web_e2e/deadcode/密钥文件权限 600、004 的 `Controller.Steer/FollowUp/Retract/DrainSteers/Preview/Queue/Release` 方法归属（controller.go:217-452，属实）。

## 评审记录（第二轮，review-plan：012–016 与 009/README 修订；本机时钟 2026-10-05）

- 五份新计划（012–016）的全部代码引用对照 `bda9505` 复核（4 路只读 subagent 并行 + 主线冷读裁决，1 路限流由主线补查）。修正的实质偏差：**012 的状态库现状过期**——实际 `stateSchemaVersion = 6`（`schema_migrations.go:16`）、迁移表 V1–V6（最近一条 `migrateStateV5ToV6`，V4→V5 给 `fb_runs` 加 `owner`），计划写的 4/V3→V4；设计第 6 节"下一个版本号"的例子改为 `migrateStateV6ToV7`（版本无关的"以最大版本 +1 为准"原本就在，不影响可执行性）。012 的 `TestRunWorkedLinesCloseEachRunAfterItsRun`→`...AfterItsLastRow`。013 的 `toolInvocationParts` 函数名不存在→实际 `toolDisplayParts`（`render.go:2513`，八个 subagent 分支 `:2866-2959` 在其中）；T3 补"target 先取 `title`、`description` 是回退"；T21 注明 `:1761` 的 `… +N tool uses` 第三层保留。013 依赖补 001（第 7 步真机用 `$D click`）。009 自相矛盾修正："八个要求"→九个（第 5b 步是要求 9），完成标准"第 1–5 步…要求 1–8"→"第 1–5b 步…要求 1–9"。015 的 R9 `PlanUpdatedPayload` 字段清单改为"有 `Title`/`Items`/`Completed`/`Total`/`Explanation`/`AgentID`，没有 `Active`"（论点不变）。README：013 行依赖补 001、依赖说明加一条、"009 覆盖 010–014"改为 010–016、"2026-10-06 提交 bda9505"按 git 时间戳改为 2026-10-04 深夜。
- 漂移检查与范围对齐（模板要求）：012 补 `pkg/event/run_events.go`；013 补 `pkg/tui/chat_surface.go`；015 补 `pkg/event/plan_progress.go`、`pkg/run/orchestration_llm.go`、`pkg/tool/request_permissions.go`、`pkg/tool/format.go`、`pkg/turn/subagent_approval.go`、`pkg/gateway/api_extra.go`、`pkg/state/schema_migrations.go`、`RunWorkedLine.vue`、`ChatView.vue`、`locales/index.ts` 共 10 个。
- 行号修正（内容属实、仅漂移）：012 `RunEventFromStep :31`→`:19`、server.go `:1796/:1819`→`:1815/:1838`（事件投影另有 `:1911`）、`handleChatMessages :1139`→`:1155`、迁移测试 `:1302`→`:1303`；013 `replayTimelineWithReducer :248`→`:247`、commands.go SubagentEndedMsg `:793`→`:801`；014 api.ts `toCamelCase :1073`→`:1100`；015 `completeSurfaceApproval :496`→`:441`、`notifyToolApprovalDenied :1012-1019`→`:976` 一带、gateway approval.go `:415`→`:422`、run.go ReplaceAll `:1068`→`:1070`、commands.go SubagentSpawnedMsg `:787`→`:795`、`ChatView.vue :440-446`→`:440-448`。
- 核实过且**不需要改**的声明（含推翻 subagent 误报两处）：`renderStatus` 存在（`render.go:5468`，`*Renderer` 方法，244 灰）——核对时 grep 漏了方法接收者形式；`fanoutFrameForTest`（`chat_session_test.go:21556`）与 `TestRosterCursorFollowsTheViewOpenedByClickingACard`（`:21737`）在 `bda9505` 已存在（001、013 都是引用既有测试）。016 全部引用精确命中：六个守门测试（`:4148/:10693/:12482/:12535/:12556/:12601`）、`buildExitPlanChoices`（overlays.go:267）、`serve_run.go:79`、`withDetachedGatewayApprovalHooks`/`promptGatewaySubagentApproval`（approval.go:25/:243）、`ListSessionEventsOfType`（session_events.go:223）、`PlanPathForProject`（plan.go:41）、`ToolApprovalDecision.RequestPlanReview`（model.go:566）、TUI_FIRST_RUNTIME_REFACTOR.md:1402 引文、pkg/process 同时 import run+turn 且 20 个生产文件、`POST /subagents/:id/cancel`。其余：run_test.go 恰 3 处 `installRunAuditStepHook`；`subagentModelsByType` run.go:1059；reducer_test.go `:587/:1143` 断言原文；012 A1/A2 摘录逐字命中（subagent.go:1487-1499/:886/:1233-1238，TaskIndex 未置 0）；`pkg/tool` 不 import `pkg/agent`；`PartTypeToolDisplay` 不进模型消息；八个工具返回形状；`prep.ctx` 由 `baseCtx` 派生；`reasoningEffortFromParams` 在 runner.go:1604（015 未声称位置）。预览页 `subagent-cards-preview.html`：两处 "tool uses" 一处是"不再出现"说明、一处是保留的第三层 `… +N`；退出计划模式审批节与四个选择、任务计时/最终耗时俱在。
