package agentv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"unichatgo/internal/auth"
	"unichatgo/internal/blob"
	"unichatgo/internal/credits"
	"unichatgo/internal/drafts"
	"unichatgo/internal/finance"
	"unichatgo/internal/llm"
)

// ModelResolver selects the platform or BYOK model for a user.
type ModelResolver interface {
	ResolveForUser(ctx context.Context, userID int64) (llm.ChatModel, string, error)
}

// Credits is the credits service surface used by the turn lifecycle.
type Credits interface {
	Reserve(ctx context.Context, userID int64, mode string, hasImage bool) (*credits.Reservation, error)
	Snapshot(ctx context.Context, userID int64, mode string) (credits.Snapshot, error)
}

// Drafts is the draft service surface used by the decision endpoint and the
// message read path. *drafts.Service implements it.
type Drafts interface {
	Get(ctx context.Context, userID int64, id string) (*drafts.Draft, error)
	Decide(ctx context.Context, in drafts.DecideInput) (*drafts.Decision, error)
	ExpirePending(ctx context.Context, userID int64) (int64, error)
}

const (
	sessionLockTTL   = 120 * time.Second
	idempotencyTTL   = 10 * time.Minute
	maxContentRunes  = 2000
	maxTitleRunes    = 120
	defaultPageLimit = 20
	maxPageLimit     = 50
	maxMessageLimit  = 100
)

// Handler serves /api/users/:id/agent.
type Handler struct {
	store          Store
	runner         *Runner
	models         ModelResolver
	credits        Credits
	drafts         Drafts
	uploads        UploadStore
	blobs          blob.Store
	uploadTTL      time.Duration
	coord          Coordination
	limiter        RateLimiter
	apiMiddleware  gin.HandlerFunc
	csrfMiddleware gin.HandlerFunc
	now            func() time.Time
	newTurnID      func() string
}

// HandlerOption configures optional capabilities such as image uploads.
type HandlerOption func(*Handler)

// WithUploads enables the private image upload endpoints. The blob store and
// the metadata store must both be present.
func WithUploads(store UploadStore, blobs blob.Store, ttl time.Duration) HandlerOption {
	return func(h *Handler) {
		h.uploads = store
		h.blobs = blobs
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		h.uploadTTL = ttl
	}
}

