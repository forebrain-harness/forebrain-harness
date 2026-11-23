# Plan 242: the store repairs every text column it writes, so a message row cannot be refused

> **Executor instructions**: Follow the steps in the order given; confirm each
> verification. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and
> report it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- pkg/state/session_store.go pkg/state/message_sync.go pkg/state/message_parts.go pkg/state/usage.go pkg/state/output_history.go pkg/state/dbx/text.go`
> Compare the "Current state" excerpts with the live code; content mismatch is a
> STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED — the repair is idempotent on clean text (its fast path returns
  the same string), but it now runs on every insert, so the tests must prove that
  a clean row is byte-identical to today's.
- **Depends on**: plan 228 (the helper and the policy this plan completes);
  plan 245 makes the MySQL/PostgreSQL legs runnable in one command
- **Category**: bug (state, data loss)
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Finding**: branch audit 2026-11-05 (`origin/dev..HEAD`), finding 5
  (`introduced` by plan 228 — the promise was "the store repairs what an engine
  refuses", the implementation covers five call sites, not the store)

## Why this matters

MySQL 8 refuses invalid UTF-8 (error 1366) and PostgreSQL refuses it plus a NUL
(SQLSTATE 22021). Plan 228 added `dbx.ValidText` / `dbx.ValidJSONText` to repair
that before an engine sees it — and wired them into five places:
`message_sync.go:104` (the reconciler's content), `output_history.go:175-176`
(two output columns), `session_events.go:207` (event payload JSON) and
`message_parts.go:338` (the parts document).

The **main** write path is not among them. `AppendMessageForRun` and
`AppendStructuredMessageForRun` put `content` (and `tool_meta_json`) into the row
unrepaired, and both are the call sites every surface uses for a user turn
(`pkg/turn/session.go:315`) and every display row. Consequences on a server
engine:

- a user pastes a GBK filename or a `\x00` into the composer → the row is refused
  → the turn's message is lost, and the failure surfaces as a generic store error;
- the `content` column is repaired while `parts` is repaired *differently*
  (`json.Marshal` writes one U+FFFD **per invalid byte**, `strings.ToValidUTF8`
  collapses a run into **one**) → the same message has two texts in one row.
  `ParseMessage` reads `parts`; title generation, `lastAssistantTextSQL` and the
  undo path read `content`;
- the transcript reconciler then compares a repaired stored row against an
  unrepaired canonical message (`message_sync.go:348`), never matches, and either
  skips the in-place completion it was asked to do or replays the tail.

The same hole exists in the legacy output-history import (`output_history.go:711-720`),
where a single bad byte rolls back the whole import transaction — and
`OpenOutputHistory` only logs a warning, keeps the file, and retries the same
failing import on every start.

## Current state

`pkg/state/dbx/text.go:37-43` — the repair (already written; do not change it):

```go
func ValidText(s string) string {
	if utf8.ValidString(s) && !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, ReplacementText), "\x00", ReplacementText)
}
```

`dbx/text.go:54-…` — `ValidJSONText([]byte) []byte`, which additionally rewrites
the `\u0000` escape PostgreSQL's jsonb/json lexer refuses.

`pkg/state/session_store.go:3031-3049` — **the one choke point** every append goes
through (`appendRow` → `appendRowInTx` → `insertMessage`; `AppendMessageForRun`
and `AppendStructuredMessageForRun` both call `appendRow`):

```go
func insertMessage(ctx context.Context, q dbtx, row messageRow) (int64, error) {
	execStarted, execFinished, execDuration := row.Exec.columns()
	args := []any{row.SessionID, nullIfEmpty(row.RunID), row.Role, row.Visibility, row.Content, row.PartsJSON, row.MessageID, row.Model, row.UsageJSON, row.ToolStepID, row.ToolMetaJSON,
		execStarted, execFinished, execDuration, row.CreatedAt}
```

`pkg/state/session_store.go:3223-3238` — the in-place completion, also unrepaired:

```go
	res, err := q.ExecContext(ctx, updateMessageContentAndPartsSQL.For(dbx.DialectOf(q)),
		strings.TrimSpace(content), partsJSON, rowID, s.AgentID())
```

`pkg/state/session_store.go:1076-1079` (`ensureSession`) and `:1435` (`SetTitle`)
— the session title is written as given.

`pkg/state/usage.go:116-127` — the parts builder that makes `parts` diverge from
`content`:

```go
		case llm.ContentTypeText:
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": part.Text,
				})
			}
