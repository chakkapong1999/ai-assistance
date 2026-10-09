package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadAPIDefaults(t *testing.T) {
	c, err := Load(ModeAPI, env(map[string]string{
		"DATABASE_URL":             "postgres://x",
		"BITBUCKET_WEBHOOK_SECRET": "s3cret",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.HTTPAddr != ":8080" || c.ReviewerMode != ReviewerMock || c.MockReviewScenario != ScenarioFindings {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.MockReviewDelay != 0 {
		t.Fatalf("delay default = %v, want 0", c.MockReviewDelay)
	}
}

func TestLoadRequiredPerMode(t *testing.T) {
	cases := []struct {
		name string
		mode string
		env  map[string]string
		want []string
	}{
		{"api needs db and secret", ModeAPI, nil, []string{"DATABASE_URL", "BITBUCKET_WEBHOOK_SECRET"}},
		{"worker needs db and token", ModeWorker, nil, []string{"DATABASE_URL", "BITBUCKET_TOKEN"}},
		{"backfill needs db and token", ModeBackfill, nil, []string{"DATABASE_URL", "BITBUCKET_TOKEN"}},
		{"unknown mode", "nope", nil, []string{`unknown mode "nope"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.mode, env(tc.env))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestMockSettings(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t"}

	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	c, err := Load(ModeWorker, env(with(map[string]string{"MOCK_REVIEW_DELAY": "2s", "MOCK_REVIEW_SCENARIO": "usage_limit"})))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.MockReviewDelay != 2*time.Second || c.MockReviewScenario != ScenarioUsageLimit {
		t.Fatalf("got %+v", c)
	}

	for name, extra := range map[string]map[string]string{
		"bad scenario":   {"MOCK_REVIEW_SCENARIO": "weird"},
		"bad delay":      {"MOCK_REVIEW_DELAY": "soon"},
		"negative delay": {"MOCK_REVIEW_DELAY": "-1s"},
		"bad reviewer":   {"REVIEWER_MODE": "gpt"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(ModeWorker, env(with(extra))); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestClaudeCLIRequiresBinaryInWorkerOnly(t *testing.T) {
	e := map[string]string{
		"DATABASE_URL":             "postgres://x",
		"BITBUCKET_TOKEN":          "t",
		"BITBUCKET_WEBHOOK_SECRET": "s",
		"REVIEWER_MODE":            "claude_cli",
		"CLAUDE_BIN":               "/definitely/not/here/claude",
	}

	_, err := Load(ModeWorker, env(e))
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_BIN") {
		t.Fatalf("worker: want CLAUDE_BIN error, got %v", err)
	}

	// The API never runs the CLI, so a missing binary must not stop it.
	if _, err := Load(ModeAPI, env(e)); err != nil {
		t.Fatalf("api: unexpected error: %v", err)
	}

	// A binary that exists passes.
	e["CLAUDE_BIN"] = "sh"
	if _, err := Load(ModeWorker, env(e)); err != nil {
		t.Fatalf("worker with existing binary: %v", err)
	}
}

func TestBitbucketBaseURLOverride(t *testing.T) {
	env := func(extra map[string]string) func(string) string {
		base := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t", "REVIEWER_MODE": "mock"}
		for k, v := range extra {
			base[k] = v
		}
		return func(k string) string { return base[k] }
	}
	cfg, err := Load("worker", env(map[string]string{"BITBUCKET_BASE_URL": " http://localhost:7990 "}))
	if err != nil || cfg.BitbucketBaseURL != "http://localhost:7990" {
		t.Fatalf("cfg=%q err=%v", cfg.BitbucketBaseURL, err)
	}
	if cfg, err := Load("worker", env(nil)); err != nil || cfg.BitbucketBaseURL != "" {
		t.Fatalf("default should be empty (use Bitbucket Cloud): %q %v", cfg.BitbucketBaseURL, err)
	}
	for _, bad := range []string{"localhost:7990", "ftp://x", "http://"} {
		if _, err := Load("worker", env(map[string]string{"BITBUCKET_BASE_URL": bad})); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestAPITokens(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_WEBHOOK_SECRET": "s"}
	load := func(tokens string) (Config, error) {
		e := map[string]string{"API_TOKENS": tokens}
		for k, v := range base {
			e[k] = v
		}
		return Load(ModeAPI, env(e))
	}

	c, err := load(" aaaaaaaaaaaaaaaa:admin , bbbbbbbbbbbbbbbb:viewer ")
	if err != nil || len(c.APITokens) != 2 || c.APITokens[0] != (APIToken{Token: "aaaaaaaaaaaaaaaa", Role: RoleAdmin}) || c.APITokens[1].Role != RoleViewer {
		t.Fatalf("got %+v, %v", c.APITokens, err)
	}
	if c, err := load(""); err != nil || len(c.APITokens) != 0 {
		t.Fatalf("unset tokens must be allowed (API disabled): %+v, %v", c.APITokens, err)
	}

	secret := "supersecrettoken1234"
	for name, v := range map[string]string{
		"no role":    secret,
		"bad role":   secret + ":root",
		"short":      "short:admin",
		"duplicate":  secret + ":admin," + secret + ":viewer",
		"empty role": secret + ":",
	} {
		_, err := load(v)
		if err == nil {
			t.Errorf("%s: want an error", name)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaks the token: %v", name, err)
		}
	}
}

func TestAPITokensWithUsers(t *testing.T) {
	load := func(tokens string) (Config, error) {
		return Load(ModeAPI, env(map[string]string{"API_TOKENS": tokens, "DATABASE_URL": "postgres://x", "BITBUCKET_WEBHOOK_SECRET": "s"}))
	}
	c, err := load("aaaaaaaaaaaaaaaa:author:7, bbbbbbbbbbbbbbbb:senior:8,cccccccccccccccc:lead:9,dddddddddddddddd:admin:10,eeeeeeeeeeeeeeee:admin,ffffffffffffffff:viewer,ab:cd-0123456789:author:11")
	if err != nil || len(c.APITokens) != 7 {
		t.Fatalf("%+v %v", c.APITokens, err)
	}
	want := []APIToken{
		{"aaaaaaaaaaaaaaaa", RoleAuthor, 7}, {"bbbbbbbbbbbbbbbb", RoleSenior, 8}, {"cccccccccccccccc", RoleLead, 9},
		{"dddddddddddddddd", RoleAdmin, 10}, {"eeeeeeeeeeeeeeee", RoleAdmin, 0}, {"ffffffffffffffff", RoleViewer, 0}, {"ab:cd-0123456789", RoleAuthor, 11},
	}
	for i, w := range want {
		if c.APITokens[i] != w {
			t.Errorf("token %d = %+v, want %+v", i, c.APITokens[i], w)
		}
	}
	for name, v := range map[string]string{
		"author without user": "aaaaaaaaaaaaaaaa:author",
		"senior without user": "aaaaaaaaaaaaaaaa:senior",
		"lead without user":   "aaaaaaaaaaaaaaaa:lead",
		"zero user":           "aaaaaaaaaaaaaaaa:author:0",
		"negative user":       "aaaaaaaaaaaaaaaa:author:-3",
		"user without role":   "aaaaaaaaaaaaaaaa:5",
	} {
		if _, err := load(v); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if !RoleAtLeast(RoleLead, RoleSenior) || RoleAtLeast(RoleAuthor, RoleSenior) || RoleAtLeast("nope", RoleViewer) {
		t.Error("RoleAtLeast ranks wrongly")
	}
}

func TestPollConfig(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t"}
	load := func(extra map[string]string) (Config, error) {
		e := map[string]string{}
		for k, v := range base {
			e[k] = v
		}
		for k, v := range extra {
			e[k] = v
		}
		return Load(ModeWorker, env(e))
	}

	c, err := load(nil)
	if err != nil || len(c.PollRepos) != 0 || c.PollInterval != 5*time.Minute || c.PollLookback != 168*time.Hour {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = load(map[string]string{"POLL_REPOS": " acme/api, acme/*,acme/api ,my-ws/web.app", "POLL_INTERVAL": "90s", "POLL_LOOKBACK": "24h"})
	if err != nil || len(c.PollRepos) != 3 || c.PollRepos[1] != "acme/*" || c.PollInterval != 90*time.Second || c.PollLookback != 24*time.Hour {
		t.Fatalf("parsed: %+v %v", c, err)
	}
	for name, e := range map[string]map[string]string{
		"no slash":        {"POLL_REPOS": "acme"},
		"star workspace":  {"POLL_REPOS": "*/api"},
		"path traversal":  {"POLL_REPOS": "acme/../x"},
		"extra segment":   {"POLL_REPOS": "acme/api/x"},
		"bad interval":    {"POLL_INTERVAL": "often"},
		"interval < 1m":   {"POLL_INTERVAL": "10s"},
		"lookback < 1h":   {"POLL_LOOKBACK": "5m"},
		"days not a unit": {"POLL_LOOKBACK": "7d"},
	} {
		if _, err := load(e); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestReconcileInterval(t *testing.T) {
	load := func(v string) (Config, error) {
		e := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t"}
		if v != "" {
			e["RECONCILE_INTERVAL"] = v
		}
		return Load(ModeWorker, env(e))
	}
	if c, err := load(""); err != nil || c.ReconcileInterval != 5*time.Minute {
		t.Fatalf("default: %v %v", c.ReconcileInterval, err)
	}
	if c, err := load("90s"); err != nil || c.ReconcileInterval != 90*time.Second {
		t.Fatalf("parsed: %v %v", c.ReconcileInterval, err)
	}
	for _, v := range []string{"often", "10s"} {
		if _, err := load(v); err == nil {
			t.Errorf("%q: want an error", v)
		}
	}
}

func TestBackfillSettings(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t", "POLL_REPOS": "acme/*"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	c, err := Load(ModeBackfill, env(with()))
	if err != nil || c.BackfillDays != 90 || c.BackfillMaxCommits != 2000 || c.BackfillReview {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = Load(ModeBackfill, env(with("BACKFILL_DAYS", "365", "BACKFILL_MAX_COMMITS", "50", "BACKFILL_REVIEW", "true")))
	if err != nil || c.BackfillDays != 365 || c.BackfillMaxCommits != 50 || !c.BackfillReview {
		t.Fatalf("parsed: %+v %v", c, err)
	}
	for _, kv := range [][]string{{"BACKFILL_DAYS", "0"}, {"BACKFILL_DAYS", "4000"}, {"BACKFILL_MAX_COMMITS", "x"}, {"BACKFILL_REVIEW", "maybe"}} {
		if _, err := Load(ModeBackfill, env(with(kv...))); err == nil {
			t.Errorf("%v: want an error", kv)
		}
	}
	noRepos := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t"}
	if _, err := Load(ModeBackfill, env(noRepos)); err == nil || !strings.Contains(err.Error(), "POLL_REPOS") {
		t.Errorf("backfill without POLL_REPOS: %v", err)
	}
}

func TestDirectorySyncInterval(t *testing.T) {
	load := func(v string) (Config, error) {
		e := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t"}
		if v != "" {
			e["DIRECTORY_SYNC_INTERVAL"] = v
		}
		return Load(ModeWorker, env(e))
	}
	if c, err := load(""); err != nil || c.DirectoryInterval != 24*time.Hour {
		t.Fatalf("default: %v %v", c.DirectoryInterval, err)
	}
	if c, err := load("12h"); err != nil || c.DirectoryInterval != 12*time.Hour {
		t.Fatalf("parsed: %v %v", c.DirectoryInterval, err)
	}
	for _, v := range []string{"daily", "5m"} {
		if _, err := load(v); err == nil {
			t.Errorf("%q: want an error", v)
		}
	}
}

func TestReviewNewReposDefaultsToOnAndCanBeSwitchedOff(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t"}
	with := func(v string) map[string]string {
		m := map[string]string{"REVIEW_NEW_REPOS": v}
		for k, x := range base {
			m[k] = x
		}
		return m
	}
	for in, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "0": false, "FALSE": false} {
		c, err := Load(ModeWorker, env(with(in)))
		if err != nil || c.ReviewNewRepos != want {
			t.Errorf("REVIEW_NEW_REPOS=%q -> %v, %v; want %v", in, c.ReviewNewRepos, err, want)
		}
	}
	if _, err := Load(ModeWorker, env(with("maybe"))); err == nil || !strings.Contains(err.Error(), "REVIEW_NEW_REPOS") {
		t.Errorf("an invalid value must be an error naming the variable, got %v", err)
	}
}

func TestClaudeModelDefaultsToSonnet55(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "BITBUCKET_TOKEN": "t"}
	c, err := Load(ModeWorker, env(base))
	if err != nil || c.ClaudeModel != "claude-sonnet-5-5" {
		t.Fatalf("default: %q %v", c.ClaudeModel, err)
	}
	base["CLAUDE_MODEL"] = "claude-opus-5-5"
	if c, err = Load(ModeWorker, env(base)); err != nil || c.ClaudeModel != "claude-opus-5-5" {
		t.Fatalf("override: %q %v", c.ClaudeModel, err)
	}
}
