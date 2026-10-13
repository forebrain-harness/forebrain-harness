import { describe, expect, it } from 'vitest'

import { parseSkillCandidates } from './skillCandidates'

describe('parseSkillCandidates', () => {
  it('pulls the candidates block out of the reply', () => {
    const reply = 'Found 2 candidates:\n\n```skill-candidates\n[{"name":"go-style-review","description":"review style","source":"samber/cc-skills-golang","installed":false},{"name":"code-review","description":"generic","source":"skills.sh/x","installed":true}]\n```\n\nPick one.'
    const { text, candidates } = parseSkillCandidates(reply)
    expect(candidates).toHaveLength(2)
    expect(candidates[0]).toEqual({ name: 'go-style-review', description: 'review style', source: 'samber/cc-skills-golang', installed: false })
    expect(candidates[1].installed).toBe(true)
    expect(text).toContain('Found 2 candidates')
    expect(text).not.toContain('skill-candidates')
  })

  it('returns the reply untouched with no block', () => {
    const { text, candidates } = parseSkillCandidates('just a normal reply')
    expect(candidates).toEqual([])
    expect(text).toBe('just a normal reply')
  })

  it('ignores a malformed block', () => {
    const { candidates } = parseSkillCandidates('```skill-candidates\nnot json\n```')
    expect(candidates).toEqual([])
  })
})
