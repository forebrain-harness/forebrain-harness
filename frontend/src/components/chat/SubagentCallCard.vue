<template>
  <div class="forebrain-subagent-call-card overflow-hidden rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)]" data-testid="subagent-call-card">
    <div class="flex w-full items-center gap-2.5 px-3 py-2 text-left">
      <Loader2 v-if="running" class="size-4 shrink-0 animate-spin text-[var(--forebrain-brand-1)]" aria-hidden="true" />
      <Bot v-else class="size-4 shrink-0" :class="failed ? 'text-[var(--forebrain-danger)]' : 'text-[var(--forebrain-brand-1)]'" aria-hidden="true" />
      <span class="min-w-0 flex-1 text-[13px] font-semibold leading-snug text-[var(--forebrain-text)]">{{ header }}</span>
      <span v-if="view.headerDuration" class="shrink-0 text-xs tabular-nums text-[var(--forebrain-muted-text)]">{{ view.headerDuration }}</span>
    </div>
    <p v-if="view.callError" class="whitespace-pre-wrap px-3 pb-2 pl-9 text-xs leading-relaxed text-[var(--forebrain-danger)]">{{ view.callError }}</p>
    <div v-if="view.empty" class="border-t border-[var(--forebrain-divider)] px-3 py-2 text-xs text-[var(--forebrain-muted-text)]">
      {{ t('chat.toolNoOutput') }}
    </div>
    <div v-else-if="view.rows.length" class="border-t border-[var(--forebrain-divider)]">
      <component
        :is="row.agentId ? 'button' : 'div'"
        v-for="(row, index) in view.rows"
        :key="`${step.stepId}-task-${index}`"
        :type="row.agentId ? 'button' : undefined"
        :disabled="row.agentId ? undefined : true"
        data-testid="subagent-task-row"
        class="forebrain-subagent-row flex w-full items-start gap-2.5 border-0 border-t border-[var(--forebrain-divider)] bg-transparent px-3 py-2 text-left first:border-t-0"
        :class="row.agentId ? 'cursor-pointer' : 'cursor-default'"
        @click="row.agentId && emit('open', row.agentId)"
      >
        <CheckCircle2 v-if="row.status === 'done'" class="mt-0.5 size-4 shrink-0 text-[var(--forebrain-success)]" aria-hidden="true" />
        <XCircle v-else-if="row.status === 'failed'" class="mt-0.5 size-4 shrink-0 text-[var(--forebrain-danger)]" aria-hidden="true" />
        <Ban v-else-if="row.status === 'cancelled'" class="mt-0.5 size-4 shrink-0 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
        <AlertTriangle v-else-if="row.status === 'skipped'" class="mt-0.5 size-4 shrink-0 text-[var(--forebrain-warning)]" aria-hidden="true" />
        <Loader2 v-else-if="row.status === 'running'" class="mt-0.5 size-4 shrink-0 animate-spin text-[var(--forebrain-brand-1)]" aria-hidden="true" />
        <CircleDashed v-else class="mt-0.5 size-4 shrink-0 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
        <span class="min-w-0 flex-1">
          <span class="flex min-w-0 items-baseline gap-1.5">
            <span class="min-w-0 truncate text-[13px] text-[var(--forebrain-text)]" :title="row.title || t('subagentCard.emptyTask')">{{ row.title || t('subagentCard.emptyTask') }}</span>
            <span v-if="row.clock" data-testid="subagent-row-clock" class="shrink-0 text-xs tabular-nums text-[var(--forebrain-muted-text)]"> · {{ row.clock.label }}</span>
          </span>
          <span v-if="detailOf(row)" class="mt-0.5 block truncate text-xs text-[var(--forebrain-muted-text)]">{{ detailOf(row) }}</span>
          <span v-if="row.error" class="mt-0.5 block whitespace-pre-wrap text-xs leading-relaxed" :class="row.status === 'skipped' ? 'text-[var(--forebrain-warning)]' : 'text-[var(--forebrain-danger)]'">{{ row.error }}</span>
        </span>
        <span v-if="row.agentId" class="shrink-0 self-center whitespace-nowrap text-xs text-[var(--forebrain-brand-1)]">{{ t('subagentCard.open') }}</span>
      </component>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One subagent_* call, drawn the same way for the conversation and for a
 * subagent: a header sentence, one row per task, and the row's own clock —
 * walking while the task runs, final once it ended. The rows that know their
 * agent open its view. Everything the card says comes from the engine's own
 * card facts plus the lifecycle bindings (subagentCallView), never from the
 * call's prompt, its result JSON, or a raw task id.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { AlertTriangle, Ban, Bot, CheckCircle2, CircleDashed, Loader2, XCircle } from 'lucide-vue-next'
