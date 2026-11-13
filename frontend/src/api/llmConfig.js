import http from './http'

// The server probes upstream with a 30s budget and the transport allows 35s,
// so these calls must outlast the default 20s client timeout.
const LONG_TIMEOUT = 40000

export async function getLLMConfig(userId) {
  const { data } = await http.get(`/users/${userId}/llm-config`, { timeout: LONG_TIMEOUT })
  return data
}

export async function saveLLMConfig(userId, payload) {
  const { data } = await http.put(`/users/${userId}/llm-config`, payload, { timeout: LONG_TIMEOUT })
  return data
}

export async function testLLMConfig(userId, payload) {
  const { data } = await http.post(`/users/${userId}/llm-config/test`, payload, { timeout: LONG_TIMEOUT })
  return data
}

export async function deleteLLMConfig(userId) {
  await http.delete(`/users/${userId}/llm-config`)
}
