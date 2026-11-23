# Plan 180：shell 卡的「改动了哪些文件」只能算这条命令自己改的

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func (r \*shellEditDiffRun) finish" pkg/tool/shell_diff.go` 为 `1`；
> `grep -c "func (r \*shellDiffRepo) diffSince" pkg/tool/shell_diff.go` 为 `1`；
> `grep -c "func attachTurnDiff" pkg/tool/workspace.go` 为 `1`；
> `grep -c "func Build(path string, before, after \[\]byte)" pkg/event/turndiff.go` 为 `1`；
> `grep -c "difflib.SplitLines(string(before))" pkg/event/turndiff.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tool/shell_diff.go pkg/tool/workspace.go pkg/event/turndiff.go pkg/tool/file_tools.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/event ./pkg/tui -count=1`（超时给足 900 s）。

## Status

- **Priority**: P1（卡片把别人的改动算在自己头上，读者据此判断"这条命令做了什么"）
- **Effort**: M（两处根因：归因窗口 + 新/删文件的幻影行）
- **Risk**: MED（动 shell 卡的事实来源与**所有** `event.Build` 的 diff 计数）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（事实错误）×2

## 先回答 owner 的问题

截图里那张 `Ran cd … && go test …` 卡的 output 区下面出现
`Created plans/177-subagent-views-show-first-token.md (+338 -1)` 与一个 diff 块。**结论：是 bug，而且有两层。**

1. **"这段输出区里为什么会有改动卡"——这一层不是 bug。** shell 卡的正文按设计由三段组成：
   命令输出 → 截断底栏 → **它改动的文件**（`pkg/tool/diff_blocks.go` 的 `shellEditDiffBlocks`；
   设计见计划 130/132/137，模型面那侧另有 `drop` 白名单 `pkg/tool/format.go:3634`，模型看不到这段）。
2. **"这条 `go test` 凭什么说自己创建了 `plans/177…md`"——这是真 bug（归因错误）。**
   `plans/177…md` 是**另一个工具**（`write_file`）在同一个时间窗内写的：并行工具调用是常态
   （计划 124 让"可安全并发的调用一起跑"），而 shell 卡的改动集是**纯时间窗 diff**（命令前快照 vs 命令后快照），
   窗口里别人写的东西全算在这条命令头上。
3. **`(+338 -1)` 里的 `-1` 也是 bug。** 一个新建文件不可能删掉 1 行；那一行是 diff 库对空字符串产出的
   "一个空行"，所以卡片上还多出一条红色 `1 -` 行。

## 根因 A：改动集是"时间窗"，不是"因果"

```go
// pkg/tool/shell_diff.go:1032-1042（beginShellEditDiff，命令开始前）
	run := &shellEditDiffRun{repo: repo, token: &shellDiffToken{}, cwd: cwd}
	repo.enter(run.token)
	repo.mu.Lock()
	before, err := repo.snapshot(ctx)      // ← 命令开始那一刻的工作树
	repo.mu.Unlock()
```

```go
// pkg/tool/shell_diff.go:1048-1080（finish，命令结束后）
	if r.skipped { … }
	r.repo.mu.Lock()
	diff, err := r.repo.diffSince(ctx, r.before, r.cwd)   // ← 现在的工作树，两者相减
	r.repo.mu.Unlock()
	…
	diff.Shared = r.shared
```

于是 `diff` 回答的是"这段时间里磁盘上多了什么"，而卡片把它呈现为"这条命令改了什么"。
并发写盘的来源里，**只有另一种 shell 命令**被承认过：

```go
// pkg/tool/shell_diff.go:987-1000（enter：只登记 shell run 自己）
func (r *shellDiffRepo) enter(t *shellDiffToken) {
	…
	r.inflight[t] = false
	if len(r.inflight) > 1 {          // 第二条 shell 并行 ⇒ 全部标 shared
		for k := range r.inflight { r.inflight[k] = true }
	}
}
```

`enter` 的唯一调用者就是 `beginShellEditDiff`，所以**文件工具（`write_file` / `edit_file` / `apply_patch`）
从不登记**，它们落在窗口里的写入被 100% 栽给 shell 命令。设计其实已经承认过这个问题类别——
`pkg/tool/diff_blocks.go:40` 的注释文案就是：

