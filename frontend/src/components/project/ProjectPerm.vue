<template>
  <div class="mx-auto max-w-4xl space-y-5">
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
      <PageTopRow :title="t('permissions.projectRulesTitle')" :description="t('permissions.projectPermDesc')">
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="perm-verify" @click="openVerify">
            {{ t('permissions.projectVerifyButton') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!tools.length" data-testid="add-rule" @click="openAdd">
            {{ t('permissions.addRuleButton') }}
          </button>
        </template>
      </PageTopRow>

      <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading" class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rules.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-[13px] text-[var(--forebrain-muted-text)]">
        {{ t('settings.noRules') }}
      </div>
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
            <tr v-for="(rule, index) in rules" :key="`${rule.behavior}-${rule.toolName}-${rule.ruleContent}-${index}`" data-testid="project-rule">
              <td>
                <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="behaviorClass(rule.behavior)">{{ behaviorLabel(rule.behavior) }}</span>
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
      <p class="mt-3 font-mono text-[11px] text-[var(--forebrain-muted-text)]">.forebrain/safety.json</p>
    </template>

    <ForebrainModal :open="modalOpen" size="md" :title="editing ? t('permissions.editRule') : t('permissions.addRuleButton')" @close="modalOpen = false">
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('permissions.behavior') }}</span>
          <select v-model="form.behavior" class="forebrain-field h-9 text-[13px]" data-testid="rule-behavior" :title="t('permissions.projectRulesNoAllow')">
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
 * This project's own permission rules (its .forebrain/safety.json) as a list,
 * with its buttons above it; adding and editing happen in a modal, and the
 * match test is a modal too. Rules are read and written through the project's
 * own endpoints — never the rules of whatever project the gateway happens to
 * run in. The list waits for the trust gate, and a project the engine would
 * not take rules from (not under version control) says so instead of offering
 * writes that would be dropped.
 */
import { onMounted, reactive, ref, watch } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import PermissionExplainResult from '@/components/permissions/PermissionExplainResult.vue'
import { getErrorMessage, forebrainApi, type PermissionExplainResponse, type PermissionRuleRecord, type ProjectRecord } from '@/lib/api'
import { permissionBehaviorClass, permissionBehaviorLabel } from '@/lib/permissionLabels'
import { useI18n } from '@/locales'

const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const rules = ref<PermissionRuleRecord[]>([])
const applies = ref(false)
const tools = ref<string[]>([])
const loading = ref(false)
const busy = ref(false)
const error = ref('')
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

function behaviorLabel(behavior: string): string {
  return permissionBehaviorLabel(behavior)
}

function behaviorClass(behavior: string): string {
  return permissionBehaviorClass(behavior)
}

async function loadRules() {
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.projectPermissionRules(props.projectId)
    applies.value = res.applies
    rules.value = res.rules
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

async function loadTools() {
  try {
    const res = await forebrainApi.toolsList()
    tools.value = res.map((tool) => tool.name).filter(Boolean)
    if (tools.value.length && !form.toolName) form.toolName = tools.value[0]
    if (tools.value.length && !probe.toolName) probe.toolName = tools.value[0]
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

async function trust() {
  if (trusting.value) return
  trusting.value = true
  error.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    trusted.value = true
    await loadRules()
  } catch (cause) {
    error.value = getErrorMessage(cause)
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
    if (editing.value) {
      const removed = await forebrainApi.projectPermissionUpdate(props.projectId, {
        type: 'removeRules',
        behavior: editing.value.behavior,
        rules: [editing.value.rule],
      })
      rules.value = removed.rules ?? []
    }
    const res = await forebrainApi.projectPermissionUpdate(props.projectId, {
      type: 'addRules',
      behavior: form.behavior,
      rules: [{ toolName: form.toolName, ruleContent: form.ruleContent.trim() }],
    })
    rules.value = res.rules ?? []
    modalOpen.value = false
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
    const res = await forebrainApi.projectPermissionUpdate(props.projectId, {
      type: 'removeRules',
      behavior: rule.behavior,
      rules: [rule.rule],
    })
    rules.value = res.rules ?? []
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
  if (verifying.value || !probe.toolName) return
  verifying.value = true
  error.value = ''
  try {
    explain.value = await forebrainApi.projectPermissionExplain(props.projectId, { toolName: probe.toolName, input: probe.input.trim() })
  } catch (cause) {
    error.value = getErrorMessage(cause)
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
  if (value) void loadRules()
}, { immediate: true })
</script>
