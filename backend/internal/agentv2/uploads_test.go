package agentv2

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/blob"
	"unichatgo/internal/llm"
)

// memoryUploadStore is an in-memory UploadStore for handler tests.
type memoryUploadStore struct {
	mu     sync.Mutex
	nextID int64
	rows   map[int64]*Upload
}

func newMemoryUploadStore() *memoryUploadStore {
	return &memoryUploadStore{rows: make(map[int64]*Upload)}
}

func (s *memoryUploadStore) CreateUpload(_ context.Context, upload Upload) (*Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	upload.ID = s.nextID
	if upload.CreatedAt.IsZero() {
		upload.CreatedAt = time.Now().UTC()
	}
	upload.Status = UploadActive
	saved := upload
	s.rows[saved.ID] = &saved
	return &saved, nil
}

func (s *memoryUploadStore) GetUpload(_ context.Context, userID, id int64) (*Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok || row.UserID != userID {
		return nil, ErrNotFound
	}
	copy := *row
	return &copy, nil
}

func (s *memoryUploadStore) ListUploadsByIDs(_ context.Context, userID int64, ids []int64) ([]Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Upload, 0, len(ids))
	for _, id := range ids {
		row, ok := s.rows[id]
		if !ok || row.UserID != userID {
			continue
		}
		out = append(out, *row)
	}
	return out, nil
}

func (s *memoryUploadStore) ClaimUploads(_ context.Context, userID, sessionID int64,
	ids []int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		row, ok := s.rows[id]
		if !ok || row.UserID != userID || row.SessionID != sessionID ||
			row.Status != UploadActive || !now.Before(row.ExpiresAt) {
			return ErrUploadUnavailable
		}
	}
	for _, id := range ids {
		s.rows[id].Status = UploadUsed
	}
	return nil
}

func (s *memoryUploadStore) LinkUploads(_ context.Context, userID int64, ids []int64, messageID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if row, ok := s.rows[id]; ok && row.UserID == userID {
			row.MessageID = messageID
		}
	}
	return nil
}

func (s *memoryUploadStore) ReleaseUploads(_ context.Context, userID int64, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if row, ok := s.rows[id]; ok && row.UserID == userID && row.MessageID == 0 {
			row.Status = UploadActive
		}
	}
	return nil
}

