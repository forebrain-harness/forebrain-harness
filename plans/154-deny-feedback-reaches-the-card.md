# Plan 154：拒绝时用户写的那句话要真的上卡（终端 + 网页）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置 1（否则 STOP 1）——目标文件必须干净**：
> `git status --porcelain pkg/event/tool_events.go pkg/tool/notification.go pkg/tui/chat_surface.go` 必须**无输出**。
>
> **前置 2（否则 STOP 1）——锚点仍在**（行号以本计划的基线为准，以**符号**为准）：
> `grep -n "type ToolCallMeta struct" pkg/event/tool_events.go` 恰好 1 条；
> `grep -n "eventMeta := event.ToolCallMeta{" pkg/tool/notification.go` 恰好 1 条；
> `grep -n "func (s \*ChatSession) notifyInPlaceRefusedCard" pkg/tui/chat_surface.go` 恰好 1 条；
> `grep -n "meta.DenyFeedback = strings.TrimSpace(evt.DenyFeedback)" pkg/tool/format.go` 恰好 1 条。
>
> **Drift check（先跑）**：`git diff --stat bca0157 -- pkg/event/tool_events.go pkg/tool/notification.go pkg/tui/chat_surface.go`
> 必须**无输出**。有输出就按下文"现状"里的**函数名**逐段核对；被引用的代码含义变了即 STOP 1。
>
> **基线（Step 1 之前各跑一次，记下失败清单；否则 STOP 5）**：
> - `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/gateway -count=1`（超时给足 600 s）
>
> 基线里已经红的用例是存量问题，**不是你要修的**；Done criteria 只要求"不新增红"。

## Status

- **Priority**: P1（138 的 Done criteria 有一条永远不成立；owner 2026-10-26 真机验收时发现）
- **Effort**: S–M
- **Risk**: LOW–MED（只加一个字段与两处投影；不改判定、不改拒绝语义）
- **Depends on**: 无
- **Blocks**: 无（但 138 的第二行正文要等它）
- **Planned at**: dev `bca0157`，2026-10-26（137–150 批次已落 dev，工作区干净）
- **原型**：免原型（138 的卡早就画得下两行，缺的只是数据）
- **Category**: bug（数据在引擎里，没送到卡上）
- **修订（2026-10-26 执行中）**：执行者在链路核查触发 STOP 3——网页**主 agent** 的 canonical 事件
  不走 `notification.go` 投影，而走 `pkg/gateway/wsevents.go` 的 `toolCallMetaFromWS`（见「现状 3」），
  该函数同样丢 `deny_feedback`。评审方核实证据后裁决：scope 扩入 `wsevents.go` 一行补读（同一根因、
  同一验收场景），见 Step 2b。`pkg/run/subagent.go` 的 `webSearchEventMeta` 是第三份字段拷贝，但
  provider web search 不可被拒绝，不扩。
- **修订 2（2026-10-26 执行中，STOP 4）**：真机取证显示数据层已闭环（持久化事件带上了
  `deny_feedback`）但终端卡上仍无原话——`pkg/tui/render.go` **从不画** `ToolMeta.DenyFeedback`
  （卡正文只来自 `f.Content`，见「现状 4」）。scope 扩入 `render.go` + render 测试（见 Step 3b）。
- **修订 3（2026-10-26 评审，同类穷举）**：parked 路径的 **live** 卡（`pkg/tui/chat_turn.go`
  `notifyToolApprovalDenied`）构造 meta 时同样用合成 step、不带 `Denied/DenyFeedback`（原话
  `act.Error` 只进了 Content，shell 时为空）——live 卡 `(no output)` 而重载有原话，违反本计划
  「三处形状相同」的自身不变式。scope 扩入 `chat_turn.go` denied 分支 + `chat_session_test.go`
  断言（见 Step 3c）；`canceled`（嵌套 request_permissions）分支不动。

## Why this matters

用户在等待中的工具卡里点「不要运行」，在说明框写下 `改成把它移到 trash/ 目录` 再发送。网页上的结果只有一句：

```
已拒绝  rm -rf scratch138/doomed.txt
  你拒绝了这条命令，它没有运行。
```

**那句原话没有出现**。138 的 Done criteria 明确要求拒绝后两行正文（一句话 + 原话），原型 ⑬⑭ 也是两行。

