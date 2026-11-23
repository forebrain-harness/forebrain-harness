# Plan 186：shell 卡的 edit-diff 归因必须跨进程（tool-write 台账落到机器级共享目录）

> 执行状态：**已实施 2026-10-29（in-tree，零 commit，改动留工作区）**。基线 `6f68cb21`。
> owner 执行期加了一条硬要求：「不同 home 的进程 本期就必须加上一并修复」——台账目录因此从
> `<home>/state/tool-writes` 改为**按用户的机器级目录**（见「修复设计」与「执行偏差」）。

## Status

- **Priority**: P1（owner 亲眼看到的错误信息：卡片把别的会话的改动写成这条命令做的）
- **Effort**: S（一个文件重写 + 三处注释 + 测试改写）
- **Risk**: LOW（唯一的运行时行为变化是「多进程写更不容易被误归因」；台账失败一律静默降级为无台账行为）
- **Category**: bug（数据/信息错误归因，跨进程）
- **Depends on**: 无。**与 plan 180 的关系**：180 建立了台账，但只覆盖同进程；本计划补上它缺的那半个作用域。

## Why this matters

owner 截图：一条 `CGO_ENABLED=1 go vet ./... 2>&1 | tail -20; echo "vet exit=$?"` 的卡（`Ran … 3.3s`，输出只有
`vet exit=0`）下方多出一个 diff 块 `Updated pkg/skill/git_install.go (+12 -0)`，内容是**另一个 forebrain TUI 进程里
并行会话**正在写的 `gitKillGrace` 注释。

这不是显示串台（不是渲染层拿错了数据），而是**工具自己给出的错误归因**：卡片上的 diff 全部来自工具输出
`shell_edit_diff`（`pkg/tool/diff_blocks.go:349 shellEditDiffBlocks` → `Verb: "Updated"`），而该字段由 shell 工具的
「命令前后两次 work-tree 快照」算出来。快照对只能回答「这段时间磁盘上变了什么」，回答不了「是谁改的」；本仓库
早就在 `plan 180` 承认了这一点并加了一条纠正通道 —— 但那条通道是**进程内**的，另一个进程的写入它看不见。

### 现场证据（owner 真实 home，只读查 `~/.forebrain/state/forebrain.state.sqlite`）

- 出错卡：`fb_messages.id=313591`，session `cli-9b62dc22-4b21-4cce-82fb-99e92464c601`，`role=tool`，
  `exec_started_at_ms=1793196537056` / `exec_finished_at_ms=1793196540362` / `exec_duration_ms=3306`
  ——— 与截图 `3.3s` 一致；`parts` 里的 body 正是 `Updated pkg/skill/git_install.go (+12 -0)` + 那段 diff。
- 真正的写入方：`fb_messages.id=313589/313590`，session `cli-b30752b5-a1b4-459a-99a4-7832643416ae`，
  `edit_file pkg/skill/git_install.go`，`exec_started_at_ms=1793196538155` / `exec_finished_at_ms=1793196539656`
  → **完全落在 go vet 的窗口内**（命令开始后 +1099 ms 开始，+2300 ms 结束）；该 tool 的 body 是
  `turn diff: /…/pkg/skill/git_install.go (+12/-0)` —— 与卡片里那段 diff 逐字相同。
- 两个 session 属于两个进程：`fb_run_owners` 里现存两个不同 owner（`tui-4d6ac6b2…`、`tui-bd34b06e…`），
  同一 home、同一 work tree —— 也就是 owner 的常规用法（两个 TUI 并行）。
- 反证「不是同进程漏记」：若两个会话在同一进程，`noteToolFileWrite` 会在 edit 结束时（1793196539656）记上该路径，
  而它在 go vet 窗口内，`dropExternalWrites` 会把它扣掉。实际没扣掉 ⇒ 台账没看到这次写入 ⇒ 跨进程。

## 根因（file:line 证据链）

1. 只有非只读命令才做快照对：`pkg/tool/shell_tool.go:255` `editDiff := beginShellEditDiff(ctx, cmd, wdAbs, readOnlyCommand)`。
2. `pkg/tool/shell_diff.go:1025` `beginShellEditDiff` 取 before 快照；`shell_diff.go:1052` `finish` 取 after 快照并用
   `diff-tree before after` 求差（`shell_diff.go:805 diffSince`）——两份快照之间**任何**来源的改动都会进来。
