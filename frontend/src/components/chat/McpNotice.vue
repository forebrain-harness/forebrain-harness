<template>
  <!-- One notice at the head of the thread for every MCP server that failed to
       start: MCP starts once per conversation, so the fact belongs where the
       conversation begins, not above the composer and not once per turn. The
       servers' own words stay behind "查看详情" — the line says what it means
       for this conversation, the detail says what actually happened. -->
  <div v-if="failures.length" class="forebrain-err-block forebrain-err-block--warn" data-testid="mcp-notice" role="status">
    <PlugZap class="size-3.5" aria-hidden="true" />
    <div class="min-w-0">
      <span>{{ t('chat.mcpNoticeLine', { names: failureNames }) }}</span>
      <span class="forebrain-err-s">
        {{ t('chat.mcpNoticeFix') }}
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto px-0.5 py-0 text-[12px]" data-testid="mcp-notice-toggle" @click="open = !open">
          {{ open ? t('chat.mcpNoticeHide') : t('chat.mcpNoticeShow') }}
        </Button>
      </span>
      <code
        v-for="row in failures"
        v-show="open"
        :key="row.name"
        class="mt-1.5 block whitespace-pre-wrap break-words font-mono text-[11.5px] text-[var(--forebrain-text-2)]"
        data-testid="mcp-notice-detail"
      >{{ row.name }}: {{ row.error }}</code>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { PlugZap } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { useI18n } from '@/locales'

const props = defineProps<{
  /** The servers that failed to start, each with the error text the server
   *  itself produced. */
  failures: { name: string; error: string }[]
}>()

const { t } = useI18n()
const open = ref(false)

const failureNames = computed(() => props.failures.map((row) => row.name).join('、'))
</script>
