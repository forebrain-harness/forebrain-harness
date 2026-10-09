<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { PromptInputAttachment, usePromptInput } from '@repo/elements/prompt-input'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { Bot, ChevronLeft, ChevronRight, FileText, Folder, Image as ImageIcon, Paperclip, Slash, X } from 'lucide-vue-next'
import { computed, nextTick, ref, watch } from 'vue'
import { forebrainApi, type MentionCandidate, type SlashCommandRecord } from '@/lib/api'
import { mergeSubmissions, type ComposerSubmission, type SubmittedAttachment } from '@/lib/composerSubmission'
import { useI18n } from '@/locales'

export type BotOption = {
  id: string
  label: string
  mention: string
  description: string | null
  status?: string | null
}

type MentionMenuLevel = 'root' | 'bots' | 'files'

// Root menu rows, in display order. ArrowUp/Down move between them and
// ArrowRight/Enter/Tab opens the highlighted one.
const rootMenuRows: MentionMenuLevel[] = ['bots', 'files']

/**
 * A menu row reached by the keyboard (data-highlighted) or the pointer takes
 * the accent ground together with its own ink: the accent ground is dark in
 * the light theme, and the popover's ink on it would be unreadable.
 */
const menuRowClass =
  'group outline-none transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:bg-accent focus-visible:text-accent-foreground data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground'
/** Secondary text and icons in a menu row, which follow the row onto the accent ground. */
const menuRowMutedClass =
  'text-muted-foreground group-hover:text-accent-foreground/80 group-focus-visible:text-accent-foreground/80 group-data-[highlighted]:text-accent-foreground/80'

const props = defineProps<{
  placeholder?: string
  class?: HTMLAttributes['class']
  bots?: BotOption[]
  botsFetchDone?: boolean
  duringRun?: boolean
  /**
   * The composer's queue preview shows a message. Recalling the newest one
   * (Shift+← or Alt+↑) works whenever one is shown: a subagent's view keeps
   * its own queue while the conversation is idle, and a page reloaded mid-run
   * shows the run's queue without having started it.
   */
  queueVisible?: boolean
  sideConversation?: boolean
}>()

const emit = defineEmits<{
  (e: 'queue-follow-up'): void
  (e: 'edit-last-queued'): void
  (e: 'interrupt-run'): void
  // The reader typed, pasted or deleted in the composer (never a programmatic
  // restore), and Escape with no run to interrupt: both stop a continuation
  // waiting on a usage limit, as they do in the terminal.
  (e: 'typed'): void
  (e: 'escape'): void
}>()

const { textInput, setTextInput, submitForm, addFiles, clearFiles, files, removeFile } = usePromptInput()
const { t } = useI18n()
const textareaRef = ref<HTMLTextAreaElement | null>(null)
const botsListRef = ref<HTMLDivElement | null>(null)
const isComposing = ref(false)
const mentionOpen = ref(false)
const mentionMenuLevel = ref<MentionMenuLevel>('root')
const mentionStart = ref(0)
const mentionQuery = ref('')
const highlightIndex = ref(0)
const slashOpen = ref(false)
const slashQuery = ref('')
const slashCommands = ref<SlashCommandRecord[]>([])
const slashHighlightIndex = ref(0)
const slashLoading = ref(false)
const slashListRef = ref<HTMLDivElement | null>(null)
let slashRequestSeq = 0
const filesListRef = ref<HTMLDivElement | null>(null)
const fileCandidates = ref<MentionCandidate[]>([])
const filesLoading = ref(false)
let filesRequestSeq = 0
// Workspace-relative paths of images the picker resolved this turn. They ride
// the submission separately from the text, because an attached image is not
// named in the prompt at all.
const mentionImages = ref<string[]>([])
// Files already uploaded for a message the composer was given back — withdrawn
// before its turn began, or recalled from the queue. They are sent by id again
// rather than uploaded twice.
const uploadedAttachments = ref<SubmittedAttachment[]>([])