3. 唯一的纠正通道：

```go
// pkg/tool/shell_diff.go:1074
		external := r.externalWrites
		if external == nil {
			external = toolWritesBetween
		}
		// The snapshot pair answers "what changed on disk during the window",
		// not "what did this command change": subtract the paths …
		diff = dropExternalWrites(diff, external(r.startedAt, time.Now()))
```

4. 而 `toolWrites` 当时是**进程内** map：

```go
// pkg/tool/tool_writes.go:23（改动前）
var toolWrites = &toolWriteLedger{byPath: map[string]time.Time{}}
```

写入方是 file 工具的单一出口（`workspace.go:21` `noteToolFileWrite(absPath, time.Now())`，move-from 的例外在
`file_tools.go:1779`）。**结论**：另一进程的文件写入永远不会出现在 `toolWrites` 里，
`dropExternalWrites` 不扣，于是被记成这条 shell 命令的改动。

`plan 180` 的前提写在 `tool_writes.go` 头部注释里：「Tool calls do run together (plan 124 runs the concurrency-safe
ones in one batch)」—— 只考虑了同一批 tool call；真正的前提是**同一个 work tree 可以被两个 forebrain 进程同时写**
（TUI×TUI、TUI×gateway），而且这两个进程**不一定共用一个 home**（验收/CI 用临时 `FOREBRAIN_HOME`，owner 的双 TUI
共用 `~/.forebrain`）。修复的正是这个作用域。

## 修复设计（已实施）

把「某个 tool 写了这个路径」的公告从「进程内 map」换成**按用户的机器级 append-only 台账目录**，读写两侧都走同一个
目录，从而跨进程、跨 home 可见。**不引入 flock**（全仓无先例，且 Windows 需要额外文件），用「按分钟分片 + 只追加」
把并发降到单次 `write(2)`。

- 目录：`os.UserCacheDir()/forebrain/tool-writes`
  （macOS `~/Library/Caches/forebrain/tool-writes`、Linux `$XDG_CACHE_HOME|~/.cache`、Windows `%LocalAppData%`）。
  目录本身天然按用户隔离，不需要 uid 后缀，也不依赖 `FOREBRAIN_HOME` —— **两个 home 不同的 forebrain 写同一 work tree
  时也必须互相可见**（owner 执行期裁决）。
- 覆盖变量：`FOREBRAIN_TOOL_WRITES_DIR`（测试与隔离运行用；TestMain 无条件指向 scratch 目录）。
- 分片：`<unix 秒/60>.log`，一行一条：`<unixNano>\t<strconv.Quote(resolvedPath)>\n`。
- 写：`open(O_WRONLY|O_CREATE|O_APPEND, 0o600)` → 一次 `Write` 整行 → `close`。
  单次追加 ≤ 4 KiB 在 POSIX 上原子（`O_APPEND` 语义），Windows 上 Go 以 `FILE_APPEND_DATA` 打开、一次 `WriteFile`，
  多进程追加不会互相截断（POSIX 侧是明确语义，Windows 侧按打开方式推断、**本机未实测**，风险低：真丢了公告只退回
  「可能多归因」）。
- 读：`toolWritesBetween(from, to)` → `os.ReadDir` 目录，只看分钟号落在 `[from/60, to/60]` 的分片，逐行解析，
  保留 `at.After(from) && !at.After(to)`（与 plan 180 语义逐字一致），坏行/撕裂行跳过。
- 清理：写侧顺带（每进程每分钟最多一次，`toolWriteSweptAt` 节流）删除分钟号早于 `now - 30min - 1min` 的分片。
  分片一旦过了它那一分钟就不会再被任何人追加，**任何进程删除旧分片都是安全的**，不需要锁。
- 删除：`toolWriteMaxEntries`（内存 map 的上限，分片方案下无意义）、`toolWriteLedger` 类型、`pruneLocked`、`toolWrites`。
- 失败一律静默：cache 目录不可解析、`MkdirAll`/打开/写入失败、分片不可读 —— 全部当作「没有公告」，即无台账行为
  （可能多归因，绝不吞掉工具结果、绝不影响退出码）。
