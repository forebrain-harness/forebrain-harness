# Plan: 跨进程 peer 消息实时送达——审批期与相位竞态不再降级进 follow-up 队列

> **Executor instructions**: 逐步执行本计划；每步跑验证命令并确认预期结果后再继续。
> 出现 STOP conditions 立即停下报告，不得即兴发挥。默认**禁止任何 git commit**
> （owner 常设裁决），改动留工作区由 owner 审阅。实施直接在当前源码上进行，
> 不用 worktree/快照（owner 裁决）。
>
> **Drift check（先跑）**: 基线 `c3377e148539accbb33f925c857ae3a25f34d81d`（dev）。
> `git diff --stat c3377e14..HEAD -- pkg/run/controller.go pkg/process/peer_inbound.go pkg/run/controller_test.go pkg/process/peer_inbound_test.go docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md`
> 有差异时，把下面 "Current state" 摘录与活代码逐段对照；**内容不匹配才是 STOP**，
> 仅行号漂移可继续（用符号重新定位）。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: MED（共享投递语义，但收敛到既有单一实现，不新造状态）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `c3377e14`, 2026-11-13（会话内日期以实际为准）
- **Owner 裁决**: 审批停泊（waiting_approval）期间的跨进程来信由 follow-up **改为 steer**
  （user_interaction 已批准，推荐项「改为插话 steer」）。

## Why this matters

owner 报告（附 TUI 截图）：跨 TUI 进程的主代理之间 `peer_send`，偶现消息在收件方
落入 "Queued follow-up inputs" 卡片，要等当前 run 结束才执行，实时性不足。
设计裁决（`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` 行 17）：
「正在跑的收件方一律按插话在下一个工具边界送达」。用户自己的插话（TUI）与 gateway
在审批停泊相位都走队列层 `InputQueue.Steer`（steer 不打断审批本身，只在审批通过后
的下一次模型调用送达），唯独 peer 路径经过一个更严的 `Controller.Steer` 相位门，
把来信降级进 rejected/followUp lane——同一语义两套实现，出错的是 peer 这套。
修复后三个 surface 语义归一，来信在收件方存活 turn 内实时送达。

## Current state（摘录均来自 advisor 本会话亲自读取）

### 1. peer 投递表 — `pkg/process/peer_inbound.go:754-790` `deliverPrimary`

```go
if runID, phase, ok := in.svc.env.Control.Find(sessionID); ok {
	switch phase {
	case run.Running:
		if in.svc.env.Control.Steer(runID, peerInput(prompt)) {
			in.noteHanded(sessionID)
			return ""
		}
	case run.Waiting, run.Finishing:
		if in.svc.env.Control.FollowUp(runID, peerInput(prompt)) {
			in.noteHanded(sessionID)
			return ""
		}
	}
	// The run ended between the lookup and the delivery: the message is a
	// turn of its own from here, exactly as if it had arrived a moment later.
}
if in.svc.env.deps().RunRT != nil {
	if err := in.svc.env.deps().RunRT.SessionBusy(ctx, sessionID); err != nil {
		return peerRefusedBusyElsewhere
	}
}
if in.surface.Wake == nil { return peerRefusedInboxNotAttached }
if err := in.surface.Wake(ctx, prompt); err != nil { ... return peerRefusedInboxNotAttached }
in.noteHanded(sessionID)
return ""
```

`Controller.Steer` 全仓唯一调用点就是这里（`grep -rn "Control.Steer" pkg --include=*.go | grep -v _test`
只命中 peer_inbound.go:761）。

### 2. 相位门与降级 — `pkg/run/controller.go:482-498`

```go
func (c *Controller) Steer(runID string, in Input) bool {
	state, ok := c.Get(runID)
	if !ok { return false }
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase != Running {
		if state.input != nil {
			rejected := cloneInput(in)
			rejected.Rejected = true
			state.input.FollowUp(rejected)   // ← 降级进 rejected lane
		}
		return false
	}
	return state.input != nil && state.input.Steer(in)
}
```

