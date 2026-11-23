# Plan 173：会话历史按 row-id 向后翻页（keyset，一次只读一页）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func (s \*Server) handleChatMessages" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "turns, err = s.Sessions.ListAllMessages(r.Context(), sid, 0)" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "func (s \*SessionStore) listStoredMessagesSince" pkg/state/session_store.go` 为 `1`；
> `grep -c "chatMessages(workerSessionId, 0)" frontend/src/composables/useChatStream.ts` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/gateway/api_extra.go pkg/state/session_store.go frontend/src/composables/useChatStream.ts frontend/src/lib/api.ts` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/state -count=1` 与
> `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts`。

## Status

- **Priority**: P2（每次打开/刷新会话都全量载入并下发整条历史）
- **Effort**: M（state 一条 keyset 查询 + 处理器重排 + 前端一次「加载更早」）
- **Risk**: LOW–MED（改的是网页读历史的契约；TUI 直读状态库，不受影响）
- **Depends on**: 无（`171` 减的是同一响应里的字节，本计划减的是行数；两者可任意顺序，同一执行者连续做时先跑一次前端基线）
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: perf（载荷 / 首屏）

## Why this matters

网页打开一个会话时，网关**先把整条会话全部读进内存，再全部下发**：

```go
	const historyLimit = 5000
	if s.Sessions != nil {
		turns, err = s.Sessions.ListAllMessages(r.Context(), sid, 0)   // 0 = 无 LIMIT
	}
	…
	if limit > 0 && len(visibleIndexes) > limit {
		visibleIndexes = visibleIndexes[len(visibleIndexes)-limit:]     // 只是"取最后 N 条"
	}
```

而网页请求的就是 `chatMessages(sessionId, 0)`——`limit` 的默认值就是 0（`frontend/src/lib/api.ts:2261`），
所以"截最后 N 条"这条路**从来没有生效过**：整个会话一定会被完整读、完整投影、完整下发。

代价随会话线性增长，而且是三份叠加：`fb_messages.content` + `parts`（工具卡片正文，上限 120 KB/条）+
网关从同一 body 再解析出的 `shell_card`。一个跑了几天、上百次工具调用的会话，一次打开就是数 MB JSON，
外加服务端把每行的 `parts` 全量 `json.Unmarshal` 一遍（`storedToolRowHeaderParts`）。

只读一页就能解决：`fb_messages.id` 是 `INTEGER PRIMARY KEY AUTOINCREMENT`，天然可做 keyset；
网页需要的"往上滚看更早"也正好是 keyset 的形态（**向后**翻，不是页码）。

## Current state

处理器的骨架（`pkg/gateway/api_extra.go:1963-2120`，节选）：

```go
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, parseErr := strconv.Atoi(raw); parseErr == nil && n >= 0 {
			limit = n
		}
	}
	if limit > 5000 {
		limit = 5000
	}
	…
	turns, err = s.Sessions.ListAllMessages(r.Context(), sid, 0)
	…
	compactionsBefore := map[int][]chatCompactionRow{}   // 按"下标"落位
	goalsBefore := map[int][]chatGoalRow{}
	…
	workedLines := turn.RunWorkedLines(turns)            // 按"下标"落位
	…
	emitCompactions := func(pos int) { if pos < firstVisible { return } … }
```

三个按**下标**落位的东西（compaction、goal、worked 行）是本计划唯一的技术难点：翻页之后，下标只在页内有意义。

好消息是它们都只依赖"页内那一行"：

- `turn.RunWorkedLines(turns)`（`pkg/turn/compaction.go:203-228`）只找**每个 run 的最后一行**，
  所以只要那一行在本页，行就画在正确位置；一个跨页的 run，其"Worked for"永远落在**较新的那一页**。
- `turn.CompactionPosition` / `turn.GoalPosition` 把事件换算成"插在第 i 行之前"，锚点是某一行的**下标**；
  翻页后必须改成"锚点行 id"，再按页内行 id 落位。

数据库侧现有的查询（`pkg/state/session_store.go`）：

```go
func (s *SessionStore) listStoredMessagesSince(ctx context.Context, q dbtx, sessionID string, sinceRowID int64, limit int) ([]storedMessage, error) {
	query := `SELECT ` + messageColumnsJoined + `
		FROM fb_messages m LEFT JOIN fb_runs r ON r.id = m.run_id
		WHERE m.session_id = ?
		  AND m.visibility = 'visible'`
	args := []any{sessionID}
```

