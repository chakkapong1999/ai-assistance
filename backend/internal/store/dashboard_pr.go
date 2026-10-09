package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
)

// ------------------------------------------------------------ pull requests

// PullRequestSummary is one row of the pull request list. Score and findings
// come from the newest review of the whole pull request.
type PullRequestSummary struct {
	ID         int64      `json:"id"`
	Number     int        `json:"number"` // Bitbucket's pull request id
	Title      string     `json:"title"`
	Repository RepoRef    `json:"repository"`
	Author     AuthorRef  `json:"author"`
	SourceBr   *string    `json:"source_branch"`
	DestBr     *string    `json:"destination_branch"`
	State      string     `json:"state"`
	Status     string     `json:"review_status"`
	SkipReason *string    `json:"review_skip_reason"`
	Files      *int       `json:"files_changed"`
	Additions  *int       `json:"additions"`
	Deletions  *int       `json:"deletions"`
	Score      *int       `json:"score"`
	Findings   int        `json:"findings"`
	ReviewedAt *time.Time `json:"reviewed_at"`
	// Stale: the newest review was taken at an older head than the pull
	// request has now (a push is waiting to be reviewed).
	Stale     bool      `json:"review_outdated"`
	UpdatedAt time.Time `json:"updated_at"`
	// Fix workflow, of the latest review (see CommitSummary).
	OpenFindings int  `json:"open_findings"`
	Closed       bool `json:"review_closed"`
}

const prSortAt = `COALESCE(pr.bb_updated_on, pr.created_at)`

const prAuthorName = `COALESCE(u.display_name, '')`

const prCols = `
	pr.id, pr.bb_pr_id, pr.title, r.id, ws.slug || '/' || r.slug,
	u.id, ` + prAuthorName + `, u.avatar_url,
	pr.source_branch, pr.dest_branch, pr.state, pr.review_status, pr.review_skip_reason,
	pr.files_changed, pr.additions, pr.deletions,
	lr.score, COALESCE(fc.n, 0), lr.created_at,
	(lr.id IS NOT NULL AND lr.pr_head_hash IS DISTINCT FROM NULLIF(pr.source_hash, '')),
	` + prSortAt + `,
	COALESCE(fc.open, 0), lr.closed_at IS NOT NULL`

const prFrom = `
	FROM pull_requests pr
	JOIN repositories r ON r.id = pr.repo_id
	JOIN projects p ON p.id = r.project_id
	JOIN workspaces ws ON ws.id = p.workspace_id
	LEFT JOIN users u ON u.id = pr.author_user_id
	LEFT JOIN LATERAL (
		SELECT rv.id, rv.score, rv.created_at, rv.pr_head_hash, rv.closed_at
		FROM reviews rv WHERE rv.pr_id = pr.id
		ORDER BY rv.created_at DESC, rv.id DESC LIMIT 1
	) lr ON true
	LEFT JOIN LATERAL (SELECT count(*)::int AS n, (count(*) FILTER (WHERE f.status = 'open'))::int AS open FROM review_findings f WHERE f.review_id = lr.id) fc ON true`

func (p *PullRequestSummary) dests() []any {
	return []any{&p.ID, &p.Number, &p.Title, &p.Repository.ID, &p.Repository.FullName,
		&p.Author.ID, &p.Author.Name, &p.Author.AvatarURL,
		&p.SourceBr, &p.DestBr, &p.State, &p.Status, &p.SkipReason,
		&p.Files, &p.Additions, &p.Deletions,
		&p.Score, &p.Findings, &p.ReviewedAt, &p.Stale, &p.UpdatedAt, &p.OpenFindings, &p.Closed}
}

type PullRequestFilter struct {
	RepoID   int64
	AuthorID int64
	State    string // OPEN, MERGED, DECLINED, SUPERSEDED, DELETED
	Status   string // review status
	Q        string
	Fix      string // open | ready | closed
	Offset   int    // skip this many rows; not for use with a cursor
}

