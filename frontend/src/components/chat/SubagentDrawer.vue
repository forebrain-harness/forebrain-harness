<template>
  <aside
    class="subagent-drawer"
    :class="{ 'subagent-drawer--overlay': overlay }"
    :style="overlay ? undefined : { width: `${width}px` }"
    data-testid="subagent-drawer"
    :aria-label="t('chat.subagentDrawer')"
    @keydown.esc="onEsc"
  >
    <!-- The drawer's width is dragged by the edge it shares with the
         conversation, and is this session's alone (see usePanelWidth). -->
    <div
      class="panel-resizer"
      role="separator"
      aria-orientation="vertical"
      :aria-label="t('common.resizePanel')"
      tabindex="0"
      data-testid="subagent-drawer-resizer"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerUp"
      @dblclick="reset"
      @keydown="onKeydown"
    />
    <header class="subagent-drawer-head">
      <!-- One tab array for both kinds, in the order they were opened: the
           drawer is one surface, and a plan opened between two subagents sits
           between them (prototype :4740/:4845). -->
      <div class="subagent-drawer-tabs" role="tablist" :aria-label="t('chat.subagentDrawer')">
        <template v-for="entry in orderedTabs" :key="entry.key">
        <div
          v-if="entry.kind === 'plan'"
          class="subagent-drawer-tab"
          :class="{ 'is-on': entry.key === activePlan }"
          :data-testid="`plan-tab-${entry.key}`"
        >
          <button
            type="button"
            role="tab"
            class="subagent-drawer-tab-main"
            :aria-selected="entry.key === activePlan"
            @click="emit('select-plan', entry.key)"
          >
            <MapIcon class="is-plan" aria-hidden="true" />
            <span class="subagent-drawer-tab-label" v-tip:bottom.trunc="entry.plan?.label">{{ entry.plan?.label }}</span>
          </button>
          <button
            type="button"
            class="subagent-drawer-tab-close"
            :aria-label="t('chat.subagentTabCloseAria', { name: entry.plan?.label })"
            v-tip:bottom="t('chat.subagentTabClose')"
            :data-testid="`plan-tab-close-${entry.key}`"
            @click="emit('close-plan', entry.key)"
          >
            <X aria-hidden="true" />
          </button>
        </div>
        <div
          v-else-if="entry.agent"
          :key="entry.key"
          class="subagent-drawer-tab"
          :class="{ 'is-on': !activePlan && entry.key === active }"
          :data-testid="`subagent-tab-${entry.key}`"
        >
          <button
            type="button"
            role="tab"
            class="subagent-drawer-tab-main"
            :aria-selected="!activePlan && entry.key === active"
            @click="emit('select', entry.key)"
          >
            <Loader2 v-if="entry.agent && phaseOf(entry.agent) === 'active'" class="is-run" aria-hidden="true" />
            <CheckCircle2 v-else-if="entry.agent && phaseOf(entry.agent) === 'done'" class="is-ok" aria-hidden="true" />
            <XCircle v-else class="is-bad" aria-hidden="true" />
            <span class="subagent-drawer-tab-label" v-tip:bottom.trunc="entry.agent ? tabLabel(entry.agent) : ''">{{ entry.agent ? tabLabel(entry.agent) : '' }}</span>
            <span
              v-if="entry.agent && unseen(entry.agent)"
              class="subagent-drawer-tab-new"
              :aria-label="t('chat.subagentUnseen')"
            />
          </button>
          <button
            type="button"
            class="subagent-drawer-tab-close"
            :aria-label="t('chat.subagentTabCloseAria', { name: entry.agent ? tabLabel(entry.agent) : '' })"
            v-tip:bottom="t('chat.subagentTabClose')"
            :data-testid="`subagent-tab-close-${entry.key}`"
            @click="emit('close-tab', entry.key)"
          >
            <X aria-hidden="true" />
          </button>
        </div>
        </template>
      </div>
      <button
        type="button"
        class="subagent-drawer-icon"
        aria-haspopup="menu"
        :aria-expanded="menuOpen"
        :aria-label="t('chat.subagentOpenOther')"
        v-tip:bottom="t('chat.subagentOpenOther')"
        data-testid="subagent-drawer-add"
        @click="toggleMenu"
      >
        <Plus aria-hidden="true" />
      </button>
      <span class="flex-1" />
      <button
        type="button"
        class="subagent-drawer-icon"
        :aria-label="t('chat.subagentDrawerCollapse')"
        v-tip:bottom="t('chat.subagentDrawerCollapse')"
        data-testid="subagent-drawer-collapse"
        @click="emit('collapse')"
      >
        <PanelRightClose aria-hidden="true" />
      </button>

      <!-- Every subagent this conversation dispatched, in the terminal roster's
           order: the tab opens one, and says which are already open. -->
      <div v-if="menuOpen" class="subagent-drawer-menu" role="menu" data-testid="subagent-drawer-menu">
        <div class="subagent-drawer-menu-cap">{{ t('chat.subagentDrawerList', { n: records.length }) }}</div>
        <button
          v-for="row in records"
          :key="row.agentId"
          type="button"
          role="menuitem"
          class="subagent-drawer-menu-row"
          :data-testid="`subagent-menu-${row.agentId}`"
          @click="openFromMenu(row.agentId)"
        >
          <Loader2 v-if="phaseOf(row) === 'active'" class="is-run" aria-hidden="true" />
          <CheckCircle2 v-else-if="phaseOf(row) === 'done'" class="is-ok" aria-hidden="true" />
          <XCircle v-else class="is-bad" aria-hidden="true" />
          <span class="subagent-drawer-menu-label">{{ tabLabel(row) }}</span>
          <span class="subagent-drawer-menu-side">{{ sideOf(row) }}</span>
        </button>
      </div>
    </header>
    <div class="subagent-drawer-body">
      <slot />
    </div>
  </aside>
