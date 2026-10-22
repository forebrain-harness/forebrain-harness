<template>
  <div class="space-y-3" data-testid="workshop-evals">
    <div class="flex flex-wrap items-center justify-between gap-2 gap-y-1.5">
      <span class="whitespace-nowrap text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('workshop.tabEvals') }}</span>
      <div v-if="!running" class="flex shrink-0 items-center gap-1.5">
        <!-- The library's Select is a button, so its trigger carries the height
             the native select had: 28px, which the library's own
             `data-[size=default]:h-9` would otherwise win at 0,2,0. -->
        <Select v-if="iterations.length" :model-value="selected" @update:model-value="(value) => (selected = String(value ?? ''))">
          <SelectTrigger class="forebrain-field !h-7 w-[130px] text-[12px]" data-testid="workshop-eval-round">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="iter in iterations" :key="iter.name" :value="iter.name">{{ roundLabel(iter) }}</SelectItem>
          </SelectContent>
        </Select>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="!skillName || !caseCount" data-testid="workshop-eval-run" @click="runEval">
          {{ t('workshop.runEval') }}
        </Button>
      </div>
      <Button v-else type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" data-testid="workshop-eval-stop" @click="emit('stop')">
        {{ t('workshop.stop') }}
      </Button>
    </div>

    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="workshop-evals-error">{{ error }}</p>
    <p v-if="!skillName" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.tabNeedsSkill') }}</p>
    <template v-else-if="running">
      <div class="h-1.5 w-full overflow-hidden rounded-full bg-[var(--forebrain-input-bg)]">
        <div class="h-full bg-[var(--forebrain-brand-1)]" :style="{ width: progressPercent }" />
      </div>
      <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.evalRunning', { done: doneCount, total: caseCount }) }}</span>
    </template>
    <p v-else-if="!caseCount" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.evalAddCasesFirst') }}</p>
    <template v-else>
      <div class="flex flex-wrap items-center gap-2">
        <BadgeComponent variant="success">{{ t('workshop.evalWithBadge', { passed: withPassed, total: caseCount }) }}</BadgeComponent>
        <BadgeComponent variant="gray">{{ t('workshop.evalWithoutBadge', { passed: withoutPassed, total: caseCount }) }}</BadgeComponent>
      </div>
      <DataTable
        :columns="columns"
        :rows="currentCases"
        :row-key="(row) => row.id"
        class="w-full"
      >
        <template #cell-case="{ rowIndex }">{{ rowIndex + 1 }}</template>
        <template #cell-withSkill="{ row }">
          <BadgeComponent :variant="passed(row, 'with_skill') ? 'success' : 'error'">{{ passed(row, 'with_skill') ? t('workshop.evalPass') : t('workshop.evalFail') }}</BadgeComponent>
        </template>
        <template #cell-withoutSkill="{ row }">
          <BadgeComponent :variant="passed(row, 'without_skill') ? 'success' : 'error'">{{ passed(row, 'without_skill') ? t('workshop.evalPass') : t('workshop.evalFail') }}</BadgeComponent>
        </template>
        <template #cell-feedback="{ row }">
          <MessageSquareText v-if="row.feedback" class="h-3.5 w-3.5 text-[var(--forebrain-brand-1)]" aria-hidden="true" />
          <span v-else class="text-[var(--forebrain-muted-text)]">—</span>
        </template>
        <template #cell-actions="{ rowIndex }">
          <RowActions>
            <RowAction
              :icon="Eye"
              :label="t('workshop.view')"
              :name="`${t('workshop.evalColCase')} ${rowIndex + 1}`"
              :data-testid="`workshop-eval-view-${rowIndex}`"
              @click="open(rowIndex)"
            />
          </RowActions>
        </template>
      </DataTable>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" data-testid="workshop-eval-refix" @click="emit('fill', t('workshop.refixMessage', { n: roundNumber }))">
        {{ t('workshop.refix') }}
      </Button>
    </template>

    <EvalCaseDialog
      :open="dialogOpen"
      :title="dialogTitle"
      :item="dialogItem"
      @save="onSaveFeedback"
      @cancel="dialogOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The workshop's evaluation tab: the rounds a skill's runs produced, each
 * case's pass/fail under both configurations, and the feedback box that is
 * saved back into the round's feedback.json. Running an evaluation is the
 * model's job — the tab fills the composer and sends, it never runs a script.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { Eye, MessageSquareText } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import EvalCaseDialog from './EvalCaseDialog.vue'
