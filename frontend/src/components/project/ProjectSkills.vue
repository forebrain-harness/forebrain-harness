<template>
  <div class="mx-auto max-w-4xl space-y-5">
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

      <template v-else>
        <ListToolbar>
          <template #left>
            <input
              v-model="q"
              type="search"
              :placeholder="t('skills.searchPlaceholder')"
              class="forebrain-field h-[34px]"
              style="max-width: 240px"
              data-testid="project-skills-search"
            />
            <select v-model="origin" class="forebrain-field h-[34px]" style="width: 110px" :aria-label="t('skills.colSource')" data-testid="project-skills-source-filter">
              <option value="">{{ t('skills.sourceAll') }}</option>
              <option value="project">{{ t('skills.originProject') }}</option>
              <option value="agent">{{ t('skills.originAgent') }}</option>
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
              data-testid="project-skills-batch-download"
              @click="batchDownload"
            >
              <Download class="size-3.5" aria-hidden="true" />{{ t('skills.batchDownload') }}
            </button>
            <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" data-testid="project-skills-offline-install" @click="offlineOpen = true">
              <Upload class="size-3.5" aria-hidden="true" />{{ t('skills.offlineInstall') }}
            </button>
            <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" data-testid="project-skills-online-install" @click="onlineOpen = true">
              <Globe class="size-3.5" aria-hidden="true" />{{ t('skills.onlineInstall') }}
            </button>
            <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" data-testid="project-skills-discover" @click="openWorkshop('discover')">
              <Search class="size-3.5" aria-hidden="true" />{{ t('skills.discoverInstall') }}
            </button>
            <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" data-testid="project-skills-create" @click="openWorkshop('create')">
              <Plus class="size-3.5" aria-hidden="true" />{{ t('skills.createSkill') }}
            </button>
            <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" @click="refresh">
              <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
            </button>
          </template>
        </ListToolbar>

        <SkillsTable
          v-model:selected="selected"
          :rows="rows"
          :loading="loading"
          :saving="saving"
          scope="project"
          @toggle="onToggle"
          @delete="onDelete"
          @download="onDownload"
          @detail="openSkill"
        />

        <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
        <ListNote>{{ t('skills.projectNote') }}</ListNote>
      </template>

    <InstallDialogs
      v-model:online="onlineOpen"
      v-model:offline="offlineOpen"
      dest="project"
      :project-id="projectId"
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
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Download, Globe, Plus, RotateCw, Search, Upload } from 'lucide-vue-next'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import TablePager from '@/components/common/TablePager.vue'
import InstallDialogs from '@/components/skills/InstallDialogs.vue'
import SkillsTable from '@/components/skills/SkillsTable.vue'
import { useServerList } from '@/composables/useServerList'
import forebrainApi, {
  getErrorMessage,
  saveSkillDownload,
  type ProjectRecord,
  type SkillRecord,
} from '@/lib/api'
import { workshopRoute } from '@/lib/workshopTarget'
import { useI18n } from '@/locales'

/**
 * The project's skills tab: everything the project's sessions can actually
 * load — its own skills plus the agent, shared and built-in layers it
 * inherits. Only project rows carry controls; inherited rows are read-only
 * and managed where they belong (decision D9).
 *
 * The table is the server's (plan 073): the search and the layer filter narrow
 * it there and the pager says how many rows there are. This tab's own title
 * and layout are the project space's business (plan 075); it only carries the
 * list's toolbar and pager.
 */
// The route passes the whole project record; only its id reaches the API.
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()
const router = useRouter()

function openWorkshop(mode: 'create' | 'discover') {
  void router.push(workshopRoute({ scope: 'project', mode, projectId: props.projectId }))
}

function openSkill(row: SkillRecord) {
  void router.push(workshopRoute({ scope: 'project', mode: 'improve', skill: row.name, projectId: props.projectId }))
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
} = useServerList<SkillRecord>(
  (query) => forebrainApi.projectSkillsPage(props.projectId, query),
  { pageSize: 10, filters: { origin: '' } },
)

/** The layer filter, read and written through the list's own filters. */
const origin = computed({
  get: () => String(filters.value.origin ?? ''),
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

// The gate follows the project row the shell loaded (its trust decision is
// part of the detail read); the trust action here moves it forward.
const trusted = ref(false)
const trusting = ref(false)

function refresh(): void {
  if (!trusted.value) return
  void reload()
}

async function trust() {
  if (trusting.value) return
  trusting.value = true
  error.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    trusted.value = true
    // The gate opening is what makes the listing loadable; fetch right away
    // instead of waiting for the next visit.
    await reload()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    trusting.value = false
  }
}

// The toggle endpoint takes the complete enabled list, so the whole set is
// read back before it is sent — the page holds one page of rows.
async function onToggle(row: SkillRecord, enabled: boolean) {
  saving.value = true
  error.value = ''
  try {
    const whole = await forebrainApi.projectSkillsOverview(props.projectId)
    const next = whole.installed.map((item) => (item.name === row.name ? { ...item, enabled } : item))
    await forebrainApi.projectSkillsToggle(props.projectId, next.filter((item) => item.enabled).map((item) => item.rootPath || ''))
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
    await forebrainApi.skillDelete(row.name, 'project', props.projectId)
    confirmDelete.value = null
    selected.value = new Set()
    await reload()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
  if (trusted.value) void reload()
}, { immediate: true })
</script>
