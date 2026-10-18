<template>
  <!-- The global approval default, flat in its tab (owner 2026-10-18): the one
       choice, what each preset means, and the save that writes it to
       forebrain.yaml. -->
  <div class="grid gap-3">
    <div class="grid gap-1.5">
      <RadioField
        :model-value="chosen"
        :label="t('approval.title')"
        name="approval-default"
        :options="options"
        vertical
        data-testid="approval-default"
        @update:model-value="chosen = $event"
      />
      <!-- The help names the two configuration keys in monospace, so it is
           split around the placeholder and drawn here rather than as the
           group's plain hint. -->
      <p class="forebrain-list-note">
        {{ helpParts[0] }}<span class="font-mono">{{ t('approval.configPathHint') }}</span>{{ helpParts[1] }}
      </p>
    </div>

    <p v-if="current === null && loaded" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('approval.custom') }}</p>
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="text-[12px] text-[var(--forebrain-text-2)]">{{ notice }}</p>

    <div>
      <button
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="!changed || saving"
        data-testid="approval-save"
        @click="save"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The global approval default: the same three presets the terminal's
 * /permissions offers, as one standard radio group. The choice is written to
 * forebrain.yaml by the save at the bottom; sessions may override their own
 * view from the composer.
 */
import { computed, onMounted, ref } from 'vue'
import RadioField from '@/components/common/RadioField.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { tIn, useI18n, type I18nKey } from '@/locales'

const { t } = useI18n()

/**
 * One preset's two names. This page names a preset in both languages —
 * `只读 · Read Only` — so a reader of either manual finds the same word here
 * as in the configuration file and in the terminal's /permissions.
 */
function bothNames(key: I18nKey): string {
  return `${tIn('zh', key)} · ${tIn('en', key)}`
}

const options = computed(() => [
  { value: 'read-only', label: bothNames('approval.readOnly'), description: t('approval.readOnlyDescription') },
  { value: 'auto', label: bothNames('approval.default'), description: t('approval.defaultDescription') },
  { value: 'full-access', label: bothNames('approval.fullAccess'), description: t('approval.fullAccessDescription') },
])

/** The help sentence, split around the configuration keys it names. */
const helpParts = computed(() => t('approval.help').split('{path}'))

const current = ref<string | null>(null)
const chosen = ref('')
const loaded = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')

const changed = computed(() => loaded.value && chosen.value !== (current.value ?? ''))

async function load() {
  error.value = ''
  try {
    const res = await forebrainApi.approvalDefault()
    current.value = res.current
    chosen.value = res.current ?? ''
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loaded.value = true
  }
}

async function save() {
  if (!changed.value || saving.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.saveApprovalDefault(chosen.value)
    current.value = res.current
    chosen.value = res.current ?? ''
    notice.value = t('approval.saved')
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void load()
})
</script>
