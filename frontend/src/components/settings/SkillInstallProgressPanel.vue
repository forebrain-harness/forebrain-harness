<template>
  <div v-if="events.length" class="mt-3 space-y-2 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-3">
    <div class="text-[12px] font-medium text-[var(--forebrain-text)]">{{ t('settings.installProgress') }}</div>
    <div
      v-for="event in events"
      :key="`${event.taskId}-${event.phase}-${event.createdAt}`"
      class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2"
    >
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="text-[12px] font-medium text-[var(--forebrain-text)]">{{ event.phaseLabel || event.message || event.phase || t('common.processing') }}</div>
        <div class="text-[11px] font-mono text-[var(--forebrain-muted-text)]">{{ event.progressText }}</div>
      </div>
      <div class="mt-2 h-2 overflow-hidden rounded-full bg-[var(--forebrain-bg-alt)]">
        <div class="h-full rounded-full bg-[var(--forebrain-brand-1)] transition-all" :style="{ width: event.progressBarWidth }" />
      </div>
      <div class="mt-2 text-[11px] text-[var(--forebrain-muted-text)]">
        <span class="font-mono">{{ event.taskId }}</span>
        <template v-if="event.installedNames.length"> · {{ event.installedNames.join(', ') }}</template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from '@/locales'

export interface SkillInstallEventViewModel {
  taskId: string
  phase: string
  phaseLabel: string
  message: string
  progress: number
  progressText: string
  progressBarWidth: string
  installedNames: string[]
  createdAt: number
}

defineProps<{
  events: SkillInstallEventViewModel[]
}>()

const { t } = useI18n()
</script>
