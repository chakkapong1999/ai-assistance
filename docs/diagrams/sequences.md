# Sequence diagrams

Every runtime flow, traced from the code. The River job queue lives in the same Postgres database (`river_job`); it is drawn as its own participant only to show when jobs are inserted and picked up. A grey box is one database transaction.

Participants: **BB** Bitbucket, **API** `--mode=api`, **W** `--mode=worker`, **Q** River queue, **DB** Postgres, **LLM** the reviewer (`claude -p` or mock), **UI** Next.js server, **B** browser.

## 1. Webhook ingest

`POST /webhooks/bitbucket` (`httpapi/webhook.go`, `store/events.go`). Only `repo:push` gets a job. Other events are stored and nothing more.

```mermaid
sequenceDiagram
    participant BB as Bitbucket
    participant API as API server
    participant DB as Postgres
    participant Q as River queue (in Postgres)

    BB->>API: POST /webhooks/bitbucket (X-Hub-Signature, X-Event-Key, X-Request-UUID)
    API->>API: read body (max 5 MiB)
    alt body too large
        API-->>BB: 413
    end
    API->>API: verify HMAC signature
    alt bad signature
        API-->>BB: 401 invalid signature
    else missing headers or invalid JSON
        API-->>BB: 400
    end
    rect rgba(128,128,128,0.12)
        Note over API,Q: one transaction
        API->>DB: INSERT webhook_events ON CONFLICT (request_uuid) DO NOTHING
        alt duplicate request UUID
            DB-->>API: no row
            API-->>BB: 202 duplicate
        else new delivery
            opt event key is repo:push
                API->>Q: InsertTx process_webhook(event_id)
            end
            API->>DB: COMMIT
            API-->>BB: 202 accepted
        end
    end
    Note over BB,API: store error → 500, so Bitbucket retries with the same UUID
```

## 2. Process webhook job

`process_webhook` on the default queue, up to 4 at a time (`worker/process.go`, `ingest/ingest.go`).

```mermaid
sequenceDiagram
    participant Q as River queue
    participant W as Worker
    participant DB as Postgres
    participant BB as Bitbucket

    Q->>W: process_webhook(event_id)
    W->>DB: SELECT payload, processed_at FROM webhook_events
    alt event missing
        W-->>Q: JobCancel
    else already processed
        W-->>Q: done (no-op)
    end
    W->>W: ParsePush(payload)
    alt unparseable payload
        W-->>Q: JobCancel (will never parse)
    end
    loop each branch change (tags and deleted branches skipped)
        opt payload truncated
            W->>BB: ListCommits(include new head, exclude old head or main branch)
            BB-->>W: commits
            Note over W,BB: rate limited → JobSnooze(retry-after)
        end
        W->>W: keep newest 500 commits
    end
    rect rgba(128,128,128,0.12)
        Note over W,DB: one transaction
        W->>DB: SyncPushTx: upsert workspace, project, repo, users, commits, branches
        DB-->>W: new commits, repo review_enabled
        loop each new commit
            alt repo review off
                W->>DB: commit skipped "repo review disabled"
            else merge commit
                W->>DB: commit skipped "merge commit"
            else
                W->>Q: InsertTx review_commit(commit_id)
            end
        end
        W->>DB: UPDATE webhook_events SET processed_at = now()
        W->>DB: COMMIT
    end
    W-->>Q: done
```

## 3. Commit review job

`review_commit` on the `review` queue. One runs at a time (`MaxWorkers: 1`), with a 10 min timeout and 5 attempts (`worker/review.go`, `review/pipeline.go`).

