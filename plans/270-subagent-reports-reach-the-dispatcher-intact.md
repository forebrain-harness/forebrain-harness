# Plan 270: 子代理报告原样送到主代理（截断只说一次，全文可读）

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」
> 就停下来报告，不要自己发挥。**不要 commit / push / merge**：改动留在工作区（仓库主人手动提交）。
> 做完后把 `plans/README.md` 里本计划那一行的状态改成 DONE 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/tool/shell_output.go` 与
> `git diff --stat 0a698bf2 -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/tool/shell_output.go`
> → 期望都为空。（基线 `0a698bf2` 就是当前 HEAD，`git diff 0a698bf2..HEAD` 比的是两个 commit，
> **看不到未提交改动**，实测恒为空——不要用它下结论。）
> `pkg/run/orchestration_llm.go` 单独看：若 plan 272 已先行执行（README 执行序 272 → 270 → 271），它会带
> 272 Step 5 在 no-tool-call 分支（基线 `:836` 那条硬错误之前）插入的「撞上限收尾」——这属预期，不是 STOP。
> 核对方式：`git show 0a698bf2:pkg/run/orchestration_llm.go` 对照「Current state」，只要 `governToolResultDetails`
> （`:2394`）与 `governedContentParts`（`:2373`）两段仍对得上即可继续；对不上才 STOP。
> **注意（2026-11-07）**：`pkg/llm/` 下有未提交改动（`pkg/llm/openai/compat_llm.go`、`pkg/llm/retry.go`、
> `pkg/llm/stream.go`、`pkg/llm/anthropic/agent_llm.go` 等）——可能来自并行会话，也可能是 272 先行执行的产物。
> 本计划不碰 `pkg/llm/`；若你发现必须改它，STOP。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改模型可见的工具结果文本；TUI/网页读的是事件，不解析这段 JSON，但要逐条确认）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `0a698bf2`, 2026-11-07

## Why this matters

owner 2026-11-07 真机跑 6 路 `subagent_fanout`（计划锚点只读核对），主代理只完整看到 6 份报告里的 3 份
（第 0、4、5 份），另外 3 份被截断；主代理接着用 `read_file` 读落盘文件，读到的仍是同样 3 份，
最后只能自己写 python 从 JSON 里抠。

已用临时测试（`go test -overlay`，工作树零改动）在当前代码上复现并量化：

| 场景 | 结果 JSON | 主代理实际可见 | 丢掉 | 结论可见的任务 |
|---|---|---|---|---|
| 6 × 10 KB 报告 | 91,484 B | 32,994 B | 58,747 B | 仅 0、4、5 |
| 4 × 12 KB 报告 | 71,340 B | 32,995 B | 38,602 B | 仅 3 |
| 2 × 40 KB 报告 | 87,336 B | 32,996 B | 54,597 B | 仅 1 |

（每份 10 KB、每份首行与末行带唯一标记；"结论可见"= 主代理收到的文本里还有该份报告末行的标记。）

四层叠加造成了它：

1. **所有报告共用一份 32 KB 预算**：`buildFanoutSummary` 把全部结果拼成**一行 JSON**，
   再走通用截断 `TrimHeadTail`，只留头尾各约 16 KB —— 中间几份整份消失。
2. **体积被无谓放大**：每个任务的完整 prompt 原样回显在 `task` 字段（复现中 6 份 prompt 共约 13.7 KB），
   JSON 转义又把 `\n`、`<`、`&`、`"` 变长，整份结果只剩 1 行。
3. **落盘文件是单行 JSON**：`read_file` 按行分页，读第 1 行就是整份 91 KB，于是被同一个 32 KB 再截一次、
   再落一份新文件（复现：每次 read_file 都 `re-spilled=true`）。模型无法定位中间内容。
4. **同一份报告最多被截三次**：`finishSubagent`（`pkg/run/subagent.go:3766`）截一次，
   fanout（`:3202`）与 `subagent_run`（`:391`）对已截过的文本**再截再落盘**，
   外层 `governToolResultDetails`（`pkg/run/orchestration_llm.go:2131`）第三次截。
   复现里 2 份报告出现 4 个 "full output available via read_file:" 路径，其中 2 个指向的**本身就是截断版**。

做完后：每份报告只截一次、截断点写清楚、全文是多行纯文本可被 `read_file` 分页读完；
"完整输出"路径第一次就是真的。

