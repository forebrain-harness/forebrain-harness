<template>
  <div class="settings-row" :class="{ 'settings-row--stack': stacked }" :data-field="field.path">
    <div class="settings-row__label">
      <button
        v-if="field.restore"
        type="button"
        class="settings-row__act"
        :disabled="disabled || restoreDisabled"
        :data-restore="field.path"
        @click="emit('restore')"
      >
        {{ t('settings.restoreDefault') }}
      </button>
      <b v-if="showLabel" :class="{ 'settings-row__title': titleLabel }">{{ t(labelKey) }}</b>
      <span class="settings-row__hint">
        {{ outOfRange ? t('settings.rangeHint', { range: rangeText(field) }) : hint }}
      </span>
    </div>

    <div class="settings-row__ctl">
      <SwitchComponent
        v-if="field.kind === 'bool'"
        :model-value="boolValue"
        :disabled="disabled"
        :aria-label="t(labelKey)"
        :data-switch="field.path"
        @update:model-value="emit('update:value', $event)"
      />

      <RadioGroup
        v-else-if="field.kind === 'enum' || field.kind === 'preset'"
        :model-value="String(value ?? '')"
        :class="field.kind === 'preset' ? 'settings-radios settings-radios--vert' : 'settings-radios'"
        :aria-label="t(labelKey)"
        @update:model-value="(next) => emit('update:value', String(next))"
      >
        <div v-for="option in radioOptions" :key="option.value" class="settings-radio">
          <RadioGroupItem :id="`${field.path}-${option.value}`" :value="option.value" :disabled="disabled" />
          <Label :for="`${field.path}-${option.value}`" class="settings-radio__label">
            <span v-if="option.description" class="settings-radio__desc">
              <b>{{ option.label }}</b>
              <span>{{ option.description }}</span>
            </span>
            <template v-else>{{ option.label }}</template>
          </Label>
        </div>
      </RadioGroup>

      <span v-else-if="field.kind === 'num'" class="settings-num">
        <span class="settings-num__box" :class="{ 'is-bad': outOfRange }">
          <input
            :value="value ?? ''"
            inputmode="numeric"
            type="text"
            :disabled="disabled"
            :aria-label="t(labelKey)"
            :data-num="field.path"
            @input="onNumber"
          />
          <em v-if="field.unit">{{ field.unit }}</em>
        </span>
        <span v-if="rangeText(field)" class="settings-num__range">{{ rangeText(field) }}</span>
      </span>

      <!-- A model row offers the install's configured models by id (plan 266):
           the same provider and model may be configured twice, so the row — not
           the pair — is what a pick names, and the signature is what tells the
           two apart. 「当前会话的模型」 is the empty string on the wire; a
           SelectItem may not carry it, so the sentinel stands in on both sides. -->
      <Select
        v-else-if="field.kind === 'model'"
        :model-value="toChoice(String(value ?? ''))"
        :disabled="disabled"
        @update:model-value="(next) => emit('update:value', fromChoice(next))"
      >
        <SelectTrigger class="settings-input !h-auto min-h-[32px] py-1" :aria-label="t(labelKey)">
          <ModelSignature
            v-if="selectedChoice"
            :model="selectedChoice.model"
            :provider-name="selectedChoice.providerName || selectedChoice.provider"
            :base-url="selectedChoice.baseUrl"
            :api-path="selectedChoice.apiPath"
          />
          <span v-else class="settings-row__hint">{{ field.placeholder }}</span>
        </SelectTrigger>
        <SelectContent>
          <SelectItem :value="EMPTY_CHOICE">{{ field.placeholder }}</SelectItem>
          <SelectItem v-for="choice in choices" :key="choice.id" :value="choice.id">
            <ModelSignature
              :model="choice.model"
              :provider-name="choice.providerName || choice.provider"
              :base-url="choice.baseUrl"
              :api-path="choice.apiPath"
            />
          </SelectItem>
        </SelectContent>
      </Select>

      <!-- A credential: bullets and the last four while it is set, a password
           box once 更换 is pressed, and leaving that box blank keeps the
           stored value (plan 191 §0.20). -->
      <span v-else-if="field.kind === 'secret'" class="settings-secret">
        <template v-if="secretSet && !editing">
          <span class="settings-input settings-input--masked" :data-masked="field.path">
            ••••••••<b>{{ secretLast4 }}</b>
          </span>
          <button
            type="button"
            class="settings-row__act"
            :disabled="disabled"
            :data-secret-edit="field.path"
            @click="editing = true"
          >
            {{ t('settings.secretChange') }}
          </button>
        </template>
        <template v-else>
          <input
            v-model="typed"
            type="password"
            class="settings-input settings-input--mono"
            autocomplete="new-password"
            spellcheck="false"
            :disabled="disabled"
            :placeholder="secretSet ? t('settings.secretPlaceholder') : ''"
            :aria-label="t(labelKey)"
            :data-secret="field.path"
          />
          <button
            v-if="secretSet"
            type="button"
            class="settings-row__act"
            :disabled="disabled"
            :data-secret-cancel="field.path"
            @click="cancelSecret"
          >
            {{ t('common.cancel') }}
          </button>
        </template>
      </span>

      <textarea
        v-else-if="field.kind === 'area'"
        class="settings-input settings-input--area"
        :rows="field.rows ?? 3"
        :value="String(value ?? '')"
        :disabled="disabled"
        :aria-label="t(labelKey)"
        :data-area="field.path"
        @input="emit('update:value', ($event.target as HTMLTextAreaElement).value)"
      />

      <span v-else-if="field.kind === 'tags'" class="settings-tags">
        <TagsInput
          :model-value="tagValues"
          :add-on-blur="true"
          :disabled="disabled"
          :placeholder="field.placeholder"
          :aria-label="t(labelKey)"
          :data-tags="field.path"
          class="settings-input settings-tags__box"
          @update:model-value="onTags"
        >
          <TagsInputItem v-for="tag in tagValues" :key="tag" :value="tag" class="settings-tag">
            {{ tag }}
            <TagsInputItemDelete :aria-label="tag" />
          </TagsInputItem>
          <TagsInputInput :placeholder="field.placeholder" />
        </TagsInput>
      </span>

      <span v-else-if="field.kind === 'list'" class="settings-list">
        <div v-for="(item, index) in listValues" :key="index" class="settings-list__row">
          <input
            class="settings-input settings-input--mono"
            :value="item"
            :placeholder="field.placeholder"
            :disabled="disabled"
            :aria-label="t(labelKey)"
            :data-list="field.path"
            :data-i="index"
            @input="setList(index, ($event.target as HTMLInputElement).value)"
          />
          <button
            type="button"
            class="settings-row__act settings-row__act--danger"
            :disabled="disabled"
            :aria-label="t('common.delete')"
            :data-del="field.path"
            :data-i="index"
            @click="removeList(index)"
          >
            ×
          </button>
        </div>
        <button type="button" class="settings-add" :disabled="disabled" :data-add="field.path" @click="addList">
          + {{ t(field.addLabelKey ?? 'settings.addDirectory') }}
        </button>
      </span>

      <span v-else-if="field.kind === 'pairs'" class="settings-list">
        <div v-for="(pair, index) in pairValues" :key="index" class="settings-list__row">
          <input
            class="settings-input settings-input--mono"
            :value="pair[0]"
            :placeholder="field.placeholders?.[0]"
            :disabled="disabled"
            :aria-label="field.columns?.[0]"
            :data-pair="field.path"
            :data-i="index"
            data-c="0"
            @input="setPair(index, 0, ($event.target as HTMLInputElement).value)"
          />
          <input
            class="settings-input settings-input--mono"
            :value="pair[1]"
            :placeholder="field.placeholders?.[1]"
            :disabled="disabled"
            :aria-label="field.columns?.[1]"
            :data-pair="field.path"
            :data-i="index"
            data-c="1"
            @input="setPair(index, 1, ($event.target as HTMLInputElement).value)"
          />
          <button
            type="button"
            class="settings-row__act settings-row__act--danger"
            :disabled="disabled"
            :aria-label="t('common.delete')"
            :data-del="field.path"
            :data-i="index"
            @click="removePair(index)"
          >
            ×
          </button>
        </div>
        <button type="button" class="settings-add" :disabled="disabled" :data-add="field.path" @click="addPair">
          + {{ t(field.addLabelKey ?? 'settings.addMapping') }}
        </button>
      </span>

      <input
        v-else
        :value="String(value ?? '')"
        class="settings-input"
        :class="{ 'settings-input--mono': field.mono }"
        :placeholder="field.placeholder"
        :disabled="disabled"
        :aria-label="t(labelKey)"
        :data-text="field.path"
        @input="emit('update:value', ($event.target as HTMLInputElement).value)"
      />

      <p v-if="error" class="settings-row__error">{{ error }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One settings row (plan 191 §4.1): the field's name and its grey explanation
 * on the left, the control the prototype draws for that kind on the right.
 *
 * The row is a description rendered, not a form built by hand: the kind decides
 * the control, the range decides when a number turns red, and the credential
 * kind is the one place the page has a small state machine of its own — bullets
 * and the last four characters until 更换 opens a password box, and a blank box
 * means "leave the stored value alone" (§0.20).
 */
import { computed, ref, watch } from 'vue'
import { RadioGroup, RadioGroupItem } from '@repo/shadcn-vue/components/ui/radio-group'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@repo/shadcn-vue/components/ui/select'
import {
  TagsInput,
  TagsInputInput,
  TagsInputItem,
  TagsInputItemDelete,
} from '@repo/shadcn-vue/components/ui/tags-input'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import ModelSignature from '@/components/models/ModelSignature.vue'
import { EMPTY_CHOICE, fromChoice, toChoice } from '@/lib/selectValue'
import { APPROVAL_PRESETS, fieldLabelKey, numberOutOfRange, rangeText, type SettingsField } from '@/lib/settingsSchema'
import { useModelChoices } from '@/composables/useModelChoices'
import { useI18n, type I18nKey } from '@/locales'

const props = withDefaults(
  defineProps<{
    field: SettingsField
    value: unknown
    /** Whether the field's name is drawn; a lone field in a lone group is not. */
    showLabel?: boolean
    /** Draw the name in the group-title type (the memory switch). */
    titleLabel?: boolean
    /** Whether a credential is stored, and its tail. */
    secretSet?: boolean
    secretLast4?: string
    /** The refusal the gateway answered for this path, if any. */
    error?: string
    disabled?: boolean
    /** Whether 恢复默认 is greyed out (the text already is the built-in one). */
    restoreDisabled?: boolean
    /** Whether the group's save just landed: a credential row closes its box
     * and goes back to the mask (the prototype's secEdit.delete). */
    justSaved?: boolean
  }>(),
  {
    showLabel: true,
    titleLabel: false,
    secretSet: false,
    secretLast4: '',
    error: '',
    disabled: false,
    restoreDisabled: false,
    justSaved: false,
  },
)

const emit = defineEmits<{
  'update:value': [value: unknown]
  'update:secret': [value: string]
  restore: []
}>()

const { t: translate } = useI18n()

/**
 * A locale sentence by a key this page composed at runtime — a field's name is
 * `settings.field.<path>`, and the paths come from the schema, so the key is
 * not one the type checker can enumerate. The key itself is the fallback, which
 * is what makes a missing entry visible rather than blank.
 */
function t(key: string, params?: Record<string, string | number>): string {
  return translate(key as I18nKey, params)
}

const labelKey = computed(() => fieldLabelKey(props.field.path))

/** The install's models, asked for once per page. Only a model row needs them. */
const { choices, ensureChoices } = useModelChoices()
if (props.field.kind === 'model') ensureChoices()

/** The row this field's stored id names, or null for 「当前会话的模型」. */
const selectedChoice = computed(() => {
  const id = String(props.value ?? '')
  return id ? (choices.value.find((choice) => choice.id === id) ?? null) : null
})
/** The grey explanation, or nothing when the prototype gives the row none. */
const hint = computed(() => {
  const key = `settings.hint.${props.field.path}`
  const text = t(key)
  return text === key ? '' : text
})
const stacked = computed(() => Boolean(props.field.stack) || ['list', 'tags', 'pairs', 'area', 'preset'].includes(props.field.kind))
const outOfRange = computed(() => numberOutOfRange(props.field, props.value))

const boolValue = computed(() => props.value === true)

/** An enum's options, and the approval presets' two described choices. */
const radioOptions = computed(() => {
  if (props.field.kind === 'preset') {
    return APPROVAL_PRESETS.map((preset) => ({
      value: preset.value,
      label: t(preset.labelKey),
      description: t(preset.descriptionKey),
    }))
  }
  return (props.field.options ?? []).map((option) => ({ value: option.value, label: t(option.labelKey), description: '' }))
})

function onNumber(event: Event): void {
  const input = event.target as HTMLInputElement
  const raw = input.value.trim()
  if (raw === '') {
    emit('update:value', undefined)
    return
  }
  const parsed = Number(raw)
  if (Number.isNaN(parsed)) return
  // A clamping field stores the edge rather than the number typed past it
  // (plan 191 §0.19's blur rule, mirroring the loader).
  if (props.field.clamp) {
    if (props.field.min !== undefined && parsed < props.field.min) {
      emit('update:value', props.field.min)
      input.value = String(props.field.min)
      return
    }
    if (props.field.max !== undefined && parsed > props.field.max) {
      emit('update:value', props.field.max)
      input.value = String(props.field.max)
      return
    }
  }
  emit('update:value', parsed)
}

// A credential's typed value lives here, not in the group's draft: only what
// the user actually typed travels as a secret.
const typed = ref('')
const editing = ref(false)

watch(typed, (value) => emit('update:secret', value))

// The group's save landing closes the box: the mask is back, with the tail of
// what was just stored (the prototype's secEdit.delete on save).
watch(
  () => props.justSaved,
  (saved) => {
    if (!saved) return
    typed.value = ''
    editing.value = false
  },
)

function cancelSecret(): void {
  typed.value = ''
  editing.value = false
}

const tagValues = computed(() => (Array.isArray(props.value) ? props.value.map((item) => String(item)) : []))

function onTags(value: unknown): void {
  emit('update:value', Array.isArray(value) ? value.map((item) => String(item)) : [])
}

const listValues = computed(() => (Array.isArray(props.value) ? props.value.map((item) => String(item)) : []))

function setList(index: number, next: string): void {
  const copy = [...listValues.value]
  copy[index] = next
  emit('update:value', copy)
}

function removeList(index: number): void {
  emit('update:value', listValues.value.filter((_, at) => at !== index))
}

function addList(): void {
  emit('update:value', [...listValues.value, ''])
}

const pairValues = computed<[string, string][]>(() =>
  Array.isArray(props.value) ? (props.value as [string, string][]) : [],
)

function setPair(index: number, column: 0 | 1, next: string): void {
  const copy = pairValues.value.map((pair) => [pair[0] ?? '', pair[1] ?? ''] as [string, string])
  copy[index][column] = next
  emit('update:value', copy)
}

function removePair(index: number): void {
  emit('update:value', pairValues.value.filter((_, at) => at !== index))
}

function addPair(): void {
  emit('update:value', [...pairValues.value, ['', ''] as [string, string]])
}
</script>

<style scoped>
.settings-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px 24px;
  padding: 11px 0;
  border-top: 1px solid var(--forebrain-divider);
}
.settings-row:first-child {
  border-top: 0;
  padding-top: 0;
}
.settings-row--stack {
  grid-template-columns: minmax(0, 1fr);
}
.settings-row__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.settings-row__label > b {
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
}
.settings-row__title {
  font-size: 15px;
  font-weight: 600;
}
.settings-row__hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.settings-row__ctl {
  display: flex;
  flex-direction: column;
  gap: 6px;
  align-items: flex-start;
  min-width: 0;
}
.settings-row__error {
  font-size: 12px;
  color: var(--forebrain-danger);
}
.settings-row__act {
  align-self: flex-start;
  font-size: 12px;
  color: var(--forebrain-brand-1);
  padding: 0;
}
.settings-row__act:disabled {
  color: var(--forebrain-muted-text);
  cursor: not-allowed;
}
.settings-row__act--danger {
  color: var(--forebrain-danger);
  font-size: 14px;
}
.settings-input {
  width: 100%;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-input-bg);
  padding: 6px 10px;
  font-size: 13px;
  color: var(--forebrain-text);
}
.settings-input--mono {
  font-family: var(--forebrain-mono-font, ui-monospace, SFMono-Regular, monospace);
}
.settings-input--area {
  min-height: 120px;
  resize: vertical;
}
.settings-input--masked {
  letter-spacing: 1px;
  color: var(--forebrain-text-2);
}
.settings-input--masked b {
  letter-spacing: 0;
  color: var(--forebrain-text);
}
.settings-radios {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 14px;
}
.settings-radios--vert {
  flex-direction: column;
  gap: 8px;
}
.settings-radio {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
.settings-radio__label {
  font-size: 13px;
  font-weight: 400;
  color: var(--forebrain-text);
}
.settings-radio__desc {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.settings-radio__desc > span {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.settings-num {
  display: flex;
  align-items: center;
  gap: 8px;
}
.settings-num__box {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-input-bg);
  padding: 6px 10px;
  width: 120px;
}
.settings-num__box.is-bad {
  border-color: var(--forebrain-danger);
}
.settings-num__box input {
  width: 100%;
  background: transparent;
  font-size: 13px;
  color: var(--forebrain-text);
  outline: none;
}
.settings-num__box em {
  font-size: 12px;
  font-style: normal;
  color: var(--forebrain-muted-text);
}
.settings-num__range {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.settings-secret {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}
.settings-tags,
.settings-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.settings-tags__box {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding: 6px 8px;
}
.settings-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border-radius: 6px;
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  font-size: 12px;
  padding: 1px 6px;
}
.settings-list__row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.settings-add {
  align-self: flex-start;
  font-size: 12px;
  color: var(--forebrain-brand-1);
}
</style>
