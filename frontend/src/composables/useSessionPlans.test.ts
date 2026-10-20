import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import { useSessionPlans } from './useSessionPlans'
import { forebrainApi } from '@/lib/api'

describe('useSessionPlans', () => {
  afterEach(() => vi.restoreAllMocks())

  it('draws the plan history newest first, whatever order the list arrived in', async () => {
    vi.spyOn(forebrainApi, 'sessionPlans').mockResolvedValue([
      { name: 'older.md', path: 'plans/p/s1/older.md', updatedAt: 200, markdown: '# Older' },
      { name: 'newer.md', path: 'plans/p/s1/newer.md', updatedAt: 300, markdown: '# Newer' },
    ])

    const plans = useSessionPlans(ref('s1'))
    await plans.reload()

    expect(forebrainApi.sessionPlans).toHaveBeenCalledWith('s1')
    expect(plans.plans.value.map((plan) => plan.name)).toEqual(['newer.md', 'older.md'])
    expect(plans.loading.value).toBe(false)
  })

  it('asks for nothing when the page has no conversation yet', async () => {
    const fetch = vi.spyOn(forebrainApi, 'sessionPlans').mockResolvedValue([])

    const plans = useSessionPlans(ref(null))
    await plans.reload()

    expect(fetch).not.toHaveBeenCalled()
    expect(plans.plans.value).toEqual([])
  })

  it('keeps the history on screen when a re-read fails', async () => {
    const fetch = vi.spyOn(forebrainApi, 'sessionPlans')
      .mockResolvedValue([{ name: 'only.md', path: 'plans/p/s1/only.md', updatedAt: 100, markdown: '# Only' }])

    const plans = useSessionPlans(ref('s1'))
    await plans.reload()
    fetch.mockRejectedValue(new Error('offline'))
    await plans.reload()

    expect(plans.plans.value.map((plan) => plan.name)).toEqual(['only.md'])
  })
})
