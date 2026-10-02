<template>
  <div class="mx-auto max-w-4xl">
    <CronView :project-id="projectId" scope="project" />
  </div>
</template>

<script setup lang="ts">
import CronView from '@/views/CronView.vue'

/**
 * The project's standing work: the same cron surface the agent page shows,
 * scoped to this project — its list, its jobs, its schedule builder. The
 * scope badge and description follow the project boundary, not the agent's.
 */
defineProps<{ project: unknown; projectId: string }>()
</script>
