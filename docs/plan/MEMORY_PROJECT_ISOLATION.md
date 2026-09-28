# 记忆系统 Project 隔离方案

> 目标：消除跨项目记忆污染 —— Java 技术栈的会话不应该在 prompt 前缀里拿到 Golang 项目的
> `memory_summary.md`，也不应该 grep 到别的项目的 `MEMORY.md` 块。
>
> 约束（本仓库的硬规则）：
> 1. 任何改动都不得降低输入 token 的缓存命中率；注入在会话之前的内容必须在一次会话内逐字节稳定。
> 2. 只修根因，不打补丁；不接受"让症状看不见"的防御式代码。

---

## 1. 现状与根因

### 1.1 现在的分区边界只有一层：primary agent（租户）

```
<workspaceRoot>/memories/            # = memories.ResolveRootForAgent(workspaceRoot).MemoryRoot
  MEMORY.md                          # 全部项目混在一起的手册
  memory_summary.md                  # 全部项目混在一起的索引（每次会话注入）
  raw_memories.md                    # 全部项目的 stage-1 原料合并
  rollout_summaries/*.md             # 全部项目的会话摘要
  skills/<name>/                     # consolidation 产出的 skill 提案
  extensions/ad_hoc/notes/*.md       # 用户当场口述的规则
  phase2_workspace_diff.md
  .git                               # phase-2 diff 基线
<workspaceRoot>/state/
  memory-rollouts/*.jsonl            # 证据快照（按 thread）
  memory-consolidated-at             # 单一 consolidation 水位标记
```

数据库侧同样只有租户维度：`fb_memory_stage1_outputs.agent_id`、
`fb_memory_jobs(kind, job_key=agentID)`。

### 1.2 根因：project 只是"散文"，不是数据

`cwd` 确实进入了系统，但**只以自然语言的形式存在于模型自己写出来的文件里**：

- `templates/consolidation.md` 要求模型写 `applies_to: cwd=<...>`；
- 要求 `memory_summary.md` 用 `### <cwd / project scope>` 分节。

也就是说，项目边界完全依赖 consolidation 模型的自觉，而**所有真正的执行点都没有项目维度**：

| 执行点 | 代码位置 | 现在的作用域 |
| --- | --- | --- |
| 每轮注入的摘要 | `instruction.go:renderRecallSection` | agent 全量 |
| 待固化 note 注入 | `instruction.go:pendingAdHocNotes` | agent 全量 |
| `memories_read/list/search` 的根 | `codetools/memory_tools.go:memoryBackend` | agent 全量 |
| `read_file` 的额外只读根 | `codetools/tools.go:89` | agent 全量 |
| phase-2 输入集 | `store.go:SelectStage1ForPhase2` | agent 全量 |
| consolidation agent 的 confinement 根 | `agentrun/memory_startup.go:205` | agent 全量 |
| consolidation 任务行 | `jobs.go:consolidateJobKey(agentID)` | agent 全量 |
| ad-hoc note 落盘目录 | `localbackend.AdHocNotesDir` | agent 全量 |

**结论**：污染不是"模型分类没做好"，而是"系统里根本没有可分类的字段"。
因此修法必须是——**把 project 提升为一等分区键，贯穿存储布局、检索过滤、固化输入、
以及 agent 的文件系统边界**，而不是在注入前按标题文本切片（那只是把散文当数据用，
是同一个根因的另一种表现）。

### 1.3 已有的可复用基础

仓库里项目身份已经存在，不需要新发明：

- `memories.ProjectRoot(cwd)` / `memories.ProjectKey(root)`（`internal/memories/project.go`）；
- `Runner.ProjectKey` 由 `ChatSession.SetWorkingDir` 一次性确定并传播到子 agent
  （`clifacade/chat_session.go:441`、`agentrun/factory.go`）；
- `AgentToolRuntime.ProjectKey` 已经存在，工具层拿得到；
- plan 存储已经按项目分目录：`<workspaceRoot>/plans/<projectKey>/`
  （`internal/planstore/planstore.go:34`）—— 本方案沿用同一套惯例。

---

## 2. 目标模型：Scope（作用域）

引入唯一的新概念 **Scope**，取值两种：

- `project:<projectKey>` —— 与某个代码库绑定的一切：任务组、仓库约定、命令、坑与修法；
- `global` —— **只放用户级偏好**：语言/语气、协作方式、"永远/绝不"类跨项目规则、通用工作流习惯。

