<template>
  <!-- The confirm only appears once the preview says what the delete would
       take with it; until then (and while a run is in flight) the same modal
       explains why nothing is being deleted yet. -->
  <ConfirmDialog
    v-if="preview && !preview.running"
    :open="true"
    :title="t('projects.deleteModalTitle', { name: project.name })"
    :message="message"
    :confirm-label="t('projects.deleteConfirmButton')"
    danger
    :typed-confirm="project.name"
    @cancel="emit('cancel')"
    @confirm="emit('deleted')"
  />
  <ForebrainModal
    v-else
    :open="true"
    size="sm"
    :title="t('projects.deleteModalTitle', { name: project.name })"
    @close="emit('cancel')"
  >
    <p class="text-[13px] text-[var(--forebrain-text-2)]">{{ preview ? t('projects.deleteBusy') : t('common.loading') }}</p>
    <template #footer>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="emit('cancel')">{{ t('common.close') }}</button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * Deleting a project, asked the same way from the projects table and from the
 * project's own settings tab: the preview first says how many conversations
 * and cron jobs go with it and whether a run is in flight, then the confirm
 * asks for the project's name before the danger button wakes.
 *
 * The dialog owns the asking, not the deleting: `deleted` says the operator
 * typed the name and pressed the danger button, and whoever opened the dialog
 * performs the delete so a refusal lands on that page's own error line.
 */
import { computed, ref, watch } from 'vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { forebrainApi, getErrorMessage, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{ project: ProjectRecord }>()
const emit = defineEmits<{ deleted: []; cancel: [error?: string] }>()

const { t } = useI18n()

const preview = ref<{ sessions: number; cronJobs: number; running: boolean } | null>(null)

const message = computed(() => {
  const p = preview.value
  const sessions = p?.sessions ?? 0
  const jobs = p?.cronJobs ?? 0
  const head = jobs
    ? t('projects.deletePreviewWithJobs', { sessions, jobs })
    : t('projects.deletePreview', { sessions })
  return `${head}\n${t('projects.deleteModalDir')}：${props.project.root ?? ''}\n${t('projects.deleteModalHint')}`
})

async function loadPreview(): Promise<void> {
  preview.value = null
  try {
    const response = await forebrainApi.projectDeletePreview(props.project.id)
    preview.value = { sessions: response.sessions, cronJobs: response.cron_jobs, running: response.running }
  } catch (cause) {
    // Nothing can be said about what would go with it, so the dialog closes
    // and the page that opened it says why.
    emit('cancel', getErrorMessage(cause))
  }
}

watch(() => props.project.id, () => void loadPreview(), { immediate: true })
</script>
