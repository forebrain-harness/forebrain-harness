<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-2">
          <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('projects.sessionsTitle') }}</div>
          <span class="scope-badge">{{ t('scope.project') }}</span>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="creating" @click="createSession">
          {{ creating ? t('common.loading') : t('projects.newSession') }}
        </button>
      </div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.sessionsDescription') }}</p>

      <p v-if="error" class="mt-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading" class="mt-4 text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!sessions.length" class="mt-6 rounded-xl border border-dashed border-[var(--forebrain-divider)] px-6 py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">
        {{ t('projects.noSessions') }}
      </div>
      <ul v-else class="mt-4 divide-y divide-[var(--forebrain-divider)]">
        <li v-for="session in sessions" :key="session.id">
          <button type="button" class="flex w-full items-center justify-between gap-3 py-3 text-left" @click="openSession(session.id)">
            <span class="min-w-0 flex-1 truncate text-[13px] text-[var(--forebrain-text)]">{{ session.title || t('projects.untitledSession') }}</span>
            <span class="shrink-0 text-[11px] text-[var(--forebrain-muted-text)]">{{ formatTime(session.updatedAt) }}</span>
          </button>
        </li>
      </ul>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getErrorMessage, forebrainApi, type ProjectSessionRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * Only this project's sessions: the drawer keeps non-project conversations,
 * so a project's work lives behind its own door (owner decision D6).
 */
const props = defineProps<{ project: unknown; projectId: string }>()

const { t } = useI18n()
const router = useRouter()

const sessions = ref<ProjectSessionRecord[]>([])
const loading = ref(false)
const creating = ref(false)
const error = ref('')

function formatTime(unix?: number): string {
  if (!unix) return ''
  return new Date(unix * 1000).toLocaleString()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    sessions.value = await forebrainApi.projectSessions(props.projectId)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

async function createSession() {
  if (creating.value) return
  creating.value = true
  error.value = ''
  try {
    const created = await forebrainApi.projectSessionCreate(props.projectId, '')
    await router.push({ path: '/', query: { session: created.id } })
  } catch (cause) {
    error.value = getErrorMessage(cause)
    creating.value = false
  }
}

function openSession(id: string) {
  void router.push({ path: '/', query: { session: id } })
}

onMounted(() => {
  void load()
})
</script>

<style scoped>
.scope-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 9999px;
  padding: 2px 10px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
</style>
