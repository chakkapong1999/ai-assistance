// Command server is the single backend binary. --mode (or the MODE env var)
// selects what it runs: the HTTP API, the job worker or the one-off backfill.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/httpapi"
	"github.com/chakkapong1999/ai-assistance/backend/internal/restapi"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	mode := flag.String("mode", os.Getenv("MODE"), "api | worker | backfill (default: $MODE)")
	flag.Parse()

	cfg, err := config.Load(*mode, os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch cfg.Mode {
	case config.ModeAPI:
		err = runAPI(ctx, log, cfg)
	case config.ModeWorker:
		err = runWorker(ctx, log, cfg)
	case config.ModeBackfill:
		err = runBackfill(ctx, log, cfg)
	}
	if err != nil {
		log.Error("exited with error", "error", err)
		os.Exit(1)
	}
}

func runAPI(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.CheckSchema(ctx, pool); err != nil {
		return err
	}
	// An insert-only River client: the API only enqueues; the worker runs jobs.
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return err
	}

	deps := httpapi.Deps{
		WebhookSecret: []byte(cfg.BitbucketWebhookSecret),
		Events:        store.NewEvents(pool, rc),
	}
	if h := restapi.New(store.NewDashboard(pool, rc), cfg.APITokens, log); h != nil {
		deps.Dashboard = h
	} else {
		log.Warn("API_TOKENS is not set: the dashboard REST API (/api/v1) is disabled")
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(deps),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("api listening", "addr", cfg.HTTPAddr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down api")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runWorker runs the River workers until a signal arrives, then stops
// gracefully: running jobs get a short grace period, then are cancelled and
// will be retried by the next worker start.
func runWorker(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.CheckSchema(ctx, pool); err != nil {
		return err
	}

	if cfg.BitbucketBaseURL != "" {
		log.Warn("Bitbucket API base URL is overridden; the token is sent there", "base_url", cfg.BitbucketBaseURL)
	}
	bb, err := bitbucket.New(bitbucket.Options{Token: cfg.BitbucketToken, BaseURL: cfg.BitbucketBaseURL})
	if err != nil {
		return err
	}
	reviewer, err := review.New(cfg)
	if err != nil {
		return err
	}
	deps := worker.Deps{Pool: pool, Log: log, Bitbucket: bb, Reviewer: reviewer, Limits: review.DefaultLimits(), ReviewNewRepos: cfg.ReviewNewRepos, ReconcileInterval: cfg.ReconcileInterval}
	if len(cfg.PollRepos) > 0 {
		deps.Poll = &worker.PollConfig{Repos: cfg.PollRepos, Interval: cfg.PollInterval, Lookback: cfg.PollLookback, Bitbucket: bb}
		log.Info("polling enabled", "repos", cfg.PollRepos, "interval", cfg.PollInterval, "lookback", cfg.PollLookback)
	}
	rc, err := worker.NewClient(deps)
	if err != nil {
		return err
	}
	if err := rc.Start(ctx); err != nil {
		return err
	}
	log.Info("worker started", "reviewer", cfg.ReviewerMode, "model", review.ModelName(cfg.ReviewerMode))

	<-ctx.Done()
	log.Info("worker stopping: waiting for running jobs")

	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := rc.Stop(stopCtx); err != nil {
		log.Warn("jobs still running after grace period; cancelling them", "error", err)
		cancelCtx, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel2()
		if err := rc.StopAndCancel(cancelCtx); err != nil {
			return err
		}
	}
	log.Info("worker stopped")
	return nil
}

// runBackfill reads the history of the POLL_REPOS repositories once and exits.
func runBackfill(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.CheckSchema(ctx, pool); err != nil {
		return err
	}
	bb, err := bitbucket.New(bitbucket.Options{Token: cfg.BitbucketToken, BaseURL: cfg.BitbucketBaseURL})
	if err != nil {
		return err
	}
	// Insert-only: the worker, not this process, runs any review that is queued.
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return err
	}
	log.Info("backfill started", "repos", cfg.PollRepos, "days", cfg.BackfillDays, "max_per_branch", cfg.BackfillMaxCommits, "review", cfg.BackfillReview)
	res, err := worker.Backfill(ctx, log, pool, bb, rc, worker.BackfillOptions{
		Repos: cfg.PollRepos, Days: cfg.BackfillDays, MaxPerBranch: cfg.BackfillMaxCommits,
		Review: cfg.BackfillReview, ReviewNewRepos: cfg.ReviewNewRepos,
	})
	log.Info("backfill finished", "repositories", res.Repositories, "failed", res.Failed, "new_commits", res.NewCommits, "queued", res.Queued, "skipped", res.Skipped)
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d repositories could not be backfilled; run it again after fixing them", res.Failed)
	}
	return nil
}
