<template>
  <!-- The prototype’s changed-files section (app-preview.html :358-381): one
       row per file — a colour-block icon for what happened to it, the
       directory grey and ellipsised from its own end so the file name stays,
       the counts on the right — the diff under its row, and the engine’s
       short notes, one line each, closing the card. -->
  <div v-for="(file, i) in files" :key="`${file.path}-${i}`" class="fc" :data-testid="`file-change-${i}`">
    <div class="fc-h">
      <span
        class="fc-ic"
        :class="kindOf(file)"
        role="img"
        :aria-label="wordOf(file)"
        v-tip:top="wordOf(file)"
      >
        <Plus v-if="kindOf(file) === 'add'" aria-hidden="true" />
        <Minus v-else-if="kindOf(file) === 'del'" aria-hidden="true" />
        <Pencil v-else aria-hidden="true" />
      </span>
      <span class="fc-p">
        <span v-if="dirOf(file)" class="fc-dir"><bdi>{{ dirOf(file) }}</bdi></span>
        <span class="fc-fn">{{ nameOf(file) }}</span>
      </span>
      <span v-if="file.added || file.deleted" class="fc-n">
        <span v-if="file.added" class="na">+{{ file.added }}</span>
        <span v-if="file.deleted" class="nd">−{{ file.deleted }}</span>
      </span>
    </div>
    <DiffBlock
      v-if="file.unifiedDiff"
      :rows="parseUnifiedDiff(file.unifiedDiff)"
      :plain="kindOf(file) === 'add'"
    />
  </div>
  <div v-for="(note, i) in notes" :key="`note-${i}`" class="fc-note" :data-testid="`diff-note-${i}`">
    <component :is="diffNoteIcon(note)" aria-hidden="true" />
    <span>{{ translateDiffNote(note, lang) }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * The files a command changed and the notes that follow them (plan 130’s
 * data, drawn the way the prototype’s shFiles does). No library ships this
 * row — the directory/filename split with an rtl ellipsis on the directory
 * half is the prototype’s own layout — so it is written here, the diff body
 * itself delegated to the shared DiffBlock.
 */
import { computed } from 'vue'
import { Minus, Pencil, Plus } from 'lucide-vue-next'
import { useI18n, t } from '@/locales'
import { translateDiffNote, diffNoteIcon } from '@/locales/diffNotes'
import { parseUnifiedDiff } from '@/lib/unifiedDiff'
import type { ShellCardFile } from '@/composables/useChatStream'
import DiffBlock from './DiffBlock.vue'

withDefaults(defineProps<{
  files: ShellCardFile[]
  notes?: string[]
}>(), {
  notes: () => [],
})

const { locale } = useI18n()
const lang = computed(() => (locale.value === 'zh' ? 'zh' : 'en') as 'zh' | 'en')

/** The row’s kind: created is add, deleted is del, everything else updated. */
function kindOf(file: ShellCardFile): 'add' | 'upd' | 'del' {
  const verb = String(file.verb ?? '').trim().toLowerCase()
  if (verb === 'created') return 'add'
  if (verb === 'deleted') return 'del'
  return 'upd'
}

function wordOf(file: ShellCardFile): string {
  const kind = kindOf(file)
  return kind === 'add' ? t('chat.fileCreated') : kind === 'del' ? t('chat.fileDeleted') : t('chat.fileUpdated')
}

/** The path’s directory half, with its slash; empty at the root. */
function dirOf(file: ShellCardFile): string {
  const path = String(file.path ?? '')
  const cut = path.lastIndexOf('/')
  return cut >= 0 ? path.slice(0, cut + 1) : ''
}

function nameOf(file: ShellCardFile): string {
  const path = String(file.path ?? '')
  const cut = path.lastIndexOf('/')
  return cut >= 0 ? path.slice(cut + 1) : path
}
</script>

<style scoped>
/* The prototype’s :is(.shc, .fdiff) .fc family (:358-370, :380-381). */
.fc {
  border-top: 1px solid var(--forebrain-divider);
}

.fc-h {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 14px;
}

.fc-ic {
  flex: none;
  width: 20px;
  height: 20px;
  border-radius: 6px;
  display: grid;
  place-items: center;
}

.fc-ic :deep(svg) {
  width: 12px;
  height: 12px;
  stroke-width: 2.5;
}

.fc-ic.add {
  background: #ecfdf3;
  color: #067647;
}

.fc-ic.upd {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

.fc-ic.del {
  background: #fef3f2;
  color: #b42318;
}

.fc-p {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: baseline;
  white-space: nowrap;
  font: 13px var(--forebrain-ui-font);
}

/* The directory half ellipsises from its own end (rtl), so a long path gives
   up its outermost directories and keeps the file name. */
.fc-dir {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  direction: rtl;
  color: var(--forebrain-muted-text);
}

.fc-dir bdi {
  direction: ltr;
  unicode-bidi: isolate;
}

.fc-fn {
  flex: none;
  color: var(--forebrain-text);
  font-weight: 500;
}

.fc-n {
  flex: none;
  font: 500 12px var(--forebrain-ui-font);
  font-variant-numeric: tabular-nums;
}

.fc-n .na {
  color: #067647;
}

.fc-n .nd {
  color: #b42318;
}

.fc-n .na + .nd {
  margin-left: 8px;
}

.fc-note {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 14px;
  border-top: 1px solid var(--forebrain-divider);
  font: 12px var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.fc-note :deep(svg) {
  width: 14px;
  height: 14px;
  flex: none;
  color: #98a2b3;
}
</style>
