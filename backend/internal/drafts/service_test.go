package drafts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
)

// memoryStore is an in-memory Store with the same atomic-consumption contract
// as the MySQL implementation.
type memoryStore struct {
	mu     sync.Mutex
	now    func() time.Time
	drafts map[string]*Draft
}

func newMemoryStore(now func() time.Time) *memoryStore {
	return &memoryStore{now: now, drafts: make(map[string]*Draft)}
}

func (s *memoryStore) Insert(_ context.Context, draft Draft) (*Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := draft
	s.drafts[draft.ID] = &copy
	saved := draft
	return &saved, nil
}

func (s *memoryStore) Get(_ context.Context, userID int64, id string) (*Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.drafts[id]
	if !ok || draft.UserID != userID {
		return nil, ErrNotFound
	}
	copy := *draft
	return &copy, nil
}

func (s *memoryStore) Consume(_ context.Context, userID int64, id, status, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.drafts[id]
	if !ok || draft.UserID != userID || draft.Status != StatusPending {
		return false, nil
	}
	if !s.now().Before(draft.ExpiresAt) {
		return false, nil
	}
	draft.Status = status
	draft.RejectReason = reason
	decided := s.now()
	draft.DecidedAt = &decided
	return true, nil
}

func (s *memoryStore) CompleteTx(_ context.Context, _ *sql.Tx, userID int64, id string,
	finalPayload, result json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.drafts[id]
	if !ok || draft.UserID != userID {
		return ErrNotFound
	}
	draft.Status = StatusApproved
	draft.FinalPayload = finalPayload
	draft.Result = result
	return nil
}

func (s *memoryStore) MarkFailed(_ context.Context, userID int64, id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.drafts[id]
	if !ok || draft.UserID != userID {
		return ErrNotFound
	}
	draft.Status = StatusFailed
	draft.RejectReason = reason
	return nil
}

func (s *memoryStore) ExpirePending(_ context.Context, userID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, draft := range s.drafts {
		if userID > 0 && draft.UserID != userID {
			continue
		}
		if draft.Status == StatusPending && !s.now().Before(draft.ExpiresAt) {
			draft.Status = StatusExpired
			count++
		}
	}
	return count, nil
}

func (s *memoryStore) ListPendingForSession(_ context.Context, userID, sessionID int64) ([]Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Draft, 0, 2)
	for _, draft := range s.drafts {
		if draft.UserID == userID && draft.SessionID == sessionID && draft.Status == StatusPending {
			out = append(out, *draft)
		}
	}
	return out, nil
}

// flowFinance simulates the finance write surface. The real SQL paths are
// covered by the MySQL integration suite.
type flowFinance struct {
	mu           sync.Mutex
	nextID       int64
	transactions map[int64]finance.Transaction
	budgets      map[string]*finance.Budget
	failWrite    error
	db           *sql.DB
}

func newFlowFinance() *flowFinance {
	// A throwaway SQLite pool supplies a real *sql.Tx for the service's
	// commit/rollback calls; the fake never executes SQL through it.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1)
	return &flowFinance{
		nextID: 100, transactions: make(map[int64]finance.Transaction),
		budgets: make(map[string]*finance.Budget), db: db,
	}
}

func (f *flowFinance) SnapshotReadOnly(context.Context, int64, string) (*insight.Snapshot, error) {
	return &insight.Snapshot{Month: "2026-11", RemainingCents: 10000, Categories: []insight.CategoryStat{}, Missing: []string{}}, nil
}

func (f *flowFinance) Affordability(snapshot *insight.Snapshot, amount int64, category insight.Category) insight.Affordability {
	return insight.Affordability{AmountCents: amount, Category: category}
}

func (f *flowFinance) ListTransactions(context.Context, int64, finance.TransactionListOptions) (*finance.TransactionPage, error) {
	return &finance.TransactionPage{Items: []finance.Transaction{}}, nil
}

func (f *flowFinance) GetTransaction(_ context.Context, userID, id int64) (*finance.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.transactions[id]
	if !ok {
		return nil, finance.ErrNotFound
	}
	return &record, nil
}

