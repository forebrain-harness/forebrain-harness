# Plan 232: every write on PostgreSQL takes the write lock the design promises

> **Executor instructions**: Follow the steps in order; confirm each verification.
> If a STOP condition occurs, stop and report. When done, update this plan's row in
> `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/state/session_events.go pkg/state/action_service.go pkg/state/session_store.go pkg/state/dbx/tx.go pkg/architecture/dialect_coverage.go pkg/architecture/testdata/sqlite_sql_snapshot.json`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED — adding transactions to hot write paths can surface deadlocks that
  were previously impossible; the lock is already global so the added contention is
  bounded by the existing design.
- **Depends on**: none, but it shares files with three plans in this batch. Plan
  227 rewrites `pkg/state/dbx/tx.go`; plan 233 edits `pkg/state/session_store.go`;
  plan 228 edits `pkg/state/session_events.go` and `session_store.go`. Land 227
  before this plan; if 233/228 have already landed, re-anchor the excerpts instead
  of stopping, and keep their changes.
- **Category**: correctness
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 12 (`introduced` by plans 209–212)

## Why this matters

The multi-database design serialises **every** write behind one global lock so that
a reader never observes a gap: SQLite takes the write lock at `BEGIN`, MySQL a named
lock, PostgreSQL a transaction-scoped advisory lock (`dbx.BeginTx`). Event rows
carry a monotonically increasing `sequence` per session, and clients page by it —
so an out-of-order commit (101 visible before 100) makes a paging client advance its
cursor past 100 and never see that event.

On PostgreSQL a few writes bypass `dbx.BeginTx` and run straight on `*sql.DB`, which
takes **no** advisory lock. Two concurrent appends can then interleave: one commits
with a higher sequence while the lower one is still open, and a reader in that window
skips an event. This is exactly the failure the lock exists to prevent, and it is
PG-only, which is why it will not show up in a SQLite test run.

## Current state

`pkg/state/dbx/tx.go:144-163` — the promise: a standalone write on MySQL/PostgreSQL
takes the lock in a transaction of its own:

```go
// Exec runs one write outside a transaction. On MySQL and PostgreSQL it takes
// the global write lock in a short transaction of its own, because SQLite's
// immediate transactions already serialise a bare statement with every other
// writer and a single-dialect caller must not be able to slip between them.
func Exec(ctx context.Context, db *sql.DB, q string, args ...any) (sql.Result, error) {
```

`pkg/state/session_events.go:216-219` — the PG event insert bypasses it (the MySQL
arm at `:210-214` goes through `appendSessionEventMySQL`, which does take it):

```go
	stored, err := scanSessionEvent(s.DB.QueryRowContext(ctx, sessionEventInsertSQL.For(d), args...))
	if err == nil {
		return stored, true, nil
	}
```

`pkg/state/action_service.go:507-509` — the expire sweep likewise:

```go
	rows, err := s.DB.QueryContext(ctx, expirePendingActionsSQL.For(d),
		string(ActionExpired), reason, time.Now().Unix(), string(ActionPending), cutoff)
```

`pkg/state/session_store.go:932-955` — `Ensure`/`EnsureAt` call
`ensureSession(ctx, s.db, …)` on the bare handle.

`docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md` §2.3 — the rule this violates: every
standalone write must take the lock.

`pkg/architecture/dialect_coverage.go` — the existing source-level guard that every
statement has a dialect arm; this plan adds a second guard of the same kind (see
step 4).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Store | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/architecture -count=1` | `ok` |
| Postgres leg | `./scripts/test-db.sh up`, then the explicit `FOREBRAIN_TEST_DATABASE_URL` command in step 3 | `ok` |
| MySQL leg (no regression) | `./scripts/test-db.sh mysql` | `ok` |

## Scope

**In scope**:
- `pkg/state/session_events.go`
- `pkg/state/action_service.go`
- `pkg/state/session_store.go` (only the bare-handle write sites)
- `pkg/state/dbx/tx.go` (only if a helper is needed)
- `pkg/architecture/dialect_coverage.go` + its testdata (the new guard)
- the matching `_test.go` files

**Out of scope**:
- The lock's implementation, timeouts, and retry policy — no retry is added here;
  a busy error still surfaces, exactly as SQLite's `SQLITE_BUSY` does today.
- Read queries: reads do not take the lock by design.
- Any statement text: this plan changes *how* a statement is executed, not what it
  says. The SQLite SQL snapshot must not move.
- `pkg/memory`, `pkg/process`, `pkg/gateway` — if the new guard (step 4) flags them,
  report the sites and let the owner decide scope; do not fix them here.

## Steps

### Step 1 — find every bare write

Run these by hand and record the list (at `7d505aa3` they are the five real
sites):

```
grep -n "s\.DB\.QueryRowContext(ctx, sessionEventInsertSQL" pkg/state/session_events.go
grep -n "s\.DB\.QueryContext(ctx, expirePendingActionsSQL" pkg/state/action_service.go
grep -n "ensureSession(ctx, s\.db" pkg/state/session_store.go
grep -n "setMessageVisibility(ctx, s\.db" pkg/state/session_store.go
```

Expected: `session_events.go:217`, `action_service.go:507`,
`session_store.go:936`, `:950`, `:3086` (the last one in `WithdrawUserTurn`).
Every site must be classified as "write" before you touch it. The source text
contains no `INSERT`/`UPDATE`/`DELETE` literal — the statement comes from a
`dbx.SQL` constant — so a guard that greps for SQL keywords cannot see these
sites (step 4 gives the workable shape).

### Step 2 — wrap each write in `dbx.BeginTx`

For each write site: open `dbx.BeginTx(ctx, s.DB, nil)`, run the statement on the
transaction (`tx.QueryRowContext` / `tx.QueryContext`), scan, `tx.Commit()`, and
`defer tx.Rollback()` right after opening (the pattern at
`pkg/state/session_events.go:236-268`'s MySQL arm is the exemplar — read it and
match it, including the comment style). `ensureSession` and
`setMessageVisibility` already take a `dbtx` (`pkg/state/session_store.go:980`,
`:3229`), so pass the `tx` instead of `s.db` rather than duplicating their bodies.
The event `sequence` is an auto-increment column (`INTEGER PRIMARY KEY
AUTOINCREMENT` / `GENERATED BY DEFAULT AS IDENTITY` / `AUTO_INCREMENT`), not a Go
assignment: what matters is that the INSERT which allocates it runs inside the
locked transaction, which `BeginTx` gives you.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/architecture -count=1`
→ `ok` (`-tags fts5` is mandatory: `pkg/state` refuses to open without it, see
`requireSQLiteBuild`); SQLite behaviour unchanged (it was already serialised).

