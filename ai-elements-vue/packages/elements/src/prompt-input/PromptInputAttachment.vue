<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import type { AttachmentFile } from './types'
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from '@repo/shadcn-vue/components/ui/hover-card'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { PaperclipIcon, XIcon } from 'lucide-vue-next'
import { computed } from 'vue'
import { usePromptInput } from './context'

const props = defineProps<{
  file: AttachmentFile
  class?: HTMLAttributes['class']
  /** What the remove button says before the file's name, in the app's own
   *  language ("Remove readme.md"); English when the caller says nothing. */
  removeLabel?: string
}>()

const { removeFile } = usePromptInput()

const filename = computed(() => props.file.filename || '')
const isImage = computed(() =>
  props.file.mediaType?.startsWith('image/') && props.file.url,
)
const label = computed(() => filename.value || (isImage.value ? 'Image' : 'Attachment'))

function handleRemove(e: Event) {
  e.stopPropagation()
  removeFile(props.file.id)
}
</script>

<template>
  <HoverCard :open-delay="0" :close-delay="0">
    <HoverCardTrigger as-child>
      <!-- The prototype's .att (:755): one 26px chip, mono 11.5px on the alt
           surface, its remove button always there — a control the reader looks
           for does not appear only once the pointer finds it. -->
      <div
        :class="cn(
          'group relative flex h-[26px] cursor-pointer select-none items-center gap-1.5 rounded-md border border-border bg-[var(--forebrain-bg-alt)] py-0 pl-2 pr-1 font-mono text-[11.5px] text-[var(--forebrain-text-2)] transition-colors hover:border-[var(--forebrain-divider-strong)] hover:text-[var(--forebrain-text)]',
          props.class,
        )"
      >
        <div class="relative flex size-[13px] shrink-0 items-center justify-center overflow-hidden rounded-sm">
          <img
            v-if="isImage"
            :src="file.url"
            :alt="label"
            class="size-[13px] object-cover"
          >
          <PaperclipIcon v-else class="size-[13px] text-muted-foreground" />
        </div>

        <span class="max-w-[150px] truncate">{{ label }}</span>

        <button
          type="button"
          class="inline-flex size-4 shrink-0 cursor-pointer items-center justify-center rounded border-0 bg-transparent p-0 text-muted-foreground hover:text-[var(--forebrain-text)]"
          :aria-label="props.removeLabel ? `${props.removeLabel} ${label}` : `Remove ${label}`"
          @click="handleRemove"
        >
          <XIcon class="size-3" />
        </button>
      </div>
    </HoverCardTrigger>

    <HoverCardContent class="w-auto p-2" align="start">
      <div class="w-auto space-y-3">
        <div v-if="isImage" class="flex max-h-96 w-96 items-center justify-center overflow-hidden rounded-md border">
          <img
            :src="file.url"
            :alt="label"
            class="max-h-full max-w-full object-contain"
          >
        </div>
        <div class="flex items-center gap-2.5">
          <div class="min-w-0 flex-1 space-y-1 px-0.5">
            <h4 class="truncate font-semibold text-sm leading-none">
              {{ label }}
            </h4>
            <p v-if="file.mediaType" class="truncate font-mono text-muted-foreground text-xs">
              {{ file.mediaType }}
            </p>
          </div>
        </div>
      </div>
    </HoverCardContent>
  </HoverCard>
</template>
