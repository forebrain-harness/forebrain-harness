# Plan 247: a test that runs on three engines has to speak all three

> **Executor instructions**: Follow the steps in order; confirm each verification
> before the next. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and report
> it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat d79a186b..HEAD -- pkg/run/subagent_test.go pkg/run/attempt_ledger_test.go pkg/run/image_input_test.go pkg/run/memory_llm_test.go pkg/tui/chat_session_test.go scripts/test-db.sh`
> If any in-scope file changed, compare the "Current state" excerpts with the live
> code. Content mismatch is a STOP condition; line drift alone is not.
>
> **Numbering**: this plan is 247, not 246. 246 is claimed by another session
> (the peer-card body rendering work) that had not written its file when this plan
> was drafted. Do not renumber this file.
>
> **Reading the drift check**: `d79a186b` is the reviewed baseline. The 238–245
> batch — including plan 245's `engine_arms` rewrite of `scripts/test-db.sh` —
> was an uncommitted working tree when this plan was drafted and is **committed**
> there; at that commit the nine literals, their handles and the two `engine_arms`
> rows quoted in "Current state" were verified against the live files. An empty
> diff therefore means "exactly as reviewed"; a non-empty diff is not
> automatically drift — compare the excerpts, and only a content mismatch stops
> the plan. Two in-scope files already moved between the drafting commit and
> `d79a186b` for reasons outside this plan: `scripts/test-db.sh` took 245's
> rewrite (the two rows this plan edits are unchanged and quoted in "Current
> state"), and `pkg/tui/chat_session_test.go` took the peer-card session's
> letter-text change around lines 9882/9936 — far from this plan's two literals,
> never yours to touch.

## Status

- **Priority**: P1 — `./scripts/test-db.sh postgres` fails at its `pkg/run` row (the
  leg aborts there), so the gate plan 245 installed is red today and every later
  batch inherits a red gate: a full `./pkg/run` on PostgreSQL shows 34 failing
  tests, plus 1 in `pkg/tui` outside every row's pattern.
- **Effort**: S — nine one-line wrappings, two matrix rows, no production code.
- **Risk**: LOW — the statements are test fixtures; `dbx.Rebind` is a no-op unless
  the dialect is PostgreSQL, and its own doc names this exact use ("a test's
  fixture, a one-off probe").
- **Depends on**: 245 (the matrix that finds these — DONE, its `engine_arms`
  rewrite committed in `d79a186b`), 242/243 (whose tree this baseline carries).
- **Category**: tests (multi-database)
- **Planned at**: commit `c7f8f5f4`, 2026-11-05 (245/238–244 were then an
  uncommitted working tree); **reviewed at** `d79a186b`, 2026-11-05 — the batch
  is committed there, the tree is clean, and the drift check anchors to it.

## Why this matters

`pkg/state/dbx.Rebind` numbers a statement's `?` placeholders as `$1…$n` for
PostgreSQL and leaves them alone for SQLite and MySQL. The store's own statements
are `dbx.SQL` arms and always carry the right spelling. A **test** that writes its
own SQL has no such arm, and nothing stops it writing SQLite's spelling — which
works on SQLite, works on MySQL, and is a syntax error on PostgreSQL, where `?`
is an operator rather than a placeholder.

Plan 245 made the matrix run every package with an engine arm, which is how this
surfaced. Measured on the current tree (`c7f8f5f4` + the batch in the working
tree), with `fb-mysql`/`fb-pg` up:

```
$ FOREBRAIN_TEST_DATABASE_URL=postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable \
  FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1 \
  -run 'Subagent|Runner|Config|Skills|Compaction|Permissions|AttemptLedger|Transcript'
--- FAIL: TestSubagentDispatchBirthsItsWorkerSession (0.15s)
    subagent_test.go:221: worker session row: ERROR: syntax error at end of input (SQLSTATE 42601)
--- FAIL: TestSubagentManualCompactionReusesTheSubagentsPrefix (0.14s)
    subagent_test.go:4888: ERROR: syntax error at or near "," (SQLSTATE 42601)
