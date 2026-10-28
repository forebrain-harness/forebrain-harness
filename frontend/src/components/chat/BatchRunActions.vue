<template>
  <div class="task-run-actions" :data-status="status">
    <button
      v-for="action in actions"
      :key="action"
      type="button"
      class="task-run-btn"
      :class="toneOf(action)"
      :disabled="disabled"
      :data-testid="`batch-run-${action}`"
      @click="emit(action)"
    >
      <component :is="iconOf(action)" class="size-3.5" aria-hidden="true" />
      {{ labelOf(action) }}
    </button>
  </div>
</template>

<script setup lang="ts">
/**
 * The three acts a read-only batch conversation allows (plan 153 A11): stop the
 * run in flight, continue a stopped one, run a failed or stopped one again.
 * Nothing else — a batch execution takes no input, so there is nothing to type
 * and nothing to queue.
 *
 * Both places the prototype draws them (beside the status in the top bar and on
 * the read-only bar that takes the composer's place) use this one component, so
 * the two can never disagree about which acts a state allows.
 */
import { computed } from 'vue'
import { Play, RotateCw, Square } from 'lucide-vue-next'
import { batchRunActions, type BatchRunAction } from '@/lib/tasks'
import { t } from '@/locales'

const props = withDefaults(defineProps<{
  /** The execution's state; the acts follow it and nothing else. */
  status: string
  disabled?: boolean
}>(), { disabled: false })

const emit = defineEmits<{ (e: BatchRunAction): void }>()

const actions = computed(() => batchRunActions(props.status))

function iconOf(action: BatchRunAction) {
  if (action === 'stop') return Square
  if (action === 'continue') return Play
  return RotateCw
}

function labelOf(action: BatchRunAction): string {
  if (action === 'stop') return t('tasks.stopRun')
  if (action === 'continue') return t('tasks.continueRun')
  return t('tasks.rerunRun')
}

function toneOf(action: BatchRunAction): string {
  if (action === 'stop') return 'task-run-btn--danger'
  if (action === 'continue') return 'task-run-btn--primary'
  return ''
}
</script>

<style scoped>
.task-run-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}

/* The prototype's .btn (site's own button kit, :179) and its two tones. */
.task-run-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 26px;
  padding: 0 9px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  background: var(--forebrain-page-bg);
  color: var(--forebrain-text);
  font-size: 12px;
  font-weight: 500;
  white-space: nowrap;
  cursor: pointer;
}

.task-run-btn:hover {
  background: var(--forebrain-bg-alt);
}

.task-run-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

.task-run-btn--primary {
  border-color: var(--forebrain-brand-1);
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

.task-run-btn--primary:hover {
  background: var(--forebrain-brand-soft);
}

.task-run-btn--danger {
  color: var(--forebrain-danger);
  border-color: var(--forebrain-danger);
}
</style>
