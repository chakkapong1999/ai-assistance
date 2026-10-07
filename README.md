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
