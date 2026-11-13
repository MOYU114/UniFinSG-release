<script setup>
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { ArrowDown } from '@element-plus/icons-vue'
import { createTransaction, updateTransaction } from '../../api/finance'
import { CATEGORIES } from '../../constants/finance'
import { centsFromDollars, dollarsFromCents, getErrorMessage, singaporeToday } from '../../utils/format'
import { sendTaskEvent } from '../../utils/evaluation'

const props = defineProps({ modelValue: Boolean, userId: Number, transaction: { type: Object, default: null } })
const emit = defineEmits(['update:modelValue', 'saved'])

const saving = ref(false)
const showDetails = ref(false)
const form = reactive({ amount: '', category: 'food', spentOn: singaporeToday(), merchant: '', note: '' })
const editing = computed(() => Boolean(props.transaction?.id))

watch(() => props.modelValue, (open) => {
  if (!open) return
  const item = props.transaction
  form.amount = item ? dollarsFromCents(item.amount_cents) : ''
  form.category = item?.category || 'food'
  form.spentOn = item?.spent_on || singaporeToday()
  form.merchant = item?.merchant || ''
  form.note = item?.note || ''
  showDetails.value = Boolean(item?.merchant || item?.note)
  if (!editing.value) void sendTaskEvent('add_expense', 'start')
})

function close() {
  emit('update:modelValue', false)
}

async function submit() {
  const amountCents = centsFromDollars(form.amount)
  if (!amountCents || amountCents <= 0) return ElMessage.warning('Enter an amount greater than S$0.')
  if (!form.category) return ElMessage.warning('Choose a category.')
  if (!form.spentOn) return ElMessage.warning('Choose a date.')

  saving.value = true
  const payload = {
    amount_cents: amountCents,
    category: form.category,
    spent_on: form.spentOn,
    merchant: form.merchant.trim() || null,
    note: form.note.trim() || null,
  }
  try {
    const saved = editing.value
      ? await updateTransaction(props.userId, props.transaction.id, payload)
      : await createTransaction(props.userId, payload)
    if (!editing.value) await sendTaskEvent('add_expense', 'end', { success: true })
    emit('saved', saved)
    close()
    ElMessage.success(editing.value ? 'Expense updated.' : 'Expense added.')
  } catch (error) {
    if (!editing.value) {
      await sendTaskEvent('add_expense', 'end', {
        success: false,
        errorCode: error?.response?.data?.error?.code || 'save_failed',
      })
    }
    ElMessage.error(getErrorMessage(error, 'We could not save this expense.'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="editing ? 'Edit expense' : 'Add an expense'"
    width="min(560px, calc(100vw - 24px))"
    class="expense-dialog"
    destroy-on-close
    @close="close"
  >
    <div class="expense-form">
      <label class="field-label amount-field">
        <span>Amount</span>
        <div class="hero-money-input"><span>S$</span><input v-model="form.amount" inputmode="decimal" autofocus placeholder="0.00" @keyup.enter="submit" /></div>
      </label>

      <fieldset class="category-picker">
        <legend>Category</legend>
        <div class="category-picker__grid">
          <label v-for="category in CATEGORIES" :key="category.value" :class="{ selected: form.category === category.value }">
            <input v-model="form.category" type="radio" :value="category.value" />
            <span :style="{ background: category.soft }">{{ category.icon }}</span>
            <small>{{ category.shortLabel }}</small>
          </label>
        </div>
      </fieldset>

      <label class="field-label">
        <span>Date</span>
        <el-date-picker v-model="form.spentOn" type="date" value-format="YYYY-MM-DD" format="DD MMM YYYY" :disabled-date="(date) => date > new Date()" />
      </label>

      <button class="details-toggle" type="button" @click="showDetails = !showDetails">
        {{ showDetails ? 'Hide' : 'Add' }} merchant & note <el-icon :class="{ rotated: showDetails }"><ArrowDown /></el-icon>
      </button>
      <div v-if="showDetails" class="optional-details">
        <label class="field-label"><span>Merchant <small>Optional</small></span><el-input v-model="form.merchant" maxlength="64" placeholder="e.g. Fine Food" /></label>
        <label class="field-label"><span>Note <small>Optional</small></span><el-input v-model="form.note" type="textarea" :rows="2" maxlength="200" show-word-limit placeholder="Anything useful to remember" /></label>
      </div>
    </div>
    <template #footer>
      <el-button @click="close">Cancel</el-button>
      <el-button type="primary" :loading="saving" @click="submit">{{ editing ? 'Save changes' : 'Save expense' }}</el-button>
    </template>
  </el-dialog>
</template>
