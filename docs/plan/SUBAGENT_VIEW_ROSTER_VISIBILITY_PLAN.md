# 修复：已结束（只读）的子代理视图不再显示 agent roster

> 状态：已实现，待 owner 审阅提交（工作区改动，零 commit）
> 基线：`5c6a7d34`（dev）+ 并行会话脏树
> 改动面：`pkg/tui/{reducer.go,render.go,run.go}` + `pkg/tui/run_test.go`；不 commit

---

## 1. 现象

主视图还有**进行中**的子代理时，roster 面板会画出来。此时点击一个**已结束**子代理的消息卡进入它的视图——
roster 仍然画在屏幕上，还对它显示 `Enter to view · x to stop` 提示，但**任何对 roster 行的操作都无效**：
Enter 打不开行、`x` 停不掉行、Down 只是把 ❯ 移到一个永远不会响应的位置，连 Esc 都要按两次才能退出该视图
（第一次被"看不见的 roster 焦点"吃掉）。

owner 裁决的目标行为：**已经结束的子代理视图里不显示 agent roster**。

这不是"少画一个面板"的观感问题：roster 是一个键盘控件，它在只读视图里被画出来却没有任何输入通道，等于给用户
一块死 UI，并且顺带污染 Esc 的语义。

## 2. 复现（确定性；`go test -overlay` 探针，仓库零改动）

修复前同一场景在真机上的输出（下节 A/B 证据）：

```
viewing general-purpose · 198d · esc to return · deepseek/deepseek-v4-flash
                          ↑ A 的视图（A 已结束）
  main
  general-purpose  slow probe          ← B 还在跑，roster 画在只读视图里
```

按一次 Down：

```
❯ main                          ↑/↓ to select · Enter to view
  general-purpose  slow probe
```

第一次 Esc 无效（仍是 `viewing …`），第二次 Esc 才回到主视图。

## 3. 根因

"roster 是否上屏"与"roster 是否可操作"被写成了两个互不相同的判据：

| 事实 | 位置 | 判据 |
|---|---|---|
| 只读视图仍画 roster | `buildReadOnlyComposerBlock`（reducer.go） | `len(cs.OverlayRows)==0 && agentRosterHasRunningSubagent(cs.AgentRoster)` |
| 可编辑视图画 roster | `buildNormalComposerBlock`（reducer.go） | 同上（此处 `ReadOnly==false`，正确） |
| 只读视图丢弃按键 | `input_events.go` reader | `composerReadOnly.Load() && b != 0x1b && b != 0x03` → Enter/`x`/可打印字符全部丢弃 |
| 只读视图仍转发方向键 | `input_events.go` 的 nav gate | gate 里包含 `composerReadOnly`，于是 Down/Up 变成 `overlay_down/up` |
| 状态侧"roster 活跃" | `renderComposerWithState`（run.go） | `agentRosterHasRunningSubagent(state.agentRoster)`——只问"有没有在跑的子代理" |
| roster 导航 gate | `handleOverlayNav`（run.go） | 同上 |

`composerReadOnly` 的语义**已经**在 `renderComposerWithState` 定义好了：

```go
idle := ""
if view := strings.TrimSpace(renderer.ActiveView()); view != "" && !state.subagentViewRunning(view) {
	idle = view
}
composerReadOnly.Store(idle != "")
```

即"当前视图是已结束子代理的视图" ⇔ 该 composer 收不到输入。**唯独 roster 的两条链路没有用这个语义**：
画的时候只问"有没有在跑的子代理"，导航时也只问同一个问题。于是"只读"与"roster 已上屏"不一致。

为什么从**主视图卡片**进入会命中：`openAgentViewAtRowLocked` 让已结束子代理的视图仍然可达（它的 roster 行
已随结束删除，fanout 卡的行还在），进入后 `renderComposerWithState` 把 `idle` 设为该视图 ⇒ `ReadOnly=true`
⇒ 走 `buildReadOnlyComposerBlock` ⇒ 仍画 roster。

## 4. 修复（一个判据，两个消费方）

