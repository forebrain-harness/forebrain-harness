<template>
  <div
    class="rounded-xl border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-card-gradient)] px-4 py-3 text-sm text-[var(--forebrain-text)] shadow-[var(--forebrain-inset-shadow)]"
  >
    <div class="mb-2 flex items-center gap-2 font-semibold text-[var(--forebrain-brand-1)]">
      <span class="text-lg leading-none">•</span>
      <span>{{ plan.title || t('chat.updatedPlan') }}</span>
    </div>
    <div
      v-if="plan.completed != null || plan.total != null || plan.explanation"
      class="mb-2 space-y-1"
    >
      <div
        v-if="plan.completed != null || plan.total != null"
        class="text-[11px] font-mono uppercase tracking-[0.18em] text-[var(--forebrain-muted-text)]"
      >
        Tasks {{ plan.completed ?? 0 }}/{{ plan.total ?? 0 }}
      </div>
      <div
        v-if="plan.explanation"
        class="text-[13px] leading-relaxed text-[var(--forebrain-text-2)]"
      >
        {{ plan.explanation }}
      </div>
    </div>
    <ul class="space-y-1.5 pl-5">
      <li
        v-for="item in plan.items"
        :key="item.id || item.content"
        class="flex items-start gap-2"
      >
        <span :class="planStatusClass(item.status)" class="mt-[1px] font-mono text-[13px]">
          {{ planStatusGlyph(item.status) }}
        </span>
        <span
          :class="item.status === 'completed'
            ? 'text-[var(--forebrain-brand-1)]'
            : item.status === 'in_progress'
              ? 'font-medium text-[var(--forebrain-brand-2)]'
              : 'text-[var(--forebrain-brand-1)]'"
        >
          {{ item.content }}
        </span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
/**
 * The card a session_todo call is rendered as, in the conversation and in a
 * subagent's own view alike.
 *
 * A subagent keeps its own todo list, and its plan belongs to its own view. The
 * card lives here rather than inline in one view so both render the same thing:
 * when the subagent view had no plan card at all, the raw JSON result of the
 * same call was shown in its place.
 */
import { useI18n } from '@/locales'
import type { PlanUpdateData } from '@/composables/useChatStream'

defineProps<{ plan: PlanUpdateData }>()

const { t } = useI18n()

function planStatusGlyph(status: string): string {
  const s = String(status ?? '').trim().toLowerCase()
  if (s === 'completed' || s === 'done') return '✓'
  if (s === 'in_progress' || s === 'in-progress' || s === 'active') return '▣'
  if (s === 'cancelled' || s === 'canceled') return '✕'
  return '□'
}

function planStatusClass(status: string): string {
  const s = String(status ?? '').trim().toLowerCase()
  if (s === 'in_progress' || s === 'in-progress' || s === 'active') return 'text-[var(--forebrain-brand-2)]'
  if (s === 'cancelled' || s === 'canceled') return 'text-[var(--forebrain-danger)]'
  return 'text-[var(--forebrain-brand-1)]'
}
</script>
