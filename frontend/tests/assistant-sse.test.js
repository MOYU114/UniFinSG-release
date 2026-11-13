import test from 'node:test'
import assert from 'node:assert/strict'
import { consumeAssistantSSE } from '../src/utils/sse.js'

function body(text, byteByByte = false) {
  const bytes = new TextEncoder().encode(text)
  return new ReadableStream({ start(controller) {
    if (byteByByte) for (const byte of bytes) controller.enqueue(new Uint8Array([byte]))
    else controller.enqueue(bytes)
    controller.close()
  } })
}

const events = [
  'event: ack\r\ndata: {"request_id":"r_1","intent":"overview"}\r\n\r\n',
  'event: facts\r\ndata: {"numbers":{"remaining_cents":53500},"rule_advice":[]}\r\n\r\n',
  'event: stream\r\ndata: {"delta":"You have "}\r\n\r\n',
  'event: stream\r\ndata: {"delta":"S$535.00 left."}\r\n\r\n',
  'event: done\r\ndata: {"request_id":"r_1","verified":true,"fallback":false,"answer":"You have S$535.00 left."}\r\n\r\n',
].join('')

test('assistant SSE exposes ack, facts, incremental text and a valid completion', async () => {
  const seen = []
  let answer = ''
  const stream = body(events, true)
  await consumeAssistantSSE(stream, {
    onAck: () => seen.push('ack'),
    onFacts: (facts) => { seen.push('facts'); assert.equal(facts.numbers.remaining_cents, 53500) },
    onStream: ({ delta }) => { seen.push('stream'); answer += delta },
    onDone: () => seen.push('done'),
  })
  assert.deepEqual(seen, ['ack', 'facts', 'stream', 'stream', 'done'])
  assert.equal(answer, 'You have S$535.00 left.')
  assert.equal(stream.locked, false)
})

test('assistant SSE accepts a fallback completion and releases the reader', async () => {
  const stream = body('event: done\ndata: {"request_id":"r_2","verified":true,"fallback":true,"answer":"Rule advice"}\n\n')
  let fallback = false
  await consumeAssistantSSE(stream, { onDone: (done) => { fallback = done.fallback } })
  assert.equal(fallback, true)
  assert.equal(stream.locked, false)
})

test('assistant SSE rejects incomplete completion and terminal stream errors', async () => {
  await assert.rejects(
    consumeAssistantSSE(body('event: done\ndata: {"request_id":"r_3"}\n\n')),
    /Incomplete assistant completion event/,
  )
  await assert.rejects(
    consumeAssistantSSE(body('event: error\ndata: {"code":"internal","message":"stopped"}\n\n')),
    /stopped/,
  )
})

test('assistant SSE rejects a stream that closes before done', async () => {
  const stream = body('event: ack\ndata: {"request_id":"r_4"}\n\n')
  await assert.rejects(consumeAssistantSSE(stream), /before the assistant finished/)
  assert.equal(stream.locked, false)
})
