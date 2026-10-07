# 计划 002：会话的目录身份在出生时由创建者给定，不再改写全局默认值

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **漂移检查（先运行）**：
> `git diff --stat 1a6d708..HEAD -- pkg/state/session_store.go pkg/turn/service.go pkg/turn/session.go pkg/gateway/api_extra.go pkg/process/open.go`
> 计划 001 会改 `pkg/turn/session.go`（`UserTurn`/`PersistUserTurn`）和 `pkg/gateway/api_extra.go`（`handleChatMessages`），这是预期的。其它出入按 STOP 条件处理。

## 状态

- **优先级**：P1（顺带发现的既有缺陷，按规矩纳入本期；计划 005 依赖它）
- **工作量**：S
- **风险**：LOW
- **依赖**：无
- **类别**：bug
- **基线**：提交 `1a6d708`，2026-10-03

## 为什么要做

会话行的 `cwd` 就是这个会话的"项目身份"：记忆流水线按它决定会话的记忆归到哪个项目（`pkg/memory/jobs.go:133-170` 用 `ProjectScopeForCwd(candidate.Cwd)`）。
今天，网页在项目空间里新建会话时，接口把**整个会话存储**的默认 cwd 改成了那个项目的根目录，而且再也不改回来。从那以后，这个 gateway 进程新建的**每一个**会话（普通对话、渠道会话、定时任务每次触发的会话）都被记在那个项目名下，它们的记忆被抽进那个项目，跨项目串味，违反记忆的项目隔离。
另外，这几个默认值字段在 HTTP 请求的协程里被写，在别的协程里被读，没有任何同步，属于数据竞争。

根因：会话的目录身份是**每个会话在创建时**就该定下的事实，代码却把它建模成存储上一个可变的、进程级的默认值。修复：创建者在创建时直接给出身份；进程级默认值只代表"本进程的启动目录"，启动后只有配置热加载会改记忆模式，并且读写都加锁。

## 现状（基线 `1a6d708` 的事实）

`pkg/state/session_store.go:17-26`：

```go
type SessionStore struct {
	db           *sql.DB
	memoryMode   string
	memorySource string
	cwd          string
	gitBranch    string

	agentMu sync.RWMutex
	agentID string
}
```

`:72-92`：`ConfigureMemoryDefaults(memoryMode, memorySource, cwd, gitBranch string)` 直接给这四个字段赋值，没有锁。注释写的是："Only a caller that owns that identity decision may call this. A caller that merely reacts to a configuration change uses SetMemoryMode instead."
`:100-105`：`SetMemoryMode` 只写 `memoryMode`，也没有锁。

`:141-170`，唯一的会话 upsert：

```go
func (s *SessionStore) ensureSession(ctx context.Context, q dbtx, id string, title string) error {
	now := time.Now().Unix()
	mode := strings.TrimSpace(s.memoryMode)
	...
	res, err := q.ExecContext(ctx,
		`INSERT INTO fb_sessions(id, title, updated_at, created_at, memory_mode, memory_source, cwd, git_branch, agent_id)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at
		WHERE fb_sessions.agent_id=excluded.agent_id`,
		id, title, now, now, mode, strings.TrimSpace(s.memorySource), strings.TrimSpace(s.cwd), strings.TrimSpace(s.gitBranch), s.AgentID())
```

调用方有 4 处：`Ensure`（`:115`），以及 `:1355`、`:1751` 两条写消息的路径，它们在事务里先确保会话存在（另一处是定义本身）。冲突时只更新 `updated_at`，所以身份字段只在**出生那一刻**写入，之后不变。

两个调用 `ConfigureMemoryDefaults` 的地方：

- `pkg/process/open.go`（基线 `:308`；工作区里并行的 LSP 改动已把它挪到 `:338`，用 `grep -n ConfigureMemoryDefaults pkg/process/open.go` 定位）（正确）：启动时把默认值设为启动目录：`sessStore.ConfigureMemoryDefaults(memory.SessionModeForConfig(cfgRoot), source, launchDir, memory.GitBranch(launchDir))`。
- `pkg/gateway/api_extra.go:4150-4155`（**缺陷**），在 `handleProjectSessionsCreate`（`:4130` 起）里：

  ```go
  	if s.Sessions != nil {
  		// The session row records the project root as its cwd so history and
  		// memory scopes read the directory the conversation is about.
  		s.Sessions.ConfigureMemoryDefaults(memory.SessionModeForConfig(s.liveCfg()), memory.SessionSourceWebchat, p.Root, memory.GitBranch(p.Root))
  	}
  ```

  随后用 `s.Core.CreateSession(r.Context(), title)` 建会话（`:4161`），`Core` 为 nil 时退回 `state.NewID("web")` 加 `s.Sessions.Ensure`（`:4167-4176`），再 `store.BindSession`（`:4182`）和 `pool.BindSession`（`:4189`）。

