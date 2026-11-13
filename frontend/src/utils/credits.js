const SINGAPORE_TIME_ZONE = 'Asia/Singapore'

function number(value) {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : null
}

export function creditsLabel(credits) {
  if (!credits) return ''
  if (credits.mode === 'byok') return 'Using your own model'
  const used = number(credits.used)
  const grant = number(credits.daily_grant)
  if (used === null || grant === null || grant <= 0) return ''
  const remaining = Math.max(0, grant - used)
  return `Today ${remaining} / ${grant}`
}

export function creditsResetLabel(resetsAt) {
  if (!resetsAt) return ''
  const date = new Date(resetsAt)
  if (Number.isNaN(date.getTime())) return ''
  const time = new Intl.DateTimeFormat('en-SG', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZone: SINGAPORE_TIME_ZONE,
  }).format(date)
  return `Resets at ${time} (SGT)`
}

export function creditsCostLabel(credits) {
  const costs = credits?.costs
  if (!costs) return ''
  const parts = []
  if (number(costs.chat) !== null) parts.push(`Chat ${costs.chat}`)
  if (number(costs.tool) !== null) parts.push(`Tool use ${costs.tool}`)
  if (number(costs.vision) !== null) parts.push(`Screenshot ${costs.vision}`)
  return parts.join(' · ')
}

export function creditsPopoverLines(credits) {
  if (!credits) return []
  if (credits.mode === 'byok') {
    return ['Your own API key is used for every request.', 'No daily credit limit.']
  }
  return [creditsCostLabel(credits), creditsResetLabel(credits.resets_at)].filter(Boolean)
}
