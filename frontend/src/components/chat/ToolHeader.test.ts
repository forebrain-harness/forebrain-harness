import { h } from 'vue'
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { Collapsible } from '@repo/shadcn-vue/components/ui/collapsible'

import ToolHeader from '@repo/elements/tool/ToolHeader.vue'

/**
 * The one-line card head (owner 2026-10-25): the engine's three header parts
 * — action grey, object dark in mono, suffix grey — the duration on the right,
 * a non-zero exit left of it, and the failure state drawn as a cross.
 */
describe('ToolHeader', () => {
  const parts = { action: 'Read', target: 'a.go', suffix: 'lines 1-9', duration: '0.3s' }
  type HeadProps = InstanceType<typeof ToolHeader>['$props']

  function head(props: HeadProps) {
    // A trigger lives inside its collapsible; mounting the head alone in one
    // keeps the fixture to exactly what is being asserted.
    return mount(Collapsible, { slots: { default: () => h(ToolHeader, props) } })
  }

  it('words the action and the suffix in Chinese, keeps the object as it came', () => {
    const wrapper = head({ parts, lang: 'zh', status: 'done', statusLabel: 'done' })
    const ttl = wrapper.get('.ttl')
    expect(ttl.find('.verb').text()).toBe('已读取')
    expect(ttl.find('.tgt').text()).toBe('a.go')
    expect(ttl.find('.sfx').text()).toBe('第 1-9 行')
    expect(wrapper.get('.dur').text()).toBe('0.3s')
  })

  it('keeps the engine’s own words in English', () => {
    const wrapper = head({ parts, lang: 'en', status: 'done', statusLabel: 'done' })
    expect(wrapper.get('.ttl .verb').text()).toBe('Read')
    expect(wrapper.get('.ttl .sfx').text()).toBe('lines 1-9')
  })

  it('draws a head with no object as its action alone, in the title colour', () => {
    const wrapper = head({ parts: { action: 'Todo' }, lang: 'zh', status: 'done', statusLabel: 'done' })
    expect(wrapper.find('.ttl .solo').exists()).toBe(true)
    expect(wrapper.get('.ttl .solo').text()).toBe('待办')
    expect(wrapper.find('.ttl .verb').exists()).toBe(false)
    expect(wrapper.find('.ttl .tgt').exists()).toBe(false)
  })

  it('falls back to the joined title when no parts came', () => {
    const wrapper = head({ title: 'Read a.go lines 1-9', status: 'done', statusLabel: 'done' })
    expect(wrapper.get('.ttl .solo').text()).toBe('Read a.go lines 1-9')
  })

  it('shows a non-zero exit as a pill left of the duration, worded per language', () => {
    const zh = head({ parts, lang: 'zh', exitCode: 2, status: 'error', statusLabel: 'failed' })
    expect(zh.get('.exit').text()).toBe('退出码 2')
    const en = head({ parts, lang: 'en', exitCode: 2, status: 'error', statusLabel: 'failed' })
    expect(en.get('.exit').text()).toBe('Exit 2')
    // A zero exit is not news: no pill.
    const ok = head({ parts, lang: 'en', exitCode: 0, status: 'done', statusLabel: 'done' })
    expect(ok.find('.exit').exists()).toBe(false)
  })

  it('draws a failure as a cross, and hands the state to the node when asked', () => {
    const failed = head({ title: 'Ran ls', status: 'error', statusLabel: 'failed' })
    expect(failed.get('.tool-status').find('svg').attributes('class')).toContain('lucide-x')
    // On the activity line the node carries the state; the head carries none.
    const onNode = head({ title: 'Ran ls', status: 'error', statusLabel: 'failed', statusOnNode: true })
    expect(onNode.find('.tool-status').exists()).toBe(false)
  })
})
