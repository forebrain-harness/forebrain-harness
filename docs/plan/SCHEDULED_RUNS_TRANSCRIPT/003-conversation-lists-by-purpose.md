# 计划 003：会话列表按用途在 SQL 里过滤，标题直接按 id 读取

> **执行者须知**：逐步执行。每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"里的任何情况，立即停止并报告。
> 完成后更新 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md` 里本计划的状态行。**不要提交代码。** 先读 README 的"全局规则"。
>
> **漂移检查（先运行）**：
> `git diff --stat 1a6d708..HEAD -- pkg/state/session_store.go pkg/turn/session.go pkg/turn/service.go pkg/turn/model.go pkg/turn/executor.go pkg/tui/tty_session.go pkg/tui/chat_session.go pkg/tui/notify.go pkg/tui/chat_surface.go pkg/gateway/api_extra.go frontend/src/lib/api.ts frontend/src/composables/useChatSessions.ts frontend/src/views/WorkshopView.vue frontend/src/views/ChatView.vue`
> 计划 001、002 会改 `pkg/turn/session.go`、`pkg/turn/service.go`、`pkg/gateway/api_extra.go`、`frontend/src/views/ChatView.vue`、`frontend/src/lib/api.ts`，这是预期的；只核对本计划"现状"里摘录的那几段。

## 状态

- **优先级**：P1（顺带发现的既有缺陷，按规矩纳入本期；计划 005 依赖它）
- **工作量**：M
- **风险**：LOW-MED（改变了几处列表的内容；终端 `/resume` 也受影响，见决策 D3）
- **依赖**：无（会话用途 `cron` 的取值由本计划定义，由计划 005 开始写入）
- **类别**：bug
- **基线**：提交 `1a6d708`，2026-10-03
- **决策**：D3 按推荐 A（owner 2026-10-03）

## 为什么要做

`fb_sessions.source` 记录"这个会话是干什么的"：`''` 是普通对话，`'workshop'` 是技能工坊任务；计划 005 会加上 `'cron'`，表示定时任务的一次触发。
今天所有列表都是"先取最近 N 个会话，再在内存里挑"：

- 对话抽屉：后端取最近 200 个，在 Go 里跳过项目会话；前端再把工坊任务过滤掉。
- 工坊任务列表：同一个接口取回来，前端只留 `'workshop'`。

只要别的会话一多，比如一个每小时触发的定时任务一天就新建 24 个会话，最近 200 个就被它们占满，抽屉里的普通对话和工坊任务就"消失"了。
另外有三处"给一个会话 id，找它的标题"的代码，是扫最近 100/200/500 个会话去找的。会话一多它们就找不到，退回显示 id。网页聊天页的标题也是从抽屉列表里找，所以项目会话打开后标题永远显示成"对话"。
根因是"按用途过滤"和"按 id 取标题"都没有落到 SQL 上。修复：列表在 SQL 里按用途过滤，标题直接按 id 查。

## 现状（基线 `1a6d708` 的事实）

### 存储层

`pkg/state/session_store.go`：

- `:383-391` `SessionSummary{ID, Title, UpdatedAt, ProjectID, Source}`，注释："Source is what the session is for: "" an ordinary conversation, "workshop" a skill-workshop task."
- `:429-462` `ListSessionsRecent(ctx, limit)`：`SELECT id, title, updated_at, source, COALESCE(project_id, '') FROM fb_sessions WHERE agent_id = ? ORDER BY updated_at DESC LIMIT ?`（limit 默认 80，上限 500）。
- `:466-503` `ListSessionsForProject(ctx, projectID, limit, offset)`：`... WHERE agent_id = ? AND project_id = ? ORDER BY updated_at DESC, id LIMIT ? OFFSET ?`。
- `:505-547` `ListSessionsRecentPaged(ctx, limit, offset)`：`... WHERE agent_id = ? ORDER BY updated_at DESC, id ASC LIMIT ? OFFSET ?`。
- `:312-330` `SessionTitle(ctx, id)`：直接按 id 查，未命名（标题等于 id）或不存在都返回 `""`。
- 没有任何会话用途常量。`"workshop"` 只以字面量出现在 `pkg/gateway/api_extra.go:968`。

### gateway

- `pkg/gateway/api_extra.go:1049-1090` `handleChatSessions`：`s.Core.ListSessionsRecent(r.Context(), 200)`（`Core` 为 nil 时用 `s.Sessions`），循环里 `if strings.TrimSpace(sum.ProjectID) != "" { continue }`，不读任何 query 参数。
- `:1096-1116` `handleChatSessionProject`（路由 `chatSessions.Get("/:id/project", ...)`，`:116`）：返回 `{"project": {id, name} | null}`，不检查会话归属。
- `:1121-1126` `sessionDisplayTitle(sum)`：标题等于 id 时返回 `""`，由客户端画占位文字。
- `pkg/turn/session.go:79-84` `Service.ListSessionsRecent` 只被上面的 `handleChatSessions` 调用（`grep -rn "Core.ListSessionsRecent" pkg` 核实）。

### 列出会话的 `/resume` 选择器

- 网页 `/resume`：`pkg/turn/executor.go:302-325` 的 `execResume` 调 `ctx.Sessions.ListSessionsRecent(ctx.commandContext(), 51)`。
- 终端 `/resume`：`pkg/tui/run.go:4320-4335`，优先 `ListSessionsRecentPaged`，否则 `ListSessionsRecent`。

### 扫列表找标题的三处

- `pkg/tui/tty_session.go:413-437` `lookupSessionTitle`：`session.ListSessionsRecent(ctx, 100)` 后逐行比对，未命名返回 `""`。它的 `session` 参数类型是 `pkg/tui/notify.go:1288` 所在的那个 `Session` 接口。
- `pkg/tui/chat_session.go:278-287`：`s.sessStore().ListSessionsRecent(ctx, 500)` 找到后作为 `Ensure` 的标题。
- `pkg/turn/executor.go:938-952` `currentSessionTitle`：`ctx.Sessions.ListSessionsRecent(context.Background(), 200)` 找到后返回 `sessionSummaryTitle(sum)`（`:1078-1084`，未命名返回 `"New conversation"`），找不到就返回 id。`ctx.Sessions` 的类型是 `pkg/turn/model.go:366-378` 的 `SessionStore` 接口。

### 网页

- `frontend/src/lib/api.ts:1086-1090`：`chatSessions(current = 1, size = 100)`，发 `GET /chat/sessions?current&size`（后端忽略这两个参数）。
- `frontend/src/composables/useChatSessions.ts:23-44`：`chatSessions(1, 100)`，然后 `.filter((r) => (r.source ?? '') !== 'workshop')`。
- `frontend/src/views/WorkshopView.vue:196-197`：`chatSessions()`，然后 `.filter((row) => row.source === 'workshop')`。
- `frontend/src/views/ChatView.vue:644-650`：

  ```ts
  /** The conversation's name, from the shared list the drawer shows. */
  const sessionTitle = computed(() => {
    const sid = sessionId.value
    if (!sid) return t('nav.chat')
    const row = sessions.value.find((item) => item.id === sid)
    return row?.title || t('nav.chat')
  })
  ```

  `:628-642` 的 `sessionProject` / `loadSessionProject(sid)` 调 `forebrainApi.chatSessionProject(sid)`（`api.ts:1120-1124`）；`:1466-1467` 在 `sessionId` 变化时调用它；模板 `:19-23` 用 `sessionProject` 画项目标签。
- `frontend/src/components/ChatDrawer.vue:129-138`：抽屉打开时 `fetchSessions()`；`ChatView.vue:1428`（一个回合结束时）和 `:1446`（挂载时）也会调用。

## 设计

### 1. 存储层：用途常量与按用途过滤

`pkg/state/session_store.go`：

```go
// What a session is for, stored in fb_sessions.source. It is a different
// fact from memory.SessionSource* (which surface wrote the session, in
// fb_sessions.memory_source).
const (
	SessionSourceConversation = ""         // an ordinary conversation
	SessionSourceWorkshop     = "workshop" // a skill-workshop task
	SessionSourceCron         = "cron"     // one fire of a scheduled task
)
```

- `ListSessionsRecent`、`ListSessionsRecentPaged`：在 `WHERE` 里加 `AND source <> 'cron'`（用参数绑定 `SessionSourceCron`，不写字面量）。更新注释：这两个是"人要恢复的会话"——除了定时任务的触发以外都在；定时任务的触发从它所属任务的执行记录里进入（决策 D3）。
- `ListSessionsForProject`：加 `AND source = ''`（绑定 `SessionSourceConversation`）。注释：项目的对话列表只列对话，项目里定时任务的触发从项目的定时任务标签进入。
- 新增：

  ```go
  // ListSessionsOfSource returns the agent's own sessions — not bound to a
  // project — that are for one purpose, newest first. The filter is in the
  // query, so sessions of another purpose never take a row of the limit.
  func (s *SessionStore) ListSessionsOfSource(ctx context.Context, source string, limit int) ([]SessionSummary, error)
  ```

  SQL：`SELECT id, title, updated_at, source FROM fb_sessions WHERE agent_id = ? AND project_id IS NULL AND source = ? ORDER BY updated_at DESC, id ASC LIMIT ?`。limit 的默认值和上限沿用 `ListSessionsRecent`。
- `SessionSummary` 的 `Source` 注释改为引用这三个常量。
- `pkg/gateway/api_extra.go:968` 的白名单改用 `state.SessionSourceConversation`、`state.SessionSourceWorkshop`。

### 2. gateway：抽屉和工坊各取各的

- `handleChatSessions`：读 query `source`，缺省是 `''`；只接受 `''` 和 `'workshop'`，其它值返回 400，文案 `source must be empty or workshop`（和创建接口一致）。直接调 `s.Sessions.ListSessionsOfSource(r.Context(), source, 200)`，删掉 Go 里跳过项目会话的循环条件。`s.Sessions == nil` 时返回空列表（这是今天 `Core` 和 `Sessions` 都为 nil 时的行为）。
- 删除不再有调用方的 `turn.Service.ListSessionsRecent`（`pkg/turn/session.go:79-84`）。如果 `SessionRepository.ListSessionsRecent` 也因此没有调用方，从接口里删掉它，并删掉测试替身里的对应实现。用 `deadcode` 和编译器确认。
- 新增"单个会话"接口，替换 `/:id/project`：

  ```go
  // handleChatSession names one conversation for the page that has it open:
  // its title and the project it belongs to. The page reads this rather than
  // looking itself up in the drawer's list, which lists only the agent's own
  // conversations — a project's session, or a scheduled task's fire, is never
  // in it.
  func (s *Server) handleChatSession(w http.ResponseWriter, r *http.Request)
  ```

  - 路由：`chatSessions.Get("/:id", s.handleChatSession)`；删除 `chatSessions.Get("/:id/project", ...)` 和 `handleChatSessionProject`。
  - 先 `s.sessionOwned`；不属于本主代理返回 404。
  - 响应：`{"id": sid, "title": <SessionTitle 的结果，未命名为 "">, "project": {id, name} | null}`。项目部分照搬 `handleChatSessionProject` 的逻辑（`s.projectStore()` 加 `ProjectForSession`）。

### 3. 标题直接按 id 读

- `pkg/turn/model.go` 的 `SessionStore` 接口加 `SessionTitle(ctx context.Context, id string) (string, error)`。`currentSessionTitle` 改为：

  ```go
  	title, err := ctx.Sessions.SessionTitle(context.Background(), sessionID)
  	switch {
  	case err != nil:
  		return strings.TrimSpace(sessionID)
  	case title == "":
  		return "New conversation"
  	default:
  		return title
  	}
  ```

  `ctx.Sessions == nil` 的早返回保持不变。
- `pkg/tui/notify.go` 的 `Session` 接口加 `SessionTitle(ctx context.Context, id string) (string, error)`，`ChatSession` 实现为转调 `s.sessStore().SessionTitle`（`sessStore()` 为 nil 时返回 `"", nil`，写法和同文件 `ListSessionsRecent` 的实现一致）。`lookupSessionTitle` 改为直接调用它（出错返回 `""`）。
- `pkg/tui/chat_session.go:278-287`：改为 `if t, err := s.sessStore().SessionTitle(ctx, sid); err == nil && t != "" { title = t }`。
- 改完后，如果 `Session` 接口里的 `ListSessionsRecent` 只剩 `/resume` 的回退路径在用，就保留它；否则按 `deadcode` 的结果删。

### 4. 网页

- `frontend/src/lib/api.ts`：`chatSessions(source: '' | 'workshop' = '')`，发 `GET /chat/sessions?source=...`。新增 `chatSession(sessionId: string)`，返回 `{ id: string; title: string; project: { id: string; name: string } | null }`；删除 `chatSessionProject`。
- `useChatSessions.ts`：改调 `chatSessions('')`，删掉 `.filter`。
- `WorkshopView.vue:196-197`：改调 `chatSessions('workshop')`，删掉 `.filter`。
- `ChatView.vue`：
  - 用 `sessionInfo = ref<{ title: string; project: { id: string; name: string } | null } | null>(null)` 和 `loadSessionInfo(sid)` 替换 `sessionProject` 和 `loadSessionProject`（请求计数防乱序的写法照旧）。
  - `sessionTitle` 改为 `sessionInfo.value?.title || t('nav.chat')`，注释改成"从会话自身读取"。
  - 模板里的项目标签改用 `sessionInfo?.project`。
  - 触发时机：`sessionId` 变化时（替换 `:1466-1467`），以及 `watch(sessions, ...)`：共享列表每次刷新（回合结束、抽屉打开、改名）就重读当前会话。这样不必在每个刷新点各调一次。
  - 不再从 `useChatSessions()` 解构 `sessions` 用于标题；如果 `sessions` 只剩这个 watch 在用，就保留它作为刷新信号。

## 缓存影响

无。只改列表查询和标题查询，不涉及请求内容。

## 需要的命令

见 README"常用命令"。

## 范围

**要改的文件**：

- `pkg/state/session_store.go`、`pkg/state/session_store_test.go`
- `pkg/turn/session.go`、`pkg/turn/service.go`、`pkg/turn/model.go`、`pkg/turn/executor.go`、`pkg/turn/executor_test.go`、`pkg/turn/session_test.go`（以及编译器指出的测试替身）
- `pkg/tui/notify.go`、`pkg/tui/chat_surface.go`、`pkg/tui/tty_session.go`、`pkg/tui/chat_session.go` 及其对应的 `_test.go`（以及编译器指出的 `Session` 接口测试替身）
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`，以及 `pkg/gateway/session_context_test.go`、`pkg/gateway/skills_download_test.go` 里调用 `handleChatSessions` 的测试（按需改断言）
- `frontend/src/lib/api.ts`、`frontend/src/composables/useChatSessions.ts`、`frontend/src/views/WorkshopView.vue`、`frontend/src/views/ChatView.vue`，以及它们的 `.test.ts` 里对 `chatSessions` / `chatSessionProject` 的 mock
- `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/README.md`（状态行）

