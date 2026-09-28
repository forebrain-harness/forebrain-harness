# Subagent Live / Resume 一致性与工具审批修复计划

日期：2026-09-05（实施记录更新：2026-09-07）  
状态：代码修复和自动化回归已完成；真实浏览器/PTY、参考硬件性能采样及发布回滚演练仍是发布前人工门禁，详见第 11 节。  
范围：subagent runtime、TUI、gateway/Web、session 历史回放、连接恢复、工具审批及其持久化恢复。

## 1. 目标、边界与完成定义

### 1.1 必须达到的产品结果

1. 同一 session 在同一 surface 中，resume/replay 后完整恢复 live 中的主会话和所有 subagent 会话：消息内容、顺序、卡片类型、归属、工具输入输出、生命周期、审批过程及可用交互一致。
2. 主会话中的每个 subagent 卡片都可以进入对应子任务的独立视图。TUI 使用已有 alt-screen VM；Web 使用对应的独立 conversation。不能出现点击 A 进入 B、卡片存在但视图为空、完成后卡片消失。
3. 子任务视图可读取全部已持久化历史，包含初始任务、continue 指令、reasoning、assistant、工具调用、审批请求和决策、最终结果、错误与取消；长历史通过分页完整可达。
4. 连接中断不等于任务结束。父任务结束不等于后台 subagent 结束；审批等待不等于 run 完成。
5. 审批允许、拒绝、取消及不同授权范围在 TUI/Web 具有一致语义；进程重启后可以找回待决请求和恢复上下文，不重复启动同一 continuation。
6. context compact/clear 只改变模型后续输入，不隐式删除用户可浏览的历史。

### 1.2 “完全一致”的可测试定义

- 对同一持久化事件前缀 E，在同一渲染配置下，`Normalize(ProjectLive(E)) == Normalize(ProjectReplay(E))`。
- 比较内容包括：主/子 VM 的完整 block 序列、稳定 ID、所属 agent/turn/tool/action、最终状态、正文与展示元数据、卡片入口、可展开详情、审批决策和历史时间信息。
- 只允许归一化随机测试 ID、当前墙钟和动画瞬时相位；不得排除消息顺序、工具状态、错误、reasoning、usage、审批、子任务归属来让测试通过。
- TUI 与 Web 不要求像素布局相同，但要求显示相同事实、相同历史和同义操作。每个 surface 内部的 live/replay 应使用同一套渲染组件和交互规则。
- 活跃 run 的 resume 必须恢复当前 running/waiting 状态并继续接收增量；终止 run 的 replay 不得重新启动计时、执行工具、发送消息或弹出可执行的旧审批。
- 展开/折叠、滚动位置、follow、选区、当前子视图属于浏览状态，单独建模。切换视图应保存各视图状态；正常关闭后 resume 恢复已保存状态。进程崩溃后至少恢复最后一次持久化的浏览状态。历史数据不能因当前折叠状态而截断。

### 1.3 兼容性和范围边界

- 新版写入的完整数据必须满足严格一致性，不以“显示摘要”代替完整子会话。
- 对旧数据，优先组合原始 transcript、run steps、worker session、fork sidechain 和 ledger 进行恢复；从未保存的正文、时间或事件不能凭空还原。无法恢复的部分应明确标记，不伪造完成状态或消息。
- 默认授权范围目标：会话授权属于用户看到的 conversation；worker session 只承担执行历史隔离。运行/单次授权仍绑定具体 run/tool/action，不能扩大到兄弟任务或其他 conversation。
- 不在本次修复中顺带重构无关模型策略、工具目录、sandbox 后端或网站文档子模块。
- 与根目录已有的 `SANDBOX_APPROVAL_HANDOFF.md`、`TUI_FIRST_REFACTOR_TASKS.md`、`TUI_FIRST_REFACTOR_PROGRESS.md` 协调；旧文档的“已完成”不能替代当前源码复现和本计划验收。

## 2. 问题追踪表

编号 F01–F22 对应前次源码审查的 22 项问题。优先级沿用审查结论；实施时先建立最小复现，记录实际可达路径和受影响的版本。源码审查推导不等于已经进行过浏览器、PTY 或崩溃复现。

| 编号 | 优先级 | 问题 | 修复任务组 | 关键验收 |
| --- | --- | --- | --- | --- |
| F01 | P1 | TUI replay 未恢复子 VM，且清空 AgentID | T03、T05 | 恢复后逐卡片进入正确的完整子视图 |
| F02 | P1 | 已完成 fanout replay 被识别为 start | T02、T05 | completion-only 回放为终态，无假 running |
| F03 | P1 | Web resume 未恢复 subagent 卡片/blocks | T03、T06 | 重载页面后卡片及全部子历史存在 |
| F04 | P1 | Web 用模型上下文作为历史，且截断 | T03、T06 | compact/clear/长历史仍完整可达 |
| F05 | P1 | Web 历史 API 丢失展示及关联字段 | T01、T03、T06 | 工具、附件、计划和引用能重建 |
| F06 | P1 | live 子任务展示事件未完整持久化 | T01、T02 | 从磁盘恢复的投影等于 live |
| F07 | P1 | 重连只回放父 run、缺任务文本、有事件上限 | T02、T03、T04 | 子树和全部分页可恢复 |
| F08 | P1 | 历史读取与实时订阅之间存在丢事件窗口 | T04 | 边界处新增/完成事件不丢失 |
| F09 | P2 | Web 重连回放不幂等，重复卡片 | T04、T06 | 任意重连次数不重复消息/usage |
| F10 | P1 | fanout 按到达顺序绑定子任务 | T01、T02、T05 | 乱序及空任务仍正确导航 |
| F11 | P1 | Web 收到就地审批即关闭流 | T04、T06、T07 | 审批前后连接和后续消息连续 |
| F12 | P1 | fork 重试丢失审批 resume context | T02、T07 | 允许/拒绝上下文进入真实执行 |
| F13 | P1 | async/continue 未使用统一审批恢复循环 | T02、T07 | 各派发入口具备相同审批语义 |
| F14 | P1 | 删除 durable wait 导致崩溃后无法恢复 | T07、T08 | 决策各阶段重启不丢等待、不重复恢复 |
| F15 | P1 | session 授权落到 worker，和 UI 语义不一致 | T01、T07 | 父/兄弟任务范围正确，跨会话不泄漏 |
| F16 | P2 | Web 审批跨会话混排、同类型请求难区分 | T03、T06、T07 | 按 conversation 查询并导航到 agent |
| F17 | P2 | continue 缺新一轮生命周期和 prompt | T02、T05、T06 | 多轮任务完整、状态正确 |
| F18 | P2 | session 切换出现异步旧响应覆盖 | T06 | A→B 乱序返回不污染 B |
| F19 | P2 | Web 删除只含 subagent 卡片的消息 | T06 | 无父回答、报错或取消仍保留卡片 |
| F20 | P2 | async 生命周期长于 Web 连接，后续不可见 | T04、T06 | 父完成后子任务仍可实时观察/重连 |
| F21 | P2 | waiting/cancelled/failed 状态语义不完整 | T01、T02、T05、T06、T07 | 所有 surface 展示一致 |
| F22 | P2 | 审批未进入可回放子会话时间线 | T01、T02、T05、T06、T07 | 请求、决定、拒绝反馈和取消可回看 |

