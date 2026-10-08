<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('permissions.title') }}</h1>
            <ScopeBadge type="agent" :label="t('scope.agent')" />
          </div>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('permissions.description') }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="loadRules">
          {{ t('common.refresh') }}
        </button>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <!-- Verify before you trust: ask the runtime what it would actually do
           with a given call, and see which rule decided it. -->
      <CardComponent class="mb-5">
        <template #header>
          <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.verifyTitle') }}</h2>
        </template>
        <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.verifyDescription') }}</p>
        <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]">
          <select
            v-model="probe.toolName"
            :aria-label="t('permissions.tool')"
            class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 font-mono text-[12px] text-[var(--forebrain-text)] outline-none focus:border-[var(--forebrain-focus-border)]"
          >
            <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
          </select>
          <input
            v-model="probe.input"
            :placeholder="t('permissions.inputPlaceholder')"
            class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 font-mono text-[12px] text-[var(--forebrain-text)] outline-none focus:border-[var(--forebrain-focus-border)]"
            @keyup.enter="verify"
          />
          <button
            type="button"
            class="forebrain-btn forebrain-btn-primary h-9 px-4 text-[12px]"
            :disabled="verifying || !probe.toolName.trim()"
            @click="verify"
          >
            {{ t('permissions.verify') }}
          </button>
        </div>
        <PermissionExplainResult v-if="explain" :explain="explain" />
      </CardComponent>

      <!-- Add a local rule: fixed choices are dropdowns; the pattern is the
           only free text. -->
      <CardComponent class="mb-5">
        <template #header>
          <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.addRuleTitle') }}</h2>
        </template>
        <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.addRuleDescription') }}</p>
        <div class="grid gap-2 md:grid-cols-[130px_minmax(0,1fr)_minmax(0,1.4fr)_auto]">
          <select v-model="ruleForm.behavior" class="forebrain-field text-[13px]" :aria-label="t('permissions.behavior')">
            <option value="allow">{{ t('permissions.allow') }}</option>
            <option value="deny">{{ t('permissions.deny') }}</option>
            <option value="ask">{{ t('permissions.ask') }}</option>
          </select>
          <select v-model="ruleForm.toolName" class="forebrain-field font-mono text-[12px]" :aria-label="t('permissions.tool')">
            <option v-if="!tools.length" value="">{{ t('common.loading') }}</option>
            <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
          </select>
          <input v-model="ruleForm.ruleContent" class="forebrain-field font-mono text-[12px]" :placeholder="t('permissions.contentPlaceholder')" />
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!ruleForm.ruleContent.trim() || savingRule" data-testid="add-rule" @click="addRule">
            {{ savingRule ? t('common.loading') : t('common.add') }}
          </button>
        </div>
        <p v-if="ruleError" class="text-[12px] text-[var(--forebrain-danger)]">{{ ruleError }}</p>
      </CardComponent>

      <CardComponent>
        <template #header>
          <div class="flex w-full flex-wrap items-center justify-between gap-2">
            <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.rulesTitle') }}</h2>
            <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.mode') }}: {{ permissionModeLabel(mode) }}</span>
          </div>
        </template>
        <div v-if="loading" class="py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
        <ul v-else-if="rules.length" class="space-y-1">
          <li
            v-for="(rule, idx) in rules"
            :key="`${rule.behavior}-${rule.toolName}-${rule.ruleContent}-${idx}`"
            class="flex flex-wrap items-center gap-2 rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface)]"
            data-testid="agent-rule"
          >
            <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="permissionBehaviorClass(rule.behavior)">
              {{ permissionBehaviorLabel(rule.behavior) }}
            </span>
            <span class="font-mono text-[var(--forebrain-text)]">{{ rule.toolName || '*' }}</span>
            <span v-if="rule.ruleContent" class="min-w-0 break-all font-mono text-[var(--forebrain-text-2)]">{{ rule.ruleContent }}</span>
            <button
              type="button"
              class="forebrain-btn forebrain-btn-ghost ml-auto h-7 px-2 text-[11px] text-[var(--forebrain-danger)]"
              :disabled="removing"
              data-testid="remove-rule"
              @click="removeRule(rule)"
            >
              {{ t('common.delete') }}
            </button>
          </li>
        </ul>
        <p v-else class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
          {{ t('common.empty') }}
        </p>
      </CardComponent>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The permission rules in force for the active primary agent, and a probe that
 * asks the runtime what it would do with a given call. Reading the rules says
 * what is configured; the probe says what will actually happen, which is the
 * part worth trusting.
 */
import { onMounted, reactive, ref } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import PermissionExplainResult from '@/components/permissions/PermissionExplainResult.vue'
import { getErrorMessage, forebrainApi, type PermissionExplainResponse, type PermissionRuleRecord } from '@/lib/api'
import { permissionBehaviorClass, permissionBehaviorLabel, permissionModeLabel } from '@/lib/permissionLabels'
import { useI18n } from '@/locales'

const { t } = useI18n()
const rules = ref<PermissionRuleRecord[]>([])
const mode = ref('')
const loading = ref(false)
const error = ref('')
const removing = ref(false)
const probe = reactive({ toolName: '', input: '' })
const tools = ref<string[]>([])
const ruleForm = reactive({ behavior: 'allow', toolName: '', ruleContent: '' })
const savingRule = ref(false)
const ruleError = ref('')

async function loadTools() {
  try {
    const res = await forebrainApi.toolsList()
    tools.value = res.map((tool) => tool.name).filter(Boolean)
    if (tools.value.length && !probe.toolName) probe.toolName = tools.value[0]
    if (tools.value.length && !ruleForm.toolName) ruleForm.toolName = tools.value[0]
  } catch {
    tools.value = []
  }
}

async function addRule() {
  if (savingRule.value || !ruleForm.ruleContent.trim()) return
  savingRule.value = true
  ruleError.value = ''
  try {
    await forebrainApi.permissionsUpdate({
      type: 'addRules',
      destination: 'localSettings',
      behavior: ruleForm.behavior,
      rules: [{ toolName: ruleForm.toolName, ruleContent: ruleForm.ruleContent.trim() }],
    })
    ruleForm.ruleContent = ''
    await loadRules()
  } catch (cause) {
    ruleError.value = getErrorMessage(cause)
  } finally {
    savingRule.value = false
  }
}

async function removeRule(rule: PermissionRuleRecord) {
  if (removing.value) return
  removing.value = true
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
    removing.value = false
  }
}

const explain = ref<PermissionExplainResponse | null>(null)
const verifying = ref(false)

// The page is the agent's own: only its local rules are listed here, a
// project's live in that project's space and a conversation's in the chat.
async function loadRules() {
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.permissionsRules({ source: 'localSettings' })
    mode.value = res.mode
    rules.value = res.rules
  } catch (e) {
    error.value = getErrorMessage(e)
    rules.value = []
  } finally {
    loading.value = false
  }
}

async function verify() {
  const toolName = probe.toolName.trim()
  if (!toolName) return
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
