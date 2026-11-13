package agentv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"unichatgo/internal/drafts"
	"unichatgo/internal/llm"
)

// fakeDraftCreator records the draft the screenshot workflow creates.
type fakeDraftCreator struct {
	mu     sync.Mutex
	inputs []drafts.CreateInput
	err    error
}

func (f *fakeDraftCreator) Create(_ context.Context, in drafts.CreateInput) (*drafts.Draft, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.inputs = append(f.inputs, in)
	return &drafts.Draft{
		ID: "d_test", UserID: in.UserID, SessionID: in.SessionID, TurnID: in.TurnID,
		ToolCallID: in.ToolCallID, Kind: in.Kind, Payload: in.Payload, Before: in.Before,
		Warnings: in.Warnings, Status: drafts.StatusPending,
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}, nil
}

func (f *fakeDraftCreator) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

// fakeDuplicateFinder marks every record as a possible duplicate.
type fakeDuplicateFinder struct{ called int }

func (f *fakeDuplicateFinder) MarkDuplicates(_ context.Context, _ int64, records []ValidatedRecord) error {
	f.called++
	for i := range records {
		records[i].Warnings = append(records[i].Warnings, WarningPossibleDuplicate)
	}
	return nil
}

func imageParts() []llm.Part {
	return []llm.Part{
		{Type: llm.PartImage, ImageMIME: MimePNG, ImageData: pngBytes},
		{Type: llm.PartText, Text: "lunch receipt"},
	}
}

func extractionCall(args string) modelStep {
	return modelStep{calls: []llm.ToolCall{{
		ID: "call_extract", Name: extractionToolName, Args: json.RawMessage(args),
	}}}
}

