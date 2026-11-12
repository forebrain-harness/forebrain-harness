import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeAll, describe, expect, it } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'

import RunErrorBlock from './RunErrorBlock.vue'

/**
 * What ended a turn in failure, and the way to its log (plan 288): the card is
 * the same one every surface draws, and the one that sits in a conversation
 * hands the log page that conversation and the turn's own window.
 */

beforeAll(() => setLocale('zh'))

async function mountBlock(props: Record<string, unknown>): Promise<{ wrapper: VueWrapper; router: ReturnType<typeof createRouter> }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/settings', component: { template: '<div />' } },
    ],
  })
  await router.push('/')
  const wrapper = mount(RunErrorBlock, {
    props: { text: '[400] Request contains an invalid argument.', ...props },
    global: { plugins: [router], directives: { tip } },
  })
  await flushPromises()
  return { wrapper, router }
}

describe('RunErrorBlock', () => {
  it('draws the runtime’s sentence, and the classified fact under it', async () => {
    const { wrapper } = await mountBlock({ detail: { status: 400, message: 'Request contains an invalid argument.' } })

    expect(wrapper.find('[data-testid="run-error"]').text()).toContain('[400] Request contains an invalid argument.')
    // No conversation named: no way to the log page from here.
    expect(wrapper.find('[data-testid="run-error-logs"]').exists()).toBe(false)
  })

  it('opens the log page on this conversation and this turn’s window', async () => {
    const started = new Date(2026, 10, 11, 11, 6, 0).toISOString()
    const failed = new Date(2026, 10, 11, 11, 20, 57).getTime()
    const { wrapper, router } = await mountBlock({ session: 's1', startedAt: started, at: failed })

    const button = wrapper.find('[data-testid="run-error-logs"]')
    expect(button.text()).toBe('查看日志')
    await button.trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.path).toBe('/settings')
    expect(router.currentRoute.value.query).toMatchObject({
      tab: 'logs',
      session: 's1',
      // A minute before the turn started …
      from: new Date(Date.parse(started) - 60_000).toISOString(),
      // … to a minute after it failed.
      to: new Date(failed + 60_000).toISOString(),
    })
  })

  it('falls back to the quarter of an hour before the failure when the start was never recorded', async () => {
    const failed = new Date(2026, 10, 11, 11, 20, 57).getTime()
    const { wrapper, router } = await mountBlock({ session: 's2', at: failed })

    await wrapper.find('[data-testid="run-error-logs"]').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query.from).toBe(new Date(failed - 15 * 60_000).toISOString())
    expect(router.currentRoute.value.query.to).toBe(new Date(failed + 60_000).toISOString())
  })
})
