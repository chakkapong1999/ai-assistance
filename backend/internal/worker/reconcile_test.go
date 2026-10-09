package worker

import (
	"context"
	"fmt"
	"github.com/riverqueue/river"
	"testing"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
)

// blockedReviewer never answers, so a review job that gets queued stays where
// the test can see it.
var blockedReviewer = reviewFn(func(ctx context.Context, _ review.Request) (review.Result, error) {
	<-ctx.Done()
	return review.Result{}, ctx.Err()
})

// reconcileClient starts a real client and returns a function that runs one
// reconcile pass and waits for it to finish.
func reconcileClient(t *testing.T) func() {
	t.Helper()
	rc, err := NewClient(Deps{Pool: testPool, Log: quiet, Bitbucket: &fakeBB{diff: diffText}, Reviewer: blockedReviewer, PollInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := rc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		stop, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		rc.Stop(stop) //nolint:errcheck
	})
	return func() {
		t.Helper()
		before := count(t, `SELECT count(*) FROM river_job WHERE kind = 'reconcile' AND state = 'completed'`)
		if _, err := rc.Insert(ctx, jobs.ReconcileArgs{}, nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "reconcile pass", func() bool {
			return count(t, `SELECT count(*) FROM river_job WHERE kind = 'reconcile' AND state = 'completed'`) > before
		})
	}
}

// addJob puts a job row in the given state. finishedAgo only matters for
// states that are final.
func addJob(t *testing.T, kind, argKey string, id int64, state string, finishedAgo time.Duration) {
	t.Helper()
	q := `INSERT INTO river_job (kind, args, state, max_attempts, queue, finalized_at)
	      VALUES ($1, jsonb_build_object($2::text, $3::bigint), $4::river_job_state, 5, 'default',
	              CASE WHEN $4::text IN ('completed','discarded','cancelled') THEN now() - make_interval(secs => $5::int) END)`
	if _, err := testPool.Exec(context.Background(), q, kind, argKey, id, state, int(finishedAgo.Seconds())); err != nil {
		t.Fatal(err)
	}
}

func jobsFor(t *testing.T, kind, argKey string, id int64) int {
	return count(t, fmt.Sprintf(`SELECT count(*) FROM river_job WHERE kind = $1 AND (args->>'%s')::bigint = $2`, argKey), kind, id)
}

