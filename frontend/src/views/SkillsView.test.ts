import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type SkillRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import { openSelect, pickOption } from '@/test-utils/formControls'
import SkillsView from './SkillsView.vue'

/**
 * The primary agent's skills (plan 073): a toolbar whose search and layer
 * filter are the server's, the shared skill table, a pager and the page's note
 * — and no title row, because the top bar already names the page.
 */
const mounted: VueWrapper[] = []

function skill(overrides: Partial<SkillRecord> = {}): SkillRecord {
  return {
    name: 'gateway-ops',
    description: '网关排障',
    origin: 'agent',
    enabled: true,
    rootPath: '/s/gateway-ops',
    version: 3,
    state: 'published',
    downloadUrl: '/api/skills/gateway-ops/download',
    ...overrides,
  }
}

async function mountPage(): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(SkillsView, { global: { plugins: [router], directives: { tip } } })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

/**
 * The layer filter is the library's select: it measures its viewport with
 * ResizeObserver, which jsdom does not have, and scrolling an item into view is
 * not implemented either.
 */
function stubSelectMeasurement(): void {
  ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Element.prototype.scrollIntoView = () => undefined
}

describe('SkillsView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillsPage').mockResolvedValue({
      rows: [skill(), skill({ name: 'golang-testing', description: '共享', origin: 'shared', rootPath: '/h/golang-testing' })],
      actions: [],
      total: 2,
      page: 1,
      pageSize: 10,
    })
    vi.spyOn(forebrainApi, 'skillsOverview').mockResolvedValue({
      installed: [
        skill(),
        skill({ name: 'golang-testing', description: '共享', origin: 'shared', rootPath: '/h/golang-testing' }),
      ],
      actions: [],
    })
    vi.spyOn(forebrainApi, 'skillsToggle').mockResolvedValue({ status: 'ok', skills: [] })
  })

  afterEach(() => {
    while (mounted.length) mounted.pop()?.unmount()
    vi.restoreAllMocks()
  })

  it('opens on a search toolbar, the table, the pager and the note — no title row', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('h2').exists()).toBe(false)

    const toolbar = wrapper.find('.forebrain-list-toolbar')
    expect(toolbar.find('input[type="search"]').exists()).toBe(true)
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)
    expect(wrapper.findAll('[data-skill-row]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toMatch(/^本主代理实际加载的技能集合/)
  })

  it('offers the layers this page can hold, and no other', async () => {
    // The dropdown needs the jsdom stubs above before it can open.
    stubSelectMeasurement()
    const wrapper = await mountPage()

    // The layer filter is the library's select: its items exist only while it
    // is open, and reka renders them into a portal of its own.
    await openSelect(wrapper.find('[data-testid="skills-source-filter"]').element as HTMLElement)
    const list = document.querySelector('[role="listbox"][data-state="open"]')
    expect([...(list?.querySelectorAll('[role="option"]') ?? [])].map((option) => (option.textContent ?? '').trim())).toEqual([
      '全部来源',
      '主代理',
      '共享',
      '内置',
    ])
  })

  it('sends the layer filter to the server instead of filtering in the browser', async () => {
    // The dropdown needs the jsdom stubs above before it can open.
    stubSelectMeasurement()
    const wrapper = await mountPage()
    const page = vi.mocked(forebrainApi.skillsPage)

    await pickOption(wrapper.find('[data-testid="skills-source-filter"]').element as HTMLElement, '共享')
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(2)
    expect(page.mock.calls[1][0]).toMatchObject({ origin: 'shared', page: 1 })
    // The answer is drawn as it came: the page never hides a row of its own.
    expect(wrapper.findAll('[data-skill-row]')).toHaveLength(2)

    // 全部来源 is the empty string on the wire: the sentinel maps back to it.
    await pickOption(wrapper.find('[data-testid="skills-source-filter"]').element as HTMLElement, '全部来源')
    await flushPromises()
    expect(page.mock.calls[2][0]).toMatchObject({ origin: '', page: 1 })
  })

  it('keeps every action the page had, as icon buttons on the rows', async () => {
    const wrapper = await mountPage()

    expect(wrapper.find('[data-testid="skills-offline-install"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="skills-online-install"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="skills-discover"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="skills-create"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="skills-batch-download"]').exists()).toBe(true)

    // 查看 is the old 详情 button, now an eye icon that names its row.
    const own = wrapper.find('[data-skill-row="gateway-ops"]')
    expect(own.find('[data-testid="skill-detail-gateway-ops"]').attributes('aria-label')).toBe('查看 gateway-ops')
    expect(wrapper.text()).not.toContain('详情')
    expect(own.find('td:last-child button.forebrain-btn').exists()).toBe(false)
  })

  it('submits the whole enabled set when a switch is turned, not the page', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="skill-toggle-gateway-ops"]').trigger('click')
    await flushPromises()

    // The endpoint takes the complete list, so the page reads it back first.
    expect(forebrainApi.skillsOverview).toHaveBeenCalled()
    expect(vi.mocked(forebrainApi.skillsToggle).mock.calls[0][0]).toEqual(['/h/golang-testing'])
    // …and the table is read again afterwards.
    expect(vi.mocked(forebrainApi.skillsPage).mock.calls.length).toBe(2)
  })

  it('downloads only the rows ticked on this page', async () => {
    const batch = vi.spyOn(forebrainApi, 'skillDownloadBatch').mockResolvedValue({ blob: new Blob(), filename: 'skills.zip' })
    const wrapper = await mountPage()

    const own = wrapper.find('[data-skill-row="gateway-ops"]')
    await own.find('[role="checkbox"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('已选 1 个')

    await wrapper.find('[data-testid="skills-batch-download"]').trigger('click')
    await flushPromises()
    expect(batch.mock.calls[0][0]).toEqual(['gateway-ops'])
  })
})