**不要碰**：

- `fb_sessions` 的表结构。本计划不需要迁移：`source` 列在 v4 就有了。
- `SessionTitle` 自身的语义（未命名返回 `""`）。
- 子会话列表 `ListChildSessionsRecent`。
- 计划 005 的任何内容。本计划不写入 `cron`，只定义它并按它过滤。

## 步骤

### 第 1 步：存储层

按设计 §1 改。测试写进 `pkg/state/session_store_test.go`：

1. `TestConversationListsAreFilteredInTheQuery`：建 1 个普通会话（最早），然后建 250 个 `source='cron'` 的会话和 250 个绑定项目的会话（`SetSessionSource`，以及 `UPDATE fb_sessions SET project_id=?`，或者用 `ProjectStore.BindSession`）。`ListSessionsOfSource(ctx, "", 200)` 返回那个普通会话；`ListSessionsRecent(ctx, 500)` 和 `ListSessionsRecentPaged(ctx, 500, 0)` 里没有任何 `cron` 会话。
2. `TestProjectSessionListListsOnlyConversations`：一个项目下有 1 个普通会话和 1 个 `cron` 会话时，`ListSessionsForProject` 只返回普通会话。
3. `TestWorkshopListIsItsOwn`：`ListSessionsOfSource(ctx, "workshop", 200)` 只返回工坊会话。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -count=1` → `ok`。

### 第 2 步：标题直接读

按设计 §3 改 `pkg/turn`、`pkg/tui`。测试：

- `pkg/turn/executor_test.go`：`currentSessionTitle` 对已命名会话返回标题，对未命名会话返回 `"New conversation"`。造一个"存在但不在最近 200 条里"的会话：插入 300 个更新的会话后，仍能拿到它的标题（这就是本计划修的缺陷）。
- `pkg/tui`：给 `lookupSessionTitle` 补同样的"不在最近 N 条里"的用例，放进覆盖 `tty_session.go` 的测试文件。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ ./pkg/tui/ -count=1` → `ok`；`grep -n "ListSessionsRecent" pkg/turn/executor.go pkg/tui/tty_session.go pkg/tui/chat_session.go` → 只剩 `execResume` 里那一处。

