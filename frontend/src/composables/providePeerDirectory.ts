import { inject, provide, type InjectionKey, type Ref } from 'vue'
import type { PeerDirectoryView } from './useChatStream'

/**
 * The conversation's peer directory, shared down its tree.
 *
 * A letter's sender carries a kind label and a "same tree" chip only when the
 * directory knows them — the conversation's own letters and the letters a
 * subagent receives ask the same two questions of the same directory, so both
 * read it through the one pair of lookups below. The page provides the ref the
 * stream keeps; a card rendered outside a provider — a test harness, a story —
 * injects null and simply carries no label, the state it had before this
 * existed.
 */
export const PeerDirectoryKey: InjectionKey<Ref<PeerDirectoryView>> = Symbol('forebrain-peer-directory')

/** Publishes the conversation's peer directory to its subtree. */
export function providePeerDirectory(directory: Ref<PeerDirectoryView>): Ref<PeerDirectoryView> {
  provide(PeerDirectoryKey, directory)
  return directory
}

/** The subtree's peer directory, or null where none was provided. */
export function injectPeerDirectory(): Ref<PeerDirectoryView> | null {
  return inject(PeerDirectoryKey, null)
}

/** What the directory knows about a letter's sender, for the card's own labels.
 *  A sender the directory has not listed yet simply carries no label. */
export function peerKindOf(directory: PeerDirectoryView | null | undefined, name?: string): string {
  const agent = String(name ?? '').trim()
  if (!agent || !directory) return ''
  return directory.sameTree.find((row) => row.name === agent)?.kind
    ?? directory.other.find((row) => row.name === agent)?.kind
    ?? ''
}

/** Whether the letter's sender works in this directory tree. */
export function peerSameTreeOf(directory: PeerDirectoryView | null | undefined, name?: string): boolean {
  const agent = String(name ?? '').trim()
  return Boolean(agent) && Boolean(directory?.sameTree.some((row) => row.name === agent))
}