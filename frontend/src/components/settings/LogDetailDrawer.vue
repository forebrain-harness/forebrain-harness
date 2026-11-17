<template>
  <ForebrainDrawer :open="open" :title="drawerTitle" :width="720" @close="emit('close')">
    <template #head>
      <div class="dr-head">
        <span v-if="badgeText" class="bdg" :class="`bdg--${badgeTone}`" data-testid="log-drawer-badge">{{ badgeText }}</span>
        <b class="dr-title" data-testid="log-drawer-title">{{ drawerTitle }}</b>
        <span class="dr-time" data-testid="log-drawer-time">{{ timeText }}</span>
      </div>
    </template>

    <div v-if="record && callView" class="pane" @keydown.up.prevent="step(-1)" @keydown.down.prevent="step(1)">
      <div class="dtabs" role="tablist" data-testid="log-drawer-tabs">
        <button
          v-for="entry in TABS"
          :key="entry.key"
          type="button"
          role="tab"
          :aria-selected="tab === entry.key"
          :data-testid="`log-drawer-tab-${entry.key}`"
          @click="tab = entry.key"
        >
          {{ t(entry.label) }}
          <small v-if="entry.key === 'req' && exchange?.request">{{ formatBytes(exchange.request.bytes) }}</small>
          <small v-else-if="entry.key === 'resp' && exchange?.response">{{ formatBytes(exchange.response.bytes) }}</small>
        </button>
      </div>

      <div v-if="callError" class="failed" data-testid="log-drawer-call-error">
        <TriangleAlert class="ic" aria-hidden="true" />
        <span>{{ callError }}</span>
        <button type="button" class="btn plain" @click="loadCall">{{ t('logs.retry') }}</button>
      </div>
      <div v-else-if="!exchange" class="muted">{{ t('logs.loading') }}</div>

      <!-- 概览：the call's own numbers, then who it belongs to, then the calls
           that were in flight with it — one lane each, per conversation. -->
      <section v-else-if="tab === 'overview'" data-testid="log-drawer-overview">
        <div class="stats">
          <div class="stat">
            <span>{{ t('logs.statStatus') }}</span>
            <b :class="statusTone(statusOf(exchange))">{{ statusOf(exchange) }}</b>
          </div>
          <div class="stat">
            <span>{{ t('logs.statDuration') }}</span>
            <b>{{ formatDuration(exchange.response?.durMs ?? 0) }}</b>
          </div>
          <div class="stat">
            <span>{{ t('logs.statFirstByte') }}</span>
            <b>{{ formatDuration(exchange.response?.ttfbMs ?? 0) }}</b>
          </div>
          <div class="stat">
            <span>{{ t('logs.statLongestGap') }}</span>
            <b :class="{ wn: (exchange.response?.maxGapMs ?? 0) > 30000 }">
              {{ formatDuration(exchange.response?.maxGapMs ?? 0) }}
            </b>
          </div>
        </div>
        <dl class="facts">
          <template v-for="fact in callFacts" :key="fact.label">
            <dt>{{ fact.label }}</dt>
            <dd :class="{ mono: fact.mono }">
              {{ fact.value }}
              <button v-if="fact.action === 'session'" type="button" data-testid="log-drawer-only-session" @click="pickSession">
                {{ t('logs.onlySession') }}
              </button>
              <button v-else-if="fact.action === 'run'" type="button" data-testid="log-drawer-only-run" @click="pickRun">
                {{ t('logs.onlyRun') }}
              </button>
            </dd>
          </template>
        </dl>

        <div class="sec">
          {{ t('logs.concurrent') }}
          <span class="sp" />
          <small>{{ lanesNote }}</small>
        </div>
        <div class="lanes" data-testid="log-drawer-lanes">
          <button
            v-for="lane in laneRows"
            :key="lane.id"
            type="button"
            class="lane"
            :class="{ me: lane.me }"
            :disabled="lane.me"
            :data-testid="`log-drawer-lane-${lane.id}`"
            @click="openCall(lane.day, lane.id)"
          >
            <span class="who">{{ lane.title }}<small>{{ lane.agent }}</small></span>
            <span class="trk"><i :class="{ bad: lane.status >= 400 }" :style="{ left: `${lane.left}%`, width: `${lane.width}%` }" /></span>
            <span class="du">{{ lane.dur }}</span>
          </button>
        </div>
      </section>

      <!-- 请求：the head it sent and the body, whole for a failed call and head
           and tail for a successful one. -->
      <section v-else-if="tab === 'req'" data-testid="log-drawer-request">
        <template v-if="!exchange.request">
          <p class="lead">{{ t('logs.noRequest') }}</p>
        </template>
        <template v-else>
          <div v-if="requestWholeNote" class="note-in ok" data-testid="log-drawer-request-whole">
            <Check class="ic" aria-hidden="true" />
            <span>{{ requestWholeNote }}</span>
          </div>
          <div v-if="payloadLoading === 'request'" class="muted">{{ t('logs.loading') }}</div>
          <template v-else-if="requestPayload">
            <div class="sec">{{ t('logs.requestHeaders') }}</div>
            <pre class="code" data-testid="log-drawer-request-head">{{ requestHead }}</pre>
            <div class="sec">
              {{ t('logs.requestBody') }}
              <span class="sp" />
              <small>{{ payloadSize(requestPayload) }}</small>
              <a
                class="btn plain sm"
                :href="downloadUrl('request')"
                download
                data-testid="log-drawer-request-download"
              >
                <Download class="ic" aria-hidden="true" />{{ t('logs.download') }}
              </a>
            </div>
            <div v-if="requestCutNote" class="cut" data-testid="log-drawer-request-cut">{{ requestCutNote }}</div>
            <div v-if="requestIsJSON" class="codebox tall">
              <ReadonlyCode path="request.json" :text="requestBody" />
            </div>
            <pre v-else class="code tall" data-testid="log-drawer-request-body">{{ requestBody }}</pre>
            <button
              v-if="requestMore"
              type="button"
              class="btn plain sm more"
              data-testid="log-drawer-request-more"
              @click="loadMore('request')"
            >
              {{ payloadMore('request') }}
            </button>
          </template>
        </template>
      </section>

      <!-- 响应：what came back, as the summary read it; a refused call keeps the
           upstream's own words instead. -->
      <section v-else-if="tab === 'resp'" data-testid="log-drawer-response">
        <template v-if="!exchange.response">
          <p class="lead" data-testid="log-drawer-no-response">{{ t('logs.noResponse') }}</p>
          <button type="button" class="btn plain" data-testid="log-drawer-see-request" @click="tab = 'req'">
            {{ t('logs.seeRequest') }}
          </button>
        </template>
        <template v-else-if="exchange.summary">
          <div class="msgs">
            <div v-if="exchange.summary.reasoning" class="mcard" data-testid="log-drawer-thinking">
              <div>{{ t('logs.thinking') }}<small>{{ exchange.summary.reasoning.length }} {{ t('logs.chars') }}</small></div>
              <pre>{{ exchange.summary.reasoning }}</pre>
            </div>
            <div v-if="exchange.summary.text" class="mcard" data-testid="log-drawer-answer">
              <div>{{ t('logs.answer') }}<small>{{ exchange.summary.text.length }} {{ t('logs.chars') }}</small></div>
              <pre>{{ exchange.summary.text }}</pre>
            </div>
            <div
              v-for="(call, index) in exchange.summary.toolCalls ?? []"
              :key="`${call.name}-${index}`"
              class="mcard"
              data-testid="log-drawer-tool-call"
            >
              <div>{{ t('logs.toolCall') }} {{ call.name }}<small>{{ t('logs.argumentsRaw') }}</small></div>
              <div v-if="isJSON(call.arguments)" class="codebox">
                <ReadonlyCode path="arguments.json" :text="prettyJSON(call.arguments)" />
              </div>
              <pre v-else>{{ call.arguments }}</pre>
            </div>
          </div>
          <div class="sec">{{ t('logs.ending') }}<span class="sp" /><small>{{ t('logs.eventCount', { n: exchange.summary.events }) }}</small></div>
          <dl class="facts">
            <dt>{{ t('logs.finishReason') }}</dt>
            <dd class="mono">{{ exchange.summary.finishReason || '—' }}</dd>
            <template v-if="exchange.summary.usage">
              <dt>{{ t('logs.usage') }}</dt>
              <dd class="mono">
                {{ t('logs.usageLine', { input: exchange.summary.usage.input, output: exchange.summary.usage.output }) }}
              </dd>
            </template>
          </dl>
        </template>
        <template v-else>
          <div v-if="payloadLoading === 'response'" class="muted">{{ t('logs.loading') }}</div>
          <template v-else-if="responsePayload">
            <div class="sec">{{ t('logs.responseHeaders') }}</div>
            <pre class="code" data-testid="log-drawer-response-head">{{ responseHead }}</pre>
            <div class="sec">
              {{ t('logs.responseBody') }}<span class="sp" /><small>{{ t('logs.upstreamWords') }}</small>
              <a class="btn plain sm" :href="downloadUrl('response')" download data-testid="log-drawer-response-download">
                <Download class="ic" aria-hidden="true" />{{ t('logs.download') }}
              </a>
            </div>
            <pre class="code" data-testid="log-drawer-response-body">{{ responseBody }}</pre>
            <button
              v-if="responseMore"
              type="button"
              class="btn plain sm more"
              data-testid="log-drawer-response-more"
              @click="loadMore('response')"
            >
              {{ payloadMore('response') }}
            </button>
          </template>
        </template>
      </section>

      <!-- 原文：the response payload line by line, as the process wrote it. -->
      <section v-else data-testid="log-drawer-raw">
        <div v-if="payloadLoading === 'response'" class="muted">{{ t('logs.loading') }}</div>
        <template v-else-if="responsePayload">
          <div class="sec">
            {{ exchange.summary ? t('logs.events') : t('logs.responseBody') }}
            <span class="sp" />
            <small>{{ t('logs.rawLines', { n: rawRows.length }) }} {{ formatBytes(responsePayload.total) }}</small>
            <a class="btn plain sm" :href="downloadUrl('response')" download data-testid="log-drawer-raw-download">
              <Download class="ic" aria-hidden="true" />{{ t('logs.download') }}
            </a>
          </div>
          <div class="sse" data-testid="log-drawer-raw-rows">
            <div v-for="row in rawRows" :key="row.n">
              <span class="n">{{ row.n }}</span>
              <span class="dv">{{ row.text }}</span>
            </div>
          </div>
          <button
            v-if="responseMore"
            type="button"
            class="btn plain sm more"
            data-testid="log-drawer-raw-more"
            @click="loadMore('response')"
          >
            {{ payloadMore('response') }}
          </button>
        </template>
        <p v-else-if="!exchange.response?.blob" class="lead">{{ t('logs.noResponse') }}</p>
      </section>
    </div>

    <!-- A record that is not a model call: what it says, who said it, its
         fields, an error and a stack. -->
    <div v-else-if="record" class="pane" @keydown.up.prevent="step(-1)" @keydown.down.prevent="step(1)">
      <p v-if="showLead" class="lead" :class="{ e: record.lvl === 'ERROR' }" data-testid="log-drawer-lead">{{ record.msg }}</p>
      <dl class="facts">
        <template v-for="fact in recordFacts" :key="fact.label">
          <dt>{{ fact.label }}</dt>
          <dd :class="{ mono: fact.mono }">
            {{ fact.value }}
            <button v-if="fact.action === 'session'" type="button" data-testid="log-drawer-only-session" @click="pickSession">
              {{ t('logs.onlySession') }}
            </button>
            <button v-else-if="fact.action === 'run'" type="button" data-testid="log-drawer-only-run" @click="pickRun">
              {{ t('logs.onlyRun') }}
            </button>
            <button v-else-if="fact.action === 'call'" type="button" data-testid="log-drawer-open-call" @click="openCall(record.day, attrText(record, 'x'))">
              {{ t('logs.openCall') }}
            </button>
          </dd>
        </template>
      </dl>

      <template v-if="record.err">
        <div class="sec">{{ t('logs.error') }}</div>
        <pre class="code" data-testid="log-drawer-error">{{ record.err }}</pre>
      </template>

      <template v-if="record.kind === 'tool'">
        <div class="sec">{{ t('logs.arguments') }}<span class="sp" /><small>{{ t('logs.argumentsRaw') }}</small></div>
        <div v-if="isJSON(toolArguments)" class="codebox">
          <ReadonlyCode path="arguments.json" :text="prettyJSON(toolArguments)" />
        </div>
        <pre v-else class="code" data-testid="log-drawer-tool-arguments">{{ toolArguments }}</pre>
        <div class="sec">
          {{ t('logs.result') }}<span class="sp" /><small>{{ formatBytes(attrNumber(record, 'resultBytes')) }}</small>
        </div>
        <pre class="code" data-testid="log-drawer-tool-result">{{ toolResult }}</pre>
      </template>

      <template v-if="record.attrs && Object.keys(record.attrs).length">
        <div class="sec">{{ t('logs.fields') }}</div>
        <pre class="code" data-testid="log-drawer-attrs">{{ prettyJSON(JSON.stringify(record.attrs)) }}</pre>
      </template>

      <template v-if="record.attrs?.stack">
        <div class="sec">{{ t('logs.stack') }}</div>
        <pre class="code tall" data-testid="log-drawer-stack">{{ String(record.attrs.stack) }}</pre>
      </template>
    </div>

    <template #footer>
      <button type="button" class="btn plain" data-testid="log-drawer-around" @click="emit('around', record?.t ?? '')">
        <Clock class="ic" aria-hidden="true" />{{ t('logs.aroundMinute') }}
      </button>
      <button type="button" class="btn plain" data-testid="log-drawer-copy" @click="copyRecord">
        <Copy class="ic" aria-hidden="true" />{{ copied ? t('logs.copied') : t('logs.copyRecord') }}
      </button>
    </template>
  </ForebrainDrawer>
