# EXIT_PLAN_GATE_GHOST_CARD:回放不得给被取消/未决的 exit-plan gate 画"Exited plan mode"卡

> 执行者须知:按步骤逐步执行,每步跑验证命令并确认预期结果。触发 STOP condition
> 时停下报告,不要即兴发挥。批准后第 0 步先把本计划落盘到仓库
> `docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md`,实施完成回填验收记录。
> 基线:`d501e09`(工作区含大量与本修复无关的未提交改动,见 Scope"不碰"清单;
> drift 检查用下方 file:line 对照,不依赖 SHA)。

> **勘误（2026-10-10，见 `plans/007-exit-gate-live-card-and-cardless-anchoring.md`）**：主 agent 的工具步骤经
> `runAuditStepHook → notifyToolStepHooks` 直接进 UI，不经 `publishRunEvent` 漏斗，所以本文"TUI live 已干净 /
> 等待卡已删除"的结论不成立；gate 等待帧仍被画成 "Exited plan mode"、取消后被改成 "Canceled"。007 把过滤移到
> reducer 入口，并让无卡 gate 的确认行与 review 卡按产生顺序落位（live 与回放一致）。

> 评审记录:2026-10-09 deepseek/deepseek-v4-flash 评审结论 rework,5 项主张
> 逐条核实**全部成立**并已吸收:
> ① 被 cancel 的 exit gate 会经 `turn.AnswerAbandonedToolCalls` 写出**成交**
> canceled 工具行(chat_turn.go:1153-1155),回放走 `replayToolMessage` 而非
> 孤儿路径——初稿只堵孤儿路径不完整,改法改为在**每个** exit gate 投影口过滤;
> ② 既有测试 `TestResumeRebuildsOneCanceledCardPerOrphanToolCall`
> (commands_test.go:2136-2197)正向锁死假卡且其前提(live 有等待卡、
> FinalizePendingTools 盖 canceled)自 d501e09 起已不成立,按"删掉的语义不留
> 断言"整条改写;③ 初稿"清除 InsertBeforeLastTool"子步与 live/web 既有锚定
> 语义相冲突(PrintApprovalConfirmation 恒置 true、live 本就无 gate 卡),
> **删除**该子步,落点保持 live/web/回放一致;④ `TestToolStepHoldsNoCard` 已
> 存在(format_test.go:2475),改为扩展正向清单;⑤ owner 拍板(2026-10-09):
> **web 同步**——canceled exit 成交行在 web 端同样整行丢弃(1:1 parity)。
> 评审未能验证"canceled exit 成交行"的真实落盘样本(库中只见到
> denied/approved),场景 D 真机实测负责坐实该路径。

## Status

- **Priority**: P1
- **Effort**: M(TUI 回放两处 + web 一处 + 测试改写)
- **Risk**: LOW–MED(只动回放投影与一个共享谓词,不碰审批/交付/run 语义)
- **Depends on**: none(d501e09 已落地"等待卡删除"语义,本计划是它的收尾:
  canceled 与未决态在回放面同样不留卡)
- **Category**: bug(回放画出从未发生的"Exited plan mode",违反 1:1 replay/live)
- **Planned at**: 2026-10-09

## Why this matters(owner 原话与裁决)

owner 报告(附 TUI 截图):"plan-reviewer subagent 还在跑,实际并没有退出 plan
mode,但是 tui 视图显示了 Exited plan mode 消息,是 bug"。

常设裁决(2026-10-09,global 记忆 `exit-plan-gate-holds-no-card`):
**exit_plan_mode 审批 gate 的等待态在任何 surface 任何情况都不画卡——审批
overlay 就是全部等待;只保留三类 settled 卡:用户 deny 的 "Kept planning"、
失败、批准后的 "Exited plan mode"。** canceled(用户 Esc 取消 / 进程死在
gate 上)不在保留清单里:它的用户可见记录是审批确认行
("✗ You canceled forebrain's request to exit plan mode"),卡片无痕。

web 端"未应答不画卡"已实现(useChatStream.ts:636-643);TUI 的**回放路径**
与 web 的"canceled 成交行"均未对齐,会画出 **"Exited plan mode"**——一个
从未被批准的退出。

