// Package httpapi holds the HTTP router and handlers of the API process.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// NewRouter returns the API handler. Later milestones add the webhook and
// dashboard routes here.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	return mux
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
