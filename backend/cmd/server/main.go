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

	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/httpapi"
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
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(),
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

// runWorker only waits for a signal until the queue lands in M3.
func runWorker(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	log.Info("worker started", "reviewer", cfg.ReviewerMode)
	<-ctx.Done()
	log.Info("worker stopped")
	return nil
}
