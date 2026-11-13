package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"unichatgo/internal/auth"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/redis"
)

var monthFormatRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

type Handler struct {
	db       *sql.DB
	cache    *redis.Client
	provider insight.Provider
	streamer Streamer
	store    *store
	limiter  *rateLimiter
	mu       sync.Mutex
	requests map[string]time.Time
}

// NewHandler wires the Agent to a real insight provider. A nil provider is a
// startup configuration error and is never replaced with fake data.
func NewHandler(db *sql.DB, cache *redis.Client, provider insight.Provider, streamer Streamer) (*Handler, error) {
	if provider == nil {
		return nil, errors.New("agent insight provider is required")
	}
	if streamer == nil {
		streamer = newHTTPStreamer()
	}
	return &Handler{db: db, cache: cache, provider: provider, streamer: streamer, store: newStore(db), limiter: newRateLimiter(cache), requests: make(map[string]time.Time)}, nil
}

// RegisterRoutes attaches the retained v1 routes. From 2.0 onward the only
// remaining assistant endpoint is the rule-based advice feed used by the
// dashboard (design section 9, Q-B); ask, history and quota are served by
// agentv2 instead.
func (h *Handler) RegisterRoutes(api *gin.RouterGroup, authService *auth.Service) {
	users := api.Group("/users/:id")
	users.Use(authService.APIMiddleware(), auth.RequirePathUser(), authService.APICSRFMiddleware())
	users.GET("/assistant/advice", h.advice)
}

// quota is retained for the legacy regression suite only.
//
// Deprecated: the v1 quota endpoint is no longer registered.
func (h *Handler) quota(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	c.JSON(http.StatusOK, h.limiter.usage(c.Request.Context(), userID))
}

type askRequest struct {
	Question        string `json:"question"`
	Month           string `json:"month"`
	ClientRequestID string `json:"client_request_id"`
}

