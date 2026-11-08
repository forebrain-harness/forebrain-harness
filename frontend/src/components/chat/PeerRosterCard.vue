<template>
  <!-- A peer_list answer (pkg/tool ReadablePeer): the machine's roster, one
       agent per row. The identity block is the same one the drawer and the
       letters wear; a subagent of this conversation is outlined in its
       parent's tone, an agent of another process filled. Status, what the
       agent is doing, and where it works close the row. -->
  <div class="peer-roster" data-testid="peer-roster">
    <p v-if="!rows.length" class="empty" data-testid="peer-roster-empty">{{ t('chat.peerRosterEmpty') }}</p>
    <ol v-else class="rows">
      <li
        v-for="row in rows"
        :key="row.peer.name"
        class="peer-roster-row"
        data-testid="peer-roster-row"
      >
        <PeerMark
          :mark="row.peer.mark ?? ''"
          :tone="row.peer.tone ?? 0"
          :name="row.peer.name"
          :kind="row.peer.own ? 'sub' : 'agent'"
        />
        <div class="body">
          <div class="l1">
            <span class="nm">{{ row.name }}</span>
            <span class="st" :class="statusClass(row.peer.status)">
              <i class="dot" aria-hidden="true" />{{ peerStatusLabel(row.peer.status, t) }}
            </span>
          </div>
          <div v-if="row.peer.task" class="l2">{{ row.peer.task }}</div>
          <div v-if="row.place" class="l3" :class="{ path: row.path }">{{ row.place }}</div>
        </div>
      </li>
    </ol>
  </div>
</template>

<script setup lang="ts">
/**
 * The roster a peer_list call answers with, drawn in the peer design language
 * (plan 200 §3): identity blocks, the four tones, the second text colour for
 * everything that is not a name. The names and the block come from the
 * engine; this only lays them out and words the status and the place.
 */
import { computed } from 'vue'
import PeerMark from './PeerMark.vue'
import { peerKindLabel, peerStatusLabel } from '@/lib/peerMark'
import type { ReadableAnswer, ReadablePeer } from '@/composables/useChatStream'
import { useI18n } from '@/locales'

const props = defineProps<{ answer: ReadableAnswer }>()

const { t } = useI18n()

/**
 * rows pairs each agent with the name it shows. Two subagents of one type are
 * told apart by the short key the engine derived — the rule the terminal's
 * roster follows — so a card never shows the same name twice.
 */
const rows = computed(() => {
  const peers = props.answer.peers ?? []
  const same = new Map<string, number>()
  for (const peer of peers) same.set(displayOf(peer), (same.get(displayOf(peer)) ?? 0) + 1)
  return peers.map((peer) => {
    let name = displayOf(peer)
    if ((same.get(name) ?? 0) > 1 && peer.short) name += ` · ${peer.short}`
    return { peer, name, place: placeOf(peer), path: isPathPlace(peer) }
  })
})

function displayOf(peer: ReadablePeer): string {
  return String(peer.display ?? '').trim() || String(peer.name ?? '').trim()
}

/**
 * placeOf is where an agent works and how long it has been idle: the kind of
 * session, "same directory" when that is this conversation's own tree, or the
 * directory it works in — the path is elided from its head by CSS, so the
 * end that names the project survives a narrow card.
 */
function placeOf(peer: ReadablePeer): string {
  const parts: string[] = []
  if (!peer.own) {
    const kind = peerKindLabel(peer.kind, t)
    if (kind) parts.push(kind)
    if (peer.sameTree) parts.push(t('chat.peerSameTree'))
    else if (peer.cwd) parts.push(peer.cwd)
  }
  if (String(peer.status ?? '').trim().toLowerCase() === 'idle') {
    const ago = agoLabel(peer.activeAgo)
    if (ago) parts.push(ago)
  }
  return parts.join(' · ')
}

/** isPathPlace reports whether a row's place is a directory rather than a
 * word: only a path is elided from its head. */
function isPathPlace(peer: ReadablePeer): boolean {
  if (peer.own || peer.sameTree) return false
  const cwd = String(peer.cwd ?? '').trim()
  return cwd.startsWith('/') || cwd.startsWith('~')
}

/** agoLabel is the engine's coarse "45m" in the reader's own words. */
function agoLabel(ago?: string): string {
  const text = String(ago ?? '').trim()
  if (!text) return ''
  if (text === 'just now') return t('chat.peerJustNow')
  const minutes = /^(\d+)m$/.exec(text)
  if (minutes) return t('chat.peerMinutesAgo', { count: Number(minutes[1]) })
  const hours = /^(\d+)h$/.exec(text)
  if (hours) return t('chat.peerHoursAgo', { count: Number(hours[1]) })
  return text
}

/** statusClass picks the dot's colour: at work, waiting on a person, done. */
function statusClass(status?: string): string {
  switch (String(status ?? '').trim().toLowerCase()) {
    case 'running':
    case 'busy':
    case 'shell':
      return 'st--on'
    case 'waiting':
      return 'st--wait'
    case 'finished':
      return 'st--done'
    default:
      return ''
  }
}
</script>

<style scoped>
/* The card's own body (ToolOutput's .tool-body): the same padding, size and
   bound, so a roster reads as one more tool result, not a new kind of box. */
.peer-roster {
  padding: 9px 11px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text);
  max-height: 360px;
  overflow: auto;
}

.empty {
  margin: 0;
  color: var(--forebrain-text-2);
}

.rows {
  margin: 0;
  padding: 0;
  list-style: none;
}

/* One agent is a block of up to three lines beside its identity block; the
   dashed rule between agents is the drawer's own (plan 200 §3). */
.peer-roster-row {
  display: grid;
  grid-template-columns: 20px minmax(0, 1fr);
  gap: 2px 10px;
  padding: 9px 0;
}

.peer-roster-row:first-child {
  padding-top: 0;
}

.peer-roster-row:last-child {
  padding-bottom: 0;
}

.peer-roster-row + .peer-roster-row {
  border-top: 1px dashed var(--forebrain-divider);
}

.peer-roster-row > .peer-mark {
  grid-row: span 3;
}

.body {
  min-width: 0;
}

.l1 {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.nm {
  font-weight: 600;
  font-size: 13.5px;
  color: var(--forebrain-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.st {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-text-2);
  white-space: nowrap;
}

.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--forebrain-divider-strong);
}

.st--on .dot {
  background: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
}

.st--wait .dot {
  background: var(--forebrain-warning);
  box-shadow: 0 0 0 3px var(--forebrain-warning-soft);
}

.st--done .dot {
  background: var(--forebrain-success);
}

.l2 {
  font-size: 13px;
  color: var(--forebrain-text-2);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* The place is a path as often as a word: mono, cut at its end; a path is
   elided from its head instead (right-to-left overflow, drawn left-aligned),
   so the end that names the project is what survives a narrow card. */
.l3 {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  font-family: var(--forebrain-mono-font);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.l3.path {
  direction: rtl;
  text-align: left;
}
</style>
