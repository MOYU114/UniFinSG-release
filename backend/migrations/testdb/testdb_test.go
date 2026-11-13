package testdb

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCleanupOrder(t *testing.T) {
	tests := []struct {
		name         string
		objects      map[string]string
		dependencies [][2]string
		want         []string
		fail         bool
	}{
		{
			name: "views and newly added foreign keys",
			objects: map[string]string{
				"users": "BASE TABLE", "offers": "BASE TABLE", "offer_clicks": "BASE TABLE",
				"budgets": "BASE TABLE", "budget_categories": "BASE TABLE", "dashboard": "VIEW",
			},
			dependencies: [][2]string{
				{"offer_clicks", "offers"}, {"offer_clicks", "users"},
				{"budgets", "users"}, {"budget_categories", "budgets"},
				{"budget_categories", "budgets"},
			},
			want: []string{"dashboard", "budget_categories", "budgets", "offer_clicks", "offers", "users"},
		},
		{
			name: "self references can be dropped", objects: map[string]string{"tree": "BASE TABLE"},
			dependencies: [][2]string{{"tree", "tree"}}, want: []string{"tree"},
		},
		{
			name: "cycles refuse all cleanup", objects: map[string]string{"a": "BASE TABLE", "b": "BASE TABLE"},
			dependencies: [][2]string{{"a", "b"}, {"b", "a"}}, fail: true,
		},
		{
			name: "missing parent refuses cleanup", objects: map[string]string{"a": "BASE TABLE"},
			dependencies: [][2]string{{"a", "unrecognized"}}, fail: true,
		},
		{name: "unknown object refuses cleanup", objects: map[string]string{"a": "UNKNOWN"}, fail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := cleanupOrder(test.objects, test.dependencies)
			if test.fail {
				if err == nil || got != nil {
					t.Fatalf("unsafe cleanup order: %v %v", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	if got := quoteIdentifier("odd`table"); got != "`odd``table`" {
		t.Fatalf("identifier was not safely quoted: %s", got)
	}
}

func TestAcquireRejectsNonTestDatabaseBeforeConnecting(t *testing.T) {
	for _, dsn := range []string{
		"not-a-dsn", "root@tcp(127.0.0.1:1)/production",
		"root@tcp(127.0.0.1:1)/unifinsg_test_", "root@tcp(127.0.0.1:1)/unifinsg_test_x.other",
	} {
		if owned, err := acquire(context.Background(), dsn); err == nil || owned != nil {
			t.Fatalf("accepted unsafe database target: %v", err)
		}
	}
}

func TestMySQLTestDatabaseOwnershipAndCleanup(t *testing.T) {
	// No shared fallback here: this helper's own destructive lifecycle must be
	// selected explicitly and run separately from other suites.
	dsn := os.Getenv("TEST_MYSQL_TESTDB_DSN")
	if dsn == "" {
		t.Skip("set TEST_MYSQL_TESTDB_DSN to an empty exclusive unifinsg_test_* database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	owned, err := acquire(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := owned.cleanup(ctx); err != nil {
			t.Error(err)
		}
		if err := owned.close(); err != nil {
			t.Error(err)
		}
	}()
	if competing, err := acquire(ctx, dsn); err == nil || competing != nil || !strings.Contains(err.Error(), "already in use") {
		if competing != nil {
			_ = competing.close()
		}
		t.Fatalf("another suite acquired the same test schema: %v", err)
	}
	for _, statement := range []string{
		"CREATE TABLE `parent``table` (id BIGINT PRIMARY KEY)",
		"CREATE TABLE child_table (id BIGINT PRIMARY KEY, parent_id BIGINT, FOREIGN KEY(parent_id) REFERENCES `parent``table`(id))",
		"CREATE VIEW child_view AS SELECT id FROM child_table",
		"INSERT INTO `parent``table` VALUES (1)",
		"INSERT INTO child_table VALUES (2, 1)",
	} {
		if _, err := owned.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := owned.checkEmpty(ctx); err == nil || !strings.Contains(err.Error(), "nonempty") {
		t.Fatalf("did not refuse a nonempty schema: %v", err)
	}
	var preserved int
	if err := owned.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM child_table").Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("nonempty check modified existing data: %d %v", preserved, err)
	}
	if err := owned.cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owned.checkEmpty(ctx); err != nil {
		t.Fatalf("dynamic cleanup left objects behind: %v", err)
	}
}
