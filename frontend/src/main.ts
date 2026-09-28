import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './assets/main.css'
import 'vue-stream-markdown/index.css'
import 'vue-stream-markdown/theme.css'

const app = createApp(App)
app.use(router)
app.mount('#app')
