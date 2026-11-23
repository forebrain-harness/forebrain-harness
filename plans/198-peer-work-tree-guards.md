# 198 — 同树护栏：身份台账、同树概况提醒、写入提示、危险 git 审批、失败归因

- **基线**：`202ed4f2`（dev）+ 194–197 落地后的工作区（只硬依赖 195；排在 197 之后做，可以复用 `PeerDirectory` 端口）。
- **状态**：TODO
- **依赖**：195（建议在 197 之后）
- **执行隔离**：in-tree，**零 commit**
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` §6

> **执行者须知**：逐步执行，每步跑验证；碰到 §9 STOP 条件就停下报告。完成后更新 `plans/README.md` 本行状态。
>
> **Drift check（先跑）**：`git diff --stat 202ed4f2 -- pkg/tool/tool_writes.go pkg/tool/workspace.go pkg/tool/file_tools.go pkg/tool/shell_tool.go pkg/safety/shell.go pkg/safety/engine.go pkg/run/config.go pkg/run/runner.go`，核对 §2 摘录；不符就 STOP。

## 0. 背景与 owner 裁决（全族共用，执行期不得再改）

多个代理在同一工作树里并行干活。owner 2026-10-30 裁决 3（原话选项）：**「提醒 + 危险 git 需审批」**——每轮开头给模型一条同树代理概况；写一个别的在线代理近期改过的文件时照常写入、结果里附提示；会冲掉他人未提交改动的 git 命令（stash、checkout/restore 文件、reset --hard、clean、commit -a/add -A 等）在有其他在线代理时**必须审批**；命令失败时附上执行期间别的代理改过的文件，帮模型归因。这些是 forebrain 独有的护栏（Claude Code 没有对应物），与「线协议对齐 Claude Code」不冲突。

「必须审批」的执行语义（owner 2026-10-30 追加裁决）：**完全访问（Full Access / YOLO，审批策略 never）下也强制弹审批**，与 `exit_plan_mode`、用户交互类工具在 YOLO 下必须弹审批同属一类例外；不是硬拒绝。跑批会话没有人可问，沿用跑批既有语义（任务沙箱只写本次执行工作区，`.git` 写不进去），本计划不为跑批加分支。

## 1. 共享架构（摘要）

- 只有 `pkg/process` 能 import `pkg/peer`。`pkg/tool`（扇出超标）通过 197 定义的端口 `tool.PeerDirectory` 拿同树信息；本计划给该接口**追加**护栏方法。`pkg/run`（扇出超标）的提醒包装器同样只用这个端口。
- `pkg/safety` 加一个**纯函数**分类器（不 import 新包）。
- 台账：机器级、跨 home、按用户的 append-only 分片目录，原则与 plan 186 的 `tool-writes` 完全一致，但**另立目录**（旧版本读者不认多列的行）。

## 2. 现状事实（已对基线核实）

- 186 台账：`pkg/tool/tool_writes.go:56-69` 目录（`os.UserCacheDir()/forebrain/tool-writes`，覆盖变量 `FOREBRAIN_TOOL_WRITES_DIR`）；`:88-99` `noteToolFileWrite(absPath, at)`；`:105-124` 一行 `纳秒\t引用路径`、`O_APPEND` 0600、每分钟一片；`:139-159` 30 分钟保留与限频清扫；`:161-200` 读区间；`:202-210` `resolveForLedger`。写入点：`pkg/tool/file_tools.go:1780`、`pkg/tool/workspace.go:22`（「the one place every file-writing tool passes through」）。
- LSP 迟到提醒包装器（照它写）：`pkg/run/config.go:1438-1461` `wrapLSPDiagnosticsReminderLLM` / `Execute`——`PeekLate` 有内容才在请求末尾追加 `planReminderMessage(text)`（`config.go:1419-1426`，包成 `<system-reminder>`、`IsMeta`），`recordReminderAdoption` 交给编排循环，调用成功后 `AckLate`。安装在 `pkg/run/runner.go:1116-1122`（注释解释为何放在 summaryChain 之外）。
- shell 审批：`pkg/tool/shell_tool.go:1121-1146` `shellAssessmentRequiresApproval`；`:1166-1190` `shellAssessmentApprovalPayload` 写 `force_tool_approval`、`approval_reason`、`dangerous_rule`；`pkg/run/runner.go:460` `forceToolApproval := exitPlanMode || payloadForcesToolApproval(pay)`。
- YOLO 下仍要问的现有机制（本计划要泛化它）：`pkg/run/runner.go:421` `exitPlanMode := strings.EqualFold(kind, "exit_plan_mode")`；`:516` `if !exitPlanMode && decision.Behavior != safety.BehaviorDeny && r.yoloEnabled() { … return "", false, nil }`（YOLO 直接放行，exit_plan_mode 除外）；`:522` `if !exitPlanMode && forceToolApproval && approvalPolicy.Mode == safety.ApprovalNever { return …"permission denied: approval policy is never" }`（never 策略下强制审批变拒绝，exit_plan_mode 除外）；`:439` 跑批（`tool.TaskWorkspaceFromContext(ctx) != ""`）一律不问；`:547` 流式派发时 exit_plan_mode 推迟到流结束再问。
- shell 结果：`pkg/tool/shell_tool.go:654-690`——`exitCode`、给模型的 `resultPayload`（`stdout/stderr/exit_code`…，JSON）与给卡片的 `completionOutput` 分开；**只往 `resultPayload` 加字段**（卡片不能出现原始 JSON）。
- git 分类：`pkg/safety/shell.go:2341-2393`（`stash` 非 `list` → Writes、`checkout/restore/reset/clean/commit/add…` → Writes）；危险命令匹配器 `:514-578`（`dangerousCommandFromWords`，会拆 `sudo`/`env`/`sh -c` 包装）——新分类器照它的拆包装写法。
- 预设：`pkg/safety/permission_types.go:860-866` Full Access＝`ApprovalNever` + `danger-full-access`。
- 文件数：`pkg/tool` 197 后 30（上限 31）；`pkg/safety` 20；`pkg/run` 26。

## 3. 设计

### 3.1 端口追加（`pkg/tool/peer_tools.go`，197 的同一接口）

```go
type PeerDirectory interface {
    // … 197 的 List / Send …

    // WorkTreePeers lists other live agents whose directory overlaps root
    // (equal, ancestor or descendant), with their recent file-tool edits.
    WorkTreePeers(ctx context.Context, root string) []WorkTreePeer
    // NoteWrite records that the calling agent's file tool wrote absPath.
    NoteWrite(ctx context.Context, absPath string, at time.Time)
    // TouchesBetween returns other agents' file-tool writes under root in (from, to].
    TouchesBetween(ctx context.Context, root string, from, to time.Time) []PeerTouch
}

