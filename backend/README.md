# UniFinSG backend

Go API server for UniFinSG: authentication, budgets and transactions, the
conversation agent with human-approved drafts, daily credits, BYOK model
configuration, private image uploads, and an MCP stdio server that exposes the
same tools. It uses Gin, MySQL 8 and Redis 7.

The HTTP and agent API is specified in [`docs/api/contract.md`](../docs/api/contract.md).

## Layout

```
backend/
├── main.go, ops_startup.go   # entry point, config loading, route wiring
├── cmd/
│   ├── mcp-stdio/            # MCP server over stdin/stdout (local debugging)
│   ├── mock-llm/             # OpenAI-compatible mock model for demos and tests
│   ├── seed/                 # create local demo / load-test accounts
│   ├── cloud-init/           # one-off ECS task that creates the app's MySQL account
│   └── migrate-attachments/  # one-off migration of legacy chat attachments
├── internal/
│   ├── agentv2/              # conversation API: sessions, turns, SSE streaming
│   ├── tools/                # tool registry shared by the agent and MCP server
│   ├── drafts/               # agent-proposed writes, executed only after approval
│   ├── llm/                  # provider-neutral model interface (OpenAI-compatible, Anthropic)
│   ├── llmconfig/            # per-user BYOK configuration (encrypted keys)
│   ├── credits/              # daily platform-model allowance
│   ├── blob/                 # private upload storage (local disk or S3)
│   ├── finance/              # profiles, budgets, transactions, dashboard
│   ├── insight/              # budget snapshot contract shared by finance and the agent
│   ├── agent/                # rule-based advice and v1 assistant endpoints
│   ├── auth/                 # local login and AWS Cognito, sessions, CSRF
│   ├── mcpserver/            # MCP adapter over the tool registry
│   ├── eval/                 # anonymous task-event recording and export
│   ├── crypto/               # AES-256-GCM envelope for stored secrets
│   └── api, service, worker, execution, storage, models, redis, config
│                             # retained chat foundation from UnichatGo
└── migrations/               # embedded MySQL migrations (see migrations/README.md)
```

## Requirements

- Go 1.24+ with a C compiler (CGO is used by the SQLite driver)
- MySQL 8.0.16+ and Redis 7

## Configuration

The server reads a JSON file named by `UNICHATGO_CONFIG` (default
`./config.json`, git-ignored). Start from
[`config.json.example`](config.json.example). Environment variables override
individual fields, so the usual setup is the example file plus the variables in
the repository's [`.env.example`](../.env.example).

| Variable | Purpose |
| --- | --- |
| `UNICHATGO_DB` | `mysql` (required for UniFinSG features) or `sqlite3` |
| `MYSQL_*`, `REDIS_*`, `REDIS_TLS` | Database and cache connection |
| `AUTH_MODE` | `local` (username/password) or `cognito` |
| `GIN_MODE` | `debug` for local HTTP; `release` for HTTPS (cookies are marked `Secure`) |
| `UNICHATGO_APIKEY_KEY` | Base64-encoded 32-byte key that encrypts BYOK keys. Keep it stable across restarts |
| `LLM_KIND`, `LLM_BASE_URL`, `LLM_MODEL`, `LLM_API_KEY` | Platform model |
| `ASSISTANT_RPM`, `CREDITS_*`, `AGENT_MAX_STEPS`, `AGENT_DRAFT_TTL_MINUTES` | Rate limit, credits and agent limits |
| `STORAGE_DRIVER`, `UPLOAD_DIR`, `UPLOAD_TTL_HOURS`, `S3_*` | Upload storage |
| `COGNITO_*`, `FRONTEND_URL` | Cognito hosted sign-in (only with `AUTH_MODE=cognito`) |
| `TRUSTED_PROXY_CIDRS` | Proxies whose forwarding headers are trusted for client IPs |
| `ADMIN_TOKEN` | Enables the evaluation export endpoint |