func TestScreenshotCreatesDraftAndPauses(t *testing.T) {
	creator := &fakeDraftCreator{}
	model := &scriptedModel{steps: []modelStep{
		extractionCall(`{"records":[
			{"amount_cents":650,"spent_on":"2026-11-05","merchant":"Kopitiam","category":"food","note":"lunch","confidence":0.9},
			{"amount_cents":1200,"spent_on":null,"merchant":null,"category":null,"note":null,"confidence":0.4}
		],"image_kind":"receipt","notes":"two items"}`),
	}}
	runner := NewRunner(newTestRegistry(t, &fakeFinance{}), 6, time.Second).
		WithDrafts(creator).
		WithClock(func() time.Time { return extractionTime() })

	var events []string
	var phases []string
	result, err := runner.Run(context.Background(), RunInput{
		UserID: 1, SessionID: 2, TurnID: "turn-1", Parts: imageParts(),
		Model: model,
		Emit: func(event string, payload any) error {
			events = append(events, event)
			if event == "status" {
				phases = append(phases, payload.(map[string]string)["phase"])
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.AwaitingApproval || result.Draft == nil {
		t.Fatalf("the turn did not pause on a draft: %+v", result)
	}
	if result.ModelCalls != 1 || model.callCount() != 1 {
		t.Fatalf("model calls = %d", result.ModelCalls)
	}
	if creator.count() != 1 {
		t.Fatalf("drafts created = %d", creator.count())
	}
	if phases[0] != "extracting" {
		t.Fatalf("first phase = %q", phases[0])
	}
	for _, want := range []string{"status", "tool_call", "draft", "tool_result", "delta"} {
		if !contains(events, want) {
			t.Fatalf("missing %s event in %v", want, events)
		}
	}
	var payload struct {
		Items []draftItem `json:"items"`
	}
	if err := json.Unmarshal(creator.inputs[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("items = %+v", payload.Items)
	}
	if payload.Items[1].SpentOn != "2026-11-06" || payload.Items[1].Category != "entertainment_other" {
		t.Fatalf("second item was not corrected: %+v", payload.Items[1])
	}
	if !hasWarning(creator.inputs[0].Warnings, WarningNeedsReview) {
		t.Fatalf("draft warnings = %v", creator.inputs[0].Warnings)
	}
	if !strings.Contains(result.Answer, "2 expenses") {
		t.Fatalf("answer = %q", result.Answer)
	}
	// The extraction request carried the image bytes.
	first := model.requests[0]
	if len(first.Messages) != 1 || len(first.Messages[0].Parts) != 2 {
		t.Fatalf("extraction request = %+v", first)
	}
	if first.Messages[0].Parts[0].Type != llm.PartImage || len(first.Messages[0].Parts[0].ImageData) == 0 {
		t.Fatal("the image was not sent to the model")
	}
	if len(first.JSONSchema) == 0 {
		t.Fatal("the extraction request must force the JSON schema")
	}
}

func TestScreenshotUsesModelSentenceWhenPresent(t *testing.T) {
	creator := &fakeDraftCreator{}
	step := extractionCall(`{"records":[{"amount_cents":500}],"image_kind":"receipt","notes":""}`)
	step.text = "I read one S$5.00 expense."
	model := &scriptedModel{steps: []modelStep{step}}
	runner := NewRunner(newTestRegistry(t, &fakeFinance{}), 6, time.Second).WithDrafts(creator)
	result, err := runner.Run(context.Background(), RunInput{
		UserID: 1, SessionID: 2, TurnID: "t", Parts: imageParts(), Model: model,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Answer != "I read one S$5.00 expense." {
		t.Fatalf("answer = %q", result.Answer)
	}
}

func TestScreenshotEmptyExtractionFallsBackToReAct(t *testing.T) {
	creator := &fakeDraftCreator{}
	model := &scriptedModel{steps: []modelStep{
		extractionCall(`{"records":[],"image_kind":"other","notes":"no expense"}`),
		{text: "This looks like a screenshot of a chat, not a receipt."},
	}}
	runner := NewRunner(newTestRegistry(t, &fakeFinance{}), 6, time.Second).WithDrafts(creator)
	result, err := runner.Run(context.Background(), RunInput{
		UserID: 1, SessionID: 2, TurnID: "t", Parts: imageParts(), Model: model,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if creator.count() != 0 {
		t.Fatal("an empty extraction must not create a draft")
	}
	if result.AwaitingApproval {
		t.Fatal("the turn must not pause")
	}
	if model.callCount() != 2 || result.ModelCalls != 2 {
		t.Fatalf("model calls = %d, result = %d", model.callCount(), result.ModelCalls)
	}
	if !strings.Contains(result.Answer, "not a receipt") {
		t.Fatalf("answer = %q", result.Answer)
	}
	// The fallback keeps the image in context.
	second := model.requests[1]
	foundImage := false
	for _, message := range second.Messages {
		for _, part := range message.Parts {
			if part.Type == llm.PartImage && len(part.ImageData) > 0 {
				foundImage = true
			}
		}
	}
	if !foundImage {
		t.Fatal("the image was dropped from the fallback context")
	}
}

func TestScreenshotMarksPossibleDuplicates(t *testing.T) {
	creator := &fakeDraftCreator{}
	finder := &fakeDuplicateFinder{}
	model := &scriptedModel{steps: []modelStep{
		extractionCall(`{"records":[{"amount_cents":650,"spent_on":"2026-11-05","merchant":"Kopitiam","category":"food"}]}`),
	}}
	runner := NewRunner(newTestRegistry(t, &fakeFinance{}), 6, time.Second).
		WithDrafts(creator).
		WithDuplicates(finder).
		WithClock(func() time.Time { return extractionTime() })
	if _, err := runner.Run(context.Background(), RunInput{
		UserID: 1, SessionID: 2, TurnID: "t", Parts: imageParts(), Model: model,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if finder.called != 1 {
		t.Fatalf("duplicate finder calls = %d", finder.called)
	}
	if !hasWarning(creator.inputs[0].Warnings, WarningPossibleDuplicate) {
		t.Fatalf("draft warnings = %v", creator.inputs[0].Warnings)
	}
}

func TestScreenshotDraftFailureDoesNotChargeOutput(t *testing.T) {
	creator := &fakeDraftCreator{err: errors.New("store down")}
	model := &scriptedModel{steps: []modelStep{
		extractionCall(`{"records":[{"amount_cents":650,"spent_on":"2026-11-05","category":"food"}]}`),
	}}
	runner := NewRunner(newTestRegistry(t, &fakeFinance{}), 6, time.Second).WithDrafts(creator)
	result, err := runner.Run(context.Background(), RunInput{
		UserID: 1, SessionID: 2, TurnID: "t", Parts: imageParts(), Model: model,
	})
	if err == nil {
		t.Fatal("expected the draft error")
	}
	if result.Output {
		t.Fatal("a failed draft must not be billed as output")
	}
}

func TestScreenshotTurnStreamsDraftAndAwaitingApproval(t *testing.T) {
	creator := &fakeDraftCreator{}
	model := &scriptedModel{steps: []modelStep{
		extractionCall(`{"records":[{"amount_cents":650,"spent_on":"2026-11-05","merchant":"Kopitiam","category":"food","note":"lunch","confidence":0.9}],"image_kind":"receipt","notes":""}`),
	}}
	fixture := newUploadFixture(t, 1, model, creator)
	id := fixture.upload(t, "receipt.png", pngBytes)
	response := fixture.send(t, `{"content":"record this","upload_ids":[`+itoa(id)+`]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	events := parseSSEEvents(t, response.Body.String())
	statuses := make([]string, 0, 2)
	for _, event := range events {
		if event.Name == "status" {
			statuses = append(statuses, event.Data["phase"].(string))
		}
	}
	if !contains(statuses, "extracting") {
		t.Fatalf("missing extracting phase: %v", statuses)
	}
	draftEvent := firstEvent(t, events, "draft")
	if draftEvent.Data["draft_id"] != "d_test" {
		t.Fatalf("draft event = %+v", draftEvent.Data)
	}
	done := firstEvent(t, events, "done")
	if done.Data["status"] != TurnApproval {
		t.Fatalf("done status = %v", done.Data["status"])
	}
	if done.Data["credits_charged"].(float64) != 5 {
		t.Fatalf("credits = %v", done.Data["credits_charged"])
	}
	if used := fixture.credits.used[1]; used != 5 {
		t.Fatalf("credits used = %d", used)
	}
}
