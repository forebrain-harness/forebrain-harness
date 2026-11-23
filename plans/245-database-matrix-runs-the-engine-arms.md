# Plan 245: the database matrix runs every package that has an engine arm

> **Executor instructions**: Follow the steps in order and confirm each
> verification. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and
> report it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- scripts/test-db.sh`
> If the script changed, compare the "Current state" excerpt; content mismatch is
> a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW — a shell script; the worst case is a run that takes longer, and
  the new guard turns a false green into a red.
- **Depends on**: none (plan 242's engine legs are cheaper once this lands)
- **Category**: tests
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Finding**: branch audit 2026-11-05 (`origin/dev..HEAD`), finding 17
  (`introduced`: plans 222–237 added engine arms the script cannot reach)

## Why this matters

`scripts/test-db.sh` is the only way the suite is ever run on MySQL 8 and
PostgreSQL 14. It runs:

```bash
"${go_test[@]}" ./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open'
```

Two packages, three name fragments. Everything else that has an engine arm is
silently skipped — including the arms this branch added (lease CAS, the peer
inbound queue) and several that predate it (`pkg/process`, `pkg/gateway`,
`pkg/memory`, `pkg/migrate`, `pkg/tool`, `pkg/turn`, `pkg/run`).

How it stays invisible: `state.OpenStateForTest` (`pkg/state/db.go:241-250`)
forks every caller onto the server engine when `FOREBRAIN_TEST_DATABASE_URL` is
set. A package that never receives that variable simply runs its tests on SQLite
and reports `ok`. `plans/228-persist-text-encoding.md:119` already admits the
consequence in prose ("the script's `Dialect/Schema/Open` filter skips the new
tests"); the fix is to make the script stop lying, not to keep writing that note.

Worse, the filter has a second failure mode with no warning at all: a `-run`
pattern that matches no test exits 0. A plan that says "run `-run 'TestX'`" can be
green while nothing ran.

## Current state

`scripts/test-db.sh:52-56` — the whole engine path:

```bash
run_dialect() {
  local url=$1
  FOREBRAIN_TEST_DATABASE_URL="$url" FOREBRAIN_HOME="$(scratch_home)" \
    "${go_test[@]}" ./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open'
}

