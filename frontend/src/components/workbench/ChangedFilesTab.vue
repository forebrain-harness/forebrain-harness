<template>
  <div class="bench-card chg-card">
    <div class="bench-card-head">
      <span class="bench-card-title"><FileDiff class="size-3.5" aria-hidden="true" />{{ t('chat.changesTitle') }}</span>
      <span class="bench-sub">{{ t('chat.changesCount', { count: files.length }) }}</span>
    </div>

    <p v-if="!sessionId" class="bench-hint">{{ t('chat.changesEmpty') }}</p>
    <p v-else-if="loading" class="bench-hint">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="bench-hint bench-hint--error">{{ error }}</p>
    <template v-else-if="files.length">
      <div v-for="file in files" :key="file.path" class="chg-row" :data-path="file.path">
        <span class="chg-path font-mono">{{ file.path }}</span>
        <div class="chg-meta">
          <span v-if="file.created" class="chg-badge">{{ t('chat.changesNew') }}</span>
          <span v-else class="chg-stats font-mono">+{{ file.added }} −{{ file.removed }}</span>
          <div class="chg-acts">
            <Button
              v-if="!file.created"
              type="button"
              class="forebrain-btn h-auto text-xs"
              :data-testid="`chg-diff-${file.path}`"
              @click="openDiff(file.path)"
            >
              {{ t('chat.changesDiff') }}
            </Button>
            <Button
              type="button"
              class="forebrain-btn h-auto text-xs"
              :data-testid="`chg-edit-${file.path}`"
              @click="openEdit(file.path)"
            >
              {{ t('chat.changesEdit') }}
            </Button>
            <Button
              type="button"
              class="forebrain-btn h-auto text-xs"
              :data-testid="`chg-revert-${file.path}`"
              @click="askRevert(file)"
            >
              {{ t('chat.changesRevert') }}
            </Button>
          </div>
        </div>
      </div>
      <p class="bench-hint">{{ t('chat.changesFootnote') }}</p>
    </template>
    <p v-else class="bench-hint">{{ t('chat.changesEmpty') }}</p>
  </div>

  <!-- Compare: the conversation's unified diff for one file. -->
  <ForebrainModal :open="diffOpen" size="xl" :title="diffTitle" @close="diffOpen = false">
    <p v-if="diffLoading" class="bench-hint">{{ t('common.loading') }}</p>
    <p v-else-if="diffError" class="bench-hint bench-hint--error">{{ diffError }}</p>
    <div v-else class="chg-diff font-mono">
      <div v-for="(line, idx) in diffLines" :key="idx" :class="diffLineClass(line)">{{ line || ' ' }}</div>
    </div>
  </ForebrainModal>

  <!-- Edit: the shared file editor. -->
  <FileEditDialog :open="editOpen" :scope="scope" :path="editPath" @close="editOpen = false" @saved="onEdited" />

  <!-- Revert: a destructive confirmation. -->
  <ConfirmDialog
    :open="revertTarget !== null"
    :title="revertTitle"
    :message="revertMessage"
    :confirm-label="t('chat.changesRevertAction')"
    danger
    @confirm="confirmRevert"
    @cancel="revertTarget = null"
  />
</template>

<script setup lang="ts">
/**
 * The workbench's changed-files tab: the files this conversation changed with
 * a file tool, each offering compare, edit and revert. Its data is plan 033's
 * rewind ledger — the same one "rewind to here" reads — and a revert restores
 * the file before the conversation first wrote it, dropping it from the list.
 */
import { computed, ref, toRef, watch } from 'vue'
import { FileDiff } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'

import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import FileEditDialog from '@/components/workbench/FileEditDialog.vue'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import { getErrorMessage, forebrainApi, type RewindChangedFile, type WorkspaceScope } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{ scope: WorkspaceScope }>()
const { t } = useI18n()
const { refresh: refreshTree } = useWorkspaceTree(toRef(props, 'scope'))

const sessionId = computed(() => String(props.scope.sessionId ?? '').trim())
const files = ref<RewindChangedFile[]>([])
const loading = ref(false)
const error = ref('')

const diffOpen = ref(false)
const diffPath = ref('')
const diffText = ref('')
const diffLoading = ref(false)
const diffError = ref('')

const editOpen = ref(false)
const editPath = ref('')

