// Command rivermigrate applies (or reverts) River's own schema. River owns
// its tables, so they are migrated with its migrator rather than with our SQL
// files:
//
//	DATABASE_URL=... go run ./cmd/rivermigrate up
//	DATABASE_URL=... go run ./cmd/rivermigrate down   # one step
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rivermigrate:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		return fmt.Errorf("usage: rivermigrate up|down (DATABASE_URL must be set)")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()

	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	dir, opts := rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{}
	if os.Args[1] == "down" {
		dir, opts = rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{MaxSteps: 1}
	}
	res, err := m.Migrate(ctx, dir, opts)
	if err != nil {
		return err
	}
	for _, v := range res.Versions {
		fmt.Printf("%s river migration %03d %s\n", res.Direction, v.Version, v.Name)
	}
	if len(res.Versions) == 0 {
		fmt.Println("river schema already up to date")
	}
	return nil
}
