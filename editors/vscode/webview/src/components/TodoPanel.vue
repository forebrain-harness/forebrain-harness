<template>
  <div class="wv-todo" role="region" aria-label="待办" data-testid="todo-list">
    <div class="wv-todo-head" data-testid="todo-list-head">
      <span class="wv-todo-title">待办</span>
      <button
        v-if="unread.length"
        type="button"
        class="wv-todo-readall"
        data-testid="todo-read-all"
        @click="emit('read-all')"
      >
        全部已读
      </button>
      <span class="grow" />
      <span class="wv-todo-count" data-testid="todo-count">{{ countText }}</span>
      <button type="button" class="wv-icon-btn" aria-label="收起待办" data-testid="todo-close" @click="emit('close')">
        <X class="size-3.5" aria-hidden="true" />
      </button>
    </div>
    <div class="wv-todo-body">
      <div
        v-for="(item, index) in items"
        :key="`${index}-${item.content}`"
        class="wv-todo-row"
        :class="rowClass(item, index)"
        :data-status="statusOf(item)"
        data-testid="todo-row"
        :title="unread.includes(index) ? '标为已读' : undefined"
        @click="unread.includes(index) && emit('read', index)"
      >
        <span v-if="unread.includes(index)" class="wv-todo-unread-dot" aria-hidden="true" />
        <span
          class="wv-todo-state"
          role="checkbox"
          :aria-checked="checkedOf(item)"
          :aria-label="item.content"
        >
          <Check v-if="isDone(item)" class="size-3" aria-hidden="true" />
          <Loader2 v-else-if="isRunning(item)" class="size-3 wv-todo-spin" aria-hidden="true" />
          <X v-else-if="isCancelled(item)" class="size-3" aria-hidden="true" />
          <Circle v-else class="size-3" aria-hidden="true" />
        </span>
        <span class="wv-todo-text" :class="{ 'wv-todo-done': isDone(item), 'wv-todo-cancel': isCancelled(item) }">
          {{ item.content }}
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// The session's todos as a custom webview list, aligned with Claude Code's:
// one row per item, a three-state checkbox (done checked, in-flight mixed,
// pending empty), completed rows muted. Updates land as unread — a changed
// row keeps a brand dot and bold text until the reader marks it read, and
// flashes once when it arrives. This is a webview surface, not a native
// tree, and there is no PlanUpdateCard message card in the panel.
import { computed } from 'vue'
import { Check, Circle, Loader2, X } from 'lucide-vue-next'
import type { PlanUpdateData, PlanUpdateItem } from '@/composables/useChatStream'

const props = defineProps<{
  todo: PlanUpdateData
  /** The item indexes whose change the reader has not acknowledged yet. */
  unread: number[]
  /** The indexes that just changed: they flash once on arrival. */
  flash: number[]
}>()

const emit = defineEmits<{
  (e: 'read', index: number): void
  (e: 'read-all'): void
  (e: 'close'): void
}>()

// The list follows the payload: a session_todo update hands over a new object,
// and the rows and the tally must move with it.
const items = computed(() => props.todo.items)

function statusOf(item: PlanUpdateItem): string {
  return String(item.status ?? '').trim().toLowerCase()
}
function isDone(item: PlanUpdateItem): boolean {
  return ['completed', 'done'].includes(statusOf(item))
}
function isRunning(item: PlanUpdateItem): boolean {
  return ['in_progress', 'in-progress', 'active'].includes(statusOf(item))
}
function isCancelled(item: PlanUpdateItem): boolean {
  return ['cancelled', 'canceled'].includes(statusOf(item))
}
function checkedOf(item: PlanUpdateItem): 'true' | 'false' | 'mixed' {
  return isDone(item) ? 'true' : isRunning(item) ? 'mixed' : 'false'
}
function rowClass(item: PlanUpdateItem, index: number): string[] {
  return [
    isDone(item) ? 'wv-todo-row--done' : '',
    isRunning(item) ? 'wv-todo-row--run' : '',
    isCancelled(item) ? 'wv-todo-row--cancel' : '',
    props.unread.includes(index) ? 'wv-todo-row--unread' : '',
    props.flash.includes(index) ? 'wv-todo-row--flash' : '',
  ].filter(Boolean)
}

const countText = computed(() => `${items.value.filter((item) => isDone(item)).length}/${items.value.length}`)
</script>

<style scoped>
.wv-todo {
  flex: none;
  margin: 0 10px 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 0.5rem;
  background: var(--forebrain-bg);
  overflow: hidden;
}
.wv-todo-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.wv-todo-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.wv-todo-count {
  font-size: 11px;
  color: var(--forebrain-muted-text);
  font-family: var(--forebrain-mono-font);
}
.wv-todo-readall {
  border: 0;
  background: none;
  color: var(--forebrain-brand-1);
  font-size: 11px;
  cursor: pointer;
  padding: 0;
}
.wv-todo-body {
  padding: 4px 6px 6px;
  display: flex;
  flex-direction: column;
}
.wv-todo-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px;
  border-radius: 6px;
  font-size: 12.5px;
  color: var(--forebrain-text);
}
.wv-todo-row--unread {
  background: var(--forebrain-brand-soft);
  font-weight: 600;
}
.wv-todo-row--flash {
  animation: wv-todo-flash 1.2s ease-out 1;
}
@keyframes wv-todo-flash {
  0% { background: var(--forebrain-brand-soft); }
  100% { background: transparent; }
}
@media (prefers-reduced-motion: reduce) {
  .wv-todo-row--flash { animation: none; }
}
.wv-todo-unread-dot {
  width: 6px;
  height: 6px;
  border-radius: 3px;
  background: var(--forebrain-brand-1);
  flex: none;
}
.wv-todo-state {
  display: inline-grid;
  place-items: center;
  color: var(--forebrain-muted-text);
  flex: none;
}
.wv-todo-row--done .wv-todo-state { color: var(--forebrain-success); }
.wv-todo-row--run .wv-todo-state { color: var(--forebrain-brand-1); }
.wv-todo-row--cancel .wv-todo-state { color: var(--forebrain-muted-text); }
.wv-todo-spin { animation: wv-todo-spin 1.4s linear infinite; }
@keyframes wv-todo-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) {
  .wv-todo-spin { animation: none; }
}
.wv-todo-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wv-todo-done { color: var(--forebrain-muted-text); text-decoration: line-through; }
.wv-todo-cancel { color: var(--forebrain-muted-text); text-decoration: line-through; }
.grow { flex: 1; }
</style>
