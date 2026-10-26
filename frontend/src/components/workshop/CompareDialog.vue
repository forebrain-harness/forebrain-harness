<template>
  <ForebrainModal :open="open" size="xl" :title="t('workshop.compareTitle', { name: skillName, from, to: toLabel })" @close="emit('cancel')">
    <p v-if="error" class="cmp-hint cmp-hint--error" data-testid="workshop-compare-error">{{ error }}</p>
    <div class="cmp" data-testid="workshop-compare">
      <div class="files">
        <button
          v-for="file in files"
          :key="file.path"
          type="button"
          :class="{ on: file.path === selected }"
          :data-compare-file="file.path"
          @click="selected = file.path"
        >
          <span class="p">{{ file.path }}</span><span class="n">{{ diffCounts(file) }}</span>
        </button>
        <p v-if="loaded && !files.length && !error" class="cmp-hint">{{ t('workshop.noChanges') }}</p>
      </div>
      <div class="pane" data-testid="workshop-compare-pane">
        <p v-if="fileLoading" class="cmp-hint">{{ t('common.loading') }}</p>
        <p v-else-if="fileError" class="cmp-hint cmp-hint--error">{{ fileError }}</p>
        <template v-else-if="shown">
          <p v-if="shown.binary" class="cmp-hint">{{ t('workshop.diffBinary') }}</p>
          <p v-else-if="shown.tooLarge" class="cmp-hint">{{ t('workshop.diffTooLarge') }}</p>
          <p v-else-if="!shown.unifiedDiff" class="cmp-hint">{{ t('workshop.diffUnchanged') }}</p>
          <DiffBlock v-else :rows="parseUnifiedDiff(shown.unifiedDiff)" :plain="shown.status === 'added'" :max-height="null" />
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
 * or the draft). The left pane lists them from skillDiff's file-level answer;
 * the right pane asks skillDiffFile for the selected file and draws its
 * unified diff with DiffBlock — the one diff style every surface draws
 * (owner 2026-10-25, diff 全站统一). A newly added file renders as plain
 * code, not a wall of green.
 */
import { computed, ref, watch } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import DiffBlock from '@/components/chat/DiffBlock.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import forebrainApi, { getErrorMessage, type SkillFileDiff } from '@/lib/api'
import { parseUnifiedDiff } from '@/lib/unifiedDiff'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  open: boolean
  skillName: string
  projectId?: string
  from: string
  to: string
  initialPath?: string
}>(), { projectId: '', initialPath: '' })

const emit = defineEmits<{ cancel: [] }>()
const { t } = useI18n()

const files = ref<SkillFileDiff[]>([])
const selected = ref('')
const error = ref('')
const loaded = ref(false)

const shown = ref<SkillFileDiff | null>(null)
const fileLoading = ref(false)
const fileError = ref('')

const toLabel = computed(() => (props.to === 'draft' ? t('workshop.draftWord') : props.to))

watch(
  () => [props.open, props.from, props.to, props.initialPath],
  async ([open]) => {
    if (!open) return
    error.value = ''
    files.value = []
    selected.value = ''
    loaded.value = false
    try {
      const res = await forebrainApi.skillDiff(props.skillName, props.from, props.to, props.projectId)
      files.value = res.files ?? []
      const wanted = files.value.some((file) => file.path === props.initialPath)
        ? props.initialPath
        : files.value[0]?.path ?? ''
      // A reopen can land on the file that was already selected, in which
      // case the selected watcher below stays quiet — load it here instead.
      if (selected.value === wanted) void loadFile()
      else selected.value = wanted
    } catch (e: unknown) {
      error.value = getErrorMessage(e)
    } finally {
      loaded.value = true
    }
  },
  { immediate: true },
)

let fileGeneration = 0
async function loadFile() {
  const path = selected.value
  if (!path) {
    shown.value = null
    fileLoading.value = false
    return
  }
  const gen = ++fileGeneration
  fileLoading.value = true
  fileError.value = ''
  try {
    const file = await forebrainApi.skillDiffFile(props.skillName, props.from, props.to, path, props.projectId)
    if (gen === fileGeneration) shown.value = file
  } catch (cause) {
    if (gen === fileGeneration) {
      shown.value = null
      fileError.value = getErrorMessage(cause)
    }
  } finally {
    if (gen === fileGeneration) fileLoading.value = false
  }
}

watch(selected, () => void loadFile())

function diffCounts(file: SkillFileDiff): string {
  if (file.status === 'added') return `+${file.added}`
  if (file.status === 'removed') return `−${file.removed}`
  return `+${file.added} −${file.removed}`
}
</script>

<style scoped>
/* The prototype's 版本对比 panel (app-preview.html :1319-1324): the file list
   and the diff share one frame, and the diff panel is the frame's right
   cell. */
.cmp { display: grid; grid-template-columns: 200px minmax(0, 1fr); border: 1px solid var(--forebrain-divider); border-radius: 8px; overflow: hidden; height: 300px; }
.files { border-right: 1px solid var(--forebrain-divider); background: var(--forebrain-bg-alt); padding: 4px; display: grid; gap: 1px; align-content: start; overflow: auto; }
.files button { border: 0; background: transparent; text-align: left; padding: 6px 8px; border-radius: 6px; font-family: var(--forebrain-mono-font); font-size: 11.5px; cursor: pointer; color: var(--forebrain-text-2); display: flex; justify-content: space-between; gap: 6px; }
.files button .p { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.files button .n { flex-shrink: 0; color: var(--forebrain-muted-text); }
.files button.on { background: var(--forebrain-brand-soft); color: var(--forebrain-brand-1); }
.pane { min-width: 0; height: 100%; overflow: auto; box-sizing: border-box; }
/* The pane's own frame is the border (the prototype's .cmp .diff { border: 0 }). */
.pane :deep(.fc-b) { border-top: 0; min-height: 100%; box-sizing: border-box; }
.cmp-hint { padding: 8px 10px; font-size: 12px; color: var(--forebrain-muted-text); }
.cmp-hint--error { color: var(--forebrain-danger); }
</style>
