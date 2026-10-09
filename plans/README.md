# Forebrain Harness 改进计划索引

本目录包含由 `improve` skill 生成的代码改进计划。每个计划都是自包含的实施指南，可由其他执行器模型独立完成。

## 最新审核信息

- **审核日期**：2026-10-09
- **基准 commit**：71c0449
- **审核类型**：多个 TUI 进程同时在 plan 模式时 exit_plan_mode 审批串台的根因定位与修复（`plan` 模式，单计划 004）
- **问题报告**：owner 报告"启动多个 tui 进程，不同进程里分别同时进行 plan 模式下的会话，其中一个会话调用 exit_plan_mode，审批 overlay 里会出现其他 tui 进程里的会话生成的计划文件"
- **Owner 裁决（004）**：隔离方案选「每会话子目录」`plans/<projectKey>/<sessionID>/`（评估过保持平铺的后缀命名 / 绑定文件名 / SQLite 归属表后仍维持子目录）；旧会话不迁移（扁平目录里的旧计划只作历史）；`/migrate` 导入的计划进入所属对话的目录，一个 Claude slug 对应多个会话时复制到每个会话；删会话不碰计划目录
- **上一轮（003）**：TUI message queue 在途 steer 无法撤回；owner 裁决：压缩窗口选「压缩不吞未送达 steer」；「清空队列对在途 steer 不撤回」和 web/gateway 同类问题并入本期一起修，web 送达的 steer 实时拆成独立气泡

## 计划列表

| # | 标题 | 类别 | 状态 | 优先级 | 工作量 | 风险 |
|---|------|------|------|--------|--------|------|
| [001-fix-message-recall-after-detach](001-fix-message-recall-after-detach.md) | 修复 runtime detach 后消息无法撤回 | Bug Fix | DONE | **HIGH** | S | LOW |
| [001-frontend-component-design-spec-alignment](001-frontend-component-design-spec-alignment.md) | 前端通用组件样式规范对齐 | Design System | TODO | HIGH | M (4-6h) | MEDIUM |
| [002-subagent-view-input-freeze-ledger-read](002-subagent-view-input-freeze-ledger-read.md) | subagent 视图的 composer 重绘不再整读 subagent 账本（修复快速滚轮后输入卡死） | Bug Fix / Perf | DONE | **P1** | S | LOW |
| [003-recall-in-flight-steer](003-recall-in-flight-steer.md) | 队列里显示的 steer 在模型开始回答前随时能撤回、清空时一并撤走，web/gateway 与 TUI 一致 | Bug Fix | DONE | **P1** | L | MED |
| [004-session-scoped-plan-files](004-session-scoped-plan-files.md) | 计划文件按会话隔离：多个 TUI / 会话同时在 plan 模式时 exit_plan_mode 审批不再显示别人的计划 | Bug Fix | DONE | **P1** | M | MED |
| [005-serialize-run-queue-hook-and-drop-retract-last-steer](005-serialize-run-queue-hook-and-drop-retract-last-steer.md) | gateway 的 run 队列 hook 一次只发一个变化；删掉已无人使用的 `RetractLastSteer` | Bug Fix / Tech Debt | DONE | P3 | S | LOW |
| [006-shared-compaction-guard-and-unseparable-provisional-tail](006-shared-compaction-guard-and-unseparable-provisional-tail.md) | 排查 Runner 共享的压缩 guard 会否被同 run ID 的另一段历史覆盖；不可达则让分不清暂定尾巴的调用不压缩 | Investigation | TODO | P3 | S | LOW |

## 执行顺序

推荐按以下顺序执行：

```
003-recall-in-flight-steer (独立，无依赖，owner 报告的严重 bug，优先)
004-session-scoped-plan-files (逻辑上独立；与 003 改到同几个文件 api_extra.go / orchestration_llm_test.go，等 003 提交后再执行，或在基于 HEAD 的独立 worktree 里执行)
001-frontend-component-design-spec-alignment (独立，需用户确认命名策略)
005-serialize-run-queue-hook-and-drop-retract-last-steer (依赖 003 的代码在工作区或已提交；与 004 不重叠)
006-shared-compaction-guard-and-unseparable-provisional-tail (依赖 003；排查计划，Step 4 是决策闸门，可达时停下交 owner 拍板；与 005 不重叠，可并行)
```

## 状态说明

