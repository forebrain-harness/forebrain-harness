<template>
  <!-- The agents on this machine (plan 200 §3): the same drawer the subagents
       and the MCP panel use, and the same four sections in the same order —
       what is waiting for the person, what this directory is having edited and
       by whom, who is in this directory, and who is elsewhere. It offers no
       "send message" action: messaging is the agent's job, not the reader's. -->
  <aside
    class="peer-drawer"
    :class="{ 'peer-drawer--overlay': overlay }"
    data-testid="peer-drawer"
    :aria-label="t('chat.peerDrawerAria')"
    @keydown.esc="emit('close')"
  >
    <header class="peer-drawer-head">
      <h3>{{ t('chat.peerDrawerTitle') }}</h3>
      <button
        type="button"
        class="peer-drawer-close"
        :aria-label="t('chat.peerDrawerClose')"
        :data-testid="'peer-drawer-close'"
        @click="emit('close')"
      ><X aria-hidden="true" /></button>
    </header>

    <div class="peer-drawer-body">
      <!-- 1. What is waiting for the person. The only section that acts. -->
      <section v-if="held.length" class="peer-sec" data-testid="peer-drawer-held">
        <h4>{{ t('chat.peerHeldSection') }} <span class="count">{{ held.length }}</span></h4>
        <div v-for="row in held" :key="row.msgId" class="peer-waiting" :style="peerToneVar(row.tone)">
          <div class="peer-waiting-top">
            <PeerMark :mark="row.mark" :tone="row.tone" :name="row.fromName" />
            <b>{{ row.fromName || t('chat.peerUnknownAgent') }}</b>
            <span class="why">{{ heldReason(row.cause) }}</span>
          </div>
          <div class="peer-waiting-text">{{ row.preview }}</div>
          <div class="peer-waiting-gate">
            <button
              type="button"
              class="forebrain-btn forebrain-btn-primary"
              :data-testid="'peer-drawer-release-' + row.msgId"
              @click="emit('release', row)"
            >{{ t('chat.peerHoldRelease') }}</button>
            <button
              type="button"
              class="forebrain-btn forebrain-btn-ghost"
              :data-testid="'peer-drawer-deny-' + row.msgId"
              @click="emit('deny', row)"
            >{{ t('chat.peerHoldDeny') }}</button>
          </div>
        </div>
        <p v-if="!held.length" class="none">{{ t('chat.peerHeldNone') }}</p>
      </section>

      <!-- 2. The files this directory is having edited, and by whom. -->
      <section class="peer-sec">
        <h4>{{ t('chat.peerFilesSection') }}</h4>
        <div v-if="files.length" class="peer-tree">
          <template v-for="group in fileGroups" :key="group.dir">
            <div class="dir">{{ group.dir ? `${group.dir}/` : '/' }}</div>
            <div
              v-for="file in group.files"
              :key="group.dir + '/' + file.name"
              class="file"
              :class="{ clash: file.clash }"
              :data-testid="'peer-file-' + file.name"
            >
              <span class="fn">{{ file.name }}</span>
              <span class="marks">
                <PeerMark
                  v-for="agent in file.agents"
                  :key="agent.name"
                  :mark="agent.mark"
                  :tone="agent.tone"
                  :name="agent.name"
                  :kind="agent.self ? 'me' : 'agent'"
                />
              </span>
              <span class="ago">{{ file.clash ? t('chat.peerClash') : ago(file.lastMs) }}</span>
            </div>
          </template>
        </div>
        <p v-else class="none">{{ t('chat.peerFilesNone') }}</p>
      </section>

      <!-- 3. Who is in this directory. -->
      <section class="peer-sec">
        <h4>{{ t('chat.peerSameTreeSection') }} <span class="count">{{ sameTree.length }}</span></h4>
        <PeerAgentRow v-for="agent in sameTree" :key="agent.name" :agent="agent" :ago="agentAgo(agent)" />
        <p v-if="!sameTree.length" class="none">{{ t('chat.peerSameTreeNone') }}</p>
      </section>

      <!-- 4. Who is elsewhere, paged. -->
      <section class="peer-sec">
        <h4>{{ t('chat.peerOtherSection') }} <span class="count">{{ otherTotal }}</span></h4>
        <PeerAgentRow v-for="agent in other" :key="agent.name" :agent="agent" :ago="agentAgo(agent)" />
        <p v-if="!other.length" class="none">{{ t('chat.peerOtherNone') }}</p>
        <div v-if="otherTotal > pageSize" class="pager">
          {{ pagerLabel }}
          <button
            type="button"
            class="page-btn"
            :disabled="page <= 1"
            :aria-label="t('chat.peerPagerPrev')"
            :data-testid="'peer-pager-prev'"
            @click="emit('page-change', page - 1)"
          ><ChevronLeft aria-hidden="true" /></button>
          <button
            type="button"
            class="page-btn"
            :disabled="page * pageSize >= otherTotal"
            :aria-label="t('chat.peerPagerNext')"
            :data-testid="'peer-pager-next'"
            @click="emit('page-change', page + 1)"
          ><ChevronRight aria-hidden="true" /></button>
        </div>
      </section>
    </div>
  </aside>
