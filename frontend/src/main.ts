import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { applyStoredAppearance } from './composables/useAppearance'
import tip from './directives/tip'
import './assets/main.css'
import 'vue-stream-markdown/index.css'
import 'vue-stream-markdown/theme.css'

const app = createApp(App)
app.use(router)
// The compact hover tip (plan 071): icon buttons and cut-off cells name
// themselves with `v-tip` instead of a browser-native title.
app.directive('tip', tip)
// The stored appearance is applied before mount so the first paint already
// carries the right brand and rail theme — no default-colour flash.
applyStoredAppearance()
// The first navigation resolves the auth question before anything mounts, so
// the app never paints a shell it is about to replace with the sign-in page.
router.isReady().then(() => app.mount('#app'))