type WorkTreePeer struct {
    Name, Kind, Status, Task string
    RecentFiles []string // root-relative, newest first, at most 8
    MoreFiles   int
}

type PeerTouch struct {
    Path, AgentName string // Path root-relative
    At time.Time
}
```

### 3.2 身份台账（实现在 `pkg/process/peer_worktree.go`，新）

- 目录 `os.UserCacheDir()/forebrain/agent-touches`，覆盖变量 `FOREBRAIN_AGENT_TOUCHES_DIR`；测试二进制里未设覆盖 → 不写不读（同 194 `Enabled()` 的守卫思路）。
- 一行：`<unixNano>\t<strconv.Quote(resolvedPath)>\t<strconv.Quote(agentAddr)>\t<strconv.Quote(agentName)>\n`，`agentAddr`＝`<进程 uds 地址>#<代理 id>`；分片、保留 30 分钟、限频清扫、`O_APPEND` 0600、一切失败静默——逐条照 `tool_writes.go`。
- 写入点：`noteToolFileWrite` 的两个调用处旁边各加一行 `if p := rt.Peers; p != nil { p.NoteWrite(ctx, abs, now) }`（拿得到 ctx/rt 的那一层；拿不到就在调用链上一层加，**不改 186 的行格式**）。
- 读：排除本代理（`agentAddr` 相同）的写入；路径在 `root` 之下才算。

### 3.3 同树概况提醒（`pkg/run/config.go` 新包装器）

