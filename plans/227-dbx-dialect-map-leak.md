# Plan 227: the dialect map stops growing once per write

> **Executor instructions**: Follow the steps in order. If a STOP condition
> occurs, stop and report. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/state/dbx/dbx.go pkg/state/dbx/tx.go pkg/state/dbx/dbx_test.go`
> Content mismatch is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: perf / bug
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 6 (`introduced` by plan 209)

## Why this matters

`DialectOf` has to answer for a `*sql.Conn` because the migration executor drives
one by hand. To do that, `dbx.Conn` records every connection it hands out in a
package-level `sync.Map`. Nothing ever deletes from it — and on MySQL **every
write** goes through it, because `dbx.Exec` opens a transaction with a dedicated
connection (`beginMySQL`), and every store write goes through `dbx.Exec`.

A gateway on MySQL therefore keeps one map entry per write forever: message rows,
session events, each lease heartbeat. At a few tens of writes per second this is
hundreds of megabytes a day of unreclaimable map entries, ending in an OOM
restart of the process that is holding the user's conversation.

## Current state

`pkg/state/dbx/dbx.go:133-160` — the two maps and the recording:

```go
var (
	dbDialects   sync.Map
	connDialects sync.Map
)

// Register records the dialect db speaks. ...
func Register(db *sql.DB, d Dialect) {
	if db == nil {
		return
	}
	dbDialects.Store(db, d)
}

