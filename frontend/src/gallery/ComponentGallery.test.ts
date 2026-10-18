import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import tip from '@/directives/tip'
import ComponentGallery from './ComponentGallery.vue'

/**
 * The component gallery is the dev-only page that mounts the real components,
 * so it must always render: an example left pointing at a component the product
 * deleted is a page that cannot open (plan 076 deleted the title row).
 */
describe('ComponentGallery', () => {
  it('mounts every example section', () => {
    const wrapper = mount(ComponentGallery, {
      global: { directives: { tip }, stubs: { teleport: true } },
    })

    expect(wrapper.find('h1').text()).toBe('组件库')
    expect(wrapper.findAll('section').length).toBeGreaterThan(5)
    // The list pieces the product pages use, rendered with their real values.
    expect(wrapper.find('.forebrain-list-toolbar').exists()).toBe(true)
    expect(wrapper.find('[data-testid="table-pager"]').exists()).toBe(true)
  })
})
