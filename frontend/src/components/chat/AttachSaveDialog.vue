<template>
  <ForebrainModal :open="open" :title="t('chat.attachTitle')" @close="emit('cancel')">
    <div class="att-files" :aria-label="t('chat.attachFiles')">
      <div v-for="f in files" :key="f.filename" class="row">
        <ImageIcon v-if="isImage(f)" class="ic" aria-hidden="true" />
        <FileText v-else class="ic" aria-hidden="true" />
        <span class="mono nm">{{ f.filename }}</span>
        <span class="hint">{{ formatSize(f.size) }}<template v-if="f.fromClipboard"> · {{ t('chat.attachFromClipboard') }}</template></span>
      </div>
    </div>

    <div class="head-row">
      <span class="hint">{{ t('chat.attachProject') }} <b>{{ project?.name ?? '' }}</b></span>
      <button type="button" class="btn sm" data-testid="attach-new-folder" @click="startNewFolder">
        <FolderPlus class="ic" aria-hidden="true" />
        {{ t('chat.newFolder') }}
      </button>
    </div>

    <div class="att-tree" role="tree">
      <button
        type="button"
        class="tn dir"
        :class="{ on: selectedDir === '' }"
        role="treeitem"
        :aria-selected="selectedDir === ''"
        data-testid="attach-root"
        style="padding-left: 6px"
        @click="selectRow('')"
      >
        <span class="sp13" />
        <FolderOpen class="ic" aria-hidden="true" />
        <span class="nm">{{ t('chat.attachRoot') }}</span>
        <span v-if="lastDir === ''" class="badge gray">{{ t('chat.attachLast') }}</span>
      </button>
      <template v-for="row in rows" :key="row.rel">
        <button
          type="button"
          class="tn dir"
          :class="{ on: selectedDir === row.rel }"
          role="treeitem"
          :aria-selected="selectedDir === row.rel"
          :aria-expanded="expanded.has(row.rel)"
          :data-testid="`attach-dir-${row.rel}`"
          :style="{ paddingLeft: `${6 + row.depth * 14}px` }"
          @click="selectRow(row.rel)"
        >
          <ChevronRight v-if="row.hasChildren" class="ch" :class="{ down: expanded.has(row.rel) }" aria-hidden="true" />
          <span v-else class="sp13" />
          <component :is="expanded.has(row.rel) ? FolderOpen : Folder" class="ic" aria-hidden="true" />
          <span class="nm">{{ row.name }}</span>
          <span v-if="lastDir === row.rel" class="badge gray">{{ t('chat.attachLast') }}</span>
        </button>
      </template>
    </div>

    <template #footer>
      <span class="sel-path">{{ t('chat.attachTo', { dir: dirLabel }) }}</span>
      <button type="button" class="btn" data-testid="attach-cancel" @click="emit('cancel')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn pri" data-testid="attach-confirm" :disabled="!project" @click="emit('confirm', selectedDir)">
        {{ t('chat.attachSaveUpload') }}
      </button>
    </template>

    <NewFolderSheet
      v-if="newFolderOpen"
      :parent="folderSheetParent"
      :error="newFolderError"
      @create="createFolder"
      @cancel="newFolderOpen = false"
    />
  </ForebrainModal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ChevronRight, FileText, Folder, FolderOpen, FolderPlus, Image as ImageIcon } from 'lucide-vue-next'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import NewFolderSheet from '@/components/chat/NewFolderSheet.vue'
import { forebrainApi, type ProjectDirEntry } from '@/lib/api'
import { useI18n } from '@/locales'

/** One file waiting to be saved into the project. */
export interface AttachFileRow {
  filename: string
  size: number
  mediaType: string
  /** True when the file came from a paste, shown as "from clipboard". */
  fromClipboard: boolean
}

interface TreeRow {
  rel: string
  name: string
  depth: number
  hasChildren: boolean
}

/**
 * The "save to" dialog the composer opens the moment a file is attached: the
 * files to save, the project's own directory tree (directories only, loaded
 * lazily), and a place to make a new folder. It starts on the project's last
 * attach directory — falling back to the root when that directory is gone —
 * and answers the directory the user picked.
 */
const props = defineProps<{
  open: boolean
  project: { id: string; name: string } | null
  files: AttachFileRow[]
  /** The project's last attach directory, '' for the root. */
  initialDir: string
}>()

const emit = defineEmits<{
  (e: 'confirm', dir: string): void
  (e: 'cancel'): void
}>()

const { t } = useI18n()

const selectedDir = ref('')
const lastDir = ref('')
const expanded = ref<Set<string>>(new Set(['']))
const childCache = ref<Record<string, ProjectDirEntry[]>>({})
const newFolderOpen = ref(false)
const newFolderError = ref(false)

