# Plan: TUI 子代理视图收到的 peer 消息画成与主视图一致的「↘ Received a message from <sender>」卡

> **Executor instructions**: 按步骤顺序执行，每步跑验证命令并确认预期结果后再继续。
> 命中「STOP conditions」任一条就停下报告，不要即兴发挥。本计划经 owner 批准，由
> **执行者子代理在独立 git worktree 实施**（见 Git workflow）：只改 Scope in-scope 文件，
> 在 worktree 自己分支上 commit，不 push/merge/碰其它分支。计划文件/索引/Implementation
> Status 由 advisor（tech lead）维护，执行者跳过（含 Step 0）。
>
> **Drift check（先跑）**: 基线 commit `4d65d015`（2026-11-20；计划初稿写于 `4003bcb7`，
> 其间两个**out-of-scope** commit 已落定：`d3aa95ed` pkg/llm catalog、`4d65d015`
> subagent_continue effort pinning——均不碰本计划目标函数）。派发前工作树已干净
> （`git status --porcelain` 空）。drift 检查**以本计划「Current state」摘录对照活代码
> 内容**为准（不用全文件 SHA diff）：只核对 `publishInputDelivered`、`peerSendToDispatcher`、
> `peerSendToSubagent`、`SubagentInputDeliveredPayload`、`SubagentInputDeliveredMsg`、
> reducer 的 `case SubagentInputDeliveredMsg`、`peerOriginCard` 摘录是否仍匹配。
> 内容不符 = STOP；仅行号漂移不算 drift。advisor 已在 `4d65d015` 逐一核实匹配。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改共享事件契约 + 引擎送达路径 + TUI reducer；含一处刻意的主视图行为修订）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `4d65d015`, 2026-11-20（初稿 `4003bcb7`；基线随两个 out-of-scope commit 前进，目标函数内容未变）
- **执行模型**: owner 审批 hand-off 指定 **deepseek/deepseek-flash 执行者子代理在独立 git worktree 实施**（`.worktrees/fix-subagent-view-peer-message-card`，从 HEAD 拉同名分支），advisor 任 tech lead 评审。此 hand-off 覆盖下文 Git workflow 早先「不用 worktree」一句；合并回主树由 owner 决定。
- **Scope 裁决**: owner 2026-11-20 选 **A 全族归一**——含进程内 peer_send 双向补 origin，
  并签收随之而来的主视图 turn 路由归一（进程内来信跑独立 peer turn，见行为矩阵行4/连带语义）。
- **评审**: 独立 plan 子代理冷读证实因果链与假设 a/c/d/e；MED-1（agent-message 不走 Parse）
  与 MED-2（turn 路由）已折叠进本计划。

## Why this matters

用户截图：TUI **子代理视图**里收到来自其他代理的 peer 消息，被画成「历史用户消息卡」
（`›` 前缀 + 上下横线 + 原样 `<cross-session-message …>` 信封），而**主代理视图**收到
同类消息画的是「↘ Received a message from <sender>」系统卡（解包后的正文、Markdown）。
两端不一致，且子代理视图把传输信封原样暴露给读者。根因是 peer 消息的 origin 在
「子代理送达事件」这条链上被逐环丢弃，reducer 无条件画用户卡。修复把这条链归一到
主视图已在用的唯一卡定义 `peerOriginCard`，让所有 peer 消息（跨进程来信、进程内
peer_send 插话）在两个视图、实时与回放都画同一张_sender 卡_。

## Current state（摘录均来自 advisor 本会话亲自读取）

### 主视图的正确 UI（唯一卡定义，三处复用，本计划不改它）

`pkg/tui/commands.go:351` `peerOriginCard(origin, content)`：

```go
func peerOriginCard(origin, content string) (Message, bool) {
	switch strings.TrimSpace(origin) {
	case state.MessageOriginPeer:
		title := "Received a message from another agent"
		body := content
		if name, displayBody, ok := process.PeerEnvelopeDisplay(content); ok {
			if name != "" { title = "Received a message from " + name }
			body = displayBody
		}
		return Message{Kind: MsgKindSystem, Title: title, Content: body, MarkdownBody: true, Marker: "↘"}, true
	case state.MessageOriginPeerStatus:
		return Message{Kind: MsgKindSystem, Title: "Delivery", Content: peerDeliveryBody(content), MarkdownBody: true}, true
	}
	return Message{}, false
}
```

三处调用：`pkg/tui/run.go:3288`（live busy，`renderDeliveredInputCards`）、
`pkg/tui/run.go:4426`（live idle，`executePeerTurn`）、`pkg/tui/commands.go:319`（replay
带 origin 的 user 行）。解包器 `process.PeerEnvelopeDisplay`（`pkg/process/peer_display.go:10`
→ `peer.DisplayText`）同时认识 `cross-session-message`（跨进程）与 `agent-message`
（进程内 main⇄subagent）两种信封。

### 缺陷链（子代理视图）