### 第 3 步：gateway

按设计 §2 改。测试写进 `pkg/gateway/api_extra_test.go`（参照 `:745` 和 `:1733` 附近现有的 `handleChatSessions` 测试）：

- 缺省 `source` 只返回普通对话（不含工坊、`cron`、项目会话）；`?source=workshop` 只返回工坊任务；`?source=cron` 返回 400。
- 挤占回归：250 个 `cron` 会话之后，仍能列出更早的普通对话。
- `handleChatSession`：本租户会话返回标题和项目；未命名会话 `title` 为 `""`；别的租户的会话返回 404。把 `:755` 附近原来测 `handleChatSessionProject` 的用例迁移过来。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`；`grep -rn "handleChatSessionProject\|Core.ListSessionsRecent" pkg --include='*.go'` → 无输出。

### 第 4 步：网页

按设计 §4 改。更新所有 mock 了 `chatSessions` / `chatSessionProject` 的前端测试（`grep -rln "chatSessions\|chatSessionProject" frontend/src`），并加：

- `useChatSessions`：`chatSessions` 被以 `''` 调用；返回的记录原样进入列表，前端不再过滤。
- `ChatView`（如果已有挂载测试，就在那里加；没有就测一个从 `ChatView.vue` 抽出来的纯函数）：标题来自 `chatSession(sid).title`，空标题显示 `nav.chat` 的文案。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` 退出码 0。

