# PLAN_REVIEW_DELIVERY_SILENT:plan review 交付全程静默化(删死"Plan review delivered"消息语义)

> 执行者须知:按步骤逐步执行,每步跑验证命令并确认预期结果。触发 STOP condition
> 时停下报告,不要即兴发挥。批准后第 0 步先把本计划落盘到仓库
> `docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md`,实施完成回填验收记录。
>
> 评审记录:2026-10-09 zhipuai/glm-5.3 评审通过(附 6 项修改),已全部核实并吸收:
> TUI 事件回放出口(replayApprovalEventFrames)、web 历史工具卡确定性丢弃、
> pkg/run 测试断言修订、`turn` 参数遮蔽、Confirmation 写入方事实修正、
> 失败重挂补码从条件改为必做、常量留 pkg/tool 原地改名。
>
> **实施中 owner 裁决(2026-10-09,取代 Step 3/4)**:"Exiting plan mode这条消息卡
> 完全不需要,彻底删掉,清理死代码"、"任何情况下不要显示Exiting plan mode消息"。
> exit_plan_mode 的等待卡整体删除:live 漏斗不再为 exit_plan_mode 发 running/
> awaiting approval 卡(`tool.ToolStepHoldsNoCard` 共享规则,TUI 漏斗与 web 时间线
> 同步过滤),render.go 标题表的 "Exiting plan mode" 分支随之删除;原 Step 3
> (WithdrawPendingTool 撤卡)与 Step 4(失败重挂补发等待卡)连同其测试全部作废
> 并按"删除语义无痕迹"纪律清除。失败重挂只剩审批 overlay 重提示,transcript 不画卡。

> **勘误（2026-10-10，见 `plans/007-exit-gate-live-card-and-cardless-anchoring.md`）**：主 agent 的工具步骤经
> `runAuditStepHook → notifyToolStepHooks` 直接进 UI，不经 `publishRunEvent` 漏斗，所以本文"TUI live 已干净 /
> 等待卡已删除"的结论不成立；gate 等待帧仍被画成 "Exited plan mode"、取消后被改成 "Canceled"。007 把过滤移到
> reducer 入口，并让无卡 gate 的确认行与 review 卡按产生顺序落位（live 与回放一致）。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED(横跨 TUI live/replay、gateway 事件、web 三面,含持久化数据兼容)
- **Depends on**: none
- **Category**: bug(产品语义:内部管线消息泄漏给用户)
- **Planned at**: 2026-10-09 工作区(drift 检查用下方 file:line 对照,不依赖 SHA)

## Why this matters(owner 原话裁决)

用户选择其他模型 review plan 的场景,当前把内部交接消息暴露给了用户:

1. review 运行期间,TUI 悬挂 "◆ Exiting plan mode" 等待卡——"这行消息也不需要,也要删掉"。
2. review 交付时,TUI 打印 "◆ Kept planning / Plan review delivered — the planner is
   revising the plan." 工具行 + "● system / Plan review delivered — ..." 消息行——
   "不要显示,不需要暴露给用户"。
3. web 端同样收到/渲染这条消息(时间线 handoff 行、历史工具卡正文)——"web端也要改"、
   "Plan review delivered消息可以不发送给web端和tui端"。
4. LLM 侧验证:模型收到的是 `reviewDeliveryGuidanceHeader` + 评审全文
   (`pkg/turn/approval.go:1121-1125`、`pkg/run/orchestration_llm.go:119-121`),
   **从不**收到这句话 → "如果也不需要发送给llm,那就没用了,可以把相关代码删掉,清理死代码"。

## 根因(file:line 证据链)

这句话共 **7 个用户可见出口**,全部源自一个常量 `pkg/tool/format.go:28`:

```
const PlanReviewDeliveredText = "Plan review delivered — the planner is revising the plan."
```

