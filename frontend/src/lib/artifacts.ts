/**
 * Deliverables (plan 146/153): what an office or batch conversation handed
 * back, and how a surface draws it.
 *
 * Three places carry the same rows — the `present_files` card's structured
 * output, the run-end frames' `artifacts`, and the conversation's artifact
 * list — so every one of them is read into one shape here. Nothing is
 * guessed: `kind`, `preview` and `size` are the engine's own
 * (pkg/tool/artifacts.go's classification), and a file the model never
 * presented does not become a row.
 */
import type { Component } from 'vue'
import type { I18nKey } from '@/locales'
import {
  Code,
  File,
  FileArchive,
  FileText,
  Film,
  Image,
  Music,
  Palette,
  Presentation,
  Ruler,
  Table,
} from 'lucide-vue-next'

/** The engine's class of a delivered file (pkg/tool/artifacts.go). */
export type ArtifactKind =
  | 'document'
  | 'spreadsheet'
  | 'presentation'
  | 'pdf'
  | 'image'
  | 'video'
  | 'audio'
  | 'html'
  | 'markdown'
  | 'text'
  | 'archive'
  | 'design'
  | 'cad'
  | 'other'

/**
 * How a surface may show a file in place — the engine's `preview` value.
 * `''` means download only: nothing here renders it.
 */
export type ArtifactPreviewKind =
  | ''
  | 'image'
  | 'video'
  | 'audio'
  | 'pdf'
  | 'html'
  | 'markdown'
  | 'text'
  | 'docx'
  | 'sheet'
  | 'office_pdf'
  | 'archive'

/**
 * Who put a file in the conversation (pkg/state/artifact_store.go's origins):
 * the model's own `present_files`, a batch task's output folder collected
 * because the task presented nothing, or an office plan kept as the
 * deliverable. `''` is a row that did not say — the card's own structured rows
 * predate the field.
 */
export type ArtifactOrigin = 'model' | 'fallback' | 'plan' | ''

/** One row of the delivery group (and of the card that named the files). */
export interface ArtifactView {
  /** The conversation the file belongs to; every URL below hangs off it. */
  sessionId: string
  id: string
  /** The run that delivered it, when the row says — what ties a file to the
   *  turn whose delivery group draws it. */
  runId?: string
  /** Where the file came from. What names a delivery group is asked of this,
   *  so a reloaded turn answers it from the artifact list alone. */
  origin: ArtifactOrigin
  /** The file's own name — what a row and a drawer tab are titled by. */
  name: string
  /** The model's short title when it gave one, else the name. */
  title: string
  kind: ArtifactKind
  preview: ArtifactPreviewKind
  size: number
  /** The bytes are gone from disk (the row outlives them). */
  missing: boolean
  /** The bytes are no longer the ones that were presented. */
  changed: boolean
  /** `…/content?disposition=attachment`, or '' when there is no session. */
  downloadUrl: string
  /** `…/content?disposition=inline`: what an in-place preview reads. */
  contentUrl: string
  /** `…/preview.pdf`, for an office document LibreOffice converted. */
  previewUrl?: string
  /** `…/entries`, for an archive's directory list. */
  entriesUrl?: string
}

/** A kind's label key and glyph. The keys live in the locale tables. */
const KIND_TABLE: Record<ArtifactKind, { labelKey: I18nKey; icon: Component }> = {
  document: { labelKey: 'chat.artifactKindDocument', icon: FileText },
  spreadsheet: { labelKey: 'chat.artifactKindSpreadsheet', icon: Table },
  presentation: { labelKey: 'chat.artifactKindPresentation', icon: Presentation },
  pdf: { labelKey: 'chat.artifactKindPdf', icon: FileText },
  image: { labelKey: 'chat.artifactKindImage', icon: Image },
  video: { labelKey: 'chat.artifactKindVideo', icon: Film },
  audio: { labelKey: 'chat.artifactKindAudio', icon: Music },
  html: { labelKey: 'chat.artifactKindHtml', icon: Code },
  markdown: { labelKey: 'chat.artifactKindMarkdown', icon: FileText },
  text: { labelKey: 'chat.artifactKindText', icon: FileText },
  archive: { labelKey: 'chat.artifactKindArchive', icon: FileArchive },
  design: { labelKey: 'chat.artifactKindDesign', icon: Palette },
  cad: { labelKey: 'chat.artifactKindCad', icon: Ruler },
  other: { labelKey: 'chat.artifactKindOther', icon: File },
}