- `wrapPeerAwarenessReminderLLM(inner, peers func() tool.PeerDirectory)`，安装在 `runner.go:1122` 那行之后，注释说明与 LSP 提醒同理。
- 每次请求：`root`＝会话工作目录（`tool.ConversationSessionIDFromContext` 对应会话的 cwd；拿不到就跳过）；`list := WorkTreePeers(ctx, root)`；`key`＝`list` 的规范化指纹（名字+状态+任务+最近文件，排序后哈希）。
  - 本轮（run）第一次请求，或 `key` 与本 run 上次注入时不同 → 追加一条提醒；`len(list)==0` 且上次注入过非空 → 追加一条「其他代理都已离开」的提醒；否则不追加。
  - 「本 run 上次注入的 key」存在按 run id 键的小 map 里（run 结束清掉）；调用失败不更新。
- 文本（常量模板，逐字）：

```
<system-reminder>
Other agents are working in this directory tree right now:
- <name> (<kind>, <status>) — task: <task>; recently edited: <f1>, <f2> (+<N> more)
Coordinate through peer_send before editing files another agent is working on. Do not revert, stash, discard or commit changes you did not make; if a build or test fails in files you did not touch, ask the agent working on them instead of fixing it yourself.
</system-reminder>
```

  全部离开时：`<system-reminder>\nNo other agents are working in this directory tree any more.\n</system-reminder>`。没有任务或文件的部分整段省略（`— task: …`、`; recently edited: …`）。

### 3.4 写入提示（文件工具结果末尾）

- 写成功后：`TouchesBetween(ctx, root, now-30m, now)` 里有**别的代理**写过同一路径 → 结果末尾追加一行（常量模板）：
  `Note: <path> was also edited <N> min ago by <name> (task: <task>). Coordinate with peer_send before changing it further.`（多个代理取最近的一个；没有任务省略括号。）
- 只加在给模型的结果文本里，不改卡片数据。

### 3.5 危险 git 护栏

- `pkg/safety/git_sweep.go`（新）：`func GitSweepsWorkTree(command string) (rule string, ok bool)`，按 `dangerousCommandFromWords` 的方式拆 `sudo`/`env`/`sh -c`，对每个 git 调用（跳过 `-C <dir>`、`-c k=v` 等全局选项后取子命令）判断：

| rule | 命中条件 |
|---|---|
| `git_stash` | `stash` 且子动作不是 `list`/`show` |
| `git_checkout_paths` | `checkout` 且参数里有 `--` 或 `.` |
| `git_checkout_force` | `checkout` 且有 `-f`/`--force` |
| `git_restore` | `restore`（任何形式） |
| `git_reset_hard` | `reset` 且有 `--hard`/`--merge`/`--keep` |
| `git_clean` | `clean` 且有 `-f`/`--force`（含合并短选项如 `-fd`） |
| `git_commit_all` | `commit` 且有 `-a`/`--all`（含合并短选项如 `-am`） |
| `git_add_all` | `add` 且有 `-A`/`--all`/`.`/`-u`/`--update` |
| `git_switch_discard` | `switch` 且有 `--discard-changes`/`-f`/`--force` |

- shell 工具：在 `shellAssessmentRequiresApproval` 被调用之前，`rule, ok := safety.GitSweepsWorkTree(cmd)`；`ok` 且 `rt.Peers != nil` 且 `WorkTreePeers(ctx, cwd)` 非空 → 强制审批：`payload["force_tool_approval"]=true`、`payload["ask_even_in_yolo"]=true`、`payload["approval_reason"]="peer_work_tree"`、`payload["dangerous_rule"]=rule`、`payload["peer_names"]=[…]`、`payload["peer_files"]=[{name, files[]}…]`（199/200 用来在审批卡上写明会影响谁）。不论审批策略，都不返回工具错误。
- **泛化 YOLO 例外**（`pkg/run/runner.go`）：新增 `askEvenInYOLO := exitPlanMode || payloadBool(pay, "ask_even_in_yolo")`，把 `:516`（YOLO 放行）与 `:522`（never 策略拒绝）两处条件里的 `!exitPlanMode` 换成 `!askEvenInYOLO`；**其余引用 `exitPlanMode` 的分支（`:424` 已批准判断、`:439` 跑批、`:498` allow 规则、`:547` 流式推迟、`:550`/`:562`/`:579`/`:634`）一律不动**——它们是计划审批特有的语义。在 `payloadForcesToolApproval` 旁边加注释：`ask_even_in_yolo` 是「这个问题必须由人回答」的标记，YOLO 与 never 策略都不能替用户回答。
- allow 规则（`:498`）：`decision.Matched != nil` 时现有逻辑会放行强制审批——对 `ask_even_in_yolo` 的调用**也不放行**（用户事先写的 `git stash` allow 规则不知道同目录有别人）：条件改为 `(!forceToolApproval || (decision.Matched != nil && !askEvenInYOLO))`。
- 跑批会话：走 `:439` 的既有路径（不问、由任务沙箱约束），本计划不改。
- 子代理同样受约束（审批照现有路径归根会话）。