```go
	diffNoteShared = "(another command ran in this repository at the same time; a change made by either may show under either result)"
```

——只是它只覆盖了 shell×shell，而且即便命中，卡片**仍然把文件列表照画一遍**，只是末尾多一行免责。

### 文件工具的写盘只有一个收口（这是修法的落点）

三个写文件的工具（`write_file` / `edit_file` / `apply_patch`）在真正写完之后都会经过同一个 helper：

```go
// pkg/tool/workspace.go:8-16
func attachTurnDiff(output map[string]any, absPath string, before, after []byte) {
	if output == nil { return }
	summary, err := event.Build(absPath, before, after)
	if err != nil { return }
	output["turn_diff"] = summary
}
```

调用点：`pkg/tool/file_tools.go:944`（write_file）、`:1176`（edit_file）、`:1811`（apply_patch，逐个 op）。

## 根因 B：新/删文件的 diff 多一个幻影行

```go
// pkg/event/turndiff.go:16-23
func Build(path string, before, after []byte) (Summary, error) {
	diffText, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(before)),
		B:        difflib.SplitLines(string(after)),
		…
```

`go-difflib` 的 `SplitLines("")` 返回**一个空行**（`strings.SplitAfter("", "\n")` → `[""]`，
末元素补 `"\n"` ⇒ `["\n"]`，实测见 `github.com/pmezard/go-difflib/difflib@v1.0.0/difflib.go:768-772`）。
于是"新建文件"的 A 侧是 1 行空行 ⇒ 计数 `Deleted=1`，并画出一条 `1 -`。截图里的 `(+338 -1)` 与那条红行就是它。
反向（删除文件）同理：`Added=1`。

受同一个 `Build` 影响的地方还有：文件编辑卡的 `turn_diff`、终端审批预览
（`pkg/tui/overlays.go:2030` 对新建文件传 `[]byte("")`）、`kind: rewind` 的差异、技能版本对比
（`pkg/tui/skills_versions.go:180,286`）——所以修在 `Build` 里是**一处改、所有卡片一致**。

## Scope

**In scope（只改这些）**：
- `pkg/tool/tool_writes.go`（**新建**：文件工具写入日志 `noteToolFileWrite` / `toolWritesBetween`）
- `pkg/tool/workspace.go`（`attachTurnDiff` 里登记这次写）
- `pkg/tool/file_tools.go`（只在**删除/移动**等没有 `attachTurnDiff` 的路径补登记，Step 3 会定位）
- `pkg/tool/shell_diff.go`（`shellEditDiffRun` 记开始时刻 + `finish` 减去同窗内别人写的路径；`ChangedFiles`/`MoreFiles` 同步）
- `pkg/event/turndiff.go`（空侧不拆行）
- 测试：`pkg/tool/tool_writes_test.go`（新建）、`pkg/tool/shell_diff_test.go`、`pkg/event/turndiff_test.go`

