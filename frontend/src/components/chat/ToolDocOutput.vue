<template>
  <!-- The whole output of one shell call, as a document page of the drawer
       (plan 137): what a truncated card’s eye opens. The text is laid out in
       the prototype’s plain greys — monospace, wrapped, never highlighted —
       with copy in the corner and a footnote for what was never spooled. -->
  <div class="tool-doc-output" data-testid="tool-doc-output">
    <DocCopyButton :label="t('chat.copyOutput')" :text="text" class="doc-copy" />
    <p v-if="loading" class="state">{{ t('chat.outputLoading') }}</p>
    <p v-else-if="gone" class="state">{{ t('chat.outputGone') }}</p>
    <p v-else-if="error" class="state state--error">{{ error }}</p>
    <template v-else>
      <ScrollArea class="out-area">
        <pre class="out-text">{{ shownText }}</pre>
        <!-- The sentinel that asks for the next slice: the whole output
             renders 2 MB at a time, so a huge one never blocks the page. -->
        <div ref="sentinel" class="sentinel" aria-hidden="true" />
      </ScrollArea>
      <p v-if="spoolOmittedBytes > 0" class="spool-note">{{ t('chat.outputSpoolOmitted') }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The output document page (plan 137). The full text is fetched once from
 * the tool-output route the gateway serves a truncated card’s retrieval id
 * — the conversation’s own, or a subagent’s through its agent — and rendered
 * in slices: a 100 MB output draws 2 MB at a time, the next slice arriving
 * when the reader reaches the bottom. No library component fits (the
 * elements’ document pages lay out commands and titles, not a fetch), so
 * this is written here beside them.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { AxiosError } from 'axios'
import { ScrollArea } from '@repo/shadcn-vue/components/ui/scroll-area'
import { t } from '@/locales'
import { forebrainApi, getErrorMessage } from '@/lib/api'
import DocCopyButton from './DocCopyButton.vue'

/** One rendered slice of the output. */
const SLICE_BYTES = 2 * 1024 * 1024

const props = defineProps<{
  /** Where the whole output reads from: the session (and agent) and retrieval id. */
  source: { sessionId: string; agentId?: string; retrieveId: number }
}>()

// A source always loads on mount, so the page's first frame already says so.
const loading = ref(true)
const gone = ref(false)
const error = ref('')
const text = ref('')
const spoolOmittedBytes = ref(0)
const shownBytes = ref(SLICE_BYTES)
const sentinel = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | null = null
let generation = 0

async function load() {
  const gen = ++generation
  loading.value = true
  gone.value = false
  error.value = ''
  text.value = ''
  shownBytes.value = SLICE_BYTES
  try {
    const res = props.source.agentId
      ? await forebrainApi.subagentToolOutput(props.source.sessionId, props.source.agentId, props.source.retrieveId)
      : await forebrainApi.chatToolOutput(props.source.sessionId, props.source.retrieveId)
    if (gen !== generation) return
    text.value = res.text
    spoolOmittedBytes.value = res.spoolOmittedBytes ?? 0
  } catch (cause) {
    if (gen !== generation) return
    if ((cause as AxiosError)?.response?.status === 404) gone.value = true
    else error.value = getErrorMessage(cause)
  } finally {
    if (gen === generation) loading.value = false
  }
}

/** The slice of the output drawn so far, and whether more remains. */
const shownText = computed(() => text.value.slice(0, shownBytes.value))

function grow() {
  if (shownBytes.value < text.value.length) shownBytes.value += SLICE_BYTES
}

watch(() => props.source, () => {
  void load()
})

onMounted(() => {
  void load()
  if (typeof IntersectionObserver === 'undefined') return
  observer = new IntersectionObserver((entries) => {
    if (entries.some((entry) => entry.isIntersecting)) grow()
  }, { root: null, rootMargin: '600px' })
  if (sentinel.value) observer.observe(sentinel.value)
})

// The sentinel mounts with the text; an observer set up before it exists
// watches nothing, so it is re-aimed whenever the element appears.
watch(sentinel, (el, was) => {
  if (el && observer && el !== was) observer.observe(el)
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})
</script>

<style scoped>
.tool-doc-output {
  position: relative;
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  margin: 10px 12px;
}

.doc-copy {
  position: absolute;
  top: -2px;
  right: 0;
  z-index: 2;
}

.state {
  margin: 4px 2px;
  font: 12.5px/1.6 var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.state--error {
  color: var(--forebrain-danger);
}

.out-area {
  flex: 1;
  min-height: 0;
  overflow: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
}

.out-text {
  margin: 0;
  padding: 8px 30px 8px 2px;
  font: 12px/1.65 var(--forebrain-mono-font);
  color: var(--forebrain-text-2);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  tab-size: 4;
}

.sentinel {
  height: 1px;
}

.spool-note {
  margin: 8px 2px 0;
  font: 12px var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}
</style>
