<template>
  <aside
    class="subagent-drawer"
    :class="{ 'subagent-drawer--overlay': overlay }"
    data-testid="subagent-drawer"
    :aria-label="t('chat.subagentDrawer')"
    @keydown.esc="onEsc"
  >
    <header class="subagent-drawer-head">
      <div class="subagent-drawer-tabs" role="tablist" :aria-label="t('chat.subagentDrawer')">
        <div
          v-for="tab in tabs"
          :key="tab.agentId"
          class="subagent-drawer-tab"
          :class="{ 'is-on': tab.agentId === active }"
          :data-testid="`subagent-tab-${tab.agentId}`"
        >
          <button
            type="button"
            role="tab"
            class="subagent-drawer-tab-main"
            :aria-selected="tab.agentId === active"
            @click="emit('select', tab.agentId)"
          >
            <Loader2 v-if="phaseOf(tab) === 'active'" class="is-run" aria-hidden="true" />
            <CheckCircle2 v-else-if="phaseOf(tab) === 'done'" class="is-ok" aria-hidden="true" />
            <XCircle v-else class="is-bad" aria-hidden="true" />
            <span class="subagent-drawer-tab-label" v-tip:bottom.trunc="tabLabel(tab)">{{ tabLabel(tab) }}</span>
            <span
              v-if="unseen(tab)"
              class="subagent-drawer-tab-new"
              :aria-label="t('chat.subagentUnseen')"
            />
          </button>
          <button
            type="button"
            class="subagent-drawer-tab-close"
            :aria-label="t('chat.subagentTabCloseAria', { name: tabLabel(tab) })"
            v-tip:bottom="t('chat.subagentTabClose')"
            :data-testid="`subagent-tab-close-${tab.agentId}`"
            @click="emit('close-tab', tab.agentId)"
          >
            <X aria-hidden="true" />
          </button>
        </div>
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
 * opened, the "+" that opens another, and the way back out. It is a shell
 * only — every tab's content is handed in by the page that owns the stream.
 *
 * It shares the right-hand column with the workbench, so it draws itself the
 * same way: a column on a wide screen, an overlay on a narrow one.
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { CheckCircle2, Loader2, PanelRightClose, Plus, X, XCircle } from 'lucide-vue-next'
import { formatRuntimeDuration, type SubagentTranscript } from '@/composables/useChatStream'
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
}>(), {
  now: undefined,
})

const emit = defineEmits<{
  (e: 'select', agentId: string): void
  (e: 'close-tab', agentId: string): void
  (e: 'open', agentId: string): void
  (e: 'collapse'): void
  (e: 'escape-key'): void
}>()

const { t } = useI18n()
const menuOpen = ref(false)

const openIds = computed(() => props.tabs.map((tab) => tab.agentId))

function phaseOf(record: SubagentTranscript) {
  return subagentPhase(record.status)
}

function tabLabel(record: SubagentTranscript): string {
  return subagentTabLabel(record)
}

/** A tab the user is not reading says so when the subagent produced something since. */
function unseen(tab: SubagentTranscript): boolean {
  if (tab.agentId === props.active) return false
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
  width: 440px;
  flex: none;
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-left: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
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
