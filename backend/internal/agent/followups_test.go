package agent

import "testing"

func usage(v int) *int { return &v }

func TestFollowUpsUseFocusCategoryAndStayAnswerable(t *testing.T) {
	facts := Facts{Categories: []CategoryFact{
		{Category: CategoryFood, BudgetCents: 40000, SpentCents: 650, UsageBP: usage(162)},
		{Category: CategoryTransport, BudgetCents: 10000, SpentCents: 4200, UsageBP: usage(4200)},
	}}
	got := followUps(ParseQuery("can you introduce my current status?"), facts)
	if len(got) != 3 || got[0] != "How much can I still spend on transport?" {
		t.Fatalf("overview follow-ups: %v", got)
	}
	for _, q := range got {
		if parsed := ParseQuery(q); parsed.Intent == IntentOther && parsed.Category == "" {
			t.Fatalf("follow-up %q does not parse to a supported question", q)
		}
	}
	got = followUps(ParseQuery("How much can I still spend on food this month?"), facts)
	if got[0] != "How can I cut back on food?" {
		t.Fatalf("category follow-ups should keep the asked category: %v", got)
	}
	aff := ParseQuery("Can I afford an S$80 headphone?")
	got = followUps(aff, Facts{Affordability: &Affordability{Category: CategoryStudyDigital}})
	if got[0] != "How much can I still spend on study and digital?" || got[1] != "Will I overspend this month?" {
		t.Fatalf("affordability follow-ups: %v", got)
	}
}

func TestFollowUpsEmptyWhenDataMissing(t *testing.T) {
	if got := followUps(ParseQuery("Where did my money go?"), Facts{Missing: []string{"budget"}}); len(got) != 0 {
		t.Fatalf("expected no follow-ups, got %v", got)
	}
}
