# Plan 244: an interrupted subagent ends as "interrupted", not as a red failure

> **Executor instructions**: Follow the steps in order and confirm each
> verification. If a STOP condition occurs, stop and report — do not improvise.
> Do **not** commit, push or merge: leave the change in the working tree and
> report it. When done, update this plan's row in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat 69ab0706..HEAD -- frontend/src/composables/useChatStream.ts frontend/src/components/chat/RunStoppedLine.vue frontend/src/components/chat/SubagentConversation.vue frontend/src/components/chat/SubagentCallCard.vue frontend/src/lib/subagentStatus.ts`
> Compare the "Current state" excerpts with the live code; content mismatch is a
> STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW — one block carries two more fields and one status stops being
  collapsed; the drawn text for a plain stop stays "Cancelled"/"已取消".
- **Depends on**: none
- **Category**: bug (web)
- **Planned at**: commit `69ab0706`, 2026-11-05
- **Finding**: branch audit 2026-11-05 (`origin/dev..HEAD`), finding 9
  (`introduced` with the subagent-interrupt feature)

## Why this matters

The engine publishes two different endings for an execution the user stopped:

- `Status: "cancelled"` — the user pressed stop;
- `Status: "interrupted"` — the execution's approval wait could not be resumed,
  and the payload carries the sentence explaining why
  (`pkg/gateway/api_extra.go:485-495`).

The web collapses both. Three symptoms, all visible at once:

1. The card row turns **red** with the interrupt's explanation, because
   `subagentEndStatus` maps anything with an error to `failed` and the row renders
   `row.error` in the danger colour.
2. The transcript line says **"Cancelled"** for both, because `RunStoppedLine`
   hardcodes that locale key and never receives the status.
3. On a reload, the ledger path drops `interrupted` to `done` — the tab header
   then claims the execution finished normally while a stopped line sits under it.

