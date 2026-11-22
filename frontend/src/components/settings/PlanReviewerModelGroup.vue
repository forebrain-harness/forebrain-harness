<template>
  <!-- 设置 · 子代理 · 「/plan 复核模型」(plan 313). The goal evaluator's block one row
       down (plan 299), and hand-written for the same reason: a model signature,
       a caret that becomes a clear x, and a thinking level that belongs to the
       chosen row and is disabled without one are exactly what SETTINGS_FORMS
       cannot express. Keeping it out of the schema is also what keeps
       settingsSchema.test.ts comparing the prototype's own FORMS.

       It reads and writes the same form the tab does:
       `agents.plan_reviewer.model` holds a configured row's id and
       `agents.plan_reviewer.reasoning_effort` holds "" while the level equals
       that row's own default and the level only when it overrides it. The row
       supplies the exit-plan approval picker's default; the reviewer chosen on
       the approval card still wins. -->
  <div :class="bare ? 'pr pr--bare' : 'pr'" data-group="plan-reviewer-model">
    <header v-if="!bare" class="pr__head">
      <h3>{{ t('subagents.planReviewer.title') }}</h3>
    </header>

    <div class="pr__rows">
      <div class="pr__row" :class="{ 'pr__row--dirty': dirty }">
        <div class="pr__lab">
          <b>{{ t('subagents.planReviewer.modelLabel') }}</b>
        </div>
        <div class="pr__ctl">
          <GoalEvaluatorModelSelect
            :model-value="modelId"
            :choices="choices"
            @update:model-value="onModel"
          />
        </div>
      </div>

      <div class="pr__row" :class="{ 'pr__row--dirty': dirty }">
        <div class="pr__lab">
          <b>{{ t('subagents.planReviewer.effortLabel') }}</b>
          <span v-if="hint" class="pr__hint" data-plan-reviewer-hint>{{ hint }}</span>
          <span
            v-if="showReset"
            class="pr__link"
            role="button"
            tabindex="0"
            data-plan-reviewer-reset
            @click="resetEffort"
            @keydown.enter.prevent="resetEffort"
            @keydown.space.prevent="resetEffort"
          >{{ t('subagents.planReviewer.effortReset') }}</span>
        </div>
        <div class="pr__ctl">
          <select
            class="pr__select"
            :class="{ 'pr__select--unset': !selected }"
            data-plan-reviewer-effort
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

    <div class="pr__tray" :class="{ 'pr__tray--open': trayOpen }">
      <div class="pr__tray-in">
        <div class="pr__tray-bar">
          <span v-if="savedNotice" class="pr__ok" data-plan-reviewer-saved>✓ {{ t('subagents.planReviewer.saved') }}</span>
          <span v-if="refusal?.message" class="pr__err" role="alert">{{ refusal.message }}</span>
          <button type="button" class="pr__btn" data-plan-reviewer-discard @click="discard">
            {{ t('subagents.planReviewer.discard') }}
          </button>
          <button
            type="button"
            class="pr__btn pr__btn--pri"
            data-plan-reviewer-save
            :disabled="saving"
            @click="save"
          >
            {{ t('subagents.planReviewer.save') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The /plan reviewer's settings block (plan 313): one global row for the whole
 * install, because the reviewer is dispatched by the exit-plan approval rather
 * than by a primary agent. Only two things are configurable — the model and the
 * thinking level — and both live in the page's own form, one group save each.
 *
 * The row is the approval picker's DEFAULT pre-selection: the reviewer chosen on
 * the approval card still wins, and an empty row leaves that default as it was.
 */
import { computed, onBeforeUnmount, ref } from 'vue'

// The goal evaluator's clearable model picker, unchanged: it answers a configured
// row's id and knows nothing about the goal evaluator but its first caller.
import GoalEvaluatorModelSelect from '@/components/settings/GoalEvaluatorModelSelect.vue'
import type { SettingsGroup } from '@/lib/settingsSchema'
import { effortLevelLabel } from '@/lib/effortLabel'
import { useModelChoices } from '@/composables/useModelChoices'
import { useI18n } from '@/locales'

import type { SettingsRefusal, useSettingsForm } from '@/components/settings/form/useSettingsForm'

const props = withDefaults(defineProps<{ form: ReturnType<typeof useSettingsForm>; bare?: boolean }>(), { bare: false })

const MODEL_PATH = 'agents.plan_reviewer.model'
const EFFORT_PATH = 'agents.plan_reviewer.reasoning_effort'

const { t } = useI18n()
const { choices, ensureChoices } = useModelChoices()
ensureChoices()

/** The group the page's own save/dirty machinery reads: two fields, one save. */
const group: SettingsGroup = {
  id: 'plan-reviewer-model',
  titleKey: 'subagents.planReviewer.title',
  fields: [
    { path: MODEL_PATH, kind: 'model' },
    { path: EFFORT_PATH, kind: 'effort' },
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
  const options = [{ value: '', label: t('subagents.planReviewer.effortFollowRow') }]
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
  // A level is the model row's, so without one there is nothing to pick and the
  // block says which setting to choose first (the subagent drawer's own words).
  if (!row) return t('subagents.effortNeedsModel')
  if (effectiveEffort.value === rowDefault.value) {
    return rowDefault.value
      ? t('subagents.planReviewer.hintRowDefault', { effort: effortText(rowDefault.value) })
      : t('subagents.planReviewer.hintRowNone')
  }
  return t('subagents.planReviewer.hintOverridden', { effort: effortText(effectiveEffort.value) })
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
/* The goal evaluator's block, one row down: same rhythm, same tray. */
.pr {
  margin-top: 36px;
}
.pr__head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.pr__head h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--forebrain-text);
  white-space: nowrap;
}
.pr__row {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 360px;
  column-gap: 24px;
  align-items: center;
  padding: 12px 0 12px 16px;
}
.pr__row + .pr__row {
  border-top: 1px solid var(--forebrain-divider);
}
/* The unsaved marker: a line down the row's left edge while the draft differs. */
.pr__row--dirty::before {
  content: '';
  position: absolute;
  left: 0;
  top: 8px;
  bottom: 8px;
  width: 2px;
  border-radius: 2px;
  background: var(--forebrain-brand-1);
}
.pr__lab {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.pr__lab > b {
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
}
.pr__hint {
  font-size: 12px;
  line-height: 1.5;
  color: var(--forebrain-muted-text);
}
.pr__link {
  align-self: flex-start;
  font-size: 12px;
  color: var(--forebrain-brand-1);
  cursor: pointer;
}
.pr__ctl {
  min-width: 0;
}
.pr__select {
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
.pr__select--unset,
.pr__select:disabled {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-muted-text);
  cursor: not-allowed;
}
/* The save bar, collapsed to nothing until a row differs or a save landed. */
.pr__tray {
  display: grid;
  grid-template-rows: 0fr;
  transition: grid-template-rows 0.24s ease;
}
.pr__tray--open {
  grid-template-rows: 1fr;
}
.pr__tray-in {
  min-height: 0;
  overflow: hidden;
}
.pr__tray-bar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 0 2px 16px;
  border-top: 1px solid var(--forebrain-divider);
  margin-top: 2px;
}
.pr__ok {
  margin-right: auto;
  font-size: 12px;
  color: var(--forebrain-brand-1);
}
.pr__err {
  margin-right: auto;
  font-size: 12px;
  color: var(--forebrain-danger);
}
.pr__btn {
  border-radius: 8px;
  padding: 5px 14px;
  font-size: 13px;
  color: var(--forebrain-text-2);
  cursor: pointer;
}
.pr__btn--pri {
  background: var(--forebrain-brand-1);
  color: #fff;
}
.pr__btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
