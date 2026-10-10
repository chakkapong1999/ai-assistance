package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/restapi"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/testdb"
)

const (
	adminTok  = "admin-token-0123456789"
	viewerTok = "viewer-token-0123456789"

	aliceTok     = "alice-author-token-0123"
	robTok       = "rob-author-token-012345"
	samTok       = "sam-senior-token-01234"
	aliceLeadTok = "alice-lead-token-01234"
	ghostTok     = "ghost-author-token-012"
	adminSamTok  = "sam-admin-token-012345"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testdb.Main(m, func(p *pgxpool.Pool) { pool = p }) }

type env struct {
	srv *httptest.Server
	ids map[string]int64
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func must(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// setup resets the data and seeds:
//
//	repo "acme/api" (review enabled) and "acme/web" (disabled)
//	alice (linked, has an e-mail) and a raw-only author "Rob <rob@x.io>"
//	api commits c1..c5 (newest = c1): c1 done score 91 with findings, c2 done
//	(two reviews, the newer one counts), c3 skipped, c4 failed on branch
//	"dev", c5 pending merge commit. web commit w1 pending.
func setup(t *testing.T) *env {
	t.Helper()
	exec(t, `TRUNCATE workspaces, users, river_job RESTART IDENTITY CASCADE`)
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]int64{}
	ws := must(t, `INSERT INTO workspaces (bb_uuid, slug, name) VALUES ('{ws}', 'acme', 'Acme') RETURNING id`)
	pj := must(t, `INSERT INTO projects (workspace_id, bb_uuid, key, name) VALUES ($1, '{pj}', 'CORE', 'Core') RETURNING id`, ws)
	ids["api"] = must(t, `INSERT INTO repositories (project_id, bb_uuid, slug, name, review_enabled, default_branch) VALUES ($1, '{api}', 'api', 'API', true, 'main') RETURNING id`, pj)
	ids["web"] = must(t, `INSERT INTO repositories (project_id, bb_uuid, slug, name, review_enabled) VALUES ($1, '{web}', 'web', 'Web', false) RETURNING id`, pj)
	ids["alice"] = must(t, `INSERT INTO users (bb_account_id, display_name, nickname, email, job_title) VALUES ('a1', 'Alice A', 'alice', 'alice@acme.io', 'Engineer') RETURNING id`)
	ids["rob"] = must(t, `INSERT INTO users (display_name, email) VALUES ('Rob', 'rob@x.io') RETURNING id`)

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	commit := func(name, repo string, author any, raw any, msg, branch, status string, ageH int, merge bool) {
		ids[name] = must(t, `
			INSERT INTO commits (repo_id, hash, author_user_id, author_raw, message, branch, committed_at, review_status, is_merge, files_changed, additions, deletions)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 2, 10, 3) RETURNING id`,
			ids[repo], name+"0000", author, raw, msg, branch, base.Add(-time.Duration(ageH)*time.Hour), status, merge)
	}
	commit("c1", "api", ids["alice"], nil, "Fix login\n\nlong body", "main", "done", 0, false)
	commit("c2", "api", ids["alice"], nil, "Add 100% coverage", "main", "done", 1, false)
	commit("c3", "api", nil, "Rob <rob@x.io>", "docs only", "main", "skipped", 2, false)
	commit("c4", "api", ids["rob"], nil, "break things", "dev", "failed", 3, false)
	commit("c5", "api", ids["alice"], nil, "Merge branch dev", "main", "pending", 4, true)
	commit("w1", "web", ids["alice"], nil, "web change", "main", "pending", 5, false)
	exec(t, `UPDATE commits SET review_skip_reason = 'only docs' WHERE id = $1`, ids["c3"])

	review := func(commit string, score int, ageMin int) int64 {
		return must(t, `INSERT INTO reviews (commit_id, model, prompt_version, score, summary, created_at)
			VALUES ($1, 'mock', 'v1', $2::int, 'sum '||$2::text, now() - make_interval(mins => $3::int)) RETURNING id`, ids[commit], score, ageMin)
	}
	finding := func(rv int64, file string, line int, sev, title string) int64 {
		return must(t, `INSERT INTO review_findings (review_id, file_path, line_start, line_end, severity, category, title, explanation)
			VALUES ($1, $2, $3, $3, $4, 'bug', $5, 'because') RETURNING id`, rv, file, line, sev, title)
	}
	r1 := review("c1", 91, 1)
	finding(r1, "b.go", 9, "minor", "minor-b")
	finding(r1, "z.go", 1, "critical", "crit-z")
	fid := finding(r1, "a.go", 5, "major", "major-a")
	exec(t, `UPDATE review_findings SET code_context = $2 WHERE id = $1`, fid, "@@ -5,1 +5,1 @@\n-x\n+y")
	exec(t, `INSERT INTO code_suggestions (finding_id, original_snippet, suggested_snippet, unified_diff) VALUES ($1, 'x', 'y', '--- a/a.go')`, fid)
	old := review("c2", 10, 120)
	finding(old, "old.go", 1, "critical", "stale")
	r2 := review("c2", 100, 5) // newer: this one counts
	_ = r2
	ids["sam"] = must(t, `INSERT INTO users (display_name) VALUES ('Sam Senior') RETURNING id`)
	h := restapi.New(store.NewDashboard(pool, rc), []config.APIToken{
		{Token: adminTok, Role: "admin"}, {Token: viewerTok, Role: "viewer"},
		{Token: aliceTok, Role: "author", UserID: ids["alice"]},
		{Token: robTok, Role: "author", UserID: ids["rob"]},
		{Token: samTok, Role: "senior", UserID: ids["sam"]},
		{Token: aliceLeadTok, Role: "lead", UserID: ids["alice"]},
		{Token: ghostTok, Role: "author", UserID: 99999},
		{Token: adminSamTok, Role: "admin", UserID: ids["sam"]},
	}, discardLog())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{srv: srv, ids: ids}
}

func (e *env) do(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *env) get(t *testing.T, path string, out any) {
	t.Helper()
	code, b := e.do(t, "GET", path, viewerTok, nil)
	if code != 200 {
		t.Fatalf("GET %s = %d %s", path, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("decode %s: %v\n%s", path, err, b)
		}
	}
}

type commitList struct {
	Items []struct {
		ID       int64  `json:"id"`
		Hash     string `json:"hash"`
		Subject  string `json:"subject"`
		Status   string `json:"review_status"`
		Score    *int   `json:"score"`
		Findings int    `json:"findings"`
		Author   struct {
			ID   *int64 `json:"id"`
			Name string `json:"name"`
		} `json:"author"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func hashes(l commitList) string {
	var hs []string
	for _, c := range l.Items {
		hs = append(hs, c.Hash[:2])
	}
	return strings.Join(hs, ",")
}

func TestNoTokensMeansNoAPI(t *testing.T) {
	if h := restapi.New(nil, nil, discardLog()); h != nil {
		t.Fatal("API must not be served without tokens")
	}
}

func TestAuthentication(t *testing.T) {
	e := setup(t)
	for name, hdr := range map[string]string{
		"none": "", "wrong": "Bearer nope-nope-nope-nope", "scheme": "Basic " + adminTok, "empty": "Bearer ", "raw": adminTok,
	} {
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/me", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: status %d, want 401 with WWW-Authenticate", name, resp.StatusCode)
		}
	}
	var me struct{ Role string }
	for tok, want := range map[string]string{adminTok: "admin", viewerTok: "viewer"} {
		code, b := e.do(t, "GET", "/api/v1/me", tok, nil)
		_ = json.Unmarshal(b, &me)
		if code != 200 || me.Role != want {
			t.Errorf("token %s: %d %s", want, code, b)
		}
	}
	// The spec is public; unknown paths are a JSON 404 (after no auth needed).
	if code, b := e.do(t, "GET", "/api/v1/openapi.yaml", "", nil); code != 200 || !bytes.Contains(b, []byte("openapi: 3.0.3")) {
		t.Errorf("openapi: %d", code)
	}
	if code, b := e.do(t, "GET", "/api/v1/nope", viewerTok, nil); code != 404 || !bytes.Contains(b, []byte(`"not_found"`)) {
		t.Errorf("unknown path: %d %s", code, b)
	}
}

func TestViewerCannotWrite(t *testing.T) {
	e := setup(t)
	api := e.ids["api"]
	// 403 wins over 404 and over body validation: a viewer learns nothing.
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/api/v1/repositories/999999"},
		{"PATCH", "/api/v1/repositories/" + itoa(api)},
		{"POST", "/api/v1/commits/999999/rereview"},
		{"POST", "/api/v1/commits/" + itoa(e.ids["c1"]) + "/rereview"},
	} {
		if code, _ := e.do(t, c.method, c.path, viewerTok, map[string]bool{"review_enabled": false}); code != 403 {
			t.Errorf("viewer %s %s = %d, want 403", c.method, c.path, code)
		}
	}
	var repo struct {
		ReviewEnabled bool `json:"review_enabled"`
	}
	e.get(t, "/api/v1/repositories/"+itoa(api), &repo)
	if !repo.ReviewEnabled {
		t.Fatal("a rejected PATCH changed the repository")
	}
	var s string
	pool.QueryRow(context.Background(), `SELECT review_status FROM commits WHERE id=$1`, e.ids["c1"]).Scan(&s)
	if s != "done" {
		t.Fatalf("a rejected rereview changed the commit: %s", s)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestEmailsAreAdminOnly(t *testing.T) {
	e := setup(t)
	for tok, want := range map[string]bool{adminTok: true, viewerTok: false} {
		for _, p := range []string{"/api/v1/users", "/api/v1/users/" + itoa(e.ids["alice"])} {
			_, b := e.do(t, "GET", p, tok, nil)
			if got := bytes.Contains(b, []byte("alice@acme.io")); got != want {
				t.Errorf("%s with %s token: email present = %v, want %v", p, map[string]string{adminTok: "admin", viewerTok: "viewer"}[tok], got, want)
			}
		}
	}
	// The raw git author string is stripped of its address for everyone.
	_, b := e.do(t, "GET", "/api/v1/commits", adminTok, nil)
	if bytes.Contains(b, []byte("rob@x.io")) || !bytes.Contains(b, []byte(`"name":"Rob"`)) {
		t.Errorf("commit list leaks or loses the raw author: %s", b)
	}
}

func TestCommitFilters(t *testing.T) {
	e := setup(t)
	api, alice := itoa(e.ids["api"]), itoa(e.ids["alice"])
	cases := []struct{ name, query, want string }{
		{"all", "", "c1,c2,c3,c4,c5,w1"},
		{"repo", "?repo_id=" + api, "c1,c2,c3,c4,c5"},
		{"author", "?author_id=" + alice, "c1,c2,c5,w1"},
		{"author+repo", "?author_id=" + alice + "&repo_id=" + api, "c1,c2,c5"},
		{"status", "?status=done", "c1,c2"},
		{"branch", "?branch=dev", "c4"},
		{"q message", "?q=login", "c1"},
		{"q hash", "?q=c30000", "c3"},
		{"q wildcard is literal", "?q=100%25", "c2"},
		{"q underscore is literal", "?q=_", ""},
		{"q percent alone is literal", "?q=%25", "c2"},
		{"until", "?until=" + urlTime(time.Now().Add(-2*time.Hour-30*time.Minute)), "c3,c4,c5,w1"},
		{"since", "?since=" + urlTime(time.Now().Add(-2*time.Hour-30*time.Minute)), "c1,c2"},
	}
	for _, c := range cases {
		var l commitList
		e.get(t, "/api/v1/commits"+c.query, &l)
		if got := hashes(l); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func urlTime(t time.Time) string {
	return strings.NewReplacer("+", "%2B", ":", "%3A").Replace(t.UTC().Format(time.RFC3339))
}

func TestCommitFieldsAndLatestReview(t *testing.T) {
	e := setup(t)
	var l commitList
	e.get(t, "/api/v1/commits?repo_id="+itoa(e.ids["api"]), &l)
	by := map[string]int{}
	for i, c := range l.Items {
		by[c.Hash[:2]] = i
	}
	c1, c2, c3 := l.Items[by["c1"]], l.Items[by["c2"]], l.Items[by["c3"]]
	if c1.Subject != "Fix login" || c1.Score == nil || *c1.Score != 91 || c1.Findings != 3 {
		t.Errorf("c1 = %+v", c1)
	}
	// c2 has two reviews: only the newest counts (score 100, no findings).
	if c2.Score == nil || *c2.Score != 100 || c2.Findings != 0 {
		t.Errorf("c2 should use its latest review: %+v", c2)
	}
	if c3.Score != nil || c3.Author.ID != nil || c3.Author.Name != "Rob" {
		t.Errorf("c3 = %+v", c3)
	}
}

func TestOffsetPagingAndTotal(t *testing.T) {
	e := setup(t)
	var all commitList
	e.get(t, "/api/v1/commits?limit=200", &all)
	var page struct {
		Total int `json:"total"`
		commitList
	}
	e.get(t, "/api/v1/commits?limit=2&offset=2", &page)
	if page.Total != len(all.Items) || len(page.Items) != 2 {
		t.Fatalf("total=%d items=%d, want total %d and 2 items", page.Total, len(page.Items), len(all.Items))
	}
	skipped := commitList{Items: all.Items[2:4]}
	if got, want := hashes(page.commitList), hashes(skipped); got != want {
		t.Errorf("offset page = %q, want %q", got, want)
	}
	// The total follows the filters, not the page.
	e.get(t, "/api/v1/commits?limit=1&status=done", &page)
	if page.Total != 2 || len(page.Items) != 1 {
		t.Errorf("filtered total=%d items=%d, want 2 and 1", page.Total, len(page.Items))
	}
	// Past the end is an empty page, not an error.
	e.get(t, "/api/v1/commits?offset=500", &page)
	if len(page.Items) != 0 || page.Total != len(all.Items) {
		t.Errorf("past the end: items=%d total=%d", len(page.Items), page.Total)
	}
	for _, p := range []string{"/api/v1/commits?offset=-1", "/api/v1/commits?offset=x", "/api/v1/commits?offset=1&cursor=abc", "/api/v1/pull-requests?offset=-1"} {
		if code, b := e.do(t, "GET", p, viewerTok, nil); code != 400 {
			t.Errorf("GET %s = %d %s, want 400", p, code, b)
		}
	}
	var prs struct {
		Total int `json:"total"`
	}
	e.get(t, "/api/v1/pull-requests?limit=1", &prs)
	if prs.Total < 0 {
		t.Errorf("pr total = %d", prs.Total)
	}
}

func TestPaginationWalksEveryCommitOnce(t *testing.T) {
	e := setup(t)
	// Identical timestamps force the id tiebreak of the cursor.
	exec(t, `UPDATE commits SET committed_at = (SELECT committed_at FROM commits WHERE hash = 'c10000') WHERE hash IN ('c20000','c30000')`)
	var all []int64
	cursor, pages := "", 0
	for {
		var l commitList
		e.get(t, "/api/v1/commits?limit=2"+map[bool]string{true: "&cursor=" + cursor}[cursor != ""], &l)
		for _, c := range l.Items {
			all = append(all, c.ID)
		}
		pages++
		if l.NextCursor == nil {
			break
		}
		cursor = *l.NextCursor
		if pages > 10 {
			t.Fatal("pagination does not terminate")
		}
	}
	if pages != 3 || len(all) != 6 {
		t.Fatalf("pages=%d items=%d, want 3 pages / 6 items", pages, len(all))
	}
	seen := map[int64]bool{}
	for _, id := range all {
		if seen[id] {
			t.Fatalf("commit %d returned twice: %v", id, all)
		}
		seen[id] = true
	}
	// A new commit arriving between pages must not shift the next page.
	var first commitList
	e.get(t, "/api/v1/commits?limit=2", &first)
	exec(t, `INSERT INTO commits (repo_id, hash, message, committed_at) VALUES ($1, 'fresh000', 'new', now())`, e.ids["api"])
	var second commitList
	e.get(t, "/api/v1/commits?limit=2&cursor="+*first.NextCursor, &second)
	if got := hashes(second); strings.Contains(got, "fr") || got == "" {
		t.Errorf("page 2 after a new commit arrived = %q", got)
	}
}

func TestBadParameters(t *testing.T) {
	e := setup(t)
	for _, p := range []string{
		"/api/v1/commits?limit=0", "/api/v1/commits?limit=201", "/api/v1/commits?limit=x",
		"/api/v1/commits?status=bogus", "/api/v1/commits?repo_id=-1", "/api/v1/commits?repo_id=abc",
		"/api/v1/commits?since=yesterday", "/api/v1/commits?cursor=%21%21", "/api/v1/commits?q=" + strings.Repeat("a", 201),
		"/api/v1/overview?days=0", "/api/v1/overview?days=366",
		"/api/v1/repositories?offset=-1", "/api/v1/repositories?review_enabled=maybe",
		"/api/v1/users?sort=salary", "/api/v1/users/0", "/api/v1/commits/abc", "/api/v1/repositories/-3",
	} {
		if code, b := e.do(t, "GET", p, viewerTok, nil); code != 400 {
			t.Errorf("GET %s = %d %s, want 400", p, code, b)
		}
	}
	for _, p := range []string{"/api/v1/commits/99999", "/api/v1/users/99999", "/api/v1/repositories/99999"} {
		if code, _ := e.do(t, "GET", p, viewerTok, nil); code != 404 {
			t.Errorf("GET %s = %d, want 404", p, code)
		}
	}
}

func TestCommitDetail(t *testing.T) {
	e := setup(t)
	var d struct {
		Message      string `json:"message"`
		ReviewsCount int    `json:"reviews_count"`
		Review       *struct {
			Score    int `json:"score"`
			Findings []struct {
				Title       string  `json:"title"`
				Severity    string  `json:"severity"`
				CodeContext *string `json:"code_context"`
				Suggestion  *struct {
					UnifiedDiff string `json:"unified_diff"`
				} `json:"suggestion"`
			} `json:"findings"`
		} `json:"review"`
	}
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c1"]), &d)
	if d.Review == nil || d.Review.Score != 91 || !strings.Contains(d.Message, "long body") || d.ReviewsCount != 1 {
		t.Fatalf("detail = %+v", d)
	}
	var titles []string
	for _, f := range d.Review.Findings {
		titles = append(titles, f.Severity+":"+f.Title)
	}
	if got := strings.Join(titles, ","); got != "critical:crit-z,major:major-a,minor:minor-b" {
		t.Errorf("findings order = %s", got)
	}
	if s := d.Review.Findings[1].Suggestion; s == nil || s.UnifiedDiff != "--- a/a.go" || d.Review.Findings[0].Suggestion != nil {
		t.Errorf("suggestion not attached to the right finding: %+v", d.Review.Findings)
	}
	if c := d.Review.Findings[1].CodeContext; c == nil || *c != "@@ -5,1 +5,1 @@\n-x\n+y" || d.Review.Findings[0].CodeContext != nil {
		t.Errorf("code_context not returned (null when unrecorded): %+v", d.Review.Findings)
	}
	// c2: newest review, none of the stale findings.
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c2"]), &d)
	if d.Review == nil || d.Review.Score != 100 || len(d.Review.Findings) != 0 || d.ReviewsCount != 2 {
		t.Errorf("c2 detail = %+v", d.Review)
	}
	// Unreviewed: review is null and findings is not an issue.
	var raw map[string]any
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c3"]), &raw)
	if v, ok := raw["review"]; !ok || v != nil {
		t.Errorf("review should be null: %v", raw["review"])
	}
}

func TestOverview(t *testing.T) {
	e := setup(t)
	var o struct {
		Commits  int            `json:"commits"`
		ByStatus map[string]int `json:"commits_by_status"`
		Reviewed int            `json:"reviewed"`
		AvgScore *float64       `json:"avg_score"`
		Sev      map[string]int `json:"findings_by_severity"`
		Series   []struct {
			Day      string `json:"day"`
			Commits  int    `json:"commits"`
			Reviewed int    `json:"reviewed"`
		} `json:"series"`
		Enabled, Total int `json:"-"`
		EnabledRepos   int `json:"enabled_repositories"`
		TotalRepos     int `json:"total_repositories"`
		ActiveAuthors  int `json:"active_authors"`
	}
	e.get(t, "/api/v1/overview?days=7", &o)
	if o.Commits != 6 || o.ByStatus["done"] != 2 || o.ByStatus["pending"] != 2 || o.ByStatus["failed"] != 1 || o.ByStatus["running"] != 0 {
		t.Errorf("status counts = %+v (%d)", o.ByStatus, o.Commits)
	}
	// Latest reviews only: 91 and 100 -> 95.5; the stale critical finding of c2 does not count.
	if o.Reviewed != 2 || o.AvgScore == nil || *o.AvgScore != 95.5 {
		t.Errorf("reviewed=%d avg=%v", o.Reviewed, o.AvgScore)
	}
	if o.Sev["critical"] != 1 || o.Sev["major"] != 1 || o.Sev["minor"] != 1 || o.Sev["info"] != 0 {
		t.Errorf("severity = %+v", o.Sev)
	}
	if len(o.Series) != 7 {
		t.Fatalf("series has %d days, want 7 (gaps filled)", len(o.Series))
	}
	total, rev := 0, 0
	for _, p := range o.Series {
		total += p.Commits
		rev += p.Reviewed
	}
	if total != 6 || rev != 2 {
		t.Errorf("series sums = %d/%d, want 6/2", total, rev)
	}
	if o.EnabledRepos != 1 || o.TotalRepos != 2 || o.ActiveAuthors != 2 {
		t.Errorf("repos/authors = %d/%d/%d", o.EnabledRepos, o.TotalRepos, o.ActiveAuthors)
	}
	// Old commits fall out of the window.
	exec(t, `UPDATE commits SET committed_at = now() - interval '40 days' WHERE hash = 'c10000'`)
	e.get(t, "/api/v1/overview?days=7", &o)
	if o.Commits != 5 {
		t.Errorf("commits in 7 days = %d, want 5", o.Commits)
	}
	e.get(t, "/api/v1/overview?days=60", &o)
	if o.Commits != 6 {
		t.Errorf("commits in 60 days = %d, want 6", o.Commits)
	}
}

func TestRepositories(t *testing.T) {
	e := setup(t)
	var p struct {
		Items []struct {
			ID            int64    `json:"id"`
			FullName      string   `json:"full_name"`
			ReviewEnabled bool     `json:"review_enabled"`
			Commits       int      `json:"commits"`
			Reviewed      int      `json:"reviewed"`
			AvgScore      *float64 `json:"avg_score"`
		} `json:"items"`
		Total int `json:"total"`
	}
	e.get(t, "/api/v1/repositories", &p)
	if p.Total != 2 || p.Items[0].FullName != "acme/api" || p.Items[0].Commits != 5 || p.Items[0].Reviewed != 2 ||
		p.Items[0].AvgScore == nil || *p.Items[0].AvgScore != 95.5 || p.Items[1].AvgScore != nil {
		t.Errorf("repos = %+v", p)
	}
	e.get(t, "/api/v1/repositories?review_enabled=false", &p)
	if p.Total != 1 || p.Items[0].FullName != "acme/web" {
		t.Errorf("filter enabled=false: %+v", p)
	}
	e.get(t, "/api/v1/repositories?q=ACME/A", &p)
	if p.Total != 1 || p.Items[0].FullName != "acme/api" {
		t.Errorf("q: %+v", p)
	}
	e.get(t, "/api/v1/repositories?limit=1&offset=1", &p)
	if p.Total != 2 || len(p.Items) != 1 || p.Items[0].FullName != "acme/web" {
		t.Errorf("paging: %+v", p)
	}
}

func TestToggleRepository(t *testing.T) {
	e := setup(t)
	web := "/api/v1/repositories/" + itoa(e.ids["web"])
	code, b := e.do(t, "PATCH", web, adminTok, map[string]bool{"review_enabled": true})
	if code != 200 || !bytes.Contains(b, []byte(`"review_enabled":true`)) {
		t.Fatalf("enable: %d %s", code, b)
	}
	var on bool
	pool.QueryRow(context.Background(), `SELECT review_enabled FROM repositories WHERE id=$1`, e.ids["web"]).Scan(&on)
	if !on {
		t.Fatal("not persisted")
	}
	for name, body := range map[string]any{
		"empty": map[string]any{}, "wrong type": map[string]any{"review_enabled": "yes"}, "extra field": map[string]any{"review_enabled": true, "name": "x"},
	} {
		if code, _ := e.do(t, "PATCH", web, adminTok, body); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := e.do(t, "PATCH", "/api/v1/repositories/99999", adminTok, map[string]bool{"review_enabled": true}); code != 404 {
		t.Errorf("unknown repo: %d", code)
	}
}

func jobs(t *testing.T) (n int) {
	t.Helper()
	pool.QueryRow(context.Background(), `SELECT count(*) FROM river_job WHERE kind='review_commit'`).Scan(&n)
	return
}

func status(t *testing.T, id int64) (s string, reason *string) {
	t.Helper()
	pool.QueryRow(context.Background(), `SELECT review_status, review_skip_reason FROM commits WHERE id=$1`, id).Scan(&s, &reason)
	return
}

func TestRereview(t *testing.T) {
	e := setup(t)
	post := func(id int64) int {
		code, _ := e.do(t, "POST", "/api/v1/commits/"+itoa(id)+"/rereview", adminTok, nil)
		return code
	}

	if code := post(e.ids["c3"]); code != 202 { // skipped with a reason
		t.Fatalf("rereview skipped commit = %d", code)
	}
	if s, reason := status(t, e.ids["c3"]); s != "pending" || reason != nil {
		t.Fatalf("status = %s reason = %v", s, reason)
	}
	if jobs(t) != 1 {
		t.Fatalf("jobs = %d, want 1", jobs(t))
	}
	var arg string
	pool.QueryRow(context.Background(), `SELECT args::text FROM river_job`).Scan(&arg)
	if !strings.Contains(arg, itoa(e.ids["c3"])) {
		t.Errorf("job args = %s", arg)
	}

	// Asking again while the job waits does not queue a second one.
	if code := post(e.ids["c3"]); code != 202 || jobs(t) != 1 {
		t.Fatalf("repeat: code %d, jobs %d", code, jobs(t))
	}

	// Once that job has finished, a new request must queue again (River's
	// default uniqueness would otherwise swallow it for the next day).
	exec(t, `UPDATE river_job SET state = 'completed', finalized_at = now()`)
	exec(t, `UPDATE commits SET review_status = 'done' WHERE id = $1`, e.ids["c3"])
	if code := post(e.ids["c3"]); code != 202 || jobs(t) != 2 {
		t.Fatalf("after completion: code %d, jobs %d, want 202 and 2", code, jobs(t))
	}

	exec(t, `UPDATE commits SET review_status = 'running' WHERE id = $1`, e.ids["c1"])
	if code := post(e.ids["c1"]); code != 409 {
		t.Errorf("running commit = %d, want 409", code)
	}
	if code := post(e.ids["w1"]); code != 409 { // repo has review disabled
		t.Errorf("disabled repo = %d, want 409", code)
	}
	if code := post(e.ids["c5"]); code != 409 { // merge commit
		t.Errorf("merge commit = %d, want 409", code)
	}
	if code := post(99999); code != 404 {
		t.Errorf("unknown = %d, want 404", code)
	}
	if s, _ := status(t, e.ids["c1"]); s != "running" {
		t.Errorf("a refused rereview changed the status to %s", s)
	}
	if jobs(t) != 2 {
		t.Errorf("refused requests enqueued jobs: %d", jobs(t))
	}
}

func TestRereviewRollsBackWhenEnqueueFails(t *testing.T) {
	e := setup(t)
	exec(t, `ALTER TABLE river_job RENAME TO river_job_off`)
	defer pool.Exec(context.Background(), `ALTER TABLE IF EXISTS river_job_off RENAME TO river_job`)
	code, _ := e.do(t, "POST", "/api/v1/commits/"+itoa(e.ids["c1"])+"/rereview", adminTok, nil)
	exec(t, `ALTER TABLE river_job_off RENAME TO river_job`)
	if code != 500 {
		t.Fatalf("code = %d, want 500", code)
	}
	if s, _ := status(t, e.ids["c1"]); s != "done" {
		t.Fatalf("status = %s: a commit must not be left pending without a job", s)
	}
}

func TestUsers(t *testing.T) {
	e := setup(t)
	var p struct {
		Items []struct {
			ID       int64    `json:"id"`
			Name     string   `json:"display_name"`
			Linked   bool     `json:"linked"`
			Commits  int      `json:"commits"`
			Reviewed int      `json:"reviewed"`
			AvgScore *float64 `json:"avg_score"`
			Findings int      `json:"findings"`
		} `json:"items"`
		Total int `json:"total"`
	}
	e.get(t, "/api/v1/users?sort=commits", &p)
	if p.Total != 3 || p.Items[0].Name != "Alice A" || !p.Items[0].Linked || p.Items[0].Commits != 4 ||
		p.Items[0].Reviewed != 2 || p.Items[0].AvgScore == nil || *p.Items[0].AvgScore != 95.5 || p.Items[0].Findings != 3 {
		t.Errorf("alice = %+v", p.Items[0])
	}
	if rob := p.Items[1]; rob.Name != "Rob" || rob.Linked || rob.Commits != 1 || rob.AvgScore != nil {
		t.Errorf("rob = %+v", rob)
	}
	e.get(t, "/api/v1/users?q=ALI", &p)
	if p.Total != 1 || p.Items[0].Name != "Alice A" {
		t.Errorf("q: %+v", p)
	}
	e.get(t, "/api/v1/users?sort=score", &p)
	if p.Items[0].Name != "Alice A" { // NULL scores sort last
		t.Errorf("score sort: %+v", p.Items)
	}
	// Window: with all of alice's commits old, her stats are empty but she is listed.
	exec(t, `UPDATE commits SET committed_at = now() - interval '90 days' WHERE author_user_id = $1`, e.ids["alice"])
	e.get(t, "/api/v1/users?days=30", &p)
	for _, u := range p.Items {
		if u.Name == "Alice A" && (u.Commits != 0 || u.AvgScore != nil) {
			t.Errorf("window ignored: %+v", u)
		}
	}
}

func TestUserDetailTrend(t *testing.T) {
	e := setup(t)
	exec(t, `UPDATE commits SET committed_at = now() - interval '20 days' WHERE hash = 'c20000'`)
	var d struct {
		DisplayName string `json:"display_name"`
		Commits     int    `json:"commits"`
		Trend       []struct {
			Week    string `json:"week"`
			Commits int    `json:"commits"`
		} `json:"trend"`
	}
	e.get(t, "/api/v1/users/"+itoa(e.ids["alice"]), &d)
	sum := 0
	for _, w := range d.Trend {
		sum += w.Commits
		if _, err := time.Parse("2006-01-02", w.Week); err != nil {
			t.Errorf("week %q", w.Week)
		}
	}
	if d.DisplayName != "Alice A" || d.Commits != 4 || sum != 4 || len(d.Trend) < 2 {
		t.Errorf("detail = %+v", d)
	}
	for i := 1; i < len(d.Trend); i++ {
		if d.Trend[i-1].Week >= d.Trend[i].Week {
			t.Errorf("trend not ascending: %+v", d.Trend)
		}
	}
}

func TestOverviewUsage(t *testing.T) {
	e := setup(t)
	// Pin run times explicitly instead of relying on the clock.
	exec(t, `UPDATE reviews SET created_at = now() - interval '3 days'`) // everything old
	exec(t, `UPDATE reviews SET created_at = now(), tokens_in = 1000, tokens_out = 100, cost_usd = 0.25
	         WHERE commit_id = $1`, e.ids["c1"])
	exec(t, `UPDATE reviews SET created_at = now(), tokens_in = 3000, tokens_out = 300, cost_usd = 0.75
	         WHERE commit_id = $1 AND score = 100`, e.ids["c2"]) // a re-review: its cost counts too
	exec(t, `UPDATE reviews SET tokens_in = 9999, tokens_out = 999, cost_usd = 9 WHERE commit_id = $1 AND score = 10`, e.ids["c2"]) // 3 days old

	var o struct {
		Usage struct {
			Runs      int      `json:"runs"`
			Measured  int      `json:"measured_runs"`
			TokensIn  int64    `json:"tokens_in"`
			TokensOut int64    `json:"tokens_out"`
			CostUSD   float64  `json:"cost_usd"`
			Avg       *float64 `json:"avg_cost_usd"`
		} `json:"usage"`
		Series []struct {
			Day  string  `json:"day"`
			Cost float64 `json:"cost_usd"`
		} `json:"series"`
	}
	e.get(t, "/api/v1/overview?days=1", &o)
	u := o.Usage
	if u.Runs != 2 || u.Measured != 2 || u.TokensIn != 4000 || u.TokensOut != 400 || u.CostUSD != 1 || u.Avg == nil || *u.Avg != 0.5 {
		t.Errorf("1-day usage = %+v", u)
	}
	if len(o.Series) != 1 || o.Series[0].Cost != 1 {
		t.Errorf("series cost = %+v", o.Series)
	}
	e.get(t, "/api/v1/overview?days=7", &o)
	if o.Usage.Runs != 3 || o.Usage.CostUSD != 10 {
		t.Errorf("7-day usage = %+v", o.Usage)
	}

	// Unmeasured (mock) runs count as runs but add nothing and do not skew the average.
	exec(t, `UPDATE reviews SET tokens_in = NULL, tokens_out = NULL, cost_usd = NULL WHERE commit_id = $1 AND score = 100`, e.ids["c2"])
	e.get(t, "/api/v1/overview?days=1", &o)
	if o.Usage.Runs != 2 || o.Usage.Measured != 1 || o.Usage.CostUSD != 0.25 || o.Usage.Avg == nil || *o.Usage.Avg != 0.25 {
		t.Errorf("with an unmeasured run = %+v", o.Usage)
	}

	// Failed attempts add to the totals and are reported on their own; the
	// per-run average stays about stored reviews.
	exec(t, `INSERT INTO review_attempts (commit_id, attempt, error, tokens_in, tokens_out, cost_usd) VALUES
	         ($1, 1, 'bad json', 500, 50, 0.5), ($1, 2, 'killed', NULL, NULL, NULL)`, e.ids["c1"])
	exec(t, `INSERT INTO review_attempts (commit_id, attempt, error, tokens_in, tokens_out, cost_usd, created_at)
	         VALUES ($1, 1, 'old', 7, 7, 7, now() - interval '3 days')`, e.ids["c1"])
	var w struct {
		Usage struct {
			CostUSD float64 `json:"cost_usd"`
			Avg     float64 `json:"avg_cost_usd"`
			Wasted  struct {
				Runs     int     `json:"runs"`
				Measured int     `json:"measured_runs"`
				CostUSD  float64 `json:"cost_usd"`
				TokensIn int64   `json:"tokens_in"`
			} `json:"wasted"`
		} `json:"usage"`
		Series []struct {
			Cost float64 `json:"cost_usd"`
		} `json:"series"`
	}
	e.get(t, "/api/v1/overview?days=1", &w)
	if w.Usage.Wasted.Runs != 2 || w.Usage.Wasted.Measured != 1 || w.Usage.Wasted.CostUSD != 0.5 || w.Usage.Wasted.TokensIn != 500 ||
		w.Usage.CostUSD != 0.75 || w.Usage.Avg != 0.25 || len(w.Series) != 1 || w.Series[0].Cost != 0.75 {
		t.Errorf("with failed attempts = %+v", w)
	}

	// The commit detail shows the run's own numbers, null when unmeasured.
	var d struct {
		Review struct {
			TokensIn *int     `json:"tokens_in"`
			Cost     *float64 `json:"cost_usd"`
		} `json:"review"`
	}
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c1"]), &d)
	if d.Review.TokensIn == nil || *d.Review.TokensIn != 1000 || d.Review.Cost == nil || *d.Review.Cost != 0.25 {
		t.Errorf("c1 review usage = %+v", d.Review)
	}
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c2"]), &d)
	if d.Review.TokensIn != nil || d.Review.Cost != nil {
		t.Errorf("unmeasured review must be null: %+v", d.Review)
	}
}

func TestHealth(t *testing.T) {
	e := setup(t)
	exec(t, `TRUNCATE webhook_events`)
	type health struct {
		Status   string   `json:"status"`
		Problems []string `json:"problems"`
		Webhooks struct {
			Unprocessed int  `json:"unprocessed"`
			Oldest      *int `json:"oldest_age_seconds"`
		} `json:"webhooks"`
		Stuck struct {
			Commits int `json:"commits"`
		} `json:"stuck"`
		LastPoll      *struct{ OK bool } `json:"last_poll"`
		LastReconcile *struct {
			Commits int `json:"commits"`
		} `json:"last_reconcile"`
	}
	var h health
	e.get(t, "/api/v1/health", &h)
	// Fresh pending commits are inside the grace period: nothing is wrong yet.
	if h.Status != "ok" || len(h.Problems) != 0 || h.Stuck.Commits != 0 || h.Webhooks.Unprocessed != 0 || h.Webhooks.Oldest != nil || h.LastPoll != nil || h.LastReconcile != nil {
		t.Fatalf("healthy = %+v", h)
	}
	if code, _ := e.do(t, "GET", "/api/v1/health", "", nil); code != 401 {
		t.Errorf("anonymous health = %d, want 401", code)
	}

	// Pending commits that lost their job, and a delivery nobody processed.
	exec(t, `UPDATE commits SET created_at = now() - interval '1 hour' WHERE review_status = 'pending'`)
	exec(t, `INSERT INTO webhook_events (request_uuid, event_key, payload, received_at) VALUES ('u1', 'repo:push', '{}', now() - interval '1 hour')`)
	exec(t, `INSERT INTO river_job (kind, state, args, max_attempts, finalized_at, metadata) VALUES
		('poll_repos', 'discarded', '{}', 1, now(), '{}'),
		('reconcile', 'completed', '{}', 3, now() - interval '2 hours', '{"output":{"commits":3}}')`)
	h = health{}
	e.get(t, "/api/v1/health", &h)
	if h.Status != "degraded" || h.Stuck.Commits != 2 || h.Webhooks.Unprocessed != 1 || h.Webhooks.Oldest == nil || *h.Webhooks.Oldest < 3500 {
		t.Fatalf("degraded = %+v", h)
	}
	if h.LastPoll == nil || h.LastPoll.OK || h.LastReconcile == nil || h.LastReconcile.Commits != 3 {
		t.Errorf("last poll/reconcile = %+v / %+v", h.LastPoll, h.LastReconcile)
	}
	if len(h.Problems) != 4 { // webhook wait, stuck, failed poll, reconcile silent
		t.Errorf("problems = %q", h.Problems)
	}

	// A live job for a commit means it is not stuck.
	exec(t, `INSERT INTO river_job (kind, state, args, max_attempts) SELECT 'review_commit', 'available', jsonb_build_object('commit_id', id), 3 FROM commits WHERE review_status = 'pending'`)
	h = health{}
	e.get(t, "/api/v1/health", &h)
	if h.Stuck.Commits != 0 {
		t.Errorf("commits with a live job are stuck: %+v", h)
	}
}

func (e *env) post(t *testing.T, path, tok string, body any) (int, string) {
	t.Helper()
	code, b := e.do(t, "POST", path, tok, body)
	return code, string(b)
}

func TestFixWorkflowOverHTTP(t *testing.T) {
	e := setup(t)
	var rv int64
	var fids []int64
	if err := pool.QueryRow(context.Background(), `SELECT r.id FROM reviews r JOIN commits c ON c.id = r.commit_id WHERE c.hash = 'c10000'`).Scan(&rv); err != nil {
		t.Fatal(err)
	}
	rows, _ := pool.Query(context.Background(), `SELECT id FROM review_findings WHERE review_id = $1 ORDER BY id`, rv)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		fids = append(fids, id)
	}
	rows.Close()
	if len(fids) != 3 {
		t.Fatalf("seed has %d findings", len(fids))
	}
	fp := func(i int, action string) string { return fmt.Sprintf("/api/v1/findings/%d/%s", fids[i], action) }
	rp := fmt.Sprintf("/api/v1/reviews/%d/close", rv)

	type fixView struct {
		Open   int  `json:"open_findings"`
		Closed bool `json:"review_closed"`
	}
	listFix := func() (fixView, string) {
		var l struct {
			Items []struct {
				Hash string `json:"hash"`
				fixView
			}
		}
		e.get(t, "/api/v1/commits?repo_id="+fmt.Sprint(e.ids["api"]), &l)
		for _, c := range l.Items {
			if strings.HasPrefix(c.Hash, "c1") {
				return c.fixView, ""
			}
		}
		return fixView{}, "c1 missing"
	}
	overviewFix := func() (o struct {
		F struct {
			Open   int `json:"open_findings"`
			Fixed  int `json:"fixed_findings"`
			Ready  int `json:"reviews_ready_to_close"`
			Closed int `json:"reviews_closed"`
		} `json:"fix"`
	}) {
		e.get(t, "/api/v1/overview?days=7", &o)
		return
	}
	if f, msg := listFix(); msg != "" || f.Open != 3 || f.Closed {
		t.Fatalf("list before = %+v %s", f, msg)
	}
	if o := overviewFix(); o.F.Open != 3 || o.F.Fixed != 0 || o.F.Ready != 0 || o.F.Closed != 0 {
		t.Fatalf("overview before = %+v", o.F)
	}
	hasC1 := func(fix string) bool {
		var l commitList
		e.get(t, "/api/v1/commits?fix="+fix, &l)
		return strings.Contains(hashes(l), "c1")
	}
	if !hasC1("open") || hasC1("ready") || hasC1("closed") {
		t.Fatalf("fix filter before: open=%v ready=%v closed=%v", hasC1("open"), hasC1("ready"), hasC1("closed"))
	}
	if code, _ := e.do(t, "GET", "/api/v1/commits?fix=bogus", viewerTok, nil); code != 400 {
		t.Fatalf("fix=bogus = %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/v1/pull-requests?fix=bogus", viewerTok, nil); code != 400 {
		t.Fatalf("pr fix=bogus = %d", code)
	}

	// Who am I?
	var me struct {
		Role string `json:"role"`
		User *struct {
			ID   int64
			Name string
		} `json:"user"`
	}
	code, b := e.do(t, "GET", "/api/v1/me", aliceTok, nil)
	if err := json.Unmarshal(b, &me); err != nil || code != 200 || me.Role != "author" || me.User == nil || me.User.Name != "Alice A" {
		t.Fatalf("me = %d %s", code, b)
	}
	code, b = e.do(t, "GET", "/api/v1/me", viewerTok, nil)
	if code != 200 || !strings.Contains(string(b), `"user":null`) {
		t.Fatalf("viewer me = %d %s", code, b)
	}

	// Role gates.
	for _, c := range []struct {
		path, tok string
		want      int
	}{
		{fp(0, "fixed"), "", 401},
		{fp(0, "fixed"), viewerTok, 403},
		{fp(0, "reopen"), aliceTok, 403}, // an author is not a reviewer
		{rp, aliceTok, 403},
		{fp(0, "fixed"), ghostTok, 403}, // user does not exist
		{fp(0, "fixed"), robTok, 403},   // not their commit
		{fp(0, "fixed"), samTok, 403},   // reviewers do not fix other people's findings
		{"/api/v1/findings/0/fixed", aliceTok, 400},
		{"/api/v1/findings/999999/fixed", aliceTok, 404},
	} {
		if code, body := e.post(t, c.path, c.tok, nil); code != c.want {
			t.Errorf("POST %s as %q = %d %s, want %d", c.path, c.tok, code, body, c.want)
		}
	}

	// Bad body.
	if code, _ := e.post(t, fp(0, "fixed"), aliceTok, map[string]any{"nope": 1}); code != 400 {
		t.Errorf("unknown field = %d, want 400", code)
	}

	// The author fixes two findings; the third stays open.
	for i := 0; i < 2; i++ {
		if code, body := e.post(t, fp(i, "fixed"), aliceTok, map[string]string{"note": "done"}); code != 200 {
			t.Fatalf("fixed %d = %d %s", i, code, body)
		}
	}
	if code, _ := e.post(t, fp(0, "fixed"), aliceTok, nil); code != 409 {
		t.Errorf("fixing twice = %d, want 409", code)
	}

	if o := overviewFix(); o.F.Open != 1 || o.F.Fixed != 2 || o.F.Ready != 0 {
		t.Errorf("overview with one open finding = %+v", o.F)
	}

	// Review: not own, not while open.
	if code, body := e.post(t, rp, aliceLeadTok, nil); code != 403 || !strings.Contains(body, "your own work") {
		t.Errorf("closing own review = %d %s", code, body)
	}
	if code, body := e.post(t, rp, samTok, nil); code != 409 || !strings.Contains(body, "1 findings are still open") {
		t.Errorf("closing with an open finding = %d %s", code, body)
	}
	if code, _ := e.post(t, fp(1, "reopen"), samTok, nil); code != 409 {
		t.Errorf("reopen without a note = %d, want 409", code)
	}
	if code, body := e.post(t, fp(1, "reopen"), samTok, map[string]string{"note": "still racy"}); code != 200 {
		t.Fatalf("reopen = %d %s", code, body)
	}
	if code, body := e.post(t, fp(2, "dismiss"), adminSamTok, map[string]string{"note": "false positive"}); code != 200 {
		t.Fatalf("dismiss = %d %s", code, body)
	}
	if code, _ := e.post(t, fp(1, "fixed"), aliceTok, nil); code != 200 {
		t.Fatal("author fixes again")
	}
	if o := overviewFix(); o.F.Open != 0 || o.F.Fixed != 2 || o.F.Ready != 1 || o.F.Closed != 0 {
		t.Errorf("overview ready to close = %+v", o.F)
	}
	if code, body := e.post(t, rp, samTok, map[string]string{"note": "ok"}); code != 200 {
		t.Fatalf("close = %d %s", code, body)
	}

	if f, _ := listFix(); f.Open != 0 || !f.Closed {
		t.Errorf("list after = %+v", f)
	}
	if o := overviewFix(); o.F.Open != 0 || o.F.Fixed != 0 || o.F.Ready != 0 || o.F.Closed != 1 {
		t.Errorf("overview after = %+v", o.F)
	}

	// The detail shows all of it.
	var d struct {
		Review struct {
			Closed *struct {
				By   struct{ Name string }
				Note string
			} `json:"closed"`
			Findings []struct {
				ID      int64
				Status  string
				History []struct{ Action string }
			}
		}
	}
	e.get(t, fmt.Sprintf("/api/v1/commits/%d", e.ids["c1"]), &d)
	if d.Review.Closed == nil || d.Review.Closed.By.Name != "Sam Senior" || d.Review.Closed.Note != "ok" {
		t.Errorf("closed = %+v", d.Review.Closed)
	}
	got := map[int64]string{}
	hist := map[int64]int{}
	for _, f := range d.Review.Findings {
		got[f.ID], hist[f.ID] = f.Status, len(f.History)
	}
	if got[fids[0]] != "fixed" || got[fids[1]] != "fixed" || got[fids[2]] != "dismissed" || hist[fids[1]] != 3 {
		t.Errorf("statuses = %v history = %v", got, hist)
	}
}

// An admin token needs no user: it may fix on the author's behalf, send back,
// dismiss and close. What it does is recorded without a name.
func TestAdminTokenWithoutUserRunsTheWholeWorkflow(t *testing.T) {
	e := setup(t)
	var rv int64
	if err := pool.QueryRow(context.Background(), `SELECT r.id FROM reviews r JOIN commits c ON c.id = r.commit_id WHERE c.hash = 'c10000'`).Scan(&rv); err != nil {
		t.Fatal(err)
	}
	var fids []int64
	rows, _ := pool.Query(context.Background(), `SELECT id FROM review_findings WHERE review_id = $1 ORDER BY id`, rv)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		fids = append(fids, id)
	}
	rows.Close()
	fp := func(i int, action string) string { return fmt.Sprintf("/api/v1/findings/%d/%s", fids[i], action) }
	rp := fmt.Sprintf("/api/v1/reviews/%d/close", rv)
	do := func(path string, tok string, note string) {
		t.Helper()
		var body any
		if note != "" {
			body = map[string]string{"note": note}
		}
		if code, b := e.post(t, path, tok, body); code != 200 {
			t.Fatalf("POST %s = %d %s", path, code, b)
		}
	}

	if code, _ := e.post(t, fp(0, "fixed"), viewerTok, nil); code != 403 {
		t.Fatalf("viewer = %d, want 403", code)
	}
	do(fp(0, "fixed"), adminTok, "fixed for the author")
	do(fp(1, "fixed"), adminTok, "")
	do(fp(2, "dismiss"), adminTok, "not a problem")
	do(fp(1, "reopen"), adminTok, "not good enough")
	do(fp(1, "fixed"), adminTok, "")
	do(rp, adminTok, "closed by admin")
	if code, _ := e.post(t, rp, adminTok, nil); code != 409 {
		t.Errorf("closing twice = %d, want 409", code)
	}
	do(fp(0, "reopen"), adminTok, "regressed") // reopens the review as well

	var closed bool
	var by *int64
	var events, unnamed int
	pool.QueryRow(context.Background(), `SELECT closed_at IS NOT NULL FROM reviews WHERE id = $1`, rv).Scan(&closed)
	pool.QueryRow(context.Background(), `SELECT closed_by FROM reviews WHERE id = $1`, rv).Scan(&by)
	pool.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE actor_id IS NULL) FROM finding_events`).Scan(&events, &unnamed)
	if closed || by != nil || events != 6 || unnamed != 6 {
		t.Errorf("closed=%v by=%v events=%d unnamed=%d", closed, by, events, unnamed)
	}

	// An admin who is linked to a user still may not review that user's own work.
	if code, body := e.post(t, fp(0, "fixed"), adminSamTok, nil); code != 200 {
		t.Fatalf("linked admin fixing = %d %s", code, body)
	}
	exec(t, `UPDATE commits SET author_user_id = $1 WHERE hash = 'c10000'`, e.ids["sam"])
	if code, body := e.post(t, fp(0, "reopen"), adminSamTok, map[string]string{"note": "x"}); code != 403 || !strings.Contains(body, "your own work") {
		t.Errorf("linked admin on own work = %d %s", code, body)
	}
}

func TestDetailShowsFailedAttempts(t *testing.T) {
	e := setup(t)
	type fa struct {
		FailedAttempts struct {
			Count     int     `json:"count"`
			Measured  int     `json:"measured"`
			TokensIn  int64   `json:"tokens_in"`
			CostUSD   float64 `json:"cost_usd"`
			LastError *string `json:"last_error"`
		} `json:"failed_attempts"`
	}
	var d fa
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c1"]), &d)
	if d.FailedAttempts.Count != 0 || d.FailedAttempts.LastError != nil {
		t.Fatalf("no attempts yet: %+v", d.FailedAttempts)
	}
	exec(t, `INSERT INTO review_attempts (commit_id, attempt, error, tokens_in, tokens_out, cost_usd, created_at) VALUES
	         ($1, 1, 'first', 100, 10, 0.1, now() - interval '1 hour'), ($1, 2, 'second', NULL, NULL, NULL, now())`, e.ids["c1"])
	d = fa{}
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c1"]), &d)
	f := d.FailedAttempts
	if f.Count != 2 || f.Measured != 1 || f.TokensIn != 100 || f.CostUSD != 0.1 || f.LastError == nil || *f.LastError != "second" {
		t.Errorf("commit failed attempts = %+v", f)
	}
	e.get(t, "/api/v1/commits/"+itoa(e.ids["c2"]), &d)
	if d.FailedAttempts.Count != 0 {
		t.Errorf("attempts leaked to another commit: %+v", d.FailedAttempts)
	}
}

func TestMyWork(t *testing.T) {
	e := setup(t)
	work := func(tok string) store.MyWork {
		t.Helper()
		code, b := e.do(t, "GET", "/api/v1/me/work", tok, nil)
		if code != 200 {
			t.Fatalf("me/work = %d %s", code, b)
		}
		var w store.MyWork
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	if code, _ := e.do(t, "GET", "/api/v1/me/work", "", nil); code != 401 {
		t.Fatalf("no token = %d", code)
	}

	// Alice's open findings, worst first; the stale review of c2 does not count.
	w := work(aliceTok)
	if len(w.ToFix.Items) != 1 || w.ToFix.TotalFindings != 3 || len(w.ToReview) != 0 {
		t.Fatalf("alice: %+v", w)
	}
	it := w.ToFix.Items[0]
	if it.Kind != "commit" || it.ID != e.ids["c1"] || it.Title != "Fix login" || it.Repository != "acme/api" || it.Findings != 3 {
		t.Fatalf("item: %+v", it)
	}
	var sev []string
	var fids []int64
	for _, f := range it.Open {
		sev = append(sev, f.Severity)
		fids = append(fids, f.ID)
		if f.SentBack != nil {
			t.Fatalf("nothing was sent back yet: %+v", f)
		}
	}
	if fmt.Sprint(sev) != "[critical major minor]" {
		t.Fatalf("order = %v", sev)
	}

	// Nobody else has anything to fix; a token with no user gets empty lists, not null.
	for _, tok := range []string{robTok, viewerTok, adminTok} {
		if w := work(tok); len(w.ToFix.Items) != 0 || w.ToFix.TotalFindings != 0 || len(w.ToReview) != 0 {
			t.Fatalf("%s: %+v", tok, w)
		}
	}
	if _, b := e.do(t, "GET", "/api/v1/me/work", viewerTok, nil); !strings.Contains(string(b), `"items":[]`) || !strings.Contains(string(b), `"to_review":[]`) {
		t.Fatalf("empty lists must be arrays: %s", b)
	}

	// A pull request counts while it matters; a declined one does not.
	for _, st := range []string{"OPEN", "DECLINED"} {
		pr := must(t, `INSERT INTO pull_requests (repo_id, bb_pr_id, title, state, author_user_id) VALUES ($1, $2, 'PR '||$3::text, $3, $4) RETURNING id`,
			e.ids["api"], map[string]int{"OPEN": 7, "DECLINED": 8}[st], st, e.ids["alice"])
		rv := must(t, `INSERT INTO reviews (pr_id, model, prompt_version, score, summary) VALUES ($1, 'mock', 'v1', 50, 's') RETURNING id`, pr)
		exec(t, `INSERT INTO review_findings (review_id, file_path, line_start, line_end, severity, category, title, explanation) VALUES ($1, 'p.go', 1, 1, 'minor', 'bug', 'pr finding', 'x')`, rv)
	}
	w = work(aliceTok)
	if len(w.ToFix.Items) != 2 || w.ToFix.TotalFindings != 4 {
		t.Fatalf("with a pull request: %+v", w.ToFix)
	}
	if p := w.ToFix.Items[1]; p.Kind != "pull_request" || p.Title != "PR OPEN" || p.Number == nil || *p.Number != 7 {
		t.Fatalf("pull request item: %+v", p)
	}
	exec(t, `DELETE FROM pull_requests WHERE state = 'OPEN'`)

	// Once everything is fixed it is the reviewer's turn, and only a reviewer sees it.
	for _, id := range fids {
		if code, b := e.post(t, fmt.Sprintf("/api/v1/findings/%d/fixed", id), aliceTok, nil); code != 200 {
			t.Fatalf("fixed = %d %s", code, b)
		}
	}
	if w := work(aliceTok); len(w.ToFix.Items) != 0 || len(w.ToReview) != 0 {
		t.Fatalf("alice after fixing: %+v", w)
	}
	w = work(samTok)
	if len(w.ToReview) != 1 || w.ToReview[0].ID != e.ids["c1"] || w.ToReview[0].Author == nil || *w.ToReview[0].Author != "Alice A" {
		t.Fatalf("sam: %+v", w.ToReview)
	}

	// Sending one back puts it on Alice's list with the reason, and takes the commit off Sam's.
	if code, b := e.post(t, fmt.Sprintf("/api/v1/findings/%d/reopen", fids[0]), samTok, map[string]string{"note": "still reachable"}); code != 200 {
		t.Fatalf("reopen = %d %s", code, b)
	}
	w = work(aliceTok)
	if len(w.ToFix.Items) != 1 || len(w.ToFix.Items[0].Open) != 1 {
		t.Fatalf("alice after send back: %+v", w)
	}
	sb := w.ToFix.Items[0].Open[0].SentBack
	if sb == nil || sb.By == nil || sb.By.Name != "Sam Senior" || sb.Note == nil || *sb.Note != "still reachable" {
		t.Fatalf("sent back = %+v", sb)
	}
	if w := work(samTok); len(w.ToReview) != 0 {
		t.Fatalf("sam still has it: %+v", w.ToReview)
	}
}

func TestAuditLog(t *testing.T) {
	e := setup(t)
	exec(t, `TRUNCATE audit_log`)

	var rv int64
	if err := pool.QueryRow(context.Background(), `SELECT r.id FROM reviews r JOIN commits c ON c.id = r.commit_id WHERE c.hash = 'c10000'`).Scan(&rv); err != nil {
		t.Fatal(err)
	}
	var fids []int64
	rows, _ := pool.Query(context.Background(), `SELECT id FROM review_findings WHERE review_id = $1 ORDER BY id`, rv)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		fids = append(fids, id)
	}
	rows.Close()
	f := func(i int, action string) string { return fmt.Sprintf("/api/v1/findings/%d/%s", fids[i], action) }

	// Things that fail or change nothing leave no trace.
	if code, _ := e.post(t, f(0, "fixed"), robTok, nil); code != 403 {
		t.Fatalf("someone else's finding = %d", code)
	}
	if code, _ := e.post(t, f(1, "dismiss"), samTok, map[string]string{}); code != 409 {
		t.Fatalf("dismiss without a note = %d", code)
	}
	web := "/api/v1/repositories/" + itoa(e.ids["web"])
	if code, _ := e.do(t, "PATCH", web, adminTok, map[string]bool{"review_enabled": false}); code != 200 { // already off
		t.Fatalf("no-op patch = %d", code)
	}

	// Things that work are logged, in order.
	if code, b := e.post(t, f(0, "fixed"), aliceTok, map[string]string{"note": "moved the check"}); code != 200 {
		t.Fatalf("fixed = %d %s", code, b)
	}
	if code, b := e.post(t, f(1, "dismiss"), samTok, map[string]string{"note": "not a bug"}); code != 200 {
		t.Fatalf("dismiss = %d %s", code, b)
	}
	if code, _ := e.do(t, "PATCH", web, adminTok, map[string]bool{"review_enabled": true}); code != 200 {
		t.Fatalf("enable = %d", code)
	}
	if code, b := e.post(t, "/api/v1/commits/"+itoa(e.ids["c1"])+"/rereview", adminSamTok, nil); code != 202 {
		t.Fatalf("rereview = %d %s", code, b)
	}

	type entry struct {
		Action string `json:"action"`
		Actor  struct {
			ID   *int64  `json:"id"`
			Name *string `json:"name"`
			Role string  `json:"role"`
		} `json:"actor"`
		Subject struct {
			Type string `json:"type"`
			ID   int64  `json:"id"`
		} `json:"subject"`
		Detail map[string]any `json:"detail"`
	}
	type auditPage struct {
		Total int     `json:"total"`
		Items []entry `json:"items"`
	}
	list := func(q string) auditPage {
		t.Helper()
		code, b := e.do(t, "GET", "/api/v1/audit"+q, adminTok, nil)
		if code != 200 {
			t.Fatalf("GET audit%s = %d %s", q, code, b)
		}
		var p auditPage
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	all := list("")
	var got []string
	for _, it := range all.Items {
		got = append(got, it.Action)
	}
	want := []string{"commit.rereview", "repository.review_enabled", "finding.dismissed", "finding.fixed"}
	if all.Total != 4 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit = %d %v, want newest first %v", all.Total, got, want)
	}
	fixed, dismissed, repo, rr := all.Items[3], all.Items[2], all.Items[1], all.Items[0]
	if fixed.Actor.Name == nil || *fixed.Actor.Name != "Alice A" || fixed.Actor.Role != "author" ||
		fixed.Subject.Type != "finding" || fixed.Subject.ID != fids[0] ||
		fixed.Detail["note"] != "moved the check" || fixed.Detail["from"] != "open" || fixed.Detail["to"] != "fixed" ||
		fixed.Detail["commit_id"] != float64(e.ids["c1"]) || fixed.Detail["review_id"] != float64(rv) {
		t.Errorf("fixed entry = %+v", fixed)
	}
	if dismissed.Actor.Role != "senior" || dismissed.Detail["note"] != "not a bug" {
		t.Errorf("dismissed entry = %+v", dismissed)
	}
	// A token that is not linked to a user is logged by role only.
	if repo.Actor.ID != nil || repo.Actor.Name != nil || repo.Actor.Role != "admin" || repo.Subject.Type != "repository" || repo.Detail["repository"] != "acme/web" {
		t.Errorf("repository entry = %+v", repo)
	}
	if rr.Actor.Role != "admin" || rr.Actor.Name == nil || *rr.Actor.Name != "Sam Senior" || rr.Subject.Type != "commit" || rr.Subject.ID != e.ids["c1"] {
		t.Errorf("rereview entry = %+v", rr)
	}

	// Filters.
	if n := list("?action=finding").Total; n != 2 {
		t.Errorf("action=finding = %d, want 2", n)
	}
	if n := list("?action=finding.fixed").Total; n != 1 {
		t.Errorf("action=finding.fixed = %d, want 1", n)
	}
	if n := list("?actor_id=" + itoa(e.ids["alice"])).Total; n != 1 {
		t.Errorf("actor_id=alice = %d, want 1", n)
	}
	if n := list("?subject_type=repository&subject_id=" + itoa(e.ids["web"])).Total; n != 1 {
		t.Errorf("repository filter = %d, want 1", n)
	}
	if p := list("?limit=1&offset=1"); p.Total != 4 || len(p.Items) != 1 || p.Items[0].Action != "repository.review_enabled" {
		t.Errorf("paging = %+v", p)
	}
	for _, q := range []string{"?actor_id=0", "?subject_type=bogus", "?since=yesterday", "?limit=0"} {
		if code, _ := e.do(t, "GET", "/api/v1/audit"+q, adminTok, nil); code != 400 {
			t.Errorf("%s = %d, want 400", q, code)
		}
	}

	// Only admins read it.
	for _, tok := range []string{viewerTok, aliceTok, samTok, ""} {
		want := 403
		if tok == "" {
			want = 401
		}
		if code, _ := e.do(t, "GET", "/api/v1/audit", tok, nil); code != want {
			t.Errorf("token %q: audit = %d, want %d", tok, code, want)
		}
	}

	// The log cannot be rewritten through the database either.
	if _, err := pool.Exec(context.Background(), `UPDATE audit_log SET actor_role = 'admin'`); err == nil {
		t.Error("audit_log was updated")
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM audit_log`); err == nil {
		t.Error("audit_log was deleted from")
	}

	// A closed review is logged too, and reopening it is its own entry.
	exec(t, `UPDATE review_findings SET status = 'fixed' WHERE review_id = $1 AND status = 'open'`, rv)
	if code, b := e.post(t, fmt.Sprintf("/api/v1/reviews/%d/close", rv), samTok, map[string]string{"note": "all good"}); code != 200 {
		t.Fatalf("close = %d %s", code, b)
	}
	if code, b := e.post(t, f(0, "reopen"), samTok, map[string]string{"note": "still broken"}); code != 200 {
		t.Fatalf("reopen = %d %s", code, b)
	}
	acts := map[string]bool{}
	for _, it := range list("?limit=50").Items {
		acts[it.Action] = true
	}
	for _, a := range []string{"review.closed", "review.reopened", "finding.reopened"} {
		if !acts[a] {
			t.Errorf("missing %s in %v", a, acts)
		}
	}
}
