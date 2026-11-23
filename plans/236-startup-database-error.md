# Plan 236: an unreachable database is an error, not a fresh install

> **Executor instructions**: Follow the steps in order; confirm each verification.
> If a STOP condition occurs, stop and report. When done, update this plan's row in
> `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/process/config_db.go`
> Content mismatch against the excerpts is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: correctness
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 16 (`introduced` by plan 187)

## Why this matters

Runtime configuration moved into the state database. For a MySQL or PostgreSQL
install the `forebrain.yaml` file now holds little more than the database URL, so
**the database is the configuration**.

`ComposedStartupConfig` treats *any* failure to open the database as "there is no
database yet, fall back to the file":

```go
	db, err := state.OpenFromConfig(ctx, home, databaseURL, &state.OpenOptions{ReadOnly: true})
	if err != nil {
		return appcfg.Root{}, false, nil
	}
```

So a database that is down, whose password is wrong, whose host is unreachable, or
whose TLS handshake fails is indistinguishable from a brand-new SQLite home. The
first-run gate (`NeedsFirstSetup`) then reports "not configured" and the terminal
launches the setup wizard on an install that *is* configured; without a terminal
the gateway exits with "main agent LLM is not fully configured". In both cases the
real error — the one an operator needs — is discarded.

The fallback is correct for exactly one case: a SQLite home with no state file yet.

## Current state

`pkg/process/config_db.go:434-448` — the swallow:

```go
	databaseURL, err := appcfg.FileDatabaseURL(cfgPath)
	if err != nil {
		return appcfg.Root{}, false, fmt.Errorf("read database.url from %s: %w", cfgPath, err)
	}
	db, err := state.OpenFromConfig(ctx, home, databaseURL, &state.OpenOptions{ReadOnly: true})
	if err != nil {
		return appcfg.Root{}, false, nil
	}
```

`pkg/tui/setup.go:1620-1647` — the callers (`startupJudgeConfig`,
`NeedsFirstSetup`) turn "not composed" into "judge from the file", and an
unconfigured file into "first setup".

`cmd/forebrain/serve.go:46-58` — the gateway's own path to the same judgement.

`pkg/state/db.go:232-240` — `OpenStateForTest` shows the difference the code needs:
`FOREBRAIN_TEST_DATABASE_URL` is honoured when set, otherwise SQLite. The product
path is `OpenFromConfig(ctx, home, databaseURL, …)`, where `databaseURL` already
tells the caller which engine was asked for: empty ⇒ SQLite; `mysql://` /
`postgres://` ⇒ a server.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui -run 'Config|Setup' -count=1` | `ok` |
| Gateway | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain ./pkg/gateway -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/process/config_db.go`
- `pkg/process/config_db_test.go` (the step-1 tests)
- `pkg/tui/setup_test.go` (the propagation pin)

**Out of scope**:
- `pkg/tui/setup.go` and `cmd/forebrain/serve.go` — they already propagate the
  error; this plan only proves it. No production change there.
- `pkg/state`'s open/migrate behaviour.
- What the setup wizard writes.
- The fallback for a SQLite home with no state file (that one is correct; keep it).
- Retry/backoff on a database that is briefly unavailable — the error is the
  product here.

## Steps

### Step 1 — decide the fallback by engine, not by failure

In `ComposedStartupConfig` (`pkg/process/config_db.go:432-461`), decide by engine
**before** opening, and make every non-transition open error an error:

- `databaseURL` empty (SQLite): `os.Stat(state.StateDBPath(home))` first. A
  missing file is the "not configured yet" case → `(Root{}, false, nil)`; any
  other stat error is returned. A file that exists is opened, and a failure is
  handled by the transition rule below.
- `mysql://` or `postgres://`: open, and on failure apply the transition rule
  below; anything else is
  `fmt.Errorf("open the state database named by %s: %w", cfgPath, err)`.

**The transition rule** (keep it, or the upgrade path breaks): a read-only open
whose error is `errors.Is(err, state.ErrStateSchemaOutdated)` or
`state.ErrStateSchemaNewer` still falls back to `(Root{}, false, nil)`. Those are
the cases the current comment blesses — the file is still the answer until the
binary is upgraded or migration runs (`pkg/state/schema_dialect.go:97,253-258`).
Every other error — connection refused, authentication, unknown database, a
locked file — is a failure to report.