// Conn is db.Conn with the dialect recorded, so DialectOf answers for the
// connection a caller drives by hand — the migration executor does. ...
func Conn(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	connDialects.Store(conn, DialectOf(db))
	return conn, nil
}
```

`pkg/state/dbx/dbx.go:163-181` — `DialectOf`'s three cases (`*Tx`, `*sql.DB`,
`*sql.Conn`); only the `*sql.Conn` case reads `connDialects`.

`pkg/state/dbx/tx.go:99-120` — the MySQL write path, which calls `Conn` for
**every** transaction:

```go
func beginMySQL(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (*Tx, error) {
	conn, err := Conn(ctx, db)
	if err != nil {
		return nil, err
	}
	// The lock release runs on its own context: a cancelled caller context
	// must not leave the named lock held on a connection nothing owns.
	if err := mysqlGetLock(ctx, conn, WriteLockName, LockTimeout); err != nil {
		_ = conn.Close()
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, opts)
	...
	return &Tx{Tx: tx, dialect: MySQL, release: func() error { ... }}, nil
}
```

`pkg/state/dbx/tx.go:144-163` — `dbx.Exec` routes through `BeginTx` on MySQL and
PostgreSQL, so every standalone write on MySQL takes this path.

Verified: `grep -rn "connDialects" pkg` shows exactly the declaration, the `Store`
in `Conn`, and the `Load` in `DialectOf` — no `Delete` anywhere.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/dbx -count=1` | `ok` |
| Store | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` | `ok` |
| MySQL leg | `./scripts/test-db.sh mysql` | `ok` |

## Scope

**In scope**:
- `pkg/state/dbx/dbx.go`
- `pkg/state/dbx/dbx_test.go`
- `pkg/state/dbx/conn_internal_test.go` (new; deleted again with the map)

**Out of scope**:
- `dbDialects` itself — one `*sql.DB` per `Open`; the process opens a handful.
- `DialectOf`'s signature and its callers (200+ sites).
- The write-lock design (`GET_LOCK`, the PG advisory lock) — this plan only
  changes how the connection's dialect is remembered.

## Steps

### Step 1 — reproduce with a test

Add `pkg/state/dbx/conn_internal_test.go` (`package dbx`, so it can see the
unexported map) with `TestConnRegistrationsGrowWithoutBound`: open a SQLite file,
call `Conn(ctx, db)` 20 times, close each conn, then count `connDialects` entries
with `Range`. Today the count is 20 — the map keeps every connection it ever saw,
including closed ones — and `beginMySQL` (`pkg/state/dbx/tx.go:99-104`) calls
`Conn` once per write transaction. That is the per-write growth this plan exists
for.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/dbx -run TestConnRegistrations -count=1`
→ **fails** with 20 retained entries. Record the number. The test is deleted in
step 2 together with the map; it is a reproduction, not a regression pin.

(`dbx_test.go` is `package dbx_test` and cannot see `connDialects`; a test there
can only observe exported behaviour. That is why this file is internal.)

### Step 2 — stop recording connections the transaction already knows

Delete `connDialects` entirely and make `Conn` a thin `db.Conn(ctx)`; drop the
`*sql.Conn` case from `DialectOf` (every other handle keeps its documented
default: SQLite for one nobody registered).

Why this is safe, and why it is the whole fix: no product code asks a
hand-driven conn for its dialect — the only `DialectOf(conn)` call in the tree is
the test at `pkg/state/dbx/dbx_test.go:84`. The two migration call sites already
know their dialect: `AcquireMigrationLock` switches on `DialectOf(db)`, and
`schema_dialect.go:102` carries a `dialect` variable. Keep `Conn`'s name and
signature so those callers do not move; drop only the bookkeeping. `beginMySQL`
needs no edit — it calls `Conn`, which no longer registers anything, and its `Tx`
already carries `dialect: MySQL`.

Update the test at `dbx_test.go:84` to pin the new documented answer (an
unregistered conn reads as SQLite) and say in its comment that a caller which
needs the dialect holds the `*sql.DB` or the `Tx`. Delete the step-1 reproduction
file with the map.

**Verify**: `grep -rn "connDialects" pkg` returns nothing,
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/dbx ./pkg/state -count=1`
→ `ok`.

### Step 3 — prove the migration path still answers

**Verify**: `./scripts/test-db.sh mysql` and `./scripts/test-db.sh postgres` → both
`ok`. This is the leg that exercises `AcquireMigrationLock` on a real server; if
Docker is unavailable, mark it unverified in the report and do not claim it.

### Step 4 — state the invariant in the code

Add to `dbx.go` a one-line comment on `DialectOf` saying only a `*sql.DB`
registered by `Open` and a `*Tx` from `BeginTx` answer, and that a hand-driven
`*sql.Conn` deliberately reads as the default. This is the guard against the same
mistake returning with the next convenience wrapper.

**Verify**: `grep -n "bounded by" pkg/state/dbx/dbx.go` returns the comment.

## Test plan

- New: `TestConnRegistrationsGrowWithoutBound` (step 1, temporary).
- Changed: the `DialectOf(conn)` assertion in `dbx_test.go:84` now pins the
  documented default.
- Keep green: everything in `pkg/state/dbx` and `pkg/state`, plus the two dialect
  legs.
- Failure path: a `*sql.Conn` obtained through `dbx.Conn` must still report the
  documented default (SQLite) — assert it once so the choice is intentional.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` passes
- [ ] `grep -rn "connDialects" pkg` returns nothing (map and its bookkeeping are gone)
- [ ] The `DialectOf` test pins the new documented answer
- [ ] `./scripts/test-db.sh mysql` passes, or is reported unverified with the reason
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 227 updated

## STOP conditions

- A production caller of `DialectOf` passes a `*sql.Conn`: grep the tree before
  editing. At `7d505aa3` the only such call is `dbx_test.go:84`; if you find a
  product call site, STOP — the fix then has to keep a dialect registry and
  release it explicitly, which is a different plan.
- Removing the map makes a migration test fail on SQLite (the migration executor
  may obtain its conn through `dbx.Conn` there too): report which test, and take
  the delete-on-release variant.
- Docker is unavailable and the migration leg is the only coverage: complete
  steps 1, 2, 4, and say the migration path is unverified on a server.

## Maintenance notes

- Any future convenience wrapper that records a handle in a package-level map
  needs a release path in the same change; the comment added in step 4 is the
  reminder.
- A reviewer should check that the MySQL write path still takes the named lock on
  **its own dedicated connection** — that is the reason `db.Conn` is used at all,
  and dropping it would silently serialise nothing.