相位常量（controller.go:18-23）：`Running / Waiting("waiting_approval") / Finishing / Finished`。
`WaitApproval` = Running→Waiting（审批门）；`Cancel` 置 Finishing（controller.go:407-411）；
`Finish` 置 Finished 且 `state.input=nil`。`FollowUp`（501-512）对 Finishing/Finished 返回 false。

### 3. 队列层语义（用户/gateway 路径，正确基准）

- `pkg/run/turn_input.go:826-872` `steer()`：只以 `q.rt == nil`（runtime 未附着）为拒绝条件，
  拒绝时进 rejected lane；**不看相位**。审批停泊期间 rt 仍附着（Begin/Track 时 Attach，
  run 结束 Release 才 Detach）。
- TUI 用户插话：`pkg/tui/run.go:5048` 与 `:5325` `state.sessionQueue().Steer(queueInput(submission))`。
- gateway：`pkg/gateway/run_control.go:154` `accepted := q.Steer(in)`。
- lane 渲染：`pkg/tui/render.go:4826-4833` 把 `RejectedSteers + QueuedMessages` 合并画成
  **"Queued follow-up inputs"** 卡片（截图症状的直接来源）；steer 另有 PendingSteers 区
  （header 带 "(press esc to interrupt and send immediately)"）。
- 消费点：`pkg/run/turn_input.go:1035-1082` `Next(boundary)` 只在 run 结束边界消费
  rejected/steers/followUp → 降级消息必然「run 结束才执行」。

### 4. 设计文档原表 — `docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md:246-248`

```
| 主代理有 run 在 Running | 插话（下一个工具边界送达），user 行带 is_meta + origin: peer |
| 主代理 run 停在审批 | 作为该 run 之后的 follow-up |   ← 行 247，本次经 owner 批准修订
| 主代理空闲 | 自动唤醒… |
```

### 5. 复现证据（advisor 已跑，仓库外程序 `/tmp/peer-repro/main.go`，`go run` 于仓库根）

```
Waiting  Controller.Steer accepted: false steers=0 rejected=1 followUp=0
Waiting  queue.Steer      accepted: true  steers=1 rejected=0 followUp=0
Race     Find= running Controller.Steer accepted: false steers=0 rejected=1 followUp=0
Race     Find= running Controller.Steer accepted: false steers=0 rejected=1 followUp=0
```

因果链：`peer_send` 跨进程帧 → `deliverPrimary` → `Controller.Steer` 重检相位
（Waiting=审批停泊的确定性路径；或 Find 与 Steer 之间 Running→Waiting/Finishing 的竞态）
→ 降级 rejected lane → 渲染成 follow-up 卡片 → 仅 run 结束边界消费。
竞态路径另有回执矛盾：降级后 `deliverPrimary` 落穿到 `SessionBusy`
（`pkg/state/runrt.go:851`，本进程 live run → `ErrSessionRunning`）→ 发送方收 **refused**
回执，而消息实际留在队列里 run 结束后执行。

已排除的竞争解释：① Wake/空闲路径坏——`TestPeerInboundWakesAnIdleSession` 绿，空闲走 Wake
不进队列；② 渲染误报——复现证明条目客观在 rejected lane；③ 边界消费坏——`Next` 会先发
rejected，消息终会送达，投诉点是实时性，与 lane 语义吻合。

### 6. 并行 WIP（不得触碰）

- `forebrain-harness-21`：`pkg/turn/models.go`、`pkg/turn/executor.go`、TUI picker 呈现；
  工作区现有 `pkg/run/subagent.go`、`pkg/run/subagent_test.go` 改动归它，不动不改不提交。
- `forebrain-harness-e9`：/connect 面板（Volcengine Ark 行）相关，与本计划无交集。
- 已 peer_send 与 21 互确认 scope 无交集。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 环境（直接 go 命令编译 pkg/pdf 必需） | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | — |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '<用例>' -count=1` | PASS |
| 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` | 全 PASS |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| fmt | `gofmt -l cmd pkg third_party` | 无输出 |

CGO 与 `-tags fts5` 是仓库硬要求（漏 tag 的 `no such module: fts5` 是工具链错误，不是产品 bug）。

