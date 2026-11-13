package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mattn/go-sqlite3"
)

// CognitoUserDeleter accepts only the provider username stored after ID-token
// verification. The account endpoint never takes a provider identity from JSON.
type CognitoUserDeleter interface {
	DeleteUser(context.Context, string) error
}

type AccountHTTPHandler struct {
	service          *Service
	mode             string
	deleter          CognitoUserDeleter
	afterLocalDelete func(int64)
}

// SetAfterLocalDelete installs application cleanup such as stopping an in-flight
// legacy chat worker. Call it while registering routes, before serving requests.
// The hook receives only the authenticated user's ID after a committed deletion.
func (handler *AccountHTTPHandler) SetAfterLocalDelete(callback func(int64)) {
	handler.afterLocalDelete = callback
}

func NewAccountHTTPHandler(service *Service, mode string, deleter CognitoUserDeleter) (*AccountHTTPHandler, error) {
	if service == nil || service.db == nil {
		return nil, errors.New("authentication service is required")
	}
	if mode == "" {
		mode = ModeLocal
	}
	if mode != ModeLocal && mode != ModeCognito {
		return nil, errors.New("AUTH_MODE must be local or cognito")
	}
	return &AccountHTTPHandler{service: service, mode: mode, deleter: deleter}, nil
}

func (handler *AccountHTTPHandler) RegisterRoutes(api *gin.RouterGroup) {
	api.DELETE("/users/:id", handler.service.APIMiddleware(), RequirePathUser(),
		handler.service.APICSRFMiddleware(), handler.deleteAccount)
}

func (handler *AccountHTTPHandler) deleteAccount(c *gin.Context) {
	confirm, err := decodeAccountConfirmation(http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10))
	if err != nil {
		WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with a string confirmation.", nil)
		return
	}
	if confirm != "DELETE" {
		WriteAPIError(c, http.StatusBadRequest, "validation_failed", "Account deletion must be confirmed.", map[string]string{"confirm": "must_equal_DELETE"})
		return
	}
	userID, _ := UserIDFromContext(c)
	username, tokens, err := handler.deleteLocalAccount(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			WriteAPIError(c, http.StatusUnauthorized, "unauthenticated", "The account no longer exists.", nil)
		} else {
			WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Account deletion is temporarily unavailable.", nil)
		}
		return
	}
	if handler.afterLocalDelete != nil {
		handler.afterLocalDelete(userID)
	}

	// SQL remains authoritative even if Redis is unavailable. Complete cleanup
	// after the commit when a browser disconnects, with a bounded lifetime.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	handler.clearAccountCache(cleanupCtx, tokens)
	cancelCleanup()
	(&HTTPHandler{service: handler.service}).setCookies(c, "", "", -1)

	if handler.mode == ModeCognito {
		if strings.TrimSpace(username) == "" || handler.deleter == nil {
			log.Print("warning: Cognito account deletion could not be attempted after local account deletion")
		} else {
			deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
			err := handler.deleter.DeleteUser(deleteCtx, username)
			cancelDelete()
			if err != nil {
				// Never log usernames, subjects, tokens, or provider responses.
				log.Print("warning: Cognito account deletion failed after local account deletion")
			}
		}
	}
	c.Status(http.StatusNoContent)
}

func decodeAccountConfirmation(reader io.Reader) (string, error) {
	decoder := json.NewDecoder(reader)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return "", err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return "", errors.New("an object is required")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("only one JSON object is allowed")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	var confirm string
	if value, exists := fields["confirm"]; exists {
		if err := json.Unmarshal(value, &confirm); err != nil {
			return "", err
		}
	}
	return confirm, nil
}

func (handler *AccountHTTPHandler) deleteLocalAccount(ctx context.Context, userID int64) (string, []string, error) {
	tx, err := handler.service.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()

	column := "id"
	if handler.mode == ModeCognito {
		column = "cognito_username"
	}
	query := "SELECT " + column + " FROM users WHERE id = ?"
	// Local authentication retains the legacy SQLite test/development path.
	// Production MySQL locks the user before reading tokens or deleting rows.
	if _, legacySQLite := handler.service.db.Driver().(*sqlite3.SQLiteDriver); !legacySQLite {
		query += " FOR UPDATE"
	}
	var username sql.NullString
	if handler.mode == ModeCognito {
		err = tx.QueryRowContext(ctx, query, userID).Scan(&username)
	} else {
		var foundID int64
		err = tx.QueryRowContext(ctx, query, userID).Scan(&foundID)
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT token FROM user_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return "", nil, err
	}
	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			_ = rows.Close()
			return "", nil, err
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", nil, err
	}
	if err := rows.Close(); err != nil {
		return "", nil, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return "", nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return "", nil, err
	}
	if affected != 1 {
		return "", nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return username.String, tokens, nil
}

func (handler *AccountHTTPHandler) clearAccountCache(ctx context.Context, tokens []string) {
	if handler.service.rdb == nil {
		return
	}
	for _, token := range tokens {
		if err := handler.service.rdb.Del(ctx, redisTokenPrefix+token); err != nil {
			log.Print("warning: session cache cleanup failed after local account deletion")
		}
	}
}
