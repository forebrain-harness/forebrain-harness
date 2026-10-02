<template>
  <main class="lp">
    <div class="lp-top">
      <button type="button" class="lp-lang" @click="toggleLocale">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
          <circle cx="12" cy="12" r="9" />
          <path d="M3.5 12h17M12 3c2.6 2.7 3.9 5.7 3.9 9s-1.3 6.3-3.9 9c-2.6-2.7-3.9-5.7-3.9-9s1.3-6.3 3.9-9z" />
        </svg>
        {{ otherLanguageLabel }}
      </button>
    </div>
    <div class="lp-main">
      <div class="lp-card">
        <div class="lp-brand">
          <BrandMark :size="44" class="lp-mark" />
          <span class="lp-wordmark"><b>Forebrain</b> Harness</span>
        </div>
        <template v-if="viaLink">
          <div class="lp-link">
            <div class="lp-link-row">
              <span class="lp-spin" aria-hidden="true" />
              {{ t('login.viaLink') }}
            </div>
            <p>{{ t('login.viaLinkHint') }}</p>
          </div>
        </template>
        <template v-else>
          <div class="lp-head">
            <h2>{{ t('login.title') }}</h2>
            <p>{{ t('login.description') }}</p>
          </div>
          <form class="lp-form" novalidate @submit.prevent="submit">
            <label class="lp-label" for="gateway-token">{{ t('login.tokenLabel') }}</label>
            <input
              id="gateway-token"
              v-model="token"
              class="lp-input"
              type="password"
              autocomplete="off"
              spellcheck="false"
              :placeholder="t('login.tokenPlaceholder')"
              :aria-invalid="error ? 'true' : undefined"
              :readonly="submitting"
            />
            <div v-if="error" class="lp-error" role="alert">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
                <circle cx="12" cy="12" r="9" />
                <path d="M12 8v5M12 16.5v.01" />
              </svg>
              {{ error }}
            </div>
            <div class="lp-hint">{{ t('login.tokenHint') }}</div>
            <button class="lp-btn" type="submit" :disabled="!token.trim() || submitting">
              <span v-if="submitting" class="lp-spin" aria-hidden="true" />
              {{ submitting ? t('login.submitting') : t('login.submit') }}
            </button>
          </form>
        </template>
      </div>
    </div>
    <div class="lp-foot">
      <span>{{ t('login.connectedTo') }}</span>
      <code>{{ host }}</code>
      <span>·</span>
      <span>{{ version }}</span>
    </div>
  </main>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  markGatewaySessionOk,
  probeGatewaySession,
  signInToGateway,
  tokenFromLocationHash,
} from '@/lib/gatewaySession'
import BrandMark from '@/components/BrandMark.vue'
import { useI18n } from '@/locales'

const { locale: selectedLocale, languageOptions, setLocale, t } = useI18n()
const route = useRoute()
const router = useRouter()

const version = __FOREBRAIN_VERSION__
const host = typeof window === 'undefined' ? '' : window.location.host

const token = ref('')
const error = ref('')
const submitting = ref(false)
const viaLink = ref(false)

const otherLanguageLabel = computed(
  () => languageOptions.value.find((option) => option.value !== selectedLocale.value)?.label ?? '',
)

function toggleLocale() {
  const other = languageOptions.value.find((option) => option.value !== selectedLocale.value)
  if (other) setLocale(other.value)
}

function redirectTarget(): string {
  const value = route.query.redirect
  return typeof value === 'string' && value.startsWith('/') && !value.startsWith('//') ? value : '/'
}

async function submit(): Promise<void> {
  const value = token.value.trim()
  if (!value || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    await signInToGateway(value)
    markGatewaySessionOk()
    await router.replace(redirectTarget())
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause)
    submitting.value = false
  }
}

// The sign-in link carries the token in the fragment, which browsers never
// send to the server but do keep in history — scrub it before anything else.
async function signInFromLink(value: string): Promise<void> {
  viaLink.value = true
  try {
    await signInToGateway(value)
    markGatewaySessionOk()
    await router.replace(redirectTarget())
  } catch (cause) {
    // The link's token was refused: fall back to the form and show the
    // server's message, exactly as a manual submission would.
    viaLink.value = false
    error.value = cause instanceof Error ? cause.message : String(cause)
  }
}

onMounted(async () => {
  const linkToken = tokenFromLocationHash(window.location.hash)
  if (linkToken) {
    window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search)
    await signInFromLink(linkToken)
    return
  }
  if ((await probeGatewaySession()) === 'ok') {
    await router.replace(redirectTarget())
  }
})

watch(selectedLocale, () => {
  document.title = t('routes.loginTitle')
})
</script>

<style scoped>
/* The sign-in page is always white with the navy brand; it carries its own
   tokens so an unauthenticated browser sees it before any shell theming. */
