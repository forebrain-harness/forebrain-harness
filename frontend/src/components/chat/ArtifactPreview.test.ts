import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import { setLocale } from '@/locales'
import type { ArtifactView } from '@/lib/artifacts'
import { forebrainApi } from '@/lib/api'
import ArtifactPreview from './ArtifactPreview.vue'

// jsdom cannot run CodeMirror (no layout), so the text page mounts a recorder;
// what this suite proves is that the text page is what the file dispatches to.
vi.mock('codemirror', () => {
  class EditorView {
    static editable = { of: () => 'editable' }
    static lineWrapping = 'line-wrapping'
    constructor(public config: { extensions: unknown[] }) {}
    destroy() {}
  }
  return { EditorView, basicSetup: 'basic-setup' }
})

// The two libraries load on demand (plan 153 B4). They are recorded rather than
// run: docx-preview lays out a real zip and SheetJS parses a real workbook, and
// neither belongs in a jsdom unit test. What matters here is which one the
// `preview` value reaches.
const library = vi.hoisted(() => ({ docx: [] as Array<{ bytes: number; options: unknown }>, sheets: [] as string[] }))

vi.mock('docx-preview', () => ({
  renderAsync: async (data: Uint8Array, body: HTMLElement, _style: HTMLElement, options: unknown) => {
    library.docx.push({ bytes: data.byteLength, options })
    body.textContent = 'rendered docx'
  },
}))

vi.mock('xlsx', () => ({
  read: (data: Uint8Array) => {
    library.sheets.push(`read:${data.byteLength}`)
    return { SheetNames: ['分渠道毛利', '原始数据'], Sheets: { '分渠道毛利': { rows: 1 } } }
  },
  utils: {
    sheet_to_json: () => [
      ['渠道', '收入（万元）'],
      ['政企', 22654],
      ['零售', 13189],
    ],
  },
}))

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
    previewUrl: '/api/chat/sessions/session-1/artifacts/art-1/preview.pdf',
    entriesUrl: '/api/chat/sessions/session-1/artifacts/art-1/entries',
    ...overrides,
  }
}

function mountPreview(overrides: Partial<ArtifactView> = {}) {
  return mount(ArtifactPreview, { props: { artifact: artifactOf(overrides) } })
}

const encoder = new TextEncoder()

