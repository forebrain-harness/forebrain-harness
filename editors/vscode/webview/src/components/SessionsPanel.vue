<template>
  <aside class="wv-sessions" data-testid="webview-sessions">
    <header class="wv-sessions-head">
      <span class="wv-sessions-title">{{ t('chatDrawer.sessions') }}</span>
      <button
        type="button"
        class="wv-icon-btn"
        :aria-label="t('chatDrawer.newChat')"
        :title="t('chatDrawer.newChat')"
        data-testid="sessions-new-chat"
        @click="emit('create')"
      >
        <Plus class="size-3.5" aria-hidden="true" />
      </button>
    </header>
    <label class="wv-sessions-search">
      <Search class="size-3.5" aria-hidden="true" />
      <input
        v-model="query"
        type="search"
        :placeholder="t('chatDrawer.searchPlaceholder')"
        :aria-label="t('chatDrawer.searchPlaceholder')"
        data-testid="sessions-search"
      >
    </label>
    <!-- A refused load is the list's own refusal, said in the list: what came
         back stays empty and the way back in is one button. -->
    <div v-if="sessionsError" class="forebrain-err-block" role="alert" data-testid="sessions-error">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>
        {{ t('chatDrawer.loadFailed') }}
        <span class="forebrain-err-s">{{ sessionsError }}</span>
        <button type="button" class="wv-retry" data-testid="sessions-retry" @click="reload">
          {{ t('chat.retry') }}
        </button>
      </div>
    </div>
    <div v-else-if="loading" class="wv-thread-pulse" data-testid="sessions-loading" aria-label="…" />
    <div v-else-if="sessions.length === 0" class="wv-sessions-empty" data-testid="sessions-empty">
      {{ query.trim() ? t('chatDrawer.noMatch') : t('chatDrawer.empty') }}
    </div>
    <div v-else class="wv-sessions-list" data-testid="sessions-list" role="listbox" :aria-label="t('chatDrawer.sessions')">
      <button
        v-for="row in sessions"
        :key="row.id"
        type="button"
        class="wv-sess-row"
        :class="{ 'wv-sess-row--on': row.id === activeSessionId }"
        role="option"
        :aria-selected="row.id === activeSessionId"
        :data-session-id="row.id"
        data-testid="sessions-row"
        @click="emit('open', row.id)"
      >
        <span class="wv-sess-title">{{ row.title }}</span>
        <span class="wv-sess-meta">
          <span v-if="row.projectName" class="wv-sess-project">{{ row.projectName }}</span>
          <span class="wv-sess-time">{{ formatClock(Date.parse(row.updateTime) / 1000) }}</span>
        </span>
      </button>
      <button
        v-if="hasMore"
        type="button"
        class="wv-sessions-more"
        data-testid="sessions-load-more"
        :disabled="loadingMore"
        @click="loadMore()"
      >
        {{ t('chatDrawer.loadMore') }}
      </button>
    </div>
    <footer class="wv-sessions-foot">{{ t('chatDrawer.foot') }}</footer>
  </aside>
</template>

<script setup lang="ts">
// The Sessions view's list: the shared session store (one fact, the same the
// rail's drawer reads) with the panel's own chrome around it — search, new,
// load more, and the row marking the conversation the chat view has open.
// Delete is not a row action anywhere: the gateway has no DELETE for sessions.
import { onMounted, ref, watch } from 'vue'
import { CircleAlert, Plus, Search } from 'lucide-vue-next'
import { useChatSessions } from '@/composables/useChatSessions'
import { formatClock } from '@/lib/time'
import { t } from '@/locales'

defineProps<{
  /** The conversation the chat view has open, as the host last reported it. */
  activeSessionId?: string
}>()

const emit = defineEmits<{
  (e: 'open', sessionId: string): void
  (e: 'create'): void
}>()

const { sessions, loading, loadingMore, error: sessionsError, hasMore, search, fetchSessions, loadMore } = useChatSessions()

const query = ref(search.value)
// The drawer's own pacing: a small debounce so a typed word is one request,
// not one per key.
let searchTimer: ReturnType<typeof setTimeout> | null = null
watch(query, (value) => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => void fetchSessions(value), 300)
})

onMounted(() => {
  void fetchSessions()
})

function reload() {
  void fetchSessions(query.value)
}
</script>

<style scoped>
.wv-sessions {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: var(--forebrain-bg);
  color: var(--forebrain-text);
  font-family: var(--forebrain-ui-font);
  font-size: 13px;
}
.wv-sessions-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 12px 6px;
}
.wv-sessions-title {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--forebrain-muted-text);
}
.wv-sessions-search {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 10px 8px;
  padding: 5px 8px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 0.5rem;
  background: var(--forebrain-surface);
  color: var(--forebrain-muted-text);
}
.wv-sessions-search input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: none;
  background: none;
  color: var(--forebrain-text);
  font: inherit;
}
.wv-sessions-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 0 8px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.wv-sess-row {
  display: flex;
  flex-direction: column;
  gap: 2px;
  text-align: left;
  border: 1px solid transparent;
  border-radius: 0.5rem;
  background: none;
  padding: 7px 10px;
  cursor: pointer;
  color: var(--forebrain-text);
  font: inherit;
}
.wv-sess-row:hover {
  background: var(--forebrain-bg-alt);
}
.wv-sess-row--on {
  background: var(--forebrain-brand-soft);
  border-color: var(--forebrain-brand-border);
}
.wv-sess-row--on .wv-sess-title {
  color: var(--path-ink);
  font-weight: 600;
}
.wv-sess-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wv-sess-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.wv-sess-project {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wv-sess-time {
  margin-left: auto;
  flex: none;
}
.wv-sessions-empty {
  padding: 24px 16px;
  color: var(--forebrain-muted-text);
  text-align: center;
}
.wv-sessions-more {
  margin: 4px 10px 8px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 0.5rem;
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  font-size: 12px;
  padding: 6px;
  cursor: pointer;
}
.wv-sessions-more:disabled {
  opacity: 0.6;
  cursor: default;
}
.wv-sessions-foot {
  padding: 8px 12px 10px;
  border-top: 1px solid var(--forebrain-divider);
  color: var(--forebrain-muted-text);
  font-size: 11px;
  line-height: 1.5;
}
.wv-icon-btn {
  display: inline-grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  cursor: pointer;
}
.wv-retry {
  border: 0;
  background: none;
  color: var(--forebrain-brand-1);
  cursor: pointer;
  font: inherit;
  padding: 0;
  text-decoration: underline;
}
.wv-thread-pulse {
  width: 22px;
  height: 6px;
  margin: 12px;
  border-radius: 3px;
  background: var(--forebrain-divider);
  animation: wv-pulse 1.2s ease-in-out infinite;
}
@keyframes wv-pulse {
  50% { opacity: 0.35; }
}
@media (prefers-reduced-motion: reduce) {
  .wv-thread-pulse { animation: none; }
}
</style>
