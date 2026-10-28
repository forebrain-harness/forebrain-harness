<template>
  <div class="mx-auto max-w-4xl">
    <CronView :project-id="projectId" :project-type="project?.projectType" />
  </div>
</template>

<script setup lang="ts">
import CronView from '@/views/CronView.vue'
import type { ProjectRecord } from '@/lib/api'

/**
 * The project's standing work: the cron surface, scoped to this project — its
 * list of jobs, its schedule builder, and the fires they run. A job belongs to
 * the project, so there is no agent-wide list to fall back to.
 *
 * The width is every other project tab's (plan 075): the table inside owns the
 * space it needs, so nothing here caps it narrower than its neighbours.
 *
 * The project's type comes along because a batch project's job form has no
 * delivery channel: its runs are announced by the project's 任务通知 (plan 153).
 */
defineProps<{ project: ProjectRecord | null; projectId: string }>()
</script>
