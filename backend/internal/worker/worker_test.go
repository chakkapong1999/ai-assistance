package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/httpapi"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/testdb"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { testdb.Main(m, func(p *pgxpool.Pool) { testPool = p }) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// A diff with two reviewable files, a deleted file and a lockfile.
const diffText = `diff --git a/svc/user.go b/svc/user.go
--- a/svc/user.go
+++ b/svc/user.go
@@ -10,3 +10,4 @@ func Load() {
 	a := 1
-	b := 2
+	b := compute()
+	use(b)
 	c := 3
diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1,1 +0,0 @@
-package old
diff --git a/go.sum b/go.sum
--- a/go.sum
+++ b/go.sum
@@ -1,1 +1,1 @@
-a
+b
`

const lockOnlyDiff = "diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n@@ -1,1 +1,1 @@\n-a\n+b\n"

// fakeBB stands in for Bitbucket.
type fakeBB struct {
	mu        sync.Mutex
	diff      string
	diffErr   error
	listErr   error
	diffCalls int
}

func (f *fakeBB) GetDiff(context.Context, string, string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffCalls++
	return f.diff, f.diffErr
}

func (f *fakeBB) GetPullRequestDiff(context.Context, string, string, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffCalls++
	return f.diff, f.diffErr
}

func (f *fakeBB) ListCommits(context.Context, string, string, string, string) ([]webhook.Commit, error) {
	return nil, f.listErr
}

func (f *fakeBB) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.diffCalls
}

func count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func str(t *testing.T, q string, args ...any) string {
	t.Helper()
	var s *string
	if err := testPool.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if s == nil {
		return "<null>"
	}
	return *s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func reset(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`TRUNCATE workspaces, users, webhook_events, river_job RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repo_push.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// enableRepoFromFixture creates the fixture's repo and switches review on.
func enableRepoFromFixture(t *testing.T) {
	t.Helper()
	ev, err := webhook.ParsePush(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewSyncer(testPool).SyncPush(context.Background(), store.PushInput{Repo: ev.Repository}); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE repositories SET review_enabled = true`); err != nil {
		t.Fatal(err)
	}
}

// startClient runs a real River client (both queues) against the test DB.
func startClient(t *testing.T, rv review.Reviewer, bb Bitbucket) *store.Events {
	t.Helper()
	rc, err := NewClient(Deps{Pool: testPool, Log: quiet, Bitbucket: bb, Reviewer: rv, PollInterval: 120 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := rc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := rc.Stop(stop); err != nil {
			cancel()
			rc.StopAndCancel(stop) //nolint:errcheck
		}
		cancel()
	})
	return store.NewEvents(testPool, rc)
}

