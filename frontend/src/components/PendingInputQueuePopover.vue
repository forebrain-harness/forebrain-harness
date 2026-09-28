<script setup lang="ts">
import { computed } from 'vue'
import { ArrowUp, CornerDownRight, ListChecks, PencilLine, Zap } from 'lucide-vue-next'
import type { PendingInputPreview } from '@/composables/useChatStream'

const props = defineProps<{
  preview: PendingInputPreview
}>()

const emit = defineEmits<{
  (e: 'edit-last-queued'): void
  (e: 'interrupt-run'): void
}>()

const totalCount = computed(() => {
  return props.preview.pendingSteers.length +
    props.preview.rejectedSteers.length +
    props.preview.queuedMessages.length
})

const visible = computed(() => totalCount.value > 0)

type QueueSection = {
  id: string
  title: string
  hint?: string
  items: string[]
  italic?: boolean
  accent: string
}

const sections = computed<QueueSection[]>(() => [
  {
    id: 'pending',
    title: 'Messages to be submitted after next tool call',
    hint: '(press esc to interrupt and send immediately)',
    items: props.preview.pendingSteers,
    accent: 'bg-[var(--forebrain-brand-1)]',
  },
  {
    id: 'rejected',
    title: 'Messages to be submitted at end of turn',
    items: props.preview.rejectedSteers,
    accent: 'bg-amber-500',
  },
  {
    id: 'queued',
    title: 'Queued follow-up inputs',
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
    <div
      v-if="visible"
      class="pointer-events-none absolute inset-x-0 bottom-full z-30 mb-3 px-1"
    >
      <div
        class="pointer-events-auto mx-auto max-h-[42vh] w-full max-w-[780px] overflow-hidden rounded-lg border border-[var(--forebrain-divider-strong)] bg-[var(--forebrain-popover-bg)] shadow-[0_24px_70px_rgba(9,52,74,0.18)] backdrop-blur-xl dark:shadow-[0_24px_70px_rgba(0,0,0,0.34)]"
      >
        <div class="flex items-center justify-between gap-3 border-b border-[var(--forebrain-divider)] px-3 py-2">
          <div class="flex min-w-0 items-center gap-2">
            <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg border border-[var(--forebrain-brand-border-strong)] bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]">
              <ListChecks class="h-4 w-4" />
            </span>
            <div class="min-w-0">
              <div class="truncate text-sm font-semibold leading-5 text-[var(--forebrain-text)]">Message queue</div>
              <div class="text-[11px] leading-4 text-[var(--forebrain-muted-text)]">{{ totalCount }} pending</div>
            </div>
          </div>
          <div class="flex shrink-0 items-center gap-1.5">
            <button
              v-if="preview.pendingSteers.length"
              type="button"
              class="inline-flex h-7 items-center gap-1.5 rounded-md border border-[var(--forebrain-brand-border-strong)] bg-[var(--forebrain-brand-soft)] px-2 text-[11px] font-medium text-[var(--forebrain-brand-1)] transition hover:bg-[var(--forebrain-button-alt-hover-bg)]"
              @click="emit('interrupt-run')"
            >
              <Zap class="h-3.5 w-3.5" />
              <span>esc</span>
            </button>
            <button
              v-if="preview.queuedMessages.length || preview.rejectedSteers.length || preview.pendingSteers.length"
              type="button"
              class="inline-flex h-7 items-center gap-1.5 rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-control)] px-2 text-[11px] font-medium text-[var(--forebrain-text-2)] transition hover:bg-[var(--forebrain-button-alt-bg)] hover:text-[var(--forebrain-text)]"
              @click="emit('edit-last-queued')"
            >
              <PencilLine class="h-3.5 w-3.5" />
              <span>⌥ +</span>
              <ArrowUp class="h-3 w-3" />
            </button>
          </div>
        </div>

        <div class="max-h-[calc(42vh-45px)] overflow-y-auto px-3 py-2">
          <div
            v-for="section in sections"
            :key="section.id"
            class="grid grid-cols-[12px_minmax(0,1fr)] gap-x-2 py-1.5"
          >
            <div class="flex justify-center pt-1.5">
              <span class="h-2 w-2 rounded-full" :class="section.accent" />
            </div>
            <div class="min-w-0 space-y-1">
              <div class="flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5 text-xs font-medium text-[var(--forebrain-text-2)]">
                <span>{{ section.title }}</span>
                <span v-if="section.hint" class="font-normal text-[var(--forebrain-muted-text)]">{{ section.hint }}</span>
              </div>
              <div class="space-y-1">
                <div
                  v-for="(item, idx) in section.items"
                  :key="`${section.id}-${idx}-${item}`"
                  class="group flex min-w-0 gap-2 rounded-md px-1.5 py-1 text-xs leading-5 text-[var(--forebrain-muted-text)] transition hover:bg-[var(--forebrain-button-alt-bg)]"
                >
                  <CornerDownRight class="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--forebrain-brand-1)] opacity-80" />
                  <span
                    class="min-w-0 overflow-hidden [display:-webkit-box] [-webkit-box-orient:vertical] [-webkit-line-clamp:2]"
                    :class="{ italic: section.italic }"
                  >{{ item }}</span>
                </div>
              </div>
            </div>
          </div>
          <button
            v-if="preview.queuedMessages.length || preview.rejectedSteers.length || preview.pendingSteers.length"
            type="button"
            class="mt-1 inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-left text-[11px] text-[var(--forebrain-muted-text)] transition hover:bg-[var(--forebrain-button-alt-bg)] hover:text-[var(--forebrain-text-2)]"
            @click="emit('edit-last-queued')"
          >
            <PencilLine class="h-3.5 w-3.5" />
            <span>⌥ + ↑ edit last queued message</span>
          </button>
        </div>
      </div>
    </div>
  </Transition>
</template>