const modelValue = computed({
  get: () => textInput.value,
  set: (val) => setTextInput(String(val)),
})

/**
 * Inserts a workspace path picked outside the composer (the file tree). It
 * goes through the same accept call as the @ picker so a clicked image is
 * attached rather than named, exactly as picking it from the menu would.
 */
async function insertWorkspacePath(path: string, isDir = false) {
  const el = textareaRef.value
  if (!el) return
  const draft = el.value
  const cursor = typeof el.selectionStart === 'number' ? el.selectionStart : draft.length
  // Accept against an empty token at the caret: there is no "@" to replace.
  const runeCursor = toRuneOffset(draft, cursor)
  let res
  try {
    res = await forebrainApi.mentionAccept({
      draft,
      token_start: runeCursor,
      token_end: runeCursor,
      path,
      is_dir: isDir,
    })
  } catch {
    return
  }
  setTextInput(res.draft)
  if (res.imagePath && !mentionImages.value.includes(res.imagePath)) {
    mentionImages.value = [...mentionImages.value, res.imagePath]
  }
  const caret = toUtf16Offset(res.draft, res.cursor)
  await nextTick()
  el.focus()
  el.setSelectionRange(caret, caret)
}

/**
 * Returns what the composer attached besides the files being picked now — the
 * uploads it was given back and the @ picker's images — and clears them for
 * the next turn.
 */
function takeAttached(): Pick<ComposerSubmission, 'attachments' | 'mentionImages'> {
  const taken = { attachments: uploadedAttachments.value, mentionImages: mentionImages.value }
  uploadedAttachments.value = []
  mentionImages.value = []
  return taken
}

function hasAttached(): boolean {
  return uploadedAttachments.value.length > 0 || mentionImages.value.length > 0
}

/**
 * Takes a message back whole — its text, its uploads, its @ images and any
 * picked file that never uploaded — ahead of whatever the composer holds now,
 * which follows it.
 */
function restoreSubmission(returned: ComposerSubmission, unsentFiles: File[] = []) {
  const merged = mergeSubmissions(returned, {
    text: textInput.value,
    attachments: uploadedAttachments.value,
    mentionImages: mentionImages.value,
  })
  setTextInput(merged.text)
  uploadedAttachments.value = merged.attachments
  mentionImages.value = merged.mentionImages
  if (unsentFiles.length) {
    const picked = files.value.flatMap((attachment) => (attachment.file ? [attachment.file] : []))
    clearFiles()
    addFiles([...unsentFiles, ...picked])
  }
  nextTick(() => {
    const el = textareaRef.value
    if (!el) return
    el.focus()
    el.setSelectionRange(el.value.length, el.value.length)
  })
}

function removeUploadedAttachment(fileId: string) {
  uploadedAttachments.value = uploadedAttachments.value.filter((attachment) => attachment.fileId !== fileId)
}

function removeMentionImage(path: string) {
  mentionImages.value = mentionImages.value.filter((image) => image !== path)
}

function uploadedAttachmentLabel(attachment: SubmittedAttachment): string {
  return attachment.filename || t('chat.attachment')
}

function mentionImageLabel(path: string): string {
  return path.split('/').filter(Boolean).pop() ?? path
}

defineExpose({ insertWorkspacePath, takeAttached, hasAttached, restoreSubmission })

const filteredBots = computed(() => {
  const list = props.bots ?? []
  const q = mentionQuery.value.toLowerCase()
  const filtered = list.filter((b) => {
    if (!q) return true
    if (b.id.toLowerCase().startsWith(q)) return true
    return b.label.toLowerCase().includes(q)
  })
  return filtered.sort((a, b) => {
    const aOnline = (a.status ?? '').toUpperCase() === 'ONLINE'
    const bOnline = (b.status ?? '').toUpperCase() === 'ONLINE'
    if (aOnline === bOnline) return 0
    return aOnline ? -1 : 1
  })
})

