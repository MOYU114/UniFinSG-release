import http from './http'

export async function getCredits(userId) {
  const { data } = await http.get(`/users/${userId}/agent/credits`)
  return data
}
