# 196 — 收件与投递：准入、挂起放行、路由、插话/自动唤醒、落库、回执、空闲通知

- **2026-11-01 执行前核实（评审者按现场刷新）**：202 已交付本计划要用的进程内插话入口——`pkg/run/subagent.go:2252` `steerRunningSubagent(ch, in)`（从 `SendToSubagent` 抽出的内部函数，`SendToSubagent` 保留人入口的跑批拒绝）与 `pkg/run/turn_input.go:748` `InputQueue.SteerFromAgent(in)`（不受关闭队列拦截）；`peer_list`/`peer_send` 已在 `pkg/run/peer_tools.go` 并进 `pkg/tool/permissions.go:777` 的豁免。**所以不要再新建 `SteerSubagentFromPeer`**——§3.1 与 §4 步骤 2 里它的位置改由这两个既有函数承担（文末「与 202 的关系」已如此裁决，正文按此执行）。195 的占位收件在 `pkg/process/peers.go:555`（`RefusedStatus("inbox not ready", …)`），本计划替换它；`PeerSurface` 已带 196 的 `Publish`/`Wake` 占位注释。文件数现状：`pkg/run` **28**（上限 31）、`pkg/process` **24**、`pkg/gateway` **31（上限，绝不新建）**。前置 STOP 条件 1 已核实不触发：`grep -rn "json.Marshal(m\b|json.Marshal(msg\b|json.Marshal(messages" pkg/llm` 无输出，没有 provider 整体序列化 `llm.Message`。