- **TODO**：待执行
- **IN_PROGRESS**：执行中
- **BLOCKED**：被阻塞（等待决策或依赖）
- **DONE**：已完成
- **REJECTED**：已拒绝（说明原因）

---

## 最新审核发现总结（2026-10-09）

### branch 审核：003 在工作区里的实现（未提交，基于 `71c0449`）

**结论**：没有阻塞问题。实现与 003 计划一致；核心不变式经逐段核对成立（steer 恒在 `pending` 或某个未结算交付里、同一把 `rt.mu`；
「先 Commit 再查 `Retracted()`」两处顺序正确；`Recall`（`q.mu`→`rt.mu`）与 `Commit`（释放 `rt.mu` 后才进 `q.mu`）无锁序反转；
提醒类包装都在 `recoverableLLM` 里面，暂定尾巴的长度校验在今天的链路上成立；压缩的摘要请求会屏蔽 `OnResponseStarted`；
三个流式 provider 都触发 `NotifyResponseStarted`；`TakeAll`/`Next` 只在不可能有在途交付的位置调用）。
验证：`go vet`、`gofmt` 干净；`go test -race ./pkg/run`、`./pkg/gateway`、`./pkg/turn`、`./pkg/event`、`./pkg/architecture` 通过；
`./pkg/tui` 仅 `TestCharacterizationNetworkApproval` 因真实访问 example.com 偶发失败、单跑通过；两个前端测试文件 108 个用例通过。
未做：全量 `go test ./...`、TUI 真机 Step 12、web 浏览器复核清单（003 Maintenance notes ①②③，需 owner 手动）。

| # | 发现 | 归属 | 影响 | 计划 |
|---|------|------|------|------|
| 1 | gateway `watchRunQueue` 的 hook 在多个 goroutine 上并发运行且无互斥：别的请求的 hook 可能抢走刚送达的 steer，`input_delivered` 晚于回答首个 delta 落库，web 实时把回答开头画进上一个气泡；旧预览也可能最后落库 | 本分支引入 | 低（窗口极窄，仅 web 实时，刷新恢复） | 005 |
| 2 | `TurnInputRuntime.RetractLastSteer` 已无生产调用方，且看不到在途交付——003 自己的维护说明把它列为回归陷阱 | 本分支引入（变成死代码） | 低 | 005 |
| 3 | 撤回路径裁剪 adoption 依赖「暂定尾巴被遵守」；`applyRunScopedCompact` 的防御分支会整段压缩并折入 steer。探针证实：共享的单槽 `compactRunGuard` 在同一 run ID 下会把另一段历史的 checkpoint 拼进父请求；尾巴不被遵守时撤回会把 checkpoint 裁掉。生产可达性未定（线索：`own.RunRT == nil` 时 fork 子任务沿用父 run ID，且 fork 链与主链共用同一个 `recoverableLLM`） | 防御分支本分支引入；共享 guard 为既有 | 今天的链路上未见触发；若可达则是更大的既有压缩串台 | 006（排查） |

### 类别：Correctness — 多进程 / 多会话 plan 模式的计划文件串台

**严重程度**：**HIGH**

**根因**：「当前计划」= `<stateRoot>/plans/<projectKey>/` 下 mtime 最新的 `.md`（`pkg/state/plan.go` 的
`PlanPathForProject` → `newestMarkdown`），解析不带会话 ID。同一项目里的所有 TUI 进程、gateway 会话、/fork 出来的会话
共用这个扁平目录，谁最后写计划，谁的文件就成了所有会话的当前计划。同一个函数还决定了：批准后告诉模型去实现哪个文件、
计划评审读哪个文件、每轮 plan-mode 提醒里的 current、实现阶段的提醒、`/plan`、`/status`、web 的 plan-md 与审批卡、
shell 的 `FOREBRAIN_PLAN_FILE`。

**真机证据**（driver `toolcall` 模式，HEAD `71c0449`）：会话自己从没写过计划，在项目计划目录里放一份「别的进程的计划」后，
exit_plan_mode 审批框显示了这份外来计划；批准后工具结果是 `Implement the approved plan in …/other-process-plan.md`。

