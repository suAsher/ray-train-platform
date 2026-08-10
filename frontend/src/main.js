import { createApp } from 'vue'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

import App from './App.vue'
import router from './router'
import './index.css'
import { initAuth } from './auth'
import { loadSession } from './stores/session'

const app = createApp(App)

for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

app.use(router)
app.use(ElementPlus)

// Authentication is resolved before mounting so the router guard sees a
// settled session; a failure here still mounts the app, which lands the user
// on the login page rather than a blank screen.
initAuth()
  .catch(() => false)
  .then((authenticated) => (authenticated ? loadSession().catch(() => null) : null))
  .then(() => app.mount('#root'))
