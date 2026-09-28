<template>
  <div class="forebrain-app-shell">
    <aside class="forebrain-rail">
      <RouterLink to="/" class="forebrain-rail-brand">
        <img class="forebrain-rail-mark" src="/mark.svg" alt="" aria-hidden="true" />
        <span class="forebrain-rail-name">
          <span class="forebrain-rail-wordmark">Forebrain Harness</span>
          <span class="forebrain-rail-version">{{ appVersion }}</span>
        </span>
      </RouterLink>

      <!-- The tenant. Everything below belongs to the agent named here: its
           workspace, sessions, skills, memories and subagents. Switching is
           therefore a change of scope, not a filter. -->
      <div class="forebrain-tenant" ref="tenantMenuRef">
        <button
          type="button"
          class="forebrain-tenant-trigger"
          :aria-label="t('agents.selectorLabel')"
          :aria-expanded="tenantMenuOpen"
          :disabled="primaryLoading"
          @click="tenantMenuOpen = !tenantMenuOpen"
        >
          <span class="min-w-0 flex-1">
            <span class="forebrain-tenant-label">{{ t('agents.currentAgent', { count: primaryRecords.length }) }}</span>
            <span class="forebrain-tenant-name">{{ activePrimary?.id || t('common.none') }}</span>
          </span>
          <ChevronDown class="forebrain-tenant-caret" :class="{ 'forebrain-tenant-caret--open': tenantMenuOpen }" aria-hidden="true" />
        </button>
        <div class="forebrain-tenant-scope" :title="activePrimary?.workspaceRoot">
          {{ activePrimary?.workspaceRoot || '' }}
        </div>
        <div v-if="tenantMenuOpen" class="forebrain-tenant-menu" role="listbox">
          <button
            v-for="agent in primaryRecords"
            :key="agent.id"
            type="button"
            role="option"
            :aria-selected="agent.id === activePrimaryId"
            class="forebrain-tenant-option"
            :class="{ 'forebrain-tenant-option--active': agent.id === activePrimaryId }"
            @click="selectPrimaryAgent(agent.id)"
          >
            <span class="forebrain-tenant-option-name">
              <span v-if="agent.id === activePrimaryId" class="forebrain-tenant-dot" aria-hidden="true" />
              {{ agent.id }}
            </span>
            <span class="forebrain-tenant-option-path">{{ agent.workspaceRoot }}</span>
          </button>
        </div>
      </div>

      <nav
        v-for="group in navGroups"
        :key="group.key"
        class="forebrain-rail-group"
        :class="{ 'forebrain-rail-group--foot': group.foot }"
        :aria-label="group.label || navAria"
      >
        <div v-if="group.label" class="forebrain-rail-group-label">{{ group.label }}</div>
        <RouterLink
          v-for="item in group.items"
          :key="item.to"
          :to="item.to"
          class="forebrain-rail-link"
          :class="{ 'forebrain-rail-link--active': isActive(item.to) }"
        >
          <component :is="item.icon" class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>
    </aside>

    <div class="forebrain-work">
      <header class="forebrain-topbar">
        <span class="forebrain-topbar-title">{{ pageTitle }}</span>
        <span class="forebrain-topbar-spacer" />
        <div class="forebrain-language-menu" ref="languageMenuRef">
          <button
            type="button"
            class="forebrain-language-trigger"
            :aria-label="t('language.label')"
            :title="t('language.label')"
            :aria-expanded="languageMenuOpen"
            @click="languageMenuOpen = !languageMenuOpen"
          >
            <Languages class="forebrain-language-ic" aria-hidden="true" />
            <span class="forebrain-language-text">{{ currentLanguageLabel }}</span>
            <ChevronDown class="forebrain-language-caret" aria-hidden="true" />
          </button>
          <div v-if="languageMenuOpen" class="forebrain-language-popover">
            <button
              v-for="item in languageOptions"
              :key="item.value"
              type="button"
              class="forebrain-language-option"
              :class="{ 'forebrain-language-option--active': selectedLocale === item.value }"
              @click="selectLanguage(item.value)"
            >
              <span>{{ item.label }}</span>
              <Check v-if="selectedLocale === item.value" class="forebrain-language-check" aria-hidden="true" />
            </button>
          </div>
        </div>
        <button
          type="button"
          class="forebrain-theme-toggle"
          :aria-label="themeToggleLabel"
          :title="themeToggleLabel"
          @click="toggleTheme"
        >
          <Sun v-if="isDarkTheme" class="forebrain-theme-toggle-ic" aria-hidden="true" />
          <Moon v-else class="forebrain-theme-toggle-ic" aria-hidden="true" />
        </button>
      </header>
      <main class="forebrain-main">
        <router-view v-slot="{ Component }">
          <transition name="forebrain-page" mode="out-in">
            <!-- Keyed on the active agent: a switch is a change of tenant, so
                 every view is rebuilt against the new one rather than left
                 showing the previous agent's data. -->
            <component :is="Component" :key="`${route.fullPath}::${activePrimaryId}`" />
          </transition>
        </router-view>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  MessageSquare,
  Network,
  Brain,
  Settings,
  Wrench,
  ShieldCheck,
  FolderGit2,
  Plug,
  Clock,
  Radio,
  Boxes,
  Webhook,
  FileCog,
  Moon,
  Sun,
  Languages,
  ChevronDown,
  Check,
} from 'lucide-vue-next'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { navLabels, type Locale, useI18n } from '@/locales'