func NewHandler(store Store, runner *Runner, models ModelResolver, creditService Credits,
	draftService Drafts, coord Coordination, limiter RateLimiter, authService *auth.Service,
	options ...HandlerOption) *Handler {
	handler := &Handler{
		store:          store,
		runner:         runner,
		models:         models,
		credits:        creditService,
		drafts:         draftService,
		coord:          coord,
		limiter:        limiter,
		apiMiddleware:  authService.APIMiddleware(),
		csrfMiddleware: authService.APICSRFMiddleware(),
		now:            time.Now,
		newTurnID:      newTurnID,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// newTestHandler lets unit tests inject middleware and fakes.
func newTestHandler(store Store, runner *Runner, models ModelResolver, creditService Credits, draftService Drafts,
	coord Coordination, limiter RateLimiter, apiMiddleware, csrfMiddleware gin.HandlerFunc,
	options ...HandlerOption) *Handler {
	handler := &Handler{
		store: store, runner: runner, models: models, credits: creditService, drafts: draftService,
		coord: coord, limiter: limiter,
		apiMiddleware: apiMiddleware, csrfMiddleware: csrfMiddleware,
		now: time.Now, newTurnID: newTurnID,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

func newTurnID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (h *Handler) RegisterRoutes(api *gin.RouterGroup) {
	users := api.Group("/users/:id")
	users.Use(h.apiMiddleware, auth.RequirePathUser(), h.csrfMiddleware)
	users.POST("/agent/sessions", h.createSession)
	users.GET("/agent/sessions", h.listSessions)
	users.PATCH("/agent/sessions/:sid", h.renameSession)
	users.DELETE("/agent/sessions/:sid", h.deleteSession)
	users.GET("/agent/sessions/:sid/messages", h.listMessages)
	users.POST("/agent/sessions/:sid/messages", h.sendMessage)
	users.POST("/agent/uploads", h.uploadImage)
	users.GET("/agent/uploads/:upload_id", h.getUpload)
	users.POST("/agent/drafts/:draft_id/decision", h.decideDraft)
}

func (h *Handler) createSession(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	var request struct {
		Title string `json:"title"`
	}
	if c.Request.Body != nil {
		if err := decodeJSONBody(c.Request.Body, &request); err != nil {
			auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
			return
		}
	}
	if utf8.RuneCountInString(strings.TrimSpace(request.Title)) > maxTitleRunes {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The session title is invalid.",
			map[string]string{"title": "must_be_at_most_120_characters"})
		return
	}
	session, err := h.store.CreateSession(c.Request.Context(), userID, request.Title)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}
	c.JSON(http.StatusCreated, session)
}

func (h *Handler) listSessions(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	limit, ok := h.pageLimit(c)
	if !ok {
		return
	}
	sessions, next, err := h.store.ListSessions(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions, "next_cursor": next})
}

func (h *Handler) renameSession(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	sessionID, ok := h.sessionID(c)
	if !ok {
		return
	}
	var request struct {
		Title string `json:"title"`
	}
	if err := decodeJSONBody(c.Request.Body, &request); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	title := strings.TrimSpace(request.Title)
	if n := utf8.RuneCountInString(title); n < 1 || n > maxTitleRunes {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The session title is invalid.",
			map[string]string{"title": "must_be_between_1_and_120_characters"})
		return
	}
	session, err := h.store.RenameSession(c.Request.Context(), userID, sessionID, title)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

func (h *Handler) deleteSession(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	sessionID, ok := h.sessionID(c)
	if !ok {
		return
	}
	// Capture the private objects before the cascade removes their rows.
	sessionUploads := h.sessionUploads(c.Request.Context(), userID, sessionID)
	if err := h.store.DeleteSession(c.Request.Context(), userID, sessionID); err != nil {
		h.writeStoreError(c, err)
		return
	}
	h.deleteUploadObjects(c.Request.Context(), sessionUploads)
	c.Status(http.StatusNoContent)
}

func (h *Handler) listMessages(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	sessionID, ok := h.sessionID(c)
	if !ok {
		return
	}
	limit := defaultPageLimit
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxMessageLimit {
			auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The message limit is invalid.",
				map[string]string{"limit": "must_be_between_1_and_100"})
			return
		}
		limit = value
	}
	before := int64(0)
	if raw := c.Query("before"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The message cursor is invalid.",
				map[string]string{"before": "must_be_a_message_id"})
			return
		}
		before = value
	}
	if _, err := h.store.GetSession(c.Request.Context(), userID, sessionID); err != nil {
		h.writeStoreError(c, err)
		return
	}
	// Design section 6.4 step 6: reads lazily expire overdue drafts.
	if h.drafts != nil {
		_, _ = h.drafts.ExpirePending(c.Request.Context(), userID)
	}
	messages, hasMore, err := h.store.ListMessages(c.Request.Context(), userID, sessionID, before, limit)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}
	if h.drafts != nil {
		h.attachDrafts(c.Request.Context(), userID, messages)
	}
	if h.uploads != nil {
		h.attachUploads(c.Request.Context(), userID, messages)
	}
	c.JSON(http.StatusOK, gin.H{"messages": publicMessages(messages), "has_more": hasMore})
}

// Signatures are model replay metadata, not browser-visible message content.
func publicMessages(messages []Message) []Message {
	if messages == nil {
		return nil
	}
	out := append([]Message{}, messages...)
	for i := range out {
		out[i].Parts = append([]Part(nil), messages[i].Parts...)
		for j := range out[i].Parts {
			out[i].Parts[j].ExtraContent = nil
		}
	}
	return out
}

