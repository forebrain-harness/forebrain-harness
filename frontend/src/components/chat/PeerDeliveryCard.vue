<template>
  <!-- What became of the messages this conversation sent (plan 200 §3): one row
       per recipient, each with its identity block, its own state tag and the
       plain reason. The states are the protocol's; the words are ours, so the
       card reads the same whatever language the reader is in. -->
  <article class="peer-delivery" data-testid="peer-delivery-card">
    <div class="head">
      <span class="node" aria-hidden="true"><Send /></span>
      <span class="txt">{{ t('chat.peerDeliveryTitle') }}</span>
    </div>
    <div class="panel">
      <ul class="receipts">
        <li v-for="row in receipts" :key="row.name" :data-testid="'peer-receipt-' + row.status">
          <PeerMark :mark="row.mark" :tone="row.tone" :name="row.name" />
          <span class="n">{{ row.name }}</span>
          <span class="state" :class="`state--${row.status}`">{{ statusLabel(row.status) }}</span>
          <span class="why">{{ reasonLabel(row) }}</span>
        </li>
      </ul>
    </div>
  </article>
</template>

<script setup lang="ts">
/**
 * The delivery status card (plan 200 §3): a note this process wrote about the
 * messages it sent, drawn as one row per recipient. The rows arrive on the
 * message from the engine, which read them back out of its own receipt summary
 * — the surface never parses the note.
 */
import { Send } from 'lucide-vue-next'
import PeerMark from './PeerMark.vue'
import { useI18n } from '@/locales'

withDefaults(defineProps<{
  receipts: { name: string; status: string; reason?: string; mark: string; tone: number }[]
}>(), { receipts: () => [] })

const { t } = useI18n()

/** The state's own word. Held is the one state that is still waiting, so it is
 *  the only amber one; delivered is green, denied red, and everything that
 *  never reached the reader is grey. */
function statusLabel(status: string): string {
  switch (String(status ?? '').trim()) {
    case 'held':
      return t('chat.peerReceiptHeld')
    case 'delivered':
      return t('chat.peerReceiptDelivered')
    case 'denied':
      return t('chat.peerReceiptDenied')
    case 'refused':
      return t('chat.peerReceiptRefused')
    case 'dropped':
      return t('chat.peerReceiptDropped')
    default:
      return t('chat.peerReceiptExpired')
  }
}

/**
 * The plain reason beside the tag. The four common states have a sentence of
 * their own; a refusal or a drop carries the engine's reason, which is the only
 * place the protocol's own cause is written.
 */
function reasonLabel(row: { status: string; reason?: string }): string {
  const reason = String(row.reason ?? '').trim()
  switch (String(row.status ?? '').trim()) {
    case 'held':
      return t('chat.peerReceiptHeldWhy')
    case 'delivered':
      return t('chat.peerReceiptDeliveredWhy')
    case 'denied':
      return t('chat.peerReceiptDeniedWhy')
    case 'expired':
      return t('chat.peerReceiptExpiredWhy')
    default:
      return reason
  }
}
</script>

<style scoped>
/* The prototype's .receipts (:134-142): a bordered panel under a bare head, one
   row per recipient, the state a tag and the reason the remaining width. */
.peer-delivery {
  margin: 4px 0 16px;
  min-width: 0;
}

.peer-delivery .head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 0;
}

.peer-delivery .node {
  width: 20px;
  height: 20px;
  margin-left: -32px;
  display: grid;
  place-items: center;
  color: var(--forebrain-muted-text);
  background: var(--forebrain-surface);
  box-shadow: inset 0 0 0 1.5px var(--forebrain-divider);
  border-radius: 50%;
}

.peer-delivery .node :deep(svg) {
  width: 12px;
  height: 12px;
  stroke-width: 2.2;
}

.peer-delivery .txt {
  font-size: 13.5px;
  color: var(--forebrain-text);
}

.peer-delivery .panel {
  margin-top: 2px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
  overflow: hidden;
}

.peer-delivery .receipts {
  list-style: none;
  margin: 0;
  padding: 4px 0;
}

.peer-delivery .receipts li {
  display: grid;
  grid-template-columns: 20px auto auto minmax(0, 1fr);
  align-items: center;
  gap: 10px;
  padding: 7px 12px;
}

.peer-delivery .receipts li + li {
  border-top: 1px solid var(--forebrain-divider);
}

.peer-delivery .n {
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.peer-delivery .why {
  font-size: 12.5px;
  color: var(--forebrain-text-2);
  min-width: 0;
  overflow-wrap: anywhere;
}

/* The state tag: the one coloured thing in the row, and the only place a
   receipt's outcome is colour-coded. */
.peer-delivery .state {
  display: inline-flex;
  align-items: center;
  height: 22px;
  padding: 0 8px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 500;
  white-space: nowrap;
}

.state--held {
  background: #fffaeb;
  color: #b54708;
  box-shadow: inset 0 0 0 1px #fedf89;
}

.state--delivered {
  background: #ecfdf3;
  color: #067647;
  box-shadow: inset 0 0 0 1px #abefc6;
}

.state--denied {
  background: #fef3f2;
  color: #b42318;
  box-shadow: inset 0 0 0 1px #fecdca;
}

.state--expired,
.state--refused,
.state--dropped {
  background: #f2f4f7;
  color: var(--forebrain-text-2);
  box-shadow: inset 0 0 0 1px var(--forebrain-divider);
}

:global(.dark) .state--held {
  color: #fec84b;
}

:global(.dark) .state--delivered {
  color: #6ce9a6;
}

:global(.dark) .state--denied {
  color: #fda29b;
}
</style>
