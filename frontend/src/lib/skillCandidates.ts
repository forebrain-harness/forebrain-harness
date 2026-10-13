import type { SkillCandidate } from '@/lib/api'

/**
 * The discovery flow's structured block. The skill-workshop prompt tells the
 * model to emit one ```skill-candidates fenced block holding a JSON array of
 * candidate skills, so the workshop page can draw them as installable cards
 * instead of a wall of prose.
 */
export interface ParsedCandidates {
  /** The reply with the candidates block removed. */
  text: string
  candidates: SkillCandidate[]
}

const BLOCK = /```skill-candidates\s*\n([\s\S]*?)```/i

export function parseSkillCandidates(reply: string): ParsedCandidates {
  const match = BLOCK.exec(reply ?? '')
  if (!match) return { text: reply ?? '', candidates: [] }
  let rows: unknown = []
  try {
    rows = JSON.parse(match[1])
  } catch {
    rows = []
  }
  const candidates: SkillCandidate[] = Array.isArray(rows)
    ? rows
        .map((row) => {
          const item = row as Record<string, unknown>
          const name = String(item.name ?? '').trim()
          if (!name) return null
          return {
            name,
            description: String(item.description ?? '').trim(),
            source: String(item.source ?? '').trim(),
            installed: item.installed === true,
          }
        })
        .filter((row): row is SkillCandidate => row !== null)
    : []
  const text = `${reply.slice(0, match.index)}${reply.slice(match.index + match[0].length)}`.trim()
  return { text, candidates }
}
