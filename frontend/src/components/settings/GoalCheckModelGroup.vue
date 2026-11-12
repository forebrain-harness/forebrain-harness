<template>
  <!-- 设置 · 常规/子代理 · 「/goal 检查模型」(plan 299 §2). Hand-written rather
       than schema-driven on purpose: the page's 「子代理」tab is otherwise built
       from SETTINGS_FORMS, and this block is exactly the shape that form cannot
       express — a two-line model signature, a caret that becomes a clear x, an
       effort that belongs to the chosen row and is disabled without one. Keeping
       it out of the schema is also what keeps settingsSchema.test.ts comparing
       the prototype's own FORMS, untouched.

       It reads and writes the same form the tab does: `agents.goal_evaluator.model`
       holds a configured row's id, `agents.goal_evaluator.reasoning_effort` holds
       "" while the level equals that row's own default (a live reading of the
       model page) and the level only when it overrides it. -->
  <section class="gc" data-group="goal-check-model">
    <header class="gc__head">
      <h3>{{ t('subagents.goalCheck.title') }}</h3>
    </header>

    <div class="gc__rows">
      <div class="gc__row" :class="{ 'gc__row--dirty': dirty }">
        <div class="gc__lab">
          <b>{{ t('subagents.goalCheck.modelLabel') }}</b>
        </div>
        <div class="gc__ctl">
          <GoalCheckModelSelect
            :model-value="modelId"
            :choices="choices"
            @update:model-value="onModel"
          />
        </div>
      </div>

      <div class="gc__row" :class="{ 'gc__row--dirty': dirty }">
        <div class="gc__lab">
          <b>{{ t('subagents.goalCheck.effortLabel') }}</b>
          <span v-if="hint" class="gc__hint" data-goal-check-hint>{{ hint }}</span>
          <span
            v-if="showReset"
            class="gc__link"
            role="button"
            tabindex="0"
            data-goal-check-reset
            @click="resetEffort"
            @keydown.enter.prevent="resetEffort"
            @keydown.space.prevent="resetEffort"
          >{{ t('subagents.goalCheck.effortReset') }}</span>
        </div>
        <div class="gc__ctl">
          <select
            class="gc__select"
            :class="{ 'gc__select--unset': !selected }"
            data-goal-check-effort
            :disabled="!selected"
            :value="effectiveEffort"
            @change="onEffortChange"
          >
            <option v-for="option in effortOptions" :key="option.value" :value="option.value">
              {{ option.label }}
            </option>
          </select>
        </div>
      </div>
    </div>

    <div class="gc__tray" :class="{ 'gc__tray--open': trayOpen }">
      <div class="gc__tray-in">
        <div class="gc__tray-bar">
          <span v-if="savedNotice" class="gc__ok" data-goal-check-saved>✓ {{ t('subagents.goalCheck.saved') }}</span>
          <span v-if="refusal?.message" class="gc__err" role="alert">{{ refusal.message }}</span>
          <button type="button" class="gc__btn" data-goal-check-discard @click="discard">
            {{ t('subagents.goalCheck.discard') }}
          </button>
          <button
            type="button"
            class="gc__btn gc__btn--pri"
            data-goal-check-save
            :disabled="saving"
            @click="save"
          >
            {{ t('subagents.goalCheck.save') }}
          </button>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
/**
 * The /goal check's settings block (plan 299): one global row for every primary
 * agent, because the check is dispatched by the system rather than by a primary
 * agent. Only two things are configurable — the model and the thinking level —
 * and both live in the page's own form, one group save each.
 */
import { computed, onBeforeUnmount, ref } from 'vue'

import GoalCheckModelSelect from '@/components/settings/GoalCheckModelSelect.vue'
import type { SettingsGroup } from '@/lib/settingsSchema'
import { effortLevelLabel } from '@/lib/effortLabel'
import { useModelChoices } from '@/composables/useModelChoices'
import { useI18n } from '@/locales'

import type { SettingsRefusal, useSettingsForm } from '@/components/settings/form/useSettingsForm'

const props = defineProps<{ form: ReturnType<typeof useSettingsForm> }>()

const MODEL_PATH = 'agents.goal_evaluator.model'
const EFFORT_PATH = 'agents.goal_evaluator.reasoning_effort'

const { t } = useI18n()
const { choices, ensureChoices } = useModelChoices()
ensureChoices()

/** The group the page's own save/dirty machinery reads: two fields, one save. */
const group: SettingsGroup = {
  id: 'goal-check-model',
  titleKey: 'subagents.goalCheck.title',
  fields: [
    { path: MODEL_PATH, kind: 'model' },
    { path: EFFORT_PATH, kind: 'text' },
  ],
}

const modelId = computed(() => String(props.form.draft.value[MODEL_PATH] ?? ''))
/** The stored level: "" means "whatever the row's own default is". */
const draftEffort = computed(() => String(props.form.draft.value[EFFORT_PATH] ?? ''))
const selected = computed(() => choices.value.find((choice) => choice.id === modelId.value) ?? null)
const rowDefault = computed(() => selected.value?.defaultEffort ?? '')
/** What the select shows: the override, or the level the chosen row runs on. */
const effectiveEffort = computed(() => draftEffort.value || rowDefault.value)

