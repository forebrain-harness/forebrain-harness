import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import CompareDialog from './CompareDialog.vue'
import { forebrainApi, type SkillFileDiff, type SkillDiffResponse } from '@/lib/api'
import { setLocale } from '@/locales'
import { InlineTeleport } from '@/test-utils/portal'

config.global.directives = { tip }
setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

// The unified diff Step 2's engine test pins, as the one-file answer carries
// it: 3 added rows, 2 removed.
const skillDiffText =
  '--- a/SKILL.md\n+++ b/SKILL.md\n@@ -1,8 +1,9 @@\n ---\n name: gateway-ops\n-description: test skill gateway-ops\n+description: d\n ---\n \n line one\n-line two\n+line two changed\n line three\n+line four\n'
const newFileDiffText = '@@ -0,0 +1 @@\n+brand new\n'

function listResponse(): SkillDiffResponse {
  return {
    name: 'gateway-ops',
    from: 'v1',
    to: 'draft',
    files: [
      { path: 'SKILL.md', status: 'modified', added: 3, removed: 2 },
      { path: 'new.txt', status: 'added', added: 1, removed: 0 },
    ],
  }
}

function mountDialog(extra: Record<string, unknown> = {}) {
  return mount(CompareDialog, {
    props: { open: true, skillName: 'gateway-ops', from: 'v1', to: 'draft', ...extra },
    global: { stubs: { Teleport: InlineTeleport } },
  })
}

describe('CompareDialog', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillDiff').mockResolvedValue(listResponse())
  })
  afterEach(() => vi.restoreAllMocks())

  it('draws the selected file as line rows, not a line count', async () => {
    const fileSpy = vi.spyOn(forebrainApi, 'skillDiffFile').mockResolvedValue({
      path: 'SKILL.md', status: 'modified', added: 3, removed: 2, unifiedDiff: skillDiffText,
    } satisfies SkillFileDiff)
    const wrapper = mountDialog()
    await flush()
    const pane = wrapper.find('[data-testid="workshop-compare-pane"]')
    expect(pane.find('[data-testid="diff-block"]').exists()).toBe(true)
    expect(pane.findAll('.dr.add')).toHaveLength(3)
    expect(pane.findAll('.dr.del')).toHaveLength(2)
    expect(fileSpy.mock.calls[0]![3]).toBe('SKILL.md')
  })

  it('draws a newly added file as plain code', async () => {
    vi.spyOn(forebrainApi, 'skillDiffFile')
      .mockResolvedValueOnce({ path: 'SKILL.md', status: 'modified', added: 3, removed: 2, unifiedDiff: skillDiffText } satisfies SkillFileDiff)
      .mockResolvedValueOnce({ path: 'new.txt', status: 'added', added: 1, removed: 0, unifiedDiff: newFileDiffText } satisfies SkillFileDiff)
    const wrapper = mountDialog()
    await flush()
    await wrapper.find('[data-compare-file="new.txt"]').trigger('click')
    await flush()
    const pane = wrapper.find('[data-testid="workshop-compare-pane"]')
    expect(pane.findAll('.dr.add')).toHaveLength(0)
    expect(pane.text()).toContain('brand new')
  })

  it('opens on the file the entry point named', async () => {
    const fileSpy = vi.spyOn(forebrainApi, 'skillDiffFile').mockResolvedValue({
      path: 'new.txt', status: 'added', added: 1, removed: 0, unifiedDiff: newFileDiffText,
    } satisfies SkillFileDiff)
    const wrapper = mountDialog({ initialPath: 'new.txt' })
    await flush()
    expect(fileSpy.mock.calls[0]![3]).toBe('new.txt')
    expect(wrapper.find('[data-compare-file="new.txt"]').classes()).toContain('on')
  })

  it('says why a file has no lines to draw', async () => {
    const cases: Array<{ file: SkillFileDiff; hint: string }> = [
      { file: { path: 'SKILL.md', status: 'modified', added: 3, removed: 2, binary: true }, hint: '二进制文件，不显示内容' },
      { file: { path: 'SKILL.md', status: 'modified', added: 3, removed: 2, tooLarge: true }, hint: '文件太大，不显示差异' },
      { file: { path: 'SKILL.md', status: 'unchanged', added: 0, removed: 0 }, hint: '内容相同' },
    ]
    for (const { file, hint } of cases) {
      vi.spyOn(forebrainApi, 'skillDiffFile').mockResolvedValue(file)
      const wrapper = mountDialog()
      await flush()
      const pane = wrapper.find('[data-testid="workshop-compare-pane"]')
      expect(pane.text()).toContain(hint)
      expect(pane.find('[data-testid="diff-block"]').exists()).toBe(false)
      wrapper.unmount()
      vi.restoreAllMocks()
      vi.spyOn(forebrainApi, 'skillDiff').mockResolvedValue(listResponse())
    }
  })

  it('keeps the last selected file when an earlier request lands late', async () => {
    let resolveFirst!: (file: SkillFileDiff) => void
    const pending = new Promise<SkillFileDiff>((resolve) => {
      resolveFirst = resolve
    })
    vi.spyOn(forebrainApi, 'skillDiffFile')
      .mockImplementationOnce(() => pending)
      .mockResolvedValueOnce({ path: 'new.txt', status: 'added', added: 1, removed: 0, unifiedDiff: newFileDiffText } satisfies SkillFileDiff)
    const wrapper = mountDialog()
    await flush()
    // SKILL.md's request hangs; new.txt answers and wins.
    await wrapper.find('[data-compare-file="new.txt"]').trigger('click')
    await flush()
    resolveFirst({ path: 'SKILL.md', status: 'modified', added: 3, removed: 2, unifiedDiff: skillDiffText })
    await flush()
    const pane = wrapper.find('[data-testid="workshop-compare-pane"]')
    expect(pane.text()).toContain('brand new')
    expect(pane.text()).not.toContain('line two changed')
  })

  it('shows the listing error instead of a blank dialog', async () => {
    vi.spyOn(forebrainApi, 'skillDiff').mockRejectedValue(new Error('diff route down'))
    const wrapper = mountDialog()
    await flush()
    const error = wrapper.find('[data-testid="workshop-compare-error"]')
    expect(error.exists()).toBe(true)
    expect(error.text()).toContain('diff route down')
  })
})