**修复**：见 `004-session-scoped-plan-files.md`——计划改放 `plans/<projectKey>/<sessionID>/`，会话 ID 一律取对话 ID
（`tool.PlanSessionIDFromContext`，subagent 写进父对话的目录）；所有读写入口切到会话版本，删掉按项目解析的旧 API；
/fork 复制源会话的计划；plan 模式下写不进别的会话目录（`isSanctionedPlanWrite` 只放行可写目录正下方的文件，无需修改）；
`/migrate` 把导入的计划写进 `cli-<id>` 会话目录（slug 对应多个会话时每个一份）；`RemoveSessionStateFiles` 有意不删计划目录。

### 类别：Correctness — 在途 steer 显示在队列里却无法撤回

**严重程度**：**HIGH**

**根因**：steer 在工具边界（或无工具调用的最终回答处）被 `TurnInputRuntime.BeginSteerDelivery` 从 runtime 的
`pending` 取走、拼进下一次模型请求，但要等该请求的**首个输出事件**才 `Commit`、才离开 `InputQueue.steers`
（队列预览的数据源）。这段窗口里消息仍显示在队列中，`InputQueue.Recall` 却用只查 `pending` 的
`RetractLastSteer` 去撤回，必然失败并跳过整条 steer 车道 → Shift+← 无响应，首 token 到达后消息自动显示为已发送。
窗口 = 下一次请求的首 token 延迟，若该请求触发中途自动压缩还要加上整段压缩时间（压缩在 `recoverableLLM` 里，
处于同一次调用之内）。现有子测试 `TestInputQueueRecallPicksNewestByLane/unretractable steer is skipped` 把这个行为钉死了。

**同类缺口（owner 要求并入本期）**：切换会话时 `InputQueue.Discard` 清空旧会话队列，但同样只撤得回 `pending`——
在途 steer 只从预览消失，旧 run 照样把它交给模型，Commit 时队列里已找不到它，模型回答了一条从未进 transcript 的消息；
runtime 已 detach 时 `Discard` 还不计 steer，「queued input discarded」提示少报。

**web / gateway 的同类问题（owner 要求并入本期）**：
- gateway 的主会话与 subagent `edit_last` 都走 `InputQueue.Recall`，同一个在途窗口；引擎修复覆盖，补 gateway 测试锁住。
- web subagent 视图的队列预览只在 HTTP 操作后推送：steer 送达、执行结束后预览不更新，已送达的消息一边显示成用户消息、一边挂在队列里，按撤回必失败（持久化事件回放后依旧）。修法：引擎 `SubagentSurface.OnQueueChanged` + 通道的队列 change hook，gateway 据此推预览。
- web 主会话里 steer 送达后从队列消失、却不进实时对话（TUI 画成 you 消息），要刷新才可见；gateway 会话队列的 delivered 列表只增不减。修法：新增持久事件 `input_delivered`，gateway `watchRunQueue` 取走 delivered 并先于预览发出；web 实时收尾当前回答、插入用户气泡、开新回答气泡（与刷新后的回放一致）。
- web 的 Shift+← 只在本页自己的 run 流式时生效：主会话空闲的 subagent 视图、刷新后进行中的 run，队列可见却按键无效。修法：输入框按「队列可见」放行，`editLastQueuedMessage` 捕获 409。

**顺带发现的潜伏 bug**：中途压缩的输入是整个 session（含在途 steer），checkpoint 落库后若这次请求失败，
`Rollback` 把 steer 放回队列由 turn 边界重发——checkpoint 里一份、重发一份，重复。

**修复**：见 `003-recall-in-flight-steer.md`——runtime 登记未结算的在途交付，新增 `RetractSteer(seq)` 可从在途交付里
拿回 steer 并中止承载它的调用；`Recall` 与 `Discard` 都改用它；被撤回过的交付拒绝 Commit，编排循环丢弃该调用结果、去掉撤回的 steer 后自动重发；
中途/被动压缩只 checkpoint 暂定消息之前的历史，并在不受撤回影响的 ctx 上运行。三个撤回入口（TUI、subagent 视图、
gateway `edit_last`）都走 `InputQueue.Recall`，引擎层一处修复全部生效。

### 类别：Performance / Correctness — subagent 视图输入卡死

**严重程度**：**HIGH**