1. **引擎送达事件丢 origin、压平换行** — `pkg/run/subagent.go` `publishInputDelivered`
   （约 1017–1035；`beginAttempt` 的 `rt.AddChangeHook` 与 `awaitUserMessage` 都调它）：

   ```go
   for _, entry := range delivered {
       if entry.Mode != TurnInputModeSteer { continue }
       text := strings.Join(strings.Fields(llm.TextWithoutImageSourceNotes(entry.Parts)), " ")
       if text == "" { continue }
       publishEvent(context.Background(), own.Events, ch.runID, ch.sessionID,
           event.RunEventSubagentInputDelivered, event.SubagentInputDeliveredPayload{
               AgentID: ch.agentKey, ExecutionID: ex.executionID, Text: text,
           })
   }
   ```

   `entry.Origin` 在此被丢弃；`strings.Fields` 把换行压成空格。对照：主视图的
   `event.InputDeliveredPayload`（`pkg/event/run_events.go:568`）**带 Origin**（plan 200 §3）。

2. **事件 payload 无 Origin 字段** — `pkg/event/run_events.go:553`：

   ```go
   type SubagentInputDeliveredPayload struct {
       AgentID     string `json:"agent_id,omitempty"`
       ExecutionID string `json:"execution_id,omitempty"`
       Text        string `json:"text,omitempty"`
   }
   ```

3. **TUI 两处映射不带 origin** — live `pkg/tui/notify.go:1051` 与 replay
   `pkg/tui/commands.go:1617` 都构造：

   ```go
   SubagentInputDeliveredMsg{AgentID: strings.TrimSpace(p.AgentID), RunID: evt.RunID, Text: strings.TrimSpace(p.Text)}
   ```

   msg 结构 `pkg/tui/notify.go:505` 只有 `AgentID/RunID/Text` 三字段。

4. **reducer 无条件画用户卡** — `pkg/tui/reducer.go` `case SubagentInputDeliveredMsg`
   （约 1845–1873）：

   ```go
   text := strings.TrimSpace(m.Text)
   agentID := strings.TrimSpace(m.AgentID)
   if text == "" || agentID == "" { return EventResult{} }
   if r.tracker != nil { r.tracker.ObserveAgentPaused(agentID, false) }
   r.updateSubagentStatus(agentID, "running")
   frames := []Frame{{
       Kind: FrameUser, Title: "you", Content: text,
       RunID: strings.TrimSpace(m.RunID), AgentID: agentID, Final: true,
   }}
   return EventResult{Frames: append(frames, r.setCardTaskPaused(agentID, false)...)}
   ```

   live 与 replay 共用这一分支 → 两种路径都画用户卡（截图形状）。

### origin 的来源（子代理 steer）

- **跨进程来信→子代理**：`pkg/process/peer_inbound.go:701-704`
  `run.SteerLiveSubagent(..., peerInput(PeerPrompt{Origin: state.MessageOriginPeer, ...}))`。
  `peerInput`（`peer_inbound.go:792`）带 `Parts: []llm.ContentPart{llm.Text(prompt.Text)}`
  （原文逐字）+ `Origin`。origin 经 `turn_input.go:864` / `steerEntries`（`subagent.go:1130`）
  一路保留到 `entry.Origin` —— **只在 publishInputDelivered 被丢**。
- **进程内 peer_send（双向）origin 为空**：`pkg/run/peer_tools.go`
  `peerSendToDispatcher`（约 353–362）`queue.SteerFromAgent(Input{Text: text})` 与
  `peerSendToSubagent`（约 371–383）`steerRunningSubagent(ch, Input{Text: text})`，
  两处都**不带 Origin、不带 Parts**（`peer_tools.go` 全文无 `Origin`）。`text` 是
  `peerMessageText`（`peer_tools.go:408`）包的 `<agent-message from="…">…</agent-message>`。
  这是 plan 202 §277 明确留给 199/200 的界面后果，199/200 只覆盖了带 origin 的行，
  进程内插话至今漏网。

### 两个 load-bearing 细节

- **信封换行契约**：`peer.Build`/`Parse`（`pkg/peer/envelope.go:40` 正则）要求
  `>\n<body>\n</cross-session-message>`；`Parse` 是**线协议读器，只认 cross-session-message，
  不认 agent-message**（`pkg/peer/display.go:16-19`：agent-message「never travels the socket」）。
  模型 steer 路径不调 `Parse`——进程内 `<agent-message>` 模型本就按 plain text 读。
  `cleanInputText`（`turn_input.go:1184`）压平 `in.Text`；进程内 peer_send 不带 Parts →
  `steerParts` 用压平后的 Text 建 Parts → 信封正文换行丢失。`peerInput` 带逐字 Parts
  （`Parts:[llm.Text(prompt.Text)]`）正是为避开压平。`readLooseLevel`（`display.go:170`）
  不要求换行，压平信封仍能解包，但正文段落变空格——要与主视图卡正文（保留换行/Markdown）
  一致，payload 必须逐字携带文本。`llm.TextContent`（`llm.go:411`）逐字拼接、保留内部换行。
- **publishInputDelivered 有三个调用点**（同函数，改一处全覆盖）：`subagent.go:1005`
  （beginAttempt change hook）、`1104`（awaitUserMessage pause→wake）、`1312`
  （interrupt-to-send resume）。
- **FrameSystem + AgentID 在子代理视图可渲染**（无需改 renderer）：既有先例
  `pkg/tui/reducer.go:1749` `ResponseNoticeMsg` 发
  `Frame{Kind: FrameSystem, Content: m.Text, AgentID: agentID, Final: true}`。

### 复现证据（advisor 已跑红，临时测试跑完即删；实施时作为回归测试重建）

命令（`PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`，
`FOREBRAIN_HOME="$(mktemp -d)"`，`CGO_ENABLED=1`，`-tags fts5`）：

