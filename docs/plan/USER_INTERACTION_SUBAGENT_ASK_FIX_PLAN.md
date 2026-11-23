# 修复 user_interaction：子代理提问失败退出 + 校验过严 + Canceled 卡语义

> 状态：待 owner 审批。已经独立 plan 子代理冷读评审并折叠全部发现（F1–F7）。
> 调查基线 `4e8158e9`（主树干净）；并行 peer P4 即将 ff-only 落 dev `6c75da7f`
> （只改 config_panel*.go / settings 组件 / locales，与本计划零交集）。执行时以最新 dev HEAD
> 为基线，drift 检查用内容 grep 锚点（非 SHA 钉死），不受 dev 前进影响。

## Context / Why this matters

owner 报告（TUI 子代理视图截图）：子代理调用 `user_interaction`，先因
「option label must contain between 1 and 5 words」被拒（Failed to ask user 卡），
重试后整个子代理 run 直接失败退出，屏幕三张卡：

1. `● Failed to ask user / tool user_interaction: option label must contain between 1 and 5 words`
2. `○ Canceled / (no output)`
3. `● subagent failed / missing ask form payload`

严重 bug：子代理一旦向用户提问就必死。owner 要求：定位根因彻底修复；**删掉或放宽
参数校验，必须提高首次调用成功率**；**Canceled 卡不符合逻辑语义**（提问是失败不是被取消）；
**主代理与主代理视图同类问题本期一并修复**。

## Baseline / drift check

- 执行第一步先 `git rev-parse HEAD` 记当前基线（peer P4 落地后应为 `6c75da7f` 或更新）；
  `git status --porcelain` 应干净（他人 WIP 在隔离 worktree，不在主树）。
- 并行 agent forebrain-harness-dc 改 `pkg/tui/config_panel*.go` 及 settings `.vue`/locales，
  与本计划 scope 无交集（已 peer_send 双向确认）。主树他人 WIP 不动。
- drift 检查（内容不符即 STOP；行号漂移不算）：
  - `grep -n "AskFormJSON" pkg/turn/approval.go` 应只有 `req.AskFormJSON = askFormJSON` 一处赋值（PendingForRun 内）。
  - `grep -n "missing ask form payload" pkg/tui/overlays.go` 命中 newQuestionOverlay（全库唯一源）。
  - `grep -n "maxOptionLabelWord\|between 1 and 5 words" pkg/tool/human.go` 命中常量与错误文案。
  - `grep -n "len(q.Options) < 2" pkg/state/action_service.go` 命中 CreateAsk。

## Root cause（file:line 证据链，已独立核实）

提问有两条 surface 应答路径，**只有一条携带表单 payload**：

- 主代理 in-place：`resolveInlineApproval`（pkg/tui/chat_surface.go:1423）→
  `PendingForRun`（pkg/turn/approval.go:1846）从 action store 读 `act.PayloadJSON` 设
  `req.AskFormJSON`（approval.go:1893）。**正确**。
- 子代理 in-place：`runAcrossApprovals`（pkg/run/subagent.go:2123-2166）捕获子代理 run
  抛出的 `RequiresActionError` → approve hook = `resolveSubagentApproval`
  （chat_surface.go:1394-1414）→ `turn.SubagentApprovalRequest`
  （pkg/turn/subagent_approval.go:83-97）从 gate 构造请求。**从不设 `req.AskFormJSON`**
  （gate 结构 subagent_approval.go:31-45 无表单字段）。

因果链（对应截图三张卡）：

1. 首次 label 超 5 词 → `validateInput`（pkg/tool/human.go:179-182）→
   `ErrOptionLabelTooLong`（human.go:423）→ 工具错误回模型 → **Failed to ask user 卡**；模型重试短 label。
2. 重试过校验 → interactor（pkg/tool/registry.go:455-508）`humanInputToAskForm` →
   `CreateAsk` → 返回 RAE，`ToolInput` 仅 `{action_id,kind}`，**RAE 不携带表单 payload**
   （结构 pkg/tool/request_permissions.go:486-502；`OriginatingToolCallID` 字段已存在但 interactor 未填）。
