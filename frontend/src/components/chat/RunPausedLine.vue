<template>
  <div
    class="flex items-center gap-1.5 text-[11.5px] text-[var(--forebrain-muted-text)]"
    role="note"
    data-testid="run-paused-line"
  >
    <span class="h-px w-3 flex-none bg-[var(--forebrain-divider-strong)]" />
    <CirclePause class="size-[13px] flex-none text-[var(--forebrain-text-2)]" aria-hidden="true" />
    <span class="whitespace-nowrap font-medium text-[var(--forebrain-text-2)]">{{ t('chat.subagentPaused') }}</span>
    <span class="h-px flex-1 bg-[var(--forebrain-divider-strong)]" />
  </div>
</template>

<script setup lang="ts">
/**
 * Where the user interrupted a subagent from its tab: the step it was on
 * stopped, and it waits for their next message. It is ruled like the worked
 * line that closes a run (RunWorkedLine), because both mark where a run
 * stopped — the terminal prints this one as "Interrupted · waiting for your
 * message".
 */
import { CirclePause } from 'lucide-vue-next'
import { useI18n } from '@/locales'

const { t } = useI18n()
</script>
