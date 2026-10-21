<script setup lang="ts">
import { computed } from 'vue'
import { ArrowUp, ListChecks, PencilLine, Zap } from 'lucide-vue-next'
import {
  Queue, QueueItem, QueueItemContent, QueueItemIndicator,
  QueueList, QueueSection, QueueSectionContent, QueueSectionLabel, QueueSectionTrigger,
} from '@repo/elements/queue'
import { useI18n } from '@/locales'
import type { PendingInputPreview } from '@/composables/useChatStream'

/**
 * What is waiting behind the running turn, above the composer: the messages
 * that go out after the next tool call, the ones held for the end of the turn,
 * and the queued follow-ups.
 *
 * The queue itself is the library's `queue` family — the sections fold, the
 * rows are its items, the per-row control is its action — so this file carries
 * only this surface's three section titles, its counts and its two ways out.
 */
const props = defineProps<{
  preview: PendingInputPreview
}>()

const emit = defineEmits<{
  (e: 'edit-last-queued'): void
  (e: 'interrupt-run'): void
}>()

const { t } = useI18n()

const totalCount = computed(() => (
  props.preview.pendingSteers.length
  + props.preview.rejectedSteers.length
  + props.preview.queuedMessages.length
))

const visible = computed(() => totalCount.value > 0)

type QueueGroup = {
  id: string
  title: string
  hint?: string
  items: string[]
  italic?: boolean
  accent: string
}

const sections = computed<QueueGroup[]>(() => [
  {
    id: 'pending',
    title: t('chat.queueAfterToolCall'),
    hint: t('chat.queueAfterToolCallHint'),
    items: props.preview.pendingSteers,
    accent: 'bg-[var(--forebrain-brand-1)]',
  },
  {
    id: 'rejected',
    title: t('chat.queueAtTurnEnd'),
    items: props.preview.rejectedSteers,
    accent: 'bg-[var(--forebrain-warning)]',
  },
  {
    id: 'queued',
    title: t('chat.queueFollowUps'),
    items: props.preview.queuedMessages,
    italic: true,
    accent: 'bg-[var(--forebrain-text-2)]',
  },
].filter((section) => section.items.length > 0))
</script>

<template>
  <Transition
    enter-active-class="transition duration-150 ease-out"
    enter-from-class="translate-y-2 opacity-0"
    enter-to-class="translate-y-0 opacity-100"
    leave-active-class="transition duration-100 ease-in"
    leave-from-class="translate-y-0 opacity-100"
    leave-to-class="translate-y-2 opacity-0"
  >
    <div v-if="visible" class="pointer-events-none absolute inset-x-0 bottom-full z-30 mb-3 px-1">
      <Queue class="pointer-events-auto queue-card" data-testid="pending-queue">
        <div class="queue-head">
          <div class="flex min-w-0 items-center gap-2">
            <span class="queue-head-mark">
              <ListChecks class="size-4" aria-hidden="true" />
            </span>
            <div class="min-w-0">
              <div class="queue-head-title">{{ t('chat.queueTitle') }}</div>
              <div class="queue-head-sub">{{ t('chat.queuePending', { n: totalCount }) }}</div>
            </div>
          </div>
          <div class="flex shrink-0 items-center gap-1.5">
            <button
              v-if="preview.pendingSteers.length"
              type="button"
              class="queue-act queue-act--brand"
              data-testid="queue-interrupt"
              @click="emit('interrupt-run')"
            >
              <Zap class="size-3.5" aria-hidden="true" />
              <span>esc</span>
            </button>
            <button
              v-if="visible"
              type="button"
              class="queue-act"
              data-testid="queue-edit"
              @click="emit('edit-last-queued')"
            >
              <PencilLine class="size-3.5" aria-hidden="true" />
              <span>⌥ +</span>
              <ArrowUp class="size-3" aria-hidden="true" />
            </button>
          </div>
        </div>

        <!-- The list scrolls; the head above it does not. The card is a column
             whose max-height bounds the whole thing, so a short queue still
             shrinks to its content and a long one scrolls here instead of being
             cut off at the card's edge. -->
        <div class="queue-body" data-testid="queue-body">
          <QueueSection
            v-for="section in sections"
            :key="section.id"
            class="queue-section"
            data-testid="queue-section"
          >
            <QueueSectionTrigger class="queue-section-trigger">
              <QueueSectionLabel
                :count="section.items.length"
                :label="section.title"
                class="queue-section-label"
              >
                <template #icon>
                  <span class="queue-dot" :class="section.accent" />
                </template>
              </QueueSectionLabel>
              <span v-if="section.hint" class="queue-section-hint">{{ section.hint }}</span>
            </QueueSectionTrigger>
            <QueueSectionContent>
              <QueueList class="queue-list">
                <QueueItem
                  v-for="(item, idx) in section.items"
                  :key="`${section.id}-${idx}-${item}`"
                  class="queue-item"
                >
                  <div class="queue-item-row">
                    <QueueItemIndicator class="queue-item-dot" :class="section.accent" />
                    <QueueItemContent class="queue-item-text" :class="{ 'queue-item-text--italic': section.italic }">
                      {{ item }}
                    </QueueItemContent>
                  </div>
                </QueueItem>
              </QueueList>
            </QueueSectionContent>
          </QueueSection>
        </div>
      </Queue>
    </div>
  </Transition>
