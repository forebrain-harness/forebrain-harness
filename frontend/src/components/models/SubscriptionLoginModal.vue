<template>
  <ForebrainModal :open="open" size="xl" :title="t('models.subscribeTitle')" @close="close">
    <p class="stepl" data-testid="models-subscribe-step">{{ stepText }}</p>

    <!-- Step 1: which subscription. Only the ones that can sign in are listed;
         a key-only plan is an endpoint in 「添加模型」 instead. -->
    <template v-if="step === 1">
      <div class="subl">
        <button
          v-for="subscription in subscriptions"
          :key="subscription.id"
          type="button"
          class="subr"
          :data-subscription="subscription.id"
          @click="pickSubscription(subscription)"
        >
          <span class="subr__t">
            <b>{{ subscription.name }}</b>
            <span>{{ describe(subscription) }}</span>
          </span>
          <BadgeComponent :variant="stateVariant(subscription.state)" :data-testid="`models-sub-state-${subscription.id}`">
            {{ stateText(subscription) }}
          </BadgeComponent>
          <ChevronRight class="size-3.5" aria-hidden="true" />
        </button>
      </div>
      <p v-if="!subscriptions.length" class="listnote">{{ t('models.subscribeEmpty') }}</p>
      <p class="listnote">{{ t('models.subscribeNote') }}</p>
    </template>

    <!-- Step 2: the flow itself, one branch per kind the backend drives. -->
    <template v-else-if="step === 2">
      <div class="auth">
        <!-- device_code: a code to type on the authorization page; the poll
             advances on its own once the person approves there. -->
        <template v-if="flow === 'device_code'">
          <p>{{ t('models.deviceHint') }}</p>
          <div class="ucode" data-testid="models-login-code">{{ userCode || t('models.deviceCodePending') }}</div>
          <div class="acts2">
            <Button
              type="button"
              class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs"
              :disabled="!url"
              data-testid="models-login-open"
              @click="openAuthorization"
            >
              <ExternalLink class="size-3.5" aria-hidden="true" />{{ t('models.deviceOpen') }}
            </Button>
            <span class="wait"><span class="spin" aria-hidden="true" />{{ t('models.deviceWaiting') }}</span>
          </div>
        </template>

        <!-- paste_callback: the page the sign-in ends on cannot reach the
             gateway's machine, so the address is pasted back. -->
        <template v-else>
          <p>{{ t('models.pasteHint') }}</p>
          <div class="acts2">
            <Button
              type="button"
              class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs"
              :disabled="!url"
              data-testid="models-login-open"
              @click="openAuthorization"
            >
              <ExternalLink class="size-3.5" aria-hidden="true" />{{ t('models.deviceOpen') }}
            </Button>
            <span class="wait"><span class="spin" aria-hidden="true" />{{ t('models.deviceWaiting') }}</span>
          </div>
          <div class="paste">
            <span>{{ t('models.pasteLabel') }}</span>
            <div class="paste__row">
              <Input
                v-model="pasteUrl"
                class="forebrain-field h-[32px] font-mono text-xs"
                placeholder="http://localhost:1455/auth/callback?code=…"
                :aria-invalid="pasteError ? 'true' : undefined"
                data-testid="models-login-paste"
              />
              <Button
                type="button"
                class="forebrain-btn h-[32px] px-3 text-xs"
                :disabled="!pasteUrl.trim()"
                data-testid="models-login-paste-submit"
                @click="submitCallback"
              >
                {{ t('models.pasteSubmit') }}
              </Button>
            </div>
            <p v-if="pasteError" class="bad" role="alert" data-testid="models-login-paste-error">{{ pasteError }}</p>
          </div>
        </template>

        <p v-if="loginError" class="bad" data-testid="models-login-error">{{ loginError }}</p>
        <p v-if="lost" class="bad" data-testid="models-login-lost">{{ t('models.loginLostHint') }}</p>
      </div>
    </template>

    <!-- Step 3: what the sign-in unlocked. -->
    <template v-else>
      <p v-if="offersError" class="bad" data-testid="models-offers-error">{{ offersError }}</p>
      <OfferPicker
        v-else
        :offers="offers"
        :chosen="chosen"
        :subagents="chosenSubagents"
        :key-choice="keyChoice"
        :key-choice-for="keyChoiceFor"
        :key-id="keyId"
        :needs-consent="needsConsent"
        @update:chosen="chosen = $event"
        @update:subagents="chosenSubagents = $event"
        @update:key-id="keyId = $event"
        @update:create-key="createKey = $event"
      />
      <p v-if="provisionError" class="bad" data-testid="models-provision-error">{{ provisionError }}</p>
    </template>

    <template #footer>
      <Button
        v-if="step === 1"
        type="button"
        variant="ghost"
        class="forebrain-btn forebrain-btn--ghost h-auto text-xs"
        data-testid="models-subscribe-close"
        @click="close"
      >
        {{ t('common.close') }}
      </Button>
      <template v-else>
        <Button
          type="button"
          variant="ghost"
          class="forebrain-btn forebrain-btn--ghost h-auto text-xs"
          data-testid="models-subscribe-prev"
          @click="back"
        >
          {{ t('models.prev') }}
        </Button>
        <Button
          v-if="step === 2 && pending"
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto text-xs"
          data-testid="models-login-retry"
          @click="restart"
        >
          {{ t('models.relogin') }}
        </Button>
        <Button
          v-else-if="step === 3"
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto text-xs"
          :disabled="!chosen.length || provisioning"
          data-testid="models-provision"
          @click="provision"
        >
          {{ provisioning ? t('common.loading') : t('models.addModels', { n: chosen.length }) }}
        </Button>
      </template>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * The one door into every subscription (plan 266): pick it, sign in to it, then
 * choose the models it unlocked. The gateway drives the flow — a device code, or
 * an address pasted back — so this modal never speaks a vendor's protocol; it
 * renders whichever kind the status reports and polls until the flow ends.
 *
 * The third step is the vendor's own listing written as rows: the same model may
 * be served by two plans, so rows are grouped by plan and every one carries its
 * endpoint. A plan that holds several keys asks which one under its own group,
 * and one whose account has none asks before creating it.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ChevronRight, ExternalLink } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import OfferPicker from '@/components/models/OfferPicker.vue'