- **基线**：`202ed4f2`（dev）+ 194、195、202 落地后的工作区。
- **状态**：TODO
- **依赖**：195
- **执行隔离**：in-tree，**零 commit**
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` §3.6–§3.12、§5

> **执行者须知**：逐步执行，每步跑验证；碰到 §9 STOP 条件就停下报告。完成后更新 `plans/README.md` 本行状态。
>
> **Drift check（先跑）**：`git diff --stat 202ed4f2 -- pkg/run/controller.go pkg/run/turn_input.go pkg/run/orchestration_llm.go pkg/run/subagent.go pkg/llm/llm.go pkg/state/message_parts.go pkg/turn/session.go pkg/event/run_events.go pkg/tui/notify.go pkg/gateway/run_control.go`，并逐条核对 §2 的行号摘录；对不上就 STOP。

## 0. 背景与 owner 裁决（全族共用，执行期不得再改）

owner 2026-10-30：按工作树、跨 home 协同；**空闲收件方自动唤醒开一轮，正在跑的按插话在下一个工具边界送达**；线协议与 Claude Code 互通；**网关收到不带扩展寻址的消息按发送方工作树路由到同树最近活跃的会话，找不到回 refused**；防乒乓**只用 Claude Code 那套**（限速、重复、hop-chain、队列上限、权限模式不一致时挂起等用户放行）。旧规则「一端忙时另一端发消息拒绝、不做跨进程排队」**只管用户亲手发的消息，本计划不改它**。

**同日三条追加裁决**：①与 Claude Code 互通是存于 `fb_config` 的全局开关，**默认关闭**；②forebrain 跨进程通信**自闭环**，不依赖 Claude Code 或其他任何进程；③检测到本机装了 Claude Code 才显示这个开关，**打开后只用 Claude Code 的目录与 `cc-socks`，不再碰自己的登记目录**。落到本计划：一个进程任何时刻只有**一个收信口**——当前登记模式的 socket（195）。FB 模式（默认）下 195 的来源闸门保证它只收 forebrain 的帧；CC 模式下与 Claude Code 一样按准入收同用户的帧。流水线对两种模式相同；回执、空闲通知一律**从收到那条帧的地址**发出（`in.Local`）；切换模式时（195 的 `onModeSwitch` 钩子）挂起未放行的来信全部回 `expired`（设计 §7）。

本计划把 195 的占位收件换成真正的收件流水线，让来信变成收件代理看得见的一条 user 消息（落库），并按 Claude Code 语义回执。**不做**：发送工具（197）、同树护栏（198）、卡片样式（199/200——本计划只保证 TUI/网关能开轮、能收到挂起事件，样式先用现有的 origin 渲染）。

## 1. 共享架构（摘要）

- 收件流水线全部在 `pkg/process/peer_inbound.go`（新），TUI 与 gateway 共用。表面只提供两个回调：`Publish`（把运行事件送进自己的事件漏斗）与 `Wake`（为一条对等提示开一轮）。
- 模型看到的就是**信封原文**（`<cross-session-message …>…</cross-session-message>`），与 Claude Code 收件方一致。落库：收件会话的 user 行带 `is_meta` 部件 + `origin` 部件 `kind:"peer"`，**不改表结构**。
- 对等来信**不经过**表面的提交解析：不跑 `turn.Execute`/`turn.SkillReferences`，不解析斜杠命令、技能、`@` 引用（对应 Claude Code 的 `skipSlashCommands`、`skipAttachments`）。

## 2. 现状事实（已对基线核实）

- 插话：`pkg/run/controller.go:477-494` `Steer(runID, in)`——`phase != Running` 时把一份 `Rejected=true` 的副本放进 follow-up 并返回 false；`:496-506` `FollowUp(runID, in)`。`:331` `Find(sessionID)`。
- 插话变成模型消息：`pkg/run/orchestration_llm.go:167-183` `steerMessages` 对每个 `TurnInputEntry` 产出 `llm.UserMessage(entry.Parts...)`（`:180`）；`TurnInputEntry` 定义在 `pkg/run/turn_input.go:21-32`（字段 `Mode, Parts, Seq, SkillName, SkillPath`）；`run.Input` 在 `pkg/run/controller.go:33-60`。
- 子代理插话：`pkg/run/subagent.go:2208-2245` `SendToSubagent`——跑批拒收、空闲返回 `ErrSubagentNotRunning`、`ch.setSurface(surface)` 会**改写**子代理的表面；`ch.queue.Steer(in)` 返回 true＝已插话。
- `llm.Message`（`pkg/llm/llm.go:145-181`）有 `IsMeta`，**没有** origin 字段；`ContentPart.SourcePath`（`llm.go:50-55`）是「只用于持久化、永不发给 provider」的先例。
- 落库的 origin：`pkg/state/message_parts.go:23-27` `PartTypeOrigin`（「display-only: ParseMessageParts ignores it」），`:128-166` `MessageOriginHeartbeat/Cron`、`WithMessageOrigin`、`MessageOrigin`；`:15` `PartTypeIsMeta`，`:250-251` `IsMeta` 消息写 `is_meta` 部件。开轮时写 origin：`pkg/turn/session.go:303-305`（`in.Origin != ""` → `WithMessageOrigin`）。
- 表面读 origin：TUI 回放 `pkg/tui/commands.go:288-292`（有 origin 的 user 行渲染成 `MsgKindSystem`、标题＝origin）；网关 `pkg/gateway/api_extra.go:2306`（`m.Origin = state.MessageOrigin(t.PartsJSON)`）。
- 非用户发起的开轮先例：TUI `pkg/tui/notify.go:2253-2265` `continueAfterUsageLimit` → `s.notifyUI(AutoContinueDueMsg{…})`，由事件循环开轮；网关 `pkg/gateway/run_control.go:816-845` `detachedTurn{SessionID, Prompt, Trigger, Origin, Unattended, PromptOrigin, Announce, …}`、`:855` `startDetachedTurn`，心跳用法在 `:1066-1082`（`PromptOrigin: state.MessageOriginHeartbeat`、`Unattended: true`）。
- 事件：`pkg/event/run_events.go:71` `RunEventHeartbeatFired = "heartbeat_fired"`、`:351` `HeartbeatFiredPayload`——新事件照这个形状加。
- 事件漏斗：TUI `s.publishTUIRunEvent`、网关 `gw.publishGatewayRunEvent`，签名都是 `func(ctx, sessionID, runID, typ string, payload any) error`（`turn.AbandonedRunReaper.Publish` 用的就是它们，`pkg/tui/notify.go:1700-1704`、`pkg/gateway/serve_run.go:168-173`）。
- 会话被别的进程占着：`pkg/state/runrt.go:321` `RunStore.SessionBusy(ctx, sid) error`（属于 `ErrSessionBusy` 家族，`:28-39`）。
- 技能识别在表面：TUI `pkg/tui/chat_surface.go:150` `skills = turn.SkillReferences(userText)`；引擎入口 `pkg/turn/executor.go:29-38` `Execute`。
- 文件数：`pkg/gateway` 31（上限，**不新建文件**）；`pkg/run` **28**；`pkg/process` **24**（195 后）。

## 3. 设计

### 3.1 新增与改动的类型

- `pkg/llm/llm.go`：`Message` 加 `Origin string \`json:"origin,omitempty"\``，注释写明「who wrote this message on the person's behalf (state.MessageOrigin*); persistence metadata only, never sent to a provider」。
- `pkg/state/message_parts.go`：常量加 `MessageOriginPeer = "peer"`、`MessageOriginPeerStatus = "peer_status"`（197 用）；`MessagePartsJSON` 在 `msg.Origin != ""` 时追加 origin 部件（复用 `WithMessageOrigin` 的形状 `{"type":"origin","kind":…}`）；`ParseMessage…` 读回时**不**还原 `Origin` 给模型（只给表面用的 `MessageOrigin()` 读）。
- `pkg/run`：`Input` 加 `Origin string`；`TurnInputEntry` 加 `Origin string`；Input→Entry 的转换处（`steerEntries`，`subagent.go:820` 起；`InputQueue.Steer` 把 Input 交给 runtime 的那条路径）把 `Origin` 带过去；`steerMessages` 里 `m := llm.UserMessage(entry.Parts...)`，`entry.Origin != ""` 时 `m.IsMeta = true; m.Origin = entry.Origin`。
- `pkg/run/subagent.go`：**不新建函数**——用 202 已交付的 `steerRunningSubagent(ch *subagentChannel, in Input) (SubagentDelivery, error)`（`:2252`）。它不调用 `setSurface`，空闲返回 `ErrSubagentNotRunning`，正是本计划要的语义。`InputQueue.SteerFromAgent`（`turn_input.go:748`）承担「不受关闭队列拦截」的那一半。
- `pkg/turn/session.go`：开轮写 user 行时，`in.Origin` 是 `peer` 或 `peer_status` → 消息 `IsMeta = true`（再走现有 `WithMessageOrigin`）。
- `pkg/event/run_events.go`：
  - `RunEventPeerMessageHeld = "peer_message_held"`，`PeerMessageHeldPayload{MsgID, From, FromName, Preview, Cause string; HeldAtMs int64}`（`Preview`＝正文前 200 个字符；`Cause ∈ mode-mismatch | no-mode-asserted | hold-all`）。
  - `RunEventPeerMessageSettled = "peer_message_settled"`，`PeerMessageSettledPayload{MsgID, Outcome string}`（`Outcome ∈ released | denied | expired`）。