**根因**：subagent 视图里每处理一次输入事件，TUI 主 goroutine 都会重绘 composer，而 composer 的队列预览
`run.SubagentInputPreview` 经 `agent.GetMerged → ListHistory` **整文件读取并逐行 JSON 解码**
workspace 级账本 `state/subagent-history.jsonl`（owner 环境 3.4 MB / 448 条，只增不减，约 30 ms/次）。
快速滚轮产生上千事件，每轮主循环付 30 ms，事件在输入管线积压 → 输入冻结数秒，之后积压的操作被快速执行。
主视图的预览读内存队列，不受影响。

**证据**（tmux + owner 真实 `~/.forebrain`，同一进程对照）：N=1000 个滚轮事件后探针回显延迟，
subagent 视图 3.97 s，主视图 0.11 s；`sample` + `go tool addr2line` 显示主 goroutine 栈 124/124 落在
`renderComposerWithState → … → agent.ListHistory → json.Unmarshal`。

**修复**：见 `002-subagent-view-input-freeze-ledger-read.md`——给进程内 subagent channel 表加
(会话 id, task id) 二级索引，预览 / 撤回 / Esc / 丢弃 / running 判断只查内存，不再碰账本。

---

## 历史审核记录

### 2026-10-08 - TUI message queue bug（Shift+← 无法撤回）

**根因**：`pkg/run/turn_input.go` 的 `InputQueue.Recall()` 把 `q.rt == nil`（runtime 已 detach）误当作
"不可撤回"，跳过整条 steer lane。runtime detach 后消息仍在 `InputQueue.steers`（未交付），可直接撤回。

**计划**：`001-fix-message-recall-after-detach.md`（已 DONE；单测 `TestRecallSteerAfterRuntimeDetach`，
TUI 真机：提交 → 等待 → Shift+← 撤回）。

### 2026-10-08 - 设计稿与实现一致性审核（通用组件样式）

**审核范围**：`frontend/src/assets/main.css` vs `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`

**发现**：设计稿定义的通用组件规范（`.btn`, `.field`, `.badge`, `.sw`, `.card`）与 `main.css` 实现存在三类问题：

1. **命名空间不一致**：设计稿用简短类名（`.btn`），实现用前缀类名（`.forebrain-btn`）
2. **数值偏差**：按钮、输入框的 `border-radius` 和 `padding` 不匹配
3. **组件缺失**：`.badge`、`.sw`、`.card` 及部分变体完全缺失

**计划**：见 `001-frontend-component-design-spec-alignment.md`（需用户确认命名策略）

---

## 已审核但未计划的发现

- **会话内容落在磁盘文件里，而非只在 SQLite**（owner 2026-10-09 定方向："所有会话消息无论是 primary agent
  的还是 subagent 的，都不应该存在磁盘文件里，都应该只存在 sqlite 数据库里"；同日裁决"本期先优先解决
  subagent 视图卡死，其他放在以后处理"）。已盘点到的落盘位置（均在 `<workspaceRoot>/state/` 下，
  owner 环境 2026-10-09 体量）：
  - `subagent-history.jsonl`（3.4 MB）——`pkg/agent/subagent_history.go`，subagent 任务全文与输出；
    读写方遍布 `pkg/run`、`pkg/tui`、`pkg/gateway`、`pkg/turn`。002 只把它移出每事件热路径，迁库待下期。
  - `fork-sidechain/<sid>/*.jsonl`（9.3 MB）——`pkg/hook/dispatch.go:633`
  - `hook-transcripts/<sid>.jsonl`（62 MB）——`pkg/hook/dispatch.go:640`（hook 协议的 transcript_path，迁库涉及兼容性取舍，需 owner 拍板）
  - `intermediate/<sid>.md`——`pkg/state/intermediate.go:14`
  - `cli-input-history.txt`——composer 输入历史
  - 迁库需按仓库约定为用户旧数据写迁移（state 库当前 `user_version = 8`，账本按 agent workspace 分布、
    state 库在 home 级，导入旧 jsonl 只能在运行期做，不能放进纯 SQL 的 schema 迁移）。

## 已考虑并拒绝的发现

- **在 `InputQueue` 引擎层统一串行化所有 change hook**（branch 审核，2026-10-09）：能顺带覆盖 TUI 与 subagent 通道，但 subagent 的 hook
  会在持有通道锁时被调用，要重新论证锁序；问题只在 gateway 有可见后果，005 只在 `watchRunQueue` 里加锁。
