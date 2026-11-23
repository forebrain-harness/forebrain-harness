# 子代理 worktree 真隔离 —— 修 `peer_list` 把 worktree 里干活的子代理报成 "same tree"

> 本文件是 handoff 计划（执行者零上下文可读）。批准后实施时另复制一份到仓库惯例位置
> `docs/plan/PEER_SUBAGENT_WORKTREE_ISOLATION_PLAN.md`（`docs/plan/` 已 gitignore，不入库）。

## 0. 基线与漂移检查

- 基线 = 工作区状态，`git rev-parse HEAD` = `f15bc894`（`dev`）。
- 同树的并行 WIP（**不属于本计划范围**，禁止改动/回退/提交）：
  - `pkg/tui/plan_drawer*.go`、`pkg/tui/zz_repro_test.go`（另一个 terminal agent 在做 plan 抽屉分页）；
  - `.worktrees/283-execute`（另一个 terminal agent 在实施 `plans/283`，其分支独立，主树不含其改动）。
- 漂移检查（只比内容，不比行号）：动工前逐文件核对下表"现状摘录"列的关键符号是否仍是原文；
  内容不符 = STOP（说明有人改了同一处，先报告再动手）。

| 文件 | 现状摘录（必须仍存在） |
| :--- | :--- |
| `pkg/run/subagent.go` | `type SubagentInput struct`（含 `Title`/`Task`/`SubagentType`）；`type subagentDispatch struct`；`prepareSubagentExecutionResolved` 中 `entry := agent.HistoryEntry{`；`subagentExecContext(prep preparedSubagent)`；`subagentRecordContext(ctx, fac, record)`；`type LiveSubagent struct`；`subagentsFor` 的调用点 |
| `pkg/agent/subagent_history.go` | `type HistoryEntry struct`（JSONL 记录，`historyPath` = `<workspaceRoot>/state/subagent-history.jsonl`） |
| `pkg/tool/git_worktrees.go` | `MainWorktreeRoot`、`WorktreesDir`、`IsLinkedWorktreeTop`、`EnsureWorktreesIgnored`、`shellDiffRun(ctx, shellDiffDiscoveryEnv(), timeout, nil, "-C", dir, "worktree", ...)` |
| `pkg/tool/state.go` | `func WorkingDir(ctx)`；`func (s *State) GuardWrite(ctx, absPath)` 里 `TaskWorkspaceFromContext` 那段 |
| `pkg/tool/shell_tool.go` | `runSandboxedShellCommand` 里 `manager.UpdateConfig(cfg, shellToolSettingsSources(rt, cfg), snap, req.cwd, req.cwd, os.TempDir(), st.AllowedRoots())`；`sandboxedShellRequest.cwd` 来自 `wdAbs`（`WorkingDir`） |
| `pkg/safety/sandbox_config.go` | `ConvertToRuntimeConfig(...)`：`AllowRead` 含根 `/`，`AllowWrite` = cwd + tempDir + `/tmp` + `additionalDirs` |
| `pkg/process/peers.go` | `func (svc *peerService) subagentsFor(ctx, sid, primaryName, root)`，其中 `Root: root`；`branchFor(root)` |
| `pkg/process/peer_outbound.go` | `candidate.sameTree = sameWorkTree(candidate.cwd, v.launchDir)`；`func (o *peerOutbound) NoteWrite(...)`；`guardIdentity(ctx)` |
| `pkg/process/peer_worktree.go` | `agentTouchRetention`、`appendAgentTouch`、`type workTree`/`contains`、`peersUnder` |

**不要碰**：`pkg/tui/**`（另一个 agent 的 WIP）、`.worktrees/283-execute/**`（另一个 agent 的 WIP）、
`plans/283-*.md`、`pkg/turn/approval.go`。`pkg/state` 的库表结构**不需要**任何迁移（见 §4.1）。

## 1. 为什么修

owner 报障（附两张截图）：`list agents`（`peer_list` 卡片）里，正在 `.worktrees/283-execute`
里执行 plans/283 的子代理 `forebrain-harness-ec.283` 被写成 `· same tree`，而它的每一个编辑都落在
`.worktrees/283-execute/pkg/...`，与主代理并不在一棵树里。

`same tree` 不只影响一行字：同一个布尔值同时决定

1. `peer_list` 卡片 / 网页 peer 面板的分组（"这个目录" vs "其他"）；
2. 文件工具的同树守卫、失败归因、写冲突提示（`WorkTreePeers`/`TouchesBetween`）；
3. 网关按"发送方工作树"路由消息。

所以它错了，三处一起错。

## 2. 根因（证据链，全部本轮现场取证）

### 2.1 现场证据

