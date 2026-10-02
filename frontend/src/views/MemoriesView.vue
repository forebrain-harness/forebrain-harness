<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-5xl">
      <div class="flex items-center gap-2">
        <h1 class="text-xl font-medium text-[var(--forebrain-text)]">{{ t('memories.title') }}</h1>
        <span class="scope-badge">{{ t('scope.agent') }}</span>
      </div>
      <p class="mt-1 text-[13px] text-[var(--forebrain-text-2)]">{{ t('memories.filesDescription') }}</p>
      <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.filesHint') }}</p>

      <div class="mt-4">
        <MemoryFilesPanel scope="global" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import MemoryFilesPanel from '@/components/memory/MemoryFilesPanel.vue'
import { useI18n } from '@/locales'

/**
 * The primary agent's memory files: the cross-project scope every session of
 * this tenant may recall. The memory on/off switches live in settings; this
 * page is the files themselves.
 */
const { t } = useI18n()
</script>
