<template>
  <!-- The appearance settings, flat in their tab (owner 2026-10-18): no card
       and no title row — the tab's own name says what this is. Everything
       here is immediate: a choice applies on the spot and is stored in this
       browser. -->
  <div class="grid gap-3">
    <RadioField
      :model-value="brand"
      :label="t('settings.appearanceBrand')"
      name="appearance-brand"
      :options="brandOptions"
      :hint="t('settings.appearanceBrandHint')"
      data-testid="appearance-brand"
      @update:model-value="setBrand($event as BrandScheme)"
    />

    <RadioField
      :model-value="railTheme"
      :label="t('settings.appearanceRail')"
      name="appearance-rail"
      :options="railOptions"
      :hint="t('settings.appearanceRailHint')"
      data-testid="appearance-rail"
      @update:model-value="setRailTheme($event as RailTheme)"
    />

    <!-- The same setting the top bar's language menu writes: one locale, two
         places to change it. The label is its own element rather than a wrapper:
         the control is the library's Select, a button, and a button inside a
         `<label>` would answer one click with two. -->
    <div class="forebrain-form-field w-40">
      <Label for="appearance-language" class="block text-[12px] font-medium leading-[normal] text-[var(--forebrain-text-2)]">
        {{ t('settings.appearanceLanguage') }}
      </Label>
      <Select :model-value="locale" @update:model-value="chooseLanguage">
        <SelectTrigger
          id="appearance-language"
          class="forebrain-field !h-[34px] w-full"
          data-testid="appearance-language"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem v-for="option in languageOptions" :key="option.value" :value="option.value">{{ option.label }}</SelectItem>
        </SelectContent>
      </Select>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * Brand colour, menu bar theme and language. All three are browser settings,
 * applied at once: there is no form to submit and nothing to save.
 */
import { computed } from 'vue'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import RadioField from '@/components/common/RadioField.vue'
import { BRAND_SCHEMES, useAppearance, type BrandScheme, type RailTheme } from '@/composables/useAppearance'
import { useI18n, type I18nKey, type Locale } from '@/locales'

const { t, locale, languageOptions, setLocale } = useI18n()
const { brand, railTheme, setBrand, setRailTheme } = useAppearance()

// The three brand colours as swatches: their own fixed hexes, not the active
// theme's variables, or the navy chip would draw teal while teal is in force.
const BRAND_SWATCHES: Record<BrandScheme, string> = {
  navy: '#1f4a9e',
  teal: '#0e6e7e',
  graphite: '#1c1c1f',
}

// The default carries its own label: it is the one the app ships with.
const BRAND_LABELS: Record<BrandScheme, I18nKey> = {
  navy: 'appearance.navyDefault',
  teal: 'appearance.teal',
  graphite: 'appearance.graphite',
}

const brandOptions = computed(() => BRAND_SCHEMES.map((scheme) => ({
  value: scheme,
  label: t(BRAND_LABELS[scheme]),
  swatch: BRAND_SWATCHES[scheme],
})))

const railOptions = computed(() => [
  { value: 'dark', label: t('appearance.railDark') },
  { value: 'light', label: t('appearance.railLight') },
])

function chooseLanguage(value: unknown): void {
  setLocale(String(value ?? '') as Locale)
}
</script>
