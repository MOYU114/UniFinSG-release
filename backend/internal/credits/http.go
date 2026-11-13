package credits

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
	"unichatgo/internal/llm"
)

// ModeResolver reports whether the user is on the platform model or BYOK.
type ModeResolver interface {
	ModeForUser(ctx context.Context, userID int64) (string, error)
}

// HTTPHandler serves GET /api/users/:id/agent/credits.
type HTTPHandler struct {
	service        *Service
	modes          ModeResolver
	apiMiddleware  gin.HandlerFunc
	csrfMiddleware gin.HandlerFunc
}

func NewHTTPHandler(service *Service, modes ModeResolver, authService *auth.Service) *HTTPHandler {
	return &HTTPHandler{
		service:        service,
		modes:          modes,
		apiMiddleware:  authService.APIMiddleware(),
		csrfMiddleware: authService.APICSRFMiddleware(),
	}
}

func newTestHandler(service *Service, modes ModeResolver, apiMiddleware, csrfMiddleware gin.HandlerFunc) *HTTPHandler {
	return &HTTPHandler{service: service, modes: modes, apiMiddleware: apiMiddleware, csrfMiddleware: csrfMiddleware}
}

func (h *HTTPHandler) RegisterRoutes(api *gin.RouterGroup) {
	users := api.Group("/users/:id")
	users.Use(h.apiMiddleware, auth.RequirePathUser(), h.csrfMiddleware)
	users.GET("/agent/credits", h.get)
}

func (h *HTTPHandler) get(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	mode, err := h.modes.ModeForUser(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, llm.ErrNotConfigured) {
			auth.WriteAPIError(c, http.StatusServiceUnavailable, "llm_not_configured", "No model is configured for this account.", nil)
			return
		}
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Credits are temporarily unavailable.", nil)
		return
	}
	snapshot, err := h.service.Snapshot(c.Request.Context(), userID, mode)
	if err != nil {
		WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

// WriteError is shared with the P2 turn runner so 402 responses keep the same
// shape everywhere.
func WriteError(c *gin.Context, err error) {
	var exhausted *ExhaustedError
	switch {
	case errors.As(err, &exhausted):
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
			"error": gin.H{
				"code":    "credits_exhausted",
				"message": "Your daily credits are used up. They reset at midnight (SGT).",
				"details": gin.H{"resets_at": exhausted.ResetsAt.Format(time.RFC3339)},
			},
		})
	case errors.Is(err, ErrUnavailable):
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "credits_unavailable", "Credits are temporarily unavailable.", nil)
	default:
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Credits are temporarily unavailable.", nil)
	}
}