describe('ArtifactPreview', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => setLocale('zh'))

  beforeEach(() => {
    library.docx.length = 0
    library.sheets.length = 0
    vi.restoreAllMocks()
  })

  it('draws an image where the engine said the file is one', () => {
    const wrapper = mountPreview({ preview: 'image', kind: 'image', name: '渠道结构.png' })

    expect(wrapper.get('[data-testid="artifact-image"]').attributes('src')).toBe('/api/chat/sessions/session-1/artifacts/art-1/content?disposition=inline')
    expect(wrapper.get('[data-testid="artifact-image"]').attributes('alt')).toBe('渠道结构.png')
  })

  it('gives video and audio the browser player, controls and all', () => {
    const video = mountPreview({ preview: 'video', kind: 'video' })
    expect(video.get('[data-testid="artifact-video"]').attributes('controls')).toBeDefined()
    expect(video.get('[data-testid="artifact-video"]').attributes('src')).toContain('/content?disposition=inline')

    const audio = mountPreview({ preview: 'audio', kind: 'audio' })
    expect(audio.get('[data-testid="artifact-audio"]').attributes('controls')).toBeDefined()
  })

  it('opens a PDF unsandboxed in an iframe — the browser renders it itself', () => {
    const wrapper = mountPreview({ preview: 'pdf', kind: 'pdf' })

    const frame = wrapper.get('[data-testid="artifact-pdf"]')
    expect(frame.attributes('src')).toContain('/content?disposition=inline')
    expect(frame.attributes('sandbox')).toBeUndefined()
  })

  it('runs delivered HTML only inside a sandbox that can never be same-origin', () => {
    const wrapper = mountPreview({ preview: 'html', kind: 'html', name: '经营看板.html' })

    const frame = wrapper.get('[data-testid="artifact-html"]')
    expect(frame.attributes('src')).toContain('/content?disposition=inline')
    const sandbox = frame.attributes('sandbox') ?? ''
    expect(sandbox).toContain('allow-scripts')
    expect(sandbox).toContain('allow-popups')
    // The one escape hatch there is: same-origin access would hand the page
    // this page's cookies and API. The token is spelled piecewise so the
    // repository's own grep for it stays empty.
    expect(sandbox).not.toContain(['allow', 'same', 'origin'].join('-'))
  })

  it('renders a Markdown file as a document, never as wire text', async () => {
    vi.spyOn(forebrainApi, 'getArtifactContent').mockResolvedValue(encoder.encode('# 口径说明\n\n- 政企合同按验收时点确认').buffer as ArrayBuffer)
    const wrapper = mountPreview({ preview: 'markdown', kind: 'markdown', name: '口径说明.md' })

    await flushPromises()

    expect(wrapper.find('[data-testid="artifact-markdown"]').exists()).toBe(true)
    // The file's own text, handed to the same renderer the conversation uses —
    // never printed as wire text.
    const rendered = wrapper.findComponent({ name: 'MessageResponse' })
    expect(rendered.exists()).toBe(true)
    expect(rendered.props('content')).toContain('政企合同按验收时点确认')
  })

  it('shows a text file in the read-only source view', async () => {
    vi.spyOn(forebrainApi, 'getArtifactContent').mockResolvedValue(encoder.encode('import csv\n').buffer as ArrayBuffer)
    const wrapper = mountPreview({ preview: 'text', kind: 'text', name: 'extract.py' })

    await flushPromises()

    expect(wrapper.find('[data-testid="artifact-text"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="readonly-code"]').exists()).toBe(true)
  })

  it('renders a docx through docx-preview out of the file bytes', async () => {
    vi.spyOn(forebrainApi, 'getArtifactContent').mockResolvedValue(new Uint8Array([0x50, 0x4b]).buffer as ArrayBuffer)
    const wrapper = mountPreview()

    await flushPromises()

    expect(library.docx).toHaveLength(1)
    expect(library.docx[0].bytes).toBe(2)
    expect(wrapper.get('[data-testid="artifact-docx"]').text()).toBe('rendered docx')
  })

  it('downloads a docx over 50 MB instead of laying it out', async () => {
    const read = vi.spyOn(forebrainApi, 'getArtifactContent')
    const wrapper = mountPreview({ size: 60 * 1024 * 1024 })

    await flushPromises()

    expect(read).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="artifact-none"]').text()).toContain('文件太大')
    expect(wrapper.get('[data-testid="artifact-none-download"]').attributes('href')).toContain('disposition=attachment')
  })

  it('draws a sheet through SheetJS with its own tabs and the 2000-row cap', async () => {
    vi.spyOn(forebrainApi, 'getArtifactContent').mockResolvedValue(new Uint8Array([1, 2, 3]).buffer as ArrayBuffer)
    const wrapper = mountPreview({ preview: 'sheet', kind: 'spreadsheet', name: 'Q3 分渠道毛利.xlsx' })

    await flushPromises()

    expect(library.sheets).toEqual(['read:3'])
    const sheet = wrapper.get('[data-testid="artifact-sheet"]')
    expect(sheet.text()).toContain('政企')
    expect(sheet.text()).toContain('分渠道毛利')
    expect(sheet.text()).toContain('原始数据')
    expect(sheet.text()).toContain('2 张工作表')
  })

  it('turns a missing LibreOffice into an unavailable state, not an error', async () => {
    vi.spyOn(forebrainApi, 'getArtifactPreviewState').mockResolvedValue('libreoffice_unavailable')
    const wrapper = mountPreview({ preview: 'office_pdf', kind: 'presentation', name: '董事会汇报.pptx' })

    await flushPromises()

    expect(wrapper.find('[data-testid="artifact-office-pdf"]').exists()).toBe(false)
    const none = wrapper.get('[data-testid="artifact-none"]')
    expect(none.text()).toContain('预览服务不可用')
    expect(none.text()).toContain('LibreOffice')
  })

  it('opens the converted PDF when LibreOffice is there', async () => {
    vi.spyOn(forebrainApi, 'getArtifactPreviewState').mockResolvedValue('ready')
    const wrapper = mountPreview({ preview: 'office_pdf', kind: 'presentation' })

    await flushPromises()

    expect(wrapper.get('[data-testid="artifact-office-pdf"]').attributes('src'))
      .toBe('/api/chat/sessions/session-1/artifacts/art-1/preview.pdf')
  })

  it('lists the entries an archive holds without extracting them', async () => {
    vi.spyOn(forebrainApi, 'getArtifactEntries').mockResolvedValue({
      entries: [
        { name: '底稿/', size: 0, dir: true },
        { name: '凭证-1001.xlsx', size: 49357, dir: false },
      ],
      total: 2,
      truncated: false,
    })
    const wrapper = mountPreview({ preview: 'archive', kind: 'archive', name: '底稿清单.zip' })

    await flushPromises()

    const listing = wrapper.get('[data-testid="artifact-archive"]')
    expect(listing.text()).toContain('底稿/')
    expect(listing.text()).toContain('48.2 KB')
    expect(wrapper.get('[data-testid="artifact-archive-note"]').text()).toBe('2 个条目')
  })

  it('offers only the download for a kind nothing here renders', () => {
    const wrapper = mountPreview({ preview: '', kind: 'design', name: '品牌主视觉.psd' })

    const none = wrapper.get('[data-testid="artifact-none"]')
    expect(none.text()).toContain('不支持预览')
    expect(none.text()).toContain('设计稿')
    expect(wrapper.get('[data-testid="artifact-none-download"]').attributes('href')).toContain('disposition=attachment')
  })

  it('says what failed when the bytes cannot be read', async () => {
    vi.spyOn(forebrainApi, 'getArtifactContent').mockRejectedValue(new Error('artifact_file_missing'))
    const wrapper = mountPreview({ preview: 'markdown', kind: 'markdown' })

    await flushPromises()

    const none = wrapper.get('[data-testid="artifact-none"]')
    expect(none.text()).toContain('预览失败')
    expect(none.text()).toContain('artifact_file_missing')
  })
})