watch(mentionQuery, (query) => {
  highlightIndex.value = 0
  if (mentionOpen.value && mentionMenuLevel.value === 'files') {
    void fetchFileCandidates(query)
  }
})

watch(mentionOpen, (open) => {
  if (!open) {
    mentionMenuLevel.value = 'root'
    highlightIndex.value = 0
  }
})

watch(slashQuery, () => {
  slashHighlightIndex.value = 0
})

watch([slashOpen, slashQuery, () => props.duringRun, () => props.sideConversation], async ([open, query]) => {
  if (!open) {
    slashCommands.value = []
    slashLoading.value = false
    return
  }
  const req = ++slashRequestSeq
  slashLoading.value = true
  try {
    const res = await forebrainApi.slashCommands('webchat', String(query), {
      duringRun: props.duringRun === true,
      sideConversation: props.sideConversation === true,
    })
    if (req !== slashRequestSeq) return
    slashCommands.value = Array.isArray(res.records) ? res.records : []
  } catch {
    if (req !== slashRequestSeq) return
    slashCommands.value = []
  } finally {
    if (req === slashRequestSeq) slashLoading.value = false
  }
})

function parseSlash(text: string, cursor: number) {
  const before = text.slice(0, cursor)
  if (before.includes('\n')) return null
  if (!before.startsWith('/')) return null
  const raw = before.slice(1)
  if (/\s/.test(raw)) return null
  return { query: raw }
}

function parseMention(text: string, cursor: number) {
  const before = text.slice(0, cursor)
  const m = before.match(/[@\uFF04]([^\s\n@\uFF04]*)$/)
  if (!m) return null
  const full = m[0]
  const start = cursor - full.length
  return { query: m[1] ?? '', start }
}

function openBotsSubmenu() {
  mentionMenuLevel.value = 'bots'
  highlightIndex.value = 0
  clampBotsHighlight()
  nextTick(() => scrollHighlightedBotIntoView())
}

function openFilesSubmenu() {
  mentionMenuLevel.value = 'files'
  highlightIndex.value = 0
  void fetchFileCandidates(mentionQuery.value)
}

function openSubmenu(level: MentionMenuLevel) {
  if (level === 'files') openFilesSubmenu()
  else openBotsSubmenu()
}

function backToRootMenu() {
  mentionMenuLevel.value = 'root'
  highlightIndex.value = 0
}

/**
 * UTF-16 string indices and the engine's rune offsets diverge as soon as the
 * draft holds astral characters, so token bounds are converted at the boundary
 * rather than assumed equal.
 */
function toRuneOffset(text: string, utf16Index: number): number {
  return Array.from(text.slice(0, utf16Index)).length
}

function toUtf16Offset(text: string, runeIndex: number): number {
  return Array.from(text).slice(0, runeIndex).join('').length
}

async function fetchFileCandidates(query: string) {
  const seq = ++filesRequestSeq
  filesLoading.value = true
  try {
    const res = await forebrainApi.mentionSearch(query)
    if (seq !== filesRequestSeq) return
    fileCandidates.value = Array.isArray(res?.records) ? res.records : []
    if (highlightIndex.value >= fileCandidates.value.length) highlightIndex.value = 0
  } catch {
    if (seq !== filesRequestSeq) return
    fileCandidates.value = []
  } finally {
    if (seq === filesRequestSeq) filesLoading.value = false
  }
}

function scrollHighlightedFileIntoView() {
  const el = filesListRef.value?.querySelector<HTMLElement>(
    `[data-file-option-index="${highlightIndex.value}"]`,
  )
  el?.scrollIntoView({ block: 'nearest' })
}

/**
 * Applies a picked file through the shared engine. Every decision — bare path
 * versus attachment, where the cursor lands, whether the picker stays open to
 * drill into a directory — comes back from the server, so this surface cannot
 * drift from the terminal composer.
 */
