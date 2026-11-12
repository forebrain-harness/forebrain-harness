/**
 * The settings page's form description (plan 191 §4.1): every group and field
 * the page draws, as the same shape the prototype describes them in.
 *
 * The prototype (`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`)
 * is the acceptance standard: its `FORMS`, `TBSW`, `LSPM` and `PRESETS`
 * constants, field for field, group for group. This file is that description
 * for the app — same groups, same order, same paths, same kinds, same units,
 * ranges, defaults and visibility rules — with its text living in the locale
 * catalogs so both languages say the same thing.
 *
 * A field's `path` is the configuration path `PATCH /api/settings` writes; the
 * gateway's registry (pkg/gateway/settings_paths.go) is the list of paths it
 * will accept, and every path here is one of them. Text keys:
 *
 *   settings.field.<path>   the field's name
 *   settings.hint.<path>    its grey explanation
 *   settings.enum.<path>.<value>  one option's name
 *
 * Changing the page therefore starts with the prototype (plan 191 §7): the
 * prototype, then this file, then the components — and the prototype test in
 * `settingsSchema.test.ts` compares the two, so a deviation shows up as a
 * failing test rather than as a review note.
 *
 * One block is deliberately outside this description (plan 299): 设置 · 子代理's
 * 「/goal 检查模型」. The prototype appends it from its own `systemSubagentsHTML()`
 * rather than listing it in `FORMS`, and its two rows are controls this schema
 * cannot express — a two-line model signature, a caret that becomes a clear x,
 * and an effort list that depends on the chosen model row. It is hand-written in
 * `GoalCheckModelGroup.vue`, sits beside `SettingsTabForm` rather than in
 * `SETTINGS_FORMS`, and must not be added here: the block's shape is the reason
 * the tab's groups still match the prototype exactly.
 */

/** How a field is drawn. */
export type SettingsKind =
  | 'bool'
  | 'num'
  | 'enum'
  | 'text'
  | 'area'
  | 'list'
  | 'tags'
  | 'pairs'
  | 'model'
  | 'secret'
  | 'preset'

/** One option of an enum field. The value is what the configuration stores. */
export interface SettingsOption {
  value: string
  labelKey: string
}

export interface SettingsField {
  /** The configuration path this row reads and writes. */
  path: string
  kind: SettingsKind
  /** The value the row shows when the stored one is unset. */
  def?: unknown
  /** Lower bound of a number, and the range hint's left side. */
  min?: number
  max?: number
  /**
   * Whether a number out of range is stored at the edge instead of being
   * flagged: the prototype's `clamp` (its blur rule), which mirrors what the
   * loader does with the value anyway.
   */
  clamp?: boolean
  /** The unit drawn inside a number box (%, 次, 秒, 天, 条, 字, 词元, 毫秒, 台). */
  unit?: string
  /** A range written differently from min–max, e.g. 0 或 60–86400. */
  range?: string
  /** The value a number accepts when the plain min/max pair is not enough. */
  ok?: (value: number) => boolean
  options?: SettingsOption[]
  /** The placeholder of a text, tags or list field. */
  placeholder?: string
  /** How many rows a textarea shows. */
  rows?: number
  /** Monospace text: an address, a path, a tokenizer id. */
  mono?: boolean
  /** The 「添加」button of a list or a pairs field. */
  addLabelKey?: string
  /** A pairs field's two column names. */
  columns?: [string, string]
  /** A pairs field's two placeholders. */
  placeholders?: [string, string]
  /** The field's control sits under its name, not beside it. */
  stack?: boolean
  /** A switch drawn in the warning tone (the prototype's `warn`). */
  warn?: boolean
  /** The 「恢复默认」button a textarea offers. */
  restore?: boolean
  /** Draw only while this holds — the prototype's `when(draft)`. */
  when?: (draft: Record<string, unknown>) => boolean
  /**
   * Whether the value travels as a credential: the gateway answers only
   * whether one is set and its last four characters (plan 191 §0.20).
   */
  secret?: boolean
}

export interface SettingsGroup {
  id: string
  /** The group's title; absent for a lone field, which shows its own name. */
  titleKey: string
  /** The long explanation beside the title (the prototype's `note`). */
  noteKey?: string
  fields: SettingsField[]
  /**
   * Whether a single-field group still shows the field's name — the batch
   * project group is the one exception the prototype draws.
   */
  keepLabel?: boolean
  /**
   * Whether the group's single field's name is drawn in the group-title type
   * instead of a row label (the memory switch).
   */
  titleLabel?: boolean
  /** What the group is hidden behind (the memory switch's own value). */
  when?: (draft: Record<string, unknown>) => boolean
}

