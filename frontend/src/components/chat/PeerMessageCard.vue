<template>
  <!-- A letter from another agent (plan 200 §3): left-aligned and unframed, the
       way the terminal draws it — not a bubble of the reader's own. The header
       names who wrote it and where from; the body carries that agent's tone as
       a rule down its left edge, so two letters from two agents never read as
       one conversation. -->
  <article
    class="peer-letter"
    :style="peerToneVar(tone)"
    :data-testid="'peer-message-' + (mark || 'card')"
  >
    <div class="who">
      <PeerMark :mark="mark" :tone="tone" :name="fromName" />
      <span class="name">{{ fromName || t('chat.peerUnknownAgent') }}</span>
      <span v-if="kind" class="kind">{{ kindLabel }}</span>
      <span v-if="sameTree" class="near" data-testid="peer-same-tree">{{ t('chat.peerSameTree') }}</span>
      <time v-if="when" class="when">{{ when }}</time>
    </div>
    <div class="text">
      <MessageResponse :content="body" />
    </div>
  </article>
</template>

<script setup lang="ts">
/**
 * One peer message as the conversation draws it (plan 200 §3). Everything it
 * shows — the sender's name, the body, the two characters and the tone — was
 * derived by the engine and arrives on the message row; the surface only lays
 * it out, so a live letter and a reloaded one are the same card.
 */
import { computed } from 'vue'
import { MessageResponse } from '@repo/elements/message'
import PeerMark from './PeerMark.vue'
import { peerClockTime, peerKindLabel, peerToneVar } from '@/lib/peerMark'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  /** The sender's name, as the envelope carried it. */
  fromName?: string
  /** The identity block's two characters, derived by the engine. */
  mark?: string
  /** The identity block's tone, 0..3. */
  tone?: number
  /** The body the envelope carried. Absent when the row was not an envelope. */
  body?: string
  /** The row's own content, used when the envelope could not be read. */
  content?: string
  /** What kind of session the sender is, when the directory knows it. */
  kind?: string
  /** Whether the sender works in this directory tree. */
  sameTree?: boolean
  /** The row's creation time, in milliseconds. */
  createdAt?: number
}>(), { fromName: '', mark: '', tone: 0, body: '', content: '', kind: '', sameTree: false, createdAt: undefined })

const { t, locale } = useI18n()

const body = computed(() => String(props.body ?? '').trim() || String(props.content ?? '').trim())
const when = computed(() => peerClockTime(props.createdAt, locale.value))
const kindLabel = computed(() => peerKindLabel(props.kind, t))
</script>

<style scoped>
/* The prototype's .letter (:123-131): no frame, no background — the tone's rule
   is the only thing that sets it apart from the reader's own words. */
.peer-letter {
  margin: 4px 0 16px;
  min-width: 0;
}

.peer-letter .who {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.peer-letter .name {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--forebrain-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.peer-letter .kind {
  font-size: 12.5px;
  color: var(--forebrain-text-2);
  white-space: nowrap;
}

/* "同目录" is a neutral chip: it says where the sender works, not what it is
   doing, so it carries no tone. */
.peer-letter .near {
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11.5px;
  color: var(--forebrain-text-2);
  background: var(--forebrain-surface-2, #f2f4f7);
  white-space: nowrap;
}

.peer-letter .when {
  margin-left: auto;
  color: var(--forebrain-muted-text);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* The body hangs on the sender's tone, indented past the block. */
.peer-letter .text {
  margin: 6px 0 0 9px;
  padding: 1px 0 1px 18px;
  border-left: 2px solid var(--peer-tone);
  font-size: 14.5px;
  line-height: 1.75;
  color: var(--forebrain-text);
  min-width: 0;
  overflow-wrap: anywhere;
}
</style>