```mermaid
sequenceDiagram
    participant Q as River queue
    participant W as Worker
    participant DB as Postgres
    participant BB as Bitbucket
    participant LLM as Reviewer (claude CLI or mock)

    Q->>W: review_commit(commit_id)
    W->>DB: load commit, repo, workspace, author
    alt commit missing
        W-->>Q: JobCancel
    else status done or skipped
        W-->>Q: done (no-op)
    else repo review switched off since queueing
        W->>DB: skipped "repo review disabled"
    end
    W->>DB: review_status = running
    W->>BB: GetDiff(workspace, repo, hash)
    alt 404 (force-pushed away)
        W->>DB: skipped "commit no longer available in Bitbucket"
    end
    W->>W: mask secrets, parse diff, filter files, split into chunks
    alt unparseable diff
        W->>DB: failed
        W-->>Q: JobCancel
    else nothing reviewable (empty or all files filtered)
        W->>DB: record stats, skipped "no reviewable files: ..."
    end
    loop each chunk
        W->>LLM: Review(repo, hash, message, author, chunk)
        LLM-->>W: summary, findings, usage, model
        W->>W: Sanitize: drop findings that do not match the diff
    end
    W->>W: sort findings, score
    alt review or save failed
        W->>DB: INSERT review_attempts (usage of the wasted attempt)
        alt usage limit or Bitbucket rate limit
            W->>DB: pending
            W-->>Q: JobSnooze(retry-after) (no attempt used)
        else worker shutting down
            W->>DB: pending
            W-->>Q: JobSnooze(0)
        else last attempt
            W->>DB: failed + reason
            W-->>Q: error (discarded)
        else
            W->>DB: pending
            W-->>Q: error (River retries)
        end
    else success
        rect rgba(128,128,128,0.12)
            Note over W,DB: one transaction
            W->>DB: INSERT reviews (model, score, summary, tokens, cost)
            W->>DB: INSERT review_findings + code_suggestions
            W->>DB: commit review_status = done, diff stats
            W->>DB: COMMIT
        end
        W-->>Q: done
    end
```

## 4. Poll round: commits

`poll_repos` runs on a timer (`POLL_INTERVAL`) and at start, only when `POLL_REPOS` is set. Only one round runs at a time (`worker/poll.go`).

```mermaid
sequenceDiagram
    participant Q as River queue
    participant W as Worker
    participant BB as Bitbucket
    participant DB as Postgres

    Q->>W: poll_repos (periodic)
    loop each POLL_REPOS entry
        alt "workspace/*"
            W->>BB: ListWorkspaceRepositories
            opt repo listed without a main branch
                W->>BB: GetRepository
            end
        else "workspace/repo"
            W->>BB: GetRepository
        end
    end
    loop each repository (errors logged, next repo)
        W->>DB: read poll_cursors (branch → last head)
        W->>BB: ListBranches
        loop each branch whose head moved (main branch first)
            alt cursor exists
                W->>BB: ListRecentCommits(head, exclude cursor)
            else first sight, not main branch
                W->>BB: ListRecentCommits(head, exclude main head, since lookback)
            else first sight, main branch
                W->>BB: ListRecentCommits(head, since POLL_LOOKBACK, max 500)
            end
        end
        opt something changed
            rect rgba(128,128,128,0.12)
                Note over W,DB: one transaction per repository
                W->>DB: SyncPushTx (same as webhooks, de-duplicated by hash)
                W->>DB: settle new commits: skip or InsertTx review_commit
                W->>DB: SavePollCursorsTx(heads)
                W->>DB: COMMIT
            end
        end
        W->>W: poll pull requests (flow 5)
    end
    Note over W,BB: any rate limit → JobSnooze(retry-after), the round restarts later
    W-->>Q: done
```

## 5. Poll round: pull requests

This step runs per repository inside the same `poll_repos` round, after that repository's commits (`worker/pollpr.go`).

```mermaid
sequenceDiagram
    participant W as Worker
    participant BB as Bitbucket
    participant DB as Postgres
    participant Q as River queue

    W->>DB: PRRepo(repo uuid)
    alt repo not synced yet
        W-->>W: skip
    end
    W->>BB: ListOpenPullRequests(max 100, newest update first)
    W->>DB: TrackedOpenPRs (open last round)
    opt open list not cut short
        loop tracked PR missing from the open list (max 20)
            W->>BB: GetPullRequest(id)
            alt 404
                W->>W: mark as vanished
            else
                BB-->>W: MERGED / DECLINED / SUPERSEDED
            end
        end
    end
    W->>DB: PendingPRs (pending, maybe lost their job)
    rect rgba(128,128,128,0.12)
        Note over W,Q: one transaction
        W->>DB: SyncPullRequestsTx(open + closed): upsert, new source head → pending
        Note right of DB: on first look, PRs not updated within POLL_LOOKBACK are left alone
        alt nothing changed and nothing pending
            W->>DB: ROLLBACK (idle round, no writes)
        else
            W->>DB: vanished PRs → state DELETED
            loop each PR needing review or pending
                W->>Q: InsertTx review_pull_request(pr_id) (unique while waiting or running)
            end
            W->>DB: COMMIT
        end
    end
```

