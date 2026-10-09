# SUBAGENT_VIEW_PERF_ROLLBACK:回滚 subagent/主视图全部性能优化(外科手术式)

> 执行者须知:按步骤逐步执行,每步跑验证命令并确认预期结果。本文件即已批准计划的
> 入库副本,实施完成回填验收记录。

## Status

- **Priority**: P1(owner 明确指令)
- **Effort**: S
- **Risk**: LOW(回滚到已交付过的旧状态;依赖检查全绿;评审者已在临时副本实跑:
  build/vet/gofmt/pkg/tui 与 pkg/architecture 测试绿)
- **Depends on**: none
- **Category**: revert
- **Planned at**: HEAD @ `d501e09`(2026-10-09;锚点已按 HEAD 复核,吸收 deepseek 评审
  v1 的全部采纳项)

## 1. 背景(owner 裁决与来源)

- owner 裁决(本轮原始指令):
  `docs/plan/SUBAGENT_VIEW_WHEEL_SCROLL_RUNAWAY_PLAN.md` 及其后所有针对 subagent 视图
  与 primary agent 主视图的性能优化**全部无效**,代码回滚到该计划之前(基线 `6667bb5`,
  2026-10-08)的状态。
- owner 裁决(2026-10-09 本会话结构化提问,owner 选项原文「一并回滚(推荐)」,选项描述
  已明示代价「裸终端饱和重绘时输入可能再次冻结」):`a7151f5`(asyncPaintWriter paint
  解耦)虽提交类型为 `fix(tui)`,实质属性能链一环,**一并回滚**。
- owner 约束(本轮原话):同提交混入的其他 feat 与 bug 修复代码**必须保留**(外科手术式
  回滚)。
- 已接受的后果(owner 已知悉,无需再讨论):
  1. 滚轮 guard 回退 → press+release 双倍、motion 采样 N 倍超滚行为回来;
  2. paint 解耦回退 → 裸终端(iTerm2/Terminal.app)subagent 高频重绘时输入管线可能再次
     冻结(tmux 不复现)。

## 2. 回滚范围(证据链)

基线 `6667bb5` → HEAD `d501e09` 共 13 个提交;性能相关改动集中在 3 个提交:
`b7eda22`(滚轮 guard)、`ac90041`(TUI performance work 部分)、`a7151f5`(paint 解耦)。
其余 10 个提交(plan-review 送达/静默、gateway status 文案、logging hardening、web
前端设计对齐、LSP 修复、docs/skill)全部保留。

### 2.1 整文件恢复到 6667bb5(5 个文件,净 diff 已核实只含回滚目标)

| 文件 | 回滚内容 |
|---|---|
| `pkg/tui/input_events.go` | 删 wheel 分支 10 行 press/motion 白名单 guard(b7eda22) |
| `pkg/tui/tty_session.go` | 删 `liveBlockCount` 字段、`isLiveFrame`、`setFrame`,恢复直接 frame 赋值与 reset(ac90041) |
| `pkg/tui/reducer.go` | 删 `renderViewport` 预分配(恢复 `make([]string, 0, 256)`)、`hasLiveBlockLocked` 恢复 O(N) 遍历(ac90041,2 个 hunk);删 async paint 全部 **7** 个 hunk(a7151f5:EnableViewportMode、DisableViewportMode、Enable/DisableSoftwareCursor、paintViewportLocked×2、writeClipboardOSC52Locked) |
| `pkg/tui/reducer_test.go` | 删 ac90041 追加的 15 个 `Benchmark*`(该文件 6667bb5 后仅 ac90041 动过) |
| `pkg/tui/render_test.go` | 删 ac90041 追加的 `BenchmarkSelfContainedRows`(同上) |

操作:`git restore --source=6667bb5 -- <5 个文件>`(净 diff 已核实中间无 keep-hunk)。

### 2.2 hunk 级手术(3 处,锚点为 HEAD 当前行号)

