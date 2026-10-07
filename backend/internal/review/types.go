// Package review turns a commit diff into validated review findings.
//
// Flow (see Run): mask secrets -> parse diff -> filter -> chunk -> Reviewer per
// chunk -> sanitize against the real diff -> aggregate -> score. Everything
// except the Reviewer itself is deterministic code; the LLM never computes the
// score and never decides which of its findings are trustworthy.
package review

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Severity levels, most to least serious.
const (
	SeverityCritical = "critical"
	SeverityMajor    = "major"
	SeverityMinor    = "minor"
	SeverityInfo     = "info"
)

// Categories the dashboard knows about. Unknown values from a model are
// normalised to CategoryOther rather than rejected.
const (
	CategoryBug             = "bug"
	CategorySecurity        = "security"
	CategoryPerformance     = "performance"
	CategoryMaintainability = "maintainability"
	CategoryStyle           = "style"
	CategoryTest            = "test"
	CategoryOther           = "other"
)

// ModelMock is stored in reviews.model so mock results are never confused
// with real ones.
const (
	ModelMock      = "mock"
	ModelClaudeCLI = "claude_cli"
)

// DefaultUsageLimitWait is used when the CLI reports a limit without saying
// when it resets.
const DefaultUsageLimitWait = 30 * time.Minute

var (
	// ErrInvalidOutput means the model answered, but not with the JSON we
	// asked for. Callers should retry (a different sample may be fine).
	ErrInvalidOutput = errors.New("review: invalid model output")
	// ErrTimeout means one reviewer call exceeded its time budget.
	ErrTimeout = errors.New("review: reviewer timed out")
	// ErrUnparseableDiff means a non-empty diff contained no recognisable
	// files; failing loudly beats silently marking the commit "clean".
	ErrUnparseableDiff = errors.New("review: diff has no recognisable files")
)

// UsageLimitError means the LLM provider refused work until RetryAfter has
// passed. It is not a failure of the commit; callers should snooze the job.
type UsageLimitError struct {
	RetryAfter time.Duration
	Message    string
}

func (e *UsageLimitError) Error() string {
	return fmt.Sprintf("review: usage limit reached (retry in %s): %s", e.RetryAfter.Round(time.Second), e.Message)
}

// Suggestion is a proposed replacement for the lines a finding points at.
type Suggestion struct {
	Original    string
	Suggested   string
	UnifiedDiff string // filled by the pipeline, not by the model
}

// Finding is one issue in the new side of the diff. Lines are 1-based line
// numbers in the file after the commit.
type Finding struct {
	FilePath    string
	LineStart   int
	LineEnd     int
	Severity    string
	Category    string
	Title       string
	Explanation string
	Suggestion  *Suggestion
}

// Result is what a Reviewer returns for one chunk.
type Result struct {
	Summary  string
	Findings []Finding
	Model    string
	Usage    Usage
}

// Usage is what one or more model calls consumed. Known is false when the
// reviewer reports nothing (the mock), so "unknown" is never stored as zero.
type Usage struct {
	// InputTokens counts everything sent to the model, cached or not.
	InputTokens  int
	OutputTokens int
	// CostUSD is the figure the provider reports, not a price list lookup.
	CostUSD float64
	Known   bool
}

// Add returns the sum; the result is Known if either side is.
func (u Usage) Add(o Usage) Usage {
	return Usage{u.InputTokens + o.InputTokens, u.OutputTokens + o.OutputTokens, u.CostUSD + o.CostUSD, u.Known || o.Known}
}

// Request is one chunk of one commit.
type Request struct {
	Repo    string
	Commit  string
	Message string
	Author  string
	Chunk   Chunk
	// PullRequest: the chunk belongs to a whole pull request, not one commit.
	PullRequest bool
}

// Reviewer reviews one chunk. Implementations must honour ctx and return
// ErrInvalidOutput, ErrTimeout or *UsageLimitError for those conditions so the
// worker can react without string matching.
type Reviewer interface {
	Review(ctx context.Context, req Request) (Result, error)
}
