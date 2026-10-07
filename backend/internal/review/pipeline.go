package review

import (
	"context"
	"fmt"
	"strings"
)

// Input is one commit to review. Diff is the raw unified diff from Bitbucket.
type Input struct {
	Repo, Commit, Message, Author string
	Diff                          string
}

// Outcome is everything the worker stores for a finished review.
type Outcome struct {
	// Reviewable is false when nothing was sent to the reviewer (every file
	// was filtered out); the commit should be marked skipped, not reviewed.
	Reviewable bool
	Summary    string
	Findings   []Finding
	Score      int
	Model      string
	Skipped    []Skipped
	Dropped    int // findings discarded because they did not match the diff
	Chunks     int
	// Usage sums every chunk's model calls.
	Usage Usage

	// Whole-commit stats, counting files that were skipped too.
	Files, Additions, Deletions int
}

// PromptVersion is stored with every review so scores can be compared across
// prompt changes. Bump it whenever promptInstructions changes.
const PromptVersion = "v1"

// Run reviews one commit. It stops at the first chunk error and returns it
// unchanged in kind (errors.Is / errors.As still work) so the caller can
// retry, snooze or fail the job; partial results are discarded because a
// score built from some chunks would be misleading.
func Run(ctx context.Context, rv Reviewer, in Input, lim Limits) (Outcome, error) {
	lim = lim.withDefaults()

	// Mask first: everything after this (prompt, stored snippets, suggestions)
	// only ever sees the masked text.
	diff := MaskSecrets(in.Diff)

	files := ParseDiff(diff)
	if len(files) == 0 {
		if strings.TrimSpace(diff) == "" {
			return Outcome{Reviewable: false}, nil // empty commit
		}
		return Outcome{}, ErrUnparseableDiff
	}

	kept, skipped := Filter(files, lim)
	out := Outcome{Skipped: skipped, Files: len(files)}
	for _, f := range files {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case '+':
					out.Additions++
				case '-':
					out.Deletions++
				}
			}
		}
	}
	if len(kept) == 0 {
		return out, nil
	}
	out.Reviewable = true

	chunks := Split(kept, lim.MaxChunkBytes)
	out.Chunks = len(chunks)

	var summaries []string
	for i, ch := range chunks {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		res, err := rv.Review(ctx, Request{
			Repo: in.Repo, Commit: in.Commit, Message: in.Message, Author: in.Author, Chunk: ch,
		})
		if err != nil {
			return Outcome{}, fmt.Errorf("chunk %d/%d: %w", i+1, len(chunks), err)
		}
		res, dropped := Sanitize(res, ch)
		out.Usage = out.Usage.Add(res.Usage)
		out.Dropped += dropped
		out.Findings = append(out.Findings, res.Findings...)
		if res.Summary != "" {
			summaries = append(summaries, res.Summary)
		}
		if out.Model == "" {
			out.Model = res.Model
		}
	}

	out.Findings = SortFindings(out.Findings)
	out.Summary = strings.Join(summaries, "\n")
	out.Score = Score(out.Findings)
	return out, nil
}
