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
    <!-- The failed turn is the one thing an operator most often needs the raw
         log for, so the card carries the way there (plan 288): the log page,
         already narrowed to this conversation and to this turn's window. -->
    <button
      v-if="session"
      v-tip:bottom="t('logs.viewLogsHint')"
      type="button"
      class="err-logs"
      data-testid="run-error-logs"
      @click="openLogs"
    >
      <List class="err-logs-ic" aria-hidden="true" />{{ t('logs.viewLogs') }}
    </button>
  </div>
</template>

<script setup lang="ts">
/**
 * What ended a run in failure, drawn inside its turn — the last thing the turn
 * says, above its worked line — the way the terminal draws it. The classified
 * provider facts render in the viewer's language, following a switch made
 * while the error is on screen; the runtime's sentence covers the rest.
 *
 * `session` is the conversation the card is drawn in. With it the card offers
 * 「查看日志」: the settings page's log tab, narrowed to that conversation and
 * to the turn's own window. Without it — a subagent's transcript, the workshop —
 * there is no conversation to narrow to, so the card keeps just its words.
 */
import { computed } from 'vue'
import { CircleAlert, List } from 'lucide-vue-next'
import { formatProviderError, type ProviderErrorDetail } from '@/lib/providerError'
import { useSurfaceNavigation } from '@/composables/provideSurfaceNavigation'
import { useI18n } from '@/locales'

const props = defineProps<{
  text: string
  detail?: ProviderErrorDetail
  /** The conversation this turn ran in; absent outside a conversation page. */
  session?: string
  /** When the turn failed, in milliseconds; absent falls back to now. */
  at?: number
  /** When the turn started, RFC3339; absent falls back to 15 minutes before the failure. */
  startedAt?: string
}>()

const { locale, t } = useI18n()
const navigation = useSurfaceNavigation()

// The classified fact is a second line under the runtime's own sentence, not a
// replacement for it: the reader gets both what happened and what it means.
const shown = computed(() => (props.detail ? formatProviderError(props.detail, locale.value) : null) || '')

/**
 * The window the log page opens on: the turn, from a minute before it started
 * to a minute after it failed. A turn whose start was never recorded gets the
 * quarter of an hour before the failure, which is what a reader looks at anyway.
 */
const window = computed(() => {
  const failed = props.at && Number.isFinite(props.at) ? props.at : Date.now()
  const started = props.startedAt ? Date.parse(props.startedAt) : Number.NaN
  const from = Number.isNaN(started) ? failed - 15 * 60_000 : started - 60_000
  return { from, to: failed + 60_000 }
})

function openLogs(): void {
  navigation.openSettingsPage('logs', {
    session: props.session ?? '',
    from: new Date(window.value.from).toISOString(),
    to: new Date(window.value.to).toISOString(),
  })
}
</script>

<style scoped>
.err-logs {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  align-self: flex-start;
  flex: none;
  margin-left: auto;
  height: 28px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
  font-size: 12.5px;
  font-weight: 500;
  color: var(--forebrain-text);
  white-space: nowrap;
}
.err-logs:hover {
  background: var(--forebrain-bg-alt);
}
/* The block's own rule colours every lucide mark in it red; the button's mark
   is a neutral one, so it is named by the button. */
.err-logs .err-logs-ic {
  width: 13px;
  height: 13px;
  margin-top: 0;
  color: var(--forebrain-text-2);
}
</style>