const revertTarget = ref<RewindChangedFile | null>(null)

const diffTitle = computed(() => t('chat.changesDiffTitle', { path: diffPath.value }))
const diffLines = computed(() => (diffText.value ? diffText.value.replace(/\n$/, '').split('\n') : []))

const revertTitle = computed(() => {
  const file = revertTarget.value
  return file ? t('chat.changesRevertTitle', { name: basename(file.path) }) : ''
})
const revertMessage = computed(() => {
  const file = revertTarget.value
  if (!file) return ''
  const body = file.created ? t('chat.changesRevertNewBody') : t('chat.changesRevertBody')
  return `${file.path}\n${body}`
})

function basename(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash >= 0 ? path.slice(slash + 1) : path
}

function diffLineClass(line: string): string {
  if (line.startsWith('@@')) return 'chg-diff-hunk'
  if (line.startsWith('+++') || line.startsWith('---')) return 'chg-diff-hunk'
  if (line.startsWith('+')) return 'chg-diff-add'
  if (line.startsWith('-')) return 'chg-diff-del'
  return 'chg-diff-ctx'
}

let generation = 0
async function load() {
  const sid = sessionId.value
  if (!sid) {
    files.value = []
    return
  }
  const gen = ++generation
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.sessionFileChanges(sid)
    if (gen === generation) files.value = res
  } catch (cause) {
    if (gen === generation) {
      files.value = []
      error.value = getErrorMessage(cause)
    }
  } finally {
    if (gen === generation) loading.value = false
  }
}

async function openDiff(path: string) {
  const sid = sessionId.value
  if (!sid) return
  diffPath.value = path
  diffText.value = ''
  diffError.value = ''
  diffOpen.value = true
  diffLoading.value = true
  try {
    const res = await forebrainApi.sessionFileChangeDiff(sid, path)
    diffText.value = res.diff
  } catch (cause) {
    diffError.value = getErrorMessage(cause)
  } finally {
    diffLoading.value = false
  }
}

function openEdit(path: string) {
  editPath.value = path
  editOpen.value = true
}

function askRevert(file: RewindChangedFile) {
  revertTarget.value = file
}

async function confirmRevert() {
  const file = revertTarget.value
  const sid = sessionId.value
  if (!file || !sid) return
  try {
    await forebrainApi.sessionFileChangeRevert(sid, file.path)
    revertTarget.value = null
    await load()
    await refreshTree()
  } catch (cause) {
    void getErrorMessage(cause)
    revertTarget.value = null
  }
}

async function onEdited() {
  editOpen.value = false
  await load()
  await refreshTree()
}

watch(sessionId, () => void load(), { immediate: true })
</script>

<style scoped>
.bench-card {
  display: grid;
  gap: 8px;
  padding: 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
}
.bench-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.bench-card-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.bench-sub {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.chg-row {
  display: grid;
  gap: 4px;
  padding: 8px 0;
  border-top: 1px solid var(--forebrain-divider);
}
.chg-row:first-of-type {
  border-top: 0;
}
.chg-path {
  font-size: 12.5px;
  color: var(--forebrain-text);
  overflow-wrap: anywhere;
}
.chg-meta {
  display: flex;
  align-items: center;
  gap: 6px;
}
.chg-stats {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  font-variant-numeric: tabular-nums;
}
.chg-badge {
  font-size: 11px;
  font-weight: 500;
  padding: 1px 8px;
  border-radius: 999px;
  color: var(--forebrain-warning);
  background: var(--forebrain-warning-soft);
  border: 1px solid var(--forebrain-warning-soft);
}
.chg-acts {
  display: flex;
  gap: 4px;
  margin-left: auto;
}
.chg-diff {
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-code-bg);
  font-size: 11px;
  line-height: 1.6;
  padding: 4px 0;
  overflow-x: auto;
  white-space: pre;
}
.chg-diff > div {
  padding: 0 8px;
}
.chg-diff-add {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}
.chg-diff-del {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-danger);
}
.chg-diff-hunk {
  color: var(--forebrain-muted-text);
}
.chg-diff-ctx {
  color: var(--forebrain-text-2);
}
.bench-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  margin: 0;
}
.bench-hint--error {
  color: var(--forebrain-danger);
}
</style>
