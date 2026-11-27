<template>
  <!-- A delivered text file, read-only (plan 153 B4): the same CodeMirror the
       file editor uses, in its read-only mode and the one palette every code
       surface here draws — monokai-light, whatever the theme. -->
  <div ref="mountEl" class="readonly-code" data-testid="readonly-code" />
</template>

<script setup lang="ts">
/**
 * Read-only source view for a delivered text file. The editor's own read-only
 * mode, without its WYSIWYG half: a .py or a .csv has no document view, and a
 * preview must never be editable.
 *
 * A file:line link opens this view with a range to highlight (plan
 * file-line-range-links): the lines carry a brand-soft class that scrolls to
 * the centre, then fades out once — the highlight is a landing mark, not a
 * permanent selection.
 */
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { RangeSetBuilder, StateEffect, StateField } from '@codemirror/state'
import { Decoration, type DecorationSet } from '@codemirror/view'
import { monokaiLightHighlight } from '@/lib/editor/monokaiLight'
import { languageForPath } from '@/lib/editor/languages'

const props = defineProps<{
  /** The file's name, which decides the language. */
  path: string
  /** The whole file's text. */
  text: string
  /** The first line to highlight, 1-based; absent, nothing is highlighted. */
  highlightFrom?: number
  /** The last line to highlight, 1-based; absent, the first line alone. */
  highlightTo?: number
}>()

/** How long the landing mark stays before it fades. */
const HIGHLIGHT_HOLD_MS = 2500

const setHighlight = StateEffect.define<DecorationSet>()

/** The highlighted lines, held as decorations so a doc change maps them. */
const highlightField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(value, tr) {
    value = value.map(tr.changes)
    for (const effect of tr.effects) {
      if (effect.is(setHighlight)) value = effect.value
    }
    return value
  },
  provide: (field) => EditorView.decorations.from(field),
})

const mountEl = ref<HTMLElement | null>(null)
let view: EditorView | null = null
let fadeTimer: ReturnType<typeof setTimeout> | null = null

function clearFadeTimer() {
  if (fadeTimer !== null) {
    clearTimeout(fadeTimer)
    fadeTimer = null
  }
}

/**
 * Paints the requested range, clamped to the document, and centres its first
 * line. The class comes off after the hold; the persistent .cm-line transition
 * is what fades it, so the mark lands at once and leaves slowly.
 */
function applyHighlight() {
  if (!view) return
  clearFadeTimer()
  const requested = props.highlightFrom
  if (requested === undefined) {
    view.dispatch({ effects: setHighlight.of(Decoration.none) })
    return
  }
  const lines = view.state.doc.lines
  const from = Math.min(Math.max(1, requested), lines)
  const to = Math.min(Math.max(from, props.highlightTo ?? requested), lines)
  const builder = new RangeSetBuilder<Decoration>()
  for (let line = from; line <= to; line++) {
    const at = view.state.doc.line(line).from
    builder.add(at, at, Decoration.line({ class: 'cm-file-line-highlight' }))
  }
  view.dispatch({
    effects: [
      setHighlight.of(builder.finish()),
      EditorView.scrollIntoView(view.state.doc.line(from).from, { y: 'center' }),
    ],
  })
  fadeTimer = setTimeout(() => {
    fadeTimer = null
    view?.dispatch({ effects: setHighlight.of(Decoration.none) })
  }, HIGHLIGHT_HOLD_MS)
}

async function mount() {
  const el = mountEl.value
  if (!el) return
  const extensions = [
    basicSetup,
    monokaiLightHighlight,
    // The editor's own read-only mode (what FileEditor draws a locked file
    // with): no caret edits, no edits at all.
    EditorView.editable.of(false),
    EditorView.lineWrapping,
    highlightField,
  ]
  const description = languageForPath(props.path)
  if (description) extensions.push(await description.load())
  if (el !== mountEl.value) return
  el.innerHTML = ''
  view = new EditorView({ doc: props.text, extensions, parent: el })
  applyHighlight()
}

function destroy() {
  clearFadeTimer()
  view?.destroy()
  view = null
}

watch(() => [props.path, props.text], () => {
  destroy()
  void mount()
})

// The range is its own watch: reopening the same file at a new range repaints
// and recentres without remounting the editor.
watch(() => [props.highlightFrom, props.highlightTo], applyHighlight)

onMounted(() => {
  void mount()
})

onBeforeUnmount(destroy)
</script>

<style scoped>
.readonly-code {
  min-height: 0;
  font-size: 12.5px;
}

.readonly-code :deep(.cm-editor) {
  background: transparent;
  outline: none;
}

.readonly-code :deep(.cm-content) {
  font: 12.5px/1.7 var(--forebrain-mono-font);
}

/* The landing mark's fade lives on every line, so removing the highlight
   class transitions the background back instead of snapping it. */
.readonly-code :deep(.cm-line) {
  transition: background-color 1.1s ease;
}

.readonly-code :deep(.cm-file-line-highlight) {
  background-color: var(--forebrain-brand-soft);
  transition: none;
}
</style>