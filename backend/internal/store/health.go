package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
)

// What counts as a problem. Review and webhook jobs normally start within
// seconds; these are generous so a busy day does not raise a false alarm.
const (
	healthStaleEvent    = 15 * time.Minute
	healthStaleJob      = 30 * time.Minute
	healthReconcileSeen = 30 * time.Minute
)

type HealthWebhooks struct {
	Unprocessed      int  `json:"unprocessed"`
	OldestAgeSeconds *int `json:"oldest_age_seconds"`
}

type HealthJobs struct {
	Waiting              int  `json:"waiting"`
	Running              int  `json:"running"`
	Retrying             int  `json:"retrying"`
	Snoozed              int  `json:"snoozed"`
	Discarded24h         int  `json:"discarded_24h"`
	OldestWaitingSeconds *int `json:"oldest_waiting_seconds"`
}

type HealthStuck struct {
	Commits      int `json:"commits"`
	PullRequests int `json:"pull_requests"`
}

type HealthPoll struct {
	At time.Time `json:"at"`
	OK bool      `json:"ok"`
}

type HealthReconcile struct {
	At           time.Time `json:"at"`
	Events       int       `json:"events"`
	Commits      int       `json:"commits"`
	PullRequests int       `json:"pull_requests"`
	GaveUp       int       `json:"gave_up"`
}

// Health is the answer to "is the pipeline moving?": what is waiting, what has
// lost its job, and when the worker last did its periodic work.
type Health struct {
	Status        string           `json:"status"` // ok | degraded
	Problems      []string         `json:"problems"`
	CheckedAt     time.Time        `json:"checked_at"`
	Webhooks      HealthWebhooks   `json:"webhooks"`
	Jobs          HealthJobs       `json:"jobs"`
	Stuck         HealthStuck      `json:"stuck"`
	LastPoll      *HealthPoll      `json:"last_poll"`
	LastReconcile *HealthReconcile `json:"last_reconcile"`
}

var healthKinds = []string{"process_webhook", "review_commit", "review_pull_request"}

func (d *Dashboard) Health(ctx context.Context) (Health, error) {
	h := Health{Problems: []string{}, CheckedAt: time.Now().UTC()}

	err := d.pool.QueryRow(ctx, `
		SELECT count(*)::int, EXTRACT(EPOCH FROM now() - min(received_at))::int
		FROM webhook_events WHERE processed_at IS NULL`).Scan(&h.Webhooks.Unprocessed, &h.Webhooks.OldestAgeSeconds)
	if err != nil {
		return h, fmt.Errorf("health webhooks: %w", err)
	}

	err = d.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state IN ('available', 'pending'))::int,
		       count(*) FILTER (WHERE state = 'running')::int,
		       count(*) FILTER (WHERE state = 'retryable')::int,
		       count(*) FILTER (WHERE state = 'scheduled')::int,
		       count(*) FILTER (WHERE state = 'discarded' AND finalized_at > now() - interval '24 hours')::int,
		       EXTRACT(EPOCH FROM now() - min(scheduled_at) FILTER (WHERE state = 'available'))::int
		FROM river_job WHERE kind = ANY($1)`, healthKinds).
		Scan(&h.Jobs.Waiting, &h.Jobs.Running, &h.Jobs.Retrying, &h.Jobs.Snoozed, &h.Jobs.Discarded24h, &h.Jobs.OldestWaitingSeconds)
	if err != nil {
		return h, fmt.Errorf("health jobs: %w", err)
	}
	if h.Jobs.OldestWaitingSeconds != nil && *h.Jobs.OldestWaitingSeconds < 0 {
		zero := 0
		h.Jobs.OldestWaitingSeconds = &zero
	}

	// The same test the reconciler uses, so "stuck" is exactly what its next pass will fix.
	args := []any{jobs.LiveStates(), int(jobs.ReconcileSettle.Seconds()), int(jobs.ReconcileGrace.Seconds())}
	err = d.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM commits c
		        WHERE c.review_status IN ('pending', 'running') AND c.created_at < now() - make_interval(secs => $3::int)`+
		jobs.NoLiveJobSQL("review_commit", "commit_id", "c.id", false)+`)::int,
		       (SELECT count(*) FROM pull_requests p
		        WHERE p.state = 'OPEN' AND p.review_status IN ('pending', 'running') AND p.updated_at < now() - make_interval(secs => $3::int)`+
		jobs.NoLiveJobSQL("review_pull_request", "pull_request_id", "p.id", false)+`)::int`, args...).
		Scan(&h.Stuck.Commits, &h.Stuck.PullRequests)
	if err != nil {
		return h, fmt.Errorf("health stuck: %w", err)
	}

	var at time.Time
	var state string
	err = d.pool.QueryRow(ctx, `
		SELECT finalized_at, state::text FROM river_job
		WHERE kind = 'poll_repos' AND state IN ('completed', 'discarded') ORDER BY finalized_at DESC LIMIT 1`).Scan(&at, &state)
	switch {
	case err == nil:
		h.LastPoll = &HealthPoll{At: at, OK: state == "completed"}
	case !errors.Is(err, pgx.ErrNoRows):
		return h, fmt.Errorf("health poll: %w", err)
	}

	var out []byte
	err = d.pool.QueryRow(ctx, `
		SELECT finalized_at, metadata->'output' FROM river_job
		WHERE kind = 'reconcile' AND state = 'completed' ORDER BY finalized_at DESC LIMIT 1`).Scan(&at, &out)
	switch {
	case err == nil:
		r := HealthReconcile{At: at}
		_ = json.Unmarshal(out, &r) // an older or missing output just leaves the counts at zero
		r.At = at
		h.LastReconcile = &r
	case !errors.Is(err, pgx.ErrNoRows):
		return h, fmt.Errorf("health reconcile: %w", err)
	}

	h.problems()
	return h, nil
}

func age(sec int) string {
	d := time.Duration(sec) * time.Second
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1f h", d.Hours())
}

// problems turns the numbers into sentences a person can act on.
func (h *Health) problems() {
	add := func(format string, a ...any) { h.Problems = append(h.Problems, fmt.Sprintf(format, a...)) }
	if w := h.Webhooks; w.Unprocessed > 0 && w.OldestAgeSeconds != nil && time.Duration(*w.OldestAgeSeconds)*time.Second > healthStaleEvent {
		add("%d webhook deliveries have waited %s without being processed. Check that the worker is running.", w.Unprocessed, age(*w.OldestAgeSeconds))
	}
	if s := h.Jobs.OldestWaitingSeconds; s != nil && time.Duration(*s)*time.Second > healthStaleJob {
		add("A job has been waiting %s to start. Check that the worker is running.", age(*s))
	}
	if n := h.Stuck.Commits + h.Stuck.PullRequests; n > 0 {
		add("%d commits or pull requests are marked as waiting but have no job. The worker queues them again on its next check.", n)
	}
	if h.Jobs.Discarded24h > 0 {
		add("%d jobs gave up after repeated failures in the last 24 hours. Look for failed commits and pull requests.", h.Jobs.Discarded24h)
	}
	if h.LastPoll != nil && !h.LastPoll.OK {
		add("The last polling round failed. The worker log says why.")
	}
	if r := h.LastReconcile; r != nil && h.CheckedAt.Sub(r.At) > healthReconcileSeen {
		add("The worker's background check last ran %s ago. Is the worker running?", age(int(h.CheckedAt.Sub(r.At).Seconds())))
	}
	h.Status = "ok"
	if len(h.Problems) > 0 {
		h.Status = "degraded"
	}
}
