<template>
  <!-- The install's retention setting, flat in its tab (owner 2026-10-18):
       the one number, what it means, and the two buttons that write it. -->
  <div class="grid gap-3">
    <p
      v-if="errorText"
      data-testid="cron-retention-error"
      class="text-[12px] text-[var(--forebrain-danger)]"
    >{{ errorText }}</p>
    <p v-if="notice" class="text-[12px] text-[var(--forebrain-text-2)]">{{ notice }}</p>

    <div class="grid gap-1.5">
      <label class="forebrain-form-field w-32">
        <span>{{ t('cronSettings.retentionLabel') }}</span>
        <input
          v-model.number="draft"
          type="number"
          :min="settings?.minDays"
          :max="settings?.maxDays"
          step="1"
          class="forebrain-field h-[34px] w-full"
          data-testid="cron-retention-days"
          :disabled="loading || saving || resetting"
        />
      </label>
      <p class="forebrain-list-note">{{ t('cronSettings.retentionDescription') }}</p>
      <p v-if="settings && !settings.configured" class="forebrain-list-note">
        {{ t('cronSettings.defaultHint', { days: settings.defaultDays }) }}
      </p>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <button
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        data-testid="cron-retention-save"
        :disabled="loading || saving || !changed"
        @click="save"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </button>
      <button
        type="button"
        class="forebrain-btn forebrain-btn-ghost text-xs"
        data-testid="cron-retention-reset"
        :disabled="loading || resetting"
        @click="restoreDefault"
      >
        {{ resetting ? t('common.loading') : t('cronSettings.reset') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import forebrainApi, { type CronSettings, GatewayHttpError, getErrorMessage } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * Retention is install-level configuration (forebrain.yaml's cron section),
 * so it lives in the settings page; the /cron page only reads it. The range
 * the GET carries is checked here before any request, so an out-of-range
 * number is refused in the viewer's language without a round trip.
 */
const { t } = useI18n()
const settings = ref<CronSettings | null>(null)
const draft = ref<number | ''>(0)
const loading = ref(false)
const saving = ref(false)
const resetting = ref(false)
const error = ref('')
const notice = ref('')

/** The typed number, or null when the field holds nothing whole. */
const draftDays = computed(() => (typeof draft.value === 'number' && Number.isInteger(draft.value) ? draft.value : null))

const outOfRange = computed(() => {
  if (!settings.value) return false
  const days = draftDays.value
  return days === null || days < settings.value.minDays || days > settings.value.maxDays
})

// Local validation speaks first; a server refusal only shows once the range
// itself is satisfied.
const errorText = computed(() => {
  if (error.value) return error.value
  if (!outOfRange.value || !settings.value) return ''
  return t('cronSettings.outOfRange', { min: settings.value.minDays, max: settings.value.maxDays })
})

const changed = computed(() => Boolean(settings.value) && draft.value !== settings.value?.retentionDays)

async function loadSettings() {
  loading.value = true
  error.value = ''
  try {
    settings.value = await forebrainApi.cronSettings()
    draft.value = settings.value.retentionDays
  } catch (cause) {
    error.value = cause instanceof GatewayHttpError
      ? t('cronSettings.failedStatus', { status: cause.status })
      : getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!settings.value || outOfRange.value || draftDays.value === null) return
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.saveCronSettings(draftDays.value)
    // The GET carries what the write left in force — the configured flag and
    // the effective days — so the form redraws from the same source as /cron.
    settings.value = await forebrainApi.cronSettings()
    draft.value = settings.value.retentionDays
    notice.value = t('cronSettings.saved')
  } catch (cause) {
    error.value = cause instanceof GatewayHttpError
      ? t('cronSettings.failedStatus', { status: cause.status })
      : getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

async function restoreDefault() {
  resetting.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.saveCronSettings(null)
    settings.value = await forebrainApi.cronSettings()
    draft.value = settings.value.retentionDays
    notice.value = t('cronSettings.saved')
  } catch (cause) {
    error.value = cause instanceof GatewayHttpError
      ? t('cronSettings.failedStatus', { status: cause.status })
      : getErrorMessage(cause)
  } finally {
    resetting.value = false
  }
}

onMounted(() => {
  void loadSettings()
})
</script>
