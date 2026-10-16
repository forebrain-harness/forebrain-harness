# Plan: 会话列表过滤掉没有任何消息的空会话（/resume 面板）

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步都跑"验证"命令并确认预期输出后再进入下一步。命中
> STOP conditions 就停下来报告，不要自行发挥。**不建 git worktree、不在当前分支实施之外做任何 git 操作、不 commit**
> （owner 裁决：改动留工作区由 owner 审阅；计划文件本身禁止 commit）。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16，工作区干净）。
> 执行前先 `git rev-parse --short HEAD` 与 `git status --porcelain`：
> 若 HEAD 已变或 in-scope 文件已被改动，逐条对照本文件 "Current state" 的行号摘录与线上代码；
> 对不上即按 STOP condition 处理（本仓库无历史 SHA diff 可依赖，以 file:line 摘录为准）。

## Status

- **Priority**: P1（owner 直接报的交互缺陷）
- **Effort**: S
- **Risk**: LOW（只改"列不列出"的查询谓词 + 测试；不动数据、不动 web 应用代码、不动 CLI）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `fa4af8a`, 2026-10-16
- **落盘**: 已实施完成，2026-10-16 从会话计划目录另存到本文件（内容一致；仅本行与文末 "Implementation Status" 为落地后补写）。
  **状态：已实施并真机验收通过（2026-10-16）**。
- **需要 owner 拍板的一处**：有一条**现有** Playwright 断言专门钉住了"点了『新建项目会话』却没发消息，它仍出现在项目会话页"
  （`frontend/e2e/project-space.spec.ts:83-96`）。本变更按新语义**改写**这条断言（见 Step 7 与行为矩阵之后的说明）。
  其余面没有验收脚本钉住空会话可见。

## Why this matters

TUI 的 `/resume` 面板里堆着大量显示为 “New conversation” 的条目（owner 截图）。实测这些条目**没有任何消息**：
它们只是 `/new`、`/model`、`/migrate`、web 建会话端点等"建行先于对话"的路径留下的空壳行。选一个空会话 resume
毫无用处，却把真正想继续的会话挤到列表下面。修复后：列表里的每一条都是"有内容、可继续"的会话，TUI / web /
频道三条面（共享同一组 store 查询）行为一致。

## Current state（执行者要看到的事实，全部摘自本次亲自阅读的代码）

### 1) TUI `/resume` 的面板与标签

`pkg/tui/run.go:4823-4872`（节选，行号即现状）：

```go
// promptRecentSession is /resume's picker over the other sessions, newest
// first; current, the one this chat is in, is not listed, since resuming it
// would change nothing. listed is false when there was nothing else to pick.
func promptRecentSession(ctx context.Context, session Session, selector Selector, readLine func(context.Context) (string, error), current string) (sid string, listed bool) {
	...
	if canPage {
		list, err = pagedLister.ListSessionsRecentPaged(ctx, resumeSessionPageSize, 0)
	} else {
		list, err = session.ListSessionsRecent(ctx, resumeSessionPageSize)
	}
	...
	appendPage := func(page []SessionSummary) {
		for _, item := range page {
			if strings.TrimSpace(item.ID) == current {
				continue
			}
			// A session still titled by its id has not been named by a first
			// message yet.
			title := strings.TrimSpace(item.Title)
			if title == "" || title == strings.TrimSpace(item.ID) {
				title = "New conversation"
			}
			...
```

`pkg/tui/chat_surface.go:279-287` 把面板接到 store（项目绑定时走项目列表）：

```go
func (s *ChatSession) ListSessionsRecentPaged(ctx context.Context, limit, offset int) ([]state.SessionSummary, error) {
	...
	if projectID := strings.TrimSpace(s.ProjectID); projectID != "" {
		return s.sessStore().ListSessionsForProject(ctx, projectID, limit, offset)
	}
	return s.sessStore().ListSessionsRecentPaged(ctx, limit, offset)
}
```

`pkg/tui/chat_surface.go:253-263` 是它的非分页孪生兄弟 `ListSessionsRecent`（项目绑定 → `ListSessionsForProject`，否则
`ListSessionsRecent`）。

### 2) 根因：四个会话列表查询都不要求会话有消息

`pkg/state/session_store.go:736-771`：

```go
// ListSessionsRecent is what a person may want back, newest first: every
// purpose but a scheduled task's fire, which is reached from its task's run
// record instead (decision D3).
func (s *SessionStore) ListSessionsRecent(ctx context.Context, limit int) ([]SessionSummary, error) {
	...
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source, COALESCE(project_id, '')
		 FROM fb_sessions
		 WHERE agent_id = ? AND source NOT IN (?, ?)
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		s.AgentID(), SessionSourceCron, SessionSourceSubagent, limit)
```

`pkg/state/session_store.go:861-880`（`ListSessionsRecentPaged`，/resume 懒加载分页用的就是它）：

```go
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source
		 FROM fb_sessions
		 WHERE agent_id = ? AND source NOT IN (?, ?)
		 ORDER BY updated_at DESC, id ASC
		 LIMIT ? OFFSET ?`,
		s.AgentID(), SessionSourceCron, SessionSourceSubagent, limit, offset)
```

`pkg/state/session_store.go:831-837`（`ListSessionsForProject`，TUI 项目内 /resume、web 项目页会话列表）：

```go
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, project_id
		 FROM fb_sessions
		 WHERE agent_id = ? AND project_id = ? AND source = ?
		 ORDER BY updated_at DESC, id
		 LIMIT ? OFFSET ?`,
		s.AgentID(), projectID, SessionSourceConversation, limit, offset)
```

`pkg/state/session_store.go:787-793`（`ListSessionsOfSource`，web 抽屉 `/api/chat/sessions` 与技能工坊任务列表）：

```go
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source
		 FROM fb_sessions
		 WHERE agent_id = ? AND project_id IS NULL AND source = ?
		 ORDER BY updated_at DESC, id ASC
		 LIMIT ?`,
		s.AgentID(), source, limit)
