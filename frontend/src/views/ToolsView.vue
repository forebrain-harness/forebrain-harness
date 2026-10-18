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
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table forebrain-table--fixed">
          <colgroup>
            <col style="width: 280px" />
            <col />
            <col style="width: 136px" />
            <col style="width: 150px" />
            <col style="width: 56px" />
          </colgroup>
          <thead>
            <tr>
              <th>{{ t('tools.colTool') }}</th>
              <th>{{ t('tools.colDesc') }}</th>
              <th>{{ t('tools.colCategory') }}</th>
              <th>{{ t('tools.colType') }}</th>
              <th class="text-right">{{ t('tools.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="tool in rows" :key="tool.name">
              <tr :class="{ 'tool-row--open': openTool === tool.name }" :data-tool-row="tool.name">
                <td>
                  <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="tool.name">{{ tool.name }}</span>
                </td>
                <td>
                  <span class="forebrain-cell-1" v-tip:top.trunc="tool.description ?? ''">{{ tool.description }}</span>
                </td>
                <td>{{ categoryLabel(tool.category ?? '') }}</td>
                <td>
                  <span class="inline-flex items-center gap-1.5">
                    <BadgeComponent :variant="tool.readOnly ? 'success' : 'warning'">
                      {{ tool.readOnly ? t('tools.type.readonly') : t('tools.type.write') }}
                    </BadgeComponent>
                    <BadgeComponent v-if="tool.destructive" variant="error">{{ t('tools.destructive') }}</BadgeComponent>
                  </span>
                </td>
                <td class="text-right">
                  <RowActions>
                    <RowAction
                      :icon="openTool === tool.name ? ChevronUp : ChevronDown"
                      :label="openTool === tool.name ? t('tools.hideSchema') : t('tools.showSchema')"
                      :name="tool.name"
                      :aria-expanded="openTool === tool.name"
                      :data-testid="`tool-inputs-${tool.name}`"
                      @click="toggleParams(tool.name)"
                    />
                  </RowActions>
                </td>
              </tr>
              <tr v-if="openTool === tool.name" class="tool-sub" :data-testid="`tool-params-${tool.name}`">
                <td colspan="5">
                  <table v-if="paramsOf(tool).length" class="forebrain-table tool-params">
                    <colgroup>
                      <col style="width: 180px" />
                      <col style="width: 96px" />
                      <col style="width: 60px" />
                      <col />
                    </colgroup>
                    <thead>
                      <tr>
                        <th>{{ t('tools.colParam') }}</th>
                        <th>{{ t('tools.colType') }}</th>
                        <th>{{ t('tools.colRequired') }}</th>
                        <th>{{ t('tools.colDesc') }}</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="param in paramsOf(tool)" :key="param.name" :data-tool-param="param.name">
                        <td class="font-mono">{{ param.name }}</td>
                        <td class="font-mono">{{ param.type }}</td>
                        <td>{{ param.required ? t('common.yes') : t('common.no') }}</td>
                        <td>{{ param.description }}</td>
                      </tr>
                    </tbody>
                  </table>
                  <span v-else class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('tools.noParams') }}</span>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>

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
   block inside the table (plan 073). */
.tool-row--open > td {
  background: var(--forebrain-bg-alt);
  border-bottom-color: transparent;
}
.tool-sub > td {
  padding: 0 10px 12px;
  background: var(--forebrain-bg-alt);
  white-space: normal;
}
/* The nested inputs table: the page's own fill on the alt row, its own edge. */
.tool-params {
  table-layout: fixed;
  background: var(--forebrain-bg);
  border: 1px solid var(--forebrain-divider);
  border-collapse: separate;
  border-spacing: 0;
  border-radius: 6px;
  overflow: hidden;
}
.tool-params th {
  background: var(--forebrain-bg);
  padding: 6px 10px;
}
.tool-params td {
  padding: 6px 10px;
  white-space: normal;
  vertical-align: top;
  border-bottom: 0;
}
</style>
