<script setup lang="ts">
import type { ChatStatus } from 'ai'
import type { HTMLAttributes } from 'vue'
import { InputGroupButton } from '@repo/shadcn-vue/components/ui/input-group'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { ArrowUpIcon, Loader2Icon, SquareIcon, XIcon } from 'lucide-vue-next'
import { computed } from 'vue'

type InputGroupButtonProps = InstanceType<typeof InputGroupButton>['$props']

interface Props extends /* @vue-ignore */ InputGroupButtonProps {
  class?: HTMLAttributes['class']
  status?: ChatStatus
  /** What the button does in its current state, in the reader's language. */
  label?: string
  variant?: InputGroupButtonProps['variant']
  size?: InputGroupButtonProps['size']
}

const props = withDefaults(defineProps<Props>(), {
  variant: 'default',
  size: 'icon-sm',
  label: '',
})

const emit = defineEmits<{ (e: 'stop'): void }>()

/**
 * While a run streams, the button stops it — the same interrupt Escape sends —
 * rather than submitting a turn behind the one already going. A control drawn
 * as a stop square that submits anyway is a promise the surface cannot keep.
 */
const stopping = computed(() => props.status === 'streaming' || props.status === 'submitted')

const icon = computed(() => {
  if (props.status === 'submitted') return Loader2Icon
  if (stopping.value) return SquareIcon
  if (props.status === 'error') return XIcon
  // The prototype's composer submits with an up arrow (app-preview.html :3085).
  return ArrowUpIcon
})

const iconClass = computed(() => (props.status === 'submitted' ? 'size-4 animate-spin' : 'size-4'))

function onClick(event: MouseEvent) {
  if (!stopping.value) return
  event.preventDefault()
  emit('stop')
}

const { status, size, variant, class: _, ...restProps } = props
</script>

<template>
  <InputGroupButton
    :aria-label="props.label"
    :class="cn(props.class)"
    :size="size"
    :variant="variant"
    :type="stopping ? 'button' : 'submit'"
    v-bind="restProps"
    @click="onClick"
  >
    <slot>
      <component :is="icon" :class="iconClass" />
    </slot>
  </InputGroupButton>
</template>
