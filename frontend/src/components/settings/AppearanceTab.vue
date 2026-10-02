<template>
  <div class="pb-6">
    <div class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-4 py-3 text-sm text-[var(--forebrain-text)]">
      <div class="font-medium text-[var(--forebrain-text)]">{{ t('settings.appearance') }}</div>
      <div class="mt-3">
        <div class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('settings.appearanceBrand') }}</div>
        <div class="mt-2 flex gap-2" role="radiogroup" :aria-label="t('settings.appearanceBrand')">
          <button
            v-for="scheme in BRAND_SCHEMES"
            :key="scheme"
            type="button"
            role="radio"
            :aria-checked="brand === scheme"
            class="inline-flex items-center gap-2 rounded-lg border px-3 py-2 text-[13px]"
            :class="brand === scheme
              ? 'border-[var(--forebrain-brand-1)] border-2 text-[var(--forebrain-text)]'
              : 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'"
            @click="setBrand(scheme)"
          >
            <span class="brand-swatch" :data-scheme="scheme" aria-hidden="true" />
            {{ t(`appearance.${scheme}`) }}
          </button>
        </div>
      </div>
      <div class="mt-3">
        <div class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('settings.appearanceRail') }}</div>
        <div class="mt-2 flex gap-2" role="radiogroup" :aria-label="t('settings.appearanceRail')">
          <button
            v-for="option in [{ value: 'dark' as const, label: t('appearance.railDark') }, { value: 'light' as const, label: t('appearance.railLight') }]"
            :key="option.value"
            type="button"
            role="radio"
            :aria-checked="railTheme === option.value"
            class="rounded-lg border px-3 py-2 text-[13px]"
            :class="railTheme === option.value
              ? 'border-[var(--forebrain-brand-1)] border-2 text-[var(--forebrain-text)]'
              : 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'"
            @click="setRailTheme(option.value)"
          >
            {{ option.label }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { BRAND_SCHEMES, useAppearance } from '@/composables/useAppearance'
import { useI18n } from '@/locales'

const { t } = useI18n()
const { brand, railTheme, setBrand, setRailTheme } = useAppearance()
</script>

<style scoped>
.brand-swatch {
  width: 28px;
  height: 28px;
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider-strong);
}
.brand-swatch[data-scheme='navy'] {
  background: var(--forebrain-brand-1); /* navy */
}
.brand-swatch[data-scheme='teal'] {
  background: var(--brand-teal-swatch);
}
.brand-swatch[data-scheme='graphite'] {
  background: var(--brand-graphite-swatch);
}
</style>