（`listAllStoredMessages` = `listStoredMessagesSince(…, 0, limit)`；`listStoredMessagesAfterRowID` 是"往后"的那条，
本计划要的是"往前"，需要新增。）

索引：`idx_fb_messages_session_visibility ON fb_messages(session_id, visibility)`（`schema.sql:114`）；
`id` 是 rowid，索引条目在前缀相等时按 rowid 有序——**是否足够**由 Step 2 的 `EXPLAIN QUERY PLAN` 判定，不许假设。

网页侧两个调用点：

- `frontend/src/composables/useChatStream.ts:5905` `forebrainApi.chatMessages(target, 0)` —— 主会话（本计划的改造对象）；
- `frontend/src/composables/useChatStream.ts:3592` `forebrainApi.chatMessages(workerSessionId, 0)` —— **子代理 worker 会话**。
  它是另一个会话 id、另一份历史，**不在本计划范围**，且必须继续拿到全量（见下条契约）。

滚动位置已经有现成机制：`ChatView.vue:1681-1806` 的 browse state（`scrollTop` + `follow`
+ `el.scrollHeight - el.clientHeight - el.scrollTop <= 32`），"加载更早之后保持视口"可以复用它。

## 契约（本计划定下来的，后续计划照此）

| 请求 | 含义 |
|---|---|
| `GET …/messages`（无参数） | **不变**：整个会话（`limit` 缺省 0 = 全部）。子代理 worker 会话走这条 |
| `GET …/messages?limit=200` | **最新** 200 条（尾部一页） |
| `GET …/messages?limit=200&before=<row_id>` | 严格早于该 row id 的 200 条 |
| 响应 | **仍是数组**（不改成对象信封，避免破坏既有 API 消费者） |
| `has_more` | 客户端用 `page.length === limit` 推断（多取一次空页是可接受的代价） |

`limit` 上限 1000（今天的 5000 上限保留给"无参数"的全量路径不变）。

## Scope

**In scope（只改这些）**：
- `pkg/state/session_store.go`（新增 `ListMessagesBefore(ctx, sid, beforeRowID, limit)`，返回行 + 是否还有更早的）
- `pkg/gateway/api_extra.go`（`handleChatMessages`：`before` 分页路径 + 事件按**行 id** 落位）
- `pkg/gateway/api_extra_test.go`（新增 3 个测试）
- `frontend/src/lib/api.ts`（`chatMessages(sessionId, limit, before?)`）
- `frontend/src/composables/useChatStream.ts`（`loadMessages` 取一页；新增 `loadOlderMessages()`）
- `frontend/src/views/ChatView.vue`（滚到顶触发 `loadOlderMessages` + 视口锚定）
- `frontend/src/composables/useChatStream.test.ts`、`frontend/src/views/ChatView.test.ts`（各加 1 个测试）

**Out of scope（看着相关也不要动）**：
- **无参数**路径的全量语义与 `limit=0`（子代理 worker 会话、以及任何外部消费者靠它）
- 子代理历史那两个端点（`/subagent-history`、worker 会话的 `chatMessages(…, 0)`）
- `/events` 端点与 WS 实时帧（另一条链路）
- `pkg/state` 的 schema（**不许**为索引加迁移，见 Step 2 的 STOP）
- `171` 要删的 `Content` 字段（本计划不动 DTO 字段，只动行数）
- TUI（直读状态库）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| state 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run '^TestListMessagesBefore' -count=1 -v` | PASS |
| 网关定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^TestChatMessagesPagesBackwards$' -count=1 -v` | PASS |
| 两包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/state -count=1` | 与基线相比无新增 FAIL |
| 前端定向 | `cd frontend && pnpm vitest run src/views/ChatView.test.ts -t 'older'` | PASS |
| 前端回归 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts` | 与基线相比无新增 FAIL |
| 类型/构建 | `cd frontend && pnpm typecheck`；`cd frontend && pnpm build` | exit 0 |

## Steps

### Step 1：state —— 一条"往前翻"的查询

在 `ListAllMessages` 附近新增（复用 `messageColumnsJoined`、`requireOwned`、`messageFromStored`，
并在**同一次查询**里判断是否还有更早的可见行，别用 `LIMIT n+1` 再丢弃——那会多读一行正文）：

```go
// ListMessagesBefore returns up to limit visible rows strictly older than
// beforeRowID (0 = the newest page), in chronological order, plus whether an
// older visible row exists. Paging is by row id, not by offset: the transcript
// is append-only, so a row id is a stable cursor while an offset shifts under
// a session that is still being written.
func (s *SessionStore) ListMessagesBefore(ctx context.Context, sessionID string, beforeRowID int64, limit int) ([]Message, bool, error)
```