The operator cannot tell a deliberate stop from an aborted wait, and the one place
that still holds the reason is the red line the design explicitly says a stop must
not have (`RunStoppedLine`'s own comment: "A stop is not a failure").

## Current state

`frontend/src/composables/useChatStream.ts:2515-2519` — the collapse:

```ts
function subagentEndStatus(status: string, error: string): 'done' | 'failed' | 'cancelled' {
  if (status === 'cancelled') return 'cancelled'
  if (status === 'failed' || error) return 'failed'
  return 'done'
}
```

`useChatStream.ts:2474-2476` — the row union has no `interrupted`:

```ts
export interface SubagentCallRowView {
  status: 'waiting' | 'running' | 'done' | 'failed' | 'cancelled' | 'skipped'
```

`useChatStream.ts:2571-2577` — the row's error text, which is what paints red:

```ts
      ...(status !== 'waiting' && subagentTaskTerminal(status) && String(bound?.error ?? facts.error ?? '').trim()
        ? { error: String(bound?.error ?? facts.error ?? '').trim() }
        : {}),
```

`useChatStream.ts:1566-1568` — the block that is drawn, with no status:

```ts
  | { kind: 'stopped'; id: string }
```

`useChatStream.ts:5263-5266` — the live path (status known, discarded):

```ts
	  const stopped = transcriptStatus === 'cancelled' || transcriptStatus === 'interrupted'
	  if (stopped && !blocks.some((block) => block.kind === 'stopped' && block.id === `stopped:${executionId}`)) {
		blocks = [...blocks, { kind: 'stopped' as const, id: `stopped:${executionId}` }]
	  }
```

`useChatStream.ts:6697-6705` — the reload path (status collapsed a second time):

```ts
			status: status === 'cancelled' ? 'cancelled' : status === 'failed' ? 'failed' : status === 'running' ? 'running' : 'done',
			blocks: [
			  …
			  ...(status === 'cancelled' || status === 'interrupted'
				? [{ kind: 'stopped' as const, id: `stopped:${executionId}` }]
```

`frontend/src/components/chat/RunStoppedLine.vue:1-11` — the line, with the label
hardcoded (`chat.subagentCancelled`) and no props at all.

`frontend/src/components/chat/SubagentConversation.vue:113-116` — where it is drawn:

```vue
          <RunPausedLine v-else-if="run.item.kind === 'paused'" />
          …
          <RunStoppedLine v-else-if="run.item.kind === 'stopped'" />
```

`frontend/src/components/chat/SubagentCallCard.vue:35-38,53` — the icons and the
red error line:

```vue
        <Ban v-else-if="row.status === 'cancelled'" class="… text-[var(--forebrain-muted-text)]" />
…
          <span v-if="row.error" class="mt-0.5 block whitespace-pre-wrap text-xs leading-relaxed" :class="row.status === 'skipped' ? '…warning…' : '…danger…'">{{ row.error }}</span>
```

`frontend/src/lib/subagentStatus.ts:33-49` — the vocabulary that already knows both
words (`chat.subagentCancelled` / `chat.subagentInterrupted`,
`frontend/src/locales/index.ts:1804-1805` zh, `:4013-4014` en).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Types | `cd frontend && pnpm typecheck` | exit 0 |
| Unit tests | `cd frontend && pnpm vitest run src/components/chat src/composables/useChatStream.test.ts src/lib/subagentStatus.test.ts` | all pass |
| Build | `cd frontend && pnpm build` | exit 0 |
| E2E (task-scoped) | `FOREBRAIN_E2E_SPEC="e2e/subagent-conversation.spec.ts" ./scripts/acceptance/web_e2e.sh` | pass |

The whole browser suite is a release gate; run only the spec named above.

## Scope

**In scope**:
- `frontend/src/composables/useChatStream.ts`
- `frontend/src/components/chat/RunStoppedLine.vue`
- `frontend/src/components/chat/SubagentConversation.vue`
- `frontend/src/components/chat/SubagentCallCard.vue`
- `frontend/src/lib/subagentStatus.ts` (only if a shared label helper is reused)
- the matching `*.test.ts` files, and `frontend/e2e/subagent-conversation.spec.ts`
  only if its existing assertion changes

**Out of scope**:
- `pkg/gateway/api_extra.go` — the engine's `interrupted` payload is correct.
- The TUI (`pkg/tui`): it *emits* `interrupted` (`chat_session.go:1154`,
  `Status: "interrupted"` with the reason) but renders no dedicated frame for
  it — `subagentClosingFrames` (`reducer.go:2192-2198`) branches only
  `failed`/`cancelled`. The mislabelling this plan fixes is the web's; giving
  the TUI its own interrupted wording is the deferred follow-up (maintenance).
  Do not edit `pkg/tui` here.
- `RunPausedLine.vue` and the pause/resume lifecycle (plan-230-era behaviour).
- `SubagentDrawer.vue`'s header — it already uses `subagentStatusLabel`.

## Steps

### Step 1: carry the status and the reason on the block

`useChatStream.ts:1566-1568`:

```ts
  | { kind: 'stopped'; id: string; status: 'cancelled' | 'interrupted'; reason?: string }
```

`status` is required: a stopped block whose ending is unknown is exactly the bug.

### Step 2: set them on both paths

Live path (`:5263-5266`) — `transcriptStatus` is already narrowed there:

```ts
	  const stopped = transcriptStatus === 'cancelled' || transcriptStatus === 'interrupted'
	  if (stopped && !blocks.some((block) => block.kind === 'stopped' && block.id === `stopped:${executionId}`)) {
		blocks = [...blocks, {
		  kind: 'stopped' as const,
		  id: `stopped:${executionId}`,
		  status: transcriptStatus,
		  ...(transcriptStatus === 'interrupted' && errText ? { reason: errText } : {}),
		}]
	  }
```

Reload path (`:6697-6705`): keep the status instead of collapsing it, and pass the
record's own reason.

```ts
			status: status === 'cancelled' ? 'cancelled' : status === 'interrupted' ? 'interrupted' : status === 'failed' ? 'failed' : status === 'running' ? 'running' : 'done',
			…
			  ...(status === 'cancelled' || status === 'interrupted'
				? [{ kind: 'stopped' as const, id: `stopped:${executionId}`, status: status as 'cancelled' | 'interrupted',
				     ...(status === 'interrupted' && String(record.error ?? '').trim() ? { reason: String(record.error).trim() } : {}) }]
```

**Verify**: `cd frontend && pnpm typecheck` → exit 0

### Step 3: say the right word, and say why

`RunStoppedLine.vue` — take props and label them:

```vue
<script setup lang="ts">
/**
 * How an execution the user stopped ended, in the place it stopped. A stop is
 * not a failure … (keep the existing comment)
 */
import { Ban } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import { computed } from 'vue'

const props = withDefaults(defineProps<{ status?: 'cancelled' | 'interrupted'; reason?: string }>(), {
  status: 'cancelled',
  reason: '',
})
// The drawer's own vocabulary, so the word a stop is called is chosen once.
const label = computed(() => subagentStatusLabel(props.status))
</script>
```

(`subagentStatusLabel` from `@/lib/subagentStatus` — not an inline
`t(status === 'interrupted' ? 'chat.subagentInterrupted' : 'chat.subagentCancelled')`.
The done criterion below greps for `chat.subagentCancelled` in this file and
requires it to be **absent**: the label is chosen by the shared helper, the way
`SubagentDrawer.vue`'s header already does it, so the two surfaces cannot drift
apart. An earlier draft of this step showed the inline ternary, which cannot
satisfy that criterion.)

Template: the label becomes `{{ label }}`; after the label add, only when
`reason` is non-empty:

```vue
    <span v-if="reason" class="min-w-0 truncate text-[var(--forebrain-muted-text)]" :title="reason">{{ reason }}</span>
```

Keep the element's `data-testid="run-stopped-line"` — the e2e spec and the TUI
parity note both rely on it.

`SubagentConversation.vue:115`:

```vue
          <RunStoppedLine v-else-if="run.item.kind === 'stopped'" :status="run.item.status" :reason="run.item.reason" />
```

**Verify**: `cd frontend && pnpm typecheck` → exit 0

### Step 4: stop painting the stop red on the card row

- `subagentEndStatus` (`:2515`) returns `'interrupted'` for that status instead of
  `'failed'`; widen its return type to include `'interrupted'`.
- `SubagentCallRowView['status']` gains `'interrupted'`.
- `subagentTaskTerminal` (`:2510`) already lists the four terminal statuses — do
  **not** add `interrupted` there (it is not a task outcome the tally counts).
- The row's **clock** is the one place that is not a tally: `subagentCallView`
  gives a row its `final` duration only where `subagentTaskTerminal(status)` is
  true, and for a single-task card that row clock is the only duration there is
  (`headerDuration` is query-only or multi-task only). So the clock branch gets
  its own settled-ending test —
  `const settledEnding = subagentTaskTerminal(status) || status === 'interrupted'`
  — used by nothing else. Without it an interrupted row silently loses the time
  it ran, which the fold to `failed` used to show.
- `subagentCallView`'s error field: suppress it for both stops —

```ts
      ...(status !== 'waiting' && subagentTaskTerminal(status) && status !== 'cancelled' && status !== 'interrupted'
        && String(bound?.error ?? facts.error ?? '').trim()
        ? { error: … }
        : {}),
```

The reason now lives on the stopped line inside the transcript, where the design
puts it.

- `SubagentCallCard.vue:36` — draw both stops the same muted way:

```vue
        <Ban v-else-if="row.status === 'cancelled' || row.status === 'interrupted'" … />
```

(`Ban` must come before any generic branch that could catch `interrupted`; it
already precedes `failed`.)

**Verify**: `cd frontend && pnpm typecheck` → exit 0

## Test plan

- `frontend/src/composables/useChatStream.test.ts` — add three cases:
  (a) a live `subagent_ended` with `status: 'interrupted'` + `error` produces a
  `stopped` block with `status: 'interrupted'` and that reason, and the card row
  has no `error`; (b) `status: 'cancelled'` produces the same block with
  `status: 'cancelled'` and no reason; (c) a replayed `interrupted` record keeps
  `status: 'interrupted'` on the transcript entry (today it becomes `done`).
- `frontend/src/components/chat/SubagentCallCard.test.ts` — an `interrupted` row
  draws the muted ban and no danger text.
- `frontend/src/components/chat/SubagentConversation.test.ts` — a `stopped` block
  with `status: 'interrupted'` + reason renders "已中断"/"Interrupted" and the
  reason; the existing `{ kind: 'stopped', id: 'stopped:exec-1' }` fixture must be
  updated to carry a status (that update is part of this plan).
- `frontend/e2e/subagent-conversation.spec.ts:228` — the pressed path is a stop
  (`[data-testid="subagent-stop"]`), so `toHaveText('Cancelled')` at `:228`
  (with the `:232-233` count assertion on the same word) stays correct;
  only touch them if the run shows otherwise, and report the change.
- Negative control: with step 3 reverted, the new "Interrupted" case fails.

## Done criteria

ALL must hold:

- [ ] `cd frontend && pnpm typecheck` exits 0
- [ ] The three vitest areas named above pass, with the new cases visible
- [ ] `cd frontend && pnpm build` exits 0
- [ ] `FOREBRAIN_E2E_SPEC="e2e/subagent-conversation.spec.ts" ./scripts/acceptance/web_e2e.sh` passes
- [ ] `grep -n "chat.subagentCancelled" frontend/src/components/chat/RunStoppedLine.vue` returns nothing (the label is chosen, not hardcoded)
- [ ] `grep -n "status !== 'cancelled' && status !== 'interrupted'" frontend/src/composables/useChatStream.ts` matches
- [ ] Only in-scope files modified (`git status`)
- [ ] `plans/README.md` row for 244 updated

## STOP conditions

- `subagentTaskTerminal` turns out to gate something else that needs
  `interrupted` (a tally, a dedupe key): report where, instead of adding it —
  the tally counts outcomes and an interrupted execution is not one.
- The e2e spec's assertion changes (the fake provider's stop path reports
  `interrupted` rather than `cancelled`): report the payload the engine actually
  sent, and adjust the assertion to the word the payload justifies — do not
  weaken it to a regex that accepts both.
- The TUI turns out to render `interrupted` somewhere already (`grep -rn
  interrupted pkg/tui`): report it so the two surfaces can be aligned in a
  follow-up; do not edit `pkg/tui` here.

## Maintenance notes

- The invariant: **the word and the reason come from the payload, once.** A
  second place that says "Cancelled" for any stop re-introduces the bug.
- A reviewer should check that a plain stop's rendering is byte-identical to today
  ("Cancelled"/"已取消", one line, muted Ban) and that no red text survives for a
  stop anywhere on the card.
- Deliberately deferred: the terminal's own `interrupted` vocabulary (the TUI
  reducer has no such status). When it gains one, the two surfaces should share
  the wording.
