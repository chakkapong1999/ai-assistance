package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCLITimeout = 5 * time.Minute
	maxCLIOutput      = 8 << 20
	maxUsageWait      = 12 * time.Hour
)

// RunResult is the outcome of running the CLI process.
type RunResult struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Runner starts the CLI. It is a field so tests can avoid a real process;
// the default runs bin with os/exec.
type Runner func(ctx context.Context, bin string, args []string, stdin []byte, dir string) (RunResult, error)

// CLIOptions configures ClaudeCLI.
type CLIOptions struct {
	Bin     string        // default "claude"
	Model   string        // optional --model
	Timeout time.Duration // per call; default 5m
	Run     Runner        // default: os/exec
}

// ClaudeCLI reviews by running the local `claude` CLI in headless mode.
type ClaudeCLI struct {
	opts CLIOptions
	now  func() time.Time
}

func NewClaudeCLI(o CLIOptions) *ClaudeCLI {
	if o.Bin == "" {
		o.Bin = "claude"
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultCLITimeout
	}
	if o.Run == nil {
		o.Run = execRunner
	}
	return &ClaudeCLI{opts: o, now: time.Now}
}

// cliEnvelope is the JSON printed by `claude -p --output-format json`; the
// model's answer is in Result.
type cliEnvelope struct {
	IsError bool   `json:"is_error"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
	// TotalCostUSD is the CLI's own estimate for the call.
	TotalCostUSD *float64 `json:"total_cost_usd"`
	Usage        *struct {
		InputTokens         int `json:"input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		OutputTokens        int `json:"output_tokens"`
	} `json:"usage"`
}

func (e cliEnvelope) usage() Usage {
	var u Usage
	if e.Usage != nil {
		u.InputTokens = e.Usage.InputTokens + e.Usage.CacheCreationTokens + e.Usage.CacheReadTokens
		u.OutputTokens = e.Usage.OutputTokens
		u.Known = true
	}
	if e.TotalCostUSD != nil {
		u.CostUSD = *e.TotalCostUSD
		u.Known = true
	}
	return u
}

func (c *ClaudeCLI) Review(ctx context.Context, req Request) (Result, error) {
	args := []string{"-p", "--output-format", "json", "--max-turns", "1"}
	if c.opts.Model != "" {
		args = append(args, "--model", c.opts.Model)
	}

	// The diff is untrusted input. Run in an empty directory so the CLI has no
	// repository, CLAUDE.md or project settings to read or touch.
	dir, err := os.MkdirTemp("", "aicr-claude-*")
	if err != nil {
		return Result{}, fmt.Errorf("review: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	callCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	rr, runErr := c.opts.Run(callCtx, c.opts.Bin, args, []byte(BuildPrompt(req)), dir)

	// Check the contexts first: a killed process surfaces as an ExitError,
	// which would otherwise be misread as a CLI failure.
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return Result{}, ErrTimeout
	}
	if runErr != nil {
		return Result{}, fmt.Errorf("review: run %s: %w", c.opts.Bin, runErr)
	}

	var env cliEnvelope
	envErr := json.Unmarshal(bytes.TrimSpace(rr.Stdout), &env)

	if rr.ExitCode != 0 || (envErr == nil && env.IsError) {
		text := env.Result + "\n" + string(rr.Stderr) + "\n" + string(rr.Stdout)
		if ule, ok := ClassifyUsageLimit(text, c.now()); ok {
			return Result{}, ule
		}
		return Result{}, fmt.Errorf("review: claude exited %d: %s", rr.ExitCode, cliFailureDetail(env, envErr, rr))
	}
	if envErr != nil {
		return Result{}, fmt.Errorf("%w: CLI output is not JSON: %v", ErrInvalidOutput, envErr)
	}

	res, err := ParseOutput([]byte(env.Result))
	if err != nil {
		return Result{}, err
	}
	res.Model = ModelClaudeCLI
	res.Usage = env.usage()
	return res, nil
}

// cliFailureDetail says why the CLI failed. The CLI often reports the reason
// only in the JSON on stdout (is_error with an empty result and a subtype such
// as error_max_turns), so an empty message would hide it.
func cliFailureDetail(env cliEnvelope, envErr error, rr RunResult) string {
	msg := strings.TrimSpace(env.Result + " " + string(rr.Stderr))
	if env.Subtype != "" {
		msg = strings.TrimSpace("[" + env.Subtype + "] " + msg)
	}
	if msg == "" {
		// nothing parsed: show the start of stdout, which is where the CLI printed the reason
		msg = "no message; stdout: " + strings.TrimSpace(string(rr.Stdout))
		if envErr != nil && len(rr.Stdout) == 0 {
			msg = "no output at all (does the CLI run here and is it logged in?)"
		}
	}
	return truncateRunes(msg, 500)
}

var (
	usageLimitMarkers = []string{"usage limit", "rate limit", "limit reached", "too many requests", "quota", "overloaded"}
	// The CLI has been seen to append the reset time as unix seconds: "...limit reached|1760000000".
	resetEpoch = regexp.MustCompile(`\|\s*(\d{10})\b`)
)

// ClassifyUsageLimit recognises a provider limit in CLI output and works out
// how long to wait. The exact wording is the CLI's, not ours, so this is
// deliberately tolerant and falls back to DefaultUsageLimitWait; it should be
// checked against a real limit message (see the tests for the assumed forms).
func ClassifyUsageLimit(text string, now time.Time) (*UsageLimitError, bool) {
	lower := strings.ToLower(text)
	hit := false
	for _, m := range usageLimitMarkers {
		if strings.Contains(lower, m) {
			hit = true
			break
		}
	}
	if !hit {
		return nil, false
	}
	wait := DefaultUsageLimitWait
	if m := resetEpoch.FindStringSubmatch(text); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			if d := time.Unix(sec, 0).Sub(now) + time.Minute; d > 0 {
				wait = min(d, maxUsageWait)
			}
		}
	}
	return &UsageLimitError{RetryAfter: wait, Message: truncateRunes(strings.TrimSpace(text), 300)}, true
}

type limitedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.buf.Len()+len(p) > l.max {
		l.over = true
		return len(p), nil // discard, keep the process from blocking
	}
	return l.buf.Write(p)
}

func execRunner(ctx context.Context, bin string, args []string, stdin []byte, dir string) (RunResult, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	out := &limitedBuffer{max: maxCLIOutput}
	errb := &limitedBuffer{max: 1 << 20}
	cmd.Stdout, cmd.Stderr = out, errb
	cmd.WaitDelay = 5 * time.Second // do not hang on grandchildren holding the pipes

	err := cmd.Run()
	rr := RunResult{Stdout: out.buf.Bytes(), Stderr: errb.buf.Bytes()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		rr.ExitCode = ee.ExitCode()
		return rr, nil
	}
	if err != nil {
		return rr, err
	}
	if out.over {
		return rr, fmt.Errorf("output exceeded %d bytes", maxCLIOutput)
	}
	return rr, nil
}
