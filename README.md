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
# token:role[:user_id] pairs; roles: viewer (read only), author, senior, lead, admin (see "Fix workflow" below)
API_TOKENS=$(openssl rand -hex 24):admin  go run ./cmd/server --mode=api   # from backend/
curl -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/overview
```

- **No `API_TOKENS` = no API.** The routes are not registered and the API logs a warning; it never falls back to open access.
- Viewer tokens never see `users.email`; writes need an admin token.
- `docker compose` reads settings from a `.env` in the repository root (`cp .env.example .env`); the api and worker containers get every variable in it. `DATABASE_URL` is always set by compose. `API_TOKENS` defaults to `dev-admin-token-change-me:admin` and the frontend gets the same value as `API_TOKEN`. Change both for anything but local use.
- The shared `API_TOKEN` of the dashboard identifies the dashboard server only. People who need to act sign in with their own token (next section); real login (for example Bitbucket OAuth) is not built yet, so keep the dashboard on a trusted network.

## Fix workflow: author fixes, senior closes (migration 0005)

After the AI review, each finding goes through three steps:

1. **Author:** marks a finding **fixed** (optional note on what changed). Only the author of that commit or pull request can.
2. **Senior, lead or admin:** looks at the fix and either **sends it back** (note required; the finding is open again), **dismisses** it as not a real problem (note required), or leaves it fixed.
3. Once no finding is open, a senior, lead or admin **closes the review**. Nobody can review their own work: a reviewer who authored the commit sees the review but cannot close it. Sending a finding back on a closed review reopens the review.

Everything is recorded (who, when, note) and shown under each finding. A new automatic review of the same commit (Review again) starts fresh with all findings open.

Who is who comes from the token: `API_TOKENS=token:role:user_id`, where `user_id` is the person's id on the *People* page (`/users/{id}`).

```bash
API_TOKENS='<random>:author:12,<random>:senior:7,<random>:lead:3,<random>:admin:1,<shared dashboard token>:viewer'
```

- `viewer` reads. `author` (needs a user id) marks own findings fixed. `senior` and `lead` (need a user id) review and close. `admin` can do all of it plus the settings; an admin token without a user id still manages repositories but cannot take part in reviews.
- Dashboard: **Sign in** (top right) with a personal token; it is kept in an httpOnly cookie for 14 days (`COOKIE_SECURE=true` marks it secure when you serve over HTTPS). Sign out removes it.
- Where you see it: each finding and a bar above the findings on the commit/PR page; a "N to fix / Ready to close / Closed" mark in the commit and pull request lists (`open_findings`, `review_closed` in the API); a **Fixes** summary on the Overview (open findings, reviews ready to close, closed, for the commits in the window; `fix` in `GET /api/v1/overview`).
- The Overview also shows the pipeline health from `GET /api/v1/health` (see "Health and self-repair"): a red box listing what is wrong, and the last worker check and last poll under *Cost and capacity*.
- When Bitbucket OAuth arrives, only the sign-in step changes: the workflow already works on users and roles.
- Run `make migrate` for migration 0005. Endpoints: `POST /api/v1/findings/{id}/fixed|reopen|dismiss`, `POST /api/v1/reviews/{id}/close`; `GET /api/v1/me` shows who the token is.

## Dashboard (`frontend/`)

Next.js (server components, no client-side data fetching): the browser only receives HTML; the server reads the REST API with `API_TOKEN`.

| Page | Shows |
| --- | --- |
| `/` Overview | commits, average score, review cost and tokens, queue, findings by severity, per-day charts (7 / 30 / 90 days) |
| `/commits` | every commit, newest first, filter by text, repository, status, branch, author; older pages via cursor |
| `/commits/{id}` | latest review: summary, findings by severity, suggested changes as diffs, tokens and cost of the run; **Review again** (admin token) |
| `/pull-requests`, `/pull-requests/{id}` | pull requests with the review of their whole diff, state (open / merged / ...), an "outdated" hint when the branch has new commits; **Review again** (admin token, open PRs only) |
| `/repositories` | per-repository numbers and the **review on/off** switch (admin token) |
| `/people`, `/users/{id}` | commits, average score, findings per author, weekly trend |

```bash
cd frontend && cp .env.example .env.local   # API_BASE_URL, API_TOKEN (one of the API's API_TOKENS), DISPLAY_TZ
npm ci && npm run dev
```

A viewer token makes the whole dashboard read-only (the buttons disappear and e-mails are hidden); an admin token enables the two actions.
Remember that **anyone who can open the dashboard acts with its token** - there is no per-user login yet.

## Which repositories are reviewed

A repository is created automatically the first time the worker sees it (a webhook or a poll). It **starts with review on**, so its code goes to the reviewer (a real model when `REVIEWER_MODE=claude_cli`) without anyone choosing it. To make new repositories start off instead, set `REVIEW_NEW_REPOS=false` on the worker and switch repositories on one by one in the dashboard.

- It is only the starting value: a repository an admin switched off or on is never changed by later syncs.
- Repositories that already exist keep their current setting; changing the variable affects only repositories created afterwards.
- Commits that arrived while a repository had review off stay *skipped*; use **Review again** on them if you want them reviewed.

## Health and self-repair

Jobs can be lost (a database restore, a queue purge, a crash between two writes). The worker therefore checks every `RECONCILE_INTERVAL` (default 5m, min 1m; it also runs once at start):

- **Webhook deliveries** stored but never processed, with no job left for them, are queued again. A delivery whose job was cancelled is treated as bad input and left alone.
- **Commits and open pull requests** still *pending* or *running* after 10 minutes with no live job are queued again; one that has already had 4 jobs is marked *failed* ("use Review again"). Repositories with review switched off and merge commits are marked *skipped* instead.
- Work is only touched when no job exists for it, and each pass runs in one transaction, so it never double-queues a review that is simply slow.

`GET /api/v1/health` (viewer token) answers "is the pipeline moving?": unprocessed deliveries and their age, jobs waiting/running/retrying, failed jobs in the last 24h, commits and pull requests without a job, the last polling round and the last reconcile pass with its counts. `status` is `degraded` with plain-language `problems` when something has waited too long.

## Polling instead of (or besides) webhooks

If you cannot register a webhook, the worker can look for new commits itself:

```bash
POLL_REPOS='acme/api,acme/web'   # or 'acme/*' for every repository of a workspace the token can see
POLL_INTERVAL=5m                 # min 1m
POLL_LOOKBACK=168h               # how far back the FIRST poll of a branch reads
```

- Each round, per repository: read the branch list; for every branch whose head moved since the last round, read only the commits after the last synced head. A branch that did not move costs no commits request.
- Commits go through the same path as webhook commits: stored once (de-duplicated by hash, so polling and webhooks can run together), skipped when review is off for the repository or for merge commits, otherwise queued for review.
- Commits and the "where I got to" cursor (`poll_cursors`) are saved in one transaction, so a failed round is simply repeated. A Bitbucket rate limit pauses the round; one repository that cannot be read is logged and does not stop the others.
- The first poll of a branch reads at most `POLL_LOOKBACK` (and 500 commits); a new branch only counts what the main branch does not have. A repository the worker meets for the first time starts with review **on** (`REVIEW_NEW_REPOS`, below), so its first-round commits are reviewed; with it set to `false` they are stored as *skipped* and are not reviewed if you switch review on later.
- Requires migration 0002 (`make migrate`). Needs the same access token as reviewing (`Repositories: Read`).
- Try it without Bitbucket: `go run ./cmd/mockbitbucket -repo acme/demo`, run the worker with `BITBUCKET_BASE_URL=http://localhost:7990 POLL_REPOS=acme/demo POLL_INTERVAL=1m`, then add a commit with `curl -X POST 'localhost:7990/_mock/commit?repo=acme/demo&branch=main&message=hello'`.

