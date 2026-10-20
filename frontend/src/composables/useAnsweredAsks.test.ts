import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import { useAnsweredAsks } from './useAnsweredAsks'
import { forebrainApi, type ActionRecord } from '@/lib/api'

/** One answered round as the actions table stores it: the form it asked in
 *  payload_json, and the answer it got in answer_json. */
function answeredAction(overrides: Partial<ActionRecord> = {}): ActionRecord {
  return {
    id: 'a1',
    kind: 'user_interaction',
    status: 'answered',
    payloadJson: JSON.stringify({
      questions: [
        {
          id: '范围',
          prompt: '这次改到哪一层？',
          options: [
            { id: 'web', label: '只改网页' },
            { id: 'engine', label: '引擎也改' },
          ],
        },
        {
          id: '补充',
          prompt: '还要补什么？',
          options: [{ id: 'doc', label: '补文档' }],
        },
      ],
    }),
    answerJson: JSON.stringify({
      answers: [
        { question_id: '范围', option_ids: ['engine'] },
        { question_id: '补充', option_ids: ['doc'], other_text: '顺手补测试' },
      ],
    }),
    createdAt: 100,
    updatedAt: 200,
    ...overrides,
  }
}

describe('useAnsweredAsks', () => {
  afterEach(() => vi.restoreAllMocks())

  it('reads an answered round as its questions and the labels the user picked', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([answeredAction()])

    const asks = useAnsweredAsks(ref('s1'))
    await asks.reload()

    expect(forebrainApi.actionsList).toHaveBeenCalledWith('answered', 's1')
    expect(asks.asks.value).toHaveLength(1)
    const [round] = asks.asks.value
    expect(round.at).toBe(200)
    expect(round.questions.map((question) => question.header)).toEqual(['范围', '补充'])
    expect(round.questions[0].prompt).toBe('这次改到哪一层？')
    // Option ids are mapped back to the labels the form offered.
    expect(round.questions[0].answers).toEqual(['引擎也改'])
    // A typed answer travels beside the picked label.
    expect(round.questions[1].answers).toEqual(['补文档', '顺手补测试'])
  })

  it('lists the newest round first and ignores every other kind of action', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      answeredAction({ id: 'older', updatedAt: 100 }),
      answeredAction({ id: 'not-an-ask', kind: 'write_file' }),
      answeredAction({ id: 'newer', updatedAt: 900 }),
    ])

    const asks = useAnsweredAsks(ref('s1'))
    await asks.reload()

    expect(asks.asks.value.map((round) => round.id)).toEqual(['newer', 'older'])
  })

  it('keeps the round a payload that is not an ask form never had out of the list', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([
      answeredAction({ id: 'broken', payloadJson: 'not json' }),
      answeredAction({ id: 'no-questions', payloadJson: '{}' }),
    ])

    const asks = useAnsweredAsks(ref('s1'))
    await asks.reload()

    expect(asks.asks.value).toEqual([])
  })

  it('asks for nothing when the page has no conversation yet', async () => {
    const fetch = vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([])

    const asks = useAnsweredAsks(ref(null))
    await asks.reload()

    expect(fetch).not.toHaveBeenCalled()
    expect(asks.asks.value).toEqual([])
  })
})
