<template>
  <div
    role="alert"
    class="whitespace-pre-wrap break-words rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-surface)] px-3 py-2 text-[13px] leading-relaxed text-[var(--forebrain-danger)]"
    data-testid="run-error"
  >{{ shown }}</div>
</template>

<script setup lang="ts">
/**
 * What ended a run in failure, drawn inside its turn — the last thing the turn
 * says, above its worked line — the way the terminal draws it. The classified
 * provider facts render in the viewer's language, following a switch made
 * while the error is on screen; the runtime's sentence covers the rest.
 */
import { computed } from 'vue'
import { formatProviderError, type ProviderErrorDetail } from '@/lib/providerError'
import { useI18n } from '@/locales'

const props = defineProps<{ text: string; detail?: ProviderErrorDetail }>()

const { locale } = useI18n()
const shown = computed(() => (props.detail ? formatProviderError(props.detail, locale.value) : null) || props.text)
</script>