/** A kind the engine sent, narrowed to the table above. */
export function artifactKind(kind: unknown): ArtifactKind {
  const key = String(kind ?? '').trim().toLowerCase()
  return key in KIND_TABLE ? (key as ArtifactKind) : 'other'
}

/** The locale key naming a kind (文档 / 表格 / …). */
export function artifactKindLabelKey(kind: unknown): I18nKey {
  return KIND_TABLE[artifactKind(kind)].labelKey as I18nKey
}

/** The glyph a row, a drawer tab and the delivery head draw for a kind. */
export function artifactKindIcon(kind: unknown): Component {
  return KIND_TABLE[artifactKind(kind)].icon
}

/** A preview value the engine sent, narrowed to the dispatcher's cases. */
const PREVIEW_KINDS: readonly ArtifactPreviewKind[] = [
  'image', 'video', 'audio', 'pdf', 'html', 'markdown', 'text',
  'docx', 'sheet', 'office_pdf', 'archive',
]

export function artifactPreview(preview: unknown): ArtifactPreviewKind {
  const key = String(preview ?? '').trim().toLowerCase()
  return (PREVIEW_KINDS as readonly string[]).includes(key) ? (key as ArtifactPreviewKind) : ''
}

/**
 * humanSize reads a byte count the way the engine prints it
 * (pkg/tool/artifacts.go HumanSize): one decimal on KB, MB and GB, plain
 * bytes under a kilobyte. A card that said "1.2 MB" while the notification
 * said "1232896 B" would be two accounts of one file.
 */