// ask is retained for the legacy regression suite only.
//
// Deprecated: POST /assistant/ask is no longer registered; agentv2 serves
// POST /agent/sessions/:sid/messages.
func (h *Handler) ask(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	var req askRequest
	if err := decodeJSON(c, &req); err != nil {
		apiErr(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	if n := utf8.RuneCountInString(req.Question); n < 1 || n > 500 {
		apiErr(c, http.StatusBadRequest, "validation_failed", "The question is invalid.", map[string]string{"question": "must_be_between_1_and_500_characters"})
		return
	}
	if req.Month != "" && !monthFormatRe.MatchString(req.Month) {
		apiErr(c, http.StatusBadRequest, "bad_request", "The month must use YYYY-MM.", nil)
		return
	}
	claimed := false
	if req.ClientRequestID != "" {
		if h.duplicate(c.Request.Context(), userID, req.ClientRequestID) {
			apiErr(c, http.StatusConflict, "conflict", "This request has already been accepted.", nil)
			return
		}
		claimed = true
	}
	allowed, reason, err := h.limiter.allow(c.Request.Context(), userID)
	if err != nil {
		if claimed {
			h.releaseDuplicate(c.Request.Context(), userID, req.ClientRequestID)
		}
		apiErr(c, http.StatusServiceUnavailable, "unavailable", "Assistant is temporarily unavailable.", nil)
		return
	}
	if !allowed {
		if claimed {
			h.releaseDuplicate(c.Request.Context(), userID, req.ClientRequestID)
		}
		c.Header("Retry-After", strconv.Itoa(h.limiter.retryAfter(userID, reason)))
		msg := "Too many assistant requests. Try again later."
		if reason == "quota_exceeded" {
			msg = "The daily assistant quota has been exceeded."
		}
		apiErr(c, http.StatusTooManyRequests, reason, msg, nil)
		return
	}
	query := ParseQuery(req.Question)
	snapshot, err := h.provider.Snapshot(c.Request.Context(), userID, req.Month)
	if err != nil {
		h.limiter.release(c.Request.Context(), userID)
		if claimed {
			h.releaseDuplicate(c.Request.Context(), userID, req.ClientRequestID)
		}
		writeSnapshotError(c, err)
		return
	}
	var aff *Affordability
	if query.Intent == IntentAffordability && query.AmountCents > 0 {
		value, ok := safeAffordability(h.provider, snapshot, query.AmountCents, query.Category)
		if ok {
			aff = &value
		}
	}
	advice := RuleAdviceFor(snapshot, aff)
	missing := append([]string(nil), snapshot.Missing...)
	if missing == nil {
		missing = []string{}
	}
	facts := Facts{Month: snapshot.Month, SnapshotHash: snapshot.Hash, Numbers: FactsNumbers{TotalBudgetCents: snapshot.TotalBudgetCents, TotalSpentCents: snapshot.TotalSpentCents, RemainingCents: snapshot.RemainingCents, ProjectedSpentCents: snapshot.ProjectedSpentCents}, Categories: make([]CategoryFact, 0, len(snapshot.Categories)), UsageBP: snapshot.UsageBP, Affordability: aff, RuleAdvice: advice, Missing: missing}
	for _, stat := range snapshot.Categories {
		facts.Categories = append(facts.Categories, CategoryFact{Category: stat.Category, BudgetCents: stat.BudgetCents, SpentCents: stat.SpentCents, RemainingCents: stat.BudgetCents - stat.SpentCents, UsageBP: stat.UsageBP})
	}
	send, ok := prepareSSE(c)
	if !ok {
		h.limiter.release(c.Request.Context(), userID)
		if claimed {
			h.releaseDuplicate(c.Request.Context(), userID, req.ClientRequestID)
		}
		return
	}
	requestID := "r_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := send("ack", gin.H{"request_id": requestID, "intent": query.Intent}); err != nil {
		return
	}
	if err := send("facts", facts); err != nil {
		return
	}
	answer := fallbackAnswer(advice, snapshot.Missing)
	invalidAmount := query.Intent == IntentAffordability && query.AmountOutOfRange
	if invalidAmount {
		answer = "The requested amount is outside the supported range of S$1,000,000.00. " + Disclaimer
	}
	fallback := len(snapshot.Missing) > 0 || invalidAmount
	fallbackReason := ""
	verified := true
	if fallback {
		if invalidAmount {
			fallbackReason = "amount_out_of_range"
		} else {
			fallbackReason = "insufficient_data"
		}
	} else if h.streamer != nil {
		history, _ := h.store.history(c.Request.Context(), userID, 6)
		var chunks strings.Builder
		streamCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		var firstToken atomic.Bool
		var timedOut atomic.Bool
		timer := time.AfterFunc(10*time.Second, func() {
			if !firstToken.Load() {
				timedOut.Store(true)
				cancel()
			}
		})
		err := h.streamer.Stream(streamCtx, buildPrompt(facts, history, req.Question), func(delta string) error {
			firstToken.Store(true)
			chunks.WriteString(delta)
			return send("stream", gin.H{"delta": delta})
		})
		timer.Stop()
		cancel()
		if err != nil {
			logStoreError("stream llm answer", err)
			fallback = true
			fallbackReason = "llm_error"
			if timedOut.Load() || errors.Is(err, context.DeadlineExceeded) {
				fallbackReason = "llm_timeout"
			}
		} else {
			answer = chunks.String()
			if answer == "" {
				fallback = true
				fallbackReason = "llm_error"
			} else {
				verified = verifyAnswer(answer, facts)
				if !strings.Contains(answer, Disclaimer) {
					answer += " " + Disclaimer
				}
			}
		}
	} else {
		fallback = true
		fallbackReason = "llm_error"
	}
	if fallbackReason == "" && fallback {
		fallbackReason = "llm_error"
	}
	if fallback {
		verified = true
	}
	messageID, saveErr := h.store.saveTurn(c.Request.Context(), userID, req.Question, answer, verified, fallback)
	logStoreError("save assistant messages", saveErr)
	auditErr := h.store.audit(c.Request.Context(), auditRecord{UserID: userID, SnapshotHash: snapshot.Hash, Intent: query.Intent, Verified: verified, Fallback: fallback, CreatedAt: time.Now().UTC()})
	logStoreError("save assistant audit", auditErr)
	done := gin.H{"request_id": requestID, "verified": verified, "fallback": fallback, "fallback_reason": nil, "answer": answer, "disclaimer": Disclaimer, "suggestions": followUps(query, facts), "quota": h.limiter.usage(c.Request.Context(), userID)}
	if fallbackReason != "" {
		done["fallback_reason"] = fallbackReason
	}
	if messageID > 0 {
		done["message_id"] = messageID
	}
	_ = send("done", done)
}

func (h *Handler) advice(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	snapshot, err := h.provider.Snapshot(c.Request.Context(), userID, c.Query("month"))
	if err != nil {
		writeSnapshotError(c, err)
		return
	}
	missing := snapshot.Missing
	if missing == nil {
		missing = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"month": snapshot.Month, "snapshot_hash": snapshot.Hash, "rule_advice": RuleAdviceFor(snapshot, nil), "missing": missing, "disclaimer": Disclaimer})
}

