import { computed, ref, type Ref } from 'vue'

export type BrandScheme = 'navy' | 'teal' | 'graphite'
export type RailTheme = 'dark' | 'light'

export const BRAND_SCHEMES: BrandScheme[] = ['navy', 'teal', 'graphite']

const BRAND_KEY = 'forebrain-brand'
const THEME_KEY = 'forebrain-theme'

const brand = ref<BrandScheme>('navy')
const railTheme = ref<RailTheme>('dark')

function storedBrand(): BrandScheme {
  try {
    const value = localStorage.getItem(BRAND_KEY)
    return BRAND_SCHEMES.includes(value as BrandScheme) ? (value as BrandScheme) : 'navy'
  } catch {
    return 'navy'
  }
}

function storedRailTheme(): RailTheme | null {
  try {
    const value = localStorage.getItem(THEME_KEY)
    return value === 'light' || value === 'dark' ? value : null
  } catch {
    return null
  }
}

function writeBrand(next: BrandScheme): void {
  try {
    localStorage.setItem(BRAND_KEY, next)
  } catch {
    // Storage may be unavailable (private mode, disabled); the in-memory
    // value still applies for this session.
  }
}

function applyRailTheme(next: RailTheme): void {
  document.documentElement.dataset.railTheme = next
}

function writeRailTheme(next: RailTheme): void {
  applyRailTheme(next)
  try {
    localStorage.setItem(THEME_KEY, next)
  } catch {
    // See writeBrand.
  }
}

let prefersDarkQuery: MediaQueryList | null = null

function syncSystemRailTheme(event: MediaQueryListEvent): void {
  // Only follow the system while the user has not picked a side themselves.
  if (storedRailTheme() === null) {
    railTheme.value = event.matches ? 'dark' : 'light'
    applyRailTheme(railTheme.value)
  }
}

/**
 * Apply the stored appearance before the app mounts, so the first paint
 * already carries the right brand and rail theme.
 */
export function applyStoredAppearance(): void {
  brand.value = storedBrand()
  document.documentElement.dataset.brand = brand.value
  const stored = storedRailTheme()
  if (stored !== null) {
    railTheme.value = stored
    applyRailTheme(stored)
  } else if (typeof window.matchMedia === 'function') {
    prefersDarkQuery = window.matchMedia('(prefers-color-scheme: dark)')
    railTheme.value = prefersDarkQuery.matches ? 'dark' : 'light'
    applyRailTheme(railTheme.value)
    prefersDarkQuery.addEventListener('change', syncSystemRailTheme)
  }
}

export function useAppearance(): {
  brand: Readonly<Ref<BrandScheme>>
  railTheme: Readonly<Ref<RailTheme>>
  setBrand(next: BrandScheme): void
  setRailTheme(next: RailTheme): void
  toggleRailTheme(): void
} {
  return {
    brand: computed(() => brand.value),
    railTheme: computed(() => railTheme.value),
    setBrand(next: BrandScheme) {
      brand.value = next
      document.documentElement.dataset.brand = next
      writeBrand(next)
    },
    setRailTheme(next: RailTheme) {
      railTheme.value = next
      writeRailTheme(next)
    },
    toggleRailTheme() {
      // A manual toggle is a choice: stop following the system.
      const next = railTheme.value === 'dark' ? 'light' : 'dark'
      railTheme.value = next
      writeRailTheme(next)
    },
  }
}
