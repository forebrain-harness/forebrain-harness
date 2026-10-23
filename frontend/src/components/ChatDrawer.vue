<template>
  <Teleport to="body">
    <!-- The drawer is a panel beside the rail, not a modal (prototype .drawer,
         :660): no backdrop covers the menu bar, so the next page is one click
         away and the menu bar keeps working while the list is open. -->
    <aside
      v-if="open"
      ref="drawerRef"
      class="chat-drawer"
      data-testid="chat-drawer"
      role="complementary"
      :aria-label="t('chatDrawer.title')"
      :style="{ left: `${railWidth}px` }"
      tabindex="-1"
      @keydown.escape="emit('close')"
    >
      <header class="chat-drawer-head">
        <span class="chat-drawer-title">{{ t('chatDrawer.title') }}</span>
        <button type="button" class="chat-drawer-close" :aria-label="t('chatDrawer.collapse')" v-tip:bottom="t('chatDrawer.collapse')" @click="emit('close')">
          <X class="size-4" aria-hidden="true" />
        </button>
      </header>

      <button type="button" class="chat-drawer-new" data-testid="drawer-new-chat" @click="newChat">
        <Plus class="size-4" aria-hidden="true" />
        {{ t('chatDrawer.newChat') }}
      </button>

      <!-- The search is a field with its mark (prototype .d-search, :668). The
           row draws the box, so the field inside it keeps only its text. -->
      <label class="chat-drawer-search">
        <Search class="chat-drawer-search-icon" aria-hidden="true" />
        <Input
          v-model="query"
          class="chat-drawer-search-input h-auto w-auto px-0 py-0 shadow-none"
          type="search"
          :placeholder="t('chatDrawer.searchPlaceholder')"
          :aria-label="t('chatDrawer.searchPlaceholder')"
        />
      </label>

      <!-- The project filter (prototype .d-filter, :737): the list is the
           server's, so the choice goes to it and the count and pages follow. -->
      <Select :model-value="toChoice(projectFilter)" @update:model-value="(value) => chooseProject(fromChoice(value))">
        <SelectTrigger
          class="forebrain-field chat-drawer-filter"
          :aria-label="t('chatDrawer.filterByProject')"
          data-testid="drawer-project-filter"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent class="chat-drawer-filter-menu">
          <SelectItem :value="EMPTY_CHOICE">{{ t('chatDrawer.allProjects') }}</SelectItem>
          <SelectItem v-for="project in projectOptions" :key="project.id" :value="project.id">{{ project.name }}</SelectItem>
        </SelectContent>
      </Select>

      <div class="chat-drawer-section">{{ t('chatDrawer.sessions') }}</div>
      <div class="chat-drawer-list">
        <p v-if="loading" class="chat-drawer-hint">{{ t('chatDrawer.loading') }}</p>
        <p v-else-if="error" class="chat-drawer-hint chat-drawer-error">{{ error }}</p>
        <p v-else-if="!sessions.length && (query.trim() || projectFilter)" class="chat-drawer-hint">{{ t('chatDrawer.noMatch') }}</p>
        <p v-else-if="!sessions.length" class="chat-drawer-hint">{{ t('chatDrawer.empty') }}</p>
        <template v-else>
          <button
            v-for="row in sessions"
            :key="row.id"
            type="button"
            class="chat-drawer-row"
            :class="{ 'chat-drawer-row--active': row.id === activeSessionId }"
            @click="emit('select', row.id)"
          >
            <span class="chat-drawer-row-main">
              <span class="chat-drawer-row-title">{{ row.title }}</span>
              <span v-if="row.projectName" class="chat-drawer-row-project">{{ row.projectName }}</span>
            </span>
            <span
              v-if="beatOf(row.id)"
              class="chat-drawer-heart"
              :class="{ 'chat-drawer-heart--paused': beatOf(row.id)?.paused }"
              v-tip:top="heartbeatLabel(row.id)"
              :aria-label="heartbeatLabel(row.id)"
              role="img"
              data-testid="drawer-heartbeat-indicator"
            >
              <Activity class="size-[13px]" aria-hidden="true" />
            </span>
          </button>
          <button
            v-if="hasMore"
            type="button"
            class="chat-drawer-more"
            data-testid="drawer-load-more"
            :disabled="loadingMore"
            @click="loadMore()"
          >
            {{ t('chatDrawer.loadMore') }}
          </button>
        </template>
      </div>
      <!-- The one thing the list cannot say for itself (prototype .d-foot,
           :4579): where these conversations come from. -->
      <div class="chat-drawer-foot" data-testid="chat-drawer-foot">{{ t('chatDrawer.foot') }}</div>
    </aside>
  </Teleport>
</template>

<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { Activity, Plus, Search, X } from 'lucide-vue-next'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import { useChatSessions } from '@/composables/useChatSessions'
import { useHeartbeats } from '@/composables/useHeartbeats'
import { forebrainApi } from '@/lib/api'
import { EMPTY_CHOICE, fromChoice, toChoice } from '@/lib/selectValue'
import { useI18n } from '@/locales'

