import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import SessionHeartbeat from './SessionHeartbeat.vue'
import { resetTenantScope } from '@/composables/useTenantScope'
import { forebrainApi, type HeartbeatRecord } from '@/lib/api'
import { setLocale } from '@/locales'

function beat(sessionId: string, extra: Partial<HeartbeatRecord> = {}): HeartbeatRecord {
  return { sessionId, intervalSeconds: 600, prompt: 'anything new?', paused: false, ...extra }
}

async function mountControl(sessionId: string) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div />' } }] })
  await router.push(`/?session=${sessionId}`)
  await router.isReady()
  const wrapper = mount(SessionHeartbeat, { props: { sessionId }, global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return { wrapper, router }
}

describe('SessionHeartbeat', () => {
  beforeAll(() => setLocale('en'))
  afterAll(() => setLocale('en'))
  beforeEach(() => resetTenantScope())
  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('says on the trigger whether this conversation has a running heartbeat', async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([beat('s1', { intervalSeconds: 1800 })])
    const { wrapper } = await mountControl('s1')

    expect(wrapper.find('[data-testid="heartbeat-status"]').text()).toBe('Every 30 min')
    expect(wrapper.find('[data-testid="heartbeat-toggle"]').classes()).toContain('heartbeat-trigger--running')
    wrapper.unmount()
  })

  it('opens on every click and starts a heartbeat for this conversation', async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([])
    const save = vi.spyOn(forebrainApi, 'saveHeartbeat').mockResolvedValue(beat('s1', { intervalSeconds: 900, prompt: 'news?' }))
    const { wrapper } = await mountControl('s1')

    expect(wrapper.find('[data-testid="heartbeat-status"]').exists()).toBe(false)
    await wrapper.find('[data-testid="heartbeat-toggle"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="heartbeat-popover"]').exists()).toBe(true)
    // Nothing to save until there is something to send.
    expect(wrapper.find('[data-testid="heartbeat-save"]').attributes('disabled')).toBeDefined()

    await wrapper.find('input[type="number"]').setValue('15')
    await wrapper.find('textarea').setValue('news?')
    await wrapper.find('[data-testid="heartbeat-save"]').trigger('click')
    await flushPromises()

    expect(save).toHaveBeenCalledWith({ sessionId: 's1', intervalSeconds: 900, prompt: 'news?', paused: false })
    expect(wrapper.find('[data-testid="heartbeat-status"]').text()).toBe('Every 15 min')
    // Saved and unchanged: nothing more to save.
    expect(wrapper.find('[data-testid="heartbeat-save"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('pauses with what is saved, keeping an unsaved edit in the form', async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([beat('s1')])
    const save = vi.spyOn(forebrainApi, 'saveHeartbeat').mockResolvedValue(beat('s1', { paused: true }))
    const { wrapper } = await mountControl('s1')
    await wrapper.find('[data-testid="heartbeat-toggle"]').trigger('click')
    await flushPromises()

    await wrapper.find('textarea').setValue('half-typed')
    await wrapper.find('[data-testid="heartbeat-switch"]').trigger('click')
    await flushPromises()

    expect(save).toHaveBeenCalledWith({ sessionId: 's1', intervalSeconds: 600, prompt: 'anything new?', paused: true })
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('half-typed')
    expect(wrapper.find('[data-testid="heartbeat-schedule"]').text()).toContain('Paused, nothing will be sent')
    expect(wrapper.find('[data-testid="heartbeat-status"]').text()).toBe('Paused')
    wrapper.unmount()
  })

  it("lists other conversations' heartbeats, each pausable and one click away", async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([
      beat('s1'),
      beat('s2', { sessionTitle: 'Release watch' }),
      beat('s3', { paused: true }),
    ])
    const save = vi.spyOn(forebrainApi, 'saveHeartbeat').mockResolvedValue(beat('s2', { paused: true }))
    const { wrapper, router } = await mountControl('s1')
    await wrapper.find('[data-testid="heartbeat-toggle"]').trigger('click')
    await flushPromises()

    const others = wrapper.find('[data-testid="heartbeat-others"]')
    expect(others.text()).toContain('Heartbeats in other conversations · 2')
    expect(others.text()).toContain('Release watch')
    expect(others.text()).toContain('Untitled session')

    await others.find('button[aria-label="Pause"]').trigger('click')
    await flushPromises()
    expect(save).toHaveBeenCalledWith({ sessionId: 's2', intervalSeconds: 600, prompt: 'anything new?', paused: true })

    await others.find('.heartbeat-other-open').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.session).toBe('s2')
    wrapper.unmount()
  })

  it('closes on Escape and on a press outside', async () => {
    vi.spyOn(forebrainApi, 'heartbeats').mockResolvedValue([])
    const { wrapper } = await mountControl('s1')
    const toggle = wrapper.find('[data-testid="heartbeat-toggle"]')

    await toggle.trigger('click')
    await flushPromises()
    await wrapper.find('textarea').trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[data-testid="heartbeat-popover"]').exists()).toBe(false)

    // A save disables the focused button and drops focus to the body; Escape
    // still closes.
    await toggle.trigger('click')
    await flushPromises()
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(wrapper.find('[data-testid="heartbeat-popover"]').exists()).toBe(false)

    await toggle.trigger('click')
    await flushPromises()
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(wrapper.find('[data-testid="heartbeat-popover"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
