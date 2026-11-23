# 修复：subagent_send 异步边界丢掉派发事实（模型 pin / plan 模式 / batch 工作区 / 会话 effort）

> **Executor instructions**：严格按步骤执行。每一步的验证命令都必须跑，并确认预期输出后再进入下一步。
> 遇到「STOP conditions」里的情况就停下来报告，不要自行发挥。完成后不要 commit（owner 手动审阅提交）。
>
> **Drift check（先跑）**：本计划写于工作区 HEAD `c3377e14`（2026-11-13，`git status --short` 干净）。
> 计划依赖的是**内容锚点**，不是行号。开工前逐条核对下面这些锚点，内容不一致就是 STOP：
>
> ```bash
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
> git rev-parse --short HEAD                      # 写计划时是 c3377e14；若有更新的 commit，继续核对锚点即可
> grep -c 'func asyncSubagentContext(ctx context.Context) context.Context {' pkg/run/subagent.go      # 期望 1
> grep -c 'func (e \*capturingModelArgumentExecutor) pinsOf()' pkg/run/subagent_test.go              # 期望 1
> grep -c 'func TestSubagentSendModelArgumentPinsTheAsyncRun' pkg/run/subagent_test.go              # 期望 0
> grep -c 'func WithSessionModelSelection' pkg/run/config.go                                        # 期望 1
> grep -n 'entry, err := spawnAsyncSubagent' pkg/run/subagent.go                                    # 期望 1 行
> ```
>
> 只动 `pkg/run/subagent.go` 与 `pkg/run/subagent_test.go`。`subagentModelDisplay`（`pkg/run/subagent.go:4666`）是 `c3377e14`
> **刚落地的显示投影**（另一个任务的成果，不在本计划范围）：同一文件的不同区域，**不要**碰它，也不要碰 `configuredRow` 等行投影代码；
> 需要改到那里就停下来报告。

## Status

- **Status**: 已实施并真机验收通过（2026-11-13）
- **Priority**: P1（owner 报告的契约级缺陷：交接模型没生效）
- **Effort**: S
- **Risk**: LOW（只补上下文传递 + 两个测试）
- **Depends on**: none
- **Category**: bug
- **Planned at**: 工作区基线 `c3377e14`，2026-11-13（本仓库 `plans/`、`docs/plan/` 已 gitignore）
- **Issue**: 无

## Why this matters

owner 在 TUI 的 `exit_plan_mode` 审批里选了 **hand off to zhipuai/glm-5.3**，主代理（`qodercn/qmodel_latest`）也正确地把 hand-off 行 id
传给了 `subagent_send`，但实际派出的执行子代理**跑的还是主代理的模型**。这不是大模型幻觉，是代码缺陷：`subagent_send` 在派发上下文上
解析出了模型 pin，随后 fire-and-forget 执行要用「后台上下文」重建，而那个白名单漏掉了 pin —— 子代理因此回落到会话模型，且它的 run 记录、
subagent 视图底栏、ledger 都如实写成 `qodercn/qmodel_latest`。修好之后：交接指定的模型就是执行子代理真实请求的模型，记录/视图/ledger 与之一致。

同一处白名单还原样漏掉了三类按派发上下文决定的事实（owner 2026-11-13 裁决：**全量对齐异步边界**）：

1. `tool.ModeFromContext`（plan/agent 模式）→ plan 模式下用 `subagent_send` 派出的 general-purpose（可写）子代理不受 plan 模式写入限制（写权限红线）；
2. batch 任务的 `TaskWorkspace` / `TaskContext` / `BatchBoundary` → 异步子代理会跑到任务工作区之外；
3. `SessionModelSelection`（会话模型选择）→ 子代理沿用会话模型时的 reasoning effort 细化被丢掉。

## Current state（证据链，全部为本次调查实测）

### 现场证据（另一个 TUI 进程 PID 84703，会话 `cli-028bdaf3-…`）

- hand-off 已正确落盘 —— `~/.forebrain/workspace/state/modes/cli-028bdaf3-0330-4ff1-822c-76bf8f6a524f.json`：

  ```json
  {"mode":"agent","has_exited_plan":true,
   "plan_hand_off":{"model_id":"llm-legacy-eb55960ddc4daafa","provider":"zhipuai","model":"glm-5.3"}}
  ```

- 主代理**没有幻觉**：`~/.forebrain/logs/2026-11-13/tui-doudoudeMacBook-Air.local-84703-e15703.jsonl` 第 284 行，
  `subagent_send` 的工具入参里就有 `"model": "llm-legacy-eb55960ddc4daafa"`（即 hand-off 行 id）；第 286 行的回执给出子 run `8c3f0530-…`。
- 子代理**没有**跑在 glm-5.3：同一日志中 `run=8c3f0530…` 的每一条 `llm request` 的 `url` 都是
  `https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/age`（qodercn）；整份日志里唯一的 bigmodel 请求在 18:46:58、模型
  `glm-5.3-flash`（派发发生在 18:48:04 之后），即没有任何请求发往 zhipuai 的 `glm-5.3`。
- 该 id 不是陈旧 id、也不是授权开关的锅：真实库 `~/.forebrain/state/forebrain.state.sqlite` 的 `fb_llm_models` 里有
  `llm-legacy-eb55960ddc4daafa | zhipuai | glm-5.3 | base_url=https://open.bigmodel.cn/api/coding/paas/v4 | api_key 非空`。

### 代码链（`pkg/run/subagent.go`，当前源码）

1. 工具入口解析 pin：`subagent_send` 注册处（约 3652 起）→ 3676–3681：

   ```go
   if in != nil {
       pin, err := subagentModelOverrideForRef(ctx, fac, in.Model)
       if err != nil { return "", err }
       ctx = WithSubagentModelOverride(ctx, pin)
   }
   ...
   entry, err := spawnAsyncSubagent(ctx, fac, taskID, title, task, subType, worktree)   // 3683
   ```

   `subagentModelOverrideForRef`（4527 起）用 `usableModelByID`（4464 起）读库；因为 id 等于
   `handOffModelID`（4512 起，读 `<stateRoot>/state/modes/<sid>.json` 的 `plan_hand_off.model_id`），
   hand-off 例外放行开关，返回 `{ModelID, Provider: zhipuai, Model: glm-5.3, Entry: 行, Version: …}`。

