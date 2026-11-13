<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ArrowLeft, ArrowRight, Plus, TrendCharts, Check } from '@element-plus/icons-vue'
import { getDashboard, listTransactions } from '../api/finance'
import { getAssistantAdvice } from '../api/assistant'
import { useAuthStore } from '../store/auth'
import { CATEGORIES, CATEGORY_MAP } from '../constants/finance'
import { clamp, formatDate, formatMoney, formatMonth, formatPercent, getErrorMessage, shiftMonth, singaporeMonth } from '../utils/format'
import { participantCode, sendTaskEvent } from '../utils/evaluation'
import SpendingDonut from '../components/dashboard/SpendingDonut.vue'
import TransactionFormDialog from '../components/transactions/TransactionFormDialog.vue'

const authStore = useAuthStore()
const route = useRoute()
const router = useRouter()
const loading = ref(true)
const error = ref('')
const month = ref(authStore.user?.current_month || singaporeMonth())
const snapshot = ref(null)
const recentTransactions = ref([])
const assistantAdvice = ref(null)
const showExpenseDialog = ref(false)
const testMode = Boolean(participantCode())

const isCurrentMonth = computed(() => month.value === (authStore.user?.current_month || singaporeMonth()))
const hasSpending = computed(() => (snapshot.value?.transaction_count || 0) > 0)
const usagePercent = computed(() => clamp((snapshot.value?.usage_bp || 0) / 100, 0, 100))
const paceMessage = computed(() => {
  if (!snapshot.value || !hasSpending.value) return 'Add an expense to start seeing your monthly pace.'
  if (snapshot.value.projected_spent_cents == null) return 'Keep logging expenses for a few more days to unlock a projection.'
  const difference = snapshot.value.projected_spent_cents - snapshot.value.total_budget_cents
  if (difference > 0) return `At your current pace, you may finish ${formatMoney(difference)} over budget.`
  return `At your current pace, you may finish ${formatMoney(Math.abs(difference))} under budget.`
})
const paceTone = computed(() => {
  if (!snapshot.value?.projected_spent_cents) return 'neutral'
  return snapshot.value.projected_spent_cents > snapshot.value.total_budget_cents ? 'warning' : 'positive'
})
const adviceItems = computed(() => assistantAdvice.value?.rule_advice || [])

async function loadDashboard() {
  loading.value = true
  error.value = ''
  try {
    const [dashboardData, transactionData, adviceData] = await Promise.all([
      getDashboard(authStore.userId, month.value),
      listTransactions(authStore.userId, { month: month.value, page: 1, page_size: 4 }),
      getAssistantAdvice(authStore.userId, month.value).catch(() => null),
    ])
    snapshot.value = dashboardData
    recentTransactions.value = transactionData.items || []
    assistantAdvice.value = adviceData
  } catch (requestError) {
    error.value = getErrorMessage(requestError, 'We could not load your dashboard.')
  } finally {
    loading.value = false
  }
}

function changeMonth(offset) {
  const next = shiftMonth(month.value, offset)
  if (next > (authStore.user?.current_month || singaporeMonth())) return
  month.value = next
  void loadDashboard()
}

function openExpenseDialog() {
  showExpenseDialog.value = true
}

function savedExpense() {
  void loadDashboard()
}

async function recordBudgetUnderstanding(success) {
  await sendTaskEvent('understand_budget', 'end', { success, errorCode: success ? null : 'moderator_fail' })
  ElMessage.success(`Result recorded: ${success ? 'Pass' : 'Fail'}`)
}

onMounted(() => {
  if (testMode) void sendTaskEvent('understand_budget', 'start')
  if (route.query.welcome === '1') {
    ElMessage({ message: 'You’re all set. Add your first expense when you’re ready.', type: 'success', duration: 4500 })
    router.replace({ name: 'dashboard' })
  }
  void loadDashboard()
})
</script>

