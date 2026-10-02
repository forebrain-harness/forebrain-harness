<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <div class="flex items-center gap-2">
      <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('memories.projectTitle') }}</div>
      <span class="scope-badge">{{ t('scope.project') }}</span>
    </div>
    <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.projectFilesDescription') }}</p>

    <!-- A project with no project key shares no memory directory of its own;
         there is nothing to manage here. -->
    <div v-if="!hasScope" class="rounded-2xl border border-dashed border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-6 py-10 text-center">
      <p class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('memories.noProjectScope') }}</p>
    </div>
    <MemoryFilesPanel v-else scope="project" :project-id="projectId" />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import MemoryFilesPanel from '@/components/memory/MemoryFilesPanel.vue'
import forebrainApi from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The project's own memory scope: exactly the files this project's sessions
 * recall, never another project's. Projects without their own key have no
 * separate scope and say so instead of listing a neighbour's.
 */
const props = defineProps<{ project: unknown; projectId: string }>()

const { t } = useI18n()

const hasScope = ref(false)

onMounted(async () => {
  try {
    const record = (await forebrainApi.projectGet(props.projectId)) as unknown as { projectKey?: string }
    hasScope.value = Boolean(record?.projectKey)
  } catch {
    hasScope.value = false
  }
})
</script>
