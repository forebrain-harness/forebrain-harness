# Plan 228: text that an engine refuses is repaired before it is stored, and a failed save is not silent

> **Executor instructions**: Follow the steps in order. If a STOP condition
> occurs, stop and report. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 7d505aa3..HEAD -- pkg/state/message_sync.go pkg/state/output_history.go pkg/state/session_events.go pkg/state/message_parts.go pkg/tool/shell_output.go pkg/turn/session.go pkg/gateway/server.go pkg/tui/chat_session.go`
> Content mismatch is a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW — clean text is unaffected; the change only replaces bytes the
  server rejects outright.
- **Depends on**: none, but it shares `pkg/state/session_events.go` with plan
  232. If 232 has already landed when you start, the drift check reports that
  file; re-anchor the excerpts instead of stopping, and do not revert 232's
  transaction wrapping.
- **Category**: bug (data loss)
- **Planned at**: commit `7d505aa3`, 2026-11-04
- **Finding**: branch audit 2026-11-04, finding 7 (`introduced` by plans 209–212);
  found by a subagent, verified here against the code

## Why this matters

The state database now runs on MySQL 8 or PostgreSQL as well as SQLite. Both of
the former reject text SQLite stores happily:

- invalid UTF-8 bytes (MySQL error 1366 in strict mode, PostgreSQL SQLSTATE 22021);
- a NUL byte inside a value (`\u0000` in a JSON payload cast to PostgreSQL's
  `jsonb` is SQLSTATE 22P05).

Tools produce exactly that. `read_file` only refuses a file containing a NUL in
its first 8 KiB, so a GBK/Shift-JIS/Latin-1 text file is read as raw bytes; shell
output is never repaired at all (`git ls-files -z`, `find -print0`, a program
printing binary). Nothing between the tool and the store normalises the text:
`pkg/llm/truncate.go:26` repairs only its own truncation path.

The consequence is worse than one bad row. `AppendMessageSequenceForRun` writes a
run's messages in **one transaction** (`pkg/state/message_sync.go:46-60`), and its
callers throw the error away — `pkg/turn/session.go:191`, `pkg/gateway/server.go:2778`,
`pkg/tui/chat_session.go:2116` (`_ = …AppendMessageSequenceForRun(...)`). So the
first time a conversation contains such text, the whole transaction rolls back,
the message stays in memory, and **every later save of that conversation fails
the same way**: the conversation stops being persisted for good, without a word
to the user. On SQLite the same bytes round-trip fine, which is why this has not
been seen on the default engine.

## Current state

`pkg/state/message_sync.go:112-142` — the content goes into the store unchanged:

```go
	content := strings.TrimSpace(msg.TextContent())
	...
			Content:    content,
			PartsJSON:  MessagePartsJSON(msg, content),
```

`pkg/state/schema_mysql.sql:162-163` — the columns:

```sql
  content LONGTEXT NOT NULL,
  parts LONGTEXT NOT NULL DEFAULT ('[]'),
```

`pkg/tool/file_tools.go:665-680` — the read gate for binary **content**
(`looksLikeBinaryContent`: the first 8 KiB are checked for a NUL byte); the
extension gate `isBinaryFileExtension` is a separate check, applied together with
it at `file_tools.go:354`. Neither repairs non-UTF-8 bytes, so `read_file` hands
them on unchanged:

`pkg/tool/shell_output.go:600-612` — the compressed output and its history row;
`pkg/state/output_history.go:170-183` writes the original and filtered text with
no repair, and the failure is invisible because the retrieval id is only used when
`err == nil && id > 0`:

```go
		if err == nil && id > 0 {
			meta.RetrieveID = id
			res.Output = res.Output + "\n" + MarkerText(res.CompressedPct, res.SavedTokens, id)
		}
```

`pkg/state/session_events.go:462-486` — the PostgreSQL arms cast the JSON text to
`jsonb`, which is where a NUL in a tool payload becomes a hard error on that
engine only:

```go
	case dbx.Postgres:
		return `event_type='tool_call_completed'
  AND COALESCE(payload_json::jsonb->>'tool_name','') <> 'request_permissions'
  ...
```