- `resolveForLedger` 保留不动（读写两侧必须用同一个路径规范化，`/var` vs `/private/var` 已有先例）。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 同进程兄弟 tool 在窗口内写文件（plan 180 场景） | 扣除 | 扣除（改走文件台账，语义不变） |
| **另一进程（任意 home）在窗口内写文件** | **被记成该命令的改动 ← 本 bug** | 扣除，卡片只显示命令输出 |
| 命令自己写文件、无并发 | 显示该文件 diff | 显示该文件 diff（`TestShellCardStillClaimsWhatTheCommandWrote` 钉住） |
| cache 目录不可解析/不可写 | — | 与今日相同（无公告，可能多归因），工具结果不受影响 |
| 台账清理 | 内存 map：30 min + 4096 条上限（每次写入 O(n) 排序） | 30 min 分片整片删除（无上限、无排序） |
| 进程退出 | map 随进程消失 | 分片留在 cache 目录，由后续任何进程按保留期删除 |

### 明确不做（Out of scope）

- **shell × shell 的跨进程重叠**：另一进程的 `gofmt -w` 仍然无法归因（它自己的写入集合就是它要算的 diff，循环依赖）。
  同进程的 shell 重叠已有 `diffNoteShared` 提示（`shell_diff.go:993 enter`），跨进程没有对应信号 —— 写进 Maintenance。
- 不碰渲染层（`pkg/tui`、gateway）、不碰 `Shared`/`TreeMoved` 语义、不碰快照算法本身、不给 file 工具加新的调用点。

## Steps（均已执行）

### Step 1 — 重写 `pkg/tool/tool_writes.go` ✅

两个函数签名不变（`noteToolFileWrite(absPath string, at time.Time)`、`toolWritesBetween(from, to time.Time) map[string]struct{}`），
`resolveForLedger` 不动；`toolWriteLedger`/`toolWrites`/`pruneLocked`/`toolWriteMaxEntries` 换成
`toolWriteLedgerDir()` / `appendToolFileWrite(dir, resolved, at)` / `sweepToolWriteShardsOnce` / `sweepToolWriteShards` /
`toolWriteShardName` / `toolWriteShardMinute`，外加常量 `toolWriteRetention = 30 * time.Minute`、
`toolWriteSweepEvery = time.Minute`、`toolWriteDirEnv`。文件头注释重写：跨进程/跨 home 的公告、分片与追加的原子性、
「失败静默」。

**验证**：`go build ./pkg/tool` OK；`grep -n 'toolWriteMaxEntries\|pruneLocked\|toolWriteLedger\b' pkg/tool/tool_writes.go` → 无输出。

### Step 2 — `pkg/tool` 的 TestMain pin 掉两个目录 ✅

`pkg/tool/session_tools_test.go` 的 `TestMain`：`memory.InstallDictionary` 之后为 `FOREBRAIN_HOME` 与
`FOREBRAIN_TOOL_WRITES_DIR` **各建一个 scratch 目录并无条件 `os.Setenv` 覆盖**（不是「只在未设置时填」），
`m.Run()` 结束后 `RemoveAll` 再 `os.Exit(code)`。

**唯一豁免**：`FB_TOOLWRITES_HELPER=1` 的子进程（本文件 Step 3 的跨进程助手）跳过覆盖，从而继承父进程传入的
`FOREBRAIN_TOOL_WRITES_DIR`；它拿到的目录本身就是父用例的 `t.TempDir()`，不会碰真实安装。

**验证**：`FOREBRAIN_HOME=/nonexistent/fb-home CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestToolWrites' -count=1`
→ `ok … 0.419s`（若 TestMain 没覆盖，台账写不进去，用例会红）；跑测试前后
`ls ~/.forebrain/state/tool-writes ~/Library/Caches/forebrain/tool-writes` 都不存在。

### Step 3 — 测试 ✅

- 保留 `TestToolWritesLedgerCoversEveryFileToolPath`（断言未改，钉「每个 file 工具写入路径都公告」）。
- 删 `TestToolWritesLedgerDropsOldestOverTheCap`（cap 概念消失），换成
  `TestToolWritesLedgerPrunesExpiredShards`：造一个 32 分钟前的分片 + 一条新写入触发清理，断言旧分片文件被删、
  旧条目不再返回、新条目可读。
