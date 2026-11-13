import test from 'node:test'
import assert from 'node:assert/strict'
import { quotaExhausted, quotaLabel } from '../src/utils/assistantQuota.js'

test('quota label shows remaining daily questions', () => {
  assert.equal(quotaLabel({ daily_limit: 30, daily_remaining: 27 }), '27 of 30 questions left today')
  assert.equal(quotaLabel({ daily_limit: 1, daily_remaining: 1 }), '1 of 1 question left today')
  assert.equal(quotaLabel({ daily_limit: 30, daily_remaining: 0 }), 'No questions left today · resets at midnight (SGT)')
  assert.equal(quotaLabel({ daily_limit: 30, daily_remaining: 45 }), '30 of 30 questions left today')
})

test('quota label is empty when the quota is unknown', () => {
  assert.equal(quotaLabel(null), '')
  assert.equal(quotaLabel({}), '')
  assert.equal(quotaLabel({ daily_limit: 0, daily_remaining: 0 }), '')
})

test('quota is exhausted only when the backend reports none left', () => {
  assert.equal(quotaExhausted({ daily_remaining: 0 }), true)
  assert.equal(quotaExhausted({ daily_remaining: 3 }), false)
  assert.equal(quotaExhausted(null), false)
})
