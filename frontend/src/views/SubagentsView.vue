<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">
        {{ error }}
      </p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('subagents.search')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="subagents-search"
          />
          <select
            v-model="kindFilter"
            class="forebrain-field h-[34px]"
            style="width: 120px"
            :aria-label="t('subagents.colKind')"
            data-testid="subagents-kind-filter"
          >
            <option value="">{{ t('subagents.filterAll') }}</option>
            <option value="builtin">{{ t('subagents.kind.builtin') }}</option>
            <option value="custom">{{ t('subagents.kind.custom') }}</option>
            <option value="system">{{ t('subagents.kind.system') }}</option>
          </select>
        </template>
        <template #actions>
          <!-- This agent's own switch, left of 新建子代理: it follows the global
               default (设置 → 子代理) until it is switched here, and switching it
               switches every row under it. -->
          <span class="sa-tsw">
            <SwitchComponent
              :model-value="agentSwitch"
              :disabled="saving"
              :aria-label="t('subagents.switchLabel')"
              v-tip:top="switchHint"
              data-testid="subagents-switch"
              @update:model-value="toggleAgentSwitch"
            />
            <b>{{ t('subagents.switchLabel') }}</b>
          </span>
          <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" data-testid="subagents-new" @click="openNew">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('subagents.new') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div
        v-else-if="!rows.length"
        class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]"
        data-testid="subagents-empty"
      >
        {{ t('common.empty') }}
      </div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table forebrain-table--fixed">
          <colgroup>
            <col />
            <col style="width: 76px" />
            <col style="width: 220px" />
            <col style="width: 92px" />
            <col style="width: 128px" />
          </colgroup>
          <thead>
            <tr>
              <th>{{ t('subagents.colName') }}</th>
              <th>{{ t('subagents.colKind') }}</th>
              <th>{{ t('subagents.colModel') }}</th>
              <th>{{ t('subagents.colTools') }}</th>
              <th class="text-center">{{ t('subagents.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.name" :data-subagent-row="row.name">
              <td>
                <div class="sa-nm">
                  <b class="forebrain-cell-1 min-w-0 font-mono">{{ row.name }}</b>
                  <BadgeComponent
                    v-if="row.promptOverridden"
                    variant="warning"
                    class="shrink-0"
                    v-tip:top="t('subagents.promptOverriddenHint')"
                    :data-testid="`subagent-overridden-${row.name}`"
                  >
                    {{ t('subagents.promptOverridden') }}
                  </BadgeComponent>
                </div>
                <div class="sa-ds text-[var(--forebrain-muted-text)]" v-tip:top.trunc="descriptionOf(row)">{{ descriptionOf(row) }}</div>
              </td>
              <td>
                <BadgeComponent :variant="kindVariant(row.kind)">{{ kindLabel(row.kind) }}</BadgeComponent>
              </td>
              <td>
                <!-- A system subagent's model is the system's to choose when it
                     calls it, so the page says what happens rather than a value. -->
                <span v-if="row.kind === 'system'" class="text-[var(--forebrain-muted-text)]">{{ systemModel(row) }}</span>
                <template v-else-if="row.model">
                  <span
                    class="font-mono"
                    v-tip:top="row.modelSource === 'global' ? t('subagents.modelFromGlobal', { name: row.name }) : ''"
                  >{{ providerModel(row) }}</span>
                  <span v-if="row.reasoningEffort" class="text-[var(--forebrain-muted-text)]"> · {{ effortLevelLabel(row.reasoningEffort) }}</span>
                </template>
                <span v-else class="text-[var(--forebrain-muted-text)]">{{ t('subagents.followPrimary') }}</span>
              </td>
              <td>{{ toolsLabel(row) }}</td>
              <td class="text-right">
                <RowActions>
                  <!-- The row switch comes first: switching one may move this
                       agent's own switch in the toolbar, which is why every
                       write reloads the list instead of predicting the answer. -->
                  <span v-if="row.kind !== 'system'" class="sa-sw">
                    <SwitchComponent
                      :model-value="row.enabled === true"
                      :disabled="saving"
                      :aria-label="t('subagents.rowSwitch', { name: row.name })"
                      v-tip:top="row.enabled === true ? t('subagents.rowOff') : t('subagents.rowOn')"
                      :data-testid="`subagent-switch-${row.name}`"
                      @update:model-value="toggleRow(row, $event)"
                    />
                  </span>
                  <span v-else class="sa-sw sa-sw-slot" aria-hidden="true" />
                  <template v-if="row.kind === 'custom'">
                    <RowAction
                      :icon="Pencil"
                      :label="t('common.edit')"
                      :name="row.name"
                      :disabled="saving"
                      :data-testid="`subagent-edit-${row.name}`"
                      @click="openEdit(row)"
                    />
                    <RowAction
                      :icon="Trash2"
                      :label="t('common.delete')"
                      :name="row.name"
                      tone="danger"
                      :disabled="saving"
                      :data-testid="`subagent-delete-${row.name}`"
                      @click="askDelete(row)"
                    />
                  </template>
                  <template v-else-if="row.kind === 'builtin'">
                    <RowAction
                      :icon="Pencil"
                      :label="t('common.edit')"
                      :name="row.name"
                      :disabled="saving"
                      :data-testid="`subagent-edit-${row.name}`"
                      @click="openEdit(row)"
                    />
                    <RowActionSlot />
                  </template>
                  <template v-else>
                    <RowAction
                      :icon="Eye"
                      :label="t('common.view')"
                      :name="row.name"
                      :data-testid="`subagent-view-${row.name}`"
                      @click="openView(row)"
                    />
                    <RowActionSlot />
                  </template>
                </RowActions>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('subagents.note') }}</ListNote>
    </div>

    <SubagentConfigDrawer
      v-if="drawer"
      :open="true"
      :agent-id="activeId"
      :name="drawer.name"
      :mode="drawer.mode"
      @close="drawer = null"
      @saved="onSaved"
    />

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('subagents.deleteTitle', { name: deleteTarget?.name ?? '' })"
      :message="t('subagents.deleteBody')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The primary agent's subagents, as this agent configures them (plan 079): the
 * harness's own — the four the model may dispatch and the six the system calls
 * on its own occasions — plus the ones this agent added. The page is the list;
 * creating, editing and viewing happen in the drawer, and a delete asks first.
 *
 * The table is the server's (plan 073): the search and the kind narrow it there,
 * the agent's own rows first and the newest of them at the top. The toolbar's
 * 子代理功能 switch is this agent's own — it follows the global default
 * (设置 → 子代理) until it is switched here — and switching it switches every row
 * under it, while a row switched on turns the toolbar's switch on and the last
 * row switched off turns it off. That linkage is the server's to decide, so
 * every write reloads the list and reads the answer's own `switch` rather than
 * predicting it.
 */
import { computed, ref, watch } from 'vue'
import { Eye, Pencil, Plus, RotateCw, Trash2 } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import TablePager from '@/components/common/TablePager.vue'
import SubagentConfigDrawer from '@/components/subagents/SubagentConfigDrawer.vue'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type ListQuery, type SubagentConfigRow, type SubagentKind } from '@/lib/api'
import { effortLevelLabel } from '@/lib/effortLabel'
import { useI18n, type I18nKey } from '@/locales'

