<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <div class="flex items-center gap-2">
      <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('memories.projectTitle') }}</div>
      <ScopeBadge type="project" :label="t('scope.project')" />
    </div>
    <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.projectFilesDescription') }}</p>

    <!-- A project with no project key shares no memory directory of its own;
         there is nothing to manage here. -->
    <div v-if="!project" class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!hasScope" class="rounded-2xl border border-dashed border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-6 py-10 text-center">
      <p class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('memories.noProjectScope') }}</p>
    </div>
    <MemoryFilesPanel v-else scope="project" :project-id="projectId" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import MemoryFilesPanel from '@/components/memory/MemoryFilesPanel.vue'
import type { ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The project's own memory scope: exactly the files this project's sessions
 * recall, never another project's. Projects without their own key have no
 * separate scope and say so instead of listing a neighbour's. The key comes
 * from the project row the shell already loaded (and whose load errors the
 * shell reports).
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const hasScope = computed(() => Boolean(props.project?.projectKey))
</script>