- 活体登记记录 `~/Library/Caches/forebrain/peers/59338.json`（执行 plans/283 的那个进程）：
  ```json
  {"id":"cli-645c7032-…", "name":"forebrain-harness-ec", "kind":"primary",
   "root":"/Users/doudou/workspace/unionj-cloud/forebrain-harness", …}
  {"id":"cli-645c7032-…/subagent-ac1501dc-fec5-4d94-8775-ef604741051a",
   "name":"forebrain-harness-ec.283", "kind":"subagent",
   "root":"/Users/doudou/workspace/unionj-cloud/forebrain-harness", …}   ← 写成主树
  ```
- 写入台账 `~/Library/Caches/forebrain/agent-touches/29905298.log`：
  ```
  "/Users/…/.worktrees/283-execute/pkg/run/subagent.go"          ← agentID …/subagent-ac1501dc…, name forebrain-harness-ec.283
  "/Users/…/.worktrees/283-execute/pkg/architecture/cache_test.go" ← 同上
  ```
  → 子代理真实工作树 = `.worktrees/283-execute`，record 却写主树。

### 2.2 因果链（file:line）

1. `pkg/run/subagent.go:2585` `type LiveSubagent struct { Key, Title, StartedAt, WorkerSessionID }`
   —— 子代理的"工作树"在运行时里根本没有位置。
2. `pkg/process/peers.go:1001` `subagentsFor(...)` → `:1024` `Root: root`
   （`root` = 会话根；primary 与所有子代理共用一个值），primary 才有 `Branch`（`:910`）。
3. 于是扩展块 `peer.ExtAgent.Root`（`pkg/peer/record.go:84`）把子代理记成会话根。
4. `pkg/process/peer_outbound.go:265` `candidate.sameTree = sameWorkTree(candidate.cwd, v.launchDir)`
   —— cwd = 主树 ⇒ `true`。
5. 展示三面全吃它：`pkg/tool/format.go:4227`（`peer_list` 卡片的 `same_tree`）、
   `pkg/tui/commands.go:2690`、`pkg/tui/render.go:3507`、网页 `pkg/gateway/api_extra.go:2741`。
6. 守卫也吃它：`pkg/process/peer_worktree.go:518/555/577`（`WorkTreePeers`/同树提醒）。
   讽刺的是子代理写进 linked worktree 的行会被 `workTree.contains` 排除，于是主代理能看到
   "同树里有这个子代理"，却永远列不出它写的文件。
7. 消息路由：`pkg/process/peer_inbound.go:453 senderWorkTree` 优先取发送代理的 `root`。

### 2.3 已有的正确语义（本计划必须归一到这里，不新造判定）

- 设计文档 `docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md:41`：工作树 = 代理的工作目录；
  同树 = 路径包含；**唯一例外** = 中间隔着一个 linked git worktree 顶（owner 2026-11-09），
  submodule 仍算同树。实现 = `pkg/process/peer_worktree.go:290 workTree.contains` +
  `pkg/tool/git_worktrees.go:88 IsLinkedWorktreeTop`，`sameWorkTree`（`peer_inbound.go:1199`）
  已含该例外（测试 `peer_worktree_test.go:513 TestSameWorkTreeStopsAtALinkedWorktree`）。
- "代理自己的工作树"写进扩展块的字段就是 `root`（`peer.ExtAgent.Root`）。**本计划只是把它填对**，
  不新增同树判据。
- `git worktree` 作业位置唯一：`<主 worktree>/.worktrees/<name>`
  （`pkg/tool/git_worktrees.go:26 WorktreesDirName` + `:41 MainWorktreeRoot` + `:124 EnsureWorktreesIgnored`）。

### 2.4 根因一句话

plans/283 的交接合同让**执行子代理自己在 `.worktrees/<name>` 里干活并提交**（`plans/283` 第 192 行：
`git worktree add -b <branch> <main worktree>/.worktrees/<name> HEAD`，"
Every read, edit, command and commit happens inside that directory and nowhere else"），
但运行时从来没有"子代理在哪棵树里跑"这个概念 —— 它的工具根是会话根，登记记录也只能写会话根。
**根因：派发层没有工作树这个合同，记录层只能谎报。**

## 3. owner 裁决（2026-11-10，本轮 user_interaction 四项）

1. **参数形态**：具名 `worktree: "<kebab-name>"` —— 引擎在 `<主 worktree>/.worktrees/<name>`
   从 HEAD 新建工作树 + 分支 `<name>` 并检出，子代理就在里面跑；父代理的简报可以点名这个目录。