## 3. 设计约束与职责划分

### 3.1 身份模型

具体 JSON/Go 字段名在 T01 定稿，以下为必须表达的语义。兼容已有 `AgentID`/roster key，不能简单把旧字段重新解释成另一种 ID。

| 字段语义 | 作用 | 不允许的替代方式 |
| --- | --- | --- |
| conversation_session_id | 用户看到的根会话、订阅与会话授权范围 | 用 worker session 冒充 |
| worker_session_id | 子 agent 独立执行历史 | 用父 transcript 承担所有子历史 |
| agent_id / task_id | 跨 live/replay 稳定的卡片和子 VM 身份 | 用 subtype 或到达位置绑定 |
| run_id / execution_id | 一次执行/continue 尝试的身份 | 只靠 agent_id 去重所有轮次 |
| parent_agent_id / parent_run_id | 派发归属和子树关系 | 将所有生命周期都归到当前主回答 |
| parent_tool_call_id / task_index | fanout 任务和具体卡片的精确对应 | 第一个 waiting 任务 |
| message_id / block_id / tool_call_id | 消息与工具 start/completion 关联 | 文本内容或数组下标 |
| action_id / decision_id | 审批请求与决定关联 | 当前可见 overlay 的内存对象 |
| event_id / conversation_seq | 去重、分页、断线游标和跨子任务顺序 | 时间戳排序、父 run 局部 seq |
| schema_version / occurred_at | 协议兼容与原始时间 | replay 时生成新的历史时间 |

### 3.2 事件、存储和投影

- `pkg/event`：定义展示事件契约和版本；表达主/子身份，不依赖 TUI/Vue。
- `pkg/state`：保存原始历史、展示事件、快照、分页游标和 durable approval 状态，落实 ownership 查询边界。
- `pkg/run`、`pkg/process`：所有派发入口产生一致事件，携带恢复 context，明确 parent/worker scope。
- `pkg/turn`：共享历史查询、事件投影语义、审批决策与恢复协调；不能再由两个 surface 各自解释审批状态机。
- `pkg/tui`：将 canonical 事件映射到 reducer/VM，通过同一渲染路径处理 live 与 replay。
- `pkg/gateway`：提供 snapshot、增量订阅、历史与审批 API；连接只负责观察，不拥有执行生命周期。
- `frontend/src`：session 级状态与订阅、稳定 block 投影、现有组件渲染、独立子视图导航。
- Go 与 TypeScript 可保留各自投影实现，但必须消费同一协议和共享 fixtures；不要求把渲染代码跨语言共享。
- 展示事件不写回模型 prompt。worker 原始 transcript、模型上下文投影和 UI 投影明确分离。
- 事件应在 durable commit 后被发布；跨存储更新使用事务或可恢复 outbox。不得正常展示一个声称可恢复但未被保存的事件而不报告写入失败。
- 高吞吐 delta 可批量持久化，但必须保持正文和边界。工具/审批/lifecycle/终态是 flush 边界；不能采样丢正文。完整 live/replay 等价要求已发布前缀可恢复。

### 3.3 审批状态和恢复原则

- 审批 action 的 pending/approved/denied/cancelled/expired/answered 与 run 的 running/waiting/terminal 分开建模。
- 用户拒绝工具调用：把拒绝及反馈交给原子 agent，让其决定下一步；不能统一转换成任务失败。
- 用户取消：终止被明确指定的作用域。子任务卡片/子审批取消目标是该子任务；全局取消目标是父 run 及受其管理的子任务。fanout 的 fail-fast 传播遵循配置并明确显示。
- `user_interaction` 的 answered、网络 allow/deny、权限 grant 等结果必须做类型化归一化，不能仅用 `ActionApproved` 判断所有交互是否成功。
- durable wait 保留 snapshot/reference、原 tool-call、agent/run/session 身份以及恢复阶段；禁止通过删除 wait 来代表“现在由内存循环恢复”。
- 决策持久化、授权生效和 continuation claim 必须可恢复并有幂等键；多 tab、HTTP/WS/TUI、进程重启只能有一个有效恢复执行者。
- 不能对任意外部副作用承诺 exactly-once。对“工具已执行但结果未持久化”的崩溃窗口，应记录结果不确定并按工具幂等/对账能力恢复，禁止默认再次执行。
- 纯历史 replay 只渲染事实。只有当前仍 pending 且经服务端验证的 action 提供决策按钮。

## 4. 阶段依赖和建议提交顺序

| 里程碑 | 任务 | 前置条件 | 可交付结果 |
| --- | --- | --- | --- |
| M0 | T00 | 无 | 22 项问题的复现台账、fixtures、基线 |
| M1 | T01 | T00 | 身份、事件、审批和 API 契约定稿 |
| M2 | T02、T03 | T01 | 完整事件生产/存储、只读历史服务 |
| M3 | T04、T07 | T01；整体验收依赖 T02/T03 | 无缝重连、durable 审批与统一恢复 |
| M4 | T05、T06 | T02/T03；完整交互依赖 T04/T07 | TUI/Web live/replay 与导航一致 |
| M5 | T08、T09 | T02–T07 | 旧数据兼容、集成/故障/性能验收 |
| M6 | T10 | M5 全部必需门禁通过 | 发布与回滚准备、文档和问题关闭 |

