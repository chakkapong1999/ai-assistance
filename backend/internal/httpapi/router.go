// Package httpapi holds the HTTP router and handlers of the API process.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Deps are the collaborators the router needs. Events may be nil, in which
// case the webhook route is not registered.
type Deps struct {
	WebhookSecret []byte
	Events        EventStore
	// Dashboard serves everything under /api/v1/; nil leaves those routes
	// unregistered.
	Dashboard http.Handler
	// MaxWebhookBody caps the request body in bytes; zero means the default.
	MaxWebhookBody int64
}

// NewRouter returns the API handler. Later milestones add the dashboard routes.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	if d.Events != nil {
		mux.Handle("POST /webhooks/bitbucket", newWebhookHandler(d))
	}
	if d.Dashboard != nil {
		mux.Handle("/api/v1/", d.Dashboard)
	}
	return mux
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
