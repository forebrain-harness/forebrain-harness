<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <CardComponent>
      <template #header>
        <div class="flex w-full flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-2">
          <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('skills.projectTitle') }}</div>
          <ScopeBadge type="project" :label="t('scope.project')" />
        </div>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="project-skills-online-install" @click="onlineOpen = true">
            {{ t('skills.onlineInstall') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="project-skills-offline-install" @click="offlineOpen = true">
            {{ t('skills.offlineInstall') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
            {{ loading ? t('common.loading') : t('common.refresh') }}
          </button>
        </div>
        </div>
      </template>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.projectDescription') }}</p>

      <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

      <!-- Trust gate: until the project is trusted, none of its skills load —
           the same gate the terminal applies. -->
      <div v-if="!trusted" class="rounded-lg border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
        <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
        <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
        <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="project-trust" @click="trust">
          {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
        </button>
      </div>

      <div v-else>
        <SkillsTable
          :rows="rows"
          :loading="loading"
          :saving="saving"
          :downloading="downloading"
          owner-origin="project"
          @toggle="onToggle"
          @delete="onDelete"
          @download="onDownload"
          @batch-download="onBatchDownload"
        />
      </div>
    </CardComponent>

    <InstallDialogs
      v-model:online="onlineOpen"
      v-model:offline="offlineOpen"
      dest="project"
      :project-id="projectId"
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
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" @click="doDelete">
            {{ saving ? t('common.loading') : t('common.delete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import InstallDialogs from '@/components/skills/InstallDialogs.vue'
import SkillsTable from '@/components/skills/SkillsTable.vue'
import forebrainApi, {
  getErrorMessage,
  saveSkillDownload,
  type ProjectRecord,
  type SkillRecord,
} from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The project's skills tab: everything the project's sessions can actually
 * load — its own skills plus the agent, shared and built-in layers it
 * inherits. Only project rows carry controls; inherited rows are read-only
 * and managed where they belong (decision D9).
 */
// The route passes the whole project record; only its id reaches the API.
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const rows = ref<SkillRecord[]>([])
const loading = ref(false)
const saving = ref(false)
const downloading = ref(false)
const error = ref('')
const onlineOpen = ref(false)
const offlineOpen = ref(false)
const confirmDelete = ref<SkillRecord | null>(null)

// The gate follows the project row the shell loaded (its trust decision is
// part of the detail read); the trust action here moves it forward.
const trusted = ref(false)
const trusting = ref(false)

async function trust() {
  if (trusting.value) return
  trusting.value = true
  error.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    trusted.value = true
    // The gate opening is what makes the listing loadable; fetch right away
    // instead of waiting for the next visit.
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    trusting.value = false
  }
}

async function load() {
  if (!trusted.value) return
  loading.value = true
  error.value = ''
  try {
    const data = await forebrainApi.projectSkillsOverview(props.projectId)
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
    const next = rows.value.map((item) => (item === row ? { ...item, enabled } : item))
    const res = await forebrainApi.projectSkillsToggle(props.projectId, next.filter((item) => item.enabled).map((item) => item.rootPath || ''))
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
    saveSkillDownload(await forebrainApi.skillDownloadBatch(names, props.projectId))
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    downloading.value = false
  }
}

function onDelete(row: SkillRecord) {
  if (row.origin !== 'project') return
  confirmDelete.value = row
}

async function doDelete() {
  const row = confirmDelete.value
  if (!row) return
  saving.value = true
  error.value = ''
  try {
    const res = await forebrainApi.skillDelete(row.name, 'project', props.projectId)
    rows.value = res.skills
    confirmDelete.value = null
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
  void load()
}, { immediate: true })
</script>