/** The approval presets, in the prototype's order and with its wording. */
export const APPROVAL_PRESETS: { value: string; labelKey: string; descriptionKey: string }[] = [
  { value: 'workspace_write', labelKey: 'settings.preset.workspaceWrite', descriptionKey: 'settings.preset.workspaceWriteDesc' },
  { value: 'full_access', labelKey: 'settings.preset.fullAccess', descriptionKey: 'settings.preset.fullAccessDesc' },
]

const WS = 'agents.defaults.web_search'
const WF = 'agents.defaults.web_fetch'
const TE = 'agents.defaults.token_estimate'
const CI = 'agents.defaults.context_inject'
const GR = 'agents.defaults.guardrails'

const field = (path: string, kind: SettingsKind, extra: Partial<SettingsField> = {}): SettingsField => ({
  path,
  kind,
  ...extra,
})

/** Every group of the settings page, keyed by the tab that draws it. */
export const SETTINGS_FORMS: Record<string, SettingsGroup[]> = {
  approval: [
    {
      id: 'preset',
      titleKey: 'settings.groupPreset',
      noteKey: 'settings.groupPresetNote',
      fields: [field('approval_preset', 'preset')],
    },
    {
      id: 'sww',
      titleKey: 'settings.groupSandbox',
      fields: [
        field('sandbox_workspace_write.writable_roots', 'list', {
          placeholder: '/绝对/路径',
          addLabelKey: 'settings.addDirectory',
        }),
        field('sandbox_workspace_write.network_access', 'bool', { def: true }),
      ],
      when: (draft) => draft['approval_preset'] === 'workspace_write',
    },
    {
      id: 'batch',
      titleKey: 'settings.groupBatchProject',
      keepLabel: true,
      fields: [field('batch.sandbox_default', 'bool', { def: true })],
    },
  ],
  subagents: [
    {
      id: 'sa',
      titleKey: 'settings.groupSubagents',
      fields: [
        field('agents.defaults.enable_subagent', 'bool', { def: false }),
        field('agents.defaults.execution.max_parallel_subagents', 'num', { min: 0, def: 7 }),
      ],
    },
    {
      id: 'peers',
      titleKey: 'settings.groupPeers',
      fields: [field('peers.claude_code', 'bool', { def: false })],
      // Meta-driven, like the prototype's: claude_code_available rides along
      // with the snapshot's meta into the draft, and without Claude Code on
      // the machine the whole group — the section's only row — is gone
      // (owner 2026-10-30, plan 195 §3.7).
      when: (draft) => draft['claude_code_available'] === true,
    },
  ],
  net: [
    {
      id: 'ws',
      titleKey: 'settings.groupWebSearch',
      fields: [
        field(`${WS}.provider`, 'enum', {
          def: '',
          stack: true,
          options: [
            { value: '', labelKey: 'settings.enum.disabled' },
            { value: 'tavily', labelKey: 'settings.enum.tavily' },
            { value: 'brave', labelKey: 'settings.enum.brave' },
            { value: 'baidu', labelKey: 'settings.enum.baidu' },
            { value: 'deepseek', labelKey: 'settings.enum.deepseek' },
          ],
        }),
        field(`${WS}.tavily.api_key`, 'secret', { secret: true, when: (d) => d[`${WS}.provider`] === 'tavily' }),
        field(`${WS}.tavily.search_depth`, 'enum', {
          def: 'basic',
          when: (d) => d[`${WS}.provider`] === 'tavily',
          options: [
            { value: 'basic', labelKey: 'settings.enum.basic' },
            { value: 'advanced', labelKey: 'settings.enum.advanced' },
          ],
        }),
        field(`${WS}.brave.api_key`, 'secret', { secret: true, when: (d) => d[`${WS}.provider`] === 'brave' }),
        field(`${WS}.baidu.api_key`, 'secret', { secret: true, when: (d) => d[`${WS}.provider`] === 'baidu' }),
        field(`${WS}.deepseek.api_key`, 'secret', { secret: true, when: (d) => d[`${WS}.provider`] === 'deepseek' }),
      ],
    },
    {
      id: 'wf',
      titleKey: 'settings.groupWebFetch',
      fields: [
        field(`${WF}.provider`, 'enum', {
          def: '',
          options: [
            { value: '', labelKey: 'settings.enum.builtinFetch' },
            { value: 'deepseek', labelKey: 'settings.enum.deepseek' },
          ],
        }),
        field(`${WF}.deepseek.api_key`, 'secret', { secret: true, when: (d) => d[`${WF}.provider`] === 'deepseek' }),
        field(`${WF}.allow_private_ip`, 'bool', { def: false, warn: true }),
      ],
    },
  ],
  session: [
    {
      id: 'cp',
      titleKey: 'settings.groupCompaction',
      fields: [
        field('compact.auto_compact_percent', 'num', { min: 50, max: 90, clamp: true, unit: '%', def: 90 }),
        field('compact.prompt', 'area', { rows: 11, restore: true }),
      ],
    },
    {
      id: 'net2',
      titleKey: 'settings.groupRetry',
      fields: [
        field('agents.defaults.network_retry_max', 'num', { min: 0, max: 15, clamp: true, unit: '次', def: 10 }),
        field('agents.defaults.stream_idle_timeout_seconds', 'num', { min: 0, max: 1800, clamp: true, unit: '秒', def: 300 }),
      ],
    },
    {
      id: 'ci',
      titleKey: 'settings.groupContextBudget',
      fields: [
        field(`${CI}.model_context_tokens`, 'num', { min: 0, unit: '词元', def: 200000 }),
        field(`${CI}.warn_remaining_percent`, 'num', { min: 1, max: 50, clamp: true, unit: '%', def: 12 }),
        field(`${CI}.max_pre_hook_rules_chars`, 'num', { min: 0, unit: '字', def: 120000 }),
      ],
    },
    {
      id: 'gr',
      titleKey: 'settings.groupGuardrails',
      fields: [
        field(`${GR}.input.max_runes`, 'num', { min: 0, unit: '字', def: 200000 }),
        field(`${GR}.input.block_substrings`, 'tags', { placeholder: '输入后回车', stack: true }),
      ],
    },
    {
      id: 'te',
      titleKey: 'settings.groupTokenEstimate',
      fields: [
        field(`${TE}.download`, 'bool', { def: true }),
        field(`${TE}.hubs`, 'tags', { placeholder: '输入后回车', def: ['huggingface', 'modelscope'] }),
        field(`${TE}.huggingface_endpoint`, 'text', { mono: true, def: 'https://huggingface.co' }),
        field(`${TE}.modelscope_endpoint`, 'text', { mono: true, def: 'https://modelscope.cn' }),
        field(`${TE}.default_tokenizer`, 'text', { mono: true, def: 'gpt-o200k' }),
        field(`${TE}.tokenizers`, 'pairs', {
          columns: ['模型前缀', '分词器或绝对路径'],
          placeholders: ['glm-', 'glm-4'],
          addLabelKey: 'settings.addMapping',
        }),
      ],
    },
  ],
  memory: [
    {
      id: 'ms',
      titleKey: 'settings.groupMemoryFeature',
      titleLabel: true,
      fields: [field('features.memories', 'bool', { def: true })],
    },
    {
      id: 'mr',
      titleKey: 'settings.groupMemoryModels',
      when: memoryOn,
      fields: [
        field('memories.extract_model', 'model', { placeholder: '当前模型', def: '' }),
        field('memories.consolidation_model', 'model', { placeholder: '当前模型', def: '' }),
      ],
    },
    {
      id: 'mq',
      titleKey: 'settings.groupMemorySearch',
      when: memoryOn,
      fields: [
        field('memories.search_top_k', 'num', { min: 1, max: 20, clamp: true, unit: '条', def: 5 }),
        field('memories.max_raw_memories_for_consolidation', 'num', { min: 1, max: 4096, clamp: true, unit: '条', def: 256 }),
      ],
    },
    {
      id: 'mo',
      titleKey: 'settings.groupMemorySessions',
      noteKey: 'settings.groupMemorySessionsNote',
      when: memoryOn,
      fields: [
        field('memories.max_unused_days', 'num', { min: 0, max: 365, clamp: true, unit: '天', def: 30 }),
        field('memories.max_rollout_age_days', 'num', { min: 0, max: 90, clamp: true, unit: '天', def: 10 }),
        field('memories.max_rollouts_per_startup', 'num', { min: 1, max: 128, clamp: true, def: 2 }),
        field('memories.min_rollout_idle_hours', 'num', { min: 1, max: 48, clamp: true, unit: '小时', def: 6 }),
      ],
    },
  ],
  cron: [
    {
      id: 'cn',
      titleKey: 'settings.groupCron',
      fields: [field('cron.retention_days', 'num', { min: 1, max: 3650, unit: '天', def: 30 })],
    },
  ],
}