| # | 出口 | 位置 |
|---|------|------|
| 1 | TUI live "Kept planning" 工具行 | `pkg/tui/chat_turn.go:1067-1080`(notifyToolApprovalDenied:deliveredReview→content=常量;标题映射 render.go:2655-2658) |
| 2 | TUI live system 消息 | `pkg/tui/chat_surface.go:968-972`(deliverCompletedPlanReview notifyUI(MsgKindSystem)) |
| 3 | TUI 交付事件 Confirmation | `pkg/tui/chat_surface.go:965` |
| 4 | gateway 交付事件 Confirmation | `pkg/gateway/api_extra.go:2903` |
| 5 | TUI 回放:持久化事件 Confirmation → FrameStatus 行 | `pkg/tui/commands.go:617-643`(replayApprovalEventFrames;入口 :304)——老会话重启后仍显示 |
| 6 | TUI 回放:持久化 denial 行(tool_display body=常量)→ 工具卡 | `pkg/tui/commands.go:1089-1150`(replayToolMessage) |
| 7 | web 回放:①重放事件 confirmation → ApprovalCard 行(`ApprovalCard.vue:9,39`);②历史 denial 行 body → 工具卡正文(`useChatStream.ts` toolStepFromRow,output: display.body;主会话行映射 ~:555-562) | 老会话仍显示 |

"Exiting plan mode" 悬卡:审批 gate 首次停靠时由运行侧工具步事件发射一次
(`pkg/tool/format.go:2273-2277`:completed+requires-approval → "awaiting approval"),
进入渲染器 viewModel 可重绘块(`render.go:787-798` 证明)。用户选 review 后
(`completeSurfacePlanReviewRequest` `pkg/tui/chat_surface.go:835-855`)没有任何路径
解除它,直到交付被出口 #1 替换。

