# 修复：`resume` 重放子代理视图丢块（exec/subagent-replay-blocks）

状态：DONE（worktree `$TMPDIR/fb-wt-subagent-replay`，分支 `exec/subagent-replay-blocks`，
基于 dev `202ed4f2`；本地 commit 见文末，未 push、未 merge——等 owner 一句明确指令）
计划原文：session plan `subagent-view-replay-loses-blocks.md`（cli-0ebab9dd）。

## 根因（一段话）

块级流片段按设计永不落库（`pkg/event/run_events.go` `IsPushOnlyStreamEvent`；
`pkg/tui/notify.go` "painted but never stored"），行的内容是每块唯一的持久记录；
而 `replayTimelineWithWorkers` 的 worker-body 消费端却按"每块一个事件"的假设
对位（`startTool` 换 step id 就无条件吞一条 body 消息），持久日志里根本没有这些
事件，于是只有工具边界把 body 放出几条，其余全部丢失，末句靠 `subagent_ended`
的 `Output` 兜底才显得"首尾都在"。修法只动消费端 `pkg/tui/commands.go`：

- `replayWorkerBody` 改为**按行顺序**放出：`advanceToTool(stepID)` 放出
  `messages[cursor..i]`（含该 step 的卡）；`emitted` 登记已画 step，同 step 的
  completed 直接吞；行里没有该 step → `(nil,false)` 落回事件画卡路径。
- `takeRest()` 在该 run **最后一个事件**处收尾（`lastEventByRun` 预扫描）：
  纯文本执行的思考+回答、最后一个工具之后的尾部块全部放出。
- `consume` 返回 `([]any, bool)`；`subagent_ended` 的 `Output` 在"行的末条是
  assistant"时清空，行的记录优先（行缺失时 Output 仍是唯一兜底）。

## 回归测试

`pkg/tui/commands_test.go` `TestReplaySubagentWorkerBodyDrawsEveryBlock`
（真实持久形状：无 `assistant_delta`/`reasoning_delta`/`reasoning_done`）：

| 子用例 | 修复前 | 修复后 |
|---|---|---|
| two-tool-calls-with-tail | 4 块（丢 4 块）FAIL | 8 块按序 PASS |
| text-only | 2 块（思考丢失）FAIL | 3 块 PASS |
| tool-step-without-row | 3 块（step 吞掉行）FAIL | 4 块 PASS |

## 验证记录

- Step 0 修复前红：三例全 FAIL（块数 4/2/3，期望 8/3/4）——失败留痕见上表。
- Step 1：`go test -tags fts5 ./pkg/tui -run TestReplaySubagentWorkerBodyDrawsEveryBlock` PASS；
  全包仅 3 个既有红（`TestOnboardConfiguresLLMWhenMissing`、`TestOnboardEscGoesBackAPage`、
  `TestConfigureOpenCodeGoOffersPerModelEffortLevels`），在**未改动的 dev 主树同样红**
  （并行 config-DB 会话在修，与本 diff 无关）。
- Step 2：`./pkg/gateway` 绿；`./pkg/process` **编译失败**——`202ed4f2` 移除了
  `ProviderAPIKeyConfigReference` 而测试仍引用（dev 上既有，非本 diff）；
  `go vet ./...` 同样只报该处，`./pkg/tui` vet 干净。
- Step 3 全量一次：除上述基线红外，`pkg/migrate` 7 例在 worktree 红/主树绿
  （并行会话提交后仍在演进 config 断言，`202ed4f2` 快照自带的中间态）；
  `cmd/forebrain` 1 例为 worktree `/var` vs `/private/var` 符号链接路径断言；
  `TestBatchShellIsConfinedToTheTaskWorkspace` 为计划已知环境红；
  `TestTheReaperLeavesAnUncertainContinuationToItsRecovery` 单独复跑绿（负载抖动）。
  全部与本 diff（仅 pkg/tui 重放消费端）无因果。
- Step 4 真机（必须）：`build/bin/forebrain resume cli-81e8e333-…`，副本 home
  `$TMPDIR/fb-verify-replay/home` + `tmux -L fbreplay`（原 scratch home 与真实
  `~/.forebrain` 均零写入；进入方式=滚轮到"快任务"卡点其任务行，footer
  `viewing d5e4 · esc to return`）。逐块比对 live（§2）：

  1. ▲ Thought for 10s ✔
  2. ● Read README.md lines 1-3 · <0.1s（└ 1| scratch project…）✔
  3. ▲ Thought for 1s（README.md has 3 lines. Now read go.mod.）✔
  4. ● README.md 读取成功（3 行）。接着读取 go.mod。✔（修复前整块丢失）
  5. ● Read go.mod lines 1-5 · <0.1s（└ 1| module example.com/scratch）✔
  6. ▲ Thought for 1s（go.mod has 5 lines…）✔（修复前丢失）
  7. ● 两个文件均存在且已按顺序读取完毕：… ✔ 只出现一次（ended 的 Output
     不再重复末句）

  已知刻意延后：replay 无 "Worked for · first token" 行（缺 `FirstToken` 落库，
  计划 §5 明示另计）。

## Deviation（均已留痕）

- owner 指令（2026-10-30）："必须把主树dev分支的改动提交后合到git worktree里继续
  开发本计划" → 主树 55 文件落成本地 bulk commit `202ed4f2`（不 push），worktree
  从它建出（原计划 §5"不并入"条款被 owner 本句覆盖）。
- 验收入口：resume 需 cwd=会话项目目录（`$TMPDIR/fb-verify-async/proj`，只读使用）
  且 `ZHIPUAI_API_KEY` 需可解析（dummy 值，重放不调模型）——新 config 门的
  环境性要求，非数据改动。
- 计划里的 `Down,Down,Enter` 入口只适用当时的底卡；本次会话底卡是后来的
  "验收快照"，故改用滚轮+点击 d5e4 任务行（等价入口）。

## 收尾状态

- 本地 commit：`fix(tui): replay a worker-backed subagent view row by row`
  （分支 `exec/subagent-replay-blocks`，仅 `pkg/tui/commands.go` +334/-28、
  `pkg/tui/commands_test.go` +220）。
- **未 push、未 ff-merge、未删 worktree**——按约定等 owner 明确一句再动。
