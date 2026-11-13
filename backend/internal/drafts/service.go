package drafts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
)

// DefaultTTL is the draft lifetime from design section 11.
const DefaultTTL = 30 * time.Minute

// Finance is the write surface the decision endpoint needs. *finance.Service
// implements it.
type Finance interface {
	BeginWrite(ctx context.Context, userID int64) (*sql.Tx, error)
	CreateAgentTransaction(ctx context.Context, tx *sql.Tx, userID int64,
		input finance.TransactionInput, target finance.AgentWriteTarget) (*finance.Transaction, error)
	UpdateAgentTransaction(ctx context.Context, tx *sql.Tx, userID, transactionID int64,
		patch finance.TransactionPatch, target finance.AgentWriteTarget) (*finance.Transaction, error)
	DeleteAgentTransaction(ctx context.Context, tx *sql.Tx, userID, transactionID int64) error
	SaveAgentBudget(ctx context.Context, tx *sql.Tx, userID int64, month string,
		input finance.BudgetInput) (*finance.Budget, error)
}

// Service creates and decides drafts.
type Service struct {
	store   Store
	finance Finance
	ttl     time.Duration
	now     func() time.Time
}

func NewService(store Store, financeService Finance, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{store: store, finance: financeService, ttl: ttl, now: time.Now}
}

