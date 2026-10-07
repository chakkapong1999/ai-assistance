// Command server is the single backend binary. --mode (or the MODE env var)
// selects what it runs: the HTTP API, the job worker or the one-off backfill.
package main

import (
	"context"
	"errors"
	"flag"
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
		log.Info("backfill is not implemented yet (M2)")
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

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			WebhookSecret: []byte(cfg.BitbucketWebhookSecret),
			Events:        store.NewEvents(pool, rc),
		}),
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

	bb, err := bitbucket.New(bitbucket.Options{Token: cfg.BitbucketToken})
	if err != nil {
		return err
	}
	reviewer, err := review.New(cfg)
	if err != nil {
		return err
	}
	rc, err := worker.NewClient(worker.Deps{
		Pool: pool, Log: log, Bitbucket: bb, Reviewer: reviewer, Limits: review.DefaultLimits(),
	})
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