async function applyFileCandidate(cand: MentionCandidate) {
  const el = textareaRef.value
  if (!el) return
  const draft = el.value
  const cursor = typeof el.selectionStart === 'number' ? el.selectionStart : draft.length
  const parsed = parseMention(draft, cursor)
  if (!parsed) return
  let res
  try {
    res = await forebrainApi.mentionAccept({
      draft,
      token_start: toRuneOffset(draft, parsed.start),
      token_end: toRuneOffset(draft, cursor),
      path: cand.path,
      is_dir: cand.isDir,
    })
  } catch {
    return
  }
  setTextInput(res.draft)
  if (res.imagePath && !mentionImages.value.includes(res.imagePath)) {
    mentionImages.value = [...mentionImages.value, res.imagePath]
  }
  const caret = toUtf16Offset(res.draft, res.cursor)
  if (res.keepOpen) {
    // A directory only advanced the token; keep drilling from the new query.
    mentionQuery.value = ''
  } else {
    mentionOpen.value = false
  }
  await nextTick()
  el.focus()
  el.setSelectionRange(caret, caret)
  if (res.keepOpen) syncMentionState()
}

function scrollHighlightedBotIntoView() {
  const idx = highlightIndex.value
  const el = botsListRef.value?.querySelector<HTMLElement>(`[data-bot-option-index="${idx}"]`)
  el?.scrollIntoView({ block: 'nearest' })
}

function clampBotsHighlight() {
  const len = filteredBots.value.length
  if (len === 0) {
    highlightIndex.value = 0
    return
  }
  if (highlightIndex.value >= len) highlightIndex.value = len - 1
  if (highlightIndex.value < 0) highlightIndex.value = 0
}

function syncMentionState() {
  const el = textareaRef.value
  if (!el) return
  const cursor = typeof el.selectionStart === 'number' ? el.selectionStart : el.value.length
  const slashParsed = parseSlash(el.value, cursor)
  if (slashParsed) {
    slashOpen.value = true
    slashQuery.value = slashParsed.query
    mentionOpen.value = false
    return
  }
  slashOpen.value = false
  const parsed = parseMention(el.value, cursor)
  if (!parsed) {
    mentionOpen.value = false
    return
  }
  const wasOpen = mentionOpen.value
  mentionStart.value = parsed.start
  mentionQuery.value = parsed.query
  // The menu always has a Files row, which does not depend on the bot roster,
  // so it opens even while bots are still loading or none exist.
  mentionOpen.value = true
  if (!wasOpen) {
    mentionMenuLevel.value = 'root'
    highlightIndex.value = 0
  }
  if (mentionMenuLevel.value === 'bots') clampBotsHighlight()
}

function applyMention(bot: BotOption) {
  const el = textareaRef.value
  if (!el) return
  if ((bot.status ?? '').toUpperCase() !== 'ONLINE') return
  const cursor = typeof el.selectionStart === 'number' ? el.selectionStart : el.value.length
  const t = el.value
  const parsed = parseMention(t, cursor)
  if (!parsed) return
  const before = t.slice(0, parsed.start)
  const after = t.slice(cursor)
  const insert = bot.mention.endsWith(' ') ? bot.mention : `${bot.mention} `
  setTextInput(before + insert + after)
  mentionOpen.value = false
  nextTick(() => {
    const pos = (before + insert).length
    el.focus()
    el.setSelectionRange(pos, pos)
  })
}

/**
 * The gateway lists built-in commands and skills as two groups, the group
 * holding the best match first; the menu heads each group with its name.
 */
function isSkillCommand(cmd: SlashCommandRecord | undefined): boolean {
  return cmd?.category === 'skill'
}

function scrollHighlightedSlashIntoView() {
  const el = slashListRef.value?.querySelector<HTMLElement>(
    `[data-slash-option-index="${slashHighlightIndex.value}"]`,
  )
  el?.scrollIntoView({ block: 'nearest' })
}

