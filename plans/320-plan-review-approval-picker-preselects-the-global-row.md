# Plan 320: the plan-review approval picker opens on the install-wide reviewer row

> **Executor instructions**: follow the steps in order, run every verification
> and confirm its expected result before moving on. Touch only the files in
> Scope. A STOP condition means stop and report — do not improvise.
>
> **Drift check (run first)**: `git status --porcelain -- <in-scope paths>` and
> `git diff <baseline> -- <same paths>`; re-locate every symbol by name (line
> numbers below are advisory). Content mismatch = STOP.

## Status

- **Priority**: P1 (owner 2026-11-17 asked for the two follow-ups to be
  scheduled rather than left as notes)
- **Effort**: M
- **Risk**: MED (touches the approval request shape shared by TUI, gateway and
  replay; a wrong default would silently review with the wrong model)
- **Depends on**: plan 313 (landed, dev `831c45b4`) — it made the row
  configurable and visible. Plan 318 also edits the approval-picker effort
  labels; **land 318 first** so this plan does not collide with it.
- **Category**: Feature / consistency
- **Planned at**: 2026-11-17, on top of `831c45b4`

## Why this matters

Plan 313 gave the exit-plan reviewer an install-wide model row
(`agents.plan_reviewer`) and shows it on the subagent page and in `/config`,
but the approval picker still opens on **the first row that is not the current
one** instead of on the row the install configured. A user who configured
"review with glm-5.3" still has to pick it by hand at every plan approval, and
the web approval picker has the same behaviour. The owner's rule is that the
TUI is the standard and every surface behaves the same, so this must be fixed
once, at the one place that builds the approval request — not patched per
surface.

313's own Step 3c tried to fix this and stopped, correctly: its premise ("the
decision is built in `pkg/run/subagent.go`") does not exist. The real chain,
verified live by that executor on `831c45b4`:

- `RunPlanReviewSubagent` (`pkg/run/subagent.go`, ~:4973) only *pins* the
  override it is handed.
- `turn.PlanReviewModelOptions` (`pkg/turn/approval.go`, ~:1302) builds the
  picker's options and already sets `DefaultEffort` from the configured row.
- `process.PlanReviewer.Review` (`pkg/process/run_executor.go`) builds the
  `SubagentModelOverride` handed to the run.
- The TUI's `ReviewModels` closure (`pkg/tui/chat_surface.go`, ~:611) feeds the
  picker, and `enterPlanPickerMode` (`pkg/tui/overlays.go`, ~:1466) selects the
  first non-current row.
- `approvalOverlay` carries only `turn.ToolApprovalRequest`.

So the model choice and the effort already share one source; only the *cursor*
does not.

## Scope

**In scope**: `pkg/turn/approval.go` (+ its test) for the preferred-row field on
the approval request/options; `pkg/process/run_executor.go` (+ its test) to fill
it from `config`; `pkg/tui/overlays.go` and `pkg/tui/chat_surface.go` (+ tests)
so the picker opens on it; `frontend/src` approval picker component (+ test) for
the same default; `pkg/gateway` only if the request crosses the wire (it does
for the web picker) and only to carry the field; `plans/README.md` row.

**Out of scope**: `pkg/run`'s pinning path (it is correct), the reviewer's
effort candidates (313 + 318 own them), the picker's layout, any change to
which rows are offered.

## Steps

1. **Name the fact once, in `turn`.** Add the resolved reviewer row id to the
   plan-review approval request (a field on `PlanReviewModelOptions`' input and
   on the request the picker receives; empty = no configured row). It must be
   the **row id**, not a display label, and it must be resolved through the
   same lookup the runtime uses (`LLMModelHeld`/`ResolveModelRef` semantics),
   never by string-matching a display name.
