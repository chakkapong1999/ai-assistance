// Package mockbitbucket is a stand-in for the few Bitbucket Cloud endpoints
// the worker calls, for trying the pipeline without a Bitbucket account or
// real commits. Every commit gets the same diff. It also keeps a small
// in-memory set of repositories, branches and commits, so the poller can be
// tried (and tested) without Bitbucket.
package mockbitbucket

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SampleDiff is a small, realistic change (a Go handler with a few things a
// reviewer might flag) used when no diff file is given.
const SampleDiff = `diff --git a/internal/payments/handler.go b/internal/payments/handler.go
index 3b18e1c..9f2a7d4 100644
--- a/internal/payments/handler.go
+++ b/internal/payments/handler.go
@@ -21,9 +21,16 @@ func (h *Handler) Refund(w http.ResponseWriter, r *http.Request) {
 	id := r.URL.Query().Get("id")
-	amount, _ := strconv.Atoi(r.URL.Query().Get("amount"))
-	h.store.Refund(id, amount)
+	amount, _ := strconv.Atoi(r.URL.Query().Get("amount"))
+	rows, err := h.db.Query("SELECT balance FROM accounts WHERE id = '" + id + "'")
+	if err != nil {
+		log.Println(err)
+	}
+	defer rows.Close()
+	h.store.Refund(id, amount)
+	w.WriteHeader(http.StatusOK)
 }
`

// Handler serves the endpoints with no repositories; see Mock for more.
func Handler(diff string) http.Handler { return New(diff).Handler() }

type commit struct {
	hash, message, author string
	parents               []string
	date                  time.Time
	seq                   int
}

type repo struct {
	workspace, slug string
	defaultBranch   string
	branches        map[string]string // name -> head hash
}

// Mock is an in-memory Bitbucket: repositories with branches and a commit graph.
type Mock struct {
	diff string

	mu      sync.Mutex
	repos   map[string]*repo // "workspace/slug"
	commits map[string]*commit
	seq     int
	calls   map[string]int
	queries map[string][]string // kind -> raw query of each request
}

func New(diff string) *Mock {
	if strings.TrimSpace(diff) == "" {
		diff = SampleDiff
	}
	return &Mock{diff: diff, repos: map[string]*repo{}, commits: map[string]*commit{}, calls: map[string]int{}, queries: map[string][]string{}}
}

// AddRepo creates a repository whose main branch is "main" and has no commits yet.
func (m *Mock) AddRepo(workspace, slug string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repos[workspace+"/"+slug]; !ok {
		m.repos[workspace+"/"+slug] = &repo{workspace: workspace, slug: slug, defaultBranch: "main", branches: map[string]string{}}
	}
}

// AddCommit adds a commit at the tip of branch (created from the main branch
// if it does not exist) dated now, and returns its hash.
func (m *Mock) AddCommit(workspace, slug, branch, message string) string {
	return m.AddCommitAt(workspace, slug, branch, message, time.Now())
}

func (m *Mock) AddCommitAt(workspace, slug, branch, message string, at time.Time) string {
	m.AddRepo(workspace, slug)
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.repos[workspace+"/"+slug]
	parent, ok := r.branches[branch]
	if !ok {
		parent = r.branches[r.defaultBranch]
	}
	m.seq++
	sum := sha1.Sum([]byte(fmt.Sprintf("%s/%s:%d:%s", workspace, slug, m.seq, message)))
	c := &commit{hash: hex.EncodeToString(sum[:]), message: message, author: "Mock Dev <mock.dev@example.com>", date: at.UTC(), seq: m.seq}
	if parent != "" {
		c.parents = []string{parent}
	}
	m.commits[c.hash] = c
	r.branches[branch] = c.hash
	return c.hash
}

// DeleteBranch removes a branch (its commits stay).
func (m *Mock) DeleteBranch(workspace, slug, branch string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.repos[workspace+"/"+slug]; r != nil {
		delete(r.branches, branch)
	}
}

// Calls returns how many requests each endpoint kind has served
// ("repository", "repositories", "branches", "commits", "diff").
func (m *Mock) Calls(kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[kind]
}

func (m *Mock) count(kind string, r *http.Request) {
	m.mu.Lock()
	m.calls[kind]++
	m.queries[kind] = append(m.queries[kind], r.URL.RawQuery)
	m.mu.Unlock()
}

// Queries returns the raw query string of every request of that kind so far.
func (m *Mock) Queries(kind string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.queries[kind]...)
}