… 21 failures, every one SQLSTATE 42601
```

and outside that row's pattern, the same defect is already red today:

```
$ … go test -tags fts5 ./pkg/run -run 'TestImageRefusalRecorderIsIdempotent|TestWrapMemoryCitationLLM_UsageAccountingAcceptsOnlyCanonicalUUIDs'
--- FAIL: TestImageRefusalRecorderIsIdempotent (0.17s)
    image_input_test.go:409: count rows: ERROR: syntax error at end of input (SQLSTATE 42601)
--- FAIL: TestWrapMemoryCitationLLM_UsageAccountingAcceptsOnlyCanonicalUUIDs (0.12s)
    memory_llm_test.go:326: ERROR: syntax error at or near "," (SQLSTATE 42601)
$ … go test -tags fts5 ./pkg/tui -run 'TestWithdrawSurfaceTurnLeavesNoTrace'
--- FAIL: TestWithdrawSurfaceTurnLeavesNoTrace (0.20s)
    chat_session_test.go:6319: audit read: ERROR: syntax error at or near "AND" (SQLSTATE 42601)
```

Thirty-four of `pkg/run`'s tests are red on PostgreSQL and green on the other two
engines (twenty-one of them inside 245's row pattern — the rest are in the two
files that pattern does not reach), plus one in `pkg/tui`. Measured with a full
`go test ./pkg/run` against PostgreSQL, every failure is `SQLSTATE 42601` and
nothing else fails in that package, so the seven literals below account for all
of it. The matrix is the only thing that can see this, and a red matrix is a gate
nobody can use: 245's promise ("every package with an engine arm runs on the
server") is worth exactly as long as its rows are green.

The fix is the convention the repository already uses in three packages —
`dbx.Rebind(dbx.DialectOf(db), …)` at the call site, seen today in
`pkg/state/db_test.go:395`, `pkg/state/rewind_test.go:45`, `pkg/memory/store_test.go:172`
and `pkg/turn/events_test.go:286`.

## Current state

`pkg/state/dbx/dbx.go:89-97` — the helper, and the sentence that authorises this
use:

```go
// Rebind rewrites a SQLite '?'-placeholder statement for d: PostgreSQL numbers
// its parameters, SQLite and MySQL write every one as '?'. The arguments stay
// in the order the '?' appear. A '?' inside a single-quoted string literal is
// left alone, so a statement that carries a literal '?' still rebinds.
//
// It is for a statement that is assembled or seeded outside the store's own
// dbx.SQL arms — a test's fixture, a one-off probe — where writing the same text
// three times would be more code than the statement is worth.
func Rebind(d Dialect, query string) string
```

`pkg/state/dbx/dbx.go:158` — `DialectOf(q any) Dialect` answers for a `*sql.DB`
registered by `state.Open`/`state.OpenFromConfig` (which is what
`state.OpenStateForTest` returns under `FOREBRAIN_TEST_DATABASE_URL`), and for a
`dbx.Tx`.

The nine literals, with the handle each one runs on:

| File:line | Handle | Statement |
|---|---|---|
| `pkg/run/subagent_test.go:220` | `db` (`*sql.DB`) | `SELECT source, parent_session_id FROM fb_sessions WHERE id = ?` |
| `pkg/run/subagent_test.go:4215` | `f.store.DB()` | `INSERT INTO fb_runs(…) VALUES('seed-run', ?, NULL, …)` — inside `persistenceFixture.seedWorker`, so it is the seed of a large part of the package |
| `pkg/run/subagent_test.go:5558` | `store.DB()` | `SELECT COUNT(*) FROM fb_messages WHERE session_id=?` — inside the `countConversationMessages` helper |
| `pkg/run/attempt_ledger_test.go:200` | `db` | `SELECT usage_llm_calls, usage_prompt_tokens, usage_completion_tokens FROM fb_runs WHERE id=?` |
| `pkg/run/image_input_test.go:408` | `db` | `SELECT COUNT(*) FROM fb_session_image_refusals WHERE session_id=?` |
| `pkg/run/memory_llm_test.go:325` | `ms.DB` | `INSERT INTO fb_sessions(…) VALUES(?,?,?,?,?,?)` |
| `pkg/run/memory_llm_test.go:340` | `ms.DB` | `SELECT COALESCE(usage_count,0) FROM fb_memory_stage1_outputs WHERE thread_id=?` |
| `pkg/tui/chat_session_test.go:2338` | `s.Env.SQL` | `SELECT COUNT(*) FROM fb_sessions WHERE id=?` |
| `pkg/tui/chat_session_test.go:6316` | `s.sessStore().DB()` | `SELECT COUNT(*) FROM fb_messages WHERE session_id=? AND visibility='withdrawn'` |

Two statements that name `fb_runs` but carry **no** placeholder are correct as
they stand and must not be touched: `pkg/run/subagent_test.go:5303` and
`pkg/tui`'s other raw statements with literal values. `Rebind` is only for a
statement whose parameters are `?`.

The matrix rows this plan widens, in `scripts/test-db.sh` (`engine_arms`):

```bash
  "./pkg/run|Subagent|Runner|Config|Skills|Compaction|Permissions|AttemptLedger|Transcript"
  "./pkg/tui|RecordedLikeTheGateway|PrimaryBudget|SurfaceToolApproval"
