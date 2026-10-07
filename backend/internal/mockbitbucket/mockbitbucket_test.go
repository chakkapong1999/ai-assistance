package mockbitbucket

import (
	"context"
	"net/http/httptest"
	"testing"

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
