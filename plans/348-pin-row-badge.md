# Plan 348: Mark a pinned model row on the models page

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**. This plan starts on top of **plan 347**
> (`plans/347-pin-labels-and-refusal-copy.md`), which is not merged yet, so
> the anchor is content, not a commit: line numbers below are anchors only.
> Run these and compare with the "Current state" excerpts:
>
> ```bash
> git status --porcelain -- frontend/src/components/models/ModelsTable.vue \
>   frontend/src/components/models/ModelsTable.test.ts \
>   frontend/src/lib/api.ts frontend/src/lib/modelPins.ts \
>   frontend/src/locales/index.ts frontend/e2e/settings-models.spec.ts
> grep -c 'export function modelPinLabels' frontend/src/lib/modelPins.ts
> grep -c 'json:"kind"' pkg/gateway/models_api.go
> grep -c 'pinnedBy?: LlmModelPinRef\[\]' frontend/src/lib/api.ts
> ```
>
> Expected: clean status, `1`, `1`, `1`. Anything else means plan 347 is not
> in this tree (STOP condition 1) or the code moved — re-locate by the quoted
> symbols before proceeding. **Content mismatch is a STOP condition**; moved
> lines alone are not.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW (one badge in a table cell; no behavior changes, the remove
  button stays enabled)
- **Depends on**: plan 347 (the structured `pinned_by` payload and
  `src/lib/modelPins.ts`). Execute after 347 is approved and present in the
  working branch; rebase this worktree on 347's tip before starting.
- **Category**: tech-debt / UX (the wire field finally has its consumer)
- **Planned at**: after `ca703193` + plan 347, 2026-11-21

## Why this matters

Plan 345 taught the delete guards to refuse a row that a subagent type, the
plan reviewer, the goal check or a subagent definition pins by id — and the
API has carried `pinned_by` on every models-page row since then, but **nothing
reads it**. A person therefore learns a row cannot be deleted only by trying:
the confirm dialog, then a refusal. This plan puts the fact on the row itself,
with the owners' names in the tooltip, reusing the localized labels plan 347
introduced. The remove button stays enabled on purpose: the refusal dialog is
where the reason and the "change those references first" remedy are spelled
out, and the server guard stays the one truth.

## Current state

Read at `ca703193` plus the plan-347 work.

- `frontend/src/components/models/ModelsTable.vue` — the table's cells. The
  model cell (`:10-14`) already draws one badge for the default:

```vue
    <template #cell-model="{ row }">
      <span class="mname" data-testid="models-cell-model">{{ row.modelName || row.model }}</span>
      <BadgeComponent v-if="row.isDefault" class="ml-1.5" data-testid="models-cell-default">{{ t('models.defaultBadge') }}</BadgeComponent>
      <span class="mprov">{{ providerLine(row) }}</span>
    </template>
```

  It imports `useI18n` (`:96`) and has `const { t } = useI18n()` (`:110`).
  The remove button (`:60-67`, `data-testid="models-remove-${row.id}"`) emits
  `remove`; leave it as it is.
- `frontend/src/components/models/ModelsTable.test.ts` — the table's unit
  test. It does `setLocale('en')` in `beforeAll`, builds rows with

```ts
function row(overrides: Partial<LlmModelRow> = {}): LlmModelRow {
  return { id: 'r1', ..., isDefault: false, createdAt: 0, ...overrides }
}
```

  and mounts through a `mountTable(rows)` helper. Follow it.
- `frontend/src/lib/api.ts` — plan 347 added `LlmModelPinRef { kind: string;
  name?: string }` and `LlmModelRow.pinnedBy?: LlmModelPinRef[]`.
- `frontend/src/lib/modelPins.ts` — plan 347 added
  `modelPinLabel(t, pin)` and `modelPinLabels(t, pins)`; the locale keys are
  `models.pinSubagent` (`子代理 {name}` / `subagent {name}`),
  `models.pinPlanReview` (`计划评审` / `plan review`), `models.pinGoalCheck`
  (`目标评估` / `goal check`).
- `frontend/e2e/settings-models.spec.ts:245-283` — the pinned-row case, which
  (after plan 347) cleans its pin up at the end. This plan adds one assertion
  inside it, before the remove click.
