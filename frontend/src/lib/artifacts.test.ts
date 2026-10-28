import { describe, expect, it } from 'vitest'

import {
  artifactOrigin,
  artifactView,
  artifactsOfPresentFiles,
  planArtifactOf,
  runFrameArtifacts,
  turnDeliverables,
} from './artifacts'

/** A `present_files` completion's own rows (pkg/tool/artifacts.go). */
const PRESENTED = {
  presented: [
    { id: 'a1', name: 'Q3 报告.docx', relPath: 'Q3 报告.docx', title: 'Q3 报告', kind: 'document', preview: 'docx', size: 253952 },
    { id: 'a2', name: '毛利.xlsx', relPath: '毛利.xlsx', title: '毛利', kind: 'spreadsheet', preview: 'sheet', size: 80896 },
  ],
  note: 'The user now sees a download and preview card for each file.',
}

describe('artifactView', () => {
  it('builds the three URLs a structured row does not carry', () => {
    const view = artifactView('session-1', PRESENTED.presented[0])

    expect(view?.id).toBe('a1')
    expect(view?.kind).toBe('document')
    expect(view?.title).toBe('Q3 报告')
    expect(view?.downloadUrl).toBe('/api/chat/sessions/session-1/artifacts/a1/content?disposition=attachment')
    expect(view?.contentUrl).toBe('/api/chat/sessions/session-1/artifacts/a1/content?disposition=inline')
    expect(view?.previewUrl).toBeUndefined()
  })

  it('takes the URLs and disk facts a gateway row brings with it', () => {
    const view = artifactView('session-1', {
      id: 'a3',
      runId: 'run-1',
      name: '董事会汇报.pptx',
      kind: 'presentation',
      preview: 'office_pdf',
      size: 3416064,
      missing: true,
      changed: false,
      downloadUrl: '/custom/download',
      contentUrl: '/custom/content',
      previewUrl: '/custom/preview.pdf',
    })

    expect(view?.runId).toBe('run-1')
    expect(view?.missing).toBe(true)
    expect(view?.downloadUrl).toBe('/custom/download')
    expect(view?.previewUrl).toBe('/custom/preview.pdf')
  })

  it('keeps an unknown kind and an unknown preview from claiming anything', () => {
    const view = artifactView('session-1', { id: 'a4', name: 'x.bin', kind: 'weird', preview: 'nope', size: 1 })

    expect(view?.kind).toBe('other')
    expect(view?.preview).toBe('')
    expect(view?.title).toBe('x.bin')
  })

  it('drops a row that names no file', () => {
    expect(artifactView('session-1', { id: '' })).toBeNull()
    expect(artifactView('session-1', null)).toBeNull()
  })

  it('keeps the origin a row carries and never invents one', () => {
    // The artifact list's rows say where a file came from (a kept plan is what
    // names a delivery group); a card's own row says nothing.
    const plan = artifactView('session-1', { id: 'p1', name: 'plan.md', origin: 'plan' })

    expect(plan?.origin).toBe('plan')
    expect(artifactView('session-1', PRESENTED.presented[0])?.origin).toBe('')
    expect(artifactOrigin('fallback')).toBe('fallback')
    expect(artifactOrigin(' Plan ')).toBe('plan')
    expect(artifactOrigin('elsewhere')).toBe('')
    expect(artifactOrigin(undefined)).toBe('')
  })
})

