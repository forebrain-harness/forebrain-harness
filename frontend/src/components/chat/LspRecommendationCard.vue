<script setup lang="ts">
/**
 * The card that asks whether to enable (or install and enable) a language
 * server the runtime just recommended. The terminal asks the same question
 * as a modal; both answers run the same decision API, and the result line
 * here is the web's copy of the transcript line the terminal shows. None of
 * it reaches the model.
 */
import { computed, onBeforeUnmount, reactive } from 'vue'
import { ServerCog, X } from 'lucide-vue-next'
import { decideLspRecommendation, getErrorMessage, lspSnapshot } from '@/lib/api'
import type { LspRecommendation, LspRecommendationChoice } from '@/lib/lspRecommendation'
import { useI18n } from '@/locales'

const props = defineProps<{ recommendation: LspRecommendation; sessionId?: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()

/** The install poll: every 2s, for at most 10 minutes (spec §10.2/§6.5). */
const INSTALL_POLL_MS = 2000
const INSTALL_POLL_DEADLINE_MS = 10 * 60 * 1000
const RESULT_LINGER_MS = 5000

type Phase = 'asking' | 'sending' | 'installing' | 'result'

const state = reactive<{ phase: Phase; error: string; result: string }>({
  phase: 'asking',
  error: '',
  result: '',
})

let closeTimer: ReturnType<typeof setTimeout> | null = null
let unmounted = false
onBeforeUnmount(() => {
  unmounted = true
  if (closeTimer) clearTimeout(closeTimer)
})

const languages = computed(() => props.recommendation.languages.join(', '))
const primaryChoice = computed<LspRecommendationChoice>(
  () => (props.recommendation.mode === 'install' ? 'install' : 'enable'),
)
const foundValue = computed(() => {
  const rec = props.recommendation
  if (rec.mode === 'install') {
    return t('lsp.recommendation.foundMissing', { command: rec.installCommand ?? '' })
  }
  return rec.version ? `${rec.binaryPath ?? ''} (${rec.version})` : (rec.binaryPath ?? '')
})

async function decide(choice: LspRecommendationChoice) {
  if (state.phase !== 'asking') return
  state.phase = 'sending'
  state.error = ''
  try {
    await decideLspRecommendation(props.recommendation.id, choice, props.sessionId)
  } catch (error) {
    state.phase = 'asking'
    state.error = getErrorMessage(error)
    return
  }
  if (choice === 'install') {
    state.phase = 'installing'
    void pollInstall()
    return
  }
  await finish(choice)
}

/** The close button is the same answer as 「No, not now」. */
function decideNotNow() {
  void decide('not_now')
}

async function finish(choice: LspRecommendationChoice) {
  let line = ''
  if (choice === 'enable') {
    line = t('lsp.recommendation.resultEnable', { name: props.recommendation.displayName, languages: languages.value })
  } else if (choice === 'never') {
    line = t('lsp.recommendation.resultNever', { id: props.recommendation.serverId })
  } else if (choice === 'disable_all') {
    line = t('lsp.recommendation.resultDisableAll')
  } else {
    // Five dismissals in a row turn recommendations off; the snapshot says
    // which line is the truth.
    line = t('lsp.recommendation.resultNotNow')
    try {
      const snap = await lspSnapshot(props.sessionId)
      if (snap.recommendationsDisabled) line = t('lsp.recommendation.resultNotNowOff')
    } catch {
      // The decision stands; only the finer line is lost.
    }
  }
  showResult(line)
}

async function pollInstall() {
  const serverId = props.recommendation.serverId
  const deadline = Date.now() + INSTALL_POLL_DEADLINE_MS
  while (!unmounted && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, INSTALL_POLL_MS))
    if (unmounted) return
    try {
      const snap = await lspSnapshot(props.sessionId)
      const server = snap.servers.find((entry) => entry.id === serverId)
      if (server && !server.installing) {
        showResult(
          server.installError
            ? t('lsp.recommendation.installFailed', { error: server.installError })
            : t('lsp.recommendation.resultInstalled', { name: props.recommendation.displayName, languages: languages.value }),
        )
        return
      }
    } catch {
      // A snapshot that cannot be read now may read next time.
    }
  }
  if (!unmounted) showResult(t('lsp.recommendation.installFailed', { error: 'timed out' }))
}

