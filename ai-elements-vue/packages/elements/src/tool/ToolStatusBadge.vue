<!-- A call's state, drawn as an icon (owner 2026-10-23: where an icon says it,
     no word): a spinner while it runs, a tick when it is done, a circle with a
     bar when it was refused, a slashed circle when it never ran, an alert when
     it failed. The word is still there for a reader that cannot see colour — as
     the icon's label and its tooltip — it just no longer takes room in the
     header. -->
<script setup lang="ts">
import { Check, CircleAlert, CircleSlash, Clock, Ban } from 'lucide-vue-next'
import LoaderIcon from '../loader/LoaderIcon.vue'

const props = defineProps<{
  status: 'running' | 'awaiting' | 'done' | 'denied' | 'canceled' | 'error'
  label: string
}>()
</script>

<template>
  <span
    class="tool-status"
    :class="`tool-status--${props.status}`"
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
    <CircleAlert v-else class="tool-status-icon" aria-hidden="true" />
    <span class="tool-status-word">{{ props.label }}</span>
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
</style>
