<template>
  <aside class="chat-workbench" :class="{ 'chat-workbench--overlay': overlay }" data-testid="chat-workbench">
    <header class="chat-workbench-head">
      <span class="chat-workbench-title">{{ t('chat.workbench') }}</span>
      <button
        type="button"
        class="chat-workbench-close"
        :aria-label="t('chat.workbenchClose')"
        v-tip:bottom="t('chat.workbenchClose')"
        data-testid="chat-workbench-close"
        @click="emit('close')"
      >
        <PanelRightClose aria-hidden="true" />
      </button>
    </header>

    <div
      ref="barEl"
      class="bench-tabs"
      :class="{ 'bench-tabs--paged': paged, 'bench-tabs--moved': paged && offset > 0 }"
      role="tablist"
      :aria-label="t('chat.workbench')"
    >
      <button
        v-if="paged && offset > 0"
        type="button"
        class="bench-tab-nav"
        v-tip:top="t('chat.benchTabsPrev')"
        :aria-label="t('chat.benchTabsPrev')"
        data-testid="bench-tabs-prev"
        @click="step(-1)"
      >
        <ChevronLeft aria-hidden="true" />
      </button>
      <div class="bench-tab-strip">
        <button
          v-for="tab in shownTabs"
          :key="tab.key"
          type="button"
          role="tab"
          class="bench-tab-btn"
          :aria-selected="active === tab.key"
          :disabled="tab.disabled"
          v-tip:top="tab.disabled ? t('workshop.tabNeedsSkill') : ''"
          :data-testid="`bench-tab-${tab.key}`"
          @click="select(tab.key)"
        >
          {{ t(tab.label) }}
        </button>
      </div>
      <button
        v-if="paged"
        type="button"
        class="bench-tab-nav bench-tab-nav--next"
        v-tip:top="t('chat.benchTabsNext')"
        :aria-label="t('chat.benchTabsNext')"
        :disabled="atEnd"
        data-testid="bench-tabs-next"
        @click="step(1)"
      >
        <ChevronRight aria-hidden="true" />
      </button>
      <!-- Every tab at its natural width, invisible: what the window is measured
           against. It is hidden by hand rather than with v-if, because what it
           reports is the input to the count that decides what to draw. -->
      <div ref="measureEl" class="bench-tabs-measure" aria-hidden="true">
        <span v-for="tab in allTabs" :key="tab.key" class="bench-tab-btn">{{ t(tab.label) }}</span>
      </div>
    </div>

    <div class="chat-workbench-body">
      <OverviewTab
        v-if="active === 'overview'"
        :scope="scope"
        :records="records"
        :loading="loading"
        :error="error"
        @view="(row) => emit('view', row)"
        @cancel="(row) => emit('cancel', row)"
        @cancel-all="() => emit('cancel-all')"
        @open-plan="(plan) => emit('open-plan', plan)"
      />
      <ContextTab v-else-if="active === 'context'" :scope="scope" />
      <WorkingDirTab v-else-if="active === 'tree'" :scope="scope" :root-path="rootPath" @insert-ref="emit('insert-ref', $event)" />
      <!-- The changes tab is overridable: the chat page shows the files the
           conversation changed, the workshop shows the draft against the
           current version. -->
      <slot v-else-if="active === 'changes'" name="changes">
        <ChangedFilesTab :scope="scope" />
      </slot>
      <slot v-else :name="active" />
    </div>
  </aside>
</template>

<script setup lang="ts">
/**
 * The chat page's collapsible third column, now four base tabs — an overview
 * of what is alive and what it has spent, the context report, the working
 * directory's tree with per-node actions, and the files the conversation
 * changed — plus any extra tabs a page adds (the workshop's evaluation and
 * version tabs). The workbench opens collapsed and only what it shows is
 * loaded; the tab last used for a given conversation is remembered in the
 * browser.
 *
 * One width and one tab bar for the conversation page and the skills workshop
 * (the workshop has eight tabs): the bar shows as many tabs as the column has
 * room for — measured, not assumed, because the labels are as wide as their
 * language makes them — and the arrows beside them move that window, while the
 * selection itself always drags the window to itself.
 */
import { computed, onBeforeUnmount, onMounted, onUpdated, ref, watch } from 'vue'
import { ChevronLeft, ChevronRight, PanelRightClose } from 'lucide-vue-next'

import ContextTab from '@/components/workbench/ContextTab.vue'
import ChangedFilesTab from '@/components/workbench/ChangedFilesTab.vue'
import OverviewTab from '@/components/workbench/OverviewTab.vue'
import WorkingDirTab from '@/components/workbench/WorkingDirTab.vue'
import type { SessionPlan } from '@/composables/useSessionPlans'
import type { AgentRosterRow, WorkspaceScope } from '@/lib/api'
import { useI18n, type I18nKey } from '@/locales'

