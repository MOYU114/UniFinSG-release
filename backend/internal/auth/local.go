package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"

	"unichatgo/internal/models"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUsernameExists     = errors.New("username already exists")
	localUsername         = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)
)

const (
	prehashedBcryptPrefix = "bcrypt-sha256$"
	legacyBcryptPrefix    = "bcrypt-legacy-trim$"
)

type CredentialsValidationError struct {
	Fields map[string]string
}

func (e *CredentialsValidationError) Error() string {
	return "username or password does not meet the requirements"
}

func (s *Service) RegisterLocalUser(ctx context.Context, username, password string) (*models.User, error) {
	fields := make(map[string]string)
	if !localUsername.MatchString(username) {
		fields["username"] = "must_be_3_to_32_letters_digits_or_underscores"
	}
	if utf8.RuneCountInString(password) < 8 {
		fields["password"] = "must_be_at_least_8_characters"
	}
	if len(fields) > 0 {
		return nil, &CredentialsValidationError{Fields: fields}
	}
	hash, err := encodePassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, hash, now,
	)
	if err != nil {
		if isDuplicateUsername(err) {
			return nil, ErrUsernameExists
		}
		return nil, fmt.Errorf("create local user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read local user id: %w", err)
	}
	return &models.User{ID: id, Username: username, CreatedAt: now}, nil
}

func (s *Service) AuthenticateLocalUser(ctx context.Context, username, password string) (*models.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	var user models.User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`,
		username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("find local user: %w", err)
	}
	if len(user.PasswordHash) != sha256.Size*2 {
		if !passwordMatches(user.PasswordHash, password) {
			return nil, ErrInvalidCredentials
		}
		user.PasswordHash = ""
		return &user, nil
	}

	legacyHash, err := hex.DecodeString(user.PasswordHash)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	// The legacy service trimmed passwords before hashing.
	canonicalPassword := strings.TrimSpace(password)
	sum := sha256.Sum256([]byte(canonicalPassword))
	if subtle.ConstantTimeCompare(legacyHash, sum[:]) != 1 {
		return nil, ErrInvalidCredentials
	}
	hash, err := encodePassword(canonicalPassword)
	if err != nil {
		return nil, fmt.Errorf("upgrade legacy password: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ? AND password_hash = ?`,
		legacyBcryptPrefix+hash, user.ID, user.PasswordHash,
	)
	if err != nil {
		return nil, fmt.Errorf("save upgraded password: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read password upgrade result: %w", err)
	}
	if updated == 0 {
		// A concurrent login may have migrated this same password.
		var currentHash string
		err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, user.ID).Scan(&currentHash)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		if err != nil {
			return nil, fmt.Errorf("reload upgraded password: %w", err)
		}
		if !passwordMatches(currentHash, password) {
			return nil, ErrInvalidCredentials
		}
	}
	user.PasswordHash = ""
	return &user, nil
}

func encodePassword(password string) (string, error) {
	prefix := ""
	input := []byte(password)
	if len(input) > 72 {
		// bcrypt's input limit is bytes, not characters. Version the encoding
		// instead of imposing an undocumented API limit or truncating a password.
		prefix = prehashedBcryptPrefix
		sum := sha256.Sum256(input)
		input = []byte(hex.EncodeToString(sum[:]))
	}
	hash, err := bcrypt.GenerateFromPassword(input, bcrypt.DefaultCost)
	return prefix + string(hash), err
}

func passwordMatches(encoded, password string) bool {
	if strings.HasPrefix(encoded, legacyBcryptPrefix) {
		// Legacy accounts retain their historical normalization after migration.
		encoded = strings.TrimPrefix(encoded, legacyBcryptPrefix)
		password = strings.TrimSpace(password)
	}
	if strings.HasPrefix(encoded, prehashedBcryptPrefix) {
		encoded = strings.TrimPrefix(encoded, prehashedBcryptPrefix)
		sum := sha256.Sum256([]byte(password))
		password = hex.EncodeToString(sum[:])
	} else if len(password) > 72 {
		// Reject rather than accept a suffix that bcrypt cannot distinguish.
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
}

func isDuplicateUsername(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return true
	}
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique
}