const { t } = useI18n()

// The tenant the shell is on: a primary agent is a tenant, and its subagents are
// keyed by its id, so a switch re-reads the page under the new one.
const { activeId } = usePrimaryAgents()

/** This agent's own subagent switch, as the last answer from the server said. */
const agentSwitch = ref(false)

/** One read of the page, with the switch the toolbar shows beside it. */
async function fetchSubagents(query: ListQuery) {
  const answer = await forebrainApi.subagentConfigPage(activeId.value, query)
  agentSwitch.value = answer.switch
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
  error: listError,
  reload,
} = useServerList<SubagentConfigRow>(fetchSubagents, { pageSize: 10, filters: { kind: '' } })

/** The kind filter, read and written through the list's own filters, so a new
 * kind goes back to page 1 and reloads by itself. */
const kindFilter = computed({
  get: () => String(filters.value.kind ?? ''),
  set: (value: string) => {
    filters.value = { ...filters.value, kind: value }
  },
})

/** A failed write, beside whatever the list's own read said. */
const writeError = ref('')
const error = computed(() => listError.value || writeError.value)

/** A switch write in flight: the toolbar's and every row's wait for it, since
 * the server's answer decides both. */
const saving = ref(false)

const switchHint = computed(() => (agentSwitch.value ? t('subagents.switchOffHint') : t('subagents.switchOnHint')))

