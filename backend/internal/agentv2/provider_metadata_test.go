package agentv2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderMetadataPersistsForReplayButIsHiddenFromHistoryAPI(t *testing.T) {
	stored := []Message{{Role: RoleAssistant, Parts: []Part{
		{Type: PartToolCall, ID: "call_1", Name: "list_categories", Args: json.RawMessage(`{}`), ExtraContent: json.RawMessage(`{"google":{"thought_signature":"opaque-test-signature"}}`)},
		{Type: PartToolResult, CallID: "call_1", Name: "list_categories", Result: json.RawMessage(`{"categories":[]}`)},
	}}}
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var restored []Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	modelMessages := messageToLLM(restored[0])
	if len(modelMessages) != 2 || !strings.Contains(string(modelMessages[0].Parts[0].ToolExtraContent), "opaque-test-signature") {
		t.Fatal("stored metadata was lost before model replay")
	}
	public, err := json.Marshal(publicMessages(restored))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "opaque-test-signature") || strings.Contains(string(public), "provider_extra") {
		t.Fatal("provider replay metadata leaked into public message history")
	}
	if len(restored[0].Parts[0].ExtraContent) == 0 {
		t.Fatal("public serialization mutated stored model context")
	}
}
