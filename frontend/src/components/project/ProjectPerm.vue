<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="flex items-center gap-2">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.rulesTitle') }}</div>
        <span class="scope-badge">{{ t('scope.project') }}</span>
      </div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.projectRulesDescription') }}</p>

      <!-- Trust gate: project rules only load for a trusted project. -->
      <div v-if="!trusted" class="mt-4 rounded-xl border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
        <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
        <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
        <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="perm-trust" @click="trust">
          {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
        </button>
      </div>

      <template v-else>
        <!-- Add rule: every fixed choice is a dropdown; only the content is typed. -->
        <div class="mt-4 grid gap-2 md:grid-cols-[130px_minmax(0,1fr)_minmax(0,1fr)_auto]">
          <select v-model="form.behavior" class="forebrain-field text-[13px]" :aria-label="t('permissions.behavior')">
            <option value="allow">{{ t('permissions.allow') }}</option>
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

        <p v-if="error" class="mt-2 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

        <div v-if="loading" class="mt-4 text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
        <div v-else-if="!rules.length" class="mt-4 rounded-xl border border-dashed border-[var(--forebrain-divider)] px-6 py-8 text-center text-[13px] text-[var(--forebrain-muted-text)]">
          {{ t('settings.noRules') }}
        </div>
        <ul v-else class="mt-4 divide-y divide-[var(--forebrain-divider)]">
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
    </section>

    <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.explainTitle') }}</div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.projectExplainDescription') }}</p>
      <div class="mt-3 grid gap-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
        <select v-model="explainForm.toolName" class="forebrain-field text-[13px]" :aria-label="t('permissions.tool')">
          <option v-for="tool in tools" :key="tool" :value="tool">{{ tool }}</option>
        </select>
        <input v-model="explainForm.input" class="forebrain-field text-[13px]" :placeholder="t('permissions.contentPlaceholder')" />
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="explaining" @click="explain">
          {{ explaining ? t('common.loading') : t('permissions.explain') }}
        </button>
      </div>
      <pre v-if="explainResult" class="mt-3 max-h-64 overflow-auto rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-code-bg)] p-3 text-[12px] leading-relaxed text-[var(--forebrain-code-text)]">{{ explainResult }}</pre>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { getErrorMessage, forebrainApi, type PermissionRuleRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * This project's own permission rules (destination: projectSettings). The
 * page is disabled until the project is trusted — the engine refuses those
 * writes for an untrusted root, so the honest UI refuses them too.
 */
const props = defineProps<{ project: { root?: string; trustRecorded?: boolean } | null; projectId: string }>()

const { t } = useI18n()

const rules = ref<PermissionRuleRecord[]>([])
const tools = ref<string[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const trusted = ref(false)
const trusting = ref(false)
const explaining = ref(false)
const explainResult = ref('')
const form = reactive({ behavior: 'allow', toolName: '', ruleContent: '' })
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
    const res = await forebrainApi.permissionsRules({ source: 'projectSettings' })
    rules.value = (res.rules ?? []).filter((rule) => rule.source === 'projectSettings')
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
  } catch {
    tools.value = []
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
    await forebrainApi.permissionsUpdate({
      type: 'addRules',
      destination: 'projectSettings',
      behavior: form.behavior,
      rules: [{ toolName: form.toolName, ruleContent: form.ruleContent.trim() }],
    })
    form.ruleContent = ''
    await loadRules()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

async function removeRule(rule: PermissionRuleRecord) {
  error.value = ''
  try {
    await forebrainApi.permissionsUpdate({
      type: 'removeRules',
      destination: 'projectSettings',
      behavior: rule.behavior,
      rules: [{ toolName: rule.toolName, ruleContent: rule.ruleContent }],
    })
    await loadRules()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

async function explain() {
  if (explaining.value) return
  explaining.value = true
  error.value = ''
  explainResult.value = ''
  try {
    const res = await forebrainApi.permissionsExplain({
      toolName: explainForm.toolName,
      input: explainForm.input,
    })
    explainResult.value = JSON.stringify(res, null, 2)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    explaining.value = false
  }
}

onMounted(async () => {
  trusted.value = Boolean(props.project?.trustRecorded)
  await Promise.all([loadTools(), loadRules()])
})
</script>

<style scoped>
.scope-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 9999px;
  padding: 2px 10px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
</style>