3. `SubagentApprovalRequest` 构造的 `req.AskFormJSON == ""`。
4. `answerInPlaceApproval` → `sink.PromptToolApproval`（pkg/tui/channels.go:110-113）→
   `newQuestionOverlay`（pkg/tui/overlays.go:2494-2498）空 payload → `"missing ask form payload"`。
5. 错误分支（chat_surface.go:1514-1523）：lease select 后 `actionSvc().Cancel(...)` → 返回 err →
   `runAcrossApprovals` 返回 err → 子代理 run 失败 → **subagent failed 卡**。
6. run 结束 → `SubagentEndedMsg` → `FinalizeAgentPendingTools`（pkg/tui/run.go:433-436、
   render.go:957-980）把仍 pending 的提问 step 扫成 `canceled` → **Canceled/(no output) 卡**。

**同类缺陷 sibling paths（全库排查，无遗漏）：**
- `planReviewToolApprovalRequest`（chat_surface.go:1658-1671）同样不设 AskFormJSON →
  plan reviewer 提问同病。本期修 payload（A.4）。
- fork / subagent_fanout / goal-evaluator / continuation 全经 `runAcrossApprovals` 同一 hook
  （subagent.go:1164/2186/2192/2332/3287/5142/5164）→ A 一并覆盖。
- interactor 是唯一 ask gate 源（user_interaction 自管审批协议，pkg/run/permissions.go:445-446）；
  unwind/resume 的 RAE 重建点都经 store 或已批准重跑，不需新字段。
- web gateway 从 action store `PayloadJSON` 读表单（api_extra.go:4301-4319 actionListRow 内嵌
  state.Action；handleActionsAsk:4358 委托 CreateAsk）→ 不缺 payload，不改。
- 历史：`git log -S AskFormJSON` 仅初始导入 → **非 P3 回归**。

**Canceled 语义根因（主/子共享）：** in-place 审批 `PromptToolApproval` 返回基础设施错误时，
`answerInPlaceApproval` 只 Cancel action 返回 err，**不为 parked step 发 failed 完成事件**；
run 结束 sweep 扫成 Canceled，错误文案被吞。
- 子代理：run 失败 → SubagentEndedMsg → sweep → Canceled（截图所示）。
- 主代理（**更正 F4**）：纯 overlay 构造错误走 turn 失败分支（chat_turn.go:1086-1092），
  发错误卡但**无 StreamResetMsg** → parked 卡**卡死在 awaiting approval**（不是 Canceled）；
  仅 lease-loss/用户取消 → promptCtx cancel → Cancelled decision → cancel 收尾 →
  StreamResetMsg → `FinalizePendingTools` 才是 Canceled。两种旧症状都被 C 的 failed 卡替换。

**校验过严根因（主/子共享）：** `validateInput`（human.go:142-191）与 `CreateAsk`
（pkg/state/action_service.go:175-190）**两处独立**施加 questions 1-4 / options 2-4 /
label 1-5 词 / description 必填。全库上限仅此两处（pkg/llm 不强制 schema，maxItems 仅是
模型可见描述）。模型首次调用极易踩中 → 首次成功率低。

## 复现与回归测试（执行第一步：先跑复现确认 FAIL，再改代码）

### 复现 1（pkg/tui，修复前可编译且 FAIL）

复用 `inlineGateSession`（chat_surface_test.go:1528）+ `scriptedApprovalSink`
（chat_session_test.go:13709，捕获 `requests`）。`ToolApprovalRequest.AskFormJSON`
修复前已存在（pkg/turn/model.go:596），故下式修复前可编译：

