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
 */
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { monokaiLightHighlight } from '@/lib/editor/monokaiLight'
import { languageForPath } from '@/lib/editor/languages'

const props = defineProps<{
  /** The file's name, which decides the language. */
  path: string
  /** The whole file's text. */
  text: string
}>()

const mountEl = ref<HTMLElement | null>(null)
let view: EditorView | null = null

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
  ]
  const description = languageForPath(props.path)
  if (description) extensions.push(await description.load())
  if (el !== mountEl.value) return
  el.innerHTML = ''
  view = new EditorView({ doc: props.text, extensions, parent: el })
}

function destroy() {
  view?.destroy()
  view = null
}

watch(() => [props.path, props.text], () => {
  destroy()
  void mount()
})

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
</style>
