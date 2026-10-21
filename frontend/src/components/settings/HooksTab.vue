<template>
  <div class="flex flex-col gap-4 pb-6">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]" data-testid="hooks-error">{{ error }}</p>
    <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

    <ListToolbar>
      <template #left>
        <input
          v-model="q"
          type="search"
          :placeholder="t('hooks.searchPlaceholder')"
          class="forebrain-field h-[34px]"
          style="max-width: 240px"
          data-testid="hooks-search"
        />
      </template>
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" :disabled="saving" data-testid="hooks-add" @click="openAdd">
          <Plus class="size-3.5" aria-hidden="true" />{{ t('hooks.addTitle') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" data-testid="hooks-refresh" @click="reload">
          <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
        </button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div
      v-else-if="!rows.length"
      class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]"
      data-testid="hooks-empty"
    >
      {{ t('hooks.empty') }}
    </div>
    <DataTable
      v-else
      :columns="columns"
      :rows="rows"
      :row-key="(row) => `${row.event}-${row.matcherIdx}-${row.commandIdx}`"
      :row-attrs="() => ({ 'data-testid': 'hook-row' })"
    >
      <template #cell-event="{ row }"><span class="font-mono">{{ row.event }}</span></template>
      <!-- A hook that matches every tool says so in words; a matcher is
           a tool name and reads as one. -->
      <template #cell-matcher="{ row }">
        <span :class="row.matcher ? 'font-mono' : ''">{{ row.matcher || t('hooks.matcherAllTools') }}</span>
      </template>
      <template #cell-command="{ row }">
        <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="row.command">{{ row.command }}</span>
      </template>
      <template #cell-actions="{ row }">
        <RowActions>
          <RowAction
            :icon="Pencil"
            :label="t('hooks.edit')"
            :name="row.event"
            :disabled="saving"
            data-testid="hook-edit"
            @click="openEdit(row)"
          />
          <RowAction
            :icon="Trash2"
            :label="t('common.delete')"
            :name="row.event"
            tone="danger"
            :disabled="saving"
            data-testid="hook-remove"
            @click="removeRow = row"
          />
        </RowActions>
      </template>
    </DataTable>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    <ListNote data-testid="hooks-note">{{ t('hooks.description') }}</ListNote>

    <ForebrainModal :open="modalOpen" size="md" :title="editing ? t('hooks.editModalTitle') : t('hooks.addModalTitle')" @close="modalOpen = false">
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('hooks.eventLabel') }}</span>
          <select v-model="form.event" class="forebrain-field h-9 text-[13px]" data-testid="hook-event">
            <option v-for="name in events" :key="name" :value="name">{{ name }}</option>
          </select>
        </label>
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('hooks.typeLabel') }}</span>
          <select v-model="form.type" class="forebrain-field h-9 text-[13px]" data-testid="hook-type">
            <option v-for="kind in knownTypes" :key="kind" :value="kind">{{ kind }}</option>
          </select>
        </label>
      </div>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('hooks.matcherLabel') }}</span>
        <select v-model="form.matcher" class="forebrain-field h-9 font-mono text-[12px]" data-testid="hook-matcher">
          <option value="">{{ t('hooks.anyTool') }}</option>
          <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
        </select>
      </label>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('hooks.commandLabel') }}</span>
        <input v-model="form.command" class="forebrain-field h-9 w-full font-mono text-[12px]" :placeholder="t('hooks.commandPlaceholder')" data-testid="hook-command" />
      </label>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalOpen = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!form.event || !form.command.trim() || saving" data-testid="hook-save" @click="saveModal">
          {{ saving ? t('common.loading') : editing ? t('common.save') : t('common.add') }}
        </button>
      </template>
    </ForebrainModal>

    <ConfirmDialog
      :open="!!removeRow"
      :title="t('hooks.deleteTitle')"
      :message="t('hooks.deleteMessage')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="removeRow = null"
      @confirm="confirmRemove"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * Hooks run commands at points in a turn, as a table with its buttons above it;
 * adding and editing happen in a modal and write the whole table back. The
 * events and kinds offered here are the ones the runtime accepts — the lists
 * come from the server rather than being written down twice — so a hook that
 * is offered is a hook that will load.
 *
 * The list is the server's (plan 076): it answers one row per command, searched
 * over the event, the matcher and the command. The store is still one map, so
 * an edit or a delete reads the whole map back first, changes its one entry by
 * the two indices the row carries, and writes the map.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { Pencil, Plus, RotateCw, Trash2 } from 'lucide-vue-next'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import {
  getErrorMessage,
  forebrainApi,
  type HookCommandRecord,
  type HookRowRecord,
  type HooksSettingsRecord,
  type ListQuery,
} from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

const events = ref<string[]>([])
const knownTypes = ref<string[]>([])
const tools = ref<string[]>([])
const saving = ref(false)
const notice = ref('')
const actionError = ref('')

