<template>
  <div
    v-if="online || offline"
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
    data-testid="skills-install-dialog"
    @click.self="close"
  >
    <div class="w-full max-w-lg rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
      <div class="text-[14px] font-medium text-[var(--forebrain-text)]">
        {{ online ? t('skills.onlineInstall') : t('skills.offlineInstall') }}
      </div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.installDestHint') }} · {{ destLabel }}</p>

      <p v-if="error" class="mt-3 rounded-lg bg-[var(--forebrain-input-bg)] px-3 py-2 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-else-if="notice" class="mt-3 rounded-lg bg-[var(--forebrand-soft,var(--forebrain-brand-soft))] px-3 py-2 text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

      <template v-if="online">
        <p class="mt-3 rounded-lg bg-[var(--forebrain-input-bg)] px-3 py-2 text-[12px] text-[var(--forebrain-text-2)]">
          {{ t('skills.onlineInstallHint') }}
        </p>
        <input
          v-model="sourceRef"
          type="text"
          class="mt-3 w-full rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]"
          :placeholder="t('skills.sourceRefPlaceholder')"
          :disabled="submitting"
          data-testid="skills-online-source"
        />
      </template>

      <template v-else>
        <input
          ref="fileInput"
          type="file"
          class="hidden"
          accept=".zip,.tar.gz,.tgz,.tar"
          data-testid="skills-offline-file"
          @change="onFilePicked"
        />
        <button
          type="button"
          class="mt-3 w-full rounded-md border border-dashed border-[var(--forebrain-divider-strong)] px-3 py-4 text-[13px] text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]"
          :disabled="submitting"
          data-testid="skills-offline-pick"
          @click="fileInput?.click()"
        >
          {{ pickedFile ? pickedFile.name : t('skills.pickArchive') }}
        </button>
        <p class="mt-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.archiveFormatsHint') }}</p>
      </template>

      <div class="mt-4 flex justify-end gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="submitting" @click="close">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="submitting || !canSubmit"
          data-testid="skills-install-submit"
          @click="submit"
        >
          {{ submitting ? t('common.loading') : t('common.install') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import forebrainApi, { getErrorMessage } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{
  online: boolean
  offline: boolean
  /** Where this page installs to: the primary agent, the shared library, the project. */
  dest: 'workspace' | 'global' | 'project'
  projectId?: string
}>()

const emit = defineEmits<{
  'update:online': [value: boolean]
  'update:offline': [value: boolean]
  installed: [names: string[]]
}>()

const { t } = useI18n()

const sourceRef = ref('')
const pickedFile = ref<File | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const submitting = ref(false)
const error = ref('')
const notice = ref('')

const canSubmit = computed(() => (props.online ? sourceRef.value.trim() !== '' : pickedFile.value !== null))

const destLabel = computed(() => {
  switch (props.dest) {
    case 'workspace': return t('skills.destAgent')
    case 'global': return t('skills.destShared')
    case 'project': return t('skills.destProject')
  }
})

watch(
  () => [props.online, props.offline],
  () => {
    error.value = ''
    notice.value = ''
  },
)

function close() {
  if (submitting.value) return
  emit('update:online', false)
  emit('update:offline', false)
}

function onFilePicked(event: Event) {
  const input = event.target as HTMLInputElement
  pickedFile.value = input.files && input.files.length ? input.files[0] : null
  error.value = ''
}

async function submit() {
  submitting.value = true
  error.value = ''
  notice.value = ''
  try {
    if (props.online) {
      const res = await (props.projectId
        ? forebrainApi.projectSkillInstall(props.projectId, { sourceRef: sourceRef.value.trim(), dest: props.dest })
        : forebrainApi.skillInstall({ sourceRef: sourceRef.value.trim(), dest: props.dest }))
      notice.value = t('skills.installStarted', { name: res.installed?.name ?? sourceRef.value.trim() })
      emit('installed', res.installed?.installed?.map((item) => item.name) ?? [res.installed?.name ?? ''])
      emit('update:online', false)
      return
    }
    const file = pickedFile.value
    if (!file) return
    const res = await forebrainApi.skillInstallUpload(file, props.dest, props.projectId)
    notice.value = t('skills.installedNames', { names: res.installed.join(', ') })
    emit('installed', res.installed)
    emit('update:offline', false)
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    submitting.value = false
  }
}
</script>
