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
