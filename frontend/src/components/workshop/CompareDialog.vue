<template>
  <ForebrainModal :open="open" size="xl" :title="t('workshop.compareTitle', { name: skillName, from, to: toLabel })" @close="emit('cancel')">
    <div class="cmp grid grid-cols-[200px_minmax(0,1fr)] gap-3">
      <div class="space-y-0.5">
        <button
          v-for="file in files"
          :key="file.path"
          type="button"
          class="flex w-full items-center justify-between rounded-md px-2 py-1 text-left font-mono text-[11px]"
          :class="file.path === selected ? 'bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]' : 'text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]'"
          @click="selected = file.path"
        >
          <span class="truncate">{{ file.path }}</span>
          <span class="shrink-0 pl-2">{{ diffCounts(file) }}</span>
        </button>
        <p v-if="!files.length" class="px-2 py-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.noChanges') }}</p>
      </div>
      <div class="min-h-[200px] rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] p-2 font-mono text-[11px] leading-relaxed">
        <template v-if="currentFile">
          <div class="text-[var(--forebrain-text-2)]">{{ currentFile.path }}</div>
          <div v-if="currentFile.status === 'added'" class="text-[var(--forebrain-success)]">{{ t('workshop.diffAdded', { n: currentFile.added }) }}</div>
          <div v-else-if="currentFile.status === 'removed'" class="text-[var(--forebrain-danger)]">{{ t('workshop.diffRemoved', { n: currentFile.removed }) }}</div>
          <div v-else>
            <div class="text-[var(--forebrain-success)]">{{ t('workshop.diffAdded', { n: currentFile.added }) }}</div>
            <div class="text-[var(--forebrain-danger)]">{{ t('workshop.diffRemoved', { n: currentFile.removed }) }}</div>
          </div>
        </template>
      </div>
    </div>
    <template #footer>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="emit('cancel')">{{ t('common.close') }}</Button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * The compare dialog: the files that differ between two snapshots (a version
 * or the draft) with their added/removed line counts. The version store's diff
 * route reports line counts per file, not the lines themselves, so the right
 * pane summarises the selected file's changes rather than pasting its body.
 */
import { computed, ref, watch } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import forebrainApi, { getErrorMessage, type SkillFileDiff } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  open: boolean
  skillName: string
  projectId?: string
  from: string
  to: string
}>(), { projectId: '' })

const emit = defineEmits<{ cancel: [] }>()
const { t } = useI18n()

const files = ref<SkillFileDiff[]>([])
const selected = ref('')
const error = ref('')

const toLabel = computed(() => (props.to === 'draft' ? t('workshop.draftWord') : props.to))
const currentFile = computed<SkillFileDiff | null>(() => files.value.find((file) => file.path === selected.value) ?? null)

watch(
  () => [props.open, props.from, props.to],
  async ([open]) => {
    if (!open) return
    error.value = ''
    files.value = []
    selected.value = ''
    try {
      const res = await forebrainApi.skillDiff(props.skillName, props.from, props.to, props.projectId)
      files.value = res.files ?? []
      selected.value = files.value[0]?.path ?? ''
    } catch (e: unknown) {
      error.value = getErrorMessage(e)
    }
  },
  { immediate: true },
)

function diffCounts(file: SkillFileDiff): string {
  if (file.status === 'added') return `+${file.added}`
  if (file.status === 'removed') return `−${file.removed}`
  return `+${file.added} −${file.removed}`
}
</script>
