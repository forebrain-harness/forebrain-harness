<template>
<!-- Bare routes (the sign-in page) render without the app shell. -->
<router-view v-if="route.meta.bare" />
<div v-else class="forebrain-app-shell">
    <aside class="forebrain-rail" :class="{ 'forebrain-rail--collapsed': railCollapsed }">
      <div class="forebrain-rail-top">
        <RouterLink to="/" class="forebrain-rail-brand">
          <BrandMark :size="28" class="forebrain-rail-mark" />
          <span class="forebrain-rail-name">
            <span class="forebrain-rail-wordmark"><span class="forebrain-rail-wordmark-strong">Forebrain</span> Harness</span>
            <span class="forebrain-rail-version">{{ appVersion }}</span>
          </span>
        </RouterLink>
        <button
          v-if="!railCollapsed"
          type="button"
          class="forebrain-rail-fold"
          data-testid="rail-collapse"
          :aria-label="t('rail.collapse')"
          :aria-expanded="true"
          @click="collapseRail()"
        >
          <PanelLeftClose class="size-4" aria-hidden="true" />
        </button>
      </div>
      <button
        v-if="railCollapsed"
        type="button"
        class="forebrain-rail-expand"
        data-testid="rail-collapse"
        :aria-label="t('rail.expand')"
        :aria-expanded="false"
        @mouseenter="hoverTooltip($event.currentTarget as HTMLElement, t('rail.expand'))"
        @mouseleave="clearTooltip"
        @focus="hoverTooltip($event.currentTarget as HTMLElement, t('rail.expand'))"
        @blur="clearTooltip"
        @click="expandRail()"
      >
        <PanelLeftOpen class="size-[18px]" aria-hidden="true" />
      </button>

      <!-- The tenant selector: the one switch point for primary agents. -->
      <div class="forebrain-tenant" ref="tenantMenuRef">
        <button
          v-if="!railCollapsed"
          type="button"
          class="forebrain-tenant-trigger"
          :aria-label="t('agents.selectorLabel')"
          :aria-expanded="tenantMenuOpen"
          aria-haspopup="listbox"
          :disabled="primaryLoading"
          @click="tenantMenuOpen = !tenantMenuOpen"
        >
          <span class="min-w-0 flex-1">
            <span class="forebrain-tenant-label">{{ t('agents.currentAgent', { count: primaryRecords.length }) }}</span>
            <span class="forebrain-tenant-name">{{ activePrimary?.id || t('common.none') }}</span>
          </span>
          <ChevronDown class="forebrain-tenant-caret" :class="{ 'forebrain-tenant-caret--open': tenantMenuOpen }" aria-hidden="true" />
        </button>
        <button
          v-else
          type="button"
          class="forebrain-tenant-tile"
          :aria-label="tenantTileLabel"
          :title="tenantTileLabel"
          aria-haspopup="listbox"
          :aria-expanded="tenantMenuOpen"
          @mouseenter="hoverTooltip($event.currentTarget as HTMLElement, tenantTileLabel)"
          @mouseleave="clearTooltip"
          @focus="hoverTooltip($event.currentTarget as HTMLElement, tenantTileLabel)"
          @blur="clearTooltip"
          @click="tenantMenuOpen = !tenantMenuOpen"
        >
          {{ tenantInitial }}
        </button>
        <Teleport to="body">
          <div v-if="tenantMenuOpen" class="forebrain-tenant-backdrop" @pointerdown="tenantMenuOpen = false" />
          <div
            v-if="tenantMenuOpen"
            class="forebrain-tenant-menu"
            role="listbox"
            :style="{ left: `${railWidth}px`, top: tenantMenuTop }"
          >
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
        </Teleport>
        <!-- A switch or a load that failed says so, in the server's words. -->
        <p v-if="primaryError && !railCollapsed" class="forebrain-tenant-error" role="alert" data-testid="tenant-error">{{ primaryError }}</p>
      </div>

      <nav class="forebrain-rail-group" :aria-label="navAria">
        <button
          type="button"
          class="forebrain-rail-link"
          :class="{ 'forebrain-rail-link--active': isActive('/') }"
          :aria-label="L.chat"
          @mouseenter="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, L.chat)"
          @mouseleave="clearTooltip"
          @focus="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, L.chat)"
          @blur="clearTooltip"
          @click="toggleChatDrawer"
        >
          <MessageSquare class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ L.chat }}</span>
          <ChevronRight v-if="!railCollapsed" class="forebrain-rail-caret" :class="{ 'forebrain-rail-caret--open': chatDrawerOpen }" aria-hidden="true" />
        </button>
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="forebrain-rail-link"
          :class="{ 'forebrain-rail-link--active': isActive(item.to) }"
          :aria-label="item.label"
          @mouseenter="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, item.label)"
          @mouseleave="clearTooltip"
          @focus="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, item.label)"
          @blur="clearTooltip"
        >
          <component :is="item.icon" class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="forebrain-rail-foot">
        <button
          type="button"
          class="forebrain-rail-link"
          :aria-label="L.heartbeat"
          :disabled="!heartbeatSessionId"
          :title="heartbeatSessionId ? undefined : t('heartbeat.needsSession')"
          @mouseenter="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, L.heartbeat)"
          @mouseleave="clearTooltip"
          @focus="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, L.heartbeat)"
          @blur="clearTooltip"
          @click="toggleHeartbeatPopover"
        >
          <Activity class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ L.heartbeat }}</span>
        </button>
        <RouterLink
          to="/settings"
          class="forebrain-rail-link"
          :class="{ 'forebrain-rail-link--active': isActive('/settings') }"
          :aria-label="L.settings"
          @mouseenter="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, L.settings)"
          @mouseleave="clearTooltip"
          @focus="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, L.settings)"
          @blur="clearTooltip"
        >
          <Settings class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ L.settings }}</span>
        </RouterLink>
        <button
          type="button"
          class="forebrain-rail-link"
          :aria-label="themeToggleLabel"
          @mouseenter="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, themeToggleLabel)"
          @mouseleave="clearTooltip"
          @focus="railCollapsed && hoverTooltip($event.currentTarget as HTMLElement, themeToggleLabel)"
          @blur="clearTooltip"
          @click="toggleRailTheme"
        >
          <Sun v-if="isDarkTheme" class="forebrain-rail-ic" aria-hidden="true" />
          <Moon v-else class="forebrain-rail-ic" aria-hidden="true" />
          <span class="forebrain-rail-scheme">{{ isDarkTheme ? t('theme.toLight') : t('theme.toDark') }}</span>
        </button>
      </div>
    </aside>

    <ChatDrawer
      :open="chatDrawerOpen"
      :rail-width="railWidth"
      :agent-name="activePrimary?.id || ''"
      :active-session-id="heartbeatSessionId"
      @close="chatDrawerOpen = false"
      @select="(id) => { chatDrawerOpen = false; openSession(id) }"
      @created="async (id) => { await openSession(id) }"
    />
    <RailHeartbeat
      :open="heartbeatOpen"
      :rail-width="railWidth"
      :session-id="heartbeatSessionId"
      @close="heartbeatOpen = false"
    />
    <RailTooltip :anchor="tooltipAnchor" :text="tooltipText" />

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
      </header>
      <main class="forebrain-main">
        <router-view v-slot="{ Component }">
          <!-- Keyed on path + tenant, not the full URL: a session change is a
               query change and must not rebuild the conversation view. No
               <transition> here: an out-in leave around async route
               components never completes on a cold load with the bare
               sign-in branch above, leaving the view mounted in memory but
               absent from the DOM. -->
          <component :is="Component" :key="`${route.path}::${activePrimaryId}`" />
        </router-view>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  Activity,
  ScrollText,
  Users,
  Boxes,
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  Clock,
  FolderGit2,
  Languages,
  MessageSquare,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Radio,
  Settings,
  ShieldCheck,
  Sun,
  Wrench,
  Hammer,
  Sparkles,
} from 'lucide-vue-next'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ChatDrawer from '@/components/ChatDrawer.vue'
import RailHeartbeat from '@/components/RailHeartbeat.vue'
import RailTooltip from '@/components/RailTooltip.vue'
import BrandMark from '@/components/BrandMark.vue'
import { useAppearance } from '@/composables/useAppearance'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { useRailCollapse } from '@/composables/useRailCollapse'
import { resetTenantScope } from '@/composables/useTenantScope'
import { navLabels, type Locale, useI18n } from '@/locales'

