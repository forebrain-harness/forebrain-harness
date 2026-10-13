import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const hostDirs = vi.fn()
const hostDirCreate = vi.fn()

vi.mock('@/lib/api', () => ({
  forebrainApi: {
    hostDirs: (...args: unknown[]) => hostDirs(...args),
    hostDirCreate: (...args: unknown[]) => hostDirCreate(...args),
  },
  getErrorMessage: (error: unknown) => String(error),
}))

import { setLocale } from '@/locales'
import type { HostDirEntry } from '@/lib/api'
import FolderDialog from './FolderDialog.vue'

function listing(path: string, entries: HostDirEntry[], extra: { parent?: string; places?: Array<{ name: string; path: string }> } = {}) {
  return {
    path,
    parent: extra.parent ?? '',
    ancestors: [path],
    places: extra.places ?? [],
    entries,
  }
}

const dir = (name: string, over: Partial<HostDirEntry> = {}): HostDirEntry => ({ name, mtime: 0, blocked: false, ...over })

/** The system-style folder picker: navigate, filter, select, and make a
 * folder — with the forebrain home shown as a locked row. */
describe('FolderDialog', () => {
  beforeAll(() => setLocale('en'))

  beforeEach(() => {
    hostDirs.mockReset()
    hostDirCreate.mockReset()
    hostDirs.mockImplementation((target?: string) => {
      if (!target || target === '/root') {
        return Promise.resolve(listing('/root', [dir('a'), dir('b'), dir('.forebrain', { blocked: true })], {
          parent: '/',
          places: [{ name: '主目录', path: '/home' }],
        }))
      }
      if (target === '/root/a') {
        return Promise.resolve(listing('/root/a', [dir('inner')], { parent: '/root' }))
      }
      return Promise.resolve(listing(target, [], { parent: '/' }))
    })
  })

  async function mountDialog() {
    const wrapper = mount(FolderDialog, { props: { initialPath: '/root' } })
    await flushPromises()
    return wrapper
  }

  it('lists subdirectories, shows places, and locks the forebrain home', async () => {
    const wrapper = await mountDialog()
    const rows = wrapper.findAll('.fb-row').map((row) => row.text())
    expect(rows.some((text) => text.includes('a'))).toBe(true)
    expect(rows.some((text) => text.includes('b'))).toBe(true)
    const home = wrapper.findAll('.fb-row').find((row) => row.text().includes('.forebrain'))!
    expect(home.classes()).toContain('off')
    expect(home.element.tagName).toBe('DIV') // not a selectable option
    expect(home.text()).toContain('Not selectable')
    expect(wrapper.find('.fb-side').text()).toContain('主目录')
  })

  it('enters a folder on double click and goes back up', async () => {
    const wrapper = await mountDialog()
    const a = wrapper.findAll('.fb-row').find((row) => row.text().startsWith('a'))!
    await a.trigger('dblclick')
    await flushPromises()
    expect(hostDirs).toHaveBeenCalledWith('/root/a')
    expect(wrapper.find('.fb-list').text()).toContain('inner')
    // Up one level.
    await wrapper.findAll('.icon-btn')[2].trigger('click')
    await flushPromises()
    expect(hostDirs).toHaveBeenLastCalledWith('/root')
  })

  it('filters the current list', async () => {
    const wrapper = await mountDialog()
    await wrapper.find('.fb-search input').setValue('a')
    expect(wrapper.find('.fb-list').text()).not.toContain('bb')
  })

  it('selects a folder and chooses it', async () => {
    const wrapper = await mountDialog()
    const a = wrapper.findAll('.fb-row').find((row) => row.text().startsWith('a'))!
    await a.trigger('click')
    expect(wrapper.find('.sel-path').text()).toBe('/root/a')
    await wrapper.findAll('.modal-f .btn.pri')[0].trigger('click')
    expect(wrapper.emitted('select')).toEqual([['/root/a']])
  })

  it('creates a folder, reports a duplicate, and selects the new one', async () => {
    const wrapper = await mountDialog()
    await wrapper.find('.modal-f .btn.sm').trigger('click')
    await flushPromises()
    const sheet = wrapper.find('.sheet')
    expect(sheet.exists()).toBe(true)
    // A duplicate name: the sheet shows the refusal.
    hostDirCreate.mockRejectedValueOnce(new Error('exists'))
    await sheet.find('input').setValue('a')
    await sheet.findAll('.btn')[1].trigger('click')
    await flushPromises()
    expect(wrapper.find('.sheet .err').text()).toContain('already exists')
    // A fresh name: the directory is created and selected.
    hostDirCreate.mockResolvedValueOnce({ path: '/root/new' })
    hostDirs.mockImplementation((target?: string) => {
      if (target === '/root') {
        return Promise.resolve(listing('/root', [dir('a'), dir('new')], { parent: '/' }))
      }
      return Promise.resolve(listing('/root', [], {}))
    })
    await sheet.find('input').setValue('new')
    await sheet.findAll('.btn')[1].trigger('click')
    await flushPromises()
    expect(hostDirCreate).toHaveBeenLastCalledWith('/root', 'new')
    expect(wrapper.find('.sel-path').text()).toBe('/root/new')
  })
})
