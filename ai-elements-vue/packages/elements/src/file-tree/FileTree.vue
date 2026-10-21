<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { computed, provide, ref, watch } from 'vue'
import { FileTreeKey } from './context'

interface Props extends /* @vue-ignore */ HTMLAttributes {
  class?: HTMLAttributes['class']
  expanded?: Set<string>
  defaultExpanded?: Set<string>
  selectedPath?: string
}

const props = withDefaults(defineProps<Props>(), {
  defaultExpanded: () => new Set(),
})

const emit = defineEmits<{
  (e: 'update:selectedPath', path: string): void
  (e: 'expandedChange', expanded: Set<string>): void
}>()

// Controlled when `expanded` is given: the caller's set is the state, and this
// component only reports the toggle. Uncontrolled otherwise (`defaultExpanded`,
// or no set at all). Keeping a copy in controlled mode is what let the caller's
// own expand state and this component's fold drift apart after the first click:
// the copy was reassigned on that click and never heard about the caller's
// later changes, so the fold stayed shut while the rows were already open.
const uncontrolledExpanded = ref(new Set(props.defaultExpanded))
const expandedSet = computed<Set<string>>(() => props.expanded ?? uncontrolledExpanded.value)

const internalSelectedPath = ref(props.selectedPath)

watch(
  () => props.selectedPath,
  (newVal) => {
    internalSelectedPath.value = newVal
  },
)

function togglePath(path: string) {
  const next = new Set(expandedSet.value)
  if (next.has(path)) {
    next.delete(path)
  }
  else {
    next.add(path)
  }
  // In controlled mode the caller owns the set: report the new value and leave
  // the state alone, so there is exactly one owner of what is open.
  if (!props.expanded) {
    uncontrolledExpanded.value = next
  }
  emit('expandedChange', next)
}

function onSelect(path: string) {
  internalSelectedPath.value = path
  emit('update:selectedPath', path)
}

provide(FileTreeKey, {
  expandedPaths: expandedSet,
  togglePath,
  selectedPath: internalSelectedPath,
  onSelect,
})
</script>

<template>
  <div
    :class="cn('rounded-lg border bg-background font-mono text-sm', props.class)"
    role="tree"
    v-bind="$attrs"
  >
    <div class="p-2">
      <slot />
    </div>
  </div>
</template>