建议按职责拆分提交：复现测试 → 契约/schema → runtime/存储 → 历史 API → 连接恢复 → durable 审批 → TUI → Web → 兼容/集成。T04 与 T07 可在契约冻结后分别推进；TUI/Web 共享 fixtures 后可分别实现。本计划不要求自动创建并行 agent。

## 5. 开发任务清单

以下复选框保留最初计划的细粒度分解；实施后的权威状态、F01–F22 逐项证据和未完成门禁记录在第 11 节。任务关闭以可运行证据为准；当前修复仍在未提交工作树中，未用“代码已合并”代替完成标准。

### T00 — 复现与回归基线（P1）

主要文件：`pkg/tui/*_test.go`、`pkg/run/subagent_test.go`、`pkg/turn/*_test.go`、`pkg/gateway/*_test.go`、`frontend/src/composables/useChatStream.test.ts`。

- [ ] T00.01 建立 F01–F22 复现台账，每项记录触发输入、事件序列、预期、实际、影响路径和测试名称。
- [ ] T00.02 用可控假 LLM/工具产生单子任务、fanout、fork、typed、async、continue 和审批序列；不依赖真实模型随机输出。
- [ ] T00.03 保存 live 主/子 VM 的结构化结果，同时关闭并重开 store，调用真实 resume API 生成 replay 结果。
- [ ] T00.04 为 fanout 反序派发、空任务和双 fanout 添加确定性调度，不靠 sleep 概率碰撞。
- [ ] T00.05 建立 WS 断线、分页边界、进程重启、审批前后故障注入点。
- [ ] T00.06 记录既有测试基线；区分“沿用当前行为的 characterization”与“期望修复后通过的回归测试”，不得把错误行为固化为验收。

验收：22 项均有可复现测试或明确的静态可达证据；发现推导不成立时修订台账，不为凑问题数量实施无效变更。

### T01 — 身份、事件和状态契约（P1）

主要文件：`pkg/event`、`pkg/turn/events.go`、`pkg/agent/subagent_history.go`、`pkg/state/runrt.go`、`frontend/src/lib/forebrainGatewayRuntime.ts`。

- [ ] T01.01 定稿第 3 节身份字段；给已有 UUID、task/roster key 制定显式映射和旧数据 fallback。
- [ ] T01.02 定义 spawned/continued/prompt/assistant/reasoning/tool/usage/approval/lifecycle 事件，补全 parent tool 与 fanout index。
- [ ] T01.03 明确 tool started/completed/failed/cancelled 的字段、消息边界、展示正文、结构化输入、错误及耗时。
- [ ] T01.04 定义 subagent running/waiting_approval/waiting_input/succeeded/failed/cancelled/interrupted 等状态及合法迁移；旧状态统一映射。
- [ ] T01.05 定义 snapshot、分页与 resume cursor 协议，采用 conversation 级单调顺序覆盖父子 run。
- [ ] T01.06 定义批准范围和取消作用域；明确 session grant、turn grant、one-shot、remembered rule、network、request_permissions、ask 的差异。
- [ ] T01.07 发布版本化 JSON fixtures，Go 解码/投影与 TS 解码/投影均验证；未知字段可兼容，未知事件不能静默破坏已有历史。

验收：不再通过当前视图、数组位置或 subtype 推断所有权；协议能够表达完整验收场景。

### T02 — 统一 runtime 与完整事件持久化（P1）

主要文件：`pkg/run/subagent.go`、`pkg/run/run.go`、`pkg/run/fork.go`、`pkg/run/transcript.go`、`pkg/run/runner.go`、`pkg/process/one_shot.go`、`pkg/state`。

- [ ] T02.01 在派发前分配稳定 task/agent 身份，携带 parent_tool_call_id、task_index、原始任务文本；移除 UI 对派发顺序的依赖。
- [ ] T02.02 收敛 direct notification、child run step、parent mirror 的逻辑事件身份，确保重复传输不会产生多个逻辑 spawn/end。
- [ ] T02.03 为 assistant、reasoning、usage、provider web search、工具 display 元数据与状态边界建立 durable 写入路径。
- [ ] T02.04 保留原始显示正文与模型正文的区别；持久化附件/引用稳定标识，避免 replay 依赖已失效的内存对象或临时路径。
- [ ] T02.05 统一 typed/fork/同步/async/continue/plan reviewer 的运行与审批恢复 seam；修复 fork 使用 prep.ctx 丢失 resumeCtx 的路径。
- [ ] T02.06 continue 使用原 agent 身份、新执行轮次；追加本轮 prompt 和 lifecycle，清除本轮临时错误状态，保留此前消息和错误记录。
- [ ] T02.07 async 在父 turn 返回后继续产生完整、正确归属的事件；明确 detach、cancel、registry 和最终持久化的生命周期。
- [ ] T02.08 审核 RunFork sidechain 与 worker transcript 的写入/读取关系，建立统一索引或适配器；失败/审批前的部分输出也可恢复。
- [ ] T02.09 非流式 provider 或只有最终结果的执行也产生完整 assistant 记录；已有 stream 时不重复追加最终答案。
- [ ] T02.10 审批预算耗尽、用户取消、启动失败、panic 和存储错误清理关联 pending action/运行状态，不能留下无人处理的假 running。
- [ ] T02.11 明确存储失败的处理与告警；终态前 flush，事务/outbox 保证展示事件和状态更新可恢复。

验收：新执行关闭所有内存状态后，能仅从磁盘恢复全部子树、消息和终态；所有入口的审批行为一致。

### T03 — 完整历史查询与兼容投影（P1）

主要文件：`pkg/state/session_store.go`、`pkg/state/runrt.go`、`pkg/turn/session.go`、`pkg/turn/events.go`、`pkg/gateway/api_extra.go`、`frontend/src/lib/api.ts`。

