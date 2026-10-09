package worker

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

// DirectoryBitbucket is the part of the Bitbucket client the directory sync uses.
type DirectoryBitbucket interface {
	ListWorkspacePermissions(ctx context.Context, workspace string) ([]bitbucket.WorkspacePermission, error)
	ListRepoUserPermissions(ctx context.Context, workspace, repo string) ([]bitbucket.RepoPermission, error)
	GetUser(ctx context.Context, id string) (bitbucket.User, error)
}

// DirectoryConfig turns the directory sync on. A nil value leaves it off.
type DirectoryConfig struct {
	Interval  time.Duration
	Bitbucket DirectoryBitbucket
	// ProfileMaxAge is how old a profile gets before it is read again; default 24h.
	ProfileMaxAge time.Duration
	// ProfileBatch caps profile lookups per run; default 200.
	ProfileBatch int
}

func (c *DirectoryConfig) enabled() bool { return c != nil && c.Interval > 0 && c.Bitbucket != nil }

const directoryJobTimeout = 15 * time.Minute

type syncDirectoryWorker struct {
	river.WorkerDefaults[jobs.SyncDirectoryArgs]
	d      Deps
	syncer *store.Syncer
}

func (w *syncDirectoryWorker) Timeout(*river.Job[jobs.SyncDirectoryArgs]) time.Duration {
	return directoryJobTimeout
}

// A missing permission to look at something is expected (the token may not be
// an admin of every workspace or repository); it is logged and skipped.
func noAccess(err error) bool {
	if errors.Is(err, bitbucket.ErrNotFound) { // a 404 is its own error in the client
		return true
	}
	var ae *bitbucket.APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusNotFound || ae.Status == http.StatusUnauthorized)
}

func account(u bitbucket.User) store.ProfileInfo {
	return store.ProfileInfo{
		Account:   webhook.Account{UUID: u.UUID, AccountID: u.AccountID, DisplayName: u.DisplayName, Nickname: u.Nickname},
		AvatarURL: u.Links.Avatar.Href,
	}
}

// Work runs one pass: roles of each workspace, permissions of each
// repository, then the profiles that have gone stale. A Bitbucket rate limit
// snoozes the job; the pass is idempotent, so starting over is harmless.
func (w *syncDirectoryWorker) Work(ctx context.Context, job *river.Job[jobs.SyncDirectoryArgs]) error {
	c := w.d.Directory
	if !c.enabled() {
		return river.JobCancel(errors.New("directory sync is not configured"))
	}
	log := w.d.Log
	var members, permissions, profiles, skipped int
	var denied []string // places the token may not read; reported once per pass
	handle := func(what string, err error) (stop error) {
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		}
		if d, ok := snoozeFor(err); ok {
			log.Warn("directory sync rate limited; resuming later", "retry_after", d)
			return river.JobSnooze(d)
		}
		if noAccess(err) {
			skipped++
			denied = append(denied, what)
			log.Debug("directory sync cannot read "+what, "error", err)
			return nil
		}
		skipped++
		log.Error("directory sync failed for "+what, "error", err)
		return nil
	}

	wss, err := w.syncer.Workspaces(ctx)
	if err != nil {
		return wrap("list workspaces", err)
	}
	for _, ws := range wss {
		list, err := c.Bitbucket.ListWorkspacePermissions(ctx, ws.Slug)
		if err != nil {
			if stop := handle("workspace "+ws.Slug, err); stop != nil {
				return stop
			}
			continue
		}
		ms := make([]store.Member, 0, len(list))
		for _, p := range list {
			ms = append(ms, store.Member{ProfileInfo: account(p.User), Role: p.Permission})
		}
		n, err := w.syncer.SyncWorkspaceMembers(ctx, ws.ID, ms)
		if err != nil {
			return wrap("workspace members", err)
		}
		members += n
	}

	repos, err := w.syncer.Repos(ctx)
	if err != nil {
		return wrap("list repositories", err)
	}
	for _, r := range repos {
		list, err := c.Bitbucket.ListRepoUserPermissions(ctx, r.Workspace, r.Slug)
		if err != nil {
			if stop := handle("repository "+r.Workspace+"/"+r.Slug, err); stop != nil {
				return stop
			}
			continue
		}
		ms := make([]store.Member, 0, len(list))
		for _, p := range list {
			ms = append(ms, store.Member{ProfileInfo: account(p.User), Role: p.Permission})
		}
		n, err := w.syncer.SyncRepoPermissions(ctx, r.ID, ms)
		if err != nil {
			return wrap("repository permissions", err)
		}
		permissions += n
	}

	maxAge, batch := c.ProfileMaxAge, c.ProfileBatch
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	if batch <= 0 {
		batch = 200
	}
	stale, err := w.syncer.StaleProfiles(ctx, maxAge, batch)
	if err != nil {
		return wrap("stale profiles", err)
	}
	for _, u := range stale {
		p, err := c.Bitbucket.GetUser(ctx, u.Key)
		if err != nil {
			if noAccess(err) {
				// Gone or hidden: look again tomorrow, not on the next pass.
				if err := w.syncer.MarkProfileChecked(ctx, u.ID); err != nil {
					return wrap("mark profile", err)
				}
			}
			if stop := handle("profile "+u.Key, err); stop != nil {
				return stop
			}
			continue
		}
		if err := w.syncer.SaveProfile(ctx, u.ID, p.DisplayName, p.Nickname, p.Links.Avatar.Href); err != nil {
			return wrap("save profile", err)
		}
		profiles++
	}
	if len(denied) > 0 {
		// One line instead of one per place: a token without admin rights is
		// denied every repository on every pass.
		log.Warn("directory sync skipped places the token cannot read (roles and permissions need an admin token)",
			"count", len(denied), "first", denied[:min(len(denied), 3)])
	}
	log.Info("directory sync finished", "members", members, "permissions", permissions, "profiles", profiles, "skipped", skipped)
	return nil
}
