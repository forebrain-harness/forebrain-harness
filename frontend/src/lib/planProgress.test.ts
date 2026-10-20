import { describe, expect, it } from 'vitest'

import { planProgress, planTitle } from './planProgress'

describe('planProgress', () => {
  it('counts the items a checklist marks done against every item', () => {
    const markdown = ['# Plan: add auth', '', '- [x] read the middleware', '- [ ] write the guard', '- [ ] test it'].join('\n')
    expect(planProgress(markdown)).toEqual({ done: 1, total: 3 })
  })

  it('accepts the bullet marks and the capital X a plan is written with', () => {
    const markdown = ['* [X] one', '+ [ ] two', '- [x] three'].join('\n')
    expect(planProgress(markdown)).toEqual({ done: 2, total: 3 })
  })

  it('does not count a box that is not a list item', () => {
    const markdown = ['[ ] not a list', 'text - [x] inline', '- not a box'].join('\n')
    expect(planProgress(markdown)).toEqual({ done: 0, total: 0 })
  })

  it('reports nothing to count for a plan without a checklist', () => {
    expect(planProgress('# Plan\n\nJust prose.')).toEqual({ done: 0, total: 0 })
    expect(planProgress('')).toEqual({ done: 0, total: 0 })
  })
})

describe('planTitle', () => {
  it('draws the first heading a plan carries, without its marks', () => {
    expect(planTitle('# Plan: add auth\n\n- [ ] step')).toBe('Plan: add auth')
    expect(planTitle('intro\n\n## 方案\n')).toBe('方案')
  })

  it('has no title for a plan that carries no heading', () => {
    expect(planTitle('- [ ] just a list')).toBe('')
    expect(planTitle('')).toBe('')
  })
})
