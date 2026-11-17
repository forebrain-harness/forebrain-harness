<template>
  <!-- 子代理 (plan 191 §0.14, §4.3): the global switch and the parallel limit,
       both global defaults a primary agent may override on its own page. -->
  <SettingsTabForm :tab="tab" :form="form" />
  <!-- The system subagents the settings page configures, hand-written rather
       than schema-driven, so they sit beside the tab's form instead of inside
       SETTINGS_FORMS — see the note at the top of settingsSchema.ts. Two of
       them: the /goal check (plan 299) and the exit-plan reviewer (plan 313). -->
  <GoalCheckModelGroup :form="form" />
  <PlanReviewerModelGroup :form="form" />
  <!-- The install-wide shared subagent registry and the four built-in types'
       default model rows (plans 314, 315). A registry rather than a group of
       scalar settings, so it keeps its own state and writes through the
       subagent endpoints instead of the settings form. -->
  <GlobalSubagentsGroup />
</template>

<script setup lang="ts">
/**
 * The subagent defaults: whether the model may dispatch subagents at all, and
 * how many run at once. Both are the values a primary agent starts from — its
 * own page overrides them — which is what each row's explanation says.
 *
 * The two system subagents' own models are configured here too: the /goal check
 * (plan 299) and the exit-plan reviewer (plan 313) are dispatched by the system
 * rather than by a primary agent, so each is one global row for the whole
 * install and belongs beside these defaults.
 *
 * The install-wide shared subagent registry sits below them (plan 314): one
 * definition every primary agent may enable, plus the four built-in types'
 * default model rows. Its group owns its state, because a registry is CRUD
 * rather than a form.
 */
import GlobalSubagentsGroup from '@/components/settings/GlobalSubagentsGroup.vue'
import GoalCheckModelGroup from '@/components/settings/GoalCheckModelGroup.vue'
import PlanReviewerModelGroup from '@/components/settings/PlanReviewerModelGroup.vue'
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