2. 异步入口重建上下文：`spawnAsyncSubagent`（2245 起）第 2258 行

   ```go
   prep, handle, err := startSubagent(asyncSubagentContext(ctx), fac, d)
   ```

3. **根因**：`asyncSubagentContext`（2208–2243）用 `context.Background()` 重新拼装白名单，只搬了
   `AgentSessionID/ConversationSessionID`、`PromptCacheKey`、`RunID`、`ToolUseID` 和三个 hook（step / subagent approval / inline approval）；
   `SubagentModelOverride` 没搬。

4. 于是 `prepareSubagentExecutionResolved`（1785 起）第 1992 行的第一优先级落空：

   ```go
   if override, ok := SubagentModelOverrideFromContext(baseCtx); ok && agentType != GoalCheckSubagentType {
       pin = override
       modelLive = pin.Entry != nil
   } else if modelID, provider, model, effort, ok := cat.Model(agentType); ok {
       ...
   } else if provider, model, effort := AgentModel(own, sid, ...); strings.TrimSpace(model) != "" {
       ...
   }
   ...
   if pin.valid() {
       entry.ModelProvider = ...; entry.Model = ...; entry.ModelID = ...
       cctx = WithSubagentModelOverride(cctx, pin)
   }
   ```

   → 落到会话模型（qodercn/qmodel_latest）：写进 `entry`（ledger、spawned 事件、subagent 视图底栏都读它），也写进 `cctx` 的 pin。
   LLM 链上的 `wrapSubagentModelOverrideLLM`（4601 起）自然看不到任何 pin，请求发往会话 provider。

同一函数里被 `baseCtx` 读取、但 `asyncSubagentContext` 同样漏掉的还有（行号为当前源码）：

| 事实 | prepare 的读取点 | 丢失后的行为 |
| --- | --- | --- |
| `tool.ModeFromContext` | 1937–1945（写子代理自己的 plan 模式状态文件） | plan 模式下 `subagent_send` 派出的可写子代理不受 plan 模式限制（`pkg/tool/permissions.go:906` 的 GuardTool 也按 ctx 模式判定） |
| `tool.TaskWorkspaceFromContext` | 1810（worktree 拒绝条件）、1872 | 异步 batch 派发跑到任务工作区之外；带 worktree 的异步 batch 派发绕过了「batch 不能建 worktree」的拒绝 |
| `tool.TaskContextFromContext` / `tool.BatchBoundaryFromContext` | 1872–1877 | 子代理拿不到任务事实/边界 |
| `SessionModelSelectionFromContext` | 2015–2020 | 子代理沿用会话模型时，effort 细化丢失 |

（`tool.IsSubagentRunFromContext` 是「子代理不得再派子代理」的结构性守卫，主代理 ctx 上永不存在，**不搬**——不要为不可达状态写防御代码。）

### 复现（已在本地实测，勿改源码即可复现）

用 overlay 把一个临时测试塞进包内（不改工作区），确认「派发 pin 在异步边界丢失」：

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
# /tmp/fb-repro/zz_async_pin_repro_test.go 见本计划「Test plan」第 1 条；overlay.json 把该文件映射到 pkg/run/ 下的一个新文件名
CGO_ENABLED=1 go test -tags fts5 -overlay=/tmp/fb-repro/overlay.json ./pkg/run \
  -run TestReproSubagentSendModelArgumentPinsTheAsyncRun -count=1 -v
```

实测（修复前）：`captured pins = 0, want 1 (the async run dropped the dispatch pin)` → FAIL。
把本计划的四段搬运代码临时打进 `asyncSubagentContext`（同样用 overlay）后同一用例 PASS。这就是「改在因上」的证据。
外部评审 `zhipuai/glm-5.3` 在隔离 worktree 里独立重做了同一复现与补丁验证（两轮：`c5cab032` 与 `c3377e14`），结论一致；
第二轮还把 Step 4 真机验收双向跑通（见「评审记录」）。

## 修复设计

只改一个函数（`asyncSubagentContext`），把「派发上下文里决定子代理行为的事实」显式搬过边界，并把**不变式写进注释**：
`prepareSubagentExecutionResolved` 从 base context 读的、**派发回合可能携带**的每一条事实，都必须在这里有一行；以后在那里新增一条而这里漏写，
就是一次静默行为丢失。（唯一刻意不搬的是 fanout 的任务序号 `subagentTaskIndexFromContext`，只有**同步** fanout 会设置它，异步路径不可达。）

目标代码形状（插在三个 hook 搬运之后、`return base` 之前）：

```go
	// The facts the child's dispatch reads from its base context cross here
	// too. A dispatch is decided on the dispatching context and then runs on
	// this one, so a fact that is not moved is silently lost — the child does
	// not fail, it just behaves like something the caller never asked for:
	//
	//   - the mode (plan/agent): prepareSubagentExecutionResolved writes the
	//     child's own plan-mode entry from it, and GuardTool reads the same
	//     mode off the context — without it a write-capable child dispatched
	//     during planning could edit files;
	//   - a batch dispatch's workspace, task facts and boundary: the child
	//     works inside the execution's workspace under the same task;
	//   - the dispatch-time model pin: prepare records it and routes every
	//     request of the run on it (the model argument, or the plan review's
	//     pick) — dropped, the child runs the conversation's model and its
	//     record names that instead;
	//   - the session's model selection: the effort a child on the
	//     conversation's model runs at.
	//
	// The invariant: every fact a dispatching turn can carry that
	// prepareSubagentExecutionResolved reads from its base context has a line
	// here. A new one added there without one is a behaviour loss, not a
	// compile error. The one base-context read deliberately absent is the
	// fanout task index (subagentTaskIndexFromContext): only the synchronous
	// fanout sets it.
	//
	// A hook the dispatcher does not carry stays absent: nothing is invented
	// here.
	if mode := strings.TrimSpace(tool.ModeFromContext(ctx)); mode != "" {
		base = tool.WithMode(base, mode)
	}
	if ws := strings.TrimSpace(tool.TaskWorkspaceFromContext(ctx)); ws != "" {
		base = tool.WithTaskWorkspace(base, ws)
		if text := strings.TrimSpace(tool.TaskContextFromContext(ctx)); text != "" {
			base = tool.WithTaskContext(base, text)
		}
		if boundary, ok := tool.BatchBoundaryFromContext(ctx); ok {
			base = tool.WithBatchBoundary(base, boundary)
		}
	}
	if override, ok := SubagentModelOverrideFromContext(ctx); ok {
		base = WithSubagentModelOverride(base, override)
	}
	if sel, ok := SessionModelSelectionFromContext(ctx); ok {
		base = WithSessionModelSelection(base, sel)
	}
