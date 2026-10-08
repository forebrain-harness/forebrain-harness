# Plan: 修复 subagent 视图 footer 上下文仪表被"100%"覆盖（模型无关）

> **Owner 裁决的执行顺序（2026-10-08）**：第一轮退出计划模式**只做真机验证、不改任何源码**——
> ~~用 glm-5.3（与 GPT relay 若可达）真机复现"实时仪表被打开视图时的播种覆盖回 100%"~~
> ✅ **取证已完成**（2026-10-08，证据链见文末 Implementation Status）：glm-5.3 真机复现成功——
> live 事件 98% 已写入 → 14:02:41.3 打开子视图 → footer 播种覆盖为 `100%/1M` 持续约 8 秒（cap00–cap18）→
> 下一条实时事件 97% 到达才纠正（cap19 起 `97%/1M`）。与 §2.4 GPT 插桩 trace 同构，"模型无关"成立。
> GPT relay 网络可达但 `OPENAI_API_KEY` 本会话不可得 → GPT 侧以 §2.4 trace 为证据（§6 场景 B 注明）。
> **本次提交 = 定稿送审（实施阶段）**：执行 §3 修复 + §3.3 测试 + §5 验证 + §6 修复后复验。

> **Executor instructions**: 逐条执行；每步先跑验证命令并确认预期结果再前进。
> 触发任何 STOP condition 时停下汇报，不要即兴发挥。实施完成不 commit，改动留工作区。
> 批准后第一步先把本计划复制为仓库惯例路径 `docs/plan/SUBAGENT_FOOTER_BUDGET_STOMP_PLAN.md`，收口时在该文件回填验收记录。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（单点条件守卫 + 两个回归测试；不动事件链与持久化）
- **Depends on**: none
- **Category**: bug
- **Planned at**: 工作区 @ `1fd93a1`（2026-10-08；实施前以本文件 file:line 对照做 drift check，不依赖 SHA diff）

## 1. 目标（Why this matters）

owner 报告：TUI subagent 视图 footer 右侧上下文统计在 GPT 模型（gpt-5.6-sol，openai 兼容 relay）下"一直是 100%"。经真机 + 插桩定位：**实时仪表值被视图打开时的"播种"无条件覆盖回 100%**。该缺陷模型无关——只要 subagent 执行还在进行中，播种读到的持久化占用恒为 0。修复后：任何模型下，视图打开时保留已到达的实时仪表值，只在视图尚无仪表时才播种。

owner 追加要求：用 glm-5.3 真机验证同一缺陷存在（见 §6 验收场景 A），修复后同场景复验通过。

## 2. 根因（file:line 证据链 + 真机证据）

### 2.1 实时链路（正常）

- subagent 每次模型响应：`pkg/run/subagent.go:914-921` `OnUsageSnapshot` → `ContextBudget(own, sessionID, &record, inputTokens+outputTokens)`（用**本次响应绝对占用**，不依赖 DB）→ `publishEvent(..., event.CompactEventBudgetUpdated, payload)`，payload 带 roster key。
- TUI 漏斗：`pkg/tui/notify.go:858-866` 转 `TokenBudgetUpdatedMsg` → 主循环 `pkg/tui/run.go:292`（Reduce）→ `pkg/tui/reducer.go:1399-1403` 产出 `EventResult{ComposerTokenStats, ComposerTokenStatsAgent}` → `pkg/tui/run.go:367-379` `renderer.SetComposerTokenStats(view, stats)`。
- footer 渲染读 renderer 按 view 键存的 map：`pkg/tui/reducer.go:3364-3367`、`pkg/tui/render.go:4194-4204`。

### 2.2 覆盖点（bug 本体）

`pkg/tui/run.go:2126-2152` `syncComposerToView`：视图切换时

```go
if view != "" {
    // Each subagent's view shows that subagent's own context window; the
    // engine computes it from the subagent's worker session and model.
    renderer.SetComposerTokenStats(view, s.session.SubagentComposerTokenStats(s.sessionID, view))
}
```

`SubagentComposerTokenStats`（`pkg/tui/chat_session.go:1205-1213`）→ `run.SubagentContextBudget`（`pkg/run/subagent.go:2111-2122`）→ `ContextOccupancy(ctx, store, record.WorkerSessionID)`（`pkg/run/config.go:1822-1831`）= `state.TokenCountFromLastAPIResponse(turns)`，读 **worker 会话已持久化的 usage_json 行**。

### 2.3 为什么执行中恒为 100%