import { t, type I18nKey } from '@/locales'
import { subagentCallView, type SubagentCallRowView, type SubagentCallTaskLive, type SubagentToolStep } from '@/composables/useChatStream'

const props = withDefaults(defineProps<{
  step: SubagentToolStep
  /** The live bindings of this call's tasks, from the chat stream. */
  live?: ReadonlyMap<number, SubagentCallTaskLive>
  /** The clock the caller already ticks; the card keeps its own otherwise. */
  now?: number
}>(), {
  live: undefined,
  now: undefined,
})

const emit = defineEmits<{ (e: 'open', agentId: string): void }>()

// The card reads `now` from its caller when one is handed in (the stream's
// shared clock); alone, it ticks one of its own — only while a row is
// actually running, so no settled card keeps an interval alive.
const localNow = ref(Date.now())
let clockTimer: ReturnType<typeof setInterval> | null = null
const effectiveNow = computed(() => props.now ?? localNow.value)
const view = computed(() => subagentCallView(props.step, props.live, effectiveNow.value))
watch(() => view.value.hasLiveClock, (live) => {
  if (live && clockTimer == null) {
    clockTimer = setInterval(() => {
      localNow.value = Date.now()
    }, 1_000)
  } else if (!live && clockTimer != null) {
    clearInterval(clockTimer)
    clockTimer = null
  }
}, { immediate: true })
onBeforeUnmount(() => {
  if (clockTimer != null) clearInterval(clockTimer)
})

const running = computed(() => {
  const key = view.value.headerKey
  return key.endsWith('.running') || key.endsWith('.starting')
})
const failed = computed(() => view.value.headerKey.endsWith('.failed'))

/** The header sentence: the verb's phrase for the phase, and its task count. */
const header = computed(() => {
  const v = view.value
  const parts: string[] = []
  if (v.headerKey === 'subagentCard.canceled') {
    parts.push(t('subagentCard.canceled', { label: v.canceledLabel ?? '' }))
  } else if (v.headerCount) {
    // The count phrase carries its own plural; the header takes it whole.
    const tasks = t(v.headerCount.one ? 'subagentCard.tasksOne' : 'subagentCard.tasksOther', {
      count: v.headerCount.count,
      type: v.headerCount.type ? ` ${v.headerCount.type} ` : '',
    }).replace(/\s+/g, ' ')
    parts.push(t(v.headerKey as I18nKey, { tasks }))
  } else {
    parts.push(t(v.headerKey as I18nKey))
  }
  if (v.breakdown) {
    const join = t('subagentCard.breakdownJoin')
    const tally: string[] = [t('subagentCard.breakdownDone', { count: v.breakdown.done })]
    if (v.breakdown.failed > 0) tally.push(t('subagentCard.breakdownFailed', { count: v.breakdown.failed }))
    if (v.breakdown.cancelled > 0) tally.push(t('subagentCard.breakdownCancelled', { count: v.breakdown.cancelled }))
    if (v.breakdown.skipped > 0) tally.push(t('subagentCard.breakdownSkipped', { count: v.breakdown.skipped }))
    parts.push(tally.join(join))
  }
  return parts.join(' · ')
})

/** A row's one gray line: what its dispatch last did, or what its query saw. */
function detailOf(row: SubagentCallRowView): string {
  if (row.latestTool) {
    const more = row.moreToolUses
      ? ` · ${t(row.moreToolUses === 1 ? 'subagentCard.moreToolUsesOne' : 'subagentCard.moreToolUsesOther', { count: row.moreToolUses })}`
      : ''
    return `${t('subagentCard.latestTool', { label: row.latestTool })}${more}`
  }
  if (row.listType || row.timedOut || row.stopRequested) {
    const parts = [
      row.listType,
      row.timedOut ? t('subagentCard.phrase.waitEnded') : '',
      row.stopRequested ? t('subagentCard.phrase.stopRequested') : '',
    ].filter(Boolean)
    return parts.join(' · ')
  }
  if (row.status === 'waiting') return t('subagentCard.waiting')
  return ''
}
</script>

<style scoped>
.forebrain-subagent-row:hover {
  background: var(--forebrain-bg-alt);
}
.forebrain-subagent-row:focus-visible {
  outline: 2px solid var(--forebrain-brand-1);
  outline-offset: -2px;
}
</style>
