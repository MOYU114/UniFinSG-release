<script setup>
import { computed, ref, watch } from 'vue'
import { Delete } from '@element-plus/icons-vue'
import { CATEGORIES } from '../../constants/finance'
import { centsFromDollars, dollarsFromCents, formatMoney } from '../../utils/format'
import {
  budgetChanged,
  budgetRows,
  buildBudgetPayload,
  buildCreatePayload,
  buildUpdatePayload,
  createRowsFromPayload,
  draftIsActionable,
  draftKindLabel,
  draftStatusLabel,
  draftTotalCents,
  duplicateItemIndexes,
  updateFormFromTransaction,
} from '../../utils/drafts'

const props = defineProps({
  draft: { type: Object, required: true },
  busy: { type: Boolean, default: false },
})
const emit = defineEmits(['decide'])

const error = ref('')
const showReason = ref(false)
const reason = ref('')
const rows = ref([])
const updateAfter = ref({})
const budgetAfter = ref({ total: '0.00', warn_bp: 8000, categories: [] })

const status = computed(() => props.draft.status || 'pending')
const kind = computed(() => props.draft.kind)
const before = computed(() => props.draft.before || null)
const actionable = computed(() => draftIsActionable(props.draft) && !props.busy)

watch(() => props.draft, (draft) => {
  error.value = ''
  if (!draft) return
  if (draft.kind === 'create_transactions') {
    rows.value = createRowsFromPayload(draft.payload)
  } else if (draft.kind === 'update_transaction') {
    updateAfter.value = updateFormFromTransaction(draft.before || {})
  } else if (draft.kind === 'update_budget') {
    const source = draft.payload?.categories?.length ? draft.payload : draft.before
    budgetAfter.value = {
      total: dollarsFromCents(draft.payload?.total_cents ?? draft.before?.total_cents ?? 0),
      warn_bp: Number(draft.payload?.warn_bp ?? draft.before?.warn_bp ?? 8000),
      categories: budgetRows(source).map((row) => ({ ...row, amount: dollarsFromCents(row.budget_cents) })),
    }
  }
}, { immediate: true })

const totalCents = computed(() => draftTotalCents(rows.value.map((row) => ({
  amount_cents: centsFromDollars(row.amount) || 0,
}))))
const duplicates = computed(() => duplicateItemIndexes(rows.value.map((row) => ({
  amount_cents: centsFromDollars(row.amount) || 0,
  spent_on: row.spent_on,
}))))

const WARNING_TEXT = {
  possible_duplicate: 'Some rows may already exist in your ledger.',
  needs_review: 'Some categories could not be recognised; please check them.',
  amount_dropped: 'Some amounts could not be read and were skipped.',
  future_date: 'A future date was changed to today.',
  invalid_date: 'An unreadable date was changed to today.',
  records_truncated: 'Only the first 20 records are shown.',
}
const warningText = computed(() => (props.draft.warnings || [])
  .map((warning) => WARNING_TEXT[warning] || warning)
  .join(' '))

const createPayload = computed(() => {
  const built = buildCreatePayload(rows.value)
  return built.payload || null
})

const updatePayload = computed(() => {
  if (!before.value) return null
  const amount = centsFromDollars(updateAfter.value.amount)
  if (!amount) return null
  const built = buildUpdatePayload(before.value, {
    amount_cents: amount,
    spent_on: updateAfter.value.spent_on,
    category: updateAfter.value.category,
    merchant: updateAfter.value.merchant,
    note: updateAfter.value.note,
  })
  return built.payload
})

const budgetPayload = computed(() => {
  const total = centsFromDollars(budgetAfter.value.total)
  if (total === null) return null
  const categories = []
  for (const row of budgetAfter.value.categories) {
    const amount = centsFromDollars(row.amount)
    if (amount === null) return null
    categories.push({ category: row.category, budget_cents: amount })
  }
  return buildBudgetPayload(
    props.draft.payload?.month || before.value?.month,
    total,
    budgetAfter.value.warn_bp,
    categories,
  )
})

const hasEdits = computed(() => {
  if (kind.value === 'create_transactions') {
    if (!createPayload.value) return false
    return JSON.stringify(createPayload.value.items) !== JSON.stringify(normalizeItems(props.draft.payload))
  }
  if (kind.value === 'update_transaction') return Boolean(updatePayload.value)
  if (kind.value === 'update_budget') return Boolean(budgetPayload.value) && budgetChanged(before.value, budgetPayload.value)
  return false
})

const confirmLabel = computed(() => {
  if (kind.value === 'delete_transaction') return 'Delete expense'
  return hasEdits.value ? 'Confirm changes' : 'Confirm'
})

const isDelete = computed(() => kind.value === 'delete_transaction')

const appliedCount = computed(() => {
  const ids = props.draft.result?.transaction_ids
  return Array.isArray(ids) ? ids.length : 0
})

