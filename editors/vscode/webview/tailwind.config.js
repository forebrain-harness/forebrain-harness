// The web app's Tailwind config, with this package's sources added to the
// content scan. Reusing the same config keeps one utility vocabulary on both
// sides; the extra globs stop the compiler from pruning classes only the
// webview (or a component it reuses) writes.
import base from '../../../frontend/tailwind.config.js'

/** @type {import('tailwindcss').Config} */
export default {
  ...base,
  content: [
    './index.html',
    './src/**/*.{vue,js,ts,jsx,tsx}',
    '../../../frontend/src/**/*.{vue,js,ts,jsx,tsx}',
    '../../../ai-elements-vue/packages/elements/src/**/*.{vue,ts}',
    '../../../ai-elements-vue/packages/shadcn-vue/components/**/*.{vue,ts}',
    '../../../ai-elements-vue/packages/shadcn-vue/lib/**/*.ts',
  ],
}
