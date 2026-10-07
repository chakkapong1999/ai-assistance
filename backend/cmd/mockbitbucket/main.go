// Command mockbitbucket serves canned Bitbucket API responses so the worker
// can be tried without Bitbucket:
//
//	go run ./cmd/mockbitbucket                 # listens on :7990, built-in sample diff
//	go run ./cmd/mockbitbucket -diff my.diff   # serve your own diff for every commit
//	go run ./cmd/mockbitbucket -repo acme/demo # also serve a repository with a few commits (to try polling)
//
// With -repo, add commits while it runs (they show up at the next poll):
//
//	curl -X POST 'localhost:7990/_mock/commit?repo=acme/demo&branch=main&message=fix+rounding'
//
// Point the worker at it with BITBUCKET_BASE_URL=http://localhost:7990.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/mockbitbucket"
)

func main() {
	addr := flag.String("addr", ":7990", "listen address")
	diffFile := flag.String("diff", "", "file with the unified diff to serve (default: built-in sample)")
	var repos repoFlags
	flag.Var(&repos, "repo", "workspace/slug of a repository to serve with sample commits (repeatable)")
	flag.Parse()

	diff := ""
	if *diffFile != "" {
		b, err := os.ReadFile(*diffFile)
		if err != nil {
			log.Fatal(err)
		}
		diff = string(b)
	}
	fmt.Printf("mock Bitbucket listening on %s (set BITBUCKET_BASE_URL=http://localhost%s)\n", *addr, *addr)
	m := mockbitbucket.New(diff)
	for _, r := range repos {
		ws, slug, ok := strings.Cut(r, "/")
		if !ok || ws == "" || slug == "" {
			log.Fatalf("-repo %q must look like workspace/slug", r)
		}
		m.AddCommit(ws, slug, "main", "initial commit")
		m.AddCommit(ws, slug, "main", "add handler")
		m.AddCommit(ws, slug, "feature/x", "work in progress")
		fmt.Printf("serving repository %s with branches main and feature/x\n", r)
	}
	srv := &http.Server{Addr: *addr, Handler: m.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type repoFlags []string

func (r *repoFlags) String() string     { return strings.Join(*r, ",") }
func (r *repoFlags) Set(v string) error { *r = append(*r, v); return nil }
