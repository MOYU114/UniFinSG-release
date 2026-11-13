export const CATEGORIES = [
  { value: 'food', label: 'Food', shortLabel: 'Food', icon: '🍜', color: '#e76f51', soft: '#fff0eb' },
  { value: 'transport', label: 'Transport', shortLabel: 'Transport', icon: '🚌', color: '#2a9d8f', soft: '#e8f7f4' },
  { value: 'housing_tuition', label: 'Housing / Tuition', shortLabel: 'Housing', icon: '🏠', color: '#457b9d', soft: '#edf4f8' },
  { value: 'daily_shopping', label: 'Daily Shopping', shortLabel: 'Shopping', icon: '🛍️', color: '#e9a23b', soft: '#fff7e8' },
  { value: 'study_digital', label: 'Study / Digital', shortLabel: 'Study', icon: '💻', color: '#7457c5', soft: '#f2effb' },
  { value: 'entertainment_other', label: 'Entertainment / Other', shortLabel: 'Other', icon: '🎧', color: '#d65a8a', soft: '#fceef4' },
]

export const CATEGORY_MAP = Object.fromEntries(CATEGORIES.map((category) => [category.value, category]))

export const LOCATIONS = [
  { value: 'utown', label: 'UTown' },
  { value: 'kent_ridge_central', label: 'Kent Ridge (Central)' },
  { value: 'science', label: 'Science' },
  { value: 'engineering', label: 'Engineering' },
  { value: 'biz_computing', label: 'BIZ / Computing' },
  { value: 'bukit_timah', label: 'Bukit Timah' },
  { value: 'clementi_west_coast', label: 'Clementi / West Coast' },
  { value: 'online', label: 'Online' },
  { value: 'islandwide', label: 'Islandwide' },
]

export const LOCATION_MAP = Object.fromEntries(LOCATIONS.map((location) => [location.value, location.label]))

export const HOUSING_OPTIONS = [
  { value: 'on_campus', label: 'On campus', description: 'Hall, residential college or student residence' },
  { value: 'off_campus', label: 'Off campus', description: 'Renting privately near or away from campus' },
  { value: 'with_family', label: 'With family', description: 'Living at home with family' },
]

export const SUGGESTED_BUDGET_WEIGHTS = {
  food: 40,
  transport: 10,
  housing_tuition: 0,
  daily_shopping: 15,
  study_digital: 15,
  entertainment_other: 20,
}

export function allocateBudget(totalCents, weights = SUGGESTED_BUDGET_WEIGHTS) {
  const total = Math.max(0, Number(totalCents) || 0)
  const entries = CATEGORIES.map((category) => ({
    category: category.value,
    budget_cents: Math.floor((total * (weights[category.value] || 0)) / 100),
  }))
  const allocated = entries.reduce((sum, entry) => sum + entry.budget_cents, 0)
  if (entries.length) entries[0].budget_cents += total - allocated
  return entries
}

export function allocateEvenly(totalCents) {
  const total = Math.max(0, Number(totalCents) || 0)
  const base = Math.floor(total / CATEGORIES.length)
  return CATEGORIES.map((category, index) => ({
    category: category.value,
    budget_cents: base + (index < total - base * CATEGORIES.length ? 1 : 0),
  }))
}