<template>
  <div class="dashboard-page">
    <header class="page-header dashboard-heading">
      <div>
        <span class="eyebrow">Your monthly overview</span>
        <h1>{{ formatMonth(month) }}</h1>
        <p>Here’s how your spending is tracking.</p>
      </div>
      <div class="page-header__actions">
        <div class="month-switcher">
          <el-button circle text aria-label="Previous month" @click="changeMonth(-1)"><el-icon><ArrowLeft /></el-icon></el-button>
          <span>{{ formatMonth(month) }}</span>
          <el-button circle text aria-label="Next month" :disabled="isCurrentMonth" @click="changeMonth(1)"><el-icon><ArrowRight /></el-icon></el-button>
        </div>
        <el-button type="primary" size="large" @click="openExpenseDialog"><el-icon><Plus /></el-icon> Add expense</el-button>
      </div>
    </header>

    <div v-if="error" class="state-card error-state">
      <h2>Dashboard unavailable</h2><p>{{ error }}</p><el-button @click="loadDashboard">Try again</el-button>
    </div>

    <div v-else-if="loading" class="dashboard-skeleton">
      <el-skeleton :rows="8" animated />
    </div>

    <template v-else-if="snapshot">
      <section class="summary-grid">
        <article class="summary-card summary-card--primary">
          <span>Spent this month</span>
          <strong>{{ formatMoney(snapshot.total_spent_cents) }}</strong>
          <small>{{ snapshot.transaction_count }} {{ snapshot.transaction_count === 1 ? 'expense' : 'expenses' }} recorded</small>
        </article>
        <article class="summary-card">
          <span>Remaining</span>
          <strong :class="{ negative: snapshot.remaining_cents < 0 }">{{ formatMoney(snapshot.remaining_cents) }}</strong>
          <small>of {{ formatMoney(snapshot.total_budget_cents) }} budget</small>
        </article>
        <article class="summary-card summary-card--progress">
          <div class="summary-card__row"><span>Budget used</span><strong>{{ formatPercent(snapshot.usage_bp) }}</strong></div>
          <div class="large-progress"><i :class="{ warning: (snapshot.usage_bp || 0) >= 8000, over: (snapshot.usage_bp || 0) > 10000 }" :style="{ width: `${usagePercent}%` }"></i></div>
          <small v-if="(snapshot.usage_bp || 0) > 10000" class="danger-text">Over budget by {{ formatMoney(Math.abs(snapshot.remaining_cents)) }}</small>
          <small v-else>{{ 100 - Math.round(usagePercent) }}% still available</small>
        </article>
      </section>

      <section v-if="!hasSpending" class="empty-dashboard-card">
        <div class="empty-illustration"><span>🌱</span></div>
        <div><span class="eyebrow">A fresh start</span><h2>Your budget is ready.</h2><p>Add your first expense to see category insights and track your monthly pace.</p></div>
        <el-button type="primary" size="large" @click="openExpenseDialog"><el-icon><Plus /></el-icon> Add your first expense</el-button>
      </section>

      <div v-else class="dashboard-content-grid">
        <section class="surface-card spending-card">
          <div class="card-heading"><div><span class="eyebrow">Breakdown</span><h2>Spending by category</h2></div><span class="card-caption">{{ formatMoney(snapshot.total_spent_cents) }} total</span></div>
          <div class="spending-visual">
            <div class="donut-wrap">
              <SpendingDonut :categories="snapshot.categories" />
              <div class="donut-centre"><small>Total spent</small><strong>{{ formatMoney(snapshot.total_spent_cents, { compact: true }) }}</strong></div>
            </div>
            <div class="chart-legend">
              <div v-for="item in snapshot.categories.filter((category) => category.spent_cents > 0)" :key="item.category">
                <i :style="{ background: CATEGORY_MAP[item.category]?.color }"></i>
                <span>{{ CATEGORY_MAP[item.category]?.shortLabel }}</span>
                <strong>{{ formatMoney(item.spent_cents, { compact: true }) }}</strong>
              </div>
            </div>
          </div>
        </section>

        <section class="surface-card pace-card" :class="`pace-card--${paceTone}`">
          <span class="pace-icon"><TrendCharts /></span>
          <span class="eyebrow">Budget guidance</span>
          <h2>{{ adviceItems.length ? 'A few grounded next steps.' : paceTone === 'warning' ? 'A little course correction may help.' : paceTone === 'positive' ? 'You’re on a steady path.' : 'Your pace is taking shape.' }}</h2>
          <ul v-if="adviceItems.length" class="dashboard-advice-list">
            <li v-for="item in adviceItems" :key="item.id">{{ item.text }}</li>
          </ul>
          <p v-else>{{ paceMessage }}</p>
          <div v-if="snapshot.projected_spent_cents != null" class="pace-projection">
            <span>Projected month-end spend</span><strong>{{ formatMoney(snapshot.projected_spent_cents) }}</strong>
          </div>
          <router-link to="/chat" class="text-link">Ask the Budget Assistant <ArrowRight /></router-link>
        </section>
      </div>

      <section class="surface-card category-progress-card">
        <div class="card-heading"><div><span class="eyebrow">Category budgets</span><h2>Where you stand</h2></div></div>
        <div class="category-progress-list">
          <div v-for="category in snapshot.categories" :key="category.category" class="category-progress-row">
            <span class="category-icon" :style="{ background: CATEGORY_MAP[category.category]?.soft }">{{ CATEGORY_MAP[category.category]?.icon }}</span>
            <div class="category-progress-copy">
              <div><strong>{{ CATEGORY_MAP[category.category]?.label }}</strong><span>{{ formatMoney(category.spent_cents) }} of {{ formatMoney(category.budget_cents) }}</span></div>
              <div class="category-progress-track"><i :class="category.status" :style="{ width: `${clamp((category.usage_bp || 0) / 100)}%` }"></i></div>
            </div>
            <span class="status-pill" :class="category.status">{{ category.status === 'over' ? 'Over budget' : category.status === 'warn' ? 'Nearly there' : formatPercent(category.usage_bp) }}</span>
          </div>
        </div>
      </section>

      <section v-if="recentTransactions.length" class="surface-card recent-card">
        <div class="card-heading"><div><span class="eyebrow">Latest activity</span><h2>Recent expenses</h2></div><router-link to="/transactions" class="text-link">View all <ArrowRight /></router-link></div>
        <div class="recent-list">
          <div v-for="item in recentTransactions" :key="item.id" class="recent-row">
            <span class="category-icon" :style="{ background: CATEGORY_MAP[item.category]?.soft }">{{ CATEGORY_MAP[item.category]?.icon }}</span>
            <div><strong>{{ item.merchant || CATEGORY_MAP[item.category]?.label }}</strong><small>{{ CATEGORY_MAP[item.category]?.label }} · {{ formatDate(item.spent_on) }}</small></div>
            <strong>{{ formatMoney(item.amount_cents) }}</strong>
          </div>
        </div>
      </section>

      <aside v-if="testMode" class="moderator-panel">
        <span><Check /> Moderator: did the participant understand their budget status?</span>
        <div><el-button size="small" @click="recordBudgetUnderstanding(false)">Fail</el-button><el-button size="small" type="primary" @click="recordBudgetUnderstanding(true)">Pass</el-button></div>
      </aside>
    </template>

    <button class="mobile-fab" aria-label="Add expense" @click="openExpenseDialog"><Plus /></button>
    <TransactionFormDialog v-model="showExpenseDialog" :user-id="authStore.userId" @saved="savedExpense" />
  </div>
</template>
