# Plan 051: rewind 的可见性谓词限定在本会话——别的会话的事件不再被一起隐藏

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **本计划不在 2026-10-14 那次「schema 与迁移性能」审计的范围里**（它是一次正确性审计的副产物），但它的修复只有一处 SQL 文本 + 7 个调用点 + 一个回归测试，且顺带修掉「每次读事件都扫一遍 `fb_rewinds`」的性能问题。owner 可以照常执行或直接搁置。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/state/rewind.go pkg/state/session_events.go`
> 若这两个文件在 a8140fc 之后有改动，先把 "Current state" 的摘录与实码逐段对照；对不上按 STOP 处理。

## Status

- **Priority**: **P1**（正确性：一个会话的 rewind 会隐藏**所有**会话中 sequence 更大的事件）
- **Effort**: S（一处常量 + 7 个调用点 + 一个测试，约 60 行）
- **Risk**: LOW（修的是「外层的 session 引用没被限定」，修复后本会话的隐藏语义逐字不变）
- **Depends on**: none（只改 `pkg/state` 的读取路径，与 048/049/050 的迁移路径不重叠）
- **Category**: bug
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

`rewindHiddenEventsPredicateTemplate`（`pkg/state/rewind.go:356-371`）里的第一个占位符是「外层查询的 session_id 表达式」：

```go
const rewindHiddenEventsPredicateTemplate = `NOT EXISTS (
  SELECT 1 FROM fb_rewinds rw
  WHERE rw.session_id = %[1]s
    AND rw.hidden_event_seq_from > 0
    AND %[2]s >= rw.hidden_event_seq_from
    AND (rw.state = 'pending' OR ...)
)`
```

而 `RewindEventsPredicate` 的 7 个调用点（全部在 `pkg/state/session_events.go`）传的是**裸列名** `"session_id"` / `"sequence"`：

```go
// :143-144（SessionEventHighWater）
err := s.DB.QueryRowContext(ctx, `SELECT IFNULL(MAX(sequence), 0) FROM fb_session_events WHERE session_id=? AND `+
	RewindEventsPredicate("session_id", "sequence"), strings.TrimSpace(sessionID)).Scan(&high)
// :166-168（ListRunEvents）、:202-204、:236-238、:287-289、:350-352（ListSessionToolCalls）、:399-401（CountSessionToolCalls）同形
```

SQLite 的名字解析是**内层作用域优先**：子查询的 FROM 是 `fb_rewinds rw`，它有 `session_id` 列，于是 `rw.session_id = session_id` 解析成 `rw.session_id = rw.session_id`——一个恒真式。后果：

- **跨会话隐藏**：只要库里存在任意一条 `hidden_event_seq_from > 0` 的 rewind，**所有会话**中 `sequence >= 该阈值` 的事件都会被 `NOT EXISTS` 判掉。受害读取面包括 `ListSessionEvents`（分页读）、`SessionEventHighWater`（游标）、`ListRunEvents` / `ListRunEventsOfType`、`ListSessionEventsOfType`、`ListSessionToolCalls`、`CountSessionToolCalls`。固定（fixed）状态的 rewind 只在「后续没有新用户消息」时才把范围开到无穷，pending 状态的 rewind 则一直如此——也就是「另一个对话刚被 rewind、还没继续打字」的窗口内，别的对话历史看起来停在 rewind 那一刻。
- **顺带的性能问题**：恒真式让 `idx_fb_rewinds_session_state` 完全用不上，子查询对每个候选事件行都退化成对 `fb_rewinds` 的一次扫描。

修复后：`fb_rewinds` 的会话引用指向**外层**表，用上索引，且隐藏范围严格限于它自己的会话。

## Current state

### 谓词与它的工厂（`pkg/state/rewind.go:350-379`）

```go
// rewindHiddenEventsPredicateTemplate excludes the events one or more rewinds
// hide from a conversation. sessionExpr and seqExpr are the enclosing query's
// SQL expressions for an event's session_id and sequence columns. The range a
// pending rewind hides runs from its first hidden event to the end of the
// conversation; a fixed rewind's range ends at the first event of the first
// new user message that followed it.
const rewindHiddenEventsPredicateTemplate = `NOT EXISTS (...)`   // 见上文摘录

