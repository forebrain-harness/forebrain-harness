<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

      <ListToolbar>
        <template #left>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('rules.searchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="rules-search"
          />
        </template>
        <template #actions>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs" data-testid="rules-create" @click="openCreate">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('rules.newFile') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </Button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('rules.empty') }}</div>
      <DataTable
        v-else
        :columns="columns"
        :rows="rows"
        :row-key="(row) => row.name"
        :row-attrs="(row) => ({ 'data-testid': 'rule-file-row', 'data-rule-file': row.name })"
      >
        <template #cell-name="{ row }">
          <div class="flex min-w-0 items-center gap-1.5">
            <span class="min-w-0 truncate font-mono">{{ row.name }}</span>
            <BadgeComponent v-if="!row.exists" variant="gray" class="shrink-0">{{ t('rules.notCreated') }}</BadgeComponent>
          </div>
        </template>
        <template #cell-purpose="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="purposeFor(row.name)">{{ purposeFor(row.name) }}</span>
        </template>
        <template #cell-updated="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction
              :icon="Pencil"
              :label="t('rules.editFile')"
              :name="row.name"
              :data-testid="`rule-edit-${row.name}`"
              @click="openEdit(row.name)"
            />
          </RowActions>
        </template>
      </DataTable>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('rules.note') }}</ListNote>
    </div>

    <ForebrainModal :open="creating" size="sm" :title="t('rules.createModalTitle')" @close="creating = false">
      <!-- The label is its own element rather than a wrapper: the control is the
           library's Select, a button, and a button inside a `label` would answer
           one click with two. -->
      <div class="block">
        <Label for="rules-create-select" class="mb-1 block text-[12px] font-normal leading-[normal] text-[var(--forebrain-text-2)]">{{ t('rules.fileLabel') }}</Label>
        <Select :model-value="createName" @update:model-value="(value) => (createName = String(value ?? ''))">
          <SelectTrigger id="rules-create-select" class="forebrain-field w-full text-[13px]" data-testid="rules-create-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="name in creatable" :key="name" :value="name">{{ name }}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('rules.createOnlyHint') }}</p>
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="creating = false">{{ t('common.cancel') }}</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="!createName" data-testid="rules-create-ok" @click="confirmCreate">{{ t('rules.create') }}</Button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="editing !== ''" size="xl" :title="editing" @close="closeEdit">
      <div data-testid="rules-editor-modal">
        <p v-if="warning" class="mb-2 text-[12px] text-[var(--forebrain-danger)]">{{ t('rules.budgetWarning') }}</p>
        <div v-if="!loaded" class="min-h-[200px]">{{ t('common.loading') }}</div>
        <FileEditor v-else :key="editing" height="420px" :path="editing" v-model="draft" @dirty="dirty = $event" />
      </div>
      <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('rules.applyShort') }}</p>
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="closeEdit">{{ t('common.cancel') }}</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="saving || !dirty" data-testid="rules-save" @click="save">
          {{ saving ? t('common.loading') : t('common.save') }}
        </Button>
      </template>
    </ForebrainModal>
  </div>
</template>

<script setup lang="ts">
import { defineAsyncComponent, onMounted, ref } from 'vue'
import { Pencil, Plus, RotateCw } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type RuleFileSummary } from '@/lib/api'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

const FileEditor = defineAsyncComponent(() => import('@/components/editor/FileEditor.vue'))

/**
 * The primary agent's instruction files (AGENTS.md / SOUL.md / USER.md) as a
 * list: the files themselves, and the buttons above it. Creating and editing
 * happen in modals, so the page never grows an inline form. Editing them is
 * editing what every session of this tenant is told before its first word;
 * changes start with the next session, never mid-run.
 *
 * The table is server-paged (plan 073): the search narrows it there, and the
 * create modal still offers every file that does not exist yet, which is a fact
 * of the whole list rather than of the page on screen.
 */
const { t } = useI18n()

const { rows, total, page, pageSize, q, loading, error, reload } = useServerList<RuleFileSummary>(
  forebrainApi.agentRuleFilesPage,
  { pageSize: 10 },
)

/**
 * The table's columns: the name, what the file is for and when it changed share
 * the width, the actions keep their 64px. Nothing sorts — the list endpoint
 * ignores `sort`.
 */
const columns: DataColumn[] = [
  { id: 'name', header: t('rules.colFile') },
  { id: 'purpose', header: t('rules.colPurpose'), cellClass: 'text-[var(--forebrain-text-2)]' },
  { id: 'updated', header: t('rules.colUpdated'), cellClass: 'font-mono text-[var(--forebrain-text-2)]' },
  { id: 'actions', header: t('rules.colActions'), size: 64, headerClass: 'text-center', cellClass: 'text-right' },
]

const notice = ref('')
const creating = ref(false)
const createName = ref('')
/** What the create modal may offer: the files that do not exist yet. */
const creatable = ref<string[]>([])
const editing = ref('')
const loaded = ref(false)
const draft = ref('')
const dirty = ref(false)
const saving = ref(false)
const warning = ref(false)

const purposes: Record<string, () => string> = {
  'AGENTS.md': () => t('rules.purposeAgents'),
  'SOUL.md': () => t('rules.purposeSoul'),
  'USER.md': () => t('rules.purposeUser'),
}

function purposeFor(name: string): string {
  return purposes[name] ? purposes[name]() : ''
}

function refresh(): void {
  void reload()
}

async function openCreate() {
  error.value = ''
  creatable.value = []
  createName.value = ''
  creating.value = true
  try {
    const whole = await forebrainApi.agentRuleFiles()
    creatable.value = (whole.files ?? []).filter((file) => !file.exists).map((file) => file.name)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
  createName.value = creatable.value[0] ?? ''
}

function confirmCreate() {
  if (!createName.value) return
  const name = createName.value
  creating.value = false
  void openEdit(name)
}

async function openEdit(name: string) {
  editing.value = name
  loaded.value = false
  dirty.value = false
  warning.value = false
  notice.value = ''
  try {
    const file = await forebrainApi.agentRuleFile(name)
    // A file not created yet reads as empty; saving it creates it.
    draft.value = file.content
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loaded.value = true
  }
}

function closeEdit() {
  editing.value = ''
  loaded.value = false
  dirty.value = false
}

async function save() {
  if (saving.value || !dirty.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  warning.value = false
  try {
    const res = await forebrainApi.saveAgentRuleFile(editing.value, draft.value)
    warning.value = res.warning === 'exceeds_assembly_budget'
    notice.value = t('rules.savedNotice')
    dirty.value = false
    closeEdit()
    await reload()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void reload()
})
</script>
