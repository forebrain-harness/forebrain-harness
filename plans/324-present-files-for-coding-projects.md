# Plan 324: present_files for every project type, with a guarantee-level turn-end deliverable top-up

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and report — do not
> improvise. When done, update this plan's row in `plans/README.md` unless
> a reviewer told you they maintain the index.
>
> **Drift check (run first)**:
> ```bash
> git status --porcelain -- pkg/tool/registry.go pkg/tool/artifacts.go pkg/tool/artifacts_test.go pkg/tool/notification.go pkg/run/runner.go pkg/run/runner_test.go pkg/turn/submit.go pkg/turn/batch.go pkg/state/rewind.go pkg/state/artifact_store.go pkg/tui/reducer.go pkg/tui/notify.go pkg/tui/commands.go pkg/tui/chat_session.go pkg/tui/chat_turn.go pkg/agent/
> git diff 18acb26e -- <same paths>
> ```
> Expected: both print nothing. On a **content** mismatch with the excerpts
> below, STOP. Moved line numbers alone are not drift — re-locate anchors
> by the exact quoted strings. `pkg/tui/setup.go`/`setup_test.go` and
> `plans/config-tui-prototype.html` carry other agents' WIP: out of scope,
> their dirtiness is expected, never a STOP, never touch/revert them.

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED (shared turn engine + batch semantics + TUI reducer/replay)
- **Depends on**: none
- **Category**: feature (owner request)
- **Planned at**: commit `18acb26e` (dev), 2026-11-18 (rev 4 — supersedes
  rev 1/2/3 of this plan; rev 4 closes the failed-shell detection hole F1
  by owner ruling 「纳入本期根治」)

## Why this matters

Owner requirements (2026-11-18, settled across six messages):

1. coding must support `present_files`, semantics consistent with
   office/batch.
2. At turn end, EVERY file the turn created or modified in the project —
   source code or any type, regardless of what changed — must be listed as
   that turn's deliverables via present_files. Web must match TUI, same
   iteration. Reference: Qoder screenshot (per-file deliverable cards +
   edited-files diff summary card).
3. The coding system prompt MAY be modified per the requirement.
4. Mechanism stays consistent with office/batch = prompt-driven mandate.
5. PLUS a guarantee-level "change-set top-up" for **all three types**: at
   turn end the engine registers any turn-changed file not already
   presented. **All three types must root-cure "presented some, missed
   some"** (owner emphasized twice).
6. For batch, existing behavior may only be improved, never regressed.
7. 「纳入本期根治」 — the failed/cancelled-shell detection hole (F1) is
   closed in this iteration: a failed or cancelled command's writes must
   also be tracked and delivered.

