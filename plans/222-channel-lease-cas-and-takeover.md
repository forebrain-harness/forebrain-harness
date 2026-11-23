# Plan 222: one replica holds a channel account, and a standby actually takes over

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on. If
> anything in "STOP conditions" happens, stop and report — do not improvise.
> When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/process/lease.go pkg/process/lease_test.go`
> Then compare the excerpts below against the live code. Content mismatch is a
> STOP condition; line movement alone is not.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW (confined to `pkg/process/lease.go` and its test)
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 1 (`introduced` by plans 213)

## Why this matters

When several gateway replicas share one database, exactly one of them may keep a
long-connection chat account (Telegram, Feishu, Discord, …) connected — two
connected copies answer every inbound message twice. `pkg/process/lease.go` owns
the compare-and-swap row (`fb_leases`) that decides which replica that is.

Three defects make that row wrong today. (1) The takeover cutoff is bound to the
**new** expiry instead of "now", so a live holder's fresh lease always looks
expired and a second replica steals it on its next 15 s retry — both replicas end
up connected until the old holder notices up to 10 s later. (2) The same
`n == 1` success test is applied to MySQL's `INSERT … ON DUPLICATE KEY UPDATE`,
which reports **2** when it updates a row: on MySQL a replica can never win a
lease that already has a row, so after any restart the channel never reconnects
at all. (3) A replica that loses the first acquire cancels its own context and
leaves the retry loop, so it never takes the account over when the holder dies —
the standby does not stand by.

## Current state

`pkg/process/lease.go` — the lease row, its three SQL arms, and the handler
wrapper. Read the whole file before editing; it is ~250 lines.

`pkg/process/lease.go:21-30` — the timing constants:

```go
	// LeaseTTL is how long a held lease stays valid without a renewal. It is
	// twice the renewal interval, so one missed renewal — a database hiccup,
	// not a dead process — does not hand the account to another replica.
	LeaseTTL = 20 * time.Second
	// LeaseRenewEvery is how often the holder extends its lease while the
	// channel is connected.
	LeaseRenewEvery = 10 * time.Second
	// LeaseRetryEvery is how often a replica that did not get the lease tries
	// again, which is what takes the account over when its holder goes away.
	LeaseRetryEvery = 15 * time.Second
```

`pkg/process/lease.go:37-52` — the compare-and-swap. Note the SQLite and
Postgres arms compare `fb_leases.expires_at_ms < ?`, and the MySQL arm compares
against `VALUES(expires_at_ms)`:

```go
	leaseAcquireSQL = dbx.SQL{
		SQLite: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES(?,?,?)
ON CONFLICT(name) DO UPDATE SET holder=excluded.holder, expires_at_ms=excluded.expires_at_ms
WHERE fb_leases.expires_at_ms < ?`,
		MySQL: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES(?,?,?)
ON DUPLICATE KEY UPDATE
  holder=IF(fb_leases.expires_at_ms < VALUES(expires_at_ms), VALUES(holder), holder),
  expires_at_ms=IF(fb_leases.expires_at_ms < VALUES(expires_at_ms), VALUES(expires_at_ms), fb_leases.expires_at_ms)`,
		Postgres: `
INSERT INTO fb_leases(name, holder, expires_at_ms) VALUES($1,$2,$3)
ON CONFLICT(name) DO UPDATE SET holder=excluded.holder, expires_at_ms=excluded.expires_at_ms
WHERE fb_leases.expires_at_ms < $4`,
	}
