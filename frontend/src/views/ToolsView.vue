<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('tools.searchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="tools-search"
          />
          <select v-model="category" class="forebrain-field h-[34px]" style="width: 110px" :aria-label="t('tools.colCategory')" data-testid="tools-category-filter">
            <option value="">{{ t('tools.categoryAll') }}</option>
            <option v-for="name in categories" :key="name" :value="name">{{ categoryLabel(name) }}</option>
          </select>
          <select v-model="typeFilter" class="forebrain-field h-[34px]" style="width: 110px" :aria-label="t('tools.colType')" data-testid="tools-type-filter">
            <option value="">{{ t('tools.typeAll') }}</option>
            <option value="readonly">{{ t('tools.type.readonly') }}</option>
            <option value="write">{{ t('tools.type.write') }}</option>
          </select>
        </template>
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.empty') }}</div>
      <DataTable
        v-else
        :columns="columns"
        :rows="rows"
        :row-key="(row) => row.name"
        :row-attrs="(row) => ({ 'data-tool-row': row.name })"
        :row-class="(row) => ({ 'tool-row--open': openTool === row.name })"
        :sub-row="(row) => openTool === row.name"
      >
        <template #cell-name="{ row }">
          <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="row.name">{{ row.name }}</span>
        </template>
        <template #cell-description="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.description ?? ''">{{ row.description }}</span>
        </template>
        <template #cell-category="{ row }">{{ categoryLabel(row.category ?? '') }}</template>
        <template #cell-readOnly="{ row }">
          <span class="inline-flex items-center gap-1.5">
            <BadgeComponent :variant="row.readOnly ? 'success' : 'warning'">
              {{ row.readOnly ? t('tools.type.readonly') : t('tools.type.write') }}
            </BadgeComponent>
            <BadgeComponent v-if="row.destructive" variant="error">{{ t('tools.destructive') }}</BadgeComponent>
          </span>
        </template>
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction
              :icon="openTool === row.name ? ChevronUp : ChevronDown"
              :label="openTool === row.name ? t('tools.hideSchema') : t('tools.showSchema')"
              :name="row.name"
              :aria-expanded="openTool === row.name"
              :data-testid="`tool-inputs-${row.name}`"
              @click="toggleParams(row.name)"
            />
          </RowActions>
        </template>
        <template #sub-row="{ row }">
          <div :data-testid="`tool-params-${row.name}`">
            <!-- The tool's inputs, drawn by the same table as everything else:
                 a compact page of them inside the row it belongs to. -->
            <DataTable
              v-if="paramsOf(row).length"
              class="tool-params"
              container-class="forebrain-table-wrap"
              :columns="paramColumns"
              :rows="paramsOf(row)"
              :row-key="(param) => param.name"
              :row-attrs="(param) => ({ 'data-tool-param': param.name })"
            >
              <template #cell-param="{ row: param }"><span class="font-mono">{{ param.name }}</span></template>
              <template #cell-paramType="{ row: param }"><span class="font-mono">{{ param.type }}</span></template>
              <template #cell-required="{ row: param }">{{ param.required ? t('common.yes') : t('common.no') }}</template>
              <template #cell-paramDesc="{ row: param }">{{ param.description }}</template>
            </DataTable>
            <span v-else class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('tools.noParams') }}</span>
          </div>
        </template>
      </DataTable>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('tools.note') }}</ListNote>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The tool table the active primary agent's runtime exposes. It is a
 * verification surface: what the model can call right now, under this tenant,
 * with the safety flags the approval policy reads.
 *
 * The table is the server's (plan 073): the search, the category and the type
 * narrow it there, ordered by name, and the pager says how many tools there
 * are. The category dropdown offers every category the runtime publishes
 * whatever the filters leave, so a filter always keeps the way back. The row's
 * one icon action opens the tool's inputs in place, under the row, read from
 * the JSON Schema the runtime publishes.
 */
import { computed, onMounted, ref } from 'vue'
import { ChevronDown, ChevronUp, RotateCw } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, type ListQuery, type ToolMetaRecord } from '@/lib/api'
import { toolParams, type ToolParam } from '@/lib/toolSchema'
import { useI18n } from '@/locales'