/** The memory section's own switch: the three groups below follow it. */
function memoryOn(draft: Record<string, unknown>): boolean {
  return draft['features.memories'] !== false
}

/**
 * A single switch drawn in its tab's toolbar and saved the moment it is flipped
 * — the language server and the skill offer (plan 191 §0.7).
 */
export const TOOLBAR_SWITCHES: Record<string, SettingsField> = {
  lsp: field('features.lsp', 'bool', { def: true }),
  'shared-skills': field('features.skill_offer', 'bool', { def: true }),
}

/** The language server tab's parameter group, drawn in a modal. */
export const LSP_PARAMETERS: SettingsGroup = {
  id: 'lspm',
  titleKey: 'settings.groupLspParameters',
  fields: [
    field('lsp.recommendations', 'bool', { def: true }),
    field('lsp.diagnostics.after_edit', 'bool', { def: true }),
    field('lsp.max_servers', 'num', { min: 1, max: 32, unit: '台', def: 6 }),
    field('lsp.idle_timeout', 'num', {
      min: 0,
      max: 86400,
      unit: '秒',
      def: 600,
      range: '0 或 60–86400',
      ok: (value) => value === 0 || (value >= 60 && value <= 86400),
    }),
    field('lsp.request_timeout', 'num', { min: 1, max: 600, unit: '秒', def: 30 }),
    field('lsp.diagnostics.wait_ms', 'num', { min: 0, max: 30000, unit: '毫秒', def: 2500 }),
    field('lsp.diagnostics.min_severity', 'enum', {
      def: 'warning',
      options: [
        { value: 'error', labelKey: 'settings.enum.error' },
        { value: 'warning', labelKey: 'settings.enum.warning' },
        { value: 'information', labelKey: 'settings.enum.information' },
        { value: 'hint', labelKey: 'settings.enum.hint' },
      ],
    }),
    field('lsp.diagnostics.max_per_file', 'num', { min: 1, max: 200, unit: '条', def: 20 }),
    field('lsp.diagnostics.max_files', 'num', { min: 1, max: 50, def: 10 }),
    field('lsp.diagnostics.late_delivery', 'bool', { def: true }),
  ],
}