两级的分工是刻意的：只做 project 隔离会把"回答用中文""改完必须跑测试"这类偏好在每个项目重学一遍
（这是今天唯一靠"全局混装"得到的好处）；只做 global 就等于今天的污染。
两级同时存在，才能既止血又不丢能力。

### 2.1 新目录布局

```
<workspaceRoot>/memories/
  global/                              # scope = global
    MEMORY.md
    memory_summary.md
    extensions/ad_hoc/notes/*.md       # 明确标记为跨项目的 note
    promotion_candidates.md            # 生成的输入：各项目上报的跨项目候选
    .git
  projects/
    -Users-doudou-workspace-unionj-cloud-forebrain-harness/   # scope = project:<ProjectKey>
      MEMORY.md
      memory_summary.md
      global_candidates.md             # 生成：本项目上报的跨项目偏好候选（global pass 的输入）
      raw_memories.md                  # 生成：仅本项目的 stage-1 原料
      rollout_summaries/*.md           # 仅本项目
      skills/<name>/
      global_candidates.md             # 本项目 pass 上报的跨项目偏好候选（≤N 条）
      extensions/ad_hoc/notes/*.md
      extensions/<ext>/instructions.md
      phase2_workspace_diff.md
      .git                             # 每个 scope 自己的 phase-2 基线

<workspaceRoot>/state/
  memories/<scopeSegment>/             # 每个 scope 一个目录：<projectKey> 或 global
    memory-rollouts/<threadHash>-<ts>.jsonl
    memory-consolidation               # 该 scope 的固化水位标记
```

`scopeID`：`global` 或 `project-<projectKey>`（文件名安全形式）。

### 2.2 项目身份的唯一定义

```go
// internal/memories/scope.go
type ScopeKind string
const (ScopeProject ScopeKind = "project"; ScopeGlobal ScopeKind = "global")

type Scope struct { Kind ScopeKind; Key string } // Key 仅 project 有效

func GlobalScope() Scope
func ProjectScopeForCwd(cwd string) (Scope, bool) // cwd 解析不出项目 -> false
func (s Scope) ID() string           // "global" | "project/<key>"
func (s Scope) dir() string          // "global" | "projects/<key>"
```

**没有项目身份的会话（cwd 为空）不进入项目记忆**：不注入项目摘要、不抽取、不写项目 note，
只享有 global 偏好。刻意不设 `_unscoped` 兜底桶 —— 那正是本方案要消灭的混装桶，
换个名字留下来毫无意义。

`ProjectScopeForCwd` 内部走 `ProjectRoot -> ProjectKey`。

**`ProjectRoot(cwd)`**（已有，`project.go:14`）：`Abs` → `EvalSymlinks` → 逐级向上找到
第一个含 `.git`（目录或普通文件）的目录；找不到就用 cwd 自身的绝对路径；cwd 为空则返回空串。
新增一条 **worktree 归一**规则：

> 若 `<dir>/.git` 是普通文件且内容形如 `gitdir: <main>/.git/worktrees/<name>`，
> 则项目根取 `<main>`（主检出），而不是 worktree 目录。

理由：同一仓库的多个 worktree 是同一个项目。不归一的话，每开一个 worktree 就是一个冷启动的空
scope，并各自触发一次 consolidation —— 既丢记忆又费钱。

**`ProjectKey(root)`** 改成 **Claude Code 口径**：项目根的完整绝对路径，斜杠换成中横线。

```go
// /Users/doudou/workspace/unionj-cloud/forebrain-harness
//   -> -Users-doudou-workspace-unionj-cloud-forebrain-harness
func ProjectKey(path string) string
```

（Windows 上先 `filepath.ToSlash`，再把 `/` 与 `:` 一并换成 `-`。）

它是纯函数、零状态：不需要注册表、不需要分配器、没有并发创建的竞态，
而且整条路径都编码在目录名里，**同名不同路径的项目天然不会撞进同一个桶**
—— 这正是本次要修的污染在"两个都叫 api 的项目"上的变种。旧实现的
`名字_sha256前8位` 形式（`forebrain_6d2298cb`）被这条规则取代；
`ProjectKey` 当前唯一的消费点是 `shell_tool.go:61` 导出的 `FOREBRAIN_PROJECT_KEY`
环境变量，没有任何地方解析它的格式，改格式是安全的。

