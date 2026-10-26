import { describe, expect, it } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { mount } from '@vue/test-utils'

import { usePendingActions } from '@/composables/usePendingActions'
import { injectPendingActions, providePendingActions } from '@/composables/providePendingActions'

/**
 * The conversation's one pending-actions store travels by provide/inject
 * (plan 138): the page calls `usePendingActions` once and hands it down, and
 * every card below injects the same instance — one loaded list, one answer
 * state, wherever a gate is decided. A card outside a provider (a harness, a
 * story) injects null and carries no pending request.
 */
describe('providePendingActions', () => {
  it('hands the subtree the one store the page created', () => {
    const seen: unknown[] = []
    const Child = defineComponent({
      setup() {
        seen.push(injectPendingActions())
        return () => h('div')
      },
    })
    const Host = defineComponent({
      setup() {
        const store = providePendingActions(usePendingActions({
          sessionId: ref('s1'),
          version: ref(0),
          contextUsedPercent: ref(null),
        }))
        seen.push(store)
        return () => h(Child)
      },
    })
    mount(Host)
    expect(seen).toHaveLength(2)
    expect(seen[1]).toBe(seen[0])
  })

  it('injects null where no provider stands above', () => {
    const Orphan = defineComponent({
      setup() {
        const got = injectPendingActions()
        return () => h('div', got ? 'store' : 'none')
      },
    })
    const wrapper = mount(Orphan)
    expect(wrapper.text()).toBe('none')
  })
})
