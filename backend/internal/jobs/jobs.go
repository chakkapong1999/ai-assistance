// Package jobs defines the River job payloads shared by the API (which
// enqueues) and the worker (which runs them).
package jobs

// ProcessWebhookArgs is enqueued, in the same transaction that stores the
// webhook_events row, for every repo:push delivery. The worker (M3) loads the
// event by ID, syncs workspace/project/repo/user/commit records and fans out
// the per-commit review jobs.
type ProcessWebhookArgs struct {
	EventID int64 `json:"event_id"`
}

func (ProcessWebhookArgs) Kind() string { return "process_webhook" }
