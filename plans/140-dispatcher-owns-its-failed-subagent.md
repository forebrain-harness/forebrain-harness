# Plan 140：被阻塞等待的子代理失败时，主代理拿到时长、原因与 task_id，并由主代理续跑

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置 1（否则 STOP 1）——依赖已完成**：`plans/README.md` 里 `139` 的状态是 DONE，且
> `grep -n "func fanoutDrawnCallError" pkg/tui/reducer.go` 有 1 条。原因：本计划把子代理失败时工具调用的错误文本
> 换成一段多行说明（写给主代理看的），139 保证它不会画在子代理卡上。
>
> **前置 2（否则 STOP 1）——目标文件必须干净**：
> `git status --porcelain pkg/run/subagent.go pkg/run/subagent_test.go pkg/turn/submit.go pkg/turn/submit_test.go pkg/tui/notify.go pkg/gateway/serve_run.go .claude/skills/run-forebrain docs/plan/SUBAGENT_CONVERSATION/011-subagent-auto-continue.md`
> 必须**无输出**。
>
> **Drift check（先跑）**：`git diff --stat 82ab168 -- pkg/run/subagent.go pkg/turn/submit.go pkg/tui/notify.go pkg/gateway/serve_run.go .claude/skills/run-forebrain`
> 有输出就按下文"现状"里的**函数名**逐段核对；被引用的代码含义变了即 STOP 1。下文行号是 `82ab168` 时的近似值，以函数名为准。
>
> **基线（Step 1 之前各跑一次，记下失败清单；否则 STOP 5）**：
> - `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/process ./pkg/gateway -count=1`（超时给足 600 s）
>
> 基线里已经红的用例是存量问题，**不是你要修的**；Done criteria 只要求"不新增红"。

## Status

- **Priority**: P1（owner 2026-10-26 指定）
- **Effort**: M
- **Risk**: MED（改了模型看到的工具结果文本，并收窄了 011 的自动续跑范围；引擎内部调用方的错误文本保持不变）
- **Depends on**: `plans/139-failed-subagent-card-says-why-once.md`（必须先 DONE）
- **Category**: bug + direction
- **Planned at**: dev `82ab168`，2026-10-26
- **owner 裁定（2026-10-26，原话见下）**：阻塞派发的子代理撞上用量上限时，续跑**交给主代理**；subagent_send 后台派发、
  用户在子代理视图里发起的执行、自动续跑本身仍按计划 011。

## Why this matters

owner 的原话：

> subagent任务执行失败的原因和已经执行的时长都应该给到primary agent，否则继续会话时，primary agent会错误的判断失败的subagent什么都没做，
> 其实截图中所示的subagent已经执行了12分钟以上，已经修改了一些代码，primary agent不应该派发一个新的subagent从头开始，而是应该继续前面这个失败的subagent

实际发生的事：一个 `subagent_run` 派发的子代理跑了 12m34s、做了 127 次工具调用、改了代码，然后撞上服务商的周/月用量上限（429）。
主代理拿到的工具结果只有一句原始报错：

```
Error executing tool 'subagent_run': tool subagent_run: error, status code: 429, status: 429 Too Many Requests, message: {"error":{"code":"1310","message":"您已达到每周/每月使用上限…"}}
```

没有 task_id、没有运行时长、也没说它的工作还在。用户说"额度已恢复，请继续"后，主代理的推理是
"the executor never started — 429 on dispatch … Clean dispatch now"，于是派了一个新子代理从头做。

还有第二个隐患（本计划一并修）：按计划 011，**每一次**子代理执行撞上用量上限都会给子代理自己排一个"额度恢复后自动续跑"
（按它的 worker session 计），而主代理自己的回合通常也撞上同一个上限、也排了一个。到点两边一起续跑：子代理在自己的视图里接着干、
结果不回主代理（011 的 D3）；主代理拿着那句原始报错重派新子代理——两个代理同时改同一批文件。在终端里用户在主输入框打字只取消主代理那一个，
子代理那个仍挂着。owner 的裁定：被主代理**阻塞等待**的执行，失败归主代理处理，子代理不再自己排续跑。

## Current state

### 1. 主代理现在拿到什么

- `pkg/run/subagent.go` `registerSubagentTool` 里的 `subagent_run` 处理函数（约 `:343-410`）：
  ```go
  taskID := newSubagentTaskID()
  execRes, err := execGeneralSubagent(ctx, fac, taskID, title, task, subType)
  if err != nil {
      return "", err
  }
  ```
- `pkg/run/orchestration_llm.go` 把工具错误包两层再给模型：`executeToolHandler`（约 `:2034-2045`）
  `fmt.Errorf("tool %s: %w", name, err)`；`toolResultMessage`（约 `:2082-2090`）
  `fmt.Sprintf("Error executing tool '%s': %v", result.call.Function.Name, result.err)`。**这两处不改**。
  工具错误只有 `RequiresActionError`（审批闸门）会中止回合（`isRequiresActionError(result.err)`），其他错误只是一条失败的工具结果。
- `execGeneralSubagent`（约 `:1665-1711`）调 `dispatchSubagent`，出错时 `return subagentExecResult{}, err`，原样上抛。
  它在 `run` 回调里调 `runSubagentExecution(execCtx, fac, prep.entry, false, Input{}, …)`（约 `:1683`）。
- `dispatchSubagent`（约 `:3146-3182`）：
  ```go
  prep, handle, err = startSubagent(ctx, fac, d)
  if err != nil {
      return preparedSubagent{}, "", err
  }
  defer prep.cancel()
  defer prep.detach()
  defer func() { … panic → finishSubagent(…, panicErr) … }()

  output, runErr := d.run(ctx, fac, prep)
  final := finishSubagent(fac, d, prep, handle, output, runErr)
  if runErr != nil {
      return prep, "", runErr
  }
  return prep, final.Output, nil
  ```
  调用方有三个：`execGeneralSubagent`（subagent_run / subagent_fanout）、计划审阅（约 `:3567`）、`/goal` 检查 `runGoalCheck`（约 `:3595`）。
  后两个只把 `err` 原样上抛——**它们看到的 `err.Error()` 必须保持不变**。
- `finishSubagent`（约 `:3112-3141`）决定终态：`runErr == nil` → ok；`context.Canceled` → cancelled；其余 → failed，
  `final.Error = llm.ExplainError(runErr)`（"描述过"的失败原因，子代理卡任务行、roster、子代理自己的视图显示的就是它）。
  `final` 是 `agent.HistoryEntry`，带 `TaskID`、`Title`、`AgentType`、`OneShot`、`Continuable` 等。
- 子代理失败时它已做的工作**是保留的**：失败的执行会把它截至失败的对话写进 worker session
  （`pkg/process/one_shot.go` `PersistSubagentTurn` 的 `PersistCancelledTurn` 分支），`subagent_continue` 正是在这段对话上接着跑。
