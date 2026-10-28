import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import { humanSize, type ArtifactView } from '@/lib/artifacts'
import ArtifactCard from './ArtifactCard.vue'

config.global.directives = { tip }

function artifactOf(overrides: Partial<ArtifactView> = {}): ArtifactView {
  return {
    sessionId: 'session-1',
    id: 'art-1',
    origin: 'model',
    name: 'Q3 经营分析报告.docx',
    title: 'Q3 经营分析报告.docx',
    kind: 'document',
    preview: 'docx',
    size: 253952,
    missing: false,
    changed: false,
    downloadUrl: '/api/chat/sessions/session-1/artifacts/art-1/content?disposition=attachment',
    contentUrl: '/api/chat/sessions/session-1/artifacts/art-1/content?disposition=inline',
    ...overrides,
  }
}

describe('ArtifactCard', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => setLocale('zh'))

  it('draws the file, its kind and its size the way the engine words them', () => {
    const wrapper = mount(ArtifactCard, { props: { artifact: artifactOf() } })

    expect(wrapper.get('.m b').text()).toBe('Q3 经营分析报告.docx')
    expect(wrapper.get('.m span').text()).toBe('文档 · 248.0 KB')
    // The glyph is the kind's own: a document, not a generic file.
    expect(wrapper.find('.art-file-icon svg').exists()).toBe(true)
  })

  it('sizes a file as pkg/tool/artifacts.go HumanSize does', () => {
    expect(humanSize(0)).toBe('0 B')
    expect(humanSize(999)).toBe('999 B')
    expect(humanSize(1024)).toBe('1.0 KB')
    expect(humanSize(253952)).toBe('248.0 KB')
    expect(humanSize(3416064)).toBe('3.3 MB')
    expect(humanSize(1610612736)).toBe('1.5 GB')
  })

  it('offers the preview only for a file that says how it is previewed', () => {
    const previewable = mount(ArtifactCard, { props: { artifact: artifactOf() } })
    expect(previewable.find('[data-testid="artifact-preview"]').exists()).toBe(true)

    // psd / cad and the rest of the download-only kinds carry no preview.
    const downloadOnly = mount(ArtifactCard, { props: { artifact: artifactOf({ preview: '', kind: 'design' }) } })
    expect(downloadOnly.find('[data-testid="artifact-preview"]').exists()).toBe(false)
    expect(downloadOnly.get('.m span').text()).toBe('设计稿 · 248.0 KB')
  })

  it('never previews a file whose bytes are gone', () => {
    const wrapper = mount(ArtifactCard, { props: { artifact: artifactOf({ missing: true }) } })
    expect(wrapper.find('[data-testid="artifact-preview"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="artifact-download"]').attributes('href')).toContain('disposition=attachment')
  })

  it('marks a deleted file and a changed one, one at a time', () => {
    const missing = mount(ArtifactCard, { props: { artifact: artifactOf({ missing: true }) } })
    expect(missing.get('[data-testid="artifact-missing"]').text()).toBe('已删除')
    expect(missing.find('[data-testid="artifact-changed"]').exists()).toBe(false)

    const changed = mount(ArtifactCard, { props: { artifact: artifactOf({ changed: true }) } })
    expect(changed.get('[data-testid="artifact-changed"]').text()).toBe('已改动')

    const plain = mount(ArtifactCard, { props: { artifact: artifactOf() } })
    expect(plain.find('[data-testid="artifact-missing"]').exists()).toBe(false)
    expect(plain.find('[data-testid="artifact-changed"]').exists()).toBe(false)
  })

  it('downloads by the attachment link the row carries, and asks to preview that row', async () => {
    const artifact = artifactOf()
    const wrapper = mount(ArtifactCard, { props: { artifact } })

    const link = wrapper.get('[data-testid="artifact-download"]')
    expect(link.attributes('href')).toBe(artifact.downloadUrl)
    expect(link.attributes('download')).toBeDefined()

    await wrapper.get('[data-testid="artifact-preview"]').trigger('click')
    expect(wrapper.emitted('preview')?.[0]).toEqual([artifact])
  })
})