- `BadgeComponent` (`frontend/src/components/common/BadgeComponent.vue`)
  takes `variant: 'default' | 'gray' | 'success' | 'warning' | 'error'`; the
  default badge uses the default variant.

The wire shape (plan 347, `pkg/gateway/models_api.go`):

```go
type llmModelPin struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}
```

so one pinned row answers e.g. `"pinned_by": [{"kind": "plan_review"}]` and an
unpinned one omits the field (`omitempty`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Frontend types | `cd frontend && corepack pnpm typecheck` | exit 0 |
| Frontend unit tests | `cd frontend && corepack pnpm test -- --run src/components/models/ModelsTable` | all pass |
| Browser e2e | `FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh` | ends with `web e2e: PASS` |

The e2e harness builds the binary and the UI; the data volume had ~5 GiB free
at planning time. On `ENOSPC`, stop and report — delete nothing outside the
repo.

## Scope

**In scope** (the only files you should modify):

- `frontend/src/components/models/ModelsTable.vue` — the pinned badge
- `frontend/src/components/models/ModelsTable.test.ts` — its unit test
- `frontend/src/locales/index.ts` — `models.pinnedBadge`, `models.pinJoin`, zh + en
- `frontend/e2e/settings-models.spec.ts` — the row assertion in the pinned case
- `plans/README.md` — this plan's status row

**Out of scope** (do NOT touch, even though they look related):

- The remove button, `ModelsTab.vue`'s confirm/refusal dialogs, and anything
  under `pkg/` — the guard and the payload are plan 345/347's and stay as they
  are.
- Disabling the remove button for a pinned row: the refusal dialog is the
  place that explains the remedy, and a disabled button would hide it.
- `frontend/src/components/settings/ModelsTab.vue` — that page's badge list is
  the *refusal*; this plan is the *row*.
- Any locale key that already exists (`models.defaultBadge` etc.).

## Git workflow

- Worktree `.worktrees/348-pin-row-badge` in the repo's main worktree, branch
  `348-pin-row-badge`, created **from plan 347's tip** (not from `dev`, which
  lacks both plans). Record `BASE=$(git rev-parse HEAD)` before the first edit.
- A fresh worktree lacks gitignored build deps for the e2e run; copy them in
  the same way plan 347's Git workflow section shows (`third_party/pdfium`,
  `third_party/libtokenizers`, `pkg/gateway/dist` from the main worktree).
- Commit as one or two conventional commits matching `git log` (e.g.
  `feat(web): mark a model row a setting pins on the models page (plan 348)`).
- Do **NOT** push, merge, or touch the user's branch — the owner decides
  landing.

## Steps

### Step 1: The locale keys

In `frontend/src/locales/index.ts`, after the `models.pin*` keys plan 347
added (zh block, then the en block in the same order):

```ts
  'models.pinnedBadge': '被引用',
  'models.pinJoin': '、',
```
```ts
  'models.pinnedBadge': 'Referenced',
  'models.pinJoin': ', ',
```

**Verify**: `cd frontend && corepack pnpm typecheck` → exit 0 (the en map is
`Record<keyof typeof zh, string>`, so a missing half is a type error).

### Step 2: The badge on the model cell

In `frontend/src/components/models/ModelsTable.vue`, right after the default
badge in `#cell-model`:

```vue
      <BadgeComponent
        v-if="row.pinnedBy?.length"
        class="ml-1.5"
        variant="gray"
        :title="pinnedTitle(row)"
        data-testid="models-cell-pinned"
        >{{ t('models.pinnedBadge') }}</BadgeComponent
      >
```

and in the script block, beside the other helpers:

```ts
/** The owners a pinned row's badge names, in the reader's language. */
function pinnedTitle(row: LlmModelRow): string {
  return modelPinLabels(t, row.pinnedBy).join(t('models.pinJoin'))
}
```

with `import { modelPinLabels } from '@/lib/modelPins'` at the top of the
script imports, and `LlmModelRow` already imported as a type (check the
existing import; keep it type-only).

**Verify**: `cd frontend && corepack pnpm typecheck` → exit 0.

### Step 3: The unit test

In `frontend/src/components/models/ModelsTable.test.ts`, add one case in the
existing `describe`:

