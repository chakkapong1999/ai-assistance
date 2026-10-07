package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/mockbitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
)

// startedRepo polls a repository once so it exists, then switches review on.
func startedRepo(t *testing.T, m *mockbitbucket.Mock, rv review.Reviewer) (*poller, int) {
	t.Helper()
	m.AddCommit("acme", "api", "main", "first")
	p := startPollerWith(t, m, []string{"acme/api"}, nil, rv)
	p.waitRounds(1)
	if _, err := testPool.Exec(context.Background(), `UPDATE repositories SET review_enabled = true`); err != nil {
		t.Fatal(err)
	}
	return p, 1
}

func prReviews() int {
	var n int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM reviews WHERE pr_id IS NOT NULL AND commit_id IS NULL`).Scan(&n)
	return n
}

func TestPullRequestIsReviewedAsOneDiffAndAgainOnNewPush(t *testing.T) {
	m := mockbitbucket.New("")
	p, rounds := startedRepo(t, m, review.Mock{Scenario: config.ScenarioFindings})

	m.AddCommit("acme", "api", "feature/x", "part one")
	m.AddCommit("acme", "api", "feature/x", "part two")
	id := m.AddPullRequest("acme", "api", "feature/x", "", "Add x", "does x")
	rounds = p.poll(rounds)

	waitFor(t, "pull request reviewed", func() bool {
		return count(t, `SELECT count(*) FROM pull_requests WHERE review_status = 'done'`) == 1
	})
	if got := m.Calls("prdiff"); got != 1 {
		t.Fatalf("pull request diff fetched %d times, want once for the whole PR", got)
	}
	if n := prReviews(); n != 1 {
		t.Fatalf("pr reviews = %d, want 1", n)
	}
	if n := count(t, `SELECT count(*) FROM reviews rv JOIN pull_requests pr ON pr.id = rv.pr_id
		WHERE rv.pr_head_hash = pr.source_hash AND pr.title = 'Add x' AND pr.bb_pr_id = $1 AND pr.state = 'OPEN'`, id); n != 1 {
		t.Fatal("review is not tied to the head it was taken at")
	}
	if count(t, `SELECT count(*) FROM review_findings f JOIN reviews rv ON rv.id = f.review_id WHERE rv.pr_id IS NOT NULL`) == 0 {
		t.Fatal("pull request review stored no findings")
	}
	if n := count(t, `SELECT count(*) FROM pull_requests WHERE files_changed > 0 AND author_user_id IS NOT NULL`); n != 1 {
		t.Fatal("pull request stats or author missing")
	}

	// Nothing changed: no write, no new review.
	before := str(t, `SELECT updated_at::text FROM pull_requests`)
	rounds = p.poll(rounds)
	if after := str(t, `SELECT updated_at::text FROM pull_requests`); after != before {
		t.Fatalf("an idle round wrote to the pull request (%s -> %s)", before, after)
	}
	if prReviews() != 1 || m.Calls("prdiff") != 1 {
		t.Fatalf("an idle round reviewed again: reviews=%d diffs=%d", prReviews(), m.Calls("prdiff"))
	}

	// A new commit on the source branch is a new head: reviewed again.
	m.AddCommit("acme", "api", "feature/x", "part three")
	rounds = p.poll(rounds)
	waitFor(t, "second review", func() bool { return prReviews() == 2 })
	waitFor(t, "settled", func() bool {
		return count(t, `SELECT count(*) FROM pull_requests WHERE review_status = 'done'`) == 1
	})
	if n := count(t, `SELECT count(DISTINCT pr_head_hash) FROM reviews WHERE pr_id IS NOT NULL`); n != 2 {
		t.Fatalf("the two reviews were taken at %d distinct heads, want 2", n)
	}

	// Merged: state follows, no review of a closed pull request, the old review stays.
	m.SetPullRequestState("acme", "api", id, "MERGED")
	p.poll(rounds)
	waitFor(t, "state follows", func() bool {
		return count(t, `SELECT count(*) FROM pull_requests WHERE state = 'MERGED' AND review_status = 'done'`) == 1
	})
	if prReviews() != 2 {
		t.Fatalf("a merged pull request was reviewed again: %d", prReviews())
	}
	if m.Calls("pullrequest") == 0 {
		t.Fatal("the closed pull request was not looked up")
	}
}

func TestPullRequestOfDisabledRepoIsRecordedNotReviewed(t *testing.T) {
	m := mockbitbucket.New("")
	m.AddCommit("acme", "api", "main", "first")
	m.AddCommit("acme", "api", "feature/x", "work")
	m.AddPullRequest("acme", "api", "feature/x", "", "Add x", "")
	p := startPoller(t, m, []string{"acme/api"}, nil)
	p.waitRounds(1)

	if n := count(t, `SELECT count(*) FROM pull_requests WHERE review_status = 'skipped' AND review_skip_reason = 'review is not enabled for this repository'`); n != 1 {
		t.Fatalf("pull request of a repository with review off: %d skipped rows", n)
	}
	if count(t, `SELECT count(*) FROM river_job WHERE kind = 'review_pull_request'`) != 0 || m.Calls("prdiff") != 0 {
		t.Fatal("a pull request of a repository with review off was queued or fetched")
	}
}

func TestStaleOpenPullRequestsAreNotReviewedOnFirstSight(t *testing.T) {
	m := mockbitbucket.New("")
	m.AddCommit("acme", "api", "main", "first")
	m.AddCommit("acme", "api", "old", "work")
	id := m.AddPullRequest("acme", "api", "old", "", "Ancient", "")
	m.SetPullRequestUpdated("acme", "api", id, time.Now().Add(-30*24*time.Hour))
	p := startPoller(t, m, []string{"acme/api"}, nil)
	p.waitRounds(1)
	if n := count(t, `SELECT count(*) FROM pull_requests`); n != 0 {
		t.Fatalf("a pull request untouched for 30 days was stored on first sight: %d", n)
	}
}

func TestPullRequestGoneFromBitbucketIsMarked(t *testing.T) {
	m := mockbitbucket.New("")
	p, rounds := startedRepo(t, m, review.Mock{Scenario: config.ScenarioFindings})
	m.AddCommit("acme", "api", "feature/x", "work")
	id := m.AddPullRequest("acme", "api", "feature/x", "", "Add x", "")
	rounds = p.poll(rounds)
	waitFor(t, "reviewed", func() bool { return prReviews() == 1 })

	m.DeletePullRequest("acme", "api", id)
	p.poll(rounds)
	waitFor(t, "marked", func() bool { return count(t, `SELECT count(*) FROM pull_requests WHERE state = 'DELETED'`) == 1 })
}

// movingReviewer simulates a push that lands while the first review runs.
type movingReviewer struct {
	review.Mock
	calls atomic.Int32
}

func (r *movingReviewer) Review(ctx context.Context, req review.Request) (review.Result, error) {
	if req.PullRequest && r.calls.Add(1) == 1 {
		testPool.Exec(ctx, `UPDATE pull_requests SET source_hash = 'newhead', review_status = 'pending'`) //nolint:errcheck
	}
	return r.Mock.Review(ctx, req)
}

func TestPullRequestThatMovesDuringReviewIsReviewedAgain(t *testing.T) {
	m := mockbitbucket.New("")
	rv := &movingReviewer{Mock: review.Mock{Scenario: config.ScenarioFindings}}
	p, rounds := startedRepo(t, m, rv)
	m.AddCommit("acme", "api", "feature/x", "work")
	m.AddPullRequest("acme", "api", "feature/x", "", "Add x", "")
	p.poll(rounds)

	waitFor(t, "reviewed at the new head", func() bool {
		return count(t, `SELECT count(*) FROM reviews WHERE pr_head_hash = 'newhead'`) == 1 &&
			count(t, `SELECT count(*) FROM pull_requests WHERE review_status = 'done'`) == 1
	})
	if n := prReviews(); n != 2 {
		t.Fatalf("reviews = %d, want 2 (the stale head, then the new one)", n)
	}
}

func TestPendingPullRequestWithoutJobGetsOne(t *testing.T) {
	m := mockbitbucket.New("")
	p, rounds := startedRepo(t, m, review.Mock{Scenario: config.ScenarioFindings})
	m.AddCommit("acme", "api", "feature/x", "work")
	m.AddPullRequest("acme", "api", "feature/x", "", "Add x", "")
	rounds = p.poll(rounds)
	waitFor(t, "first review", func() bool { return prReviews() == 1 })

	// A crash between steps could leave this: pending, and no job anywhere.
	testPool.Exec(context.Background(), `UPDATE pull_requests SET review_status = 'pending'`)
	testPool.Exec(context.Background(), `DELETE FROM river_job WHERE kind = 'review_pull_request'`)
	p.poll(rounds)
	waitFor(t, "reviewed again", func() bool {
		return prReviews() == 2 && count(t, `SELECT count(*) FROM pull_requests WHERE review_status = 'done'`) == 1
	})
}

func TestReviewJobOfAClosedPullRequestIsSkippedWithoutFetching(t *testing.T) {
	m := mockbitbucket.New("")
	p, _ := startedRepo(t, m, review.Mock{Scenario: config.ScenarioFindings})
	var id int64
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO pull_requests (repo_id, bb_pr_id, title, state, source_hash, review_status)
		SELECT id, 7, 'closed meanwhile', 'MERGED', 'abc', 'pending' FROM repositories LIMIT 1 RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.rc.Insert(context.Background(), jobs.ReviewPullRequestArgs{PullRequestID: id}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "skipped", func() bool {
		return count(t, `SELECT count(*) FROM pull_requests WHERE id = $1 AND review_status = 'skipped' AND review_skip_reason = 'pull request is not open'`, id) == 1
	})
	if m.Calls("prdiff") != 0 {
		t.Fatal("the diff of a closed pull request was fetched")
	}
}