- `pkg/process/peers.go` 的 `PeerSurface` 加：
  - `Publish func(ctx context.Context, sessionID, runID, typ string, payload any) error`
  - `Wake func(ctx context.Context, p PeerPrompt) error`，`type PeerPrompt struct{ SessionID, Text, Origin, MsgID, FromName string }`；表面不能开轮时返回 `process.ErrPeerWakeUnavailable`。

### 3.2 收件流水线（`peer_inbound.go`，按顺序）

对 `type:"user"` 帧：

0. **来源闸门**（195，FB 模式下生效，CC 模式没有）：没过闸门的帧、以及切换模式前旧 socket 上迟到的帧，根本到不了这里。另记下本帧的「本地地址」`local = 帧到达的 socket`（`in.Local`），后面所有回执都从它发。
1. **形状**：`message.content` 须是非空字符串，否则丢弃（只记日志，不回执）。帧带 `session_id` 且不等于本进程记录的 `sessionId` → 丢弃。
2. **信封**：`peer.Parse(content)`；失败 → 自己包一层 `peer.Build(Envelope{From: 帧.from（合规时）, Body: peer.EscapeBody(content)})`，此后一律用这份文本。从信封取 `FromName`、`HopChain`、`FromMode`。
3. **准入**：`Admission.Admit(senderKey, 正文, HopChain, {自身 hop token：本进程用过的每个地址各一个（195）}, queued, now)`；`senderKey`＝`"from:"+帧.from`（无 from 时 `"pid:"+PeerPID`）；`queued`＝本进程已接收但还没交给模型的对等消息数（挂起 + 已排进 follow-up 尚未开始的）。不通过 → 回执 `dropped`（`drop_reason`＝原因，`dropped_msg_ids:[]`），结束。
4. **路由**（设计 §5.1）：
   - 帧带 `forebrain.to` → 必须是本进程扩展块里的代理 id，否则回执拒收「unknown agent」。
   - TUI：目标＝`CurrentSession()` 的主代理。
   - gateway：用 `PeerPID` 在当前 `layout.Dir` 里找发送方记录；工作树＝发送方扩展块里 `forebrain.from` 对应代理的 `root`（有的话）否则记录的 `cwd`；候选＝`SessionStore.RecentlyActiveSessions`（195）里 `cwd` 与该工作树同树（相等、祖先或后代，按路径分隔符判断，先 `filepath.EvalSymlinks` 两边）、非跑批、且 `SessionBusy` 为 nil 或 run 属于本进程（`env.Control.Find(sid)` 命中）的会话，取 `updated_at` 最新的；没有 → 拒收「no conversation in this work tree」。
   - 目标会话是跑批 → 拒收「batch conversations take no input」。
