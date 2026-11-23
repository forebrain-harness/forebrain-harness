# Plan 316: Remove every middle-dot connector from surface copy — use spaces (composer footer, working / worked-for lines, slash panel included)

> **Executor instructions**: step by step; run each verification; STOP and
> report on any STOP condition; update this plan's README row when done
> unless a reviewer maintains the index.
>
> **Drift check (run first)**: `git status --porcelain -- <in-scope paths>` +
> `git diff <baseline> -- <same paths>`; compare excerpts; content mismatch
> = STOP.

## Status

- **Priority**: P1 (本期 — owner 2026-11-16 点名必须本期修)
- **Effort**: S
- **Risk**: LOW (copy-only; but many golden/pinned tests assert the old
  strings — they must be updated in the same change, never skipped)
- **Depends on**: none (independent of 313/314/315; can run any time this
  period). Coordinate with 313/315 if they touch the same lines concurrently.
- **Category**: dx / ux-copy
- **Planned at**: 2026-11-16, on top of `7611d6cc`

## Why this matters

Owner ruling (2026-11-16): **「我很讨厌文案之间用 · 连接，去掉文案里的所有的 ·，
改成空格」** — every `·` (U+00B7) used to JOIN two pieces of copy becomes a
space, across ALL surfaces. The owner explicitly enumerated, in addition to
the config panel: **the composer footer area of the main-agent view AND the
subagent view, the `working` message line, the `worked for` message line, and
the built-in slash-command panel**. This supersedes the earlier
"deliverable-rows only" no-`·` rule (2026-11-15) with a repo-wide copy rule.
The prototype already forbids `·` (`config-tui-prototype.test.cjs` asserts
`!html.includes('·')`); the real TUI/web code does not yet comply.

## Current state (verified anchors at 7611d6cc; re-grep on your baseline)

> **2026-11-17 reconcile (HEAD `fcb241bc`)**: all cited anchors re-grepped and
> intact (commands.go:818/2753/2768, chat_slash.go:2220/2223/2226,
> SubagentsView.vue:103 — now a template span ` · {{ effortLevelLabel(...) }}`,
> run.go workingStatusFormatter joins, config_panel.go joins). **New `·` sites
> landed since the baseline** (catalog_provider commits) and are IN scope:
> `pkg/tui/setup.go:1054` (`value += " · " + formatTokensCompact(...) + " window"`,
> the Model data fact line; setup.go also holds pre-existing joins at
> :1091/:1954/:1962/:2354), `frontend/src/locales/index.ts:2448/:4974`
> (`models.catalogMatched` zh/en), plus their pinned tests
> (`pkg/tui/setup_test.go:1453`, `ModelFormDrawer.test.ts:265`) — update the
> assertions in the same change. The sweep grep below remains the authoritative
> enumeration (16 non-test pkg/tui files hold `·` today; locales holds 166
> lines incl. page titles — the ruling covers every connector). A parallel
> session's uncommitted WIP touches setup.go copy (pickProvider subtitle
> removal, no `·`); coordinate before dispatch. Step 6 anchor verified live:
> `BuiltinApprovalPresets` workspace_write `Label: "Default"` at
> permission_types.go:646.

Middle-dot join sites in `pkg/tui` (non-test), each to become a space:

- **worked-for line**: `commands.go:818`
  `Frame{... Title: strings.Join(parts, " · ") ...}` (replayWorkedFrame /
  worked completion; parts = counters + `workedCompletionTime`).
- **working line**: `run.go` `workingStatusFormatter` (:4799+):
  `title += " · " + counters` (~:4830), `title += " · " + wait` (~:4836),
  `fmt.Sprintf("Checking whether the goal is met · after round %d", …)`
  (:4849), `fmt.Sprintf("Working toward the goal · round %d", …)` (:4851).
  (The same formatter serves the main view and the subagent view — one fix
  covers both; verify by grepping its callers.)
- **composer footer / status joins**: `commands.go:2753`, `:2768`
  `strings.Join(parts, " · ")`; `commands.go:2342`, `:2355`
  `status + " · " + invocation`; `commands.go:818` above. Grep
  `strings.Join(parts, " · ")` and `" · "` across pkg/tui for the footer
  stats join (render.go / run.go footer) and fix every hit.
- **built-in slash-command panel**: `chat_slash.go:2220` `"· "+state`,
  `:2223` `"· adds "+cmd`, `:2226` `"· shadows "+shadowed`; plus
  `:1278`/`:1281` `": authenticated · …"`, `:1847`/`:1849`
  `"Enabled/Disabled <name> · new sessions …"`.
- **config panel**: `config_panel.go:278,281,283,285,381,384,386,406,651`
  and the `editRow`/`addRow`/`rowsPrompt` prompts (`field.Label+" · "…`).
- **other TUI copy**: `chat_surface.go:511` (`Title + " · " + status`),
  `chat_session.go:2928` (`"ran %s · exit %d"`), `commands.go:2549`
  (`"Full Access · fixed by --yolo"`), `:3025`/`:3027` (rewind summaries).
- **web copy**: `frontend/src/views/SubagentsView.vue:103`
  (`' · ' + effortLevelLabel(...)`) and any other `' · '` / `" · "` join in
  frontend/src (grep). Replace with a single space.

NOT in scope: the `•` bullet (U+2022, e.g. run.go working line
"• esc to interrupt") and `/` inside `provider/model` identifiers — the
ruling targets the middle-dot connector `·` only. Do not change identifier
slashes.

Conventions: copy stays English in TUI, i18n in web; keep wording otherwise
identical (only the connector changes); update every golden/pinned test that
asserts an old `·` string in the same commit (grep tests for `·`).

## Commands you will need

