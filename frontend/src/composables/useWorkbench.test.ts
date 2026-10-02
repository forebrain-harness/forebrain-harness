import { describe, expect, it } from 'vitest'

import { useWorkbench } from './useWorkbench'

describe('useWorkbench', () => {
  it('starts collapsed and toggles back and forth', () => {
    const wb = useWorkbench()
    expect(wb.open.value).toBe(false)
    wb.toggle()
    expect(wb.open.value).toBe(true)
    wb.toggle()
    expect(wb.open.value).toBe(false)
  })

  it('close collapses an open workbench', () => {
    const wb = useWorkbench()
    wb.toggle()
    wb.close()
    expect(wb.open.value).toBe(false)
  })
})
