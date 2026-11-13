import test from 'node:test'
import assert from 'node:assert/strict'
import { consumeAgentSSE, normalizeSuggestions, toolLabel, toolResultSummary, verifiedHint, VERIFIED_HINT } from '../src/utils/agent.js'

function body(text, byteByByte = false) {
  const bytes = new TextEncoder().encode(text)
  return new ReadableStream({
    start(controller) {
      if (byteByByte) for (const byte of bytes) controller.enqueue(new Uint8Array([byte]))
      else controller.enqueue(bytes)
      controller.close()
    },
  })
}

const stream = [
  'event: ack\ndata: {"turn_id":"t1","user_message_id":11,"mode":"platform","credits_reserved":2}\n\n',
  'event: status\ndata: {"phase":"thinking"}\n\n',
  'event: tool_call\ndata: {"id":"call_1","name":"get_budget_snapshot","args":{}}\n\n',
  'event: tool_result\ndata: {"id":"call_1","name":"get_budget_snapshot","ok":true,"summary":"Checked the 2026-11 budget snapshot"}\n\n',
  'event: delta\ndata: {"text":"You have S$12.00 left."}\n\n',
  'event: done\ndata: {"status":"completed","assistant_message_id":12,"verified":true,"credits_charged":2,"credits_remaining":98}\n\n',
].join('')

test('agent SSE dispatches every named event in order', async () => {
  const seen = []
  const handlers = {}
  for (const name of ['onAck', 'onStatus', 'onToolCall', 'onToolResult', 'onDelta', 'onDone']) {
    handlers[name] = (data) => seen.push([name, data])
  }
  const result = await consumeAgentSSE(body(stream, true), handlers)
  assert.equal(result, null)
  assert.deepEqual(seen.map(([name]) => name), [
    'onAck', 'onStatus', 'onToolCall', 'onToolResult', 'onDelta', 'onDone',
  ])
  assert.equal(seen[0][1].turn_id, 't1')
  assert.equal(seen[2][1].name, 'get_budget_snapshot')
  assert.equal(seen[3][1].summary, 'Checked the 2026-11 budget snapshot')
  assert.equal(seen[4][1].text, 'You have S$12.00 left.')
  assert.equal(seen[5][1].verified, true)
})

test('agent SSE keeps reading after an error event and still delivers done', async () => {
  const errors = []
  let done = null
  const text = [
    'event: error\ndata: {"code":"max_steps","message":"The assistant stopped after reaching the step limit."}\n\n',
    'event: done\ndata: {"status":"completed","assistant_message_id":3,"verified":null,"credits_charged":2,"credits_remaining":50}\n\n',
  ].join('')
  const result = await consumeAgentSSE(body(text), { onError: (data) => errors.push(data), onDone: (data) => { done = data } })
  assert.equal(result.code, 'max_steps')
  assert.equal(errors.length, 1)
  assert.equal(done.status, 'completed')
  assert.equal(done.verified, null)
})

test('agent SSE rejects a stream without done and releases the reader', async () => {
  const streamBody = body('event: delta\ndata: {"text":"partial"}\n\n')
  await assert.rejects(consumeAgentSSE(streamBody), /before the assistant finished/)
  assert.equal(streamBody.locked, false)
})

test('verified hint only appears for an explicit false', () => {
  assert.equal(verifiedHint(null), '')
  assert.equal(verifiedHint(undefined), '')
  assert.equal(verifiedHint(true), '')
  assert.equal(verifiedHint(false), VERIFIED_HINT)
  assert.match(VERIFIED_HINT, /could not be checked against your ledger/)
})

test('tool labels are human readable and fall back to the tool name', () => {
  assert.equal(toolLabel('get_budget_snapshot', { month: '2026-11' }), 'Checked the 2026-11 budget snapshot')
  assert.equal(toolLabel('get_budget_snapshot'), 'Checked the budget snapshot')
  assert.equal(toolLabel('list_transactions', { category: 'food' }), 'Listed food transactions')
  assert.equal(toolLabel('list_categories'), 'Listed the spending categories')
  assert.equal(toolLabel('unknown_tool'), 'Used unknown_tool')
})

test('history summaries mirror the backend wording', () => {
  assert.equal(toolResultSummary('get_budget_snapshot', { month: '2026-11' }, false), 'Checked the 2026-11 budget snapshot')
  assert.equal(toolResultSummary('list_transactions', { month: '2026-11', count: 3 }, false), 'Listed 3 transactions for 2026-11')
  assert.equal(toolResultSummary('list_categories', {}, false), 'Listed the 6 spending categories')
  assert.equal(toolResultSummary('get_budget_snapshot', {}, true), 'get_budget_snapshot could not run')
})

test('agent SSE dispatches the draft event and an awaiting_approval completion', async () => {
  const text = [
    'event: ack\ndata: {"turn_id":"t2","user_message_id":21,"mode":"platform","credits_reserved":2}\n\n',
    'event: tool_call\ndata: {"id":"call_1","name":"propose_transactions","args":{"items":[]}}\n\n',
    'event: draft\ndata: {"draft_id":"d_1","kind":"create_transactions","payload":{"items":[{"amount_cents":650}]},"warnings":[],"expires_at":"2026-11-06T10:00:00Z"}\n\n',
    'event: done\ndata: {"status":"awaiting_approval","assistant_message_id":22,"verified":null,"credits_charged":2,"credits_remaining":98}\n\n',
  ].join('')
  const drafts = []
  let done = null
  const result = await consumeAgentSSE(body(text, true), {
    onDraft: (draft) => drafts.push(draft),
    onDone: (value) => { done = value },
  })
  assert.equal(result, null)
  assert.equal(drafts.length, 1)
  assert.equal(drafts[0].draft_id, 'd_1')
  assert.equal(drafts[0].payload.items[0].amount_cents, 650)
  assert.equal(done.status, 'awaiting_approval')
  assert.equal(done.credits_charged, 2)
})

test('propose tools get a human label', () => {
  assert.equal(toolLabel('propose_transactions'), 'Prepared expenses for confirmation')
  assert.equal(toolResultSummary('propose_update_budget', {}, false), 'Waiting for your confirmation')
})

test('normalizeSuggestions keeps up to three distinct non-empty strings', () => {
  assert.deepEqual(normalizeSuggestions(undefined), [])
  assert.deepEqual(normalizeSuggestions('not a list'), [])
  assert.deepEqual(
    normalizeSuggestions([' Where did my money go? ', '', 42, 'where did my money go?', 'Will I overspend?', 'Record lunch: 6.5', 'Fourth']),
    ['Where did my money go?', 'Will I overspend?', 'Record lunch: 6.5'],
  )
})
