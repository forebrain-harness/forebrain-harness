/**
 * The settings page's form state (plan 191 §4.1, §0.3, §0.4).
 *
 * One group is one save. The page reads `GET /api/settings` once, holds the
 * stored values (`saved`) and the form's own (`draft`), and a group's save is
 * one `PATCH /api/settings` carrying the paths that differ — all or nothing, on
 * the wire and in the store.
 *
 * Nothing here invents a value: every row shows what is stored, the gateway
 * having written the defaults into the database when it created it (§0.4), and a
 * credential is known only as "set + its last four characters" (§0.20).
 */
import { computed, ref } from 'vue'
import forebrainApi, { getErrorMessage, type SettingsSecretFact } from '@/lib/api'
import type { SettingsField, SettingsGroup } from '@/lib/settingsSchema'

export interface SettingsRefusal {
  path: string
  message: string
}

/**
 * One refusal read off a failed save: the gateway answers `{path, message}` for
 * a value it will not take, and the row under that path shows the message.
 */
export function settingsRefusalFrom(error: unknown): SettingsRefusal {
  const data = (error as { response?: { data?: unknown } })?.response?.data
  if (data && typeof data === 'object') {
    const record = data as Record<string, unknown>
    const path = typeof record.path === 'string' ? record.path : ''
    const message = typeof record.message === 'string' ? record.message : ''
    if (message) return { path, message }
  }
  return { path: '', message: getErrorMessage(error) }
}

export function useSettingsForm() {
  const saved = ref<Record<string, unknown>>({})
  const draft = ref<Record<string, unknown>>({})
  const secrets = ref<Record<string, SettingsSecretFact>>({})
  /** The credentials the user typed, per path: only what was typed travels. */
  const typedSecrets = ref<Record<string, string>>({})
  const builtinHandoffPrompt = ref('')
  const approvalPresetsVisible = ref(true)
  /** Whether Claude Code is installed: the interop switch's group shows only then. */
  const claudeCodeAvailable = ref(false)
  const loading = ref(false)
  const loadError = ref('')

  async function load(): Promise<void> {
    loading.value = true
    loadError.value = ''
    try {
      const snapshot = await forebrainApi.settings()
      saved.value = { ...snapshot.values }
      // The form starts at the stored values: nothing is "unsaved" until the
      // user changes something.
      draft.value = structuredClone(snapshot.values)
      secrets.value = { ...snapshot.secrets }
      typedSecrets.value = {}
      builtinHandoffPrompt.value = snapshot.meta?.builtinHandoffPrompt ?? ''
      approvalPresetsVisible.value = snapshot.meta?.approvalPresetsVisible !== false
      claudeCodeAvailable.value = snapshot.meta?.claudeCodeAvailable === true
      // The meta-driven visibility conditions read the draft — the same
      // shape the prototype's `when(draft)` uses — so the fact the gateway
      // answers rides along with the values. It is not a field: no save
      // ever carries it (plan 195 §3.7).
      draft.value = { ...draft.value, claude_code_available: claudeCodeAvailable.value }
    } catch (error) {
      loadError.value = getErrorMessage(error)
    } finally {
      loading.value = false
    }
  }

  /**
   * The value a row shows: the form's, with the schema's default standing in
   * for an unset one. Comparing effective values is what makes flipping a
   * switch back to its default count as "no change" (§8.4-5).
   */
  function draftValue(field: SettingsField): unknown {
    return draft.value[field.path] ?? field.def
  }

  /** Whether this one row differs from what is stored. */
  function fieldDirty(field: SettingsField): boolean {
    if (Object.prototype.hasOwnProperty.call(typedSecrets.value, field.path)) {
      return typedSecrets.value[field.path] !== ''
    }
    return !sameValue(draft.value[field.path], saved.value[field.path])
  }

  /** Whether any row of the group differs — the save bar's trigger. */
  function groupDirty(group: SettingsGroup): boolean {
    return group.fields.some((field) => fieldDirty(field))
  }

  function setValue(path: string, value: unknown): void {
    draft.value = { ...draft.value, [path]: value }
  }

  /** Records a value the gateway took outside a group save (a toolbar switch). */
  function markSaved(path: string, value: unknown): void {
    saved.value = { ...saved.value, [path]: value }
  }

  /** What is stored for one path — the value a failed save falls back to. */
  function savedValueOf(path: string): unknown {
    return saved.value[path]
  }

  function setTypedSecret(path: string, value: string): void {
    typedSecrets.value = { ...typedSecrets.value, [path]: value }
  }

  /** Cancels the group's edits: every row goes back to the stored value. */
  function cancelGroup(group: SettingsGroup): void {
    const next = { ...draft.value }
    const typed = { ...typedSecrets.value }
    for (const field of group.fields) {
      next[field.path] = saved.value[field.path]
      delete typed[field.path]
    }
    draft.value = next
    typedSecrets.value = typed
  }

  /**
   * Saves one group. The values are the rows whose value changed, the secrets
   * are the credentials the user typed, and the answer is the refusal the
   * gateway gave, if it gave one.
   */
  async function saveGroup(group: SettingsGroup): Promise<SettingsRefusal | null> {
    const values: Record<string, unknown> = {}
    const typed: Record<string, string> = {}
    for (const field of group.fields) {
      if (field.secret) {
        const value = typedSecrets.value[field.path]
        if (value !== undefined && value !== '') typed[field.path] = value
        continue
      }
      if (!sameValue(draft.value[field.path], saved.value[field.path])) {
        values[field.path] = draft.value[field.path] ?? field.def ?? null
      }
    }
    if (Object.keys(values).length === 0 && Object.keys(typed).length === 0) return null
    try {
      await forebrainApi.saveSettings(values, typed)
      saved.value = { ...saved.value, ...values }
      for (const path of Object.keys(typed)) {
        // The answer never carries the value; what the page now knows is that
        // one is set and its tail, exactly as a reload would say.
        const text = typed[path]
        secrets.value = {
          ...secrets.value,
          [path]: { set: true, last4: text.length >= 4 ? text.slice(-4) : '' },
        }
      }
      const cleared = { ...typedSecrets.value }
      for (const path of Object.keys(typed)) delete cleared[path]
      typedSecrets.value = cleared
      return null
    } catch (error) {
      return settingsRefusalFrom(error)
    }
  }

  const groupsDirty = computed(() => (groups: SettingsGroup[]) => groups.some(groupDirty))

  return {
    saved,
    draft,
    secrets,
    typedSecrets,
    builtinHandoffPrompt,
    approvalPresetsVisible,
    claudeCodeAvailable,
    loading,
    loadError,
    load,
    draftValue,
    fieldDirty,
    groupDirty,
    groupsDirty,
    setValue,
    setTypedSecret,
    markSaved,
    savedValueOf,
    cancelGroup,
    saveGroup,
  }
}

/** Two configuration values are the same: order matters for a list, and an
 * unset value equals an empty one (both mean "nothing stored"). */
export function sameValue(a: unknown, b: unknown): boolean {
  const left = a === undefined ? null : a
  const right = b === undefined ? null : b
  return JSON.stringify(left) === JSON.stringify(right)
}
