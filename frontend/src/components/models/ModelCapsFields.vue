<template>
  <div class="caps">
    <div class="caps__head">
      <b>{{ t('models.advanced') }}</b>
      <span class="caps__changed">{{ changedCount ? t('models.advancedChanged', { n: changedCount }) : '' }}</span>
      <Button
        type="button"
        variant="ghost"
        class="forebrain-btn forebrain-btn--ghost h-[26px] px-2.5 text-xs"
        :aria-expanded="open"
        data-testid="models-advanced-toggle"
        @click="emit('update:open', !open)"
      >
        {{ open ? t('models.collapse') : t('models.expand') }}
      </Button>
    </div>

    <div v-if="open" class="caps__rows">
      <!-- 「支持的输入」: the name and its four boxes share one line (§Step 4). -->
      <div class="caps__row caps__row--inline">
        <div class="caps__label"><b>{{ t('models.capsInputs') }}</b></div>
        <div class="caps__ctl caps__checks">
          <label v-for="box in inputBoxes" :key="box.key" class="caps__check">
            <input
              type="checkbox"
              :checked="Boolean(draft[box.key])"
              :data-cap="box.key"
              @change="setFlag(box.key, ($event.target as HTMLInputElement).checked)"
            />
            {{ t(box.label) }}
          </label>
        </div>
      </div>

      <div class="caps__row">
        <div class="caps__label"><b>{{ t('models.capsReasoning') }}</b></div>
        <div class="caps__ctl">
          <SwitchComponent
            :model-value="draft.reasoning !== false"
            :aria-label="t('models.capsReasoning')"
            data-switch="reasoning"
            @update:model-value="setFlag('reasoning', $event)"
          />
        </div>
      </div>

      <div class="caps__row">
        <div class="caps__label"><b>{{ t('models.capsReasoningOff') }}</b></div>
        <div class="caps__ctl">
          <SwitchComponent
            :model-value="draft.reasoningOff === true"
            :aria-label="t('models.capsReasoningOff')"
            data-switch="reasoningOff"
            @update:model-value="setFlag('reasoningOff', $event)"
          />
        </div>
      </div>

      <!-- A source that listed the levels answered the question: the levels are
           the dropdown's own options, so they are not offered again as boxes. -->
      <div v-if="!effortsKnown" class="caps__row caps__row--stack">
        <div class="caps__label"><b>{{ t('models.capsEfforts') }}</b></div>
        <div class="caps__ctl caps__checks">
          <label v-for="level in pickableLevels" :key="level" class="caps__check">
            <input
              type="checkbox"
              :checked="draftEfforts.includes(level)"
              :data-effort="level"
              @change="toggleEffort(level, ($event.target as HTMLInputElement).checked)"
            />
            {{ effortLevelLabel(level) }}<span class="caps__en">{{ level }}</span>
          </label>
        </div>
      </div>

      <div class="caps__row">
        <div class="caps__label"><b>{{ t('models.capsEffort') }}</b></div>
        <div class="caps__ctl">
          <Select
            :model-value="toChoice(defaultEffort)"
            @update:model-value="(next) => emit('update:defaultEffort', fromChoice(next))"
          >
            <SelectTrigger class="caps__input" :aria-label="t('models.capsEffort')" data-field="defaultEffort">
              <SelectValue :placeholder="effortsLoading ? t('models.capsEffortLoading') : ''" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="EMPTY_CHOICE">{{ t('chat.effortDefault') }}</SelectItem>
              <SelectItem v-for="level in effortOptions" :key="level" :value="level">
                {{ effortLevelLabel(level) }} <span class="caps__en">{{ level }}</span>
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div class="caps__row">
        <div class="caps__label"><b>{{ t('models.capsContext') }}</b></div>
        <div class="caps__ctl">
          <span class="caps__num">
            <input
              type="text"
              inputmode="numeric"
              :value="draft.contextWindow ?? ''"
              :placeholder="placeholder(catalog.contextWindow)"
              :aria-label="t('models.capsContext')"
              data-field="contextWindow"
              @input="setNumber('contextWindow', ($event.target as HTMLInputElement).value)"
            />
            <em>{{ t('models.capsTokens') }}</em>
          </span>
        </div>
      </div>

      <div class="caps__row">
        <div class="caps__label"><b>{{ t('models.capsMaxOutput') }}</b></div>
        <div class="caps__ctl">
          <span class="caps__num">
            <input
              type="text"
              inputmode="numeric"
              :value="draft.maxOutput ?? ''"
              :placeholder="placeholder(catalog.maxOutput)"
              :aria-label="t('models.capsMaxOutput')"
              data-field="maxOutput"
              @input="setNumber('maxOutput', ($event.target as HTMLInputElement).value)"
            />
            <em>{{ t('models.capsTokens') }}</em>
          </span>
        </div>
      </div>

      <!-- 请求参数 is a block of its own, not one row among the others. -->
      <div class="caps__json">
        <div class="caps__json-head">
          <b>{{ t('models.params') }}</b>
          <span>{{ t('models.paramsHint') }}</span>
          <span v-if="prettyNote === 'ok'" class="caps__ok">{{ t('models.paramsPrettyOk') }}</span>
          <span v-else-if="prettyNote === 'bad'" class="caps__bad">{{ t('models.paramsPrettyBad') }}</span>
          <Button
            type="button"
            variant="ghost"
            class="forebrain-btn forebrain-btn--ghost h-[26px] px-2.5 text-xs"
            data-testid="models-params-pretty"
            @click="pretty"
          >
            {{ t('models.paramsPretty') }}
          </Button>
        </div>
        <ParamsJsonField
          :model-value="params"
          :invalid="prettyNote === 'bad'"
          :aria-label="t('models.params')"
          @update:model-value="(next) => emit('update:params', next)"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The model form's advanced section (plan 266): what the model accepts, how it
 * thinks, how much it holds, and the request parameters merged into every call.
 *
 * Every control starts at the value the catalog reports — that is why the
 * switches and boxes are two-state rather than three: an untouched control *is*
 * 「follow the catalog」, and the caller stores an override only for the ones the
 * person changed. The thinking levels are read from `effortsKnown`: a source that
 * listed them (the vendor's own model list, or models.dev) answers the question,
 * so the boxes would be a second, weaker way to say the same thing.
 */
import { computed, ref } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'

import SwitchComponent from '@/components/common/SwitchComponent.vue'
import ParamsJsonField from '@/components/models/ParamsJsonField.vue'
import { EFFORT_CHOICES, effortLevelLabel } from '@/lib/effortLabel'
import { EMPTY_CHOICE, fromChoice, toChoice } from '@/lib/selectValue'
import type { LlmModelCaps } from '@/lib/api'
import { useI18n, type I18nKey } from '@/locales'

const props = withDefaults(
  defineProps<{
    /** The values as they stand: the catalog's, with the row's overrides on top. */
    draft: LlmModelCaps
    /** The catalog's own values, which a control's placeholder shows. */
    catalog: LlmModelCaps
    /** The thinking levels this model's levels were checked with. */
    draftEfforts: string[]
    /** Levels a source listed; empty when none did. */
    sourceEfforts: string[]
    effortsKnown: boolean
    effortsLoading?: boolean
    params: string
    open: boolean
    changedCount: number
  }>(),
  { effortsLoading: false },
)

const emit = defineEmits<{
  'update:draft': [draft: LlmModelCaps]
  'update:draftEfforts': [levels: string[]]
  'update:params': [text: string]
  'update:open': [open: boolean]
  'update:defaultEffort': [level: string]
}>()

const { t } = useI18n()

/** The levels a person may tick: the six, `none` being a capability. */
const pickableLevels = EFFORT_CHOICES

const inputBoxes: { key: 'images' | 'pdf' | 'video' | 'audio'; label: I18nKey }[] = [
  { key: 'images', label: 'models.capsImage' },
  { key: 'pdf', label: 'models.capsPdf' },
  { key: 'video', label: 'models.capsVideo' },
  { key: 'audio', label: 'models.capsAudio' },
]

const defaultEffort = computed(() => String(props.draft.defaultEffort ?? ''))

/** The dropdown's options: what a source listed, or what the person ticked. */
const effortOptions = computed(() => {
  if (props.effortsKnown) return props.sourceEfforts
  return props.draftEfforts.filter((level) => level !== 'none')
})

/** The catalog's own number, as the box's placeholder — the number, nothing more. */
function placeholder(value: number | undefined): string {
  return value === undefined || value === null ? '' : String(value)
}

function setFlag(key: keyof LlmModelCaps, value: boolean): void {
  emit('update:draft', { ...props.draft, [key]: value })
}

function toggleEffort(level: string, checked: boolean): void {
  const kept = props.draftEfforts.filter((entry) => entry !== level)
  const next = checked ? [...kept, level] : kept
  // The order is the levels' own, so a stored list reads the same however the
  // person ticked it.
  emit(
    'update:draftEfforts',
    EFFORT_CHOICES.filter((entry) => next.includes(entry)),
  )
}

function setNumber(key: 'contextWindow' | 'maxOutput', raw: string): void {
  const digits = raw.replace(/[^0-9]/g, '')
  const next = { ...props.draft }
  if (digits === '') delete next[key]
  else next[key] = Number(digits)
  emit('update:draft', next)
}

/** 「格式化」 rewrites the text only when it parses: a broken document is left
 *  exactly as typed, with the reason on the toolbar. */
const prettyNote = ref<'' | 'ok' | 'bad'>('')
function pretty(): void {
  const raw = props.params.trim()
  if (!raw) {
    prettyNote.value = ''
    return
  }
  try {
    emit('update:params', JSON.stringify(JSON.parse(raw), null, 2))
    prettyNote.value = 'ok'
  } catch {
    prettyNote.value = 'bad'
  }
}
</script>

<style scoped>
.caps__head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 18px;
  padding-top: 12px;
  border-top: 1px solid var(--forebrain-divider);
}
.caps__head b {
  font-size: 12.5px;
  font-weight: 600;
}
.caps__changed {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.caps__head :deep(.forebrain-btn) {
  margin-left: auto;
}
.caps__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 230px;
  column-gap: 16px;
  align-items: center;
  padding: 11px 0;
  border-bottom: 1px solid var(--forebrain-divider);
}
.caps__row:last-child {
  border-bottom: 0;
}
.caps__row--inline {
  grid-template-columns: auto minmax(0, 1fr);
}
.caps__row--stack {
  grid-template-columns: minmax(0, 1fr);
  row-gap: 8px;
}
.caps__label b {
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
}
.caps__ctl {
  display: flex;
  justify-content: flex-end;
}
.caps__row--inline .caps__ctl,
.caps__row--stack .caps__ctl {
  justify-content: flex-start;
}
.caps__checks {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
}
.caps__row--inline .caps__checks {
  flex-wrap: nowrap;
}
.caps__check {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12.5px;
  color: var(--forebrain-text);
  white-space: nowrap;
  cursor: pointer;
}
.caps__check input {
  width: 15px;
  height: 15px;
  accent-color: var(--forebrain-brand-1);
}
/* The English half of a level's label: smaller and quieter, so the Chinese
   label stays the one the eye lands on. */
.caps__en {
  margin-left: 4px;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.caps__input {
  height: 32px;
  width: 100%;
  font-size: 12.5px;
}
.caps__num {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  background: var(--forebrain-input-bg);
}
.caps__num:focus-within {
  border-color: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
}
.caps__num input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  color: var(--forebrain-text);
}
.caps__num input::placeholder {
  color: var(--forebrain-muted-text);
}
.caps__num em {
  font-style: normal;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
}
.caps__json {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--forebrain-divider);
}
.caps__json-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
}
.caps__json-head b {
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
  white-space: nowrap;
}
.caps__json-head span {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  flex: 1;
  min-width: 0;
}
.caps__json-head span.caps__ok {
  flex: none;
  color: var(--forebrain-success);
}
.caps__json-head span.caps__bad {
  flex: none;
  color: var(--forebrain-danger);
}
.caps__json-head :deep(.forebrain-btn) {
  flex: none;
}
</style>
