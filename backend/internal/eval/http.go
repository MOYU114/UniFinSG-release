package eval

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
)

type HTTPHandler struct {
	service   *Service
	resolveIP func(*http.Request) (string, error)
}

type HTTPOption func(*HTTPHandler)

// WithTrustedClientIPResolver must only be supplied by deployment code that
// verifies its proxy boundary. Without this option X-Forwarded-For, X-Real-IP
// and Gin's global proxy settings cannot influence the rate-limit identity.
func WithTrustedClientIPResolver(resolve func(*http.Request) (string, error)) HTTPOption {
	return func(handler *HTTPHandler) {
		if resolve != nil {
			handler.resolveIP = resolve
		}
	}
}

func NewHTTPHandler(service *Service, options ...HTTPOption) *HTTPHandler {
	handler := &HTTPHandler{
		service: service,
		resolveIP: func(request *http.Request) (string, error) {
			host, _, err := net.SplitHostPort(request.RemoteAddr)
			return host, err
		},
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

func (h *HTTPHandler) RegisterRoutes(api *gin.RouterGroup) {
	api.POST("/eval/events", h.record)
	api.GET("/admin/eval/export", h.export)
}

func (h *HTTPHandler) record(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	host, err := h.resolveIP(c.Request)
	if err != nil {
		writeUnavailable(c)
		return
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		writeUnavailable(c)
		return
	}
	allowed, retry, err := h.service.AllowIP(c.Request.Context(), address.Unmap().WithZone(""))
	if err != nil {
		writeUnavailable(c)
		return
	}
	if !allowed {
		c.Header("Retry-After", strconv.Itoa(retry))
		auth.WriteAPIError(c, http.StatusTooManyRequests, "rate_limited", "Too many evaluation events. Try again later.", nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input EventInput
	if err := decodeEvent(c.Request.Body, &input); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	if err := h.service.Record(c.Request.Context(), input); err != nil {
		var validation *ValidationError
		if errors.As(err, &validation) {
			auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "One or more evaluation fields are invalid.", validation.Fields)
		} else {
			writeUnavailable(c)
		}
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

func (h *HTTPHandler) export(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	expected := os.Getenv("ADMIN_TOKEN")
	if expected == "" {
		writeUnavailable(c)
		return
	}
	expectedHash := sha256.Sum256([]byte(expected))
	actualHash := sha256.Sum256([]byte(c.GetHeader("X-Admin-Token")))
	if subtle.ConstantTimeCompare(expectedHash[:], actualHash[:]) != 1 {
		auth.WriteAPIError(c, http.StatusForbidden, "forbidden", "The administrator token is invalid.", nil)
		return
	}
	format, view := c.DefaultQuery("format", "csv"), c.DefaultQuery("view", "summary")
	if format != "csv" && format != "json" || view != "summary" && view != "events" {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The export format or view is invalid.", nil)
		return
	}
	var payload any
	var csvRows [][]string
	var err error
	switch view {
	case "summary":
		var rows []TaskSummary
		rows, err = h.service.Summary(c.Request.Context())
		payload = rows
		if format == "csv" {
			csvRows = summaryCSV(rows)
		}
	case "events":
		var rows []Event
		rows, err = h.service.Events(c.Request.Context())
		payload = rows
		if format == "csv" {
			csvRows = eventsCSV(rows)
		}
	}
	if err != nil {
		writeUnavailable(c)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="unifinsg-eval-`+view+`.`+format+`"`)
	if format == "json" {
		c.JSON(http.StatusOK, payload)
		return
	}
	data, err := encodeCSV(csvRows)
	if err != nil {
		writeUnavailable(c)
		return
	}
	c.Data(http.StatusOK, "text/csv; charset=utf-8", data)
}

func decodeEvent(body io.Reader, input *EventInput) error {
	decoder := json.NewDecoder(body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("an event object is required")
	}
	if err := json.Unmarshal(raw, input); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("only one event object is allowed")
	}
	return nil
}

func writeUnavailable(c *gin.Context) {
	auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Evaluation data is temporarily unavailable.", nil)
}
