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

type pullRequest struct {
	id                  int
	title, description  string
	source, dest, state string
	lastHash            string // source head while the branch exists
	created, updated    time.Time
	deleted             bool
}

type repo struct {
	workspace, slug string
	defaultBranch   string
	branches        map[string]string // name -> head hash
	prs             []*pullRequest
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

	accounts   map[string]*account          // uuid -> profile
	wsRoles    map[string]map[string]string // workspace -> uuid -> role
	repoPerms  map[string]map[string]string // "workspace/slug" -> uuid -> permission
	noPermsFor map[string]bool              // "workspace/slug" or "workspace" the token may not read
}

type account struct{ uuid, accountID, name, nickname, avatar string }

// AddAccount creates (or replaces) a Bitbucket account.
func (m *Mock) AddAccount(uuid, accountID, name, nickname, avatar string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[uuid] = &account{uuid, accountID, name, nickname, avatar}
}

// SetWorkspaceRole gives an account a role (owner, collaborator, member) in a
// workspace; an empty role removes it.
func (m *Mock) SetWorkspaceRole(workspace, uuid, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wsRoles[workspace] == nil {
		m.wsRoles[workspace] = map[string]string{}
	}
	if role == "" {
		delete(m.wsRoles[workspace], uuid)
		return
	}
	m.wsRoles[workspace][uuid] = role
}

// SetRepoPermission gives an account admin, write or read on a repository; an empty permission removes it.
func (m *Mock) SetRepoPermission(workspace, slug, uuid, permission string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := workspace + "/" + slug
	if m.repoPerms[k] == nil {
		m.repoPerms[k] = map[string]string{}
	}
	if permission == "" {
		delete(m.repoPerms[k], uuid)
		return
	}
	m.repoPerms[k][uuid] = permission
}

// DenyPermissions makes the permission endpoints answer 403 for a workspace
// ("ws") or a repository ("ws/slug"), like a token without admin scope.
func (m *Mock) DenyPermissions(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.noPermsFor[key] = true
}

func accountJSON(a *account) map[string]any {
	return map[string]any{
		"type": "user", "uuid": a.uuid, "account_id": a.accountID, "display_name": a.name, "nickname": a.nickname,
		"links": map[string]any{"avatar": map[string]any{"href": a.avatar}},
	}
}