### 3.6 失败归因

- shell 命令 `exitCode != 0` 且 `rt.Peers != nil`：`TouchesBetween(ctx, cwd, 开始时间-2min, 结束时间)` 非空 → `resultPayload["changed_by_other_agents"] = [{"path":…, "agent":…, "minutes_ago":…}]`（最多 20 条）。**不进 `completionOutput`**。

## 4. 步骤

1. **分类器**：`pkg/safety/git_sweep.go` + `git_sweep_test.go`：上表每条至少一个命中、一个相近但不命中（`git stash list`、`git checkout main`、`git reset --soft HEAD~1`、`git clean -n`、`git commit -m x`、`git add file.go`、`git switch dev`），以及 `sudo git stash`、`sh -c 'git clean -fd'`、`env A=1 git restore .`、`git -C sub stash` 命中。
   验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run GitSweep -count=1` → ok。
2. **端口追加 + 台账实现**：改 `pkg/tool/peer_tools.go`；新建 `pkg/process/peer_worktree.go` + `peer_worktree_test.go`（覆盖变量指向临时目录）：两个代理身份各写几条 → `TouchesBetween` 排除自己（「自己」＝代理 id 相同且进程地址属于本进程用过的任何地址——195 切换登记模式会换地址，切换前写的行也算自己的；补一例：同一代理先后用两个地址各写一条，两条都被排除）、只含 root 之下；30 分钟外的不返回；坏行忽略；`WorkTreePeers` 只含同树对端（相等/祖先/后代，含符号链接两边 `EvalSymlinks`）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'Peer|Touch' -count=1` → ok。
3. **写入点与写入提示**：改 `pkg/tool/file_tools.go`、`pkg/tool/workspace.go`（只加调用，不改 186 行为）。用例：假 `PeerDirectory` 报告「同一路径 3 分钟前被 b 写过」→ 结果末尾出现提示行，逐字；没有别人写过 → 结果不变；186 的 `tool_writes_test.go` 原样通过。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → ok。
4. **git 护栏、YOLO 例外泛化与失败归因**：改 `pkg/tool/shell_tool.go`、`pkg/run/runner.go`。用例：
   - 有同树对端 + `git stash` + 审批策略 OnRequest → 审批 payload 含 `ask_even_in_yolo:true`、`approval_reason:"peer_work_tree"`、`dangerous_rule:"git_stash"`、`peer_names`、`peer_files`，且弹出审批。
   - 同上但 YOLO（`r.yoloEnabled()`）→ **仍弹审批**；审批策略 never → **仍弹审批**（不是 `permission denied: approval policy is never`）；存在匹配 `git stash` 的 allow 规则 → 仍弹审批。
   - 回归：`exit_plan_mode` 在 YOLO / never 下的现有用例全部原样通过；一个普通的 `force_tool_approval` 调用（不带新标记）在 never 下仍得到 `permission denied: approval policy is never`；跑批会话里的同一条 `git stash` 走跑批既有路径（不问）。
   - 无同树对端 → 走原路径。
   - `exit_code=1` 且台账有别人写入 → `resultPayload` 有 `changed_by_other_agents`，`completionOutput` 没有。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run -count=1` → ok。
5. **提醒包装器**：改 `pkg/run/config.go`、`pkg/run/runner.go` + 用例（参照 LSP 迟到提醒的现有测试）：首个请求注入、同一 run 内列表不变第二个请求不再注入、列表变化再注入、全部离开时注入离开提醒、调用失败不记为已注入；golden 两段文本；断言不含厂商名。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok；`./pkg/architecture` ok（`pkg/run`、`pkg/tool` 扇出不变）。
6. **手测**：两个 scratch home 的 TUI 在同一临时 git 仓库（`git init` 后有一个已提交文件），fake provider 驱动 A 编辑 `a.go`；B 的下一轮请求里能在 `FOREBRAIN_DEBUG` 日志或请求快照中看到同树提醒（若无请求快照手段，就在 B 里让模型调用 `peer_list` 观察）；B 执行 `git stash` → 出现审批，审批卡原因是 `peer_work_tree`。命令与输出贴进执行记录。
7. **收口**：`go vet ./...`；`bash scripts/package-graph.sh`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`。

