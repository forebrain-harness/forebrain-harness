<template>
  <div class="space-y-3" data-testid="schedule-builder">
    <div class="grid gap-2 sm:grid-cols-2">
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.mode') }}</span>
        <select v-model="mode" class="forebrain-field h-9" data-testid="schedule-mode">
          <option v-for="item in modes" :key="item.key" :value="item.key">{{ item.label }}</option>
        </select>
      </label>

      <template v-if="mode === 'once-at'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.pickDateTime') }}</span>
          <input v-model="onceAt" type="datetime-local" class="forebrain-field h-9" data-testid="schedule-once-at" />
        </label>
      </template>

      <template v-else-if="mode === 'once-in'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.after') }}</span>
          <div class="flex gap-2">
            <input v-model.number="inCount" type="number" min="1" class="forebrain-field h-9 w-24" data-testid="schedule-in-count" />
            <select v-model="inUnit" class="forebrain-field h-9 flex-1" data-testid="schedule-in-unit">
              <option value="m">{{ t('cron.unitMinutes') }}</option>
              <option value="h">{{ t('cron.unitHours') }}</option>
            </select>
          </div>
        </label>
      </template>

      <template v-else-if="mode === 'every'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.everyLabel') }}</span>
          <div class="flex gap-2">
            <input v-model.number="everyCount" type="number" min="1" class="forebrain-field h-9 w-24" data-testid="schedule-every-count" />
            <select v-model="everyUnit" class="forebrain-field h-9 flex-1" data-testid="schedule-every-unit">
              <option value="m">{{ t('cron.unitMinutes') }}</option>
              <option value="h">{{ t('cron.unitHours') }}</option>
              <option value="d">{{ t('cron.unitDays') }}</option>
            </select>
          </div>
        </label>
      </template>

      <template v-else-if="mode === 'daily' || mode === 'weekdays' || mode === 'weekly'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.atTime') }}</span>
          <input v-model="clockTime" type="time" class="forebrain-field h-9" data-testid="schedule-time" />
        </label>
      </template>

      <template v-else-if="mode === 'cron'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.cronLabel') }}</span>
          <input v-model="cronText" type="text" class="forebrain-field h-9 font-mono" data-testid="schedule-cron" :placeholder="t('cron.cronPlaceholder')" />
        </label>
      </template>
    </div>

    <div v-if="mode === 'weekly'" class="flex flex-wrap gap-2" data-testid="schedule-weekdays">
      <label
        v-for="day in weekdays"
        :key="day.value"
        class="flex cursor-pointer items-center gap-1.5 rounded-xl border px-3 py-1.5 text-[12px]"
        :class="pickedDays.has(day.value)
          ? 'border-[var(--forebrain-brand-border-strong)] bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]'
          : 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'"
      >
        <input
          type="checkbox"
          class="h-3.5 w-3.5 rounded border-[var(--forebrain-divider-strong)] text-[var(--forebrain-brand-1)]"
          :checked="pickedDays.has(day.value)"
          @change="toggleDay(day.value, ($event.target as HTMLInputElement).checked)"
        />
        {{ day.label }}
      </label>
    </div>

    <div class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2" data-testid="schedule-preview">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px]">
        <span class="text-[var(--forebrain-text-2)]">{{ t('cron.willSaveAs') }}</span>
        <code class="font-mono text-[var(--forebrain-text)]">{{ expression || '—' }}</code>
      </div>
      <div v-if="previewing" class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="preview && preview.valid" class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">
        {{ t('cron.nextRuns') }}:
        <span v-for="(at, index) in preview.next" :key="at" class="font-mono text-[var(--forebrain-text)]">
          {{ formatPreviewTime(at) }}{{ index < (preview.next?.length ?? 0) - 1 ? ' · ' : '' }}
        </span>
      </div>
      <div v-else-if="preview && !preview.valid" class="mt-1 text-[12px] text-[var(--forebrain-danger)]" data-testid="schedule-error">{{ preview.error }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import forebrainApi, { getErrorMessage, type CronPreviewResponse } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The schedule builder turns one of seven modes into a schedule expression and
 * asks the engine (through the preview endpoint) what it means. The user never
 * writes an expression by hand — except in the cron mode, which exists for the
 * ones who can.
 */
const props = defineProps<{ modelValue: string }>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  /** The builder's own validity: a parent can only save a valid schedule. */
  validated: [valid: boolean]
}>()

const { t, locale } = useI18n()

