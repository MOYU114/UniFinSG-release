package agentv2

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"unichatgo/internal/auth"
	"unichatgo/internal/blob"
)

// Upload record statuses stored in temp_files.status.
const (
	UploadActive  = "active"
	UploadUsed    = "used"
	UploadExpired = "expired"
)

const (
	maxUploadBytes     = 10 << 20
	maxUploadsPerTurn  = 5
	maxImageNameRunes  = 120
	maxUploadBodyBytes = maxUploadBytes + (1 << 20)
)

// Upload is one temp_files row that holds a private image.
type Upload struct {
	ID        int64
	UserID    int64
	SessionID int64
	FileName  string
	Key       string
	MIME      string
	Size      int64
	Status    string
	// MessageID mirrors temp_files.summary_message_id and records which user
	// message consumed the image.
	MessageID int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (u Upload) expired(now time.Time) bool {
	return u.Status == UploadExpired || !now.Before(u.ExpiresAt)
}

// UploadStore persists upload metadata. Every method is scoped by userID.
type UploadStore interface {
	CreateUpload(ctx context.Context, upload Upload) (*Upload, error)
	GetUpload(ctx context.Context, userID, id int64) (*Upload, error)
	ListUploadsByIDs(ctx context.Context, userID int64, ids []int64) ([]Upload, error)
	// ClaimUploads moves active uploads to used. It returns ErrUploadUnavailable
	// when any id is missing, already used, expired or from another session.
	ClaimUploads(ctx context.Context, userID, sessionID int64, ids []int64, now time.Time) error
	LinkUploads(ctx context.Context, userID int64, ids []int64, messageID int64) error
	ReleaseUploads(ctx context.Context, userID int64, ids []int64) error
	ListExpiredUploads(ctx context.Context, now time.Time) ([]Upload, error)
	MarkUploadsExpired(ctx context.Context, ids []int64) error
	ListSessionUploads(ctx context.Context, userID, sessionID int64) ([]Upload, error)
}

// Upload errors.
var (
	ErrUploadUnavailable    = errors.New("upload unavailable")
	ErrUploadExpired        = errors.New("upload expired")
	ErrUploadInvalidSession = errors.New("upload belongs to another session")
)

const uploadColumns = "id, user_id, session_id, file_name, stored_path, mime_type, size, status, summary_message_id, created_at, expires_at"

// MySQLUploadStore is the production UploadStore.
type MySQLUploadStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewMySQLUploadStore(db *sql.DB) *MySQLUploadStore {
	return &MySQLUploadStore{db: db, now: time.Now}
}

func (s *MySQLUploadStore) CreateUpload(ctx context.Context, upload Upload) (*Upload, error) {
	createdAt := upload.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.now().UTC().Truncate(time.Microsecond)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO temp_files
		(user_id, session_id, file_name, stored_path, mime_type, size, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		upload.UserID, upload.SessionID, upload.FileName, upload.Key, upload.MIME, upload.Size,
		UploadActive, createdAt, upload.ExpiresAt.UTC().Truncate(time.Microsecond))
	if err != nil {
		return nil, fmt.Errorf("create upload: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read upload id: %w", err)
	}
	saved := upload
	saved.ID = id
	saved.Status = UploadActive
	saved.CreatedAt = createdAt
	return &saved, nil
}

func (s *MySQLUploadStore) GetUpload(ctx context.Context, userID, id int64) (*Upload, error) {
	upload, err := scanUpload(s.db.QueryRowContext(ctx,
		`SELECT `+uploadColumns+` FROM temp_files WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	return upload, nil
}

func (s *MySQLUploadStore) ListUploadsByIDs(ctx context.Context, userID int64, ids []int64) ([]Upload, error) {
	if len(ids) == 0 {
		return []Upload{}, nil
	}
	query := `SELECT ` + uploadColumns + ` FROM temp_files WHERE user_id = ? AND id IN (` +
		placeholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list uploads: %w", err)
	}
	defer rows.Close()
	uploads := make([]Upload, 0, len(ids))
	for rows.Next() {
		upload, err := scanUpload(rows)
		if err != nil {
			return nil, fmt.Errorf("scan upload: %w", err)
		}
		uploads = append(uploads, *upload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read uploads: %w", err)
	}
	return uploads, nil
}

func (s *MySQLUploadStore) ClaimUploads(ctx context.Context, userID, sessionID int64,
	ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	query := `UPDATE temp_files SET status = ?
		WHERE user_id = ? AND session_id = ? AND status = ? AND expires_at > ? AND id IN (` +
		placeholders(len(ids)) + `)`
	args := []any{UploadUsed, userID, sessionID, UploadActive, now.UTC().Truncate(time.Microsecond)}
	for _, id := range ids {
		args = append(args, id)
	}
	// A single UPDATE can match only a subset when another turn wins the race.
	// Run it in a transaction so a partial claim is rolled back instead of
	// consuming the valid uploads of a failed request.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin claim uploads: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("claim uploads: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read claimed uploads: %w", err)
	}
	if affected != int64(len(ids)) {
		return ErrUploadUnavailable
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit claim uploads: %w", err)
	}
	return nil
}

func (s *MySQLUploadStore) LinkUploads(ctx context.Context, userID int64, ids []int64, messageID int64) error {
	if len(ids) == 0 {
		return nil
	}
	query := `UPDATE temp_files SET summary_message_id = ?
		WHERE user_id = ? AND status = ? AND id IN (` + placeholders(len(ids)) + `)`
	args := []any{messageID, userID, UploadUsed}
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("link uploads: %w", err)
	}
	return nil
}

// ReleaseUploads restores claimed uploads when the message could not be
// stored. A linked upload is never released.
func (s *MySQLUploadStore) ReleaseUploads(ctx context.Context, userID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	query := `UPDATE temp_files SET status = ?
		WHERE user_id = ? AND status = ? AND summary_message_id IS NULL AND id IN (` +
		placeholders(len(ids)) + `)`
	args := []any{UploadActive, userID, UploadUsed}
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("release uploads: %w", err)
	}
	return nil
}

func (s *MySQLUploadStore) ListExpiredUploads(ctx context.Context, now time.Time) ([]Upload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+uploadColumns+` FROM temp_files
		WHERE status IN (?, ?) AND expires_at <= ? ORDER BY id LIMIT 1000`,
		UploadActive, UploadUsed, now.UTC().Truncate(time.Microsecond))
	if err != nil {
		return nil, fmt.Errorf("list expired uploads: %w", err)
	}
	defer rows.Close()
	uploads := make([]Upload, 0, 16)
	for rows.Next() {
		upload, err := scanUpload(rows)
		if err != nil {
			return nil, fmt.Errorf("scan expired upload: %w", err)
		}
		uploads = append(uploads, *upload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read expired uploads: %w", err)
	}
	return uploads, nil
}

func (s *MySQLUploadStore) MarkUploadsExpired(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	query := `UPDATE temp_files SET status = ? WHERE id IN (` + placeholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+1)
	args = append(args, UploadExpired)
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("expire uploads: %w", err)
	}
	return nil
}

func (s *MySQLUploadStore) ListSessionUploads(ctx context.Context, userID, sessionID int64) ([]Upload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+uploadColumns+`
		FROM temp_files WHERE user_id = ? AND session_id = ?`, userID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session uploads: %w", err)
	}
	defer rows.Close()
	uploads := make([]Upload, 0, 8)
	for rows.Next() {
		upload, err := scanUpload(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session upload: %w", err)
		}
		uploads = append(uploads, *upload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read session uploads: %w", err)
	}
	return uploads, nil
}

func scanUpload(row interface{ Scan(...any) error }) (*Upload, error) {
	var (
		upload    Upload
		messageID sql.NullInt64
	)
	if err := row.Scan(&upload.ID, &upload.UserID, &upload.SessionID, &upload.FileName,
		&upload.Key, &upload.MIME, &upload.Size, &upload.Status, &messageID,
		&upload.CreatedAt, &upload.ExpiresAt); err != nil {
		return nil, err
	}
	if messageID.Valid {
		upload.MessageID = messageID.Int64
	}
	upload.CreatedAt = upload.CreatedAt.UTC()
	upload.ExpiresAt = upload.ExpiresAt.UTC()
	return &upload, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Image type detection uses magic bytes only; the client Content-Type and the
// file extension are never trusted.
const (
	MimePNG  = "image/png"
	MimeJPEG = "image/jpeg"
	MimeWebP = "image/webp"
)

func detectImageType(head []byte) (mime, ext string, ok bool) {
	switch {
	case len(head) >= 8 && head[0] == 0x89 && head[1] == 0x50 && head[2] == 0x4e && head[3] == 0x47 &&
		head[4] == 0x0d && head[5] == 0x0a && head[6] == 0x1a && head[7] == 0x0a:
		return MimePNG, "png", true
	case len(head) >= 3 && head[0] == 0xff && head[1] == 0xd8 && head[2] == 0xff:
		return MimeJPEG, "jpg", true
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return MimeWebP, "webp", true
	default:
		return "", "", false
	}
}

// uploadImage stores one private image and records it in temp_files.
func (h *Handler) uploadImage(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	if h.uploads == nil || h.blobs == nil {
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Uploads are temporarily unavailable.", nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBodyBytes)
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			auth.WriteAPIError(c, http.StatusRequestEntityTooLarge, "upload_too_large",
				"Images must be 10MB or smaller.", nil)
			return
		}
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The upload must be multipart form data.", nil)
		return
	}
	rawSession := strings.TrimSpace(c.Request.FormValue("session_id"))
	sessionID, err := strconv.ParseInt(rawSession, 10, 64)
	if err != nil || sessionID <= 0 {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The session is invalid.",
			map[string]string{"session_id": "must_be_a_session_id"})
		return
	}
	ctx := c.Request.Context()
	if _, err := h.store.GetSession(ctx, userID, sessionID); err != nil {
		h.writeStoreError(c, err)
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "An image file is required.",
			map[string]string{"file": "must_be_an_image"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		auth.WriteAPIError(c, http.StatusBadRequest, "bad_request", "The image could not be read.", nil)
		return
	}
	if len(data) > maxUploadBytes {
		auth.WriteAPIError(c, http.StatusRequestEntityTooLarge, "upload_too_large",
			"Images must be 10MB or smaller.", nil)
		return
	}
	if len(data) == 0 {
		auth.WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The image is empty.",
			map[string]string{"file": "must_not_be_empty"})
		return
	}
	mime, ext, ok := detectImageType(data)
	if !ok {
		auth.WriteAPIError(c, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"Only PNG, JPEG and WebP images are supported.", nil)
		return
	}

	now := h.now().UTC()
	key := fmt.Sprintf("uploads/%d/%s/%s.%s", userID, now.In(singaporeLocation()).Format("20060102"),
		strings.ReplaceAll(uuid.NewString(), "-", ""), ext)
	if err := h.blobs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), mime); err != nil {
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "The image could not be stored.", nil)
		return
	}
	upload, err := h.uploads.CreateUpload(ctx, Upload{
		UserID: userID, SessionID: sessionID, FileName: displayName(header.Filename, ext),
		Key: key, MIME: mime, Size: int64(len(data)), ExpiresAt: now.Add(h.uploadTTL),
	})
	if err != nil {
		// Do not leave an unreferenced object behind.
		_ = h.blobs.Delete(ctx, key)
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "The upload could not be recorded.", nil)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"upload_id": upload.ID, "mime_type": upload.MIME, "size": upload.Size,
		"expires_at": upload.ExpiresAt,
	})
}

// getUpload streams one private image to its owner. It is not a public or
// presigned URL: authentication and ownership are checked on every request.
func (h *Handler) getUpload(c *gin.Context) {
	userID, _ := auth.UserIDFromContext(c)
	if h.uploads == nil || h.blobs == nil {
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Uploads are temporarily unavailable.", nil)
		return
	}
	uploadID, err := strconv.ParseInt(c.Param("upload_id"), 10, 64)
	if err != nil || uploadID <= 0 {
		auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The image was not found.", nil)
		return
	}
	ctx := c.Request.Context()
	upload, err := h.uploads.GetUpload(ctx, userID, uploadID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The image was not found.", nil)
			return
		}
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Uploads are temporarily unavailable.", nil)
		return
	}
	if upload.expired(h.now().UTC()) {
		auth.WriteAPIError(c, http.StatusGone, "upload_expired", "This image has expired.", nil)
		return
	}
	reader, err := h.blobs.Get(ctx, upload.Key)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			auth.WriteAPIError(c, http.StatusNotFound, "not_found", "The image was not found.", nil)
			return
		}
		auth.WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "The image could not be read.", nil)
		return
	}
	defer reader.Close()
	c.Header("Content-Type", upload.MIME)
	c.Header("Content-Length", strconv.FormatInt(upload.Size, 10))
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, reader)
}

func displayName(raw, ext string) string {
	name := strings.TrimSpace(raw)
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	if name == "" || utf8.RuneCountInString(name) > maxImageNameRunes {
		return "image." + ext
	}
	return name
}

// StartUploadCleaner runs the hourly sweep. Expired rows are marked first so
// an expired image always reads as 410, then local files are deleted. S3
// objects are removed by the bucket lifecycle rule.
func (h *Handler) StartUploadCleaner(ctx context.Context, interval time.Duration) {
	if h.uploads == nil {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if count, err := h.CleanupExpiredUploads(ctx); err != nil {
					// Never log object keys or image bytes.
					log.Printf("agentv2: upload cleanup failed: %v", err)
				} else if count > 0 {
					log.Printf("agentv2: expired %d uploads", count)
				}
			}
		}
	}()
}

// CleanupExpiredUploads marks expired uploads and, for the local driver,
// removes their files. It returns the number of rows marked.
func (h *Handler) CleanupExpiredUploads(ctx context.Context) (int, error) {
	if h.uploads == nil {
		return 0, nil
	}
	expired, err := h.uploads.ListExpiredUploads(ctx, h.now().UTC())
	if err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return 0, nil
	}
	ids := make([]int64, 0, len(expired))
	for _, upload := range expired {
		ids = append(ids, upload.ID)
	}
	if err := h.uploads.MarkUploadsExpired(ctx, ids); err != nil {
		return 0, err
	}
	if h.blobs != nil && deletesOnExpire(h.blobs) {
		for _, upload := range expired {
			if err := h.blobs.Delete(ctx, upload.Key); err != nil {
				log.Printf("agentv2: expired upload file could not be removed: %v", err)
			}
		}
	}
	return len(expired), nil
}

func deletesOnExpire(store blob.Store) bool {
	expirer, ok := store.(blob.Expirer)
	return ok && expirer.DeleteOnExpire()
}

// sessionUploads lists the private objects of a conversation. It must run
// before the session row is deleted, because the foreign key cascade removes
// the temp_files rows.
func (h *Handler) sessionUploads(ctx context.Context, userID, sessionID int64) []Upload {
	if h.uploads == nil {
		return nil
	}
	uploads, err := h.uploads.ListSessionUploads(ctx, userID, sessionID)
	if err != nil {
		return nil
	}
	return uploads
}

// deleteUploadObjects removes the private objects of a deleted conversation.
func (h *Handler) deleteUploadObjects(ctx context.Context, uploads []Upload) {
	if h.blobs == nil {
		return
	}
	for _, upload := range uploads {
		_ = h.blobs.Delete(ctx, upload.Key)
	}
}

// validateUploadIDs rejects duplicates and more than five images.
func validateUploadIDs(ids []int64) error {
	if len(ids) > maxUploadsPerTurn {
		return fmt.Errorf("upload_ids must contain at most %d images", maxUploadsPerTurn)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return errors.New("upload_ids must contain positive ids")
		}
		if _, ok := seen[id]; ok {
			return errors.New("upload_ids must not repeat an image")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// loadTurnUploads validates ownership, session, status and expiry of the
// uploads of one turn.
func (h *Handler) loadTurnUploads(ctx context.Context, userID, sessionID int64, ids []int64, now time.Time) ([]Upload, error) {
	uploads, err := h.uploads.ListUploadsByIDs(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	if len(uploads) != len(ids) {
		return nil, ErrNotFound
	}
	byID := make(map[int64]Upload, len(uploads))
	for _, upload := range uploads {
		byID[upload.ID] = upload
	}
	ordered := make([]Upload, 0, len(ids))
	for _, id := range ids {
		upload, ok := byID[id]
		if !ok {
			return nil, ErrNotFound
		}
		if upload.SessionID != sessionID {
			return nil, ErrUploadInvalidSession
		}
		if upload.expired(now) {
			return nil, ErrUploadExpired
		}
		if upload.Status != UploadActive {
			return nil, ErrUploadUnavailable
		}
		ordered = append(ordered, upload)
	}
	return ordered, nil
}

// readUploadBytes loads the private object for the current turn only.
func (h *Handler) readUploadBytes(ctx context.Context, uploads []Upload) ([]turnImage, error) {
	out := make([]turnImage, 0, len(uploads))
	for _, upload := range uploads {
		reader, err := h.blobs.Get(ctx, upload.Key)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxUploadBytes+1))
		reader.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) == 0 || len(data) > maxUploadBytes {
			return nil, blob.ErrNotFound
		}
		out = append(out, turnImage{MIME: upload.MIME, Data: data})
	}
	return out, nil
}

// turnImage is one image of the current turn, sent to the model as base64.
type turnImage struct {
	MIME string
	Data []byte
}