.lp {
  --lp-page: #ffffff;
  --lp-subtle: #f7f8fa;
  --lp-line: #e4e7ec;
  --lp-line-2: #d0d5dd;
  --lp-text: #101828;
  --lp-text-2: #475467;
  --lp-muted: #667085;
  --lp-danger: #b42318;
  --lp-brand: #1f4a9e;
  --lp-brand-hover: #173b80;
  --lp-brand-soft: #ebf0f9;
  --lp-brand-border: #c3d2ec;
  --lp-on-brand: #ffffff;
  min-height: 100vh;
  display: grid;
  grid-template-rows: auto 1fr auto;
  background: var(--lp-page);
  color: var(--lp-text);
  font-family: -apple-system, BlinkMacSystemFont, 'PingFang SC', 'Hiragino Sans GB', 'Segoe UI',
    'Microsoft YaHei', 'Noto Sans SC', Roboto, 'Helvetica Neue', Arial, sans-serif;
}

.lp-top {
  display: flex;
  justify-content: flex-end;
  padding: 16px 20px;
}

.lp-lang {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  border: 1px solid var(--lp-line);
  border-radius: 6px;
  padding: 0 10px;
  font-size: 12px;
  color: var(--lp-text-2);
  background: var(--lp-page);
  cursor: pointer;
}
.lp-lang svg { width: 13px; height: 13px; }

.lp-main {
  display: grid;
  place-items: center;
  padding: 8px 16px 24px;
}

.lp-card {
  width: 100%;
  max-width: 400px;
  border: 1px solid var(--lp-line);
  border-radius: 12px;
  padding: 32px 32px 28px;
  display: grid;
  gap: 22px;
  background: var(--lp-page);
}

.lp-brand {
  display: flex;
  align-items: center;
  gap: 12px;
}
.lp-mark { width: 40px; height: 40px; }
.lp-wordmark { font-size: 16px; line-height: 1.2; }
.lp-wordmark b { font-weight: 650; }

.lp-head { display: grid; gap: 6px; }
.lp-head h2 { margin: 0; font-size: 22px; line-height: 1.3; font-weight: 650; }
.lp-head p { margin: 0; color: var(--lp-text-2); font-size: 14px; }

.lp-form { display: grid; gap: 8px; }
.lp-label { font-size: 13px; font-weight: 600; }

.lp-input {
  width: 100%;
  box-sizing: border-box;
  height: 42px;
  border: 1px solid var(--lp-line-2);
  border-radius: 8px;
  padding: 0 12px;
  font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 13px;
  color: var(--lp-text);
  background: var(--lp-page);
  outline: none;
}
.lp-input::placeholder { font-family: inherit; color: var(--lp-muted); }
.lp-input:focus { border-color: var(--lp-brand); box-shadow: 0 0 0 3px var(--lp-brand-soft); }
.lp-input[aria-invalid='true'] { border-color: var(--lp-danger); }
.lp-input[aria-invalid='true']:focus { box-shadow: none; }

.lp-error {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--lp-danger);
  font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace;
}
.lp-error svg { width: 14px; height: 14px; flex: none; }

.lp-hint { font-size: 12.5px; color: var(--lp-muted); line-height: 1.6; }

.lp-btn {
  height: 42px;
  border: 0;
  border-radius: 8px;
  font: inherit;
  font-size: 14px;
  font-weight: 600;
  color: var(--lp-on-brand);
  background: var(--lp-brand);
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  margin-top: 6px;
}
.lp-btn:hover:not([disabled]) { background: var(--lp-brand-hover); }
.lp-btn:focus-visible { outline: 2px solid var(--lp-brand); outline-offset: 2px; }
.lp-btn[disabled] { background: var(--lp-brand-border); cursor: default; }

.lp-spin {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  border: 2px solid currentColor;
  border-right-color: transparent;
  animation: lp-spin 0.8s linear infinite;
}

.lp-link { display: grid; justify-items: start; gap: 10px; padding: 8px 0; }
.lp-link-row { display: flex; align-items: center; gap: 10px; font-size: 15px; font-weight: 600; }
.lp-link-row .lp-spin { color: var(--lp-brand); width: 16px; height: 16px; }
.lp-link p { margin: 0; color: var(--lp-text-2); font-size: 13.5px; }

.lp-foot {
  display: flex;
  justify-content: center;
  gap: 8px;
  padding: 18px 16px 22px;
  font-size: 12px;
  color: var(--lp-muted);
  flex-wrap: wrap;
}
.lp-foot code {
  font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 11.5px;
  color: var(--lp-text-2);
}

@keyframes lp-spin { to { transform: rotate(360deg); } }

@media (prefers-reduced-motion: reduce) {
  .lp-spin { animation: none; border-right-color: currentColor; opacity: 0.6; }
}

/* Phone width: the card merges with the page instead of floating on it. */
@media (max-width: 480px) {
  .lp-card { border: 0; padding: 12px 4px; }
}
</style>