```

同时改写 `asyncSubagentContext` 的文档注释：原文只说「the few facts … hooks」，改为「派发事实 + 运行期 hook 都从这里显式跨越，
清单必须与 `prepareSubagentExecutionResolved` 的读取保持一致」。（`SessionModelSelectionFromContext` / `WithSessionModelSelection` 在
`pkg/run/config.go:2265/2274`；`tool.WithMode/WithTaskWorkspace/WithTaskContext/WithBatchBoundary` 在 `pkg/tool/state.go`、`pkg/tool/batch_boundary.go`。）

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 是否刻意修订 |
| --- | --- | --- | --- |
| exit_plan hand-off → `subagent_send(model=<handoff 行>)` | 子代理跑会话模型；ledger/视图/事件都写会话模型 | 子代理跑 hand-off 行；记录与视图写该行（`ModelLive=true`，续聊会重读该行） | 是（本次修复） |
| `subagent_send(model=<允许的行>)` | 同上 | 同上 | 是 |
| `subagent_run` / `subagent_fanout` / `subagent_continue` | 已经正确 | 不变 | 否 |
| `subagent_send` 不带 `model` | 用类型配置或会话模型 | 不变（pin 只在非空时进入 ctx） | 否 |
| plan 模式下 `subagent_send` 派出 general-purpose | 子代理按 agent 模式运行，可写文件 | 子代理继承 plan 模式：状态文件写 plan、写工具被 GuardWrite 限制到 plan 文件 | 是（owner 裁决全量对齐） |
| batch 任务内 `subagent_send` | 子代理在任务工作区外；带 worktree 的异步派发不被拒绝 | 子代理沿用任务工作区/任务事实/边界；worktree 派发被拒绝（与同步路径一致） | 是 |
| 子代理沿用会话模型时的 effort | 丢细化 | 沿用会话选择（与同步路径一致） | 是 |
| 同上路径的 hook/会话 id/cache key/tool use id | 已搬运 | 不变 | 否 |

## Scope

**In scope（只改这两个文件）**

- `pkg/run/subagent.go`：`asyncSubagentContext` 及其文档注释。
- `pkg/run/subagent_test.go`：新增两个测试（见 Test plan），外加一个只读访问器 `modelOf()`（紧跟既有 `pinsOf()`）。

**Out of scope（明确不碰）**

- `subagentModelDisplay`（`pkg/run/subagent.go:4666`）、`configuredRow`、`usableModelByID`/`handOffModelID`：显示/授权投影，
  `4666` 那处是 `c3377e14` 刚落地的另一个任务的成果；
- `pkg/tui`、`pkg/gateway` 的任何投影（记录修好后视图自动正确，不需要改）；
- `subagent_send` 回执里补 `model` 字段（不是本 bug，别加）；
- `asyncSubagentContext` 里**没有**搬运的 per-call 事实（`ToolApprovalResume`、`PolicyApproved`、`ToolStepID`、`TurnAnchorMessageID`、`TurnInputRuntime`、`PartialSessionCapture`、`StreamingDispatch` 等）：它们属于正在进行的某一次工具调用，不属于子 run，**不得**搬。
- 任何 commit / rebase / merge / worktree 清理。

## Commands you will need

| 用途 | 命令 | 成功预期 |
| --- | --- | --- |
| cgo/PDFium 环境 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | 无输出 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentSendModelArgumentPinsTheAsyncRun\|TestAsyncSubagentContextCarriesTheDispatchFacts' -count=1 -v` | 两个用例 PASS |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok` |
| 相邻包回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tool -count=1` | 两个包 `ok` |
| vet（CI 同款） | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/process` | 无输出 |
| 格式 | `gofmt -l pkg/run/subagent.go pkg/run/subagent_test.go` | 无输出 |
| 真机二进制 | `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`（或 `$D build`） | exit 0 |

## Steps

### Step 1：先落回归测试，确认它们**在今天源码上失败**

在 `pkg/run/subagent_test.go` 末尾（`capturingModelArgumentExecutor` 那一族测试附近）新增：

1. `func (e *capturingModelArgumentExecutor) modelOf() string` —— 加锁返回 `e.model`（放在既有 `pinsOf()` 旁边）。
2. `func TestSubagentSendModelArgumentPinsTheAsyncRun(t *testing.T)` —— 用 `newModelArgumentFixture(t, subagentsEnabledConfig())`，
   行用 **fixture 方法**播种（不是自由函数）：`rowID := fix.seedModelRow(t, "zhipuai", "glm-5.3", "https://one.test/v1", "key-one", true)`
   （签名见 `pkg/run/subagent_test.go:8376`）。注册工具照抄同文件 `subagentRunTool`（`:8430-8444`）的形状：

   ```go
   a, err := agent.New(noopLLM{}, "main", "main")   // 失败即 t.Fatalf
   if err := RegisterSubagentTool(a, fix.fac); err != nil { t.Fatalf("RegisterSubagentTool: %v", err) }
   send, ok := aToolByName(a, "subagent_send")      // 没有现成 helper，照抄 subagent_run 的查找方式
   if !ok { t.Fatal("subagent_send not registered") }
   ```

   然后按 rowID 组 JSON（`fmt.Sprintf` + `%q`）调 `send.Handle(dispatchTestCtx(), ...)`，参数对象是
   `{"task":"inspect","subagent_type":"general-purpose","model":"<rowID>"}`；
   轮询最多 5s 等 `fix.capture.pinsOf()` 非空；断言 `pin.ModelID == rowID && pin.Provider == "zhipuai" &&
   pin.Model == "glm-5.3" && pin.Entry != nil`，并断言 `fix.capture.modelOf() == "glm-5.3"`（交给执行器的记录模型就是该行）。
3. `func TestAsyncSubagentContextCarriesTheDispatchFacts(t *testing.T)` —— 构造携带全部事实的 ctx 后过一次
   `asyncSubagentContext`，逐条断言仍然可读：

   ```go
   ctx := context.Background()
   ctx = toolpkg.WithMode(ctx, "plan")
   ctx = toolpkg.WithTaskWorkspace(ctx, "/tmp/fb-task-ws")
   ctx = toolpkg.WithTaskContext(ctx, "work item 7")
   ctx = toolpkg.WithBatchBoundary(ctx, toolpkg.BatchBoundary{Workspace: "/tmp/fb-task-ws"})
   ctx = WithSubagentModelOverride(ctx, SubagentModelOverride{ModelID: "llm_row2", Provider: "zhipuai", Model: "glm-5.3"})
   ctx = WithSessionModelSelection(ctx, PrimaryModelState{ModelID: "llm_row1", Provider: "qodercn", Model: "qmodel_latest", Effort: "high"})
   carried := asyncSubagentContext(ctx)
   // 断言 ModeFromContext == "plan"、TaskWorkspace == "/tmp/fb-task-ws"、TaskContext == "work item 7"、
   // BatchBoundary ok 且 .Workspace == "/tmp/fb-task-ws"、SubagentModelOverrideFromContext ok 且 Model == "glm-5.3"、
   // SessionModelSelectionFromContext ok 且 Effort == "high"
   ```

   `BatchBoundaryFromContext` 要求 `Workspace` 非空才算携带成功（`pkg/tool/batch_boundary.go:135` 起），所以最小合法值就是
   `BatchBoundary{Workspace: "/tmp/fb-task-ws"}`。`PrimaryModelState` 的字段见 `pkg/run/config.go:1799-1805`；`WithSessionModelSelection`
   在 `pkg/run/config.go:2265`。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentSendModelArgumentPinsTheAsyncRun|TestAsyncSubagentContextCarriesTheDispatchFacts' -count=1 -v`