- `pkg/tui/render.go`：新增唯一判据，紧邻 `agentRosterHasRunningSubagent`：

```go
func agentRosterOperable(snapshot AgentRosterSnapshot, composerTakesInput bool) bool {
	return composerTakesInput && agentRosterHasRunningSubagent(snapshot)
}
```

- `pkg/tui/run.go`：把"本视图只读"抽成 `streamState.composerReadOnlyView(renderer)`（紧邻
  `subagentViewRunning`），`renderComposerWithState` 由它写 `composerReadOnly` 并推出
  `rosterActive := agentRosterOperable(state.agentRoster, idle == "")`；`handleOverlayNav` 的 roster gate
  改用 `agentRosterOperable(s.agentRoster, s.composerReadOnlyView(renderer) == "")`。
- `pkg/tui/reducer.go`：`buildReadOnlyComposerBlock` 删除 `rosterVisible`/`rosterLines` 与末尾追加 roster
  行的分支；`buildNormalComposerBlock` 不改（该分支只在 `ReadOnly==false` 时进入）。

### 4.1 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 主视图有在跑的子代理；进入**已结束**子代理的视图 | roster 在屏上、带提示，Enter/x 全无效，Down 把焦点移到它 | **roster 不显示**；Down 不进 roster；Esc 一次即退出 |
| 同上，按 Esc | 第一次被 roster 焦点吃掉，第二次才退出 | 一次退出 |
| **运行中**子代理的视图（composer 可编辑） | roster 显示且可操作 | **不变** |
| 主视图（对话本身）有在跑的子代理 | roster 显示且可操作 | **不变** |
| 子代理在你正看着它时结束 | roster 行消失，但若另有子代理在跑，roster 仍画、焦点仍可能留在上面 | roster 消失、焦点清掉（只读视图） |
| 没有在跑的子代理 | 不显示 | **不变** |

## 5. Scope

In scope：`pkg/tui/render.go`、`pkg/tui/run.go`、`pkg/tui/reducer.go`、`pkg/tui/run_test.go`。
Out of scope：`pkg/tui/input_events.go`（reader 的只读丢字节/方向键转发是正确实现）、`pkg/gateway/*`、
`pkg/process/*`、上一计划与并行会话留下的脏文件、roster 其它 UX。

## 6. 防线

`pkg/tui/run_test.go` 新增两个用例：

- `TestFinishedSubagentViewDoesNotPaintTheAgentRoster`：只读视图的 composer 块不含 roster 行与
  `agentRosterSubagentHint`；把子代理改回运行后两者都回来。
- `TestFinishedSubagentViewDoesNotFocusTheRoster`：只读视图里 Down 不接管、不设
  `agentRosterFocused`；子代理重新运行后 Down 恢复接管。

## 7. 维护契约

- **只有能接收输入的 composer 才能承载键盘控件**。roster 的上屏判据是
  `agentRosterOperable(rows, composerTakesInput)`；新增任何 roster 绘制点或导航入口都必须走它。
- "本视图只读"的唯一来源是 `streamState.composerReadOnlyView`；`composerReadOnly` 原子量由它写入。
  改动只读判据（例如将来允许空闲子代理接收消息）时，roster 会自动跟随。
- web 侧同规则已审计并加护栏，见 §8。

## 8. web（`pkg/gateway` + `frontend`）同类审计

审计对象：web 子代理表面是否也存在"已结束（无输入通道）的视图仍呈现一个按不动的操作入口"。
逐面判据与结论（`active` = 该次执行仍在进行，来自 `subagentPhase(record.status)`）：

