import { describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import TreeNodeActions from './TreeNodeActions.vue'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

describe('TreeNodeActions', () => {
  it('offers a file two actions', () => {
    const wrapper = mount(TreeNodeActions, { props: { isDir: false, name: 'a.md' } })
    expect(wrapper.findAll('button')).toHaveLength(2)
    expect(wrapper.find('[data-testid="tree-node-new"]').exists()).toBe(false)
  })

  it('offers a directory three actions, including a new file', () => {
    const wrapper = mount(TreeNodeActions, { props: { isDir: true, name: 'docs' } })
    expect(wrapper.findAll('button')).toHaveLength(3)
    expect(wrapper.find('[data-testid="tree-node-new"]').exists()).toBe(true)
  })

  it('emits the action the icon names', async () => {
    const wrapper = mount(TreeNodeActions, { props: { isDir: true, name: 'docs' } })
    await wrapper.find('[data-testid="tree-node-new"]').trigger('click')
    await wrapper.find('[data-testid="tree-node-edit"]').trigger('click')
    await wrapper.find('[data-testid="tree-node-delete"]').trigger('click')
    expect(wrapper.emitted('new-file')).toHaveLength(1)
    expect(wrapper.emitted('edit')).toHaveLength(1)
    expect(wrapper.emitted('delete')).toHaveLength(1)
  })
})