/** Every group of one tab, plus the tab's own toolbar switch when it has one. */
export function groupsForTab(tab: string): SettingsGroup[] {
  return SETTINGS_FORMS[tab] ?? []
}

/** Every field of every group, in the order the page draws them. */
export const ALL_SETTINGS_FIELDS: SettingsField[] = [
  ...Object.values(SETTINGS_FORMS).flatMap((groups) => groups.flatMap((group) => group.fields)),
  ...Object.values(TOOLBAR_SWITCHES),
  ...LSP_PARAMETERS.fields,
]

/** A field's name, from the locale catalogs. */
export function fieldLabelKey(path: string): string {
  return `settings.field.${path}`
}

/** A field's explanation, from the locale catalogs. */
export function fieldHintKey(path: string): string {
  return `settings.hint.${path}`
}

/**
 * Whether a number is out of its range: the prototype's `badNum`, which is what
 * draws the box red as soon as the value leaves the range (a clamping field's
 * out-of-range value is stored at the edge instead, so it never shows red).
 */
export function numberOutOfRange(field: SettingsField, value: unknown): boolean {
  if (field.kind !== 'num' || value === undefined || value === null || value === '' || field.clamp) return false
  const n = Number(value)
  if (Number.isNaN(n)) return true
  if (field.ok) return !field.ok(n)
  if (field.min !== undefined && n < field.min) return true
  return field.max !== undefined && n > field.max
}

/** The range text a number box shows beside it. */
export function rangeText(field: SettingsField): string {
  if (field.range) return field.range
  return field.max !== undefined ? `${field.min}–${field.max}` : ''
}

/**
 * The value a row is in effect: what is stored, or the field's own default when
 * nothing is. An empty list stores nothing, so it takes the default too — the
 * prototype's `eff`.
 */
export function effectiveValue(field: SettingsField, stored: unknown): unknown {
  const missing =
    stored === undefined ||
    stored === null ||
    stored === '' ||
    (Array.isArray(stored) && stored.length === 0)
  return missing ? field.def : stored
}