Investigation + an independent pressure test established: the registration
gate is the only type-dependent point of the delivery pipeline; the
guarantee needs a shared engine epilogue in `turn.Service.Submit` (both
surfaces' canonical turn path); the change set is recoverable from two
durable sources (file-change ledger + raw shell-step diff payloads); the
TUI does **not** consume run-end frame artifacts today (live or replay) —
a real parity gap this plan closes; batch's zero-delivery trigger must be
re-expressed as "model presented nothing" so the top-up running first does
not suppress the outputs/ scan.

### Design rulings (do not deviate)

- **R1** Prompt-driven mandate (office/batch pattern) + engine top-up
  guarantee. No synthetic present_files tool step is ever emitted: the
  top-up registers `fb_artifacts` rows (`origin=fallback`, the existing
  vocabulary for engine-collected deliverables); surfaces show them in the
  same deliverables group. The LLM transcript is untouched.
- **R2** Coding's completeness rule (every touched file, no kind excluded)
  lives in the coding prompt as a type specialization. The shared tool
  description and ALL golden files stay frozen; office/batch prompts stay
  byte-identical.
- **R3** Batch non-regression: the outputs/ zero-delivery scan stays; the
  trigger becomes model-origin-count==0 (a deliberate improvement on the
  continue path, pinned by new trigger-level tests); the scan gains
  session-level same-sha dedup; batch's top-up mirrors the scan's dot-dir
  and `.work/` exclusions so scratch never becomes a deliverable.
- **R4** Guarantee contract = "every **tracked** turn change not already
  delivered is delivered". Tracked = file-change ledger ∪ shell changed
  paths **including failed and cancelled commands** (rev 4, Step 6b). The
  honest residual limits are enumerated in §Limits — do not silently
  expand scope to close them (each is a separate ruling).
- **R6** Failed/cancelled shell cards now truthfully show the changes the
  command actually made (files + hunks), where they previously showed
  none. No new "diff mechanism failed" wording is introduced — the owner
  ruling 「shell 卡片不得暴露 diff 机制自身失败」 holds: `Unavailable` stays
  reserved for genuine snapshot inability and renders as today.
- **R5** Web/TUI parity = same deliverable rows shown on both surfaces,
  live and replayed, deduped by artifact ID. Web needs zero code change;
  TUI gains the frame-artifact union (live + replay) and an ID on its
  deliverable shape.

## Current state (verified at 18acb26e; match by content)

### Gate — pkg/tool/registry.go:181-191

```go
	// Deliverables belong to the conversation, not the process: only office
	// and batch sessions register the tool, so a coding project's tool set is
	// exactly what it was.
	var tPresent *llm.Tool
	if rt != nil && (rt.ProjectType == state.ProjectTypeOffice || rt.ProjectType == state.ProjectTypeBatch) {
		tPresent, err = newPresentFilesTool(st, rt)
		…
		registerMeta(tPresent, "Use when your work produced final files the user should receive (download and preview cards)", "session", event.ToolMeta{ReadOnly: true, ConcurrencySafe: true})
	}
```

`if tPresent != nil { _ = st.Register(a, tPresent) }` (205-207) stays.
`AgentToolRuntime.ProjectType` comment (314-316) says "office and batch
conversations register present_files, coding keeps today's tool set".
Handler `pkg/tool/artifacts.go:205-314` never reads ProjectType;
primary-only guard `artifacts.go:428-433` + `pkg/tool/permissions.go:884`
+ `pkg/run/subagent.go:601`; approval exemption `pkg/safety/engine.go:452-458`.
`state.NormalizeProjectType` (`pkg/state/project_store.go:173`) maps ""→coding;
production always normalizes upstream (`pkg/process/runner_pool.go:849/1090`),
so "" is a test-only case, not a real production state.

### Tool comments — pkg/tool/artifacts.go:28-30 and 202-204

"registered only / for office and batch conversations" and "only wired up
for / office and batch conversations" — refresh both (Step 3). Description
constant 33-36 frozen in `pkg/tool/testdata/present_files_description.golden`.
`AddArtifact` always inserts a new row (`pkg/state/artifact_store.go:132-163`)
— re-presenting a path = new version, consistent with sha-compare dedup.

### Deliverable shape — pkg/tool/notification.go:42-46

`tool.Deliverable` carries Title/Name/Kind/RelPath/Size — **no ID**.
`appendDeliverables` (`pkg/tui/reducer.go:424-440`) dedups by
(RelPath,Size,Title). Web dedups by id (`frontend/src/lib/artifacts.ts:299-324`,
"merged by id, keeps first occurrence"). The present_files rows already
carry `"id"` (`artifacts.go:302-305`); `event.Artifact` has ID. Step 8 adds
ID to the shape so both surfaces dedup identically.

### Shared turn path — pkg/turn/submit.go:74-318

`Service.Submit` is called by **seven** sites: `pkg/tui/chat_session.go:2446`
(TUI normal), `pkg/tui/chat_turn.go:1003` (TUI approval-continue),
`pkg/gateway/server.go:2710` (webchat), `run_control.go:1545`,
`approval.go:608` (deny-continue), `api_extra.go:5217` (detached),
`channels.go:561` (channel). After `s.runner.Run` (:239): waiting-approval
(:249, interactive turn NOT over — but batch reaches it as terminal
`endBatch(failed,"approval_required")` at :255), cancelled (:265), failed
(:276), completed (:295). Each terminal branch calls `endBatch(...)` then
publishes the terminal RunEvent then returns. `endBatch` closure (:150-155)
uses `context.WithoutCancel(ctx)`. Session lock: `defer unlock()` at :112 —
the epilogue runs UNDER it (deliberate, see Step 5). `runID` may be "" if
the executor failed before creating a run (:157-161) — epilogue must skip.
Compaction/goal-check internal calls do NOT go through Submit (no
double-delivery). Auto-continue is a surface-submitted new turn with a new
root run (`submit.go:829` deliver → surface Submit).

### Batch epilogue — pkg/turn/batch.go

`endBatchExecution` (:276); :328-335 `if ListArtifactsPage(sid,1,0) total==0
→ collectBatchFallbackArtifacts`; :336 re-lists (session ASC, cap
`batchArtifactListLimit=500` :376) into `BatchExecutionEnd.Artifacts`
(:338-349 `batchOnEnd`). `collectBatchFallbackArtifacts` (:386-452): scans
`<workspace>/outputs/`, skips dot-dirs (:404-409) and non-regular files,
cap `batchFallbackFileLimit=200` (:379), `origin=fallback` (:447),
per-file errors logged+skipped. `hashArtifactFile` lives at
**pkg/turn/batch.go:456** (same package — reuse, zero new import).
`AdmitBatchTurn` (:160-167): a stopped execution continues in the SAME
session — so turn-1 fallback rows make turn-2 `total>0` today (scan
suppressed); the model-count trigger deliberately fires turn-2 scan.
**The pinned test `TestBatchFallbackArtifactsOnlyWhenNothingWasDelivered`
(batch_test.go:515-533) calls `collectBatchFallbackArtifacts` directly —
it never exercises the total==0 trigger and protects nothing about it.**
New trigger-level tests are required (Step 7).

### Change sources

- **Ledger**: `fb_file_changes` (`pkg/state/rewind.go`; schema
  `schema_migrations.go:485-505`, indexes session_anchor + session_path
  only — **no run_id index**). Columns: session_id, run_id, step_id,
  anchor_message_id, path, existed_before, before_snapshot, after_sha256,
  deleted. Recorded ONLY by file tools (`pkg/tool/file_tools.go:988` write,
  `:1236` edit, `:1842/1878/1884/1892` apply_patch) via `recordFileChange`
  (`pkg/tool/state.go:1910-1923`, stamps ctx session/run/step/anchor).
  `AllFileChanges` is per-session only (:217). Subagent runs share the
  primary session (`pkg/state/runrt.go:875-878` CreateSubagentRun); run-id
  stamping is mixed (`pkg/run/subagent.go:2237` parent vs `:1589/:3411`
  own) — both land inside the run tree. `fb_runs.parent_run_id` +
  recursive `run_tree` CTE exemplar: `pkg/state/llm_attempts.go:60-78`.
  Rewind deletes ledger rows by session+path between turns
  (`state/rewind.go:46-48,250`) — safe for the epilogue; the ledger is not
  a complete history.
- **Shell**: NOT in the ledger. `pkg/tool/shell_diff.go` computes changed
  files per command via a private git dir; `shellDiffRepoFor` (:296-307)
  returns **nil without a user git repo** (non-git projects: shell changes
  untracked). `unavailableDiff` (:783) still returns ChangedFiles paths.
  `shellDiffAbsPaths` caps at `shellDiffMaxChangedFiles`.
  `dropExternalWrites` (:1095-1107 + `tool_writes.go:162-163`) subtracts
  **this process's file-tool writes** in the command window — NOT other
  processes' writes (so ledger ∪ shell paths are near-disjoint, but
  external concurrent writes can be over-delivered).  **`finish()` (:1052-1093) early-returns nil at :1056-1059 on `ctx.Err() !=
  nil || runErr != nil || shellExitIsFailure(command, exitCode)`** — a
  failing/cancelled shell command's writes are invisible to both sources
  (F1; closed by Step 6b). The success tail (:1060-1092: release →
  skipped/unavailable guards → `diffSince` under repo.mu →
  `dropExternalWrites` → Shared/TreeMoved flags) is the shared computation
  Step 6b extracts. `shellExitIsFailure` exempts grep/git-diff exit 1
  (pinned at shell_diff_test.go:313-326 — those keep diffing, unchanged).
  `ShellEditDiff` rides in the shell tool step's RAW output payload;
  decode with `DecodeShellEditDiff` (`shell_diff.go:51`) /
  `ExtractTurnDiffPayload` (`pkg/turn/events.go:196`). **Binary/large files
  appear in `ChangedFiles` but NOT in the diff `Files`/hunks**
  (`selectRecord` :900-930) — generated xlsx/zip/png are exactly the
  skipped class. The `turn_diff_updated` companion event (persisted via
  `pkg/tui/chat_turn.go:292`, `pkg/gateway/approval.go:133`,
  `wsevents.go:143-146`) carries only `Files` — **never use it as the path
  source**. Read raw `tool_completed` output with
  **`ListRunEventsOfTypes`** (`pkg/state/session_events.go:339-360`, no
  LIMIT, rewind visibility filter) — NOT `ListRunEvents` (capped 500
  default / 5000 max, :313-318).

