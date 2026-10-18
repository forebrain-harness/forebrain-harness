<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-4xl flex-col gap-4">
      <p v-if="pageError || error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">
        {{ pageError || error }}
      </p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('permissions.searchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="permissions-search"
          />
        </template>
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="permissions-verify" @click="openVerify">
            {{ t('permissions.verifyTitle') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" :disabled="!tools.length" data-testid="add-rule" @click="openAdd">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('permissions.addRuleButton') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.empty') }}</div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table forebrain-table--fixed">
          <colgroup>
            <col style="width: 88px" />
            <col style="width: 200px" />
            <col />
            <col style="width: 88px" />
          </colgroup>
          <thead>
            <tr>
              <th>{{ t('permissions.behavior') }}</th>
              <th>{{ t('permissions.tool') }}</th>
              <th>{{ t('permissions.rulesTitle') }}</th>
              <th class="text-right">{{ t('permissions.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(rule, idx) in rows" :key="`${rule.behavior}-${rule.toolName}-${rule.ruleContent}-${idx}`" data-testid="agent-rule">
              <td>
                <BadgeComponent :variant="behaviorVariant(rule.behavior)">{{ permissionBehaviorLabel(rule.behavior) }}</BadgeComponent>
              </td>
              <td>
                <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="rule.toolName || '*'">{{ rule.toolName || '*' }}</span>
              </td>
              <td>
                <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="rule.ruleContent ?? ''">{{ rule.ruleContent }}</span>
              </td>
              <td class="text-right">
                <RowActions>
                  <RowAction
                    :icon="Pencil"
                    :label="t('common.edit')"
                    :name="rule.ruleContent ?? ''"
                    :disabled="busy"
                    data-testid="edit-rule"
                    @click="openEdit(rule)"
                  />
                  <RowAction
                    :icon="Trash2"
                    :label="t('common.delete')"
                    :name="rule.ruleContent ?? ''"
                    tone="danger"
                    :disabled="busy"
                    data-testid="remove-rule"
                    @click="askRemove(rule)"
                  />
                </RowActions>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ noteParts[0] }}<span class="font-mono">state/permissions/local_settings.json</span>{{ noteParts[1] }}</ListNote>
    </div>

    <ForebrainModal :open="modalOpen" size="md" :title="editing ? t('permissions.editRule') : t('permissions.addRuleButton')" @close="modalOpen = false">
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.behavior') }}</span>
          <select v-model="form.behavior" class="forebrain-field h-9 text-[13px]" data-testid="rule-behavior">
            <option value="allow">{{ t('permissions.allow') }}</option>
            <option value="ask">{{ t('permissions.ask') }}</option>
            <option value="deny">{{ t('permissions.deny') }}</option>
          </select>
        </label>
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.tool') }}</span>
          <select v-model="form.toolName" class="forebrain-field h-9 font-mono text-[12px]" data-testid="rule-tool">
            <option v-if="!tools.length" value="">{{ t('common.loading') }}</option>
            <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
          </select>
        </label>
      </div>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.rulesTitle') }}</span>
        <input v-model="form.ruleContent" class="forebrain-field h-9 w-full font-mono text-[12px]" :placeholder="t('permissions.contentPlaceholder')" data-testid="rule-content" />
      </label>
      <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('permissions.agentWhere') }}</p>
      <p v-if="ruleError" class="text-[12px] text-[var(--forebrain-danger)]">{{ ruleError }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalOpen = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!form.ruleContent.trim() || busy" data-testid="rule-save" @click="saveRule">
          {{ busy ? t('common.loading') : editing ? t('common.save') : t('common.add') }}
        </button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="verifyOpen" size="md" :title="t('permissions.verifyTitle')" @close="verifyOpen = false">
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.verifyDescription') }}</p>
      <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
        <select v-model="probe.toolName" class="forebrain-field h-9 font-mono text-[12px]" data-testid="verify-tool" :aria-label="t('permissions.verifyTool')">
          <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
        </select>
        <input v-model="probe.input" class="forebrain-field h-9 w-full font-mono text-[12px]" data-testid="verify-input" :placeholder="t('permissions.inputPlaceholder')" @keyup.enter="runVerify" />
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
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
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