```

四个查询都只看 `agent_id / source / project_id`：**只要有一行 `fb_sessions` 就被当成一条会话**。

### 3) 行会在"还没有任何消息"时被创建（空壳行的来源）

- `/new`：`pkg/turn/executor.go:314` `ctx.Sessions.Ensure(createCtx, sessionID, sessionID)`（注释解释了 title==id 的
  "尚未命名"哨兵语义）。
- `/model`：`pkg/tui/chat_slash.go:675` `store.Ensure(ctx, sid, sid)`（"A first-command model change must be durable:
  the row is created here"）。
- `/migrate`：`pkg/tui/chat_slash.go:1000` `store.Ensure(...)`（把迁移报告写成 session event 之前先建行）。
- web 建会话端点：`pkg/gateway/api_extra.go:1178` `s.Sessions.EnsureAt(ctx, id, stored, birth)`（`createSessionWithBirth`）。
- 网关 WS 绑定：`pkg/gateway/server.go:1574` `s.Sessions.Ensure(r.Context(), sid, sid)`。

即：**行的存在 ≠ 会话的存在**；列表却按行来列。

### 4) 现场证据（owner 真实库，只读查询，2026-10-16 上午）

库：`~/.forebrain/state/forebrain.state.sqlite`（一律 `sqlite3 -readonly`，不得写入）。

- `agent_id='main' AND source=''`：**411** 条会话，其中 0 消息行 **38** 条、无"可见"消息 **41** 条、
  `title = id`（面板里显示 "New conversation"）**35** 条。
- 项目 `prj_project-b0dee98b-…`（就是本仓库，`forebrain-harness`）：310 条列表行中 **30** 条 0 消息；
  最近 25 行里有 **3** 条落在 "New conversation"，且**最新的一条**（`cli-7bce6683-…`，今天 09:34）就是空的
  （唯一内容是 1 条 `migration_completed` 事件）。
- 例：`cli-b8781d36-…`、`cli-7658dd50-…`、`cli-8f21321e-…` 均为 0 消息、0 事件、0 run 的纯残渣。
- 空壳行都带 `project_id`（TUI 启动目录注册成项目），所以它们落在 /resume 的项目列表里。

### 5) 受影响的其它面（同一组查询）

- 非 TUI 的 `/resume`（频道 / web 斜杠）：`pkg/turn/executor.go:328-352` `execResume` → `ListSessionsRecent`；
  标签 `sessionSummaryTitle`（`pkg/turn/executor.go:1118-1124`）。
- web 抽屉：`pkg/gateway/api_extra.go:1383` `handleChatSessions` → `ListSessionsOfSource`。
- web 项目页：`pkg/gateway/api_extra.go:5841` `handleProjectSessionsList` → `ListSessionsForProject`。

### 6) 已经存在的同类语义（新谓词要与它们对齐，而不是另起一套）

`pkg/state/session_store.go:896-918` `SessionIDWithLatestMessage`：

```go
		`SELECT s.id FROM fb_sessions s
		 WHERE s.agent_id = ?
		   AND EXISTS (SELECT 1 FROM fb_messages m WHERE m.session_id = s.id AND m.visibility = 'visible')
```

`pkg/state/cron.go:622-628`：

```go
// The conversation column is derived: a fire's session holds a transcript
// exactly when it holds a visible message.
const cronRunSelect = `
SELECT ..., 
       EXISTS (SELECT 1 FROM fb_messages m WHERE m.session_id = fb_cron_runs.session_id AND m.visibility = 'visible')
FROM fb_cron_runs`
```

两者都是**"有没有可显示内容"**的显示判断（cron 那一列就是"这次 fire 有没有转录"）。

## 修复设计

### 判定语义（本计划的唯一新增语义）

> **一条 `fb_sessions` 行只有在它已经有消息时才是一条会话。**

```sql
EXISTS (SELECT 1 FROM fb_messages m WHERE m.session_id = fb_sessions.id)
```

**不看 `visibility`**（任意消息行都算）。理由（执行者不要"顺手优化"成可见过滤）：

1. owner 的原话是"这些会话实际是空的，无任何消息" —— 判据就是"有没有消息"。
2. 用 `visibility='visible'` 会连带隐藏"被 rewind 到最初消息之前"的会话：rewind 把消息置为 `'rewound'`，
   撤销入口在会话自己的时间线卡片上，一旦被列表隐藏就永远打不开、撤销不了（制造可达性陷阱）。
   本谓词把这类会话留在列表里（代价：打开后时间线为空）——刻意选择。
3. **既有测试就是硬约束**：`pkg/state/session_store_test.go:150-165`
   （`TestWithdrawUserTurnReleasesSessionTitle` 的 "only message" 子用例）先 `Append` 一条用户消息、再 `WithdrawUserTurn`
   撤回它，然后通过 `ListSessionsRecent` 读回标题（读不到就 `t.Fatalf("session %q not listed")`）。
   即"消息全被撤回的会话仍必须在列表里"是被现有测试钉住的契约，`visibility='visible'` 会让这条测试直接炸。
4. 三处既有探针（第 6 节）回答的是"有没有可画的内容"，本谓词回答的是"这条会话有没有成为过会话"。
   两者用途不同，不要合并、也不要去改那三处。

### 被否决的替代方案（不要再翻案）

- **"可见消息 OR 存在 session event"**：owner 库里 11 条只有 `/migrate` 报告 / MCP 启动报错卡的会话会**继续**以
  "New conversation" 出现在列表顶部（截图里最新一条正是这种），等于没修；而且它会隐藏 fully-rewound 的会话。
- **在 `pkg/tui/run.go` 的面板里跳过空行**：只修 TUI，web/频道不一致；而且分页页面会被跳空行掏空（20 条一页变成 3 条），
  懒加载语义被破坏。必须在 store 的查询里过滤，页才不会被空行占位。

