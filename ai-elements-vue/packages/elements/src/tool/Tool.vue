<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { Collapsible } from '@repo/shadcn-vue/components/ui/collapsible'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { reactiveOmit } from '@vueuse/core'

type ToolProps = InstanceType<typeof Collapsible>['$props']

interface Props extends /* @vue-ignore */ ToolProps {
  class?: HTMLAttributes['class']
}

const props = defineProps<Props>()
const delegatedProps = reactiveOmit(props, 'class')

defineOptions({ inheritAttrs: false })
</script>

<template>
  <Collapsible
    :class="cn('group not-prose mb-4 w-full rounded-md border', props.class)"
    v-bind="{ ...delegatedProps, ...$attrs }"
  >
    <slot />
  </Collapsible>
</template>
