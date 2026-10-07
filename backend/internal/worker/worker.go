// Package worker holds the River workers: process_webhook (sync a push into
// the database and queue reviews) and review_commit (review one commit).
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/ingest"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

// Bitbucket is the part of the Bitbucket client the workers use.
type Bitbucket interface {
	ingest.CommitLister
	GetDiff(ctx context.Context, workspace, repo, hash string) (string, error)
}

// Deps are the workers' collaborators.
type Deps struct {
	Pool      *pgxpool.Pool
	Log       *slog.Logger
	Bitbucket Bitbucket
	Reviewer  review.Reviewer
	Limits    review.Limits

	// Tuning; zero values give the production defaults.
	WebhookWorkers int           // default 4
	PollInterval   time.Duration // how often idle queues look for jobs; default River's (1s)
}

const (
	// ReviewJobTimeout bounds one commit review (all its chunks).
	ReviewJobTimeout  = 10 * time.Minute
	webhookJobTimeout = 2 * time.Minute

	// A reason is stored on the commit and shown in the dashboard.
	maxReasonLen = 500
)

// NewClient builds the River client. Reviews run one at a time (the review
// queue has a single worker) while webhook processing runs in parallel.
func NewClient(d Deps) (*river.Client[pgx.Tx], error) {
	if d.Pool == nil || d.Bitbucket == nil || d.Reviewer == nil {
		return nil, errors.New("worker: Pool, Bitbucket and Reviewer are required")
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.WebhookWorkers <= 0 {
		d.WebhookWorkers = 4
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, &processWebhookWorker{d: d, syncer: store.NewSyncer(d.Pool)})
	river.AddWorker(workers, &reviewCommitWorker{d: d})

	return river.NewClient(riverpgxv5.New(d.Pool), &river.Config{
		Logger:  d.Log,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			jobs.QueueDefault: {MaxWorkers: d.WebhookWorkers},
			jobs.QueueReview:  {MaxWorkers: 1},
		},
		JobTimeout:        ReviewJobTimeout,
		FetchPollInterval: d.PollInterval,
	})
}

// snoozeFor converts "the other side told us to wait" into a River snooze,
// which reschedules the job without using up one of its attempts.
func snoozeFor(err error) (time.Duration, bool) {
	var ule *review.UsageLimitError
	if errors.As(err, &ule) {
		return ule.RetryAfter, true
	}
	var rle *bitbucket.RateLimitedError
	if errors.As(err, &rle) {
		return rle.RetryAfter, true
	}
	return 0, false
}

func shorten(s string) string {
	r := []rune(s)
	if len(r) > maxReasonLen {
		return string(r[:maxReasonLen])
	}
	return s
}

func wrap(what string, err error) error { return fmt.Errorf("%s: %w", what, err) }