```go
func TestSubagentAskGateCarriesTheForm(t *testing.T) {
	cs, _, act, runID, rae := inlineGateSession(t, "user_interaction")
	rae.AgentID = "worker-1"; rae.SubagentType = "explore"
	// 修复 A 落地后补这一行（修复前 RAE 无此字段，不能写）：模拟 interactor 新契约
	// rae.AskFormJSON = act.PayloadJSON
	sink := &scriptedApprovalSink{decisions: []turn.ToolApprovalDecision{{Approved: true}}}
	cs.SetToolApprovalSink(sink)
	if _, err := cs.resolveSubagentApproval(inlineGateCtx(runID), rae); err != nil {
		t.Fatalf("resolveSubagentApproval: %v", err)
	}
	if len(sink.requests) != 1 { t.Fatalf("prompts=%d want 1", len(sink.requests)) }
	got := strings.TrimSpace(sink.requests[0].AskFormJSON)
	if got == "" { t.Fatalf("subagent ask gate carried no form payload; overlay fails 'missing ask form payload'") }
	if got != strings.TrimSpace(act.PayloadJSON) { t.Fatalf("AskFormJSON=%q want action payload", got) }
}
```

`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestSubagentAskGateCarriesTheForm -count=1`
**修复前预期 FAIL（空 payload）**。修复 A 后补 `rae.AskFormJSON = act.PayloadJSON` 一行 → PASS
（钉住 gate→request 投影 A.1/A.3/A.4）。
**F1 要点**：该测试绕过 interactor，单独不能证明 A.2；必须配下面的 interactor 级测试。

### 复现 2（pkg/tui，Canceled 语义，修复前 FAIL）

```go
type failingApprovalSink struct{ err error }
func (s *failingApprovalSink) PromptToolApproval(context.Context, turn.ToolApprovalRequest) (turn.ToolApprovalDecision, error) {
	return turn.ToolApprovalDecision{}, s.err
}
func TestInPlaceApprovalPromptFailureShowsFailedCard(t *testing.T) {
	cs, _, _, runID, rae := inlineGateSession(t, "user_interaction")
	cs.SetToolApprovalSink(&failingApprovalSink{err: errors.New("overlay boom")})
	var failed []Message
	cs.PrependUINotify(func(msg any) {
		if m, ok := msg.(NewMessageMsg); ok && m.Msg.Kind == MsgKindTool &&
			m.Msg.StepID == "call-inline" && strings.EqualFold(m.Msg.ToolMeta.Status, "failed") {
			failed = append(failed, m.Msg)
		}
	})
	_, _ = cs.resolveInlineApproval(inlineGateCtx(runID), rae)
	if len(failed) == 0 { t.Fatalf("failed prompt left parked ask pending (swept Canceled); want failed card") }
}
```
（`PrependUINotify` 先例 chat_surface_test.go:1497；sink 单方法接口 pkg/turn/model.go:733。）
**修复前 FAIL（无 failed 卡），修复 C 后 PASS。**

### interactor 级回归（pkg/tool，钉住 A.2，随修复 A 新增）

仿 `TestAskUserQuestionToolCreatesPendingActionAndResumesWithAnswer`
（request_permissions_test.go:2501，驱动 `askTool.Handle` 取 RAE）：ctx 加
`WithToolUseID(ctx, "call-ask-1")`，Handle 取 RAE 后断言
`req.AskFormJSON == actions.Get(req.ActionID).PayloadJSON` 且
`req.OriginatingToolCallID == "call-ask-1"`。漏改 registry.go 则此测试 FAIL。

## Fix design（三部分，归一到既有语义）

### A. 表单 payload 随 gate 走（根因修复）

1. `pkg/tool/request_permissions.go` `RequiresActionError`（486-502）**仅增** `AskFormJSON string`
   （`OriginatingToolCallID` 已存在，不动）。
2. `pkg/tool/registry.go` interactor 返回 RAE（495-508）增：
   `AskFormJSON: a.PayloadJSON`、`OriginatingToolCallID: ToolUseIDFromContext(ctx)`
   （= call.ID = step id；executeOneToolCall 注入 ctx，orchestration_llm.go:2171；
   file_tools.go:1711 同款用法）。
