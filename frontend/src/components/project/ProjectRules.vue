<template>
  <div class="mx-auto max-w-5xl">
    <div class="flex items-center gap-2">
      <h2 class="text-[15px] font-medium text-[var(--forebrain-text)]">{{ t('rules.projectTitle') }}</h2>
      <span class="scope-badge">{{ t('scope.project') }}</span>
    </div>
    <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('rules.projectDescription') }}</p>
    <div class="mt-4">
      <RulesFileEditor
        :files="files as unknown as Array<{ exists: boolean } & Record<string, unknown>>"
        :creatable="creatable as unknown as Array<Record<string, unknown>>"
        :active-key="activeDir"
        :active-label="activeLabel"
        :content="draft"
        :dirty="dirty"
        :saving="saving"
        :notice="notice"
        :warning="warning"
        :error="error"
        :file-key="(f: Record<string, unknown>) => String(f.dir)"
        :label="(f: Record<string, unknown>) => (f.dir ? String(f.dir) + '/FOREBRAIN.md' : 'FOREBRAIN.md')"
        :item-key="(i: Record<string, unknown>) => String(i)"
        :item-value="(i: Record<string, unknown>) => String(i)"
        :item-label="(i: Record<string, unknown>) => t('rules.createAtDir', { dir: String(i) || t('rules.projectRoot') })"
        @select="onSelect"
        @create="onCreate"
        @update="draft = $event"
        @save="save"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import RulesFileEditor from '@/components/rules/RulesFileEditor.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * This project's FOREBRAIN.md chain: one file per directory layer, root
 * first. Only the project's own files appear — the chain is assembled from
 * its root, never from anything inherited.
 */
const props = defineProps<{ project: unknown; projectId: string }>()

const { t } = useI18n()

type FileRow = { dir: string; exists: boolean; sizeBytes?: number; updatedAt?: number }

const files = ref<FileRow[]>([])
const activeDir = ref('')
const draft = ref('')
const savedContent = ref('')
const saving = ref(false)
const error = ref('')
const notice = ref('')
const warning = ref('')

const creatable = computed(() => files.value.filter((file) => !file.exists).map((file) => file.dir))
const dirty = computed(() => draft.value !== savedContent.value)
const activeLabel = computed(() => (activeDir.value ? `${activeDir.value}/FOREBRAIN.md` : 'FOREBRAIN.md'))

async function loadList() {
  error.value = ''
  try {
    const res = await forebrainApi.projectRuleFiles(props.projectId)
    files.value = res.files ?? []
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

async function onSelect(file: Record<string, unknown>) {
  activeDir.value = String(file.dir)
  await loadContent()
}

async function onCreate(value: string) {
  activeDir.value = value
  draft.value = ''
  savedContent.value = ''
  warning.value = ''
  notice.value = ''
}

async function loadContent() {
  error.value = ''
  notice.value = ''
  warning.value = ''
  try {
    const content = await forebrainApi.projectRuleFile(props.projectId, activeDir.value)
    draft.value = content.startsWith('{"exists":false') ? '' : content
    savedContent.value = draft.value
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

async function save() {
  if (saving.value || !dirty.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  warning.value = ''
  try {
    const res = await forebrainApi.saveProjectRuleFile(props.projectId, activeDir.value, draft.value)
    savedContent.value = draft.value
    warning.value = res.warning === 'exceeds_assembly_budget' ? 'budget' : ''
    notice.value = t('rules.savedNotice')
    await loadList()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

onMounted(async () => {
  await loadList()
  const first = files.value.find((file) => file.exists)
  if (first) {
    activeDir.value = first.dir
    await loadContent()
  }
})
</script>

<style scoped>
.scope-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 9999px;
  padding: 2px 10px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
</style>
