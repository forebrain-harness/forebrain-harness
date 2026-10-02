<template>
  <button
    type="button"
    class="forebrain-subagent-card flex w-full items-start gap-2 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2 text-left"
    @click="$emit('open', card.agentId)"
  >
	<span class="mt-[3px] shrink-0" :class="card.phase === 'ended' && card.status !== 'failed' && card.status !== 'cancelled' ? 'text-[var(--forebrain-brand-1)]' : 'text-[var(--forebrain-muted-text)]'">
      <Bot class="size-3.5" />
    </span>
    <span class="min-w-0 flex-1">
      <span class="block truncate text-[13px] text-[var(--forebrain-text)]">{{ title }}</span>
      <span v-if="detail" class="mt-0.5 block truncate text-[11px] text-[var(--forebrain-muted-text)]">{{ detail }}</span>
      <span v-if="card.error" class="mt-0.5 block whitespace-pre-wrap text-[11px] text-[var(--forebrain-danger)]">{{ card.error }}</span>
    </span>
    <span class="shrink-0 self-center text-[11px] text-[var(--forebrain-muted-text)]">{{ t('chat.subagentOpen') }}</span>
  </button>
</template>

<script setup lang="ts">
/**
 * The lifecycle line the conversation shows for a subagent — dispatched, then
 * finished — mirroring what the terminal prints in the primary transcript, and
 * doubling as the way into that subagent's own view.
 */
import { computed } from 'vue'
import { Bot } from 'lucide-vue-next'
import { t } from '@/locales'
import type { SubagentLifecycleCard } from '@/composables/useChatStream'

const props = defineProps<{ card: SubagentLifecycleCard }>()
defineEmits<{ (e: 'open', agentId: string): void }>()

const title = computed(() => {
  const type = props.card.agentType || t('agents.subagent')
  return props.card.phase === 'spawned'
    ? t('chat.subagentSpawned', { type })
    : t('chat.subagentEnded', { type })
})

// Same fields the terminal puts on the card: the task id, and the status the
// run ended with. A dispatched task is named by its title — the prompt is the
// first message of the subagent's own view, not a line in the conversation.
const detail = computed(() => {
  const parts: string[] = []
  if (props.card.taskId) parts.push(`[${props.card.taskId}]`)
  const dispatched = props.card.title || props.card.task
  if (props.card.phase === 'spawned' && dispatched) parts.push(dispatched)
  if (props.card.phase === 'ended' && props.card.status) parts.push(`status=${props.card.status}`)
  return parts.join(' ')
})
</script>

<style scoped>
.forebrain-subagent-card {
  transition: border-color 160ms ease, background-color 160ms ease;
}

.forebrain-subagent-card:hover {
  border-color: var(--forebrain-brand-border, var(--forebrain-divider));
  background: var(--forebrain-surface);
}
</style>