### Surfaces

- **Web** (zero change): `deliverablesOf` unions plan + presented steps +
  frame artifacts (`frontend/src/views/ChatView.vue:1777-1820`,
  `lib/artifacts.ts:312-324` merged by id), `ArtifactList` :398-403.
  Run-end frames attach `turn.RunArtifactsOf` (`pkg/turn/events.go:134`,
  reads fb_artifacts by run) at `pkg/gateway/server.go:1432`,
  `approval.go:177-185`.
- **TUI live**: `FrameDeliverables` group (`pkg/tui/reducer.go:68-72`)
  accumulates ONLY from primary present_files tool steps
  (`pendingDeliverables` :403-449, :2212-2218), emitted at turn close
  (:1495-1505, also on error/cancel :1503-1506), rendered :3128-3146/:3383.
  **The reducer never consumes RunArtifacts** (zero references).
  `RunEndedMsg` (`pkg/tui/notify.go:171-186`) has **no artifacts field**;
  real producers: `chat_session.go:2267` (normal), `chat_turn.go:944`
  (approval-continue), `:1352` (gate-cancel). Do NOT mount the user-shell
  producer `chat_session.go:2731` or empty flush `commands.go:1430`
  (synthetic runID reads empty). Run-end payloads already carry
  RunArtifacts (`notify.go:1296-1315`, `turn_lifecycle.go:25`;
  `event/run_events.go:252-275`).
