<script setup lang="ts">
/**
 * The notice above the composer while the conversation waits for a usage limit
 * to reset and then continues by itself. The terminal shows the same wait
 * under its composer; both read it from the runtime, so cancelling it on either
 * surface cancels it on both.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { TriangleAlert } from 'lucide-vue-next'
import { formatAutoContinueNotice, type AutoContinueState } from '@/lib/autoContinue'
import { useI18n } from '@/locales'

const props = defineProps<{ state: AutoContinueState | null }>()
const emit = defineEmits<{ (e: 'cancel'): void }>()

const { t, locale } = useI18n()

// The notice names a clock time, which only needs re-reading when the day
// turns over ("2:10 AM" becomes "Wed 2:10 AM"), so a minute tick is plenty.
const now = ref(new Date())
let timer: ReturnType<typeof setInterval> | null = null
watch(() => props.state, (state) => {
  if (state && !timer) {
    now.value = new Date()
    timer = setInterval(() => { now.value = new Date() }, 60_000)
  } else if (!state && timer) {
    clearInterval(timer)
    timer = null
  }
}, { immediate: true })
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})

const notice = computed(() => (props.state ? formatAutoContinueNotice(props.state, locale.value, now.value) : ''))
</script>

<template>
  <div
    v-if="state"
    class="auto-continue-banner mb-2 flex items-center gap-2 rounded-xl border px-3 py-2 text-[13px]"
    role="status"
    aria-live="polite"
    data-testid="auto-continue-banner"
  >
    <TriangleAlert class="size-4 shrink-0" aria-hidden="true" />
    <span class="min-w-0 flex-1">{{ notice }}</span>
    <button
      type="button"
      class="forebrain-btn forebrain-btn-ghost h-7 shrink-0 px-3 text-[12px]"
      :title="t('autoContinue.cancelHint')"
      data-testid="auto-continue-cancel"
      @click="emit('cancel')"
    >
      {{ t('autoContinue.cancel') }}
    </button>
  </div>
</template>

<style scoped>
.auto-continue-banner {
  border-color: rgba(216, 160, 70, 0.38);
  background: rgba(216, 160, 70, 0.12);
  color: rgb(176, 120, 30);
}

:global(.dark) .auto-continue-banner {
  color: rgb(226, 176, 96);
}
</style>