## Scope

**In scope（只许改这些）**:
- `pkg/run/controller.go`（仅 `Steer` 方法）
- `pkg/process/peer_inbound.go`（仅 `deliverPrimary` 的 switch）
- `pkg/run/controller_test.go`、`pkg/process/peer_inbound_test.go`
- `docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md`（仅行 247 一行）
- `docs/plan/PEER_REALTIME_PEER_DELIVERY_PLAN.md`（本计划归档，gitignored）

**Out of scope（看似相关也不碰）**:
- `pkg/run/turn_input.go` — 队列语义是正确基准，不改。
- `pkg/tui/*`（render.go/notify.go/run.go）、`pkg/gateway/*` — 渲染与唤醒路径不变。
- `pkg/turn/models.go`、`pkg/turn/executor.go`、`pkg/run/subagent*.go` — 他人 WIP。
- `peer_send` 工具描述文案、`Controller.FollowUp` 方法本身（gateway 等仍用）。

## Git workflow

不建分支、不 commit、不 push。改动留工作区。计划归档文件不 commit（docs/plan 已 gitignore）。

## Steps

### Step 0: 归档计划
把本文件复制到 `docs/plan/PEER_REALTIME_PEER_DELIVERY_PLAN.md`。
**Verify**: 文件存在且内容与本计划一致。

### Step 1: 先写回归测试，确认红（复现门）
在 `pkg/run/controller_test.go` 新增（模式参照同文件 `TestControllerCancelAndQueue`；
`llm` 包该文件已 import）：

```go
func TestControllerSteersWhileParkedOnApproval(t *testing.T) {
	c := NewController()
	if _, err := c.Begin(context.Background(), "r1", "s1"); err != nil { t.Fatal(err) }
	if !c.WaitApproval("r1") { t.Fatal("park") }
	// peer 路径先 Find 再 Steer；两步之间相位可能翻转，两种顺序都必须 steer。
	if _, phase, ok := c.Find("s1"); !ok || phase != Waiting { t.Fatal("find") }
	if !c.Steer("r1", Input{Text: "peer", Parts: []llm.ContentPart{llm.Text("peer")}}) {
		t.Fatal("steer refused while parked on approval")
	}
	p := c.SessionQueue("s1").Preview()
	if len(p.Steers) != 1 || len(p.Rejected) != 0 || len(p.FollowUp) != 0 {
		t.Fatalf("parked steer lanes = %#v, want 1 steer and nothing demoted", p)
	}
	if !c.Resume("r1") { t.Fatal("resume") }
	entries := c.SessionQueue("s1").Runtime().DrainSteers()
	if len(entries) != 1 || llm.TextContent(entries[0].Parts...) != "peer" {
		t.Fatalf("drained = %#v, want the peer steer", entries)
	}
}
```

并在 `TestControllerCancelAndQueue` 末尾（现有 `if c.Steer("r1", Input{Text: "late"}) {...}` 之后）追加：

```go
	if p := c.SessionQueue("s1").Preview(); len(p.Rejected) != 0 || len(p.FollowUp) != 0 {
		t.Fatalf("a refused steer must not linger in the queue: %#v", p)
	}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestControllerSteersWhileParkedOnApproval|TestControllerCancelAndQueue' -count=1`
→ **FAIL**（前者 "steer refused while parked on approval"；后者 rejected 残留）。
若此步不红，STOP 报告（根因假设不成立）。

### Step 2: 修 `Controller.Steer`（收敛到队列层语义）
`pkg/run/controller.go` 的 `Steer` 改为（注释风格跟随同文件）：

```go
// Steer queues an interrupting message while the turn is live. A turn parked
// on its approval gate is still live: the steer waits for the next model call
// after the approval, exactly as the person's own steer does (InputQueue.Steer).
// A winding-down or finished run takes nothing; the caller falls back to its
// own next-turn path.
func (c *Controller) Steer(runID string, in Input) bool {
	state, ok := c.Get(runID)
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase == Finishing || state.phase == Finished {
		return false
	}
	return state.input != nil && state.input.Steer(in)
}
```

