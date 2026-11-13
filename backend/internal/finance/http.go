package finance

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
)

type HTTPHandler struct {
	service *Service
	auth    *auth.Service
}

func NewHTTPHandler(service *Service, authService *auth.Service) *HTTPHandler {
	return &HTTPHandler{service: service, auth: authService}
}

func (h *HTTPHandler) RegisterRoutes(api *gin.RouterGroup) {
	users := api.Group("/users/:id")
	users.Use(h.auth.APIMiddleware(), auth.RequirePathUser(), h.auth.APICSRFMiddleware())
	users.GET("/profile", h.getProfile)
	users.PUT("/profile", h.saveProfile)
	users.GET("/budgets/:month", h.getBudget)
	users.PUT("/budgets/:month", h.saveBudget)
}

func (h *HTTPHandler) getProfile(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	profile, err := h.service.GetProfile(c.Request.Context(), userID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, profile)
}

func (h *HTTPHandler) saveProfile(c *gin.Context) {
	var input ProfileInput
	if err := decodeObject(c.Request.Body, &input); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	userID, _ := auth.UserIDFromContext(c)
	profile, err := h.service.SaveProfile(c.Request.Context(), userID, input)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, profile)
}

func (h *HTTPHandler) getBudget(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	budget, err := h.service.GetOrCreateBudget(c.Request.Context(), userID, c.Param("month"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, budget)
}

func (h *HTTPHandler) saveBudget(c *gin.Context) {
	var input BudgetInput
	if err := decodeObject(c.Request.Body, &input); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	userID, _ := auth.UserIDFromContext(c)
	budget, err := h.service.SaveBudget(c.Request.Context(), userID, c.Param("month"), input)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, budget)
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

func writeError(c *gin.Context, err error) {
	var validation *ValidationError
	switch {
	case errors.As(err, &validation):
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "One or more finance fields are invalid.", validation.Fields)
	case errors.Is(err, ErrInvalidMonth):
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The month must use YYYY-MM.", nil)
	case errors.Is(err, ErrNotFound):
		auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The requested resource was not found.", nil)
	case errors.Is(err, ErrProfileRequired):
		auth.WriteAPIError(c, http.StatusConflict, "profile_required", "Save a profile before generating a default budget.", nil)
	case errors.Is(err, ErrInconsistentData):
		auth.WriteAPIError(c, http.StatusInternalServerError, "internal", "The financial data could not be read.", nil)
	default:
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Financial data is temporarily unavailable.", nil)
	}
}