</template>

<style scoped>
/* The card itself: the prototype's popover (surface, hairline, pop shadow)
   around the library's queue, which brings the sections and the rows. It is a
   column with a bounded height, so the head stays put and the list below it is
   what scrolls. */
.queue-card {
  width: 100%;
  max-width: 780px;
  max-height: 42vh;
  margin-inline: auto;
  gap: 0;
  overflow: hidden;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 8px;
  background: var(--forebrain-surface);
  box-shadow: var(--forebrain-shadow-pop);
}

/* Long content scrolls, it is never cut (the house rule): min-height:0 is what
   lets a flex child shrink below its content so the scrollbar can appear, and
   the right padding keeps a row's text off the scrollbar. */
.queue-body {
  min-height: 0;
  overflow-y: auto;
  padding-right: 2px;
}

.queue-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  border-bottom: 1px solid var(--forebrain-divider);
  padding: 8px 12px;
}

.queue-head-mark {
  display: flex;
  width: 28px;
  height: 28px;
  flex: none;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--forebrain-brand-border);
  border-radius: 8px;
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

.queue-head-title {
  font-size: 14px;
  font-weight: 600;
  line-height: 20px;
  color: var(--forebrain-text);
}

.queue-head-sub {
  font-size: 11px;
  line-height: 16px;
  color: var(--forebrain-muted-text);
}

.queue-act {
  display: inline-flex;
  height: 28px;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
  padding: 0 8px;
  font-size: 11px;
  font-weight: 500;
  color: var(--forebrain-text-2);
  cursor: pointer;
}

.queue-act:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}

.queue-act--brand {
  border-color: var(--forebrain-brand-border);
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

.queue-section {
  padding: 0 12px;
}

.queue-section-trigger {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px;
  padding: 6px 0;
  background: transparent;
  font-size: 12px;
  color: var(--forebrain-text-2);
}

.queue-section-trigger:hover {
  background: transparent;
  color: var(--forebrain-text-2);
}

.queue-section-label {
  gap: 6px;
  font-size: 12px;
  font-weight: 500;
}

.queue-section-hint {
  font-size: 11px;
  font-weight: 400;
  color: var(--forebrain-muted-text);
}

.queue-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 999px;
}

.queue-list {
  margin-top: 0;
  padding: 0;
}

.queue-item {
  padding: 0;
  border-radius: 6px;
}

.queue-item:hover {
  background: var(--forebrain-button-alt-bg);
}

.queue-item-row {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 8px;
  padding: 4px 6px;
}

.queue-item-dot {
  margin-top: 6px;
  border: 0;
}

.queue-item-text {
  font-size: 12px;
  line-height: 20px;
  color: var(--forebrain-muted-text);
  display: block;
  overflow: hidden;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.queue-item-text--italic {
  font-style: italic;
}
</style>