2. **写入边界**：硬边界 —— worktree 子代理的文件工具在 worktree 外一律**拒写**（不是审批）。
3. **今日流程**：加"写入证据兜底" —— 派发没声明 worktree 时，登记记录里的树取该子代理
   **最近一次写入所在的 linked worktree**（同树守卫已在用的那份台账），使 283 这类
   "自己 `git worktree add`" 的子代理立刻也不再谎报。
4. **shell 边界**：跟着收进 worktree —— worktree 子代理的 shell 命令写沙箱根收窄到 worktree
   （+ 系统临时目录），沙箱可用时生效。

## 4. 修复设计

### 4.1 `worktree` 参数：引擎建树（`pkg/tool/git_worktrees.go`）

新增（与既有 worktree 词汇同文件，复用 `shellDiffRun` / `MainWorktreeRoot` / `EnsureWorktreesIgnored`）：

```go
// worktreeCreateTimeout bounds `git worktree add`: a cold checkout of a large
// repository takes seconds, and a dispatch must never hang the turn on git.
const worktreeCreateTimeout = 60 * time.Second

// CreateWorktree makes a worktree of the repository projectRoot belongs to,
// at <main worktree>/.worktrees/<name>, checked out from HEAD on a new branch
// <name> (owner 2026-11-10). It answers the resolved directory — the path a
// subagent's record carries and a reviewer runs git with — and "" outside a
// repository, when the name is already taken and when git refuses.
func CreateWorktree(ctx context.Context, projectRoot, name string) (dir string, err error)
```

- 名字校验（在 `pkg/run` 的入参处做，见 §4.2；`CreateWorktree` 只再断言一次）：
  `^[a-z0-9][a-z0-9-]{0,63}$`，拒绝 `/`、`.`、`..`、大写、空格。
- 步骤：`MainWorktreeRoot(ctx, projectRoot)`（"" ⇒ error "worktree dispatch needs a git repository"）
  → `EnsureWorktreesIgnored(ctx, mainRoot)` → `os.MkdirAll(<mainRoot>/.worktrees, 0o755)`
  目录（**不要先建 `<name>` 子目录**：`git worktree add` 自己建）→
  `shellDiffRun(ctx, shellDiffDiscoveryEnv(), worktreeCreateTimeout, nil,
   "-C", mainRoot, "worktree", "add", "-b", name, dir, "HEAD")` →
  失败时把 git 原文塞进 error → 成功后 `filepath.EvalSymlinks(dir)` 并断言
  `IsLinkedWorktreeTop(dir)`（否则 error：它不是 linked worktree 顶，同树判定会跟着错）。
- **不删除、不复用**：名字已存在（目录或分支）⇒ 报错，交给模型换名字（owner 的 worktree 一律不删）。
- 不做 DB 迁移：子代理记录是 JSONL（`pkg/agent/subagent_history.go` `historyPath` =
  `<workspace>/state/subagent-history.jsonl`），加字段即兼容旧记录；worker session 的
  `Cwd` 不动（避免把子代理的记忆/项目归档挪到 worktree 名下，见 §5 out-of-scope）。

### 4.2 执行根与写边界：新 context 值 `IsolatedWorktree`（`pkg/tool`）

**不能复用 `TaskWorkspace`**：`pkg/run/runner.go:474` 只要
`TaskWorkspaceFromContext(ctx) != ""` 就按"跑批无人值守"处理 —— **一次审批都不再问**
（包括同树危险 git 的强制审批）。worktree 子代理必须照常会问审批，所以另立一个值：

```go
// pkg/tool/state.go
// WithIsolatedWorktree records the git worktree this run was dispatched into
// (owner 2026-11-10): its file tools resolve paths there and may write nowhere
// else, and the shell sandbox keeps its writes there too. It is NOT a task
// workspace: an isolated run still asks for approvals.
func WithIsolatedWorktree(ctx context.Context, dir string) context.Context
func IsolatedWorktreeFromContext(ctx context.Context) string
```

- `WorkingDir(ctx)`（`state.go:684`）**最优先**读它（在 `TaskWorkspaceFromContext` 之前），
  于是 `ResolvePath`、file tools、`@` 提及、shell `req.cwd`、shell-diff 全部落在 worktree。
- `GuardWrite`（`state.go` `func (s *State) GuardWrite`）在 `TaskWorkspace` 判断**之前**加一条：
  ```go
  if root := IsolatedWorktreeFromContext(ctx); root != "" && !pathWithinWorkspace(absPath, root) {
      return fmt.Errorf("worktree subagent: writes are confined to its worktree %s", root)
  }
  ```
  （`pathWithinWorkspace` 已按符号链接解析；跑批的旧文案一字不改，跑批行为不变。）
