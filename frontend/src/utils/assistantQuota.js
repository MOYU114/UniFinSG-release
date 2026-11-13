// Turns the backend quota payload into the short label shown above the
// assistant input. Returns '' when the quota is unknown so nothing renders.
export function quotaLabel(quota) {
  const limit = Number(quota?.daily_limit)
  const remaining = Number(quota?.daily_remaining)
  if (!Number.isFinite(limit) || limit < 1 || !Number.isFinite(remaining)) return ''
  const left = Math.min(Math.max(remaining, 0), limit)
  if (left === 0) return 'No questions left today · resets at midnight (SGT)'
  return `${left} of ${limit} ${limit === 1 ? 'question' : 'questions'} left today`
}

export function quotaExhausted(quota) {
  return Number.isFinite(Number(quota?.daily_remaining)) && Number(quota.daily_remaining) <= 0
}