3. `pkg/turn/subagent_approval.go`：
   - `SubagentApprovalGate`（31-45）增 `AskFormJSON string`、`OriginatingToolCallID string`；
   - `SubagentApprovalGateFromError`（48-64）从 rae 投影两字段；
   - `SubagentApprovalRequest`（83-97）：ApprovalSource 增 `ToolStepID`（取
     `gate.OriginatingToolCallID`，空则 `PendingApprovalToolStepID(gate.Session, gate.ToolName)`；
     turn 无 firstNonEmpty，用局部 if）；Build 后 `req.AskFormJSON = strings.TrimSpace(gate.AskFormJSON)`。
     （`ApprovalSource.ToolStepID` 已存在 approval.go:38，BuildToolApprovalRequest 透传 :835。）
4. `pkg/tui/chat_surface.go` `planReviewToolApprovalRequest`（1658-1671）：ApprovalSource 增
   `ToolStepID: turn.ApprovalToolStepID(rae)`；Build 后 `req.AskFormJSON = strings.TrimSpace(rae.AskFormJSON)`。

主代理 PendingForRun 与 web 路径**不动**。活路径无 RAE 复制丢字段（tagRAEWithRunID /
attachSessionSnapshotToRAE 原位改，`%w` 保指针）。

### B. 放宽校验（提高首次成功率；保留结构性 identity）

新契约（刻意修订）：questions ≥1 **无上限**；每题 options ≥1 **无上限**（Other 恒在）；
label 非空 **无词数上限**；description **可选**；保留 header 非空+大小写不敏感唯一、
question 非空、label 非空（答案映射/选项 ID 的结构性前提）。

落点：
1. `pkg/tool/human.go`：
   - 常量（17-26）删 `maxQuestions/maxOptions/maxOptionLabelWord`；`minOptions` 2→1。
   - schema：`Input.Questions`(36) `minItems=1,maxItems=4`→`minItems=1`、描述去「(1-4 questions)」；
     `Question.Options`(44) `minItems=2,maxItems=4`→`minItems=1`、去「(2-4 options)」；
     `Choice.Label`(49) 去「(1-5 words)」；`Choice.Description`(50) 去 `minLength=1`、
     描述改「Optional explanation of this option」。
   - `validateInput`(142-191)：删 `>maxQuestions` 分支；选项数改 `len(q.Options) < minOptions`；
     删 label 词数分支；删 description 必填分支。
   - 错误变量(400-426)：删 `ErrTooManyQuestions/ErrOptionLabelTooLong/ErrOptionDescriptionRequired`
     （零外部引用）；`ErrInvalidOptionCount` 文案改「question must define at least one option」（名保留）。
   - 注释 29-32 同步。
2. `pkg/state/action_service.go` `CreateAsk`(184)：`len(q.Options) < 2`→`< 1`（同源第二处，必须同改）。
3. `pkg/state/action_service_test.go`(313 badForms)：删 1-option 反例；新增 1-option CreateAsk 成功正例。

展示安全（已验证，无需改 UI）：TUI overlay 全行 paint 期 `softWrapOverlayLine`
（reducer.go:6548/6592）折行；web `AskTabs.vue:78` label `min-w-0 break-words`、description `v-if`；
空 description 跳过（overlays.go:3329）；overlay 索引全相对 `len(Options)`，0/1 选项安全；
`AskAnswerSelectFirstOptionEachQuestion` 兼容 1 选项。与 owner「header/label 不设上限、展示折行」不变式一致。
**F6 降级说明**：>9 选项时数字键 1-9 跳转够不到第 10+ 项（overlays.go:2784-2790），方向键/j/k 仍可达；可接受。

### C. 提问失败显示 failed 卡（主/子共享；live==replay）

在 `answerInPlaceApproval` 错误分支（chat_surface.go:1514-1523）**lease select 之后的 default 路径、
Cancel 之前**调 `notifyInPlaceFailedCard(ctx, gate, req, err)`。
**F2 收窄**：lease-loss 分支（`case leaseErr := <-leaseLost: return …`）提前返回，**不发 failed 卡**
——lease 丢失=另一 surface 接管问题、子代理会在新 owner 下继续，画 Failed 是谎报；lease 转移保持现状（另期）。