function showResult(line: string) {
  state.phase = 'result'
  state.result = line
  closeTimer = setTimeout(() => {
    if (!unmounted) emit('close')
  }, RESULT_LINGER_MS)
}
</script>

<template>
  <div
    class="lsp-recommendation-card mb-2 rounded-lg border bg-[var(--forebrain-surface)] px-3 py-2.5 text-[13px]"
    role="dialog"
    aria-live="polite"
    data-testid="lsp-rec-card"
  >
    <template v-if="state.phase !== 'result'">
      <div class="flex items-start gap-2">
        <ServerCog class="mt-0.5 size-4 shrink-0 text-[var(--forebrain-text-2)]" aria-hidden="true" />
        <div class="min-w-0 flex-1">
          <p class="font-medium text-[var(--forebrain-text)]">{{ t('lsp.recommendation.title') }}</p>
          <p class="mt-0.5 leading-relaxed text-[var(--forebrain-text-2)]">{{ t('lsp.recommendation.description') }}</p>
        </div>
        <button
          type="button"
          class="shrink-0 rounded-md p-1 text-[var(--forebrain-muted-text)] hover:text-[var(--forebrain-text)]"
          :title="t('lsp.recommendation.close')"
          data-testid="lsp-rec-close"
          @click="decideNotNow"
        >
          <X class="size-4" aria-hidden="true" />
        </button>
      </div>

      <dl class="mt-2 grid grid-cols-1 gap-x-4 gap-y-1 text-[12px] sm:grid-cols-2">
        <div>
          <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.recommendation.factServer') }}: </dt>
          <dd class="ml-1 inline text-[var(--forebrain-text-2)]">
            {{ recommendation.displayName }}<template v-if="languages"> ({{ languages }})</template>
          </dd>
        </div>
        <div>
          <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.recommendation.factFound') }}: </dt>
          <dd class="ml-1 inline font-mono break-all text-[var(--forebrain-text-2)]">{{ foundValue }}</dd>
        </div>
        <div>
          <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.recommendation.factTriggeredBy') }}: </dt>
          <dd class="ml-1 inline text-[var(--forebrain-text-2)]">
            {{ t('lsp.recommendation.valueTriggeredBy', { extension: recommendation.triggerExtension }) }}
          </dd>
        </div>
        <div>
          <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.recommendation.factRuns') }}: </dt>
          <dd class="ml-1 inline text-[var(--forebrain-text-2)]">{{ t('lsp.recommendation.valueRuns') }}</dd>
        </div>
      </dl>

      <p v-if="state.error" class="mt-2 text-[12px] text-[var(--forebrain-danger)]" data-testid="lsp-rec-error">
        {{ state.error }}
      </p>

      <div v-if="state.phase === 'installing'" class="mt-2 text-[12px] text-[var(--forebrain-muted-text)]" data-testid="lsp-rec-installing">
        {{ t('lsp.recommendation.installing') }}
      </div>

      <div v-else class="mt-2 flex flex-wrap items-center gap-2">
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary h-7 px-3 text-[12px]"
          :disabled="state.phase === 'sending'"
          :data-choice="primaryChoice"
          @click="decide(primaryChoice)"
        >
          {{ primaryChoice === 'install' ? t('lsp.recommendation.installBtn') : t('lsp.recommendation.enableBtn') }}
        </button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-7 px-3 text-[12px]"
          :disabled="state.phase === 'sending'"
          data-choice="not_now"
          @click="decide('not_now')"
        >
          {{ t('lsp.recommendation.notNow') }}
        </button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-7 px-3 text-[12px]"
          :disabled="state.phase === 'sending'"
          data-choice="never"
          @click="decide('never')"
        >
          {{ t('lsp.recommendation.never', { id: recommendation.serverId }) }}
        </button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-7 px-3 text-[12px]"
          :disabled="state.phase === 'sending'"
          data-choice="disable_all"
          @click="decide('disable_all')"
        >
          {{ t('lsp.recommendation.disableAll') }}
        </button>
      </div>
    </template>

    <p v-else class="text-[12px] leading-relaxed text-[var(--forebrain-text-2)]" data-testid="lsp-rec-result">
      {{ state.result }}
    </p>
  </div>
</template>

<style scoped>
.lsp-recommendation-card {
  border-color: var(--forebrain-divider);
}
</style>