## 6. Pull request review job

`review_pull_request` on the `review` queue. It shares the single review worker with commit reviews (`worker/reviewpr.go`).

```mermaid
sequenceDiagram
    participant Q as River queue
    participant W as Worker
    participant DB as Postgres
    participant BB as Bitbucket
    participant LLM as Reviewer

    Q->>W: review_pull_request(pr_id)
    W->>DB: load PR (state, review_status, source_hash = head), repo
    alt PR missing
        W-->>Q: JobCancel
    else done or skipped
        W-->>Q: done (no-op)
    else repo review off
        W->>DB: skipped
    else state is not OPEN
        W->>DB: skipped "pull request is not open"
    end
    W->>DB: review_status = running
    W->>BB: GetPullRequestDiff(id) (all commits together)
    alt 404
        W->>DB: skipped "pull request no longer available in Bitbucket"
    end
    loop each chunk
        W->>LLM: Review(title + description, head, chunk, PullRequest=true)
        LLM-->>W: findings, usage
    end
    alt failure
        Note over W,Q: same as the commit review: review_attempts row, then snooze, retry or fail
    else success
        rect rgba(128,128,128,0.12)
            Note over W,DB: one transaction
            W->>DB: INSERT reviews (pr_id, pr_head_hash = head) + findings + suggestions
            W->>DB: UPDATE pull_requests SET review_status = (source_hash = head ? done : pending)
            W->>DB: COMMIT
        end
        alt head moved during the review (a new push)
            W-->>Q: JobSnooze(1s), so the same job reviews the new head
        else
            W-->>Q: done
        end
    end
```

## 7. Reconcile (self-repair)

`reconcile` runs every `RECONCILE_INTERVAL` and at start, as one transaction with `FOR UPDATE SKIP LOCKED`. It only touches rows that have no live job and were not settled in the last 5 min (`worker/reconcile.go`).

```mermaid
sequenceDiagram
    participant Q as River queue
    participant W as Worker
    participant DB as Postgres

    Q->>W: reconcile (periodic)
    rect rgba(128,128,128,0.12)
        Note over W,DB: one transaction
        W->>DB: webhook_events unprocessed > 10 min, no live or cancelled job, < 4 jobs
        loop each
            W->>Q: InsertTx process_webhook
        end
        W->>DB: commits pending/running > 10 min, no live job
        loop each
            alt repo review off
                W->>DB: skipped "repo review disabled"
            else merge commit
                W->>DB: skipped "merge commit"
            else had 4 jobs already
                W->>DB: failed "use Review again"
            else
                W->>DB: pending
                W->>Q: ReleaseFinished + InsertTx review_commit
            end
        end
        W->>DB: open PRs pending/running > 10 min, no live job
        loop each
            alt repo review off
                W->>DB: skipped
            else had 4 jobs already
                W->>DB: failed "use Review again"
            else
                W->>DB: pending
                W->>Q: InsertTx review_pull_request
            end
        end
        W->>DB: COMMIT
    end
    W->>Q: RecordOutput(summary counts) (read by GET /health)
    W-->>Q: done
```

## 8. Directory sync

`sync_directory` runs every `DIRECTORY_SYNC_INTERVAL` (default 24h) and at start. When the token cannot read a place (401/403/404), that place is skipped and logged once per pass (`worker/directory.go`).

```mermaid
sequenceDiagram
    participant Q as River queue
    participant W as Worker
    participant DB as Postgres
    participant BB as Bitbucket

    Q->>W: sync_directory (periodic)
    W->>DB: list workspaces
    loop each workspace
        W->>BB: ListWorkspacePermissions
        BB-->>W: members + role
        W->>DB: SyncWorkspaceMembers (people who left are removed from roles, kept in users)
    end
    W->>DB: list repositories
    loop each repository
        W->>BB: ListRepoUserPermissions
        BB-->>W: users + admin/write/read
        W->>DB: SyncRepoPermissions
    end
    W->>DB: StaleProfiles(older than 24h, max 200)
    loop each stale profile
        W->>BB: GetUser(uuid)
        alt no access or gone
            W->>DB: MarkProfileChecked (try again tomorrow)
        else
            W->>DB: SaveProfile(name, nickname, avatar)
        end
    end
    Note over W,BB: rate limit → JobSnooze (the pass is idempotent)
    W-->>Q: done
```

