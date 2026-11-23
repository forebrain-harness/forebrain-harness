# Plan 350: Restore the /config panel to 1:1 parity with the frozen prototype

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat fb4fd294 -- pkg/tui/config_panel.go pkg/tui/config_panel_schema.go pkg/tui/config_panel_test.go pkg/tui/tabbed_picker.go pkg/tui/overlays.go pkg/tui/render.go`
> and `git status --porcelain -- pkg/tui`. If any in-scope file has changed
> since this plan was written — committed or not — compare the "Current state"
> excerpts against the live code before proceeding; on a content mismatch,
> treat it as a STOP condition (moved line numbers alone are not a mismatch).

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED–HIGH (the row renderer and footer hint are shared with every
  rich picker; every visual change must be gated to the /config pages so
  /skills, /model, /status and the setup flows do not regress)
- **Depends on**: none. Overlaps plans 318/319 (reasoning-semantics / effort
  pickers, still TODO): the effort empty-label fix here (Step 5) must use the
  same wording those plans settle on — see Maintenance notes.
- **Category**: bug / tech-debt (UI–UX contract parity)
- **Planned at**: commit `fb4fd294`, 2026-11-22
- **Frozen contract**: `plans/config-tui-prototype.html`
  sha256 `b22e4ddb266fa49350273c40b1f55e98e66da41b931a36008656f3a4bf22e3a5`
  (270 lines). Regression harness `plans/config-tui-prototype.test.cjs`
  sha256 `3cd9adc6495f2cb6afb67c44071a9d65b751b30e8b0587ce73d712b0a2a6d588`.
  The prototype is the design contract; do NOT edit it (owner re-freeze rule).

## Why this matters

The owner reports the /config panel — especially the **Agents & tools** tab
and the agent create/edit pages — is "几乎不能用，bug 非常多，基本上没有按照原型稿实现"
and mandates a 1:1 restoration this period. Real-machine driving (isolated
instance, dev `fb4fd294`) plus the owner's own screenshots confirm the panel
diverges from the frozen prototype on every layer: row layout, page chrome,
value/status semantics, and core interactions (tab switching, create, delete).
The cost is concrete: tabs cannot be switched with ←/→ (only Tab), agent
definitions cannot be deleted from the list (`d` is inert), creating an agent
swallows the name you type, every field row carries an inline help sentence the
prototype reserves for the editor, unset fields print a invented "Default"
twice, booleans read On/Off instead of True/False, and the tab bar vanishes the
moment you enter a section. This plan restores the contract so the panel is
usable and visually identical to the prototype at 120/88/68 columns.

## Current state

Files (role + the load-bearing excerpt as it exists at `fb4fd294`):

- `pkg/tui/config_panel.go` (3609 lines) — the /config controller: section
  list, field editors, template pages, add/remove, value/status wording.
  - `:255` overview opens the tabbed page:
    `idx, ok, err := selectRichTabbedPage(p.sel, "", items, TabbedSelectOptions{DefaultIdx: p.overviewRow})`
  - `:592-599` section heading (the block the prototype removed):
    `func (p *configPanel) sectionPrompt(...) string { head := schema.Title + "\n" + schema.Summary ... }`
  - `:46-47` boolean labels: `configBoolOnLabel = "On"` / `configBoolOffLabel = "Off"`
    (also `:67-68` `configNewTrueRow = "On"` / `configNewFalseRow = "Off"`;
    used at `:778-779`, `:2833`, `:2901`).
  - `:1531` `configRowDefaultEffort = "Row default"`; `:1537`
    `configProviderDefaultEffort = "Provider default"`; `:566-573`
    `configEffortSummary(level)` returns `configRowDefaultEffort` for empty.
  - `:1517-1520` `configDefaultStatus = "Default"`; `:3479-3505`
    `configFieldDefaultText(field)` returns `configDefaultStatus` ("Default")
    for an unset field with no default of its own — this fills the value column
    with "Default" the prototype leaves empty.
  - `:1287-1363` `editRows` registers the footer actions:
    `FooterAction{Label: configAddActionLabel, Key: "a", Kind: FooterActionAdd}`
    and `FooterAction{Label: configRemoveActionLabel, Key: "d", Kind: FooterActionRemove,
    RowActive: func(itemIdx int) bool { return ... p.configRowIsEntry(doc, path, keys[itemIdx]) }}`.
  - `:1365+` `configRowIsEntry` — "a row the generic editor dispatches straight
    to its text editor — a path row or a string row … never a container". This
    is why `d` is inert on agent definitions / subagents / MCP servers (all
    containers): the prototype marks EVERY collection row removable.
  - `:2762`, `:2793` keyName create: `case row.field.KeyName:` …
    `name, ok, err := p.sel.Input("New agent\nName\n"+configKeyNameHint, "")` —
    the Name editor opens only after an extra Enter on the create form.
  - `:487-550` `editBuiltinSubagents` / `builtinSubagentItems` — the six
    install-wide type rows.
- `pkg/tui/config_panel_schema.go` (636 lines) — pure-data section/field
  descriptors (`configField`, `configSectionSchema`), kinds and flags
  (`KeyName`, `CreateEditable`, `PrimaryOnly`, `SubagentOnly`, `HideOnBuiltin`,
  `CustomOnly`, `ShowFor`, `Entry`, `Requires`, `Group`, `Multiline`, `Secret`,
  `ModelEmpty`/`ModelFlavor`). Comments cite "plan 325/326/327/328/329" — those
  plan files no longer exist in `plans/`; the implementation followed a narrower
  rule than the frozen prototype, which is the drift this plan corrects.
- `pkg/tui/tabbed_picker.go` (533 lines) — the tabbed picker key loop.
  - `:378-394` `tabbedKeyLoop(..., configPage bool)`; `configPage` sets
    `p.noFilter = true` (no filter box on /config).
  - `:423-426` `case rawKeyTab: p.switchTab(1)` / `case rawKeyShiftTab: p.switchTab(-1)`.
  - `:453-462` **the tab-switch bug**:
    ```go
    case rawKeyLeft:
        if !p.inFilter { continue }
        p.filter.move(-1)
    case rawKeyRight:
        if !p.inFilter { continue }
        p.filter.move(1)
    ```
    With `noFilter` true, `inFilter` is never true, so ←/→ are always a no-op.
- `pkg/tui/run.go` — the **reference** behavior, /status (`uiPanel`):
  - `:6406-6417`
    ```go
    case hotkeyPanelLeft:
        if panel.kind == "status" { panel.switchTab(); return true }
        panel.back(); return true
    case hotkeyPanelRight, hotkeyPanelTab:
        if panel.kind == "status" { panel.switchTab() }
        return true
    ```
    /status switches tabs on ←, → and Tab. /config must match.
- `pkg/tui/overlays.go` — the shared rich-picker rendering.
  - `:5159-5168` `pickerHead(b, label)` draws `b.title(lines[0])` +
    `b.subtitle(rest)` — the heading block /config must stop drawing.
  - `:5170-5178` `selectFooterHint(multi, esc)` returns
    `"↑/↓ to navigate Enter to confirm " + esc` (esc defaults to "Esc to cancel").
  - `:6277-6332` `richPickerState.panel(label)`; the `configPage` branch already
    draws ruled group labels (`b.groupRule`) and airy spacing (`b.breathe`),
    then draws every row single-line at `:6321`:
    `b.selectableColumnsValue(ci == p.cursor, "", name, nameWidth,
    strings.TrimSpace(item.Description), item.ElideTail, item.Value, item.Status, mark)`
    — `item.Description` (the field help) is drawn inline; this is the inline-help bug.
  - `:6338-6351` `footerHint()` = `selectFooterHint(false, p.esc)` + footer actions.
  - `:6366-6395` `fireAction` — `a`/`d` fire only when `p.noFilter` and the
    matching `FooterAction` exists and `actionServesRow`; `Ctrl+U` fires
    `FooterActionDefault`.
- `pkg/tui/render.go` — the panel drawing primitives (reuse these, do not invent
  a parallel toolkit): `panelBuilder` `:8148+` with `title/subtitle/text/blank/
  group/groupRule/breathe/hint/facts/selectable/addSelectable`; `panelLine{lead,
  text}`; selection highlight via `focusStart/focusEnd` in `addSelectable`
  `:8291-8305`. `/status` builds its tab line into `b.head`
  (`buildStatusPanel` `:8307-8334`) — the exemplar for a persistent tab header.

Conventions: Go 1.26, CGO mandatory, build tag `fts5`. Tests are table-driven
with a scripted selector double — see `pkg/tui/config_panel_test.go` (4594
lines) and `pkg/tui/commands_test.go` (`scriptedSelector`/`sequenceSelector`).
Match that style. One implementation per behavior: the /config pages reuse the
shared picker; gate visual changes behind `configPage`/`noFilter`.

### The frozen contract (inlined from the prototype — the executor has not read it)

Tabs (`html:40`): `safety` "Approval & sandbox" (short "Safety"), `session`
"Session & memory" (short "Session"), `agents` "Agents & tools" (short "Agents").
Tab bar shows label + section count (3/4/4); width < 100 uses the short label and
hides counts (`.terminal.narrow .tab .count{display:none}`). Active tab is blue
with a 2px blue underline (`.tab.on`). ←/→ switch tabs (`html:211` ArrowLeft/Right
→ `goOverview(nextTab)`); the tab bar is visible on EVERY page (overview, section,
field, choice, edit) — `render()` always emits `<header class="panel-header">tabs`.

Chrome (owner 2026-11-18/21, `html:163,165,166,169`): `breadcrumb()=''`,
`heading()=''`, `filterBox()=''`, filter disabled. So: NO breadcrumb, NO title/
summary heading block below the tabs, NO filter input box. The panel header is
the tab bar only.

Row layout (CSS `html:9`):
- **Overview** rows (`.item.overview`): min-height 3 lines, padding ½ line top/
  bottom; `.item-label{display:block;font-weight:600}` (bold title line) +
  `.item-desc{display:block;color:muted;font-weight:400}` (summary on the NEXT
  line). Selected = background highlight + blue `❯` (`.item.selected`).
- **Fields** rows (`.field-table .item`): single line, label + value right
  (`.item-value{margin-left:auto;text-align:right;color:blue}`) + status right
  (`.item-status{flex:0 0 16ch;text-align:right}`); **NO description line** —
  `rowMarkup` only renders `row.desc`, and the fields view sets `help` not `desc`
  (`html:171`), so field help is NOT shown inline. Airy: padding ½ line, one blank
  line between rows (the `configPage` branch already breathes).
- **Choice** rows (`.item.option`): single line, `opt-label` (fixed name column)
  + `opt-desc` (inline, quiet) + `✓` check on the original value.
- Group labels (`.group-label`): upper-cased, quiet, trailing rule to the edge,
  one blank line before (already implemented via `groupRule`+`breathe`).
- Locked rows (`.item.locked`): opacity .55, label muted, NOT selectable; Enter
  on a readOnly field answers the notice "Set when the definition was created;
  it cannot be changed." (`html:141`).

Value/status semantics (`html:124-125` `display`/`rawDisplay`):
- An unset field value (`undefined|null|''`) renders **empty** (`display` returns
  `''`), EXCEPT a boolean with a default renders `True`/`False`. The word
  "Default" appears ONLY as the overview status of a section the store has no row
  for (`html:170` `status: state.saved[s.id]===undefined?'Default':''`). It is NOT
  a per-field value and NOT a per-field status.
- Booleans read `True`/`False` everywhere (choice labels `html:143` and display).
- A keyName field's value is the definition's key itself (e.g. `main`), never
  "Default".
- Thinking level empty for a subagent/type row reads **"Follow the model default"**
  (`effortLabel`, `html:68`), deliberately distinct from /connect's "Provider
  default" (`levelLabel('')`, `html:57`). Effort candidates come from the model
  row's sendable levels; Off only when the row is off-capable AND its protocol can
  send `none` (Anthropic Messages rows get no Off).
- Model choices come only from the global model table; a row's description is
  `base_url + api_path`; the empty option carries its own label+desc; a dangling
  pin is preserved as an extra option.

Footer hints (`html:184-205` `footerHTML`, the slash-picker hint line, never
buttons/chips):
- overview: `↑/↓ to navigate Enter to open Esc to close`
- fields: `↑/↓ to navigate Enter to open Esc to go back`; on a collection/generic
  frame add ` a Add d Remove` when the cursor is on an entry row, else ` a Add`.
- choice: `↑/↓ to navigate Enter to save Esc to cancel`
- edit (single-line): `Enter to save` + (` Ctrl+U Default` when the field has a
  default) + ` Esc to cancel`; multiline: `Ctrl+Enter to save` + (Ctrl+U) +
  ` Enter for a new line Esc to cancel`.
- add: `Enter to save Esc to cancel`; delete: `↑/↓ to navigate Enter to confirm
  Esc to cancel`.

Interactions:
- Create (`html:158` `startAdd`): on the agent collection, `a` creates a `''` key
  `{primary:true}` and opens the create form **with the Name editor focused**
  (`focusEditor`); typing the name immediately works. Name validation: lowercase
  letters/numbers/hyphens, no duplicate key, none of the 6 built-in names; refusal
  keeps the input and shows an inline error (`html:150-151`).
- Delete (`html:160-161,211`): `d` on ANY collection entry row (the prototype's
  `rowField` marks every collection row `entry:true`, `html:132`) opens
  "Remove <label>?" with `Remove item` (danger) / `Keep item`, **Keep preselected**;
  confirm removes from the draft and persists.
- requires guard (`html:142`): opening a thinking level with no model selected
  refuses with "Choose a model before setting its thinking level."
- model change/clear clears the sibling `reasoning_effort` (`html:148`);
  `primary=false` drops `mcp_servers`/`enable_subagent`/`subagents` (`html:147`).
- The list scrolls (`.panel-body{overflow:auto}`); it is not paginated with a
  "↓ N more" footer.

Owner screenshots (authoritative targets, in `.forebrain/attachments/`):
- `forebrain-clipboard-31686-3889333557.png` — current overview, single-line rows (wrong).
- `forebrain-clipboard-31686-766085985.png` — prototype overview, two-line blocks (target).
- `forebrain-clipboard-31686-2929515719.png` — current agent edit page, inline help + Default×2 + On (wrong).
- `forebrain-clipboard-31686-2570158163.png` — prototype agent edit page: tabs persist, no heading, single-line label-only rows, Primary "True", no Default (target).
- `forebrain-clipboard-31686-767280408.png` — current "New agent" create page (wrong).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build (day-to-day) | `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |
| Focused tests | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfig' -count=1` | all pass |
| Package tests | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| Architecture gate | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok (fan-out ceilings unchanged) |
| Format | `gofmt -l cmd pkg third_party plugins` | prints nothing |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./...` | exit 0 |
| Real machine | `.claude/skills/run-forebrain/driver.sh` (see Step 8) | screens match the contract |
| Prototype regression | `cd plans && python3 -m http.server 8765` then `node plans/config-tui-prototype.test.cjs` | passes up to the KNOWN stop point (see Step 8) |

`pkg/pdf` is not in scope; if a direct `go` command touches it, carry
`PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`.
**Never run `scripts/test-db.sh`** (owner standing rule); no state SQL changes
are in scope here.

## Suggested executor toolkit

- Skill `run-forebrain` (`.claude/skills/run-forebrain/SKILL.md`): build, launch
  and drive the TUI in an isolated tmux instance, capture screens. Use it for
  Step 8 and for reproducing each bug before/after. Give your run its own
  `FOREBRAIN_RUN_WORK`/`FOREBRAIN_RUN_SESSION`/`FOREBRAIN_RUN_PORT`.
- Skill `tui-render-ab-verify` if you need SGR-level proof that a colour/layout
  change took effect (capture-pane -e per-token comparison).
- Read `plans/config-tui-prototype.html` (the contract) and
  `pkg/tui/config_panel_test.go` (the test pattern) before starting.

## Scope

**In scope** (the only files you should modify):
- `pkg/tui/config_panel.go`
- `pkg/tui/config_panel_schema.go` (only if a descriptor flag is needed to drive layout)
- `pkg/tui/config_panel_test.go`
- `pkg/tui/tabbed_picker.go`
- `pkg/tui/tabbed_picker_test.go` (create if absent)
- `pkg/tui/overlays.go` — ONLY the `configPage`/`noFilter`-gated rendering paths
  (`richPickerState.panel`, `pickerHead` usage for config, `footerHint`,
  `fireAction`, the config row drawing) and the ←/→ tab-switch in `tabbedKeyLoop`.
- `pkg/tui/render.go` — ONLY additive panel primitives (e.g. a two-line
  selectable row, a persistent tab header helper) reused by the config pages.

**Out of scope** (do NOT touch, even though they look related):
- `plans/config-tui-prototype.html` / `.test.cjs` — the frozen contract; editing
  it requires an owner re-freeze. The regression's known Ctrl+U stop point is a
  prototype-side gap, not yours to fix here.
- Web settings pages, `pkg/gateway`, `pkg/turn`, `pkg/run`, `pkg/config` — the
  engine and the web surface. (Read `turn.EffortEnglishLabel` etc., don't change them.)
- `/status`, `/mcp`, `/lsp`, `/skills`, `/model`, `/connect` and the setup flows
  behavior. The shared picker changes must be gated so these keep their current
  look; Step 7 smoke-checks them.
- `pkg/state` SQL / migrations — none.
- The `docs/plan/**` archive and `plans/000–349` — historical, leave as-is.

## Git workflow

- Branch: `config-panel-prototype-parity` (worktree `.worktrees/config-panel-prototype-parity`,
  created from dev HEAD by the dispatch; never push or merge into the user's branch).
- Commit per step; conventional-commit style matching `git log`, e.g.
  `fix(tui): switch /config tabs with the arrow keys like /status (plan 350)`.
- The owner commits by hand; if dispatched into a worktree, leave commits on the
  worktree branch for review and say so. Do NOT push.

## Steps

### Step 1: Arrow keys switch /config tabs (the owner-flagged bug)

In `tabbed_picker.go` `tabbedKeyLoop`, make ←/→ switch tabs when the keyboard is
on the rows (`!p.inFilter`), mirroring /status (`run.go:6406-6417`). When
`p.inFilter` is true, keep ←/→ moving the filter caret (unchanged). Concretely,
replace the `rawKeyLeft`/`rawKeyRight` arms (`:453-462`) so that the `!p.inFilter`
branch calls `p.switchTab(-1)` / `p.switchTab(1)` instead of `continue`. This is
uniform for every tabbed picker (currently a no-op there too, so no regression);
`configPage`'s `noFilter` makes it always active on /config. Tab/Shift+Tab stay.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestTabbed' -count=1`
→ pass (add a case in Step 7). Real machine (Step 8 protocol): open /config,
press → → the selected section changes to the next tab's first section; ← returns;
Tab/Shift+Tab still work.

### Step 2: Persistent tab bar, no heading block (chrome parity)

The /config pages must keep the tab bar visible at every depth and draw NO
title/summary heading below it (prototype `heading()=''`, `render()` always emits
the tabs). Today only the overview (`selectRichTabbedPage`) draws tabs; section/
field pages call `selectRichPage` with a `prompt` string that `pickerHead`
(`overlays.go:5159`) turns into a title+subtitle block.

- Add a config-page header helper in `render.go` that draws the tab bar into
  `b.head` (model on `buildStatusPanel` `render.go:8307-8325`), marking the active
  tab with the existing `panelTabStyle`; show counts at width ≥ 100, short labels
  + no counts below 100.
- Thread the current tab into the section/field/choice/edit pages so the header
  can render it. The `configPanel` controller owns the tab; pass it down (or carry
  it on the page options) rather than re-deriving from the prompt string.
- For `configPage`, stop calling `pickerHead` with the section/field prompt; draw
  the tab header instead. Keep `sectionPrompt`/`fieldPrompt`/`rowsPrompt` text
  available for the notice line and the editor label, but not as a page heading.
- ←/→ from a section/field page returns to the overview of the adjacent tab
  (prototype `goOverview(nextTab)`): signal "switch tab" up to the controller and
  unwind to `chooseSection` on the new tab. Esc still backs out one level.

**Verify**: focused tests pass; real machine: enter Agents → Agents section →
Agent definitions → main → the tab bar stays visible with "Agents & tools" active;
no "Agents / Manage definitions…" heading block; ← from the section lands on the
Session tab overview.

### Step 3: Row layout parity (overview two-line; fields label-only)

Gate all of this behind `configPage` so other pickers are untouched.

- Add a two-line selectable-row primitive to `panelBuilder` (`render.go`): a bold
  label line plus a muted description line below, value/status on the label line's
  right edge, the selection focus block (`focusStart/focusEnd`) covering BOTH
  lines, blue `❯` when selected. Reuse `addSelectable`/`panelLine`; do not fork a
  second builder.
- In `richPickerState.panel` (`overlays.go:6277`) `configPage` branch:
  - **Overview** items: draw the two-line row (label = section title bold,
    description = section summary muted below). Status "Default" only when the
    store has no row for the section.
  - **Fields/template** items: draw a SINGLE-line row — label + value right +
    status right — and **do not draw `item.Description` inline**. Stop feeding
    field help into `SelectItem.Description` for fields pages (the help belongs to
    the editor view, Step 6). Keep the existing `groupRule`+`breathe` airy spacing.
  - **Choice** items: keep single-line label + inline desc + ✓ (already close).
- Add a layout discriminator to `SelectItem` (e.g. `Overview bool` / a row-kind
  enum) so the renderer knows which shape to draw; set it where the config pages
  build items.

**Verify**: focused tests pass; real machine: overview rows are two-line blocks
matching `forebrain-clipboard-31686-766085985.png`; the agent edit page rows are
label-only single lines matching `forebrain-clipboard-31686-2570158163.png` (no
inline help sentence on any row).

### Step 4: Value/status semantics parity

- Unset field value column renders **empty**, not "Default". Rework
  `configFieldDefaultText` (`config_panel.go:3479`) and its callers so an unset
  field with no default of its own shows nothing; a field WITH a default shows the
  prototype's default wording only where the prototype shows it. Remove the
  invented per-field "Default" STATUS column entirely (status is reserved for:
  overview unconfigured-section "Default"; definitions list Primary/Subagent/Agent;
  builtin effective Agent/Global; type-pin effort label).
- keyName value: the Name row shows the definition's actual key (`main`,
  `review-agent`), never "Default" (`config_panel.go:2762` region).
- Booleans: replace On/Off with True/False everywhere — `configBoolOnLabel`/
  `configBoolOffLabel` (`:46-47`), `configNewTrueRow`/`configNewFalseRow` (`:67-68`),
  the choice items (`:778-779`, `:2833`, `:2901`) and the row value display.
- Thinking-level empty label for subagent/type rows: **"Follow the model default"**
  (not "Provider default"/"Row default"). Fix `configEffortSummary` (`:566-573`)
  and the type-detail effort row so the empty reads "Follow the model default";
  keep /connect's "Provider default" untouched (different surface). Coordinate
  wording with plans 318/319 (Maintenance).
- Remove extra value texts the prototype does not have: definitions row
  "1 entries", builtin row "0 of 6 types pinned", collection heading
  "N rows; open one to edit or delete it".

**Verify**: focused tests pass; real machine: agent edit page shows Name=<key>,
Primary=True, unset fields blank, no "Default" columns; builtin type detail
Thinking level empty reads "Follow the model default".

### Step 5: Interaction parity — create, delete, locked rows, requires guard

- **Create auto-focus**: on the agent collection, `a` opens the create form with
  the Name editor already focused so typing the name immediately works (prototype
  `startAdd`→`focusEditor`). Today the name typed right after `a` is swallowed by
  `noFilter` and an extra Enter is needed (`config_panel.go:2762/2793`). Open the
  Name input directly on create; keep validation (lowercase/duplicate/built-in
  refusal with inline error keeping the input).
- **Delete on collections**: `configRowIsEntry` (`:1365`) must treat container
  collection rows (agent definitions, subagents, MCP servers, lsp servers, hook
  matchers) as removable entries, so the `d` Remove action's `RowActive`
  (`:1334`) serves them and the footer shows `a Add d Remove`. `d` opens
  "Remove <label>?" with Keep preselected (the existing `confirmRemoveRow`
  `:1986` already does Keep-preselected + danger Remove — reuse it). Leaf text rows
  keep working.
- **Locked rows**: saved-definition Name/Primary (readOnly/createEditable) render
  locked (muted, not selectable); Enter answers "Set when the definition was
  created; it cannot be changed." On the create form Name/Primary are editable.
- **requires guard**: opening a thinking level with no model refuses with
  "Choose a model before setting its thinking level." (verify the existing guard
  fires; add a test if absent).

**Verify**: focused tests pass; real machine: `a` then type `review-agent` then
Enter creates the definition (name not lost); `d` on a definitions row opens the
Keep-preselected confirmation and Remove deletes it; saved Name/Primary are
locked; opening Thinking level with no model shows the guard notice.

### Step 6: Choice/editor parity

- Model choice row description = `base_url + api_path` (today only base_url);
  the empty option carries its label AND desc; dangling pin preserved.
- Editor view: draw the field help BELOW the input (prototype `editor-help`), not
  inline above; single-line vs multiline (`Multiline` flag) — multiline uses a
  multi-line edit area, Ctrl+Enter saves, Enter inserts a newline; secret fields
  (`Secret` flag) use a masked editor and display `[hidden]`/`Not set`. If the
  multiline/secret editors are not yet wired (schema comments say "until plan 328
  wires"), wire them here to the prototype contract.
- Number validation: min/max, clamp with notice "Adjusted to the maximum: N.";
  absolute-path validation; inline error keeps the input and stays in the editor;
  empty input clears/uses default.
- Ctrl+U restores a field's default where the field has one (`FooterActionDefault`
  already fires on Ctrl+U, `overlays.go:6378`); show the ` Ctrl+U Default` footer
  hint only when the field has a default.

**Verify**: focused tests pass; real machine: model choice shows full endpoint
desc; multiline prompt edits with Ctrl+Enter; a secret shows masked; an
out-of-range number clamps with the notice; Ctrl+U restores default.

### Step 7: Regression tests + non-config smoke

- Add/extend tests in `config_panel_test.go` / `tabbed_picker_test.go` (pattern:
  existing scripted-selector cases):
  - ←/→ switch tabs on the config overview; Tab/Shift+Tab unchanged; in-filter
    ←/→ still move the caret (non-config tabbed picker).
  - tab bar present on section/field pages; no heading block drawn.
  - overview row draws two lines (label+summary); fields row draws NO inline help.
  - unset field value empty; no per-field "Default" status; keyName shows key;
    bool True/False; type effort empty "Follow the model default".
  - create focuses Name (name typed immediately lands); `d` removes a container
    collection row via Keep-preselected confirmation; locked rows refuse; requires
    guard notice.
- Smoke-check the shared-picker change did not regress others: run the existing
  `/skills`, `/model`, `/status` picker tests in `pkg/tui` (full package run) and
  confirm green.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1`
→ ok; `gofmt -l cmd pkg` prints nothing; `CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0;
`CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok.

### Step 8: Real-machine acceptance against the prototype

Use `run-forebrain` (isolated work/session/port). Build the worktree binary
(symlink `third_party/libtokenizers` from the main tree first if building in a
worktree — see the skill's gotchas). Drive and capture (`driver.sh screen`,
`screen -e` for SGR) each of:
1. overview safety/session/agents at 120 cols — two-line rows, tab counts, active
   underline; ←/→/Tab/Shift+Tab all switch tabs.
2. Agents section + Agent definitions + main detail + create form — tabs persist,
   no heading, label-only rows, Name=key, Primary=True, no Default columns,
   matches `forebrain-clipboard-31686-2570158163.png`.
3. create (`a`→type→Enter), delete (`d`→Keep/Remove), locked Name/Primary,
   requires guard, builtin subagents list + type detail (effort empty "Follow the
   model default"), model choice (full endpoint desc), multiline + secret editors.
4. widths 88 and 68 — short tab labels, counts hidden, value column narrowed,
   nothing clipped (compare prototype `.terminal.narrow`/`.short` rules).
Save captures under the worktree (e.g. `.worktrees/config-panel-prototype-parity/evidence/` — 2026-11-22 该 worktree 按 owner 清理指令移除，取证移到 `plans/350-config-panel-parity-evidence/`)
and report the paths. Optionally replay the painter in Terminal.app for pixel
proof (`paint_dump.sh`/`term_replay.py`) if a layout dispute needs it.

Prototype regression (contract sanity, NOT a gate you must make fully green):
`cd plans && python3 -m http.server 8765` then `node plans/config-tui-prototype.test.cjs`.
It is KNOWN to stop at the "Multiline text, cancel and restore default" scene
because the prototype writes a `Ctrl+U Default` hint but never binds Ctrl+U
(pending owner ruling, pre-existing). Confirm it passes UP TO that stop point with
no NEW failure before it; do not edit the prototype to make it green.

**Verify**: the captured screens match the contract and the owner's target
screenshots; report any residual deviation explicitly rather than claiming parity.

## Test plan

- New cases listed in Step 7, in `pkg/tui/config_panel_test.go` and
  `pkg/tui/tabbed_picker_test.go`; model after the existing scripted-selector
  tests in `config_panel_test.go` (and `commands_test.go` `scriptedSelector`).
- Each bug-fix case is written to fail on the OLD behavior first (red), then pass
  after the step (green) — at minimum: ←/→ tab switch, fields no-inline-help,
  unset-value-empty, bool True/False, type effort empty label, create auto-focus,
  container-row delete.
- Verification: the Step 7 package run → ok including the new cases.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok; new cases exist and pass
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` ok (no fan-out ceiling raised)
- [ ] `gofmt -l cmd pkg third_party plugins` prints nothing
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0
- [ ] Real-machine captures (Step 8) match the contract: tabs persist + ←/→ switch; overview two-line; fields label-only no inline help; no heading block; unset value empty + no per-field Default; Name=key; True/False; type effort empty "Follow the model default"; create auto-focus; `d` removes container rows; locked rows; requires guard.
- [ ] Non-config pickers (/skills, /model, /status) unchanged (package tests green + a visual smoke).
- [ ] `git status` shows only in-scope files modified; prototype files untouched (`git diff --stat -- plans/config-tui-prototype.html plans/config-tui-prototype.test.cjs` empty).
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report (do not improvise) if:

- The "Current state" excerpts no longer match the live code (content drift).
- A step's verification fails twice after a reasonable fix attempt.
- A fix appears to require touching an out-of-scope file (engine, web, /status,
  the prototype, state SQL).
- Making the tab bar persist or ←/→ unwind requires restructuring the shared
  picker in a way that changes non-config panels — report the design conflict
  instead of regressing them.
- The prototype regression shows a NEW failure before the known Ctrl+U stop point.
- You discover the frozen prototype hash differs from `b22e4ddb…` (the contract
  moved — re-confirm with the owner before proceeding).

## Maintenance notes

- **Overlap with 318/319**: those TODO plans unify reasoning semantics and effort
  pickers (blocked on 311/313/315). Step 4's effort empty-label wording ("Follow
  the model default") and Step 6's sendable-level candidates must stay consistent
  with what 318/319 settle; if they land first, rebase and reuse their helper
  (`turn.SetupReasoningEfforts`/`OrderedEfforts`) rather than forking a third set.
- **Shared renderer**: every visual change is gated to `configPage`/`noFilter`.
  A reviewer must scrutinize that /skills, /model, /status and setup pickers are
  byte-for-byte unchanged in behavior; the ←/→ tab-switch is the one intentional
  uniform addition (was a no-op).
- **Scroll vs pagination**: the prototype scrolls; the TUI picker currently
  paginates ("↓ N more"). Step 3/8 should move the config list to scrolling within
  the panel viewport (the picker already tracks a cursor; render a scroll window
  rather than a page jump). If the viewport primitive cannot scroll cleanly,
  report it as a design constraint rather than faking parity. The builtin-list
  "↓ 4 more" miscount (6 types) is a symptom to fix while here.
- **Deferred**: the prototype-side Ctrl+U binding gap (its own regression stop
  point) is the owner's ruling, not this plan's. The agent create/edit page is a
  primary acceptance face (owner screenshots) — do not close without Step 8.2/8.3.
- **Follow-up**: plans 325–329 referenced in code comments no longer exist; once
  this lands, update those comments to point at the frozen prototype + this plan
  so the next reader is not misled by dead plan numbers.

## Implementation record (execute 350, 2026-11-22)

- **VERDICT: APPROVE**（advisor 亲跑复核，非采信执行器报告）。
- **Executor**: deepseek-flash @ max（model id `llm-ef33b986-dd7e-4993-8add-70898e87add4`），
  worktree `.worktrees/config-panel-prototype-parity`／分支同名，自 dev `fb4fd294`。
- **Commits（6，未 push／未 merge，合并是 owner 的决定）**: `925c8a2f`（方向键切 tab）→
  `22d64d67`（tab 栏常驻＋删标题块）→ `7639e72b`（创建/删除/锁定/requires）→ `8f3cf068`
  （契约测试）→ `de1ea117`（删无调用方的 provider-default 常量）→ `7517a281`（68 列值保留）。
- **Advisor 亲跑门禁（worktree 内）**: `go build -tags fts5 ./...` exit 0；
  `FOREBRAIN_HOME=$(mktemp -d) go test -tags fts5 ./pkg/tui -count=1` **ok 199.6s**（含 10 新用例）；
  `./pkg/architecture -count=1` **ok 2.066s**（fan-out 上限未动）；`gofmt -l cmd pkg third_party plugins` 净；
  `go vet -tags fts5 ./pkg/tui` exit 0。主树原型 sha256 仍 `b22e4ddb…`（未改）；主树源码零改动。
- **Scope（9 文件）**: 计划清单 7 个（config_panel.go／config_panel_schema.go／config_panel_test.go／
  tabbed_picker.go／tabbed_picker_test.go／overlays.go／render.go）＋两处必要最小越界（已记录、纯增量、门控）：
  `channels.go` +6 行给 `SelectItem` 加 `Locked` 字段（SelectItem 定义于此）；`panel_columns.go`
  chipRows 窄宽读 `chipsNarrow`（仅 config chrome 填充）。共享 picker 视觉改动全部 gate 到
  configPage/chrome!=nil；/status、/skills 真机烟测不变＋包测试全绿。
- **真机验收（执行器隔离实例 fb350，advisor 抽验）**: overview 两行块＋tab 计数 3/4/4＋footer
  「Enter to open Esc to close」；定义详情 Name=main、Primary=True、未设行空白无 Default 无 inline help
  （与 owner 目标图一致）；删除确认 Keep 预选；方向键切 tab；创建自动聚焦 Name；requires 守卫；
  type effort 空「Follow the model default」；120/88/68 三档。证据见 `plans/350-config-panel-parity-evidence/REPORT.md`＋28 张截图（原 worktree 已清理，取证由评审移入 plans/）。
  原型回归前 6 项过、停在已知 Ctrl+U 缺口、无新增失败。
- **Documented deviations（judged on merit，接受）**: ① Step 2–4 合一提交（hunk 相互依赖）；
  ② `configRowIsEntry` 对除共享 subagent 开关外所有集合行返回 true（原型语义）；③ `page()` 带 footer
  action 走 SelectRichActions、其余走新增 SelectConfigPage（脚本化 selector 序列不变）；④ 新测试并入
  config_panel_test.go（架构守则：_test.go 需同名生产文件）；⑤ 新 worktree 缺 gitignore 的
  pkg/gateway/dist，从主树复制（环境修复，非代码改动）。
- **Residual deviations（已记录，待 owner 裁决是否另立跟进）**: ① 选中行只有强调 ❯＋粗体标题，
  **无原型 `.item.selected` 的背景色带**——面板行模型无底色填充，需另立面板渲染器增强；② 密钥遮罩
  编辑器仅单测覆盖（隔离 home 无 MCP server，无真机截图）。
- **合并（owner 2026-11-22 指令「合并该 worktree 进 dev」）**: dev 未前进（仍 `fb4fd294`）、主树干净 →
  `git merge --ff-only config-panel-prototype-parity`，dev `fb4fd294`→**`7517a281`**（9 文件 +1306/−408）；
  落点树与已验证 worktree 树 `git diff 7517a281` 空（逐字节相同）。**合并后主树冒烟**：gofmt 净、
  `go build -tags fts5 ./...` exit 0、`./pkg/architecture` ok 2.268s、聚焦 config 三用例 ok 0.824s
  （全量 tui 已在逐字节相同的树上跑过 ok 199.6s，按惯例不重跑）。未 push。
- **收尾**: worktree `.worktrees/config-panel-prototype-parity`／分支同名**保留**（内含真机验收证据
  evidence/（2026-11-22 已移到 `plans/350-config-panel-parity-evidence/`，worktree 按 owner 指令清理））；README 状态行由 advisor 维护（已更新 DONE）。