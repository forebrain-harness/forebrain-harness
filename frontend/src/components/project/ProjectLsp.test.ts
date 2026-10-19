import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { hideTip, useTooltipState } from '@/composables/useTooltip'
import tip from '@/directives/tip'
import { forebrainApi, type ProjectLspRow, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectLsp from './ProjectLsp.vue'

/**
 * The project space's language-server tab (plan 075): one table in which a
 * pending entry, an allowed one and a declined one are rows, the decision in the
 * pending row's own action column, above the pager and the tab's note.
 */
function project(trusted = true): ProjectRecord {
  return {
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
    createdAt: 0,
    updatedAt: 0,
    trusted,
  }
}

function lspRows(): ProjectLspRow[] {
  return [
    { id: 'projectls', summary: 'projectls: runs `/bin/projectls`', status: 'pending', fingerprint: 'fp-projectls' },
    { id: 'gopls', summary: '', status: 'allowed', fingerprint: 'fp-gopls' },
    { id: 'rust-analyzer', summary: '', status: 'denied', fingerprint: 'fp-rust-analyzer' },
  ]
}

async function mountTab(trusted = true) {
  const wrapper = mount(ProjectLsp, {
    props: { project: project(trusted), projectId: 'p1' },
    global: { directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectLsp', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectLspPage').mockResolvedValue({
      rows: lspRows(),
      trusted: true,
      notes: [],
      total: 3,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'projectLspConsent').mockResolvedValue({ ok: true, allowed: [] })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('draws one table under the tab toolbar, with the pager and the note', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('.forebrain-list-toolbar input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-lsp-row]')).toHaveLength(3)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 3 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toContain('lsp_servers.yaml')
  })

  it('names each row status and shows a summary only where there is one', async () => {
    const wrapper = await mountTab()

    const pending = wrapper.find('[data-lsp-row="projectls"]')
    expect(pending.text()).toContain('projectls: runs `/bin/projectls`')
    expect(pending.text()).toContain('待确认')

    const allowed = wrapper.find('[data-lsp-row="gopls"]')
    expect(allowed.text()).toContain('已允许')
    expect(allowed.text()).toContain('—')

    const denied = wrapper.find('[data-lsp-row="rust-analyzer"]')
    expect(denied.text()).toContain('已拒绝')
  })

  it('offers allow and decline only on a pending row', async () => {
    const wrapper = await mountTab()

    expect(wrapper.findAll('[data-lsp-row="projectls"] .forebrain-row-action')).toHaveLength(2)
    // A declined row keeps one action — the way back — and one hole.
    expect(wrapper.findAll('[data-lsp-row="rust-analyzer"] .forebrain-row-action')).toHaveLength(1)
    expect(wrapper.findAll('[data-lsp-row="rust-analyzer"] .forebrain-icon-slot')).toHaveLength(1)
    // A decided and allowed row keeps the action column's holes, so the icons
    // stay aligned.
    expect(wrapper.find('[data-lsp-row="gopls"] .forebrain-row-action').exists()).toBe(false)
    expect(wrapper.findAll('[data-lsp-row="gopls"] .forebrain-icon-slot')).toHaveLength(2)
  })

  it('records an allow through projectLspConsent and reloads', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-testid="lsp-allow-projectls"]').trigger('click')
    await flushPromises()

    // The request names the one entry the click decides together with the
    // fingerprint its row was rendered from, so the project's other entries
    // keep whatever decision they have.
    expect(forebrainApi.projectLspConsent).toHaveBeenCalledWith('p1', ['projectls'], ['projectls'], [
      { name: 'projectls', fingerprint: 'fp-projectls' },
    ])
    expect(forebrainApi.projectLspPage).toHaveBeenCalledTimes(2)
  })

  it('records a decline with an empty allow list', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-testid="lsp-decline-projectls"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.projectLspConsent).toHaveBeenCalledWith('p1', [], ['projectls'], [
      { name: 'projectls', fingerprint: 'fp-projectls' },
    ])
  })

  it('allows a declined entry again, which is the only way back', async () => {
    const wrapper = await mountTab()

    const undo = wrapper.find('[data-testid="lsp-undecline-rust-analyzer"]')
    expect(undo.exists()).toBe(true)
    expect(wrapper.find('[data-testid="lsp-decline-rust-analyzer"]').exists()).toBe(false)

    // The badge says how the decision is taken back.
    hideTip()
    await wrapper.find('[data-lsp-row="rust-analyzer"] .forebrain-badge').trigger('mouseenter')
    expect(useTooltipState().text).toBe('不会启用；点“允许”撤回')

    await undo.trigger('click')
    await flushPromises()

    expect(forebrainApi.projectLspConsent).toHaveBeenCalledWith('p1', ['rust-analyzer'], ['rust-analyzer'], [
      { name: 'rust-analyzer', fingerprint: 'fp-rust-analyzer' },
    ])
    expect(forebrainApi.projectLspPage).toHaveBeenCalledTimes(2)
  })

  it('keeps the server’s 409 text up and re-reads the table when the file changed under it', async () => {
    const wrapper = await mountTab()
    expect(forebrainApi.projectLspPage).toHaveBeenCalledTimes(1)
    vi.mocked(forebrainApi.projectLspConsent).mockRejectedValueOnce({
      response: {
        status: 409,
        data: {
          error: '项目文件已变化，请核对后重新确认 / The project file changed; review and confirm again',
          code: 'consent_stale',
          stale: ['projectls'],
        },
      },
    })

    await wrapper.find('[data-testid="lsp-allow-projectls"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.projectLspConsent).toHaveBeenCalledWith('p1', ['projectls'], ['projectls'], [
      { name: 'projectls', fingerprint: 'fp-projectls' },
    ])
    // Nothing was recorded: the server's own text stays up, and the rows are
    // asked for again so the entry reappears with its new summary and
    // fingerprint.
    expect(wrapper.text()).toContain('项目文件已变化，请核对后重新确认')
    expect(forebrainApi.projectLspPage).toHaveBeenCalledTimes(2)
  })

  it('keeps the trust gate in front of the table', async () => {
    const wrapper = await mountTab(false)

    expect(wrapper.find('[data-testid="project-trust"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap').exists()).toBe(false)
    expect(wrapper.find('p.forebrain-list-note').exists()).toBe(false)
  })
})