删除整个 `phase != Running` 降级分支（不新造 lane/状态/判定）。
**Verify**: Step 1 两条用例转 PASS；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` 全绿
（`TestControllerNextListsRejectedInputWhole` 此时会红——它钉的是旧降级行为，Step 4 修订）。

### Step 3: 修 `deliverPrimary` 投递表 + 重写 parked 测试
`pkg/process/peer_inbound.go` switch 改为：

```go
	switch phase {
	case run.Running, run.Waiting:
		if in.svc.env.Control.Steer(runID, peerInput(prompt)) {
			in.noteHanded(sessionID)
			return ""
		}
	}
```

删除 `case run.Waiting, run.Finishing:` 的 FollowUp 分支（Finishing 落穿到
SessionBusy/Wake，与现状一致；FollowUp 对 Finishing 本就返回 false，分支是死路）。
保留落穿注释。

`pkg/process/peer_inbound_test.go` 的 `TestPeerInboundFollowUpToParkedRunCarriesThePrompt`
重写为 `TestPeerInboundSteersAParkedRunCarriesThePrompt`（harness 不变：
`startPeerTestService` + `TrackRuntime` + `WaitApproval`，参照同文件
`TestPeerInboundSteersARunningSession`）：断言 `queue.Preview().Steers == 1` 且
Rejected/FollowUp 为 0；`queue.TakeAll()` 单条，`Origin == state.MessageOriginPeer`、
`Payload.(PeerPrompt)` 的 Text/FromName/MsgID/SessionID 断言保留；`svc.noReceipt`、
`view.wakeCount() == 0` 保留。注释说明：审批停泊的 run 仍存活，来信按插话送达（owner 裁决）。
**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestPeerInbound' -count=1` 全绿。

### Step 4: 修订钉旧行为的既有测试（保留其原本目的）
`TestControllerNextListsRejectedInputWhole` 的目的＝「边界整条列出 rejected 输入」。
rejected 的构造改走 **runtime 未附着** 路径（队列语义里真正的拒绝条件），不再依赖相位降级：

```go
	c := NewController()
	q := c.SessionQueue("s1") // 无 run、runtime 未附着 → steer 被拒进 rejected lane
	q.Steer(Input{Text: "look at this", MentionImages: []string{"shots/a.png"}})
	q.Steer(Input{Attachments: []string{"file-1"}})
	send, _ := q.Next(BoundaryCompleted)
	// 原有 send 断言（len==2、Rejected、Text、MentionImages、Attachments）保持不变
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` 全绿。

### Step 5: 设计文档同步
`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` 行 247 改为：
`| 主代理 run 停在审批 | 插话（审批通过后的下一次模型调用送达，与用户插话同语义；owner 2026-11 裁决修订） |`
**Verify**: `grep -n "停在审批" docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` 显示新行。

### Step 6: 回归门禁
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`
→ 全 PASS；`CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0；`gofmt -l cmd pkg third_party` 无输出。
**Verify**: 三条命令预期如上。若失败文件不在 Scope 内（他人 WIP），STOP 报告，不修别人的文件。

### Step 7: tmux 真机验收（fake 模型，owner 裁决：agent 验收一律 fake）
先加载 `.claude/skills/run-forebrain/SKILL.md`，用其 `driver.sh` 起隔离 TUI。
**修复前对照**：Step 2 之前先 `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain-before ./cmd/forebrain`。

隔离 rendezvous（两进程共用，不污染真实目录）：
```bash
WORK=$(mktemp -d); TREE=$WORK/tree; mkdir -p "$TREE/a" "$TREE/b"
export FOREBRAIN_PEERS_DIR=$WORK/peers FOREBRAIN_PEER_SOCKET_DIR=$WORK/socks
```
- 收件 TUI：cwd=$TREE/a，fake mode `shell`，answer text 为 JSON
  `{"command":"echo approved-run","sandbox_permissions":"require_escalated"}` → 审批 overlay → run Waiting。
