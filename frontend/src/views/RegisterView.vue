<script setup>
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../store/auth'

const router = useRouter()
const authStore = useAuthStore()

const formRef = ref()
const form = reactive({
  username: '',
  password: '',
  confirmPassword: '',
})

const rules = {
  username: [
    { required: true, message: 'Username is required', trigger: 'blur' },
    { pattern: /^[A-Za-z0-9_]{3,32}$/, message: 'Use 3–32 letters, numbers or underscores', trigger: 'blur' },
  ],
  password: [
    { required: true, message: 'Password is required', trigger: 'blur' },
    { min: 8, message: 'Password must be at least 8 characters', trigger: 'blur' },
  ],
  confirmPassword: [
    { required: true, message: 'Please confirm your password', trigger: 'blur' },
    {
      validator: (_, value, callback) => {
        if (value !== form.password) {
          callback(new Error('Passwords do not match'))
          return
        }
        callback()
      },
      trigger: ['change', 'blur'],
    },
  ],
}

const loading = computed(() => authStore.loading)

async function handleSubmit() {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }

  const success = await authStore.register({
    username: form.username.trim(),
    password: form.password,
  })

  if (!success) {
    if (authStore.error) {
      ElMessage.error(authStore.error)
    }
    return
  }

  ElMessage.success('Registration successful. Please sign in.')
  router.replace({ name: 'signin' })
}

function goToLogin() {
  router.push({ name: 'signin' })
}
</script>

<template>
  <div class="auth-wrapper">
    <div class="auth-card">
      <div class="auth-header">
        <span class="auth-brand-mark">U</span>
        <h1>Create your account</h1>
        <p class="sub-title">Start building a student budget that feels manageable.</p>
      </div>

      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        size="large"
        label-width="auto"
        class="auth-form"
        @keyup.enter="handleSubmit"
      >
        <el-form-item prop="username">
          <el-input v-model="form.username" aria-label="Username" placeholder="Username" autocomplete="username" />
        </el-form-item>

        <el-form-item prop="password">
          <el-input
            v-model="form.password"
            aria-label="Password"
            type="password"
            show-password
            placeholder="Password"
            autocomplete="new-password"
          />
        </el-form-item>

        <el-form-item prop="confirmPassword">
          <el-input
            v-model="form.confirmPassword"
            aria-label="Confirm password"
            type="password"
            show-password
            placeholder="Confirm password"
            autocomplete="new-password"
          />
        </el-form-item>

        <el-form-item>
          <el-button
            type="primary"
            class="full-width"
            :loading="loading"
            @click="handleSubmit"
          >
            Create account
          </el-button>
        </el-form-item>
      </el-form>

      <div class="auth-footer">
        <span>Already have an account?</span>
        <el-link type="primary" @click="goToLogin">Back to login</el-link>
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
  max-width: 460px;
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

@media (max-width: 520px) {
  .auth-wrapper { padding: 1rem; }
  .auth-card { padding: 2rem 1.4rem; border-radius: 22px; }
}
</style>
