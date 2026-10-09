package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

// ProfileInfo is what Bitbucket tells us about an account.
type ProfileInfo struct {
	Account   webhook.Account
	AvatarURL string
}

// Member is an account together with its role in a workspace, or its
// permission on a repository.
type Member struct {
	ProfileInfo
	Role string
}

// Workspace and Repo name what a directory sync walks over.
type Workspace struct {
	ID   int64
	Slug string
}

type DirRepo struct {
	ID        int64
	Workspace string
	Slug      string
}

// Workspaces lists every workspace seen so far.
func (s *Syncer) Workspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, slug FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Slug); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Repos lists every repository with the workspace it lives in.
func (s *Syncer) Repos(ctx context.Context) ([]DirRepo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, ws.slug, r.slug FROM repositories r
		JOIN projects p ON p.id = r.project_id JOIN workspaces ws ON ws.id = p.workspace_id
		ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirRepo
	for rows.Next() {
		var r DirRepo
		if err := rows.Scan(&r.ID, &r.Workspace, &r.Slug); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// touchProfile upserts the account and stores what Bitbucket said about it.
func touchProfile(ctx context.Context, tx pgx.Tx, p ProfileInfo) (int64, error) {
	id, err := upsertAccountUser(ctx, tx, p.Account, "", "")
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE users SET avatar_url = COALESCE(NULLIF($2, ''), avatar_url), synced_at = now() WHERE id = $1`, id, p.AvatarURL)
	return id, err
}

// SyncWorkspaceMembers makes workspace_members match Bitbucket for one
// workspace: new people are added, roles updated, people who left removed.
// An empty list changes nothing: Bitbucket answers "no access" with an error,
// and an empty answer is more likely a gap than everyone leaving.
func (s *Syncer) SyncWorkspaceMembers(ctx context.Context, workspaceID int64, members []Member) (int, error) {
	if len(members) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	keep := make([]int64, 0, len(members))
	for _, m := range members {
		if m.Account.UUID == "" && m.Account.AccountID == "" || m.Role == "" {
			continue
		}
		id, err := touchProfile(ctx, tx, m.ProfileInfo)
		if err != nil {
			return 0, fmt.Errorf("member %s: %w", m.Account.DisplayName, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)
			ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role`, workspaceID, id, m.Role); err != nil {
			return 0, err
		}
		keep = append(keep, id)
	}
	if len(keep) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workspace_members WHERE workspace_id = $1 AND NOT (user_id = ANY($2))`, workspaceID, keep); err != nil {
		return 0, err
	}
	return len(keep), tx.Commit(ctx)
}

// SyncRepoPermissions does the same for repo_permissions. Only the three
// levels the table allows are kept.
func (s *Syncer) SyncRepoPermissions(ctx context.Context, repoID int64, members []Member) (int, error) {
	if len(members) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	keep := make([]int64, 0, len(members))
	for _, m := range members {
		if m.Account.UUID == "" && m.Account.AccountID == "" {
			continue
		}
		switch m.Role {
		case "admin", "write", "read":
		default:
			continue
		}
		id, err := touchProfile(ctx, tx, m.ProfileInfo)
		if err != nil {
			return 0, fmt.Errorf("member %s: %w", m.Account.DisplayName, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO repo_permissions (repo_id, user_id, permission) VALUES ($1, $2, $3)
			ON CONFLICT (repo_id, user_id) DO UPDATE SET permission = EXCLUDED.permission`, repoID, id, m.Role); err != nil {
			return 0, err
		}
		keep = append(keep, id)
	}
	if len(keep) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_permissions WHERE repo_id = $1 AND NOT (user_id = ANY($2))`, repoID, keep); err != nil {
		return 0, err
	}
	return len(keep), tx.Commit(ctx)
}

// StaleUser is an account whose profile has not been refreshed lately.
type StaleUser struct {
	ID int64
	// Key is what the Bitbucket users endpoint takes: "{uuid}" or the account id.
	Key string
}

// StaleProfiles returns up to limit Bitbucket accounts not synced within the
// last olderThan, never-synced ones first. People known only by commit email
// have no account and are never listed.
func (s *Syncer) StaleProfiles(ctx context.Context, olderThan time.Duration, limit int) ([]StaleUser, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(bb_uuid, bb_account_id) FROM users
		WHERE (bb_uuid IS NOT NULL OR bb_account_id IS NOT NULL)
		  AND (synced_at IS NULL OR synced_at < now() - make_interval(secs => $1::int))
		ORDER BY synced_at NULLS FIRST, id LIMIT $2`, int(olderThan.Seconds()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaleUser
	for rows.Next() {
		var u StaleUser
		if err := rows.Scan(&u.ID, &u.Key); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SaveProfile stores a fresh profile. Empty fields never erase what is known.
func (s *Syncer) SaveProfile(ctx context.Context, id int64, display, nickname, avatar string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET
			display_name = COALESCE(NULLIF($2, ''), display_name),
			nickname     = COALESCE(NULLIF($3, ''), nickname),
			avatar_url   = COALESCE(NULLIF($4, ''), avatar_url),
			synced_at    = now()
		WHERE id = $1`, id, display, nickname, avatar)
	return err
}

// MarkProfileChecked records that an account was looked at (for example it no
// longer exists), so the next pass does not ask again straight away.
func (s *Syncer) MarkProfileChecked(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET synced_at = now() WHERE id = $1`, id)
	return err
}
