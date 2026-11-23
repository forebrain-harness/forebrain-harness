# Plan 350 — real-machine evidence (isolated driver run, `fb350`)

Built from this worktree with `CGO_ENABLED=1 go build -tags fts5`, launched through
`.claude/skills/run-forebrain/driver.sh` with `FOREBRAIN_RUN_WORK=$TMPDIR/fb350`,
`FOREBRAIN_RUN_SESSION=fb350`, `FOREBRAIN_RUN_PORT=8763`.

## Bugs reproduced before any edit (`fb4fd294`, evidence/REPRO.md)

| bug | how | observed |
|---|---|---|
| ←/→ does not switch tabs | `→` on the overview | first row stayed `Approval preset` (safety tab) — `before-arrow-right.txt` |
| create swallows the name | `a` on Agent definitions, then type `review-agent` | nothing changed: `New agent` form, no editor focused — `before-create-swallow.txt` |
| `d` inert on a container row | `d` on `main` | footer showed `a Add` alone, screen unchanged — `before-d-inert.txt` |

## After the change

| capture | what it shows |
|---|---|
| `8-1-overview-120.txt` | overview: two-line blocks, tab bar with counts 3/4/4, footer `↑/↓ to navigate Enter to open Esc to close` |
| `8-1-overview-120-agents.txt` | `→` twice lands on the Agents tab (tab bar + first row) |
| `after-arrow-right.txt` | `→` from safety lands on `Context compaction` (session tab) — bug 1 fixed |
| `8-2-agents-section.txt` | section page: tab bar stays, no heading block, label + value rows only |
| `8-2-definitions-list.txt` | definitions list: `a Add d Remove` in the footer — bug 3 fixed |
| `8-2-definition-detail.txt` | saved definition: Name=`main`, Primary=True, unset rows blank, no Default column |
| `8-3-create-name-focused.txt` | `a` opens the name editor; the typed `review-agent` is in the field — bug 2 fixed |
| `8-3-definitions-after-create.txt` | `main` + `review-agent` after Enter landed the definition |
| `8-3-delete-confirm.txt` | `Remove review-agent?` with `❯ Keep item` preselected |
| `8-3-delete-done.txt` | after confirming Remove, only `main` remains in the store |
| `8-3-editor-help-below.txt` | editor: `New value` label, field bar, help sentence under it |
| `8-3-builtin-subagents.txt` | six built-in types, model + level, no pin counts |
| `8-3-type-detail.txt` | type detail: `Thinking level … Follow the model default` |
| `8-3-requires-guard.txt` | `Choose a model before setting its thinking level.` on the page it was opened from |
| `8-3-model-choice.txt` | model picker: empty option with its own label+desc, table rows with endpoint, ✓ on the current value |
| `8-4-overview-88.txt`, `8-4-overview-68.txt` | short tab labels `Safety Session Agents`, counts hidden, blocks intact |
| `8-3-fields-88.txt` | 88 columns: values right-aligned in a narrowed column |
| `8-4-fields-68.txt` | 68 columns: values kept, status hidden (prototype narrow rules) |
| `8-5-status-smoke.txt` | `/status` unchanged (Status/Usage tabs, own footer) |
| `8-5-skills-smoke.txt` | `/skills` unchanged (title+subtitle head, tab counts, filter box, two-column rows) |

## Residual deviations from the prototype

- The selected row is marked with the accent `❯` only; the prototype also paints a
  background band across the row (`.item.selected`). The panel's line model has no
  background fill, so the band is not drawn.
- No live capture of the masked secret editor: the isolated home has no MCP server
  configured, so no secret row exists to open. Covered by
  `TestConfigPanelMasksSecrets` (the masked editor, empty-handed ask, `Label`).
