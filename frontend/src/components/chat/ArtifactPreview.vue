<template>
  <!-- A delivered file, opened in the drawer's own tab (plan 153 A8/B4). The
       engine already said how it may be drawn (`preview`); this is the one
       place that decision is carried out, and the two that touch live content
       — HTML and SVG — are drawn inside a sandbox that can never reach this
       page's origin. -->
  <div class="artifact-preview" data-testid="artifact-preview">
    <!-- Nothing to draw: no preview kind, no LibreOffice, or a file too large
         to hand to a renderer. Each says which, and offers the download. -->
    <div v-if="noticeText" class="art-face art-none" data-testid="artifact-none">
      <component :is="noticeText.icon" aria-hidden="true" />
      <b>{{ noticeText.title }}</b>
      <span>{{ noticeText.body }}</span>
      <a v-if="artifact.downloadUrl" class="btn-download" :href="artifact.downloadUrl" download data-testid="artifact-none-download">
        <Download aria-hidden="true" />{{ t('chat.artifactDownloadPlain') }}
      </a>
    </div>
    <p v-else-if="busy && preview !== 'docx'" class="art-state" data-testid="artifact-loading">{{ t('chat.artifactLoading') }}</p>

    <img
      v-else-if="preview === 'image'"
      class="art-shot"
      :src="artifact.contentUrl"
      :alt="artifact.name"
      data-testid="artifact-image"
    />
    <video
      v-else-if="preview === 'video'"
      class="art-player"
      controls
      :src="artifact.contentUrl"
      data-testid="artifact-video"
    />
    <audio
      v-else-if="preview === 'audio'"
      class="art-audio"
      controls
      :src="artifact.contentUrl"
      data-testid="artifact-audio"
    />
    <iframe
      v-else-if="preview === 'pdf'"
      class="art-frame"
      :src="artifact.contentUrl"
      :title="artifact.name"
      data-testid="artifact-pdf"
    />
    <!-- HTML runs the author's own scripts: an opaque origin with scripts and
         popups, and never same-origin access, which would hand it this page's
         cookies and API. -->
    <iframe
      v-else-if="preview === 'html'"
      class="art-frame"
      :src="artifact.contentUrl"
      sandbox="allow-scripts allow-popups"
      :title="artifact.name"
      data-testid="artifact-html"
    />
    <div v-else-if="preview === 'markdown'" class="art-face art-doc" data-testid="artifact-markdown">
      <MessageResponse :content="text" />
    </div>
    <div v-else-if="preview === 'text'" class="art-face art-code" data-testid="artifact-text">
      <ReadonlyCode :path="artifact.name" :text="text" />
    </div>
    <div v-else-if="preview === 'docx'" ref="docxEl" class="art-face art-docx" data-testid="artifact-docx">
      <p v-if="busy" class="art-state">{{ t('chat.artifactLoading') }}</p>
    </div>
    <div v-else-if="preview === 'sheet'" class="art-sheet" data-testid="artifact-sheet">
      <ScrollArea class="art-sheet-area">
        <table class="tb">
          <thead>
            <tr>
              <th v-for="(_, col) in sheetColumns" :key="`h-${col}`" class="r">{{ sheetHeader(col) }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, index) in sheetRows" :key="`r-${index}`">
              <td v-for="(_, col) in sheetColumns" :key="`c-${index}-${col}`">{{ row[col] ?? '' }}</td>
            </tr>
          </tbody>
        </table>
      </ScrollArea>
      <div class="art-sheet-foot">
        <button
          v-for="(name, index) in sheetNames"
          :key="name"
          type="button"
          class="sheet-tab"
          :aria-pressed="index === activeSheet"
          :class="{ on: index === activeSheet }"
          @click="pickSheet(index)"
        >
          {{ name }}
        </button>
        <span class="grow" />
        <span class="hint" data-testid="artifact-sheet-note">{{ sheetNote }}</span>
      </div>
    </div>
    <iframe
      v-else-if="preview === 'office_pdf'"
      class="art-frame"
      :src="artifact.previewUrl"
      :title="artifact.name"
      data-testid="artifact-office-pdf"
    />
    <div v-else-if="preview === 'archive'" class="art-sheet" data-testid="artifact-archive">
      <ScrollArea class="art-sheet-area">
        <table class="tb">
          <thead>
            <tr>
              <th>{{ t('chat.artifactEntryName') }}</th>
              <th class="r">{{ t('chat.artifactEntrySize') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry in archivePage" :key="entry.name">
              <td>
                <Folder v-if="entry.dir" class="entry-icon" aria-hidden="true" />
                {{ entry.name }}
              </td>
              <td class="r">{{ entry.dir ? '—' : humanSize(entry.size) }}</td>
            </tr>
          </tbody>
        </table>
      </ScrollArea>
      <div class="art-sheet-foot">
        <button type="button" class="sheet-tab" :disabled="archiveOffset === 0" @click="pageArchive(-1)">
          {{ t('list.prev') }}
        </button>
        <button
          type="button"
          class="sheet-tab"
          :disabled="archiveOffset + ARCHIVE_PAGE >= archiveEntries.length"
          @click="pageArchive(1)"
        >
          {{ t('list.next') }}
        </button>
        <span class="grow" />
        <span class="hint" data-testid="artifact-archive-note">
          {{ archiveNote }}
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One delivered file's preview, dispatched on the engine's `preview` value
 * (plan 153 B4). Everything a browser can draw natively is handed to the
 * browser; the two formats that need a library (docx, xlsx) load it on demand,
 * so a conversation that only ever shows a PDF never pays for them.
 *
 * The sandbox is the rule that matters here: HTML runs in an opaque origin
 * with scripts and popups, and never same-origin access — the gateway stamps
 * its own CSP on the same bytes (pkg/gateway/session_files.go), so the two
 * layers stand independently. (pkg/gateway/session_files_test.go and
 * ArtifactPreview.test.ts each pin the missing token.)
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { CircleAlert, Download, FileWarning, Folder, ServerOff } from 'lucide-vue-next'
import { ScrollArea } from '@repo/shadcn-vue/components/ui/scroll-area'
import { MessageResponse } from '@repo/elements/message'
import { useI18n } from '@/locales'
import { forebrainApi, getErrorMessage, type ArtifactEntry } from '@/lib/api'
import {
  artifactKindLabelKey,
  artifactPreview,
  humanSize,
  type ArtifactView,
} from '@/lib/artifacts'
import ReadonlyCode from '@/components/editor/ReadonlyCode.vue'

/** A docx is unzipped and laid out in the page; past this it is a download. */
const DOCX_MAX_BYTES = 50 * 1024 * 1024
/** A sheet is a view, not the whole book: this much fits a drawer. */
const SHEET_MAX_ROWS = 2000
const SHEET_MAX_COLS = 100
/** An archive's listing pages like the prototype's: eight entries at a time. */
const ARCHIVE_PAGE = 8

const props = defineProps<{
  artifact: ArtifactView
}>()

const { t } = useI18n()

const preview = computed(() => artifactPreview(props.artifact.preview))
const kindLabel = computed(() => t(artifactKindLabelKey(props.artifact.kind)))

const error = ref('')
const loading = ref(false)
const text = ref('')
const sheetNames = ref<string[]>([])
const activeSheet = ref(0)
const sheetRows = ref<string[][]>([])
const sheetColumnCount = ref(0)
const sheetTruncated = ref(false)
const archiveEntries = ref<ArtifactEntry[]>([])
const archiveTruncated = ref(false)
const archiveOffset = ref(0)
const officeState = ref<'ready' | 'libreoffice_unavailable'>('ready')
/** The workbook is huge and never reactive: only the rows being shown are. */
let workbook: { SheetNames: string[]; Sheets: Record<string, unknown> } | null = null
/** The SheetJS read-off, kept beside the workbook it was loaded with. */
let toRows: ((sheet: unknown, opts: Record<string, unknown>) => unknown[][]) | null = null
const docxEl = ref<HTMLElement | null>(null)

const tooLarge = computed(() => props.artifact.preview === 'docx' && props.artifact.size > DOCX_MAX_BYTES)

/**
 * noticeText is the one block the states share: nothing to draw, and why.
 * `busy` is not among them — a preview that is still reading says so.
 */
const noticeText = computed(() => {
  if (preview.value === '') {
    return {
      icon: Download,
      title: t('chat.artifactUnsupportedTitle'),
      body: t('chat.artifactUnsupportedBody', { kind: kindLabel.value }),
    }
  }
  if (preview.value === 'office_pdf' && officeState.value === 'libreoffice_unavailable') {
    return { icon: ServerOff, title: t('chat.artifactLibreTitle'), body: t('chat.artifactLibreBody') }
  }
  if (tooLarge.value) {
    return { icon: FileWarning, title: t('chat.artifactTooLargeTitle'), body: t('chat.artifactTooLargeBody') }
  }
  if (error.value) {
    return { icon: CircleAlert, title: t('chat.artifactPreviewFailed'), body: error.value }
  }
  return null
})

const busy = computed(() => loading.value && !noticeText.value)

/** The columns a sheet draws: its widest row, capped. */
const sheetColumns = computed(() => Array.from({ length: sheetColumnCount.value }, (_, index) => index))

/** The sheet's own column letters (A, B, …), the way a spreadsheet names them. */
function sheetHeader(col: number): string {
  let name = ''
  let n = col
  do {
    name = String.fromCharCode(65 + (n % 26)) + name
    n = Math.floor(n / 26) - 1
  } while (n >= 0)
  return name
}

const sheetNote = computed(() => {
  const sheets = t('chat.artifactSheetCount', { count: sheetNames.value.length })
  return sheetTruncated.value ? `${sheets} · ${t('chat.artifactSheetTruncated')}` : sheets
})

const archivePage = computed(() => archiveEntries.value.slice(archiveOffset.value, archiveOffset.value + ARCHIVE_PAGE))

const archiveNote = computed(() => {
  const total = t('chat.artifactEntryCount', { count: archiveEntries.value.length })
  return archiveTruncated.value ? `${total} · ${t('chat.artifactEntriesTruncated')}` : total
})

function pageArchive(direction: number) {
  const next = archiveOffset.value + direction * ARCHIVE_PAGE
  archiveOffset.value = Math.max(0, Math.min(next, Math.max(0, archiveEntries.value.length - ARCHIVE_PAGE)))
}

/** A sheet's cells as plain strings: what a table draws, nothing inferred. */
function pickSheet(index: number) {
  const book = workbook
  const sheet = book?.Sheets[book.SheetNames[index] ?? '']
  if (!book || !toRows) return
  activeSheet.value = index
  const rows = sheet ? toRows(sheet, { header: 1, blankrows: false, defval: '' }) : []
  const limited = rows.slice(0, SHEET_MAX_ROWS)
  const columns = limited.reduce((max, row) => Math.max(max, row.length), 0)
  sheetColumnCount.value = Math.min(columns, SHEET_MAX_COLS)
  sheetRows.value = limited.map((row) => row.slice(0, SHEET_MAX_COLS).map((cell) => (cell == null ? '' : String(cell))))
  sheetTruncated.value = rows.length > SHEET_MAX_ROWS || columns > SHEET_MAX_COLS
}

async function renderDocx(buffer: ArrayBuffer) {
  const el = docxEl.value
  if (!el) return
  const { renderAsync } = await import('docx-preview')
  el.innerHTML = ''
  await renderAsync(new Uint8Array(buffer), el, el, { className: 'docx', inWrapper: true })
}

async function renderSheet(buffer: ArrayBuffer) {
  // Loaded on demand: a conversation that never opens a spreadsheet never pays
  // for SheetJS. The official tarball is the source (see NOTICE) — the npm
  // `xlsx` package stops at 0.18.5.
  const xlsx = await import('xlsx')
  const book = xlsx.read(new Uint8Array(buffer), { type: 'array' })
  workbook = book as unknown as NonNullable<typeof workbook>
  toRows = xlsx.utils.sheet_to_json as unknown as NonNullable<typeof toRows>
  sheetNames.value = [...book.SheetNames]
  pickSheet(0)
}

async function load() {
  const artifact = props.artifact
  const kind = preview.value
  error.value = ''
  text.value = ''
  workbook = null
  toRows = null
  sheetNames.value = []
  sheetRows.value = []
  sheetColumnCount.value = 0
  sheetTruncated.value = false
  archiveEntries.value = []
  archiveTruncated.value = false
  archiveOffset.value = 0
  officeState.value = 'ready'
  if (kind === '') return
  if (kind === 'docx' && tooLarge.value) return
  if (!artifact.sessionId || !artifact.id) return
  loading.value = true
  try {
    if (kind === 'markdown' || kind === 'text' || kind === 'sheet') {
      const buffer = await forebrainApi.getArtifactContent(artifact.sessionId, artifact.id)
      if (kind === 'sheet') {
        await renderSheet(buffer)
      } else {
        text.value = new TextDecoder('utf-8').decode(buffer)
      }
    } else if (kind === 'docx') {
      // docx-preview lays the document out in its own container, which only
      // exists once the page is no longer saying it is reading.
      const buffer = await forebrainApi.getArtifactContent(artifact.sessionId, artifact.id)
      loading.value = false
      await nextTick()
      await renderDocx(buffer)
    } else if (kind === 'office_pdf') {
      officeState.value = await forebrainApi.getArtifactPreviewState(artifact.sessionId, artifact.id)
    } else if (kind === 'archive') {
      const res = await forebrainApi.getArtifactEntries(artifact.sessionId, artifact.id)
      archiveEntries.value = Array.isArray(res.entries) ? res.entries : []
      archiveTruncated.value = res.truncated === true
    }
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

watch(() => [props.artifact.sessionId, props.artifact.id, props.artifact.preview], () => {
  void load()
}, { immediate: true })

onBeforeUnmount(() => {
  if (docxEl.value) docxEl.value.innerHTML = ''
})
</script>

<style scoped>
.artifact-preview {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  padding: 10px 12px 12px;
}

.art-face {
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-surface);
  overflow: hidden;
}

.art-state {
  margin: 4px 2px;
  font: 12.5px/1.6 var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.art-shot {
  display: block;
  width: 100%;
  max-height: 60vh;
  object-fit: contain;
  background: var(--forebrain-bg-alt);
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
}

.art-player,
.art-audio {
  width: 100%;
  border-radius: 8px;
}

.art-player {
  background: #1c1c1f;
}

.art-frame {
  flex: 1;
  width: 100%;
  min-height: 60vh;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-surface);
}

.art-doc {
  padding: 18px 20px;
  font-size: 13px;
  line-height: 1.65;
  color: var(--forebrain-text);
  overflow-y: auto;
  min-height: 0;
}

.art-code {
  padding: 10px 12px;
  overflow: auto;
  min-height: 0;
}

.art-docx {
  padding: 14px 16px;
  overflow-y: auto;
  min-height: 0;
  font-size: 13px;
}

.art-sheet {
  display: flex;
  flex-direction: column;
  min-height: 0;
  flex: 1;
  gap: 8px;
}

.art-sheet-area {
  flex: 1;
  min-height: 0;
  overflow: auto;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
}

.tb {
  width: 100%;
  border-collapse: collapse;
  font: 12px/1.6 var(--forebrain-ui-font);
}

.tb th,
.tb td {
  padding: 5px 8px;
  border-bottom: 1px solid var(--forebrain-divider);
  text-align: left;
  white-space: nowrap;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.tb th {
  position: sticky;
  top: 0;
  background: var(--forebrain-bg-alt);
  font-weight: 600;
  color: var(--forebrain-text-2);
}

.tb .r {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.entry-icon {
  width: 13px;
  height: 13px;
  vertical-align: -1px;
  color: var(--forebrain-muted-text);
}

.art-sheet-foot {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 8px;
}

.art-sheet-foot .grow {
  flex: 1;
}

.sheet-tab {
  border: 1px solid transparent;
  border-radius: 6px;
  padding: 3px 8px;
  background: transparent;
  color: var(--forebrain-text-2);
  font: 12px/1.4 var(--forebrain-ui-font);
  cursor: pointer;
}

.sheet-tab.on {
  border-color: var(--forebrain-brand-border);
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

.sheet-tab:disabled {
  opacity: 0.45;
  cursor: default;
}

.hint {
  font: 11.5px/1.4 var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.art-none {
  display: grid;
  justify-items: center;
  gap: 8px;
  padding: 34px 16px;
  text-align: center;
  color: var(--forebrain-muted-text);
  font: 12.5px/1.6 var(--forebrain-ui-font);
}

.art-none :deep(svg) {
  width: 22px;
  height: 22px;
}

.art-none b {
  font: 600 13px/1.5 var(--forebrain-ui-font);
  color: var(--forebrain-text);
}

.btn-download {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  color: var(--forebrain-text-2);
  font: 12px/1.4 var(--forebrain-ui-font);
  text-decoration: none;
}

.btn-download:hover {
  border-color: var(--forebrain-brand-border-strong);
  color: var(--forebrain-brand-1);
}

.btn-download :deep(svg) {
  width: 14px;
  height: 14px;
}
</style>
