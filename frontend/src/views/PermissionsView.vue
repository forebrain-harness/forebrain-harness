<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-4xl">
      <PageTopRow :title="t('permissions.title')" :description="t('permissions.description')">
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="permissions-verify" @click="openVerify">
            {{ t('permissions.verifyTitle') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!tools.length" data-testid="add-rule" @click="openAdd">
            {{ t('permissions.addRuleButton') }}
          </button>
        </template>
      </PageTopRow>

      <p v-if="error" class="mb-4 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading" class="py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rules.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.empty') }}</div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table">
          <thead>
            <tr>
              <th>{{ t('permissions.behavior') }}</th>
              <th>{{ t('permissions.tool') }}</th>
              <th>{{ t('permissions.rulesTitle') }}</th>
              <th class="text-right">{{ t('permissions.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(rule, idx) in rules" :key="`${rule.behavior}-${rule.toolName}-${rule.ruleContent}-${idx}`" data-testid="agent-rule">
              <td>
                <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="permissionBehaviorClass(rule.behavior)">{{ permissionBehaviorLabel(rule.behavior) }}</span>
              </td>
              <td class="font-mono">{{ rule.toolName || '*' }}</td>
              <td class="font-mono text-[var(--forebrain-text-2)]">{{ rule.ruleContent }}</td>
              <td class="text-right">
                <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="busy" data-testid="edit-rule" @click="openEdit(rule)">
                  {{ t('permissions.editRule') }}
                </button>
                <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" :disabled="busy" data-testid="remove-rule" @click="askRemove(rule)">
                  {{ t('common.delete') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p class="mt-3 font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ t('permissions.storageHint') }}</p>
      <p class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('permissions.modeHint') }}</p>
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
 */
import { onMounted, reactive, ref } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import PermissionExplainResult from '@/components/permissions/PermissionExplainResult.vue'
import { getErrorMessage, forebrainApi, type PermissionExplainResponse, type PermissionRuleRecord } from '@/lib/api'
import { permissionBehaviorClass, permissionBehaviorLabel } from '@/lib/permissionLabels'
import { useI18n } from '@/locales'

const { t } = useI18n()
const rules = ref<PermissionRuleRecord[]>([])
const tools = ref<string[]>([])
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const ruleError = ref('')

const modalOpen = ref(false)
const editing = ref<PermissionRuleRecord | null>(null)
const form = reactive({ behavior: 'allow', toolName: '', ruleContent: '' })
const removing = ref<PermissionRuleRecord | null>(null)

const verifyOpen = ref(false)
const probe = reactive({ toolName: '', input: '' })
const explain = ref<PermissionExplainResponse | null>(null)
const verifying = ref(false)

async function loadTools() {
  try {
    const res = await forebrainApi.toolsList()
    tools.value = res.map((tool) => tool.name).filter(Boolean)
    if (tools.value.length && !probe.toolName) probe.toolName = tools.value[0]
  } catch {
    tools.value = []
  }
}

// The page is the agent's own: only its local rules are listed here, a
// project's live in that project's space and a conversation's in the chat.
async function loadRules() {
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.permissionsRules({ source: 'localSettings' })
    rules.value = res.rules
  } catch (e) {
    error.value = getErrorMessage(e)
    rules.value = []
  } finally {
    loading.value = false
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
    await loadRules()
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
  error.value = ''
  try {
    await forebrainApi.permissionsUpdate({
      type: 'removeRules',
      destination: 'localSettings',
      behavior: rule.behavior,
      rules: [rule.rule],
    })
    await loadRules()
  } catch (cause) {
    error.value = getErrorMessage(cause)
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
  error.value = ''
  try {
    explain.value = await forebrainApi.permissionsExplain({ toolName, input: probe.input.trim() })
  } catch (e) {
    error.value = getErrorMessage(e)
    explain.value = null
  } finally {
    verifying.value = false
  }
}

onMounted(() => {
  void loadRules()
  // Tool names come from the runtime's own registry, so the dropdowns never
  // list a tool the engine does not expose.
  void loadTools()
})
</script>
