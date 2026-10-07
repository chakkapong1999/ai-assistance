package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

func resetSync(t *testing.T) *store.Syncer {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`TRUNCATE workspaces, users, webhook_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	return store.NewSyncer(testPool)
}

func repoOf(project bool) webhook.Repository {
	r := webhook.Repository{
		UUID: "{r-1}", Name: "loan-service", FullName: "acme/loan-service",
		Workspace: &webhook.Workspace{UUID: "{w-1}", Slug: "acme", Name: "Acme"},
	}
	if project {
		r.Project = &webhook.Project{UUID: "{p-1}", Key: "LOAN", Name: "Loan"}
	}
	return r
}

func commit(hash, raw string, acct *webhook.Account, parents int) webhook.Commit {
	c := webhook.Commit{Hash: hash, Message: "msg " + hash, Date: time.Date(2026, 10, 7, 7, 0, 0, 0, time.UTC)}
	c.Author.Raw, c.Author.User = raw, acct
	for i := 0; i < parents; i++ {
		c.Parents = append(c.Parents, struct {
			Hash string `json:"hash"`
		}{Hash: "p"})
	}
	return c
}

var max = &webhook.Account{UUID: "{u-1}", AccountID: "557058:aaaa", DisplayName: "Max", Nickname: "max"}

func in(branch string, cs ...webhook.Commit) store.PushInput {
	return store.PushInput{Repo: repoOf(true), Branches: []store.BranchCommits{{Branch: branch, Commits: cs}}}
}

func TestSyncPushCreatesEverythingOnce(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	push := in("feature/x",
		commit("c2", "Max <Max@Example.com>", max, 2),
		commit("c1", "Somebody <s@example.com>", nil, 1))

	res, err := s.SyncPush(ctx, push)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NewCommits) != 2 || res.ReviewEnabled {
		t.Fatalf("result = %+v; a new repo must start with review disabled", res)
	}
	for table, want := range map[string]int{"workspaces": 1, "projects": 1, "repositories": 1, "commits": 2, "users": 2, "user_emails": 2} {
		if got := count(t, "SELECT count(*) FROM "+table); got != want {
			t.Errorf("%s = %d, want %d", table, got, want)
		}
	}
	if count(t, `SELECT count(*) FROM commits WHERE hash='c2' AND is_merge`) != 1 || count(t, `SELECT count(*) FROM commits WHERE hash='c1' AND NOT is_merge`) != 1 {
		t.Error("is_merge not derived from parents")
	}
	// The account-linked author is mapped to the account user; the other is raw-only.
	if count(t, `SELECT count(*) FROM commits c JOIN users u ON u.id=c.author_user_id WHERE c.hash='c2' AND u.bb_account_id='557058:aaaa' AND u.email='max@example.com'`) != 1 {
		t.Error("account author not mapped (or email not lower-cased)")
	}
	if count(t, `SELECT count(*) FROM commits c JOIN users u ON u.id=c.author_user_id WHERE c.hash='c1' AND u.bb_account_id IS NULL AND u.bb_uuid IS NULL AND u.display_name='Somebody'`) != 1 {
		t.Error("raw-only author not created")
	}

	// Replaying the same push (a River retry) changes nothing.
	again, err := s.SyncPush(ctx, push)
	if err != nil || len(again.NewCommits) != 0 {
		t.Fatalf("replay: %v %+v", err, again)
	}
	for table, want := range map[string]int{"commits": 2, "users": 2, "user_emails": 2} {
		if got := count(t, "SELECT count(*) FROM "+table); got != want {
			t.Errorf("after replay %s = %d, want %d", table, got, want)
		}
	}
}

func TestSyncPushKeepsAdminChoiceAndFirstBranch(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	if _, err := s.SyncPush(ctx, in("a", commit("c1", "M <m@x.com>", max, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE repositories SET review_enabled = true`); err != nil {
		t.Fatal(err)
	}
	// Same commit again on another branch, plus a renamed repo.
	p := in("b", commit("c1", "M <m@x.com>", max, 1), commit("c2", "M <m@x.com>", max, 1))
	p.Repo.Name = "renamed"
	res, err := s.SyncPush(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReviewEnabled || len(res.NewCommits) != 1 || res.NewCommits[0].Hash != "c2" {
		t.Fatalf("result = %+v; review_enabled must survive and only c2 is new", res)
	}
	if count(t, `SELECT count(*) FROM commits WHERE hash='c1' AND branch='a'`) != 1 {
		t.Error("commit's first branch was overwritten")
	}
	if count(t, `SELECT count(*) FROM repositories WHERE name='renamed' AND review_enabled`) != 1 {
		t.Error("repo not updated or review_enabled reset")
	}
}

