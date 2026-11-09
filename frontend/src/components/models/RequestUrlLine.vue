<template>
  <div class="reqline" data-testid="models-request-line">
    <!-- 「自定义协议」 sends the address as given, so the line says so instead of
         printing a path nothing appends. -->
    <template v-if="verbatim">
      {{ t('models.requestVerbatim') }}<code>{{ baseUrl || t('models.requestFillBase') }}</code>
    </template>
    <template v-else>
      {{ t('models.requestActual') }}<code>{{ baseUrl || t('models.requestFillBase') }}<em v-if="shownPath">{{ shownPath }}</em></code>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The address a request really goes to (plan 266): the base URL with the
 * protocol's path highlighted after it, updating as either is typed. It is the
 * one place the two are shown joined, so a reader never has to guess how the
 * engine composes them — and a 「自定义协议」 row says the address is the whole
 * request rather than pretending a path gets appended.
 */
import { computed } from 'vue'

import { useI18n } from '@/locales'

const props = defineProps<{
  baseUrl: string
  /** The protocol's path; empty while none is chosen. */
  apiPath: string
  /** The address is sent as it is (`url_verbatim`). */
  verbatim?: boolean
  /** What to say when no protocol is chosen yet. */
  fallback?: string
}>()

const { t } = useI18n()

const shownPath = computed(() => props.apiPath || props.fallback || t('models.requestPickProtocol'))
</script>

<style scoped>
.reqline {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  margin-top: 8px;
  padding: 7px 10px;
  border-radius: 6px;
  background: var(--forebrain-bg-alt);
  font-size: 12px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
}
.reqline code {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-text);
}
.reqline code em {
  font-style: normal;
  margin-left: 1px;
  padding: 0 3px;
  border-radius: 3px;
  background: var(--path-bg);
  color: var(--path-ink);
}
</style>