func deliver(t *testing.T, ev *store.Events, uuid string, payload []byte) {
	t.Helper()
	if _, err := ev.Ingest(context.Background(), httpapi.Event{
		RequestUUID: uuid, EventKey: "repo:push", Payload: payload, Enqueue: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookToReviewEndToEnd(t *testing.T) {
	reset(t)
	enableRepoFromFixture(t)
	bb := &fakeBB{diff: diffText}
	ev := startClient(t, review.Mock{Scenario: config.ScenarioFindings}, bb)

	deliver(t, ev, "e2e", fixture(t))

	waitFor(t, "both commits settled", func() bool {
		return count(t, `SELECT count(*) FROM commits WHERE review_status IN ('done','skipped')`) == 2
	})

	if got := str(t, `SELECT review_status FROM commits WHERE hash='2222222'`); got != "done" {
		t.Fatalf("normal commit status = %s", got)
	}
	if got, reason := str(t, `SELECT review_status FROM commits WHERE hash='3333333'`), str(t, `SELECT review_skip_reason FROM commits WHERE hash='3333333'`); got != "skipped" || reason != "merge commit" {
		t.Fatalf("merge commit: %s / %s", got, reason)
	}
	if bb.calls() != 1 {
		t.Fatalf("diff fetched %d times, want 1 (merge commit must not be reviewed)", bb.calls())
	}

	if n := count(t, `SELECT count(*) FROM reviews WHERE model='mock' AND prompt_version=$1 AND score=91`, review.PromptVersion); n != 1 {
		t.Fatalf("expected one mock review with score 91 (100-7-2), got %d", n)
	}
	if n := count(t, `SELECT count(*) FROM review_findings`); n != 2 {
		t.Fatalf("findings = %d, want 2", n)
	}
	if n := count(t, `SELECT count(*) FROM review_findings WHERE code_context LIKE '@@ %'`); n != 2 {
		t.Fatalf("findings with code_context = %d, want 2", n)
	}
	if got := str(t, `SELECT severity FROM review_findings ORDER BY id LIMIT 1`); got != "major" {
		t.Fatalf("first finding severity = %s (findings must be stored most severe first)", got)
	}
	if n := count(t, `SELECT count(*) FROM code_suggestions WHERE unified_diff LIKE '%@@ -11,1 +11,1 @@%'`); n != 1 {
		t.Fatalf("suggestion with unified diff missing (got %d)", n)
	}
	// Stats count every file in the commit, including the ones that were not reviewed.
	if n := count(t, `SELECT count(*) FROM commits WHERE hash='2222222' AND files_changed=3 AND additions=3 AND deletions=3`); n != 1 {
		t.Fatalf("commit stats wrong: files/additions/deletions = %s", str(t, `SELECT files_changed||'/'||additions||'/'||deletions FROM commits WHERE hash='2222222'`))
	}
	if count(t, `SELECT count(*) FROM webhook_events WHERE processed_at IS NOT NULL`) != 1 {
		t.Fatal("event not marked processed")
	}
	// River records completion just after Work returns, so wait for it.
	waitFor(t, "review job completed", func() bool {
		return count(t, `SELECT count(*) FROM river_job WHERE kind='review_commit' AND state='completed'`) == 1
	})
}

func TestDisabledRepoNeverReachesTheReviewer(t *testing.T) {
	reset(t) // repo is created by the webhook itself, with review_enabled = false
	bb := &fakeBB{diff: diffText}
	ev := startClient(t, review.Mock{Scenario: config.ScenarioFindings}, bb)

	deliver(t, ev, "disabled", fixture(t))

	waitFor(t, "event processed", func() bool {
		return count(t, `SELECT count(*) FROM webhook_events WHERE processed_at IS NOT NULL`) == 1
	})
	if n := count(t, `SELECT count(*) FROM commits WHERE review_status='skipped' AND review_skip_reason='repo review disabled'`); n != 2 {
		t.Fatalf("skipped commits = %d, want 2", n)
	}
	if count(t, `SELECT count(*) FROM river_job WHERE kind='review_commit'`) != 0 || count(t, `SELECT count(*) FROM reviews`) != 0 || bb.calls() != 0 {
		t.Fatal("a repo that nobody approved must never be fetched or reviewed")
	}
}

func TestUsageLimitSnoozesTheJobAndKeepsTheCommitPending(t *testing.T) {
	reset(t)
	enableRepoFromFixture(t)
	ev := startClient(t, review.Mock{Scenario: config.ScenarioUsageLimit}, &fakeBB{diff: diffText})

	deliver(t, ev, "limit", fixture(t))

	waitFor(t, "review job snoozed", func() bool {
		return count(t, `SELECT count(*) FROM river_job WHERE kind='review_commit' AND state='scheduled'`) == 1
	})
	if got := str(t, `SELECT review_status FROM commits WHERE hash='2222222'`); got != "pending" {
		t.Fatalf("status after snooze = %s, want pending", got)
	}
	if n := count(t, `SELECT count(*) FROM river_job WHERE kind='review_commit' AND scheduled_at > now() + interval '10 seconds'`); n != 1 {
		t.Fatal("job should be rescheduled into the future")
	}
	if n := count(t, `SELECT count(*) FROM river_job WHERE kind='review_commit' AND cardinality(errors) > 0`); n != 0 {
		t.Fatal("a usage limit is not an error and must not be recorded as one")
	}
	if count(t, `SELECT count(*) FROM reviews`) != 0 {
		t.Fatal("no review may be stored")
	}
}

// --- direct calls to the review worker ---------------------------------------

type reviewFn func(context.Context, review.Request) (review.Result, error)

func (f reviewFn) Review(ctx context.Context, r review.Request) (review.Result, error) {
	return f(ctx, r)
}

// oneCommit creates the fixture repo with review on and one pending commit.
func oneCommit(t *testing.T) int64 {
	t.Helper()
	reset(t)
	ev, _ := webhook.ParsePush(fixture(t))
	c := ev.Push.Changes[0].Commits[1] // 2222222, not a merge
	res, err := store.NewSyncer(testPool).SyncPush(context.Background(), store.PushInput{
		Repo: ev.Repository, Branches: []store.BranchCommits{{Branch: "feature/x", Commits: []webhook.Commit{c}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE repositories SET review_enabled = true`); err != nil {
		t.Fatal(err)
	}
	return res.NewCommits[0].ID
}

func reviewJob(id int64, attempt, max int) *river.Job[jobs.ReviewCommitArgs] {
	return &river.Job[jobs.ReviewCommitArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: max},
		Args:   jobs.ReviewCommitArgs{CommitID: id},
	}
}

func newReviewWorker(rv review.Reviewer, bb Bitbucket) *reviewCommitWorker {
	return &reviewCommitWorker{d: Deps{Pool: testPool, Log: quiet, Bitbucket: bb, Reviewer: rv}}
}

func status(t *testing.T, id int64) string {
	return str(t, `SELECT review_status||'|'||COALESCE(review_skip_reason,'') FROM commits WHERE id=$1`, id)
}

func TestInvalidOutputRetriesThenFailsOnTheLastAttempt(t *testing.T) {
	id := oneCommit(t)
	w := newReviewWorker(review.Mock{Scenario: config.ScenarioInvalidJSON}, &fakeBB{diff: diffText})

	err := w.Work(context.Background(), reviewJob(id, 1, 5))
	if !errors.Is(err, review.ErrInvalidOutput) {
		t.Fatalf("attempt 1 err = %v, want ErrInvalidOutput (so River retries)", err)
	}
	if got := status(t, id); got != "pending|" {
		t.Fatalf("after attempt 1: %s; must not be 'failed' while retries remain", got)
	}

	err = w.Work(context.Background(), reviewJob(id, 5, 5))
	if !errors.Is(err, review.ErrInvalidOutput) {
		t.Fatalf("last attempt err = %v", err)
	}
	if got := status(t, id); got[:6] != "failed" || len(got) < 10 {
		t.Fatalf("after last attempt: %q, want failed with a reason", got)
	}
}

func TestBitbucketConditions(t *testing.T) {
	var snooze *rivertype.JobSnoozeError
	var cancel *rivertype.JobCancelError

	id := oneCommit(t)
	w := newReviewWorker(review.Mock{Scenario: config.ScenarioFindings}, &fakeBB{diffErr: bitbucket.ErrNotFound})
	if err := w.Work(context.Background(), reviewJob(id, 1, 5)); err != nil {
		t.Fatalf("missing commit should settle quietly, got %v", err)
	}
	if got := status(t, id); got != "skipped|commit no longer available in Bitbucket" {
		t.Fatalf("status = %q", got)
	}

	id = oneCommit(t)
	w = newReviewWorker(review.Mock{Scenario: config.ScenarioFindings}, &fakeBB{diffErr: &bitbucket.RateLimitedError{RetryAfter: 7 * time.Minute}})
	err := w.Work(context.Background(), reviewJob(id, 1, 5))
	if !errors.As(err, &snooze) || snooze.Duration != 7*time.Minute {
		t.Fatalf("rate limit err = %v, want a 7m snooze", err)
	}
	if got := status(t, id); got != "pending|" {
		t.Fatalf("status = %q", got)
	}

	id = oneCommit(t)
	w = newReviewWorker(review.Mock{Scenario: config.ScenarioFindings}, &fakeBB{diff: "this is not a diff"})
	err = w.Work(context.Background(), reviewJob(id, 1, 5))
	if !errors.As(err, &cancel) {
		t.Fatalf("unparseable diff err = %v, want a cancel (retrying cannot help)", err)
	}
	if got := status(t, id); got[:6] != "failed" {
		t.Fatalf("status = %q", got)
	}
}

func TestNothingReviewableIsSkippedWithAReason(t *testing.T) {
	id := oneCommit(t)
	w := newReviewWorker(failIfCalled(t), &fakeBB{diff: lockOnlyDiff})
	if err := w.Work(context.Background(), reviewJob(id, 1, 5)); err != nil {
		t.Fatal(err)
	}
	if got := status(t, id); got != "skipped|no reviewable files: lockfile" {
		t.Fatalf("status = %q", got)
	}
	if count(t, `SELECT count(*) FROM commits WHERE id=$1 AND files_changed=1 AND additions=1 AND deletions=1`, id) != 1 {
		t.Fatal("stats should still be recorded for a skipped commit")
	}
}

func failIfCalled(t *testing.T) review.Reviewer {
	return reviewFn(func(context.Context, review.Request) (review.Result, error) {
		t.Error("reviewer must not be called")
		return review.Result{}, errors.New("called")
	})
}

func TestSettledCommitsAreNotReviewedTwice(t *testing.T) {
	id := oneCommit(t)
	bb := &fakeBB{diff: diffText}
	w := newReviewWorker(review.Mock{Scenario: config.ScenarioFindings}, bb)

	for i := 0; i < 2; i++ {
		if err := w.Work(context.Background(), reviewJob(id, 1, 5)); err != nil {
			t.Fatal(err)
		}
	}
	if count(t, `SELECT count(*) FROM reviews`) != 1 || bb.calls() != 1 {
		t.Fatalf("reviews=%d diff fetches=%d, want 1 and 1", count(t, `SELECT count(*) FROM reviews`), bb.calls())
	}
}

func TestRepoDisabledAfterQueueingIsHonoured(t *testing.T) {
	id := oneCommit(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE repositories SET review_enabled = false`); err != nil {
		t.Fatal(err)
	}
	bb := &fakeBB{diff: diffText}
	if err := newReviewWorker(failIfCalled(t), bb).Work(context.Background(), reviewJob(id, 1, 5)); err != nil {
		t.Fatal(err)
	}
	if got := status(t, id); got != "skipped|repo review disabled" || bb.calls() != 0 {
		t.Fatalf("status=%q diff fetches=%d", got, bb.calls())
	}
}

func TestShutdownDoesNotSpendAnAttemptOrMarkFailure(t *testing.T) {
	id := oneCommit(t)
	started := make(chan struct{})
	rv := reviewFn(func(ctx context.Context, _ review.Request) (review.Result, error) {
		close(started)
		<-ctx.Done()
		return review.Result{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newReviewWorker(rv, &fakeBB{diff: diffText}).Work(ctx, reviewJob(id, 5, 5)) }()
	<-started
	cancel()

	var snooze *rivertype.JobSnoozeError
	if err := <-done; !errors.As(err, &snooze) || snooze.Duration != 0 {
		t.Fatalf("err = %v, want an immediate snooze", err)
	}
	if got := status(t, id); got != "pending|" {
		t.Fatalf("status = %q; an interrupted review on its last attempt must not become 'failed'", got)
	}
}

func TestModelTextWithNULBytesIsStored(t *testing.T) {
	id := oneCommit(t)
	rv := reviewFn(func(_ context.Context, r review.Request) (review.Result, error) {
		res, err := review.ParseOutput([]byte(`{"summary":"s\u0000x","findings":[{"file":"svc/user.go","line_start":11,"severity":"minor","title":"t\u0000","explanation":"e\u0000"}]}`))
		res.Model = "test"
		return res, err
	})
	if err := newReviewWorker(rv, &fakeBB{diff: diffText}).Work(context.Background(), reviewJob(id, 1, 5)); err != nil {
		t.Fatal(err)
	}
	if got := status(t, id); got != "done|" {
		t.Fatalf("status = %q", got)
	}
}

// --- direct calls to the webhook worker --------------------------------------

func insertEvent(t *testing.T, payload string) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(),
		`INSERT INTO webhook_events (request_uuid, event_key, payload) VALUES ('d-'||gen_random_uuid(), 'repo:push', $1::jsonb) RETURNING id`, payload).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func webhookJob(id int64) *river.Job[jobs.ProcessWebhookArgs] {
	return &river.Job[jobs.ProcessWebhookArgs]{JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 5}, Args: jobs.ProcessWebhookArgs{EventID: id}}
}

const truncatedPush = `{"repository":{"uuid":"{r}","name":"svc","full_name":"acme/svc","workspace":{"uuid":"{w}","slug":"acme","name":"Acme"}},
 "push":{"changes":[{"truncated":true,"old":{"type":"branch","name":"f","target":{"hash":"o"}},"new":{"type":"branch","name":"f","target":{"hash":"n"}},"commits":[{"hash":"n"}]}]}}`

func TestProcessWebhookFailureModes(t *testing.T) {
	reset(t)
	var snooze *rivertype.JobSnoozeError
	var cancel *rivertype.JobCancelError
	mk := func(bb Bitbucket) *processWebhookWorker {
		return &processWebhookWorker{d: Deps{Pool: testPool, Log: quiet, Bitbucket: bb}, syncer: store.NewSyncer(testPool)}
	}

	// Bitbucket down: error out so River retries; nothing is half-written.
	id := insertEvent(t, truncatedPush)
	if err := mk(&fakeBB{listErr: errors.New("bitbucket down")}).Work(context.Background(), webhookJob(id)); err == nil {
		t.Fatal("expected an error")
	}
	if count(t, `SELECT count(*) FROM webhook_events WHERE id=$1 AND processed_at IS NULL`, id) != 1 || count(t, `SELECT count(*) FROM repositories`) != 0 {
		t.Fatal("a failed attempt must leave no trace")
	}

	// Rate limited: snooze for as long as Bitbucket asked.
	err := mk(&fakeBB{listErr: &bitbucket.RateLimitedError{RetryAfter: 3 * time.Minute}}).Work(context.Background(), webhookJob(id))
	if !errors.As(err, &snooze) || snooze.Duration != 3*time.Minute {
		t.Fatalf("err = %v, want a 3m snooze", err)
	}

	// A payload that can never parse is cancelled, not retried.
	bad := insertEvent(t, `{"nothing":"useful"}`)
	if err := mk(&fakeBB{}).Work(context.Background(), webhookJob(bad)); !errors.As(err, &cancel) {
		t.Fatalf("err = %v, want a cancel", err)
	}
	// So is a job whose event vanished.
	if err := mk(&fakeBB{}).Work(context.Background(), webhookJob(999999)); !errors.As(err, &cancel) {
		t.Fatalf("err = %v, want a cancel", err)
	}

	// An already processed event is a no-op.
	done := insertEvent(t, truncatedPush)
	if _, err := testPool.Exec(context.Background(), `UPDATE webhook_events SET processed_at = now() WHERE id=$1`, done); err != nil {
		t.Fatal(err)
	}
	if err := mk(&fakeBB{listErr: errors.New("must not be called")}).Work(context.Background(), webhookJob(done)); err != nil {
		t.Fatalf("processed event should be skipped, got %v", err)
	}
}

var _ = pgx.ErrNoRows

// The review queue must never run two reviews at once (they share one local
// Claude CLI and its usage quota).
func TestReviewsRunOneAtATime(t *testing.T) {
	reset(t)
	enableRepoFromFixture(t)
	var running, peak, calls atomic.Int32
	rv := reviewFn(func(ctx context.Context, r review.Request) (review.Result, error) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		calls.Add(1)
		time.Sleep(150 * time.Millisecond)
		return review.Result{Model: "test"}, nil
	})
	ev := startClient(t, rv, &fakeBB{diff: diffText})

	payload := `{"repository":{"uuid":"{r-1}","name":"loan-service","full_name":"acme/loan-service",
	  "workspace":{"uuid":"{w-1}","slug":"acme","name":"Acme"}},
	  "push":{"changes":[{"old":{"type":"branch","name":"f","target":{"hash":"o"}},"new":{"type":"branch","name":"f","target":{"hash":"c4"}},
	  "commits":[{"hash":"c1","parents":[{"hash":"p"}]},{"hash":"c2","parents":[{"hash":"p"}]},{"hash":"c3","parents":[{"hash":"p"}]},{"hash":"c4","parents":[{"hash":"p"}]}]}]}}`
	deliver(t, ev, "serial", []byte(payload))

	waitFor(t, "all four reviewed", func() bool {
		return count(t, `SELECT count(*) FROM commits WHERE review_status='done'`) == 4
	})
	if peak.Load() != 1 || calls.Load() != 4 {
		t.Fatalf("peak concurrency = %d (want 1), reviewer calls = %d (want 4)", peak.Load(), calls.Load())
	}
}

type usageRV struct{ review.Mock }

func (u usageRV) Review(ctx context.Context, r review.Request) (review.Result, error) {
	res, err := u.Mock.Review(ctx, r)
	res.Usage = review.Usage{InputTokens: 1234, OutputTokens: 56, CostUSD: 0.0789, Known: true}
	return res, err
}

func TestReviewStoresUsage(t *testing.T) {
	reset(t)
	enableRepoFromFixture(t)
	ev := startClient(t, usageRV{review.Mock{Scenario: config.ScenarioFindings}}, &fakeBB{diff: diffText})
	deliver(t, ev, "usage", fixture(t))
	waitFor(t, "reviewed", func() bool { return count(t, `SELECT count(*) FROM reviews`) == 1 })
	if n := count(t, `SELECT count(*) FROM reviews WHERE tokens_in = 1234 AND tokens_out = 56 AND cost_usd = 0.0789`); n != 1 {
		t.Fatalf("usage not stored: %d", n)
	}
}

func TestMockReviewLeavesUsageNull(t *testing.T) {
	reset(t)
	enableRepoFromFixture(t)
	ev := startClient(t, review.Mock{Scenario: config.ScenarioFindings}, &fakeBB{diff: diffText})
	deliver(t, ev, "nousage", fixture(t))
	waitFor(t, "reviewed", func() bool { return count(t, `SELECT count(*) FROM reviews`) == 1 })
	if n := count(t, `SELECT count(*) FROM reviews WHERE tokens_in IS NULL AND tokens_out IS NULL AND cost_usd IS NULL`); n != 1 {
		t.Fatalf("mock usage must be NULL, not zero: %d", n)
	}
}
