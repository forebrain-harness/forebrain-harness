# 修复：subagent 视图只显示回答/思考卡，工具卡全部缺失

> 状态：已实现，待 owner 审阅提交（工作区改动，零 commit）
> 基线：`0e6e3827`（dev）
> 改动面：`pkg/run/subagent.go`、`pkg/run/subagent_test.go`

---

## 1. 现象

fire-and-forget 派发的子代理（`subagent_send`）在它自己的视图里只显示 assistant 消息卡和
“Thought for Ns” 思考卡，**一张工具卡都没有**；主视图里对应的
`○ Running 1 general-purpose task in background… └ …` 卡片正常。同一缺陷同时影响 TUI 与
gateway/web，因此修在引擎 `pkg/run`，不动 TUI 渲染。

## 2. 根因

`subagent_send` 的 handler 为“活过本次 turn”的执行重建了一个 `context.Background()` 上下文，
只搬了 4 个身份事实（session id / prompt-cache key / run id / tool-use id），**没有搬派发 turn
冻结在上下文里的 4 个 run-scoped hook**：

- step hook —— 工具步事件的唯一发射前提。`pkg/run/orchestration_llm.go` 的 started/completed
  两个发射点都是 `step := tool.StepHookFromContext(ctx, w.state)`，为 nil 时整段跳过。所以不是
  “画不出来”，是**事件从未产生**：子代理的工具步既不进 event log，也不进任何 surface 的实时画卡路径。
- network / subagent / inline 三个 approval hook。TUI 靠进程级 `s.Env.Tools()` 的 hook 兜底还能
  工作（但会随进程状态漂移）；gateway 的 hook 只装在每个 run 的 `agCtx` 上，State 上没有兜底，
  即 **web 的 async 子代理审批在修复前是真的会失败**。

裂口位置就是这一处上下文构造。设计契约本来就写在代码注释里
（`pkg/tui/chat_turn.go`：turn 内派发的 run 通过 `prepareTUIAgentBase` 冻结的上下文继承 hook，
**turn 之外运行的必须自己携带**），`subagent_send` 的执行正是“outside a turn”，却没有携带——
与既有契约冲突，不是设计选择。

## 3. 修复

`pkg/run/subagent.go`：

1. 新增 `asyncSubagentContext(ctx) context.Context`：把原 handler 里的身份搬运**原样**移入，
   再补上 4 个 hook（`StepHookFromContext(ctx, nil)` 取到才装；`nil` 不装，绝不发明 hook）。
   兜底传 `nil` 而不是 `factoryToolsState(fac)`：子执行在项目 pool runner 的工具状态上，那个
   State 不指向派发者；sync 路径的取值规则就是“只继承派发上下文里冻结的 hook”。
2. `spawnAsyncSubagent` 改为自己构造：`startSubagent(asyncSubagentContext(ctx), fac, d)`。
   于是“异步派发带什么过边界”只存在于一个函数里，且现有 `ctxCaptureSubagentExecutor` 能直接测到。
3. `subagent_send` handler 删掉那段内联构造，改为把派发 `ctx` 直接交给 `spawnAsyncSubagent`。
   session / prompt-cache / parent run id / tool-use id 行为一字未变。

形态是“同一语义多处存在，唯独出错路径没用它”——sync 派发（`subagent_run`/`subagent_fanout`）与
continuation 都带 hook，只有 async 初始派发不带。其余入口逐个核对过，都不需要改：

| 入口 | 上下文来源 | hooks |
|---|---|---|
| `subagent_run` / `subagent_fanout` | `execGeneralSubagent` → `dispatchSubagent` → `prep.ctx` | ✅ 继承派发 ctx |
| `subagent_send` | 原来的 `context.Background()` + 4 身份字段 | ❌ 本 bug |
| `subagent_continue` / 用户给子代理发消息 | `subagentRecordContext(ctx,…)` | ✅ 继承派发 ctx |
| 计划评审 `RunPlanReviewSubagent` | `dispatchSubagent(reviewCtx)` + 显式 StepHook/Approve 字段 | ✅ 双保险 |
| `/goal` 检查 | `dispatchSubagent(ctx)` | ✅ 继承派发 ctx |
| 引擎自动续跑 `startSubagentContinuation` | `surface.Frame` 自装（web 4 个全装，TUI 装 step + 进程级 approval 兜底） | ✅ 由 surface 自装 |

## 4. 行为变化

| 场景 | 旧 | 新 |
|---|---|---|
| async 子代理跑工具 | 无 StepEvent：无 tool 事件、子视图无工具卡（TUI/web 皆然） | 产生事件 + 实时工具卡，落 event log，live 与 replay 同源 |
| async 子代理工具被审批门拦下（web） | hook 缺失 → 整个执行失败 | 与 sync 一致：就地提问、批准后继续 |
| async 子代理工具被审批门拦下（TUI） | 靠进程级 hook 兜底 | 用派发 turn 冻结的 hook：与 sync 同源，不随进程状态漂移 |
| sync 派发 / continuation / 无 surface 的纯引擎派发 | 如上 | 不变 |

## 5. 机器可查的验证

回归测试（`pkg/run/subagent_test.go`）：

- `TestAsyncSubagentExecutionKeepsTheDispatchingRunsHooks`：经**注册的 `subagent_send` 工具**
  派发，断言捕获到的执行上下文带全部 4 个 hook，并对 step hook 做行为断言（测试自己的记录器收到事件）。
- `TestAsyncSubagentContextKeepsTheDispatchIdentity`：session / prompt-cache / run id / tool-use id
  穿越边界后仍正确（保护第 3 步的搬运）。