</template>

<script setup lang="ts">
/**
 * The peer drawer (plan 200 §3). Four sections in a fixed order — the messages
 * waiting for the person, the files this directory is having edited, the agents
 * in this directory, and the agents elsewhere — because the first question a
 * reader asks is always "what do I have to answer", and the second is "who is
 * stepping on my work".
 *
 * Every name, mark and tone comes from the gateway; the drawer only lays them
 * out and reports the two answers a held message can be given.
 */
import { computed } from 'vue'
import { ChevronLeft, ChevronRight, X } from 'lucide-vue-next'
import PeerMark from './PeerMark.vue'
import PeerAgentRow from './PeerAgentRow.vue'
import { peerAgoLabel, peerToneVar } from '@/lib/peerMark'
import type { PeerAgentRow as PeerAgentRowData, PeerFileRow, PeerHeldRow } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  held: PeerHeldRow[]
  files: PeerFileRow[]
  sameTree: PeerAgentRowData[]
  other: PeerAgentRowData[]
  otherTotal: number
  page: number
  pageSize: number
  /** The clock the relative times are read against. */
  now: number
  overlay?: boolean
}>(), { held: () => [], files: () => [], sameTree: () => [], other: () => [], otherTotal: 0, page: 1, pageSize: 20, now: 0, overlay: false })

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'release', held: PeerHeldRow): void
  (e: 'deny', held: PeerHeldRow): void
  (e: 'page-change', page: number): void
}>()

const { t } = useI18n()

/** The file list grouped by directory, in the order the gateway sent them. */
const fileGroups = computed(() => {
  const groups: { dir: string; files: PeerFileRow[] }[] = []
  const byDir = new Map<string, PeerFileRow[]>()
  for (const file of props.files) {
    const list = byDir.get(file.dir) ?? []
    list.push(file)
    byDir.set(file.dir, list)
  }
  for (const file of props.files) {
    if (byDir.get(file.dir)?.[0] === file) groups.push({ dir: file.dir, files: byDir.get(file.dir)! })
  }
  return groups
})

const pagerLabel = computed(() => {
  const start = (props.page - 1) * props.pageSize + 1
  const end = Math.min(props.page * props.pageSize, props.otherTotal)
  return t('chat.peerPagerRange', { start, end, total: props.otherTotal })
})

function heldReason(cause: string): string {
  switch (String(cause ?? '').trim()) {
    case 'mode-mismatch':
      return t('chat.peerHoldModeMismatch')
    case 'no-mode-asserted':
      return t('chat.peerHoldNoMode')
    case 'hold-all':
      return t('chat.peerHoldAll')
    default:
      return ''
  }
}

function ago(ms: number): string {
  return peerAgoLabel(ms, props.now, t)
}

/** When the agent last said anything, for the row's own second line. */
function agentAgo(agent: PeerAgentRowData): string {
  return agent.task ? agent.task : ''
}
</script>

<style scoped>
.peer-drawer {
  position: relative;
  width: 440px;
  max-width: 100%;
  flex: none;
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-left: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  animation: forebrain-drawer-in 160ms ease-out;
}

.peer-drawer--overlay {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  z-index: 44;
  width: 100%;
  box-shadow: var(--forebrain-shadow-pop);
}

