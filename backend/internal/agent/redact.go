package agent

import "regexp"

var piiPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`), "[email]"},
	{regexp.MustCompile(`(?i)https?://\S+`), "[link]"},
	{regexp.MustCompile(`(?i)\b[STFGM]\d{7}[A-Z]\b`), "[id]"},
	{regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`), "[card]"},
	{regexp.MustCompile(`(?:\+65[ -]?)?\b[689]\d{3}[ -]?\d{4}\b`), "[phone]"},
	{regexp.MustCompile(`(?i)\bmy name is\s+[a-z]+(?:\s+[a-z]+)?`), "my name is [name]"},
}

// RedactPII removes common personal identifiers (email, links, NRIC/FIN, card
// and Singapore phone numbers, "my name is ...") before a question leaves
// the backend. Money amounts such as S$80 are kept.
func RedactPII(text string) string {
	for _, p := range piiPatterns {
		text = p.pattern.ReplaceAllString(text, p.replacement)
	}
	return text
}