- `go test -tags fts5 ./pkg/run -run TestZZRepro -count=1` 红：
  - `TestZZReproSubagentDeliveredPayloadKeepsOrigin`：
    `REPRODUCED: delivered payload origin=<nil>, want "peer" (payload={"agent_id":"task-zzrepro","execution_id":"…","text":"<cross-session-message from=\"uds:/tmp/a.sock\" from-name=\"builder\"> line one line two </cross-session-message>"})`
  - `TestZZReproInProcessPeerSendCarriesOrigin`：
    `REPRODUCED: main->subagent peer_send origin="" want peer`
- `go test -tags fts5 ./pkg/tui -run TestZZRepro -count=1` 红：
  - `TestZZReproSubagentPeerDeliveryDrawsSenderCard`：
    `REPRODUCED: peer envelope drawn as user card: kind=user title="you" content="<cross-session-message …>\nplease review a.go\nsecond line\n</cross-session-message>"`（与截图逐字同形）

因果链每环已对照源码核实；竞争解释已排除：不是 renderer 按视图区别画 FrameUser
（主视图对 peer origin 从不产 FrameUser，见 green 测试
`TestDeliveredPeerInputDrawsItsCardWithoutPanicking`，`chat_session_test.go:9741`）；
不是 live/replay 路径分叉（两者共用同一 reducer 分支）；不是 DisplayText 解不了信封
（主视图解同一信封正常）。

## Commands you will need

所有 go 命令前置：`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`
（直连 go 编译 pkg/pdf 需要；Makefile 只在 make 内设）。测试用_scratch home_：
`FOREBRAIN_HOME="$(mktemp -d)"`。CGO 与 fts5 是硬要求。

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 定点（引擎） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentDeliveredPayload\|InProcessPeerSend\|PeerSend' -count=1` | 全 PASS |
| 定点（TUI） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'SubagentPeerDelivery\|PeerRow\|DeliveredPeerInput' -count=1` | 全 PASS |
| 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/event -count=1 -timeout 30m` | ok（pkg/tui 全量约 10m，务必 `-timeout 30m`，默认 10m 会假红） |
| vet（CI 同款） | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| gofmt（CI 同款） | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| 真机构建 | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | 产出二进制（漏 fts5 会 `no such module: fts5`，那是工具链错不是产品 bug） |

**协调注意**：另一会话 forebrain-harness-30 正在改 `pkg/llm/catalog.go`/`catalog_test.go`，
其 Step1→2 之间 `pkg/llm` 有一个故意 RED 的回归测试
`TestCatalogSupplementFillsAbsentReasoningOptions`（短暂窗口）。**不要跑全树
`go test ./...`**（会撞该窗口假红）；包级回归只跑上表三个包。`go vet ./...` 只编译不跑
测试，若编译期因其在改文件报错，先 peer_send 问它，不要自行修 pkg/llm。

## Scope

**In scope（唯一可改的文件）**：
- `pkg/event/run_events.go` — `SubagentInputDeliveredPayload` 加 `Origin` 字段。
- `pkg/run/subagent.go` — **仅** `publishInputDelivered`（携带 Origin + 逐字文本）。
- `pkg/run/peer_tools.go` — `peerSendToDispatcher`、`peerSendToSubagent` 两处 Input
  加 `Origin: state.MessageOriginPeer` + `Parts: []llm.ContentPart{llm.Text(text)}`。
- `pkg/tui/notify.go` — `SubagentInputDeliveredMsg` 加 `Origin`；`RunEventSubagentInputDelivered`
  映射透传 `p.Origin`。
- `pkg/tui/commands.go` — replay 映射（约 1617）透传 `p.Origin`。
- `pkg/tui/reducer.go` — `case SubagentInputDeliveredMsg` 经 `peerOriginCard` 路由卡。
- 测试：`pkg/run/subagent_test.go`（或新建 `pkg/run/peer_delivery_origin_test.go`）、
  `pkg/run/peer_tools_test.go`、`pkg/tui/reducer_test.go`（或 `commands_test.go`）新增回归用例。
- `docs/plan/PEER_SUBAGENT_VIEW_CARD_PLAN.md`（第 0 步落盘本计划）。

**Out of scope（看似相关但明确不碰）**：
- `pkg/run/subagent.go` 的 effort-pinning 相关代码（`applySubagentContinueModel`、
  `handOffModelID`、`subagentModelOverrideForRef`、SubagentInput/SendInput/Task 的
  reasoning_effort 描述文本，已 commit 于 `4d65d015`）与 `subagent_test.go` 对应用例
  —— 非本修复目标，不碰。
- `pkg/run/subagent.go:1211` `subagentAttemptText` 的另一处 `strings.Fields` 压平
  —— 那是 resume attempt 的 task 文本，与送达显示无关，不碰。
- `pkg/tui/commands.go` 的 `peerOriginCard` 本体 —— 复用，不改（改了会动主视图）。
- 主视图三条 peer 卡路径（`run.go:3288/4426`、`commands.go:319`）—— 已正确，不碰。
- `pkg/peer/display.go`、`pkg/peer/envelope.go`、`pkg/process/peer_display.go`、
  `pkg/process/peer_inbound.go` —— 解包/入站已正确，不碰。
