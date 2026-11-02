<template>
  <!-- 子代理 (plan 191 §0.14, §4.3): the global switch and the parallel limit,
       both global defaults a primary agent may override on its own page. -->
  <SettingsTabForm :tab="tab" :form="form" />
</template>

<script setup lang="ts">
/**
 * The subagent defaults: whether the model may dispatch subagents at all, and
 * how many run at once. Both are the values a primary agent starts from — its
 * own page overrides them — which is what each row's explanation says.
 */
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
