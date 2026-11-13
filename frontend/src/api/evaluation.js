import http from './http'

export async function recordEvaluationEvent(payload) {
  const { data } = await http.post('/eval/events', payload)
  return data
}