subagent worker 会话的带 usage 写入只发生在执行收尾（`pkg/turn/session.go:362-400` `appendAssistantRows` 挂在 turn 收尾的 `PersistAssistantTurn`）；执行中途的写入点 `pkg/run/subagent.go:1113`、`2387` 固定传 `""`（仅初始消息）。因此执行进行中 `ContextOccupancy=0` → `ContextBudget(..., 0)` → `PercentLeft=100`（`pkg/state/usage.go:242`，threshold-0/threshold=100%）。

### 2.4 真机证据（GPT，隔离 FOREBRAIN_HOME，插桩二进制）

插桩 trace（时序逐行）：

```
[dbg] apply stats view="subagent-fa30..." percent=99 window=1050000   ← 实时事件已写入 map
[dbg] footer read view="subagent-fa30..." percent=99
[dbg] seed view="subagent-fa30..." percent=100                          ← 打开视图，播种覆盖
[dbg] footer read view="subagent-fa30..." percent=100                   ← 此后 footer 显示 100%
```

DB 侧同会话确有 `token_budget_updated`（agent=subagent-fa30…, percent=99, token_usage=7906）。owner 截图会话（cli-514b225d，gpt-5.6-sol）DB 中 22 条同类事件（97%→72%），而截屏时刻 footer 仍 100%——事件间隔分钟级（xhigh 推理 + relay 慢），覆盖后的恢复窗口长，故"一直是 100%"。

### 2.5 模型无关性论证 + glm 侧证据

- 覆盖点代码不含任何 provider/model 分支；播种 100% 的充分条件是"执行进行中"，对所有模型成立。
- glm 事件频率高（每响应一条、秒级），被覆盖后很快被下一条实时事件纠正，历史上（MEMORY：plan-reviewer "65%/1M"）看似正常——症状窗口短不等于无缺陷。**已真机实证（2026-10-08，glm-5.3 effort low）**：复现窗口约 8 秒（98% 被播种覆盖回 100%，下一事件 97% 才纠正），取证细节见文末 Implementation Status；修复后同流程复验见 §6 场景 A'。
- GPT usage 解析本身无缺陷：`openAIUsage`（`pkg/llm/openai/responses_llm.go:303-321`）对 cached 拆桶正确；本 bug 与 usage 字段取值无关（owner 曾怀疑的方向已证伪）。

## 3. 修复设计

### 3.1 生产改动（唯一一处）

`pkg/tui/run.go` `syncComposerToView` 的播种加"视图尚无仪表"守卫：

```go
if view != "" {
    // A live per-response gauge may have arrived before the user opened
    // this view; the persisted occupancy the seed reads stays empty until
    // the execution finishes appending its rows, so seeding over a live
    // gauge repaints a mid-flight view as a fresh "100%". Seed only when
    // the view has no gauge yet.
    if !renderer.ComposerTokenStats(view).Active {
        renderer.SetComposerTokenStats(view, s.session.SubagentComposerTokenStats(s.sessionID, view))
    }
}
```

语义：`Active` 的实时仪表（`composerTokenStatsFromBudget`，`pkg/tui/reducer.go:167-173`：TokenUsage>0 || ContextWindow>0）永远比滞后播种新，保留；视图无仪表（本进程首次打开、或纯历史 subagent 视图）时照旧播种。恢复语义不变：下一响应的实时事件、执行收尾的 `publishSubagentContextBudget`（`pkg/run/subagent.go:2154-2171`）都会继续刷新。

### 3.2 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| 执行中打开 subagent 视图，实时仪表已到达（任意模型） | 播种 100% 覆盖实时值，直到下一条事件才恢复 | 保留实时值 | 刻意修订（本 bug） |
| 执行中打开视图，实时仪表未到达 | 播种 100%（空上下文满窗） | 同左 | 不变；与主会话新会话首帧 100% 语义一致 |
| 打开已结束/历史 subagent 视图（本进程无仪表） | 播种持久化占用 | 同左 | 不变 |
| Esc 回主视图再进同一 subagent 视图 | 再次播种（覆盖已有值） | 已有 Active 仪表则保留 | 刻意修订；同上覆盖缺陷 |
| 主视图 footer、`RefreshComposerTokenUsage`、`refreshSessionFooter` | 不变 | 不变 | 均只作用于 view "" |
| compaction 后仪表 | `context_compacted` 分支刷新 | 同左 | 不变 |

### 3.3 回归测试（`pkg/tui/run_test.go`，紧邻 `TestDraftsBelongToTheirViews` :5169 的写法）