// RewindEventsPredicate returns the SQL predicate that excludes events hidden
// by a conversation's rewinds, for the enclosing query's session_id and
// sequence expressions. Every reader of fb_session_events composes it, so the
// rule lives in one place.
func RewindEventsPredicate(sessionExpr, seqExpr string) string {
	return fmt.Sprintf(rewindHiddenEventsPredicateTemplate, sessionExpr, seqExpr)
}
```

### 调用点（`pkg/state/session_events.go`，共 7 处）

`:144`、`:168`、`:204`、`:238`、`:289`、`:352`、`:401`，每处都是 `RewindEventsPredicate("session_id", "sequence")`，且它们所在查询的 FROM 一律是**未起别名的** `FROM fb_session_events`（`grep -n "FROM fb_session_events" pkg/state/session_events.go` 逐条核对过）。

### 既有测试（`pkg/state/rewind_test.go`）

`TestRewindMessagesAndEventsHideFromEveryReader`（`:54`）钉住**本会话**的隐藏语义：pending rewind 隐藏锚点起的消息与事件；`FixPendingRewinds` + 新的用户消息把固定范围收窄到新回合。它用的是现成夹具手法：`openSessionDB(t)`、`NewSessionStore(db, "main")`、`&RunStore{DB: db}`、`store.Ensure`、`insertRun`、`store.AppendMessageForRun`、`runs.AppendSessionEvent`、`store.InsertRewind`、`run` 侧的 `ListSessionEvents` / `SessionEventHighWater`。**这个测试必须原样保持绿色**（本计划不改本会话语义）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 本包测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` | `ok`（基线约 21s） |
| 新回归（修复前） | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestRewindHidesNothingInAnotherSession -v` | **FAIL**（见 Step 2） |
| rewind 相关 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run 'Rewind' -v` | 全 PASS |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...`、`gofmt -l pkg/state` | 无输出 |

## Scope

**In scope**（只改这三个文件）：
- `pkg/state/rewind.go`
- `pkg/state/session_events.go`
- `pkg/state/rewind_test.go`
- `plans/README.md`（只改 051 的状态行）

**Out of scope**：
- `pkg/state/schema.sql` / 迁移 / `fb_rewinds` 的表结构：本计划只改读取端的一条谓词。
- `FixPendingRewinds` / `RewindEventStart` / `RewindMessagesFrom` 的行为。
- 其它 `visibleToolCallPredicate` 一类的谓词（它们只引用外层列，没有这个陷阱）。
- 把谓词改成参数化绑定（`?`）：会让 `seqExpr >= rw.hidden_event_seq_from` 无法写成同一段文本，收益也只是「调用点更少一行」——本计划选择常量，见 Step 3。

## Git workflow

- 不建分支、不 commit、不 push；改动留在工作区。
- 若将来提交，建议 message：`fix(state): scope the rewind visibility predicate to its own session`。

## Steps

### Step 1: 取证（修复前的计划与失败）

```bash
cd /tmp && rm -f fx3.db && sqlite3 fx3.db < <repo>/pkg/state/schema.sql
sqlite3 fx3.db "EXPLAIN QUERY PLAN SELECT IFNULL(MAX(sequence), 0) FROM fb_session_events WHERE session_id=? AND NOT EXISTS (SELECT 1 FROM fb_rewinds rw WHERE rw.session_id = session_id AND rw.hidden_event_seq_from > 0 AND sequence >= rw.hidden_event_seq_from AND rw.state = 'pending');"
```
**记录输出**（预期含 `SCAN rw`——恒真式用不上 `idx_fb_rewinds_session_state`），贴进交付报告作为「修复前」。

### Step 2: 先写会失败的回归测试

在 `pkg/state/rewind_test.go` 末尾新增（夹具手法照 `TestRewindMessagesAndEventsHideFromEveryReader`）：

```go
// A rewind hides its own conversation's events, and only its own. Sequences
// are global, so a session that keeps writing after another session's rewind
// holds events past that rewind's hidden range; an unqualified session term
// inside the subquery would bind to fb_rewinds itself and hide them too.
func TestRewindHidesNothingInAnotherSession(t *testing.T) {
	ctx := context.Background()
	db := openSessionDB(t)
	store := NewSessionStore(db, "main")
	runs := &RunStore{DB: db}
	require.NoError(t, store.Ensure(ctx, "s-rwd", "s-rwd"))
	require.NoError(t, store.Ensure(ctx, "s-other", "s-other"))
	insertRun(t, db, "r-rwd", "s-rwd")
	insertRun(t, db, "r-other", "s-other")

	// Give r-rwd one event before asking where its hidden range starts:
	// RewindEventStart answers 0 for a run with no events, and a rewind whose
	// range starts at 0 is inert under both the buggy and the fixed predicate
	// — the fixture would pass before the fix and prove nothing.
	_, err = runs.AppendSessionEvent(ctx, SessionEvent{
		ID: "evt-rwd", SessionID: "s-rwd", RunID: "r-rwd", Type: "tool_call_completed",
		Payload: json.RawMessage(`{"tool_name":"read_file"}`),
	})
	require.NoError(t, err)

	// s-rwd rewinds to before its first user message (pending, never fixed).
	anchor, err := store.AppendMessageForRun(ctx, "s-rwd", "r-rwd", "user", "q", "", "", MessageExecTiming{})
	require.NoError(t, err)
	start, err := store.RewindEventStart(ctx, "s-rwd", anchor)
	require.NoError(t, err)
	require.Greater(t, start, int64(0), "a rewind with a zero range hides nothing, in any session")
	require.NoError(t, store.InsertRewind(ctx, RewindRecord{
		ID: "rw-cross", SessionID: "s-rwd", AnchorMessageID: anchor, Mode: "both",
		HiddenEventSeqFrom: start, State: "pending", CreatedAtMs: 1,
	}))

	// s-other writes after that, so its event's sequence is past the range.
	evt, err := runs.AppendSessionEvent(ctx, SessionEvent{
		ID: "evt-other", SessionID: "s-other", RunID: "r-other", Type: "tool_call_completed",
		Payload: json.RawMessage(`{"tool_name":"read_file"}`),
	})
	require.NoError(t, err)

	page, err := runs.ListSessionEvents(ctx, "s-other", 0, 0, 50)
	require.NoError(t, err)
	require.Len(t, page.Events, 1, "another conversation's rewind hid this session's events")
	require.Equal(t, evt.Sequence, page.Events[0].Sequence)

	high, err := runs.SessionEventHighWater(ctx, "s-other")
	require.NoError(t, err)
	require.Equal(t, evt.Sequence, high, "the high-water cursor must count this session's own events")

	byTool, total, err := runs.CountSessionToolCalls(ctx, "s-other")
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, 1, byTool["read_file"])
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run TestRewindHidesNothingInAnotherSession -v`
→ **FAIL**，失败信息是 `another conversation's rewind hid this session's events`（或 `page.Events` 为空）。
（若它**通过**：先检查 `require.Greater(t, start, int64(0))` 是否通过——第一轮执行时这里踩过坑：夹具没给 `r-rwd` 插事件时 `RewindEventStart` 返回 0，范围为零的 rewind 在修复前后都什么都不隐藏，测试就会假通过。断言已修好；若仍通过，说明谓词已不是 `rw.session_id = session_id` 的形状或读取路径被别处拦住了——按 STOP 报告。）

### Step 3: 谓词改成引用外层表的常量

**3a.** 在 `pkg/state/rewind.go` 里，把 `rewindHiddenEventsPredicateTemplate` 与 `RewindEventsPredicate` 一起换成下面这个常量（把 `%[1]s`/`%[2]s` 换成本仓库 `fb_session_events` 的两个列名，**带表名限定**）：

```go
// rewindVisibleEventsPredicate excludes the events a conversation's rewinds
// hide. Every reader of fb_session_events composes it against that table
// unaliased, and the session term carries the table name for a reason: inside
// the NOT EXISTS subquery the FROM list is fb_rewinds, which has a session_id
// of its own, and SQLite resolves an unqualified name in the innermost scope
// first. A bare `session_id` would compare the rewinds row with itself — every
// rewind would then hide events of every conversation, and
// idx_fb_rewinds_session_state could not answer the subquery. A constant, not
// a function of caller-supplied expressions, so there is nothing left to pass
// wrong; a caller that needs an alias has to revisit this.
const rewindVisibleEventsPredicate = `NOT EXISTS (
  SELECT 1 FROM fb_rewinds rw
  WHERE rw.session_id = fb_session_events.session_id
    AND rw.hidden_event_seq_from > 0
    AND fb_session_events.sequence >= rw.hidden_event_seq_from
    AND (rw.state = 'pending' OR fb_session_events.sequence < IFNULL((
          SELECT MIN(e2.sequence)
          FROM fb_session_events e2
          JOIN fb_messages m2 ON m2.run_id = e2.run_id AND m2.session_id = e2.session_id
          WHERE m2.session_id = rw.session_id
            AND m2.role = 'user'
            AND m2.id > rw.anchor_message_id
            AND m2.visibility = 'visible'
            AND TRIM(IFNULL(m2.run_id,'')) <> ''
        ), 9223372036854775807))
)`
```

（除表名限定外，谓词内容与今天的模板**逐字相同**：内外层括号、`IFNULL` 的哨兵值、`m2` 子查询的全部条件。）

**3b.** 把 `pkg/state/session_events.go` 的 7 处 `RewindEventsPredicate("session_id", "sequence")` 全部替换为 `rewindVisibleEventsPredicate`（`grep -c` 应为 0）。编译器会指出漏掉的调用点。

**Verify**:
```bash
grep -c "rewindVisibleEventsPredicate" pkg/state/session_events.go      # 7
grep -rn "RewindEventsPredicate" --include='*.go' pkg cmd | wc -l       # 0
grep -n "rw.session_id = fb_session_events.session_id" pkg/state/rewind.go   # 1 行
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run 'Rewind' -v      # 全 PASS（含新回归与既有 TestRewindMessagesAndEventsHideFromEveryReader）
```

### Step 4: 索引可用性的对照（性能那一半）

把 Step 1 的 `EXPLAIN QUERY PLAN` 换成修复后的谓词文本再跑一次（把 `rw.session_id = session_id` 改为 `rw.session_id = fb_session_events.session_id`，其余不动）。

**Verify**: 输出必须出现 `SEARCH rw USING INDEX idx_fb_rewinds_session_state (session_id=?)`，且不再有 `SCAN rw`。把前后两段输出都贴进交付报告。

## Test plan

- 新增：`TestRewindHidesNothingInAnotherSession`（跨会话隔离：分页读、high-water、工具调用计数三条读取面）。
- 既有：`TestRewindMessagesAndEventsHideFromEveryReader`（本会话隐藏语义）必须原样通过——它是「没有把隐藏改坏」的守卫。
- 验证：`CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`。

## Done criteria

ALL must hold:

- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state` → `ok`，且报告里有新回归**修复前**的失败输出
- [ ] `grep -rn "RewindEventsPredicate" --include='*.go' pkg cmd` → 无输出
- [ ] `grep -c "rewindVisibleEventsPredicate" pkg/state/session_events.go` → `7`
- [ ] `EXPLAIN QUERY PLAN` 出现 `SEARCH rw USING INDEX idx_fb_rewinds_session_state`（Step 4 的前后对照已贴进报告）
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/state -run Rewind -v` → 全 PASS
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...` 与 `gofmt -l pkg/state` 无输出
- [ ] `git status --short` 只多出 `pkg/state/rewind.go`、`pkg/state/session_events.go`、`pkg/state/rewind_test.go`、`plans/README.md`
- [ ] `plans/README.md` 中 051 的状态行更新

