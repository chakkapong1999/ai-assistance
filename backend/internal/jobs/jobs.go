// Package jobs defines the River job payloads shared by the API (which
// enqueues) and the worker (which runs them).
package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
		// A commit is never queued twice while a job for it is waiting or
		// running. A finished job must not count: its row would keep the
		// unique key and River would refuse every later job for the commit
		// (reconcile, "Review again") without an error.
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: WaitingOrRunning},
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

// ReviewPullRequestArgs reviews one stored pull request: the whole diff of its
// source branch against the destination, taken when the job runs.
type ReviewPullRequestArgs struct {
	PullRequestID int64 `json:"pull_request_id"`
}

func (ReviewPullRequestArgs) Kind() string { return "review_pull_request" }

func (ReviewPullRequestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueReview,
		MaxAttempts: maxAttempts,
		// One job per pull request while one is waiting or running. A new push
		// that arrives during a run is picked up by that job re-queueing itself
		// (see the worker), not by a second insert.
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: WaitingOrRunning},
	}
}

// WaitingOrRunning are the job states in which a job still counts as "the
// one for this subject"; a finished job does not block a new one.
var WaitingOrRunning = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
	rivertype.JobStateRetryable, rivertype.JobStateScheduled,
}

// SyncDirectoryArgs refreshes who is who: workspace roles, repository
// permissions and the profiles (name, avatar) of everyone seen. It is inserted
// on a timer by the worker, once a day by default.
type SyncDirectoryArgs struct{}

func (SyncDirectoryArgs) Kind() string { return "sync_directory" }

func (SyncDirectoryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueDefault,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: WaitingOrRunning},
	}
}

// ReconcileArgs runs one reconciliation pass: it finds work that lost its job
// (an event nobody processed, a commit or pull request marked pending or
// running with no job behind it) and queues it again. It is inserted on a
// timer by the worker; a pass that is still running is never doubled up.
type ReconcileArgs struct{}

func (ReconcileArgs) Kind() string { return "reconcile" }

func (ReconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       QueueDefault,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: WaitingOrRunning},
	}
}

// LiveStates are WaitingOrRunning as strings, for SQL against river_job.
func LiveStates() []string {
	out := make([]string, len(WaitingOrRunning))
	for i, s := range WaitingOrRunning {
		out[i] = string(s)
	}
	return out
}

// Reconciliation thresholds, shared by the worker (which acts) and the health
// report (which counts what the next pass would act on).
const (
	// A thing younger than this is left alone: its job may simply not have
	// started, or it may have just finished.
	ReconcileGrace = 10 * time.Minute
	// A job that ended less than this long ago may still be settling the row
	// it belongs to (the status is written just before the job is finalised).
	ReconcileSettle = 5 * time.Minute
	// How many jobs one event, commit or pull request may have had before the
	// reconciler stops queueing another: a subject that keeps losing its job is
	// not going to be fixed by one more try.
	ReconcileMaxJobs = 4
	ReconcileBatch   = 100
)

// NoLiveJobSQL is a SQL fragment for a WHERE clause: no job of `kind` for the
// subject whose id is `subjectID` (a column expression) is waiting or running,
// and none ended in the last ReconcileSettle. With respectCancel, a cancelled
// job also counts (for a webhook event that means its payload can never be
// parsed, so queueing it again is pointless). It uses $1 (the LiveStates) and
// $2 (ReconcileSettle in seconds, as an int).
func NoLiveJobSQL(kind, key, subjectID string, respectCancel bool) string {
	cancelled := ""
	if respectCancel {
		cancelled = " OR j.state = 'cancelled'"
	}
	return fmt.Sprintf(`
		AND NOT EXISTS (SELECT 1 FROM river_job j WHERE j.kind = '%[1]s' AND (j.args->>'%[2]s')::bigint = %[3]s
		                AND (j.state::text = ANY($1)%[4]s
		                     OR j.finalized_at > now() - make_interval(secs => $2::int)))`, kind, key, subjectID, cancelled)
}

// JobCountSQL is a SQL expression: how many jobs of `kind` the subject has had.
func JobCountSQL(kind, key, subjectID string) string {
	return fmt.Sprintf(`(SELECT count(*) FROM river_job j WHERE j.kind = '%s' AND (j.args->>'%s')::bigint = %s)`, kind, key, subjectID)
}

// ReleaseFinished frees the unique key held by finished jobs of kind for the
// subject whose id is in args[key]. Jobs created before ReviewCommitArgs stopped
// counting finished jobs still hold it, and would make a new insert for the
// same subject a silent no-op. Call it in the transaction that inserts.
func ReleaseFinished(ctx context.Context, tx pgx.Tx, kind, key string, id int64) error {
	_, err := tx.Exec(ctx, `
		UPDATE river_job SET unique_key = NULL, unique_states = NULL
		WHERE kind = $1 AND (args->>$2)::bigint = $3 AND unique_key IS NOT NULL AND state::text <> ALL($4)`,
		kind, key, id, LiveStates())
	return err
}