```ts
  it('marks a row a setting pins, and names the owners in the title', () => {
    const wrapper = mountTable([
      row({ id: 'pinned', model: 'pinned-model', pinnedBy: [{ kind: 'plan_review' }, { kind: 'subagent', name: 'explore' }] }),
      row({ id: 'plain', model: 'plain-model' }),
    ])
    const badge = wrapper.find('[data-testid="models-cell-pinned"]')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe('Referenced')
    expect(badge.attributes('title')).toBe('plan review, subagent explore')
    // The plain row draws no badge.
    expect(wrapper.findAll('[data-testid="models-cell-pinned"]')).toHaveLength(1)
  })
```

(If the mounted wrapper needs the locale set, the file's `beforeAll` already
does `setLocale('en')`.)

**Verify**: `cd frontend && corepack pnpm test -- --run src/components/models/ModelsTable` → all pass, including the new case.

### Step 4: The browser assertion

In `frontend/e2e/settings-models.spec.ts`, inside the pinned-row case, after
the row is asserted visible and **before** clicking
`[data-testid^="models-remove-"]`:

```ts
  // The row says it is pinned before anyone clicks: the badge names the
  // setting in the tooltip, and the refusal dialog then explains the remedy.
  const pinnedBadge = row.locator('[data-testid="models-cell-pinned"]')
  await expect(pinnedBadge).toBeVisible()
  await expect(pinnedBadge).toHaveAttribute('title', '计划评审')
```

**Verify**: `FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`.

### Step 5: Frontend gates

```bash
cd frontend && corepack pnpm typecheck && corepack pnpm test && corepack pnpm build
```

Expected: typecheck exit 0, vitest all pass, build exit 0.

## Test plan

| Where | Test | Covers |
|---|---|---|
| `frontend/src/components/models/ModelsTable.test.ts` | the new case | badge only when `pinnedBy` is non-empty; localized label; owners in the title |
| `frontend/e2e/settings-models.spec.ts` | the pinned case (one assertion added) | the badge reaches a real pinned row in the zh UI |

Regression pins that must pass unchanged: every existing case in
`ModelsTable.test.ts` (the default badge, the model label, the remove button)
and the whole `settings-models.spec.ts` file (plan 347 made it leave no pin
behind).

## Done criteria

ALL must hold:

- [ ] `cd frontend && corepack pnpm typecheck` exit 0.
- [ ] `cd frontend && corepack pnpm test` — all pass; the new ModelsTable case
      exists and passes.
- [ ] `FOREBRAIN_E2E_SPEC=e2e/settings-models.spec.ts scripts/acceptance/web_e2e.sh`
      → `web e2e: PASS`.
- [ ] `grep -c 'models-cell-pinned' frontend/src/components/models/ModelsTable.vue` ≥ 1
      and the remove button's binding is unchanged
      (`git diff "$BASE" -- frontend/src/components/models/ModelsTable.vue`
      shows no change to the `models-remove` block).
- [ ] `git diff --name-only "$BASE"` ⊆ the in-scope list (+ `plans/README.md`) —
      no `pkg/` file, no `ModelsTab.vue`.
- [ ] `plans/README.md` status row updated (DONE / BLOCKED with reason).

## STOP conditions

Stop and report (do not improvise) if:

1. `frontend/src/lib/modelPins.ts` does not exist, or `LlmModelRow.pinnedBy`
   is missing, or `pinned_by` is still `[]string` on the wire — plan 347 is
   not in this tree; do not re-implement it here.
2. The models page turns out to render rows from a different component (the
   rows are drawn by `ModelsTable.vue`'s `DataTable` cells today) — report
   where they come from before editing.
3. A step's verification fails twice after a reasonable fix attempt.
4. The change appears to require an out-of-scope file (a `pkg/` change, or
   `ModelsTab.vue`).

## Maintenance notes

- **The badge is a fact, not a permission.** The remove button stays enabled;
  the server guard refuses and the dialog explains. If someone later wants the
  button disabled, they must keep a path to that explanation.
- **One label source**: the badge text is a locale key and the tooltip reuses
  `modelPinLabels`, so a new pin kind added to plan 347's vocabulary shows up
  here with no code change beyond its locale keys.
- `models.pinJoin` exists only to join the tooltip in the reader's language;
  if another surface needs a list separator, promote it rather than copying
  it.
- Reviewer scrutiny: the badge must not appear for a row whose only reference
  is an agent's `default_model` (that is `default_for`, already a badge), and
  the title must be localized, not the raw kinds.
