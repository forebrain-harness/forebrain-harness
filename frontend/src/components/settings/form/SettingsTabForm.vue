<template>
  <!-- One settings tab built from the schema (plan 191 §4.1–§4.3): the groups
       the prototype draws for this tab, each with its own save bar, plus the
       toolbar switch a tab may carry — flipped and saved at once, the way the
       language server and skill offer switches are (§0.7). -->
  <div class="grid gap-4" :data-tab="tab">
    <p v-if="form.loadError.value" class="text-[12px] text-[var(--forebrain-danger)]">{{ form.loadError.value }}</p>

    <ListToolbar v-if="toolbarField">
      <template #left>
        <span class="text-[12px] text-[var(--forebrain-muted-text)]" />
      </template>
      <template #actions>
        <label class="flex items-center gap-2 text-[13px] text-[var(--forebrain-text)]">
          {{ t(fieldLabel(toolbarField.path)) }}
          <SwitchComponent
            :model-value="toolbarOn"
            :aria-label="t(fieldLabel(toolbarField.path))"
            :data-toolbar-switch="toolbarField.path"
            @update:model-value="saveToolbarSwitch"
          />
        </label>
      </template>
    </ListToolbar>

    <SettingsGroup
      v-for="group in groups"
      :key="group.id"
      :group="group"
      :lone-tab="visibleGroups.length === 1"
      :value-for="form.draftValue"
      :draft="form.draft.value"
      :dirty="form.groupDirty(group)"
      :secrets="form.secrets.value"
      :disabled="form.loading.value"
      :saving="saving === group.id"
      :refusal="refusals[group.id] ?? null"
      :saved-notice="savedGroup === group.id"
      :restore-disabled="restoreDisabled"
      :visible="groupVisible(group)"
      @change="form.setValue"
      @secret="form.setTypedSecret"
      @save="save(group)"
      @cancel="form.cancelGroup(group)"
      @restore="restore"
    />

    <!-- A tab may add its own list under the groups (the language servers, the
         memory reset button). -->
    <slot />
  </div>
</template>

<script setup lang="ts">
/**
 * A schema-driven settings tab. Every tab but 外观, MCP, 钩子, 配置文件 and
 * 运行状态 is one of these: the prototype's groups for that tab, each saved on
 * its own, plus the toolbar switch a couple of them carry.
 *
 * The component owns only the per-save ephemera — which group is saving, the
 * refusal it got, the "已保存" line that lingers 2.6 seconds — because those are
 * per group and the form itself belongs to the page.
 */
import { computed, onBeforeUnmount, ref } from 'vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import SettingsGroup from '@/components/settings/form/SettingsGroup.vue'
import {
  fieldLabelKey,
  groupsForTab,
  TOOLBAR_SWITCHES,
  type SettingsField,
  type SettingsGroup as SettingsGroupShape,
} from '@/lib/settingsSchema'
import forebrainApi, { getErrorMessage } from '@/lib/api'
import { useI18n, type I18nKey } from '@/locales'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'

const props = defineProps<{
  tab: string
  /** The page's form: one draft, one save per group. */
  form: ReturnType<typeof useSettingsForm>
}>()

const { t: translate } = useI18n()
const t = (key: string) => translate(key as I18nKey)

function fieldLabel(path: string): string {
  return fieldLabelKey(path)
}

const groups = computed(() => groupsForTab(props.tab))

/** The tab's toolbar switch, when the prototype puts one here (§0.7). */
const toolbarField = computed<SettingsField | null>(() => TOOLBAR_SWITCHES[props.tab] ?? null)
const toolbarOn = computed(() => props.form.draftValue(toolbarField.value as SettingsField) === true)

const saving = ref('')
const savedGroup = ref('')
const refusals = ref<Record<string, { path: string; message: string } | null>>({})
let savedTimer: ReturnType<typeof setTimeout> | undefined

/** A group draws only while its own condition holds (the memory folding). */
function groupVisible(group: SettingsGroupShape): boolean {
  return !group.when || group.when(props.form.draft.value)
}

/**
 * The groups the tab actually draws. A hidden one does not count towards
 * "this tab is a single section", so 子代理 left alone once the peers switch
 * disappears (no Claude Code, plan 195 §3.7) keeps its title-less shape (§0.5).
 */
const visibleGroups = computed(() => groups.value.filter(groupVisible))

/** Whether 恢复默认 is greyed out: the text already is the built-in prompt. */
function restoreDisabled(field: SettingsField): boolean {
  if (!field.restore) return false
  const value = String(props.form.draftValue(field) ?? '').trim()
  return value === props.form.builtinHandoffPrompt.value.trim()
}

async function save(group: SettingsGroupShape): Promise<void> {
  saving.value = group.id
  refusals.value = { ...refusals.value, [group.id]: null }
  const refusal = await props.form.saveGroup(group)
  saving.value = ''
  refusals.value = { ...refusals.value, [group.id]: refusal }
  if (refusal) return
  savedGroup.value = group.id
  if (savedTimer) clearTimeout(savedTimer)
  // The confirmation lives 2.6 seconds, the prototype's own timing.
  savedTimer = setTimeout(() => {
    savedGroup.value = ''
  }, 2600)
}

/** 恢复默认 puts the built-in prompt back on screen; saving is the user's. */
function restore(field: SettingsField): void {
  props.form.setValue(field.path, props.form.builtinHandoffPrompt.value)
}

/** The toolbar switch saves the moment it is flipped. */
async function saveToolbarSwitch(next: boolean): Promise<void> {
  const field = toolbarField.value
  if (!field) return
  props.form.setValue(field.path, next)
  try {
    await forebrainApi.saveSettings({ [field.path]: next })
    props.form.markSaved(field.path, next)
  } catch (error) {
    // A switch that did not save goes back to what is stored: the page must
    // never show a posture the gateway did not take.
    props.form.setValue(field.path, props.form.savedValueOf(field.path))
    refusals.value = { ...refusals.value, [props.tab]: { path: '', message: getErrorMessage(error) } }
  }
}

onBeforeUnmount(() => {
  if (savedTimer) clearTimeout(savedTimer)
})
</script>
