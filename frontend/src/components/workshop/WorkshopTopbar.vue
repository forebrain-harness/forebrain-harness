<template>
  <div v-if="part === 'leading'" class="flex min-w-0 items-center gap-2">
    <button
      type="button"
      class="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]"
      :title="t('workshop.backToSkills')"
      :aria-label="t('workshop.backToSkills')"
      data-testid="workshop-back"
      @click="emit('back')"
    >
      <ArrowLeft class="h-4 w-4" aria-hidden="true" />
    </button>
    <nav class="flex min-w-0 items-center gap-1 text-[13px]" :aria-label="t('workshop.breadcrumb')">
      <button type="button" class="text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)]" data-testid="workshop-crumb-skills" @click="emit('back')">
        {{ t('skills.title') }}
      </button>
      <ChevronRight class="h-3.5 w-3.5 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
      <span class="truncate" :class="hasSkill ? 'font-mono' : ''" data-testid="workshop-crumb-leaf">{{ label }}</span>
    </nav>
    <template v-if="hasSkill">
      <BadgeComponent v-if="state === 'draft'" variant="warning">{{ t('skills.draft') }}</BadgeComponent>
      <template v-else>
        <BadgeComponent variant="gray">{{ `v${version}` }}</BadgeComponent>
        <BadgeComponent v-if="state === 'pending'" variant="warning">{{ t('skills.unpublished') }}</BadgeComponent>
      </template>
    </template>
    <button
      v-if="scope === 'project' && projectName"
      type="button"
      class="inline-flex items-center gap-1 rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]"
      data-testid="workshop-project-chip"
      @click="emit('open-project')"
    >
      <FolderGit2 class="h-3.5 w-3.5" aria-hidden="true" />
      {{ t('workshop.projectChip', { name: projectName }) }}
    </button>
    <span v-if="model" class="truncate text-[12px] text-[var(--forebrain-muted-text)]" data-testid="workshop-model">{{ model }}</span>
  </div>

  <div v-else class="flex items-center gap-1">
    <SessionHeartbeat v-if="sessionId" :session-id="sessionId" />
    <button
      type="button"
      class="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]"
      :title="t('workshop.newSession')"
      :aria-label="t('workshop.newSession')"
      data-testid="workshop-new-session"
      @click="emit('new-session')"
    >
      <MessageSquarePlus class="h-4 w-4" aria-hidden="true" />
    </button>
    <button
      type="button"
      class="forebrain-btn forebrain-btn-ghost text-xs"
      :aria-pressed="workbenchOpen"
      data-testid="workshop-workbench-toggle"
      @click="emit('toggle-workbench')"
    >
      <PanelRight class="h-4 w-4" aria-hidden="true" />
      {{ t('chat.workbench') }}
      <span v-if="runningCount" class="ml-1 rounded-full bg-[var(--forebrain-brand-soft)] px-1.5 text-[11px] text-[var(--forebrain-brand-1)]">{{ runningCount }}</span>
    </button>
    <button
      v-if="hasSkill"
      type="button"
      class="forebrain-btn forebrain-btn-primary text-xs"
      :disabled="!canPublish"
      :title="canPublish ? undefined : t('workshop.noDraftToPublish')"
      data-testid="workshop-publish"
      @click="emit('publish')"
    >
      {{ t('workshop.publish', { version: publishVersion }) }}
    </button>
  </div>
</template>

<script setup lang="ts">
/**
 * The workshop page's contribution to the shell's top bar: the leading half is
 * the back link, the breadcrumb, the version chip and (for a project workshop)
 * the project chip; the trailing half is the heartbeat, the new-session
 * button, the workbench toggle and the publish button. The shell still draws
 * its own "new agent" and language controls after the trailing half.
 */
import { ArrowLeft, ChevronRight, FolderGit2, MessageSquarePlus, PanelRight } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import SessionHeartbeat from '@/components/chat/SessionHeartbeat.vue'
import { useI18n } from '@/locales'

withDefaults(defineProps<{
  part: 'leading' | 'trailing'
  label: string
  hasSkill: boolean
  version: number
  state: string
  scope: 'agent' | 'project'
  projectName?: string
  model?: string
  sessionId?: string
  workbenchOpen: boolean
  runningCount?: number
  publishVersion: string
  canPublish: boolean
}>(), { projectName: '', model: '', sessionId: '', runningCount: 0 })

const emit = defineEmits<{
  back: []
  'new-session': []
  'toggle-workbench': []
  publish: []
  'open-project': []
}>()

const { t } = useI18n()
</script>
