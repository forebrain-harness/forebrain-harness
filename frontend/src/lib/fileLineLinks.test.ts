import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, provide } from 'vue'
import { mount } from '@vue/test-utils'
import { MessageResponse } from '@repo/elements/message'
import {
  captureHostParam,
  fileLineRefOf,
  isNativeWebviewHost,
  isVscodeHost,
  openFileLineKey,
  postFileRangeToHost,
  sendFileRangeToHost,
  setNativeFileRangeSender,
  splitFileLineLinks,
  type FileLineRef,
} from './fileLineLinks'

/** The references a value carries, in order. */
function refsOf(value: string): FileLineRef[] {
  return splitFileLineLinks(value).flatMap((segment) => (segment.ref ? [segment.ref] : []))
}

describe('splitFileLineLinks', () => {
  it('links the canonical path:from-to and a single line', () => {
    expect(refsOf('见 pkg/config/llm_models.go:338-343 的实现')).toEqual([
      { path: 'pkg/config/llm_models.go', from: 338, to: 343 },
    ])
    expect(refsOf('main.go:12')).toEqual([{ path: 'main.go', from: 12, to: 12 }])
  })

  it('accepts hyphen, en dash and em dash ranges', () => {
    expect(refsOf('a.go:3-7')).toEqual([{ path: 'a.go', from: 3, to: 7 }])
    expect(refsOf('a.go:3–7')).toEqual([{ path: 'a.go', from: 3, to: 7 }])
    expect(refsOf('a.go:3—7')).toEqual([{ path: 'a.go', from: 3, to: 7 }])
  })

  it('keeps the surrounding text and every reference in order', () => {
    const segments = splitFileLineLinks('先看 a.go:1-2，再看 b/c.ts:9。')
    expect(segments.map((s) => s.text)).toEqual(['先看 ', 'a.go:1-2', '，再看 ', 'b/c.ts:9', '。'])
    expect(segments[1].ref).toEqual({ path: 'a.go', from: 1, to: 2 })
    expect(segments[3].ref).toEqual({ path: 'b/c.ts', from: 9, to: 9 })
  })

  it('supports relative paths without widening the boundary', () => {
    expect(refsOf('./scripts/check.sh:5')).toEqual([{ path: './scripts/check.sh', from: 5, to: 5 }])
    expect(refsOf('scripts/check.sh:5')).toEqual([{ path: 'scripts/check.sh', from: 5, to: 5 }])
    expect(refsOf('../a.go:1')).toEqual([])
    expect(refsOf('pkg/../a.go:2')).toEqual([])
  })

  it('refuses URLs, host:port, Windows drives and ~ homes', () => {
    expect(refsOf('https://example.com/a.go:12')).toEqual([])
    expect(refsOf('http://x.com/a.md:1-2')).toEqual([])
    expect(refsOf('localhost:8080')).toEqual([])
    expect(refsOf('example.com:443')).toEqual([])
    expect(refsOf('C:\\repo\\a.go:12')).toEqual([])
    expect(refsOf('C:/repo/a.go:12')).toEqual([])
    expect(refsOf('~/a.go:12')).toEqual([])
    expect(refsOf('~user/a.go:1')).toEqual([])
  })

  it('refuses IPs, ratios and clock times', () => {
    expect(refsOf('192.168.1.1:8080')).toEqual([])
    expect(refsOf('3:2')).toEqual([])
    expect(refsOf('1.5:2')).toEqual([])
    expect(refsOf('10:30')).toEqual([])
    expect(refsOf('2026-11-27T10:30:00')).toEqual([])
  })

  it('refuses CLF log timestamps and docker references (basename guard)', () => {
    expect(refsOf('127.0.0.1 - - [26/Nov/2026:10:13:22 +0800]')).toEqual([])
    expect(refsOf('26/Nov/2026:10')).toEqual([])
    expect(refsOf('mcr.microsoft.com/dotnet/sdk:8.0')).toEqual([])
    expect(refsOf('image: mcr.microsoft.com/dotnet/sdk:8.0-jammy')).toEqual([])
  })

  it('links whitelisted extensions case-insensitively and whitelisted names', () => {
    expect(refsOf('README.MD:3')).toEqual([{ path: 'README.MD', from: 3, to: 3 }])
    expect(refsOf('a.vue:7')).toEqual([{ path: 'a.vue', from: 7, to: 7 }])
    expect(refsOf('x.jsonc:1')).toEqual([{ path: 'x.jsonc', from: 1, to: 1 }])
    expect(refsOf('Makefile:10')).toEqual([{ path: 'Makefile', from: 10, to: 10 }])
    expect(refsOf('Dockerfile:5')).toEqual([{ path: 'Dockerfile', from: 5, to: 5 }])
    expect(refsOf('Dockerfile.ubuntu-bubblewrap:9')).toEqual([
      { path: 'Dockerfile.ubuntu-bubblewrap', from: 9, to: 9 },
    ])
    expect(refsOf('.gitignore:2')).toEqual([{ path: '.gitignore', from: 2, to: 2 }])
    expect(refsOf('.env.local:1')).toEqual([{ path: '.env.local', from: 1, to: 1 }])
    expect(refsOf('go.work:4')).toEqual([{ path: 'go.work', from: 4, to: 4 }])
    // md and html are source text here, like workbuddy's own editor.
    expect(refsOf('notes.md:4')).toEqual([{ path: 'notes.md', from: 4, to: 4 }])
    expect(refsOf('page.html:8')).toEqual([{ path: 'page.html', from: 8, to: 8 }])
  })

  it('refuses basenames outside both whitelists', () => {
    expect(refsOf('scripts/build-x:12')).toEqual([])
    expect(refsOf('archive.zip:3')).toEqual([])
    expect(refsOf('photo.png:1')).toEqual([])
    expect(refsOf('data.bin:2')).toEqual([])
    expect(refsOf('foo.unknownext:1')).toEqual([])
    expect(refsOf('makefile:10')).toEqual([])
  })

  it('refuses unsound line numbers and overlong ranges', () => {
    expect(refsOf('a.go:0')).toEqual([])
    expect(refsOf('a.go:5-3')).toEqual([])
    expect(refsOf('a.go:1-2001')).toEqual([])
    expect(refsOf('a.go:1-2000')).toEqual([{ path: 'a.go', from: 1, to: 2000 }])
  })

  it('leaves trailing punctuation and a half-typed range out of the reference', () => {
    const segments = splitFileLineLinks('a.go:12.')
    expect(segments.map((s) => s.text)).toEqual(['a.go:12', '.'])
    expect(refsOf('a.go:12-')).toEqual([{ path: 'a.go', from: 12, to: 12 }])
  })

  it('covers the whole value even when nothing links', () => {
    expect(splitFileLineLinks('')).toEqual([{ text: '' }])
    expect(splitFileLineLinks('plain text')).toEqual([{ text: 'plain text' }])
  })
})

