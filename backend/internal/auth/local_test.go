package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestLocalPasswordEncodings(t *testing.T) {
	cases := []struct {
		name     string
		password string
		prefix   string
	}{
		{"normal", "Passw0rd!", "$2"},
		{"exactly_72_bytes", strings.Repeat("x", 72), "$2"},
		{"over_72_bytes", strings.Repeat("x", 73), prehashedBcryptPrefix},
		{"long_unicode", strings.Repeat("\u5b66", 30), prehashedBcryptPrefix},
		{"significant_whitespace", "  Passw0rd!  ", "$2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			t.Cleanup(func() { db.Close() })
			svc := NewService(db, nil, time.Hour)
			user, err := svc.RegisterLocalUser(context.Background(), "tester", tc.password)
			if err != nil {
				t.Fatal(err)
			}
			var stored string
			if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, user.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(stored, tc.prefix) || stored == tc.password {
				t.Fatalf("unexpected password encoding: prefix=%q", tc.prefix)
			}
			authenticated, err := svc.AuthenticateLocalUser(context.Background(), "tester", tc.password)
			if err != nil || authenticated.ID != user.ID || authenticated.PasswordHash != "" {
				t.Fatalf("login failed: user=%+v err=%v", authenticated, err)
			}
			if _, err := svc.AuthenticateLocalUser(context.Background(), "tester", tc.password+"extra"); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("accepted changed suffix: %v", err)
			}
			if tc.name == "significant_whitespace" {
				if _, err := svc.AuthenticateLocalUser(context.Background(), "tester", strings.TrimSpace(tc.password)); !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("trimmed a new password: %v", err)
				}
			}
		})
	}
}

func TestLocalRegistrationValidation(t *testing.T) {
	cases := []struct {
		name     string
		username string
		password string
		field    string
	}{
		{"short_username", "ab", "Passw0rd!", "username"},
		{"long_username", strings.Repeat("a", 33), "Passw0rd!", "username"},
		{"username_whitespace", " test ", "Passw0rd!", "username"},
		{"username_punctuation", "a.b", "Passw0rd!", "username"},
		{"username_unicode", "\u7528\u6237\u540d", "Passw0rd!", "username"},
		{"short_password", "tester", "1234567", "password"},
		{"short_unicode_password", "tester", "\u5b66\u751f\u7528\u6237", "password"},
	}
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, nil, time.Hour)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RegisterLocalUser(context.Background(), tc.username, tc.password)
			var validation *CredentialsValidationError
			if !errors.As(err, &validation) || validation.Fields[tc.field] == "" {
				t.Fatalf("expected validation field %s, got %v", tc.field, err)
			}
		})
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid registration wrote data: count=%d err=%v", count, err)
	}
}

func TestLocalRegistrationDuplicate(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, nil, time.Hour)
	if _, err := svc.RegisterLocalUser(context.Background(), "tester", "Passw0rd!"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RegisterLocalUser(context.Background(), "tester", "Different!"); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("expected duplicate username: %v", err)
	}
	if _, err := svc.AuthenticateLocalUser(context.Background(), "tester", "Passw0rd!"); err != nil {
		t.Fatalf("duplicate registration changed existing password: %v", err)
	}
}

func TestLegacyPasswordsUpgradeAfterSuccessfulLogin(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", 73), strings.Repeat("\u5b66", 30)} {
		t.Run("bytes_"+strconv.Itoa(len(password)), func(t *testing.T) {
			db := openTestDB(t)
			t.Cleanup(func() { db.Close() })
			svc := NewService(db, nil, time.Hour)
			sum := sha256.Sum256([]byte(password))
			legacy := hex.EncodeToString(sum[:])
			if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
				"old_user", legacy, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.AuthenticateLocalUser(context.Background(), "old_user", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("wrong legacy password: %v", err)
			}
			var stored string
			if err := db.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, "old_user").Scan(&stored); err != nil || stored != legacy {
				t.Fatalf("wrong password changed hash: %v", err)
			}
			if _, err := svc.AuthenticateLocalUser(context.Background(), " old_user ", " "+password+" "); err != nil {
				t.Fatalf("legacy login: %v", err)
			}
			if err := db.QueryRow(`SELECT password_hash FROM users WHERE username = ?`, "old_user").Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(stored, legacyBcryptPrefix) || !passwordMatches(stored, password) {
				t.Fatal("legacy hash was not upgraded to bcrypt")
			}
			for _, candidate := range []string{password, " " + password + " "} {
				if _, err := svc.AuthenticateLocalUser(context.Background(), "old_user", candidate); err != nil {
					t.Fatalf("migrated login changed legacy normalization: %v", err)
				}
			}
		})
	}
}

func TestConcurrentLegacyLogins(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, nil, time.Hour)
	sum := sha256.Sum256([]byte("Passw0rd!"))
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		"old_user", hex.EncodeToString(sum[:]), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.AuthenticateLocalUser(context.Background(), "old_user", "Passw0rd!")
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatalf("concurrent legacy migration failed: %v", err)
		}
	}
}

func TestExistingBcryptHashStillAuthenticates(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, nil, time.Hour)
	hash, err := bcrypt.GenerateFromPassword([]byte("Passw0rd!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		"bcrypt_user", string(hash), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateLocalUser(context.Background(), "bcrypt_user", "Passw0rd!"); err != nil {
		t.Fatal(err)
	}
}
