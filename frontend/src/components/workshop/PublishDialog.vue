<template>
  <ForebrainModal :open="open" size="sm" :title="t('workshop.publishTitle', { name: skillName, version: currentVersion + 1 })" @close="emit('cancel')">
    <div>
      <div class="mb-1 text-[12px] text-[var(--forebrain-text-2)]">{{ currentVersion ? t('workshop.publishChangesSince', { v: currentVersion }) : t('workshop.publishFirst') }}</div>
      <div class="space-y-0.5">
        <div v-for="file in files" :key="file.path" class="flex items-center justify-between font-mono text-[11px] text-[var(--forebrain-text-2)]">
          <span class="truncate">{{ file.path }}</span>
          <span class="shrink-0 pl-2">{{ diffCounts(file) }}</span>
        </div>
        <div v-if="!files.length" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.noChanges') }}</div>
      </div>
    </div>
    <label class="block">
      <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.releaseNote') }}</span>
      <Input v-model="note" class="forebrain-field h-auto w-full" :placeholder="t('workshop.releaseNotePlaceholder')" data-testid="workshop-publish-note" />
    </label>
    <span class="text-[11px] text-[var(--forebrain-muted-text)]">
      {{ currentVersion ? t('workshop.publishHint', { next: currentVersion + 1, cur: currentVersion }) : t('workshop.publishFirstHint') }}
    </span>
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    <template #footer>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" :disabled="busy" @click="emit('cancel')">{{ t('common.cancel') }}</Button>
      <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="busy" data-testid="workshop-publish-confirm" @click="confirm">{{ busy ? t('common.loading') : t('workshop.publishAction') }}</Button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/** The publish dialog: what changed since the current version, an optional
 *  release note, and the note that running sessions keep the old version. */
import { ref, watch } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import forebrainApi, { getErrorMessage, type SkillFileDiff } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  open: boolean
  skillName: string
  projectId?: string
  currentVersion: number
}>(), { projectId: '' })

const emit = defineEmits<{ published: [version: number]; cancel: [] }>()
const { t } = useI18n()

const files = ref<SkillFileDiff[]>([])
const note = ref('')
const busy = ref(false)
const error = ref('')

watch(
  () => props.open,
  async (open) => {
    if (!open) return
    note.value = ''
    error.value = ''
    files.value = []
    if (props.currentVersion > 0) {
      try {
        const res = await forebrainApi.skillDiff(props.skillName, `v${props.currentVersion}`, 'draft', props.projectId)
        files.value = res.files ?? []
      } catch (e: unknown) {
        error.value = getErrorMessage(e)
      }
    }
  },
  { immediate: true },
)

function diffCounts(file: SkillFileDiff): string {
  if (file.status === 'added') return `+${file.added}`
  if (file.status === 'removed') return `−${file.removed}`
  return `+${file.added} −${file.removed}`
}

async function confirm() {
  busy.value = true
  error.value = ''
  try {
    const res = await forebrainApi.skillPublish(props.skillName, note.value.trim(), props.projectId)
    emit('published', res.version)
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>