- `subagent_continue` 处理函数（约 `:2919-2933`）调 `continueSubagentExecution`，出错 `return "", err`。
  `continueSubagentExecution`（约 `:2371-2511`）：开头拒绝"one-shot 且不可续"（`record.OneShot && !record.Continuable`），
  约 `:2451` `notifySubagentSpawnedDirect(fac, lifecycle, "")`，约 `:2471` `runSubagentExecution(hostCtx, fac, lifecycle, false, Input{}, …)`，
  结尾：
  ```go
  default:
      final.Status = agent.StatusFailed
      final.Error = llm.ExplainError(runErr)
  }
  …
  if runErr != nil {
      return final, runErr
  }
  return final, nil
  ```
- `subagent_fanout`（`registerFanoutTool`，约 `:2637-2775`）每个任务在 goroutine 里调 `execGeneralSubagent`，失败时：
  ```go
  results[i] = FanoutResult{Index: i, Task: task, SubagentType: subType, Error: err.Error(), OK: false}
  ```
  `fail_fast` 时（约 `:2745`）`return buildFanoutSummary(results), firstErr`——**带错误返回时模型只看到错误文本，汇总 JSON 不会给模型**
  （`executeToolHandler` 出错时不产出内容部件）。`FanoutResult`（约 `:275-282`）：
  ```go
  type FanoutResult struct {
      Index        int    `json:"index"`
      Task         string `json:"task"`
      SubagentType string `json:"subagent_type,omitempty"`
      Output       string `json:"output,omitempty"`
      Error        string `json:"error,omitempty"`
      OK           bool   `json:"ok"`
  }
  ```

### 2. 子代理自己的自动续跑（计划 011）

- `runSubagentExecution`（约 `:848-901`）是所有子代理执行的必经之路，结束时调
  `reportSubagentExecutionEnded(fac, record, err)`（两处：约 `:890` 撤回分支、`:897` 正常分支）。
- `reportSubagentExecutionEnded`（约 `:917-930`）构造 `run.SubagentExecutionEnd{ConversationSessionID, WorkerSessionID, AgentKey, RunID, Err}`
  交给 `SubagentExecutor.SubagentExecutionEnded`。`run.SubagentExecutionEnd` 定义在约 `:3468-3486`。
- `runSubagentExecution` 的 6 个调用点：`:1683` execGeneralSubagent（**阻塞**）、`:1743` spawnAsyncSubagent（后台，subagent_send）、
  `:2198` 用户在子代理视图发消息、`:2471` continueSubagentExecution（**阻塞**，subagent_continue）、`:3603` /goal 检查、`:3625` 计划审阅。
- 组合根转发：`pkg/process/one_shot.go` `SubagentExecutionEnded` 原样把 `run.SubagentExecutionEnd` 交给 surface 登记的回调；
  surface 再逐字段抄成 `turn.SubagentExecutionEnd`：
  - `pkg/tui/notify.go` 约 `:1676-1690`：
    ```go
    Ended: func(end run.SubagentExecutionEnd) {
        s.Core.SubagentExecutionEnded(context.Background(), turn.SubagentExecutionEnd{
            ConversationSessionID: end.ConversationSessionID,
            WorkerSessionID:       end.WorkerSessionID,
            AgentKey:              end.AgentKey,
            RunID:                 end.RunID,
            Origin:                turn.Origin{Surface: turn.SurfaceTUI},
            Err:                   end.Err,
        })
    },
    ```
  - `pkg/gateway/serve_run.go` 约 `:172-186`：同样的一段，`Origin: turn.Origin{Surface: turn.SurfaceWebChat}`。
- `pkg/turn/submit.go`：`SubagentExecutionEnd` 结构（约 `:393-413`）；`(*autoContinuer).subagentExecutionEnded`（约 `:548-560`）：
  ```go
  func (a *autoContinuer) subagentExecutionEnded(ctx context.Context, end SubagentExecutionEnd) {
      if a == nil {
          return
      }
      plan, ok := a.continuationFor(strings.TrimSpace(end.ConversationSessionID), end.RunID, end.Origin, end.Err)
      if !ok {
          a.closeRun(strings.TrimSpace(end.WorkerSessionID))
          return
      }
      plan.AgentKey = strings.TrimSpace(end.AgentKey)
      plan.WorkerSessionID = strings.TrimSpace(end.WorkerSessionID)
      a.arm(ctx, plan.key(), plan)
  }
  ```
- 设计文档 `docs/plan/SUBAGENT_CONVERSATION/011-subagent-auto-continue.md` 的"语义"表第一行写的是
  "subagent 的一次执行（首次派发、模型 `subagent_continue`、用户发起、自动继续本身）以同样的错误失败 → 安排它自己的继续"。本计划收窄它。

### 3. 工具与约定

- `tool.FormatDuration(d)`（`pkg/tool/display_header.go` 约 `:261`）："1m23s" / "12s" / "1.2s" / "<0.1s"，`d<=0` 返回空串——卡片上的时长就是它画的。
  `pkg/run/subagent.go` 已经 import 了 `pkg/tool`（包名 `tool`）、`pkg/llm`、`errors`、`time`。
- `llm.ExplainError(err)` 给描述过的一句话；`llm.Explain(err)` 返回 `(ErrorExplanation, bool)`，`ErrorExplanation.ResetAt` 是恢复时间
  （用 `errors.As` 找 `*llm.APIError`，所以包一层带 `Unwrap` 的错误不影响它）。
- `isRequiresActionError(err)` 在 `pkg/run` 里已有（fanout 用它）。
- 写给模型的运行时文本是常量、字节稳定、英文（参见 `pkg/turn/submit.go` 里 `AutoContinuePrompt` 的注释）。
- 测试范例：
  - `pkg/run/subagent_test.go` `TestSubagentToolResultsKeepTheirShape`（约 `:1451`）：建 `Runner{Deps: &Deps{Home, RunRT, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: &fixedSubagentExecutor{output: "done"}, Control: NewController(), Events: notifier}`，
    `RegisterSubagentTool(a, Factory{Home: home, Owner: owner})`，`aToolByName(a, "subagent_send")`，`tool.Handle(callCtx, args)`。
  - `fixedSubagentExecutor`（约 `:1600`）的 `PersistSubagentTurn` / `SubagentExecutionStarting` / `SubagentExecutionEnded` 都是指针接收者的空实现（约 `:3241`、`:3258`）。
  - `TestEverySubagentExecutionIsReportedToItsScheduler`（约 `:4895`）：录下每次执行的 `SubagentExecutionEnd`。
  - `pkg/turn/submit_test.go`：`newAutoContinueHarness`、`quotaError(resetAt, plan)`（约 `:460`）、`h.endSubagent(...)`（约 `:764`）、
    `TestSubagentUsageLimitArmsItsOwnContinuation`（约 `:811`）、`h.svc.PendingAutoContinue(key)`、`h.svc.PendingAutoContinuePlans("s1")`、`h.continuations()`。
- 代码注释：英文、讲"为什么"，与周围一致。

## 要做成的样子（目标行为）