- 读收件方注册名：`jq -r --arg cwd "$TREE/a" 'select(.cwd==$cwd)|.name' "$FOREBRAIN_PEERS_DIR"/*.json`
  （无 jq 时用 `python3 -c 'import json,glob,sys; [print(json.load(open(p))["name"]) for p in glob.glob(sys.argv[1]+"/*.json") if json.load(open(p))["cwd"]==sys.argv[2]]' "$FOREBRAIN_PEERS_DIR" "$TREE/a"`）。
- 发件 TUI：cwd=$TREE/b，fake mode `tool`，answer text
  `{"name":"peer_send","arguments":{"to":"<上一步名字>","message":"realtime probe"}}`。
- 场景 1（复现→修复）：旧二进制收件方 capture-pane 应见 "Queued follow-up inputs"（真机复现）；
  新二进制同场景应见 PendingSteers 区（"(press esc to interrupt and send immediately)"）且**无**
  follow-up 卡片；回答审批后，transcript 出现 peer meta 行（发件方原话），provider 日志的下一请求
  携带该 envelope（steer 在审批后下一次模型调用送达）。
- 场景 2（回归）：收件方 fake mode `stream`（run 在跑）→ 来信仍 steer；收件方空闲 → 自动唤醒开一轮。
- 场景 3（中断自救）：审批停泊中 Esc 退出 → EXIT_CODE 干净 → 再启动可恢复。
每场景逐屏 `tmux capture-pane -t <sess> -p` 取证。结束 `tmux kill-session`、清理 $WORK。
**Verify**: 场景 1 新二进制无 follow-up 卡片且审批后送达；场景 2/3 行为如述。

### Step 8: 收口
计划归档文件回填「已实施并真机验收通过（日期）」+ 验收记录章节（场景→证据）。
`git status --short` 改动集合 == Scope（含他人 WIP 两文件原样）。不 commit。

## Test plan

- 新：`TestControllerSteersWhileParkedOnApproval`（确定性 Waiting + Find→Steer 竞态顺序，
  steer lane、Resume 后 DrainSteers 送达）。
- 新断言：`TestControllerCancelAndQueue` 追加「Finishing 拒绝后队列无残留」。
- 重写：`TestPeerInboundSteersAParkedRunCarriesThePrompt`（跨 socket 全入站链路，origin/payload/回执/不唤醒）。
- 修订：`TestControllerNextListsRejectedInputWhole`（rejected 构造改走 rt 未附着，目的不变）。
- 结构模式：`pkg/process/peer_inbound_test.go` 的 `TestPeerInboundSteersARunningSession`、
  `pkg/run/controller_test.go` 的 `TestControllerCancelAndQueue`。

## Done criteria

- [ ] Step 1 复现门先红后绿（两条用例）。
- [ ] `go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`（scratch home）全 PASS。
- [ ] `go vet -tags fts5 ./...` exit 0；`gofmt -l cmd pkg third_party` 无输出。
- [ ] `grep -n "state.phase != Running" pkg/run/controller.go` 无匹配；
      `grep -n "run.Waiting, run.Finishing" pkg/process/peer_inbound.go` 无匹配。
- [ ] 真机场景 1：审批停泊收件方无 "Queued follow-up inputs"，steer 可见且审批后送达。
- [ ] `git status` 改动 ⊆ Scope；零 commit。

## STOP conditions

- Drift check 内容不匹配（Steer/deliverPrimary 摘录与活代码对不上）。
- Step 1 不红（根因假设不成立）。
- `grep -rn "Control.Steer" pkg --include=*.go | grep -v _test` 出现 peer_inbound 之外的调用点
  （改相位门会波及未知调用方）。
- 真机审批通过后 steer 未被下一次模型调用消费（orchestration 在 Waiting 后不 drain）——
  停下重查 orchestration，不得加症状补丁。
- 回归失败落在 Scope 外文件（他人 WIP）——停下报告。
- 同一验证两次合理修复后仍失败——停下报告。

## Maintenance notes

