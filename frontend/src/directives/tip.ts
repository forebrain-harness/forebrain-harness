import type { Directive, DirectiveBinding } from 'vue'

import { hideTip, showTip, useTooltipState, type TipPlacement } from '@/composables/useTooltip'

/**
 * `v-tip` — an element names itself with the app's compact tip instead of the
 * browser's native `title` (whose delay and styling differ per browser):
 *
 *   v-tip="'删除'"              the text, placed above the element
 *   v-tip:right="'刷新'"        the placement: top (default), bottom or right
 *   v-tip.trunc="row.title"     only when the element's own text is cut off
 *                               (its scrollWidth is wider than its box)
 *
 * The tip appears on hover and on keyboard focus and goes away on leave,
 * blur or click (a click means the element did its job). Natively nothing is
 * set, so no second, delayed browser tooltip can appear behind this one.
 */

/** What one bound element says, kept current by `updated`. */
interface TipOptions {
  text: string
  placement: TipPlacement
  onlyWhenTruncated: boolean
}

/** The listeners one bound element installed, so `unmounted` can take them off. */
interface TipListeners {
  show: () => void
  hide: () => void
}

const options = new WeakMap<HTMLElement, TipOptions>()
const listeners = new WeakMap<HTMLElement, TipListeners>()

function isPlacement(arg: string | undefined): arg is TipPlacement {
  return arg === 'top' || arg === 'bottom' || arg === 'right'
}

function optionsOf(binding: DirectiveBinding<string | null | undefined>): TipOptions {
  return {
    text: String(binding.value ?? '').trim(),
    placement: isPlacement(binding.arg) ? binding.arg : 'top',
    onlyWhenTruncated: Boolean(binding.modifiers.trunc),
  }
}

const tip: Directive<HTMLElement, string | null | undefined> = {
  mounted(el, binding) {
    options.set(el, optionsOf(binding))
    const show = () => {
      const current = options.get(el)
      if (!current?.text) return
      if (current.onlyWhenTruncated && el.scrollWidth <= el.clientWidth) return
      showTip(el, current.text, current.placement)
    }
    const hide = () => {
      if (useTooltipState().anchor === el) hideTip()
    }
    listeners.set(el, { show, hide })
    el.addEventListener('mouseenter', show)
    el.addEventListener('focus', show)
    el.addEventListener('mouseleave', hide)
    el.addEventListener('blur', hide)
    el.addEventListener('click', hide)
  },

  updated(el, binding) {
    options.set(el, optionsOf(binding))
  },

  unmounted(el) {
    const installed = listeners.get(el)
    if (installed) {
      el.removeEventListener('mouseenter', installed.show)
      el.removeEventListener('focus', installed.show)
      el.removeEventListener('mouseleave', installed.hide)
      el.removeEventListener('blur', installed.hide)
      el.removeEventListener('click', installed.hide)
    }
    listeners.delete(el)
    options.delete(el)
    // A tip still pointing at this element would outlive its anchor.
    if (useTooltipState().anchor === el) hideTip()
  },
}

export default tip