## STOP conditions

Stop and report back (do not improvise) if:

- Step 2 的回归测试**通过**（说明谓词已不是 `rw.session_id = session_id` 的形状，或读取路径被别处拦住了）。
- 任一调用点的查询给 `fb_session_events` 起了别名、或把谓词用在了 JOIN 的另一张表上——常量就不适用，停下报告该处。
- `EXPLAIN` 在修复后仍是 `SCAN rw`（说明外层限定没生效）。
- 既有 `TestRewindMessagesAndEventsHideFromEveryReader` 需要改断言才能通过——那意味着本会话语义被改动了。
- 需要改 `fb_rewinds` 的表结构或迁移版本号才能修复。

## Maintenance notes

- **不变式**：这条谓词**只能**贴到 `FROM fb_session_events`（未起别名）的查询上。新的读取方如果起别名，必须把谓词改成以别名为前缀的形式，并同步本计划的回归测试。
- **为什么用常量而不是函数**：参数化的 `sessionExpr` 正是这个 bug 的成因（名字写错不会被编译器发现，运行时静默变质）。若将来确有别名需求，把签名改成 `rewindVisibleEventsPredicateFor(alias string)` 并在函数体里拼接 `alias + ".session_id"`，且用一个表驱动测试把「裸列名」拒之门外。
- **审阅重点**：谓词正文除两处表名限定外与旧模板逐字一致（尤其 `IFNULL(..., 9223372036854775807)` 哨兵与 `m2` 子查询的条件）；7 个调用点全部替换；`FixPendingRewinds` 的行为未动。
- **副作用**：这条修复同时让每次事件读取不再对 `fb_rewinds` 做无索引子查询——是本计划对性能的附带贡献，但不要把它当作主要目的去改动谓词的结构。