const expiryLabel = computed(() => {
  if (!props.draft.expires_at) return ''
  const date = new Date(props.draft.expires_at)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleTimeString('en-SG', { hour: '2-digit', minute: '2-digit' })
})

function normalizeItems(payload) {
  return (payload?.items || []).map((item) => ({
    amount_cents: item.amount_cents,
    spent_on: item.spent_on,
    category: item.category,
    merchant: item.merchant || null,
    note: item.note || null,
  }))
}

function removeRow(index) {
  rows.value.splice(index, 1)
}

function decide(action) {
  if (!actionable.value) return
  error.value = ''
  const payload = { action }
  if (action === 'approve') {
    if (kind.value === 'create_transactions') {
      const built = buildCreatePayload(rows.value)
      if (built.error) {
        error.value = built.error
        return
      }
      if (hasEdits.value) payload.edited_payload = built.payload
    } else if (kind.value === 'update_transaction') {
      const amount = centsFromDollars(updateAfter.value.amount)
      if (!amount) {
        error.value = 'Enter a valid amount.'
        return
      }
      const built = buildUpdatePayload(before.value, {
        amount_cents: amount,
        spent_on: updateAfter.value.spent_on,
        category: updateAfter.value.category,
        merchant: updateAfter.value.merchant,
        note: updateAfter.value.note,
      })
      if (built.payload) payload.edited_payload = built.payload
    } else if (kind.value === 'update_budget') {
      if (!budgetPayload.value) {
        error.value = 'Enter valid budget amounts.'
        return
      }
      if (hasEdits.value) payload.edited_payload = budgetPayload.value
    }
  } else {
    const trimmed = reason.value.trim()
    if ([...trimmed].length > 200) {
      error.value = 'Keep the reason within 200 characters.'
      return
    }
    if (trimmed) payload.reason = trimmed
  }
  emit('decide', payload)
}
</script>