真机取证（2026-10-26，隔离 home + 真 glm-5.3-flash，gateway 用 `sqlite3` 直接查状态库）：

- `fb_actions`：`status=denied`，`payload_json.reason` 就是用户写的那句 —— 原话**到了**引擎；
- 同一次运行的 `fb_session_events.tool_call_completed.payload_json.tool_meta`：
  `{"status":"denied", ...}`，**没有 `deny_feedback` 键**；
- 因此 `frontend/src/components/chat/ToolCallCard.vue` 的 `refusalWords`（读 `step.denyFeedback`）永远为空，
  只画出 `refusalSentence` 那一句。

同一现象在终端也存在，且原因有两处（见下）。

## Current state

### 1. 引擎侧：原话被投影丢掉了（网页 live 与重载都受影响）

- `pkg/tool/format.go:211` `BuildToolMeta(evt StepEvent) ToolMeta` 在 `evt.Denied` 时**确实**写入了原话：
  ```go
  // The refusal's own words ride the meta so the card that shows the refusal
  // can show them (plan 135); every other call keeps the field empty.
  if evt.Denied {
      meta.DenyFeedback = strings.TrimSpace(evt.DenyFeedback)   // format.go:255-257
  }
  ```
  `pkg/tool/format.go:99` `DeniedToolStep(stepID, toolName, input, feedback)` 也把 `feedback` 放进 `StepEvent.DenyFeedback`。
- 但落到**持久化事件**上的那一层是 `pkg/tool/notification.go:20` 的 `RunEventFromStep`，它把
  `tool.ToolMeta` **逐字段**翻译成 `event.ToolCallMeta`（`notification.go:31-46`）：
  ```go
  meta := EnrichToolMeta(ctx, BuildToolMeta(evt))
  eventMeta := event.ToolCallMeta{
      ToolName: meta.ToolName, Status: meta.Status, Purpose: meta.Purpose,
      Invocation: meta.Invocation, Input: meta.Input, AgentID: meta.AgentID,
      AgentType: meta.AgentType, AgentKind: meta.AgentKind, Category: meta.Category,
      SkillName: meta.SkillName, SkillPath: meta.SkillPath,
      ResultLines: meta.ResultLines, ResultOffset: meta.ResultOffset,
      PreviewKind: meta.PreviewKind, PreviewPath: meta.PreviewPath, PreviewMIME: meta.PreviewMIME,
      PreviewWidth: meta.PreviewWidth, PreviewHeight: meta.PreviewHeight,
      PreviewBytes: meta.PreviewBytes, PreviewScaled: meta.PreviewScaled,
      StartedAtMs: meta.StartedAtMs,
      Origin:       strings.TrimSpace(evt.Origin),
      SubagentCall: meta.SubagentCall,
  }
  ```
  **`DenyFeedback` 不在这个列表里**。
- 而且 `event.ToolCallMeta`（`pkg/event/tool_events.go:62-100`）**根本没有这个字段**：
  `tool_name`/`status`/`purpose`/`invocation`/`input`/`agent_id`/`agent_type`/`agent_kind`/`category`/
  `skill_name`/`skill_path`/`result_lines`/`result_offset`/`preview_*`/`started_at_ms`/`subagent_call` —— 没有 `deny_feedback`。
- 已就绪的两端（**不要动**）：`pkg/gateway/server.go:368-373` 早就把 `meta.DenyFeedback` 塞进审批数据 map；
  `frontend/src/composables/useChatStream.ts:246-247` 早就声明了 `denyFeedback?: string`（注释写着 `tool_meta.deny_feedback`）；
  `frontend/src/components/chat/ToolCallCard.vue:52-61` 早就会画第二行。缺的只有事件这一跳。

### 2. 终端侧：live 卡是从一个**没有标记 Denied、也没有原话**的合成 step 画出来的

`pkg/tui/chat_surface.go` 的 `notifyInPlaceRefusedCard`（in-place 拒绝时终端自己画卡）：

```go
meta := tool.BuildToolMeta(tool.StepEvent{
    Kind: tool.StepKindToolCompleted, StepID: stepID, ToolName: toolName, Input: input,
})
meta.Status = "denied"
s.notifyUI(NewMessageMsg{Msg: Message{
    ...
    Content:   tool.DeniedToolDisplayBody(toolName, strings.TrimSpace(decision.DenyReason)),
    ToolMeta:  meta,
    ...
}})
```

