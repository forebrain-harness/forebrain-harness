# Plan 183：`/resume` 后恢复 read_file 的防覆盖基线——不再把“上一进程已经读过”误判为“从未读取”

> **执行者须知**：按步骤执行；每一步先跑验证并确认预期。触发 STOP 即停下报告，不要即兴发挥。
> **本计划获批前不得改产品源码**。批准后直接在主树 dev 当前源码实施，不建 worktree、不建分支、不 commit、不 push、不开 PR；改动留待 owner 审阅。
> 工作区已有其他计划的改动，不得回滚、清理或格式化无关文件。
>
> **前置锚点（先跑）**：
> `grep -cF 'return "", fmt.Errorf("must read_file before edit")' pkg/tool/file_tools.go` = 1；
> `grep -cF 'st.RememberRead(abs, fi.ModTime(), fi.Size(), raw)' pkg/tool/file_tools.go` = 1；
> `grep -cF 'readFiles:       make(map[string]ReadState)' pkg/tool/state.go` = 1；
> `grep -cF 'if r.tools == nil {' pkg/run/runner.go` ≥ 1；
> `grep -cF 'const stateSchemaVersion = 19' pkg/state/schema_migrations.go` = 1。若最后一项不同，只按 Step 1 的动态版本规则处理；其它锚点不符即 STOP 1。
>
> **基线**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tool ./pkg/run ./pkg/process ./pkg/architecture -count=1`（900 s）。记下既有失败，不能把与本修复无关的红例算进本计划。

## Status

- **Priority**: P1（真实会话中阻断续聊后的正常编辑；防覆盖语义不能靠绕过门禁解决）
- **Effort**: M
- **Risk**: HIGH（文件防覆盖基线与状态库迁移；错误恢复可能覆盖用户外部修改）
- **Depends on**: none；与 145–153 独立。若同时执行，状态库版本号按实际落地顺序顺延，不能两份计划占用同一版本
- **Category**: bug + migration
- **Planned at**: dev `d9d0b4d` + 2026-10-27 工作区；源码符号与本会话 live DB 已核实

## Why this matters

`read_file` 的“编辑前必须读过”不是一条永久布尔权限，而是“模型见过的内容”与“当前磁盘内容”的防覆盖基线。
现在它只存在于进程内存，所以 `/resume` 恢复了对话文本，却没有恢复这项运行时事实：用户在旧进程读过的文件，在新进程第一次 `edit_file` 时被误报 `must read_file before edit`。
修复必须恢复**同一 session、同一路径、同一内容版本**的基线，同时保留当前文件 mtime/size/SHA-256 检查；不能取消门禁，也不能在 edit 内偷偷重读后直接放行。

## 根因与现场证据（已核实，不是推测）

### 1. 代码证据链

1. `pkg/tool/file_tools.go` `NewFileReadTool` 的文本分支（当前约 `:179-185`）：

   ```go
   if err := validateTextReadableFile(abs, raw); err != nil { ... }
   fi, err := statFile(abs)
   if err == nil {
       st.RememberRead(abs, fi.ModTime(), fi.Size(), raw)
   }
   ```

   图片/PDF 分支不进入文本读取基线，这是既有语义，本计划不改。

2. `pkg/tool/state.go`（当前约 `:2728-2764`）：`RememberRead` → `rememberTouch` → `s.readFiles[absPath] = ReadState{...}`。
   `ReadState` 含 `AbsPath, ModTime, Size, SHA256Hex, ReadAt, TouchKind`；`NewState`（约 `:1197-1215`）总是新建空 map。
3. `edit_file`（`pkg/tool/file_tools.go` 约 `:1085-1114`）：
   - `st.GetReadState(abs)` miss → `must read_file before edit`；
   - 命中后仍检查 `fi.ModTime().After(rs.ModTime) || fi.Size() != rs.Size`；
   - 再读当前文件并 `readStateMatchesContent(rs, raw)` 校验 SHA-256，漂移则 `file changed since last read`。
   这三层检查全部保留。
4. `pkg/process/open.go` 每次进程启动构造新的 Runner；`pkg/run/runner.go` 在 `r.tools == nil` 时 `tool.NewState(...)`。没有从状态库或 transcript 恢复 `readFiles` 的路径。
5. `TrimStaleReadStates` 的清理阈值是 `runnerPoolSessionEviction = 24 * time.Hour`（`pkg/process/runner_pool.go`），与本次约 25 分钟的故障无关。

### 2. 本会话 live DB 证据（只读查询获得）

session：`cli-53066e56-cb53-4957-b8f3-24833f226106`。

- plans/145–153 的首次 `read_file` 都发生在 run `0b972c57-8009-4c9c-bd0a-735083916de7`；失败的八次 `edit_file` 发生在 run `1deca00c-a90b-4b6d-9e9c-c0793d4b4288`。
- `fb_runs.owner` 分别为 `tui-9a901374-881a-4d2f-b5c4-47e6246a8b30` 与 `tui-8b6d5972-9c1c-4f70-a341-33e8f84e1558`，证明是两个进程。
- owner 补充该会话通过 `/resume` 续聊；进程启动时间与 run 时间也吻合。
- `148` 唯一成功，是因为新进程中又执行过一次 `read_file`；其余八份只有旧进程的读取记录。重新读取全部文件后，同一组并行编辑全部成功。
- 因而根因不是 `multi_tool_use.parallel`、old_string 不唯一或 24 小时清理，而是**续聊新进程没有读取基线**。

### 3. 为什么不从 transcript 直接恢复

`read_file` 的事件只保留给模型/界面看的窗口文本（文本页最多 200 行）及分页信息，并没有完整文件内容、原始 mtime 与完整 SHA-256。
用 preview 重建会伪造“读过全文件”的事实。`edit_file` 内自动读当前文件再放行也不合格：模型看不到外部变化，old_string 仍匹配时可能覆盖别人刚写的内容。

## 修复设计

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 同一 session 在进程 A 读过，进程 B `/resume` 后首次编辑，文件未变 | `must read_file before edit` | 从状态库恢复基线，经当前 stat + SHA 校验后允许编辑 |
| 同上，但文件在进程外被改过 | 首次门禁报未读 | 恢复后仍报 `file changed since last read`，不写文件 |
| session B 未读过，只有 session A 的基线 | 新进程没有可恢复数据 | 仍报 `must read_file before edit`，不得跨 session 借用 |
| 另一主代理的同名 session/path | 不应可见 | 查询同时按 `agent_id + session_id + path` 限定，仍拒绝 |
| 同进程反复读取/编辑 | 内存基线生效 | 保持原路径；最新读取覆盖同 key 的旧基线 |
| 删除 session | 内存随进程消失 | DB 基线随 session `ON DELETE CASCADE` 删除 |
| 无 SessionStore / 无 session id 的工具测试或嵌入用法 | 内存门禁 | 保持原行为，不凭空要求持久化 |

### 数据与读取边界

新增 `fb_file_read_states`，key 为 `(session_id, path)`，另存 `agent_id` 并在所有读写中校验。
只保存绝对路径、mtime、size、SHA-256、读取时间、`touch_kind='read'`，**不保存文件正文**。
读取顺序固定为：当前进程内存命中 → 使用；内存 miss 且有 SessionStore/session id → 查询同一 session 的持久基线 → 仍走 edit/write 现有的 stat 与内容 SHA 校验。
本计划只持久化 `read_file` 产生的 `TouchKind='read'` 基线；`RememberWrite` 的进程内行为不改。若上次只有写入没有读取，续聊后要求重新读取是安全的既有行为，不在本缺陷内扩大。

## Current state：可复用接口与约束

- `pkg/state/session_store.go`：`NewSessionStore(db, agentID)`、`AgentID()`、`DB()`、`Ensure/EnsureAt`；同包私有 `requireOwned(ctx, q, sessionID)` 可复用租户检查。
- `pkg/state/db.go`：`Open(ctx, absSQLitePath, *OpenOptions)`；测试用临时 home/临时 sqlite，不继承 ambient `FOREBRAIN_HOME`。
- `pkg/tool/state.go`：`State` 已 import `pkg/state`，可增加一个接口字段而不增加包级 fan-out；`ConversationSessionIDFromContext(ctx)` 在 `pkg/tool/state.go` 已存在。
- `pkg/run/runner.go` `Load` 已有 `r.SessionStore` 与 `r.tools.SetFileChangeRecorder(r.SessionStore)` 的安装范例；持久化 store 在同一位置安装。
- `pkg/state` 当前 25 个生产文件，可新增 `file_read_store.go` + 同名测试；`pkg/tool`、`pkg/run` 不新增生产文件。
- `stateSchemaVersion` 当前为 19，v18/v19 已被其它功能占用，不能再写死“19→20”之外的旧版本名。实施时若已变化，取**当时当前值 + 1**，同步函数/测试名并报告；若另一并行计划刚占用同一号，以实际落地顺序顺延。
- `TestStateMigrationMatchesFreshSchema` 比较迁移后的 `sqlite_master` 文本；新表 SQL 必须与 schema.sql 逐字一致。

## Commands you will need

| 目的 | 命令 | 成功时 |
|---|---|---|
| 编译 | `go build ./...` | exit 0 |
| 定点状态库 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'FileReadState|StateMigrationMatchesFreshSchema' -count=1` | ok |
| 定点工具/续聊 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'ReadBaseline|EditFile|WriteFile' -count=1` | ok |
| 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tool ./pkg/run ./pkg/process ./pkg/architecture -count=1` | 无新增失败 |
| vet | `go vet ./pkg/state ./pkg/tool ./pkg/run ./pkg/process` | exit 0 |
| 真机 | `.claude/skills/run-forebrain/SKILL.md` 的隔离 home + fake provider + tmux | 见 Step 6 |