import {
  forebrainApi,
  GatewayHttpError,
  getErrorMessage,
  type ProviderLoginStatus,
  type SubscriptionOffer,
  type SubscriptionRow,
} from '@/lib/api'
import { useI18n } from '@/locales'

/** The poll period on the gateway's own status endpoint: this only asks a local
 *  process what it has learned, so a second is often enough and cheap. */
const CONNECT_POLL_MS = 1000

const props = withDefaults(
  defineProps<{
    open: boolean
    /** A subscription to open on (the drawer's 「重新登录」); '' starts at step 1. */
    initialId?: string
    /** Known subscriptions, when the caller already read them. */
    known?: SubscriptionRow[]
  }>(),
  { initialId: '', known: () => [] },
)

const emit = defineEmits<{ close: []; saved: [] }>()

const { t } = useI18n()

const step = ref<1 | 2 | 3>(1)
const subscriptions = ref<SubscriptionRow[]>([...props.known])
const current = ref<SubscriptionRow | null>(null)
const loginId = ref('')
const url = ref('')
const userCode = ref('')
const flow = ref('')
const loginError = ref('')
const lost = ref(false)
const pending = ref(false)
const pasteUrl = ref('')
const pasteError = ref('')
const offers = ref<SubscriptionOffer[]>([])
const offersError = ref('')
const chosen = ref<number[]>([])
/** The chosen offers the agent may pick for subagents on its own (plan 283).
 *  Empty is the default: an installation only opens what the user opens. */
const chosenSubagents = ref<number[]>([])
const provisioning = ref(false)
const provisionError = ref('')
const keyChoice = ref<{ id: string; name: string; tail: string }[]>([])
const keyChoiceFor = ref('')
const keyId = ref('')
const needsConsent = ref(false)
const createKey = ref(false)

let timer: ReturnType<typeof setTimeout> | null = null

const stepText = computed(() =>
  step.value === 1 ? t('models.subscribeStep1') : step.value === 2 ? t('models.subscribeStep2') : t('models.subscribeStep3'),
)

/** What the second line under a subscription's name says: the flow it drives,
 *  in the page's words rather than the backend's enum. */
function describe(subscription: SubscriptionRow): string {
  if (subscription.flows.includes('paste_callback')) return t('models.pasteHint')
  return t('models.deviceHint')
}

function stateVariant(state: string): 'success' | 'gray' | 'warning' {
  if (state === 'usable') return 'success'
  if (state === 'dead' || state === 'unreachable') return 'warning'
  return 'gray'
}

function stateText(subscription: SubscriptionRow): string {
  return subscription.state === 'usable' ? t('models.stateUsable') : t('models.subscribeNotLoggedIn')
}

function stopPoll(): void {
  if (timer) {
    clearTimeout(timer)
    timer = null
  }
}