新增 helper（仿 notifyInPlaceRefusedCard chat_surface.go:1600-1634，但**补持久化**，F3）：
```go
func (s *ChatSession) notifyInPlaceFailedCard(ctx context.Context, gate turn.SubagentApprovalGate, req turn.ToolApprovalRequest, promptErr error) {
	stepID := strings.TrimSpace(req.ToolStepID); toolName := strings.TrimSpace(req.ToolName)
	if stepID == "" || toolName == "" || promptErr == nil { return }
	input := map[string]any{} // 从 req.ToolInputJSON 解，失败则 {"raw": raw}
	failed := tool.StepEvent{Kind: tool.StepKindToolCompleted, StepID: stepID, ToolName: toolName, Input: input, Error: promptErr.Error()}
	stepCtx := tool.WithHookAgentID(ctx, strings.TrimSpace(gate.AgentID)) // 子→子视图；主""→主视图
	// 持久化 canonical failed 完成事件（filed on gate.RunID），replay 画同一张卡：
	for _, canonical := range turn.CanonicalEventsFromStep(stepCtx, req.SessionID, gate.RunID, "tui", failed) {
		if !event.IsPushOnlyStreamEvent(canonical.Type) { s.persistRunEvent(stepCtx, canonical) }
	}
	s.notifyToolStepHooks(stepCtx, req.SessionID, gate.RunID, "tui", failed)
}
```
机制核实：`notifyToolStepHooks`（notify.go:598）用 BuildRenderedToolStepNotification+BuildToolMeta+
EnrichToolMeta 渲染；EnrichToolMeta 从 ctx HookAgentID 取 meta.AgentID（format.go:330-331）→
帧进 per-agent VM（reducer.go:6311-6318），同 StepID 替换 parked 卡。`stepStatusLabel`
（format.go:2707）Error 非空→"failed"→卡头「Failed to ask user」（header_parts.go:562）+错误 body。
`persistRunEvent`（notify.go:1327）只需 runSvc（fixture 有），与 publishRunEvent 同样
AppendSessionEventOnce keyed by RunID → 子代理 replay 读同一行（**F3 live==replay**）。
failed 卡 Final=true 先于 sweep（子代理 parked/ended 均同步 FIFO，无竞态）→ sweep 跳过。
用户**主动取消**走 Cancelled decision 分支（非此错误分支）→ 语义不变。
**执行须验证**：tmux resume 场景子代理视图 replay 显示 failed 卡（非 Canceled）；若子代理
replay 漏斗读不到 persistRunEvent 所 file 的行，改用 runAuditStepHook 的 Events.Publish 漏斗（等价持久化）。

## 行为变化矩阵（旧→新；★=刻意修订）

| 场景 | 旧 | 新 |
|---|---|---|
| 子代理/plan reviewer/fork/fanout 提问（合法 label） | run 失败 missing ask form payload + Canceled | ★ 弹 overlay，答题后继续 |
| label >5 词 / >4 选项 / >4 问题 / 单选项 / 无 description | 工具拒绝（首次失败） | ★ 接受（折行/不渲染描述） |
| 0 选项/空 header/重复 header/空 question/空 label | 拒绝 | 拒绝（结构性，不变） |
| 子代理 in-place prompt 基础设施失败 | Canceled/(no output)，错误被吞 | ★ Failed to ask user + 错误文案（live+replay） |
| 主代理 in-place prompt 基础设施失败 | parked 卡卡死 awaiting approval（纯构造错误）或 Canceled（lease/cancel） | ★ Failed to ask user + 错误文案 |
| lease 转移（另一 surface 接管） | 子代理新 owner 下继续 | 不变（不发 failed 卡，F2） |
| 用户主动取消提问 | Canceled | Canceled（不变） |
| 主代理/web 正常提问 | 正常 | 正常（不变） |

## Scope

In scope：pkg/tool/request_permissions.go、pkg/tool/registry.go、pkg/tool/human.go、
pkg/turn/subagent_approval.go、pkg/tui/chat_surface.go、pkg/state/action_service.go；
测试 pkg/tool/human_test.go、pkg/tool/request_permissions_test.go、pkg/state/action_service_test.go、
pkg/tui/chat_surface_test.go；验收脚本 .claude/skills/run-forebrain/fake_provider.py（增 subagent-ask 场景）。

