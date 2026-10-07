package webhook

import (
	"os"
	"testing"
)

func TestShouldProcess(t *testing.T) {
	if !ShouldProcess("repo:push") {
		t.Error("repo:push must be processed")
	}
	for _, k := range []string{"diagnostics:ping", "pullrequest:created", ""} {
		if ShouldProcess(k) {
			t.Errorf("%q must not start a review yet", k)
		}
	}
}

func TestParsePush(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/repo_push.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := ParsePush(raw)
	if err != nil {
		t.Fatalf("ParsePush: %v", err)
	}

	if e.Repository.UUID != "{r-1}" || e.Repository.Project.Key != "LOAN" || e.Repository.Workspace.Slug != "acme" {
		t.Fatalf("repository decoded wrong: %+v", e.Repository)
	}
	if len(e.Push.Changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(e.Push.Changes))
	}

	c := e.Push.Changes[0]
	if c.Branch() != "feature/x" || len(c.Commits) != 2 {
		t.Fatalf("change 0: branch=%q commits=%d", c.Branch(), len(c.Commits))
	}
	if !c.Commits[0].IsMerge() || c.Commits[1].IsMerge() {
		t.Error("merge detection wrong: first commit is a merge, second is not")
	}
	if c.Commits[0].Author.User == nil || c.Commits[0].Author.User.AccountID != "557058:aaaa" {
		t.Error("author user not decoded")
	}
	if c.Commits[1].Author.User != nil || c.Commits[1].Author.Raw != "Somebody <s@example.com>" {
		t.Error("author without a Bitbucket account must keep only the raw string")
	}
	if c.Commits[0].Date.IsZero() {
		t.Error("commit date not decoded")
	}

	del := e.Push.Changes[1]
	if del.New != nil || del.Branch() != "" || !del.Closed {
		t.Errorf("deleted branch decoded wrong: %+v", del)
	}
}

func TestParsePushRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":           `nope`,
		"no repository uuid": `{"repository":{"name":"x"},"push":{"changes":[]}}`,
		"empty object":       `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePush([]byte(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
