# ai-assistance

AI code review platform: receives Bitbucket webhooks for every commit, reviews the diff with an LLM, stores results in Postgres and shows them on a dashboard.

- `backend/`  Go (API + worker)
- `frontend/` Next.js dashboard
- `api/`      OpenAPI contract shared by both

Work is delivered milestone by milestone (M0 to M7), one PR each.

## Local database setup

```sh
cd backend
make up             # Postgres via docker compose
make migrate        # our schema (golang-migrate)
make migrate-river  # River's job-queue schema (needs Go; River owns these tables)
make run-api        # refuses to start if either step above was skipped
```

Integration tests that need Postgres run only when `TEST_DATABASE_URL` is set
(see `backend/.env.example`); otherwise they are skipped.

## Trying the pipeline locally

1. Set `BITBUCKET_TOKEN` to a real token with read access (the worker fetches each commit's diff from Bitbucket
   even when `REVIEWER_MODE=mock`; mock only replaces the LLM) and `BITBUCKET_WEBHOOK_SECRET` to the secret
   configured on the Bitbucket webhook.
2. Start the api and the worker (`docker compose up -d --build`, or `make run-api` / `make run-worker`).
3. Push to a repo that has the webhook. The repo and its commits appear in the database, but nothing is reviewed
   yet: **repositories start with `review_enabled = false`** (code is sent to an LLM, so it is opt-in). Commits are
   recorded as `skipped` with the reason `repo review disabled`.
4. Opt a repo in (until the dashboard has a toggle):
   `UPDATE repositories SET review_enabled = true WHERE slug = 'my-repo';`
   Commits pushed from then on are reviewed, one at a time.

Commit states: `pending` -> `running` -> `done` | `skipped` (with `review_skip_reason`) | `failed`. A commit shows
`failed` only after its last retry; usage limits and rate limits reschedule the job instead of failing it.

## Testing without Bitbucket (mock payload + mock Bitbucket)

No Bitbucket account, token or real commits are needed:

```sh
# 1. a stand-in for the Bitbucket API (serves one canned diff for every commit)
cd backend && go run ./cmd/mockbitbucket            # :7990; -diff my.diff to serve your own

# 2. api + worker, reviews mocked, pointed at the stand-in
#    (BITBUCKET_BASE_URL on the worker; any BITBUCKET_TOKEN value works against the mock)
BITBUCKET_BASE_URL=http://localhost:7990 BITBUCKET_TOKEN=dev REVIEWER_MODE=mock \
  DATABASE_URL=... go run ./cmd/server --mode=worker
BITBUCKET_WEBHOOK_SECRET=dev-secret DATABASE_URL=... go run ./cmd/server --mode=api

# 3. send a signed repo:push (new random commit hashes each time; payload defaults to backend/testdata/repo_push.json)
scripts/send-webhook.sh                      # WEBHOOK_URL / WEBHOOK_SECRET / FRESH=0 to tweak
```

The first push is recorded but skipped (`repo review disabled`); run
`UPDATE repositories SET review_enabled = true;`, send again, and the new commit is reviewed
(`mock` model, 2 findings, one with a suggestion diff). Use your own payload by passing a file; use
`MOCK_REVIEW_SCENARIO=clean|findings|invalid_json|usage_limit|timeout` to see each outcome.
`BITBUCKET_BASE_URL` sends the token to that URL, so leave it unset for real use.

## REST API (`/api/v1`)

The dashboard reads everything through a JSON API served by `--mode=api`. The contract is
[`backend/internal/restapi/openapi.yaml`](backend/internal/restapi/openapi.yaml), also served at
`GET /api/v1/openapi.yaml`; a test fails if the spec and the routes drift apart.

```bash
# token:role pairs; roles are viewer (read only) and admin (also toggle repositories, re-review a commit)
API_TOKENS=$(openssl rand -hex 24):admin  go run ./cmd/server --mode=api   # from backend/
curl -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/overview
```

- **No `API_TOKENS` = no API.** The routes are not registered and the API logs a warning; it never falls back to open access.
- Viewer tokens never see `users.email`; writes need an admin token.
- With `docker compose`, `API_TOKENS` defaults to `dev-admin-token-change-me:admin` and the frontend gets the same value as `API_TOKEN`. Change both for anything but local use.
- **This authenticates the dashboard server, not the people using it.** Anyone who can open the dashboard can do what its token can. End-user login (for example Bitbucket OAuth) is not built yet; until then keep the dashboard on a trusted network.

## Dashboard (`frontend/`)

Next.js (server components, no client-side data fetching): the browser only receives HTML; the server reads the REST API with `API_TOKEN`.

| Page | Shows |
| --- | --- |
| `/` Overview | commits, average score, review cost and tokens, queue, findings by severity, per-day charts (7 / 30 / 90 days) |
| `/commits` | every commit, newest first, filter by text, repository, status, branch, author; older pages via cursor |
| `/commits/{id}` | latest review: summary, findings by severity, suggested changes as diffs, tokens and cost of the run; **Review again** (admin token) |
| `/repositories` | per-repository numbers and the **review on/off** switch (admin token) |
| `/people`, `/users/{id}` | commits, average score, findings per author, weekly trend |

```bash
cd frontend && cp .env.example .env.local   # API_BASE_URL, API_TOKEN (one of the API's API_TOKENS), DISPLAY_TZ
npm ci && npm run dev
```

A viewer token makes the whole dashboard read-only (the buttons disappear and e-mails are hidden); an admin token enables the two actions.
Remember that **anyone who can open the dashboard acts with its token** - there is no per-user login yet.
