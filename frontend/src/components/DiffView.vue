<template>
  <div class="diff-view" data-testid="diff-view">
    <!-- summary header -->
    <div
      v-if="summary"
      class="mb-1 text-[11px] font-medium uppercase tracking-wide text-[var(--forebrain-muted-text)]"
    >
      {{ summary }}
    </div>

    <div
      v-for="(file, fileIdx) in files"
      :key="fileIdx"
      class="diff-file"
    >
      <!-- file header: the changed-file row every diff shares (plan 137) —
           colour-block icon, grey directory with the dark file name, counts. -->
      <button
        type="button"
        class="fc-h"
        :aria-expanded="expandedFiles.has(fileIdx)"
        :data-testid="`diff-file-${fileIdx}`"
        @click="toggleFile(fileIdx)"
      >
        <span class="fc-ic" :class="kindOf(file)" role="img" :aria-label="wordOf(file)">
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
        <span v-if="file.binary" class="fc-tag">binary</span>
        <span v-else-if="file.status === 'mode-only'" class="fc-tag">mode only</span>
        <span class="fc-chev" :class="{ open: expandedFiles.has(fileIdx) }" aria-hidden="true">▶</span>
      </button>

      <!-- hunk content: the one diff row component every surface draws. -->
      <DiffBlock
        v-if="expandedFiles.has(fileIdx) && !file.binary && file.hunks?.length"
        :rows="diffRowsFromLines(file.hunks)"
        :plain="String(file.status ?? '').toLowerCase() === 'added'"
      />

      <!-- binary / empty-hunk message -->
      <div
        v-else-if="expandedFiles.has(fileIdx) && file.binary"
        class="diff-fallback"
      >
        Binary file changed.
      </div>
      <div
        v-else-if="expandedFiles.has(fileIdx) && !file.hunks?.length"
        class="diff-fallback"
      >
        No line-level diff available.
      </div>
    </div>

    <!-- empty state -->
    <div
      v-if="!files.length"
      class="diff-fallback"
    >
      No changes since last commit.
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The turn diff a message ends with, drawn in the one unified style every
 * diff shares (plan 137): the same changed-file row the shell card’s files
 * use, the same DiffBlock rows beneath. The per-file fold and the small-diff
 * auto-expand stay; the old two-gutter table with its own colours is gone.
 */
import { reactive } from 'vue'
import { Minus, Pencil, Plus } from 'lucide-vue-next'
import type { DiffHunk } from '@/lib/forebrainGatewayRuntime'
import { diffRowsFromLines } from '@/lib/unifiedDiff'
import { t } from '@/locales'
import DiffBlock from '@/components/chat/DiffBlock.vue'

export interface DiffViewFile {
  path?: string
  old_path?: string
  status?: string
  added?: number
  deleted?: number
  binary?: boolean
  hunks?: DiffHunk[]
}

const props = defineProps<{
  files: DiffViewFile[]
  summary?: string
  /** Total changed lines across all files. Used to decide initial expand/collapse. */
  totalLines?: number
}>()

// Auto-expand files when the total diff is small (< 40 changed lines).
const AUTO_EXPAND_THRESHOLD = 40

// Track per-file expand state. Auto-expand all files when diff is small.
const expandedFiles = reactive<Set<number>>(
  (() => {
    const s = new Set<number>()
    if ((props.totalLines ?? 0) <= AUTO_EXPAND_THRESHOLD) {
      props.files.forEach((_, i) => s.add(i))
    }
    return s
  })(),
)

function toggleFile(fileIdx: number) {
  if (expandedFiles.has(fileIdx)) expandedFiles.delete(fileIdx)
  else expandedFiles.add(fileIdx)
}

/** The row’s kind: added is add, deleted is del, everything else updated. */
function kindOf(file: DiffViewFile): 'add' | 'upd' | 'del' {
  const status = String(file.status ?? '').trim().toLowerCase()
  if (status === 'added' || status === 'created') return 'add'
  if (status === 'deleted' || status === 'removed') return 'del'
  return 'upd'
}

function wordOf(file: DiffViewFile): string {
  const kind = kindOf(file)
  return kind === 'add' ? t('chat.fileCreated') : kind === 'del' ? t('chat.fileDeleted') : t('chat.fileUpdated')
}

/** The path’s directory half, with its slash; empty at the root. */
function dirOf(file: DiffViewFile): string {
  const path = String(file.path ?? '')
  const cut = path.lastIndexOf('/')
  return cut >= 0 ? path.slice(0, cut + 1) : ''
}

function nameOf(file: DiffViewFile): string {
  const path = String(file.path ?? '')
  const cut = path.lastIndexOf('/')
  return cut >= 0 ? path.slice(cut + 1) : path
}
</script>

<style scoped>
.diff-view {
  font-size: 12px;
}

.diff-file + .diff-file {
  border-top: 1px solid var(--forebrain-divider);
}

/* The changed-file row, the same one FileChangeList draws (the prototype’s
   .fc-h, :359-370); its styles are repeated here because this head is a
   button with a fold, which FileChangeList’s row is not. */
.fc-h {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 9px 0;
  background: transparent;
  border: 0;
  text-align: left;
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

.fc-tag {
  flex: none;
  font-size: 10px;
  color: var(--forebrain-muted-text);
}

.fc-chev {
  flex: none;
  font-size: 10px;
  color: var(--forebrain-muted-text);
  transition: transform 0.15s;
}

.fc-chev.open {
  transform: rotate(90deg);
}

.diff-fallback {
  padding: 8px 0;
  font-size: 11px;
  font-style: italic;
  color: var(--forebrain-muted-text);
}
</style>
