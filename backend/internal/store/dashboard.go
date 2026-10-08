package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
)

var (
	ErrNotFound      = errors.New("store: not found")
	ErrBadCursor     = errors.New("store: invalid cursor")
	ErrRepoDisabled  = errors.New("review is disabled for this repository")
	ErrReviewRunning = errors.New("review is running")
	ErrMergeCommit   = errors.New("merge commits are not reviewed")
	ErrPRNotOpen     = errors.New("only open pull requests are reviewed")
)

// Dashboard answers the read queries of the REST API and holds the two small
// writes it offers (toggle a repository, re-review a commit).
type Dashboard struct {
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
}

func NewDashboard(pool *pgxpool.Pool, rc *river.Client[pgx.Tx]) *Dashboard {
	return &Dashboard{pool: pool, river: rc}
}

// latestReview is the one review that counts for a commit: the newest.
// Every query that reports a score or findings joins it, so "score of a
// commit" means the same thing everywhere.
const latestReview = `
	LEFT JOIN LATERAL (
		SELECT r.id, r.score, r.created_at, r.closed_at
		FROM reviews r WHERE r.commit_id = c.id
		ORDER BY r.created_at DESC, r.id DESC LIMIT 1
	) lr ON true`

// authorName turns git's raw "Name <email>" into just the name, so e-mail
// addresses are only ever exposed through users.email (admin only).
const authorName = `COALESCE(u.display_name, NULLIF(regexp_replace(COALESCE(c.author_raw, ''), '\s*<[^>]*>\s*$', ''), ''), '')`

// likePattern escapes LIKE wildcards in user input.
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// args collects positional parameters while a WHERE clause is built.
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

