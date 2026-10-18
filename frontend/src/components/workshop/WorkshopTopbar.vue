<template>
  <div v-if="part === 'leading'" class="forebrain-topbar-crumb">
    <nav class="flex min-w-0 items-center gap-1 text-[13px]" :aria-label="t('workshop.breadcrumb')">
      <button type="button" class="text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)]" data-testid="workshop-crumb-skills" @click="emit('back')">
        {{ t('skills.title') }}
      </button>
      <ChevronRight class="h-3.5 w-3.5 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
      <span
        class="forebrain-topbar-name"
        :class="hasSkill ? 'font-mono' : ''"
        data-testid="workshop-crumb-leaf"
        v-tip:bottom.trunc="label"
      >{{ label }}</span>
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
      class="chat-project-tag"
      data-testid="workshop-project-chip"
      v-tip:bottom="t('chat.openProjectSpace')"
      @click="emit('open-project')"
    >
      <FolderGit2 aria-hidden="true" />
      {{ projectName }}
    </button>
  </div>

  <div v-else class="flex items-center gap-1">
    <SessionHeartbeat v-if="sessionId" :session-id="sessionId" />
    <button
      type="button"
      class="forebrain-chip-btn forebrain-chip-btn--sq"
      :aria-label="t('workshop.newSession')"
      data-testid="workshop-new-session"
      v-tip:bottom="t('workshop.newSession')"
      @click="emit('new-session')"
    >
      <MessageSquarePlus aria-hidden="true" />
    </button>
    <button
      type="button"
      class="forebrain-chip-btn"
      :aria-pressed="workbenchOpen"
      data-testid="workshop-workbench-toggle"
      @click="emit('toggle-workbench')"
    >
      <PanelRight aria-hidden="true" />
      {{ t('chat.workbench') }}
      <span v-if="runningCount" class="forebrain-chip-count">{{ runningCount }}</span>
    </button>
    <button
      v-if="hasSkill"
      type="button"
      class="forebrain-btn forebrain-btn-primary text-xs"
      :disabled="!canPublish"
      data-testid="workshop-publish"
      v-tip:top="canPublish ? '' : t('workshop.noDraftToPublish')"
      @click="emit('publish')"
    >
      {{ t('workshop.publish', { version: publishVersion }) }}
    </button>
  </div>
</template>

<script setup lang="ts">
/**
 * The workshop page's contribution to the shell's top bar: the leading half is
 * the breadcrumb, the version chip and (for a project workshop) the project
 * chip; the trailing half is the heartbeat, the new-session button, the
 * workbench toggle and the publish button. The shell still draws its own "new
 * agent" and language controls after the trailing half. The breadcrumb's own
 * "技能" is the way back — the back arrow it carried was the same trip twice
 * (owner 2026-10-18).
 */
import { ChevronRight, FolderGit2, MessageSquarePlus, PanelRight } from 'lucide-vue-next'
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
  sessionId?: string
  workbenchOpen: boolean
  runningCount?: number
  publishVersion: string
  canPublish: boolean
}>(), { projectName: '', sessionId: '', runningCount: 0 })

const emit = defineEmits<{
  back: []
  'new-session': []
  'toggle-workbench': []
  publish: []
  'open-project': []
}>()

const { t } = useI18n()
</script>
