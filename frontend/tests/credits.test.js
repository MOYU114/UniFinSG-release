import test from 'node:test'
import assert from 'node:assert/strict'
import { creditsLabel, creditsPopoverLines, creditsResetLabel } from '../src/utils/credits.js'

const platform = {
  mode: 'platform',
  daily_grant: 100,
  used: 13,
  remaining: 87,
  resets_at: '2026-11-06T00:00:00+08:00',
  costs: { chat: 1, tool: 2, vision: 5 },
}

test('credits label shows remaining over the daily grant', () => {
  assert.equal(creditsLabel(platform), 'Today 87 / 100')
  assert.equal(creditsLabel({ ...platform, used: 120, remaining: 0 }), 'Today 0 / 100')
})

test('credits label explains BYOK and hides unknown data', () => {
  assert.equal(creditsLabel({ mode: 'byok' }), 'Using your own model')
  assert.equal(creditsLabel(null), '')
  assert.equal(creditsLabel({ mode: 'platform' }), '')
})

test('credits popover lists tier costs and the SGT reset time', () => {
  assert.equal(creditsResetLabel(platform.resets_at), 'Resets at 00:00 (SGT)')
  assert.equal(creditsResetLabel('not-a-date'), '')
  assert.deepEqual(creditsPopoverLines(platform), [
    'Chat 1 · Tool use 2 · Screenshot 5',
    'Resets at 00:00 (SGT)',
  ])
  assert.deepEqual(creditsPopoverLines({ mode: 'byok' }), [
    'Your own API key is used for every request.',
    'No daily credit limit.',
  ])
})
