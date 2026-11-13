import test from 'node:test'
import assert from 'node:assert/strict'
import { allocateBudget, allocateEvenly, CATEGORIES } from '../src/constants/finance.js'
import { centsFromDollars, dollarsFromCents, formatMoney, shiftMonth } from '../src/utils/format.js'

test('money helpers preserve integer cents and reject ambiguous input', () => {
  assert.equal(centsFromDollars('12.30'), 1230)
  assert.equal(centsFromDollars('S$ 1,234.50'), 123450)
  assert.equal(centsFromDollars('12.345'), null)
  assert.equal(centsFromDollars('-1.00'), null)
  assert.equal(dollarsFromCents(1230), '12.30')
  assert.match(formatMoney(1230), /S\$12\.30/)
})

test('suggested and even allocations preserve the exact total', () => {
  for (const allocator of [allocateBudget, allocateEvenly]) {
    const allocation = allocator(100003)
    assert.equal(allocation.length, CATEGORIES.length)
    assert.equal(allocation.reduce((sum, item) => sum + item.budget_cents, 0), 100003)
  }
})

test('month shifting works across year boundaries', () => {
  assert.equal(shiftMonth('2026-01', -1), '2025-12')
  assert.equal(shiftMonth('2026-12', 1), '2027-01')
})