## 根因(file:line 证据链)

### 1. 标题表的 else 分支吞掉 canceled

`pkg/tui/render.go:2653-2663`:`exit_plan_mode` 只有 denied/failed 分支,
`isCanceled`(render.go:2183-2185,status=="canceled")落进 else →
**"Exited plan mode"**。

### 2. 生产者 A:canceled **成交行**(主路径,评审补齐)

用户在 gate 上 Esc/取消时,`abortPendingApproval`
(`pkg/tui/chat_turn.go:1146-1155`)调用 `turn.AnswerAbandonedToolCalls`
(`pkg/turn/session.go:437-453`)→ `cancelledToolAnswer`
(`session.go:500-523`)为未应答的 gate 调用**写出成交工具行**:
`ToolDisplay.ToolMetaJSON = {"tool_name":"exit_plan_mode","status":"canceled"}`
(重启后恢复已取消 action 的路径 `pkg/tui/chat_session.go:647` 同归此处)。
回放时该调用有结果行、**不是孤儿**,走 `replayTurnWithReducer`
(`pkg/tui/commands.go:196-218`)→ `replayToolMessage`
(`commands.go:1102-1160`,采用存储 meta)→ FrameTool canceled → 标题表
else → **"Exited plan mode"**。`ToolStepHoldsNoCard` 全仓仅 notify.go 两处
引用,不过滤此路径。

### 3. 生产者 B:孤儿重建

无结果行的 gate 调用(进程死在 gate 上等)被 `orphanToolCalls`
(`commands.go:697-710`,仅豁免 `isSubagentDispatchTool`)+
`replayOrphanToolCallMessage`(`:724-751`,Status="canceled")重建为卡 →
同落 else → 假卡。

### 4. parity 证据

- web 未应答:`frontend/src/composables/useChatStream.ts:636-643` 整行跳过。
- web canceled 成交行:`toolStepFromRow`(`:471-487`)仍画一张 canceled 卡
  (`:611-614`、`:635/:650` 两个画点)——**owner 已拍板本计划内同步改为整行
  丢弃**。
- TUI live:`notify.go:789-804` + `pkg/tool/format.go:2269-2279`
  `ToolStepHoldsNoCard`(running/awaiting approval)——live 无 canceled 生产者,
  已干净。

### 5. 现场证据(状态库,只读取证)

`~/.forebrain/state/forebrain.state.sqlite` 会话 `cli-d5621446-…`:
13:01:14 gate 停靠 → 13:02:19 用户要求 deepseek review → 13:14:10 review
完成自动交付(marker deny,行 108833 带 drop key,回放整行丢弃 ✓)→
13:18:35 后续 gate 获批真实退出(行 108859)。今日两次 review 会话都跑在
pre-d501e09 的 npm vendor 二进制(vendor 14:07 才重建),旧二进制的"等待卡
悬挂"d501e09 已修;本计划修 HEAD 上仍可达的 canceled/未决回放洞。

## 修复设计

一条共享语义收口:**exit-plan gate 只画三类 settled 卡(deny/失败/真退出);
canceled 与未决一律不画,审批确认行照画、落点保持 live 语义
(InsertBeforeLastTool 不动)。** 谓词扩到 canceled 后成为完整规则,TUI 两个
投影口与 web 的成交行画点共用它。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 刻意修订 |
|---|---|---|---|
| Esc 取消 gate 后回放(TUI) | 成交 canceled 行 → 假 "Exited plan mode" | 不画卡;"✗ You canceled…" 确认行照画、落点不变 | ✓ |
| 进程死在 gate 上后回放(TUI) | 孤儿 canceled 卡 → 假 "Exited plan mode" | 不画卡;重启后审批恢复 overlay 重提示该 gate | ✓ |
| 用户 deny 后回放 | "Kept planning"+反馈 | 不变 | |
| 批准后回放 | "Exited plan mode"+批准文案 | 不变 | |
| 失败后回放 | "Failed to exit plan mode" | 不变 | |
| web:canceled exit 成交行 | canceled 卡(措辞非 "Exited plan mode") | 整行丢弃;确认行照画(owner 2026-10-09 拍板) | ✓ |
| 非 exit 工具的孤儿/成交 canceled 卡 | canceled 卡 | 不变(只豁免 exit gate) | |
| live:等待/review 运行期间 | 无卡(d501e09 已修) | 不变 | |