func where(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// ---------------------------------------------------------------- overview

type DayPoint struct {
	Day      string   `json:"day"`
	Commits  int      `json:"commits"`
	Reviewed int      `json:"reviewed"`
	AvgScore *float64 `json:"avg_score"`
	CostUSD  float64  `json:"cost_usd"`
}

// Usage totals every review *run* in the window, re-reviews included (money
// is spent per run, not per commit). Reviews that reported nothing (mock)
// count in Runs but add nothing to the sums.
type Usage struct {
	Runs       int      `json:"runs"`
	Measured   int      `json:"measured_runs"`
	TokensIn   int64    `json:"tokens_in"`
	TokensOut  int64    `json:"tokens_out"`
	CostUSD    float64  `json:"cost_usd"`
	AvgCostUSD *float64 `json:"avg_cost_usd"`
}

type Overview struct {
	Usage         Usage          `json:"usage"`
	Days          int            `json:"days"`
	Since         time.Time      `json:"since"`
	Commits       int            `json:"commits"`
	ByStatus      map[string]int `json:"commits_by_status"`
	Reviewed      int            `json:"reviewed"`
	AvgScore      *float64       `json:"avg_score"`
	FindingsBySev map[string]int `json:"findings_by_severity"`
	QueueWaiting  int            `json:"queue_waiting"`
	QueueRunning  int            `json:"queue_running"`
	Series        []DayPoint     `json:"series"`
	ActiveRepos   int            `json:"active_repositories"`
	ActiveAuthors int            `json:"active_authors"`
	EnabledRepos  int            `json:"enabled_repositories"`
	TotalRepos    int            `json:"total_repositories"`
	// Fix is the state of the latest reviews of the commits in the window.
	Fix FixOverview `json:"fix"`
}

type FixOverview struct {
	OpenFindings  int `json:"open_findings"`          // waiting for the author
	FixedFindings int `json:"fixed_findings"`         // fixed, waiting for a reviewer, review not closed
	ReadyToClose  int `json:"reviews_ready_to_close"` // findings all fixed or dismissed, not closed yet
	ClosedReviews int `json:"reviews_closed"`
}

// Overview summarises the last `days` UTC days (today included).
func (d *Dashboard) Overview(ctx context.Context, days int) (Overview, error) {
	now := time.Now().UTC()
	since := now.Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	o := Overview{
		Days: days, Since: since,
		ByStatus:      map[string]int{"pending": 0, "running": 0, "done": 0, "skipped": 0, "failed": 0},
		FindingsBySev: map[string]int{"critical": 0, "major": 0, "minor": 0, "info": 0},
		Series:        []DayPoint{},
	}

	rows, err := d.pool.Query(ctx, `SELECT review_status, count(*) FROM commits WHERE committed_at >= $1 GROUP BY 1`, since)
	if err != nil {
		return o, fmt.Errorf("status counts: %w", err)
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return o, err
		}
		o.ByStatus[s] = n
		o.Commits += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}

	if err := d.pool.QueryRow(ctx, `
		SELECT count(lr.id), round(avg(lr.score)::numeric, 1)::float8,
		       count(DISTINCT c.repo_id), count(DISTINCT c.author_user_id)
		FROM commits c `+latestReview+` WHERE c.committed_at >= $1`, since).
		Scan(&o.Reviewed, &o.AvgScore, &o.ActiveRepos, &o.ActiveAuthors); err != nil {
		return o, fmt.Errorf("review stats: %w", err)
	}

	if err := d.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(fc.open), 0)::int,
		       COALESCE(sum(fc.fixed) FILTER (WHERE lr.closed_at IS NULL), 0)::int,
		       count(*) FILTER (WHERE lr.closed_at IS NULL AND fc.n > 0 AND fc.open = 0)::int,
		       count(*) FILTER (WHERE lr.closed_at IS NOT NULL)::int
		FROM commits c `+latestReview+`
		JOIN LATERAL (SELECT count(*)::int AS n,
		                     (count(*) FILTER (WHERE f.status = 'open'))::int AS open,
		                     (count(*) FILTER (WHERE f.status = 'fixed'))::int AS fixed
		              FROM review_findings f WHERE f.review_id = lr.id) fc ON true
		WHERE c.committed_at >= $1`, since).
		Scan(&o.Fix.OpenFindings, &o.Fix.FixedFindings, &o.Fix.ReadyToClose, &o.Fix.ClosedReviews); err != nil {
		return o, fmt.Errorf("fix workflow: %w", err)
	}

	rows, err = d.pool.Query(ctx, `
		SELECT f.severity, count(*)
		FROM commits c `+latestReview+`
		JOIN review_findings f ON f.review_id = lr.id
		WHERE c.committed_at >= $1 GROUP BY 1`, since)
	if err != nil {
		return o, fmt.Errorf("findings: %w", err)
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return o, err
		}
		o.FindingsBySev[s] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}

	if err := d.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state = 'running'),
		       count(*) FILTER (WHERE state <> 'running')
		FROM river_job
		WHERE kind = $1 AND state IN ('available', 'scheduled', 'retryable', 'running')`, jobs.ReviewCommitArgs{}.Kind()).
		Scan(&o.QueueRunning, &o.QueueWaiting); err != nil {
		return o, fmt.Errorf("queue: %w", err)
	}

	if err := d.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE review_enabled) FROM repositories`).
		Scan(&o.TotalRepos, &o.EnabledRepos); err != nil {
		return o, fmt.Errorf("repo counts: %w", err)
	}

	if err := d.pool.QueryRow(ctx, `
		SELECT count(*), count(cost_usd),
		       COALESCE(sum(tokens_in), 0)::bigint, COALESCE(sum(tokens_out), 0)::bigint,
		       COALESCE(sum(cost_usd), 0)::float8, round(avg(cost_usd)::numeric, 4)::float8
		FROM reviews WHERE created_at >= $1`, since).
		Scan(&o.Usage.Runs, &o.Usage.Measured, &o.Usage.TokensIn, &o.Usage.TokensOut, &o.Usage.CostUSD, &o.Usage.AvgCostUSD); err != nil {
		return o, fmt.Errorf("usage: %w", err)
	}

	costByDay := map[string]float64{}
	crows, err := d.pool.Query(ctx, `
		SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), COALESCE(sum(cost_usd), 0)::float8
		FROM reviews WHERE created_at >= $1 GROUP BY 1`, since)
	if err != nil {
		return o, fmt.Errorf("daily cost: %w", err)
	}
	for crows.Next() {
		var day string
		var c float64
		if err := crows.Scan(&day, &c); err != nil {
			crows.Close()
			return o, err
		}
		costByDay[day] = c
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return o, err
	}

	rows, err = d.pool.Query(ctx, `
		SELECT to_char(g.d, 'YYYY-MM-DD'), count(c.id), count(lr.id), round(avg(lr.score)::numeric, 1)::float8
		FROM generate_series($1::date, $2::date, interval '1 day') AS g(d)
		LEFT JOIN commits c
		       ON c.committed_at >= $3 AND c.committed_at < $4
		      AND (c.committed_at AT TIME ZONE 'UTC')::date = g.d::date
		`+latestReview+`
		GROUP BY g.d ORDER BY g.d`,
		since.Format("2006-01-02"), now.Format("2006-01-02"), since, since.AddDate(0, 0, days))
	if err != nil {
		return o, fmt.Errorf("series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p DayPoint
		if err := rows.Scan(&p.Day, &p.Commits, &p.Reviewed, &p.AvgScore); err != nil {
			return o, err
		}
		p.CostUSD = costByDay[p.Day]
		o.Series = append(o.Series, p)
	}
	return o, rows.Err()
}

// ------------------------------------------------------------ repositories

type Repository struct {
	ID            int64      `json:"id"`
	Workspace     string     `json:"workspace"`
	ProjectKey    string     `json:"project_key"`
	ProjectName   string     `json:"project_name"`
	Slug          string     `json:"slug"`
	Name          string     `json:"name"`
	FullName      string     `json:"full_name"`
	MainLanguage  *string    `json:"main_language"`
	DefaultBranch *string    `json:"default_branch"`
	ReviewEnabled bool       `json:"review_enabled"`
	Commits       int        `json:"commits"`
	Reviewed      int        `json:"reviewed"`
	AvgScore      *float64   `json:"avg_score"`
	LastCommitAt  *time.Time `json:"last_commit_at"`
}

type RepoFilter struct {
	Q       string
	Enabled *bool
}

const repoSelect = `
	SELECT r.id, ws.slug, p.key, p.name, r.slug, r.name, ws.slug || '/' || r.slug,
	       r.main_language, r.default_branch, r.review_enabled,
	       COALESCE(st.commits, 0), COALESCE(st.reviewed, 0), st.avg_score, st.last_commit_at
	FROM repositories r
	JOIN projects p ON p.id = r.project_id
	JOIN workspaces ws ON ws.id = p.workspace_id
	LEFT JOIN LATERAL (
		SELECT count(*) AS commits, count(lr.id) AS reviewed,
		       round(avg(lr.score)::numeric, 1)::float8 AS avg_score, max(c.committed_at) AS last_commit_at
		FROM commits c ` + latestReview + `
		WHERE c.repo_id = r.id
	) st ON true`

func scanRepo(row pgx.Row) (Repository, error) {
	var r Repository
	err := row.Scan(&r.ID, &r.Workspace, &r.ProjectKey, &r.ProjectName, &r.Slug, &r.Name, &r.FullName,
		&r.MainLanguage, &r.DefaultBranch, &r.ReviewEnabled, &r.Commits, &r.Reviewed, &r.AvgScore, &r.LastCommitAt)
	return r, err
}

func (d *Dashboard) Repositories(ctx context.Context, f RepoFilter, limit, offset int) ([]Repository, int, error) {
	var a args
	var conds []string
	if f.Q != "" {
		conds = append(conds, "(ws.slug || '/' || r.slug) ILIKE "+a.add(likePattern(f.Q)))
	}
	if f.Enabled != nil {
		conds = append(conds, "r.review_enabled = "+a.add(*f.Enabled))
	}
	w := where(conds)

	var total int
	if err := d.pool.QueryRow(ctx, `
		SELECT count(*) FROM repositories r JOIN projects p ON p.id = r.project_id
		JOIN workspaces ws ON ws.id = p.workspace_id`+w, a...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count repositories: %w", err)
	}

	q := repoSelect + w + " ORDER BY ws.slug, r.slug, r.id LIMIT " + a.add(limit) + " OFFSET " + a.add(offset)
	rows, err := d.pool.Query(ctx, q, a...)
	if err != nil {
		return nil, 0, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()
	out := []Repository{}
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (d *Dashboard) Repository(ctx context.Context, id int64) (Repository, error) {
	r, err := scanRepo(d.pool.QueryRow(ctx, repoSelect+" WHERE r.id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (d *Dashboard) SetReviewEnabled(ctx context.Context, id int64, enabled bool) (Repository, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE repositories SET review_enabled = $2 WHERE id = $1`, id, enabled)
	if err != nil {
		return Repository{}, err
	}
	if tag.RowsAffected() == 0 {
		return Repository{}, ErrNotFound
	}
	return d.Repository(ctx, id)
}

