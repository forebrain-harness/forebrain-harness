import { describe, expect, it, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'

import ContextTab from './ContextTab.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

describe('ContextTab', () => {
  afterEach(() => vi.restoreAllMocks())

  it('flags a context near its limit at 80 percent or more', async () => {
    vi.spyOn(forebrainApi, 'sessionContext').mockResolvedValue({
      modelContextTokens: 128000,
      projectedContextTokens: 110000,
      availableWindowTokens: 120000,
      reserveTokens: 8000,
      conversationTokens: 40000,
      itemCount: 1,
      topItems: [{ sourceId: 'FOREBRAIN.md', estimatedTokens: 1200 }],
    })
    const wrapper = mount(ContextTab, { props: { scope: { sessionId: 's1' } } })
    await flush()
    expect(wrapper.text()).toContain('接近上限')
    expect(wrapper.text()).toContain('下一轮前会自动压缩较早的对话。')
    expect(wrapper.text()).not.toContain('正常')
  })

  it('shows the normal badge well below the limit', async () => {
    vi.spyOn(forebrainApi, 'sessionContext').mockResolvedValue({
      modelContextTokens: 128000,
      projectedContextTokens: 40000,
      availableWindowTokens: 120000,
      reserveTokens: 8000,
      conversationTokens: 40000,
    })
    const wrapper = mount(ContextTab, { props: { scope: { sessionId: 's1' } } })
    await flush()
    expect(wrapper.text()).toContain('正常')
    expect(wrapper.text()).not.toContain('接近上限')
  })
})
