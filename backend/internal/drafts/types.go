// Package drafts stores agent-proposed writes and executes them only after the
// user confirms. The model never writes the ledger: tools insert a pending
// draft, the decision endpoint consumes it atomically and runs the finance
// write inside one database transaction.
package drafts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Draft kinds, matching agent_drafts.kind.
const (
	KindCreateTransactions = "create_transactions"
	KindUpdateTransaction  = "update_transaction"
	KindDeleteTransaction  = "delete_transaction"
	KindUpdateBudget       = "update_budget"
)

// Draft statuses, matching agent_drafts.status.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusExpired  = "expired"
	StatusFailed   = "failed"
)

// Decision actions.
const (
	ActionApprove = "approve"
	ActionReject  = "reject"
)

// ErrNotFound is returned when the draft does not exist or belongs to another
// user. Ownership failures are deliberately indistinguishable from absence.
var ErrNotFound = errors.New("agent draft not found")

// ErrNotPending is returned when a draft was already decided, expired or
// failed before the decision arrived.
var ErrNotPending = errors.New("agent draft is not pending")

// ErrInvalidAction reports an unsupported decision action.
var ErrInvalidAction = errors.New("unsupported draft decision")

// Draft is one stored proposal.
type Draft struct {
	ID           string
	UserID       int64
	SessionID    int64
	TurnID       string
	ToolCallID   string
	Kind         string
	Payload      json.RawMessage // validated tool input
	Before       json.RawMessage // pre-change snapshot for update/delete/budget
	Warnings     []string
	FinalPayload json.RawMessage
	Status       string
	RejectReason string
	Result       json.RawMessage
	ExpiresAt    time.Time
	DecidedAt    *time.Time
	CreatedAt    time.Time
}

// CreateInput is the validated tool output handed to the service.
type CreateInput struct {
	UserID     int64
	SessionID  int64
	TurnID     string
	ToolCallID string
	Kind       string
	Payload    json.RawMessage
	Before     json.RawMessage
	Warnings   []string
}

// DecideInput is one confirmation or rejection.
type DecideInput struct {
	UserID        int64
	DraftID       string
	Action        string
	EditedPayload json.RawMessage
	Reason        string
}

// Decision is the outcome of a decision, including the tool_result payload
// that must be appended to the paused conversation.
type Decision struct {
	Draft      *Draft
	Applied    json.RawMessage
	ToolResult json.RawMessage
	Edited     bool
}

// Store persists drafts. Every read and write is scoped by user_id.
type Store interface {
	Insert(ctx context.Context, draft Draft) (*Draft, error)
	// Get returns ErrNotFound for a missing draft or another user's draft.
	Get(ctx context.Context, userID int64, id string) (*Draft, error)
	// Consume atomically moves a pending, unexpired draft to status. It
	// reports false when another request won the race or the draft expired.
	Consume(ctx context.Context, userID int64, id, status, reason string) (bool, error)
	// CompleteTx records the executed result inside the caller's transaction.
	CompleteTx(ctx context.Context, tx *sql.Tx, userID int64, id string,
		finalPayload, result json.RawMessage) error
	MarkFailed(ctx context.Context, userID int64, id, reason string) error
	// ExpirePending marks expired pending drafts and completes their turns.
	// userID 0 expires every user's drafts (periodic cleanup).
	ExpirePending(ctx context.Context, userID int64) (int64, error)
	ListPendingForSession(ctx context.Context, userID, sessionID int64) ([]Draft, error)
}

// storedPayload is the JSON envelope kept in agent_drafts.payload. The design
// table has no columns for the before snapshot or warnings, so they travel
// with the validated input; readers flatten them back to the wire shape.
type storedPayload struct {
	Input    json.RawMessage `json:"input"`
	Before   json.RawMessage `json:"before,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

func encodePayload(input, before json.RawMessage, warnings []string) (json.RawMessage, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(storedPayload{Input: input, Before: before, Warnings: warnings})
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func decodePayload(raw json.RawMessage, draft *Draft) error {
	if len(raw) == 0 {
		return nil
	}
	var stored storedPayload
	if err := json.Unmarshal(raw, &stored); err != nil {
		return err
	}
	draft.Payload = stored.Input
	draft.Before = stored.Before
	draft.Warnings = stored.Warnings
	return nil
}

func optionalJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return string(raw)
}

func decodeOptionalJSON(value sql.NullString) json.RawMessage {
	if !value.Valid || value.String == "" {
		return nil
	}
	return json.RawMessage(value.String)
}