func TestRawOnlyUserIsAdoptedWhenAccountAppears(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	if _, err := s.SyncPush(ctx, in("a", commit("c1", "Max Tussakorn <max@example.com>", nil, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncPush(ctx, in("a", commit("c2", "Max <max@example.com>", max, 1))); err != nil {
		t.Fatal(err)
	}
	if n := count(t, `SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("users = %d, want 1 (raw-only row adopted, not duplicated)", n)
	}
	if count(t, `SELECT count(DISTINCT author_user_id) FROM commits`) != 1 ||
		count(t, `SELECT count(*) FROM users WHERE bb_account_id='557058:aaaa' AND bb_uuid='{u-1}' AND display_name='Max'`) != 1 {
		t.Fatal("both commits should belong to the one user, now carrying the Bitbucket ids")
	}
}

func TestSameEmailKnownToAnotherUserDoesNotStealIt(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	other := &webhook.Account{UUID: "{u-2}", AccountID: "557058:bbbb", DisplayName: "Other"}
	if _, err := s.SyncPush(ctx, in("a", commit("c1", "Max <max@example.com>", max, 1))); err != nil {
		t.Fatal(err)
	}
	// A different account commits with Max's email address.
	if _, err := s.SyncPush(ctx, in("a", commit("c2", "Max <max@example.com>", other, 1))); err != nil {
		t.Fatal(err)
	}
	if count(t, `SELECT count(*) FROM user_emails WHERE lower(email)='max@example.com'`) != 1 ||
		count(t, `SELECT count(*) FROM user_emails e JOIN users u ON u.id=e.user_id WHERE u.bb_account_id='557058:aaaa'`) != 1 {
		t.Fatal("email must stay with its first owner")
	}
	if count(t, `SELECT count(*) FROM commits c JOIN users u ON u.id=c.author_user_id WHERE c.hash='c2' AND u.bb_account_id='557058:bbbb'`) != 1 {
		t.Fatal("commit must still be attributed to the account Bitbucket named")
	}
}

func TestSyncPushEdgeCases(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()

	// No project in the payload, no author info, NUL byte and invalid UTF-8 in the message.
	p := store.PushInput{Repo: repoOf(false), Branches: []store.BranchCommits{{Branch: "a", Commits: []webhook.Commit{
		{Hash: "e1", Message: "bad\x00byte \xff end " + strings.Repeat("ก", 30000), Date: time.Now()},
	}}}}
	res, err := s.SyncPush(ctx, p)
	if err != nil || len(res.NewCommits) != 1 {
		t.Fatalf("edge push: %v %+v", err, res)
	}
	if count(t, `SELECT count(*) FROM projects WHERE key='NONE'`) != 1 {
		t.Error("placeholder project not created")
	}
	if count(t, `SELECT count(*) FROM commits WHERE author_user_id IS NULL AND author_raw IS NULL AND octet_length(message) <= 65536`) != 1 {
		t.Error("commit without author info mishandled or message not bounded")
	}

	// Missing workspace is a hard error (a bug upstream), not a silent skip.
	bad := in("a", commit("e2", "x <x@x.com>", nil, 1))
	bad.Repo.Workspace = nil
	if _, err := s.SyncPush(ctx, bad); err == nil {
		t.Fatal("expected an error for a repository without workspace")
	}
}

func TestSyncPushIsAtomic(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	// A commit whose date is outside Postgres' range fails the insert halfway through.
	badDate := commit("bad", "x <x@x.com>", nil, 1)
	badDate.Date = time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.SyncPush(ctx, in("a", commit("ok", "x <x@x.com>", nil, 1), badDate)); err == nil {
		t.Fatal("expected the out-of-range date to fail")
	}
	for _, table := range []string{"workspaces", "repositories", "commits", "users"} {
		if n := count(t, "SELECT count(*) FROM "+table); n != 0 {
			t.Errorf("%s has %d rows after a failed sync; the transaction must roll back", table, n)
		}
	}
}

// Pushes to the same workspace serialise on its row, so the race that matters
// is one person appearing for the first time in several workspaces at once.
func TestConcurrentSyncOfTheSameNewUserAcrossWorkspaces(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	const n = 12
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			p := in("a", commit(fmt.Sprintf("c-%d", i), "Max <max@example.com>", max, 1))
			p.Repo.UUID = fmt.Sprintf("{r-%d}", i)
			p.Repo.FullName = fmt.Sprintf("ws%d/svc", i)
			p.Repo.Workspace = &webhook.Workspace{UUID: fmt.Sprintf("{w-%d}", i), Slug: fmt.Sprintf("ws%d", i), Name: "W"}
			p.Repo.Project = &webhook.Project{UUID: fmt.Sprintf("{p-%d}", i), Key: "K", Name: "P"}
			<-start
			_, err := s.SyncPush(ctx, p)
			errs <- err
		}(i)
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if u, c := count(t, `SELECT count(*) FROM users`), count(t, `SELECT count(*) FROM commits`); u != 1 || c != n {
		t.Fatalf("users=%d commits=%d, want 1 and %d", u, c, n)
	}
}

func TestConcurrentPushesToOneRepo(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			_, err := s.SyncPush(ctx, in("a", commit(fmt.Sprintf("c-%d", i), "Max <max@example.com>", max, 1)))
			errs <- err
		}(i)
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if count(t, `SELECT count(*) FROM users`) != 1 || count(t, `SELECT count(*) FROM commits`) != 8 {
		t.Fatal("concurrent pushes to one repo lost or duplicated rows")
	}
}

func TestConcurrentRawOnlyAuthorGetsOneUser(t *testing.T) {
	s := resetSync(t)
	ctx := context.Background()
	const n = 12
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			p := in("a", commit(fmt.Sprintf("r-%d", i), "Ghost <ghost@example.com>", nil, 1))
			p.Repo.UUID = fmt.Sprintf("{r-%d}", i)
			p.Repo.FullName = fmt.Sprintf("ws%d/svc", i)
			p.Repo.Workspace = &webhook.Workspace{UUID: fmt.Sprintf("{w-%d}", i), Slug: fmt.Sprintf("ws%d", i), Name: "W"}
			p.Repo.Project = &webhook.Project{UUID: fmt.Sprintf("{p-%d}", i), Key: "K", Name: "P"}
			<-start
			_, err := s.SyncPush(ctx, p)
			errs <- err
		}(i)
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	if u := count(t, `SELECT count(*) FROM users`); u != 1 {
		t.Fatalf("users = %d, want 1 (no orphans)", u)
	}
	if c := count(t, `SELECT count(*) FROM commits WHERE author_user_id IS NOT NULL`); c != n {
		t.Fatalf("commits attributed = %d, want %d", c, n)
	}
}

func TestNewRepositoryStartsWithReviewAsConfigured(t *testing.T) {
	ctx := context.Background()
	s := resetSync(t)

	// Default: off, as before.
	res, err := s.SyncPush(ctx, in("a", commit("c1", "M <m@x.com>", max, 1)))
	if err != nil || res.ReviewEnabled {
		t.Fatalf("default: enabled=%v err=%v; a new repository must start with review off unless asked", res.ReviewEnabled, err)
	}

	// On: a repository created from now on starts enabled.
	resetSync(t)
	on := store.NewSyncer(testPool).ReviewNewRepos(true)
	res, err = on.SyncPush(ctx, in("a", commit("c1", "M <m@x.com>", max, 1)))
	if err != nil || !res.ReviewEnabled {
		t.Fatalf("on: enabled=%v err=%v", res.ReviewEnabled, err)
	}

	// It is only a starting value: an admin who switched the repository off keeps it off.
	if _, err := testPool.Exec(ctx, `UPDATE repositories SET review_enabled = false`); err != nil {
		t.Fatal(err)
	}
	res, err = on.SyncPush(ctx, in("a", commit("c2", "M <m@x.com>", max, 1)))
	if err != nil || res.ReviewEnabled {
		t.Fatalf("a later sync turned review back on: enabled=%v err=%v", res.ReviewEnabled, err)
	}
}
