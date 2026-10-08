<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <CardComponent>
      <template #header>
        <div class="flex items-center gap-2">
          <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.rulesTitle') }}</div>
          <ScopeBadge type="project" :label="t('scope.project')" />
        </div>
      </template>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.projectRulesDescription') }}</p>

      <!-- Trust gate: project rules only load for a trusted project. -->
      <div v-if="!trusted" class="rounded-xl border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
        <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
        <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
        <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="perm-trust" @click="trust">
          {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
        </button>
      </div>

      <p v-else-if="!applies" class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-3 text-[12px] text-[var(--forebrain-muted-text)]" data-testid="perm-not-applied">
        {{ t('permissions.projectRulesNotApplied') }}
      </p>

      <template v-else>
        <!-- Add rule: every fixed choice is a dropdown; only the content is typed.
             A project's rules may deny or ask; allowing is the user's own call. -->
        <div class="grid gap-2 md:grid-cols-[130px_minmax(0,1fr)_minmax(0,1fr)_auto]">
          <select v-model="form.behavior" class="forebrain-field text-[13px]" :aria-label="t('permissions.behavior')" :title="t('permissions.projectRulesNoAllow')">
            <option value="deny">{{ t('permissions.deny') }}</option>
            <option value="ask">{{ t('permissions.ask') }}</option>
          </select>
          <select v-model="form.toolName" class="forebrain-field text-[13px]" :aria-label="t('permissions.tool')">
            <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
          </select>
          <input v-model="form.ruleContent" class="forebrain-field text-[13px]" :placeholder="t('permissions.contentPlaceholder')" />
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!form.ruleContent.trim() || saving" data-testid="perm-add" @click="addRule">
            {{ saving ? t('common.loading') : t('common.add') }}
          </button>
        </div>

        <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

        <div v-if="loading" class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
        <div v-else-if="!rules.length" class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-6 py-8 text-center text-[13px] text-[var(--forebrain-muted-text)]">
          {{ t('settings.noRules') }}
        </div>
        <ul v-else class="divide-y divide-[var(--forebrain-divider)]">
          <li v-for="(rule, index) in rules" :key="ruleKey(rule, index)" class="flex items-center gap-3 py-2.5 text-[13px]">
            <span class="w-14 shrink-0 font-medium" :class="behaviorClass(rule.behavior)">{{ behaviorLabel(rule.behavior) }}</span>
            <span class="w-32 shrink-0 truncate font-mono text-[12px] text-[var(--forebrain-text-2)]" :title="rule.toolName">{{ rule.toolName }}</span>
            <span class="min-w-0 flex-1 truncate font-mono text-[12px] text-[var(--forebrain-text-2)]" :title="rule.ruleContent">{{ rule.ruleContent }}</span>
            <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" @click="removeRule(rule)">
              {{ t('common.delete') }}
            </button>
          </li>
        </ul>
      </template>
    </CardComponent>

    <CardComponent>
      <template #header>
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.explainTitle') }}</div>
      </template>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.projectExplainDescription') }}</p>
      <div class="grid gap-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
        <select v-model="explainForm.toolName" class="forebrain-field text-[13px]" :aria-label="t('permissions.tool')">
          <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
        </select>
        <input v-model="explainForm.input" class="forebrain-field text-[13px]" :placeholder="t('permissions.contentPlaceholder')" />
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="explaining" @click="explain">
          {{ explaining ? t('common.loading') : t('permissions.explain') }}
        </button>
      </div>
      <PermissionExplainResult v-if="explainResult" :explain="explainResult" />
    </CardComponent>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import PermissionExplainResult from '@/components/permissions/PermissionExplainResult.vue'
import { getErrorMessage, forebrainApi, type PermissionExplainResponse, type PermissionRuleRecord, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * This project's own permission rules (its .forebrain/safety.json), read and
 * written through the project's own endpoints — never the rules of whatever
 * project the gateway happens to run in. The form waits for the trust gate,
 * and a project the engine would not take rules from (not under version
 * control) says so instead of offering writes that would be dropped.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const rules = ref<PermissionRuleRecord[]>([])
const applies = ref(false)
const tools = ref<string[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const trusted = ref(false)
const trusting = ref(false)
const explaining = ref(false)
const explainResult = ref<PermissionExplainResponse | null>(null)
const form = reactive({ behavior: 'deny', toolName: '', ruleContent: '' })
const explainForm = reactive({ toolName: '', input: '' })

function behaviorLabel(behavior: string): string {
  if (behavior === 'allow') return t('permissions.allow')
  if (behavior === 'deny') return t('permissions.deny')
  if (behavior === 'ask') return t('permissions.ask')
  return behavior
}

function behaviorClass(behavior: string): string {
  if (behavior === 'allow') return 'text-[var(--forebrain-success)]'
  if (behavior === 'deny') return 'text-[var(--forebrain-danger)]'
  return 'text-[var(--forebrain-warning)]'
}

function ruleKey(rule: PermissionRuleRecord, index: number): string {
  return `${rule.source}-${rule.behavior}-${rule.toolName}-${rule.ruleContent}-${index}`
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
    if (tools.value.length && !explainForm.toolName) explainForm.toolName = tools.value[0]
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

async function addRule() {
  if (saving.value || !form.ruleContent.trim()) return
  saving.value = true
  error.value = ''
  try {
    const res = await forebrainApi.projectPermissionUpdate(props.projectId, {
      type: 'addRules',
      behavior: form.behavior,
      rules: [{ toolName: form.toolName, ruleContent: form.ruleContent.trim() }],
    })
    form.ruleContent = ''
    rules.value = res.rules ?? []
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

async function removeRule(rule: PermissionRuleRecord) {
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
  }
}

async function explain() {
  if (explaining.value || !explainForm.toolName) return
  explaining.value = true
  error.value = ''
  explainResult.value = null
  try {
    explainResult.value = await forebrainApi.projectPermissionExplain(props.projectId, {
      toolName: explainForm.toolName,
      input: explainForm.input,
    })
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    explaining.value = false
  }
}

// The shell loads the project row (its trust decision included) and hands it
// down; the gate follows it rather than guessing from a field only the create
// response carries.
watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
}, { immediate: true })

void loadTools()
void loadRules()
</script>