func (s *memoryUploadStore) ListExpiredUploads(_ context.Context, now time.Time) ([]Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Upload, 0, 4)
	for _, row := range s.rows {
		if (row.Status == UploadActive || row.Status == UploadUsed) && !now.Before(row.ExpiresAt) {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (s *memoryUploadStore) MarkUploadsExpired(_ context.Context, ids []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if row, ok := s.rows[id]; ok {
			row.Status = UploadExpired
		}
	}
	return nil
}

func (s *memoryUploadStore) ListSessionUploads(_ context.Context, userID, sessionID int64) ([]Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Upload, 0, 4)
	for _, row := range s.rows {
		if row.UserID == userID && row.SessionID == sessionID {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (s *memoryUploadStore) expire(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok {
		row.ExpiresAt = time.Now().Add(-time.Minute).UTC()
	}
}

func (s *memoryUploadStore) status(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok {
		return row.Status
	}
	return ""
}

type uploadFixture struct {
	store     *memoryStore
	uploads   *memoryUploadStore
	blobs     *blob.Local
	router    *gin.Engine
	sessionID int64
	credits   *fakeCreditsStore
	model     *scriptedModel
	userID    int64
}

func newUploadFixture(t *testing.T, userID int64, model *scriptedModel, creator DraftCreator) *uploadFixture {
	t.Helper()
	if model == nil {
		model = &scriptedModel{steps: []modelStep{{text: "I could not read the image."}, {text: "I could not read the image."}}}
	}
	store := newMemoryStore()
	session, err := store.CreateSession(context.Background(), userID, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	uploads := newMemoryUploadStore()
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new local blob: %v", err)
	}
	creditService, creditsStore := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 12345}), 6, time.Second)
	if creator != nil {
		runner = runner.WithDrafts(creator)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := newTestHandler(store, runner,
		&fakeResolver{model: model, mode: llm.ModePlatform}, creditService, nil, newFakeCoord(), nil,
		func(c *gin.Context) { c.Set("auth_user_id", userID); c.Next() },
		func(c *gin.Context) { c.Next() },
		WithUploads(uploads, blobs, time.Hour),
	)
	handler.RegisterRoutes(router.Group("/api"))
	return &uploadFixture{
		store: store, uploads: uploads, blobs: blobs, router: router,
		sessionID: session.ID, credits: creditsStore, model: model, userID: userID,
	}
}

func (f *uploadFixture) postMultipart(t *testing.T, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("session_id", strconv.FormatInt(f.sessionID, 10)); err != nil {
		t.Fatalf("write session field: %v", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/users/"+itoa(f.userID)+"/agent/uploads", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func (f *uploadFixture) upload(t *testing.T, filename string, data []byte) int64 {
	t.Helper()
	response := f.postMultipart(t, filename, data)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		UploadID  int64  `json:"upload_id"`
		MIMEType  string `json:"mime_type"`
		Size      int64  `json:"size"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode upload: %v", err)
	}
	if payload.UploadID <= 0 || payload.MIMEType == "" || payload.Size != int64(len(data)) || payload.ExpiresAt == "" {
		t.Fatalf("incomplete upload response: %+v", payload)
	}
	return payload.UploadID
}

func (f *uploadFixture) send(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		"/api/users/"+itoa(f.userID)+"/agent/sessions/"+itoa(f.sessionID)+"/messages",
		bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

var (
	pngBytes  = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("payload")...)
	jpegBytes = append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("payload")...)
	webpBytes = append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("payload")...)
)

func TestUploadDetectsTypeFromMagicBytes(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	for _, test := range []struct {
		name     string
		filename string
		data     []byte
		mime     string
	}{
		{"png with a lying extension", "photo.jpg", pngBytes, MimePNG},
		{"jpeg", "photo", jpegBytes, MimeJPEG},
		{"webp", "photo.png", webpBytes, MimeWebP},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.postMultipart(t, test.filename, test.data)
			if response.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			if !bytes.Contains(response.Body.Bytes(), []byte(test.mime)) {
				t.Fatalf("mime %s missing from %s", test.mime, response.Body.String())
			}
		})
	}
}

func TestUploadRejectsNonImageContent(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	for _, test := range []struct {
		name     string
		filename string
		data     []byte
	}{
		{"text pretending to be png", "receipt.png", []byte("this is not an image")},
		{"gif", "receipt.gif", []byte("GIF89a....")},
		{"empty", "receipt.png", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.postMultipart(t, test.filename, test.data)
			if response.Code != http.StatusUnsupportedMediaType && response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestUploadEnforcesTenMegabyteLimit(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	oversized := make([]byte, maxUploadBytes+1)
	copy(oversized, pngBytes)
	response := fixture.postMultipart(t, "huge.png", oversized)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("upload_too_large")) {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
}

func TestUploadRequiresOwnedSession(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	other, err := fixture.store.CreateSession(context.Background(), 2, "private")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	original := fixture.sessionID
	fixture.sessionID = other.ID
	response := fixture.postMultipart(t, "receipt.png", pngBytes)
	fixture.sessionID = original
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestUploadReadAndExpiry(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)

	request := httptest.NewRequest(http.MethodGet,
		"/api/users/1/agent/uploads/"+itoa(id), nil)
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("read status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != MimePNG {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache control = %q", response.Header().Get("Cache-Control"))
	}
	if !bytes.Equal(response.Body.Bytes(), pngBytes) {
		t.Fatal("the stored bytes changed")
	}

	fixture.uploads.expire(id)
	request = httptest.NewRequest(http.MethodGet, "/api/users/1/agent/uploads/"+itoa(id), nil)
	response = httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	if response.Code != http.StatusGone {
		t.Fatalf("expired read status = %d, want 410: %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("upload_expired")) {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
}

func TestUploadHistoryMarksExpiredImages(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)
	response := fixture.send(t, `{"content":"what is this?","upload_ids":[`+itoa(id)+`]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("send status = %d: %s", response.Code, response.Body.String())
	}
	fixture.uploads.expire(id)

	request := httptest.NewRequest(http.MethodGet,
		"/api/users/1/agent/sessions/"+itoa(fixture.sessionID)+"/messages", nil)
	listing := httptest.NewRecorder()
	fixture.router.ServeHTTP(listing, request)
	if listing.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listing.Code, listing.Body.String())
	}
	var payload struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(listing.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	found := false
	for _, message := range payload.Messages {
		for _, part := range message.Parts {
			if part.Type == PartImage {
				found = true
				if !part.Expired {
					t.Fatalf("expired image part not marked: %+v", part)
				}
			}
		}
	}
	if !found {
		t.Fatal("the image part was not persisted")
	}
}

func TestSendMessageRejectsTooManyUploads(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	ids := make([]string, 0, maxUploadsPerTurn+1)
	for i := 0; i < maxUploadsPerTurn+1; i++ {
		ids = append(ids, itoa(fixture.upload(t, "receipt.png", pngBytes)))
	}
	body := `{"content":"many","upload_ids":[` + strings.Join(ids, ",") + `]}`
	response := fixture.send(t, body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("validation_failed")) {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
}

func TestSendMessageRejectsReusedUpload(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)
	first := fixture.send(t, `{"content":"first","upload_ids":[`+itoa(id)+`]}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}
	if fixture.uploads.status(id) != UploadUsed {
		t.Fatalf("upload status = %q, want used", fixture.uploads.status(id))
	}
	second := fixture.send(t, `{"content":"second","upload_ids":[`+itoa(id)+`]}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409: %s", second.Code, second.Body.String())
	}
	if !bytes.Contains(second.Body.Bytes(), []byte("upload_used")) {
		t.Fatalf("unexpected body: %s", second.Body.String())
	}
}

func TestSendMessageRejectsExpiredUpload(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)
	fixture.uploads.expire(id)
	response := fixture.send(t, `{"content":"late","upload_ids":[`+itoa(id)+`]}`)
	if response.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410: %s", response.Code, response.Body.String())
	}
}

