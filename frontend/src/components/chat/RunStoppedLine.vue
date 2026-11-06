<template>
  <div
    class="flex items-center gap-1.5 text-[11.5px] text-[var(--forebrain-muted-text)]"
    role="note"
    data-testid="run-stopped-line"
  >
    <span class="h-px w-3 flex-none bg-[var(--forebrain-divider-strong)]" />
    <Ban class="size-[13px] flex-none text-[var(--forebrain-muted-text)]" aria-hidden="true" />
    <span class="whitespace-nowrap">{{ label }}</span>
    <span v-if="reason" class="min-w-0 truncate text-[var(--forebrain-muted-text)]" :title="reason">{{ reason }}</span>
    <span class="h-px flex-1 bg-[var(--forebrain-divider-strong)]" />
  </div>
</template>

<script setup lang="ts">
/**
 * How an execution the user stopped ended, in the place it stopped. A stop is
 * not a failure: it is drawn with the muted ban the card row and the drawer's
 * own tab already use for the same agent, never the red account a failure gets
 * — and it is the one place that says so. Ruled like the worked line that
 * closes the run, because both mark where a run ended.
 *
 * The engine names which ending it was and, when an approval wait could not be
 * resumed, why. Both are the payload's, said here once: a pressed stop is
 * "Cancelled", an aborted wait is "Interrupted" with its sentence beside it.
 */
import { Ban } from 'lucide-vue-next'
import { computed } from 'vue'
import { subagentStatusLabel } from '@/lib/subagentStatus'

const props = withDefaults(defineProps<{ status?: 'cancelled' | 'interrupted'; reason?: string }>(), {
  status: 'cancelled',
  reason: '',
})
// The drawer's own vocabulary, so the word a stop is called is chosen once.
const label = computed(() => subagentStatusLabel(props.status))
</script>
