const SINGAPORE_TIME_ZONE = 'Asia/Singapore'

export function formatMoney(cents, { showSign = false, compact = false } = {}) {
  const numeric = Number(cents)
  if (!Number.isFinite(numeric)) return '—'
  const dollars = numeric / 100
  const sign = showSign && dollars > 0 ? '+' : ''
  return `${sign}${new Intl.NumberFormat('en-SG', {
    style: 'currency',
    currency: 'SGD',
    currencyDisplay: 'narrowSymbol',
    minimumFractionDigits: compact && Number.isInteger(dollars) ? 0 : 2,
    maximumFractionDigits: 2,
  }).format(dollars).replace('$', 'S$')}`
}

export function formatPercent(basisPoints, digits = 0) {
  const numeric = Number(basisPoints)
  if (!Number.isFinite(numeric)) return '—'
  return `${(numeric / 100).toFixed(digits)}%`
}

export function centsFromDollars(value) {
  if (value === '' || value === null || value === undefined) return null
  const normalized = String(value).replace(/[S$, \s]/g, '')
  if (!/^\d+(\.\d{0,2})?$/.test(normalized)) return null
  const [whole, fraction = ''] = normalized.split('.')
  const cents = Number(whole) * 100 + Number(fraction.padEnd(2, '0'))
  return Number.isSafeInteger(cents) ? cents : null
}

export function dollarsFromCents(cents) {
  const numeric = Number(cents)
  if (!Number.isFinite(numeric)) return ''
  return (numeric / 100).toFixed(2)
}

function singaporeParts(date = new Date()) {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: SINGAPORE_TIME_ZONE,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(date)
  return Object.fromEntries(parts.map((part) => [part.type, part.value]))
}

export function singaporeToday(date = new Date()) {
  const { year, month, day } = singaporeParts(date)
  return `${year}-${month}-${day}`
}

export function singaporeMonth(date = new Date()) {
  return singaporeToday(date).slice(0, 7)
}

export function formatMonth(month) {
  if (!/^\d{4}-\d{2}$/.test(month || '')) return month || '—'
  const [year, numericMonth] = month.split('-').map(Number)
  return new Intl.DateTimeFormat('en-SG', { month: 'long', year: 'numeric', timeZone: 'UTC' })
    .format(new Date(Date.UTC(year, numericMonth - 1, 1)))
}

export function shiftMonth(month, offset) {
  const [year, numericMonth] = month.split('-').map(Number)
  const shifted = new Date(Date.UTC(year, numericMonth - 1 + offset, 1))
  return `${shifted.getUTCFullYear()}-${String(shifted.getUTCMonth() + 1).padStart(2, '0')}`
}

export function formatDate(dateString, options = {}) {
  if (!dateString) return '—'
  const date = new Date(`${dateString}T00:00:00+08:00`)
  if (Number.isNaN(date.getTime())) return dateString
  return new Intl.DateTimeFormat('en-SG', {
    day: 'numeric',
    month: 'short',
    year: options.includeYear ? 'numeric' : undefined,
    timeZone: SINGAPORE_TIME_ZONE,
  }).format(date)
}

export function getErrorMessage(error, fallback = 'Something went wrong. Please try again.') {
  const payload = error?.response?.data?.error
  if (typeof payload === 'string') return payload
  if (payload?.message) return payload.message
  if (error?.message && error.message !== 'Network Error') return error.message
  if (error?.message === 'Network Error') return 'Unable to reach the server. Check your connection and try again.'
  return fallback
}

export function clamp(value, minimum = 0, maximum = 100) {
  return Math.min(maximum, Math.max(minimum, Number(value) || 0))
}
