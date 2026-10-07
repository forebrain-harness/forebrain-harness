import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { forebrainApi, type ProjectLspRecord, type ProjectRecord } from '@/lib/api'
import ProjectLsp from './ProjectLsp.vue'

const projectRecord = (trusted = true): ProjectRecord => ({
  id: 'p1',
  name: 'proj',
  icon: '',
  description: '',
  instructions: '',
  root: '/tmp/proj',
  projectKey: 'proj',
  memoryScope: '',
  resourceAccess: false,
  pinned: false,
  archivedAt: 0,
  createdAt: 0,
  updatedAt: 0,
  trusted,
})

const recordOf = (overrides: Partial<ProjectLspRecord> = {}): ProjectLspRecord => ({
  trusted: true,
  pending: [
    { id: 'projectls', summary: 'projectls: runs `/bin/projectls`' },
    { id: 'gopls', summary: 'gopls: changes settings of the built-in gopls server' },
  ],
  allowed: [],
  denied: [],
  notes: ['gopls: env_passthrough and priority are ignored in project files'],
  ...overrides,
})

async function mountTab(record: ProjectLspRecord, trusted = true) {
  vi.mocked(forebrainApi.projectLsp).mockResolvedValue(record)
  const wrapper = mount(ProjectLsp, {
    props: { project: projectRecord(trusted), projectId: 'p1' },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectLsp', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectLsp').mockResolvedValue(recordOf())
    vi.spyOn(forebrainApi, 'projectLspConsent').mockResolvedValue({ ok: true, allowed: [] })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders the pending entries with their summaries and the notes', async () => {
    const wrapper = await mountTab(recordOf())
    expect(wrapper.find('[data-testid="project-lsp-pending"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('projectls')
    expect(wrapper.text()).toContain('projectls: runs `/bin/projectls`')
    expect(wrapper.text()).toContain('gopls: changes settings of the built-in gopls server')
    expect(wrapper.text()).toContain('env_passthrough and priority are ignored in project files')
  })

  it('records an allow through projectLspConsent and reloads', async () => {
    const wrapper = await mountTab(recordOf())
    await wrapper.find('[data-testid="lsp-allow-projectls"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.projectLspConsent).toHaveBeenCalledWith('p1', ['projectls'])
    // The reload after the decision ran.
    expect(forebrainApi.projectLsp).toHaveBeenCalledTimes(2)
  })

  it('records a decline with an empty allow list', async () => {
    const wrapper = await mountTab(recordOf())
    await wrapper.find('[data-testid="lsp-decline-gopls"]').trigger('click')
    await flushPromises()
    expect(forebrainApi.projectLspConsent).toHaveBeenCalledWith('p1', [])
  })

  it('shows the allowed and denied lists after decisions exist', async () => {
    const wrapper = await mountTab(recordOf({ pending: [], allowed: ['projectls'], denied: ['gopls'] }))
    expect(wrapper.find('[data-testid="project-lsp-pending"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('projectls')
    // The denied id renders too, once it has a decision.
    expect(wrapper.text()).toContain('gopls')
  })
})