### Step 3 — prove the ordering on PostgreSQL

Add `TestPostgresEventSequenceNeverCommitsOutOfOrder` (build tag: none — it skips
without `FOREBRAIN_TEST_DATABASE_URL`; follow the existing skip idiom in
`pkg/state/*_test.go`): two goroutines append events for one session concurrently,
N times each, while a reader pages by `MAX(sequence)` as
`ListSessionEvents` does; assert no page ever moves its cursor past an event that
was not observed. Bound the run to a second or two.

**Verify**: `./scripts/test-db.sh up` then
`FOREBRAIN_TEST_DATABASE_URL='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable' FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run TestPostgresEventSequenceNeverCommitsOutOfOrder -count=1`
→ `ok`; reverting step 2 makes this test fail at least once in ten runs (do that
check once, record the outcome, then restore). `./scripts/test-db.sh postgres`
would **not** run it: that command is
`./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open'`
(`scripts/test-db.sh:52-58`) and the new test name matches none of those words.
If it does not fail even without the fix, say so plainly: the test is then only a
regression pin, not a reproduction.

### Step 4 — keep it from coming back

Add a `pkg/architecture` test (source text, the way `dialect_coverage.go` does it)
that fails on the exact shapes this plan removes. The statement constant or the
`s.db` argument is what identifies a write — the SQL text is not in the call:

```
.DB.QueryRowContext(ctx, sessionEventInsertSQL
.DB.QueryContext(ctx, expirePendingActionsSQL
ensureSession(ctx, s.db
setMessageVisibility(ctx, s.db
```

Each match reports its file:line. Keep an explicit allowlist for genuine
read-only call sites, each with a one-line reason, mirroring how
`dialect_coverage.go` handles exceptions. The guard is narrow on purpose: "any
write" needs type information a source scan does not have (a bare `s.db` is also
passed to reads).

**Verify**: name it `TestNoBareHandleWrites`, then
`go test ./pkg/architecture -run TestNoBareHandleWrites -count=1` → `ok` on the
fixed tree; introducing a fake bare write locally makes it fail (check once, then
remove the fake).

### Step 5 — both engine legs

**Verify**: `./scripts/test-db.sh mysql` and `./scripts/test-db.sh postgres` → both
`ok` (the dialect/state baseline — the new ordering test ran in step 3). If Docker
is unavailable, report both as unverified and say that the PostgreSQL half of this
finding is unproven.

## Test plan

- New: the PG ordering test (step 3), the architecture guard (step 4).
- Keep green: every `pkg/state` test on SQLite, plus both engine legs.
- Failure paths: a concurrent append pair; a busy lock (the statement fails rather
  than hanging — assert the error surfaces).

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/architecture -count=1` passes
- [ ] The PostgreSQL ordering test passes through the explicit command in step 3; `./scripts/test-db.sh mysql` and `./scripts/test-db.sh postgres` pass as the baseline, or both are reported unverified
- [ ] None of these four shapes is left: `grep -rn "s.DB.QueryRowContext(ctx, sessionEventInsertSQL\|s.DB.QueryContext(ctx, expirePendingActionsSQL\|ensureSession(ctx, s.db\|setMessageVisibility(ctx, s.db" pkg/state` returns nothing
- [ ] The new architecture guard fails on a deliberately introduced bare write (checked once, then reverted)
- [ ] `pkg/architecture/testdata/sqlite_sql_snapshot.json` is unchanged (`git status`)
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 232 updated

## STOP conditions

- The new guard flags sites in packages outside the scope (`pkg/memory`,
  `pkg/process`, `pkg/gateway`): report the list; do not expand the plan.
- Wrapping a site in a transaction changes an error a caller depends on (e.g. a
  test asserts `sql.ErrNoRows` from the bare path): report the site and the test.
- The PG ordering test cannot be made to fail without the fix and you cannot say
  why: report it as an unproven regression risk instead of claiming it reproduces.
- `dbx.BeginTx` on the event path deadlocks with a caller that already holds the
  lock (nested acquisition): stop and report. The mechanism is worth stating
  correctly — MySQL's `GET_LOCK` is reentrant **on one session**, and PostgreSQL's
  advisory lock is per transaction, but a nested `dbx.BeginTx` takes a *different*
  connection from the pool, so the second acquisition waits on the first until
  `LockTimeout`.

## Maintenance notes

- The rule is "a write goes through `dbx.BeginTx` or `dbx.Exec`". The guard is what
  makes that checkable; extend its allowlist only with a reason.
- A reviewer should check the sequence is assigned inside the transaction and that
  no statement text changed (the SQLite snapshot is the witness).