const rows = computed<TreeRow[]>(() => {
  const out: TreeRow[] = []
  const walk = (rel: string, depth: number) => {
    for (const entry of childCache.value[rel] ?? []) {
      const child = rel ? `${rel}/${entry.name}` : entry.name
      out.push({ rel: child, name: entry.name, depth, hasChildren: entry.hasChildren })
      if (expanded.value.has(child)) walk(child, depth + 1)
    }
  }
  walk('', 1)
  return out
})

const dirLabel = computed(() => (selectedDir.value ? `${selectedDir.value}/` : `${t('chat.attachRoot')} /`))
const folderSheetParent = computed(() => `${props.project?.name ?? ''} / ${dirLabel.value}`)

function isImage(f: AttachFileRow): boolean {
  return (f.mediaType ?? '').startsWith('image/')
}

function formatSize(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}

/** Load one directory's children once and cache them. */
async function loadDir(rel: string): Promise<void> {
  if (!props.project) return
  if (childCache.value[rel]) return
  const listing = await forebrainApi.projectDirs(props.project.id, rel)
  childCache.value = { ...childCache.value, [rel]: listing.entries ?? [] }
}

/** True when the directory is still there, so a remembered one can be trusted. */
async function dirExists(rel: string): Promise<boolean> {
  try {
    await loadDir(rel)
    return true
  } catch {
    return false
  }
}

function expand(rel: string) {
  expanded.value = new Set([...expanded.value, rel])
}

function selectRow(rel: string) {
  selectedDir.value = rel
  if (!rel) return
  if (expanded.value.has(rel)) return
  expand(rel)
  void loadDir(rel).catch(() => undefined)
}

function startNewFolder() {
  newFolderError.value = false
  newFolderOpen.value = true
}

async function createFolder(name: string) {
  if (!props.project) return
  const parent = selectedDir.value
  try {
    await forebrainApi.projectDirCreate(props.project.id, parent, name)
  } catch {
    newFolderError.value = true
    return
  }
  newFolderOpen.value = false
  newFolderError.value = false
  const rel = parent ? `${parent}/${name}` : name
  const existing = childCache.value[parent] ?? []
  if (!existing.some((entry) => entry.name === name)) {
    childCache.value = { ...childCache.value, [parent]: [...existing, { name, hasChildren: false }] }
  }
  expand(parent)
  selectedDir.value = rel
}

async function openAt(initial: string) {
  newFolderOpen.value = false
  newFolderError.value = false
  childCache.value = {}
  expanded.value = new Set([''])
  selectedDir.value = ''
  lastDir.value = ''
  if (!props.project) return
  try {
    await loadDir('')
  } catch {
    return
  }
  const want = (initial ?? '').trim()
  if (!want) return
  lastDir.value = want
  if (!(await dirExists(want))) {
    // The remembered directory is gone: fall back to the root.
    lastDir.value = ''
    return
  }
  let acc = ''
  for (const seg of want.split('/').filter(Boolean)) {
    acc = acc ? `${acc}/${seg}` : seg
    expand(acc)
    try {
      await loadDir(acc)
    } catch {
      break
    }
  }
  selectedDir.value = want
}

watch(
  () => props.open,
  (open) => {
    if (open) void openAt(props.initialDir)
  },
  { immediate: true },
)
</script>

<style scoped>
.att-files {
  display: grid;
  gap: 4px;
}
.att-files .row {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-text-2);
}
.att-files .row .nm {
  font-family: var(--forebrain-mono-font);
  color: var(--forebrain-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ic {
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--forebrain-muted-text);
}
.head-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.att-tree {
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  height: 250px;
  overflow: auto;
  padding: 4px;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.tn {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 6px;
  border-radius: 6px;
  font-size: 12.5px;
  color: var(--forebrain-text);
  text-align: left;
  width: 100%;
}
.tn:hover {
  background: var(--forebrain-button-alt-hover-bg);
}
.tn.on {
  background: color-mix(in srgb, var(--forebrain-brand-1) 14%, transparent);
  color: var(--forebrain-brand-1);
  font-weight: 500;
}
.tn .nm {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tn .ch {
  width: 13px;
  height: 13px;
  flex: none;
  color: var(--forebrain-muted-text);
}
.tn .ch.down {
  transform: rotate(90deg);
}
.sp13 {
  width: 13px;
  flex: none;
}
.badge.gray {
  margin-left: auto;
  height: 18px;
  padding: 0 6px;
  border-radius: 9px;
  font-size: 11px;
  display: inline-flex;
  align-items: center;
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-muted-text);
}
.sel-path {
  margin-right: auto;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  font-family: var(--forebrain-mono-font);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  font-size: 12px;
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
.btn.pri:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>
