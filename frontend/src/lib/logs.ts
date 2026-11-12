/**
 * The log page's vocabulary (plan 288): the four levels, the time presets, the
 * filters the page holds, and the pure functions that turn them into a request
 * and a record into what its row says.
 *
 * Everything here is a function of its arguments — no clock, no fetch — so the
 * page's behaviour (which request a change makes, what a row reads as) is
 * pinned by tests rather than by the browser. The wire shapes are plan 287's
 * three endpoints: `GET /api/logs`, `GET /api/logs/exchange` and
 * `GET /api/logs/blob`.
 */
import type { I18nKey } from '@/locales'

/** The four levels the event log stores, in `slog.Level`'s own spelling. */
export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

/** Louder first, the order the filter bar draws them in. */
export const LOG_LEVELS: readonly LogLevel[] = ['DEBUG', 'INFO', 'WARN', 'ERROR']

/** Each level's words: Debug / Info / Warn / Error, 调试 / 信息 / 警告 / 错误. */
export const LOG_LEVEL_LABEL: Record<LogLevel, I18nKey> = {
  DEBUG: 'logs.levelDebug',
  INFO: 'logs.levelInfo',
  WARN: 'logs.levelWarn',
  ERROR: 'logs.levelError',
}

/**
 * What the page asks for before the reader touches a level: the three that
 * mean something happened. A successful read is DEBUG (plan 285), so a debug
 * on by default would bury the turn in access records.
 */
export const LOG_DEFAULT_LEVELS: readonly LogLevel[] = ['INFO', 'WARN', 'ERROR']

/** The sources the segmented control offers: '' is every source. */
export const LOG_SOURCES = ['', 'tui', 'gateway'] as const

/** A source's words: 终端 / 网关 / 命令行, and the default 全部. */
export const LOG_SOURCE_LABEL: Record<string, I18nKey> = {
  tui: 'logs.sourceTui',
  gateway: 'logs.sourceGateway',
  cli: 'logs.sourceCli',
}

/** The kinds the type menu offers: '' is every kind. */
export const LOG_KINDS = ['', 'log', 'turn', 'llm', 'tool', 'http', 'panic'] as const

/** A kind's words: 运行日志 / 轮次 / 模型调用 / 工具调用 / 接口访问 / 崩溃. */
export const LOG_KIND_LABEL: Record<string, I18nKey> = {
  '': 'logs.kindAll',
  log: 'logs.kindLog',
  turn: 'logs.kindTurn',
  llm: 'logs.kindLlm',
  tool: 'logs.kindTool',
  http: 'logs.kindHttp',
  panic: 'logs.kindPanic',
}

/**
 * A time preset. `custom` is the reader's own range: the filter bar's two
 * datetime fields, or a dragged segment of the histogram.
 */
export type LogPreset = '15m' | '1h' | 'today' | '7d' | 'custom'

/** The presets the time menu lists, in the design's order. */
export const LOG_PRESETS: readonly Exclude<LogPreset, 'custom'>[] = ['15m', '1h', 'today', '7d']

/** A preset's words: 最近 15 分钟 / 最近 1 小时 / 今天 / 最近 7 天. */
export const LOG_PRESET_LABEL: Record<Exclude<LogPreset, 'custom'>, I18nKey> = {
  '15m': 'logs.range15m',
  '1h': 'logs.range1h',
  today: 'logs.rangeToday',
  '7d': 'logs.range7d',
}

/** A range of instants, in milliseconds since the epoch. */
export interface LogRange {
  from: number
  to: number
}

