package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"unichatgo/internal/drafts"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
)

type fakeFinance struct {
	snapshot    *insight.Snapshot
	page        *finance.TransactionPage
	budget      *finance.Budget
	transaction *finance.Transaction
	snapshotErr error
	listErr     error
	budgetErr   error
	getErr      error

	lastUserID  int64
	lastMonth   string
	lastOptions finance.TransactionListOptions
	listCalls   int
}

func (f *fakeFinance) GetTransaction(_ context.Context, userID, transactionID int64) (*finance.Transaction, error) {
	f.lastUserID = userID
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.transaction == nil || f.transaction.ID != transactionID {
		return nil, finance.ErrNotFound
	}
	copy := *f.transaction
	return &copy, nil
}

func (f *fakeFinance) ReadBudget(_ context.Context, userID int64, month string) (*finance.Budget, error) {
	f.lastUserID = userID
	f.lastMonth = month
	if f.budgetErr != nil {
		return nil, f.budgetErr
	}
	if f.budget == nil {
		return nil, finance.ErrNotFound
	}
	copy := *f.budget
	return &copy, nil
}

func (f *fakeFinance) SnapshotReadOnly(ctx context.Context, userID int64, month string) (*insight.Snapshot, error) {
	return f.Snapshot(ctx, userID, month)
}

func (f *fakeFinance) Snapshot(_ context.Context, userID int64, month string) (*insight.Snapshot, error) {
	f.lastUserID = userID
	f.lastMonth = month
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		return nil, finance.ErrInvalidMonth
	}
	return f.snapshot, nil
}

func (f *fakeFinance) Affordability(snapshot *insight.Snapshot, amountCents int64, category insight.Category) insight.Affordability {
	if snapshot == nil || amountCents <= 0 {
		panic("invalid affordability input")
	}
	return insight.Affordability{
		AmountCents: amountCents, Category: category,
		RemainingAfterCents: snapshot.RemainingCents - amountCents,
		ExceedsTotal:        amountCents > snapshot.RemainingCents,
	}
}

func (f *fakeFinance) ListTransactions(_ context.Context, userID int64, options finance.TransactionListOptions) (*finance.TransactionPage, error) {
	f.lastUserID = userID
	f.lastOptions = options
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.page, nil
}

func testSnapshot() *insight.Snapshot {
	categories := make([]insight.CategoryStat, 0, len(Categories))
	for _, category := range Categories {
		categories = append(categories, insight.CategoryStat{
			Category: insight.Category(category.Category), BudgetCents: 10000, SpentCents: 2500, Status: insight.StatusOK,
		})
	}
	return &insight.Snapshot{
		Month: "2026-11", AsOf: time.Date(2026, 11, 6, 9, 0, 0, 0, time.FixedZone("SGT", 8*3600)),
		TotalBudgetCents: 60000, TotalSpentCents: 15000, RemainingCents: 45000,
		Categories: categories, Missing: []string{},
	}
}

func newTestRegistry(t *testing.T, service Finance) *Registry {
	t.Helper()
	registry, err := NewReadOnlyRegistry(service, func() time.Time {
		return time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return registry
}

func TestRegistryRejectsDuplicateAndIncompleteTools(t *testing.T) {
	registry := NewRegistry()
	tool := Tool{Name: "a", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, UserContext, json.RawMessage) (any, error) { return nil, nil }}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register(tool); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if err := registry.Register(Tool{Name: "b"}); !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("expected invalid error, got %v", err)
	}
	if err := registry.Register(Tool{Name: "c", Description: "d", InputSchema: json.RawMessage(`{`),
		Handler: tool.Handler}); !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("expected invalid schema error, got %v", err)
	}
}