```

`pkg/process/lease.go:85-97` — the defect. The fourth argument is `expires`
(now + 20 s), but the comparison needs `now`:

```go
func (l *Leases) acquire(ctx context.Context, name string) (bool, error) {
	expires := time.Now().UnixMilli() + LeaseTTL.Milliseconds()
	res, err := dbx.Exec(ctx, l.DB, leaseAcquireSQL.For(dbx.DialectOf(l.DB)),
		name, l.Holder, expires, expires)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
```

`pkg/process/lease.go:150-205` — `Start` and `hold`. `cancel` is the cancel of
`runCtx`; it is handed to `hold` as `delegateCancel` **already non-nil**, so the
first loop iteration calls `renewHeld`, and a replica with no row of its own
fails it (0 rows affected), stops a handler it never started, cancels `runCtx`,
and then returns at `if ctx.Err() != nil { return }` — the acquire retry below is
never reached:

```go
	won, err := s.leases.acquire(runCtx, s.name)
	...
	if won {
		if err := s.handler.Start(runCtx, add, bus); err != nil {
			cancel()
			_ = s.leases.release(context.WithoutCancel(runCtx), s.name)
			close(done)
			return err
		}
	}
	go s.hold(runCtx, add, bus, done, cancel)
	return nil
```

`pkg/process/lease.go:160-170` (inside `hold`) — the loop head that a standby
must not enter with a delegate it never started:

```go
	for {
		if delegateCancel != nil && !s.renewHeld(ctx) {
			_ = s.handler.Stop(context.WithoutCancel(ctx))
			delegateCancel()
			delegateCancel = nil
		}
		if ctx.Err() != nil {
			return
		}
```

`pkg/process/lease.go:117-121` — `release` exists and is only called when
`Start` fails; nothing releases on a normal stop.

`pkg/process/lease_test.go` — the existing tests, and the helpers this plan's new
test must reuse: `testLeases(t, holder)` (`:17-25`), `ageLease(t, db, name)`
(`:258-264`), `takeLease(t, db, name, holder)` (`:266-275`),
`waitForLease(t, what, ok)` (`:277+`), and `stubChannel` (same file).

### Reproduction already recorded (2026-11-04)

- `TestLeaseIsHeldByOneReplicaAndTakenOverWhenItExpires` **passes only when both
  acquires land in the same millisecond**. A probe test that sleeps 5 ms between
  them reports `second.acquire won=true; first.renew held=false`, i.e. a fresh
  lease was stolen. So the existing test is flaky by construction.
- Reading `hold` confirms a standby never retries (defect 3). Your step 2 test
  is the reproduction to add; it must be red before step 3.

### Conventions

- Every statement in this package carries three dialect arms in a `dbx.SQL{}`
  value; never write a bare `?` statement that only SQLite can run. See
  `pkg/process/lease.go` itself and `pkg/memory/jobs.go:162-186` for the
  conditional-upsert shape this file must match.
- Comments explain *why* (see the block above `leaseAcquireSQL`). Keep that voice.
- MySQL evaluates a single-table `UPDATE`/upsert `SET` list **left to right**,
  reading the values it has already assigned. Any `IF(old, new, old)` chain must
  be ordered with that in mind; `pkg/memory/jobs.go:324-337` documents the same
  trap for the memory claim.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0, no output |
| Focused tests (SQLite) | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestLease|TestGuarded' -count=1` | `ok`, all pass |
| Three-dialect leg | `./scripts/test-db.sh up`, then the explicit `FOREBRAIN_TEST_DATABASE_URL` command in step 5 (the script itself does not run `pkg/process`) | `ok` on each engine |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/channel ./pkg/gateway -count=1` | `ok` |

`./scripts/test-db.sh` starts `fb-mysql` (port 13306) and `fb-pg` (15432) and runs
the dialect tests with `FOREBRAIN_TEST_DATABASE_URL` already set; each test
derives its own database (`fbt_<hash>`), so runs do not collide. If Docker is
unavailable, say so in your report and mark the MySQL leg unverified — do **not**
claim it passed.

## Scope

**In scope**:
- `pkg/process/lease.go`
- `pkg/process/lease_test.go`

**Out of scope** (do not touch):
- `pkg/channel/account.go` (`LeaseName`/`NeedsLease` classification is settled).
- `pkg/gateway/channels.go` wiring.
- The `fb_leases` schema and its migrations — no column changes are needed.
- Any retry/backoff policy for `dbx.WriteLockTimeout`-style errors; the lease
  loop's existing cadence is the contract.

## Steps

### Step 1 — bind "now" as the takeover cutoff, on all three arms

In `acquire`, compute `now := time.Now().UnixMilli()` and `expires := now +
LeaseTTL.Milliseconds()`, and pass `now` as the fourth argument. Rewrite the MySQL
arm so both `IF`s compare against a bound parameter instead of
`VALUES(expires_at_ms)`:

```sql
ON DUPLICATE KEY UPDATE
  holder=IF(fb_leases.expires_at_ms < ?, VALUES(holder), fb_leases.holder),
  expires_at_ms=IF(fb_leases.expires_at_ms < ?, VALUES(expires_at_ms), fb_leases.expires_at_ms)
```

Two extra `?` placeholders means the MySQL arm takes **five** arguments
(name, holder, expires, now, now) while SQLite/Postgres take four. `dbx.SQL.For`
does not renumber by dialect, so pass the arguments per dialect: branch on
`dbx.DialectOf(l.DB)` in `acquire` and build the slice accordingly. Keep the SQLite/Postgres arms byte-identical to today: the SQLite arm's text is
pinned by `pkg/architecture/testdata/sqlite_sql_snapshot.json`, and
`TestSQLiteStatementsUnchanged` (`pkg/architecture/sqlite_sql_test.go:22`) fails
on any edit to it. That snapshot is regenerated only by
`go run ./scripts/sqlite-sql-snapshot`, and only for a deliberate change — this
plan needs none, because only the MySQL arm gains placeholders. After step 1 run
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5
./pkg/architecture -count=1`; it must pass unchanged. If it reports the SQLite
text moved, STOP and report: the statement shape was not supposed to change.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run TestLeaseIsHeldByOneReplicaAndTakenOverWhenItExpires -count=1`
→ still passes. Then a temporary probe (delete it afterwards) that sleeps 5 ms
between the two acquires must now report `won=false`.

### Step 2 — judge a MySQL acquire by affected rows, not by `n == 1`

Replace the `n == 1` success test with `n > 0`. After step 1 the upsert's `SET`
clause only fires when the bound `now` beats the row's `expires_at_ms`, so
"changed a row" and "won the lease" are the same event: MySQL reports 1 for the
insert, 2 for the winning update and 0 for a row it left alone, so `n > 0` is the
one test that is right on all three engines. Do **not** add a read-back
`SELECT holder ...`: a new production statement would need a three-arm `dbx.SQL`
(a bare `?` does not run on pgx) and would move the pinned SQLite snapshot — two
architecture gates for a question `RowsAffected` already answers.

`renew` keeps `n == 1`: it is a plain single-row `UPDATE`, which MySQL reports as
1 when it changed the row and 0 when the value was already equal; the 2 belongs
to `INSERT ... ON DUPLICATE KEY UPDATE` only. Leave the line as it is.

**Verify**: the probe from step 1 still passes, and
`TestLeaseReleaseGivesTheAccountBack` passes.

### Step 3 — a standby must retry, and a holder must release on stop

Two things must change together, or only half the defect is fixed.

1. `Start` must give the delegate its **own child context**:
   `delegateCtx, delegateCancel := context.WithCancel(runCtx)`, and pass that
   context to `s.handler.Start`. Today `Start` hands `hold` the cancel of
   `runCtx` itself (`pkg/process/lease.go:139,178`), and `hold` runs on `runCtx`
   too — so when any holder loses the lease and calls `delegateCancel()`, it
   cancels the retry loop as well and exits for good. A displaced holder would
   never take the account back.
2. `Start` must pass `nil` as `hold`'s `delegateCancel` when the acquire was not
   won, and the child's cancel only when the delegate was actually started. Then
   restructure `hold` so a replica without a delegate goes straight to
   `sleep(ctx, LeaseRetryEvery)` + `acquire`: today the `if ctx.Err() != nil {
   return }` after the cancel makes that impossible.

`Stop` keeps cancelling `runCtx` — that ends the loop and, through the parent,
the delegate.

Add: `Stop` releases the row (`s.leases.release` with a `context.WithoutCancel`
context, only when this process ever won it). A stopped holder that leaves its
row behind forces every standby to wait out `LeaseTTL`.

**Verify**: the new test in the test plan below — red before this step, green
after.

### Step 4 — the takeover test

Add `TestLeaseStandbyTakesOverWhenTheHolderStopsRenewing` to
`pkg/process/lease_test.go`, modelled on `TestGuardedChannelConnectsOnOneReplicaOnly`
(same `stubChannel`, same `waitForLease`):

1. two `Leases` over one SQLite database (the existing `testLeases` +
   `second.DB = first.DB` pattern), guarded handlers for both;
2. start both; wait until the first's `connected() == 1` and assert the second's
   `connected() == 0`;
3. `ageLease(t, db, "channel:acct")` (the holder "died" without renewing);
4. `waitForLease(t, "the standby to take the account over", func() bool { return
   secondH.connected() == 1 })` — this waits up to 20 s, one `LeaseRetryEvery`
   cycle plus slack;
5. stop both handlers; snapshot each `stubChannel`'s `connections()` counter,
   sleep 50 ms, and assert neither moved;
6. add the symmetric half: with the second replica connected, `ageLease` again
   and `waitForLease` until the **first** is connected again — a holder that lost
   the lease must rejoin the retry loop instead of exiting.

Also change `TestLeaseIsHeldByOneReplicaAndTakenOverWhenItExpires` so the two
acquires are separated by a real gap (`time.Sleep(5 * time.Millisecond)`) — the
test that catches the stealing bug must not depend on both calls landing in the
same millisecond.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestLease|TestGuarded' -count=1 -v`
→ every test passes, and the step-4 test's duration is ≥ 15 s (it waited a real
retry cycle).

### Step 5 — the MySQL leg

`./scripts/test-db.sh mysql` does **not** run this package: it runs
`./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open'`
(`scripts/test-db.sh:52-58`). Run the lease tests directly against the server:

```bash
./scripts/test-db.sh up
FOREBRAIN_TEST_DATABASE_URL='mysql://root:fb@127.0.0.1:13306/fbt' \
  FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 \
  go test -tags fts5 ./pkg/process -run 'TestLease|TestGuarded' -count=1
```

`testLeases` opens through `state.OpenStateForTest`, which honours that variable
and derives an `fbt_<hash>` database per run (`pkg/state/db.go:241-262`), so the
run does not collide with anything else.

**Verify**: `ok`. Before steps 1–2 this run fails on MySQL — but not where it
looks: the second acquire's upsert *does* overwrite the row (its `IF` compares
against the new expiry) and reports 2 affected rows, so `won` is false; the test
then fails one assertion later, when the first replica's `renew` finds the row is
no longer its own.