`pkg/turn/session.go:41-62` 的 `Service.CreateSession`：用 `state.NewID(source)` 生成 id，标题为空就以 id 作标题，然后 `s.sessionStore.Ensure(ctx, id, stored)`。`s.sessionStore` 的类型是 `pkg/turn/service.go:20-25` 的接口 `SessionRepository`（`Ensure`、`SetTitle`、`ListSessionsRecent`、`ListRecentMessages`）。

`SetMemoryMode` 的调用方：`pkg/process/config_reload.go:250`（配置热加载）和 `pkg/tui/chat_session.go:1990`。

## 设计

- `pkg/state/session_store.go`：
  - 新增

    ```go
    // SessionBirth is what a session is born with and keeps: the directory it
    // is about. The memory pipeline files the session's memories under that
    // directory's project, so it is decided by whoever creates the session —
    // a project's session is born in the project root — and never by a
    // default some other request changed. A zero Cwd is the process's launch
    // directory, the default every ordinary session is born in.
    type SessionBirth struct {
    	Cwd       string
    	GitBranch string
    }
    ```

  - 新增 `func (s *SessionStore) EnsureAt(ctx context.Context, id, title string, birth SessionBirth) error`，语义同 `Ensure`，只是身份来自 `birth`。`Ensure` 改为 `return s.ensureSession(ctx, s.db, id, title, SessionBirth{})`。
  - `ensureSession` 增加参数 `birth SessionBirth`。函数开头在锁内取默认值的快照：`birth.Cwd == ""` 时 cwd 和 branch **都**用默认值（两者成对，不能一个用传入值、一个用默认值）；memory mode 和 memory source 总是用默认值。另外两处事务内的调用传 `SessionBirth{}`。
  - 给默认值加锁：新增字段 `defaultsMu sync.RWMutex`。`ConfigureMemoryDefaults`、`SetMemoryMode` 持写锁写入，`ensureSession` 持读锁取快照。在 `ConfigureMemoryDefaults` 的注释里写明：它只在进程启动时由组合根调用，表示启动目录；会话各自的目录走 `EnsureAt`。
- `pkg/turn/service.go`：`SessionRepository` 增加 `EnsureAt(ctx context.Context, id, title string, birth state.SessionBirth) error`。
- `pkg/turn/session.go`：新增 `func (s *Service) CreateSessionAt(ctx context.Context, title string, birth state.SessionBirth) (SessionCreateResult, error)`，把今天 `CreateSession` 的函数体搬进来，最后调 `s.sessionStore.EnsureAt(...)`。`CreateSession` 改为 `return s.CreateSessionAt(ctx, title, state.SessionBirth{})`。
- `pkg/gateway/api_extra.go` 的 `handleProjectSessionsCreate`：
  - 删除 `ConfigureMemoryDefaults` 那段（连同上面的注释）。
  - 定义 `birth := state.SessionBirth{Cwd: p.Root, GitBranch: memory.GitBranch(p.Root)}`。
  - `Core` 分支改为 `s.Core.CreateSessionAt(r.Context(), title, birth)`；`Core == nil` 分支把 `s.Sessions.Ensure(...)` 改为 `s.Sessions.EnsureAt(r.Context(), res.ID, stored, birth)`。
  - 函数头注释保留"the session's cwd is the project root"，并补一句：这个身份是会话出生时给定的，不经过存储的默认值。

## 缓存影响

无。会话行的 `cwd`/`git_branch` 只被记忆流水线（`pkg/memory/jobs.go`、`pkg/memory/store.go:266`）和 `ForkInto` 读取（`grep -rn "cwd" pkg/state/*.go pkg/memory/*.go` 核实过），不进入任何请求的前缀。

## 需要的命令

见 README"常用命令"。本计划额外用到：

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 数据竞争 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/state/ -run 'SessionBirth\|SessionDefaults' -count=1` | `ok`，没有 `DATA RACE` |

## 范围

**要改的文件**：

- `pkg/state/session_store.go`、`pkg/state/session_store_test.go`
- `pkg/turn/service.go`、`pkg/turn/session.go`、`pkg/turn/session_test.go`，以及 `pkg/turn` 测试里所有实现 `SessionRepository` 的替身（编译器会指出来）
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md`（状态行）

**不要碰**：

- `pkg/process/open.go` 里启动时的那次 `ConfigureMemoryDefaults` 调用：设启动目录是正确的，保持原样。
- `pkg/memory/*`：记忆流水线读 `cwd` 的逻辑是对的，错的是写入方。
- 历史数据：无法可靠区分哪些旧会话被记错了 cwd（见 README"考虑过但不做的"），不写数据修复。
- `ForkInto` 复制 `cwd` 的语义：分叉出的会话本来就是同一段对话，沿用源会话的目录是对的。

