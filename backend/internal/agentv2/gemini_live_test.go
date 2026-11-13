//go:build live

package agentv2

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"unichatgo/internal/llm"
)

// The model and extraction pipeline are real. Only draft storage is isolated
// in memory so verification never writes a test expense into a user's ledger.
func TestGeminiReceiptLive(t *testing.T) {
	key := os.Getenv("GOOGLE_API_KEY")
	if key == "" {
		t.Skip("GOOGLE_API_KEY is required for live verification")
	}
	image, err := os.ReadFile("testdata/gemini-receipt.png")
	if err != nil {
		t.Fatal(err)
	}
	model := llm.NewOpenAICompatible(llm.Config{BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", Model: "gemini-3-flash-preview", APIKey: key})
	creator := &fakeDraftCreator{}
	runner := NewRunner(newTestRegistry(t, &fakeFinance{}), 6, 90*time.Second).WithDrafts(creator).
		WithClock(func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) })
	result, err := runner.Run(context.Background(), RunInput{UserID: 1, SessionID: 1, TurnID: "gemini-live-verification",
		Parts: []llm.Part{{Type: llm.PartImage, ImageMIME: "image/png", ImageData: image}}, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AwaitingApproval || result.Draft == nil || creator.count() != 1 {
		t.Fatal("real screenshot extraction did not produce one pending draft")
	}
	var payload struct {
		Items []draftItem `json:"items"`
	}
	if err := json.Unmarshal(result.Draft.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].AmountCents != 650 || payload.Items[0].SpentOn != "2026-11-08" {
		t.Fatal("receipt amount/date did not match the visible test image")
	}
	t.Log("Real image extraction produced one S$6.50 draft dated 2026-11-08; no ledger write occurred")
}