被弃 gate 不留只读标记:确认行即记录(既有"删掉的语义无痕"裁决)。

## Scope

**In scope**
- `pkg/tool/format.go` — `ToolStepHoldsNoCard`(:2269-2279)status 分支扩为
  `running / awaiting approval / canceled`,doc 注释写成完整规则。
- `pkg/tui/commands.go` — ①`replayToolMessage`:在既有 delivered-review 整行
  丢弃(:1132-1134)旁,加同型丢弃——解析后的 (toolName, meta.Status) 命中
  `tool.ToolStepHoldsNoCard` → 返回 ok=false(生产者 A);②`orphanToolCalls`
  (:697-710)豁免 `tool.ToolStepHoldsNoCard(call.name, "canceled")` 为真的
  调用(生产者 B)。
- `frontend/src/composables/useChatStream.ts` — 新增 `isCanceledExitRow`
  (镜像 `isPlanReviewDeliveredRow` :256,读 tool_display 的 tool_meta_json
  status=="canceled" 且 tool_name 为 exit_plan_mode),在 `:611` 与 `:635`
  两个画点与 delivered-review 判定并列整行丢弃;`stepHoldsNoCard`
  (:466-467)正向清单补 "canceled"(与 Go 谓词同义)。
- 测试:改写 `pkg/tui/commands_test.go:2136-2197`;新增成交行丢弃用例;
  扩展 `pkg/tool/format_test.go:2475` `TestToolStepHoldsNoCard`;
  frontend `useChatStream.test.ts`(已有 exit gate fixture :1980-2038)补
  canceled 成交行丢弃用例。

**Out of scope(明确不碰)**
- `pkg/tui/render.go` 标题表——过滤后 canceled exit 帧不可达;else 分支只服务
  completed(真退出文案),不为不可达状态加分支。
- **任何 `InsertBeforeLastTool` 落点语义**(`render.go:749-756`、
  `commands.go:644-652`、reducer.go:841-845、web `upsertApprovalBlock`
  :862-871):live 对该 gate 本就不画卡、确认行本就落在上一个工具卡之前,
  回放保持同一语义,不动。
- `pkg/turn` 审批/交付/run 语义(`AnswerAbandonedToolCalls` 本身、
  `DeliverPlanReview`、marker deny、resume)。
- gateway 事件面与 Go 侧之外的投影。
- 工作区里与本修复无关的未提交改动(pkg/gateway/dist 删除、docs/plan 旧文档、
  `.forebrain/skills/tui-input-freeze-diagnosis` 删除等)——不还原、不整理。
- 预期 Go 侧无 import 增删 → 不动 `pkg/architecture/testdata/graph.json`;
  若必须改 import,按 STOP 3 处理。

## Steps(每步带验证)

**Step 0** 落盘计划到 `docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md`,并
`git rev-parse --short HEAD` 记录实施基线。