const route = useRoute()
const router = useRouter()
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
  error: primaryError,
  loadPrimaryAgents,
  switchPrimaryAgent,
} = usePrimaryAgents()
const { railTheme, toggleRailTheme } = useAppearance()
const { collapsed: railCollapsed, collapse: collapseRail, expand: expandRail } = useRailCollapse()

const appVersion = __FOREBRAIN_VERSION__
const languageMenuOpen = ref(false)
const languageMenuRef = ref<HTMLElement | null>(null)
const tenantMenuOpen = ref(false)
const tenantMenuRef = ref<HTMLElement | null>(null)
const chatDrawerOpen = ref(false)
const heartbeatOpen = ref(false)
const tooltipAnchor = ref<HTMLElement | null>(null)
const tooltipText = ref('')

/** The rail's own width: the overlays anchor to its right edge. */
const railWidth = computed(() => (railCollapsed.value ? 64 : 264))

/** The conversation whose heartbeat the rail would act on: the session the
 *  address bar names, when the chat page is where we are. */
const heartbeatSessionId = computed(() => {
  const sid = String(route.query.session ?? '').trim()
  return route.path === '/' && sid ? sid : null
})

function hoverTooltip(anchor: HTMLElement, text: string) {
  tooltipAnchor.value = anchor
  tooltipText.value = text
}

