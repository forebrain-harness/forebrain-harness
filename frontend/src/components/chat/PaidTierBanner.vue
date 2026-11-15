<script setup lang="ts">
/**
 * The notice above the composer while a spent free tier offers its paid twin: the
 * same row, sent at the paid rate, or nothing. The terminal shows and answers the
 * same offer, so taking it on either surface takes it on both.
 */
import { computed } from 'vue'
import { TriangleAlert } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { formatPaidTierNotice, type PaidTierOffer } from '@/lib/paidTier'
import { useI18n } from '@/locales'

const props = defineProps<{ offer: PaidTierOffer | null }>()
const emit = defineEmits<{ (e: 'use'): void; (e: 'dismiss'): void }>()

const { t, locale } = useI18n()

const notice = computed(() => (props.offer ? formatPaidTierNotice(props.offer, locale.value) : ''))
const paidLabel = computed(() => props.offer?.displayName || props.offer?.paidModel || '')
</script>

<template>
  <div
    v-if="offer"
    class="paid-tier-banner mb-2 flex items-center gap-2 rounded-lg border px-3 py-2 text-[13px]"
    role="status"
    aria-live="polite"
    data-testid="paid-tier-banner"
  >
    <TriangleAlert class="size-4 shrink-0" aria-hidden="true" />
    <span class="min-w-0 flex-1">{{ notice }}</span>
    <Button
      type="button"
      class="forebrain-btn forebrain-btn-primary h-7 shrink-0 px-3 text-[12px]"
      data-testid="paid-tier-use"
      @click="emit('use')"
    >
      {{ t('chat.paidTier.usePaid', { model: paidLabel }) }}
    </Button>
    <Button
      type="button"
      class="forebrain-btn forebrain-btn-ghost h-7 shrink-0 px-3 text-[12px]"
      data-testid="paid-tier-dismiss"
      @click="emit('dismiss')"
    >
      {{ t('chat.paidTier.dismiss') }}
    </Button>
  </div>
</template>

<style scoped>
.paid-tier-banner {
  border-color: rgba(216, 160, 70, 0.38);
  background: rgba(216, 160, 70, 0.12);
  color: rgb(176, 120, 30);
}

:global(.dark) .paid-tier-banner {
  color: rgb(226, 176, 96);
}
</style>
