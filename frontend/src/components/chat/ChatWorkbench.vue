<template>
  <aside
    class="chat-workbench"
    :class="{ 'chat-workbench--overlay': overlay }"
    data-testid="chat-workbench"
  >
    <header class="chat-workbench-head">
      <span class="chat-workbench-title">{{ t('chat.workbench') }}</span>
      <button type="button" class="chat-workbench-close" :aria-label="t('chat.workbenchClose')" @click="emit('close')">
        <X class="size-4" aria-hidden="true" />
      </button>
    </header>
    <div class="chat-workbench-body">
      <AgentRosterPanel
        v-if="records.length > 0 || error"
        :title="t('agents.liveTitle')"
        :records="records"
        :loading="loading"
        :error="error"
        @view="(row) => emit('view', row)"
        @cancel="(row) => emit('cancel', row)"
        @cancel-all="() => emit('cancel-all')"
      />
      <WorkspaceTree @insert-ref="(path) => emit('insert-ref', path)" />
      <div class="chat-workbench-tip">{{ t('chat.insertRefTip') }} <span class="font-mono">@path</span></div>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { X } from 'lucide-vue-next'
import AgentRosterPanel from '@/components/AgentRosterPanel.vue'
import WorkspaceTree from '@/components/chat/WorkspaceTree.vue'
import type { AgentRosterRow } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The chat page's collapsible third column. It shows only what is alive
 * right now: the live-agent card appears when agents are running (or failed
 * to list), the workspace files when there are any.
 */
defineProps<{
  records: AgentRosterRow[]
  loading: boolean
  error: string
  overlay: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'view', row: AgentRosterRow): void
  (e: 'cancel', row: AgentRosterRow): void
  (e: 'cancel-all'): void
  (e: 'insert-ref', path: string): void
}>()

const { t } = useI18n()
</script>

<style scoped>
.chat-workbench {
  display: flex;
  min-height: 0;
  flex-direction: column;
  width: 300px;
  flex: none;
  border-left: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
}
.chat-workbench--overlay {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 44;
  border-left: 1px solid var(--forebrain-divider);
  box-shadow: var(--forebrain-shadow-pop);
}
.chat-workbench-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 12px;
}
.chat-workbench-title {
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
}
.chat-workbench-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: 7px;
  color: var(--forebrain-muted-text);
}
.chat-workbench-close:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.chat-workbench-body {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 0 12px 12px;
}
.chat-workbench-section {
  font-size: 12px;
  font-weight: 500;
  color: var(--forebrain-text-2);
}
.chat-workbench-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.chat-workbench-files {
  display: flex;
  flex-direction: column;
  gap: 2px;
  list-style: none;
  margin: 0;
  padding: 0;
}
.chat-workbench-file {
  width: 100%;
  text-align: left;
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 12px;
  color: var(--forebrain-text-2);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-workbench-file:hover {
  background: var(--forebrain-button-alt-bg);
}
.chat-workbench-dir {
  padding: 4px 8px;
  font-size: 11px;
  color: var(--forebrain-muted-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-workbench-tip {
  margin-top: 8px;
  font-size: 11px;
  color: var(--forebrain-muted-text);
}

@media (max-width: 1023px) {
  .chat-workbench {
    width: min(300px, calc(100vw - 64px));
  }
}
</style>