func (f *flowFinance) ReadBudget(_ context.Context, _ int64, month string) (*finance.Budget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	budget, ok := f.budgets[month]
	if !ok {
		return nil, finance.ErrNotFound
	}
	copy := *budget
	return &copy, nil
}

func (f *flowFinance) BeginWrite(context.Context, int64) (*sql.Tx, error) {
	return f.db.Begin()
}

func (f *flowFinance) CreateAgentTransaction(_ context.Context, _ *sql.Tx, userID int64,
	input finance.TransactionInput, target finance.AgentWriteTarget) (*finance.Transaction, error) {
	if f.failWrite != nil {
		return nil, f.failWrite
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	draftID := target.DraftID
	record := finance.Transaction{
		ID: f.nextID, AmountCents: input.AmountCents, SpentOn: input.SpentOn, Category: input.Category,
		Merchant: input.Merchant, Note: input.Note, Source: finance.SourceAgent, DraftID: &draftID,
	}
	f.transactions[record.ID] = record
	return &record, nil
}

func (f *flowFinance) UpdateAgentTransaction(_ context.Context, _ *sql.Tx, userID, id int64,
	patch finance.TransactionPatch, target finance.AgentWriteTarget) (*finance.Transaction, error) {
	if f.failWrite != nil {
		return nil, f.failWrite
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.transactions[id]
	if !ok {
		return nil, finance.ErrNotFound
	}
	updated, _, err := finance.ApplyTransactionPatch(record, patch,
		time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC))
	if err != nil {
		return nil, err
	}
	draftID := target.DraftID
	updated.ID = id
	updated.Source = finance.SourceAgent
	updated.DraftID = &draftID
	f.transactions[id] = updated
	return &updated, nil
}

func (f *flowFinance) DeleteAgentTransaction(_ context.Context, _ *sql.Tx, _ int64, id int64) error {
	if f.failWrite != nil {
		return f.failWrite
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.transactions[id]; !ok {
		return finance.ErrNotFound
	}
	delete(f.transactions, id)
	return nil
}

func (f *flowFinance) SaveAgentBudget(_ context.Context, _ *sql.Tx, _ int64, month string,
	input finance.BudgetInput) (*finance.Budget, error) {
	if f.failWrite != nil {
		return nil, f.failWrite
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	budget := &finance.Budget{Month: month, WarnBP: 8000, Source: finance.SourceAgent}
	if input.TotalCents != nil {
		budget.TotalCents = *input.TotalCents
	}
	if input.WarnBP != nil {
		budget.WarnBP = *input.WarnBP
	}
	for _, category := range input.Categories {
		if category.BudgetCents != nil {
			budget.Categories = append(budget.Categories, finance.CategoryBudget{
				Category: category.Category, BudgetCents: *category.BudgetCents,
			})
		}
	}
	f.budgets[month] = budget
	copy := *budget
	return &copy, nil
}

func fixedClock() func() time.Time {
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

func newTestService(t *testing.T) (*Service, *memoryStore, *flowFinance, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	store := newMemoryStore(now)
	fake := newFlowFinance()
	service := NewService(store, fake, 30*time.Minute).WithClock(now)
	return service, store, fake, &clock
}

func mustCreate(t *testing.T, service *Service, kind string, payload, before string) *Draft {
	t.Helper()
	draft, err := service.Create(context.Background(), CreateInput{
		UserID: 1, SessionID: 2, TurnID: "turn-1", ToolCallID: "call-1", Kind: kind,
		Payload: json.RawMessage(payload), Before: json.RawMessage(before),
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	return draft
}

const createPayloadJSON = `{"items":[{"amount_cents":650,"spent_on":"2026-11-05","category":"food","merchant":"Toast Box","note":null}]}`

func TestApproveCreateWritesAgentTransaction(t *testing.T) {
	service, _, fake, _ := newTestService(t)
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")

	decision, err := service.Decide(context.Background(), DecideInput{
		UserID: 1, DraftID: draft.ID, Action: ActionApprove,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if decision.Edited {
		t.Fatal("an unedited approval must not report edited")
	}
	if len(fake.transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(fake.transactions))
	}
	for _, record := range fake.transactions {
		if record.Source != finance.SourceAgent || record.DraftID == nil || *record.DraftID != draft.ID {
			t.Fatalf("provenance = %s %v", record.Source, record.DraftID)
		}
	}
	stored, err := service.Get(context.Background(), 1, draft.ID)
	if err != nil {
		t.Fatalf("get approved draft: %v", err)
	}
	if stored.Status != StatusApproved || len(stored.Result) == 0 {
		t.Fatalf("draft = %+v", stored)
	}
	var result map[string]any
	if err := json.Unmarshal(stored.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if ids, ok := result["transaction_ids"].([]any); !ok || len(ids) != 1 {
		t.Fatalf("result = %s", stored.Result)
	}
}

func TestApproveEditedPayloadUsesEdits(t *testing.T) {
	service, _, fake, _ := newTestService(t)
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")

	edited := `{"items":[{"amount_cents":700,"spent_on":"2026-11-05","category":"transport","merchant":"MRT","note":null}]}`
	decision, err := service.Decide(context.Background(), DecideInput{
		UserID: 1, DraftID: draft.ID, Action: ActionApprove, EditedPayload: json.RawMessage(edited),
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !decision.Edited {
		t.Fatal("edited approval must report edited=true")
	}
	for _, record := range fake.transactions {
		if record.AmountCents != 700 || record.Category != insight.CategoryTransport {
			t.Fatalf("edited payload was not applied: %+v", record)
		}
	}
}

func TestRejectKeepsLedgerUntouchedAndReturnsReason(t *testing.T) {
	service, _, fake, _ := newTestService(t)
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")

	decision, err := service.Decide(context.Background(), DecideInput{
		UserID: 1, DraftID: draft.ID, Action: ActionReject, Reason: "Wrong amount",
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if len(fake.transactions) != 0 {
		t.Fatal("reject must not write the ledger")
	}
	var payload map[string]any
	if err := json.Unmarshal(decision.ToolResult, &payload); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if payload["status"] != StatusRejected || payload["reason"] != "Wrong amount" {
		t.Fatalf("tool result = %s", decision.ToolResult)
	}
	if instruction, _ := payload["instruction"].(string); instruction == "" {
		t.Fatal("reject result must carry instruction text for the model")
	}
	stored, _ := service.Get(context.Background(), 1, draft.ID)
	if stored.Status != StatusRejected || stored.RejectReason != "Wrong amount" {
		t.Fatalf("stored draft = %+v", stored)
	}
}

func TestConcurrentDecisionOnlyConsumesOnce(t *testing.T) {
	service, _, fake, _ := newTestService(t)
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.Decide(context.Background(), DecideInput{
				UserID: 1, DraftID: draft.ID, Action: ActionApprove,
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var ok, notPending int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrNotPending):
			notPending++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || notPending != 1 {
		t.Fatalf("ok=%d notPending=%d", ok, notPending)
	}
	if len(fake.transactions) != 1 {
		t.Fatalf("transactions = %d, want exactly one", len(fake.transactions))
	}
}

func TestExpiredDraftIsNotPending(t *testing.T) {
	service, _, _, clock := newTestService(t)
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")
	*clock = clock.Add(31 * time.Minute)

	_, err := service.Decide(context.Background(), DecideInput{
		UserID: 1, DraftID: draft.ID, Action: ActionApprove,
	})
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("expected ErrNotPending, got %v", err)
	}
	stored, err := service.Get(context.Background(), 1, draft.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != StatusExpired {
		t.Fatalf("status = %s, want expired", stored.Status)
	}
}

func TestAnotherUsersDraftIsNotFound(t *testing.T) {
	service, _, _, _ := newTestService(t)
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")

	if _, err := service.Get(context.Background(), 2, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, err := service.Decide(context.Background(), DecideInput{
		UserID: 2, DraftID: draft.ID, Action: ActionApprove,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decide: %v", err)
	}
}

func TestApproveRejectsInvalidEditedPayload(t *testing.T) {
	service, _, fake, _ := newTestService(t)
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")

	_, err := service.Decide(context.Background(), DecideInput{
		UserID: 1, DraftID: draft.ID, Action: ActionApprove,
		EditedPayload: json.RawMessage(`{"items":[{"amount_cents":0,"category":"food"}]}`),
	})
	var validation *finance.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	if len(fake.transactions) != 0 {
		t.Fatal("invalid edits must not reach the ledger")
	}
	// The draft stays actionable so the user can correct the input.
	stored, _ := service.Get(context.Background(), 1, draft.ID)
	if stored.Status != StatusPending {
		t.Fatalf("status = %s, want pending", stored.Status)
	}
}

func TestApproveUpdateDeleteAndBudget(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		service, _, fake, _ := newTestService(t)
		fake.transactions[7] = finance.Transaction{
			ID: 7, AmountCents: 500, SpentOn: "2026-11-04", Category: insight.CategoryFood,
			Source: finance.SourceManual,
		}
		before, _ := json.Marshal(fake.transactions[7])
		draft := mustCreate(t, service, KindUpdateTransaction,
			`{"transaction_id":7,"changes":{"amount_cents":800}}`, string(before))
		if _, err := service.Decide(context.Background(), DecideInput{
			UserID: 1, DraftID: draft.ID, Action: ActionApprove,
		}); err != nil {
			t.Fatalf("approve update: %v", err)
		}
		if fake.transactions[7].AmountCents != 800 {
			t.Fatalf("update not applied: %+v", fake.transactions[7])
		}
	})

	t.Run("delete", func(t *testing.T) {
		service, _, fake, _ := newTestService(t)
		fake.transactions[8] = finance.Transaction{
			ID: 8, AmountCents: 500, SpentOn: "2026-11-04", Category: insight.CategoryFood,
			Source: finance.SourceManual,
		}
		before, _ := json.Marshal(fake.transactions[8])
		draft := mustCreate(t, service, KindDeleteTransaction, `{"transaction_id":8}`, string(before))
		if _, err := service.Decide(context.Background(), DecideInput{
			UserID: 1, DraftID: draft.ID, Action: ActionApprove,
		}); err != nil {
			t.Fatalf("approve delete: %v", err)
		}
		if _, ok := fake.transactions[8]; ok {
			t.Fatal("delete not applied")
		}
	})

	t.Run("budget", func(t *testing.T) {
		service, _, fake, _ := newTestService(t)
		fake.budgets["2026-11"] = &finance.Budget{
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
		before, _ := json.Marshal(fake.budgets["2026-11"])
		payload := `{"month":"2026-11","total_cents":65000,"warn_bp":8500,"categories":[` +
			`{"category":"food","budget_cents":25000}]}`
		draft := mustCreate(t, service, KindUpdateBudget, payload, string(before))
		if _, err := service.Decide(context.Background(), DecideInput{
			UserID: 1, DraftID: draft.ID, Action: ActionApprove,
		}); err != nil {
			t.Fatalf("approve budget: %v", err)
		}
		saved := fake.budgets["2026-11"]
		if saved.TotalCents != 65000 || saved.WarnBP != 8500 {
			t.Fatalf("budget totals = %+v", saved)
		}
		if len(saved.Categories) != 6 {
			t.Fatalf("categories = %d, want 6", len(saved.Categories))
		}
	})
}

func TestExecutionFailureMarksDraftFailed(t *testing.T) {
	service, _, fake, _ := newTestService(t)
	fake.failWrite = errors.New("database unavailable")
	draft := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")

	if _, err := service.Decide(context.Background(), DecideInput{
		UserID: 1, DraftID: draft.ID, Action: ActionApprove,
	}); err == nil {
		t.Fatal("expected the execution error")
	}
	stored, _ := service.Get(context.Background(), 1, draft.ID)
	if stored.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", stored.Status)
	}
}

func TestListPendingForSession(t *testing.T) {
	service, _, _, _ := newTestService(t)
	first := mustCreate(t, service, KindCreateTransactions, createPayloadJSON, "")
	second, err := service.Create(context.Background(), CreateInput{
		UserID: 1, SessionID: 9, TurnID: "turn-2", ToolCallID: "call-2", Kind: KindCreateTransactions,
		Payload: json.RawMessage(createPayloadJSON),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pending, err := service.ListPendingForSession(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("pending = %+v", pending)
	}
	if second.SessionID != 9 {
		t.Fatalf("session = %d", second.SessionID)
	}
}