2. **Fill it in `process`.** `PlanReviewer.Review` reads
   `agents.plan_reviewer.model`; if it resolves to a row present in the table,
   set the field; if it is empty or dangling, leave it empty (a dangling id must
   not become a cursor — 315's rule: a dangling reference behaves as unset).
3. **TUI opens on it.** `enterPlanPickerMode`: when the field names a row that
   is offered, the cursor starts there; otherwise keep today's first-non-current
   behaviour. No new keystroke, no extra page.
4. **Web and replay agree.** The gateway payload carries the same field and the
   web approval picker preselects it; the replay projection of a decided
   approval must show the row that was actually used (it already records the
   choice — assert it rather than invent a second source).
5. **Tests.** One test per surface that (a) a configured row is the initial
   selection, (b) an empty/dangling row falls back to today's behaviour,
   (c) choosing another row still wins and is recorded, (d) TUI, gateway payload
   and replay report the same row for the same config.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/turn ./pkg/process ./pkg/tui ./pkg/gateway -count=1` → ok;
`go build -tags fts5 ./...` → 0; `gofmt -l pkg` → empty; frontend
`npm run typecheck && npx vitest run src/components` → 0.

## Done criteria

- [ ] With `agents.plan_reviewer.model` set to a table row, the approval picker
      (TUI and web) opens on that row; with it unset or dangling, behaviour is
      unchanged.
- [ ] The chosen row is what the review actually runs on, and replay shows it.
- [ ] No surface derives the default from a display label or a provider name.
- [ ] Full package suite + architecture green; no out-of-scope file changed.

## STOP conditions

- Carrying the field needs a second source of truth (a new config key, a new
  table column, or a per-surface mapping table) — report instead.
- The web picker cannot receive the field without changing the approval API's
  public shape beyond an additive field — report.
- The reviewer row and the picker disagree about which model the run used —
  report with the request and the run's model id.
- Any verification fails twice for the same reason.

## Maintenance notes

- One source: the configured row. A future surface that preselects anything else
  is a regression, not a preference.
- The failure mode to watch in review: a dangling row id silently becoming a
  cursor, or a display label being compared to a row id.

---

## 2026-11-17 correction at dispatch (first executor stopped on the Step 2 premise)

The first run stopped correctly instead of improvising. Verified against the
tree (`5cfe6312`), the plan's Step 2 premise was wrong:

- `process.PlanReviewer.Review` (`pkg/process/run_executor.go:427`) runs
  **after** the user has chosen: it reads `r.Model` (the chosen row) and
  resolves its row id via `planReviewModelRow` (`:473`). It never reads
  `cfg.Agents.PlanReviewer` and never builds a `turn.ToolApprovalRequest`, so
  it cannot give the picker a default.
- `cfg.Agents.PlanReviewer` has exactly three non-test readers, none in
  `pkg/process`: `pkg/config/load.go:218` (validation),
  `pkg/gateway/subagents_api.go:299` (display label) and
  `pkg/gateway/api_extra.go:9871` (settings normalization to a row id).
- The request is built in one place: `turn.PendingApprovalGate.PendingForRun`
  (`pkg/turn/approval.go:1824`, `req.PlanReviewModels = g.ReviewModels(sessionID)`
  at `:1892`), with the option list injected by two closures — TUI
  `pkg/tui/chat_surface.go:611` and gateway `pkg/gateway/approval.go:49` — both
  of which already call `turn.PlanReviewModelOptions(cfg, …)`
  (`pkg/turn/approval.go:1302`). `PendingApprovalGate` is constructed only in
  those two surfaces.
- The web dock (`frontend/src/components/chat/PlanApprovalDock.vue:104-121`) has
  **no selection state at all** — one button per reviewer row, click submits.
  "Preselect" there is a new affordance, not an additive field, so the original
  Out-of-scope line cannot hold.

### Corrected steps (these replace Steps 1, 2 and 4 above)

1. **Resolve once, where `cfg` already is.** In `turn.PlanReviewModelOptions`
   (`pkg/turn/approval.go:1302`), resolve
   `config.ResolveModelRef(cfg, "main", strings.TrimSpace(cfg.Agents.PlanReviewer.Model))`
   and, when it resolves, mark that option. Use `ResolveModelRef` — the same
   lookup 313's display and the settings normalization use; a dangling or empty
   reference marks nothing (315's rule). No new config key, no new table
   column, no per-surface mapping, and **no change in `pkg/process`**.
2. **Carry it on the option, not the request.** Add `Preferred bool` to
   `turn.PlanReviewModelOption` (the type lives in `pkg/turn/model.go:646`;
   `ToolApprovalRequest` is `pkg/turn/model.go:576` — the Scope's file names
   were wrong). The option list is the one thing both surfaces already iterate,
   so one flag serves both without a second lookup.
3. **TUI**: `enterPlanPickerMode` (`pkg/tui/overlays.go:1449`) starts on the
   preferred option when there is one, else keeps today's first-non-current
   rule. Extend the existing `enterPlanPickerMode` tests
   (`pkg/tui/overlays_test.go:3905/4163`).
4. **Web**: `PlanApprovalDock.vue` gains a default highlight on the preferred
   row and submits it on plain Enter (the click path stays). The wire struct
   `planReviewModelWire` (`pkg/gateway/api_extra.go:4624`) gains an additive
   `preferred` field. This is now **in scope** — the three-surface rule requires
   it, and a field alone would leave the web without the behaviour.
5. **Replay**: assert that a decided approval shows the row that ran (the chain
   TUI `RequestPlanReview` → `turn.Model` → `process.PlanReviewer.Model` →
   `SubagentModelOverride.ModelID`, and the web body the same) — an assertion
   test, not a new path.

Everything else in this plan (done criteria, the one-source rule, the dangling
rule) stands.