/** Everything the filter bar holds. A page change keeps it; a reset restores it. */
export interface LogFilters {
  preset: LogPreset
  /** The custom range's own ends; ignored while `preset` is not 'custom'. */
  from: number
  to: number
  levels: LogLevel[]
  /** '' every source, else one of LOG_SOURCES. */
  source: string
  /** A project id, '' for every project. */
  project: string
  /** A conversation id, '' for every conversation. */
  session: string
  /** '' every kind, else one of LOG_KINDS. */
  kind: string
  q: string
}

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** The filters a freshly opened page holds: the last hour, the loud three. */
export function defaultFilters(now: number): LogFilters {
  return {
    preset: '1h',
    from: now - HOUR,
    to: now,
    levels: [...LOG_DEFAULT_LEVELS],
    source: '',
    project: '',
    session: '',
    kind: '',
    q: '',
  }
}

/**
 * The instants a preset covers, `now` included. `custom` is the range the
 * reader chose, returned exactly as given — the histogram and the table must
 * both read the range the request carried, not a rounded copy of it.
 */
export function rangeOf(preset: LogPreset, now: number, custom: LogRange): LogRange {
  switch (preset) {
    case '15m':
      return { from: now - 15 * MINUTE, to: now }
    case '1h':
      return { from: now - HOUR, to: now }
    case 'today': {
      const midnight = new Date(now)
      midnight.setHours(0, 0, 0, 0)
      return { from: midnight.getTime(), to: now }
    }
    case '7d':
      return { from: now - 7 * DAY, to: now }
    case 'custom':
      return custom
  }
}

