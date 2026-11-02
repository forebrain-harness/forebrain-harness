<template>
  <!-- 定时任务 (plan 191 §0.18, §4.3): how long a scheduled run's history is
       kept. The jobs themselves live on the 定时任务 page, not here. -->
  <SettingsTabForm :tab="tab" :form="form" />
</template>

<script setup lang="ts">
/**
 * The install's scheduled-task setting: the retention that decides how long a
 * run's conversation and history record survive its last activity.
 */
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
