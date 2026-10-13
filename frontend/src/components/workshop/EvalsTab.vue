<template>
  <div class="space-y-3" data-testid="workshop-evals">
    <div class="flex items-center justify-between gap-2">
      <span class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('workshop.tabEvals') }}</span>
      <div v-if="!running" class="flex items-center gap-1.5">
        <select v-if="iterations.length" v-model="selected" class="forebrain-field h-7 w-[130px] text-[12px]" data-testid="workshop-eval-round">
          <option v-for="iter in iterations" :key="iter.name" :value="iter.name">{{ roundLabel(iter) }}</option>
        </select>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!skillName || !caseCount" data-testid="workshop-eval-run" @click="runEval">
          {{ t('workshop.runEval') }}
        </button>
      </div>
      <button v-else type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="workshop-eval-stop" @click="emit('stop')">
        {{ t('workshop.stop') }}
      </button>
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
      <table class="forebrain-table w-full">
        <thead>
          <tr>
            <th>{{ t('workshop.evalColCase') }}</th>
            <th>{{ t('workshop.evalWithSkill') }}</th>
            <th>{{ t('workshop.evalWithoutSkill') }}</th>
            <th>{{ t('workshop.evalColFeedback') }}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="(item, index) in currentCases" :key="item.id">
            <td>{{ index + 1 }}</td>
            <td><BadgeComponent :variant="passed(item, 'with_skill') ? 'success' : 'error'">{{ passed(item, 'with_skill') ? t('workshop.evalPass') : t('workshop.evalFail') }}</BadgeComponent></td>
            <td><BadgeComponent :variant="passed(item, 'without_skill') ? 'success' : 'error'">{{ passed(item, 'without_skill') ? t('workshop.evalPass') : t('workshop.evalFail') }}</BadgeComponent></td>
            <td><MessageSquareText v-if="item.feedback" class="h-3.5 w-3.5 text-[var(--forebrain-brand-1)]" aria-hidden="true" /><span v-else class="text-[var(--forebrain-muted-text)]">—</span></td>
            <td class="text-right">
              <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :data-testid="`workshop-eval-view-${index}`" @click="open(index)">{{ t('workshop.view') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="workshop-eval-refix" @click="emit('fill', t('workshop.refixMessage', { n: roundNumber }))">
        {{ t('workshop.refix') }}
      </button>
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
import { MessageSquareText } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
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
