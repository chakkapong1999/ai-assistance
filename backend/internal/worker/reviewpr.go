package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

type reviewPullRequestWorker struct {
	river.WorkerDefaults[jobs.ReviewPullRequestArgs]
	d Deps
}

func (w *reviewPullRequestWorker) Timeout(*river.Job[jobs.ReviewPullRequestArgs]) time.Duration {
	return ReviewJobTimeout
}

type prRow struct {
	bbID                              int
	title, description, state, status string
	head, author                      string
	repoSlug, workspaceSlug           string
	reviewEnabled                     bool
}

// Work reviews one pull request: the whole diff, all commits together.
//
// pull_requests.review_status follows the same states as commits. The head the
// review was taken at is read first and stored with the review; if the branch
// has moved by the time the review is saved, the pull request goes back to
// pending and this job runs again for the new head (a second job could not be
// inserted while this one is running).
func (w *reviewPullRequestWorker) Work(ctx context.Context, job *river.Job[jobs.ReviewPullRequestArgs]) error {
	id := job.Args.PullRequestID

	var p prRow
	err := w.d.Pool.QueryRow(ctx, `
		SELECT pr.bb_pr_id, pr.title, pr.description, pr.state, pr.review_status, COALESCE(pr.source_hash, ''),
		       r.slug, ws.slug, r.review_enabled, COALESCE(u.display_name, '')
		FROM pull_requests pr
		JOIN repositories r ON r.id = pr.repo_id
		JOIN projects pj ON pj.id = r.project_id
		JOIN workspaces ws ON ws.id = pj.workspace_id
		LEFT JOIN users u ON u.id = pr.author_user_id
		WHERE pr.id = $1`, id).
		Scan(&p.bbID, &p.title, &p.description, &p.state, &p.status, &p.head,
			&p.repoSlug, &p.workspaceSlug, &p.reviewEnabled, &p.author)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(fmt.Errorf("pull request %d does not exist", id))
	}
	if err != nil {
		return wrap("load pull request", err)
	}
	if p.status == "done" || p.status == "skipped" {
		return nil // already settled; do not review twice
	}
	if !p.reviewEnabled {
		return w.settle(ctx, id, "skipped", store.PRSkipRepoDisabled)
	}
	if p.state != "OPEN" {
		return w.settle(ctx, id, "skipped", store.PRSkipNotOpen)
	}

	if err := w.setStatus(ctx, id, "running", ""); err != nil {
		return wrap("mark running", err)
	}

	diff, err := w.d.Bitbucket.GetPullRequestDiff(ctx, p.workspaceSlug, p.repoSlug, p.bbID)
	if errors.Is(err, bitbucket.ErrNotFound) {
		return w.settle(ctx, id, "skipped", "pull request no longer available in Bitbucket")
	}
	if err != nil {
		return w.retryOrFail(ctx, job, wrap("fetch pull request diff", err))
	}

	started := time.Now()
	out, err := review.Run(ctx, w.d.Reviewer, review.Input{
		Repo: p.workspaceSlug + "/" + p.repoSlug, Commit: p.head, Author: p.author, Diff: diff, PullRequest: true,
		Message: strings.TrimSpace(p.title + "\n\n" + p.description),
	}, w.d.Limits)
	if errors.Is(err, review.ErrUnparseableDiff) {
		if serr := w.settle(ctx, id, "failed", err.Error()); serr != nil {
			return serr
		}
		return river.JobCancel(err)
	}
	if err != nil {
		return w.retryOrFail(ctx, job, err)
	}

	if !out.Reviewable {
		if _, err := w.d.Pool.Exec(ctx, `UPDATE pull_requests SET files_changed = $2, additions = $3, deletions = $4 WHERE id = $1`,
			id, out.Files, out.Additions, out.Deletions); err != nil {
			return wrap("record stats", err)
		}
		return w.settle(ctx, id, "skipped", skipReason(out))
	}
	moved, err := w.save(ctx, id, p.head, out, time.Since(started))
	if err != nil {
		return w.retryOrFail(ctx, job, wrap("save review", err))
	}
	w.d.Log.Info("pull request reviewed", "pull_request_id", id, "repo", p.workspaceSlug+"/"+p.repoSlug, "pr", p.bbID,
		"findings", len(out.Findings), "score", out.Score, "dropped", out.Dropped, "chunks", out.Chunks, "model", out.Model)
	if moved {
		w.d.Log.Info("pull request moved during its review; reviewing the new head", "pull_request_id", id)
		return river.JobSnooze(time.Second)
	}
	return nil
}

// save stores the review and settles the pull request, unless its head moved
// meanwhile: then it stays pending and moved is true.
func (w *reviewPullRequestWorker) save(ctx context.Context, id int64, head string, out review.Outcome, took time.Duration) (moved bool, err error) {
	tx, err := w.d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := insertReviewTx(ctx, tx, subject{prID: &id, prHead: head}, out, took); err != nil {
		return false, err
	}
	var status string
	if err := tx.QueryRow(ctx, `
		UPDATE pull_requests SET
			review_status = CASE WHEN COALESCE(source_hash, '') = $2 THEN 'done' ELSE 'pending' END,
			review_skip_reason = NULL, files_changed = $3, additions = $4, deletions = $5, updated_at = now()
		WHERE id = $1 RETURNING review_status`, id, head, out.Files, out.Additions, out.Deletions).Scan(&status); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return status == "pending", nil
}

func (w *reviewPullRequestWorker) retryOrFail(ctx context.Context, job *river.Job[jobs.ReviewPullRequestArgs], err error) error {
	id := job.Args.PullRequestID
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if d, ok := snoozeFor(err); ok {
		w.setStatus(bg, id, "pending", "") //nolint:errcheck
		w.d.Log.Warn("pull request review snoozed", "pull_request_id", id, "for", d, "reason", err)
		return river.JobSnooze(d)
	}
	if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
		w.setStatus(bg, id, "pending", "") //nolint:errcheck
		return river.JobSnooze(0)
	}
	if job.Attempt >= job.MaxAttempts {
		w.setStatus(bg, id, "failed", shorten(err.Error())) //nolint:errcheck
		w.d.Log.Error("pull request review failed permanently", "pull_request_id", id, "attempts", job.Attempt, "error", err)
		return err
	}
	w.setStatus(bg, id, "pending", "") //nolint:errcheck
	w.d.Log.Warn("pull request review attempt failed, will retry", "pull_request_id", id, "attempt", job.Attempt, "error", err)
	return err
}

func (w *reviewPullRequestWorker) setStatus(ctx context.Context, id int64, status, reason string) error {
	_, err := w.d.Pool.Exec(ctx, `UPDATE pull_requests SET review_status = $2, review_skip_reason = NULLIF($3, ''), updated_at = now() WHERE id = $1`, id, status, reason)
	return err
}

func (w *reviewPullRequestWorker) settle(ctx context.Context, id int64, status, reason string) error {
	if err := w.setStatus(ctx, id, status, shorten(reason)); err != nil {
		return wrap("set "+status, err)
	}
	return nil
}