// attachUploads marks image parts whose upload row is gone or expired so the
// history renders "Image expired" without a second request.
func (h *Handler) attachUploads(ctx context.Context, userID int64, messages []Message) {
	ids := make([]int64, 0, 8)
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == PartImage && part.UploadID > 0 {
				ids = append(ids, part.UploadID)
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	uploads, err := h.uploads.ListUploadsByIDs(ctx, userID, ids)
	if err != nil {
		return
	}
	byID := make(map[int64]Upload, len(uploads))
	for _, upload := range uploads {
		byID[upload.ID] = upload
	}
	now := h.now().UTC()
	for i := range messages {
		for j := range messages[i].Parts {
			part := &messages[i].Parts[j]
			if part.Type != PartImage || part.UploadID <= 0 {
				continue
			}
			upload, ok := byID[part.UploadID]
			part.Expired = !ok || upload.expired(now)
		}
	}
}

// attachDrafts fills every draft part with its live status, payload and result
// so a page reload restores the same card.
func (h *Handler) attachDrafts(ctx context.Context, userID int64, messages []Message) {
	for i := range messages {
		for j := range messages[i].Parts {
			part := &messages[i].Parts[j]
			if part.Type != PartDraft || part.DraftID == "" {
				continue
			}
			draft, err := h.drafts.Get(ctx, userID, part.DraftID)
			if err != nil {
				// The row is gone (for example the conversation was cleaned
				// up); render an inert card instead of a confirmable one.
				part.Status = "expired"
				continue
			}
			part.Kind = draft.Kind
			part.Status = draft.Status
			part.Payload = draft.Payload
			part.Before = draft.Before
			part.Warnings = draft.Warnings
			part.Result = draft.Result
			part.RejectReason = draft.RejectReason
			expiresAt := draft.ExpiresAt
			part.ExpiresAt = &expiresAt
			part.DecidedAt = draft.DecidedAt
		}
	}
}

func (h *Handler) sendMessage(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	sessionID, ok := h.sessionID(c)
	if !ok {
		return
	}
	var request struct {
		Content         string  `json:"content"`
		ClientRequestID string  `json:"client_request_id"`
		UploadIDs       []int64 `json:"upload_ids"`
	}
	if err := decodeJSONBody(c.Request.Body, &request); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	content := strings.TrimSpace(request.Content)
	hasImages := len(request.UploadIDs) > 0
	if n := utf8.RuneCountInString(content); n > maxContentRunes {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The message is invalid.",
			map[string]string{"content": "must_be_between_1_and_2000_characters"})
		return
	} else if n < 1 && !hasImages {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The message is invalid.",
			map[string]string{"content": "must_be_between_1_and_2000_characters"})
		return
	}
	if hasImages {
		if err := validateUploadIDs(request.UploadIDs); err != nil {
			auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The images are invalid.",
				map[string]string{"upload_ids": err.Error()})
			return
		}
	}

	ctx := c.Request.Context()
	session, err := h.store.GetSession(ctx, userID, sessionID)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}

	now := h.now().UTC()
	var turnUploads []Upload
	var images []turnImage
	if hasImages {
		if h.uploads == nil || h.blobs == nil {
			auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Uploads are temporarily unavailable.", nil)
			return
		}
		turnUploads, err = h.loadTurnUploads(ctx, userID, sessionID, request.UploadIDs, now)
		if err != nil {
			h.writeUploadError(c, err)
			return
		}
		images, err = h.readUploadBytes(ctx, turnUploads)
		if err != nil {
			auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "The images could not be read.", nil)
			return
		}
	}

	// Resolve the model before taking the Redis lock so a BYOK user can still
	// run when Redis is down (§6.6: platform rejects, BYOK continues).
	model, mode, err := h.models.ResolveForUser(ctx, userID)
	if err != nil {
		if errors.Is(err, llm.ErrNotConfigured) {
			auth.WriteAPIError(c, http.StatusServiceUnavailable, "llm_not_configured", "No model is configured for this account.", nil)
			return
		}
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "The model could not be prepared.", nil)
		return
	}

	// Idempotency: a retried client_request_id must not create a second turn.
	claimedKey := ""
	accepted := false
	if request.ClientRequestID != "" {
		claimedKey = fmt.Sprintf("agent:request:%d:%s", userID, request.ClientRequestID)
		acquired, err := h.coord.Acquire(ctx, claimedKey, idempotencyTTL)
		if err != nil {
			if mode != llm.ModeBYOK {
				h.writeCoordinationError(c)
				return
			}
			claimedKey = ""
		} else if !acquired {
			auth.WriteAPIError(c, http.StatusConflict, "duplicate_request", "This request has already been accepted.", nil)
			return
		}
	}
	defer func() {
		if claimedKey != "" && !accepted {
			_ = h.coord.Release(context.Background(), claimedKey)
		}
	}()

	if h.limiter != nil {
		allowed, retry, err := h.limiter.Allow(ctx, userID)
		if err != nil {
			h.writeCoordinationError(c)
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(retry))
			auth.WriteAPIError(c, http.StatusTooManyRequests, "rate_limited", "Too many messages. Try again in a minute.", nil)
			return
		}
	}

	lockKey := fmt.Sprintf("agent:session:%d:lock", sessionID)
	locked, lockErr := h.coord.Acquire(ctx, lockKey, sessionLockTTL)
	if lockErr != nil {
		if mode != llm.ModeBYOK {
			h.writeCoordinationError(c)
			return
		}
		locked = false
	} else if !locked {
		auth.WriteAPIError(c, http.StatusConflict, "turn_in_progress", "Another turn is already running in this conversation.", nil)
		return
	}
	if locked {
		defer func() { _ = h.coord.Release(context.Background(), lockKey) }()
	}

	reservation, err := h.credits.Reserve(ctx, userID, mode, hasImages)
	if err != nil {
		credits.WriteError(c, err)
		return
	}

	history, _, err := h.store.ListMessages(ctx, userID, sessionID, 0, contextMessageLimit)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}

	// Claim the uploads before storing the message so a second message cannot
	// reuse them. A failed insert releases the claim.
	claimed := false
	if hasImages {
		if err := h.uploads.ClaimUploads(ctx, userID, sessionID, request.UploadIDs, now); err != nil {
			h.writeUploadError(c, err)
			return
		}
		claimed = true
	}

	turnID := h.newTurnID()
	messageParts := make([]Part, 0, len(request.UploadIDs)+1)
	for _, uploadID := range request.UploadIDs {
		messageParts = append(messageParts, Part{Type: PartImage, UploadID: uploadID})
	}
	if content != "" {
		messageParts = append(messageParts, Part{Type: PartText, Text: content})
	}
	userMessage, err := h.store.InsertMessage(ctx, Message{
		UserID: userID, SessionID: sessionID, Role: RoleUser, Content: content,
		Parts: messageParts, TurnID: turnID,
	})
	if err != nil {
		if claimed {
			_ = h.uploads.ReleaseUploads(ctx, userID, request.UploadIDs)
		}
		h.writeStoreError(c, err)
		return
	}
	if claimed {
		// The claim already prevents reuse; the link is for traceability and
		// must not fail the turn.
		_ = h.uploads.LinkUploads(ctx, userID, request.UploadIDs, userMessage.ID)
	}
	turn := Turn{
		ID: turnID, UserID: userID, SessionID: sessionID, Mode: mode,
		Kind: credits.TierChat, Status: TurnRunning, CreatedAt: now,
	}
	if err := h.store.CreateTurn(ctx, turn); err != nil {
		h.writeStoreError(c, err)
		return
	}
	if title := TitleFromContent(content); title != "" && session.Title == DefaultSessionTitle {
		if err := h.store.AutoTitle(ctx, userID, sessionID, title, now); err != nil {
			// A failed rename must not fail the turn.
			_ = err
		}
	}

	emit, ok := prepareSSE(c)
	if !ok {
		return
	}
	accepted = true
	if err := emit("ack", map[string]any{
		"turn_id": turnID, "user_message_id": userMessage.ID, "mode": mode,
		"credits_reserved": reservation.Reserved(),
	}); err != nil {
		return
	}

	llmParts := make([]llm.Part, 0, len(images)+1)
	for _, image := range images {
		llmParts = append(llmParts, llm.Part{
			Type: llm.PartImage, ImageMIME: image.MIME, ImageData: image.Data,
		})
	}
	if content != "" {
		llmParts = append(llmParts, llm.Part{Type: llm.PartText, Text: content})
	}
	result, runErr := h.runner.Run(ctx, RunInput{
		UserID: userID, SessionID: sessionID, TurnID: turnID,
		History: history, Content: content, Parts: llmParts, Model: model, Emit: emit,
	})
	// The client may disconnect mid-stream. Persisting the answer (which may
	// carry a draft's tool_call), settling credits and closing the audit row
	// must still happen, so they use a context that outlives the request.
	ctx = context.WithoutCancel(ctx)

	verified := Verify(result.Answer, successfulToolResults(result.Parts))
	assistantID := int64(0)
	if len(result.Parts) > 0 {
		saved, err := h.store.InsertMessage(ctx, Message{
			UserID: userID, SessionID: sessionID, Role: RoleAssistant,
			Content: result.Answer, Parts: result.Parts, TurnID: turnID, Verified: verified,
		})
		if err == nil {
			assistantID = saved.ID
		}
	}
	_ = h.store.TouchSession(ctx, userID, sessionID, h.now().UTC())

	// Any turn with an image settles the vision tier, whichever branch ran.
	tier := credits.TierFor(hasImages, result.UsedTools)
	charged := 0
	if result.Output {
		if value, err := reservation.Settle(ctx, tier); err == nil {
			charged = value
		}
	}
	var remaining *int
	if mode == llm.ModePlatform {
		if snapshot, err := h.credits.Snapshot(ctx, userID, mode); err == nil {
			remaining = snapshot.Remaining
		}
	}

	status := TurnCompleted
	switch {
	case result.AwaitingApproval:
		status = TurnApproval
	case runErr != nil:
		status = TurnFailed
	}
	finished := h.now().UTC()
	turn.Kind = tier
	turn.CreditsCharged = charged
	turn.ModelCalls = result.ModelCalls
	turn.ErrorCode = result.ErrorCode
	turn.Status = status
	turn.FinishedAt = &finished
	if err := h.store.FinishTurn(ctx, turn); err != nil {
		// The audit row is best-effort; the SSE answer is authoritative.
		_ = err
	}

	if result.ErrorCode == "max_steps" {
		_ = emit("error", map[string]any{
			"code": "max_steps", "message": "The assistant stopped after reaching the step limit.",
		})
	}
	if runErr != nil {
		_ = emit("error", errorEvent(runErr))
	}
	_ = emit("done", map[string]any{
		"status": status, "assistant_message_id": assistantID, "verified": verified,
		"credits_charged": charged, "credits_remaining": remaining,
		"suggestions": SuggestFollowUps(c.Request.Context(), model, content, result.Answer, result.Parts, status),
	})
}