5. **入站策略**（设计 §3.10；**always-on 裁决：不读配置**）——自身模式＝目标会话审批模式为 `ApprovalNever` ? `bypass` : `prompting`；信封带 `FromMode` → 相同 accept、不同 hold(`mode-mismatch`)；不带 → 自身 bypass 则 hold(`no-mode-asserted`)，否则 accept。没有 `peers.inbound`、没有 `refuse` 档（配置里不存在该字段，见 195 的 `PeersSection`）。
6. **挂起**：放进进程内挂起缓冲（上限 20，满了淘汰最旧并给它回 `expired`；每条记下 `local`；切换登记模式或服务停止时——195 的 `onModeSwitch` 钩子——全部回 `expired` 并发 `settled{expired}`，回执在旧 socket 关闭之前从 `local` 发出），回 `held`，`Publish(target session, "", RunEventPeerMessageHeld, …)`。等待 `ReleasePeerMessage(msgID)` / `DenyPeerMessage(msgID)`（导出在 `Environment` 上，199/200 调）：放行 → 走第 7 步并回 `delivered`、发 `peer_message_settled{released}`；拒绝 → 回 `denied`、发 `settled{denied}`。
7. **投递**（设计 §5.2）：

| 目标 | 动作 |
|---|---|
| 主代理，`Control.Find(sid)` 为 `Running` | `Control.Steer(runID, Input{Text: 信封, Parts: [Text(信封)], Origin: "peer"})` |
| 主代理，`Waiting`/`Finishing` | `Control.FollowUp(runID, …同上…)` |
| 主代理，无 run，`SessionBusy` 非 nil（别的进程在跑） | 拒收「busy in another process」 |
| 主代理，空闲 | `Wake(PeerPrompt{SessionID, Text: 信封, Origin: "peer", MsgID, FromName})`；返回 `ErrPeerWakeUnavailable` → 拒收「inbox not attached」 |
| 子代理，正在跑 | `steerRunningSubagent(ch, Input{…Origin:"peer"})`（202 已交付，`subagent.go:2252`） |
| 子代理，已结束 | 对其主代理按上面几行投递，信封之前多一条 meta 消息（常量 `peerForwardedToDispatcherNote`，见 §3.4） |

对 `type:"control"` 帧：

