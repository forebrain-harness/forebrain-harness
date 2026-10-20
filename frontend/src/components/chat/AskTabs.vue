<template>
  <!-- The terminal's question overlay, on the web: one question per screen,
       a tab per question above (☐ unanswered / ✔ answered / the current one
       highlighted), the Submit tab and the arrows only where the terminal
       shows them — a form is only "multi" when it has several questions or a
       multi-select one. A single-select click answers and moves on, the way
       the overlay's Enter does; a multi-select click only toggles, and the
       answers are reviewed before they are sent. -->
  <div class="space-y-2">
    <div class="flex flex-wrap items-center gap-1 text-xs" role="tablist" data-testid="ask-tabs-nav">
      <span
        v-if="showSubmit"
        :class="onReview || current === 0 ? 'text-[var(--forebrain-muted-text)]' : 'text-[var(--forebrain-text-2)]'"
      >←</span>
      <button
        v-for="(q, i) in questions"
        :key="q.id"
        type="button"
        role="tab"
        :aria-selected="current === i"
        :data-testid="`ask-tab-${i}`"
        :data-answered="answered(i) ? 'true' : 'false'"
        :class="[
          'rounded-full border px-2 py-0.5',
          current === i
            ? 'border-[var(--forebrain-brand-border)] bg-[var(--forebrain-bg-alt)] font-medium text-[var(--forebrain-text)]'
            : answered(i)
              ? 'border-[var(--forebrain-divider)] text-[var(--forebrain-text)]'
              : 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]',
        ]"
        @click="current = i"
      >{{ answered(i) ? '✔' : '☐' }} {{ chipLabel(q) }}</button>
      <button
        v-if="showSubmit"
        type="button"
        role="tab"
        :aria-selected="onReview"
        data-testid="ask-tab-submit"
        :class="[
          'rounded-full border px-2 py-0.5',
          onReview
            ? 'border-[var(--forebrain-brand-border)] bg-[var(--forebrain-bg-alt)] font-medium text-[var(--forebrain-text)]'
            : 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]',
        ]"
        @click="current = questions.length"
      >✔ {{ t('common.submit') }}</button>
      <span v-if="showSubmit" class="text-[var(--forebrain-muted-text)]">→</span>
    </div>

    <div v-if="!onReview">
      <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ currentQuestion.prompt }}</div>
      <div class="mt-1 space-y-1">
        <div v-for="(opt, i) in currentQuestion.options" :key="opt.id">
          <button
            type="button"
            class="flex w-full items-start gap-2 rounded-md border px-2 py-1 text-left text-[13px] disabled:pointer-events-none disabled:opacity-60"
            :class="isSelected(opt)
              ? 'border-[var(--forebrain-brand-border)] bg-[var(--forebrain-bg-alt)] text-[var(--forebrain-text)]'
              : 'border-transparent text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-bg-alt)]'"
            :disabled="submitting"
            :data-selected="isSelected(opt) ? 'true' : 'false'"
            data-testid="ask-option"
            @click="choose(opt)"
          >
            <span v-if="currentQuestion.allowMultiple" class="font-mono text-[var(--forebrain-muted-text)]">[{{ isSelected(opt) ? 'x' : ' ' }}]</span>
            <span class="font-mono text-[var(--forebrain-muted-text)]">{{ i + 1 }}.</span>
            <span class="min-w-0 break-words">{{ opt.label }}<span v-if="opt.recommended" class="text-[var(--forebrain-muted-text)]">{{ t('chat.askRecommended') }}</span><span v-if="!currentQuestion.allowMultiple && isSelected(opt)" class="text-[var(--forebrain-success)]"> ✓</span></span>
          </button>
          <div v-if="opt.description" class="pl-8 text-xs text-[var(--forebrain-muted-text)]">{{ opt.description }}</div>
          <!-- The focused option's preview, where the overlay shows it: under
               the chosen row, monospaced, bounded, scrollable. -->
          <pre
            v-if="isSelected(opt) && opt.preview"
            class="mx-2 mb-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] p-2 font-mono text-[12px] text-[var(--forebrain-text-2)]"
            data-testid="ask-option-preview"
          >{{ opt.preview }}</pre>
        </div>

        <!--「其他」is the last numbered row; picking it turns the row into the
             field, and the text stays until it is answered. -->
        <div v-if="currentQuestion.allowOther">
          <button
            v-if="!otherOpen[currentQuestion.id]"
            type="button"
            class="flex w-full items-center gap-2 rounded-md border border-transparent px-2 py-1 text-left text-[13px] text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-bg-alt)]"
            data-testid="ask-other"
            @click="openOther"
          >
            <span v-if="currentQuestion.allowMultiple" class="font-mono text-[var(--forebrain-muted-text)]">[{{ hasOther ? 'x' : ' ' }}]</span>
            <span class="font-mono text-[var(--forebrain-muted-text)]">{{ currentQuestion.options.length + 1 }}.</span>
            <span>{{ t('chat.otherOptional') }}</span>
          </button>
          <input
            v-else
            ref="otherInput"
            v-model="otherText"
            class="mt-1 w-full rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2 py-1 text-xs text-[var(--forebrain-text-2)]"
            :placeholder="t('chat.otherOptional')"
            :disabled="submitting"
            data-testid="ask-other-input"
          >
        </div>
      </div>

      <!-- A multi-select question only toggles, so it gets its own way
           forward; a single question with no submit tab (single-select) needs
           one only for an「其他」answer, which no option click submits. -->
      <div v-if="currentQuestion.allowMultiple" class="flex justify-end gap-2">
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost text-xs"
          :disabled="submitting || current === questions.length - 1"
          data-testid="ask-next"
          @click="current += 1"
        >{{ t('chat.askNext') }}</button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="submitting"
          data-testid="ask-review-open"
          @click="current = questions.length"
        >{{ t('common.submit') }}</button>
      </div>
      <div v-else-if="!showSubmit && otherOpen[currentQuestion.id]" class="flex justify-end">
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="submitting"
          data-testid="ask-submit"
          @click="emit('submit')"
        >{{ t('common.submit') }}</button>
      </div>
    </div>

    <!-- The review the overlay's Submit tab shows: what was picked, then the
         one decision that sends it. -->
    <div v-else data-testid="ask-review">
      <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ t('chat.askReviewTitle') }}</div>
      <div v-if="!allAnswered" class="mt-1 text-xs text-[var(--forebrain-danger)]">{{ t('chat.askIncomplete') }}</div>
      <div class="mt-1 space-y-1">
        <div v-for="q in questions" :key="q.id">
          <div class="text-[13px] text-[var(--forebrain-text)]">• {{ q.prompt }}</div>
          <div v-if="answerSummary(q)" class="pl-4 text-[13px] text-[var(--forebrain-success)]">→ {{ answerSummary(q) }}</div>
          <div v-else class="pl-4 text-[13px] text-[var(--forebrain-muted-text)]">→ {{ t('chat.askNoAnswer') }}</div>
        </div>
      </div>
      <div class="mt-2 flex justify-end gap-2">
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost text-xs"
          :disabled="submitting"
          data-testid="ask-review-back"
          @click="current = questions.length - 1"
        >{{ t('common.cancel') }}</button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="submitting"
          data-testid="ask-submit"
          @click="emit('submit')"
        >{{ t('common.submit') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The agent's questions, one per screen with a tab bar above — the shape the
 * terminal's overlay has always asked with (one question at a time, ☐/✔ tabs,
 * a review before submit), now on the web. The card owns no answer state of
 * its own: every pick is reported to the caller, which writes it into the
 * shared pending-actions store, so both surfaces that show one action answer
 * it identically.
 */
import { computed, nextTick, ref } from 'vue'
import { useI18n } from '@/locales'
import type { AskAction } from '@/composables/usePendingActions'

const props = withDefaults(defineProps<{
  /** The form's questions, in the order they are asked. */
  questions: AskAction['form']['questions']
  /** This action's answers, by question id — a single option id for a
   *  single-select question, the chosen ids for a multi-select one. */
  answers: Record<string, string | string[]>
  /** This action's「其他」texts, by question id. */
  others: Record<string, string>
  /** Whether this action's answer is in flight. */
  submitting?: boolean
}>(), { submitting: false })

const emit = defineEmits<{
  (e: 'set-answer', questionId: string, value: string | string[]): void
  (e: 'set-other', questionId: string, value: string): void
  (e: 'submit'): void
}>()

const { t } = useI18n()

/** The tab the card is on: a question's index, or past the last one for the
 * review screen. */
const current = ref(0)

/** Which questions'「其他」rows have been picked, by id — the row a question
 * keeps until its text is answered. */
const otherOpen = ref<Record<string, boolean>>({})

const otherInput = ref<HTMLInputElement | null>(null)

/** The Submit tab — and with it the arrows — shows for exactly the forms the
 * terminal shows it for: several questions, or a multi-select one. */
const showSubmit = computed(() => !(props.questions.length === 1 && !props.questions[0].allowMultiple))

const onReview = computed(() => current.value === props.questions.length)

const currentQuestion = computed(() => props.questions[current.value])

const hasOther = computed(() => String(props.others[currentQuestion.value?.id ?? ''] ?? '').trim() !== '')

/** The current question's「其他」text, written through to the caller. */
const otherText = computed({
  get: () => String(props.others[currentQuestion.value?.id ?? ''] ?? ''),
  set: (value: string) => {
    const q = currentQuestion.value
    if (q) emit('set-other', q.id, value)
  },
})

function chipLabel(q: AskAction['form']['questions'][number]): string {
  return (q.header || '').trim() || q.prompt
}

function selectionsOf(q: AskAction['form']['questions'][number]): string[] {
  const raw = props.answers[q.id]
  return Array.isArray(raw) ? raw : []
}

function isSelected(opt: AskAction['form']['questions'][number]['options'][number]): boolean {
  const q = currentQuestion.value
  if (!q) return false
  if (q.allowMultiple) return selectionsOf(q).includes(opt.id)
  return props.answers[q.id] === opt.id
}

function answered(i: number): boolean {
  const q = props.questions[i]
  if (!q) return false
  if (q.allowMultiple) return selectionsOf(q).length > 0
  return String(props.answers[q.id] ?? '').trim() !== '' || String(props.others[q.id] ?? '').trim() !== ''
}

const allAnswered = computed(() => props.questions.every((_, i) => answered(i)))

/** A single-select pick is the answer and the way forward, exactly as the
 * overlay's Enter is: the one question of a single-select form submits itself,
 * any other form moves to the next question or the review. */
function advanceOrSubmit() {
  if (props.questions.length === 1) {
    emit('submit')
    return
  }
  if (current.value < props.questions.length - 1) current.value += 1
  else current.value = props.questions.length
}

function choose(opt: AskAction['form']['questions'][number]['options'][number]) {
  const q = currentQuestion.value
  if (!q) return
  if (q.allowMultiple) {
    const ids = selectionsOf(q)
    emit('set-answer', q.id, ids.includes(opt.id) ? ids.filter((id) => id !== opt.id) : [...ids, opt.id])
    return
  }
  emit('set-answer', q.id, opt.id)
  advanceOrSubmit()
}

function openOther() {
  const q = currentQuestion.value
  if (!q) return
  otherOpen.value = { ...otherOpen.value, [q.id]: true }
  void nextTick(() => otherInput.value?.focus())
}

/** What the review says was picked: the options' labels in their order, the
 *「其他」text last — the summary the overlay's submit screen prints. */
function answerSummary(q: AskAction['form']['questions'][number]): string {
  const parts = q.options
    .filter((opt) => (q.allowMultiple ? selectionsOf(q).includes(opt.id) : props.answers[q.id] === opt.id))
    .map((opt) => opt.label)
  const other = String(props.others[q.id] ?? '').trim()
  if (other) parts.push(t('chat.askOtherAnswer', { text: other }))
  return parts.join(', ')
}
</script>