function refresh(): void {
  writeError.value = ''
  void reload()
}

/**
 * The page lists one primary agent's subagents, so it reads once that agent is
 * known — the shell reads its agents when it is shown, a moment after this page
 * mounts on a direct visit — and again whenever the tenant changes.
 */
watch(
  activeId,
  (id) => {
    if (id) void reload()
  },
  { immediate: true },
)

function kindLabel(kind: SubagentKind): string {
  if (kind === 'custom') return t('subagents.kind.custom')
  if (kind === 'system') return t('subagents.kind.system')
  return t('subagents.kind.builtin')
}

function kindVariant(kind: SubagentKind): 'default' | 'success' | 'gray' {
  if (kind === 'custom') return 'default'
  if (kind === 'system') return 'gray'
  return 'success'
}

/**
 * A row's description: a custom subagent's own text, and for the harness's own
 * the page's words with the definition's English behind them.
 */
function descriptionOf(row: SubagentConfigRow): string {
  if (row.kind === 'custom') return row.description
  const key = `subagents.desc.${row.name}`
  const text = t(key as I18nKey)
  return text === key ? row.description : text
}

/** `provider/model`, the way the model is stored and written everywhere else. */
function providerModel(row: SubagentConfigRow): string {
  return [row.modelProvider, row.model].filter(Boolean).join('/')
}

/** What a system subagent runs on: the page's words for it, or 跟随主代理. */
function systemModel(row: SubagentConfigRow): string {
  const key = `subagents.modelBySystem.${row.name}`
  const text = t(key as I18nKey)
  return text === key ? t('subagents.followPrimary') : text
}

function toolsLabel(row: SubagentConfigRow): string {
  switch (row.tools) {
    case 'edit':
      return t('subagents.toolsEdit')
    case 'pick':
      return t('subagents.toolsPick', { n: row.allowedTools.length })
    case 'none':
      return t('subagents.toolsNone')
    default:
      return t('subagents.toolsReadOnly')
  }
}

async function toggleAgentSwitch(next: boolean): Promise<void> {
  if (!activeId.value || saving.value) return
  saving.value = true
  writeError.value = ''
  try {
    await forebrainApi.setAgentSubagentSwitch(activeId.value, next)
    await reload()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

async function toggleRow(row: SubagentConfigRow, next: boolean): Promise<void> {
  if (!activeId.value || saving.value) return
  saving.value = true
  writeError.value = ''
  try {
    await forebrainApi.setSubagentEnabled(activeId.value, row.name, next)
    await reload()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

/** The drawer, when one is open: 新建, 编辑 or 查看. */
const drawer = ref<{ mode: 'create' | 'edit' | 'view'; name?: string } | null>(null)

function openNew(): void {
  drawer.value = { mode: 'create' }
}

function openEdit(row: SubagentConfigRow): void {
  drawer.value = { mode: 'edit', name: row.name }
}

function openView(row: SubagentConfigRow): void {
  drawer.value = { mode: 'view', name: row.name }
}

async function onSaved(): Promise<void> {
  drawer.value = null
  await reload()
}

const deleteTarget = ref<SubagentConfigRow | null>(null)
const deleting = ref(false)

function askDelete(row: SubagentConfigRow): void {
  deleteTarget.value = row
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || !activeId.value || deleting.value) return
  deleting.value = true
  writeError.value = ''
  try {
    await forebrainApi.deleteSubagentConfig(activeId.value, target.name)
    deleteTarget.value = null
    await reload()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    deleting.value = false
  }
}
</script>

<style scoped>
/* The prototype's own measurements for this page (`.sa-tsw`, `.sa-nm`, `.sa-ds`,
   and the action column's 32px switch). */
.sa-tsw {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  margin-right: 6px;
  font-size: 13px;
  white-space: nowrap;
}
.sa-tsw b {
  font-weight: 500;
}
.sa-nm {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
/* The description is a second line under the name, cut at the column's edge. */
.sa-ds {
  margin-top: 1px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* The switch keeps its 32px in every row, so the icons after it line up with the
   rows that have no switch at all. */
.sa-sw {
  display: inline-flex;
  align-items: center;
  width: 32px;
  flex: none;
  margin-right: 4px;
}
.sa-sw-slot {
  height: 18px;
}
</style>