and `jsonFieldText` (`:475-486`) returns `column + `::jsonb->>'` + field + `'`` for
PostgreSQL. The same cast appears in `pkg/state/action_service.go:272`.

`pkg/turn/session.go:189-193` — the swallowed error:

```go
	if len(toPersist) > 0 {
		_ = store.AppendMessageSequenceForRun(
```

Verified: `grep -rn "ToValidUTF8\|ValidString" pkg` finds repairs only in
`pkg/llm/truncate.go`, `pkg/llm/explain.go`, `pkg/tui/reducer.go`,
`pkg/tui/channels.go`, `pkg/turn/batch.go`, `pkg/tool/display_header.go`,
`pkg/tool/web_tools.go` — never at the persistence boundary.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tool -count=1` | `ok` |
| Callers | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` | `ok` |
| MySQL leg | the explicit `FOREBRAIN_TEST_DATABASE_URL` command in step 5 (the script's `Dialect/Schema/Open` filter skips the new tests) | `ok` |
| Postgres leg | same command with the postgres URL | `ok` |

## Scope

**In scope**:
- `pkg/state/message_sync.go`
- `pkg/state/output_history.go`
- `pkg/state/session_events.go` (the JSON payload repair)
- `pkg/state/message_parts.go` (the parts / `tool_meta_json` JSON repair — the
  marshal choke point is the `json.Marshal(parts)` that returns the stored
  `parts` string, around `:330`)
- one new file `pkg/state/dbx/text.go` (or the same helper in `pkg/state/text.go`
  — pick one and say which)
- `pkg/turn/session.go`, `pkg/gateway/server.go`, `pkg/tui/chat_session.go`
  (only replacing `_ =` with a logged error)
- `pkg/tool/shell_output.go` (only the failed history write's log line)
- the matching `_test.go` files

**Out of scope**:
- `pkg/tool/file_tools.go` — making `read_file` reject non-UTF-8 files is a
  behaviour change for every engine; the store-level repair is enough, and the
  model may legitimately want to see the bytes.
- `pkg/tool/shell_output.go`'s retrieval-id logic, beyond the log line.
- `pkg/llm/truncate.go`'s existing repair.
- **The read-side casts.** `pkg/state/session_store.go`'s
  `tool_meta_json::jsonb` predicates and `session_events.go`'s jsonb predicates
  only fail for rows written before this change. Repairing those rows needs a
  migration; the write side is what this plan guarantees.
- **Changing the JSON cast alone.** `payload_json::jsonb -> 'x'` on PostgreSQL
  fails for a `\u0000` escape anywhere in the document, and `::json` fails
  identically (`\u0000` is rejected by the shared JSON lexer, not by jsonb's
  storage form — measured on PG 14.24 on 2026-11-04). The fix is to stop writing
  the escape, not to change the cast.
- The schema: no column type or collation change. A `LONGTEXT`/`TEXT`/`text`
  column change belongs to plan 229's sibling work on collation, not here.
- History already stored: nothing is migrated. A row written before this change
  can still fail a PostgreSQL predicate; that is a follow-up, recorded in the
  maintenance notes.

## Steps

### Step 1 — reproduce on SQLite without a server

Add `TestStoreRepairsTextAnEngineWouldReject` to `pkg/state`: build a message whose
text contains (a) an invalid UTF-8 byte `"\xff"` and (b) a NUL `"\x00"`, append it
via `AppendMessageSequenceForRun`, read it back, and assert the stored text is
valid UTF-8 and contains no NUL.

To make the reproduction honest for MySQL/PostgreSQL too, the assertion must be
"the stored value is what the engine accepts", not "the value is unchanged":
write the test so it fails today on **the NUL part even on SQLite** if you choose
to normalise on all engines (recommended — see step 2), and record in the report
which engines you could run.

**Verify**: the test is **red** before step 2. SQLite stores both the bare NUL and
the invalid byte as they are, so **both** assertions fail there; MySQL and
PostgreSQL additionally reject the row server-side. If the SQLite half does not
fail, say so and rely on an engine leg.

### Step 2 — one repair function, applied at the store boundary

Add `dbx.ValidText(s string) string` (or `state.ValidText`, one place only):

1. `strings.ToValidUTF8(s, "\uFFFD")`;
2. replace `"\x00"` with `"\uFFFD"` (or drop it — pick one, document why; the
   replacement matches how the rest of the repo renders bad bytes);
3. leave `\n`, `\r`, `\t` and every other valid sequence untouched (the store
   must not start rewriting prose).

Apply it to: `message_sync.go`'s `content` (and anywhere the same value reaches
`PartsJSON`, since a NUL inside the parts JSON is the same problem), and
`output_history.go`'s original/filtered text. Apply it on **all three engines**,
so a conversation's stored bytes do not depend on which engine it ran on — say so
in the helper's comment.

**Verify**: step 1's test passes; `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → `ok`.

### Step 3 — repair the JSON the same way, and leave the cast alone

A NUL inside a `SessionEvent.Payload` reaches the store as the six-character JSON
escape `\u0000` (the payload is `json.RawMessage`, marshaled upstream before the
store sees it), and `tool_meta_json` has the same shape. Repairing only the plain
text columns leaves that escape in place.

Do **not** change `::jsonb` to `::json`: a `\u0000` escape is rejected by the JSON
lexer before either type's storage form matters, so `::json` fails identically
(measured on PostgreSQL 14.24, 2026-11-04). Extend the step-2 helper with a JSON
form, `ValidJSONText(raw []byte) []byte`:

1. `ToValidUTF8` and raw-NUL replacement, as for text;
2. walking the bytes, replace an **unescaped** `\u0000` escape with `\uFFFD`; when
   a backslash is followed by another backslash, copy both and continue, so a
   literal `\\u0000` (a backslash followed by the text `u0000`) survives
   unchanged.

Apply it to the payload in `session_events.go`'s insert and to the message-parts
JSON returned by `pkg/state/message_parts.go`'s marshal helper (`:330`) — that is
where `parts` and the `tool_meta_json` field inside it are produced, so one call
covers both. Leave `session_store.go`'s `tool_meta_json::jsonb` read predicates
alone: they only fail for rows written before this change, and repairing those is
a migration (see the maintenance note). Leave `action_service.go` alone too: it
has no cast of its own — it calls `jsonFieldText`
(`pkg/state/action_service.go:272`, implemented at `session_events.go:479-487`).

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1`
→ `ok`, including unit cases for `ValidJSONText` (an unescaped `\u0000` is
replaced, `\\u0000` is not, ordinary escapes survive) and a PostgreSQL test that
stores a tool-completed payload whose tool output contained a NUL, then lists tool
calls for that session and gets the row back.

### Step 4 — a failed save is logged, loudly

Replace the `_ =` sites **in the three in-scope files** with a `slog.Error`
carrying the session id and the error, and keep the call non-fatal (a save
failure must not abort the turn — that would be a behaviour change beyond this
plan). Sites: `pkg/turn/session.go:191`, `pkg/gateway/server.go:2778`,
`pkg/tui/chat_session.go:2116` and `:2163`. Use the package's existing logger
idiom (grep `slog.Error` in each file's package).

Five more product sites swallow the same error and are **out of scope**:
`pkg/run/subagent.go:1374`, `:2901`, `pkg/gateway/run_control.go:1611`,
`pkg/process/run_executor.go:372` (and the two test calls in
`pkg/run/subagent_test.go`). List them in your report so the reviewer sees the
remaining gap; do not edit those packages here.

In `pkg/tool/shell_output.go`, when the history write fails, log it as well: the
model then gets output without a retrieval marker, which is acceptable — silence
is not.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` → `ok`, and
`grep -n "_ = .*AppendMessageSequenceForRun" pkg/turn/session.go pkg/gateway/server.go pkg/tui/chat_session.go` returns nothing.

### Step 5 — the engine legs

`./scripts/test-db.sh mysql` / `postgres` run only
`./pkg/state/dbx/... ./pkg/state/ -run 'Dialect|Schema|Open'`
(`scripts/test-db.sh:52-58`); the new test names do not match, so the script
would print `ok` without running them. Start the servers, then run the tests
directly:

```bash
./scripts/test-db.sh up
for url in 'mysql://root:fb@127.0.0.1:13306/fbt' \
           'postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'; do
  FOREBRAIN_TEST_DATABASE_URL="$url" FOREBRAIN_HOME="$(mktemp -d)" \
    CGO_ENABLED=1 go test -tags fts5 ./pkg/state \
    -run 'TestStoreRepairs|TestValidText|TestValidJSON|TestPostgresToolPayload' -count=1
done
```

**Verify**: `ok` on both engines. If Docker is unavailable, mark both unverified
and say that the two-engine half of this finding is therefore unproven.

## Test plan

- New: `TestStoreRepairsTextAnEngineWouldReject` (step 1), a PostgreSQL
  tool-call-listing test whose payload held a NUL (step 3), and unit tests for
  `ValidText` / `ValidJSONText` covering: valid text unchanged; invalid byte
  replaced; NUL replaced; an unescaped `\u0000` replaced; `\\u0000` preserved;
  newlines/tabs preserved; a 4-byte emoji preserved.
- Structural pattern: existing `pkg/state` store tests that call
  `AppendMessageSequenceForRun` and read back.
- Failure paths: a save that fails is logged and the turn continues; a NUL in a
  tool payload does not break the session's tool-call listing on PostgreSQL.

## Done criteria

ALL must hold:

- [ ] `go vet ./...` exits 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/tool ./pkg/turn ./pkg/gateway ./pkg/tui -count=1` passes
- [ ] The engine legs in step 5 pass on MySQL and PostgreSQL, or both are reported unverified with the reason
- [ ] `grep -n "_ = .*AppendMessageSequenceForRun" pkg/turn/session.go pkg/gateway/server.go pkg/tui/chat_session.go` returns nothing (the out-of-scope sites are listed in the report)
- [ ] `grep -n "ValidJSONText" pkg/state/*.go` shows the helper and both call sites (session events, message parts)
- [ ] Stored valid prose round-trips unchanged (asserted in the `ValidText` test)
- [ ] Only in-scope files modified
- [ ] `plans/README.md` row for 228 updated

## STOP conditions

- Repairing at the store boundary makes a **golden** test fail because it pins
  exact stored bytes (grep the failure for a fixture file): report which fixture
  and why before regenerating it. Regenerating a golden must be its own decision.
- A caller depends on the NUL surviving (e.g. a test asserts byte equality with
  shell output): report it — that call site needs its own decision, not a silent
  normalisation.
- `::json` is **not** a fix for `\u0000` on PostgreSQL; the cast must stay as it
  is. The repair belongs on the write side.
- Docker is unavailable: do steps 1, 2, 4 and the parts of 3 you can test on
  SQLite, and say plainly that MySQL/PostgreSQL are unverified.

## Maintenance notes

- Every new text column that stores tool or model output inherits this: the repair
  belongs at the store boundary, not in each tool.
- A reviewer should check that valid prose is byte-identical after the change
  (the `ValidText` identity test), that the replacement character is the repo's
  usual one, and that no error is swallowed on this path again.
- Deliberately deferred: repairing text **already** stored on a MySQL/PostgreSQL
  install. Such rows cannot exist for the failing cases (the insert was rejected),
  but a row written before this change can still carry a `\u0000` escape and fail
  a PostgreSQL predicate that parses it. That needs a data migration (or a
  predicate that does not parse JSON) and its own plan; this plan only guarantees
  that nothing new is written in a form an engine refuses.
