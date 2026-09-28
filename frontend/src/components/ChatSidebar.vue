<template>
  <aside
    class="hidden w-[260px] shrink-0 flex-col border-r border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-glass)] shadow-[var(--forebrain-sidebar-shadow)] backdrop-blur-md md:flex lg:w-[272px]"
  >
    <div class="p-3">
      <button
        type="button"
        @click="$emit('new-chat')"
        class="flex w-full items-center justify-center gap-2 rounded-xl border border-[var(--forebrain-brand-1)] bg-[var(--forebrain-brand-1)] px-3 py-2.5 text-[15px] font-medium text-[var(--forebrain-on-brand)] shadow-[var(--forebrain-brand-button-shadow)] transition-all duration-150 hover:border-[var(--forebrain-brand-hover)] hover:bg-[var(--forebrain-brand-hover)] active:translate-y-px"
      >
        <Plus class="size-4 opacity-80" />
        {{ t('sidebar.newChat') }}
      </button>
    </div>
    <div class="px-3 pb-2">
      <div
        class="flex items-center gap-2 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg-soft)] px-3 py-2 shadow-[var(--forebrain-inset-shadow)]"
      >
        <Search class="size-4 shrink-0 text-[var(--forebrain-brand-1)]" />
        <input
          v-model="searchQuery"
          type="text"
          :placeholder="t('sidebar.search')"
          class="min-w-0 flex-1 bg-transparent text-[14px] text-[var(--forebrain-text)] outline-none placeholder:text-[var(--forebrain-muted-text)]"
        />
      </div>
    </div>
    <div class="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
      <div class="px-2 pb-2">
        <span class="text-[11px] font-medium uppercase tracking-[0.14em] text-[var(--forebrain-text-2)]">{{ t('sidebar.sessions') }}</span>
      </div>
      <div v-if="loading" class="py-8 text-center text-[14px] text-[var(--forebrain-muted-text)]">{{ t('sidebar.loading') }}</div>
      <ul v-else class="space-y-0.5">
        <li
          v-for="s in filteredSessions"
          :key="s.id"
          @click="$emit('select', s.id)"
          :class="[
            'flex cursor-pointer items-center gap-2 rounded-xl px-3 py-2.5 text-[14px] transition-colors duration-150',
            activeId === s.id
              ? 'bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-text)] shadow-[var(--forebrain-brand-inset-shadow)]'
              : 'text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-button-alt-bg)] hover:text-[var(--forebrain-text)]',
          ]"
        >
          <MessageSquare class="size-4 shrink-0 opacity-70" />
          <span class="min-w-0 flex-1 truncate">{{ s.title || t('sidebar.untitledChat') }}</span>
        </li>
      </ul>
      <p v-if="!loading && filteredSessions.length === 0" class="py-8 text-center text-[14px] text-[var(--forebrain-muted-text)]">
        {{ t('sidebar.empty') }}
      </p>
      <Alert v-if="props.error" variant="destructive" class="mt-2 rounded-xl border">
        <CircleAlert class="size-4" />
        <AlertTitle>{{ t('sidebar.loadFailed') }}</AlertTitle>
        <AlertDescription>{{ props.error }}</AlertDescription>
      </Alert>
    </div>
    <div class="mt-auto border-t border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt-glass)] p-3">
      <RouterLink
        to="/settings"
        class="mb-2 flex w-full items-center justify-center rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-button-alt-bg)] px-3 py-2 text-[13px] font-medium text-[var(--forebrain-text)] hover:bg-[var(--forebrain-button-alt-hover-bg)]"
      >
        {{ t('sidebar.settings') }}
      </RouterLink>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { RouterLink } from 'vue-router'
import { Plus, Search, MessageSquare, CircleAlert } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import type { ChatSessionItem } from '@/composables/useChatSessions'
import { useI18n } from '@/locales'

const props = defineProps<{
  sessions: ChatSessionItem[]
  loading: boolean
  activeId: string | null
  error?: string | null
}>()

defineEmits<{
  'new-chat': []
  select: [id: string]
}>()

const searchQuery = ref('')
const { t } = useI18n()

const filteredSessions = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return props.sessions
  return props.sessions.filter((s) => (s.title || '').toLowerCase().includes(q))
})
</script>
