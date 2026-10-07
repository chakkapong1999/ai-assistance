// Package ingest decides which commits a stored repo:push webhook brought.
// The worker feeds the plan to store.Syncer.SyncPushTx.
package ingest

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

// MaxCommitsPerChange bounds how many commits one branch update can add. It
// protects against a first push of a huge repository flooding the review
// queue; the newest commits are kept.
const MaxCommitsPerChange = 500

// CommitLister is the part of the Bitbucket client Plan needs.
type CommitLister interface {
	ListCommits(ctx context.Context, workspace, repo, include, exclude string) ([]webhook.Commit, error)
}

// Plan decides which commits a push brought. Payloads list at most a handful
// of commits and set Truncated when there are more; those are completed from
// the API. Tags and deleted branches carry no code to review and are skipped.
func Plan(ctx context.Context, log *slog.Logger, bb CommitLister, ev webhook.PushEvent) (store.PushInput, error) {
	in := store.PushInput{Repo: ev.Repository}
	ws := ""
	if ev.Repository.Workspace != nil {
		ws = ev.Repository.Workspace.Slug
	}
	slug := ev.Repository.Slug()

	for _, ch := range ev.Push.Changes {
		branch := ch.Branch()
		if branch == "" || ch.New == nil || ch.New.Target.Hash == "" {
			continue
		}
		commits := ch.Commits
		if ch.Truncated {
			exclude := ""
			switch {
			case ch.Old != nil:
				exclude = ch.Old.Target.Hash
			case branch != ev.Repository.DefaultBranch():
				// A new branch: only what is not already on the main branch.
				exclude = ev.Repository.DefaultBranch()
			}
			if ws == "" || slug == "" {
				return store.PushInput{}, fmt.Errorf("ingest: cannot complete truncated push without workspace and repo slug")
			}
			all, err := bb.ListCommits(ctx, ws, slug, ch.New.Target.Hash, exclude)
			if err != nil {
				return store.PushInput{}, fmt.Errorf("complete truncated push on %s: %w", branch, err)
			}
			commits = all
		}
		if len(commits) > MaxCommitsPerChange {
			log.Warn("push has more commits than the per-push cap; keeping the newest",
				"branch", branch, "commits", len(commits), "cap", MaxCommitsPerChange)
			commits = commits[:MaxCommitsPerChange] // Bitbucket lists newest first
		}
		in.Branches = append(in.Branches, store.BranchCommits{Branch: branch, Commits: commits})
	}
	return in, nil
}
