package agent

import "strings"

var followUpCategoryNames = map[Category]string{
	CategoryFood:               "food",
	CategoryTransport:          "transport",
	CategoryHousingTuition:     "housing and tuition",
	CategoryDailyShopping:      "daily shopping",
	CategoryStudyDigital:       "study and digital",
	CategoryEntertainmentOther: "entertainment",
}

const maxFollowUps = 3

// followUps suggests next questions from the intent just answered and the
// facts behind it. They are deterministic, cost no extra LLM call, and are
// phrased so ParseQuery maps them back to a supported intent and category.
func followUps(query Query, facts Facts) []string {
	if len(facts.Missing) > 0 {
		return []string{}
	}
	focus := focusCategory(query, facts)
	name := followUpCategoryNames[focus]
	var candidates []string
	addCategory := func(template string) {
		if name != "" {
			candidates = append(candidates, strings.Replace(template, "%", name, 1))
		}
	}
	switch query.Intent {
	case IntentOverview:
		addCategory("How much can I still spend on %?")
		candidates = append(candidates, "Will I overspend this month?", "Any tips to save money this week?")
	case IntentOverspend:
		addCategory("How can I cut back on %?")
		candidates = append(candidates, "Where did my money go this month?", "Any tips to save money this week?")
	case IntentAffordability:
		addCategory("How much can I still spend on %?")
		candidates = append(candidates, "Will I overspend this month?")
	case IntentTips:
		addCategory("How can I cut back on %?")
		candidates = append(candidates, "Will I overspend this month?", "Where did my money go this month?")
	default:
		if query.Category != "" {
			addCategory("How can I cut back on %?")
		} else {
			addCategory("How much can I still spend on %?")
		}
		candidates = append(candidates, "Where did my money go this month?", "Will I overspend this month?")
	}
	seen := map[string]bool{}
	out := make([]string, 0, maxFollowUps)
	for _, c := range candidates {
		if seen[c] || len(out) == maxFollowUps {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// focusCategory prefers the category the student asked about, then the one
// with the highest budget usage, then the one with the most spending.
func focusCategory(query Query, facts Facts) Category {
	if query.Category != "" {
		return query.Category
	}
	if query.Intent == IntentAffordability && facts.Affordability != nil {
		return facts.Affordability.Category
	}
	var best Category
	bestUsage, bestSpent := -1, int64(-1)
	for _, c := range facts.Categories {
		if c.SpentCents == 0 {
			continue
		}
		usage := -1
		if c.UsageBP != nil {
			usage = *c.UsageBP
		}
		if usage > bestUsage || (usage == bestUsage && c.SpentCents > bestSpent) {
			best, bestUsage, bestSpent = c.Category, usage, c.SpentCents
		}
	}
	return best
}
