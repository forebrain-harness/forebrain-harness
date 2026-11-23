# Plan 233: the same statement means the same thing on MySQL as on SQLite

> **Executor instructions**: Follow the steps in order; confirm each verification.
> If a STOP condition occurs, stop and report. When done, update this plan's row in
> `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/state/runrt.go pkg/state/session_store.go pkg/state/db.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none, but it shares `pkg/state/session_store.go` with plan 232.
  If 232 has already landed, the drift check reports that file; re-anchor the
  excerpts instead of stopping, and keep 232's transaction wrapping intact.
- **Category**: correctness
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 13 (`introduced` by plans 209–212)

## Why this matters

Two statements were written for SQLite's semantics and hand-translated for MySQL.
On MySQL they mean something else, and only on MySQL:

1. **A reaped run gets the wrong end time.** The reap `UPDATE` sets `updated_at=?`
   *before* the expressions that fall back to `updated_at*1000`. SQLite and
   PostgreSQL read the pre-update row for every `SET` expression; MySQL evaluates
   left to right and reads what it has already assigned. So on MySQL a run left
   behind by a process that exited cleanly is stamped as finished at **reap** time,
   and `worked_ms` is inflated by however long the reap took — which can be days.

2. **A title search containing `%`, `_` or `\` behaves differently.** The search
   builds `"%"+q+"%"` and passes it as a LIKE parameter. MySQL and PostgreSQL treat
   `\` as LIKE's default escape character; SQLite does not. Searching for a Windows
   path (`C:\Users`) therefore matches different rows on different engines, and a
   `_` in a title matches any character on all three.

## Current state

`pkg/state/runrt.go:696-706` — the MySQL reap arm, with `updated_at` first:

```go
	reapAbandonedRunUpdateMySQLSQL = dbx.SQL{
		MySQL: `
UPDATE fb_runs
SET status=?, updated_at=?,
    started_at_ms=COALESCE(started_at_ms, created_at*1000),
    finished_at_ms=COALESCE(finished_at_ms, ` + reapClockExprMySQL + `),
    worked_ms=COALESCE(worked_ms, ` + reapClockExprMySQL + `-created_at*1000)
WHERE id=? AND status=? AND owner=?`,
	}
```

`pkg/state/runrt.go:728-729` — the fallback the expressions use:

```go
	reapClockExprMySQL    = `GREATEST(COALESCE((SELECT o.heartbeat_at_ms FROM fb_run_owners o WHERE o.owner=fb_runs.owner), updated_at*1000), created_at*1000)`
	reapClockExprPostgres = `GREATEST(COALESCE((SELECT o.heartbeat_at_ms FROM fb_run_owners o WHERE o.owner=fb_runs.owner), updated_at*1000), created_at*1000)`
```

The comment at `pkg/state/runrt.go:1975-1982` states the assumption that breaks on
MySQL ("they read the pre-update row"). Confirm it there before editing.

`pkg/state/session_store.go:673-690` — the **channel-prefix** statement, whose
pattern is built from a hex project id (`"%-"+hex+"-%"`, `:1812`) and therefore
contains no user text and no LIKE metacharacter:

```sql
	 WHERE agent_id = ? AND project_id = ? AND id LIKE ?
```

Leave it alone. The user-controlled search is the title path: `titleLikeFor`
(`:749-757`), called from the two session-list queries at `:1692-1693` and
`:1888-1889`, where the value is wrapped as `"%" + q + "%"`. That is the only
place this plan escapes.

`pkg/state/db.go:143-151` — the collation comment that promises byte-exactness; the
LIKE issue is about the **escape character**, not the collation, and is fixed
independently.

`pkg/memory/jobs.go:324-337` — the precedent for fixing #1: a MySQL upsert whose
`SET` list is deliberately ordered because of left-to-right evaluation, with a
comment saying so.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'Reap|Search|List' -count=1` | `ok` |
| MySQL leg | `./scripts/test-db.sh up` then `FOREBRAIN_TEST_DATABASE_URL='mysql://root:fb@127.0.0.1:13306/fbt' FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestReapedRun|TestSearchBackslash' -count=1` | `ok` |
| Postgres leg | same with `postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable` | `ok` |

`./scripts/test-db.sh mysql` runs only `./pkg/state/dbx/... ./pkg/state/
-run 'Dialect|Schema|Open'` (`scripts/test-db.sh:52-58`); the two new test names
do not match it, so the script would print `ok` without running them.

## Scope

**In scope**:
- `pkg/state/runrt.go` (the MySQL reap arm and its comment)
- `pkg/state/session_store.go` (the LIKE call sites and their pattern builders)
- the matching `_test.go` files

**Out of scope**:
- The SQLite and PostgreSQL reap arms — they are correct; do not touch their text
  (the SQLite snapshot pins it).
- The collation itself (`utf8mb4_bin` PAD SPACE vs `utf8mb4_0900_bin`): changing a
  collation needs a migration and a decision; deferred, see maintenance notes.
- Column lengths (`TEXT` vs `LONGTEXT`) and any request-side length cap; a separate
  plan.
- Full-text search (`pkg/memory`) — it already quotes its terms.

## Steps

### Step 1 — reproduce both, on MySQL

With `./scripts/test-db.sh up` running:

- `TestReapedRunEndsWhenItStoppedNotWhenItWasReaped` — plant a run whose owner row
  is gone and whose `updated_at` is an hour old, reap it, assert `finished_at_ms`
  is within a few seconds of the old `updated_at*1000`, not of now; assert
  `worked_ms` is not inflated.
- `TestSearchBackslashMatchesLiterally` — store titles `C:\Users\x` and `CUsersx`,
  search `C:\Users`, assert exactly one row matches and it is the first.

