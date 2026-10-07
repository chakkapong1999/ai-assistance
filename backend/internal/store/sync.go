package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

const maxMessageBytes = 64 << 10

// BranchCommits is the commits a push added to one branch.
type BranchCommits struct {
	Branch  string
	Commits []webhook.Commit
}

// PushInput is everything SyncPush needs; fetching it (including completing a
// truncated push) is the caller's job so this layer stays pure database work.
type PushInput struct {
	Repo     webhook.Repository
	Branches []BranchCommits
}

// NewCommit is a commit that did not exist before this push.
type NewCommit struct {
	ID      int64
	Hash    string
	IsMerge bool
}

// PushResult tells the worker what to do next.
type PushResult struct {
	RepoID int64
	// ReviewEnabled is false for repos nobody has switched on yet; their
	// commits are recorded but must not be sent to an LLM.
	ReviewEnabled bool
	NewCommits    []NewCommit
}

// Syncer writes Bitbucket data into Postgres.
type Syncer struct {
	pool           *pgxpool.Pool
	reviewNewRepos bool
}

func NewSyncer(pool *pgxpool.Pool) *Syncer { return &Syncer{pool: pool} }

// ReviewNewRepos sets whether a repository created by a sync starts with
// review on. It is only the starting value; syncing an existing repository
// never changes it. Off unless asked for.
func (s *Syncer) ReviewNewRepos(on bool) *Syncer {
	s.reviewNewRepos = on
	return s
}

// SyncPush creates whatever is missing (workspace, project, repository, users)
// and records the pushed commits, all in one transaction. It is idempotent:
// replaying the same push returns no NewCommits and changes nothing, which is
// what lets River retry the job safely.
func (s *Syncer) SyncPush(ctx context.Context, in PushInput) (PushResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PushResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	res, err := s.SyncPushTx(ctx, tx, in)
	if err != nil {
		return PushResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PushResult{}, err
	}
	return res, nil
}

// SyncPushTx does the same inside the caller's transaction, so the worker can
// enqueue review jobs atomically with the commits they refer to: a commit is
// never stored without its job (or skip decision), and never queued twice.
func (s *Syncer) SyncPushTx(ctx context.Context, tx pgx.Tx, in PushInput) (PushResult, error) {
	repo := in.Repo
	if repo.UUID == "" {
		return PushResult{}, errors.New("sync: repository has no uuid")
	}
	if repo.Workspace == nil || repo.Workspace.UUID == "" {
		return PushResult{}, errors.New("sync: repository has no workspace")
	}

	var wsID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspaces (bb_uuid, slug, name) VALUES ($1, $2, $3)
		ON CONFLICT (bb_uuid) DO UPDATE SET slug = EXCLUDED.slug, name = EXCLUDED.name
		RETURNING id`,
		repo.Workspace.UUID, repo.Workspace.Slug, firstNonEmpty(repo.Workspace.Name, repo.Workspace.Slug),
	).Scan(&wsID); err != nil {
		return PushResult{}, fmt.Errorf("upsert workspace: %w", err)
	}

	// Every repository belongs to a project in Bitbucket Cloud, but do not
	// depend on the payload saying so: fall back to one placeholder per workspace.
	projUUID, projKey, projName := "none:"+repo.Workspace.UUID, "NONE", "No project"
	if p := repo.Project; p != nil && p.UUID != "" {
		projUUID, projKey, projName = p.UUID, p.Key, firstNonEmpty(p.Name, p.Key)
	}
	var projID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO projects (workspace_id, bb_uuid, key, name) VALUES ($1, $2, $3, $4)
		ON CONFLICT (bb_uuid) DO UPDATE SET workspace_id = EXCLUDED.workspace_id, key = EXCLUDED.key, name = EXCLUDED.name
		RETURNING id`, wsID, projUUID, projKey, projName,
	).Scan(&projID); err != nil {
		return PushResult{}, fmt.Errorf("upsert project: %w", err)
	}

	// review_enabled is set on insert only and is deliberately absent from the
	// UPDATE: an admin's choice must survive every later push.
	res := PushResult{}
	if err := tx.QueryRow(ctx, `
		INSERT INTO repositories (project_id, bb_uuid, slug, name, default_branch, review_enabled)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)
		ON CONFLICT (bb_uuid) DO UPDATE SET
			project_id = EXCLUDED.project_id, slug = EXCLUDED.slug, name = EXCLUDED.name,
			default_branch = COALESCE(EXCLUDED.default_branch, repositories.default_branch)
		RETURNING id, review_enabled`,
		projID, repo.UUID, repo.Slug(), firstNonEmpty(repo.Name, repo.Slug()), repo.DefaultBranch(), s.reviewNewRepos,
	).Scan(&res.RepoID, &res.ReviewEnabled); err != nil {
		return PushResult{}, fmt.Errorf("upsert repository: %w", err)
	}

	for _, bc := range in.Branches {
		for _, c := range bc.Commits {
			if c.Hash == "" {
				continue
			}
			authorID, err := resolveAuthor(ctx, tx, c)
			if err != nil {
				return PushResult{}, fmt.Errorf("resolve author of %s: %w", c.Hash, err)
			}
			committed := c.Date
			if committed.IsZero() {
				committed = time.Now()
			}
			var id int64
			err = tx.QueryRow(ctx, `
				INSERT INTO commits (repo_id, hash, author_user_id, author_raw, message, branch, committed_at, is_merge)
				VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8)
				ON CONFLICT (repo_id, hash) DO NOTHING
				RETURNING id`,
				res.RepoID, c.Hash, authorID, cleanText(c.Author.Raw, 1000), cleanText(c.Message, maxMessageBytes), bc.Branch, committed, c.IsMerge(),
			).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already known (re-push, rebase, or reached via another branch)
			}
			if err != nil {
				return PushResult{}, fmt.Errorf("insert commit %s: %w", c.Hash, err)
			}
			res.NewCommits = append(res.NewCommits, NewCommit{ID: id, Hash: c.Hash, IsMerge: c.IsMerge()})
		}
	}

	return res, nil
}

