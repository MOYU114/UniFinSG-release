package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	migrate "github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"unichatgo/internal/storage"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

// This suite requires migration 009 and a dedicated empty MySQL schema.
// RSA/JWKS/provider exchanges remain local simulations, not a real user pool.
func TestMySQLCognitoIdentityAndAccountLifecycle(t *testing.T) {
	db := testdb.Open(t, "TEST_MYSQL_COGNITO_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	fixture := newCognitoTestHarness(t)
	service := NewService(db, nil, time.Hour)
	fixture.handler.service = service
	fixture.handler.resolveIdentity = fixture.handler.provisionIdentity
	identity := CognitoIdentity{
		Subject: "provider:opaque/subject+not-a-uuid", Email: "student@u.nus.edu", Username: "stored-cognito-username",
	}
	first, err := fixture.handler.provisionIdentity(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	if first <= 0 {
		t.Fatal("Cognito identity was not mapped to an integer user ID")
	}
	identity.Email, identity.Username = "updated@u.nus.edu", "updated-provider-username"
	again, err := fixture.handler.provisionIdentity(ctx, identity)
	if err != nil || again != first {
		t.Fatalf("repeat login created a different user: first=%d repeated=%d err=%v", first, again, err)
	}
	var email, username, password, providerName string
	if err := db.QueryRowContext(ctx, `SELECT email, username, password_hash, cognito_username FROM users WHERE id = ?`,
		first).Scan(&email, &username, &password, &providerName); err != nil {
		t.Fatal(err)
	}
	if email != identity.Email || username != cognitoLocalUsername(identity.Subject) || len(username) != 72 || password != "!cognito" ||
		providerName != identity.Username {
		t.Fatal("persisted verified identity changed or is locally password-authenticatable")
	}
	if _, err := service.AuthenticateLocalUser(ctx, username, "!cognito"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Cognito account accepted a local password: %v", err)
	}

	opaqueIdentities := make(map[string]int64)
	t.Run("opaque subjects compare byte for byte without truncation", func(t *testing.T) {
		for _, subject := range []string{
			"opaque-subject", "Opaque-subject", "opaque-subject ", "opaque-subject\x00", strings.Repeat("s", 255),
		} {
			value := CognitoIdentity{Subject: subject, Email: identity.Email, Username: "opaque-provider-user"}
			id, err := fixture.handler.provisionIdentity(ctx, value)
			if err != nil {
				t.Fatalf("provision opaque subject: %v", err)
			}
			for _, prior := range opaqueIdentities {
				if prior == id {
					t.Fatal("distinct subject bytes were merged into one account")
				}
			}
			if id == first {
				t.Fatal("matching email merged different subjects")
			}
			opaqueIdentities[subject] = id
			var saved string
			if err := db.QueryRowContext(ctx, `SELECT cognito_sub FROM users WHERE id = ?`, id).Scan(&saved); err != nil || saved != subject {
				t.Fatalf("opaque subject was truncated or normalized: %v", err)
			}
			repeated, err := fixture.handler.provisionIdentity(ctx, value)
			if err != nil || repeated != id {
				t.Fatalf("exact subject did not resolve its existing user: first=%d repeated=%d err=%v", id, repeated, err)
			}
		}
	})

	t.Run("parallel first login resolves the same sub exactly once", func(t *testing.T) {
		concurrentIdentity := CognitoIdentity{
			Subject: "0ecfeef5-f815-41eb-88b7-d8e8f705ac18", Email: identity.Email, Username: "parallel-provider-username",
		}
		const workers = 6
		var wait sync.WaitGroup
		ids, failures := make(chan int64, workers), make(chan error, workers)
		for i := 0; i < workers; i++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				id, err := fixture.handler.provisionIdentity(ctx, concurrentIdentity)
				ids <- id
				failures <- err
			}()
		}
		wait.Wait()
		close(ids)
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		var expected int64
		for id := range ids {
			if expected == 0 {
				expected = id
			}
			if id != expected || id == first {
				t.Fatal("concurrent mapping split one sub or merged users with matching email")
			}
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE cognito_sub = ?`,
			concurrentIdentity.Subject).Scan(&count); err != nil || count != 1 {
			t.Fatalf("sub uniqueness failed: count=%d err=%v", count, err)
		}
	})

	t.Run("legacy username collision cannot link an unrelated account", func(t *testing.T) {
		collision := CognitoIdentity{
			Subject: "8b193ff0-bc39-4b05-83c9-3c5a9d9a136a", Email: "collision@u.nus.edu", Username: "collision-provider",
		}
		result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at)
			VALUES (?, 'legacy-marker', ?)`, cognitoLocalUsername(collision.Subject), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		localID, _ := result.LastInsertId()
		if _, err := fixture.handler.provisionIdentity(ctx, collision); err == nil {
			t.Fatal("username collision linked a local account to an unrelated sub")
		}
		var subject, storedEmail sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT cognito_sub, email FROM users WHERE id = ?`,
			localID).Scan(&subject, &storedEmail); err != nil || subject.Valid || storedEmail.Valid {
			t.Fatalf("collision changed the unrelated local account: %v", err)
		}
	})

	t.Run("callback creates a usable SQL session", func(t *testing.T) {
		fixture.claims["sub"], fixture.claims["email"], fixture.claims["cognito:username"] =
			identity.Subject, identity.Email, identity.Username
		cookie, _ := fixture.login(t, "")
		result := fixture.callback(cookie, fixture.expectedFlow.State)
		if result.Code != http.StatusFound || result.Header().Get("Location") != "http://localhost:3000/dashboard" {
			t.Fatalf("real SQL callback failed: %d %s", result.Code, result.Header().Get("Location"))
		}
		var session string
		for _, cookie := range result.Result().Cookies() {
			if cookie.Name == "auth_token" {
				session = cookie.Value
			}
		}
		if id, err := service.ValidateToken(ctx, session); err != nil || id != first {
			t.Fatalf("callback issued an invalid SQL session: id=%d err=%v", id, err)
		}
	})

	t.Run("cognito deletion cascades personal rows after ownership checks", func(t *testing.T) {
		otherIdentity := CognitoIdentity{
			Subject: "7891253c-0e65-49eb-9ba9-d09eca6eb125", Email: "other@u.nus.edu", Username: "other-provider",
		}
		other, err := fixture.handler.provisionIdentity(ctx, otherIdentity)
		if err != nil {
			t.Fatal(err)
		}
		seedAccountPersonalRows(t, db, first)
		seedAccountPersonalRows(t, db, other)
		token, err := service.IssueToken(ctx, first)
		if err != nil {
			t.Fatal(err)
		}
		second, err := service.IssueToken(ctx, first)
		if err != nil {
			t.Fatal(err)
		}
		otherToken, err := service.IssueToken(ctx, other)
		if err != nil {
			t.Fatal(err)
		}
		var calls int
		deleter := accountDeleteFunc(func(ctx context.Context, actual string) error {
			calls++
			if actual != identity.Username {
				t.Fatal("deletion used an unverified request identity")
			}
			assertAccountRows(t, ctx, db, first, 0)
			return errors.New("sensitive-provider-failure")
		})
		handler, err := NewAccountHTTPHandler(service, ModeCognito, deleter)
		if err != nil {
			t.Fatal(err)
		}
		router := gin.New()
		handler.RegisterRoutes(router.Group("/api"))
		ownPath, otherPath := fmt.Sprintf("/api/users/%d", first), fmt.Sprintf("/api/users/%d", other)
		assertAuthError(t, accountRequest(router, otherPath, `{"confirm":"DELETE"}`, token, true), 403, "forbidden")
		assertAuthError(t, accountRequest(router, ownPath, `{"confirm":"DELETE"}`, token, false), 403, "csrf_invalid")
		assertAuthError(t, accountRequest(router, ownPath, `{"confirm":"delete"}`, token, true), 400, "validation_failed")
		assertAccountRows(t, ctx, db, first, 1)
		response := accountRequest(router, ownPath, `{"confirm":"DELETE","username":"injected","sub":"injected"}`, token, true)
		if response.Code != http.StatusNoContent || calls != 1 || response.Body.Len() != 0 {
			t.Fatalf("local deletion failed when provider failed: status=%d calls=%d", response.Code, calls)
		}
		assertAccountRows(t, ctx, db, first, 0)
		assertAccountRows(t, ctx, db, other, 1)
		for _, revoked := range []string{token, second} {
			if _, err := service.ValidateToken(ctx, revoked); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("account token remains usable after deletion: %v", err)
			}
		}
		if id, err := service.ValidateToken(ctx, otherToken); err != nil || id != other {
			t.Fatalf("another account's session changed: id=%d err=%v", id, err)
		}
	})

	t.Run("009 rollback preserves widened subject bytes and replays", func(t *testing.T) {
		source, err := iofs.New(os.DirFS("../../migrations"), ".")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			_ = source.Close()
			t.Fatal(err)
		}
		driver, err := migratemysql.WithConnection(ctx, conn, &migratemysql.Config{
			MigrationsTable: "unifinsg_schema_migrations", StatementTimeout: 30 * time.Second,
		})
		if err != nil {
			_ = source.Close()
			_ = conn.Close()
			t.Fatal(err)
		}
		migrator, err := migrate.NewWithInstance("iofs", source, "mysql", driver)
		if err != nil {
			_ = source.Close()
			_ = driver.Close()
			t.Fatal(err)
		}
		// Roll back to the version just before 009 explicitly. A relative
		// Steps(-1) only undoes the latest migration, which stops being 009 as
		// soon as later migrations (e.g. 010/011 assistant tables) are added.
		downErr := migrator.Migrate(cognitoIdentityMigrationVersion - 1)
		sourceErr, databaseErr := migrator.Close()
		if err := errors.Join(downErr, sourceErr, databaseErr); err != nil {
			t.Fatalf("009 down could not safely preserve opaque subjects: %v", err)
		}
		for subject, id := range opaqueIdentities {
			var stored string
			if err := db.QueryRowContext(ctx, `SELECT cognito_sub FROM users WHERE id = ?`, id).Scan(&stored); err != nil || stored != subject {
				t.Fatalf("009 rollback changed identity bytes for user %d: %v", id, err)
			}
		}
		var displayColumns int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = 'users' AND column_name IN ('email', 'cognito_username')`).
			Scan(&displayColumns); err != nil || displayColumns != 0 {
			t.Fatalf("009 down did not remove display columns: count=%d err=%v", displayColumns, err)
		}
		if err := migrations.Apply(ctx, db); err != nil {
			t.Fatalf("009 replay failed: %v", err)
		}
		for subject, expected := range opaqueIdentities {
			id, err := fixture.handler.provisionIdentity(ctx, CognitoIdentity{
				Subject: subject, Email: identity.Email, Username: "opaque-provider-user",
			})
			if err != nil || id != expected {
				t.Fatalf("009 replay changed identity: got=%d expected=%d err=%v", id, expected, err)
			}
		}
	})
}