- [ ] T03.01 将 UI 原始历史查询与 active model context 查询分离；UI 不再通过 ListRecentMessages 恢复完整会话。
- [ ] T03.02 提供 conversation snapshot/事件分页和 agent 历史分页；返回 next cursor、has_more、schema version 和一致性高水位。
- [ ] T03.03 消除固定 100/500/5000/200 条造成的不可达历史；允许每页限流，但必须能继续读到全部数据。
- [ ] T03.04 返回稳定 message/block/tool/action ID、原始 parts 或等价展示字段、附件引用、计划、引用和时间元数据。
- [ ] T03.05 建立 conversation→agent→execution→worker/sidechain 的索引，支持按子任务读取全部轮次，不仅返回最终 output 摘要。
- [ ] T03.06 合并父镜像与子事件时按 event identity 去重、按 conversation_seq 排序；补齐旧 dispatch 缺 task 的可恢复信息。
- [ ] T03.07 历史展示过滤 runtime-only meta user 消息，保留真正用户输入；不通过正文标签猜测消息是否 meta。
- [ ] T03.08 审批列表支持 conversation 和可选 agent 过滤；在服务端校验 primary-agent/workspace ownership，禁止只靠前端过滤。
- [ ] T03.09 分页快照在并发新增消息时不漏不重；附件/工具产物缺失时保留卡片并显示可理解的缺失原因。

验收：compact/clear 后可浏览原始全历史；超过所有原上限的主/子历史可完整分页；API 提供渲染所需结构且不跨 ownership 边界。

### T04 — Session 级订阅与无缝重连（P1）

主要文件：`pkg/gateway/server.go`、`pkg/gateway/wsevents.go`、`pkg/state/runrt.go`、`frontend/src/composables/useChatStream.ts`。

- [ ] T04.01 将观察连接从 send() 单次执行中解耦；订阅覆盖整个 conversation 的父/子/后台事件。
- [ ] T04.02 采用可证明无缝的 snapshot+tail 协议：先建立缓冲订阅，获取一致性高水位 H，回放至 H，再按序消费大于 H 的事件；允许其他等价事务/outbox 方案。
- [ ] T04.03 客户端保存最后已应用游标；服务端断线后按游标补齐。游标过期返回明确 resync 指令，不能静默截断。
- [ ] T04.04 live/replay 的同一 event_id 只应用一次；重传、重复 lifecycle、重复 usage 和 completion 不增加第二份内容。
- [ ] T04.05 区分 run terminal、approval wait、connection status；父 run terminal 不关闭仍需观察的后台 agent 流。
- [ ] T04.06 legacy requires_action 与 canonical approval_requested 不再意外触发 socket.close；兼容旧客户端时明确协议版本行为。
- [ ] T04.07 定义 subscribe/bind/unbind、旧连接回调隔离、慢消费者背压及取消订阅，避免无限内存队列。
- [ ] T04.08 测试历史读取中、新订阅前、首增量前恰好出现 completion 的竞态；验证不会永久 waiting。

验收：多次重连后 VM 与不掉线执行一致；父完成后的后台子任务仍可观察；审批期间连接状态不被当作业务终态。

### T05 — TUI replay、卡片绑定与独立 VM（P1）

主要文件：`pkg/tui/commands.go`、`pkg/tui/reducer.go`、`pkg/tui/notify.go`、`pkg/tui/chat_surface.go`、`pkg/tui/input_events.go`。

- [ ] T05.01 resume 调用完整 snapshot/历史服务，构造主 VM 与各子 VM；保留 AgentID，移除强制归主视图逻辑。
- [ ] T05.02 live 与 replay 进入相同 reducer 语义入口；旧 transcript 重建通过适配器输出 canonical 事件，不再独立猜测状态。
- [ ] T05.03 工具 completion 即使没有本地 start 也直接建立终态 block；不能生成假 Running fanout。
- [ ] T05.04 fanout 通过 parent tool 和 task index 绑定 agent；覆盖并发、空 prompt、启动失败、同类型多任务和多 fanout。
- [ ] T05.05 为历史卡片提供确定的 agent target；子历史未加载时显示加载/错误状态并可重试，不能静默拒绝点击。
- [ ] T05.06 子视图展示全部 prompt/continue/reasoning/assistant/tool/approval/error/cancel 内容，按原始顺序分页接入。
- [ ] T05.07 完成、失败、取消后保留卡片和可导航的子 VM；审批 waiting 不显示为成功或泛化失败。
- [ ] T05.08 live/replay 共用 fold、鼠标行定位、键盘入口和返回操作；保存每个视图的 scroll/follow/fold 状态。
- [ ] T05.09 历史时间来自持久化数据；终态不重新启动计时，footer 模型上下文统计仍使用 active context，避免混入历史 replay usage。
- [ ] T05.10 保持批量回放与虚拟视图更新，避免每条消息重绘造成 O(n²)；按需加载不能使历史卡片失去导航目标。

验收：固定窗口尺寸下 live/replay 结构和必要截图相等；逐个点击每个 subagent 卡片，进入正确 VM 并读到首条、末条和各轮历史。

### T06 — Web session 状态、子会话与审批 UX（P1/P2）

主要文件：`frontend/src/composables/useChatStream.ts`、`frontend/src/views/ChatView.vue`、`frontend/src/components/chat/SubagentCard.vue`、`SubagentConversation.vue`、`AgentViewTabs.vue`、`frontend/src/lib/api.ts`。

- [ ] T06.01 建立按 conversation 索引的状态容器，恢复主消息、子 transcript、lifecycle 和 pending actions；不只恢复普通 messages。
- [ ] T06.02 snapshot 和 live 增量进入同一投影函数，组件只读取统一 blocks；恢复后使用原消息卡片组件。
- [ ] T06.03 引入 request generation/AbortController；所有历史、mode、roster、pending action 的异步响应校验 session，旧 finally/catch 也不能污染新会话。
- [ ] T06.04 在 snapshot 加载期间缓冲或按高水位合并 live 事件，防止历史响应覆盖新消息。
- [ ] T06.05 卡片与 transcript 使用稳定 ID；删除“必须有父 assistant 正文才保留消息”的判断，错误/取消时保存已产生内容。
- [ ] T06.06 continue 追加每轮 prompt，区分新执行与旧 spawn 重传；重置本轮状态但保留历史错误和输出。
- [ ] T06.07 补全 waiting/cancelled/interrupted 等状态，以及工具失败/取消的组件 state；状态文案和可执行按钮与服务端一致。
- [ ] T06.08 订阅不随父回答结束或审批等待退出；批准后由同一 session 订阅接收继续执行结果，不靠刷新 pending 列表代替恢复消息。
- [ ] T06.09 审批卡片显示具体任务、agent 身份和所属会话，点击进入对应子视图；显示允许范围、拒绝反馈和明确取消作用域。
- [ ] T06.10 审批提交增加请求中状态、错误提示及幂等重试；多 tab 决策后立即同步已解决状态，不能保留旧可点按钮。
- [ ] T06.11 历史摘要列表复用 agent 导航，不再作为无法进入详情的独立数据孤岛。
- [ ] T06.12 实现主/子历史分页、稳定滚动锚点、独立 fold/follow/滚动状态和浏览状态保存；加载失败可重试且不清空已有消息。
- [ ] T06.13 审批历史只读展示，当前 pending action 在主/子视图使用同一个 action 状态，避免各自提交不同决定。

