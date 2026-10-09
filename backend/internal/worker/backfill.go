package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

const skipBackfill = "history, not reviewed"

// BackfillOptions say how far back to read and what to do with what is found.
type BackfillOptions struct {
	Repos        []string // "workspace/repo" or "workspace/*", as for polling
	Days         int      // how far back to read each branch
	MaxPerBranch int      // cap per branch, newest first
	// Review queues reviews for the commits found (where review is on for the
	// repository). Off, history is only recorded, so scores and activity are
	// complete without paying for a review of every old commit.
	Review         bool
	ReviewNewRepos bool
}

// BackfillResult is what a run did.
type BackfillResult struct {
	Repositories int
	Failed       int
	NewCommits   int
	Queued       int
	Skipped      int
}

// Backfill reads the history of the configured repositories once and stores
// it. It is safe to run again and to run next to the worker: commits already
// stored are left alone, every repository is stored in its own transaction,
// and a repository that cannot be read is logged without stopping the rest.
// rc only enqueues; it needs no workers.
func Backfill(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, bb PollBitbucket, rc *river.Client[pgx.Tx], o BackfillOptions) (BackfillResult, error) {
	var res BackfillResult
	if o.Days <= 0 || o.MaxPerBranch <= 0 {
		return res, fmt.Errorf("backfill: Days and MaxPerBranch must be positive")
	}
	syncer := store.NewSyncer(pool).ReviewNewRepos(o.ReviewNewRepos)
	repos, errs := expandRepos(ctx, bb, o.Repos)
	for _, e := range errs {
		res.Failed++
		log.Error("backfill cannot resolve a configured repository", "error", e)
	}
	since := time.Now().Add(-time.Duration(o.Days) * 24 * time.Hour)
	for _, meta := range repos {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.Repositories++
		// Cursors are ignored on purpose: this reads history, not what is new.
		in, heads, err := collectBranches(ctx, bb, meta, nil, since, o.MaxPerBranch)
		if err != nil {
			res.Failed++
			log.Error("backfill failed to read repository", "repo", meta.FullName, "error", err)
			continue
		}
		n, queued, skipped, err := backfillStore(ctx, pool, syncer, rc, meta.UUID, in, heads, o)
		if err != nil {
			res.Failed++
			log.Error("backfill failed to store repository", "repo", meta.FullName, "error", err)
			continue
		}
		res.NewCommits += n
		res.Queued += queued
		res.Skipped += skipped
		log.Info("backfilled repository", "repo", meta.FullName, "new_commits", n, "queued", queued, "skipped", skipped)
	}
	return res, nil
}

func backfillStore(ctx context.Context, pool *pgxpool.Pool, syncer *store.Syncer, rc *river.Client[pgx.Tx], repoUUID string, in store.PushInput, heads map[string]string, o BackfillOptions) (n, queued, skipped int, err error) {
	// A branch the poller already follows keeps its cursor, so the poller still
	// reads everything after it; commits stored twice are no-ops.
	cursors, err := syncer.PollCursors(ctx, repoUUID)
	if err != nil {
		return 0, 0, 0, err
	}
	merged := make(map[string]string, len(heads))
	for b, h := range heads {
		merged[b] = h
		if last, ok := cursors[b]; ok {
			merged[b] = last
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, 0, wrap("begin", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	push, err := syncer.SyncPushTx(ctx, tx, in)
	if err != nil {
		return 0, 0, 0, wrap("sync", err)
	}
	skipAll := ""
	if !o.Review {
		skipAll = skipBackfill
	}
	queued, skipped, err = settleWith(ctx, tx, rc, push, skipAll)
	if err != nil {
		return 0, 0, 0, err
	}
	if err := store.SavePollCursorsTx(ctx, tx, push.RepoID, merged); err != nil {
		return 0, 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, 0, wrap("commit", err)
	}
	return len(push.NewCommits), queued, skipped, nil
}
