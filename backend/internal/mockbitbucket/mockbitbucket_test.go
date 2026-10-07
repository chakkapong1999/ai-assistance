package mockbitbucket

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/review"
)

// The mock must be consumable by the real client and produce a diff the
// review pipeline can parse and review.
func TestClientAndPipelineWorkAgainstTheMock(t *testing.T) {
	srv := httptest.NewServer(Handler(""))
	defer srv.Close()

	c, err := bitbucket.New(bitbucket.Options{Token: "dev", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := c.GetDiff(context.Background(), "acme", "svc", "abc")
	if err != nil {
		t.Fatal(err)
	}
	out, err := review.Run(context.Background(), review.Mock{Scenario: "findings"}, review.Input{Diff: diff}, review.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Reviewable || len(out.Findings) != 2 || out.Dropped != 0 {
		t.Fatalf("outcome: %+v", out)
	}
	if cs, err := c.ListCommits(context.Background(), "acme", "svc", "abc", ""); err != nil || len(cs) != 0 {
		t.Fatalf("commits: %v %v", cs, err)
	}
}

func newClient(t *testing.T, m *Mock) *bitbucket.Client {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	c, err := bitbucket.New(bitbucket.Options{Token: "dev", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRepositoryAndBranchEndpoints(t *testing.T) {
	m := New("")
	m.AddCommit("acme", "api", "main", "one")
	m.AddCommit("acme", "api", "feature/x", "two")
	m.AddRepo("acme", "web")
	c := newClient(t, m)
	ctx := context.Background()

	r, err := c.GetRepository(ctx, "acme", "api")
	if err != nil || r.UUID == "" || r.FullName != "acme/api" || r.Slug() != "api" || r.DefaultBranch() != "main" ||
		r.Workspace == nil || r.Workspace.Slug != "acme" || r.Project == nil || r.Project.UUID == "" {
		t.Fatalf("repository: %+v %v", r, err)
	}
	if _, err := c.GetRepository(ctx, "acme", "nope"); !errors.Is(err, bitbucket.ErrNotFound) {
		t.Fatalf("unknown repo: %v, want ErrNotFound", err)
	}
	rs, err := c.ListWorkspaceRepositories(ctx, "acme")
	if err != nil || len(rs) != 2 || rs[0].Slug() != "api" || rs[1].Slug() != "web" {
		t.Fatalf("workspace repos: %+v %v", rs, err)
	}
	bs, err := c.ListBranches(ctx, "acme", "api")
	if err != nil || len(bs) != 2 || bs[0].Name != "feature/x" || bs[1].Name != "main" || bs[0].Head.Hash == "" {
		t.Fatalf("branches: %+v %v", bs, err)
	}
}

func TestListRecentCommits(t *testing.T) {
	m := New("")
	now := time.Now()
	var hashes []string
	for i := 0; i < 250; i++ { // more than two pages of 100
		hashes = append(hashes, m.AddCommitAt("acme", "api", "main", "c", now.Add(-time.Duration(250-i)*time.Hour)))
	}
	feature := m.AddCommit("acme", "api", "feature/x", "only on feature")
	c := newClient(t, m)
	ctx := context.Background()

	all, err := c.ListCommits(ctx, "acme", "api", "main", "")
	if err != nil || len(all) != 250 || all[0].Hash != hashes[249] {
		t.Fatalf("all: %d %v", len(all), err)
	}
	capped, err := c.ListRecentCommits(ctx, "acme", "api", "main", "", time.Time{}, 120)
	if err != nil || len(capped) != 120 || capped[0].Hash != hashes[249] {
		t.Fatalf("capped: %d %v", len(capped), err)
	}
	// Dated cut-off: commit i is (250-i) hours old, so "since 100.5h ago" keeps i >= 150.
	recent, err := c.ListRecentCommits(ctx, "acme", "api", "main", "", now.Add(-100*time.Hour-30*time.Minute), 0)
	if err != nil || len(recent) != 100 {
		t.Fatalf("since: %d %v", len(recent), err)
	}
	// Reading stops early: the third page is never requested for 120 commits... but is for 250.
	before := m.Calls("commits")
	if _, err := c.ListRecentCommits(ctx, "acme", "api", "main", "", time.Time{}, 50); err != nil {
		t.Fatal(err)
	}
	if got := m.Calls("commits") - before; got != 1 {
		t.Fatalf("a 50-commit read made %d requests, want 1", got)
	}
	// exclude: what a branch has that main does not.
	only, err := c.ListCommits(ctx, "acme", "api", "feature/x", "main")
	if err != nil || len(only) != 1 || only[0].Hash != feature {
		t.Fatalf("branch-only commits: %+v %v", only, err)
	}
	// Parents are carried (merge detection depends on them).
	if len(all[0].Parents) != 1 || all[249].Parents != nil && len(all[249].Parents) != 0 {
		t.Fatalf("parents: %+v / %+v", all[0].Parents, all[249].Parents)
	}
}
