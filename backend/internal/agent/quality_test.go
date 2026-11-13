package agent

import (
	"strings"
	"testing"
)

func TestParseQueryMatchesWholeWords(t *testing.T) {
	q := ParseQuery("can you introduce my current status?")
	if q.Intent != IntentOverview || q.Category != "" {
		t.Fatalf("status question parsed as %+v", q)
	}
	q = ParseQuery("how much shall i spend on eating if i want to travel to china")
	if q.Category != CategoryFood {
		t.Fatalf("eating should map to food, got %+v", q)
	}
	q = ParseQuery("Can I afford an S$80 headphone?")
	if q.Intent != IntentAffordability || q.Category != CategoryStudyDigital || q.AmountCents != 8000 {
		t.Fatalf("affordability parse changed: %+v", q)
	}
	if q := ParseQuery("Where did my money go this month?"); q.Intent != IntentOverview {
		t.Fatalf("where-did-my-money question should be overview, got %+v", q)
	}
}

func TestRedactPIIKeepsAmounts(t *testing.T) {
	in := "My name is Tan Wei, email tan@u.nus.edu, phone +65 9123 4567, NRIC S1234567D. Can I afford S$80 headphones?"
	out := RedactPII(in)
	for _, leaked := range []string{"Tan Wei", "tan@u.nus.edu", "9123", "S1234567D"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("redacted text still contains %q: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "S$80") {
		t.Fatalf("amount must be kept: %s", out)
	}
	if got := RedactPII("I am going to travel to China"); got != "I am going to travel to China" {
		t.Fatalf("ordinary text changed: %s", got)
	}
}

func TestBuildPromptSendsRedactedQuestion(t *testing.T) {
	msgs := buildPrompt(Facts{}, []HistoryMessage{{Role: "user", Content: "ping me at a@b.com about food"}}, "How is my food budget?")
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "How is my food budget?") || !strings.Contains(last, "category=food") {
		t.Fatalf("question missing from prompt: %s", last)
	}
	if hist := msgs[2].Content; strings.Contains(hist, "a@b.com") || !strings.Contains(hist, "about food") {
		t.Fatalf("history not redacted correctly: %s", hist)
	}
}

func TestVerifierAcceptsCategoryRemaining(t *testing.T) {
	facts := Facts{Numbers: FactsNumbers{TotalBudgetCents: 100000, TotalSpentCents: 650, RemainingCents: 99350}, Categories: []CategoryFact{{Category: CategoryFood, BudgetCents: 40000, SpentCents: 650, RemainingCents: 39350}}}
	if !verifyAnswer("Your Food budget is S$400.00, you've spent S$6.50, leaving S$393.50. "+Disclaimer, facts) {
		t.Fatal("category remaining should verify")
	}
	if verifyAnswer("That is about S$13.12 per day.", facts) {
		t.Fatal("invented numbers must still fail")
	}
}
