import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, h, onMounted } from 'vue'

import App from './App.vue'
import { usePrimaryAgents } from './composables/usePrimaryAgents'
import tip from './directives/tip'
import { forebrainApi, type PrimaryAgentsResponse } from './lib/api'
import { setLocale } from './locales'

/**
 * The shell's page key: a page is rebuilt when the address names another page
 * or the person switched tenant — and not when the gateway has only just said
 * which tenant the shell was on all along.
 *
 * That first resolution is the whole point of these two tests. The shell is
 * drawn before `/agents/primary` answers, so its key is built from an empty
 * tenant id; a key that carries the id would change the moment the answer
 * lands, tearing down the page that had just mounted and mounting a second one
 * that reads everything the first one read.
 */

/** Every mount of the routed page, in order: the count is the assertion. */
const mounts: string[] = []

/** The routed page: it counts the times the shell puts it on screen. */
const Probe = defineComponent({
  name: 'Probe',
  setup() {
    onMounted(() => { mounts.push('mounted') })
    return () => h('div', { 'data-testid': 'probe' })
  },
})

function agent(id: string, active = false) {
  return {
    id,
    workspaceRoot: `/tmp/${id}`,
    privateSkillsRoot: '',
    sharedSkillsRoots: [],
    active,
  }
}

function roster(activeId: string): PrimaryAgentsResponse {
  return { activeId, records: [agent(activeId, true), agent('other')] }
}

async function mountShell(): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: Probe }],
  })
  await router.push('/')
  await router.isReady()
  const wrapper = mount(App, {
    global: {
      plugins: [router],
      directives: { tip },
      // The drawer is the shell's own neighbour, not the page under test; the
      // modal only renders while one is open.
      stubs: { ChatDrawer: true, ForebrainModal: true },
    },
  })
  await flushPromises()
  return wrapper
}

describe('App shell page key', () => {
  beforeAll(() => setLocale('en'))

  beforeEach(() => {
    mounts.length = 0
    const { activeId, records } = usePrimaryAgents()
    activeId.value = ''
    records.value = []
    // jsdom has no media queries; the shell reads one to collapse the rail.
    vi.stubGlobal('matchMedia', () => ({
      matches: false,
      addEventListener: () => {},
      removeEventListener: () => {},
    }))
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('resolves the tenant once without rebuilding the page on screen', async () => {
    const agents = vi.spyOn(forebrainApi, 'primaryAgents').mockResolvedValue(roster('main'))

    const wrapper = await mountShell()

    // The page mounted before the answer, and the answer is not a switch: it
    // stays the same instance, still on screen, never read a second time.
    await vi.waitFor(() => expect(usePrimaryAgents().activeId.value).toBe('main'))
    await flushPromises()
    expect(agents).toHaveBeenCalledTimes(1)
    expect(mounts).toHaveLength(1)
    expect(wrapper.find('[data-testid="probe"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('rebuilds the page when the person switches tenant', async () => {
    vi.spyOn(forebrainApi, 'primaryAgents').mockResolvedValue(roster('main'))
    vi.spyOn(forebrainApi, 'primaryAgentSwitch').mockResolvedValue(roster('other'))

    const wrapper = await mountShell()
    await vi.waitFor(() => expect(usePrimaryAgents().activeId.value).toBe('main'))
    expect(mounts).toHaveLength(1)

    await usePrimaryAgents().switchPrimaryAgent('other')
    await flushPromises()

    // A switch is the one thing that rebuilds: the page belongs to the tenant
    // the shell was on, so the new tenant gets a new one.
    expect(mounts).toHaveLength(2)
    expect(wrapper.find('[data-testid="probe"]').exists()).toBe(true)
    wrapper.unmount()
  })
})
