// Package config loads and validates runtime configuration from environment
// variables. Validation is done once at start-up so a misconfigured process
// fails fast with a clear message instead of failing later inside a job.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Run modes of the single backend binary.
const (
	ModeAPI      = "api"
	ModeWorker   = "worker"
	ModeBackfill = "backfill"
)

// Reviewer modes. "mock" never calls an LLM and is the safe default.
const (
	ReviewerClaudeCLI = "claude_cli"
	ReviewerMock      = "mock"
)

// Mock scenarios used to exercise retry / snooze / error handling in tests.
const (
	ScenarioClean       = "clean"
	ScenarioFindings    = "findings"
	ScenarioInvalidJSON = "invalid_json"
	ScenarioUsageLimit  = "usage_limit"
	ScenarioTimeout     = "timeout"
)

type Config struct {
	Mode string

	HTTPAddr    string
	DatabaseURL string

	BitbucketWebhookSecret string
	BitbucketToken         string
	// BitbucketBaseURL overrides the Bitbucket API root (default api.bitbucket.org/2.0).
	// For local testing against cmd/mockbitbucket; the token is sent to this URL.
	BitbucketBaseURL string

	ReviewerMode       string
	MockReviewScenario string
	MockReviewDelay    time.Duration
	ClaudeBin          string
}

// Load builds a Config from getenv (os.Getenv in production, a map lookup in
// tests) and the run mode, then validates it for that mode.
func Load(mode string, getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		Mode:                   mode,
		HTTPAddr:               get("HTTP_ADDR", ":8080"),
		DatabaseURL:            get("DATABASE_URL", ""),
		BitbucketWebhookSecret: get("BITBUCKET_WEBHOOK_SECRET", ""),
		BitbucketToken:         get("BITBUCKET_TOKEN", ""),
		BitbucketBaseURL:       strings.TrimSpace(get("BITBUCKET_BASE_URL", "")),
		ReviewerMode:           get("REVIEWER_MODE", ReviewerMock),
		MockReviewScenario:     get("MOCK_REVIEW_SCENARIO", ScenarioFindings),
		ClaudeBin:              get("CLAUDE_BIN", "claude"),
	}

	var errs []error
	if d := get("MOCK_REVIEW_DELAY", "0s"); d != "" {
		parsed, err := time.ParseDuration(d)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("MOCK_REVIEW_DELAY: %w", err))
		case parsed < 0:
			errs = append(errs, errors.New("MOCK_REVIEW_DELAY must not be negative"))
		default:
			c.MockReviewDelay = parsed
		}
	}

	errs = append(errs, c.validate()...)
	return c, errors.Join(errs...)
}

func (c Config) validate() []error {
	var errs []error
	need := func(name, val string) {
		if val == "" {
			errs = append(errs, fmt.Errorf("%s is required in %s mode", name, c.Mode))
		}
	}

	switch c.Mode {
	case ModeAPI:
		need("DATABASE_URL", c.DatabaseURL)
		need("BITBUCKET_WEBHOOK_SECRET", c.BitbucketWebhookSecret)
	case ModeWorker, ModeBackfill:
		need("DATABASE_URL", c.DatabaseURL)
		need("BITBUCKET_TOKEN", c.BitbucketToken)
	default:
		errs = append(errs, fmt.Errorf("unknown mode %q (want api, worker or backfill)", c.Mode))
	}

	if c.BitbucketBaseURL != "" {
		if u, err := url.Parse(c.BitbucketBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("BITBUCKET_BASE_URL %q must be an http(s) URL", c.BitbucketBaseURL))
		}
	}

	switch c.ReviewerMode {
	case ReviewerMock:
		switch c.MockReviewScenario {
		case ScenarioClean, ScenarioFindings, ScenarioInvalidJSON, ScenarioUsageLimit, ScenarioTimeout:
		default:
			errs = append(errs, fmt.Errorf("MOCK_REVIEW_SCENARIO %q is not one of clean, findings, invalid_json, usage_limit, timeout", c.MockReviewScenario))
		}
	case ReviewerClaudeCLI:
		// Only the worker actually runs the CLI.
		if c.Mode == ModeWorker {
			if _, err := exec.LookPath(c.ClaudeBin); err != nil {
				errs = append(errs, fmt.Errorf("REVIEWER_MODE=claude_cli but CLAUDE_BIN %q was not found: %w", c.ClaudeBin, err))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("REVIEWER_MODE %q is not one of claude_cli, mock", c.ReviewerMode))
	}

	return errs
}
