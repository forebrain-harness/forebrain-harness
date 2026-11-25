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
            <!-- A stored sign-in's account replaces the flow sentence: the
                 person is choosing between accounts, not between protocols. -->
            <span
              v-if="subscription.account"
              :data-testid="`models-sub-account-${subscription.id}`"
            >
              {{ accountLine(subscription.account) }}
            </span>
            <span v-else>{{ describe(subscription) }}</span>
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
             advances on its own once the person approves there. A plugin flow
             with no code (browser-confirm) drops the empty code box: the
             address is the whole instruction. -->
        <template v-if="flow === 'device_code'">
          <p>{{ deviceHintText }}</p>
          <div v-if="userCode || (!currentPlugin?.hideEmptyUserCode && !url)" class="ucode" data-testid="models-login-code">
            {{ userCode || t('models.deviceCodePending') }}
          </div>
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
      <!-- What the sign-out ended in: an external source saying the machine
           will be signed in again, or a failure worth retrying. -->
      <p
        v-if="notice"
        class="noticebar"
        :class="{ bad: notice === 'failed' }"
        :role="notice === 'failed' ? 'alert' : 'status'"
        :data-testid="notice === 'external' ? 'models-logout-external' : 'models-logout-failed'"
      >
        {{ notice === 'external' ? t('models.logoutExternal') : t('models.logoutFailed') }}
      </p>
      <!-- Who the machine is signed in as, and the way out: the sign-out a
           confirmation owns, and the switch that runs the flow again. -->
      <div v-if="current" class="acct" data-testid="models-login-account">
        <div class="acct__t">
          <b>{{ current.account?.name ?? t('models.accountUnknown') }}</b>
          <span v-if="current.account">{{ accountDetail(current.account) }}</span>
          <BadgeComponent :variant="stateVariant(current.state)">{{ stateText(current) }}</BadgeComponent>
        </div>
        <div v-if="!current.account?.envKey" class="acct__acts">
          <Button
            v-if="current.canLogout"
            type="button"
            variant="ghost"
            class="forebrain-btn forebrain-btn--ghost acct-logout h-auto text-xs"
            data-testid="models-logout"
            @click="logoutOpen = true"
          >
            {{ t('models.logout') }}
          </Button>
          <Button
            type="button"
            variant="ghost"
            class="forebrain-btn forebrain-btn--ghost h-auto text-xs"
            data-testid="models-switch-account"
            @click="switchAccount"
          >
            {{ t('models.switchAccount') }}
          </Button>
        </div>
      </div>
      <p v-if="current?.account?.envKey" class="envhint" data-testid="models-login-envkey">
        {{ t('models.envKeyHint', { name: current.account.name }) }}
      </p>
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
      <!-- A plan whose key nobody can fetch asks for it here: the console is
           the only place the key exists, and it is stored on this machine. -->
      <div v-if="keyPasteNeeded" class="kpaste">
        <span>{{ t('models.keyPasteLabel') }}</span>
        <Input
          v-model="pastedKey"
          type="password"
          autocomplete="off"
          class="forebrain-field h-[32px] font-mono text-xs"
          :aria-invalid="keyPasteMissing ? 'true' : undefined"
          data-testid="models-key-paste"
        />
        <p class="listnote">
          {{ t('models.keyPasteHint') }}
          <a v-if="keyConsoleURL" :href="keyConsoleURL" target="_blank" rel="noopener">{{ keyConsoleURL }}</a>
        </p>
      </div>
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
          :disabled="!chosen.length || provisioning || keyPasteMissing"
          data-testid="models-provision"
          @click="provision"
        >
          {{ provisioning ? t('common.loading') : t('models.addModels', { n: chosen.length }) }}
        </Button>
      </template>
    </template>
  </ForebrainModal>

  <!-- The sign-out asks first: removing a login is irreversible, and the models
       it added only work again once the person signs back in. A credential file
       another client shares is named, so its sign-out is never silent. -->
  <ConfirmDialog
    :open="logoutOpen"
    :title="t('models.logoutTitle')"
    :message="logoutMessage"
    :confirm-label="t('models.logoutConfirm')"
    danger
    @cancel="logoutOpen = false"
    @confirm="doLogout"
  />
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
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import OfferPicker from '@/components/models/OfferPicker.vue'
import {
  forebrainApi,
  GatewayHttpError,
  getErrorMessage,
  type ProviderLoginStatus,
  type SubscriptionAccount,
  type SubscriptionOffer,
  type SubscriptionRow,
} from '@/lib/api'
import { useI18n } from '@/locales'
import { getSubscriptionFrontendPlugin } from '@/plugins/subscriptionPlugin'

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