1. `subagent_run` / `subagent_continue` 的子代理**启动后失败**（不是被取消、不是审批闸门）时，模型读到的是（示例，`error` 行是 `llm.ExplainError` 的结果，换行压成空格）：

   ```
   Error executing tool 'subagent_run': tool subagent_run: The subagent started and then failed before it finished its task.
   task_id: subagent-3f1c…
   subagent_type: general-purpose
   title: 执行计划 137 shell 正文
   ran_for: 12m34s
   error: 您已达到每周/每月使用上限，您的限额将在 2026-11-01 06:09:05 重置。 (HTTP 429 Too Many Requests)
   resets_at: 2026-10-31T22:09:05Z
   Its conversation and every change it already made (file edits, commands it ran) are kept.
   To finish the task, call subagent_continue with this task_id and a message saying what is left to do. Do not dispatch a new subagent for it: a new one starts from nothing and would redo or overwrite this one's work. Continuing before resets_at fails the same way.
   ```

   - `title` 为空时整行省略；`resets_at` 只在 `llm.Explain` 给出恢复时间时出现（UTC，RFC3339），最后那句 "Continuing before resets_at …" 也随之出现。
   - 不可续的类型（`OneShot && !Continuable`）把最后两句换成：
     `The changes it already made (file edits, commands it ran) are kept.` 和
     `Its subagent_type is one-shot and cannot be continued. Check the workspace for the changes it already made before dispatching the task again.`
   - 失败发生在启动之前（类型解析失败、建 run 失败）、被取消、或是审批闸门：**一切照旧**。
2. `subagent_fanout` 里每个"启动后失败"的任务，结果 JSON 多 `task_id`、`ran_for`、`resets_at`（有才写）、`next`（上面那句建议），
   `error` 改为描述过的那一句（不再是原始报错）。`fail_fast` 因此类失败返回错误时，错误文本就是第 1 条那段说明。
3. `dispatchSubagent` 的其他调用方（计划审阅、/goal 检查）看到的 `err.Error()` 与今天**逐字相同**。
4. 被阻塞等待的执行（`execGeneralSubagent` 与 `continueSubagentExecution` 发起的）撞上用量上限时，**不再**给子代理自己排续跑；
   后台派发（subagent_send）、用户发起、自动续跑本身照旧排。

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^<名字>$' -count=1 -v` | 看到 `--- PASS: <名字>`（**必须看到 PASS 行**：`-run` 写错只报 `no tests to run` 且 exit 0） |
| turn 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run '^<名字>$' -count=1 -v` | 同上 |
| 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/process ./pkg/gateway ./pkg/tui -count=1` | 与基线相比无新增 FAIL |
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/turn ./pkg/tui ./pkg/gateway` | exit 0 |
| gofmt | `gofmt -l pkg/run pkg/turn pkg/tui pkg/gateway` | 无输出 |
| Python 语法 | `python3 -m py_compile .claude/skills/run-forebrain/fake_provider.py` | exit 0 |

## Scope

**In scope（只改这些）**：
- `pkg/run/subagent.go`
- `pkg/run/subagent_test.go`
- `pkg/turn/submit.go`
- `pkg/turn/submit_test.go`
- `pkg/tui/notify.go`（只加一行字段映射）
- `pkg/gateway/serve_run.go`（只加一行字段映射）
- `.claude/skills/run-forebrain/fake_provider.py`、`.claude/skills/run-forebrain/SKILL.md`、`.claude/skills/run-forebrain/driver.sh`（只改 `subagent-limit` 的说明与一个环境变量开关）
- `docs/plan/SUBAGENT_CONVERSATION/011-subagent-auto-continue.md`（加一条带日期的修订说明）

**Out of scope（看着相关也不要动）**：
- `pkg/run/orchestration_llm.go` 的两层包装（`tool %s:` / `Error executing tool`）——全工具通用。
- 工具描述（`subagent_run` / `subagent_continue` 的 description 与 jsonschema）——改了会让所有会话的缓存前缀失效一次，本计划不需要。
- 计划审阅与 `/goal` 检查的派发、它们的自动续跑行为。
- `subagent_send` 的结果形状；`subagent_run` 成功时的结果形状（`TestSubagentToolResultsKeepTheirShape` 钉着）。
- 任何 UI（139 已处理）；`AutoContinuePrompt` 的文字。
- 不要试图统计"子代理做了多少次工具调用"：引擎侧没有单一来源（fork 子代理的 worker session 里还带着继承来的父对话前缀，按对话计数会虚高），
  owner 要的是"原因 + 时长"，本计划不加这个数。

## Steps

### Step 1：钉住"引擎内部调用方看到的错误不变"

`pkg/run/subagent_test.go` 里已有 `TestDispatchSubagentReportsFailureAndStillClosesTheRow`（约 `:114`），断言
`err.Error()` 含 `upstream 429`。**不要改它**；它在 Step 2 之后必须仍然通过——这是"计划审阅 / goal 检查读到的文本不变"的保证。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestDispatchSubagentReportsFailureAndStillClosesTheRow$' -count=1 -v` → `--- PASS`。

### Step 2：`subagentFailure`——启动后失败的执行，带着记录与时长

在 `pkg/run/subagent.go` 里 `finishSubagent` 的正上方新增：

```go
// subagentFailure is a subagent execution that started and then failed, as
// the call that awaited it receives it: the execution's record as it ended,
// how long it ran, and the error it stopped on. Error() is that error's own
// text, so a caller that only passes it on — plan review, the goal check —
// reads what it always read; the dispatching tools turn it into the account
// the model needs to pick the task up again (subagentFailureForModel).
type subagentFailure struct {
	record  agent.HistoryEntry
	elapsed time.Duration
	err     error
}

func (f *subagentFailure) Error() string { return f.err.Error() }

func (f *subagentFailure) Unwrap() error { return f.err }
```

改 `dispatchSubagent`：

1. 在 `prep, handle, err = startSubagent(ctx, fac, d)` 的错误检查**之后**、`defer prep.cancel()` 之前加：
   ```go
   	// The execution's clock starts where its card row's does: at the spawned
   	// notification startSubagent has just published.
   	started := time.Now()
   ```
2. 把结尾的
   ```go
   	if runErr != nil {
   		return prep, "", runErr
   	}
   ```
   改为
   ```go
   	if runErr != nil {
   		// A gate is a suspension, not a failure, and a cancelled execution was
   		// stopped on purpose: both travel on as they are.
   		if final.Status == agent.StatusFailed && !isRequiresActionError(runErr) {
   			return prep, "", &subagentFailure{record: final, elapsed: time.Since(started), err: runErr}
   		}
   		return prep, "", runErr
   	}
   ```

在 `TestDispatchSubagentReportsFailureAndStillClosesTheRow` 末尾追加断言（只加，不改原有断言）：

```go
	var failed *subagentFailure
	if !errors.As(err, &failed) || failed.record.TaskID != "task-fail" || failed.record.Status != agent.StatusFailed {
		t.Fatalf("err = %#v, want the failed execution's record carried with it", err)
	}
```

