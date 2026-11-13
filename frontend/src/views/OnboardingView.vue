<script setup>
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ArrowRight, Check } from '@element-plus/icons-vue'
import { saveBudget, saveProfile } from '../api/finance'
import { useAuthStore } from '../store/auth'
import {
  CATEGORIES,
  HOUSING_OPTIONS,
  allocateBudget,
  allocateEvenly,
} from '../constants/finance'
import { centsFromDollars, dollarsFromCents, getErrorMessage, singaporeMonth } from '../utils/format'
import { sendTaskEvent } from '../utils/evaluation'

const router = useRouter()
const authStore = useAuthStore()
const saving = ref(false)
const started = ref(false)

const form = reactive({
  monthlyDisposable: '1200.00',
  housing: 'on_campus',
  totalBudget: '1000.00',
  categoryBudgets: Object.fromEntries(
    allocateBudget(100000).map((item) => [item.category, dollarsFromCents(item.budget_cents)]),
  ),
})

const categorySum = computed(() => CATEGORIES.reduce(
  (sum, category) => sum + (centsFromDollars(form.categoryBudgets[category.value]) || 0),
  0,
))
const totalBudgetCents = computed(() => centsFromDollars(form.totalBudget) || 0)
const difference = computed(() => totalBudgetCents.value - categorySum.value)

function markStarted() {
  if (started.value) return
  started.value = true
  void sendTaskEvent('onboarding', 'start')
}

function applyAllocation(items) {
  for (const item of items) form.categoryBudgets[item.category] = dollarsFromCents(item.budget_cents)
  markStarted()
}

function suggestedSplit() {
  applyAllocation(allocateBudget(totalBudgetCents.value))
}

function splitEvenly() {
  applyAllocation(allocateEvenly(totalBudgetCents.value))
}

function validate() {
  const disposable = centsFromDollars(form.monthlyDisposable)
  const budget = centsFromDollars(form.totalBudget)
  if (!disposable || disposable <= 0) return 'Enter a monthly disposable amount greater than S$0.'
  if (budget === null || budget < 0) return 'Enter a valid monthly budget.'
  if (!form.housing) return 'Choose your housing situation.'
  for (const category of CATEGORIES) {
    if (centsFromDollars(form.categoryBudgets[category.value]) === null) {
      return `Enter a valid budget for ${category.label}.`
    }
  }
  return ''
}

async function saveSetup() {
  markStarted()
  const validationMessage = validate()
  if (validationMessage) {
    ElMessage.warning(validationMessage)
    return
  }

  saving.value = true
  try {
    const monthlyDisposableCents = centsFromDollars(form.monthlyDisposable)
    const totalCents = centsFromDollars(form.totalBudget)
    await saveProfile(authStore.userId, {
      monthly_disposable_cents: monthlyDisposableCents,
      housing: form.housing,
      default_budget_cents: totalCents,
    })
    await saveBudget(authStore.userId, authStore.user?.current_month || singaporeMonth(), {
      total_cents: totalCents,
      warn_bp: 8000,
      categories: CATEGORIES.map((category) => ({
        category: category.value,
        budget_cents: centsFromDollars(form.categoryBudgets[category.value]),
      })),
    })
    await authStore.markOnboarded()
    await sendTaskEvent('onboarding', 'end', { success: true })
    ElMessage.success('Your budget is ready.')
    router.replace({ name: 'dashboard', query: { welcome: '1' } })
  } catch (error) {
    await sendTaskEvent('onboarding', 'end', {
      success: false,
      errorCode: error?.response?.data?.error?.code || 'save_failed',
    })
    ElMessage.error(getErrorMessage(error, 'We could not save your setup.'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="onboarding-page" @focusin="markStarted">
    <header class="onboarding-header">
      <router-link to="/onboarding" class="brand-lockup">
        <span class="brand-mark">U</span>
        <span><strong>UniFinSG</strong><small>Spend with clarity</small></span>
      </router-link>
      <div class="setup-progress"><span><Check /></span> Account created <i></i><b>2</b> Set your budget</div>
    </header>

    <main class="onboarding-content">
      <div class="onboarding-intro">
        <span class="eyebrow">Takes about 2 minutes</span>
        <h1>Let’s shape a budget<br />that works for you.</h1>
        <p>A few simple numbers are all we need. You can change them anytime.</p>
      </div>

      <section class="setup-card">
        <div class="setup-section">
          <div class="section-number">01</div>
          <div class="setup-section__body">
            <div class="section-heading">
              <div><h2>Your monthly picture</h2><p>What do you have available after fixed commitments?</p></div>
            </div>
            <div class="setup-grid two-columns">
              <label class="field-label">
                <span>Monthly disposable amount</span>
                <div class="money-input"><span>S$</span><input v-model="form.monthlyDisposable" inputmode="decimal" /></div>
                <small>Income, allowance or scholarship available to spend</small>
              </label>
              <label class="field-label">
                <span>Total budget for this month</span>
                <div class="money-input"><span>S$</span><input v-model="form.totalBudget" inputmode="decimal" @change="suggestedSplit" /></div>
                <small>How much you plan to spend in total</small>
              </label>
            </div>

            <fieldset class="housing-fieldset">
              <legend>Where do you live?</legend>
              <div class="choice-grid">
                <label v-for="option in HOUSING_OPTIONS" :key="option.value" class="choice-card" :class="{ selected: form.housing === option.value }">
                  <input v-model="form.housing" type="radio" :value="option.value" />
                  <span class="choice-radio"></span>
                  <strong>{{ option.label }}</strong>
                  <small>{{ option.description }}</small>
                </label>
              </div>
            </fieldset>
          </div>
        </div>

        <div class="setup-divider"></div>

        <div class="setup-section">
          <div class="section-number">02</div>
          <div class="setup-section__body">
            <div class="section-heading split-heading">
              <div><h2>Plan by category</h2><p>Give every dollar a purpose. Your category total can differ from the overall budget.</p></div>
              <div class="split-actions">
                <el-button plain @click="splitEvenly">Split evenly</el-button>
                <el-button plain @click="suggestedSplit">Suggested split</el-button>
              </div>
            </div>

            <div class="category-budget-grid">
              <label v-for="category in CATEGORIES" :key="category.value" class="category-budget-field">
                <span class="category-icon" :style="{ background: category.soft }">{{ category.icon }}</span>
                <span class="category-budget-field__copy"><strong>{{ category.label }}</strong><small>Monthly limit</small></span>
                <div class="mini-money-input"><span>S$</span><input v-model="form.categoryBudgets[category.value]" inputmode="decimal" /></div>
              </label>
            </div>

            <div class="allocation-summary" :class="{ warning: difference !== 0 }">
              <span>Category total <strong>{{ dollarsFromCents(categorySum) }}</strong></span>
              <span v-if="difference !== 0">{{ difference > 0 ? 'Unallocated' : 'Over-allocated' }} <strong>S${{ dollarsFromCents(Math.abs(difference)) }}</strong></span>
              <span v-else class="balanced"><Check /> Fully allocated</span>
            </div>
          </div>
        </div>

        <footer class="setup-footer">
          <p><strong>Privacy first.</strong> We only use these totals to calculate your budget insights.</p>
          <el-button type="primary" size="large" :loading="saving" @click="saveSetup">
            Finish setup <el-icon class="el-icon--right"><ArrowRight /></el-icon>
          </el-button>
        </footer>
      </section>
    </main>
  </div>
</template>
