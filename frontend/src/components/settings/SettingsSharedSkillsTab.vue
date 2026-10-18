<template>
  <div class="pb-6">
    <ListToolbar>
      <template #left>
        <input
          v-model="q"
          type="search"
          :placeholder="t('skills.searchPlaceholder')"
          class="forebrain-field h-[34px]"
          style="max-width: 240px"
          data-testid="shared-skills-search"
        />
        <select v-model="origin" class="forebrain-field h-[34px]" style="width: 110px" :aria-label="t('skills.colSource')" data-testid="shared-skills-source-filter">
          <option value="shared,builtin">{{ t('skills.sourceAll') }}</option>
          <option value="shared">{{ t('skills.originShared') }}</option>
          <option value="builtin">{{ t('skills.originBuiltin') }}</option>
        </select>
      </template>
      <template #actions>
        <span v-if="selected.size" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.selectedCount', { count: selected.size }) }}</span>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs"
          :disabled="!selected.size || downloading"
          data-testid="shared-skills-batch-download"
          @click="batchDownload"
        >
          <Download class="size-3.5" aria-hidden="true" />{{ t('skills.batchDownload') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" data-testid="shared-skills-online-install" @click="onlineOpen = true">
          <Globe class="size-3.5" aria-hidden="true" />{{ t('skills.onlineInstall') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" data-testid="shared-skills-offline-install" @click="offlineOpen = true">
          <Upload class="size-3.5" aria-hidden="true" />{{ t('skills.offlineInstall') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" @click="refresh">
          <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
        </button>
      </template>
    </ListToolbar>
    <p class="mt-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.sharedDescription') }}</p>

    <p v-if="error" class="mt-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <div class="mt-4">
      <SkillsTable
        v-model:selected="selected"
        :rows="rows"
        :loading="loading"
        :saving="saving"
        scope="shared"
        @toggle="onToggle"
        @delete="onDelete"
        @download="onDownload"
        @detail="openSkill"
      />
    </div>

    <TablePager class="mt-3" :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    <ListNote class="mt-3" data-testid="shared-skills-note">{{ t('skills.sharedNote') }}</ListNote>

    <InstallDialogs
      v-model:online="onlineOpen"
      v-model:offline="offlineOpen"
      dest="global"
      @installed="refresh"
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
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Download, Globe, RotateCw, Upload } from 'lucide-vue-next'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import TablePager from '@/components/common/TablePager.vue'
import InstallDialogs from '@/components/skills/InstallDialogs.vue'
import SkillsTable from '@/components/skills/SkillsTable.vue'
import { useServerList } from '@/composables/useServerList'
import forebrainApi, {
  getErrorMessage,
  saveSkillDownload,
  type SkillRecord,
} from '@/lib/api'
import { workshopRoute } from '@/lib/workshopTarget'
import { useI18n } from '@/locales'

/**
 * The shared skills library: every primary agent loads from it, so this is
 * where its rows are toggled, installed and deleted (decision D9).
 *
 * The listing is the server's (plan 073): this tab's slice is the shared and
 * built-in layers, so the layer filter says so — 全部来源 is `shared,builtin`,
 * sent as the endpoint's `origin`, and the pager says how many rows that is. A
 * toggle still submits the whole enabled set the endpoint expects, which is a
 * fact of the listing rather than of the page on screen.
 */
const { t } = useI18n()
const router = useRouter()

function openSkill(row: SkillRecord) {
  void router.push(workshopRoute({ scope: 'agent', mode: 'improve', skill: row.name }))
}

const {
  rows,
  total,
  page,
  pageSize,
  q,
  filters,
  loading,
  reload,
} = useServerList<SkillRecord>(forebrainApi.skillsPage, {
  pageSize: 10,
  filters: { origin: 'shared,builtin' },
})

/** The layer filter, read and written through the list's own filters. */
const origin = computed({
  get: () => String(filters.value.origin ?? 'shared,builtin'),
  set: (value: string) => {
    filters.value = { ...filters.value, origin: value }
  },
})

const saving = ref(false)
const downloading = ref(false)
const error = ref('')
const onlineOpen = ref(false)
const offlineOpen = ref(false)
const confirmDelete = ref<SkillRecord | null>(null)
/** The rows ticked for a bulk download, by name. */
const selected = ref(new Set<string>())

function refresh(): void {
  void reload()
}

async function onToggle(row: SkillRecord, enabled: boolean) {
  saving.value = true
  error.value = ''
  try {
    // Full-set semantics: every enabled row is submitted — inherited rows
    // keep their current state by being sent unchanged, not by being left
    // out. The page holds one page of rows, so the whole set is read back.
    const whole = await forebrainApi.skillsOverview()
    const next = whole.installed.map((item) => (item.name === row.name ? { ...item, enabled } : item))
    await forebrainApi.skillsToggle(next.filter((item) => item.enabled).map((item) => item.rootPath || ''))
    await reload()
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

async function batchDownload() {
  const names = [...selected.value]
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
    await forebrainApi.skillDelete(row.name, 'shared')
    confirmDelete.value = null
    selected.value = new Set()
    await reload()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void reload()
})
</script>