### 第 5 步：真机验证

1. `scripts/acceptance/web_e2e.sh` → `web e2e: PASS`（`layout.spec.ts`、`skill-workshop.spec.ts`、`project-space.spec.ts` 覆盖了抽屉、工坊、项目会话）。
2. 在 `frontend/e2e/project-space.spec.ts` 里加一条：在项目里新建会话并打开后，聊天页标题栏显示的是会话标题（发完第一条消息、标题自动命名之后），而不是"对话"/"Chat"。
3. 终端：tmux 里打开 TUI，用 `sqlite3` 往隔离的 `FOREBRAIN_HOME` 库里插一个 `source='cron'` 的会话，然后 `/resume`，截屏确认它不在列表里，普通会话在。

**验证**：e2e PASS；截屏符合预期。把截屏要点写进"执行记录"。

## 完成标准（全部满足）

- [ ] README 常用命令里的每一条都达到成功标志
- [ ] `grep -rn "filter((r) => (r.source\|filter((row) => row.source" frontend/src` 无输出
- [ ] `grep -rn "handleChatSessionProject\|chatSessionProject\|Core.ListSessionsRecent" pkg frontend/src` 无输出
- [ ] 第 1–3 步列出的测试存在且通过
- [ ] `deadcode` 输出与基线一致
- [ ] `git status` 里只有"范围"列出的文件有改动
- [ ] README 状态行已更新

