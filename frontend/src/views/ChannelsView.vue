<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('channels.title') }}</h1>
            <ScopeBadge type="agent" :label="t('scope.agent')" />
          </div>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('channels.description') }}</p>
        </div>
        <div class="flex gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving || !dirty" @click="save">{{ t('common.save') }}</button>
        </div>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="mb-4 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>
      <p class="mb-3 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('channels.secretNotice') }}</p>

      <div v-if="loading && !entries.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else class="space-y-2">
        <li v-for="entry in entries" :key="entry.key" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3">
          <div class="flex items-center justify-between gap-3">
            <span class="font-mono text-[13px] text-[var(--forebrain-text)]">{{ entry.key }}</span>
            <label class="flex items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
              {{ t('channels.enabled') }}
              <SwitchComponent :model-value="isEnabled(entry.key)" @update:model-value="setEnabled(entry.key, $event)" />
            </label>
          </div>
          <div class="mt-2 grid gap-2 sm:grid-cols-2">
            <label v-for="field in entry.fields" :key="field" class="block">
              <span class="mb-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ field }}</span>
              <input
                class="forebrain-field w-full"
                :class="{ 'font-mono': true }"
                :value="fieldValue(entry.key, field)"
                @input="setField(entry.key, field, ($event.target as HTMLInputElement).value)"
              />
            </label>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The delivery channels of the current primary agent. A channel binds an
 * external account to one tenant's sessions, so what is edited here belongs to
 * the agent named in the rail and to no other.
 *
 * Secrets come back masked. Leaving a masked field untouched keeps the value
 * already on disk; typing over it replaces that value.
 */
import { computed, onMounted, ref } from 'vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

type ChannelMap = Record<string, Record<string, unknown>>

const { t } = useI18n()
const channels = ref<ChannelMap>({})
const loading = ref(false)
const saving = ref(false)
const dirty = ref(false)
const error = ref('')
const notice = ref('')

const entries = computed(() =>
  Object.keys(channels.value)
    .sort()
    .map((key) => ({
      key,
      fields: Object.keys(channels.value[key] ?? {}).filter((field) => field !== 'enabled').sort(),
    })),
)

function isEnabled(key: string): boolean {
  return Boolean((channels.value[key] ?? {}).enabled)
}

function setEnabled(key: string, value: boolean) {
  channels.value = { ...channels.value, [key]: { ...(channels.value[key] ?? {}), enabled: value } }
  dirty.value = true
}

function fieldValue(key: string, field: string): string {
  const raw = (channels.value[key] ?? {})[field]
  return raw == null ? '' : String(raw)
}

function setField(key: string, field: string, value: string) {
  channels.value = { ...channels.value, [key]: { ...(channels.value[key] ?? {}), [field]: value } }
  dirty.value = true
}

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.channels()
    channels.value = (res.channels ?? {}) as ChannelMap
    dirty.value = false
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.saveChannels(channels.value)
    notice.value = t('channels.saved')
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