```bash
export CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```
Sweep enumeration (run first, record the hit list):
```bash
grep -rn '·' pkg/tui pkg/turn --include=*.go | grep -v _test
grep -rn '·' frontend/src --include=*.vue --include=*.ts | grep -v test
```
Gates: `go build -tags fts5 ./...`; `gofmt -l pkg/tui pkg/turn`;
`FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/tui ./pkg/turn -count=1`;
`cd frontend && pnpm typecheck && pnpm vitest run && pnpm run build`;
prototype regression unchanged (26 passed).

## Scope

**In scope**: the `pkg/tui` / `pkg/turn` files holding the joins above +
their tests; `frontend/src` copy joins + their tests; `plans/README.md` row.
**Out of scope**: identifier slashes, `•` bullets, logic/behavior, frozen
prototypes, setup.go semantics (copy-only there if a `·` exists).

## Steps

1. Enumerate every `·` join (commands above). For each, replace the
   `" · "` / `"· "` connector with a single space, keeping both operands and
   all other wording byte-identical. Work file by file; after each file run
   its package tests and fix the golden assertions that pinned the old
   string (update expected strings, never delete/skip the test).
2. Cover the owner-enumerated surfaces explicitly and verify by rendering:
   composer footer (main + subagent), working line, worked-for line, slash
   panel. Add/adjust a pinned test per surface asserting the space-joined
   form (model on the existing footer/working-line tests).
3. Web joins → space; update SubagentsView tests.
4. Re-run the sweep grep: zero `·` joins remain in non-test surface copy
   (tests may contain `·` only inside assertions of the NEW space form, i.e.
   none). Full gates + prototype regression.
5. Update README row.

### Step 6: unify the approval-preset display name to "Workspace write"

Owner 2026-11-16: the `/permissions` picker's "Default" and the `/config`
approval preset's "Workspace access" (prototype wording) are the SAME preset
(`workspace_write`) and must both read **"Workspace write"** on every surface.
The stored value stays `workspace_write`; only the display label changes.

- `pkg/safety/permission_types.go:646` — `BuiltinApprovalPresets()`
  workspace_write `Label: "Default"` → `"Workspace write"`. This Label feeds
  the `/permissions` picker (`pkg/tui/commands.go:2570`) and every consumer.
- `pkg/tui/config_panel_schema.go:117` — `OptionLabels`
  `"workspace_write": "Default"` → `"Workspace write"`.
- Web i18n: `frontend/src/locales/index.ts` `settings.preset.workspaceWrite`
  (:335 zh) and `approval.default` (:446 zh) plus their en counterparts →
  "Workspace write" (consumed by `ApprovalPresetPicker.vue:88` and
  `settingsSchema.ts:127`). Keep the product term identical in zh/en.
- Tests asserting the old label: `pkg/tui/commands_test.go:798,818` and
  `pkg/safety/permission_rules_test.go:1020` → "Workspace write".
  (`overlays_test.go` "Default" labels are generic select items, NOT this
  preset — leave them.)
- The prototype is already unified (`approval_preset` option label
  "Workspace write"); keep it as the contract.

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" go test -tags fts5 ./pkg/safety ./pkg/tui -run 'Permission|Preset|Approval' -count=1 -v` → `--- PASS`, no `[no tests to run]`; `cd frontend && pnpm vitest run src/components/chat/ApprovalPresetPicker.test.ts` exit 0; `grep -rn "Workspace access" pkg/ frontend/src plans/config-tui-prototype.html` empty.

## Done criteria

- [ ] `grep -rn '·' pkg/tui pkg/turn --include=*.go | grep -v _test` empty
- [ ] `grep -rn '·' frontend/src --include=*.vue --include=*.ts | grep -v test` empty
- [ ] build/gofmt/package tests/frontend gates green; prototype 26 passed
- [ ] pinned tests exist for footer(main+subagent)/working/worked-for/slash-panel space-joined copy
- [ ] approval preset reads "Workspace write" on /permissions, /config and web (Step 6); no "Workspace access"/preset-"Default" label remains
- [ ] no behavior change; no out-of-scope edits; README row updated

## STOP conditions

- A `·` appears inside a model-visible prompt/tool description whose wording
  is golden-pinned by a spec appendix (changing it would break a contract
  beyond copy) — report, do not silently rewrite spec text.
- A test cannot be updated without weakening what it pins — report.
- The sweep reveals `·` in a surface outside pkg/tui/pkg/turn/frontend
  (e.g. channel notifications) — include it only if it is user-facing copy;
  otherwise report.
- A verification fails twice after a reasonable fix.

## Maintenance notes

- Standing rule: no `·` connectors in any copy, ever (owner taste). New code
  must join copy with spaces; reviewers reject `·`.
- The `•` bullet and identifier `/` are deliberately untouched; if the owner
  later extends the ban, that is a new ruling.

## 2026-11-17 reasoning-semantics collision (audit)

> Prototype gate: **owner re-confirmed the revised config prototype on
> 2026-11-17** ("原型都已确认"); its hash is now `95e75ded…e688` and the
> regression is 26/26 (the audit added one check).

`pkg/tui/setup.go:1069-1081` (`customThinkingLevels`) is touched by both this
plan and plan 311:

- this plan removes the `·` from its labels (e.g. `low · 低` becomes
  `low 低` or a two-column label); plan 311 adds the seventh item `Off` and
  renames the English `xhigh` label from `Highest` to `xHigh` (the shared
  `turn.EffortEnglishLabel` spelling).
- Apply this plan first, then 311. If 311 lands first, rebase this plan on it
  and keep the `Off` item and `xHigh` spelling — do not restore `Highest` or
  the `·`.
- The same label table drives the `/connect` effort page and the docs
  prototype `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`
  (`Highest` → `xHigh` in the audit's prototype fix); any new `·`-free copy
  must use the resulting names.