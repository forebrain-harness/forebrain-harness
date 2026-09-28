/**
 * A message as the user submitted it from the composer, whole: its text, the
 * files attached to it and the workspace images the @ picker attached. It is
 * what a message carries while it waits in a run's queue, and what the
 * composer gets back when a message is withdrawn or recalled for editing, so
 * nothing the user attached is dropped on the way.
 */
export interface ComposerSubmission {
  text: string
  attachments: SubmittedAttachment[]
  /** Workspace-relative paths the @ picker resolved to images. */
  mentionImages: string[]
}

/** An uploaded attachment, with what the composer shows for it. */
export interface SubmittedAttachment {
  fileId: string
  filename: string
  mediaType: string
}

export function emptySubmission(): ComposerSubmission {
  return { text: '', attachments: [], mentionImages: [] }
}

/**
 * The draft a submission given back to the composer makes with what the
 * composer holds now. The returned message was written first, so it leads, and
 * whatever was typed or attached since follows it — nothing on either side is
 * dropped.
 */
export function mergeSubmissions(returned: ComposerSubmission, current: ComposerSubmission): ComposerSubmission {
  const attachments: SubmittedAttachment[] = []
  for (const attachment of [...returned.attachments, ...current.attachments]) {
    if (!attachments.some((existing) => existing.fileId === attachment.fileId)) attachments.push(attachment)
  }
  return {
    text: [returned.text.trim(), current.text.trim()].filter(Boolean).join('\n'),
    attachments,
    mentionImages: [...new Set([...returned.mentionImages, ...current.mentionImages])],
  }
}

/** A file picked in the composer for the message being sent. */
export interface PickedFile {
  file: File
  filename: string
  mediaType: string
}

export interface UploadedSubmission {
  submission: ComposerSubmission
  /** Picked files that were not uploaded, in the order they were picked. */
  unsent: File[]
  /** The upload that failed, when one did. */
  failure: { filename: string; error: unknown } | null
}

/**
 * Uploads the files picked for a message and builds the submission it
 * becomes, after what the composer already attached. Uploading stops at the
 * first file that fails, and that file and every one after it come back
 * unsent: a message is never sent without something the user attached to it.
 */
export async function uploadSubmission(
  text: string,
  attached: Pick<ComposerSubmission, 'attachments' | 'mentionImages'>,
  picked: PickedFile[],
  upload: (file: File) => Promise<{ fileId: string }>,
): Promise<UploadedSubmission> {
  const uploaded: SubmittedAttachment[] = []
  const unsent: File[] = []
  let failure: UploadedSubmission['failure'] = null
  for (const item of picked) {
    if (failure) {
      unsent.push(item.file)
      continue
    }
    try {
      const { fileId } = await upload(item.file)
      uploaded.push({ fileId, filename: item.filename, mediaType: item.mediaType })
    } catch (error) {
      failure = { filename: item.filename, error }
      unsent.push(item.file)
    }
  }
  return {
    submission: { text, attachments: [...attached.attachments, ...uploaded], mentionImages: attached.mentionImages },
    unsent,
    failure,
  }
}
