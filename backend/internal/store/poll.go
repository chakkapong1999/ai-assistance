package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PollCursors returns, per branch, the head commit the poller last synced for
// the repository with this Bitbucket UUID. A repository that is not stored yet
// has no cursors.
func (s *Syncer) PollCursors(ctx context.Context, repoUUID string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.branch, c.last_hash
		FROM poll_cursors c JOIN repositories r ON r.id = c.repo_id
		WHERE r.bb_uuid = $1`, repoUUID)
	if err != nil {
		return nil, fmt.Errorf("poll cursors: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var b, h string
		if err := rows.Scan(&b, &h); err != nil {
			return nil, err
		}
		out[b] = h
	}
	return out, rows.Err()
}

// SavePollCursorsTx records the branch heads just synced and forgets branches
// that no longer exist. It runs in the same transaction as the commit sync, so
// a cursor never moves past commits that were not stored.
func SavePollCursorsTx(ctx context.Context, tx pgx.Tx, repoID int64, heads map[string]string) error {
	names := make([]string, 0, len(heads))
	for b, h := range heads {
		if _, err := tx.Exec(ctx, `
			INSERT INTO poll_cursors (repo_id, branch, last_hash) VALUES ($1, $2, $3)
			ON CONFLICT (repo_id, branch) DO UPDATE SET last_hash = EXCLUDED.last_hash, updated_at = now()`,
			repoID, b, h); err != nil {
			return fmt.Errorf("save cursor %s: %w", b, err)
		}
		names = append(names, b)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM poll_cursors WHERE repo_id = $1 AND NOT (branch = ANY($2))`, repoID, names); err != nil {
		return fmt.Errorf("prune cursors: %w", err)
	}
	return nil
}