- **不**改 `GuardWrite` 的 plan-mode / review-scope 分支顺序，避免改变既有语义。

### 4.3 shell 写沙箱收窄（`pkg/tool/shell_tool.go` + `pkg/safety`）

- `sandboxedShellRequest.cwd` 已来自 `wdAbs = WorkingDir(ctx)`，收窄后它就是 worktree
  （`ConvertToRuntimeConfig` 把 cwd 放进 `AllowWrite`）。
- 唯一改动点：`runSandboxedShellCommand` 里
  `manager.UpdateConfig(..., st.AllowedRoots())` 的最后一个参数（`additionalDirs`，决定
  `AllowWrite` 还能写哪些额外根）。抽一个小函数并在有隔离 worktree 时返回 `nil`：
  ```go
  // sandboxAdditionalDirs is what the sandbox may write besides its cwd, temp
  // and /tmp. An isolated worktree run adds nothing: its cwd is the worktree and
  // a shell command must not reach the conversation's tree (owner 2026-11-10).
  func sandboxAdditionalDirs(ctx context.Context, st *State) []string
  ```
  读根不动（`AllowRead` 仍含 `/`）：构建、工具链、语言运行时都还在 /tmp 之外读。
- **已知限制（写进报告）**：沙箱被会话自己抬掉时不拦 —— `danger-full-access`/YOLO 或
  `require_escalated`（用户亲手批的提权）下命令跑在宿主上，写越界只能靠 §4.2 的文件工具硬拒
  与主代理评审。不为此改 YOLO 的语义（用户说"别拦我"）。

### 4.4 记录层：把树填对（`pkg/run` → `pkg/process`）

1. `pkg/agent/subagent_history.go` `HistoryEntry` 加：
   ```go
   // Worktree is the git worktree this subagent was dispatched into; empty for
   // a subagent that works where its conversation does. Continuations run
   // there again, and the peer record reports it as the agent's own tree.
   Worktree string `json:"worktree,omitempty"`
   ```
2. `pkg/run/subagent.go`
   - `SubagentInput` / `SubagentSendInput` / fanout 的 `SubagentTask` 加
     `Worktree string \`json:"worktree,omitempty"\``，说明是**固定文字**（不随配置变化）：
     > `Optional git worktree to run this subagent in: a short kebab-case name. The worktree is
     > created at <main worktree>/.worktrees/<name> on a new branch <name> from HEAD, and every
     > file and shell tool of the subagent works inside it. Omit to run in the conversation's
     > directory.`
   - `subagentDispatch` 加 `worktree string`；`execGeneralSubagent` 收参即传；
     fanout 每任务一份。
   - `prepareSubagentExecutionResolved`：**在创建 child run 记录之前**（`CreateSubagentRun` 之上）
     做三件事：(a) 名字校验；(b) 跑批上下文（`tool.TaskWorkspaceFromContext(baseCtx) != ""`）
     ⇒ 明确拒绝 `"a worktree dispatch needs an interactive session"`（跑批的无人值守审批语义
     与隔离语义不能混）；(c) `tool.CreateWorktree(ctx, fac.projectRoot(), name)`。
     拿到路径后 `entry.Worktree = dir`（`:1796` 的记录构造处），并在
     `cctx = tool.WithIsolatedWorktree(cctx, dir)`。
   - `subagentExecContext(prep)`：`if prep.entry.Worktree != "" { hostCtx = tool.WithIsolatedWorktree(hostCtx, dir) }`
     —— 这是每个派发路径都走的**唯一**子上下文构建点（fork/typed/custom 都在此）。
   - `subagentRecordContext(ctx, fac, record)`（续跑）：同样按 `record.Worktree` 设置；
     先 `os.Stat` 校验目录仍在，缺了 ⇒ `fmt.Errorf("subagent %s ran in worktree %s, which no longer exists", …)`
     （删掉的 worktree 不偷偷跑回会话根）。
   - `subagentChannel` 增加 `worktree string`（`subagentChannelFor` 从 record 填，写在
     `subagentChannelsMu` 下，和 `title` 同一约定）；`LiveSubagent` 增加 `Worktree string`，
     `LiveSubagents` 一并返回。
   - `subagentExecResult` 加 `Worktree string`；`subagent_run`/`subagent_send` 的回执 JSON 与非空
     时加 `"worktree": dir`，fanout `FanoutResult` 同样加字段（父代理据此评审、写实施记录）。
3. `pkg/process/peers.go` `subagentsFor`：
   ```go
   tree := strings.TrimSpace(sub.Worktree)
   if tree == "" { tree = root }
   agents = append(agents, peer.ExtAgent{ ..., Root: tree, Branch: svc.branchFor(tree), ... })
   ```
   （`branchFor` 已有 TTL 缓存，`readGitBranch` 认 worktree 的 `.git` 文件 ⇒ 得到 `<name>` 分支。）
