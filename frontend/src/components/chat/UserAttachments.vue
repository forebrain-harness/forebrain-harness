<template>
  <!-- The prototype's .u-atts: what the message attached, under its words, one
       chip each. A chip the page can open has two targets — the mark on its
       left previews the file, its name downloads it — and one it cannot name
       (nothing to address it by, or a file that is gone) is a plain label. -->
  <ul class="u-atts" :aria-label="t('chat.messageAttachments')" data-testid="message-attachments">
    <li v-for="item in items" :key="item.key">
      <span class="u-att" :class="{ 'u-att--live': item.live }" data-testid="message-attachment">
        <button
          v-if="item.live"
          type="button"
          class="u-att-mark"
          v-tip:top="t('chat.attachmentPreviewTip')"
          :aria-label="t('chat.attachmentPreview', { name: item.name })"
          data-testid="attachment-preview"
          @click="previewing = item"
        >
          <img
            v-if="item.kind === 'image'"
            :src="item.inlineUrl"
            alt=""
            class="u-att-thumb"
            loading="lazy"
            @error="broken[item.key] = true"
          />
          <FileText v-else class="u-att-ic" aria-hidden="true" />
        </button>
        <span v-else class="u-att-mark">
          <ImageIcon v-if="item.kind === 'image'" class="u-att-ic" aria-hidden="true" />
          <Paperclip v-else class="u-att-ic" aria-hidden="true" />
        </span>
        <a
          v-if="item.live"
          :href="item.url"
          :download="item.name"
          class="u-att-name"
          v-tip:top="t('chat.attachmentDownloadAria', { name: item.name })"
          data-testid="attachment-download"
        >{{ item.name }}</a>
        <span v-else class="u-att-name" v-tip:top="item.hint">{{ item.name }}</span>
      </span>
    </li>
  </ul>
  <AttachmentPreviewDialog
    v-if="previewing"
    :name="previewing.name"
    :kind="previewing.kind"
    :url="previewing.url"
    :inline-url="previewing.inlineUrl"
    @close="previewing = null"
  />
</template>

<script setup lang="ts">
/**
 * What one user message attached. An upload is opened by its file id; a file
 * the terminal pasted into the project is opened by the message that holds it
 * (the page never learns its path) — either way the page asks the gateway for
 * the bytes twice over: as a download (the name) and drawn in place (the mark,
 * which opens the preview).
 */
import { computed, reactive, ref } from 'vue'
import { FileText, Image as ImageIcon, Paperclip } from 'lucide-vue-next'
import { forebrainApi, type ChatAttachmentRecord } from '@/lib/api'
import { useI18n } from '@/locales'
import AttachmentPreviewDialog, { type AttachmentKind } from './AttachmentPreviewDialog.vue'

const props = defineProps<{
  attachments: ChatAttachmentRecord[]
  /** The conversation the message belongs to, and the message's own row: what a
   *  path-recorded attachment is addressed by. */
  sessionId?: string | null
  rowId?: number
}>()

const { t } = useI18n()

interface Item {
  key: string
  name: string
  kind: AttachmentKind
  url: string
  inlineUrl: string
  live: boolean
  hint: string
}

/** A file that failed to load (gone, or outside what the gateway may serve) stops offering itself. */
const broken = reactive<Record<string, boolean>>({})
const previewing = ref<Item | null>(null)

function kindOf(attachment: ChatAttachmentRecord): AttachmentKind {
  const mediaType = String(attachment.mediaType ?? '').toLowerCase()
  if (mediaType.startsWith('image/')) return 'image'
  if (mediaType === 'application/pdf') return 'pdf'
  const name = String(attachment.name ?? attachment.path ?? '').toLowerCase()
  if (/\.(png|jpe?g|gif|webp)$/.test(name)) return 'image'
  if (name.endsWith('.pdf')) return 'pdf'
  return 'file'
}

const items = computed<Item[]>(() => props.attachments.map((attachment, index) => {
  const where = { sessionId: props.sessionId ?? undefined, rowId: props.rowId, index }
  const url = forebrainApi.attachmentUrl(attachment, where)
  const key = `${attachment.fileId ?? attachment.path ?? attachment.name}-${index}`
  return {
    key,
    name: attachment.name || t('chat.attachment'),
    kind: kindOf(attachment),
    url,
    inlineUrl: forebrainApi.attachmentUrl(attachment, where, true),
    live: Boolean(url) && !broken[key],
    hint: attachment.path ?? attachment.name,
  }
}))
</script>

<style scoped>
.u-atts {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 6px 0 0;
  padding: 0;
  list-style: none;
}

/* The chip: a 26px row on the page surface, the mark and the name two targets. */
.u-att {
  display: inline-flex;
  align-items: stretch;
  min-height: 26px;
  max-width: 100%;
  overflow: hidden;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-text);
}

.u-att-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 26px;
  color: var(--forebrain-muted-text);
}

.u-att-ic {
  width: 16px;
  height: 16px;
}

/* A screenshot shows itself, cropped square to the mark. */
.u-att-thumb {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.u-att-name {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  padding: 0 8px 0 6px;
  overflow-wrap: anywhere;
}

.u-att--live .u-att-mark,
.u-att--live .u-att-name {
  cursor: pointer;
}

.u-att--live .u-att-mark:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}

.u-att--live .u-att-name:hover {
  background: var(--forebrain-button-alt-bg);
  text-decoration: underline;
}
</style>
