<template>
  <div class="w-full">
    <PageTopRow :title="t('channels.title')" :description="t('channels.description')">
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="saving || !dirty"
          data-testid="channels-save"
          @click="save"
        >
          {{ t('common.save') }}
        </button>
      </template>
    </PageTopRow>

    <p class="mb-3 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('channels.secretNotice') }}</p>
    <p v-if="error" class="mb-4 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="mb-4 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>

    <div v-if="loading && !kinds.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <ul v-else class="space-y-2">
      <li
        v-for="kind in kinds"
        :key="kind"
        class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3"
      >
        <div class="flex items-center justify-between gap-3">
          <span class="font-mono text-[13px] text-[var(--forebrain-text)]">{{ kind }}</span>
          <label class="flex items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
            {{ t('channels.enabled') }}
            <SwitchComponent :model-value="isEnabled(kind)" @update:model-value="setEnabled(kind, $event)" />
          </label>
        </div>
        <div v-if="isEnabled(kind)" class="mt-2 grid gap-2 sm:grid-cols-2">
          <label v-for="field in fieldsOf(kind)" :key="field" class="block">
            <span class="mb-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ field }}</span>
            <input
              class="forebrain-field w-full font-mono"
              :value="fieldValue(kind, field)"
              @input="setField(kind, field, ($event.target as HTMLInputElement).value)"
            />
          </label>
        </div>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
/**
 * The delivery channels of one project. A channel binds an external account to
 * this project's conversations, so what is edited here belongs to the project
 * named in the breadcrumb and to no other: the same bot cannot be configured
 * for two projects.
 *
 * Secrets come back masked. Leaving a masked field untouched keeps the value
 * already stored; typing over it replaces that value.
 */
import { onMounted, ref } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

type ChannelMap = Record<string, Record<string, unknown>>

const props = defineProps<{ project: unknown; projectId: string }>()

const { t } = useI18n()
const channels = ref<ChannelMap>({})
const kinds = ref<string[]>([])
const loading = ref(false)
const saving = ref(false)
const dirty = ref(false)
const error = ref('')
const notice = ref('')

function fieldsOf(kind: string): string[] {
  return Object.keys(channels.value[kind] ?? {}).filter((field) => field !== 'enabled').sort()
}

function isEnabled(kind: string): boolean {
  return Boolean((channels.value[kind] ?? {}).enabled)
}

function setEnabled(kind: string, value: boolean) {
  channels.value = { ...channels.value, [kind]: { ...(channels.value[kind] ?? {}), enabled: value } }
  dirty.value = true
}

function fieldValue(kind: string, field: string): string {
  const raw = (channels.value[kind] ?? {})[field]
  return raw == null ? '' : String(raw)
}

function setField(kind: string, field: string, value: string) {
  channels.value = { ...channels.value, [kind]: { ...(channels.value[kind] ?? {}), [field]: value } }
  dirty.value = true
}

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.projectChannels(props.projectId)
    channels.value = (res.channels ?? {}) as ChannelMap
    kinds.value = Array.isArray(res.kinds) && res.kinds.length ? res.kinds : Object.keys(channels.value).sort()
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
    await forebrainApi.saveProjectChannels(props.projectId, channels.value)
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
