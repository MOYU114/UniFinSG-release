// Package migrations applies the MySQL-only UniFinSG schema after the legacy
// storage migration has created users and the other existing tables.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.sql
var files embed.FS

const versionTable = "unifinsg_schema_migrations"

// Apply runs pending migrations and leaves db open for the application.
// Cancellation requests a graceful stop between statements; each in-flight
// migration statement is independently limited to 30 seconds.
func Apply(ctx context.Context, db *sql.DB) (err error) {
	m, err := newMigrator(ctx, db)
	if err != nil {
		return err
	}
	defer func() {
		sourceErr, databaseErr := m.Close()
		if closeErr := errors.Join(sourceErr, databaseErr); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close migration resources: %w", closeErr))
		}
	}()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case m.GracefulStop <- true:
			default:
			}
		case <-done:
		}
	}()

	err = m.Up()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply UniFinSG migrations: %w", err)
	}
	return ctx.Err()
}

func newMigrator(ctx context.Context, db *sql.DB) (*migrate.Migrate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("migration database is required")
	}
	source, err := iofs.New(files, ".")
	if err != nil {
		return nil, fmt.Errorf("open migration files: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = source.Close()
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	driver, err := mysql.WithConnection(ctx, conn, &mysql.Config{
		MigrationsTable:  versionTable,
		StatementTimeout: 30 * time.Second,
	})
	if err != nil {
		_ = conn.Close()
		_ = source.Close()
		return nil, fmt.Errorf("initialize MySQL migration driver: %w", err)
	}
	// WithConnection owns only this borrowed connection. WithInstance would
	// close the application's entire pool when the migrator is closed.
	m, err := migrate.NewWithInstance("iofs", source, "mysql", driver)
	if err != nil {
		_ = driver.Close()
		_ = source.Close()
		return nil, fmt.Errorf("initialize migrator: %w", err)
	}
	return m, nil
}
