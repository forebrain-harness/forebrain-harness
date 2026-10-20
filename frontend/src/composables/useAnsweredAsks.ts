import { ref, watch, type Ref } from 'vue'

import { forebrainApi, type ActionRecord } from '@/lib/api'

/** One question the agent asked, with the answer the user gave: the option
 *  labels they picked and, when they typed one, their own words. */
export type AnsweredQuestion = {
  /** The short tab label the engine keys answers by. */
  header: string
  prompt: string
  answers: string[]
}

/** One round of questions the conversation answered, as the workbench's
 *  history list shows it. */
export type AnsweredAsk = {
  id: string
  /** Unix seconds. */
  at: number
  questions: AnsweredQuestion[]
}

/** How much of a conversation's asking history the card keeps. A long session
 *  asks often; the workbench lists the recent rounds, not an archive. */
const maxAnsweredAsks = 50

type AskFormPayload = {
  questions?: {
    id?: string
    prompt?: string
    options?: { id?: string; label?: string }[]
  }[]
}

type AskAnswerPayload = {
  answers?: { question_id?: string; option_ids?: string[]; other_text?: string }[]
}

/**
 * The conversation's answered ask rounds, newest first, read from the same
 * actions table the pending cards come from: a `user_interaction` row keeps
 * the form it asked (payload_json) and the answer it got (answer_json), so the
 * history is a record of what was asked rather than a re-rendering of it.
 */
export function useAnsweredAsks(sessionId: Ref<string | null>, refresh?: () => unknown) {
  const asks = ref<AnsweredAsk[]>([])
  const loading = ref(false)
  let generation = 0

  /** One row as a round, or null when the row is not an ask or its payload is
   *  not a form. */
  function parseAnsweredAsk(a: ActionRecord): AnsweredAsk | null {
    if (a.kind !== 'user_interaction') return null
    let form: AskFormPayload
    let answered: AskAnswerPayload
    try {
      form = JSON.parse(a.payloadJson || '{}') as AskFormPayload
      answered = JSON.parse(a.answerJson || '{}') as AskAnswerPayload
    } catch {
      return null
    }
    if (!Array.isArray(form.questions) || !form.questions.length) return null
    const questions: AnsweredQuestion[] = []
    for (const question of form.questions) {
      const id = String(question?.id ?? '').trim()
      if (!id) continue
      const picked = answered.answers?.find((item) => String(item?.question_id ?? '').trim() === id)
      const labels = (picked?.option_ids ?? [])
        .map((optionId) => (question.options ?? []).find((option) => String(option?.id ?? '') === String(optionId))?.label)
        .filter((label): label is string => Boolean(String(label ?? '').trim()))
      const other = String(picked?.other_text ?? '').trim()
      questions.push({
        header: id,
        prompt: String(question?.prompt ?? '').trim(),
        answers: other ? [...labels, other] : labels,
      })
    }
    if (!questions.length) return null
    return { id: a.id, at: a.updatedAt, questions }
  }

  async function reload() {
    const sid = String(sessionId.value ?? '').trim()
    const current = ++generation
    if (!sid) {
      asks.value = []
      loading.value = false
      return
    }
    loading.value = true
    try {
      const list = await forebrainApi.actionsList('answered', sid)
      if (current !== generation) return
      asks.value = (Array.isArray(list) ? list : [])
        .map(parseAnsweredAsk)
        .filter((ask): ask is AnsweredAsk => Boolean(ask))
        .sort((a, b) => b.at - a.at)
        .slice(0, maxAnsweredAsks)
    } catch {
      // A list that cannot be read leaves the previous one on screen: an
      // unnoticed refresh failure must not empty the history a reader is
      // looking at.
    } finally {
      if (current === generation) loading.value = false
    }
  }

  watch([sessionId, () => (refresh ? refresh() : null)], () => void reload(), { immediate: true })

  return { asks, loading, reload }
}
