<template>
  <div class="offers">
    <div v-for="group in groups" :key="group.plan" class="ogrp" :data-plan="group.plan">
      <div class="ogh">
        {{ group.label }}
        <span>{{ t('models.chosenCount', { n: group.rows.filter((row) => chosen.includes(row.index)).length }) }}</span>
      </div>
      <div v-for="row in group.rows" :key="row.index" class="orow">
        <label class="orow__check">
          <input
            type="checkbox"
            :checked="chosen.includes(row.index)"
            :data-offer="row.index"
            @change="toggle(row.index, ($event.target as HTMLInputElement).checked)"
          />
        </label>
        <ModelSignature
          class="orow__sig"
          :model="row.offer.model"
          :base-url="row.offer.baseUrl"
          :api-path="row.offer.apiPath"
          hide-provider
        />
        <small>{{ facts(row.offer) }}</small>
      </div>
      <!-- The vendor could not decide which key, or the account has none: both
           are the person's call, and both belong under the plan they concern. -->
      <div v-if="keyChoice.length > 0 && keyChoiceFor === group.plan" class="okey">
        <CircleAlert class="size-3.5" aria-hidden="true" />
        {{ t('models.keyChoice') }}
        <Select :model-value="keyId" @update:model-value="(next) => emit('update:keyId', String(next))">
          <SelectTrigger class="okey__select" data-testid="models-key-choice">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="key in keyChoice" :key="key.id" :value="key.id">
              {{ key.name }} ••••{{ key.tail }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-else-if="needsConsent && keyChoiceFor === group.plan" class="okey">
        <CircleAlert class="size-3.5" aria-hidden="true" />
        {{ t('models.keyConsent') }}
        <Button
          type="button"
          variant="ghost"
          class="forebrain-btn forebrain-btn--ghost ml-auto h-[26px] px-2.5 text-xs"
          data-testid="models-key-consent"
          @click="emit('update:createKey', true)"
        >
          {{ t('models.keyCreate') }}
        </Button>
      </div>
      <div v-if="group.staticList" class="ogh ogh--note">{{ t('models.staticList') }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * What a finished sign-in unlocked (plan 266): one group per plan, one row per
 * model, each row the model's signature so two plans serving the same model name
 * read as two rows rather than one.
 *
 * Which key pays for a plan is the vendor's question, not the page's: a plan
 * whose pool holds several answers 409 with the keys to choose from, an empty
 * pool with a request to create one. Both are drawn under the group they
 * concern, because the choice applies to that plan only.
 */
import { computed } from 'vue'
import { CircleAlert } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'

import ModelSignature from '@/components/models/ModelSignature.vue'
import type { SubscriptionOffer } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(
  defineProps<{
    offers: SubscriptionOffer[]
    /** The chosen offers, by their index in `offers`. */
    chosen: number[]
    /** The keys a plan holds, when the gateway answered that it cannot pick. */
    keyChoice?: { id: string; name: string; tail: string }[]
    /** Which plan asked for a key (the offers' own plan id). */
    keyChoiceFor?: string
    keyId?: string
    /** The account holds no key and a write would have to create one. */
    needsConsent?: boolean
  }>(),
  { keyChoice: () => [], keyChoiceFor: '', keyId: '', needsConsent: false },
)

const emit = defineEmits<{
  'update:chosen': [indices: number[]]
  'update:keyId': [id: string]
  'update:createKey': [create: boolean]
}>()

const { t } = useI18n()

interface OfferRow {
  index: number
  offer: SubscriptionOffer
}

/** The offers by plan, in the order the vendor listed them. */
const groups = computed(() => {
  const byPlan = new Map<string, { plan: string; label: string; rows: OfferRow[]; staticList: boolean }>()
  props.offers.forEach((offer, index) => {
    const key = offer.plan || offer.planName || ''
    const group = byPlan.get(key) ?? {
      plan: key,
      label: offer.planName || offer.plan || '',
      rows: [],
      staticList: false,
    }
    group.rows.push({ index, offer })
    group.staticList = group.staticList || offer.fromStaticList
    byPlan.set(key, group)
  })
  return [...byPlan.values()]
})

/** What the vendor says about one model, in the two facts a picker shows. */
function facts(offer: SubscriptionOffer): string {
  const parts: string[] = []
  if (offer.contextWindow) parts.push(window(offer.contextWindow))
  if (offer.supportsImages) parts.push(t('models.capsImage'))
  return parts.join(' · ')
}

/** A context window as the console writes it: 1M, 256K, or the number. */
function window(tokens: number): string {
  if (tokens >= 1_000_000) return `${Math.round(tokens / 100_000) / 10}M`
  if (tokens >= 1000) return `${Math.round(tokens / 1000)}K`
  return String(tokens)
}

function toggle(index: number, checked: boolean): void {
  const kept = props.chosen.filter((entry) => entry !== index)
  emit('update:chosen', checked ? [...kept, index].sort((a, b) => a - b) : kept)
}
</script>

<style scoped>
.ogrp {
  margin-top: 14px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  overflow: hidden;
}
.ogrp:first-child {
  margin-top: 6px;
}
.ogh {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 36px;
  padding: 0 12px;
  background: var(--forebrain-bg-alt);
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 12.5px;
  font-weight: 600;
}
.ogh span {
  margin-left: auto;
  font-weight: 400;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.ogh--note {
  height: auto;
  padding: 8px 12px;
  border-bottom: 0;
  border-top: 1px solid var(--forebrain-divider);
  font-weight: 400;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.orow {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.orow:last-of-type {
  border-bottom: 0;
}
.orow__check {
  display: flex;
  padding-top: 3px;
}
.orow__check input {
  width: 15px;
  height: 15px;
  accent-color: var(--forebrain-brand-1);
}
.orow__sig {
  flex: 1;
  min-width: 0;
}
.orow small {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
}
.okey {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  border-top: 1px solid var(--forebrain-divider);
  font-size: 12.5px;
  color: var(--forebrain-text-2);
}
.okey__select {
  margin-left: auto;
  width: auto;
  height: 28px;
  font-size: 12px;
}
</style>