type ModeKey = 'once-at' | 'once-in' | 'every' | 'daily' | 'weekdays' | 'weekly' | 'cron'

const modes = computed(() => [
  { key: 'once-at' as ModeKey, label: t('cron.modeOnceAt') },
  { key: 'once-in' as ModeKey, label: t('cron.modeOnceIn') },
  { key: 'every' as ModeKey, label: t('cron.modeEvery') },
  { key: 'daily' as ModeKey, label: t('cron.modeDaily') },
  { key: 'weekdays' as ModeKey, label: t('cron.modeWeekdays') },
  { key: 'weekly' as ModeKey, label: t('cron.modeWeekly') },
  { key: 'cron' as ModeKey, label: t('cron.modeCron') },
])

const mode = ref<ModeKey>('every')
const onceAt = ref('')
const inCount = ref(30)
const inUnit = ref<'m' | 'h'>('m')
const everyCount = ref(1)
const everyUnit = ref<'m' | 'h' | 'd'>('h')
const clockTime = ref('09:00')
const cronText = ref(props.modelValue ?? '')
const pickedDays = ref(new Set<number>([1]))

const weekdays = computed(() => [
  { value: 1, label: t('cron.dayMon') },
  { value: 2, label: t('cron.dayTue') },
  { value: 3, label: t('cron.dayWed') },
  { value: 4, label: t('cron.dayThu') },
  { value: 5, label: t('cron.dayFri') },
  { value: 6, label: t('cron.daySat') },
  { value: 0, label: t('cron.daySun') },
])

function toggleDay(value: number, checked: boolean) {
  const next = new Set(pickedDays.value)
  if (checked) {
    next.add(value)
  } else {
    next.delete(value)
  }
  pickedDays.value = next
}

/** "07:30" -> "7:30am"; "14:00" -> "2pm" — the engine's plain clock spelling. */
function clock12(hhmm: string): string {
  const [rawHour, rawMinute] = hhmm.split(':')
  const hour = Number(rawHour)
  const minute = rawMinute === '00' ? '' : `:${rawMinute}`
  if (hour === 0) return `12${minute}am`
  if (hour === 12) return `12${minute}pm`
  if (hour > 12) return `${hour - 12}${minute}pm`
  return `${hour}${minute}am`
}

const expression = computed(() => {
  switch (mode.value) {
    case 'once-at':
      return onceAt.value.trim()
    case 'once-in': {
      if (!Number.isFinite(inCount.value) || inCount.value <= 0) return ''
      return `in ${inCount.value}${inUnit.value}`
    }
    case 'every': {
      if (!Number.isFinite(everyCount.value) || everyCount.value <= 0) return ''
      return `every ${everyCount.value}${everyUnit.value}`
    }
    case 'daily':
      return clockTime.value ? `daily at ${clock12(clockTime.value)}` : ''
    case 'weekdays':
      return clockTime.value ? `weekdays at ${clock12(clockTime.value)}` : ''
    case 'weekly': {
      if (!pickedDays.value.size || !clockTime.value) return ''
      const clock = clock12(clockTime.value)
      const days = Array.from(pickedDays.value).sort((a, b) => a - b)
      // The engine's plain form takes one weekday; more than one is a cron
      // dow list, generated here because the user picked them, not written.
      if (days.length === 1) {
        const name = ['sunday', 'monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday'][days[0]]
        return `every ${name} ${clock}`
      }
      const [rawHour, rawMinute] = clockTime.value.split(':')
      return `${Number(rawMinute)} ${Number(rawHour)} * * ${days.join(',')}`
    }
    case 'cron':
      return cronText.value.trim()
    default:
      return ''
  }
})

const preview = ref<CronPreviewResponse | null>(null)
const previewing = ref(false)

let previewDebounce: ReturnType<typeof setTimeout> | null = null
watch(expression, (value) => {
  emit('update:modelValue', value)
  if (previewDebounce) clearTimeout(previewDebounce)
  preview.value = null
  if (!value) {
    emit('validated', false)
    return
  }
  previewDebounce = setTimeout(async () => {
    previewing.value = true
    try {
      const out = await forebrainApi.cronPreview(value)
      preview.value = out
      emit('validated', out.valid)
    } catch (e: unknown) {
      preview.value = { raw: value, valid: false, error: getErrorMessage(e) }
      emit('validated', false)
    } finally {
      previewing.value = false
    }
  }, 400)
}, { immediate: true })

function formatPreviewTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}
</script>
