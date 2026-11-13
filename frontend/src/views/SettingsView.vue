<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, SwitchButton } from '@element-plus/icons-vue'
import { getBudget, getProfile, saveBudget, saveProfile } from '../api/finance'
import { deleteLLMConfig, getLLMConfig, saveLLMConfig, testLLMConfig } from '../api/llmConfig'
import { useAuthStore } from '../store/auth'
import { CATEGORIES, HOUSING_OPTIONS } from '../constants/finance'
import { centsFromDollars, dollarsFromCents, formatMoney, getErrorMessage, singaporeMonth } from '../utils/format'

const authStore = useAuthStore()
const router = useRouter()
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const month = computed(() => authStore.user?.current_month || singaporeMonth())
const form = reactive({ monthlyDisposable: '', housing: '', totalBudget: '', warnPercent: 80, categoryBudgets: {} })

const modelLoading = ref(true)
const modelSaving = ref(false)
const modelTesting = ref(false)
const modelError = ref('')
const modelForm = reactive({
  mode: 'platform',
  kind: 'openai_compatible',
  baseUrl: '',
  model: '',
  apiKey: '',
  last4: '',
  configured: false,
})

const baseUrlPlaceholder = computed(() => (
  modelForm.kind === 'anthropic' ? 'https://api.anthropic.com' : 'https://api.openai.com/v1'
))

const categoryTotal = computed(() => CATEGORIES.reduce(
  (sum, item) => sum + (centsFromDollars(form.categoryBudgets[item.value]) || 0), 0,
))