| 面 | 绘制判据（file:line） | 结论 |
|---|---|---|
| 子代理标签页的输入框 | `SubagentTab.vue:63` `v-if="active && !readonly"`；`active` 来自 `:233` `subagentPhase(...) === 'active'` | 正确：结束即换 `subagent-idle-bar`（`:115`），无输入框 |
| 该页的停止按钮 | `SubagentTab.vue:95` `v-if="active"`，且整块在 `:63` 的输入框分支内 | 正确：不会出现在已结束的子代理上 |
| 状态联合类型闭合 | `useChatStream.ts:1543`（`running\|waiting_approval\|waiting_input\|done\|failed\|cancelled\|interrupted`）+ `subagentStatus.ts:14-24`（默认分支=active） | 正确：默认分支只覆盖三种"仍在进行"的状态 |
| roster 面板行操作 | `AgentRosterPanel.vue:59` `v-if="isRunning(row)"`（停止）、`:9` `v-if="hasRunningRows"`（全部停止）、`:35` `:disabled="!row.sessionId"`（打开） | 正确：每个控件都指向该行仍能做的动作 |
| 抽屉 "+" 菜单（= 终端 roster 的对应物） | `SubagentDrawer.vue:168-182`，每行点击 `openFromMenu` | 正确：菜单只在点开后出现，且每行都真的能打开标签页——不存在终端那种"画出来却没有任何输入通道"的形态（web 无键盘 roster 焦点模型） |
| 卡片上的任务行 | `SubagentCallCard.vue:22-32`（有 `agentId` 才是 button，点击打开该 agent 视图） | 正确 |
| 批量执行页的只读视图 | `ChatView.vue:444` 只读条替代 composer；`frontend/src/lib/tasks.ts:35` `status === 'running'` 才给 `stop`；`TaskDetailView.vue:27,125` 同一判据 | 正确（`TaskDetailView.test.ts:160` 已钉住"结束即无 stop"） |
| Esc 语义 | `ChatView.vue:2403-2411`：`interruptSubagentToSend` 用服务端 `interrupted` 回答（`useChatStream.ts:4517-4527`），无在跑执行时返回 false，随后直接收起抽屉 | 正确：不存在终端那次"第一下 Esc 被看不见的 roster 焦点吃掉" |
| 服务端拒绝面 | `pkg/gateway/api_extra.go:2820` `subagent_not_running`（409） | 正确：即便绕过界面直呼，引擎也拒绝给已结束子代理发消息 |

审计结论：**web 侧不存在同类缺陷**——"能否操作"的判据在每一面都取自该次执行自己的状态，而不是
"别处是否还有在跑的子代理"；且 web 的"roster"是点开才出现的菜单，每一行都能真正打开视图，
没有终端那种"画着提示却没有任何按键通道"的死控件。

审计发现的唯一缺口是**护栏**而非行为：`AgentRosterPanel` 的判据此前没有任何测试钉住（`TaskDetailView`
的同类判据有）。已新增 `frontend/src/components/AgentRosterPanel.test.ts` 四条：
运行行才有停止、无运行行时连"全部停止"都不画、全部停止只在有运行行时出现、无 session 的行打开按钮
disabled。并按"护栏必须能失败"验证：把 `v-if="isRunning(row)"` 临时改成 `v-if="records.length > 0"`
后其中两条立即变红（`expected ... to have a length of 1 but got 2` / `of +0 but got 2`），恢复后全绿，
组件文件 sha256 与改动前逐字节一致。

## Implementation Status

已实现并验收（工作区改动，零 commit）。

### 漂移检查

基线 `5c6a7d34`；执行前四个 in-scope 文件 `git hash-object` 与计划锚点逐字节一致
（`44f485e2 render.go` / `8675be8f reducer.go` / `de23aaaa run.go` / `07b1775a run_test.go`），
内容锚点（只读块判据、`composerReadOnly` 写入点、`handleOverlayNav` gate、reader 丢弃分支）全部吻合。

### 单测（先红后绿）

修复前（`go test -overlay` 用 HEAD 的三个产品文件替换当前树，仓库零改动）：

```
--- FAIL: TestFinishedSubagentViewDoesNotFocusTheRoster
    run_test.go:3992: Down must not take the keyboard onto a roster a finished view does not paint
--- FAIL: TestFinishedSubagentViewDoesNotPaintTheAgentRoster
    run_test.go:6453: a finished subagent's view still paints the roster:
      "viewing explore · task · esc to return\n\n❯ general-purpose  network probe … Enter to view · x to stop"
```