.peer-drawer-head {
  height: 52px;
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 10px 0 16px;
  border-bottom: 1px solid var(--forebrain-divider);
}

.peer-drawer-head h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--forebrain-text);
}

.peer-drawer-close {
  margin-left: auto;
  width: 30px;
  height: 30px;
  border-radius: 8px;
  border: 0;
  background: transparent;
  color: var(--forebrain-text-2);
  display: grid;
  place-items: center;
  cursor: pointer;
}

.peer-drawer-close:hover {
  background: var(--forebrain-surface-2, #f2f4f7);
}

.peer-drawer-close :deep(svg) {
  width: 16px;
  height: 16px;
}

.peer-drawer-body {
  flex: 1;
  overflow: auto;
  padding: 6px 16px 20px;
}

.peer-sec {
  padding: 14px 0 6px;
}

.peer-sec + .peer-sec {
  border-top: 1px solid var(--forebrain-divider);
}

.peer-sec h4 {
  margin: 0 0 10px;
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.peer-sec h4 .count {
  color: var(--forebrain-muted-text);
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}

.none {
  font-size: 13px;
  color: var(--forebrain-muted-text);
  padding: 2px 0 6px;
}

/* A held message in the drawer: the same question the banner asks, in the
   sender's tone. */
.peer-waiting {
  border: 1px solid var(--forebrain-brand-border);
  border-radius: 10px;
  overflow: hidden;
  margin-bottom: 8px;
}

.peer-waiting-top {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px 0;
}

.peer-waiting-top b {
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
}

.peer-waiting-top .why {
  margin-left: auto;
  color: var(--forebrain-text-2);
  font-size: 12px;
  white-space: nowrap;
}

.peer-waiting-text {
  margin: 6px 12px 10px 21px;
  padding-left: 15px;
  border-left: 2px solid var(--peer-tone);
  color: var(--forebrain-text-2);
  font-size: 13px;
  line-height: 1.65;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.peer-waiting-gate {
  display: flex;
  gap: 6px;
  padding: 8px 12px;
  background: var(--forebrain-brand-soft);
  border-top: 1px solid var(--forebrain-brand-border);
}

/* The file tree. */
.peer-tree {
  font-size: 13px;
}

.peer-tree .dir {
  color: var(--forebrain-text-2);
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  margin: 8px 0 4px;
}

.peer-tree .dir:first-child {
  margin-top: 0;
}

.peer-tree .file {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 10px;
  padding: 5px 8px 5px 14px;
  margin: 0 -8px;
  border-radius: 8px;
  position: relative;
}

.peer-tree .file::before {
  content: '';
  position: absolute;
  left: 5px;
  top: 50%;
  width: 5px;
  height: 1px;
  background: var(--forebrain-divider-strong);
}

.peer-tree .fn {
  font-family: var(--forebrain-mono-font);
  font-size: 12.5px;
  color: var(--forebrain-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.peer-tree .marks {
  display: inline-flex;
  gap: 3px;
}

.peer-tree .ago {
  color: var(--forebrain-muted-text);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  min-width: 52px;
  text-align: right;
}

/* Both of us: the one row the reader must not miss, in the same amber an
   auto-continue wait uses. */
.peer-tree .file.clash {
  background: #fffaeb;
}

.peer-tree .file.clash .ago {
  color: #b54708;
  font-weight: 500;
}

:global(.dark) .peer-tree .file.clash {
  background: rgba(181, 71, 8, 0.14);
}

:global(.dark) .peer-tree .file.clash .ago {
  color: #fec84b;
}

.pager {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 6px;
  padding: 8px 0 0;
  font-size: 12px;
  color: var(--forebrain-text-2);
  font-variant-numeric: tabular-nums;
}

.page-btn {
  width: 28px;
  height: 28px;
  border-radius: 7px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  display: grid;
  place-items: center;
  cursor: pointer;
}

.page-btn[disabled] {
  opacity: 0.4;
  cursor: default;
}

.page-btn :deep(svg) {
  width: 14px;
  height: 14px;
}

@media (prefers-reduced-motion: reduce) {
  .peer-drawer {
    animation: none;
  }
}
</style>