两个问题：`BuildToolMeta` 收到的 `StepEvent` 没有 `Denied: true`（所以 `meta.DenyFeedback` 不会被填），
`DeniedToolDisplayBody` 对 `shell` 返回空串（`pkg/tool/format.go` 该函数 default 分支 `return ""`）——
所以终端的拒绝卡正文也是空的，原话同样不出现。

### 3. 网页主 agent：canonical 事件还有第二个投影（修订新增，2026-10-26 执行中确认）

- 网页 socket 的 step hook（`pkg/gateway/server.go:2094-2185`）里，只有 subagent 步（HookAgentID 非空）
  与 skill 步走 `RunEventFromStep`；**主 agent 自己的 ToolCompleted 走 `writeMsg(Op:"step")`**，
  出站 `tool_meta` map 由 `gatewayToolMetaPayload`（`server.go:366-373`）构造——`deny_feedback` **已经在里面**。
- 但 `writeMsg`（`server.go:1058`）对每条出站消息做 `canonicalRunEventsFromWS` → `Publish` 持久化/广播，
  重建用的 `toolCallMetaFromWS`（`pkg/gateway/wsevents.go:483-527`）逐字段拷贝、**不读 `deny_feedback`**——
  这就是 owner 取证里 `tool_call_completed.payload_json.tool_meta` 缺 `deny_feedback` 键的写入者。
- 前端只吃 canonical `run_event`（live 与 reload 同源），所以修 `notification.go` 对这条链零作用；
  必须在 `toolCallMetaFromWS` 里补一行（Step 2b）。
- 顺带确认（**不在本计划修**，报告给 owner）：出站 map 也发送 `category`/`skill_name`/`skill_path`，
  `toolCallMetaFromWS` 同样不读——同类存量丢失，但与拒绝卡无关。

### 4. 终端渲染端：meta 有了也没人画（修订 2 新增，2026-10-26 真机取证）

- `pkg/tui/render.go` 的工具卡正文只来自 `f.Content`（`summaryToolBody`，render.go:2843 一带）；
  `DeniedToolDisplayBody` 对 `shell` 返回空串（设计使然，正文那句是各端本地化文案——终端的
  「Denied」在卡头），于是拒绝卡正文落到 `(no output)`。
- `render.go` 画了 `ToolMeta.Status/PreviewKind/Input/ResultLines/Category`，但**没有任何一处画
  `DenyFeedback`**（grep 全 pkg/tui 证实；网页端 `ToolCallCard.vue` 的 `refusalWords` 行早就画）。
- 网页形状是基准：一句话（`refusalSentence`）+ ↳ 原话行（`refusalWords`，CornerDownRight 前缀）。
  终端等价物=在拒绝卡的正文里把 `f.ToolMeta.DenyFeedback`（trim 后非空时）画成一行，跟在本地化
  句子（Content，可为空）之后；无原话时卡保持现状形状。parked 路径
  （`chat_turn.go:876` 早已把 `resume.Feedback` 放进 meta）与 in-place 路径共用同一渲染，
  三处（live/重载/网页）形状一致。

### 真机/单测上如何复现

- 真机（终端，最快）：`driver.sh` 的 `streamapprove` 场景 —— 提交后按 `3`（No）、`Enter` 进入说明行，
  写一句话再 `Enter`；卡上只有 `Denied echo approved-run`，没有那句原话。
- 单测（无需真机）：见 Test plan 里的两个新用例，改代码前必须红。

### 约定

- 事件线的字段是 **snake_case**（`deny_feedback`），前端由 `useChatStream.ts` 的映射读成 `denyFeedback`（已存在）。
- `pkg/tool` 的 `ToolMeta`（`pkg/tool/state.go:233-234`）与 `pkg/event` 的 `ToolCallMeta` 是两层不同结构，
  本计划**只补投影**，不合并、不重构。
- 代码注释：英文、讲"为什么"，与周围一致。

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./<pkg> -run '^<名字>$' -count=1 -v` | 看到 `--- PASS: <名字>`（**必须看到 PASS 行**：`-run` 写错只报 `no tests to run` 且 exit 0） |
| 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/gateway` | exit 0 |
| gofmt | `gofmt -l pkg` | 无输出 |
| 真机（终端） | `.claude/skills/run-forebrain/driver.sh` 的 `streamapprove`（见 Step 4） | 卡上出现用户原话 |

