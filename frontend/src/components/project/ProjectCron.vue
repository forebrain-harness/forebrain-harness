<template>
  <div class="mx-auto max-w-4xl">
    <CronView :project-id="projectId" />
  </div>
</template>

<script setup lang="ts">
import CronView from '@/views/CronView.vue'

/**
 * The project's standing work: the cron surface, scoped to this project — its
 * list of jobs, its schedule builder, and the fires they run. A job belongs to
 * the project, so there is no agent-wide list to fall back to.
 *
 * The width is every other project tab's (plan 075): the table inside owns the
 * space it needs, so nothing here caps it narrower than its neighbours.
 */
defineProps<{ project: unknown; projectId: string }>()
</script>
