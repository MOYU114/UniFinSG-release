package agent

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var answerNumberPattern = regexp.MustCompile(`(?i)(?:s\$|sgd\s*|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)|([0-9]+(?:\.[0-9]+)?)\s*%`)

func verifyAnswer(answer string, facts Facts) bool {
	allowedCents := map[int64]bool{}
	for _, n := range facts.RuleAdvice {
		for _, v := range n.NumbersCents {
			allowedCents[v] = true
		}
	}
	for _, category := range facts.Categories {
		allowedCents[category.BudgetCents] = true
		allowedCents[category.SpentCents] = true
		allowedCents[category.RemainingCents] = true
		if category.RemainingCents < 0 {
			allowedCents[-category.RemainingCents] = true
		}
	}
	allowedCents[facts.Numbers.TotalBudgetCents] = true
	allowedCents[facts.Numbers.TotalSpentCents] = true
	allowedCents[facts.Numbers.RemainingCents] = true
	if facts.Numbers.ProjectedSpentCents != nil {
		allowedCents[*facts.Numbers.ProjectedSpentCents] = true
	}
	if facts.Affordability != nil {
		allowedCents[facts.Affordability.AmountCents] = true
		allowedCents[facts.Affordability.RemainingAfterCents] = true
	}
	for _, m := range answerNumberPattern.FindAllStringSubmatch(answer, -1) {
		if m[1] != "" {
			clean := strings.ReplaceAll(m[1], ",", "")
			parts := strings.SplitN(clean, ".", 2)
			whole, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				return false
			}
			frac := int64(0)
			if len(parts) == 2 {
				f := parts[1]
				if len(f) == 1 {
					f += "0"
				}
				frac, err = strconv.ParseInt(f, 10, 64)
				if err != nil {
					return false
				}
			}
			if whole > (math.MaxInt64-frac)/100 || !allowedCents[whole*100+frac] {
				return false
			}
		} else if m[2] != "" {
			value, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				return false
			}
			bp := int64(value*100 + 0.5)
			known := false
			if facts.UsageBP != nil && bp == int64(*facts.UsageBP) {
				known = true
			}
			if facts.Affordability != nil && facts.Affordability.CategoryUsageAfterBP != nil && bp == int64(*facts.Affordability.CategoryUsageAfterBP) {
				known = true
			}
			for _, category := range facts.Categories {
				if category.UsageBP != nil && bp == int64(*category.UsageBP) {
					known = true
					break
				}
			}
			if !known {
				return false
			}
		}
	}
	return true
}
