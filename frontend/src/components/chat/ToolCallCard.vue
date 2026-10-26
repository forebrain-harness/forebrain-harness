<template>
  <Tool
    ref="cardRoot"
    class="tool-card"
    :class="{ 'tool-card--on-line': onLine, 'tool-card--waiting': isWaiting }"
    :open="effectiveOpen ?? undefined"
    :default-open="open == null ? openedByDefault : undefined"
    @update:open="emit('update:open', Boolean($event))"
  >
    <ToolHeader
      :title="title"
      :parts="step.headerParts"
      :lang="lang"
      :exit-code="exitCode"
      :status="toolStatus"
      :status-label="toolStatusLabel"
      :status-on-node="onLine"
    >
      <template #after-title>
        <!-- The eye: the head's one line ended in an ellipsis, or the command
             has lines of its own — the whole text opens in the drawer. -->
        <Button
          v-if="showEye"
          type="button"
          variant="ghost"
          size="icon"
          class="tool-eye"
          :aria-label="t('chat.showAll')"
          v-tip:top="t('chat.showAll')"
          data-testid="tool-show-all"
          @click.stop="openDoc"
        >
          <Eye class="size-[15px]" aria-hidden="true" />
        </Button>
      </template>
    </ToolHeader>
    <ToolContent class="tool-card-body">
      <!-- While the call waits on the reader, what the decision turns on is
           the panel (plan 138): the gate's facts replace the result the call
           has not produced yet. -->
      <ToolGateFacts
        v-if="isWaiting && pendingAction"
        :action="pendingAction"
        :gate="approvalGateOf(pendingAction)"
        :requester="gateRequester"
        :can-open-agent="canOpenAgent"
        @open-agent="(agentId) => emit('open-agent', agentId)"
      />
      <!-- A settled refusal is one sentence, then the words the user gave
           (prototype ⑬⑭, .tool-b .say / .said): the call never ran, so there
           is no result to draw — only what happened to it. -->
      <div v-else-if="isRefused" class="tool-refused" data-testid="tool-refused">
        <p class="say">{{ refusalSentence }}</p>
        <p v-if="refusalWords" class="said">
          <CornerDownRight class="size-3.5 shrink-0" aria-hidden="true" />
          <span>{{ refusalWords }}</span>
        </p>
      </div>
      <!-- An image step's pixels live behind a URL, never in the step payload:
           the thumbnail fetches them from the workspace raw endpoint, and a
           click opens the full image in a new tab. -->
      <template v-else-if="previewUrl">
        <a
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
             encoded size, scaled). Each segment is honest per block kind: a
             sent image reports its encoded bytes, a refused or degraded one
             the on-disk size; unknown segments are omitted rather than shown
             as 0. -->
        <div v-if="imageMetaLine" class="tool-media-meta">{{ imageMetaLine }}</div>
      </template>
      <!-- An answered question is the conversation's own record: the question
           in the agent's words, then each answer under it — never the raw
           result as one paragraph of text. -->
      <AskDoneCard v-else-if="isAnsweredAsk" :text="String(step.output ?? '')" />
      <!-- A shell call's body is its result only (plan 137): the streams, the
           files, the notes and the truncation footer, from the gateway's own
           card facts — never the Markdown body the card once rendered. While
           it runs the same panel carries the tail of what it has printed. -->
      <ShellCardBody
        v-else-if="isShellBody"
        :card="step.shellCard"
        :status="toolStatus"
        :live-output="step.output ?? ''"
        :command="command"
        :doc-source="docSource"
        @open-doc="emit('open-doc', $event)"
      />
      <!-- A file tool's card opens with its turn diff (plan 137): the same
           unified rows every diff draws, a new file as plain code; what
           follows the diff section (diagnostics and the like) renders as it
           always has. -->
      <template v-else-if="step.fileDiff">
        <DiffBlock
          v-if="step.fileDiff.unifiedDiff"
          :rows="parseUnifiedDiff(step.fileDiff.unifiedDiff)"
          :plain="fileDiffIsNew"
        />
        <ToolOutput :output="step.fileDiff.rest ?? ''" :error-text="''" />
      </template>
      <!-- A skill card's display_body is the whole user-facing text ("Loaded
           from …" / "Failed to load from …" plus the raw error), so the
           separate error line would repeat what the body already says. -->
      <ToolOutput
        v-else
        :output="step.output ?? ''"
        :error-text="isSkill ? '' : step.error"
      />
    </ToolContent>
    <!-- A call waiting on the reader (plan 138): what the decision turns on is
         the panel above, and the question with its answers closes the card on
         its own tinted footer — one card, one node, no second box at the
         thread's end. -->
    <ToolContent v-if="isWaiting && gateFooter" class="tool-card-gate">
      <ToolGateFooter
        :action="gateFooter.action"
        :options="gateFooter.options"
        :decision-label="gateFooter.decisionLabel"
        :submit="gateFooter.submit"
        :refuse-with-words="gateFooter.refuseWithWords"
        :refuse-and-stop="gateFooter.refuseAndStop"
        :submitting="gateFooter.submitting"
        :error="gateFooter.error"
      />
    </ToolContent>
  </Tool>