1. `TestOpeningASubagentViewKeepsTheLiveBudgetItAlreadyReceived`：`fakeSession{subagentTokenStats: {Active:true, PercentLeft:100, ContextWindow:1_050_000}}`（模拟滞后的播种源），renderer 预先 `SetComposerTokenStats("task-b", {Active:true, PercentLeft:72, ContextWindow:1_050_000})`（模拟已到达实时事件）；`SetActiveView("task-b")` + `syncComposerToView` 后断言 `renderer.ComposerTokenStats("task-b").PercentLeft == 72`。失败模式：修复前为 100。
2. `TestOpeningASubagentViewWithoutALiveBudgetSeedsPersistedStats`：`fakeSession{subagentTokenStats: {Active:true, PercentLeft:40, ContextWindow:128_000}}`，不预置 map；同步后断言 map 得到 40/128k。

fakeSession 已有可配置 `subagentTokenStats`（`pkg/tui/run_test.go:1184-1188`）。

## 4. Scope

**In scope**
- `pkg/tui/run.go`：`syncComposerToView` 播种守卫（§3.1）。
- `pkg/tui/run_test.go`：上述两个测试。
- `docs/plan/SUBAGENT_FOOTER_BUDGET_STOMP_PLAN.md`：本计划入库版 + 验收回填。

**Out of scope（明确不碰）**
- `pkg/run/subagent.go`、`pkg/run/config.go`、`pkg/state/usage.go`：事件生产与占用计算语义正确。
- `pkg/llm/openai/*`：GPT usage 解析已证无误。
- web/gateway 前端：无对应视图打开播种路径（web 靠 WS 事件流，无此覆盖点）；如 owner 要求 parity 复核另立计划。
- `initialComposerTokenStats` / 会话切换重置路径（`run.go:1764` 作用 view ""，不涉 subagent）。

## 5. Steps（每步带验证）

1. 复制本计划到 `docs/plan/SUBAGENT_FOOTER_BUDGET_STOMP_PLAN.md`。验证：文件存在。
2. §3.1 守卫。验证：`CGO_ENABLED=1 go build ./pkg/tui` 无输出。
3. §3.3 两个测试。验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestOpeningASubagentView' -count=1` → ok；且把守卫临时还原后测试 1 变红（反证后恢复守卫）。
4. 包级回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok；`go vet ./...` → 无新增告警。
5. §6 真机验收（构建必须 `-tags fts5`）。

## 6. 真机验收（实施后执行；修复前取证已完成）

- **场景 A（glm-5.3 修复前复现取证）：✅ 已完成（2026-10-08）**——隔离 home + glm-5.3 effort low + watcher 轮询 `fb_session_events` 首条 subagent `token_budget_updated`（percent_left<100）即 `Down/Down/Enter` 开子视图、0.4s/帧截 footer：live 98% → 开视图后 cap00–cap18 恒 `100%/1M`（约 8s）→ 下一事件 97% 后 cap19 起 `97%/1M`。证据：`/tmp/fbstomp/{watch.log,caps/}`+隔离 DB（暂存，可清除）。
- **场景 A'（glm-5.3 修复后复验）**：同一 watcher 流程复跑；预期打开视图后 footer 立即显示实时 percent（98%），无 100% 覆盖窗口。
- **场景 B（GPT 复验）**：relay `api.sbbbbbbbbb.xyz/v1` 网络可达（401/0.45s）但 `OPENAI_API_KEY` 本会话不可得（auth.json null、shell 未导出）→ **以 §2.4 已固定的插桩 trace 作为 GPT 侧证据**；若实施时 owner 提供密钥则同流程补跑。
- **场景 C（回归）**：主视图 footer 100%→99% 正常；执行结束的 subagent 视图打开显示最终占用（播种路径）。
- 隔离 home/临时目录，结束后清理（取证目录 `/tmp/fbstomp` 过目后 `rm -rf`）；不污染真实 `~/.forebrain*`。

## 7. Done criteria（机器可查）

1. `grep -n "ComposerTokenStats(view).Active" pkg/tui/run.go` 命中 1 处。
2. 新增两测试绿，且反证（还原守卫）时测试 1 红。
3. `./pkg/tui` 包级 + `go vet` 绿。
4. 场景 A glm-5.3 真机：打开视图 footer = 实时 percent（非 100%）。
5. `git status` 改动面 = §4 In scope，未 commit。

## 8. STOP conditions

- 守卫导致任何既有 `./pkg/tui` 测试红且两次合理修复无效。
- 发现第二个覆盖 `composerTokens[view]` 的写点（当前 grep 仅 `SetComposerTokenStats` 一族）→ 停下汇报，不得顺手改。
- glm/GPT 真机因网络长期不可达 → 以 harness（fake provider，`FAKE_SUBAGENT_STALL` 时序法，已验证可复现该流程）代替并明确记录，不阻塞收口。
- 场景 A 修复后仍 100% → 根因链有缺口，停止并重新取证。

## 9. Maintenance notes

