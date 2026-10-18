import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { forebrainApi, type RuntimeInfo, type SkillRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import RuntimeStatusTab from './RuntimeStatusTab.vue'

/**
 * The settings page's runtime tab (plan 076): one flat key-value list — no
 * card, no title row — of what the gateway answered, which build this is, where
 * it listens and what it keeps. A fact the gateway did not answer is —.
 */
const INFO: RuntimeInfo = {
  status: 'ok',
  auth: 'token',
  agents: 3,
  home: '/h',
  logsDir: '/h/logs',
  stateDir: '/h/state',
}

async function mountTab(): Promise<VueWrapper> {
  const wrapper = mount(RuntimeStatusTab)
  await flushPromises()
  return wrapper
}

function valueOf(wrapper: VueWrapper, label: string): string {
  const term = wrapper.findAll('dt').find((dt) => dt.text() === label)
  return term?.element.nextElementSibling?.textContent?.trim() ?? ''
}

describe('RuntimeStatusTab', () => {
  beforeEach(() => {
    setLocale('zh')
    vi.spyOn(forebrainApi, 'runtimeInfo').mockResolvedValue(INFO)
    vi.spyOn(forebrainApi, 'skillsOverview').mockResolvedValue({
      installed: [
        { name: 'a', enabled: true } as SkillRecord,
        { name: 'b', enabled: true } as SkillRecord,
        { name: 'c', enabled: false } as SkillRecord,
      ],
      actions: [],
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 200 })))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('draws one flat list of the nine facts, with no card and no title', async () => {
    const wrapper = await mountTab()

    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.find('.forebrain-card').exists()).toBe(false)
    expect(wrapper.findAll('dl.forebrain-kv dt').map((dt) => dt.text())).toEqual([
      '状态',
      '版本',
      '监听地址',
      '鉴权',
      '主代理',
      '主目录',
      '日志',
      '状态库',
      '技能',
    ])
  })

  it('answers each fact from the gateway, the build and the browser', async () => {
    const wrapper = await mountTab()

    expect(valueOf(wrapper, '状态')).toContain('正常')
    expect(valueOf(wrapper, '版本')).toBe(__FOREBRAIN_VERSION__)
    expect(valueOf(wrapper, '监听地址')).toBe(window.location.host)
    expect(valueOf(wrapper, '鉴权')).toBe('token')
    expect(valueOf(wrapper, '主代理')).toBe('3 个')
    expect(valueOf(wrapper, '主目录')).toBe('/h')
    expect(valueOf(wrapper, '日志')).toBe('/h/logs')
    expect(valueOf(wrapper, '状态库')).toBe('/h/state')
    expect(valueOf(wrapper, '技能')).toBe('发现 3 个，启用 2 个')
  })

  it('shows — for what the gateway did not answer', async () => {
    vi.mocked(forebrainApi.runtimeInfo).mockResolvedValue({} as RuntimeInfo)
    vi.mocked(forebrainApi.skillsOverview).mockRejectedValueOnce(new Error('not available'))
    const wrapper = await mountTab()

    expect(valueOf(wrapper, '鉴权')).toBe('—')
    expect(valueOf(wrapper, '主代理')).toBe('—')
    expect(valueOf(wrapper, '主目录')).toBe('—')
    expect(valueOf(wrapper, '日志')).toBe('—')
    expect(valueOf(wrapper, '状态库')).toBe('—')
    expect(valueOf(wrapper, '技能')).toBe('—')
  })

  it('says 异常 with the answer it got when the health check fails', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 503 })))
    const wrapper = await mountTab()

    const status = valueOf(wrapper, '状态')
    expect(status).toContain('异常')
    expect(status).toContain('HTTP 503')
  })
})