</template>

<script lang="ts">
/**
 * statusOf is the card's state in the prototype's own vocabulary
 * (app-preview.html :3130), as a pure function so the view that draws the
 * card on the activity line can use the same state as the card's node.
 */
export type ToolCardStatus = 'running' | 'awaiting' | 'done' | 'denied' | 'canceled' | 'error'

export function statusOf(step: { status?: string; error?: string }): ToolCardStatus {
  const status = String(step.status ?? '').trim().toLowerCase()
  if (status === 'canceled' || status === 'cancelled') return 'canceled'
  if (status === 'denied') return 'denied'
  if (step.error || status === 'failed' || status === 'error') return 'error'
  if (status === 'awaiting approval' || status === 'awaiting_approval' || status === 'approval-requested') return 'awaiting'
  if (!status || status === 'running' || status === 'pending') return 'running'
  return 'done'
}
</script>

<script setup lang="ts">
/**
 * One tool call, drawn the same way for the conversation and for a subagent: a
 * call is a call, and the two views had better agree about what it says. The
 * caller owns the open state (the subagent view persists it per transcript, the
 * conversation does not), so the card works both controlled and not.
 *
 * The head is one line (owner 2026-10-25): the engine's three header parts —
 * the action grey, the object dark in mono, the suffix grey — with the
 * duration on the right and the fold at the end. The body holds the result
 * only; the command is the title, and the whole of either opens in the
 * drawer from the title's eye when the one line could not hold it.
 */
import { computed, nextTick, onMounted, ref, watch, type ComponentPublicInstance } from 'vue'
import { CornerDownRight, Eye } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Tool, ToolHeader, ToolContent, ToolOutput } from '@repo/elements/tool'
import { t, useI18n } from '@/locales'
import { forebrainApi, type WorkspaceScope } from '@/lib/api'
import { skillCardTitle, imageBytesLabel, answeredAskEntries, type SubagentToolStep } from '@/composables/useChatStream'
import { injectPendingActions } from '@/composables/providePendingActions'
import type { ActionRow } from '@/composables/usePendingActions'
import { approvalGateOf } from '@/lib/approvalGates'
import type { ApprovalDecisionOption } from '@/lib/approvalSuggestions'
import { useTruncated } from '@/composables/useTruncated'
import { docTabFor, type DocTab } from '@/composables/useDocTabs'
import { parseUnifiedDiff } from '@/lib/unifiedDiff'
import AskDoneCard from './AskDoneCard.vue'
import DiffBlock from './DiffBlock.vue'
import ShellCardBody from './ShellCardBody.vue'
import ToolGateFacts from './ToolGateFacts.vue'
import ToolGateFooter from './ToolGateFooter.vue'

const props = withDefaults(defineProps<{
  step: SubagentToolStep
  open?: boolean
  defaultOpen?: boolean
  /**
   * Whether the card hangs on the activity line (the default): the head then
   * carries no box of its own and the state lives on the line's node. A
   * caller drawing the card on its own passes false and gets the boxed head
   * with the state in it.
   */
  onLine?: boolean
  /**
   * The request this call is parked on, when it is waiting on the reader
   * (plan 138): the panel becomes the gate's facts and the footer below holds
   * the question and its answers. Null on every settled call.
   */
  pendingAction?: ActionRow | null
  /**
   * The file view an image step's pixels are fetched from. Absent (or empty)
   * leaves the image step's text body showing with no thumbnail — the raw
   * endpoint resolves the root from exactly this scope.
   */
  scope?: WorkspaceScope
  /**
   * The subagent whose view this card sits in, when it sits in one: the
   * drawer's output page reads a subagent's spool through its own route.
   */
  agentId?: string
}>(), {
  open: undefined,
  defaultOpen: false,
  onLine: true,
  pendingAction: null,
  scope: undefined,
  agentId: undefined,
})