</template>

<script setup lang="ts">
/**
 * One log record, opened beside the table (plan 288): a model call shows the
 * four things a reader asks of it — what it sent, what came back, the events
 * themselves, and the numbers around them — and anything else shows what the
 * record says, who said it and its fields.
 *
 * The payloads are read on demand: a call's request is a file that may be a
 * megabyte, so only the tab that shows it fetches it, 256KB at a time, and the
 * whole thing is one download away. A drawer that switches tab or call keeps
 * its one mount, so the panel slides in once and never again.
 */
import { computed, ref, watch } from 'vue'
import { Check, Clock, Copy, Download, TriangleAlert } from 'lucide-vue-next'

import ForebrainDrawer from '@/components/common/ForebrainDrawer.vue'
import ReadonlyCode from '@/components/editor/ReadonlyCode.vue'
import { forebrainApi, type LogBlobText, type LogExchange, type LogExchangeCall, type LogRecord } from '@/lib/api'
import {
  attrNumber,
  attrText,
  formatBytes,
  formatDuration,
  formatLogClock,
  formatLogFull,
  LOG_LEVEL_LABEL,
  LOG_SOURCE_LABEL,
  looksLikeJSON,
  splitPayload,
  type LogLevel,
} from '@/lib/logs'
import { t, type I18nKey } from '@/locales'

