# UniFinSG frontend

Vue 3 + Pinia + Element Plus client for UniFinSG, built with Vite. It is
mobile-first and works down to 375px wide, with a sidebar on desktop and a
bottom navigation bar on phones.

## Pages

| Route | View | What it does |
| --- | --- | --- |
| `/chat`, `/chat/:sessionId` | `ChatView.vue` | Home page: conversations with the agent, image attachments, draft cards to confirm or reject, credit badge |
| `/dashboard` | `FinanceDashboardView.vue` | Spent / remaining / used, month-end projection, category bars, spending donut, recent expenses, advice |
| `/transactions` | `TransactionsView.vue` | Month and category filters, create / edit / delete, AI badge on agent-written entries |
| `/onboarding` | `OnboardingView.vue` | Monthly disposable amount, housing, total budget and six category budgets |
| `/settings` | `SettingsView.vue` | Profile and budget, model settings (platform or BYOK with connection test), sign out, delete account |
| `/signin`, `/register` | `LoginView.vue`, `RegisterView.vue` | Local sign-in, or one Cognito hosted sign-in button |

Session state is restored from `GET /api/me`, and route guards send new users
to onboarding.

## Commands

```bash
npm ci
npm run dev       # http://localhost:5173, proxies /api to http://localhost:8090
npm test          # node --test tests/*.test.js
npm run build     # production build in dist/
```

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `VITE_API_BASE_URL` | `/api` | Same-origin backend API prefix |
| `VITE_AUTH_MODE` | `local` | `local` shows the username/password form; `cognito` shows a hosted sign-in button. Must match the backend's `AUTH_MODE` |

## Conventions

- The backend is the source of truth for amounts, percentages, identity and
  onboarding status; the client does not recompute them.
- Amounts are sent to the API as integer SGD cents.
- Every write request sends the `csrf_token` cookie value as `X-CSRF-Token`.
- Agent replies are streamed over SSE and rendered as sanitised Markdown
  (`marked` + `DOMPurify`).

## Container image

`Dockerfile` builds the static bundle and serves it with nginx
(`nginx.conf`), which also proxies `/api` to the backend service in Docker
Compose.
