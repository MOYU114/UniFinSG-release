import { recordEvaluationEvent } from '../api/evaluation'

const PARTICIPANT_KEY = 'unifinsg:participant-code'
const VALID_PARTICIPANT = /^P\d{2}$/

export function initialiseParticipant(search = globalThis.location?.search || '') {
  if (typeof sessionStorage === 'undefined') return ''
  const code = new URLSearchParams(search).get('pid')?.toUpperCase() || ''
  if (VALID_PARTICIPANT.test(code)) sessionStorage.setItem(PARTICIPANT_KEY, code)
  return participantCode()
}

export function participantCode() {
  if (typeof sessionStorage === 'undefined') return ''
  const value = sessionStorage.getItem(PARTICIPANT_KEY) || ''
  return VALID_PARTICIPANT.test(value) ? value : ''
}

export async function sendTaskEvent(task, event, options = {}) {
  const code = participantCode()
  if (!code) return false
  const payload = {
    participant_code: code,
    task,
    event,
    client_ts: new Date().toISOString(),
  }
  if (event === 'end') {
    payload.success = Boolean(options.success)
    payload.error_code = options.errorCode || null
  }
  try {
    await recordEvaluationEvent(payload)
    return true
  } catch {
    return false
  }
}
