import { beforeAll, describe, expect, it } from 'vitest'

// The prototype is the acceptance standard (plan 191 §1, §4.1, §5): the page
// is built 1:1 from it, and this test is what keeps the two the same. It reads
// the prototype's own source — the one file the owner confirmed — and compares
// every field it draws with the schema the app draws from: same paths, in the
// same order, with the same wording, kinds, units, ranges, defaults, options
// and visibility rules. A deviation anywhere is a failing test, not a review
// note.
import prototypeSource from '../../../docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html?raw'

import {
  ALL_SETTINGS_FIELDS,
  APPROVAL_PRESETS,
  LSP_PARAMETERS,
  SETTINGS_FORMS,
  TOOLBAR_SWITCHES,
  fieldHintKey,
  fieldLabelKey,
  type SettingsField,
  type SettingsGroup,
} from '@/lib/settingsSchema'
import { setLocale, tIn, type I18nKey } from '@/locales'

/** The prototype's field/group constants, evaluated out of its own source.
 * `@preset` is the settings path `approval_preset` (plan 191 §3.1), and
 * `project.batch_sandbox` belongs to the project space, not this schema. */
interface PrototypeField {
  p: string
  label: string
  kind: string
  def?: unknown
  min?: number
  max?: number
  clamp?: boolean
  unit?: string
  range?: string
  hint?: string
  ph?: string
  add?: string
  rows?: number
  mono?: boolean
  warn?: boolean
  stack?: boolean
  restore?: boolean
  opts?: [string, string][]
  cols?: [string, string]
  when?: (draft: Record<string, unknown>) => boolean
  ok?: (value: number) => boolean
}

interface PrototypeGroup {
  id: string
  title: string
  fields: PrototypeField[]
  note?: string
  keepLabel?: boolean
  titleLabel?: boolean
  when?: (draft: Record<string, unknown>) => boolean
}

function prototypeConstants(): {
  FORMS: Record<string, PrototypeGroup[]>
  TBSW: Record<string, PrototypeField>
  LSPM: PrototypeGroup
  PRESETS: [string, string, string][]
} {
  const start = prototypeSource.indexOf('const F = (p, label')
  const end = prototypeSource.indexOf('const GROUPS = {};')
  const block = prototypeSource.slice(start, end)
  return new Function(`${block}\nreturn { FORMS, TBSW, LSPM, PRESETS }`)() as ReturnType<typeof prototypeConstants>
}

/** The prototype draws `@preset` as the path this schema (and the gateway's
 * registry) calls `approval_preset`. */
function schemaPathOf(prototypeField: PrototypeField): string {
  if (prototypeField.p === '@preset') return 'approval_preset'
  return prototypeField.p
}

function zh(key: string): string {
  return tIn('zh', key as I18nKey)
}

const { FORMS, TBSW, LSPM, PRESETS } = prototypeConstants()

beforeAll(() => {
  setLocale('zh')
})

/** Everything a field is, side by side, so a mismatch can name its path. */
function describeField(field: SettingsField): Record<string, unknown> {
  return {
    path: field.path,
    kind: field.kind,
    label: zh(fieldLabelKey(field.path)),
    def: field.def,
    min: field.min,
    max: field.max,
    clamp: field.clamp,
    unit: field.unit,
    range: field.range,
    mono: field.mono,
    warn: field.warn,
    stack: field.stack,
    restore: field.restore,
    rows: field.rows,
    placeholder: field.placeholder,
    add: field.addLabelKey ? zh(field.addLabelKey) : undefined,
    columns: field.columns,
    placeholders: field.placeholders,
    options: field.options?.map((option) => [option.value, zh(option.labelKey)]),
    secret: field.secret === true,
    hasWhen: field.when !== undefined,
  }
}

