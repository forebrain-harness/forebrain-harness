<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-4xl flex-col gap-4">
      <p v-if="pageError || error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">
        {{ pageError || error }}
      </p>

      <ListToolbar>
        <template #left>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('permissions.searchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="permissions-search"
          />
        </template>
        <template #actions>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" data-testid="permissions-verify" @click="openVerify">
            {{ t('permissions.verifyTitle') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs" :disabled="!tools.length" data-testid="add-rule" @click="openAdd">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('permissions.addRuleButton') }}
          </Button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.empty') }}</div>
      <DataTable v-else :columns="columns" :rows="rows" :row-key="ruleKey" :row-attrs="() => ({ 'data-testid': 'agent-rule' })">
        <template #cell-behavior="{ row }">
          <BadgeComponent :variant="behaviorVariant(row.behavior)">{{ permissionBehaviorLabel(row.behavior) }}</BadgeComponent>
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
              :label="t('common.edit')"
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
      <ListNote>{{ noteParts[0] }}<span class="font-mono">state/permissions/local_settings.json</span>{{ noteParts[1] }}</ListNote>
    </div>

    <ForebrainModal :open="modalOpen" size="md" :title="editing ? t('permissions.editRule') : t('permissions.addRuleButton')" @close="modalOpen = false">
      <div class="grid gap-3 sm:grid-cols-2">
        <!-- The labels are their own elements rather than wrappers: the controls
             are the library's Select, a button, and a button inside a `label`
             would answer one click with two. -->
        <div class="block">
          <Label for="rule-behavior" class="mb-1 block text-[12px] font-normal leading-[normal] text-[var(--forebrain-text-2)]">{{ t('permissions.behavior') }}</Label>
          <Select :model-value="form.behavior" @update:model-value="(value) => (form.behavior = String(value ?? ''))">
            <SelectTrigger id="rule-behavior" class="forebrain-field h-9 text-[13px]" data-testid="rule-behavior">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="allow">{{ t('permissions.allow') }}</SelectItem>
              <SelectItem value="ask">{{ t('permissions.ask') }}</SelectItem>
              <SelectItem value="deny">{{ t('permissions.deny') }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="block">
          <Label for="rule-tool" class="mb-1 block text-[12px] font-normal leading-[normal] text-[var(--forebrain-text-2)]">{{ t('permissions.tool') }}</Label>
          <Select :model-value="form.toolName" @update:model-value="(value) => (form.toolName = String(value ?? ''))">
            <SelectTrigger id="rule-tool" class="forebrain-field h-9 font-mono text-[12px]" data-testid="rule-tool">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-if="!tools.length" :value="EMPTY_CHOICE">{{ t('common.loading') }}</SelectItem>
              <SelectItem v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.rulesTitle') }}</span>
        <Input v-model="form.ruleContent" class="forebrain-field h-9 w-full font-mono text-[12px]" :placeholder="t('permissions.contentPlaceholder')" data-testid="rule-content" />
      </label>
      <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('permissions.agentWhere') }}</p>
      <p v-if="ruleError" class="text-[12px] text-[var(--forebrain-danger)]">{{ ruleError }}</p>
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="modalOpen = false">{{ t('common.cancel') }}</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="!form.ruleContent.trim() || busy" data-testid="rule-save" @click="saveRule">
          {{ busy ? t('common.loading') : editing ? t('common.save') : t('common.add') }}
        </Button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="verifyOpen" size="md" :title="t('permissions.verifyTitle')" @close="verifyOpen = false">
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.verifyDescription') }}</p>
      <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
        <Select :model-value="probe.toolName" @update:model-value="(value) => (probe.toolName = String(value ?? ''))">
          <SelectTrigger
            class="forebrain-field h-9 w-full font-mono text-[12px]"
            data-testid="verify-tool"
            :aria-label="t('permissions.verifyTool')"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</SelectItem>
          </SelectContent>
        </Select>
        <Input v-model="probe.input" class="forebrain-field h-9 w-full font-mono text-[12px]" data-testid="verify-input" :placeholder="t('permissions.inputPlaceholder')" @keyup.enter="runVerify" />
      </div>
      <PermissionExplainResult v-if="explain" :explain="explain" />
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="verifyOpen = false">{{ t('common.close') }}</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="verifying || !probe.toolName.trim()" data-testid="verify-run" @click="runVerify">
          {{ verifying ? t('common.loading') : t('permissions.verify') }}
        </Button>
      </template>
    </ForebrainModal>

    <ConfirmDialog
      :open="removing !== null"
      :title="t('permissions.title')"
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
 * The permission rules in force for the active primary agent: the list, with
 * its buttons above it. Adding, editing and the decision probe all happen in
 * modals. Reading the rules says what is configured; the probe says what will
 * actually happen, which is the part worth trusting.
 *
 * The table is the server's (plan 073): the search and the page are applied
 * there, and a new or changed rule reloads the page it was written from.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { Pencil, Plus, Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
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
import { getErrorMessage, forebrainApi, type PermissionExplainResponse, type PermissionRuleRecord } from '@/lib/api'
import { permissionBehaviorLabel } from '@/lib/permissionLabels'
import { EMPTY_CHOICE } from '@/lib/selectValue'
import { useI18n } from '@/locales'

const { t } = useI18n()

const tools = ref<string[]>([])
const busy = ref(false)
const pageError = ref('')
const ruleError = ref('')

const modalOpen = ref(false)
const editing = ref<PermissionRuleRecord | null>(null)
const form = reactive({ behavior: 'allow', toolName: '', ruleContent: '' })
const removing = ref<PermissionRuleRecord | null>(null)

const verifyOpen = ref(false)
const probe = reactive({ toolName: '', input: '' })
const explain = ref<PermissionExplainResponse | null>(null)
const verifying = ref(false)

// The page is the agent's own: only its local rules are listed here, a
// project's live in that project's space and a conversation's in the chat.
const { rows, total, page, pageSize, q, loading, error, reload } = useServerList<PermissionRuleRecord>(
  forebrainApi.permissionsRulesPage,
  { pageSize: 10, filters: { source: 'localSettings' } },
)

/**
 * The table's columns, at the widths the design declares: the rule text takes
 * the room the others leave. Nothing sorts — the list endpoint ignores `sort`,
 * so a header that offered it would promise an order the server never sends.
 */
const columns: DataColumn[] = [
  { id: 'behavior', header: t('permissions.behavior'), size: 88 },
  { id: 'toolName', header: t('permissions.tool'), size: 200 },
  { id: 'ruleContent', header: t('permissions.rulesTitle') },
  { id: 'actions', header: t('permissions.colActions'), size: 88, headerClass: 'text-center', cellClass: 'text-right' },
]

/**
 * A rule's identity: the three fields that make it, plus its place. Two rules
 * can differ only by an empty content, and the list is a page of a longer set,
 * so the place is what keeps a re-render from merging two rows.
 */
function ruleKey(rule: PermissionRuleRecord): string {
  return [rule.behavior, rule.toolName, rule.ruleContent, rows.value.indexOf(rule)].join('\u0000')
}

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

/** The note names the rules file in monospace, so it is split around it. */
const noteParts = computed(() => t('permissions.note').split('{path}'))

async function loadTools() {
  try {
    const res = await forebrainApi.toolsList()
    tools.value = res.map((tool) => tool.name).filter(Boolean)
    if (tools.value.length && !probe.toolName) probe.toolName = tools.value[0]
  } catch {
    tools.value = []
  }
}

function openAdd() {
  editing.value = null
  Object.assign(form, { behavior: 'allow', toolName: tools.value[0] ?? '', ruleContent: '' })
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
      await forebrainApi.permissionsUpdate({
        type: 'removeRules',
        destination: 'localSettings',
        behavior: editing.value.behavior,
        rules: [editing.value.rule],
      })
    }
    await forebrainApi.permissionsUpdate({
      type: 'addRules',
      destination: 'localSettings',
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
  pageError.value = ''
  try {
    await forebrainApi.permissionsUpdate({
      type: 'removeRules',
      destination: 'localSettings',
      behavior: rule.behavior,
      rules: [rule.rule],
    })
    await reload()
  } catch (cause) {
    pageError.value = getErrorMessage(cause)
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
  const toolName = probe.toolName.trim()
  if (!toolName || verifying.value) return
  verifying.value = true
  pageError.value = ''
  try {
    explain.value = await forebrainApi.permissionsExplain({ toolName, input: probe.input.trim() })
  } catch (e) {
    pageError.value = getErrorMessage(e)
    explain.value = null
  } finally {
    verifying.value = false
  }
}

onMounted(() => {
  void reload()
  // Tool names come from the runtime's own registry, so the dropdowns never
  // list a tool the engine does not expose.
  void loadTools()
})
</script>