**Verify**：新增测试 `TestListMessagesBeforePagesWithoutGapsOrRepeats`（`pkg/state/session_store_test.go`）：
造 5 条可见行 + 1 条 `visibility='repaired'` 的行，翻两页（每页 2 条）→ 合起来正好 5 条、无重复、
顺序为时间正序、末页 `hasMore=false`，且被隐藏的行一次都不出现。

### Step 2（强制门）：确认索引能撑住倒序扫描

在一个**临时 home** 里建一个一万行的会话（测试里用 `OpenStateForTest`），跑：

```
EXPLAIN QUERY PLAN
SELECT … FROM fb_messages m LEFT JOIN fb_runs r ON r.id = m.run_id
WHERE m.session_id = ? AND m.visibility = 'visible' AND m.id < ?
ORDER BY m.id DESC LIMIT 200;
```

**判定**：计划里出现 `USE TEMP B-TREE FOR ORDER BY` → **STOP 3**（需要新增索引，而新增索引要一次 schema 迁移，
要先和 `145`/`146`/`149`/`151` 预留的 v18–v21 排好版本——那是评审方的决定，不是你的）。
不出现 → 继续（`idx_fb_messages_session_visibility` 的条目在前缀相等时按 rowid 有序，倒序扫描可直接命中）。

把这行 `EXPLAIN` 的输出贴进执行报告。

### Step 3：网关 —— `before` 分页 + 事件按行 id 落位

1. 解析：`before := int64(...)`；`before > 0` 或 `limit > 0` 时走分页路径（`limit` 缺省 200、上限 1000）；
   两者都没有时**保持今天的全量行为**（`ListAllMessages(…, 0)`）。
2. 分页路径用 `ListMessagesBefore` 一次取一页；`hasMore` 仅供服务端断言/日志，响应仍是数组。
3. **落位改成按行 id**：
   - compaction / goal：把 `compactionsBefore := map[int][]…` 改成 `map[int64][]…`，键是**锚点行的
     `fb_messages.id`**（`CompactionPosition`/`GoalPosition` 给出的是页内下标；用 `turns[pos-1].RowID`
     换算成锚点行 id——`state.Message` 的行 id 字段名就是 `RowID`（`session_store.go:643`），没有 `ID`
     字段——即"插在这一行**之后**"→ 改成"插在下一行之前"，页首即为该页第一行时也成立）；
   - 发 `msg` 的循环里，每行**之前**先吐锚点行 id 等于该行 id 的事件；
   - `workedLines := turn.RunWorkedLines(turns)` 的下标改为"该 run 最后一行在本页的页内下标"，
     语义不变（跨页的 run 的 Worked 行落在较新的那一页）。
4. 页首补齐：一个 compaction 事件若锚定在"本页第一行之前那一行"，它属于**较旧**的那一页；
   不要为了它多读一行（那会让每页都多一次查询）。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^TestChatMessagesPagesBackwards$' -count=1 -v`
→ PASS。该测试要覆盖三个边界（**每个都必须是断言，不是注释**）：

- a) 第一页 = 最新 N 条，`before` = 页内最旧行的 `row_id` → 第二页紧接其上，两页合起来**无重复、无缺口**；
- b) 一个 compaction 事件锚定在页边界那一行：只出现在**较新的那一页**的页首（或较旧那页的页尾），
   **不得两页都出现**；
- c) 一个 run 横跨页边界：它的 `worked` 行出现在**较新**那一页。

### Step 4：前端 —— 一次取一页 + 滚到顶加载更早

1. `frontend/src/lib/api.ts`：

```ts
  chatMessages(sessionId: string, limit = 0, before?: number) {
    return api
      .get<ChatMessageRecord[]>(`/chat/sessions/${sessionId}/messages`, {
        params: before && before > 0 ? { limit, before } : { limit },
      })
      .then((res) => res.data)
  },
```

2. `useChatStream.ts` 的 `loadMessages`：主会话改成 `chatMessages(target, 200)`；
   **`chatMessages(workerSessionId, 0)` 保持原样**（子代理 worker 会话仍取全量）；
   记下当前页最旧的 `row_id`（`Math.min(...rows.map(r => r.rowId || Infinity))`）。
