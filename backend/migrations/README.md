# UniFinSG MySQL migrations

Call `migrations.Apply(ctx, db)` after `storage.Migrate(db, "mysql")` and before
serving application traffic. SQLite and the legacy table creation code remain
unchanged. The migration source is embedded in the Go binary, so the working
directory does not affect migration discovery.

The runner borrows one connection from the application pool, uses MySQL advisory
locking, and tracks versions in `unifinsg_schema_migrations`. It treats an already
current schema as success and refuses to continue when a migration is dirty.
Application startup must fail if `Apply` returns an error; do not automatically
force a version or roll back a failed migration.

Each migration file contains exactly one SQL statement. This lets migrations use
the existing application connection without enabling `multiStatements`. Add new
versions when changing the schema; do not rewrite migrations already applied to
a shared environment.

Cancellation requests a graceful stop between statements. An in-flight migration
statement has a 30-second timeout. MySQL DDL is not transactional across migration
versions, so interrupted work may require inspecting the schema and its recorded
dirty version before an operator repairs it.

## Schema conventions

- All foreign-key IDs use `BIGINT UNSIGNED` to match the legacy `users.id`.
- Amounts use signed `BIGINT` cents, with database constraints for valid ranges.
- Category values are the six fixed categories. `budget_categories` uses
  `budget_cents`; queries must scope the joined `budgets` row by the authenticated
  `user_id`.
- Budget provenance is stored in `budgets.source`. Application writes must set
  `updated_at` when a record changes.
- Account deletion cascades through profiles, budgets, category budgets and
  transactions, assistant messages and assistant audits.
- Future transaction dates are rejected by the service using Asia/Singapore
  time; the database does not use a server-local current-date constraint.

## Integration verification

Use a disposable, empty MySQL 8.0.16+ database whose name starts with
`unifinsg_test_`. Set `TEST_MYSQL_DSN` privately to its connection string, then run
from `backend/`:

```sh
go test ./migrations -run TestMySQLMigrationLifecycle -count=1 -v
```

The test refuses any nonempty database or database without the test prefix.
It creates the legacy schema, applies all migrations twice and concurrently,
checks constraints and user-deletion cascades, verifies dirty-version refusal,
rolls back only the test-created schema, and applies it again. Cleanup drops only
the known tables created by this test and never drops a database.

The test forces `multiStatements=false` and verifies that the application pool
remains usable after migration. Run this package with its own empty test database;
do not share that database with other test processes. Without `TEST_MYSQL_DSN`,
the integration test is explicitly skipped, not counted as MySQL verification.

Down migrations remove finance data and the Cognito mapping column. They are
provided for controlled development/test rollback; the application never invokes
them on startup.

## Migrations

| Version | Change |
| --- | --- |
| 000001 | `users.cognito_sub` |
| 000002–000005 | `user_profiles`, `budgets`, `budget_categories`, `transactions` |
| 000006–000007 | `offers`, `offer_clicks` (retained; no longer used by the app) |
| 000008 | `eval_events` |
| 000009 | Cognito identity columns on `users` |
| 000010–000011 | v1 assistant messages and audits |
| 000012 | `add_expense_by_chat` evaluation task |
| 000013 | `llm_configs` (BYOK) |
| 000014 | `agent_turns` |
| 000015–000016 | agent columns on the existing `messages` and `sessions` tables |
| 000017 | `agent_drafts` |
| 000018 | `transactions.source` |
| 000019 | allow `agent` in `budgets.source` |

Because each file holds one statement, a change that touches two tables uses
two versions (000015 and 000016).

Migration 000012's down file narrows the enum back to the pre-v2 values. It is a
single statement; delete any `add_expense_by_chat` rows manually before running
it, otherwise MySQL rejects the narrowing and the schema is marked dirty.

Migration 009 expands `users.cognito_sub` to `VARBINARY(255)` so opaque,
case-sensitive subject values are matched exactly, including trailing spaces.
Its down migration drops `email` and `cognito_username` but deliberately retains
the compatible subject storage expansion: narrowing to the original length and
collation could truncate or merge existing identities. Migration 001's down
migration still removes the subject column entirely. Reapplying 009 remains safe.