- **web 前端**（`frontend/src/composables/useChatStream.ts:4618` 把
  `subagent_input_delivered` 画成 user block，同缺陷）—— 用户本次只要 TUI；web 改动按
  199/200 惯例须先出原型，列为后续。payload 加 `Origin` 字段向后兼容，为 web 后续铺路。
- `pkg/gateway` —— 无该事件专门消费（grep 证实），不碰。
- `pkg/llm/**`、`FOREBRAIN.md`、`docs/plan/CONNECT_HOME_REASONING_OPTIONS_PLAN.md`
  —— forebrain-harness-30 的 scope，零交集。

## 行为变化矩阵（旧 → 新；★ = 刻意修订）

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 跨进程来信→运行中子代理（截图） | 子代理视图：`› you` 用户卡 + 原样信封 | 子代理视图：`↘ Received a message from <name>` 系统卡，解包正文保留换行/Markdown |
| 同上，resume/回放 | 同上（事件 replay 共用分支） | 同上（payload 带 origin 后 replay 一致） |
| 进程内 main→subagent `peer_send` | 子代理视图：`› you` 用户卡 + 原样 `<agent-message>` | 子代理视图：`↘ Received a message from parent` 卡 ★ |
| 进程内 subagent→main `peer_send` | 主视图 live：note 被 `takeDeliveredSteersForDisplay` 丢弃（nil Payload + 空 origin）；回放：原样 `you` 行；释放时经 `releasedSubmissions` 合成 you submission **并入下一 turn** | 主视图 live + 回放：`↘ Received a message from <taskID>` 卡；释放时归一跨进程 peer 语义——`releasedSubmissions`（`run.go:3186`）跳过 peer → `parkQueuedPeerTurns`（`run.go:3215`）→ `executePeerTurn`/`DispatchPeerTurn` **跑独立 peer turn**（不再并入下一 turn；Esc 撤回不再折进可编辑草稿）★（修复主视图既有缺口，归一 199/200；turn 拆分经 owner 2026-11-20 选 A 签收） |
| 用户在子代理视图亲手发的 steer | `› you` 用户卡 | 不变（origin 空 → 仍 FrameUser）；正文换行现在保留（与主视图 DisplayText 一致）★微修订 |
| 旧事件（无 origin 字段）回放 | `› you` 用户卡 | 不变（向后兼容；与主视图旧行无 origin 同语义） |
| 子代理完成报告→main（`one_shot.go:112` 已带 origin=peer） | `↘` 卡 | 不变 |
| 模型侧读进程内 peer_send 信封 | 压平后正文换行丢失；`<agent-message>` 模型按 plain text 读（`peer.Parse` 是线读器，不认 agent-message，steer 路径不调 Parse） | 逐字 Parts，正文换行保留；`steerMessage`（`orchestration_llm.go:194-200`）标 `IsMeta=true`+`Origin=peer`，模型按上下文读 ★（非「Parse 往返」——agent-message 从不走 Parse） |

连带语义（均为既有 peer 语义的归一，非新造；与跨进程来信早已一致）：进程内 note 带
origin=peer 后——
- `steerMessage`（`orchestration_llm.go:194-200`）标 `IsMeta=true`+`Origin`。IsMeta 下游：
  remote compaction 选最近 user 消息跳过 meta（`responses_llm.go:1273`）、plan-reminder
  turn 计数（`config.go:970/1056`）、input rail `latestSentUserText`（`llm_middleware.go:724`）、
  `ExtractReasoningText` 边界（`message_parts.go`）。
- `newestLocked`（`turn_input.go:945`）跳过 peer（用户不可 recall 他人来信）；queue preview
  的 `Editable`（`turn_input.go:1166`）排除 peer。
- **turn 路由（MED-2，owner 选 A 已签收）**：释放路径 `releasedSubmissions`（`run.go:3186`）
  跳过 peer origin → `parkQueuedPeerTurns`（`run.go:3215`）→ `setPeerDue` → `executePeerTurn`
  → `DispatchPeerTurn`（`chat_surface.go:70-88`，写 IsMeta+peer 行、跑独立模型 turn）。
  触发点：`nextAutomaticSubmission` 的 BoundaryCompleted/Interrupted/InterruptToSend
  （`run.go:4525-4556`）与 Esc 撤回 `restoreWithdrawnWork`（`run.go:3469`）。即进程内 note
  从「并入下一 turn」归一为「独立 peer turn」，与跨进程来信一致；多条排队时 turn 数可能增加。
- **卡正文形状（LOW-1，非回归）**：主视图 live/idle 卡用 `in.Text`（`renderDeliveredInputCards`
  `run.go:3288`、`executePeerTurn:4426`），`in.Text` 入队时被 `cleanInputText` 压平 →
  进程内 note 的**主视图↘卡正文段落仍变空格**；子代理视图卡用逐字 payload → 保留换行。
  跨进程主视图卡今天同样压平，故非回归；本计划不改主视图入队压平（超 scope）。

## Git workflow

- **执行模型（owner 审批 hand-off）**：执行者子代理在独立 worktree
  `.worktrees/fix-subagent-view-peer-message-card`（从 HEAD `4d65d015` 拉同名分支）实施，
  所有读/改/命令/commit 都在该 worktree 内。**在 worktree 自己的分支上 commit**（按步骤
  或逻辑单元；message 风格匹配仓库 conventional commits，如 `fix(tui): draw a subagent's
  peer letter as the sender card`）。**不 push、不 merge、不 rebase、不碰其它分支**；
  合并回主树由 owner 决定。advisor 评审在该 worktree 内复跑判据。