- 评审重点：`Controller.Steer` 与 `InputQueue.Steer` 语义一致（存活 turn 可插话，
  Finishing/Finished 拒绝）；没有新 lane/状态/消息类型。
- 未来新增 Phase 时，必须同时在 `Controller.Steer` 与 `deliverPrimary` 声明其可插话性；
  队列层 rt 附着仍是唯一拒绝基准。
- `Controller.FollowUp` 保留（用户 follow-up 语义不变）；peer 路径不再使用它。
- 延期：无。

## Implementation Status

已实施并真机验收通过（2026-11-13）。零 commit，改动留工作区（HEAD 仍 c3377e14）。

### 步骤完成状态

- Step 0 归档：完成（本文件，与计划逐字一致，gitignored）。
- Step 1 复现门：完成，先红——TestControllerSteersWhileParkedOnApproval "steer refused while parked on approval"；TestControllerCancelAndQueue rejected lane 残留 "late"。
- Step 2 Controller.Steer：完成（删除 phase != Running 降级分支，Finishing/Finished 拒绝，其余交给 InputQueue.Steer）。两条用例转绿；pkg/run 当时唯一红例为计划预测的 TestControllerNextListsRejectedInputWhole。
- Step 3 deliverPrimary + parked 测试重写：完成（case run.Running, run.Waiting → Steer；删除 Waiting/Finishing FollowUp 分支）。TestPeerInbound* 全绿。
- Step 4 修订 TestControllerNextListsRejectedInputWhole：完成（rejected 构造改走 rt 未附着；原断言不变）。pkg/run 全绿。
- Step 5 设计文档行 247：完成（grep 验证新行）。
- Step 6 回归门禁：完成——首轮全量 36 包全 PASS；vet exit 0；gofmt 无输出。
- Step 7 真机验收：完成（fake 模型，3 场景，证据见下）。
- Step 8 收口：完成。

### 验收记录（场景 → 证据）

环境：隔离 FOREBRAIN_PEERS_DIR/FOREBRAIN_PEER_SOCKET_DIR + 每实例独立 FOREBRAIN_RUN_WORK/SESSION/PORT；收发双方 cwd 分离；fake provider（driver.sh）。相位客观探针 = 发件方 peer_send 回执的 peer 状态（sessionStatus：Running→busy，Waiting→waiting，空闲→idle）。

- 场景 1a（修复前复现，forebrain-before）：收件方 Shift+Tab 进 plan 模式 → 模型 exit_plan_mode → gate overlay，回执 "Sent to proj-77 (waiting)" 确认 Waiting 停泊。批准后 30s 存活窗口（FAKE_REPLY_DELAY=30）截屏：`• Queued follow-up inputs ↳ realtime probe`；request-0002 不含 probe。run 结束后才出现 "↘ Received a message from proj-63 / realtime probe" 新 turn（request-0003 携带信封，独立 Working 计时）——owner 投诉的「run 结束才执行」逐字复现。
- 场景 1b（修复后，forebrain）：同场景回执 "Sent to proj-42 (waiting)"。批准后截屏：`• Messages to be submitted after next tool call(press esc to interrupt and send immediately) ↳ realtime probe`，无 follow-up 卡片。DB 证据：全部 7 条消息（含 msg6 peer 信封、msg7 模型应答）同一 run_id a47daaf7-06e2-4560-96d6-797885135389，fb_runs 仅 1 条——steer 在存活 run 内由最终应答 reopen 路径（orchestration_llm.go:897）消费，模型在 run 结束前应答；单一 "Worked for 1m 12s" 计时（两次 held 调用）。
- 场景 2a（Running 回归）：stream 模式收件方，回执 "(busy)"，截屏 PendingSteers 区可见、无 follow-up 卡片。
- 场景 2b（空闲回归）：reply 模式收件方完成后空闲，回执 "(idle)"，自动唤醒开新 turn："↘ Received a message from proj-32 / realtime probe / ● idle answer"，DB msg4-6 为新 turn，provider 2 个 REQUEST。
- 场景 3（中断自救）：gate 停泊 + steer 在队 → Esc = "◆ Kept planning └ cancelled"，run 干净结束，steer 不丢（落为独立 peer turn 执行）；连发 C-c C-c → EXIT=0；RESUME_ID 重启完整回放（含取消的 gate 卡与 peer 消息卡）。

