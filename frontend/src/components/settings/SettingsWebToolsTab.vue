<template>
  <!-- 联网工具 (plan 191 §0.15, §4.3): the web search service and each of its
       keys, and the fetch service. A key only appears for the service that is
       chosen, and it is a credential: bullets and the last four characters
       until 更换 opens the box (§0.20). -->
  <SettingsTabForm :tab="tab" :form="form" />
</template>

<script setup lang="ts">
/**
 * The tools that reach the internet: which search service is used (or none), and
 * which service fetches a page. The rows come from the schema; the tab names
 * the surface.
 */
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()
</script>