- `peer_message_status` → 交给出站回执跟踪（197 实现；本计划留一个 `onReceipt func(peer.PeerMessageStatus, from string)` 钩子，默认记日志）。
- `notify_when_idle` → `from` 须通过 `peer.ReplyTargetAllowed(from, local, …)`（`peerFeatures` 取发送方记录的），不过就丢弃；订阅记下 `local`，空闲通知从该地址发（切换登记模式时，所有订阅先发 `state:"unavailable"` 再作废）；目标会话此刻空闲 → 立即回 `peer_idle_notice{orig_msg_id, state:"idle", finished_at:<最近一轮结束 ms>}`；否则登记订阅（每进程最多 32 个，12 小时过期），会话由忙变闲（1s tick 看到 `Control.Find` 由命中变不命中）时发送；进程退出时对所有订阅发 `state:"exited"`。**本计划不把 `notify_idle` 加进 `peerFeatures`**（201 真机核实后再加）。
- `peer_idle_notice`、`rename`、其它 → 记日志。

### 3.3 回执

- 只在 held、放行后送达（`delivered`）、拒绝（`denied`）、过期（`expired`）、拒收（线上写 `expired` + `status_detail:"refused"`）、丢弃（`dropped`）时发；正常接受不发。
- 目标＝帧的 `from`，仅当 `peer.ReplyTargetAllowed(from, local, os.Getuid(), 发送方记录的 peerFeatures)` 为真；回执的 `from` 字段＝`peer.EncodeUDS(local)`；`peer.Send(…, SendOptions{NoFollowSymlink: true, ExpectPID: PeerPID})`；失败只记日志。
- `reason` 用英文短句（不进模型）：held `held: permission modes differ` / `held: this session holds every peer message`；delivered `released after approval`；denied `declined by the user`；expired `expired without approval`；拒收用各自原因；dropped 用 `peer.DropReasons`。
- 进程退出：对挂起缓冲里每一条回 `expired`（总预算 1s）。

### 3.4 模型可见常量（新文件内 `const`，英文、不含任何厂商/模型/工具品牌名，配 golden 测试）

```go
const peerForwardedToDispatcherNote = "<system-reminder>\nA peer message addressed to your subagent %s arrived after that subagent finished, so it is delivered to you. Decide whether to continue the subagent with subagent_continue or to answer the sender yourself.\n</system-reminder>"
```

（`%s` 填子代理名；`fmt.Sprintf` 后整体是一条 `IsMeta` user 消息，`Origin:"peer"`。）

### 3.5 表面端口（各改一处现有文件）

- TUI（`pkg/tui/notify.go`）：`Publish: s.publishTUIRunEvent`；`Wake: s.wakeForPeer`——形状照 `continueAfterUsageLimit`：未挂 UI → `ErrPeerWakeUnavailable`；否则 `s.notifyUI(PeerWakeMsg{SessionID, Prompt, MsgID})`。事件循环收到 `PeerWakeMsg`：会话不是当前会话或当前会话已有 run → 走 `Control.FollowUp`/返回（由 process 侧兜底：返回错误时 process 改为拒收）；否则**直接构造** `turn.TurnRequest{SessionID, UserText: 信封, Origin: turn.Origin{Surface: turn.SurfaceTUI, ChannelID: "peer"}, Trigger: "peer"}` 并让 `turn.TurnInput.Origin = state.MessageOriginPeer`（沿用 TUI 开轮的现有调用，但**跳过** `turn.Execute`/`SkillReferences`/附件处理）。`PeerWakeMsg` 与 `AutoContinueDueMsg` 定义在同一文件。
- gateway（`pkg/gateway/run_control.go`）：`Wake` → `s.startDetachedTurn(detachedTurn{SessionID, Prompt: 信封, Trigger: "peer", Origin: turn.Origin{Surface: turn.SurfaceWebChat, ChannelID: "peer"}, Unattended: true, PromptOrigin: state.MessageOriginPeer})`；`Publish: gw.publishGatewayRunEvent`。在 `serve_run.go` 的 `StartPeers` 调用处把两者传进去。
- 已有的 origin 渲染（TUI `commands.go:288-292`、网关 `api_extra.go:2306`）本计划不改，peer 行先显示成标题为 `peer` 的系统消息；样式在 199/200。

## 4. 步骤

