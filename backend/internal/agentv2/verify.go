package agentv2

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// moneyPattern matches S$12.50, $12.5, SGD 12.50, 12.50 元 and 12.5 块.
var moneyPattern = regexp.MustCompile(`(?i)(?:s\$|sgd\s*|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)|([0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*(?:元|块)`)

// Verify checks every amount in the answer against the tool results of the
// turn (design section 6.5). It returns nil when the turn called no tools,
// because there is nothing to compare against.
func Verify(answer string, toolResults []json.RawMessage) *bool {
	if len(toolResults) == 0 {
		return nil
	}
	allowed := make(map[int64]struct{})
	for _, raw := range toolResults {
		collectCents(raw, allowed)
	}
	amounts := extractAmounts(answer)
	if len(amounts) == 0 {
		verified := true
		return &verified
	}
	// Two-operand sums and differences are accepted because the model is
	// allowed to combine two tool figures (for example spent + planned).
	pairs := make(map[int64]struct{}, len(allowed)*len(allowed))
	for left := range allowed {
		for right := range allowed {
			pairs[left+right] = struct{}{}
			pairs[left-right] = struct{}{}
		}
	}
	verified := true
	for _, amount := range amounts {
		if _, ok := allowed[amount]; ok {
			continue
		}
		if _, ok := pairs[amount]; ok {
			continue
		}
		verified = false
		break
	}
	return &verified
}

// collectCents walks a tool result and records every *_cents value.
func collectCents(raw json.RawMessage, out map[int64]struct{}) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return
	}
	walkCents(value, out)
}

func walkCents(value any, out map[int64]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.HasSuffix(key, "_cents") {
				if cents, ok := centsValue(child); ok {
					out[cents] = struct{}{}
				}
			}
			walkCents(child, out)
		}
	case []any:
		for _, child := range typed {
			walkCents(child, out)
		}
	}
}

func centsValue(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if number > math.MaxInt64 || number < math.MinInt64 {
		return 0, false
	}
	return int64(math.Round(number)), true
}

// extractAmounts returns the amounts in the answer as integer cents.
func extractAmounts(answer string) []int64 {
	matches := moneyPattern.FindAllStringSubmatch(answer, -1)
	out := make([]int64, 0, len(matches))
	for _, match := range matches {
		raw := match[1]
		if raw == "" {
			raw = match[2]
		}
		if cents, ok := parseCents(raw); ok {
			out = append(out, cents)
		}
	}
	return out
}

func parseCents(raw string) (int64, bool) {
	clean := strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	if clean == "" {
		return 0, false
	}
	parts := strings.SplitN(clean, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, false
	}
	fraction := int64(0)
	if len(parts) == 2 {
		digits := parts[1]
		if len(digits) == 1 {
			digits += "0"
		}
		fraction, err = strconv.ParseInt(digits, 10, 64)
		if err != nil || fraction < 0 || fraction > 99 {
			return 0, false
		}
	}
	if whole > (math.MaxInt64-fraction)/100 {
		return 0, false
	}
	return whole*100 + fraction, true
}
