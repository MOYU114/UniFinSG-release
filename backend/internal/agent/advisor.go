package agent

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func money(cents int64) string {
	neg := cents < 0
	var magnitude uint64
	if neg {
		magnitude = uint64(-(cents + 1)) + 1
	} else {
		magnitude = uint64(cents)
	}
	value := fmt.Sprintf("S$%s.%02d", commaNumber(magnitude/100), magnitude%100)
	if neg {
		return "-" + value
	}
	return value
}

func commaNumber(value uint64) string {
	raw := strconv.FormatUint(value, 10)
	if len(raw) <= 3 {
		return raw
	}
	var b strings.Builder
	first := len(raw) % 3
	if first == 0 {
		first = 3
	}
	b.WriteString(raw[:first])
	for i := first; i < len(raw); i += 3 {
		b.WriteByte(',')
		b.WriteString(raw[i : i+3])
	}
	return b.String()
}

func categoryLabel(category Category) string {
	switch category {
	case CategoryFood:
		return "Food"
	case CategoryTransport:
		return "Transport"
	case CategoryHousingTuition:
		return "Housing / Tuition"
	case CategoryDailyShopping:
		return "Daily Shopping"
	case CategoryStudyDigital:
		return "Study / Digital"
	case CategoryEntertainmentOther:
		return "Entertainment / Other"
	default:
		return string(category)
	}
}

// RuleAdvice reads values from the snapshot and only formats or compares
// them. Any derived overage is included in numbers_cents for verification.
func RuleAdviceFor(snapshot *Snapshot, aff *Affordability) []RuleAdvice {
	if snapshot == nil {
		return nil
	}
	result := make([]RuleAdvice, 0, 3)
	for _, stat := range snapshot.Categories {
		if stat.Status == StatusOver {
			overage := int64(0)
			if stat.SpentCents > stat.BudgetCents && stat.BudgetCents >= 0 {
				overage = stat.SpentCents - stat.BudgetCents
			}
			result = append(result, RuleAdvice{ID: string(stat.Category) + "_over", Text: fmt.Sprintf("%s is over its %s budget.", categoryLabel(stat.Category), money(stat.BudgetCents)), NumbersCents: []int64{stat.BudgetCents, stat.SpentCents, overage}})
		} else if stat.Status == StatusWarn {
			result = append(result, RuleAdvice{ID: string(stat.Category) + "_warn", Text: fmt.Sprintf("%s is at %s of its %s budget.", categoryLabel(stat.Category), money(stat.SpentCents), money(stat.BudgetCents)), NumbersCents: []int64{stat.SpentCents, stat.BudgetCents}})
		}
		if len(result) >= 2 {
			break
		}
	}
	if snapshot.ProjectedSpentCents != nil && snapshot.TotalBudgetCents >= 0 && *snapshot.ProjectedSpentCents > snapshot.TotalBudgetCents {
		overage := *snapshot.ProjectedSpentCents - snapshot.TotalBudgetCents
		if overage >= 0 && overage <= math.MaxInt64 {
			result = append(result, RuleAdvice{ID: "projection_over", Text: fmt.Sprintf("At your current pace you may spend %s this month, %s over budget.", money(*snapshot.ProjectedSpentCents), money(overage)), NumbersCents: []int64{*snapshot.ProjectedSpentCents, overage}})
		}
	}
	if aff != nil {
		text := fmt.Sprintf("A %s purchase in %s would leave %s.", money(aff.AmountCents), categoryLabel(aff.Category), money(aff.RemainingAfterCents))
		if aff.ExceedsTotal {
			text = fmt.Sprintf("A %s purchase would exceed your total remaining budget.", money(aff.AmountCents))
		}
		result = append(result, RuleAdvice{ID: "affordability", Text: text, NumbersCents: []int64{aff.AmountCents, aff.RemainingAfterCents}})
	}
	if len(result) == 0 && len(snapshot.Missing) == 0 {
		result = append(result, RuleAdvice{ID: "on_track", Text: fmt.Sprintf("You have %s remaining this month.", money(snapshot.RemainingCents)), NumbersCents: []int64{snapshot.RemainingCents}})
	}
	if len(result) > 3 {
		result = result[:3]
	}
	return result
}

func fallbackAnswer(advice []RuleAdvice, missing []string) string {
	if len(missing) > 0 {
		return fmt.Sprintf("I need your %s before I can give a complete budgeting answer. %s", joinMissing(missing), Disclaimer)
	}
	if len(advice) == 0 {
		return "I do not have enough information to make a budgeting suggestion. " + Disclaimer
	}
	answer := advice[0].Text
	for _, item := range advice[1:] {
		answer += " " + item.Text
	}
	return answer + " " + Disclaimer
}

func joinMissing(missing []string) string {
	if len(missing) == 1 {
		return missing[0]
	}
	if len(missing) == 2 {
		return missing[0] + " and " + missing[1]
	}
	return missing[0] + ", " + missing[1] + " and " + missing[2]
}