| 文件 | 操作 |
|---|---|
| `pkg/tui/render.go` | 只删 `Renderer.asyncPaint` 字段及其注释(a7151f5)。**保留** a7151f5 顺手做的 exit_plan_mode 块 gofmt 缩进对齐(回退它会挂 gofmt 门);**保留** b7eda22(URL 死代码删除)、eafaf46(plan-file 行删除)、d501e09(exit 等待卡删除)的改动 |
| `pkg/tui/run.go` | 只删 `handleActiveRunInput` wheel case 里 a7151f5 加的那行 `renderer.ViewportHover(ev.mouseCol, ev.mouseRow)`。净 diff 自 6667bb5 仅此一行,已核实 |
| `pkg/tui/chat_session_test.go` | 删 `TestWheelReleaseAndMotionReportsDoNotScroll` 及其前置注释块(b7eda22 加入)。同文件 plan-review 测试、`TestWebFetchHeaderWrapsLongURLWithoutLoss` 等保留。已核实无连带死代码:`lockedBuffer` 另有 3 处使用、`wheelScrollLines` 仍被 `overlays_test.go` 使用 |

### 2.3 文件删除(2 个)

- `pkg/tui/paint_writer.go`(asyncPaintWriter 及 `writeTTYOrderedLocked`/
  `startAsyncPaintWriterLocked`/`stopAsyncPaintWriterLocked` 唯一定义处)
- `pkg/tui/paint_writer_test.go`

依赖检查(已核实):`asyncPaintWriter`/`asyncPaint`/`setFrame`/`liveBlockCount`/
`isLiveFrame`/`writeTTYOrderedLocked` 只被上述回滚目标文件引用;`dropPaintShadowLocked`
有大量既有调用者,保留;后继提交(含 `d501e09`)零依赖这些符号。

### 2.4 善后

- **四份性能文档加 REVERTED 抬头(不整删)**(owner 拍板 2026-10-09 结构化提问,选项
  「加 REVERTED 抬头」):
  `docs/plan/SUBAGENT_VIEW_WHEEL_SCROLL_RUNAWAY_PLAN.md`、
  `docs/plan/TUI_PERFORMANCE_DEEP_OPTIMIZATION_SUMMARY.md`、
  `docs/plan/TUI_PERFORMANCE_OPTIMIZATION_IMPL.md`、
  `docs/plan/TUI_PERFORMANCE_ACCEPTANCE_REPORT.md`。每份开头插入状态块:本计划所述
  代码已于 2026-10-09 由 SUBAGENT_VIEW_PERF_ROLLBACK 计划整体回滚,文中 COMPLETED/
  验收回填仅作历史记录,代码位置(`liveBlockCount`、`setFrame`、预分配、wheel guard、
  `paint_writer.go`)已不存在。理由:整删违反本仓"plan 文档永久留痕"惯例且会孤儿化
  `b7eda22` 的 Refs;不标注则给后续 agent 留下"未失效的复现配方"。
- **skill 整体删除**(owner 拍板 2026-10-09 结构化提问,自由输入原话「把
  tui-input-freeze-diagnosis 这个skill完整删除」):删除
  `.forebrain/skills/tui-input-freeze-diagnosis/` 整个目录(`SKILL.md` +
  `scripts/slowpty.py`,均 git 跟踪、`35b25ce` 引入;已核实 git 跟踪文件中无外部引用)。
  该 skill 的方法论以已回滚的 `paint_writer.go` 修复为落点,随回滚一并退场。
- `pkg/architecture/testdata/graph.json`:pkg/tui loc 变化 → 重跑
  `scripts/package-graph.sh` 再生成。注意 `TestGraphFixture` 只做 fixture 自检、抓不到
  过期 graph.json,真正的门是 CI 的 `scripts/package-graph.sh`——重生成步骤不可省。
