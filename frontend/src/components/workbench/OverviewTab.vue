<template>
  <div class="bench-tab">
    <!-- 1. Running agents: the card is always drawn — an empty one says so,
         because a card that vanishes leaves the reader wondering whether the
         panel failed to load (prototype :4196). -->
    <AgentRosterPanel
      :title="t('agents.liveTitle')"
      :records="records"
      :loading="loading"
      :error="error"
      @view="(row) => emit('view', row)"
      @cancel="(row) => emit('cancel', row)"
      @cancel-all="() => emit('cancel-all')"
    />

    <!-- 2. To-dos. -->
    <div v-if="todos.length" class="bench-card">
      <div class="bench-card-head">
        <span class="bench-card-title"><ListChecks class="size-3.5" aria-hidden="true" />{{ t('chat.benchTodos') }}</span>
        <span class="bench-sub">{{ doneTodos }}/{{ todos.length }}</span>
      </div>
      <ul class="bench-todos">
        <li v-for="todo in todos" :key="todo.id" class="bench-todo" :class="`bench-todo--${todoTone(todo.status)}`">
          <component :is="todoIcon(todo.status)" class="size-3.5" aria-hidden="true" />
          <span>{{ todo.content }}</span>
        </li>
      </ul>
    </div>

    <!-- 3. Answers: every round of questions this conversation asked and
         answered, newest first, one round open at a time. -->
    <div v-if="asks.length" class="bench-card" data-testid="bench-answers">
      <div class="bench-card-head">
        <span class="bench-card-title">
          <MessageSquareText class="size-3.5" aria-hidden="true" />{{ t('chat.benchAnswers') }}
        </span>
        <span class="bench-sub">{{ t('chat.benchAsksCount', { n: asks.length }) }}</span>
      </div>
      <div class="bench-hist">
        <div v-for="ask in asks" :key="ask.id" class="bench-hist-item">
          <button
            type="button"
            class="bench-hist-row"
            :aria-expanded="expanding(openAsk, asks[0]?.id ?? '') === ask.id"
            :data-testid="`bench-ask-${ask.id}`"
            @click="toggleAsk(ask.id)"
          >
            <span class="bench-hist-time">{{ formatClock(ask.at) }}</span>
            <span class="bench-hist-title">{{ ask.questions[0]?.prompt ?? '' }}</span>
            <span class="bench-hist-side">{{ t('chat.benchAskQuestions', { n: ask.questions.length }) }}</span>
          </button>
          <div
            v-if="expanding(openAsk, asks[0]?.id ?? '') === ask.id"
            class="bench-hist-body bench-qas"
            data-testid="bench-ask-body"
          >
            <div v-for="question in ask.questions" :key="question.header" class="bench-qa">
              <span class="bench-qa-q">{{ question.prompt }}</span>
              <span v-for="(answer, idx) in question.answers" :key="idx" class="bench-qa-a">→ {{ answer }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 4. Plan: every plan file this conversation wrote, newest first. The
         workbench row carries the file's own checklist progress; the document
         itself opens in the drawer, rendered. -->
    <div v-if="plans.length" class="bench-card" data-testid="bench-plans">
      <div class="bench-card-head">
        <span class="bench-card-title"><Map class="size-3.5" aria-hidden="true" />{{ t('chat.benchPlan') }}</span>
        <span class="bench-sub">{{ t('chat.benchPlansCount', { n: plans.length }) }}</span>
      </div>
      <div class="bench-hist">
        <div v-for="plan in plans" :key="plan.name" class="bench-hist-item">
          <button
            type="button"
            class="bench-hist-row"
            :aria-expanded="expanding(openPlan, plans[0]?.name ?? '') === plan.name"
            :data-testid="`bench-plan-${plan.name}`"
            @click="togglePlan(plan.name)"
          >
            <span class="bench-hist-time">{{ formatClock(plan.updatedAt) }}</span>
            <span class="bench-hist-title">{{ planTitle(plan.markdown) || plan.name }}</span>
            <span v-if="planProgress(plan.markdown).total" class="bench-hist-side">
              <SquareCheck class="size-3.5" aria-hidden="true" />
              {{ t('chat.benchPlanProgress', planProgress(plan.markdown)) }}
            </span>
          </button>
          <div
            v-if="expanding(openPlan, plans[0]?.name ?? '') === plan.name"
            class="bench-hist-body"
            data-testid="bench-plan-body"
          >
            <div class="bench-plan">
              <b>{{ planTitle(plan.markdown) || plan.name }}</b>
              <ol v-if="planPointsOf(plan.markdown).length">
                <li v-for="(point, idx) in planPointsOf(plan.markdown)" :key="idx">{{ point }}</li>
              </ol>
            </div>
            <button type="button" class="bench-link bench-plan-open" @click="emit('open-plan', plan)">
              {{ t('chat.benchPlanView') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- 5. MCP servers. -->
    <div v-if="sessionId" class="bench-card">
      <div class="bench-card-head">
        <span class="bench-card-title"><Plug class="size-3.5" aria-hidden="true" />{{ t('chat.benchMcp') }}</span>
        <span class="bench-sub">{{ t('chat.benchMcpOnStart') }}</span>
      </div>
      <template v-if="mcpServers.length">
        <div v-for="server in mcpServers" :key="server.name" class="bench-kvs">
          <span class="k">{{ server.name }}</span>
          <span class="bench-badge" :class="mcpBadgeClass(server.connStatus)">{{ mcpLabel(server) }}</span>
        </div>
      </template>
      <p v-else class="bench-hint">{{ t('chat.benchContextEmpty') }}</p>
    </div>

    <!-- 6. Usage. -->
    <div v-if="sessionId" class="bench-card">
      <div class="bench-card-head">
        <span class="bench-card-title"><Receipt class="size-3.5" aria-hidden="true" />{{ t('chat.benchUsage') }}</span>
        <span class="bench-sub">{{ t('chat.benchUsageCumulative') }}</span>
      </div>
      <div class="bench-kvs">
        <span class="k">{{ t('chat.benchUsageTokens') }}</span>
        <span class="v">{{ t('chat.benchUsageTokensValue', { input: compact(cost?.inputTokens), output: compact(cost?.outputTokens) }) }}</span>
      </div>
      <div class="bench-kvs">
        <span class="k">{{ t('chat.benchUsageRequests') }}</span>
        <span class="v">{{ t('chat.benchUsageRequestsValue', { count: cost?.requests ?? 0 }) }}</span>
      </div>
      <div v-if="cost?.outputTokensPerSecond" class="bench-kvs">
        <span class="k">{{ t('chat.benchUsageRate') }}</span>
        <span class="v">{{ t('chat.benchUsageRateValue', { rate: cost.outputTokensPerSecond.toFixed(1) }) }}</span>
      </div>
      <div class="bench-kvs">
        <span class="k">{{ t('chat.benchUsageCache') }}</span>
        <span class="v">{{ cacheLabel }}</span>
      </div>
      <div class="bench-meter" role="img" :aria-label="cacheLabel">
        <i :style="{ width: `${cachePercent}%`, background: 'var(--forebrain-success)' }" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The workbench's overview tab: what is alive right now (running agents),
 * what the conversation has done (to-dos, the rounds it was asked, the plans
 * it wrote), what it may use (MCP servers) and what it has spent (usage).
 * Each card appears only when it has something to say — a conversation with no
 * running agents draws no card.
 *
 * A long conversation answers several rounds and writes several plans, so both
 * are histories rather than snapshots: one row per round or file, newest
 * first, one of them open at a time. A plan's row carries the file's own
 * checklist progress, and opening the document itself goes to the drawer's
 * plan tab, where Markdown is rendered rather than printed.
 */
import { computed, ref, watch } from 'vue'
import { CircleDot, ListChecks, Map, MessageSquareText, Plug, Receipt, Square, SquareCheck } from 'lucide-vue-next'

import AgentRosterPanel from '@/components/AgentRosterPanel.vue'
import {
  getErrorMessage,
  forebrainApi,
  type AgentRosterRow,
  type McpServerRecord,
  type SessionCostSummary,
  type WorkspaceScope,
} from '@/lib/api'
import { compactTokenCount } from '@/lib/contextDebug'
import { planProgress, planTitle } from '@/lib/planProgress'
import { formatClock } from '@/lib/time'
import { useAnsweredAsks } from '@/composables/useAnsweredAsks'
import { useSessionPlans, type SessionPlan } from '@/composables/useSessionPlans'
import { useI18n } from '@/locales'

const props = defineProps<{
  scope: WorkspaceScope
  records: AgentRosterRow[]
  loading: boolean
  error: string
}>()
const emit = defineEmits<{
  (e: 'view', row: AgentRosterRow): void
  (e: 'cancel', row: AgentRosterRow): void
  (e: 'cancel-all'): void
  /** 查看全文: the plan's document belongs in the drawer's plan tab, rendered. */
  (e: 'open-plan', plan: SessionPlan): void
}>()

const { t } = useI18n()

const sessionId = computed(() => String(props.scope.sessionId ?? '').trim())

const todos = ref<{ id: string; content: string; status: string }[]>([])
const mcpServers = ref<McpServerRecord[]>([])
const cost = ref<SessionCostSummary | null>(null)

// Both histories re-read on the same signal that refreshes the rest of the
// tab, so a round answered or a plan written while the workbench is open shows
// up without a reload.
const { asks } = useAnsweredAsks(sessionId, () => props.loading)
const { plans } = useSessionPlans(sessionId, () => props.loading)

/** Which row of each history is open. null means the newest one, so a reader
 *  who just opened the tab sees the latest round rather than nothing; '' means
 *  the reader closed them all. */
const openAsk = ref<string | null>(null)
const openPlan = ref<string | null>(null)

/** The row of a card that is expanded. */
function expanding(chosen: string | null, newestId: string): string {
  return chosen === null ? newestId : chosen
}

/** Opening a row closes the rest of its family, and a second click closes it. */
function toggleAsk(id: string) {
  openAsk.value = expanding(openAsk.value, asks.value[0]?.id ?? '') === id ? '' : id
}
function togglePlan(name: string) {
  openPlan.value = expanding(openPlan.value, plans.value[0]?.name ?? '') === name ? '' : name
}

const doneTodos = computed(() => todos.value.filter((todo) => todo.status === 'done' || todo.status === 'completed').length)
const cachePercent = computed(() => Math.max(0, Math.min(100, cost.value?.cacheHitPercent ?? 0)))
const cacheLabel = computed(() => `${Math.round(cachePercent.value)}%`)

/** A plan document's first bullet points, read off its Markdown. */
function planPointsOf(markdown: string): string[] {
  return String(markdown ?? '')
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => /^([-*]|\d+\.)\s+\S/.test(line))
    .slice(0, 6)
    .map((line) => line.replace(/^([-*]|\d+\.)\s+/, ''))
}

function todoTone(status: string): 'done' | 'doing' | 'wait' {
  const normalized = String(status ?? '').toLowerCase()
  if (normalized === 'done' || normalized === 'completed') return 'done'
  if (normalized === 'doing' || normalized === 'in_progress' || normalized === 'in-progress') return 'doing'
  return 'wait'
}
function todoIcon(status: string) {
  const tone = todoTone(status)
  if (tone === 'done') return SquareCheck
  if (tone === 'doing') return CircleDot
  return Square
}

function mcpBadgeClass(status?: string): string {
  const normalized = String(status ?? '').toLowerCase()
  if (normalized === 'connected') return 'bench-badge--ok'
  if (normalized === 'connecting') return 'bench-badge--brand'
  return 'bench-badge--warn'
}
function mcpLabel(server: McpServerRecord): string {
  const status = String(server.connStatus ?? '').toLowerCase()
  if (status === 'connected') return t('chat.benchMcpConnected', { count: server.toolCount ?? 0 })
  if (status === 'connecting') return t('chat.benchMcpConnecting')
  // Three different things, three different sentences: a server that never
  // answered in time, one that failed to start, and one whose start was
  // cancelled.
  if (status === 'timeout') return t('chat.benchMcpTimeout')
  if (status === 'error') return t('chat.benchMcpError')
  if (status === 'cancelled' || status === 'canceled') return t('chat.benchMcpCancelled')
  return t('chat.benchMcpUnknown')
}
function compact(value?: number) {
  return compactTokenCount(value)
}

let generation = 0
async function load() {
  const sid = sessionId.value
  if (!sid) {
    todos.value = []
    mcpServers.value = []
    cost.value = null
    return
  }
  const gen = ++generation
  const settle = async <T>(task: Promise<T>, assign: (value: T) => void) => {
    try {
      const value = await task
      if (gen === generation) assign(value)
    } catch (cause) {
      if (gen === generation) void getErrorMessage(cause)
    }
  }
  await Promise.all([
    settle(forebrainApi.sessionTodos(sid).then((res) => res.items ?? []), (items) => { todos.value = items }),
    settle(forebrainApi.mcpServersWithStatus({ sessionId: sid }).then((res) => res.servers ?? []), (servers) => { mcpServers.value = servers }),
    settle(forebrainApi.sessionCostSummary(sid), (summary) => { cost.value = summary }),
  ])
}

watch([sessionId, () => props.loading], () => void load(), { immediate: true })
</script>

<style scoped>
.bench-tab {
  display: grid;
  gap: 12px;
  align-content: start;
}
.bench-card {
  display: grid;
  gap: 8px;
  padding: 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
}
.bench-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.bench-card-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.bench-sub {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.bench-link {
  font-size: 12px;
  color: var(--forebrain-brand-1);
  background: transparent;
  border: 0;
  padding: 0;
}
.bench-link:hover {
  text-decoration: underline;
}
.bench-todos {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 6px;
}
.bench-todo {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--forebrain-text);
}
.bench-todo svg {
  margin-top: 2px;
  flex: none;
}
.bench-todo--done {
  color: var(--forebrain-muted-text);
}
.bench-todo--done span {
  text-decoration: line-through;
}
.bench-todo--done svg {
  color: var(--forebrain-success);
}
.bench-todo--doing svg {
  color: var(--forebrain-brand-1);
}
.bench-todo--doing span {
  font-weight: 500;
}
.bench-todo--wait svg {
  color: var(--forebrain-muted-text);
}
.bench-plan {
  font-size: 12.5px;
  display: grid;
  gap: 4px;
}
.bench-plan b {
  font-weight: 600;
  color: var(--forebrain-text);
}
.bench-plan ol {
  margin: 0;
  padding-left: 18px;
  color: var(--forebrain-text-2);
  display: grid;
  gap: 2px;
}
/* One history card: a row per round or file, newest first, the one in front
   expanded under its own row. */
.bench-hist {
  display: grid;
  gap: 2px;
}
.bench-hist-item {
  display: grid;
  gap: 4px;
}
.bench-hist-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 3px 4px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  text-align: left;
  font-size: 12.5px;
  color: var(--forebrain-text);
  cursor: pointer;
}
.bench-hist-row:hover {
  background: var(--forebrain-button-alt-bg);
}
.bench-hist-time {
  flex: none;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  font-variant-numeric: tabular-nums;
}
.bench-hist-title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bench-hist-side {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex: none;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  font-variant-numeric: tabular-nums;
}
.bench-hist-side svg {
  color: var(--forebrain-muted-text);
}
.bench-hist-body {
  display: grid;
  gap: 6px;
  justify-items: start;
  padding: 2px 4px 6px;
  font-size: 12.5px;
}
/* One answered question: the question on its own line, then each answer under
   it — the same shape the ask card asked it in, never reflowed into prose. */
.bench-qas {
  gap: 8px;
}
.bench-qa {
  display: grid;
  gap: 2px;
}
.bench-qa-q {
  color: var(--forebrain-text);
  font-weight: 500;
}
.bench-qa-a {
  padding-left: 10px;
  color: var(--forebrain-text-2);
}
.bench-plan-open {
  justify-self: end;
}
.bench-kvs {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 12px;
}
.bench-kvs .k {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--forebrain-text-2);
}
.bench-kvs .v {
  flex: none;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  font-variant-numeric: tabular-nums;
}
.bench-badge {
  font-size: 11px;
  font-weight: 500;
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--forebrain-divider);
}
.bench-badge--ok {
  color: var(--forebrain-success);
  background: var(--forebrain-success-soft);
  border-color: var(--forebrain-success-soft);
}
.bench-badge--warn {
  color: var(--forebrain-warning);
  background: var(--forebrain-warning-soft);
  border-color: var(--forebrain-warning-soft);
}
.bench-badge--brand {
  color: var(--forebrain-brand-1);
  background: var(--forebrain-brand-soft);
  border-color: var(--forebrain-brand-border);
}
.bench-meter {
  display: flex;
  height: 8px;
  border-radius: 999px;
  background: var(--forebrain-button-alt-bg);
  overflow: hidden;
}
.bench-meter i {
  display: block;
  height: 100%;
}
.bench-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  margin: 0;
}
</style>
