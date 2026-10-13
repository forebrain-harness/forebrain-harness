<template>
  <div ref="wrap" class="pj-wrap">
    <button
      ref="pill"
      type="button"
      class="pj-pill"
      :class="{ empty: !selected, shake: invalid }"
      :aria-invalid="invalid || undefined"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="project-pill"
      @click.stop="toggle"
    >
      <FolderGit2 v-if="selected && !selected.isNew" class="ic" aria-hidden="true" />
      <Folder v-else class="ic" aria-hidden="true" />
      <span class="nm">{{ selected ? selected.name : t('chat.pickProject') }}</span>
      <span v-if="selected?.isNew" class="new">{{ t('chat.newProject') }}</span>
      <ChevronDown class="ch" aria-hidden="true" />
    </button>
    <ProjectPopover
      v-if="open"
      :projects="projects"
      :selected="selected"
      @select="pick"
      @browse="browse"
      @close="open = false"
    />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronDown, Folder, FolderGit2 } from 'lucide-vue-next'
import type { ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'
import ProjectPopover from '@/components/chat/ProjectPopover.vue'
import type { ProjectTarget } from '@/lib/projectTarget'

/**
 * The project pill the new-chat composer opens with: it names the project (or
 * folder) the conversation will run in, and opens the chooser above it.
 * Nothing selected reads "选择项目"; a folder that is not yet a project reads
 * its name with a "新项目" mark.
 */
defineProps<{
  projects: ProjectRecord[]
  selected: ProjectTarget | null
  /** Set when a send was refused for want of a project: the pill is marked. */
  invalid?: boolean
}>()

const emit = defineEmits<{
  (e: 'select', target: ProjectTarget): void
  (e: 'browse'): void
}>()

const { t } = useI18n()
const open = ref(false)
const wrap = ref<HTMLElement | null>(null)
const pill = ref<HTMLButtonElement | null>(null)

function toggle() {
  open.value = !open.value
}

function pick(target: ProjectTarget) {
  open.value = false
  emit('select', target)
}

function browse() {
  open.value = false
  emit('browse')
}

function onPointerDown(event: PointerEvent) {
  if (!open.value) return
  if (wrap.value && !wrap.value.contains(event.target as Node)) open.value = false
}

onMounted(() => window.addEventListener('pointerdown', onPointerDown))
onBeforeUnmount(() => window.removeEventListener('pointerdown', onPointerDown))

defineExpose({ focus: () => pill.value?.focus() })
</script>

<style scoped>
.pj-wrap {
  position: relative;
  display: inline-flex;
  flex: none;
}
.pj-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 10px;
  border: 0;
  border-radius: 999px;
  background: var(--forebrain-bg-alt);
  font-size: 12.5px;
  color: var(--forebrain-text);
  cursor: pointer;
  flex: none;
}
.pj-pill:hover {
  background: var(--forebrain-button-alt-hover-bg);
}
.pj-pill.empty {
  color: var(--forebrain-text-2);
}
.pj-pill .nm {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 220px;
}
.pj-pill .new {
  font-size: 11px;
  color: var(--forebrain-success);
  font-weight: 600;
}
.pj-pill .ic {
  width: 14px;
  height: 14px;
}
.pj-pill .ch {
  width: 14px;
  height: 14px;
  color: var(--forebrain-muted-text);
}
.pj-pill.shake {
  animation: pj-shake 240ms ease;
}
@keyframes pj-shake {
  0%, 100% { transform: translateX(0); }
  25% { transform: translateX(-3px); }
  75% { transform: translateX(3px); }
}
</style>