// ----------------------------------------------------------------- commits

type RepoRef struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type AuthorRef struct {
	ID        *int64  `json:"id"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

type CommitSummary struct {
	ID          int64      `json:"id"`
	Hash        string     `json:"hash"`
	Subject     string     `json:"subject"`
	Repository  RepoRef    `json:"repository"`
	Author      AuthorRef  `json:"author"`
	Branch      *string    `json:"branch"`
	CommittedAt time.Time  `json:"committed_at"`
	Status      string     `json:"review_status"`
	SkipReason  *string    `json:"review_skip_reason"`
	Files       *int       `json:"files_changed"`
	Additions   *int       `json:"additions"`
	Deletions   *int       `json:"deletions"`
	IsMerge     bool       `json:"is_merge"`
	Score       *int       `json:"score"`
	Findings    int        `json:"findings"`
	ReviewedAt  *time.Time `json:"reviewed_at"`
	// Fix workflow, of the latest review: findings the author still has to
	// fix (open), and whether a reviewer closed it.
	OpenFindings int  `json:"open_findings"`
	Closed       bool `json:"review_closed"`
}

const commitCols = `
	c.id, c.hash, left(split_part(c.message, E'\n', 1), 300), r.id, ws.slug || '/' || r.slug,
	u.id, ` + authorName + `, u.avatar_url,
	c.branch, c.committed_at, c.review_status, c.review_skip_reason,
	c.files_changed, c.additions, c.deletions, c.is_merge,
	lr.score, COALESCE(fc.n, 0), lr.created_at, COALESCE(fc.open, 0), lr.closed_at IS NOT NULL`

const commitFrom = `
	FROM commits c
	JOIN repositories r ON r.id = c.repo_id
	JOIN projects p ON p.id = r.project_id
	JOIN workspaces ws ON ws.id = p.workspace_id
	LEFT JOIN users u ON u.id = c.author_user_id` + latestReview + `
	LEFT JOIN LATERAL (SELECT count(*)::int AS n, (count(*) FILTER (WHERE f.status = 'open'))::int AS open FROM review_findings f WHERE f.review_id = lr.id) fc ON true`

func (c *CommitSummary) dests() []any {
	return []any{&c.ID, &c.Hash, &c.Subject, &c.Repository.ID, &c.Repository.FullName,
		&c.Author.ID, &c.Author.Name, &c.Author.AvatarURL,
		&c.Branch, &c.CommittedAt, &c.Status, &c.SkipReason,
		&c.Files, &c.Additions, &c.Deletions, &c.IsMerge,
		&c.Score, &c.Findings, &c.ReviewedAt, &c.OpenFindings, &c.Closed}
}

type CommitFilter struct {
	RepoID   int64
	AuthorID int64
	Status   string
	Branch   string
	Q        string
	Since    *time.Time
	Until    *time.Time
	Fix      string // open | ready | closed, see fixCond
	Offset   int    // skip this many rows; not for use with a cursor
}

// ValidFix reports whether s is a fix filter value ("" means no filter).
func ValidFix(s string) bool {
	return s == "" || s == "open" || s == "ready" || s == "closed"
}

// fixCond narrows by the fix workflow state of the latest review: open has
// findings still to fix, ready has none left and waits for a reviewer, closed
// was closed by a reviewer. Needs the lr and fc joins.
func fixCond(fix string) string {
	switch fix {
	case "open":
		return "(lr.id IS NOT NULL AND lr.closed_at IS NULL AND COALESCE(fc.open, 0) > 0)"
	case "ready":
		return "(lr.id IS NOT NULL AND lr.closed_at IS NULL AND COALESCE(fc.open, 0) = 0)"
	case "closed":
		return "lr.closed_at IS NOT NULL"
	}
	return ""
}

func encodeCursor(t time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(t.UnixMicro(), 10) + ":" + strconv.FormatInt(id, 10)))
}

func decodeCursor(s string) (time.Time, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, ErrBadCursor
	}
	us, ids, ok := strings.Cut(string(b), ":")
	if !ok {
		return time.Time{}, 0, ErrBadCursor
	}
	n, err1 := strconv.ParseInt(us, 10, 64)
	id, err2 := strconv.ParseInt(ids, 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, 0, ErrBadCursor
	}
	return time.UnixMicro(n).UTC(), id, nil
}

// commitConds is the WHERE of a commit list, shared by the page and its count.
func commitConds(f CommitFilter, a *args) []string {
	var conds []string
	if f.RepoID != 0 {
		conds = append(conds, "c.repo_id = "+a.add(f.RepoID))
	}
	if f.AuthorID != 0 {
		conds = append(conds, "c.author_user_id = "+a.add(f.AuthorID))
	}
	if f.Status != "" {
		conds = append(conds, "c.review_status = "+a.add(f.Status))
	}
	if f.Branch != "" {
		conds = append(conds, "c.branch = "+a.add(f.Branch))
	}
	if f.Q != "" {
		p := a.add(likePattern(f.Q))
		conds = append(conds, "(c.message ILIKE "+p+" OR c.hash ILIKE "+p+")")
	}
	if f.Since != nil {
		conds = append(conds, "c.committed_at >= "+a.add(*f.Since))
	}
	if f.Until != nil {
		conds = append(conds, "c.committed_at < "+a.add(*f.Until))
	}
	if c := fixCond(f.Fix); c != "" {
		conds = append(conds, c)
	}
	return conds
}

// CountCommits is the number of commits a filter matches, for page numbers.
func (d *Dashboard) CountCommits(ctx context.Context, f CommitFilter) (int, error) {
	var a args
	var n int
	if err := d.pool.QueryRow(ctx, "SELECT count(*)::int"+commitFrom+where(commitConds(f, &a)), a...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count commits: %w", err)
	}
	return n, nil
}

// Commits lists newest first. Page with next as the cursor (stable while new
// commits arrive), or with f.Offset when the caller wants page numbers.
func (d *Dashboard) Commits(ctx context.Context, f CommitFilter, cursor string, limit int) (items []CommitSummary, next string, err error) {
	var a args
	conds := commitConds(f, &a)
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		conds = append(conds, "(c.committed_at, c.id) < ("+a.add(t)+", "+a.add(id)+")")
	}

	q := "SELECT " + commitCols + commitFrom + where(conds) +
		" ORDER BY c.committed_at DESC, c.id DESC LIMIT " + a.add(limit+1) + " OFFSET " + a.add(f.Offset)
	rows, err := d.pool.Query(ctx, q, a...)
	if err != nil {
		return nil, "", fmt.Errorf("list commits: %w", err)
	}
	defer rows.Close()
	items = []CommitSummary{}
	for rows.Next() {
		var c CommitSummary
		if err := rows.Scan(c.dests()...); err != nil {
			return nil, "", err
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = encodeCursor(last.CommittedAt, last.ID)
	}
	return items, next, nil
}

type Suggestion struct {
	ID               int64  `json:"id"`
	OriginalSnippet  string `json:"original_snippet"`
	SuggestedSnippet string `json:"suggested_snippet"`
	UnifiedDiff      string `json:"unified_diff"`
	Status           string `json:"status"`
}

type Finding struct {
	ID          int64  `json:"id"`
	FilePath    string `json:"file_path"`
	LineStart   int    `json:"line_start"`
	LineEnd     int    `json:"line_end"`
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
	// CodeContext is the diff hunk the finding is about; null for reviews made
	// before it was recorded.
	CodeContext *string     `json:"code_context"`
	Suggestion  *Suggestion `json:"suggestion"`
	// Fix workflow: open -> fixed (by the author) -> back to open, dismissed or left fixed.
	Status     string         `json:"status"`
	StatusBy   *UserRef       `json:"status_by"`
	StatusNote *string        `json:"status_note"`
	StatusAt   *time.Time     `json:"status_at"`
	History    []FindingEvent `json:"history"`
}

type Review struct {
	ID            int64         `json:"id"`
	Model         string        `json:"model"`
	PromptVersion string        `json:"prompt_version"`
	Score         *int          `json:"score"`
	Summary       string        `json:"summary"`
	DurationMs    *int          `json:"duration_ms"`
	TokensIn      *int          `json:"tokens_in"`
	TokensOut     *int          `json:"tokens_out"`
	CostUSD       *float64      `json:"cost_usd"`
	CreatedAt     time.Time     `json:"created_at"`
	Closed        *ReviewClosed `json:"closed"`
	Findings      []Finding     `json:"findings"`
}

type CommitDetail struct {
	CommitSummary
	Message      string  `json:"message"`
	ReviewsCount int     `json:"reviews_count"`
	Review       *Review `json:"review"`
}

func (d *Dashboard) Commit(ctx context.Context, id int64) (CommitDetail, error) {
	var cd CommitDetail
	err := d.pool.QueryRow(ctx, "SELECT "+commitCols+", c.message, (SELECT count(*) FROM reviews rv WHERE rv.commit_id = c.id)"+
		commitFrom+" WHERE c.id = $1", id).
		Scan(append(cd.CommitSummary.dests(), &cd.Message, &cd.ReviewsCount)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return cd, ErrNotFound
	}
	if err != nil {
		return cd, fmt.Errorf("commit: %w", err)
	}
	if cd.ReviewsCount == 0 {
		return cd, nil
	}

	rv, err := d.latestReviewOf(ctx, "commit_id", id)
	if err != nil {
		return cd, err
	}
	cd.Review = rv
	return cd, nil
}

// latestReviewOf loads the newest review of a commit (col = "commit_id") or of
// a pull request (col = "pr_id") with its findings and suggestions. col is
// never user input.
func (d *Dashboard) latestReviewOf(ctx context.Context, col string, id int64) (*Review, error) {
	rv := &Review{Findings: []Finding{}}
	var (
		closedAt              *time.Time
		closedBy              *int64
		closedName, closeNote *string
	)
	err := d.pool.QueryRow(ctx, `
		SELECT r.id, r.model, r.prompt_version, r.score, r.summary, r.duration_ms, r.tokens_in, r.tokens_out, r.cost_usd::float8, r.created_at,
		       r.closed_at, r.closed_by, cu.display_name, r.close_note
		FROM reviews r LEFT JOIN users cu ON cu.id = r.closed_by
		WHERE r.`+col+` = $1 ORDER BY r.created_at DESC, r.id DESC LIMIT 1`, id).
		Scan(&rv.ID, &rv.Model, &rv.PromptVersion, &rv.Score, &rv.Summary, &rv.DurationMs, &rv.TokensIn, &rv.TokensOut, &rv.CostUSD, &rv.CreatedAt,
			&closedAt, &closedBy, &closedName, &closeNote)
	if err != nil {
		return nil, fmt.Errorf("review: %w", err)
	}
	if closedAt != nil {
		rv.Closed = &ReviewClosed{At: *closedAt, Note: closeNote}
		if closedBy != nil {
			rv.Closed.By = &UserRef{ID: *closedBy, Name: ptrStr(closedName)}
		}
	}

	rows, err := d.pool.Query(ctx, `
		SELECT f.id, f.file_path, f.line_start, f.line_end, f.severity, f.category, f.title, f.explanation, f.code_context,
		       f.status, f.status_by, su.display_name, f.status_note, f.status_at,
		       s.id, s.original_snippet, s.suggested_snippet, s.unified_diff, s.status
		FROM review_findings f
		LEFT JOIN code_suggestions s ON s.finding_id = f.id
		LEFT JOIN users su ON su.id = f.status_by
		WHERE f.review_id = $1
		ORDER BY CASE f.severity WHEN 'critical' THEN 0 WHEN 'major' THEN 1 WHEN 'minor' THEN 2 ELSE 3 END,
		         f.file_path, f.line_start, f.id, s.id`, rv.ID)
	if err != nil {
		return nil, fmt.Errorf("findings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		var sid *int64
		var orig, sugg, diff, status, byName *string
		var by *int64
		if err := rows.Scan(&f.ID, &f.FilePath, &f.LineStart, &f.LineEnd, &f.Severity, &f.Category, &f.Title, &f.Explanation, &f.CodeContext,
			&f.Status, &by, &byName, &f.StatusNote, &f.StatusAt,
			&sid, &orig, &sugg, &diff, &status); err != nil {
			return nil, err
		}
		if n := len(rv.Findings); n > 0 && rv.Findings[n-1].ID == f.ID {
			continue // a second suggestion for the same finding; one is shown
		}
		if by != nil {
			f.StatusBy = &UserRef{ID: *by, Name: ptrStr(byName)}
		}
		f.History = []FindingEvent{}
		if sid != nil {
			f.Suggestion = &Suggestion{ID: *sid, OriginalSnippet: *orig, SuggestedSnippet: *sugg, UnifiedDiff: *diff, Status: *status}
		}
		rv.Findings = append(rv.Findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rv, d.loadHistory(ctx, rv)
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// loadHistory fills in what happened to each finding after the review.
func (d *Dashboard) loadHistory(ctx context.Context, rv *Review) error {
	if len(rv.Findings) == 0 {
		return nil
	}
	at := make(map[int64]*Finding, len(rv.Findings))
	for i := range rv.Findings {
		at[rv.Findings[i].ID] = &rv.Findings[i]
	}
	rows, err := d.pool.Query(ctx, `
		SELECT e.finding_id, e.action, e.actor_id, u.display_name, e.note, e.created_at
		FROM finding_events e
		JOIN review_findings f ON f.id = e.finding_id
		LEFT JOIN users u ON u.id = e.actor_id
		WHERE f.review_id = $1 ORDER BY e.id`, rv.ID)
	if err != nil {
		return fmt.Errorf("finding history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var ev FindingEvent
		var by *int64
		var name *string
		if err := rows.Scan(&fid, &ev.Action, &by, &name, &ev.Note, &ev.At); err != nil {
			return err
		}
		if by != nil {
			ev.By = &UserRef{ID: *by, Name: ptrStr(name)}
		}
		if f := at[fid]; f != nil {
			f.History = append(f.History, ev)
		}
	}
	return rows.Err()
}

// Rereview puts a commit back in the queue. The status change and the job
// are one transaction, so a commit is never left "pending" without a job.
func (d *Dashboard) Rereview(ctx context.Context, id int64) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var status string
	var merge, enabled bool
	err = tx.QueryRow(ctx, `
		SELECT c.review_status, c.is_merge, r.review_enabled
		FROM commits c JOIN repositories r ON r.id = c.repo_id
		WHERE c.id = $1 FOR UPDATE OF c`, id).Scan(&status, &merge, &enabled)
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
	case merge:
		return ErrMergeCommit
	}

	if _, err := tx.Exec(ctx, `UPDATE commits SET review_status = 'pending', review_skip_reason = NULL WHERE id = $1`, id); err != nil {
		return err
	}
	// Default uniqueness also counts a *completed* job, which would swallow
	// this request; only a job that is still waiting or running may dedupe it.
	opts := jobs.ReviewCommitArgs{}.InsertOpts()
	opts.UniqueOpts = river.UniqueOpts{
		ByArgs: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
			rivertype.JobStateRetryable, rivertype.JobStateScheduled,
		},
	}
	if _, err := d.river.InsertTx(ctx, tx, jobs.ReviewCommitArgs{CommitID: id}, &opts); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return tx.Commit(ctx)
}

// ------------------------------------------------------------------- users

type User struct {
	ID          int64   `json:"id"`
	DisplayName string  `json:"display_name"`
	Nickname    *string `json:"nickname"`
	AvatarURL   *string `json:"avatar_url"`
	// Email is only filled for admins (the handler clears it otherwise).
	Email      *string  `json:"email,omitempty"`
	JobTitle   *string  `json:"job_title"`
	Department *string  `json:"department"`
	Linked     bool     `json:"linked"`
	Commits    int      `json:"commits"`
	Reviewed   int      `json:"reviewed"`
	AvgScore   *float64 `json:"avg_score"`
	Findings   int      `json:"findings"`
}

type WeekPoint struct {
	Week     string   `json:"week"`
	Commits  int      `json:"commits"`
	Reviewed int      `json:"reviewed"`
	AvgScore *float64 `json:"avg_score"`
}

type UserDetail struct {
	User
	Days  int         `json:"days"`
	Trend []WeekPoint `json:"trend"`
}

// userSelect: stats cover commits since $1 only. "linked" is false for people
// known only from a raw git author string.
const userSelect = `
	SELECT u.id, u.display_name, u.nickname, u.avatar_url, u.email, u.job_title, u.department,
	       (u.bb_account_id IS NOT NULL OR u.bb_uuid IS NOT NULL),
	       COALESCE(st.commits, 0), COALESCE(st.reviewed, 0), st.avg_score, COALESCE(st.findings, 0)
	FROM users u
	LEFT JOIN LATERAL (
		SELECT count(*)::int AS commits, count(lr.id)::int AS reviewed,
		       round(avg(lr.score)::numeric, 1)::float8 AS avg_score,
		       COALESCE(sum(fc.n), 0)::int AS findings
		FROM commits c ` + latestReview + `
		LEFT JOIN LATERAL (SELECT count(*) AS n FROM review_findings f WHERE f.review_id = lr.id) fc ON true
		WHERE c.author_user_id = u.id AND c.committed_at >= $1
	) st ON true`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.DisplayName, &u.Nickname, &u.AvatarURL, &u.Email, &u.JobTitle, &u.Department,
		&u.Linked, &u.Commits, &u.Reviewed, &u.AvgScore, &u.Findings)
	return u, err
}

var userOrder = map[string]string{
	"name":    "u.display_name, u.id",
	"commits": "st.commits DESC NULLS LAST, u.display_name, u.id",
	"score":   "st.avg_score DESC NULLS LAST, u.display_name, u.id",
}

// IsUserSort reports whether s is an accepted sort key.
func IsUserSort(s string) bool { _, ok := userOrder[s]; return ok }

func (d *Dashboard) Users(ctx context.Context, q, sort string, days, limit, offset int) ([]User, int, error) {
	order, ok := userOrder[sort]
	if !ok {
		order = userOrder["name"]
	}
	since := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	// The count query has no stats, hence no $1; build its filter on its own.
	var ca args
	var cc []string
	if q != "" {
		p := ca.add(likePattern(q))
		cc = append(cc, "(u.display_name ILIKE "+p+" OR u.nickname ILIKE "+p+")")
	}
	var total int
	if err := d.pool.QueryRow(ctx, "SELECT count(*) FROM users u"+where(cc), ca...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	a := args{since}
	var conds []string
	if q != "" {
		p := a.add(likePattern(q))
		conds = append(conds, "(u.display_name ILIKE "+p+" OR u.nickname ILIKE "+p+")")
	}
	w := where(conds)
	rows, err := d.pool.Query(ctx, userSelect+w+" ORDER BY "+order+" LIMIT "+a.add(limit)+" OFFSET "+a.add(offset), a...)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func (d *Dashboard) User(ctx context.Context, id int64, days int) (UserDetail, error) {
	since := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	u, err := scanUser(d.pool.QueryRow(ctx, userSelect+" WHERE u.id = $2", since, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return UserDetail{}, ErrNotFound
	}
	if err != nil {
		return UserDetail{}, fmt.Errorf("user: %w", err)
	}
	ud := UserDetail{User: u, Days: days, Trend: []WeekPoint{}}

	rows, err := d.pool.Query(ctx, `
		SELECT to_char(date_trunc('week', c.committed_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
		       count(*)::int, count(lr.id)::int, round(avg(lr.score)::numeric, 1)::float8
		FROM commits c `+latestReview+`
		WHERE c.author_user_id = $1 AND c.committed_at >= $2
		GROUP BY 1 ORDER BY 1`, id, since)
	if err != nil {
		return ud, fmt.Errorf("trend: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p WeekPoint
		if err := rows.Scan(&p.Week, &p.Commits, &p.Reviewed, &p.AvgScore); err != nil {
			return ud, err
		}
		ud.Trend = append(ud.Trend, p)
	}
	return ud, rows.Err()
}

// UserName is the display name of a user, for showing who a token belongs to.
func (d *Dashboard) UserName(ctx context.Context, id int64) (string, error) {
	var name string
	err := d.pool.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
