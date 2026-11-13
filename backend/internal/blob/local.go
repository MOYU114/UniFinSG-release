package blob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultUploadDir is UPLOAD_DIR's default from design section 11.
const DefaultUploadDir = "./data/uploads"

// Local stores objects under one directory. It is used for development and the
// local docker-compose stack.
type Local struct {
	root string
}

// NewLocal creates the upload directory if needed.
func NewLocal(dir string) (*Local, error) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultUploadDir
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("blob: resolve upload dir: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("blob: create upload dir: %w", err)
	}
	return &Local{root: absolute}, nil
}

// DeleteOnExpire reports that the cleaner must remove local files.
func (l *Local) DeleteOnExpire() bool { return true }

func (l *Local) path(key string) (string, error) {
	relative, err := relativeKey(key)
	if err != nil {
		return "", err
	}
	full := filepath.Join(l.root, filepath.FromSlash(relative))
	// ValidateKey already rejects traversal; this is defence in depth in case
	// a platform-specific path separator changes the result.
	if !strings.HasPrefix(full, l.root+string(os.PathSeparator)) {
		return "", ErrInvalidKey
	}
	return full, nil
}

func (l *Local) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	target, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("blob: create object dir: %w", err)
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("blob: create object: %w", err)
	}
	if _, err := io.Copy(file, r); err != nil {
		file.Close()
		_ = os.Remove(target)
		return fmt.Errorf("blob: write object: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("blob: close object: %w", err)
	}
	return nil
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	target, err := l.path(key)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob: open object: %w", err)
	}
	return file, nil
}

func (l *Local) Delete(_ context.Context, key string) error {
	target, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("blob: delete object: %w", err)
	}
	return nil
}