async function loadSettings() {
  loading.value = true
  error.value = ''
  try {
    const [profile, budget] = await Promise.all([getProfile(authStore.userId), getBudget(authStore.userId, month.value)])
    form.monthlyDisposable = dollarsFromCents(profile.monthly_disposable_cents)
    form.housing = profile.housing
    form.totalBudget = dollarsFromCents(budget.total_cents)
    form.warnPercent = budget.warn_bp / 100
    form.categoryBudgets = Object.fromEntries(budget.categories.map((item) => [item.category, dollarsFromCents(item.budget_cents)]))
  } catch (requestError) {
    error.value = getErrorMessage(requestError, 'We could not load your settings.')
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  const disposable = centsFromDollars(form.monthlyDisposable)
  const total = centsFromDollars(form.totalBudget)
  if (!disposable || total === null) return ElMessage.warning('Enter valid monthly amounts.')
  const categories = CATEGORIES.map((item) => ({ category: item.value, budget_cents: centsFromDollars(form.categoryBudgets[item.value]) }))
  if (categories.some((item) => item.budget_cents === null)) return ElMessage.warning('Enter a valid amount for every category.')

  saving.value = true
  try {
    await saveProfile(authStore.userId, { monthly_disposable_cents: disposable, housing: form.housing, default_budget_cents: total })
    await saveBudget(authStore.userId, month.value, { total_cents: total, warn_bp: Math.round(form.warnPercent * 100), categories })
    ElMessage.success('Settings saved.')
  } catch (requestError) {
    ElMessage.error(getErrorMessage(requestError, 'We could not save your settings.'))
  } finally {
    saving.value = false
  }
}

function modelErrorMessage(requestError, fallback) {
  const upstream = requestError?.response?.data?.error?.upstream?.message
  return upstream || getErrorMessage(requestError, fallback)
}

async function loadModelConfig() {
  modelLoading.value = true
  modelError.value = ''
  if (!authStore.userId) {
    modelLoading.value = false
    return
  }
  try {
    const config = await getLLMConfig(authStore.userId)
    modelForm.configured = Boolean(config.configured)
    modelForm.mode = config.enabled ? 'byok' : 'platform'
    modelForm.kind = config.kind || 'openai_compatible'
    modelForm.baseUrl = config.base_url || ''
    modelForm.model = config.model || ''
    modelForm.last4 = config.api_key_last4 || ''
    modelForm.apiKey = ''
  } catch (requestError) {
    modelError.value = getErrorMessage(requestError, 'We could not load your model settings.')
  } finally {
    modelLoading.value = false
  }
}

function modelPayload() {
  const payload = {
    kind: modelForm.kind,
    base_url: modelForm.baseUrl.trim(),
    model: modelForm.model.trim(),
    enabled: modelForm.mode === 'byok',
  }
  const key = modelForm.apiKey.trim()
  if (key) payload.api_key = key
  return payload
}

function validateModelForm() {
  if (!modelForm.baseUrl.trim() || !modelForm.model.trim()) {
    ElMessage.warning('Enter a base URL and a model name.')
    return false
  }
  if (!modelForm.last4 && !modelForm.apiKey.trim()) {
    ElMessage.warning('Enter an API key.')
    return false
  }
  return true
}

async function testConnection() {
  if (!validateModelForm()) return
  modelTesting.value = true
  try {
    const result = await testLLMConfig(authStore.userId, modelPayload())
    if (result.ok) ElMessage.success(`Connected in ${result.latency_ms} ms.`)
    else ElMessage.error(result.error || 'The connection test failed.')
  } catch (requestError) {
    ElMessage.error(modelErrorMessage(requestError, 'The connection test failed.'))
  } finally {
    modelTesting.value = false
  }
}

async function saveModel() {
  modelSaving.value = true
  try {
    if (modelForm.mode === 'platform') {
      if (modelForm.configured) {
        await deleteLLMConfig(authStore.userId)
        modelForm.configured = false
        modelForm.last4 = ''
        modelForm.apiKey = ''
      }
      ElMessage.success('Using the platform model.')
    } else {
      if (!validateModelForm()) return
      const config = await saveLLMConfig(authStore.userId, modelPayload())
      modelForm.configured = Boolean(config.configured)
      modelForm.last4 = config.api_key_last4 || ''
      modelForm.apiKey = ''
      ElMessage.success('Model settings saved.')
    }
    window.dispatchEvent(new CustomEvent('credits:refresh'))
  } catch (requestError) {
    ElMessage.error(modelErrorMessage(requestError, 'We could not save your model settings.'))
  } finally {
    modelSaving.value = false
  }
}

async function signOut() {
  try {
    const logoutUrl = await authStore.logout()
    if (logoutUrl) window.location.assign(logoutUrl)
    else router.replace({ name: 'signin' })
  } catch (requestError) {
    ElMessage.error(getErrorMessage(requestError, 'Sign out failed.'))
  }
}

async function deleteAccount() {
  let value
  try {
    const result = await ElMessageBox.prompt(
      'This permanently removes your profile, budgets and expenses. Type DELETE to continue.',
      'Delete your account',
      { confirmButtonText: 'Delete account', cancelButtonText: 'Cancel', inputPlaceholder: 'DELETE', inputPattern: /^DELETE$/, inputErrorMessage: 'Type DELETE exactly.' },
    )
    value = result.value
  } catch {
    return
  }
  if (value !== 'DELETE') return

  try {
    await authStore.removeAccount()
    ElMessage.success('Your account has been deleted.')
    router.replace({ name: 'signin' })
  } catch (requestError) {
    ElMessage.error(getErrorMessage(requestError, 'We could not delete your account.'))
  }
}

onMounted(() => {
  loadSettings()
  loadModelConfig()
})
</script>

<template>
  <div class="settings-page">
    <header class="page-header"><div><span class="eyebrow">Make it yours</span><h1>Settings</h1><p>Update your monthly plan and account preferences.</p></div></header>

    <div v-if="error" class="state-card error-state"><h2>Settings unavailable</h2><p>{{ error }}</p><el-button @click="loadSettings">Try again</el-button></div>
    <div v-else-if="loading" class="surface-card"><el-skeleton :rows="10" animated /></div>
    <template v-else>
      <section class="settings-section surface-card">
        <div class="settings-section__heading"><span>01</span><div><h2>Monthly profile</h2><p>The basics used to shape your budget.</p></div></div>
        <div class="settings-form-grid">
          <label class="field-label"><span>Monthly disposable amount</span><div class="money-input"><span>S$</span><input v-model="form.monthlyDisposable" inputmode="decimal" /></div></label>
          <label class="field-label"><span>Housing</span><el-select v-model="form.housing" size="large"><el-option v-for="option in HOUSING_OPTIONS" :key="option.value" :label="option.label" :value="option.value" /></el-select></label>
        </div>
      </section>

      <section class="settings-section surface-card">
        <div class="settings-section__heading"><span>02</span><div><h2>Model settings</h2><p>Choose which model answers your questions.</p></div></div>
        <div v-if="modelError" class="model-inline-error">{{ modelError }}</div>
        <el-skeleton v-if="modelLoading" :rows="4" animated />
        <template v-else>
          <el-radio-group v-model="modelForm.mode" class="model-mode-group">
            <el-radio value="platform" border>Use the platform model (100 credits per day)</el-radio>
            <el-radio value="byok" border>Use my own API (no credit limit)</el-radio>
          </el-radio-group>

          <div v-if="modelForm.mode === 'byok'" class="model-form-grid">
            <label class="field-label"><span>API type</span>
              <el-select v-model="modelForm.kind" size="large">
                <el-option label="OpenAI compatible" value="openai_compatible" />
                <el-option label="Anthropic" value="anthropic" />
              </el-select>
            </label>
            <label class="field-label"><span>Base URL</span>
              <el-input v-model="modelForm.baseUrl" size="large" :placeholder="baseUrlPlaceholder" />
            </label>
            <label class="field-label"><span>Model name</span>
              <el-input v-model="modelForm.model" size="large" placeholder="gpt-4o-mini" />
            </label>
            <label class="field-label"><span>API key</span>
              <el-input
                v-model="modelForm.apiKey"
                size="large"
                type="password"
                show-password
                :placeholder="modelForm.last4 ? `••••${modelForm.last4} (leave blank to keep)` : 'Paste your API key'"
              />
            </label>
            <p class="model-form__note">Your key is encrypted and used only for your requests. Messages and images you send are forwarded to the provider you configure. The model must support tool calling, and vision if you send screenshots.</p>
            <div class="model-form__actions">
              <el-button :loading="modelTesting" @click="testConnection">Test connection</el-button>
            </div>
          </div>
          <p v-else class="model-form__note">The platform model is included with 100 credits per day. Switch to your own API if you want unlimited use with your own provider key.</p>

          <div class="settings-save">
            <el-button type="primary" size="large" :loading="modelSaving" @click="saveModel">Save model settings</el-button>
          </div>
        </template>
      </section>

      <section class="settings-section surface-card">
        <div class="settings-section__heading"><span>03</span><div><h2>{{ month }} budget</h2><p>Adjust the current month without changing past months.</p></div></div>
        <div class="settings-form-grid settings-form-grid--budget">
          <label class="field-label"><span>Total monthly budget</span><div class="money-input"><span>S$</span><input v-model="form.totalBudget" inputmode="decimal" /></div></label>
          <label class="field-label"><span>Warn me at {{ form.warnPercent }}%</span><el-slider v-model="form.warnPercent" :min="50" :max="100" :step="5" show-stops /></label>
        </div>
        <div class="settings-category-grid">
          <label v-for="item in CATEGORIES" :key="item.value" class="category-budget-field">
            <span class="category-icon" :style="{ background: item.soft }">{{ item.icon }}</span>
            <span class="category-budget-field__copy"><strong>{{ item.label }}</strong><small>Monthly limit</small></span>
            <div class="mini-money-input"><span>S$</span><input v-model="form.categoryBudgets[item.value]" inputmode="decimal" /></div>
          </label>
        </div>
        <div class="settings-budget-total"><span>Category total</span><strong>{{ formatMoney(categoryTotal) }}</strong><small>Overall budget: {{ formatMoney(centsFromDollars(form.totalBudget) || 0) }}</small></div>
        <div class="settings-save"><el-button type="primary" size="large" :loading="saving" @click="saveSettings">Save changes</el-button></div>
      </section>

      <section class="settings-section surface-card settings-account-section">
        <div class="settings-section__heading"><span>04</span><div><h2>Account</h2><p>Signed in as {{ authStore.user?.email }}.</p></div></div>
        <div class="account-action-list">
          <button type="button" @click="signOut"><el-icon><SwitchButton /></el-icon><span><strong>Sign out</strong><small>End your session on this device.</small></span></button>
          <button type="button" class="danger-action" @click="deleteAccount"><el-icon><Delete /></el-icon><span><strong>Delete account</strong><small>Permanently remove your personal data.</small></span></button>
        </div>
      </section>
    </template>
  </div>
</template>