**Verify**：Step 1 的命令 → `--- PASS`；`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0。

### Step 3：写给主代理的那段说明

在 `subagentFailure` 之后新增（常量与函数照抄；注释可以按周围风格微调措辞，**常量字符串一个字节都不要改**）：

```go
// What a dispatching agent reads when a subagent it awaited failed partway.
// It is runtime text that reaches the model, so it is constant and
// byte-stable.
const (
	subagentFailedLead     = "The subagent started and then failed before it finished its task."
	subagentWorkKept       = "Its conversation and every change it already made (file edits, commands it ran) are kept."
	subagentChangesKept    = "The changes it already made (file edits, commands it ran) are kept."
	subagentContinueAdvice = "To finish the task, call subagent_continue with this task_id and a message saying what is left to do. Do not dispatch a new subagent for it: a new one starts from nothing and would redo or overwrite this one's work."
	subagentResetAdvice    = "Continuing before resets_at fails the same way."
	subagentOneShotAdvice  = "Its subagent_type is one-shot and cannot be continued. Check the workspace for the changes it already made before dispatching the task again."
)

// reason is the failure as every surface describes it, on one line.
func (f *subagentFailure) reason() string {
	return strings.Join(strings.Fields(llm.ExplainError(f.err)), " ")
}

// resetsAt is when the provider said its limit resets, empty when it did not.
func (f *subagentFailure) resetsAt() string {
	if explanation, ok := llm.Explain(f.err); ok && !explanation.ResetAt.IsZero() {
		return explanation.ResetAt.UTC().Format(time.RFC3339)
	}
	return ""
}

// continuable mirrors the refusal in continueSubagentExecution.
func (f *subagentFailure) continuable() bool {
	return !(f.record.OneShot && !f.record.Continuable)
}

// advice is what the dispatching agent should do next.
func (f *subagentFailure) advice() string {
	if !f.continuable() {
		return subagentOneShotAdvice
	}
	if f.resetsAt() != "" {
		return subagentContinueAdvice + " " + subagentResetAdvice
	}
	return subagentContinueAdvice
}

// subagentFailureForModel is the account a dispatching agent receives when a
// subagent it awaited started and then failed: which task, how long it ran,
// why it stopped, and how to pick it up. With only the provider's raw error
// the agent concluded the subagent never ran, and dispatched a fresh one that
// redid — and overwrote — the work this one had done.
func subagentFailureForModel(f *subagentFailure) string {
	var b strings.Builder
	b.WriteString(subagentFailedLead)
	line := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			b.WriteString("\n" + key + ": " + value)
		}
	}
	line("task_id", f.record.TaskID)
	line("subagent_type", f.record.AgentType)
	line("title", f.record.Title)
	line("ran_for", tool.FormatDuration(f.elapsed))
	line("error", f.reason())
	line("resets_at", f.resetsAt())
	if f.continuable() {
		b.WriteString("\n" + subagentWorkKept)
	} else {
		b.WriteString("\n" + subagentChangesKept)
	}
	b.WriteString("\n" + f.advice())
	return b.String()
}
```

（若 `tool` 在这个位置被局部变量遮蔽导致编译错误，STOP 3——不要改包的 import 名。）

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0。

### Step 4：subagent_run 与 subagent_continue 把说明交给模型

1. `subagent_run` 处理函数里：
   ```go
   execRes, err := execGeneralSubagent(ctx, fac, taskID, title, task, subType)
   if err != nil {
       return "", err
   }
   ```
   改为
   ```go
   execRes, err := execGeneralSubagent(ctx, fac, taskID, title, task, subType)
   if err != nil {
       var failed *subagentFailure
       if errors.As(err, &failed) {
           return "", errors.New(subagentFailureForModel(failed))
       }
       return "", err
   }
   ```
2. `continueSubagentExecution`：
   - 在 `notifySubagentSpawnedDirect(fac, lifecycle, "")`（约 `:2451`）**之前**一行加：
     ```go
     	// The continuation's clock starts where its card row's does: at the
     	// spawned notification below.
     	started := time.Now()
     ```
   - 结尾的
     ```go
     	if runErr != nil {
     		return final, runErr
     	}
     ```
     改为
     ```go
     	if runErr != nil {
     		// A gate is a suspension and a cancellation was asked for; only an
     		// execution that ran and failed is the dispatcher's to pick up.
     		if !isRequiresActionError(runErr) && !errors.Is(runErr, context.Canceled) {
     			return final, &subagentFailure{record: final, elapsed: time.Since(started), err: runErr}
     		}
     		return final, runErr
     	}
     ```
3. `subagent_continue` 处理函数里：
   ```go
   record, err := continueSubagentExecution(ctx, fac, in)
   if err != nil {
       return "", err
   }
   ```
   改为
   ```go
   record, err := continueSubagentExecution(ctx, fac, in)
   if err != nil {
       var failed *subagentFailure
       if errors.As(err, &failed) {
           return "", errors.New(subagentFailureForModel(failed))
       }
       return "", err
   }
   ```

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestSubagentToolResultsKeepTheirShape$' -count=1 -v` → `--- PASS`（成功路径形状不变）。

### Step 5：subagent_fanout 的每个任务也带上同样的事实

1. `FanoutResult` 加四个字段（放在 `OK` 之后）：
   ```go
   	// TaskID, RanFor, ResetsAt and Next are set when the task's subagent
   	// started and then failed: the account subagent_run gives, so the
   	// dispatching agent continues that subagent instead of starting over.
   	TaskID   string `json:"task_id,omitempty"`
   	RanFor   string `json:"ran_for,omitempty"`
   	ResetsAt string `json:"resets_at,omitempty"`
   	Next     string `json:"next,omitempty"`
   ```
2. fanout goroutine 里的
   ```go
   results[i] = FanoutResult{Index: i, Task: task, SubagentType: subType, Error: err.Error(), OK: false}
   return
   ```
   改为
   ```go
   var failed *subagentFailure
   if errors.As(err, &failed) {
       results[i] = FanoutResult{
           Index: i, Task: task, SubagentType: subType, OK: false,
           Error:    failed.reason(),
           TaskID:   failed.record.TaskID,
           RanFor:   tool.FormatDuration(failed.elapsed),
           ResetsAt: failed.resetsAt(),
           Next:     failed.advice(),
       }
       return
   }
   results[i] = FanoutResult{Index: i, Task: task, SubagentType: subType, Error: err.Error(), OK: false}
   return
   ```
   （`mu.Lock()` … `mu.Unlock()` 那段与 `firstErr` / `suspended` 的处理**不动**，只改给 `results[i]` 赋值这一句。）
3. `fail_fast` 的返回：
   ```go
   if in.FailFast && firstErr != nil {
       return buildFanoutSummary(results), firstErr
   }
   ```
   改为
   ```go
   if in.FailFast && firstErr != nil {
       var failed *subagentFailure
       if errors.As(firstErr, &failed) {
           return buildFanoutSummary(results), errors.New(subagentFailureForModel(failed))
       }
       return buildFanoutSummary(results), firstErr
   }
   ```

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → exit 0。

### Step 6：被阻塞等待的执行不给子代理自己排续跑

`pkg/run/subagent.go`：

1. 在 `subagentTaskIndexKey` 那组（约 `:3092`）旁边新增：
   ```go
   type subagentAwaitedKey struct{}

   // withSubagentAwaited marks an execution a blocking tool call of the
   // dispatching agent is waiting for: subagent_run, subagent_fanout,
   // subagent_continue. That agent receives the execution's failure as the
   // call's result and continues the subagent itself, so a usage limit that
   // stops the execution arms no continuation of the subagent's own (owner,
   // 2026-10-26): one would resume the subagent beside the agent about to
   // continue it, and its result would reach no one.
   func withSubagentAwaited(ctx context.Context) context.Context {
   	return context.WithValue(ctx, subagentAwaitedKey{}, true)
   }

   func subagentAwaitedFromContext(ctx context.Context) bool {
   	awaited, _ := ctx.Value(subagentAwaitedKey{}).(bool)
   	return awaited
   }
   ```