Out of scope（明确不碰）：
- pkg/tui/config_panel*.go 及 settings .vue/locales（peer 正在改）。
- pkg/tui/overlays.go/reducer.go/render.go 的 sweep 全局语义（只靠 failed 卡先终结帧）。
- pkg/gateway/*、frontend/*（web 不缺 payload；其 Canceled 语义另期）。
- validateAnswers（答案校验，非模型入参）；主代理 PendingForRun（已正确）。
- **F5 另期**：parked/unwind 审批循环 prompt 失败（chat_surface.go:252-254，payload 不缺、恢复路径）；
  plan reviewer no-sink 分支（chat_surface.go:1376-1377，A.4 修 payload 但此失败语义未覆盖）。本期注明遗留。

## Steps（每步带验证）

0. **复现先行**：加复现 1（不含 rae.AskFormJSON 行）+ 复现 2，跑
   `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestSubagentAskGateCarriesTheForm|TestInPlaceApprovalPromptFailureShowsFailedCard' -count=1`，
   确认两者 FAIL 且原因分别是空 payload / 无 failed 卡。不符即 STOP。
1. **A payload**：改 request_permissions.go/registry.go/subagent_approval.go/chat_surface.go(planReview)。
   复现 1 补 `rae.AskFormJSON = act.PayloadJSON` 行；新增 interactor 级回归（A.2）。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/tui -run 'Ask|SubagentApproval|InlineApproval' -count=1` 绿。
2. **B 校验**：改 human.go/action_service.go + 更新 action_service_test.go。新增 human_test.go 回归
   （仿 TestAskAcceptsHeaderWithoutLengthLimit human_test.go:293）：长 label/>4 选项/>4 问题/单选项/无 description
   各一例 ask 成功 + schema 不再声明对应上限。验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/state -count=1` 绿。
3. **C failed 卡**：chat_surface.go 加 notifyInPlaceFailedCard + 错误分支 default 路径调用。
   验证：复现 2 PASS；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 绿。
4. **包级回归 + vet**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`；
   `gofmt -l cmd pkg third_party plugins`（须空）；`CGO_ENABLED=1 go vet -tags fts5 ./...`。
5. **tmux 真机验收**（必做；加载 run-forebrain skill）：
   - 构建 `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`（或 driver.sh build）。
   - fake_provider.py 增 `subagent-ask`：主代理首请求 subagent_run 派 general-purpose；子代理自身请求
     （按 system prompt 识别，参考 subagent-limit/steer + SUBAGENT_SYSTEM_MARK）回 user_interaction
     （label >5 词长句、选项 >4）；收 tool result 后子代理回纯文本；主代理后续回纯文本。
   - `ENABLE_SUBAGENT=1 .claude/skills/run-forebrain/driver.sh start subagent-ask`；切子代理视图；
     `tmux capture-pane -t forebrain-run -p` 取证：**断言** overlay 出现（tab/选项/Other），
     **不出现** subagent failed / missing ask form payload / Canceled；Enter 答题→子代理继续到完成。
   - **replay 验收（F3）**：答题前/后 resume 该会话，子代理视图 replay 的提问卡与 live 一致（failed 场景显示 Failed 非 Canceled）。
   - 回归：`driver.sh start ask`（主代理两问）正常渲染/答题/继续；长 label 折行不溢出。
   - 中断自救：overlay 打开时 Esc → 子代理 run 干净结束（用户取消=Canceled 语义），EXIT 干净，再启动可恢复。
   - 清理：driver.sh stop、kill tmux、删临时 home/proj（driver 已隔离）。
6. **收口**：本计划回填「已实施并真机验收通过（日期）」+ 验收记录；`git status` 改动 ⊆ Scope，留工作区不 commit。

## Done criteria（机器可查）

- 复现 1/2 修复前 FAIL、修复后 PASS；interactor 级回归 PASS。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/state ./pkg/tui -count=1` 全绿。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` 全绿。
- `gofmt -l cmd pkg third_party plugins` 空；`CGO_ENABLED=1 go vet -tags fts5 ./...` 无输出。
- `grep -c "maxOptionLabelWord\|maxQuestions\|maxOptions" pkg/tool/human.go` = 0。
- tmux 子代理提问取证：overlay 出现、无 subagent failed/Canceled、答题后继续；replay 卡与 live 一致。
- `git status --porcelain` 改动 ⊆ Scope。

## STOP conditions

- 复现修复前不 FAIL 或失败原因不符 → 停，重新诊断。
- drift 内容不符（基线被推进且影响 in-scope）→ 停，重新对照。
- 放宽后既有断言失败：属契约修订的随契约更新注明理由；暴露真实回归 → 停，报 owner。
- 需改 Out-of-scope（尤其 config_panel*/gateway/sweep 全局）才能修好 → 停，问 owner。
- 真机子代理提问仍失败、或 replay 与 live 卡不一致 → 停，取证回报。
- 包级回归失败落在他人正在编辑文件 → peer_send 交该 agent。