## Scope

**In scope（只改这些）**：
- `pkg/event/tool_events.go`（`ToolCallMeta` 加一个字段）
- `pkg/tool/notification.go`（`eventMeta` 投影补一行）
- `pkg/tui/chat_surface.go`（`notifyInPlaceRefusedCard` 用 `DeniedToolStep` 建 step，让原话进 meta）
- `pkg/gateway/wsevents.go`（`toolCallMetaFromWS` 补读 `deny_feedback`——修订扩入，网页主 agent 链路）
- `pkg/tui/render.go`（拒绝卡正文画 `ToolMeta.DenyFeedback`——修订 2 扩入，终端渲染）
- `pkg/tui/chat_turn.go`（`notifyToolApprovalDenied` 的 denied 分支改用 `DeniedToolStep`——修订 3 扩入，parked live 卡）
- `pkg/event/tool_events_test.go`、`pkg/tool/notification_test.go`、`pkg/tui/chat_surface_test.go`、
  `pkg/gateway/wsevents_test.go`、`pkg/tui/render_test.go`、`pkg/tui/chat_session_test.go`（各加/改断言）

**Out of scope（看着相关也不要动）**：
- `frontend/`（`useChatStream.ts` 的类型与 `ToolCallCard.vue` 的第二行早就就绪；138 已验收过形状）
- `pkg/gateway/server.go` 的 `deny_feedback` 映射（已存在，且它服务的是审批数据 map，不是本计划的持久化事件）
- `pkg/turn/approval.go` 的 `Decide`/`ComposeDenyFeedback`、`pkg/turn/subagent_approval.go` 的 `DenyFeedback: resume.Reason`
- `pkg/tool/request_permissions.go` 的 `DenyFeedback`（另一条路径，语义不同）
- `DeniedToolDisplayBody` 对 `shell` 返回空串这个现状（138 的卡用本地化句子，不改）
- 拒绝的**语义**（停不停回合）——那是 `plans/146`

## Steps

### Step 1：先写会红的测试

1. `pkg/tool/notification_test.go`（**已存在**，15 个用例；照 `TestRunEventFromStepCarriesTheRunningStart`
   的写法：`RunEventFromStep(ctx, "s1", "run-x", "tui", StepEvent{...})` 拿 `event.RunEvent`，再解 `Payload`）：造一个
   `tool.DeniedToolStep("call-1", "shell", map[string]any{"command": "rm -rf x"}, "改成移到 trash/")`，
   过 `RunEventFromStep`（同包，直接调），解出 `event.ToolCallCompletedPayload.ToolMeta`，
   断言 `DenyFeedback == "改成移到 trash/"`。
2. `pkg/tui/chat_surface_test.go`（**已存在**）：照 `TestInlineApprovalRefusalWithoutWordsStopsInPlace`（`:1370`）
   与 `TestInlineApprovalApprovesWithoutASecondRun`（`:1318`）的搭建写新用例：取到 `gate` 与 `req` 后调
   `s.notifyInPlaceRefusedCard(gate, req, turn.ToolApprovalDecision{Denied: true, DenyReason: "改成移到 trash/"})`，
   从该用例已有的 UI 收集器（`fakeSession` 的 `NewMessageMsg` 收件箱，按现有断言方式取）里断言
   `msg.ToolMeta.DenyFeedback == "改成移到 trash/"`。

**Verify**：两条定向测试各自 `--- FAIL`（`DenyFeedback` 为空）。若直接 PASS，STOP 2。

### Step 2：事件层补字段

`pkg/event/tool_events.go` 的 `ToolCallMeta` 里，紧跟 `Status` 之后加：

```go
	// DenyFeedback is the user's own words when they refused this call, for
	// the card that shows the refusal (plan 154). Empty on every call the user
	// did not refuse with words.
	DenyFeedback string `json:"deny_feedback,omitempty"`
```

`pkg/tool/notification.go` 的 `eventMeta` 字面量里补一行（放在 `Status` 之后、`Purpose` 之前，
与实际字段顺序无关紧要，但保持与 `tool.ToolMeta` 的次序一致便于对照）：

```go
		DenyFeedback: meta.DenyFeedback,
```

**Verify**：Step 1 的第 1 条用例 `--- PASS`；`CGO_ENABLED=1 go build -tags fts5 ./...` exit 0。

