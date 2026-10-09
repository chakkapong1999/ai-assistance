package store

import (
	"strings"
	"testing"
	"time"
)

func TestHealthMessagesAgreeWithTheCount(t *testing.T) {
	old := int((time.Hour).Seconds())
	for _, tc := range []struct {
		n         int
		want, not string
	}{
		{1, "1 webhook delivery has waited", "deliveries"},
		{2, "2 webhook deliveries have waited", "delivery has"},
	} {
		h := Health{CheckedAt: time.Now(), Webhooks: HealthWebhooks{Unprocessed: tc.n, OldestAgeSeconds: &old}}
		h.problems()
		if got := strings.Join(h.Problems, "|"); !strings.Contains(got, tc.want) || strings.Contains(got, tc.not) {
			t.Errorf("n=%d: %q", tc.n, got)
		}
	}
	for _, tc := range []struct {
		n    int
		want string
	}{
		{1, "1 commit or pull request is marked as waiting but has no job. The worker queues it again"},
		{3, "3 commits or pull requests are marked as waiting but have no job. The worker queues them again"},
	} {
		h := Health{CheckedAt: time.Now(), Stuck: HealthStuck{Commits: tc.n}}
		h.problems()
		if got := strings.Join(h.Problems, "|"); !strings.Contains(got, tc.want) {
			t.Errorf("stuck n=%d: %q", tc.n, got)
		}
	}
	for n, want := range map[int]string{1: "1 job gave up", 4: "4 jobs gave up"} {
		h := Health{CheckedAt: time.Now(), Jobs: HealthJobs{Discarded24h: n}}
		h.problems()
		if got := strings.Join(h.Problems, "|"); !strings.Contains(got, want) {
			t.Errorf("discarded n=%d: %q", n, got)
		}
	}
}