/** Everything a previous attempt left behind, so reopening never shows it. */
function reset(): void {
  stopPoll()
  step.value = 1
  current.value = null
  loginId.value = ''
  url.value = ''
  userCode.value = ''
  flow.value = ''
  loginError.value = ''
  lost.value = false
  pending.value = false
  pasteUrl.value = ''
  pasteError.value = ''
  offers.value = []
  offersError.value = ''
  chosen.value = []
  provisionError.value = ''
  keyChoice.value = []
  keyChoiceFor.value = ''
  keyId.value = ''
  needsConsent.value = false
  createKey.value = false
}

watch(
  () => props.open,
  (open) => {
    if (!open) {
      cancelRunning()
      return
    }
    reset()
    void loadSubscriptions()
  },
  { immediate: true },
)

async function loadSubscriptions(): Promise<void> {
  try {
    subscriptions.value = await forebrainApi.subscriptions()
  } catch (cause) {
    loginError.value = getErrorMessage(cause)
    return
  }
  const wanted = props.initialId ? subscriptions.value.find((entry) => entry.id === props.initialId) : undefined
  if (wanted) pickSubscription(wanted)
}

/** A subscription already signed in goes straight to its models: the flow would
 *  answer "complete" without a code, so asking for one would be a lie. */
function pickSubscription(subscription: SubscriptionRow): void {
  current.value = subscription
  chosen.value = []
  offers.value = []
  provisionError.value = ''
  if (subscription.state === 'usable') {
    void loadOffers()
    return
  }
  step.value = 2
  void startLogin()
}

async function startLogin(): Promise<void> {
  const subscription = current.value
  if (!subscription) return
  stopPoll()
  loginError.value = ''
  lost.value = false
  pending.value = false
  loginId.value = ''
  url.value = ''
  userCode.value = ''
  pasteUrl.value = ''
  pasteError.value = ''
  try {
    const started = await forebrainApi.startProviderLogin(subscription.id)
    if (current.value?.id !== subscription.id) return
    // A reusable sign-in on this machine is already the finished one: there is
    // nothing to poll and no code to confirm.
    if (started.status === 'complete') {
      await loadOffers()
      return
    }
    if (!started.loginId) {
      loginError.value = getErrorMessage(new Error('login started without an id'))
      pending.value = true
      return
    }
    loginId.value = started.loginId
    flow.value = subscription.flows[0] ?? ''
    pending.value = true
    poll(subscription.id, started.loginId)
  } catch (cause) {
    loginError.value =
      cause instanceof GatewayHttpError && cause.status === 409 ? t('models.loginBusy') : t('models.loginFailed')
    pending.value = true
  }
}

function poll(id: string, login: string): void {
  stopPoll()
  timer = setTimeout(() => {
    timer = null
    void pollOnce(id, login)
  }, CONNECT_POLL_MS)
}

async function pollOnce(id: string, login: string): Promise<void> {
  if (loginId.value !== login) return
  let status: ProviderLoginStatus | null
  try {
    status = await forebrainApi.providerLoginStatus(id, login)
  } catch (cause) {
    loginError.value = getErrorMessage(cause)
    return
  }
  if (loginId.value !== login) return
  if (status === null) {
    // The gateway forgot the id (it restarted, or the finished login was
    // pruned): polling again can only answer the same.
    lost.value = true
    pending.value = false
    return
  }
  if (status.flow) flow.value = status.flow
  if (status.url) url.value = status.url
  if (status.userCode) userCode.value = status.userCode
  if (status.status === 'failed') {
    loginError.value = failureText(status.error)
    pending.value = false
    return
  }
  if (status.status === 'complete') {
    await loadOffers()
    return
  }
  poll(id, login)
}

/** The sentence a failure reason becomes: the OAuth vocabulary the flows report,
 *  never the raw message, which names internals. */
function failureText(reason: string): string {
  switch (String(reason).trim().toLowerCase()) {
    case 'canceled':
      return t('models.loginCanceled')
    case 'denied':
      return t('models.loginDenied')
    case 'expired':
      return t('models.loginExpired')
    default:
      return t('models.loginFailed')
  }
}

function openAuthorization(): void {
  if (!url.value) return
  window.open(url.value, '_blank', 'noopener')
}

async function submitCallback(): Promise<void> {
  const address = pasteUrl.value.trim()
  const subscription = current.value
  if (!address || !subscription || !loginId.value) return
  pasteError.value = ''
  try {
    await forebrainApi.submitProviderLoginCallback(subscription.id, loginId.value, address)
    pasteUrl.value = ''
  } catch (cause) {
    if (cause instanceof GatewayHttpError && (cause.status === 404 || cause.status === 409)) {
      lost.value = true
      pending.value = false
      return
    }
    const reason = cause instanceof GatewayHttpError ? cause.message.trim() : ''
    pasteError.value =
      reason === 'state_mismatch'
        ? t('models.pasteStateMismatch')
        : reason === 'no_code'
          ? t('models.pasteNoCode')
          : t('models.pasteNotCallback')
  }
}