/** RFC3339 to the second: what the gateway's `time.Parse(time.RFC3339, …)` takes. */
export function rfc3339(ms: number): string {
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/**
 * The query `GET /api/logs` is asked with, in the gateway's own parameter
 * names: the range, the dimensions the reader narrowed by, the page. Unset
 * dimensions are absent rather than empty, so the answer is the server's
 * default and never an empty-string filter.
 */
export interface LogQuery {
  from: string
  to: string
  /** The levels to include, comma separated; never empty (see `toQuery`). */
  level: string
  source?: string
  project?: string
  session?: string
  kind?: string
  q?: string
  page: number
  page_size: number
}

/**
 * The query `GET /api/logs` is asked with, or null when the reader has every
 * level switched off: the gateway reads an empty level as the default three,
 * so "nothing selected" is never a request — it is an empty table.
 */
export function toQuery(filters: LogFilters, page: number, pageSize: number, now: number): LogQuery | null {
  if (!filters.levels.length) return null
  const range = rangeOf(filters.preset, now, { from: filters.from, to: filters.to })
  const query: LogQuery = {
    from: rfc3339(range.from),
    to: rfc3339(range.to),
    level: LOG_LEVELS.filter((level) => filters.levels.includes(level)).join(','),
    page,
    page_size: pageSize,
  }
  if (filters.source) query.source = filters.source
  if (filters.project) query.project = filters.project
  if (filters.session) query.session = filters.session
  if (filters.kind) query.kind = filters.kind
  const text = filters.q.trim()
  if (text) query.q = text
  return query
}

/** How a record's content column renders it. */
export type LogRowKind = 'llm-request' | 'llm-response' | 'tool' | 'http' | 'plain'

/**
 * Which content column a record needs. A model call writes two records that
 * share one call id — the DEBUG request it sent and the response it read — and
 * the list draws each as its own row, so the response carries the call's own
 * numbers (status, duration, address, model) and the request its size.
 */
export function rowKind(record: Pick<LogRecord, 'kind' | 'msg'>): LogRowKind {
  if (record.kind === 'llm') return record.msg === 'llm request' ? 'llm-request' : 'llm-response'
  if (record.kind === 'tool') return 'tool'
  if (record.kind === 'http') return 'http'
  return 'plain'
}

/**
 * A duration as the prototype writes it: `858ms`, `31.9s`, `13m34s`. The
 * minute form keeps the seconds' two digits, so a row's numbers line up.
 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms)) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < MINUTE) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / MINUTE)}m${String(Math.round((ms % MINUTE) / 1000)).padStart(2, '0')}s`
}

/** A byte count as the prototype writes it: `156B`, `93.0KB`, `1.12MB`. */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—'
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / (1024 * 1024)).toFixed(2)}MB`
}

function pad(value: number, width: number): string {
  return String(value).padStart(width, '0')
}

function parse(iso: string): Date | null {
  const at = Date.parse(iso)
  return Number.isNaN(at) ? null : new Date(at)
}

/** The row's own clock: local time to the millisecond, `11:20:57.545`. */
export function formatLogClock(iso: string): string {
  const at = parse(iso)
  if (!at) return '—'
  return `${pad(at.getHours(), 2)}:${pad(at.getMinutes(), 2)}:${pad(at.getSeconds(), 2)}.${pad(at.getMilliseconds(), 3)}`
}

/** The row's month and day, shown ahead of the clock when a range spans days. */
export function formatLogDay(iso: string): string {
  const at = parse(iso)
  if (!at) return '—'
  return `${pad(at.getMonth() + 1, 2)}-${pad(at.getDate(), 2)}`
}

/** A moment read in full: `2026-11-11 11:20:57`, the drawer's header and facts. */
export function formatLogFull(iso: string): string {
  const at = parse(iso)
  if (!at) return '—'
  return `${at.getFullYear()}-${formatLogDay(iso)} ${pad(at.getHours(), 2)}:${pad(at.getMinutes(), 2)}:${pad(at.getSeconds(), 2)}`
}

/** A moment read in the histogram's axis: `11-11 11:20`, or `11:20` inside a day. */
export function formatLogShort(iso: string, withDay = true): string {
  const at = parse(iso)
  if (!at) return '—'
  const clock = `${pad(at.getHours(), 2)}:${pad(at.getMinutes(), 2)}`
  return withDay ? `${formatLogDay(iso)} ${clock}` : clock
}

/** An endpoint as a row shows it: the host and the path, never the query. */
export function shortURL(url: string): string {
  try {
    const parsed = new URL(url)
    return parsed.host + parsed.pathname
  } catch {
    return url
  }
}

/** One field of a record's `attrs`, as the wire carried it. */
export function attrText(record: Pick<LogRecord, 'attrs'>, key: string): string {
  const value = record.attrs?.[key]
  if (value === undefined || value === null) return ''
  return typeof value === 'string' ? value : String(value)
}

/** One numeric field of a record's `attrs`; 0 when the field is absent or not a number. */
export function attrNumber(record: Pick<LogRecord, 'attrs'>, key: string): number {
  const value = record.attrs?.[key]
  if (typeof value === 'number') return value
  if (typeof value === 'string' && value.trim() !== '' && Number.isFinite(Number(value))) return Number(value)
  return 0
}

/** A payload reference: where one call's request or response text lives. */
export interface LogBlobRef {
  /** The file's name under the day's `x/`. */
  name: string
  /** The whole payload's size on disk. */
  bytes: number
  /** False when only the head and tail were kept. */
  whole: boolean
  /** Bytes left out between the head and the tail, when `whole` is false. */
  omitted?: number
}

// Every object below is what the page reads *after* `@/lib/api`'s response
// pass: the gateway writes snake_case, and every response is camelCased once on
// the way in (`toCamelCase` in `lib/case.ts`). So a record's `session_title` is
// `sessionTitle` here, and an attribute's `dur_ms` is `durMs` — including the
// ones inside a record's own `attrs` map, which the pass walks as deep as it goes.

/** A level tally as the wire carries it: a map's keys are camelCased with every
 *  other key, so the four level names arrive lowercased. */
export interface LogLevelCounts {
  debug?: number
  info?: number
  warn?: number
  error?: number
}

/** The tally in the record vocabulary, so a level button reads one key. */
export function levelCountsOf(counts: LogLevelCounts | undefined): Record<LogLevel, number> {
  return {
    DEBUG: Number(counts?.debug ?? 0),
    INFO: Number(counts?.info ?? 0),
    WARN: Number(counts?.warn ?? 0),
    ERROR: Number(counts?.error ?? 0),
  }
}

/** One bucket of the histogram: the records of a slice of the range, by level. */
export interface LogBucket {
  t: string
  debug: number
  info: number
  warn: number
  error: number
}

/** One record, as `GET /api/logs` answers it. */
export interface LogRecord {
  /** `<day>/<file name>:<line offset>`, stable across queries. */
  id: string
  day: string
  source: string
  host: string
  pid: number
  proc?: string
  /** RFC3339 with milliseconds and the writer's own offset. */
  t: string
  lvl: LogLevel
  kind: string
  msg: string
  project?: string
  session?: string
  worker?: string
  run?: string
  agent?: string
  at?: string
  err?: string
  attrs?: Record<string, unknown>
  blobs?: Record<string, LogBlobRef>
  projectId?: string
  projectName?: string
  sessionTitle?: string
}

/** The answer to one page of `GET /api/logs`. */
export interface LogPage {
  records: LogRecord[]
  total: number
  page: number
  pageSize: number
  /** Rows matching every condition but the level, so each button can say how many it would add. */
  levelCounts?: LogLevelCounts
  /** 60 slices of the requested range, by level: the histogram. */
  buckets: LogBucket[]
  /** The scan stopped at its ceiling: `total` is a lower bound. */
  truncated: boolean
}

/** The request half of one model call. */
export interface LogExchangePart {
  t: string
  method?: string
  url?: string
  model?: string
  bytes: number
  whole: boolean
  omitted?: number
  blob?: string
}

/** The response half: how the call ended and how long it took. */
export interface LogExchangeReply {
  t: string
  status: number
  outcome?: string
  durMs: number
  ttfbMs: number
  maxGapMs: number
  bytes: number
  blob?: string
}

/** Who made the call: the record's own facts, restored from the file's name. */
export interface LogExchangeScope {
  source: string
  host: string
  pid: number
  proc?: string
  project?: string
  session?: string
  worker?: string
  run?: string
  agent?: string
  projectName?: string
  sessionTitle?: string
}

/** Another call that was in flight while this one was. */
export interface LogExchangeCall {
  id: string
  day: string
  session?: string
  agent?: string
  t0: string
  t1: string
  status: number
  sessionTitle?: string
}

/** One tool call a response asked for, with its arguments as the protocol sent them. */
export interface LogSummaryToolCall {
  name: string
  arguments: string
}

/** A response's token counts. */
export interface LogSummaryUsage {
  input: number
  output: number
}

/** What a response body carried, for display: what a reader wants from a call that worked. */
export interface LogResponseSummary {
  text: string
  reasoning: string
  toolCalls?: LogSummaryToolCall[]
  finishReason?: string
  usage?: LogSummaryUsage
  /** How many events the body carried. */
  events: number
}

/** The answer to `GET /api/logs/exchange`: one call, its payload names and its neighbours. */
export interface LogExchange {
  id: string
  day: string
  request: LogExchangePart | null
  response: LogExchangeReply | null
  scope: LogExchangeScope
  summary: LogResponseSummary | null
  concurrent: LogExchangeCall[]
}

/** What a payload read answered: the bytes and the whole file's size. */
export interface LogBlobText {
  text: string
  /** `X-Log-Blob-Bytes`: the whole payload's size, whatever window this read carried. */
  total: number
}

/**
 * Split a payload file's text into the head it starts with (the request or
 * response line plus its header lines) and the body that follows the blank
 * line. A payload with no blank line has no head to show separately.
 */
export function splitPayload(text: string): { head: string; body: string } {
  const at = text.indexOf('\n\n')
  if (at < 0) return { head: '', body: text }
  return { head: text.slice(0, at), body: text.slice(at + 2) }
}

/**
 * Whether a payload's body is a JSON document, which draws in the code view;
 * a request trimmed to head and tail is not one, and reads as text.
 */
export function looksLikeJSON(text: string): boolean {
  const trimmed = text.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return false
  try {
    JSON.parse(trimmed)
    return true
  } catch {
    return false
  }
}