（`internal/planstore` 的 `plans/<裸项目名>` 是更早的写法，本次不动它；
需要对齐的话可以后续单独做。）

**不变式（必须写进注释并有测试覆盖）**：读路径与固化路径都从**同一个会话 cwd**导出 scope
（读路径经 `Runner.ProjectKey`，固化路径经 stage-1 行上持久化的 `project_key`），
因此两边不可能对同一个会话给出不同的项目。

---

## 3. 各层改造

### 3.1 Root API：删掉"忘记项目"这条路

```go
// internal/memories/root.go
type Roots struct{ workspaceRoot string }

func ResolveRootsForAgent(workspaceRoot string) (Roots, error)
func (r Roots) Scope(s Scope) Root  // Root{MemoryRoot: memories/<s.dir()>, StateRoot: state}
func (r Roots) Base() string        // memories/  —— 仅枚举 scope 时使用
func (r Roots) ListProjectScopes() ([]Scope, error)
```

`Root` 结构体保留（下游签名基本不用改），但**语义从"agent 的记忆根"变成"某个 scope 的根"**。
`ResolveRootForAgent` **删除**：按根因修复的规则，不保留一个"容易用错"的 API，
让"忘记指定 scope"变成编译错误。

同时删掉 `Root.stateRoot()` 里那条 `filepath.Dir(MemoryRoot)/state` 的回退分支 ——
目录多了一层之后它会算错，而 `Roots` 总是显式给出 StateRoot，这个分支是死代码。

需要改的调用点（共 7 处）：

| 文件 | 改成 |
| --- | --- |
| `agentrun/memory_instruction_llm.go` | 会话 scope + global |
| `agentrun/memory_startup.go` | pipeline 按 scope 跑 |
| `codetools/memory_tools.go` | backend 挂 project + global 两个根 |
| `codetools/tools.go` | 只读根收窄到 project + global |
| `skilllifecycle/memory_source.go` | 列当前项目 + global 的 skill |
| `clifacade/chat_session_slash_handlers.go` | reset 按 scope |
| `gateway/memories_api.go` | reset 按 scope |

### 3.2 数据库：给 stage-1 产出打上项目标签

`internal/store/schema.sql` 里 `fb_memory_stage1_outputs` **直接声明成最终形态**
（全新库口径，不写 `ALTER TABLE` / `ensureColumn` 之类的增量迁移）：

```sql
  project_key TEXT NOT NULL CHECK (TRIM(project_key) <> ''),
...
CREATE INDEX IF NOT EXISTS idx_fb_memory_stage1_agent_project
  ON fb_memory_stage1_outputs(agent_id, project_key, source_updated_at DESC, thread_id DESC);
```

`CHECK` 与 `agent_id` 同款：让"没有项目标签的记忆行"在数据库层面无法存在，
而不是靠调用方自觉。

- 写入：stage-1 完成时由 `SessionCandidate.Cwd` 算出（Go 侧，一次），
  与 `raw_memory` 一起落库。**持久化而不是每次现算**：目录被删/被移之后
  `ProjectRoot` 会退化成 cwd 绝对路径，现算会让老记忆漂移到一个幽灵项目里。
- 读取：`SelectStage1ForPhase2(ctx, scope, limit, maxUnusedDays)` 增加 `o.project_key=?`。
- 任务行：`consolidateJobKey(agentID)` → `consolidateJobKey(agentID, scope)`
  = `agentID + "|" + scope.ID()`，于是每个 scope 有独立的 lease / cooldown / retry。
  `enqueueGlobalPhase2On` → `enqueuePhase2On(ctx, exec, agentID, scope, watermark)`，
  stage-1 成功时入队**该 thread 所属项目**的 scope。

### 3.3 Pipeline：按 scope 固化

`Pipeline` 持有 `Roots` 与 `SessionScope`（当前会话的项目 scope）。`Run` 的顺序：

1. prune / 证据清理：仍是 agent 级（不变）；
2. stage-1 抽取：仍是 agent 级（每个 thread 独立，抽完打上 project_key）；
3. **phase-2 只跑当前会话的项目 scope**，随后**仅当 global 输入变化时**再跑一次 global。

