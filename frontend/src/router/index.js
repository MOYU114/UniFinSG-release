import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../store/auth'
import { initialiseParticipant } from '../utils/evaluation'

const routes = [
  { path: '/', redirect: '/chat' },
  {
    path: '/signin',
    alias: '/login',
    name: 'signin',
    component: () => import('../views/LoginView.vue'),
    meta: { guestOnly: true },
  },
  {
    path: '/register',
    name: 'register',
    component: () => import('../views/RegisterView.vue'),
    meta: { guestOnly: true, localOnly: true },
  },
  {
    path: '/onboarding',
    name: 'onboarding',
    component: () => import('../views/OnboardingView.vue'),
    meta: { requiresAuth: true },
  },
  {
    path: '/',
    component: () => import('../components/layout/AppLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      { path: 'chat', name: 'chat', component: () => import('../views/ChatView.vue') },
      { path: 'chat/:sessionId', name: 'chat-session', component: () => import('../views/ChatView.vue') },
      { path: 'dashboard', name: 'dashboard', component: () => import('../views/FinanceDashboardView.vue') },
      { path: 'transactions', name: 'transactions', component: () => import('../views/TransactionsView.vue') },
      { path: 'settings', name: 'settings', component: () => import('../views/SettingsView.vue') },
    ],
  },
  // Old links to the retired assistant page land on the conversation home.
  { path: '/assistant', redirect: '/chat' },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('../views/NotFoundView.vue'),
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach(async (to) => {
  initialiseParticipant(to.fullPath.includes('?') ? to.fullPath.slice(to.fullPath.indexOf('?')) : '')
  const authStore = useAuthStore()
  if (!authStore.initialised) {
    try {
      await authStore.restoreSession()
    } catch {
      // Let the destination render its own connectivity state.
    }
  }

  if (to.meta.requiresAuth && !authStore.isAuthenticated) {
    return { name: 'signin', query: { redirect: to.fullPath } }
  }

  if (to.meta.localOnly && (import.meta.env.VITE_AUTH_MODE || 'local') === 'cognito') {
    return { name: 'signin' }
  }

  if (authStore.isAuthenticated && !authStore.user?.onboarded && to.name !== 'onboarding') {
    return { name: 'onboarding' }
  }

  if (to.meta.guestOnly && authStore.isAuthenticated) {
    return { name: authStore.user?.onboarded ? 'chat' : 'onboarding' }
  }

  if (to.name === 'onboarding' && authStore.user?.onboarded && to.query.edit !== '1') {
    return { name: 'chat' }
  }

  return true
})

export default router