/** The four things a call drawer shows, in the design's order. */
type DrawerTab = 'overview' | 'req' | 'resp' | 'raw'

const TABS: { key: DrawerTab; label: I18nKey }[] = [
  { key: 'overview', label: 'logs.tabOverview' },
  { key: 'req', label: 'logs.tabRequest' },
  { key: 'resp', label: 'logs.tabResponse' },
  { key: 'raw', label: 'logs.tabRaw' },
]

/** One fact of the record's or the call's own table: a label, its value, and
 *  the one action a reader may take from it. */
interface Fact {
  label: string
  value: string
  mono?: boolean
  action?: 'session' | 'run' | 'call'
}

/** One lane of the concurrency strip: a call in flight with this one. */
interface LaneRow {
  id: string
  day: string
  title: string
  agent: string
  status: number
  left: number
  width: number
  dur: string
  me: boolean
}

const props = defineProps<{
  open: boolean
  record: LogRecord | null
}>()

const emit = defineEmits<{
  close: []
  'pick-session': [session: string, title?: string]
  'pick-run': [run: string]
  around: [iso: string]
  /** Move to the neighbouring record of the page the drawer was opened from. */
  step: [delta: number]
}>()

const TONE: Record<LogLevel, 'd' | 'i' | 'w' | 'e'> = { DEBUG: 'd', INFO: 'i', WARN: 'w', ERROR: 'e' }

