<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="chat-drawer-backdrop"
      @click="emit('close')"
      @keydown.escape="emit('close')"
    />
    <aside
      v-if="open"
      ref="drawerRef"
      class="chat-drawer"
      data-testid="chat-drawer"
      role="dialog"
      :aria-label="t('chatDrawer.title', { agent: agentName })"
      :style="{ left: `${railWidth}px` }"
      tabindex="-1"
      @keydown.escape="emit('close')"
    >
      <header class="chat-drawer-head">
        <span class="chat-drawer-title">{{ t('chatDrawer.title', { agent: agentName }) }}</span>
        <button type="button" class="chat-drawer-close" :aria-label="t('common.close')" @click="emit('close')">
          <X class="size-4" aria-hidden="true" />
        </button>
      </header>

      <p v-if="createError" class="chat-drawer-error" role="alert">{{ createError }}</p>

      <button type="button" class="chat-drawer-new" data-testid="drawer-new-chat" :disabled="creating" @click="newChat">
        <Plus class="size-4" aria-hidden="true" />
        {{ creating ? t('common.loading') : t('chatDrawer.newChat') }}
      </button>

      <input
        v-model="query"
        class="forebrain-field chat-drawer-search"
        type="search"
        :placeholder="t('chatDrawer.searchPlaceholder')"
        :aria-label="t('chatDrawer.searchPlaceholder')"
      />

      <div class="chat-drawer-section">{{ t('chatDrawer.sessions') }}</div>
      <div class="chat-drawer-list">
        <p v-if="loading" class="chat-drawer-hint">{{ t('chatDrawer.loading') }}</p>
        <p v-else-if="error" class="chat-drawer-hint chat-drawer-error">{{ error }}</p>
        <p v-else-if="!filtered.length" class="chat-drawer-hint">{{ t('chatDrawer.empty') }}</p>
        <button
          v-for="row in filtered"
          v-else
          :key="row.id"
          type="button"
          class="chat-drawer-row"
          :class="{ 'chat-drawer-row--active': row.id === activeSessionId }"
          @click="emit('select', row.id)"
        >
          <span class="chat-drawer-row-title">{{ row.title }}</span>
          <Activity
            v-if="row.id === activeSessionId && heartbeatEnabled"
            class="size-[13px] chat-drawer-heart"
            data-testid="drawer-heartbeat-indicator"
            aria-hidden="true"
          />
        </button>
      </div>

      <footer class="chat-drawer-foot">{{ t('chatDrawer.projectNote') }}</footer>
    </aside>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Activity, Plus, X } from 'lucide-vue-next'
import { useChatSessions } from '@/composables/useChatSessions'
import { useSessionHeartbeat } from '@/composables/useSessionHeartbeat'
import { getErrorMessage } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The conversation drawer the rail's Chat item opens: new chat, search, and
 * the agent's session list — the things the deleted second column carried,
 * in the one place navigation belongs.
 */
const props = defineProps<{
  open: boolean
  railWidth: number
  agentName: string
  activeSessionId: string | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select', id: string): void
  (e: 'created', id: string): void
}>()

const { t } = useI18n()
const { sessions, loading, error, createSession } = useChatSessions()
const { current: heartbeat } = useSessionHeartbeat()

const query = ref('')
const creating = ref(false)
const createError = ref('')
const drawerRef = ref<HTMLElement | null>(null)

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return sessions.value
  return sessions.value.filter((row) => (row.title ?? '').toLowerCase().includes(q))
})

const heartbeatEnabled = computed(() => Boolean(heartbeat.value && !heartbeat.value.paused))

async function newChat() {
  if (creating.value) return
  creating.value = true
  createError.value = ''
  try {
    const id = await createSession()
    emit('created', id)
    emit('close')
  } catch (cause) {
    createError.value = getErrorMessage(cause)
  } finally {
    creating.value = false
  }
}

watch(() => props.open, async (open) => {
  if (open) {
    createError.value = ''
    await nextTick()
    drawerRef.value?.focus()
  }
})
</script>

<style scoped>
.chat-drawer-backdrop {
  position: fixed;
  inset: 0;
  z-index: 45;
  background: transparent;
}
.chat-drawer {
  position: fixed;
  z-index: 46;
  top: 0;
  bottom: 0;
  width: 300px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  background: var(--forebrain-surface);
  border-right: 1px solid var(--forebrain-divider);
  box-shadow: var(--forebrain-shadow-pop);
  outline: none;
}
.chat-drawer-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.chat-drawer-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-drawer-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: 7px;
  color: var(--forebrain-muted-text);
}
.chat-drawer-close:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.chat-drawer-new {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 36px;
  border-radius: 9px;
  border: 1px solid var(--forebrain-brand-1);
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
  font-size: 13px;
  font-weight: 500;
}
.chat-drawer-new:hover:not(:disabled) {
  background: var(--forebrain-brand-hover);
  border-color: var(--forebrain-brand-hover);
}
.chat-drawer-new:disabled {
  opacity: 0.6;
  cursor: default;
}
.chat-drawer-search {
  height: 34px;
}
.chat-drawer-section {
  font-size: 11px;
  color: var(--forebrain-muted-text);
  padding: 2px 2px 0;
}
.chat-drawer-list {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.chat-drawer-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  padding: 6px 2px;
}
.chat-drawer-error {
  color: var(--forebrain-danger);
}
.chat-drawer-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  text-align: left;
  border-radius: 8px;
  padding: 7px 9px;
  font-size: 13px;
  color: var(--forebrain-text-2);
}
.chat-drawer-row:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.chat-drawer-row--active {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  font-weight: 500;
}
.chat-drawer-row-title {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-drawer-heart {
  color: var(--forebrain-brand-1);
  flex: none;
}
.chat-drawer-foot {
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 8px;
  font-size: 11px;
  color: var(--forebrain-muted-text);
}

@media (max-width: 900px) {
  .chat-drawer {
    width: min(300px, calc(100vw - 64px));
  }
}
</style>
