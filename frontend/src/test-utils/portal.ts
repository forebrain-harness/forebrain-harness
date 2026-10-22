import { defineComponent } from 'vue'

/**
 * A Teleport stub that renders its children in place. VTU's built-in
 * `stubs: { teleport: true }` returns an empty `<teleport-stub>` for a
 * component whose Teleport sits behind another component (the library's
 * DialogPortal), which would hide every dialog's content from
 * `wrapper.find`. Rendering inline is what the shell's own `<Teleport>` did
 * before plan 096, so these component tests keep driving the same DOM they
 * always did; the real portal is covered by the e2e specs and by
 * `ForebrainModal.test.ts` / `ForebrainDrawer.test.ts`, which query the
 * document.
 */
export const InlineTeleport = defineComponent({
  name: 'Teleport',
  // A `to` (and the other Teleport props) reach this stub as attributes, and
  // Vue warns — and would put them on the DOM node — unless they are declared.
  // They are accepted and ignored: rendering in place is the whole point.
  props: {
    to: { type: [String, Object], default: undefined },
    disabled: { type: Boolean, default: false },
    defer: { type: Boolean, default: false },
    forceMount: { type: Boolean, default: false },
  },
  setup(_, { slots }) {
    return () => {
      const children = slots.default?.() as unknown
      const vnodes = Array.isArray(children)
        ? children
        : (children as { default?: () => unknown } | undefined)?.default?.()
      return vnodes as never
    }
  },
})