const tab = ref<DrawerTab>('overview')
const call = ref<{ day: string; id: string } | null>(null)
const exchange = ref<LogExchange | null>(null)
const callError = ref('')
const payloads = ref<{ request?: LogBlobText; response?: LogBlobText }>({})
const payloadLoading = ref<'' | 'request' | 'response'>('')
const copied = ref(false)

let latest = 0
let copiedTimer: ReturnType<typeof setTimeout> | null = null

/** The call view is shown when the record is a model call, or when the reader
 *  followed a record's own call into it. */
const callView = computed(() => call.value !== null)

const drawerTitle = computed(() => {
  const record = props.record
  if (!record) return ''
  if (callView.value) {
    const session = exchange.value
      ? exchange.value.scope.sessionTitle || (exchange.value.scope.session ?? '').slice(0, 12)
      : record.sessionTitle || (record.session ?? '').slice(0, 12)
    return session ? t('logs.callTitle', { session }) : t('logs.callTitleAlone')
  }
  if (record.kind === 'tool') return t('logs.toolTitle', { name: attrText(record, 'tool') || record.msg })
  if (record.kind === 'http') return `${attrText(record, 'method')} ${attrText(record, 'path')}`.trim()
  return record.msg
})

const timeText = computed(() => {
  const record = props.record
  if (!record) return ''
  if (callView.value && exchange.value?.request) return formatLogFull(exchange.value.request.t)
  return formatLogFull(record.t)
})

const badgeText = computed(() => {
  if (!callView.value) {
    const record = props.record
    return record ? t(LOG_LEVEL_LABEL[record.lvl]) : ''
  }
  return exchange.value ? String(statusOf(exchange.value)) : ''
})

const badgeTone = computed(() => {
  if (!callView.value) return props.record ? TONE[props.record.lvl] : 'd'
  return exchange.value && statusOf(exchange.value) >= 300 ? 'e' : 's'
})

function statusOf(view: LogExchange): number {
  return view.response?.status ?? 0
}

