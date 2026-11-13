package finance

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
	"unichatgo/internal/insight"
)

func (h *HTTPHandler) RegisterTransactionRoutes(api *gin.RouterGroup) {
	users := api.Group("/users/:id")
	users.Use(h.auth.APIMiddleware(), auth.RequirePathUser(), h.auth.APICSRFMiddleware())
	users.GET("/transactions", h.listTransactions)
	users.POST("/transactions", h.createTransaction)
	users.PATCH("/transactions/:tid", h.updateTransaction)
	users.DELETE("/transactions/:tid", h.deleteTransaction)
}

func (h *HTTPHandler) listTransactions(c *gin.Context) {
	query := c.Request.URL.Query()
	options := TransactionListOptions{Month: query.Get("month"), Category: insight.Category(query.Get("category"))}
	if _, exists := query["month"]; exists && options.Month == "" {
		writeError(c, ErrInvalidMonth)
		return
	}
	if _, exists := query["category"]; exists && options.Category == "" {
		writeError(c, &ValidationError{Fields: map[string]string{"category": "must_be_a_supported_category"}})
		return
	}
	for _, parameter := range []struct {
		name   string
		target *int
	}{
		{"page", &options.Page}, {"page_size", &options.PageSize},
	} {
		values, exists := query[parameter.name]
		if !exists {
			continue
		}
		if len(values) != 1 || values[0] == "" || strings.Trim(values[0], "0123456789") != "" {
			auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "Pagination parameters must be positive integers.", nil)
			return
		}
		value, err := strconv.Atoi(values[0])
		if err != nil || value < 1 {
			auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "Pagination parameters must be positive integers.", nil)
			return
		}
		*parameter.target = value
	}
	userID, _ := auth.UserIDFromContext(c)
	page, err := h.service.ListTransactions(c.Request.Context(), userID, options)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (h *HTTPHandler) createTransaction(c *gin.Context) {
	var input TransactionInput
	if err := decodeObject(c.Request.Body, &input); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	userID, _ := auth.UserIDFromContext(c)
	record, err := h.service.CreateTransaction(c.Request.Context(), userID, input)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, record)
}

func (h *HTTPHandler) updateTransaction(c *gin.Context) {
	id, ok := transactionIDFromPath(c)
	if !ok {
		return
	}
	var patch TransactionPatch
	if err := decodeObject(c.Request.Body, &patch); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	userID, _ := auth.UserIDFromContext(c)
	record, err := h.service.UpdateTransaction(c.Request.Context(), userID, id, patch)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

func (h *HTTPHandler) deleteTransaction(c *gin.Context) {
	id, ok := transactionIDFromPath(c)
	if !ok {
		return
	}
	userID, _ := auth.UserIDFromContext(c)
	if err := h.service.DeleteTransaction(c.Request.Context(), userID, id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func transactionIDFromPath(c *gin.Context) (int64, bool) {
	raw := c.Param("tid")
	if raw == "" || strings.Trim(raw, "0123456789") != "" {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The transaction ID must be a positive integer.", nil)
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The transaction ID must be a positive integer.", nil)
		return 0, false
	}
	return id, true
}
