<template>
  <div class="mask" @click.self="emit('cancel')">
    <div class="modal fb" role="dialog" :aria-label="t('chat.folderTitle')">
      <div class="fb-bar">
        <div class="fb-nav">
          <button type="button" class="icon-btn" :aria-label="t('chat.folderBack')" :disabled="idx <= 0" @click="back">
            <ChevronLeft class="ic" aria-hidden="true" />
          </button>
          <button type="button" class="icon-btn" :aria-label="t('chat.folderForward')" :disabled="idx >= history.length - 1" @click="forward">
            <ChevronRight class="ic" aria-hidden="true" />
          </button>
          <button type="button" class="icon-btn" :title="t('chat.folderUp')" :aria-label="t('chat.folderUp')" :disabled="!parent" @click="up">
            <ArrowUp class="ic" aria-hidden="true" />
          </button>
        </div>
        <div class="fb-where">
          <button type="button" class="fb-cur" aria-haspopup="listbox" :aria-expanded="menuOpen" @click="menuOpen = !menuOpen">
            <Folder class="ic" aria-hidden="true" />
            <span class="cur-nm">{{ baseName(path) || path }}</span>
            <ChevronsUpDown class="ic2" aria-hidden="true" />
          </button>
          <div v-if="menuOpen" class="fb-menu" role="listbox">
            <button
              v-for="(p, i) in ancestors"
              :key="p"
              type="button"
              @click="go(p)"
            >
              <FolderOpen v-if="i === 0" class="ic" aria-hidden="true" />
              <Folder v-else class="ic" aria-hidden="true" />
              {{ baseName(p) || p }}
            </button>
          </div>
        </div>
        <div class="fb-search">
          <Search class="ic" aria-hidden="true" />
          <input v-model="query" :placeholder="t('chat.folderSearch')" :aria-label="t('chat.folderSearch')" />
        </div>
      </div>

      <div class="fb-main">
        <nav class="fb-side" :aria-label="t('chat.folderPlaces')">
          <div class="cap">{{ t('chat.folderPlaces') }}</div>
          <button
            v-for="place in placeRows"
            :key="place.path"
            type="button"
            :class="{ on: path === place.path }"
            @click="go(place.path)"
          >
            <component :is="place.icon" class="ic" aria-hidden="true" />
            {{ place.name }}
          </button>
          <div class="cap">{{ t('chat.folderProjects') }}</div>
          <button
            v-for="p in liveProjects"
            :key="p.id"
            type="button"
            :class="{ on: path === p.root }"
            @click="go(p.root)"
          >
            <FolderGit2 class="ic" aria-hidden="true" />
            {{ p.name }}
          </button>
        </nav>

        <div class="fb-list" role="listbox" :aria-label="baseName(path) || path">
          <div class="fb-head"><span>{{ t('chat.folderName') }}</span><span>{{ t('chat.folderModified') }}</span></div>
          <p v-if="loading" class="fb-empty">{{ t('common.loading') }}</p>
          <p v-else-if="!filteredEntries.length" class="fb-empty">{{ t('chat.folderEmpty') }}</p>
          <template v-for="entry in filteredEntries" :key="entry.name">
            <div v-if="entry.blocked" class="fb-row off" aria-disabled="true">
              <span class="nm"><FolderLock class="ic" aria-hidden="true" />{{ entry.name }}</span>
              <span class="dt">{{ t('chat.folderUnselectable') }}</span>
            </div>
            <button
              v-else
              type="button"
              class="fb-row"
              :class="{ sel: selected === childPath(entry.name) }"
              role="option"
              :aria-selected="selected === childPath(entry.name)"
              @click="select(entry.name)"
              @dblclick="enter(entry.name)"
            >
              <span class="nm">
                <FolderGit2 v-if="entry.project" class="ic" aria-hidden="true" />
                <Folder v-else class="ic" aria-hidden="true" />
                {{ entry.name }}
                <span v-if="entry.project" class="badge" style="height: 18px">{{ t('chat.folderProject') }}</span>
              </span>
              <span class="dt">{{ formatDate(entry.mtime) }}</span>
            </button>
          </template>
        </div>
      </div>

      <div class="modal-f">
        <button type="button" class="btn sm" @click="startNewFolder">
          <FolderPlus class="ic" aria-hidden="true" />
          {{ t('chat.newFolder') }}
        </button>
        <span class="sel-path">{{ selected || path }}</span>
        <button type="button" class="btn" @click="emit('cancel')">{{ t('common.cancel') }}</button>
        <button type="button" class="btn pri" @click="choose">{{ t('chat.folderChoose') }}</button>
      </div>

      <NewFolderSheet
        v-if="newFolderOpen"
        :parent="path"
        :error="newFolderError"
        @create="createFolder"
        @cancel="newFolderOpen = false"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  ArrowUp,
  ChevronLeft,
  ChevronRight,
  ChevronsUpDown,
  Download,
  FileText,
  Folder,
  FolderGit2,
  FolderLock,
  FolderOpen,
  FolderPlus,
  House,
  Monitor,
  Search,
} from 'lucide-vue-next'
import { forebrainApi, getErrorMessage, type HostDirEntry, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'
import NewFolderSheet from '@/components/chat/NewFolderSheet.vue'
import { baseName } from '@/lib/projectTarget'

/**
 * The system-style folder picker the new-chat page and the new-project form
 * open: navigate with back/forward/up or the current-path menu, filter the
 * list, pick a place or a project from the sidebar, select a folder (double
 * click or Enter enters it), and make a new folder in place. Only directories
 * are listed, and the forebrain home shows as a locked row.
 */
const props = defineProps<{
  /** Where the picker opens; defaults to the user's home directory. */
  initialPath?: string
  /** The primary agent's projects, listed in the sidebar. */
  projects?: ProjectRecord[]
}>()

const emit = defineEmits<{
  (e: 'select', path: string): void
  (e: 'cancel'): void
}>()

const { t } = useI18n()

const PLACE_ICONS: Record<string, unknown> = { 主目录: House, 桌面: Monitor, 文稿: FileText, 下载: Download }

const path = ref('')
const parent = ref('')
const ancestors = ref<string[]>([])
const places = ref<Array<{ name: string; path: string }>>([])
const entries = ref<HostDirEntry[]>([])
const loading = ref(false)
const error = ref('')
const query = ref('')
const selected = ref('')
const menuOpen = ref(false)

const history = ref<string[]>([])
const idx = ref(0)

const newFolderOpen = ref(false)
const newFolderError = ref(false)

const placeRows = computed(() =>
  places.value.map((place) => ({ ...place, icon: PLACE_ICONS[place.name] ?? Folder })),
)
const projectList = computed(() => props.projects ?? [])
const liveProjects = computed(() => projectList.value.filter((p) => !p.missing))

const filteredEntries = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return entries.value
  return entries.value.filter((entry) => entry.name.toLowerCase().includes(q))
})

