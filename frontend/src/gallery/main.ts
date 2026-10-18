import { createApp } from 'vue'
import ComponentGallery from './ComponentGallery.vue'
import tip from '../directives/tip'
import '../assets/main.css'

const app = createApp(ComponentGallery)
// The same compact hover tip the app registers, so the gallery shows the row
// action buttons and cut-off cells exactly as a product page does.
app.directive('tip', tip)
app.mount('#gallery')