验收：页面刷新、切会话、断线重连、无父正文、审批后继续、异步子任务等场景均保留正确卡片和完整子历史。

### T07 — Durable 审批与统一恢复协调（P1）

主要文件：`pkg/turn/approval.go`、`pkg/turn/subagent_approval.go`、`pkg/state/runrt.go`、`pkg/run/runner.go`、`pkg/tui/chat_surface.go`、`pkg/gateway/approval.go`、`pkg/gateway/api_extra.go`、`pkg/tool/permissions.go`。

- [ ] T07.01 用 durable ownership/claim 替代 ReleaseSubagentApprovalWait 的“删除 wait 防双恢复”；snapshot 在 continuation 安全接续前可恢复。
- [ ] T07.02 将 action 决策、权限效果和恢复任务建立原子记录或 outbox；记录恢复阶段，保证重启后可重放尚未生效的权限效果。
- [ ] T07.03 建立唯一 continuation claim，覆盖 HTTP/WS/TUI、内存循环、重复提交和多 tab；引入 owner generation/fencing，旧 owner 不能继续执行。
- [ ] T07.04 统一批准/拒绝/取消/过期/已回答/网络决策/permission grant 的类型化恢复结果；补测 user_interaction，确认各 subtype 是否允许调用。
- [ ] T07.05 所有子执行入口使用同一恢复上下文构造器，检查真实调用拿到 ActionID、ToolApprovalResume、Denied/Reason、snapshot 和原工具标识。
- [ ] T07.06 分离 conversationSessionID 和 workerSessionID；权限规则与 network session decision 按用户看到的 conversation 授权，模型历史仍按 worker 隔离。
- [ ] T07.07 核对 request_permissions 的 turn/session grant 和 strict auto review；UI 只能展示服务端允许的决策，不新增宽泛授权能力。
- [ ] T07.08 待决审批恢复后实时显示；历史已决审批只读。取消/过期/预算耗尽清理相关等待和 registry 状态，并产生可回放事件。
- [ ] T07.09 处理 fanout 多子任务并发审批：TUI 队列等待可取消，不能被不可取消 mutex 阻塞；Web 多请求清晰定位，决定一个不错误唤醒/取消其他任务。
- [ ] T07.10 对“已批准未 claim、已 claim 未执行、已执行未记结果、已记结果未发事件”分别定义恢复策略；外部副作用结果不确定时不自动重复执行。
- [ ] T07.11 禁止 replay 触发审批 handler/工具执行；恢复执行必须经过当前 durable 状态验证和唯一 claim。
- [ ] T07.12 为决定、权限效果、恢复 claim、执行和完成记录可关联的诊断信息，避免仅返回成功而 continuation 丢失。

验收：允许/拒绝后正确接续原 agent；取消不继续执行；重复决定无重复 continuation；任意故障注入点重启后请求、上下文和授权范围正确。

### T08 — 旧历史与启动恢复（P1/P2）

- [ ] T08.01 schema 采用可重入、增量迁移；保留旧消息、ledger 和 sidechain，不批量删除原始数据。
- [ ] T08.02 为旧 session 建立可恢复索引，统一 task/roster UUID 映射；同内容的两次 continue 不得因文本去重而合并。
- [ ] T08.03 旧数据优先恢复原始消息和结构，再使用可证明的 ledger 摘要 fallback；缺失事件标记历史不完整，不伪造 reasoning/耗时/工具结果。
- [ ] T08.04 旧 wait/session 授权范围无法可靠推断时，保留原请求并要求新的有效决定，不自动扩大权限。
- [ ] T08.05 启动时协调 stale running、pending、已决但未恢复的 action 与 registry；无法继续的 run 显示 interrupted/recovery required，不能永久假 running。
- [ ] T08.06 同一旧 session 多次打开不重复导入；兼容读取不可修改旧历史正文或重新执行工具。
- [ ] T08.07 快照是可重建投影，记录版本和游标；损坏或过期时回退原始事件，不删除原始历史。

验收：旧版数据可打开、可导航到已有子消息；缺失数据边界明确；新版完整历史满足严格等价标准。

### T09 — 集成、故障与性能验收（P1）

- [ ] T09.01 完成第 6 节全部必需矩阵，记录 Go/TS/HTTP/WS/PTY/浏览器各层证据。
- [ ] T09.02 共享 fixtures 在 live/replay 两条路径执行，比较规范化主/子 VM；不能仅测试“某段文本存在”。
- [ ] T09.03 新建进程或重新打开数据库进行重启测试，避免复用 registry、Vue refs 或内存 approval snapshot。
- [ ] T09.04 对审批 claim、取消、派发绑定、session 切换和重连边界进行确定性并发测试及相关 Go race 检查。
- [ ] T09.05 执行真实 TUI 鼠标/键盘导航和 Web 点击/刷新/返回验证，覆盖首屏外和折叠后的 fanout 卡片。
- [ ] T09.06 使用至少 1000 条主消息、300 个子任务、单 run 超过 5000 个事件和 10 万级 conversation 事件验证无硬截断、分页完整和可交互性。
- [ ] T09.07 记录参考硬件、窗口/viewport、首屏时间、翻页/切视图延迟、峰值内存和慢客户端积压。M0 记录基线并确定数值门槛；至少保证无 O(n²) 回放、无无界传输队列和输入长时间阻塞。
- [ ] T09.08 覆盖大型工具输出、Unicode、多行 code、附件缺失与不可读历史记录；错误必须可见且不清空其他历史。

验收：所有 F 编号均能指向通过的针对性用例；剩余不确定性有明确记录，不能用既有局部测试通过替代完整验收。

