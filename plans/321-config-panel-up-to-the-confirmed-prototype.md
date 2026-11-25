# Plan 321: bring the live `/config` panel up to the confirmed prototype (Enter persists, no draft, footer Default, four tabs)

> **Executor instructions**: step by step, every verification before the next
> step. Touch only the files in Scope. STOP and report on any STOP condition.
>
> **Drift check (run first)**: `git status --porcelain -- <in-scope paths>` and
> `git diff <baseline> -- <same paths>`; re-locate symbols by name. Content
> mismatch = STOP.

## Status

- **Priority**: P2 (owner 2026-11-17: schedule it rather than let the panel and
  the confirmed prototype drift apart)
- **Effort**: XL
- **Risk**: MED (the panel is the only write surface for several sections; a
  half-migrated save semantics loses edits)
- **Depends on**: plan 313 (landed `831c45b4`), plan 315 (landed `429987df`,
  the retired-key filter), plan 319 (field types / pickers) — **land 319 first**.
- **Category**: Feature / UX rebuild
- **Planned at**: 2026-11-17, on top of `831c45b4`
- **Landed**: `19cd12b5`（2026-11-18「feat(tui): a confirmed /config edit is the write」）——draft、Save 行、成功提示与 unsaved 文案整段删除，每次确认的编辑走同一个 store 路径。2026-11-24 reconcile 在 dev `cba1bfba` 复核：`grep -c configSavedNotice pkg/tui/*.go` = 0、`Default (unset)` = 0，四 tab 仍在。要点 4 的「四 tab 顺序与文案」此后由 `plans/327`–`plans/329`、`plans/350` 继续按冻结原型收紧（本计划不重复实施）。

## Why this matters

The design contract is `plans/config-tui-prototype.html`
(sha256 `95e75dedd9479317e23464d1978efc6a9301e3dfead0efade0bcbee9c8e9e688`,
owner re-confirmed 2026-11-17, regression `node config-tui-prototype.test.cjs`
→ 26 passed, needs `python3 -m http.server 8765 --directory plans`).

The live panel still implements plan 190's draft model, and the gap was
verified by plan 313's executor on `831c45b4`:

- a `configSavedNotice` reading "Written to the database; applies from the next
  turn", i.e. an explicit save step and a delayed effect;
- a `Default (unset)` **row inside the bool picker** (the prototype deletes it
  and puts unset on a footer `Default` action);
- per-option descriptions, a context bar and a composer strip the prototype
  does not have;
- no footer `Default` action, no Enter-to-persist.

Owner rulings this plan implements (all 2026-11-16, latest wins):

1. **Enter persists.** No draft / staged / review / leave-unsaved semantics, no
   Ctrl+S, no `Unsaved changes` / `Modified` / `Change staged` tags. A
   confirmed Enter writes through the existing store path immediately and
   shows the brief `Saved.` notice; a failed write keeps the previous value and
   reports the error.
2. **Unset is an action, not a row.** bool/enum choice lists list only real
   values; going back to unset is the choice page's footer `Default` action.
   Model/effort pickers keep their real-valued `Follow …` / row entries.
3. **No `·` connectors** (plan 316 landed), `Default` not "Use default",
   section titles `Subagent models` and `MCP servers`, concise copy with no raw
   config paths, English-only in the TUI (the pinned gate
   `TestConfigPanelSaysEverythingInEnglish` in a same-named file must keep
   passing).

## Scope

**In scope**: `pkg/tui/config_panel.go`, `config_panel_schema.go`,
`config_panel_columns.go` if present, `config_store.go` (only the save/persist
call shape), their tests, and the panel-only helpers they own. Copy and layout
confined to those files.

**Out of scope**: `pkg/config` contracts and validators (the panel must write
what the loader already accepts), the gateway settings API, the web settings
pages (they have their own prototype), `pkg/tui/setup.go`, the frozen
prototype files themselves.

## Steps

1. **Inventory the delta.** Walk the prototype scene by scene and write the
   list into the plan's own progress note: for each section, what exists today,
   what the prototype wants, what blocks it. No code yet.
