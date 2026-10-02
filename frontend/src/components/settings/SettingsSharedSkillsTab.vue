<template>
  <div class="pb-6">
    <div class="flex flex-wrap items-center gap-2">
      <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="shared-skills-online-install" @click="onlineOpen = true">
        {{ t('skills.onlineInstall') }}
      </button>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="shared-skills-offline-install" @click="offlineOpen = true">
        {{ t('skills.offlineInstall') }}
      </button>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
        {{ loading ? t('common.loading') : t('common.refresh') }}
      </button>
    </div>
    <p class="mt-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.sharedDescription') }}</p>

    <p v-if="error" class="mt-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <div class="mt-4">
      <SkillsTable
        :rows="visibleRows"
        :loading="loading"
        :saving="saving"
        :downloading="downloading"
        owner-origin="shared"
        @toggle="onToggle"
        @delete="onDelete"
        @download="onDownload"
        @batch-download="onBatchDownload"
      />
    </div>

    <InstallDialogs
      v-model:online="onlineOpen"
      v-model:offline="offlineOpen"
      dest="global"
      @installed="load"
    />

    <div
      v-if="confirmDelete"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
      data-testid="skills-delete-confirm"
      @click.self="confirmDelete = null"
    >
      <div class="w-full max-w-md rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('skills.deleteTitle', { name: confirmDelete.name }) }}</div>
        <p class="mt-2 text-[12px] text-[var(--forebrain-text-2)]">{{ t('skills.deleteHint') }}</p>
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="confirmDelete = null">{{ t('common.cancel') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" @click="doDelete">
            {{ saving ? t('common.loading') : t('common.delete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import InstallDialogs from '@/components/skills/InstallDialogs.vue'
import SkillsTable from '@/components/skills/SkillsTable.vue'
import forebrainApi, {
  getErrorMessage,
  saveSkillDownload,
  type SkillRecord,
} from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The shared skills library: every primary agent loads from it, so this is
 * where its rows are toggled, installed and deleted (decision D9). The
 * listing itself still loads the full effective set — the toggle endpoint
 * takes the complete enabled list — and shows the shared and built-in slice.
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

const visibleRows = computed(() => rows.value.filter((row) => row.origin === 'shared' || row.origin === 'builtin'))

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

async function onToggle(row: SkillRecord, enabled: boolean) {
  saving.value = true
  error.value = ''
  try {
    // Full-set semantics: inherited rows keep their current state by being
    // submitted unchanged, not by being left out.
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
  if (row.origin !== 'shared') return
  confirmDelete.value = row
}

async function doDelete() {
  const row = confirmDelete.value
  if (!row) return
  saving.value = true
  error.value = ''
  try {
    const res = await forebrainApi.skillDelete(row.name, 'shared')
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