## Test plan

- New: `TestLeaseStandbyTakesOverWhenTheHolderStopsRenewing` (step 4).
- Changed: `TestLeaseIsHeldByOneReplicaAndTakenOverWhenItExpires` — insert the
  5 ms gap.
- Structural pattern: `TestGuardedChannelConnectsOnOneReplicaOnly`
  (`pkg/process/lease_test.go:169`) — two guarded handlers over one database,
  `stubChannel` counters, `waitForLease`.
- Regression: `TestLeaseReleaseGivesTheAccountBack`,
  `TestGuardLeavesWebhookKindsAndHalfConfiguredChannelsAlone`,
  `TestNeedsLeaseClassification` must all stay green.
- Failure paths covered by the above: a fresh lease is not stolen; a standby
  takes over after expiry; a loser does not connect while the holder holds; a
  displaced holder rejoins and retakes the account; a release makes the account
  available immediately.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0 with no output
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/channel ./pkg/gateway -count=1` passes
- [ ] The `./pkg/process` lease tests pass on MySQL and PostgreSQL through the `FOREBRAIN_TEST_DATABASE_URL` command in step 5 (or the report names the leg as unverified with the reason)
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` passes (no SQLite statement moved)
- [ ] `grep -n "name, l.Holder, expires, expires" pkg/process/lease.go` returns nothing
- [ ] A standby whose first acquire lost now reaches the retry loop: the step-4 test passes and shows `connected() == 1`
- [ ] `git status --short` shows only the two in-scope files
- [ ] `plans/README.md` row for 222 updated

## STOP conditions

- The drift check shows `pkg/process/lease.go` changed since `7d505aa3` in a way
  that moves the acquire/hold structure — re-read before editing.
- The SQLite text of `leaseAcquireSQL` has to change (the snapshot in
  `pkg/architecture/testdata/sqlite_sql_snapshot.json` would move): stop and
  report, because that snapshot is a deliberate artifact.
- `pkg/channel` classifies a kind you did not expect as needing a lease, or
  `LeaseName` returns an empty name for a configured channel: report rather than
  change the classification.
- MySQL reports a third affected-row count for your upsert — print the number and
  stop; do not add a third interpretation.

## Maintenance notes

- Any new long-connection channel kind must keep working through
  `NeedsLease`/`LeaseName`; nothing in this plan changes who mounts routes.
- A reviewer should check: the SQLite/Postgres SQL is unchanged; the MySQL arm
  binds `now` twice; `acquire` no longer trusts `RowsAffected` on MySQL; `Stop`
  releases only a lease this process actually won.
- Deliberately deferred: a renewal that fails because the database is down holds
  the account for up to `LeaseTTL` (20 s) on the old replica. Making renewal
  failure distinguish "database unavailable" from "lost the lease" needs a
  health signal the lease does not have; out of scope here.