**Step 1** `pkg/tool/format.go`:`ToolStepHoldsNoCard` 的 status 分支扩为
`case "running", "awaiting approval", "canceled"`,doc 注释改为完整规则
("waiting, abandoned and canceled hold no card; only the settled answers
draw: the user's denial, a failure, the exit itself")。

验证:`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestToolStepHoldsNoCard -count=1`
——先扩展既有用例(format_test.go:2475):正向清单加 `"canceled"`;反向清单
保持 denied/completed/failed(+shell/空名)不变。

**Step 2** `pkg/tui/commands.go`:
1. `replayToolMessage`:在 delivered-review 整行丢弃(:1132-1134)之后加:
   `if tool.ToolStepHoldsNoCard(toolName, meta.Status) { return Message{}, "", false }`
   (此时 toolName/meta 已完成存储显示与回退解析,谓词吃解析后的值)。
2. `orphanToolCalls`:`!tool.ToolStepHoldsNoCard(call.name, "canceled")` 加入
   豁免条件(与 `isSubagentDispatchTool` 并列),同步更新函数 doc 注释。

验证:`CGO_ENABLED=1 go build ./...`。

**Step 3** TUI 测试(改写 + 新增,放 `pkg/tui/commands_test.go`):
1. **改写** `TestResumeRebuildsOneCanceledCardPerOrphanToolCall`(2136-2197)
   → `TestReplayDrawsNoCardForCanceledExitGate`:同一构造(assistant 行带
   `exit_plan_mode` call、无 tool 行 + cancelled 决策事件),新契约:replay 后
   **不存在任何 FrameTool**;唯一记录是该决策 FrameStatus(含 "You canceled"、
   行数帽 approvalConfirmationMaxLines、AgentID 为空)。旧断言(卡存在、
   canceled 状态、行在卡上方)随旧语义整体删除,不写"断言不再出现"的反向用例。
2. **新增成交行用例** `TestReplayDropsCanceledExitGateSettledRow`:assistant
   行 + tool 行(tool_display:`{"tool_name":"exit_plan_mode","status":"canceled"}`)
   → replay 后无 FrameTool。对照:同结构 denied 行仍画 "Kept planning" 卡。
3. **对照用例**:非 exit 工具(如 shell)孤儿仍画一张 canceled 卡(过滤器
   误伤会红);approved exit 行仍画 "Exited plan mode" 卡。

验证:`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿。

**Step 4** web 同步(frontend/):
1. `useChatStream.ts`:新增 `isCanceledExitRow`(读 row 的 tool_display
   meta,判定同 Step 1 谓词语义);在 `:611` 与 `:635` 两个画点与
   `isPlanReviewDeliveredRow` 并列 `continue`;`stepHoldsNoCard`(:466)
   正向清单补 `'canceled'`。
2. `useChatStream.test.ts`(fixture 参考 :1980-2038):新增"canceled exit
   成交行 → 无工具卡、确认行照画";对照 denied 成交行仍出卡。

验证:`cd frontend && pnpm test`(全绿)。

**Step 5** 包级回归 + CI 同款:

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1
go vet ./...
gofmt -l pkg cmd
cd frontend && pnpm test && pnpm build
```

**Step 6** tmux 真机验收(构建必须 `-tags fts5`;临时 `FOREBRAIN_HOME`+临时
项目目录,不污染真实 `~/.forebrain*`):

```bash
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
```

- 场景 A(复现→修复,生产者 B):驱动 plan 会话到 gate 停靠,**直接杀进程**
  (不经取消路径)→ `forebrain resume <sid>` → capture-pane 断言:无
  "Exited plan mode" 卡;审批恢复 overlay 重提示该 gate。
- 场景 B(复现→修复,生产者 A):gate 停靠时按 Esc 取消 → 确认行
  "✗ You canceled forebrain's request to exit plan mode" → 重启回放 →
  断言:无 exit 卡,确认行仍在。此场景同时坐实评审未能从库中验证的
  canceled 成交行落盘形态(实施时顺带留存该行 parts 取证到验收记录)。
- 场景 C(回归/deny):deny 带反馈 → "Kept planning"+反馈;重启回放一致。
- 场景 D(回归/approve):批准 → 真实退出 "Exited plan mode"+批准文案;
  重启回放一致。
- 场景 E(web):`forebrain gateway start` 起网关,同一会话的 web 历史时间线
  无 canceled exit 卡、确认行照画(deny/approve 行不受影响)。

验收后 `tmux kill-session`、清理临时目录;计划文件回填"验收记录"章节
(场景→证据,含工具链坑)。

## Done criteria(机器可查)

1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/tool -count=1` 全绿,含
   Step 3 改写与新增用例;`cd frontend && pnpm test` 全绿,含 Step 4 用例。
2. `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`、
   `go vet ./...`、`gofmt -l pkg cmd`(空输出)全绿;`pnpm build` 成功。
3. tmux 场景 A–E capture 取证满足各自断言;场景 B 留存 canceled 成交行
   parts 取证。
4. `git status` 中本修复改动仅:`pkg/tool/format.go`、`pkg/tui/commands.go`、
   `frontend/src/composables/useChatStream.ts`、两至三个测试文件、
   `docs/plan/EXIT_PLAN_GATE_GHOST_CARD_PLAN.md`;改动留工作区不 commit。

## STOP conditions

1. Step 2 的过滤若波及**其他**审批种类的回放(request_permissions /
   user_interaction 的 canceled 行/确认行锚定发生变化)→ 停下报告,豁免面
   只许 exit gate,不得顺手扩大。
2. 若回放中确认行/review 卡的落点因卡片丢弃出现顺序颠倒(确认行跑到
   assistant 行之前)→ 停下报告实测定位,不即兴调顺序。
3. 若实现中必须增删 pkg 导入(架构图会变)→ 先停下说明原因。
4. 真机场景 A 若审批恢复 overlay 未重提示恢复的 gate(恢复链路另有缺陷)→
   记录证据停下,那是另一个 bug,不在本计划内顺手改。

## Maintenance notes

- `ToolStepHoldsNoCard` 是 exit-gate 卡规则的唯一定义(TUI live 漏斗 + TUI
  回放两个投影口共用);web 对应物为 `isExitPlanGate`/`stepHoldsNoCard`/
  `isCanceledExitRow`(useChatStream.ts)。改语义三处同改,doc 注释即契约。
- render.go 标题表 else 分支现在只服务 completed;未来新增 exit_plan_mode
  帧状态时,先扩谓词与投影过滤,再考虑标题。
- 若未来给"被弃 gate"设计可见记录(如只读标记),做成独立语义并三面同步,
  不恢复卡片。
- 回放 1:1 判据:live 没画过的帧,回放不得发明——本修复是该原则在 exit gate
  上的落实。

## 验收记录(2026-10-09,实施基线 d501e09,改动留工作区未提交)

### Step 0–2 落地

- `pkg/tool/format.go`:`ToolStepHoldsNoCard` status 分支扩为
  `running / awaiting approval / canceled`,doc 注释写成完整规则。
- `pkg/tui/commands.go`:`replayToolMessage` 在 delivered-review 整行丢弃之后
  加 `tool.ToolStepHoldsNoCard(toolName, meta.Status)` 同型整行丢弃(谓词吃
  stored display 解析后的 meta);`orphanToolCalls` 豁免条件并列
  `tool.ToolStepHoldsNoCard(call.name, "canceled")`,函数 doc 同步。
- 无 import 增删(STOP 3 未触发),`pkg/architecture` 随全量测试通过。

### 测试

- `pkg/tool/format_test.go`:`TestToolStepHoldsNoCard` 正向清单加 "canceled"。
- `pkg/tui/commands_test.go`:按评审②改写——`TestResumeRebuildsOneCanceledCard
  PerOrphanToolCall` → `TestReplayDrawsNoCardForCanceledExitGate`(孤儿 exit:
  无任何 FrameTool,唯一记录是决策 FrameStatus);新增
  `TestReplayDropsCanceledExitGateSettledRow`(成交行 canceled→无卡,denied→
  "Kept planning"、completed→"Exited plan mode" 对照);新增
  `TestResumeRebuildsCanceledCardForNonGateOrphan`(shell 孤儿仍一张 canceled
  卡,防误伤)。同族既有测试一并按新契约改写:
  `TestResumeRebuildsOneCanceledCardForADuplicatedOrphanCall`(主旨是孤儿去重,
  fixture 换 shell)、`TestCanceledGateAndConfirmationSurviveAReopenedStore` →
  `TestCanceledGateConfirmationSurvivesAReopenedStore`(真库端到端:cards==0 &&
  lines==1)。
- `frontend/src/composables/useChatStream.ts`:新增 `isCanceledExitRow`;
  两个画点(tool 行分支与 calls 循环)与 delivered-review 判定并列整行丢弃;
  `stepHoldsNoCard` 正向清单补 `'canceled'`(并 toLowerCase 对齐 Go 谓词)。
- `frontend/src/composables/useChatStream.test.ts`:新增集成用例"canceled exit
  成交行 → 无工具卡、确认行照画(blocks=['assistant','approval'])"与对照
  "denied 成交行仍出卡"。

### 验证命令与结果

- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`:唯一
  失败 `TestGatewayIdempotentApproveStillCancelsAnInFlightPlanReview`,原因是
  `TempDir RemoveAll cleanup: directory not empty`(macOS unlinkat 清理竞态),
  非 断言失败;单独 -count=3 复跑全绿,确认为 flake,与本次改动无关(pkg/gateway
  无修改)。
- `go vet ./...` 干净;`gofmt -l pkg cmd` 空输出。
- `cd frontend && pnpm test` 315 tests 全绿(含新增用例);`pnpm build` 成功。

### tmux 真机验收(驱动:`.claude/skills/run-forebrain/driver.sh`,mode=toolcall,
临时 FOREBRAIN_HOME,不碰真实 `~/.forebrain*`;验收二进制
`go build -tags fts5` → `./build/bin/forebrain`)