### T10 — 发布准备和交付（P1）

- [ ] T10.01 为每个 F 编号补上修复提交、测试、运行证据与兼容说明。
- [ ] T10.02 前后端协议按版本协商；优先部署兼容的新存储/服务端，随后切换客户端，避免旧客户端误解新审批状态。
- [ ] T10.03 确认回滚保留新历史，旧版本不能误写未知 schema 或重复消费新审批 claim；不兼容时使用只读历史/停止新执行的明确模式。
- [ ] T10.04 更新 session/subagent/approval 开发文档和相关交接文档，说明旧数据限制及诊断入口。
- [ ] T10.05 检查无无关文件变更，运行必需检查并记录结果；发布说明区分恢复历史、恢复执行和重新发起任务。

验收：不存在未说明的 P1 缺口；新数据严格一致、旧数据可解释、审批可恢复且授权范围正确。

## 6. 必需测试矩阵

“两端”指 TUI 和 Web。浏览状态截图使用固定尺寸；业务断言优先比较规范化 block/事件结构。

| 用例 | 输入/故障 | 必须断言 | 层级 |
| --- | --- | --- | --- |
| V01 | 单个 typed subagent 正常完成 | 主卡片、任务、工具、结论；live/replay 相等 | runtime、两端 |
| V02 | 单个 fork，流式与非流式各一次 | 完整答案，无主子串流，无重复最终消息 | runtime、两端 |
| V03 | fanout 至少 3 个，同 subtype，反序 spawn | 每张卡片指向正确 task；结果/usage 不串位 | reducer、两端交互 |
| V04 | 空 prompt、启动失败、两个 fanout 交错 | 无 waiting 占位错配，错误任务可解释 | runtime、TUI |
| V05 | 工具只有 completion 的历史 | 显示终态，不重启 spinner/计时 | 投影、两端 |
| V06 | reasoning→assistant→tool→assistant | block 顺序、原始正文、详情和时长一致 | 共享 fixture、两端 |
| V07 | provider search、usage、工具 display | 持久化完整；重传不重复累加 | 存储、投影 |
| V08 | 第 2、3 次 continue，包含相同指令文本 | 每轮 prompt 恰好一次、旧错误保留、本轮状态正确 | runtime、两端 |
| V09 | async 子任务晚于父 run 完成 | 仍收到后续消息，结束后仍能打开 | WS、Web、TUI |
| V10 | 子工具允许/拒绝并附反馈/取消 | 接续或终止目标正确，拒绝不冒充失败 | 审批、runtime、两端 |
| V11 | fork、async、continue 分别触发审批 | 真正执行使用恢复 context，无重复旧工具 | runtime 集成 |
| V12 | fanout 多个子审批，同时决定/取消 | 一个 action 不误影响其他任务，状态清晰 | race、两端 |
| V13 | session/turn/once/remember/network grant | 正确 conversation/run 范围，其他会话不继承 | 权限、两端 |
| V14 | request_permissions、strict review、ask answered | 使用类型化结果，不误判取消 | 审批契约/集成 |
| V15 | pending 时关闭进程再恢复 | 原请求、工具参数、worker snapshot 可找回 | 重启集成 |
| V16 | 决定持久化/claim/执行/结果各边界崩溃 | 不丢决定，不重复 continuation；不确定副作用不盲重试 | 故障注入 |
| V17 | 多 tab/HTTP/WS 同时或重复决定 | 唯一生效/明确冲突，所有视图同步 | API、race、Web |
| V18 | 历史读取与 tail 接入之间新增终态 | 无事件缝隙，无永久等待 | WS 确定性调度 |
| V19 | 多次断线，重复 event，游标过期 | 无重复卡片/正文/usage；完整 resync | WS、前端 |
| V20 | compact 与 clear 前后 resume | 全历史可见；模型上下文和 footer 不恢复旧预算 | 存储、两端 |
| V21 | A→B→A 快速切换，响应反序 | messages/mode/roster/actions/订阅互不污染 | 前端 |
| V22 | 父回答为空、错误或取消，仅留下子卡片 | 卡片和已产生子消息不被删除 | 前端 |
| V23 | 超过旧消息/事件/ledger 上限 | 所有分页首尾连续，无缺条、重复或漏终态 | API、性能 |
| V24 | 旧 transcript + ledger + sidechain，不完整数据 | 尽量还原且标明缺失；重复打开不重复导入 | 兼容集成 |
| V25 | 历史审批再次回放 | 不调用 LLM、工具或 decision API，不重新弹出已决请求 | 两端、副作用计数 |
| V26 | 展开、滚动、点击非首屏卡片、返回 | 正确 hit target，保留视图状态，历史完整可读 | PTY、浏览器 |
| V27 | 跨 primary-agent/workspace 的 session/action ID | 查询/订阅/决定拒绝越界，不返回其他历史 | ownership 集成 |
| V28 | 慢客户端、写库失败、缺附件、损坏快照 | 不静默丢数据，无界队列受控，可恢复/可重试 | 故障、性能 |

## 7. 验证命令与执行说明

修复前基线曾运行下列筛选测试。修复后的最终自动化结果和环境限制记录在第 11.3 节。

```sh
go test ./pkg/tui ./pkg/turn ./pkg/gateway ./pkg/run ./pkg/agent -run 'Subagent|Replay|Resume|Fanout|Approval' -count=1
pnpm --dir frontend test src/composables/useChatStream.test.ts src/lib/approvalSuggestions.test.ts
```

实施中按变更范围执行相关测试，最终至少执行：

```sh
go test ./pkg/event ./pkg/state ./pkg/agent ./pkg/run ./pkg/process ./pkg/turn ./pkg/tool ./pkg/tui ./pkg/gateway
go test -race ./pkg/state ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway -run 'Subagent|Replay|Resume|Fanout|Approval|Reconnect|Session' -count=1
pnpm --dir frontend test
pnpm --dir frontend build
```

新增回归测试可能不匹配上述筛选表达式，必须另行全量执行所属包或显式列出新测试。PTY、浏览器、进程崩溃、数据库迁移和性能场景需要实际实现对应 harness，并在交付时补上可复制命令；不能把源码包含某个字段当作交互验收。

## 8. 风险、迁移和回滚约束