function clearTooltip() {
  tooltipAnchor.value = null
  tooltipText.value = ''
}

function toggleChatDrawer() {
  chatDrawerOpen.value = !chatDrawerOpen.value
}

function toggleHeartbeatPopover() {
  heartbeatOpen.value = !heartbeatOpen.value
}

async function openSession(id: string) {
  await router.push({ path: '/', query: { ...route.query, session: id } })
}

const tenantInitial = computed(() => (activePrimary.value?.id ?? '?').trim().charAt(0).toUpperCase() || '?')

const tenantTileLabel = computed(() => t('rail.currentAgentName', { name: activePrimary.value?.id ?? '' }))

const tenantMenuTop = computed(() => {
  const rect = tenantMenuRef.value?.getBoundingClientRect()
  return rect ? `${rect.top}px` : '76px'
})

// One flat list: destinations are peers, and the Chat entry above them opens
// the drawer instead of navigating. Group labels are gone with the groups.
// The final flat menu, order fixed by the approved preview. Rules (009),
// skills (010) and the workshop (011) add their own entries later; every
// item carries the scope its page operates in.
const navItems = computed(() => {
  const L = navLabels()
  return [
    { to: '/projects', label: L.projects, icon: FolderGit2, scope: 'project' as const },
    { to: '/rules', label: L.rules, icon: ScrollText, scope: 'agent' as const },
    { to: '/skills', label: L.skills, icon: Sparkles, scope: 'agent' as const },
    { to: '/workshop', label: L.workshop, icon: Hammer, scope: 'agent' as const },
    { to: '/subagents', label: L.subagents, icon: Users, scope: 'agent' as const },
    { to: '/cron', label: L.cron, icon: Clock, scope: 'agent' as const },
    { to: '/channels', label: L.channels, icon: Radio, scope: 'agent' as const },
    { to: '/providers', label: L.providers, icon: Boxes, scope: 'agent' as const },
    { to: '/tools', label: L.tools, icon: Wrench, scope: 'agent' as const },
    { to: '/permissions', label: L.permissions, icon: ShieldCheck, scope: 'agent' as const },
    { to: '/memories', label: L.memories, icon: Brain, scope: 'agent' as const },
  ]
})

const L = computed(() => navLabels())

const navAria = computed(() => navLabels().aria)

const pageTitle = computed(() => {
  if (isActive('/')) return L.value.chat
  for (const item of navItems.value) {
    if (isActive(item.to)) return item.label
  }
  return L.value.chat
})

const isDarkTheme = computed(() => railTheme.value === 'dark')
const themeToggleLabel = computed(() => (isDarkTheme.value ? t('theme.toLight') : t('theme.toDark')))

function selectLanguage(nextLocale: Locale) {
  setLocale(nextLocale)
  languageMenuOpen.value = false
}

async function selectPrimaryAgent(id: string) {
  tenantMenuOpen.value = false
  if (!id || id === activePrimaryId.value) return
  try {
    // The open conversation belongs to the agent being left. The address bar
    // is what the chat page opens, and the page is rebuilt for the new
    // tenant the moment the switch lands — so the session leaves the
    // address first, or the new tenant's page would reopen it.
    if (route.query.session) {
      await router.replace({ query: { ...route.query, session: undefined } })
    }
    const before = activePrimaryId.value
    try {
      await switchPrimaryAgent(id)
    } finally {
      // The tenant the gateway is on now — the one picked, or the one a
      // failed switch recorded before its rebuild failed — owns everything
      // the frontend shows; what was loaded under the old one is dropped.
      if (activePrimaryId.value !== before) resetTenantScope()
    }
  } catch {
    // switchPrimaryAgent records the failure on the shared store and reloads
    // the list, so the rail shows the agent the gateway is actually bound to.
  }
}

// The tenant menu is teleported out of the rail, so "outside the rail's
// selector" says nothing about it — every press on one of its options would
// count as outside and close it before the click lands. Its own backdrop is
// what catches presses outside it.
function handleDocumentPointerDown(event: PointerEvent) {
  const target = event.target
  if (!(target instanceof Node)) return
  if (!languageMenuRef.value?.contains(target)) languageMenuOpen.value = false
}

function isActive(path: string) {
  if (path === '/') {
    return route.path === '/' || route.path === ''
  }
  return route.path === path || route.path.startsWith(path + '/')
}

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
})

// The shell loads its agents when it is actually shown: the bare sign-in page
// must not fire business requests before a session exists.
watch(() => route.meta.bare, (bare) => {
  if (!bare) void loadPrimaryAgents()
}, { immediate: true })

onUnmounted(() => {
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
})

watch(selectedLocale, () => {
  const title = route.meta?.title
  if (typeof title === 'function') document.title = title()
})
</script>