## Current state

- `pkg/run/subagent.go:3506` —— 结果拼装（问题是它把 `FanoutResult` 整个 JSON 化）：

```go
func buildFanoutSummary(results []FanoutResult) string {
	...
	resp := map[string]any{"summary": summary, "results": results}
	b, _ := json.Marshal(resp)
	return string(b)
}
```

- `pkg/run/subagent.go:275` —— `FanoutResult` 字段：`Index int`、`Task string`（完整 prompt）、`SubagentType`、
  `Output string`、`Error string`、`OK bool`、`Status string`、`TaskID/RanFor/ResetsAt/Next string`
  （后四个是 plan 140 给主代理的续跑说明）。
- 三次截断的调用点：
  - `pkg/run/subagent.go:3766`（`finishSubagent`，`d.governTool` = `"subagent_run"`）
  - `pkg/run/subagent.go:3202`（fanout 每任务 `governSubagentOutput(fac, "subagent_fanout", taskID, execRes.Output).Text`）
  - `pkg/run/subagent.go:391`（`subagent_run` 工具对 `execRes.Output` 再 govern 一次）
  - `pkg/run/orchestration_llm.go:2131` → `governToolResultDetails`（`:2394`）对**所有**工具结果一视同仁
- `pkg/run/subagent.go:4034` —— `governSubagentOutput`：

```go
func governSubagentOutput(fac Factory, toolName, callID, output string) tool.GovernedOutput {
	governor := newToolOutputGovernor(factoryToolsState(fac))
	if governor == nil {
		return tool.GovernedOutput{Text: output}
	}
	return governor.GovernDetailed(strings.TrimSpace(toolName), strings.TrimSpace(callID), output)
}
```

- `pkg/tool/shell_output.go:694` `SpillThresholdBytes = 32 * 1024`、`:695` `SpillDir = "tool-outputs"`、
  `:742` `GovernDetailed`、`:356` `TrimHeadTail`（头 1/2、尾 1/2，中间 `...[omitted N bytes]...`）、
  `:767` 截断尾巴文案：

```go
suffix += fmt.Sprintf("; full output available via read_file: %s]", path)
```

- `pkg/tool/shell_output.go:793` 已有 `Store(toolName, callID, content) (string, error)`：
  **原样写盘**（不截断、不改换行），正好用于把全文写成多行纯文本。
- `pkg/run/subagent.go:3069` `mergeContinuationOutput`：`truncatePreviewText(prev + "\n\nContinuation result:\n" + cur, 24000)`
  —— 只保留**前** 24000 字节，续跑产生的新结果在旧报告很长时会被整段丢掉（`llm.TruncateBytes` 保头，`pkg/llm/truncate.go:25`）。
- 仓库约定：模型可见文本必须 provider-neutral、逐字节稳定（`FOREBRAIN.md` House rules），
  所以下面所有文案写成 `const`，并配 golden/断言测试；错误与提示文案风格参考
  `pkg/run/subagent.go:3740` 附近的 `subagentFailureForModel`（分行 `key + 值`，不写解释性废话）。
- 界面侧不解析这段 JSON（已核实。**2026-11-07 修订：此核验当时漏了 `pkg/tool/format.go` 的
  `SubagentCallFromStep` 卡片事实层——它确实解析 `Output["output"]` 的 JSON，Step 4b 以 sidecar 修复**）：
  - TUI 子代理卡读事件（`pkg/tui/commands.go:1131` `isSubagentDispatchTool`，`pkg/tui/reducer.go:1122`）；
  - 网页同样读事件（`frontend/src/composables/useChatStream.ts:682`）；
  - 唯一的 JSON 字面量出现在测试与 e2e 夹具 `frontend/e2e/subagent-cards.spec.ts:83`（夹具是手写数据，不是解析器）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 本包测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | ok |