func TestReconcileQueuesWebhookEventsThatLostTheirJob(t *testing.T) {
	reset(t)
	old := func(payload string) int64 {
		id := insertEvent(t, payload)
		if _, err := testPool.Exec(context.Background(), `UPDATE webhook_events SET received_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	lost := old(`{}`)
	young := insertEvent(t, `{}`) // just arrived: its job has probably not started
	exhausted := old(`{}`)
	for i := 0; i < jobs.ReconcileMaxJobs; i++ {
		addJob(t, "process_webhook", "event_id", exhausted, "discarded", time.Hour)
	}
	poison := old(`{}`)
	addJob(t, "process_webhook", "event_id", poison, "cancelled", time.Hour)
	waiting := old(`{}`)
	addJob(t, "process_webhook", "event_id", waiting, "retryable", 0)

	pass := reconcileClient(t)
	pass()

	for _, c := range []struct {
		name string
		id   int64
		want int
	}{{"lost", lost, 1}, {"young", young, 0}, {"exhausted", exhausted, jobs.ReconcileMaxJobs}, {"poison", poison, 1}, {"waiting", waiting, 1}} {
		if got := jobsFor(t, "process_webhook", "event_id", c.id); got != c.want {
			t.Errorf("%s event has %d jobs, want %d", c.name, got, c.want)
		}
	}

	// The queued job cannot parse `{}` and cancels itself; a second pass must
	// not keep queueing it.
	waitFor(t, "event job cancelled", func() bool {
		return count(t, `SELECT count(*) FROM river_job WHERE kind = 'process_webhook' AND state = 'cancelled' AND (args->>'event_id')::bigint = $1`, lost) == 1
	})
	pass()
	if got := jobsFor(t, "process_webhook", "event_id", lost); got != 1 {
		t.Fatalf("a cancelled event was queued again: %d jobs", got)
	}
}

// stuckCommits creates a repository with review on and n commits, all marked
// as stuck (old, no job).
func stuckCommits(t *testing.T, status string, n int) []int64 {
	t.Helper()
	id0 := oneCommit(t)
	ids := []int64{id0}
	for i := 1; i < n; i++ {
		var id int64
		if err := testPool.QueryRow(context.Background(), `
			INSERT INTO commits (repo_id, hash, committed_at, review_status)
			SELECT repo_id, 'extra'||$1::int::text, now(), 'pending' FROM commits WHERE id = $2 RETURNING id`, i, id0).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := testPool.Exec(context.Background(),
		`UPDATE commits SET review_status = $1, created_at = now() - interval '1 hour'`, status); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestReconcileQueuesCommitsThatLostTheirJob(t *testing.T) {
	ids := stuckCommits(t, "running", 6)
	lost, live, justEnded, exhausted, disabledRepo, young := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]
	addJob(t, "review_commit", "commit_id", live, "available", 0)
	addJob(t, "review_commit", "commit_id", justEnded, "completed", time.Minute)
	for i := 0; i < jobs.ReconcileMaxJobs; i++ {
		addJob(t, "review_commit", "commit_id", exhausted, "discarded", time.Hour)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE commits SET created_at = now() WHERE id = $1`, young); err != nil {
		t.Fatal(err)
	}
	// Review switched off for the repository after the commit was queued.
	if _, err := testPool.Exec(context.Background(), `
		WITH r AS (INSERT INTO repositories (project_id, bb_uuid, slug, name, review_enabled)
		           SELECT project_id, '{off}', 'off', 'off', false FROM repositories LIMIT 1 RETURNING id)
		UPDATE commits SET repo_id = (SELECT id FROM r) WHERE id = $1`, disabledRepo); err != nil {
		t.Fatal(err)
	}

	pass := reconcileClient(t)
	pass()

	if got := jobsFor(t, "review_commit", "commit_id", lost); got != 1 {
		t.Errorf("lost commit has %d jobs, want 1", got)
	}
	if got := status(t, lost); got != "pending|" && got != "running|" {
		t.Errorf("lost commit status = %q, want it back in the queue", got)
	}
	if got := jobsFor(t, "review_commit", "commit_id", live); got != 1 {
		t.Errorf("commit with a live job has %d jobs, want 1 (no second job)", got)
	}
	if got := jobsFor(t, "review_commit", "commit_id", justEnded); got != 1 {
		t.Errorf("commit whose job just ended has %d jobs, want 1 (still settling)", got)
	}
	if got := jobsFor(t, "review_commit", "commit_id", young); got != 0 {
		t.Errorf("young commit has %d jobs, want 0", got)
	}
	if got := status(t, exhausted); got != "failed|"+giveUpReason {
		t.Errorf("exhausted commit status = %q", got)
	}
	if got := jobsFor(t, "review_commit", "commit_id", exhausted); got != jobs.ReconcileMaxJobs {
		t.Errorf("exhausted commit got another job (%d)", got)
	}
	if got := status(t, disabledRepo); got != "skipped|"+skipRepoDisabled {
		t.Errorf("commit of a repository switched off = %q", got)
	}

	pass() // idempotent
	if got := jobsFor(t, "review_commit", "commit_id", lost); got != 1 {
		t.Errorf("second pass queued the commit again: %d jobs", got)
	}
}

func TestReconcileQueuesPullRequestsThatLostTheirJob(t *testing.T) {
	ids := stuckCommits(t, "done", 1)
	_ = ids
	mk := func(num int, state, status string, age string) int64 {
		var id int64
		if err := testPool.QueryRow(context.Background(), fmt.Sprintf(`
			INSERT INTO pull_requests (repo_id, bb_pr_id, title, state, review_status, updated_at)
			SELECT repo_id, $1, 'pr', $2, $3, now() - interval '%s' FROM commits LIMIT 1 RETURNING id`, age), num, state, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	lost := mk(1, "OPEN", "running", "1 hour")
	merged := mk(2, "MERGED", "running", "1 hour")
	young := mk(3, "OPEN", "pending", "1 minute")
	done := mk(4, "OPEN", "done", "1 hour")

	pass := reconcileClient(t)
	pass()
	pass()

	if got := jobsFor(t, "review_pull_request", "pull_request_id", lost); got != 1 {
		t.Errorf("lost pull request has %d jobs, want exactly 1 after two passes", got)
	}
	for name, id := range map[string]int64{"merged": merged, "young": young, "done": done} {
		if got := jobsFor(t, "review_pull_request", "pull_request_id", id); got != 0 {
			t.Errorf("%s pull request was queued (%d jobs)", name, got)
		}
	}
}

// A job carries a unique key. Once it has finished, a later reconcile (or
// "Review again") must still be able to queue the commit: the finished row
// must not hold the key. Jobs made before ReviewCommitArgs stopped counting
// finished jobs still hold it, and are released when the commit is requeued.
func TestReconcileRequeuesACommitWhoseEarlierJobFinished(t *testing.T) {
	cases := map[string]*river.InsertOpts{
		"job made by the current code": nil,
		"job made by the old code":     {UniqueOpts: river.UniqueOpts{ByArgs: true}}, // default states include completed
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			id := stuckCommits(t, "pending", 1)[0]
			rc, err := NewClient(Deps{Pool: testPool, Log: quiet, Bitbucket: &fakeBB{diff: diffText}, Reviewer: blockedReviewer})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rc.Insert(context.Background(), jobs.ReviewCommitArgs{CommitID: id}, opts); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(context.Background(),
				`UPDATE river_job SET state = 'completed', finalized_at = now() - interval '1 hour' WHERE kind = 'review_commit'`); err != nil {
				t.Fatal(err)
			}

			reconcileClient(t)()

			if got := jobsFor(t, "review_commit", "commit_id", id); got != 2 {
				t.Fatalf("commit has %d jobs, want 2: the finished one and a new one", got)
			}
		})
	}
}