### 偏差与裁决（均为非承载性 harness 细节，intent/scope/Done criteria 不变）

1. 发件方需 ENABLE_SUBAGENT=1：registerPeerTools 挂在 subagent 开关上（pkg/run/peer_tools.go "They ride the subagent family's own switch"），driver 默认 false → 首轮 "unknown tool peer_send"。harness 配置，非产品缺陷。
2. 场景触发器 shell→exit_plan_mode gate：计划假设「shell 审批 → run Waiting」不成立——shell require_escalated 是 in-place 审批（run 不返回，相位保持 Running；首轮 shell 场景回执 "(busy)"，旧二进制也 steer）。真 Waiting 停泊 = RequiresAction 类 gate：exit_plan_mode 无条件 RequiresActionError（session_tools.go:736）→ tuiWait（chat_session.go:2491）。改用 Shift+Tab 进 plan 模式 + 模型 exit_plan_mode，以回执 "(waiting)" 客观确认。修复本身与单位级因果链不受影响（复现门红→绿）。
3. 送达时点细化：steer 由「审批后同 run 内的下一个模型调用边界」消费（首次审批后调用完成 → reopen 携带 steer），而非字面的审批后第一个请求——与队列层 steer 语义（调用边界 drain）及设计文档「下一个工具边界送达」一致，用户插话同此语义。Done criteria「审批后送达、不等 run 结束」以 run_id 证据满足。若要求 steer 上审批后第一个请求，属 orchestration 改动（且会同时改变用户插话语义），超出本计划 scope，留作后续。
4. 测试注释语言：计划示例中的中文注释按仓库惯例译为英文（controller_test.go 全英文注释）。
5. 目录布局：driver 硬编码 PROJ=$WORK/proj，故用每实例 $WORK/proj 作 cwd（替代计划字面的 $TREE/a、$TREE/b）；共享 rendezvous 经 env 覆盖不变。注册名匹配用物理路径（/tmp→/private/tmp）。
6. FAKE_REPLY_DELAY=30 用于在审批后存活窗口截屏（driver 文档化旋钮）；退出需连发 C-c C-c（确认窗口短）。

### 验证结果汇总

- 复现门：先红（两条用例，失败原因与计划逐字吻合）→ 修后绿。
- 定点：pkg/run 三用例、pkg/process TestPeerInbound* 收口复跑全绿。
- 全量：首轮 36 包全 PASS（scratch home）。收口复跑 35/36 PASS，pkg/gateway TestTheReaperLeavesAnUncertainContinuationToItsRecovery 一次 TempDir RemoveAll cleanup 竞态失败（断言本身通过；聚焦重跑绿）——落在 21 号 peer 正在编辑的包、非本计划改动路径，已 peer_send 告知 21，未触碰。
- vet -tags fts5 ./... exit 0（两次）；gofmt -l cmd pkg third_party 无输出（两次）。
- grep 门禁：controller.go 无 "state.phase != Running"；peer_inbound.go 无 "run.Waiting, run.Finishing"。
- git：HEAD=c3377e14 未动，零 commit；工作区改动 = 本计划 4 文件 + 他人 WIP（原样保留）。docs/plan 两文件 gitignored。

### 改动文件

- pkg/run/controller.go（Steer 方法）
- pkg/process/peer_inbound.go（deliverPrimary switch）
- pkg/run/controller_test.go（新增 TestControllerSteersWhileParkedOnApproval；TestControllerCancelAndQueue 追加无残留断言；TestControllerNextListsRejectedInputWhole 改走 rt 未附着）
- pkg/process/peer_inbound_test.go（TestPeerInboundFollowUpToParkedRunCarriesThePrompt → TestPeerInboundSteersAParkedRunCarriesThePrompt）
- docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md（行 247）
- docs/plan/PEER_REALTIME_PEER_DELIVERY_PLAN.md（归档 + 本记录）