function childPath(name: string): string {
  return path.value.endsWith('/') ? `${path.value}${name}` : `${path.value}/${name}`
}

async function load(target?: string, push = true) {
  loading.value = true
  error.value = ''
  try {
    const listing = await forebrainApi.hostDirs(target)
    path.value = listing.path
    parent.value = listing.parent
    ancestors.value = listing.ancestors
    places.value = listing.places
    entries.value = listing.entries
    selected.value = ''
    menuOpen.value = false
    query.value = ''
    if (push) {
      history.value = [...history.value.slice(0, idx.value + 1), listing.path]
      idx.value = history.value.length - 1
    }
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

function go(target: string) {
  menuOpen.value = false
  void load(target, true)
}

function back() {
  if (idx.value <= 0) return
  idx.value -= 1
  void load(history.value[idx.value], false)
}

function forward() {
  if (idx.value >= history.value.length - 1) return
  idx.value += 1
  void load(history.value[idx.value], false)
}

function up() {
  if (!parent.value) return
  go(parent.value)
}

function select(name: string) {
  selected.value = childPath(name)
}

function enter(name: string) {
  go(childPath(name))
}

function choose() {
  const target = selected.value || path.value
  if (!target) return
  emit('select', target)
}

function startNewFolder() {
  newFolderError.value = false
  newFolderOpen.value = true
}

async function createFolder(name: string) {
  try {
    await forebrainApi.hostDirCreate(path.value, name)
    newFolderOpen.value = false
    newFolderError.value = false
    await load(path.value, false)
    selected.value = childPath(name)
  } catch (cause) {
    // A name already taken is the sheet's own line; anything else is shown as
    // the same "already there" refusal so the sheet never dead-ends.
    newFolderError.value = true
    void cause
  }
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Backspace' && !(event.target instanceof HTMLInputElement)) {
    event.preventDefault()
    up()
  } else if (event.key === 'ArrowUp' && (event.metaKey || event.altKey)) {
    event.preventDefault()
    up()
  } else if (event.key === 'Escape') {
    emit('cancel')
  }
}

onMounted(() => {
  void load(props.initialPath || undefined, true)
  window.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

const MONTHS = ['1月', '2月', '3月', '4月', '5月', '6月', '7月', '8月', '9月', '10月', '11月', '12月']
function formatDate(sec: number): string {
  if (!sec) return '—'
  const date = new Date(sec * 1000)
  const now = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  if (date.toDateString() === now.toDateString()) return `${pad(date.getHours())}:${pad(date.getMinutes())}`
  return `${MONTHS[date.getMonth()]}${date.getDate()}日`
}
</script>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: color-mix(in srgb, var(--forebrain-text) 32%, transparent);
  z-index: 60;
  display: grid;
  place-items: center;
}
.modal {
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  box-shadow: var(--forebrain-shadow-pop);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.modal.fb {
  width: 720px;
  height: 520px;
  max-height: none;
}
.fb-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-bottom: 1px solid var(--forebrain-divider);
  background: var(--forebrain-bg-alt);
}
.fb-nav {
  display: flex;
  gap: 2px;
}
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 6px;
  color: var(--forebrain-text-2);
}
.icon-btn:hover:not(:disabled) {
  background: var(--forebrain-button-alt-hover-bg);
}
.icon-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.icon-btn .ic {
  width: 16px;
  height: 16px;
}
.fb-where {
  position: relative;
  flex: 1;
  min-width: 0;
  display: flex;
  justify-content: center;
}
.fb-cur {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  max-width: 100%;
  color: var(--forebrain-text);
}
.fb-cur .ic {
  width: 14px;
  height: 14px;
  color: var(--forebrain-brand-1);
  flex: none;
}
.fb-cur .ic2 {
  width: 14px;
  height: 14px;
  color: var(--forebrain-muted-text);
  flex: none;
}
.cur-nm {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fb-menu {
  position: absolute;
  top: calc(100% + 4px);
  left: 50%;
  transform: translateX(-50%);
  min-width: 220px;
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  box-shadow: var(--forebrain-shadow-pop);
  padding: 4px;
  z-index: 2;
  display: grid;
  gap: 1px;
}
.fb-menu button {
  display: flex;
  align-items: center;
  gap: 8px;
  border: 0;
  background: transparent;
  border-radius: 6px;
  padding: 6px 10px;
  cursor: pointer;
  font-size: 12.5px;
  text-align: left;
  color: var(--forebrain-text);
}
.fb-menu button:hover {
  background: var(--forebrain-bg-alt);
}
.fb-menu .ic {
  width: 14px;
  height: 14px;
  color: var(--forebrain-brand-1);
}
.fb-search {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 160px;
  height: 28px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  padding: 0 8px;
  background: var(--forebrain-surface);
  color: var(--forebrain-muted-text);
  box-sizing: border-box;
}
.fb-search .ic {
  width: 13px;
  height: 13px;
}
.fb-search input {
  border: 0;
  outline: none;
  background: transparent;
  font: inherit;
  font-size: 12.5px;
  min-width: 0;
  flex: 1;
  color: var(--forebrain-text);
}
.fb-main {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: 170px 1fr;
}
.fb-side {
  border-right: 1px solid var(--forebrain-divider);
  padding: 6px;
  overflow: auto;
  display: grid;
  gap: 1px;
  align-content: start;
  background: var(--forebrain-bg-alt);
}
.fb-side .cap {
  font-size: 11px;
  color: var(--forebrain-muted-text);
  padding: 8px 8px 2px;
}
.fb-side button {
  display: flex;
  align-items: center;
  gap: 8px;
  border: 0;
  background: transparent;
  border-radius: 6px;
  padding: 5px 8px;
  font-size: 12.5px;
  cursor: pointer;
  text-align: left;
  color: var(--forebrain-text-2);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.fb-side button:hover {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-text);
}
.fb-side button.on {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  font-weight: 500;
}
.fb-side .ic {
  width: 14px;
  height: 14px;
  color: var(--forebrain-brand-1);
  flex: none;
}
.fb-list {
  overflow: auto;
  display: flex;
  flex-direction: column;
}
.fb-head {
  display: grid;
  grid-template-columns: 1fr 110px;
  padding: 6px 14px;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  border-bottom: 1px solid var(--forebrain-divider);
  position: sticky;
  top: 0;
  background: var(--forebrain-surface);
}
.fb-row {
  display: grid;
  grid-template-columns: 1fr 110px;
  align-items: center;
  gap: 8px;
  padding: 5px 14px;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: default;
  font-size: 13px;
  color: var(--forebrain-text);
  width: 100%;
}
.fb-row:hover {
  background: var(--forebrain-bg-alt);
}
.fb-row.sel {
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
.fb-row .nm {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.fb-row .dt {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.fb-row.sel .dt {
  color: var(--forebrain-on-brand);
  opacity: 0.8;
}
.fb-row.off {
  color: var(--forebrain-muted-text);
}
.fb-row .ic {
  width: 16px;
  height: 16px;
  color: var(--forebrain-brand-1);
  flex: none;
}
.fb-row.sel .ic {
  color: var(--forebrain-on-brand);
}
.fb-row.off .ic {
  color: var(--forebrain-muted-text);
}
.fb-empty {
  padding: 24px;
  text-align: center;
  color: var(--forebrain-muted-text);
  font-size: 12.5px;
}
.badge {
  display: inline-flex;
  align-items: center;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  white-space: nowrap;
}
.modal-f {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--forebrain-divider);
}
.modal-f .sel-path {
  flex: 1;
  min-width: 0;
  text-align: right;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider-strong);
  background: var(--forebrain-surface);
  color: var(--forebrain-text);
  font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
  white-space: nowrap;
}
.btn.sm {
  height: 28px;
  padding: 0 10px;
}
.btn.sm .ic {
  width: 14px;
  height: 14px;
}
.btn.pri {
  background: var(--forebrain-brand-1);
  border-color: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
.btn.pri:hover {
  background: var(--forebrain-brand-hover);
  border-color: var(--forebrain-brand-hover);
}
</style>
