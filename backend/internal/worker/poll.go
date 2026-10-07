package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/ingest"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

// PollBitbucket is the part of the Bitbucket client polling uses.
type PollBitbucket interface {
	GetRepository(ctx context.Context, workspace, repo string) (webhook.Repository, error)
	ListWorkspaceRepositories(ctx context.Context, workspace string) ([]webhook.Repository, error)
	ListBranches(ctx context.Context, workspace, repo string) ([]bitbucket.Branch, error)
	ListRecentCommits(ctx context.Context, workspace, repo, include, exclude string, since time.Time, max int) ([]webhook.Commit, error)
}

// PollConfig turns polling on. Without it (or with no Repos) the worker only
// reacts to webhooks.
type PollConfig struct {
	Repos     []string // "workspace/repo" or "workspace/*"
	Interval  time.Duration
	Lookback  time.Duration
	Bitbucket PollBitbucket
}

func (p *PollConfig) enabled() bool { return p != nil && len(p.Repos) > 0 && p.Bitbucket != nil }

const pollJobTimeout = 10 * time.Minute

type pollReposWorker struct {
	river.WorkerDefaults[jobs.PollReposArgs]
	d      Deps
	syncer *store.Syncer
}

func (w *pollReposWorker) Timeout(*river.Job[jobs.PollReposArgs]) time.Duration {
	return pollJobTimeout
}

// Work runs one polling round. Each repository is synced in its own
// transaction, so progress survives a failure half way, and one repository
// that cannot be read (renamed, no access) is logged and does not stop the
// others. Only a Bitbucket rate limit ends the round early, by snoozing it.
func (w *pollReposWorker) Work(ctx context.Context, job *river.Job[jobs.PollReposArgs]) error {
	p := w.d.Poll
	if !p.enabled() {
		return river.JobCancel(errors.New("polling is not configured"))
	}

	repos, expandErrs := w.expand(ctx, p)
	if d, ok := firstSnooze(expandErrs); ok {
		return river.JobSnooze(d)
	}

	failed := len(expandErrs)
	newCommits := 0
	for _, meta := range repos {
		n, err := w.pollRepo(ctx, p, meta)
		if err != nil {
			if d, ok := snoozeFor(err); ok {
				w.d.Log.Warn("poll rate limited; resuming later", "retry_after", d)
				return river.JobSnooze(d)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			w.d.Log.Error("poll failed for repository", "repo", meta.FullName, "error", err)
			continue
		}
		newCommits += n
	}
	for _, e := range expandErrs {
		w.d.Log.Error("poll cannot resolve a configured repository", "error", e)
	}
	w.d.Log.Info("poll round finished", "repositories", len(repos), "failed", failed, "new_commits", newCommits)
	return nil
}

func firstSnooze(errs []error) (time.Duration, bool) {
	for _, e := range errs {
		if d, ok := snoozeFor(e); ok {
			return d, true
		}
	}
	return 0, false
}

// expand resolves the configured entries to repository records. A "ws/*"
// entry lists the workspace; a repository listed there without a main branch
// is looked up on its own.
func (w *pollReposWorker) expand(ctx context.Context, p *PollConfig) ([]webhook.Repository, []error) {
	var out []webhook.Repository
	var errs []error
	seen := map[string]bool{}
	add := func(r webhook.Repository) {
		if r.UUID != "" && !seen[r.UUID] {
			seen[r.UUID] = true
			out = append(out, r)
		}
	}
	for _, entry := range p.Repos {
		ws, slug, _ := strings.Cut(entry, "/")
		if slug == "*" {
			list, err := p.Bitbucket.ListWorkspaceRepositories(ctx, ws)
			if err != nil {
				errs = append(errs, fmt.Errorf("list workspace %s: %w", ws, err))
				continue
			}
			for _, r := range list {
				if r.DefaultBranch() == "" {
					full, err := p.Bitbucket.GetRepository(ctx, ws, r.Slug())
					if err != nil {
						errs = append(errs, fmt.Errorf("repository %s: %w", r.FullName, err))
						continue
					}
					r = full
				}
				add(r)
			}
			continue
		}
		r, err := p.Bitbucket.GetRepository(ctx, ws, slug)
		if err != nil {
			errs = append(errs, fmt.Errorf("repository %s: %w", entry, err))
			continue
		}
		add(r)
	}
	return out, errs
}

// pollRepo brings one repository up to date and returns how many commits were new.
//
// For every branch whose head moved since the last round it reads the commits
// that are new (not reachable from the last synced head), then stores them,
// queues their reviews and moves the cursors in one transaction.
//
// With no cursor yet, only Lookback is read, and a branch other than the main
// branch only contributes what the main branch does not already have; this
// bounds the first round on a repository with a long history.
func (w *pollReposWorker) pollRepo(ctx context.Context, p *PollConfig, meta webhook.Repository) (int, error) {
	ws := ""
	if meta.Workspace != nil {
		ws = meta.Workspace.Slug
	}
	slug := meta.Slug()
	if ws == "" || slug == "" {
		return 0, fmt.Errorf("repository %q has no workspace or slug", meta.FullName)
	}

	branches, err := p.Bitbucket.ListBranches(ctx, ws, slug)
	if err != nil {
		return 0, fmt.Errorf("list branches: %w", err)
	}
	cursors, err := w.syncer.PollCursors(ctx, meta.UUID)
	if err != nil {
		return 0, err
	}

	def := meta.DefaultBranch()
	sort.SliceStable(branches, func(i, j int) bool { // main branch first: it claims shared commits
		if (branches[i].Name == def) != (branches[j].Name == def) {
			return branches[i].Name == def
		}
		return branches[i].Name < branches[j].Name
	})
	defHead := ""
	for _, b := range branches {
		if b.Name == def {
			defHead = b.Head.Hash
		}
	}

	heads := make(map[string]string, len(branches))
	in := store.PushInput{Repo: meta}
	since := time.Now().Add(-p.Lookback)
	for _, b := range branches {
		if b.Head.Hash == "" {
			continue
		}
		heads[b.Name] = b.Head.Hash
		last, seenBefore := cursors[b.Name]
		if seenBefore && last == b.Head.Hash {
			continue // nothing new; no commits request needed
		}

		exclude, from := "", since
		switch {
		case seenBefore:
			exclude, from = last, time.Time{}
		case b.Name != def && defHead != "":
			exclude = defHead
		}
		commits, err := p.Bitbucket.ListRecentCommits(ctx, ws, slug, b.Head.Hash, exclude, from, ingest.MaxCommitsPerChange)
		if err != nil {
			return 0, fmt.Errorf("commits of %s: %w", b.Name, err)
		}
		if len(commits) > 0 {
			in.Branches = append(in.Branches, store.BranchCommits{Branch: b.Name, Commits: commits})
		}
	}

	if len(in.Branches) == 0 && sameHeads(cursors, heads) {
		return 0, nil
	}

	tx, err := w.d.Pool.Begin(ctx)
	if err != nil {
		return 0, wrap("begin", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	res, err := w.syncer.SyncPushTx(ctx, tx, in)
	if err != nil {
		return 0, wrap("sync", err)
	}
	queued, skipped, err := settleNewCommits(ctx, tx, res)
	if err != nil {
		return 0, err
	}
	if err := store.SavePollCursorsTx(ctx, tx, res.RepoID, heads); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, wrap("commit", err)
	}
	if n := len(res.NewCommits); n > 0 {
		w.d.Log.Info("poll found new commits", "repo", meta.FullName, "new_commits", n, "queued", queued, "skipped", skipped)
	}
	return len(res.NewCommits), nil
}

func sameHeads(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
