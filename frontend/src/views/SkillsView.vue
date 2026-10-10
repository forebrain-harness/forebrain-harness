<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-5xl">
      <div class="flex items-center gap-2">
        <h1 class="text-xl font-medium text-[var(--forebrain-text)]">{{ t('skills.title') }}</h1>
        <ScopeBadge type="agent" :label="t('scope.agent')" />
      </div>
      <p class="mt-1 text-[13px] text-[var(--forebrain-text-2)]">{{ t('skills.agentDescription') }}</p>

      <div class="mt-4 flex flex-wrap items-center gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="skills-online-install" @click="onlineOpen = true">
          {{ t('skills.onlineInstall') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="skills-offline-install" @click="offlineOpen = true">
          {{ t('skills.offlineInstall') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ loading ? t('common.loading') : t('common.refresh') }}
        </button>
        <RouterLink
          to="/workshop"
          class="ml-auto text-[12px] text-[var(--forebrain-brand-1)] hover:underline"
          data-testid="skills-workshop-hint"
        >
          {{ t('skills.workshopHint') }}
        </RouterLink>
      </div>

      <p v-if="error" class="mt-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

      <div class="mt-4">
        <SkillsTable
          :rows="rows"
          :loading="loading"
          :saving="saving"
          :downloading="downloading"
          owner-origin="agent"
          @toggle="onToggle"
          @delete="onDelete"
          @download="onDownload"
          @batch-download="onBatchDownload"
        />
      </div>
    </div>

    <InstallDialogs
      v-model:online="onlineOpen"
      v-model:offline="offlineOpen"
      dest="workspace"
      @installed="load"
    />

    <div
      v-if="confirmDelete"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
      data-testid="skills-delete-confirm"
      @click.self="confirmDelete = null"
    >
      <div class="w-full max-w-md rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('skills.deleteTitle', { name: confirmDelete.name }) }}</div>
        <p class="mt-2 text-[12px] text-[var(--forebrain-text-2)]">{{ t('skills.deleteHint') }}</p>
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="confirmDelete = null">{{ t('common.cancel') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" data-testid="skills-delete-confirm-ok" @click="doDelete">
            {{ saving ? t('common.loading') : t('common.delete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import InstallDialogs from '@/components/skills/InstallDialogs.vue'
import SkillsTable from '@/components/skills/SkillsTable.vue'
import forebrainApi, {
  getErrorMessage,
  saveSkillDownload,
  type SkillRecord,
} from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The primary agent's skills page: the effective set this agent's sessions
 * load — its own skills plus the shared, built-in and cross-tool layers it
 * inherits. Only agent rows carry controls; every inherited row is read-only
 * and managed in the layer that owns it (decision D9).
 */
const { t } = useI18n()

const rows = ref<SkillRecord[]>([])
const loading = ref(false)
const saving = ref(false)
const downloading = ref(false)
const error = ref('')
const onlineOpen = ref(false)
const offlineOpen = ref(false)
const confirmDelete = ref<SkillRecord | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await forebrainApi.skillsOverview()
    rows.value = data.installed
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

// Toggles keep the full-set semantics the endpoint has: every enabled row is
// submitted — inherited rows included, unchanged — so disabling one skill
// never silently disables another.
async function onToggle(row: SkillRecord, enabled: boolean) {
  saving.value = true
  error.value = ''
  try {
    const next = rows.value.map((item) => (item === row ? { ...item, enabled } : item))
    const res = await forebrainApi.skillsToggle(next.filter((item) => item.enabled).map((item) => item.rootPath || ''))
    rows.value = res.skills
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function onDownload(row: SkillRecord) {
  if (!row.downloadUrl) return
  downloading.value = true
  error.value = ''
  try {
    saveSkillDownload(await forebrainApi.skillDownload(row.downloadUrl))
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    downloading.value = false
  }
}

async function onBatchDownload(names: string[]) {
  if (!names.length) return
  downloading.value = true
  error.value = ''
  try {
    saveSkillDownload(await forebrainApi.skillDownloadBatch(names))
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    downloading.value = false
  }
}

function onDelete(row: SkillRecord) {
  if (row.origin !== 'agent') return
  confirmDelete.value = row
}

async function doDelete() {
  const row = confirmDelete.value
  if (!row) return
  saving.value = true
  error.value = ''
  try {
    const res = await forebrainApi.skillDelete(row.name, 'agent')
    rows.value = res.skills
    confirmDelete.value = null
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
