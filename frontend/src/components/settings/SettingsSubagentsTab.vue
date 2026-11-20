<template>
  <!-- 子代理 (plan 191 §0.14, §4.3): the global switch and the parallel limit,
       both global defaults a primary agent may override on its own page. -->
  <SettingsTabForm :tab="tab" :form="form" />
  <!-- The system subagents the settings page configures, hand-written rather
       than schema-driven, so they sit beside the tab's form instead of inside
       SETTINGS_FORMS — see the note at the top of settingsSchema.ts. Two of
       them: the /goal check (plan 299) and the exit-plan reviewer (plan 313). -->
  <!-- The built-in subagent types' install-wide rows (P4): the four public
       types' per-type rows and the two system types' model rows, one card —
       and the install-wide shared subagent registry beside them (plans 314,
       315). The registry keeps its own state and writes through the subagent
       endpoints instead of the settings form. -->
  <GlobalSubagentsGroup :form="form" />
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
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
