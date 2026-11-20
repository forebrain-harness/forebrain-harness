import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
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

describe('App primary-agent pencil dialog', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    const { activeId, records } = usePrimaryAgents()
    activeId.value = ''
    records.value = []
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

  /** The shell with a real modal behind it, attached to the document so the
   * dialog's teleported body is reachable. */
  async function mountShellWithModal(): Promise<VueWrapper> {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/', component: { template: '<div />' } }],
    })
    await router.push('/')
    await router.isReady()
    const wrapper = mount(App, {
      attachTo: document.body,
      global: { plugins: [router], directives: { tip }, stubs: { ChatDrawer: true } },
    })
    await flushPromises()
    return wrapper
  }

  it('edits the display name and the description; the name stays read-only', async () => {
    vi.spyOn(forebrainApi, 'primaryAgents').mockResolvedValue(
      roster('main') as PrimaryAgentsResponse,
    )
    const updated = {
      activeId: 'main',
      records: [
        { ...agent('main', true), name: 'main', description: '' },
        { ...agent('other'), name: '调研助手', description: '读论文、查资料、写调研摘要。' },
      ],
    } as PrimaryAgentsResponse
    vi.spyOn(forebrainApi, 'primaryAgentUpdate').mockResolvedValue(updated)
    const wrapper = await mountShellWithModal()
    await vi.waitFor(() => expect(usePrimaryAgents().activeId.value).toBe('main'))

    // Every row carries the pencil, main included: editing is not deleting.
    // The dropdown teleports to the body, so the document is where its rows
    // live.
    await wrapper.find('[aria-label="主代理"]').trigger('click')
    const pencils = [...document.querySelectorAll('[data-testid="agent-edit"]')]
    expect(pencils).toHaveLength(2)

    pencils[1].dispatchEvent(new Event('click', { bubbles: true }))
    await flushPromises()
    const id = document.querySelector('[data-testid="agent-edit-id"]') as HTMLInputElement
    expect(id.readOnly).toBe(true)
    expect(id.value).toBe('other')
    const name = document.querySelector('[data-testid="agent-edit-name"]') as HTMLInputElement
    const description = document.querySelector('[data-testid="agent-edit-description"]') as HTMLTextAreaElement
    expect(name.value).toBe('')
    expect(description.value).toBe('')

    await new DOMWrapper(name).setValue('调研助手')
    await new DOMWrapper(description).setValue('读论文、查资料、写调研摘要。')
    await new DOMWrapper(document.querySelector('[data-testid="agent-edit-save"]') as HTMLElement).trigger('click')
    await flushPromises()

    expect(forebrainApi.primaryAgentUpdate).toHaveBeenCalledWith('other', {
      name: '调研助手',
      description: '读论文、查资料、写调研摘要。',
    })
    // The listing that answers is the record set; the dialog closes on it.
    expect(usePrimaryAgents().records.value[1]?.name).toBe('调研助手')
    expect(document.querySelector('[data-testid="agent-edit-save"]')).toBeNull()
    wrapper.unmount()
  })
})