**不死的部分(必须保留)**:
- `Confirmation` 字段是用户真实决定的通用机制。写入方全集(已核实):
  `pkg/tui/chat_session.go:1336`(用户 approve/deny 决定行)——**TUI 独有**;
  gateway 发布的 resolved 事件(`pkg/gateway/approval.go:474`、`api_extra.go:3051`)
  **不带** Confirmation(既有行为,不在本计划范围)。删的只是两个**交付**写入方
  (#3、#4)。
- 交付的 gate 关闭语义:`PlanReviewDeliveredReason`/`planReviewDeliveredAnswer`/
  `ActionIsReviewDelivered`(`pkg/turn/approval.go:1087-1112`)与 denied resume
  (评审全文进模型上下文)全部保留。
- 用户自己 deny 的 "Kept planning" 行(带用户反馈)是真实决定历史,保留
  (`pkg/tui/chat_session_test.go:10408` 锁定)。
- 持久化 denial tool_display 本身(出口 #6 的数据源)保留:它是回放护栏,防止
  replay 把模型侧评审全文当显示体(`pkg/run/orchestration_llm_test.go:4577` 已锁定
  "display half 不得含模型侧文本")。变的是消费者:从"被渲染"变为"被识别后整行丢弃"。
  该句常量因此从"显示文案"转为"持久化丢弃键"——它唯一的存活形态。

## 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| review 运行中(TUI) | "Exiting plan mode" 悬卡 + "✔ You asked..." 行 + review 子代理块 | 悬卡消失;其余不变 |
| review 失败(TUI) | 悬卡保持,审批重挂 | 悬卡已撤 → 重挂时**补发**等待卡(Step 4 必做) |
| review 交付(TUI live) | 悬卡替换为 "Kept planning/Plan review delivered..." 行 + system 行 | 零输出;审批静默关闭,planner 直接修订 |
| review 交付(web live) | 卡关闭 + 时间线 handoff 行 | 卡关闭,无行(上游不再发 Confirmation) |
| 用户直接 deny(TUI/web) | "Kept planning"+反馈;TUI 决定 Confirmation 行 | 不变 |
| 老会话回放(TUI) | ①事件 Confirmation 行 ②denial 行,均显示该句 | ①②整行丢弃(出口 #5/#6) |
| 老会话回放(web) | ①重放事件行 ②历史 denial 工具卡正文 | ①confirmation 过滤 ②该行整行丢弃(出口 #7) |
| LLM resume | guidance header + 评审全文 | 不变 |
| Esc 取消 review、web stop-review | 现有行为 | 不变 |

## Scope

**In scope(只改这些)**:
- `pkg/tool/format.go`(常量原地改名 `PlanReviewDeliveredDisplayKey` + doc 改写)
- `pkg/turn/approval.go`(删除 1114-1119 的死 alias 及注释——改名后无引用者)
- `pkg/tui/chat_surface.go`(删 system 消息块、删事件 Confirmation、review 开始撤卡、失败重挂补发等待卡)
- `pkg/tui/chat_turn.go`(delivered 情形跳过工具行发射)
- `pkg/tui/run.go` + `pkg/tui/render.go`(新增 WithdrawPendingToolMsg → 渲染器撤卡)
- `pkg/tui/commands.go`(共享丢弃判定 helper;replayToolMessage + replayApprovalEventFrames 两处过滤)
- `pkg/run/orchestration_llm.go`(常量引用改名;持久化行为不变;注释补丢弃键职责)
- `frontend/src/composables/useChatStream.ts`(丢弃键常量;事件 confirmation 过滤;历史 denial 行丢弃)
- 测试:`pkg/tui/chat_session_test.go`、`pkg/tui` replay/reducer 新测试、`pkg/run/orchestration_llm_test.go`、gateway 交付断言、`frontend/src/composables/useChatStream.test.ts`、`frontend/src/components/chat/ApprovalCard.test.ts`、`frontend/e2e/exit-plan-approval.spec.ts`
- `pkg/architecture/testdata/graph.json`(仅当 import 实际变化)

**Out of scope(明确不碰)**:
- web "正在评审这份计划" 卡与 stop-review 控件(交互功能,未被要求删)
- `PlanReviewDeliveredReason`/`ActionIsReviewDelivered`/denied resume 模型侧语义
- 用户 deny 的 "Kept planning" 行与 `DeniedToolDisplayBody`
- 用户决定的 Confirmation 机制(`chat_session.go:1336`)
- review 子代理块("Ran 1 plan-reviewer task")
- web 发起 review 时另一端 TUI 的联动呈现(web 发起/TUI 观看属既有跨面行为,本计划不涉及;验收记录注明)

## Steps

### Step 0: 落盘计划到 docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md
复制本文件;实施中的补充直接改 docs/plan 副本。
**Verify**: `ls docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` → 存在

### Step 1: 常量原地改名 + 语义改写(不新增包边)
1. `pkg/tool/format.go:24-28`:doc 注释改写 + 常量改名为
   `PlanReviewDeliveredDisplayKey`(值不变)。doc 要点:"持久化 denial 显示体上的
   交付丢弃键:交付关闭的 gate 其 denial 行的唯一内容,任何 surface 都不渲染它——
   TUI/web 的回放投影识别到它就整行丢弃。仅交付的持久化路径写入。"
2. `pkg/turn/approval.go:1114-1119`:删除 `PlanReviewDeliveredText` alias 及其注释块
   (改名后无引用者;引用方改用 `tool.PlanReviewDeliveredDisplayKey`)。
3. `pkg/run/orchestration_llm.go:137`:`tool.PlanReviewDeliveredText` →
   `tool.PlanReviewDeliveredDisplayKey`(pkg/run 已 import tool,无新包边)。
   128-143 持久化行为一字不改,仅注释补一句"body 是回放丢弃键,不被渲染"。

**Verify**:
```bash
grep -rn "PlanReviewDeliveredText" pkg cmd frontend/src --include=*.go --include=*.ts --include=*.vue   # 期望:零匹配(含测试,测试随 Step 7 一起改;此步完成后测试未改前允许测试文件命中)
CGO_ENABLED=1 go build ./...   # exit 0
```

### Step 2: 交付侧用户可见出口删除(TUI live + 两端事件)
1. `pkg/tui/chat_surface.go:968-972`:删除 notifyUI(MsgKindSystem) 整块。
2. `pkg/tui/chat_surface.go:957-967`:删除 `Confirmation:` 字段及"web 时间线回放同一
   句"注释行(事件保留:驱动卡片关闭/状态)。
3. `pkg/gateway/api_extra.go:2895-2905`:同删 `Confirmation` 字段与对应注释。
4. `pkg/tui/chat_turn.go` notifyToolApprovalDenied(1026-1092):deliveredReview==true
   时**整体跳过 UI 发射**(在 1067-1073 判定后提前 return),删除 1074-1080 的
   content 覆盖分支;deliveredReview 判定(actionSvc().Get +
   turn.ActionIsReviewDelivered)保留。user-deny 路径不动。

**Verify**: `CGO_ENABLED=1 go build ./...` → 0;
`grep -n "PlanReviewDeliveredDisplayKey" pkg/tui/chat_surface.go pkg/gateway/api_extra.go pkg/tui/chat_turn.go` → 空

### Step 3: review 开始时撤掉 "Exiting plan mode" 悬卡(TUI)
1. `pkg/tui/render.go`:仿照 `FinalizePendingTools`(764-798)新增
   `WithdrawPendingTool(stepID string)`:遍历 `r.vm` 与 `r.perAgentVM`,删除
   `Kind==FrameTool && toolStatusPending(frame) && frame 的 StepID==stepID` 的 VM 块
   (StepID 取 frame.ToolMeta.StepID,与 buildFrame 写入一致,实施时核对字段),
   失效命中块缓存,`paintViewportLocked()`;零匹配静默返回。
2. `pkg/tui/run.go` 消息循环:新增 `WithdrawPendingToolMsg{StepID string}` 类型与
   case → `renderer.WithdrawPendingTool(m.StepID)`(与 StreamResetMsg→
   FinalizePendingTools 同型,run.go:346-349)。
3. `pkg/tui/chat_surface.go` completeSurfacePlanReviewRequest(~835-855):校验通过后、
   `runPlanReview` 之前:复用既有只读访问器 `s.peekPendingApproval()`
   (`pkg/tui/chat_turn.go:203`,无需新增)取 `p.ToolStepID` →
   `s.notifyUI(WithdrawPendingToolMsg{StepID: p.ToolStepID})`;peek 为 nil 时跳过。

**Verify**: `CGO_ENABLED=1 go build ./...` → 0;`go vet ./...` → 0

### Step 4: review 失败重挂时补发等待卡(必做,已核实无现存重发路径)
已核实:重挂循环(`chat_surface.go:158-176`)只经 `sink.PromptToolApproval` 重新
提示,等待帧只在 gate 首次停靠时由运行侧工具步事件发射一次
(`pkg/tool/format.go:2273-2277`),撤卡后没有任何东西重发它。
实施:在 `runPlanReview`(`chat_surface.go` ~862-922)的失败分支
(`reviewErr != nil`,~918 处返回前)重发等待帧:`s.notifyUI` 一条
`Message{Kind: MsgKindTool, StepID: p.ToolStepID, ToolName: "exit_plan_mode",
ToolMeta: {ToolName: "exit_plan_mode", Status: "awaiting approval"}}`(消息形状照
`chat_session_test.go:10413-10417` 夹具;经 peekPendingApproval 取 p,取不到则跳过)。
交付成功分支不补发(卡已随交付消失)。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestPlanReviewFailure' -count=1` → pass(含"卡回来"新断言)

### Step 5: TUI 回放双出口丢弃(新老数据一致;注意 `turn` 遮蔽)
`pkg/tui/commands.go` 顶部新增包级 helper(避免 replayToolMessage 的 `turn state.Message`
参数遮蔽 pkg/turn——评审点 4,直接在函数体内写 `turn.X` 无法编译):

```go
// isPlanReviewDeliveredDisplay reports whether a stored denial display body is
// the review-delivery drop key: such rows are internal handoff plumbing and are
// dropped whole by every replay projection.
func isPlanReviewDeliveredDisplay(body string) bool {
	return strings.TrimSpace(body) == tool.PlanReviewDeliveredDisplayKey
}
```

1. replayToolMessage(1089-1150):`hasStoredDisplay` 分支(1108-1112)后加
   `if isPlanReviewDeliveredDisplay(storedDisplay.Body) { return Message{}, "", false }`。
2. replayApprovalEventFrames(617-643):`line` 剥离(634-637)后加
   `if isPlanReviewDeliveredDisplay(line) { return nil }`(老交付事件的 Confirmation
   存的是裸句子,无 ✔ 前缀,sgr 剥离后精确等于键)。
3. 确认 transcript 渲染路径(commands.go:860 一带)经 replayToolMessage 出卡;若存在
   第二构建器,同一判定提为共用 helper。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Replay' -count=1` → pass

### Step 6: web 丢弃键过滤(确定性,非条件——评审已证实两出口都渲染)
`frontend/src/composables/useChatStream.ts`:
```ts
export const PLAN_REVIEW_DELIVERED_KEY = 'Plan review delivered — the planner is revising the plan.'
```
1. 事件 confirmation 过滤(gateway 会把持久化 `approval_resolved` 事件原样重放给新
   连接,`pkg/gateway/server.go:924-947`,老事件带 confirmation):两处消费点——
   subagent 视图(~:2795/2820)与会话时间线(~:3773/3785)——统一 helper:
   `const confirmation = raw === PLAN_REVIEW_DELIVERED_KEY ? undefined : normalize(raw)`。
2. 历史 denial 行丢弃(评审已核实:主会话历史 tool 行经 ~:555-562 映射进
   `toolStepFromRow`,其 `output: display.body` 即该句,会渲染成工具卡正文):
   在该 tool 行映射处,`parseToolDisplayPart(row.partsJson).body?.trim() ===
   PLAN_REVIEW_DELIVERED_KEY` → 整行跳过(不 push block、不占 drawn)。

**Verify**: `cd frontend && pnpm test -- useChatStream` → pass(含新过滤用例)

### Step 7: 测试修订(删语义断言,不写"不再出现"反断言)
- `pkg/run/orchestration_llm_test.go:4573`:断言改为
  `got != toolpkg.PlanReviewDeliveredDisplayKey`(持久化契约保留);
  :4577 的"display half 不得含模型侧文本"断言保留。
- `pkg/tui/chat_session_test.go` TestPlanReviewAutoDeliversToPlannerAfterDone
  (~12620-12705):删 `resolved[0].Confirmation` 断言(~12646-12648)与 sawNotice
  循环(~12650-12662);status/marker/事件数/resume 内容断言全保留。
- gateway:`grep -rn "PlanReviewDelivered\|Confirmation" pkg/gateway --include=*_test.go`
  逐个核对交付断言(已知 `api_extra_test.go:3604` 一带),随契约删除/改写。
- `frontend/src/composables/useChatStream.test.ts:2014/2022`:改为新契约——重放事件
  confirmation=丢弃键 → block 无 line(新过滤语义的回归测试,非反断言)。
- `frontend/src/components/chat/ApprovalCard.test.ts:32-35`:样本 confirmation 改为
  普通用户决定文案(机制保留,继续锁定 line 渲染)。
- `frontend/e2e/exit-plan-approval.spec.ts` 第二条用例(~176-210):删 203-207
  handoff 文本断言,保留 `card` toHaveCount(0) 与截图;用例名去掉
  "the timeline says so"。

新增测试(Test plan 细则见下),全部随各 Step 落地。

## Test plan(新增)

1. **撤卡(reducer/renderer 级)**:`pkg/tui` 新测试——构造 awaiting 工具帧(形状照
   `TestExitPlanModeDeniedToolFrameReplacesPendingCard`,10408)→ WithdrawPendingToolMsg
   → 帧消失且不产生新帧;user-deny 完成消息仍正常替换为 "Kept planning"。
2. **撤卡接线(session 级)**:`newPlanReviewSession` 夹具 + stub reviewer,决策带
   RequestPlanReview 后,断言 UI 消息流出现 WithdrawPendingToolMsg 且
   StepID==审批工具步。
3. **失败重挂卡回来**:扩展 TestPlanReviewFailureKeepsTheApprovalAndReportsIt——
   review 失败后重挂呈现补发 awaiting 帧(Step 4)。
4. **TUI 回放双丢弃**:①tool-role 持久行(tool_display body=丢弃键、status=denied)
   → replayToolMessage ok=false;body 为用户反馈的 denial 行仍出现;②持久
   approval_resolved 事件(Confirmation=丢弃键)→ replayApprovalEventFrames 返回 nil;
   Confirmation 为普通决定行时照旧出 FrameStatus。
5. **web 过滤**:useChatStream.test.ts——①approval_resolved confirmation=丢弃键 →
   block.confirmation undefined;普通 confirmation 照旧;②历史 denial 行 body=丢弃键
   → 不生成 tool block;普通 denial 行照旧。

汇总验证命令:

```bash
gofmt -l pkg cmd                                            # 空
CGO_ENABLED=1 go build ./...                                # exit 0
go vet ./...                                                # exit 0
scripts/package-graph.sh                                    # 若 graph.json 变化,重生成并留在工作区
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/run ./pkg/gateway ./pkg/turn ./pkg/tool -count=1   # 定点包
CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1                                 # 全量回归
cd frontend && pnpm test && pnpm build                      # 前端
```

## Done criteria(机器可查)

- [ ] `grep -rn "PlanReviewDeliveredText" pkg cmd frontend/src` → 零匹配(旧名无痕迹)
- [ ] `grep -rn "PlanReviewDeliveredDisplayKey" pkg --include=*.go | grep -v _test` → 恰好:format.go 定义、orchestration_llm.go 持久化、commands.go helper 三处
- [ ] `grep -rn "Confirmation" pkg/tui/chat_surface.go pkg/gateway/api_extra.go | grep -v _test` → 交付路径无该字段
- [ ] 上述全部测试命令 exit 0
- [ ] `git status` 改动文件全部落在 In scope 清单内
- [ ] docs/plan 副本回填验收记录

## tmux 真机验收(plan-gated-bugfix 步骤 7)

```bash
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain   # 必带 fts5
tmux new-session -d -s fbt -x 120 -y 42 \
  'cd <临时项目目录> && FOREBRAIN_HOME=<临时home> ./build/bin/forebrain; echo EXIT_CODE=$?; sleep 300'
```

场景(隔离 FOREBRAIN_HOME + 临时项目目录,不污染真实 home):
1. 驱动到 exit_plan_mode 审批 → 选 "review with another model" → capture:
   无 "Exiting plan mode" 卡(撤卡重绘生效)、"✔ You asked ..." 行与 review 子代理块在。
2. review 完成(或 Esc 中途取消走重挂)→ capture:交付时无 "Kept planning"/system 行;
   取消重挂时等待卡重新出现。
3. 重启/`/resume` 同会话 → capture:回放无 "Plan review delivered" 行(事件行与
   denial 行均无)。
4. `forebrain gateway start </dev/null` 非 TTY 路径不回归(诊断正常、不挂起)。

若隔离环境无法配置第二个评审模型:以单元/reducer 测试 + 重启回放场景代替 1-2 的
live 部分,并在验收记录写明限制(不虚构证据)。

## STOP conditions

- `pkg/tui/chat_session.go:1336` 的用户决定 Confirmation 写入点被发现依赖交付写入方
  (与本计划"机制通用、仅删交付方"的前提矛盾)。
- Step 4 补发的等待帧在重挂呈现上不可见或与审批提示框状态不一致,且修复超出
  chat_surface 呈现层。
- Step 5 helper 方案遇到 replayToolMessage/replayApprovalEventFrames 之外的第三个
  持久化出口且形态不同(报告后再统一)。
- 任何验证命令两次合理修复后仍失败。

## Maintenance notes

- 丢弃键是持久化契约:改这句文案 = 改协议,必须保留旧键做识别,否则老会话回放
  重新泄漏。
- review 交互此后只剩三个用户可见物:"✔ You asked..." 确认行、review 子代理块、
  失败/重挂的审批卡。给 review 加新 UI 时勿再引入交付侧文案(交付是静默交接)。
- 评审人审查 PR 时重点:Step 2 的提前 return 不得吞掉非 delivered 的 user-deny
  发射;Step 5/6 过滤只做键的精确等值匹配,不得放宽为模糊匹配。

## Implementation Status(2026-10-09 实施完成)

### 落地清单

| 计划步骤 | 结果 |
|---|---|
| Step 0 计划落盘 | ✅ 本文件 |
| Step 1 常量改名 | ✅ `pkg/tool/format.go` `PlanReviewDeliveredDisplayKey`(doc 改写为丢弃键语义);`pkg/turn/approval.go` 死 alias 删除;`pkg/run/orchestration_llm.go` 改名+注释 |
| Step 2 交付可见出口删除 | ✅ `chat_surface.go` 事件 Confirmation+system 块删除(doc 同步);`gateway/api_extra.go` Confirmation 删除;`chat_turn.go` notifyToolApprovalDenied delivered 提前 return |
| Step 3 撤卡 / Step 4 失败补卡 | ❌ **被 owner 实施中裁决取代**(见文件头裁决块):exit_plan_mode 等待卡整体删除,撤卡/补卡机制按"删除语义无痕迹"纪律清除(WithdrawPendingToolMsg、WithdrawPendingTool、pendingApprovalToolStepForAction 及其测试全部不存在) |
| 替代实现:等待卡删除 | ✅ `pkg/tool/format.go` 新增 `ToolStepHoldsNoCard`(共享显示规则)+ 谓词测试;`pkg/tui/notify.go` 事件漏斗过滤 exit_plan_mode started/awaiting-completed;`pkg/tui/render.go` 标题表 "Exiting plan mode" 死分支删除;web `useChatStream.ts` `stepHoldsNoCard`/`isExitPlanGate`(主时间线 handleStepEvent 过滤)+ conversationFromTranscript 未应答 exit 调用不画卡(记 `gateStepIds` 供审批锚定) |
| Step 5 TUI 回放双出口 | ✅ `pkg/tui/commands.go` `isPlanReviewDeliveredDisplay` helper;replayToolMessage denial 行整行丢弃;replayApprovalEventFrames 事件行丢弃 |
| Step 6 web 丢弃键过滤 | ✅ `PLAN_REVIEW_DELIVERED_KEY` 常量;subagent 视图 + 会话时间线两处 confirmation 过滤;历史 denial 行(claimed + orphan 两分支)确定性丢弃 |
| Step 7 测试 | ✅ `pkg/run/orchestration_llm_test.go` 常量断言改名(display-half 契约保留);`chat_session_test.go` 删 Confirmation 断言与 sawNotice;`TestExitPlanModeDeniedToolFrameStandsAlone` 重写(denied 帧独立成卡);`pkg/tool` 谓词测试;gateway `api_extra_test.go` 删 Confirmation 断言;前端 useChatStream/ApprovalCard/e2e 按新契约改写 |

### 计划未预见、实施中发现并修复

1. **web 审批锚定依赖 gate 卡存在**:upsertConversationApproval 按 toolStepId 找宿主消息,exit 卡不画后决定记录会丢失。修复:conversationFromTranscript 对被扣卡的 gate 记录 `ChatMessage.gateStepIds`,审批记录锚到该 turn;`upsertApprovalBlock` 在具名 gate 卡缺席时退到"插在最后一个工具卡之前"(与 TUI InsertBeforeLastTool 同规则)。
2. **web reload 的 canceledToolStep 兜底**会给未应答 exit 调用合成 canceled 卡(与 live 过滤后不一致,parity 测试抓到):未应答 exit 调用同样不画卡。
3. ToolStepID 解析:计划假设 `peekPendingApproval()` 可用——实测夹具与重启场景只有持久 wait row(其 SessionSnapshot 才是权威源,approval.go:1660);该发现随撤卡方案一起作废,留档防再犯。

### 验证记录(全部实跑)

- `gofmt -l pkg cmd` → 空;`CGO_ENABLED=1 go build ./...` → 0;`go vet ./...` → 0
- 定点:`go test -tags fts5 ./pkg/tui ./pkg/run ./pkg/gateway ./pkg/turn ./pkg/tool -count=1` → 全绿(pkg/run 一次 TempDir 清理 flake,复跑即绿)
- 全量:`go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` → 全绿(磁盘满一次,清 go build cache 后复跑绿);`./pkg/architecture` 重生成 graph.json 后复跑绿(graph 仅 loc 漂移,无包边变化)
- 旧名零残留:`grep -rn "PlanReviewDeliveredText" pkg cmd frontend/src` → 0;`PlanReviewDeliveredDisplayKey` 恰好 3 处生产引用(format/orchestration_llm/commands)
- 前端:`pnpm test` 313 passed;`pnpm build` 成功
- Done criteria 逐项:✅(Confirmation 字段交付路径已无;改动文件全部在 In scope+本文件;dist 构建产物随前端改动更新)

### tmux 真机验收(driver.sh + fake provider,隔离 FOREBRAIN_HOME)

1. **等待卡删除**:`start toolcall` 驱动到 exit_plan_mode 审批停靠——transcript 只有 `● calling exit_plan_mode`,无任何 "Exiting plan mode" 卡 ✅(对照:enter_plan_mode 停靠留下其 settled 卡 "◆ Entered plan mode",正常)
2. **用户 deny 路径**:选 "No, keep planning" + 反馈——`✗ You did not approve...` 决定行 + `◆ Kept planning └ split the plan into milestones` 完整保留,无等待卡 ✅
3. **重启回放**:stop/start + /resume——回放无 "Exiting plan mode"、决定行不重复、Kept planning 保留 ✅
4. **交付丢弃键回放(造数据)**:向隔离 state 库注入 denial tool 行(body=丢弃键)+ approval_resolved 事件(confirmation=丢弃键)后回放——两者零显示,turn 只剩 user 卡 ✅(replayToolMessage 与 replayApprovalEventFrames 两出口真机证实)
5. **限制(按计划降级条款)**:live review 交付全流程(需第二评审模型)未在隔离环境真机跑,以会话级单测(TestPlanReviewAutoDeliversToPlannerAfterDone 等)+ 造数据回放代替;gateway 非 TTY 场景未真机跑(本任务未动启动路径,构建+gateway 测试绿)。