// decideDraft consumes a pending draft and continues the paused ReAct loop.
// Only this branch may write the ledger; the model itself never does.
func (h *Handler) decideDraft(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	draftID := strings.TrimSpace(c.Param("draft_id"))
	var request struct {
		Action        string          `json:"action"`
		EditedPayload json.RawMessage `json:"edited_payload"`
		Reason        string          `json:"reason"`
	}
	if err := decodeJSONBody(c.Request.Body, &request); err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be a JSON object with valid field types.", nil)
		return
	}
	if draftID == "" {
		auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The draft was not found.", nil)
		return
	}
	if h.drafts == nil {
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Drafts are temporarily unavailable.", nil)
		return
	}
	ctx := c.Request.Context()

	// The continuation is free but still counts toward the per-minute limit.
	// It is checked before consuming so a refused request leaves the draft
	// actionable.
	if h.limiter != nil {
		allowed, retry, err := h.limiter.Allow(ctx, userID)
		if err != nil {
			h.writeCoordinationError(c)
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(retry))
			auth.WriteAPIError(c, http.StatusTooManyRequests, "rate_limited", "Too many messages. Try again in a minute.", nil)
			return
		}
	}

	model, mode, err := h.models.ResolveForUser(ctx, userID)
	if err != nil {
		if errors.Is(err, llm.ErrNotConfigured) {
			auth.WriteAPIError(c, http.StatusServiceUnavailable, "llm_not_configured", "No model is configured for this account.", nil)
			return
		}
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "The model could not be prepared.", nil)
		return
	}

	draft, err := h.drafts.Get(ctx, userID, draftID)
	if err != nil {
		h.writeDraftError(c, err)
		return
	}

	lockKey := fmt.Sprintf("agent:session:%d:lock", draft.SessionID)
	locked, lockErr := h.coord.Acquire(ctx, lockKey, sessionLockTTL)
	if lockErr != nil {
		if mode != llm.ModeBYOK {
			h.writeCoordinationError(c)
			return
		}
		locked = false
	} else if !locked {
		auth.WriteAPIError(c, http.StatusConflict, "turn_in_progress", "Another turn is already running in this conversation.", nil)
		return
	}
	if locked {
		defer func() { _ = h.coord.Release(context.Background(), lockKey) }()
	}

	decision, err := h.drafts.Decide(ctx, drafts.DecideInput{
		UserID: userID, DraftID: draftID, Action: request.Action,
		EditedPayload: request.EditedPayload, Reason: request.Reason,
	})
	if err != nil {
		h.writeDraftError(c, err)
		return
	}
	draft = decision.Draft

	resultPart := Part{
		Type: PartToolResult, CallID: draft.ToolCallID, Name: draftToolName(draft.Kind),
		Result: decision.ToolResult,
	}
	// The ledger write is already committed at this point. A failed history
	// append must not turn a successful approval into a 503 that the client
	// would retry (and then see 409); the reload reconciles from the draft row.
	_ = h.store.AppendToolResult(ctx, userID, draft.SessionID, draft.TurnID, draft.ToolCallID, resultPart)
	history, _, err := h.store.ListMessages(ctx, userID, draft.SessionID, 0, contextMessageLimit)
	if err != nil {
		h.writeStoreError(c, err)
		return
	}
	remaining := h.runner.maxSteps
	if turn, err := h.store.GetTurn(ctx, userID, draft.TurnID); err == nil {
		remaining = h.runner.maxSteps - turn.ModelCalls
		if remaining < 0 {
			remaining = 0
		}
	}

	emit, ok := prepareSSE(c)
	if !ok {
		return
	}
	_ = emit("status", map[string]string{"phase": "thinking"})

	result, runErr := h.runner.Resume(ctx, ResumeInput{
		UserID: userID, SessionID: draft.SessionID, TurnID: draft.TurnID,
		History: history, Steps: remaining, Model: model, Emit: emit,
	})
	// See sendMessage: persistence must survive a client disconnect.
	ctx = context.WithoutCancel(ctx)

	verified := Verify(result.Answer, successfulToolResults(result.Parts))
	assistantID := int64(0)
	if len(result.Parts) > 0 {
		saved, err := h.store.InsertMessage(ctx, Message{
			UserID: userID, SessionID: draft.SessionID, Role: RoleAssistant,
			Content: result.Answer, Parts: result.Parts, TurnID: draft.TurnID, Verified: verified,
		})
		if err == nil {
			assistantID = saved.ID
		}
	}
	_ = h.store.TouchSession(ctx, userID, draft.SessionID, h.now().UTC())

	status := TurnCompleted
	switch {
	case result.AwaitingApproval:
		status = TurnApproval
	case runErr != nil:
		status = TurnFailed
	}
	if err := h.store.ContinueTurn(ctx, userID, draft.TurnID, status, result.ModelCalls, result.ErrorCode); err != nil {
		_ = err
	}

	var creditsRemaining *int
	if mode == llm.ModePlatform {
		if snapshot, err := h.credits.Snapshot(ctx, userID, mode); err == nil {
			creditsRemaining = snapshot.Remaining
		}
	}
	if result.ErrorCode == "max_steps" {
		_ = emit("error", map[string]any{
			"code": "max_steps", "message": "The assistant stopped after reaching the step limit.",
		})
	}
	if runErr != nil {
		_ = emit("error", errorEvent(runErr))
	}
	_ = emit("done", map[string]any{
		"status": status, "assistant_message_id": assistantID, "verified": verified,
		"credits_charged": 0, "credits_remaining": creditsRemaining,
		// The approved draft's result counts as a tool this turn used.
		"suggestions": SuggestFollowUps(c.Request.Context(), model, "", result.Answer, append([]Part{resultPart}, result.Parts...), status),
	})
}

