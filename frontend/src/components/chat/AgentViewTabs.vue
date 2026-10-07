<template>
  <nav
    v-if="records.length"
    class="forebrain-agent-tabs flex items-center gap-1.5 overflow-x-auto px-1 py-1"
    :aria-label="t('chat.agentViewsAria')"
  >
    <button
      type="button"
      class="forebrain-agent-tab"
      :class="active === '' ? 'is-active' : ''"
      :aria-current="active === '' ? 'page' : undefined"
      @click="$emit('select', '')"
    >
      <MessageSquare class="size-3.5 shrink-0" />
      <span class="truncate">{{ t('chat.agentViewMain') }}</span>
    </button>
    <button
      v-for="entry in records"
      :key="entry.agentId"
      type="button"
      class="forebrain-agent-tab"
      :class="active === entry.agentId ? 'is-active' : ''"
      :aria-current="active === entry.agentId ? 'page' : undefined"
      :title="entry.title || entry.agentId"
      @click="$emit('select', entry.agentId)"
    >
      <span class="forebrain-agent-dot" :class="dotClass(entry)" />
      <span class="truncate">{{ tabLabel(entry) }}</span>
      <span
        v-if="unseen(entry)"
        class="forebrain-agent-badge"
        :aria-label="t('chat.agentViewUnseen')"
      />
    </button>
  </nav>
</template>

<script setup lang="ts">
/**
 * The strip that switches the conversation between the primary agent and any
 * subagent it dispatched — the web counterpart of the terminal's per-agent
 * screens. A subagent that produced something while the user was reading
 * another view carries a dot, so switching is a decision the user can make from
 * here rather than by guessing.
 */
import { MessageSquare } from 'lucide-vue-next'
import { t } from '@/locales'
import type { SubagentTranscript } from '@/composables/useChatStream'

const props = defineProps<{
  records: SubagentTranscript[]
  active: string
  /** Last updatedSeq the user has seen per agent, keyed by agent id. */
  seen: Record<string, number>
}>()

defineEmits<{ (e: 'select', agentId: string): void }>()

// A tab is named the way the terminal's roster row is named: what the agent
// is, then the task it was dispatched to do — the one name the engine derived,
// never the prompt. Without a title the type alone stands (the engine names
// every spawn, so this is the fallback for records older than that rule).
function tabLabel(entry: SubagentTranscript): string {
  const type = entry.agentType || t('agents.subagent')
  const title = (entry.title ?? '').trim()
  return title ? `${type} · ${title}` : type
}

function unseen(entry: SubagentTranscript): boolean {
  if (props.active === entry.agentId) return false
  return entry.updatedSeq > (props.seen[entry.agentId] ?? 0)
}

function dotClass(entry: SubagentTranscript): string {
  switch (entry.status) {
    case 'done':
      return 'is-done'
    case 'failed':
      return 'is-failed'
    default:
      return 'is-running'
  }
}
</script>

<style scoped>
.forebrain-agent-tabs {
  scrollbar-width: none;
}
.forebrain-agent-tabs::-webkit-scrollbar {
  display: none;
}

.forebrain-agent-tab {
  position: relative;
  display: inline-flex;
  max-width: 15rem;
  flex: 0 0 auto;
  align-items: center;
  gap: 0.375rem;
  border-radius: 9999px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  padding: 0.25rem 0.75rem;
  font-size: 12px;
  line-height: 1.25rem;
  color: var(--forebrain-muted-text);
  transition: color 160ms ease, background-color 160ms ease, border-color 160ms ease;
}

.forebrain-agent-tab:hover {
  color: var(--forebrain-text);
  border-color: var(--forebrain-brand-border, var(--forebrain-divider));
}

.forebrain-agent-tab.is-active {
  color: var(--forebrain-text);
  border-color: var(--forebrain-brand-border, var(--forebrain-divider));
  background: var(--forebrain-surface);
  font-weight: 500;
}

.forebrain-agent-dot {
  height: 0.4rem;
  width: 0.4rem;
  flex: none;
  border-radius: 9999px;
  background: var(--forebrain-muted-text);
}

.forebrain-agent-dot.is-running {
  background: var(--forebrain-brand-1);
  animation: forebrain-agent-pulse 1.6s ease-in-out infinite;
}

.forebrain-agent-dot.is-done {
  background: var(--forebrain-brand-1);
}

.forebrain-agent-dot.is-failed {
  background: var(--forebrain-danger);
}

.forebrain-agent-badge {
  height: 0.375rem;
  width: 0.375rem;
  flex: none;
  border-radius: 9999px;
  background: var(--forebrain-brand-1);
}

@keyframes forebrain-agent-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}

@media (prefers-reduced-motion: reduce) {
  .forebrain-agent-dot.is-running {
    animation: none;
  }
}
</style>
