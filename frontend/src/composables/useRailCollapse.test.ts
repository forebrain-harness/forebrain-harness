import { describe, expect, it } from 'vitest'

import { useRailCollapse } from './useRailCollapse'

describe('useRailCollapse', () => {
  it('starts expanded and can collapse and expand', () => {
    const rail = useRailCollapse()
    rail.expand()
    expect(rail.collapsed.value).toBe(false)
    rail.toggle()
    expect(rail.collapsed.value).toBe(true)
    rail.expand()
    expect(rail.collapsed.value).toBe(false)
    rail.collapse()
    expect(rail.collapsed.value).toBe(true)
  })
})