## 9. Backfill (one-off)

`server --mode=backfill` is a separate process. It only inserts jobs and never runs them (`worker/backfill.go`).

```mermaid
sequenceDiagram
    actor Op as Operator
    participant BF as Backfill process
    participant BB as Bitbucket
    participant DB as Postgres
    participant Q as River queue

    Op->>BF: POLL_REPOS=... BACKFILL_DAYS=90 server --mode=backfill
    BF->>DB: CheckSchema
    BF->>BB: expand POLL_REPOS (as in polling)
    loop each repository
        BF->>BB: ListBranches + ListRecentCommits(since BACKFILL_DAYS, max per branch)
        Note right of BF: cursors ignored, history read in full
        BF->>DB: read poll_cursors (a followed branch keeps its cursor)
        rect rgba(128,128,128,0.12)
            Note over BF,Q: one transaction per repository
            BF->>DB: SyncPushTx (commits already stored are left alone)
            alt BACKFILL_REVIEW=false (default)
                BF->>DB: new commits skipped "history, not reviewed"
            else BACKFILL_REVIEW=true
                BF->>Q: InsertTx review_commit (repo review on, not merge)
            end
            BF->>DB: SavePollCursorsTx(merged heads)
            BF->>DB: COMMIT
        end
    end
    BF-->>Op: exit 0, or non-zero if any repository failed
```

## 10. Dashboard read (every page)

Pages are Next.js server components, so the browser never calls the API. The same path serves Overview, commits, pull requests, repositories, users, search, My work (`/me/work`), health and audit (admin only) (`frontend/lib/api.ts`, `lib/guard.tsx`, `restapi/restapi.go`).

```mermaid
sequenceDiagram
    actor U as User
    participant B as Browser
    participant UI as Next.js server
    participant API as API server
    participant DB as Postgres

    U->>B: open /commits/42
    B->>UI: GET /commits/42 (cookie aicr_token if signed in)
    UI->>UI: token = cookie aicr_token, else API_TOKEN
    par layout
        UI->>API: GET /api/v1/me
        opt signed-in person
            UI->>API: GET /api/v1/me/work (My work count)
        end
    and page
        UI->>API: GET /api/v1/commits/42 (Bearer token, no-store)
    end
    API->>API: sha256(token), constant-time compare against API_TOKENS
    alt unknown token
        API-->>UI: 401
    else role below route's access
        API-->>UI: 403
    else
        API->>DB: query (store.Dashboard)
        DB-->>API: rows
        API-->>UI: 200 JSON (Cache-Control: no-store)
    end
    UI->>UI: guard(): 404 → not-found page, other error → red banner
    UI-->>B: HTML
    opt review pending or running
        loop every 5 s (detail) or 15 s (lists, overview) while the tab is visible
            B->>UI: AutoRefresh router.refresh()
            UI->>API: GET same endpoints
            UI-->>B: updated RSC payload
        end
    end
```

## 11. Sign in and sign out

`frontend/app/signin/actions.ts`. The personal token replaces the shared `API_TOKEN` for every API call that follows.

```mermaid
sequenceDiagram
    actor U as User
    participant B as Browser
    participant UI as Next.js server
    participant API as API server

    U->>B: paste personal token on /signin
    B->>UI: server action signIn(token, next)
    alt empty token
        UI-->>B: redirect /signin?error=...
    end
    UI->>API: GET /api/v1/me (Bearer personal token)
    alt 401
        UI-->>B: redirect /signin?error="The API does not know this token"
    else token has no user_id
        UI-->>B: redirect /signin?error="not linked to a person"
    else
        API-->>UI: role + user
        UI->>B: Set-Cookie aicr_token (httpOnly, lax, 14 days)
        UI-->>B: redirect next (same-site paths only)
    end

    U->>B: Sign out
    B->>UI: server action signOut
    UI->>B: delete cookie aicr_token
    UI-->>B: redirect /
```

## 12. Fix workflow: author fixes, senior closes

`frontend/app/workflow/actions.ts` calls `POST /findings/{id}/fixed|reopen|dismiss` and `POST /reviews/{id}/close`. The store decides who may do what (`store/workflow.go`). Each change and its audit row are written in one transaction.