describe('the delivery group of one turn', () => {
  const presented = artifactsOfPresentFiles(PRESENTED, 'session-1')
  const plan = planArtifactOf({
    planOnly: true,
    planFile: 'plans/年度审计计划.md',
    artifact: { id: 'p1', name: '年度审计计划.md', kind: 'markdown', preview: 'markdown', size: 9216 },
  }, 'session-1')

  it('reads a kept plan out of the exit envelope, and nothing out of an ordinary exit', () => {
    expect(plan?.id).toBe('p1')
    expect(plan?.preview).toBe('markdown')
    // The envelope names no origin: a file reached through this call is the
    // kept plan, which is how it was registered.
    expect(plan?.origin).toBe('plan')
    expect(planArtifactOf({ planOnly: false }, 'session-1')).toBeNull()
    expect(planArtifactOf({}, 'session-1')).toBeNull()
  })

  it('names the model as a presented file’s origin', () => {
    expect(presented.map((row) => row.origin)).toEqual(['model', 'model'])
  })

  it('puts the presented files in the group, in the order the model gave them', () => {
    const group = turnDeliverables({ presented, plan: [], frame: [], fallback: [] })

    expect(group.map((row) => row.id)).toEqual(['a1', 'a2'])
    expect(group[0].name).toBe('Q3 报告.docx')
  })

  it('merges every source by id, each file once', () => {
    // The same two files also arrive on the run's end frame and in the
    // conversation's list, plus one only the frame knew about.
    const frame = runFrameArtifacts({
      artifacts: [
        { id: 'a2', sessionId: 'session-1', name: '毛利.xlsx', kind: 'spreadsheet', preview: 'sheet', size: 80896 },
        { id: 'a9', sessionId: 'session-1', name: '执行摘要.txt', kind: 'text', preview: 'text', size: 2048 },
      ],
    })
    const fallback = [
      artifactView('session-1', { id: 'a1', name: 'Q3 报告.docx', kind: 'document', preview: 'docx', size: 253952 })!,
      artifactView('session-1', { id: 'a9', name: '执行摘要.txt', kind: 'text', preview: 'text', size: 2048 })!,
    ]

    const group = turnDeliverables({ presented, plan: [], frame, fallback })

    expect(group.map((row) => row.id)).toEqual(['a1', 'a2', 'a9'])
  })

  it('keeps the presented rows of a reloaded turn, whose frame is gone', () => {
    // A reload rebuilds the card from the same structured rows and has no end
    // frame left: the group is still the presented files, once.
    const fallback = [artifactView('session-1', { id: 'a2', runId: 'run-1', name: '毛利.xlsx', kind: 'spreadsheet', preview: 'sheet', size: 80896 })!]

    const group = turnDeliverables({ presented, plan: [], frame: [], fallback })

    expect(group.map((row) => row.id)).toEqual(['a1', 'a2'])
  })

  it('puts the kept plan first, once, even when the frame lists it too', () => {
    const frame = runFrameArtifacts({
      artifacts: [{ id: 'p1', sessionId: 'session-1', runId: 'run-1', name: '年度审计计划.md', kind: 'markdown', preview: 'markdown', size: 9216, origin: 'plan' }],
    })

    const group = turnDeliverables({ presented: [], plan: plan ? [plan] : [], frame, fallback: [] })

    expect(group.map((row) => row.id)).toEqual(['p1'])
    // The group keeps the plan's own origin, so a reloaded turn (whose plan
    // arrives on the frame or the artifact list only) names the group the same.
    expect(group[0].origin).toBe('plan')
  })

  it('reads an end frame’s origin off each row, and a frame older than the field as presented', () => {
    const frame = runFrameArtifacts({
      artifacts: [
        { id: 'a1', sessionId: 'session-1', name: 'Q3 报告.docx', kind: 'document', preview: 'docx', size: 253952, origin: 'model' },
        { id: 'x1', sessionId: 'session-1', name: '输出目录', kind: 'other', preview: '', size: 0, origin: 'fallback' },
        { id: 'a9', sessionId: 'session-1', name: '执行摘要.txt', kind: 'text', preview: 'text', size: 2048 },
      ],
    })

    expect(frame.map((row) => row.origin)).toEqual(['model', 'fallback', 'model'])
  })

  it('opens a kept plan and its other files with the plan first', () => {
    const group = turnDeliverables({ presented, plan: plan ? [plan] : [], frame: [], fallback: [] })

    expect(group.map((row) => row.id)).toEqual(['p1', 'a1', 'a2'])
  })
})