| 风险 | 处理要求 |
| --- | --- |
| 老历史从未保存过部分 live 内容 | 标明恢复完整度；保留原数据，不生成虚构正文 |
| 事件与 transcript 双写重复 | 明确单一逻辑事件身份和投影边界，幂等导入 |
| 主/worker session 混用扩大授权 | 分离字段并添加跨会话负向测试，禁止用修 UI 的方式放宽策略 |
| 批准 action 后 crash，权限效果未应用 | durable 效果记录/outbox，幂等应用与恢复 |
| 工具副作用发生但没有 completion | 对账/幂等恢复；无法判断时显示不确定并停止自动重试 |
| stream 持久化吞吐或磁盘增长 | 批处理与索引、测量吞吐；未经明确策略不删除用户历史 |
| 新旧客户端同时连接 | 协议版本协商，兼容适配，未知状态不能变成成功 |
| 只比较终态漏掉 waiting 过程错误 | 对事件前缀、审批中间态、重连边界分别做等价测试 |
| 一次大改导致难以定位回归 | 按 M0–M6 和职责提交，每步保留可执行 fixtures |

## 9. 每项任务的关闭模板

```markdown
- 任务 ID：Txx.yy
- 对应问题：Fxx
- 修复提交/PR：
- 改动及原因：
- 修复前复现证据：
- 验收测试/命令：
- TUI/Web/重启/兼容验证结果：
- 数据迁移与回滚影响：
- 剩余限制：
- 状态：未开始 / 进行中 / 待验收 / 已完成
```

## 10. 最终交付检查

- [x] F01–F22 全部有可追踪的修复或经验证的结论修订；没有仅凭注释宣称完成的条目。
- [x] 新 session 完成、审批等待、取消、失败及继续执行时，live/replay 的主/子 VM 等价自动化测试通过。
- [x] TUI/Web 卡片目标、独立子视图和完整分页历史的自动化测试通过。
- [x] compact/clear、长历史、async 父子生命周期、session 切换和断线重连不丢消息。
- [x] 审批可在重启后恢复，决定与 continuation 幂等，授权 scope 正确，无盲目重执行副作用。
- [x] 已决审批 replay 只读；待决审批定位到具体任务且可继续处理。
- [ ] 旧历史限制明确，迁移可重入，回滚不销毁新历史。
- [ ] Go/前端测试、必要 race、PTY/浏览器、崩溃恢复和性能验收均有证据。
- [x] 相关开发/交接文档更新；工作区原有无关改动保持不变。

## 11. 实施结果、证据与剩余发布门禁

### 11.1 任务组状态

| 任务组 | 状态 | 实施结果 |
| --- | --- | --- |
| T00 | 自动化完成 | 建立确定性 runtime、replay、WS、审批、重启、并发和长历史回归；测试名与 F 编号映射见 11.2。 |
| T01 | 完成 | canonical `RunEvent` 增加稳定身份、父子关系、工具/审批展示字段、`schema_version`、`occurred_at`；Go/TS 共用版本化 fixture。 |
| T02 | 完成 | 所有 subagent 入口统一持久化 spawned/continue/tool/approval/end 事件；conversation/worker/run/action 身份分离；panic、取消和写库失败落 durable 终态。 |
| T03 | 完成 | 新增 immutable conversation event log、固定高水位分页、完整 UI 历史与 active model context 分离；历史和 action 查询执行 owner/session 边界。 |
| T04 | 完成 | gateway session observer 使用 durable snapshot+buffered tail；event ID/sequence 去重；断线按 cursor 恢复；绑定 generation 隔离旧会话；慢消费者/快照 tail 溢出明确关闭并要求重同步。 |
| T05 | 自动化完成 | TUI live/replay 共用 reducer；恢复每个 agent VM、卡片 hit target、历史 tool/approval/error/lifecycle；主/子视图各自保存 scroll/follow/fold/selection。真实 PTY 手工验收待 11.4。 |
| T06 | 自动化完成 | Web 恢复主消息和完整子 transcript，卡片进入对应子视图；snapshot/live 共用事件投影；异步 generation、重连去重、错误重试与 localStorage 浏览状态已落地。真实浏览器手工验收待 11.4。 |
| T07 | 完成 | durable wait/owner lease/execution fence 取代删除 wait；批准、拒绝、取消、answered、network、request_permissions、expired 具有类型化终态；重复提交和恢复只允许一个 continuation。 |
| T08 | 自动化完成 | schema 增量迁移、旧 transcript/ledger/sidechain fallback、重复打开幂等、stale/uncertain wait 恢复均有测试；从未持久化的旧正文仍按第 1.3 节约束不可凭空生成。 |
| T09 | 部分完成 | Go/TS/HTTP/WS、数据库重开、故障、race、1000 主消息/300 子任务/>5000 单 run/100001 conversation event 已通过；真实 PTY/浏览器和参考硬件 UX 指标待执行。 |
| T10 | 部分完成 | 协议版本协商、schema fail-closed、文档与自动化检查完成；尚未提交/部署，旧版本滚回演练需在 staging/release 环境执行。 |

### 11.2 F01–F22 关闭证据

当前“修复提交/PR”统一为：**未提交工作树**。下表给出源码落点和代表性可运行测试；完整变更由本文件所在工作树的 diff 保留。