2. `execGeneralSubagent` 里 `return runSubagentExecution(execCtx, fac, prep.entry, false, Input{}, func(execCtx context.Context) (string, error) {`
   改为 `return runSubagentExecution(withSubagentAwaited(execCtx), fac, prep.entry, false, Input{}, func(execCtx context.Context) (string, error) {`。
3. `continueSubagentExecution` 里 `outText, runErr := runSubagentExecution(hostCtx, fac, lifecycle, false, Input{}, func(execCtx context.Context) (string, error) {`
   改为 `outText, runErr := runSubagentExecution(withSubagentAwaited(hostCtx), fac, lifecycle, false, Input{}, func(execCtx context.Context) (string, error) {`。
   **其余 4 个调用点不加**（后台派发、用户发起、/goal、计划审阅）。
4. `runSubagentExecution` 函数体第一行加 `awaited := subagentAwaitedFromContext(ctx)`；两处
   `reportSubagentExecutionEnded(fac, record, …)` 都改成多传一个 `awaited`。
5. `reportSubagentExecutionEnded` 签名改为 `func reportSubagentExecutionEnded(fac Factory, record agent.HistoryEntry, err error, awaited bool)`，
   构造的 `SubagentExecutionEnd` 加 `AwaitedByDispatcher: awaited,`；注释补一句 awaited 的含义。
6. `run.SubagentExecutionEnd` 结构（约 `:3472`）在 `Err` 之后加：
   ```go
   	// AwaitedByDispatcher is true when a blocking call of the dispatching
   	// agent was waiting for this execution (subagent_run, subagent_fanout,
   	// subagent_continue). That agent receives the failure and owns the
   	// continuation, so the scheduler arms none for the subagent itself.
   	AwaitedByDispatcher bool
   ```

`pkg/turn/submit.go`：

7. `turn.SubagentExecutionEnd`（约 `:397`）在 `Err` 之后加同样的字段与注释。
8. `(*autoContinuer).subagentExecutionEnded` 在 `if a == nil { return }` 之后加：
   ```go
   	if end.AwaitedByDispatcher {
   		// The agent that dispatched and awaited this execution was handed its
   		// failure and continues the subagent itself; a continuation of the
   		// subagent's own would resume it beside that one.
   		a.closeRun(strings.TrimSpace(end.WorkerSessionID))
   		return
   	}
   ```
9. `(*Service).SubagentExecutionEnded` 的注释（约 `:425-428`）补半句："… except for an execution its dispatching agent awaited, whose failure that agent continues."

surface 映射（各加一行）：

10. `pkg/tui/notify.go` 那段 `turn.SubagentExecutionEnd{…}` 里 `Err: end.Err,` 之后加 `AwaitedByDispatcher: end.AwaitedByDispatcher,`。
11. `pkg/gateway/serve_run.go` 同样加一行。

**Verify**：
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `grep -rn "turn.SubagentExecutionEnd{" pkg --include='*.go' | grep -v _test` → 恰好 2 条（notify.go、serve_run.go），且两处都已加 `AwaitedByDispatcher`
  （若多于 2 条，说明有第三个 surface 也在转发：STOP 4）。
- `gofmt -l pkg/run pkg/turn pkg/tui pkg/gateway` → 无输出

### Step 7：测试

**7a. `pkg/turn/submit_test.go`**，在 `TestSubagentUsageLimitArmsItsOwnContinuation` 之后新增：

```go
// An execution its dispatching agent awaited reports its failure to that
// agent, which continues the subagent itself: the scheduler arms nothing for
// the subagent, and the conversation's own plans are untouched.
func TestAwaitedSubagentUsageLimitArmsNoContinuation(t *testing.T) {
	h := newAutoContinueHarness(t)
	resetAt := h.clock.Now().Add(3 * time.Hour)
	h.svc.SubagentExecutionEnded(context.Background(), SubagentExecutionEnd{
		ConversationSessionID: "s1",
		WorkerSessionID:       "worker-a",
		AgentKey:              "task-a",
		RunID:                 "subrun-worker-a",
		Origin:                Origin{Surface: SurfaceTUI},
		Err:                   quotaError(resetAt, "pro"),
		AwaitedByDispatcher:   true,
	})
	if plan, ok := h.svc.PendingAutoContinue("worker-a"); ok {
		t.Fatalf("an awaited subagent armed its own continuation: %+v", plan)
	}
	if plans := h.svc.PendingAutoContinuePlans("s1"); len(plans) != 0 {
		t.Fatalf("pending plans = %+v, want none", plans)
	}
	h.clock.Advance(3*time.Hour + defaultAutoContinueGrace)
	if got := h.continuations(); len(got) != 0 {
		t.Fatalf("continuations = %+v, want none", got)
	}
}
```

**7b. `pkg/run/subagent_test.go`**，扩展 `TestEverySubagentExecutionIsReportedToItsScheduler`：在最后那个 `for i, end := range ends` 循环之后追加：

```go
	// The model's dispatch and its continuation were awaited by the call that
	// made them; the user's own message was not.
	if !ends[0].AwaitedByDispatcher || !ends[1].AwaitedByDispatcher || ends[2].AwaitedByDispatcher {
		t.Fatalf("awaited = %v %v %v, want true true false",
			ends[0].AwaitedByDispatcher, ends[1].AwaitedByDispatcher, ends[2].AwaitedByDispatcher)
	}
```

**7c. `pkg/run/subagent_test.go`**，在 `TestSubagentToolResultsKeepTheirShape` 之后新增一个失败执行器和两个测试。执行器：

```go
// failingSubagentExecutor is the fixed executor whose next runs fail with the
// queued errors, the way a provider refusal stops a real subagent; once the
// queue is empty its runs succeed.
type failingSubagentExecutor struct {
	*fixedSubagentExecutor
	mu   sync.Mutex
	errs []error
}

func (f *failingSubagentExecutor) RunSubagentExec(ctx context.Context, req SubagentExecRequest) (string, error) {
	f.mu.Lock()
	var err error
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	f.mu.Unlock()
	if err != nil {
		return "", err
	}
	return f.fixedSubagentExecutor.RunSubagentExec(ctx, req)
}

func (f *failingSubagentExecutor) failNext(errs ...error) {
	f.mu.Lock()
	f.errs = append(f.errs, errs...)
	f.mu.Unlock()
}
```

一个共用的搭建函数（照 `TestSubagentToolResultsKeepTheirShape` 的前半段，只把执行器换掉、去掉预置的历史记录）：