func TestSendMessageRejectsUploadFromAnotherSession(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)
	other, err := fixture.store.CreateSession(context.Background(), 1, "other")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/sessions/"+itoa(other.ID)+"/messages",
		bytes.NewReader([]byte(`{"content":"wrong","upload_ids":[`+itoa(id)+`]}`)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
	}
}

func TestSendMessageWithImageChargesVisionOnce(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)
	response := fixture.send(t, `{"content":"","upload_ids":[`+itoa(id)+`]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	events := parseSSEEvents(t, response.Body.String())
	done := firstEvent(t, events, "done")
	if done.Data["credits_charged"].(float64) != 5 {
		t.Fatalf("credits_charged = %v, want 5", done.Data["credits_charged"])
	}
	if used := fixture.credits.used[1]; used != 5 {
		t.Fatalf("credits used = %d, want 5 (settled once)", used)
	}
	ack := firstEvent(t, events, "ack")
	if ack.Data["credits_reserved"].(float64) != 5 {
		t.Fatalf("credits_reserved = %v, want 5", ack.Data["credits_reserved"])
	}
}

func TestDeleteSessionRemovesUploadObjects(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)
	upload, err := fixture.uploads.GetUpload(context.Background(), 1, id)
	if err != nil {
		t.Fatalf("get upload: %v", err)
	}
	request := httptest.NewRequest(http.MethodDelete,
		"/api/users/1/agent/sessions/"+itoa(fixture.sessionID), nil)
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", response.Code, response.Body.String())
	}
	if _, err := fixture.blobs.Get(context.Background(), upload.Key); err == nil {
		t.Fatal("the private object survived the session deletion")
	}
}

func TestUploadCleanupMarksExpiredAndDeletesLocalFile(t *testing.T) {
	fixture := newUploadFixture(t, 1, nil, nil)
	id := fixture.upload(t, "receipt.png", pngBytes)
	upload, err := fixture.uploads.GetUpload(context.Background(), 1, id)
	if err != nil {
		t.Fatalf("get upload: %v", err)
	}
	reader, err := fixture.blobs.Get(context.Background(), upload.Key)
	if err != nil {
		t.Fatalf("blob get: %v", err)
	}
	reader.Close()
	fixture.uploads.expire(id)

	handler := &Handler{uploads: fixture.uploads, blobs: fixture.blobs, now: time.Now}
	count, err := handler.CleanupExpiredUploads(context.Background())
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if count != 1 {
		t.Fatalf("cleanup count = %d", count)
	}
	if fixture.uploads.status(id) != UploadExpired {
		t.Fatalf("status = %q, want expired", fixture.uploads.status(id))
	}
	if _, err := fixture.blobs.Get(context.Background(), upload.Key); err == nil {
		t.Fatal("the local file was not removed")
	}
}
