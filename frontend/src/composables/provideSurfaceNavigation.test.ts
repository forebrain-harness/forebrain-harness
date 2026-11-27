import { flushPromises, mount } from '@vue/test-utils'
import { beforeAll, describe, expect, it } from 'vitest'
import { defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import {
  SurfaceNavigationKey,
  useSurfaceNavigation,
  type SurfaceNavigation,
} from './provideSurfaceNavigation'

/**
 * The seam a card's "open the page that owns this" goes through. The web page
 * keeps its router pushes verbatim; a surface with no router instance (the VS
 * Code panel) publishes its own answers, and the same cards call those instead
 * — without a router ever being asked for.
 */

beforeAll(() => setLocale('zh'))

function recorder(): { calls: string[]; navigation: SurfaceNavigation } {
  const calls: string[] = []
  return {
    calls,
    navigation: {
      openConversation: (sessionId) => calls.push(`conversation:${sessionId}`),
      openSettingsPage: (tab, query) => calls.push(`settings:${tab}:${JSON.stringify(query ?? {})}`),
    },
  }
}

const Probe = defineComponent({
  setup() {
    const navigation = useSurfaceNavigation()
    return () => h('div', [
      h('button', { id: 'conv', onClick: () => navigation.openConversation('s-2') }, 'conversation'),
      h('button', { id: 'logs', onClick: () => navigation.openSettingsPage('logs', { session: 's-1' }) }, 'logs'),
    ])
  },
})

describe('useSurfaceNavigation', () => {
  it('takes the surface’s answers where one published them, with no router at all', async () => {
    const { calls, navigation } = recorder()
    const wrapper = mount(Probe, {
      global: { provide: { [SurfaceNavigationKey as symbol]: navigation } },
    })

    await wrapper.get('#conv').trigger('click')
    await wrapper.get('#logs').trigger('click')

    expect(calls).toEqual(['conversation:s-2', 'settings:logs:{"session":"s-1"}'])
  })

  it('falls back to the router’s own two pushes where nothing was published', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: { template: '<div />' } },
        { path: '/settings', component: { template: '<div />' } },
      ],
    })
    await router.push('/?keep=1')
    await router.isReady()
    const wrapper = mount(Probe, { global: { plugins: [router] } })

    await wrapper.get('#conv').trigger('click')
    await flushPromises()
    // The conversation push keeps the query the page already had.
    expect(router.currentRoute.value.path).toBe('/')
    expect(router.currentRoute.value.query).toMatchObject({ keep: '1', session: 's-2' })

    await wrapper.get('#logs').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/settings')
    expect(router.currentRoute.value.query).toMatchObject({ tab: 'logs', session: 's-1' })
  })

  it('carries a real card’s log button to the surface, not to a router that is not there', async () => {
    const { calls, navigation } = recorder()
    const wrapper = mount(RunErrorBlock, {
      props: { text: '[400] Request contains an invalid argument.', session: 's-1' },
      global: { provide: { [SurfaceNavigationKey as symbol]: navigation }, directives: { tip } },
    })

    await wrapper.get('[data-testid="run-error-logs"]').trigger('click')
    await flushPromises()

    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatch(/^settings:logs:/)
    expect(JSON.parse(calls[0].slice('settings:logs:'.length))).toMatchObject({ session: 's-1' })
  })
})