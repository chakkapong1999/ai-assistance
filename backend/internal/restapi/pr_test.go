package restapi_test

import (
	"context"
	"strings"
	"testing"
)

type prList struct {
	Items []struct {
		ID       int64  `json:"id"`
		Number   int    `json:"number"`
		Title    string `json:"title"`
		State    string `json:"state"`
		Status   string `json:"review_status"`
		Score    *int   `json:"score"`
		Findings int    `json:"findings"`
		Outdated bool   `json:"review_outdated"`
		Author   struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func numbers(l prList) string {
	var ns []string
	for _, p := range l.Items {
		ns = append(ns, itoa(int64(p.Number)))
	}
	return strings.Join(ns, ",")
}

// withPullRequests adds, to the standard data:
//
//	#1 api, alice, OPEN, pending: reviewed twice (heads h1, h2); the branch is at h3, so the review is outdated
//	#2 api, rob,   MERGED, done:  reviewed once at its head
//	#3 web (review off), alice, OPEN, skipped
func withPullRequests(t *testing.T, e *env) {
	t.Helper()
	pr := func(key, repo string, num int, author any, title, state, status, head string, ageMin int) {
		e.ids[key] = must(t, `
			INSERT INTO pull_requests (repo_id, bb_pr_id, title, description, author_user_id, source_branch, dest_branch, state,
				source_hash, bb_updated_on, review_status, files_changed, additions, deletions)
			VALUES ($1, $2::int, $3::text, 'desc of '||$3::text, $4, 'feature/'||$2::text, 'main', $5, $6,
				now() - make_interval(mins => $7::int), $8, 4, 20, 5) RETURNING id`,
			e.ids[repo], num, title, author, state, head, ageMin, status)
	}
	pr("pr1", "api", 1, e.ids["alice"], "Add retries", "OPEN", "pending", "h3", 1)
	pr("pr2", "api", 2, e.ids["rob"], "Fix typo", "MERGED", "done", "m1", 10)
	pr("pr3", "web", 3, e.ids["alice"], "Web redesign", "OPEN", "skipped", "w1", 20)
	exec(t, `UPDATE pull_requests SET review_skip_reason = 'review is not enabled for this repository' WHERE id = $1`, e.ids["pr3"])

	rv := func(pr, head string, score, ageMin int, cost float64) int64 {
		return must(t, `INSERT INTO reviews (pr_id, pr_head_hash, model, prompt_version, score, summary, tokens_in, tokens_out, cost_usd, created_at)
			VALUES ($1, $2, 'mock', 'v1', $3::int, 'pr sum '||$3::text, 1000, 100, $4, now() - make_interval(mins => $5::int)) RETURNING id`,
			e.ids[pr], head, score, cost, ageMin)
	}
	rv("pr1", "h1", 20, 60, 0.5)
	r := rv("pr1", "h2", 70, 30, 0.25)
	for i, sev := range []string{"minor", "critical"} {
		must(t, `INSERT INTO review_findings (review_id, file_path, line_start, line_end, severity, category, title, explanation)
			VALUES ($1, 'pr.go', $2, $2, $3, 'bug', 'pr-finding-'||$3, 'because') RETURNING id`, r, i+1, sev)
	}
	rv("pr2", "m1", 95, 15, 0.1)
}

func TestListPullRequests(t *testing.T) {
	e := setup(t)
	withPullRequests(t, e)

	var l prList
	e.get(t, "/api/v1/pull-requests", &l)
	if got := numbers(l); got != "1,2,3" {
		t.Fatalf("order = %s, want most recently updated first (1,2,3)", got)
	}
	p1 := l.Items[0]
	if p1.Title != "Add retries" || p1.Score == nil || *p1.Score != 70 || p1.Findings != 2 || !p1.Outdated || p1.Author.Name != "Alice A" || p1.Status != "pending" {
		t.Errorf("#1 = %+v; want the latest review (70, 2 findings), outdated, by Alice", p1)
	}
	if p2 := l.Items[1]; p2.Outdated || p2.Score == nil || *p2.Score != 95 || p2.State != "MERGED" {
		t.Errorf("#2 = %+v; reviewed at its head, so not outdated", p2)
	}
	if p3 := l.Items[2]; p3.Score != nil || p3.Findings != 0 || p3.Outdated {
		t.Errorf("#3 = %+v; never reviewed", p3)
	}

	for q, want := range map[string]string{
		"?state=OPEN":                      "1,3",
		"?state=MERGED":                    "2",
		"?review_status=done":              "2",
		"?repo_id=" + itoa(e.ids["web"]):   "3",
		"?author_id=" + itoa(e.ids["rob"]): "2",
		"?q=typo":                          "2",
		"?q=feature%2F3":                   "3",
		"?q=1":                             "1",
		"?state=DELETED":                   "",
	} {
		var f prList
		e.get(t, "/api/v1/pull-requests"+q, &f)
		if got := numbers(f); got != want {
			t.Errorf("%s -> %q, want %q", q, got, want)
		}
	}

	// Keyset pagination is stable and complete.
	var seen []string
	cursor := ""
	for i := 0; i < 5; i++ {
		var pg prList
		e.get(t, "/api/v1/pull-requests?limit=1&cursor="+cursor, &pg)
		seen = append(seen, numbers(pg))
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	if strings.Join(seen, "|") != "1|2|3" {
		t.Errorf("pages = %v", seen)
	}

	for _, q := range []string{"?state=open", "?state=NOPE", "?review_status=bogus", "?repo_id=x", "?author_id=0", "?limit=0", "?cursor=!!"} {
		if code, _ := e.do(t, "GET", "/api/v1/pull-requests"+q, viewerTok, nil); code != 400 {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}
}

func TestPullRequestDetail(t *testing.T) {
	e := setup(t)
	withPullRequests(t, e)

	var d struct {
		Number       int    `json:"number"`
		Description  string `json:"description"`
		ReviewsCount int    `json:"reviews_count"`
		Outdated     bool   `json:"review_outdated"`
		Review       *struct {
			Score    int      `json:"score"`
			Cost     *float64 `json:"cost_usd"`
			Findings []struct {
				Title    string `json:"title"`
				Severity string `json:"severity"`
			} `json:"findings"`
		} `json:"review"`
	}
	e.get(t, "/api/v1/pull-requests/"+itoa(e.ids["pr1"]), &d)
	if d.Number != 1 || d.Description != "desc of Add retries" || d.ReviewsCount != 2 || !d.Outdated {
		t.Errorf("detail = %+v", d)
	}
	if d.Review == nil || d.Review.Score != 70 || d.Review.Cost == nil || *d.Review.Cost != 0.25 ||
		len(d.Review.Findings) != 2 || d.Review.Findings[0].Severity != "critical" {
		t.Errorf("review = %+v; want the newest (70), findings critical first", d.Review)
	}

	e.get(t, "/api/v1/pull-requests/"+itoa(e.ids["pr3"]), &d)
	if d.Review != nil || d.ReviewsCount != 0 {
		t.Errorf("an unreviewed pull request has review %+v", d.Review)
	}
	if code, _ := e.do(t, "GET", "/api/v1/pull-requests/99999", viewerTok, nil); code != 404 {
		t.Errorf("unknown id = %d, want 404", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/pull-requests/abc", viewerTok, nil); code != 400 {
		t.Errorf("bad id = %d, want 400", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/pull-requests", "", nil); code != 401 {
		t.Errorf("no token = %d, want 401", code)
	}
}

func TestRereviewPullRequest(t *testing.T) {
	e := setup(t)
	withPullRequests(t, e)
	path := func(k string) string { return "/api/v1/pull-requests/" + itoa(e.ids[k]) + "/rereview" }

	if code, _ := e.do(t, "POST", path("pr1"), viewerTok, nil); code != 403 {
		t.Errorf("viewer = %d, want 403", code)
	}
	if code, _ := e.do(t, "POST", "/api/v1/pull-requests/99999/rereview", adminTok, nil); code != 404 {
		t.Errorf("unknown = %d, want 404", code)
	}
	// 409s: not open, review off, running.
	for k, why := range map[string]string{"pr2": "merged", "pr3": "repository review off"} {
		if code, _ := e.do(t, "POST", path(k), adminTok, nil); code != 409 {
			t.Errorf("%s (%s) = %d, want 409", k, why, code)
		}
	}
	exec(t, `UPDATE pull_requests SET review_status = 'running' WHERE id = $1`, e.ids["pr1"])
	if code, _ := e.do(t, "POST", path("pr1"), adminTok, nil); code != 409 {
		t.Errorf("running = %d, want 409", code)
	}

	exec(t, `UPDATE pull_requests SET review_status = 'done', review_skip_reason = 'old' WHERE id = $1`, e.ids["pr1"])
	if code, b := e.do(t, "POST", path("pr1"), adminTok, nil); code != 202 {
		t.Fatalf("admin = %d %s, want 202", code, b)
	}
	if got := queryStr(t, `SELECT review_status || '/' || COALESCE(review_skip_reason, '-') FROM pull_requests WHERE id = $1`, e.ids["pr1"]); got != "pending/-" {
		t.Errorf("status after rereview = %s", got)
	}
	if n := queryInt(t, `SELECT count(*) FROM river_job WHERE kind = 'review_pull_request' AND (args->>'pull_request_id')::bigint = $1 AND state = 'available'`, e.ids["pr1"]); n != 1 {
		t.Errorf("jobs = %d, want 1", n)
	}
	// Asking again while the job waits does not queue a second one.
	if code, _ := e.do(t, "POST", path("pr1"), adminTok, nil); code != 202 {
		t.Errorf("second request = %d", code)
	}
	if n := queryInt(t, `SELECT count(*) FROM river_job WHERE kind = 'review_pull_request'`); n != 1 {
		t.Errorf("jobs after a repeat = %d, want 1", n)
	}
}

func TestOverviewUsageIncludesPullRequestReviews(t *testing.T) {
	e := setup(t)
	exec(t, `UPDATE reviews SET created_at = now() - interval '3 days'`)
	withPullRequests(t, e)
	var o struct {
		Usage struct {
			Runs    int     `json:"runs"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"usage"`
	}
	e.get(t, "/api/v1/overview?days=1", &o)
	// The pull request reviews cost 0.5 + 0.25 + 0.1 (money is spent per run).
	if o.Usage.Runs != 3 || o.Usage.CostUSD != 0.85 {
		t.Errorf("usage = %+v, want 3 runs, 0.85", o.Usage)
	}
}

func queryStr(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func queryInt(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
