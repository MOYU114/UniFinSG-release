// Command mcp-stdio serves the UniFinSG tool registry over the Model Context
// Protocol on stdin/stdout, so local clients such as MCP Inspector or Claude
// Code can read a user's ledger and propose drafts.
//
// It is a local debugging entry point only: no HTTP listener, no routes, no
// background workers. The process acts as exactly one user, chosen with
// --user-id. Drafts created here are confirmed in the UniFinSG UI, never by
// the external client.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"unichatgo/internal/config"
	"unichatgo/internal/drafts"
	"unichatgo/internal/finance"
	"unichatgo/internal/mcpserver"
	"unichatgo/internal/storage"
	"unichatgo/internal/tools"
	"unichatgo/migrations"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	userID := flag.Int64("user-id", 0, "UniFinSG user id this MCP session acts as (required, positive)")
	configPath := flag.String("config", "", "path to config.json; defaults to $UNICHATGO_CONFIG, then config.json")
	flag.Parse()
	if err := run(*userID, *configPath); err != nil {
		log.Fatalf("mcp-stdio: %v", err)
	}
}

func run(userID int64, configPath string) error {
	if userID <= 0 {
		return errors.New("--user-id must be a positive integer")
	}
	if configPath == "" {
		configPath = os.Getenv("UNICHATGO_CONFIG")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := config.ApplyRuntimeEnvironment(cfg); err != nil {
		return fmt.Errorf("configure environment: %w", err)
	}
	if _, ok := cfg.Databases["mysql"]; !ok {
		return errors.New("MySQL is required: the config has no databases.mysql entry")
	}

	db, err := storage.Open("mysql", cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := storage.Migrate(db, "mysql"); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	migrateCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err = migrations.Apply(migrateCtx, db)
	cancel()
	if err != nil {
		return fmt.Errorf("migrate finance database: %w", err)
	}

	var exists int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("user %d not found", userID)
	}
	if err != nil {
		return fmt.Errorf("read user %d: %w", userID, err)
	}

	financeService := finance.NewService(db)
	draftService := drafts.NewService(drafts.NewMySQLStore(db), financeService, draftTTL())
	// Drafts need a real session (foreign key and UI review). Reuse one
	// dedicated conversation per user instead of inventing an orphan session.
	sessionID, err := ensureMCPSession(ctx, db, userID)
	if err != nil {
		return err
	}
	registry, err := tools.NewAgentRegistry(financeService, &visibleDrafts{
		service: draftService, db: db, sessionID: sessionID,
	}, nil)
	if err != nil {
		return fmt.Errorf("configure agent tools: %w", err)
	}
	server, err := mcpserver.New(registry, func(context.Context) (mcpserver.Identity, error) {
		return mcpserver.Identity{UserID: userID, SessionID: sessionID}, nil
	})
	if err != nil {
		return fmt.Errorf("configure MCP server: %w", err)
	}

	log.Printf("mcp-stdio: serving %d tools as user %d over stdio", len(registry.List()), userID)
	return server.Run(ctx, &mcp.StdioTransport{})
}

// visibleDrafts records a draft message in the dedicated MCP conversation so
// the UniFinSG UI renders the same DraftCard the in-app agent produces. The
// MCP client still cannot confirm; the user reviews it in /chat.
type visibleDrafts struct {
	service   *drafts.Service
	db        *sql.DB
	sessionID int64
}

func (v *visibleDrafts) Create(ctx context.Context, in drafts.CreateInput) (*drafts.Draft, error) {
	draft, err := v.service.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	// The draft is already durable; a missing message must not fail the tool.
	if err := v.record(ctx, in, draft); err != nil {
		log.Printf("mcp-stdio: draft %s created but its message was not recorded: %v", draft.ID, err)
	}
	return draft, nil
}

func (v *visibleDrafts) record(ctx context.Context, in drafts.CreateInput, draft *drafts.Draft) error {
	parts := []map[string]any{
		{
			"type": "tool_call", "id": in.ToolCallID,
			"name": proposalToolName(in.Kind), "args": json.RawMessage(in.Payload),
		},
		{"type": "draft", "draft_id": draft.ID, "kind": draft.Kind},
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = v.db.ExecContext(ctx, `INSERT INTO messages
		(user_id, session_id, role, content, parts, turn_id, verified, created_at)
		VALUES (?, ?, 'assistant', '', ?, ?, NULL, ?)`,
		in.UserID, v.sessionID, string(raw), in.TurnID, now)
	return err
}

func proposalToolName(kind string) string {
	switch kind {
	case drafts.KindCreateTransactions:
		return "propose_transactions"
	case drafts.KindUpdateTransaction:
		return "propose_update_transaction"
	case drafts.KindDeleteTransaction:
		return "propose_delete_transaction"
	case drafts.KindUpdateBudget:
		return "propose_update_budget"
	default:
		return "propose_draft"
	}
}

// ensureMCPSession returns the user's dedicated MCP conversation, creating it
// on first use. The title is stable so repeated stdio runs reuse the same row.
func ensureMCPSession(ctx context.Context, db *sql.DB, userID int64) (int64, error) {
	const title = "MCP drafts"
	var sessionID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM sessions
		WHERE user_id = ? AND title = ? AND kind = 'agent' ORDER BY id LIMIT 1`, userID, title).Scan(&sessionID)
	if err == nil {
		return sessionID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read MCP session: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	result, err := db.ExecContext(ctx, `INSERT INTO sessions (user_id, title, kind, created_at, updated_at)
		VALUES (?, ?, 'agent', ?, ?)`, userID, title, now, now)
	if err != nil {
		return 0, fmt.Errorf("create MCP session: %w", err)
	}
	sessionID, err = result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read MCP session id: %w", err)
	}
	return sessionID, nil
}

// draftTTL reads AGENT_DRAFT_TTL_MINUTES (default 30 minutes), matching the
// API server so drafts expire at the same time everywhere.
func draftTTL() time.Duration {
	raw := os.Getenv("AGENT_DRAFT_TTL_MINUTES")
	if raw == "" {
		return drafts.DefaultTTL
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 24*60 {
		log.Fatalf("invalid AGENT_DRAFT_TTL_MINUTES: %q", raw)
	}
	return time.Duration(value) * time.Minute
}
