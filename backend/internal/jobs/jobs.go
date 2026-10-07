// Package jobs defines the River job payloads shared by the API (which
// enqueues) and the worker (which runs them).
package jobs

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Queues. Webhook processing is cheap and may run in parallel; reviews are
// serialised (MaxWorkers 1 in the worker) because they use a local LLM CLI.
const (
	QueueDefault = river.QueueDefault
	QueueReview  = "review"
)

const maxAttempts = 5

// ProcessWebhookArgs is enqueued, in the same transaction that stores the
// webhook_events row, for every repo:push delivery. The worker loads the
// event by ID, syncs workspace/project/repo/user/commit records and enqueues
// one review job per new commit.
type ProcessWebhookArgs struct {
	EventID int64 `json:"event_id"`
}

func (ProcessWebhookArgs) Kind() string { return "process_webhook" }

func (ProcessWebhookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: maxAttempts}
}

// ReviewCommitArgs reviews one stored commit.
type ReviewCommitArgs struct {
	CommitID int64 `json:"commit_id"`
}

func (ReviewCommitArgs) Kind() string { return "review_commit" }

func (ReviewCommitArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueReview,
		MaxAttempts: maxAttempts,
		// A commit is never queued twice while a job for it is still pending.
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
}

// PollReposArgs runs one polling round over the configured repositories. It
// is inserted on a timer by the worker; the uniqueness below (counting only
// jobs that are still waiting or running, not finished ones) means a round
// that is still running is never doubled up.
type PollReposArgs struct{}

func (PollReposArgs) Kind() string { return "poll_repos" }

func (PollReposArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueDefault,
		MaxAttempts: 3,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
}
