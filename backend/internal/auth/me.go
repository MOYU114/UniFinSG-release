package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
)

var (
	// Never present a local username as a Cognito email.
	ErrCurrentUserUnavailable = errors.New("current user identity is unavailable")
	meSingapore               = func() *time.Location {
		location, err := time.LoadLocation("Asia/Singapore")
		if err != nil {
			panic(err) // time/tzdata embeds this zone for minimal containers.
		}
		return location
	}()
)

type CurrentUserInfo struct {
	UserID       int64  `json:"user_id"`
	Email        string `json:"email"`
	AuthMode     string `json:"auth_mode"`
	Onboarded    bool   `json:"onboarded"`
	CurrentMonth string `json:"current_month"`
}

// CurrentUser reads the identity and onboarding state after the HTTP boundary
// has ensured the current budget through the finance service.
func (s *Service) CurrentUser(ctx context.Context, userID int64, mode string, now time.Time) (*CurrentUserInfo, error) {
	if userID <= 0 {
		return nil, ErrInvalidToken
	}
	if mode == "" {
		mode = ModeLocal
	}
	if (mode != ModeLocal && mode != ModeCognito) || s == nil || s.db == nil {
		return nil, ErrCurrentUserUnavailable
	}
	identityColumn := "u.username"
	if mode == ModeCognito {
		identityColumn = "CASE WHEN u.cognito_sub IS NOT NULL THEN u.email ELSE NULL END"
	}
	user := &CurrentUserInfo{
		UserID: userID, AuthMode: mode, CurrentMonth: now.In(meSingapore).Format("2006-01"),
	}
	var email sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT `+identityColumn+`,
		EXISTS(SELECT 1 FROM user_profiles p WHERE p.user_id = u.id)
		AND EXISTS(SELECT 1 FROM budgets b WHERE b.user_id = u.id AND b.month = ?)
		FROM users u WHERE u.id = ?`, user.CurrentMonth, userID).
		Scan(&email, &user.Onboarded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("read current user: %w", err)
	}
	if !email.Valid || email.String == "" {
		return nil, ErrCurrentUserUnavailable
	}
	user.Email = email.String
	return user, nil
}

// CurrentBudgetInitializer keeps budget generation in finance without an import
// cycle between the finance HTTP handlers and authentication.
type CurrentBudgetInitializer interface {
	EnsureCurrentBudget(context.Context, int64, time.Time) error
}

// RegisterUserRoutes requires the shared budget initializer before serving /me.
func (h *HTTPHandler) RegisterUserRoutes(api *gin.RouterGroup, budgets CurrentBudgetInitializer) {
	if budgets == nil {
		panic("current budget initializer is required")
	}
	api.GET("/me", h.service.APIMiddleware(), func(c *gin.Context) {
		h.currentUser(c, budgets)
	})
}

func (h *HTTPHandler) currentUser(c *gin.Context, budgets CurrentBudgetInitializer) {
	userID, _ := UserIDFromContext(c)
	now := time.Now()
	if err := budgets.EnsureCurrentBudget(c.Request.Context(), userID, now); err != nil {
		WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Account information is temporarily unavailable.", nil)
		return
	}
	user, err := h.service.CurrentUser(c.Request.Context(), userID, h.mode, now)
	if err != nil {
		if errors.Is(err, ErrInvalidToken) {
			WriteAPIError(c, http.StatusUnauthorized, "unauthenticated", "The session is invalid or expired.", nil)
		} else {
			WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Account information is temporarily unavailable.", nil)
		}
		return
	}
	c.JSON(http.StatusOK, user)
}
