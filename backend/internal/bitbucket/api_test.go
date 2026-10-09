package bitbucket

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetDiffPath(t *testing.T) {
	var path string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.Write([]byte("diff --git a/x b/x\n"))
	}), nil)

	out, err := c.GetDiff(context.Background(), "my-ws", "loan-service", "a1b2c3")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/2.0/repositories/my-ws/loan-service/diff/a1b2c3" {
		t.Fatalf("path = %q", path)
	}
	if !strings.HasPrefix(out, "diff --git") {
		t.Fatalf("out = %q", out)
	}
}

func TestGetFileEscapesEachSegment(t *testing.T) {
	var path string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.Write([]byte("package x"))
	}), nil)

	if _, err := c.GetFile(context.Background(), "ws", "repo", "abc", "src/my dir/file#1?.go"); err != nil {
		t.Fatal(err)
	}
	want := "/2.0/repositories/ws/repo/src/abc/src/my%20dir/file%231%3F.go"
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestGetFileRejectsTraversalAndEmpty(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1) }), nil)

	for _, p := range []string{"../secret", "a/../b", "a//b", ".", ""} {
		if _, err := c.GetFile(context.Background(), "ws", "repo", "abc", p); err == nil {
			t.Errorf("path %q must be rejected", p)
		}
	}
	if n.Load() != 0 {
		t.Fatalf("%d requests reached the server", n.Load())
	}
}

func TestMissingArgumentsFailBeforeAnyRequest(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1) }), nil)
	ctx := context.Background()

	if _, err := c.GetDiff(ctx, "", "repo", "abc"); err == nil {
		t.Error("empty workspace")
	}
	if _, err := c.ListCommits(ctx, "ws", "", "", ""); err == nil {
		t.Error("empty repo")
	}
	if _, err := c.ListWorkspacePermissions(ctx, " "); err == nil {
		t.Error("blank workspace")
	}
	if n.Load() != 0 {
		t.Fatalf("%d requests reached the server", n.Load())
	}
}

func TestListCommitsFollowsPagesAndSendsRange(t *testing.T) {
	var srv *httptest.Server
	var queries []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		switch r.URL.Query().Get("page") {
		case "":
			fmt.Fprintf(w, `{"values":[{"hash":"c3"},{"hash":"c2"}],"next":"%s/2.0/repositories/ws/repo/commits?page=2"}`, srv.URL)
		case "2":
			fmt.Fprintf(w, `{"values":[{"hash":"c1","parents":[{"hash":"p1"},{"hash":"p2"}]}],"next":"%s/2.0/repositories/ws/repo/commits?page=3"}`, srv.URL)
		default:
			fmt.Fprint(w, `{"values":[{"hash":"c0"}]}`)
		}
	}))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL + "/2.0", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.ListCommits(context.Background(), "ws", "repo", "newhash", "oldhash")
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, cm := range got {
		hashes = append(hashes, cm.Hash)
	}
	if strings.Join(hashes, ",") != "c3,c2,c1,c0" {
		t.Fatalf("hashes = %v", hashes)
	}
	if !got[2].IsMerge() || got[0].IsMerge() {
		t.Error("merge detection through the API type is wrong")
	}
	first := queries[0]
	if !strings.Contains(first, "include=newhash") || !strings.Contains(first, "exclude=oldhash") || !strings.Contains(first, "pagelen=100") {
		t.Fatalf("first query = %q", first)
	}
}

func TestPaginationNeverLeavesTheAPIHost(t *testing.T) {
	var evilHit atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilHit.Add(1)
		w.Write([]byte(`{"values":[]}`))
	}))
	defer evil.Close()

	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"values":[{"hash":"c1"}],"next":"%s/steal"}`, evil.URL)
	}), nil)

	_, err := c.ListCommits(context.Background(), "ws", "repo", "", "")
	if err == nil || !strings.Contains(err.Error(), "refusing to follow") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if evilHit.Load() != 0 {
		t.Fatal("the access token was sent to another host")
	}
}

func TestPaginationHasAPageLimit(t *testing.T) {
	var n atomic.Int32
	var self string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		fmt.Fprintf(w, `{"values":[{"hash":"x"}],"next":"%s"}`, self)
	}))
	defer srv.Close()
	self = srv.URL + "/2.0/repositories/ws/repo/commits"
	c, _ := New(Options{BaseURL: srv.URL + "/2.0", Token: "t"})

	if _, err := c.ListCommits(context.Background(), "ws", "repo", "", ""); err == nil {
		t.Fatal("an endless next chain must fail")
	}
	if n.Load() != maxPages {
		t.Fatalf("fetched %d pages, want %d", n.Load(), maxPages)
	}
}

func TestPermissionsDecode(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/workspaces/ws/permissions"):
			w.Write([]byte(`{"values":[{"permission":"owner","user":{"uuid":"{u1}","account_id":"a1","display_name":"Max","nickname":"max","links":{"avatar":{"href":"https://img/max.png"}}}},{"permission":"member","user":{"uuid":"{u2}","display_name":"Dev"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/repositories/ws/repo/permissions-config/users"):
			w.Write([]byte(`{"values":[{"permission":"write","user":{"uuid":"{u2}","account_id":"a2"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}), nil)
	ctx := context.Background()

	ws, err := c.ListWorkspacePermissions(ctx, "ws")
	if err != nil || len(ws) != 2 {
		t.Fatalf("workspace permissions: %v %v", ws, err)
	}
	if ws[0].Permission != "owner" || ws[0].User.AccountID != "a1" || ws[0].User.Links.Avatar.Href != "https://img/max.png" {
		t.Fatalf("decoded wrong: %+v", ws[0])
	}

	rp, err := c.ListRepoUserPermissions(ctx, "ws", "repo")
	if err != nil || len(rp) != 1 || rp[0].Permission != "write" {
		t.Fatalf("repo permissions: %v %v", rp, err)
	}
}

func TestGetUserEscapesTheUUIDAndDecodes(t *testing.T) {
	var path string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		fmt.Fprint(w, `{"uuid":"{u-1}","account_id":"a1","display_name":"Max","nickname":"max","links":{"avatar":{"href":"https://img/max.png"}}}`)
	}), nil)
	u, err := c.GetUser(context.Background(), "{u-1}")
	if err != nil || u.DisplayName != "Max" || u.Links.Avatar.Href != "https://img/max.png" {
		t.Fatalf("user = %+v, %v", u, err)
	}
	if !strings.HasSuffix(path, "/users/%7Bu-1%7D") {
		t.Errorf("path = %s", path)
	}
	if _, err := c.GetUser(context.Background(), " "); err == nil {
		t.Error("blank id")
	}
}

func TestBadJSONPageIsAnError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>`))
	}), nil)
	if _, err := c.ListCommits(context.Background(), "ws", "repo", "", ""); err == nil {
		t.Fatal("expected a decode error")
	}
}
