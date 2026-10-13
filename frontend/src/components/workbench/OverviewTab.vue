<template>
  <div class="bench-tab">
    <!-- 1. Running agents: the card appears only while something is running. -->
    <AgentRosterPanel
      v-if="records.length > 0 || error"
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

    <!-- 3. Plan. -->
    <div v-if="planTitle" class="bench-card">
      <div class="bench-card-head">
        <span class="bench-card-title"><Map class="size-3.5" aria-hidden="true" />{{ t('chat.benchPlan') }}</span>
        <button type="button" class="bench-link" @click="planOpen = true">{{ t('chat.benchPlanView') }}</button>
      </div>
      <div class="bench-plan">
        <b>{{ planTitle }}</b>
        <ol v-if="planPoints.length">
          <li v-for="(point, idx) in planPoints" :key="idx">{{ point }}</li>
        </ol>
      </div>
    </div>

    <!-- 4. MCP servers. -->
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

    <!-- 5. Usage. -->
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
      <div class="bench-kvs">
        <span class="k">{{ t('chat.benchUsageCache') }}</span>
        <span class="v">{{ cacheLabel }}</span>
      </div>
      <div class="bench-meter" role="img" :aria-label="cacheLabel">
        <i :style="{ width: `${cachePercent}%`, background: 'var(--forebrain-success)' }" />
      </div>
    </div>
  </div>

  <ForebrainModal :open="planOpen" size="xl" :title="t('chat.benchPlan')" @close="planOpen = false">
    <pre class="bench-plan-full">{{ planMarkdown }}</pre>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * The workbench's overview tab: what is alive right now (running agents),
 * what the conversation is doing (to-dos, plan), what it may use (MCP
 * servers) and what it has spent (usage). Each card appears only when it has
 * something to say — a conversation with no running agents draws no card.
 */
import { computed, ref, watch } from 'vue'
import { CircleDot, ListChecks, Map, Plug, Receipt, Square, SquareCheck } from 'lucide-vue-next'

import AgentRosterPanel from '@/components/AgentRosterPanel.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import {
  getErrorMessage,
  forebrainApi,
  type AgentRosterRow,
  type McpServerRecord,
  type SessionCostSummary,
  type WorkspaceScope,
} from '@/lib/api'
import { compactTokenCount } from '@/lib/contextDebug'
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
}>()

const { t } = useI18n()

const sessionId = computed(() => String(props.scope.sessionId ?? '').trim())

const todos = ref<{ id: string; content: string; status: string }[]>([])
const planMarkdown = ref('')
const mcpServers = ref<McpServerRecord[]>([])
const cost = ref<SessionCostSummary | null>(null)
const planOpen = ref(false)

const doneTodos = computed(() => todos.value.filter((todo) => todo.status === 'done' || todo.status === 'completed').length)
const cachePercent = computed(() => Math.max(0, Math.min(100, cost.value?.cacheHitPercent ?? 0)))
const cacheLabel = computed(() => `${Math.round(cachePercent.value)}%`)

/** The plan's title and first bullet points, read off its Markdown. */
const planLines = computed(() => planMarkdown.value.split('\n').map((line) => line.trim()))
const planTitle = computed(() => {
  const heading = planLines.value.find((line) => /^#{1,6}\s+\S/.test(line))
  return heading ? heading.replace(/^#{1,6}\s+/, '') : ''
})
const planPoints = computed(() =>
  planLines.value
    .filter((line) => /^([-*]|\d+\.)\s+\S/.test(line))
    .slice(0, 6)
    .map((line) => line.replace(/^([-*]|\d+\.)\s+/, '')),
)

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
  if (status === 'error' || status === 'cancelled') return t('chat.benchMcpTimeout')
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
    planMarkdown.value = ''
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
    settle(forebrainApi.sessionPlanMd(sid).then((res) => res.markdown ?? ''), (md) => { planMarkdown.value = md }),
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
.bench-plan-full {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  line-height: 1.6;
  color: var(--forebrain-text);
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