Do **not** try to recognise the missing-file error with
`errors.Is(err, fs.ErrNotExist)`: `state.Open` returns the driver's
`sqlite3.Error{Code: SQLITE_CANTOPEN, SystemErrno: ENOENT}`, and
`mattn/go-sqlite3`'s error type has no `Is`/`Unwrap` — the test is always false and
would turn every fresh home into a startup failure.

Comment the function with the rule: *a configured server that cannot be reached is
a failure to report, never an empty configuration; an outdated schema is not a
failure to reach.*

**Verify**: a new test opens a home whose `forebrain.yaml` names
`mysql://127.0.0.1:1/x` (a port nothing listens on) and asserts
`ComposedStartupConfig` returns an error, not `(Root{}, false, nil)`. A second test
keeps a fresh SQLite home falling back silently. Both must pass; the first must
fail before the change.

### Step 2 — stop the surfaces from turning that error into "set up"

`startupJudgeConfig` already returns `(Root{}, err)` (`pkg/tui/setup.go:1620-1627`),
`NeedsFirstSetup` already returns `false, err` (`:1638-1648`), and
`ensureGatewayStartSetup` already returns the error (`cmd/forebrain/serve.go:47-50`).
**No production file changes in this step.** Pin the propagation with tests: the
new error from step 1 must reach `NeedsFirstSetup` (not `(true, nil)`) and the
gateway startup path. If either surface turns out not to propagate, stop and
report — that contradicts this plan's baseline and is a separate fix.

**Verify**: a test asserts `NeedsFirstSetup` returns the error (not `(true, nil)`)
for the unreachable-server home. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./cmd/forebrain -count=1` → `ok`.

### Step 3 — the message names the operator's next step

Make the error text name the file and the engine but **no credentials**: the URL may
carry a password, so never format the URL into the message. Say which config file
named it and what failed (`connection refused`, `authentication failed`, `unknown
database` — whatever the driver said, which is safe to pass through; check the
driver error for a DSN echo before trusting it — if the driver error includes the
URL, strip it).

**Verify**: a test asserts the message contains the config path and does not contain
the password from the test URL.

### Step 4 — the TUI's first-run path still works on a fresh home

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/process ./cmd/forebrain -count=1`
→ `ok`; a fresh SQLite home still reports "needs first setup".

## Test plan

- New: unreachable-server returns an error (step 1), fresh SQLite falls back (step
  1), an existing-but-unreadable SQLite file is an error (step 1), an **outdated**
  schema still falls back to the file (step 1), `NeedsFirstSetup` propagates (step
  2), the error message carries no password (step 3).
- Structural pattern: existing `pkg/process` tests that build a home with a
  `forebrain.yaml` (grep `FileDatabaseURL` in tests).
- Failure paths: wrong password; unknown database; server down; fresh home.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui ./cmd/forebrain ./pkg/gateway -count=1` passes
- [ ] `grep -n "return appcfg.Root{}, false, nil" pkg/process/config_db.go` is reachable only on the SQLite-missing-file branch (or gone)
- [ ] A test proves a `mysql://` URL that cannot be reached produces an error, and a test proves an `ErrStateSchemaOutdated` open still falls back to the file
- [ ] `pkg/tui/setup.go` and `cmd/forebrain/serve.go` are unmodified (`git status`)
- [ ] The error message contains no credentials (asserted)
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 236 updated

## STOP conditions

- `state.StateDBPath(home)` is not where an empty `database.url` actually opens
  (a relocated home, a symlinked state directory): report the path you found
  instead of hard-coding a second one — `state.OpenFromConfig` falls back to
  `Open(ctx, StateDBPath(home))` (`pkg/state/db.go:73-77`), so the stat and the
  open must name the same file.
- A surface ignores errors from `startupJudgeConfig` today by design (it must keep
  working when the database is empty): report that surface and leave it.
- The driver's error text echoes the DSN: report it; the message must be rebuilt
  from the driver's own code rather than passed through.

## Maintenance notes

- The invariant: **only SQLite's missing file means "not configured yet"**. Any new
  engine must implement the same distinction.
- A reviewer should check the fresh-home path is byte-for-byte unchanged and that no
  credential can reach a log line through this error.