const emit = defineEmits<{ close: []; saved: []; changed: [] }>()

const { t, locale } = useI18n()

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
/** The API key a plan asks the user to paste (plan 281), and where the gateway
 *  says it is copied from. */
const pastedKey = ref('')
const keyConsoleURL = ref('')
/** The gateway answered that a chosen plan needs a key it cannot fetch. */
const keyPasteAsked = ref(false)
/** The sign-out's confirmation is open. */
const logoutOpen = ref(false)
/** What the last sign-out ended in: an external source that will sign this
 *  machine in again, or a failure worth retrying; '' is nothing to say. */
const notice = ref<'' | 'external' | 'failed'>('')

let timer: ReturnType<typeof setTimeout> | null = null

const stepText = computed(() =>
  step.value === 1 ? t('models.subscribeStep1') : step.value === 2 ? t('models.subscribeStep2') : t('models.subscribeStep3'),
)

/** Whether the chosen offers include a plan whose key the user has to paste:
 *  either the listing says so, or the write answered that it needs one. */
const keyPasteNeeded = computed(
  () => keyPasteAsked.value || chosen.value.some((index) => offers.value[index]?.needsKey === true),
)

/** The add button stays closed until the key a plan asks for is there. */
const keyPasteMissing = computed(() => keyPasteNeeded.value && !pastedKey.value.trim())

/** The local plugin that owns the subscription being signed in to, when one
 *  does: it carries the flow's wording and whether its device flow shows a
 *  code at all. */
const currentPlugin = computed(() => (current.value ? getSubscriptionFrontendPlugin(current.value.id) : undefined))

/** What the device-flow step says while the person authorizes elsewhere: the
 *  plugin's own sentence when it has one, the page's otherwise. */
const deviceHintText = computed(
  () => currentPlugin.value?.waitingHint?.(locale.value === 'zh' ? 'zh' : 'en') ?? t('models.deviceHint'),
)

/** What the second line under a subscription's name says: the flow it drives,
 *  in the page's words rather than the backend's enum — or, for a subscription
 *  a local plugin owns, in that plugin's words. */