func (h *Handler) writeDraftError(c *gin.Context, err error) {
	var validation *finance.ValidationError
	switch {
	case errors.Is(err, drafts.ErrNotFound):
		auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The draft was not found.", nil)
	case errors.Is(err, drafts.ErrNotPending):
		auth.WriteAPIError(c, http.StatusConflict, "draft_not_pending",
			"This draft was already handled or has expired.", nil)
	case errors.Is(err, drafts.ErrInvalidAction):
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request",
			"The action must be approve or reject.", nil)
	case errors.As(err, &validation):
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed",
			"The draft payload is invalid.", validation.Fields)
	default:
		auth.WriteAPIError(c, http.StatusUnprocessableEntity, "draft_failed",
			"The draft could not be applied. It has been marked as failed.", nil)
	}
}

// draftToolName maps a draft kind back to the propose_* tool that created it,
// so the decision result keeps the same tool_call name.
func draftToolName(kind string) string {
	switch kind {
	case drafts.KindCreateTransactions:
		return "propose_transactions"
	case drafts.KindUpdateTransaction:
		return "propose_update_transaction"
	case drafts.KindDeleteTransaction:
		return "propose_delete_transaction"
	case drafts.KindUpdateBudget:
		return "propose_update_budget"
	default:
		return "propose_draft"
	}
}

