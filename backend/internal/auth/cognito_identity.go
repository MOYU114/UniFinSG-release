package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
)

func (handler *CognitoHandler) provisionIdentity(ctx context.Context, identity CognitoIdentity) (int64, error) {
	// A concurrent first login can race on the unique sub index. Retry from a
	// fresh transaction; never resolve a user through email or username.
	for attempt := 0; attempt < 3; attempt++ {
		id, err := handler.provisionIdentityAttempt(ctx, identity)
		if err == nil {
			return id, nil
		}
		var driverErr *mysql.MySQLError
		if !errors.As(err, &driverErr) || (driverErr.Number != 1062 && driverErr.Number != 1213) {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	return 0, errors.New("unable to resolve Cognito identity")
}

func (handler *CognitoHandler) provisionIdentityAttempt(ctx context.Context, identity CognitoIdentity) (int64, error) {
	tx, err := handler.service.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var userID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE cognito_sub = ? FOR UPDATE`, identity.Subject).Scan(&userID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Hash the opaque subject only for the internal username placeholder;
		// identity lookup still uses the original bytes in cognito_sub. This
		// reserved 72-character name cannot be registered by the local API.
		result, err := tx.ExecContext(ctx, `INSERT INTO users
			(username, password_hash, created_at, cognito_sub, email, cognito_username)
			VALUES (?, ?, ?, ?, ?, ?)`,
			cognitoLocalUsername(identity.Subject), "!cognito", handler.now().UTC(),
			identity.Subject, identity.Email, identity.Username)
		if err != nil {
			return 0, err
		}
		userID, err = result.LastInsertId()
		if err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	default:
		_, err := tx.ExecContext(ctx, `UPDATE users SET email = ?, cognito_username = ?
			WHERE id = ? AND cognito_sub = ?`, identity.Email, identity.Username, userID, identity.Subject)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit Cognito identity: %w", err)
	}
	return userID, nil
}

func cognitoLocalUsername(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return "cognito_" + hex.EncodeToString(sum[:])
}
