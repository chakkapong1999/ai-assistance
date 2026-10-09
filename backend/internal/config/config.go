// Package config loads and validates runtime configuration from environment
// variables. Validation is done once at start-up so a misconfigured process
// fails fast with a clear message instead of failing later inside a job.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
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

	// APITokens guard the dashboard REST API (/api/v1). Empty = API not served.
	APITokens []APIToken

	// Polling (worker): look for new commits in these repositories without a
	// webhook. Entries are "workspace/repo" or "workspace/*" (every repository
	// of a workspace the token can see). Empty = polling is off.
	PollRepos    []string
	PollInterval time.Duration
	// PollLookback bounds how far back the first poll of a branch reads.
	PollLookback time.Duration

	// ReconcileInterval is how often the worker looks for work that lost its
	// job and queues it again.
	ReconcileInterval time.Duration
	// DirectoryInterval is how often roles, permissions and profiles are read
	// from Bitbucket again. Default 24h.
	DirectoryInterval time.Duration

	// ReviewNewRepos: a repository first seen by the worker starts with review
	// on (default) or off. It only decides the starting value; an admin's later
	// choice in the dashboard is never overwritten.
	ReviewNewRepos bool

	ReviewerMode       string
	MockReviewScenario string
	MockReviewDelay    time.Duration
	ClaudeBin          string
}

// Roles of an API token, lowest first. viewer = read only; author = a developer
// who marks their own findings as fixed; senior, lead and admin also review
// other people's work and close reviews; admin may also change settings.
// author, senior and lead tokens must name the user they belong to.
const (
	RoleViewer = "viewer"
	RoleAuthor = "author"
	RoleSenior = "senior"
	RoleLead   = "lead"
	RoleAdmin  = "admin"
)

// MinTokenLen is the shortest accepted API token.
const MinTokenLen = 16

type APIToken struct {
	Token  string
	Role   string
	UserID int64 // 0 = a token that is not linked to a person
}

var roleRank = map[string]int{RoleViewer: 0, RoleAuthor: 1, RoleSenior: 2, RoleLead: 3, RoleAdmin: 4}

// RoleAtLeast reports whether role is min or higher.
func RoleAtLeast(role, min string) bool {
	r, ok := roleRank[role]
	m, ok2 := roleRank[min]
	return ok && ok2 && r >= m
}

// parseAPITokens parses "token:role[:user_id],token:role[:user_id]". Errors never include the
// token text, so a bad value does not end up in logs.
func parseAPITokens(raw string) ([]APIToken, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []APIToken
	seen := map[string]bool{}
	for i, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		tok, role, uid, err := splitToken(part)
		switch {
		case err != nil:
			return nil, fmt.Errorf("API_TOKENS entry %d: %v", i+1, err)
		case roleRank[role] == 0 && role != RoleViewer:
			return nil, fmt.Errorf("API_TOKENS entry %d: role must be one of viewer, author, senior, lead, admin", i+1)
		case (role == RoleAuthor || role == RoleSenior || role == RoleLead) && uid == 0:
			return nil, fmt.Errorf("API_TOKENS entry %d: a %s token needs a user id (token:%s:USER_ID)", i+1, role, role)
		case len(tok) < MinTokenLen:
			return nil, fmt.Errorf("API_TOKENS entry %d: token must be at least %d characters", i+1, MinTokenLen)
		case seen[tok]:
			return nil, fmt.Errorf("API_TOKENS entry %d: duplicate token", i+1)
		}
		seen[tok] = true
		out = append(out, APIToken{Token: tok, Role: role, UserID: uid})
	}
	return out, nil
}

// splitToken separates "token:role" or "token:role:user_id". The token itself
// may contain colons; the error never includes it.
func splitToken(part string) (tok, role string, uid int64, err error) {
	idx := strings.LastIndex(part, ":")
	if idx < 0 {
		return "", "", 0, errors.New("want token:role")
	}
	last := strings.TrimSpace(part[idx+1:])
	rest := part[:idx]
	if n, perr := strconv.ParseInt(last, 10, 64); perr == nil {
		if n <= 0 {
			return "", "", 0, errors.New("user id must be a positive number")
		}
		j := strings.LastIndex(rest, ":")
		if j < 0 {
			return "", "", 0, errors.New("want token:role:user_id")
		}
		return strings.TrimSpace(rest[:j]), strings.TrimSpace(rest[j+1:]), n, nil
	}
	return strings.TrimSpace(rest), last, 0, nil
}

const (
	defaultPollInterval      = 5 * time.Minute
	defaultPollLookback      = 7 * 24 * time.Hour
	defaultReconcileInterval = 5 * time.Minute
	defaultDirectoryInterval = 24 * time.Hour
	// MinPollInterval protects the Bitbucket API quota.
	MinPollInterval = time.Minute
)

var pollRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/([A-Za-z0-9][A-Za-z0-9._-]*|\*)$`)

// parsePollRepos parses "ws/repo,ws/*"; duplicates are dropped.
func parsePollRepos(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !pollRepoRe.MatchString(part) {
			return nil, fmt.Errorf("POLL_REPOS entry %q must look like workspace/repo or workspace/*", part)
		}
		if !seen[part] {
			seen[part] = true
			out = append(out, part)
		}
	}
	return out, nil
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
	c.ReviewNewRepos = true
	if v := get("REVIEW_NEW_REPOS", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("REVIEW_NEW_REPOS: %q is not true or false", v))
		} else {
			c.ReviewNewRepos = b
		}
	}
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

	if repos, err := parsePollRepos(getenv("POLL_REPOS")); err != nil {
		errs = append(errs, err)
	} else {
		c.PollRepos = repos
	}
	c.PollInterval = defaultPollInterval
	c.PollLookback = defaultPollLookback
	c.ReconcileInterval = defaultReconcileInterval
	c.DirectoryInterval = defaultDirectoryInterval
	for _, d := range []struct {
		key string
		dst *time.Duration
		min time.Duration
	}{{"POLL_INTERVAL", &c.PollInterval, MinPollInterval}, {"POLL_LOOKBACK", &c.PollLookback, time.Hour}, {"RECONCILE_INTERVAL", &c.ReconcileInterval, time.Minute}, {"DIRECTORY_SYNC_INTERVAL", &c.DirectoryInterval, time.Hour}} {
		if v := get(d.key, ""); v != "" {
			parsed, err := time.ParseDuration(v)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("%s: %w (use a Go duration such as 5m or 168h)", d.key, err))
			case parsed < d.min:
				errs = append(errs, fmt.Errorf("%s must be at least %s", d.key, d.min))
			default:
				*d.dst = parsed
			}
		}
	}

	if toks, err := parseAPITokens(getenv("API_TOKENS")); err != nil {
		errs = append(errs, err)
	} else {
		c.APITokens = toks
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