```

`pkg/state/message_sync.go:344-350` — the comparison that reads `parts` text:

```go
func messageText(msg llm.Message) string {
	return strings.TrimSpace(llm.TextContent(msg.Parts...))
}
```

`pkg/state/output_history.go:172-178` and `:711-720` — the two output paths:

```go
	e.OriginalOutput = dbx.ValidText(e.OriginalOutput)
	e.FilteredOutput = dbx.ValidText(e.FilteredOutput)
	args := []any{
		e.Kind, e.Command, e.OriginalOutput, e.FilteredOutput,
```

```go
		if _, err := tx.ExecContext(ctx, insert,
			e.ID, e.Kind, e.Command, e.SessionID, e.OriginalOutput, e.FilteredOutput,
```

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` | `ok` |
| MySQL leg | `./scripts/test-db.sh mysql` (after plan 245; otherwise `FOREBRAIN_TEST_DATABASE_URL=mysql://root:fb@127.0.0.1:13306/fbt FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run 'Repair|Sync|Parts|Output' -count=1`) | `ok` |
| Postgres leg | same with `postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable` | `ok` |

Run the legs **serially**; they share two Docker containers and each test creates
its own scratch database.

## Scope

**In scope**:
- `pkg/state/session_store.go` (`insertMessage`, `UpdateMessageContentAndParts`,
  `ensureSession`'s title argument, `SetTitle`)
- `pkg/state/usage.go` (`contentBlocksJSON`'s text branch)
- `pkg/state/message_sync.go` (`messageText` normalisation)
- `pkg/state/output_history.go` (`Save`, `insertImportedRecords`)
- `pkg/state/*_test.go` for the above

**Out of scope**:
- `pkg/state/dbx/text.go` — the helper is correct; changing how it collapses
  invalid bytes would rewrite every caller's policy.
- `pkg/llm/truncate.go`, `pkg/tool/display_header.go` — they repair their own
  output for their own reasons.
- The **read-side** `::jsonb` casts (`session_store.go`'s `tool_meta_json`
  predicates): they only fail on rows written before this change; repairing those
  needs a migration, which plan 228 deliberately deferred.
- Any change to what `ParseMessage` returns.

## Steps

### Step 1: repair at the insert choke point

In `insertMessage`, repair the three columns that carry text from outside the
process, before building `args`:

```go
	// The store is the one place every writer passes through, so the repair
	// lives here rather than in each caller (plan 228): content is prose from a
	// tool or a person, and parts/tool_meta_json are JSON documents that can
	// carry a NUL escape. Both are idempotent on clean input.
	content := dbx.ValidText(row.Content)
	partsJSON := string(dbx.ValidJSONText([]byte(row.PartsJSON)))
	toolMetaJSON := string(dbx.ValidJSONText([]byte(row.ToolMetaJSON)))
```

and use those three in `args` instead of `row.Content`, `row.PartsJSON`,
`row.ToolMetaJSON`. Leave the other columns (`MessageID`, `Model`, `UsageJSON`)
alone — they are machine-generated. `insertMessage` is the one choke point: the
two callers that reach it directly instead of through `appendRow`
(`session_store.go:3573` and `:3654`) share this repair with no extra edit.

**Verify**: `go build ./...` → exit 0

### Step 2: repair the in-place completion and the titles

- `UpdateMessageContentAndParts`: repair `strings.TrimSpace(content)` with
  `dbx.ValidText` and `partsJSON` with `dbx.ValidJSONText` **after** the
  `json.Valid` check's premise (repair first, then validate — the repair can only
  make the document valid).
- `ensureSession` (`:1079`): pass `dbx.ValidText(title)`.
- `SetTitle` (`:1435`): `title = dbx.ValidText(title)` next to the existing
  `strings.TrimSpace`.
- `generateSessionTitle` (`session_store.go`, immediately after
  `SessionTitleFromContent`): repair **here**, not in each caller. It is the one
  place every title string is built — the auto-naming statement
  (`nameSessionFromTurnSQL`, `:2937` area) writes `fb_sessions.title` straight
  from it, and MySQL reports that column as its own 1366 once `content` is
  repaired. It also owns the `[]rune` truncation, which turns every invalid byte
  into its own U+FFFD; repairing first keeps the title the same text the message
  row holds (one replacement per run) instead of a per-byte expansion.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'Title|Sync|Parts' -count=1` → `ok`

### Step 3: make `content` and `parts` agree

In `contentBlocksJSON` (`usage.go:124`) use the repaired text:

```go
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": dbx.ValidText(part.Text),
				})
```

(`pkg/state/usage.go` does **not** import `dbx` today — add it.)

Then in `message_sync.go`, normalise the comparison side too:

```go
func messageText(msg llm.Message) string {
	return dbx.ValidText(strings.TrimSpace(llm.TextContent(msg.Parts...)))
}
```

This is what makes `messagesAlign` (`:321-346`) compare repaired-to-repaired: a
row this store wrote can no longer look "different from" the message that
produced it. Note the repair lands in `messageText` itself, so its three other
callers inherit the same normalisation — `assistantRowNeedsCompletion` (`:284`),
`messageMatchKeys` (`:395`) and `fuzzyUserMatch` (`:428`) — which is the point:
every one of them compares against stored rows. If any of them matches
*differently* after this change, that is a finding to report, not a call site
to special-case.

**Verify**: `go build ./...` → exit 0

### Step 4: repair the output-history command and its import

- `Save` (`:175`): add `e.Command = dbx.ValidText(e.Command)` beside the two
  existing repairs.
- `insertImportedRecords` (`:711`): repair each record's text **per row**, so one
  bad legacy record cannot roll back the batch:

```go
		if _, err := tx.ExecContext(ctx, insert,
			e.ID, e.Kind, dbx.ValidText(e.Command), e.SessionID,
			dbx.ValidText(e.OriginalOutput), dbx.ValidText(e.FilteredOutput),
```

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'Output' -count=1` → `ok`

## Test plan

Model the store-level tests on the existing plan-228 tests
(`pkg/state/message_sync_test.go`'s repair case, `pkg/state/dbx/text_test.go`).

1. `TestAppendMessageForRunRepairsContent` — append a message whose `Content` is
   `"a\xff\xffb"` **and** whose `PartsJSON` is a document carrying `\u0000`;
   assert `err == nil`, and read the row back: `content` is `a` + one U+FFFD +
   `b`, and the parts text equals the content.
   This is the regression — before step 1 the assertion on the raw bytes fails.
2. `TestAppendStructuredMessageRepairsParts` — same through
   `AppendStructuredMessageForRun` (the real composer path,
   `pkg/turn/session.go:315`).
3. `TestMessageTextComparesRepairedRows` — build a canonical message with invalid
   bytes, store it, then call `AppendMessageSequenceForRun` again with the same
   canonical list: assert no duplicate row was appended and the in-place
   completion happened (row count unchanged). Before step 3 this appends a
   second copy.
4. `TestOutputHistoryRepairsCommand` and `TestOutputHistoryImportsARepairableLegacyRow`
   — one record whose `Command`/`OriginalOutput` carry invalid bytes; assert the
   row lands repaired instead of the import failing.
5. Clean-text control: a message with ordinary prose round-trips byte-identically
   (asserted in the same test file).

Then run the legs in "Commands you will need". On SQLite these tests pass before
the fix (SQLite stores the bytes); the **behavioral** proof of the fix is that
MySQL/PostgreSQL accept the row.

## Done criteria

ALL must hold:

- [ ] `go build ./...` and `go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... -count=1` passes
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` passes
- [ ] MySQL and PostgreSQL legs pass the new tests (commands above), and the run
      is reported with the engine named
- [ ] `grep -n 'dbx.ValidText(row.Content)' pkg/state/session_store.go` matches
      exactly once (the choke point)
- [ ] A clean message round-trips unchanged (asserted)
- [ ] Only in-scope files modified (`git status --porcelain`)
- [ ] `plans/README.md` row for 242 updated

## STOP conditions

- The store's clean fast path is not actually allocation-free / returns a changed
  string for clean text: report it — every append would then rewrite bytes.
- A test shows `content` and `parts` still disagree for a case this plan does not
  cover (e.g. a message with two text parts): report the case instead of adding a
  join rule; the rule "one text, one repair" is what the plan installs.
- The MySQL/PostgreSQL containers are unavailable: run the SQLite suite, report
  the legs as **not run**, and say which done criteria are therefore unverified.
  Do not fake them.
- Repairing the title changes session naming in a way an existing test pins (e.g.
  a title with an emoji): report the test rather than widening the repair.

## Maintenance notes

- The invariant: **every text column that can carry tool or model output is
  repaired where the row is written, not where the caller happens to remember.**
  A new text column inherits this by being added to `insertMessage` and to the
  update helpers.
- The tests live in `pkg/state/message_sync_test.go`, not in a file of their own:
  `pkg/architecture`'s `TestTestFilesCorrespondToProductionFiles` requires
  `X_test.go` to sit beside a production `X.go`, and there is no `store_repair.go`
  (the tests exercise several existing files, and a production file invented to
  satisfy the rule would be worse). Same reason plan 228's `text_repair_test.go`
  was folded into that file.
- A reviewer should check the repair order (repair → validate), that `ValidText`
  is not applied twice in a way that changes clean text (it is idempotent), and
  that the `content`/`parts` equality test really compares the two columns.
- Deliberately deferred: a migration repairing rows already stored with bad bytes
  (plan 228 deferred it; the read-side casts still fail on such rows), and the
  `::jsonb` predicate rewrite.
