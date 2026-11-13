package blob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalPutGetDelete(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	key := "uploads/7/20261106/abc.jpg"
	payload := []byte("image-bytes")
	if err := store.Put(ctx, key, strings.NewReader(string(payload)), int64(len(payload)), "image/jpeg"); err != nil {
		t.Fatalf("put: %v", err)
	}
	// The canonical "uploads/" namespace is replaced by UPLOAD_DIR, so the
	// object lives directly under the configured directory.
	onDisk := filepath.Join(root, "uploads", "7", "20261106", "abc.jpg")
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("object was not written under UPLOAD_DIR: %v", err)
	}
	reader, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(reader)
	reader.Close()
	if string(got) != string(payload) {
		t.Fatalf("read %q", got)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	// Deleting a missing object is idempotent.
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestLocalRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(filepath.Join(root, "uploads"))
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	ctx := context.Background()
	for _, key := range []string{
		"../etc/passwd",
		"uploads/../../etc/passwd",
		"uploads/7/../../../etc/passwd",
		"/uploads/7/a.jpg",
		`uploads\7\a.jpg`,
		"uploads//7/a.jpg",
		"uploads/7/./a.jpg",
		"uploads",
	} {
		if err := store.Put(ctx, key, strings.NewReader("x"), 1, "image/jpeg"); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("put %q: expected ErrInvalidKey, got %v", key, err)
		}
		if _, err := store.Get(ctx, key); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("get %q: expected ErrInvalidKey, got %v", key, err)
		}
	}
	// Nothing escaped the upload directory.
	if _, err := os.Stat(filepath.Join(root, "etc", "passwd")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside the root")
	}
}

func TestLocalDeleteOnExpire(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	var expirer Expirer = store
	if !expirer.DeleteOnExpire() {
		t.Fatal("local driver must delete expired objects")
	}
}

func TestFromEnvRejectsUnknownDriver(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", "ftp")
	if _, err := FromEnv(context.Background()); err == nil {
		t.Fatal("expected an unsupported driver error")
	}
}

func TestFromEnvLocalDefault(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", "")
	t.Setenv("UPLOAD_DIR", t.TempDir())
	store, err := FromEnv(context.Background())
	if err != nil {
		t.Fatalf("from env: %v", err)
	}
	if _, ok := store.(*Local); !ok {
		t.Fatalf("driver = %T, want *Local", store)
	}
}
