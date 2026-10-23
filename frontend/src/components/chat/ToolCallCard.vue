<template>
  <Tool
    :open="open ?? undefined"
    :default-open="open == null ? openedByDefault : undefined"
    @update:open="emit('update:open', Boolean($event))"
  >
    <ToolHeader
      :title="title"
      :icon="toolIcon"
      :status="toolStatus"
      :status-label="toolStatusLabel"
      single-line
    />
    <ToolContent>
      <!-- The whole invocation, for the calls whose header had to shorten it:
           wrapped, bounded at the prototype's 208px, scrolling, with the bottom
           edge faded to say there is more — and copyable, because a command is
           here to be run again, not just read. -->
      <!-- The copy button is drawn here rather than taken from `code-block`:
           that one reads the code it copies out of a CodeBlock's own context,
           so reusing it would mean rendering this command through CodeBlock's
           Shiki theme — and the design draws it as a plain alt-surface block
           with a bottom fade, not as highlighted code. -->
      <div v-if="fullCommand" class="cmd-full-wrap">
        <pre class="cmd-full">{{ fullCommand }}</pre>
        <button
          type="button"
          class="cmd-copy"
          v-tip:top="t('chat.copyMessage')"
          :aria-label="t('chat.copyCommand')"
          data-testid="tool-cmd-copy"
          @click="copyCommand"
        >
          <Copy class="size-3.5" aria-hidden="true" />
        </button>
      </div>
      <!-- An image step's pixels live behind a URL, never in the step payload:
           the thumbnail fetches them from the workspace raw endpoint, and a
           click opens the full image in a new tab. -->
      <a
        v-if="previewUrl"
        :href="previewUrl"
        v-tip:top="t('chat.toolImageOpen')"
        target="_blank"
        rel="noopener"
        class="tool-media"
      >
        <img
          :src="previewUrl"
          :alt="previewAlt"
          loading="lazy"
          class="max-h-40 w-auto rounded-md border border-border object-contain"
        />
      </a>
      <!-- The thumbnail's facts, mirroring the TUI's ▤ line (width×height,
           encoded size, scaled). Each segment is honest per block kind: a sent
           image reports its encoded bytes, a refused or degraded one the
           on-disk size; unknown segments are omitted rather than shown as 0. -->
      <div
        v-if="previewUrl && imageMetaLine"
        class="tool-media-meta"
      >{{ imageMetaLine }}</div>
      <!-- An answered question is the conversation's own record: the question
           in the agent's words, then each answer under it — never the raw
           result as one paragraph of text. -->
      <AskDoneCard v-if="isAnsweredAsk" :text="String(step.output ?? '')" />
      <!-- A skill card's display_body is the whole user-facing text ("Loaded
           from …" / "Failed to load from …" plus the raw error), so the
           separate error line would repeat what the body already says. -->
      <ToolOutput
        v-else
        :output="step.output ?? ''"
        :error-text="isSkill ? '' : step.error"
      />
    </ToolContent>
  </Tool>
</template>

<script setup lang="ts">
/**
 * One tool call, drawn the same way for the conversation and for a subagent: a
 * call is a call, and the two views had better agree about what it says. The
 * caller owns the open state (the subagent view persists it per transcript, the
 * conversation does not), so the card works both controlled and not.
 */
import { computed, type Component } from 'vue'
import { Copy } from 'lucide-vue-next'
import {
  BookOpen, Brain, ClipboardCheck, FileCode, FilePen, FilePlus, FileText, FolderOpen, Globe,
  ListChecks, Map, MessageSquareText, NotebookPen, NotebookText, ScrollText, Search, Shield,
  Terminal, Wrench,
} from 'lucide-vue-next'
import { Tool, ToolHeader, ToolContent, ToolOutput } from '@repo/elements/tool'
import { t } from '@/locales'
import { forebrainApi, type WorkspaceScope } from '@/lib/api'
import { skillCardTitle, imageBytesLabel, answeredAskEntries, type SubagentToolStep } from '@/composables/useChatStream'
import AskDoneCard from './AskDoneCard.vue'

const props = withDefaults(defineProps<{
  step: SubagentToolStep
  open?: boolean
  defaultOpen?: boolean
  /**
   * The file view an image step's pixels are fetched from. Absent (or empty)
   * leaves the image step's text body showing with no thumbnail — the raw
   * endpoint resolves the root from exactly this scope.
   */
  scope?: WorkspaceScope
}>(), {
  open: undefined,
  defaultOpen: false,
  scope: undefined,
})