### Step 2b：网页主 agent 的 canonical 投影补读（修订新增）

`pkg/gateway/wsevents_test.go` 先加一条会红的断言（照该文件里既有 `toolCallMetaFromWS` /
`canonicalRunEventsFromWS` 用例的搭建）：构造带 `tool_meta: {"tool_name":"shell","status":"denied",`
`"deny_feedback":"改成移到 trash/"}` 的 WS step 消息，过 `canonicalRunEventsFromWS`，断言解出的
`event.ToolCallMeta.DenyFeedback == "改成移到 trash/"`。

然后 `pkg/gateway/wsevents.go` 的 `toolCallMetaFromWS` 里、`meta.Status` 那行之后补：

```go
		meta.DenyFeedback = strings.TrimSpace(stringOr(metaMap["deny_feedback"]))
```

（一行即可；`category`/`skill_name`/`skill_path` 的同类缺口**不修**，见「现状 3」。）

**Verify**：新断言先 `--- FAIL` 后 `--- PASS`（两次都要留记录）；`./pkg/gateway` 整包与基线相比无新增 FAIL。

### Step 3：终端 live 卡带上原话

`pkg/tui/chat_surface.go` 的 `notifyInPlaceRefusedCard` 里，把合成 step 换成引擎同一个构造器：

```go
	refused := tool.DeniedToolStep(stepID, toolName, input, decision.DenyReason)
	meta := tool.BuildToolMeta(refused)
	// BuildToolMeta reads Denied/DenyFeedback off the event, so the card and
	// its meta are the same refusal the runtime draws on the parked path —
	// no second, differently-shaped card for the in-place answer.
	if meta.Status == "" {
		meta.Status = "denied"
	}
```

并用 `refused` 与 `meta` 填 `NewMessageMsg`（`Content` 仍用 `tool.DeniedToolDisplayBody(toolName, strings.TrimSpace(decision.DenyReason))`；
`DeniedToolDisplayBody` 对 shell 返回空串是预期，卡的第二行由 `ToolMeta.DenyFeedback` 提供）。

**Verify**：Step 1 的第 2 条用例 `--- PASS`；`gofmt -l pkg` 无输出。

### Step 3b：终端渲染画原话（修订 2 新增）

`pkg/tui/render_test.go` 先加一条会红的用例（照该文件既有工具卡渲染用例的搭建）：构造
`Frame{Kind: FrameTool, ToolMeta: ToolMeta{Status: "denied", DenyFeedback: "改成移到 trash/"}},`
Content 为空（shell 拒绝的实况），断言渲染出的卡正文含 `改成移到 trash/`；再造一条
`DenyFeedback` 为空的 denied frame，断言卡形状与现状一致（不含 `(no output)` 之外的新行）。

然后 `pkg/tui/render.go` 的 `summaryToolBody`（或拒绝卡正文组装处）补 denied 分支：
`toolStatusDenied(f)` 且 `strings.TrimSpace(f.ToolMeta.DenyFeedback)` 非空时，把它画成正文行
（跟在 Content 的本地化句子之后；Content 为空时它就是正文）。只画这一处——parked 与 in-place
共用，勿在 chat_surface/chat_turn 里另行拼字。

**Verify**：新 render 用例先 `--- FAIL` 后 `--- PASS`；Step 4 真机拒绝卡上出现用户原话
（`grep -A2 'Denied'` 能看到那句话，不再只有 `(no output)`）。

### Step 3c：parked 路径的 live 卡同型修复（修订 3 新增）

`pkg/tui/chat_session_test.go` 先加一条会红的断言（照 :13483/:13531 既有驱动
`notifyToolApprovalDenied` 的用例搭建）：denied 场景下发出的卡片消息
`msg.ToolMeta.DenyFeedback` 必须等于 action 里用户写的那句。

然后 `pkg/tui/chat_turn.go` `notifyToolApprovalDenied`：`status == "denied"` 时把合成 step
换成 `tool.DeniedToolStep(stepID, toolName, input, feedback)` 再 `tool.BuildToolMeta`（同 Step 3
的形状；`meta.Status` 由构造器给出）；`status == "canceled"`（嵌套 request_permissions，非拒绝）
分支保持现状。`Content` 仍走 `DeniedToolDisplayBody`，勿在 Content 里拼原话。

