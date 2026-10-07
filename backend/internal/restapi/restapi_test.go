package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	h := restapi.New(store.NewDashboard(pool, rc), []config.APIToken{{Token: adminTok, Role: "admin"}, {Token: viewerTok, Role: "viewer"}}, discardLog())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

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
	exec(t, `INSERT INTO code_suggestions (finding_id, original_snippet, suggested_snippet, unified_diff) VALUES ($1, 'x', 'y', '--- a/a.go')`, fid)
	old := review("c2", 10, 120)
	finding(old, "old.go", 1, "critical", "stale")
	r2 := review("c2", 100, 5) // newer: this one counts
	_ = r2
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
				Title      string `json:"title"`
				Severity   string `json:"severity"`
				Suggestion *struct {
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
	if p.Total != 2 || p.Items[0].Name != "Alice A" || !p.Items[0].Linked || p.Items[0].Commits != 4 ||
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
