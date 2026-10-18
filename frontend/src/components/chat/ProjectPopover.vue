<template>
  <div class="pj-pop" role="listbox" :aria-label="t('chat.pickProject')">
    <div class="srch">
      <Search class="ics" aria-hidden="true" />
      <input
        ref="searchRef"
        v-model="query"
        :placeholder="t('chat.searchProjects')"
        :aria-label="t('chat.searchProjects')"
        @keydown.escape="emit('close')"
      />
    </div>
    <div class="grp list">
      <!-- A folder that is not yet a project leads, marked "new project". -->
      <button
        v-if="selected?.isNew"
        type="button"
        class="pj-opt"
        role="option"
        aria-selected="true"
        @click="emit('select', selected)"
      >
        <Folder class="ic" aria-hidden="true" />
        <span class="nm">{{ selected.name }}</span>
        <span class="badge ok" style="height: 18px">{{ t('chat.newProject') }}</span>
        <span class="pth">{{ selected.root }}</span>
        <Check class="ck" aria-hidden="true" />
      </button>
      <button
        v-for="p in filtered"
        :key="p.id"
        type="button"
        class="pj-opt"
        role="option"
        :aria-selected="p.id === selected?.projectId"
        :disabled="p.missing"
        v-tip:top="p.missing ? t('chat.dirMissing') : ''"
        @click="emit('select', targetFromProject(p))"
      >
        <FolderGit2 v-if="!p.missing" class="ic" aria-hidden="true" />
        <Folder v-else class="ic" aria-hidden="true" />
        <span class="nm">{{ p.name }}</span>
        <span v-if="p.missing" class="badge err" style="height: 18px">{{ t('chat.dirMissing') }}</span>
        <span v-else-if="trustOf(p) === 'unknown'" class="badge warn" style="height: 18px">{{ t('chat.trustUnknown') }}</span>
        <span class="pth">{{ p.root }}</span>
        <Check v-if="p.id === selected?.projectId" class="ck" aria-hidden="true" />
        <span v-else class="spc" />
      </button>
      <p v-if="!filtered.length && !selected?.isNew" class="pj-empty">{{ t('chat.noProjects') }}</p>
    </div>
    <div class="grp">
      <button type="button" class="pj-opt" @click="emit('browse')">
        <FolderPlus class="ic" aria-hidden="true" />
        <span class="nm">{{ t('chat.openFolder') }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { Check, Folder, FolderGit2, FolderPlus, Search } from 'lucide-vue-next'
import type { ProjectRecord } from '@/lib/api'
import { targetFromProject, type ProjectTarget } from '@/lib/projectTarget'
import { useI18n } from '@/locales'

/**
 * The project chooser behind the new-chat page's pill: a search box, the
 * primary agent's projects (the one already picked is ticked, a project whose
 * directory is gone is disabled, one never judged shows a trust badge), and
 * the entry that opens the folder picker.
 */
const props = defineProps<{
  projects: ProjectRecord[]
  selected: ProjectTarget | null
}>()

const emit = defineEmits<{
  (e: 'select', target: ProjectTarget): void
  (e: 'browse'): void
  (e: 'close'): void
}>()

const { t } = useI18n()
const query = ref('')
const searchRef = ref<HTMLInputElement | null>(null)

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return props.projects
  return props.projects.filter(
    (p) => p.name.toLowerCase().includes(q) || p.root.toLowerCase().includes(q),
  )
})

function trustOf(p: ProjectRecord) {
  return p.trustLevel ?? (p.trusted ? 'trusted' : 'unknown')
}

onMounted(() => {
  void nextTick(() => searchRef.value?.focus())
})
</script>

<style scoped>
.pj-pop {
  position: absolute;
  left: 0;
  /* It opens downwards (prototype .pj-pop): the new-chat pill sits in the
     middle of the pane now, so the room it has is below it, not above. */
  top: calc(100% + 6px);
  width: 400px;
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  box-shadow: var(--forebrain-shadow-pop);
  z-index: 6;
  overflow: hidden;
  text-align: left;
}
.srch {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-bottom: 1px solid var(--forebrain-divider);
  color: var(--forebrain-muted-text);
}
.srch .ics {
  width: 15px;
  height: 15px;
}
.srch input {
  border: 0;
  outline: none;
  flex: 1;
  font: inherit;
  font-size: 13px;
  background: transparent;
  color: var(--forebrain-text);
}
.grp {
  padding: 4px;
  display: grid;
  gap: 1px;
}
.grp + .grp {
  border-top: 1px solid var(--forebrain-divider);
}
.grp.list {
  max-height: 240px;
  overflow: auto;
}
.pj-opt {
  display: flex;
  align-items: center;
  gap: 10px;
  border: 0;
  background: transparent;
  border-radius: 8px;
  padding: 8px 10px;
  cursor: pointer;
  text-align: left;
  width: 100%;
  color: var(--forebrain-text);
  font-size: 13px;
}
.pj-opt:hover,
.pj-opt[aria-selected='true'] {
  background: var(--forebrain-bg-alt);
}
.pj-opt[disabled] {
  opacity: 0.5;
  cursor: not-allowed;
}
.pj-opt .ic {
  width: 16px;
  height: 16px;
  color: var(--forebrain-text-2);
  flex: none;
}
.pj-opt .nm {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pj-opt .ck {
  width: 15px;
  height: 15px;
  color: var(--forebrain-brand-1);
  flex: none;
}
.pj-opt .spc {
  width: 15px;
  flex: none;
}
.pj-opt .pth {
  margin-left: auto;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  color: var(--forebrain-muted-text);
  max-width: 190px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pj-empty {
  padding: 12px;
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
  white-space: nowrap;
}
.badge.ok {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}
.badge.warn {
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-warning);
}
.badge.err {
  background: var(--forebrain-bg);
  color: var(--forebrain-danger);
  border: 1px solid var(--forebrain-danger);
}
</style>
