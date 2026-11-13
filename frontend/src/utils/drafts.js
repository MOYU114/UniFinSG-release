import { CATEGORIES } from '../constants/finance.js'
import { centsFromDollars, dollarsFromCents } from './format.js'

export const DRAFT_KIND_LABELS = {
  create_transactions: 'New expenses',
  update_transaction: 'Edit expense',
  delete_transaction: 'Delete expense',
  update_budget: 'Budget change',
}

export const DRAFT_STATUS_LABELS = {
  pending: 'Waiting for your confirmation',
  approved: 'Recorded',
  rejected: 'Rejected',
  expired: 'Expired',
  failed: 'Could not be applied',
}

export function draftKindLabel(kind) {
  return DRAFT_KIND_LABELS[kind] || 'Draft'
}

export function draftStatusLabel(status) {
  return DRAFT_STATUS_LABELS[status] || DRAFT_STATUS_LABELS.pending
}

export function draftIsActionable(draft) {
  return (draft?.status || 'pending') === 'pending' && !draft?.deciding
}

export function draftTotalCents(items = []) {
  return items.reduce((sum, item) => sum + (Number(item?.amount_cents) || 0), 0)
}

export function categoryLabel(value) {
  return CATEGORIES.find((category) => category.value === value)?.label || value || '—'
}

// duplicateItemIndexes flags rows that share the same date and amount, the
// usual sign of a double entry in one proposal.
export function duplicateItemIndexes(items = []) {
  const seen = new Map()
  const duplicates = new Set()
  items.forEach((item, index) => {
    const key = `${item?.spent_on || ''}:${Number(item?.amount_cents) || 0}`
    if (!item?.amount_cents) return
    if (seen.has(key)) {
      duplicates.add(index)
      duplicates.add(seen.get(key))
    } else {
      seen.set(key, index)
    }
  })
  return duplicates
}

// buildCreatePayload converts the editable rows back to the tool payload.
// It returns { payload } or { error } so the card can show one message.
export function buildCreatePayload(rows = []) {
  if (!rows.length) return { error: 'Keep at least one expense.' }
  const items = []
  for (const row of rows) {
    const amountCents = centsFromDollars(row.amount)
    if (!amountCents || amountCents < 1) return { error: 'Enter a valid amount for every row.' }
    if (!row.spent_on) return { error: 'Every row needs a date.' }
    if (!row.category) return { error: 'Every row needs a category.' }
    items.push({
      amount_cents: amountCents,
      spent_on: row.spent_on,
      category: row.category,
      merchant: emptyToNull(row.merchant),
      note: emptyToNull(row.note),
    })
  }
  return { payload: { items } }
}

export function createRowsFromPayload(payload) {
  const items = Array.isArray(payload?.items) ? payload.items : []
  return items.map((item) => ({
    amount: dollarsFromCents(item.amount_cents),
    spent_on: item.spent_on || '',
    category: item.category || '',
    merchant: item.merchant || '',
    note: item.note || '',
  }))
}

// buildUpdatePayload keeps only fields that actually changed and preserves an
// explicit null when the user cleared merchant or note.
export function buildUpdatePayload(before = {}, after = {}) {
  const changes = {}
  if (Number(after.amount_cents) !== Number(before.amount_cents)) changes.amount_cents = Number(after.amount_cents)
  if ((after.spent_on || '') !== (before.spent_on || '')) changes.spent_on = after.spent_on
  if ((after.category || '') !== (before.category || '')) changes.category = after.category
  const merchant = emptyToNull(after.merchant)
  if ((merchant || '') !== (before.merchant || '')) changes.merchant = merchant
  const note = emptyToNull(after.note)
  if ((note || '') !== (before.note || '')) changes.note = note
  if (!Object.keys(changes).length) return { payload: null }
  return { payload: { transaction_id: before.id, changes } }
}

export function updateFormFromTransaction(transaction = {}) {
  return {
    amount: dollarsFromCents(transaction.amount_cents),
    spent_on: transaction.spent_on || '',
    category: transaction.category || '',
    merchant: transaction.merchant || '',
    note: transaction.note || '',
  }
}

export function buildBudgetPayload(month, totalCents, warnBp, categories = []) {
  return {
    month,
    total_cents: Number(totalCents),
    warn_bp: Number(warnBp),
    categories: categories.map((category) => ({
      category: category.category,
      budget_cents: Number(category.budget_cents) || 0,
    })),
  }
}

export function budgetRows(budget) {
  const values = new Map((budget?.categories || []).map((category) => [category.category, category.budget_cents]))
  return CATEGORIES.map((category) => ({
    category: category.value,
    label: category.label,
    budget_cents: Number(values.get(category.value) || 0),
  }))
}

export function budgetChanged(before, after) {
  if (Number(before?.total_cents) !== Number(after?.total_cents)) return true
  if (Number(before?.warn_bp) !== Number(after?.warn_bp)) return true
  const previous = new Map((before?.categories || []).map((category) => [category.category, Number(category.budget_cents)]))
  return after.categories.some((category) => Number(previous.get(category.category) ?? 0) !== Number(category.budget_cents))
}

export function emptyToNull(value) {
  const trimmed = String(value ?? '').trim()
  return trimmed === '' ? null : trimmed
}

// draftFromPart normalises a stored draft part into the shape DraftCard uses.
export function draftFromPart(part) {
  return {
    draft_id: part.draft_id,
    kind: part.kind,
    status: part.status || 'pending',
    payload: part.payload || {},
    before: part.before || null,
    warnings: part.warnings || [],
    expires_at: part.expires_at || null,
    result: part.result || null,
    reject_reason: part.reject_reason || '',
  }
}
