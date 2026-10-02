<script setup lang="ts">
import { computed } from 'vue'
import { t } from '@/locales'

// The seal mark: an F cut into a rounded square with a small seal-stone block
// at its foot. Geometry lives only here and in the static favicons — change
// them together.
const props = withDefaults(defineProps<{ size?: number }>(), { size: 28 })

const dimension = computed(() => `${props.size}px`)
const label = computed(() => t('brand.name'))
</script>

<template>
  <svg
    class="brand-mark"
    :width="dimension"
    :height="dimension"
    viewBox="0 0 32 32"
    role="img"
    :aria-label="label"
  >
    <rect width="32" height="32" rx="7" fill="var(--brand-mark-fill)" />
    <path d="M9.5 7.5h13v4h-9v3.75h7.5v4h-7.5V24.5h-4z" fill="var(--brand-mark-glyph)" />
    <rect x="18.5" y="20.5" width="4" height="4" rx="0.6" fill="var(--brand-mark-glyph)" />
  </svg>
</template>
