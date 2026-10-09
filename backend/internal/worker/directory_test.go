package worker

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/chakkapong1999/ai-assistance/backend/internal/bitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/jobs"
	"github.com/chakkapong1999/ai-assistance/backend/internal/mockbitbucket"
	"github.com/chakkapong1999/ai-assistance/backend/internal/store"
)

// directory starts a worker whose timer is an hour (so the only pass is the
// one at start) and returns a function that runs one more pass and waits for it.
func directory(t *testing.T, m *mockbitbucket.Mock) func() {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	bb, err := bitbucket.New(bitbucket.Options{Token: "t", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	// Repositories come from Bitbucket, as the poller would store them.
	for _, slug := range []string{"web", "api"} {
		meta, err := bb.GetRepository(context.Background(), "acme", slug)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.NewSyncer(testPool).SyncPush(context.Background(), store.PushInput{Repo: meta}); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := NewClient(Deps{
		Pool: testPool, Log: quiet, Bitbucket: bb, Reviewer: blockedReviewer, PollInterval: 100 * time.Millisecond,
		Directory: &DirectoryConfig{Interval: time.Hour, Bitbucket: bb},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := rc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		stop, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		rc.Stop(stop) //nolint:errcheck
	})
	done := func() int {
		return count(t, `SELECT count(*) FROM river_job WHERE kind = 'sync_directory' AND state = 'completed'`)
	}
	waitFor(t, "first directory pass", func() bool { return done() >= 1 })
	return func() {
		t.Helper()
		before := done()
		if _, err := rc.Insert(ctx, jobs.SyncDirectoryArgs{}, &river.InsertOpts{}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "directory pass", func() bool { return done() > before })
	}
}

func newDirectoryMock() *mockbitbucket.Mock {
	m := mockbitbucket.New("")
	m.AddRepo("acme", "web")
	m.AddRepo("acme", "api")
	m.AddAccount("{u1}", "acc1", "Una One", "una", "https://img/u1.png")
	m.AddAccount("{u2}", "acc2", "Ben Two", "ben", "https://img/u2.png")
	m.AddAccount("{u3}", "acc3", "Cy Three", "cy", "https://img/u3.png")
	return m
}

func TestDirectorySyncKeepsRolesAndPermissionsInStep(t *testing.T) {
	reset(t)
	m := newDirectoryMock()
	m.SetWorkspaceRole("acme", "{u1}", "owner")
	m.SetWorkspaceRole("acme", "{u2}", "member")
	m.SetRepoPermission("acme", "web", "{u1}", "admin")
	m.SetRepoPermission("acme", "web", "{u2}", "read")
	m.DenyPermissions("acme/api") // a token that is no admin of this repository
	pass := directory(t, m)

	role := func(who string) string {
		return str(t, `SELECT (SELECT role FROM workspace_members wm WHERE wm.user_id = u.id) FROM users u WHERE bb_account_id = $1`, who)
	}
	perm := func(who, repo string) string {
		return str(t, `SELECT (SELECT permission FROM repo_permissions rp JOIN repositories r ON r.id = rp.repo_id WHERE rp.user_id = u.id AND r.slug = $2) FROM users u WHERE bb_account_id = $1`, who, repo)
	}
	if got := role("acc1") + "," + role("acc2"); got != "owner,member" {
		t.Fatalf("workspace roles = %s", got)
	}
	if got := perm("acc1", "web") + "," + perm("acc2", "web"); got != "admin,read" {
		t.Fatalf("web permissions = %s", got)
	}
	if n := count(t, `SELECT count(*) FROM repo_permissions rp JOIN repositories r ON r.id = rp.repo_id WHERE r.slug = 'api'`); n != 0 {
		t.Fatalf("a repository that could not be read got %d permissions", n)
	}
	if got := str(t, `SELECT display_name || '|' || avatar_url || '|' || (synced_at IS NOT NULL)::text FROM users WHERE bb_account_id = 'acc1'`); got != "Una One|https://img/u1.png|true" {
		t.Fatalf("profile = %q", got)
	}

	// Ben leaves, Cy joins, Una is renamed and gets write instead of admin.
	m.SetWorkspaceRole("acme", "{u2}", "")
	m.SetWorkspaceRole("acme", "{u3}", "collaborator")
	m.SetRepoPermission("acme", "web", "{u2}", "")
	m.SetRepoPermission("acme", "web", "{u3}", "write")
	m.SetRepoPermission("acme", "web", "{u1}", "write")
	m.AddAccount("{u1}", "acc1", "Una Renamed", "una", "https://img/u1b.png")
	pass()
	if got := count(t, `SELECT count(*) FROM workspace_members`); got != 2 {
		t.Fatalf("workspace_members = %d, want 2 (Ben removed, Cy added)", got)
	}
	if got := role("acc2"); got != "<null>" {
		t.Errorf("Ben still has role %s", got)
	}
	if got := role("acc3") + "," + perm("acc3", "web") + "," + perm("acc1", "web"); got != "collaborator,write,write" {
		t.Errorf("after the change: %s", got)
	}
	if got := str(t, `SELECT display_name || '|' || avatar_url FROM users WHERE bb_account_id = 'acc1'`); got != "Una Renamed|https://img/u1b.png" {
		t.Errorf("rename not picked up: %q", got)
	}
	if n := count(t, `SELECT count(*) FROM users WHERE bb_account_id = 'acc2'`); n != 1 {
		t.Errorf("a person who left was deleted (%d rows); history must keep their name", n)
	}

	// An empty answer is more likely a gap than everyone leaving: nothing is removed.
	m.SetWorkspaceRole("acme", "{u1}", "")
	m.SetWorkspaceRole("acme", "{u3}", "")
	pass()
	if got := count(t, `SELECT count(*) FROM workspace_members`); got != 2 {
		t.Errorf("an empty list wiped workspace_members (%d left)", got)
	}
}

func TestDirectorySyncRefreshesOnlyStaleProfiles(t *testing.T) {
	reset(t)
	m := newDirectoryMock()
	pass := directory(t, m)
	ctx := context.Background()

	add := func(uuid, account, name, synced string) int64 {
		var id int64
		if err := testPool.QueryRow(ctx, `
			INSERT INTO users (bb_uuid, bb_account_id, display_name, synced_at)
			VALUES ($1, $2, $3, CASE WHEN $4 = '' THEN NULL ELSE now() - $4::interval END) RETURNING id`, uuid, account, name, synced).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stale := add("{u1}", "acc1", "old name", "3 days")
	never := add("{u2}", "acc2", "never synced", "")
	fresh := add("{u3}", "acc3", "fresh name", "1 hour")
	gone := add("{gone}", "accgone", "Left The Company", "3 days")
	if _, err := testPool.Exec(ctx, `INSERT INTO users (display_name) VALUES ('email only')`); err != nil {
		t.Fatal(err)
	}

	before := m.Calls("user")
	pass()
	if got := m.Calls("user") - before; got != 3 {
		t.Errorf("%d profile lookups, want 3 (stale, never synced, gone; not the fresh one or the email-only user)", got)
	}
	name := func(id int64) string { return str(t, `SELECT display_name FROM users WHERE id = $1`, id) }
	if got := name(stale) + "|" + name(never) + "|" + name(fresh) + "|" + name(gone); got != "Una One|Ben Two|fresh name|Left The Company" {
		t.Errorf("names = %s", got)
	}
	if got := str(t, `SELECT avatar_url FROM users WHERE id = $1`, stale); got != "https://img/u1.png" {
		t.Errorf("avatar = %s", got)
	}
	// The account that no longer exists is not asked about again until tomorrow.
	if n := count(t, `SELECT count(*) FROM users WHERE id = $1 AND synced_at > now() - interval '1 minute'`, gone); n != 1 {
		t.Errorf("a missing account was not marked as checked")
	}
	before = m.Calls("user")
	pass()
	if got := m.Calls("user") - before; got != 0 {
		t.Errorf("second pass asked about %d profiles again", got)
	}
}