- **TUI replay** (separate path): `renderReplayDeliverables`
  (`pkg/tui/commands.go:768-777`) draws from `takePendingDeliverables()`
  accumulated from transcript tool rows; replay's synthesized run-end has
  `ClosesTurn=false` (`notify.go:181-185`: "replay draws the group from the
  stored rows instead"). **No replay path consumes run-end RunArtifacts
  today** — top-up rows would be missing on replay without new plumbing.

### Prompt assembly + pins

`pkg/run/runner.go:1261-1283` coding branch: ScopeDiscipline +
NonFunctional + BugRootCause + GitWorktree + SubagentUserRuling, then
shared PlanHandoffQualityPrompt (:1282). Pinned exact concatenation:
`pkg/run/runner_test.go:2799` `wantCoding`. Prompt pieces are
one-file-one-constant with own tests; `advisor_prompts.go` /
`office_prompts.go` golden tests assert key-set equality
(`advisor_prompts_test.go:16-43`, `office_prompts_test.go:16-65`) — a new
constant in a NEW file keeps both goldens untouched. Office/batch prompt
isolation assertions exist in the same runner_test function (e.g.
GitWorktreePrompt ~:2825).

### Architecture constraints

- **pkg/turn fan-out is 12/12 FULL** (`pkg/architecture/cache_test.go:219`).
  `deliverables.go` may use ONLY imports turn already has (state, tool,
  event are present — verified). New SQL lives in pkg/state. Needing a new
  turn import → STOP.
- Every new SQL statement: `dbx.SQL` triple (SQLite/MySQL/Postgres) +
  regenerate `pkg/architecture` SQL snapshot (`sqlite_sql_test.go:12-22`,
  `TestSQLiteStatementsUnchanged`).
- Layering: turn(L3)→state(L1)/tool(L2)/event(L2) allowed; surfaces(L5)
  consume via ports. No new cross-layer imports.

## Commands you will need

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

| Purpose | Command | Expected |
|---|---|---|
| Tool tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -count=1` | ok |
| Agent tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent/ -count=1` | ok |
| State tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -count=1` | ok |
| Turn tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn/ -count=1` | ok |
| Run tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | ok |
| TUI tests | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1` | ok |
| Architecture | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` | ok (snapshot regenerated) |
| Build | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| Format | `gofmt -l pkg cmd` | silent |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |

`make build` once first if `third_party/pdfium/…` missing. The full
`./...` suite is the final gate here (shared engine + surfaces touched);
run it with a scratch home:
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`.
Pre-existing failures in files you did not touch → STOP and report
(parallel-agent WIP), do not fix.

## Scope

**In scope**:

- `pkg/tool/registry.go`, `pkg/tool/artifacts.go` (comments),
  `pkg/tool/artifacts_test.go` (gate test), `pkg/tool/notification.go`
  (Deliverable ID) + its tests
- `pkg/tool/shell_diff.go` (failure-path diff, Step 6b),
  `pkg/tool/shell_diff_test.go` (flip failure expectations) + affected
  shell-card tests
- `pkg/agent/coding_delivery.go` (create), `pkg/agent/coding_delivery_test.go` (create)
- `pkg/run/runner.go` (one line), `pkg/run/runner_test.go` (wantCoding + isolation)
- `pkg/state/rewind.go` (FileChangesForRunTree), `pkg/state/artifact_store.go`
  (latest-by-path, model-origin count) + their tests
- `pkg/turn/deliverables.go` (create), `pkg/turn/deliverables_test.go`
  (create), `pkg/turn/submit.go` (epilogue calls), `pkg/turn/batch.go`
  (trigger + dedup), `pkg/turn/batch_test.go` (trigger-level + continue tests)
- `pkg/tui/reducer.go` (union + dedup key), `pkg/tui/notify.go` (RunEndedMsg
  field), `pkg/tui/chat_session.go` + `pkg/tui/chat_turn.go` (mount field),
  `pkg/tui/commands.go` (replay union) + TUI tests
- `pkg/architecture` SQL snapshot (regenerate only)
- `docs/integration/batch-tasks.md` (fallback semantics, same diff)

**Out of scope** (do NOT touch):

- `presentFilesToolDescription`, every `testdata/*.golden*`,
  `pkg/agent/office_prompts.go`, `pkg/agent/advisor_prompts.go` — R2.
- `pkg/tool/shell_diff.go` beyond Step 6b's failure-path change — do not
  rework the snapshot mechanism, caps, or `selectRecord`.
- `pkg/gateway/**`, `frontend/**` — web already correct (R5); gateway
  frames already attach RunArtifactsOf.
- `pkg/tui/setup.go`/`setup_test.go`, `plans/config-tui-prototype.html` —
  other agents' WIP.
- No migration (fb_file_changes/fb_artifacts/fb_runs columns already
  exist); no new config key; no new origin value; no synthetic tool step.

## Git workflow

Default **in-tree, zero commits** (owner reviews/commits). Only if the
dispatcher explicitly instructs a worktree:
`.worktrees/324-deliverable-guarantee`, branch
`advisor/324-present-files-for-coding-projects`; merging is the owner's.

## Steps

### Step 1: Drift check + anchors

Header drift commands; then
`grep -c 'rt.ProjectType == state.ProjectTypeOffice || rt.ProjectType == state.ProjectTypeBatch' pkg/tool/registry.go`
→ 1; `grep -c 'wantCoding := "Forebrain Harness agent"' pkg/run/runner_test.go`
→ 1; `grep -n 'func (s \*Service) endBatchExecution' pkg/turn/batch.go` → :276.
Any mismatch → STOP.

### Step 2: Flip the registration test (reproduction)

`pkg/tool/artifacts_test.go`: rename
`TestPresentFilesRegisteredOnlyForOfficeAndBatch` →
`TestPresentFilesRegisteredForEveryProjectType`; table → four rows all
`want:true` (coding/office/batch/"" — "" is test-only, prod normalizes).
Body unchanged.
**Verify**: `… ./pkg/tool/ -run TestPresentFilesRegisteredForEveryProjectType -count=1`
→ FAIL `project type "coding" registered present_files = false, want true`.
Passing before the gate change → STOP.

### Step 3: Open gate + refresh comments

`registry.go`: condition → `if rt != nil {`; block comment →
"Deliverables belong to the conversation, not the process: every / project
type registers the tool with the same contract, so a coding / session hands
back finished files exactly like an office or batch one."; ProjectType
field comment → "ProjectType is the session's project type. It gates the
batch posture / (batchProject) and the session-tool set; present_files is
registered / for every type with the same contract."
`artifacts.go` 28-30 → "presentFilesToolName is the one deliverable action.
It is registered for / every project type (the runner passes the session's
project type), and / only the primary agent may call it."; 202-204 →
"newPresentFilesTool builds the deliverable tool. It needs the session /
store (artifacts live in the state database) and is wired up for every /
project type with the same contract."
**Verify**: `… ./pkg/tool/ -run TestPresentFiles -count=1` → ok.

### Step 4: Coding prompt constant + pins + wiring

Create `pkg/agent/coding_delivery.go` (package agent) with EXACTLY:

```go
// CodingDeliveryPrompt is the creative-coding conversation's delivery rule
// (owner 2026-11-18): every file a turn created or modified is that turn's
// deliverable — source code included, no kind excluded — and the model lists
// them all through present_files before it finishes. Same prompt-driven
// delivery office and batch use, with coding's own completeness rule; the
// shared tool description stays frozen. The engine's turn-end top-up
// (pkg/turn/deliverables.go) guarantees completeness even if the model skips
// the call.
const CodingDeliveryPrompt = `

# Hand over every file this turn touched

In this project the work itself is the deliverable: every file this turn created or modified inside the project directory is a deliverable of the turn — source code, scripts, configuration, data, documents, images, audio, video, archives, any type, regardless of what changed or how small the change. No kind of file is excluded: a helper script or a one-line source edit is delivered exactly like a report or an image.

Before finishing a turn that created or modified files, account for the full set: your own file writes and edits, the effects of every shell command you ran, and the files your subagents produced (they report their paths back to you). When the set is not obvious, verify it against the filesystem — for example, files whose modification time falls inside this turn — instead of guessing. List only files this turn actually changed: the project may carry older uncommitted work or another agent's concurrent changes, and those are not yours to deliver. Then call present_files once with every created or modified file, after the files are final.

Do not present files this turn did not change, and do not create a file merely to present it. A turn that changed no files answers directly without calling present_files. A deleted file cannot be presented — name it in your reply instead. Presenting the same path again in a later turn records a new version. Then reply briefly: what you changed and why, and anything the user still has to check or decide.`
```

Create `pkg/agent/coding_delivery_test.go` (pattern:
`scope_discipline_test.go`, `TestOfficePromptsNameTheDeliveryTool`
office_prompts_test.go:110-119): contains `present_files`; contains each of
`"every file this turn created or modified"`, `"No kind of file is excluded"`,
`"A turn that changed no files answers directly"`, `"another agent's
concurrent changes"`, `"A deleted file cannot be presented"`; lowercased
text contains none of `deepseek anthropic openai chatgpt kimi zhipu gemini
gpt claude skill.md .claude ~/.forebrain forebrain.yaml http:// https://`
nor other builtin tool names (`write_file edit_file read_file apply_patch
subagent_run git`).
`pkg/run/runner.go` coding branch: after `desc += agent.SubagentUserRulingPrompt`
add `desc += agent.CodingDeliveryPrompt`.
`pkg/run/runner_test.go`: insert `+ agent.CodingDeliveryPrompt` into
`wantCoding` between SubagentUserRulingPrompt and PlanHandoffQualityPrompt;
add isolation assertions (office/batch !Contains, coding Contains).
**Verify**: `… ./pkg/agent/ -count=1` ok (goldens' key sets unchanged);
the prompt-assembly test (function containing wantCoding) +
`… ./pkg/run/ -run TestBatchToolSet -count=1` ok; wantOffice/wantBatch
unchanged and green.

### Step 5: State queries (+ snapshot, dialects)

`pkg/state/rewind.go`: add `FileChangesForRunTree(ctx, sessionID, runID)
([]FileChangeRow, error)` — recursive `run_tree` CTE from `fb_runs`
(exemplar `llm_attempts.go:60-78`), then `fb_file_changes WHERE
session_id=? AND run_id IN (SELECT id FROM run_tree) ORDER BY id ASC`.
**Predicate carries session_id** (index prefix; no run_id index). dbx.SQL
triple.
`pkg/state/artifact_store.go`: add (a) `LatestArtifactByPath(ctx,
sessionID, path) (Artifact, bool, error)` — newest row for the path in the
session (ORDER BY id DESC LIMIT 1); (b) `CountArtifactsByOrigin(ctx,
sessionID, origin) (int, error)`. dbx.SQL triples.
Tests in the packages' existing test files (pattern: neighboring
store tests; use the test DB helper the package already uses).
Regenerate the architecture SQL snapshot per `sqlite_sql_test.go`
instructions.
**Verify**: `… ./pkg/state/ -count=1` ok; `… ./pkg/architecture/ -count=1` ok.

### Step 6: The guarantee epilogue

Create `pkg/turn/deliverables.go` — `func (s *Service)
collectTurnDeliverables(ctx context.Context, sessionID, runID,
projectType, projectRoot string)`. Use ONLY imports pkg/turn already has
(state, tool, event, …; **fan-out 12/12 full — a new import is a STOP**).
Logic:

1. Skip if `runID==""` or the artifact store is nil.
2. `ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)`;
   defer cancel. Wrap the body in a `defer recover()` that logs and returns
   — a panic must never change TurnOutcome.
3. Change set = union, keyed by **canonicalized absolute path**
   (EvalSymlinks; fall back to Clean on error — symlink spelling split,
   tool.ResolvePath does not evaluate symlinks while shell diff uses git's
   physical toplevel):
   - `FileChangesForRunTree(ctx, sessionID, runID)` rows: skip `deleted`;
     path = row.Path.
   - Shell paths: for each run in the tree, `ListRunEventsOfTypes` for
     tool-completed steps; decode RAW output via `DecodeShellEditDiff` /
     `ExtractTurnDiffPayload`; take `ChangedFiles` (NOT the diff
     Files/hunks, NOT the turn_diff_updated companion event — binary/large
     files live only in ChangedFiles).
4. Exclude: paths outside `projectRoot` (canonical containment); `.git/`
   segments; forebrain home/state paths; **for batch additionally** any
   dot-directory segment and the execution workspace's `.work/` subtree
   (mirror `collectBatchFallbackArtifacts` batch.go:404-409 — scratch must
   never become a batch deliverable). coding/office: only .git/home/state
   (owner: any file).
5. For each remaining path: `os.Stat` (skip missing — deleted since write);
   `hashArtifactFile` (batch.go:456, same package); compare with
   `LatestArtifactByPath(ctx, sessionID, path)`: skip if found and
   `SHA256==sum`; else `AddArtifact` with `Origin: state.ArtifactOriginFallback`,
   SessionID/RunID/Path/RelPath/Name/Title/MIME/Kind/Preview/Size/MTimeNS/SHA256
   (mirror batch.go:434-447). **Session-level** compare (batch continue
   reuses the session; auto-continue's new root run must not re-deliver
   unchanged bytes).
6. Per-file errors: `slog.WarnContext` + continue. No arbitrary file cap
   (owner: every file). On timeout: log partial registration, return.
   Never return an error to Submit.

Wire in `pkg/turn/submit.go`: call
`s.collectTurnDeliverables(context.WithoutCancel(ctx), req.SessionID, runID,
projectType, projectRoot)` **before `endBatch(...)` and before the terminal
`s.publish`** in the cancelled (:265), failed (:276), completed (:295)
branches; in the waiting-approval branch (:249) call it **only when
`batchTurn`** (batch approval_required is terminal). The epilogue runs
under the session lock — deliberate: after unlock, the next turn's writes
would race the hashing. The 10s timeout bounds lock hold. Obtain
projectType/projectRoot from the Service's existing runner/session access
(the same source `endBatchExecution` uses for the workspace; locate it —
if the Service cannot see project type/root, add them to TurnRequest or the
executor callback, NOT a new turn import).
**Verify**: new `pkg/turn/deliverables_test.go` (pattern: batch_test.go's
recordingArtifactStore + env): (a) file-tool change unpresented → registered
fallback; (b) model-presented same-sha → skipped; (c) re-edited after
presentation → new version; (d) deleted excluded; (e) outside-root
excluded; (f) shell ChangedFiles incl. a binary path → registered; (g)
subagent run-tree write → registered; (h) cancelled-turn context → still
registers (WithoutCancel); (i) batch .work/ + dot-dir excluded; (j)
runID=="" → no-op. `… ./pkg/turn/ -count=1` ok.

### Step 6b: Close F1 — failed/cancelled shell commands report their writes

`pkg/tool/shell_diff.go` `finish()` (:1052-1093):

1. Extract the success tail (:1060-1092, from the skipped/unavailable
   guards through `diffSince` + `dropExternalWrites` + Shared/TreeMoved)
   into a method `func (r *shellEditDiffRun) computeDiff(ctx context.Context,
   command string) *ShellEditDiff` — one implementation, both callers.
2. The failure branch (:1056-1059) becomes: `r.release()`; return nil when
   `r.skipped || r.unavailable || r.repo == nil` (nothing watched / no
   before-snapshot); otherwise compute with a detached, bounded context —
   `dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
   shellDiffTimeoutFor(command))` (the command's ctx may be cancelled; the
   writes already landed) — `defer cancel()`, return
   `r.computeDiff(dctx, command)`. A launch error with no writes yields no
   diff (nil), exactly as today.
3. The success branch calls `r.computeDiff(ctx, command)` unchanged.

Consequence (R6, owner-authorized): a failed/cancelled command that wrote
files now returns a real diff (Files + ChangedFiles), so its shell card
shows those changes and the Step 6 epilogue's shell union covers them.
No new note wording; `Unavailable` semantics unchanged (genuine snapshot
inability only). `dropExternalWrites` still subtracts this process's
file-tool writes in the window.

Tests:
- `pkg/tool/shell_diff_test.go` `TestShellEditDiffReadOnlyAndFailedCommands`
  (:300): flip the failure expectations — the cancelled case (:331,
  `finish(cancelCtx, "echo hi > x.txt", 0, nil)` currently asserts nil)
  and any failed-exit case must now assert a diff whose ChangedFiles
  contains the written path. grep/git-diff exit-1 cases (:313-326) stay
  green unchanged. Add a failed-exit case (`exit 1` after a write) and a
  launch-error case (runErr non-nil, no writes → nil).
- Any shell-card test asserting a failed command shows no diff section:
  locate by `grep -n "finish\|exit_code" pkg/tool/shell_card_test.go
  pkg/tool/format_test.go` and flip to the new truthful rendering;
  `format_test.go:982` (body must not contain DiffUnavailableNotePrefix)
  stays green.
- `pkg/turn/deliverables_test.go`: add case (k) — a shell step with failed
  exit whose raw output carries ChangedFiles → the epilogue registers the
  path as fallback.

**Verify**: `… ./pkg/tool/ -count=1` ok; `… ./pkg/turn/ -count=1` ok.

### Step 7: Batch trigger + dedup + doc (non-regression)

`pkg/turn/batch.go`:
- :329-331 trigger: replace `ListArtifactsPage(...total==0)` with
  `CountArtifactsByOrigin(ctx, sid, state.ArtifactOriginModel)==0`. This is
  a **deliberate improvement** on the continue path (turn-1 fallback rows
  suppressed turn-2 scan before; now turn-2 scan fires when the model
  presented nothing) — NOT semantics-preserving; pin it.
- `collectBatchFallbackArtifacts`: before AddArtifact, skip paths whose
  `LatestArtifactByPath(ctx, sessionID, path)` has the same SHA256
  (session-level dedup — unchanged outputs must not re-register under a
  continue's new runID).
- The :336 re-list and cap 500 stay; BatchExecutionEnd.Artifacts now
  carries top-up + outputs rows (report cap unchanged; REST paginates full).
`pkg/turn/batch_test.go`: KEEP the existing pinned test passing; ADD
trigger-level tests through `endBatchExecution`: (a) zero model delivery →
outputs scan fires; (b) partial model delivery → scan suppressed AND
top-up fills the tracked gap (the root cure); (c) continue second turn:
scan fires, unchanged outputs deduped, new outputs collected; (d)
approval_required branch runs the epilogue.
`docs/integration/batch-tasks.md`: update :21 and :351 — fallback rows now
coexist with model rows on partial delivery; the outputs/ scan trigger is
"the model presented nothing" (origin=model count zero).
**Verify**: `… ./pkg/turn/ -count=1` ok; `grep -n "一个文件都没交付\|origin=fallback" docs/integration/batch-tasks.md` shows the updated wording.

### Step 8: TUI parity — live + replay

- `pkg/tool/notification.go`: add `ID string` to `Deliverable` (additive);
  populate from the artifact row id wherever Deliverable is built
  (`DeliverablesFrom` / `DeliverablesFromEvent` in artifacts.go — rows
  already carry "id"; the frame conversion in Step 8b). Update shape tests.
- `pkg/tui/reducer.go`: `appendDeliverables` dedup key → ID when non-empty,
  fall back to the existing (RelPath,Size,Title) composite for legacy rows
  without ID. Pin the corner: same path+size, two different-content
  versions both kept (web keeps both by id).
- `pkg/tui/notify.go`: `RunEndedMsg` gains `RunArtifacts event.RunArtifacts`;
  mount at the REAL producers `chat_session.go:2267`, `chat_turn.go:944`,
  `chat_turn.go:1352` (read via the existing `s.runArtifacts(ctx, runID)`).
  Do NOT mount `chat_session.go:2731` / `commands.go:1430`.
- Reducer turn close (:1495-1505): union `takePendingDeliverables()` with
  the msg's RunArtifacts rows converted to Deliverable (ID populated),
  deduped by ID; order = pending first, then frame-only rows (pin this
  order; web's plan→presented→frame order differs slightly — content
  parity is the contract, order is pinned explicitly).
- **Replay** (`pkg/tui/commands.go`): `renderReplayDeliverables` (:768-777)
  and the replay loop must union the stored run-end payload's artifacts
  (replay run-end has ClosesTurn=false, notify.go:181-185) — locate where
  replay reads run-end events and feed the same union. Pin a live==replay
  test: a turn with a top-up-only deliverable shows the identical group
  live and replayed.
**Verify**: `… ./pkg/tool/ -count=1` ok; `… ./pkg/tui/ -count=1` ok
(including existing deliverable tests reducer_test.go:5899+ and the new
union/replay/dedup-corner tests).

### Step 9: Full gates + acceptance

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
gofmt -l pkg cmd
CGO_ENABLED=1 go vet -tags fts5 ./...
```

All green / exit 0 / silent. Then real-run acceptance (run-forebrain skill
if available, fake provider): a **coding** turn that (a) edits a source
file and writes a binary asset via a successful shell command, (b) calls
present_files with ONLY the source file → verify the asset appears as a
fallback deliverable on BOTH TUI (live + replay) and web; a **failing**
shell command (exit 1) that wrote a file → that file is delivered too
(F1); a turn that changes files and presents nothing → full delivery; a
**batch** execution
with partial delivery → gaps filled (root cure) and outputs/ scan behavior
per Step 7 tests. If the skill is unavailable, record that real-run
acceptance was not performed; Steps 2-8 tests are the mandatory proof.

## Limits (honest, documented — do not silently expand scope)

- **F1 CLOSED (rev 4, Step 6b)**: failed/cancelled shell commands now
  report their writes (detached-ctx diff in `finish()`); the epilogue
  covers them. Residual: a command killed mid-write delivers whatever
  landed (truthful); a launch error that wrote nothing delivers nothing.
- **F8 external concurrent writes**: dropExternalWrites subtracts only this
  process's file-tool writes; another process's write inside a shell window
  can be over-delivered. The engine cannot attribute; the prompt clause
  binds the model only.
- **F9 channel turns**: `pkg/gateway/channels.go` has no WithStepHook —
  primary shell steps may not be logged there (shell union empty; file
  tools still covered). Executor probes; if missing, document (mounting a
  hook is a gateway change — out of scope unless the probe shows it is
  trivially the same detached hook; then STOP and ask).
- **F10 reaper-settled runs** (process death, `serve_run.go:175-180`): Submit
  never returns → no epilogue. Inherent.
- **non-git projects**: shellDiffRepoFor returns nil → shell changes
  untracked; ledger covers file tools; batch outputs/ scan still nets
  untracked outputs writes on zero delivery.
- **caps**: shellDiffMaxChangedFiles per command; batch end-report cap 500
  (REST full). Untracked write paths (MCP servers writing directly) are not
  guaranteed.
- rewind erases ledger rows by session+path between turns (safe; ledger is
  not complete history). Auto-continue: per-run epilogue + session-level
  dedup; compaction/goal-check never go through Submit.

## Test plan

- Changed: registration test (4 cases); wantCoding + isolation; batch
  pinned test kept; Deliverable shape tests; appendDeliverables corner.
- New: CodingDelivery* (3); FileChangesForRunTree / LatestArtifactByPath /
  CountArtifactsByOrigin store tests; deliverables_test.go (a)-(k);
  shell failure-path flips + failed-exit/launch-error cases (Step 6b);
  batch trigger-level (a)-(d); TUI union live+replay+dedup-corner.
- Negative controls: Step 2 red-before/green-after; epilogue tests fail
  without Step 6 wiring (run them before wiring to confirm).
- Regression: both goldens, office/batch prompt bytes, web tests
  (`cd frontend && pnpm test` — zero web change, must stay green),
  architecture fan-out + SQL snapshot.

## Done criteria

- [ ] Full scratch-home `./...` suite green; build exit 0; gofmt silent; vet exit 0.
- [ ] `grep -n 'ProjectTypeOffice || rt.ProjectType == state.ProjectTypeBatch' pkg/tool/registry.go` → none.
- [ ] `grep -rn 'RegisteredOnlyForOfficeAndBatch\|only wired up for\|only office' pkg/tool/` → none.
- [ ] `grep -c 'desc += agent.CodingDeliveryPrompt' pkg/run/runner.go` → 1 (coding branch).
- [ ] `grep -c 'collectTurnDeliverables' pkg/turn/submit.go` → 4 (3 terminal + batch-only waiting branch).
- [ ] `git diff --name-only -- pkg/tool/testdata pkg/agent/testdata pkg/agent/office_prompts.go pkg/agent/advisor_prompts.go frontend` → empty.
- [ ] `pkg/architecture` tests green (fan-out unchanged, snapshot regenerated).
- [ ] `git status --porcelain` only in-scope files (+ README if maintained; list pre-existing WIP separately).
- [ ] `plans/README.md` row updated.

## STOP conditions

- Drift content mismatch; anchors not found by content.
- Step 2 passes before the gate change, or fails for another reason.
- Any golden / office-batch prompt byte / web test fails — do not edit them.
- A new pkg/turn import is needed (fan-out 12/12) — report, do not raise the ceiling.
- The Service cannot see projectType/projectRoot without a new import or a
  TurnRequest shape change you cannot justify — report.
- Channel step-hook probe (F9) shows missing logging needing gateway work.
- Any verification fails twice after a reasonable fix; failures in files
  you did not touch (parallel WIP) — report, never fix/revert.

## Maintenance notes

- The guarantee is "tracked changes delivered"; §Limits is the contract's
  honest boundary. F1 is closed (rev 4); the remaining limits each need
  their own ruling.
- Failed/cancelled shell cards now show the diff of what the command
  actually wrote (R6). A reviewer must scrutinize Step 6b's detached-ctx
  computation (no hang on a cancelled command: shellDiffTimeoutFor bound)
  and that no "diff unavailable" wording leaked into failure cards.
- Batch trigger is now model-origin-count; the continue-path behavior
  change is deliberate and pinned — a reviewer must scrutinize Step 7
  tests, not just the old pinned test (which protects only the scan
  function).
- TUI now unions frame artifacts live AND replay; the replay plumbing
  (commands.go) is the subtle half — review it against
  renderReplayDeliverables and the ClosesTurn=false contract.
- Deliverable.ID is additive; legacy replayed rows without ID fall back to
  the composite key — keep that fallback until old transcripts age out.
- Office/batch prompts and the tool description stayed byte-frozen; the
  coding "no kind excluded" rule is a prompt specialization, not a
  contract fork.