// cognitoIdentityMigrationVersion is 000009_users_cognito_identity.
const cognitoIdentityMigrationVersion uint = 9

func seedAccountPersonalRows(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	now := time.Now().UTC()
	for _, statement := range []string{
		`INSERT INTO user_profiles (user_id, monthly_disposable_cents, housing, default_budget_cents)
			VALUES (?, 100000, 'on_campus', 90000)`,
		`INSERT INTO budgets (user_id, month, total_cents) VALUES (?, '2026-10', 90000)`,
		`INSERT INTO transactions (user_id, amount_cents, spent_on, category) VALUES (?, 1000, '2026-10-28', 'food')`,
	} {
		if _, err := db.Exec(statement, userID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO budget_categories (budget_id, category, budget_cents)
		SELECT id, 'food', 90000 FROM budgets WHERE user_id = ?`, userID); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO sessions (user_id, title, created_at, updated_at) VALUES (?, 'Test chat', ?, ?)`, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ := result.LastInsertId()
	if _, err := db.Exec(`INSERT INTO messages (user_id, session_id, role, content, created_at)
		VALUES (?, ?, 'user', 'test-only private text', ?)`, userID, sessionID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO apiKeys (user_id, provider, api_key, created_at)
		VALUES (?, 'test-only-provider', 'test-only-value', ?)`, userID, now); err != nil {
		t.Fatal(err)
	}
}

func assertAccountRows(t *testing.T, ctx context.Context, db *sql.DB, userID int64, expected int) {
	t.Helper()
	for _, table := range []string{"users", "user_profiles", "budgets", "transactions", "sessions", "messages", "apiKeys"} {
		column := "user_id"
		if table == "users" {
			column = "id"
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+" = ?", userID).Scan(&count); err != nil || count != expected {
			t.Fatalf("account table %s count=%d want=%d err=%v", table, count, expected, err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budget_categories bc JOIN budgets b ON b.id = bc.budget_id
		WHERE b.user_id = ?`, userID).Scan(&count); err != nil || count != expected {
		t.Fatalf("budget category cascade count=%d want=%d err=%v", count, expected, err)
	}
	if expected == 0 {
		// The FK must prevent orphaned rows, not merely make the ownership JOIN
		// invisible after its budget was removed.
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budget_categories bc LEFT JOIN budgets b ON b.id = bc.budget_id
			WHERE b.id IS NULL`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphaned categories remain: count=%d err=%v", count, err)
		}
	}
}

func TestCognitoCallbackRejectsProvisioningFailureWithoutDetails(t *testing.T) {
	fixture := newCognitoTestHarness(t)
	fixture.handler.resolveIdentity = func(context.Context, CognitoIdentity) (int64, error) {
		return 0, errors.New("private-db-details")
	}
	cookie, _ := fixture.login(t, "")
	response := fixture.callback(cookie, fixture.expectedFlow.State)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "http://localhost:3000/signin?error=cognito_unavailable" {
		t.Fatalf("provisioning failure was not handled: %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "private") {
		t.Fatal("database error leaked into callback response")
	}
}
