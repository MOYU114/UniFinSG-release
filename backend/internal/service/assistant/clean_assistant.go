package assistant

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultTempFileTTL             = 24 * time.Hour
	DefaultTempFileCleanupInterval = time.Hour
)

func (s *Service) StartTempFileCleaner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultTempFileCleanupInterval
	}
	go s.cleanupLoop(ctx, interval)
}

func (s *Service) cleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.cleanupExpiredFiles(); err != nil {
				log.Printf("cleanup temp files error: %v", err)
			}
		}
	}
}

func (s *Service) cleanupExpiredFiles() error {
	rows, err := s.db.Query(`
		SELECT id, stored_path FROM temp_files
		WHERE status = 'active' AND expires_at <= ?`, time.Now().UTC())
	if err != nil {
		return err
	}
	defer rows.Close()

	type fileRow struct {
		id   int64
		path string
	}
	var files []fileRow
	for rows.Next() {
		var fr fileRow
		if err := rows.Scan(&fr.id, &fr.path); err != nil {
			return err
		}
		// v2 uploads store a blob key ("uploads/<user>/<date>/<uuid>.<ext>")
		// in stored_path and are removed by agentv2's upload cleaner, which
		// goes through the configured blob driver (local or S3). Skipping them
		// here avoids deleting the row while orphaning the object.
		if isBlobKey(fr.path) {
			continue
		}
		files = append(files, fr)
	}

	for _, f := range files {
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			log.Printf("remove temp file %s failed: %v", f.path, err)
			continue
		}
		if err := s.deleteTempFileRecord(f.id); err != nil {
			log.Printf("delete temp file record %d failed: %v", f.id, err)
		}

		// prune empty directories
		_ = os.Remove(filepath.Dir(f.path))
	}
	return nil
}

func (s *Service) deleteTempFileRecord(id int64) error {
	_, err := s.db.Exec(`DELETE FROM temp_files WHERE id = ?`, id)
	return err
}

// isBlobKey reports whether stored_path is a v2 blob key rather than a
// filesystem path written by the legacy upload handler.
func isBlobKey(storedPath string) bool {
	return strings.HasPrefix(storedPath, "uploads/")
}