describe('App unified agent create dialog', () => {
  beforeAll(() => setLocale('zh'))

  // The shell is attached to the document so the teleported dialog is
  // reachable, which is exactly why it must unmount even when an assertion
  // throws: a shell left behind drives the next test's clicks, and the next
  // test's spies answer them.
  let shell: VueWrapper | null = null

  beforeEach(() => {
    shell = null
    const { activeId, records } = usePrimaryAgents()
    activeId.value = ''
    records.value = []
    vi.stubGlobal('matchMedia', () => ({
      matches: false,
      addEventListener: () => {},
      removeEventListener: () => {},
    }))
  })

  afterEach(() => {
    shell?.unmount()
    shell = null
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  const mountShellWithModal = async (): Promise<VueWrapper> => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/', component: { template: '<div />' } }],
    })
    await router.push('/')
    await router.isReady()
    shell = mount(App, {
      attachTo: document.body,
      global: { plugins: [router], directives: { tip }, stubs: { ChatDrawer: true } },
    })
    await flushPromises()
    return shell
  }

  async function openDialog(wrapper: VueWrapper): Promise<void> {
    await wrapper.find('[aria-label="主代理"]').trigger('click')
    const create = document.querySelector('[data-testid="new-agent"]')
    expect(create).not.toBeNull()
    create!.dispatchEvent(new Event('click', { bubbles: true }))
    await flushPromises()
  }

  it('creates a primary agent by default and switches to it', async () => {
    vi.spyOn(forebrainApi, 'primaryAgents').mockResolvedValue(roster('main') as PrimaryAgentsResponse)
    vi.spyOn(forebrainApi, 'primaryAgentSwitch').mockResolvedValue(roster('helper') as PrimaryAgentsResponse)
    const create = vi.spyOn(forebrainApi, 'createPrimaryAgent').mockResolvedValue(roster('helper') as PrimaryAgentsResponse)
    const wrapper = await mountShellWithModal()
    await vi.waitFor(() => expect(usePrimaryAgents().activeId.value).toBe('main'))

    await openDialog(wrapper)
    const name = document.querySelector('[data-testid="new-agent-name"]') as HTMLInputElement
    await new DOMWrapper(name).setValue('helper')
    // The switch is on: a primary agent is what the dialog has always made.
    await new DOMWrapper(document.querySelector('[data-testid="new-agent-create"]') as HTMLElement).trigger('click')
    await flushPromises()

    expect(create).toHaveBeenCalledWith({ id: 'helper' })
  })

  it('creates a subagent definition with its prompt and switches nowhere', async () => {
    vi.spyOn(forebrainApi, 'primaryAgents').mockResolvedValue(roster('main') as PrimaryAgentsResponse)
    const create = vi.spyOn(forebrainApi, 'createPrimaryAgent').mockResolvedValue(roster('main') as PrimaryAgentsResponse)
    const switchSpy = vi.spyOn(forebrainApi, 'primaryAgentSwitch')
    const wrapper = await mountShellWithModal()
    await vi.waitFor(() => expect(usePrimaryAgents().activeId.value).toBe('main'))

    await openDialog(wrapper)
    // The switch is the library's own button: a click toggles it and emits
    // the update the v-model carries.
    await new DOMWrapper(document.querySelector('[data-testid="new-agent-primary"]')).trigger('click')
    await flushPromises()
    const prompt = document.querySelector('[data-testid="new-agent-system-prompt"]') as HTMLTextAreaElement
    expect(prompt).not.toBeNull()
    await new DOMWrapper(prompt).setValue('You audit repositories.')
    const name = document.querySelector('[data-testid="new-agent-name"]') as HTMLInputElement
    await new DOMWrapper(name).setValue('repo-auditor')
    await new DOMWrapper(document.querySelector('[data-testid="new-agent-create"]') as HTMLElement).trigger('click')
    await flushPromises()

    expect(create).toHaveBeenCalledWith({ id: 'repo-auditor', primary: false, systemPrompt: 'You audit repositories.' })
    // A subagent definition joins no tenant switch: the shell stays on main.
    expect(switchSpy).not.toHaveBeenCalled()
  })

  it('refuses a subagent definition without its prompt before calling', async () => {
    vi.spyOn(forebrainApi, 'primaryAgents').mockResolvedValue(roster('main') as PrimaryAgentsResponse)
    const create = vi.spyOn(forebrainApi, 'createPrimaryAgent')
    const wrapper = await mountShellWithModal()
    await vi.waitFor(() => expect(usePrimaryAgents().activeId.value).toBe('main'))

    await openDialog(wrapper)
    // The switch is the library's own button: a click toggles it and emits
    // the update the v-model carries.
    await new DOMWrapper(document.querySelector('[data-testid="new-agent-primary"]')).trigger('click')
    await flushPromises()
    const name = document.querySelector('[data-testid="new-agent-name"]') as HTMLInputElement
    await new DOMWrapper(name).setValue('repo-auditor')
    await new DOMWrapper(document.querySelector('[data-testid="new-agent-create"]') as HTMLElement).trigger('click')
    await flushPromises()

    expect(create).not.toHaveBeenCalled()
    expect((document.querySelector('[data-testid="new-agent-error"]') as HTMLElement).textContent).toContain('子代理定义需要系统提示词')
  })
})