</template>

<script setup lang="ts">
/**
 * The conversation's right-hand drawer: one tab per subagent the user has
 * opened, one per plan document opened from the workbench, the "+" that opens
 * another subagent, and the way back out. It is a shell only — every tab's
 * content is handed in by the page that owns the stream.
 *
 * It shares the right-hand column with the workbench, so it draws itself the
 * same way: a column on a wide screen, an overlay on a narrow one.
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { CheckCircle2, Loader2, Map as MapIcon, PanelRightClose, Plus, X, XCircle } from 'lucide-vue-next'
import { usePanelWidth } from '@/composables/usePanelWidth'
import { formatRuntimeDuration, type SubagentTranscript } from '@/composables/useChatStream'
import type { PlanTab } from '@/composables/useSessionPlans'
import { subagentPhase, subagentStatusLabel, subagentTabLabel } from '@/lib/subagentStatus'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  /** The open tabs, in the order they were opened. */
  tabs: SubagentTranscript[]
  /** The tab in front. */
  active: string
  /** Every subagent of this conversation, for the "+" menu. */
  records: SubagentTranscript[]
  /** Last updatedSeq the user has seen per agent, keyed by agent id. */
  seen: Record<string, number>
  overlay: boolean
  /** The stream's shared clock, for a running row's elapsed. */
  now?: number
  /** The plan documents open as tabs, in the same drawer. */
  planTabs?: PlanTab[]
  /** The plan tab in front; empty while a subagent tab is. */
  activePlan?: string
  /**
   * The order the tabs were opened in, across both kinds. Absent (or naming
   * nothing that is still open) falls back to the subagents, then the plans.
   */
  order?: string[]
}>(), {
  now: undefined,
  planTabs: () => [],
  activePlan: '',
  order: () => [],
})

const emit = defineEmits<{
  (e: 'select', agentId: string): void
  (e: 'close-tab', agentId: string): void
  (e: 'open', agentId: string): void
  (e: 'collapse'): void
  (e: 'escape-key'): void
  (e: 'select-plan', id: string): void
  (e: 'close-plan', id: string): void
}>()

const { t } = useI18n()
const menuOpen = ref(false)

// The drawer's design width, and how far the window lets it be dragged: 440 is
// where a tab's transcript reads, and past 60% of the window the conversation
// would be the one that lost its room (owner 2026-10-21). As an overlay below
// lg the width is the layout's, not the handle's — main.css hides the handle
// there and the drawer is drawn without an inline width.
const DESIGN_WIDTH = 440
const MIN_WIDTH = 320
const MAX_WIDTH = 860

