# Plan 271: read_file 不再二次截断、不再二次落盘（超预算就报错）

> **Executor instructions**: 按步骤顺序执行，每步验证通过再做下一步。遇到「STOP conditions」就停下报告，
> 不要自己发挥。**不要 commit / push / merge**。做完后把 `plans/README.md` 里本计划那一行的状态改成 DONE
> 并附一句摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git status --porcelain -- pkg/tool/shell_output.go pkg/tool/file_tools.go pkg/tool/file_tools_test.go` 与
> `git diff --stat 0a698bf2 -- pkg/tool/shell_output.go pkg/tool/file_tools.go pkg/tool/file_tools_test.go`
> → 期望都为空。（基线 `0a698bf2` 就是当前 HEAD，`git diff 0a698bf2..HEAD` 比的是两个 commit，
> **看不到未提交改动**，实测恒为空——不要用它下结论。）
> `pkg/run/orchestration_llm.go` / `orchestration_llm_test.go` 按执行序（272 → 270 → 271）大概率已带先行改动：
> 272 Step 5/6（no-tool-call 分支的撞上限收尾与其测试）与 270 Step 4/6（`governToolResultDetails` 开头的
> `case "subagent_run", "subagent_fanout"` 豁免与测试改写）——都属预期，不是 STOP。核对方式：
> `git show 0a698bf2:<path>` 对照「Current state」；本计划的 `read_file` 豁免分支与 270 的 switch **并列**，
> 若 270 的豁免不在场，就把 `read_file` 分支加在同一位置。对不上才 STOP。

## Status

- **Priority**: P2
- **Effort**: S–M
- **Risk**: MED（改所有 `read_file` 的超大读取行为；`read_file` 是模型最常用的工具）
- **Depends on**: none（与 plan 270 配合更好：270 让报告变多行，271 让大文件可读）
- **Category**: bug
- **Planned at**: commit `0a698bf2`, 2026-11-07

## Why this matters

owner 2026-11-07 的真机 transcript 里，主代理拿到 fanout 报告被截断后，用 `read_file` 去读落盘文件，
结果**又被截断一次**，最后只能自己写 python 解析。

在当前代码上复现（`go test -overlay`，工作树零改动，6 份 10 KB 报告 → fanout JSON 91,484 B）：

```
fanout 外层: 模型可见 32,994 B（omitted 58,747 B）
read_file(spill) -> 原始 91,486 B, 模型可见 32,990 B, omitted 58,747 B, re-spilled=true
```

三件事叠在一起：

1. **`read_file` 的结果也走通用截断**：`governToolResultDetails`（`pkg/run/orchestration_llm.go:2394`）
   对所有工具一视同仁，超过 `SpillThresholdBytes`（32 KB）就 `TrimHeadTail` → 头尾各半、中间丢弃。
2. **截断后又落盘一份新文件**：`GovernDetailed` 在截断的同时 `spill` 全文，于是"读文件"这个动作反过来
   又生产了一个文件（复现里 `re-spilled=true`），模型要读的目标一路上只会变多。
3. **单行文件根本没法分页**：`read_file` 按行分页（每次最多 200 行，`pkg/tool/file_tools.go:719`），
   一行 91 KB 的 JSON 无论 `offset` 取哪里都只能拿到同一整行，再被 32 KB 截一次。

做完后：`read_file` 按行返回，**不再截断、不再落盘**；选中的窗口放不下时**明确报错**并告诉模型
怎么缩小范围（这是 Claude Code `Read` 的做法：`File content (N tokens) exceeds maximum allowed tokens (M).
Use offset and limit parameters to read specific portions of the file, or search for specific content instead
of reading the whole file.`）。单行过长时截断该行并注明省略字节数，否则单行文件永远读不出来。

## Current state

- `pkg/tool/shell_output.go:742` —— `GovernDetailed` 对**任何**工具的输出生效：

```go
	if len(output) <= limit {
		return res
	}
	trimmed := TrimHeadTail(output, limit)   // 头尾各半，中间丢弃
	...
	path, err := g.spill(toolName, callID, output)
	...
	suffix += fmt.Sprintf("; full output available via read_file: %s]", path)