```mermaid
sequenceDiagram
    actor A as Author
    actor S as Senior / lead / admin
    participant UI as Next.js server
    participant API as API server
    participant DB as Postgres

    A->>UI: Mark fixed (optional note)
    UI->>API: POST /api/v1/findings/7/fixed (author token)
    API->>API: route needs author role or higher
    rect rgba(128,128,128,0.12)
        Note over API,DB: one transaction
        API->>DB: token user still exists?
        API->>DB: lock finding + review FOR UPDATE
        alt not the change's author (admin may stand in)
            API-->>UI: 403
        else review closed or finding not open
            API-->>UI: 409
        else
            API->>DB: finding status = fixed
            API->>DB: INSERT finding_events + audit_log "finding.fixed"
            API->>DB: COMMIT
            API-->>UI: 200
        end
    end
    UI-->>A: redirect back with notice

    S->>UI: Send back or Dismiss (note required)
    UI->>API: POST /api/v1/findings/7/reopen or /dismiss
    rect rgba(128,128,128,0.12)
        Note over API,DB: one transaction
        API->>DB: lock finding + review
        alt own work, or no user on a non-admin token
            API-->>UI: 403
        else note missing
            API-->>UI: 409
        else send back
            opt review was closed
                API->>DB: reopen review + audit "review.reopened"
            end
            API->>DB: finding status = open + finding_events + audit
            API-->>UI: 200
        else dismiss (review not closed)
            API->>DB: finding status = dismissed + finding_events + audit
            API-->>UI: 200
        end
    end

    S->>UI: Close review
    UI->>API: POST /api/v1/reviews/3/close
    rect rgba(128,128,128,0.12)
        Note over API,DB: one transaction
        API->>DB: lock review, count open findings
        alt own work
            API-->>UI: 403
        else already closed or open findings > 0
            API-->>UI: 409
        else
            API->>DB: reviews.closed_at = now(), closed_by
            API->>DB: audit "review.closed"
            API-->>UI: 200
        end
    end
    UI-->>S: redirect back with notice
```

## 13. Review again (commit or pull request)

Admin only. Commits use `store.Rereview` and pull requests use `store.RereviewPullRequest`. The job is picked up by flow 3 or flow 6.

```mermaid
sequenceDiagram
    actor U as Admin
    participant UI as Next.js server
    participant API as API server
    participant DB as Postgres
    participant Q as River queue

    U->>UI: Review again
    UI->>API: POST /api/v1/commits/42/rereview (or /pull-requests/9/rereview)
    alt token below admin
        API-->>UI: 403
        UI-->>U: "viewer role" notice
    end
    rect rgba(128,128,128,0.12)
        Note over API,Q: one transaction
        API->>DB: lock commit (or PR) + repo FOR UPDATE
        alt running
            API-->>UI: 409 review running
        else repo review off
            API-->>UI: 409
        else merge commit (commits) or PR not OPEN (PRs)
            API-->>UI: 409
        else
            API->>DB: review_status = pending
            opt commit
                API->>Q: ReleaseFinished (free unique key of old finished jobs)
            end
            API->>Q: InsertTx review job (unique only while waiting or running)
            API->>DB: audit "commit.rereview" / "pull_request.rereview"
            API->>DB: COMMIT
            API-->>UI: 200
        end
    end
    UI-->>U: redirect to the detail page "Queued for another review", which then auto-refreshes
```

## 14. Repository review on/off

Admin only (`PATCH /api/v1/repositories/{id}`). Commits already queued see the new value when their job runs: flow 3 skips them if review is now off.

```mermaid
sequenceDiagram
    actor U as Admin
    participant UI as Next.js server
    participant API as API server
    participant DB as Postgres

    U->>UI: toggle review on /repositories
    UI->>API: PATCH /api/v1/repositories/5 {review_enabled}
    alt token below admin
        API-->>UI: 403
    end
    rect rgba(128,128,128,0.12)
        Note over API,DB: one transaction
        API->>DB: lock repository FOR UPDATE
        opt value actually changes
            API->>DB: UPDATE repositories SET review_enabled
            API->>DB: audit "repository.review_enabled" / "repository.review_disabled"
        end
        API->>DB: COMMIT
    end
    API-->>UI: 200 repository
    UI->>UI: revalidatePath /repositories
    UI-->>U: page re-rendered (notice only on error)
```
