// Package mockbitbucket is a stand-in for the few Bitbucket Cloud endpoints
// the worker calls, for trying the pipeline without a Bitbucket account or
// real commits. Every commit gets the same diff.
package mockbitbucket

import (
	"fmt"
	"net/http"
	"strings"
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

// Handler serves the endpoints used by internal/bitbucket.
//
//	GET /repositories/{workspace}/{repo}/diff/{hash}     -> diff
//	GET /repositories/{workspace}/{repo}/commits         -> {"values": []}
func Handler(diff string) http.Handler {
	if strings.TrimSpace(diff) == "" {
		diff = SampleDiff
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/diff/{hash}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, diff)
	})
	mux.HandleFunc("GET /repositories/{workspace}/{repo}/commits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"values":[]}`)
	})
	return mux
}