const { t } = useI18n()

/** Every category the runtime exposes, whatever the filters leave. */
const categories = ref<string[]>([])

async function fetchTools(query: ListQuery) {
  const answer = await forebrainApi.toolsPage(query)
  categories.value = answer.categories
  return answer
}

const {
  rows,
  total,
  page,
  pageSize,
  q,
  filters,
  loading,
  error,
  reload,
} = useServerList<ToolMetaRecord>(fetchTools, { pageSize: 10, filters: { category: '', type: '' } })

/**
 * The table's columns, at the design's widths: the description takes the room
 * the others leave. Nothing sorts — the list endpoint ignores `sort`.
 */
const columns: DataColumn[] = [
  { id: 'name', header: t('tools.colTool'), size: 280 },
  { id: 'description', header: t('tools.colDesc') },
  { id: 'category', header: t('tools.colCategory'), size: 136 },
  { id: 'readOnly', header: t('tools.colType'), size: 150 },
  { id: 'actions', header: t('tools.colActions'), size: 56, headerClass: 'text-center', cellClass: 'text-right' },
]

/** The inputs table a row opens: the same table, a narrower set of columns. */
const paramColumns: DataColumn[] = [
  { id: 'param', header: t('tools.colParam'), size: 180 },
  { id: 'paramType', header: t('tools.colType'), size: 96 },
  { id: 'required', header: t('tools.colRequired'), size: 60 },
  { id: 'paramDesc', header: t('tools.colDesc') },
]

/** The two filters, read and written through the list's own filters. */
const category = computed({
  get: () => String(filters.value.category ?? ''),
  set: (value: string) => {
    filters.value = { ...filters.value, category: value }
  },
})

const typeFilter = computed({
  get: () => String(filters.value.type ?? ''),
  set: (value: string) => {
    filters.value = { ...filters.value, type: value }
  },
})

/** The tool whose inputs are open; one at a time. */
const openTool = ref('')

function toggleParams(name: string) {
  openTool.value = openTool.value === name ? '' : name
}

function paramsOf(tool: ToolMetaRecord): ToolParam[] {
  return toolParams(tool.inputSchema)
}

// The engine's categories in the viewer's language; a category it adds later
// shows as its own key until a translation lands.
const CATEGORY_LABELS: Record<string, () => string> = {
  filesystem: () => t('tools.categoryFilesystem'),
  shell: () => t('tools.categoryShell'),
  code: () => t('tools.categoryCode'),
  web: () => t('tools.categoryWeb'),
  memory: () => t('tools.categoryMemory'),
  session: () => t('tools.categorySession'),
  permissions: () => t('tools.categoryPermissions'),
  skill: () => t('tools.categorySkill'),
  mcp: () => t('tools.categoryMcp'),
}

function categoryLabel(name: string): string {
  const label = CATEGORY_LABELS[name.toLowerCase()]
  return label ? label() : name
}

function refresh(): void {
  void reload()
}

onMounted(() => {
  void reload()
})
</script>

<style scoped>
/* The open row and its inputs share the alt fill, so the pair reads as one
   block inside the table (plan 073). Both rows are drawn by the shared table,
   so the rules reach into it: the class and the hook are what the page names,
   the elements are the table's own. */
:deep(tr.tool-row--open) > td {
  background: var(--forebrain-bg-alt);
  border-bottom-color: transparent;
}
:deep(tr[data-sub-row]) > td {
  padding: 0 10px 12px;
  background: var(--forebrain-bg-alt);
  white-space: normal;
}
/* The nested inputs table: the page's own fill on the alt row, its own edge,
   and the tighter cell padding a table inside a row wants. */
.tool-params :deep(th) {
  background: var(--forebrain-bg);
  padding: 6px 10px;
}
.tool-params :deep(td) {
  padding: 6px 10px;
  white-space: normal;
  vertical-align: top;
  border-bottom: 0;
}
</style>