func successfulToolResults(parts []Part) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		if part.Type == PartToolResult && !part.IsError && len(part.Result) > 0 {
			out = append(out, part.Result)
		}
	}
	return out
}

func errorEvent(err error) map[string]any {
	var upstream *llm.UpstreamError
	switch {
	case errors.As(err, &upstream):
		return map[string]any{
			"code":     "llm_upstream_error",
			"message":  "The model provider returned an error.",
			"upstream": map[string]any{"status": upstream.Status, "message": upstream.Message},
		}
	case errors.Is(err, context.DeadlineExceeded):
		return map[string]any{"code": "turn_timeout", "message": "The turn took too long and was stopped."}
	default:
		return map[string]any{"code": "agent_failed", "message": "The assistant could not finish this turn."}
	}
}

func (h *Handler) sessionID(c *gin.Context) (int64, bool) {
	value, err := strconv.ParseInt(c.Param("sid"), 10, 64)
	if err != nil || value <= 0 {
		auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The conversation was not found.", nil)
		return 0, false
	}
	return value, true
}

func (h *Handler) pageLimit(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return defaultPageLimit, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maxPageLimit {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The page limit is invalid.",
			map[string]string{"limit": "must_be_between_1_and_50"})
		return 0, false
	}
	return value, true
}

func (h *Handler) writeStoreError(c *gin.Context, err error) {
	if errors.Is(err, ErrInvalidCursor) {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The pagination cursor is invalid.",
			map[string]string{"cursor": "must_be_a_valid_cursor"})
		return
	}
	if errors.Is(err, ErrNotFound) {
		auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The conversation was not found.", nil)
		return
	}
	auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Conversations are temporarily unavailable.", nil)
}

func (h *Handler) writeCoordinationError(c *gin.Context) {
	auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "The agent is temporarily unavailable.", nil)
}

// writeUploadError maps the upload validation failures of a turn.
func (h *Handler) writeUploadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The image was not found.", nil)
	case errors.Is(err, ErrUploadExpired):
		auth.WriteAPIError(c, http.StatusGone, "upload_expired", "This image has expired. Upload it again.", nil)
	case errors.Is(err, ErrUploadInvalidSession):
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The images are invalid.",
			map[string]string{"upload_ids": "must_belong_to_this_conversation"})
	case errors.Is(err, ErrUploadUnavailable):
		auth.WriteAPIError(c, http.StatusConflict, "upload_used",
			"This image was already sent with another message.", nil)
	default:
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Uploads are temporarily unavailable.", nil)
	}
}

func prepareSSE(c *gin.Context) (Emitter, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		auth.WriteAPIError(c, http.StatusInternalServerError, "internal", "Streaming is unavailable.", nil)
		return nil, false
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	return func(event string, payload any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}, true
}

func decodeJSONBody(body io.Reader, target any) error {
	if body == nil {
		return errors.New("body required")
	}
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("a JSON object is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("only one JSON object is allowed")
	}
	return nil
}
