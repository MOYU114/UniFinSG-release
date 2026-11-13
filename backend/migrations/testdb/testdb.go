// Package testdb manages exclusive, initially empty MySQL integration databases.
// It never creates or drops databases and does not apply application migrations.
package testdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

var databaseName = regexp.MustCompile(`^unifinsg_test_[a-zA-Z0-9_]+$`)

// Open returns an empty test-owned database. Each suite should supply a distinct
// environment variable and database. TEST_MYSQL_DSN remains a fallback for a
// single suite selected with go test -run; never share it between concurrent runs.
//
// A named lock is held on a dedicated connection until cleanup finishes. All
// test users of these schemas must cooperate with this lock and keep the schema
// exclusive for the test's lifetime. Cleanup discovers tables created since the
// empty-schema check and removes them in foreign-key dependency order.
func Open(t testing.TB, envName string) *sql.DB {
	t.Helper()
	dsn := os.Getenv(envName)
	if dsn == "" && envName != "TEST_MYSQL_DSN" {
		dsn = os.Getenv("TEST_MYSQL_DSN")
	}
	if dsn == "" {
		t.Skipf("set %s to an empty exclusive unifinsg_test_* MySQL database", envName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owned, err := acquire(ctx, dsn)
	if err != nil {
		t.Fatalf("open exclusive MySQL test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := owned.cleanup(ctx); err != nil {
			t.Errorf("clean test-owned MySQL tables: %v", err)
		}
		if err := owned.close(); err != nil {
			t.Errorf("close MySQL test database: %v", err)
		}
	})
	return owned.db
}

type database struct {
	db       *sql.DB
	conn     *sql.Conn
	name     string
	lockName string
}

func acquire(ctx context.Context, dsn string) (_ *database, err error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid MySQL test DSN")
	}
	if !databaseName.MatchString(cfg.DBName) {
		return nil, errors.New("test DSN must name an empty exclusive unifinsg_test_* database")
	}
	cfg.ParseTime, cfg.MultiStatements, cfg.Loc = true, false, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 15*time.Second, 15*time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, errors.New("open MySQL test connection failed")
	}
	db.SetMaxOpenConns(10) // Includes the dedicated advisory-lock connection.
	owned := &database{db: db, name: cfg.DBName}
	defer func() {
		if err != nil {
			_ = owned.close()
		}
	}()
	owned.conn, err = db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to MySQL test database: %w", err)
	}
	// Lowercase also serializes equivalent schema spellings on servers whose
	// lower_case_table_names makes names insensitive to case.
	lockDigest := sha256.Sum256([]byte(strings.ToLower(cfg.DBName)))
	lockName := fmt.Sprintf("unifinsg-test:%x", lockDigest[:24])
	var acquired sql.NullInt64
	if err := owned.conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 0)`, lockName).Scan(&acquired); err != nil {
		return nil, fmt.Errorf("lock test database: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return nil, errors.New("test database is already in use")
	}
	owned.lockName = lockName
	if err := owned.checkCurrentDatabase(ctx); err != nil {
		return nil, err
	}
	if err := owned.checkEmpty(ctx); err != nil {
		return nil, err
	}
	return owned, nil
}

func (owned *database) checkEmpty(ctx context.Context) error {
	var objects int
	if err := owned.conn.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM information_schema.tables WHERE BINARY table_schema = BINARY DATABASE()) +
		(SELECT COUNT(*) FROM information_schema.routines WHERE BINARY routine_schema = BINARY DATABASE()) +
		(SELECT COUNT(*) FROM information_schema.events WHERE BINARY event_schema = BINARY DATABASE())`).Scan(&objects); err != nil {
		return fmt.Errorf("inspect empty test database: %w", err)
	}
	if objects != 0 {
		return errors.New("refusing a nonempty MySQL test database")
	}
	return nil
}

func (owned *database) checkCurrentDatabase(ctx context.Context) error {
	var current string
	if err := owned.conn.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&current); err != nil {
		return fmt.Errorf("read current test database: %w", err)
	}
	if current != owned.name {
		return errors.New("test connection selected an unexpected database")
	}
	return nil
}