function statusTone(status: number): string {
  if (status >= 400) return 'bad'
  if (status >= 300) return 'wn'
  return 'ok'
}

function sourceText(source: string): string {
  const key = LOG_SOURCE_LABEL[source]
  return key ? t(key) : source
}

const showLead = computed(() => ['log', 'turn', 'panic', 'setup'].includes(props.record?.kind ?? ''))

const recordFacts = computed<Fact[]>(() => {
  const record = props.record
  if (!record) return []
  const facts: Fact[] = [
    { label: t('logs.factSource'), value: `${sourceText(record.source)} ${record.host} ${t('logs.factProcess', { pid: record.pid })}` },
  ]
  if (record.projectName) facts.push({ label: t('logs.factProject'), value: record.projectName })
  if (record.session) {
    facts.push({ label: t('logs.factSession'), value: record.sessionTitle || record.session.slice(0, 12), action: 'session' })
    facts.push({ label: t('logs.factSessionId'), value: record.session, mono: true })
  }
  if (record.run) facts.push({ label: t('logs.factRun'), value: record.run, mono: true, action: 'run' })
  if (record.agent) {
    facts.push({
      label: t('logs.factAgent'),
      value: record.agent === 'main' ? t('logs.agentMain') : t('logs.agentSub', { name: record.agent }),
    })
  }
  const callId = attrText(record, 'x')
  if (callId && record.kind !== 'llm') facts.push({ label: t('logs.factCall'), value: callId, mono: true, action: 'call' })
  if (record.at) facts.push({ label: t('logs.factAt'), value: record.at, mono: true })
  return facts
})

const callFacts = computed<Fact[]>(() => {
  const view = exchange.value
  if (!view) return []
  const facts: Fact[] = []
  if (view.request?.url) {
    facts.push({ label: t('logs.factAddress'), value: `${view.request.method ?? 'POST'} ${view.request.url}`, mono: true })
  }
  if (view.request?.model) facts.push({ label: t('logs.factModel'), value: view.request.model, mono: true })
  if (view.scope.session) {
    facts.push({
      label: t('logs.factSession'),
      value: view.scope.sessionTitle || view.scope.session.slice(0, 12),
      action: 'session',
    })
  }
  if (view.scope.agent) {
    facts.push({
      label: t('logs.factAgent'),
      value: view.scope.agent === 'main' ? t('logs.agentMain') : t('logs.agentSub', { name: view.scope.agent }),
    })
  }
  if (view.scope.run) facts.push({ label: t('logs.factRun'), value: view.scope.run, mono: true, action: 'run' })
  facts.push({
    label: t('logs.factSource'),
    value: `${sourceText(view.scope.source)} ${view.scope.host} ${t('logs.factProcess', { pid: view.scope.pid })}`,
  })
  const start = view.request?.t ?? view.response?.t ?? ''
  const end = view.response?.t ?? start
  if (start) facts.push({ label: t('logs.factSpan'), value: `${formatLogFull(start)} → ${formatLogClock(end)}`, mono: true })
  if (view.summary?.usage) {
    facts.push({
      label: t('logs.usage'),
      value: `${t('logs.usageLine', { input: view.summary.usage.input, output: view.summary.usage.output })}　${t('logs.finishReason')} ${view.summary.finishReason || '—'}`,
      mono: true,
    })
  }
  return facts
})

/** The strip's own words: how many other calls there were, or that there were none. */
const lanesNote = computed(() => {
  const others = exchange.value?.concurrent ?? []
  return others.length ? t('logs.concurrentCount', { n: others.length }) : t('logs.concurrentAlone')
})

/**
 * The calls around this one, drawn on one shared window: this call's own span
 * widened by a quarter on each side, so a concurrent call's bar reads as
 * "still running after mine ended" rather than as a bar of its own scale.
 */
const laneRows = computed<LaneRow[]>(() => {
  const view = exchange.value
  if (!view) return []
  const own: LogExchangeCall = {
    id: view.id,
    day: view.day,
    session: view.scope.session,
    agent: view.scope.agent,
    t0: view.request?.t ?? view.response?.t ?? '',
    t1: view.response?.t ?? view.request?.t ?? '',
    status: statusOf(view),
    sessionTitle: view.scope.sessionTitle,
  }
  const mine = { start: Date.parse(own.t0), end: Date.parse(own.t1) }
  const pad = Math.max(2000, (mine.end - mine.start) * 0.25)
  const windowStart = mine.start - pad
  const span = Math.max(1, mine.end + pad - windowStart)
  const place = (call: LogExchangeCall, me: boolean): LaneRow => {
    const start = Date.parse(call.t0)
    const end = Date.parse(call.t1)
    const left = Math.min(100, Math.max(0, ((start - windowStart) / span) * 100))
    const right = Math.min(100, Math.max(0, ((end - windowStart) / span) * 100))
    return {
      id: call.id,
      day: call.day,
      title: call.sessionTitle || (call.session ?? '').slice(0, 12),
      agent: call.agent && call.agent !== 'main' ? call.agent : '',
      status: call.status,
      left,
      width: Math.max(0.8, right - left),
      dur: formatDuration(end - start),
      me,
    }
  }
  const others = (view.concurrent ?? []).slice(0, 6)
  return [place(own, true), ...others.map((other) => place(other, false))]
})