- **场景 A(生产者 B:gate 上杀进程)**:enter/exit 两道 gate 依序出现(toolcall
  模式下 enter_plan_mode 也被门控,先 `y` 批准);exit gate 停靠时
  `tmux kill-session` 直接杀进程 → `forebrain resume <sid>` 回放:
  `grep -c "Exited plan mode"` = **0**(修复前此处是假卡);屏幕保留
  "● calling exit_plan_mode"(transcript 自带 assistant 行,live/replay 1:1,
  非卡)。**注意**:审批恢复 overlay 不在打开时自动弹出,而是 run 保持
  `waiting_action`,用户在恢复会话里下一次提交时由 dispatch 循环的
  `surfaceNeedsToolApproval` 重提示该 gate(实测 overlay 完整重现
  "Tool: exit_plan_mode" 与四个选项)——非 STOP 4 缺陷,恢复链路按此设计工作。
- **场景 B(生产者 A:Esc 取消)**:exit gate 上 `Escape` → 确认行
  "✗ You canceled forebrain's request to exit plan mode" 落屏;canceled 成交行
  落盘形态取证(fb_messages id=7 的 parts):
  `[{"text":"The user canceled this call before it ran.","type":"text"},
  {"tool_call_id":"call_fake_1","type":"tool_result_meta"},
  {"body":"","summary":"running exit_plan_mode","tool_meta_json":"{\"tool_name\":
  \"exit_plan_mode\",\"status\":\"canceled\",\"purpose\":\"Inspect or update
  session planning context.\",\"invocation\":\"exit_plan_mode\"}","type":
  "tool_display"}]` —— 评审⑤未能从真实库验证的路径由此坐实,与谓词匹配形态
  完全一致。重启回放:`Exited plan mode` 计数 **0**,确认行照画。