2. **Delete the draft model.** One Enter = one store write through the existing
   `configStore` path; remove `configSavedNotice`, the draft/staged bookkeeping,
   the review/leave concepts and the Ctrl+S binding. Keep the write failure
   path (previous value restored, error shown).
   - **Atomicity**: a section that validates several fields together (the
     prototype's own example: a hook switched to HTTP without a url) must be
     committed as one candidate, not field-by-field — either edit the companion
     field in the same write or keep the edit behind the validator. Choose one
     and pin it with a test; a half-written section is a STOP.
3. **Choice lists and the footer `Default`.** bool/enum lists lose the
   `Default (unset)` row; the choice page gains a `Default` action that deletes
   the override; `edit`-kind rows keep their `Default` button. Model/effort
   pickers keep their `Follow …` values.
4. **Four tabs, order and copy** per the prototype: section order, titles,
   value-right alignment, single-line status, no context bar, no composer
   strip, no per-option descriptions, no row arrows, no `Read only`, no
   `Saved.`-style tags beyond the transient notice, no raw config paths in
   hints.
5. **Retired keys and guards.** `dropRetiredAgentKeys` (plan 315) stays; the
   schema-coverage guards stay green; the schema gains nothing that shadows
   plan 319's field types.
6. **Tests.** A render golden per tab (real renderer, not the fake selector),
   a save-on-Enter test per row kind (bool, enum, number, text, rows, model
   ref), the unset-via-footer test, the failed-write test, and the atomicity
   test from Step 2. Keep the English-only gate file same-named as its
   production file.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui -count=1` → ok;
`go build -tags fts5 ./...` → 0; `gofmt -l pkg/tui` → empty; prototype
regression → 26 passed (run it from the main tree; `plans/` is not in a
worktree, so report it as "run by the reviewer" if you cannot).

## Done criteria

- [ ] Every Enter-confirmed edit is persisted immediately; no draft/staged
      state exists anywhere in the panel or its tests.
- [ ] bool/enum choice lists contain only real values; unset is reachable only
      through a `Default` action (choice-page footer or edit-page button).
- [ ] The four tabs, section order, titles and copy match the prototype; no
      `·`, no raw config paths, English-only, `TestConfigPanelSaysEverythingInEnglish`
      green.
- [ ] A failed write leaves the stored value untouched and shows the error.
- [ ] Cross-field validation commits atomically (test proves it).
- [ ] Full package suite + architecture green; no out-of-scope file changed.

## STOP conditions

- A section has no write path today and would need a new gateway mutation or a
  new store method to persist on Enter — report; that is a separate change.
- Atomic cross-field commit would require the loader to accept a candidate it
  currently refuses — report with the validator's message.
- Removing the draft model leaves a code path that can only be tested by
  weakening a guard — report.
- More than two verifications fail for the same reason.

## Maintenance notes

- The prototype is the contract, not an inspiration: a future panel change that
  reintroduces a draft step, a `Default` row in a value list, or an extra save
  button is a regression.
- Reviewers: check that Enter-to-persist writes through the same `configStore`
  path as the rest of the app (no second write path), and that no section's
  companion field is left half-written.

---

## 2026-11-17 correction at dispatch (first run stopped at Step 2's atomicity)

The first executor stopped correctly: it ran the store's real write-side
validator (`appcfg.FinishLoadWith`, what `pkg/process/config_db.go`'s
`validateRootSection` calls) over the candidates the panel and the prototype
produce, and Step 2's "edit the companion field in the same write" is
impossible for `hooks` — the first write of a new row is always the candidate
the loader refuses:

```
{}                                  => unsupported hook type
{matcher, hooks: []}                => hook matcher requires hooks
{type: command}                     => command hook requires command
command -> http, no url             => http hook requires absolute url
url first, type still command        => nil   (accepted)
type http + url in one candidate     => nil   (accepted)
```

`agents`, `lsp` and `mcp` rows are accepted as `{}`, so this is `hooks`-only.
`gateway` and `credentials` have no write path at all (`YAMLOnly`, "edited in
forebrain.yaml by hand only") but the prototype renders them read-only with no
fields, so they have no Enter to persist — non-blocking, out of scope.

### Decision (owner, 2026-11-17): a new hook row asks its template's fields

1. **Create, then write once.** Creating a hook row asks the fields its type
   needs — `command` for `command`, `url` for `http`, `prompt` for `prompt` and
   `agent` (the prototype's `templates.hook` `showFor` lists) — and writes the
   complete row in one candidate. Never store an incomplete hook, never relax
   `ValidateHookCommand` (`pkg/config/hook_schema.go`), and do not keep a draft
   for hooks: the row exists only once it is valid.
2. **Every other row kind keeps today's "Enter writes immediately"** with an
   empty seed (`{}` is accepted for agents, lsp, mcp).
3. **Success is silent.** The prototype sets `notice=''` on persist, matching
   the owner's 2026-11-16 "silent success" ruling — the plan's earlier
   "brief `Saved.` notice" is withdrawn. A failed write still keeps the old
   value and shows the error.
4. **Scope grows by the two shared renderers** the executor proved are outside
   the original file list: `pkg/tui/panel_columns.go` (value-right alignment
   and the 16-column status column live in `columnRows` /
   `selectableColumnsStatus`) and `pkg/tui/overlays.go` (the choice page's
   footer/`hint` line, where the `Default` action belongs). There is no
   `config_panel_columns.go`. If a per-caller footer cannot be added there
   without a layout rewrite, the Done criterion's alternative stands: a
   trailing action row on the choice list that unsets the value.
5. **Copy notes from the inventory**: the prototype's "MCP servers" is a
   **field label** (`defaults.mcp_servers`), not a section title, so today's
   "Shared MCP servers" is not a title violation; the only section title the
   panel lacks is "Workspace sandbox" (today "Sandbox writable roots"). Tabs,
   tab contents and their order already match — do not re-do them.
6. **Also already conformant** (do not "fix" them): there is no context bar and
   no composer strip in the live panel.

Everything else in this plan stands, including its two STOP conditions.