```

- `pkg/tool/shell_output.go:708` `SpillThresholdBytes = 32 * 1024`；`:356` `TrimHeadTail`；`:793` `Store`。
- `pkg/run/orchestration_llm.go:2131` 调用点与 `:2394` `governToolResultDetails`（无工具白名单）。
- `pkg/tool/file_tools.go:63` 的 `Limit` 说明：`max lines to return (capped at 200 …)`；
  `:719` `resolveReadFileRange`（限 200 行、越界自动收窄）；`:680` `sliceWithLineNumbersBytes`
  用 `sliceWithLineNumbersFromReader` 逐行输出 `N|line`（`pkg/tool/file_tools.go` 内 `lineNumberPrefix`）。
- 行前缀格式（`fmt.Fprintf(&b, "%d|%s\n", line, sc.Text())`，`pkg/tool/file_tools.go:779`）**保持不动**：
  它是模型定位行号的依据，也是 `display_header.go:177` 卡片显示行区间的数据源。
- 同一个函数 `:762` 有 `sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)`：单行超过 2 MB 时
  `bufio.Scanner` 会以 `token too long` 失败（今天的行为）。本计划**不改**这个上限，只在文档上记一句。
- `pkg/llm/tokestimate.go:98` `llm.EstimateText(s string) int` —— 仓库已有的 token 估算入口，
  用它判断预算，不要新写一个估算器。
- 现有测试：`pkg/tool/file_tools_test.go`（`read_file` 行为）、`pkg/run/orchestration_llm_test.go:8026`
  `TestToolOrchestrationTruncatesLargeReadFileResultBeforeContextReplay`（**它断言的正是要被改掉的行为**）、
  `pkg/tool/request_permissions_test.go:1870` 附近的 governor 单测（`shell` 工具，不受影响）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 构建 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 工具包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -count=1` | ok |
| run 包 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | ok |
| 格式 | `gofmt -l pkg/tool pkg/run` | 无输出 |

## Scope

**In scope**：
- `pkg/tool/file_tools.go`
- `pkg/tool/file_tools_test.go`
- `pkg/run/orchestration_llm.go`（govenor 白名单）
- `pkg/run/orchestration_llm_test.go`（改那条断言的测试）

**Out of scope**：
- `pkg/tool/shell_output.go` 的 `TrimHeadTail` / `GovernDetailed` 对其它工具的行为（shell、web、MCP 保持原样）。
- `read_file` 的行号前缀、分页上限（200 行）、`offset` 语义。
- 图片/PDF 分支。
- `pkg/llm/**`（有并行会话未提交改动）。

## Design decisions（已由 owner 拍板）

1. `read_file` **豁免**通用落盘：不截断、不写新文件。
2. 窗口超出预算 → **报错**（不是静默截断），文案要点：token 数、上限、用 `offset`/`limit` 缩小范围。
3. 单行超长 → 截断该行并注明 `[... N chars omitted]`（否则单行文件永远超预算、永远读不出来）。

## Steps

### Step 1: `read_file` 的预算检查与单行截断

`pkg/tool/file_tools.go`：

- 新增常量（放在文件顶部常量块）：

```go
const (
	// ReadFileMaxResultTokens is the ceiling on one read_file result. It is a
	// token budget rather than a byte one because what it protects is the
	// model's context, and 4 bytes of English and 1.5 bytes of Chinese cost
	// the same there. Claude Code's Read uses the same number.
	ReadFileMaxResultTokens = 25000
	// ReadFileMaxLineChars caps one line inside a result. Without it a
	// minified JSON or a bundle on a single line can never fit any window,
	// and "use offset/limit to read less" would be advice that cannot work.
	ReadFileMaxLineChars = 2000
)
```

- 在 `sliceWithLineNumbersBytes`（`:680`）产出的每一行做行级截断：超过 `ReadFileMaxLineChars`
  字符时保留前 `ReadFileMaxLineChars`，追加固定标记
  `[… 12345 chars omitted from this line]`（用 `llm.TruncateBytes` 的 UTF-8 边界处理思路，
  但按字符切；可直接 `[]rune` 切片，行内不含换行）。
- 在 `NewFileReadTool` 的文本分支（`pkg/tool/file_tools.go:198` 附近，`sliceWithLineNumbersBytes` 之后）：

```go
tokens := llm.EstimateText(out)
if tokens > ReadFileMaxResultTokens {
	return "", fmt.Errorf(
		"file content (%d tokens) exceeds the maximum of %d tokens for one read: "+
			"use offset and limit to read a smaller window, or search for the part you need "+
			"(the file has %d lines)",
		tokens, ReadFileMaxResultTokens, totalLines)
}
```

  注意：**先**做行级截断、**后**判预算（否则单行超长文件永远报错）。
- 不新增 `allow_large` 之类的旁路参数。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run ReadFile -count=1` → ok（新测试见 Step 4）。

### Step 2: 通用截断豁免 `read_file`

`pkg/run/orchestration_llm.go` 的 `governToolResultDetails`（`:2394`）开头，与 plan 270 的子代理豁免并列：

```go
	// read_file already fits its own result to a token budget and reports an
	// error when the window does not fit (pkg/tool/file_tools.go): truncating
	// it here would cut the lines the model just chose, and spilling it would
	// answer a read with a new file to read.
	if strings.TrimSpace(call.Function.Name) == "read_file" {
		return nil
	}
