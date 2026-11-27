// The chat workbench's entry: locale from the editor's language, the
// transport installed before anything can ask for the gateway, the app
// mounted with the transport provided for the surfaces that steer sessions
// or ask the trust question.
import { createApp } from 'vue'
import '@/assets/main.css'
import 'vue-stream-markdown/index.css'
import 'vue-stream-markdown/theme.css'
import tip from '@/directives/tip'
import { setLocale } from '@/locales'
import App from './App.vue'
import { installWebviewTransport, TRANSPORT_KEY } from './transport'

const editorLanguage = typeof navigator !== 'undefined' ? navigator.language : ''
setLocale(editorLanguage.toLowerCase().startsWith('zh') ? 'zh' : 'en')

const transport = installWebviewTransport()
const app = createApp(App)
// The shared components name their icon buttons with `v-tip` (plan 071); the
// panel registers the same directive the web app does, or every tip is lost.
app.directive('tip', tip)
app.provide(TRANSPORT_KEY, transport).mount('#app')
