package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

type wfEnv struct {
	d                     *store.Dashboard
	author, senior, other int64
	rev                   int64   // review of a commit by author
	f                     []int64 // its three findings
}

func wfID(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func wfSetup(t *testing.T) wfEnv {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `TRUNCATE workspaces, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	e := wfEnv{d: store.NewDashboard(testPool, nil)}
	ws := wfID(t, `INSERT INTO workspaces (bb_uuid, slug, name) VALUES ('{w}', 'acme', 'Acme') RETURNING id`)
	pj := wfID(t, `INSERT INTO projects (workspace_id, bb_uuid, key, name) VALUES ($1, '{p}', 'C', 'Core') RETURNING id`, ws)
	repo := wfID(t, `INSERT INTO repositories (project_id, bb_uuid, slug, name, review_enabled) VALUES ($1, '{r}', 'api', 'API', true) RETURNING id`, pj)
	e.author = wfID(t, `INSERT INTO users (display_name) VALUES ('Alice') RETURNING id`)
	e.senior = wfID(t, `INSERT INTO users (display_name) VALUES ('Sam Senior') RETURNING id`)
	e.other = wfID(t, `INSERT INTO users (display_name) VALUES ('Olga') RETURNING id`)
	c := wfID(t, `INSERT INTO commits (repo_id, hash, author_user_id, message, branch, committed_at, review_status) VALUES ($1, 'h1', $2, 'm', 'main', now(), 'done') RETURNING id`, repo, e.author)
	e.rev = wfID(t, `INSERT INTO reviews (commit_id, model, prompt_version, score, summary) VALUES ($1, 'mock', 'v1', 80, 's') RETURNING id`, c)
	for i := 0; i < 3; i++ {
		e.f = append(e.f, wfID(t, `INSERT INTO review_findings (review_id, file_path, line_start, line_end, severity, category, title) VALUES ($1, 'a.go', 1, 1, 'major', 'bug', 't') RETURNING id`, e.rev))
	}
	return e
}

func wantErr[T error](t *testing.T, err error, substr string) {
	t.Helper()
	var target T
	if !errors.As(err, &target) {
		t.Fatalf("err = %v (%T), want %T", err, err, target)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("err = %q, want it to mention %q", err, substr)
	}
}

func (e wfEnv) status(t *testing.T, f int64) string {
	t.Helper()
	var s string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM review_findings WHERE id = $1`, f).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e wfEnv) closed(t *testing.T) bool {
	t.Helper()
	var c bool
	if err := testPool.QueryRow(context.Background(), `SELECT closed_at IS NOT NULL FROM reviews WHERE id = $1`, e.rev).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAuthorMarksFixedOnlyTheirOwnOpenFindings(t *testing.T) {
	e := wfSetup(t)
	ctx := context.Background()
	author := store.Actor{UserID: e.author}

	wantErr[*store.Denied](t, e.d.MarkFixed(ctx, store.Actor{}, e.f[0], ""), "not linked to a user")
	wantErr[*store.Denied](t, e.d.MarkFixed(ctx, store.Actor{UserID: e.other}, e.f[0], ""), "only the author")
	// A reviewer who is not the author cannot fix on someone's behalf either.
	wantErr[*store.Denied](t, e.d.MarkFixed(ctx, store.Actor{UserID: e.senior, Reviewer: true}, e.f[0], ""), "only the author")
	if err := e.d.MarkFixed(ctx, author, 999999, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown finding: %v", err)
	}

	if err := e.d.MarkFixed(ctx, author, e.f[0], "  moved the check  "); err != nil {
		t.Fatal(err)
	}
	if e.status(t, e.f[0]) != "fixed" {
		t.Fatal("finding should be fixed")
	}
	wantErr[*store.Conflict](t, e.d.MarkFixed(ctx, author, e.f[0], ""), "already fixed")
	wantErr[*store.Conflict](t, e.d.MarkFixed(ctx, author, e.f[1], strings.Repeat("x", 2001)), "too long")

	cd := func() store.Finding {
		var id int64
		testPool.QueryRow(ctx, `SELECT commit_id FROM reviews WHERE id = $1`, e.rev).Scan(&id)
		d, err := e.d.Commit(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range d.Review.Findings {
			if f.ID == e.f[0] {
				return f
			}
		}
		t.Fatal("finding missing")
		return store.Finding{}
	}()
	if cd.Status != "fixed" || cd.StatusBy == nil || cd.StatusBy.Name != "Alice" || cd.StatusNote == nil || *cd.StatusNote != "moved the check" {
		t.Errorf("finding = %+v", cd)
	}
	if len(cd.History) != 1 || cd.History[0].Action != "fixed" {
		t.Errorf("history = %+v", cd.History)
	}
}

func TestReviewerSendsBackDismissesAndCloses(t *testing.T) {
	e := wfSetup(t)
	ctx := context.Background()
	author := store.Actor{UserID: e.author}
	senior := store.Actor{UserID: e.senior, Reviewer: true}
	ownReviewer := store.Actor{UserID: e.author, Reviewer: true}

	// Reviewer powers need the reviewer flag, a user, and someone else's work.
	wantErr[*store.Denied](t, e.d.CloseReview(ctx, store.Actor{UserID: e.other}, e.rev, ""), "senior, lead or admin")
	wantErr[*store.Denied](t, e.d.CloseReview(ctx, store.Actor{Reviewer: true}, e.rev, ""), "not linked")
	wantErr[*store.Denied](t, e.d.CloseReview(ctx, ownReviewer, e.rev, ""), "your own work")
	wantErr[*store.Denied](t, e.d.Dismiss(ctx, ownReviewer, e.f[0], "x"), "your own work")

	// Three findings are open, so the review cannot close yet.
	wantErr[*store.Conflict](t, e.d.CloseReview(ctx, senior, e.rev, ""), "3 findings are still open")

	if err := e.d.MarkFixed(ctx, author, e.f[0], ""); err != nil {
		t.Fatal(err)
	}
	if err := e.d.MarkFixed(ctx, author, e.f[1], ""); err != nil {
		t.Fatal(err)
	}
	wantErr[*store.Conflict](t, e.d.Reopen(ctx, senior, e.f[1], "  "), "note is required")
	wantErr[*store.Conflict](t, e.d.Reopen(ctx, senior, e.f[2], "still broken"), "already open")
	if err := e.d.Reopen(ctx, senior, e.f[1], "still racy"); err != nil {
		t.Fatal(err)
	}
	if e.status(t, e.f[1]) != "open" {
		t.Fatal("reopened finding should be open")
	}
	wantErr[*store.Conflict](t, e.d.Dismiss(ctx, senior, e.f[2], ""), "note is required")
	if err := e.d.Dismiss(ctx, senior, e.f[2], "false positive"); err != nil {
		t.Fatal(err)
	}
	wantErr[*store.Conflict](t, e.d.CloseReview(ctx, senior, e.rev, ""), "1 findings are still open")

	if err := e.d.MarkFixed(ctx, author, e.f[1], "locked it"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.CloseReview(ctx, senior, e.rev, "looks good"); err != nil {
		t.Fatal(err)
	}
	if !e.closed(t) {
		t.Fatal("review should be closed")
	}
	wantErr[*store.Conflict](t, e.d.CloseReview(ctx, senior, e.rev, ""), "already closed")

	// A closed review is frozen for the author and for dismissals...
	wantErr[*store.Conflict](t, e.d.Dismiss(ctx, senior, e.f[0], "x"), "closed")
	// ...but a reviewer who finds a problem later can reopen a finding, which reopens the review.
	if err := e.d.Reopen(ctx, senior, e.f[0], "regressed"); err != nil {
		t.Fatal(err)
	}
	if e.closed(t) {
		t.Error("reopening a finding must reopen the review")
	}
	wantErr[*store.Conflict](t, e.d.CloseReview(ctx, senior, e.rev, ""), "1 findings are still open")

	var id int64
	testPool.QueryRow(ctx, `SELECT commit_id FROM reviews WHERE id = $1`, e.rev).Scan(&id)
	d, _ := e.d.Commit(ctx, id)
	if d.Review.Closed != nil {
		t.Errorf("closed = %+v", d.Review.Closed)
	}
	for _, f := range d.Review.Findings {
		if f.ID == e.f[1] && len(f.History) != 3 { // fixed, reopened, fixed
			t.Errorf("history of the sent-back finding = %+v", f.History)
		}
	}
}

func TestUnlinkedAuthorCanBeStoodInForByAReviewer(t *testing.T) {
	e := wfSetup(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE commits SET author_user_id = NULL, author_raw = 'Rob <rob@x.io>'`); err != nil {
		t.Fatal(err)
	}
	wantErr[*store.Denied](t, e.d.MarkFixed(ctx, store.Actor{UserID: e.other}, e.f[0], ""), "only the author")
	if err := e.d.MarkFixed(ctx, store.Actor{UserID: e.senior, Reviewer: true}, e.f[0], ""); err != nil {
		t.Fatal(err)
	}
}