- **场景 C(deny 带反馈)**:gate 上 `j j` → Enter 进反馈编辑 → 输入
  "先拆分里程碑再动手" → Enter;live 画 "◆ Kept planning / └ 先拆分里程碑再动手";
  重启回放一致。
- **场景 D(approve)**:gate 上 `y`;live 画 "✔ You approved forebrain to exit
  plan mode" + "◆ Exited plan mode <0.1s"(含批准文案);重启回放一致。
- **场景 E(web)**:同一 FOREBRAIN_HOME 起 `gateway start`(token 需以
  `${FOREBRAIN_GATEWAY_TOKEN}` 引用写入 forebrain.yaml 并 export 环境变量,
  明文密钥是硬启动失败;token 链接按设计只打 TTY 不落盘)。API 实测
  `/api/chat/sessions/<sid>/messages` 返回 canceled 成交行(call_fake_1)、
  `/events` 返回 `approval_resolved(decision=cancelled, confirmation="✗ You
  canceled forebrain's request to exit plan mode")`;再把该会话**真实载荷**喂给
  真实前端投影(临时 vitest,跑后即删):canceled exit 行不画卡、
  enter_plan_mode 卡不受影响。web 画图规则本身由 315 测试套件(含新增两条
  集成用例)钉死。

### 工具链坑

- `pnpm build` 会重新生成 `pkg/gateway/dist`(构建产物);该目录在本修复开始前
  就有未提交的删除(计划"不碰"清单),验收构建使其出现新 hash 的未跟踪产物与
  `index.html` 修改——属计划强制的 Step 5 构建的副产物,非源码改动。
- 工作区另有一组与本修复无关的既有未提交改动(pkg/tui 的 asyncPaint/paint_writer
  移除等),本计划未触碰;全部测试是在含这些改动的工作区上跑绿的。
