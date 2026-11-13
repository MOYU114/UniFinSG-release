import http from './http'

export async function registerUser(payload) {
  const { data } = await http.post('/users/register', payload)
  return data
}

export async function loginUser(payload) {
  const { data } = await http.post('/users/login', payload)
  return data
}

export async function getCurrentUser() {
  const { data } = await http.get('/me')
  return data
}

export async function logoutUser() {
  const { data } = await http.post('/auth/logout')
  return data
}

export async function deleteUser(userId, confirm = 'DELETE') {
  await http.delete(`/users/${userId}`, { data: { confirm } })
}
