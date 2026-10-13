<template>
  <div class="pb-6">
    <PageTopRow :title="t('hooks.title')" :description="t('hooks.description')">
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving || !events.length" data-testid="hooks-add" @click="openAdd">
          {{ t('hooks.addTitle') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
      </template>
    </PageTopRow>

    <p v-if="error" class="mb-4 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="mb-4 text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('hooks.empty') }}</div>
    <div v-else class="forebrain-table-wrap">
      <table class="forebrain-table">
        <thead>
          <tr>
            <th>{{ t('hooks.eventLabel') }}</th>
            <th>{{ t('hooks.matcherLabel') }}</th>
            <th>{{ t('hooks.commandLabel') }}</th>
            <th class="text-right">{{ t('hooks.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in rows" :key="`${row.event}-${row.matcherIdx}-${row.commandIdx}`" data-testid="hook-row">
            <td class="font-mono">{{ row.event }}</td>
            <td :class="row.matcher ? 'font-mono' : 'text-[var(--forebrain-text-2)]'">{{ row.matcher || t('hooks.anyTool') }}</td>
            <td class="font-mono">{{ row.command }}</td>
            <td class="text-right">
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="saving" data-testid="hook-edit" @click="openEdit(row)">
                {{ t('hooks.edit') }}
              </button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" :disabled="saving" data-testid="hook-remove" @click="askRemove(index)">
                {{ t('common.delete') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

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
      :open="removeIndex >= 0"
      :title="t('hooks.deleteTitle')"
      :message="t('hooks.deleteMessage')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="removeIndex = -1"
      @confirm="confirmRemove"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * Hooks run commands at points in a turn, as a list with its buttons above it;
 * adding and editing happen in a modal and write the whole table back. The
 * events and kinds offered here are the ones the runtime accepts — the lists
 * come from the server rather than being written down twice — so a hook that
 * is offered is a hook that will load.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { getErrorMessage, forebrainApi, type HookMatcherRecord, type HooksSettingsRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

type HookRow = { event: string; matcher: string; type: string; command: string; matcherIdx: number; commandIdx: number }

const hooks = ref<HooksSettingsRecord>({})
const events = ref<string[]>([])
const knownTypes = ref<string[]>([])
const tools = ref<string[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')

const modalOpen = ref(false)
const editing = ref<HookRow | null>(null)
const removeIndex = ref(-1)
const form = reactive({ event: '', type: 'command', matcher: '', command: '' })

const rows = computed<HookRow[]>(() => {
  const out: HookRow[] = []
  for (const event of Object.keys(hooks.value).sort()) {
    ;(hooks.value[event] ?? []).forEach((matcher, matcherIdx) => {
      ;(matcher.hooks ?? []).forEach((cmd, commandIdx) => {
        out.push({
          event,
          matcher: matcher.matcher ?? '',
          type: cmd.type ?? 'command',
          command: cmd.command ?? cmd.prompt ?? cmd.url ?? '',
          matcherIdx,
          commandIdx,
        })
      })
    })
  }
  return out
})

function removeCommand(map: HooksSettingsRecord, event: string, matcherIdx: number, commandIdx: number): HooksSettingsRecord {
  const list = [...(map[event] ?? [])]
  const matcher: HookMatcherRecord = { ...list[matcherIdx] }
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

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.hooks()
    hooks.value = res.hooks ?? {}
    events.value = res.events ?? []
    knownTypes.value = res.knownTypes ?? []
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
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

function openEdit(row: HookRow) {
  editing.value = row
  Object.assign(form, { event: row.event, type: row.type, matcher: row.matcher, command: row.command })
  modalOpen.value = true
}

async function saveModal() {
  const { event, type, matcher, command } = form
  if (!event || !command.trim()) return
  let map = { ...hooks.value }
  if (editing.value) map = removeCommand(map, editing.value.event, editing.value.matcherIdx, editing.value.commandIdx)
  map = addCommand(map, event, matcher, type, command.trim())
  modalOpen.value = false
  await persist(map)
}

function askRemove(index: number) {
  removeIndex.value = index
}

async function confirmRemove() {
  const row = rows.value[removeIndex.value]
  removeIndex.value = -1
  if (!row) return
  await persist(removeCommand(hooks.value, row.event, row.matcherIdx, row.commandIdx))
}

async function persist(map: HooksSettingsRecord) {
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.saveHooks(map)
    notice.value = t('hooks.saved')
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void load()
  void loadTools()
})
</script>
