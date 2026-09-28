<template>
  <div class="diff-view text-[12px] font-mono leading-5">
    <!-- summary header -->
    <div
      v-if="summary"
      class="mb-1 text-[11px] font-medium uppercase tracking-wide text-[var(--forebrain-muted-text)]"
    >
      {{ summary }}
    </div>

    <div
      v-for="(file, fileIdx) in views"
      :key="fileIdx"
      class="mb-2 overflow-hidden rounded-lg border border-[var(--forebrain-divider)]"
    >
      <!-- file header -->
      <button
        type="button"
        class="flex w-full items-center gap-2 bg-[var(--forebrain-bg-alt)] px-3 py-1.5 text-left text-[11px] font-semibold hover:bg-[var(--forebrain-bg-alt-soft)] focus:outline-none"
        :aria-expanded="expandedFiles.has(fileIdx)"
        @click="toggleFile(fileIdx)"
      >
        <span class="text-[var(--forebrain-muted-text)] transition-transform" :class="expandedFiles.has(fileIdx) ? 'rotate-90' : ''">▶</span>
        <span class="flex-1 truncate text-[var(--forebrain-text)]">{{ displayPath(file) }}</span>
        <span class="shrink-0 text-[var(--forebrain-muted-text)]">
          <span class="diff-add-label">+{{ file.added ?? 0 }}</span>
          <span class="mx-0.5 opacity-50">−</span>
          <span class="diff-del-label">{{ file.deleted ?? 0 }}</span>
        </span>
        <span v-if="file.binary" class="ml-1 rounded bg-[var(--forebrain-bg-alt)] px-1 text-[10px] text-[var(--forebrain-muted-text)]">binary</span>
        <span v-else-if="file.status === 'mode-only'" class="ml-1 text-[10px] text-[var(--forebrain-muted-text)]">mode only</span>
      </button>

      <!-- hunk content -->
      <div v-if="expandedFiles.has(fileIdx) && !file.binary && file.hunks?.length" class="overflow-x-auto">
        <table class="w-full border-collapse">
          <tbody>
            <template v-for="(hunk, hunkIdx) in file.hunks" :key="hunkIdx">
              <!-- hunk separator -->
              <tr v-if="hunkIdx > 0">
                <td colspan="3" class="h-px bg-[var(--forebrain-divider)]" />
              </tr>
              <tr
                v-for="(line, lineIdx) in hunk.lines"
                :key="lineIdx"
                :class="lineRowClass(line.kind)"
              >
                <!-- old line number — user-select:none -->
                <td
                  class="diff-gutter select-none py-0 pr-2 text-right align-top text-[var(--forebrain-muted-text)] opacity-60"
                  aria-hidden="true"
                >{{ line.old_no || '' }}</td>
                <!-- new line number — user-select:none -->
                <td
                  class="diff-gutter select-none py-0 pr-2 text-right align-top text-[var(--forebrain-muted-text)] opacity-60"
                  aria-hidden="true"
                >{{ line.new_no || '' }}</td>
                <!-- marker — user-select:none -->
                <td
                  class="diff-marker select-none py-0 px-1 align-top font-bold"
                  :class="lineMarkerClass(line.kind)"
                  aria-hidden="true"
                >{{ lineMarker(line.kind) }}</td>
                <!-- code — selectable, copies clean -->
                <td class="diff-code w-full whitespace-pre py-0 pl-1 pr-2 align-top">
                  <template v-if="line.tokens">
                    <span
                      v-for="(token, tokenIdx) in line.tokens"
                      :key="tokenIdx"
                      :style="token.htmlStyle"
                    >{{ token.content }}</span>
                  </template>
                  <template v-else>{{ line.text }}</template>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>

      <!-- binary / empty-hunk message -->
      <div
        v-else-if="expandedFiles.has(fileIdx) && file.binary"
        class="px-3 py-2 text-[11px] italic text-[var(--forebrain-muted-text)]"
      >
        Binary file changed.
      </div>
      <div
        v-else-if="expandedFiles.has(fileIdx) && !file.hunks?.length"
        class="px-3 py-2 text-[11px] italic text-[var(--forebrain-muted-text)]"
      >
        No line-level diff available.
      </div>
    </div>

    <!-- empty state -->
    <div
      v-if="!files.length"
      class="py-2 text-[11px] italic text-[var(--forebrain-muted-text)]"
    >
      No changes since last commit.
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, shallowRef, watch } from 'vue'
import { highlightCode, languageForPath } from '@repo/elements/code-block'
import type { HighlightLanguage, TokenizedCode } from '@repo/elements/code-block'
import type { DiffHunk } from '@/lib/forebrainGatewayRuntime'

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