```go
func newFailingSubagentTools(t *testing.T) (*agent.Agent, *failingSubagentExecutor, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("OpenStateForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runSvc := &state.RunStore{DB: db}
	ensureRunSessions(t, db, "session-1")
	parent, err := runSvc.CreateRun(ctx, "session-1", "parent")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	home := t.TempDir()
	exec := &failingSubagentExecutor{fixedSubagentExecutor: &fixedSubagentExecutor{output: "probe answered"}}
	owner := &Runner{Deps: &Deps{Home: home, RunRT: runSvc, AppCfg: subagentsEnabledConfig()}, SubagentExecutor: exec, Control: NewController(), Events: &collectingUINotify{}}
	a, err := agent.New(noopLLM{}, "main", "main")
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if err := RegisterSubagentTool(a, Factory{Home: home, Owner: owner}); err != nil {
		t.Fatalf("RegisterSubagentTool: %v", err)
	}
	return a, exec, toolpkg.WithRunID(llm.WithAgentSessionID(ctx, "session-1"), parent.ID)
}

// usageLimitError is a provider's spent-allowance refusal with a known reset.
func usageLimitError(resetAt time.Time) error {
	return &llm.APIError{
		StatusCode: 429,
		Message:    "您已达到每周/每月使用上限，您的限额将在 2026-11-01 06:09:05 重置。",
		RateLimit:  &llm.RateLimit{Quota: true, Reason: "usage_limit_reached", ResetAt: resetAt},
	}
}
```

测试一：subagent_run 失败 → 文本齐全 → 用文本里的 task_id 续跑：第一次续跑再失败（也得到同样的说明）、第二次成功。

```go
// A subagent that ran and then failed is handed to the agent that awaited it
// as an account it can act on — which task, how long it ran, why it stopped —
// and the task_id in that account is the one subagent_continue resumes.
func TestSubagentRunTellsTheDispatcherHowItsSubagentFailed(t *testing.T) {
	a, exec, callCtx := newFailingSubagentTools(t)
	resetAt := time.Date(2026, 10, 31, 22, 9, 5, 0, time.UTC)
	limit := usageLimitError(resetAt)
	exec.failNext(limit)

	runTool, ok := aToolByName(a, "subagent_run")
	if !ok {
		t.Fatal("subagent_run not registered")
	}
	_, err := runTool.Handle(callCtx, `{"title":"Probe","task":"answer briefly","subagent_type":"general-purpose"}`)
	if err == nil {
		t.Fatal("subagent_run succeeded, want the failure reported")
	}
	text := err.Error()
	for _, want := range []string{
		subagentFailedLead,
		"\nsubagent_type: general-purpose\n",
		"\ntitle: Probe\n",
		"\nran_for: ",
		"\nerror: " + strings.Join(strings.Fields(llm.ExplainError(limit)), " ") + "\n",
		"\nresets_at: 2026-10-31T22:09:05Z\n",
		subagentWorkKept,
		subagentContinueAdvice + " " + subagentResetAdvice,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the dispatcher's account lacks %q:\n%s", want, text)
		}
	}
	m := regexp.MustCompile(`\ntask_id: (subagent-[0-9a-f-]+)\n`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("the dispatcher's account names no task_id:\n%s", text)
	}
	taskID := m[1]

	contTool, ok := aToolByName(a, "subagent_continue")
	if !ok {
		t.Fatal("subagent_continue not registered")
	}
	// A continuation that fails again is accounted for the same way, under
	// the same task.
	exec.failNext(limit)
	_, err = contTool.Handle(callCtx, `{"task_id":"`+taskID+`","message":"keep going"}`)
	if err == nil || !strings.Contains(err.Error(), subagentFailedLead) ||
		!strings.Contains(err.Error(), "\ntask_id: "+taskID+"\n") {
		t.Fatalf("failed continuation = %v, want the same account for %s", err, taskID)
	}
	// Once the limit is gone, the same task_id picks the subagent up.
	out, err := contTool.Handle(callCtx, `{"task_id":"`+taskID+`","message":"keep going"}`)
	if err != nil {
		t.Fatalf("continuing the failed subagent: %v", err)
	}
	if !strings.Contains(out.(string), "probe answered") {
		t.Fatalf("continuation result = %s, want the subagent's answer", out)
	}
}
```

测试二：fanout 的每任务结果与 fail_fast。

```go
// subagent_fanout gives each task that ran and failed the same facts, and a
// fail_fast stop on such a task is that task's account.
func TestFanoutTellsTheDispatcherHowEachSubagentFailed(t *testing.T) {
	a, exec, callCtx := newFailingSubagentTools(t)
	resetAt := time.Date(2026, 10, 31, 22, 9, 5, 0, time.UTC)
	limit := usageLimitError(resetAt)
	fanTool, ok := aToolByName(a, "subagent_fanout")
	if !ok {
		t.Fatal("subagent_fanout not registered")
	}
	args := `{"tasks":[{"title":"Probe","prompt":"answer briefly","subagent_type":"general-purpose"}],"max_parallel":1}`

	exec.failNext(limit)
	out, err := fanTool.Handle(callCtx, args)
	if err != nil {
		t.Fatalf("subagent_fanout: %v", err)
	}
	var summary struct {
		Results []FanoutResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out.(string)), &summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(summary.Results) != 1 {
		t.Fatalf("results = %+v", summary.Results)
	}
	got := summary.Results[0]
	if got.OK || got.Error != strings.Join(strings.Fields(llm.ExplainError(limit)), " ") ||
		!strings.HasPrefix(got.TaskID, "subagent-") || got.RanFor == "" ||
		got.ResetsAt != "2026-10-31T22:09:05Z" || got.Next != subagentContinueAdvice+" "+subagentResetAdvice {
		t.Fatalf("failed task result = %+v", got)
	}

	exec.failNext(limit)
	_, err = fanTool.Handle(callCtx, `{"tasks":[{"title":"Probe","prompt":"answer briefly","subagent_type":"general-purpose"}],"max_parallel":1,"fail_fast":true}`)
	if err == nil || !strings.Contains(err.Error(), subagentFailedLead) {
		t.Fatalf("fail_fast error = %v, want the failed task's account", err)
	}
}
```

（需要的 import：`regexp`，以及文件里已有的 `encoding/json`、`strings`、`sync`、`time`、`path/filepath`；缺什么按编译错误补。
`ensureRunSessions`、`subagentsEnabledConfig`、`collectingUINotify`、`noopLLM`、`toolpkg` 都是本测试文件已有的名字——若某个名字不存在，
按 `TestSubagentToolResultsKeepTheirShape` 实际用的写法改，**不要**改断言。若 `subagentsEnabledConfig()` 给出的
`max_parallel_subagents` 为 0 导致 fanout 拒绝，STOP 3。）

**Verify**：
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run '^(TestAwaitedSubagentUsageLimitArmsNoContinuation|TestSubagentUsageLimitArmsItsOwnContinuation|TestSubagentContinuationIsSupersededByItsNextExecution|TestPrimaryAutoContinueIsUnchangedBySubagentPlans)$' -count=1 -v` → 4 个 `--- PASS`
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^(TestEverySubagentExecutionIsReportedToItsScheduler|TestSubagentRunTellsTheDispatcherHowItsSubagentFailed|TestFanoutTellsTheDispatcherHowEachSubagentFailed|TestDispatchSubagentReportsFailureAndStillClosesTheRow|TestSubagentToolResultsKeepTheirShape)$' -count=1 -v` → 5 个 `--- PASS`