**Out of scope（看着相关也不要动）**：
- "shell 卡正文里该不该有改动段"——按 130/132/137 的设计保留
- `diffNoteShared` 的 shell×shell 语义（保留；本计划只让它不再被文件工具误触发）
- `shellDiffMaxFiles` / `shellDiffMaxHunkLines` 等上限（新建文件 338 行在 400 行上限内，属设计）
- 网页侧（`ShellCard`/`FileChangeList` 的渲染；它读同一份事实，本计划修好上游即两侧同时正确）
- `present_files` 生成物、MCP 工具、附件临时文件（不在工作树里）
- 独立进程（用户自己的编辑器、后台构建）改的文件——**无法**归因，保持现状（见 Maintenance notes）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 归因定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'ShellCard.*OtherTool\|ToolWrites' -count=1 -v` | PASS |
| diff 计数定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/event -run 'BuildOfANewFile\|BuildOfADeletedFile' -count=1 -v` | PASS |
| 三包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/event ./pkg/tui -count=1` | 与基线相比无新增 FAIL |
| 更广回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/gateway ./pkg/turn -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet / gofmt | `CGO_ENABLED=1 go build -tags fts5 ./...`；`go vet ./pkg/tool ./pkg/event`；`gofmt -l pkg` | exit 0 / 无输出 |

## Steps

### Step 1：先写归因的会红测试（用注入的"外部写"来源，不依赖真并发）

`pkg/tool/shell_diff_test.go` 新增。壳里先给 `shellEditDiffRun` 一个可注入的读取口
（Step 2 才实现，所以此步之后**编译失败也算红**，报告里要写清"红是编译红还是断言红"）：

```go
// The card reports what THIS command changed. A path another tool wrote inside
// the same window is not this command's work — parallel tool calls are normal
// (plan 124), so the window alone cannot decide it.
func TestShellCardDoesNotClaimAFileAnotherToolWrote(t *testing.T) {
	ctx := context.Background()
	root := newShellDiffRepo(t)                 // 同文件既有 helper
	run := beginShellEditDiff(ctx, "go test ./...", root, false)
	file := filepath.Join(root, "plans", "177-x.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte("# plan\n"), 0o644))
	noteToolFileWrite(file, time.Now())          // 另一个工具刚刚写了它
	diff := run.finish(ctx, "go test ./...", 0, nil)
	if diff != nil && len(diff.Files) > 0 {
		t.Fatalf("the card claimed another tool's write: %+v", diff.Files)
	}
}

// …and it still reports the file the command itself wrote, so the fix is not a
// blanket suppression.
func TestShellCardStillClaimsWhatTheCommandWrote(t *testing.T) { …（不调 noteToolFileWrite，断言 len(diff.Files)==1 且 Status=="created"）… }
```

**Verify**：`… -run 'ShellCard.*OtherTool|ShellCardStillClaims' -count=1 -v` → 第一条红、第二条绿
（第一条今天把那个文件报成 `created`）。

### Step 2：写入日志 + finish 里相减

新建 `pkg/tool/tool_writes.go`（**整份都是新的**，注释写清为什么存在）：

```go
// Tool writes: the ledger that lets a shell card say what ITS command changed.
//
// A shell card's "files it changed" is a snapshot pair — the work tree before
// the command and after it — and a snapshot pair cannot tell the command's own
// writes from a sibling tool's. Tool calls do run together (plan 124 runs the
// concurrency-safe ones in one batch), so a write_file landing inside a
// two-minute `go test` used to be reported as that command's creation. Every
// file-writing tool therefore announces the path it just wrote, and a shell
// card subtracts anything announced inside its own window.

type toolWriteLedger struct {
	mu    sync.Mutex
	byPath map[string]time.Time // resolved path → when a tool last wrote it
}

var toolWrites = &toolWriteLedger{byPath: map[string]time.Time{}}

const (
	// toolWriteRetention is how long an entry is kept: long enough to cover the
	// longest shell command a reader would wait for, short enough that the map
	// cannot grow without bound.
	toolWriteRetention = 30 * time.Minute
	toolWriteMaxEntries = 4096
)

// noteToolFileWrite records that a tool (not a shell command) wrote absPath at
// at. Called from the one place every file-writing tool passes through.
func noteToolFileWrite(absPath string, at time.Time) { … }

// toolWritesBetween returns the resolved paths a tool wrote in (from, to].
func toolWritesBetween(from, to time.Time) map[string]struct{} { … }

// resolveForLedger resolves the PARENT directory's symlinks (the file itself may
// not exist after a delete) so the ledger and the shell snapshot name a path the
// same way: this repository has been bitten by /var vs /private/var before.
func resolveForLedger(absPath string) string { … }
```

- `noteToolFileWrite` 里顺手清过期条目与超上限（按时间排序丢最旧）。
- `workspace.go` 的 `attachTurnDiff` 末尾加一行 `noteToolFileWrite(absPath, time.Now())`
  （它一次调用 == 一个工具刚刚改完这个路径）。
- `shell_diff.go`：`shellEditDiffRun` 增 `startedAt time.Time`（`beginShellEditDiff` 里 `time.Now()`）；
  `finish` 在拿到 `diff` 之后：

```go
	if diff != nil {
		external := r.externalWrites   // 默认 toolWritesBetween；测试可注入
		file, changed, more := dropExternalWrites(diff, external(r.startedAt, time.Now()))
		…
	}
```

- `dropExternalWrites`：按 `resolveForLedger(f.Path)` 命中则从 `Files` 移除、
  从 `ChangedFiles` 移除、`MoreFiles` 相应减少（不许变成负数）。全被移除时返回 `nil`
  （卡片就只剩命令输出——那正是 `go test` 的真相），**不要**加任何新文案。

**Verify**：Step 1 的两条转绿；`grep -c "noteToolFileWrite" pkg/tool/workspace.go` 为 `1`。

### Step 3：删除/移动路径的补登记

`grep -n "os.Remove(abs)\|os.Rename(" pkg/tool/file_tools.go` 找到 `apply_patch` 的删除/移动分支
（`:1746`、`:1772`、`:987`），确认**它们是否也走 `attachTurnDiff`**：

- 走 ⇒ 无需改动（Step 2 已经覆盖）；
- 不走 ⇒ 在该路径写成功后补 `noteToolFileWrite(<abs>, time.Now())`，并在报告里列出补了哪几处。

**Verify**：新增 `TestToolWritesLedgerCoversEveryFileToolPath`——对 `write_file`/`edit_file`/`apply_patch`
三个工具的"创建、修改、删除、移动"各跑一次（尽量用既有测试脚手架；太重的用 table 直接调
`attachTurnDiff` 与删除分支的 helper），断言每次之后 `toolWritesBetween(开始前, 现在)` 都含该路径。

### Step 4：新建/删除文件不再多一行

`pkg/event/turndiff.go`：

```go
// splitDiffSide splits a side of the diff. An empty side has NO lines: feeding
// difflib.SplitLines("") instead yields one empty line, which made every created
// file report a deleted line and draw a phantom "1 -" row (and every deleted
// file a phantom "+1").
func splitDiffSide(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return difflib.SplitLines(string(b))
}
```

把 `A:`/`B:` 换成 `splitDiffSide(before)` / `splitDiffSide(after)`。

**Verify**：`pkg/event/turndiff_test.go` 新增两条：

```go
func TestBuildOfANewFileCountsNoDeletion(t *testing.T) {
	sum, err := Build("x.md", nil, []byte("a\nb\n"))
	require.NoError(t, err)
	require.Equal(t, 2, sum.Added)
	require.Zero(t, sum.Deleted)
	require.NotContains(t, sum.UnifiedDiff, "\n-")
}
func TestBuildOfADeletedFileCountsNoAddition(t *testing.T) { …Added==0, Deleted==2, 无 "+" 行… }
```

（这两条在改之前必然红：`Deleted==1` / `Added==1`。）

### Step 5：端到端确认卡片计数（含 git 侧）

`pkg/tool/shell_diff_test.go` 新增一条走真快照的用例：在临时仓库里写入一个新文件、跑一次
`beginShellEditDiff`/`finish`（**不**调 `noteToolFileWrite`），断言
`Files[0].Status == "created"`、`Added == <行数>`、`Deleted == 0`。

**Verify**：`… -run 'ShellCard' -count=1 -v` 全绿。

### Step 6：回归

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/event ./pkg/tui ./pkg/run ./pkg/gateway ./pkg/turn -count=1`
→ 与基线相比无新增 FAIL。特别确认既有的 shell 卡用例（`TestParseShellCard…`、
`TestDiffBlocks…`、shell_diff 全套）与 `pkg/tui` 的审批预览/技能版本对比仍绿。

## Test plan

- `pkg/tool`：`TestShellCardDoesNotClaimAFileAnotherToolWrote`、
  `TestShellCardStillClaimsWhatTheCommandWrote`、`TestToolWritesLedgerCoversEveryFileToolPath`、
  `TestShellCardCountsACreatedFileWithNoDeletedLine`、`TestToolWritesLedgerPrunesOldEntries`。
- `pkg/event`：`TestBuildOfANewFileCountsNoDeletion`、`TestBuildOfADeletedFileCountsNoAddition`。
- 结构照抄：`pkg/tool/shell_diff_test.go` 的 `newShellDiffRepo`（建仓 + 清理）。
- 不新增并发测试：注入 seam 已经把"窗口里有别人的写"表达清楚，真并发只增加 flaky。

## Done criteria

机器可检查，全部满足：

- [ ] `grep -c "func noteToolFileWrite" pkg/tool/tool_writes.go` 为 `1`；`grep -c "noteToolFileWrite(absPath" pkg/tool/workspace.go` 为 `1`
- [ ] `grep -c "func splitDiffSide" pkg/event/turndiff.go` 为 `1`；`grep -c "difflib.SplitLines(string(" pkg/event/turndiff.go` 为 `0`
- [ ] `grep -c "dropExternalWrites" pkg/tool/shell_diff.go` ≥ `2`（定义 + 调用）
- [ ] 上面 7 条新测试 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/event ./pkg/tui ./pkg/run ./pkg/gateway ./pkg/turn -count=1` 与基线相比无新增 FAIL
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`go vet ./pkg/tool ./pkg/event` exit 0；`gofmt -l pkg` 无输出
- [ ] 真机：按 `.claude/skills/run-forebrain/SKILL.md` 起隔离 home + fake provider，让一个**长 sleep 的 shell 与一次 write_file 并发**，
      截图/`capture-pane -e` 确认那张 shell 卡**没有**列出 write_file 的文件；再跑一条真正写文件的 shell，确认它**仍然**列出（写进报告）
- [ ] `git status --porcelain` 只显示 Scope 里的文件（外加 `pkg/gateway/dist/` 的构建产物）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. `attachTurnDiff` 并不是三个写文件工具的唯一收口（`grep -rn "os.WriteFile\|os.Rename" pkg/tool/file_tools.go`
   出现没经过它的写盘）：把清单贴出来再决定往哪补登记，**不要**在 shell 侧做"路径白名单"糊过去。
4. 真机复现不出"另一工具的写落在 shell 窗口内"（并行被限流成串行）：把实测的两条工具时间线贴回来——
   那说明本计划的①不再可达，只需要做 Step 4（幻影行）；不要为了对齐计划去人为拼并发。
5. 修完 Step 2 之后，一条**真在写文件**的 shell 命令的改动段消失了：说明日志把 shell 自己的写也算了进去
   （shell 派生进程不会调 `noteToolFileWrite`，所以出现这种情况一定是别处误登记），报告调用点，别调阈值。
6. 你想顺手把 `diffNoteShared` 的文案改短或删掉：**不要**，它是 shell×shell 重叠时唯一的老实交代。
7. 你想顺带给新建文件加"只显示摘要、不画 338 行 diff"：那是另一个设计决定（本计划只修事实与计数），
   报告给 owner 另立。

## Maintenance notes

- 规则一句话：**卡片的改动集必须来自"谁改的"，不能来自"什么时候改的"。** 任何新的写盘工具
  （另一个文件工具、生成物落盘、脚本化导出）都必须在写成功后调 `noteToolFileWrite`，
  否则它会被并发的 shell 卡冒领。
- 已知边界（本计划**不**解决，也不假装解决）：另一个**进程**（用户的编辑器、后台构建、另一个 forebrain 进程）
  在窗口内改的文件无法归因，仍会出现在卡片上。要解决它需要 OS 级 watcher（`fsnotify` 之类）记录 pid/工具归属，
  属独立计划；`diffNoteShared` 那条文案目前是这个边界上唯一的诚实标记（只在 shell×shell 时触发）。
- 与 `178`/`179` 的关系：那两份说的是"面板几何必须由 widget/内容决定，不由剩余空间决定"，
  本计划是同一原则在数据上的一版——**事实来自因果，不来自时间窗**。
- 新的 `event.Build` 语义（空侧零行）会让历史上"新建文件 -1"的数字消失；如果哪里存过这类旧值，
  它们不会自愈（本仓没有这种持久化，确认过 `turn_diff` 只在事件/展示里流转）。