func (m *Mock) permissionList(grants map[string]string) []any {
	keys := make([]string, 0, len(grants))
	for u := range grants {
		keys = append(keys, u)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, u := range keys {
		if a := m.accounts[u]; a != nil {
			out = append(out, map[string]any{"type": "permission", "permission": grants[u], "user": accountJSON(a)})
		}
	}
	return out
}

func New(diff string) *Mock {
	if strings.TrimSpace(diff) == "" {
		diff = SampleDiff
	}
	return &Mock{diff: diff, repos: map[string]*repo{}, commits: map[string]*commit{}, calls: map[string]int{}, queries: map[string][]string{},
		accounts: map[string]*account{}, wsRoles: map[string]map[string]string{}, repoPerms: map[string]map[string]string{}, noPermsFor: map[string]bool{}}
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
	for _, pr := range r.prs {
		if pr.state == "OPEN" && pr.source == branch {
			pr.updated = at.UTC() // a push to the source branch updates the pull request
		}
	}
	return c.hash
}

// AddPullRequest opens a pull request from source into dest (the main branch
// when empty) and returns its id. The source branch must exist.
func (m *Mock) AddPullRequest(workspace, slug, source, dest, title, description string) int {
	m.AddRepo(workspace, slug)
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.repos[workspace+"/"+slug]
	if dest == "" {
		dest = r.defaultBranch
	}
	now := time.Now().UTC()
	pr := &pullRequest{id: len(r.prs) + 1, title: title, description: description, source: source, dest: dest,
		state: "OPEN", lastHash: r.branches[source], created: now, updated: now}
	r.prs = append(r.prs, pr)
	return pr.id
}

// SetPullRequestState moves a pull request to MERGED, DECLINED or SUPERSEDED.
func (m *Mock) SetPullRequestState(workspace, slug string, id int, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pr := m.pr(workspace, slug, id); pr != nil {
		pr.state, pr.updated = state, time.Now().UTC()
	}
}

// SetPullRequestUpdated backdates a pull request's updated_on.
func (m *Mock) SetPullRequestUpdated(workspace, slug string, id int, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pr := m.pr(workspace, slug, id); pr != nil {
		pr.updated = at.UTC()
	}
}

// DeletePullRequest makes Bitbucket answer 404 for a pull request.
func (m *Mock) DeletePullRequest(workspace, slug string, id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pr := m.pr(workspace, slug, id); pr != nil {
		pr.deleted = true
	}
}

func (m *Mock) pr(workspace, slug string, id int) *pullRequest {
	r := m.repos[workspace+"/"+slug]
	if r == nil || id < 1 || id > len(r.prs) || r.prs[id-1].deleted {
		return nil
	}
	return r.prs[id-1]
}

// prJSON renders a pull request. Like Bitbucket it reports a 12-character head.
func (m *Mock) prJSON(r *repo, pr *pullRequest) map[string]any {
	if h, ok := r.branches[pr.source]; ok {
		pr.lastHash = h
	}
	short := pr.lastHash
	if len(short) > 12 {
		short = short[:12]
	}
	return map[string]any{
		"type": "pullrequest", "id": pr.id, "title": pr.title, "description": pr.description, "state": pr.state,
		"created_on": pr.created.Format(time.RFC3339Nano), "updated_on": pr.updated.Format(time.RFC3339Nano),
		"author":      map[string]any{"uuid": "{mock-dev}", "account_id": "mock-dev", "display_name": "Mock Dev", "nickname": "mockdev"},
		"source":      map[string]any{"branch": map[string]string{"name": pr.source}, "commit": map[string]string{"hash": short}},
		"destination": map[string]any{"branch": map[string]string{"name": pr.dest}, "commit": map[string]string{"hash": ""}},
	}
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
// ("repository", "repositories", "branches", "commits", "diff", "pullrequests", "pullrequest", "prdiff").
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
//	GET /repositories/{workspace}/{repo}/pullrequests     -> open pull requests (state filter)
//	GET /repositories/{workspace}/{repo}/pullrequests/{id}, .../{id}/diff
//
// Debug (not Bitbucket): POST /_mock/commit?repo=ws/slug&branch=main&message=...
//
//	POST /_mock/pullrequest?repo=ws/slug&source=branch&title=...  (or &id=1&state=MERGED)
func (m *Mock) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/diff/{hash}", func(w http.ResponseWriter, r *http.Request) {
		m.count("diff", r)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, m.diff)
	})
	notFound := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusNotFound, map[string]any{"type": "error", "error": map[string]string{"message": "No such pull request"}})
	}
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		m.count("pullrequests", r)
		m.mu.Lock()
		var prs []*pullRequest
		rp := m.repos[r.PathValue("workspace")+"/"+r.PathValue("repo")]
		if rp != nil {
			for _, pr := range rp.prs {
				if !pr.deleted && (r.URL.Query().Get("state") == "" || r.URL.Query().Get("state") == pr.state) {
					prs = append(prs, pr)
				}
			}
		}
		sort.SliceStable(prs, func(i, j int) bool { return prs[i].updated.After(prs[j].updated) }) // sort=-updated_on
		var vals []any
		for _, pr := range prs {
			vals = append(vals, m.prJSON(rp, pr))
		}
		m.mu.Unlock()
		paged(w, r, vals)
	})
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/pullrequests/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.count("pullrequest", r)
		id, _ := strconv.Atoi(r.PathValue("id"))
		m.mu.Lock()
		pr := m.pr(r.PathValue("workspace"), r.PathValue("repo"), id)
		var body map[string]any
		if pr != nil {
			body = m.prJSON(m.repos[r.PathValue("workspace")+"/"+r.PathValue("repo")], pr)
		}
		m.mu.Unlock()
		if body == nil {
			notFound(w)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/pullrequests/{id}/diff", func(w http.ResponseWriter, r *http.Request) {
		m.count("prdiff", r)
		id, _ := strconv.Atoi(r.PathValue("id"))
		m.mu.Lock()
		pr := m.pr(r.PathValue("workspace"), r.PathValue("repo"), id)
		m.mu.Unlock()
		if pr == nil {
			notFound(w)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, m.diff)
	})
	forbidden := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusForbidden, map[string]any{"type": "error", "error": map[string]string{"message": "Forbidden"}})
	}
	mux.HandleFunc("GET /workspaces/{workspace}/permissions", func(w http.ResponseWriter, r *http.Request) {
		m.count("workspace_permissions", r)
		ws := r.PathValue("workspace")
		m.mu.Lock()
		denied, vals := m.noPermsFor[ws], m.permissionList(m.wsRoles[ws])
		m.mu.Unlock()
		if denied {
			forbidden(w)
			return
		}
		paged(w, r, vals)
	})
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/permissions-config/users", func(w http.ResponseWriter, r *http.Request) {
		m.count("repo_permissions", r)
		k := r.PathValue("workspace") + "/" + r.PathValue("repo")
		m.mu.Lock()
		denied, vals := m.noPermsFor[k], m.permissionList(m.repoPerms[k])
		m.mu.Unlock()
		if denied {
			forbidden(w)
			return
		}
		paged(w, r, vals)
	})
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.count("user", r)
		id := r.PathValue("id")
		m.mu.Lock()
		var body map[string]any
		for _, a := range m.accounts {
			if a.uuid == id || a.accountID == id {
				body = accountJSON(a)
			}
		}
		m.mu.Unlock()
		if body == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"type": "error", "error": map[string]string{"message": "No such user"}})
			return
		}
		writeJSON(w, http.StatusOK, body)
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
	// Debug: open a pull request, or change the state of one (state=MERGED).
	mux.HandleFunc("POST /_mock/pullrequest", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ws, slug, ok := strings.Cut(q.Get("repo"), "/")
		if !ok || slug == "" {
			http.Error(w, "repo=workspace/slug required", http.StatusBadRequest)
			return
		}
		if st := q.Get("state"); st != "" {
			id, _ := strconv.Atoi(q.Get("id"))
			m.SetPullRequestState(ws, slug, id, st)
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": st})
			return
		}
		src := q.Get("source")
		if src == "" {
			http.Error(w, "source=branch required", http.StatusBadRequest)
			return
		}
		title := q.Get("title")
		if title == "" {
			title = "mock pull request"
		}
		writeJSON(w, http.StatusOK, map[string]int{"id": m.AddPullRequest(ws, slug, src, q.Get("dest"), title, q.Get("description"))})
	})
	return mux
}
