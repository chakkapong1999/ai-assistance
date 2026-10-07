// Package testdb gives integration tests a clean, fully migrated Postgres.
//
// Set TEST_DATABASE_URL to an EMPTY database whose name ends in "_test": its
// public schema is dropped on every run, so the suffix is checked. Without the
// variable, Main exits 0 and the package's tests are skipped.
//
// `go test ./...` runs packages in parallel, and they share one database, so
// Main holds a Postgres advisory lock for the whole package run; packages
// queue up instead of dropping each other's tables.
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

const lockKey = 727271

// Main is meant to be called from TestMain. onReady receives the pool before
// any test runs.
func Main(m *testing.M, onReady func(*pgxpool.Pool)) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Println("TEST_DATABASE_URL not set: skipping Postgres integration tests")
		os.Exit(0)
	}
	os.Exit(run(m, url, onReady))
}

func run(m *testing.M, url string, onReady func(*pgxpool.Pool)) int {
	ctx := context.Background()

	lock, err := pgx.Connect(ctx, url)
	if err != nil {
		return fail("connect for lock: %v", err)
	}
	defer lock.Close(ctx)
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fail("advisory lock: %v", err)
	}

	pool, err := store.Open(ctx, url)
	if err != nil {
		return fail("open: %v", err)
	}
	defer pool.Close()

	var db string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil || !strings.HasSuffix(db, "_test") {
		return fail("refusing to run: database %q does not end in _test (%v)", db, err)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		return fail("reset schema: %v", err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations", "*.up.sql"))
	if err != nil || len(files) == 0 {
		return fail("no migrations found (%v)", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			return fail("read migration: %v", err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fail("apply %s: %v", filepath.Base(f), err)
		}
	}
	mig, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err == nil {
		_, err = mig.Migrate(ctx, rivermigrate.DirectionUp, nil)
	}
	if err != nil {
		return fail("river migrate: %v", err)
	}

	onReady(pool)
	return m.Run()
}

func fail(format string, args ...any) int {
	fmt.Printf("testdb: "+format+"\n", args...)
	return 1
}