- `TestToolWritesLedgerPrunesOldEntries` → `TestToolWritesLedgerIgnoresEntriesOutsideTheWindow`：窗口外（10 分钟前）
  的写入不返回、窗口内返回，且各自在「自己的窗口」里可见 —— 不再直接摸内部结构。
- 新增 `TestToolWritesLedgerIsVisiblySharedAcrossProcesses`：`exec.Command(os.Args[0], "-test.run=^TestToolWritesLedgerHelperProcess$")`
  的子进程（env 带 `FB_TOOLWRITES_HELPER=1` + `FOREBRAIN_TOOL_WRITES_DIR` + 路径 + 纳秒时间戳）跑真实的
  `noteToolFileWrite`，父进程 `toolWritesBetween` 必须看到它。助手用例非助手环境时 `t.Skip`。
- 新增 `shell_diff_test.go` `TestShellCardDoesNotClaimAFileAnotherProcessWrote`：`newShellDiffRepo(t)` →
  `t.Setenv(toolWriteDirEnv, t.TempDir())` → `beginShellEditDiff` → 磁盘上写 `git_install.go` →
  **同一个助手子进程**公告 → `finish` → `diff == nil`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'ToolWrites|ShellCard' -count=1 -v` → 全 PASS
（`TestShellCardDoesNotClaimAFileAnotherProcessWrote` 0.25 s、`…IsVisiblySharedAcrossProcesses` 0.05 s，
`TestShellCardStillClaimsWhatTheCommandWrote` 仍绿）。

### Step 4 — 注释同步（只改文字，不改逻辑）✅

- `pkg/tool/workspace.go`：公告是「任何 forebrain 进程都扣」的（plan 186）。
- `pkg/tool/shell_diff.go`：`finish` 里的相减注释改为「任何进程里的 forebrain 工具」；`externalWrites` 字段注释
  不再说「process-wide」。
- `pkg/tool/file_tools.go`：move-from 的 plan 180 注释补上「this process or another」。

### Step 5 — 检查 ✅

| 命令 | 结果 |
|---|---|
| `go vet ./...` | 无输出 |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | `ok … 12.499s` |
| `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` | 见下方「验收记录」的套件一节 |
| 跑完 `ls ~/Library/Caches/forebrain/tool-writes` | 只有验收进程写的那一个分片（测试自己的目录被 TestMain 指向 scratch） |

### Step 6 — tmux 真机验收（两个进程、两个不同 home、同一个临时 git 仓库）✅

两个 TUI 各一个 tmux session、**各自独立的 `FOREBRAIN_HOME`**（`/tmp/fbt-acc/p1/home`、`/tmp/fbt-acc/p2/home`，
两份独立的 `forebrain.state.sqlite`）、cwd 都是同一个临时 git 仓库 `/tmp/fbt-acc/repo`（`git init` + 一条 commit）。
用户目录侧共用的只有台账目录 `~/Library/Caches/forebrain/tool-writes`。

- **场景 A（修复后，本 bug）**：P1 发 `touch /tmp/fbt-acc/marker && sleep 6`（非只读命令、不碰仓库文件）；
  marker 出现后 1.5 s，P2 发 `read_file` + `edit_file a.go`。P1 的卡：

  ```
  ● Ran touch /tmp/fbt-acc/marker && sleep 6 6.2s
    └ (no output)
  ```

  —— **没有** `Updated`/`Created`/`Deleted` 行、没有 ```diff 块。P2 的卡是
  `● Edited /tmp/fbt-acc/repo/a.go └ Added 1 line, removed 1 line`。
  生产端证据（`p1/home/state/forebrain.state.sqlite`，那条 shell 的 `parts[].body`）：
  `… input: ```json {"command": "touch /tmp/fbt-acc/marker && sleep 6"}```  output: ```json {…}``` `
  —— 输入与输出之间没有任何 diff 段。