**Verify**：新断言先 `--- FAIL` 后 `--- PASS`；`./pkg/tui` 整包与基线相比无新增 FAIL；`gofmt -l pkg` 无输出。

### Step 4：真机看一眼（终端，假模型）

```bash
D=.claude/skills/run-forebrain/driver.sh
$D build
$D reset
$D start streamapprove 'ok'
$D submit 'go'
$D wait 'needs approval' 30          # 覆盖层出现（该 wait 文本若变，按屏幕实际文案调整）
$D key 3; $D key Enter               # No → 进入说明行
$D submit 'move it to trash instead'
$D screen | grep -A2 'Denied'
$D stop
```

**Verify**：`grep` 打出的拒绝卡里能看到 `move it to trash instead`（此前只有 `Denied echo approved-run`）。
若驱动起不来，按 `.claude/skills/run-forebrain/SKILL.md` 的 Troubleshooting 处理；仍不行就把现象写进报告（STOP 4），
**不要**跳过此步宣称完成。

## Test plan

- 新增 `pkg/tool/notification_test.go`：`DeniedToolStep` 的原话出现在 `event.ToolCallMeta.DenyFeedback`（本计划的核心回归）。
- 新增 `pkg/tui/chat_surface_test.go`：in-place 拒绝的卡片消息带 `ToolMeta.DenyFeedback`。
- 回归：`./pkg/tool ./pkg/turn ./pkg/tui ./pkg/gateway` 整包与基线相比无新增 FAIL。
- 真机：Step 4。

## Done criteria

- [ ] 两条新用例 `--- PASS`（notification + chat_surface），wsevents 新断言 `--- PASS`，render 新用例 `--- PASS`
- [ ] `grep -n 'DenyFeedback' pkg/event/tool_events.go` 有 1 条（字段）；`grep -n 'DenyFeedback: meta.DenyFeedback' pkg/tool/notification.go` 有 1 条；`grep -c "stringOr(metaMap\[\"deny_feedback\"])" pkg/gateway/wsevents.go` 为 1
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui ./pkg/gateway -count=1` 与基线相比无新增 FAIL
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`gofmt -l pkg` 无输出
- [ ] Step 4 屏幕上拒绝卡里出现用户原话
- [ ] `git status --porcelain` 里本计划新增的改动只落在 In scope 的文件（含 wsevents/render 两组）
- [ ] `plans/README.md` 状态行已更新（除非评审方说由其维护）

## STOP conditions

1. 前置锚点不满足，或"现状"里按函数名找到的代码含义已变（例如 `BuildToolMeta` 不再写 `meta.DenyFeedback`）。
2. 两条新用例在改代码前就通过（说明链路上已有人修过，或测试没走到投影处）。
3. ~~发现第三个把 `tool.StepEvent` 翻成 `event.ToolCallMeta` 的地方~~（已触发并裁决：`toolCallMetaFromWS`
   扩入 scope 见 Step 2b；`webSearchEventMeta` 不可被拒绝、不扩。若再出现**第四处**且与拒绝相关，回报）。
4. Step 4 的驱动反复起不来，或卡上仍没有原话。
5. 基线跑不起来（编译失败、依赖缺失）。
6. 任何一步需要改 In scope 以外的文件（尤其 `frontend/`）。

## Maintenance notes

- `tool.ToolMeta` 与 `event.ToolCallMeta` 是两份字段表，字段拷贝共三处（执行中确认）：
  `notification.go`（终端/子代理/skill 的 `RunEventFromStep`）、`wsevents.go` 的 `toolCallMetaFromWS`
  （网页主 agent 的 canonical 重建）、`subagent.go` 的 `webSearchEventMeta`（web search 专用）。
  以后给 `tool.ToolMeta` 加"卡片要显示的字段"时，**按链路**核对这三处，否则 live 与重载会分叉
  （本计划就是这么漏的）；`toolCallMetaFromWS` 还遗留 `category`/`skill_name`/`skill_path` 不读的同类缺口。
- 终端的 in-place 卡与 parked 路径的卡必须由同一个 `DeniedToolStep` 产出（同一 step id、同一 display），
  否则同一张卡在 live、重载、网页三处形状不同。
- `DeniedToolDisplayBody` 对 `shell` 返回空串是**有意**的：正文那句是各端本地化文案，原话走 meta。