interface WorkbenchExtraTab {
  key: string
  label: I18nKey
  disabled?: boolean
}

const props = withDefaults(defineProps<{
  records: AgentRosterRow[]
  loading: boolean
  error: string
  overlay: boolean
  scope?: WorkspaceScope
  rootPath?: string
  /** Extra page-specific tabs, rendered after the base four. */
  extraTabs?: WorkbenchExtraTab[]
  /** A tab something outside the panel asked to land on. */
  requestedTab?: string | null
}>(), { scope: () => ({}), rootPath: '', extraTabs: () => [], requestedTab: null })

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'view', row: AgentRosterRow): void
  (e: 'cancel', row: AgentRosterRow): void
  (e: 'cancel-all'): void
  (e: 'insert-ref', path: string): void
  (e: 'open-plan', plan: SessionPlan): void
  /** The tab a control outside the panel asked for is on screen: the caller may
   *  forget the request now, so asking for the same tab again is a new ask. */
  (e: 'requested-tab-shown'): void
}>()

const { t } = useI18n()

const baseTabs = [
  { key: 'overview', label: 'chat.benchTabOverview' },
  { key: 'context', label: 'chat.benchTabContext' },
  { key: 'tree', label: 'chat.benchTabTree' },
  { key: 'changes', label: 'chat.benchTabChanges' },
] as const

const allTabs = computed<WorkbenchExtraTab[]>(() => [
  ...baseTabs.map((tab) => ({ key: tab.key as string, label: tab.label as I18nKey })),
  ...props.extraTabs,
])

const scopeKey = computed(() => {
  const sessionId = String(props.scope.sessionId ?? '').trim()
  if (sessionId) return `s:${sessionId}`
  const projectId = String(props.scope.projectId ?? '').trim()
  return projectId ? `p:${projectId}` : ''
})

const storageKey = computed(() => (scopeKey.value ? `forebrain.workbench.tab.${scopeKey.value}` : ''))
const active = ref<string>('overview')

function tabEnabled(key: string): boolean {
  const tab = allTabs.value.find((item) => item.key === key)
  return !!tab && !tab.disabled
}

function readStored(): string {
  if (typeof localStorage === 'undefined' || !storageKey.value) return 'overview'
  const stored = localStorage.getItem(storageKey.value)
  return stored && tabEnabled(stored) ? stored : 'overview'
}

function select(tab: string) {
  if (!tabEnabled(tab)) return
  active.value = tab
}

// How many tabs the bar shows is measured, never assumed: a label is as wide as
// its language makes it (概览 is 37px, Overview is 65px), so a fixed count either
// spills one locale out of the column or wastes a slot in the other. The bar
// shows the window that fits and the arrows beside it move that window; the
// window is an offset into the tab list, not a scroll — the tabs never scroll
// sideways, and the right arrow keeps the far edge whatever the offset.
//
// The bar's paddings come from the stylesheet as custom properties, because the
// window has to be worked out from the same numbers the layout uses — including
// the padding the bar gives up to the left arrow, which cannot be measured
// before that arrow exists.
const NAV_W = 18 // .bench-tab-nav
const GAP = 2 // the bar's and the strip's gap

const barEl = ref<HTMLElement | null>(null)
const measureEl = ref<HTMLElement | null>(null)

// The bar's width and paddings as last measured, and each tab's natural width.
const barBox = ref({ width: 0, pad: 0, arrowPad: 0 })
const tabWidths = ref<number[]>([])

function measure() {
  const bar = barEl.value
  const strip = measureEl.value
  if (!bar || !strip) return
  const widths = Array.from(strip.children, (child) => child.getBoundingClientRect().width)
  const style = getComputedStyle(bar)
  const box = {
    width: bar.clientWidth,
    pad: parseFloat(style.getPropertyValue('--bench-pad')) || 0,
    arrowPad: parseFloat(style.getPropertyValue('--bench-pad-arrow')) || 0,
  }
  // Writing the same numbers back is not a change; assigning them would render,
  // measure again, and never settle.
  if (box.width !== barBox.value.width || box.pad !== barBox.value.pad || box.arrowPad !== barBox.value.arrowPad) {
    barBox.value = box
  }
  if (widths.length !== tabWidths.value.length || widths.some((width, i) => width !== tabWidths.value[i])) {
    tabWidths.value = widths
  }
}

