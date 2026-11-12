<template>
  <div class="logs" data-testid="logs-tab">
    <!-- The filter bar: two rows that wrap (the prototype's `.lbar`). Time first,
         then the four levels with their counts, then the source, then the three
         dimensions the gateway knows, then the search and the two actions. -->
    <div class="lbar">
      <div class="anchor">
        <button
          type="button"
          class="combo time"
          :class="{ open: pop === 'time' }"
          :aria-expanded="pop === 'time'"
          data-testid="logs-time"
          @click="togglePop('time')"
        >
          <Clock class="ic" aria-hidden="true" />
          <span class="v">{{ timeLabel }}</span>
          <ChevronDown class="ic" aria-hidden="true" />
        </button>
        <div v-if="pop === 'time'" class="cpop tpop" data-testid="logs-time-pop">
          <button
            v-for="preset in LOG_PRESETS"
            :key="preset"
            type="button"
            class="copt"
            :class="{ on: filters.preset === preset }"
            :data-testid="`logs-preset-${preset}`"
            @click="applyPreset(preset)"
          >
            {{ t(LOG_PRESET_LABEL[preset]) }}
            <Check v-if="filters.preset === preset" class="ic" aria-hidden="true" />
          </button>
          <div class="cus">
            <label>
              {{ t('logs.customFrom') }}
              <input v-model="customFrom" type="datetime-local" class="fld" data-testid="logs-custom-from" />
            </label>
            <label>
              {{ t('logs.customTo') }}
              <input v-model="customTo" type="datetime-local" class="fld" data-testid="logs-custom-to" />
            </label>
            <button type="button" class="btn pri sm" data-testid="logs-custom-apply" @click="applyCustom">
              {{ t('logs.apply') }}
            </button>
            <span v-if="customError" class="cus-error" data-testid="logs-custom-error">{{ customError }}</span>
          </div>
        </div>
      </div>

      <div class="lvs" role="group" :aria-label="t('logs.levels')">
        <button
          v-for="level in LOG_LEVELS"
          :key="level"
          type="button"
          class="lv"
          :class="`lv--${LEVEL_TONE[level]}`"
          :aria-pressed="filters.levels.includes(level)"
          :data-testid="`logs-level-${level.toLowerCase()}`"
          @click="toggleLevel(level)"
        >
          <i aria-hidden="true" />{{ t(LOG_LEVEL_LABEL[level]) }}<em>{{ levelCounts[level] }}</em>
        </button>
      </div>

      <div class="seg" role="radiogroup" :aria-label="t('logs.source')">
        <button
          v-for="source in LOG_SOURCES"
          :key="source || 'all'"
          type="button"
          role="radio"
          :aria-checked="filters.source === source"
          :data-testid="`logs-source-${source || 'all'}`"
          @click="setSource(source)"
        >
          {{ t(LOG_SOURCE_LABEL[source]) }}
        </button>
      </div>

      <select
        class="fld dim"
        :class="{ set: !!filters.project }"
        :value="filters.project"
        :aria-label="t('logs.project')"
        data-testid="logs-project"
        @change="setProject(($event.target as HTMLSelectElement).value)"
      >
        <option value="">{{ t('logs.allProjects') }}</option>
        <option v-for="project in projects" :key="project.id" :value="project.id">{{ project.name }}</option>
      </select>

      <div class="anchor">
        <button
          type="button"
          class="combo sess"
          :class="{ open: pop === 'session', set: !!filters.session }"
          :aria-expanded="pop === 'session'"
          data-testid="logs-session"
          @click="togglePop('session')"
        >
          <MessageSquare class="ic" aria-hidden="true" />
          <span class="v" :class="{ ph: !filters.session }">{{ sessionLabel }}</span>
          <span
            v-if="filters.session"
            class="ic-x"
            role="button"
            :aria-label="t('logs.clearSession')"
            data-testid="logs-session-clear"
            @click.stop="setSession('')"
          >
            <X class="ic" aria-hidden="true" />
          </span>
          <ChevronDown v-else class="ic" aria-hidden="true" />
        </button>
        <div v-if="pop === 'session'" class="cpop spop" data-testid="logs-session-pop">
          <label class="search">
            <Search class="ic" aria-hidden="true" />
            <input
              v-model="sessionSearchModel"
              type="search"
              :placeholder="t('logs.sessionSearch')"
              :aria-label="t('logs.sessionSearch')"
              data-testid="logs-session-search"
            />
          </label>
          <div class="list">
            <button
              v-for="option in sessionOptions"
              :key="option.id"
              type="button"
              class="copt"
              :class="{ on: filters.session === option.id }"
              :data-testid="`logs-session-option-${option.id}`"
              @click="setSession(option.id, option.title)"
            >
              <span class="two">
                <b>{{ option.title || option.id.slice(0, 12) }}</b>
                <span>{{ option.id.slice(0, 12) }}… · {{ option.project }}</span>
              </span>
              <small>{{ option.source }}</small>
            </button>
            <div v-if="!sessionOptions.length" class="none">{{ t('logs.sessionEmpty') }}</div>
          </div>
        </div>
      </div>

      <select
        class="fld dim"
        :class="{ set: !!filters.kind }"
        :value="filters.kind"
        :aria-label="t('logs.kind')"
        data-testid="logs-kind"
        @change="setKind(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="kind in LOG_KINDS" :key="kind || 'all'" :value="kind">{{ t(LOG_KIND_LABEL[kind]) }}</option>
      </select>

      <label class="search">
        <Search class="ic" aria-hidden="true" />
        <input
          v-model="searchModel"
          type="search"
          :placeholder="t('logs.searchPlaceholder')"
          :aria-label="t('logs.searchPlaceholder')"
          data-testid="logs-search"
        />
      </label>

      <button
        v-tip="t('logs.refresh')"
        type="button"
        class="icbtn"
        :aria-label="t('logs.refresh')"
        data-testid="logs-refresh"
        @click="refresh"
      >
        <RefreshCw class="ic" :class="{ spin: spinning }" aria-hidden="true" />
      </button>
      <button type="button" class="btn plain" data-testid="logs-reset" @click="reset">
        {{ t('logs.reset') }}
      </button>
    </div>

    <!-- The histogram: the range's distribution, and the handle that narrows it. -->
    <div class="hist">
      <div class="hist-top">
        <b data-testid="logs-histogram-count">{{ countLabel }}</b>
        <span data-testid="logs-histogram-range">{{ rangeText }}</span>
        <span class="sp" />
        <span>{{ t('logs.dragHint') }}</span>
      </div>
      <div ref="barsEl" class="bars" data-testid="logs-histogram" @mousedown.prevent="startBrush">
        <div v-for="(bar, index) in bars" :key="index" class="bar" :title="barTip(index)">
          <span
            v-for="segment in bar"
            :key="segment.level"
            :class="`seg-${LEVEL_TONE[segment.level]}`"
            :style="{ height: segment.height }"
          />
        </div>
        <div
          v-if="brush"
          class="brush"
          :style="{ left: `${brush.left}%`, width: `${brush.width}%` }"
          data-testid="logs-brush"
        />
      </div>
      <div class="axis">
        <span>{{ axis[0] }}</span>
        <span>{{ axis[1] }}</span>
        <span>{{ axis[2] }}</span>
      </div>
    </div>

    <!-- What a failed request answered, where the table would be: the filters stay. -->
    <div v-if="error" class="failed" data-testid="logs-error">
      <TriangleAlert class="ic" aria-hidden="true" />
      <span>{{ error }}</span>
      <button type="button" class="btn plain" data-testid="logs-retry" @click="load">{{ t('logs.retry') }}</button>
    </div>

    <div
      v-else
      class="logtable"
      @click="onRowClick"
      @keydown.enter="onRowEnter"
    >
      <DataTable
        :columns="columns"
        :rows="records"
        :row-key="(row) => row.id"
        :row-attrs="rowAttrs"
        :row-class="rowClass"
        :data-testid="'logs-table'"
      >
        <template #cell-time="{ row }">
          <span class="tm">
            <small v-if="multiDay">{{ formatLogDay(row.t) }}</small>{{ formatLogClock(row.t) }}
          </span>
        </template>
        <template #cell-level="{ row }">
          <span class="lvb" :class="`lv--${LEVEL_TONE[row.lvl]}`">
            <i aria-hidden="true" />{{ t(LOG_LEVEL_LABEL[row.lvl]) }}
          </span>
        </template>
        <template #cell-source="{ row }">
          <span class="srcc">{{ sourceText(row.source) }}<small>{{ row.pid }}</small></span>
        </template>
        <template #cell-session="{ row }">
          <div v-if="row.session" class="sc">
            <span>{{ sessionText(row) }}</span>
            <button
              v-tip="t('logs.onlySession')"
              type="button"
              class="sessb"
              :aria-label="t('logs.onlySession')"
              :data-testid="`logs-only-session-${row.id}`"
              @click.stop="setSession(row.session ?? '', row.sessionTitle ?? '')"
            >
              <Filter class="ic" aria-hidden="true" />
            </button>
          </div>
          <span v-else class="none" data-testid="logs-row-no-session">—</span>
        </template>
        <template #cell-content="{ row }">
          <span v-if="agentTag(row)" class="ag">{{ agentTag(row) }}</span>
          <span v-if="kindIcon(row)" class="kd"><component :is="kindIcon(row)" class="ic" aria-hidden="true" /></span>
          <template v-if="rowKind(row) === 'llm-request'">
            <span class="m">{{ t('logs.sentRequest') }}</span>
            <span class="kv">{{ shortURL(attrText(row, 'url')) }}</span>
            <span class="kv"><b>model=</b>{{ attrText(row, 'model') }}</span>
            <span class="kv"><b>{{ t('logs.requestBody') }} </b>{{ formatBytes(attrNumber(row, 'reqBytes')) }}</span>
          </template>
          <template v-else-if="rowKind(row) === 'llm-response'">
            <span class="st" :class="statusTone(attrNumber(row, 'status'))">{{ attrNumber(row, 'status') }}</span>
            <span class="dur">{{ formatDuration(attrNumber(row, 'durMs')) }}</span>
            <span class="kv">{{ shortURL(attrText(row, 'url')) }}</span>
            <span class="kv"><b>model=</b>{{ attrText(row, 'model') }}</span>
            <span class="kv"><b>{{ t('logs.responseBody') }} </b>{{ formatBytes(attrNumber(row, 'respBytes')) }}</span>
          </template>
          <template v-else-if="rowKind(row) === 'tool'">
            <code>{{ toolName(row) }}</code>
            <span class="dur">{{ formatDuration(attrNumber(row, 'durMs')) }}</span>
            <span v-if="row.err" class="st wn">{{ row.err }}</span>
            <span class="kv">{{ toolPreview(row) }}</span>
          </template>
          <template v-else-if="rowKind(row) === 'http'">
            <span class="m">{{ attrText(row, 'method') }}</span>
            <span class="st" :class="statusTone(attrNumber(row, 'status'))">{{ attrNumber(row, 'status') }}</span>
            <span class="kv">{{ attrText(row, 'path') }}</span>
            <span class="kv">{{ attrText(row, 'duration') }}</span>
            <span class="kv">{{ formatBytes(attrNumber(row, 'bytes')) }}</span>
          </template>
          <template v-else>
            <span class="m">{{ row.msg }}</span>
            <span v-if="row.err" class="kv">{{ row.err }}</span>
            <span v-for="field in attrPreview(row)" :key="field.key" class="kv"><b>{{ field.key }}=</b>{{ field.value }}</span>
          </template>
        </template>
        <template #empty>
          <div class="empty" data-testid="logs-empty">
            <span>{{ t('logs.empty') }}</span>
            <div class="acts">
              <button type="button" class="btn plain" data-testid="logs-empty-week" @click="applyPreset('7d')">
                {{ t('logs.widenTime') }}
              </button>
              <button type="button" class="btn plain" data-testid="logs-empty-levels" @click="allLevels">
                {{ t('logs.widenLevels') }}
              </button>
              <button type="button" class="btn plain" data-testid="logs-empty-reset" @click="reset">
                {{ t('logs.widenReset') }}
              </button>
            </div>
          </div>
        </template>
      </DataTable>
      <TablePager
        :total="total"
        :page="page"
        :page-size="pageSize"
        :total-label="totalLabel"
        @update:page="goPage"
        @update:page-size="goPageSize"
      />
    </div>

    <LogDetailDrawer
      :open="!!selected"
      :record="selected"
      @close="selectedId = ''"
      @pick-session="setSession"
      @pick-run="setRun"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * Settings · Advanced · Logs (plan 288): every process's records from plans
 * 284–287, read through the gateway's three endpoints.
 *
 * The page is a thin shell over `GET /api/logs`: the filter bar builds the
 * query, the histogram reads the server's own buckets and doubles as the
 * range's handle, and the table is one page of what came back. Choosing a
 * conversation leaves only its records, which is how two sessions' model output
 * stops mixing; one model call's request and response share one call id, and
 * opening either shows that call's own payloads.
 *
 * There is no polling and no live tail (owner ruling 2026-11-11): a page is
 * read when the reader asks for it — a filter change, a page click, the
 * refresh button.
 */
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import {
  Bot,
  Check,
  ChevronDown,
  Clock,
  Filter,
  Flag,
  Globe,
  MessageSquare,
  RefreshCw,
  Search,
  Terminal,
  TriangleAlert,
  X,
} from 'lucide-vue-next'

import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import TablePager from '@/components/common/TablePager.vue'
import LogDetailDrawer from '@/components/settings/LogDetailDrawer.vue'
import { forebrainApi, type LogBucket, type LogRecord } from '@/lib/api'
import {
  LOG_KIND_LABEL,
  LOG_KINDS,
  LOG_LEVEL_LABEL,
  LOG_LEVELS,
  LOG_PRESET_LABEL,
  LOG_PRESETS,
  LOG_SOURCE_LABEL,
  LOG_SOURCES,
  attrNumber,
  attrText,
  defaultFilters,
  formatBytes,
  formatDuration,
  formatLogClock,
  formatLogDay,
  formatLogShort,
  levelCountsOf,
  rangeOf,
  rowKind,
  shortURL,
  toQuery,
  type LogFilters,
  type LogLevel,
  type LogPreset,
} from '@/lib/logs'
import { t } from '@/locales'

const props = defineProps<{
  /** The conversation the URL named: the page opens filtered to it. */
  session?: string
  /** The range the URL named, RFC3339. A `from` alone ends at now. */
  from?: string
  to?: string
  /** The record the URL named: its drawer opens once its page is read. */
  focus?: string
}>()

/** A level's own tone: the four colours live on this page, never in the theme. */
const LEVEL_TONE: Record<LogLevel, 'd' | 'i' | 'w' | 'e'> = { DEBUG: 'd', INFO: 'i', WARN: 'w', ERROR: 'e' }

/** The prototype's kind marks: a call, a tool, an access record, a turn, a crash. */
const KIND_ICON: Record<string, typeof Bot> = {
  llm: Bot,
  tool: Terminal,
  http: Globe,
  turn: Flag,
  panic: TriangleAlert,
}

/** The page's page size before the reader picks another (10/20/50 offered). */
const PAGE_SIZE = 50

/** How long a keystroke waits before the search asks the gateway. */
const SEARCH_DEBOUNCE_MS = 300

const now = () => Date.now()

const filters = ref<LogFilters>(initialFilters())
const page = ref(1)
const pageSize = ref(PAGE_SIZE)
const records = shallowRef<LogRecord[]>([])
const total = ref(0)
const levelCounts = ref<Record<LogLevel, number>>({ DEBUG: 0, INFO: 0, WARN: 0, ERROR: 0 })
const buckets = shallowRef<LogBucket[]>([])
const truncated = ref(false)
const error = ref('')
const selectedId = ref('')
const flashId = ref('')
const spinning = ref(false)
const pop = ref<'' | 'time' | 'session'>('')
const searchText = ref(filters.value.q)
const sessionQuery = ref('')
const sessionTitle = ref('')
const sessionOptions = shallowRef<{ id: string; title: string; project: string; source: string }[]>([])
const projects = shallowRef<{ id: string; name: string }[]>([])
const customFrom = ref('')
const customTo = ref('')
const customError = ref('')
const barsEl = ref<HTMLElement | null>(null)
const brush = ref<{ left: number; width: number } | null>(null)

/** The newest request's number: an answer that is not it is dropped. */
let latest = 0
let searchTimer: ReturnType<typeof setTimeout> | null = null
let sessionTimer: ReturnType<typeof setTimeout> | null = null
let flashTimer: ReturnType<typeof setTimeout> | null = null
/** The URL's `focus` record has been opened once; a later page never reopens it. */
let focusOpened = false

function initialFilters(): LogFilters {
  const base = defaultFilters(now())
  const session = String(props.session ?? '').trim()
  if (session) base.session = session
  const from = Date.parse(String(props.from ?? ''))
  if (!Number.isNaN(from)) {
    const to = Date.parse(String(props.to ?? ''))
    base.preset = 'custom'
    base.from = from
    base.to = Number.isNaN(to) ? now() : to
  }
  return base
}

/** The range the page is showing: what the histogram and the table both read. */
const range = computed(() => rangeOf(filters.value.preset, now(), { from: filters.value.from, to: filters.value.to }))

const multiDay = computed(() => formatLogDay(new Date(range.value.from).toISOString()) !== formatLogDay(new Date(range.value.to).toISOString()))

const timeLabel = computed(() => {
  const preset = filters.value.preset
  if (preset !== 'custom') return t(LOG_PRESET_LABEL[preset])
  const { from, to } = range.value
  const start = formatLogShort(new Date(from).toISOString())
  const end = formatLogShort(new Date(to).toISOString())
  // Two ends of the same day name the day once.
  return `${start} – ${multiDay.value ? end : end.slice(6)}`
})

/** The histogram's own words: the count, and the range it counts. */
const countLabel = computed(() =>
  truncated.value ? t('logs.totalAtLeast', { total: total.value }) : t('logs.total', { total: total.value }),
)

/** The pager's own count line, when the scan stopped short of the whole range. */
const totalLabel = computed(() => (truncated.value ? t('logs.totalAtLeast', { total: total.value }) : ''))

const rangeText = computed(() => {
  const { from, to } = range.value
  return `${formatLogShort(new Date(from).toISOString())} ${t('logs.to')} ${formatLogShort(new Date(to).toISOString())}`
})

/** The axis's three labels: the two ends and the middle of the range. */
const axis = computed(() => {
  const { from, to } = range.value
  const withDay = multiDay.value
  const at = (ms: number) => formatLogShort(new Date(ms).toISOString(), withDay)
  return [at(from), at(from + (to - from) / 2), at(to)] as const
})

/**
 * The server's 60 buckets as stacked bars, tallest slice first: errors sit at
 * the bottom, debug on top, each bar scaled against the busiest bucket.
 */
const bars = computed(() => {
  const list = buckets.value.length ? buckets.value : emptyBuckets()
  const tallest = Math.max(1, ...list.map((bucket) => bucket.error + bucket.warn + bucket.info + bucket.debug))
  return list.map((bucket) => {
    const out: { level: LogLevel; height: string }[] = []
    for (const [level, key] of [
      ['ERROR', 'error'],
      ['WARN', 'warn'],
      ['INFO', 'info'],
      ['DEBUG', 'debug'],
    ] as const) {
      const count = bucket[key]
      if (count > 0) out.push({ level, height: `${((count / tallest) * 100).toFixed(1)}%` })
    }
    return out
  })
})

function emptyBuckets(): LogBucket[] {
  return Array.from({ length: 60 }, () => ({ t: '', debug: 0, info: 0, warn: 0, error: 0 }))
}

function barTip(index: number): string {
  const list = buckets.value.length ? buckets.value : emptyBuckets()
  const bucket = list[index]
  const { from, to } = range.value
  const at = formatLogShort(bucket.t || new Date(from + ((to - from) * index) / 60).toISOString())
  const count = bucket.debug + bucket.info + bucket.warn + bucket.error
  return `${at}　${t('logs.countUnit', { n: count })}`
}

const columns = computed<DataColumn[]>(() => [
  { id: 'time', header: t('logs.colTime'), size: multiDay.value ? 158 : 118 },
  { id: 'level', header: t('logs.colLevel'), size: 78 },
  { id: 'source', header: t('logs.colSource'), size: 96 },
  { id: 'session', header: t('logs.colSession'), size: 188 },
  { id: 'content', header: t('logs.colContent') },
])

const selected = computed(() => records.value.find((record) => record.id === selectedId.value) ?? null)

const sessionLabel = computed(() => {
  if (!filters.value.session) return t('logs.allSessions')
  return sessionTitle.value || filters.value.session.slice(0, 12)
})

function rowAttrs(row: LogRecord): Record<string, string | undefined> {
  return { 'data-log-row': row.id, tabindex: '0' }
}

function rowClass(row: LogRecord): Record<string, boolean> {
  return {
    'row--open': row.id === selectedId.value,
    'row--flash': row.id === flashId.value,
    'row--error': row.lvl === 'ERROR',
  }
}

function agentTag(row: LogRecord): string {
  return row.agent && row.agent !== 'main' ? row.agent : ''
}

function kindIcon(row: LogRecord): (typeof Bot) | null {
  return KIND_ICON[row.kind] ?? null
}

function sessionText(row: LogRecord): string {
  return row.sessionTitle || (row.session ?? '').slice(0, 12)
}

function statusTone(status: number): 'ok' | 'wn' | 'bad' {
  if (status >= 500) return 'bad'
  if (status >= 400) return 'wn'
  return 'ok'
}

function sourceText(source: string): string {
  const key = LOG_SOURCE_LABEL[source]
  return key ? t(key) : source
}

function toolName(row: LogRecord): string {
  return attrText(row, 'tool') || row.msg
}

function toolPreview(row: LogRecord): string {
  const text = attrText(row, 'argsPreview') || attrText(row, 'resultPreview') || attrText(row, 'args') || ''
  return text.slice(0, 90)
}

function attrPreview(row: LogRecord): { key: string; value: string }[] {
  return Object.entries(row.attrs ?? {})
    .slice(0, 4)
    .map(([key, value]) => ({ key, value: typeof value === 'string' ? value : JSON.stringify(value) }))
}

async function load(): Promise<void> {
  const sequence = ++latest
  const query = toQuery(filters.value, page.value, pageSize.value, now())
  error.value = ''
  if (!query) {
    // Nothing switched on is not a request: the gateway reads an empty level as
    // its default three, so the empty table is the page's own answer.
    records.value = []
    total.value = 0
    levelCounts.value = { DEBUG: 0, INFO: 0, WARN: 0, ERROR: 0 }
    buckets.value = []
    truncated.value = false
    return
  }
  try {
    const answer = await forebrainApi.logs(query)
    if (sequence !== latest) return
    records.value = Array.isArray(answer?.records) ? answer.records : []
    total.value = Number(answer?.total ?? 0)
    page.value = Number(answer?.page ?? page.value)
    levelCounts.value = levelCountsOf(answer?.levelCounts)
    buckets.value = Array.isArray(answer?.buckets) ? answer.buckets : []
    truncated.value = Boolean(answer?.truncated)
    if (filters.value.session && !sessionTitle.value) {
      const named = records.value.find((record) => record.session === filters.value.session)
      sessionTitle.value = named?.sessionTitle ?? ''
    }
    focusIfNamed()
  } catch (cause) {
    if (sequence !== latest) return
    // The filters stay as they are: a failure is something to retry, not a
    // reason to lose what the reader asked for.
    error.value = cause instanceof Error ? cause.message : String(cause)
  }
}

/** The URL's `focus` record: its drawer opens once the page holding it is read. */
function focusIfNamed(): void {
  if (focusOpened) return
  const id = String(props.focus ?? '').trim()
  if (!id) return
  const record = records.value.find((entry) => entry.id === id)
  if (!record) return
  focusOpened = true
  selectedId.value = id
  flash(id)
}

function flash(id: string): void {
  flashId.value = id
  if (flashTimer) clearTimeout(flashTimer)
  flashTimer = setTimeout(() => {
    flashTimer = null
    flashId.value = ''
  }, 1800)
}

function applyPreset(preset: Exclude<LogPreset, 'custom'>): void {
  filters.value.preset = preset
  pop.value = ''
  page.value = 1
  void load()
}

function applyCustom(): void {
  const from = customFrom.value ? Date.parse(`${customFrom.value}:00`) : Number.NaN
  const to = customTo.value ? Date.parse(`${customTo.value}:00`) : Number.NaN
  if (Number.isNaN(from) || Number.isNaN(to) || from >= to) {
    customError.value = t('logs.customInvalid')
    return
  }
  customError.value = ''
  filters.value.preset = 'custom'
  filters.value.from = from
  filters.value.to = to
  pop.value = ''
  page.value = 1
  void load()
}

function toggleLevel(level: LogLevel): void {
  const levels = filters.value.levels
  filters.value.levels = levels.includes(level) ? levels.filter((entry) => entry !== level) : [...levels, level]
  page.value = 1
  void load()
}

function allLevels(): void {
  filters.value.levels = [...LOG_LEVELS]
  page.value = 1
  void load()
}

function setSource(source: string): void {
  filters.value.source = source
  pop.value = ''
  page.value = 1
  void load()
}

function setProject(project: string): void {
  filters.value.project = project
  page.value = 1
  void load()
  void loadSessions()
}

function setKind(kind: string): void {
  filters.value.kind = kind
  page.value = 1
  void load()
}

function setSession(session: string, title = ''): void {
  filters.value.session = session
  sessionTitle.value = session ? title : ''
  pop.value = ''
  page.value = 1
  void load()
}

/** 「只看这一轮」 from a record's drawer: the filters narrow to the turn. */
function setRun(run: string): void {
  filters.value.q = run
  searchText.value = run
  page.value = 1
  void load()
}

function goPage(next: number): void {
  page.value = next
  void load()
}

function goPageSize(next: number): void {
  pageSize.value = next
  page.value = 1
  void load()
}

function reset(): void {
  filters.value = defaultFilters(now())
  searchText.value = ''
  sessionTitle.value = ''
  page.value = 1
  pop.value = ''
  customError.value = ''
  void load()
}

function refresh(): void {
  spinning.value = true
  setTimeout(() => {
    spinning.value = false
  }, 500)
  void load()
}

function togglePop(which: 'time' | 'session'): void {
  pop.value = pop.value === which ? '' : which
  if (pop.value === 'time') {
    customFrom.value = inputValue(range.value.from)
    customTo.value = inputValue(range.value.to)
    customError.value = ''
  }
  if (pop.value === 'session') {
    sessionQuery.value = ''
    void loadSessions()
  }
}

function inputValue(ms: number): string {
  const at = new Date(ms)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`
}

/** The conversation menu's rows: the server's own search, one page of it. */
async function loadSessions(): Promise<void> {
  try {
    const answer = await forebrainApi.chatSessions('', {
      page: 1,
      pageSize: 20,
      q: sessionQuery.value.trim() || undefined,
      projectId: filters.value.project || undefined,
    })
    sessionOptions.value = (answer?.records ?? []).map((record) => ({
      id: record.id,
      title: record.title ?? '',
      project: record.projectName ?? '',
      source: record.source ? sourceText(record.source) : '',
    }))
    // A conversation named by the URL is named here too: the label must read as
    // its title even where the range holds none of its records.
    if (filters.value.session && !sessionTitle.value) {
      sessionTitle.value = sessionOptions.value.find((option) => option.id === filters.value.session)?.title ?? ''
    }
  } catch {
    sessionOptions.value = []
  }
}

/** Project names for the project menu: one page of the existing list endpoint. */
async function loadProjects(): Promise<void> {
  try {
    const answer = await forebrainApi.projectsPage({ page: 1, pageSize: 100 })
    projects.value = answer.rows.map((project) => ({ id: project.id, name: project.name }))
  } catch {
    projects.value = []
  }
}

function onRowClick(event: MouseEvent): void {
  const row = (event.target as HTMLElement | null)?.closest?.('[data-log-row]') as HTMLElement | null
  if (!row) return
  const id = row.dataset.logRow ?? ''
  if (!id) return
  openRecord(id)
}

function onRowEnter(event: KeyboardEvent): void {
  const row = event.target as HTMLElement | null
  const id = row?.dataset?.logRow ?? ''
  if (!id) return
  event.preventDefault()
  openRecord(id)
}

function openRecord(id: string): void {
  selectedId.value = id
  flashId.value = ''
}

// —— the histogram's brush: press, drag, release = a range ——
let brushStart: number | null = null
let brushEnd: number | null = null

function barPosition(event: MouseEvent): number {
  const el = barsEl.value
  if (!el) return 0
  const rect = el.getBoundingClientRect()
  if (!rect.width) return 0
  return Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width))
}

function startBrush(event: MouseEvent): void {
  brushStart = barPosition(event)
  brushEnd = brushStart
  brush.value = { left: brushStart * 100, width: 0 }
  window.addEventListener('mousemove', moveBrush)
  window.addEventListener('mouseup', endBrush)
}

function moveBrush(event: MouseEvent): void {
  if (brushStart === null) return
  brushEnd = barPosition(event)
  const left = Math.min(brushStart, brushEnd)
  const width = Math.abs(brushEnd - brushStart)
  brush.value = { left: left * 100, width: width * 100 }
}

function endBrush(): void {
  window.removeEventListener('mousemove', moveBrush)
  window.removeEventListener('mouseup', endBrush)
  if (brushStart === null) return
  let left = Math.min(brushStart, brushEnd ?? brushStart)
  let right = Math.max(brushStart, brushEnd ?? brushStart)
  brushStart = null
  brushEnd = null
  brush.value = null
  // A press that moved less than one bar narrows to that bar.
  if (right - left < 1 / 60) {
    const index = Math.floor(left * 60)
    left = index / 60
    right = (index + 1) / 60
  }
  const { from, to } = range.value
  filters.value.preset = 'custom'
  filters.value.from = Math.round(from + (to - from) * left)
  filters.value.to = Math.round(from + (to - from) * right)
  page.value = 1
  void load()
}

onMounted(() => {
  void load()
  void loadProjects()
  void loadSessions()
})

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  if (sessionTimer) clearTimeout(sessionTimer)
  if (flashTimer) clearTimeout(flashTimer)
  window.removeEventListener('mousemove', moveBrush)
  window.removeEventListener('mouseup', endBrush)
})

// Typing asks the gateway once the person pauses; the row count answers on the
// same request as everything else. The box's own value stays the visible one,
// so a debounce never eats a keystroke.
const searchModel = computed({
  get: () => searchText.value,
  set: (value: string) => {
    searchText.value = value
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
      searchTimer = null
      const text = value.trim()
      if (filters.value.q === text) return
      filters.value.q = text
      page.value = 1
      void load()
    }, SEARCH_DEBOUNCE_MS)
  },
})

const sessionSearchModel = computed({
  get: () => sessionQuery.value,
  set: (value: string) => {
    sessionQuery.value = value
    if (sessionTimer) clearTimeout(sessionTimer)
    sessionTimer = setTimeout(() => {
      sessionTimer = null
      void loadSessions()
    }, SEARCH_DEBOUNCE_MS)
  },
})
</script>

<style scoped>
.logs {
  --lv-d: #98a2b3;
  --lv-i: #3a67c6;
  --lv-w: #dc8a14;
  --lv-e: #d92d20;
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.ic {
  width: 14px;
  height: 14px;
  flex: none;
}

/* ---------- filter bar ---------- */
.lbar {
  position: relative;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.anchor {
  position: relative;
  display: inline-flex;
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
  background: var(--forebrain-button-alt-bg);
  font-size: 12.5px;
  font-weight: 500;
  color: var(--forebrain-text);
  white-space: nowrap;
}
.btn:hover {
  background: var(--forebrain-button-alt-hover-bg);
}
.btn.pri {
  background: var(--forebrain-brand-1);
  border-color: var(--forebrain-brand-1);
  color: #f5ffff;
}
.btn.pri:hover {
  background: var(--forebrain-brand-hover);
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
.btn:focus-visible,
.icbtn:focus-visible,
.combo:focus-visible,
.lv:focus-visible,
.seg button:focus-visible {
  outline: 2px solid var(--forebrain-brand-1);
  outline-offset: 1px;
}
.icbtn {
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
  color: var(--forebrain-muted-text);
}
.icbtn:hover {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-text);
}
.spin {
  animation: logs-spin 0.5s linear;
}
@keyframes logs-spin {
  from {
    transform: rotate(0);
  }
  to {
    transform: rotate(360deg);
  }
}
.fld {
  height: 32px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  padding: 0 10px;
  background: var(--forebrain-input-bg);
  font: inherit;
  font-size: 13px;
  color: var(--forebrain-text);
  outline: none;
}
.fld:focus {
  border-color: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
}
select.fld {
  min-width: 112px;
  width: auto;
}
select.fld.set {
  border-color: var(--forebrain-brand-border);
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}
.combo {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
  font-size: 13px;
  text-align: left;
  white-space: nowrap;
  color: var(--forebrain-text);
}
.combo.time {
  min-width: 206px;
}
.combo.sess {
  width: 232px;
}
.combo .v {
  overflow: hidden;
  text-overflow: ellipsis;
}
.combo .ph {
  color: var(--forebrain-muted-text);
}
.combo > .ic:first-child {
  color: var(--forebrain-muted-text);
}
.combo > .ic:last-child {
  margin-left: auto;
  color: var(--forebrain-muted-text);
}
.combo.open {
  border-color: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
}
.combo.set {
  border-color: var(--forebrain-brand-border);
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}
.combo.set > .ic {
  color: var(--forebrain-brand-1);
}
.ic-x {
  display: inline-grid;
  place-items: center;
  width: 18px;
  height: 18px;
  margin-left: auto;
  border-radius: 4px;
  color: var(--forebrain-brand-1);
  cursor: pointer;
}
.ic-x:hover {
  background: var(--forebrain-brand-border);
}
.ic-x .ic {
  width: 12px;
  height: 12px;
}
.cpop {
  position: absolute;
  z-index: 12;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
  box-shadow: 0 12px 32px rgba(16, 24, 40, 0.14);
  padding: 4px;
}
.tpop {
  left: 0;
  top: 38px;
  width: 300px;
}
.spop {
  left: 0;
  top: 38px;
  width: 340px;
  padding: 8px;
}
.spop .list {
  max-height: 280px;
  overflow: auto;
}
.spop .none {
  padding: 18px 8px;
  text-align: center;
  font-size: 12.5px;
  color: var(--forebrain-muted-text);
}
.copt {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  border: 0;
  background: transparent;
  border-radius: 7px;
  padding: 6px 8px;
  font-size: 13px;
  text-align: left;
  color: var(--forebrain-text);
}
.copt:hover {
  background: var(--forebrain-bg-alt);
}
.copt.on {
  color: var(--forebrain-brand-1);
  font-weight: 500;
}
.copt small {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  font-weight: 400;
  white-space: nowrap;
}
.copt .two {
  min-width: 0;
  flex: 1;
}
.copt .two b {
  display: block;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.copt .two span {
  display: block;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.tpop .cus {
  border-top: 1px solid var(--forebrain-divider);
  margin-top: 4px;
  padding: 8px;
}
.tpop .cus label {
  display: grid;
  grid-template-columns: 28px 1fr;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  margin-bottom: 6px;
}
.tpop .cus .fld {
  height: 30px;
  font-size: 12px;
}
.tpop .cus .btn {
  width: 100%;
}
.cus-error {
  display: block;
  margin-top: 6px;
  font-size: 12px;
  color: var(--forebrain-danger);
}
.lvs {
  display: inline-flex;
  gap: 4px;
}
.lv {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
  font-size: 12.5px;
  color: var(--forebrain-muted-text);
}
.lv i {
  width: 8px;
  height: 8px;
  border-radius: 2px;
  background: var(--forebrain-divider-strong);
}
.lv em {
  font-style: normal;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.lv[aria-pressed='true'] {
  color: var(--forebrain-text);
  border-color: var(--forebrain-divider-strong);
}
.lv[aria-pressed='true'] em {
  color: var(--forebrain-text-2);
}
.lv[aria-pressed='true'].lv--d i {
  background: var(--lv-d);
}
.lv[aria-pressed='true'].lv--i i {
  background: var(--lv-i);
}
.lv[aria-pressed='true'].lv--w i {
  background: var(--lv-w);
}
.lv[aria-pressed='true'].lv--e i {
  background: var(--lv-e);
}
.seg {
  display: inline-flex;
  background: var(--forebrain-button-alt-bg);
  border-radius: 7px;
  padding: 2px;
  gap: 2px;
}
.seg button {
  height: 28px;
  padding: 0 10px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  font-size: 12.5px;
  color: var(--forebrain-text-2);
}
.seg button[aria-checked='true'] {
  background: var(--forebrain-surface);
  color: var(--forebrain-text);
  font-weight: 500;
  box-shadow: 0 0 0 1px var(--forebrain-divider), 0 1px 2px rgba(16, 24, 40, 0.06);
}
.search {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  flex: 1;
  min-width: 180px;
  height: 32px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  padding: 0 10px;
  color: var(--forebrain-muted-text);
  background: var(--forebrain-input-bg);
}
.search:focus-within {
  border-color: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
}
.search input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: none;
  font: inherit;
  font-size: 13px;
  background: transparent;
  color: var(--forebrain-text);
}
.search input::placeholder {
  color: var(--forebrain-muted-text);
}

/* ---------- histogram ---------- */
.hist {
  margin-top: 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  padding: 10px 12px 6px;
  background: var(--forebrain-surface);
  user-select: none;
}
.hist-top {
  display: flex;
  align-items: baseline;
  gap: 10px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  margin-bottom: 6px;
}
.hist-top b {
  font-size: 13px;
  color: var(--forebrain-text);
  font-weight: 600;
}
.hist-top .sp {
  flex: 1;
}
.bars {
  position: relative;
  display: flex;
  align-items: flex-end;
  gap: 2px;
  height: 56px;
  cursor: crosshair;
  border-bottom: 1px solid var(--forebrain-divider);
}
.bar {
  flex: 1;
  display: flex;
  flex-direction: column-reverse;
  min-width: 0;
  height: 100%;
  justify-content: flex-start;
}
.bar span {
  display: block;
  width: 100%;
}
.bar .seg-e {
  background: var(--lv-e);
}
.bar .seg-w {
  background: var(--lv-w);
}
.bar .seg-i {
  background: var(--lv-i);
}
.bar .seg-d {
  background: var(--lv-d);
}
.bar span:last-child {
  border-radius: 2px 2px 0 0;
}
.bar:hover {
  background: var(--forebrain-bg-alt);
}
.brush {
  position: absolute;
  top: 0;
  bottom: 0;
  background: rgba(31, 74, 158, 0.12);
  border-left: 1px solid var(--forebrain-brand-1);
  border-right: 1px solid var(--forebrain-brand-1);
  pointer-events: none;
}
.axis {
  display: flex;
  justify-content: space-between;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
  padding-top: 4px;
}

/* ---------- the table ---------- */
.logtable {
  margin-top: 12px;
  min-width: 0;
}
.logtable :deep(.forebrain-table-wrap) {
  border-radius: 10px;
}
.logtable :deep(.forebrain-table th) {
  height: 34px;
  padding: 0 10px;
  font-size: 12px;
  font-weight: 500;
  vertical-align: middle;
}
.logtable :deep(.forebrain-table td) {
  height: 34px;
  padding: 0 10px;
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 12.5px;
  vertical-align: middle;
  /* One line a cell, ended by an ellipsis: the prototype's row (.ltbl td). */
  overflow: hidden;
  text-overflow: ellipsis;
}
.logtable :deep(.forebrain-table tbody tr) {
  cursor: pointer;
}
.logtable :deep(.forebrain-table tbody tr:hover) {
  background: var(--forebrain-bg-alt);
}
.logtable :deep(.forebrain-table tbody tr.row--open) {
  background: var(--forebrain-brand-soft);
}
.logtable :deep(.forebrain-table tbody tr.row--open td:first-child) {
  box-shadow: inset 2px 0 0 var(--forebrain-brand-1);
}
.logtable :deep(.forebrain-table tbody tr.row--error td) {
  color: var(--forebrain-danger);
}
.logtable :deep(.forebrain-table tbody tr.row--flash) {
  animation: logs-flash 1.8s ease-out;
}
@keyframes logs-flash {
  0%,
  30% {
    background: var(--forebrain-warning-soft);
  }
  100% {
    background: transparent;
  }
}
.tm {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-text-2);
}
.tm small {
  color: var(--forebrain-muted-text);
  font-size: 11px;
  margin-right: 6px;
}
.lvb {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-text-2);
}
.lvb i {
  width: 8px;
  height: 8px;
  border-radius: 2px;
}
.lvb.lv--d i {
  background: var(--lv-d);
}
.lvb.lv--i i {
  background: var(--lv-i);
}
.lvb.lv--w i {
  background: var(--lv-w);
}
.lvb.lv--e i {
  background: var(--lv-e);
}
.lvb.lv--w {
  color: var(--forebrain-warning);
}
.lvb.lv--e {
  color: var(--forebrain-danger);
  font-weight: 500;
}
.lvb.lv--d {
  color: var(--forebrain-muted-text);
}
.srcc {
  color: var(--forebrain-text-2);
}
.srcc small {
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
  margin-left: 5px;
}
.sc {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.sc span {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.sessb {
  flex: none;
  width: 22px;
  height: 22px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--forebrain-muted-text);
  display: grid;
  place-items: center;
  opacity: 0;
}
.sessb .ic {
  width: 12px;
  height: 12px;
}
:deep(.forebrain-table tbody tr:hover) .sessb,
.sessb:focus-visible {
  opacity: 1;
}
.sessb:hover {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}
.none {
  color: var(--forebrain-muted-text);
}
.m {
  color: inherit;
}
code {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
}
.kv {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  margin-left: 10px;
}
.kv b {
  font-weight: 400;
  color: var(--forebrain-muted-text);
  opacity: 0.85;
}
.ag {
  display: inline-flex;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  margin-right: 7px;
  border-radius: 4px;
  background: var(--forebrain-button-alt-bg);
  font-size: 11px;
  color: var(--forebrain-text-2);
  vertical-align: 1px;
}
.kd {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  margin-right: 7px;
  border-radius: 4px;
  color: var(--forebrain-muted-text);
  vertical-align: -4px;
}
.kd .ic {
  width: 13px;
  height: 13px;
}
.st {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  font-weight: 600;
  margin: 0 6px;
}
.st.ok {
  color: var(--forebrain-success);
}
.st.bad {
  color: var(--forebrain-danger);
}
.st.wn {
  color: var(--forebrain-warning);
}
.dur {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  margin-left: 8px;
}
.st + .dur {
  margin-left: 0;
}

/* ---------- empty, failure ---------- */
.empty {
  display: grid;
  place-items: center;
  align-content: center;
  gap: 10px;
  height: 220px;
  color: var(--forebrain-muted-text);
  font-size: 13px;
}
.empty .acts {
  display: flex;
  gap: 8px;
}
.failed {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
  padding: 12px 14px;
  border: 1px solid var(--forebrain-danger);
  border-radius: 10px;
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-danger);
  font-size: 13px;
}
</style>