## Maintenance notes

- 评审重点：RAE 新字段在所有 gate 构造点一致携带（interactor 唯一 ask 源）；failed 卡先于 sweep 且
  live==replay；CreateAsk 与 validateInput option 下限一致（均 ≥1）；lease-loss 不误发 failed 卡。
- 不变式延续：user_interaction header/label「不设长度上限、展示折行」（owner 2026-11-19）。
- 遗留（另期）：web gateway in-place 提问失败 Canceled 语义；sweep 对 run 失败 vs 用户取消的全局区分；
  parked/unwind 循环与 plan reviewer no-sink 的 prompt 失败语义（F5）；lease 转移显示（F2）。
- 既存问题（F7，各一行，不在本期）：option ID=label 同题重复 label 不校验、答案映射折叠
  （human.go:277、validateAnswers:209-218，删上限后碰撞概率升）；gateway promptGatewaySubagentApproval
  WS data 缺 tool_step_id（approval.go:334-338，与 inline 兄弟 :436 不一致）。

## Implementation Status

**VERDICT: APPROVE**（tech lead 独立复核，2026-11-20）。

- 实施者：executor 子代理（zhipuai/glm-5.3-flash），隔离 worktree
  `.worktrees/fix-user-interaction-subagent-ask`，分支同名，基线 `6c75da7f`，
  单 commit `7eb1057e`（未 push/merge；合并由 owner 决定）。
- 任务完成度：Step 0–5 全部 done；Step 6 计划回填由 reviewer（本文件）维护，executor 按 preamble 跳过。

### Reviewer 独立重跑的判据（均在 worktree 内，隔离 GOCACHE + scratch FOREBRAIN_HOME + PDFium pkg-config）

- 定点：`go test -tags fts5 ./pkg/tool ./pkg/turn ./pkg/state -count=1` → 全 ok。
- 五个新测试显式 `-v` 重跑全 PASS：TestSubagentAskGateCarriesTheForm、
  TestInPlaceApprovalPromptFailureShowsFailedCard、TestAskUserQuestionGateCarriesFormAndCall、
  TestAskAcceptsShapesBeyondTheRemovedCaps、TestAskStillRejectsQuestionWithoutOptions。
- 全量：`go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` → 除
  `pkg/architecture TestImportsAreGroupedStdlibFirst` 外全 ok。
- **architecture 失败属基线遗留，非本次 diff**（已证）：失败详情
  `pkg/tui/config_panel_test.go has groups std | std | ext`；
  `git diff 6c75da7f..HEAD -- pkg/tui/config_panel_test.go` 为空（未改）；
  `git show 6c75da7f:pkg/tui/config_panel_test.go` 头部即 `fmt`→空行→`bytes` 坏分组；
  diff 不含任何 architecture/config_panel 文件。系 peer P4 遗留，peer 已在 dev 修复（peer_send 确认）。
