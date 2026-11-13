import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { deleteUser, getCurrentUser, loginUser, logoutUser, registerUser } from '../api/users'
import { getErrorMessage } from '../utils/format'

export const useAuthStore = defineStore('auth', () => {
  const user = ref(null)
  const loading = ref(false)
  const error = ref('')
  const initialised = ref(false)
  let restorePromise = null

  const isAuthenticated = computed(() => Number.isInteger(user.value?.user_id ?? user.value?.id))
  const userId = computed(() => user.value?.user_id ?? user.value?.id)
  const authMode = computed(() => user.value?.auth_mode || 'local')

  function clear() {
    user.value = null
    error.value = ''
  }

  // In-memory compatibility helper for the hidden legacy chat and its regression tests.
  // Authentication restoration itself is always cookie-authoritative through /api/me.
  function persistProfile(profile) {
    user.value = profile
  }

  async function restoreSession({ force = false } = {}) {
    if (restorePromise && !force) return restorePromise
    restorePromise = (async () => {
      loading.value = true
      try {
        const current = await getCurrentUser()
        // Keep the legacy chat's `id`/`username` aliases until that hidden route is removed.
        user.value = { ...current, id: current.user_id, username: current.email }
        return user.value
      } catch (requestError) {
        if (requestError?.response?.status === 401) clear()
        else throw requestError
        return null
      } finally {
        loading.value = false
        initialised.value = true
        restorePromise = null
      }
    })()
    return restorePromise
  }

  async function login(credentials) {
    loading.value = true
    error.value = ''
    try {
      await loginUser(credentials)
      await restoreSession({ force: true })
      return true
    } catch (requestError) {
      error.value = getErrorMessage(requestError, 'Sign in failed. Please check your details.')
      return false
    } finally {
      loading.value = false
    }
  }

  async function register(payload) {
    loading.value = true
    error.value = ''
    try {
      await registerUser(payload)
      return true
    } catch (requestError) {
      error.value = getErrorMessage(requestError, 'Registration failed. Please try again.')
      return false
    } finally {
      loading.value = false
    }
  }

  async function markOnboarded() {
    await restoreSession({ force: true })
  }

  async function logout() {
    let logoutUrl = null
    try {
      const response = await logoutUser()
      logoutUrl = response?.logout_url || null
    } finally {
      clear()
    }
    return logoutUrl
  }

  async function removeAccount() {
    if (!userId.value) return
    await deleteUser(userId.value, 'DELETE')
    clear()
  }

  if (typeof window !== 'undefined') {
    window.addEventListener('au:unauthorized', () => {
      clear()
    })
  }

  return {
    user,
    userId,
    loading,
    error,
    initialised,
    isAuthenticated,
    authMode,
    login,
    register,
    logout,
    removeAccount,
    restoreSession,
    markOnboarded,
    clear,
    persistProfile,
  }
})