4. **写入证据兜底**（owner 裁决 3；覆盖"子代理自己建 worktree"的今日流程）：
   - `peerService` 加一个内存表：`agentID → (tree string, at time.Time)`，窗口 =
     `agentTouchRetention`（30 min，`peer_worktree.go:40`）。
   - `peerOutbound.NoteWrite`（`peer_outbound.go:378`）在写完台账后
     `svc.noteAgentTree(agentID, tool.LinkedWorktreeTop(absPath), at)`（后者是新增的小函数：
     从路径向上找到最内层 `IsLinkedWorktreeTop` 的目录，找不到返回 ""）。
     **不进每秒热路径**：`refreshRecordLocked` 每秒重写 record，而这张表只在文件工具写文件时更新一次。
   - `subagentsFor` 的取值顺序：`sub.Worktree`（派发声明）> `svc.agentTree(sid+"/"+sub.Key)`（观测）> `root`（会话根）。
   - 表按窗口惰性清理（**同一把 `svc.mu`**：`statusAndAgentsLocked` 已在锁内，读表不加锁；
     `NoteWrite` 从工具调用处进来，取锁只做一次 map 写，条目数 = 本进程的代理数，天然有界）。

### 4.5 交接提示词（在 `283-execute` 分支，不在主树）

plans/283 的 `pkg/tool/session_tools.go`（283 分支当前版本约 :635）现在写的是：
> `1. Where to work. Pick a short kebab-case name after the plan and give the exact command:
> "git worktree add -b <branch> <main worktree>/.worktrees/<name> HEAD", where the main worktree
> is the first path "git worktree list" prints. Every read, edit, command and commit happens inside
> that directory and nowhere else.`

改成（逐字替换该编号段落）：
> `1. Where to work. Pick a short kebab-case name after the plan (the plan file name is the natural
> source) and pass it as subagent_run's "worktree" argument. The dispatch creates
> <main worktree>/.worktrees/<name> on a new branch <name> from HEAD and runs the executor inside
> it: every read, edit, command and commit happens there and nowhere else. Your brief tells the
> executor it is already in that worktree; never ask it to create one.`

同步改 283 计划里同一段文字（`plans/283-…md` 第 192 行附近）与 283 自己的 golden 测试
（283 分支 `pkg/tool/session_tools_test.go:1004`）。

**时序**：该文件目前只存在于 `.worktrees/283-execute`（另一 agent 的 WIP，本计划不许改）。
实施时先查主树：`grep -n 'git worktree add -b' pkg/tool/session_tools.go`
- 找不到 ⇒ 283 尚未合入：本计划其余步骤照做，本步记入"待 283 合入后执行"，并在报告里点明
  **283 那条流程要等这一步做完才走新合同**（兜底 §4.4.4 已让它今天就正确显示）。
- 找到 ⇒ 283 已合入，直接按上面逐字替换 + 改 golden。

### 4.6 行为变化矩阵

| # | 场景 | 旧 | 新 |
| :-- | :--- | :--- | :--- |
| 1 | `subagent_run{worktree:"283-execute"}` | 参数不存在；子代理在会话根跑 | 引擎建 `.worktrees/283-execute`（分支 `283-execute`，从 HEAD）；文件/shell 根都在那；回执带路径；`peer_list` 报该路径，**不是 same tree** |
| 2 | 老流程（含 283 交接简报现状）：子代理自己 `git worktree add` | same tree | 记录取"它最近一次写入所在的 linked worktree" ⇒ 也不再 same tree |
| 3 | 子代理没声明 worktree 且没写过文件 | same tree | same tree（没有任何证据说它在别处） |
| 4 | worktree 子代理写 worktree 之外 | 允许（同普通子代理） | 文件工具硬拒（"writes are confined to its worktree"）；shell 在沙箱可用时写根只剩 worktree+临时目录；沙箱被会话抬掉时不拦（已知限制） |
| 5 | `subagent_continue` / 自动续跑 | 会话根 | 仍在记录的 worktree；目录不存在 ⇒ 明确错误，不偷偷回落会话根 |
| 6 | 跑批上下文里派 worktree 子代理 | — | 明确拒绝（跑批的"不问审批"语义不能和隔离混用） |
| 7 | 主代理的同树守卫/写冲突提示 | 把执行子代理算同树（却永远列不出它的文件） | 不再算同树；worktree 内部的同树判定只看那棵树 |
| 8 | 网页 peer 面板分组 | "这个目录" | "其他"，并显示 worktree 路径 |
| 9 | 网关按工作树路由（子代理作为发送方） | 用会话根 | 用它自己的 worktree；找不到同树会话 ⇒ `refused`（这是设计的诚实结果） |