### Code next to each finding (migration 0004)

From migration 0004 each finding stores the diff hunk it is about (`code_context`), and the dashboard shows it as a diff, unified or split, with the finding's lines marked and the suggested change underneath. Reviews made before the migration have no stored code: they show the explanation and the suggestion diff only, until the commit or pull request is reviewed again. The API and worker refuse to start until `make migrate` has run.

## Pull request reviews

Besides each commit, the worker reviews every **open pull request as one diff** (all its commits together), so a problem that only shows when the commits are read together is not missed. Commit reviews are unchanged and keep their own page.

- **Needs polling** (`POLL_REPOS`): pull requests are read by the poller, not from webhooks. Each round, per repository, it makes one request for the open pull requests (most recently updated first, at most 100) and stores what changed.
- **When it reviews:** when a pull request is first seen and each time its source branch gets a new commit. Title or description edits and comments do not trigger a review. A push that arrives *while* a review runs is not lost: the review is saved against the head it started from, the pull request goes back to `pending`, and the same job runs again for the new head.
- **What it skips:** a repository with review off (recorded as *skipped*, like commits), a pull request that is no longer open when its turn comes, and, on the first look at a repository, open pull requests not updated within `POLL_LOOKBACK`, so a backlog of stale PRs is not all sent to the model at once.
- **Closed pull requests:** a pull request that was open and no longer is gets one lookup, so its state (merged, declined, `DELETED` when Bitbucket no longer knows it) stays right. It keeps its last review and is not reviewed again.
- **Cost:** a pull request is reviewed once per push, so a busy branch costs more than its commits alone would. Runs are counted in the overview's usage and cost like any other review. Reviews of the same pull request are one at a time, in the same queue as commit reviews.
- Requires migration 0003 (`make migrate`); the API and worker refuse to start without it. The access token also needs **Pull requests: Read** (Bitbucket scope `pullrequest`); without it the pull request step fails for each repository (logged) while commit polling keeps working. Not verified against real Bitbucket: the response shapes follow the public API docs and the mock.

- Try it without Bitbucket: with the mock above, `curl -X POST 'localhost:7990/_mock/pullrequest?repo=acme/demo&source=feature/x&title=Add+x'` (the branch needs a commit first), push more commits to the branch, and `...&id=1&state=MERGED` to merge it.