run_sqlite() {
  FOREBRAIN_HOME="$(scratch_home)" "${go_test[@]}" ./... -p 2
}
```

`scripts/test-db.sh:62-79` — the CLI (`up|mysql|postgres|all|down`), with `all`
= up + sqlite + both engines. The script runs under `set -euo pipefail`.

`pkg/state/db.go:241-250` — the helper that makes a package "engine-capable":

```go
func OpenStateForTest(ctx context.Context, absSQLitePath string) (*sql.DB, error) {
	url, ok, err := testDatabaseURLFor(absSQLitePath)
	…
	if !ok {
		return Open(ctx, absSQLitePath, nil)
	}
	return OpenFromConfig(ctx, "", url, nil)
}
```

Packages that call `OpenStateForTest` (anything that calls it has an engine arm):
`pkg/state`, `pkg/state/dbx`, `pkg/process`, `pkg/gateway`, `pkg/run`, `pkg/turn`,
`pkg/tui`, `pkg/tool`, `pkg/memory`, `pkg/migrate`, `pkg/hook`, `pkg/assembly`.
Direct `FOREBRAIN_TEST_DATABASE_URL` readers: `pkg/state/db_test.go`,
`pkg/state/schema_dialect_test.go`, `pkg/state/session_events_test.go`,
`pkg/state/dbx/dbx_test.go`, `pkg/state/dbx/tx_test.go`,
`pkg/gateway/session_files_test.go`, `pkg/memory/ftsindex_test.go`,
`pkg/migrate/codex_test.go`, `pkg/process/config_import_test.go`.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Syntax | `bash -n scripts/test-db.sh` | exit 0 |
| Guard only | `./scripts/test-db.sh check` | prints `engine arms: N rows, every package with arms listed`, exit 0 |
| SQLite | `./scripts/test-db.sh sqlite` (add this subcommand in step 2) | `ok` |
| One engine | `./scripts/test-db.sh mysql` | every leg `ok`, and each leg's test count > 0 |
| Both engines | `./scripts/test-db.sh all` | as above, twice |

Docker: the script starts `fb-mysql` and `fb-pg`. Each test creates a scratch
database, and InnoDB/PostgreSQL do not return the space, so a big run grows the
Docker volume by gigabytes. Run the legs serially, and if `docker` starts timing
out: `docker rm -f -v fb-mysql fb-pg && ./scripts/test-db.sh up` (scratch servers,
nothing to preserve).

## Scope

**In scope**:
- `scripts/test-db.sh`

**Out of scope**:
- Any Go file. Do not add a `testing`-side registry; the script is where the
  matrix lives.
- `Makefile` / CI wiring — if you think CI should call this, report it instead.
- The individual engine tests' content.

## Steps

### Step 1: a table instead of two hardcoded packages

Replace `run_dialect`'s body with a loop over an explicit table. Each row is
`<packages>|<run pattern>`; the pattern is the names that need a server engine:

```bash
# Every package with an engine arm, and the tests in it that need a server.
# "Engine arm" = a test that reaches state.OpenStateForTest or reads
# FOREBRAIN_TEST_DATABASE_URL; on SQLite they pass without a server, which is
# exactly why a missing row here is invisible. `check` (below) enforces this
# list against the same grep, so a package cannot lose its row silently.
# Fragments are case-sensitive (go test -run takes a Go regexp); every fragment
# below was verified to match at least one test name at the planning baseline.
engine_arms=(
  "./pkg/state/dbx/...|Dialect|Open|Tx|Lock"
  "./pkg/state/|Dialect|Schema|Open|Reap|SearchBackslash|StoreRepairs|Repair|Parts|Output|Events|Artifact|Project"
  "./pkg/process|Lease|ImportLegacy|ConfigDB|Startup|PeerInbound"
  "./pkg/gateway|ControlPlane|RunControl|Channels"
  "./pkg/memory|Search|Index|Consolidation"
  "./pkg/migrate|Codex|Sessions|LSP"
  "./pkg/tool|ShellOutput|Artifact"
  "./pkg/turn|Session|Scheduler|Rewind|Events|Batch|Approval"
  "./pkg/run|Subagent|Runner|Config|Skills|Compaction|Permissions|AttemptLedger|Transcript"
  "./pkg/hook|Transcript|ToolMiddleware|PermissionRequest"
  "./pkg/tui|RecordedLikeTheGateway|PrimaryBudget|SurfaceToolApproval"
  "./pkg/assembly|Compact|Resume|Checkpoint"
)
```

The `./pkg/hook`, `./pkg/tui` and `./pkg/assembly` rows are the packages the
old two-package run never reached; their patterns are a first cut from the
tests that call `OpenStateForTest` in each package. Step 3's prune pass must
reconcile them — extend a pattern where an arm is missed, never delete a
package row to get green.

Then:

```bash
run_dialect() {
  local url=$1
  local row pkgs pattern log ran
  for row in "${engine_arms[@]}"; do
    pkgs=${row%%|*}
    pattern=${row#*|}
    echo "== $pkgs on $url"
    log=$(mktemp)
    # tee keeps the live output on stderr while $log feeds the count below.
    # pipefail (set at the top of the script) makes this `if` fail when go
    # test fails — a failing leg must fail the run even though it ran tests.
    # Capturing only the grep count (the first draft's `| grep -c … || true`)
    # would have swallowed go test's exit status and turned a red leg green.
    if ! FOREBRAIN_TEST_DATABASE_URL="$url" FOREBRAIN_HOME="$(scratch_home)" \
      "${go_test[@]}" -v $pkgs -run "$pattern" 2>&1 | tee /dev/stderr >"$log"; then
      rm -f "$log"
      return 1
    fi
    ran=$(grep -c '^=== RUN' "$log" || true)
    rm -f "$log"
    # A -run that matches nothing is not a pass: it is the silent skip this
    # whole change exists to remove.
    if [ "${ran:-0}" -eq 0 ]; then
      echo "no test matched '$pattern' in $pkgs — the row is stale" >&2
      return 1
    fi
  done
}
```

Notes for the executor:

- `$pkgs` is unquoted on purpose (one row may name several packages).
- Keep `-count=1` (already in `go_test`).
- If a row's pattern matches nothing, that is a finding: prune the pattern to what
  exists **and report which row was stale** — never delete a row to get green.

**Verify**: `bash -n scripts/test-db.sh` → exit 0

### Step 2: add `sqlite` and `check`, keep `up|down|all`

- `sqlite) run_sqlite ;;` — so a run can do one engine at a time without editing
  the file.
- `check)` — the anti-rot guard:

```bash
check() {
  local missing=0 pkg
  local have
  have=$(grep -rlE 'FOREBRAIN_TEST_DATABASE_URL|OpenStateForTest\(' --include='*_test.go' pkg 2>/dev/null \
    | xargs -n1 dirname 2>/dev/null | sort -u)
  for pkg in $have; do
    if ! printf '%s\n' "${engine_arms[@]}" | grep -q "$pkg"; then
      echo "engine arms in ./$pkg but no row in engine_arms" >&2
      missing=1
    fi
  done
  if [ "$missing" -ne 0 ]; then
    echo "add the package to engine_arms (with the tests that need a server)" >&2
    return 1
  fi
  echo "engine arms: $(printf '%s\n' "${engine_arms[@]}" | wc -l | tr -d ' ') rows, every package with arms listed"
}
```

(The `2>/dev/null` on `xargs` keeps BSD xargs's empty-input `dirname` usage
message out of the output; GNU xargs runs nothing on empty input. If the two
still behave differently on your machines, the STOP below applies.)

- `all)` keeps its order: `up` → `run_sqlite` → `run_dialect mysql` →
  `run_dialect postgres`. Update the usage line.

**Verify**: `./scripts/test-db.sh check` → prints the summary line, exit 0

### Step 3: run one engine and prune honestly

**Verify**: `./scripts/test-db.sh up && ./scripts/test-db.sh mysql` → every row
prints its package and passes, and no row reports "no test matched".

For any row that reports a stale pattern, cut the pattern to the names that exist
(`grep -n '^func Test' <file>` is the source of truth) and record it in the
report. `-run` is case-sensitive: `ControlPlane` (the actual spelling,
`TestControlPlaneTokenFromRequest`) is not `Controlplane`. Then repeat for
`./scripts/test-db.sh postgres`.

## Test plan

- This plan changes a test runner, so its own acceptance is the runner:
  `bash -n`, `check`, and both engine legs with a non-zero test count per row.
- Prove the guard works: temporarily point one row at a pattern with no match
  (`-run 'NoSuchTestAtAll'`) and confirm the script **fails**; restore it.
- Prove `check` works: temporarily add `pkg/hook` to `engine_arms` removal …
  i.e. comment out its row if one exists and confirm `check` names `./pkg/hook`
  (it has `runtime_test.go` calling `OpenStateForTest`); restore it.

## Done criteria

ALL must hold:

- [ ] `bash -n scripts/test-db.sh` exits 0
- [ ] `./scripts/test-db.sh check` exits 0 and names every package with an arm
- [ ] `./scripts/test-db.sh mysql` passes, with each row's test count > 0
- [ ] `./scripts/test-db.sh postgres` passes, same
- [ ] A stale pattern makes the script exit non-zero (guard observed red, then reverted)
- [ ] `grep -n "run 'Dialect|Schema|Open'" scripts/test-db.sh` returns nothing
- [ ] Only `scripts/test-db.sh` modified (`git status --porcelain`)
- [ ] `plans/README.md` row for 245 updated

## STOP conditions

- A row's pattern matches tests that **fail** on a server engine for a reason this
  plan did not cause: keep the row, and report the failing test — that is a new
  finding, not a reason to narrow the pattern.
- The Docker containers cannot start (no Docker, no disk): run `check` and
  `bash -n`, report the legs as **not run**, and say which criteria are therefore
  unverified.
- `grep`/`xargs` behave differently on BSD vs GNU for the `check` derivation
  (`macOS` vs CI): report the difference and use a `while read` loop instead of
  `xargs` if it cannot be made portable in one line.

## Maintenance notes

- The rule: **a package with an engine arm gets a row, and a row that matches
  nothing fails the run.** When a plan adds an engine test, it adds the name to
  the row.
- Cost control: every row creates scratch databases. If `pkg/gateway`'s row grows
  to the whole package, one MySQL leg can grow the Docker volume by several GiB;
  keep its pattern narrow and recycle the containers (`down`, `up`) between large
  runs.
- A reviewer should check the count guard is on the **test** count
  (`^=== RUN`), not on the exit status alone — the exit-status-only version is what
  produced the false green this plan removes.
