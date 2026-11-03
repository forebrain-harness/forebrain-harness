<template>
  <!-- The one thing above the composer that is not a notice: a peer message
       waiting for the person's decision (plan 200 §3). It is framed in the
       brand's own colour, the same family as an approval, because it is the
       same kind of question — one only the person can answer. -->
  <section
    v-if="held"
    class="peer-hold"
    :class="{ 'peer-hold--gone': gone }"
    :style="peerToneVar(held.tone)"
    :aria-label="t('chat.peerHoldAria')"
    data-testid="peer-held-banner"
  >
    <div class="peer-hold-in">
      <div class="top-row">
        <PeerMark :mark="held.mark" :tone="held.tone" :name="held.from_name" />
        <span class="lead">
          <b>{{ held.from_name || t('chat.peerUnknownAgent') }}</b>
          {{ t('chat.peerHoldLead') }}
        </span>
        <span v-if="reason" class="reason">{{ reason }}</span>
      </div>
      <div ref="textEl" class="text">{{ held.preview }}</div>
      <div class="gate">
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary"
          data-testid="peer-held-release"
          @click="decide(true)"
        >{{ t('chat.peerHoldRelease') }}</button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost"
          data-testid="peer-held-deny"
          @click="decide(false)"
        >{{ t('chat.peerHoldDeny') }}</button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
/**
 * The held-message banner (plan 200 §3). Releasing it flies the body out of the
 * banner and into the thread, where it lands as the letter it always was, and
 * the banner collapses; denying it only collapses, leaving the thread's own
 * line saying what happened. Both answers go to the engine, which owns the
 * delivery and the receipt — this component only reports the person's choice.
 *
 * `landingRect` is where the letter the parent is about to insert will be, so
 * the flight ends where the reader will read it. A parent that cannot say
 * settles for the body fading where it was, which is still the same gesture.
 */
import { computed, nextTick, ref } from 'vue'
import PeerMark from './PeerMark.vue'
import { peerToneVar } from '@/lib/peerMark'
import type { PeerHeldRow } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  held: PeerHeldRow | null
  /** Where the released letter will land, in viewport coordinates. */
  landingRect?: () => DOMRect | null
}>(), { held: null, landingRect: undefined })

const emit = defineEmits<{
  (e: 'release', held: PeerHeldRow): void
  (e: 'deny', held: PeerHeldRow): void
  /** The banner has finished collapsing and the held row may be dropped. */
  (e: 'settled', held: PeerHeldRow): void
}>()

const { t } = useI18n()

const gone = ref(false)
const textEl = ref<HTMLElement | null>(null)
let settling = false

/** Why the message was held, in the reader's own words. The cause is the
 *  engine's; each one says what the person is being asked to settle. */
const reason = computed(() => {
  switch (String(props.held?.cause ?? '').trim()) {
    case 'mode-mismatch':
      return t('chat.peerHoldModeMismatch')
    case 'no-mode-asserted':
      return t('chat.peerHoldNoMode')
    case 'hold-all':
      return t('chat.peerHoldAll')
    default:
      return ''
  }
})

async function decide(release: boolean) {
  const held = props.held
  if (!held || settling) return
  settling = true
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  if (!release) {
    emit('deny', held)
    gone.value = true
    if (reduced) {
      emit('settled', held)
      return
    }
    window.setTimeout(() => emit('settled', held), 260)
    return
  }
  emit('release', held)
  if (reduced) {
    gone.value = true
    emit('settled', held)
    return
  }
  // The body leaves the banner before the banner leaves the page: the flight
  // is the reader's own sense of where the message went.
  const from = textEl.value?.getBoundingClientRect()
  await nextTick()
  const to = props.landingRect?.()
  if (from) fly(from, to)
  gone.value = true
  window.setTimeout(() => emit('settled', held), 260)
}

/** One clone of the body, animated from the banner to the letter it becomes. */
function fly(from: DOMRect, to: DOMRect | null | undefined) {
  const ghost = document.createElement('div')
  ghost.className = 'peer-hold-flyer'
  ghost.textContent = String(props.held?.preview ?? '')
  ghost.style.left = `${from.left}px`
  ghost.style.top = `${from.top}px`
  ghost.style.width = `${from.width}px`
  document.body.appendChild(ghost)
  const target = to ?? new DOMRect(from.left, from.top - 120, from.width, from.height)
  const dx = target.left - from.left
  const dy = target.top - from.top
  const animation = ghost.animate(
    [
      { transform: 'translate(0, 0) scale(1)', opacity: 1 },
      { transform: `translate(${dx}px, ${dy}px) scale(0.97)`, opacity: 0.4 },
    ],
    { duration: 420, easing: 'cubic-bezier(.2,.7,.2,1)' },
  )
  animation.onfinish = () => ghost.remove()
}
</script>

<style scoped>
/* The prototype's .hold (:172-183): a brand-bordered card above the composer,
   its body carrying the sender's tone as a rule, and a brand-tinted footer
   holding the two answers. It collapses by its own grid row, so the composer
   rises into the space it left. */
.peer-hold {
  margin: 0 0 10px;
  border: 1px solid var(--forebrain-brand-border);
  border-radius: 12px;
  background: var(--forebrain-surface);
  overflow: hidden;
  display: grid;
  grid-template-rows: 1fr;
  transition: grid-template-rows 0.26s cubic-bezier(0.2, 0.7, 0.2, 1),
    opacity 0.2s, margin 0.26s cubic-bezier(0.2, 0.7, 0.2, 1);
}

.peer-hold--gone {
  grid-template-rows: 0fr;
  opacity: 0;
  margin-bottom: 0;
  border-width: 0;
}

.peer-hold-in {
  min-height: 0;
  overflow: hidden;
}

.peer-hold .top-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px 0;
}

.peer-hold .lead {
  font-size: 13.5px;
  color: var(--forebrain-text);
  min-width: 0;
}

.peer-hold .lead b {
  font-weight: 600;
}

.peer-hold .reason {
  margin-left: auto;
  color: var(--forebrain-text-2);
  font-size: 12px;
  white-space: nowrap;
}

/* Three lines at most: the whole message is in the letter this becomes. */
.peer-hold .text {
  margin: 8px 12px 12px 21px;
  padding-left: 15px;
  border-left: 2px solid var(--peer-tone);
  color: var(--forebrain-text-2);
  font-size: 13.5px;
  line-height: 1.7;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.peer-hold .gate {
  display: flex;
  gap: 6px;
  padding: 9px 12px;
  background: var(--forebrain-brand-soft);
  border-top: 1px solid var(--forebrain-brand-border);
}

/* The flying copy of the body. It is the banner's own words, so it carries the
   same rule and measure; it exists only for the flight. */
.peer-hold-flyer {
  position: fixed;
  z-index: 40;
  pointer-events: none;
  background: var(--forebrain-surface);
  border-left: 2px solid var(--peer-tone, var(--forebrain-divider));
  border-radius: 4px;
  padding: 6px 10px;
  font-size: 13.5px;
  line-height: 1.7;
  color: var(--forebrain-text);
  box-shadow: 0 12px 28px rgba(16, 24, 40, 0.12);
  overflow: hidden;
}

@media (prefers-reduced-motion: reduce) {
  .peer-hold {
    transition: none;
  }
}
</style>