// WithClock pins time for tests.
func (s *Service) WithClock(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// TTL is the configured draft lifetime.
func (s *Service) TTL() time.Duration { return s.ttl }

// Create stores a validated proposal as a pending draft.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Draft, error) {
	if !validKind(in.Kind) {
		return nil, fmt.Errorf("drafts: unsupported kind %q", in.Kind)
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	draft := Draft{
		ID:         newDraftID(),
		UserID:     in.UserID,
		SessionID:  in.SessionID,
		TurnID:     in.TurnID,
		ToolCallID: in.ToolCallID,
		Kind:       in.Kind,
		Payload:    in.Payload,
		Before:     in.Before,
		Warnings:   append([]string{}, in.Warnings...),
		Status:     StatusPending,
		ExpiresAt:  now.Add(s.ttl),
		CreatedAt:  now,
	}
	return s.store.Insert(ctx, draft)
}

// Get returns one draft, lazily expiring it when its TTL has passed.
func (s *Service) Get(ctx context.Context, userID int64, id string) (*Draft, error) {
	draft, err := s.store.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if draft.Status == StatusPending && !s.now().Before(draft.ExpiresAt) {
		if _, err := s.store.ExpirePending(ctx, userID); err == nil {
			if refreshed, err := s.store.Get(ctx, userID, id); err == nil {
				draft = refreshed
			}
		}
	}
	return draft, nil
}

// ListPendingForSession returns the live pending drafts of one conversation.
func (s *Service) ListPendingForSession(ctx context.Context, userID, sessionID int64) ([]Draft, error) {
	return s.store.ListPendingForSession(ctx, userID, sessionID)
}

// ExpirePending lazily expires drafts; userID 0 runs the periodic sweep.
func (s *Service) ExpirePending(ctx context.Context, userID int64) (int64, error) {
	return s.store.ExpirePending(ctx, userID)
}

// Decide approves or rejects a draft. Approval revalidates the payload,
// consumes the draft atomically and executes the write in one transaction.
func (s *Service) Decide(ctx context.Context, in DecideInput) (*Decision, error) {
	if in.Action != ActionApprove && in.Action != ActionReject {
		return nil, ErrInvalidAction
	}
	draft, err := s.Get(ctx, in.UserID, in.DraftID)
	if err != nil {
		return nil, err
	}
	if draft.Status != StatusPending {
		return nil, ErrNotPending
	}
	if in.Action == ActionReject {
		return s.reject(ctx, draft, in.Reason)
	}
	return s.approve(ctx, draft, in.EditedPayload)
}

func (s *Service) reject(ctx context.Context, draft *Draft, reason string) (*Decision, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 200 {
		return nil, &finance.ValidationError{Fields: map[string]string{"reason": "must_be_at_most_200_characters"}}
	}
	consumed, err := s.store.Consume(ctx, draft.UserID, draft.ID, StatusRejected, reason)
	if err != nil {
		return nil, err
	}
	if !consumed {
		return nil, ErrNotPending
	}
	draft.Status = StatusRejected
	draft.RejectReason = reason
	toolResult, err := json.Marshal(map[string]any{
		"status": StatusRejected,
		"reason": reason,
		"instruction": "The user rejected this draft. " + rejectionGuidance(reason) +
			"Do not resubmit the same content; if a change is needed, ask the user first.",
	})
	if err != nil {
		return nil, err
	}
	return &Decision{Draft: draft, ToolResult: toolResult}, nil
}

func (s *Service) approve(ctx context.Context, draft *Draft, edited json.RawMessage) (*Decision, error) {
	payload := draft.Payload
	editedFlag := false
	if len(edited) > 0 && strings.TrimSpace(string(edited)) != "null" {
		payload = edited
		editedFlag = true
	}
	normalized, err := s.validate(draft.Kind, payload, draft.Before)
	if err != nil {
		return nil, err
	}
	consumed, err := s.store.Consume(ctx, draft.UserID, draft.ID, StatusApproved, "")
	if err != nil {
		return nil, err
	}
	if !consumed {
		return nil, ErrNotPending
	}
	result, err := s.execute(ctx, draft, normalized)
	if err != nil {
		// Never persist raw SQL/driver text in a user-visible field.
		_ = s.store.MarkFailed(ctx, draft.UserID, draft.ID, "The draft could not be applied.")
		return nil, err
	}
	draft.Status = StatusApproved
	draft.FinalPayload = normalized
	draft.Result = result
	toolResult, err := json.Marshal(map[string]any{
		"status":  StatusApproved,
		"applied": json.RawMessage(result),
		"edited":  editedFlag,
	})
	if err != nil {
		return nil, err
	}
	return &Decision{Draft: draft, Applied: result, ToolResult: toolResult, Edited: editedFlag}, nil
}

func (s *Service) execute(ctx context.Context, draft *Draft, payload json.RawMessage) (json.RawMessage, error) {
	tx, err := s.finance.BeginWrite(ctx, draft.UserID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var result json.RawMessage
	switch draft.Kind {
	case KindCreateTransactions:
		result, err = s.executeCreate(ctx, tx, draft, payload)
	case KindUpdateTransaction:
		result, err = s.executeUpdate(ctx, tx, draft, payload)
	case KindDeleteTransaction:
		result, err = s.executeDelete(ctx, tx, draft, payload)
	case KindUpdateBudget:
		result, err = s.executeBudget(ctx, tx, draft, payload)
	default:
		err = fmt.Errorf("drafts: unsupported kind %q", draft.Kind)
	}
	if err != nil {
		return nil, err
	}
	if err := s.store.CompleteTx(ctx, tx, draft.UserID, draft.ID, payload, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit draft execution: %w", err)
	}
	return result, nil
}

func (s *Service) executeCreate(ctx context.Context, tx *sql.Tx, draft *Draft, payload json.RawMessage) (json.RawMessage, error) {
	var input createPayload
	if err := json.Unmarshal(payload, &input); err != nil {
		return nil, &finance.ValidationError{Fields: map[string]string{"items": "must_be_a_valid_list"}}
	}
	created := make([]finance.Transaction, 0, len(input.Items))
	for _, item := range input.Items {
		record, err := s.finance.CreateAgentTransaction(ctx, tx, draft.UserID, finance.TransactionInput{
			AmountCents: item.AmountCents, SpentOn: item.SpentOn, Category: item.Category,
			Merchant: item.Merchant, Note: item.Note,
		}, finance.AgentWriteTarget{DraftID: draft.ID})
		if err != nil {
			return nil, err
		}
		created = append(created, *record)
	}
	ids := make([]int64, 0, len(created))
	for _, record := range created {
		ids = append(ids, record.ID)
	}
	return json.Marshal(map[string]any{"transaction_ids": ids, "transactions": created})
}

func (s *Service) executeUpdate(ctx context.Context, tx *sql.Tx, draft *Draft, payload json.RawMessage) (json.RawMessage, error) {
	var input updatePayload
	if err := json.Unmarshal(payload, &input); err != nil {
		return nil, &finance.ValidationError{Fields: map[string]string{"changes": "must_be_a_supported_patch"}}
	}
	patch, err := finance.ParseTransactionPatch(input.Changes)
	if err != nil {
		return nil, err
	}
	record, err := s.finance.UpdateAgentTransaction(ctx, tx, draft.UserID, input.TransactionID, patch,
		finance.AgentWriteTarget{DraftID: draft.ID})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"transaction": record})
}

func (s *Service) executeDelete(ctx context.Context, tx *sql.Tx, draft *Draft, payload json.RawMessage) (json.RawMessage, error) {
	var input deletePayload
	if err := json.Unmarshal(payload, &input); err != nil {
		return nil, &finance.ValidationError{Fields: map[string]string{"transaction_id": "must_be_a_transaction"}}
	}
	if err := s.finance.DeleteAgentTransaction(ctx, tx, draft.UserID, input.TransactionID); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"deleted_transaction_id": input.TransactionID})
}

func (s *Service) executeBudget(ctx context.Context, tx *sql.Tx, draft *Draft, payload json.RawMessage) (json.RawMessage, error) {
	var input budgetPayload
	if err := json.Unmarshal(payload, &input); err != nil {
		return nil, &finance.ValidationError{Fields: map[string]string{"month": "must_use_YYYY-MM"}}
	}
	budget, err := s.finance.SaveAgentBudget(ctx, tx, draft.UserID, input.Month, input.BudgetInput)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"budget": budget})
}

