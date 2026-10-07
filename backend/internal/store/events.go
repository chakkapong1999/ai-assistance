package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/httpapi"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
)

// Events implements httpapi.EventStore on Postgres.
type Events struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

var _ httpapi.EventStore = (*Events)(nil)

func NewEvents(pool *pgxpool.Pool, rc *river.Client[pgx.Tx]) *Events {
	return &Events{pool: pool, river: rc}
}

// Ingest stores the delivery and, for events that need processing, enqueues
// the job in the same transaction: either both happen or neither does. A
// delivery cannot be stored without its job (it would never be processed) and
// a job cannot exist without its event.
//
// Bitbucket resends a delivery with the same request UUID; the unique index
// makes that a no-op and inserted=false is returned so the caller can answer
// 202 without enqueueing again.
func (s *Events) Ingest(ctx context.Context, ev httpapi.Event) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO webhook_events (request_uuid, event_key, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_uuid) DO NOTHING
		RETURNING id`,
		ev.RequestUUID, ev.EventKey, json.RawMessage(ev.Payload),
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // duplicate delivery
	}
	if err != nil {
		return false, fmt.Errorf("insert webhook_event: %w", err)
	}

	if ev.Enqueue {
		if _, err := s.river.InsertTx(ctx, tx, jobs.ProcessWebhookArgs{EventID: id}, nil); err != nil {
			return false, fmt.Errorf("enqueue process_webhook: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}