/** How many tabs from `start` fit in `width`. */
function fitted(start: number, width: number): number {
  let count = 0
  let used = 0
  for (let i = start; i < tabWidths.value.length; i += 1) {
    const next = count === 0 ? tabWidths.value[i] : used + GAP + tabWidths.value[i]
    if (next > width) break
    used = next
    count += 1
  }
  return count
}

// The bar with no arrows at all: what the whole list has to fit into to be shown
// in one piece. It is worked out from the un-paged padding rather than the
// measured one, so the answer cannot depend on what the last render drew.
const barRoom = computed(() => Math.max(0, barBox.value.width - 2 * barBox.value.pad))
const paged = computed(() => barBox.value.width > 0 && fitted(0, barRoom.value) < allTabs.value.length)
const offset = ref(0)

// The room the tabs get once the bar pages: the bar's inner width minus the
// arrows sharing it. The left arrow comes out of the padding the bar gave up for
// it, which is why the strip keeps the same width as the window pages along.
const stripWidth = computed(() => {
  const { width, pad, arrowPad } = barBox.value
  if (width <= 0) return 0
  const arrows = offset.value > 0 ? 2 * (NAV_W + GAP) : NAV_W + GAP
  return Math.max(0, (offset.value > 0 ? width - 2 * arrowPad : width - pad - arrowPad) - arrows)
})

const visibleCount = computed(() =>
  paged.value ? Math.max(1, fitted(offset.value, stripWidth.value)) : allTabs.value.length,
)
const shownTabs = computed(() => allTabs.value.slice(offset.value, offset.value + visibleCount.value))
// The last tab is on screen: nothing further right to page to.
const atEnd = computed(() => offset.value + visibleCount.value >= allTabs.value.length)

function step(dir: -1 | 1) {
  offset.value = Math.min(Math.max(offset.value + dir, 0), Math.max(0, allTabs.value.length - 1))
}

// Whatever moves the selection — a click on a visible tab, or a page setting it
// outside the window, like the workshop opening on 变更文件 — drags the window to
// the selected tab. The arrows only move the window, never the selection.
function revealActive() {
  offset.value = Math.min(offset.value, Math.max(0, allTabs.value.length - 1))
  if (!paged.value) {
    offset.value = 0
    return
  }
  const index = allTabs.value.findIndex((tab) => tab.key === active.value)
  if (index < 0) return
  if (index < offset.value) {
    offset.value = index
  } else if (index >= offset.value + visibleCount.value) {
    // Start the window as late as it can while still ending on the selection, so
    // a jump forward lands on the old window's far edge.
    let start = index
    while (start > 0 && fitted(start - 1, stripWidth.value) >= index - start + 2) start -= 1
    offset.value = start
  }
}

onMounted(measure)
// Every render can move the bar's padding or its labels, and only the DOM knows
// by how much — including a language switch.
onUpdated(measure)

// The column's width decides how many tabs fit, and the window's media query is
// what changes it; a resize re-renders nothing on its own.
onMounted(() => window.addEventListener('resize', measure))
onBeforeUnmount(() => window.removeEventListener('resize', measure))

// One initialiser, so the order cannot flip: the tab the conversation last read
// is the default, and a control that is *about* one tab overrides it — the
// context ring lands on the context, whatever this conversation last read
// (prototype :4346). A request is reported the moment it is shown, and the
// parent answers by clearing `requestedTab`; that clearing arrives here as
// `tab === null` and is *not* a reason to re-read storage — the tab on screen is
// the one the request asked for, and the watcher below has already written it
// down. Only a change of scope re-reads what this conversation last read.
watch([scopeKey, () => props.requestedTab], ([key, tab], previous) => {
  if (tab) {
    active.value = tabEnabled(tab) ? tab : readStored()
    revealActive()
    emit('requested-tab-shown')
    return
  }
  if (!previous || previous[0] !== key) active.value = readStored()
}, { immediate: true })

// An extra tab may become disabled (no skill yet) while it is the active one;
// fall back to the overview rather than drawing a slot with nothing in it.
watch(allTabs, () => {
  if (!tabEnabled(active.value)) active.value = 'overview'
})

// The window also follows a narrower bar: the selected tab stays on screen when
// the column shrinks under it. What re-anchors is the room the bar has, not the
// count it ends up with — that count is a property of the offset too, so
// watching it would drag the window back the moment an arrow moved it.
watch([active, allTabs, barBox, tabWidths], revealActive, { immediate: true })

watch(active, (tab) => {
  if (typeof localStorage === 'undefined' || !storageKey.value) return
  localStorage.setItem(storageKey.value, tab)
})
</script>

