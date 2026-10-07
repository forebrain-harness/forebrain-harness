<template>
  <div class="mt-3 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2" data-testid="permission-explain-result">
    <div class="flex flex-wrap items-center gap-2 text-[12px]">
      <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="permissionBehaviorClass(explain.decision?.behavior)">
        {{ explain.decision?.behavior ? permissionBehaviorLabel(explain.decision.behavior) : t('common.none') }}
      </span>
      <span class="text-[var(--forebrain-muted-text)]">{{ t('permissions.mode') }}: {{ permissionModeLabel(explain.decision?.mode) }}</span>
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
        <span>· {{ permissionBehaviorLabel(rule.behavior) }}</span>
        <span>· {{ permissionSourceLabel(rule.source) }}</span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import type { PermissionExplainResponse } from '@/lib/api'
import {
  permissionBehaviorClass,
  permissionBehaviorLabel,
  permissionModeLabel,
  permissionSourceLabel,
} from '@/lib/permissionLabels'
import { useI18n } from '@/locales'

/**
 * How the engine judged one probed call: the decision, the mode it was taken
 * under, and every rule it weighed with the matching one marked — the same
 * reading on the agent's permission page and in a project's permission tab.
 */
defineProps<{ explain: PermissionExplainResponse }>()

const { t } = useI18n()
</script>

<style scoped>
.forebrain-rule-mark {
  height: 0.375rem;
  width: 0.375rem;
  border-radius: 999px;
  background: var(--forebrain-brand-1);
}
</style>