## Scope

**In scope**：

- `pkg/state/schema.sql`
- `pkg/state/schema_migrations.go`
- `pkg/state/schema_migrations_test.go`
- `pkg/state/file_read_store.go`（新建）
- `pkg/state/file_read_store_test.go`（新建）
- `pkg/tool/state.go`
- `pkg/tool/file_tools.go`
- `pkg/tool/file_tools_test.go`
- `pkg/run/runner.go`（只安装持久化 store）
- `pkg/run/runner_test.go`（只加安装断言，如现有 fixture 可直接覆盖）

**Out of scope**：

- 取消/弱化 `must read_file before edit`、`file changed since last read` 或 old_string 检查。
- 从截断的 transcript preview 伪造基线；在 edit/write 内隐式重读后直接放行。
- 图片/PDF 的读取语义、`RememberWrite` 的持久化、draft redirect 规则、并行编辑器、UI 文案。
- `pkg/process/open.go` 的 `ReadStatesProvider`（它只给 recent-files context source，不是本缺陷的恢复入口）。
- plans/145–153 的产品功能实现、任何前端文件、用户已有未提交改动。

## Steps

### Step 1：新增会话级读取基线表与迁移

1. `schema.sql` 增加（列注释说明其用途与不含正文）：

   ```sql
   CREATE TABLE fb_file_read_states (
     session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
     agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
     path TEXT NOT NULL,
     mod_time_ns INTEGER NOT NULL,
     size INTEGER NOT NULL,
     sha256 TEXT NOT NULL,
     read_at_ns INTEGER NOT NULL,
     touch_kind TEXT NOT NULL DEFAULT 'read' CHECK (touch_kind = 'read'),
     PRIMARY KEY (session_id, path)
   ) STRICT, WITHOUT ROWID;
   ```

