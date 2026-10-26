<template>
  <!-- The shell card’s body, the prototype’s shOutput + shFoot + shFiles
       (app-preview.html :3629-3680): the output alone, then the footer that
       says what was omitted, then the files it changed. No command restated,
       no labels, no JSON — the body is the result only (owner 2026-10-25). -->
  <!-- Running: the tail of what it printed so far, a blinking caret on the
       last line, and the count of the lines folded away above them. -->
  <div v-if="running && (tailLines.length || earlierCount > 0)" class="outz" data-testid="shell-output">
    <div v-if="earlierCount > 0" class="earlier">{{ t('chat.shellEarlierLines', { n: earlierCount }) }}</div>
    <div v-for="(line, i) in tailLines" :key="i" class="ol">{{ line }}<span v-if="i === tailLines.length - 1" class="caret" aria-hidden="true" /></div>
  </div>
  <!-- Settled: stdout a row a line, stderr the same in red, scrolled inside
       the panel when long; copy appears on hover or focus, never in the way. -->
  <div v-else-if="stdoutLines.length || stderrLines.length" class="outz" data-testid="shell-output">
    <Button
      type="button"
      variant="ghost"
      size="icon"
      class="cp"
      :class="{ done: copied }"
      :aria-label="t('chat.copyOutput')"
      v-tip:top="t('chat.copyOutput')"
      data-testid="shell-copy"
      @click="copyOutput"
    >
      <Check v-if="copied" class="okk" aria-hidden="true" />
      <Copy v-else class="cpy" aria-hidden="true" />
    </Button>
    <ScrollArea class="out-scroll">
      <div v-for="(line, i) in stdoutLines" :key="`o-${i}`" class="ol">{{ line }}</div>
      <div v-for="(line, i) in stderrLines" :key="`e-${i}`" class="ol err">{{ line }}</div>
    </ScrollArea>
  </div>
  <!-- Nothing printed and nothing changed: one quiet line says so. -->
  <div v-else-if="!(card?.files?.length)" class="outz" data-testid="shell-output">
    <span class="none">{{ t('chat.noOutput') }}</span>
  </div>
  <!-- The footer exists only when there is something to say: what the middle
       omitted, and the eye that opens the whole output in the drawer. -->
  <div v-if="omittedBytes > 0" class="foot" data-testid="shell-foot">
    <span>{{ t('chat.outputOmitted', { size: formatOmittedSize(omittedBytes) }) }}</span>
    <Button
      v-if="card?.retrieveId && docSource?.sessionId"
      type="button"
      variant="ghost"
      size="icon"
      class="foot-eye"
      :aria-label="t('chat.openFullOutput')"
      v-tip:top="t('chat.openFullOutput')"
      data-testid="shell-open-output"
      @click.stop="openDoc"
    >
      <Eye class="size-[15px]" aria-hidden="true" />
    </Button>
  </div>
  <!-- The files it changed, and the engine’s short notes closing the card. -->
  <FileChangeList v-if="card?.files?.length || card?.notes?.length" :files="card?.files ?? []" :notes="card?.notes ?? []" />
</template>

<script setup lang="ts">
/**
 * A shell call’s body (plan 137): only what the command produced. The facts
 * come from the gateway’s shell card (plan 135) — never re-derived from the
 * Markdown body the card once rendered — and the whole output opens as a
 * drawer document from the footer’s eye when the streams were cut. No
 * library ships this panel, so it is written here; its values are the
 * prototype’s own.
 */
import { computed, ref } from 'vue'
import { Check, Copy, Eye } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { ScrollArea } from '@repo/shadcn-vue/components/ui/scroll-area'
import { t } from '@/locales'
import type { ShellCard } from '@/composables/useChatStream'
import type { DocTab } from '@/composables/useDocTabs'
import FileChangeList from './FileChangeList.vue'

const props = withDefaults(defineProps<{
  /** The gateway’s shell card facts; absent for a call that never settled. */
  card?: ShellCard
  /** The call’s state, in the prototype’s own vocabulary. */
  status: 'running' | 'awaiting' | 'done' | 'denied' | 'canceled' | 'error'
  /** The output accumulated while the call runs, replaced by the card when it settles. */
  liveOutput?: string
  /** The command as it ran: the drawer tab’s label and the copy the page offers. */
  command: string
  /** Where the full output reads from; the eye waits for a session to read. */
  docSource?: { sessionId: string; agentId?: string }
}>(), {
  card: undefined,
  liveOutput: '',
  docSource: undefined,
})

const emit = defineEmits<{
  (e: 'open-doc', tab: DocTab): void
}>()

const running = computed(() => props.status === 'running')

/**
 * The lines of the output so far, without the trailing empty piece a
 * not-yet-finished line leaves behind.
 */
const liveLines = computed(() => {
  const text = String(props.liveOutput ?? '')
  if (!text) return []
  const lines = text.split('\n')
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop()
  return lines
})