**刻意不变**：primary 的 `root`/`branch`；跑批的 `TaskWorkspace` 语义与文案；plan-review 子代理
自己的 `ReviewWriteScope`（它自建 worktree 的流程不改）；模型/审批开关（plans/283 的 `model` 不受影响）。

## 5. Scope

**In scope（只动这些）**
- `pkg/tool/git_worktrees.go`（`CreateWorktree`、`LinkedWorktreeTop`、超时常量）
- `pkg/tool/state.go`（`WithIsolatedWorktree`/`IsolatedWorktreeFromContext`；`WorkingDir`；`GuardWrite` 一条）
- `pkg/tool/shell_tool.go`（`sandboxAdditionalDirs` 一处接线）
- `pkg/agent/subagent_history.go`（`HistoryEntry.Worktree`）
- `pkg/run/subagent.go`（入参、建树、exec/record 上下文、channel/LiveSubagent、回执、fanout 字段）
- `pkg/process/peers.go`（`subagentsFor` 的树与 branch）
- `pkg/process/peer_outbound.go` + `pkg/process/peer_worktree.go`（写入证据表）
- 上述包的测试文件（新用例）
- （283 已合入时）`pkg/tool/session_tools.go` 与 283 的 golden 测试

**Out of scope（明确不碰，一行理由）**
- `pkg/tui/**`：另一个 agent 的 WIP；且本修复不需要改展示层（`same_tree` 是数据）。
- `.worktrees/283-execute/**`、`plans/283-*.md`：另 agent WIP；只在 283 合入后做 §4.5。
- worker session 的 `Cwd`（`SessionBirth`）：会牵动记忆/项目归档的归档路径，非本 bug 所需。
- `pkg/state` 库表/迁移：子代理记录是 JSONL，无需加列。
- `pkg/safety` 的沙箱策略本体、`danger-full-access`/YOLO 的语义：只改一个 `additionalDirs` 输入。
- plan-review 的自建 worktree 流程、`pkg/gateway`、`frontend/`：数据层修好后三面自动一致。
- 未声明 worktree 的普通子代理的运行目录：不变。

## 6. 实施步骤（每步都带验证与预期）

**S1 `pkg/tool/git_worktrees.go`：建树与找树**
加 `worktreeCreateTimeout`、`CreateWorktree`（§4.1）、`LinkedWorktreeTop(path)`（向上找最内层
linked worktree 顶，找不到 `""`）。不改既有函数。
验证：
```bash
CGO_ENABLED=1 go build ./pkg/tool
gofmt -l pkg/tool/git_worktrees.go        # 无输出
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestCreateWorktree|TestLinkedWorktreeTop' -count=1 -v
```
预期：新增用例全 PASS（用例见 §7）。

**S2 `pkg/tool/state.go`：隔离上下文值**
`WithIsolatedWorktree`/`IsolatedWorktreeFromContext`；`WorkingDir` 最优先读它；`GuardWrite` 加硬拒一条。
验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestIsolatedWorktree|TestWorkingDir|TestGuardWrite' -count=1
```
预期：新用例如 PASS；`TestGuardWriteRuntimeConfinesWritesToTheTaskWorkspace`（既有跑批用例）仍 PASS（文案未改）。

**S3 `pkg/tool/shell_tool.go`：沙箱写根收窄**
抽 `sandboxAdditionalDirs(ctx, st)` 并接到 `UpdateConfig` 的 `additionalDirs` 位。
验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestSandbox|TestShell' -count=1`；
新增纯函数用例断言：普通 ctx ⇒ `st.AllowedRoots()`；带隔离 worktree ⇒ `nil`。

**S4 `pkg/agent/subagent_history.go`：`HistoryEntry.Worktree`**
验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/agent -count=1`（旧 JSONL 行反序列化不受影响）。

**S5 `pkg/run/subagent.go`：参数 → 建树 → 执行根 → 记录/回执**
按 §4.2/§4.4 全部接线（入参、`subagentDispatch.worktree`、`prepareSubagentExecutionResolved`、
`subagentExecContext`、`subagentRecordContext`、channel、`LiveSubagent`、`subagentExecResult`、
fanout 字段）。
验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestDispatchSubagent|TestSubagent|TestLiveSubagents|TestFanout' -count=1
```
预期：新增用例 PASS；既有派发/fanout/续跑用例 PASS（未声明 worktree 的路径行为不变）。
另外必须验一条**审批重放**：隔离 run 里撞到审批（`runAcrossApprovals` 用 surface 返回的 resume ctx
重试），重试后 `WorkingDir` 仍是 worktree。做法：复用既有子代理审批用例的钩子，在其中断言
`tool.IsolatedWorktreeFromContext(resumeCtx)` 非空（resume ctx 由 `approvalCtx` 派生，而
`approvalCtx` 就是隔离后的 exec ctx）。若这条不成立，先查钩子的重放路径，别靠"再包一层值"掩盖。