const emit = defineEmits<{
  (e: 'update:open', value: boolean): void
}>()

/**
 * The prototype's TOOL_ICON (app-preview.html :3118-3125): each call wears the
 * mark of the tool it invoked, so a column of cards is scannable by shape.
 */
const TOOL_ICONS: Record<string, Component> = {
  read_file: FileText,
  write_file: FilePlus,
  edit_file: FilePen,
  shell: Terminal,
  retrieve_output: ScrollText,
  web_fetch: Globe,
  web_search: Search,
  lsp: FileCode,
  session_todo: ListChecks,
  enter_plan_mode: Map,
  exit_plan_mode: ClipboardCheck,
  intermediate_tool: NotebookPen,
  user_interaction: MessageSquareText,
  request_permissions: Shield,
  memories_list: FolderOpen,
  memories_read: BookOpen,
  memories_search: Brain,
  memories_add_ad_hoc_note: NotebookText,
}

const toolIcon = computed<Component>(() => TOOL_ICONS[String(props.step.toolName ?? '')] ?? Wrench)

/**
 * isSkill marks a skill load: its display_body is the whole user-facing text
 * ("Loaded from …" / "Failed to load from …" plus the raw error), so the
 * separate error line would repeat what the body already says.
 */
const isSkill = computed(() => String(props.step.category ?? '').trim().toLowerCase() === 'skill')

/**
 * The piece of the call the one-line header could not show: a summary longer
 * than 120 characters (or one the engine wrapped itself) is ellipsised out of
 * the header, so it renders verbatim at the top of the body instead. Empty for
 * short summaries — every card does not grow an extra block.
 */
const fullCommand = computed(() => {
  const headline = props.step.header || props.step.summary
  if (headline.length > 120 || headline.includes('\n')) return headline
  return ''
})

/**
 * previewUrl is the image step's pixel URL, built from the same file view every
 * other workspace read uses. Empty for a non-image step, a step with no path,
 * or a caller that passed no scope — the card then shows the text body alone.
 */
const previewUrl = computed(() => {
  if (String(props.step.previewKind ?? '').trim() !== 'image') return ''
  const path = String(props.step.previewPath ?? '').trim()
  if (!path) return ''
  const scope = props.scope ?? {}
  if (!scope.sessionId && !scope.projectId) return ''
  return forebrainApi.workspaceFileRawUrl(scope, path)
})

/**
 * previewAlt names the image for assistive tech and for the moment before it
 * loads: its file name and, when the meta carried them, its pixel size. A
 * block whose header did not parse (or a surface that never learned the size)
 * falls back to the size-free variant rather than announcing 0×0.
 */
const previewAlt = computed(() => {
  const name = String(props.step.previewPath ?? '').trim().split('/').pop() || 'image'
  const width = props.step.previewWidth ?? 0
  const height = props.step.previewHeight ?? 0
  if (!width || !height) return t('chat.toolImageAltNoDims', { name })
  return t('chat.toolImageAlt', { name, width, height })
})

/**
 * imageMetaLine is the muted caption under the thumbnail, aligned with the
 * TUI's "▤ name · WxH · bytes · scaled": every segment that is known appears,
 * every unknown one is omitted, and "scaled" only ever names a downscale the
 * engine actually ran.
 */
const imageMetaLine = computed(() => {
  if (String(props.step.previewKind ?? '').trim() !== 'image') return ''
  const segments: string[] = []
  const width = props.step.previewWidth ?? 0
  const height = props.step.previewHeight ?? 0
  if (width > 0 && height > 0) segments.push(`${width}×${height}`)
  if ((props.step.previewBytes ?? 0) > 0) {
    segments.push(imageBytesLabel(props.step.previewBytes ?? 0))
  }
  if (props.step.previewScaled) segments.push('scaled')
  return segments.join(' · ')
})

/**
 * toolStatus maps the runtime's own status onto the states the prototype names
 * (app-preview.html :3130). A call parked on the user's approval has a state of
 * its own — it is not "running", and the reader is the reason it is not done.
 * A call the user refused is denied; one that never ran — a run that stopped
 * under it, a prompt that was dismissed — is canceled. Neither produced output
 * and neither is an error (the alternative painted them red), but a canceled
 * call must never read as a refusal: a conversation that asks for no approvals
 * refused nothing.
 */