- `TestSubagentDispatchKernelsKeepTheDispatchingRunsHooks`：表驱动覆盖两个派发内核
  （`execGeneralSubagent` 与 `subagent_send`），两者都必须拿到派发者的 step hook。

修复前（复现，失败留痕）：

```
--- FAIL: TestAsyncSubagentExecutionKeepsTheDispatchingRunsHooks
    the async subagent execution context carries no step hook: …
--- PASS: TestAsyncSubagentContextKeepsTheDispatchIdentity
--- PASS: TestSubagentDispatchKernelsKeepTheDispatchingRunsHooks/execGeneralSubagent
    --- FAIL: TestSubagentDispatchKernelsKeepTheDispatchingRunsHooks/subagent_send
```

修复后：

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1        → ok (81s)
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/process ./pkg/tui ./pkg/gateway -count=1 → 全 ok
go vet ./...                                                                             → 无输出
```

包图：`pkg/run` 的 import 边与 fan_in/fan_out 与 HEAD 完全一致（本次只加行数，不新增 import 边）。

## 6. 真机验收（真实 provider，事故现场同一 provider/model）

隔离方式：临时 `FOREBRAIN_HOME` + 临时项目目录 + 独立 tmux socket，凭据取自用户自己的
`~/.forebrain/.env`；真实 `~/.forebrain` 未被写入（该库里 `fb_sessions` 没有本次任何会话 id）。

模型：`zhipuai/glm-5.3`（reasoning effort xhigh，即用户配置里这个模型的值）。

1. 主 agent 用 `subagent_send` 派发后台子代理读文件 → 主视图出现
   `○ Running 1 general-purpose task in background… └ 慢任务 · 0s`，roster 出现 `general-purpose 慢任务`。
2. `Down, Down, Enter` 进入子视图，footer 变成
   `viewing general-purpose · e6ab · esc to return · zhipuai/glm-5.3 · xhigh`——与事故截图同一形态。
3. 子视图内容（`tmux capture-pane`，节选）：

```
● 我将按顺序读取这两个文件，然后报告行数。

● Read README.md lines 1-3 · <0.1s
  └ 1| scratch project for verifying async subagent tool cards
    2|
    3| fake file A

● Read go.mod lines 1-5 · <0.1s
  └ 1| module example.com/scratch
    2|
    3| go 1.24

▲ Thought for 1s
  Both files exist. Let me count the lines.

● 两个文件均存在，已按顺序读取完毕。报告如下：
  | README.md | 存在 | 3 行 |
  | go.mod    | 存在 | 5 行 |

─ Worked for 16s · first token 4.5s · 12:03 ─
viewing general-purpose · e6ab · esc to return · zhipuai/glm-5.3 · xhigh
```

工具卡与思考/回答卡交替同序，正是修复前缺失的那一类卡。

4. 事件日志对照（scratch home，即计划里的那条查询）：

```
tool_call_completed||5                              ← 主 agent 自己的工具步
tool_call_started||5
tool_call_completed|subagent-37ef31db-…|4           ← 修复前这类行是 0 条
tool_call_started|subagent-37ef31db-…|4
tool_call_completed|subagent-e6abb181-…|2
tool_call_started|subagent-e6abb181-…|2
…
# 谁派发的：
tool_call_started|subagent_send      ×5
tool_call_completed|subagent_send    ×5
```

`tool_meta.agent_id` 形如 `subagent-<uuid>`，与 roster key 一致；事件 payload 结构与计划里的
查询列（`$.tool_meta.agent_id`）吻合。

## 7. 遗留（本次不做，均为既有问题）

1. **replay 仍与 live 不同源**：`forebrain resume` 重放一个已结束的子代理视图时，只重建出
   部分卡片（实测 live 两张工具卡 + 三条思考，replay 只有一张工具卡 + 一条思考 + 最终回答）。
   这是**既有缺陷、与本次修复无关**：用基线 commit 的 `pkg/run/subagent.go`（`-overlay` 构建）
   重放同一份数据，输出与修复版逐行相同；`pkg/tui` 在本次改动里一字未改，修复前的历史会话
   replay 输入也完全不变。修它属于 `pkg/tui` 的 `replayWorkerBody.consume` 对齐问题，另计。
2. **`startSubagentContinuation` 的无 surface auto-continue**：引擎自己发起，没有派发上下文可继承；
   TUI 的 `subagentSurface.Frame` 只装 step hook，审批靠进程级 hook 兜底。今天可用，但若撤掉
   进程级 approval hook，auto-continue 的审批会立刻坏掉——届时把 TUI 的 Frame 换成
   gateway `withDetachedGatewayApprovalHooks` 那种“4 个全装”的形态。
3. **`scripts/package-graph.sh` 生成的 `graph.json` 在 HEAD 上已经过期**（`pkg/agent`/`pkg/tool`/
   `pkg/event`/`pkg/run` 的 loc 都与代码对不上，`pkg/run` 少记 715 行）。本次没有重新生成它
   （那是另一拨改动的产物），只核对了本次不新增 import 边。

## 8. 维护注意

- 契约现在集中在一个函数：`asyncSubagentContext`。**未来任何 fire-and-forget 执行入口都必须走它**，
  否则子执行的工具步会静默消失——本 bug 的形态就是“没有报错，只是没有卡”。
- 评审重点：①`asyncSubagentContext` 只搬事实、不造事实（`nil` 不装）；②既有 async 生命周期事件
  （spawn/end/usage/budget）一字未变；③新增回归测试是否真的走 `subagent_send` 工具路径
  （走 `spawnAsyncSubagent` 直连无法复现本 bug——那条路在修复前本来就继承派发 ctx）。