type CodeToken = TokenizedCode['tokens'][number][number]

interface HighlightTarget {
  key: string
  language: HighlightLanguage
  code: string
}

/**
 * Diff rows are syntax-highlighted with the same Monokai pair as every other
 * code surface. Shiki tokenises a file's rows as one document rather than line
 * by line, so a string or a comment that runs across rows keeps its colour;
 * the rows are joined in render order, which makes the token row at index i
 * the diff row at index i.
 */
const tokensByKey = shallowRef(new Map<string, TokenizedCode['tokens']>())

function highlightTarget(file: DiffViewFile): HighlightTarget | null {
  if (file.binary || !file.hunks?.length) {
    return null
  }
  const code = file.hunks.flatMap(hunk => hunk.lines.map(line => line.text)).join('\n')
  if (!code.trim()) {
    return null
  }
  const language = languageForPath(file.path)
  return { key: `${language}\u0000${code}`, language, code }
}

function rememberTokens(key: string, tokens: TokenizedCode['tokens']) {
  const next = new Map(tokensByKey.value)
  next.set(key, tokens)
  tokensByKey.value = next
}

/** One target per file, rebuilt only when the diff itself changes. */
const targets = computed(() => props.files.map(highlightTarget))

watch(
  targets,
  (list) => {
    for (const target of list) {
      if (!target || tokensByKey.value.has(target.key)) {
        continue
      }
      const ready = highlightCode(target.code, target.language, result =>
        rememberTokens(target.key, result.tokens))
      if (ready) {
        rememberTokens(target.key, ready.tokens)
      }
    }
  },
  { immediate: true },
)

/**
 * The files as rendered: every row carries the tokens for its own text, or
 * none while Shiki is still loading, in which case the row shows plain text
 * and gains its colours on the frame after.
 */
const views = computed(() =>
  props.files.map((file, fileIdx) => {
    const target = targets.value[fileIdx]
    const tokens = target ? tokensByKey.value.get(target.key) : undefined
    let row = 0
    return {
      ...file,
      hunks: (file.hunks ?? []).map(hunk => ({
        ...hunk,
        lines: hunk.lines.map(line => ({
          ...line,
          tokens: tokens?.[row++] as CodeToken[] | undefined,
        })),
      })),
    }
  }),
)

function toggleFile(idx: number) {
  if (expandedFiles.has(idx)) {
    expandedFiles.delete(idx)
  } else {
    expandedFiles.add(idx)
  }
}

function displayPath(file: DiffViewFile): string {
  if (file.old_path && file.old_path !== file.path) {
    return `${file.old_path} → ${file.path}`
  }
  return file.path ?? ''
}

function lineRowClass(kind: string): string {
  switch (kind) {
    case 'add': return 'diff-row-add'
    case 'del': return 'diff-row-del'
    default: return 'diff-row-ctx'
  }
}

function lineMarkerClass(kind: string): string {
  switch (kind) {
    case 'add': return 'diff-marker-add'
    case 'del': return 'diff-marker-del'
    default: return 'opacity-0'
  }
}

function lineMarker(kind: string): string {
  switch (kind) {
    case 'add': return '+'
    case 'del': return '−'
    default: return ' '
  }
}
</script>

<style scoped>
/* Gutter columns: fixed-width, non-selectable via user-select:none */
.diff-gutter {
  min-width: 2.8em;
  user-select: none;
  -webkit-user-select: none;
}

/* Marker column: non-selectable */
.diff-marker {
  width: 1.2em;
  user-select: none;
  -webkit-user-select: none;
}

/* Light theme row tints */
.diff-row-add {
  background-color: #dcfce7; /* green-100 */
}
.diff-row-del {
  background-color: #fee2e2; /* red-100 */
}
.diff-row-ctx {
  background-color: transparent;
}
.diff-marker-add { color: #16a34a; } /* green-600 */
.diff-marker-del { color: #dc2626; } /* red-600 */
.diff-add-label { color: #16a34a; }
.diff-del-label { color: #dc2626; }

/* Shiki emits each token's light colour plus a --shiki-dark custom property,
   so the dark palette is one variable swap rather than a second render. */
:global(.dark) .diff-code span {
  color: var(--shiki-dark);
}

/* Dark theme row tints */
:global(.dark) .diff-row-add {
  background-color: #14532d; /* green-900 */
}
:global(.dark) .diff-row-del {
  background-color: #7f1d1d; /* red-900 */
}
:global(.dark) .diff-marker-add { color: #86efac; } /* green-300 */
:global(.dark) .diff-marker-del { color: #fca5a5; } /* red-300 */
:global(.dark) .diff-add-label { color: #86efac; }
:global(.dark) .diff-del-label { color: #fca5a5; }
</style>
