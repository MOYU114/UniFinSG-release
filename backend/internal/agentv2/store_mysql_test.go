package agentv2

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"unichatgo/internal/storage"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

// TestMySQLStoreLifecycle exercises the real schema added by migrations
// 000015/000016. It is skipped unless TEST_MYSQL_AGENTV2_DSN points at an
// empty exclusive unifinsg_test_* database.
func TestMySQLStoreLifecycle(t *testing.T) {
	db := testdb.Open(t, "TEST_MYSQL_AGENTV2_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		"agentv2_store", "x", time.Now().UTC())
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := result.LastInsertId()

	store := NewMySQLStore(db)
	session, err := store.CreateSession(ctx, userID, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.Title != DefaultSessionTitle {
		t.Fatalf("default title = %q", session.Title)
	}

	if err := store.AutoTitle(ctx, userID, session.ID, TitleFromContent("  这个月的预算还剩多少？还有别的吗  "), time.Now().UTC()); err != nil {
		t.Fatalf("auto title: %v", err)
	}
	renamed, err := store.GetSession(ctx, userID, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if renamed.Title != "这个月的预算还剩多少？还有别的吗" {
		t.Fatalf("auto title = %q", renamed.Title)
	}

	verified := true
	userMessage, err := store.InsertMessage(ctx, Message{
		UserID: userID, SessionID: session.ID, Role: RoleUser, Content: "hello", TurnID: "turn-1",
	})
	if err != nil {
		t.Fatalf("insert user message: %v", err)
	}
	assistant, err := store.InsertMessage(ctx, Message{
		UserID: userID, SessionID: session.ID, Role: RoleAssistant, Content: "S$12.00",
		TurnID: "turn-1", Verified: &verified,
		Parts: []Part{
			{Type: PartToolCall, ID: "call_1", Name: "get_budget_snapshot", Args: json.RawMessage(`{}`)},
			{Type: PartToolResult, CallID: "call_1", Name: "get_budget_snapshot", Result: json.RawMessage(`{"remaining_cents":1200}`)},
			{Type: PartText, Text: "S$12.00"},
		},
	})
	if err != nil {
		t.Fatalf("insert assistant message: %v", err)
	}

	messages, hasMore, err := store.ListMessages(ctx, userID, session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if hasMore || len(messages) != 2 {
		t.Fatalf("messages = %d hasMore = %v", len(messages), hasMore)
	}
	if messages[0].ID != userMessage.ID || messages[1].ID != assistant.ID {
		t.Fatalf("messages are not chronological")
	}
	if len(messages[1].Parts) != 3 || messages[1].Parts[1].CallID != "call_1" {
		t.Fatalf("parts were not round-tripped: %+v", messages[1].Parts)
	}
	if messages[1].Verified == nil || !*messages[1].Verified {
		t.Fatalf("verified flag was not stored")
	}

	sessions, next, err := store.ListSessions(ctx, userID, "", 20)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || next != "" {
		t.Fatalf("sessions = %+v next = %q", sessions, next)
	}
	if _, err := store.GetSession(ctx, userID+1, session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for another user, got %v", err)
	}
	if _, err := store.RenameSession(ctx, userID+1, session.ID, "stolen"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound renaming another user's session, got %v", err)
	}

	turn := Turn{ID: "turn-1", UserID: userID, SessionID: session.ID, Mode: "platform",
		Kind: "tool", Status: TurnRunning, CreatedAt: time.Now().UTC()}
	if err := store.CreateTurn(ctx, turn); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	finished := time.Now().UTC()
	turn.Status = TurnCompleted
	turn.CreditsCharged = 2
	turn.ModelCalls = 2
	turn.FinishedAt = &finished
	if err := store.FinishTurn(ctx, turn); err != nil {
		t.Fatalf("finish turn: %v", err)
	}

	// Upload metadata reuses temp_files and is claimed exactly once.
	uploadStore := NewMySQLUploadStore(db)
	created, err := uploadStore.CreateUpload(ctx, Upload{
		UserID: userID, SessionID: session.ID, FileName: "receipt.png",
		Key: "uploads/" + "1/20261106/receipt.png", MIME: MimePNG, Size: 12,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	if created.Status != UploadActive || created.ID <= 0 {
		t.Fatalf("created upload = %+v", created)
	}
	if err := uploadStore.ClaimUploads(ctx, userID, session.ID, []int64{created.ID}, time.Now().UTC()); err != nil {
		t.Fatalf("claim upload: %v", err)
	}
	if err := uploadStore.ClaimUploads(ctx, userID, session.ID, []int64{created.ID}, time.Now().UTC()); !errors.Is(err, ErrUploadUnavailable) {
		t.Fatalf("expected ErrUploadUnavailable on reuse, got %v", err)
	}
	if err := uploadStore.LinkUploads(ctx, userID, []int64{created.ID}, userMessage.ID); err != nil {
		t.Fatalf("link upload: %v", err)
	}
	linked, err := uploadStore.GetUpload(ctx, userID, created.ID)
	if err != nil {
		t.Fatalf("get upload: %v", err)
	}
	if linked.Status != UploadUsed || linked.MessageID != userMessage.ID {
		t.Fatalf("linked upload = %+v", linked)
	}
	if expired, err := uploadStore.ListExpiredUploads(ctx, time.Now().UTC().Add(2*time.Hour)); err != nil || len(expired) != 1 {
		t.Fatalf("expired uploads = %d err = %v", len(expired), err)
	}
	if err := uploadStore.MarkUploadsExpired(ctx, []int64{created.ID}); err != nil {
		t.Fatalf("expire upload: %v", err)
	}
	if _, err := uploadStore.GetUpload(ctx, userID+1, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user must not read the upload, got %v", err)
	}

	if err := store.DeleteSession(ctx, userID, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := store.GetSession(ctx, userID, session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session is still readable")
	}
	if _, err := uploadStore.GetUpload(ctx, userID, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the upload row was not cascaded: %v", err)
	}
}
