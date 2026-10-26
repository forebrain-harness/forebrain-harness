import { describe, expect, it } from 'vitest'

// The engine's card-facts corpus, straight from its golden copy — the same
// file pkg/tool's TestCardCorpus writes (UPDATE_CARD_CORPUS=1). A static
// import rather than node:fs for the same reason toolHeader.test.ts has one:
// no @types/node here, and the corpus is a compile-time constant anyway.
// Reading that one file on both sides is what makes the two surfaces one
// contract instead of two implementations that happen to agree today.
import corpus from '../../../pkg/tool/testdata/cards.json'

import { toCamelCase } from '@/lib/case'
import { toolStepFromPayload } from '@/composables/useChatStream'

/**
 * One corpus entry shaped the way the web may spell the same facts. The entry
 * arrives in the gateway's snake_case; the page camelizes every payload before
 * toolStepFromPayload sees it (normalizeRunEventPayload), so the test feeds it
 * through the same toCamelCase first — the corpus is replayed exactly as the
 * wire delivers it.
 */
type CorpusEntry = {
  step_id?: string
  tool_name?: string
  summary?: string
  header?: string
  header_parts?: Record<string, unknown>
  display_body?: string
  duration_seconds?: number
  tool_meta?: Record<string, unknown>
  shell_card?: Record<string, unknown>
  file_diff?: Record<string, unknown>
}

/**
 * The shell-card facts the page's ShellCard interface spells: absent and zero
 * are the same for every count (the interface says so — "0 and absent are the
 * same"), so the corpus fact is normalized into the page's spelling before the
 * rebuilt card is compared to it. This is the one place the two shapes differ
 * by design; writing it down here is the point of the corpus.
 */
function expectedShellCard(raw: Record<string, unknown> | undefined) {
  if (!raw) return undefined
  const want = toCamelCase(raw) as Record<string, unknown>
  const exitCode = Number(want.exitCode)
  return {
    stdout: String(want.stdout ?? ''),
    stderr: String(want.stderr ?? ''),
    ...(Number.isFinite(exitCode) && exitCode !== 0 ? { exitCode } : {}),
    stdoutOmittedBytes: Number(want.stdoutOmittedBytes) || 0,
    stderrOmittedBytes: Number(want.stderrOmittedBytes) || 0,
    omittedBytes: Number(want.omittedBytes) || 0,
    retrieveId: Number(want.retrieveId) || 0,
    spoolOmittedBytes: Number(want.spoolOmittedBytes) || 0,
    files: (Array.isArray(want.files) ? want.files : []).map((file) => {
      const f = (file && typeof file === 'object' ? file : {}) as Record<string, unknown>
      return {
        verb: String(f.verb ?? ''),
        path: String(f.path ?? ''),
        added: Number(f.added) || 0,
        deleted: Number(f.deleted) || 0,
        ...(String(f.unifiedDiff ?? '').trim() ? { unifiedDiff: String(f.unifiedDiff) } : {}),
      }
    }),
    notes: Array.isArray(want.notes) ? want.notes.map((note) => String(note)) : [],
  }
}

/** The file-diff facts in the page's spelling: empty strings are absent. */
function expectedFileDiff(raw: Record<string, unknown> | undefined) {
  if (!raw) return undefined
  const want = toCamelCase(raw) as Record<string, unknown>
  const unifiedDiff = String(want.unifiedDiff ?? '').trim()
  const rest = String(want.rest ?? '').trim()
  return {
    path: String(want.path ?? ''),
    added: Number(want.added) || 0,
    deleted: Number(want.deleted) || 0,
    ...(unifiedDiff ? { unifiedDiff: unifiedDiff } : {}),
    ...(rest ? { rest } : {}),
  }
}

describe('the card corpus the engine and the web share', () => {
  for (const [name, entryRaw] of Object.entries(corpus as Record<string, CorpusEntry>)) {
    it(`rebuilds ${name} from the payload the gateway sends`, () => {
      const entry = entryRaw
      // The wire shape: the gateway sends snake_case, the page camelizes the
      // whole payload deep before reading it.
      const payload = toCamelCase(entry) as Record<string, unknown>
      const step = toolStepFromPayload(payload, true, 'corpus-fallback')

      expect(step.stepId, 'step id rides the payload').toBe(entry.step_id)
      expect(step.toolName, 'tool name rides the payload').toBe(entry.tool_name)
      expect(step.summary, 'the runtime label rides the payload').toBe(entry.summary)
      expect(step.header, 'the joined header is the engine\'s wording').toBe(entry.header)
      expect(step.headerParts ?? null, 'the header parts are the engine\'s four').toEqual(
        entry.header_parts ? toCamelCase(entry.header_parts) : null,
      )
      expect(step.status, 'the status word is the engine\'s').toBe(entry.tool_meta?.status ?? 'completed')
      // The refusal's own words must survive the projection (plan 154: a
      // dropped field meant the second line never rendered on the web).
      expect(step.denyFeedback ?? null, 'deny feedback rides tool_meta').toBe(
        (entry.tool_meta?.deny_feedback as string | undefined) ?? null,
      )
      // The page stores the display body trimmed (toolStepFromPayload), so
      // the engine's trailing blank lines do not survive — the wording inside
      // is what must arrive verbatim.
      expect(step.output ?? null, 'the display body rides the payload').toBe(entry.display_body?.trim() ?? null)
      expect(step.durationSeconds ?? null, 'the duration rides the payload').toBe(entry.duration_seconds ?? null)
      expect(step.shellCard ?? null, 'the shell card facts are the engine\'s read-back').toEqual(
        expectedShellCard(entry.shell_card) ?? null,
      )
      expect(step.fileDiff ?? null, 'the turn diff facts are the engine\'s read-back').toEqual(
        expectedFileDiff(entry.file_diff) ?? null,
      )
    })
  }
})