// —— payloads: read on demand, 256KB at a time ——
const PAYLOAD_CHUNK = 256 * 1024

const requestPayload = computed(() => payloads.value.request ?? null)
const responsePayload = computed(() => payloads.value.response ?? null)

const requestParts = computed(() => splitPayload(requestPayload.value?.text ?? ''))
const requestHead = computed(() => requestParts.value.head)
const requestBody = computed(() => requestParts.value.body)
const requestIsJSON = computed(() => looksLikeJSON(requestBody.value))

const responseParts = computed(() => splitPayload(responsePayload.value?.text ?? ''))
const responseHead = computed(() => responseParts.value.head)
const responseBody = computed(() => responseParts.value.body)

/** The note over a failed call's request: it is the evidence, so it is whole. */
const requestWholeNote = computed(() => {
  const view = exchange.value
  if (!view?.request) return ''
  const status = statusOf(view)
  if (!view.request.whole || (view.response && status < 400)) return ''
  return t('logs.requestWhole', { size: formatBytes(view.request.bytes) })
})

/** The note inside a successful call's request: only its head and tail were kept. */
const requestCutNote = computed(() => {
  const request = exchange.value?.request
  if (!request || request.whole) return ''
  return t('logs.requestCut', { size: formatBytes(request.omitted ?? 0) })
})

const requestMore = computed(() => {
  const payload = requestPayload.value
  return Boolean(payload && payload.total > payload.text.length)
})

const responseMore = computed(() => {
  const payload = responsePayload.value
  return Boolean(payload && payload.total > payload.text.length)
})

function payloadSize(payload: LogBlobText): string {
  return formatBytes(payload.total)
}

function payloadMore(which: 'request' | 'response'): string {
  const payload = which === 'request' ? requestPayload.value : responsePayload.value
  if (!payload) return ''
  return t('logs.loadMore', { loaded: formatBytes(payload.text.length), total: formatBytes(payload.total) })
}

/** The raw tab's rows: the body's own lines, numbered as the file wrote them. */
const rawRows = computed(() => {
  const body = splitPayload(responsePayload.value?.text ?? '').body
  return body
    .split('\n')
    .filter((line) => line.trim() !== '')
    .map((text, index) => ({ n: index + 1, text }))
})

const toolArguments = computed(() => attrText(props.record ?? { attrs: {} }, 'args') || attrText(props.record ?? { attrs: {} }, 'argsPreview'))

const toolResult = computed(() => {
  const record = props.record
  if (!record) return ''
  const full = attrText(record, 'result') || attrText(record, 'resultPreview')
  return full
})

function isJSON(text: string): boolean {
  return looksLikeJSON(text)
}

function prettyJSON(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

function downloadUrl(which: 'request' | 'response'): string {
  const view = exchange.value
  const name = which === 'request' ? view?.request?.blob : view?.response?.blob
  if (!view || !name) return '#'
  return forebrainApi.logBlobDownloadUrl(view.day, name)
}

function pickSession(): void {
  const view = exchange.value
  const session = view?.scope.session || props.record?.session || ''
  if (!session) return
  emit('pick-session', session, view?.scope.sessionTitle || props.record?.sessionTitle || '')
}

function pickRun(): void {
  const run = exchange.value?.scope.run || props.record?.run || ''
  if (!run) return
  emit('pick-run', run)
}

function step(delta: number): void {
  emit('step', delta)
}

async function copyRecord(): Promise<void> {
  const record = props.record
  if (!record) return
  try {
    await navigator.clipboard.writeText(JSON.stringify(record, null, 2))
  } catch {
    // A page without the clipboard permission still copies nothing and says so;
    // there is no second surface to fall back to here.
  }
  copied.value = true
  if (copiedTimer) clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => {
    copiedTimer = null
    copied.value = false
  }, 1200)
}

/** Open one call by its id: this record's own, or a neighbour's from the strip. */
async function openCall(day: string, id: string): Promise<void> {
  if (!day || !id) return
  call.value = { day, id }
  exchange.value = null
  callError.value = ''
  payloads.value = {}
  payloadLoading.value = ''
  tab.value = 'overview'
  await loadCall()
}