```

`pkg/run`'s row reaches the `subagent_test.go` and `attempt_ledger_test.go`
failures but not `image_input_test.go`/`memory_llm_test.go`; `pkg/tui`'s reaches
none of its two. That is the second half of the defect: the rows are narrower
than the packages' placeholder-bearing tests, so a green row says less than it
appears to.

## Commands you will need

```bash
bash -n scripts/test-db.sh                       # script syntax
./scripts/test-db.sh check                       # → "engine arms: 12 rows, every package with arms listed", exit 0
./scripts/test-db.sh up                          # fb-mysql:13306, fb-pg:15432 (idempotent)
./scripts/test-db.sh postgres                    # all twelve rows on PostgreSQL
./scripts/test-db.sh mysql                       # all twelve rows on MySQL
```

Narrow, while working:

```bash
PG='postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'
FOREBRAIN_TEST_DATABASE_URL="$PG" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 \
  go test -tags fts5 ./pkg/run -count=1 -run 'Subagent|AttemptLedger|ImageRefusal|MemoryCitation'
FOREBRAIN_TEST_DATABASE_URL="$PG" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 \
  go test -tags fts5 ./pkg/tui -count=1 -run 'WithdrawSurface|BindingANewConversation'
```

SQLite, for the other side of every change:

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui -count=1
```

Docker: each test creates a scratch database and InnoDB/PostgreSQL do not return
the space, so the volume grows. If `docker` starts timing out or the disk fills:
`docker rm -f -v fb-mysql fb-pg && ./scripts/test-db.sh up`. Run the two engine
legs **serially** — they share the containers.

## Scope

**In scope**:
- `pkg/run/subagent_test.go`, `pkg/run/attempt_ledger_test.go`,
  `pkg/run/image_input_test.go`, `pkg/run/memory_llm_test.go` (the seven literals
  and the `dbx` import)
- `pkg/tui/chat_session_test.go` (the two literals and the `dbx` import)
- `scripts/test-db.sh` (the two `engine_arms` rows)

**Out of scope**:
- Any production file. The store's own statements are `dbx.SQL` arms and are
  already correct; a change here would be a second fix for one defect.
- A new architecture rule that scans `*_test.go` for raw placeholders. It is
  tempting and it does not work: one test file routinely holds both kinds of
  test — `pkg/tui/chat_session_test.go` opens through
  `state.OpenStateForTest` (`:68`, `:130`, `:331`, …) *and* through
  `OpenChatSessionWithConfigForProject`, which pins SQLite
  (`TestBindingANewConversationUsesTheProjectsType` proves it: its `?` statement
  passes on PostgreSQL today). Any file-level rule would either flag the pinned
  tests or exempt the dialect-following ones. A function-level rule needs a
  data-flow answer no static check here can give. The matrix, run per package, is
  the guard (see Maintenance).
- The other 71 literals this sweep found across `pkg/state`, `pkg/process`,
  `pkg/migrate`, `pkg/gateway` and `pkg/turn` test files. They are listed under
  Maintenance as the known remainder; they are not red today because their tests
  open the database with `state.Open` (SQLite-pinned) or are not reached by a
  row. Fixing them is a separate batch, not this plan.
- `pkg/state/dbx/*` — `Rebind` and `DialectOf` are correct as they are.

## Steps

### Step 1: rebind the seven `pkg/run` literals

