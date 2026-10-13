<template>
  <div class="mde" :class="{ 'mde--fill': height }" data-testid="file-editor">
    <FrontmatterFields
      v-if="showFrontmatter"
      :model-value="fmInner"
      :disabled="readonly"
      @update:model-value="onFrontmatterInput"
    />
    <div class="mde-mount" :style="mountStyle">
      <div ref="mountEl" class="mde-mount-target" />
      <div v-if="loading" class="mde-loading">{{ t('editor.loading') }}</div>
    </div>
    <div class="mde-foot">
      <span>{{ languageLabel }}</span>
      <span v-if="dirty" class="dirty">· {{ t('editor.modified') }}</span>
      <span class="grow" />
      <div v-if="isMarkdown" class="mde-seg" role="group" :aria-label="t('editor.modeAria')">
        <button
          type="button"
          :aria-pressed="mode === 'rich'"
          data-testid="editor-mode-rich"
          @click="setMode('rich')"
        >
          {{ t('editor.wysiwyg') }}
        </button>
        <button
          type="button"
          :aria-pressed="mode === 'src'"
          data-testid="editor-mode-src"
          @click="setMode('src')"
        >
          {{ t('editor.source') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The one file editor the web client uses: a Markdown file opens in Milkdown
 * Crepe (WYSIWYG, with its top bar and editable, highlighted code blocks) and
 * can flip to a CodeMirror source pane; any other file opens straight into
 * CodeMirror with its language. Code is always Monokai Light, whatever the
 * theme — the palette is shared with the assistant's own replies.
 *
 * The component is imported through `defineAsyncComponent` by its callers, so
 * Crepe and CodeMirror land in a chunk of their own and only load when a file
 * is actually opened.
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { markdown } from '@codemirror/lang-markdown'
import { Crepe } from '@milkdown/crepe'

import { useI18n } from '@/locales'
import { crepeFeatureConfigs, crepeFeatures } from '@/lib/editor/crepeLocale'
import { languageForPath, languages } from '@/lib/editor/languages'
import { monokaiLightHighlight } from '@/lib/editor/monokaiLight'
import FrontmatterFields from './FrontmatterFields.vue'

import '@milkdown/crepe/theme/common/style.css'
import '@milkdown/crepe/theme/frame.css'

const props = defineProps<{
  /** The file's path; decides the language and whether it is Markdown. */
  path: string
  /** The whole file's text — the model the caller reads and writes. */
  modelValue: string
  readonly?: boolean
  /** Optional fixed height for the editing area. */
  height?: string
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'dirty', value: boolean): void
}>()

const { t, locale } = useI18n()

const mountEl = ref<HTMLElement | null>(null)
const mode = ref<'rich' | 'src'>('rich')
const loading = ref(false)

// The full current text and the text as it was when the file was opened; a
// caller writes back only while the two differ, so an untouched file is never
// re-saved (and never re-serialised onto disk).
const text = ref(props.modelValue)
const baseline = ref(props.modelValue)
let lastEmitted = props.modelValue

// The leading `--- … ---` block of a Markdown file, kept apart so the WYSIWYG
// editor only ever sees the prose and the block is stitched back byte-for-byte.
const fmHead = ref('')
const fmInner = ref('')
const fmTail = ref('')
let fmPrefixLen = 0

// A live editor instance (Crepe or a CodeMirror view) behind one destroy().
let editor: { destroy: () => void } | null = null
// Bumped on every (re)mount so an async creation that resolves after the
// editor was torn down is discarded instead of attaching to a dead node.
let runId = 0

const hasFrontmatter = ref(false)

const isMarkdown = computed(() => /\.md$/i.test(props.path))
const dirty = computed(() => text.value !== baseline.value)
const languageLabel = computed(() => {
  if (isMarkdown.value) return 'Markdown'
  return languageForPath(props.path)?.name ?? t('editor.plainText')
})
const showFrontmatter = computed(() => isMarkdown.value && mode.value === 'rich' && hasFrontmatter.value)
const mountStyle = computed(() => (props.height ? { height: props.height, maxHeight: 'none' } : undefined))

watch(dirty, (value) => emit('dirty', value), { immediate: true })

watch(
  [() => props.path, () => props.modelValue],
  ([path, value], [prevPath]) => {
    if (path !== prevPath) mode.value = 'rich'
    // Our own writes echo back through the prop; only a value we did not emit
    // is a genuine external load (a fresh file, or a reload after save).
    if (value === lastEmitted && path === prevPath) return
    void reload()
  },
)

function destroyEditor() {
  if (editor) {
    try {
      editor.destroy()
    } catch {
      // A half-built editor can throw on destroy; the node is going away anyway.
    }
    editor = null
  }
}

function parseFrontmatter() {
  hasFrontmatter.value = false
  fmHead.value = ''
  fmInner.value = ''
  fmTail.value = ''
  fmPrefixLen = 0
  if (!isMarkdown.value) return
  const match = /^---\n([\s\S]*?)\n---\n?/.exec(text.value)
  if (!match) return
  hasFrontmatter.value = true
  fmHead.value = '---\n'
  fmInner.value = match[1]
  fmTail.value = match[0].slice(fmHead.value.length + match[1].length)
  fmPrefixLen = match[0].length
}

function frontmatterPrefix(): string {
  return fmHead.value + fmInner.value + fmTail.value
}

function commit(value: string) {
  if (value === text.value) return
  text.value = value
  lastEmitted = value
  emit('update:modelValue', value)
}

// Rebuild from scratch: a new path or a value the caller loaded. Resets the
// dirty baseline.
async function reload() {
  const id = ++runId
  loading.value = true
  destroyEditor()
  text.value = props.modelValue
  baseline.value = props.modelValue
  lastEmitted = props.modelValue
  parseFrontmatter()
  loading.value = false
  await nextTick()
  if (id !== runId) return
  await mount(id)
}

// Swap WYSIWYG/source without touching the text or the dirty state.
async function remount() {
  const id = ++runId
  loading.value = true
  destroyEditor()
  parseFrontmatter()
  loading.value = false
  await nextTick()
  if (id !== runId) return
  await mount(id)
}

function setMode(next: 'rich' | 'src') {
  if (next === mode.value) return
  mode.value = next
  void remount()
}

async function mount(id: number) {
  const el = mountEl.value
  if (!el) return
  el.innerHTML = ''
  if (isMarkdown.value && mode.value === 'rich') await mountCrepe(el, id)
  else await mountCodeMirror(el, id)
}

async function mountCrepe(el: HTMLElement, id: number) {
  const body = hasFrontmatter.value ? text.value.slice(fmPrefixLen) : text.value
  const crepe = new Crepe({
    root: el,
    defaultValue: body,
    features: crepeFeatures(),
    featureConfigs: crepeFeatureConfigs(locale.value, { languages, theme: monokaiLightHighlight }),
  })
  if (props.readonly) crepe.setReadonly(true)
  // The last markdown the editor reported. Crepe re-serialises the document as
  // it parses it (list markers, blank lines, table alignment), and some of that
  // lands *after* create() resolves; none of it is the user's edit, so an
  // update that only repeats what the editor already holds moves nothing.
  let known = body
  crepe.on((listener) =>
    listener.markdownUpdated((_ctx, md) => {
      if (md === known) return
      known = md
      commit(frontmatterPrefix() + md)
    }),
  )
  editor = { destroy: () => void crepe.destroy() }
  await crepe.create()
  if (id !== runId) {
    destroyEditor()
    return
  }
  // Baseline the dirty check on what the editor now holds, not on the bytes
  // that were opened: a file Crepe reformats on load is not thereby "edited",
  // and an untouched file must never be writable.
  known = crepe.getMarkdown()
  const settled = frontmatterPrefix() + known
  baseline.value = settled
  if (settled !== text.value) {
    text.value = settled
    lastEmitted = settled
    emit('update:modelValue', settled)
  }
}

async function mountCodeMirror(el: HTMLElement, id: number) {
  const extensions = [
    basicSetup,
    monokaiLightHighlight,
    EditorView.updateListener.of((update) => {
      if (update.docChanged) commit(update.state.doc.toString())
    }),
  ]
  if (isMarkdown.value) {
    extensions.push(markdown({ codeLanguages: [...languages] }), EditorView.lineWrapping)
  } else {
    const description = languageForPath(props.path)
    if (description) extensions.push(await description.load())
  }
  if (props.readonly) extensions.push(EditorView.editable.of(false))
  if (id !== runId) return
  el.innerHTML = ''
  const view = new EditorView({ doc: text.value, extensions, parent: el })
  editor = { destroy: () => view.destroy() }
}

function onFrontmatterInput(inner: string) {
  const body = text.value.slice(fmPrefixLen)
  fmInner.value = inner
  const prefix = frontmatterPrefix()
  fmPrefixLen = prefix.length
  commit(prefix + body)
}

onBeforeUnmount(() => {
  runId++
  destroyEditor()
})

void reload()
</script>

<style scoped>
.mde {
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 8px;
  background: var(--forebrain-surface);
}
.mde-mount {
  position: relative;
  min-height: 300px;
  max-height: 470px;
  overflow: auto;
}
.mde-mount-target {
  min-height: 300px;
}
/* Fills the space its caller gives it (a panel, not a modal): the mount takes
   the slack so the mode toggle in the footer is never pushed out of view. The
   caller sizes `.mde` (a flex child); no percentage height here, which would
   fight the flex parent and jitter the layout. */
.mde--fill {
  min-height: 0;
}
.mde--fill .mde-mount {
  flex: 1 1 auto;
  min-height: 0;
  max-height: none;
}
.mde--fill .mde-mount-target {
  min-height: 0;
}
.mde-loading {
  position: absolute;
  inset: 0;
  padding: 16px 20px;
  color: var(--forebrain-muted-text);
  font-size: 12.5px;
  background: var(--forebrain-surface);
}
.mde-foot {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
  padding: 4px 6px 4px 12px;
  border-top: 1px solid var(--forebrain-divider);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.mde-foot .dirty {
  color: var(--forebrain-warning);
}
.mde-foot .grow {
  flex: 1;
}
.mde-seg {
  display: inline-flex;
  overflow: hidden;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  background: var(--forebrain-surface);
}
.mde-seg button {
  border: 0;
  background: transparent;
  font: inherit;
  font-size: 12px;
  padding: 3px 10px;
  color: var(--forebrain-text-2);
  cursor: pointer;
}
.mde-seg button[aria-pressed='true'] {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  font-weight: 600;
}
</style>

<!-- Crepe and CodeMirror build their own DOM, so their styling cannot be
     scoped to this component's elements; it lives under the `.mde` prefix the
     design preview uses, mapping the Crepe theme variables onto the app's
     tokens. -->
<style>
.mde .milkdown {
  --crepe-color-background: var(--forebrain-surface);
  --crepe-color-on-background: var(--forebrain-text);
  --crepe-color-surface: var(--forebrain-bg-alt);
  --crepe-color-surface-low: var(--forebrain-button-alt-bg);
  --crepe-color-on-surface: var(--forebrain-text);
  --crepe-color-on-surface-variant: var(--forebrain-text-2);
  --crepe-color-outline: var(--forebrain-divider-strong);
  --crepe-color-primary: var(--forebrain-brand-1);
  --crepe-color-secondary: var(--forebrain-brand-soft);
  --crepe-color-on-secondary: var(--forebrain-brand-1);
  --crepe-color-inverse: var(--forebrain-button-alt-bg);
  --crepe-color-on-inverse: var(--forebrain-text);
  --crepe-color-inline-code: #0e6e7e;
  --crepe-color-error: var(--forebrain-danger);
  --crepe-color-hover: var(--forebrain-button-alt-bg);
  --crepe-color-selected: var(--forebrain-brand-soft);
  --crepe-color-inline-area: var(--forebrain-button-alt-hover-bg);
  --crepe-base-font-size: 14px;
  --crepe-font-title: var(--forebrain-ui-font);
  --crepe-font-default: var(--forebrain-ui-font);
  --crepe-font-code: var(--forebrain-mono-font);
  --crepe-shadow-1: 0 1px 3px rgba(16, 24, 40, 0.12);
  --crepe-shadow-2: var(--forebrain-shadow-pop);
}
.mde .milkdown .ProseMirror {
  padding: 14px 28px 28px 64px;
}
.mde .milkdown .ProseMirror h1 {
  font-size: 1.6em;
  line-height: 1.3;
}
.mde .milkdown .ProseMirror h2 {
  font-size: 1.3em;
  line-height: 1.35;
}
.mde .milkdown .ProseMirror h3 {
  font-size: 1.12em;
}
.mde .milkdown .milkdown-code-block .tools .language-button {
  opacity: 1;
}
.mde .milkdown milkdown-top-bar,
.mde .milkdown .milkdown-top-bar {
  position: sticky;
  top: 0;
  z-index: 2;
}
.mde .cm-editor {
  font-size: 12.5px;
  color: #272822;
  background: var(--forebrain-code-bg);
}
.mde .milkdown .milkdown-code-block,
.mde .milkdown .milkdown-code-block .cm-editor {
  background: var(--forebrain-code-bg);
}
.mde .cm-editor.cm-focused {
  outline: none;
}
.mde .cm-scroller {
  font-family: var(--forebrain-mono-font);
  line-height: 1.65;
}
.mde .cm-gutters {
  background: var(--forebrain-bg-alt);
  border-right: 1px solid var(--forebrain-divider);
  color: var(--forebrain-muted-text);
}
</style>
