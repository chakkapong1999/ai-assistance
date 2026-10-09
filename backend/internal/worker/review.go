package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
)

type reviewCommitWorker struct {
	river.WorkerDefaults[jobs.ReviewCommitArgs]
	d Deps
}

func (w *reviewCommitWorker) Timeout(*river.Job[jobs.ReviewCommitArgs]) time.Duration {
	return ReviewJobTimeout
}

type commitRow struct {
	hash, message, status, repoSlug, workspaceSlug, author string
	reviewEnabled                                          bool
}

// Work reviews one commit and stores the result.
//
// commits.review_status is the single source of truth:
// pending -> running -> done | skipped | failed. Retriable problems put it
// back to pending; only the last failed attempt sets failed, so the dashboard
// never shows "failed" for something that is still going to be retried.
func (w *reviewCommitWorker) Work(ctx context.Context, job *river.Job[jobs.ReviewCommitArgs]) error {
	id := job.Args.CommitID

	var c commitRow
	err := w.d.Pool.QueryRow(ctx, `
		SELECT c.hash, c.message, c.review_status, r.slug, ws.slug, r.review_enabled,
		       COALESCE(u.display_name, c.author_raw, '')
		FROM commits c
		JOIN repositories r ON r.id = c.repo_id
		JOIN projects p ON p.id = r.project_id
		JOIN workspaces ws ON ws.id = p.workspace_id
		LEFT JOIN users u ON u.id = c.author_user_id
		WHERE c.id = $1`, id).
		Scan(&c.hash, &c.message, &c.status, &c.repoSlug, &c.workspaceSlug, &c.reviewEnabled, &c.author)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(fmt.Errorf("commit %d does not exist", id))
	}
	if err != nil {
		return wrap("load commit", err)
	}
	if c.status == "done" || c.status == "skipped" {
		return nil // already settled; do not review twice
	}
	if !c.reviewEnabled {
		// Switched off after the job was queued: honour it, send nothing.
		return w.settle(ctx, id, "skipped", skipRepoDisabled)
	}

	if err := w.setStatus(ctx, id, "running", ""); err != nil {
		return wrap("mark running", err)
	}

	diff, err := w.d.Bitbucket.GetDiff(ctx, c.workspaceSlug, c.repoSlug, c.hash)
	if errors.Is(err, bitbucket.ErrNotFound) {
		// Typically a force-push removed the commit.
		return w.settle(ctx, id, "skipped", "commit no longer available in Bitbucket")
	}
	if err != nil {
		return w.retryOrFail(ctx, job, wrap("fetch diff", err))
	}

	started := time.Now()
	out, err := review.Run(ctx, w.d.Reviewer, review.Input{
		Repo: c.workspaceSlug + "/" + c.repoSlug, Commit: c.hash, Message: c.message, Author: c.author, Diff: diff,
	}, w.d.Limits)
	if errors.Is(err, review.ErrUnparseableDiff) {
		// Deterministic: another attempt cannot help.
		if serr := w.settle(ctx, id, "failed", err.Error()); serr != nil {
			return serr
		}
		return river.JobCancel(err)
	}
	if err != nil {
		recordAttempt(ctx, w.d.Pool, subject{commitID: &id}, job.Attempt, out.Usage, err, time.Since(started))
		return w.retryOrFail(ctx, job, err)
	}

	if !out.Reviewable {
		if err := w.recordStats(ctx, id, out); err != nil {
			return wrap("record stats", err)
		}
		return w.settle(ctx, id, "skipped", skipReason(out))
	}
	if err := w.save(ctx, id, out, time.Since(started)); err != nil {
		err = wrap("save review", err)
		recordAttempt(ctx, w.d.Pool, subject{commitID: &id}, job.Attempt, out.Usage, err, time.Since(started))
		return w.retryOrFail(ctx, job, err)
	}
	w.d.Log.Info("commit reviewed", "commit_id", id, "hash", c.hash, "findings", len(out.Findings),
		"score", out.Score, "dropped", out.Dropped, "chunks", out.Chunks, "model", out.Model)
	return nil
}