**S6 `pkg/process`：记录与兜底**
`subagentsFor` 用 `sub.Worktree`；`peerService` 写入证据表 + `noteAgentTree`/`agentTree`；
`peerOutbound.NoteWrite` 记账；`tool.LinkedWorktreeTop` 复用。
验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestPeer|TestSubagent|TestSameWorkTree' -count=1
```
预期：新增用例 PASS；`TestSameWorkTreeStopsAtALinkedWorktree`、`peer_outbound_test.go` 既有用例 PASS。

**S7 回归 + 静态检查**
```bash
gofmt -l cmd pkg third_party                       # 必须无输出
CGO_ENABLED=1 go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/process ./pkg/agent ./pkg/tui -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1
```
预期全绿。`pkg/tui` 若有失败先查是不是别的 agent 的 WIP 引起（是 ⇒ 不修，报告）。

**S8（283 已合入才做）`pkg/tool/session_tools.go` 交接简报逐字替换**（见 §4.5）+ golden；
不在 ⇒ 记"待办"，写进报告与计划回填章节。

**S9 真机验收（tmux + fake provider；禁真模型）**
构建：`CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`
（漏 `-tags fts5` 的失败模式：`no such module: fts5` —— 是工具链错，别去"修"）。
隔离环境：临时 `FOREBRAIN_HOME` + 临时 git 仓库（`git init` + 一个 initial commit），
fake provider 按 `.claude/skills/run-forebrain/SKILL.md` 的配方驱动。三个场景：
1. **参数建树**：让主代理发 `subagent_run{worktree:"demo-exec"}`，子代理写一个文件。
   预期：`.worktrees/demo-exec` 存在且 `git -C .worktrees/demo-exec status --porcelain` 有该文件；
   卡片显示的编辑路径在 `.worktrees/demo-exec/` 下；`state/subagent-history.jsonl` 含 `"worktree"`。
2. **peer 记录/列表**：同一仓库开第二个 TUI 会话，让它调 `peer_list`。
   预期：`Listed agents` 卡片里该子代理一行是 `.worktrees/demo-exec` 路径，**不再出现 "same tree"**；
   `~/Library/Caches/forebrain/peers/<executor pid>.json` 里该 subagent 的 `root` = 那个 worktree，
   `branch` = `demo-exec`（用隔离 home 时改读 `FOREBRAIN_PEERS_DIR` 指向的目录）。
3. **兜底**：子代理不传 `worktree`、自己在 shell 里 `git worktree add -b manual .worktrees/manual HEAD`
   再写文件；另一会话 `peer_list` 预期也不再 same tree（显示 `.worktrees/manual`）。
结束：`tmux kill-session`、删临时目录、恢复 `FOREBRAIN_*`。

## 7. 新增测试（跟随既有模式）

- `pkg/tool/git_worktrees_test.go`（跟随 `nestedWorktreeRepo` 的 git 夹具）：
  - `TestCreateWorktreeChecksOutANewBranchUnderWorktrees`：dir 存在、`IsLinkedWorktreeTop`、分支 = name、
    `MainWorktreeRoot(dir)` = 主树；
  - `TestCreateWorktreeRefusesATakenName`（目录已存在 / 分支已存在 ⇒ 错误含 git 文案）；
  - `TestCreateWorktreeNeedsARepository`；
  - `TestLinkedWorktreeTopFindsTheInnermostWorktree`（main 树里的普通路径 ⇒ ""）。
- `pkg/tool/state_test.go`（或既有 `file_tools_test.go` 同区）：
  - `TestWorkingDirPrefersTheIsolatedWorktree`；
  - `TestGuardWriteRefusesOutsideTheIsolatedWorktree`（worktree 内放行、主树拒写、错误文案含 worktree 路径）。
- `pkg/tool/shell_tool_test.go`：`TestSandboxAdditionalDirsDropsRootsForAnIsolatedWorktree`。
- `pkg/run/subagent_test.go`（跟随 `newDispatchFactory` + `dispatchTestCtx`）：
  - `TestDispatchSubagentCreatesAndRunsInItsWorktree`：fac 的 projectRoot 换成 `git init` 过的临时仓库；
    断言 `prep.entry.Worktree` = 解析后的 dir、`tool.WorkingDir(subagentExecContext(prep))` = 该 dir、
    `agent.ListHistory` 的记录含 worktree；
  - `TestDispatchSubagentRefusesAWorktreeInABatchContext`；
  - `TestSubagentContinuationRunsInItsRecordedWorktree`（`subagentRecordContext` → `WorkingDir`），
    以及目录被删时的错误；
  - `TestLiveSubagentsReportsItsWorktree`。
- `pkg/process/peers_test.go` / `peer_outbound_test.go`（跟随 `peerTestEnvFor`）：
  - `TestSubagentsForRecordsTheWorktreeAsItsRoot`（活体子代理带 worktree ⇒ 扩展块 `root` = 那棵树、`branch` = 那分支）；
  - `TestPeerCandidateInAWorktreeIsNotSameTree`（手写记录：root = 仓库内 linked worktree ⇒ `SameTree=false`、
    列表给路径；对照：submodule 仍 same tree，沿用 `TestSameWorkTreeStopsAtALinkedWorktree`）；
  - `TestObservedWorktreeBecomesTheAgentsTree`（`noteAgentTree`/`agentTree` 窗口与覆盖顺序：声明 > 观测 > 会话根）。

## 8. Done criteria（机器可查）

- `git status --porcelain` 的改动文件 ⊆ §5 In scope（`pkg/tui/*`、`.worktrees/**` 零改动）。
- `gofmt -l cmd pkg third_party` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` 无输出。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/process ./pkg/agent -count=1` 全 PASS。
- 真机三个场景的 `tmux capture-pane` 截图/文本留证，且场景 2 的卡片**不含**该子代理行的 "same tree"。
- `~/Library/Caches/forebrain/peers/<pid>.json`（或隔离 `FOREBRAIN_PEERS_DIR`）里 subagent 的
  `root` 是 `.worktrees/<name>`，`branch` 是 `<name>`。
- 未 commit（改动留工作区给 owner 审）。

## 9. STOP conditions

- 漂移：§0 表的任一符号在动工前已变样（尤其 `prepareSubagentExecutionResolved` /
  `subagentExecContext` / `subagentRecordContext` / `subagentsFor` 被别的会话改了）⇒ 停，报告差异。
- 跑批语义被波及：任何跑批用例变红，或需要改 `runner.go:474` 的条件 ⇒ 停（说明隔离值与
  task workspace 混用了）。
- YOLO/完全访问下真机无法验收 §4.3 的收窄 ⇒ 不是 bug，按"已知限制"记录，不要为验收改沙箱语义。
- `git worktree add` 在目标平台不可用/超时（60s）⇒ 停，报告 git 原文；不要改成让子代理自己建树。
- 283 的交接简报文件在主树与 §4.5 的逐字文本不一致（例如 283 用了别的写法）⇒ 停，先报告再改。
- 需要动 `pkg/state` 表结构、`pkg/safety` 策略本体、或 `pkg/tui` 才能完成 ⇒ 停（超出 Scope）。
- 两次"改-验"循环仍不收敛 ⇒ 按 Scope 重新评估，不叠补丁。

## 10. 维护说明（给 reviewer 与后来者）

- 概念清单：`IsolatedWorktree`（本 run 的工作目录与写边界，仍有审批）≠ `TaskWorkspace`
  （跑批的无人值守工作目录，**不问审批**）≠ `BatchBoundary`（跑批的读禁区）≠ `ReviewWriteScope`
  （评审的白名单写域）。四个值只有一个生产者各一处，别互相替代。
- 审阅重点：(1) `runner.go:474` 不受影响；(2) `WorkingDir` 的优先级顺序；
  (3) `subagentsFor` 的取值顺序与窗口；(4) `NoteWrite` 的新记账是否在热路径上（它在文件写时跑一次，
  而 record 每秒重写只读内存表）；(5) 一条 worktree 子代理的 `peer_list` 行在 TUI 卡片、网页面板、
  `peer_send` 路由三处都读同一个 `root`。
- 后续（不在本计划）：`subagent_send`/`fanout` 的 worktree 名字冲突策略（现在一律报错）；
  worker session 的 `Cwd` 是否跟随 worktree；YOLO 下的 shell 隔离；plans/283 合入后把交接简报
  切到 `worktree` 参数（§4.5/§S8）。

### 落库

- 2026-11-11：owner 指令 "commit everything in dev"，本批 13 个文件以 dev `c31191ea`
  "feat(run): report a subagent's own worktree in peer records" 落库。提交前已验证
  （vet 干净、五个包全 PASS、真机三场景过）；提交后核对 stat 与最终工作区逐字一致。