async function loadCall(): Promise<void> {
  const target = call.value
  if (!target) return
  const sequence = ++latest
  callError.value = ''
  try {
    const answer = await forebrainApi.logExchange(target.day, target.id)
    if (sequence !== latest) return
    exchange.value = answer
    await ensurePayload()
  } catch (cause) {
    if (sequence !== latest) return
    callError.value = cause instanceof Error ? cause.message : String(cause)
  }
}

/** The tab's own payload: the request for 请求, the response for 响应 and 原文. */
async function ensurePayload(): Promise<void> {
  const view = exchange.value
  if (!view) return
  const which = tab.value === 'req' ? 'request' : tab.value === 'raw' ? 'response' : tab.value === 'resp' && !view.summary ? 'response' : null
  if (!which) return
  if (payloads.value[which]) return
  const name = which === 'request' ? view.request?.blob : view.response?.blob
  if (!name) return
  await readPayload(which, view.day, name, 0)
}

async function readPayload(which: 'request' | 'response', day: string, name: string, offset: number): Promise<void> {
  payloadLoading.value = which
  try {
    const read = await forebrainApi.logBlob(day, name, offset, PAYLOAD_CHUNK)
    const previous = offset > 0 ? payloads.value[which]?.text ?? '' : ''
    payloads.value = { ...payloads.value, [which]: { text: previous + read.text, total: read.total || previous.length + read.text.length } }
  } catch (cause) {
    callError.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    payloadLoading.value = ''
  }
}

async function loadMore(which: 'request' | 'response'): Promise<void> {
  const view = exchange.value
  const payload = payloads.value[which]
  if (!view || !payload) return
  const name = which === 'request' ? view.request?.blob : view.response?.blob
  if (!name) return
  await readPayload(which, view.day, name, payload.text.length)
}

/** Starting over: a closed drawer forgets the call it showed, its payloads and
 *  its tab, so the next record opens on its own facts. */
function reset(): void {
  latest += 1
  call.value = null
  exchange.value = null
  callError.value = ''
  payloads.value = {}
  payloadLoading.value = ''
  tab.value = 'overview'
  copied.value = false
}

watch(
  () => props.record,
  (record) => {
    reset()
    if (!record) return
    const callId = attrText(record, 'x')
    if (record.kind === 'llm' && callId) void openCall(record.day, callId)
  },
  { immediate: true },
)

watch(
  () => props.open,
  (open) => {
    if (!open) reset()
  },
)

watch(tab, () => {
  void ensurePayload()
})
</script>

<style scoped>
.pane {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.ic {
  width: 14px;
  height: 14px;
  flex: none;
}

/* ---------- the header the shell hands over ---------- */
.dr-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
}
.dr-title {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dr-time {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
}
.bdg {
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11.5px;
  font-weight: 500;
  white-space: nowrap;
}
.bdg--d {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-muted-text);
}
.bdg--i {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}
.bdg--w {
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-warning);
}
.bdg--e {
  background: #fef3f2;
  color: var(--forebrain-danger);
}
.bdg--s {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}