function applySlashCommand(cmd: SlashCommandRecord) {
  const el = textareaRef.value
  if (!el) return
  const cursor = typeof el.selectionStart === 'number' ? el.selectionStart : el.value.length
  const parsed = parseSlash(el.value, cursor)
  if (!parsed) return
  const name = cmd.canonicalName || cmd.name
  const insert = cmd.supportsInlineArgs ? `/${name} ` : `/${name}`
  setTextInput(insert)
  slashOpen.value = false
  nextTick(() => {
    const pos = insert.length
    el.focus()
    el.setSelectionRange(pos, pos)
  })
}

function handleKeyDown(e: KeyboardEvent) {
  if (slashOpen.value && e.key === 'Escape') {
    e.preventDefault()
    slashOpen.value = false
    return
  }

  if (slashOpen.value) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      const len = slashCommands.value.length
      if (len > 0) slashHighlightIndex.value = (slashHighlightIndex.value + 1) % len
      nextTick(() => scrollHighlightedSlashIntoView())
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      const len = slashCommands.value.length
      if (len > 0) slashHighlightIndex.value = (slashHighlightIndex.value - 1 + len) % len
      nextTick(() => scrollHighlightedSlashIntoView())
      return
    }
    if ((e.key === 'Enter' && !e.shiftKey) || e.key === 'Tab') {
      const cmd = slashCommands.value[slashHighlightIndex.value]
      if (cmd) {
        e.preventDefault()
        applySlashCommand(cmd)
        return
      }
    }
  }

  if (mentionOpen.value && e.key === 'Escape') {
    e.preventDefault()
    if (mentionMenuLevel.value === 'bots') {
      backToRootMenu()
    } else {
      mentionOpen.value = false
    }
    return
  }

  if (mentionOpen.value && mentionMenuLevel.value === 'root') {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      highlightIndex.value = (highlightIndex.value + 1) % rootMenuRows.length
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      highlightIndex.value =
        (highlightIndex.value - 1 + rootMenuRows.length) % rootMenuRows.length
      return
    }
    if (e.key === 'ArrowRight' || e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
      e.preventDefault()
      openSubmenu(rootMenuRows[highlightIndex.value] ?? 'bots')
      return
    }
  }

  if (mentionOpen.value && mentionMenuLevel.value === 'files') {
    if (e.key === 'ArrowLeft') {
      e.preventDefault()
      backToRootMenu()
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.stopPropagation()
      e.preventDefault()
      const len = fileCandidates.value.length
      if (len > 0) {
        const delta = e.key === 'ArrowDown' ? 1 : -1
        highlightIndex.value = (highlightIndex.value + delta + len) % len
        nextTick(() => scrollHighlightedFileIntoView())
      }
      return
    }
    if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
      e.preventDefault()
      const cand = fileCandidates.value[highlightIndex.value]
      if (cand) void applyFileCandidate(cand)
      return
    }
  }

  if (mentionOpen.value && mentionMenuLevel.value === 'bots') {
    if (e.key === 'ArrowLeft') {
      e.preventDefault()
      backToRootMenu()
      return
    }
    if (e.key === 'ArrowDown') {
      e.stopPropagation()
      e.preventDefault()
      const len = filteredBots.value.length
      if (len > 0) {
        highlightIndex.value = (highlightIndex.value + 1) % len
        nextTick(() => scrollHighlightedBotIntoView())
      }
      return
    }
    if (e.key === 'ArrowUp') {
      e.stopPropagation()
      e.preventDefault()
      const len = filteredBots.value.length
      if (len > 0) {
        highlightIndex.value = (highlightIndex.value - 1 + len) % len
        nextTick(() => scrollHighlightedBotIntoView())
      }
      return
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      const bot = filteredBots.value[highlightIndex.value]
      if (bot) applyMention(bot)
      return
    }
    if (e.key === 'Tab') {
      e.preventDefault()
      const bot = filteredBots.value[highlightIndex.value]
      if (bot) applyMention(bot)
      return
    }
  }

  if (props.duringRun && e.key === 'Escape') {
    e.preventDefault()
    emit('interrupt-run')
    return
  }

  if (e.key === 'Escape') {
    emit('escape')
    return
  }

  if ((props.duringRun || props.queueVisible) && ((e.altKey && e.key === 'ArrowUp') || (e.shiftKey && e.key === 'ArrowLeft'))) {
    e.preventDefault()
    emit('edit-last-queued')
    return
  }

  if (e.key === 'Enter') {
    if (isComposing.value || e.shiftKey) return
    e.preventDefault()
    submitForm()
  }

  if (props.duringRun && e.key === 'Tab') {
    e.preventDefault()
    emit('queue-follow-up')
    submitForm()
    return
  }

  if (e.key === 'Backspace' && textInput.value === '' && files.value.length > 0) {
    const lastFile = files.value[files.value.length - 1]
    if (lastFile) removeFile(lastFile.id)
  }
}

