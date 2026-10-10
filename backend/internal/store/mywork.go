package store

import (
	"context"
	"time"
)

// What one person has to do today: fix their own open findings and, for a
// reviewer, look again at work whose findings are all dealt with.

const (
	myWorkFindingCap = 200 // findings returned; Total says how many there are
	myWorkReviewCap  = 50
)

type SentBack struct {
	By   *UserRef  `json:"by"`
	Note *string   `json:"note"`
	At   time.Time `json:"at"`
}

type WorkFinding struct {
	ID        int64     `json:"id"`
	Severity  string    `json:"severity"`
	Category  string    `json:"category"`
	Title     string    `json:"title"`
	FilePath  string    `json:"file_path"`
	LineStart int       `json:"line_start"`
	SentBack  *SentBack `json:"sent_back"` // set when a reviewer reopened it
}

// WorkItem is a commit or pull request.
type WorkItem struct {
	Kind       string    `json:"kind"` // commit | pull_request
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	Number     *int      `json:"number"` // pull requests only
	Repository string    `json:"repository"`
	Author     *string   `json:"author"` // to_review only
	ReviewedAt time.Time `json:"reviewed_at"`
	Findings   int       `json:"findings"`
	// Open findings, worst first; empty for to_review.
	Open []WorkFinding `json:"open_findings"`
}

type MyWork struct {
	ToFix struct {
		TotalFindings int        `json:"total_findings"` // may exceed what Items holds
		Items         []WorkItem `json:"items"`
	} `json:"to_fix"`
	ToReview []WorkItem `json:"to_review"`
}

// subjects: the latest review of each commit and pull request, not closed and
// not of a pull request that no longer matters. $1 is the user.
const workSubjects = `
	SELECT 'commit' AS kind, c.id, left(split_part(c.message, E'\n', 1), 300) AS title, NULL::int AS number,
	       ws.slug || '/' || r.slug AS repo, lr.id AS review_id, lr.created_at AS reviewed_at,
	       c.author_user_id, u.display_name AS author
	FROM commits c
	JOIN repositories r ON r.id = c.repo_id
	JOIN projects p ON p.id = r.project_id
	JOIN workspaces ws ON ws.id = p.workspace_id
	LEFT JOIN users u ON u.id = c.author_user_id
	JOIN LATERAL (SELECT rv.id, rv.created_at, rv.closed_at FROM reviews rv WHERE rv.commit_id = c.id
	              ORDER BY rv.created_at DESC, rv.id DESC LIMIT 1) lr ON lr.closed_at IS NULL
	UNION ALL
	SELECT 'pull_request', pr.id, pr.title, pr.bb_pr_id, ws.slug || '/' || r.slug, lr.id, lr.created_at,
	       pr.author_user_id, u.display_name
	FROM pull_requests pr
	JOIN repositories r ON r.id = pr.repo_id
	JOIN projects p ON p.id = r.project_id
	JOIN workspaces ws ON ws.id = p.workspace_id
	LEFT JOIN users u ON u.id = pr.author_user_id
	JOIN LATERAL (SELECT rv.id, rv.created_at, rv.closed_at FROM reviews rv WHERE rv.pr_id = pr.id
	              ORDER BY rv.created_at DESC, rv.id DESC LIMIT 1) lr ON lr.closed_at IS NULL
	WHERE pr.state NOT IN ('DECLINED', 'SUPERSEDED', 'DELETED')`

// MyWork lists what userID has to do. A reviewer also gets the work waiting for
// a look; userID 0 (a token with no user) gets nothing.
func (d *Dashboard) MyWork(ctx context.Context, userID int64, reviewer bool) (MyWork, error) {
	w := MyWork{ToReview: []WorkItem{}}
	w.ToFix.Items = []WorkItem{}
	if userID == 0 {
		return w, nil
	}

	rows, err := d.pool.Query(ctx, `
		WITH s AS (`+workSubjects+`)
		SELECT s.kind, s.id, s.title, s.number, s.repo, s.reviewed_at,
		       f.id, f.severity, f.category, f.title, f.file_path, f.line_start,
		       ev.action, ev.note, ev.created_at, ev.actor_id, evu.display_name,
		       count(*) OVER ()::int,
		       (SELECT count(*)::int FROM review_findings x WHERE x.review_id = s.review_id)
		FROM s
		JOIN review_findings f ON f.review_id = s.review_id AND f.status = 'open'
		LEFT JOIN LATERAL (SELECT e.action, e.note, e.created_at, e.actor_id FROM finding_events e
		                   WHERE e.finding_id = f.id ORDER BY e.id DESC LIMIT 1) ev ON true
		LEFT JOIN users evu ON evu.id = ev.actor_id
		WHERE s.author_user_id = $1
		ORDER BY CASE f.severity WHEN 'critical' THEN 0 WHEN 'major' THEN 1 WHEN 'minor' THEN 2 ELSE 3 END,
		         s.reviewed_at DESC, s.kind, s.id, f.id
		LIMIT $2`, userID, myWorkFindingCap)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	at := map[[2]any]int{} // (kind, id) -> index in Items; order = worst finding first
	for rows.Next() {
		var it WorkItem
		var f WorkFinding
		var action, evName *string
		var note *string
		var evAt *time.Time
		var evBy *int64
		if err := rows.Scan(&it.Kind, &it.ID, &it.Title, &it.Number, &it.Repository, &it.ReviewedAt,
			&f.ID, &f.Severity, &f.Category, &f.Title, &f.FilePath, &f.LineStart,
			&action, &note, &evAt, &evBy, &evName, &w.ToFix.TotalFindings, &it.Findings); err != nil {
			return w, err
		}
		if action != nil && *action == "reopened" && evAt != nil {
			sb := &SentBack{Note: note, At: *evAt}
			if evBy != nil && evName != nil {
				sb.By = &UserRef{ID: *evBy, Name: *evName}
			}
			f.SentBack = sb
		}
		key := [2]any{it.Kind, it.ID}
		i, ok := at[key]
		if !ok {
			it.Open = []WorkFinding{}
			w.ToFix.Items = append(w.ToFix.Items, it)
			i = len(w.ToFix.Items) - 1
			at[key] = i
		}
		w.ToFix.Items[i].Open = append(w.ToFix.Items[i].Open, f)
	}
	if err := rows.Err(); err != nil {
		return w, err
	}
	rows.Close()

	if !reviewer {
		return w, nil
	}
	rows, err = d.pool.Query(ctx, `
		WITH s AS (`+workSubjects+`)
		SELECT s.kind, s.id, s.title, s.number, s.repo, s.author, s.reviewed_at, fc.n
		FROM s
		CROSS JOIN LATERAL (SELECT count(*)::int AS n, (count(*) FILTER (WHERE f.status = 'open'))::int AS open
		                    FROM review_findings f WHERE f.review_id = s.review_id) fc
		WHERE fc.n > 0 AND fc.open = 0 AND s.author_user_id IS DISTINCT FROM $1
		ORDER BY s.reviewed_at, s.kind, s.id
		LIMIT $2`, userID, myWorkReviewCap)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var it WorkItem
		if err := rows.Scan(&it.Kind, &it.ID, &it.Title, &it.Number, &it.Repository, &it.Author, &it.ReviewedAt, &it.Findings); err != nil {
			return w, err
		}
		it.Open = []WorkFinding{}
		w.ToReview = append(w.ToReview, it)
	}
	return w, rows.Err()
}