See the root [README](../README.md#48-key-environment-variables) for defaults.

## Run

```bash
# from the repository root: start MySQL and Redis
docker compose -f docker-compose.ops.yml up -d mysql redis
set -a; source .env; set +a

cd backend
go run .                       # listens on SERVER_ADDRESS, default :8090
```

`GET /healthz` reports liveness and `GET /readyz` checks MySQL and Redis.

On startup the server creates the base tables and applies the embedded
migrations in `migrations/`. A dirty or failed migration stops startup.

### Mock model

`cmd/mock-llm` is an OpenAI-compatible server that emits `tool_calls` and the
fixed `submit_extraction` response, so chat and screenshot flows work without a
real provider:

```bash
go run ./cmd/mock-llm          # :8091
# LLM_BASE_URL=http://127.0.0.1:8091 LLM_MODEL=mock
```

### Demo and load-test accounts

```bash
go run ./cmd/seed -config ./config.json -count 1 -prefix demo -output /tmp/demo-accounts.csv
go run ./cmd/seed -config ./config.json -count 100 -prefix loadtest -demo=false -output /tmp/loadtest-accounts.csv
```

Each account gets a profile and a budget; with `-demo` (the default) the first
account also gets deterministic sample expenses. `-month YYYY-MM` picks the
month (never in the future). The CSV holds `user_id,username,password`, is
written with mode `0600`, and existing usernames or output files are refused.
Keep it out of version control.

## Testing

```bash
go test -p 1 ./...
go vet ./...
```

Unit tests need no services. Integration suites run against real MySQL and
Redis and are skipped unless their variables are set. Each MySQL suite needs its
own **empty** schema whose name starts with `unifinsg_test_`; the helpers refuse
anything else and clean up only the objects they create.

| Variable | Suite |
| --- | --- |
| `TEST_MYSQL_MIGRATIONS_DSN` | migration lifecycle (`./migrations`) |
| `TEST_MYSQL_TESTDB_DSN` | test-database helper |
| `TEST_MYSQL_FINANCE_DSN`, `TEST_MYSQL_TRANSACTIONS_DSN`, `TEST_MYSQL_SNAPSHOT_DSN` | finance |
| `TEST_MYSQL_REGRESSION_DSN` | personal-route regression across new and retained routes |
| `TEST_MYSQL_ME_DSN`, `TEST_MYSQL_COGNITO_DSN` | identity and account deletion |
| `TEST_MYSQL_EVAL_DSN` | evaluation events |
| `TEST_MYSQL_AGENTV2_DSN`, `TEST_MYSQL_DRAFTS_DSN` | agent conversations and drafts |
| `TEST_REDIS_ADDR`, `TEST_REDIS_DB` | Redis-backed auth tests (use an isolated DB such as `15`; it is flushed) |
| `TEST_REDIS_EVAL_ADDR` | rate limiting for evaluation events |

`TEST_MYSQL_DSN` is a fallback for running a single suite; do not set it when
running several packages at once. Never point any of these at application data.
[`.github/workflows/ops-ci.yml`](../.github/workflows/ops-ci.yml) shows a full
setup.

## MCP over stdio

`cmd/mcp-stdio` serves the agent's seven tools over the Model Context Protocol
on stdin/stdout. It is a local debugging entry point: there is no HTTP listener
and no authentication, and the process acts as exactly one user chosen with
`--user-id`. It needs MySQL and the same configuration as the API server
(`--config` behaves like `UNICHATGO_CONFIG`).

```bash
go run ./cmd/mcp-stdio --user-id 1
```

Only MCP frames go to stdout; diagnostics go to stderr.

With MCP Inspector:

```bash
go build -o /tmp/mcp-stdio ./cmd/mcp-stdio
npx @modelcontextprotocol/inspector /tmp/mcp-stdio --user-id 1
```

With Claude Code:

```bash
claude mcp add unifinsg -- /tmp/mcp-stdio --user-id 1
```

Or in a project `.mcp.json`:

```json
{
  "mcpServers": {
    "unifinsg": {
      "command": "go",
      "args": ["run", "./cmd/mcp-stdio", "--user-id", "1"],
      "env": { "UNICHATGO_CONFIG": "/absolute/path/to/backend/config.json" }
    }
  }
}
```

Limitations:

- Read tools (`list_categories`, `get_budget_snapshot`, `list_transactions`)
  behave exactly as in the app. `propose_*` tools only create a pending draft.
- An MCP client **cannot confirm a draft**. Drafts are attached to a per-user
  `MCP drafts` conversation; open it in `/chat` to confirm or reject them.
- There are no tokens or per-client identities, and no HTTP `/mcp` endpoint.

## Account deletion and Cognito

`DELETE /api/users/:id` with `{"confirm":"DELETE"}` removes the user's personal
records, sessions, conversations and drafts. In Cognito mode it also calls
`AdminDeleteUser`, so the task role needs that permission on the user pool; a
provider failure is logged and local deletion still succeeds.

For Cognito sign-in the app client must use the authorization code flow with
`openid email` scopes and a client secret held only by the backend. Register the
exact callback (`COGNITO_REDIRECT_URI`) and sign-out (`FRONTEND_URL`) URLs, and
run with HTTPS and `GIN_MODE=release` outside localhost.