- **场景 B（对照，旧二进制 = `git archive 6f68cb21` 抽到 `/tmp/fbt-acc/old-src` 构建，不是 `git worktree`）**：
  同一场景、同样两个不同 home。旧二进制 P1 的卡：

  ```
  ● Ran touch /tmp/fbt-acc/marker && sleep 6 6.2s
    └ Updated a.go (+1 -1)
        1 - package main
        1 + package main // edited by another process
  ```

  同一行在 `q1/home/state/forebrain.state.sqlite` 的 `parts[].body` 里逐字存在 —— **bug 复现**，说明验收场景与现场等价。
- **场景 C（回归，修复后单进程）**：
  - `printf 'x\n' >> tracked.txt` → `● Ran printf 'x\n' >> tracked.txt 0.2s └ Updated tracked.txt (+1 -0)`；
  - `git status --short`（只读命令）→ 卡上只有 `└ M tracked.txt`，**无** diff 块；
  - `edit_file` 卡的 diff 不变（场景 A 的 P2 卡即为此）。

**台账证据**：`~/Library/Caches/forebrain/tool-writes/29887170.log` 一行
`1793230204222774000	"/private/tmp/fbt-acc/repo/a.go"` —— 写方是 P2（home `p2/home`），读方是 P1（home `p1/home`），
路径已按 `resolveForLedger` 规范化（`/tmp` → `/private/tmp`）。

### Step 7 — 收口 ✅

- 本文件落到 `plans/186-tool-write-ledger-spans-processes.md`。
- `plans/README.md` 三处：`## 最新审核信息` 一条、`## 计划列表` 表行、`## 执行顺序` 追加一行。
- `git status` 只剩本计划 Scope 内文件（源码 7 个 + 计划 2 个）；**未 commit**。

## Done criteria（机器可查，已跑）

```bash
grep -n 'toolWriteMaxEntries\|pruneLocked\|toolWriteLedger\b' pkg/tool/tool_writes.go   # 无输出
grep -n 'FOREBRAIN_TOOL_WRITES_DIR\|FOREBRAIN_HOME' pkg/tool/session_tools_test.go      # TestMain 里的覆盖式 pin
grep -rn 'toolWritesBetween' pkg/tool/*.go                                              # 定义 + shell_diff 默认值 + 测试
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'ToolWrites' -count=1                  # PASS（含跨进程子进程用例）
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1                                    # PASS
go vet ./...                                                                            # 无输出
```

外加场景 A/B/C 的 `tmux capture-pane` 证据与两侧 DB 的 `parts[].body`；`~/.forebrain/state/tool-writes` 全程不存在。

## 验收记录：`./pkg/... ./cmd/forebrain` 全量

`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`：
**30 个包绿**，含全部与本计划相关的包（`pkg/tool` 16.1 s、`pkg/tui` 69.6 s、`pkg/run` 38.9 s、`pkg/turn`、
`cmd/forebrain`）。

**另有两处基线自带红**（`ea6bedf4` / 办公产物那批遗留，与本计划无关）。owner 追加要求「基线自带的两处红必须修复」
后已在同一工作区一并修好（零 commit）：

1. `pkg/llm/usage_snapshot_record_test.go`（`ea6bedf4` 引入）三条架构红：
   `TestRunnerOnlyShrinks`（`Runner has 28 fields; baseline is 27`）、
   `TestFileNamesHaveAtMostTwoUnderscores`（文件名三个下划线）、
   `TestTestFilesCorrespondToProductionFiles`（没有同名生产文件）。
   - 改名：`pkg/llm/usage_snapshot_record_test.go` → `pkg/llm/stream_test.go` —— 它测的 `UsageSnapshotRecord`
     就定义在 `stream.go:131-165`，文件名 1 个下划线、与生产文件同名，后两条一次解决。
   - 基线：`pkg/architecture/cache_test.go` 的 `runnerFields` 27→28，并按该常量块**既有惯例**（26→27 时为
     `sessionPerm` 写的那段）补 documented reason：`attemptLedger` 是 plan 181 的每次调用计数器，一个实例服务
     会话链、压缩摘要链与图片拒绝记录器，挂在 Runner 而不是某条链上。**不是拔掉测试，也不是删字段**——原字段
     （`pkg/run/runner.go:178`）的语义不动；这里沿用「增加必须在同一个注释块里被记录」的既有契约。
