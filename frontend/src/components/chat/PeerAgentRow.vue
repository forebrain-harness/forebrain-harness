<template>
  <!-- One agent another process hosts (plan 200 §3): its identity block, its
       name, what kind of session it is, whether it is busy, and what it says it
       is working on. A subagent is outlined in its parent's tone and named with
       its parent, so the drawer reads as one machine full of agents rather than
       a flat list. -->
  <div class="peer-agent" :data-testid="'peer-agent-' + agent.name">
    <PeerMark
      :mark="agent.mark"
      :tone="agent.tone"
      :name="agent.name"
      size="lg"
      :kind="isSubagent ? 'sub' : 'agent'"
    />
    <div class="peer-agent-body">
      <div class="l1">
        <span class="nm">{{ agent.name }}</span>
        <span class="kd">{{ kindLabel }}</span>
        <span class="st"><i class="dot" :class="dotClass" aria-hidden="true" />{{ statusLabel }}</span>
      </div>
      <div v-if="agent.task" class="l2">{{ agent.task }}</div>
      <div v-if="agent.cwd" class="l3">{{ agent.cwd }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One row of the peer drawer's agent lists (plan 200 §3). The block, the name
 * and the kind come from the gateway; the only thing derived here is whether
 * the agent is a subagent, which the name's own shape says — a subagent is
 * named after the primary agent that spawned it.
 */
import { computed } from 'vue'
import PeerMark from './PeerMark.vue'
import { peerKindLabel, peerStatusLabel } from '@/lib/peerMark'
import type { PeerAgentRow } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{ agent: PeerAgentRow }>()

const { t } = useI18n()

const isSubagent = computed(() => props.agent.name.includes('.'))

const kindLabel = computed(() => peerKindLabel(props.agent.kind, t))

const statusLabel = computed(() => peerStatusLabel(props.agent.status, t))

/** The dot says the same thing as the words, for a reader scanning the list. */
const dotClass = computed(() => {
  switch (String(props.agent.status ?? '').trim().toLowerCase()) {
    case 'busy':
      return 'dot--busy'
    case 'waiting':
      return 'dot--wait'
    default:
      return ''
  }
})
</script>

<style scoped>
.peer-agent {
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr);
  gap: 2px 10px;
  padding: 9px 0;
}

.peer-agent + .peer-agent {
  border-top: 1px dashed var(--forebrain-divider);
}

.peer-agent > .peer-mark {
  grid-row: span 3;
}

.peer-agent-body {
  min-width: 0;
}

.peer-agent .l1 {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.peer-agent .nm {
  font-weight: 600;
  font-size: 13.5px;
  color: var(--forebrain-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.peer-agent .kd {
  color: var(--forebrain-text-2);
  font-size: 12px;
  white-space: nowrap;
}

.peer-agent .st {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-text-2);
  white-space: nowrap;
}

.peer-agent .dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--forebrain-divider-strong);
}

.peer-agent .dot--busy {
  background: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
}

.peer-agent .dot--wait {
  background: #b54708;
  box-shadow: 0 0 0 3px #fffaeb;
}

.peer-agent .l2 {
  font-size: 13px;
  color: var(--forebrain-text-2);
  min-width: 0;
  overflow-wrap: anywhere;
}

.peer-agent .l3 {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  font-family: var(--forebrain-mono-font);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