3. 新增 `loadOlderMessages()`：`before=<最旧 row_id>` 取一页，把它们**插到 `messages` 前面**，
   并返回插入的行数（或旧的最旧 row id），供视图做视口锚定；没有更早的（返回 0 行）时置 `olderExhausted`，
   不再重复请求。
4. `ChatView.vue`：在聊天滚动容器的 `scroll` 上，`scrollTop <= 32 && !olderExhausted && !historyLoading`
   时调用 `loadOlderMessages()`；调用前记 `el.scrollHeight` 与 `el.scrollTop`，插入后
   `el.scrollTop = 新scrollHeight - 旧scrollHeight + 旧scrollTop`（复用 `:1681-1806` 那套 browse state 的字段，
   不要另建一套滚动状态）。

**Verify**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts -t 'older'` → PASS；
`cd frontend && pnpm typecheck` → exit 0。

### Step 5：前端测试（视口锚定）

在 `ChatView.test.ts` 新增：挂载一个有 2 条的会话，把滚动容器 `scrollTop` 设为 0 并派发 `scroll`，
让 `chatMessages` 的 mock 第二次返回 1 条更早的行 → 断言：消息列表变成 3 条、**最早的那条在最前**、
且 `scrollTop` 增加了"新增内容的高度"（用 mock 元素的 `scrollHeight` 属性模拟）。

**Verify**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts src/composables/useChatStream.test.ts`
→ 与基线相比无新增 FAIL；`cd frontend && pnpm build` → exit 0。

## Test plan

- `pkg/state`：`TestListMessagesBeforePagesWithoutGapsOrRepeats`（可见性过滤 + 无重复无缺口）。
- `pkg/gateway`：`TestChatMessagesPagesBackwards`（三边界，见 Step 3）。
- 前端：`ChatView.test.ts` 的视口锚定用例；`useChatStream.test.ts` 里加一条"主会话只请求一页、
  worker 会话仍请求全量"的断言（防止有人顺手把 `chatMessages(…, 0)` 一起改掉）。

## Done criteria

机器可检查，全部满足：

- [ ] `EXPECT QUERY PLAN` 的输出贴在报告里，且**没有** `USE TEMP B-TREE FOR ORDER BY`
- [ ] 三个 Go 新测试 PASS；两个包回归与基线相比无新增 FAIL
- [ ] 前端两个文件回归与基线相比无新增 FAIL；`pnpm typecheck`、`pnpm build` exit 0
- [ ] `grep -c "chatMessages(workerSessionId, 0)" frontend/src/composables/useChatStream.ts` 仍为 `1`（子代理没被改）
- [ ] `grep -c "ListAllMessages(r.Context(), sid, 0)" pkg/gateway/api_extra.go` 为 `1`（无参数路径仍在）
- [ ] `grep -c "func (s \*SessionStore) ListMessagesBefore" pkg/state/session_store.go` 为 `1`
- [ ] `git status --porcelain` 只显示 Scope 里的文件（外加 `pkg/gateway/dist/` 的构建产物）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. Step 2 的 `EXPLAIN QUERY PLAN` 出现 `USE TEMP B-TREE FOR ORDER BY`（需要索引 = 需要迁移 = 需要排版本），
   或出现 `SCAN fb_messages`（全表扫描）——两者都停下，把输出贴回来。
4. 你想把响应改成 `{messages, has_more}` 这类信封：**不要**（契约规定仍是数组）。
5. 你发现除网页之外还有消费者请求这个端点且依赖"全量"（`grep -rn "chat/sessions/.*/messages"` 全仓看一遍，
   包括 e2e 与脚本）：先报告，别改它们的预期。
6. 前端保存/恢复滚动位置的既有机制（`ChatView.vue:1681-1806`）无法在不新建状态的前提下做锚定：
   报告后停手，不要为翻页另建第二套滚动状态（那会让"刷新后回到原位"与"加载更早"互相打架）。

## Maintenance notes

- 游标是 `fb_messages.id`（rowid），不是时间戳：同一毫秒内写多行时时间戳会撞，rowid 不会。
- 会话是追加型的，所以"向后翻"（更早）永远不需要处理"新行插到已读页中间"——这正是选 keyset 而不是页码的原因。
- 谁将来加"跳到某条消息"（搜索结果定位），仍然用 `before` 一路取到目标行，不要引入 offset。
- 这份契约与 `171` 正交：`171` 减每行的字节，本计划减行数。两者都做完之后，一次打开的成本是
  `200 × (一行的展示形态)`，与整条会话长度无关。
