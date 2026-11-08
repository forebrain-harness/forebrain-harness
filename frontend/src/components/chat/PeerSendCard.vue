<template>
  <!-- A peer_send answer (pkg/tool ReadablePeerSend): the message that left,
       then the delivery as a footnote on the recipient's own identity block —
       the prototype's 已发消息 card. The message is the reader's own words, so
       it keeps its line breaks; the footnote says what the recipient was doing
       when the message reached it. -->
  <div class="peer-send" data-testid="peer-send">
    <p v-if="message" class="message">{{ message }}</p>
    <div v-if="footnote" class="foot" data-testid="peer-send-foot">
      <PeerMark
        v-if="to"
        :mark="send.mark ?? ''"
        :tone="send.tone ?? 0"
        :name="to"
      />
      <span v-if="to" class="to">{{ to }}</span>
      <span class="note">{{ footnote }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The card a sent peer message leaves (plan 199 §3.2): the message, and where
 * it went. The engine reads both out of the call; this words the delivery in
 * the reader's language, so the terminal and the page never disagree about
 * what a delivery means.
 */
import { computed } from 'vue'
import PeerMark from './PeerMark.vue'
import type { ReadablePeerSend } from '@/composables/useChatStream'
import { useI18n } from '@/locales'

const props = defineProps<{ send: ReadablePeerSend }>()

const { t } = useI18n()

const message = computed(() => String(props.send.message ?? '').replace(/\r\n/g, '\n').trim())

const to = computed(() => String(props.send.to ?? '').trim())

/**
 * footnote is what became of the message in one sentence: a recipient at work
 * reads it at its next step, an idle one was started, a subagent that has
 * handed its result back takes no message at all. A status this build does not
 * know is not explained.
 */
const footnote = computed(() => {
  const status = String(props.send.status ?? '').trim().toLowerCase()
  const peerStatus = String(props.send.peerStatus ?? '').trim().toLowerCase()
  switch (status) {
    case 'delivered':
      switch (peerStatus) {
        case 'busy':
        case 'waiting':
        case 'shell':
          return t('chat.peerSendBusy')
        case 'idle':
          return t('chat.peerSendIdle')
        default:
          return t('chat.peerSendDelivered')
      }
    case 'queued':
      return t('chat.peerSendQueued')
    case 'idle':
      return t('chat.peerSendIdleSubagent')
    default:
      return ''
  }
})
</script>

<style scoped>
/* The card's own body (ToolOutput's .tool-body): the same padding, size and
   bound, so a sent message reads as one more tool result. */
.peer-send {
  padding: 9px 11px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text);
  max-height: 360px;
  overflow: auto;
}

/* The message is the reader's own words: its line breaks are part of it, and
   a long token (a path, an id) wraps instead of widening the card. */
.message {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

/* The prototype's .panel .foot (app-preview.html): the delivery under the
   message, closed off by a rule, at the second text colour. */
.foot {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 9px;
  padding-top: 8px;
  border-top: 1px solid var(--forebrain-divider);
  font-size: 12.5px;
  color: var(--forebrain-text-2);
  min-width: 0;
}

.foot .to {
  font-weight: 600;
  color: var(--forebrain-text);
  white-space: nowrap;
}

.foot .note {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