1. **类型与落库**：改 `pkg/llm/llm.go`、`pkg/state/message_parts.go`、`pkg/turn/session.go`、`pkg/run/controller.go`、`pkg/run/turn_input.go`、`pkg/run/orchestration_llm.go`。先 `grep -rn "json.Marshal(m\b\|json.Marshal(msg\b\|json.Marshal(messages" pkg/llm` 确认没有 provider 把 `llm.Message` 整体序列化发出去（有就 STOP）。测试：带 `Origin:"peer"` 的消息经 `MessagePartsJSON` 写出 `is_meta` 与 origin 两个部件、`state.MessageOrigin()` 读回 `peer`、`ParseMessage…` 读回的消息仍 `IsMeta=true` 且发给模型的部件不含 origin；插话 entry 带 origin 时 `steerMessages` 产出 `IsMeta=true`。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/state ./pkg/run ./pkg/turn -count=1` → ok。
2. **子代理入口与事件类型**：`steerRunningSubagent` 与 `SteerFromAgent` 已由 202 交付，本步**只补**它们未被覆盖的用例（`steerRunningSubagent` 不改 surface、空闲返回 `ErrSubagentNotRunning`；若 202 已有则跳过并在报告里说明）；加两个事件常量与 payload（`RunEventPeerMessageHeld` / `RunEventPeerMessageSettled`，形状见 §3.1）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/event -count=1` → ok。
3. **收件流水线**：新建 `pkg/process/peer_inbound.go` + `peer_inbound_test.go`，替换 195 的占位收件；`PeerSurface` 加 `Publish`/`Wake`；导出 `ReleasePeerMessage`/`DenyPeerMessage`/`HeldPeerMessages(sessionID)`。测试用两个 `peer.Server`（一个当收件进程的服务、一个当发送方回执地址）+ 假 `Wake`/`Publish`，覆盖：
   - 空闲 TUI 会话收到 → `Wake` 被调用一次，`PeerPrompt.Text` 等于信封原文；无回执。
   - 运行中（`Control.Begin` 一个 run 并 `TrackRuntime`）→ 进了插话队列，`Wake` 未调用。
   - 信封带 `from-mode="prompting"`、目标会话 `ApprovalNever` → 回 `held`，`Publish` 收到 `peer_message_held`；`ReleasePeerMessage` 后投递并回 `delivered`；另一条 `DenyPeerMessage` 回 `denied`；关闭服务时剩余挂起回 `expired`。
   - **入站策略（always-on，不读配置）**：信封带 `from-mode="prompting"`、目标会话 `ApprovalNever` → 回 `held`；信封不带 `from-mode`、目标会话 `ApprovalNever` → 回 `held`（`no-mode-asserted`）；信封不带 `from-mode`、目标会话 `prompting` → 直接投递、无回执。
   - 第 31 条瞬时来信 → `dropped` + `drop_reason:"rate-limited"`；同正文 30s 内 → `duplicate`；hop-chain 含 10 个自身 token → `hop-loop`。
   - 带 `forebrain.to` 指向不存在的代理 → 拒收「unknown agent」。
   - gateway 路由：两条假会话（一个 cwd 为发送方 cwd 的子目录、更新更近；一个不相关）→ 选前者；都不同树 → 拒收「no conversation in this work tree」；跑批会话被跳过。
   - 子代理已结束 → 主代理收到两条消息（转交说明 + 信封）。
   - 信封解析失败的来信 → 模型文本是新包的信封且正文已转义。
   - `notify_when_idle`：空闲时立即收到 `peer_idle_notice{state:"idle"}`；忙时登记、变闲后收到；关闭服务时收到 `state:"exited"`。
   - **CC 模式**（开关打开、四个覆盖变量都设、CC 目录已建）：发送方记录只在 CC 目录、不带扩展块 → 照常投递；回执的 `from` 是 CC 地址。另一条挂起中时把配置快照改成关闭 → 那条回 `expired`（`from` 是旧的 CC 地址，在旧 socket 关闭前发出）、`Publish` 收到 `settled{expired}`；回到 FB 模式后，FB 目录里带扩展块的发送方发来的信照常投递。
   - **开关关闭时 Claude Code 形状的发送方**：只在 FB 目录写一条不带扩展块的记录、往 FB socket 发 → 没有回执、`Wake`/`Publish` 都没被调用。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run Peer -count=1` → ok。
4. **常量 golden**：`peerForwardedToDispatcherNote` 写 golden 测试（参照 `pkg/agent/scope_discipline_test.go` 对系统提示常量的写法），并断言不含 `Claude`、`Anthropic`、`GPT`、`Codex`、`OpenAI`。
   验证：同上包 ok。
5. **表面端口**：改 `pkg/tui/notify.go`（+ 事件循环处理 `PeerWakeMsg` 的那一处，与 `AutoContinueDueMsg` 的处理同文件/同函数族）与 `pkg/gateway/run_control.go`、`pkg/gateway/serve_run.go`。TUI 用例：`PeerWakeMsg` 开的轮的 user 行 `parts` 含 `is_meta` 与 `origin:peer`，且正文里的 `/frontend-design`、`@README.md` **没有**触发技能或附件（参照 `pkg/tui/chat_session_test.go` 里自动续跑开轮的用例）。网关用例：唤醒开的 detached run 的 user 行带 origin peer（参照心跳开轮的现有用例）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -count=1` → ok。
6. **手测（scratch home 的 TUI，临时项目目录）**：按 195 §4 第 6 步的方式起 TUI A（四个覆盖变量，`$P/cc-sessions` 先不建），用 `scripts/acceptance/` 现有的 fake provider（`scripts/acceptance/fake_provider.py`）让它能跑轮。帧：`{"msgV":1,"msg_id":"<uuid>","type":"user","message":{"role":"user","content":"hello"},"priority":"next","from":"uds:/tmp/cc-socks/1.sock"}`。
   - 开关保持默认关闭：在另一个窗口用 `socat`/`nc -U` 往 A 的 socket（`$P/fb-socks/<pid>.sock`）写这一帧 → A **没有任何反应**（来源闸门：`nc` 不是登记过的 forebrain）。
   - `mkdir "$P/cc-sessions"`，在 A 的 `/config` 打开「与 Claude Code 互通」并让 A 跑一轮；≤5s 后 A 的 socket 换到 `$P/cc-socks/<pid>.sock`。往新 socket 重发（换一个 `msg_id`）→ A 空闲时自动开一轮，画面出现一条 peer 系统消息；`sqlite3 <A 的 home>/state/forebrain.state.sqlite "select parts from fb_messages order by id desc limit 3"` 能看到 `is_meta` 与 `origin` 部件。
   - forebrain 之间、开关关闭时的投递由第 3 步的单测覆盖，真机在 197 用 `peer_send` 验。