const route = useRoute()
const {
  locale: selectedLocale,
  languageOptions,
  currentLanguageLabel,
  setLocale,
  t,
} = useI18n()
const {
  records: primaryRecords,
  activeId: activePrimaryId,
  active: activePrimary,
  loading: primaryLoading,
  loadPrimaryAgents,
  switchPrimaryAgent,
} = usePrimaryAgents()

type ThemeMode = 'light' | 'dark'

const THEME_STORAGE_KEY = 'forebrain-theme'
const appVersion = __FOREBRAIN_VERSION__
const theme = ref<ThemeMode>('light')
const languageMenuOpen = ref(false)
const languageMenuRef = ref<HTMLElement | null>(null)
const tenantMenuOpen = ref(false)
const tenantMenuRef = ref<HTMLElement | null>(null)
let prefersDarkQuery: MediaQueryList | null = null

// Chat needs no heading — it is where the rail lands you — and settings sit at
// the foot, out of the scan path. Only the two groups that hold several
// destinations are worth naming.
const navGroups = computed(() => {
  const L = navLabels()
  return [
    { key: 'work', label: '', foot: false, items: [{ to: '/', label: L.chat, icon: MessageSquare }] },
    {
      key: 'control',
      label: L.controlGroup,
      foot: false,
      items: [
        { to: '/agents', label: L.agents, icon: Network },
        { to: '/projects', label: L.projects, icon: FolderGit2 },
        { to: '/cron', label: L.cron, icon: Clock },
        { to: '/channels', label: L.channels, icon: Radio },
        { to: '/permissions', label: L.permissions, icon: ShieldCheck },
      ],
    },
    {
      key: 'workspace',
      label: L.workspaceGroup,
      foot: false,
      items: [
        { to: '/tools', label: L.tools, icon: Wrench },
        { to: '/mcp', label: L.mcp, icon: Plug },
        { to: '/providers', label: L.providers, icon: Boxes },
        { to: '/hooks', label: L.hooks, icon: Webhook },
        { to: '/memories', label: L.memories, icon: Brain },
      ],
    },
    {
      key: 'settings',
      label: '',
      foot: true,
      items: [
        { to: '/config', label: L.config, icon: FileCog },
        { to: '/settings', label: L.settings, icon: Settings },
      ],
    },
  ]
})

const navAria = computed(() => navLabels().aria)

const pageTitle = computed(() => {
  for (const group of navGroups.value) {
    for (const item of group.items) {
      if (isActive(item.to)) return item.label
    }
  }
  return navLabels().chat
})

const isDarkTheme = computed(() => theme.value === 'dark')
const themeToggleLabel = computed(() => (isDarkTheme.value ? t('theme.toLight') : t('theme.toDark')))

function storedTheme(): ThemeMode | null {
  const value = window.localStorage.getItem(THEME_STORAGE_KEY)
  return value === 'dark' || value === 'light' ? value : null
}

function applyTheme(nextTheme: ThemeMode) {
  theme.value = nextTheme
  const root = document.documentElement
  root.classList.toggle('dark', nextTheme === 'dark')
  root.style.colorScheme = nextTheme
  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute(
    'content',
    nextTheme === 'dark' ? '#04111b' : '#eaf4f5',
  )
}

function toggleTheme() {
  const nextTheme: ThemeMode = isDarkTheme.value ? 'light' : 'dark'
  window.localStorage.setItem(THEME_STORAGE_KEY, nextTheme)
  applyTheme(nextTheme)
}

function syncSystemTheme(event: MediaQueryListEvent) {
  if (!storedTheme()) applyTheme(event.matches ? 'dark' : 'light')
}

function selectLanguage(nextLocale: Locale) {
  setLocale(nextLocale)
  languageMenuOpen.value = false
}

async function selectPrimaryAgent(id: string) {
  tenantMenuOpen.value = false
  if (!id || id === activePrimaryId.value) return
  try {
    await switchPrimaryAgent(id)
  } catch {
    // switchPrimaryAgent records the failure on the shared store; the rail keeps
    // showing the agent that is actually bound rather than the one just picked.
  }
}

function handleDocumentPointerDown(event: PointerEvent) {
  const target = event.target
  if (!(target instanceof Node)) return
  if (!languageMenuRef.value?.contains(target)) languageMenuOpen.value = false
  if (!tenantMenuRef.value?.contains(target)) tenantMenuOpen.value = false
}

function isActive(path: string) {
  if (path === '/') {
    return route.path === '/' || route.path === ''
  }
  return route.path === path || route.path.startsWith(path + '/')
}

onMounted(() => {
  prefersDarkQuery = window.matchMedia('(prefers-color-scheme: dark)')
  applyTheme(storedTheme() ?? (prefersDarkQuery.matches ? 'dark' : 'light'))
  prefersDarkQuery.addEventListener('change', syncSystemTheme)
  document.addEventListener('pointerdown', handleDocumentPointerDown)
  void loadPrimaryAgents()
})

onUnmounted(() => {
  prefersDarkQuery?.removeEventListener('change', syncSystemTheme)
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
})

watch(selectedLocale, () => {
  const title = route.meta?.title
  if (typeof title === 'function') document.title = title()
})
</script>

<style scoped>
.forebrain-page-enter-active,
.forebrain-page-leave-active {
  transition: opacity 0.16s ease;
}
.forebrain-page-enter-from,
.forebrain-page-leave-to {
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .forebrain-page-enter-active,
  .forebrain-page-leave-active {
    transition: none;
  }
}
</style>
