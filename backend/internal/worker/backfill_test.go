package worker

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/mockbitbucket"
)

func backfillSetup(t *testing.T, m *mockbitbucket.Mock) (*bitbucket.Client, *river.Client[pgx.Tx]) {
	t.Helper()
	reset(t)
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	bb, err := bitbucket.New(bitbucket.Options{Token: "t", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := river.NewClient(riverpgxv5.New(testPool), &river.Config{}) // insert-only, like the real command
	if err != nil {
		t.Fatal(err)
	}
	return bb, rc
}

func TestBackfillRecordsHistoryWithoutReviewingIt(t *testing.T) {
	now := time.Now()
	m := mockbitbucket.New("")
	m.AddCommitAt("acme", "api", "main", "ancient", now.Add(-200*24*time.Hour))
	m1 := m.AddCommitAt("acme", "api", "main", "last month", now.Add(-30*24*time.Hour))
	m2 := m.AddCommitAt("acme", "api", "main", "yesterday", now.Add(-24*time.Hour))
	f1 := m.AddCommitAt("acme", "api", "feature/x", "on a branch", now.Add(-12*time.Hour))
	bb, rc := backfillSetup(t, m)

	res, err := Backfill(context.Background(), quiet, testPool, bb, rc, BackfillOptions{Repos: []string{"acme/*"}, Days: 90, MaxPerBranch: 100, ReviewNewRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Repositories != 1 || res.NewCommits != 3 || res.Skipped != 3 || res.Queued != 0 || res.Failed != 0 {
		t.Fatalf("result = %+v", res)
	}
	got := hashesOf(t, `SELECT hash, COALESCE(branch,'') FROM commits`)
	if len(got) != 3 || got[m1] != "main" || got[m2] != "main" || got[f1] != "feature/x" {
		t.Fatalf("commits = %v (the 200-day-old commit is outside the 90 days)", got)
	}
	if n := count(t, `SELECT count(*) FROM commits WHERE review_status = 'skipped' AND review_skip_reason = $1`, skipBackfill); n != 3 {
		t.Fatalf("%d commits skipped as history, want 3", n)
	}
	if n := count(t, `SELECT count(*) FROM river_job WHERE kind = 'review_commit'`); n != 0 {
		t.Fatalf("%d review jobs for history", n)
	}
	// The poller carries on from the branch heads instead of reading it all again.
	cur := hashesOf(t, `SELECT branch, last_hash FROM poll_cursors`)
	if cur["main"] != m2 || cur["feature/x"] != f1 {
		t.Fatalf("cursors = %v", cur)
	}

	// Running it again changes nothing.
	res, err = Backfill(context.Background(), quiet, testPool, bb, rc, BackfillOptions{Repos: []string{"acme/*"}, Days: 90, MaxPerBranch: 100})
	if err != nil || res.NewCommits != 0 {
		t.Fatalf("second run = %+v %v", res, err)
	}
	if n := count(t, `SELECT count(*) FROM commits`); n != 3 {
		t.Fatalf("commits = %d after a second run", n)
	}
}

func TestBackfillCanQueueReviewsAndRespectsTheCap(t *testing.T) {
	now := time.Now()
	m := mockbitbucket.New("")
	var hashes []string
	for i := 0; i < 6; i++ {
		hashes = append(hashes, m.AddCommitAt("acme", "api", "main", "c", now.Add(-time.Duration(6-i)*time.Hour)))
	}
	bb, rc := backfillSetup(t, m)

	res, err := Backfill(context.Background(), quiet, testPool, bb, rc, BackfillOptions{Repos: []string{"acme/api"}, Days: 30, MaxPerBranch: 4, Review: true, ReviewNewRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.NewCommits != 4 || res.Queued != 4 {
		t.Fatalf("result = %+v, want the newest 4 queued", res)
	}
	got := hashesOf(t, `SELECT hash, COALESCE(branch,'') FROM commits`)
	for _, h := range hashes[2:] {
		if got[h] == "" {
			t.Errorf("newest commit %s missing", h[:7])
		}
	}
	for _, h := range hashes[:2] {
		if got[h] != "" {
			t.Errorf("commit %s is beyond the cap but was stored", h[:7])
		}
	}
	if n := count(t, `SELECT count(*) FROM river_job WHERE kind = 'review_commit'`); n != 4 {
		t.Fatalf("%d review jobs, want 4", n)
	}
}

func TestBackfillKeepsAnExistingPollCursorAndSurvivesABadRepository(t *testing.T) {
	now := time.Now()
	m := mockbitbucket.New("")
	first := m.AddCommitAt("acme", "api", "main", "one", now.Add(-48*time.Hour))
	m.AddCommitAt("acme", "api", "main", "two", now.Add(-24*time.Hour))
	bb, rc := backfillSetup(t, m)

	// The poller has been here before and stopped at the first commit.
	if _, err := Backfill(context.Background(), quiet, testPool, bb, rc, BackfillOptions{Repos: []string{"acme/api"}, Days: 1, MaxPerBranch: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE poll_cursors SET last_hash = $1`, first); err != nil {
		t.Fatal(err)
	}
	res, err := Backfill(context.Background(), quiet, testPool, bb, rc, BackfillOptions{Repos: []string{"acme/api", "acme/missing"}, Days: 30, MaxPerBranch: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Repositories != 1 {
		t.Fatalf("result = %+v, want the missing repository counted as failed and the other one done", res)
	}
	if got := hashesOf(t, `SELECT branch, last_hash FROM poll_cursors`)["main"]; got != first {
		t.Fatalf("cursor moved to %s; the poller's own cursor must stay so it still reads what follows it", got)
	}
	if n := count(t, `SELECT count(*) FROM commits`); n != 2 {
		t.Fatalf("commits = %d, want 2", n)
	}
}