// pullRequestConds is the WHERE of a pull request list, shared by the page and its count.
func pullRequestConds(f PullRequestFilter, a *args) []string {
	var conds []string
	if f.RepoID != 0 {
		conds = append(conds, "pr.repo_id = "+a.add(f.RepoID))
	}
	if f.AuthorID != 0 {
		conds = append(conds, "pr.author_user_id = "+a.add(f.AuthorID))
	}
	if f.State != "" {
		conds = append(conds, "pr.state = "+a.add(f.State))
	}
	if f.Status != "" {
		conds = append(conds, "pr.review_status = "+a.add(f.Status))
	}
	if f.Q != "" {
		p := a.add(likePattern(f.Q))
		conds = append(conds, "(pr.title ILIKE "+p+" OR pr.source_branch ILIKE "+p+" OR pr.bb_pr_id::text ILIKE "+p+")")
	}
	if c := fixCond(f.Fix); c != "" {
		conds = append(conds, c)
	}
	return conds
}

// CountPullRequests is the number of pull requests a filter matches.
func (d *Dashboard) CountPullRequests(ctx context.Context, f PullRequestFilter) (int, error) {
	var a args
	var n int
	if err := d.pool.QueryRow(ctx, "SELECT count(*)::int"+prFrom+where(pullRequestConds(f, &a)), a...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pull requests: %w", err)
	}
	return n, nil
}

// PullRequests lists most recently updated first, paged by cursor or by f.Offset.
func (d *Dashboard) PullRequests(ctx context.Context, f PullRequestFilter, cursor string, limit int) (items []PullRequestSummary, next string, err error) {
	var a args
	conds := pullRequestConds(f, &a)
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		conds = append(conds, "("+prSortAt+", pr.id) < ("+a.add(t)+", "+a.add(id)+")")
	}

	q := "SELECT " + prCols + prFrom + where(conds) +
		" ORDER BY " + prSortAt + " DESC, pr.id DESC LIMIT " + a.add(limit+1) + " OFFSET " + a.add(f.Offset)
	rows, err := d.pool.Query(ctx, q, a...)
	if err != nil {
		return nil, "", fmt.Errorf("list pull requests: %w", err)
	}
	defer rows.Close()
	items = []PullRequestSummary{}
	for rows.Next() {
		var p PullRequestSummary
		if err := rows.Scan(p.dests()...); err != nil {
			return nil, "", err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeCursor(last.UpdatedAt, last.ID)
	}
	return items, next, nil
}

type PullRequestDetail struct {
	PullRequestSummary
	Description  string  `json:"description"`
	ReviewsCount int     `json:"reviews_count"`
	Review       *Review `json:"review"`

	FailedAttempts FailedAttempts `json:"failed_attempts"`
}

func (d *Dashboard) PullRequest(ctx context.Context, id int64) (PullRequestDetail, error) {
	var pd PullRequestDetail
	err := d.pool.QueryRow(ctx, "SELECT "+prCols+", pr.description, (SELECT count(*) FROM reviews rv WHERE rv.pr_id = pr.id)"+
		prFrom+" WHERE pr.id = $1", id).
		Scan(append(pd.PullRequestSummary.dests(), &pd.Description, &pd.ReviewsCount)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return pd, ErrNotFound
	}
	if err != nil {
		return pd, fmt.Errorf("pull request: %w", err)
	}
	if pd.FailedAttempts, err = d.failedAttemptsOf(ctx, "pr_id", id); err != nil {
		return pd, err
	}
	if pd.ReviewsCount == 0 {
		return pd, nil
	}
	if pd.Review, err = d.latestReviewOf(ctx, "pr_id", id); err != nil {
		return pd, err
	}
	return pd, nil
}

// RereviewPullRequest puts a pull request back in the queue, atomically with
// its status, like Rereview does for a commit.
func (d *Dashboard) RereviewPullRequest(ctx context.Context, id int64) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status, state string
	var enabled bool
	err = tx.QueryRow(ctx, `
		SELECT pr.review_status, pr.state, r.review_enabled
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repo_id
		WHERE pr.id = $1 FOR UPDATE OF pr`, id).Scan(&status, &state, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	switch {
	case status == "running":
		return ErrReviewRunning
	case !enabled:
		return ErrRepoDisabled
	case state != "OPEN":
		return ErrPRNotOpen
	}
	if _, err := tx.Exec(ctx, `UPDATE pull_requests SET review_status = 'pending', review_skip_reason = NULL, updated_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	opts := jobs.ReviewPullRequestArgs{}.InsertOpts() // unique over waiting/running jobs only
	if _, err := d.river.InsertTx(ctx, tx, jobs.ReviewPullRequestArgs{PullRequestID: id}, &opts); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return tx.Commit(ctx)
}
