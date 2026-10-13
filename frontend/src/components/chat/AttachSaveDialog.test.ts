import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const projectDirs = vi.fn()
const projectDirCreate = vi.fn()

vi.mock('@/lib/api', () => ({
  forebrainApi: {
    projectDirs: (...args: unknown[]) => projectDirs(...args),
    projectDirCreate: (...args: unknown[]) => projectDirCreate(...args),
  },
}))

import { setLocale } from '@/locales'
import AttachSaveDialog, { type AttachFileRow } from './AttachSaveDialog.vue'

function listing(path: string, entries: Array<{ name: string; hasChildren?: boolean }>) {
  return { path, entries: entries.map((e) => ({ name: e.name, hasChildren: e.hasChildren ?? false })) }
}

/** The "save to" dialog: the files to save, the project's directory tree
 * loaded lazily, and a new folder made in place. */
describe('AttachSaveDialog', () => {
  beforeAll(() => setLocale('en'))

  beforeEach(() => {
    projectDirs.mockReset()
    projectDirCreate.mockReset()
    projectDirs.mockImplementation((_id: string, path: string) => {
      if (path === '') {
        return Promise.resolve(listing('', [
          { name: 'docs', hasChildren: true },
          { name: 'inbox', hasChildren: false },
        ]))
      }
      if (path === 'docs') return Promise.resolve(listing('docs', [{ name: 'inner', hasChildren: false }]))
      return Promise.reject(new Error('HTTP 404'))
    })
  })

  const files: AttachFileRow[] = [
    { filename: 'shot.png', size: 2048, mediaType: 'image/png', fromClipboard: true },
  ]

  function mountDialog(initialDir = '') {
    return mount(AttachSaveDialog, {
      props: { open: true, project: { id: 'prj_1', name: 'demo' }, files, initialDir },
      global: { stubs: { teleport: true } },
    })
  }

  it('opens on the project root and shows the file with its clipboard origin', async () => {
    const w = mountDialog()
    await flushPromises()
    expect(w.find('[data-testid="attach-root"]').classes()).toContain('on')
    expect(w.text()).toContain('shot.png')
    expect(w.text()).toContain('from clipboard')
    expect(projectDirs).toHaveBeenCalledWith('prj_1', '')
  })

  it('selects the last attach directory and marks it', async () => {
    const w = mountDialog('docs')
    await flushPromises()
    const selected = w.find('[data-testid="attach-dir-docs"]')
    expect(selected.classes()).toContain('on')
    expect(selected.text()).toContain('Last')
  })

  it('falls back to the root when the remembered directory is gone', async () => {
    const w = mountDialog('missing')
    await flushPromises()
    expect(w.find('[data-testid="attach-root"]').classes()).toContain('on')
    expect(w.find('[data-testid="attach-dir-missing"]').exists()).toBe(false)
  })

  it('lazily expands a directory on click', async () => {
    const w = mountDialog()
    await flushPromises()
    expect(projectDirs).not.toHaveBeenCalledWith('prj_1', 'docs')
    await w.find('[data-testid="attach-dir-docs"]').trigger('click')
    await flushPromises()
    expect(projectDirs).toHaveBeenCalledWith('prj_1', 'docs')
    expect(w.text()).toContain('inner')
  })

  it('makes a folder in the selected directory and selects it', async () => {
    projectDirCreate.mockResolvedValue({ path: 'inbox/new' })
    const w = mountDialog()
    await flushPromises()
    await w.find('[data-testid="attach-dir-inbox"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="attach-new-folder"]').trigger('click')
    await flushPromises()
    await w.find('.sheet input').setValue('new')
    await w.find('.sheet .btn.pri').trigger('click')
    await flushPromises()
    expect(projectDirCreate).toHaveBeenCalledWith('prj_1', 'inbox', 'new')
    expect(w.find('[data-testid="attach-dir-inbox/new"]').classes()).toContain('on')
  })

  it('confirms with the selected directory', async () => {
    const w = mountDialog()
    await flushPromises()
    await w.find('[data-testid="attach-dir-docs"]').trigger('click')
    await flushPromises()
    await w.find('[data-testid="attach-confirm"]').trigger('click')
    expect(w.emitted('confirm')?.[0]).toEqual(['docs'])
  })

  it('cancels without confirming', async () => {
    const w = mountDialog()
    await flushPromises()
    await w.find('[data-testid="attach-cancel"]').trigger('click')
    expect(w.emitted('cancel')).toHaveLength(1)
    expect(w.emitted('confirm')).toBeUndefined()
  })
})
