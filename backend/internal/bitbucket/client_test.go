package bitbucket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sleeper records the waits instead of sleeping.
type sleeper struct {
	waits []time.Duration
	err   error
}

func (s *sleeper) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return s.err
}

func newTestClient(t *testing.T, h http.Handler, mod func(*Options)) (*Client, *sleeper) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sl := &sleeper{}
	o := Options{BaseURL: srv.URL + "/2.0", Token: "tok", BaseDelay: 100 * time.Millisecond, Sleep: sl.sleep}
	if mod != nil {
		mod(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c, sl
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("missing token must fail")
	}
	if _, err := New(Options{Token: "t", BaseURL: "not a url"}); err == nil {
		t.Error("bad base URL must fail")
	}
	if _, err := New(Options{Token: "t"}); err != nil {
		t.Errorf("defaults should work: %v", err)
	}
}

func TestSendsBearerToken(t *testing.T) {
	var got string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte("diff"))
	}), nil)
	if _, err := c.GetDiff(context.Background(), "ws", "repo", "abc"); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer tok" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestRetriesServerErrorsWithBackoff(t *testing.T) {
	var n atomic.Int32
	c, sl := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}), nil)

	out, err := c.GetDiff(context.Background(), "ws", "repo", "abc")
	if err != nil || out != "ok" {
		t.Fatalf("got %q, %v", out, err)
	}
	if n.Load() != 3 {
		t.Fatalf("requests = %d, want 3", n.Load())
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(sl.waits) != 2 || sl.waits[0] != want[0] || sl.waits[1] != want[1] {
		t.Fatalf("waits = %v, want %v", sl.waits, want)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	c, sl := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}), func(o *Options) { o.MaxAttempts = 6; o.BaseDelay = time.Second; o.MaxDelay = 3 * time.Second })

	c.GetDiff(context.Background(), "ws", "repo", "abc")
	for _, w := range sl.waits {
		if w > 3*time.Second {
			t.Fatalf("wait %v exceeds the cap", w)
		}
	}
	if len(sl.waits) != 5 {
		t.Fatalf("waits = %d, want 5 (6 attempts)", len(sl.waits))
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "still down", http.StatusBadGateway)
	}), func(o *Options) { o.MaxAttempts = 3 })

	_, err := c.GetDiff(context.Background(), "ws", "repo", "abc")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 502 {
		t.Fatalf("err = %v, want APIError 502", err)
	}
	if n.Load() != 3 {
		t.Fatalf("requests = %d, want 3", n.Load())
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		var n atomic.Int32
		c, sl := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			http.Error(w, "nope", status)
		}), nil)

		_, err := c.GetDiff(context.Background(), "ws", "repo", "abc")
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != status {
			t.Fatalf("%d: err = %v", status, err)
		}
		if n.Load() != 1 || len(sl.waits) != 0 {
			t.Fatalf("%d: requests=%d waits=%v, want a single try", status, n.Load(), sl.waits)
		}
	}
}

func TestNotFound(t *testing.T) {
	var n atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.NotFound(w, r)
	}), nil)
	_, err := c.GetFile(context.Background(), "ws", "repo", "abc", "a.go")
	if !errors.Is(err, ErrNotFound) || n.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, n.Load())
	}
}

func TestRateLimitShortWaitIsHonoured(t *testing.T) {
	var n atomic.Int32
	c, sl := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}), nil)

	if _, err := c.GetDiff(context.Background(), "ws", "repo", "abc"); err != nil {
		t.Fatal(err)
	}
	if len(sl.waits) != 1 || sl.waits[0] != 2*time.Second {
		t.Fatalf("waits = %v, want [2s] from Retry-After", sl.waits)
	}
}

func TestRateLimitLongWaitIsReturnedNotSlept(t *testing.T) {
	var n atomic.Int32
	c, sl := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}), nil)

	_, err := c.GetDiff(context.Background(), "ws", "repo", "abc")
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 120*time.Second {
		t.Fatalf("err = %v, want RateLimitedError 120s", err)
	}
	if n.Load() != 1 || len(sl.waits) != 0 {
		t.Fatalf("requests=%d waits=%v: must not hold a worker for 2 minutes", n.Load(), sl.waits)
	}
}

func TestRateLimitWithoutHeaderUsesBackoff(t *testing.T) {
	var n atomic.Int32
	c, sl := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}), nil)
	if _, err := c.GetDiff(context.Background(), "ws", "repo", "abc"); err != nil {
		t.Fatal(err)
	}
	if len(sl.waits) != 1 || sl.waits[0] != 100*time.Millisecond {
		t.Fatalf("waits = %v", sl.waits)
	}
}

func TestStopsWhenContextEndsWhileWaiting(t *testing.T) {
	var n atomic.Int32
	c, sl := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "boom", 500)
	}), nil)
	sl.err = context.Canceled

	_, err := c.GetDiff(context.Background(), "ws", "repo", "abc")
	if !errors.Is(err, context.Canceled) || n.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, n.Load())
	}
}

func TestCancelledContextStopsRequest(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetDiff(ctx, "ws", "repo", "abc"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 20)))
	}), func(o *Options) { o.MaxBodyBytes = 10 })

	if _, err := c.GetDiff(context.Background(), "ws", "repo", "abc"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}