| 工具包测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -count=1` | ok |
| 格式 | `gofmt -l pkg/run pkg/tool` | 无输出 |

（`FOREBRAIN_HOME` 必须显式指向临时目录：不继承外层环境，这是仓库硬规矩。）

## Scope

**In scope**：
- `pkg/run/subagent.go`
- `pkg/run/subagent_report.go`（新建）
- `pkg/run/subagent_report_test.go`（新建）
- `pkg/run/subagent_test.go`（仅改断言，不改被测行为）
- `pkg/run/orchestration_llm.go`（只在 `governToolResultDetails` 里加豁免分支）
- `pkg/tool/format.go`（**2026-11-07 执行中止后扩入**：只改 `SubagentCallFromStep` 对 fanout 结果的识别，见 Step 4b）
- `pkg/tool/format_test.go`（Step 4b 的新测试）

**修订记录（2026-11-07，执行中止后）**：执行者按 STOP 条件 2 停在 Step 6 前——计划原稿「界面侧不解析这段 JSON」的核验漏了
卡片事实推导层：`pkg/tool/format.go:2979-2992` `SubagentCallFromStep` 对 `evt.Output["output"]` 做 `json.Unmarshal`，
fanout 分支（`:3076-3107`）按 `results[].ok/status/error` 结算卡片行；纯文本结果会让每行任务停在 `status=""`
（永不结算），消费方横跨 `format.go:259/:516/:2468/:2806/:2819` 与 `pkg/turn/compaction.go:290`。评审已亲自核实。
修法＝Step 4b（sidecar facts，根因：事实走数据、不再解析显示文本）。另：`pkg/tool/request_permissions_test.go`
在 HEAD 就不过 gofmt（预存在，非本计划引入），done 判据的 gofmt 条目改为「本计划触及的文件」。

**Out of scope**（不要碰，哪怕看起来相关）：
- `pkg/tool/shell_output.go` 的 `TrimHeadTail`/`GovernDetailed` 语义 —— `read_file` 侧的豁免是 plan 271 的事。
- `pkg/llm/**`（有并行会话的未提交改动）。
- 子代理卡的事件载荷与 TUI/网页渲染。
- `subagent_status` / `subagent_wait` / `subagent_continue` / `subagent_close` / `subagent_list` 的返回形状
  （它们返回 `record`，字段另有一套；本计划只管 `subagent_run` 与 `subagent_fanout` 的报告文本）。

## Steps

### Step 1: 建立报告预算与分节渲染（新文件 `pkg/run/subagent_report.go`）

```go
const (
	// subagentReportInlineChars is one report's inline budget. Characters, not
	// bytes, so a Chinese report and an English one get the same room; the
	// number is Claude Code's Agent-result cap.
	subagentReportInlineChars = 50_000
	// subagentFanoutInlineChars is everything one fanout may put in the
	// model's context, all reports together.
	subagentFanoutInlineChars = 200_000
	// subagentReportPreviewChars is what an over-budget report keeps when the
	// fanout as a whole is over budget: its head, enough to recognise the
	// report, plus the path to the rest.
	subagentReportPreviewChars = 2_000
)
```

同文件提供：

- `spillSubagentReport(fac Factory, taskID, report string) (string, error)`：用
  `newToolOutputGovernor(factoryToolsState(fac))` 取目录后用 `governor.Store("subagent_report", taskID, report)`
  原样写盘（**不要** `GovernDetailed`，不要再截），返回绝对路径。
- `type subagentReportSection struct { Header string; Body string; Path string; Chars int }`：
  一份报告渲染后的一节。
- `renderSubagentReportSection(index, total int, r FanoutResult, path string, budget int) subagentReportSection`：
  节头（纯文本，逐字节固定）：

  ```
  ## [3/6] 标题 · ok · task_id=<id> · ran_for=4m6s
  ```

  失败任务在节头下一行给 `error` / `resets_at` / `next`（原样搬 `FanoutResult` 的字段值，不重写文案）。
  正文按 `budget` 截断：`body = report[:head]`，切在**行边界**上，然后追加一行说明（固定句）：

  ```
  [report cut from 123456 to 50000 characters; the whole report is at <path> — read it with read_file, its end carries conclusions]
  ```

  `path` 为空时说明里去掉路径从句。
- `budgetSubagentReport(fac Factory, taskID, report string, budget int) (inline, path string)`：单份报告的
  「只截一次」——`report` 不超过 `budget` 个字符就原样返回、`path` 为空；超过就先 `spillSubagentReport` 原样落盘，
  再返回「切在行边界上的前 `budget` 个字符 + 上面那行固定说明」与路径。`renderSubagentReportSection` 与
  Step 3 的 `subagent_run` 都调它，**不要**各写一份截断逻辑。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0。

### Step 2: `buildFanoutSummary` 改为纯文本报告，不再回显 prompt

替换 `pkg/run/subagent.go:3506` 的实现（保留函数名与签名，调用点 `:3218`/`:3220`/`:3222` 不动）：

```go
func buildFanoutSummary(results []FanoutResult) string
```

- 第一行汇总：`6 tasks · 5 ok · 1 failed · 12s`（数字取 `results`，`cancelled` 非零时追加 ` · 1 cancelled`）；
  每个失败任务的 `task_id` 汇总行不必重复，放在节头。
- 逐任务一节，顺序按 `Index`。
- **不输出 `task`（prompt）字段**，只输出 `title`（`FanoutResult` 目前没有 title —— 见下一条）。
- 报告正文的落盘与预算在 Step 3 处理；本步先产出「每份报告 + 路径」，TUI/网页不受影响。

`FanoutResult` 增加 `Title string `json:"title,omitempty"``（`pkg/run/subagent.go:275`），
在 fanout goroutine 里 (`:3200` 附近) 用 `title` 变量填。`Task` 字段保留（测试与内部用），只是**不再渲染**。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0。`./pkg/run/ -run Fanout` 此时**预期红**
（`TestSubagentFanoutGovernedOutputUsesUnifiedContextTruncation` 等断言的还是旧的 governor 行为），属预期——
Step 6 修完必须全绿；在那之前不要为了让它们变绿而回退 Step 2/3。

### Step 3: 预算分配与"只截一次"

在 `buildFanoutSummary` 内（或它调用的 `renderFanoutReport`）：

1. 每份报告先按 `subagentReportInlineChars` 单独截断 → 得到 `inline[i]` 与（若被截）全文路径 `path[i]`。
2. 求和；若 `sum > subagentFanoutInlineChars`，按 `chars` **从大到小**把报告改渲染成
   `subagentReportPreviewChars` 的预览 + 路径，直到和降回预算内。**被降级的节仍保留节头与路径**。
3. 任何情况下每份报告都至少留：节头 + 前 2000 字符（或全文，若更短）+ 路径。

删除 `pkg/run/subagent.go:3202` 处的 `governSubagentOutput(...)`（报告已由 Step 3 预算），
并在 `:3766`（`finishSubagent`）与 `:391`（`subagent_run`）里**不要**再对报告文本调 governor：
`final.Output` 保存**原始全文**（供 `subagent_status`、续跑提示、事件使用），
只有工具返回给模型的文本走预算：

- fanout：上面 1–3 的预算渲染。
- `subagent_run`（`:391`）：把 `governed := governSubagentOutput(fac, "subagent_run", taskID, execRes.Output)` 换成
  `inline, _ := budgetSubagentReport(fac, taskID, execRes.Output, subagentReportInlineChars)`，响应 map 里
  `"output": governed.Text`（`:418`）改成 `"output": inline`。**必须**这样做：Step 4 让外层 governor 豁免
  `subagent_run` 之后，这里若直接放原文，一份几百 KB 的报告会整段进上下文。
  响应 map 的其它字段（`agent_id`、`task_id`、`status` 等，`:410-426`）不变，也**不要**新增字段。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0；`grep -n "governSubagentOutput" pkg/run/subagent.go` → 只剩定义（或已删净）。

### Step 4: 通用截断不再二次处理子代理工具

`pkg/run/orchestration_llm.go` 的 `governToolResultDetails`（`:2394`）开头加：

```go
	// A subagent's report is budgeted where it is produced (pkg/run/subagent.go,
	// buildFanoutSummary): re-governing it here would spill a second, truncated
	// copy over the real full text and hand the model a path that is itself cut.
	switch strings.TrimSpace(call.Function.Name) {
	case "subagent_run", "subagent_fanout":
		return nil
	}
```

（`governedContentParts`（`:2373`）在 `details` 为空时原样返回 parts，无需改动。）

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'TestToolOrchestrationTruncatesLargeToolResultBeforeContextReplay|TestTruncatedTwiceFallsBackToNonStreaming' -count=1` → ok
（这两条测的是**通用**工具截断，与本计划无关，必须保持绿。不要把 `TestSubagentRunGovernedOutputUsesUnifiedContextTruncation` /
`TestSubagentFanoutGovernedOutputUsesUnifiedContextTruncation` 选进门里——它们从 Step 3 起就是红的，Step 6 才修。）

### Step 4b: fanout 的卡片事实走 sidecar（不再解析显示文本）

模型可见文本与卡片事实分离——事实作为数据随 StepEvent 走，不再从工具结果文本反解析：

1. `pkg/run/subagent.go` 的 fanout handler（`return renderFanoutReport(fac, results), nil` 处，成功与
   fail-fast 两个收尾点都算）：返回值仍是纯文本报告（模型只见它）；同时用既有捕获机制
   （先例 `pkg/tool/shell_tool.go:724`、`pkg/tool/file_tools.go:1906`）把结构化事实放进 StepEvent：

   ```go
   tool.CaptureToolCompletion(ctx, tool.ToolCompletionPayload{
       Output: map[string]any{
           "output":  report,        // 纯文本报告，卡片主体/TUI 渲染继续读它
           "results": fanoutCardFacts(results), // 瘦事实：每任务仅 index/ok/status/error
       },
   })
   ```

   `fanoutCardFacts`（放 `subagent_report.go`）返回 `[]map[string]any`，每项只含
   `index`、`ok`、`status`、`error` 四键——正是 `format.go` fanout 分支读的那四个；
   **不带** prompt/output 正文（sidecar 也要瘦）。引擎侧 `fallbackToolOutput` 对捕获的 Output
   直接采用（`cloneStepOutput(cp.Output)`），不经过 map 返回值路径，模型可见文本不受影响。
2. `pkg/tool/format.go` `SubagentCallFromStep`：`evt.Output["results"]` 存在（`[]any`）时**优先**
   用它调 `subagentCallApplyResult`（fanout 分支原样可用）；否则走今天的 `json.Unmarshal(output字符串)`
   旧路径**保留**——存量会话的事件里存的是旧 JSON 形态，重放/续跑仍要能结算。
   `subagent_run` 不动（它仍返回 JSON map 字符串，旧路径照常工作）。
3. `pkg/tool/format_test.go` 新增：sidecar 形态结算（ok→done、!ok+cancelled→cancelled、
   error 前缀 skipped→skipped、其余→failed、error 带出）；旧 JSON 形态（`{"summary":…,"results":…}`
   字符串在 `output` 下）仍结算（等价旧测试若有则保持，不删）。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'Subagent' -count=1` → ok；
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'Fanout' -count=1` → ok（Step 6 修完后）。

### Step 5: 续跑合并不得丢掉新结果

`pkg/run/subagent.go:3069`：

```go
// mergeContinuationOutput keeps the previous result and appends this
// execution's own, reserving room for the new text: truncating after the
// concatenation would drop the very output this continuation produced
// whenever the earlier report was long.
func mergeContinuationOutput(previous, next string) string {
	prev, cur := strings.TrimSpace(previous), strings.TrimSpace(next)
	// 24000 is the record's ceiling either way.
	const limit = 24000
	switch {
	case prev == "":
		return truncatePreviewText(cur, limit)
	case cur == "":
		return truncatePreviewText(prev, limit)
	}
	room := limit - len(cur) - len("\n\nContinuation result:\n")
	if room < 1000 { // 新结果已占满：保留新结果，旧报告只留开头
		return "Continuation result:\n" + truncatePreviewText(cur, limit)
	}
	return truncatePreviewText(prev, room) + "\n\nContinuation result:\n" + cur
}
```

**Verify**: 新测试（Step 6）通过前先 `go build ./...` exit 0。

### Step 6: 修既有断言 + 新增测试

既有测试里按 JSON 形状断言的，改成按纯文本断言（不改被测行为）：
`pkg/run/subagent_test.go:1932`、`:2053`、`:2955`、`:3836`（`json.Unmarshal(&struct{ Results []FanoutResult })`）
与 `:2900`/`:2967`（断言 `[tool output truncated for context:` / `full output available via read_file:`）。
后者改为断言「节头 + 路径 + 说明」三件套。

新增 `pkg/run/subagent_report_test.go`（executor 用已有 `fixedSubagentExecutor`，
`pkg/run/subagent_test.go:2127`，它已经实现 `SubagentExecutor` 全部方法，可直接拿来喂不同长度的报告）：

| 测试名 | 断言 |
|---|---|
| `TestFanoutReportDeliversEveryReport` | 6 份各 10 KB：6 个节头都在、每份报告首行都在、**不含 prompt 文本**、总长 ≤ 200,000 |
| `TestFanoutReportTruncatesOnce` | 单份 60 KB：内联 ≤ 50,000 字符 + 恰好 1 条 `report cut from` 说明 + 恰好 1 个路径；该路径文件与原始报告**逐字节相同**且含 `\n`（多行） |
| `TestFanoutReportShrinksLargestFirst` | 6 份各 40 KB（合计 240,000 > 200,000；每份 ≤ 50,000 不触发单份截断，只测「降级」路径）：按大小降序降级 2 份后合计 164,000 回到预算内——最大的 2 份只剩 2000 字符预览 + 路径，其余 4 份全文内联，6 个节头都在 |
| `TestFanoutReportPathIsReadableInPages` | 用 `tool.NewFileReadTool` 读上一条的路径，`offset` 翻页能读到报告末行（依赖 plan 271；271 未落地时改为断言 `lines > 1`） |
| `TestContinuationKeepsNewResult` | `mergeContinuationOutput(24000 字符旧报告, "新结论")` 结果含 `新结论` 且长度 ≤ 24000 |
| `TestSubagentRunReportHasNoDoubleSpill` | `subagent_run` 返回 60 KB 报告：JSON 的 `output` 字段 ≤ 50,000 字符 + 恰好 1 条 `report cut from` 说明 + 恰好 1 个路径；该路径文件与原始报告逐字节相同；返回文本里**没有** `[tool output truncated for context:`。（`subagent_run` 的响应没有 `output_bytes` 字段，不要断言它，也不要为测试新增它） |
| `TestSubagentRunShortReportIsUntouched` | 2 KB 报告：`output` 与原文逐字节相同、不含 `report cut from` |

## Done criteria

全部满足：

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ ./pkg/tool/ -count=1` ok
- [ ] `gofmt -l` 对本计划**触及**的文件（pkg/run/subagent.go、subagent_report.go、subagent_report_test.go、
      subagent_test.go、orchestration_llm.go、pkg/tool/format.go、format_test.go）无输出；
      `pkg/tool/request_permissions_test.go` 在 HEAD 即不过 gofmt（预存在、非本计划引入，不修不碰，报给 owner）
- [ ] `grep -n '"task"' pkg/run/subagent.go` 只出现在结构体定义处，不出现在渲染函数里
- [ ] `grep -rn "full output available via read_file" pkg/run/ | grep -v _test.go` 无输出；
      `grep -c "full output available via read_file" pkg/run/subagent_test.go` → 0；
      `grep -c "full output available via read_file" pkg/run/orchestration_llm_test.go` → **恰好 2**
      （`TestToolOrchestrationTruncatesLargeToolResultBeforeContextReplay` 与 `TestForkExecutionChainAlsoTruncatesLargeToolResultBeforeContextReplay`
      测的是通用工具截断，保留不动——它们使全目录零命中的写法永假）
- [ ] 一个 6×10 KB 的 fanout，模型可见文本里 6 个节头齐全（测试覆盖）
- [ ] `git status` 只列出 Scope 里的文件

## STOP conditions

- 「Current state」的代码与工作区不符（并行会话改过 `pkg/run/subagent.go`）。
- 发现任何界面代码解析 fanout 结果的 JSON（会破坏它们）。
- Step 3 之后 `subagent_status` / 事件载荷里出现被截断的报告（说明你把 `final.Output` 也截了）。
- 同一处验证连续失败两次。

## Maintenance notes

- 报告内联预算是**字符**口径；`pkg/tool` 的落盘阈值仍是**字节**口径（32 KB）。两套单位并存是刻意的：
  报告在模型上下文里计字符（与 Claude Code 一致），通用工具输出按字节落盘（省磁盘）。改一处时别顺手统一另一处。
- `subagent_status`/`subagent_wait` 返回的 `record.output` 仍是完整报告（未做预算）；主代理读它们时可能再次撑爆上下文，
  这是已知遗留，留给后续按同一 helper 收口。
- `read_file` 侧的二次截断由 plan 271 修；271 未落地时，读大报告仍会被 32 KB 截一次。
- 评审要点：确认「每份报告只截一次」，即一次 fanout 里 `report cut from` 的出现次数 == 被截报告数。