function describe(subscription: SubscriptionRow): string {
  const plugin = getSubscriptionFrontendPlugin(subscription.id)
  if (plugin?.describe) {
    return plugin.describe(locale.value === 'zh' ? 'zh' : 'en', subscription)
  }
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

/** What the account's own line says: the name, with the plan or organization
 *  beside it — the environment's own key names the variable instead of a
 *  detail the page would have to guess at. */
function accountLine(account: SubscriptionAccount): string {
  const detail = accountDetail(account)
  return detail ? `${account.name} · ${detail}` : account.name
}

/** The detail beside a name, in one place: the environment's key reads as
 *  "provided by the environment", everything else as the vendor's own word. */
function accountDetail(account: SubscriptionAccount): string {
  return account.envKey ? t('models.accountEnvKey') : (account.detail ?? '')
}

/** The sign-out confirmation's message: what is removed, and — when the
 *  credential file is another client's own — which client signs out with it. */
const logoutMessage = computed(() => {
  const shared = current.value?.account?.sharedPath
  const text = t('models.logoutMessage')
  return shared ? text + t('models.logoutShared', { path: shared }) : text
})

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
  pastedKey.value = ''
  keyConsoleURL.value = ''
  keyPasteAsked.value = false
  logoutOpen.value = false
  notice.value = ''
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

/** Rereads the subscriptions; `pick` says whether the initial id (the drawer's
 *  relogin entry) should be acted on — a reread that follows a sign-out must
 *  not walk back into the login it just removed. */
async function loadSubscriptions(pick = true): Promise<void> {
  try {
    subscriptions.value = await forebrainApi.subscriptions()
  } catch (cause) {
    loginError.value = getErrorMessage(cause)
    return
  }
  const wanted = pick && props.initialId ? subscriptions.value.find((entry) => entry.id === props.initialId) : undefined
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

async function startLogin(force = false): Promise<void> {
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
  notice.value = ''
  try {
    // force skips the gateway's "already signed in" shortcut: switching
    // accounts is the point, so the flow runs even on a working login.
    const started = await forebrainApi.startProviderLogin(subscription.id, { force })
    if (current.value?.id !== subscription.id) return
    // A reusable sign-in on this machine is already the finished one: there is
    // nothing to poll and no code to confirm. It says who it is, so the
    // account bar is true without another round trip.
    if (started.status === 'complete') {
      applyAccount(subscription.id, started.account, started.canLogout)
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

/** A login that just completed names its account: the bar the third step draws
 *  reads the fresh answer rather than the stale row the first step listed. */
function applyAccount(id: string, account?: SubscriptionAccount, canLogout?: boolean): void {
  if (current.value?.id !== id) return
  current.value = { ...current.value, account: account ?? undefined, canLogout: canLogout ?? false }
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
    applyAccount(id, status.account, status.canLogout)
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

/** Switching accounts runs the sign-in flow again on purpose — the gateway's
 *  "already signed in" shortcut is exactly what must not fire — and any flow
 *  still running from the same modal is ended first. */
function switchAccount(): void {
  cancelRunning()
  stopPoll()
  step.value = 2
  void startLogin(true)
}

/** The confirmed sign-out: the stored login is removed, the rows it wrote
 *  stay, and the page that owns the table hears about it so it can refresh.
 *  An external source answering 409 is told as it is — the stored login was
 *  removed, but the machine will be signed in again — and the list is reread
 *  so the account bar names whoever holds the login now. */
async function doLogout(): Promise<void> {
  const subscription = current.value
  if (!subscription) return
  logoutOpen.value = false
  notice.value = ''
  try {
    await forebrainApi.logoutSubscription(subscription.id)
  } catch (cause) {
    if (cause instanceof GatewayHttpError && cause.payload?.externalLogin) {
      notice.value = 'external'
    } else {
      notice.value = 'failed'
      return
    }
  }
  emit('changed')
  await loadSubscriptions(false)
  const fresh = subscriptions.value.find((entry) => entry.id === subscription.id)
  if (fresh) current.value = fresh
  if (notice.value === 'external') {
    // The machine is signed in again by whatever answered 409: the bar keeps
    // showing whoever the reread found, and the person decides what to do.
    return
  }
  current.value = null
  step.value = 1
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
    // What this install already holds is ticked, the way the terminal's
    // /connect opens on it; a first sign-in has nothing held and preselects the
    // vendor's own default.
    const held = offers.value.map((offer, index) => (offer.held ? index : -1)).filter((index) => index >= 0)
    const preset = offers.value.findIndex((offer) => offer.default)
    chosen.value = held.length > 0 ? held : preset >= 0 ? [preset] : []
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
      ...(pastedKey.value.trim() ? { key: pastedKey.value.trim() } : {}),
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
      // The plan wants a key the console owns: show the field, and let the page
      // say where the key comes from.
      if (cause.payload?.needsKey) {
        keyPasteAsked.value = true
        keyConsoleURL.value = String(cause.payload?.consoleUrl ?? '')
        provisionError.value = ''
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
.kpaste {
  margin-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 12px;
}
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
.acct {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 6px;
  padding: 11px 14px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-bg-alt);
}
.acct__t {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.acct__t b {
  font-size: 13px;
  font-weight: 600;
  overflow-wrap: anywhere;
}
.acct__t span {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.acct__acts {
  display: flex;
  gap: 8px;
  flex: none;
}
.acct-logout {
  color: var(--forebrain-danger);
}
.acct-logout:hover {
  color: var(--forebrain-danger);
}
.envhint {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  line-height: 1.6;
}
.noticebar {
  margin: 10px 0 0;
  padding: 9px 12px;
  border: 1px solid var(--forebrain-warning);
  border-radius: 8px;
  background: var(--forebrain-warning-soft);
  font-size: 12.5px;
  color: var(--forebrain-warning);
  line-height: 1.55;
}
.noticebar.bad {
  border-color: var(--forebrain-danger);
  background: var(--forebrain-surface);
  color: var(--forebrain-danger);
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