第 3 条是成本设计：不在你没打开的项目上烧 consolidation，
下次你进那个项目时它自然会跑。**每次启动的固化次数与今天一致（≈1 次）**，
不会因为你有 10 个项目就变成 10 次。

固化 agent 的 confinement 根改为 scope 目录（`memory_startup.go:205`），
于是"项目 A 的固化 pass 写到项目 B 或 global 去"在文件系统层面就不可能发生 ——
这是结构性保证，不是提示词约定。

`syncPhase2WorkspaceInputs`、`memoryWorkspaceDiff`、`resetGitBaseline`、
`markConsolidationStart` / `lastConsolidationStart` 全部按 scope 目录工作；
一个 scope 的全部 state 收在一个目录里：`state/memories/<scopeSegment>/`，
下面放它的证据快照与固化水位标记。清空一个 scope 就是删掉它的记忆目录内容加这一个目录，
不会留下描述已不存在内容的水位标记。

### 3.4 读路径（用户直接感知的那一半）

```go
func RenderTurnInstruction(roots Roots, scope Scope, opts InstructionOptions) (string, error)
```

渲染顺序与预算：

| 段 | 来源 | 预算 |
| --- | --- | --- |
| 项目记忆摘要 | `projects/<key>/memory_summary.md` | 1900 tok |
| 全局偏好摘要 | `global/memory_summary.md` | 600 tok |
| 待固化 note | 本项目 note + global note | 1200 tok（不变） |
| capture 规则 | 模板 | 不变 |

**注入总量不超过今天的 2500 + 1200**，但其中项目那 1900 tok 全部是相关内容
（今天的 2500 tok 里可能大部分是别的技术栈）。信噪比提升，成本不升。

#### 缓存安全性论证（对应硬规则 1）

- 该开发者消息依旧**每会话渲染一次并冻结**（`memoryInstructionLLM.bySession`，
  `memory_instruction_llm.go:88` 的注释已经写明了为什么不能每轮重渲）。本方案不改这一点。
- 缓存 key 增加 project key：`flags|sessionID|projectKey`。
  原因：同一 session 下的子 agent 可能被钉在另一个 cwd 上，它的项目摘要与父级不同；
  不入 key 会串用。入 key 之后，父/子各自命中自己的稳定前缀。
- 会话中途新写的 note、中途完成的 consolidation **依旧不改注入内容**（下一会话生效），
  与现状一致。
- 净效果：注入块变小、变稳定，缓存命中率只增不减。

### 3.5 工具层与只读根：让越界读不出来

`localbackend.Backend` 改为多根挂载：

```go
type mount struct{ prefix, dir string }   // {"", projects/<key>}, {"global", global/}
func NewScoped(projectDir, globalDir string) Backend
```

- `MEMORY.md` → 项目；`global/MEMORY.md` → 全局；
- `List` / `Search` 遍历两个挂载点并给出带前缀的相对路径；
- `AddAdHocNote(scope, filename, note)` 按 scope 落盘；
- 现有的隐藏文件/符号链接/越界路径拒绝逻辑保持不变，`global` 作为保留名。

`codetools/tools.go` 的 `AdditionalReadRoots` 从 `memories/` 收窄为
`memories/projects/<key>` + `memories/global`。注意这一层的准确语义：读路径对越界路径
不是硬拒绝，而是交给沙箱与审批层裁决（`path_approval.go` 的 fall-through）。所以收窄的
实际效果是**别的项目的记忆不再落在"无需审批即可读"的集合里** —— 它从普通可读状态变成
需要沙箱放行的东西，和任何其他越界文件一视同仁。真正的硬边界在 `memories_*` 工具那一侧：
它们只挂载本项目与 global 两个根，任何路径都够不到第三个项目。证据快照也按 scope 分目录。

工具 schema 变化（`memories_add_ad_hoc_note` 增加 `scope` 枚举）属于工具表变更，
按仓库既有规则**只在新会话生效**，不在会话中途改变前缀。

### 3.6 提示词：与新结构对齐

- `templates/read_path.md`：base path 参数化；明确告诉模型"这个记忆目录只包含项目
  `<root>` 的记忆，外加一个很小的 `global/` 用户偏好目录"；删掉跨项目路由的那套话术。