/** The same 双写 the model page uses: 高 high, 极致 max, a source's own name as is. */
const effortText = (level: string): string => `${effortLevelLabel(level)} ${level}`.trim()

const effortOptions = computed(() => {
  if (!selected.value) return [{ value: '', label: t('subagents.followPrimary') }]
  const options = [{ value: '', label: t('subagents.goalCheck.effortFollowRow') }]
  for (const level of selected.value.efforts ?? []) {
    options.push({ value: level, label: effortText(level) })
  }
  // A stored level the row no longer offers still has to be readable — and
  // selectable, so the reader can see what is actually configured.
  const current = effectiveEffort.value
  if (current && !(selected.value.efforts ?? []).includes(current)) {
    options.push({ value: current, label: effortText(current) })
  }
  return options
})

const hint = computed(() => {
  const row = selected.value
  if (!row) return ''
  if (effectiveEffort.value === rowDefault.value) {
    return rowDefault.value
      ? t('subagents.goalCheck.hintRowDefault', { effort: effortText(rowDefault.value) })
      : t('subagents.goalCheck.hintRowNone')
  }
  return t('subagents.goalCheck.hintOverridden', { effort: effortText(effectiveEffort.value) })
})

/** An override is on when a level is stored: equal-to-default is stored as "". */
const showReset = computed(() => Boolean(selected.value) && draftEffort.value !== '')

const dirty = computed(() => props.form.groupDirty(group))
const saving = ref(false)
const refusal = ref<SettingsRefusal | null>(null)
const savedNotice = ref(false)
const trayOpen = computed(() => dirty.value || savedNotice.value || Boolean(refusal.value?.message))
let noticeTimer: ReturnType<typeof setTimeout> | undefined

/** Choosing a row (or clearing the choice) always goes back to following the row. */
function onModel(next: string): void {
  props.form.setValue(MODEL_PATH, next)
  props.form.setValue(EFFORT_PATH, '')
  refusal.value = null
}

/** The level the reader picked; equal to the row's own default is stored as "". */
function onEffortChange(event: Event): void {
  const next = (event.target as HTMLSelectElement).value
  props.form.setValue(EFFORT_PATH, next === rowDefault.value ? '' : next)
  refusal.value = null
}

/** 「恢复为模型默认」: back to the live reading, not a snapshot of it. */
function resetEffort(): void {
  props.form.setValue(EFFORT_PATH, '')
  refusal.value = null
}

async function save(): Promise<void> {
  saving.value = true
  refusal.value = null
  const result = await props.form.saveGroup(group)
  saving.value = false
  if (result) {
    refusal.value = result
    return
  }
  savedNotice.value = true
  if (noticeTimer) clearTimeout(noticeTimer)
  noticeTimer = setTimeout(() => {
    savedNotice.value = false
  }, 2500)
}

function discard(): void {
  props.form.cancelGroup(group)
  refusal.value = null
}

onBeforeUnmount(() => {
  if (noticeTimer) clearTimeout(noticeTimer)
})
</script>

<style scoped>
.gc {
  margin-top: 36px;
}
.gc__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.gc__head h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--forebrain-text);
  white-space: nowrap;
}
.gc__row {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 360px;
  column-gap: 24px;
  align-items: center;
  padding: 12px 0 12px 16px;
}
.gc__row + .gc__row {
  border-top: 1px solid var(--forebrain-divider);
}
/* The unsaved marker: a line down the row's left edge while the draft differs. */
.gc__row--dirty::before {
  content: '';
  position: absolute;
  left: 0;
  top: 8px;
  bottom: 8px;
  width: 2px;
  border-radius: 2px;
  background: var(--forebrain-brand-1);
}
.gc__lab {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.gc__lab > b {
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
}
.gc__hint {
  font-size: 12px;
  line-height: 1.5;
  color: var(--forebrain-muted-text);
}
.gc__link {
  align-self: flex-start;
  font-size: 12px;
  color: var(--forebrain-brand-1);
  cursor: pointer;
}
.gc__ctl {
  min-width: 0;
}
.gc__select {
  width: 100%;
  height: 32px;
  padding: 0 8px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-input-bg);
  font-size: 13px;
  color: var(--forebrain-text);
}
/* 未选模型时置灰并禁用：一个下拉，两个 placeholder 同一种灰。 */
.gc__select--unset,
.gc__select:disabled {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-muted-text);
  cursor: not-allowed;
}
/* The save bar, collapsed to nothing until a row differs or a save landed. */
.gc__tray {
  display: grid;
  grid-template-rows: 0fr;
  transition: grid-template-rows 0.24s ease;
}
.gc__tray--open {
  grid-template-rows: 1fr;
}
.gc__tray-in {
  min-height: 0;
  overflow: hidden;
}
.gc__tray-bar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 0 2px 16px;
  border-top: 1px solid var(--forebrain-divider);
  margin-top: 2px;
}
.gc__ok {
  margin-right: auto;
  font-size: 12px;
  color: var(--forebrain-brand-1);
}
.gc__err {
  margin-right: auto;
  font-size: 12px;
  color: var(--forebrain-danger);
}
.gc__btn {
  border-radius: 8px;
  padding: 5px 14px;
  font-size: 13px;
  color: var(--forebrain-text-2);
  cursor: pointer;
}
.gc__btn--pri {
  background: var(--forebrain-brand-1);
  color: #fff;
}
.gc__btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