- **subagent 队列 hook 在每次执行 Attach/Detach 都落一条 `pending_input_updated`，且 HTTP 处理函数还会再发一次**（branch 审核）：
  事件日志多几行空预览，主会话早就是同样模式，不值得单独改。
- **`Discard` 与「已 Commit、队列尚未收到通知」的瞬间竞争**（branch 审核）：被丢的 steer 已在 session 里、会进 transcript；`Discard`
  只在切换会话时调用，离开的视图本就不再渲染它，回来时从 transcript 回放。既有行为，不改。
- **web 一次送达两条 steer 时两条用户气泡之间留一个空回答气泡**（branch 审核）：核实后不会显示——被收尾的空气泡 content 置空、无 blocks，
  `messageHasBubble` 返回 false。

- **计划文件「会话绑定」方案（扁平目录不变，在 `state/modes/<sid>.json` 里记会话写过的计划文件名）**：改动更集中，
  但两个会话起同名文件时仍会串台，还要额外处理 subagent 并发写绑定。owner 2026-10-09 选了「每会话子目录」。
- **旧会话迁移 / 回退（从 transcript 回填绑定、或无绑定时回退到扁平目录里最新的文件）**：owner 2026-10-09 裁决不迁移；
  回退会让旧会话继续串台。
- **保持平铺目录的串台修法——文件名带会话后缀 `<name>--<指纹>.md`、在 SQLite 里记计划文件归属**：都能修好，但前者让文件名带上
  不可读的后缀、模型写错名要被拒一次，后者要升 schema 并把数据库句柄传进 planModeLLM / shell / 工具等多处。owner 2026-10-09 评估后维持每会话子目录。
- **删会话时清理它的计划目录**：owner 2026-10-09 裁决删会话不碰计划目录，计划作为历史保留（004 只在 `RemoveSessionStateFiles` 注释里写明）。

- **在途 steer 一被取走就移出队列预览（显示为「发送中」）**：能让「显示即可撤回」字面成立，但 owner 要的是能撤回；
  且 `SteerDelivery` 的设计本意就是失败时把 steer 留在队列里由边界重发，提前移出会重新引入「显示已发送、实际没人回答」的问题。
- **「checkpoint 落库即视为送达」处理压缩窗口**：改动小，但 PostCompact hook 执行期间撤回存在竞态，已撤回的话可能残留在
  checkpoint 里，且压缩中撤回会白做一次压缩、重跑 PreCompact hook。owner 2026-10-09 选择了「压缩不吞未送达 steer」。

- **"只在裸终端复现、终端排水阻塞 paint 写入"**（a7151f5 asyncPaintWriter，已于 2026-10-09 回滚）：不是本卡死的根因——
  tmux 里稳定复现，且同一进程主视图正常。此前 tmux 复现失败是因为 driver 隔离 home 的账本几乎为空。
- **滚轮 release/motion 报告被重复计档**（b7eda22，已回滚）：只影响滚动行程，与输入冻结无关。
- **transcript 越长 `renderViewport` 越慢**：被对照推翻——主视图 557 条消息 1000 事件 0.11 s，subagent 视图消息更少却 3.97 s。
- **给 `agent.ListHistory` 加 mtime 缓存/增量解析**：账本在每次 subagent 执行开始和结束都追加，缓存频繁失效；
  且 owner 已定迁库方向。002 让热路径根本不读账本，更彻底。

## 下一步

1. **优先执行**：`003-recall-in-flight-steer`（owner 报告的严重 bug，含「清空队列」与 web/gateway 同类缺口；web 部分完成后需 owner 按计划里的清单在浏览器复核）
2. **紧接着执行**：`004-session-scoped-plan-files`（owner 报告的严重 bug：多进程 plan 模式审批串台；003 提交后执行，Step 9 是真机验收）
3. **用户审阅**：`001-frontend-component-design-spec-alignment`，确认命名策略
4. **下期**：会话内容落盘文件全部迁入 SQLite（见「已审核但未计划的发现」），先由 owner 确定范围（尤其 hook transcript 的兼容性）
5. **低优先级收尾（003 的 branch 审核）**：`005`（小修，S）与 `006`（排查，Step 4 若判定共享压缩 guard 可达会停下来等 owner 拍板）；两者互不重叠，可在 003 提交后任意顺序执行

---

**索引更新时间**：2026-10-09
**Improve Skill 版本**：按 improve skill 模板生成
