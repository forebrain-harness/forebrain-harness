<template>
  <!-- 审批与沙箱 (plan 191 §0.12, §4.3): the preset with its two described
       choices and the note saying who it applies to, the sandbox's writable
       workspace that unfolds only while 默认 is chosen, and the batch project
       group whose lone switch keeps its name. -->
  <SettingsTabForm :tab="tab" :form="form" />
</template>

<script setup lang="ts">
/**
 * The approval preset replaced the approval policy and the sandbox mode (plan
 * 203 §0): the one choice a user makes, with the workspace sandbox's own
 * settings folded under it and the batch projects' default beside it. Every row
 * comes from the schema; the tab exists so the page can name the surface.
 */
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
