<template>
  <div class="space-y-3" data-testid="schedule-builder">
    <div class="grid gap-2 sm:grid-cols-2">
      <!-- The label is its own element rather than a wrapper: the control is the
           library's Select, a button, and a button inside a `label` would answer
           one click with two. -->
      <div class="block">
        <Label for="schedule-mode" class="mb-1 block text-[12px] font-normal leading-[normal] text-[var(--forebrain-text-2)]">{{ t('cron.mode') }}</Label>
        <Select :model-value="mode" @update:model-value="(value) => (mode = String(value) as ModeKey)">
          <SelectTrigger id="schedule-mode" class="forebrain-field h-9" data-testid="schedule-mode">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="item in modes" :key="item.key" :value="item.key">{{ item.label }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <template v-if="mode === 'once-at'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.pickDateTime') }}</span>
          <!-- The library's Input hands `type` down to the native element and
               its own default is text: dropping this line would quietly turn the
               picker into a free-text box. -->
          <Input v-model="onceAt" type="datetime-local" class="forebrain-field h-9" data-testid="schedule-once-at" />
        </label>
      </template>

      <template v-else-if="mode === 'once-in'">
        <div class="block">
          <Label for="schedule-in-count" class="mb-1 block text-[12px] font-normal leading-[normal] text-[var(--forebrain-text-2)]">{{ t('cron.after') }}</Label>
          <div class="flex gap-2">
            <!-- A number is a number: the library's Input does not implement
                 `v-model`'s `.number` modifier, so the conversion is here and an
                 emptied box falls back to the field's own floor. -->
            <Input
              :model-value="String(inCount)"
              type="number"
              min="1"
              class="forebrain-field h-9 w-24"
              data-testid="schedule-in-count"
              @update:model-value="(value) => (inCount = Number(value) || 1)"
            />
            <!-- The unit belongs to the count above it, so the label names the
                 pair and this trigger reuses that name as its own. -->
            <Select :model-value="inUnit" @update:model-value="(value) => (inUnit = String(value) as 'm' | 'h')">
              <SelectTrigger class="forebrain-field h-9 flex-1" :aria-label="t('cron.after')" data-testid="schedule-in-unit">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="m">{{ t('cron.unitMinutes') }}</SelectItem>
                <SelectItem value="h">{{ t('cron.unitHours') }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      </template>

      <template v-else-if="mode === 'every'">
        <div class="block">
          <Label for="schedule-every-count" class="mb-1 block text-[12px] font-normal leading-[normal] text-[var(--forebrain-text-2)]">{{ t('cron.everyLabel') }}</Label>
          <div class="flex gap-2">
            <Input
              :model-value="String(everyCount)"
              type="number"
              min="1"
              class="forebrain-field h-9 w-24"
              data-testid="schedule-every-count"
              @update:model-value="(value) => (everyCount = Number(value) || 1)"
            />
            <Select :model-value="everyUnit" @update:model-value="(value) => (everyUnit = String(value) as 'm' | 'h' | 'd')">
              <SelectTrigger class="forebrain-field h-9 flex-1" :aria-label="t('cron.everyLabel')" data-testid="schedule-every-unit">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="m">{{ t('cron.unitMinutes') }}</SelectItem>
                <SelectItem value="h">{{ t('cron.unitHours') }}</SelectItem>
                <SelectItem value="d">{{ t('cron.unitDays') }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      </template>

      <template v-else-if="mode === 'daily' || mode === 'weekdays' || mode === 'weekly'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.atTime') }}</span>
          <Input v-model="clockTime" type="time" class="forebrain-field h-9" data-testid="schedule-time" />
        </label>
      </template>

      <template v-else-if="mode === 'cron'">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.cronLabel') }}</span>
          <Input v-model="cronText" type="text" class="forebrain-field h-9 font-mono" data-testid="schedule-cron" :placeholder="t('cron.cronPlaceholder')" />
        </label>
      </template>
    </div>

    <div v-if="mode === 'weekly'" class="flex flex-wrap gap-2" data-testid="schedule-weekdays">
      <!-- A chip is a box with its day's name beside it. The box is the library's
           Checkbox, a button, so the name is a Label bound to it rather than a
           wrapper around it: a `label` around a button toggles it twice. -->
      <div
        v-for="day in weekdays"
        :key="day.value"
        class="flex items-center gap-1.5 rounded-md border px-3 py-1.5 text-[12px]"
        :class="pickedDays.has(day.value)
          ? 'border-[var(--forebrain-brand-border-strong)] bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]'
          : 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'"
      >
        <Checkbox
          :id="`schedule-day-${day.value}`"
          :model-value="pickedDays.has(day.value)"
          @update:model-value="(checked) => toggleDay(day.value, checked === true)"
        />
        <Label :for="`schedule-day-${day.value}`" class="cursor-pointer font-normal text-[12px] leading-[normal]">{{ day.label }}</Label>
      </div>
    </div>

    <div class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2" data-testid="schedule-preview">
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
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Checkbox } from '@repo/shadcn-vue/components/ui/checkbox'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'

import forebrainApi, { getErrorMessage, type CronPreviewResponse } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The schedule builder turns one of seven modes into a schedule expression and
 * asks the engine (through the preview endpoint) what it means. The user never
 * writes an expression by hand — except in the cron mode, which exists for the
 * ones who can.
 */
const props = defineProps<{ modelValue: string; projectId: string }>()

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

// A stored schedule opens in the cron mode, which shows the expression as-is:
// any other starting mode would rebuild it from that mode's defaults and
// replace the job's schedule before the user touched anything.
const mode = ref<ModeKey>(props.modelValue?.trim() ? 'cron' : 'every')
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
      const out = await forebrainApi.cronPreview(props.projectId, value)
      // An answer for an expression the user has since changed says nothing
      // about the one on screen; the newer request's answer will.
      if (expression.value !== value) return
      preview.value = out
      emit('validated', out.valid)
    } catch (e: unknown) {
      if (expression.value !== value) return
      preview.value = { raw: value, valid: false, error: getErrorMessage(e) }
      emit('validated', false)
    } finally {
      if (expression.value === value) previewing.value = false
    }
  }, 400)
}, { immediate: true })

// The debounce is the builder's own: a preview asked for by a builder that is
// already gone answers nobody, so the timer leaves with the component.
onBeforeUnmount(() => {
  if (previewDebounce) clearTimeout(previewDebounce)
  previewDebounce = null
})

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
