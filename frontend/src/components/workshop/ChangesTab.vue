<template>
  <div class="space-y-2" data-testid="workshop-changes">
    <div class="flex items-center justify-between">
      <span class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('chat.benchTabChanges') }}</span>
      <span v-if="hasDraft && base" class="text-[11px] text-[var(--forebrain-muted-text)]">{{ base ? t('workshop.changesSince', { v: base }) : '' }}</span>
    </div>
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="!skillName" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.tabNeedsSkill') }}</p>
    <p v-else-if="!files.length" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.noChanges') }}</p>
    <div v-else class="space-y-1">
      <div v-for="file in files" :key="file.path" class="flex items-center justify-between rounded-md border border-[var(--forebrain-divider)] p-2">
        <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-[var(--forebrain-text)]">{{ file.path }}</span>
        <span class="shrink-0 pl-2 text-[11px] text-[var(--forebrain-muted-text)]">{{ counts(file) }}</span>
        <button v-if="base" type="button" class="forebrain-btn forebrain-btn-ghost ml-1 text-xs" @click="emit('compare', { from: `v${base}`, to: 'draft' })">{{ t('workshop.compare') }}</button>
      </div>
    </div>
    <span class="block truncate font-mono text-[11px] text-[var(--forebrain-muted-text)]" v-tip:top.trunc="draftDir">{{ draftDir }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * The workshop's changes tab: the draft against the current version — not the
 * conversation's own file tools. The diff comes from the version store, so it
 * lists exactly what a publish would carry.
 */
import { onMounted, ref, watch } from 'vue'
import forebrainApi, { getErrorMessage, type SkillFileDiff } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{ skillName: string; projectId?: string; editable: boolean }>(), { projectId: '' })
const emit = defineEmits<{ compare: [payload: { from: string; to: string }] }>()
const { t } = useI18n()

const files = ref<SkillFileDiff[]>([])
const base = ref(0)
const hasDraft = ref(false)
const draftDir = ref('')
const error = ref('')

async function load() {
  if (!props.skillName) {
    files.value = []
    return
  }
  error.value = ''
  try {
    const res = await forebrainApi.skillVersions(props.skillName, props.projectId)
    base.value = res.state.draftBase ?? 0
    hasDraft.value = res.state.hasDraft
    draftDir.value = res.state.draftDir ?? ''
    if (res.state.hasDraft && base.value > 0) {
      const diff = await forebrainApi.skillDiff(props.skillName, `v${base.value}`, 'draft', props.projectId)
      files.value = diff.files ?? []
    } else {
      files.value = []
    }
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

function counts(file: SkillFileDiff): string {
  if (file.status === 'added') return `+${file.added}`
  if (file.status === 'removed') return `−${file.removed}`
  return `+${file.added} −${file.removed}`
}

defineExpose({ reload: load })
watch(() => [props.skillName, props.projectId], load)
onMounted(load)
</script>
