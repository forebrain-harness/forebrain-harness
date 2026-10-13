<template>
  <div class="space-y-3" data-testid="workshop-cases">
    <div class="flex items-center justify-between">
      <span class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('workshop.tabCases') }}</span>
      <button
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="!editable || saving"
        data-testid="workshop-case-add"
        @click="openAdd"
      >
        {{ t('workshop.addCase') }}
      </button>
    </div>

    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="workshop-cases-error">{{ error }}</p>
    <p v-else-if="loading" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</p>
    <p v-else-if="!cases.length" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.noCases') }}</p>
    <div v-else class="space-y-2">
      <div v-for="(item, index) in cases" :key="index" class="rounded-lg border border-[var(--forebrain-divider)] p-2.5">
        <div class="flex items-center justify-between">
          <span class="text-[12px] font-medium text-[var(--forebrain-text)]">{{ t('workshop.caseN', { n: index + 1 }) }}</span>
          <span class="flex gap-1">
            <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="!editable" @click="openEdit(index)">{{ t('common.edit') }}</button>
            <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]" :disabled="!editable" @click="remove(index)">{{ t('common.delete') }}</button>
          </span>
        </div>
        <div class="mt-1 text-[12px] text-[var(--forebrain-text)]">{{ item.prompt }}</div>
        <div class="mt-0.5 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('workshop.caseExpectedLabel', { text: item.expected || '—' }) }}</div>
      </div>
    </div>
    <span class="block font-mono text-[11px] text-[var(--forebrain-muted-text)]">evals/evals.json</span>

    <CaseEditDialog
      :open="dialogOpen"
      :title="editingIndex === null ? t('workshop.addCase') : t('workshop.editCase', { n: (editingIndex ?? 0) + 1 })"
      :confirm-label="editingIndex === null ? t('workshop.addCase') : t('common.save')"
      :question="dialogQuestion"
      :expected="dialogExpected"
      @save="onSave"
      @cancel="dialogOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The workshop's test cases: the skill's evals/evals.json, edited in a modal.
 * Every read and write goes through the skill file endpoints, so a write lands
 * in the skill's draft (plan 036) and travels with the next publish.
 */
import { onMounted, ref, watch } from 'vue'
import CaseEditDialog from './CaseEditDialog.vue'
import forebrainApi, { getErrorMessage } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  skillName: string
  projectId?: string
  editable: boolean
}>(), { projectId: '' })

const { t } = useI18n()

type CaseRow = { prompt: string; expected: string }

const cases = ref<CaseRow[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const dialogOpen = ref(false)
const editingIndex = ref<number | null>(null)
const dialogQuestion = ref('')
const dialogExpected = ref('')

async function load() {
  if (!props.skillName) {
    cases.value = []
    return
  }
  loading.value = true
  error.value = ''
  try {
    const file = await forebrainApi.skillFileRead(props.skillName, 'evals/evals.json')
    cases.value = parseEvals(file.content)
  } catch {
    // No evals file yet is the empty list, not an error the user must see.
    cases.value = []
  } finally {
    loading.value = false
  }
}

function parseEvals(content: string): CaseRow[] {
  try {
    const doc = JSON.parse(content) as { evals?: Array<{ prompt?: string; expected_output?: string }> }
    return (doc.evals ?? []).map((item) => ({ prompt: String(item.prompt ?? ''), expected: String(item.expected_output ?? '') }))
  } catch {
    return []
  }
}

function serializeEvals(): string {
  const doc = {
    skill_name: props.skillName,
    evals: cases.value.map((item, index) => ({ id: index + 1, prompt: item.prompt, expected_output: item.expected })),
  }
  return `${JSON.stringify(doc, null, 2)}\n`
}

async function persist() {
  saving.value = true
  error.value = ''
  try {
    await forebrainApi.skillFileWrite(props.skillName, 'evals/evals.json', serializeEvals())
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

function openAdd() {
  editingIndex.value = null
  dialogQuestion.value = ''
  dialogExpected.value = ''
  dialogOpen.value = true
}

function openEdit(index: number) {
  editingIndex.value = index
  dialogQuestion.value = cases.value[index].prompt
  dialogExpected.value = cases.value[index].expected
  dialogOpen.value = true
}

async function onSave(payload: { question: string; expected: string }) {
  const row: CaseRow = { prompt: payload.question, expected: payload.expected }
  if (editingIndex.value === null) cases.value = [...cases.value, row]
  else cases.value = cases.value.map((item, index) => (index === editingIndex.value ? row : item))
  dialogOpen.value = false
  await persist()
}

async function remove(index: number) {
  cases.value = cases.value.filter((_, i) => i !== index)
  await persist()
}

watch(() => props.skillName, load)
onMounted(load)
</script>