const emit = defineEmits<{
  (e: 'update:open', value: boolean): void
  /** The whole text the head could not hold, opened as a drawer document. */
  (e: 'open-doc', tab: DocTab): void
  /** A subagent-raised gate's requester line, opening that subagent's view. */
  (e: 'open-agent', agentId: string): void
}>()

const { locale } = useI18n()
const lang = computed(() => (locale.value === 'zh' ? 'zh' : 'en') as 'zh' | 'en')

/**
 * isSkill marks a skill load: its display_body is the whole user-facing text
 * ("Loaded from …" / "Failed to load from …" plus the raw error), so the
 * separate error line would repeat what the body already says.
 */
const isSkill = computed(() => String(props.step.category ?? '').trim().toLowerCase() === 'skill')

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
const toolStatus = computed(() => statusOf(props.step))

/**
 * A call waiting on the reader keeps its card open — the facts and the
 * question are the card's whole point while it waits, so the fold cannot hide
 * them (plan 138). The parent's own open state stands everywhere else.
 */
const isWaiting = computed(() => Boolean(props.pendingAction))

const effectiveOpen = computed(() => (isWaiting.value ? true : props.open))

/** The conversation's own pending-actions store, when this tree has one. */
const pendingStore = injectPendingActions()

/** Who is blocked on this gate, when a subagent raised it. */
const gateRequester = computed(() => (
  props.pendingAction && pendingStore ? pendingStore.requesterLabel(props.pendingAction) : ''
))

/** Whether the requester line can open that subagent's own view. */
const canOpenAgent = computed(() => Boolean(pendingStore))

/**
 * The footer's plumbing, read from the conversation's shared store: the
 * engine's own options, their family-true labels, and the two refusals. A
 * card drawn outside a provider (a harness, a story) has no store to read and
 * no footer — it keeps the ordinary call it always was.
 */
const gateFooter = computed(() => {
  const action = props.pendingAction
  const store = pendingStore
  if (!action || !store) return null
  return {
    action,
    options: store.approvalDecisionOptions(action),
    decisionLabel: (option: ApprovalDecisionOption) => store.approvalDecisionLabel(option, action),
    submit: (option: ApprovalDecisionOption) => void store.submitApprovalDecision(action, option),
    refuseWithWords: (a: ActionRow, words: string) => store.refuseWithWords(a, words),
    refuseAndStop: (a: ActionRow) => store.refuseAndStop(a),
    submitting: Boolean(store.submitting.value[action.id]),
    error: store.errors.value[action.id] ?? '',
  }
})

/**
 * A settled refusal draws two lines: the one sentence that says what happened
 * to this call, then — when the user gave their words — those words under the
 * arrow (prototype ⑬⑭). The engine's own display body stays untranslated
 * model-facing text; the sentence here is the reader's.
 */
const isRefused = computed(() => toolStatus.value === 'denied')

const refusalWords = computed(() => String(props.step.denyFeedback ?? '').trim())

