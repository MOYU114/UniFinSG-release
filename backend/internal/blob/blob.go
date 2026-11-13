// Package blob stores uploaded images in private storage. It never exposes a
// public or presigned URL: callers read the object through the backend and
// send it to the model as base64 (design section 8.2).
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Store is the storage abstraction from design section 8.2.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, mime string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// Expirer reports whether the driver deletes objects when a temp_files row
// expires. The local driver does; S3 relies on a bucket lifecycle rule.
type Expirer interface {
	DeleteOnExpire() bool
}

// ErrInvalidKey is returned for a key that could escape the storage root.
var ErrInvalidKey = errors.New("blob: invalid key")

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("blob: object not found")

// rootPrefix is the namespace every canonical upload key starts with.
const rootPrefix = "uploads/"

// ValidateKey rejects absolute paths, backslashes, empty segments and any
// traversal segment before a driver turns the key into a filesystem path or an
// object name.
func ValidateKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, `\`) {
		return ErrInvalidKey
	}
	if path.Clean(key) != key {
		return ErrInvalidKey
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrInvalidKey
		}
	}
	return nil
}

// relativeKey strips the canonical "uploads/" namespace. The driver then
// applies its own root: UPLOAD_DIR for local, S3_PREFIX for S3. With the
// defaults both resolve to the stored key, e.g. uploads/12/20261106/<uuid>.jpg.
func relativeKey(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	relative := strings.TrimPrefix(key, rootPrefix)
	if relative == "" || relative == key {
		return "", ErrInvalidKey
	}
	return relative, nil
}

// FromEnv builds the driver selected by STORAGE_DRIVER. A missing or invalid
// configuration fails fast instead of silently falling back.
func FromEnv(ctx context.Context) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("STORAGE_DRIVER"))) {
	case "", "local":
		return NewLocal(os.Getenv("UPLOAD_DIR"))
	case "s3":
		return NewS3FromEnv(ctx)
	default:
		return nil, fmt.Errorf("blob: unsupported STORAGE_DRIVER %q", os.Getenv("STORAGE_DRIVER"))
	}
}
