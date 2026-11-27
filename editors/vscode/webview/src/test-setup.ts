// Every webview test reads in Chinese: the panel's copy is the web's copy,
// and the assertions name the words a zh reader sees.
import { config } from '@vue/test-utils'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'

setLocale('zh')

// The entries register `v-tip` before mount; the harness mounts components
// directly, so it registers the same directive or every shared icon button
// warns about a directive the real app has.
config.global.directives = { ...config.global.directives, tip }
