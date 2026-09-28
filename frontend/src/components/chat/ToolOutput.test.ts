import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ToolOutput } from '@repo/elements/tool'

/**
 * The Markdown renderer types its content out rather than painting it at once,
 * so a freshly mounted card is still empty. Every assertion about what a reader
 * sees has to wait for the text to arrive.
 */
async function settled(wrapper: { text: () => string }, contains: string) {
  await vi.waitFor(() => expect(wrapper.text()).toContain(contains), { timeout: 2000 })
}

/**
 * A tool's string output is the runtime's own display body: Markdown, produced
 * by the same formatter that draws the terminal. Rendering it as a JSON code
 * block showed the reader the source instead of the result — a memory search
 * hit came out as literal `**审批**`, quoted evidence kept its bare `>`, and a
 * shell result arrived wrapped in unrendered fences.
 */
describe('ToolOutput', () => {
  it('renders a display body as Markdown, not as JSON source', async () => {
    const wrapper = mount(ToolOutput, {
      props: {
        output: '`MEMORY.md` · line 5\n\n> - **审批**浮层必须完整展示',
        errorText: undefined,
      },
    })

    await settled(wrapper, '浮层必须完整展示')
    const html = wrapper.html()
    // The mark on the term that recalled the memory is emphasis, not asterisks.
    expect(html).toContain('<strong')
    expect(wrapper.text()).not.toContain('**')
    expect(wrapper.find('strong').text()).toBe('审批')
    // The quoted evidence is a quote, and the path is a code span.
    expect(wrapper.find('blockquote').exists()).toBe(true)
    expect(wrapper.find('code').text()).toBe('MEMORY.md')
    expect(wrapper.text()).toContain('浮层必须完整展示')
    // None of the Markdown source survives into what the reader sees.
    expect(wrapper.text()).not.toContain('`')
    expect(wrapper.text()).not.toContain('> -')
  })

  it('renders a fenced shell result as a code block', async () => {
    const wrapper = mount(ToolOutput, {
      props: { output: 'stdout:\n\n```text\na.go\nb.go\n```', errorText: undefined },
    })

    await settled(wrapper, 'a.go')
    expect(wrapper.find('[data-stream-markdown="code-block"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('a.go')
    expect(wrapper.text()).not.toContain('```')
  })

  it('keeps structured output as JSON, which is what it is', async () => {
    const wrapper = mount(ToolOutput, {
      props: { output: { exitCode: 0, matches: 3 }, errorText: undefined },
    })

    await settled(wrapper, 'exitCode')
    const text = wrapper.text()
    expect(text).toContain('exitCode')
    expect(text).toContain('matches')
  })

  it('shows nothing when there is neither output nor error', () => {
    const wrapper = mount(ToolOutput, { props: { output: '', errorText: undefined } })
    expect(wrapper.text().trim()).toBe('')
  })
})
