// Package restapi is the read-mostly JSON API behind the dashboard (/api/v1).
//
// Authentication is a static bearer token with a role (see config.APIToken).
// That authenticates the *dashboard server*, not an end user; real user
// login is a separate, later piece of work.
package restapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/chakkapong1999/ai-assistance/backend/internal/config"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

//go:embed openapi.yaml
var openapiSpec []byte

// Data is what the handlers need from storage; *store.Dashboard implements it.
type Data interface {
	Overview(ctx context.Context, days int) (store.Overview, error)
	Repositories(ctx context.Context, f store.RepoFilter, limit, offset int) ([]store.Repository, int, error)
	Repository(ctx context.Context, id int64) (store.Repository, error)
	SetReviewEnabled(ctx context.Context, id int64, enabled bool) (store.Repository, error)
	Commits(ctx context.Context, f store.CommitFilter, cursor string, limit int) ([]store.CommitSummary, string, error)
	Commit(ctx context.Context, id int64) (store.CommitDetail, error)
	Rereview(ctx context.Context, id int64) error
	Users(ctx context.Context, q, sort string, days, limit, offset int) ([]store.User, int, error)
	User(ctx context.Context, id int64, days int) (store.UserDetail, error)
}

var _ Data = (*store.Dashboard)(nil)

// Access level a route requires.
type Access string

const (
	Public Access = "none"
	Viewer Access = config.RoleViewer
	Admin  Access = config.RoleAdmin
)

type Route struct {
	Method, Path string
	Access       Access
}

type server struct {
	data   Data
	tokens []tokenHash
	log    *slog.Logger
}

type tokenHash struct {
	sum  [32]byte
	role string
}

type route struct {
	Route
	h func(*server, http.ResponseWriter, *http.Request, string)
}

// table is the single source of truth for what is served; the OpenAPI drift
// test compares it with openapi.yaml.
var table = []route{
	{Route{"GET", "/api/v1/openapi.yaml", Public}, (*server).openapi},
	{Route{"GET", "/api/v1/me", Viewer}, (*server).me},
	{Route{"GET", "/api/v1/overview", Viewer}, (*server).overview},
	{Route{"GET", "/api/v1/repositories", Viewer}, (*server).listRepos},
	{Route{"GET", "/api/v1/repositories/{id}", Viewer}, (*server).getRepo},
	{Route{"PATCH", "/api/v1/repositories/{id}", Admin}, (*server).patchRepo},
	{Route{"GET", "/api/v1/commits", Viewer}, (*server).listCommits},
	{Route{"GET", "/api/v1/commits/{id}", Viewer}, (*server).getCommit},
	{Route{"POST", "/api/v1/commits/{id}/rereview", Admin}, (*server).rereview},
	{Route{"GET", "/api/v1/users", Viewer}, (*server).listUsers},
	{Route{"GET", "/api/v1/users/{id}", Viewer}, (*server).getUser},
}

// Routes lists the served routes (for tests and docs).
func Routes() []Route {
	out := make([]Route, len(table))
	for i, r := range table {
		out[i] = r.Route
	}
	return out
}

// New returns the handler for everything under /api/v1/. With no tokens it
// returns nil: an API with no credentials configured is not served at all.
func New(data Data, tokens []config.APIToken, log *slog.Logger) http.Handler {
	if len(tokens) == 0 {
		return nil
	}
	s := &server{data: data, log: log}
	for _, t := range tokens {
		s.tokens = append(s.tokens, tokenHash{sha256.Sum256([]byte(t.Token)), t.Role})
	}
	mux := http.NewServeMux()
	for _, r := range table {
		r := r
		mux.HandleFunc(r.Method+" "+r.Path, func(w http.ResponseWriter, req *http.Request) {
			role := ""
			if r.Access != Public {
				var ok bool
				if role, ok = s.authenticate(req); !ok {
					w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
					writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
					return
				}
				if r.Access == Admin && role != config.RoleAdmin {
					writeError(w, http.StatusForbidden, "forbidden", "this action needs the admin role")
					return
				}
			}
			r.h(s, w, req, role)
		})
	}
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, req *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return noStore(mux)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// authenticate compares against every configured token without stopping at
// the first match, so timing does not reveal which one matched.
func (s *server) authenticate(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tok) == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	role, found := "", 0
	for _, t := range s.tokens {
		if subtle.ConstantTimeCompare(sum[:], t.sum[:]) == 1 {
			role, found = t.role, 1
		}
	}
	return role, found == 1
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var e apiError
	e.Error.Code, e.Error.Message = code, msg
	writeJSON(w, status, e)
}

// fail maps a storage error to a response; anything unexpected is logged and
// answered with a generic 500 so internals do not leak.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrBadCursor):
		writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
	case errors.Is(err, store.ErrReviewRunning), errors.Is(err, store.ErrRepoDisabled), errors.Is(err, store.ErrMergeCommit):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "request cancelled")
	default:
		s.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