<template>
  <section class="draft-card" :class="[`draft-card--${status}`, { 'draft-card--danger': isDelete }]" aria-live="polite">
    <header class="draft-card__head">
      <div>
        <span class="draft-card__eyebrow">{{ draftKindLabel(kind) }}</span>
        <strong>{{ draftStatusLabel(status) }}</strong>
      </div>
      <span v-if="status === 'pending' && expiryLabel" class="draft-card__expiry">
        Expires {{ expiryLabel }}
      </span>
    </header>

    <p v-if="status === 'approved'" class="draft-card__state">
      Recorded<template v-if="appliedCount"> · {{ appliedCount }} {{ appliedCount === 1 ? 'expense' : 'expenses' }}</template>.
      <router-link to="/transactions">Open transactions</router-link>
    </p>
    <p v-else-if="status === 'rejected'" class="draft-card__state">
      Nothing was recorded.<template v-if="draft.reject_reason"> Reason: {{ draft.reject_reason }}</template>
    </p>
    <p v-else-if="status === 'expired'" class="draft-card__state">This draft expired. Nothing was recorded.</p>
    <p v-else-if="status === 'failed'" class="draft-card__state draft-card__state--error">
      {{ draft.reject_reason || 'The change could not be applied.' }}
    </p>

    <template v-if="status === 'pending' && kind === 'create_transactions'">
      <div class="draft-table draft-table--create">
        <div class="draft-table__header" aria-hidden="true">
          <span>Date</span><span>Amount</span><span>Category</span><span>Merchant</span><span>Note</span><span />
        </div>
        <div
          v-for="(row, index) in rows"
          :key="index"
          class="draft-row"
          :class="{ 'draft-row--duplicate': duplicates.has(index) }"
        >
          <label class="draft-field">
            <span class="draft-field__label">Date</span>
            <input v-model="row.spent_on" type="date" class="draft-input" :disabled="!actionable">
          </label>
          <label class="draft-field">
            <span class="draft-field__label">Amount</span>
            <input v-model="row.amount" inputmode="decimal" class="draft-input draft-input--money" :disabled="!actionable">
          </label>
          <label class="draft-field">
            <span class="draft-field__label">Category</span>
            <select v-model="row.category" class="draft-input" :disabled="!actionable">
              <option v-for="category in CATEGORIES" :key="category.value" :value="category.value">{{ category.label }}</option>
            </select>
          </label>
          <label class="draft-field">
            <span class="draft-field__label">Merchant</span>
            <input v-model="row.merchant" maxlength="64" class="draft-input" :disabled="!actionable">
          </label>
          <label class="draft-field">
            <span class="draft-field__label">Note</span>
            <input v-model="row.note" maxlength="200" class="draft-input" :disabled="!actionable">
          </label>
          <el-button
            text
            circle
            size="small"
            aria-label="Remove row"
            :disabled="!actionable || rows.length === 1"
            @click="removeRow(index)"
          >
            <el-icon><Delete /></el-icon>
          </el-button>
        </div>
      </div>
      <p class="draft-summary">
        {{ rows.length }} {{ rows.length === 1 ? 'expense' : 'expenses' }} · <strong>{{ formatMoney(totalCents) }}</strong>
      </p>
      <p v-if="duplicates.size" class="draft-warning">Some rows look like duplicates. Check them before confirming.</p>
      <p v-if="warningText" class="draft-warning">{{ warningText }}</p>
    </template>

    <template v-else-if="status === 'pending' && kind === 'update_transaction'">
      <div class="draft-compare">
        <div class="draft-compare__column">
          <span class="draft-compare__title">Before</span>
          <dl>
            <div><dt>Date</dt><dd>{{ before?.spent_on || '—' }}</dd></div>
            <div><dt>Amount</dt><dd>{{ formatMoney(before?.amount_cents) }}</dd></div>
            <div><dt>Category</dt><dd>{{ before?.category || '—' }}</dd></div>
            <div><dt>Merchant</dt><dd>{{ before?.merchant || '—' }}</dd></div>
            <div><dt>Note</dt><dd>{{ before?.note || '—' }}</dd></div>
          </dl>
        </div>
        <div class="draft-compare__column draft-compare__column--after">
          <span class="draft-compare__title">After</span>
          <dl>
            <div :class="{ 'draft-compare__changed': updatePayload?.changes?.spent_on }">
              <dt>Date</dt>
              <dd><input v-model="updateAfter.spent_on" type="date" class="draft-input" :disabled="!actionable"></dd>
            </div>
            <div :class="{ 'draft-compare__changed': updatePayload?.changes?.amount_cents }">
              <dt>Amount</dt>
              <dd><input v-model="updateAfter.amount" inputmode="decimal" class="draft-input draft-input--money" :disabled="!actionable"></dd>
            </div>
            <div :class="{ 'draft-compare__changed': updatePayload?.changes?.category }">
              <dt>Category</dt>
              <dd>
                <select v-model="updateAfter.category" class="draft-input" :disabled="!actionable">
                  <option v-for="category in CATEGORIES" :key="category.value" :value="category.value">{{ category.label }}</option>
                </select>
              </dd>
            </div>
            <div :class="{ 'draft-compare__changed': updatePayload?.changes?.merchant }">
              <dt>Merchant</dt>
              <dd><input v-model="updateAfter.merchant" maxlength="64" class="draft-input" :disabled="!actionable"></dd>
            </div>
            <div :class="{ 'draft-compare__changed': updatePayload?.changes?.note }">
              <dt>Note</dt>
              <dd><input v-model="updateAfter.note" maxlength="200" class="draft-input" :disabled="!actionable"></dd>
            </div>
          </dl>
        </div>
      </div>
    </template>

    <template v-else-if="status === 'pending' && kind === 'delete_transaction'">
      <div class="draft-delete">
        <strong>{{ before?.merchant || 'This expense' }}</strong>
        <span>{{ formatMoney(before?.amount_cents) }} · {{ before?.spent_on || '—' }}</span>
        <span v-if="draft.payload?.reason" class="draft-delete__reason">Reason: {{ draft.payload.reason }}</span>
      </div>
    </template>

    <template v-else-if="status === 'pending' && kind === 'update_budget'">
      <div class="draft-budget">
        <label class="draft-field">
          <span class="draft-field__label">Total</span>
          <input v-model="budgetAfter.total" inputmode="decimal" class="draft-input draft-input--money" :disabled="!actionable">
        </label>
        <label class="draft-field">
          <span class="draft-field__label">Warn at (bp)</span>
          <input v-model.number="budgetAfter.warn_bp" type="number" min="5000" max="10000" step="100" class="draft-input" :disabled="!actionable">
        </label>
      </div>
      <div class="draft-table draft-table--budget">
        <div class="draft-table__header" aria-hidden="true"><span>Category</span><span>Before</span><span>After</span></div>
        <div v-for="row in budgetAfter.categories" :key="row.category" class="draft-row">
          <span class="draft-row__label">{{ row.label }}</span>
          <span class="draft-row__before">{{ formatMoney(budgetRows(before).find((item) => item.category === row.category)?.budget_cents) }}</span>
          <input v-model="row.amount" inputmode="decimal" class="draft-input draft-input--money" :disabled="!actionable">
        </div>
      </div>
    </template>

    <p v-if="error" class="draft-card__error" role="alert">{{ error }}</p>

    <div v-if="status === 'pending'" class="draft-card__actions">
      <el-button
        :type="isDelete ? 'danger' : 'primary'"
        :disabled="!actionable"
        :loading="Boolean(draft.deciding)"
        @click="decide('approve')"
      >
        {{ confirmLabel }}
      </el-button>
      <el-button :disabled="!actionable" @click="showReason = !showReason">Reject</el-button>
    </div>
    <div v-if="status === 'pending' && showReason" class="draft-card__reject">
      <input v-model="reason" maxlength="200" class="draft-input" placeholder="Why? (optional)" :disabled="!actionable">
      <el-button size="small" :disabled="!actionable" @click="decide('reject')">Send rejection</el-button>
    </div>
  </section>
</template>
