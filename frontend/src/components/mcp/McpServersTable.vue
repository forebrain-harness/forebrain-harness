<template>
  <div class="flex flex-col gap-4">
    <p
      v-if="error"
      class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]"
      data-testid="mcp-error"
    >
      {{ error }}
    </p>

    <ListToolbar>
      <template #left>
        <input
          v-model="q"
          type="search"
          :placeholder="t('mcp.searchPlaceholder')"
          class="forebrain-field h-[34px]"
          style="max-width: 240px"
          data-testid="mcp-search"
        />
        <!-- The layer filter exists only where more than one layer is listed,
             and its options are exactly the layers this page shows, highest
             first — the order the rows arrive in. -->
        <select
          v-if="multiLayer"
          v-model="sourceFilter"
          class="forebrain-field h-[34px]"
          style="width: 110px"
          :aria-label="t('mcp.colSource')"
          data-testid="mcp-source-filter"
        >
          <option value="">{{ t('mcp.sourceAll') }}</option>
          <option v-for="layer in layers" :key="layer" :value="layer">{{ layerName(layer) }}</option>
        </select>
      </template>
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" data-testid="mcp-add" @click="openNew">
          <Plus class="size-3.5" aria-hidden="true" />{{ t('mcp.addServer') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" data-testid="mcp-refresh" @click="refresh">
          <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
        </button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div
      v-else-if="!rows.length"
      class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]"
      data-testid="mcp-empty"
    >
      {{ t('mcp.noRows') }}
    </div>
    <div v-else class="forebrain-table-wrap">
      <table class="forebrain-table forebrain-table--fixed mcp-table">
        <colgroup>
          <col style="width: 168px" />
          <col v-if="multiLayer" style="width: 84px" />
          <col />
          <col style="width: 108px" />
          <col style="width: 148px" />
          <col style="width: 132px" />
        </colgroup>
        <thead>
          <tr>
            <th>{{ t('mcp.colServer') }}</th>
            <th v-if="multiLayer">{{ t('mcp.colSource') }}</th>
            <th>{{ t('mcp.colTarget') }}</th>
            <th>{{ t('mcp.colType') }}</th>
            <th>{{ t('mcp.colStatus') }}</th>
            <th class="text-center">{{ t('mcp.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <!-- One row per entry, keyed by its identity — the layer and the
               name — never by its place in the page: re-sorted or re-paged, a
               click still lands on the entry it was drawn for. -->
          <tr v-for="entry in tableRows" :key="entry.key" :data-mcp-row="entry.key">
            <td>
              <div class="mcp-name">
                <span class="forebrain-cell-1 min-w-0 font-mono" v-tip:top.trunc="entry.row.name">{{ entry.row.name }}</span>
                <BadgeComponent v-if="entry.row.required" variant="gray" class="shrink-0">{{ t('mcp.required') }}</BadgeComponent>
              </div>
            </td>
            <td v-if="multiLayer">
              <ScopeBadge :type="entry.row.scope" :label="layerName(entry.row.scope)" />
            </td>
            <td>
              <span class="forebrain-cell-1 font-mono text-[var(--forebrain-text-2)]" v-tip:top.trunc="entry.row.target">{{ entry.row.target }}</span>
            </td>
            <td>
              <span class="mcp-ty font-mono">{{ entry.type }}</span>
            </td>
            <!-- One badge, and the words behind it — a failure in its own
                 text, why an entry is off, which layer overrides it, or how a
                 declined one is taken back — one hover away. -->
            <td>
              <BadgeComponent :variant="statusVariant(entry.status.tone)" v-tip:top="entry.status.tip">{{ entry.status.label }}</BadgeComponent>
            </td>
            <td class="text-right">
              <RowActions>
                <!-- The switch that writes the next session's list opens the
                     action column, where the prototype puts it (`mcpSwCell`):
                     a row awaiting a decision has nothing to switch yet — the
                     buttons after it are the decision — and a declined row has
                     no switch either, it does not load until 允许 takes the
                     decision back. -->
                <SwitchComponent
                  v-if="entry.own && !entry.pending && !entry.declined"
                  :model-value="enabledNow(entry.row)"
                  :disabled="entry.row.disabledByConfig === true"
                  :aria-label="t('mcp.enableAria', { name: entry.row.name })"
                  v-tip:top="entry.row.disabledByConfig ? t('mcp.enabledByConfigTip') : ''"
                  :data-testid="`mcp-enable-${entry.key}`"
                  @update:model-value="toggleEnabled(entry.row, $event)"
                />
                <!-- A row with no switch keeps its width, so the icons line up
                     down the column: a row this page does not own is not its to
                     switch. -->
                <RowActionSlot v-else class="mcp-sw-slot" />
                <template v-if="entry.pending">
                  <RowAction
                    :icon="Check"
                    :label="t('projects.mcpAllow')"
                    :name="entry.row.name"
                    tone="ok"
                    :disabled="busy === entry.key"
                    :data-testid="`mcp-allow-${entry.key}`"
                    @click="decide(entry.row, true)"
                  />
                  <RowAction
                    :icon="X"
                    :label="t('projects.mcpDecline')"
                    :name="entry.row.name"
                    tone="danger"
                    :disabled="busy === entry.key"
                    :data-testid="`mcp-decline-${entry.key}`"
                    @click="decide(entry.row, false)"
                  />
                </template>
                <template v-else-if="entry.declined">
                  <RowAction
                    :icon="Check"
                    :label="t('projects.mcpAllow')"
                    :name="entry.row.name"
                    tone="ok"
                    :disabled="busy === entry.key"
                    :data-testid="`mcp-undecline-${entry.key}`"
                    @click="decide(entry.row, true)"
                  />
                  <RowActionSlot />
                </template>
                <template v-else-if="entry.own">
                  <RowAction
                    :icon="Pencil"
                    :label="t('common.edit')"
                    :name="entry.row.name"
                    :data-testid="`mcp-edit-${entry.key}`"
                    @click="openRow('edit', entry.row)"
                  />
                  <RowAction
                    :icon="Trash2"
                    :label="t('common.delete')"
                    :name="entry.row.name"
                    tone="danger"
                    :data-testid="`mcp-delete-${entry.key}`"
                    @click="askDelete(entry)"
                  />
                </template>
                <template v-else>
                  <RowAction
                    :icon="Eye"
                    :label="t('common.view')"
                    :name="entry.row.name"
                    :data-testid="`mcp-view-${entry.key}`"
                    @click="openRow('view', entry.row)"
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
    <p v-if="notApplied.length" class="forebrain-list-note" data-testid="mcp-not-applied">
      {{ t('mcp.notApplied', { items: notAppliedText }) }}
    </p>
    <ListNote data-testid="mcp-note">{{ noteText }}</ListNote>

    <McpServerDrawer
      v-if="drawer"
      :mode="drawer.mode"
      :scope="drawer.scope"
      :project-id="projectId"
      :agent-id="agentId"
      :row="drawer.row"
      @close="drawer = null"
      @saved="onSaved"
    />

    <ConfirmDialog
      v-if="deleteTarget"
      :open="true"
      :title="t('mcp.deleteTitle', { name: deleteTarget.row.name })"
      :message="t('mcp.deleteMessage')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The MCP table (plan 074), drawn once for all three pages: 设置 · MCP lists the
 * shared layer, the menu bar's MCP page lists the primary agent's layer on top
 * of it, and a project's MCP tab lists all three. `view` is the whole
 * difference — which layers are listed, which one this page may write, and what
 * the note under the table says.
 *
 * The rules the prototype sets (docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/
 * app-preview.html: `mcpBlock`, `mcpStatusCell`, `mcpEnableCell`, `mcpActions`):
 *
 *   - the layer filter exists only on a page with more than one layer, and its
 *     options are exactly that page's layers, highest first;
 *   - the status column carries one badge; the failure's own text, the reason
 *     an entry is off, and the OAuth state ride its hover tip (`mcpStatus`);
 *   - the enable switch is the next session's switch, opens the action column,
 *     is disabled for an entry the configuration has turned off, and is absent —
 *     its width kept by an empty slot — for a row awaiting a decision, one this
 *     project declined, and a row this page does not own;
 *   - a row awaiting a decision offers 允许 / 拒绝 first; a declined one offers
 *     允许 alone, which takes the decision back; this page's own rows offer
 *     编辑 / 删除; every other layer's rows offer 查看 only, opening the
 *     read-only drawer that says where the entry is edited.
 *
 * Every row is keyed by its identity (layer + name), so a reorder or a page
 * change can never leave a click pointing at the row that used to be there.
 */
import { computed, onMounted, ref } from 'vue'
import { Check, Eye, Pencil, Plus, RotateCw, Trash2, X } from 'lucide-vue-next'
import type { AxiosError } from 'axios'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import TablePager from '@/components/common/TablePager.vue'
import { lastSessionIdValue } from '@/composables/useLastSession'
import { useServerList } from '@/composables/useServerList'
import {
  forebrainApi,
  getErrorMessage,
  mcpSetServerDisabled,
  type McpLayer,
  type McpNotAppliedRow,
  type McpServerRow,
} from '@/lib/api'
import { useI18n } from '@/locales'

import McpServerDrawer from './McpServerDrawer.vue'
import { mcpStatus, mcpTypeLabel, type McpStatus, type McpTone, type McpTransportName } from './mcpRows'

/** The three pages this one table draws, named by the layer each may write. */
type McpView = 'global' | 'agent' | 'project'

const props = defineProps<{
  /** Which page the table is: the layers it lists, and the one it may write. */
  view: McpView
  /** The project's id, for the project view's rows and writes. */
  projectId?: string
  /** The active primary agent's id, which the agent layer's writes carry. */
  agentId: string
}>()

const { t } = useI18n()

/** The layers each page lists, highest first — the rows arrive in this order,
 * and the filter offers exactly these. */
const VIEW_LAYERS: Record<McpView, McpLayer[]> = {
  global: ['global'],
  agent: ['agent', 'global'],
  project: ['project', 'agent', 'global'],
}

const layers = computed(() => VIEW_LAYERS[props.view])
const multiLayer = computed(() => layers.value.length > 1)

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
} = useServerList<McpServerRow>(
  (query) =>
    forebrainApi.mcpPage(props.view, { ...query, projectId: props.projectId }).then((answer) => {
      // A project file's entries that did not apply are the preview's own
      // fact, reported beside the page rather than as a row.
      notApplied.value = answer.notApplied ?? []
      return answer
    }),
  { pageSize: 10, filters: { source: '' } },
)

/** Entries of a project's MCP file that did not apply, with the reason the
 * preview gave: the old tab listed them, and a file that silently loads
 * nothing is exactly what a reader needs to see. */
const notApplied = ref<McpNotAppliedRow[]>([])
const notAppliedText = computed(() => notApplied.value.map((row) => `${row.name} — ${row.reason}`).join('；'))

const writeError = ref('')
const error = computed(() => listError.value || writeError.value)

/** The layer filter, read and written through the list's own filters, so a new
 * source goes back to page 1 and reloads by itself. */
const sourceFilter = computed({
  get: () => String(filters.value.source ?? ''),
  set: (value: string) => {
    filters.value = { ...filters.value, source: value }
  },
})

/** One row as the template draws it: the entry with the two derived facts every
 * cell needs, computed once per read instead of once per cell. */
interface TableRow {
  /** The row's identity — its layer and its name — used as the DOM key and in
   * the test hooks, so a click always names the entry it was drawn for. */
  key: string
  row: McpServerRow
  status: McpStatus
  type: McpTransportName
  /** This page owns the row's layer: 编辑 / 删除 and the enable switch. */
  own: boolean
  /** A project entry nobody has decided on yet: 允许 / 拒绝, no switch. */
  pending: boolean
  /** An entry this project declined: 允许 takes the decision back, and it has
   * no switch — a declined entry does not load. */
  declined: boolean
}

const tableRows = computed<TableRow[]>(() =>
  rows.value.map((row) => {
    const status = mcpStatus(row)
    return {
      key: rowKey(row),
      row,
      status,
      type: mcpTypeLabel(row),
      own: row.scope === props.view,
      pending: consentPending(row),
      declined: consentDeclined(row),
    }
  }),
)

function rowKey(row: McpServerRow): string {
  return `${row.scope}:${row.name}`
}

/** A project entry with no recorded decision: the one state whose action set is
 * the decision itself. The rule is `mcpStatus`'s first branch, spelled out here
 * because the cell needs the fact as well as the badge. */
function consentPending(row: McpServerRow): boolean {
  return row.pending === true && row.scope === 'project' && row.allowed !== true
}

/** A project entry this project declined — `mcpStatus`'s second branch. Only
 * the project view carries the decision; the flag elsewhere means nothing. */
function consentDeclined(row: McpServerRow): boolean {
  return row.declined === true && row.scope === 'project'
}

/** The switch's state: what the next session will load, not what this session
 * is running — the disable store is what the switch writes. */
function enabledNow(row: McpServerRow): boolean {
  return row.disabledNextSession !== true && row.disabledByConfig !== true
}

function layerName(layer: McpLayer): string {
  if (layer === 'project') return t('mcp.layerProject')
  if (layer === 'agent') return t('mcp.layerAgent')
  return t('mcp.layerGlobal')
}

function statusVariant(tone: McpTone): 'success' | 'warning' | 'error' | 'gray' {
  if (tone === 'ok') return 'success'
  if (tone === 'warn') return 'warning'
  if (tone === 'err') return 'error'
  return 'gray'
}

/** The note under the table: what this page lists, and who owns each layer's
 * entries. Verbatim from the design, one per view. */
const noteText = computed(() => {
  if (props.view === 'project') return t('mcp.noteProject')
  if (props.view === 'agent') return t('mcp.noteAgent')
  return t('mcp.noteGlobal')
})

/** The id a layer's write routes carry: none for the shared layer. */
function ownerIdOf(scope: McpLayer): string {
  if (scope === 'agent') return props.agentId
  if (scope === 'project') return props.projectId ?? ''
  return ''
}

function refresh(): void {
  writeError.value = ''
  void reload()
}

/** The list is the truth after a write, so every write reloads it. */
async function load(): Promise<void> {
  writeError.value = ''
  await reload()
}

/** The row whose decision is in flight, so its two buttons cannot double-send. */
const busy = ref('')

async function toggleEnabled(row: McpServerRow, next: boolean): Promise<void> {
  writeError.value = ''
  try {
    await mcpSetServerDisabled(row.name, !next, lastSessionIdValue() ?? undefined)
    await load()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  }
}

/** 允许 / 拒绝 on a project entry. The request names the one entry this click is
 * about (`decide`) together with the fingerprint the row was rendered from, and
 * the endpoint changes that entry's decision and nothing else — which is also
 * how a declined entry is allowed again. A 409 means the file moved under the
 * page: nothing was recorded, the server text stays up, and the rows reload so
 * the entry reappears with its new summary and fingerprint. */
async function decide(row: McpServerRow, allow: boolean): Promise<void> {
  const projectId = props.projectId ?? ''
  if (!projectId || busy.value !== '') return
  busy.value = rowKey(row)
  writeError.value = ''
  try {
    await forebrainApi.projectMcpConsent(projectId, allow ? [row.name] : [], [row.name], [
      { name: row.name, fingerprint: row.fingerprint ?? '' },
    ])
    await load()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
    if ((cause as AxiosError)?.response?.status === 409) await reload()
  } finally {
    busy.value = ''
  }
}

const deleteTarget = ref<TableRow | null>(null)
const deleting = ref(false)

function askDelete(entry: TableRow): void {
  deleteTarget.value = entry
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target || deleting.value) return
  deleting.value = true
  writeError.value = ''
  try {
    await forebrainApi.mcpDelete(target.row.scope, ownerIdOf(target.row.scope), target.row.name)
    deleteTarget.value = null
    await load()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    deleting.value = false
  }
}

/** The drawer, when one is open: 添加 from the toolbar, 编辑 / 查看 from a row. */
const drawer = ref<{ mode: 'new' | 'edit' | 'view'; scope: McpLayer; row?: McpServerRow } | null>(null)

function openNew(): void {
  drawer.value = { mode: 'new', scope: props.view }
}

function openRow(mode: 'edit' | 'view', row: McpServerRow): void {
  drawer.value = { mode, scope: row.scope, row }
}

async function onSaved(): Promise<void> {
  drawer.value = null
  await load()
}

// The list loads itself from here on — a search, a filter or a page change
// asks the server; a mount is the one read the page makes for it.
onMounted(() => {
  void load()
})
</script>

<style scoped>
/* The prototype's own measurements for this table (`.tb.mcp-tb`): one line per
   cell, a slightly taller row than the shared table's, and a name cell whose
   text truncates beside its 必需 badge. */
.mcp-table {
  table-layout: fixed;
}
.mcp-table tbody td {
  padding-top: 10px;
  padding-bottom: 10px;
  vertical-align: middle;
  white-space: nowrap;
}
.mcp-name {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
/* The type column says only the transport, in its short form. */
.mcp-ty {
  display: inline-flex;
  align-items: baseline;
}
/* A row with no switch to offer keeps the switch's 32px, so the icons stay
   aligned down the column. */
.mcp-sw-slot {
  width: 32px;
}
</style>