- `templates/capture.md`：新增 scope 规则 —— **默认 project**；只有当用户的措辞明确跨项目
  （"永远/每个项目/绝不"）时才写 global，并给出正反例。落盘目录由工具参数决定，
  是一次性的数据决策，不是每次检索时的文本判断。
- `templates/consolidation.md` 拆成两份：
  - `consolidation_project.md`：删掉 `### <cwd / project scope>` 这一层索引
    （改为按任务族 + 日期），保留 `applies_to:` 用于仓库内部的差异（子目录、分支）；
    新增一条：**在本项目里观察到的、明显属于用户级跨项目偏好的内容，写进
    `global_candidates.md`（限 N 条），不要写进 `MEMORY.md`**。
  - `consolidation_global.md`：输入 = 各项目上报的候选 + global note + 现有 GLOBAL 内容；
    输出**只允许** `## User Profile` / `## User preferences` / `## General Tips`；
    硬性长度上限；明确禁止出现项目专属事实（仓库名、路径、命令）。
- `templates/stage_one_input.md`：输入头里带上项目根，让原始记忆天然带项目意识。

### 3.7 Global 层如何被喂养（复用同一条流水线，不新增机制）

1. 项目 pass 每次运行时**声明式地重写**自己的 `global_candidates.md`
   （"截至目前本项目看到的跨项目用户偏好"）；
2. global pass 的输入同步阶段把所有项目的候选文件带项目标签拼进
   `global/promotion_candidates.md`；
3. global pass 在 `global/` 内固化出 `MEMORY.md` + `memory_summary.md`。

候选文件是声明式的（每次重写而不是追加），所以整个过程幂等，不需要额外的队列语义。

---

## 4. 全新库口径：不迁移、不兼容、不兜底

本方案按"这个特性今天才第一次上线"来写，代码里**不出现**任何面向存量数据的路径：

- `schema.sql` 直接是最终形态，没有 `ALTER TABLE` / `ensureColumn` / 版本号判断；
- 目录布局直接是最终形态，没有"探测旧布局 → 归档/清理"的一次性代码；
- 没有 `_unscoped` 之类的兜底分区，没有"字段缺失时退化成 X"的降级分支；
- `ResolveRootForAgent` 直接删除而不是保留成 deprecated 包装。

本机上已有的旧记忆由人工清掉，属于运维动作，不进产品代码：

```sh
WS=~/.forebrain/workspace          # 非 main primary agent 用它自己的 workspace root
rm -rf "$WS/memories" "$WS/state/memories" "$WS/state/memory-rollouts" "$WS/state/memory-consolidated-at"
sqlite3 ~/.forebrain/state/*.sqlite \
  'DROP TABLE IF EXISTS fb_memory_stage1_outputs; DROP TABLE IF EXISTS fb_memory_jobs;'
```

两张表被 drop 之后由 `schema.sql` 以新形态重建。`fb_sessions` / `fb_messages` 不动
（会话是原料来源），`<ws>/skills/` 下已 promote 的技能不动（那是工作区资产，不是记忆）。

清空后的行为：第一次进任何项目时项目摘要不存在，recall 段不渲染、只有 capture 段，
与全新安装一致。这条不是自然成立的，需要一条明确规则支撑：**没有任何输入、也没有任何
已存产物的 scope，直接跳过固化，不调用 agent**。否则光是生成的输入文件（`raw_memories.md`
里的 "No raw memories yet."）就足以构成一个 diff，让每个新项目一开张就烧一次固化调用，
并留下一份"什么都没有"的摘要，从此每一轮都注入。已经有产物的 scope 即使当前没有输入也照跑 ——
那正是证据消失后修剪存量记忆的通道。跳过时刻意不推进水位标记：标记表示"这一趟已经消化了哪些
note"，而这一趟什么都没消化。

随后按现有节流参数（`max_rollouts_per_startup=2`、
`min_rollout_idle_hours=6`、`max_rollout_age_days=10`）从仍在库里的会话重新抽取，
这次带正确的 `project_key`，几次启动后各项目自愈。10 天之前的会话不再被抽取，
那部分历史永久丢失 —— 这是清库重来的既定代价。

## 5. 命令与 UI

- `/memories` 面板顶部显示当前 scope（项目名 + 根路径）与该 scope 的统计；
  reset 拆成两项：**"重置本项目记忆"（默认）** / "重置全部项目 + 全局"。