- 将来若给"重新播种"增加新鲜度语义（比较时间戳），以实时事件为准；持久化占用只用于无仪表视图的首播。
- 复审关注：`ComposerTokenStats(view).Active` 判空是否被 `RefreshComposerTokenUsage` 的 in/out 更新干扰（不会：该路径保留 Active）。
- 遗留（明确不做）：worker 会话执行中增量持久化 usage 行（`PartialSessionCapture` 落库时机）是另一主题，如需另立计划。

## Implementation Status

**2026-10-08 实施完成（§3 修复 + §3.3 测试 + §5 验证 + §6 复验），改动未 commit，留工作区待 owner 审阅。**

### 已落地（§5 全步骤）

1. 计划入库：本文件已复制为 `docs/plan/SUBAGENT_FOOTER_BUDGET_STOMP_PLAN.md`（本节同步回填两份）。
2. §3.1 守卫：`pkg/tui/run.go` `syncComposerToView` 播种加 `!renderer.ComposerTokenStats(view).Active`。实施前 drift check：计划全部 file:line 锚点与工作区一致，无漂移。验证：`CGO_ENABLED=1 go build ./pkg/tui` 无输出。
3. §3.3 两测试（`pkg/tui/run_test.go`，紧邻 `TestDraftsBelongToTheirViews`）：`TestOpeningASubagentViewKeepsTheLiveBudgetItAlreadyReceived`、`TestOpeningASubagentViewWithoutALiveBudgetSeedsPersistedStats`。`go test -tags fts5 -run 'TestOpeningASubagentView'` ok；反证：临时还原守卫后测试 1 红（`subagent gauge = 100%, want the live 72%`），恢复守卫后绿。
4. 包级回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok（41.7s）；`go vet ./...` 无新增告警。

### §6 修复后真机验收（glm-5.3，隔离 FOREBRAIN_HOME，二进制 `go build -tags fts5`）

- **场景 A'：通过**。同一任务（subagent_run plan 读 3 个 tui 文件）。事件时间线（隔离 DB）：seq23 @14:15:37 `percent_left=98`、seq87 @14:15:44 `percent_left=97`。subagent 审批浮现自动切入其视图：footer 即时 `97%/1M`（非 100%）；Esc 回主视图、subagent 执行中（roster "x to stop"）重进视图，8×0.4s 连续帧 footer 全部 `97%/1M`，零 100% 覆盖窗口。对照修复前同流程：cap00–cap18 恒 `100%/1M` 约 8 秒。
- **场景 B（GPT）**：维持计划裁决——以 §2.4 已固定插桩 trace 为 GPT 侧证据（本会话密钥不可得）。
- **场景 C1：通过**——主视图 footer 100%→97%/1M 正常（守卫只作用 view≠""）。
- **场景 C2（新进程打开已结束 subagent 视图，播种路径）**：真机入口不可达——resume 后 roster 不渲染（roster 键盘聚焦被 `agentRosterHasRunningSubagent` 门控，`run.go:1825`），卡片行 SGR 点击注入无响应；owner 裁决（2026-10-08）：roster 行点击本就不是功能、无此需求、非缺陷，不追。播种分支由单测 2 确定性覆盖（绿）。

### 偏差与过程记录

- A' 首轮作废：归档旧 DB 时误写路径（`state/prefix-db`，真路径 `home/state/`），mv 静默失败，watcher 误触发于旧库事件（seq=189；新会话事件序号 6361+ 佐证旧库残留）。正确归档至 `prefix-archive/` 后复验有效。watcher nohup 会被执行环境回收，复验改手动受控流程，结论不受影响。
- 工作区尚有其他在途未提交改动（gateway/approval/chat_surface 等，非本计划范围、未触碰）；本计划改动面 = `pkg/tui/run.go`（+6/−3，仅守卫）+ `pkg/tui/run_test.go`（+36，仅两测试）+ `docs/plan/SUBAGENT_FOOTER_BUDGET_STOMP_PLAN.md`（新增）。
- 清理：tmux 会话已 kill、`pgrep` 确认无孤儿；真实 `~/.forebrain*` 未触碰；取证目录 `/tmp/fbstomp` 暂留 owner 过目（caps-prefix=修复前帧证据、home=隔离环境、prefix-archive=旧 DB、caps2-invalid=作废轮），确认后可 `rm -rf /tmp/fbstomp`。

### Done criteria 核对

1. `grep -n "ComposerTokenStats(view).Active" pkg/tui/run.go` → 恰 1 处（run.go:2153）✅
2. 新增两测试绿，反证（还原守卫）时测试 1 红 ✅
3. `./pkg/tui` 包级 + `go vet` 绿 ✅
4. 场景 A' 打开视图 footer = 实时 percent（97%，非 100%）✅
5. `git status` 改动面 = §4 In scope，未 commit ✅
