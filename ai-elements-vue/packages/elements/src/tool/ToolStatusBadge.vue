<!-- A call's state, as the prototype draws it beside the header's title
     (app-preview.html :250-254): one coloured word, no capsule, no per-state
     icon — and a spinner for as long as the call runs. The word itself comes
     from the surface, because the word is the reader's language. -->
<script setup lang="ts">
import LoaderIcon from '../loader/LoaderIcon.vue'

const props = defineProps<{
  status: 'running' | 'awaiting' | 'done' | 'denied' | 'error'
  label: string
}>()
</script>

<template>
  <span class="tool-status" :class="`tool-status--${props.status}`">
    <LoaderIcon
      v-if="props.status === 'running'"
      :size="12"
      class="tool-status-spinner"
      aria-hidden="true"
    />
    <span>{{ props.label }}</span>
  </span>
</template>

<style scoped>
.tool-status {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11.5px;
  white-space: nowrap;
}

.tool-status--done {
  color: var(--forebrain-success);
}

.tool-status--error,
.tool-status--denied {
  color: var(--forebrain-danger);
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
