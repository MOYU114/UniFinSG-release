package finance

import (
	"context"
	"database/sql"
	"time"
)

// AgentWriteTarget carries the draft that authorised a write. Every agent
// write sets transactions.source='agent' and transactions.draft_id, so the
// ledger and the confirmation stay linked.
type AgentWriteTarget struct {
	DraftID string
}

// CreateAgentTransaction inserts one transaction inside the caller's
// transaction. The draft decision endpoint owns the transaction and commits
// the ledger write together with the draft result.
func (s *Service) CreateAgentTransaction(ctx context.Context, tx *sql.Tx, userID int64,
	input TransactionInput, target AgentWriteTarget) (*Transaction, error) {
	record, err := normalizeTransaction(input, s.now())
	if err != nil {
		return nil, err
	}
	draftID := target.DraftID
	return s.insertTransaction(ctx, tx, userID, record, &provenance{Source: SourceAgent, DraftID: &draftID})
}

// UpdateAgentTransaction applies a validated patch inside the caller's
// transaction and marks the record as agent-written.
func (s *Service) UpdateAgentTransaction(ctx context.Context, tx *sql.Tx, userID, transactionID int64,
	patch TransactionPatch, target AgentWriteTarget) (*Transaction, error) {
	draftID := target.DraftID
	return s.updateTransactionInTx(ctx, tx, userID, transactionID, patch,
		&provenance{Source: SourceAgent, DraftID: &draftID})
}

// DeleteAgentTransaction removes one transaction inside the caller's
// transaction.
func (s *Service) DeleteAgentTransaction(ctx context.Context, tx *sql.Tx, userID, transactionID int64) error {
	return s.deleteTransactionInTx(ctx, tx, userID, transactionID)
}

// SaveAgentBudget writes a budget inside the caller's transaction and marks
// its provenance as agent.
func (s *Service) SaveAgentBudget(ctx context.Context, tx *sql.Tx, userID int64, month string,
	input BudgetInput) (*Budget, error) {
	return s.saveBudgetInTx(ctx, tx, userID, month, input, SourceAgent)
}

// SingaporeToday is the calendar date the agent should default to when a tool
// input omits spent_on.
func SingaporeToday(now time.Time) string {
	return now.In(singapore).Format("2006-01-02")
}
