import { describe, expect, it } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount } from '@vue/test-utils'

import { useTopbar, useTopbarContent } from './useTopbar'

/**
 * The shell's one shared mechanism for page-contributed top-bar content: a
 * view fills its row, the row is vacated when the view unmounts, and a view
 * that leaves late never clears the row of the one that already mounted —
 * everything else about the shell stays the page's title it draws today.
 */

/** A component that fills the bar the way a real view does. */
const filling = defineComponent({
  setup() {
    const { set } = useTopbar()
    set({
      leading: () => [h('span', { 'data-testid': 'filling-leading' }, 'leading')],
      trailing: () => [h('span', { 'data-testid': 'filling-trailing' }, 'trailing')],
    })
    return () => h('div')
  },
})

/** A component that fills nothing: it keeps the shell's own title. */
const plain = defineComponent({
  setup() {
    useTopbar()
    return () => h('div')
  },
})

function renderFactoryVNodes(content: ReturnType<typeof useTopbarContent>['value']) {
  return [...(content?.leading?.() ?? []), ...(content?.trailing?.() ?? [])]
}

describe('useTopbar', () => {
  it('holds nothing until a view fills it', () => {
    const view = mount(plain)
    expect(useTopbarContent().value).toBeNull()
    view.unmount()
  })

  it('carries the filling view\'s row and vacates it on unmount', async () => {
    const view = mount(filling)
    const content = useTopbarContent().value
    expect(content).not.toBeNull()
    const nodes = renderFactoryVNodes(content)
    expect(nodes.some((node) => node.props?.['data-testid'] === 'filling-leading')).toBe(true)
    expect(nodes.some((node) => node.props?.['data-testid'] === 'filling-trailing')).toBe(true)

    view.unmount()
    await nextTick()
    expect(useTopbarContent().value).toBeNull()
  })

  it('lets the latest view own the bar: an earlier unmount clears nothing', async () => {
    const first = mount(filling)
    const second = mount(filling)
    first.unmount()
    await nextTick()
    // The second view's row is still the bar's.
    expect(useTopbarContent().value).not.toBeNull()
    second.unmount()
    await nextTick()
    expect(useTopbarContent().value).toBeNull()
  })
})
