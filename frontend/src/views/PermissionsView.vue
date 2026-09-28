<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <h1 class="font-serif text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('permissions.title') }}</h1>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('permissions.description') }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="loadRules">
          {{ t('common.refresh') }}
        </button>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <!-- Verify before you trust: ask the runtime what it would actually do
           with a given call, and see which rule decided it. -->
      <section class="mb-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
        <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.verifyTitle') }}</h2>
        <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.verifyDescription') }}</p>
        <div class="mt-3 grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]">
          <input
            v-model="probe.toolName"
            :placeholder="t('permissions.toolNamePlaceholder')"
            class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 font-mono text-[12px] text-[var(--forebrain-text)] outline-none focus:border-[var(--forebrain-focus-border)]"
          />
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
        <div v-if="explain" class="mt-3 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-2">
          <div class="flex flex-wrap items-center gap-2 text-[12px]">
            <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="behaviorClass(explain.decision?.behavior)">
              {{ explain.decision?.behavior ? behaviorLabel(explain.decision.behavior) : t('common.none') }}
            </span>
            <span class="text-[var(--forebrain-muted-text)]">{{ t('permissions.mode') }}: {{ modeLabel(explain.decision?.mode) }}</span>
            <span v-if="explain.decision?.reason" class="text-[var(--forebrain-muted-text)]">{{ explain.decision.reason }}</span>
          </div>
          <ul v-if="explain.rules?.length" class="mt-2 space-y-1">
            <li
              v-for="(rule, idx) in explain.rules"
              :key="`${rule.source}-${rule.toolName}-${idx}`"
              class="flex flex-wrap items-center gap-2 text-[11px]"
              :class="rule.matched ? 'text-[var(--forebrain-text)]' : 'text-[var(--forebrain-muted-text)]'"
            >
              <span v-if="rule.matched" class="forebrain-rule-mark" aria-hidden="true" />
              <span class="font-mono">{{ rule.toolName || '*' }}</span>
              <span v-if="rule.ruleContent" class="font-mono">{{ rule.ruleContent }}</span>
              <span>· {{ behaviorLabel(rule.behavior) }}</span>
              <span>· {{ sourceLabel(rule.source) }}</span>
            </li>
          </ul>
        </div>
      </section>

      <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('permissions.rulesTitle') }}</h2>
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('permissions.mode') }}: {{ modeLabel(mode) }}</span>
        </div>
        <div class="mt-3 flex flex-wrap gap-2">
          <button
            v-for="option in sourceFilters"
            :key="option"
            type="button"
            class="rounded-full border px-3 py-1 text-[11px]"
            :class="sourceFilter === option
              ? 'border-[var(--forebrain-brand-border-strong)] text-[var(--forebrain-text)]'
              : 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'"
            @click="setSourceFilter(option)"
          >
            {{ option === '' ? t('permissions.allSources') : sourceLabel(option) }}
          </button>
        </div>
        <div v-if="loading" class="py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
        <ul v-else-if="rules.length" class="mt-3 space-y-1">
          <li
            v-for="(rule, idx) in rules"
            :key="`${rule.source}-${rule.toolName}-${rule.ruleContent}-${idx}`"
            class="flex flex-wrap items-center gap-2 rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface-soft)]"
          >
            <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="behaviorClass(rule.behavior)">
              {{ behaviorLabel(rule.behavior) }}
            </span>
            <span class="font-mono text-[var(--forebrain-text)]">{{ rule.toolName || '*' }}</span>
            <span v-if="rule.ruleContent" class="font-mono text-[var(--forebrain-text-2)]">{{ rule.ruleContent }}</span>
            <span class="ml-auto text-[11px] text-[var(--forebrain-muted-text)]">{{ sourceLabel(rule.source) }}</span>
          </li>
        </ul>
        <p v-else class="mt-3 rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
          {{ t('common.empty') }}
        </p>
      </section>
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
import { getErrorMessage, forebrainApi, type PermissionExplainResponse } from '@/lib/api'
import { useI18n } from '@/locales'

type RuleRow = {
  source?: string
  behavior?: string
  toolName?: string
  ruleContent?: string
}

const { t } = useI18n()
const rules = ref<RuleRow[]>([])
const mode = ref('')
const loading = ref(false)
const error = ref('')
const sourceFilter = ref('')
const sourceFilters = ['', 'localSettings', 'projectSettings', 'session']
const probe = reactive({ toolName: '', input: '' })
const explain = ref<PermissionExplainResponse | null>(null)
const verifying = ref(false)

/** Where a rule comes from, in words. */
function sourceLabel(source?: string): string {
  switch (source) {
    case 'localSettings':
      return t('permissions.source.localSettings')
    case 'projectSettings':
      return t('permissions.source.projectSettings')
    case 'session':
      return t('permissions.source.session')
    default:
      return source || '—'
  }
}

/** When the runtime asks before acting, in words. */
function modeLabel(mode?: string): string {
  switch (mode) {
    case 'on-request':
      return t('permissions.approval.onRequest')
    case 'never':
      return t('permissions.approval.never')
    case 'unless-trusted':
      return t('permissions.approval.unlessTrusted')
    case 'granular':
      return t('permissions.approval.granular')
    default:
      return mode || '—'
  }
}

/** What a rule does, in words. */
function behaviorLabel(behavior?: string): string {
  switch (behavior) {
    case 'allow':
      return t('permissions.behavior.allow')
    case 'deny':
      return t('permissions.behavior.deny')
    case 'ask':
      return t('permissions.behavior.ask')
    default:
      return behavior || '—'
  }
}

function behaviorClass(behavior?: string): string {
  switch (String(behavior ?? '').toLowerCase()) {
    case 'allow':
      return 'border-[var(--forebrain-brand-border-strong)] text-[var(--forebrain-brand-1)]'
    case 'deny':
      return 'border-[rgba(160,70,70,0.36)] text-[var(--forebrain-danger)]'
    default:
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'
  }
}

function setSourceFilter(next: string) {
  sourceFilter.value = next
  void loadRules()
}

async function loadRules() {
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.permissionsRules(
      sourceFilter.value ? { source: sourceFilter.value } : undefined,
    )
    mode.value = res.mode
    rules.value = res.rules as RuleRow[]
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

onMounted(loadRules)
</script>

<style scoped>
.forebrain-rule-mark {
  height: 0.375rem;
  width: 0.375rem;
  border-radius: 999px;
  background: var(--forebrain-brand-1);
}
</style>