<style scoped>
.chat-workbench {
  display: flex;
  min-height: 0;
  flex-direction: column;
  /* The chat page and the skills workshop are the same column (owner
     2026-10-18): one width for both. */
  width: 340px;
  flex: none;
  border-left: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  /* Opening either right-hand panel is the same gesture (owner 2026-10-20): it
     slides in from the edge it lives on. Only mounting plays it — the workbench
     switching tabs is internal state and does not remount. */
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
  .chat-workbench {
    animation: none;
  }
}
.chat-workbench--overlay {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 44;
  border-left: 1px solid var(--forebrain-divider);
  box-shadow: var(--forebrain-shadow-pop);
}
/* The head is the same strip as the drawer's: one height, one set of
   paddings, so the two panels' titles and collapse icons sit in the same place
   and switching between them does not move anything. */
.chat-workbench-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 44px;
  flex: none;
  padding: 0 8px 0 16px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.chat-workbench-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.chat-workbench-close {
  display: inline-grid;
  place-items: center;
  width: 26px;
  height: 26px;
  border-radius: 7px;
  color: var(--forebrain-muted-text);
}
.chat-workbench-close svg {
  width: 15px;
  height: 15px;
}
.chat-workbench-close:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.bench-tabs {
  /* The bar's own numbers, stated once: the script works out how many tabs fit
     from these, and the padding a paged bar gives up to the left arrow cannot
     be read off the layout before that arrow is drawn. */
  --bench-pad: 10px;
  --bench-pad-arrow: 2px;
  display: flex;
  gap: 2px;
  padding: 0 var(--bench-pad);
  border-bottom: 1px solid var(--forebrain-divider);
  flex: none;
}
/* More tabs than fit: a window of as many as the column holds, packed left, the
   right arrow pinned to the far edge and the left one only once the window has
   moved — which is when the strip gives up its left padding to make room for
   it. */
.bench-tabs--paged {
  align-items: center;
  padding: 0 var(--bench-pad-arrow) 0 var(--bench-pad);
}
.bench-tabs--moved {
  padding-left: var(--bench-pad-arrow);
}
.bench-tab-strip {
  display: flex;
  gap: 2px;
  min-width: 0;
}
/* Every tab at its natural width, out of the flow and clipped to nothing: the
   widths the window is measured against. `.bench-tab-btn` is `flex: none`, so
   the labels keep their real width even inside a zero-width box. */
.bench-tabs-measure {
  position: absolute;
  top: 0;
  left: 0;
  display: flex;
  gap: 2px;
  width: 0;
  height: 0;
  overflow: hidden;
  visibility: hidden;
  pointer-events: none;
}
/* A selected tab is bold, so the window is measured at that weight: no label can
   then be wider than the room it was measured for. */
.bench-tabs-measure .bench-tab-btn {
  font-weight: 600;
}
.bench-tab-btn {
  border: 0;
  background: transparent;
  padding: 8px 6px;
  font-size: 12.5px;
  color: var(--forebrain-text-2);
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  white-space: nowrap;
  flex: none;
}
.bench-tab-btn:disabled {
  color: var(--forebrain-muted-text);
  opacity: 0.5;
  cursor: not-allowed;
}
.bench-tab-btn[aria-selected='true'] {
  color: var(--forebrain-brand-1);
  border-bottom-color: var(--forebrain-brand-1);
  font-weight: 600;
}
.bench-tab-nav {
  display: grid;
  place-items: center;
  width: 18px;
  height: 24px;
  padding: 0;
  border: 0;
  margin: 0;
  flex: none;
  color: var(--forebrain-text-2);
}
.bench-tab-nav--next {
  margin-left: auto;
}
.bench-tab-nav:hover:not(:disabled) {
  background: var(--forebrain-button-alt-bg);
}
.bench-tab-nav:disabled {
  color: var(--forebrain-muted-text);
  opacity: 0.5;
  cursor: not-allowed;
}
.bench-tab-nav svg {
  width: 15px;
  height: 15px;
}
.chat-workbench-body {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 12px;
}
/* At 340px a card's title stays on one line and what sits beside it drops to
   the next line whole; a table in a card keeps its headers on one line and its
   cells at the narrower padding (prototype `.bench-b .card-h` / `.tb`). */
.chat-workbench-body :deep(.bench-card-head) {
  flex-wrap: wrap;
  gap: 6px 8px;
}
.chat-workbench-body :deep(.bench-card-title) {
  white-space: nowrap;
}
.chat-workbench-body :deep(.forebrain-table th) {
  white-space: nowrap;
}
.chat-workbench-body :deep(.forebrain-table th),
.chat-workbench-body :deep(.forebrain-table td) {
  padding-left: 6px;
  padding-right: 6px;
}

@media (max-width: 1023px) {
  .chat-workbench {
    width: min(340px, calc(100vw - 64px));
  }
}
</style>