## 5. 边界

- **只动**：`pkg/safety/git_sweep.go`（新）及测试、`pkg/tool/peer_tools.go`、`pkg/tool/file_tools.go`、`pkg/tool/workspace.go`、`pkg/tool/shell_tool.go` 及其测试、`pkg/run/config.go`、`pkg/run/runner.go` 及其测试、`pkg/process/peer_worktree.go`（新）及测试、`pkg/process/peers.go`（把台账实现挂进 `PeerDirectory`）。
- **不动**：186 台账的行格式与读写逻辑；`safety.AssessShellCommand` 与现有危险规则；审批引擎里除 §3.5 列出的三处条件（`:498`、`:516`、`:522`）以外的任何分支；卡片（审批卡上显示 `peer_work_tree` 原因的文案在 199/200）。
- shell 命令自己的写入不进身份台账（与 186 相同限制：无法从快照差里分出是谁写的）。

## 6. 完成判据（机器可查）

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...` 无输出。
2. `git diff 202ed4f2 -- pkg/tool/tool_writes.go` 为空（186 未被改动）。
3. `grep -rn 'changed_by_other_agents' pkg/tool/shell_tool.go` 只出现在 `resultPayload` 赋值处。
4. 手测记录贴进执行记录。
5. `plans/README.md` 本行状态更新。

## 7. 测试计划

见 §4。提醒包装器测试参照 `pkg/run` 里 `lspDiagnosticsReminderLLM` 的用例；shell 审批用例参照 `pkg/tool/shell_tool_test.go` 里 `dangerous_rule` 的现有用例。

## 8. 维护注意

- 提醒追加在请求末尾、`IsMeta`，不动缓存前缀；但每次变化都会多一条消息。若将来发现提醒过于频繁，调的是「指纹」的粒度（比如只看名字+任务），不是删掉功能。
- Claude Code 对端（只在 CC 模式下出现，owner 同日追加裁决：开关默认关闭）没有身份台账，只能列「在场」；提醒里它们没有「recently edited」。
- 开关不同的 forebrain 进程互相看不见（owner 裁决：打开后只用 Claude Code 的目录），所以同树概况与 git 护栏只覆盖同一登记模式的进程；身份台账是机器级目录、不分模式，写入提示与失败归因仍能看到另一模式的代理写过的文件——这是有意的，不要为了「一致」把台账也按模式过滤。
- 自闭环（owner 同日追加裁决）：护栏的数据只来自 forebrain 自己——身份台账目录、当前登记模式目录里的登记、本进程的工具调用；不得调用外部命令（git 护栏只分析命令字符串，不跑 `git status` 之类去探测），也不得依赖 Claude Code 是否在场。
- git 护栏的规则表是**有意偏保守**的（`add .`、`restore --staged` 都算）：误拦只是多一次审批，漏拦是冲掉别人的工作。

## 9. STOP 条件

- `noteToolFileWrite` 的两个调用处拿不到 `ctx`/`rt`，且往上一层加调用会改动 3 个以上文件。
- `pkg/run/runner.go` 里 `exitPlanMode` 参与的分支与 §2 摘录不符（行号漂移可以，条件内容变了不行），或泛化后任何 `exit_plan_mode` 现有用例变红。
- 流式派发路径（`:547`）下 `ask_even_in_yolo` 的调用不能当场问、也不能推迟到流结束——报告，不要给它另写一条推迟逻辑。
- 包装器需要在 `pkg/run` 新增 import。
- `pkg/tool` 生产文件数会超过 31。
