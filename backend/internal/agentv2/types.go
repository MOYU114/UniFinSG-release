// Package agentv2 implements the UniFinSG 2.0 conversation API: sessions,
// message history, the ReAct turn runner, draft approval and the SSE
// endpoints.
package agentv2

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Roles stored in messages.role.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Part types stored in messages.parts (design section 4.1).
const (
	PartText       = "text"
	PartImage      = "image"
	PartToolCall   = "tool_call"
	PartToolResult = "tool_result"
	PartDraft      = "draft"
)

// Turn statuses stored in agent_turns.status.
const (
	TurnRunning   = "running"
	TurnCompleted = "completed"
	TurnApproval  = "awaiting_approval"
	TurnFailed    = "failed"
)

// DefaultSessionTitle is replaced by the first user message (first 20 runes).
const DefaultSessionTitle = "New chat"

// ErrNotFound is returned for sessions and messages the caller does not own.
var ErrNotFound = errors.New("agent session not found")

// ErrInvalidCursor is returned for a malformed session pagination cursor.
var ErrInvalidCursor = errors.New("invalid session cursor")

// Part is one element of messages.parts.
type Part struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	UploadID int64  `json:"upload_id,omitempty"`
	// Expired is filled on the read path so history can render "Image expired"
	// without exposing storage keys.
	Expired bool            `json:"expired,omitempty"`
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	// Provider metadata is persisted for model replay, then removed from API history.
	ExtraContent json.RawMessage `json:"provider_extra,omitempty"`
	CallID       string          `json:"call_id,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`

	// Draft parts. The stored part only carries type/draft_id/kind; the read
	// path fills the live status and snapshot from agent_drafts so a reload
	// restores the card exactly.
	DraftID      string          `json:"draft_id,omitempty"`
	Kind         string          `json:"kind,omitempty"`
	Status       string          `json:"status,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Before       json.RawMessage `json:"before,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	DecidedAt    *time.Time      `json:"decided_at,omitempty"`
	RejectReason string          `json:"reject_reason,omitempty"`
}

// Session is one conversation.
type Session struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"-"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message is one stored conversation message.
type Message struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"-"`
	SessionID int64     `json:"-"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Parts     []Part    `json:"parts"`
	TurnID    string    `json:"turn_id,omitempty"`
	Verified  *bool     `json:"verified"`
	CreatedAt time.Time `json:"created_at"`
}

// Turn is the audit row for one agent turn.
type Turn struct {
	ID             string
	UserID         int64
	SessionID      int64
	Mode           string
	Kind           string
	CreditsCharged int
	Status         string
	ModelCalls     int
	ErrorCode      string
	CreatedAt      time.Time
	FinishedAt     *time.Time
}

// DraftInfo is the SSE draft event and the pending-draft marker a propose_*
// tool returns to the runner.
type DraftInfo struct {
	ID         string          `json:"draft_id"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	Before     json.RawMessage `json:"before,omitempty"`
	Warnings   []string        `json:"warnings"`
	ExpiresAt  time.Time       `json:"expires_at"`
	ToolCallID string          `json:"-"`
}

// Store persists sessions, messages and turns. Every method scopes its query
// by userID so a caller can never reach another account's data.
type Store interface {
	CreateSession(ctx context.Context, userID int64, title string) (*Session, error)
	ListSessions(ctx context.Context, userID int64, cursor string, limit int) ([]Session, string, error)
	GetSession(ctx context.Context, userID, sessionID int64) (*Session, error)
	RenameSession(ctx context.Context, userID, sessionID int64, title string) (*Session, error)
	DeleteSession(ctx context.Context, userID, sessionID int64) error
	TouchSession(ctx context.Context, userID, sessionID int64, at time.Time) error
	AutoTitle(ctx context.Context, userID, sessionID int64, title string, at time.Time) error

	InsertMessage(ctx context.Context, message Message) (*Message, error)
	ListMessages(ctx context.Context, userID, sessionID int64, before int64, limit int) ([]Message, bool, error)
	// AppendToolResult adds the decision result to the assistant message that
	// holds callID. The result must share the assistant message so adapters
	// receive every tool result immediately after its tool calls.
	AppendToolResult(ctx context.Context, userID, sessionID int64, turnID, callID string, result Part) error

	CreateTurn(ctx context.Context, turn Turn) error
	FinishTurn(ctx context.Context, turn Turn) error
	GetTurn(ctx context.Context, userID int64, turnID string) (*Turn, error)
	// ContinueTurn adds model calls to an existing turn and moves its status
	// without touching credits, which were settled when the turn paused.
	ContinueTurn(ctx context.Context, userID int64, turnID, status string, addedModelCalls int, errorCode string) error
}