2. 在 `schema_migrations.go` 末尾增加一个 `CREATE TABLE IF NOT EXISTS` 迁移，SQL 与上面逐字一致；`stateSchemaVersion` 取执行时当前值 + 1。
3. 新测试从“去掉 `fb_file_read_states` 并降低 user_version”重开，验证表恢复；既有 fresh-schema 对比不变。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'StateMigrationMatchesFreshSchema|FileReadStateMigration' -count=1` → ok。

### Step 2：`SessionStore` 提供持久化读写

新建 `pkg/state/file_read_store.go`：

```go
type FileReadState struct {
    SessionID string
    AgentID   string
    Path      string
    ModTimeNS int64
    Size      int64
    SHA256    string
    ReadAtNS  int64
    TouchKind string
}

func (s *SessionStore) SaveFileReadState(ctx context.Context, in FileReadState) error
func (s *SessionStore) FileReadStateForSession(ctx context.Context, sessionID, path string) (FileReadState, bool, error)
```

- `Save` 先 `requireOwned`；规范化 `path=filepath.Clean(strings.TrimSpace(path))`，要求绝对路径、非空 session、`sha256` 为 64 个小写 hex、`TouchKind='read'`。非法输入直接报错，不写行。
- `AgentID` 始终取 `s.AgentID()`，不能由调用方伪造；`ReadAtNS` 由调用方从实际读取时间生成，重复读取用 `ON CONFLICT(session_id,path) DO UPDATE` 覆盖 mtime/size/hash/read_at。
- 查询条件必须含 `session_id=? AND path=? AND agent_id=?`；`sql.ErrNoRows` → `ok=false, err=nil`。
- 不写“查不到就返回别的 session 的最近基线”之类的 fallback。

**Verify**：新增 store 单测通过：round-trip、重复读覆盖、session 隔离、agent 隔离、删除 session 级联；`go test ./pkg/state -run FileReadState -count=1` → ok。

### Step 3：`tool.State` 内存 miss 时恢复同一 session 的基线

1. 在 `pkg/tool/state.go` 定义窄接口（`*state.SessionStore` 满足）：

   ```go
   type ReadStateStore interface {
       SaveFileReadState(context.Context, state.FileReadState) error
       FileReadStateForSession(context.Context, string, string) (state.FileReadState, bool, error)
   }
   ```

   `State` 加 `readStateStore ReadStateStore` 与 `SetReadStateStore(store ReadStateStore)`；转换函数负责 `time.Time` ↔ UnixNano 与 SHA 字段名，不改变 exported `tool.ReadState` 的形状。
2. 新增 `RememberReadForSession(ctx, absPath, mod, size, content) error`：
   - 用与 `rememberTouch` 相同的 hash/time 构造一条 `ReadState`，避免两处算法漂移；可提取一个私有构造函数让两者复用。
   - 有 store 且有 `ConversationSessionIDFromContext(ctx)` 时，先成功写入 DB，再把同一基线放进内存；DB 错误直接返回，不让一次“看似成功的读取”在下次 resume 又悄然丢失。
   - 无 store/无 session id 时只走现有内存路径。
   - `NewFileReadTool` 的文本分支改调它并处理 error；`RememberWrite` 不变。
3. 新增私有 `readStateForEdit(ctx, abs) (ReadState, bool, error)`：
   - 先 `GetReadState(abs)`；命中即返回。
   - miss 时若有 store/session id，读 `FileReadStateForSession`；DB error 原样返回，不得等同于“未读”。
   - 找到后转换成 `ReadState` 并可缓存进当前 State。它只是候选基线，绝不能跳过调用方现有的 stat/SHA 检查。
   - `edit_file` 与覆盖已有文件的 `write_file` 改用此函数；draft redirect 仅在普通+持久基线都不存在时按原顺序尝试。
4. `pkg/run/runner.go` 在安装 file-change recorder 的同一位置调用 `r.tools.SetReadStateStore(r.SessionStore)`。

**Verify**：`go build ./...` → exit 0；既有 `pkg/tool` 测试不因其默认内存路径变红。

### Step 4：先写会失败的真实回归测试

在 `pkg/tool/file_tools_test.go` 使用真实 `state.Open` + `state.NewSessionStore` + `NewFileReadTool/NewFileEditTool`，不能只直接往 map 塞值：

1. `TestEditFileAfterResumeRestoresReadBaseline`：session `s` ensure 到真实 DB；State A 安装该 store，ctx 带 `WithConversationSessionID(ctx,"s")`，read_file；新建 State B（模拟新进程）安装同一 store，不再次 read，edit_file 成功且内容只发生指定替换。
2. `TestEditFileAfterResumeStillRejectsExternalChange`：同上前读；在 State B 创建前用 `os.WriteFile` 改文件；edit 返回 `file changed since last read`，当前外部内容不被覆盖。
3. `TestReadBaselineDoesNotCrossSessions`：session A 读过；State B 以 session B 调 edit → `must read_file before edit`。
4. `TestReadBaselinePersistenceErrorDoesNotMasqueradeAsUnread`：store 查询返回 sentinel error，edit 返回该 error，不得报未读或写文件。可用小 fake 实现接口，仅此测试使用 fake。
5. `TestWriteFileAfterResumeRestoresReadBaseline`：覆盖已有文件的 write_file 同样通过恢复与 hash 检查；外部修改仍拒绝。
6. 原有用例继续保持：未读 edit 拒绝、old_string 不唯一拒绝、并发 edit 串行。

测试应先证明：只完成 Step 1/2 而不接 Step 3 时，第 1/2/3 个测试会按预期暴露缺陷；不要把“测试变绿”建立在手工 `RememberRead` 到新 State 上。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'ReadBaseline|EditFileAfterResume|WriteFileAfterResume' -count=1` → ok。