### Step 8：变异验证（每次只施加一个，验证后**立刻还原**再施加下一个）

| # | 临时改动 | 必须变红的测试 |
|---|---|---|
| M1 | 删掉 Step 6 第 8 条加的 `if end.AwaitedByDispatcher { … }` | `TestAwaitedSubagentUsageLimitArmsNoContinuation` |
| M2 | `execGeneralSubagent` 里去掉 `withSubagentAwaited(...)` 包装 | `TestEverySubagentExecutionIsReportedToItsScheduler` |
| M3 | `subagent_run` 处理函数里删掉 `errors.As(err, &failed)` 分支 | `TestSubagentRunTellsTheDispatcherHowItsSubagentFailed` |
| M4 | fanout 里删掉 `errors.As(err, &failed)` 分支 | `TestFanoutTellsTheDispatcherHowEachSubagentFailed` |

**Verify**：每个变异都看到对应测试 `--- FAIL`；全部还原后 Step 7 的两条命令再次全绿。

### Step 9：假模型驱动——更新 `subagent-limit` 场景并实测

`subagent-limit` 模式今天演示的是"子代理自己续跑"，而它用 `subagent_run`（阻塞派发）派发——本计划落地后这个演示不再成立。
加一个开关保留后台派发的演示：

1. `.claude/skills/run-forebrain/fake_provider.py` 的 `serve_subagent_limit` 里，把派发的工具名从写死的 `"subagent_run"` 改为：
   ```python
   # FAKE_SUBAGENT_DISPATCH=send dispatches the probe in the background
   # (subagent_send): only such a subagent continues itself after the limit
   # resets. A probe the main agent awaits (subagent_run, the default) is
   # handed back to it to continue.
   dispatch = "subagent_send" if os.environ.get("FAKE_SUBAGENT_DISPATCH") == "send" else "subagent_run"
   ```
   并用 `dispatch` 代替 `tool_calls` 里的 `"subagent_run"`。同时改写该方法的 docstring 与文件开头 `subagent-limit` 的模式说明（约 `:72-80`），
   写清两种派发各自演示什么。
2. `.claude/skills/run-forebrain/SKILL.md` 表格里 `subagent-limit` 那一行（约 `:79`）与 `driver.sh` 帮助文字里 `subagent-limit =` 那段（约 `:341-345`）
   同步改写：默认（`subagent_run`）演示"主代理拿到失败说明、子代理不自己续跑"；`FAKE_SUBAGENT_DISPATCH=send` 演示"子代理在自己视图里提示并自己续跑"。
3. 实测（读 SKILL.md 开头的用法；`$D build` 必须先跑，否则跑的是旧二进制）：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D build
python3 -m py_compile .claude/skills/run-forebrain/fake_provider.py

# A：默认（阻塞派发）——主代理拿到说明，子代理不自己续跑
rm -rf "$TMPDIR/p140-dump"
ENABLE_SUBAGENT=1 FAKE_LIMIT_SECONDS=20 FAKE_DUMP_DIR="$TMPDIR/p140-dump" $D start subagent-limit 'probe answered'
$D submit 'go'
$D wait 'primary noted the failure' 40
grep -l 'The subagent started and then failed before it finished its task' "$TMPDIR/p140-dump"/request-*.json
sleep 35
$D provider-log | grep -c 'SUBAGENT-LIMIT answered after the reset'
$D stop

