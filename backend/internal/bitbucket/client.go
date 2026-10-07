// Package bitbucket is a small client for the parts of the Bitbucket Cloud
// REST API 2.0 the platform needs: commit diffs, file contents, commit lists
// and permissions.
package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.bitbucket.org/2.0"

	defaultMaxAttempts = 5
	defaultBaseDelay   = 500 * time.Millisecond
	defaultMaxDelay    = 8 * time.Second
	// A Retry-After longer than this is not waited out inside the call. It is
	// returned as *RateLimitedError so the job queue can reschedule instead of
	// holding a worker for minutes.
	defaultMaxInlineWait = 30 * time.Second
	defaultMaxBody       = 10 << 20 // 10 MiB
)

var (
	// ErrNotFound is returned for a 404 (deleted file, unknown commit, no access to hide existence).
	ErrNotFound = errors.New("bitbucket: not found")
	// ErrTooLarge is returned when a response body exceeds the size limit.
	ErrTooLarge = errors.New("bitbucket: response too large")
)

// APIError is any other non-2xx answer.
type APIError struct {
	Status int
	Body   string // first part of the body, for logs
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bitbucket: HTTP %d: %s", e.Status, e.Body)
}

// RateLimitedError reports a 429 whose Retry-After is too long to wait inline.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("bitbucket: rate limited, retry after %s", e.RetryAfter)
}

// Options configure a Client. Only Token is required.
type Options struct {
	BaseURL    string // default DefaultBaseURL
	Token      string // workspace, repository or app access token, sent as a Bearer token
	HTTPClient *http.Client

	MaxAttempts   int           // total tries per request, default 5
	BaseDelay     time.Duration // first backoff step, default 500ms (doubles each retry)
	MaxDelay      time.Duration // backoff cap, default 8s
	MaxInlineWait time.Duration // longest Retry-After waited out inline, default 30s
	MaxBodyBytes  int64         // response size limit, default 10 MiB

	// Sleep waits for d or until ctx is done. Replaced in tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

type Client struct {
	base          *url.URL
	token         string
	http          *http.Client
	maxAttempts   int
	baseDelay     time.Duration
	maxDelay      time.Duration
	maxInlineWait time.Duration
	maxBody       int64
	sleep         func(ctx context.Context, d time.Duration) error
}

func New(o Options) (*Client, error) {
	if strings.TrimSpace(o.Token) == "" {
		return nil, errors.New("bitbucket: token is required")
	}
	raw := o.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("bitbucket: invalid base URL %q", raw)
	}

	c := &Client{
		base:          base,
		token:         o.Token,
		http:          o.HTTPClient,
		maxAttempts:   o.MaxAttempts,
		baseDelay:     o.BaseDelay,
		maxDelay:      o.MaxDelay,
		maxInlineWait: o.MaxInlineWait,
		maxBody:       o.MaxBodyBytes,
		sleep:         o.Sleep,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 60 * time.Second}
	}
	if c.maxAttempts <= 0 {
		c.maxAttempts = defaultMaxAttempts
	}
	if c.baseDelay <= 0 {
		c.baseDelay = defaultBaseDelay
	}
	if c.maxDelay <= 0 {
		c.maxDelay = defaultMaxDelay
	}
	if c.maxInlineWait <= 0 {
		c.maxInlineWait = defaultMaxInlineWait
	}
	if c.maxBody <= 0 {
		c.maxBody = defaultMaxBody
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	return c, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// endpoint builds BASE/seg1/seg2/... with every segment path-escaped, so a file
// name containing "#", "?" or spaces cannot change the request. A file path is
// passed as one argument per directory level, never as a single "a/b/c" string.
func (c *Client) endpoint(segments ...string) string {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	raw := strings.TrimRight(c.base.EscapedPath(), "/") + "/" + strings.Join(escaped, "/")

	u := *c.base
	u.RawPath = raw
	u.Path, _ = url.PathUnescape(raw)
	return u.String()
}

// get performs one logical GET with retries and returns the (size-limited) body.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		body, retryAfter, err := c.once(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err

		var retryable *retryableError
		if !errors.As(err, &retryable) {
			return nil, err
		}
		if attempt == c.maxAttempts {
			break
		}

		wait := c.backoff(attempt)
		if retryAfter > 0 {
			if retryAfter > c.maxInlineWait {
				return nil, &RateLimitedError{RetryAfter: retryAfter}
			}
			wait = retryAfter
		}
		if err := c.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}

	var retryable *retryableError
	if errors.As(lastErr, &retryable) {
		return nil, retryable.err
	}
	return nil, lastErr
}

func (c *Client) backoff(attempt int) time.Duration {
	d := c.baseDelay << (attempt - 1)
	if d > c.maxDelay || d <= 0 {
		d = c.maxDelay
	}
	return d
}

// retryableError marks a failure worth another attempt.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func (c *Client) once(ctx context.Context, rawURL string) (body []byte, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, &retryableError{err: fmt.Errorf("bitbucket: request failed: %w", err)}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
		if err != nil {
			return nil, 0, &retryableError{err: fmt.Errorf("bitbucket: reading body: %w", err)}
		}
		if int64(len(data)) > c.maxBody {
			return nil, 0, ErrTooLarge
		}
		return data, 0, nil

	case resp.StatusCode == http.StatusNotFound:
		return nil, 0, ErrNotFound

	case resp.StatusCode == http.StatusTooManyRequests:
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, ra, &retryableError{err: &RateLimitedError{RetryAfter: ra}}

	case resp.StatusCode >= 500:
		return nil, parseRetryAfter(resp.Header.Get("Retry-After")), &retryableError{err: apiError(resp)}

	default:
		return nil, 0, apiError(resp)
	}
}

func apiError(resp *http.Response) *APIError {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
}

// parseRetryAfter reads the delta-seconds form. A missing or unparseable value
// is 0, which makes the caller fall back to exponential backoff.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