In each file add `"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"`
to the import block (alphabetical, beside `pkg/state`), then wrap the literal.
The statement text does not change — only the call that hands it over.

`pkg/run/subagent_test.go:220`:

```go
	if err := db.QueryRow(dbx.Rebind(dbx.DialectOf(db),
		`SELECT source, parent_session_id FROM fb_sessions WHERE id = ?`), prep.entry.WorkerSessionID).
		Scan(&source, &parent); err != nil {
```

`pkg/run/subagent_test.go:4215` (`seedWorker`):

```go
		if _, err := f.store.DB().Exec(dbx.Rebind(dbx.DialectOf(f.store.DB()),
			`INSERT INTO fb_runs(id, session_id, parent_run_id, input_text, status, owner, created_at, updated_at)
			VALUES('seed-run', ?, NULL, 'prior execution', 'done', '', 1, 1)`), conversationID); err != nil {
```

`pkg/run/subagent_test.go:5558` (`countConversationMessages`):

```go
	if err := store.DB().QueryRow(dbx.Rebind(dbx.DialectOf(store.DB()),
		`SELECT COUNT(*) FROM fb_messages WHERE session_id=?`), sessionID).Scan(&n); err != nil {
```

`pkg/run/attempt_ledger_test.go:200`:

```go
	if err := db.QueryRowContext(ctx, dbx.Rebind(dbx.DialectOf(db),
		`SELECT usage_llm_calls, usage_prompt_tokens, usage_completion_tokens FROM fb_runs WHERE id=?`), runID).
		Scan(&llmCalls, &promptTokens, &completionTokens); err != nil {
```

`pkg/run/image_input_test.go:408`:

```go
	if err := db.QueryRowContext(ctx, dbx.Rebind(dbx.DialectOf(db),
		`SELECT COUNT(*) FROM fb_session_image_refusals WHERE session_id=?`), sid).Scan(&rows); err != nil {
```

`pkg/run/memory_llm_test.go:325` and `:340`:

```go
	if _, err := ms.DB.ExecContext(context.Background(), dbx.Rebind(dbx.DialectOf(ms.DB),
		`INSERT INTO fb_sessions(id,agent_id,title,updated_at,created_at,memory_mode) VALUES(?,?,?,?,?,?)`),
		canonical, "main", canonical, 1, 1, memory.ThreadMemoryEnabled); err != nil {
```
```go
	if err := ms.DB.QueryRowContext(context.Background(), dbx.Rebind(dbx.DialectOf(ms.DB),
		`SELECT COALESCE(usage_count,0) FROM fb_memory_stage1_outputs WHERE thread_id=?`), canonical).Scan(&usage); err != nil {
```

Every handle here is a `*sql.DB`, which is what `DialectOf` answers for. Call
`DialectOf` on the same handle the statement runs on — not on a different
connection, and not on `state.OpenStateForTest`'s path string.

**Verify**, with PostgreSQL:

```
FOREBRAIN_TEST_DATABASE_URL="$PG" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 \
  go test -tags fts5 ./pkg/run -count=1 -run 'Subagent|AttemptLedger|ImageRefusal|MemoryCitation'
```
→ `ok` (before this step: the failures quoted above — 34 in `./pkg/run` on a full run).

### Step 2: rebind the two `pkg/tui` literals

**Coordination first.** `pkg/tui` is a busy area — the peer-card session works
in this same tree (its changes had landed, committed, at the reviewed baseline;
more may be in flight when you start). Before touching the file run
`git status --porcelain -- pkg/tui` and `git diff -- pkg/tui/chat_session_test.go`.
If `chat_session_test.go` carries foreign uncommitted edits, message that session
(`peer_list`, then `peer_send`) naming this plan and the two lines, and either
wait for its go-ahead or land only Step 1 and report Step 2 as deferred. Never
revert, stash or rewrite its edits.

`pkg/tui/chat_session_test.go:2338` and `:6316`:

```go
	require.NoError(t, s.Env.SQL.QueryRowContext(ctx,
		dbx.Rebind(dbx.DialectOf(s.Env.SQL), `SELECT COUNT(*) FROM fb_sessions WHERE id=?`), sid).Scan(&rows))
```
```go
	if err := s.sessStore().DB().QueryRow(
		dbx.Rebind(dbx.DialectOf(s.sessStore().DB()),
			`SELECT COUNT(*) FROM fb_messages WHERE session_id=? AND visibility='withdrawn'`),
		sessionID).Scan(&audited); err != nil {
```

Add the `dbx` import to that file's block (beside `pkg/state`).

**Verify**:

```
FOREBRAIN_TEST_DATABASE_URL="$PG" FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 \
  go test -tags fts5 ./pkg/tui -count=1 -run 'WithdrawSurface|BindingANewConversation'
```
→ `ok`. `TestWithdrawSurfaceTurnLeavesNoTrace` is the red one today
(`chat_session_test.go:6319: audit read: ERROR: syntax error at or near "AND"`);
`TestBindingANewConversationUsesTheProjectsType` passes before and after, which is
the point — its harness pins SQLite, so the rebind is a no-op there and the test
is not evidence either way.

### Step 3: make the rows reach the tests they claim to cover

In `scripts/test-db.sh`, in `engine_arms`, extend two rows:

```bash
  "./pkg/run|Subagent|Runner|Config|Skills|Compaction|Permissions|AttemptLedger|Transcript|ImageRefusal|MemoryCitation"
  "./pkg/tui|RecordedLikeTheGateway|PrimaryBudget|SurfaceToolApproval|WithdrawSurface|BindingANewConversation"
```

Both fragments must match at least one test name, or the row's non-zero-match
guard fails the leg — that is the guard 245 installed working as intended.
`ImageRefusal` matches three tests, `MemoryCitation` six, `WithdrawSurface` and
`BindingANewConversation` one each (measured on this tree). If any fragment turns
out to match nothing (a rename since this plan was written), drop that fragment
and say so in the report rather than widening the row some other way.

**Verify**:
```
bash -n scripts/test-db.sh && ./scripts/test-db.sh check
```
→ `engine arms: 12 rows, every package with arms listed`, exit 0.

### Step 4: the two engine legs, serially

```
./scripts/test-db.sh postgres
./scripts/test-db.sh mysql
```

Expected: each leg prints twelve `== ./pkg/… on <engine>` lines, each with
`   <n> tests matched` where n > 0, and exits 0. This is the first green
`postgres` leg since 245 landed.

Then the SQLite side, so the rebinds are proved to change nothing there:

```
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui -count=1
```

## Test plan

There is nothing to unit-test here: the change is to tests, and the reproduced
failure **is** the failing case. The proof is therefore the red→green of the
existing suite on the engine that rejects the statement:

- **Red before**: the four failure blocks quoted in "Why this matters" — run them
  before Step 1 and keep the output. Record the counts: `34` failing tests on a
  full `./pkg/run` against PostgreSQL (of which `21` are inside 245's row
  pattern), `1` for `TestImageRefusalRecorderIsIdempotent`, `1` for
  `TestWrapMemoryCitationLLM_UsageAccountingAcceptsOnlyCanonicalUUIDs`, `1` for
  the `pkg/tui` withdrawal test.
- **Green after**: Step 1's and Step 2's verify commands, then Step 4's legs.
- **Negative control (mandatory)**: after everything is green, revert exactly one
  rebind — the `seedWorker` insert (`subagent_test.go:4215`) is the highest-value
  one, since it seeds a large part of the package — re-run Step 1's command, and
  confirm the failures return as `SQLSTATE 42601` at the callers' `t.Fatal(err)`
  (for example `subagent_test.go:4888`). Restore the rebind; confirm green again.
  Do the same for the `pkg/tui` withdrawal statement with its verify command.
- **Clean-text control**: the full `./pkg/run` and `./pkg/tui` suites on SQLite
  (Step 4) — `Rebind` is a no-op for SQLite, so nothing about those runs may
  change.

## Done criteria

ALL must hold:

