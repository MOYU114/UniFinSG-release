import http from './http'
import { getAPIBase, getCSRFCookie } from '../utils/csrf'
import { consumeAgentSSE } from '../utils/agent.js'

const API_BASE_URL = getAPIBase().replace(/\/$/, '')

export async function listSessions(userId, { cursor, limit } = {}) {
  const params = {}
  if (cursor) params.cursor = cursor
  if (limit) params.limit = limit
  const { data } = await http.get(`/users/${userId}/agent/sessions`, { params })
  return data
}

export async function createSession(userId, title) {
  const { data } = await http.post(`/users/${userId}/agent/sessions`, title ? { title } : {})
  return data
}

export async function renameSession(userId, sessionId, title) {
  const { data } = await http.patch(`/users/${userId}/agent/sessions/${sessionId}`, { title })
  return data
}

export async function deleteSession(userId, sessionId) {
  await http.delete(`/users/${userId}/agent/sessions/${sessionId}`)
}

export async function listMessages(userId, sessionId, { before, limit } = {}) {
  const params = {}
  if (before) params.before = before
  if (limit) params.limit = limit
  const { data } = await http.get(`/users/${userId}/agent/sessions/${sessionId}/messages`, { params })
  return data
}

function agentError(response, payload) {
  const details = payload?.error
  const error = new Error(details?.message || `Agent request failed (${response.status}).`)
  error.code = details?.code || 'request_failed'
  error.details = details?.details || null
  error.upstream = details?.upstream || null
  error.retryAfter = Number(response.headers.get('Retry-After')) || null
  error.response = { status: response.status, data: payload }
  return error
}

export async function sendMessage(userId, sessionId, payload, handlers = {}, { signal } = {}) {
  return streamAgent(
    `${API_BASE_URL}/users/${userId}/agent/sessions/${sessionId}/messages`,
    payload, handlers, signal,
  )
}

// uploadURL is the authenticated read endpoint of one private image. It is
// not a public or presigned URL: the backend checks the session cookie and
// ownership on every request.
export function uploadURL(userId, uploadId) {
  return `${API_BASE_URL}/users/${userId}/agent/uploads/${uploadId}`
}

// uploadImage posts one image as multipart form data. XMLHttpRequest is used
// so the composer can show real upload progress.
export function uploadImage(userId, sessionId, file, { onProgress, signal } = {}) {
  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest()
    request.open('POST', `${API_BASE_URL}/users/${userId}/agent/uploads`)
    request.withCredentials = true
    const csrfToken = getCSRFCookie()
    if (csrfToken) request.setRequestHeader('X-CSRF-Token', csrfToken)
    request.upload.onprogress = (event) => {
      if (event.lengthComputable && onProgress) {
        onProgress(Math.round((event.loaded / event.total) * 100))
      }
    }
    request.onload = () => {
      let payload = null
      try {
        payload = JSON.parse(request.responseText)
      } catch {
        // A gateway error page still becomes a useful request error below.
      }
      if (request.status >= 200 && request.status < 300) {
        resolve(payload)
        return
      }
      if (request.status === 401 && typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent('au:unauthorized'))
      }
      const headers = new Headers()
      const retryAfter = request.getResponseHeader('Retry-After')
      if (retryAfter) headers.set('Retry-After', retryAfter)
      reject(agentError({ status: request.status, headers }, payload))
    }
    request.onerror = () => reject(new Error('The image could not be uploaded.'))
    request.onabort = () => reject(new Error('The upload was cancelled.'))
    if (signal) {
      if (signal.aborted) {
        request.abort()
        return
      }
      signal.addEventListener('abort', () => request.abort(), { once: true })
    }
    const form = new FormData()
    form.append('session_id', String(sessionId))
    form.append('file', file, file.name || 'image.jpg')
    request.send(form)
  })
}

// decideDraft confirms or rejects a draft. The server continues the paused
// turn and streams the continuation over the same SSE contract.
export async function decideDraft(userId, draftId, payload, handlers = {}, { signal } = {}) {
  return streamAgent(
    `${API_BASE_URL}/users/${userId}/agent/drafts/${encodeURIComponent(draftId)}/decision`,
    payload, handlers, signal,
  )
}

async function streamAgent(url, payload, handlers = {}, signal) {
  const headers = { 'Content-Type': 'application/json', Accept: 'text/event-stream' }
  const csrfToken = getCSRFCookie()
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken

  const response = await fetch(url, {
    method: 'POST',
    headers,
    credentials: 'include',
    body: JSON.stringify(payload),
    signal,
  })

  if (!response.ok) {
    let errorPayload = null
    try {
      errorPayload = await response.json()
    } catch {
      // A non-JSON gateway response still becomes a useful request error below.
    }
    if (response.status === 401 && typeof window !== 'undefined') {
      window.dispatchEvent(new CustomEvent('au:unauthorized'))
    }
    throw agentError(response, errorPayload)
  }
  if (!response.body) throw new Error('The agent response did not include a stream.')
  return consumeAgentSSE(response.body, handlers)
}
