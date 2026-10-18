<template>
  <ForebrainModal :open="true" size="md" :title="t('channels.editTitle', { name: channelLabel(row.kind) })" @close="emit('cancel')">
    <!-- The labels are the keys the channel driver reads: there is no table of
         readable names for the eighteen kinds, so the key itself is the name a
         person edits next to. -->
    <div class="grid gap-3 sm:grid-cols-2">
      <label v-for="key in keys" :key="key" class="block">
        <span class="mb-1 block font-mono text-[12px] text-[var(--forebrain-text-2)]">{{ key }}</span>
        <input
          v-model="draft[key]"
          :type="isSecret(key) ? 'password' : 'text'"
          class="forebrain-field h-9 w-full font-mono text-[12px]"
          autocomplete="off"
          :placeholder="storedSecret(key) ? t('channels.secretPlaceholder') : ''"
          :data-testid="`channel-field-${key}`"
        />
      </label>
    </div>
    <p v-if="hasStoredSecret" class="text-[11px] text-[var(--forebrain-muted-text)]" data-testid="channel-secret-hint">
      {{ t('channels.secretNotice') }}
    </p>
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    <template #footer>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="emit('cancel')">{{ t('common.cancel') }}</button>
      <button
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="saving"
        data-testid="channel-save"
        @click="save"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * One channel's configuration, in the keys the channel driver reads (plan 075).
 *
 * Secrets come back masked and are never typed back: a masked field starts
 * empty with the placeholder that says leaving it alone keeps the value, and a
 * save that leaves it empty sends the mask back so the server keeps what it
 * already has. The save writes the whole channel map — a PUT that drops a kind
 * clears it — and replaces only this kind's fields.
 */
import { computed, ref } from 'vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { forebrainApi, getErrorMessage, type ProjectChannelRow } from '@/lib/api'
import { channelLabel } from '@/lib/channelLabels'
import { useI18n } from '@/locales'

/** The placeholder the server hands back in place of a stored secret. */
const REDACTED = '[REDACTED]'

const props = defineProps<{ projectId: string; row: ProjectChannelRow }>()
const emit = defineEmits<{ cancel: []; saved: [] }>()

const { t } = useI18n()

const saving = ref(false)
const error = ref('')

/** Every field of the kind, `enabled` aside — the switch owns that one. */
const keys = computed(() => Object.keys(props.row.fields).filter((key) => key !== 'enabled').sort())

function isSecret(key: string): boolean {
  return props.row.secretKeys.includes(key)
}

/** The value on file is one the page never sees, only its mask. */
function storedSecret(key: string): boolean {
  return isSecret(key) && String(props.row.fields[key] ?? '') === REDACTED
}

const hasStoredSecret = computed(() => keys.value.some((key) => storedSecret(key)))

/** A stored secret starts empty — there is nothing to show — and every other
 * field starts with the value the server sent. */
function initialDraft(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const key of keys.value) {
    out[key] = isSecret(key) ? '' : String(props.row.fields[key] ?? '')
  }
  return out
}

const draft = ref(initialDraft())

async function save() {
  if (saving.value) return
  saving.value = true
  error.value = ''
  try {
    const raw = await forebrainApi.projectChannelsRaw(props.projectId)
    const channels: Record<string, Record<string, unknown>> = { ...raw.channels }
    const fields: Record<string, unknown> = { ...(channels[props.row.kind] ?? {}) }
    for (const key of keys.value) {
      const typed = draft.value[key] ?? ''
      if (!isSecret(key)) {
        fields[key] = typed
        continue
      }
      // An untouched secret travels back as the mask, which the server reads as
      // "keep the stored value"; a field with nothing stored is cleared.
      fields[key] = typed !== '' ? typed : storedSecret(key) ? REDACTED : ''
    }
    fields.enabled = props.row.enabled
    channels[props.row.kind] = fields
    await forebrainApi.saveProjectChannels(props.projectId, channels)
    emit('saved')
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}
</script>
