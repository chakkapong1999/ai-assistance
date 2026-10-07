package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeLister struct {
	calls   [][3]string // include, exclude, repo
	commits []webhook.Commit
	err     error
}

func (f *fakeLister) ListCommits(_ context.Context, ws, repo, include, exclude string) ([]webhook.Commit, error) {
	f.calls = append(f.calls, [3]string{include, exclude, ws + "/" + repo})
	return f.commits, f.err
}

func commitsN(n int) []webhook.Commit {
	out := make([]webhook.Commit, n)
	for i := range out {
		out[i].Hash = fmt.Sprintf("h%04d", i) // index 0 = newest
	}
	return out
}

func event(changes string) webhook.PushEvent {
	raw := `{"repository":{"uuid":"{r}","name":"svc","full_name":"acme/svc","mainbranch":{"name":"main"},
	  "workspace":{"uuid":"{w}","slug":"acme","name":"Acme"}},"push":{"changes":[` + changes + `]}}`
	ev, err := webhook.ParsePush([]byte(raw))
	if err != nil {
		panic(err)
	}
	return ev
}

func ref(name, hash string) string {
	return `{"type":"branch","name":"` + name + `","target":{"hash":"` + hash + `"}}`
}

func TestPlanUsesPayloadCommitsWhenNotTruncated(t *testing.T) {
	ev := event(`{"truncated":false,"old":` + ref("f", "o1") + `,"new":` + ref("f", "n1") + `,"commits":[{"hash":"n1"},{"hash":"x2"}]}`)
	fl := &fakeLister{}
	in, err := Plan(context.Background(), quiet, fl, ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.calls) != 0 || len(in.Branches) != 1 || len(in.Branches[0].Commits) != 2 || in.Branches[0].Branch != "f" {
		t.Fatalf("calls=%v in=%+v", fl.calls, in)
	}
}

func TestPlanCompletesTruncatedPush(t *testing.T) {
	cases := map[string]struct{ change, wantInclude, wantExclude string }{
		"update":             {`{"truncated":true,"old":` + ref("f", "OLD") + `,"new":` + ref("f", "NEW") + `,"commits":[{"hash":"NEW"}]}`, "NEW", "OLD"},
		"new feature branch": {`{"truncated":true,"created":true,"old":null,"new":` + ref("feat", "NEW") + `,"commits":[{"hash":"NEW"}]}`, "NEW", "main"},
		"first push of main": {`{"truncated":true,"created":true,"old":null,"new":` + ref("main", "NEW") + `,"commits":[{"hash":"NEW"}]}`, "NEW", ""},
	}
	for name, c := range cases {
		fl := &fakeLister{commits: commitsN(7)}
		in, err := Plan(context.Background(), quiet, fl, event(c.change))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(fl.calls) != 1 || fl.calls[0] != [3]string{c.wantInclude, c.wantExclude, "acme/svc"} {
			t.Errorf("%s: ListCommits called with %v, want include=%q exclude=%q", name, fl.calls, c.wantInclude, c.wantExclude)
		}
		if len(in.Branches[0].Commits) != 7 {
			t.Errorf("%s: commits = %d, want the 7 from the API", name, len(in.Branches[0].Commits))
		}
	}
}

func TestPlanIgnoresTagsAndDeletedBranches(t *testing.T) {
	ev := event(`{"old":` + ref("gone", "o") + `,"new":null,"closed":true,"commits":[]},` +
		`{"new":{"type":"tag","name":"v1","target":{"hash":"t"}},"commits":[{"hash":"t"}]}`)
	fl := &fakeLister{}
	in, err := Plan(context.Background(), quiet, fl, ev)
	if err != nil || len(in.Branches) != 0 || len(fl.calls) != 0 {
		t.Fatalf("err=%v in=%+v calls=%v", err, in, fl.calls)
	}
}

func TestPlanCapsHugePushKeepingNewest(t *testing.T) {
	ev := event(`{"truncated":true,"created":true,"old":null,"new":` + ref("main", "NEW") + `,"commits":[]}`)
	in, err := Plan(context.Background(), quiet, &fakeLister{commits: commitsN(MaxCommitsPerChange + 50)}, ev)
	if err != nil {
		t.Fatal(err)
	}
	got := in.Branches[0].Commits
	if len(got) != MaxCommitsPerChange || got[0].Hash != "h0000" {
		t.Fatalf("kept %d commits starting at %s", len(got), got[0].Hash)
	}
}

func TestPlanPropagatesAPIErrorSoTheJobRetries(t *testing.T) {
	boom := errors.New("bitbucket down")
	ev := event(`{"truncated":true,"old":` + ref("f", "o") + `,"new":` + ref("f", "n") + `,"commits":[]}`)
	if _, err := Plan(context.Background(), quiet, &fakeLister{err: boom}, ev); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the API error", err)
	}
}