/* ---------- tabs ---------- */
.dtabs {
  display: flex;
  gap: 2px;
  margin: -14px -16px 12px;
  padding: 0 16px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.dtabs button {
  border: 0;
  background: transparent;
  padding: 9px 12px 11px;
  font-size: 13px;
  color: var(--forebrain-text-2);
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}
.dtabs button:hover {
  color: var(--forebrain-text);
}
.dtabs button[aria-selected='true'] {
  color: var(--forebrain-brand-1);
  font-weight: 500;
  border-bottom-color: var(--forebrain-brand-1);
}
.dtabs small {
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
  margin-left: 5px;
}

/* ---------- shared pieces ---------- */
.lead {
  font-size: 14px;
  line-height: 1.6;
  color: var(--forebrain-text);
  word-break: break-word;
  margin: 0 0 14px;
}
.lead.e {
  color: var(--forebrain-danger);
}
.muted {
  font-size: 12.5px;
  color: var(--forebrain-muted-text);
}
.facts {
  display: grid;
  grid-template-columns: 76px minmax(0, 1fr);
  gap: 7px 14px;
  font-size: 12.5px;
  margin: 0;
}
.facts dt {
  color: var(--forebrain-muted-text);
}
.facts dd {
  margin: 0;
  min-width: 0;
  word-break: break-all;
  color: var(--forebrain-text);
}
.facts dd.mono {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
}
.facts dd button {
  border: 0;
  background: transparent;
  padding: 0;
  color: var(--forebrain-brand-1);
  font-size: 12px;
  margin-left: 8px;
}
.facts dd button:hover {
  text-decoration: underline;
}
.sec {
  margin: 18px 0 8px;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.sec .sp {
  flex: 1;
}
.sec small {
  font-weight: 400;
  color: var(--forebrain-muted-text);
  font-size: 12px;
}
.code {
  margin: 0;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-code-bg);
  padding: 10px 12px;
  font: 12px/1.65 var(--forebrain-mono-font);
  color: var(--forebrain-code-text);
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 360px;
  overflow: auto;
}
.code.tall {
  max-height: 520px;
}
/* A payload the code view draws: the design's own block, with the height its
   content is read in rather than the whole megabyte at once. */
.codebox {
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-code-bg);
  max-height: 220px;
  overflow: auto;
}
.codebox.tall {
  max-height: 520px;
}
.cut {
  display: block;
  margin: 6px 0;
  padding: 5px 9px;
  border-radius: 6px;
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-warning);
  font-size: 12px;
}
.note-in {
  display: flex;
  gap: 8px;
  align-items: flex-start;
  margin-bottom: 10px;
  padding: 8px 11px;
  border-radius: 8px;
  font-size: 12.5px;
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text-2);
}
.note-in.ok {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}
.note-in .ic {
  margin-top: 3px;
}
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 30px;
  padding: 0 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--forebrain-text);
  white-space: nowrap;
  text-decoration: none;
}
.btn.plain {
  background: var(--forebrain-surface);
}
.btn.plain:hover {
  background: var(--forebrain-bg-alt);
}
.btn.sm {
  height: 26px;
  padding: 0 9px;
  font-size: 12px;
}
.btn .ic {
  width: 13px;
  height: 13px;
}
.more {
  margin-top: 8px;
  align-self: flex-start;
}
.failed {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border: 1px solid var(--forebrain-danger);
  border-radius: 10px;
  color: var(--forebrain-danger);
  font-size: 13px;
}

/* ---------- overview ---------- */
.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  overflow: hidden;
  margin-bottom: 16px;
}
.stat {
  padding: 10px 12px;
  border-left: 1px solid var(--forebrain-divider);
}
.stat:first-child {
  border-left: 0;
}
.stat span {
  display: block;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.stat b {
  display: block;
  font-family: var(--forebrain-mono-font);
  font-size: 15px;
  font-weight: 600;
  color: var(--forebrain-text);
  margin-top: 1px;
}
.stat b.bad {
  color: var(--forebrain-danger);
}
.stat b.ok {
  color: var(--forebrain-success);
}
.stat b.wn {
  color: var(--forebrain-warning);
}
.lanes {
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  overflow: hidden;
}
.lane {
  display: grid;
  grid-template-columns: 190px minmax(0, 1fr) 62px;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 0 10px;
  height: 32px;
  border: 0;
  border-top: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  text-align: left;
  font-size: 12px;
  color: var(--forebrain-text);
}
.lane:first-child {
  border-top: 0;
}
.lane:hover {
  background: var(--forebrain-bg-alt);
}
.lane.me {
  background: var(--forebrain-brand-soft);
}
.lane .who {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.lane .who small {
  color: var(--forebrain-muted-text);
  margin-left: 5px;
}
.lane .trk {
  position: relative;
  height: 8px;
  border-radius: 4px;
  background: var(--forebrain-button-alt-bg);
}
.lane .trk i {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: 4px;
  background: var(--forebrain-brand-border-strong);
  min-width: 3px;
}
.lane.me .trk i {
  background: var(--forebrain-brand-1);
}
.lane .trk i.bad {
  background: #d92d20;
}
.lane .du {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  text-align: right;
}

/* ---------- what came back ---------- */
.msgs {
  display: grid;
  gap: 8px;
}
.mcard {
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  overflow: hidden;
}
.mcard > div:first-child {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 10px;
  background: var(--forebrain-bg-alt);
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 12px;
  color: var(--forebrain-text-2);
}
.mcard > div:first-child small {
  margin-left: auto;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.mcard pre {
  margin: 0;
  padding: 9px 11px;
  font: 12px/1.65 var(--forebrain-mono-font);
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--forebrain-text);
  max-height: 200px;
  overflow: auto;
}

/* ---------- the events themselves ---------- */
.sse {
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  overflow: auto;
  max-height: 560px;
  font: 12px/1.6 var(--forebrain-mono-font);
}
.sse div {
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr);
  gap: 8px;
  padding: 3px 10px;
  border-top: 1px solid var(--forebrain-divider);
}
.sse div:first-child {
  border-top: 0;
}
.sse div:nth-child(odd) {
  background: var(--forebrain-bg-alt);
}
.sse .n {
  color: var(--forebrain-muted-text);
  text-align: right;
}
.sse .dv {
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--forebrain-code-text);
}
</style>
