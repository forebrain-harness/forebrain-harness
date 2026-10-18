import { beforeAll, describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import type { ProjectRecord } from '@/lib/api'
import type { ProjectTarget } from '@/lib/projectTarget'
import ProjectPopover from './ProjectPopover.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

function project(over: Partial<ProjectRecord>): ProjectRecord {
  return {
    id: 'p',
    name: 'p',
    icon: '',
    description: '',
    instructions: '',
    root: '/p',
    projectKey: '',
    memoryScope: 'shared',
    resourceAccess: true,
    pinned: false,
    createdAt: 0,
    updatedAt: 0,
    ...over,
  }
}

/** The project chooser: projects listed with their roots, a project whose
 * directory is gone disabled, one never judged badged, a search that filters,
 * and the entry that opens the folder picker. */
describe('ProjectPopover', () => {
  beforeAll(() => setLocale('en'))

  const projects: ProjectRecord[] = [
    project({ id: 'a', name: 'alpha', root: '/work/alpha', trustLevel: 'trusted' }),
    project({ id: 'b', name: 'beta', root: '/work/beta', trustLevel: 'unknown' }),
    project({ id: 'c', name: 'gone', root: '/work/gone', trustLevel: 'trusted', missing: true }),
  ]

  function mountPopover(selected: ProjectTarget | null = null) {
    return mount(ProjectPopover, { props: { projects, selected } })
  }

  it('lists every project with its root', () => {
    const rows = mountPopover().findAll('.pj-opt')
    const names = rows.map((row) => row.find('.nm').text())
    expect(names).toEqual(['alpha', 'beta', 'gone', 'Open folder…'])
  })

  it('disables a project whose directory is gone and badges it', () => {
    const gone = mountPopover().findAll('.pj-opt').find((row) => row.find('.nm').text() === 'gone')!
    expect(gone.attributes('disabled')).toBeDefined()
    expect(gone.find('.badge.err').exists()).toBe(true)
  })

  it('badges a project that has never been judged', () => {
    const beta = mountPopover().findAll('.pj-opt').find((row) => row.find('.nm').text() === 'beta')!
    expect(beta.find('.badge.warn').exists()).toBe(true)
  })

  it('selects a project on click', async () => {
    const wrapper = mountPopover()
    const alpha = wrapper.findAll('.pj-opt').find((row) => row.find('.nm').text() === 'alpha')!
    await alpha.trigger('click')
    const emitted = wrapper.emitted('select')
    expect(emitted).toBeTruthy()
    expect((emitted![0][0] as { projectId: string }).projectId).toBe('a')
  })

  it('filters by name and by root', async () => {
    const wrapper = mountPopover()
    await wrapper.find('.srch input').setValue('bet')
    expect(wrapper.findAll('.pj-opt').map((row) => row.find('.nm').text())).toEqual(['beta', 'Open folder…'])
    await wrapper.find('.srch input').setValue('/work/gone')
    expect(wrapper.findAll('.pj-opt').map((row) => row.find('.nm').text())).toEqual(['gone', 'Open folder…'])
  })

  it('shows a new folder as a new project and opens the picker', async () => {
    const wrapper = mount(ProjectPopover, {
      props: {
        projects,
        selected: { folder: '/tmp/new', name: 'new', root: '/tmp/new', isNew: true, trustLevel: 'unknown' },
      },
    })
    const first = wrapper.findAll('.pj-opt')[0]
    expect(first.text()).toContain('new')
    expect(first.find('.badge.ok').exists()).toBe(true)
    const browse = wrapper.findAll('.pj-opt').find((row) => row.find('.nm').text() === 'Open folder…')!
    await browse.trigger('click')
    expect(wrapper.emitted('browse')).toBeTruthy()
  })
})
