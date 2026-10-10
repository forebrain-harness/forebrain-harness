<template>
  <div class="flex h-full min-h-0 flex-col rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)]">
    <div class="flex flex-wrap items-center gap-2 border-b border-[var(--forebrain-divider)] px-4 py-3">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">
        {{ skillName ? t('workshop.skillPanelFor', { name: skillName }) : t('workshop.skillPanelEmpty') }}
      </div>
      <div class="ml-auto flex items-center gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading || !skillName" data-testid="workshop-refresh" @click="load">
          {{ t('common.refresh') }}
        </button>
        <button
          v-if="downloadUrlValue"
          type="button"
          class="forebrain-btn forebrain-btn-ghost text-xs"
          data-testid="workshop-download"
          @click="download"
        >
          {{ t('workshop.downloadZip') }}
        </button>
      </div>
    </div>

    <p v-if="error" class="px-4 pt-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <div v-if="!skillName" class="px-4 py-10 text-center text-[12px] text-[var(--forebrain-muted-text)]">
      {{ t('workshop.skillPanelEmptyHint') }}
    </div>
    <div v-else-if="loading" class="px-4 py-8 text-center text-[12px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <template v-else>
      <div class="flex min-h-0 flex-1">
        <ul class="w-52 shrink-0 overflow-y-auto border-r border-[var(--forebrain-divider)] p-2" data-testid="workshop-files">
          <li v-for="file in files" :key="file.path">
            <button
              type="button"
              class="w-full truncate rounded-lg px-2 py-1 text-left font-mono text-[11px]"
              :class="activePath === file.path
                ? 'bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]'
                : 'text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]'"
              :style="{ paddingLeft: `${8 + depthOf(file.path) * 10}px` }"
              :data-workshop-file="file.path"
              @click="open(file.path)"
            >{{ leafOf(file.path) }}</button>
          </li>
          <li v-if="!files.length" class="px-2 py-4 text-center text-[11px] text-[var(--forebrain-muted-text)]">{{ t('workshop.noFiles') }}</li>
        </ul>

        <div class="flex min-w-0 flex-1 flex-col">
          <div v-if="activePath" class="flex items-center gap-2 border-b border-[var(--forebrain-divider)] px-3 py-2">
            <span class="truncate font-mono text-[12px] text-[var(--forebrain-text)]">{{ activePath }}</span>
            <span v-if="savedNotice" class="text-[11px] text-[var(--forebrain-brand-1)]" data-testid="workshop-file-saved">{{ t('workshop.savedNotice') }}</span>
            <LockIcon v-if="readOnly" class="h-3.5 w-3.5 text-[var(--forebrain-muted-text)]" :title="t('workshop.builtinReadOnly')" />
            <button
              v-if="!readOnly"
              type="button"
              class="forebrain-btn forebrain-btn-primary ml-auto text-xs"
              :disabled="saving"
              data-testid="workshop-file-save"
              @click="save"
            >
              {{ saving ? t('common.loading') : t('common.save') }}
            </button>
          </div>
          <textarea
            v-model="draft"
            :disabled="loadingFile"
            :readonly="readOnly"
            class="min-h-0 flex-1 resize-none border-0 bg-[var(--forebrain-code-bg)] p-3 font-mono text-[12px] leading-relaxed text-[var(--forebrain-code-text)] outline-none"
            data-testid="workshop-file-editor"
          />
          <p v-if="readOnly" class="border-t border-[var(--forebrain-divider)] px-3 py-2 text-[11px] text-[var(--forebrain-muted-text)]">
            {{ t('workshop.builtinReadOnly') }}
          </p>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { LockIcon } from 'lucide-vue-next'
import forebrainApi, { getErrorMessage, saveSkillDownload, type SkillFileRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The skill side of the workshop: the directory of the skill the task is
 * building, as files. The conversation decides what the skill should be;
 * this panel is where its bytes live.
 */
const props = defineProps<{ skillName: string }>()

const { t } = useI18n()

const files = ref<SkillFileRecord[]>([])
const readOnly = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const activePath = ref('')
const draft = ref('')
const savedNotice = ref(false)
// The editor stays inert until the file's current bytes arrive: typing into a
// stale draft would silently save the old content over the user's edit.
const loadingFile = ref(false)

const downloadUrlValue = computed(() => (props.skillName ? `/api/skills/${encodeURIComponent(props.skillName)}/download` : ''))

watch(() => props.skillName, () => {
  files.value = []
  activePath.value = ''
  draft.value = ''
  void load()
}, { immediate: true })

async function load() {
  if (!props.skillName) return
  loading.value = true
  error.value = ''
  try {
    const data = await forebrainApi.skillFilesList(props.skillName)
    files.value = data.files.filter((file) => !file.isDir)
    readOnly.value = Boolean(data.readOnly)
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function depthOf(path: string): number {
  return path.split('/').length - 1
}

function leafOf(path: string): string {
  return path.split('/').pop() ?? path
}

async function open(path: string) {
  activePath.value = path
  draft.value = ''
  error.value = ''
  loadingFile.value = true
  try {
    const data = await forebrainApi.skillFileRead(props.skillName, path)
    draft.value = data.content
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    loadingFile.value = false
  }
}

async function save() {
  if (!activePath.value || readOnly.value) return
  saving.value = true
  error.value = ''
  savedNotice.value = false
  try {
    await forebrainApi.skillFileWrite(props.skillName, activePath.value, draft.value)
    savedNotice.value = true
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function download() {
  const url = downloadUrlValue.value
  if (!url) return
  try {
    saveSkillDownload(await forebrainApi.skillDownload(url))
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

</script>