# B：后台派发——011 的子代理自我续跑仍然有效
ENABLE_SUBAGENT=1 FAKE_LIMIT_SECONDS=20 FAKE_SUBAGENT_DISPATCH=send $D start subagent-limit 'probe answered'
$D submit 'go'
sleep 45
$D provider-log | grep -c 'SUBAGENT-LIMIT answered after the reset'
$D stop
```

**Verify**：
- A：`grep -l` 至少列出 1 个文件（编号最小的那个是主代理的第二次请求，里面带着说明；后台请求如标题生成也可能带上对话，所以不要求恰好 1 个）；
  对编号最小的那个文件再 `grep -o 'task_id: subagent-[0-9a-f-]*'` 有输出、`grep -c 'resets_at: '` ≥ 1；
  `provider-log` 的计数是 `0`（子代理没有自己续跑）。
- B：计数 ≥ `1`（后台派发的子代理到点自己续跑了）。
- 驱动起不来按 SKILL.md 的 Troubleshooting 处理；仍不行 STOP 6，**不要**跳过此步宣称完成。

### Step 10：真模型验收（glm-5.3，主树）

假模型只能证明引擎把说明送到了；"主代理读完会续跑而不是重派"是模型的判断，owner 要求这类流程用 glm-5.3 真机跑通。
做法：主代理跑在真实的 glm-5.3 上，`general-purpose` 子代理钉在本地假模型上（`subagent-limit` 模式只识别子代理的请求），
于是子代理稳定地撞上 429，主代理是真模型。

```bash
W="$TMPDIR/p140-real"; rm -rf "$W"; mkdir -p "$W/home" "$W/proj"
( CGO_ENABLED=1 go build -tags fts5 -o "$W/forebrain" ./cmd/forebrain )
scripts/install-dictionary.sh "$W"
scripts/install-assets.sh "$W"
cp ~/.forebrain/models.json "$W/home/" 2>/dev/null || true
echo "p140 acceptance project" > "$W/proj/README.md"
cat > "$W/home/forebrain.yaml" <<'YAML'
approval_policy: on-request
agents:
  defaults:
    enable_subagent: true
  definitions:
    main:
      primary: true
      default_model: zhipuai/glm-5.3
      llm_providers:
      - provider: zhipuai
        model: glm-5.3
        api_key: ${ZHIPUAI_API_KEY}
        base_url: https://open.bigmodel.cn/api/coding/paas/v4
      - provider: deepseek
        model: deepseek-chat
        api_key: ${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:8741
      subagents:
      - name: general-purpose
        model: deepseek/deepseek-chat
YAML
FAKE_LIMIT_SECONDS=90 python3 .claude/skills/run-forebrain/fake_provider.py subagent-limit 8741 'probe answered: ok' 0 > "$W/provider.log" 2>&1 &
echo $! > "$W/provider.pid"
cat > "$W/launch.sh" <<'SH'
#!/bin/bash
eval "$(grep '^ZHIPUAI_API_KEY=' ~/.forebrain/.env)"; export ZHIPUAI_API_KEY
export FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME="$1/home"
cd "$1/proj" && exec "$1/forebrain" 2>"$1/tui.err"
SH
chmod +x "$W/launch.sh"
tmux new-session -d -s p140 -x 150 -y 150 "$W/launch.sh $W; exec bash"
```

- 密钥只在 `launch.sh` 里从 `~/.forebrain/.env` 读进环境，**不要**把它写进命令行、配置文件或报告。不碰真实的 `~/.forebrain` 状态。
- 首屏出现 `Trust this folder?` 时按 `Down` 再 `Enter`（只按一次）；出现项目服务勾选屏时按 `Esc`；等到屏幕上出现 `/ commands`（composer 就绪）。
- 发第一句（`tmux send-keys -t p140 -l '<文字>'; sleep 0.5; tmux send-keys -t p140 Enter`）：
  `请用 subagent_run 派发一个 subagent_type 为 general-purpose 的子代理，title 写“探针”，task 写“简短回答 ok”，然后告诉我它的结果。`
- 等主代理这一回合结束（`tmux capture-pane -t p140 -p` 里出现 `Worked for`）。记录：子代理卡是 `Failed to run 1 general-purpose task`，
  卡头下没有 `tool subagent_run:` 那行；主代理的回复提到了用量上限 / 恢复时间。
- 等到 `$W/provider.log` 里第一条 `LIMIT refused` 之后满 90 秒，发第二句：`额度已恢复，请继续`。
- 等回合结束后截屏保存：`tmux capture-pane -t p140 -p -S - > "$W/screen.txt"`。

**Verify（全部满足）**：
- 第二句之后出现的子代理卡卡头以 `Continued 1` 开头（类型名可能出现也可能不出现；进行中是 `Continuing 1 …`），
  **没有**新的 `Ran 1 general-purpose task` / `Failed to run 1 general-purpose task` 卡。
  （交叉核对：`sqlite3 "$(echo "$W"/home/state/*.sqlite)" "SELECT id, role, substr(content,1,120) FROM fb_messages ORDER BY id;"`——
  第二句之后主代理的工具调用应是 `subagent_continue`，task_id 与第一次失败说明里的相同。）
- 主代理最后的回复里有 `probe answered: ok`。
- `grep -c 'SUBAGENT-LIMIT answered after the reset' "$W/provider.log"` 为 `1`（只有主代理发起的那次续跑）。
- 收尾：`tmux kill-session -t p140; kill "$(cat "$W/provider.pid")"`。

若 glm-5.3 第二句之后仍然用 `subagent_run` 重派：**STOP 7**——把 `$W/screen.txt` 与主代理收到的工具结果原文（上面那条
`fb_messages` 查询里 `role` 为 tool 的行）附进报告，由 owner 决定是否加强说明或工具描述。**不要**自行改常量文案反复重试。
若 glm 在恢复前就自己调了 `subagent_continue`（会再失败一次、得到同样的说明），如实记进报告，不算失败。

### Step 11：设计文档补一条修订

在 `docs/plan/SUBAGENT_CONVERSATION/011-subagent-auto-continue.md` 的"## 语义"表格之后、"D3 照旧"那句之前，加：

```markdown
> **2026-10-26 修订（owner 裁定，plans/140）**：被主代理**阻塞等待**的执行——`subagent_run`、`subagent_fanout` 的任务、
> 模型的 `subagent_continue`——撞上用量上限时**不再**给子代理自己排续跑：失败结果（`task_id`、`ran_for`、描述过的原因、
> `resets_at`）交给主代理，由它用 `subagent_continue` 续跑，结果回到主代理。上表第一行对这三种执行不再成立；
> 后台派发（`subagent_send`）、用户在子代理视图里发起的执行、自动继续本身照旧。实现：`run.SubagentExecutionEnd.AwaitedByDispatcher`。
```

**Verify**：`grep -n "2026-10-26 修订" docs/plan/SUBAGENT_CONVERSATION/011-subagent-auto-continue.md` → 1 条。

## Test plan

- 新增 `pkg/turn`：`TestAwaitedSubagentUsageLimitArmsNoContinuation`（awaited 的执行不排续跑）。
- 扩展 `pkg/run`：`TestEverySubagentExecutionIsReportedToItsScheduler`（派发与模型续跑标 awaited、用户发起不标）；
  `TestDispatchSubagentReportsFailureAndStillClosesTheRow`（错误带着记录，`Error()` 文本不变）。
- 新增 `pkg/run`：`TestSubagentRunTellsTheDispatcherHowItsSubagentFailed`（说明的每一行 + task_id 可续跑 + 续跑再失败同样说明）；
  `TestFanoutTellsTheDispatcherHowEachSubagentFailed`（每任务字段 + fail_fast）。
- 回归：`TestSubagentToolResultsKeepTheirShape`（成功形状）、011 的三个 turn 测试、整包 `pkg/run pkg/turn pkg/process pkg/gateway pkg/tui`。
- 变异验证 M1–M4；假模型驱动 A/B；glm-5.3 真机验收。

## Done criteria

- [ ] Step 7 两条定向命令共 9 个 `--- PASS`
- [ ] Step 8 四个变异各自变红、还原后全绿
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/turn ./pkg/process ./pkg/gateway ./pkg/tui -count=1` 与基线相比无新增 FAIL
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`go vet`（见命令表）exit 0；`gofmt -l …` 无输出
- [ ] `grep -n "AwaitedByDispatcher" pkg/tui/notify.go pkg/gateway/serve_run.go` 各 1 条
- [ ] `grep -c 'withSubagentAwaited(' pkg/run/subagent.go` 为 `3`（定义 1 + 调用 2）
- [ ] Step 9 A/B 的结果如 Verify 所述
- [ ] Step 10 真机验收全部满足，或按 STOP 7 报告
- [ ] Step 11 修订已写入
- [ ] `git status --porcelain` 里本计划新增的改动只落在 In scope 的文件
- [ ] `plans/README.md` 状态行已更新（除非评审方说由其维护）

## STOP conditions

1. 前置 1 / 前置 2 不满足，或"现状"里按函数名找到的代码含义已变（例如 `dispatchSubagent` 不再返回 `prep` 或 `finishSubagent` 不再返回终态记录）。
2. 任一定向测试在改代码前就已经通过（说明行为已存在或测试没走到要测的路径）。
3. 编译需要改 import 名、或测试搭建需要的辅助名字不存在且无法照 `TestSubagentToolResultsKeepTheirShape` 改写、或 fanout 因容量为 0 拒绝。
4. 发现第三个把 `run.SubagentExecutionEnd` 转成 `turn.SubagentExecutionEnd` 的地方。
5. 基线跑不起来（编译失败、依赖缺失）。
6. Step 9 驱动反复起不来，或 A 场景里子代理仍然自己续跑。
7. Step 10 里 glm-5.3 在"请继续"之后仍重派新子代理。
8. 任何一步需要改 In scope 以外的文件。

## Maintenance notes

- 写给模型的那几句是常量（`subagentFailedLead` 等），字节稳定；改措辞会改变模型行为，必须重跑 Step 10 的真机验收。
- `subagentFailure.Error()` 故意返回原错误文本：计划审阅、/goal 检查和日志都依赖它。只有三个派发工具把它换成 `subagentFailureForModel`。
- "谁在等这次执行"只由 `withSubagentAwaited` 决定，目前只有 `execGeneralSubagent`、`continueSubagentExecution` 两处。
  以后新增阻塞派发路径时要决定它属于哪一类；若是阻塞等待，也要加上，否则会恢复"子代理与主代理同时续跑"的竞态。
- 计划审阅与 /goal 检查同样是阻塞的内部派发，但 owner 本次裁定的范围只到三个派发工具，它们保持 011 的行为；要改需另立计划。
- 与 `plans/139` 配套：说明文字作为工具错误也会进入卡片的调用级错误，139 的规则（任务行已带原因就不画调用级错误）保证它不上屏。
