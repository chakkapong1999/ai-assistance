// Command mockbitbucket serves canned Bitbucket API responses so the worker
// can be tried without Bitbucket:
//
//	go run ./cmd/mockbitbucket                 # listens on :7990, built-in sample diff
//	go run ./cmd/mockbitbucket -diff my.diff   # serve your own diff for every commit
//
// Point the worker at it with BITBUCKET_BASE_URL=http://localhost:7990.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/chakkapong1999/ai-assistance/backend/internal/mockbitbucket"
)

func main() {
	addr := flag.String("addr", ":7990", "listen address")
	diffFile := flag.String("diff", "", "file with the unified diff to serve (default: built-in sample)")
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
	srv := &http.Server{Addr: *addr, Handler: mockbitbucket.Handler(diff), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
