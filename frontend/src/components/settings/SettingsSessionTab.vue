<template>
  <!-- 会话 (plan 191 §0.13, §4.3): four groups — compaction, retries and
       timeouts, the context's headroom, the input guardrails, and the
       token estimator. The handoff prompt's 「恢复默认」 only puts the built-in
       text back on screen; saving is a separate, deliberate click. -->
  <SettingsTabForm :tab="tab" :form="form" />
</template>

<script setup lang="ts">
/**
 * The conversation's own settings — everything that shapes a turn: when the
 * context is compacted, how patient the transport is, how much room the context
 * keeps before it warns, what the input rail refuses, and how tokens are
 * estimated. The rows come from the schema; the tab names the surface.
 */
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
