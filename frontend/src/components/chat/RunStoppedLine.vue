<template>
  <div
    class="flex items-center gap-1.5 text-[11.5px] text-[var(--forebrain-muted-text)]"
    role="note"
    data-testid="run-stopped-line"
  >
    <span class="h-px w-3 flex-none bg-[var(--forebrain-divider-strong)]" />
    <Ban class="size-[13px] flex-none text-[var(--forebrain-muted-text)]" aria-hidden="true" />
    <span class="whitespace-nowrap">{{ t('chat.subagentCancelled') }}</span>
    <span class="h-px flex-1 bg-[var(--forebrain-divider-strong)]" />
  </div>
</template>

<script setup lang="ts">
/**
 * How an execution the user stopped ended, in the place it stopped. A stop is
 * not a failure: it is drawn with the muted ban the card row and the drawer's
 * own tab already use for the same agent, never the red account a failure gets
 * — and it is the one place that says so. Ruled like the worked line that
 * closes the run, because both mark where a run ended.
 */
import { Ban } from 'lucide-vue-next'
import { useI18n } from '@/locales'

const { t } = useI18n()
</script>
