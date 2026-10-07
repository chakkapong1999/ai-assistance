package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

const (
	// maxOpenPRs bounds how many open pull requests one round reads per repository.
	maxOpenPRs = 100
	// maxPRLookups bounds the single-PR requests used to find out what became of
	// pull requests that are no longer in the open list.
	maxPRLookups = 20
)

// pollPullRequests brings one repository's pull requests up to date and queues
// a review for every open one whose source branch moved.
//
// It reads the open list (one request), stores what changed, then looks up the
// pull requests that were open last time and are not any more (merged,
// declined or deleted) so their state stays right. A round where nothing
// changed makes no writes.
func (w *pollReposWorker) pollPullRequests(ctx context.Context, p *PollConfig, meta webhook.Repository) error {
	repo, ok, err := w.syncer.PRRepo(ctx, meta.UUID)
	if err != nil {
		return wrap("find repository", err)
	}
	if !ok {
		return nil // nothing synced for this repository yet
	}
	ws, slug := "", meta.Slug()
	if meta.Workspace != nil {
		ws = meta.Workspace.Slug
	}

	open, err := p.Bitbucket.ListOpenPullRequests(ctx, ws, slug, maxOpenPRs)
	if err != nil {
		return fmt.Errorf("list pull requests: %w", err)
	}
	tracked, err := w.syncer.TrackedOpenPRs(ctx, repo.ID)
	if err != nil {
		return err
	}

	var gone []webhook.PullRequest // closed since the last round
	var vanished []int             // Bitbucket no longer knows them
	if len(open) < maxOpenPRs {    // a cut-short list proves nothing about what is missing
		listed := make(map[int]bool, len(open))
		for _, pr := range open {
			listed[pr.ID] = true
		}
		looked := 0
		for id := range tracked {
			if listed[id] {
				continue
			}
			if looked++; looked > maxPRLookups {
				break // the rest next round
			}
			pr, err := p.Bitbucket.GetPullRequest(ctx, ws, slug, id)
			if errors.Is(err, bitbucket.ErrNotFound) {
				vanished = append(vanished, id)
				continue
			}
			if err != nil {
				return fmt.Errorf("pull request %d: %w", id, err)
			}
			gone = append(gone, pr)
		}
	}

	pending, err := w.syncer.PendingPRs(ctx, repo.ID)
	if err != nil {
		return err
	}
	rc, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return wrap("river client", err)
	}
	tx, err := w.d.Pool.Begin(ctx)
	if err != nil {
		return wrap("begin", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	res, err := w.syncer.SyncPullRequestsTx(ctx, tx, repo, append(open, gone...), time.Now().Add(-w.d.Poll.Lookback))
	if err != nil {
		return err
	}
	if res.Changed == 0 && len(gone) == 0 && len(vanished) == 0 && len(pending) == 0 {
		return nil // idle round: nothing was written, the deferred rollback ends the transaction
	}
	for _, id := range vanished {
		if err := store.MarkPullRequestGoneTx(ctx, tx, repo.ID, id); err != nil {
			return wrap("mark pull request gone", err)
		}
	}
	// Pending ones are included so a pull request whose job was lost (a crash
	// between steps) gets another; the uniqueness of the job makes that safe.
	queue := append(res.NeedReview, pending...)
	queued := map[int64]bool{}
	for _, id := range queue {
		if queued[id] {
			continue
		}
		queued[id] = true
		if _, err := rc.InsertTx(ctx, tx, jobs.ReviewPullRequestArgs{PullRequestID: id}, nil); err != nil {
			return wrap("enqueue pull request review", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return wrap("commit", err)
	}
	if res.Changed > 0 || len(gone) > 0 || len(vanished) > 0 {
		w.d.Log.Info("poll updated pull requests", "repo", meta.FullName, "changed", res.Changed,
			"closed", len(gone)+len(vanished), "queued", len(queued))
	}
	return nil
}
