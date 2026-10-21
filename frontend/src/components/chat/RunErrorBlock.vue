<template>
  <!-- The prototype's .err-block (:771): a left red rule, the mark, and the two
       sentences — the runtime's, then the classified one in the muted second
       line. The body keeps the reader's own text colour: a paragraph of red is
       harder to read than the error it reports. -->
  <div class="forebrain-err-block" role="alert" data-testid="run-error">
    <CircleAlert aria-hidden="true" />
    <div>
      {{ text }}
      <span v-if="shown" class="forebrain-err-s">{{ shown }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * What ended a run in failure, drawn inside its turn — the last thing the turn
 * says, above its worked line — the way the terminal draws it. The classified
 * provider facts render in the viewer's language, following a switch made
 * while the error is on screen; the runtime's sentence covers the rest.
 */
import { computed } from 'vue'
import { CircleAlert } from 'lucide-vue-next'
import { formatProviderError, type ProviderErrorDetail } from '@/lib/providerError'
import { useI18n } from '@/locales'

const props = defineProps<{ text: string; detail?: ProviderErrorDetail }>()

const { locale } = useI18n()
// The classified fact is a second line under the runtime's own sentence, not a
// replacement for it: the reader gets both what happened and what it means.
const shown = computed(() => (props.detail ? formatProviderError(props.detail, locale.value) : null) || '')
</script>