const { width, onPointerDown, onPointerMove, onPointerUp, onKeydown, reset } = usePanelWidth(
  'subagent-drawer',
  DESIGN_WIDTH,
  { min: MIN_WIDTH, max: () => Math.min(MAX_WIDTH, window.innerWidth * 0.6) },
)

const openIds = computed(() => props.tabs.map((tab) => tab.agentId))

/** The tab bar, one entry per open tab, in the order they were opened. */
const orderedTabs = computed(() => {
  const plans = new globalThis.Map(props.planTabs.map((tab) => [tab.id, tab]))
  const agents = new globalThis.Map(props.tabs.map((tab) => [tab.agentId, tab]))
  const keys = props.order.filter((key) => plans.has(key) || agents.has(key))
  for (const id of agents.keys()) if (!keys.includes(id)) keys.push(id)
  for (const id of plans.keys()) if (!keys.includes(id)) keys.push(id)
  return keys.map((key) => {
    const plan = plans.get(key)
    if (plan) return { key, kind: 'plan' as const, plan, agent: undefined }
    return { key, kind: 'agent' as const, plan: undefined, agent: agents.get(key) }
  })
})

function phaseOf(record: SubagentTranscript) {
  return subagentPhase(record.status)
}

function tabLabel(record: SubagentTranscript): string {
  return subagentTabLabel(record)
}

/** A tab the user is not reading says so when the subagent produced something since. */
function unseen(tab: SubagentTranscript): boolean {
  if (tab.agentId === props.active) return false
  // A tab whose agent is still working is not holding anything unread for the
  // reader to come back to: the dot means "it said something while you were
  // elsewhere", not "it is busy" (prototype :4845).
  if (phaseOf(tab) === 'active') return false
  return tab.updatedSeq > (props.seen[tab.agentId] ?? 0)
}

/** The menu's right-hand column: already open, elapsed, or how it ended. */
function sideOf(record: SubagentTranscript): string {
  if (openIds.value.includes(record.agentId)) return t('chat.subagentOpened')
  const phase = phaseOf(record)
  if (phase === 'active') {
    const started = record.startedAt ? Date.parse(record.startedAt) : NaN
    if (Number.isFinite(started)) return formatRuntimeDuration(Math.max(0, (props.now ?? Date.now()) - started))
    return ''
  }
  return subagentStatusLabel(record.status)
}

function toggleMenu() {
  menuOpen.value = !menuOpen.value
}

function openFromMenu(agentId: string) {
  emit('open', agentId)
  menuOpen.value = false
}

// Escape closes the menu and nothing else; with the menu shut it is the
// drawer's own key, and marking it used here keeps the page from acting twice.
function onEsc(event: KeyboardEvent) {
  if (menuOpen.value) {
    menuOpen.value = false
    event.preventDefault()
    return
  }
  if (event.defaultPrevented) return
  event.preventDefault()
  emit('escape-key')
}

function onDocPointerDown(event: PointerEvent) {
  if (!menuOpen.value) return
  const target = event.target as HTMLElement | null
  if (target?.closest?.('.subagent-drawer-head')) return
  menuOpen.value = false
}

// The listener exists only while the menu does, so a closed drawer never eats
// a click meant for the conversation.
watch(menuOpen, (open) => {
  if (typeof document === 'undefined') return
  if (open) document.addEventListener('pointerdown', onDocPointerDown)
  else document.removeEventListener('pointerdown', onDocPointerDown)
})
onUnmounted(() => {
  if (typeof document !== 'undefined') document.removeEventListener('pointerdown', onDocPointerDown)
})
</script>

<style scoped>
.subagent-drawer {
  /* The drag handle is its left edge, so it is positioned against the drawer
     (`.panel-resizer` in main.css). */
  position: relative;
  width: 440px;
  flex: none;
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-left: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  /* The workbench's gesture, on the same edge: whichever panel takes the
     right-hand column slides in from it, and only mounting plays it. */
  animation: forebrain-drawer-in 160ms ease-out;
}
@keyframes forebrain-drawer-in {
  from {
    transform: translateX(32px);
    opacity: 0.6;
  }
  to {
    transform: none;
    opacity: 1;
  }
}
@media (prefers-reduced-motion: reduce) {
  .subagent-drawer {
    animation: none;
  }
}

