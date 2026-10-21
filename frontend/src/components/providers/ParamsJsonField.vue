<template>
  <div class="pjson" :class="{ 'pjson--invalid': invalid }" data-testid="provider-params-editor">
    <div ref="mountEl" class="pjson-mount" />
  </div>
</template>

<script setup lang="ts">
/**
 * The params field of a model service: a small CodeMirror in JSON. The field
 * holds the provider's own request fields verbatim, so whoever edits it needs
 * the brackets, quotes and colouring a code block has — a single-line input
 * turns one typo into an unreadable string.
 *
 * The model is the JSON text itself (the form sends it as typed); whether that
 * text is a JSON object is the form's judgement, so `invalid` only paints the
 * border. CodeMirror cannot lay out in jsdom, so tests stub this component.
 */
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { json } from '@codemirror/lang-json'
import { monokaiLightHighlight } from '@/lib/editor/monokaiLight'

const props = withDefaults(
  defineProps<{ modelValue: string; invalid?: boolean; ariaLabel?: string }>(),
  { invalid: false, ariaLabel: '' },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const mountEl = ref<HTMLElement | null>(null)
let view: EditorView | null = null

onMounted(() => {
  const el = mountEl.value
  if (!el) return
  view = new EditorView({
    doc: props.modelValue,
    parent: el,
    extensions: [
      basicSetup,
      json(),
      monokaiLightHighlight,
      EditorView.lineWrapping,
      EditorView.contentAttributes.of(props.ariaLabel ? { 'aria-label': props.ariaLabel } : {}),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) emit('update:modelValue', update.state.doc.toString())
      }),
    ],
  })
})

// Another row, or a discarded edit, replaces the text from outside. Only a
// genuinely different document is written back, so the caret never jumps on the
// echo of the person's own typing.
watch(
  () => props.modelValue,
  (value) => {
    if (!view || value === view.state.doc.toString()) return
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } })
  },
)

onBeforeUnmount(() => {
  view?.destroy()
  view = null
})
</script>

<style scoped>
.pjson {
  overflow: hidden;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 8px;
  background: var(--forebrain-input-bg);
}
.pjson--invalid {
  border-color: var(--forebrain-danger);
}
.pjson-mount {
  height: 120px;
}
.pjson :deep(.cm-editor) {
  height: 100%;
  background: transparent;
  font-size: 12px;
}
.pjson :deep(.cm-editor.cm-focused) {
  outline: none;
}
.pjson :deep(.cm-scroller) {
  font-family: var(--forebrain-mono-font);
}
.pjson :deep(.cm-gutters) {
  border-right: 1px solid var(--forebrain-divider);
  background: transparent;
}
</style>
