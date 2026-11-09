<template>
  <span class="modelsig" :class="{ 'modelsig--bare': bare }">
    <b class="modelsig__name">
      <span class="modelsig__model">{{ model }}</span>
      <small v-if="providerName && !hideProvider" class="modelsig__prov">{{ providerName }}</small>
      <slot />
    </b>
    <span v-if="hasEndpoint" class="modelsig__addr">
      {{ shownBase }}<em v-if="urlVerbatim" class="forebrain-path forebrain-path--na">{{ t('models.customProtocol') }}</em>
      <em v-else-if="apiPath" class="forebrain-path">{{ apiPath }}</em>
    </span>
  </span>
</template>

<script setup lang="ts">
/**
 * How a model is written, everywhere (plan 266): the name with its provider on
 * the first line, the endpoint its requests go to — base URL then the API path
 * — on the second. Every picker and every table shows the same four facts
 * because a provider and a model alone cannot tell two configured rows apart:
 * the same model may be configured twice against two gateways.
 *
 * `bare` drops the scheme for the narrow dropdowns (the main-agent menu's own
 * layout), `hideProvider` is for a list already grouped by provider, and a row
 * with no path (a login-backed model) or with 「自定义协议」 draws no path block.
 */
import { computed } from 'vue'

import { useI18n } from '@/locales'

const props = withDefaults(
  defineProps<{
    model: string
    providerName?: string
    baseUrl?: string
    apiPath?: string
    /** Drop the scheme: `api.deepseek.com` instead of `https://api.deepseek.com`. */
    bare?: boolean
    /** The list is already grouped by provider, so saying it again is noise. */
    hideProvider?: boolean
    /** The address is the whole request address: no path is appended. */
    urlVerbatim?: boolean
  }>(),
  { providerName: '', baseUrl: '', apiPath: '', bare: false, hideProvider: false, urlVerbatim: false },
)

const { t } = useI18n()

const shownBase = computed(() => (props.bare ? String(props.baseUrl ?? '').replace(/^https?:\/\//, '') : props.baseUrl))

/** Whether the second line has anything to say at all. */
const hasEndpoint = computed(() => Boolean(props.baseUrl) || Boolean(props.urlVerbatim))
</script>

<style scoped>
.modelsig {
  display: block;
  min-width: 0;
}
.modelsig__name {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
  white-space: nowrap;
}
.modelsig__prov {
  font-size: 11.5px;
  font-weight: 400;
  color: var(--forebrain-muted-text);
}
.modelsig__addr {
  display: block;
  margin-top: 1px;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* The path block is `.forebrain-path` (main.css): the table, the pickers and
   the drawer all draw one, so they must draw the same one. */
.modelsig__addr em {
  margin-left: 1px;
  font-style: normal;
}
.modelsig--bare .modelsig__name {
  font-size: 12.5px;
}
</style>