function describePrototype(field: PrototypeField): Record<string, unknown> {
  return {
    path: schemaPathOf(field),
    kind: field.kind,
    label: field.label,
    def: field.def,
    min: field.min,
    max: field.max,
    clamp: field.clamp,
    unit: field.unit,
    range: field.range,
    mono: field.mono,
    warn: field.warn,
    stack: field.stack,
    restore: field.restore,
    rows: field.rows,
    placeholder: typeof field.ph === 'string' ? field.ph : undefined,
    add: field.add,
    columns: field.cols,
    placeholders: Array.isArray(field.ph) ? field.ph : undefined,
    options: field.opts,
    secret: field.kind === 'secret',
    hasWhen: field.when !== undefined,
  }
}

function probeDraft(driver: string, value: unknown): Record<string, unknown> {
  return { [driver]: value }
}

describe('settingsSchema against the prototype', () => {
  it('reads the prototype at all', () => {
    expect(prototypeSource).toContain('const FORMS = {')
    expect(Object.keys(FORMS).length).toBeGreaterThan(0)
    // Every literal `F('<path>'` call in the prototype — 34 of them since plan
    // 195 added the peers switch — plus the ones spelled with the WS/WF/CI/GR/TE
    // shorthands: every one of them is a row the page must draw.
    const literal = (prototypeSource.match(/F\('/g) ?? []).length
    expect(literal).toBe(34)
    expect(ALL_SETTINGS_FIELDS.length).toBe(
      Object.values(FORMS)
        .flatMap((groups) => groups.flatMap((group) => group.fields))
        .filter((field) => field.p !== 'project.batch_sandbox').length +
        Object.keys(TBSW).length +
        LSPM.fields.length,
    )
  })

  it('covers every tab the prototype draws, group for group in order', () => {
    // The project-space batch switch is the one prototype form this schema
    // deliberately leaves out: it belongs to the project page (§3.1).
    expect(Object.keys(FORMS)).toEqual([...Object.keys(SETTINGS_FORMS).slice(0, 1), 'projbatch', ...Object.keys(SETTINGS_FORMS).slice(1)])
    expect(FORMS.projbatch.flatMap((group) => group.fields.map((field) => field.p))).toEqual(['project.batch_sandbox'])
  })

  it('draws the same groups, with the same titles and notes', () => {
    for (const [tab, groups] of Object.entries(SETTINGS_FORMS)) {
      const prototypeGroups = FORMS[tab]
      expect(groups.map((group) => group.id), tab).toEqual(prototypeGroups.map((group) => group.id))
      groups.forEach((group: SettingsGroup, index: number) => {
        const prototypeGroup = prototypeGroups[index]
        expect(zh(group.titleKey), `${tab}/${group.id} title`).toBe(prototypeGroup.title)
        expect(group.noteKey ? zh(group.noteKey) : undefined, `${tab}/${group.id} note`).toBe(prototypeGroup.note)
        expect(group.keepLabel ?? false, `${tab}/${group.id} keepLabel`).toBe(prototypeGroup.keepLabel ?? false)
        expect(group.titleLabel ?? false, `${tab}/${group.id} titleLabel`).toBe(prototypeGroup.titleLabel ?? false)
      })
    }
    expect(zh(LSP_PARAMETERS.titleKey)).toBe(LSPM.title)
  })

  it('draws every field the prototype draws, word for word', () => {
    for (const [tab, groups] of Object.entries(SETTINGS_FORMS)) {
      const fields = groups.flatMap((group) => group.fields)
      const prototypeFields = FORMS[tab].flatMap((group) => group.fields)
      // The handoff prompt's built-in text is the one default the schema does
      // not carry: it arrives from the gateway (meta.builtin_handoff_prompt),
      // which is what the field's 恢复默认 restores (§0.13). The prototype
      // spells that same text inline as `def`, so the comparison treats the
      // prompt's stored value as the backend's to name.
      const schemaSide = fields.map((field) =>
        field.path === 'compact.prompt' ? { ...describeField(field), def: 'builtin' } : describeField(field),
      )
      const prototypeSide = prototypeFields.map((field) =>
        field.p === 'compact.prompt' ? { ...describePrototype(field), def: 'builtin' } : describePrototype(field),
      )
      expect(schemaSide, tab).toEqual(prototypeSide)
    }
    expect(LSP_PARAMETERS.fields.map(describeField)).toEqual(LSPM.fields.map(describePrototype))
    for (const field of Object.values(TOOLBAR_SWITCHES)) {
      expect(describeField(field), field.path).toEqual(describePrototype(TBSW[field.path]))
    }
  })

  it('hints say what the prototype says', () => {
    for (const field of ALL_SETTINGS_FIELDS) {
      const prototype = [...Object.values(FORMS).flat(), LSPM, ...Object.values(TBSW)].flatMap((source) =>
        'fields' in source ? source.fields : [source],
      ).find((candidate) => schemaPathOf(candidate) === field.path)
      expect(prototype, field.path).toBeDefined()
      if (prototype?.hint) expect(zh(fieldHintKey(field.path)), field.path).toBe(prototype.hint)
    }
  })

  it('hides a row on the same condition the prototype hides it', () => {
    // The one field whose condition lives outside its own group is none: every
    // conditional row sits next to the enum that drives it, so both `when`
    // functions are probed with each option that enum offers.
    for (const [tab, groups] of Object.entries(SETTINGS_FORMS)) {
      for (const group of groups) {
        const driver = group.fields.find((field) => field.kind === 'enum')
        const conditional = group.fields.filter((field) => field.when)
        if (!conditional.length) continue
        expect(driver, `${tab}/${group.id}`).toBeDefined()
        const prototypeGroup = FORMS[tab].find((candidate) => candidate.id === group.id)
        for (const value of ['', ...(driver?.options?.map((option) => option.value) ?? [])]) {
          const draft = probeDraft(driver?.path ?? '', value)
          for (const field of conditional) {
            const prototypeField = prototypeGroup?.fields.find((candidate) => schemaPathOf(candidate) === field.path)
            expect(field.when?.(draft), `${field.path} @ ${driver?.path}=${value}`).toBe(prototypeField?.when?.(draft))
          }
        }
      }
    }
  })

  it('folds the sandbox group with the preset and the memory groups with the switch', () => {
    const sandbox = SETTINGS_FORMS.approval.find((group) => group.id === 'sww')
    expect(sandbox?.when?.(probeDraft('approval_preset', 'workspace_write'))).toBe(true)
    expect(sandbox?.when?.(probeDraft('approval_preset', 'full_access'))).toBe(false)
    expect(FORMS.approval.find((group) => group.id === 'sww')?.when?.(probeDraft('@preset', 'workspace_write'))).toBe(true)

    const memoryGroups = SETTINGS_FORMS.memory.filter((group) => group.when)
    expect(memoryGroups.map((group) => group.id)).toEqual(['mr', 'mq', 'mo'])
    for (const group of memoryGroups) {
      expect(group.when?.(probeDraft('features.memories', true)), group.id).toBe(true)
      expect(group.when?.(probeDraft('features.memories', false)), group.id).toBe(false)
    }
  })

  it('offers the two approval presets with the prototype\'s words', () => {
    expect(APPROVAL_PRESETS.map((preset) => [preset.value, zh(preset.labelKey), zh(preset.descriptionKey)])).toEqual(PRESETS)
  })

  it('takes the idle-timeout values the prototype takes', () => {
    const idle = LSP_PARAMETERS.fields.find((field) => field.path === 'lsp.idle_timeout')
    const prototypeIdle = LSPM.fields.find((field) => field.p === 'lsp.idle_timeout')
    expect(idle?.ok).toBeDefined()
    for (const value of [0, 59, 60, 600, 86400, 86401]) {
      expect(idle?.ok?.(value), String(value)).toBe(prototypeIdle?.ok?.(value))
    }
    expect(idle?.range).toBe(prototypeIdle?.range)
  })
})
