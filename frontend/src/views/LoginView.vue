<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../store/auth'

const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()

const formRef = ref()
const form = reactive({
  username: '',
  password: '',
})

const rules = {
  username: [{ required: true, message: 'Username is required', trigger: 'blur' }],
  password: [
    { required: true, message: 'Password is required', trigger: 'blur' },
    { min: 6, message: 'Password must be at least 6 characters', trigger: 'blur' },
  ],
}

const loading = computed(() => authStore.loading)
const authMode = import.meta.env.VITE_AUTH_MODE || 'local'
const isCognito = authMode === 'cognito'

function safeRedirect(value) {
  return typeof value === 'string' && value.startsWith('/') && !value.startsWith('//') && !value.includes('\\')
    ? value
    : '/chat'
}

onMounted(() => {
  const errorCode = route.query.error
  if (errorCode) {
    const messages = {
      state_mismatch: 'Your sign-in session expired. Please start again.',
      token_invalid: 'We could not verify the sign-in response. Please try again.',
      cognito_unavailable: 'The sign-in service is temporarily unavailable.',
    }
    ElMessage.error(messages[errorCode] || 'Sign in was not completed.')
  }
})

async function handleSubmit() {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }
  const success = await authStore.login({
    username: form.username.trim(),
    password: form.password,
  })
  if (!success) {
    if (authStore.error) {
      ElMessage.error(authStore.error)
    }
    return
  }
  const redirect = safeRedirect(route.query.redirect)
  router.replace(redirect)
  ElMessage.success('Welcome back!')
}

function handleCognitoSignIn() {
  const redirect = safeRedirect(route.query.redirect)
  window.location.assign(`/api/auth/login?redirect=${encodeURIComponent(redirect)}`)
}

function goToRegister() {
  router.push({ name: 'register', query: route.query })
}
</script>

<template>
  <div class="auth-wrapper">
    <div class="auth-card">
      <div class="auth-header">
        <span class="auth-brand-mark">U</span>
        <h1>Welcome to UniFinSG</h1>
        <p class="sub-title">A clearer way to manage your student budget.</p>
      </div>

      <el-form
        v-if="!isCognito"
        ref="formRef"
        :model="form"
        :rules="rules"
        size="large"
        label-width="auto"
        class="auth-form"
        @keyup.enter="handleSubmit"
      >
        <el-form-item prop="username">
          <el-input
            v-model="form.username"
            aria-label="Username"
            placeholder="Username"
            autocomplete="username"
          />
        </el-form-item>

        <el-form-item prop="password">
          <el-input
            v-model="form.password"
            aria-label="Password"
            type="password"
            show-password
            placeholder="Password"
            autocomplete="current-password"
          />
        </el-form-item>

        <el-form-item>
          <el-button
            type="primary"
            class="full-width"
            :loading="loading"
            @click="handleSubmit"
          >
            Sign in
          </el-button>
        </el-form-item>
      </el-form>

      <el-button v-else type="primary" size="large" class="full-width hosted-signin" @click="handleCognitoSignIn">
        Continue to secure sign in
      </el-button>

      <div v-if="!isCognito" class="auth-footer">
        <span>Need an account?</span>
        <el-link type="primary" @click="goToRegister">Create one</el-link>
      </div>
    </div>
  </div>
</template>

<style scoped>
.auth-wrapper {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 2rem;
  background:
    radial-gradient(circle at 15% 12%, rgba(32, 112, 90, 0.13), transparent 30rem),
    radial-gradient(circle at 88% 88%, rgba(229, 155, 82, 0.12), transparent 28rem),
    #f6f7f2;
}

.auth-card {
  width: 100%;
  max-width: 430px;
  padding: 3rem;
  border: 1px solid #e7e8df;
  border-radius: 28px;
  background: #ffffff;
  box-shadow: 0 28px 80px rgba(45, 63, 53, 0.1);
  display: flex;
  flex-direction: column;
}

.auth-header {
  text-align: center;
  margin-bottom: 2rem;
}

.auth-brand-mark {
  width: 58px;
  height: 58px;
  margin: 0 auto 1.25rem;
  border-radius: 18px;
  display: grid;
  place-items: center;
  color: #fff;
  background: #1f6a55;
  font-family: Georgia, serif;
  font-size: 1.75rem;
  box-shadow: 0 12px 24px rgba(31, 106, 85, 0.22);
}

.auth-header h1 {
  margin: 0;
  color: #173b31;
  font-family: Georgia, 'Times New Roman', serif;
  font-size: 2rem;
  line-height: 1.15;
}

.sub-title {
  color: #6b7280;
  margin: 0.5rem 0 0;
}

.auth-form {
  margin-bottom: 1.5rem;
}

.hosted-signin {
  margin-bottom: 1rem;
}

@media (max-width: 520px) {
  .auth-wrapper { padding: 1rem; }
  .auth-card { padding: 2rem 1.4rem; border-radius: 22px; }
}

.full-width {
  width: 100%;
}

.auth-footer {
  text-align: center;
  color: #6b7280;
  display: flex;
  gap: 0.25rem;
  justify-content: center;
}
</style>
