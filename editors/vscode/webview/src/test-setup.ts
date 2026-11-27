// Every webview test reads in Chinese: the panel's copy is the web's copy,
// and the assertions name the words a zh reader sees.
import { config } from '@vue/test-utils'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import { SurfaceNavigationKey } from '@/composables/provideSurfaceNavigation'

setLocale('zh')

// The entries register `v-tip` before mount; the harness mounts components
// directly, so it registers the same directive or every shared icon button
// warns about a directive the real app has.
config.global.directives = { ...config.global.directives, tip }

// The panel has no router instance: its shell publishes the navigation answers
// the cards call. A bare mount carries the same seam so the cards behave as
// they do in the panel; a test that asserts where a click goes provides its own
// recorder over this one.
config.global.provide = {
  ...config.global.provide,
  [SurfaceNavigationKey as symbol]: { openConversation: () => {}, openSettingsPage: () => {} },
}

// The thread follows the turn with the same stick-to-bottom conversation the
// web uses, and it measures its viewport with ResizeObserver — which jsdom
// does not have. The stub keeps the mount real; the scroll itself is not what
// these tests assert.
;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
}