export function humanSize(bytes: number): string {
  const n = Number(bytes)
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`
  return `${Math.round(n)} B`
}

/** The three URLs a delivered file is reached by, from its conversation. */
function urlsFor(sessionId: string, id: string, preview: ArtifactPreviewKind) {
  if (!sessionId || !id) return { downloadUrl: '', contentUrl: '' }
  const base = `/api/chat/sessions/${encodeURIComponent(sessionId)}/artifacts/${encodeURIComponent(id)}`
  return {
    downloadUrl: `${base}/content?disposition=attachment`,
    contentUrl: `${base}/content?disposition=inline`,
    ...(preview === 'office_pdf' ? { previewUrl: `${base}/preview.pdf` } : {}),
    ...(preview === 'archive' ? { entriesUrl: `${base}/entries` } : {}),
  }
}

/** A row's origin, narrowed to the three the engine writes; '' when it names
 *  none (a card's own structured row, or a row older than the field). */
export function artifactOrigin(origin: unknown): ArtifactOrigin {
  const key = String(origin ?? '').trim().toLowerCase()
  return key === 'model' || key === 'fallback' || key === 'plan' ? key : ''
}

/**
 * artifactView reads one row into the shape the card draws. The row may be
 * the gateway's REST row (which already carries URLs and the two disk facts)
 * or the engine's own structured row off a card or a run-end frame (which
 * carries neither) — the missing half is built from the conversation it
 * belongs to. Null when the row names no file at all.
 *
 * Keys are the camelCase the whole client speaks: axios converts REST bodies
 * on the way in and the socket's frames are converted on arrival, so a
 * `rel_path` never reaches this function.
 */
export function artifactView(sessionId: string, raw: unknown): ArtifactView | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const row = raw as Record<string, unknown>
  const id = String(row.id ?? '').trim()
  if (!id) return null
  const name = String(row.name ?? '').trim()
  const preview = artifactPreview(row.preview)
  const built = urlsFor(sessionId, id, preview)
  const title = String(row.title ?? '').trim() || name
  return {
    sessionId,
    id,
    ...(String(row.runId ?? '').trim() ? { runId: String(row.runId).trim() } : {}),
    origin: artifactOrigin(row.origin),
    name,
    title,
    kind: artifactKind(row.kind),
    preview,
    size: Number(row.size) || 0,
    missing: row.missing === true,
    changed: row.changed === true,
    downloadUrl: String(row.downloadUrl ?? '').trim() || built.downloadUrl,
    contentUrl: String(row.contentUrl ?? '').trim() || built.contentUrl,
    ...(String(row.previewUrl ?? '').trim() || built.previewUrl
      ? { previewUrl: String(row.previewUrl ?? '').trim() || built.previewUrl }
      : {}),
    ...(String(row.entriesUrl ?? '').trim() || built.entriesUrl
      ? { entriesUrl: String(row.entriesUrl ?? '').trim() || built.entriesUrl }
      : {}),
  }
}

/** One list of rows, rows that are not a file dropped. */
export function artifactViews(sessionId: string, rows: unknown): ArtifactView[] {
  if (!Array.isArray(rows)) return []
  return rows.flatMap((row) => {
    const view = artifactView(sessionId, row)
    return view ? [view] : []
  })
}

/**
 * artifactsOfPresentFiles reads the `present_files` card's structured output:
 * `presented[]{id,name,rel_path,title,kind,preview,size}` (pkg/tool/artifacts.go).
 * A completion that produced no such shape — a refusal, a row stored without
 * its structured half — yields none, and the group then falls back on the
 * run's end frame and the conversation's artifact list.
 */
export function artifactsOfPresentFiles(result: unknown, sessionId: string): ArtifactView[] {
  if (!result || typeof result !== 'object' || Array.isArray(result)) return []
  // A presented file is one the model put forward: the card's rows carry no
  // origin, and registration gave every one of them that same one.
  return artifactViews(sessionId, (result as Record<string, unknown>).presented)
    .map((view): ArtifactView => ({ ...view, origin: 'model' }))
}

/**
 * planArtifactOf reads the artifact an `exit_plan_mode` kept as a deliverable
 * (plan 148): the call's envelope carries `{plan_only, artifact:{id,name,
 * kind,preview,size}}` and registers that file with origin `plan`.
 */
export function planArtifactOf(result: unknown, sessionId: string): ArtifactView | null {
  if (!result || typeof result !== 'object' || Array.isArray(result)) return null
  const envelope = result as Record<string, unknown>
  if (envelope.planOnly !== true && envelope.plan_only !== true) return null
  const view = artifactView(sessionId, envelope.artifact)
  // The envelope names no origin: a file reached through this call is the
  // plan that was kept, which is what registration records with it.
  return view ? { ...view, origin: 'plan' } : null
}

/**
 * runFrameArtifacts reads what a run's end frame listed (`artifacts[]`, the
 * wire shape of pkg/event.Artifact). Every way a run ends carries it. Each row
 * names its own conversation, so a frame reaches this with the row's session
 * in hand; `sessionId` is only the fallback for a row that left it out. The
 * frame carries the row's origin too; a frame recorded before the field
 * existed can only have presented files.
 */
export function runFrameArtifacts(payload: unknown, sessionId = ''): ArtifactView[] {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return []
  const rows = (payload as Record<string, unknown>).artifacts
  if (!Array.isArray(rows)) return []
  return rows.flatMap((raw) => {
    const own = raw && typeof raw === 'object' && !Array.isArray(raw)
      ? String((raw as Record<string, unknown>).sessionId ?? '').trim()
      : ''
    const view = artifactView(own || sessionId, raw)
    return view ? [{ ...view, origin: view.origin || 'model' }] : []
  })
}

/** Rows by id, the first of a repeated id winning and delivery order kept. */
export function dedupeArtifacts(rows: ArtifactView[]): ArtifactView[] {
  const seen = new Set<string>()
  const out: ArtifactView[] = []
  for (const row of rows) {
    if (seen.has(row.id)) continue
    seen.add(row.id)
    out.push(row)
  }
  return out
}

/**
 * turnDeliverables is the turn's one delivery group (plan 153 A7, owner
 * 2026-10-27): every source of delivered files — a "keep the plan only" exit's
 * plan, what each `present_files` card named, the run's end frame, and, after
 * a reload, what the conversation's artifact list holds for that run — merged
 * by id into the single list the turn shows once, under its last words.
 *
 * Order carries meaning: the plan comes first (it is what the reader asked
 * for), then the files in the order the model presented them, then whatever
 * only a frame or the list knew about. Deduplication keeps the first
 * occurrence, so a file that is both presented and listed keeps its presented
 * place, and a reloaded turn — its card rebuilt from the same structured rows
 * — shows exactly what the live one did.
 */
export function turnDeliverables(input: {
  /** The rows each `present_files` card of this turn named. */
  presented: ArtifactView[]
  /** The plan file a kept-plan exit registered. */
  plan: ArtifactView[]
  /** What this run's end frame listed. */
  frame: ArtifactView[]
  /** The artifact list's rows for this run (a reload's fallback). */
  fallback: ArtifactView[]
}): ArtifactView[] {
  return dedupeArtifacts([...input.plan, ...input.presented, ...input.frame, ...input.fallback])
}
