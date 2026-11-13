import { parseSSEChunk } from './sse.js'

export const VERIFIED_HINT = 'Some numbers could not be checked against your ledger.'

// Tool names are the same identifiers the backend registry exposes.
export function toolLabel(name, args = {}) {
  switch (name) {
    case 'get_budget_snapshot':
      return args?.month ? `Checked the ${args.month} budget snapshot` : 'Checked the budget snapshot'
    case 'list_transactions':
      return args?.category ? `Listed ${args.category} transactions` : 'Listed recent transactions'
    case 'list_categories':
      return 'Listed the spending categories'
    case 'propose_transactions':
      return 'Prepared expenses for confirmation'
    case 'propose_update_transaction':
      return 'Prepared an expense change'
    case 'propose_delete_transaction':
      return 'Prepared an expense deletion'
    case 'propose_update_budget':
      return 'Prepared a budget change'
    default:
      return name ? `Used ${name}` : 'Used a tool'
  }
}

// toolResultSummary mirrors the backend tool_result summaries so a reloaded
// conversation shows the same one-line description.
export function toolResultSummary(name, result, isError) {
  if (isError) return `${name} could not run`
  if (name === 'get_budget_snapshot') {
    return result?.month ? `Checked the ${result.month} budget snapshot` : 'Checked the budget snapshot'
  }
  if (name === 'list_transactions') {
    const count = Number.isFinite(result?.count) ? result.count : 0
    return result?.month ? `Listed ${count} transactions for ${result.month}` : `Listed ${count} transactions`
  }
  if (name === 'list_categories') return 'Listed the 6 spending categories'
  if (name.startsWith('propose_')) return 'Waiting for your confirmation'
  return name ? `Ran ${name}` : 'Ran a tool'
}

// verified === null means the turn called no tools, so there is nothing to
// warn about. Only an explicit false renders the hint.
const MAX_SUGGESTIONS = 3

// normalizeSuggestions keeps the follow-up questions from a `done` event that
// are safe to render as buttons: non-empty, distinct strings, at most three.
export function normalizeSuggestions(raw) {
  if (!Array.isArray(raw)) return []
  const seen = new Set()
  const out = []
  for (const item of raw) {
    if (typeof item !== 'string') continue
    const text = item.trim()
    const key = text.toLowerCase()
    if (!text || seen.has(key)) continue
    seen.add(key)
    out.push(text)
    if (out.length === MAX_SUGGESTIONS) break
  }
  return out
}

export function verifiedHint(verified) {
  return verified === false ? VERIFIED_HINT : ''
}

// consumeAgentSSE dispatches the named events of the agent stream. An `error`
// event does not throw: the server still sends `done` afterwards so the UI can
// keep the partial answer and the final identifiers.
export async function consumeAgentSSE(body, handlers = {}) {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let completed = false
  let streamError = null
  try {
    while (!completed) {
      const { value, done } = await reader.read()
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true })
      let match
      while ((match = /\r?\n\r?\n/.exec(buffer))) {
        const chunk = buffer.slice(0, match.index)
        buffer = buffer.slice(match.index + match[0].length)
        if (!chunk.trim()) continue
        const { event, data } = parseSSEChunk(chunk)
        if (event === 'ack') handlers.onAck?.(data)
        else if (event === 'status') handlers.onStatus?.(data)
        else if (event === 'tool_call') handlers.onToolCall?.(data)
        else if (event === 'tool_result') handlers.onToolResult?.(data)
        else if (event === 'delta') handlers.onDelta?.(data)
        else if (event === 'draft') handlers.onDraft?.(data)
        else if (event === 'error') {
          streamError = data
          handlers.onError?.(data)
        } else if (event === 'done') {
          if (!data || typeof data.status !== 'string') throw new Error('Incomplete completion event')
          handlers.onDone?.(data)
          completed = true
          break
        }
      }
      if (done) break
    }
    if (!completed) throw new Error('Connection ended before the assistant finished. Reload the conversation to see the saved reply.')
    return streamError
  } finally {
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