```

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0。

### Step 3: 修既有测试

`pkg/run/orchestration_llm_test.go:8026`
`TestToolOrchestrationTruncatesLargeReadFileResultBeforeContextReplay` 断言的是旧行为
（`[tool output truncated for context:` + `full output available via read_file:`）。改名为
`TestToolOrchestrationLeavesReadFileResultAlone` 并断言：

- 大文件读取结果**原样**进入 replay session（`len(toolText) == len(largeFile)` 或按 Step 1 报错）；
- 不含 `full output available via read_file:`；
- 不含 `[tool output truncated for context:`。

该测试的 `largeFile` 是 `"1|package main\n"` + 1200 ×（`"2|"` + 64 个 `x` + `\n`）≈ 80.4 KB，
按 `llm.EstimateText` 的 rune/4 启发式（`pkg/llm/tokestimate.go:98`，`estimateHeuristicRuneDiv4`）≈ 20.1k tokens
—— 在 25000 预算内，所以期望是**原样通过**。若实测超过预算，把每行 64 字符减到 40，并在测试注释里写明这个数是为了落在预算内。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -run 'ReadFile' -count=1` → ok。

### Step 4: 新增测试

`pkg/tool/file_tools_test.go`：

| 测试名 | 场景 | 断言 |
|---|---|---|
| `TestReadFileWindowOverBudgetErrors` | 生成 ~1 MB 文本，`limit=200` 取一段超预算 | 返回 error，文案含 token 数、`25000`、`offset`、`limit`；不含文件内容 |
| `TestReadFileNarrowWindowFits` | 同一文件 `offset=0,limit=20` | 成功，返回 20 行，含行号前缀 |
| `TestReadFileTruncatesOneLongLine` | 单行 50,000 字符的文件 | 成功（不报错），该行被截到 2000 字符 + `chars omitted from this line` 标记 |
| `TestReadFileResultIsNotSpilled` | 300 KB / 数万行的文件分页读 | 成功后 `state.ToolResultDir()` 下**没有**新增文件 |

`pkg/run/orchestration_llm_test.go`：Step 3 改名后的那条。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ ./pkg/run/ -count=1` → ok。

## Done criteria

- [ ] `CGO_ENABLED=1 go build ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ ./pkg/run/ -count=1` ok
- [ ] `gofmt -l pkg/tool/file_tools.go pkg/tool/file_tools_test.go pkg/run/orchestration_llm.go pkg/run/orchestration_llm_test.go` 无输出；
      `pkg/tool/request_permissions_test.go` 在 HEAD 即不过 gofmt（2026-11-07 执行 270 时发现的预存在问题，
      非本计划引入，不修不碰）——原判据「`gofmt -l pkg/tool pkg/run` 无输出」因它恒假，已改触及文件口径
- [ ] `grep -rn "full output available via read_file" pkg/run/ | grep -v _test.go` 无输出；
      `grep -c "full output available via read_file" pkg/run/orchestration_llm_test.go` → **恰好 2**
      （`:7976` `TestToolOrchestrationTruncatesLargeToolResultBeforeContextReplay` 与 `:8074`
      `TestForkExecutionChainAlsoTruncatesLargeToolResultBeforeContextReplay` 测**通用**工具截断，保留该文案；
      本计划只改写 read_file 那条——全目录零命中的写法会因这两条永假）；
      `pkg/run/subagent_test.go` 的两处由 plan 270 Step 6 清除（若仍在，说明 270 未先行执行，按 README 执行序应先跑 270）；
      `grep -n "full output available via read_file" pkg/tool/file_tools.go` 无命中
- [ ] `read_file` 读超预算窗口返回错误、读窄窗口成功（测试覆盖）
- [ ] `git status` 只列出 Scope 里的文件

## STOP conditions

- `sliceWithLineNumbersFromReader` 的实现与「每行 `N|content`」的假设不符（例如它对长行已有截断逻辑）。
- 豁免 `read_file` 后，TUI/网页的读取卡片依赖 `omitted_bytes` / `full_path` 元数据（先查
  `pkg/tool/format.go`、`frontend/src/components/chat/*`）；若依赖，先报告，不要顺手改界面。
- 25,000 token 预算让某个现存测试/验收脚本失败（说明有地方依赖"任意大文件都能读完"）。
- 同一处验证连续失败两次。

## Maintenance notes

- `read_file` 的预算是**token**，`shell`/web/MCP 的落盘阈值是**字节**（32 KB）。单位不同是刻意的，
  见 plan 270 的同名说明，不要统一。
- 以后若给 `read_file` 加 `allow_large` 之类的旁路，必须同时给出"何时允许"的判据，否则就是回到"静默截断"。
- 评审要点：确认预算检查发生在**行级截断之后**；确认 `read_file` 不再产生新文件。
- 已知遗留：一次 `read_file` 只给一个窗口，模型要看全文必须多次翻页；这是有意的（Claude Code 同）。
