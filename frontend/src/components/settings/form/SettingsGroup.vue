<template>
  <!-- One group of settings (plan 191 §4.1, §0.3): its title and long
       explanation, its rows, and the save bar that appears the moment one row
       differs. A lone field in a lone group omits both the group title and the
       row's name — the exception is the batch project group, which the
       prototype draws with its field's name (§0.5). -->
  <section
    v-if="visible"
    class="settings-group"
    :class="{ 'settings-group--dirty': dirty }"
    :data-group="group.id"
  >
    <header v-if="showTitle" class="settings-group__head">
      <h3>{{ t(group.titleKey) }}</h3>
      <p v-if="group.noteKey">{{ t(group.noteKey) }}</p>
    </header>

    <SettingsField
      v-for="field in visibleFields"
      :key="field.path"
      :field="field"
      :value="valueFor(field)"
      :show-label="showFieldLabel"
      :title-label="group.titleLabel === true"
      :secret-set="secrets[field.path]?.set === true"
      :secret-last4="secrets[field.path]?.last4 ?? ''"
      :just-saved="savedNotice"
      :models="models"
      :error="errorFor(field.path)"
      :disabled="disabled"
      :restore-disabled="restoreDisabled(field)"
      @update:value="(next) => onChange(field.path, next)"
      @update:secret="(next) => onSecret(field.path, next)"
      @restore="emit('restore', field)"
    />

    <footer v-if="dirty" class="settings-group__save">
      <div class="settings-group__save-actions">
        <button type="button" class="settings-group__cancel" :disabled="saving" @click="emit('cancel')">
          {{ t('settings.discard') }}
        </button>
        <button
          type="button"
          class="settings-group__submit"
          :disabled="saving"
          :data-save="group.id"
          @click="emit('save')"
        >
          {{ saving ? t('common.loading') : t('settings.saveGroup') }}
        </button>
      </div>
      <p v-if="message" class="settings-group__message" role="alert">{{ message }}</p>
    </footer>

    <!-- The success line lives outside the bar: the bar collapses when the
         save lands, and the confirmation outlives it by a couple of seconds. -->
    <p v-if="savedNotice" class="settings-group__saved">{{ t('settings.saved') }}</p>
  </section>
</template>

<script setup lang="ts">
/**
 * A settings group: the frame around one save. The group owns no state of its
 * own beyond what it shows — the page holds the draft — so what a row change
 * means is decided in one place and the bar here is only its surface.
 */
import { computed } from 'vue'
import SettingsField from '@/components/settings/form/SettingsField.vue'
import type { SettingsField as SettingsFieldShape, SettingsGroup } from '@/lib/settingsSchema'
import { useI18n } from '@/locales'

const props = withDefaults(
  defineProps<{
    group: SettingsGroup
    /** The value a row shows (the page resolves the schema's own default). */
    valueFor: (field: SettingsFieldShape) => unknown
    /** The page's draft, which is what a row's `when` is judged against. */
    draft: Record<string, unknown>
    /** Whether any row of the group differs. */
    dirty: boolean
    /** Everything the page knows about the credentials. */
    secrets: Record<string, { set: boolean; last4: string }>
    /** The models a 模型 row offers. */
    models?: string[]
    disabled?: boolean
    saving?: boolean
    /** The refusal to show under the field it names, or under the bar. */
    refusal?: { path: string; message: string } | null
    /** Whether the group's 「保存」just landed. */
    savedNotice?: boolean
    /** Whether a 「恢复默认」button is greyed out for this field. */
    restoreDisabled?: (field: SettingsFieldShape) => boolean
    /** Whether the whole group is drawn (the memory switch's folding). */
    visible?: boolean
    /** Whether this is the only group on its tab (plan 191 §0.5). */
    loneTab?: boolean
  }>(),
  {
    models: () => [],
    disabled: false,
    saving: false,
    refusal: null,
    savedNotice: false,
    restoreDisabled: () => false,
    visible: true,
    loneTab: false,
  },
)

const emit = defineEmits<{
  change: [path: string, value: unknown]
  secret: [path: string, value: string]
  save: []
  cancel: []
  restore: [field: SettingsFieldShape]
}>()

const { t: translate } = useI18n()
const t = (key: string) => translate(key as Parameters<typeof translate>[0])

/**
 * The prototype's `solo` (plan 191 §0.5): a group drawn alone on its tab, or
 * the memory switch's own `flat` group. A solo group carries no group title —
 * its field's name is the heading the reader sees.
 */
const solo = computed(() => props.loneTab === true || props.group.titleLabel === true)
const showTitle = computed(() => !solo.value)
/** A row's name is dropped only by a lone field among several groups: the
 * batch project group (`keepLabel`) and a preset row keep theirs. */
const showFieldLabel = computed(
  () =>
    solo.value ||
    props.group.keepLabel === true ||
    props.group.fields.length > 1 ||
    props.group.fields[0]?.kind === 'preset',
)

/**
 * The rows the group draws now: a field's own condition is judged against the
 * draft, not against what is stored, so a switch that has just been flipped
 * shows its dependent rows before it is saved (plan 191 §0.12, §0.16).
 */
const visibleFields = computed(() => props.group.fields.filter((field) => !field.when || field.when(props.draft)))

const message = computed(() => (props.refusal && !props.refusal.path ? props.refusal.message : ''))

function errorFor(path: string): string {
  return props.refusal?.path === path ? props.refusal.message : ''
}

function onChange(path: string, value: unknown): void {
  emit('change', path, value)
}

function onSecret(path: string, value: string): void {
  emit('secret', path, value)
}
</script>

<style scoped>
.settings-group {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px 16px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  background: var(--forebrain-bg-alt);
}
/* The unsaved marker: a line down the group's left edge while one row differs
   (plan 191 §0.3). */
.settings-group--dirty::before {
  content: '';
  position: absolute;
  left: -1px;
  top: 10px;
  bottom: 10px;
  width: 2px;
  border-radius: 2px;
  background: var(--forebrain-brand-1);
}
.settings-group__head h3 {
  font-size: 15px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.settings-group__head p {
  margin-top: 2px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.settings-group__save {
  display: flex;
  flex-direction: column;
  gap: 6px;
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 10px;
}
.settings-group__save-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.settings-group__cancel,
.settings-group__submit {
  border-radius: 8px;
  padding: 5px 14px;
  font-size: 13px;
}
.settings-group__cancel {
  color: var(--forebrain-text-2);
}
.settings-group__submit {
  background: var(--forebrain-brand-1);
  color: #fff;
}
.settings-group__submit:disabled,
.settings-group__cancel:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.settings-group__message {
  font-size: 12px;
  color: var(--forebrain-danger);
  text-align: right;
}
.settings-group__saved {
  font-size: 12px;
  color: var(--forebrain-brand-1);
  text-align: right;
}
</style>