describe('fileLineRefOf', () => {
  it('defaults to a single line and validates the range', () => {
    expect(fileLineRefOf('a.go', 12)).toEqual({ path: 'a.go', from: 12, to: 12 })
    expect(fileLineRefOf('a.go', 0)).toBeNull()
    expect(fileLineRefOf('a.go', 5, 3)).toBeNull()
    expect(fileLineRefOf('a.go', 1, 2001)).toBeNull()
    expect(fileLineRefOf('a.go', 1.5)).toBeNull()
    expect(fileLineRefOf('', 1)).toBeNull()
  })
})

/**
 * The Markdown renderer types its content out rather than painting it at once,
 * so a freshly mounted response is still empty; assertions wait for the text.
 */
async function settled(wrapper: { text: () => string }, contains: string) {
  await vi.waitFor(() => expect(wrapper.text()).toContain(contains), { timeout: 2000 })
}

function mountWith(content: string, open?: (ref: FileLineRef) => void) {
  const Host = defineComponent({
    setup() {
      if (open) provide(openFileLineKey, open)
      return () => h(MessageResponse, { content })
    },
  })
  return mount(Host)
}

describe('MessageResponse file:line links', () => {
  it('draws a keyboard-reachable anchor and hands the reference to the handler', async () => {
    const open = vi.fn()
    const wrapper = mountWith('见 pkg/config/llm_models.go:338-343 的实现', open)
    await settled(wrapper, '的实现')
    const anchor = wrapper.get('[data-stream-markdown="file-line"]')
    expect(anchor.text()).toBe('pkg/config/llm_models.go:338-343')
    expect(anchor.attributes('role')).toBe('button')
    expect(anchor.attributes('tabindex')).toBe('0')
    expect(anchor.attributes('title')).toBe('pkg/config/llm_models.go:338-343')
    // The text shell keeps its mark around the link.
    expect(anchor.element.parentElement?.getAttribute('data-stream-markdown')).toBe('text')

    await anchor.trigger('click')
    expect(open).toHaveBeenCalledWith({ path: 'pkg/config/llm_models.go', from: 338, to: 343 })
    await anchor.trigger('keydown', { key: 'Enter' })
    await anchor.trigger('keydown', { key: ' ' })
    expect(open).toHaveBeenCalledTimes(3)
    await anchor.trigger('keydown', { key: 'a' })
    expect(open).toHaveBeenCalledTimes(3)
  })

  it('stays plain text where no handler is provided', async () => {
    const wrapper = mountWith('见 pkg/config/llm_models.go:338-343 的实现')
    await settled(wrapper, '的实现')
    expect(wrapper.find('[data-stream-markdown="file-line"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('pkg/config/llm_models.go:338-343')
  })

  it('linkifies inside an inline code span, keeping the code shell', async () => {
    const open = vi.fn()
    const wrapper = mountWith('跑 `main.go:12` 看看', open)
    await settled(wrapper, '看看')
    const code = wrapper.get('code[data-stream-markdown="inline-code"]')
    const anchor = code.find('[data-stream-markdown="file-line"]')
    expect(anchor.exists()).toBe(true)
    await anchor.trigger('click')
    expect(open).toHaveBeenCalledWith({ path: 'main.go', from: 12, to: 12 })
  })

  it('never touches a fenced code block', async () => {
    const open = vi.fn()
    const wrapper = mountWith('```go\nmain.go:12\n```', open)
    await settled(wrapper, 'main.go:12')
    expect(wrapper.find('[data-stream-markdown="file-line"]').exists()).toBe(false)
    expect(open).not.toHaveBeenCalled()
  })

  it('does not nest a file link inside a markdown link label', async () => {
    const open = vi.fn()
    const wrapper = mountWith('[a.go:12](https://example.com)', open)
    await settled(wrapper, 'a.go:12')
    const link = wrapper.get('a[data-stream-markdown="link"]')
    // The built-in sanitizer normalizes the URL (a bare host gains its slash).
    expect(link.attributes('href')).toMatch(/^https:\/\/example\.com\/?$/)
    expect(link.find('[data-stream-markdown="file-line"]').exists()).toBe(false)
    expect(open).not.toHaveBeenCalled()
  })
})

describe('the VS Code host flag', () => {
  function navigate(url: string) {
    window.history.replaceState({}, '', url)
  }

  it('is written on first sight and survives every later navigation', () => {
    sessionStorage.clear()
    navigate('/?boot=abc&host=vscode')
    captureHostParam()
    expect(isVscodeHost()).toBe(true)
    // The panel navigates to a conversation and the query is gone; the flag
    // stays, so a link clicked minutes later still reaches the host.
    navigate('/?session=s1')
    captureHostParam()
    expect(isVscodeHost()).toBe(true)
    navigate('/workshop')
    expect(isVscodeHost()).toBe(true)
  })

  it('is absent in a plain browser tab', () => {
    sessionStorage.clear()
    navigate('/')
    captureHostParam()
    expect(isVscodeHost()).toBe(false)
    navigate('/?host=browser')
    captureHostParam()
    expect(isVscodeHost()).toBe(false)
  })

  it('posts the range to the parent frame with the shape the host validates', () => {
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {})
    postFileRangeToHost({ path: 'pkg/a.go', from: 3, to: 7 })
    // '*' because the parent is vscode-webview:// — an origin the page cannot
    // name; the host checks origin, source and shape on its side.
    expect(post).toHaveBeenCalledWith(
      { type: 'openFileRange', path: 'pkg/a.go', startLine: 3, endLine: 7 },
      '*',
    )
    post.mockRestore()
  })
})

describe('the native webview range transport', () => {
  afterEach(() => setNativeFileRangeSender(null))

  it('is absent in a browser or the old panel iframe, so ranges post to the parent', () => {
    expect(isNativeWebviewHost()).toBe(false)
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {})
    sendFileRangeToHost({ path: 'a.go', from: 1, to: 2 })
    expect(post).toHaveBeenCalledWith({ type: 'openFileRange', path: 'a.go', startLine: 1, endLine: 2 }, '*')
    post.mockRestore()
  })

  it('rides the registered transport once the webview app installs it', () => {
    const send = vi.fn()
    setNativeFileRangeSender(send)
    expect(isNativeWebviewHost()).toBe(true)
    sendFileRangeToHost({ path: 'b.ts', from: 4, to: 9 })
    expect(send).toHaveBeenCalledWith({ path: 'b.ts', from: 4, to: 9 })
  })

  it('falls back to the panel iframe once the transport is removed', () => {
    const send = vi.fn()
    setNativeFileRangeSender(send)
    setNativeFileRangeSender(null)
    const post = vi.spyOn(window.parent, 'postMessage').mockImplementation(() => {})
    sendFileRangeToHost({ path: 'c.md', from: 2, to: 2 })
    expect(send).not.toHaveBeenCalled()
    expect(post).toHaveBeenCalled()
    post.mockRestore()
  })
})