.subagent-drawer--overlay {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 44;
  box-shadow: var(--forebrain-shadow-pop);
}

.subagent-drawer-head {
  position: relative;
  display: flex;
  align-items: center;
  gap: 2px;
  height: 44px;
  flex: none;
  padding: 0 8px;
  border-bottom: 1px solid var(--forebrain-divider);
}

.subagent-drawer-tabs {
  display: flex;
  align-items: center;
  gap: 2px;
  min-width: 0;
  overflow: hidden;
}

.subagent-drawer-tab {
  display: flex;
  align-items: center;
  min-width: 0;
  max-width: 200px;
  height: 30px;
  padding: 0 2px 0 8px;
  border-radius: 7px;
  color: var(--forebrain-text-2);
}

.subagent-drawer-tab.is-on {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}

.subagent-drawer-tab-main {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  height: 30px;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font-size: 12.5px;
  cursor: pointer;
}

.subagent-drawer-tab-main svg {
  width: 14px;
  height: 14px;
  flex: none;
}

.subagent-drawer-tab-main svg.is-run {
  color: var(--forebrain-brand-1);
  animation: subagent-drawer-spin 1s linear infinite;
}

.subagent-drawer-tab-main svg.is-ok {
  color: var(--forebrain-success);
}

.subagent-drawer-tab-main svg.is-bad {
  color: var(--forebrain-danger);
}

.subagent-drawer-tab-main svg.is-plan {
  color: var(--forebrain-brand-1);
}

.subagent-drawer-tab-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.subagent-drawer-tab-new {
  width: 6px;
  height: 6px;
  flex: none;
  border-radius: 50%;
  background: var(--forebrain-brand-1);
}

.subagent-drawer-tab-close {
  display: inline-grid;
  place-items: center;
  width: 22px;
  height: 22px;
  margin-left: 2px;
  flex: none;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--forebrain-muted-text);
  cursor: pointer;
}

.subagent-drawer-tab-close:hover {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-text);
}

.subagent-drawer-tab-close svg {
  width: 13px;
  height: 13px;
}

.subagent-drawer-icon {
  display: inline-grid;
  place-items: center;
  width: 26px;
  height: 26px;
  flex: none;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--forebrain-text-2);
  cursor: pointer;
}

.subagent-drawer-icon:hover {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-text);
}

.subagent-drawer-icon svg {
  width: 15px;
  height: 15px;
}

.subagent-drawer-menu {
  position: absolute;
  top: 40px;
  left: 8px;
  width: 320px;
  z-index: 5;
  display: grid;
  gap: 2px;
  padding: 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
  box-shadow: var(--forebrain-shadow-pop);
}

.subagent-drawer-menu-cap {
  padding: 4px 8px 2px;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}

.subagent-drawer-menu-row {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 32px;
  padding: 0 8px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--forebrain-text);
  font-size: 12.5px;
  text-align: left;
  cursor: pointer;
}

.subagent-drawer-menu-row:hover {
  background: var(--forebrain-button-alt-bg);
}

.subagent-drawer-menu-row svg {
  width: 14px;
  height: 14px;
  flex: none;
}

.subagent-drawer-menu-row svg.is-run {
  color: var(--forebrain-brand-1);
  animation: subagent-drawer-spin 1s linear infinite;
}

.subagent-drawer-menu-row svg.is-ok {
  color: var(--forebrain-success);
}

.subagent-drawer-menu-row svg.is-bad {
  color: var(--forebrain-danger);
}

.subagent-drawer-menu-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.subagent-drawer-menu-side {
  margin-left: auto;
  flex: none;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
}

.subagent-drawer-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

@keyframes subagent-drawer-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .subagent-drawer-tab-main svg.is-run,
  .subagent-drawer-menu-row svg.is-run {
    animation: none;
  }
}

@media (max-width: 1023px) {
  .subagent-drawer {
    width: min(440px, calc(100vw - 64px));
  }
}
</style>
