package agentv2

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFollowUps(t *testing.T) {
	call := func(name string) Part { return Part{Type: PartToolCall, ID: "c", Name: name} }
	cases := []struct {
		name    string
		content string
		parts   []Part
		status  string
		want    []string
	}{
		{"failed turn", "hi", nil, TurnFailed, []string{}},
		{"awaiting approval", "lunch 5", []Part{call("propose_transactions")}, TurnApproval, []string{}},
		{"no tools", "hello", []Part{{Type: PartText, Text: "hi"}}, TurnCompleted,
			[]string{followRemaining, followWhere, followRecord}},
		{"snapshot", "budget?", []Part{call("get_budget_snapshot")}, TurnCompleted,
			[]string{followWhere, followOverspend, followTips}},
		{"transactions win over snapshot", "x", []Part{call("get_budget_snapshot"), call("list_transactions")}, TurnCompleted,
			[]string{followTop, followRemaining, followTips}},
		{"approved draft continuation", "", []Part{{Type: PartToolResult, CallID: "c", Name: "propose_transactions"}}, TurnCompleted,
			[]string{followRemaining, followRecent, followOverspend}},
		{"skips the question just asked", "will i overspend this month?", []Part{call("get_budget_snapshot")}, TurnCompleted,
			[]string{followWhere, followTips}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FollowUps(tc.content, tc.parts, tc.status)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FollowUps() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseFollowUps(t *testing.T) {
	text := "1. How much can I still spend on food?\n- \"Show my spending this week\"\n\n• 这周还能花多少？\nhow much can i still spend on food?\nExtra line"
	got := parseFollowUps(text, "")
	want := []string{"How much can I still spend on food?", "Show my spending this week", "这周还能花多少？"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFollowUps() = %v, want %v", got, want)
	}
	if got := parseFollowUps("Show my spending this week", "show my spending this week"); len(got) != 0 {
		t.Fatalf("expected the asked question to be dropped, got %v", got)
	}
}

func TestSuggestFollowUpsUsesModel(t *testing.T) {
	model := &scriptedModel{steps: []modelStep{{text: "Where did my food money go?\nCan I afford a S$30 dinner?\nRecord coffee: 4.5"}}}
	got := SuggestFollowUps(context.Background(), model, "food budget?", "You have S$120 left for food.", nil, TurnCompleted)
	want := []string{"Where did my food money go?", "Can I afford a S$30 dinner?", "Record coffee: 4.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SuggestFollowUps() = %v, want %v", got, want)
	}
	if len(model.requests) != 1 || model.requests[0].System != followUpSystem || len(model.requests[0].Tools) != 0 {
		t.Fatalf("unexpected follow-up request: %+v", model.requests)
	}
}

func TestSuggestFollowUpsFallsBack(t *testing.T) {
	snapshot := []Part{{Type: PartToolCall, ID: "c", Name: "get_budget_snapshot"}}
	rules := []string{followWhere, followOverspend, followTips}
	failing := &scriptedModel{steps: []modelStep{{err: errors.New("upstream down")}}}
	if got := SuggestFollowUps(context.Background(), failing, "q", "answer", snapshot, TurnCompleted); !reflect.DeepEqual(got, rules) {
		t.Fatalf("on error got %v, want %v", got, rules)
	}
	empty := &scriptedModel{steps: []modelStep{{text: "  \n"}}}
	if got := SuggestFollowUps(context.Background(), empty, "q", "answer", snapshot, TurnCompleted); !reflect.DeepEqual(got, rules) {
		t.Fatalf("on empty output got %v, want %v", got, rules)
	}
	unused := &scriptedModel{}
	if got := SuggestFollowUps(context.Background(), unused, "q", "answer", nil, TurnApproval); len(got) != 0 || unused.callCount() != 0 {
		t.Fatalf("awaiting approval should not call the model, got %v after %d calls", got, unused.callCount())
	}
}
