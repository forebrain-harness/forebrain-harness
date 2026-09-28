import { describe, expect, it, vi } from 'vitest'

import { mergeSubmissions, uploadSubmission, type ComposerSubmission } from './composerSubmission'

const returned: ComposerSubmission = {
  text: 'look at the diagram',
  attachments: [{ fileId: 'file-1', filename: 'spec.pdf', mediaType: 'application/pdf' }],
  mentionImages: ['shots/a.png'],
}

describe('mergeSubmissions', () => {
  it('puts the message given back ahead of what was typed since, dropping nothing', () => {
    const merged = mergeSubmissions(returned, {
      text: 'and this too',
      attachments: [{ fileId: 'file-2', filename: 'b.png', mediaType: 'image/png' }],
      mentionImages: ['shots/b.png'],
    })
    expect(merged).toEqual({
      text: 'look at the diagram\nand this too',
      attachments: [returned.attachments[0], { fileId: 'file-2', filename: 'b.png', mediaType: 'image/png' }],
      mentionImages: ['shots/a.png', 'shots/b.png'],
    })
  })

  it('is the message itself when the composer is empty', () => {
    expect(mergeSubmissions(returned, { text: '  ', attachments: [], mentionImages: [] })).toEqual(returned)
  })

  it('keeps one of anything attached on both sides', () => {
    const merged = mergeSubmissions(returned, { text: '', attachments: returned.attachments, mentionImages: ['shots/a.png'] })
    expect(merged.attachments).toHaveLength(1)
    expect(merged.mentionImages).toEqual(['shots/a.png'])
  })
})

describe('uploadSubmission', () => {
  const file = (name: string) => new File(['x'], name, { type: 'image/png' })

  it('builds the message from what the composer attached and every upload', async () => {
    const upload = vi.fn(async (f: File) => ({ fileId: `id-${f.name}` }))
    const { submission, unsent, failure } = await uploadSubmission('hello', returned, [
      { file: file('a.png'), filename: 'a.png', mediaType: 'image/png' },
    ], upload)
    expect(failure).toBeNull()
    expect(unsent).toEqual([])
    expect(submission).toEqual({
      text: 'hello',
      attachments: [returned.attachments[0], { fileId: 'id-a.png', filename: 'a.png', mediaType: 'image/png' }],
      mentionImages: ['shots/a.png'],
    })
  })

  it('stops at the first failed upload and gives that file and the rest back unsent', async () => {
    const a = file('a.png')
    const b = file('b.png')
    const c = file('c.png')
    const upload = vi.fn(async (f: File) => {
      if (f === b) throw new Error('file too large')
      return { fileId: `id-${f.name}` }
    })
    const { submission, unsent, failure } = await uploadSubmission('hello', { attachments: [], mentionImages: [] }, [
      { file: a, filename: 'a.png', mediaType: 'image/png' },
      { file: b, filename: 'b.png', mediaType: 'image/png' },
      { file: c, filename: 'c.png', mediaType: 'image/png' },
    ], upload)
    expect(failure).toMatchObject({ filename: 'b.png' })
    expect((failure?.error as Error).message).toBe('file too large')
    expect(unsent).toEqual([b, c])
    // What did upload stays uploaded, and is not uploaded a second time.
    expect(submission.attachments).toEqual([{ fileId: 'id-a.png', filename: 'a.png', mediaType: 'image/png' }])
    expect(upload).toHaveBeenCalledTimes(2)
  })
})
