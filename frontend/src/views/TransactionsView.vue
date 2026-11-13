<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowLeft, ArrowRight, Delete, Edit, Plus, Search } from '@element-plus/icons-vue'
import { deleteTransaction, listTransactions } from '../api/finance'
import { useAuthStore } from '../store/auth'
import { CATEGORIES, CATEGORY_MAP } from '../constants/finance'
import { formatDate, formatMoney, formatMonth, getErrorMessage, shiftMonth, singaporeMonth } from '../utils/format'
import TransactionFormDialog from '../components/transactions/TransactionFormDialog.vue'

const authStore = useAuthStore()
const loading = ref(true)
const error = ref('')
const month = ref(authStore.user?.current_month || singaporeMonth())
const category = ref('')
const page = ref(1)
const pageSize = 20
const result = ref({ items: [], total: 0, page: 1, page_size: pageSize })
const showDialog = ref(false)
const editingTransaction = ref(null)

const pageCount = computed(() => Math.max(1, Math.ceil(result.value.total / pageSize)))
const isCurrentMonth = computed(() => month.value === (authStore.user?.current_month || singaporeMonth()))

async function loadTransactions() {
  loading.value = true
  error.value = ''
  try {
    result.value = await listTransactions(authStore.userId, {
      month: month.value,
      category: category.value || undefined,
      page: page.value,
      page_size: pageSize,
    })
  } catch (requestError) {
    error.value = getErrorMessage(requestError, 'We could not load your expenses.')
  } finally {
    loading.value = false
  }
}

function changeMonth(offset) {
  const next = shiftMonth(month.value, offset)
  if (next > (authStore.user?.current_month || singaporeMonth())) return
  month.value = next
  page.value = 1
}

function addExpense() {
  editingTransaction.value = null
  showDialog.value = true
}

function editExpense(item) {
  editingTransaction.value = item
  showDialog.value = true
}

async function removeExpense(item) {
  try {
    await ElMessageBox.confirm(
      `Delete the ${formatMoney(item.amount_cents)} expense${item.merchant ? ` at ${item.merchant}` : ''}?`,
      'Delete expense',
      { type: 'warning', confirmButtonText: 'Delete', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }

  try {
    await deleteTransaction(authStore.userId, item.id)
    ElMessage.success('Expense deleted.')
    if (result.value.items.length === 1 && page.value > 1) page.value -= 1
    else await loadTransactions()
  } catch (requestError) {
    ElMessage.error(getErrorMessage(requestError, 'We could not delete this expense.'))
  }
}

function savedExpense() {
  void loadTransactions()
}

watch([month, category, page], loadTransactions)
onMounted(loadTransactions)
</script>

<template>
  <div class="transactions-page">
    <header class="page-header">
      <div><span class="eyebrow">Your spending log</span><h1>Transactions</h1><p>Every expense, clearly organised.</p></div>
      <el-button type="primary" size="large" @click="addExpense"><el-icon><Plus /></el-icon> Add expense</el-button>
    </header>

    <section class="filter-bar">
      <div class="month-switcher month-switcher--wide">
        <el-button circle text aria-label="Previous month" @click="changeMonth(-1)"><el-icon><ArrowLeft /></el-icon></el-button>
        <span>{{ formatMonth(month) }}</span>
        <el-button circle text aria-label="Next month" :disabled="isCurrentMonth" @click="changeMonth(1)"><el-icon><ArrowRight /></el-icon></el-button>
      </div>
      <el-select v-model="category" placeholder="All categories" clearable class="category-filter">
        <template #prefix><el-icon><Search /></el-icon></template>
        <el-option v-for="item in CATEGORIES" :key="item.value" :label="item.label" :value="item.value" />
      </el-select>
      <span class="filter-result-count">{{ result.total }} {{ result.total === 1 ? 'expense' : 'expenses' }}</span>
    </section>

    <div v-if="error" class="state-card error-state"><h2>Expenses unavailable</h2><p>{{ error }}</p><el-button @click="loadTransactions">Try again</el-button></div>
    <div v-else-if="loading" class="surface-card"><el-skeleton :rows="7" animated /></div>

    <section v-else-if="result.items.length" class="surface-card transaction-list-card">
      <div class="transaction-table transaction-table--header">
        <span>Expense</span><span>Category</span><span>Date</span><span>Amount</span><span aria-hidden="true"></span>
      </div>
      <article v-for="item in result.items" :key="item.id" class="transaction-table transaction-row">
        <div class="transaction-main">
          <span class="category-icon" :style="{ background: CATEGORY_MAP[item.category]?.soft }">{{ CATEGORY_MAP[item.category]?.icon }}</span>
          <span><strong>{{ item.merchant || CATEGORY_MAP[item.category]?.label }}<span v-if="item.source === 'agent'" class="transaction-ai-tag">AI</span></strong><small v-if="item.note">{{ item.note }}</small><small v-else>No note</small></span>
        </div>
        <span class="transaction-category">{{ CATEGORY_MAP[item.category]?.label }}</span>
        <span class="transaction-date">{{ formatDate(item.spent_on, { includeYear: true }) }}</span>
        <strong class="transaction-amount">{{ formatMoney(item.amount_cents) }}</strong>
        <div class="transaction-actions">
          <el-button circle text aria-label="Edit expense" @click="editExpense(item)"><el-icon><Edit /></el-icon></el-button>
          <el-button circle text type="danger" aria-label="Delete expense" @click="removeExpense(item)"><el-icon><Delete /></el-icon></el-button>
        </div>
      </article>
    </section>

    <section v-else class="state-card empty-state">
      <span class="empty-state__icon">🧾</span>
      <h2>{{ category ? 'No matching expenses' : 'No expenses yet' }}</h2>
      <p>{{ category ? `There are no ${CATEGORY_MAP[category]?.label.toLowerCase()} expenses in ${formatMonth(month)}.` : `Start your ${formatMonth(month)} spending log with one quick entry.` }}</p>
      <el-button v-if="!category && isCurrentMonth" type="primary" size="large" @click="addExpense"><el-icon><Plus /></el-icon> Add an expense</el-button>
      <el-button v-else-if="category" @click="category = ''">Clear filter</el-button>
    </section>

    <footer v-if="pageCount > 1" class="pagination-footer">
      <el-pagination v-model:current-page="page" background layout="prev, pager, next" :page-size="pageSize" :total="result.total" />
    </footer>

    <button class="mobile-fab" aria-label="Add expense" @click="addExpense"><Plus /></button>
    <TransactionFormDialog v-model="showDialog" :user-id="authStore.userId" :transaction="editingTransaction" @saved="savedExpense" />
  </div>
</template>
