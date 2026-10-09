package worker

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
)

// recordAttempt stores what a review attempt that did not end in a saved
// review cost, so the dashboard counts money spent on answers that were thrown
// away (unusable output, a later chunk failing after earlier ones were paid
// for, a failed save). It never fails the job: losing a cost row is better
// than losing the retry.
//
// Not recorded: shutdown (the attempt did not really happen), and a provider
// limit that cost nothing.
func recordAttempt(ctx context.Context, pool *pgxpool.Pool, sub subject, attempt int, usage review.Usage, err error, took time.Duration) {
	if err == nil {
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
		return
	}
	if _, snoozed := snoozeFor(err); snoozed && !usage.Known {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = pool.Exec(bg, `
		INSERT INTO review_attempts (commit_id, pr_id, attempt, error, duration_ms, tokens_in, tokens_out, cost_usd)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sub.commitID, sub.prID, attempt, shorten(err.Error()), took.Milliseconds(),
		usageVal(usage, usage.InputTokens), usageVal(usage, usage.OutputTokens), usageVal(usage, usage.CostUSD))
}