- 派发前主树工作树已干净（effort-pinning WIP 已 commit 为 `4d65d015`）；worktree 从
  HEAD 拉，`publishInputDelivered` 内容与计划摘录一致，无需处理任何并行 WIP。
- 计划文件 `docs/plan/PEER_SUBAGENT_VIEW_CARD_PLAN.md` 可在 worktree 内落盘并随实现
  commit（仓库惯例产物）；advisor 维护会话计划文件与 Implementation Status。

## Steps

### Step 0: 落盘计划到仓库惯例路径（advisor 维护，执行者跳过）
把本计划全文复制到 `docs/plan/PEER_SUBAGENT_VIEW_CARD_PLAN.md`。此步与计划文件/索引
更新同属 advisor（tech lead）职责，执行者按 hand-off 跳过；advisor 在评审收口时落盘。

### Step 1: 事件 payload 加 Origin
`pkg/event/run_events.go` `SubagentInputDeliveredPayload` 加字段，doc 镜像
`InputDeliveredPayload.Origin` 的措辞（plan 200 §3）：
```go
// Origin is the delivered message's own origin, when it had one: a peer
// message handed to a running subagent is another agent's letter, and the
// surface has to draw it as such rather than as the person's own words.
// Empty for everything the person typed.
Origin string `json:"origin,omitempty"`
```
**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/event` → exit 0。

### Step 2: 引擎送达携带 Origin + 逐字文本
`pkg/run/subagent.go` `publishInputDelivered`：把
`text := strings.Join(strings.Fields(llm.TextWithoutImageSourceNotes(entry.Parts)), " ")`
改为 `text := strings.TrimSpace(llm.TextWithoutImageSourceNotes(entry.Parts))`
（保留内部换行，仅去首尾空白）；payload 加 `Origin: strings.TrimSpace(entry.Origin)`。
更新该函数 doc：送达事件按 origin 区分「用户自己的插话」与「他人来信」。
**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0；
`git diff pkg/run/subagent.go` 确认本步改动仅在 `publishInputDelivered`。

### Step 3: 进程内 peer_send 收敛到 peerInput 形状
`pkg/run/peer_tools.go`：
- `peerSendToDispatcher`：`Input{Text: text}` →
  `Input{Text: text, Parts: []llm.ContentPart{llm.Text(text)}, Origin: state.MessageOriginPeer}`。
- `peerSendToSubagent`：`steerRunningSubagent(ch, Input{Text: text})` →
  `steerRunningSubagent(ch, Input{Text: text, Parts: []llm.ContentPart{llm.Text(text)}, Origin: state.MessageOriginPeer})`。
确认文件已 import `state` 与 `llm`（`peer_tools.go` 现 import 块；若缺则补，匹配既有 import 分组）。
**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0。

### Step 4: TUI msg 与两处映射透传 Origin
- `pkg/tui/notify.go:505` `SubagentInputDeliveredMsg` 加 `Origin string`；更新 doc
  （peer 来信画 sender 卡，用户插话画 you 卡）。
- `pkg/tui/notify.go:1051` 构造加 `Origin: strings.TrimSpace(p.Origin)`。
- `pkg/tui/commands.go:1617` 构造加 `Origin: strings.TrimSpace(p.Origin)`。
**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### Step 5: reducer 经 peerOriginCard 路由
`pkg/tui/reducer.go` `case SubagentInputDeliveredMsg`：保留现有 text/agentID 校验与
`ObserveAgentPaused`/`updateSubagentStatus`/`setCardTaskPaused` 簿记不变，只把帧构造改为：
```go
frame := Frame{Kind: FrameUser, Title: "you", Content: text,
    RunID: strings.TrimSpace(m.RunID), AgentID: agentID, Final: true}
