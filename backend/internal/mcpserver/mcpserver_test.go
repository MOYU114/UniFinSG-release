package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"unichatgo/internal/drafts"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/tools"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testUserID int64 = 7

// fakeFinance implements both the read surface the tools need and the write
// surface the draft service needs. Every write is counted so a test can prove
// a propose_* call never touches the ledger.
type fakeFinance struct {
	page *finance.TransactionPage
	err  error

	mu        sync.Mutex
	lastUser  int64
	beginCall int
	writes    int
}

func (f *fakeFinance) SnapshotReadOnly(context.Context, int64, string) (*insight.Snapshot, error) {
	return &insight.Snapshot{Month: "2026-11", Categories: []insight.CategoryStat{}}, nil
}

func (f *fakeFinance) Affordability(*insight.Snapshot, int64, insight.Category) insight.Affordability {
	return insight.Affordability{}
}

func (f *fakeFinance) ListTransactions(_ context.Context, userID int64, _ finance.TransactionListOptions) (*finance.TransactionPage, error) {
	f.mu.Lock()
	f.lastUser = userID
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.page, nil
}

func (f *fakeFinance) GetTransaction(context.Context, int64, int64) (*finance.Transaction, error) {
	return nil, finance.ErrNotFound
}

func (f *fakeFinance) ReadBudget(context.Context, int64, string) (*finance.Budget, error) {
	return nil, finance.ErrNotFound
}

func (f *fakeFinance) BeginWrite(context.Context, int64) (*sql.Tx, error) {
	f.mu.Lock()
	f.beginCall++
	f.mu.Unlock()
	return nil, errors.New("fake finance must not be written")
}

func (f *fakeFinance) CreateAgentTransaction(context.Context, *sql.Tx, int64,
	finance.TransactionInput, finance.AgentWriteTarget) (*finance.Transaction, error) {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil, errors.New("fake finance must not be written")
}

func (f *fakeFinance) UpdateAgentTransaction(context.Context, *sql.Tx, int64, int64,
	finance.TransactionPatch, finance.AgentWriteTarget) (*finance.Transaction, error) {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil, errors.New("fake finance must not be written")
}

func (f *fakeFinance) DeleteAgentTransaction(context.Context, *sql.Tx, int64, int64) error {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return errors.New("fake finance must not be written")
}

func (f *fakeFinance) SaveAgentBudget(context.Context, *sql.Tx, int64, string,
	finance.BudgetInput) (*finance.Budget, error) {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil, errors.New("fake finance must not be written")
}

// memoryDraftStore keeps drafts in memory. Reads and writes are scoped by
// user exactly like the MySQL store.
type memoryDraftStore struct {
	mu     sync.Mutex
	stored []drafts.Draft
}

func (s *memoryDraftStore) Insert(_ context.Context, draft drafts.Draft) (*drafts.Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft.Status = drafts.StatusPending
	s.stored = append(s.stored, draft)
	saved := draft
	return &saved, nil
}

func (s *memoryDraftStore) all() []drafts.Draft {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]drafts.Draft{}, s.stored...)
}

func (s *memoryDraftStore) Get(context.Context, int64, string) (*drafts.Draft, error) {
	return nil, errors.New("unexpected store read")
}

func (s *memoryDraftStore) Consume(context.Context, int64, string, string, string) (bool, error) {
	return false, errors.New("unexpected store consume")
}

func (s *memoryDraftStore) CompleteTx(context.Context, *sql.Tx, int64, string, json.RawMessage, json.RawMessage) error {
	return errors.New("unexpected store complete")
}

func (s *memoryDraftStore) MarkFailed(context.Context, int64, string, string) error {
	return errors.New("unexpected store failure write")
}

func (s *memoryDraftStore) ExpirePending(context.Context, int64) (int64, error) {
	return 0, errors.New("unexpected store expiry")
}

func (s *memoryDraftStore) ListPendingForSession(context.Context, int64, int64) ([]drafts.Draft, error) {
	return nil, errors.New("unexpected store list")
}

type harness struct {
	client  *mcp.ClientSession
	finance *fakeFinance
	store   *memoryDraftStore
}

// newHarness starts a real MCP server over the SDK's in-memory transport and
// connects a real MCP client to it. No network, no model.
func newHarness(t *testing.T, page *finance.TransactionPage, listErr error) *harness {
	t.Helper()
	financeService := &fakeFinance{page: page, err: listErr}
	store := &memoryDraftStore{}
	draftService := drafts.NewService(store, financeService, time.Minute)
	registry, err := tools.NewAgentRegistry(financeService, draftService, func() time.Time {
		return time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	server, err := New(registry, func(context.Context) (Identity, error) {
		return Identity{UserID: testUserID, SessionID: 1}, nil
	})
	if err != nil {
		t.Fatalf("build MCP server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpserver-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after the client disconnected")
		}
	})
	return &harness{client: session, finance: financeService, store: store}
}

