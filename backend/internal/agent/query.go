package agent

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var amountPattern = regexp.MustCompile(`(?i)(?:s\$|sgd\s*|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)

// ParseQuery performs deterministic intent and entity extraction. Unknown
// affordability categories deliberately use entertainment_other, the only
// category that is safe as a general fallback.
func ParseQuery(question string) Query {
	lower := strings.ToLower(question)
	words := wordPattern.FindAllString(lower, -1)
	intent := IntentOther
	switch {
	case hasPhrase(lower, "afford", "can i buy", "can i spend", "should i buy", "is it ok to buy"):
		intent = IntentAffordability
	case hasPhrase(lower, "overspend", "over budget", "too much", "on track", "run out"):
		intent = IntentOverspend
	case hasPhrase(lower, "save money", "how can i save", "how do i save") || hasWord(words, "tip", "suggest", "advice", "advise", "recommend"):
		intent = IntentTips
	case hasPhrase(lower, "how am i", "how is my", "how are my", "where did my money", "money go") || hasWord(words, "overview", "summary", "summarise", "summarize", "status", "introduce", "situation", "breakdown"):
		intent = IntentOverview
	}
	q := Query{Intent: intent}
	q.AmountCents, q.AmountFound, q.AmountOutOfRange = extractAmountDetails(question)
	q.Category = detectCategory(words)
	if q.Category == "" && intent == IntentAffordability {
		q.Category = CategoryEntertainmentOther
	}
	return q
}

var wordPattern = regexp.MustCompile(`[a-z]+`)

func hasPhrase(lower string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// hasWord reports whether any word starts with one of the stems, so "eating"
// matches "eat" but "current" does not match "rent".
func hasWord(words []string, stems ...string) bool {
	for _, word := range words {
		for _, stem := range stems {
			if strings.HasPrefix(word, stem) {
				return true
			}
		}
	}
	return false
}

func extractAmountCents(question string) int64 {
	amount, _, _ := extractAmountDetails(question)
	return amount
}

func extractAmountDetails(question string) (int64, bool, bool) {
	m := amountPattern.FindStringSubmatch(question)
	if len(m) != 2 {
		return 0, false, false
	}
	clean := strings.ReplaceAll(m[1], ",", "")
	parts := strings.SplitN(clean, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, true, true
	}
	if whole > 1_000_000 {
		return 0, true, true
	}
	frac := int64(0)
	if len(parts) == 2 {
		f := parts[1]
		if len(f) == 1 {
			f += "0"
		}
		frac, err = strconv.ParseInt(f, 10, 64)
		if err != nil {
			return 0, true, true
		}
	}
	if whole > (math.MaxInt64-frac)/100 {
		return 0, true, true
	}
	return whole*100 + frac, true, false
}

func detectCategory(words []string) Category {
	switch {
	case hasWord(words, "food", "meal", "eat", "restaurant", "coffee", "lunch", "dinner", "breakfast", "hawker", "canteen", "drink", "snack"):
		return CategoryFood
	case hasWord(words, "transport", "bus", "train", "mrt", "grab", "taxi", "commute"):
		return CategoryTransport
	case hasWord(words, "rent", "housing", "tuition", "hostel", "dorm"):
		return CategoryHousingTuition
	case hasWord(words, "shopping", "clothes", "grocer", "supermarket"):
		return CategoryDailyShopping
	case hasWord(words, "study", "book", "headphone", "laptop", "software", "digital", "phone"):
		return CategoryStudyDigital
	case hasWord(words, "entertain", "movie", "game", "travel", "trip", "holiday", "concert", "other"):
		return CategoryEntertainmentOther
	default:
		return ""
	}
}