### 行为变化矩阵（标注哪些是刻意修订）

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| TUI `/resume`（项目内）| 列出 0 消息会话（显示 "New conversation"）| 不列出 |
| TUI `/resume`（会话无项目绑定）| `ListSessionsRecentPaged` 同病 | 不列出 |
| 非 TUI `/resume`（频道 / web 斜杠，`execResume`）| 列出空会话 | 不列出 |
| web 抽屉 `/api/chat/sessions` | 列出空会话 | 不列出（web 的"新会话页"是首条消息才落行，正常路径无感） |
| web 项目页 `/api/v1/projects/:id/sessions` | 列出空会话 | 不列出（**刻意**：点了"新建会话"却没发消息就离开的那条会话不再出现在列表里；它仍能用 URL / 会话 id 打开并继续） |
| 技能工坊任务列表 `source=workshop` | 列出空会话 | 不列出（工坊建行后立刻发首条消息，正常路径无感） |
| 只有 rewind/withdraw 隐藏消息的会话 | 列出 | **仍列出**（刻意：保住撤销的可达性；打开后时间线为空） |
| `cron` fire / `subagent` worker 会话 | 不列出 | 不列出（不变） |
| 只有 `/migrate` 报告或启动报错卡的会话（0 消息）| 列出（"New conversation"）| **不列出（刻意修订）**：报告卡不是会话。行仍在磁盘上、`forebrain resume <id>` 仍能打开、退出提示打印的 id 也仍可用 |

**这条矩阵要 owner 拍板的点（唯一一处会改掉"现有验收断言"的行为）**：`frontend/e2e/project-space.spec.ts:83-96`
的 "project sessions live only in the project space" 用例，当初专门钉住了"点『新建会话』→ 不发消息 → 回到项目会话页，
『未命名会话』仍在列表里"这个 UX（它就是 `handleProjectSessionsList` → `ListSessionsForProject` 的列表）。本计划要
**改写这条断言**（改成：发过消息的会话在列表里；没发消息的不在）。其余面没有验收脚本钉住空会话可见。

## Scope

**In scope（只准改这些）**

- `pkg/state/session_store.go` — 新增谓词常量；四个查询加条件；四个函数的文档注释同步。
- `pkg/state/session_store_test.go` — 新增回归测试；两处既有用例补消息（见 Test plan）。
- `pkg/state/project_store_test.go` — `TestListSessionsForProjectPages` 补消息。
- `pkg/state/schema_migrations_test.go` — 读列表的那处用例补消息。
- `pkg/tui/chat_session_test.go` — `TestResumeListsOnlyTheSessionsProject` 补消息。
- `pkg/gateway/skills_download_test.go`、`pkg/gateway/api_extra_test.go` — 两处列表断言前补消息。
- `frontend/e2e/project-space.spec.ts` — 改写被本变更作废的那条断言，并补一条"没消息的项目会话不出现在项目会话页"的断言（只改这一个 spec 文件）。
- `pkg/tui/chat_slash.go` — **只改注释**（`persistMigrationReport` 上方 989-994 行关于"有行才 /resume 可达"的说法已不再成立）。
- `docs/plan/EMPTY_SESSION_FILTER_PLAN.md` — 本计划另存一份（实施阶段做）。

**Out of scope（看起来相关但明确不碰）**

- `ListChildSessionsRecent`（`pkg/state/session_store.go:700-734`）：web 花名册（`pkg/gateway/agents_api.go:242`）
  与 `/subagents` 挑选用的是它，列的是"活的子代理 / 导入的子会话"，空子行在那里有语义（刚出生的子会话也要进花名册）。不改。
- `SessionIDWithLatestMessage`、`cronRunSelect` 的可见性探针（显示语义）。不改。
- `pkg/tui/run.go` 的 "New conversation" 兜底标签、`pkg/turn/executor.go:1118-1124` 的 `sessionSummaryTitle`：
  仍然可达（有消息但 title 没被首条用户消息命名），不改。
- `frontend/src/**` 的应用代码（页面/组件/composable）：web 端不需要改（列表的服务端语义变了，前端字段与结构不变）。
  唯一要动的前端文件是 `frontend/e2e/project-space.spec.ts`（验收断言，不是产品代码）。
- 任何 `DELETE` / 数据清理：本次只改"列不列出"，**不删任何行**。
- `pkg/state/schema.sql` / 迁移：不新增列、不建索引。
- 不改 `/subagents`、`/fork`、`/agents` 等其它挑选项。

## Commands you will need

