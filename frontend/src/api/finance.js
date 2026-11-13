import http from './http'

export async function getProfile(userId) {
  const { data } = await http.get(`/users/${userId}/profile`)
  return data
}

export async function saveProfile(userId, payload) {
  const { data } = await http.put(`/users/${userId}/profile`, payload)
  return data
}

export async function getBudget(userId, month) {
  const { data } = await http.get(`/users/${userId}/budgets/${month}`)
  return data
}

export async function saveBudget(userId, month, payload) {
  const { data } = await http.put(`/users/${userId}/budgets/${month}`, payload)
  return data
}

export async function getDashboard(userId, month) {
  const { data } = await http.get(`/users/${userId}/dashboard`, { params: { month } })
  return data
}

export async function listTransactions(userId, params) {
  const { data } = await http.get(`/users/${userId}/transactions`, { params })
  return data
}

export async function createTransaction(userId, payload) {
  const { data } = await http.post(`/users/${userId}/transactions`, payload)
  return data
}

export async function updateTransaction(userId, transactionId, payload) {
  const { data } = await http.patch(`/users/${userId}/transactions/${transactionId}`, payload)
  return data
}

export async function deleteTransaction(userId, transactionId) {
  await http.delete(`/users/${userId}/transactions/${transactionId}`)
}
