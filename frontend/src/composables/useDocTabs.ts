/**
 * The drawer's document tabs (owner 2026-10-25): a card head that had to
 * truncate — or a command with more than one line — opens its whole text as a
 * tab of the conversation's right-hand drawer, the same drawer the subagents
 * and the plan documents live in. The tab is the one way to see a whole text
 * that did not fit; no popups, no expanding rows.
 */
import { t } from '@/locales'
import type { ArtifactView } from '@/lib/artifacts'

/** One document open as a tab of the drawer. */
export interface DocTab {
  id: string
  kind: 'command' | 'title' | 'output' | 'artifact'
  /** What the tab says: 命令 · <第一行> / 标题 · <标题> (or its English word) /
   *  the delivered file's own name. */
  label: string
  /** The whole text the tab holds (empty for a delivered file). */
  text: string
  /** The command as it ran, when the tab is a command (what copy gives). */
  command?: string
  /** Where the text came from, for a future live re-read (137's output). */
  source?: { sessionId: string; agentId?: string; retrieveId?: number }
  /** The delivered file this tab previews (plan 153): what to render, and
   *  the URLs to render it from. */
  artifact?: ArtifactView
}

/** A short stable digest, so the same content is the same tab. */
function digest(text: string): string {
  let hash = 5381
  for (let i = 0; i < text.length; i++) {
    hash = ((hash << 5) + hash + text.charCodeAt(i)) | 0
  }
  return (hash >>> 0).toString(36)
}

/**
 * docTabFor builds the tab a "show all" opens. The id is the kind and a
 * digest of the text, so opening the same content again only focuses the tab
 * that already holds it.
 *
 * A delivered file (kind 'artifact') is identified by what it *is* — its
 * conversation and its artifact id — never by a digest: two files can hold
 * the same bytes, and presenting a revised version must still focus the one
 * tab the reader opened.
 */
export function docTabFor(
  kind: 'command' | 'title' | 'artifact',
  text: string,
  extra: { command?: string; source?: DocTab['source']; artifact?: ArtifactView } = {},
): DocTab {
  if (kind === 'artifact') {
    const artifact = extra.artifact
    if (!artifact) throw new Error('docTabFor: an artifact tab needs its file')
    return {
      id: `doc:artifact:${artifact.sessionId}:${artifact.id}`,
      kind,
      label: artifact.name || artifact.title,
      text: '',
      artifact,
    }
  }
  const firstLine = String(text ?? '').split('\n').map((line) => line.trim()).find(Boolean) ?? ''
  const label = kind === 'command'
    ? t('chat.docTabCommand', { first: firstLine })
    : t('chat.docTabTitle', { title: firstLine })
  return {
    id: `doc:${kind}:${digest(text)}`,
    kind,
    label,
    text,
    ...(extra.command ? { command: extra.command } : {}),
    ...(extra.source ? { source: extra.source } : {}),
  }
}
