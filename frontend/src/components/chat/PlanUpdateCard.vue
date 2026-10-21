<template>
  <!-- The prototype's .plan-card (:457), drawn with the library's own plan card:
       a brand hairline, a 10px corner, the page surface, 10px by 14px of
       padding — and no tally, because the progress a tally would repeat is
       already printed above the composer. -->
  <Plan class="plan-card">
    <PlanHeader class="plan-card-head">
      <PlanTitle class="plan-card-title">{{ plan.title || t('chat.updatedPlan') }}</PlanTitle>
    </PlanHeader>
    <TaskItem v-if="plan.explanation" class="plan-card-why">{{ plan.explanation }}</TaskItem>
    <div class="plan-card-items">
      <TaskItem
        v-for="item in plan.items"
        :key="item.id || item.content"
        class="plan-card-item"
        :class="{ 'now': isNow(item.status) }"
      >
        <span :class="planStatusClass(item.status)" class="plan-card-glyph">{{ planStatusGlyph(item.status) }}</span>
        <span class="plan-card-text">{{ item.content }}</span>
      </TaskItem>
    </div>
  </Plan>
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
 *
 * It is the library's plan card (`@repo/elements/plan` + `task`) with this
 * design's metrics: the structure, the title treatment and the items are the
 * components', never a second markup of the same card.
 */
import { Plan, PlanHeader, PlanTitle } from '@repo/elements/plan'
import { TaskItem } from '@repo/elements/task'
import { useI18n } from '@/locales'
import type { PlanUpdateData } from '@/composables/useChatStream'

defineProps<{ plan: PlanUpdateData }>()

const { t } = useI18n()

function isNow(status: string): boolean {
  const s = String(status ?? '').trim().toLowerCase()
  return s === 'in_progress' || s === 'in-progress' || s === 'active'
}

function planStatusGlyph(status: string): string {
  const s = String(status ?? '').trim().toLowerCase()
  if (s === 'completed' || s === 'done') return '✓'
  if (isNow(s)) return '▣'
  if (s === 'cancelled' || s === 'canceled') return '✕'
  return '□'
}

function planStatusClass(status: string): string {
  const s = String(status ?? '').trim().toLowerCase()
  if (s === 'cancelled' || s === 'canceled') return 'text-[var(--forebrain-danger)]'
  return 'text-[var(--forebrain-brand-1)]'
}
</script>

<style scoped>
/* The card's own chrome, in place of the stock Card's: the prototype draws it
   at 10px, with the hairlines of this app's palette. */
.plan-card {
  gap: 8px;
  border: 1px solid var(--forebrain-brand-border);
  border-radius: 10px;
  background: var(--forebrain-surface);
  padding: 10px 14px;
  box-shadow: none;
  color: var(--forebrain-brand-1);
  font-size: 13px;
}

.plan-card-head {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 0;
}

.plan-card-head::before {
  content: "•";
  font-size: 15px;
  line-height: 1;
}

.plan-card-title {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.2;
  color: var(--forebrain-brand-1);
}

.plan-card-why {
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text-2);
}

.plan-card-items {
  display: grid;
  gap: 5px;
}

.plan-card-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  color: var(--forebrain-brand-1);
}

.plan-card-glyph {
  flex: none;
  width: 14px;
  font-family: var(--forebrain-mono-font);
  font-size: 13px;
  line-height: 1.35;
}

.plan-card-text {
  font-size: 13px;
  line-height: 1.5;
}

.plan-card-item.now .plan-card-text {
  font-weight: 500;
}
</style>