**Verify**: both fail against MySQL today (skip when
`FOREBRAIN_TEST_DATABASE_URL` is unset, following the existing idiom). Record the
failure values. Expected red/green matrix on SQLite: the reap test passes; the
backslash case passes too (SQLite treats `\` literally); a `_` case fails on all
three engines. Say which engine each test asserts.

### Step 2 — order the MySQL `SET` list

Move `updated_at=?` to the **end** of the MySQL arm's `SET` list, so both clock
expressions read the pre-update `updated_at`. Add a comment naming the
left-to-right rule and pointing at `pkg/memory/jobs.go:324-337` as the precedent.
Change nothing in the SQLite/PostgreSQL arms.

The arguments do **not** move. A placeholder is numbered by its position in the
statement **text**, not by the SET item it belongs to, and none of the clock
expressions takes one — so `updated_at` is still the second `?` and the executor
keeps passing `(status, updated_at, id, status, owner)` as it does today
(`pkg/state/runrt.go:1994-1996`). Reordering the arguments to follow the SET list,
`(status, id, status, owner, updated_at)`, binds `now` to `id`: the update matches
no row, `RowsAffected()==0`, and the reap silently skips every run. This was
executed and caught on MySQL on 2026-11-05 — the failing symptom is
`reaped = [], want exactly the abandoned run: the update matched no row`.

**Verify**: the reap test passes on MySQL and SQLite; a successful reap still
reports `RowsAffected()==1` (assert it, so a silent no-op cannot pass).

### Step 3 — escape LIKE metacharacters

Add one helper next to the search (`escapeLikeArg(s string) string`) that escapes
`!`, `%` and `_` with `!`, and append ` ESCAPE '!'` to the `titleLikeFor`
expression on every dialect. `!` avoids the backslash-literal trap entirely: one
SQL literal cannot be shared across engines (`ESCAPE '\\'` is a single character
on MySQL and a hard error on PostgreSQL and SQLite, which require exactly one
character), while `ESCAPE '!'` is identical and valid on all three.

Put the clause where the LIKE expression is rendered (`titleLikeFor`, which already
switches per dialect) and escape the **value** before it is wrapped in `%`, not the
wrappers. Do **not** touch the channel-prefix statement
(`projectChannelSessionsSQL`): its SQLite text is pinned by
`pkg/architecture/testdata/sqlite_sql_snapshot.json`, and its pattern carries no
user text.

**Verify**: the search test passes on SQLite, MySQL and PostgreSQL (run all three
legs); a search for `100%` matches only a literal `100%` title; a search for
`a!b` is not confused by the escape character.

### Step 4 — note the invariant where it belongs

Extend the comment above `reapAbandonedRunUpdateMySQLSQL` (and, if it exists, the
one at `:1975-1982`) to say that the MySQL arm's order is load-bearing, so the next
translator does not "tidy" it back.

**Verify**: `grep -n "left to right" pkg/state/runrt.go` finds the note.

## Test plan

- New: the reap-timestamp test, the LIKE-escape test (`C:\Users`, `100%`, `a!b`).
- Structural pattern: existing `pkg/state` tests that skip without
  `FOREBRAIN_TEST_DATABASE_URL` and use `OpenStateForTest`.
- Failure paths: reap with a missing owner row (the designed path); a search
  containing `%`, `_`, `\`; a search containing the escape character itself; a
  search with none of them (unchanged results).

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` passes on SQLite
- [ ] The engine legs in "Commands you will need" pass (or both are reported unverified with the reason)
- [ ] `sed -n '/reapAbandonedRunUpdateMySQLSQL = dbx.SQL{/,/^}/p' pkg/state/runrt.go` shows `updated_at=?` as the **last** `SET` item, and the executor's argument list is **unchanged** (`status, updated_at, id, status, owner`)
- [ ] `grep -n "ESCAPE '!'" pkg/state/session_store.go` finds the clause in `titleLikeFor` (the channel-prefix statement is unchanged)
- [ ] `pkg/architecture/testdata/sqlite_sql_snapshot.json` is unchanged (`git status`)
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 233 updated

## STOP conditions

- The reap expressions turn out to read `updated_at` from a **subquery** that MySQL
  evaluates after the assignment regardless of order: report the experiment you ran
  instead of reordering blindly.
- Adding `ESCAPE` changes matching on SQLite for an existing test: report which
  test and what it expected — an existing assertion may have pinned the buggy
  behaviour.
- A LIKE site builds its pattern somewhere else (handler, gateway): the escape
  helper belongs where the pattern is built; report the site rather than escaping
  twice.
- MySQL is unavailable: do step 2 (order is provable by reading) and step 3, and
  say the MySQL/PostgreSQL legs are unverified.

## Maintenance notes

- MySQL's left-to-right `SET` evaluation is a general trap for this codebase; the
  comment added here is the second instance of the note (see `pkg/memory/jobs.go`).
- A reviewer should check that only the MySQL arm moved, and that every LIKE site
  escapes the value rather than the wrappers.
- Deliberately deferred: MySQL's `utf8mb4_bin` is PAD SPACE, so `'a.go' = 'a.go '`.
  **已定（advisor 建议，2026-11-04）：本期不修。** 影响面是两个仅在尾随空格上不同的
  路径/名称键相撞，实际几乎不存在（本仓文件工具也不会创建尾随空格的名字），而修它要
  新开 `utf8mb4_0900_bin` 的库 + 一条重写全部表的迁移，在共享库上要停机或在线 DDL，
  风险与收益不匹配。等一次真实撞键发生后按需再立计划；本条已记录，不必重新评估。
  同样延期：`TEXT` 列（API 接受 1 MiB、MySQL 上限 64 KiB）——请求侧长度上限应与 API
  校验一起做。