var authorRe = regexp.MustCompile(`^\s*(.*?)\s*<([^<>]+)>\s*$`)

// parseAuthor splits git's "Name <email>"; either part may be empty.
func parseAuthor(raw string) (name, email string) {
	if m := authorRe.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[1]), strings.ToLower(strings.TrimSpace(m[2]))
	}
	return strings.TrimSpace(raw), ""
}

// resolveAuthor returns the users.id for a commit's author, creating the user
// if needed, or nil when the commit gives nothing to identify them by.
//
//   - Bitbucket linked the commit to an account: that account's user.
//   - Otherwise a known email maps to its user.
//   - Otherwise a raw-only user (no Bitbucket ids) is created from the email,
//     so the author still shows up in the dashboard. If that person's account
//     appears later, upsertAccountUser adopts this row instead of duplicating.
func resolveAuthor(ctx context.Context, tx pgx.Tx, c webhook.Commit) (*int64, error) {
	name, email := parseAuthor(c.Author.Raw)

	var id int64
	var err error
	switch acct := c.Author.User; {
	case acct != nil && (acct.UUID != "" || acct.AccountID != ""):
		id, err = upsertAccountUser(ctx, tx, *acct, name, email)
	case email != "":
		id, err = userByEmail(ctx, tx, email)
		if errors.Is(err, pgx.ErrNoRows) {
			id, err = createRawUser(ctx, tx, firstNonEmpty(name, email), email)
		}
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if email != "" {
		if err := linkEmail(ctx, tx, id, email); err != nil {
			return nil, err
		}
	}
	return &id, nil
}

// createRawUser makes a user known only by commit email. If a concurrent
// transaction claimed the address first, the user created here is removed and
// the winner's id is returned, so one address never ends up with two users.
func createRawUser(ctx context.Context, tx pgx.Tx, display, email string) (int64, error) {
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ($1) RETURNING id`, display).Scan(&id); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO user_emails (user_id, email, source) VALUES ($1, $2, 'commit') ON CONFLICT DO NOTHING`, id, email)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
			return 0, err
		}
		return userByEmail(ctx, tx, email)
	}
	return id, nil
}

func userByEmail(ctx context.Context, tx pgx.Tx, email string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM user_emails WHERE lower(email) = lower($1)`, email).Scan(&id)
	return id, err
}

func upsertAccountUser(ctx context.Context, tx pgx.Tx, a webhook.Account, rawName, email string) (int64, error) {
	accountID, uuid := nullable(a.AccountID), nullable(a.UUID)

	var id int64
	findByIDs := func() error {
		return tx.QueryRow(ctx, `
			SELECT id FROM users
			WHERE ($1::text IS NOT NULL AND bb_account_id = $1) OR ($2::text IS NOT NULL AND bb_uuid = $2)
			ORDER BY id LIMIT 1`, accountID, uuid).Scan(&id)
	}

	err := findByIDs()
	if errors.Is(err, pgx.ErrNoRows) && email != "" {
		// Adopt a raw-only user that was created from this person's commit email.
		err = tx.QueryRow(ctx, `
			SELECT u.id FROM user_emails e JOIN users u ON u.id = e.user_id
			WHERE lower(e.email) = lower($1) AND u.bb_account_id IS NULL AND u.bb_uuid IS NULL
			LIMIT 1`, email).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING (any unique index): a concurrent job may have
		// created the same user a moment ago; then look it up again.
		err = tx.QueryRow(ctx, `
			INSERT INTO users (bb_account_id, bb_uuid, display_name, nickname)
			VALUES ($1, $2, $3, NULLIF($4, ''))
			ON CONFLICT DO NOTHING
			RETURNING id`,
			accountID, uuid, firstNonEmpty(a.DisplayName, a.Nickname, rawName, email, "unknown"), a.Nickname,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = findByIDs()
		}
	}
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE users SET
			bb_account_id = COALESCE(bb_account_id, $2),
			bb_uuid       = COALESCE(bb_uuid, $3),
			display_name  = CASE WHEN $4 <> '' THEN $4 ELSE display_name END,
			nickname      = COALESCE(NULLIF($5, ''), nickname)
		WHERE id = $1`, id, accountID, uuid, a.DisplayName, a.Nickname)
	return id, err
}

// linkEmail records that an address belongs to a user. An address already
// owned by someone else is left with its owner (one address, one user).
func linkEmail(ctx context.Context, tx pgx.Tx, userID int64, email string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_emails (user_id, email, source) VALUES ($1, $2, 'commit')
		ON CONFLICT DO NOTHING`, userID, email); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE users SET email = $2 WHERE id = $1 AND email IS NULL`, userID, email)
	return err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// cleanText makes a string safe for a Postgres text column (no NUL, valid
// UTF-8) and at most max bytes, never cutting a multi-byte character.
func cleanText(s string, max int) string {
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > max {
		s = s[:max]
	}
	return strings.ToValidUTF8(s, "")
}
