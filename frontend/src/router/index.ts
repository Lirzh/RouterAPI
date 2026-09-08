import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/Login.vue'),
      meta: { public: true },
    },
    {
      path: '/',
      component: () => import('@/views/Layout.vue'),
      redirect: '/dashboard',
      children: [
        {
          path: 'dashboard',
          name: 'dashboard',
          component: () => import('@/views/Dashboard.vue'),
        },
        {
          path: 'vendors',
          name: 'vendors',
          component: () => import('@/views/Vendors.vue'),
        },
        {
          path: 'models',
          name: 'models',
          component: () => import('@/views/Models.vue'),
        },
        {
          path: 'api-keys',
          name: 'api-keys',
          component: () => import('@/views/ApiKeys.vue'),
        },
        {
          path: 'wallet',
          name: 'wallet',
          component: () => import('@/views/Wallet.vue'),
        },
        {
          path: 'logs',
          name: 'logs',
          component: () => import('@/views/Logs.vue'),
        },
        {
          path: 'audit-logs',
          name: 'audit-logs',
          component: () => import('@/views/AuditLogs.vue'),
          meta: { requireAdmin: true },
        },
        {
          path: 'users',
          name: 'users',
          component: () => import('@/views/Users.vue'),
          meta: { requireAdmin: true },
        },
        {
          path: 'settings',
          name: 'settings',
          component: () => import('@/views/Settings.vue'),
        },
      ],
    },
  ],
})

router.beforeEach((to, _from, next) => {
  const auth = useAuthStore()
  if (to.meta.public) {
    next()
    return
  }
  if (!auth.token) {
    next('/login')
    return
  }
  // admin 权限校验
  if (to.meta.requireAdmin && auth.role !== 'admin') {
    next('/dashboard')
    return
  }
  next()
})

export default router