// history is retained for the legacy regression suite only.
//
// Deprecated: GET /assistant/history is no longer registered; history now
// lives in GET /agent/sessions/:sid/messages.
func (h *Handler) history(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	limit := 20
	raw := c.Query("limit")
	if raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 50 {
			apiErr(c, http.StatusBadRequest, "validation_failed", "The history limit is invalid.", map[string]string{"limit": "must_be_between_1_and_50"})
			return
		}
		limit = v
	}
	messages, err := h.store.history(c.Request.Context(), userID, limit)
	if err != nil {
		apiErr(c, http.StatusServiceUnavailable, "unavailable", "Assistant history is temporarily unavailable.", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": messages})
}

func (h *Handler) duplicate(ctx context.Context, userID int64, id string) bool {
	if h.cache != nil && h.cache.Raw() != nil {
		seen, err := h.cache.Raw().SetNX(ctx, fmt.Sprintf("agent:request:%d:%s", userID, id), "1", 5*time.Minute).Result()
		if err == nil {
			return !seen
		}
		h.limiter.warnFallback()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, t := range h.requests {
		if now.Sub(t) > 5*time.Minute {
			delete(h.requests, k)
		}
	}
	key := fmt.Sprintf("%d:%s", userID, id)
	if _, ok := h.requests[key]; ok {
		return true
	}
	h.requests[key] = now
	return false
}

func (h *Handler) releaseDuplicate(ctx context.Context, userID int64, id string) {
	if id == "" {
		return
	}
	if h.cache != nil && h.cache.Raw() != nil {
		if err := h.cache.Raw().Del(ctx, fmt.Sprintf("agent:request:%d:%s", userID, id)).Err(); err == nil {
			return
		}
	}
	h.mu.Lock()
	delete(h.requests, fmt.Sprintf("%d:%s", userID, id))
	h.mu.Unlock()
}

func safeAffordability(provider insight.Provider, snapshot *insight.Snapshot, amount int64, category insight.Category) (result insight.Affordability, ok bool) {
	if amount <= 0 || category == "" || snapshot == nil {
		return result, false
	}
	defer func() {
		if recover() != nil {
			log.Printf("agent: affordability provider rejected input")
			result = insight.Affordability{}
			ok = false
		}
	}()
	return provider.Affordability(snapshot, amount, category), true
}

func writeSnapshotError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, finance.ErrInvalidMonth):
		apiErr(c, http.StatusBadRequest, "bad_request", "The month must use YYYY-MM.", nil)
	case errors.Is(err, finance.ErrNotFound):
		apiErr(c, http.StatusNotFound, "not_found", "The requested financial data was not found.", nil)
	case errors.Is(err, finance.ErrInconsistentData):
		apiErr(c, http.StatusInternalServerError, "internal", "The financial data could not be read.", nil)
	default:
		apiErr(c, http.StatusServiceUnavailable, "unavailable", "Financial data is temporarily unavailable.", nil)
	}
}
func apiErr(c *gin.Context, status int, code, msg string, fields map[string]string) {
	auth.WriteAPIError(c, status, code, msg, fields)
}
func decodeJSON(c *gin.Context, target any) error {
	if c.Request.Body == nil {
		return errors.New("body required")
	}
	return c.ShouldBindJSON(target)
}
func prepareSSE(c *gin.Context) (func(string, any) error, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		apiErr(c, http.StatusInternalServerError, "internal", "Streaming is unavailable.", nil)
		return nil, false
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, payload any) error {
		b, err := jsonMarshal(payload)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	c.Status(http.StatusOK)
	return send, true
}
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