// validate re-runs the same finance rules the tools used, so an edited payload
// cannot smuggle in an invalid amount, category or budget.
func (s *Service) validate(kind string, payload, before json.RawMessage) (json.RawMessage, error) {
	now := s.now()
	switch kind {
	case KindCreateTransactions:
		var input createPayload
		if err := strictUnmarshal(payload, &input); err != nil {
			return nil, err
		}
		if len(input.Items) < 1 || len(input.Items) > finance.MaxProposalItems {
			return nil, &finance.ValidationError{Fields: map[string]string{
				"items": "must_contain_between_1_and_20_transactions",
			}}
		}
		items := make([]createItem, 0, len(input.Items))
		for _, item := range input.Items {
			if item.SpentOn == "" {
				item.SpentOn = finance.SingaporeToday(now)
			}
			if err := finance.ValidateTransactionInput(finance.TransactionInput{
				AmountCents: item.AmountCents, SpentOn: item.SpentOn, Category: item.Category,
				Merchant: item.Merchant, Note: item.Note,
			}, now); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return json.Marshal(createPayload{Items: items})
	case KindUpdateTransaction:
		var input updatePayload
		if err := strictUnmarshal(payload, &input); err != nil {
			return nil, err
		}
		if input.TransactionID <= 0 {
			return nil, &finance.ValidationError{Fields: map[string]string{"transaction_id": "must_be_a_transaction"}}
		}
		current, err := decodeBefore(before)
		if err != nil {
			return nil, err
		}
		patch, err := finance.ParseTransactionPatch(input.Changes)
		if err != nil {
			return nil, err
		}
		_, changed, err := finance.ApplyTransactionPatch(current, patch, now)
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, &finance.ValidationError{Fields: map[string]string{"changes": "must_change_at_least_one_field"}}
		}
		return json.Marshal(input)
	case KindDeleteTransaction:
		var input deletePayload
		if err := strictUnmarshal(payload, &input); err != nil {
			return nil, err
		}
		if input.TransactionID <= 0 {
			return nil, &finance.ValidationError{Fields: map[string]string{"transaction_id": "must_be_a_transaction"}}
		}
		return json.Marshal(input)
	case KindUpdateBudget:
		var input budgetPayload
		if err := strictUnmarshal(payload, &input); err != nil {
			return nil, err
		}
		if err := finance.ValidateBudgetMonth(input.Month); err != nil {
			return nil, err
		}
		current, err := decodeBudgetBefore(before)
		if err != nil {
			return nil, err
		}
		merged, err := finance.MergeBudgetInput(current, input.BudgetInput)
		if err != nil {
			return nil, err
		}
		return json.Marshal(budgetPayload{Month: input.Month, BudgetInput: merged})
	default:
		return nil, fmt.Errorf("drafts: unsupported kind %q", kind)
	}
}

func decodeBefore(raw json.RawMessage) (finance.Transaction, error) {
	if len(raw) == 0 {
		return finance.Transaction{}, &finance.ValidationError{Fields: map[string]string{
			"transaction_id": "must_be_an_existing_transaction",
		}}
	}
	var current finance.Transaction
	if err := json.Unmarshal(raw, &current); err != nil || current.ID <= 0 {
		return finance.Transaction{}, &finance.ValidationError{Fields: map[string]string{
			"transaction_id": "must_be_an_existing_transaction",
		}}
	}
	return current, nil
}

func decodeBudgetBefore(raw json.RawMessage) (*finance.Budget, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var current finance.Budget
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, &finance.ValidationError{Fields: map[string]string{"month": "must_be_an_existing_budget"}}
	}
	return &current, nil
}

func rejectionGuidance(reason string) string {
	if reason == "" {
		return ""
	}
	return "Reason: " + reason + ". "
}

func strictUnmarshal(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &finance.ValidationError{Fields: map[string]string{"payload": "must_match_the_draft_shape"}}
	}
	return nil
}

func validKind(kind string) bool {
	switch kind {
	case KindCreateTransactions, KindUpdateTransaction, KindDeleteTransaction, KindUpdateBudget:
		return true
	default:
		return false
	}
}

var draftIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newDraftID returns "d_" plus 24 base32 characters (26 characters total),
// matching the CHAR(26) primary key.
func newDraftID() string {
	buffer := make([]byte, 15)
	if _, err := rand.Read(buffer); err != nil {
		panic("drafts: crypto/rand unavailable")
	}
	return "d_" + strings.ToLower(draftIDEncoding.EncodeToString(buffer))
}

// Payload types shared with the tools through the wire format.
type createItem struct {
	AmountCents int64            `json:"amount_cents"`
	SpentOn     string           `json:"spent_on"`
	Category    insight.Category `json:"category"`
	Merchant    *string          `json:"merchant"`
	Note        *string          `json:"note"`
}

type createPayload struct {
	Items []createItem `json:"items"`
}

type updatePayload struct {
	TransactionID int64           `json:"transaction_id"`
	Changes       json.RawMessage `json:"changes"`
}

type deletePayload struct {
	TransactionID int64  `json:"transaction_id"`
	Reason        string `json:"reason,omitempty"`
}

type budgetPayload struct {
	Month string `json:"month"`
	finance.BudgetInput
}