if card, ok := peerOriginCard(m.Origin, text); ok {
    frame = Frame{Kind: FrameSystem, Title: card.Title, Content: card.Content,
        MarkdownBody: card.MarkdownBody, Marker: card.Marker,
        RunID: strings.TrimSpace(m.RunID), AgentID: agentID, Final: true}
}
frames := []Frame{frame}
```
（不引入 `withFlushedBuffers`/`agentBuf`：现有分支不封流，peer 来信后子代理继续工作，
与主视图 `renderDeliveredInputCards` 直渲染一致；保持最小改动。）
**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### Step 6: 回归测试（重建 advisor 的红测试为正式回归）
- **引擎 payload**（`pkg/run/subagent_test.go`，仿
  `TestPauseWaitsForTheUserAndContinuesTheSameExecution` 的
  `newPersistenceFixture`/`seedChannelRecord`/`StartSubagentAutoContinue`/`PauseSubagent`/
  `SteerLiveSubagent`/`channelEventProbe` 夹具）：pause→wake 送达一条
  `Input{Parts:[信封原文], Origin: peer}`，断言 `probe.inputDelivered()[0].Origin == peer`
  且 `Text` 含内部换行（`strings.Contains(text, "\n")`）。命名如
  `TestSubagentDeliveredPayloadKeepsPeerOriginAndNewlines`。
- **进程内双向 origin + Parts**（`pkg/run/peer_tools_test.go`，仿
  `TestPeerSendReachesARunningSubagent` 的 `newPeerFixture`/`record`/`running`/`call`）：
  main→subagent 后 `ch.queue.steers[0].Origin == peer` 且
  `steers[0].Parts[0].Text == peerMessageText("parent", message)` 逐字相等
  （`peerMessageText` 是内联信封，无结构换行；若要断言含 `\n`，用多行 message 正文）；
  subagent→parent 后 `owner.Control.SessionQueue(conv).steers[0].Origin == peer`。
  （queue 需 `Attach(NewTurnInputRuntime())` 才进 steers lane，否则进 rejected lane。）
- **TUI reducer sender 卡**（`pkg/tui/reducer_test.go` 或 `commands_test.go`，仿
  `TestReplayPeerRowAgentMessageNamesSenderAndDropsMarkup`，`commands_test.go:5147`）：
  对 `cross-session-message`（`peer.Build`）与 `agent-message` 两种信封，
  `Reduce(SubagentInputDeliveredMsg{AgentID, Origin: peer, Text: 信封})` 产出
  `FrameSystem`、`Title == "Received a message from <name>"`、`Marker == "↘"`、
  `MarkdownBody == true`、`Content` 不含信封标记且含正文换行、`AgentID` 正确。
- **用户 steer 仍 you 卡**：`Origin: ""` → `FrameUser`/`Title "you"`（防回归）。
- **旧事件向后兼容**：`Origin: ""` + 信封文本 → 仍 `FrameUser`（与主视图旧行同语义）。
**Verify**: 上表「定点（引擎）」「定点（TUI）」命令 → 全 PASS，含新增用例。

### Step 7: 包级回归 + vet + gofmt
**Verify**:
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/event -count=1 -timeout 30m` → 三包 ok。
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → exit 0。
- `gofmt -l cmd pkg third_party plugins` → 无输出。
若 pkg/tui 既有断言因「用户 steer 正文保留换行」微修订而红：核对该断言是否钉死了压平
（advisor 核查 `subagent_test.go:5910` 断言单行 `"adjust approach"`，不受影响）；确属
契约修订的随契约更新并注明理由，不得为绿而改回压平。

### Step 8: tmux 真机验收（测试绿 ≠ 真机可用；加载 run-forebrain 技能取 fake-provider 夹具）
构建：`CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`。
用临时 `FOREBRAIN_HOME` + 临时项目目录（**不得污染真实 ~/.forebrain***），tmux 驱动。
场景（按 bug 裁剪，逐屏 `tmux capture-pane -p` 取证）：
1. **复现→修复（进程内）**：fake provider 脚本主代理调 `subagent_run` 派一个长子代理，
   子代理运行中主代理 `peer_send` 给它 → 进子代理视图，断言出现
   `↘ Received a message from parent` 卡、**无** `<agent-message` 原样标记、**无** `› you` 用户卡。
2. **跨进程（截图场景）**：两个隔离 home 进程 A/B；A 派运行中子代理，B `peer_send`
   到 A 的子代理 → A 子代理视图断言 `↘ Received a message from <B-name>` 卡、无
   `<cross-session-message` 原样标记。若 run-forebrain 夹具不支持双子代理跨进程编排，
   以引擎单测（Step 6 跨进程 payload 用例）+ 主视图跨进程真机卡为证，并在此注明限制。
3. **回归（用户插话）**：用户在子代理视图亲手发消息 → 仍 `› you` 用户卡。
4. **回放**：退出重进/resume 该会话 → 子代理视图 peer 来信仍 `↘` 卡（事件 replay 一致）。
5. **主视图对照**：subagent→main `peer_send` → 主视图 `↘ Received a message from <taskID>` 卡。
6. **turn 路由归一（MED-2）**：主代理 idle（无运行中 turn）时子代理 `peer_send` 来信 →
   主视图把来信作为**独立 peer turn** 运行（`executePeerTurn`/`DispatchPeerTurn`），而非并入
   下一手动 turn；取证该 turn 的 prompt 行带 IsMeta+peer、模型被单独调用一次。
验收后清理：`tmux kill-session`、删临时 home/项目目录。
**Verify**: 各场景 capture 文本含/不含上述标记；场景 6 取证独立 turn；把证据回填计划「验收记录」章节。

### Step 9: 收口（执行者）
- 把实现与测试在 worktree 分支上 commit（conventional commits）；`git status --porcelain` 干净。
- `git diff --stat 4d65d015..HEAD` 核对改动范围 ⊆ Scope；`pkg/run/subagent.go` 改动仅在
  `publishInputDelivered`。
- **不 push/merge/rebase**；合并由 owner 决定。计划文件状态行与「验收记录」、会话计划的
  Implementation Status 由 advisor（tech lead）维护，执行者不改会话计划文件。
- 按报告格式回报（STATUS/WORKTREE/STEPS/FILES CHANGED/NOTES），每条断言附本会话工具证据。

## Test plan

- 新用例见 Step 6（引擎 payload origin+换行、进程内双向 origin+Parts、TUI 两信封 sender 卡、
  用户 steer 仍 you 卡、旧事件兼容）。结构仿 `TestPauseWaitsForTheUserAndContinuesTheSameExecution`
  （`subagent_test.go:6332`）、`TestPeerSendReachesARunningSubagent`（`peer_tools_test.go:125`）、
  `TestReplayPeerRowAgentMessageNamesSenderAndDropsMarkup`（`commands_test.go:5147`）。
- 对照基线（应仍 green）：`TestDeliveredPeerInputDrawsItsCardWithoutPanicking`
  （`chat_session_test.go:9741`，主视图 live peer 卡）。