## STOP 条件

- "现状"里的摘录和实际代码对不上。
- `Service.ListSessionsRecent` 除了 `handleChatSessions` 之外还有别的生产调用方。
- 某个前端页面依赖抽屉列表里出现项目会话或工坊任务（e2e 失败且原因是这个）。

## 维护说明

- 以后加新的会话用途，就在 `SessionSource*` 常量组里加一个，并逐个决定每个列表要不要它：抽屉和工坊用 `ListSessionsOfSource`，`/resume` 用 `ListSessionsRecent*`，项目用 `ListSessionsForProject`。
- 任何"给 id 找会话某个属性"的需求都按 id 直接查，不要再扫列表。

## 执行记录

- 执行于 2026-10-05，紧随计划 002。Go 第 1–3 步 + 前端第 4 步全部落地；前端按计划第 4 步的"抽出纯函数"路径新建了 `frontend/src/composables/useSessionInfo.ts`（前端无 20 文件限制）。
- `./pkg/state/ ./pkg/turn/ ./pkg/tui/ ./pkg/gateway/` ok；全量 29 包 ok；前端 255 用例过、`vue-tsc` 0；`deadcode` 与基线仅一处行号漂移（同一符号 `pkg/tui/notify.go`，无新增死代码）。
- 三个 grep 检查全部无输出（`filter((r) => (r.source`、`chatSessionProject|handleChatSessionProject|Core.ListSessionsRecent`、`ListSessionsRecent` 仅剩 `execResume` 一处）。
- 终端真机：隔离库插入 `source='cron'` 会话与普通会话各一，TUI `/resume` 选择器列出普通会话与心跳会话、**不列 cron 会话**（tmux 截屏存证）。
- e2e：见计划 001 执行记录——断言修复后两轮假模型与智谱真模型全绿；本计划新增的项目会话标题用例（project-space.spec.ts）包含在内。
