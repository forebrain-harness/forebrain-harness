<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@repo/shadcn-vue/components/ui/collapsible'
import { cn } from '@repo/shadcn-vue/lib/utils'
import {
  ChevronRightIcon,
  FolderIcon,
  FolderOpenIcon,
} from 'lucide-vue-next'
import { computed } from 'vue'
import { useFileTreeContext } from './context'
import FileTreeIcon from './FileTreeIcon.vue'
import FileTreeName from './FileTreeName.vue'

interface Props extends /* @vue-ignore */ HTMLAttributes {
  path: string
  name: string
  /** Extra classes for the row's own button (the caret, mark and label). */
  buttonClass?: string
  class?: HTMLAttributes['class']
}

const props = defineProps<Props>()

const { expandedPaths, togglePath, selectedPath, onSelect } = useFileTreeContext()

const isExpanded = computed(() => expandedPaths.value.has(props.path))
const isSelected = computed(() => selectedPath.value === props.path)
</script>

<template>
  <Collapsible :open="isExpanded" @update:open="() => togglePath(props.path)">
    <div
      :class="cn('', props.class)"
      role="treeitem"
      tabindex="0"
      v-bind="$attrs"
    >
      <!-- The trigger and the caller's row controls are one line, but they are
           siblings: a button inside a button is invalid, and a browser repairs
           it by closing the outer one. -->
      <div class="flex items-center gap-1">
        <CollapsibleTrigger as-child>
          <button
            :class="
              cn(
                'flex w-full items-center gap-1 rounded px-2 py-1 text-left transition-colors hover:bg-muted/50',
                isSelected && 'bg-muted',
                props.buttonClass,
              )
            "
            type="button"
            @click="() => onSelect(props.path)"
          >
            <ChevronRightIcon
              :class="
                cn(
                  'size-4 shrink-0 text-muted-foreground transition-transform',
                  isExpanded && 'rotate-90',
                )
              "
            />
            <FileTreeIcon>
              <FolderOpenIcon v-if="isExpanded" class="size-4 text-blue-500" />
              <FolderIcon v-else class="size-4 text-blue-500" />
            </FileTreeIcon>
            <FileTreeName>{{ props.name }}</FileTreeName>
          </button>
        </CollapsibleTrigger>
        <slot name="actions" />
      </div>
      <CollapsibleContent>
        <div class="ml-4 border-l pl-2">
          <slot />
        </div>
      </CollapsibleContent>
    </div>
  </Collapsible>
</template>