- 记忆清理应通过 TUI 内部管理能力或底层 API 暴露，不再新增独立 CLI 子命令。
- scope 列表和项目搬迁修复通过 TUI 内部管理入口调用底层 memory service，
  不增加 `forebrain memory ...` 命令族。
- 配置项：**不新增必需开关**。可选 `memories.global_preferences`（默认 true）
  控制是否注入 global 段。

---

## 6. 测试（新增 `internal/memories/project_isolation_test.go`，风格对齐现有 `tenant_isolation_test.go`）

1. `SelectStage1ForPhase2(project A)` 永远不返回 B 的行（同租户、同一个库）。
2. `RenderTurnInstruction` 在 A 的 scope 下包含 A 的摘要、且**不包含** B 摘要里的任何独有串。
3. `localbackend` 多根：A 的会话 `memories_search` 搜不到 B 文件里的字符串；
   `global/...` 可达；`../` 越界仍被拒。
4. `codetools` 只读根：A 的会话用 `read_file` 打不开 B 的 `MEMORY.md`。
5. phase-2 任务：A 与 B 各自独立 claim；A 处于 cooldown 不阻塞 B。
6. note scope 路由：默认落项目目录；显式 global 落全局目录；两者的 pending 注入互不串。
7. worktree 归一：主检出与其 worktree 解析出同一个 project key。
8. 无项目身份的会话：cwd 为空时不注入项目段、stage-1 不认领、note 不落项目目录，
   且不会凭空造出任何桶。
9. 缓存不变式：同一会话内两次渲染返回**同一个字符串实例/内容**；
   project key 不同的两个 runtime 得到不同实例（回归 `bySession` 语义）。

---

## 7. 分阶段实施计划

| 阶段 | 内容 | 主要文件 |
| --- | --- | --- |
| P0 | Scope 类型、worktree 归一、`Roots` API、删除 `ResolveRootForAgent` 与死回退 | `memories/scope.go`(新)、`project.go`、`root.go` |
| P1 | schema 最终形态 + store 按 scope 过滤 + 每 scope 任务键 | `store/schema.sql`、`memories/store.go`、`memories/jobs.go` |
| P2 | pipeline 按 scope 固化；证据、水位、git 基线、confinement 根按 scope | `memories/pipeline.go`、`storage.go`、`workspace.go`、`evidence.go`、`agentrun/memory_startup.go` |
| P3 | **读路径**：注入项目摘要 + 全局摘要、缓存键加 project、工具多根、只读根收窄 | `memories/instruction.go`、`agentrun/memory_instruction_llm.go`、`memories/localbackend/backend.go`、`codetools/memory_tools.go`、`codetools/tools.go` |
| P4 | 提示词拆分与改写 | `memories/templates/*` |
| P5 | global 层：候选上报 + global pass + note 的 `scope` 参数 | `memories/pipeline.go`、`localbackend`、`codetools/memory_tools.go` |
| P6 | TUI/gateway/skill 提案的 scope 化；不增加独立 CLI 命令 | `clifacade`、`gateway`、`skilllifecycle` |
| P7 | 测试与验证：`go build ./...`、`go test ./internal/memories/... ./internal/codetools/... ./internal/agentrun/...` | — |

**最小可交付（止血）= P0–P4**：此时 project 隔离完整生效，
global 段为空（不渲染），跨项目偏好暂时在每个项目各自重学。
P5 补上 global 层后，恢复"用户级偏好只说一次"的能力。

---

## 8. 已知边界与后续

- **项目改名/搬家**会产生新 key、旧 scope 变孤儿。目录名本身就是编码后的绝对路径，
  人可以直接看出孤儿目录对应哪个旧路径并手工改名；尚未提供 `forebrain memory move` 之类的命令。
- **同一仓库的两个 clone** 是两个 scope（路径不同）。可接受，需要时手工 move 合并。
- **超深路径**的目录名可能触到单个文件名 255 字节上限；与 Claude Code 同样的限制，
  不为它加截断兜底，触到时 `MkdirAll` 会显式报错。
- **monorepo** 是一个 scope；包级差异由项目内 `applies_to:` 表达。
- **跨项目的技术知识**（例如团队 Go 规范）只有在"用户偏好"形状时才会经 global 层传播；
  这是刻意的取舍 —— 放宽它就是把污染放回来。