- [ ] `FOREBRAIN_TEST_DATABASE_URL=<postgres> FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` passes — the **whole package**, not a `-run` slice
- [ ] `FOREBRAIN_TEST_DATABASE_URL=<postgres> … go test -tags fts5 ./pkg/tui -count=1 -run 'WithdrawSurface'` passes
- [ ] `./scripts/test-db.sh postgres` exits 0 with twelve rows and every row's test count > 0
- [ ] `./scripts/test-db.sh mysql` exits 0, same
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tui -count=1` passes on SQLite
- [ ] `go build ./...` and `go vet ./...` exit 0
- [ ] `gofmt -l` on the five touched test files prints nothing
- [ ] The negative control was observed red, then restored to green
- [ ] `grep -c 'dbx.Rebind(dbx.DialectOf' pkg/run/{subagent,attempt_ledger,image_input,memory_llm}_test.go pkg/tui/chat_session_test.go` shows 3/1/1/2/2
- [ ] Only in-scope files modified (`git status --porcelain`); `pkg/tui` carries no foreign edit of yours
- [ ] `plans/README.md` row for 247 — it already exists in the status table as
  `TODO` — updated to `DONE`

## STOP conditions

- A rebind changes what the statement means: `Rebind` rewrites every `?` outside
  a single-quoted literal, so a statement with a legitimate `?` operator (a JSONB
  existence test, a `LIKE` pattern holding `?`) must not be rebound — report it
  and leave that one alone.
- A `pkg/tui` file carries another session's in-flight edit to the lines this plan
  touches: report and defer Step 2 (Step 1 stands on its own; the plan is still
  partly delivered and says so).
- Widening a row pulls in a test that fails on an engine for a reason this plan
  did not cause (a second, unrelated dialect defect, or a test another session is
  editing): keep the row, report the failing test — that is a new finding, not a
  reason to narrow the pattern back. Narrowing it would re-create the invisible
  coverage this plan exists to remove.
- `./scripts/test-db.sh postgres` fails in `pkg/process`, `pkg/state`, `pkg/memory`
  or another package whose tests this plan does not touch: report the failing
  test and the engine, and say which rows are therefore unverified. Do not fix a
  package outside Scope.
- The Docker containers cannot start (no Docker, low disk): run `bash -n`,
  `check`, and both SQLite suites; report the two engine legs as **not run** and
  name the criteria that stay unverified. Do not fake them.

## Maintenance notes

- **The rule**: a test that runs on whichever engine
  `FOREBRAIN_TEST_DATABASE_URL` names must not hand a `?`-placeholder statement
  to a query method. Either wrap it in
  `dbx.Rebind(dbx.DialectOf(<same handle>), …)`, or write a `dbx.SQL` arm when the
  statement is shared with production code. A statement with only literal values
  needs neither.
- **What guards it now**: the matrix, run per package. `./scripts/test-db.sh
  postgres` executes every row and fails on the first red statement, so a new raw
  `?` inside a row's pattern turns the gate red immediately. What it cannot do is
  see a test its pattern does not match — which is why Step 3 widened two rows and
  why the next person to add a test with raw SQL to a package that has a row owes
  either a like-for-like `Rebind` or a row fragment.
- **The known remainder** (reported, deliberately not fixed here; 71 literals in
  test files, found by an AST sweep of `pkg/**/*_test.go` on `c7f8f5f4`): the bulk
  sit in `pkg/state/schema_migrations_test.go` (34), `pkg/process/cron_service_test.go`
  (15), `pkg/state/session_events_test.go` (9) and `pkg/state/rewind_test.go` (6),
  with singles in `pkg/state/db_test.go` (1), `pkg/process/open_test.go` (3),
  `pkg/process/config_import_test.go` (1), `pkg/process/config_db_test.go` (1) and
  `pkg/architecture/dialect_coverage_test.go` (1 — a scanner fixture, never
  executed). They pass today because their tests open the database with
  `state.Open` (SQLite-pinned — the migration and session-event suites build
  SQLite files on purpose) or are outside every row's pattern. Before widening a
  row, run that package on PostgreSQL first; a `SQLSTATE 42601` is this defect,
  not a new one.
- **Why `Rebind` and not a shared helper**: three packages already spell it this
  way at the call site, and a helper would have to be exported from a library
  package for tests of five packages to reach it — a new public surface for a
  private convenience. If a sixth package needs it, revisit.
- A reviewer should check that `DialectOf` is called on the same handle the
  statement runs on, that no statement text changed, that no `pkg/tui` line
  belonging to another session was touched, and that the two widened rows still
  name fragments that match real tests.
