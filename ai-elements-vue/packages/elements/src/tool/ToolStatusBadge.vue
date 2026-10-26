<!-- A call's state, drawn as an icon (owner 2026-10-23: where an icon says it,
     no word): a spinner while it runs, a clock while it waits for the reader,
     a tick when it is done, a circle with a bar when it was refused, a slashed
     circle when it never ran, a cross when it failed (owner 2026-10-25: the
     cross pairs with the tick; the alert circle belongs to page-level error
     bars alone). The word is still there for a reader that cannot see colour —
     as the icon's label and its tooltip — it just no longer takes room in the
     header.
     On the activity line (variant "node", owner 2026-10-25) the same state is
     the card's node: a 20px circle on the line, tinted by the outcome. -->
<script setup lang="ts">
import { Ban, Check, CircleSlash, Clock, X } from 'lucide-vue-next'
import LoaderIcon from '../loader/LoaderIcon.vue'

const props = withDefaults(defineProps<{
  status: 'running' | 'awaiting' | 'done' | 'denied' | 'canceled' | 'error'
  label: string
  /**
   * Where the state is drawn: inline in a card head (default), or as the
   * activity line's node — a 20px circle in the outcome's colour.
   */
  variant?: 'inline' | 'node'
}>(), {
  variant: 'inline',
})
</script>

<template>
  <span
    class="tool-status"
    :class="[`tool-status--${props.status}`, { 'tool-status--node': props.variant === 'node' }]"
    role="img"
    :aria-label="props.label"
    :title="props.label"
  >
    <LoaderIcon
      v-if="props.status === 'running'"
      :size="14"
      class="tool-status-spinner"
      aria-hidden="true"
    />
    <Clock v-else-if="props.status === 'awaiting'" class="tool-status-icon" aria-hidden="true" />
    <Check v-else-if="props.status === 'done'" class="tool-status-icon" aria-hidden="true" />
    <Ban v-else-if="props.status === 'denied'" class="tool-status-icon" aria-hidden="true" />
    <CircleSlash v-else-if="props.status === 'canceled'" class="tool-status-icon" aria-hidden="true" />
    <X v-else class="tool-status-icon" aria-hidden="true" />
    <span class="tool-status-word"
      >{{ props.label }}</span>
  </span>
</template>

<style scoped>
.tool-status {
  position: relative;
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
}

.tool-status-icon {
  width: 14px;
  height: 14px;
}

/* The word, for assistive tech only. */
.tool-status-word {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}

.tool-status--done {
  color: var(--forebrain-success);
}

.tool-status--error,
.tool-status--denied {
  color: var(--forebrain-danger);
}

/* A call that never ran is not a refusal and not a failure: it is simply not
   there, so its word is the quiet one. */
.tool-status--canceled {
  color: var(--forebrain-muted-text);
}

.tool-status--running,
.tool-status--awaiting {
  color: var(--forebrain-brand-1);
}

.tool-status-spinner {
  flex: none;
  animation: tool-status-spin 1s linear infinite;
}

@keyframes tool-status-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .tool-status-spinner {
    animation: none;
  }
}

/* The activity line's node (prototype .al-run > .tool > .tool-h >
   :is(.ok,.bad,.run,.ask,.quiet), :426-433): a 20px circle the line runs
   through, tinted by the outcome, its glyph a stroke heavier than the head's. */
.tool-status--node {
  width: 20px;
  height: 20px;
  box-sizing: border-box;
  border-radius: 50%;
}

.tool-status--node .tool-status-icon {
  width: 12px;
  height: 12px;
  stroke-width: 2.6;
}

.tool-status--node .tool-status-spinner {
  width: 12px;
  height: 12px;
}

.tool-status--node.tool-status--done {
  background: #ecfdf3;
  box-shadow: inset 0 0 0 1.5px #abefc6;
  color: #067647;
}

.tool-status--node.tool-status--error {
  background: #fef3f2;
  box-shadow: inset 0 0 0 1.5px #fecdca;
  color: #b42318;
}

/* A refusal is not a failure on the line either: the quiet grey, not red —
   the prototype's --soft fill with its --line-2 ring (:432-433). */
.tool-status--node.tool-status--denied {
  background: #f2f4f7;
  box-shadow: inset 0 0 0 1.5px var(--forebrain-divider-strong);
  color: var(--forebrain-muted-text);
}

.tool-status--node.tool-status--canceled {
  background: #f2f4f7;
  color: var(--forebrain-muted-text);
}

.tool-status--node.tool-status--running,
.tool-status--node.tool-status--awaiting {
  background: var(--forebrain-brand-soft);
  box-shadow: inset 0 0 0 1.5px var(--forebrain-brand-border);
  color: var(--forebrain-brand-1);
}
</style>