### Step 5：包级与竞争回归

- 跑 `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -race -tags fts5 ./pkg/tool -run 'ReadBaseline|EditFileSerializesConcurrentWritesToSameFile' -count=1`，验证同一文件并发读写没有数据竞争或丢失更新。
- 跑 Commands 中的状态库/工具/run/process/architecture 包级回归与 vet。
- `gofmt -l` 仅允许列出本计划未改文件；本计划改过文件必须无输出。

**Verify**：上述命令 exit 0；没有新增失败。

### Step 6：tmux 真机 `/resume` 验收（必做，不得以单测代替）

先完整阅读 `.claude/skills/run-forebrain/SKILL.md`，用它的 driver/fake provider、隔离 `FOREBRAIN_HOME` 与隔离项目目录；构建带 `-tags fts5`。不得继承真实 home，不得使用真实 provider key。

1. fake provider 第一轮只让模型 `read_file` 目标文本，随后结束；保存 session id 与 provider 请求记录。
2. 退出第一个 TUI 进程，启动第二个进程并通过 `/resume` 选择该 session；第二轮 fake provider 只让模型对同一路径 `edit_file`，**脚本中不得再次 read_file**。
3. 预期：编辑成功，文件只有目标替换；屏幕没有 `must read_file before edit`；两进程 run owner 不同（可用只读 sqlite 查询证明）。
4. 再在第二个进程外手工改文件，第三轮仍只请求 edit：必须显示 `file changed since last read`，外部内容保持。
5. 对照：另一个 session 没有读过该文件时仍显示 `must read_file before edit`。
6. 保存每段 `capture-pane`、fake 请求顺序与文件 hash；清理 tmux、fake provider 和临时 home。若验收暴露与根因同类的新缺陷，停止并补入本计划，不做症状绕过。