7. **收口**：`go vet ./...`；`bash scripts/package-graph.sh`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`。

## 5. 边界

- **只动**：`pkg/llm/llm.go`、`pkg/state/message_parts.go`、`pkg/turn/session.go`、`pkg/run/{controller,turn_input,orchestration_llm,subagent}.go`、`pkg/event/run_events.go`、`pkg/process/peers.go`、`pkg/process/peer_inbound.go`（新）及以上各自的测试、`pkg/tui/notify.go`（+ 事件循环那一处）、`pkg/gateway/run_control.go`、`pkg/gateway/serve_run.go`、`pkg/architecture/testdata/graph.json`。
- **不动**：表面卡片样式（199/200）；发送（197）；用户亲手发消息时的忙判定（`SessionBusy` 对用户输入的拒绝语义原样保留）；跑批、心跳、cron 开轮路径。
- 不新建 `pkg/gateway` 文件；`pkg/run`、`pkg/tool`、`pkg/tui`、`pkg/gateway` 不新增 import。

## 6. 完成判据（机器可查）

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...` 无输出。
2. `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` 绿（扇出不增）。
3. `grep -rn 'SkillReferences\|turn.Execute(' pkg/process/peer_inbound.go` 无输出。
4. §4 第 6 步手测输出贴进本计划文末「执行记录」。
5. `plans/README.md` 本行状态更新。

## 7. 测试计划

