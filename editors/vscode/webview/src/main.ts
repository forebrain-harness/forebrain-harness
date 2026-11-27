// The chat webview's entry. The transport installs first — the first request
// the app makes must already ride the host channel — then the shell mounts.
import { createApp } from 'vue'
import '@/assets/main.css'
import App from './App.vue'
import { installWebviewTransport } from './transport'

installWebviewTransport()
createApp(App).mount('#app')
