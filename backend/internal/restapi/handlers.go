package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

const (
	defaultLimit = 50
	maxLimit     = 200
	defaultDays  = 30
	maxDays      = 365
	maxQueryLen  = 200
)

type paramError string

func (e paramError) Error() string { return string(e) }

func intParam(r *http.Request, name string, def, min, max int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, paramError(name + " must be an integer between " + strconv.Itoa(min) + " and " + strconv.Itoa(max))
	}
	return n, nil
}

func idParam(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil && n > 0
}

func textParam(r *http.Request, name string) (string, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if len(v) > maxQueryLen {
		return "", paramError(name + " is too long")
	}
	return v, nil
}

func timeParam(r *http.Request, name string) (*time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, paramError(name + " must be an RFC 3339 timestamp")
	}
	return &t, nil
}

func badRequest(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, "bad_request", err.Error())
}

func badID(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "bad_request", "id must be a positive integer")
}

func (s *server) openapi(w http.ResponseWriter, _ *http.Request, _ string) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiSpec)
}

type meResponse struct {
	Role string `json:"role"`
	User *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
}

func (s *server) me(w http.ResponseWriter, r *http.Request, role string) {
	out := meResponse{Role: role}
	if p := principalOf(r); p.UserID != 0 {
		name, err := s.data.UserName(r.Context(), p.UserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
		out.User = &struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}{p.UserID, name}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) overview(w http.ResponseWriter, r *http.Request, _ string) {
	days, err := intParam(r, "days", defaultDays, 1, maxDays)
	if err != nil {
		badRequest(w, err)
		return
	}
	o, err := s.data.Overview(r.Context(), days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *server) health(w http.ResponseWriter, r *http.Request, _ string) {
	h, err := s.data.Health(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

type page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func paging(r *http.Request) (limit, offset int, err error) {
	if limit, err = intParam(r, "limit", defaultLimit, 1, maxLimit); err != nil {
		return
	}
	offset, err = intParam(r, "offset", 0, 0, 1<<31-1)
	return
}

func (s *server) listRepos(w http.ResponseWriter, r *http.Request, _ string) {
	limit, offset, err := paging(r)
	if err != nil {
		badRequest(w, err)
		return
	}
	q, err := textParam(r, "q")
	if err != nil {
		badRequest(w, err)
		return
	}
	f := store.RepoFilter{Q: q}
	switch v := r.URL.Query().Get("review_enabled"); v {
	case "":
	case "true", "false":
		b := v == "true"
		f.Enabled = &b
	default:
		badRequest(w, paramError("review_enabled must be true or false"))
		return
	}
	items, total, err := s.data.Repositories(r.Context(), f, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page[store.Repository]{items, total, limit, offset})
}

func (s *server) getRepo(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := idParam(r, "id")
	if !ok {
		badID(w)
		return
	}
	repo, err := s.data.Repository(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (s *server) patchRepo(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := idParam(r, "id")
	if !ok {
		badID(w)
		return
	}
	var body struct {
		ReviewEnabled *bool `json:"review_enabled"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.ReviewEnabled == nil {
		badRequest(w, paramError(`body must be {"review_enabled": true|false}`))
		return
	}
	repo, err := s.data.SetReviewEnabled(r.Context(), id, *body.ReviewEnabled)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("repository review toggled", "repo_id", id, "review_enabled", *body.ReviewEnabled)
	writeJSON(w, http.StatusOK, repo)
}

var commitStatuses = map[string]bool{"pending": true, "running": true, "done": true, "skipped": true, "failed": true}

type commitPage struct {
	Items      []store.CommitSummary `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

func (s *server) listCommits(w http.ResponseWriter, r *http.Request, _ string) {
	limit, err := intParam(r, "limit", defaultLimit, 1, maxLimit)
	if err != nil {
		badRequest(w, err)
		return
	}
	var f store.CommitFilter
	for _, p := range []struct {
		name string
		dst  *int64
	}{{"repo_id", &f.RepoID}, {"author_id", &f.AuthorID}} {
		if v := r.URL.Query().Get(p.name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 {
				badRequest(w, paramError(p.name+" must be a positive integer"))
				return
			}
			*p.dst = n
		}
	}
	f.Status = r.URL.Query().Get("status")
	if f.Status != "" && !commitStatuses[f.Status] {
		badRequest(w, paramError("status must be one of pending, running, done, skipped, failed"))
		return
	}
	if f.Branch, err = textParam(r, "branch"); err != nil {
		badRequest(w, err)
		return
	}
	if f.Q, err = textParam(r, "q"); err != nil {
		badRequest(w, err)
		return
	}
	if f.Since, err = timeParam(r, "since"); err != nil {
		badRequest(w, err)
		return
	}
	if f.Until, err = timeParam(r, "until"); err != nil {
		badRequest(w, err)
		return
	}
	items, next, err := s.data.Commits(r.Context(), f, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := commitPage{Items: items}
	if next != "" {
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getCommit(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := idParam(r, "id")
	if !ok {
		badID(w)
		return
	}
	c, err := s.data.Commit(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) rereview(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := idParam(r, "id")
	if !ok {
		badID(w)
		return
	}
	if err := s.data.Rereview(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("re-review requested", "commit_id", id)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

var prStates = map[string]bool{"OPEN": true, "MERGED": true, "DECLINED": true, "SUPERSEDED": true, "DELETED": true}

type pullRequestPage struct {
	Items      []store.PullRequestSummary `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
}

func (s *server) listPullRequests(w http.ResponseWriter, r *http.Request, _ string) {
	limit, err := intParam(r, "limit", defaultLimit, 1, maxLimit)
	if err != nil {
		badRequest(w, err)
		return
	}
	var f store.PullRequestFilter
	for _, p := range []struct {
		name string
		dst  *int64
	}{{"repo_id", &f.RepoID}, {"author_id", &f.AuthorID}} {
		if v := r.URL.Query().Get(p.name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 1 {
				badRequest(w, paramError(p.name+" must be a positive integer"))
				return
			}
			*p.dst = n
		}
	}
	if f.State = r.URL.Query().Get("state"); f.State != "" && !prStates[f.State] {
		badRequest(w, paramError("state must be one of OPEN, MERGED, DECLINED, SUPERSEDED, DELETED"))
		return
	}
	if f.Status = r.URL.Query().Get("review_status"); f.Status != "" && !commitStatuses[f.Status] {
		badRequest(w, paramError("review_status must be one of pending, running, done, skipped, failed"))
		return
	}
	if f.Q, err = textParam(r, "q"); err != nil {
		badRequest(w, err)
		return
	}
	items, next, err := s.data.PullRequests(r.Context(), f, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := pullRequestPage{Items: items}
	if next != "" {
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getPullRequest(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := idParam(r, "id")
	if !ok {
		badID(w)
		return
	}
	pr, err := s.data.PullRequest(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

func (s *server) rereviewPullRequest(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := idParam(r, "id")
	if !ok {
		badID(w)
		return
	}
	if err := s.data.RereviewPullRequest(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("pull request re-review requested", "pull_request_id", id)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request, role string) {
	limit, offset, err := paging(r)
	if err != nil {
		badRequest(w, err)
		return
	}
	days, err := intParam(r, "days", defaultDays, 1, maxDays)
	if err != nil {
		badRequest(w, err)
		return
	}
	q, err := textParam(r, "q")
	if err != nil {
		badRequest(w, err)
		return
	}
	sort := r.URL.Query().Get("sort")
	if sort != "" && !store.IsUserSort(sort) {
		badRequest(w, paramError("sort must be one of name, commits, score"))
		return
	}
	items, total, err := s.data.Users(r.Context(), q, sort, days, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if role != config.RoleAdmin {
		for i := range items {
			items[i].Email = nil
		}
	}
	writeJSON(w, http.StatusOK, page[store.User]{items, total, limit, offset})
}

func (s *server) getUser(w http.ResponseWriter, r *http.Request, role string) {
	id, ok := idParam(r, "id")
	if !ok {
		badID(w)
		return
	}
	days, err := intParam(r, "days", defaultDays, 1, maxDays)
	if err != nil {
		badRequest(w, err)
		return
	}
	u, err := s.data.User(r.Context(), id, days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if role != config.RoleAdmin {
		u.Email = nil
	}
	writeJSON(w, http.StatusOK, u)
}

// noteBody reads the optional {"note": "..."} of the workflow actions. An empty body is fine.
func noteBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Note string `json:"note"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		badRequest(w, paramError(`body must be {"note": "..."} or empty`))
		return "", false
	}
	return body.Note, true
}

// workflowAction runs one of the fix-workflow actions for the calling user.
func (s *server) workflowAction(name string, run func(ctx context.Context, a store.Actor, id int64, note string) error) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, _ string) {
		id, ok := idParam(r, "id")
		if !ok {
			badID(w)
			return
		}
		note, ok := noteBody(w, r)
		if !ok {
			return
		}
		p := principalOf(r)
		if err := run(r.Context(), p.actor(), id, note); err != nil {
			s.fail(w, r, err)
			return
		}
		s.log.Info("review workflow", "action", name, "id", id, "user_id", p.UserID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func (s *server) findingFixed(w http.ResponseWriter, r *http.Request, role string) {
	s.workflowAction("fixed", s.data.MarkFixed)(w, r, role)
}
func (s *server) findingReopen(w http.ResponseWriter, r *http.Request, role string) {
	s.workflowAction("reopen", s.data.Reopen)(w, r, role)
}
func (s *server) findingDismiss(w http.ResponseWriter, r *http.Request, role string) {
	s.workflowAction("dismiss", s.data.Dismiss)(w, r, role)
}
func (s *server) reviewClose(w http.ResponseWriter, r *http.Request, role string) {
	s.workflowAction("close", s.data.CloseReview)(w, r, role)
}