→ 用例 2 失败于 `captured pins = 0`、用例 3 失败于某条事实丢失（**这就是复现**）。若用例 2 在未改源码时通过，STOP：诊断错了。

### Step 2：在 `asyncSubagentContext` 里搬运四组事实

按「修复设计」的代码形状插入，并改写函数文档注释。**不要**顺手改别的函数。

**Verify**：同 Step 1 的定点命令 → 两个用例 PASS。

### Step 3：包级回归 + vet + gofmt

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tool -count=1
CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/process
gofmt -l pkg/run/subagent.go pkg/run/subagent_test.go
```

预期：两个 `ok`、vet 无输出、gofmt 无输出。若 `pkg/run` 有非本改动引起的失败：先分辨是否既有 flaky（连续 4 轮 `-race` 全绿才可放行，
不许改无关测试掩盖）；分不清就 STOP 报告。

### Step 4：真机验收（TUI + fake provider，禁真模型）

隔离环境（不要用默认 work 目录，避免和其他会话抢 tmux/端口）：

```bash
cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK="$TMPDIR/fb-asyncpin" FOREBRAIN_RUN_SESSION=fb-asyncpin FOREBRAIN_RUN_PORT=8765
export FAKE_DUMP_DIR="$TMPDIR/fb-dump-asyncpin"
rm -rf "$FAKE_DUMP_DIR"
$D build
$D reset
```

**harness 事实（决定命令怎么写；不要改驱动脚本）**

- fake provider 的 `tool` 模式按**进程内请求序号**发脚本：第 1 条请求吃第 1 个 call，之后答 `done`
  （`.claude/skills/run-forebrain/fake_provider.py:170-201,1083-1099`）。记忆管线等**后台 pass 也走同一条 wire**，
  所以脚本写成「同一个 call 的数组」：后台请求吃掉几份也不影响主回合拿到一份。
- 后台 pass 的会话 id 与主会话不同 → 它的 `handOffModelID` 读不到我们的 hand-off 状态，会以拒绝文案失败，
  **不会**伪造出一条带 pin 的 ledger 记录（这就是断言能保持干净的原因）。
- DB 是 `$FOREBRAIN_RUN_WORK/home/state/*.sqlite`（用 `$D db`），state root 是 `$FOREBRAIN_RUN_WORK/home/workspace`。
- **`agents:` 配置在 home 首次加载时固化进状态库 `fb_config`**（`forebrain.yaml` 只承载 gateway/credentials/database 三节，
  `pkg/config/file_keys.go:18-40`），所以每次 `$D start` 重写的 `enable_subagent:` 对**已存在的 home 无效**：
  `ENABLE_SUBAGENT=1` 必须出现在这个 home 的**第一次** `$D start` 上，后续 resume 带不带都无所谓（外部评审实测：
  首启不带开关 → 只有 17 个工具、`subagent_send` 报 `unknown tool`；首启带开关 → 27 个工具、8 个 subagent 工具）。
- 等证据要等**生产者侧**（ledger / provider 落盘的请求体 / provider-log），不要等屏幕文案。

**场景 A（复现→修复：hand-off 行经 `subagent_send` 真正生效）**

```bash
LEDGER="$FOREBRAIN_RUN_WORK/home/workspace/state/subagent-history.jsonl"
CALL='[{"name":"subagent_send","arguments":{"task":"probe the pinned model","title":"Probe model pin","subagent_type":"general-purpose","model":"llm-handoff-probe"}},
       {"name":"subagent_send","arguments":{"task":"probe the pinned model","title":"Probe model pin","subagent_type":"general-purpose","model":"llm-handoff-probe"}},
       {"name":"subagent_send","arguments":{"task":"probe the pinned model","title":"Probe model pin","subagent_type":"general-purpose","model":"llm-handoff-probe"}}]'

# 1) 先拿一个可 resume 的会话 id（普通回合）。ENABLE_SUBAGENT 必须在这个 home 的**首次** start 上带
#    ——agents 配置首启固化进 fb_config，之后 resume 再带无效（见上方 harness 事实）
ENABLE_SUBAGENT=1 $D start reply ok
$D submit 'hello'
$D wait 'Worked for' 25
SID=$($D sid); echo "SID=$SID"
$D stop

# 2) resume 同一会话；把前几条请求脚本化成「带 model 的 subagent_send」
ENABLE_SUBAGENT=1 RESUME_ID=$SID $D start tool "$CALL"

# 3) 注入 hand-off 行（subagent_allowed=0：只有 hand-off 例外能放行，正是 owner 的真实场景）
$D db "INSERT INTO fb_llm_models (id,provider,model,base_url,api_path,api_key,subagent_allowed,created_at,updated_at)
        VALUES ('llm-handoff-probe','zhipuai','glm-5.3','http://127.0.0.1:8765','/chat/completions','sk-local-fake',0,strftime('%s','now'),strftime('%s','now'));"
$D db "select id,provider,model,subagent_allowed from fb_llm_models where id='llm-handoff-probe';"

# 4) 写入该会话的 hand-off 状态
mkdir -p "$FOREBRAIN_RUN_WORK/home/workspace/state/modes"
printf '%s\n' '{"mode":"agent","has_exited_plan":true,"plan_hand_off":{"model_id":"llm-handoff-probe","provider":"zhipuai","model":"glm-5.3"}}' \
  > "$FOREBRAIN_RUN_WORK/home/workspace/state/modes/$SID.json"
cat "$FOREBRAIN_RUN_WORK/home/workspace/state/modes/$SID.json"

# 5) 触发派发，等 producer 侧证据
$D submit 'dispatch the executor'
for i in $(seq 1 100); do grep -q '"model":"glm-5.3"' "$LEDGER" 2>/dev/null && break; sleep 0.3; done
```

断言（三条都要命中）：

```bash
grep '"model":"glm-5.3"' "$LEDGER"                                   # a. ledger 记录的就是 hand-off 行
grep '"model_provider":"zhipuai"' "$LEDGER"
grep '"model_id":"llm-handoff-probe"' "$LEDGER"
python3 - "$FAKE_DUMP_DIR" <<'PY'
import glob, json, sys
models = {}
for f in sorted(glob.glob(sys.argv[1] + "/request-*.json")):
    try:
        models[f] = json.load(open(f)).get("model")
    except Exception:
        pass
print(models)
assert "glm-5.3" in models.values(), "b. no child request carried the pinned model"
assert "deepseek-chat" in models.values(), "c. no parent request carried the conversation model"
PY
$D provider-log | tail -20
```

预期：a、b、c 全命中。**修复前**：b 无命中，a 永远不会出现（子代理记录写的是 `deepseek-chat`，`model_live` 为假）。
若 25s 内 ledger 没有 glm-5.3 记录、且 `provider-log` 的第一次 REQUEST 不属于本回合：`$D stop`，重跑第 2–5 步（脚本按进程重置）；
连续两次不行就 STOP（不要改驱动脚本，更不要把 `subagent_allowed` 改成 1）。

**场景 B（plan 模式继承）**

```bash
$D stop
ENABLE_SUBAGENT=1 RESUME_ID=$SID $D start tool "$CALL"
$D db "select id from fb_llm_models where id='llm-handoff-probe';"       # 行还在（同一个 home）
$D submit '/plan'                                                      # `/plan` 是内置 slash 命令（pkg/turn/slash.go:247）；submit '/plan' 的可行性另见 pkg/tui/chat_session_test.go:2926
sleep 3; $D screen | head -20                                          # 屏幕上应出现 plan 模式标记
# /plan 会重写模式文件（进入 plan 清空 hand-off），所以 hand-off 状态在 /plan 之后写
printf '%s\n' '{"mode":"plan","has_exited_plan":false,"plan_hand_off":{"model_id":"llm-handoff-probe","provider":"zhipuai","model":"glm-5.3"}}' \
  > "$FOREBRAIN_RUN_WORK/home/workspace/state/modes/$SID.json"
$D submit 'dispatch the executor'
for i in $(seq 1 100); do grep -q '"model":"glm-5.3"' "$LEDGER" 2>/dev/null && break; sleep 0.3; done
WORKER=$(python3 - "$LEDGER" <<'PY'
import json,sys
rows=[json.loads(l) for l in open(sys.argv[1]) if l.strip()]
pinned=[e for e in rows if e.get("model")=="glm-5.3" and e.get("worker_session_id")]
print(pinned[-1]["worker_session_id"] if pinned else "")
PY
)
echo "WORKER=$WORKER"
cat "$FOREBRAIN_RUN_WORK/home/workspace/state/modes/$WORKER.json"
```

预期：`"mode": "plan"`（修复前该文件不存在 → 子代理实际在 agent 模式，可写文件）。若 `/plan` 后系统并未处于 plan 模式
（屏幕无标记、也没有 plan 提醒），按 STOP 记录「plan 模式继承仅单测覆盖」，不要删掉这段搬运。

**场景 C（回归：不带 `model` 的异步派发不变）**

```bash
$D stop
rm -rf "$FAKE_DUMP_DIR"
$D reset                                  # 干净 home：ledger 从零开始（新 home → 首启必须重新带开关）
ENABLE_SUBAGENT=1 $D start reply ok
$D submit 'hello'; $D wait 'Worked for' 25
SID=$($D sid); $D stop
CALL_REGRESS='[{"name":"subagent_send","arguments":{"task":"probe the default model","title":"Probe default model","subagent_type":"general-purpose"}},
               {"name":"subagent_send","arguments":{"task":"probe the default model","title":"Probe default model","subagent_type":"general-purpose"}}]'
ENABLE_SUBAGENT=1 RESUME_ID=$SID $D start tool "$CALL_REGRESS"
$D submit 'dispatch the executor'
for i in $(seq 1 60); do [ -f "$LEDGER" ] && break; sleep 0.3; done; sleep 2
grep -c '"model":"glm-5.3"' "$LEDGER" || true        # 期望 0
python3 - "$LEDGER" <<'PY'
import json,sys
for l in open(sys.argv[1]):
    if not l.strip(): continue
    e=json.loads(l)
    if e.get("agent_type")=="general-purpose":
        print("model=%r model_provider=%r" % (e.get("model",""), e.get("model_provider","")))
PY
```

预期：`glm-5.3` 命中数为 0；general-purpose 记录的 `model`/`model_provider` 为空或会话模型（与改动前一致：ctx 里没有 pin）。

**收尾**：`$D stop; rm -rf "$FOREBRAIN_RUN_WORK" "$FAKE_DUMP_DIR"`（tmux 会话必须杀干净、临时 home 必须删除；**不得**碰真实 `~/.forebrain*`）。

### Step 5：回填与报告

- 把这个计划文件复制到项目惯例路径 `docs/plan/SUBAGENT_SEND_ASYNC_DISPATCH_FACTS_PLAN.md`（gitignored），
  补「验收记录：场景 → 证据」小节，状态改「已实施并真机验收通过（日期）」。
- 报告：根因链（含真机/日志证据）、最小改动清单、验证证据（含场景 A/B/C 输出）、遗留限制（batch 路径与 effort 细化只有单测覆盖）。
- 不 commit。改动留工作区给 owner 审阅。

## Test plan

- 新增（`pkg/run/subagent_test.go`，紧跟 `TestSubagentModelArgumentPinsTheNamedRow` / `pinsOf` 那一族，风格照抄）：
  - `TestSubagentSendModelArgumentPinsTheAsyncRun`：异步派发的 pin 与记录模型（对应缺陷的直接回归）。
  - `TestAsyncSubagentContextCarriesTheDispatchFacts`：把异步边界的搬运清单钉死（mode / task workspace / task context / batch boundary / model pin / session selection）。
- 不改任何既有断言：`subagent_run`/`fanout`/`continue` 的既有 pin 测试（`TestSubagentModelArgumentPinsTheNamedRow`、
  `TestSubagentExecContextKeepsThePinnedModel`、`TestSubagentFollowsTheConversationModelAtDispatchOnly`、`TestModelLiveContinuationRereadsTheRow`、
  `TestUsableModelsLegacyHandOffNeedsCredentials`）必须原样绿。
- 失败路径：`subagent_send` 带一个未授权/无凭据的 id 仍然报既有三句拒绝文案（既有测试已覆盖，不需要新写）。
- 真机：Step 4 场景 A/B/C。

## Done criteria（机器可查）

1. `grep -c 'func asyncSubagentContext(ctx context.Context) context.Context {' pkg/run/subagent.go` → 1，且函数体内同时出现
   `tool.ModeFromContext(ctx)`、`tool.TaskWorkspaceFromContext(ctx)`、`tool.TaskContextFromContext(ctx)`、
   `tool.BatchBoundaryFromContext(ctx)`、`SubagentModelOverrideFromContext(ctx)`、`SessionModelSelectionFromContext(ctx)` 各 1 次。
2. `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`；`./pkg/process ./pkg/tool` → `ok`。
3. `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/process` 无输出；`gofmt -l` 对两个改动文件无输出。
4. Step 4：场景 A 的 a（三条 grep）与 b/c（python 断言无 `AssertionError`）全部通过；场景 B 的 worker 模式文件是 `"mode": "plan"`（或按 STOP 记录为单测覆盖）；场景 C 的 `grep -c '"model":"glm-5.3"'` 为 0 且 general-purpose 记录 model 为空。
5. `git status --short` 只出现 `pkg/run/subagent.go`、`pkg/run/subagent_test.go`（外加 gitignored 的计划/验收产物）；没有 commit。

## STOP conditions

- Step 1 的 `TestSubagentSendModelArgumentPinsTheAsyncRun` 在**未改源码**时通过 → 根因判断错了，停。
- `asyncSubagentContext` 的现有内容与「Current state」摘录不一致（drift check 失败）→ 停，先重新定位。
- 需要新增/修改 `pkg/tool`、`pkg/process` 的 API（搬运用的 setter 不存在/签名不同）→ 停，报告；不要造新 API。
- 派发报 `unknown tool "subagent_send"`（工具表里没有 subagent 族）→ 这个 home 出生时没带 `ENABLE_SUBAGENT=1`：
  `$D reset` 后从「首启带开关」重跑场景（见 Step 4 的 harness 事实），不要误入 hand-off/state 排查分支。
- 场景 A 里派发报 `is not one you may pick for subagents`（hand-off 例外没读到）→ 先核对 state root 与 state 文件路径；
  仍不行就报告。**不得**把 `subagent_allowed` 改成 1 来让验收通过。
- 场景 A 里 ledger 对了但请求体仍是 `deepseek-chat` → 说明还有第二处丢 pin，停，重新定位，不要只改投影。
- 任何验证失败需要改动 out-of-scope 文件（尤其 `subagentModelDisplay`，另一个会话在改）→ 停，报告。
- `$D start`/`/plan` 在隔离环境里无法复现预期（例如 plan 模式进不去、fake provider 端口冲突）→ 记录即可，别去改驱动脚本/产品代码。

## 评审记录（外部评审 `zhipuai/glm-5.3`，2026-11-13，三轮，最终结论：**通过**）

### 第三轮：基线已移动，计划在新 HEAD 仍成立

- HEAD 已从 `c5cab032` 移到 **`c3377e14`**（另一个任务的显示投影落地：`subagentModelDisplay` 改用 `FormatProviderModelDisplay`，
  `pkg/run/subagent.go:4666`；其 worktree 已删除）。评审在新 HEAD 的干净 worktree 重做双向复现：未打补丁时两用例 FAIL
  （`captured pins = 0`、六项事实全丢），按本计划代码形状打补丁后 PASS，`go vet`/`gofmt` 干净、`./pkg/run` 全量 `ok`（45.4s）。
  本计划在新基线**无需改逻辑**。
- 采纳的两处措辞修订（非阻塞建议）：① drift check 与 Status 的基线改为 `c3377e14`（并保留「更新 commit 时核对锚点」条款）；
  ② Out-of-scope/Executor instructions 里对 `subagentModelDisplay` 的说法从「另一个会话正在 worktree 里改」改为
  「`c3377e14` 刚落地的显示投影，不要碰」——避免执行者去找已删除的 `.worktrees/qodercn-model-display-name`。
- 未跑场景 B/C（第三轮声明）；**Step 4 仍是实施期必做项**，B 有单测兜底与降级 STOP。
- 第三轮产物：`.worktrees/plan-review-1734270104/`（`pkg/run/zz_review_async_pin_repro_test.go` + 标注 `REVIEW:` 的补丁），
  仅参考、不得依赖，主树未动。

### 第二轮：实跑 Step 4，双向验证（本轮的核心价值）

- 评审把上轮未跑的 Step 4 在真机上跑完了，并发现一个会让三个场景**全部**失败的过程性缺陷：**`ENABLE_SUBAGENT=1` 必须出现在 home
  的首次 start 上**。机制：`agents:` 节在 home 首次加载时固化进状态库 `fb_config`（`forebrain.yaml` 只承载
  gateway/credentials/database，`pkg/config/file_keys.go:18-40`），所以之后 resume 时 `cmd_serve` 重写 yaml 的
  `enable_subagent:` 不生效。实测：按计划原步骤 → `unknown tool "subagent_send"`（17 个工具）；首启带开关 → 27 个工具、8 个 subagent 工具。
  **已修正**：场景 A/C 的首个 `$D start reply ok` 改成 `ENABLE_SUBAGENT=1 $D start reply ok`，并把这一事实写进 Step 4 的
  harness 事实和 STOP conditions。（B 复用 A 的 home，自动继承。）
- **双向真机验证通过**（补丁 worktree 二进制 vs 未打补丁主树二进制）：
  - 修复后：ledger 命中 `"model":"glm-5.3"` / `"model_provider":"zhipuai"` / `"model_id":"llm-handoff-probe"`；子代理请求体是
    `glm-5.3`、主代理是 `deepseek-chat` —— 断言 a/b/c 全过；hand-off 例外（`subagent_allowed=0`）与 ledger 路径
    `$WORK/home/workspace/state/subagent-history.jsonl` 均属实。
  - 修复前：ledger 的 general-purpose 记录是 `model: deepseek-chat, model_provider: deepseek`（`model_live` 缺省），6 条请求体全是
    `deepseek-chat`、无 `glm-5.3` —— 与本计划「修复前 b 无命中、a 永不出现」的预测逐字一致，也正是 owner 事故的形态。
- 小瑕疵已修：场景 B 的 `/plan` 引用改为 `pkg/turn/slash.go:247`，并注明 `$D submit '/plan'` 的可行性依据
  `pkg/tui/chat_session_test.go:2926`（原引用的 `pkg/tui/commands.go:2344` 是 MsgKind case，不精确）。
- 第二轮未跑场景 B/C（B 有单测兜底 + 计划自带降级 STOP；C 是回归路径，修正开关后风险低）；**Step 4 仍是实施期必做项。**

### 第一轮：静态核实 + 隔离 worktree 复现

- 评审在隔离 worktree（基线 `c5cab032`）**独立复现**了缺陷：`subagent_send` 带 model 行 id 派发后 `captured pins = 0`；六项事实过
  `asyncSubagentContext` 后全部丢失。按本计划的代码形状打补丁后两用例转绿，`./pkg/run`（43.7s）、`./pkg/process`、`./pkg/tool` 全绿，
  vet/gofmt 干净；并逐环核对了代码链、现场证据与边界收敛（`spawnAsyncSubagent` 只有一个调用者；`pkg/run/fork.go` 里的
  `context.Background()` 是 nil-guard、不是同类边界；`subagent_run/fanout/continue` 的 pin 路径无此缺陷）。
- 采纳的修改：① 不变式注释收窄为「**派发回合可能携带**的事实」，并显式注明 fanout 的任务序号
  `subagentTaskIndexFromContext`（`pkg/run/subagent.go:1956`）刻意不搬（只有同步 fanout 设置它，异步不可达）；
  ② Step 1 的测试指引改为 fixture 方法 `fix.seedModelRow(...)`（`pkg/run/subagent_test.go:8376`）+ 照抄 `subagentRunTool`（`:8430-8444`）
  与 `aToolByName(a, "subagent_send")` 的注册/查找形状（原文写成自由函数会撞编译错误）；
  ③ 保留原注释里「A hook the dispatcher does not carry stays absent」这句（对 hook 是好不变式）。
- 第一轮未跑 Step 4（TUI 被并行会话占用），但核对了驱动子命令、fake provider 的脚本序号机制、以及场景 B 依赖的
  「子代理模式文件写在 workspace root」（`pkg/run/subagent.go:1932-1936`）都与源码自洽；第二轮已实跑，结论见上。
- 范围依据：四组事实「全量对齐异步边界」来自 owner 本会话的显式选择（选项原文：pin + plan 模式 + task workspace/batch 边界 + 会话 effort），
  不是从仓库推得；评审提出的确认点已由该选择关闭。
- 评审产物（仅参考，不得依赖，可能被清理）：`.worktrees/plan-review-4252018711/pkg/run/zz_review_async_pin_repro_test.go`。

## 验收记录（实施期实测，2026-11-13）

环境：工作区基线 `c3377e14`；隔离 TUI 环境 `FOREBRAIN_RUN_WORK=$TMPDIR/fb-asyncpin`、`FOREBRAIN_RUN_SESSION=fb-asyncpin`、
`FOREBRAIN_RUN_PORT=8765`、`FAKE_DUMP_DIR=$TMPDIR/fb-dump-asyncpin`；驱动脚本未改动。

### Step 1 复现（未改源码，先落测试）

```
=== RUN   TestSubagentSendModelArgumentPinsTheAsyncRun
    subagent_test.go:8551: captured pins = 0, want 1 (the async run dropped the dispatch pin)
--- FAIL: TestSubagentSendModelArgumentPinsTheAsyncRun (5.03s)
=== RUN   TestAsyncSubagentContextCarriesTheDispatchFacts
    mode = "", want plan
    task workspace = "", want /tmp/fb-task-ws
    task context = "", want work item 7
    batch boundary = {...}, ok=false
    model pin = {...}, ok=false
    session model selection = {...}, Set:false
--- FAIL: TestAsyncSubagentContextCarriesTheDispatchFacts
```

与计划预测逐项一致（pin 丢失 + 六项事实全丢），根因确认。只改 `asyncSubagentContext` 后同一定点命令两用例 PASS。

### Step 3 回归

| 检查 | 结果 |
| --- | --- |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok 45.927s` |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tool -count=1` | `ok 233.507s` / `ok 11.996s` |
| `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/process` | 无输出，exit 0 |
| `gofmt -l pkg/run/subagent.go pkg/run/subagent_test.go` | 无输出 |

### 场景 A：hand-off 行经 `subagent_send` 真正生效

会话 `cli-dea70cfc-3a07-425d-8a37-35db11a7a18d`；`fb_llm_models` 行 `llm-handoff-probe | zhipuai | glm-5.3 | subagent_allowed=0`；
`state/modes/<sid>.json` 带 `plan_hand_off.model_id=llm-handoff-probe`；脚本化 3 条 `subagent_send`（均带该 model）。

- a. ledger `subagent-history.jsonl`：6 条记录（3 次派发 × running/ok），全部
  `"model":"glm-5.3"`、`"model_provider":"zhipuai"`、`"model_id":"llm-handoff-probe"`、`"model_live":true`。
- b/c. fake provider 请求体：`request-0001..0004` 为 `deepseek-chat`（主代理），`request-0005..0007` 为 `glm-5.3`（子代理）；
  `provider-log` 共 7 条 `REQUEST`，与请求体一一对应。

修复前同一路径的记录是 `model: deepseek-chat, model_provider: deepseek`、`model_live` 缺省（见评审记录第二轮双向实测），
本场景的 a/b/c 全部转绿。

### 场景 B：plan 模式继承

`/plan` 之后会话模式文件为 `{"mode":"plan","phase":"plan","pre_plan_mode":"agent"}`；写入带 `plan_hand_off` 的 plan 状态后派发，
本轮新子代理（`started_at=1794576529`，`model=glm-5.3`）的模式文件为：

```json
{
  "mode": "plan",
  "phase": "plan",
  "pre_plan_mode": "agent",
  "updated_at": 1794576529
}
```

即 `tool.ModeFromContext` 已跨过异步边界（修复前该文件不存在，子代理实际在 agent 模式）。

过程记录（非产品缺陷）：场景 A 结束后 `$D sid` 返回的是最后一个 **worker** 会话 id，直接拿它 resume 会打开子代理会话；
场景 B 改用首次 hello 回合的主会话 id。另：resume 已有内容的会话时，driver 的 `start` 会因 banner `/ commands`
已滚出屏幕而打印 "TUI never reached the composer"，此时应用实际已启动、可正常 `submit`——按「不改驱动脚本」处理，未做产品改动。

### 场景 C：不带 `model` 的异步派发回归

`$D reset` 后全新 home（首启 `ENABLE_SUBAGENT=1`），两条不带 `model` 的 `subagent_send`：
`grep -c '"model":"glm-5.3"'` 为 `0`；4 条 general-purpose ledger 记录均为
`model='deepseek-chat' model_provider='deepseek' model_live=False`（会话模型，与改动前一致）。

### 收尾

`$D stop` 后已删除 `$TMPDIR/fb-asyncpin` 与 `$TMPDIR/fb-dump-asyncpin`，无残留 tmux 会话；隔离 home 全程未触碰真实 `~/.forebrain*`。

### 遗留限制

- batch 任务的 workspace/task facts/boundary 与 session effort 的跨界由 `TestAsyncSubagentContextCarriesTheDispatchFacts` 单测覆盖，
  真机未单独构造 batch/effort 场景（计划允许）。
- 派发记录/视图的显示投影未改动；`subagentModelDisplay`（`pkg/run/subagent.go:4666`）保持 `c3377e14` 原样。

## Maintenance notes

- 不变式（写进 `asyncSubagentContext` 的注释里）：**`prepareSubagentExecutionResolved` 从 base context 读的、派发回合可能携带的每一条事实，都必须在
  `asyncSubagentContext` 有一行搬运**。新增读取点时要同步这里，否则失败形态是「静默跑成另一个样子」，没有任何报错。
  唯一例外是 fanout 的任务序号（同步专用）；若将来做「异步 fanout」，必须同时搬 `subagentTaskIndexFromContext`，否则每个任务都会读到 0。
- 以后给 `subagent_send` 加新参数（如 reasoning effort、权限档位）时，先判断它是 dispatch 事实（要跨界）还是 per-call 事实（不跨界）。
- 新增 `*_send` 类的 fire-and-forget 入口时，必须复用它（`spawnAsyncSubagent` 只此一个调用者，边界只此一处）。
- 审阅重点：pin 到达后 `entry.ModelLive` 必须为真（续聊会重读行、按开关重新授权）；`subagent_run`/`fanout` 的 pin 记录不应出现任何差异。