async function fetchHooks(query: ListQuery) {
  const answer = await forebrainApi.hooksPage(query)
  events.value = answer.events
  knownTypes.value = answer.knownTypes
  return answer
}

const {
  rows,
  total,
  page,
  pageSize,
  q,
  loading,
  error: listError,
  reload,
} = useServerList<HookRowRecord>(fetchHooks, { pageSize: 10 })

/**
 * The table's columns, at the design's widths: the command takes the room the
 * others leave. Nothing sorts — the list endpoint ignores `sort`.
 */
const columns: DataColumn[] = [
  { id: 'event', header: t('hooks.eventLabel'), size: 160 },
  { id: 'matcher', header: t('hooks.matcherLabel'), size: 160 },
  { id: 'command', header: t('hooks.commandLabel') },
  { id: 'actions', header: t('hooks.colActions'), size: 88, headerClass: 'text-center', cellClass: 'text-right' },
]

const error = computed(() => listError.value || actionError.value)

const modalOpen = ref(false)
const editing = ref<HookRowRecord | null>(null)
const removeRow = ref<HookRowRecord | null>(null)
const form = reactive({ event: '', type: 'command', matcher: '', command: '' })

/**
 * wholeMap reads the store as it is — the map an edit or a delete changes and
 * writes back. It also refreshes the two lists the modal offers.
 */
async function wholeMap(): Promise<HooksSettingsRecord> {
  const res = await forebrainApi.hooks()
  if (res.events?.length) events.value = res.events
  if (res.knownTypes?.length) knownTypes.value = res.knownTypes
  return res.hooks ?? {}
}

/** commandAt finds the one command a row stands for, inside the whole map. */
function commandAt(map: HooksSettingsRecord, row: HookRowRecord): HookCommandRecord | undefined {
  return map[row.event]?.[row.matcherIdx]?.hooks?.[row.commandIdx]
}

function removeCommand(map: HooksSettingsRecord, event: string, matcherIdx: number, commandIdx: number): HooksSettingsRecord {
  const list = [...(map[event] ?? [])]
  const matcher = { ...list[matcherIdx] }
  matcher.hooks = (matcher.hooks ?? []).filter((_, i) => i !== commandIdx)
  if (matcher.hooks.length === 0) list.splice(matcherIdx, 1)
  else list[matcherIdx] = matcher
  const next = { ...map }
  if (list.length === 0) delete next[event]
  else next[event] = list
  return next
}

function addCommand(map: HooksSettingsRecord, event: string, matcher: string, type: string, command: string): HooksSettingsRecord {
  const list = [...(map[event] ?? [])]
  const idx = list.findIndex((m) => (m.matcher ?? '') === matcher)
  if (idx >= 0) {
    list[idx] = { ...list[idx], hooks: [...(list[idx].hooks ?? []), { type, command }] }
  } else {
    list.push({ matcher: matcher || undefined, hooks: [{ type, command }] })
  }
  return { ...map, [event]: list }
}

async function loadTools() {
  try {
    const res = await forebrainApi.toolsList()
    tools.value = res.map((tool) => tool.name).filter(Boolean)
  } catch {
    tools.value = []
  }
}

function openAdd() {
  editing.value = null
  Object.assign(form, { event: events.value[0] ?? '', type: knownTypes.value[0] ?? 'command', matcher: '', command: '' })
  modalOpen.value = true
}

/** Editing opens on the whole map, so the row's own kind comes with it. */
async function openEdit(row: HookRowRecord) {
  actionError.value = ''
  try {
    const cmd = commandAt(await wholeMap(), row)
    editing.value = row
    Object.assign(form, {
      event: row.event,
      type: cmd?.type ?? 'command',
      matcher: row.matcher,
      command: row.command,
    })
    modalOpen.value = true
  } catch (e) {
    actionError.value = getErrorMessage(e)
  }
}

async function saveModal() {
  const { event, type, matcher, command } = form
  if (!event || !command.trim()) return
  saving.value = true
  actionError.value = ''
  notice.value = ''
  try {
    let map = await wholeMap()
    if (editing.value) map = removeCommand(map, editing.value.event, editing.value.matcherIdx, editing.value.commandIdx)
    map = addCommand(map, event, matcher, type, command.trim())
    modalOpen.value = false
    await forebrainApi.saveHooks(map)
    notice.value = t('hooks.saved')
    await reload()
  } catch (e) {
    actionError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function confirmRemove() {
  const row = removeRow.value
  removeRow.value = null
  if (!row) return
  saving.value = true
  actionError.value = ''
  notice.value = ''
  try {
    const map = await wholeMap()
    await forebrainApi.saveHooks(removeCommand(map, row.event, row.matcherIdx, row.commandIdx))
    notice.value = t('hooks.saved')
    await reload()
  } catch (e) {
    actionError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void reload()
  void loadTools()
})
</script>