// retryOrFail decides what a failed attempt means for the commit.
func (w *reviewCommitWorker) retryOrFail(ctx context.Context, job *river.Job[jobs.ReviewCommitArgs], err error) error {
	id := job.Args.CommitID
	// Detached context: the job's own may already be cancelled or timed out.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if d, ok := snoozeFor(err); ok {
		w.setStatus(bg, id, "pending", "") //nolint:errcheck
		w.d.Log.Warn("review snoozed", "commit_id", id, "for", d, "reason", err)
		return river.JobSnooze(d)
	}
	if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
		// Shutting down: not the commit's fault, so do not spend an attempt.
		// (A job that hits its own timeout is different: that counts, or a
		// commit that always times out would be retried forever.)
		w.setStatus(bg, id, "pending", "") //nolint:errcheck
		return river.JobSnooze(0)
	}
	if job.Attempt >= job.MaxAttempts {
		w.setStatus(bg, id, "failed", shorten(err.Error())) //nolint:errcheck
		w.d.Log.Error("review failed permanently", "commit_id", id, "attempts", job.Attempt, "error", err)
		return err
	}
	w.setStatus(bg, id, "pending", "") //nolint:errcheck
	w.d.Log.Warn("review attempt failed, will retry", "commit_id", id, "attempt", job.Attempt, "error", err)
	return err
}

func (w *reviewCommitWorker) setStatus(ctx context.Context, id int64, status, reason string) error {
	_, err := w.d.Pool.Exec(ctx, `UPDATE commits SET review_status = $2, review_skip_reason = NULLIF($3, '') WHERE id = $1`, id, status, reason)
	return err
}

func (w *reviewCommitWorker) settle(ctx context.Context, id int64, status, reason string) error {
	if err := w.setStatus(ctx, id, status, shorten(reason)); err != nil {
		return wrap("set "+status, err)
	}
	return nil
}

func (w *reviewCommitWorker) recordStats(ctx context.Context, id int64, out review.Outcome) error {
	_, err := w.d.Pool.Exec(ctx, `UPDATE commits SET files_changed = $2, additions = $3, deletions = $4 WHERE id = $1`,
		id, out.Files, out.Additions, out.Deletions)
	return err
}

// skipReason explains, in one line, why nothing was reviewed.
func skipReason(out review.Outcome) string {
	if len(out.Skipped) == 0 {
		return "empty commit"
	}
	set := map[string]bool{}
	for _, s := range out.Skipped {
		set[s.Reason] = true
	}
	reasons := make([]string, 0, len(set))
	for r := range set {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	return "no reviewable files: " + strings.Join(reasons, ", ")
}

// save stores the review, its findings and suggestions, and settles the commit
// as done, atomically.
func (w *reviewCommitWorker) save(ctx context.Context, id int64, out review.Outcome, took time.Duration) error {
	tx, err := w.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := insertReviewTx(ctx, tx, subject{commitID: &id}, out, took); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE commits SET review_status = 'done', review_skip_reason = NULL,
			files_changed = $2, additions = $3, deletions = $4
		WHERE id = $1`, id, out.Files, out.Additions, out.Deletions); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// subject is what a review is about: a commit, or a pull request at a head.
type subject struct {
	commitID *int64
	prID     *int64
	prHead   string
}

// insertReviewTx writes one review with its findings and code suggestions.
func insertReviewTx(ctx context.Context, tx pgx.Tx, sub subject, out review.Outcome, took time.Duration) error {
	var reviewID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO reviews (commit_id, pr_id, pr_head_hash, model, prompt_version, score, summary, duration_ms, tokens_in, tokens_out, cost_usd)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		sub.commitID, sub.prID, sub.prHead, out.Model, review.PromptVersion, out.Score, out.Summary, took.Milliseconds(),
		usageVal(out.Usage, out.Usage.InputTokens), usageVal(out.Usage, out.Usage.OutputTokens), usageVal(out.Usage, out.Usage.CostUSD),
	).Scan(&reviewID); err != nil {
		return err
	}
	for _, f := range out.Findings {
		var fid int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO review_findings (review_id, file_path, line_start, line_end, severity, category, title, explanation, code_context)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')) RETURNING id`,
			reviewID, f.FilePath, f.LineStart, f.LineEnd, f.Severity, f.Category, f.Title, f.Explanation, f.CodeContext,
		).Scan(&fid); err != nil {
			return err
		}
		if s := f.Suggestion; s != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO code_suggestions (finding_id, original_snippet, suggested_snippet, unified_diff)
				VALUES ($1, $2, $3, $4)`, fid, s.Original, s.Suggested, s.UnifiedDiff); err != nil {
				return err
			}
		}
	}
	return nil
}

// usageVal is v when the reviewer reported usage and NULL otherwise, so the
// dashboard can tell "free" from "not measured".
func usageVal[T int | float64](u review.Usage, v T) any {
	if !u.Known {
		return nil
	}
	return v
}