- `go vet -tags fts5 ./...` → 无输出（exit 0）。
- `gofmt -l pkg/tool pkg/turn pkg/state pkg/tui` → 空。
- `grep -c "maxOptionLabelWord\|maxQuestions\|maxOptions" pkg/tool/human.go` → 0。
- Scope：`git diff --stat 6c75da7f..HEAD` 11 文件全在 In-scope 清单；`git status --porcelain` 干净。
- **真机（reviewer 亲自驱动，隔离 work/session/port）**：构建 worktree 二进制 →
  `ENABLE_SUBAGENT=1 driver.sh start subagent-ask` → submit → provider-log
  `SUBAGENT-ASK handing out the gate`；子代理视图渲染问题 overlay（tab 芯片「☐ 实施方案」、
  5 选项、长 label「采用方案一并同步通知所有关联的下游服务负责人」完整折行、Other、Chat about this），
  失败文案 grep 计数 0，子代理卡「network probe └ ask user」运行中；Enter 答题 →
  provider-log `answered after the gate` → 屏幕「✓ network probe / ● primary done / Worked for 26s」。
  **replay（F3）**：resume 会话 → 主视图「primary done / resumed 4 messages」；点击任务卡进子代理视图 →
  「● Asked user └ 子代理在继续任务前需要你确认实施方案。 → 采用方案一…」（含答案，非 Canceled）+
  「answer received, subagent done」。live==replay 证实。验收后已 stop/kill/清理临时目录与 review 缓存。

### 偏差（已记录，按 merit 接受）

- 复现 2 用带 2s 超时的 channel 收集 UI 消息（计划写 slice）：UI 派发走异步 goroutine，
  channel 是既有先例（chat_surface_test.go:1497）形态，断言语义不变（仍断言 failed 卡含错误文案）。
- fresh worktree 无三方依赖且 GitHub 直连超时：executor 从主树复制 gitignored 的
  third_party/{libtokenizers,pdfium} 与 pkg/gateway/dist，手动等价构建（go build + install-dictionary/assets），
  绕过 driver.sh build 的联网 fetch。reviewer 真机复核沿用此法（依赖已备，无联网）。
- SubagentApprovalGateFromError 用 `ApprovalToolStepID(rae)` 投影 OriginatingToolCallID
  （含 snapshot 兜底），SubagentApprovalRequest 再兜底一次——双重兜底冗余但无害，符合计划意图。

### 未做（计划内注明另期，未动）

- web gateway 提问失败 Canceled 语义；sweep 全局区分；parked/unwind 与 plan reviewer no-sink
  的 prompt 失败语义（F5）；lease 转移显示（F2）；option ID=label 同题重复不校验（F7）。
- 测试小瑕（非阻断）：超限形状测试的长 label 用中文（strings.Fields=1 词），未直接用 >5 英文词
  label 钉词数上限 removal；但词数分支已整段删除、schema 断言钉住无上限，覆盖充分。

### 落地（owner 指示，经 peer forebrain-harness-dc 协调）

- owner 指示「合并回主树新分支，后续 owner 评审」。peer 代做 rebase（worktree 无 WIP，reviewer 授权 B 走法）：
  分支 `fix-user-interaction-subagent-ask` HEAD 由 `7eb1057e` 重写为 **`4de94476`**，基线挪到 dev tip **`b1cce725`**，
  零冲突，diff --stat 11 文件 +423/-42 不变，scope 与清单一致。未 push/merge；合并进 dev 由 owner 决定。
- **rebase 后 reviewer 再次独立重跑全部判据，全绿**：
  - 全量 `go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` 全部 ok，**含 pkg/architecture**
    （dev 已含 peer P6 `5b644881` import-grouping 修复，此前唯一失败 TestImportsAreGroupedStdlibFirst 消失）。
  - 五个新测试显式 -v 全 PASS；`go vet -tags fts5 ./...` 无输出；`gofmt -l cmd pkg third_party plugins` 空；caps grep=0。
  - worktree git status 干净，无残留 rebase 状态。
- 已向 peer 报「分支已就绪待评审」，由 peer 转报 owner。

### 遗留待 owner

- 合并 worktree 分支 `fix-user-interaction-subagent-ask`（HEAD 4de94476，基线 b1cce725）进 dev 由 owner 决定。