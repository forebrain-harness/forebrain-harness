<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('skills.searchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="skills-search"
          />
          <!-- 全部来源 is the empty string the list filters by, which a
               SelectItem may not carry: the sentinel stands in for it on both
               sides. -->
          <Select :model-value="toChoice(origin)" @update:model-value="(value) => (origin = fromChoice(value))">
            <SelectTrigger
              class="forebrain-field !h-[34px]"
              style="width: 110px"
              :aria-label="t('skills.colSource')"
              data-testid="skills-source-filter"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="EMPTY_CHOICE">{{ t('skills.sourceAll') }}</SelectItem>
              <SelectItem value="agent">{{ t('skills.originAgent') }}</SelectItem>
              <SelectItem value="shared">{{ t('skills.originShared') }}</SelectItem>
              <SelectItem value="builtin">{{ t('skills.originBuiltin') }}</SelectItem>
            </SelectContent>
          </Select>
        </template>
        <template #actions>
          <span v-if="selected.size" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.selectedCount', { count: selected.size }) }}</span>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs"
            :disabled="!selected.size || downloading"
            data-testid="skills-batch-download"
            @click="batchDownload"
          >
            <Download class="size-3.5" aria-hidden="true" />{{ t('skills.batchDownload') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" data-testid="skills-offline-install" @click="offlineOpen = true">
            <Upload class="size-3.5" aria-hidden="true" />{{ t('skills.offlineInstall') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" data-testid="skills-online-install" @click="onlineOpen = true">
            <Globe class="size-3.5" aria-hidden="true" />{{ t('skills.onlineInstall') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" data-testid="skills-discover" @click="openWorkshop('discover')">
            <Search class="size-3.5" aria-hidden="true" />{{ t('skills.discoverInstall') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs" data-testid="skills-create" @click="openWorkshop('create')">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('skills.createSkill') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </Button>
        </template>
      </ListToolbar>

      <SkillsTable
        v-model:selected="selected"
        :rows="rows"
        :loading="loading"
        :saving="saving"
        scope="agent"
        @toggle="onToggle"
        @delete="onDelete"
        @download="onDownload"
        @detail="openSkill"
      />

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('skills.note') }}</ListNote>
    </div>

    <InstallDialogs
      v-model:online="onlineOpen"
      v-model:offline="offlineOpen"
      dest="workspace"
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
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="confirmDelete = null">{{ t('common.cancel') }}</Button>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="saving" data-testid="skills-delete-confirm-ok" @click="doDelete">
            {{ saving ? t('common.loading') : t('common.delete') }}
          </Button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Download, Globe, Plus, RotateCw, Search, Upload } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
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
import { EMPTY_CHOICE, fromChoice, toChoice } from '@/lib/selectValue'
import { workshopRoute } from '@/lib/workshopTarget'
import { useI18n } from '@/locales'

/**
 * The primary agent's skills page: the effective set this agent's sessions
 * load — its own skills plus the shared, built-in and cross-tool layers it
 * inherits. Only agent rows carry controls; every inherited row is read-only
 * and managed in the layer that owns it (decision D9).
 *
 * The table is the server's (plan 073): the search and the layer filter narrow
 * it there, and the pager under it says how many skills the layer holds. The
 * source filter offers only the layers this page can actually contain. A
 * toggle still submits the whole enabled set the endpoint expects, which is a
 * fact of the listing rather than of the page on screen.
 */
const { t } = useI18n()
const router = useRouter()

const {
  rows,
  total,
  page,
  pageSize,
  q,
  filters,
  loading,
  error: listError,
  reload,
} = useServerList<SkillRecord>(forebrainApi.skillsPage, { pageSize: 10, filters: { origin: '' } })

const actionError = ref('')
const error = computed(() => actionError.value || listError.value)

/** The layer filter, read and written through the list's own filters. */
const origin = computed({
  get: () => String(filters.value.origin ?? ''),
  set: (value: string) => {
    filters.value = { ...filters.value, origin: value }
  },
})

function openWorkshop(mode: 'create' | 'discover') {
  void router.push(workshopRoute({ scope: 'agent', mode }))
}

function openSkill(row: SkillRecord) {
  void router.push(workshopRoute({ scope: 'agent', mode: 'improve', skill: row.name }))
}

const saving = ref(false)
const downloading = ref(false)
const onlineOpen = ref(false)
const offlineOpen = ref(false)
const confirmDelete = ref<SkillRecord | null>(null)
/** The rows ticked for a bulk download, by name. */
const selected = ref(new Set<string>())

function refresh(): void {
  void reload()
}

// Toggles keep the full-set semantics the endpoint has: every enabled row is
// submitted — inherited rows included, unchanged — so disabling one skill
// never silently disables another. The page holds one page of rows, so the
// whole set is read back before it is sent.
async function onToggle(row: SkillRecord, enabled: boolean) {
  saving.value = true
  actionError.value = ''
  try {
    const whole = await forebrainApi.skillsOverview()
    const next = whole.installed.map((item) => (item.name === row.name ? { ...item, enabled } : item))
    await forebrainApi.skillsToggle(next.filter((item) => item.enabled).map((item) => item.rootPath || ''))
    await reload()
  } catch (e: unknown) {
    actionError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function onDownload(row: SkillRecord) {
  if (!row.downloadUrl) return
  downloading.value = true
  actionError.value = ''
  try {
    saveSkillDownload(await forebrainApi.skillDownload(row.downloadUrl))
  } catch (e: unknown) {
    actionError.value = getErrorMessage(e)
  } finally {
    downloading.value = false
  }
}

async function batchDownload() {
  const names = [...selected.value]
  if (!names.length) return
  downloading.value = true
  actionError.value = ''
  try {
    saveSkillDownload(await forebrainApi.skillDownloadBatch(names))
  } catch (e: unknown) {
    actionError.value = getErrorMessage(e)
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
  actionError.value = ''
  try {
    await forebrainApi.skillDelete(row.name, 'agent')
    confirmDelete.value = null
    selected.value = new Set()
    await reload()
  } catch (e: unknown) {
    actionError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void reload()
})
</script>