func repoJSON(r *repo) map[string]any {
	return map[string]any{
		"type":       "repository",
		"uuid":       "{repo-" + r.workspace + "-" + r.slug + "}",
		"name":       r.slug,
		"full_name":  r.workspace + "/" + r.slug,
		"project":    map[string]any{"uuid": "{proj-" + r.workspace + "}", "key": "MOCK", "name": "Mock project"},
		"workspace":  map[string]any{"uuid": "{ws-" + r.workspace + "}", "slug": r.workspace, "name": r.workspace},
		"mainbranch": map[string]any{"type": "branch", "name": r.defaultBranch},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// paged answers one page of values the way Bitbucket does: pagelen and page
// query parameters, and a "next" link while more remain.
func paged(w http.ResponseWriter, r *http.Request, values []any) {
	pagelen, _ := strconv.Atoi(r.URL.Query().Get("pagelen"))
	if pagelen < 1 || pagelen > 100 {
		pagelen = 10
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	lo := (page - 1) * pagelen
	if lo > len(values) {
		lo = len(values)
	}
	hi := min(lo+pagelen, len(values))
	out := map[string]any{"values": values[lo:hi], "pagelen": pagelen, "page": page}
	if values == nil {
		out["values"] = []any{}
	}
	if hi < len(values) {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(page+1))
		out["next"] = "http://" + r.Host + r.URL.Path + "?" + q.Encode()
	}
	writeJSON(w, http.StatusOK, out)
}

// reachable returns every commit reachable from ref (a branch name or a hash).
func (m *Mock) reachable(r *repo, ref string) map[string]bool {
	set := map[string]bool{}
	if ref == "" {
		return set
	}
	start := ref
	if h, ok := r.branches[ref]; ok {
		start = h
	}
	stack := []string{start}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		c := m.commits[h]
		if c == nil || set[h] {
			continue
		}
		set[h] = true
		stack = append(stack, c.parents...)
	}
	return set
}

func commitJSON(c *commit) map[string]any {
	ps := make([]map[string]string, len(c.parents))
	for i, p := range c.parents {
		ps[i] = map[string]string{"hash": p}
	}
	return map[string]any{
		"hash": c.hash, "message": c.message, "date": c.date.Format(time.RFC3339),
		"parents": ps, "author": map[string]any{"raw": c.author},
	}
}

// Handler serves the endpoints used by internal/bitbucket.
//
//	GET /repositories/{workspace}                         -> repositories
//	GET /repositories/{workspace}/{repo}                  -> repository
//	GET /repositories/{workspace}/{repo}/refs/branches    -> branches
//	GET /repositories/{workspace}/{repo}/commits          -> commits (include / exclude)
//	GET /repositories/{workspace}/{repo}/diff/{hash}      -> diff (the same for every commit)
//
// Debug (not Bitbucket): POST /_mock/commit?repo=ws/slug&branch=main&message=...
func (m *Mock) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/diff/{hash}", func(w http.ResponseWriter, r *http.Request) {
		m.count("diff", r)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, m.diff)
	})
	mux.HandleFunc("GET /repositories/{workspace}", func(w http.ResponseWriter, r *http.Request) {
		m.count("repositories", r)
		m.mu.Lock()
		var vals []any
		for _, rp := range m.repos {
			if rp.workspace == r.PathValue("workspace") {
				vals = append(vals, rp)
			}
		}
		sort.Slice(vals, func(i, j int) bool { return vals[i].(*repo).slug < vals[j].(*repo).slug })
		for i, v := range vals {
			vals[i] = repoJSON(v.(*repo))
		}
		m.mu.Unlock()
		paged(w, r, vals)
	})
	mux.HandleFunc("GET /repositories/{workspace}/{repo}", func(w http.ResponseWriter, r *http.Request) {
		m.count("repository", r)
		m.mu.Lock()
		rp := m.repos[r.PathValue("workspace")+"/"+r.PathValue("repo")]
		var body map[string]any
		if rp != nil {
			body = repoJSON(rp)
		}
		m.mu.Unlock()
		if body == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"type": "error", "error": map[string]string{"message": "No such repository"}})
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/refs/branches", func(w http.ResponseWriter, r *http.Request) {
		m.count("branches", r)
		m.mu.Lock()
		var vals []any
		if rp := m.repos[r.PathValue("workspace")+"/"+r.PathValue("repo")]; rp != nil {
			names := make([]string, 0, len(rp.branches))
			for n := range rp.branches {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				vals = append(vals, map[string]any{"type": "branch", "name": n, "target": map[string]string{"hash": rp.branches[n]}})
			}
		}
		m.mu.Unlock()
		paged(w, r, vals)
	})
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/commits", func(w http.ResponseWriter, r *http.Request) {
		m.count("commits", r)
		m.mu.Lock()
		var vals []any
		if rp := m.repos[r.PathValue("workspace")+"/"+r.PathValue("repo")]; rp != nil {
			q := r.URL.Query()
			inc := m.reachable(rp, q.Get("include"))
			if q.Get("include") == "" {
				inc = m.reachable(rp, rp.defaultBranch)
			}
			exc := m.reachable(rp, q.Get("exclude"))
			var cs []*commit
			for h := range inc {
				if !exc[h] {
					cs = append(cs, m.commits[h])
				}
			}
			sort.Slice(cs, func(i, j int) bool { return cs[i].seq > cs[j].seq }) // newest first
			for _, c := range cs {
				vals = append(vals, commitJSON(c))
			}
		}
		m.mu.Unlock()
		paged(w, r, vals)
	})
	mux.HandleFunc("POST /_mock/commit", func(w http.ResponseWriter, r *http.Request) {
		ws, slug, ok := strings.Cut(r.URL.Query().Get("repo"), "/")
		if !ok || slug == "" {
			http.Error(w, "repo=workspace/slug required", http.StatusBadRequest)
			return
		}
		branch := r.URL.Query().Get("branch")
		if branch == "" {
			branch = "main"
		}
		msg := r.URL.Query().Get("message")
		if msg == "" {
			msg = "mock commit"
		}
		writeJSON(w, http.StatusOK, map[string]string{"hash": m.AddCommit(ws, slug, branch, msg)})
	})
	return mux
}