func TestSpecsExposeThreeReadOnlyTools(t *testing.T) {
	registry := newTestRegistry(t, &fakeFinance{snapshot: testSnapshot()})
	specs := registry.Specs()
	if len(specs) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(specs))
	}
	want := []string{"list_categories", "get_budget_snapshot", "list_transactions"}
	for i, spec := range specs {
		if spec.Name != want[i] {
			t.Fatalf("spec %d = %s, want %s", i, spec.Name, want[i])
		}
		if spec.Description == "" {
			t.Fatalf("tool %s has no description", spec.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Fatalf("tool %s schema is not JSON: %v", spec.Name, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("tool %s schema must be an object", spec.Name)
		}
	}
	for _, tool := range registry.List() {
		if !tool.ReadOnly {
			t.Fatalf("tool %s must be read-only in P2", tool.Name)
		}
	}
}

func TestListCategoriesReturnsSixBilingualEntries(t *testing.T) {
	registry := newTestRegistry(t, &fakeFinance{snapshot: testSnapshot()})
	tool, _ := registry.Get("list_categories")
	result, err := tool.Handler(context.Background(), UserContext{UserID: 1}, nil)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	encoded, _ := json.Marshal(result)
	var payload struct {
		Categories []CategoryInfo `json:"categories"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Categories) != 6 {
		t.Fatalf("expected 6 categories, got %d", len(payload.Categories))
	}
	for _, category := range payload.Categories {
		if category.LabelEN == "" || category.LabelZH == "" {
			t.Fatalf("category %s is missing a label", category.Category)
		}
	}
}

func TestBudgetSnapshotUsesExplicitUserContext(t *testing.T) {
	service := &fakeFinance{snapshot: testSnapshot()}
	registry := newTestRegistry(t, service)
	tool, _ := registry.Get("get_budget_snapshot")

	result, err := tool.Handler(context.Background(), UserContext{UserID: 42, SessionID: 7, TurnID: "t", ToolCallID: "c"},
		json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if service.lastUserID != 42 {
		t.Fatalf("handler used user %d, want 42", service.lastUserID)
	}
	if service.lastMonth != "2026-11" {
		t.Fatalf("default month = %s, want 2026-11", service.lastMonth)
	}
	payload, _ := json.Marshal(result)
	var decoded BudgetSnapshotResult
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.RemainingCents != 45000 || len(decoded.Categories) != 6 {
		t.Fatalf("unexpected snapshot: %+v", decoded)
	}
	if decoded.Categories[0].RemainingCents != 7500 {
		t.Fatalf("category remaining = %d, want 7500", decoded.Categories[0].RemainingCents)
	}
}

func TestBudgetSnapshotAffordabilityFallsBackToOtherCategory(t *testing.T) {
	service := &fakeFinance{snapshot: testSnapshot()}
	registry := newTestRegistry(t, service)
	tool, _ := registry.Get("get_budget_snapshot")

	result, err := tool.Handler(context.Background(), UserContext{UserID: 1},
		json.RawMessage(`{"affordability":{"amount_cents":800,"category":"not_a_category"}}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	payload, _ := json.Marshal(result)
	var decoded BudgetSnapshotResult
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Affordability == nil || decoded.Affordability.Category != insight.CategoryEntertainmentOther {
		t.Fatalf("expected entertainment_other fallback, got %+v", decoded.Affordability)
	}
}

func TestBudgetSnapshotRejectsBadArguments(t *testing.T) {
	registry := newTestRegistry(t, &fakeFinance{snapshot: testSnapshot()})
	tool, _ := registry.Get("get_budget_snapshot")
	for _, args := range []string{
		`{"unknown":true}`,
		`{"affordability":{"amount_cents":0,"category":"food"}}`,
		`{"month":"2026-13"}`,
	} {
		if _, err := tool.Handler(context.Background(), UserContext{UserID: 1}, json.RawMessage(args)); err == nil {
			t.Fatalf("expected an argument error for %s", args)
		}
	}
}

func TestListTransactionsFiltersAndDefaults(t *testing.T) {
	merchant := "Kopitiam"
	note := "Lunch"
	other := "MRT"
	page := &finance.TransactionPage{Total: 3, Items: []finance.Transaction{
		{ID: 3, SpentOn: "2026-11-05", AmountCents: 650, Category: insight.CategoryFood, Merchant: &merchant, Note: &note, Source: finance.SourceAgent},
		{ID: 2, SpentOn: "2026-11-03", AmountCents: 120, Category: insight.CategoryTransport, Merchant: &other},
		{ID: 1, SpentOn: "2026-10-31", AmountCents: 900, Category: insight.CategoryFood, Merchant: &merchant},
	}}
	service := &fakeFinance{snapshot: testSnapshot(), page: page}
	registry := newTestRegistry(t, service)
	tool, _ := registry.Get("list_transactions")

	result, err := tool.Handler(context.Background(), UserContext{UserID: 9},
		json.RawMessage(`{"from":"2026-11-01","to":"2026-11-30","query":"kopi","limit":5}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if service.lastUserID != 9 {
		t.Fatalf("handler used user %d, want 9", service.lastUserID)
	}
	if service.lastOptions.Month != "2026-11" || service.lastOptions.Category != "" {
		t.Fatalf("unexpected list options: %+v", service.lastOptions)
	}
	payload, _ := json.Marshal(result)
	var decoded listTransactionsResult
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Count != 1 || decoded.Transactions[0].ID != 3 {
		t.Fatalf("expected only the November Kopitiam row, got %+v", decoded.Transactions)
	}
	if decoded.Transactions[0].Source != finance.SourceAgent {
		t.Fatalf("source = %q, want agent", decoded.Transactions[0].Source)
	}
	if decoded.TotalInMonth != 3 {
		t.Fatalf("total_in_month = %d, want 3", decoded.TotalInMonth)
	}
}

func TestListTransactionsRejectsCrossMonthRangeAndBadLimit(t *testing.T) {
	registry := newTestRegistry(t, &fakeFinance{snapshot: testSnapshot()})
	tool, _ := registry.Get("list_transactions")
	for _, args := range []string{
		`{"from":"2026-10-31","to":"2026-11-02"}`,
		`{"limit":51}`,
		`{"limit":0}`,
		`{"category":"nope"}`,
		`{"from":"31-10-2026"}`,
	} {
		if _, err := tool.Handler(context.Background(), UserContext{UserID: 1}, json.RawMessage(args)); err == nil {
			t.Fatalf("expected an argument error for %s", args)
		}
	}
}

func TestListTransactionsPropagatesFinanceErrors(t *testing.T) {
	registry := newTestRegistry(t, &fakeFinance{snapshot: testSnapshot(), listErr: errors.New("boom")})
	tool, _ := registry.Get("list_transactions")
	if _, err := tool.Handler(context.Background(), UserContext{UserID: 1}, nil); err == nil {
		t.Fatal("expected the finance error to surface")
	}
}

// fakeDrafts records the validated proposal without writing anything.
type fakeDrafts struct {
	created []drafts.CreateInput
}

func (f *fakeDrafts) Create(_ context.Context, in drafts.CreateInput) (*drafts.Draft, error) {
	f.created = append(f.created, in)
	return &drafts.Draft{
		ID: "d_test", Kind: in.Kind, Payload: in.Payload, Before: in.Before,
		Warnings: in.Warnings, Status: drafts.StatusPending,
		ExpiresAt: time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC),
	}, nil
}

func newAgentRegistry(t *testing.T, service Finance, creator DraftCreator) *Registry {
	t.Helper()
	registry, err := NewAgentRegistry(service, creator, func() time.Time {
		return time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return registry
}

func TestAgentRegistryExposesSevenToolsWithAnnotations(t *testing.T) {
	registry := newAgentRegistry(t, &fakeFinance{snapshot: testSnapshot()}, &fakeDrafts{})
	specs := registry.Specs()
	if len(specs) != 7 {
		t.Fatalf("expected 7 tools, got %d", len(specs))
	}
	want := []string{
		"list_categories", "get_budget_snapshot", "list_transactions",
		"propose_transactions", "propose_update_transaction",
		"propose_delete_transaction", "propose_update_budget",
	}
	for i, spec := range specs {
		if spec.Name != want[i] {
			t.Fatalf("spec %d = %s, want %s", i, spec.Name, want[i])
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil || schema["type"] != "object" {
			t.Fatalf("tool %s has an invalid schema: %v", spec.Name, err)
		}
	}
	for _, name := range want[:3] {
		tool, _ := registry.Get(name)
		if !tool.ReadOnly || tool.Destructive {
			t.Fatalf("%s must be read-only and non-destructive", name)
		}
	}
	for _, name := range want[3:] {
		tool, _ := registry.Get(name)
		if tool.ReadOnly {
			t.Fatalf("%s must not be read-only", name)
		}
	}
	deleteTool, _ := registry.Get("propose_delete_transaction")
	if !deleteTool.Destructive {
		t.Fatal("propose_delete_transaction must be destructive")
	}
}

func TestProposeTransactionsValidatesAndCreatesDraft(t *testing.T) {
	creator := &fakeDrafts{}
	registry := newAgentRegistry(t, &fakeFinance{snapshot: testSnapshot()}, creator)
	tool, _ := registry.Get("propose_transactions")

	result, err := tool.Handler(context.Background(),
		UserContext{UserID: 7, SessionID: 8, TurnID: "turn", ToolCallID: "call"},
		json.RawMessage(`{"items":[{"amount_cents":650,"category":"food","merchant":"Toast Box"}]}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(creator.created) != 1 {
		t.Fatalf("drafts = %d", len(creator.created))
	}
	created := creator.created[0]
	if created.UserID != 7 || created.SessionID != 8 || created.TurnID != "turn" || created.ToolCallID != "call" {
		t.Fatalf("identity = %+v", created)
	}
	if created.Kind != drafts.KindCreateTransactions {
		t.Fatalf("kind = %s", created.Kind)
	}
	var payload struct {
		Items []struct {
			AmountCents int64  `json:"amount_cents"`
			SpentOn     string `json:"spent_on"`
		} `json:"items"`
	}
	if err := json.Unmarshal(created.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Items[0].SpentOn != "2026-11-06" {
		t.Fatalf("default date = %q", payload.Items[0].SpentOn)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"status":"pending_approval"`) {
		t.Fatalf("result = %s", encoded)
	}

	for _, args := range []string{
		`{"items":[]}`,
		`{"items":[{"amount_cents":0,"category":"food"}]}`,
		`{"items":[{"amount_cents":100,"category":"nope"}]}`,
		`{"items":[{"amount_cents":100,"category":"food","merchant":"` + strings.Repeat("m", 65) + `"}]}`,
	} {
		if _, err := tool.Handler(context.Background(), UserContext{UserID: 1}, json.RawMessage(args)); err == nil {
			t.Fatalf("expected an argument error for %s", args)
		}
	}
}

func TestProposeUpdateAndDeleteRequireOwnedTransaction(t *testing.T) {
	service := &fakeFinance{snapshot: testSnapshot(), getErr: finance.ErrNotFound}
	registry := newAgentRegistry(t, service, &fakeDrafts{})
	for _, name := range []string{"propose_update_transaction", "propose_delete_transaction"} {
		tool, _ := registry.Get(name)
		if _, err := tool.Handler(context.Background(), UserContext{UserID: 1},
			json.RawMessage(`{"transaction_id":5,"changes":{"amount_cents":900}}`)); err == nil {
			t.Fatalf("%s accepted a missing transaction", name)
		}
	}
}

func TestProposeUpdateStoresBeforeSnapshot(t *testing.T) {
	merchant := "Kopitiam"
	service := &fakeFinance{
		snapshot: testSnapshot(),
		transaction: &finance.Transaction{
			ID: 5, AmountCents: 650, SpentOn: "2026-11-05", Category: insight.CategoryFood,
			Merchant: &merchant, Source: finance.SourceManual,
		},
	}
	creator := &fakeDrafts{}
	registry := newAgentRegistry(t, service, creator)
	tool, _ := registry.Get("propose_update_transaction")

	if _, err := tool.Handler(context.Background(), UserContext{UserID: 1},
		json.RawMessage(`{"transaction_id":5,"changes":{"amount_cents":900}}`)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(creator.created) != 1 || len(creator.created[0].Before) == 0 {
		t.Fatalf("missing before snapshot: %+v", creator.created)
	}
	var before finance.Transaction
	if err := json.Unmarshal(creator.created[0].Before, &before); err != nil || before.AmountCents != 650 {
		t.Fatalf("before = %s", creator.created[0].Before)
	}
	if !strings.Contains(string(creator.created[0].Payload), `"amount_cents":900`) {
		t.Fatalf("payload = %s", creator.created[0].Payload)
	}
}

func TestProposeUpdateBudgetMergesCurrentValues(t *testing.T) {
	budget := &finance.Budget{
		Month: "2026-11", TotalCents: 60000, WarnBP: 8000, Source: "saved",
		Categories: []finance.CategoryBudget{
			{Category: insight.CategoryFood, BudgetCents: 20000},
			{Category: insight.CategoryTransport, BudgetCents: 5000},
			{Category: insight.CategoryHousingTuition, BudgetCents: 0},
			{Category: insight.CategoryDailyShopping, BudgetCents: 5000},
			{Category: insight.CategoryStudyDigital, BudgetCents: 5000},
			{Category: insight.CategoryEntertainmentOther, BudgetCents: 25000},
		},
	}
	creator := &fakeDrafts{}
	registry := newAgentRegistry(t, &fakeFinance{snapshot: testSnapshot(), budget: budget}, creator)
	tool, _ := registry.Get("propose_update_budget")

	if _, err := tool.Handler(context.Background(), UserContext{UserID: 1},
		json.RawMessage(`{"month":"2026-11","total_cents":65000,"categories":[{"category":"food","budget_cents":25000}]}`)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(creator.created) != 1 {
		t.Fatalf("drafts = %d", len(creator.created))
	}
	var payload struct {
		Month      string                   `json:"month"`
		TotalCents int64                    `json:"total_cents"`
		Categories []finance.CategoryBudget `json:"categories"`
	}
	if err := json.Unmarshal(creator.created[0].Payload, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.TotalCents != 65000 || len(payload.Categories) != 6 {
		t.Fatalf("payload = %s", creator.created[0].Payload)
	}
	if !strings.Contains(string(creator.created[0].Payload), `"budget_cents":25000`) {
		t.Fatalf("edited category missing: %s", creator.created[0].Payload)
	}
}
