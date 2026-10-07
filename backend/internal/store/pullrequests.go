package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

// Skip reasons stored on a pull request.
const (
	PRSkipRepoDisabled = "review is not enabled for this repository"
	PRSkipNotOpen      = "pull request is not open"
)

// PRRepo is what the pull request step needs to know about a repository.
type PRRepo struct {
	ID            int64
	ReviewEnabled bool
}

// PRRepo looks a repository up by its Bitbucket UUID; ok is false when it has
// not been synced yet (an empty repository, for example).
func (s *Syncer) PRRepo(ctx context.Context, repoUUID string) (r PRRepo, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT id, review_enabled FROM repositories WHERE bb_uuid = $1`, repoUUID).
		Scan(&r.ID, &r.ReviewEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// TrackedOpenPRs returns the Bitbucket ids of the pull requests stored as open.
func (s *Syncer) TrackedOpenPRs(ctx context.Context, repoID int64) (map[int]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT bb_pr_id FROM pull_requests WHERE repo_id = $1 AND state = 'OPEN'`, repoID)
	if err != nil {
		return nil, fmt.Errorf("tracked pull requests: %w", err)
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// PendingPRs returns open pull requests waiting for a review, so the poller can
// make sure each has a job even if one was lost (a crash between steps, say).
func (s *Syncer) PendingPRs(ctx context.Context, repoID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM pull_requests
		WHERE repo_id = $1 AND state = 'OPEN' AND review_status = 'pending'
		ORDER BY id`, repoID)
	if err != nil {
		return nil, fmt.Errorf("pending pull requests: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PRSyncResult lists the pull requests that now need a review job.
type PRSyncResult struct {
	NeedReview []int64 // pull_requests.id
	Changed    int     // rows inserted or updated, reviewed or not
}

// SyncPullRequestsTx stores the pull requests of one repository inside the
// caller's transaction (so review jobs can be queued atomically with them).
//
// A pull request already stored and unchanged costs one SELECT and no writes,
// which keeps an idle polling round write-free. A new head on an open pull
// request puts it back to "pending" so it is reviewed again. A pull request
// that is not stored yet and was last updated before newerThan is ignored
// (zero = keep all): the first round must not review every stale open PR.
func (s *Syncer) SyncPullRequestsTx(ctx context.Context, tx pgx.Tx, repo PRRepo, prs []webhook.PullRequest, newerThan time.Time) (PRSyncResult, error) {
	var res PRSyncResult
	for _, pr := range prs {
		if pr.ID < 1 {
			continue
		}
		var (
			id                                  int64
			title, desc, state, srcB, dstB, src string
		)
		err := tx.QueryRow(ctx, `
			SELECT id, title, description, state, COALESCE(source_branch, ''), COALESCE(dest_branch, ''), COALESCE(source_hash, '')
			FROM pull_requests WHERE repo_id = $1 AND bb_pr_id = $2`, repo.ID, pr.ID).
			Scan(&id, &title, &desc, &state, &srcB, &dstB, &src)
		known := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return res, fmt.Errorf("load pull request %d: %w", pr.ID, err)
		}

		titleC, descC := cleanText(pr.Title, 1000), cleanText(pr.Description, maxMessageBytes)
		if known && title == titleC && desc == descC && state == pr.State &&
			srcB == pr.Source.Branch.Name && dstB == pr.Destination.Branch.Name && src == pr.Source.Commit.Hash {
			continue
		}
		if !known && !newerThan.IsZero() && !pr.UpdatedOn.IsZero() && pr.UpdatedOn.Before(newerThan) {
			continue
		}

		var authorID *int64
		if a := pr.Author; a != nil && (a.UUID != "" || a.AccountID != "") {
			c := webhook.Commit{}
			c.Author.User = a
			if authorID, err = resolveAuthor(ctx, tx, c); err != nil {
				return res, fmt.Errorf("resolve author of pull request %d: %w", pr.ID, err)
			}
		}

		// What the review should do next.
		headMoved := !known || src != pr.Source.Commit.Hash
		reopened := known && state != "OPEN" && pr.State == "OPEN"
		status, reason := "", ""
		needReview := false
		switch {
		case pr.State != "OPEN":
			// A closed pull request keeps whatever review it already has.
			if !known {
				status, reason = "skipped", PRSkipNotOpen
			}
		case !repo.ReviewEnabled:
			status, reason = "skipped", PRSkipRepoDisabled
		case headMoved || reopened:
			status, needReview = "pending", true
		}

		if !known {
			if status == "" {
				status = "pending"
			}
			err = tx.QueryRow(ctx, `
				INSERT INTO pull_requests (repo_id, bb_pr_id, title, description, author_user_id, source_branch, dest_branch,
					state, source_hash, bb_created_on, bb_updated_on, review_status, review_skip_reason)
				VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, NULLIF($9, ''), $10, $11, $12, NULLIF($13, ''))
				RETURNING id`,
				repo.ID, pr.ID, titleC, descC, authorID, pr.Source.Branch.Name, pr.Destination.Branch.Name,
				pr.State, pr.Source.Commit.Hash, nullTime(pr.CreatedOn), nullTime(pr.UpdatedOn), status, reason).Scan(&id)
		} else {
			// COALESCE keeps the old author when Bitbucket sent none.
			_, err = tx.Exec(ctx, `
				UPDATE pull_requests SET
					title = $2, description = $3, state = $4, source_branch = NULLIF($5, ''), dest_branch = NULLIF($6, ''),
					source_hash = NULLIF($7, ''), bb_updated_on = COALESCE($8, bb_updated_on),
					author_user_id = COALESCE($9, author_user_id),
					review_status = CASE WHEN $10 = '' THEN review_status ELSE $10 END,
					review_skip_reason = CASE WHEN $10 = '' THEN review_skip_reason ELSE NULLIF($11, '') END,
					updated_at = now()
				WHERE id = $1`,
				id, titleC, descC, pr.State, pr.Source.Branch.Name, pr.Destination.Branch.Name, pr.Source.Commit.Hash,
				nullTime(pr.UpdatedOn), authorID, status, reason)
		}
		if err != nil {
			return res, fmt.Errorf("store pull request %d: %w", pr.ID, err)
		}
		res.Changed++
		if needReview {
			res.NeedReview = append(res.NeedReview, id)
		}
	}
	return res, nil
}

// MarkPullRequestGoneTx records that Bitbucket no longer knows a pull request.
func MarkPullRequestGoneTx(ctx context.Context, tx pgx.Tx, repoID int64, bbID int) error {
	_, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'DELETED', updated_at = now() WHERE repo_id = $1 AND bb_pr_id = $2`, repoID, bbID)
	return err
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