import forebrainApi, { getErrorMessage, type WorkshopEvalCase, type WorkshopEvalIteration } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  skillName: string
  projectId?: string
  running?: boolean
  doneCount?: number
}>(), { projectId: '', running: false, doneCount: 0 })

const emit = defineEmits<{
  send: [text: string]
  fill: [text: string]
  stop: []
  'feedback-saved': []
}>()

const { t, locale } = useI18n()

const iterations = ref<WorkshopEvalIteration[]>([])
const selected = ref('')
const error = ref('')

const currentIteration = computed(() => iterations.value.find((iter) => iter.name === selected.value) ?? null)
const currentCases = computed<WorkshopEvalCase[]>(() => currentIteration.value?.cases ?? [])

/**
 * The table's columns. No widths: every column shares the width, which is how
 * this comparison reads — the two results sit side by side. Nothing sorts.
 */
const columns: DataColumn[] = [
  { id: 'case', header: t('workshop.evalColCase') },
  { id: 'withSkill', header: t('workshop.evalWithSkill') },
  { id: 'withoutSkill', header: t('workshop.evalWithoutSkill') },
  { id: 'feedback', header: t('workshop.evalColFeedback') },
  { id: 'actions', header: '', cellClass: 'text-right' },
]
const caseCount = computed(() => {
  // The number of cases is the round's own; before a round exists it is the
  // test cases the skill declares, so "运行评测" is live as soon as there is
  // something to run.
  return currentCases.value.length
})
const progressPercent = computed(() => {
  if (!caseCount.value) return '0%'
  return `${Math.min(100, Math.round(((props.doneCount ?? 0) / caseCount.value) * 100))}%`
})
const roundNumber = computed(() => currentIteration.value?.order ?? iterations.value.length)

const withPassed = computed(() => currentCases.value.filter((item) => passed(item, 'with_skill')).length)
const withoutPassed = computed(() => currentCases.value.filter((item) => passed(item, 'without_skill')).length)

const dialogOpen = ref(false)
const dialogIndex = ref(0)
const dialogItem = computed<WorkshopEvalCase | null>(() => currentCases.value[dialogIndex.value] ?? null)
const dialogTitle = computed(() => t('workshop.evalCaseTitle', { n: dialogIndex.value + 1, round: roundNumber.value }))

function passed(item: WorkshopEvalCase, config: string): boolean {
  const c = item.configs?.[config]
  return !!c && c.present && c.total > 0 && c.passed === c.total
}

function roundLabel(iter: WorkshopEvalIteration): string {
  const stamp = iter.modified ? new Date(iter.modified * 1000) : null
  const time = stamp && !Number.isNaN(stamp.getTime())
    ? stamp.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
    : ''
  return t('workshop.evalRound', { n: iter.order, time })
}

async function load() {
  if (!props.skillName) {
    iterations.value = []
    selected.value = ''
    return
  }
  error.value = ''
  try {
    const data = await forebrainApi.skillWorkshopEvals(props.skillName, props.projectId)
    iterations.value = data.iterations ?? []
    if (!iterations.value.some((iter) => iter.name === selected.value)) {
      selected.value = iterations.value[0]?.name ?? ''
    }
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

function runEval() {
  emit('send', t('workshop.runEval'))
}

function open(index: number) {
  dialogIndex.value = index
  dialogOpen.value = true
}

async function onSaveFeedback(payload: { runId: string; feedback: string }) {
  dialogOpen.value = false
  if (!currentIteration.value) return
  try {
    await forebrainApi.skillWorkshopFeedback(props.skillName, currentIteration.value.name, payload.runId, payload.feedback, props.projectId)
    emit('feedback-saved')
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

defineExpose({ reload: load })

watch(() => [props.skillName, props.projectId], load)
onMounted(load)
</script>