- 工作区已有 ~195 个 `pkg/gateway/dist` 未暂存删除(HEAD 767 个 assets、磁盘 572 个)。
  收口时**不 `git add -A`、不 `make ui`**,按文件路径精确暂存/呈报,避免搅浑 owner 的
  手工审阅 diff。

## 3. Scope

**In scope**:§2.1–§2.4 全部 + 本计划入库(本文件)并回填验收记录(仓库惯例)。

**Out of scope**:

- `pkg/gateway/dist` 的未提交删除(不碰、不修复、不提交);
- `b7eda22`/`ac90041`/`a7151f5` 之外 10 个提交的任何代码;
- 不新增任何"断言旧行为回归"的单元测试(删掉的语义当作从未存在过,不留反断言;§5.3
  的注入验证是真机观察项,不是单测)。

## 4. 执行顺序

1. 复制本计划入库(docs/plan/)。
2. §2.3 删 2 文件 → §2.1 restore 5 文件 → §2.2 三处手术 → §2.4 善后(文档抬头、skill、
   graph.json)。
3. `scripts/package-graph.sh` 重生成 graph.json。
4. 验证(§5)。
5. 改动全部留在工作区,**不 commit**(owner 手动审阅提交)。

## 5. 验证

1. `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`。已知干扰项:
   `TestCharacterizationNetworkApproval`(chat_session_test.go:7914)自述非 hermetic
   (依赖 macOS sandbox-exec + 真联网),网络受限时以 `curl: (28)` 超时失败——失败先
   `-skip TestCharacterizationNetworkApproval` 或单包重跑判定是否环境所致,勿误判为
   回滚引入;pkg/tui 曾观测到一次偶发 FAIL 后三连绿。
2. `go vet ./...`;`gofmt -l pkg cmd`(空)。
3. tmux 真机冒烟(run-forebrain driver,fts5 构建):
   - **正向断言(恢复后的旧行为)**:向 pane 注入 `\e[<64;30;10M`(wheel press)×1,
     视图滚动恰 3 行(`wheelScrollLines=3`);press-only 连续注入总行程精确。
   - 保留功能不回归:主视图与 subagent 视图点击/拖选、exit-plan 审批提示(无等待卡)、
     plan-review 送达路径。

## Implementation Status

**已完成（2026-10-09），改动留在工作区未提交（owner 手动审阅）。**

- §2.1–§2.3 按计划逐条落地；三处手术 diff 逐字核对一致，`run.go` 恢复后与
  `6667bb5` 零 diff；残留符号（`asyncPaint`/`writeTTYOrderedLocked`/
  `liveBlockCount`/`isLiveFrame`/`setFrame`/`paint_writer`）全仓零引用。
- §2.4：四份文档抬头、skill 删除、graph.json 重生成（pkg/tui loc
  `45509→45262`，其余不动）均落地；`pkg/gateway/dist` 既有未暂存删除未触碰。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...`
  绿；`gofmt -l pkg cmd` 空；tmux 真机（run-forebrain driver）量化冒烟：
  1 次 wheel press = 恰 +3 行、4 次 press = +12 行精确累计；release 报告
  （`<64;30;10m`）与 motion 报告（`<96;30;10M`）各再滚 +3 行——guard 移除的
  旧行为实测回来（owner 已接受）。保留功能：Jump-to-bottom 点击跳底、拖选
  SGR A/B（`48;5;24` 选区底色、边界列精确、点击清除）、exit-plan 两级审批
  overlay 无 "Exiting plan mode" 等待卡（d501e09 语义保留）、subagent 视图
  打开/continue 应答/拖选/钳制不崩。plan-review 送达路径由 pkg/tui 单测覆盖
  （fake driver 无对应模式）。
- 顺带观察（非本期引入、未改动）：exit 审批 overlay 悬挂期间其上方有一行
  无时长/无正文的 "◆ Exited plan mode" 卡，回滚前后行为相同，留 owner 裁决。