2. `pkg/llm` 的 `TestJSONSchemaDescriptionTagsSurviveReflection`：`pkg/tool/artifacts.go` 的 `presentFileInput` /
   `presentFilesInput` 把带逗号的描述搬进 `jsonschema_description:`（`minItems`/`maxItems` 留在 `jsonschema:`）。
   invopop 的 `reflect.go:613` 直接 `t.Description = f.Tag.Get("jsonschema_description")`，`genericKeywords` 的
   `case "description"` 写的是同一个 `Schema.Description` 字段，所以这次改动**只**改变逗号之后的文字是否可见。
   **实测（`-overlay` 把 `pkg/tool/artifacts.go` 换回 HEAD 内容，同一 dump 用例对比）**：
   - HEAD：`"path": "The file"`、`"title": "A short human title for the card"`、`"files": "Every final deliverable of this task"`
   - 改后：三处都是完整句子（`The file, relative to the project directory or an absolute path inside it.` 等）
   —— 即模型本来就该看到、却一直被截断的那半句。副作用：`present_files` 的输入 schema 是模型可见文本，
   办公/跑批会话的缓存前缀会**位移一次**（与 `pkg/safety/permission_types.go` 早先同样的修法一致）；`present_files`
   只在办公/跑批会话注册，编码会话不受影响。

修完复跑：`pkg/architecture`、`pkg/llm`、`pkg/tool`、`pkg/agent`、`pkg/turn`、`pkg/run` 全绿；`go vet ./...` 无输出。

## STOP conditions（逐条核对，均未触发）

1. `home.Root()` 解析不出来 —— 已改用 `os.UserCacheDir()`，验收里真实解析成功（分片就落在 cache 目录）。
2. 测试期间真实台账目录出现文件 —— 未发生：TestMain 无条件覆盖 `FOREBRAIN_TOOL_WRITES_DIR`，
   `FOREBRAIN_HOME=/nonexistent/fb-home` 下跑同一批用例仍 PASS。
3. 现有 `pkg/tool` 因台账改写而红的用例 —— 只有计划内列出的三条被改写，其余全绿。
4. 旧二进制复现不出错误 diff —— 复现出了（场景 B）。
5. 改动文件超出 Scope —— 只动了 `pkg/tool/{tool_writes.go,tool_writes_test.go,session_tools_test.go,shell_diff.go,shell_diff_test.go,workspace.go,file_tools.go}`。

## Maintenance notes

- **为什么不 flock**：全仓无先例，且 Windows 要另写一套。分片方案把跨进程写降成「单次 O_APPEND 追加」，
  分片过期后不再被任何人追加，删除旧分片天然安全。**不要**改成「一个共享文件 + 就地压缩」——
  那会引入 rename/截断与追加的竞争，丢一条公告就退回本 bug。
- **为什么不落 state DB（sqlite）**：写入点在 file 工具的热路径上，而 `pkg/tool` 没有现成的共享
  `*sql.DB` 句柄——要落库就得加一个按 home 缓存的包级句柄、schema 迁移和关闭时机；而 state DB 是**按 home** 的，
  跨 home 的进程又会互相看不见，等于没修。文件追加零依赖，格式只有「时间 + 路径」，丢了也只退回「可能多归因」。
- **为什么是用户级目录而不是 home 内**：台账的作用域必须 ≤ 它能保护的作用域。work tree 不属于任何 home
  （两个 home 的进程可以写同一棵树），所以台账也不能属于任何 home。`os.UserCacheDir()` 是不用自建 uid 隔离的
  用户级目录（Windows 上是 `%LocalAppData%`，没有 `os.Getuid()` 那种 build-tag 麻烦）。
- **公告晚于写入的窄窗**：台账在写入完成时追加，读侧若在这几毫秒之间就读到了，仍会多归因。
  不要为了关掉这个窗口改成「写前公告」——那会扣掉工具其实没改的路径（反向错误）。
- **shell × shell 跨进程**仍无法归因（见 Out of scope）。若将来要做，得让每条命令在结束时公告自己的改动集合，
  且要处理「谁先到」的对称性——是独立设计，别塞进本计划。
- 台账里只有「绝对路径 + 时间」，不含内容，不涉及隐私；分片按分钟命名，一分钟最多一个文件。