| 用途 | 命令 | 成功标准 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 定点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestConversationLists|TestListSessionsRecentPaged|TestListSessionsForProjectPages|TestProjectSessionListListsOnlyConversations' -count=1` | PASS（**`-run` 是 Go 正则**：用 `\|` 会变成字面竖线、一个用例都匹配不到而"假绿"，命令里必须是裸 `|`、外面单引号） |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui ./pkg/gateway ./pkg/turn ./pkg/migrate ./pkg/process -count=1` | 全 PASS（`./pkg/process` 也要进来：`worker_cli_test.go:594` 是 `ListSessionsOfSource` 的第四个消费者） |
| CI 同款 lint | `go vet ./...` | exit 0 |
| 全量（隔离 home） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全 PASS |
| web e2e（单 spec） | `FOREBRAIN_E2E_SPEC=e2e/project-space.spec.ts scripts/acceptance/web_e2e.sh` | 全 PASS |
| 验收二进制 | `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | 产出二进制 |

**web e2e 说明**：Playwright 套件（`frontend/package.json` 的 `pnpm e2e`）**不在 CI 里**（`.github/workflows/*.yml` 没有任何 e2e 步骤），
唯一跑它的是 owner 手工用的 `scripts/acceptance/web_e2e.sh`（自带隔离 home、自建假 provider、真实 Chrome）。
它支持 `FOREBRAIN_E2E_SPEC=e2e/<file>.spec.ts` 收窄到单个 spec——本计划必须显式跑一次收窄的 `project-space.spec.ts`，
否则改坏的那条 spec 不会被任何门发现（见 Step 7）。

**铁律（仓库 house rules）**：测试/验收**绝不能继承环境里已有的 `FOREBRAIN_HOME`**（agent 会话默认导出真实 home 指向
`~/.forebrain`，会把真实安装库迁移/写坏）。一律显式给临时 home，且用 `-tags fts5` 构建（漏 tag 会报
`no such module: fts5`，那是工具链问题不是产品 bug）。

## Git workflow

- 不建 worktree、不建分支、不 commit（owner 裁决：改动留当前工作区由 owner 逐个审阅）。
- 只允许在现有工作区（当前分支 `main`，HEAD `fa4af8a`）上直接改文件。
- 完成后 `git status --porcelain` 必须只包含 Scope 里列出的文件。

## Steps

### Step 1: 在 store 里加入唯一谓词常量

文件：`pkg/state/session_store.go`，位置：`ListSessionsRecent` 之前（现 736 行上方，紧跟 `ListChildSessionsRecent`
之后）。加：

```go
// conversationHoldsAMessage is the condition every conversation list carries:
// a session is a conversation once it holds a message. A row exists as soon as
// a surface needs the session's identity — /new, /model, /migrate, the web's
// create endpoint all ensure it before the conversation has said anything — so
// listing rows would offer a person conversations with nothing to resume.
//
// Any message row counts, whatever its visibility: a conversation the user
// rewound to before its first message still holds its rows, and its own view is
// the only place the rewind can be undone.
const conversationHoldsAMessage = `EXISTS (SELECT 1 FROM fb_messages m WHERE m.session_id = fb_sessions.id)`
```

**Verify**: `gofmt -l pkg/state/session_store.go` → 无输出。

### Step 2: 四个查询都带上这个谓词

同一个文件，四处 `WHERE` 末尾追加（保持参数顺序不变，谓词放在最后、不引入新参数）：

1. `ListSessionsRecent`（现 751-757）：

```go
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source, COALESCE(project_id, '')
		 FROM fb_sessions
		 WHERE agent_id = ? AND source NOT IN (?, ?)
		   AND `+conversationHoldsAMessage+`
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		s.AgentID(), SessionSourceCron, SessionSourceSubagent, limit)
```

2. `ListSessionsRecentPaged`（现 874-880）：

```go
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source
		 FROM fb_sessions
		 WHERE agent_id = ? AND source NOT IN (?, ?)
		   AND `+conversationHoldsAMessage+`
		 ORDER BY updated_at DESC, id ASC
		 LIMIT ? OFFSET ?`,
		s.AgentID(), SessionSourceCron, SessionSourceSubagent, limit, offset)
```

3. `ListSessionsForProject`（现 831-837）：

```go
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, project_id
		 FROM fb_sessions
		 WHERE agent_id = ? AND project_id = ? AND source = ?
		   AND `+conversationHoldsAMessage+`
		 ORDER BY updated_at DESC, id
		 LIMIT ? OFFSET ?`,
		s.AgentID(), projectID, SessionSourceConversation, limit, offset)
```

4. `ListSessionsOfSource`（现 787-793）：

```go
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, updated_at, source
		 FROM fb_sessions
		 WHERE agent_id = ? AND project_id IS NULL AND source = ?
		   AND `+conversationHoldsAMessage+`
		 ORDER BY updated_at DESC, id ASC
		 LIMIT ?`,
		s.AgentID(), source, limit)
```

同时更新这四个函数的文档注释，各加一句指出"没有消息的会话不列"（示例措辞：
`// A session with no message is not a conversation and is never listed: see conversationHoldsAMessage.`）。

**Verify**: `CGO_ENABLED=1 go build ./...` → exit 0。

### Step 3: 同步 `/migrate` 的注释（只改注释，不改逻辑）

`pkg/tui/chat_slash.go:989-994` 现文：

```go
// persistMigrationReport writes the report the user is about to see as a
// session event BEFORE displaying it, so quitting the moment it appears
// still leaves it replayable (shown implies recorded). The session row is
// ensured first: a conversation that has only ever run slash commands has
// no transcript row yet, and an event against a session id that owns no row
// is invisible to /resume — the report would be durable but unreachable.
```

改成（语义变更：只有报告、没有消息的会话不再出现在 /resume，报告仍随会话落盘、仍可在该会话内/用会话 id 复看）：

```go
// The session row is ensured first: fb_session_events.session_id is a foreign
// key, so an event against a session id that owns no row could not be stored at
// all. A conversation that has only ever run slash commands is not listed by
// /resume (it holds no message), so the report's durable copy is read back when
// the session itself is opened — by id, or while it is the open conversation.
```

**Verify**: `gofmt -l pkg/tui/chat_slash.go` → 无输出。

### Step 4: 补齐既有测试的前置数据（这些用例 pin 的是分页/用途过滤，不是"空会话"）

对每处，在断言列表**之前**给相关会话追加一条消息（用该测试里已有的 store/DB 句柄），并保持原断言不变：

| 文件:行 | 会话 | 要加的语句 |
|---|---|---|
| `pkg/state/session_store_test.go:280-287`（`TestListSessionsRecentPaged` 建 45 条）| `s-00`…`s-44` | 循环里加 `if _, err := acme.Append(ctx, id, "user", "hello "+id); err != nil { t.Fatal(err) }`，且必须放在该循环里已有的 `UPDATE fb_sessions SET updated_at = ?` **之前**：`Append` 走 `ensureSession` 会把 `updated_at` 顶成当前时间，顺序会失去确定性 |
| `pkg/state/session_store_test.go:350-356`（`TestConversationListsAreFilteredInTheQuery`）| `ordinary` | `if _, err := store.Append(ctx, "ordinary", "user", "the conversation"); err != nil { t.Fatal(err) }`，同样要放在 354 行那条 `UPDATE fb_sessions SET updated_at = 1` **之前** |
| `pkg/state/session_store_test.go:485-486`（`TestProjectSessionListListsOnlyConversations`）| `talk` | `if _, err := sessions.Append(ctx, "talk", "user", "hello"); err != nil { t.Fatal(err) }`（`fire` 不动，它本来就不该被列） |
| `pkg/state/project_store_test.go:265-276`（`TestListSessionsForProjectPages`）| `sess-p0`、`sess-p1` | 对这两个各 `Append(ctx, sid, "user", "hello")`（`sess-p2` 属于另一个项目，可加可不加） |
| `pkg/state/schema_migrations_test.go:1473-1483` | `sid` | 在 `SetSessionSource` 之前 `if _, err := store.Append(ctx, sid, "user", "hello"); err != nil { t.Fatalf(...) }` |
| `pkg/tui/chat_session_test.go:2173-2176`（`TestResumeListsOnlyTheSessionsProject`）| `cur` | `_, err = sessions.Append(ctx, "cur", "user", "hello"); require.NoError(t, err)`（`oth` 不用） |
| `pkg/gateway/skills_download_test.go:462-473` | 刚建的 workshop 会话 | 该用例现在不解析响应体，需要先解出 id：`var created struct{ ID string \`json:"id"\` }; require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))`，然后 `_, err := s.Sessions.Append(context.Background(), created.ID, "user", "hello"); require.NoError(t, err)`（放在 471 行的列表请求之前）；`json` 若未导入则从 `encoding/json` 加 |
| `pkg/gateway/api_extra_test.go:856-882`（项目会话列表用例）| 刚建的会话 `session.ID` | 在 873 行断言项目列表之前加 `_, err = s.Sessions.Append(context.Background(), session.ID, "user", "hello"); require.NoError(t, err)`（该用例已从响应体解出 `session.ID`） |

实现要点：**不要**为了通过测试去放宽过滤或改断言（那会变成症状补丁）。这些用例要的是"有内容的会话被正确列出 + 过滤在 SQL 里"。

顺带确认（不需要改，但要知道它们在门里）：`pkg/process/worker_cli_test.go:594` 用 `ListSessionsOfSource(SessionSourceSubagent, …)`
列出两个 worker 会话并断言 `len == 2`。它仍然通过：`RunSubagentSupervised` 在列出之前就把任务写进 worker 会话
（`pkg/process/worker_cli.go:88` 起 `turn.PersistUserTurn(... SessionID: sid ...)`），所以两个 worker 会话都有消息行。

**Verify**:
`CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui ./pkg/gateway ./pkg/process -count=1` → 全 PASS。

### Step 5: 新增回归测试

**(a) `pkg/state/session_store_test.go` 新增 `TestConversationListsHideSessionsWithoutAMessage`**
（模仿同文件 `TestConversationListsAreFilteredInTheQuery`（345 行起）的建数据方式：`openStateDB(t)` +
`NewSessionStore(db,"main")`）。断言四件事：

1. 四个 listers（`ListSessionsRecent`、`ListSessionsRecentPaged`、`ListSessionsForProject`（需先 `NewProjectStore`
   +`BindSession`，参照 `project_store_test.go:260`）、`ListSessionsOfSource(ctx, SessionSourceConversation, …)`）
   都不返回只 `Ensure` 过、没消息的会话；而"`Ensure` 之后 `Append` 一条"的会话都返回。
2. **分页不被空行占位**：造 3 条有消息 + 5 条空行，按 `limit=1` 逐页收（照 `TestListSessionsRecentPaged` 的 `collect`
   写法），得到的 id 序列必须正好是有消息的那 3 条、顺序 newest-first。
3. **刻意边界**：一条会话的消息全部 `UPDATE fb_messages SET visibility='withdrawn'`（或调 `WithdrawUserTurn`）后，
   它**仍然**被列出（谓词不看可见性）。
4. 另一租户（`NewSessionStore(db,"other")`）看不到别人的会话（保持既有租户不变式）。

**(b) `pkg/tui/chat_session_test.go` 新增 `TestResumeListsOnlyConversationsWithMessages`**
（照 `TestResumeListsOnlyTheSessionsProject`（2158 行起）的结构：`t.Setenv("FOREBRAIN_HOME", t.TempDir())` +
`OpenChatSessionWithConfigForProject(ctx, testConfig(), launch)` + `defer s.Close()`）。造一条只 `Ensure` 的空会话和
一条 `Append` 过的会话，断言 `s.ListSessionsRecent` 与 `s.ListSessionsRecentPaged` 都**不含**空会话、**含**有消息的会话。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui -run 'TestConversationListsHideSessionsWithoutAMessage|TestResumeListsOnlyConversationsWithMessages' -count=1 -v` → 两个用例 PASS。

### Step 6: 包级回归 + vet + 全量测试

**Verify**（依次）：
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui ./pkg/gateway ./pkg/turn ./pkg/migrate ./pkg/process -count=1` → 全 PASS。
2. `go vet ./...` → exit 0。
3. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` → 全 PASS。
（包/导入没有变化，`scripts/package-graph.sh` 不必跑；若它报 graph.json 变化，说明动到了不该动的包，按 STOP 处理。）

### Step 7: 改写被本变更作废的 web e2e 断言

文件：`frontend/e2e/project-space.spec.ts`，用例 **"project sessions live only in the project space"**（现 83-96 行）：

```ts
  await page.click('button:has-text("新的项目会话"), button:has-text("New session")')
  await page.waitForURL((url) => url.pathname === '/' && url.searchParams.has('session'), { timeout: 15_000 })
  await page.goto(projectUrl + '/sessions', { waitUntil: 'networkidle' })
  await expect(page.getByRole('button', { name: /未命名会话|Untitled session/ }).first()).toBeVisible({ timeout: 10_000 })
  // The drawer must not list the project session.
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await expect(page.locator('[data-testid="chat-drawer"]')).toBeVisible()
  const drawerText = await page.locator('[data-testid="chat-drawer"]').innerText()
  expect(drawerText).not.toContain('e2e-alpha')
```

改写为（保持"项目会话只在项目空间"的原意，同时钉住新契约：**没消息的会话不进任何会话列表**）：

```ts
  await page.click('button:has-text("新的项目会话"), button:has-text("New session")')
  await page.waitForURL((url) => url.pathname === '/' && url.searchParams.has('session'), { timeout: 15_000 })
  // A conversation with no message is not a conversation: the project's list
  // shows its empty state until the first message lands.
  await page.goto(projectUrl + '/sessions', { waitUntil: 'networkidle' })
  await expect(page.getByRole('button', { name: /未命名会话|Untitled session/ })).toHaveCount(0)
  // Send the first message; the conversation becomes listable under its title.
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'project-space-e2e')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  await page.goto(projectUrl + '/sessions', { waitUntil: 'networkidle' })
  await expect(page.getByRole('button', { name: /project-space-e2e/ }).first()).toBeVisible({ timeout: 15_000 })
  // The drawer must not list the project session.
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await expect(page.locator('[data-testid="chat-drawer"]')).toBeVisible()
  const drawerText = await page.locator('[data-testid="chat-drawer"]').innerText()
  expect(drawerText).not.toContain('e2e-alpha')
```

要点：
- `expectAssistantReply` 已在同文件第三个用例里使用（假 provider 模式下可用），直接复用，不要新写等待。
- 一条完整模型回合会超过默认 1 分钟超时：给该用例 `test.info().setTimeout(150_000)`（照第三个用例第 82 行的写法）。
- **只改这一个 spec 文件**；`frontend/src/**` 不动。`cron-fire.spec.ts:149-151`（抽屉里不得出现 cron fire 会话）语义不变，无需改。

**Verify**: `FOREBRAIN_E2E_SPEC=e2e/project-space.spec.ts scripts/acceptance/web_e2e.sh` → 该 spec 全 PASS
（脚本自带隔离 home / 假 provider / 真实 Chrome；失败时它会打印 gateway.err 末 50 行）。

### Step 8: 真机验收（tmux，测试绿 ≠ 真机可用）

构建：`go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`

隔离环境（**不得**用真实 home）：`ACCEPT_HOME="$(mktemp -d)"`、`PROJ="$(mktemp -d)"`。

**场景 1（复现→修复）**
```bash
tmux new-session -d -s fbacc -x 120 -y 42 "cd $PROJ && FOREBRAIN_HOME=$ACCEPT_HOME $PWD/build/bin/forebrain; echo EXIT_CODE=\$?; sleep 300"
tmux send-keys -t fbacc '/new' Enter        # 建一条没有消息的会话行
tmux send-keys -t fbacc '/resume' Enter     # 此刻还看不见它（当前会话被排除）
# 退出并重启，让它成为"别的会话"
tmux send-keys -t fbacc '/exit' Enter ; sleep 2
tmux new-session -d -s fbacc2 -x 120 -y 42 "cd $PROJ && FOREBRAIN_HOME=$ACCEPT_HOME $PWD/build/bin/forebrain; echo EXIT_CODE=\$?; sleep 300"
tmux send-keys -t fbacc2 '/resume' Enter ; sleep 1
tmux capture-pane -t fbacc2 -p | tail -20
```
预期：面板**不出现** "New conversation" 行；若该目录只有那一条空会话，应显示
`resume — There is no other conversation to resume.`（`pkg/tui/commands.go:51-56`）。
修复前用同样的脚本必须能看到 "New conversation"（若复现不出，见 STOP conditions 第 3 条）。

**场景 2（有内容的会话仍在、仍可 resume）**
在 `PROJ` 下启动 TUI，输入一句普通消息（如 `hello acceptance`）等回复，`/exit`；重启后 `/resume`：
预期面板出现以该消息为标题的行；按 Enter 选中后时间线重放出这条消息（`resumed — N messages above…`）。

**场景 3（回归：非 TTY 行为不变）**
`FOREBRAIN_HOME="$ACCEPT_HOME" ./build/bin/forebrain gateway start </dev/null` → 立即给出诊断并退出，不挂起。

验收结束：`tmux kill-session -t fbacc fbacc2`，删除临时目录。**不碰 `~/.forebrain`**。

### Step 9: 计划回填

- 把本文件另存到 `docs/plan/EMPTY_SESSION_FILTER_PLAN.md`（仓库惯例位置），状态行写"已实施并真机验收通过（YYYY-MM-DD）"，
  补"验收记录"小节（场景 → 证据：命令 + capture-pane 关键行）。
- 在会话计划目录里同步更新同一份内容。
- `git status --porcelain` 核对改动范围与 Scope 一致；**不 commit**。

## Test plan

- 新增：`TestConversationListsHideSessionsWithoutAMessage`（`pkg/state/session_store_test.go`）——修的就是这个回归：
  空会话不得进列表；同页分页不被空行占位；全隐藏消息的会话仍在（刻意边界）；租户隔离不变。
- 新增：`TestResumeListsOnlyConversationsWithMessages`（`pkg/tui/chat_session_test.go`）——把契约钉在 TUI 面（ChatSession →
  store）上，证明面板走的是过滤后的查询。
- 结构范式：`pkg/state/session_store_test.go:345`（`TestConversationListsAreFilteredInTheQuery`）、
  `pkg/state/session_store_test.go:271`（分页收集写法）、`pkg/tui/chat_session_test.go:2158`（真实 store + 隔离 home）。
- 既有用例：见 Step 4 的表（补消息，不改断言）。
- 既有 e2e 断言：`frontend/e2e/project-space.spec.ts` 那条"没发消息也出现在项目会话页"的断言按新契约改写
  （Step 7）——它属于**刻意修订**，不是回归。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui ./pkg/gateway ./pkg/turn ./pkg/migrate ./pkg/process -count=1` 全绿，
  含 2 个新用例；外加 `FOREBRAIN_E2E_SPEC=e2e/project-space.spec.ts scripts/acceptance/web_e2e.sh` 全绿。

## Done criteria（机器可查，全都要成立）

- [ ] `grep -c 'conversationHoldsAMessage' pkg/state/session_store.go` ≥ 5（1 处定义 + 4 处查询）
- [ ] `CGO_ENABLED=1 go build ./...` exit 0；`go vet ./...` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui ./pkg/gateway ./pkg/turn ./pkg/migrate ./pkg/process -count=1` 全 PASS，且两个新用例在 `-v` 输出里出现
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全 PASS
- [ ] `FOREBRAIN_E2E_SPEC=e2e/project-space.spec.ts scripts/acceptance/web_e2e.sh` 全 PASS
- [ ] tmux 场景 1/2/3 按预期取证（空会话不列出；有消息的会话列出且可 resume；非 TTY 行为不变）
- [ ] `git status --porcelain` 只含 Scope 列出的文件
- [ ] 计划文件已回填状态与验收记录（会话计划目录 + `docs/plan/EMPTY_SESSION_FILTER_PLAN.md`）

## STOP conditions

出现以下任一条，**停下来报告**，不要自行发挥：

1. "Current state" 里四个查询的代码与线上不一致（行号/谓词对不上）——代码已漂移。
2. 发现**任何**地方依赖"没消息的会话必须出现在会话列表里"（既有测试、验收脚本、前端流程断言）。
   已知且已处理的一处是 `frontend/e2e/project-space.spec.ts:83-96`（Step 7 改写）；**其它**任何一处都不要就地放宽过滤、
   也不要删测试，停下来报告后再定。
3. 真机场景 1 复现不出 "New conversation"（`/new` 后重启 `/resume` 本来就看不到空会话）——说明过滤点找错了。
4. 需要动 `frontend/src/**`（应用代码/组件/composable）、`ListChildSessionsRecent`、`SessionIDWithLatestMessage`、
   `cronRunSelect` 才能让测试过。
5. 需要给 `fb_sessions` 加列/加索引/写迁移才能实现。
6. 某个步骤的验证连续两次失败且原因不明。

## Maintenance notes

- 谓词是**查询内的**（不是取回后再过滤），所以 `LIMIT/OFFSET` 取的是过滤后的页：页不会被空行掏空，/resume 的懒加载
  分页语义不变。空行比例很大时 SQLite 会沿 `idx_fb_sessions_agent_updated` 走、逐行探测 `idx_fb_messages_session_visibility`
  索引；会话量到百万级且有大量空行时才需要考虑物化的 `has_message` 标志列（现在不需要）。
- **新增任何"会话列表"都必须带上 `conversationHoldsAMessage`**。当前家族：`ListSessionsRecent`、`ListSessionsRecentPaged`、
  `ListSessionsForProject`、`ListSessionsOfSource`。已接线到：TUI `/resume`（`pkg/tui/run.go:4826`）、非 TUI `/resume`
  （`pkg/turn/executor.go:328`）、web 抽屉（`pkg/gateway/api_extra.go:1383`）、web 项目页（`pkg/gateway/api_extra.go:5841`）、
  技能工坊任务列表。
- 审查要点：①四处查询都带了谓词且参数顺序没乱；②没有把谓词写成 `visibility='visible'`（那会引入 rewind 可达性陷阱）；
  ③`ListChildSessionsRecent` 没被顺手改（花名册需要刚出生的空子行）。
- 明确不做的后续（本次刻意不做，别当遗留 bug 追）：`/new`、`/model`、web 建会话端点仍会先建行——这是"身份先落地、
  内容后到"的设计，本次只改"列不列出"；不去删空行、不做清理任务。
- 若将来引入"不用的会话不建行"的改动，本谓词会变成冗余但无害（保留即可，它同时兜住了历史遗留的空行）。
- **web e2e 不在 CI 里**：`scripts/acceptance/web_e2e.sh` 是唯一入口（`.github/workflows/*.yml` 无 e2e 步骤），
  改了会话列表语义就要跑一次它（`FOREBRAIN_E2E_SPEC=e2e/project-space.spec.ts` 收窄）。若将来把 Playwright 接进 CI，
  这条维护注记可以删。
- `frontend/src/composables/useChatSessions.ts:51-55` 的 `createSession`（建行后立刻 `fetchSessions`）目前**没有任何调用方**
  （`grep -rn createSession frontend/src` 只剩 `ProjectSessions.vue` 自己直连 API 的那条）。若将来有人接线它，
  注意：新建的空会话在首条消息落地之前不会出现在抽屉列表里——这是本计划的语义，不是 bug。

## Findings considered and rejected（写进计划以免重审）

- **只在 TUI 面板层过滤空行**：否决——web/频道与 TUI 不一致，且会掏空分页页。
- **"可见消息 OR 有 session event"**：否决——会留下 11 条只有报告/报错卡的空会话继续显示 "New conversation"（截图里最新一条正是它），
  并会隐藏 fully-rewound 的会话；且 `TestWithdrawUserTurnReleasesSessionTitle` 明确要求"只有撤回消息的会话仍在列表里"。
- **删除空会话行 / 加自动清理**：超出 owner 的诉求（"过滤掉"），有数据破坏风险，不做。
- **改 `frontend/src/**` 应用代码**：不需要——web 抽屉与项目页列表的服务端语义变了，前端字段与结构不变。
  要动的只有 `frontend/e2e/project-space.spec.ts` 这条断言（Step 7）。

## 评审意见处理（v2，2026-10-16）

外部评审（glm-5.3）对本计划提了三条，逐条核实后处理如下：

1. **e2e `project-space.spec.ts` 钉住了被作废的行为 —— 属实，已纳入 Scope。**
   我已亲自核对 `frontend/e2e/project-space.spec.ts:83-96`：该用例点"新建会话"后**不发消息**就回项目会话页，
   断言 `未命名会话|Untitled session` 按钮可见。新谓词下这条必挂。已改写 Scope、新增 Step 7、加入 Done criteria。
   一处事实更正：评审说该失败会"在 CI 或 owner 手工验收时暴露"——实际 **CI 不跑 Playwright**
   （`.github/workflows/*.yml` 无 e2e 步骤），唯一入口是 owner 手工跑的 `scripts/acceptance/web_e2e.sh`，
   所以计划必须显式把它列为验证步骤（Step 7 / Done criteria），否则没有任何自动门会发现。
2. **Commands 表的 `-run '…\|…'` 是假绿门 —— 属实，已改成裸 `|`**（Step 5 的命令本来就是对的，现在两处一致）
   并在表里写明 `-run` 收 Go 正则、`\|` 会匹配不到任何用例而 exit 0。
3. **包级回归门漏 `./pkg/process` —— 属实，已加入 Step 4/Step 6 的命令与 Done criteria**，并把"为什么它仍通过"
   写进 Step 4（`RunSubagentSupervised` 先把任务写进 worker 会话，见 `pkg/process/worker_cli.go:88`，
   所以 `worker_cli_test.go:594` 列出的两个 worker 会话都有消息行）。
4. 评审建议的 `useChatSessions.createSession` 无调用方，已写进 Maintenance notes；评审补充的
   "`TestWithdrawUserTurnReleasesSessionTitle` 是'不看 visibility'的硬约束"，已作为第 3 条理由写进判定语义一节。

## Implementation Status

**已落地；测试与真机验收全绿（2026-10-16）。基线 `fa4af8a`，实际分支 `dev`（计划写 `main`，同一 commit，无影响）。未做任何 git 操作
（无 worktree、无 stash、无 commit），改动全部留在工作区。**

### 落地内容

- `pkg/state/session_store.go` — 唯一新语义 `conversationHoldsAMessage`（1 处定义 + 4 处查询 + 5 处文档注释），
  四个列表查询 `ListSessionsRecent` / `ListSessionsRecentPaged` / `ListSessionsForProject` / `ListSessionsOfSource`
  各加 `AND `+谓词（参数顺序未变、无新参数）；四个函数的文档注释同步。
- `pkg/tui/chat_slash.go:989-995` — `persistMigrationReport` 注释同步（**只改注释**，逻辑未动）。
- 既有用例补前置消息（断言一字未改）：`pkg/state/session_store_test.go`（分页 45 条循环、`ordinary`、`talk`）、
  `pkg/state/project_store_test.go`（`sess-p*`）、`pkg/state/schema_migrations_test.go`（`sid`）、
  `pkg/tui/chat_session_test.go`（`cur`/`oth`）、`pkg/gateway/skills_download_test.go`（workshop 会话，先从响应体解 id）、
  `pkg/gateway/api_extra_test.go`（项目会话）。
- 新增回归测试：`TestConversationListsHideSessionsWithoutAMessage`（`pkg/state/session_store_test.go`，3 个子用例：四个列表器都隐藏
  0 消息会话 / 分页 `limit=1` 不被空行掏空 / 另一租户为空；新增 `slices` 导入）、
  `TestResumeListsOnlyConversationsWithMessages`（`pkg/tui/chat_session_test.go`）。
- `frontend/e2e/project-space.spec.ts:76-102` — 按新语义改写被作废的断言（空会话不列出 + 发首条消息后按标题列出），抽屉断言不变。
- 本文件落盘到 `docs/plan/EMPTY_SESSION_FILTER_PLAN.md`。

### 与计划的偏差

1. **Step 4 表漏列一处**：`pkg/gateway/api_extra_test.go:915` `TestChatSessionsAreFilteredByPurpose` 也用裸 `Ensure` 造
   `talk`/`task` 并断言它们出现在列表里。该用例自己的注释写明它钉的是"purpose 过滤在查询里"，空行只是顺手造的数据，与表内 8 处同类
   → 用**同一处方**处理（给 fixture 补 `Append`，断言一字未改），未放宽过滤、未删断言。与 STOP condition 2 的区别：那条针对
   "把空会话可见当作契约"的断言（唯一一处即 `project-space.spec.ts`，已按 Step 7 改写）；此处只是 fixture 数据。
2. **Step 8 场景 3 的预期与实际不符**：在"只有 `agents:` 段、没有 `gateway:` 段"的隔离 home 下，`forebrain gateway start </dev/null`
   **不会**打印诊断后退出，而是照常起服务并监听 6060（前台常驻）。按"非 TTY 行为不变"的原意改为**新旧二进制对照**：
   同一下两枚二进制行为完全一致，都打印 `Listening on http://127.0.0.1:6060`。故非回归，是计划对该场景的预期写错。
3. `grep -c conversationHoldsAMessage` 实际为 **10**（≥5 成立）。
4. `sess-p2`（属于另一个项目）也补了消息（计划说"可加可不加"），使三个 fixture 一致。

### 未做（计划内明确不做）

不删任何行、不加清理任务、不加列/索引/迁移；`frontend/src/**` 未动；`ListChildSessionsRecent`、`SessionIDWithLatestMessage`、
`cronRunSelect` 未触碰。

### 验证（全部实跑）

| 检查 | 命令 | 结果 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 格式 | `gofmt -l` 全部改动文件 | 无输出 |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tui ./pkg/gateway ./pkg/turn ./pkg/migrate ./pkg/process -count=1` | 全 PASS |
| vet | `go vet ./...` | exit 0 |
| 全量（隔离 home） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全 PASS（无 FAIL） |
| 新用例 | `-run 'TestConversationListsHideSessionsWithoutAMessage\|TestResumeListsOnlyConversationsWithMessages' -v` | 3+1 子用例 PASS |
| web e2e | `FOREBRAIN_E2E_SPEC=e2e/project-space.spec.ts scripts/acceptance/web_e2e.sh` | 7 passed，`web e2e: PASS` |

**证伪（证明新用例不是同义反复）**：把谓词临时改成 `1=1` 后——① 两个新 Go 用例失败
（`ListSessionsRecent listed "empty"…`、`paged ids = [s-0…s-7], want [s-1 s-4 s-6]`、TUI 的 `NotContains` 失败）；
② `project-space.spec.ts:89` 的 `toHaveCount(0)` 失败（`unexpected value "1"`）。两次都随后还原谓词并复跑全绿。

### 真机验收（tmux + 隔离 home；两枚二进制对照）

环境：`/tmp/fbaccept-es/`（`bin/`、`home*/`、`proj/`）；`bin/forebrain` = 本修复，`bin/forebrain-prefix` = 仅把谓词换成 `1=1` 的
对照版本（事后源码已还原）；假 provider 跑在 tmux 里；一切在 `FOREBRAIN_HOME=/tmp/fbaccept-es/home*` 下，**未碰 `~/.forebrain`**。

- **场景 1（复现 → 修复）**：修复前 `/new` → `/exit` → 重启 → `/resume` 面板出现 `❯ just now New conversation`；
  修复后同一脚本得到 `● resume / There is no other conversation to resume.`。库内对照（`sqlite3 -readonly`）：
  prefix home 有一条 `msgs=0` 的行（空行仍在磁盘上，只是不再列出）；fixed home 有 `hello acceptance|msgs=5` 与 `<id>|msgs=0`
  两条，面板只列出前者。
- **场景 2（有内容的会话仍在、仍可 resume）**：发 `hello acceptance` → 假 provider 回 `ACCEPT_REPLY_OK`；`/exit` 重启 `/resume`
  → 只有一条 `❯ just now hello acceptance`；Enter 选中 → 重放并打印 `resumed 4 messages above; scroll up to review them.`
- **场景 3（非 TTY 回归）**：见偏差 2，新旧二进制一致，服务已 kill。
- 验收后清理：tmux 会话、假 provider 进程、临时目录全部移除。

### 已知限制 / 遗留

- 空壳行仍会在 `/new`、`/model`、`/migrate`、web 建会话端点处创建（"身份先落地、内容后到"），本次只改"列不列出"；因此历史遗留空行
  无需清理。
- 只有 `/migrate` 报告或启动报错卡、0 消息的会话不再出现在任何会话列表（刻意修订，见行为矩阵）：报告仍落盘，仍可在该会话内或
  `forebrain resume <id>` 复看。
- `frontend/src/composables/useChatSessions.ts` 的 `createSession` 目前无调用方；若将来接线，新建空会话在首条消息前不出现在抽屉里
  （本语义，不是 bug）。
