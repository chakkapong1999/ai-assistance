package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/ingest"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

const (
	skipRepoDisabled = "repo review disabled"
	skipMerge        = "merge commit"
)

type processWebhookWorker struct {
	river.WorkerDefaults[jobs.ProcessWebhookArgs]
	d      Deps
	syncer *store.Syncer
}

func (w *processWebhookWorker) Timeout(*river.Job[jobs.ProcessWebhookArgs]) time.Duration {
	return webhookJobTimeout
}

// Work turns one stored delivery into commits and review jobs. The sync, the
// per-commit decision and the enqueue share one transaction: after a crash
// either all of it happened or none of it did, so a retry can never leave a
// commit without a job or queue the same commit twice.
func (w *processWebhookWorker) Work(ctx context.Context, job *river.Job[jobs.ProcessWebhookArgs]) error {
	var payload []byte
	var processedAt *time.Time
	err := w.d.Pool.QueryRow(ctx, `SELECT payload, processed_at FROM webhook_events WHERE id = $1`, job.Args.EventID).
		Scan(&payload, &processedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(fmt.Errorf("webhook event %d does not exist", job.Args.EventID))
	}
	if err != nil {
		return wrap("load event", err)
	}
	if processedAt != nil {
		return nil // already handled; a duplicate or manually retried job
	}

	ev, err := webhook.ParsePush(payload)
	if err != nil {
		// A payload that cannot be parsed will never parse; retrying is pointless.
		return river.JobCancel(err)
	}

	// Network work happens before the transaction so it is not held open.
	plan, err := ingest.Plan(ctx, w.d.Log, w.d.Bitbucket, ev)
	if err != nil {
		if d, ok := snoozeFor(err); ok {
			return river.JobSnooze(d)
		}
		return wrap("plan push", err)
	}

	tx, err := w.d.Pool.Begin(ctx)
	if err != nil {
		return wrap("begin", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	res, err := w.syncer.SyncPushTx(ctx, tx, plan)
	if err != nil {
		return wrap("sync push", err)
	}

	queued, skipped, err := settleNewCommits(ctx, tx, res)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `UPDATE webhook_events SET processed_at = now() WHERE id = $1`, job.Args.EventID); err != nil {
		return wrap("mark processed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return wrap("commit", err)
	}
	w.d.Log.Info("webhook processed", "event_id", job.Args.EventID, "repo", ev.Repository.FullName,
		"new_commits", len(res.NewCommits), "queued", queued, "skipped", skipped)
	return nil
}

// settleNewCommits decides, inside the sync transaction, what happens to each
// commit that was just stored: skipped with a reason (review off for the repo,
// or a merge commit) or queued for review. Webhooks and polling share it, so
// both treat a commit the same way.
func settleNewCommits(ctx context.Context, tx pgx.Tx, res store.PushResult) (queued, skipped int, err error) {
	rc, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return 0, 0, wrap("river client", err)
	}
	for _, c := range res.NewCommits {
		reason := ""
		switch {
		case !res.ReviewEnabled:
			reason = skipRepoDisabled
		case c.IsMerge:
			reason = skipMerge
		}
		if reason != "" {
			if _, err := tx.Exec(ctx, `UPDATE commits SET review_status = 'skipped', review_skip_reason = $2 WHERE id = $1`, c.ID, reason); err != nil {
				return 0, 0, wrap("skip commit", err)
			}
			skipped++
			continue
		}
		if _, err := rc.InsertTx(ctx, tx, jobs.ReviewCommitArgs{CommitID: c.ID}, nil); err != nil {
			return 0, 0, wrap("enqueue review", err)
		}
		queued++
	}
	return queued, skipped, nil
}
