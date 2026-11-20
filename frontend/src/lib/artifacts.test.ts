import { describe, expect, it } from 'vitest'

import {
  artifactOrigin,
  artifactView,
  artifactsOfPresentFiles,
  displayPathOf,
  planArtifactOf,
  runFrameArtifacts,
  turnDeliverables,
  turnDiffTotals,
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

  it('drops out-of-project rows: a kept plan outside the project is not a deliverable', () => {
    // The one row that lives outside the project is a kept plan (plans are
    // stored under the workspace root); owner 2026-11-19: being inside the
    // project is a necessary condition for a deliverable.
    const outside = planArtifactOf({
      planOnly: true,
      planFile: '/Users/me/.forebrain/workspace/plans/年度审计计划.md',
      artifact: { id: 'p1', name: '年度审计计划.md', kind: 'markdown', preview: 'markdown', size: 9216 },
    }, 'session-1')
    const inProject = runFrameArtifacts({
      artifacts: [{ id: 'a1', sessionId: 'session-1', path: '/Users/me/projects/audit/out/report.md', name: 'report.md', kind: 'text', preview: 'text', size: 10 }],
    })

    const group = turnDeliverables(
      { presented: [], plan: outside ? [outside] : [], frame: inProject, fallback: [] },
      '/Users/me/projects/audit',
    )
    expect(group.map((row) => row.id)).toEqual(['a1'])

    // A root the surface does not know must not silently drop rows.
    const noRoot = turnDeliverables({ presented: [], plan: outside ? [outside] : [], frame: [], fallback: [] }, '')
    expect(noRoot.map((row) => row.id)).toEqual(['p1'])
    const blankRoot = turnDeliverables({ presented: [], plan: outside ? [outside] : [], frame: [], fallback: [] }, '   ')
    expect(blankRoot.map((row) => row.id)).toEqual(['p1'])
    // An undefined root keeps the rows too (the parameter is optional).
    const undefinedRoot = turnDeliverables({ presented: [], plan: outside ? [outside] : [], frame: [], fallback: [] })
    expect(undefinedRoot.map((row) => row.id)).toEqual(['p1'])
  })

  it('merges every source by path, each file once', () => {
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

  it('draws one row per file: a path presented twice holds its later version', () => {
    // `present_files` records a new version row on every presentation
    // (pkg/tool/artifacts.go), so a turn that presented plans/README.md twice
    // carries two ids for one file. Keying the group by id drew it twice; the
    // file is one row, in its first place, holding the later version's content.
    const earlier = artifactsOfPresentFiles({
      presented: [{ id: 'art-1', name: 'README.md', relPath: 'plans/README.md', title: 'plans/README.md', kind: 'markdown', size: 869854 }],
    }, 'session-1')
    const other = artifactsOfPresentFiles({
      presented: [{ id: 'art-3', name: 'logo.png', relPath: 'assets/logo.png', kind: 'image', size: 521 }],
    }, 'session-1')
    const later = artifactsOfPresentFiles({
      presented: [{ id: 'art-2', name: 'README.md', relPath: 'plans/README.md', title: 'plans/README.md', kind: 'markdown', size: 871486 }],
    }, 'session-1')

    const group = turnDeliverables({ presented: [...earlier, ...other, ...later], plan: [], frame: [], fallback: [] }, '/Users/me/projects/audit')

    expect(group.map((row) => row.id)).toEqual(['art-2', 'art-3'])
    expect(group[0].size).toBe(871486)
  })

  it('merges one path across every source, the latest version winning the row', () => {
    const presented = artifactsOfPresentFiles({
      presented: [{ id: 'art-1', name: 'a.md', relPath: 'out/a.md', kind: 'markdown', size: 10 }],
    }, 'session-1')
    const frame = runFrameArtifacts({
      artifacts: [{ id: 'art-2', sessionId: 'session-1', path: '/Users/me/projects/audit/out/a.md', relPath: 'out/a.md', name: 'a.md', kind: 'markdown', size: 20 }],
    })
    const fallback = [artifactView('session-1', { id: 'art-3', name: 'a.md', relPath: 'out/a.md', kind: 'markdown', size: 30 })!]

    const group = turnDeliverables({ presented, plan: [], frame, fallback }, '/Users/me/projects/audit')

    expect(group.map((row) => row.id)).toEqual(['art-3'])
    expect(group[0].size).toBe(30)
  })

  it('keeps the same file name in two directories as two files', () => {
    const rows = artifactsOfPresentFiles({
      presented: [
        { id: 'r1', name: 'report.md', relPath: 'reports/report.md', kind: 'markdown', size: 10 },
        { id: 'r2', name: 'report.md', relPath: 'archive/report.md', kind: 'markdown', size: 20 },
      ],
    }, 'session-1')

    const group = turnDeliverables({ presented: rows, plan: [], frame: [], fallback: [] }, '/Users/me/projects/audit')

    expect(group.map((row) => row.relPath)).toEqual(['reports/report.md', 'archive/report.md'])
  })

  it('merges a presented row with its frame row when the root is unknown', () => {
    // A session whose project root is unknown is reachable (a conversation
    // with no project): displayPathOf cannot relativize an absolute path
    // without a root, so the frame row labels itself by its absolute path
    // while the presented row keeps its relPath. Keying by relPath first keeps
    // one row where keying by the drawn label would split it in two.
    const presented = artifactsOfPresentFiles({
      presented: [{ id: 'a1', name: 'report.md', relPath: 'out/report.md', kind: 'markdown', size: 10 }],
    }, 'session-1')
    const frame = runFrameArtifacts({
      artifacts: [{ id: 'a1', sessionId: 'session-1', path: '/Users/me/projects/audit/out/report.md', relPath: 'out/report.md', name: 'report.md', kind: 'markdown', size: 10 }],
    })

    const group = turnDeliverables({ presented, plan: [], frame, fallback: [] })

    expect(group.map((row) => row.id)).toEqual(['a1'])
  })

  it('merges two versions of one relPath when the root is unknown', () => {
    const presented = artifactsOfPresentFiles({
      presented: [
        { id: 'a1', name: 'report.md', relPath: 'out/report.md', kind: 'markdown', size: 10 },
        { id: 'a2', name: 'report.md', relPath: 'out/report.md', kind: 'markdown', size: 20 },
      ],
    }, 'session-1')

    const group = turnDeliverables({ presented, plan: [], frame: [], fallback: [] })

    expect(group.map((row) => row.id)).toEqual(['a2'])
  })

  it('keeps the presented rows of a reloaded turn, whose frame is gone', () => {
    // A reload rebuilds the card from the same structured rows and has no end
    // frame left: the group is still the presented files, once.
    const fallback = [artifactView('session-1', { id: 'a2', runId: 'run-1', name: '毛利.xlsx', kind: 'spreadsheet', preview: 'sheet', size: 80896 })!]

    const group = turnDeliverables({ presented, plan: [], frame: [], fallback })

    expect(group.map((row) => row.id)).toEqual(['a1', 'a2'])
  })

  it('puts the kept plan first, once, even when the frame lists it too', () => {
    // The production frame's plan row carries the plan's path (the artifact
    // was registered with Path, pkg/tool/session_tools.go), which is the same
    // absolute-or-envelope path planArtifactOf hands the plan row: the two
    // rows name one file and merge. Without the path the frame row would be
    // keyed by its bare name and split into a second row.
    const frame = runFrameArtifacts({
      artifacts: [{ id: 'p1', sessionId: 'session-1', runId: 'run-1', path: 'plans/年度审计计划.md', name: '年度审计计划.md', kind: 'markdown', preview: 'markdown', size: 9216, origin: 'plan' }],
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
/**
 * The row's label is a path the reader can locate (owner 2026-11-19): inside
 * the session's project root it is relative to that root, outside it it stays
 * absolute, and the root boundary is a separator match — `/work/a` is not
 * inside `/work/ab`. A structured row without an absolute path keeps its
 * relPath, and a legacy row with neither falls back to its name.
 */
describe('displayPathOf', () => {
  it('relativizes an absolute path inside the project root', () => {
    expect(displayPathOf({ path: '/work/proj/deliverables/季度报告.md' }, '/work/proj')).toBe('deliverables/季度报告.md')
    // The boundary is a separator: a sibling directory that shares a prefix
    // is outside, and its file stays absolute.
    expect(displayPathOf({ path: '/work/proj2/交付说明.docx' }, '/work/proj')).toBe('/work/proj2/交付说明.docx')
  })

  it('keeps an outside-root path absolute, and never relativizes without a root', () => {
    expect(displayPathOf({ path: '/var/tmp/渠道分布.png' }, '/work/proj')).toBe('/var/tmp/渠道分布.png')
    expect(displayPathOf({ path: '/work/proj/报告.md' }, '')).toBe('/work/proj/报告.md')
    expect(displayPathOf({ path: '/work/proj/报告.md' })).toBe('/work/proj/报告.md')
  })

  it('reads a structured row without an absolute path from its relPath, then its name', () => {
    expect(displayPathOf({ relPath: 'deliverables/notes.md' }, '/work/proj')).toBe('deliverables/notes.md')
    expect(displayPathOf({ name: 'legacy.docx' }, '/work/proj')).toBe('legacy.docx')
  })

  it('speaks forward slashes whatever the platform wrote', () => {
    expect(displayPathOf({ path: '/work/proj\\子目录\\报告.md' }, '/work/proj')).toBe('子目录/报告.md')
    expect(displayPathOf({ relPath: 'deliverables\\notes.md' }, '/work/proj')).toBe('deliverables/notes.md')
  })

  it('hands a kept plan outside the root its absolute path from the envelope', () => {
    // The envelope's plan_file is the one path fact a kept-plan artifact row
    // carries (its artifact subset names no path), so it reaches the row.
    const kept = planArtifactOf({
      planOnly: true,
      planFile: '/Users/who/.forebrain/plans/s1/plan.md',
      artifact: { id: 'p9', name: 'plan.md', kind: 'markdown', preview: 'markdown', size: 138 },
    }, 'session-1')
    expect(displayPathOf(kept ?? {}, '/work/proj')).toBe('/Users/who/.forebrain/plans/s1/plan.md')
  })

  // Windows is a shipped target (forebrain-win32-x64): a delivered row can
  // carry a drive-letter or UNC path, and the same inside/outside rule holds.
  it('relativizes a Windows drive-letter path inside the project root', () => {
    expect(displayPathOf({ path: 'C:/work/proj/docs/a.md' }, 'C:/work/proj')).toBe('docs/a.md')
    // Backslashes arrive from the platform's own separators; output stays `/`.
    expect(displayPathOf({ path: 'C:\\work\\proj\\docs\\a.md' }, 'C:\\work\\proj')).toBe('docs/a.md')
  })

  it('keeps a Windows path outside the root absolute', () => {
    expect(displayPathOf({ path: 'C:/other/a.md' }, 'C:/work/proj')).toBe('C:/other/a.md')
    // A different drive is a different volume, however alike the directories.
    expect(displayPathOf({ path: 'D:/work/proj/a.md' }, 'C:/work/proj')).toBe('D:/work/proj/a.md')
  })

  it('relativizes across drive-letter case, on segment boundaries only', () => {
    // Windows compares paths case-insensitively, the drive included.
    expect(displayPathOf({ path: 'c:/WORK/PROJ/docs/a.md' }, 'C:/work/proj')).toBe('docs/a.md')
    // The boundary is still a whole separator: a sibling directory that
    // shares the name prefix is outside and stays absolute.
    expect(displayPathOf({ path: 'C:/work/proj2/a.md' }, 'C:/work/proj')).toBe('C:/work/proj2/a.md')
  })

  it('relativizes a UNC path inside its share root', () => {
    expect(displayPathOf({ path: '//server/share/docs/a.md' }, '//server/share')).toBe('docs/a.md')
    // Another share is outside; host and share names compare without case.
    expect(displayPathOf({ path: '//server/share/docs/a.md' }, '//other/share')).toBe('//server/share/docs/a.md')
    expect(displayPathOf({ path: '//Server/Share/docs/a.md' }, '//server/share')).toBe('docs/a.md')
  })

  it('keeps POSIX matching case-sensitive, unlike Windows paths', () => {
    expect(displayPathOf({ path: '/Work/Proj/docs/a.md' }, '/work/proj')).toBe('/Work/Proj/docs/a.md')
  })
})

/**
 * The counts a delivered row shows come from the turn's own diffs
 * (`turn_diff_updated`): one aggregation per turn, keyed by the same display
 * path the rows are labeled by. The wire's counts are `omitempty` — a created
 * file carries `added` alone, a deleted one `deleted` alone — so a missing
 * side on a counted diff is zero, while a binary, mode-only or malformed row
 * shows nothing at all rather than a guessed pair.
 */
describe('turnDiffTotals', () => {
  it('aggregates the same path across entries and keys them by display path', () => {
    const totals = turnDiffTotals([
      { path: '/work/proj/deliverables/report.md', added: 2, deleted: 1 },
      { path: 'deliverables/report.md', added: 3, deleted: 2 },
      { path: '/work/proj/deliverables/other.md', added: 4, deleted: 4 },
    ], '/work/proj')
    expect(totals.get('deliverables/report.md')).toEqual({ added: 5, deleted: 3 })
    expect(totals.has('deliverables/other.md')).toBe(true)
  })

  it('reads the omitempty protocol: a missing side is zero on a counted diff', () => {
    const totals = turnDiffTotals([
      { path: 'deliverables/created.md', status: 'added', added: 2 },
      { path: 'deliverables/gone.md', status: 'deleted', deleted: 3 },
    ])
    expect(totals.get('deliverables/created.md')).toEqual({ added: 2, deleted: 0 })
    expect(totals.get('deliverables/gone.md')).toEqual({ added: 0, deleted: 3 })
  })

  it('shows nothing for binary, numberless or all-zero rows', () => {
    const totals = turnDiffTotals([
      { path: 'out/app.bin', binary: true, added: 9, deleted: 9 },
      { path: 'docs/mode.md', status: 'changed' },
      { path: 'docs/zero.md', added: 0, deleted: 0 },
    ])
    expect(totals.has('out/app.bin')).toBe(false)
    expect(totals.has('docs/mode.md')).toBe(false)
    expect(totals.has('docs/zero.md')).toBe(false)
  })

  it('relates nothing when there are no diffs at all', () => {
    expect(turnDiffTotals(undefined, '/work/proj').size).toBe(0)
    expect(turnDiffTotals([], '/work/proj').size).toBe(0)
  })
})