/** The last four lines the prototype’s running card shows (its demo is four). */
const tailLines = computed(() => (running.value ? liveLines.value.slice(-4) : []))

/** How many lines above the tail were folded away; 0 when all four or fewer. */
const earlierCount = computed(() => Math.max(0, liveLines.value.length - tailLines.value.length))

/** The settled streams, a row a line — the engine already trimmed blank edges. */
const stdoutLines = computed(() => (props.card?.stdout ? props.card.stdout.split('\n') : []))
const stderrLines = computed(() => (props.card?.stderr ? props.card.stderr.split('\n') : []))

/** What the middle of the output omitted: the streams’ counts, else the unnamed one. */
const omittedBytes = computed(() => {
  const streams = (props.card?.stdoutOmittedBytes ?? 0) + (props.card?.stderrOmittedBytes ?? 0)
  return streams > 0 ? streams : (props.card?.omittedBytes ?? 0)
})

/**
 * A byte count in the 1024 scale, one decimal only under ten: 412 KB,
 * 3.4 MB. The footnote names what was cut, not a precise measure of it.
 */
function formatOmittedSize(bytes: number): string {
  const units = ['B', 'KB', 'MB', 'GB']
  let value = Math.max(0, bytes)
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  const rounded = value >= 10 ? Math.round(value) : Math.round(value * 10) / 10
  return `${rounded} ${units[unit]}`
}

const copied = ref(false)
let restoreTimer: ReturnType<typeof setTimeout> | null = null

async function copyOutput() {
  const card = props.card
  if (!card) return
  const text = card.stderr ? `${card.stdout}\n${card.stderr}` : card.stdout
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    if (restoreTimer) clearTimeout(restoreTimer)
    restoreTimer = setTimeout(() => {
      copied.value = false
    }, 1200)
  } catch {
    // A clipboard the page may not write is not an error the reader can act on.
  }
}

/** The whole output, opened as a drawer document from the footer’s eye. */
function openDoc() {
  const source = props.docSource
  const retrieveId = props.card?.retrieveId ?? 0
  if (!source?.sessionId || retrieveId <= 0) return
  const firstLine = props.command.split('\n').map((line) => line.trim()).find(Boolean) ?? ''
  emit('open-doc', {
    id: `doc:output:${source.sessionId}:${source.agentId ?? ''}:${retrieveId}`,
    kind: 'output',
    label: t('chat.docTabOutput', { first: firstLine }),
    text: '',
    command: props.command,
    source: { sessionId: source.sessionId, ...(source.agentId ? { agentId: source.agentId } : {}), retrieveId },
  })
}
</script>

<style scoped>
/* The prototype’s .shc .outz family (:337-345) and .foot (:355-356). */
.outz {
  position: relative;
  border-top: 1px solid var(--forebrain-divider);
  padding: 10px 44px 12px 14px;
  font: 12px/1.65 var(--forebrain-mono-font);
  color: var(--forebrain-text-2);
  tab-size: 4;
}

.ol {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ol.err {
  color: #b42318;
}

.none,
.earlier {
  font: 12.5px var(--forebrain-ui-font);
  color: #98a2b3;
}

.earlier {
  font-size: 12px;
  margin-bottom: 2px;
}

.caret {
  display: inline-block;
  width: 0.5em;
  height: 1.1em;
  margin-left: 2px;
  vertical-align: -3px;
  border-radius: 1px;
  background: var(--forebrain-brand-1);
  animation: sh-blink 1.1s steps(1) infinite;
}

@keyframes sh-blink {
  50% { opacity: 0; }
}

@media (prefers-reduced-motion: reduce) {
  .caret {
    animation: none;
  }
}

/* A long result scrolls inside its panel (prototype :345). */
.out-scroll {
  max-height: 360px;
  overflow: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
}

/* Copy: there when the pointer is on the output or the button is focused,
   never in the way (prototype .cp :347-353). */
.cp {
  position: absolute;
  top: 8px;
  right: 8px;
  z-index: 2;
  width: 28px;
  height: 28px;
  border-radius: 7px;
  color: var(--forebrain-muted-text);
  opacity: 0;
  transition: opacity 0.15s, background 0.15s, color 0.15s;
}

.outz:hover .cp,
.cp:focus-visible,
.cp.done {
  opacity: 1;
}

.cp:hover {
  background: rgba(16, 24, 40, 0.06);
  color: var(--forebrain-text);
}

.cp :deep(svg) {
  width: 15px;
  height: 15px;
}

.cp .okk {
  color: var(--forebrain-success);
}

.foot {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
  border-top: 1px solid var(--forebrain-divider);
  font: 12px var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

/* The footer’s eye: the same quiet square the card head’s eye is (136). */
.foot-eye {
  width: 24px;
  height: 24px;
  flex: none;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
  transition: background 0.15s, color 0.15s;
}

.foot-eye:hover {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}
</style>
