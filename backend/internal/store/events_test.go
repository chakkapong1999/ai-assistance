package store_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/httpapi"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/testdb"
)

const secret = "test-secret"

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { testdb.Main(m, func(p *pgxpool.Pool) { testPool = p }) }

func newServer(t *testing.T) (*httptest.Server, *store.Events) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `TRUNCATE webhook_events, river_job RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	rc, err := river.NewClient(riverpgxv5.New(testPool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ev := store.NewEvents(testPool, rc)
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{WebhookSecret: []byte(secret), Events: ev}))
	t.Cleanup(srv.Close)
	return srv, ev
}

func sign(body []byte) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}

func post(t *testing.T, srv *httptest.Server, body []byte, uuid, event, sig string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/webhooks/bitbucket", bytes.NewReader(body))
	req.Header.Set("X-Event-Key", event)
	req.Header.Set("X-Request-UUID", uuid)
	if sig != "" {
		req.Header.Set("X-Hub-Signature", sig)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPushStoresOneRowAndOneJob(t *testing.T) {
	srv, _ := newServer(t)
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "repo_push.json"))
	if err != nil {
		t.Fatal(err)
	}

	resp := post(t, srv, body, "uuid-1", "repo:push", sign(body))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if n := count(t, `SELECT count(*) FROM webhook_events`); n != 1 {
		t.Fatalf("webhook_events = %d, want 1", n)
	}
	if n := count(t, `SELECT count(*) FROM river_job WHERE kind = 'process_webhook'`); n != 1 {
		t.Fatalf("jobs = %d, want 1", n)
	}

	// The job carries the ID of the row, and the payload round-trips as jsonb.
	var evID int64
	var jobEvID int64
	var event string
	var repoName string
	if err := testPool.QueryRow(context.Background(),
		`SELECT id, event_key, payload #>> '{repository,full_name}' FROM webhook_events`).Scan(&evID, &event, &repoName); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(),
		`SELECT (args->>'event_id')::bigint FROM river_job`).Scan(&jobEvID); err != nil {
		t.Fatal(err)
	}
	if jobEvID != evID || event != "repo:push" || repoName == "" {
		t.Fatalf("job event_id=%d row id=%d event=%q repo=%q", jobEvID, evID, event, repoName)
	}
}

func TestResendDoesNotDuplicate(t *testing.T) {
	srv, _ := newServer(t)
	body := []byte(`{"repository":{"full_name":"ws/r"},"push":{"changes":[]}}`)

	first := post(t, srv, body, "uuid-dup", "repo:push", sign(body))
	second := post(t, srv, body, "uuid-dup", "repo:push", sign(body))
	if first.StatusCode != 202 || second.StatusCode != 202 {
		t.Fatalf("statuses = %d, %d, want 202, 202", first.StatusCode, second.StatusCode)
	}
	if n := count(t, `SELECT count(*) FROM webhook_events`); n != 1 {
		t.Fatalf("webhook_events = %d after resend, want 1", n)
	}
	if n := count(t, `SELECT count(*) FROM river_job`); n != 1 {
		t.Fatalf("jobs = %d after resend, want 1", n)
	}
	// A different UUID is a different delivery.
	post(t, srv, body, "uuid-other", "repo:push", sign(body))
	if n := count(t, `SELECT count(*) FROM webhook_events`); n != 2 {
		t.Fatalf("webhook_events = %d, want 2", n)
	}
}

func TestBadOrMissingSignatureStoresNothing(t *testing.T) {
	srv, _ := newServer(t)
	body := []byte(`{"a":1}`)
	for name, sig := range map[string]string{"wrong": "sha256=" + strings.Repeat("0", 64), "missing": ""} {
		if resp := post(t, srv, body, "uuid-bad-"+name, "repo:push", sig); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s signature: status = %d, want 401", name, resp.StatusCode)
		}
	}
	if n := count(t, `SELECT count(*) FROM webhook_events`) + count(t, `SELECT count(*) FROM river_job`); n != 0 {
		t.Fatalf("rows stored for unauthenticated requests: %d", n)
	}
}

func TestPingIsStoredButNotEnqueued(t *testing.T) {
	srv, _ := newServer(t)
	body := []byte(`{}`)
	if resp := post(t, srv, body, "uuid-ping", "diagnostics:ping", sign(body)); resp.StatusCode != 202 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if n := count(t, `SELECT count(*) FROM webhook_events WHERE event_key = 'diagnostics:ping'`); n != 1 {
		t.Fatalf("ping not stored: %d", n)
	}
	if n := count(t, `SELECT count(*) FROM river_job`); n != 0 {
		t.Fatalf("ping must not enqueue a job, got %d", n)
	}
}

// If enqueueing fails the event row must roll back too; otherwise Bitbucket's
// retry would hit the duplicate path and the event would never be processed.
func TestEnqueueFailureRollsBackTheEvent(t *testing.T) {
	_, ev := newServer(t)

	// Break the job table so InsertTx fails after the event insert succeeded.
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `ALTER TABLE river_job RENAME TO river_job_off`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `ALTER TABLE IF EXISTS river_job_off RENAME TO river_job`) })

	inserted, err := ev.Ingest(ctx, httpapi.Event{RequestUUID: "uuid-rb", EventKey: "repo:push", Payload: []byte(`{}`), Enqueue: true})
	if err == nil || inserted {
		t.Fatalf("Ingest = %v, %v; want an error", inserted, err)
	}
	if n := count(t, `SELECT count(*) FROM webhook_events`); n != 0 {
		t.Fatalf("event row survived a failed enqueue: %d", n)
	}
	// The retry is not mistaken for a duplicate once the table is back.
	if _, err := testPool.Exec(ctx, `ALTER TABLE river_job_off RENAME TO river_job`); err != nil {
		t.Fatal(err)
	}
	if inserted, err := ev.Ingest(ctx, httpapi.Event{RequestUUID: "uuid-rb", EventKey: "repo:push", Payload: []byte(`{}`), Enqueue: true}); err != nil || !inserted {
		t.Fatalf("retry Ingest = %v, %v", inserted, err)
	}
}

func TestConcurrentDuplicatesInsertExactlyOnce(t *testing.T) {
	_, ev := newServer(t)
	const n = 20
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() {
			ins, err := ev.Ingest(context.Background(), httpapi.Event{RequestUUID: "uuid-race", EventKey: "repo:push", Payload: []byte(`{}`), Enqueue: true})
			if err != nil {
				t.Error(err)
			}
			results <- ins
		}()
	}
	won := 0
	for i := 0; i < n; i++ {
		if <-results {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent deliveries reported inserted, want exactly 1", won)
	}
	if c := count(t, `SELECT count(*) FROM river_job`); c != 1 {
		t.Fatalf("jobs = %d, want 1", c)
	}
}

func TestCheckSchema(t *testing.T) {
	if err := store.CheckSchema(context.Background(), testPool); err != nil {
		t.Fatalf("schema should be complete: %v", err)
	}
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `ALTER TABLE river_job RENAME TO river_job_off`); err != nil {
		t.Fatal(err)
	}
	defer testPool.Exec(ctx, `ALTER TABLE river_job_off RENAME TO river_job`)
	err := store.CheckSchema(ctx, testPool)
	if err == nil || !strings.Contains(err.Error(), "make migrate-river") {
		t.Fatalf("err = %v, want a hint to run make migrate-river", err)
	}
}

var _ pgx.Tx // keep the import used if helpers change
