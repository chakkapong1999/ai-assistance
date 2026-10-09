package worker

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
)

const giveUpReason = "review did not finish after several queued attempts; use Review again"

// ReconcileSummary is what one pass did; it is recorded as the job's output,
// which the health endpoint reads back.
type ReconcileSummary struct {
	Events       int `json:"events"`
	Commits      int `json:"commits"`
	PullRequests int `json:"pull_requests"`
	GaveUp       int `json:"gave_up"`
}

func (s ReconcileSummary) total() int { return s.Events + s.Commits + s.PullRequests + s.GaveUp }

type reconcileWorker struct {
	river.WorkerDefaults[jobs.ReconcileArgs]
	d Deps
}

// Work puts back work that lost its job. Jobs are the only thing that moves
// a stored row forward, and a job can be lost: it was discarded after its
// retries ran out while the cause was a passing outage, or its worker died
// while the row said "running", or it was pruned. All of it is queued again
// with the same uniqueness rules as the normal paths, so running a pass twice
// (or while the poller does its own sweep) never doubles anything.
func (w *reconcileWorker) Work(ctx context.Context, _ *river.Job[jobs.ReconcileArgs]) error {
	rc, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return wrap("river client", err)
	}
	tx, err := w.d.Pool.Begin(ctx)
	if err != nil {
		return wrap("begin", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var sum ReconcileSummary
	if sum.Events, err = requeueEvents(ctx, tx, rc); err != nil {
		return err
	}
	if sum.Commits, sum.GaveUp, err = requeueCommits(ctx, tx, rc); err != nil {
		return err
	}
	var gave int
	if sum.PullRequests, gave, err = requeuePullRequests(ctx, tx, rc); err != nil {
		return err
	}
	sum.GaveUp += gave
	if err := tx.Commit(ctx); err != nil {
		return wrap("commit", err)
	}

	if sum.total() > 0 {
		w.d.Log.Warn("reconcile: queued work that had lost its job",
			"events", sum.Events, "commits", sum.Commits, "pull_requests", sum.PullRequests, "gave_up", sum.GaveUp)
	}
	if err := river.RecordOutput(ctx, sum); err != nil {
		w.d.Log.Warn("reconcile: record output", "error", err)
	}
	return nil
}

// requeueEvents queues a processing job for every stored webhook delivery that
// nobody processed and that has no job left.
func requeueEvents(ctx context.Context, tx pgx.Tx, rc *river.Client[pgx.Tx]) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT e.id FROM webhook_events e
		WHERE e.processed_at IS NULL AND e.received_at < now() - make_interval(secs => $3::int)
		`+jobs.NoLiveJobSQL("process_webhook", "event_id", "e.id", true)+`
		  AND `+jobs.JobCountSQL("process_webhook", "event_id", "e.id")+` < $4
		ORDER BY e.id LIMIT $5
		FOR UPDATE OF e SKIP LOCKED`,
		jobs.LiveStates(), int(jobs.ReconcileSettle.Seconds()), int(jobs.ReconcileGrace.Seconds()), jobs.ReconcileMaxJobs, jobs.ReconcileBatch)
	if err != nil {
		return 0, wrap("find stuck events", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, wrap("read stuck events", err)
	}
	for _, id := range ids {
		if _, err := rc.InsertTx(ctx, tx, jobs.ProcessWebhookArgs{EventID: id}, nil); err != nil {
			return 0, wrap("requeue event", err)
		}
	}
	return len(ids), nil
}

type stuck struct {
	id      int64
	jobs    int
	enabled bool
	merge   bool
}

func requeueCommits(ctx context.Context, tx pgx.Tx, rc *river.Client[pgx.Tx]) (queued, gaveUp int, err error) {
	rows, err := tx.Query(ctx, `
		SELECT c.id, `+jobs.JobCountSQL("review_commit", "commit_id", "c.id")+`, r.review_enabled, c.is_merge
		FROM commits c JOIN repositories r ON r.id = c.repo_id
		WHERE c.review_status IN ('pending', 'running') AND c.created_at < now() - make_interval(secs => $3::int)
		`+jobs.NoLiveJobSQL("review_commit", "commit_id", "c.id", false)+`
		ORDER BY c.id LIMIT $4
		FOR UPDATE OF c SKIP LOCKED`,
		jobs.LiveStates(), int(jobs.ReconcileSettle.Seconds()), int(jobs.ReconcileGrace.Seconds()), jobs.ReconcileBatch)
	if err != nil {
		return 0, 0, wrap("find stuck commits", err)
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (s stuck, err error) {
		err = r.Scan(&s.id, &s.jobs, &s.enabled, &s.merge)
		return
	})
	if err != nil {
		return 0, 0, wrap("read stuck commits", err)
	}
	for _, c := range list {
		switch {
		case !c.enabled:
			err = setCommit(ctx, tx, c.id, "skipped", skipRepoDisabled)
		case c.merge:
			err = setCommit(ctx, tx, c.id, "skipped", skipMerge)
		case c.jobs >= jobs.ReconcileMaxJobs:
			err = setCommit(ctx, tx, c.id, "failed", giveUpReason)
			gaveUp++
		default:
			if err = setCommit(ctx, tx, c.id, "pending", ""); err == nil {
				err = jobs.ReleaseFinished(ctx, tx, "review_commit", "commit_id", c.id)
			}
			if err == nil {
				_, err = rc.InsertTx(ctx, tx, jobs.ReviewCommitArgs{CommitID: c.id}, reinsertOpts(jobs.ReviewCommitArgs{}.InsertOpts()))
			}
			queued++
		}
		if err != nil {
			return 0, 0, wrap("requeue commit", err)
		}
	}
	return queued, gaveUp, nil
}

func requeuePullRequests(ctx context.Context, tx pgx.Tx, rc *river.Client[pgx.Tx]) (queued, gaveUp int, err error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, `+jobs.JobCountSQL("review_pull_request", "pull_request_id", "p.id")+`, r.review_enabled, false
		FROM pull_requests p JOIN repositories r ON r.id = p.repo_id
		WHERE p.state = 'OPEN' AND p.review_status IN ('pending', 'running') AND p.updated_at < now() - make_interval(secs => $3::int)
		`+jobs.NoLiveJobSQL("review_pull_request", "pull_request_id", "p.id", false)+`
		ORDER BY p.id LIMIT $4
		FOR UPDATE OF p SKIP LOCKED`,
		jobs.LiveStates(), int(jobs.ReconcileSettle.Seconds()), int(jobs.ReconcileGrace.Seconds()), jobs.ReconcileBatch)
	if err != nil {
		return 0, 0, wrap("find stuck pull requests", err)
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (s stuck, err error) {
		err = r.Scan(&s.id, &s.jobs, &s.enabled, &s.merge)
		return
	})
	if err != nil {
		return 0, 0, wrap("read stuck pull requests", err)
	}
	for _, p := range list {
		switch {
		case !p.enabled:
			_, err = tx.Exec(ctx, `UPDATE pull_requests SET review_status = 'skipped', review_skip_reason = 'review is not enabled for this repository' WHERE id = $1`, p.id)
		case p.jobs >= jobs.ReconcileMaxJobs:
			_, err = tx.Exec(ctx, `UPDATE pull_requests SET review_status = 'failed', review_skip_reason = $2 WHERE id = $1`, p.id, giveUpReason)
			gaveUp++
		default:
			if _, err = tx.Exec(ctx, `UPDATE pull_requests SET review_status = 'pending', review_skip_reason = NULL WHERE id = $1`, p.id); err == nil {
				_, err = rc.InsertTx(ctx, tx, jobs.ReviewPullRequestArgs{PullRequestID: p.id}, nil)
			}
			queued++
		}
		if err != nil {
			return 0, 0, wrap("requeue pull request", err)
		}
	}
	return queued, gaveUp, nil
}

func setCommit(ctx context.Context, tx pgx.Tx, id int64, status, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE commits SET review_status = $2, review_skip_reason = NULLIF($3, '') WHERE id = $1`, id, status, reason)
	return err
}

// reinsertOpts makes a job count as a duplicate only while another one for the
// same commit is waiting or running. River's default also counts a finished
// job, which would swallow this request.
func reinsertOpts(o river.InsertOpts) *river.InsertOpts {
	o.UniqueOpts = river.UniqueOpts{ByArgs: true, ByState: jobs.WaitingOrRunning}
	return &o
}
