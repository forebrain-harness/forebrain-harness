import { reactive } from 'vue'

/** Where the tip sits relative to the element it describes. */
export type TipPlacement = 'top' | 'bottom' | 'right'

export interface TooltipState {
  anchor: HTMLElement | null
  text: string
  placement: TipPlacement
}

/**
 * The app's one tip (plan 071): the compact 20px label an icon button or a
 * collapsed rail entry names itself with. Its state is module-level, so there
 * is exactly one tip on screen no matter how many elements offer one, and the
 * single component that paints it (`AppTooltip.vue`, mounted once in `App.vue`)
 * is the only reader. Showing a tip replaces the previous one; hiding clears
 * it, so a stale tip can never outlive its anchor.
 */
const state = reactive<TooltipState>({
  anchor: null,
  text: '',
  placement: 'top',
})

export function showTip(anchor: HTMLElement | null, text: string, placement: TipPlacement = 'top'): void {
  if (!anchor || !text) {
    hideTip()
    return
  }
  state.anchor = anchor
  state.text = text
  state.placement = placement
}

export function hideTip(): void {
  state.anchor = null
  state.text = ''
}

export function useTooltipState(): TooltipState {
  return state
}