## 步骤

### 第 1 步：存储层

按设计改 `pkg/state/session_store.go`。测试写进 `pkg/state/session_store_test.go`：

1. `TestSessionBirthIsTheCreatorsAndNeverMovesTheDefault`：`ConfigureMemoryDefaults("disabled", "webchat", "/launch", "main")`；`EnsureAt(ctx, "p1", "p1", SessionBirth{Cwd: "/proj", GitBranch: "feat"})`；再 `Ensure(ctx, "a1", "a1")`。断言 `p1` 的 `cwd/git_branch` 是 `/proj`、`feat`，`a1` 的是 `/launch`、`main`（直接用 `SELECT cwd, git_branch FROM fb_sessions WHERE id=?` 读）。
2. `TestSessionBirthKeepsCwdAndBranchTogether`：`EnsureAt(..., SessionBirth{})` 时两者都取默认值。
3. `TestSessionDefaultsAreSafeToChangeWhileSessionsAreBorn`：一个协程循环 `SetMemoryMode`，另一个循环 `Ensure` 新会话，各 200 次。这个测试是给 `-race` 用的。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -count=1` → `ok`；`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/state/ -run 'SessionBirth|SessionDefaults' -count=1` → `ok` 且无 `DATA RACE`。

### 第 2 步：引擎层

按设计改 `pkg/turn/service.go`、`pkg/turn/session.go`。编译 `./pkg/turn/...` 并修好所有实现 `SessionRepository` 的测试替身（新方法记录收到的 `birth`）。在 `pkg/turn/session_test.go` 加一条：`CreateSessionAt` 把 `birth` 原样交给 `EnsureAt`；`CreateSession` 交出零值。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ -count=1` → `ok`。

### 第 3 步：gateway

按设计改 `handleProjectSessionsCreate`。在 `pkg/gateway/api_extra_test.go` 加 `TestProjectSessionIsBornInTheProjectAndLeavesOthersAlone`，参照该文件里已有的项目会话测试（`grep -n "handleProjectSessionsCreate" pkg/gateway/api_extra_test.go`）：在一个项目下建会话，然后用 `handleChatSessionCreate` 建一个普通会话。断言前者 `cwd` 是项目根目录，后者 `cwd` 是测试设定的启动目录（夹具里先 `ConfigureMemoryDefaults` 一个启动目录）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`；`grep -rn "ConfigureMemoryDefaults(" pkg --include='*.go' | grep -v _test` → 只剩 `pkg/process/open.go` 一处。

### 第 4 步：全量回归

运行 README 常用命令里的格式、静态检查、Go 全量测试、架构约束、`deadcode`。

**验证**：全部达到成功标志，`deadcode` 输出与基线一致。

## 完成标准（全部满足）

- [ ] `grep -rn "ConfigureMemoryDefaults(" pkg --include='*.go' | grep -v _test` 只有 `pkg/process/open.go`
- [ ] 第 1 步的三个测试存在且通过，`-race` 无报告
- [ ] 第 3 步的 gateway 测试存在且通过
- [ ] README 常用命令里的 Go 部分全部达到成功标志
- [ ] `git status` 里只有"范围"列出的文件有改动
- [ ] README 状态行已更新

## STOP 条件

- "现状"里的摘录和实际代码对不上。
- 除了上面列出的地方，发现还有别的代码在运行期调用 `ConfigureMemoryDefaults`。
- `ensureSession` 的调用方不是 4 处（含定义），或者某处调用需要的身份不是"默认值"。
- 改动需要触碰 `pkg/memory`。

## 维护说明

- 以后任何"在某个目录/项目里"新建会话的入口，都必须用 `EnsureAt` / `CreateSessionAt` 传入 `SessionBirth`，不得再修改存储的默认值。计划 005 的定时任务会话就是第一个新用户。
- `SessionBirth` 将来如果要加"出生时的用途"（`source`），由计划 005 加；加的时候同样遵守"只在出生那一刻写入"。

## 执行记录

- 执行于 2026-10-05，紧随计划 001。8 个文件按设计落地（含编译器指出的 `pkg/gateway/session_context_test.go` 替身补 `EnsureAt`）。
- `go test ./pkg/state/` ok；`-race`（`SessionBirth|SessionDefaults`）ok、无 `DATA RACE`；`./pkg/turn/`、`./pkg/gateway/` ok；全量 29 包 ok；架构测试 ok；`deadcode` 与基线逐行一致。
- `grep -rn "ConfigureMemoryDefaults(" pkg --include='*.go' | grep -v _test`：仅剩 `pkg/process/open.go:340`（启动设启动目录，保持原样）。
