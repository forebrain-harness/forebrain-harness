<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-5xl">
      <PageTopRow :title="t('memories.title')" :description="t('memories.filesDescription')" />
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.filesHint') }}</p>

      <div class="mt-4">
        <MemoryFilesPanel scope="global" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import PageTopRow from '@/components/common/PageTopRow.vue'
import MemoryFilesPanel from '@/components/memory/MemoryFilesPanel.vue'
import { useI18n } from '@/locales'

/**
 * The primary agent's memory files: the cross-project scope every session of
 * this tenant may recall. The memory on/off switches live in settings; this
 * page is the files themselves.
 */
const { t } = useI18n()
</script>
