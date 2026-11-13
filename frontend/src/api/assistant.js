import http from './http'

// The v1 assistant chat endpoints (ask / history / quota) are gone in 2.0.
// Only the rule-based advice feed remains, for the dashboard.
export async function getAssistantAdvice(userId, month) {
  const { data } = await http.get(`/users/${userId}/assistant/advice`, { params: { month } })
  return data
}