function restart(): void {
  void startLogin()
}

async function loadOffers(): Promise<void> {
  const subscription = current.value
  if (!subscription) return
  stopPoll()
  pending.value = false
  step.value = 3
  offersError.value = ''
  provisionError.value = ''
  try {
    offers.value = await forebrainApi.subscriptionOffers(subscription.id)
    // A first sign-in needs one model, and the vendor's default is the one it
    // would pick; everything else stays unchecked.
    const preset = offers.value.findIndex((offer) => offer.default)
    chosen.value = preset >= 0 ? [preset] : []
  } catch (cause) {
    offersError.value = getErrorMessage(cause) || t('models.offersFailed')
  }
}

async function provision(): Promise<void> {
  const subscription = current.value
  if (!subscription || !chosen.value.length || provisioning.value) return
  provisioning.value = true
  provisionError.value = ''
  try {
    await forebrainApi.provisionSubscription(subscription.id, {
      offers: chosen.value.map((index) => {
        const offer = offers.value[index]
        return {
          provider: offer.provider,
          model: offer.model,
          baseUrl: offer.baseUrl,
          apiPath: offer.apiPath,
          subagentAllowed: chosenSubagents.value.includes(index),
        }
      }),
      ...(keyId.value ? { keyId: keyId.value } : {}),
      ...(createKey.value ? { createKey: true } : {}),
    })
    emit('saved')
  } catch (cause) {
    if (cause instanceof GatewayHttpError && cause.status === 409) {
      const keys = cause.payload?.keyChoice
      if (Array.isArray(keys) && keys.length) {
        keyChoice.value = keys as { id: string; name: string; tail: string }[]
        keyChoiceFor.value = offers.value[chosen.value[0]]?.plan ?? ''
        keyId.value = keyChoice.value[0]?.id ?? ''
        // The choice applies to the plan of the first chosen row: a kit mixing
        // two plans is the vendor's call, and its refusal names what it needs.
        provisionError.value = t('models.keyChoice')
        return
      }
      if (cause.payload?.needsKeyConsent) {
        needsConsent.value = true
        keyChoiceFor.value = offers.value[chosen.value[0]]?.plan ?? ''
        provisionError.value = t('models.keyConsent')
        return
      }
    }
    provisionError.value = getErrorMessage(cause) || t('models.provisionFailed')
  } finally {
    provisioning.value = false
  }
}

function back(): void {
  if (step.value === 3) {
    step.value = current.value?.state === 'usable' ? 1 : 2
    return
  }
  stopPoll()
  step.value = 1
  pending.value = false
}

/** Walking away from a running flow ends it: the gateway would otherwise keep a
 *  device code, or a loopback port, held for the next sign-in. */
function cancelRunning(): void {
  const subscription = current.value
  const login = loginId.value
  const running = pending.value
  stopPoll()
  if (subscription && login && running) {
    void forebrainApi.cancelProviderLogin(subscription.id, login).catch(() => undefined)
  }
}

function close(): void {
  cancelRunning()
  reset()
  emit('close')
}

onBeforeUnmount(() => {
  cancelRunning()
})
</script>

<style scoped>
.stepl {
  margin: 0 0 4px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.listnote {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.subl {
  display: grid;
  margin-top: 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  overflow: hidden;
}
.subr {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 11px 14px;
  border: 0;
  border-bottom: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  text-align: left;
}
.subr:last-child {
  border-bottom: 0;
}
.subr:hover {
  background: var(--forebrain-bg-alt);
}
.subr__t {
  flex: 1;
  min-width: 0;
}
.subr__t b {
  display: block;
  font-size: 13px;
  font-weight: 600;
}
.subr__t span {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.subr > :last-child {
  color: var(--forebrain-muted-text);
}
.auth {
  display: grid;
  gap: 12px;
  margin-top: 6px;
  padding: 14px 16px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-bg-alt);
}
.auth p {
  margin: 0;
  font-size: 12.5px;
  color: var(--forebrain-text-2);
}
.acts2 {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
}
.ucode {
  padding: 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-surface);
  font-family: var(--forebrain-mono-font);
  font-size: 22px;
  font-weight: 600;
  letter-spacing: 0.16em;
  text-align: center;
}
.wait {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.spin {
  width: 13px;
  height: 13px;
  border-radius: 50%;
  border: 2px solid var(--forebrain-divider-strong);
  border-top-color: var(--forebrain-brand-1);
  animation: spin 1s linear infinite;
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
.paste {
  display: grid;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.paste__row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.bad {
  margin: 0;
  font-size: 12px;
  color: var(--forebrain-danger);
}
</style>