func (h *harness) call(t *testing.T, name string, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return h.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func textOf(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected one content block, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return text.Text
}

func TestListToolsExposesTheSevenRegistryTools(t *testing.T) {
	h := newHarness(t, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := h.client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	want := []string{
		"list_categories", "get_budget_snapshot", "list_transactions",
		"propose_transactions", "propose_update_transaction",
		"propose_delete_transaction", "propose_update_budget",
	}
	// tools/list has no guaranteed order, so compare by name.
	if len(result.Tools) != len(want) {
		t.Fatalf("tools/list returned %d tools, want %d", len(result.Tools), len(want))
	}
	byName := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		byName[tool.Name] = tool
	}
	for i, name := range want {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("tools/list is missing %s", name)
		}
		if tool.Description == "" {
			t.Fatalf("%s has no description", name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("%s input schema is not an object schema: %T", name, tool.InputSchema)
		}
		if tool.Annotations == nil {
			t.Fatalf("%s has no annotations", name)
		}
		wantReadOnly := i < 3
		if tool.Annotations.ReadOnlyHint != wantReadOnly {
			t.Fatalf("%s readOnlyHint = %v, want %v", name, tool.Annotations.ReadOnlyHint, wantReadOnly)
		}
		if tool.Annotations.DestructiveHint == nil {
			t.Fatalf("%s has no destructiveHint", name)
		}
		wantDestructive := name == "propose_delete_transaction"
		if *tool.Annotations.DestructiveHint != wantDestructive {
			t.Fatalf("%s destructiveHint = %v, want %v", name, *tool.Annotations.DestructiveHint, wantDestructive)
		}
	}
}

func TestCallListTransactionsUsesTheConfiguredUser(t *testing.T) {
	merchant := "Kopitiam"
	note := "Lunch"
	page := &finance.TransactionPage{Total: 1, Items: []finance.Transaction{{
		ID: 11, SpentOn: "2026-11-05", AmountCents: 650, Category: insight.CategoryFood,
		Merchant: &merchant, Note: &note, Source: finance.SourceAgent,
	}}}
	h := newHarness(t, page, nil)
	result, err := h.call(t, "list_transactions", map[string]any{"limit": 5})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", textOf(t, result))
	}
	if h.finance.lastUser != testUserID {
		t.Fatalf("tool ran as user %d, want %d", h.finance.lastUser, testUserID)
	}
	var decoded struct {
		Month        string `json:"month"`
		Count        int    `json:"count"`
		Transactions []struct {
			ID          int64  `json:"id"`
			AmountCents int64  `json:"amount_cents"`
			Source      string `json:"source"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal([]byte(textOf(t, result)), &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.Month != "2026-11" || decoded.Count != 1 {
		t.Fatalf("unexpected result: %+v", decoded)
	}
	if decoded.Transactions[0].ID != 11 || decoded.Transactions[0].Source != finance.SourceAgent {
		t.Fatalf("unexpected transaction: %+v", decoded.Transactions[0])
	}
}

func TestCallProposeTransactionsCreatesOneDraftAndNoLedgerWrite(t *testing.T) {
	h := newHarness(t, nil, nil)
	result, err := h.call(t, "propose_transactions", map[string]any{
		"items": []map[string]any{{"amount_cents": 650, "category": "food", "merchant": "Toast Box"}},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", textOf(t, result))
	}
	var decoded struct {
		DraftID string `json:"draft_id"`
		Status  string `json:"status"`
		Kind    string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(textOf(t, result)), &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.Status != tools.PendingApproval || decoded.Kind != drafts.KindCreateTransactions {
		t.Fatalf("unexpected proposal: %+v", decoded)
	}
	stored := h.store.all()
	if len(stored) != 1 {
		t.Fatalf("stored %d drafts, want exactly 1", len(stored))
	}
	if stored[0].ID != decoded.DraftID {
		t.Fatalf("draft id %s != returned %s", stored[0].ID, decoded.DraftID)
	}
	if stored[0].Status != drafts.StatusPending || stored[0].UserID != testUserID {
		t.Fatalf("unexpected stored draft: %+v", stored[0])
	}
	if stored[0].ToolCallID == "" {
		t.Fatal("draft is missing the tool call id")
	}
	if h.finance.beginCall != 0 || h.finance.writes != 0 {
		t.Fatalf("propose_transactions wrote the ledger: begin=%d writes=%d", h.finance.beginCall, h.finance.writes)
	}
}

func TestUnknownToolAndInvalidArgumentsAreMCPErrors(t *testing.T) {
	h := newHarness(t, nil, nil)
	if _, err := h.call(t, "no_such_tool", map[string]any{}); err == nil {
		t.Fatal("unknown tool returned no error")
	}
	if _, err := h.call(t, "list_transactions", map[string]any{"limit": 51}); err == nil {
		t.Fatal("invalid arguments returned no error")
	}
	if _, err := h.call(t, "propose_transactions", map[string]any{"items": []any{}}); err == nil {
		t.Fatal("empty proposal returned no error")
	}
}

func TestHandlerFailureBecomesToolError(t *testing.T) {
	h := newHarness(t, nil, errors.New("database is unavailable"))
	result, err := h.call(t, "list_transactions", map[string]any{})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an isError result")
	}
	if !strings.Contains(textOf(t, result), "database is unavailable") {
		t.Fatalf("unexpected error text: %s", textOf(t, result))
	}
}
