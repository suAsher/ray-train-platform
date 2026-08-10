import { createRouter, createWebHistory } from 'vue-router'
import Layout from '../layout/Layout.vue'
import { installAuthGuard } from '../auth/guard'

const routes = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('../views/Login/index.vue'),
    meta: { title: '登录', public: true }
  },
  {
    path: '/',
    component: Layout,
    redirect: '/job',
    children: [
      {
        path: 'job',
        name: 'JobList',
        component: () => import('../views/Job/index.vue'),
        meta: { title: '训练任务管理', requiresAuth: true }
      },
      {
        path: 'job/create',
        name: 'CreateJob',
        component: () => import('../views/Job/CreateJob.vue'),
        meta: { title: '创建训练任务', requiresAuth: true }
      },
      {
        path: 'job/detail/:id',
        name: 'JobDetail',
        component: () => import('../views/Job/JobDetail.vue'),
        meta: { title: '任务详情控制台', requiresAuth: true }
      },
      {
        path: 'quota',
        name: 'QuotaManage',
        component: () => import('../views/QuotaManage/index.vue'),
        meta: { title: '多租户与 GPU 配额隔离', requiresAuth: true, admin: true }
      },
      {
        path: 'devcenter',
        name: 'DevCenter',
        component: () => import('../views/Devcenter/index.vue'),
        meta: { title: '在线代码调试', requiresAuth: true }
      },
      {
        path: 'datacache',
        name: 'DataCache',
        component: () => import('../views/DataCache/index.vue'),
        meta: { title: '数据集与模型产物 (TOS)', requiresAuth: true }
      },
      {
        path: 'control-center',
        name: 'ControlCenter',
        component: () => import('../views/ControlCenter/index.vue'),
        meta: { title: '集群算力概览', requiresAuth: true, admin: true }
      },
      {
        path: 'devices',
        name: 'DevicesManagement',
        component: () => import('../views/DevicesManagement/index.vue'),
        meta: { title: '显卡物理矩阵', requiresAuth: true, admin: true }
      }
    ]
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

installAuthGuard(router)

export default router
