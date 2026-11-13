<div align="center">

# UniFinSG

**A conversation-first bookkeeping agent for NUS students**

Chat bookkeeping · Draft confirmation · Monthly budgets · Screenshot receipts · Spending dashboard · Bring your own model

![License: MIT](https://img.shields.io/badge/license-MIT-green) ![Backend: Go](https://img.shields.io/badge/backend-Go%201.24-00ADD8) ![Frontend: Vue 3](https://img.shields.io/badge/frontend-Vue%203-42b883) ![Status: 2.0 preview](https://img.shields.io/badge/status-2.0%20preview-orange)

</div>

---

## Contents

1. [What is UniFinSG?](#1-what-is-unifinsg)
2. [Who is it for?](#2-who-is-it-for)
3. [Features](#3-features)
4. [Quick Start: Run Locally](#4-quick-start-run-locally)
5. [User Guide](#5-user-guide)
6. [Models, Credits and BYOK](#6-models-credits-and-byok)
7. [Local MCP Debugging](#7-local-mcp-debugging)
8. [Data and Privacy](#8-data-and-privacy)
9. [FAQ](#9-faq)
10. [Tech Stack and Project Layout](#10-tech-stack-and-project-layout)
11. [Testing](#11-testing)
12. [Documentation](#12-documentation)
13. [Contributing](#13-contributing)
14. [License and Acknowledgements](#14-license-and-acknowledgements)

---

## 1. What is UniFinSG?

UniFinSG helps National University of Singapore (NUS) students keep track of monthly living expenses. In 2.0 the main entry point is a conversation, not a form:

- **Say it:** type “record lunch 6.5” or attach a receipt screenshot.
- **Check it:** the agent replies with a draft card showing the date, amount and category.
- **Confirm it:** nothing is written to your ledger until you press **Confirm**. You can edit the rows first, or reject the draft with a reason.
- **Understand it:** the dashboard, budget and transaction pages stay in sync, and agent-written expenses carry an **AI** marker.

The agent answers only from your own ledger. Before replying it reads your numbers through tools, and after replying it checks the amounts in the text against the tool results.

> The interface is in English, designed mobile-first for portrait phone screens, and works equally well in a desktop browser.

## 2. Who is it for?

| You are… | UniFinSG helps you |
| --- | --- |
| A new student who just arrived in Singapore and doesn't know local prices yet | Generate a sensible category budget in one tap with "Suggested split" and settle into a spending rhythm |
| A student living on an allowance, scholarship or part-time income | Record spending in a sentence or a screenshot, and see what is left before a category goes over |
| Someone who already pays for a model subscription | Bring your own API key and keep using your own model instead of the shared daily allowance |
| A developer interested in agent design | See how a tool-calling agent with human approval, private image storage and a Go MCP adapter is built (see [Tech Stack](#10-tech-stack-and-project-layout)) |

## 3. Features

### 💬 Chat-first bookkeeping

- The home page is `/chat`. Each conversation keeps its own message history; you can create, rename and delete conversations from the left panel.
- Ask questions in plain English: “How much is left for food?”, “Can I afford S$80 headphones?”, “What did I spend last week?”.
- The agent can read categories, budgets and transactions, and can propose new expenses, edits, deletions and budget changes.
- **Every write needs confirmation.** Proposed writes appear as an editable card. The card expires after 30 minutes and can only be used once, even if you double-click.
- Tool activity is shown as a compact line (for example “Checked the 2026-11 budget snapshot”); the full result stays on the server.

### 📸 Screenshot bookkeeping

- Attach a PNG, JPEG or WebP image (up to 10MB, up to 5 per message), or paste/drag it into the composer.
- The agent extracts one or more records, checks the amounts and dates, flags possible duplicates, and shows them as a draft.
- Images are stored privately and expire after 24 hours by default; after that the conversation shows “Image expired”.
- PDFs and bank statements are **not** supported in this release.

### 🧭 Onboarding and monthly budget

- One page, about 2 minutes: enter your **monthly disposable amount**, your **housing** and your **total budget for this month**.
- Six spending categories: 🍜 Food, 🚌 Transport, 🏠 Housing / Tuition, 🛍️ Daily Shopping, 💻 Study / Digital, 🎧 Entertainment / Other.
- Two one-tap allocation options: **Split evenly** and **Suggested split** (Food 40%, Entertainment / Other 20%, Daily Shopping 15%, Study / Digital 15%, Transport 10%, Housing / Tuition 0%).
- Each new month's budget is generated from your settings. You can also change the budget by asking the agent; the change is applied only after you confirm the draft.

### 🧾 Transactions

- Browse by month and filter by category; edit or delete any entry.
- Expenses written by the agent show a small **AI** badge; manual entries do not.
- The dashboard updates as soon as a draft is confirmed, an entry is edited or an entry is deleted.

### 📊 Dashboard

- **Spent this month / Remaining / Budget used**, plus a month-end projection once you have expenses on at least 3 days.
- Category progress bars: yellow at ≥ 80%, red “Over budget” above 100%.
- Spending-by-category donut chart, recent expenses and rule-based advice from `GET /assistant/advice` (no model call, no credits).

### 🔑 Model settings and daily credits

- By default the shared platform model gives you **100 credits per day** (Singapore time, resets at midnight, no roll-over):
  - 1 credit for a plain chat turn,
  - 2 credits when the turn uses a tool,
  - 5 credits when the turn includes an image.
- Confirming or rejecting a draft and the follow-up turn are free.
- Or switch to **your own API** (OpenAI-compatible or Anthropic): no credits are consumed, but a per-minute rate limit still applies.

### ⚙️ Settings

- Update your monthly profile and current month's budget.
- Choose the platform model or your own model; test the connection before saving.
- Sign out, or delete your account (type `DELETE` to confirm; personal data is permanently removed).

## 4. Quick Start: Run Locally

> UniFinSG needs MySQL and Redis. SQLite mode starts but does not register the conversation, credits, BYOK or upload routes.

### 4.1 Requirements

| Dependency | Version |
| --- | --- |
| Git | Any recent version |
| Docker Engine + Docker Compose | For MySQL and Redis |
| Go | 1.24+ (with a C compiler) |
| Node.js | 22 |
| Python | 3.x (only to generate local secrets) |

### 4.2 Get the code

```bash
git clone https://github.com/MOYU114/UniFinSG.git
cd UniFinSG
```

### 4.3 Generate local configuration

```bash
python3 deploy/init-local.py
```

This creates `.env` from `.env.example`, fills in random database and Redis passwords and a 32-byte encryption key, and prints nothing. `.env` is git-ignored; never commit it. The script refuses to overwrite an existing `.env`.

### 4.4 Start the backend with Docker Compose

```bash
docker compose -f docker-compose.ops.yml up --build -d
curl http://localhost:8090/healthz   # liveness
curl http://localhost:8090/readyz    # database and Redis readiness
```

Migrations run automatically on startup. MySQL (3306) and Redis (6379) bind to `127.0.0.1` only.

<details>
<summary>Prefer not to build the backend image? Run the Go server directly</summary>

Start only MySQL and Redis in Docker, then run the backend with Go:

```bash
docker compose -f docker-compose.ops.yml up -d mysql redis
set -a; source .env; set +a      # export the variables in .env
cd backend && go run .
```

> Note: `MYSQL_PARAMS` in `.env` contains `&`. Quote its value before sourcing, for example `MYSQL_PARAMS="charset=utf8mb4&parseTime=true&loc=UTC"`.

</details>

### 4.5 Configure a model

The platform model is any OpenAI-compatible `/v1/chat/completions` endpoint or an Anthropic endpoint. Set in `.env` and restart the backend:

```bash
LLM_KIND=openai_compatible                # or anthropic
LLM_MODEL=<model name>                    # must support tool calling
LLM_API_KEY=<your API key>
LLM_BASE_URL=https://api.openai.com/v1    # or your gateway / Anthropic base URL
```

Keep the key in `.env` locally and in Secrets Manager in the cloud; it is never sent to the browser.

Without a key, use the bundled mock model for demos and tests. It can emit `tool_calls` and the fixed `submit_extraction` response, so the chat and screenshot flows work without a real provider:

```bash
cd backend && go run ./cmd/mock-llm     # listens on :8091 by default
# then set LLM_BASE_URL=http://127.0.0.1:8091 and any LLM_MODEL value
```

If no platform model and no BYOK configuration exist, the API returns `503 llm_not_configured`; the dashboard and ledger pages still work.

### 4.6 Start the frontend

```bash
cd frontend
npm ci
npm run dev
```

Open <http://localhost:5173>. The dev server proxies `/api` to `http://localhost:8090`. Local sign-in uses `AUTH_MODE=local` and requires `GIN_MODE=debug`.

### 4.7 Demo accounts

```bash
cd backend
cp config.json.example config.json          # fill in the same database settings as .env
go run ./cmd/seed -config ./config.json -prefix demo -count 1 -output ./demo-accounts.csv
```

The command reads the JSON file passed with `-config` (not `.env`), creates local accounts named `<prefix>_0001`, `<prefix>_0002`, …, and writes each generated password to the CSV. The first account gets deterministic sample expenses. The CSV is written with owner-only permissions; keep it out of version control.

### 4.8 Key environment variables

| Variable | Description | Default / example |
| --- | --- | --- |
| `AUTH_MODE` | `local` (username and password) or `cognito` | `local` |
| `VITE_AUTH_MODE` | Must match `AUTH_MODE` | `local` |
| `GIN_MODE` | `debug` for local HTTP; `release` is required for HTTPS in production | `debug` |
| `MYSQL_*` / `REDIS_*` | Database and cache connection | see `.env.example` |
| `LLM_KIND` | `openai_compatible` or `anthropic` | `openai_compatible` |
| `LLM_MODEL` / `LLM_API_KEY` / `LLM_BASE_URL` | Platform model | — |
| `LLM_ALLOW_INSECURE_BYOK` | Allow `http` and loopback for BYOK (development only) | `false` |
| `UNICHATGO_APIKEY_KEY` | 32-byte key used to encrypt BYOK keys (required in 2.0) | — |
| `ASSISTANT_RPM` | Turns per user per minute, platform and BYOK | `5` |
| `CREDITS_DAILY_GRANT` | Daily platform credits | `100` |
| `CREDITS_COST_CHAT` / `_TOOL` / `_VISION` | Cost per turn tier | `1` / `2` / `5` |
| `AGENT_MAX_STEPS` | Maximum model calls per turn | `6` |
| `AGENT_DRAFT_TTL_MINUTES` | Draft lifetime | `30` |
| `STORAGE_DRIVER` | `local` or `s3` | `local` |
| `UPLOAD_DIR` | Directory for the local driver | `./data/uploads` |
| `UPLOAD_TTL_HOURS` | Upload metadata lifetime; must be ≤24 with S3 | `24` |
| `S3_BUCKET` / `S3_REGION` / `S3_PREFIX` | Private bucket settings; required when `STORAGE_DRIVER=s3` | — / — / `uploads/` |

`ASSISTANT_DAILY_LIMIT` was removed in 2.0; daily usage is now credit-based.

The full list, including Cognito variables, is in section 7 of [`docs/api/contract.md`](docs/api/contract.md). Cloud deployment is described in [`deploy/README.md`](deploy/README.md).

### 4.9 Stop

```bash
docker compose -f docker-compose.ops.yml down   # without -v, data is kept
```

## 5. User Guide

### Step 1: Register and sign in

On the “Welcome to UniFinSG” page, click **Create one** (username 3–32 characters, password at least 8 characters), then sign in. If the deployment uses Cognito, the page shows a single **Sign in** button instead.

### Step 2: Set your monthly budget

On first sign-in you land on “Let's shape a budget”: enter your monthly disposable amount, choose your housing, set this month's total budget, then use **Suggested split** or **Split evenly** (or adjust each category by hand) and save.

### Step 3: Record an expense by chatting

1. On `/chat`, type something like `record lunch 6.5` and send it.
2. The agent calls `propose_transactions` and shows a draft card with date, amount and category.
3. Adjust any row if needed, then press **Confirm**. The card becomes “Recorded” and links to the transaction list.
4. Open **Transactions**: the entry is there with an **AI** badge.

You can also ask the agent to change or delete an existing expense or to change a category budget; the same confirmation step applies.

### Step 4: Record from a receipt

1. Attach a receipt screenshot in the composer and send it (optionally with text such as “this was yesterday's lunch”).
2. The agent extracts the amount, date, merchant and category, flags possible duplicates, and shows the draft.
3. Confirm the rows you want. The dashboard numbers change immediately.

### Step 5: Check your budget

Open **Dashboard**: spent, remaining and percentage used, a month-end projection after 3 days of data, category bars (yellow at 80%, red when over), the donut chart and recent expenses. Use the month arrows to look back.

### Step 6: Manage your expenses

Open **Transactions** to filter by month or category, edit an entry, or delete it (deletion asks for confirmation). Agent-written entries keep their AI badge after editing.

### Step 7: Use your own model

Open **Settings → Model settings**, choose “Use my own API”, pick the API type, enter the base URL, model and key, press **Test connection**, then save. The top bar changes from the credit badge to “Using your own model”, and turns no longer consume credits.

## 6. Models, Credits and BYOK

- **Platform model:** shared key configured by the operator. Each turn is charged to a daily allowance of 100 credits in Singapore time; the badge in the top bar shows the remaining amount and the cost of each tier.
- **Precheck and settle:** a turn is accepted only if the remaining credits cover its highest possible tier (2 for text, 5 with an image); it is charged at the actual tier when it finishes. A turn that fails before producing output is not charged.
- **Drafts are free:** confirming or rejecting a draft and the follow-up answer do not consume credits, but they do count toward the per-minute limit.
- **BYOK:** one configuration per user (type, base URL, model, key). The key is encrypted with AES-GCM and never returned; saving runs a connectivity test first. BYOK turns are not charged. Only `https` base URLs are allowed unless `LLM_ALLOW_INSECURE_BYOK=true` for local development.
- **Model requirements:** the model must support tool calling; screenshots additionally need image input. UniFinSG does not probe capabilities — if the model cannot do it, the upstream error is shown and you can change the model in Settings.

## 7. Local MCP Debugging

The same tool registry that the built-in agent uses is also exposed over the Model Context Protocol on stdio. This is a local debugging entry point only: there is no HTTP endpoint, no authentication and no external access in this release.

```bash
cd backend
UNICHATGO_CONFIG=config.json.example go run ./cmd/mcp-stdio --user-id 1
```

Point any stdio-capable MCP client (for example MCP Inspector or Claude Code) at that command. The process acts as exactly one user, chosen by `--user-id`, and reads the local MySQL database.

Available tools: `list_categories`, `get_budget_snapshot`, `list_transactions`, `propose_transactions`, `propose_update_transaction`, `propose_delete_transaction`, `propose_update_budget`.

`propose_*` only creates a draft. **An external MCP client cannot confirm a draft**; open UniFinSG in the browser and confirm the card there. The seven tools and their `readOnly` / `destructive` hints are listed in section 4.11 of [`docs/api/contract.md`](docs/api/contract.md).

## 8. Data and Privacy

- **Only what budgeting needs:** budgets, expense amounts, categories and dates, plus any merchant names, notes and receipt images you choose to add.
- **What is sent to the model:** your message text (with emails, links, NRIC/FIN, card and phone numbers removed), the tool results needed to answer, and — for the current turn only — the images you attached. Your username and account email are not sent; older images in the conversation are replaced by a placeholder.
- **Images are private:** they are stored in a private local directory or private S3 bucket, read only through an authenticated endpoint, and expire after 24 hours by default. No public or presigned URLs are generated.
- **BYOK keys:** encrypted at rest with AES-256-GCM and never returned by the API or written to logs.
- **Delete any time:** deleting your account in Settings permanently removes your personal data, conversations and drafts. Upload records are removed with them; the stored objects are no longer reachable and, on S3, expire within a day through the bucket lifecycle rule. In local development, clear the `UPLOAD_DIR` directory when you want the files gone immediately.
- **Secure sign-in:** HttpOnly cookies with CSRF protection; the cloud deployment uses AWS Cognito email registration and verification.

## 9. FAQ

**Does UniFinSG connect to my bank account or import statements automatically?**
No. You record expenses by chatting, by sending a screenshot, or on the Transactions page. UniFinSG never reads a bank or payment account. PDF statements are out of scope.

**What currency and time zone are used?**
All amounts are in Singapore dollars (S$). Dates, “today” and the daily credit reset follow Singapore time (Asia/Singapore).

**Why did nothing get recorded after the agent replied?**
Writes are drafts until you confirm them. Look for the card in the conversation and press **Confirm**; drafts expire after 30 minutes.

**Why can't the agent see my screenshot?**
The selected model must accept images. If it does not, the upstream error is shown in the conversation; switch to a vision-capable model in Settings.

**I ran out of credits. What happens now?**
The platform allowance resets at midnight Singapore time. You can wait, or switch to your own API in Settings, which does not use credits.

**Can I use any model with BYOK?**
It must be OpenAI-compatible or Anthropic-style and support tool calling. Models without tool support will fail when the agent tries to read your ledger.

**Do draft confirmations cost credits?**
No. The confirmation turn and its follow-up are free, though they count toward the per-minute rate limit.

**I'm told I'm sending messages too often.**
Each user can send 5 turns per minute by default (`ASSISTANT_RPM`). Wait for the indicated time and retry.

**When running locally, I get signed out right after signing in.**
Local HTTP requires `GIN_MODE=debug`. In `release` mode cookies are marked `Secure` and only work over HTTPS.

**Local startup says a port is already in use.**
Another MySQL (3306) or Redis (6379) is already running. Change `MYSQL_PORT` / `REDIS_PORT` in `.env` and adjust the Compose port mappings.

**How do I delete all my data?**
Settings → Delete account, type `DELETE` to confirm. This cannot be undone.

## 10. Tech Stack and Project Layout

| Layer | Technology |
| --- | --- |
| Frontend | Vue 3, Pinia, Element Plus, ECharts, Vite |
| Backend | Go 1.24, Gin, MySQL 8.4, Redis 7 |
| Agent | In-process ReAct loop (max 6 model calls per turn), SSE streaming, server-side draft approval, amount verification |
| Models | Two adapters (`openai_compatible`, `anthropic`), platform key or per-user BYOK, daily credits |
| Tools | One Go registry exposed to both the agent and an MCP stdio server (`cmd/mcp-stdio`) |
| Storage | Private local disk or private S3 bucket, 24-hour expiry |
| Sign-in | Local username and password (development) / AWS Cognito (cloud) |
| Cloud deployment | AWS ECS + ALB, RDS MySQL, ElastiCache Redis, S3 + CloudFront, Secrets Manager, CloudWatch (ap-southeast-1) |

```
.
├── backend/              # Go backend (auth, finance, insight, agentv2, tools, drafts, llm, credits, eval)
│   ├── cmd/              # mcp-stdio, mock-llm, seed, migrate-attachments
│   └── migrations/       # 000001–000019, MySQL only
├── frontend/             # Vue 3 frontend (chat home, dashboard, transactions, settings)
├── deploy/               # Deployment scripts and notes
├── loadtest/             # k6 load-test scripts
├── docs/api/             # HTTP and agent API contract
└── docker-compose.ops.yml
```

## 11. Testing

```bash
cd backend && go test -p 1 ./...          # unit tests; MySQL/Redis suites skip unless their TEST_* variables are set
cd frontend && npm test && npm run build  # frontend unit tests and production build
```

The MySQL and Redis integration suites, and the variables that enable them, are described in [`backend/README.md`](backend/README.md#testing). GitHub Actions (`.github/workflows/ops-ci.yml`) runs both suites against MySQL 8.4 and Redis 7.

## 12. Documentation

- [Backend](backend/README.md): configuration, migrations, tests and the MCP stdio server
- [Frontend](frontend/README.md): development server, build and environment
- [Deployment](deploy/README.md): Docker Compose and the AWS reference deployment
- [Load testing](loadtest/README.md): k6 journey script
- [API contract](docs/api/contract.md): HTTP and agent endpoints (in Chinese)

## 13. Contributing

Bug reports and suggestions are welcome as [GitHub Issues](https://github.com/MOYU114/UniFinSG/issues). For pull requests, run the backend and frontend tests above and keep each change focused. Never commit `.env`, `config.json` or generated account CSVs.

## 14. License and Acknowledgements

- UniFinSG is released under the [MIT License](LICENSE).
- It is built on [MOYU114/UnichatGo](https://github.com/MOYU114/UnichatGo), whose authentication, session and streaming chat foundation is retained in the backend.
