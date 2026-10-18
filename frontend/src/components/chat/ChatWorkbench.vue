<template>
  <aside
    class="chat-workbench"
    :class="{ 'chat-workbench--overlay': overlay, 'chat-workbench--wide': wide }"
    data-testid="chat-workbench"
  >
    <header class="chat-workbench-head">
      <span class="chat-workbench-title">{{ t('chat.workbench') }}</span>
      <button type="button" class="chat-workbench-close" :aria-label="t('chat.workbenchClose')" @click="emit('close')">
        <X class="size-4" aria-hidden="true" />
      </button>
    </header>

    <div class="bench-tabs" role="tablist" :aria-label="t('chat.workbench')">
      <button
        v-for="tab in allTabs"
        :key="tab.key"
        type="button"
        role="tab"
        class="bench-tab-btn"
        :aria-selected="active === tab.key"
        :disabled="tab.disabled"
        v-tip:top="tab.disabled ? t('workshop.tabNeedsSkill') : ''"
        :data-testid="`bench-tab-${tab.key}`"
        @click="select(tab.key)"
      >
        {{ t(tab.label) }}
      </button>
    </div>

    <div class="chat-workbench-body">
      <OverviewTab
        v-if="active === 'overview'"
        :scope="scope"
        :records="records"
        :loading="loading"
        :error="error"
        @view="(row) => emit('view', row)"
        @cancel="(row) => emit('cancel', row)"
        @cancel-all="() => emit('cancel-all')"
      />
      <ContextTab v-else-if="active === 'context'" :scope="scope" />
      <WorkingDirTab v-else-if="active === 'tree'" :scope="scope" :root-path="rootPath" @insert-ref="emit('insert-ref', $event)" />
      <!-- The changes tab is overridable: the chat page shows the files the
           conversation changed, the workshop shows the draft against the
           current version. -->
      <slot v-else-if="active === 'changes'" name="changes">
        <ChangedFilesTab :scope="scope" />
      </slot>
      <slot v-else :name="active" />
    </div>
  </aside>
</template>

<script setup lang="ts">
/**
 * The chat page's collapsible third column, now four base tabs — an overview
 * of what is alive and what it has spent, the context report, the working
 * directory's tree with per-node actions, and the files the conversation
 * changed — plus any extra tabs a page adds (the workshop's evaluation and
 * version tabs). The workbench opens collapsed and only what it shows is
 * loaded; the tab last used for a given conversation is remembered in the
 * browser.
 */
import { computed, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'

import ContextTab from '@/components/workbench/ContextTab.vue'
import ChangedFilesTab from '@/components/workbench/ChangedFilesTab.vue'
import OverviewTab from '@/components/workbench/OverviewTab.vue'
import WorkingDirTab from '@/components/workbench/WorkingDirTab.vue'
import type { AgentRosterRow, WorkspaceScope } from '@/lib/api'
import { useI18n, type I18nKey } from '@/locales'

interface WorkbenchExtraTab {
  key: string
  label: I18nKey
  disabled?: boolean
}

const props = withDefaults(defineProps<{
  records: AgentRosterRow[]
  loading: boolean
  error: string
  overlay: boolean
  scope?: WorkspaceScope
  rootPath?: string
  wide?: boolean
  /** Extra page-specific tabs, rendered after the base four. */
  extraTabs?: WorkbenchExtraTab[]
}>(), { scope: () => ({}), rootPath: '', wide: false, extraTabs: () => [] })

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'view', row: AgentRosterRow): void
  (e: 'cancel', row: AgentRosterRow): void
  (e: 'cancel-all'): void
  (e: 'insert-ref', path: string): void
}>()

const { t } = useI18n()

const baseTabs = [
  { key: 'overview', label: 'chat.benchTabOverview' },
  { key: 'context', label: 'chat.benchTabContext' },
  { key: 'tree', label: 'chat.benchTabTree' },
  { key: 'changes', label: 'chat.benchTabChanges' },
] as const

const allTabs = computed<WorkbenchExtraTab[]>(() => [
  ...baseTabs.map((tab) => ({ key: tab.key as string, label: tab.label as I18nKey })),
  ...props.extraTabs,
])

const scopeKey = computed(() => {
  const sessionId = String(props.scope.sessionId ?? '').trim()
  if (sessionId) return `s:${sessionId}`
  const projectId = String(props.scope.projectId ?? '').trim()
  return projectId ? `p:${projectId}` : ''
})

const storageKey = computed(() => (scopeKey.value ? `forebrain.workbench.tab.${scopeKey.value}` : ''))
const active = ref<string>('overview')

function tabEnabled(key: string): boolean {
  const tab = allTabs.value.find((item) => item.key === key)
  return !!tab && !tab.disabled
}

function readStored(): string {
  if (typeof localStorage === 'undefined' || !storageKey.value) return 'overview'
  const stored = localStorage.getItem(storageKey.value)
  return stored && tabEnabled(stored) ? stored : 'overview'
}

function select(tab: string) {
  if (!tabEnabled(tab)) return
  active.value = tab
}

watch(scopeKey, () => {
  active.value = readStored()
}, { immediate: true })

// An extra tab may become disabled (no skill yet) while it is the active one;
// fall back to the overview rather than drawing a slot with nothing in it.
watch(allTabs, () => {
  if (!tabEnabled(active.value)) active.value = 'overview'
})

watch(active, (tab) => {
  if (typeof localStorage === 'undefined' || !storageKey.value) return
  localStorage.setItem(storageKey.value, tab)
})
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
.chat-workbench--wide {
  width: 420px;
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
  padding: 10px 12px 10px 16px;
}
.chat-workbench-title {
  font-size: 13px;
  font-weight: 600;
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
.bench-tabs {
  display: flex;
  gap: 2px;
  padding: 0 10px;
  border-bottom: 1px solid var(--forebrain-divider);
  flex: none;
  overflow-x: auto;
}
.bench-tab-btn {
  border: 0;
  background: transparent;
  padding: 8px 7px;
  font-size: 12.5px;
  color: var(--forebrain-text-2);
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  white-space: nowrap;
}
.bench-tab-btn:disabled {
  color: var(--forebrain-muted-text);
  opacity: 0.5;
  cursor: not-allowed;
}
.bench-tab-btn[aria-selected='true'] {
  color: var(--forebrain-brand-1);
  border-bottom-color: var(--forebrain-brand-1);
  font-weight: 600;
}
.chat-workbench-body {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 12px;
}

@media (max-width: 1023px) {
  .chat-workbench {
    width: min(300px, calc(100vw - 64px));
  }
  .chat-workbench--wide {
    width: min(420px, calc(100vw - 64px));
  }
}
</style>