const refusalSentence = computed(() => {
  const name = String(props.step.toolName ?? '').trim().toLowerCase()
  if (name === 'shell') return t('chat.refusedShell')
  if (name === 'write_file' || name === 'edit_file' || name === 'apply_patch') return t('chat.refusedFile')
  return t('chat.refusedCall')
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

/** The full shell command, when the engine recorded the call's input. */
const command = computed(() => String(props.step.input?.command ?? '').trim())

/**
 * A shell call draws the shell body when it carries the gateway's card facts
 * or is still running (its tail streams in place of them). A settled call
 * with no facts is pre-135 data: the ordinary Markdown body stands in.
 */
const isShellBody = computed(() => (
  String(props.step.toolName ?? '').trim().toLowerCase() === 'shell'
  && (Boolean(props.step.shellCard) || toolStatus.value === 'running')
))

/** A non-zero exit rides the head left of the duration; 0 and absent are the same. */
const exitCode = computed(() => props.step.shellCard?.exitCode ?? null)

/** Where the drawer's output page reads a truncated call's whole output from. */
const docSource = computed(() => {
  const sessionId = String(props.scope?.sessionId ?? '').trim()
  if (!sessionId) return undefined
  const agentId = String(props.agentId ?? '').trim()
  return { sessionId, ...(agentId ? { agentId } : {}) }
})

/**
 * A write_file whose head says Created opened a file that did not exist: its
 * diff is the file's whole body, drawn as plain code rather than a wall of
 * green (the same rule the shell card's changed files follow).
 */
const fileDiffIsNew = computed(() => (
  String(props.step.toolName ?? '').trim().toLowerCase() === 'write_file'
  && String(props.step.headerParts?.action ?? '').trim().toLowerCase() === 'created'
))

/**
 * The eye appears when the head's one line could not hold its text, or when
 * the command has lines of its own — a multi-line command reads whole in the
 * drawer, never squeezed onto one line (owner 2026-10-25).
 */
const multiLineCommand = computed(() => command.value.includes('\n'))

// The title area is measured where the head drew it; the card re-measures
// whenever the head's words change, because a resize alone will not fire.
const cardRoot = ref<ComponentPublicInstance | HTMLElement | null>(null)
const titleArea = ref<HTMLElement | null>(null)
const { truncated, recheck } = useTruncated(titleArea)

function resolveTitleArea() {
  const raw = cardRoot.value
  const el = raw instanceof HTMLElement ? raw : (raw?.$el as HTMLElement | undefined)
  titleArea.value = el?.querySelector('.ttl') ?? null
}
// The room the head has depends on the fold and on whether the state sits on
// the node; the words themselves decide how much of it the title needs.
const headInputs = computed(() => [props.step.headerParts, props.step.header, title.value, props.onLine, lang.value])

onMounted(() => nextTick(() => {
  resolveTitleArea()
  recheck()
}))
watch(headInputs, () => nextTick(() => {
  resolveTitleArea()
  recheck()
}))

const showEye = computed(() => truncated.value || multiLineCommand.value)

/** Opens the whole text — the command as it ran, else the title — as a document tab. */
function openDoc() {
  if (command.value) {
    emit('open-doc', docTabFor('command', command.value, { command: command.value }))
    return
  }
  emit('open-doc', docTabFor('title', title.value))
}
</script>

<style scoped>
/* On the activity line the row carries no box (prototype .al-run > .tool,
   :423): the head is bare, the result one panel under it. */
.tool-card--on-line {
  border: 0;
  border-radius: 0;
  background: transparent;
  overflow: visible;
}

.tool-card--on-line :deep(.tool-head) {
  gap: 10px;
  min-width: 0;
  padding: 6px 0;
  background: transparent;
  border-bottom: 0;
}

/* The action word goes quieter still on the line (:434). */
.tool-card--on-line :deep(.tool-head .verb) {
  color: #98a2b3;
}

/* The result panel: one bordered, 10px-corner panel under the bare head
   (prototype .al-run > .tool > .tool-h + *, :435-438). A gate footer follows
   as a second panel of the same frame, so the top panel keeps no bottom edge
   of its own. */
.tool-card--on-line :deep(.tool-card-body) {
  margin-top: 2px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px 10px 0 0;
  background: var(--forebrain-page-bg);
}

.tool-card--on-line :deep(.tool-card-body:last-child) {
  border-radius: 10px;
}

/* The gate's own panel: the frame's sides and bottom; its top edge is the
   footer's own brand border (prototype .tool-b + .gate-ask). */
.tool-card--on-line :deep(.tool-card-gate) {
  border-left: 1px solid var(--forebrain-divider);
  border-right: 1px solid var(--forebrain-divider);
  border-bottom: 1px solid var(--forebrain-divider);
  border-radius: 0 0 10px 10px;
}

/* The waiting frame is the one place a thread asks for a hand, so it is the
   one place louder (prototype .tool.waiting, :675): both panels take the
   brand's border colour; a card drawn off the line colours its own frame. */
.tool-card--waiting :deep(.tool-card-body),
.tool-card--waiting :deep(.tool-card-gate) {
  border-color: var(--forebrain-brand-border);
}

.tool-card--waiting {
  border-color: var(--forebrain-brand-border);
}

/* A settled refusal: the sentence in the body's own voice, the user's words
   under the arrow (prototype .tool-b .say / .said, :704-706). */
.tool-refused {
  display: grid;
  gap: 6px;
  font-size: 13px;
  line-height: 1.6;
}

.tool-refused .say {
  margin: 0;
  color: var(--forebrain-text-2);
}

.tool-refused .said {
  display: flex;
  gap: 7px;
  align-items: flex-start;
  margin: 0;
  color: var(--forebrain-text);
}

.tool-refused .said svg.lucide {
  width: 14px;
  height: 14px;
  margin-top: 4px;
  color: var(--forebrain-muted-text);
}

/* The eye (prototype .app .tt-all, :454): a quiet 24px square, the brand's
   tint only while it is pointed at. */
.tool-eye {
  width: 24px;
  height: 24px;
  flex: none;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
  transition: background 0.15s, color 0.15s;
}

.tool-eye:hover {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
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
