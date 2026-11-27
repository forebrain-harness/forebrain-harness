// The Sessions view's entry: same locale and transport rules as the chat
// app — one acquireVsCodeApi, installed before anything asks the gateway.
import { createApp } from 'vue'
import '@/assets/main.css'
import { setLocale } from '@/locales'
import SessionsApp from './SessionsApp.vue'
import { installWebviewTransport, TRANSPORT_KEY } from './transport'

const editorLanguage = typeof navigator !== 'undefined' ? navigator.language : ''
setLocale(editorLanguage.toLowerCase().startsWith('zh') ? 'zh' : 'en')

const transport = installWebviewTransport()
createApp(SessionsApp).provide(TRANSPORT_KEY, transport).mount('#app')
