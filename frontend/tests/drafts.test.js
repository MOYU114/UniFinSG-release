import test from 'node:test'
import assert from 'node:assert/strict'
import {
  budgetChanged,
  budgetRows,
  buildBudgetPayload,
  buildCreatePayload,
  buildUpdatePayload,
  createRowsFromPayload,
  draftFromPart,
  draftIsActionable,
  draftKindLabel,
  draftStatusLabel,
  draftTotalCents,
  duplicateItemIndexes,
  updateFormFromTransaction,
} from '../src/utils/drafts.js'

test('draft totals and duplicate rows use integer cents', () => {
  const rows = [
    { amount: '6.50', spent_on: '2026-11-05' },
    { amount: '12', spent_on: '2026-11-05' },
    { amount: '6.50', spent_on: '2026-11-05' },
  ]
  const cents = rows.map((row) => ({ amount_cents: Math.round(Number(row.amount) * 100), spent_on: row.spent_on }))
  assert.equal(draftTotalCents(cents), 2500)
  const duplicates = duplicateItemIndexes(cents)
  assert.deepEqual([...duplicates].sort(), [0, 2])
})

test('create payload rejects empty and invalid rows', () => {
  assert.equal(buildCreatePayload([]).error, 'Keep at least one expense.')
  assert.match(buildCreatePayload([{ amount: 'abc', spent_on: '2026-11-05', category: 'food' }]).error, /valid amount/)
  assert.match(buildCreatePayload([{ amount: '6.50', spent_on: '', category: 'food' }]).error, /date/)
  const built = buildCreatePayload([
    { amount: '6.50', spent_on: '2026-11-05', category: 'food', merchant: '  Toast Box ', note: ' ' },
  ])
  assert.deepEqual(built.payload, {
    items: [{
      amount_cents: 650, spent_on: '2026-11-05', category: 'food', merchant: 'Toast Box', note: null,
    }],
  })
})

test('create rows round-trip from the tool payload', () => {
  const rows = createRowsFromPayload({
    items: [{ amount_cents: 650, spent_on: '2026-11-05', category: 'food', merchant: 'Toast Box', note: null }],
  })
  assert.deepEqual(rows, [{
    amount: '6.50', spent_on: '2026-11-05', category: 'food', merchant: 'Toast Box', note: '',
  }])
})

test('update payload keeps only changed fields and preserves explicit clears', () => {
  const before = {
    id: 5, amount_cents: 650, spent_on: '2026-11-05', category: 'food', merchant: 'Toast Box', note: 'lunch',
  }
  const form = updateFormFromTransaction(before)
  assert.equal(form.amount, '6.50')
  assert.equal(buildUpdatePayload(before, { ...before }).payload, null)

  const amountEdit = buildUpdatePayload(before, { ...before, amount_cents: 700 })
  assert.deepEqual(amountEdit.payload, { transaction_id: 5, changes: { amount_cents: 700 } })

  const cleared = buildUpdatePayload(before, { ...before, merchant: '', note: null })
  assert.deepEqual(cleared.payload, { transaction_id: 5, changes: { merchant: null, note: null } })
})

test('budget rows cover all six categories and detect changes', () => {
  const before = {
    month: '2026-11',
    total_cents: 60000,
    warn_bp: 8000,
    categories: [
      { category: 'food', budget_cents: 20000 },
      { category: 'transport', budget_cents: 5000 },
    ],
  }
  const rows = budgetRows(before)
  assert.equal(rows.length, 6)
  assert.equal(rows[0].budget_cents, 20000)
  assert.equal(rows[2].budget_cents, 0)

  const payload = buildBudgetPayload('2026-11', 65000, 8500, rows)
  assert.equal(payload.month, '2026-11')
  assert.equal(payload.total_cents, 65000)
  assert.equal(payload.categories.length, 6)
  assert.equal(budgetChanged(before, payload), true)
  assert.equal(budgetChanged(before, buildBudgetPayload('2026-11', 60000, 8000, rows)), false)
})

test('stored draft parts restore the card state', () => {
  const draft = draftFromPart({
    type: 'draft', draft_id: 'd_1', kind: 'create_transactions', status: 'approved',
    payload: { items: [] }, before: null, warnings: [], result: { transaction_ids: [1] },
  })
  assert.equal(draft.status, 'approved')
  assert.equal(draftIsActionable(draft), false)
  assert.equal(draftStatusLabel(draft.status), 'Recorded')
  assert.equal(draftKindLabel(draft.kind), 'New expenses')
  assert.equal(draftFromPart({ draft_id: 'd_2', kind: 'update_budget' }).status, 'pending')
})