function handlePaste(e: ClipboardEvent) {
  const items = e.clipboardData?.items
  if (!items) return

  const pastedFiles: File[] = []
  for (const item of Array.from(items)) {
    if (item.kind === 'file') {
      const file = item.getAsFile()
      if (file) pastedFiles.push(file)
    }
  }

  if (pastedFiles.length > 0) {
    e.preventDefault()
    addFiles(pastedFiles)
  }
}

function onInput() {
  emit('typed')
  syncMentionState()
}

function setComposing(v: boolean) {
  isComposing.value = v
}

function onCompositionEnd() {
  setComposing(false)
  nextTick(() => syncMentionState())
}

function onRootRowClick(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  if (target?.closest?.('[data-mention-chevron]')) {
    openBotsSubmenu()
    return
  }
  openBotsSubmenu()
}
</script>

<template>
  <div class="relative min-w-0 w-full flex-1">
    <ul v-if="files.length || uploadedAttachments.length || mentionImages.length" class="flex flex-wrap items-center gap-2 px-3 pt-3"
      :aria-label="t('prompt.attached')">
      <li v-for="file in files" :key="file.id">
        <PromptInputAttachment :file="file" />
      </li>
      <li v-for="attachment in uploadedAttachments" :key="attachment.fileId"
        class="inline-flex h-8 max-w-[220px] items-center gap-1.5 rounded-md border border-border pl-1.5 pr-1 text-sm font-medium"
        :title="uploadedAttachmentLabel(attachment)">
        <ImageIcon v-if="attachment.mediaType.startsWith('image/')" class="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <Paperclip v-else class="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span class="min-w-0 truncate">{{ uploadedAttachmentLabel(attachment) }}</span>
        <button type="button"
          class="inline-flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
          :aria-label="t('prompt.removeAttached', { name: uploadedAttachmentLabel(attachment) })"
          @click="removeUploadedAttachment(attachment.fileId)">
          <X class="size-3.5" aria-hidden="true" />
        </button>
      </li>
      <li v-for="path in mentionImages" :key="path"
        class="inline-flex h-8 max-w-[220px] items-center gap-1.5 rounded-md border border-border pl-1.5 pr-1 text-sm font-medium"
        :title="path">
        <ImageIcon class="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span class="min-w-0 truncate">{{ mentionImageLabel(path) }}</span>
        <button type="button"
          class="inline-flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
          :aria-label="t('prompt.removeAttached', { name: mentionImageLabel(path) })"
          @click="removeMentionImage(path)">
          <X class="size-3.5" aria-hidden="true" />
        </button>
      </li>
    </ul>
    <textarea ref="textareaRef" v-model="modelValue" data-slot="input-group-control" name="message"
      :placeholder="placeholder" :class="cn(
        'max-h-48 min-h-20 w-full min-w-0 flex-1 resize-none overflow-y-auto rounded-none border-0 bg-transparent px-3 py-3 align-top text-base leading-normal shadow-none !outline-none focus:!outline-none focus-visible:!outline-none focus:!ring-0 focus-visible:!ring-0 dark:bg-transparent md:min-h-[5.25rem] md:text-sm placeholder:text-muted-foreground [field-sizing:fixed]',
        props.class,
      )
        " @keydown="handleKeyDown" @input="onInput" @keyup="syncMentionState" @click="syncMentionState"
      @select="syncMentionState" @paste="handlePaste" @compositionstart="setComposing(true)"
      @compositionend="onCompositionEnd" />
    <div
      v-if="slashOpen"
      class="absolute bottom-full left-0 z-[210] mb-1 min-w-[260px] max-w-[min(100vw-2rem,360px)] overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-lg"
      role="menu"
      :aria-label="t('prompt.slashMenu')"
    >
      <div v-if="slashCommands.length > 0" ref="slashListRef" class="max-h-64 overflow-y-auto">
        <template v-for="(cmd, i) in slashCommands" :key="cmd.name">
          <div
            v-if="i === 0 || isSkillCommand(slashCommands[i - 1]) !== isSkillCommand(cmd)"
            :class="cn(
              'slash-group px-2.5 py-1.5 text-xs font-medium text-muted-foreground',
              i > 0 && 'border-t border-border',
            )"
            role="presentation"
          >
            {{ isSkillCommand(cmd) ? t('prompt.slashGroupSkills') : t('prompt.slashGroupCommands') }}
          </div>
          <button
            type="button"
            role="menuitem"
            :data-slash-option-index="i"
            :data-highlighted="i === slashHighlightIndex || undefined"
            :class="cn('flex w-full items-start gap-2.5 px-3 py-2.5 text-left text-sm', menuRowClass)"
            @mouseenter="slashHighlightIndex = i"
            @mousedown.prevent="applySlashCommand(cmd)"
          >
            <Slash :class="cn('mt-0.5 size-4 shrink-0', menuRowMutedClass)" aria-hidden="true" />
            <span class="min-w-0 flex-1">
              <span class="block font-medium">/{{ cmd.name }}</span>
              <span :class="cn('block text-xs', menuRowMutedClass)">{{ cmd.description }}</span>
              <span v-if="cmd.argumentHint" :class="cn('mt-0.5 block text-[11px]', menuRowMutedClass)">
                {{ cmd.argumentHint }}
              </span>
            </span>
          </button>
        </template>
      </div>
      <div v-else class="px-3 py-2.5 text-sm text-muted-foreground">
        {{ slashLoading ? t('common.loading') : t('prompt.noSlashMatches') }}
      </div>
    </div>
    <div v-if="!slashOpen && mentionOpen"
      class="absolute bottom-full left-0 z-[200] mb-1 min-w-[220px] max-w-[min(100vw-2rem,320px)] overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-lg"
      role="menu" :aria-label="t('prompt.mentionMenu')">
      <div class="border-b border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground">
        {{ mentionMenuLevel === 'root'
          ? t('prompt.mention')
          : mentionMenuLevel === 'files'
            ? t('prompt.selectFile')
            : t('prompt.selectBot') }}
      </div>

      <template v-if="mentionMenuLevel === 'root'">
        <button type="button" role="menuitem" :aria-expanded="false"
          :data-highlighted="highlightIndex === 0 || undefined"
          :class="cn('flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm', menuRowClass)"
          @mouseenter="highlightIndex = 0" @mousedown.prevent="onRootRowClick">
          <Bot :class="cn('size-4 shrink-0', menuRowMutedClass)" aria-hidden="true" />
          <span class="min-w-0 flex-1 font-medium">{{ t('prompt.bot') }}</span>
          <span data-mention-chevron
            :class="cn('inline-flex shrink-0 rounded p-0.5 hover:bg-accent-foreground/15 hover:text-accent-foreground', menuRowMutedClass)"
            role="presentation" @mousedown.prevent.stop="openBotsSubmenu()">
            <ChevronRight class="size-4" aria-hidden="true" />
          </span>
        </button>
        <button type="button" role="menuitem" :aria-expanded="false"
          :data-highlighted="highlightIndex === 1 || undefined"
          :class="cn('flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm', menuRowClass)"
          @mouseenter="highlightIndex = 1" @mousedown.prevent="openFilesSubmenu()">
          <FileText :class="cn('size-4 shrink-0', menuRowMutedClass)" aria-hidden="true" />
          <span class="min-w-0 flex-1 font-medium">{{ t('prompt.file') }}</span>
          <span data-mention-chevron
            :class="cn('inline-flex shrink-0 rounded p-0.5 hover:bg-accent-foreground/15 hover:text-accent-foreground', menuRowMutedClass)"
            role="presentation" @mousedown.prevent.stop="openFilesSubmenu()">
            <ChevronRight class="size-4" aria-hidden="true" />
          </span>
        </button>
      </template>

      <template v-else-if="mentionMenuLevel === 'files'">
        <button type="button"
          :class="cn('flex w-full items-center gap-2 border-b border-border px-3 py-2 text-left text-xs text-muted-foreground', menuRowClass)"
          @mousedown.prevent="backToRootMenu()">
          <ChevronLeft class="size-3.5 shrink-0" aria-hidden="true" />
          {{ t('common.back') }}
        </button>
        <div ref="filesListRef" class="max-h-56 overflow-y-auto">
          <template v-if="fileCandidates.length > 0">
            <button v-for="(cand, i) in fileCandidates" :key="cand.path" type="button" role="menuitem"
              :data-file-option-index="i" :data-highlighted="i === highlightIndex || undefined"
              :class="cn('flex w-full items-start gap-2 px-3 py-2 text-left text-sm', menuRowClass)"
              @mouseenter="highlightIndex = i" @mousedown.prevent="applyFileCandidate(cand)">
              <component :is="cand.isDir ? Folder : FileText" :class="cn('mt-0.5 size-4 shrink-0', menuRowMutedClass)"
                aria-hidden="true" />
              <span class="min-w-0 flex-1 break-all">{{ cand.path }}</span>
            </button>
          </template>
          <div v-else class="px-3 py-2.5 text-sm text-muted-foreground">
            {{ filesLoading ? t('common.loading') : t('prompt.noFileMatches') }}
          </div>
        </div>
      </template>

      <template v-else>
        <button type="button"
          :class="cn('flex w-full items-center gap-2 border-b border-border px-3 py-2 text-left text-xs text-muted-foreground', menuRowClass)"
          @mousedown.prevent="backToRootMenu()">
          <ChevronLeft class="size-3.5 shrink-0" aria-hidden="true" />
          {{ t('common.back') }}
        </button>
        <div ref="botsListRef" class="max-h-56 overflow-y-auto">
          <template v-if="filteredBots.length > 0">
            <button v-for="(bot, i) in filteredBots" :key="bot.id" type="button" role="menuitem" :disabled="(bot.status ?? '').toUpperCase() !== 'ONLINE'"
              :aria-disabled="(bot.status ?? '').toUpperCase() !== 'ONLINE' ? 'true' : undefined"
              :data-bot-option-index="i" :data-highlighted="i === highlightIndex || undefined"
              :class="cn(
                'flex w-full flex-col items-start gap-0.5 px-3 py-2.5 text-left text-sm',
                menuRowClass,
                (bot.status ?? '').toUpperCase() !== 'ONLINE' && 'opacity-50 cursor-not-allowed text-muted-foreground hover:bg-transparent hover:text-muted-foreground focus-visible:bg-transparent focus-visible:text-muted-foreground',
              )" @mouseenter="highlightIndex = i">
              <span class="font-medium">{{ bot.label }}</span>
              <span v-if="bot.description" :class="cn('text-xs', menuRowMutedClass)">{{ bot.description }}</span>
            </button>
          </template>
          <div v-else class="px-3 py-2.5 text-sm text-muted-foreground">
            {{ (bots?.length ?? 0) === 0 ? t('prompt.noBots') : t('prompt.noBotMatches') }}
          </div>
        </div>
      </template>
    </div>
  </div>
</template>