- 验证：定点 + 包级命令（见 Commands 表）全 PASS。

## Done criteria（机器可查，全部成立）

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0。
- [ ] 定点引擎/TUI 测试全 PASS，含 Step 6 新增用例（`go test -run` 过滤命中新用例名）。
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/event -count=1 -timeout 30m` 三包 ok。
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0；`gofmt -l cmd pkg third_party plugins` 无输出。
- [ ] `grep -n "Origin" pkg/event/run_events.go` 在 `SubagentInputDeliveredPayload` 内命中；
      `grep -n "peerOriginCard(m.Origin" pkg/tui/reducer.go` 命中。
- [ ] worktree 内 `git diff --stat 4d65d015..HEAD` 改动文件 ⊆ Scope in-scope 列表；
      `pkg/run/subagent.go` 的改动仅在 `publishInputDelivered`（`git diff 4d65d015..HEAD -- pkg/run/subagent.go`）。
- [ ] worktree 内 `git status --porcelain` 干净（改动均已 commit 到 worktree 分支）。
- [ ] tmux 真机场景 1/3/4/5/6 取证通过（场景 2 按夹具能力，限制已注明）。
- [ ] `docs/plan/PEER_SUBAGENT_VIEW_CARD_PLAN.md` 存在且回填验收记录；未 commit。

## STOP conditions

- 「Current state」任一符号摘录与活代码**内容**不符（drift；仅行号漂移不算）。
- 你的改动不得不触及 out-of-scope 文件，或 `git diff --stat 4d65d015..HEAD` 出现 Scope 外文件 → 停，报告。
- 某步验证合理修复后仍两次失败 → 停，报告（勿改回压平/勿绕 peerOriginCard 凑绿）。
- 修复需要改 out-of-scope 文件（尤其 `peerOriginCard` 本体、主视图路径、web 前端）→ 停，报告。
- 关键假设证伪：`FrameSystem + AgentID` 在子代理视图**不**渲染（与 `reducer.go:1749`
  先例矛盾）→ 停，报告（届时需 renderer 调查，超出本计划）。
- `go vet ./...` 因 forebrain-harness-30 在改的 pkg/llm 编译报错 → 停，peer_send 问它，勿自修。
- 全树测试撞 `TestCatalogSupplementFillsAbsentReasoningOptions` RED → 那是对方窗口，
  不修；本计划验证只跑三包。

## Maintenance notes

- **web 后续**：`useChatStream.ts:4618` 仍把 `subagent_input_delivered` 画 user block；
  payload 已带 `origin`，web 可据此画 sender 卡，但按 199/200 须先出原型——单独计划。
- **向后兼容**：旧事件无 `origin` → 仍 you 卡，与主视图旧行无 origin 同语义；不要为旧事件
  在渲染端做信封嗅探补 origin（会造第二套判定，违「一处一义」）。
- **reviewer 重点**：行为矩阵 ★ 处是刻意修订，归一 plan 202 §277 遗留与 199/200 语义——
  (1) 进程内 note 在主视图从「丢弃/原样 you 行」变 ↘ 卡；(2) 主视图 turn 路由从「并入下一
  turn」归一为「独立 peer turn」（owner 选 A 签收，turn 数/成本可能增加）；(3) 模型侧信封
  正文换行保留 + IsMeta/Origin 标记（agent-message 不走 Parse，勿验「Parse 往返」）。
  确认 `IsMeta`/recall/Editable/compaction 连带语义与跨进程来信一致。
- **逐字文本**：`publishInputDelivered` 去 `strings.Fields` 压平后，用户 steer 的 you 卡
  正文保留换行（与主视图 DisplayText 一致）；`subagentAttemptText`（resume task）的压平
  是另一回事，未动。
- 未来若给 `SubagentInputDeliveredPayload` 再加显示字段，镜像 `InputDeliveredPayload`
  的既有形状，保持两事件契约对称。

## Implementation Status

- **VERDICT: APPROVE**（advisor tech lead 独立复核，2026-11-20）。**已合并回主树 dev**（owner 指示）。
- **合并落地**: owner「合并回主树dev分支」→ 按既定配方执行：baseline 核对（dev=4d65d015=分支基线，
  直接后代）→ 与 343 零文件重叠（peer_send 协调放行）→ 补 `Signed-off-by: wubin1989 <328454505@qq.com>`
  （amend，`c2b42d6f`→`ac09eb14`）→ 主树 `git merge --ff-only`（线性，dev `4d65d015`→`ac09eb14`）→
  主树全门禁重跑绿 → `git worktree remove --force` + `git branch -d`（was ac09eb14）。永不 push。
  343/344 worktree 未动。
- **Worktree（已清理）**: 原 `.worktrees/fix-subagent-view-peer-message-card`，branch
  `fix-subagent-view-peer-message-card`，executor commit `c2b42d6f`（sign-off amend 后 `ac09eb14`）。
  现已 remove/delete；落地 commit = dev HEAD `ac09eb14`。
- **Executor**: deepseek/deepseek-flash（max），general-purpose 子代理。
- **每步状态**: Step 0 跳过（advisor 维护）；Step 1–7 done；Step 8 场景 1/3/4/5/6 done，
  场景 2 受限（夹具无法编排双进程，见下）；Step 9 done（单 commit、树干净）。

### advisor 本人复跑的验证（均在 worktree 内，PKG_CONFIG_PATH+scratch FOREBRAIN_HOME+fts5）

| Done criterion | 结果 |
|---|---|
| `go build -tags fts5 ./...` | exit 0 ✓ |
| 定点引擎 `-run 'SubagentDeliveredPayload\|InProcessPeerSend\|PeerSend'` | ok 0.584s ✓ |
| 定点 TUI `-run 'SubagentPeerDelivery\|PeerRow\|DeliveredPeerInput'` | ok 0.670s ✓ |
| 新用例命中（-v）：`TestSubagentDeliveredPayloadKeepsPeerOriginAndNewlines`/`...LeavesTheUsersOwnMessageUnmarked`/`TestPeerSendMarksAMessageToASubagentAsAPeerLetter`/`...SubagentsAnswerAsAPeerLetter`/`TestSubagentPeerDeliveryDrawsTheSenderCard`(4 子测试) | 全 PASS ✓ |
| 包级 `go test ./pkg/run ./pkg/tui ./pkg/event -count=1 -timeout 30m` | run 45.390s / tui 197.131s / event 0.301s 全 ok ✓ |
| `go vet -tags fts5 ./...` | exit 0 ✓ |
| `gofmt -l cmd pkg third_party plugins` | 无输出 ✓ |
| `grep Origin` payload（run_events.go:562 在 SubagentInputDeliveredPayload 内）/ `grep peerOriginCard(m.Origin` reducer.go:1874 | 命中 ✓ |
| `git diff --stat 4d65d015..HEAD` ⊆ Scope；subagent.go hunk 仅在 publishInputDelivered | 9 文件全 in-scope ✓ |
| `git status --porcelain` | 干净 ✓ |
| tmux 真机证据（/tmp/evidence-{1,4,5-6}.txt）抽读 | 场景 1/3/4/5/6 标记符合 ✓ |

### 合并后主树复跑（dev=ac09eb14，ff-only 内容与上表 worktree 受测 commit 逐字一致）

- `go build -tags fts5 ./...` 无错误 ✓；`go vet -tags fts5 ./...` 无错误 ✓；`gofmt -l cmd pkg third_party plugins` 无输出 ✓。
- `go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/event -count=1 -timeout 30m`：run 46.314s / tui 197.302s / event 0.312s 全 ok ✓。
- `git status --porcelain` 干净；`git worktree list` 仅余主树 dev + 343/344（我方已清理）。

### 真机证据要点（advisor 抽读确认）

- `evidence-1-subagent-view.txt`（子代理视图，footer「viewing general-purpose … esc to return」）：
  `↘ Received a message from parent` + 正文「please also check the router and the tests beside it」，
  `agent-message`/`cross-session-message` 原样标记计数 0；用户亲手 steer 仍 `› user note first line / second line`
  用户卡（换行保留）。**正是截图缺陷的反面，达成用户要求。**
- `evidence-4-replay.txt`：resume 后子代理视图同一 ↘ 卡，标记计数 0。
- `evidence-5-6-main-view.txt`：主视图 `↘ Received a message from subagent-97686ee1-…` + 来信正文；
  每封来信后各跟一个独立「● primary done / ─ Worked for 1s」块（MED-2 turn 路由归一取证）。

### Deviations（均 documented、in-scope、按 merit 接受）

1. 引擎 payload 测试落在计划允许的新文件 `pkg/run/peer_delivery_origin_test.go`（而非 6000 行
   subagent_test.go）——计划 Step 6 已列此可选路径。
2. 额外加一个引擎测试钉「用户自己的消息 origin 为空」——计划测试清单只有 TUI 半边，补全是好事。
3. TUI 测试名取 `TestSubagentPeerDeliveryDrawsTheSenderCard` 以命中 Done-criteria 过滤器。
4. 原生依赖直连下载 stall，执行者用本机代理 `http_proxy=…7897` 跑通三个 fetch 脚本；依赖 gitignored，
   树保持干净。环境 workaround，非产品改动。
5. **场景 2（跨进程双进程）未真机驱动**：run-forebrain 夹具脚本单进程，两个隔离 home 不共享 peer
   socket 目录，第二进程代理不可达。计划已 anticipated 此限制；改由引擎测试（`peer.Build` 产 socket
   信封）+ 既有主视图跨进程卡测试覆盖。**这是本修复唯一的真机覆盖缺口**，但跨进程 origin 携带与
   解包路径已由单测逐环证明，风险低。
6. 函数/事件 doc 措辞随行为更新（peerSend*、payload/msg、两处映射注释）；一处无关句子的 incidental
   改动被执行者回退以保持最小 diff。

### 未做 / 遗留

- web 前端 `useChatStream.ts:4618` 仍画 user block（payload 已带 origin，可后续按 199/200 出原型再改）——
  计划明确 out of scope，留后续单独计划。
- 场景 2 真机编排（见 deviation 5）。
- `peerOriginCard` 本体、主视图三条 peer 路径、`subagentAttemptText`、pkg/peer/pkg/process 信封代码、
  pkg/gateway 均未碰（grep/diff 证实）。

### 复核结论

diff 最小、归一到主视图唯一卡定义 `peerOriginCard`（一处一义），测试断言有力（双信封 sender 卡、
用户 fallback、旧事件兼容、进程内双向 origin+逐字 Parts），scope 干净，质量成立。**APPROVE**。