/**
 * The conversation drawer the rail's Chat item opens: new chat, search, and
 * the agent's session list — the things the deleted second column carried,
 * in the one place navigation belongs.
 */
const props = defineProps<{
  open: boolean
  railWidth: number
  activeSessionId: string | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select', id: string): void
  /** A new conversation was asked for: the host opens the new-chat page. */
  (e: 'new'): void
}>()

const { t } = useI18n()
const { sessions, loading, loadingMore, error, hasMore, projectFilter, fetchSessions, loadMore } = useChatSessions()
const { forSession: beatOf, refresh: refreshHeartbeats } = useHeartbeats()

const query = ref('')
const drawerRef = ref<HTMLElement | null>(null)
let searchTimer: ReturnType<typeof setTimeout> | null = null

// The search is the server's — the drawer holds one loaded page of the
// list, and a local filter could only search what it happens to show. So
// typing asks the server for the first page of matches once the person
// pauses, the same debounce every paged list's search box carries.
watch(query, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    searchTimer = null
    void fetchSessions(query.value.trim())
  }, 300)
})

// The projects the filter offers: what the new-chat page's pill lists, newest
// first. A project the person filtered by that has since gone from the list
// is cleared, so the filter never names something nothing can match.
const projectOptions = ref<{ id: string; name: string }[]>([])

async function loadProjects() {
  try {
    const rows = await forebrainApi.projectsList({ sort: 'updated', limit: 200 })
    projectOptions.value = rows.map((row) => ({ id: row.id, name: row.name }))
    if (projectFilter.value && !projectOptions.value.some((row) => row.id === projectFilter.value)) {
      void fetchSessions(undefined, '')
    }
  } catch {
    // Without the list the filter keeps "all projects"; the sessions still list.
    projectOptions.value = []
  }
}

function chooseProject(id: string) {
  if (id === projectFilter.value) return
  void fetchSessions(undefined, id)
}

/** Every conversation a heartbeat asks again is marked, the open one or not. */
function heartbeatLabel(sessionId: string): string {
  const beat = beatOf(sessionId)
  if (!beat) return ''
  return beat.paused
    ? t('heartbeat.indicatorPaused')
    : t('heartbeat.indicatorRunning', { minutes: Math.max(1, Math.round(beat.intervalSeconds / 60)) })
}

function newChat() {
  // A new chat opens the new-conversation page, where the project is chosen
  // and the conversation is created on its first message (plan 029). The
  // drawer no longer creates a project-less conversation.
  emit('new')
  emit('close')
}

watch(() => props.open, async (open) => {
  if (open) {
    // The drawer opens from any page — the list is the drawer's to load, not
    // something the chat page may or may not have fetched earlier (a fresh
    // load elsewhere, a tenant switch, a turn in another tab).
    void fetchSessions()
    void loadProjects()
    // The marks are the drawer's to read too; a failed read leaves them off.
    refreshHeartbeats().catch(() => {})
    await nextTick()
    drawerRef.value?.focus()
  }
})
</script>

<style scoped>
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
  padding: 12px 12px 8px 16px;
  margin: -12px -12px 0;
}
.chat-drawer-title {
  font-size: 14px;
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
/* The prototype's .d-search (:668): one hairline around the field and its
   mark, 8px corner, 6px by 9px of padding, 12.5px of text. */
.chat-drawer-search {
  display: flex;
  align-items: center;
  gap: 7px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 8px;
  padding: 6px 9px;
  color: var(--forebrain-muted-text);
  background: var(--forebrain-surface);
}

.chat-drawer-search-icon {
  width: 13px;
  height: 13px;
  flex: none;
}

.chat-drawer-search-input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  font-size: 12.5px;
  color: var(--forebrain-text);
}

/* The prototype's .d-filter select.field (:737): 30px tall, 12px text, the
   width of the drawer's column. */
.chat-drawer-filter {
  height: 30px;
  width: 100%;
  font-size: 12px;
}

.chat-drawer-foot {
  flex: none;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 8px;
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
.chat-drawer-row-main {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.chat-drawer-row-title {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-drawer-row-project {
  font-size: 11px;
  color: var(--forebrain-muted-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-drawer-heart {
  display: inline-flex;
  color: var(--forebrain-brand-1);
  flex: none;
}
.chat-drawer-heart--paused {
  color: var(--forebrain-muted-text);
}
.chat-drawer-more {
  border-radius: 8px;
  padding: 7px 9px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.chat-drawer-more:hover:not(:disabled) {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.chat-drawer-more:disabled {
  opacity: 0.6;
  cursor: default;
}

@media (max-width: 900px) {
  .chat-drawer {
    width: min(300px, calc(100vw - 64px));
  }
}
</style>
