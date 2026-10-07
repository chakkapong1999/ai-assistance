package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

var secret = []byte("test-secret")

type fakeStore struct {
	calls    []Event
	inserted bool
	err      error
}

func (f *fakeStore) Ingest(_ context.Context, e Event) (bool, error) {
	f.calls = append(f.calls, e)
	return f.inserted, f.err
}

func sign(body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

type req struct {
	body      string
	sig       *string // nil = compute a valid one
	eventKey  string
	requestID string
	method    string
}

func do(t *testing.T, store EventStore, max int64, r req) *httptest.ResponseRecorder {
	t.Helper()
	if r.method == "" {
		r.method = http.MethodPost
	}
	hr := httptest.NewRequest(r.method, "/webhooks/bitbucket", strings.NewReader(r.body))
	if r.sig == nil {
		hr.Header.Set("X-Hub-Signature", sign([]byte(r.body)))
	} else if *r.sig != "" {
		hr.Header.Set("X-Hub-Signature", *r.sig)
	}
	if r.eventKey != "" {
		hr.Header.Set("X-Event-Key", r.eventKey)
	}
	if r.requestID != "" {
		hr.Header.Set("X-Request-UUID", r.requestID)
	}
	rec := httptest.NewRecorder()
	NewRouter(Deps{WebhookSecret: secret, Events: store, MaxWebhookBody: max}).ServeHTTP(rec, hr)
	return rec
}

func str(s string) *string { return &s }

func TestWebhookAcceptsValidPush(t *testing.T) {
	payload, err := os.ReadFile("../../testdata/repo_push.json")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{inserted: true}

	rec := do(t, store, 0, req{body: string(payload), eventKey: "repo:push", requestID: "uuid-1"})

	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"accepted"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(store.calls) != 1 {
		t.Fatalf("store called %d times, want 1", len(store.calls))
	}
	got := store.calls[0]
	if got.RequestUUID != "uuid-1" || got.EventKey != "repo:push" || !got.Enqueue {
		t.Errorf("event = %+v", got)
	}
	if string(got.Payload) != string(payload) {
		t.Error("payload must be stored byte for byte")
	}
}

func TestWebhookDuplicateIsAcceptedNotReprocessed(t *testing.T) {
	store := &fakeStore{inserted: false}
	rec := do(t, store, 0, req{body: `{"a":1}`, eventKey: "repo:push", requestID: "uuid-1"})
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"duplicate"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookOnlyPushStartsWork(t *testing.T) {
	for key, want := range map[string]bool{
		"repo:push":           true,
		"diagnostics:ping":    false,
		"pullrequest:created": false,
	} {
		store := &fakeStore{inserted: true}
		rec := do(t, store, 0, req{body: `{}`, eventKey: key, requestID: "u"})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d", key, rec.Code)
		}
		if store.calls[0].Enqueue != want {
			t.Errorf("%s: Enqueue = %v, want %v", key, store.calls[0].Enqueue, want)
		}
	}
}

func TestWebhookRejections(t *testing.T) {
	cases := []struct {
		name string
		r    req
		max  int64
		want int
	}{
		{"wrong signature", req{body: `{}`, sig: str("sha256=00"), eventKey: "repo:push", requestID: "u"}, 0, 401},
		{"no signature", req{body: `{}`, sig: str(""), eventKey: "repo:push", requestID: "u"}, 0, 401},
		{"missing event key", req{body: `{}`, requestID: "u"}, 0, 400},
		{"missing request uuid", req{body: `{}`, eventKey: "repo:push"}, 0, 400},
		{"invalid json", req{body: `{nope`, eventKey: "repo:push", requestID: "u"}, 0, 400},
		{"body too large", req{body: strings.Repeat("x", 100), eventKey: "repo:push", requestID: "u"}, 50, 413},
		{"get not allowed", req{method: http.MethodGet}, 0, 405},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{inserted: true}
			rec := do(t, store, tc.max, tc.r)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if len(store.calls) != 0 {
				t.Fatal("a rejected delivery must never reach the store")
			}
		})
	}
}

func TestWebhookSignatureCheckedBeforeHeaders(t *testing.T) {
	// A forged request must get 401 even when headers are also missing, so the
	// endpoint reveals nothing about what it expects to unauthenticated callers.
	rec := do(t, &fakeStore{}, 0, req{body: `{}`, sig: str("sha256=00")})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestWebhookStoreErrorIs500SoBitbucketRetries(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	rec := do(t, store, 0, req{body: `{}`, eventKey: "repo:push", requestID: "u"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db down") {
		t.Error("internal error text must not leak to the caller")
	}
}