见 §4 各步。新测试文件：`pkg/process/peer_inbound_test.go`。其余在既有测试文件里加用例（`pkg/state/message_parts_test.go`、`pkg/run/turn_input_test.go` 或 `orchestration_llm_test.go`、`pkg/run/subagent_test.go`、`pkg/tui/chat_session_test.go`、网关心跳用例所在文件）。

## 8. 维护注意

- `llm.Message.Origin` 只给落库与表面用；任何 provider 适配器都不得读它。新增 provider 时在评审里查这一条。
- 挂起缓冲只在内存：进程退出就回 `expired`，与 Claude Code 一致；**不要**为它加表（未放行的消息不算会话数据）。
- 网关路由依赖 195 的 `RecentlyActiveSessions`；路由规则改动要同步设计 §5.1。
- `notify_idle` 特性的发布开关在 201；在那之前 Claude Code 照理不会对 forebrain 发订阅，发来了也按 §3.2 处理。
- 自闭环：forebrain 之间的收件在两种登记模式下都必须成立，且不依赖任何 Claude Code 进程；新增的入站逻辑要在「开关关闭」下有用例。

## 9. STOP 条件

- 某个 provider 适配器把 `llm.Message` 整体 JSON 序列化后发出（会把 `origin` 漏给模型）。
- `Controller.Steer` 在 `Running` 时返回 false 的情况多于「无输入队列」（说明插话语义已变）。
- TUI 事件循环里开轮必须经过 `turn.Execute`、绕不开技能识别——停下报告，不要在 `turn.Execute` 里加「如果是 peer 就跳过」的分支。
- 网关路由需要新建 `pkg/gateway` 文件。
- 任何测试写到了真实 `~/.claude/sessions`、`/tmp/cc-socks`、`/tmp/fb-socks-<uid>` 或真实 FB 目录。

## 追加裁决（owner 2026-10-31）：跑批任务内代理间消息不受「跑批不收输入」拦截

`SendToSubagent`（`pkg/run/subagent.go:2211-2217`）今天在最底层对跑批会话一律返回 `ErrBatchTakesNoInput`。这道拒绝的本意是「没人能加入跑批任务」——它属于**人的入口**，不属于代理之间。修订：

1. 把跑批拒绝挪到人的入口：TUI 子代理视图（`pkg/tui/chat_session.go:1567-1582`）与网关子代理视图接口（`pkg/gateway/api_extra.go:2992-3001`）各自先查项目类型并拒绝；`SendToSubagent` 本身不再判断项目类型（或拆出一个供代理发送方调用、不经人入口检查的内部函数），使本计划的进程内投递（主代理 `peer_send` → 子代理 `queue.Steer`）在跑批会话里可用。
2. 不变：跨进程入站到跑批会话仍拒收（本计划「目标会话是跑批 → 拒收」与设计文档第 241 行）；人从终端 / 网页给跑批会话或其子代理视图发消息仍拒收；跑批主会话的输入队列照旧关闭（`pkg/process/run_executor.go:205-207`）。
3. 测试：人从子代理视图给跑批子代理发消息 → 仍 `ErrBatchTakesNoInput`；同一子代理收主代理 `peer_send` → 插话送达。

**追加裁决（owner 2026-10-31）：跨进程协同固定开启（always-on）**——不能关闭、不支持关闭、不支持配置；唯一的开关是「与 Claude Code 互通」（`peers.claude_code`，默认关闭，用户可手动打开，只存库，装了 Claude Code 才显示）。本计划受影响：入站策略不再读配置 `peers.inbound`——一律按收件会话的审批模式推导（自身模式＝`ApprovalNever` ? `bypass` : `prompting`，与来信 `FromMode` 相同 accept、不同 hold）；测试里「`peers.inbound="refuse"`」一例删除。

**与 202 的关系（2026-10-31）**：进程内主代理 ⇄ 子代理的投递由 202 先交付——`steerRunningSubagent`（从 `SendToSubagent` 抽出的内部函数，`SendToSubagent` 保留人入口的跑批拒绝）与 `InputQueue.SteerFromAgent`（不受关闭队列拦截）。本计划的进程内插话改用这两者，不再另建 `SteerSubagentFromPeer`；上文「把跑批拒绝挪到人的入口」即由 202 的拆分实现。
