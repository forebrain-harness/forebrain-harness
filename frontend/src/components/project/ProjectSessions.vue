<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <PageTopRow :title="t('projects.sessionsTitle')" :description="t('projects.sessionsDescription')">
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="creating" @click="createSession">
          {{ creating ? t('common.loading') : t('projects.newSession') }}
        </button>
      </template>
    </PageTopRow>

    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <div v-if="loading" class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!sessions.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-6 py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">
      {{ t('projects.noSessions') }}
    </div>
    <div v-else class="forebrain-list-boxed">
      <ul class="divide-y divide-[var(--forebrain-divider)]">
        <li v-for="session in sessions" :key="session.id">
          <button type="button" class="flex w-full items-center justify-between gap-3 py-3 text-left" @click="openSession(session.id)">
            <span class="min-w-0 flex-1 truncate text-[13px] text-[var(--forebrain-text)]">{{ session.title || t('projects.untitledSession') }}</span>
            <span class="shrink-0 text-[11px] text-[var(--forebrain-muted-text)]">{{ formatTime(session.updatedAt) }}</span>
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import PageTopRow from '@/components/common/PageTopRow.vue'
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