const toolStatus = computed<'running' | 'awaiting' | 'done' | 'denied' | 'canceled' | 'error'>(() => {
  const status = String(props.step.status ?? '').trim().toLowerCase()
  if (status === 'canceled' || status === 'cancelled') return 'canceled'
  if (status === 'denied') return 'denied'
  if (props.step.error || status === 'failed' || status === 'error') return 'error'
  if (status === 'awaiting approval' || status === 'awaiting_approval' || status === 'approval-requested') return 'awaiting'
  if (!status || status === 'running' || status === 'pending') return 'running'
  return 'done'
})

/**
 * isAnsweredAsk marks a question call that was answered: its body is the
 * question and the answer, drawn as the card the prototype gives a settled ask
 * (app-preview.html :3305). A question the user declined, or one still
 * waiting, keeps the ordinary body — there is no answer to show.
 */
const isAnsweredAsk = computed(() => (
  String(props.step.toolName ?? '') === 'user_interaction'
  && toolStatus.value === 'done'
  && answeredAskEntries(String(props.step.output ?? '')).length > 0
))

/**
 * The prototype draws a settled ask open (:3113): the answer is the point of
 * the card, so it is not hidden behind the fold. A live call still opens
 * closed, as every other call's card does.
 */
const openedByDefault = computed(() => props.defaultOpen || isAnsweredAsk.value)

const toolStatusLabel = computed(() => {
  const labels = {
    running: t('chat.toolStatusRunning'),
    awaiting: t('chat.toolStatusAwaiting'),
    done: t('chat.toolStatusDone'),
    denied: t('chat.toolStatusDenied'),
    canceled: t('chat.toolStatusCanceled'),
    error: t('chat.toolStatusError'),
  }
  return labels[toolStatus.value]
})

async function copyCommand() {
  const text = fullCommand.value
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // A clipboard the page may not write is not an error the reader can act
    // on: the command stays selectable in place.
  }
}

/**
 * The title of a skill load comes from the structured identity via
 * skillCardTitle, never from the argument summary: the invocation carried no
 * arguments worth printing, and "review-agent {"explicit":true}" is exactly
 * the noise this card exists to prevent.
 */
const title = computed(() => {
  // The engine's own header — the terminal's words for the call ("Read
  // plans/100.md lines 1-96", "Ran ls", "Notes") — is the head whenever the
  // gateway sent it. Everything below is the fallback for an older payload.
  if (props.step.header) return props.step.header
  // A question call's head says the one thing it did — that the agent asked —
  // and leaves the questions to the card's own tab row (prototype :3299).
  if (String(props.step.toolName ?? '') === 'user_interaction') {
    return toolStatus.value === 'running' || toolStatus.value === 'awaiting'
      ? t('chat.askRunning')
      : t('chat.askAsked')
  }
  const skill = skillCardTitle(props.step)
  if (skill !== undefined) return skill
  if (String(props.step.status ?? '').trim().toLowerCase().startsWith('cancel')) {
    // A canceled call never ran: its state word says so, and the head names
    // what it would have done (or, with nothing to name, that it was canceled).
    return props.step.summary || t('chat.toolCanceled')
  }
  return props.step.summary || props.step.toolName
})
</script>

<style scoped>
/* The prototype's .tool pre.cmd-full: full width under the header, the alt
   surface, the same 208px bound as the body, and a fade at the bottom edge
   instead of a line cut in half. */
.cmd-full-wrap {
  position: relative;
  background: var(--forebrain-bg-alt);
  border-bottom: 1px solid var(--forebrain-divider);
}

.cmd-full {
  margin: 0;
  padding: 8px 11px;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  line-height: 1.6;
  color: var(--forebrain-text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 208px;
  overflow: auto;
  mask-image: linear-gradient(#000 calc(100% - 22px), transparent);
}

.cmd-copy {
  position: absolute;
  top: 6px;
  right: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  cursor: pointer;
}

.cmd-copy:hover {
  color: var(--forebrain-text);
  border-color: var(--forebrain-divider-strong);
}

.tool-media {
  display: block;
  width: fit-content;
  margin: 9px 11px 0;
}

.tool-media-meta {
  margin: 6px 11px 0;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
</style>
