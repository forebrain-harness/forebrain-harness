<template>
  <!-- 子代理 (plan 191 §0.14, §4.3): the global switch and the parallel limit,
       both global defaults a primary agent may override on its own page. -->
  <SettingsTabForm :tab="tab" :form="form" />
  <!-- The one system subagent the settings page configures (plan 299). It is
       hand-written rather than schema-driven, so it sits beside the tab's form
       instead of inside SETTINGS_FORMS — see the note at the top of
       settingsSchema.ts. -->
  <GoalCheckModelGroup :form="form" />
</template>

<script setup lang="ts">
/**
 * The subagent defaults: whether the model may dispatch subagents at all, and
 * how many run at once. Both are the values a primary agent starts from — its
 * own page overrides them — which is what each row's explanation says.
 *
 * The /goal check's own model is configured here too (plan 299): the check is
 * dispatched by the system rather than by a primary agent, so it is one global
 * row for the whole install and belongs beside these defaults.
 */
import GoalCheckModelGroup from '@/components/settings/GoalCheckModelGroup.vue'
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
