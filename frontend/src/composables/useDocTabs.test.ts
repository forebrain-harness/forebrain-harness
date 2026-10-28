import { describe, expect, it } from 'vitest'

import { docTabFor } from './useDocTabs'
import type { ArtifactView } from '@/lib/artifacts'

function artifactOf(overrides: Partial<ArtifactView> = {}): ArtifactView {
  return {
    sessionId: 'session-1',
    id: 'art-1',
    origin: 'model',
    name: 'Q3 经营分析报告.docx',
    title: 'Q3 经营分析报告.docx',
    kind: 'document',
    preview: 'docx',
    size: 253952,
    missing: false,
    changed: false,
    downloadUrl: '/api/chat/sessions/session-1/artifacts/art-1/content?disposition=attachment',
    contentUrl: '/api/chat/sessions/session-1/artifacts/art-1/content?disposition=inline',
    ...overrides,
  }
}

describe('docTabFor', () => {
  it('identifies a delivered file by what it is, not by its bytes', () => {
    const tab = docTabFor('artifact', 'Q3 经营分析报告.docx', { artifact: artifactOf() })

    expect(tab.kind).toBe('artifact')
    expect(tab.id).toBe('doc:artifact:session-1:art-1')
    // The drawer's tab is titled by the file's own name.
    expect(tab.label).toBe('Q3 经营分析报告.docx')
    expect(tab.artifact?.contentUrl).toContain('/content?disposition=inline')
  })

  it('opens the same file as the same tab, so re-opening only focuses it', () => {
    const first = docTabFor('artifact', 'report.docx', { artifact: artifactOf() })
    // A revised version of the same row: same conversation, same id.
    const again = docTabFor('artifact', 'report.docx', { artifact: artifactOf({ size: 9, changed: true }) })

    expect(again.id).toBe(first.id)
  })

  it('keeps two files of one conversation apart', () => {
    const one = docTabFor('artifact', 'a.docx', { artifact: artifactOf() })
    const two = docTabFor('artifact', 'b.docx', { artifact: artifactOf({ id: 'art-2', name: 'b.docx' }) })

    expect(two.id).not.toBe(one.id)
    expect(docTabFor('artifact', 'a.docx', { artifact: artifactOf({ sessionId: 'session-2' }) }).id)
      .not.toBe(one.id)
  })

  it('still digests the text of a command tab', () => {
    const one = docTabFor('command', 'go test ./pkg/gateway/...', { command: 'go test ./pkg/gateway/...' })
    const two = docTabFor('command', 'go test ./pkg/gateway/...', { command: 'go test ./pkg/gateway/...' })

    expect(one.kind).toBe('command')
    expect(two.id).toBe(one.id)
    expect(one.command).toBe('go test ./pkg/gateway/...')
  })
})