修复后：

```
--- PASS: TestFinishedSubagentViewDoesNotFocusTheRoster (0.00s)
--- PASS: TestFinishedSubagentViewDoesNotPaintTheAgentRoster (0.00s)
ok   github.com/forebrain-harness/forebrain-harness/pkg/tui
```

包级回归：`go test -tags fts5 ./pkg/tui -count=1` → `ok`（76.4s）；`go vet ./...` 无输出；
`gofmt -l` 四个文件无输出。六个既有 roster 用例（Enter 换视图 / x 停止 / 光标跟 agent / 点卡进 roster /
运行中视图 / 空闲视图无输入框）全绿。

### 真机 A/B（tmux + 假 provider + 隔离 home，同一 provider/脚本）

夹具：一次 `subagent_fanout` 同时派 A（`network probe`，shell `sleep 8` 后答复结束）与 B（`slow probe`，
每轮 shell 调用持续在跑），因此 A 结束时 B 仍在 roster 上。provider 写在 `$TMPDIR`，不进仓库。

| 断言 | 修复前（HEAD 三文件 `-overlay` 构建） | 修复后（工作树构建） |
|---|---|---|
| 点 A 的已完成行进入其视图 | `viewing general-purpose · 198d`，**roster 行在屏上**：`general-purpose  slow probe` | `viewing general-purpose · d294`，只有 `● probe one finished`；roster 行/提示计数 **0/0** |
| 该视图按 Down | `❯ main  ↑/↓ to select · Enter to view`（键盘移进画出来的 roster） | 无变化（仍 `viewing … · d294`，无 `Enter to view`、无 ❯） |
| 按 Esc | 第 1 次无效（仍 `viewing …`），第 2 次才回主视图 | **1 次**即回主视图（`○ Running 2 general-purpose tasks…`） |
| 运行中 B 的视图 | roster 显示且可操作 | **不变**：`viewing general-purpose · 206a`，可编辑 composer，`❯ general-purpose  slow probe  Enter to view · x to stop` |
| 主视图有 B 在跑 | roster 显示 | **不变**：`main` / `general-purpose  slow probe` |

截屏原文留存在 `/tmp/fb-roster/{after-view,after-down,after-esc,after-running-view}.txt`（临时目录，重启即失，结论以本节表格为准）。

### 偏离与判断

1. 夹具里 B 没有采用计划草稿的"答一次 `shell: sleep 60` 后挂住"：真机上挂住的 SSE 流会被流看门狗判为停滞，
   运行时随后改用**非流式**回退请求，而夹具始终回 SSE，于是解析失败（`invalid character 'd' …`）并被重试。
   改为 B 每轮答复一个 20s 的 shell 调用（`timeout_ms` 抬到 60s），全程没有未完成的响应挂在线路上——同样是
   "B 一直在跑"，但确定性更好。行为面未变。
2. 夹具用 `subagent_fanout` 一次派两个任务，而不是同一轮里的两个 `subagent_run`：两者并发确定，
   且 fanout 卡的每一行都带自己的 agent id，点击哪一行进哪个视图无歧义。
3. 计划 Step 4 要求把计划落到本文件；Done criteria #5 的"只产生 §5 的 4 个文件"因此相应理解为
   4 个源码/测试文件（本文件是计划文档，非源码改动）。

### 未做

- 未 commit、未 push（owner 手动审阅提交）。

### web 审计（原"未做"项，已补完）

见 §8：逐面核对八个绘制点与服务端拒绝面，**未发现同类缺陷**；补了 `AgentRosterPanel` 的四条护栏
（`frontend/src/components/AgentRosterPanel.test.ts`，含"删掉判据即变红"的反证）。

验证：`cd frontend && pnpm typecheck`（`vue-tsc --noEmit`）无输出；`pnpm test` 全量
**168 files / 1296 tests 全绿**（新增的四条在其中）。本次不改任何 web 视觉或模板，故未跑浏览器 e2e
（仓库规则：只跑 diff 能触达的 spec，而 diff 只有新增测试文件）。