| 编号 | 状态 | 修复与代表性证据 |
| --- | --- | --- |
| F01 | 已修复 | TUI 按 `agent_id` 恢复独立 VM，不再清空归属；`TestReplaySubagentEventsRestoreClickableCompleteAgentView`、`TestPrintSessionResumeContextRestoresSavedSubagentView`。 |
| F02 | 已修复 | completion 可独立建立终态 tool/lifecycle block，fanout end 不再伪装 start；TUI replay/tool completion 与 runtime parent-completion 测试覆盖。 |
| F03 | 已修复 | Web canonical history 恢复 subagent cards/blocks；`replays a complete subagent transcript...`、共享 fixture TS 测试。 |
| F04 | 已修复 | UI 历史改读完整 message/event 分页，和 compact 后模型上下文分离；`TestPrintSessionResumeContextUsesActiveContextForFooterWhileReplayingFullHistory`、1000/300 Web 压测。 |
| F05 | 已修复 | message/run/event API 返回稳定 ID、run 关联、parts、附件、plan、引用和时间字段；`TestHandleChatMessagesReturnsDurableRunIdentity` 及 session context/attachment tests。 |
| F06 | 已修复 | runtime 事件先落 `fb_session_events` 再发布；tool/subagent/approval/terminal 均走 canonical durable 路径；store/publish failure tests。 |
| F07 | 已修复 | conversation 级游标覆盖父子 run，任务文本与子树事件分页可达；`TestSessionEventsPageCompletelyPastLegacyCutoffs`、`TestListMergedUnboundedMakesOldSubagentCardsReachable`。 |
| F08 | 已修复 | WS `BindBuffered → high water snapshot → ResumeAfter(H)` 关闭丢事件窗口；`TestRunEventSubscriptionBuffersSnapshotTailInSequenceOrder`。 |
| F09 | 已修复 | event ID + sequence 去重，客户端重连沿 durable cursor；`keeps the projected prefix and resumes from its durable cursor after disconnect`。 |
| F10 | 已修复 | fanout 在派发前保存 task/agent/parent tool/task index，不再按到达位置配对；`TestSubagentFanoutAssignsDistinctTaskIDPerTask`、TUI fanout click/mapping tests。 |
| F11 | 已修复 | `requires_action` 只改变业务状态，不再关闭观察 socket；同一 session observer 在审批后继续接收 canonical 增量；gateway/Web approval tests。 |
| F12 | 已修复 | fork 和通用 retry 使用同一 `ToolApprovalResume` context；`TestRunAcrossApprovalsCrossesDurableFenceBeforeRetry` 及 fork approval tests。 |
| F13 | 已修复 | typed/fork/fanout/async/continue 均使用统一 `runAcrossApprovals` seam；`TestSubagentFanoutResolvesChildApprovalsInPlace`、`TestSubagentRunResolvesChildApprovalInPlace`。 |
| F14 | 已修复 | durable wait 不再因内存接管而删除，owner/lease/阶段可重启恢复；`TestWaitResumeOwnerSurvivesReload`、`TestClaimWaitResumeIsAtomicAndPreservesSourceMetadata`。 |
| F15 | 已修复 | conversation session 用于授权，worker session 仅隔离执行历史；`TestApprovalServiceReplaysRequestPermissionsEffectWithRunScope` 及跨 session grant tests。 |
| F16 | 已修复 | pending actions 在服务端按 primary owner + conversation + agent 过滤，Web 显示 requester 并可导航；`TestScopedPendingActionsAreFilteredBeforeTheGlobalDisplayLimit`。 |
| F17 | 已修复 | continue 保留 agent 身份、分配新 execution、追加相同 prompt 而不文本去重；`TestContinueSubagentExecutionReusesChildRunAndPreviousOutput`、`TestReducerSubagentContinueKeepsRepeatedPromptPerExecution`。 |
| F18 | 已修复 | Web history/mode/roster/action 使用 generation/session guard；WS binding generation 丢弃旧 session 已排队回调；session switching tests。 |
| F19 | 已修复 | 无父正文时保留独立 assistant host 和 subagent cards；`keeps subagent cards on a distinct turn when the parent produced no answer`。 |
| F20 | 已修复 | session observer 与单次 request 生命周期解耦，父 run 终态不终止后台 child 观察；async durable completion tests。 |
| F21 | 已修复 | cancelled/failed/interrupted/waiting_approval/waiting_input 独立状态；recursive descendant cancel 与 UI state tests。 |
| F22 | 已修复 | approval requested/resolved/denied/cancelled/expired 写入可回放子时间线；`TestDetachedApprovalResumePublishesToolAndNestedApprovalHistory`、`TestGatewayApprovalExpiryIsDurableVisibleAndTerminal`。 |

### 11.3 已执行自动化验证（2026-09-07，Asia/Shanghai）

```sh
go test ./...
# 27 个 Go package 全部通过

go test -race ./pkg/state ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway \
  -run 'Subagent|Replay|Resume|Fanout|Approval|Reconnect|Session|Cancel|Expire' -count=1
# 全部通过

go vet ./pkg/event ./pkg/state ./pkg/agent ./pkg/run ./pkg/process \
  ./pkg/turn ./pkg/tool ./pkg/tui ./pkg/gateway
# 通过

pnpm --dir frontend exec vue-tsc --noEmit
pnpm --dir frontend test
# 8 个 test files、69 个 tests 全部通过

pnpm --dir frontend build
# 通过；仅有既存的 Vite chunk-size warning

git diff --check
# 通过
```

规模门禁已由自动化用例覆盖：1000 条主消息、300 个子任务/900 个子事件、单 run 超过 5000 条恢复记录、100001 条 conversation events 固定高水位分页；并发 `AppendStep`、审批 claim、WS snapshot-tail/overflow、跨 session 绑定均执行 race 或确定性调度测试。

### 11.4 尚未完成的发布环境门禁

这些不是已知未修代码缺陷，但在实际执行并记录前不能宣称 M6 发布验收完成：

1. **真实浏览器点击验收**：当前应用内浏览器运行环境没有任何可用 browser instance（浏览器列表为空），因此未执行刷新、非首屏卡片点击、返回和折叠后点击的真实 DOM/视觉验收。需在可用实例中按 V03、V09、V17、V21、V22、V26 执行。
2. **真实 PTY 验收**：自动化 renderer/hit-target/browse-state 测试已过，但尚未在固定终端尺寸下手工逐卡片验证鼠标、键盘、alt-screen 返回和非首屏滚动。需按 V03、V10、V12、V26 执行并保存终端尺寸与截图/录屏。
3. **参考硬件性能采样**：规模正确性与 race 已过，但尚未记录首屏时间、翻页/切视图延迟和峰值内存。需补参考硬件、viewport、慢客户端积压数据后才能勾选 T09.07。
4. **staging 升级/回滚演练**：新客户端/服务端已有 `2026-04-18` WS protocol 和 event schema v1 fail-closed 协商；仍需在真实部署中验证“新服务端→新客户端→旧版本只读/停止执行”的顺序及新历史不被旧版本改写，才能勾选 T10.03 和最终回滚项。

上述门禁之外，当前审计没有遗留的 F01–F22 P1/P2 代码缺口。工作树原有的 `forebrain-harness.github.io` 脏子模块及根目录 `package.json`/`package-lock.json` 未被本修复修改或清理。
