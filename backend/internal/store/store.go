// Package store holds the Postgres-backed implementations.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open connects and verifies the connection.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

// CheckSchema fails fast, with an actionable message, when migrations have
// not been applied. Without it the first webhook would fail with a 500 and
// Bitbucket would retry.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	for _, c := range []struct{ table, hint string }{
		{"webhook_events", "run `make migrate`"},
		{"poll_cursors", "run `make migrate` (migration 0002)"},
		{"river_job", "run `make migrate-river`"},
	} {
		var found *string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, c.table).Scan(&found); err != nil {
			return fmt.Errorf("store: check table %s: %w", c.table, err)
		}
		if found == nil {
			return fmt.Errorf("store: table %q is missing: %s", c.table, c.hint)
		}
	}
	// Migration 0003 only adds columns, so a table check cannot see it.
	var has bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_schema = current_schema() AND table_name = 'pull_requests' AND column_name = 'source_hash')`).Scan(&has); err != nil {
		return fmt.Errorf("store: check pull_requests: %w", err)
	}
	if !has {
		return errors.New("store: pull_requests is out of date: run `make migrate` (migration 0003)")
	}
	return nil
}
