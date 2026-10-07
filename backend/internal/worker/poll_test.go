package worker

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/mockbitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

type poller struct {
	t    *testing.T
	mock *mockbitbucket.Mock
	rc   *river.Client[pgx.Tx]
	bb   *bitbucket.Client
}

// pollReviewNewRepos is what the next startPoller gives the worker as
// Deps.ReviewNewRepos; tests that need it on set it and restore it.
var pollReviewNewRepos bool

// startPoller runs a real worker client that polls the in-memory Bitbucket.
// The timer is an hour, so rounds happen only at start and when poll() asks.
func startPoller(t *testing.T, m *mockbitbucket.Mock, repos []string, wrap func(*bitbucket.Client) PollBitbucket) *poller {
	t.Helper()
	return startPollerWith(t, m, repos, wrap, review.Mock{Scenario: config.ScenarioFindings})
}

func startPollerWith(t *testing.T, m *mockbitbucket.Mock, repos []string, wrap func(*bitbucket.Client) PollBitbucket, rv review.Reviewer) *poller {
	t.Helper()
	reset(t)
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	bb, err := bitbucket.New(bitbucket.Options{Token: "t", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var pb PollBitbucket = bb
	if wrap != nil {
		pb = wrap(bb)
	}
	rc, err := NewClient(Deps{
		Pool: testPool, Log: quiet, Bitbucket: bb, Reviewer: rv, ReviewNewRepos: pollReviewNewRepos,
		PollInterval: 120 * time.Millisecond,
		Poll:         &PollConfig{Repos: repos, Interval: time.Hour, Lookback: 7 * 24 * time.Hour, Bitbucket: pb},
	})
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
	return &poller{t: t, mock: m, rc: rc, bb: bb}
}

func pollsFinished() int {
	var n int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind = 'poll_repos' AND state IN ('completed','discarded','cancelled')`).Scan(&n)
	return n
}

// waitRounds waits until n polling rounds have finished since the client started.
func (p *poller) waitRounds(n int) {
	p.t.Helper()
	waitFor(p.t, "poll rounds to finish", func() bool { return pollsFinished() >= n })
}

// poll triggers one more round and waits for it.
func (p *poller) poll(prev int) int {
	p.t.Helper()
	if _, err := p.rc.Insert(context.Background(), jobs.PollReposArgs{}, nil); err != nil {
		p.t.Fatal(err)
	}
	p.waitRounds(prev + 1)
	return prev + 1
}

func hashesOf(t *testing.T, q string) map[string]string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

func TestFirstPollReadsLookbackAndEveryBranch(t *testing.T) {
	m := mockbitbucket.New("")
	now := time.Now()
	old := m.AddCommitAt("acme", "api", "main", "ancient", now.Add(-10*24*time.Hour))
	m1 := m.AddCommitAt("acme", "api", "main", "main one", now.Add(-3*time.Hour))
	m2 := m.AddCommitAt("acme", "api", "main", "main two", now.Add(-2*time.Hour))
	f1 := m.AddCommitAt("acme", "api", "feature/x", "feature one", now.Add(-time.Hour))
	f2 := m.AddCommitAt("acme", "api", "feature/x", "feature two", now.Add(-30*time.Minute))

	p := startPoller(t, m, []string{"acme/api"}, nil)
	p.waitRounds(1)

	got := hashesOf(t, `SELECT hash, COALESCE(branch,'') FROM commits`)
	want := map[string]string{m1: "main", m2: "main", f1: "feature/x", f2: "feature/x"}
	if len(got) != len(want) {
		t.Fatalf("commits = %v, want %v (the 10-day-old commit is outside the lookback)", got, want)
	}
	for h, b := range want {
		if got[h] != b {
			t.Errorf("commit %s on %q, want %q", h[:7], got[h], b)
		}
	}
	if _, ok := got[old]; ok {
		t.Error("a commit older than the lookback was stored")
	}
	if n := count(t, `SELECT count(*) FROM repositories WHERE slug='api' AND default_branch='main' AND review_enabled = false`); n != 1 {
		t.Fatalf("repository not created with main branch and review off: %d", n)
	}
	// Review is off for a new repository, so nothing is queued or sent anywhere.
	if n := count(t, `SELECT count(*) FROM commits WHERE review_status='skipped' AND review_skip_reason='repo review disabled'`); n != 4 {
		t.Fatalf("skipped = %d, want 4", n)
	}
	if count(t, `SELECT count(*) FROM river_job WHERE kind='review_commit'`) != 0 || p.mock.Calls("diff") != 0 {
		t.Fatal("review jobs or diff fetches for a repository with review off")
	}
	cur := hashesOf(t, `SELECT branch, last_hash FROM poll_cursors`)
	if len(cur) != 2 || cur["main"] != m2 || cur["feature/x"] != f2 {
		t.Fatalf("cursors = %v", cur)
	}
}

func TestLaterPollsReadOnlyWhatIsNewAndReviewIt(t *testing.T) {
	m := mockbitbucket.New("")
	m.AddCommit("acme", "api", "main", "first")
	p := startPoller(t, m, []string{"acme/api"}, nil)
	rounds := 1
	p.waitRounds(rounds)
	testPool.Exec(context.Background(), `UPDATE repositories SET review_enabled = true`)

	// Nothing changed: branches are listed, commits are not.
	commitsBefore := m.Calls("commits")
	touched := str(t, `SELECT max(updated_at)::text FROM poll_cursors`)
	rounds = p.poll(rounds)
	if after := str(t, `SELECT max(updated_at)::text FROM poll_cursors`); after != touched {
		t.Fatalf("an idle round wrote to the database (cursor updated_at %s -> %s)", touched, after)
	}
	if got := m.Calls("commits"); got != commitsBefore {
		t.Fatalf("an unchanged repository cost %d commit requests", got-commitsBefore)
	}

	prevHead := hashesOf(t, `SELECT branch, last_hash FROM poll_cursors`)["main"]
	h := m.AddCommit("acme", "api", "main", "second")
	rounds = p.poll(rounds)
	qs := m.Queries("commits")
	if last := qs[len(qs)-1]; !strings.Contains(last, "include="+h) || !strings.Contains(last, "exclude="+prevHead) {
		t.Fatalf("the round after a push asked for %q; want only what is new (include=%s exclude=%s)", last, h, prevHead)
	}
	waitFor(t, "new commit reviewed", func() bool {
		return count(t, `SELECT count(*) FROM commits WHERE hash=$1 AND review_status='done'`, h) == 1
	})
	if n := count(t, `SELECT count(*) FROM commits`); n != 2 {
		t.Fatalf("commits = %d, want 2 (the first one must not be read again)", n)
	}
	if got := hashesOf(t, `SELECT branch, last_hash FROM poll_cursors`)["main"]; got != h {
		t.Fatalf("cursor = %s, want %s", got, h)
	}
	if n := count(t, `SELECT count(*) FROM reviews`); n != 1 {
		t.Fatalf("reviews = %d, want 1 (only the commit found after review was enabled)", n)
	}

	// A new branch contributes only what main does not have.
	f := m.AddCommit("acme", "api", "feature/y", "on a branch")
	p.poll(rounds)
	if got := hashesOf(t, `SELECT hash, COALESCE(branch,'') FROM commits`); len(got) != 3 || got[f] != "feature/y" {
		t.Fatalf("commits after new branch = %v", got)
	}
	qs = m.Queries("commits")
	if last := qs[len(qs)-1]; !strings.Contains(last, "include="+f) || !strings.Contains(last, "exclude="+h) {
		t.Fatalf("a new branch asked for %q; want only what main lacks (include=%s exclude=%s)", last, f, h)
	}
}

func TestPollAndWebhookDoNotDoubleCount(t *testing.T) {
	m := mockbitbucket.New("")
	h := m.AddCommit("acme", "api", "main", "pushed")
	p := startPoller(t, m, []string{"acme/api"}, nil)
	p.waitRounds(1)
	testPool.Exec(context.Background(), `UPDATE repositories SET review_enabled = true`)

	// The same commit now arrives as a webhook (hash already known).
	ev := webhook.PushEvent{}
	ev.Repository = webhook.Repository{UUID: "{repo-acme-api}", FullName: "acme/api", Workspace: &webhook.Workspace{UUID: "{ws-acme}", Slug: "acme"}}
	if _, err := store.NewSyncer(testPool).SyncPush(context.Background(), store.PushInput{
		Repo: ev.Repository, Branches: []store.BranchCommits{{Branch: "main", Commits: []webhook.Commit{{Hash: h}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, `SELECT count(*) FROM commits WHERE hash=$1`, h); n != 1 {
		t.Fatalf("same commit stored %d times", n)
	}
}

func TestWholeWorkspaceAndUnknownRepoDoNotStopTheRound(t *testing.T) {
	m := mockbitbucket.New("")
	m.AddCommit("acme", "api", "main", "a")
	m.AddCommit("acme", "web", "main", "b")
	m.AddCommit("other", "lib", "main", "c")
	p := startPoller(t, m, []string{"acme/*", "other/missing", "other/lib"}, nil)
	p.waitRounds(1)
	if n := count(t, `SELECT count(*) FROM repositories`); n != 3 {
		t.Fatalf("repositories = %d, want 3 (acme/api, acme/web, other/lib)", n)
	}
	if n := count(t, `SELECT count(*) FROM commits`); n != 3 {
		t.Fatalf("commits = %d, want 3", n)
	}
	// A missing repository is logged, not a failed job that River retries.
	if n := count(t, `SELECT count(*) FROM river_job WHERE kind='poll_repos' AND state='completed'`); n != 1 {
		t.Fatalf("poll job states: completed=%d", n)
	}
}

// Commits and cursors move together: if the cursor cannot be saved, nothing is
// stored, and the next round reads the same commits again.
func TestCursorAndCommitsAreOneTransaction(t *testing.T) {
	m := mockbitbucket.New("")
	h := m.AddCommit("acme", "api", "main", "a")
	ctx := context.Background()

	reset(t)
	if _, err := testPool.Exec(ctx, `ALTER TABLE poll_cursors RENAME TO poll_cursors_off`); err != nil {
		t.Fatal(err)
	}
	restore := func() { testPool.Exec(ctx, `ALTER TABLE IF EXISTS poll_cursors_off RENAME TO poll_cursors`) }
	t.Cleanup(restore)

	p := startPoller(t, m, []string{"acme/api"}, nil) // reset() inside is fine: the rename survives it
	p.waitRounds(1)
	if n := count(t, `SELECT count(*) FROM commits`); n != 0 {
		t.Fatalf("commits stored (%d) although the cursor could not be saved", n)
	}
	restore()
	p.poll(1)
	if n := count(t, `SELECT count(*) FROM commits WHERE hash=$1`, h); n != 1 {
		t.Fatalf("commit not picked up on the next round: %d", n)
	}
}

func TestDeletedBranchCursorIsForgotten(t *testing.T) {
	m := mockbitbucket.New("")
	m.AddCommit("acme", "api", "main", "a")
	m.AddCommit("acme", "api", "tmp", "b")
	p := startPoller(t, m, []string{"acme/api"}, nil)
	p.waitRounds(1)
	if n := count(t, `SELECT count(*) FROM poll_cursors`); n != 2 {
		t.Fatalf("cursors = %d, want 2", n)
	}
	m.DeleteBranch("acme", "api", "tmp")
	p.poll(1)
	if got := hashesOf(t, `SELECT branch, last_hash FROM poll_cursors`); len(got) != 1 || got["main"] == "" {
		t.Fatalf("cursors after delete = %v", got)
	}
}

type limitedBB struct{ *bitbucket.Client }

func (limitedBB) ListBranches(context.Context, string, string) ([]bitbucket.Branch, error) {
	return nil, &bitbucket.RateLimitedError{RetryAfter: time.Hour}
}

func TestRateLimitSnoozesTheRound(t *testing.T) {
	m := mockbitbucket.New("")
	m.AddCommit("acme", "api", "main", "a")
	startPoller(t, m, []string{"acme/api"}, func(c *bitbucket.Client) PollBitbucket { return limitedBB{c} })
	waitFor(t, "round snoozed", func() bool {
		return count(t, `SELECT count(*) FROM river_job WHERE kind='poll_repos' AND state='scheduled' AND scheduled_at > now() + interval '30 minutes'`) == 1
	})
	if n := count(t, `SELECT count(*) FROM river_job WHERE kind='poll_repos' AND state IN ('discarded','retryable')`); n != 0 {
		t.Fatal("a rate limit must not count as a failed attempt")
	}
}

func TestPollJobIsCancelledWhenPollingIsOff(t *testing.T) {
	reset(t)
	rc, err := NewClient(Deps{Pool: testPool, Log: quiet, Bitbucket: &fakeBB{}, Reviewer: review.Mock{}, PollInterval: 120 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { stop, c := context.WithTimeout(context.Background(), 5*time.Second); defer c(); rc.Stop(stop) }() //nolint:errcheck
	if _, err := rc.Insert(ctx, jobs.PollReposArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stale poll job cancelled", func() bool {
		return count(t, `SELECT count(*) FROM river_job WHERE kind='poll_repos' AND state='cancelled'`) == 1
	})
}

func TestPollReviewsANewRepositoryFromTheFirstRoundWhenConfigured(t *testing.T) {
	pollReviewNewRepos = true
	t.Cleanup(func() { pollReviewNewRepos = false })
	m := mockbitbucket.New("")
	h := m.AddCommit("acme", "api", "main", "first")
	p := startPoller(t, m, []string{"acme/api"}, nil)
	p.waitRounds(1)

	if count(t, `SELECT count(*) FROM repositories WHERE review_enabled`) != 1 {
		t.Fatal("the new repository did not start with review on")
	}
	waitFor(t, "first commit reviewed", func() bool {
		return count(t, `SELECT count(*) FROM commits WHERE hash = $1 AND review_status = 'done'`, h) == 1
	})
}
