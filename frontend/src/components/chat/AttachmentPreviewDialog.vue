<template>
  <!-- A look at one attachment without leaving the conversation: a screenshot
       drawn whole, a PDF in the browser's own reader, and for any other type
       the one sentence that says why there is nothing to look at, with the
       download beside it. -->
  <ForebrainModal open size="xl" :title="name" @close="emit('close')">
    <div class="att-pv" data-testid="attachment-preview-dialog">
      <img v-if="kind === 'image'" :src="inlineUrl" :alt="name" class="att-pv-img" />
      <iframe v-else-if="kind === 'pdf'" :src="inlineUrl" :title="name" class="att-pv-pdf" />
      <p v-else class="att-pv-none">{{ t('chat.attachmentNoPreview') }}</p>
    </div>
    <template #footer>
      <a :href="url" :download="name" class="forebrain-btn forebrain-btn-primary inline-flex items-center gap-1.5" data-testid="attachment-preview-download">
        <Download class="size-3.5" aria-hidden="true" />{{ t('chat.attachmentDownload') }}
      </a>
    </template>
  </ForebrainModal>
</template>

<script lang="ts">
export type AttachmentKind = 'image' | 'pdf' | 'file'
</script>

<script setup lang="ts">
import { Download } from 'lucide-vue-next'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { useI18n } from '@/locales'

defineProps<{
  name: string
  kind: AttachmentKind
  /** The download, and the same bytes asked to be drawn in place. */
  url: string
  inlineUrl: string
}>()

const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
</script>

<style scoped>
.att-pv {
  display: grid;
  place-items: center;
  min-height: 160px;
}

.att-pv-img {
  max-width: 100%;
  max-height: 62vh;
  object-fit: contain;
  border-radius: 6px;
}

.att-pv-pdf {
  width: 100%;
  height: 62vh;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
}

.att-pv-none {
  font-size: 12.5px;
  color: var(--forebrain-muted-text);
}
</style>
