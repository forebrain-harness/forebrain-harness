<script setup lang="ts">
import type { Component, HTMLAttributes } from 'vue'
import { computed } from 'vue'
import { CollapsibleTrigger } from '@repo/shadcn-vue/components/ui/collapsible'
import { Badge } from '@repo/shadcn-vue/components/ui/badge'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { ChevronDownIcon } from 'lucide-vue-next'
import StatusBadge from './ToolStatusBadge.vue'
import { headerActionWord, headerSuffixWords } from './headerWords'

type ToolHeaderProps = {
  /**
   * The head as one line, for a payload that carries no parts. A head with
   * parts (below) is drawn from them instead.
   */
  title?: string
  /** The header in parts; when present the head is drawn from them. */
  parts?: { action: string; target?: string; suffix?: string; duration?: string }
  /** The words the parts are drawn in (the action and suffix only). */
  lang?: 'zh' | 'en'
  /** A non-zero exit code, shown left of the duration (shell). */
  exitCode?: number | null
  /** Whether the state is drawn on the activity line's node instead of here. */
  statusOnNode?: boolean
  /**
   * Kept for callers that still pass it; the head draws no tool mark any more
   * (owner 2026-10-25) — on the activity line the node says what the card is.
   */
  icon?: Component
  /** The call's state, in this surface's own vocabulary. */
  status: 'running' | 'awaiting' | 'done' | 'denied' | 'canceled' | 'error'
  /** What that state is called in the reader's language. */
  statusLabel: string
  class?: HTMLAttributes['class']
}

const props = withDefaults(defineProps<ToolHeaderProps>(), {
  title: undefined,
  parts: undefined,
  lang: 'en',
  exitCode: undefined,
  statusOnNode: false,
  icon: undefined,
})

const language = computed(() => props.lang ?? 'en')

/** The action worded for the reader; the target is never translated. */
const verb = computed(() => (props.parts ? headerActionWord(props.parts.action, language.value) : ''))
const suffix = computed(() => (props.parts?.suffix ? headerSuffixWords(props.parts.suffix, language.value) : ''))
const duration = computed(() => props.parts?.duration ?? '')

/**
 * The exit reads as a pill left of the duration (prototype .shc .tool-h .exit):
 * a red-tinted capsule with the number in tabular figures. The stock Badge's
 * destructive fills solid red, so the outline variant is taken and recolored.
 */
const exitLabel = computed(() => {
  if (props.exitCode == null || props.exitCode === 0) return ''
  return language.value === 'zh' ? `退出码 ${props.exitCode}` : `Exit ${props.exitCode}`
})
</script>

<template>
  <!-- The prototype's .tool-h: one line, always. With parts (plan 136) the
       head is the action grey, the object dark in mono, the suffix grey; the
       duration rides apart on the right, a non-zero exit left of it, the state
       (unless the activity line's node carries it) and the fold at the end.
       The padding, the alt surface and the hairline belong to the caller's
       context — on the activity line the row carries no box at all. -->
  <CollapsibleTrigger :class="cn('tool-head', props.class)" v-bind="$attrs">
    <!-- A trigger is a button, which centres its text; the summary reads from
         the left like every other line of the card. One line whatever it
         holds: what does not fit ends in an ellipsis, and the eye button
         beside the title is the way to see the whole of it. -->
    <span class="ttl">
      <template v-if="props.parts">
        <template v-if="props.parts.target">
          <span class="verb">{{ verb }}</span>
          <span class="pv cmdt tgt">{{ props.parts.target }}</span>
          <span v-if="suffix" class="sfx">{{ suffix }}</span>
        </template>
        <!-- A head with no object is its action alone, in the title's colour. -->
        <span v-else class="solo">{{ verb }}</span>
      </template>
      <span v-else class="solo">{{ props.title }}</span>
    </span>
    <slot name="after-title" />
    <Badge
      v-if="exitLabel"
      variant="outline"
      class="exit !rounded-full !border-transparent !bg-[#fef3f2] !px-2 !py-px !text-[11.5px] !font-medium !leading-[1.5] !text-[var(--forebrain-danger)] tabular-nums"
    >{{ exitLabel }}</Badge>
    <span v-if="duration" class="dur">{{ duration }}</span>
    <StatusBadge v-if="!props.statusOnNode" :status="props.status" :label="props.statusLabel" />
    <ChevronDownIcon class="tool-head-chev transition-transform group-data-[state=open]:rotate-180" aria-hidden="true" />
  </CollapsibleTrigger>
</template>

<style scoped>
/* The prototype's .tool-h (:260), sized for the head inside a card. The
   activity line overrides the padding and the surface (the row has no box). */
.tool-head {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
  padding: 8px 11px;
  background: var(--forebrain-bg-alt);
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 12px;
  text-align: left;
}

/* The prototype's .tool-h .ttl (:311): the parts on one baseline, one line,
   the object the only part that may shrink — with an ellipsis. */
.ttl {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: baseline;
  gap: 10px;
  white-space: nowrap;
  overflow: hidden;
}

.ttl .verb {
  flex: none;
  font: 13px/1.45 var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.ttl .pv {
  flex: 1 1 0;
  min-width: 48px;
  overflow: hidden;
  text-overflow: ellipsis;
  font: 11.5px/1.45 var(--forebrain-mono-font);
  color: #98a2b3;
}

.ttl .pv.cmdt {
  font-size: 12px;
  color: var(--forebrain-text);
}

.ttl .pv.tgt {
  flex: 0 1 auto;
}

.ttl .sfx {
  flex: none;
  font: 12px/1.45 var(--forebrain-ui-font);
  color: #98a2b3;
}

.ttl .solo {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  font: 13px/1.45 var(--forebrain-ui-font);
  color: var(--forebrain-text);
}

.dur {
  flex: none;
  font: 12px var(--forebrain-ui-font);
  color: #98a2b3;
  font-variant-numeric: tabular-nums;
}

.exit {
  flex: none;
}

.tool-head-chev {
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--forebrain-muted-text);
}
</style>
