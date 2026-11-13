package llmconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
)

// HTTPHandler exposes the four BYOK endpoints.
type HTTPHandler struct {
	service        *Service
	apiMiddleware  gin.HandlerFunc
	csrfMiddleware gin.HandlerFunc
}

// NewHTTPHandler uses the same auth middleware pattern as the finance and
// agent handlers.
func NewHTTPHandler(service *Service, authService *auth.Service) *HTTPHandler {
	return &HTTPHandler{
		service:        service,
		apiMiddleware:  authService.APIMiddleware(),
		csrfMiddleware: authService.APICSRFMiddleware(),
	}
}

// newTestHandler lets unit tests inject middleware without a database.
func newTestHandler(service *Service, apiMiddleware, csrfMiddleware gin.HandlerFunc) *HTTPHandler {
	return &HTTPHandler{service: service, apiMiddleware: apiMiddleware, csrfMiddleware: csrfMiddleware}
}

func (h *HTTPHandler) RegisterRoutes(api *gin.RouterGroup) {
	users := api.Group("/users/:id")
	users.Use(h.apiMiddleware, auth.RequirePathUser(), h.csrfMiddleware)
	users.GET("/llm-config", h.get)
	users.PUT("/llm-config", h.put)
	users.POST("/llm-config/test", h.test)
	users.DELETE("/llm-config", h.delete)
}

type putRequest struct {
	Kind    string  `json:"kind"`
	BaseURL string  `json:"base_url"`
	Model   string  `json:"model"`
	APIKey  *string `json:"api_key"`
	Enabled *bool   `json:"enabled"`
}

type testRequest struct {
	Kind    string  `json:"kind"`
	BaseURL string  `json:"base_url"`
	Model   string  `json:"model"`
	APIKey  *string `json:"api_key"`
}

func (h *HTTPHandler) get(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	view, err := h.service.Get(c.Request.Context(), userID)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *HTTPHandler) put(c *gin.Context) {
	var request putRequest
	if err := decodeObject(c.Request.Body, &request); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	userID, _ := auth.UserIDFromContext(c)
	view, err := h.service.Save(c.Request.Context(), userID, Input{
		Kind:    request.Kind,
		BaseURL: request.BaseURL,
		Model:   request.Model,
		APIKey:  request.APIKey,
		Enabled: request.Enabled,
	})
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *HTTPHandler) test(c *gin.Context) {
	var request testRequest
	if err := decodeOptionalObject(c.Request.Body, &request); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	userID, _ := auth.UserIDFromContext(c)
	result, err := h.service.Test(c.Request.Context(), userID, Input{
		Kind:    request.Kind,
		BaseURL: request.BaseURL,
		Model:   request.Model,
		APIKey:  request.APIKey,
	})
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *HTTPHandler) delete(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	if err := h.service.Delete(c.Request.Context(), userID); err != nil {
		h.writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *HTTPHandler) writeError(c *gin.Context, err error) {
	var validation *ValidationError
	var failed *TestFailedError
	switch {
	case errors.As(err, &validation):
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "One or more model settings are invalid.", validation.Fields)
	case errors.As(err, &failed):
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":     "llm_test_failed",
				"message":  "The model connection test failed. Check the base URL, model and API key.",
				"upstream": gin.H{"status": failed.Status, "message": failed.Message},
			},
		})
	case errors.Is(err, ErrRateLimited):
		auth.WriteAPIError(c, http.StatusTooManyRequests, "rate_limited", "Too many connection tests. Try again in a minute.", nil)
	default:
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Model settings are temporarily unavailable.", nil)
	}
}

func decodeObject(body io.Reader, target any) error {
	decoder := json.NewDecoder(body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("a JSON object is required")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("only one JSON object is allowed")
	}
	return nil
}

func decodeOptionalObject(body io.Reader, target any) error {
	if body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return decodeObject(bytes.NewReader(raw), target)
}
