<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <!-- Trust gate: project rules only load for a trusted project. -->
    <div v-if="!trusted" class="rounded-lg border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
      <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="perm-trust" @click="trust">
        {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
      </button>
    </div>

    <p v-else-if="!applies" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-3 text-[12px] text-[var(--forebrain-muted-text)]" data-testid="perm-not-applied">
      {{ t('permissions.projectRulesNotApplied') }}
    </p>

    <template v-else>
      <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('permissions.searchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="project-rules-search"
          />
        </template>
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="perm-verify" @click="openVerify">
            {{ t('permissions.projectVerifyButton') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" :disabled="!tools.length" data-testid="add-rule" @click="openAdd">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('permissions.addRuleButton') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('settings.noRules') }}
      </div>
      <DataTable v-else :columns="columns" :rows="rows" :row-key="ruleKey" :row-attrs="() => ({ 'data-testid': 'project-rule' })">
        <template #cell-behavior="{ row }">
          <BadgeComponent :variant="behaviorVariant(row.behavior)">{{ behaviorLabel(row.behavior) }}</BadgeComponent>
        </template>
        <template #cell-toolName="{ row }">
          <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="row.toolName || '*'">{{ row.toolName || '*' }}</span>
        </template>
        <template #cell-ruleContent="{ row }">
          <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="row.ruleContent ?? ''">{{ row.ruleContent }}</span>
        </template>
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction
              :icon="Pencil"
              :label="t('permissions.editRule')"
              :name="row.ruleContent ?? ''"
              :disabled="busy"
              data-testid="edit-rule"
              @click="openEdit(row)"
            />
            <RowAction
              :icon="Trash2"
              :label="t('common.delete')"
              :name="row.ruleContent ?? ''"
              tone="danger"
              :disabled="busy"
              data-testid="remove-rule"
              @click="askRemove(row)"
            />
          </RowActions>
        </template>
      </DataTable>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ noteParts[0] }}<span class="font-mono">.forebrain/safety.json</span>{{ noteParts[1] }}</ListNote>
    </template>

    <ForebrainModal :open="modalOpen" size="md" :title="editing ? t('permissions.editRule') : t('permissions.addRuleButton')" @close="modalOpen = false">
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.behavior') }}</span>
          <select v-model="form.behavior" class="forebrain-field h-9 text-[13px]" data-testid="rule-behavior" v-tip:top="t('permissions.projectRulesNoAllow')">
            <option value="deny">{{ t('permissions.deny') }}</option>
            <option value="ask">{{ t('permissions.ask') }}</option>
          </select>
        </label>
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.tool') }}</span>
          <select v-model="form.toolName" class="forebrain-field h-9 font-mono text-[12px]" data-testid="rule-tool">
            <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
          </select>
        </label>
      </div>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.rulesTitle') }}</span>
        <input v-model="form.ruleContent" class="forebrain-field h-9 w-full font-mono text-[12px]" :placeholder="t('permissions.contentPlaceholder')" data-testid="rule-content" />
      </label>
      <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('permissions.projectWriteHint') }}</p>
      <p v-if="ruleError" class="text-[12px] text-[var(--forebrain-danger)]">{{ ruleError }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalOpen = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!form.ruleContent.trim() || busy" data-testid="rule-save" @click="saveRule">
          {{ busy ? t('common.loading') : editing ? t('common.save') : t('common.add') }}
        </button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="verifyOpen" size="md" :title="t('permissions.projectVerifyButton')" @close="verifyOpen = false">
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.projectExplainDescription') }}</p>
      <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
        <select v-model="probe.toolName" class="forebrain-field h-9 font-mono text-[12px]" data-testid="verify-tool" :aria-label="t('permissions.verifyTool')">
          <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
        </select>
        <input v-model="probe.input" class="forebrain-field h-9 w-full font-mono text-[12px]" data-testid="verify-input" :placeholder="t('permissions.contentPlaceholder')" @keyup.enter="runVerify" />
      </div>
      <PermissionExplainResult v-if="explain" :explain="explain" />
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="verifyOpen = false">{{ t('common.close') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="verifying || !probe.toolName.trim()" data-testid="verify-run" @click="runVerify">
          {{ verifying ? t('common.loading') : t('permissions.verify') }}
        </button>
      </template>
    </ForebrainModal>

    <ConfirmDialog
      :open="removing !== null"
      :title="t('permissions.projectRulesTitle')"
      :message="t('permissions.removeConfirm')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="removing = null"
      @confirm="confirmRemove"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * This project's own permission rules (its .forebrain/safety.json) as a table,
 * with its buttons above it; adding and editing happen in a modal, and the
 * match test is a modal too. Rules are read and written through the project's
 * own endpoints — never the rules of whatever project the gateway happens to
 * run in. The table waits for the trust gate, and a project the engine would
 * not take rules from (not under version control) says so instead of offering
 * writes that would be dropped.
 *
 * The table is the server's (plan 075): the search and the page are applied
 * there, and a new or changed rule reloads the page it was written from.
 */
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { Pencil, Plus, Trash2 } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import PermissionExplainResult from '@/components/permissions/PermissionExplainResult.vue'
import { useServerList } from '@/composables/useServerList'
import {
  getErrorMessage,
  forebrainApi,
  type ListQuery,
  type PermissionExplainResponse,
  type PermissionRuleRecord,
  type ProjectPermissionRuleRow,
  type ProjectRecord,
} from '@/lib/api'
import { permissionBehaviorLabel } from '@/lib/permissionLabels'
import { useI18n } from '@/locales'

const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const tools = ref<string[]>([])
const busy = ref(false)
const writeError = ref('')
const ruleError = ref('')
const trusted = ref(false)
const trusting = ref(false)

const modalOpen = ref(false)
const editing = ref<PermissionRuleRecord | null>(null)
const form = reactive({ behavior: 'deny', toolName: '', ruleContent: '' })
const removing = ref<PermissionRuleRecord | null>(null)

const verifyOpen = ref(false)
const probe = reactive({ toolName: '', input: '' })
const explain = ref<PermissionExplainResponse | null>(null)
const verifying = ref(false)

/**
 * Whether the engine would take rules from this project at all. It travels with
 * every page and is why an empty table is empty, so it starts out assumed true:
 * the trust gate in front of the table is the first answer, and this one only
 * ever comes back from the server.
 */
const applies = ref(true)

async function fetchRules(query: ListQuery) {
  const answer = await forebrainApi.projectPermissionRulesPage(props.projectId, query)
  applies.value = answer.applies
  return answer
}

const { rows, total, page, pageSize, q, loading, error: listError, reload } = useServerList<ProjectPermissionRuleRow>(
  fetchRules,
  { pageSize: 10 },
)

/**
 * The table's columns, at the design's widths: the rule text takes the room the
 * others leave. Nothing sorts — the list endpoint ignores `sort`.
 */
const columns: DataColumn[] = [
  { id: 'behavior', header: t('permissions.behavior'), size: 88 },
  { id: 'toolName', header: t('permissions.tool'), size: 200 },
  { id: 'ruleContent', header: t('permissions.rulesTitle'), cellClass: 'text-[var(--forebrain-text-2)]' },
  { id: 'actions', header: t('permissions.colActions'), size: 88, headerClass: 'text-center', cellClass: 'text-right' },
]

/** A rule's identity across a reload: the three fields that make it. */
function ruleKey(rule: ProjectPermissionRuleRow): string {
  return [rule.behavior, rule.toolName, rule.ruleContent].join('\u0000')
}

const error = computed(() => listError.value || writeError.value)

/** What a rule does, as the badge's colour: allow green, ask yellow, deny red. */
function behaviorVariant(behavior?: string): 'success' | 'warning' | 'error' | 'gray' {
  switch (behavior) {
    case 'allow':
      return 'success'
    case 'ask':
      return 'warning'
    case 'deny':
      return 'error'
    default:
      return 'gray'
  }
}

function behaviorLabel(behavior: string): string {
  return permissionBehaviorLabel(behavior)
}

/** The note names the rules file in monospace, so it is split around it. */
const noteParts = computed(() => t('permissions.projectNote').split('{path}'))

async function loadTools() {
  try {
    const res = await forebrainApi.toolsList()
    tools.value = res.map((tool) => tool.name).filter(Boolean)
    if (tools.value.length && !form.toolName) form.toolName = tools.value[0]
    if (tools.value.length && !probe.toolName) probe.toolName = tools.value[0]
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  }
}

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

function openAdd() {
  editing.value = null
  Object.assign(form, { behavior: 'deny', toolName: tools.value[0] ?? '', ruleContent: '' })
  ruleError.value = ''
  modalOpen.value = true
}

function openEdit(rule: PermissionRuleRecord) {
  editing.value = rule
  Object.assign(form, { behavior: rule.behavior, toolName: rule.toolName, ruleContent: rule.ruleContent ?? '' })
  ruleError.value = ''
  modalOpen.value = true
}

async function saveRule() {
  if (busy.value || !form.ruleContent.trim()) return
  busy.value = true
  ruleError.value = ''
  try {
    // An edit is a remove of the old rule followed by an add of the new one:
    // the store keys a rule by its own value, so this is how one is changed in
    // place without leaving the old value behind.
    if (editing.value) {
      await forebrainApi.projectPermissionUpdate(props.projectId, {
        type: 'removeRules',
        behavior: editing.value.behavior,
        rules: [editing.value.rule],
      })
    }
    await forebrainApi.projectPermissionUpdate(props.projectId, {
      type: 'addRules',
      behavior: form.behavior,
      rules: [{ toolName: form.toolName, ruleContent: form.ruleContent.trim() }],
    })
    modalOpen.value = false
    await reload()
  } catch (cause) {
    ruleError.value = getErrorMessage(cause)
  } finally {
    busy.value = false
  }
}

function askRemove(rule: PermissionRuleRecord) {
  removing.value = rule
}

async function confirmRemove() {
  const rule = removing.value
  removing.value = null
  if (!rule) return
  busy.value = true
  writeError.value = ''
  try {
    await forebrainApi.projectPermissionUpdate(props.projectId, {
      type: 'removeRules',
      behavior: rule.behavior,
      rules: [rule.rule],
    })
    await reload()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    busy.value = false
  }
}

function openVerify() {
  explain.value = null
  if (!probe.toolName) probe.toolName = tools.value[0] ?? ''
  verifyOpen.value = true
}

async function runVerify() {
  if (verifying.value || !probe.toolName) return
  verifying.value = true
  writeError.value = ''
  try {
    explain.value = await forebrainApi.projectPermissionExplain(props.projectId, { toolName: probe.toolName, input: probe.input.trim() })
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
    explain.value = null
  } finally {
    verifying.value = false
  }
}

watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
}, { immediate: true })

onMounted(() => {
  void loadTools()
})

watch(trusted, (value) => {
  if (value) void reload()
}, { immediate: true })
</script>
