<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <!-- Trust gate: project rules only load for a trusted project. -->
    <div v-if="!trusted" class="rounded-lg border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
      <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="rules-trust" @click="trust">
        {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
      </button>
    </div>

    <template v-else>
      <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('rules.searchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="rules-search"
          />
        </template>
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" data-testid="rules-create" @click="openCreate">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('rules.newFile') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('rules.empty') }}</div>
      <DataTable
        v-else
        :columns="columns"
        :rows="rows"
        :row-key="(row) => row.dir || '__root__'"
        :row-attrs="(row) => ({ 'data-testid': 'rule-file-row', 'data-rule-file': label(row.dir) })"
      >
        <template #cell-name="{ row }">
          <div class="flex min-w-0 items-center gap-1.5">
            <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="label(row.dir)">{{ label(row.dir) }}</span>
            <BadgeComponent v-if="!row.exists" variant="gray" class="shrink-0">{{ t('rules.notCreated') }}</BadgeComponent>
          </div>
        </template>
        <template #cell-purpose="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="purposeFor(row.dir)">{{ purposeFor(row.dir) }}</span>
        </template>
        <template #cell-updated="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction
              :icon="Pencil"
              :label="t('rules.editFile')"
              :name="label(row.dir)"
              :data-testid="`rule-edit-${label(row.dir)}`"
              @click="openEdit(row.dir)"
            />
          </RowActions>
        </template>
      </DataTable>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('rules.projectNote') }}</ListNote>
    </template>

    <ForebrainModal :open="creating" size="sm" :title="t('rules.createModalTitle')" @close="creating = false">
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('rules.typeLabel') }}</span>
        <select class="forebrain-field w-full text-[13px]" data-testid="rules-create-type">
          <option>{{ t('rules.subdirType') }}</option>
        </select>
      </label>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('rules.dirLabel') }}</span>
        <select v-model="createDir" class="forebrain-field w-full text-[13px]" data-testid="rules-create-select">
          <option v-for="dir in creatable" :key="dir" :value="dir">{{ dir }}</option>
        </select>
      </label>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="creating = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!createDir" data-testid="rules-create-ok" @click="confirmCreate">{{ t('rules.create') }}</button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="editing !== null" size="xl" :title="label(editDir)" @close="closeEdit">
      <div data-testid="rules-editor-modal">
        <p v-if="warning" class="mb-2 text-[12px] text-[var(--forebrain-danger)]">{{ t('rules.budgetWarning') }}</p>
        <div v-if="!loaded" class="min-h-[200px]">{{ t('common.loading') }}</div>
        <FileEditor v-else :key="editDir" height="420px" :path="label(editDir)" v-model="draft" @dirty="dirty = $event" />
      </div>
      <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('rules.applyShort') }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeEdit">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving || !dirty" data-testid="rules-save" @click="save">
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </ForebrainModal>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { Pencil, Plus } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { getErrorMessage, forebrainApi, type ListQuery, type ProjectRecord, type ProjectRuleFileRow } from '@/lib/api'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

const FileEditor = defineAsyncComponent(() => import('@/components/editor/FileEditor.vue'))

/**
 * This project's rules tab: the FOREBRAIN.md chain over its own directories —
 * one file per directory layer, root first. Editing happens in modals, and the
 * project instructions every session is told up front live in 项目设置, not
 * here. Only the project's own files appear — the chain is assembled from its
 * root, never from anything inherited.
 *
 * The table is the server's (plan 075): the search narrows it there, and the
 * create modal still offers every layer without a file yet, which is a fact of
 * the whole list rather than of the page on screen.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const trusted = ref(false)
const trusting = ref(false)

/** The layers with no FOREBRAIN.md yet: what the create modal may offer. */
const creatable = ref<string[]>([])
const notice = ref('')
const creating = ref(false)
const createDir = ref('')
const editing = ref<string | null>(null)
const editDir = computed(() => editing.value ?? '')
const loaded = ref(false)
const draft = ref('')
const dirty = ref(false)
const saving = ref(false)
const warning = ref(false)

async function fetchFiles(query: ListQuery) {
  const answer = await forebrainApi.projectRuleFilesPage(props.projectId, query)
  creatable.value = answer.create
  return answer
}

const { rows, total, page, pageSize, q, loading, error: listError, reload } = useServerList<ProjectRuleFileRow>(fetchFiles, { pageSize: 10 })

/**
 * The table's columns, at the design's widths: the file name takes the room the
 * others leave. Nothing sorts — the list endpoint ignores `sort`.
 */
const columns: DataColumn[] = [
  { id: 'name', header: t('rules.colFile') },
  { id: 'purpose', header: t('rules.colPurpose'), size: 220, cellClass: 'text-[var(--forebrain-text-2)]' },
  { id: 'updated', header: t('rules.colUpdated'), size: 150, cellClass: 'forebrain-num font-mono text-[var(--forebrain-text-2)]' },
  { id: 'actions', header: t('rules.colActions'), size: 56, headerClass: 'text-center', cellClass: 'text-right' },
]

const writeError = ref('')
const error = computed(() => listError.value || writeError.value)

function label(dir: string): string {
  return dir ? `${dir}/FOREBRAIN.md` : 'FOREBRAIN.md'
}

function purposeFor(dir: string): string {
  return dir ? t('rules.purposeSubdir') : t('rules.purposeRoot')
}

watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
}, { immediate: true })

async function trust() {
  if (trusting.value) return
  trusting.value = true
  writeError.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    // The gate's own watcher reloads the table once trust moves.
    trusted.value = true
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    trusting.value = false
  }
}

function openCreate() {
  createDir.value = creatable.value[0] ?? ''
  creating.value = true
}

function confirmCreate() {
  if (!createDir.value) return
  const dir = createDir.value
  creating.value = false
  void openEdit(dir)
}

async function openEdit(dir: string) {
  editing.value = dir
  loaded.value = false
  dirty.value = false
  warning.value = false
  try {
    const file = await forebrainApi.projectRuleFile(props.projectId, dir)
    // A layer without a file yet reads as empty; saving it creates it.
    draft.value = file.content
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    loaded.value = true
  }
}

function closeEdit() {
  editing.value = null
  loaded.value = false
  dirty.value = false
}

async function save() {
  if (saving.value || !dirty.value || editing.value === null) return
  saving.value = true
  writeError.value = ''
  notice.value = ''
  warning.value = false
  try {
    const res = await forebrainApi.saveProjectRuleFile(props.projectId, editing.value, draft.value)
    warning.value = res.warning === 'exceeds_assembly_budget'
    notice.value = t('rules.savedNotice')
    dirty.value = false
    closeEdit()
    await reload()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

// The listing is only asked once the project is trusted: its files live in the
// project's own directory.
watch(trusted, (value) => {
  if (value) void reload()
}, { immediate: true })
</script>