func (owned *database) cleanup(ctx context.Context) error {
	if err := owned.checkCurrentDatabase(ctx); err != nil {
		return err
	}
	// Verify this physical connection still owns the exclusion lock before
	// discovering and deleting tables. A lost connection must never clear data.
	var holdsLock bool
	if err := owned.conn.QueryRowContext(ctx, `SELECT IS_USED_LOCK(?) = CONNECTION_ID()`, owned.lockName).Scan(&holdsLock); err != nil {
		return fmt.Errorf("verify test database ownership: %w", err)
	}
	if !holdsLock {
		return errors.New("test database ownership was lost; refusing cleanup")
	}
	rows, err := owned.conn.QueryContext(ctx, `SELECT table_name, table_type FROM information_schema.tables
		WHERE BINARY table_schema = BINARY DATABASE() ORDER BY table_name`)
	if err != nil {
		return fmt.Errorf("list test-owned tables: %w", err)
	}
	objects := make(map[string]string)
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read test-owned table: %w", err)
		}
		objects[name] = kind
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("list test-owned tables: %w", err)
	}
	rows, err = owned.conn.QueryContext(ctx, `SELECT DISTINCT table_schema, table_name,
		referenced_table_schema, referenced_table_name FROM information_schema.key_column_usage
		WHERE referenced_table_name IS NOT NULL AND
		(BINARY table_schema = BINARY DATABASE() OR BINARY referenced_table_schema = BINARY DATABASE())`)
	if err != nil {
		return fmt.Errorf("list test table dependencies: %w", err)
	}
	var dependencies [][2]string
	for rows.Next() {
		var childSchema, child, parentSchema, parent string
		if err := rows.Scan(&childSchema, &child, &parentSchema, &parent); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read test table dependency: %w", err)
		}
		if childSchema != owned.name && parentSchema == owned.name {
			_ = rows.Close()
			return errors.New("another database references the test database; refusing cleanup")
		}
		if childSchema == owned.name && parentSchema == owned.name {
			dependencies = append(dependencies, [2]string{child, parent})
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("list test table dependencies: %w", err)
	}
	order, err := cleanupOrder(objects, dependencies)
	if err != nil {
		return err
	}
	for _, name := range order {
		kind := "TABLE"
		if objects[name] == "VIEW" {
			kind = "VIEW"
		}
		if _, err := owned.conn.ExecContext(ctx, "DROP "+kind+" IF EXISTS "+quoteIdentifier(owned.name)+"."+quoteIdentifier(name)); err != nil {
			return fmt.Errorf("drop test-owned object: %w", err)
		}
	}
	return nil
}

func (owned *database) close() error {
	var result error
	if owned.conn != nil {
		if owned.lockName != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := owned.conn.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, owned.lockName)
			cancel()
			result = errors.Join(result, err)
		}
		result = errors.Join(result, owned.conn.Close())
	}
	return errors.Join(result, owned.db.Close())
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func cleanupOrder(objects map[string]string, dependencies [][2]string) ([]string, error) {
	var order, tables []string
	indegree := make(map[string]int)
	parents := make(map[string]map[string]bool)
	for name, kind := range objects {
		switch kind {
		case "VIEW":
			order = append(order, name)
		case "BASE TABLE":
			tables = append(tables, name)
			indegree[name] = 0
		default:
			return nil, errors.New("unknown test schema object type; refusing cleanup")
		}
	}
	sort.Strings(order)
	sort.Strings(tables)
	for _, dependency := range dependencies {
		child, parent := dependency[0], dependency[1]
		if objects[child] != "BASE TABLE" || objects[parent] != "BASE TABLE" {
			return nil, errors.New("unrecognized test table dependency; refusing cleanup")
		}
		if child == parent {
			continue // MySQL can drop a table with a self-referencing FK.
		}
		if parents[child] == nil {
			parents[child] = make(map[string]bool)
		}
		if !parents[child][parent] {
			parents[child][parent] = true
			indegree[parent]++
		}
	}
	for len(indegree) > 0 {
		found := false
		for _, name := range tables {
			value, exists := indegree[name]
			if !exists || value != 0 {
				continue
			}
			order = append(order, name)
			delete(indegree, name)
			for parent := range parents[name] {
				indegree[parent]--
			}
			found = true
		}
		if !found {
			return nil, errors.New("cyclic test table dependencies; refusing cleanup")
		}
	}
	return order, nil
}