## Done criteria

- [ ] 新表与迁移测试通过；读写均按 agent + session + path 隔离，删除 session 级联。
- [ ] `TestEditFileAfterResumeRestoresReadBaseline` 等 Step 4 测试全部存在且通过；Step 4 所述未接线时会失败已核实。
- [ ] 续聊恢复后仍执行 mtime/size/SHA 检查；外部修改、跨 session、跨 agent 均不能写入。
- [ ] `go build ./...`、race 定点、包级测试、`go vet ./pkg/state ./pkg/tool ./pkg/run ./pkg/process` exit 0，无新增失败。
- [ ] tmux 新进程 `/resume` 真实验收通过，含 run owner、屏幕、请求顺序与外部修改负例证据。
- [ ] `grep -cF 'readStateForEdit(ctx' pkg/tool/file_tools.go` ≥ 2；`grep -cF 'SetReadStateStore(r.SessionStore)' pkg/run/runner.go` = 1。
- [ ] 改动只在 Scope 内（`git status --short` 逐项归因）；不 commit、不 push。
- [ ] `plans/README.md` 本计划行更新为 DONE，并记录迁移实际版本与真机结果。

## STOP conditions

1. 前置锚点含义变化，或已读事件与 run owner 的现场事实无法在新基线复现。
2. 方案需要跳过 stat/SHA、从截断 transcript 伪造基线、或在 edit 内隐式重读后放行——这不是可接受的修复，停下重新评审。
3. 迁移与其它并行计划撞号，或 `TestStateMigrationMatchesFreshSchema` 因 SQL 文本不一致失败；不要改该测试迁就实现。
4. 真实工具上下文没有 conversation session id，导致主路径根本无法持久化；先列出实际 ctx 传播缺口停下，不要把数据写到全局/path-only key。
5. 真机 `/resume` 仍需第二次 read、外部修改能写入、或另一 session 能借用基线。
6. 需要修改 Out of scope 文件才能解决；说明根因链并交回评审，不扩大范围。

## Maintenance notes

- 读取基线属于会话运行时事实，不是 prompt prefix，不得塞进 `fb_session_prompt_state` 的 freeze 语义。
- 基线不是授权：路径权限、审批、`GuardWrite`、old_string 和当前磁盘 hash 都仍须各自通过。
- 新读取覆盖同 key 旧基线；外部写入不更新 DB，所以恢复后会被 SHA 检出，这是有意设计。
- 若以后持久化 `RememberWrite`，必须单独处理“文件已写成功、基线写库失败”的部分完成报告，不能在本计划中顺手改。
- 真机验收与客户端回归测试必须使用真实 `NewFileReadTool`/`NewFileEditTool`/`SessionStore` 构造路径；只在 map 中手工布置 ReadState 测不到本缺陷。
