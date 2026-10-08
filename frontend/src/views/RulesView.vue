<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-5xl">
      <div class="flex items-center gap-2">
        <h1 class="text-xl font-medium text-[var(--forebrain-text)]">{{ t('rules.title') }}</h1>
        <ScopeBadge type="agent" :label="t('scope.agent')" />
      </div>
      <p class="mt-1 text-[13px] text-[var(--forebrain-text-2)]">{{ t('rules.agentDescription') }}</p>
      <div class="mt-4">
        <RulesFileEditor
          :files="files as unknown as Array<{ exists: boolean } & Record<string, unknown>>"
          :creatable="creatable as unknown as Array<Record<string, unknown>>"
          :active-key="activeName"
          :active-label="activeName"
          :content="draft"
          :dirty="dirty"
          :saving="saving"
          :notice="notice"
          :warning="warning"
          :error="error"
          :file-key="(f: Record<string, unknown>) => String(f.name)"
          :label="(f: Record<string, unknown>) => String(f.name)"
          :item-key="(i: Record<string, unknown>) => String(i)"
          :item-value="(i: Record<string, unknown>) => String(i)"
          :item-label="(i: Record<string, unknown>) => t('rules.createFile', { name: String(i) })"
          @select="onSelect"
          @create="onCreate"
          @update="draft = $event"
          @save="save"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import RulesFileEditor from '@/components/rules/RulesFileEditor.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The primary agent's instruction files (AGENTS.md / SOUL.md / USER.md).
 * Editing them is editing what every session of this tenant is told before
 * its first word; changes start with the next session, never mid-run.
 */
const { t } = useI18n()

type FileRow = { name: string; exists: boolean; sizeBytes?: number; updatedAt?: number }

const files = ref<FileRow[]>([])
const activeName = ref('AGENTS.md')
const draft = ref('')
const savedContent = ref('')
const saving = ref(false)
const error = ref('')
const notice = ref('')
const warning = ref('')

const creatable = computed(() => files.value.filter((file) => !file.exists).map((file) => file.name))
const dirty = computed(() => draft.value !== savedContent.value)

async function loadList() {
  error.value = ''
  try {
    const res = await forebrainApi.agentRuleFiles()
    files.value = res.files ?? []
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

async function onSelect(file: Record<string, unknown>) {
  activeName.value = String(file.name)
  await loadContent()
}

async function onCreate(value: string) {
  if (!value) return
  activeName.value = value
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
    const file = await forebrainApi.agentRuleFile(activeName.value)
    // A file not created yet reads as empty; saving it creates it.
    draft.value = file.content
    savedContent.value = file.content
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
    const res = await forebrainApi.saveAgentRuleFile(activeName.value, draft.value)
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
  const first = files.value.find((file) => file.exists) ?? files.value[0]
  if (first) {
    activeName.value = first.name
    await loadContent()
  }
})
</script>
